#!/usr/bin/env node
// evaluate-e2e-evidence.mjs — B3a-CI-1。沿用 PR #12（79819d8）
// codex-reviewer #82/#84 的 wrapper JSON／run-state／harness.log 內容驗證邏輯
// （G1 invalid_json_early、G2 failed_state_with_old_success、null_run_state、
// contradictory_verdict 四個反例的修法），並依 decision470 修正三個已知缺口：
//
//   1. JSON reporter 逐案核對（decision470 第 9 行「結構化報告缺口」）：
//      default／scenario 現在有 playwright.ci-default.config.ts／
//      playwright.ci-scenario.config.ts 兩個 CI-only 覆蓋檔提供 JSON
//      reporter，本檔用它核對「預期 entry 的 spec 檔集合」是否精確相符
//      （缺案／多案／額外案／錯身份）、每個 test 的 status／expectedStatus／
//      retry，不再只看 harness.log 的「最終結果：PASSED」一行字串。
//   2. controls oracle（decision470 第 10 行）：save-detection.spec.ts 的
//      control A 必須 expectedStatus=failed 且實際 failed，且錯誤訊息確實
//      來自目標存檔斷言（`expect(diskContentAtOldSignal, ...).toBe(newContent)`
//      的自訂訊息字串，逐字核對，見 frontend/e2e/controls/save-detection.spec.ts:128）；
//      control B 必須 passed；reverse-check.spec.ts 必須 skipped。任何偏離
//      （A 意外 pass、A 錯誤原因不符、A/B skip、reverse 非預期執行）一律拒絕。
//   3. NO-RUN 語意（decision470 第 21 行／v2 §3.2 點 3）：只代表「未取得 run
//      證據」，**不**寫成「確認未啟動」——PR #12 原文「判定為啟動前就失敗／
//      未開始」是過度宣稱，本檔改寫。
//
// 「這次是哪一次執行」的候選 run 目錄一律由呼叫端（run-batch.mjs）在 spawn
// 前後做 before/after 快照 diff 算好，寫進 <workdir>/new-run-dirs.json（絕對
// 路徑陣列），本檔只信任這份清單、不自己重新掃描整個 artifacts root——避免
// 跟其他案例的 run 目錄混算（decision470：「每案獨立路徑，禁止新案覆寫前案」）。
//
// 用法：node evaluate-e2e-evidence.mjs <workdir> <package-dir> <entry-id>
//   <workdir>      run-batch.mjs 為該 entry 建立的獨立工作目錄，要有
//                  e2e-wrapper-status.json／e2e.rc／new-run-dirs.json
//   <package-dir>  該 entry 的證據輸出包路徑（package-e2e-evidence.sh 已完成
//                  複製；這裡只在「確認過的未取得證據」例外成立時寫
//                  NO-RUN.txt，不動其他檔案）
//   <entry-id>     ci-e2e-entries.mjs 裡的 entry id，用來查預期 spec 檔集合
//
// exit 0：這次證據（含合法的 NO-RUN 例外）通過檢查；exit 1：缺漏或不可信。
import { readFileSync, existsSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  entryById,
  otherRequiredFilesForEntry,
  requiredDomainFilesForEntry,
  optionalDomainFilesForEntry,
  expectedGateFlowForEntry,
  expectedRunEnvScenarioForEntry,
  expectedScenarioConfigIdentity,
  expectedScenarioSecondTurnIdentity,
  expectedScenarioApprovalMethodForEntry,
  expectedCodexScenarioDecisionForEntry,
  expectedClaudeProtocolDecisionForEntry,
  ENVELOPE_STAMP_STRING_FIELDS,
  CODEX_SCENARIO_IDS,
} from './ci-e2e-entries.mjs';
import { checkEnvelopeBaseAll } from './envelope-checks.mjs';
import { rejectTestControlEnvUnderGithubActions } from './gha-runtime-guard.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// review round 7（#491 G2）：三個正常 GitHub Actions 入口之一（另兩個是
// run-batch.mjs／generate-ci-envelope.mjs）。見 gha-runtime-guard.mjs 頭
// 註——在任何 runtime／probe spawn 之前最早拒絕。
rejectTestControlEnvUnderGithubActions('evaluate-e2e-evidence.mjs', [
  'CI_E2E_SELFTEST_ACTUAL_NODE_VERSION',
]);

const [, , workdir, packageDir, entryId] = process.argv;
if (!workdir || !packageDir || !entryId) {
  process.stderr.write('usage: evaluate-e2e-evidence.mjs <workdir> <package-dir> <entry-id>\n');
  process.exit(2);
}

const entry = entryById(entryId);
if (!entry) {
  process.stderr.write(`::error::evaluate-e2e-evidence.mjs: 未知 entry-id "${entryId}"（不在 ci-e2e-entries.mjs ENTRIES 裡）\n`);
  process.exit(1);
}

// 提到最前面宣告（不是放在 evaluateControlsOracle() 旁邊）：主流程在模組頂層
// 就會同步呼叫到 evaluateClaimedSuccessRun()→evaluateControlsOracle()，若把
// 這個 const 留在檔案後段會落入 TDZ（temporal dead zone），第一次呼叫就
// ReferenceError——這是本輪離線 selftest 抓到的真實 bug，見
// /Users/eason_tseng/b3a-evidence/ci-1/attempt-001/evaluator-selftest-round2.log。
const CONTROL_A_TARGET_ASSERTION_MESSAGE = '對照 A（舊判定）預期失敗：讀到的應是舊內容而非新內容';

// review round 5（#483 S1）：跟 CONTROL_A_TARGET_ASSERTION_MESSAGE 同樣的
// TDZ 理由，提到最前面宣告——這些領域證據檔是 JSONL（每行一個 JSON 物
// 件），不是單一 JSON 物件，走 checkJsonlDomainFile() 而不是一般的
// JSON.parse() 路徑（見 ci-e2e-entries.mjs requiredDomainFilesForEntry()
// 的完整 file:line 對照）。
const JSONL_DOMAIN_FILES = new Set([
  'scenario-wire.log',
  'app-audit.jsonl',
  'app-wire-log.jsonl',
  'claude-broker-audit.raw.jsonl',
  'claude-recovery/exit-observations.jsonl',
  'claude-recovery/round1/audit.jsonl',
  'claude-recovery/round2/audit.jsonl',
]);

const ALLOWED_STATUSES = new Set([
  'completed',
  'timeout',
  'timeout-no-clean-exit',
  'interrupted',
  'producer-error',
  'watchdog-forced-exit',
  'rejected-unsupported-output-target',
]);

let hadError = false;
function fail(msg) {
  hadError = true;
  process.stdout.write(`::error::[${entryId}] ${msg}\n`);
}

function readTextIfExists(p) {
  return existsSync(p) ? readFileSync(p, 'utf8') : null;
}

/**
 * review round 7（#491 G1）：從 run-state.json 的 processes[].command 找出
 * fakeClaudeCli 啟動參數裡的 `--mcp-config .../mcp-<WSID>.json`（跟
 * frontend/e2e/support/scenario/claudeAppEvidence.ts:70-74
 * wsidFromMcpConfigPath() 同一種檔名慣例，這裡只做最小字串擷取，不 import
 * 該 .ts 檔——evaluator 是純 Node ESM .mjs，不跑 TS loader，也不重寫一份完
 * 整的 protocol judge）。run-state.json 是 App/process 端獨立於
 * claude-ui-judgement.json 的觀察來源，不受後者整份被替換影響，作為交叉核
 * 對的錨點。找不到就回傳 null，呼叫端決定要不要因此判定失敗。
 */
function extractClaudeWsidFromRunState(runDir) {
  const p = path.join(runDir, 'run-state.json');
  if (!existsSync(p)) return null;
  try {
    const parsed = JSON.parse(readFileSync(p, 'utf8'));
    const processes = Array.isArray(parsed?.processes) ? parsed.processes : [];
    for (const proc of processes) {
      const cmd = typeof proc?.command === 'string' ? proc.command : '';
      const m = /mcp-([A-Za-z0-9]+)\.json/.exec(cmd);
      if (m && m[1]) return m[1];
    }
  } catch {
    return null;
  }
  return null;
}

function verifyPackageCopy(label, sourcePath, copyPath) {
  if (!existsSync(sourcePath)) return;
  if (!existsSync(copyPath)) {
    fail(`${label} 來源存在，但輸出包副本（${copyPath}）不存在，複製可能失敗`);
    return;
  }
  const srcHash = createHash('sha256').update(readFileSync(sourcePath)).digest('hex');
  const dstHash = createHash('sha256').update(readFileSync(copyPath)).digest('hex');
  if (srcHash !== dstHash) {
    fail(`${label} 來源與輸出包副本內容不一致（sha256 不同），複製可能損毀`);
  }
}

const statusFile = path.join(workdir, 'e2e-wrapper-status.json');
const rcFile = path.join(workdir, 'e2e.rc');
const newRunDirsFile = path.join(workdir, 'new-run-dirs.json');

const statusText = readTextIfExists(statusFile);
const rcText = readTextIfExists(rcFile);

if (statusText === null) {
  // e2e-wrapper-status.json 根本不存在：交給呼叫端既有的檔案存在性檢查去
  // fail，這裡不重複判斷、也不寫 NO-RUN.txt。
  process.exit(0);
}

let wrapperStatusValid = false;
let wrapperStatus = null;
let wrapperRc = null;

let parsed;
try {
  parsed = JSON.parse(statusText);
} catch {
  parsed = undefined;
}

if (
  parsed
  && typeof parsed === 'object'
  && typeof parsed.status === 'string'
  && ALLOWED_STATUSES.has(parsed.status)
  && Number.isInteger(parsed.wrapperRc)
) {
  let rcFileValue = null;
  if (rcText !== null) {
    const trimmed = rcText.trim();
    rcFileValue = /^-?\d+$/.test(trimmed) ? Number(trimmed) : NaN;
  }
  if (rcFileValue !== null && Number.isNaN(rcFileValue)) {
    fail(`e2e.rc 內容無法解析成整數（"${rcText.trim()}"），無法核對與 wrapperRc 是否一致，視為不可信`);
  } else if (rcFileValue !== null && rcFileValue !== parsed.wrapperRc) {
    fail(`e2e-wrapper-status.json 的 wrapperRc=${parsed.wrapperRc} 與 e2e.rc 內容(${rcFileValue}) 不一致，視為不可信`);
  } else {
    wrapperStatusValid = true;
    wrapperStatus = parsed;
    wrapperRc = parsed.wrapperRc;
  }
} else {
  fail(
    'e2e-wrapper-status.json 無法解析為合法 JSON，或缺少必要欄位／型別不對'
      + '（status 需為允許值之一、wrapperRc 需為整數）——不得套用 NO-RUN 例外，視為缺漏',
  );
}

if (!wrapperStatusValid) {
  process.exit(1);
}

verifyPackageCopy('e2e-wrapper-status.json', statusFile, path.join(packageDir, 'e2e-wrapper-status.json'));
verifyPackageCopy('e2e.rc', rcFile, path.join(packageDir, 'e2e.rc'));

// 候選 run 目錄清單：完全信任 run-batch.mjs 算好的 before/after diff，不在
// 這裡重新掃描 artifacts root。
let newRunDirs = [];
if (existsSync(newRunDirsFile)) {
  try {
    const arr = JSON.parse(readFileSync(newRunDirsFile, 'utf8'));
    if (Array.isArray(arr) && arr.every((x) => typeof x === 'string')) {
      newRunDirs = arr;
    } else {
      fail('new-run-dirs.json 內容不是字串陣列，視為不可信（無法確認候選 run 目錄）');
    }
  } catch (e) {
    fail(`new-run-dirs.json 無法解析（${e.message}），視為不可信`);
  }
} else {
  fail('new-run-dirs.json 不存在（呼叫端未提供 before/after diff 結果），視為缺漏');
}

if (hadError) process.exit(1);

if (wrapperStatus.status !== 'completed' && wrapperRc === 0) {
  fail(`wrapper status="${wrapperStatus.status}" but wrapperRc=0 is inconsistent`);
}

const claimedSuccess = wrapperStatus.status === 'completed' && wrapperRc === 0;

// review round 3（#480 CHANGES_REQUIRED R3）：先前只驗 status／wrapperRc／
// e2e.rc 三者一致，沒有核對 wrapper 自己在同一份 JSON 裡記錄的
// childRc／childConfirmedGone／producerErrors（schema 見
// ci-e2e-wrapper.mjs 的 statusPayload，這幾個欄位一律會寫）。reviewer 已
// 重現：wrapperRc=0 但 childRc=7、childConfirmedGone='unknown'、
// producerErrors 非空，仍判定 claimedSuccess 通過。這三者任一跟「完整成
// 功」矛盾，就不得放行——即使 wrapperRc 本身是 0。
if (claimedSuccess) {
  if (!Number.isInteger(wrapperStatus.childRc) || wrapperStatus.childRc !== 0) {
    fail(
      `e2e-wrapper-status.json 宣稱 completed/wrapperRc=0，但 childRc=${JSON.stringify(wrapperStatus.childRc)}`
        + '（非 0 或缺失）——child／wrapper 兩個 rc 自相矛盾，不得判定成功',
    );
  }
  if (wrapperStatus.childConfirmedGone !== 'true') {
    fail(
      `e2e-wrapper-status.json 宣稱 completed，但 childConfirmedGone=${JSON.stringify(wrapperStatus.childConfirmedGone)}`
        + '（非 "true"）——child 是否真的正常結束未經確認，不得判定成功',
    );
  }
  if (!Array.isArray(wrapperStatus.producerErrors)) {
    fail('e2e-wrapper-status.json 缺 producerErrors 陣列或型別不對，無法核對是否有證據寫入失敗');
  } else if (wrapperStatus.producerErrors.length > 0) {
    fail(
      `e2e-wrapper-status.json 宣稱 completed，但 producerErrors 有 ${wrapperStatus.producerErrors.length} 筆`
        + `（${JSON.stringify(wrapperStatus.producerErrors)}）——證據寫入曾失敗，不得判定成功`,
    );
  }
}

if (claimedSuccess) {
  if (newRunDirs.length === 0) {
    fail(
      '（wrapper 宣稱 completed/wrapperRc=0，但 before/after diff 沒有偵測到任何新 run 目錄，'
        + '視為缺漏，不套用 NO-RUN 例外）',
    );
  } else if (newRunDirs.length > 1) {
    fail(
      `wrapper 宣稱 completed/wrapperRc=0，但 before/after diff 偵測到 ${newRunDirs.length} 個新 run 目錄`
        + '（預期本次唯一一個，可能是身分混淆／並行污染），視為 identity 異常，拒絕',
    );
  } else {
    evaluateClaimedSuccessRun(newRunDirs[0]);
  }
} else if (newRunDirs.length === 0) {
  if (wrapperStatus.status !== 'completed' && wrapperRc === 0) {
    fail(
      `wrapper status="${wrapperStatus.status}" 但 wrapperRc=0（非 completed 卻 rc=0 自相矛盾），`
        + '不得套用 NO-RUN 例外，視為不可信',
    );
  } else {
    // decision470／v2 §3.2 點 3 的修正：NO-RUN 只代表「未取得 run 證據」，
    // 不宣稱「確認未啟動」——wrapper 沒能觀察到任何 harness 產出的 run 目
    // 錄，可能是啟動前就失敗，也可能是啟動了但我們的觀察手段沒能捕捉到；
    // 兩者都無法用現有證據區分，因此措辭不做「未啟動」這個更強的宣稱。
    const noRunNote =
      `wrapper status=${wrapperStatus.status} wrapperRc=${wrapperRc}，且 before/after diff 未偵測到任何新 run 目錄：`
      + '未取得 run 證據，無法判定是否曾啟動（不是「確認未啟動」）。此 entry 判定為失敗。';
    try {
      writeFileSync(path.join(packageDir, 'NO-RUN.txt'), `${noRunNote}\n`);
    } catch (e) {
      fail(`寫 NO-RUN.txt 失敗：${e.message}`);
    }
    // NO-RUN 本身仍是失敗（decision470：「NO-RUN是未取得證據，不證明未開
    // 始」——不代表「可略過不計」，該 entry 判定為失敗，批次停止）。
    fail(`NO-RUN：${noRunNote}`);
  }
} else {
  // 宣稱非成功，但仍有新 run 目錄：保留既有行為（PR #12 既有裁定），不在
  // 本輪修法範圍內做內容深度核對，只記錄候選目錄數量供人工檢視。
  process.stdout.write(
    `[${entryId}] 非成功宣稱（status=${wrapperStatus.status} wrapperRc=${wrapperRc}），`
      + `但偵測到 ${newRunDirs.length} 個新 run 目錄，證據已保留，不在本輪內容驗證範圍。\n`,
  );
}

process.exit(hadError ? 1 : 0);

// ---------------------------------------------------------------------------

// review round 4（#480 CHANGES_REQUIRED R2 剩餘缺口）：可信 CI invocation
// envelope 交叉核對。reviewer 裁定：「不能只echo metadata到job log」——先前
// workflow 只把 PR head/base/test-merge SHA、toolchain、image、Chrome 版本
// echo 到 job log，這裡改成讀 run-batch.mjs 蓋在 workdir/envelope.json 的
// 結構化檔案（基底欄位由 generate-ci-envelope.mjs 在 job 開頭從 GitHub
// context／實際探測指令產生，entry 專屬欄位由 run-batch.mjs 逐 entry 蓋
// 章），跟以下來源交叉核對，任一不一致／缺檔／schema 錯誤一律拒絕：
//   - evaluator 自己現場重新執行 `git rev-parse HEAD`（不只信 envelope 自
//     己宣稱的 checkoutHeadSha）；
//   - evaluator 自己的 process.version（同一個 job 下的同一個 Node
//     runtime——「至少 Node」版本一致性的最直接來源）；
//   - 這次評估的 entry-id／entry.configPath／entry.env（entryId 對不上，
//     代表同一支 spec 底下不同案例的證據可能被互換；決定470 reviewer 明確
//     點名 commandExecution-allow/deny、fileChange-allow/deny 這種同 spec
//     案例）；
//   - runDirName（實際 run 目錄名稱——runId 對不上，代表上一次 run 或另一
//     個 entry 的證據被混用，即「跨 run」反例）。
function validateEnvelope(runDirName) {
  const envelopePath = path.join(workdir, 'envelope.json');
  if (!existsSync(envelopePath)) {
    fail(`envelope.json 不存在（${envelopePath}）——可信 CI invocation envelope 缺檔，成功宣稱下不得放行`);
    return;
  }
  verifyPackageCopy('envelope.json', envelopePath, path.join(packageDir, 'envelope.json'));

  let envelope;
  try {
    envelope = JSON.parse(readFileSync(envelopePath, 'utf8'));
  } catch (e) {
    fail(`envelope.json 無法解析（${e.message}）`);
    return;
  }
  if (!envelope || typeof envelope !== 'object' || Array.isArray(envelope)) {
    fail('envelope.json 不是物件');
    return;
  }

  let schemaOk = true;
  for (const field of ENVELOPE_STAMP_STRING_FIELDS) {
    if (typeof envelope[field] !== 'string' || envelope[field].length === 0) {
      fail(`envelope.json 缺欄位或型別錯誤：${field}（需為非空字串，實際：${JSON.stringify(envelope[field])}）`);
      schemaOk = false;
    }
  }
  if (
    envelope.selectedEnv === undefined
    || envelope.selectedEnv === null
    || typeof envelope.selectedEnv !== 'object'
    || Array.isArray(envelope.selectedEnv)
  ) {
    fail(`envelope.json 缺欄位或型別錯誤：selectedEnv（需為物件，實際：${JSON.stringify(envelope.selectedEnv)}）`);
    schemaOk = false;
  }

  // review round 5（#483 直接裁定）：schema／SHA／內部一致性／checkout
  // HEAD 交叉核對／Node 版本交叉核對改用 envelope-checks.mjs 的共用函式
  // ——run-batch.mjs 在 job 開始時已經對「job-level envelope」（不含
  // entry 專屬戳記）跑過同一組檢查（preflight，能在啟動前判定的錯誤在那
  // 裡就擋下）；這裡對「entry 專屬的最終 envelope」（含戳記）再跑一次同一
  // 組共用邏輯，確保 run-batch.mjs 蓋章之後這些基底欄位沒有被中途竄改，
  // 兩處呼叫同一份實作，不重複維護、不會漂移。
  const repoRootForGitCheck = process.env.CI_E2E_REPO_ROOT || workdir;
  // review round 6（#489 F3）：見 run-batch.mjs 同名 env var 的頭註——只在
  // 離線 selftest 需要時才設定，正式 workflow 絕不設定。
  const baseViolations = checkEnvelopeBaseAll(envelope, {
    repoRootForGitCheck,
    actualNodeVersion: process.env.CI_E2E_SELFTEST_ACTUAL_NODE_VERSION || undefined,
  });
  for (const v of baseViolations) fail(`envelope.json ${v}`);
  if (baseViolations.length > 0) schemaOk = false;

  // 基本型別都不對時不再做衍生的交叉核對——那些檢查本來就得先讀到正確型別
  // 的值才有意義，強行往下做只會對同一個根因重複噴一堆連鎖錯誤訊息。
  if (!schemaOk) return;

  // identity／provenance：envelope 宣稱的 entry／config／selectedEnv／
  // run-id 必須跟這次評估的對象完全相符，否則視為同 spec 案例互換或跨 run
  // 證據混用。
  if (envelope.entryId !== entryId) {
    fail(`envelope.json 的 entryId="${envelope.entryId}"，與本次評估的 entry-id="${entryId}" 不符——可能是別的 entry 的 envelope／證據被冒充`);
  }
  if (envelope.configPath !== entry.configPath) {
    fail(`envelope.json 的 configPath="${envelope.configPath}"，預期 "${entry.configPath}"`);
  }
  if (!shallowStringObjectEquals(envelope.selectedEnv, entry.env)) {
    fail(
      `envelope.json 的 selectedEnv=${JSON.stringify(envelope.selectedEnv)}，預期 ${JSON.stringify(entry.env)}`
        + '——同一支 spec 底下不同案例（例如 commandExecution-allow/deny）可能被互換',
    );
  }
  if (envelope.runId !== runDirName) {
    fail(
      `envelope.json 的 runId="${envelope.runId}"，與本次實際 run 目錄名稱 "${runDirName}" 不符`
        + '——可能是跨 run（上一次 run 或其他 entry）的證據被混用',
    );
  }
}

function shallowStringObjectEquals(a, b) {
  const aKeys = Object.keys(a).sort();
  const bKeys = Object.keys(b).sort();
  if (aKeys.length !== bKeys.length) return false;
  for (let i = 0; i < aKeys.length; i += 1) {
    if (aKeys[i] !== bKeys[i]) return false;
  }
  return aKeys.every((k) => a[k] === b[k]);
}

function evaluateClaimedSuccessRun(runDir) {
  const runDirName = path.basename(runDir);
  const runStateFile = path.join(runDir, 'run-state.json');
  const harnessLogFile = path.join(runDir, 'harness.log');
  const resultsFile = path.join(runDir, 'playwright-results.json');
  const packageArtifactsRoot = path.join(packageDir, 'artifacts');

  // review round 3（R1）：TEST_FAILED 是每支 spec 的 afterEach 在
  // testInfo.status!==testInfo.expectedStatus 時寫的標記（例如
  // frontend/e2e/glossary.spec.ts:23、controls/save-detection.spec.ts:51、
  // scenarios/codexApproval.spec.ts:63），global-teardown.ts:372-384 本來就
  // 會把它併進 overallFailed／「最終結果：FAILED」——但 evaluator 先前只
  // 信任 harness.log 這份「已經算好的結論」，沒有獨立核對 TEST_FAILED 本
  // 身存不存在。reviewer 已證實：直接在真實 PASS 的 run 目錄裡另外放一個
  // TEST_FAILED（harness.log 沒有同步更新），evaluator 仍 rc=0。這裡獨立
  // 核對，不依賴 harness.log 有沒有如實反映。
  if (existsSync(path.join(runDir, 'TEST_FAILED'))) {
    fail(
      `${runDirName}/TEST_FAILED 存在——依 frontend/e2e/global-teardown.ts:372-384 的既有契約，`
        + '這代表至少一個 test 的實際結果不等於 expectedStatus，不得判定成功（即使 harness.log 沒有同步反映）',
    );
  }

  // review round 3（R1／R2）：execution-entry.json 是 default／scenario 兩
  // 條 globalSetup 各自寫一次的入口標記（global-setup.ts:82／
  // global-setup.scenario.ts:93），gates 委派 default globalSetup 所以值
  // 維持 'default'（global-setup.gates.ts:8 頭註）。獨立核對存在＋型別＋
  // 值是否符合這個 entry 該有的入口，是 identity 的第二道獨立來源。
  const executionEntryFile = path.join(runDir, 'execution-entry.json');
  const expectedExecutionEntry = entry.batch === 'scenarios' ? 'scenario' : 'default';
  if (!existsSync(executionEntryFile)) {
    fail(`${runDirName}/execution-entry.json 不存在，成功宣稱缺必要證據`);
  } else {
    verifyPackageCopy(`${runDirName}/execution-entry.json`, executionEntryFile, path.join(packageArtifactsRoot, 'execution-entry.json'));
    try {
      const parsedEntry = JSON.parse(readFileSync(executionEntryFile, 'utf8'));
      if (!parsedEntry || typeof parsedEntry !== 'object' || Array.isArray(parsedEntry)) {
        fail(`${runDirName}/execution-entry.json 不是物件`);
      } else if (parsedEntry.entry !== expectedExecutionEntry) {
        fail(`${runDirName}/execution-entry.json 的 entry="${parsedEntry.entry}"，預期 "${expectedExecutionEntry}"，identity 異常`);
      }
    } catch (e) {
      fail(`${runDirName}/execution-entry.json 無法解析（${e.message}）`);
    }
  }

  // review round 3（R1／R2）＋round 5（#483 S1／S2）：領域證據檔——只驗存在
  // 還不夠，連 schema／identity／內部一致性都要核對。round 5 新增：
  //   - S1：8 個 scenario entry 各自的 producer 檔（見 ci-e2e-entries.mjs
  //     requiredDomainFilesForEntry() 頭註的完整 file:line 對照）。這些檔
  //     案有些是 JSONL（app-audit.jsonl／app-wire-log.jsonl／
  //     scenario-wire.log／claude-broker-audit.raw.jsonl／
  //     claude-recovery/*.jsonl），不是單一 JSON 物件，需要不同的解析／驗
  //     證路徑（見下方 JSONL_DOMAIN_FILES）。
  //   - S2：gate-evidence.json 的 bodyStatus／控制組 wrapperEvidence 型別／
  //     控制組 error 正規化去重等——見各分支內的 round 5 註解。
  // review round 6（#489 F2）：跨 run 身分關聯——reviewer 已證實把
  // commandExecution-allow 的 app-audit.jsonl／app-workspace-sessions.json／
  // app-wire-log.jsonl／app-wire-log.meta.json／scenario-wire.log／
  // scenario-wire.log.manifest.json 六個檔案整批換成 fileChange-allow（另一
  // 個真實 run）的原文，evaluator 仍 rc0——先前這幾個檔案只驗「存在＋型別
  // ＋單一 provider/exit code」，沒有跟 scenario-config.json 已核對過的
  // threadId／turnId／itemId／approvalRequestId／wsid 這些 producer 既有的
  // identity 欄位關聯起來。`identityFacts` 是這個 run 內、跨這幾個 domain
  // 檔案共用的可變狀態（依 CODEX_SCENARIO_DOMAIN_FILES 的固定順序，
  // app-audit.jsonl 先建立 wsid，後面的檔案再核對是否一致）——不是重寫
  // claudeApprovalJudge/protocol judge 那種完整判定，只是把 producer 已經
  // 寫在各檔案裡的身分欄位串起來，讓六檔互換無法再靜默通過。
  const identityFacts = {};
  // review round 7（#491 G1）：reviewer 已證實整份 claude-ui-judgement.json
  // 換成另一個真實 run 的原文（expectation／audit／run-env／CI envelope 全
  // 部保留這次 run 的值），evaluator 仍 rc0——判定檔本身的 violations=[]
  // 不能單獨當作可信來源。這裡先於 domain 迴圈獨立讀一次 run-state.json
  // （UNIVERSAL_REQUIRED_FILES 既有必要檔，不受 claude-ui-judgement.json
  // 本身影響），供下面 claude-ui-judgement.json 分支核對 configWsid。
  identityFacts.claudeRunStateWsid = extractClaudeWsidFromRunState(runDir);
  for (const domainFile of requiredDomainFilesForEntry(entry)) {
    const domainPath = path.join(runDir, domainFile);
    if (!existsSync(domainPath)) {
      fail(`${runDirName}/${domainFile} 不存在，成功宣稱缺必要領域證據`);
      continue;
    }
    verifyPackageCopy(`${runDirName}/${domainFile}`, domainPath, path.join(packageArtifactsRoot, domainFile));

    if (JSONL_DOMAIN_FILES.has(domainFile)) {
      checkJsonlDomainFile(domainFile, domainPath, runDirName, entry, identityFacts);
      continue;
    }

    let domainParsed;
    try {
      domainParsed = JSON.parse(readFileSync(domainPath, 'utf8'));
    } catch (e) {
      fail(`${runDirName}/${domainFile} 無法解析（${e.message}）`);
      continue;
    }
    if (domainParsed === null || typeof domainParsed !== 'object') {
      fail(`${runDirName}/${domainFile} 不是物件`);
      continue;
    }
    // claude-recovery/dom-observations.json 依 production 原始碼是陣列
    // （claudeSessionRecovery.spec.ts:126 `domObservations: Array<...>`）。
    //
    // review round 6（#489 F1）：claude-broker-audit.json 同樣是陣列，不是
    // 物件——`selectBrokerAuditForApproval()`
    // （claudeAppEvidence.ts:26，回傳型別明確是 `unknown[]`）挑出的 broker
    // audit 行，`claudeApproval.spec.ts:174-175` 直接
    // `JSON.stringify(brokerAudit, ...)` 寫出。reviewer 已證實：真實
    // deny run `20260922T000918Z-c85e82`（domain 原件完整）evaluator 唯一
    // 的錯誤就是這個合法 array 被當成「不是物件」拒絕。這裡只在這兩個檔案
    // 放行陣列，不放寬其他檔案（decision.md：「不是全面放行所有 domain
    // array」）。
    if (
      Array.isArray(domainParsed)
      && domainFile !== 'claude-recovery/dom-observations.json'
      && domainFile !== 'claude-broker-audit.json'
    ) {
      fail(`${runDirName}/${domainFile} 是陣列，預期物件`);
      continue;
    }
    checkJsonDomainFile(domainFile, domainParsed, runDirName, entry, identityFacts);
  }

  // claude-approval-deny 專屬：claude-ui-action.json 不在
  // requiredDomainFilesForEntry() 內（allow 案不會有這個檔案，見
  // ci-e2e-entries.mjs optionalDomainFilesForEntry() 頭註），但 deny 案必須
  // 存在且 clickResolved===true。
  for (const optionalFile of optionalDomainFilesForEntry(entry)) {
    const optPath = path.join(runDir, optionalFile);
    if (!existsSync(optPath)) {
      fail(`${runDirName}/${optionalFile} 不存在——${entry.id} 案的 spec 分支必然會寫這個檔案（claudeApproval.spec.ts:137-145）`);
      continue;
    }
    verifyPackageCopy(`${runDirName}/${optionalFile}`, optPath, path.join(packageArtifactsRoot, optionalFile));
    try {
      const parsed = JSON.parse(readFileSync(optPath, 'utf8'));
      if (parsed.clickResolved !== true) {
        fail(`${runDirName}/${optionalFile} 的 clickResolved=${JSON.stringify(parsed.clickResolved)}，預期 true（deny 按鈕點擊尚未真的完成）`);
      }
      if (parsed.decision !== 'deny') {
        fail(`${runDirName}/${optionalFile} 的 decision="${parsed.decision}"，預期 "deny"`);
      }
    } catch (e) {
      fail(`${runDirName}/${optionalFile} 無法解析（${e.message}）`);
    }
  }

  // review round 3（R2）：run-env.json 的 identity／provenance 交叉核對。
  // 先前只在 run-state.json 核對過 runId；run-env.json 是獨立寫入的另一份
  // 檔案（frontend/e2e/support/env.ts:59 writeRunEnv()），reviewer 已重現
  // 把它整份換成 `{"runId":"different-run","head":"wrong-sha"}` 仍 rc=0。
  // scenario 入口另外核對 `scenario` 欄位（見 ci-e2e-entries.mjs
  // expectedRunEnvScenarioForEntry() 頭註）——這是同一支 spec 底下
  // allow/deny／commandExecution/fileChange 四案唯一可信的區分來源。
  const runEnvFile = path.join(runDir, 'run-env.json');
  if (!existsSync(runEnvFile)) {
    fail(`${runDirName}/run-env.json 不存在，成功宣稱缺必要證據`);
  } else {
    verifyPackageCopy(`${runDirName}/run-env.json`, runEnvFile, path.join(packageArtifactsRoot, 'run-env.json'));
    try {
      const runEnv = JSON.parse(readFileSync(runEnvFile, 'utf8'));
      if (!runEnv || typeof runEnv !== 'object' || Array.isArray(runEnv)) {
        fail(`${runDirName}/run-env.json 不是物件`);
      } else {
        if (runEnv.runId !== runDirName) {
          fail(`${runDirName}/run-env.json 的 runId="${runEnv.runId}" 與目錄名不一致，identity 異常`);
        }
        if (typeof runEnv.artifactsDir !== 'string' || !runEnv.artifactsDir.includes(runDirName)) {
          fail(`${runDirName}/run-env.json 的 artifactsDir="${runEnv.artifactsDir}" 與本次 run 目錄不一致`);
        }
        const expectedScenario = expectedRunEnvScenarioForEntry(entry);
        if (expectedScenario !== null && runEnv.scenario !== expectedScenario) {
          fail(
            `${runDirName}/run-env.json 的 scenario="${runEnv.scenario}"，預期 "${expectedScenario}"——`
              + '同一支 spec 底下不同 scenario 案例可能被互換（見 scenarios.ts SCENARIOS 登記表、global-setup.scenario.ts:387/402 writeRunEnv）',
          );
        }
      }
    } catch (e) {
      fail(`${runDirName}/run-env.json 無法解析（${e.message}）`);
    }
  }

  // review round 4（#480 R2 剩餘缺口）：可信 CI invocation envelope 交叉核
  // 對——見下方 validateEnvelope() 頭註。envelope.json 是 run-batch.mjs 蓋
  // 在 caseDir（也就是這支腳本的 workdir 參數）底下的檔案，不在 runDir 裡
  // 面（它記錄的是「這次 CI invocation」本身的身分，不是 harness 產出的一
  // 部分）。
  validateEnvelope(runDirName);

  // review round 2 修法：先前這裡硬寫死「每個 entry 都要 chrome-argv.txt」，
  // 但用真實 controls／gate1 run 目錄核對後發現該檔案只有呼叫
  // `captureChromeArgv()` 的 spec 才會產生（見 ci-e2e-entries.mjs 的
  // `otherRequiredFilesForEntry()` 頭註逐檔 file:line 引用），改成依 entry
  // 動態決定必要檔清單，不再是每個 entry 共用同一份固定清單。
  const OTHER_REQUIRED_FILES = otherRequiredFilesForEntry(entry);
  for (const name of OTHER_REQUIRED_FILES) {
    const sourcePath = path.join(runDir, name);
    if (!existsSync(sourcePath)) {
      fail(`${runDirName}/${name} 不存在，成功宣稱缺必要證據（固定必要檔清單）`);
    } else {
      verifyPackageCopy(`${runDirName}/${name}`, sourcePath, path.join(packageArtifactsRoot, name));
    }
  }

  // --- run-state.json ---
  let runState = null;
  let runStateParsed = false;
  if (!existsSync(runStateFile)) {
    fail(`${runDirName}/run-state.json 不存在，成功宣稱缺必要證據`);
  } else {
    verifyPackageCopy(`${runDirName}/run-state.json`, runStateFile, path.join(packageArtifactsRoot, 'run-state.json'));
    try {
      runState = JSON.parse(readFileSync(runStateFile, 'utf8'));
      runStateParsed = true;
    } catch (e) {
      fail(`${runDirName}/run-state.json 無法解析（${e.message}），成功宣稱下不得信任內容`);
    }
  }
  if (runStateParsed) {
    if (runState === null || typeof runState !== 'object' || Array.isArray(runState)) {
      const actualKind = runState === null ? 'null' : Array.isArray(runState) ? 'array' : typeof runState;
      fail(`${runDirName}/run-state.json 解析結果不是物件（實際型別：${actualKind}），成功宣稱下不得信任內容`);
    } else {
      if (runState.runId !== runDirName) {
        fail(`${runDirName}/run-state.json 的 runId="${runState.runId}" 與目錄名不一致，identity 異常，無法核對這是本次執行的證據`);
      }
      if (runState.status !== 'stopped') {
        fail(`${runDirName}/run-state.json 的 status="${runState.status}"，成功宣稱要求恰好是 'stopped'`);
      }
      if (runState.failureStage) {
        fail(`${runDirName}/run-state.json 有 failureStage="${runState.failureStage}"，跟成功宣稱矛盾`);
      }
      if (!Array.isArray(runState.observationFailures)) {
        fail(`${runDirName}/run-state.json observationFailures must be an array`);
      } else if (runState.observationFailures.length > 0) {
        fail(`${runDirName}/run-state.json 有 ${runState.observationFailures.length} 筆 observationFailures，跟成功宣稱矛盾`);
      }
    }
  }

  // --- harness.log ---
  if (!existsSync(harnessLogFile)) {
    fail(`${runDirName}/harness.log 不存在，成功宣稱缺必要證據`);
  } else {
    verifyPackageCopy(`${runDirName}/harness.log`, harnessLogFile, path.join(packageArtifactsRoot, 'harness.log'));
    const lines = readFileSync(harnessLogFile, 'utf8').split('\n').filter((l) => l.length > 0);
    const verdictLines = lines.filter((l) => l.includes('globalTeardown 判定'));
    const lastVerdict = verdictLines.length > 0 ? verdictLines[verdictLines.length - 1] : null;
    if (!lastVerdict) {
      fail(`${runDirName}/harness.log 找不到 globalTeardown 判定行，無法核對是否真的成功`);
    } else {
      if (!/cleanupClean=true/.test(lastVerdict)) {
        fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行沒有 cleanupClean=true：${lastVerdict.trim()}`);
      }
      if (!/overallFailed=false/.test(lastVerdict)) {
        fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行沒有 overallFailed=false：${lastVerdict.trim()}`);
      }
      const violationsMatch = lastVerdict.match(/artifactViolations=(\d+)/);
      if (!violationsMatch) {
        fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行找不到 artifactViolations 欄位，無法核對：${lastVerdict.trim()}`);
      } else if (violationsMatch[1] !== '0') {
        fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行 artifactViolations=${violationsMatch[1]}（非 0），跟成功宣稱矛盾`);
      }
    }
    const lastLine = lines.length > 0 ? lines[lines.length - 1] : '';
    if (!/最終結果：PASSED/.test(lastLine)) {
      fail(`${runDirName}/harness.log 最後一行不是「最終結果：PASSED」（實際："${lastLine.trim()}"），不得判定成功`);
    }
  }

  // --- playwright-results.json（decision470 新增：結構化逐案核對／identity／controls oracle） ---
  if (!existsSync(resultsFile)) {
    fail(`${runDirName}/playwright-results.json 不存在——CI-only config 應該產生 JSON reporter 輸出，缺漏視為證據不足`);
    return;
  }
  verifyPackageCopy(`${runDirName}/playwright-results.json`, resultsFile, path.join(packageArtifactsRoot, 'playwright-results.json'));

  let report;
  try {
    report = JSON.parse(readFileSync(resultsFile, 'utf8'));
  } catch (e) {
    fail(`${runDirName}/playwright-results.json 無法解析（${e.message}），成功宣稱下不得信任內容`);
    return;
  }
  evaluateJsonReport(report, runDirName);
}

// review round 5（#483 S1）：JSONL 領域證據檔——每行一個 JSON 物件，非空
// 檔且每一行都要能解析成 JSON，否則視為缺漏／損毀。不重新實作完整的
// protocol judge（那是 spec 內 judge 模組的職責），只核對「這是真的、非空
// 的、內容型別正確的證據」與最基本的 identity（decision／wsid 等）。
//
// review round 6（#489 F2）：`identityFacts` 是同一次 evaluateClaimedSuccessRun
// 呼叫內、跨 domain 檔案共用的可變狀態——app-audit.jsonl 先從
// codex_approval_request 記錄核對 thread/turn/item identity 並建立
// identityFacts.wsid，後面處理的 app-wire-log.jsonl／
// app-workspace-sessions.json 再核對是否用同一個 wsid。
function checkJsonlDomainFile(domainFile, domainPath, runDirName, entry, identityFacts = {}) {
  const raw = readFileSync(domainPath, 'utf8');
  if (raw.trim().length === 0) {
    fail(`${runDirName}/${domainFile} 是空檔案，成功宣稱下不得為空`);
    return;
  }
  const lines = raw.split('\n').map((l) => l.trim()).filter((l) => l.length > 0);
  const records = [];
  for (const line of lines) {
    try {
      records.push(JSON.parse(line));
    } catch (e) {
      fail(`${runDirName}/${domainFile} 有無法解析的 JSONL 行（${e.message}）`);
      return;
    }
  }

  if (domainFile === 'app-audit.jsonl' || domainFile.endsWith('/audit.jsonl')) {
    // review round 5（S1）：codex_approval_decision 記錄必須存在，且
    // decision 對上這個 entry 的獨立期望（scenarios.ts 登記表，經
    // ci-e2e-entries.mjs expectedCodexScenarioDecisionForEntry() 換算）——
    // 只在 codex 五案適用（claude 案的 audit.jsonl 走 Claude 自己的 broker
    // audit schema，不含 codex_approval_decision）。
    const expectedDecision = expectedCodexScenarioDecisionForEntry(entry);
    if (expectedDecision !== null) {
      const decisionRecord = records.find((r) => r && r.kind === 'codex_approval_decision');
      if (!decisionRecord) {
        fail(`${runDirName}/${domainFile} 找不到 kind="codex_approval_decision" 的紀錄`);
      } else if (decisionRecord.data?.decision !== expectedDecision) {
        fail(`${runDirName}/${domainFile} 的 codex_approval_decision.decision="${decisionRecord.data?.decision}"，預期 "${expectedDecision}"`);
      }
    }
    // review round 7（#491 G1）：claude-recovery/round{1,2}/audit.jsonl——
    // reviewer 已證實把 round1/round2 的 audit.jsonl（連同 sessions.json／
    // workspace-sessions.json）整批互換，evaluator 仍 rc0。這兩個檔案本來
    // 就落在 `domainFile.endsWith('/audit.jsonl')`，但上面的
    // codex_approval_decision 分支只在 expectedDecision!==null（codex 案）
    // 才動作，Claude 案原本完全沒有檢查。preserveStateFiles()
    // （claudeSessionRecovery.spec.ts）在每輪結束時把「當下累積」的
    // audit.jsonl 快照下來，所以 round1 快照只該有 round1 的 decision，
    // round2 快照（同一份持續累加的 audit.jsonl）該同時有 round1／round2
    // 兩輪的 decision——用「這份快照裡最後一筆 decision 的 id」對上
    // claude-recovery/judgement.json 已核對過的
    // judged.agreedApprovalIds[該輪 index]，round1 快照卻混進 round2 的
    // decision（或反過來）都會讓「最後一筆」對不上預期輪次。
    if (domainFile.startsWith('claude-recovery/round') && domainFile.endsWith('/audit.jsonl')) {
      const roundKey = domainFile.includes('round1') ? 'round1' : 'round2';
      const fact = identityFacts[`claudeRecovery_${roundKey}`];
      const decisionRecords = records.filter((r) => r && r.kind === 'decision');
      if (decisionRecords.length === 0) {
        fail(`${runDirName}/${domainFile} 找不到 kind="decision" 的記錄`);
      } else {
        const last = decisionRecords[decisionRecords.length - 1];
        if (typeof last.data?.id !== 'string' || last.data.id === '') {
          fail(`${runDirName}/${domainFile} 最後一筆 decision 記錄缺 data.id（非空字串）`);
        } else if (fact?.approvalId && last.data.id !== fact.approvalId) {
          fail(
            `${runDirName}/${domainFile} 最後一筆 decision id="${last.data.id}"，`
              + `與 claude-recovery/judgement.json 的 judged.agreedApprovalIds[${roundKey === 'round1' ? 0 : 1}]="${fact.approvalId}" 不符`
              + '——可能是別的輪次的 audit 原文被冒充（見 preserveStateFiles() 逐輪快照契約）',
          );
        }
      }
    }
    // review round 6（#489 F2）：reviewer 已證實把整份 app-audit.jsonl（連
    // 同其餘五個檔案）換成另一個真實 run（相同 decision，不同 scenario）的
    // 原文，evaluator 仍 rc0——decision 相符不足以區分「同為 -allow 的不同
    // scenario」。這裡另外核對 codex_approval_request 記錄的
    // raw_params.threadId／turnId／itemId 是否對得上 scenario-config.json
    // 已驗證過的 identity（expectedScenarioConfigIdentity()，同一份權威來
    // 源），並記下 wsid 供後面的 app-wire-log.jsonl／
    // app-workspace-sessions.json 交叉核對。只在 codex 五案（有
    // scenario-config.json identity 可比對）適用。
    if (domainFile === 'app-audit.jsonl' && CODEX_SCENARIO_IDS.has(entry.id)) {
      const identity = expectedScenarioConfigIdentity(entry, runDirName);
      const requestRecord = records.find((r) => r && r.kind === 'codex_approval_request');
      if (!requestRecord) {
        fail(`${runDirName}/${domainFile} 找不到 kind="codex_approval_request" 的紀錄，無法核對 thread/turn/item identity`);
      } else {
        const rp = requestRecord.data?.raw_params;
        if (!rp || rp.threadId !== identity.threadId || rp.turnId !== identity.turnId || rp.itemId !== identity.itemId) {
          fail(
            `${runDirName}/${domainFile} 的 codex_approval_request.raw_params={threadId:${JSON.stringify(rp?.threadId)},`
              + `turnId:${JSON.stringify(rp?.turnId)},itemId:${JSON.stringify(rp?.itemId)}}，`
              + `預期 {threadId:${JSON.stringify(identity.threadId)},turnId:${JSON.stringify(identity.turnId)},itemId:${JSON.stringify(identity.itemId)}}`
              + '——可能是別的 run／scenario 的 audit 原文被冒充',
          );
        }
        if (typeof requestRecord.data?.wsid !== 'string' || requestRecord.data.wsid === '') {
          fail(`${runDirName}/${domainFile} 的 codex_approval_request.wsid 不是非空字串，無法作為後續交叉核對的 identity 基準`);
        } else {
          identityFacts.wsid = requestRecord.data.wsid;
        }
      }
    }
  } else if (domainFile === 'app-wire-log.jsonl') {
    // 至少一筆合法 frame（{dir, raw}），identity 由 meta 檔另外核對。
    if (!records.some((r) => r && typeof r === 'object' && typeof r.dir === 'string')) {
      fail(`${runDirName}/${domainFile} 沒有任何看起來合法的 wire frame（缺 dir 欄位）`);
    }
    // review round 6（#489 F2）：若 app-audit.jsonl 已經建立本次 run 的
    // wsid，這裡至少要有一筆 wire frame 用同一個 wsid——不然這份 wire log
    // 可能是別的 run 的原文被冒充（reviewer 反例正是整批互換）。
    if (identityFacts.wsid) {
      const hasMatchingWsid = records.some((r) => r && typeof r === 'object' && r.wsid === identityFacts.wsid);
      if (!hasMatchingWsid) {
        fail(`${runDirName}/${domainFile} 沒有任何 wire frame 的 wsid="${identityFacts.wsid}"（本次 approval 的 wsid，見 app-audit.jsonl 的 codex_approval_request）——可能是別的 run 的 wire log 被冒充`);
      }
    }
  } else if (domainFile === 'scenario-wire.log' && CODEX_SCENARIO_IDS.has(entry.id)) {
    // review round 6（#489 F2）：fake app-server 的 thread/start 回覆帶
    // `result.thread.id`，對得上 scenario-config.json 的 threadId——這是
    // scenario-wire.log 本身既有的 identity 欄位，不是新造的。
    const identity = expectedScenarioConfigIdentity(entry, runDirName);
    const hasMatchingThread = records.some((r) => r?.frame?.result?.thread?.id === identity.threadId);
    if (!hasMatchingThread) {
      fail(`${runDirName}/${domainFile} 沒有任何 frame 的 result.thread.id="${identity.threadId}"（thread/start 的回覆）——可能是別的 run 的 wire log 原文被冒充`);
    }
  }
  // claude-broker-audit.raw.jsonl／claude-recovery 下的
  // exit-observations.jsonl／dom-observations 相關 jsonl：只要求非空且逐行
  // 可解析（上面已經做到），沒有額外的 identity 欄位可低成本核對，不在這裡
  // 過度宣稱已驗證內容語意。
}

// review round 5（#483 S1／S2）：JSON 物件型領域證據檔的 schema／identity
// 核對——沿用既有 gate-flow.json／gate-evidence.json／control-a/b-evidence.json
// 分支，新增 8 個 scenario entry 的 producer 檔（見 ci-e2e-entries.mjs
// requiredDomainFilesForEntry() 頭註完整 file:line 對照）。
function checkJsonDomainFile(domainFile, domainParsed, runDirName, entry, identityFacts = {}) {
  if (domainFile === 'gate-flow.json') {
    const expectedFlow = expectedGateFlowForEntry(entry);
    if (domainParsed.runId !== runDirName) {
      fail(`${runDirName}/gate-flow.json 的 runId="${domainParsed.runId}" 與目錄名不一致，identity 異常`);
    }
    if (domainParsed.flow !== expectedFlow) {
      fail(`${runDirName}/gate-flow.json 的 flow="${domainParsed.flow}"，預期 "${expectedFlow}"——可能是別的 gate flow 的證據被冒充成這個 entry`);
    }
    const expectedSpecFile = entry.expectedSpecFiles[0]?.split('/').pop();
    if (domainParsed.specFile !== expectedSpecFile) {
      fail(`${runDirName}/gate-flow.json 的 specFile="${domainParsed.specFile}"，預期 "${expectedSpecFile}"`);
    }
    return;
  }
  if (domainFile === 'gate-evidence.json') {
    const expectedFlow = expectedGateFlowForEntry(entry);
    if (domainParsed.runId !== runDirName) {
      fail(`${runDirName}/gate-evidence.json 的 runId="${domainParsed.runId}" 與目錄名不一致，identity 異常`);
    }
    if (domainParsed.flow !== expectedFlow) {
      fail(`${runDirName}/gate-evidence.json 的 flow="${domainParsed.flow}"，預期 "${expectedFlow}"`);
    }
    if (domainParsed.finalStatus !== 'passed') {
      fail(`${runDirName}/gate-evidence.json 的 finalStatus="${domainParsed.finalStatus}"，成功宣稱要求恰好是 "passed"（見 gateEvidence.ts flush() 契約）`);
    }
    // review round 5（S2）：reviewer 反例 gate-journal-absent——先前只核對
    // finalStatus，從未核對 bodyStatus（gateEvidence.ts:139 的
    // `bodyStatus: finalStatus`，即 test.afterEach 呼叫 flush() 時傳入的
    // 原始 finalStatus 參數，在 journal 不完整時會被 flush() 內部邏輯降級
    // 成 recordedStatus=failed 但 bodyStatus 仍保留原始值——見
    // gateEvidence.ts:120-140）。bodyStatus!=='passed' 代表 test body 本身
    // 沒有以 passed 收尾（或 journal 快照顯示不完整、payload 被竄改），跟
    // 「成功宣稱」矛盾，即使 finalStatus 顯示 passed 也不得放行。
    if (domainParsed.bodyStatus !== 'passed') {
      fail(`${runDirName}/gate-evidence.json 的 bodyStatus="${domainParsed.bodyStatus}"，與 finalStatus="${domainParsed.finalStatus}" 矛盾——成功宣稱要求兩者都是 "passed"（見 gateEvidence.ts flush() payload）`);
    }
    if (!Array.isArray(domainParsed.steps)) {
      fail(`${runDirName}/gate-evidence.json 缺 steps 陣列`);
    }
    return;
  }
  if (domainFile === 'control-a-evidence.json' || domainFile === 'control-b-evidence.json') {
    // review round 5（S2）：reviewer 反例 controls-domain-null——先前只核對
    // `'wrapperEvidence' in domainParsed`（鍵存在即可，null 值也算存
    // 在），改成核對型別：必須是非空陣列，且每個元素的 actualDelayMs 是數
    // 字（見 save-detection.spec.ts writeEvidence() 呼叫、
    // `expect(wrapperEvidence.length,...).toBe(1)` 的既有契約）。
    const we = domainParsed.wrapperEvidence;
    if (!Array.isArray(we) || we.length === 0) {
      fail(`${runDirName}/${domainFile} 的 wrapperEvidence 不是非空陣列（實際：${JSON.stringify(we)}）——見 save-detection.spec.ts 的 writeEvidence() 呼叫與既有 wrapperEvidence.length===1 契約`);
    } else if (!we.every((e) => e && typeof e === 'object' && typeof e.actualDelayMs === 'number')) {
      fail(`${runDirName}/${domainFile} 的 wrapperEvidence 元素缺 actualDelayMs（數字）欄位`);
    }
    return;
  }

  // --- S1：8 個 scenario entry 的領域證據檔 ---
  if (domainFile === 'scenario-config.json') {
    const identity = expectedScenarioConfigIdentity(entry, runDirName);
    for (const [k, v] of Object.entries(identity)) {
      if (domainParsed[k] !== v) {
        fail(`${runDirName}/scenario-config.json 的 ${k}="${domainParsed[k]}"，預期 "${v}"——identity 異常（可能是同一支 spec 底下不同案例互換，見 scenarios.ts buildConfig()）`);
      }
    }
    const expectedMethod = expectedScenarioApprovalMethodForEntry(entry);
    if (expectedMethod !== null && domainParsed.approvalMethod !== expectedMethod) {
      fail(`${runDirName}/scenario-config.json 的 approvalMethod="${domainParsed.approvalMethod}"，預期 "${expectedMethod}"`);
    }
    if (entry.id === 'commandExecution-recovery') {
      const second = expectedScenarioSecondTurnIdentity(runDirName);
      const actualSecond = domainParsed.secondTurn ?? {};
      for (const [k, v] of Object.entries(second)) {
        if (actualSecond[k] !== v) {
          fail(`${runDirName}/scenario-config.json 的 secondTurn.${k}="${actualSecond[k]}"，預期 "${v}"`);
        }
      }
    }
    return;
  }
  if (domainFile === 'scenario-wire.log.manifest.json') {
    if (domainParsed.exitCode !== 0) {
      fail(`${runDirName}/scenario-wire.log.manifest.json 的 exitCode=${JSON.stringify(domainParsed.exitCode)}，成功宣稱要求恰好是 0（fake app-server 應乾淨收尾，見 codexApproval.spec.ts:364-374）`);
    }
    // review round 6（#489 F2）：manifest 本身已經帶 scenario／
    // approvalMethod／approvalRequestId／decisionReceived 這幾個
    // producer 既有的 identity 欄位（見真實 run 的
    // scenario-wire.log.manifest.json），先前只驗 exitCode，讓 reviewer 能
    // 把整份 manifest 換成另一個真實 run（同為 -allow、decisionReceived
    // 同樣是 accept）的原文仍 rc0。這裡逐欄核對，對得上
    // scenario-config.json 已驗證過的同一份 identity 權威來源
    // （expectedScenarioConfigIdentity()）。
    if (CODEX_SCENARIO_IDS.has(entry.id)) {
      if (domainParsed.scenario !== entry.env.E2E_SCENARIO) {
        fail(`${runDirName}/scenario-wire.log.manifest.json 的 scenario="${domainParsed.scenario}"，預期 "${entry.env.E2E_SCENARIO}"——可能是別的 scenario/run 的 manifest 被冒充`);
      }
      const expectedMethod = expectedScenarioApprovalMethodForEntry(entry);
      if (expectedMethod !== null && domainParsed.approvalMethod !== expectedMethod) {
        fail(`${runDirName}/scenario-wire.log.manifest.json 的 approvalMethod="${domainParsed.approvalMethod}"，預期 "${expectedMethod}"`);
      }
      const identity = expectedScenarioConfigIdentity(entry, runDirName);
      if (domainParsed.approvalRequestId !== identity.approvalRequestId) {
        fail(`${runDirName}/scenario-wire.log.manifest.json 的 approvalRequestId="${domainParsed.approvalRequestId}"，預期 "${identity.approvalRequestId}"——可能是別的 run 的 manifest 被冒充`);
      }
      const expectedDecision = expectedCodexScenarioDecisionForEntry(entry);
      if (expectedDecision !== null && domainParsed.decisionReceived !== expectedDecision) {
        fail(`${runDirName}/scenario-wire.log.manifest.json 的 decisionReceived="${domainParsed.decisionReceived}"，預期 "${expectedDecision}"`);
      }
    }
    return;
  }
  if (domainFile === 'app-workspace-sessions.json') {
    const entries = domainParsed.entries;
    const entryList = entries && typeof entries === 'object' ? Object.entries(entries) : [];
    const values = entryList.map(([, v]) => v);
    if (values.length === 0 || !values.some((v) => v && v.provider === 'codex')) {
      fail(`${runDirName}/app-workspace-sessions.json 沒有 provider="codex" 的登記項目（見 codexApproval.spec.ts:203-208）`);
      return;
    }
    // review round 6（#489 F2）：登記項目的 resume_session_id 就是這次
    // approval 的 threadId（見真實 run 樣本：
    // entries["<wsid>"].resume_session_id === "b3a2b2-thread-<runId>"），
    // 對得上 scenario-config.json 已驗證過的同一份 identity。wsid（entries
    // 的 key）另外跟 app-audit.jsonl 建立的 identityFacts.wsid 交叉核對
    // （若該檔已經處理過）。
    if (CODEX_SCENARIO_IDS.has(entry.id)) {
      const identity = expectedScenarioConfigIdentity(entry, runDirName);
      const match = entryList.find(([, v]) => v && v.provider === 'codex' && v.resume_session_id === identity.threadId);
      if (!match) {
        fail(`${runDirName}/app-workspace-sessions.json 沒有 provider="codex" 且 resume_session_id="${identity.threadId}" 的登記項目——可能是別的 run 的 session 登記被冒充`);
      } else if (identityFacts.wsid && match[0] !== identityFacts.wsid) {
        fail(`${runDirName}/app-workspace-sessions.json 的登記 wsid="${match[0]}"，與 app-audit.jsonl 的 codex_approval_request.wsid="${identityFacts.wsid}" 不符`);
      }
    }
    return;
  }
  if (domainFile === 'app-wire-log.meta.json') {
    if (domainParsed.provider !== 'codex') {
      fail(`${runDirName}/app-wire-log.meta.json 的 provider="${domainParsed.provider}"，預期 "codex"（見 wireEvidence.ts validateWireMeta()）`);
    }
    if (domainParsed.exit_code !== 0) {
      fail(`${runDirName}/app-wire-log.meta.json 的 exit_code=${JSON.stringify(domainParsed.exit_code)}，預期 0`);
    }
    // review round 6（#489 F2）：argv 記錄的是這次 run 專屬 toolsDir 底下啟
    // 動的 fake codex-cli 路徑（見 run-env.json toolsDir／app-audit.jsonl
    // tools_dir 同一慣例，路徑含 runDirName），不含本次 run 目錄名稱視為別
    // 的 run 的 wire meta 被冒充。
    const argv = domainParsed.argv;
    if (!Array.isArray(argv) || !argv.some((a) => typeof a === 'string' && a.includes(runDirName))) {
      fail(`${runDirName}/app-wire-log.meta.json 的 argv=${JSON.stringify(argv)} 沒有任何路徑含本次 run 目錄名稱 "${runDirName}"——可能是別的 run 的 wire meta 被冒充`);
    }
    return;
  }
  if (domainFile === 'claude-expectation.json') {
    if (entry.id === 'claude-approval-recovery') {
      if (!Array.isArray(domainParsed.rounds) || domainParsed.rounds.length !== 2) {
        fail(`${runDirName}/claude-expectation.json 的 rounds 不是長度 2 的陣列（見 buildClaudeRecoveryExpectation()）`);
      } else {
        // global-setup.scenario.ts persists the builder's rounds, not its
        // top-level sessionId. Bind their identities to this run independently.
        const sessionId = `b3a2b2-claude-session-${runDirName}`;
        for (const [index, round] of domainParsed.rounds.entries()) {
          if (round?.round !== index + 1
            || round?.approval?.sessionId !== sessionId
            || round?.resume !== (index === 0 ? null : sessionId)) {
            fail(`${runDirName}/claude-expectation.json 的 round ${index + 1} 身分不符合本次 run 的 session/resume 契約`);
          }
        }
      }
    } else {
      const expectedDecision = expectedClaudeProtocolDecisionForEntry(entry);
      if (domainParsed.approval?.decision !== expectedDecision) {
        fail(`${runDirName}/claude-expectation.json 的 approval.decision="${domainParsed.approval?.decision}"，預期 "${expectedDecision}"`);
      }
    }
    return;
  }
  if (domainFile === 'app-binary-identity.json') {
    if (typeof domainParsed.pid !== 'number' || typeof domainParsed.sha256 !== 'string' || typeof domainParsed.canonicalPath !== 'string') {
      fail(`${runDirName}/app-binary-identity.json 缺 pid（數字）／sha256（字串）／canonicalPath（字串）任一欄位`);
    }
    return;
  }
  if (domainFile === 'cliinfo.json') {
    if (domainParsed.toolsSource !== 'env' || domainParsed.workspaceSource !== 'env') {
      fail(`${runDirName}/cliinfo.json 的 toolsSource/workspaceSource 不是 "env"（實際：${domainParsed.toolsSource}/${domainParsed.workspaceSource}）`);
    }
    if (domainParsed.startupError !== '') {
      fail(`${runDirName}/cliinfo.json 的 startupError="${domainParsed.startupError}"，成功宣稱要求為空字串`);
    }
    return;
  }
  if (domainFile === 'claude-broker-audit.json') {
    // review round 6（#489 F1）：array schema＋元素 shape＋必要 identity。不
    // 重寫 claudeApprovalJudge.ts 的完整 protocol judge（那是三方一致性判
    // 定的職責），只核對「這是真的、非空、shape 正確、且 decision 記錄的
    // behavior 對得上這個 entry 的核定決策」——足以拒絕空陣列／毀損元素／
    // 錯 identity（如 allow 案混進 deny 的 decision 記錄），不是全面放行任
    // 意陣列。
    if (!Array.isArray(domainParsed)) {
      fail(`${runDirName}/claude-broker-audit.json 不是陣列（實際型別：${typeof domainParsed}）——selectBrokerAuditForApproval() 的回傳型別是 unknown[]（claudeAppEvidence.ts:26）`);
      return;
    }
    if (domainParsed.length === 0) {
      fail(`${runDirName}/claude-broker-audit.json 是空陣列，成功宣稱下必須至少有一筆 broker audit 記錄（request／decision）`);
      return;
    }
    const shapeOk = domainParsed.every(
      (r) => r && typeof r === 'object' && !Array.isArray(r) && typeof r.kind === 'string' && typeof r.ts === 'string',
    );
    if (!shapeOk) {
      fail(`${runDirName}/claude-broker-audit.json 有元素缺 kind（字串）或 ts（字串）欄位，或元素本身不是物件`);
      return;
    }
    const decisionRecord = domainParsed.find((r) => r.kind === 'decision');
    if (!decisionRecord) {
      fail(`${runDirName}/claude-broker-audit.json 找不到 kind="decision" 的記錄`);
      return;
    }
    if (typeof decisionRecord.data?.id !== 'string' || decisionRecord.data.id === '') {
      fail(`${runDirName}/claude-broker-audit.json 的 decision 記錄缺 data.id（非空字串），identity 不可信`);
    } else {
      // review round 7（#491 G1）：記下這次 run 的 broker decision id，供
      // 下面 claude-ui-judgement.json 交叉核對 observedBrokerId／
      // agreedApprovalId 是不是同一個 run 的觀察值。
      identityFacts.claudeApprovalId = decisionRecord.data.id;
    }
    const expectedDecision = expectedClaudeProtocolDecisionForEntry(entry);
    if (expectedDecision !== null && decisionRecord.data?.behavior !== expectedDecision) {
      fail(`${runDirName}/claude-broker-audit.json 的 decision.data.behavior="${decisionRecord.data?.behavior}"，預期 "${expectedDecision}"——可能是別的決策案的 broker audit 被冒充`);
    }
    return;
  }
  if (domainFile === 'claude-ui-judgement.json') {
    if (!Array.isArray(domainParsed.violations) || domainParsed.violations.length > 0) {
      fail(`${runDirName}/claude-ui-judgement.json 的 violations 不是空陣列（實際：${JSON.stringify(domainParsed.violations)}）`);
    }
    // review round 7（#491 G1）：reviewer 已證實整份 claude-ui-judgement.json
    // 換成另一個真實 allow run 的原文（expectation／audit／run-env／CI
    // envelope 全部保留這次 run 的值），evaluator 仍 rc0——先前只驗
    // violations===[]，沒有把判定檔自報的 observedBrokerId／
    // agreedApprovalId／configWsid 跟這次 run 的其他獨立來源關聯起來。這裡
    // 補上最小身分關聯（欄位定義見 claudeAppEvidence.ts
    // judgeClaudeUiConsistency()，不重寫一份判定邏輯）：
    //   - observedBrokerId／agreedApprovalId 對上 claude-broker-audit.json
    //     已驗證過的 decision id（identityFacts.claudeApprovalId）。
    //   - configWsid 對上 run-state.json 觀察到的 WSID
    //     （identityFacts.claudeRunStateWsid）。
    if (typeof domainParsed.observedBrokerId !== 'string' || domainParsed.observedBrokerId === '') {
      fail(`${runDirName}/claude-ui-judgement.json 缺 observedBrokerId（非空字串），identity 不可信`);
    } else if (identityFacts.claudeApprovalId && domainParsed.observedBrokerId !== identityFacts.claudeApprovalId) {
      fail(
        `${runDirName}/claude-ui-judgement.json 的 observedBrokerId="${domainParsed.observedBrokerId}"，`
          + `與本次 claude-broker-audit.json 的 decision id "${identityFacts.claudeApprovalId}" 不符`
          + '——可能是別的 run 的 judgement 被冒充',
      );
    }
    if (typeof domainParsed.agreedApprovalId !== 'string' || domainParsed.agreedApprovalId === '') {
      fail(`${runDirName}/claude-ui-judgement.json 缺 agreedApprovalId（非空字串），identity 不可信`);
    } else if (domainParsed.agreedApprovalId !== domainParsed.observedBrokerId) {
      fail(
        `${runDirName}/claude-ui-judgement.json 的 agreedApprovalId="${domainParsed.agreedApprovalId}" 與 `
          + `observedBrokerId="${domainParsed.observedBrokerId}" 不一致（judgeClaudeUiConsistency() 通過時兩者應相同）`,
      );
    }
    if (typeof domainParsed.configWsid !== 'string' || domainParsed.configWsid === '') {
      fail(`${runDirName}/claude-ui-judgement.json 缺 configWsid（非空字串），identity 不可信`);
    } else if (identityFacts.claudeRunStateWsid === null) {
      fail(`${runDirName}/run-state.json 找不到 mcp-<WSID>.json 形式的 process 指令，無法核對 claude-ui-judgement.json 的 configWsid`);
    } else if (domainParsed.configWsid !== identityFacts.claudeRunStateWsid) {
      fail(
        `${runDirName}/claude-ui-judgement.json 的 configWsid="${domainParsed.configWsid}"，`
          + `與 run-state.json 觀察到的 WSID "${identityFacts.claudeRunStateWsid}" 不符`
          + '——可能是別的 run 的 judgement 被冒充',
      );
    }
    return;
  }
  if (domainFile === 'claude-recovery/judgement.json') {
    const v = domainParsed.judged?.violations;
    if (!Array.isArray(v) || v.length > 0) {
      fail(`${runDirName}/claude-recovery/judgement.json 的 judged.violations 不是空陣列（實際：${JSON.stringify(v)}）`);
    }
    // review round 7（#491 G1）：reviewer 已證實把 round1/round2 三個
    // registry/audit 檔（sessions.json／workspace-sessions.json／
    // audit.jsonl）互換（round1 變成含 round2 才會有的內容、round2 變成只
    // 剩 round1 的內容），claude-recovery/judgement.json／
    // claude-expectation.json／CI envelope 都不動，evaluator 仍 rc0——先前
    // 只驗 judged.violations，完全沒有把 judgement 已核對過的兩輪欄位
    // （claudeSessionRecovery.spec.ts 既有 judge 產生，不新造）跟兩輪各自
    // 的 registry/audit 原始檔關聯起來。這裡記下
    // judged.agreedApprovalIds／judged.round{1,2}.appBoundResume／
    // registryBinding.wsid 進 identityFacts，供下面 round1/round2 的
    // audit.jsonl／sessions.json／workspace-sessions.json 交叉核對。
    const ids = domainParsed.judged?.agreedApprovalIds;
    if (!Array.isArray(ids) || ids.length !== 2 || !ids.every((x) => typeof x === 'string' && x !== '')) {
      fail(`${runDirName}/claude-recovery/judgement.json 的 judged.agreedApprovalIds 不是長度 2 的非空字串陣列（實際：${JSON.stringify(ids)}）`);
    }
    for (const [idx, roundKey] of [[0, 'round1'], [1, 'round2']]) {
      // 真實 producer 的 round1／round2 是跟 judged 同層的頂層欄位（見真實
      // claude-approval-recovery run 的 claude-recovery/judgement.json 樣
      // 本），不是巢狀在 judged 底下。
      const roundInfo = domainParsed[roundKey];
      const resume = roundInfo?.appBoundResume;
      const wsid = roundInfo?.registryBinding?.wsid;
      if (typeof resume !== 'string' || resume === '' || typeof wsid !== 'string' || wsid === '') {
        fail(`${runDirName}/claude-recovery/judgement.json 的 judged.${roundKey} 缺 appBoundResume／registryBinding.wsid（非空字串）`);
        continue;
      }
      if (resume !== `b3a2b2-claude-session-${runDirName}`) {
        fail(`${runDirName}/claude-recovery/judgement.json 的 ${roundKey}.appBoundResume 不屬於本次 run`);
      }
      if (!identityFacts.claudeRunStateWsid || wsid !== identityFacts.claudeRunStateWsid) {
        fail(`${runDirName}/claude-recovery/judgement.json 的 ${roundKey}.registryBinding.wsid 與 run-state 的 WSID 不符或無法核對`);
      }
      identityFacts[`claudeRecovery_${roundKey}`] = {
        approvalId: Array.isArray(ids) ? ids[idx] : undefined,
        resume,
        wsid,
      };
    }
    return;
  }
  if (domainFile === 'claude-recovery/round1/sessions.json' || domainFile === 'claude-recovery/round2/sessions.json') {
    // review round 7（#491 G1）：round{N}/sessions.json 的登記 key（resume
    // session id）與其 wsid 必須對得上 claude-recovery/judgement.json 已核
    // 對過的同一輪 appBoundResume／registryBinding.wsid——不然可能是別輪次
    // 的 sessions.json 被冒充（reviewer 反例正是整批互換）。
    const roundKey = domainFile.includes('round1') ? 'round1' : 'round2';
    const fact = identityFacts[`claudeRecovery_${roundKey}`];
    if (fact) {
      const bound = domainParsed[fact.resume];
      if (!bound || typeof bound !== 'object') {
        fail(`${runDirName}/${domainFile} 沒有 key="${fact.resume}" 的登記（judged.${roundKey}.appBoundResume，見 claude-recovery/judgement.json）——可能是別輪次的 sessions.json 被冒充`);
      } else if (bound.wsid !== fact.wsid) {
        fail(`${runDirName}/${domainFile} 的 "${fact.resume}".wsid="${bound.wsid}"，與 judged.${roundKey}.registryBinding.wsid="${fact.wsid}" 不符`);
      }
    }
    return;
  }
  if (domainFile === 'claude-recovery/round1/workspace-sessions.json' || domainFile === 'claude-recovery/round2/workspace-sessions.json') {
    // review round 7（#491 G1）：同上，改核對 workspace-sessions.json 的
    // entries[wsid].resume_session_id。
    const roundKey = domainFile.includes('round1') ? 'round1' : 'round2';
    const fact = identityFacts[`claudeRecovery_${roundKey}`];
    if (fact) {
      const ent = domainParsed.entries?.[fact.wsid];
      if (!ent || typeof ent !== 'object') {
        fail(`${runDirName}/${domainFile} 沒有 wsid="${fact.wsid}" 的登記（judged.${roundKey}.registryBinding.wsid）——可能是別輪次的 workspace-sessions.json 被冒充`);
      } else if (ent.resume_session_id !== fact.resume) {
        fail(`${runDirName}/${domainFile} 的 entries["${fact.wsid}"].resume_session_id="${ent.resume_session_id}"，與 judged.${roundKey}.appBoundResume="${fact.resume}" 不符`);
      }
    }
    return;
  }
  // claude-recovery/round{1,2}/audit.jsonl 的 identity 核對在
  // checkJsonlDomainFile()（JSONL_DOMAIN_FILES 內），不在這裡。
}

/** @param {import('../../frontend/node_modules/playwright/types/testReporter.js').JSONReport} report */
function evaluateJsonReport(report, runDirName) {
  // review round 3（R3）：先前只在「是 Array 且非空」時才 fail——reviewer
  // 已重現把 errors 整個換成 `{"error":"not an array"}`（物件、非陣列）
  // 時，`Array.isArray()` 為 false，舊條件式的 `&&` 短路直接跳過，等於
  // 「型別錯誤」完全沒被檢查到。改成先驗型別，非陣列本身就是缺陷（JSONReport
  // 的 `errors` 依 Playwright 型別定義必須是 TestError[]）。
  if (!Array.isArray(report.errors)) {
    fail(`${runDirName}/playwright-results.json 的 errors 不是陣列（實際型別：${report.errors === null ? 'null' : typeof report.errors}），成功宣稱下視為不可信`);
  } else if (report.errors.length > 0) {
    fail(`${runDirName}/playwright-results.json 有 ${report.errors.length} 筆 top-level errors，成功宣稱下不得有全域錯誤`);
  }
  if (report.stats && typeof report.stats === 'object') {
    if (report.stats.unexpected !== 0) {
      fail(`${runDirName}/playwright-results.json stats.unexpected=${report.stats.unexpected}（非 0），有非預期結果`);
    }
    if (report.stats.flaky !== 0) {
      fail(`${runDirName}/playwright-results.json stats.flaky=${report.stats.flaky}（非 0），retry 應為 0，不允許 flaky`);
    }
  } else {
    fail(`${runDirName}/playwright-results.json 缺 stats 物件`);
  }

  // review round 2 修法（主 agent 用真實 run 目錄複核發現的真實 bug）：先前
  //用 `spec.file.endsWith(expected)` 去配 `entry.expectedSpecFiles`（例如
  // 預期 `'gates/gate1.spec.ts'`），但實測四個真實 gate1/gate2/stale/
  // controls run 的 `playwright-results.json`（見
  // `__fixtures__/real/PROVENANCE.md`）證實：`JSONReportSpec.file` 是**相對
  // 於 `config.rootDir`** 的路徑，不含 `gates/`／`controls/` 這種批次子目
  // 錄前綴（真實值是 `'gate1.spec.ts'`），而 `config.rootDir` 才是該次
  // invocation 的 testDir 絕對路徑（例如
  // `.../frontend/e2e/gates`）。`'gate1.spec.ts'.endsWith('gates/gate1.spec.ts')`
  // 恆為 false（actual 字串比 expected 還短），所以先前的比對必然把每一個
  // 真實 entry 都判成「預期 spec 檔缺席」＋「出現非預期的 spec 檔」。
  //
  // 修法：改成用 `config.rootDir` 的最後一段目錄名稱（basename）當前綴，還
  // 原成「相對於 frontend/e2e/ 的路徑」，再跟 entry.expectedSpecFiles 做**
  // 完全相等**比對（不是後綴比對）：
  //   - rootDir basename 是 `'e2e'`（default 的 testDir 就是 frontend/e2e/
  //     本身）→ 還原路徑＝`file`（無前綴）。
  //   - 其他（`'gates'`／`'controls'`／`'scenarios'`）→ 還原路徑＝
  //     `${basename}/${file}`。
  // 這個規則同時保留「錯 identity 必須拒絕」的能力：gate2 的 run 被拿來冒充
  // gate1 時，rootDir basename 仍是 `'gates'`（因為 gate1／gate2／stale 共
  // 用同一個 testDir），但 `file` 實際是 `'gate2.spec.ts'`，還原後是
  // `'gates/gate2.spec.ts'`，跟 gate1 entry 預期的 `'gates/gate1.spec.ts'`
  // 不相等——仍然會被判定缺案＋額外案，理由正確（檔名本身不同，不是路徑格
  // 式湊巧对不上）。
  //
  // `config.rootDir` 缺失或型別不對時視為無法核對 identity，直接 fail、不
  // 嘗試用 `file` 原始值退回比對（那正是先前出錯的做法）。
  const rootDir = report.config?.rootDir;
  if (typeof rootDir !== 'string' || rootDir.length === 0) {
    fail(`${runDirName}: playwright-results.json 的 config.rootDir 缺失或不是字串，無法核對 spec 檔 identity`);
    return;
  }
  const rootDirBasename = path.basename(rootDir);
  function reconstructFrontendRelativePath(specFile) {
    if (!specFile) return specFile;
    const normalized = specFile.replace(/\\/g, '/');
    return rootDirBasename === 'e2e' ? normalized : `${rootDirBasename}/${normalized}`;
  }

  /** @type {Map<string, import('../../frontend/node_modules/playwright/types/testReporter.js').JSONReportSpec[]>} */
  const specsByFile = new Map();
  function walkSuite(suite) {
    for (const spec of suite.specs ?? []) {
      const key = reconstructFrontendRelativePath(spec.file);
      if (!specsByFile.has(key)) specsByFile.set(key, []);
      specsByFile.get(key).push(spec);
    }
    for (const child of suite.suites ?? []) walkSuite(child);
  }
  for (const topSuite of report.suites ?? []) walkSuite(topSuite);

  const actualFiles = [...specsByFile.keys()].sort();
  const expectedFiles = [...entry.expectedSpecFiles].sort();
  const missing = expectedFiles.filter((f) => !actualFiles.includes(f));
  const extra = actualFiles.filter((f) => !expectedFiles.includes(f));
  if (missing.length > 0) {
    fail(`${runDirName}: 預期 spec 檔缺席（identity 異常）：${missing.join(', ')}`);
  }
  if (extra.length > 0) {
    fail(`${runDirName}: 出現非預期的 spec 檔（identity 異常／額外案）：${extra.join(', ')}`);
  }

  let totalTests = 0;
  for (const specs of specsByFile.values()) {
    for (const spec of specs) totalTests += spec.tests.length;
  }
  if (totalTests !== entry.expectedTestCount) {
    fail(`${runDirName}: test 總數=${totalTests}，預期 ${entry.expectedTestCount}（缺案／多案／重複案）`);
  }

  if (entry.id === 'controls') {
    evaluateControlsOracle(specsByFile, runDirName);
  } else {
    for (const [file, specs] of specsByFile) {
      for (const spec of specs) {
        for (const test of spec.tests) {
          checkOrdinaryTestExpectedPass(test, `${runDirName}/${file}#${spec.title}`);
        }
      }
    }
  }
}

function checkOrdinaryTestExpectedPass(test, label) {
  if (test.status === 'unexpected') {
    fail(`${label}: test.status=unexpected（實際結果與 expectedStatus 不符，expectedStatus=${test.expectedStatus}），成功宣稱下不允許`);
    return;
  }
  if (test.status === 'flaky') {
    fail(`${label}: test.status=flaky（曾 retry 才通過），契約要求零重試，不允許`);
    return;
  }
  if (test.status !== 'expected') {
    fail(`${label}: test.status=${test.status}，非成功宣稱下允許的值（expected）`);
    return;
  }
  if (test.expectedStatus !== 'passed') {
    fail(`${label}: expectedStatus=${test.expectedStatus}，非 controls entry 預期應為 passed`);
  }
  if (!Array.isArray(test.results) || test.results.length !== 1) {
    fail(`${label}: results.length=${test.results?.length ?? 'undefined'}，預期恰好 1（零重試）`);
    return;
  }
  const r = test.results[0];
  if (r.retry !== 0) {
    fail(`${label}: results[0].retry=${r.retry}（非 0），契約要求零重試`);
  }
  if (r.status !== 'passed') {
    fail(`${label}: results[0].status=${r.status}，預期 passed`);
  }
  // review round 5（#483 S2）：reviewer 反例 ordinary-result-error——一般
  // 「passed」的 test（非 controls entry，走這個函式的路徑）先前完全沒核
  // 對 results[0].errors／error，即使塞進
  // `errors=[{message:'afterEach failed'}]` 仍判定通過。成功宣稱下這兩個
  // 欄位必須合法且空（JSONReportResult 的 errors 依 Playwright 型別定義必
  // 須是 TestError[]，passed 且零重試時應為空陣列；error 是單一 TestError
  // 或 undefined）。
  if (!Array.isArray(r.errors)) {
    fail(`${label}: results[0].errors 不是陣列（實際型別：${r.errors === null ? 'null' : typeof r.errors}）`);
  } else if (r.errors.length > 0) {
    fail(`${label}: results[0].errors 有 ${r.errors.length} 筆（成功宣稱下應為空），實際：${findErrorMessages(r).join(' | ')}`);
  }
  if (r.error !== undefined && r.error !== null) {
    fail(`${label}: results[0].error 非空（成功宣稱下應為 undefined），實際：${JSON.stringify(r.error)}`);
  }
}

function findErrorMessages(result) {
  const msgs = [];
  if (result.error?.message) msgs.push(result.error.message);
  for (const e of result.errors ?? []) {
    if (e.message) msgs.push(e.message);
  }
  return msgs;
}

function evaluateControlsOracle(specsByFile, runDirName) {
  const saveDetection = specsByFile.get('controls/save-detection.spec.ts') ?? [];
  const reverseCheck = specsByFile.get('controls/reverse-check.spec.ts') ?? [];

  const allSaveDetectionTests = saveDetection.flatMap((spec) => spec.tests.map((t) => ({ spec, test: t })));
  const controlA = allSaveDetectionTests.find(({ test }) => test.title?.includes('control A') || test.title?.includes('control A:'))
    ?? allSaveDetectionTests.find(({ spec }) => spec.title?.startsWith('control A'));
  const controlB = allSaveDetectionTests.find(({ test }) => test.title?.includes('control B') || test.title?.includes('control B:'))
    ?? allSaveDetectionTests.find(({ spec }) => spec.title?.startsWith('control B'));

  if (!controlA) {
    fail(`${runDirName}: save-detection.spec.ts 找不到 control A 的 test（缺案）`);
  } else {
    const { test } = controlA;
    if (test.expectedStatus !== 'failed') {
      fail(`${runDirName}: control A expectedStatus="${test.expectedStatus}"，預期 failed（test.fail() 應該讓 expectedStatus=failed）`);
    }
    if (test.status === 'unexpected') {
      fail(`${runDirName}: control A 意外 pass（test.status=unexpected，expectedStatus=failed 但實際沒有失敗）——oracle 拒絕`);
    } else if (test.status !== 'expected') {
      fail(`${runDirName}: control A test.status="${test.status}"，非允許值（expected）——oracle 拒絕（含 skip）`);
    } else if (!Array.isArray(test.results) || test.results.length !== 1) {
      fail(`${runDirName}: control A results.length=${test.results?.length ?? 'undefined'}，預期恰好 1（零重試）`);
    } else {
      const r = test.results[0];
      if (r.retry !== 0) fail(`${runDirName}: control A results[0].retry=${r.retry}（非 0），契約要求零重試`);
      if (r.status !== 'failed') {
        fail(`${runDirName}: control A results[0].status="${r.status}"，預期 failed`);
      } else if (!Array.isArray(r.errors)) {
        fail(`${runDirName}: control A results[0].errors 不是陣列，無法核對錯誤內容`);
      } else if (r.errors.length !== 1) {
        // review round 3（R3）：先前用 `msgs.some(includes(target))`——
        // reviewer 已重現在正確的目標錯誤之外「追加」一筆
        // 'afterEach evidence persistence failure'，因為陣列裡仍然「存在
        // 一筆」符合 target 的訊息，`.some()` 照樣回 true，仍判定通過。改
        // 成先要求「恰好一筆」錯誤，不接受目標錯誤之外還混了其他錯誤
        // （setup／locator／network／evidence 任何一種都算）。
        const msgs = findErrorMessages(r);
        fail(
          `${runDirName}: control A results[0].errors 有 ${r.errors.length} 筆（預期恰好 1），`
            + `疑似目標斷言錯誤之外還混了其他錯誤（不能任何錯誤都接受）。實際：${msgs.join(' | ')}`,
        );
      } else {
        // review round 5（#483 S2）：reviewer 反例 controls-conflicting-error
        // ——先前只核對 `errors` 陣列（長度 1 即可），完全沒看
        // `results[0].error`（單數欄位）。reviewer 把 `error` 換成一則不
        // 相關訊息、`errors[0]` 維持目標訊息不變，`errors.length===1` 仍
        // 成立、`.some(includes(target))` 仍在 `errors` 裡找得到目標訊
        // 息，因此舊判定放行。
        //
        // 修法必須是「正規化」而非「去重比對是否逐位元組相等」：真實
        // Playwright JSON reporter 的 `results[0].error` 與
        // `results[0].errors[0]`（單一錯誤時）代表同一個錯誤，但兩者格式
        // 不逐位元組相同（`errors[0].message` 常帶額外的 code-frame 附加
        // 文字，`error.message` 沒有——見 `__fixtures__/real/` 的真實
        // controls 正例，用 Set 去重會誤判真實正例為「矛盾」）。正確的正
        // 規化是：`error`（若存在）與 `errors` 陣列裡的每一則訊息**各自獨
        // 立**都必須含目標斷言字串，不要求兩者逐字相等——這樣才能同時拒絕
        // 「error 是不相關訊息」（reviewer 反例）並接受「error 與
        // errors[0] 語意相同、格式不同」（真實正例）。
        const errorMsg = r.error?.message;
        const errorsMsgs = findErrorMessages({ errors: r.errors });
        const allMsgs = errorMsg !== undefined && errorMsg !== null ? [errorMsg, ...errorsMsgs] : errorsMsgs;
        const matchesTarget = allMsgs.length > 0 && allMsgs.every((m) => m.includes(CONTROL_A_TARGET_ASSERTION_MESSAGE));
        if (!matchesTarget) {
          fail(
            `${runDirName}: control A 的 error／errors 正規化核對後不是恰好一筆目標斷言錯誤`
              + `「${CONTROL_A_TARGET_ASSERTION_MESSAGE}」——error 與 errors 兩個表示可能互相矛盾，`
              + `或混了其他 setup／locator／network／evidence 錯誤（oracle 拒絕）。實際：${allMsgs.join(' | ') || '(無 error 訊息)'}`,
          );
        }
      }
    }
  }

  if (!controlB) {
    fail(`${runDirName}: save-detection.spec.ts 找不到 control B 的 test（缺案）`);
  } else {
    checkOrdinaryTestExpectedPass(controlB.test, `${runDirName}/controls/save-detection.spec.ts#control B`);
  }

  const allReverseCheckTests = reverseCheck.flatMap((spec) => spec.tests);
  if (allReverseCheckTests.length !== 1) {
    fail(`${runDirName}: reverse-check.spec.ts test 數=${allReverseCheckTests.length}，預期恰好 1`);
  } else {
    const t = allReverseCheckTests[0];
    // review round 3（R3）：先前的條件是
    // `t.status !== 'skipped' && r?.status !== 'skipped'`——只要兩者「其中
    // 一個」是 skipped 就不會 fail（AND 短路），reviewer 已重現
    // `test.status='skipped'` 但 `results[0].status='passed'`（不一致的
    // skip 狀態）仍判定通過。改成明確要求「test.status 與
    // results[0].status 都必須是 skipped」，兩者缺一即拒絕；並補上唯一
    // result／retry=0 的核對（decision.md R3：「reverse須同時具備一致的
    // expected/actual skip、唯一結果、retry0」）。
    // review round 5（#483 S2）：reviewer 反例 reverse-wrong-expected——先
    // 前完全沒核對 `t.expectedStatus`，只核對 `t.status`／
    // `results[0].status`。reviewer 把 `expectedStatus` 換成 `'failed'`，
    // 實際 `status`／`results[0].status` 仍是 `'skipped'`（未變動），舊判
    // 定仍放行——但 expectedStatus 本身已被竄改，代表這份證據的期望值不
    // 可信。E2E_CONTROLS_REVERSE_CHECK 未設定時，這個 test 的 expectedStatus
    // 契約上必須是 `'skipped'`，缺一併入拒絕條件。
    if (t.expectedStatus !== 'skipped') {
      fail(`${runDirName}: reverse-check.spec.ts expectedStatus="${t.expectedStatus}"，預期 "skipped"（本輪 E2E_CONTROLS_REVERSE_CHECK 未設定，expectedStatus 契約上必須是 skipped）`);
    }
    if (!Array.isArray(t.results) || t.results.length !== 1) {
      fail(`${runDirName}: reverse-check.spec.ts results.length=${t.results?.length ?? 'undefined'}，預期恰好 1`);
    } else {
      const r = t.results[0];
      if (t.status !== 'skipped' || r.status !== 'skipped') {
        fail(
          `${runDirName}: reverse-check.spec.ts 狀態不一致或非預期執行`
            + `（test.status="${t.status}"，results[0].status="${r.status}"，兩者都必須是 skipped）——`
            + '本輪 E2E_CONTROLS_REVERSE_CHECK 未設定，reverse-check 是唯一容許 skipped 的案例，其他任何結果都拒絕',
        );
      }
      if (r.retry !== 0) {
        fail(`${runDirName}: reverse-check.spec.ts results[0].retry=${r.retry}（非 0），契約要求零重試`);
      }
    }
  }
}
