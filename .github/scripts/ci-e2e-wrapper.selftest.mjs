#!/usr/bin/env node
// ci-e2e-wrapper.selftest.mjs — 離線驗證 ci-e2e-wrapper.mjs 的行為契約。只
// 使用本目錄 __fixtures__/ 底下、自己持有的假子程序（小型 Node 腳本），不
// 呼叫真實 run-e2e.mjs／globalSetup，不啟動 App／browser。wrapper 邏輯原樣
// 沿用 PR #12（79819d8）已經過 #78/#82/#84 三輪反例修法的版本，歷史紅燈
// 證據見該 PR diff（存於
// /Users/eason_tseng/b3a-evidence/ci-1/attempt-001/pr12.diff）；本檔對
// 「修好之後」的行為做綠燈斷言，並額外驗證「不支援的輸出目標」與「寫入
// 失敗」兩個 decision470 明列的反例。
//
// 執行：node .github/scripts/ci-e2e-wrapper.selftest.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const WRAPPER = path.join(__dirname, 'ci-e2e-wrapper.mjs');
const FIXTURES = path.join(__dirname, '__fixtures__');

let passed = 0;
let failed = 0;
function check(name, fn) {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}
async function checkAsync(name, fn) {
  try {
    await fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

// selftest 自己的工作目錄放在本機持久 evidence 根目錄下，不用系統 /tmp
// （機器重開機會被清空；本輪任務明確要求證據放持久路徑）。
// review round 3（#480 R6）：可攜預設輸出——見
// __fixtures__/selftestWorkroot.mjs 的邏輯說明。
const SELFTEST_WORKROOT = ensureSelftestWorkRoot();
const EVIDENCE_ROOT = SELFTEST_WORKROOT.root;

function freshDir(label) {
  return mkdtempSync(path.join(EVIDENCE_ROOT, `${label}-`));
}

function runWrapperSync(dir, { deadline = 5, grace = 2, watchdogBuffer = 2, cmd, args = [] } = {}) {
  const statusFile = path.join(dir, 'status.json');
  const outFile = path.join(dir, 'out.txt');
  const tsOutFile = path.join(dir, 'ts-out.txt');
  const rcFile = path.join(dir, 'rc.txt');
  const wrapperArgs = [WRAPPER, statusFile, outFile, tsOutFile, rcFile, '--', cmd, ...args];
  const result = spawnSync('node', wrapperArgs, {
    env: {
      ...process.env,
      E2E_WRAPPER_DEADLINE_SECONDS: String(deadline),
      E2E_WRAPPER_GRACE_SECONDS: String(grace),
      E2E_WRAPPER_WATCHDOG_BUFFER_SECONDS: String(watchdogBuffer),
    },
    timeout: 30_000,
  });
  const status = existsSync(statusFile) ? JSON.parse(readFileSync(statusFile, 'utf8')) : null;
  const rc = existsSync(rcFile) ? readFileSync(rcFile, 'utf8').trim() : null;
  return { spawnRc: result.status, status, rc, statusFile, outFile, tsOutFile, rcFile };
}

// --- 正常完成，child exit 0 ---
check('child 正常 exit 0 → status=completed wrapperRc=0', () => {
  const dir = freshDir('exit0');
  const { spawnRc, status, rc } = runWrapperSync(dir, { cmd: 'node', args: [path.join(FIXTURES, 'exit-code.mjs'), '0'] });
  assert.equal(status.status, 'completed');
  assert.equal(status.wrapperRc, 0);
  assert.equal(status.childRc, 0);
  assert.equal(rc, '0');
  assert.equal(spawnRc, 0);
  rmSync(dir, { recursive: true, force: true });
});

// --- child 非零 rc ---
check('child exit 7（非零）→ wrapper 誠實回報 childRc=7 wrapperRc=7', () => {
  const dir = freshDir('exit7');
  const { status, rc } = runWrapperSync(dir, { cmd: 'node', args: [path.join(FIXTURES, 'exit-code.mjs'), '7'] });
  assert.equal(status.status, 'completed');
  assert.equal(status.childRc, 7);
  assert.equal(status.wrapperRc, 7);
  assert.equal(rc, '7');
  rmSync(dir, { recursive: true, force: true });
});

// --- deadline → TERM → child 忽略 → timeout-no-clean-exit（不得升級成 SIGKILL） ---
check('deadline 觸發、child 忽略 TERM → status=timeout-no-clean-exit wrapperRc=124（不送 SIGKILL）', () => {
  const dir = freshDir('ignoreterm');
  const { status, rc } = runWrapperSync(dir, {
    deadline: 1,
    grace: 1,
    watchdogBuffer: 5,
    cmd: 'node',
    args: [path.join(FIXTURES, 'ignore-term-then-exit.mjs'), '4000'],
  });
  assert.equal(status.status, 'timeout-no-clean-exit');
  assert.equal(status.wrapperRc, 124);
  assert.equal(status.signalSent, 'SIGTERM');
  assert.equal(status.childConfirmedGone, 'false', 'grace 到期時 child 尚未回報 exit，應誠實記成 false，不假裝已清理');
  assert.equal(rc, '124');
  rmSync(dir, { recursive: true, force: true });
});

// --- 不支援的輸出目標（目錄）→ 拒絕啟動，不 spawn child ---
check('OUT_FILE 是目錄 → 拒絕啟動（rc=2），不 spawn child，STATUS_FILE 記 rejected-unsupported-output-target', () => {
  const dir = freshDir('badtarget');
  const outDir = path.join(dir, 'out-is-a-dir');
  mkdirSync(outDir, { recursive: true });
  const statusFile = path.join(dir, 'status.json');
  const tsOutFile = path.join(dir, 'ts-out.txt');
  const rcFile = path.join(dir, 'rc.txt');
  const marker = path.join(dir, 'child-side-effect-marker');
  const result = spawnSync('node', [
    WRAPPER, statusFile, outDir, tsOutFile, rcFile, '--',
    'node', '-e', `require('fs').writeFileSync(${JSON.stringify(marker)}, 'child ran')`,
  ], { timeout: 10_000 });
  assert.equal(result.status, 2);
  assert.ok(existsSync(statusFile), 'STATUS_FILE 仍應寫出（OUT_FILE 才是有問題的那個目標）');
  const status = JSON.parse(readFileSync(statusFile, 'utf8'));
  assert.equal(status.status, 'rejected-unsupported-output-target');
  assert.equal(status.wrapperRc, 2);
  assert.ok(!existsSync(marker), 'child 不應該被 spawn，side-effect marker 不該出現');
  rmSync(dir, { recursive: true, force: true });
});

// --- 寫入失敗（RC_FILE 路徑的父層其實是一般檔案，ENOTDIR）→ producer-error ---
check('RC_FILE 寫入失敗（父路徑是檔案非目錄）→ status 升級為 producer-error，wrapperRc 非 0', () => {
  const dir = freshDir('writefail');
  const blockerFile = path.join(dir, 'blocker');
  writeFileSync(blockerFile, 'this is a file, not a directory');
  const rcFile = path.join(blockerFile, 'e2e.rc'); // 父層是檔案 → 寫入必然 ENOTDIR
  const statusFile = path.join(dir, 'status.json');
  const outFile = path.join(dir, 'out.txt');
  const tsOutFile = path.join(dir, 'ts-out.txt');
  const result = spawnSync('node', [
    WRAPPER, statusFile, outFile, tsOutFile, rcFile, '--',
    'node', path.join(FIXTURES, 'exit-code.mjs'), '0',
  ], { timeout: 10_000 });
  // rejectUnsupportedOutputTargets() 對「尚不存在的路徑」一律視為允許（lstat
  // 失敗），所以會照樣 spawn child；真正的寫入失敗發生在 finalize 階段。
  assert.ok(existsSync(statusFile), 'STATUS_FILE 應仍寫出（它自己的路徑沒問題）');
  const status = JSON.parse(readFileSync(statusFile, 'utf8'));
  assert.equal(status.status, 'producer-error');
  assert.notEqual(status.wrapperRc, 0, 'RC_FILE 寫不出去時 wrapperRc 不得是 0（不能假裝成功）');
  assert.ok(
    status.producerErrors.some((m) => m.includes('rc-file')),
    `producerErrors 應記錄 rc-file 寫入失敗，實際：${JSON.stringify(status.producerErrors)}`,
  );
  assert.equal(result.status, status.wrapperRc);
  rmSync(dir, { recursive: true, force: true });
});

// --- 外部訊號（SIGTERM，模擬 CI 取消）→ interrupted ---
await checkAsync('wrapper 自己收到 SIGTERM、轉送給 child、child 正常回應 → status=interrupted wrapperRc=143', async () => {
  const dir = freshDir('interrupted');
  const statusFile = path.join(dir, 'status.json');
  const outFile = path.join(dir, 'out.txt');
  const tsOutFile = path.join(dir, 'ts-out.txt');
  const rcFile = path.join(dir, 'rc.txt');
  const wrapperArgs = [
    WRAPPER, statusFile, outFile, tsOutFile, rcFile, '--',
    'node', path.join(FIXTURES, 'respond-to-term.mjs'), '10000',
  ];
  const child = spawn('node', wrapperArgs, {
    env: { ...process.env, E2E_WRAPPER_DEADLINE_SECONDS: '30', E2E_WRAPPER_GRACE_SECONDS: '10' },
  });
  const exitPromise = new Promise((resolve) => child.on('exit', (code) => resolve(code)));
  // 給 wrapper／child 一點時間把 SIGTERM handler 掛上去，再送訊號——不是
  // 猜測固定延遲就一定夠，只是離線 selftest 對真實子行程的合理等待。
  await new Promise((r) => setTimeout(r, 500));
  child.kill('SIGTERM');
  const exitCode = await Promise.race([
    exitPromise,
    new Promise((_, reject) => setTimeout(() => reject(new Error('wrapper 未在 10s 內對外部 SIGTERM 收尾')), 10_000)),
  ]);
  assert.equal(exitCode, 143);
  const status = JSON.parse(readFileSync(statusFile, 'utf8'));
  assert.equal(status.status, 'interrupted');
  assert.equal(status.wrapperRc, 143);
  assert.equal(status.externalSignalReceived, 'SIGTERM');
  rmSync(dir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
