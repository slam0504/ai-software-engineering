#!/usr/bin/env node
// package-e2e-evidence.review497.selftest.mjs — reviewer 裁定
// `/Users/eason_tseng/b3a-evidence/2026-09-28-review497/decision.md` §B-2／
// §B-3 的離線回歸：verdict.json 的 `overall` 先前只憑封裝／readback（＋
// evaluator exit code）決定，完全不看 wrapper／child 是否真的執行成功。
//
// 真實反例（PR26 attempt1、run 36347781855、artifact 10941635782、controls job）：
// e2e-wrapper-status.json 是 wrapperRc=1／childRc=1／status="completed"，
// 打包／readback 全部成功，但先前 verdict.json 仍寫
// `overall:"passed"`、`exitCode:0`——見
// `/Users/eason_tseng/b3a-evidence/ci-2/attempt-001/artifact/x/20260927T202458Z-0f94a4/controls/verdict.json`。
//
// 本檔驗證修法後 package-e2e-evidence.sh 新增的 executionOutcome／
// packageStatus／packagingExitCode／contentValidationScope／overall 五個
// verdict.json 欄位，涵蓋 decision497 §B-4 點名的五種情境（各自驗最終語
// 意）＋ hidden paths 最小 fixture／workflow 設定檢查（workflow 設定檢查見
// workflow-and-syntax.selftest.mjs，這裡只做 package 腳本自己的封裝層 hidden
// paths 保留驗證）。全部用合成或 __fixtures__/real/ 底下的真實樣本，不碰
// 真實 run-e2e／globalSetup／App／browser。
//
// 執行：node .github/scripts/package-e2e-evidence.review497.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { makeGoodRunDir, suiteWithOneTest } from './__fixtures__/evidence-builder.mjs';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PACKAGE_SCRIPT = path.join(__dirname, 'package-e2e-evidence.sh');
const ENVELOPE_REPO_ROOT = envelopeRepoRoot();
const REAL_CONTROLS_FAILED_PACKAGED_FIXTURE = path.join(
  __dirname, '__fixtures__', 'real', 'controls-execution-failed-packaged-20260927T202728Z-151ce1',
);

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

const SELFTEST_WORKROOT = ensureSelftestWorkRoot();
const EVIDENCE_ROOT = SELFTEST_WORKROOT.root;
function freshDir(label) {
  return mkdtempSync(path.join(EVIDENCE_ROOT, `${label}-`));
}

function runPackage(workdir, runDir, entryId) {
  const result = spawnSync('bash', [PACKAGE_SCRIPT, workdir, runDir ?? '', entryId ?? ''], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT, CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0' },
  });
  return { rc: result.status, stdout: result.stdout, stderr: result.stderr };
}

function readVerdict(workdir) {
  const p = path.join(workdir, 'verdict.json');
  assert.ok(existsSync(p), `verdict.json 應該存在（${p}）`);
  return JSON.parse(readFileSync(p, 'utf8'));
}

function writeGreenEnvelope(workdir, entryId, runDir) {
  const stamped = makeStampedEnvelope(entryById(entryId), path.basename(runDir), {}, { repoRoot: ENVELOPE_REPO_ROOT });
  writeFileSync(path.join(workdir, 'envelope.json'), JSON.stringify(stamped, null, 2));
}

// --- 情境 1／5：default 成功（executionOutcome=success，overall=passed） ---
check('decision497 §B-4：default 執行成功且打包成功 → verdict.overall=passed，executionOutcome=success，packageStatus=ok，contentValidationScope=full', () => {
  const workdir = freshDir('r497-success');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [],
  }, null, 2));
  const artifactsRoot = path.join(workdir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-r497-success', {
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  writeGreenEnvelope(workdir, 'default', runDir);
  const { rc, stdout } = runPackage(workdir, runDir, 'default');
  assert.equal(rc, 0, stdout);
  const verdict = readVerdict(workdir);
  assert.equal(verdict.executionOutcome, 'success', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.packageStatus, 'ok', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.packagingExitCode, 0);
  assert.equal(verdict.contentValidationScope, 'full');
  assert.equal(verdict.overall, 'passed');
  rmSync(workdir, { recursive: true, force: true });
});

// --- 情境 2：controls 執行失敗但打包成功（decision497 核心反例，用真實下載
//     樣本，不是合成 fixture——見
//     __fixtures__/real/controls-execution-failed-packaged-20260927T202728Z-151ce1
//     與同目錄 PROVENANCE.md）---
check('decision497 §B-2 核心反例（真實樣本）：controls wrapperRc=1／childRc=1，打包/readback/內容驗證全部成功，verdict.overall 仍必須是 failed（先前錯誤地寫 passed，見真實下載副本 attempt-001/artifact/x/…/controls/verdict.json）', () => {
  const workdir = freshDir('r497-exec-failed-packaged');
  writeFileSync(path.join(workdir, 'e2e.out'), 'fake stdout（僅供 missing 檔案檢查用，不影響判定）\n');
  // 以下 e2e.rc／e2e-wrapper-status.json 的數值是這次真實下載副本的原文
  // （見 PROVENANCE.md），不是憑空編造。
  writeFileSync(path.join(workdir, 'e2e.rc'), '1\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'completed',
    startIso: '2026-09-27T20:27:28.022Z',
    endIso: '2026-09-27T20:28:34.772Z',
    elapsedSeconds: 67,
    deadlineSeconds: 1500,
    graceSeconds: 120,
    watchdogSeconds: 1650,
    childPid: 43281,
    childCommandAtSpawn: 'node frontend/e2e/scripts/run-e2e.mjs e2e/playwright.controls.config.ts',
    childRc: 1,
    childSignal: null,
    wrapperRc: 1,
    signalSent: null,
    signalReason: null,
    externalSignalReceived: null,
    childConfirmedGone: 'true',
    producerErrors: [],
    rawLineCount: 66,
    timedLineCount: 66,
  }, null, 2));
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([REAL_CONTROLS_FAILED_PACKAGED_FIXTURE], null, 2));
  const { rc, stdout } = runPackage(workdir, REAL_CONTROLS_FAILED_PACKAGED_FIXTURE, 'controls');
  // package 腳本自己的 process exit code 允許是 0（decision497 §B-3：
  // 「package腳本可以成功封存失敗run」——封裝/readback 本身確實做好了）。
  assert.equal(rc, 0, `封裝／readback 本身應該成功（stdout=${stdout}）`);
  const verdict = readVerdict(workdir);
  assert.equal(verdict.executionOutcome, 'failed', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.packageStatus, 'ok', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.packagingExitCode, 0, JSON.stringify(verdict, null, 2));
  assert.equal(verdict.contentValidationScope, 'packaging-only', JSON.stringify(verdict, null, 2));
  // 核心斷言：即使封裝／readback／內容驗證都通過，overall 也不得是 passed。
  assert.equal(verdict.overall, 'failed', `verdict=${JSON.stringify(verdict, null, 2)}`);
  assert.ok(
    existsSync(path.join(workdir, 'e2e-evidence-package', 'validation-scope.json')),
    'evaluate-e2e-evidence.mjs 應該在非成功宣稱但仍有 run 目錄的分支落地 validation-scope.json',
  );
  rmSync(workdir, { recursive: true, force: true });
});

// --- 情境 3：NO-RUN（wrapper 沒觀察到任何 run 目錄）---
check('decision497 §B-4：NO-RUN → executionOutcome=no-run，contentValidationScope=no-run，overall=failed', () => {
  const workdir = freshDir('r497-no-run');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '1\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'timeout', wrapperRc: 1, childRc: null, childConfirmedGone: 'unknown', producerErrors: [],
  }, null, 2));
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), '[]');
  const { rc } = runPackage(workdir, '', 'default');
  assert.notEqual(rc, 0, 'NO-RUN 本身仍是失敗，package 腳本自己的 exit code 也要非零（decision470 既有裁定，本輪不變）');
  const verdict = readVerdict(workdir);
  assert.equal(verdict.executionOutcome, 'no-run', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.contentValidationScope, 'no-run', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.overall, 'failed');
  rmSync(workdir, { recursive: true, force: true });
});

// --- 情境 4：packaging/readback 失敗（即使 wrapper/child 看起來成功，封裝
//     失敗也必須 overall=failed）---
check('decision497 §B-4：封裝階段失敗（workdir 本身不可寫）→ 即使 wrapper/child 看起來成功，overall 仍必須是 failed', () => {
  const workdir = freshDir('r497-pkg-fail');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [],
  }, null, 2));
  const artifactsRoot = path.join(workdir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-r497-pkgfail', {
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  writeGreenEnvelope(workdir, 'default', runDir);
  // workdir 本身不可寫（0o555）：package-e2e-evidence.sh 的
  // `mkdir -p e2e-evidence-package` 會失敗，後續每個 copy_if_present／
  // manifest／tar 步驟都會連鎖失敗，強迫 packaging_errors 非空——不依賴任何
  // 特定檔案層級的 cp 行為細節，是最直接、平台無關的封裝失敗誘因。
  chmodSync(workdir, 0o555);
  let rc;
  let verdict;
  try {
    const result = runPackage(workdir, runDir, 'default');
    rc = result.rc;
    if (existsSync(path.join(workdir, 'verdict.json'))) {
      verdict = readVerdict(workdir);
    }
  } finally {
    chmodSync(workdir, 0o755); // 還原可寫，否則 rmSync 清不掉
  }
  assert.notEqual(rc, 0, 'workdir 不可寫時，封裝腳本自己也必須非零結束');
  if (verdict) {
    assert.equal(verdict.packageStatus, 'failed', JSON.stringify(verdict, null, 2));
    assert.equal(verdict.overall, 'failed', JSON.stringify(verdict, null, 2));
  }
  rmSync(workdir, { recursive: true, force: true });
});

// --- 情境 5：wrapper 資料缺漏（e2e-wrapper-status.json 不存在）---
check('decision497 §B-4：e2e-wrapper-status.json 缺漏 → missingWrapperFiles>=1，executionOutcome=unknown，overall=failed（不得因為 evaluator 對缺檔讓步就被誤判成功）', () => {
  const workdir = freshDir('r497-missing-status');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  // 刻意不寫 e2e-wrapper-status.json。
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), '[]');
  const { rc } = runPackage(workdir, '', 'default');
  assert.notEqual(rc, 0);
  const verdict = readVerdict(workdir);
  assert.ok(verdict.missingWrapperFiles >= 1, JSON.stringify(verdict, null, 2));
  assert.equal(verdict.executionOutcome, 'unknown', JSON.stringify(verdict, null, 2));
  assert.equal(verdict.overall, 'failed');
  rmSync(workdir, { recursive: true, force: true });
});

// --- hidden paths 最小 fixture：node_modules/.bin/x、.last-run.json 這類隱
//     藏路徑要在「本腳本自己的」cp -Rf／tar／readback 這一段保留下來（跟
//     workflow 層 actions/upload-artifact 的 include-hidden-files 是兩件不
//     同的事，見 workflow-and-syntax.selftest.mjs 的對應檢查）---
check('decision497 §B-4：hidden paths 最小 fixture（node_modules/.bin/x、.last-run.json）在 cp -Rf／tar／readback 全程保留，不被封裝腳本自己漏掉', () => {
  const workdir = freshDir('r497-hidden-paths');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [],
  }, null, 2));
  const artifactsRoot = path.join(workdir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-r497-hidden', {
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeFileSync(path.join(runDir, '.last-run.json'), JSON.stringify({ status: 'passed' }));
  mkdirSync(path.join(runDir, 'node_modules', '.bin'), { recursive: true });
  writeFileSync(path.join(runDir, 'node_modules', '.bin', 'x'), '#!/bin/sh\necho hidden\n');
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  writeGreenEnvelope(workdir, 'default', runDir);
  const { rc, stdout } = runPackage(workdir, runDir, 'default');
  assert.equal(rc, 0, stdout);
  const manifest = readFileSync(path.join(workdir, 'e2e-evidence-manifest.txt'), 'utf8');
  assert.match(manifest, /artifacts\/\.last-run\.json/, `manifest 應含隱藏檔 .last-run.json：${manifest}`);
  assert.match(manifest, /artifacts\/node_modules\/\.bin\/x/, `manifest 應含 node_modules/.bin/x：${manifest}`);
  const verdict = readVerdict(workdir);
  assert.equal(verdict.readbackOk, true, 'readback 比對（tar 解包後逐檔 sha256）應該連隱藏路徑一起核對過');
  assert.equal(verdict.overall, 'passed');
  rmSync(workdir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
