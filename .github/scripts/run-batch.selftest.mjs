#!/usr/bin/env node
// run-batch.selftest.mjs — 離線驗證 run-batch.mjs 的批次序列邏輯：before/
// after 快照 diff、逐案 wrapper→package→evaluate、任一失敗停止同批次後續。
// 用 CI_E2E_TEST_RUNNER 測試鉤子換成 __fixtures__/fake-runner.mjs（見該檔
// 頭），完全不碰真實 run-e2e.mjs／globalSetup／App／browser。
//
// 執行：node .github/scripts/run-batch.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { makeBaseEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const RUN_BATCH = path.join(__dirname, 'run-batch.mjs');
const FAKE_RUNNER = path.join(__dirname, '__fixtures__', 'fake-runner.mjs');
const REPO_ROOT = path.resolve(__dirname, '..', '..');

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

// review round 4（#480 R2 剩餘缺口）：run-batch.mjs 現在要求一份 job-level
// envelope.json（見 run-batch.mjs 開頭的 envelopeError 檢查）。`envelope:
// 'auto'`（預設）自動寫一份合法的 job-level envelope 到
// CI_E2E_ENVELOPE_PATH，讓既有測試不需要逐一修改就能繼續代表「envelope 齊
// 全且正確」；`envelope: null` 明確不寫（測「缺檔」反例）；`envelope:
// {...}` 直接用呼叫端提供的內容覆寫（測 schema 反例）。
function runBatch(batchName, {
  behaviorMap = {}, deadline = 5, grace = 2, extraEnv = {}, envelope = 'auto',
} = {}) {
  const base = freshDir(`runbatch-${batchName}`);
  const workRoot = path.join(base, 'work');
  const artifactsRoot = path.join(base, 'artifacts-root');
  mkdirSync(workRoot, { recursive: true });
  mkdirSync(artifactsRoot, { recursive: true });

  const envelopePath = path.join(base, 'envelope.json');
  if (envelope === 'auto') {
    writeFileSync(envelopePath, JSON.stringify(makeBaseEnvelope({ repoRoot: REPO_ROOT }), null, 2));
  } else if (envelope !== null) {
    writeFileSync(envelopePath, JSON.stringify(envelope, null, 2));
  }
  // envelope === null：明確不寫，測「job-level envelope 缺檔」這條路徑。

  const result = spawnSync('node', [RUN_BATCH, batchName], {
    cwd: REPO_ROOT,
    env: {
      ...process.env,
      CI_E2E_REPO_ROOT: REPO_ROOT,
      CI_E2E_WORK_ROOT: workRoot,
      CI_E2E_ARTIFACTS_ROOT: artifactsRoot,
      CI_E2E_ENVELOPE_PATH: envelopePath,
      CI_E2E_OFFLINE_SELFTEST: '1',
      CI_E2E_TEST_RUNNER: FAKE_RUNNER,
      CI_E2E_ATTEMPT_ID: 'selftest-attempt',
      // review round 6（#489 F3）：見 evaluate-e2e-evidence.selftest.mjs 同名
      // env var 的頭註——envelope fixture 的 nodeVersionActual 固定用核定
      // 版本，本機 Node 可能不同，這裡明確隔離告知比對基準。
      CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0',
      FAKE_RUNNER_BEHAVIOR: JSON.stringify(behaviorMap),
      E2E_WRAPPER_DEADLINE_SECONDS_OVERRIDE: String(deadline),
      E2E_WRAPPER_GRACE_SECONDS_OVERRIDE: String(grace),
      ...extraEnv,
    },
    encoding: 'utf8',
    timeout: 60_000,
  });
  const workAttemptRoot = path.join(workRoot, 'selftest-attempt');
  const summaryFile = path.join(workAttemptRoot, 'batch-summary.json');
  const summary = existsSync(summaryFile) ? JSON.parse(readFileSync(summaryFile, 'utf8')) : null;
  return { spawnRc: result.status, stdout: result.stdout, stderr: result.stderr, summary, base, workAttemptRoot };
}

// --- 全綠：smoke 批次兩案都 pass ---
check('smoke 批次：default/controls 都 pass → overall=passed，兩案都 passed', () => {
  const { spawnRc, summary } = runBatch('smoke', { behaviorMap: { default: 'pass', controls: 'pass' } });
  assert.equal(spawnRc, 0, JSON.stringify(summary, null, 2));
  assert.equal(summary.overall, 'passed');
  assert.equal(summary.stoppedEarly, false);
  assert.deepEqual(summary.entries.map((e) => e.status), ['passed', 'passed']);
});

// --- 第一案失敗（子行程非零 rc）→ 停止，第二案 not-run ---
check('gates 批次：gate1 子行程非零 rc → gate1 failed，gate2/stale 都 not-run，overall=failed', () => {
  const { summary } = runBatch('gates', { behaviorMap: { gate1: 'fail-rc' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'failed');
  assert.equal(byId.gate2, 'not-run');
  assert.equal(byId.stale, 'not-run');
  assert.equal(summary.overall, 'failed');
  assert.equal(summary.stoppedAt, 'gate1');
});

// --- 中間案失敗（不是第一案）→ 前面案例維持 passed，之後停止 ---
check('gates 批次：gate2（第二案）失敗 → gate1 仍 passed，stale not-run', () => {
  const { summary } = runBatch('gates', { behaviorMap: { gate1: 'pass', gate2: 'fail-rc' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'passed');
  assert.equal(byId.gate2, 'failed');
  assert.equal(byId.stale, 'not-run');
});

// --- 宣稱成功但無證據（exit 0 卻沒建 run 目錄）→ 判定失敗，停止 ---
check('smoke 批次：default 宣稱成功但沒有任何新 run 目錄 → failed，controls not-run', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'no-evidence-pass' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
});

// --- 重複案（identity 異常：同一 entry 產生 2 個新 run 目錄）→ 判定失敗 ---
check('smoke 批次：default 產生 2 個新 run 目錄（重複/多案）→ identity 異常，failed', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'duplicate' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
});

// --- wrapper 逾時（child 忽略 TERM）串接進 run-batch → 判定失敗，停止 ---
check('smoke 批次：default 忽略 TERM 導致 wrapper timeout-no-clean-exit → failed，controls not-run', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'ignore-term' }, deadline: 1, grace: 1 });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
});

// --- review round 2：真實 gate2 首跑失敗樣本串接進 run-batch → 批次因
//     wrapperSpawnRc 非零而停止，不是靠 evaluator 抓到內容問題。這是既有
//     設計（decision470：「測試失敗時仍盡可能打包原始證據」），這裡用真實
//     failed 樣本（__fixtures__/real/gate2-failed-20260925T071019Z-146267，
//     harness.log 最終結果：FAILED）而不是合成的 rc=1 佐證。 ---
check('gates 批次：真實 gate2 首跑失敗樣本（fake-runner real-gate2-failed）→ gate2 failed（因 wrapperSpawnRc 非零），stale not-run，gate1 仍 passed', () => {
  const { summary } = runBatch('gates', { behaviorMap: { gate1: 'pass', gate2: 'real-gate2-failed' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'passed');
  assert.equal(byId.gate2, 'failed');
  assert.equal(byId.stale, 'not-run');
  const gate2Result = summary.entries.find((e) => e.id === 'gate2');
  assert.equal(gate2Result.wrapperRc, 1, '批次判定失敗的依據是 wrapper 子行程本身的 exit code（真實 rc=1），不是 evaluator 額外挑出的問題');
});

// --- review round 3（R1）：對照 reviewer #480 probe-batch.py 反例——
//     default／controls 都帶 TEST_FAILED，先前整批仍 passed/rc0、不停止。
//     現在兩案都用 'pass-then-test-failed' behavior，驗證第一案（default）
//     就會被判定失敗，批次立刻停止，controls 變成 not-run。 ---
check('reviewer#480 batch 反例：smoke 批次 default/controls 都帶 TEST_FAILED → default failed，controls not-run，overall=failed', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'pass-then-test-failed', controls: 'pass-then-test-failed' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
  assert.equal(summary.overall, 'failed');
  assert.equal(summary.stoppedEarly, true);
});

// --- review round 3（R5）：spawn 前 env 邊界——ambient env 帶有這個 entry
//     不該有的 E2E_GATE，必須拒絕 spawn，不是靜默沿用或覆寫。 ---
check('R5：env 污染（ambient E2E_GATE 對 default entry 不該存在）→ 拒絕 spawn，entry failed，批次停止', () => {
  const { summary } = runBatch('smoke', {
    behaviorMap: { default: 'pass', controls: 'pass' },
    extraEnv: { E2E_GATE: 'gate1' },
  });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
  const defaultResult = summary.entries.find((e) => e.id === 'default');
  assert.equal(defaultResult.reason, 'env-pollution');
});

check('R5：ambient env 剛好等於 entry 自己要設的值（gate1 entry 的 ambient E2E_GATE=gate1）→ 不算污染，正常通過', () => {
  const { summary } = runBatch('gates', {
    behaviorMap: { gate1: 'pass' },
    extraEnv: { E2E_GATE: 'gate1' },
  });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'passed');
});

// --- review round 3（R5）：候選 run 目錄 >1 時仍安全保存原始資料 ---
check('R5：候選 run 目錄 >1（duplicate behavior）→ candidate-dirs.txt 與 package 的 candidates/ 都保留兩個候選目錄', () => {
  const { summary, workAttemptRoot } = runBatch('smoke', { behaviorMap: { default: 'duplicate' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  const caseDir = path.join(workAttemptRoot, 'default');
  const candidateListFile = path.join(caseDir, 'candidate-dirs.txt');
  assert.ok(existsSync(candidateListFile), 'candidate-dirs.txt 應該被寫出');
  const candidateLines = readFileSync(candidateListFile, 'utf8').trim().split('\n').filter(Boolean);
  assert.equal(candidateLines.length, 2, `預期 2 個候選目錄，實際：${JSON.stringify(candidateLines)}`);
  const candidatesDir = path.join(caseDir, 'e2e-evidence-package', 'candidates');
  assert.ok(existsSync(candidatesDir), 'package 應該把候選目錄複製進 e2e-evidence-package/candidates/');
  const copiedNames = readdirSync(candidatesDir);
  assert.equal(copiedNames.length, 2, `候選目錄應該各自保留原始名稱複製，實際：${JSON.stringify(copiedNames)}`);
});

// --- review round 4（#480 R2 剩餘缺口）：job-level envelope 缺檔／schema
//     錯誤——batch 開頭的前提，缺了就整批判定失敗，不嘗試跑任何 entry。 ---
check('R2：job-level envelope.json 不存在 → 整批 failed，不嘗試跑任何 entry', () => {
  const { spawnRc, summary } = runBatch('smoke', { behaviorMap: { default: 'pass', controls: 'pass' }, envelope: null });
  assert.notEqual(spawnRc, 0);
  assert.equal(summary.overall, 'failed');
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
  const defaultResult = summary.entries.find((e) => e.id === 'default');
  assert.equal(defaultResult.reason, 'envelope-invalid');
  assert.match(summary.envelopeError, /不存在/);
});

check('R2：job-level envelope.json 缺欄位（schema 錯誤）→ 整批 failed，不嘗試跑任何 entry', () => {
  const badEnvelope = { testMergeSha: '1'.repeat(40) }; // 遠不足以滿足 ENVELOPE_BASE_FIELDS
  const { spawnRc, summary } = runBatch('gates', { behaviorMap: { gate1: 'pass' }, envelope: badEnvelope });
  assert.notEqual(spawnRc, 0);
  assert.equal(summary.overall, 'failed');
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'failed');
  assert.equal(byId.gate2, 'not-run');
  assert.equal(byId.stale, 'not-run');
  assert.match(summary.envelopeError, /缺欄位或型別錯誤/);
});

check('R2：job-level envelope 合法（正例）→ 各 entry 的 caseDir 都被蓋上正確的 envelope.json', () => {
  const { summary, workAttemptRoot } = runBatch('gates', { behaviorMap: { gate1: 'pass' } });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.gate1, 'passed');
  const envelopePath = path.join(workAttemptRoot, 'gate1', 'envelope.json');
  assert.ok(existsSync(envelopePath), 'run-batch.mjs 應該把蓋章後的 envelope.json 寫進 gate1 的 caseDir');
  const stamped = JSON.parse(readFileSync(envelopePath, 'utf8'));
  assert.equal(stamped.entryId, 'gate1');
  assert.equal(stamped.configPath, 'e2e/playwright.gates.config.ts');
  assert.deepEqual(stamped.selectedEnv, { E2E_GATE: 'gate1' });
  assert.ok(stamped.runId && stamped.runId.length > 0, 'runId 應該被蓋成實際的 run 目錄名稱');
  const packagedEnvelopePath = path.join(workAttemptRoot, 'gate1', 'e2e-evidence-package', 'envelope.json');
  assert.ok(existsSync(packagedEnvelopePath), 'envelope.json 也應該被 package script 複製進 e2e-evidence-package/');
});

// --- S1（#483）：scenarios 批次（8 案）全鏈路整合測試 ---
// fake-runner.mjs 的 'pass' behavior 呼叫 makeGoodRunDir()（唯一權威來
// 源）——round 5 起這支函式會正確產生 8 個 scenario entry 各自的領域證據
// 檔（見 evidence-builder.mjs scenarioDomainContent()）。這裡跑一次完整的
// run-batch.mjs → wrapper → fake-runner → package → evaluate 全鏈路，證明
// S1 的修正不只在 evaluate-e2e-evidence.review483.selftest.mjs 的單獨呼叫
// 裡成立，整條批次管線也真的會產生、打包、驗證這些檔案。
check('S1 全鏈路：scenarios 批次 8 案全部 pass → overall=passed（含 scenario 領域證據的完整產生／打包／驗證）', () => {
  const { summary } = runBatch('scenarios', {
    behaviorMap: {
      'commandExecution-allow': 'pass',
      'commandExecution-deny': 'pass',
      'fileChange-allow': 'pass',
      'fileChange-deny': 'pass',
      'commandExecution-recovery': 'pass',
      'claude-approval-allow': 'pass',
      'claude-approval-deny': 'pass',
      'claude-approval-recovery': 'pass',
    },
  });
  assert.equal(summary.overall, 'passed', `summary: ${JSON.stringify(summary, null, 2)}`);
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  for (const id of Object.keys(byId)) assert.equal(byId[id], 'passed', `${id} 應該 passed`);
});

// --- S4（#483）：offline 鉤子不完整／矛盾時必須在任何 spawn 之前拒絕 ---
// 兩種情況都不能讓 buildCommand() 回退成 realCommandForEntry()（真正的
// run-e2e.mjs，會啟動 App／browser）。用「WORK_ROOT 從未被建立」證明拒絕
// 發生在**任何** spawn（甚至連 caseDir 的 mkdir）之前——這是決定文件要求
// 的「用記錄是否呼叫launch的假入口確認，不能真的launch作負控制」的離線等
// 價版本：不需要真的啟動 App，只要證明連 run-batch.mjs 自己的工作目錄準
// 備動作都沒有執行到，entries 迴圈（buildCommand／spawn 所在處）結構上不
//可能被進入。
check('S4：只設 CI_E2E_TEST_RUNNER、缺 CI_E2E_OFFLINE_SELFTEST=1 → 拒絕，且拒絕發生在任何 spawn 之前（WORK_ROOT 從未建立）', () => {
  const { spawnRc, stderr, workAttemptRoot } = runBatch('smoke', {
    behaviorMap: { default: 'pass' },
    extraEnv: { CI_E2E_OFFLINE_SELFTEST: undefined },
  });
  assert.notEqual(spawnRc, 0);
  assert.match(stderr, /S4 offline 鉤子邊界，spawn 之前.*CI_E2E_TEST_RUNNER 已設定但缺 CI_E2E_OFFLINE_SELFTEST=1/);
  assert.equal(existsSync(workAttemptRoot), false, 'WORK_ROOT 不該被建立——證明拒絕確實發生在任何 mkdir／spawn 之前');
});

check('S4：只設 CI_E2E_OFFLINE_SELFTEST=1、缺 CI_E2E_TEST_RUNNER → 拒絕，且拒絕發生在任何 spawn 之前（WORK_ROOT 從未建立）', () => {
  const { spawnRc, stderr, workAttemptRoot } = runBatch('smoke', {
    behaviorMap: { default: 'pass' },
    extraEnv: { CI_E2E_TEST_RUNNER: undefined },
  });
  assert.notEqual(spawnRc, 0);
  assert.match(stderr, /S4 offline 鉤子邊界，spawn 之前.*CI_E2E_OFFLINE_SELFTEST=1 但缺 CI_E2E_TEST_RUNNER/);
  assert.equal(existsSync(workAttemptRoot), false, 'WORK_ROOT 不該被建立——證明拒絕確實發生在任何 mkdir／spawn 之前');
});

check('S4：兩者都正確設定（既有的離線 selftest 正常路徑）→ 不受影響，仍正常執行', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'pass', controls: 'pass' } });
  assert.equal(summary.overall, 'passed');
});

// --- S3（#483）：run-batch.mjs 的 wrapperAbnormal／packageAbnormal 判定 ---
// SIGTERM→SIGKILL 兩段式逾時升級本身的邏輯（含 reviewer 的
// timeout-probe.mjs 核心反例：status=0 但 timedOut=true 仍必須判定異常）
// 已經在 bounded-spawn.selftest.mjs 用極小 ms 值直接單元測試過（run-batch
// 的 wrapper／package 兩段 production 逾時值是分鐘級，在這裡整合測試會讓
// selftest 慢到不現實）。這裡额外核對：`isAbnormalBoundedSpawnResult()`
// 確實是 run-batch.mjs 的 `passed` 判定依據之一，不是只在別的模組定義卻沒
// 接進主流程——用既有的 'ignore-term' 反例（wrapper 觸發 watchdog-forced-exit）
// 交叉核對 wrapperRc 非 0 這件事仍然正確導致 entry failed（既有回歸，防止
// S3 重構弄壞既有行為）。
check('S3 回歸：wrapper 逾時（watchdog-forced-exit，wrapperRc=125）→ entry failed，不因為改用 boundedSpawn 而誤判成功', () => {
  const { summary } = runBatch('smoke', { behaviorMap: { default: 'ignore-term' }, deadline: 1, grace: 1 });
  const byId = Object.fromEntries(summary.entries.map((e) => [e.id, e.status]));
  assert.equal(byId.default, 'failed');
  assert.equal(byId.controls, 'not-run');
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
