#!/usr/bin/env node
// ci-e2e-entries.mjs — B3a-CI-1 單一權威來源：13 個 invocation 的 config／env／
// spec 映射（對照 b3a-ci-design-draft-v2.md 第 2 節矩陣，逐項以
// `git show 6c741761...:<path>` 或直接讀取原始碼核對過，見
// /Users/eason_tseng/b3a-evidence/ci-1/attempt-001/progress.md）。
//
// run-batch.mjs（批次執行）與離線 selftest 都 import 這份表，避免兩處各自
//維護一份容易漂移的清單。這裡只是資料＋純函式，不 spawn 任何行程。
'use strict';

/**
 * @typedef {Object} Entry
 * @property {string} id            唯一 entry id（等於 E2E_SCENARIO／E2E_GATE 值，
 *                                    或 'default'／'controls'）
 * @property {'smoke'|'gates'|'scenarios'} batch
 * @property {string} configPath    相對於 frontend/ 的 playwright config 路徑
 * @property {Record<string,string>} env  額外注入的 env（E2E_GATE／E2E_SCENARIO）
 * @property {string[]} expectedSpecFiles 相對於 frontend/e2e/ 的預期 spec 檔（唯一）
 * @property {number} expectedTestCount   預期 test 總數（含 skipped）
 */

/** @type {Entry[]} */
export const ENTRIES = [
  {
    id: 'default',
    batch: 'smoke',
    configPath: 'e2e/playwright.ci-default.config.ts',
    env: {},
    expectedSpecFiles: ['glossary.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'controls',
    batch: 'smoke',
    configPath: 'e2e/playwright.controls.config.ts',
    env: {},
    expectedSpecFiles: ['controls/save-detection.spec.ts', 'controls/reverse-check.spec.ts'],
    expectedTestCount: 3, // control A + control B + reverse-check（skipped）
  },
  {
    id: 'gate1',
    batch: 'gates',
    configPath: 'e2e/playwright.gates.config.ts',
    env: { E2E_GATE: 'gate1' },
    expectedSpecFiles: ['gates/gate1.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'gate2',
    batch: 'gates',
    configPath: 'e2e/playwright.gates.config.ts',
    env: { E2E_GATE: 'gate2' },
    expectedSpecFiles: ['gates/gate2.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'stale',
    batch: 'gates',
    configPath: 'e2e/playwright.gates.config.ts',
    env: { E2E_GATE: 'stale' },
    expectedSpecFiles: ['gates/stale.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'commandExecution-allow',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'commandExecution-allow' },
    expectedSpecFiles: ['scenarios/codexApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'commandExecution-deny',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'commandExecution-deny' },
    expectedSpecFiles: ['scenarios/codexApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'fileChange-allow',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'fileChange-allow' },
    expectedSpecFiles: ['scenarios/codexApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'fileChange-deny',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'fileChange-deny' },
    expectedSpecFiles: ['scenarios/codexApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'commandExecution-recovery',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'commandExecution-recovery' },
    expectedSpecFiles: ['scenarios/codexSessionRecovery.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'claude-approval-allow',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'claude-approval-allow' },
    expectedSpecFiles: ['scenarios/claudeApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'claude-approval-deny',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'claude-approval-deny' },
    expectedSpecFiles: ['scenarios/claudeApproval.spec.ts'],
    expectedTestCount: 1,
  },
  {
    id: 'claude-approval-recovery',
    batch: 'scenarios',
    configPath: 'e2e/playwright.ci-scenario.config.ts',
    env: { E2E_SCENARIO: 'claude-approval-recovery' },
    expectedSpecFiles: ['scenarios/claudeSessionRecovery.spec.ts'],
    expectedTestCount: 1,
  },
];

export const BATCH_NAMES = /** @type {const} */ (['smoke', 'gates', 'scenarios']);

// decision470 收斂實作選擇 #1：批次名稱對應的 label（事件 label 名稱須精確相符）。
export const BATCH_LABELS = {
  smoke: 'b3a-ci-smoke',
  gates: 'b3a-ci-gates',
  scenarios: 'b3a-ci-scenarios',
};

// decision470 收斂實作選擇 #3：逐案 wrapper deadline（沿用既有 25min）、
// 批次 job/step 上限（初始假設值，need 首輪遠端實測校準，非最終值）。
export const WRAPPER_DEADLINE_SECONDS = 1500; // 25 min
export const WRAPPER_GRACE_SECONDS = 120; // 2 min
// review round 5（#483 直接裁定）：先前這裡仍是舊值 30／105／135／285，
// workflow（b3a-e2e-ci.yml）與 timing 文件已經先行改成
// 34／109／139／289（setup 多一個「generate CI invocation envelope」
// step，+1min；job cap = entry數×30min + setup34min + upload15min），但這
// 份單一權威來源沒有同步——reviewer 明確點名「同步 entries 內仍舊的
// SETUP_BUDGET_MINUTES=30 與 105/135/285、workflow 及 timing 文件」。這裡
// 補上，workflow／timing-check 用同一組常數核對，不再各自維護一份可能漂
// 移的數字。109+139+289=537（三個 job cap 合計，非 runner 使用授權，只是
// 可核算的修訂後設計上限）。
export const SETUP_BUDGET_MINUTES = 34;
export const TEARDOWN_BUDGET_MINUTES = 15;
export const BATCH_JOB_TIMEOUT_MINUTES = {
  smoke: 109,
  gates: 139,
  scenarios: 289,
};
// review round 5（S3）：單一 entry 的總預算上限（含異常路徑）——決定文件
// 明文「每個entry30分鐘的上限必須連異常路徑一起算進去」。run-batch.mjs 的
// 有界 spawn 策略（SIGTERM→SIGKILL，只送給自己持有的子行程）必須讓
// wrapper 階段＋package 階段的最壞情況總和 ≤ 這個值，不能只在正常路徑内
// 满足。
export const ENTRY_HARD_BUDGET_SECONDS = 1800; // 30 min

export function entriesForBatch(batch) {
  if (!BATCH_NAMES.includes(batch)) {
    throw new Error(`entriesForBatch: 未知批次 "${batch}"，僅接受 ${BATCH_NAMES.join('｜')}`);
  }
  return ENTRIES.filter((e) => e.batch === batch);
}

export function entryById(id) {
  return ENTRIES.find((e) => e.id === id) ?? null;
}

// 供 evaluator／run-batch 用：某個 entry 應該由哪個既有 npm script 對應的
// config 檔驅動（純資訊性，實際 spawn 一律直接呼叫 run-e2e.mjs，不經 npm，
// 對齊 PR#12 R3 修法：避免經 npm 轉發訊號的不確定性）。
export function realCommandForEntry(entry, { frontendRelative = true } = {}) {
  const configArg = entry.configPath; // 已經是相對於 frontend/ 的路徑
  return {
    cmd: 'node',
    args: [
      frontendRelative ? 'frontend/e2e/scripts/run-e2e.mjs' : 'e2e/scripts/run-e2e.mjs',
      configArg,
    ],
  };
}

// B3a-CI-1 review round 2（主 agent 用真實 run 目錄複核 evaluator 發現的缺
// 陷）：先前 evaluator 把 `OTHER_REQUIRED_FILES` 當成「每個 entry 一律必
// 備」的固定清單，其中 `chrome-argv.txt` 實際上只有呼叫
// `captureChromeArgv()` 的 spec 才會產生，導致四個真實通過的 controls／
// gates run 全部被誤判「缺必要證據」。逐一核對本輪找到的證據（file:line）：
//   - run-env.json：`frontend/e2e/support/env.ts:56`（writeRunEnv 寫入）、
//     `frontend/e2e/support/executionMode.ts:136`（讀取）——global-setup.ts
//     共用邏輯，default／controls／gates（經 global-setup.gates.ts 委派
//     global-setup.ts）／scenario（global-setup.scenario.ts 自己也呼叫
//     writeRunEnv 同一支模組）皆會寫，經真實 controls／gate1 run 目錄 `ls`
//     核對存在。
//   - wails-dev.log：`frontend/e2e/support/processTree.ts:51,207`，同上共
//     用模組，經真實 run 目錄核對存在。
//   - invocations.log：`frontend/e2e/support/fakeCli.ts:56/95`、
//     `frontend/e2e/support/scenario/scenarioCli.ts:46`、
//     `frontend/e2e/support/scenario/claudeScenarioCli.ts:59`（各入口各自
//     的 fake CLI 都寫）、`frontend/e2e/global-teardown.ts:261-264`（複製進
//     artifacts），經真實 run 目錄核對存在。
//   - preflight-invocations.log：`frontend/e2e/support/preflight.ts:82`，
//     經真實 run 目錄核對存在。
//   - network-samples.log：`frontend/e2e/support/networkSampler.ts:83`、
//     `frontend/e2e/global-teardown.ts:304`，經真實 run 目錄核對存在。
//   - browser-network-violations.log：`frontend/e2e/support/networkGuard.ts:55`，
//     每支 spec（glossary／save-detection／reverse-check／gate1／gate2／
//     stale／全部 scenario spec）都呼叫 `installNetworkGuard()`，經真實
//     run 目錄核對存在。
//   - artifact-integrity-baseline.json：`frontend/e2e/global-setup.ts:160`、
//     `frontend/e2e/global-setup.scenario.ts:204`；gates 入口
//     `frontend/e2e/global-setup.gates.ts:14,18-...` 委派
//     `defaultGlobalSetup`（即 `global-setup.ts`），因此 gates 也會寫，經
//     真實 gate1 run 目錄核對存在。
//   - chrome-argv.txt：**只有**
//     `frontend/e2e/glossary.spec.ts:35`（default）、
//     `frontend/e2e/scenarios/codexApproval.spec.ts:76`（scenario：
//     commandExecution-allow/deny、fileChange-allow/deny）、
//     `frontend/e2e/scenarios/codexSessionRecovery.spec.ts:66`（scenario：
//     commandExecution-recovery）、
//     `frontend/e2e/scenarios/claudeApproval.spec.ts:83`（scenario：
//     claude-approval-allow/deny）、
//     `frontend/e2e/scenarios/claudeSessionRecovery.spec.ts:109`（scenario：
//     claude-approval-recovery）呼叫 `captureChromeArgv()`；
//     `save-detection.spec.ts`／`reverse-check.spec.ts`／
//     `gate1.spec.ts`／`gate2.spec.ts`／`stale.spec.ts` 都沒有呼叫，經真實
//     controls／gate1／gate2／stale run 目錄核對**確實不存在**這個檔案。
export const UNIVERSAL_REQUIRED_FILES = [
  'run-env.json',
  'wails-dev.log',
  'invocations.log',
  'preflight-invocations.log',
  'network-samples.log',
  'browser-network-violations.log',
  'artifact-integrity-baseline.json',
];

const CHROME_ARGV_FILE = 'chrome-argv.txt';
const CHROME_ARGV_ENTRY_IDS = new Set([
  'default',
  'commandExecution-allow',
  'commandExecution-deny',
  'fileChange-allow',
  'fileChange-deny',
  'commandExecution-recovery',
  'claude-approval-allow',
  'claude-approval-deny',
  'claude-approval-recovery',
]);

/** 依 entry 回傳「成功宣稱時必須存在」的證據檔清單（固定必要檔＋依 entry 決定要不要 chrome-argv.txt）。 */
export function otherRequiredFilesForEntry(entry) {
  return CHROME_ARGV_ENTRY_IDS.has(entry.id)
    ? [...UNIVERSAL_REQUIRED_FILES, CHROME_ARGV_FILE]
    : [...UNIVERSAL_REQUIRED_FILES];
}

// review round 3（#480 CHANGES_REQUIRED R1／R2）：只驗「檔案存在＋harness
// 摘要文字」不夠——reviewer 對真實 gate1 正例做兩個變異（拿掉
// `gate-evidence.json`／`gate-flow.json`）evaluator 仍 rc=0。這兩個檔案是
// gates 專屬的「領域證據」（不是 UNIVERSAL_REQUIRED_FILES 那種只看存在與否
// 的共用檔），必須連內容 schema 與 identity 一起核對：
//   - `gate-evidence.json`：`frontend/e2e/support/gates/gateEvidence.ts:132`
//     寫出，schema `{flow, runId, bodyStatus, finalStatus, steps}`
//     （`gateEvidence.ts:141-143`）；`finalStatus==='passed'` 時三個
//     journal 必須 `status:'ok'`（`gateEvidence.ts:149-153`，這裡不重新驗
//     journal 內容，只核對這個檔案本身宣稱的 `finalStatus`／`flow`／
//     `runId`）。
//   - `gate-flow.json`：`frontend/e2e/global-setup.gates.ts:60-63` 寫出，
//     schema `{runId, flow, specFile}`；spec 端用
//     `frontend/e2e/support/gates/gateFlowDescriptor.ts:26-51` 的
//     `verifyGateFlowDescriptor()` 核對同一份契約（runId／flow／specFile
//     三個欄位都要精確相符，見該檔案）。
// controls 專屬：`control-a-evidence.json`／`control-b-evidence.json`（
// `frontend/e2e/controls/save-detection.spec.ts` 的 `writeEvidence()` 呼叫
// 寫出，見該檔 `control A`／`control B` 兩個 test 內的 `writeEvidence(...)`）
// ——真實 controls run 目錄確認存在，見
// `__fixtures__/real/controls-pass-.../control-a-evidence.json`。
//
// review round 5（#483 S1，撤回 round 3－4 的錯誤結論）：「scenario 沒有另
// 外的領域證據檔」這個說法是錯的——reviewer 已證實。真正的 producer（見
// progress.md Step 1 逐檔核對，這裡摘要 file:line）：
//
// 8 個 scenario entry 分兩類，各自的 producer：
//
// 【Codex 五案】commandExecution-allow／commandExecution-deny／
// fileChange-allow／fileChange-deny／commandExecution-recovery——
// globalSetupScenario 非 Claude 分支（`global-setup.scenario.ts:179-185`）
// 全部會走到：
//   - `scenario-config.json`：`global-setup.scenario.ts:182`
//     （`fs.writeFileSync(scenarioConfigPath, ...)`，`scenarioConfigPath`＝
//     `artifactsDir/scenario-config.json`，`:139`）。
//   - `scenario-wire.log`：`global-setup.scenario.ts:140`
//     （`scenarioLogPath`＝`artifactsDir/scenario-wire.log`），由
//     `createScenarioCodexCli()`／`fakeAppServer.ts` 寫入 fake wire frame，
//     `codexApproval.spec.ts:415`／`codexSessionRecovery.spec.ts:396` 讀取
//     （`parseRunLog(env.scenarioLogPath)`）。
//   - `scenario-wire.log.manifest.json`：
//     `support/scenario/scenarioCli.ts:79`
//     （`scenarioManifestPath: \`${scenarioLogPath}.manifest.json\`` ），
//     `codexApproval.spec.ts:364-376`／`codexSessionRecovery.spec.ts:384-394`
//     讀取（`parseManifest(env.scenarioManifestPath)`，核對 `exitCode`）。
//   - `app-audit.jsonl`：App 正式 audit 落點
//     （`<workspaceDir>/.workbench/audit.jsonl`）的原文複本，
//     `codexApproval.spec.ts:213`／`codexSessionRecovery.spec.ts:230`
//     （`fs.copyFileSync(auditPath, path.join(env.artifactsDir,
//     'app-audit.jsonl'))`）。
//   - `app-workspace-sessions.json`：同上模式，
//     `codexApproval.spec.ts:214`／`codexSessionRecovery.spec.ts:231`。
//   - `app-wire-log.jsonl`／`app-wire-log.meta.json`：App 端原始 wire 錄流
//     （`internal/wirelog/wirelog.go` always-on generation）的保存副本，
//     `persistWireEvidence()` 的預設檔名（`wireEvidence.ts:296`），呼叫點
//     `codexApproval.spec.ts:291-297`／`codexSessionRecovery.spec.ts:308-314`
//     （destDir＝`env.artifactsDir`）。
//
// 【Claude 三案】claude-approval-allow／claude-approval-deny／
// claude-approval-recovery——globalSetupScenario 的 `isClaude` 分支
// （`global-setup.scenario.ts:159-306`）：
//   - `claude-expectation.json`：`global-setup.scenario.ts:386`
//     （`setupClaudeRunEnv()` 內 `fs.writeFileSync(claudeExpectationPath,
//     ...)`）。
//   - `app-binary-identity.json`：`global-setup.scenario.ts:342-343`
//     （核定 MCP binary identity，不從待驗 config 反推）。
// 兩個 approval 案（claude-approval-allow／claude-approval-deny，
// `claudeApproval.spec.ts`）另外各自產生：
//   - `cliinfo.json`：`claudeApproval.spec.ts:94`。
//   - `claude-broker-audit.raw.jsonl`：`claudeApproval.spec.ts:40-41`
//     （`test.afterEach`，audit.jsonl 原文複本，teardown 前保存）。
//   - `claude-broker-audit.json`：`claudeApproval.spec.ts:174-175`。
//   - `claude-ui-judgement.json`：`claudeApproval.spec.ts:181-182`
//     （`judged.violations` 必須是空陣列，見同檔 `:183`）。
//   （`claude-ui-action.json` 只有 deny 案會寫，`claudeApproval.spec.ts:137-145`
//   ——見下方 `optionalDomainFilesForEntry()`，不列進「必要」清單，因為
//   allow 案不會有這個檔案、硬性要求會誤傷 allow 案。）
// claude-approval-recovery（`claudeSessionRecovery.spec.ts`）另外產生：
//   - `cliinfo.json`：`claudeSessionRecovery.spec.ts:118`。
//   - `claude-recovery/exit-observations.jsonl`：
//     `claudeSessionRecovery.spec.ts:101-102`（`fs.appendFileSync`）。
//   - `claude-recovery/dom-observations.json`：
//     `claudeSessionRecovery.spec.ts:128-129`。
//   - `claude-recovery/judgement.json`：
//     `claudeSessionRecovery.spec.ts:319-320`（巢狀 `judged.violations` 必
//     須是空陣列，見同檔 `:326`）。
//   - `claude-recovery/round1/{sessions.json,workspace-sessions.json,
//     audit.jsonl}`：`claudeSessionRecovery.spec.ts:200-204`
//     （`preserveStateFiles()`，第一輪結束、第二輪 Bind 之前）。
//   - `claude-recovery/round2/{sessions.json,workspace-sessions.json,
//     audit.jsonl}`：`claudeSessionRecovery.spec.ts:284-288`（第二輪結束
//     後）。
//
// 沒有真實遠端 run 樣本核對過這些 scenario 檔案的實際內容（`__fixtures__/real/`
// 目前只有 gate1/gate2/stale/controls 四種真實樣本，scenario 從未真的在
// browser／App 下跑過）——以上 schema 全部是依 producer 原始碼推導，
// **首次遠端執行時需要核對**，不是已驗證的真實樣本，也絕不偽造一份看起來
// 像真實樣本的 fixture（見 selftest 的 fixture 一律標示 synthetic）。
const CODEX_SCENARIO_DOMAIN_FILES = [
  'scenario-config.json',
  'scenario-wire.log',
  'scenario-wire.log.manifest.json',
  'app-audit.jsonl',
  'app-workspace-sessions.json',
  'app-wire-log.jsonl',
  'app-wire-log.meta.json',
];
const CLAUDE_APPROVAL_DOMAIN_FILES = [
  'claude-expectation.json',
  'app-binary-identity.json',
  'cliinfo.json',
  'claude-broker-audit.raw.jsonl',
  'claude-broker-audit.json',
  'claude-ui-judgement.json',
];
const CLAUDE_RECOVERY_DOMAIN_FILES = [
  'claude-expectation.json',
  'app-binary-identity.json',
  'cliinfo.json',
  'claude-recovery/exit-observations.jsonl',
  'claude-recovery/dom-observations.json',
  'claude-recovery/judgement.json',
  'claude-recovery/round1/sessions.json',
  'claude-recovery/round1/workspace-sessions.json',
  'claude-recovery/round1/audit.jsonl',
  'claude-recovery/round2/sessions.json',
  'claude-recovery/round2/workspace-sessions.json',
  'claude-recovery/round2/audit.jsonl',
];

// review round 6（#489 F2）：匯出——evaluate-e2e-evidence.mjs 的跨 run
// identity 交叉核對（scenario-config.json 的 threadId/turnId/itemId 只有
// codex 五案才有）需要同一份集合，不另外各自維護一份容易漂移的清單。
export const CODEX_SCENARIO_IDS = new Set([
  'commandExecution-allow', 'commandExecution-deny', 'fileChange-allow', 'fileChange-deny',
  'commandExecution-recovery',
]);
const CLAUDE_APPROVAL_IDS = new Set(['claude-approval-allow', 'claude-approval-deny']);
const CLAUDE_RECOVERY_IDS = new Set(['claude-approval-recovery']);

export function requiredDomainFilesForEntry(entry) {
  if (entry.batch === 'gates') return ['gate-evidence.json', 'gate-flow.json'];
  if (entry.id === 'controls') return ['control-a-evidence.json', 'control-b-evidence.json'];
  if (CODEX_SCENARIO_IDS.has(entry.id)) return [...CODEX_SCENARIO_DOMAIN_FILES];
  if (CLAUDE_APPROVAL_IDS.has(entry.id)) return [...CLAUDE_APPROVAL_DOMAIN_FILES];
  if (CLAUDE_RECOVERY_IDS.has(entry.id)) return [...CLAUDE_RECOVERY_DOMAIN_FILES];
  return [];
}

// claude-ui-action.json 只有 deny 案會寫（`claudeApproval.spec.ts:137-145`，
// 只在 `if (decision === 'deny')` 分支內）——不放進
// `requiredDomainFilesForEntry()`（那會誤傷 allow 案），但仍要核對：deny 案
// 這個檔案必須存在，且 `clickResolved===true`（點擊已真的完成）。
export function optionalDomainFilesForEntry(entry) {
  if (entry.id === 'claude-approval-deny') return ['claude-ui-action.json'];
  return [];
}

// review round 5（S1）：Codex scenario 五案的 scenario-config.json identity——
// `buildConfig()`（`scenarios.ts:41-56`）把 threadId／turnId／itemId／
// approvalRequestId 都做成 `b3a2b2-<kind>-${runId}` 樣式，`runId` 就是
// `process.env.E2E_RUN_ID`（等於 runDirName，run-env.json 的 runId 已核對過
// 兩者相等）。這是 identity 交叉核對的來源，不靠猜測。
export function expectedScenarioConfigIdentity(entry, runDirName) {
  return {
    scenario: entry.env.E2E_SCENARIO,
    threadId: `b3a2b2-thread-${runDirName}`,
    turnId: `b3a2b2-turn-${runDirName}`,
    itemId: `b3a2b2-item-${runDirName}`,
    approvalRequestId: `b3a2b2-approval-${runDirName}`,
  };
}

// commandExecution-recovery 專屬：secondTurn 的獨立期望（`scenarios.ts` 的
// `commandExecution-recovery.build()`，turnId/itemId/approvalRequestId 加
// `2` 後綴）。
export function expectedScenarioSecondTurnIdentity(runDirName) {
  return {
    turnId: `b3a2b2-turn2-${runDirName}`,
    itemId: `b3a2b2-item2-${runDirName}`,
    approvalRequestId: `b3a2b2-approval2-${runDirName}`,
  };
}

// approvalMethod 字串值（`support/scenario/protocol.ts:30-31` 的 `Method`
// 常數，commandExecution／fileChange 兩案分流；recovery 只有
// commandExecution 一種）。
export function expectedScenarioApprovalMethodForEntry(entry) {
  if (entry.id.startsWith('fileChange-')) return 'item/fileChange/requestApproval';
  if (entry.id.startsWith('commandExecution-')) return 'item/commandExecution/requestApproval';
  return null;
}

// Codex scenario 的 decision（accept／decline，`scenarios.ts` SCENARIOS 登
// 記表逐案欄位，`-allow`／`-recovery` 一律 accept，`-deny` 一律 decline）。
export function expectedCodexScenarioDecisionForEntry(entry) {
  if (!CODEX_SCENARIO_IDS.has(entry.id)) return null;
  return entry.id.endsWith('-deny') ? 'decline' : 'accept';
}

// Claude scenario 的協定層 decision（allow／deny，
// `claudeApprovalProtocol.ts:54-58` 的 `protocolDecisionFor()`：
// accept→allow、decline→deny）。claude-approval-recovery 固定兩輪 allow
// （`global-setup.scenario.ts:372-376` 明確拒絕非 allow 的 recovery 案）。
export function expectedClaudeProtocolDecisionForEntry(entry) {
  if (entry.id === 'claude-approval-deny') return 'deny';
  if (entry.id === 'claude-approval-allow' || entry.id === 'claude-approval-recovery') return 'allow';
  return null;
}

// review round 3（R2）：run-env.json 的 identity 欄位——`frontend/e2e/support/env.ts`
// 的 `RunEnv.scenario` 欄位在 scenario 入口下就是這次呼叫的 scenario 名稱
// 本身（`frontend/e2e/global-setup.scenario.ts:387/402`：
// `writeRunEnv({..., scenario: scenarioName / scenarioConfig.scenario, ...})`，
// 而 `scenarioConfig.scenario`／`scenarioName` 在 `scenarios.ts` 的
// `SCENARIOS` 登記表裡就是 entry id 本身，如 `'commandExecution-allow'`）。
// 這是四個 Codex approval entry（同一支 `codexApproval.spec.ts`）與兩個
// Claude approval entry（同一支 `claudeApproval.spec.ts`）唯一可信、非
// 猜測的 identity 區分來源——JSON reporter 的 spec.file／test title 兩者都
// 不會變化（已核對，見 progress.md），必須靠這個欄位交叉核對，否則四案／
// 兩案彼此可以互相冒充。
export function expectedRunEnvScenarioForEntry(entry) {
  return entry.batch === 'scenarios' ? entry.env.E2E_SCENARIO : null;
}

// review round 3（R2）：gate-flow.json 的 identity 欄位——只有 gates entry
// 需要核對（gate1／gate2／stale 共用同一個 testDir，JSON reporter 的
// spec.file 雖然已經能區分檔名，但 gate-flow.json 是 production 自己另一
//條獨立的身分核對路徑，見上方 requiredDomainFilesForEntry 的引用），交叉
// 核對可以在「JSON reporter 被篡改」之外提供第二個獨立來源。
export function expectedGateFlowForEntry(entry) {
  return entry.batch === 'gates' ? entry.env.E2E_GATE : null;
}

// review round 3（R5）：spawn child 之前的 env 邊界——只允許這些 key 由
// entry.env 決定；如果呼叫端（ambient process.env）已經帶有這些 key 中的
// 任何一個，一律視為「污染的 env」，拒絕 spawn（不靜默覆寫、也不靜默沿
// 用）。這份清單涵蓋 decision.md R5 點名的五個變數
// （E2E_GATE／E2E_SCENARIO／E2E_CONTROLS_REVERSE_CHECK／
// E2E_OFFLINE_SANDBOX／E2E_BROWSER）。
export const CONTROLLED_ENV_KEYS = [
  'E2E_GATE',
  'E2E_SCENARIO',
  'E2E_CONTROLS_REVERSE_CHECK',
  'E2E_OFFLINE_SANDBOX',
  'E2E_BROWSER',
];

// review round 4（#480 R2 remaining gap：可信 CI invocation envelope）：
// job 開頭由 generate-ci-envelope.mjs 從 GitHub context／實際探測指令產生的
// 「基底」欄位（不含 entry 專屬的 entryId／configPath／selectedEnv／runId，
// 那些是 run-batch.mjs 逐 entry 額外蓋章的部分，見該檔）。單一權威來源，
// run-batch.mjs 用它驗證 job-level envelope 的完整性，
// evaluate-e2e-evidence.mjs 用它驗證打包進每個 entry 證據裡的最終 envelope
// schema，避免兩處各自維護一份容易漂移的欄位清單。
// review round 5（#483 直接裁定）：「原完整契約一直要求toolchain版本，不能
// 因某次複核條列沒重複Wails名稱就刪掉」——round 4 只探測 Node／Go／Chrome，
// 漏了 Wails／Playwright／checkout tree 三項。這裡補上：
//   - wailsVersionActual：workflow 既有 `wails version` 探測（job 已經
//     `go install` 過 wails cli），這裡重新現場探測一次，落地成檔案而非只
//     echo 到 job log。
//   - playwrightVersionActual：`frontend/` 底下實際安裝的 Playwright 版
//     本（`npx --prefix frontend playwright --version`），跟
//     `frontend/package.json` 宣告的版本可能不同（lockfile 解析結果）。
//   - checkoutTreeSha：`git rev-parse HEAD^{tree}`，checkout 之後整棵樹的
//     內容雜湊——比單一 commit SHA 更直接地核對「這次 checkout 出來的檔案
//     內容」沒有被中途竄改（tree 物件雜湊涵蓋全部檔案內容與路徑）。
// review round 6（#489 F3）：envelope 先前只驗「非空字串」，從未核對實際
// tree／核定工具版本——reviewer 已證實把 checkoutTreeSha 換成另一個合法
// 40-hex 值、Wails/Go/Playwright 換成任意版本仍 violations=[]。這裡明列
// 「正常 CI」應該量到的核定版本（job 開始量一次，供 envelope-checks.mjs 的
// checkEnvelopeToolVersions() 用子字串核對；Chrome 依決定文件明文「只記錄
// 有效實測版本，不額外釘版」，不在這份清單）。這是唯一權威來源，
// run-batch.mjs（job-level preflight）與 evaluate-e2e-evidence.mjs（entry
// 專屬 envelope）預設都用這份常數，不另外各自維護。
export const REQUIRED_TOOL_VERSIONS = {
  node: 'v26.10.0',
  go: 'go1.26.5',
  wails: 'v2.13.0',
  playwright: '1.63.0',
};

export const ENVELOPE_BASE_FIELDS = [
  'testMergeSha',
  'prHeadSha',
  'prBaseSha',
  'repository',
  'workflowRunId',
  'workflowRunAttempt',
  'runnerOs',
  'imageOs',
  'imageVersion',
  'checkoutHeadSha',
  'checkoutTreeSha',
  'nodeVersionActual',
  'goVersionActual',
  'chromeVersionActual',
  'wailsVersionActual',
  'playwrightVersionActual',
  'generatedAtIso',
];

// 這五個欄位必須是合法的 40-hex git SHA（不只是「非空字串」）。
export const ENVELOPE_SHA_FIELDS = ['testMergeSha', 'prHeadSha', 'prBaseSha', 'checkoutHeadSha', 'checkoutTreeSha'];

// run-batch.mjs 逐 entry 蓋章的欄位（entryId／configPath／runId 是字串；
// selectedEnv 是物件，另外驗證，不放進這個「必為非空字串」清單）。
export const ENVELOPE_STAMP_STRING_FIELDS = ['entryId', 'configPath', 'runId'];

if (ENTRIES.length !== 13) {
  // 結構性自我檢查：這份表本身宣稱涵蓋 13 個 invocation，數量錯就是這個檔案
  // 自己的 bug，寧可在 import 當下就爆炸，不要讓下游默默少算。
  throw new Error(`ci-e2e-entries.mjs: ENTRIES.length=${ENTRIES.length}，預期 13`);
}
