#!/usr/bin/env node
// evaluate-e2e-evidence.real-fixtures.selftest.mjs — B3a-CI-1 review round 2
// （主 agent 用真實 run 目錄複核 CI-1 後要求補上的測試）。
//
// 先前 evaluate-e2e-evidence.selftest.mjs 只用合成 fixture（手動組出的
// playwright-results.json）驗證 evaluator，格式假設本身就是猜的——四個真實
// 通過的 controls／gate1／gate2／stale run 因此全部被誤判失敗（詳見
// /Users/eason_tseng/b3a-evidence/ci-1/attempt-001/main-review/real-evidence-probe.log
// 的第一輪紅燈結果）。本檔改用 `__fixtures__/real/` 底下**逐位元組複製**的
// 真實 run 目錄（來源與 hash 見同目錄 `PROVENANCE.md`）：
//   - 四個真實正例（controls／gate1／gate2／stale）必須通過（rc=0）。
//   - 真實 gate2 的 run 被拿來冒充 gate1 必須被拒絕（identity 異常，理由
//     正確：預期 spec 缺席＋出現額外 spec，兩者都指名 gate1 vs gate2 的實
//     際檔名差異，不是格式湊巧對不上）。
//   - 對真實 controls 樣本做最小變異（只改 JSON 裡的判定欄位，其餘位元組
//     不動）：control A 意外 pass、control A 錯誤訊息被替換、reverse-check
//     被執行——三者都必須被拒絕。
//
// 每個 case 都會把真實目錄複製到本次獨立的工作目錄（不動 __fixtures__/real/
// 底下的原始複製品，更不會動最原始的 worktree artifacts）。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.real-fixtures.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const EVALUATOR = path.join(__dirname, 'evaluate-e2e-evidence.mjs');
const REAL_FIXTURES_ROOT = path.join(__dirname, '__fixtures__', 'real');
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

function findRealFixtureDir(prefix) {
  const name = readdirSync(REAL_FIXTURES_ROOT).find((n) => n.startsWith(prefix));
  if (!name) throw new Error(`找不到 __fixtures__/real/ 底下以 "${prefix}" 開頭的目錄`);
  return path.join(REAL_FIXTURES_ROOT, name);
}

/**
 * 複製一份真實 run 目錄到本次工作區，並準備 evaluator 需要的 wrapper 輸出
 * （status.json／rc／new-run-dirs.json／package 副本），回傳 { caseDir,
 * runCopy, packageDir }。`mutate` 可選：在複製後、呼叫 evaluator 前對複製
 * 品的 playwright-results.json 做最小修改（只改判定欄位，其餘不動）。
 */
// review round 4（#480 R2 剩餘缺口）：entryId 預設從 fixturePrefix 推回
// （'gate1-pass'→'gate1'、'controls-pass'→'controls'……跟這個檔案既有的
// 命名慣例一致），用來蓋出一份跟這次評估對象相符的合法 envelope.json；呼
// 叫端也可以顯式傳 entryId 覆寫（例如「拿 gate2 的證據冒充 gate1」測試裡，
// envelope 仍然照實蓋 gate1，因為 production 流程中 envelope 是
// run-batch.mjs 依「正在跑的那個 entry」蓋章，不會跟著被冒充的證據一起
// 偽造）。`skipEnvelope: true` 明確不寫（供未來反例使用）。
function prepareRealCase(label, fixturePrefix, {
  wrapperRc = 0,
  wrapperStatus = 'completed',
  mutate = null,
  entryId = fixturePrefix.replace(/-(pass|failed)$/, ''),
  envelopeOverrides = {},
  skipEnvelope = false,
} = {}) {
  const src = findRealFixtureDir(fixturePrefix);
  const runIdName = path.basename(src).replace(/^[a-z0-9]+-(pass|failed)-/, '');
  const caseDir = freshDir(label);
  const runCopy = path.join(caseDir, 'artifacts-root', runIdName);
  cpSync(src, runCopy, { recursive: true });

  if (mutate) {
    const resultsFile = path.join(runCopy, 'playwright-results.json');
    const report = JSON.parse(readFileSync(resultsFile, 'utf8'));
    mutate(report);
    writeFileSync(resultsFile, JSON.stringify(report, null, 2));
  }

  writeFileSync(
    path.join(caseDir, 'e2e-wrapper-status.json'),
    JSON.stringify({
      status: wrapperStatus, wrapperRc, childRc: wrapperRc, childConfirmedGone: 'true', producerErrors: [],
      startIso: '2026-09-28T00:00:00.000Z', endIso: '2026-09-28T00:10:00.000Z',
    }, null, 2),
  );
  writeFileSync(path.join(caseDir, 'e2e.rc'), `${wrapperRc}\n`);
  writeFileSync(path.join(caseDir, 'new-run-dirs.json'), JSON.stringify([runCopy], null, 2));

  if (!skipEnvelope) {
    const stamped = makeStampedEnvelope(entryById(entryId), runIdName, envelopeOverrides, { repoRoot: ENVELOPE_REPO_ROOT });
    writeFileSync(path.join(caseDir, 'envelope.json'), JSON.stringify(stamped, null, 2));
  }

  const packageDir = path.join(caseDir, 'package');
  mkdirSync(path.join(packageDir, 'artifacts'), { recursive: true });
  for (const f of ['e2e-wrapper-status.json', 'e2e.rc', 'new-run-dirs.json', 'envelope.json']) {
    const fSrc = path.join(caseDir, f);
    if (existsSync(fSrc)) cpSync(fSrc, path.join(packageDir, f));
  }
  cpSync(runCopy, path.join(packageDir, 'artifacts'), { recursive: true });

  return { caseDir, runCopy, packageDir };
}

function runEvaluatorOn(caseDir, packageDir, entryId) {
  const result = spawnSync('node', [EVALUATOR, caseDir, packageDir, entryId], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT,
      // review round 6（#489 F3）：見 evaluate-e2e-evidence.selftest.mjs 同名
      // env var 的頭註。
      CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0',
    },
  });
  return { rc: result.status, stdout: result.stdout, stderr: result.stderr };
}

// --- 四個真實正例必須通過 ---
for (const [entryId, prefix] of [
  ['controls', 'controls-pass'],
  ['gate1', 'gate1-pass'],
  ['gate2', 'gate2-pass'],
  ['stale', 'stale-pass'],
]) {
  check(`真實 ${entryId} 正例（__fixtures__/real/${prefix}-...）→ exit 0`, () => {
    const { caseDir, packageDir } = prepareRealCase(`real-${entryId}`, prefix);
    const { rc, stdout } = runEvaluatorOn(caseDir, packageDir, entryId);
    assert.equal(rc, 0, `預期 exit 0，實際 stdout:\n${stdout}`);
    rmSync(caseDir, { recursive: true, force: true });
  });
}

// --- 真實 gate2 的 run 冒充 gate1：identity 異常，理由要指名兩個真實檔名 ---
check('真實 gate2 run 冒充 gate1 → identity 異常，exit 1，理由指名 gates/gate1.spec.ts 缺席與 gates/gate2.spec.ts 額外', () => {
  const { caseDir, packageDir } = prepareRealCase('real-gate2-as-gate1', 'gate2-pass');
  const { rc, stdout } = runEvaluatorOn(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /預期 spec 檔缺席（identity 異常）：gates\/gate1\.spec\.ts/);
  assert.match(stdout, /出現非預期的 spec 檔（identity 異常／額外案）：gates\/gate2\.spec\.ts/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 真實 controls 樣本變異：control A 意外 pass ---
check('真實 controls 樣本變異：control A 被改成 unexpected/passed → oracle 拒絕', () => {
  const { caseDir, packageDir } = prepareRealCase('real-controls-Aunexpected', 'controls-pass', {
    mutate(report) {
      const suite = report.suites.find((s) => s.file === 'save-detection.spec.ts');
      const controlA = suite.specs.find((s) => s.title.includes('control A'));
      controlA.tests[0].status = 'unexpected';
      controlA.tests[0].results[0].status = 'passed';
      delete controlA.tests[0].results[0].error;
      controlA.tests[0].results[0].errors = [];
      report.stats.unexpected = 1;
      report.stats.expected = Math.max(0, (report.stats.expected ?? 1) - 1);
    },
  });
  const { rc, stdout } = runEvaluatorOn(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /control A 意外 pass/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 真實 controls 樣本變異：control A 錯誤訊息被替換 ---
check('真實 controls 樣本變異：control A 的錯誤訊息被替換成不相關錯誤 → oracle 拒絕（不是任何錯誤都接受）', () => {
  const { caseDir, packageDir } = prepareRealCase('real-controls-Awrongerror', 'controls-pass', {
    mutate(report) {
      const suite = report.suites.find((s) => s.file === 'save-detection.spec.ts');
      const controlA = suite.specs.find((s) => s.title.includes('control A'));
      const fakeMsg = 'Error: locator.click: Timeout 15000ms exceeded waiting for [data-test="save"]';
      controlA.tests[0].results[0].error = { message: fakeMsg };
      controlA.tests[0].results[0].errors = [{ message: fakeMsg }];
    },
  });
  const { rc, stdout } = runEvaluatorOn(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /正規化核對後不是恰好一筆目標斷言錯誤/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 真實 controls 樣本變異：reverse-check 被執行 ---
check('真實 controls 樣本變異：reverse-check 被改成有執行且通過 → oracle 拒絕', () => {
  const { caseDir, packageDir } = prepareRealCase('real-controls-reverseran', 'controls-pass', {
    mutate(report) {
      const suite = report.suites.find((s) => s.file === 'reverse-check.spec.ts');
      const test = suite.specs[0].tests[0];
      test.status = 'expected';
      test.expectedStatus = 'passed';
      test.results[0].status = 'passed';
      report.stats.skipped = 0;
      report.stats.expected = (report.stats.expected ?? 2) + 1;
    },
  });
  const { rc, stdout } = runEvaluatorOn(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /reverse-check\.spec\.ts 狀態不一致或非預期執行/);
  rmSync(caseDir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
