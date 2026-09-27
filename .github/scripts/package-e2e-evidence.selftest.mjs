#!/usr/bin/env node
// package-e2e-evidence.selftest.mjs — 離線驗證 package-e2e-evidence.sh：
// 缺必要證據反例、封裝（manifest／tar）產出、cleanup（重跑不殘留舊檔）、
// 內容驗證（evaluator）判定失敗會讓封裝整體非零。全部用合成 fixture，不碰
// 真實 run-e2e／globalSetup／App／browser。
//
// 執行：node .github/scripts/package-e2e-evidence.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { makeGoodRunDir, suiteWithOneTest } from './__fixtures__/evidence-builder.mjs';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PACKAGE_SCRIPT = path.join(__dirname, 'package-e2e-evidence.sh');
const ENVELOPE_REPO_ROOT = envelopeRepoRoot();

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

// review round 3（#480 R6）：可攜預設輸出——見
// __fixtures__/selftestWorkroot.mjs 的邏輯說明。
const SELFTEST_WORKROOT = ensureSelftestWorkRoot();
const EVIDENCE_ROOT = SELFTEST_WORKROOT.root;
function freshDir(label) {
  return mkdtempSync(path.join(EVIDENCE_ROOT, `${label}-`));
}

function runPackage(workdir, runDir, entryId) {
  // review round 4（#480 R2 剩餘缺口）：evaluate-e2e-evidence.mjs 的
  // validateEnvelope() 會現場執行 `git -C <CI_E2E_REPO_ROOT> rev-parse
  // HEAD`；workdir 是系統暫存目錄底下的 freshDir()，不巢狀在任何 repo
  // 裡，必須顯式傳遞 CI_E2E_REPO_ROOT，否則 git 會找不到 repo。
  const result = spawnSync('bash', [PACKAGE_SCRIPT, workdir, runDir ?? '', entryId ?? ''], {
    encoding: 'utf8',
    timeout: 15_000,
    // review round 6（#489 F3）：見 evaluate-e2e-evidence.selftest.mjs 同名
    // env var 的頭註。
    env: { ...process.env, CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT, CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0' },
  });
  return { rc: result.status, stdout: result.stdout, stderr: result.stderr };
}

// review round 4（#480 R2 剩餘缺口）：跟 makeGoodRunDir() 產生的 runDir 配
// 成一對，寫一份合法、蓋好 entry 專屬戳記的 envelope.json 到 workdir，讓
// package-e2e-evidence.sh 內部呼叫的 evaluator 能通過 R2 交叉核對——package
// script 本身不知道「entry」的概念（只認 workdir/run-dir/entry-id 三個 CLI
// 參數），envelope 的蓋章邏輯正常是 run-batch.mjs 的職責，這裡在 selftest
// 裡手動模擬同一件事。
function writeGreenEnvelope(workdir, entryId, runDir) {
  const stamped = makeStampedEnvelope(entryById(entryId), path.basename(runDir), {}, { repoRoot: ENVELOPE_REPO_ROOT });
  writeFileSync(path.join(workdir, 'envelope.json'), JSON.stringify(stamped, null, 2));
}

// --- 缺必要證據（e2e.out/e2e.rc/e2e-wrapper-status.json 都不存在）---
check('workdir 完全空 → missing 必要證據，exit 非零', () => {
  const workdir = freshDir('pkgmissing');
  const { rc, stdout } = runPackage(workdir, '', 'default');
  assert.notEqual(rc, 0);
  assert.match(stdout, /必要證據缺漏/);
  rmSync(workdir, { recursive: true, force: true });
});

// --- 全綠：封裝成功，manifest／tar 都產生且內容非空 ---
check('完整正確證據（default）→ exit 0，manifest／sha256／tar 都產生', () => {
  const workdir = freshDir('pkggreen');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({ status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [] }, null, 2));
  const artifactsRoot = path.join(workdir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-pkg-001', {
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  writeGreenEnvelope(workdir, 'default', runDir);
  const { rc, stdout } = runPackage(workdir, runDir, 'default');
  assert.equal(rc, 0, stdout);
  assert.ok(existsSync(path.join(workdir, 'e2e-evidence.tar.gz')));
  assert.ok(existsSync(path.join(workdir, 'e2e-evidence-manifest.txt')));
  assert.ok(existsSync(path.join(workdir, 'e2e-evidence-manifest.sha256')));
  const manifest = readFileSync(path.join(workdir, 'e2e-evidence-manifest.txt'), 'utf8');
  assert.match(manifest, /run-state\.json/);
  assert.match(manifest, /playwright-results\.json/);
  // review round 4（#480 R2 剩餘缺口）：envelope.json 要跟其他證據一起打包
  // （decision.md：「這份JSON要和證據一起打包、上傳」），不是只留在 workdir
  // 沒進 e2e-evidence-package/manifest/tar。
  assert.match(manifest, /envelope\.json/);
  assert.ok(existsSync(path.join(workdir, 'e2e-evidence-package', 'envelope.json')), 'envelope.json 應該被複製進 e2e-evidence-package/');
  const sha256 = readFileSync(path.join(workdir, 'e2e-evidence-manifest.sha256'), 'utf8');
  assert.ok(sha256.trim().length > 0, 'sha256 manifest 不應為空');
  rmSync(workdir, { recursive: true, force: true });
});

// --- cleanup：重跑不殘留舊檔（先塞一個不該存在的檔案進 e2e-evidence-package） ---
check('cleanup：第二次執行不殘留第一次留下的多餘檔案', () => {
  const workdir = freshDir('pkgcleanup');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({ status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [] }, null, 2));
  const artifactsRoot = path.join(workdir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-pkg-002', {
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  writeGreenEnvelope(workdir, 'default', runDir);
  runPackage(workdir, runDir, 'default');
  // 手動塞一個「上一輪遺留」的多餘檔案，模擬舊 run 沒清乾淨的情境。
  writeFileSync(path.join(workdir, 'e2e-evidence-package', 'stale-leftover-from-previous-run.txt'), 'should not survive rerun');
  const { rc } = runPackage(workdir, runDir, 'default');
  assert.equal(rc, 0);
  assert.ok(
    !existsSync(path.join(workdir, 'e2e-evidence-package', 'stale-leftover-from-previous-run.txt')),
    'package-e2e-evidence.sh 開頭的 rm -rf e2e-evidence-package 應該清掉上一輪殘留',
  );
  rmSync(workdir, { recursive: true, force: true });
});

// --- 內容驗證失敗（evaluator 判定不可信）→ 封裝整體非零 ---
check('e2e-wrapper-status.json 壞掉（evaluator 判定不可信）→ package script 整體 exit 非零', () => {
  const workdir = freshDir('pkgcontentfail');
  writeFileSync(path.join(workdir, 'e2e.out'), 'stdout line\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), 'not json');
  writeFileSync(path.join(workdir, 'new-run-dirs.json'), '[]');
  const { rc, stdout } = runPackage(workdir, '', 'default');
  assert.notEqual(rc, 0);
  assert.match(stdout, /內容驗證.*判定證據缺漏或不可信/);
  rmSync(workdir, { recursive: true, force: true });
});

// --- 缺 entry-id 參數 → 明確 fail，不悄悄跳過內容驗證 ---
check('缺 entry-id 參數 → exit 非零，不悄悄跳過內容驗證', () => {
  const workdir = freshDir('pkgnoentry');
  writeFileSync(path.join(workdir, 'e2e.out'), 'x\n');
  writeFileSync(path.join(workdir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(workdir, 'e2e-wrapper-status.json'), JSON.stringify({ status: 'completed', wrapperRc: 0 }));
  const result = spawnSync('bash', [PACKAGE_SCRIPT, workdir], { encoding: 'utf8', timeout: 15_000 });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /缺少 entry-id 參數/);
  rmSync(workdir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
