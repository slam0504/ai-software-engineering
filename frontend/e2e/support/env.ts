// 共用 env 存取層。
//
// 設計背景：run-e2e.mjs（外層 wrapper）在 spawn `playwright test` 之前就決定
// run-id／證據目錄，經 env 傳給子行程；globalSetup 在同一個子行程內繼續
// 補上 fixture／fake tools 等「執行到那一步才知道」的路徑，一樣寫回
// `process.env`——經實測驗證，globalSetup 與 globalTeardown、worker 都跑在
// 同一個 `playwright test` 主行程內，env 會延續下去。
//
// 保險起見同時把同一份值落地成 `run-env.json`，讀取時先看 process.env、
// 缺了才退回讀檔。
import fs from 'node:fs';
import path from 'node:path';

export interface RunEnv {
  runId: string;
  artifactsDir: string;
  workspaceDir: string;
  toolsDir: string;
  glossaryPath: string;
  claudeVersion: string;
  codexVersion: string;
  baseUrl: string;
  vitePort?: number;
  // B3a-2b-2 Task C：scenario identity（僅 playwright.scenario.config.ts 這條
  // 入口會填）。這裡刻意跟 base 欄位分開放，讓 default／controls 兩條入口的
  // run-env.json 維持原樣不受影響；scenario 入口下這幾欄缺一即視為壞掉
  // ——見下方 readScenarioRunEnv，不得回退成「當作沒有 scenario」。
  scenario?: string;
  scenarioThreadId?: string;
  scenarioTurnId?: string;
  scenarioItemId?: string;
  scenarioApprovalMethod?: string;
  scenarioApprovalRequestId?: string;
  scenarioDecision?: string;
  scenarioConfigPath?: string;
  scenarioLogPath?: string;
  scenarioManifestPath?: string;
  // B3a-2b-2 F2：Claude 案專屬欄位。**與 Codex 的 scenario* 欄位互斥**——
  // Claude 案沒有 thread/turn/item/approvalMethod 這些 Codex wire 協定概念，
  // 硬塞會讓 readScenarioRunEnv 的「缺一即壞掉」契約失去意義。
  claudeExpectationPath?: string;
  claudeEvidenceDir?: string;
  /** 核定 MCP binary 的 canonical path——來自受控啟動產物，不從待驗 config 反推。 */
  claudeApprovedCommandPath?: string;
  claudeApprovedCommandSha256?: string;
  /** App stateDir（<workspace>/.workbench）：動態 socket 必須落在此目錄之下。 */
  claudeStateDir?: string;
}

function runEnvFile(artifactsDir: string): string {
  return path.join(artifactsDir, 'run-env.json');
}

export function writeRunEnv(env: RunEnv): void {
  fs.mkdirSync(env.artifactsDir, { recursive: true });
  fs.writeFileSync(runEnvFile(env.artifactsDir), JSON.stringify(env, null, 2));
  for (const [k, v] of Object.entries(envToProcessEnv(env))) {
    if (v !== undefined) process.env[k] = v;
  }
}

function envToProcessEnv(env: RunEnv): Record<string, string | undefined> {
  return {
    E2E_RUN_ID: env.runId,
    E2E_ARTIFACTS_DIR: env.artifactsDir,
    E2E_WORKSPACE_DIR: env.workspaceDir,
    E2E_TOOLS_DIR: env.toolsDir,
    E2E_GLOSSARY_PATH: env.glossaryPath,
    E2E_CLAUDE_VERSION: env.claudeVersion,
    E2E_CODEX_VERSION: env.codexVersion,
    E2E_BASE_URL: env.baseUrl,
    E2E_VITE_PORT: env.vitePort ? String(env.vitePort) : undefined,
    E2E_SCENARIO_NAME: env.scenario,
    E2E_SCENARIO_THREAD_ID: env.scenarioThreadId,
    E2E_SCENARIO_TURN_ID: env.scenarioTurnId,
    E2E_SCENARIO_ITEM_ID: env.scenarioItemId,
    E2E_SCENARIO_APPROVAL_METHOD: env.scenarioApprovalMethod,
    E2E_SCENARIO_APPROVAL_REQUEST_ID: env.scenarioApprovalRequestId,
    E2E_SCENARIO_DECISION: env.scenarioDecision,
    E2E_SCENARIO_CONFIG_PATH: env.scenarioConfigPath,
    E2E_SCENARIO_LOG_PATH: env.scenarioLogPath,
    E2E_SCENARIO_MANIFEST_PATH: env.scenarioManifestPath,
    // B3a-2b-2 F2：Claude 案欄位——**必須與 readRunEnv 的 process.env 分支成對**，
    // 否則 spec/teardown 在 env 路徑上會拿不到這些值而誤判。
    E2E_CLAUDE_EXPECTATION_PATH: env.claudeExpectationPath,
    E2E_CLAUDE_EVIDENCE_DIR: env.claudeEvidenceDir,
    E2E_CLAUDE_APPROVED_COMMAND_PATH: env.claudeApprovedCommandPath,
    E2E_CLAUDE_APPROVED_COMMAND_SHA256: env.claudeApprovedCommandSha256,
    E2E_CLAUDE_STATE_DIR: env.claudeStateDir,
  };
}

// readRunEnv：spec／teardown 用。先看 process.env，缺一項就整份退回讀檔。
export function readRunEnv(): RunEnv {
  const p = process.env;
  if (p.E2E_RUN_ID && p.E2E_ARTIFACTS_DIR && p.E2E_WORKSPACE_DIR && p.E2E_TOOLS_DIR && p.E2E_GLOSSARY_PATH
    && p.E2E_CLAUDE_VERSION && p.E2E_CODEX_VERSION && p.E2E_BASE_URL) {
    return {
      runId: p.E2E_RUN_ID,
      artifactsDir: p.E2E_ARTIFACTS_DIR,
      workspaceDir: p.E2E_WORKSPACE_DIR,
      toolsDir: p.E2E_TOOLS_DIR,
      glossaryPath: p.E2E_GLOSSARY_PATH,
      claudeVersion: p.E2E_CLAUDE_VERSION,
      codexVersion: p.E2E_CODEX_VERSION,
      baseUrl: p.E2E_BASE_URL,
      vitePort: p.E2E_VITE_PORT ? Number(p.E2E_VITE_PORT) : undefined,
      scenario: p.E2E_SCENARIO_NAME,
      scenarioThreadId: p.E2E_SCENARIO_THREAD_ID,
      scenarioTurnId: p.E2E_SCENARIO_TURN_ID,
      scenarioItemId: p.E2E_SCENARIO_ITEM_ID,
      scenarioApprovalMethod: p.E2E_SCENARIO_APPROVAL_METHOD,
      scenarioApprovalRequestId: p.E2E_SCENARIO_APPROVAL_REQUEST_ID,
      scenarioDecision: p.E2E_SCENARIO_DECISION,
      scenarioConfigPath: p.E2E_SCENARIO_CONFIG_PATH,
      scenarioLogPath: p.E2E_SCENARIO_LOG_PATH,
      scenarioManifestPath: p.E2E_SCENARIO_MANIFEST_PATH,
      claudeExpectationPath: p.E2E_CLAUDE_EXPECTATION_PATH,
      claudeEvidenceDir: p.E2E_CLAUDE_EVIDENCE_DIR,
      claudeApprovedCommandPath: p.E2E_CLAUDE_APPROVED_COMMAND_PATH,
      claudeApprovedCommandSha256: p.E2E_CLAUDE_APPROVED_COMMAND_SHA256,
      claudeStateDir: p.E2E_CLAUDE_STATE_DIR,
    };
  }
  const artifactsDir = p.E2E_ARTIFACTS_DIR;
  if (!artifactsDir) {
    throw new Error('readRunEnv: E2E_ARTIFACTS_DIR 未設定，無法退回讀取 run-env.json（globalSetup 是否有跑過？）');
  }
  return JSON.parse(fs.readFileSync(runEnvFile(artifactsDir), 'utf8')) as RunEnv;
}

// readScenarioRunEnv：scenario spec 專用——scenario identity 的九個欄位缺一
// 就整個 fail loud（不得回退成「當作沒有 scenario」或用預設值頂替，見票面
// 既有裁定）。同時涵蓋 process.env 與 run-env.json 兩條路徑：readRunEnv()
// 已經把兩邊都讀過一次，這裡只再做「scenario 欄位必須完整」這一層守門。
export interface ScenarioRunEnv extends RunEnv {
  scenario: string;
  scenarioThreadId: string;
  scenarioTurnId: string;
  scenarioItemId: string;
  scenarioApprovalMethod: string;
  scenarioApprovalRequestId: string;
  scenarioDecision: string;
  scenarioConfigPath: string;
  scenarioLogPath: string;
  scenarioManifestPath: string;
}

export function readScenarioRunEnv(): ScenarioRunEnv {
  const env = readRunEnv();
  const required = [
    'scenario', 'scenarioThreadId', 'scenarioTurnId', 'scenarioItemId',
    'scenarioApprovalMethod', 'scenarioApprovalRequestId', 'scenarioDecision',
    'scenarioConfigPath', 'scenarioLogPath', 'scenarioManifestPath',
  ] as const;
  // B3a-2b-2 Task C 第三輪限縮補正（缺陷 1 的姊妹修正）：不能用
  // `!env[k]`（truthy）代替 runtime 型別驗證——process.env 路徑的值必為
  // string，但 run-env.json 純檔案 fallback 路徑是未經驗證的 `JSON.parse`
  // 結果，欄位可能是數字（例如 `scenarioTurnId: 42`）之類非字串值；truthy
  // 檢查對非零數字會判定「有值」而放行，型別其實不對。這裡逐欄要求
  // `typeof === 'string' && length > 0`，缺失與型別錯誤都算 missing。
  const missing = required.filter(k => typeof env[k] !== 'string' || (env[k] as string).length === 0);
  if (missing.length > 0) {
    throw new Error(
      `readScenarioRunEnv: scenario identity 欄位缺失或型別錯誤（不得回退 default）：${missing.join('、')}`,
    );
  }
  return env as ScenarioRunEnv;
}

// 使用者可見的旗標／注入點（§2.1、負控制注入點，命名見 README「Browser E2E」段）。
export const userFlags = {
  browser: () => process.env.E2E_BROWSER ?? 'chrome',
  keepArtifacts: () => process.env.E2E_KEEP_ARTIFACTS === '1',
  startupTimeoutMs: () => Number(process.env.E2E_STARTUP_TIMEOUT_MS ?? 300_000), // N7 注入點
  fakeCliBadVersionAfterPreflight: () => process.env.E2E_FAKE_CLI_BAD_VERSION_AFTER_PREFLIGHT === '1', // N5 注入點
  // N10 注入點：改成觀測「非同 pgid 的 vite 後代已出現、且 34115 尚未可達」才觸發
  // （見 global-setup.ts），不再用固定延遲用猜的，故不再需要 delay 旗標。
  forceFailBeforeReady: () => process.env.E2E_FORCE_FAIL_BEFORE_READY === '1',
  // N10 測試專用協調點：真的 wails dev 時序不可靠時，改用可控假啟動器驗證
  // 同一條 lifecycle path（見 processTree.ts 的 spawnWailsDev override）。
  n10FakeStarter: () => process.env.E2E_N10_FAKE_STARTER === '1',
  treatLoopbackAsExternal: () => process.env.E2E_TREAT_LOOPBACK_AS_EXTERNAL === '1', // N9 注入點
  injectMissingToolsDirEnv: () => process.env.E2E_INJECT_MISSING_TOOLS_DIR_ENV === '1', // N3 注入點
  injectFixtureReadOnly: () => process.env.E2E_INJECT_FIXTURE_READONLY === '1', // N4 注入點
  injectBadCliCall: () => process.env.E2E_INJECT_BAD_CLI_CALL === '1', // N1 注入點（smoke spec 內使用）
  injectDeleteInvocationsLog: () => process.env.E2E_INJECT_DELETE_INVOCATIONS_LOG === '1', // N2 注入點（smoke spec 內使用）
  injectN9bFetch: () => process.env.E2E_INJECT_N9B_FETCH === '1', // N9b 注入點：頁面對保留測試網域發出 fetch
  injectN9bWebSocket: () => process.env.E2E_INJECT_N9B_WS === '1', // N9b 注入點：頁面對保留測試網域開 WebSocket
  injectN14aWriteFailure: () => process.env.E2E_INJECT_N14A_WRITE_FAILURE === '1', // N14a 注入點（缺口 A）：browser-network-violations.log 寫入失敗
  injectN14bDeleteEvidence: () => process.env.E2E_INJECT_N14B_DELETE_EVIDENCE === '1', // N14b 注入點（缺口 A）：browser-network-violations.log 在判定前被刪除
  injectPsFailure: () => process.env.E2E_INJECT_PS_FAILURE === '1', // N12 注入點：讓 ps／lsof 指向永遠 exit 2 的替身
  injectPsExit1Stderr: () => process.env.E2E_INJECT_PS_EXIT1_STDERR === '1', // N12 擴充注入點（缺口 B）：讓 ps／lsof 指向 exit 1 但印 stderr 的替身，驗證「exit 1 不能無條件當合法空結果」
  injectPostSpawnPsFailure: () => process.env.E2E_INJECT_POST_SPAWN_PS_FAILURE === '1', // R2 注入點：spawn 成功後、第一次 post-spawn 的 ps 呼叫失敗（自動 disarm，之後恢復正常），驗證 spawn 之後例外仍有收尾
  // StartHidden（owner 授權的限定 production 修改，2026-09-16）：E2E 啟動
  // wails dev 時預設帶 WORKBENCH_E2E_START_HIDDEN=1，避免雙螢幕環境下原生
  // 視窗跳出來干擾作業。這個旗標**只用於驗證**：設成 1 時，harness 不帶
  // 這個環境變數給 app 子程序（連 process.env 繼承的同名變數也會被清掉），
  // 用來確認「沒設定旗標＝維持正常開窗」這條路徑；不是給一般 E2E 使用。
  e2eVerifyNoStartHidden: () => process.env.E2E_VERIFY_NO_START_HIDDEN === '1',
  injectStaleRunMissingState: () => process.env.E2E_INJECT_STALE_RUN_MISSING_STATE === '1', // N11c 注入點：前次 run-state.json 遺失／損毀
  injectIgnoreTerm: () => process.env.E2E_INJECT_IGNORE_TERM === '1', // N13 注入點：自家 fixture 程序忽略 SIGTERM
  injectDeleteNetworkLog: () => process.env.E2E_INJECT_DELETE_NETWORK_LOG === '1', // R3 定點反證：network-samples.log 在判定前被刪除，globalTeardown 不能當成 0 違規
  injectDeleteRunStateForTeardown: () => process.env.E2E_INJECT_DELETE_RUN_STATE_FOR_TEARDOWN === '1', // R3 定點反證：run-state.json 在判定前被刪除，observationFailures 讀取失敗不能當成空陣列
};

/** B3a-2b-2 F2：Claude 案的 run-env 讀取——缺一即視為壞掉，不回退。 */
export interface ClaudeScenarioRunEnv extends RunEnv {
  scenario: string;
  claudeExpectationPath: string;
  claudeEvidenceDir: string;
  claudeApprovedCommandPath: string;
  claudeApprovedCommandSha256: string;
  claudeStateDir: string;
}

export function readClaudeScenarioRunEnv(): ClaudeScenarioRunEnv {
  const env = readRunEnv();
  const required = [
    'scenario', 'claudeExpectationPath', 'claudeEvidenceDir',
    'claudeApprovedCommandPath', 'claudeApprovedCommandSha256', 'claudeStateDir',
  ] as const;
  const missing = required.filter(k => {
    const v = env[k];
    return typeof v !== 'string' || v.length === 0;
  });
  if (missing.length > 0) {
    throw new Error(
      `readClaudeScenarioRunEnv: run-env 缺少 Claude scenario 欄位：${missing.join('、')}`
      + '——Claude 案不得在欄位不全的情況下繼續',
    );
  }
  return env as ClaudeScenarioRunEnv;
}
