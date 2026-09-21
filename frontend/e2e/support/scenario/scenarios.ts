// B3a-2b-2 Task C／Task D：scenario 名稱 → ScenarioConfig 的唯一登記表。
//
// 裁定（票面既有，Task C）：scenario entry 要求明確有效的 scenario、一個
// run-id 只跑指定案——這裡沒有「預設 scenario」這回事，未指定或指定到不存在
// 的名稱一律 throw，不回退。
//
// Task D 擴充：登記滿 2 methods × 2 decisions 的四案 matrix
// （commandExecution-allow／commandExecution-deny／fileChange-allow／
// fileChange-deny）。`decision` 是新增欄位——不在 protocol.ts 的
// ScenarioConfig 裡（那支檔案凍結不改），因為 fake app-server 本身不看
// config 決定 decision（它照單全收 client 實際送來的 accept／decline，見
// fakeAppServer.ts awaitApprovalResponse 分支）；`decision` 純粹是「本案例
// 預期瀏覽器該點哪個按鈕、判定端該核對哪個值」的獨立期望，只存在於
// ScenarioDef 這一層。四案共用同一個 buildConfig 樣板（threadId／turnId／
// itemId／approvalRequestId／afterApproval 內容一致的產生規則，只有
// scenario 名稱與 approvalMethod 不同）——避免四份幾乎相同的 build() 各自
// 手刻。
import { Method } from './protocol.js';
import type { ApprovalMethod, ScenarioConfig } from './protocol.js';

export interface ScenarioDef {
  name: string;
  // provider：B3a-2b-2 F2 新增——**provider 必須明確區分**，不得把兩種協定塞進
  // 同一個寬鬆判定（reviewer #365）。既有五案一律 'codex'，語意與契約完全不變；
  // 'claude' 是本次新增的單一 fresh-start approval allow 案。
  provider: 'codex' | 'claude';
  // decision：本案例「允許」或「拒絕」——瀏覽器該點 approval-allow 還是
  // approval-deny，以及 wire／audit 判定端該核對哪個 decision 值。單輪案例
  // 只有一個 decision；recovery 案兩輪都 accept，同一個欄位對兩輪皆適用
  // （Task E2 未擴充成每輪各自 decision——票面要求「一個 commandExecution／
  // 兩輪都 accept」，不需要兩個獨立欄位）。
  decision: 'accept' | 'decline';
  // kind：B3a-2b-2 Task E2 新增——playwright.scenario.config.ts 的
  // specRouting.ts 靠這個欄位決定一個 E2E_SCENARIO 該挑哪一支 spec 檔
  // （'approval' → codexApproval.spec.ts；'recovery' →
  // codexSessionRecovery.spec.ts），單一登記表同時是 resolveScenario 與
  // spec 路由的共同事實來源，不另外維護第二份名稱清單。
  kind: 'approval' | 'recovery';
  build(runId: string): ScenarioConfig;
}

function buildConfig(name: string, approvalMethod: ApprovalMethod, runId: string): ScenarioConfig {
  return {
    scenario: name,
    threadId: `b3a2b2-thread-${runId}`,
    turnId: `b3a2b2-turn-${runId}`,
    itemId: `b3a2b2-item-${runId}`,
    threadMode: 'start', // 真 App 走第一輪 StartSession（新 session），對應 thread/start。
    approvalMethod,
    approvalRequestId: `b3a2b2-approval-${runId}`,
    afterApproval: [
      { type: 'itemStarted', text: `b3a2b2-scenario-content-${runId}` },
      { type: 'itemCompleted', text: `b3a2b2-scenario-content-${runId}` },
    ],
    turnStatus: 'completed',
  };
}

const SCENARIOS: Record<string, ScenarioDef> = {
  'commandExecution-allow': {
    name: 'commandExecution-allow',
    provider: 'codex',
    decision: 'accept',
    kind: 'approval',
    build: (runId: string) => buildConfig('commandExecution-allow', Method.CmdExecRequestApproval, runId),
  },
  'commandExecution-deny': {
    name: 'commandExecution-deny',
    provider: 'codex',
    decision: 'decline',
    kind: 'approval',
    build: (runId: string) => buildConfig('commandExecution-deny', Method.CmdExecRequestApproval, runId),
  },
  'fileChange-allow': {
    name: 'fileChange-allow',
    provider: 'codex',
    decision: 'accept',
    kind: 'approval',
    build: (runId: string) => buildConfig('fileChange-allow', Method.FileChangeRequestApproval, runId),
  },
  'fileChange-deny': {
    name: 'fileChange-deny',
    provider: 'codex',
    decision: 'decline',
    kind: 'approval',
    build: (runId: string) => buildConfig('fileChange-deny', Method.FileChangeRequestApproval, runId),
  },
  // B3a-2b-2 Task E2：一個 commandExecution／兩輪都 accept 的 browser
  // recovery 案——第一輪 threadMode 固定 'start'（secondTurn 存在時的協定
  // 契約），第二輪由真 App 對同一個 WSID 再次送出觸發 thread/resume（見
  // app.go startSession：resume 空時回退 registryResume(w)，而真實 UI 流程
  // 在第一輪就已經透過 session_id 事件把 store 的 m.resume 填成本輪
  // threadId，因此「同一個 pane 再次送出」自然帶著 resume，不需要測試碼
  // 另外注入）。turnId／itemId／approvalRequestId／afterApproval 內容全部
  // 與第一輪不同，供兩輪 identity 交叉核對。
  'commandExecution-recovery': {
    name: 'commandExecution-recovery',
    provider: 'codex',
    decision: 'accept',
    kind: 'recovery',
    build: (runId: string) => ({
      ...buildConfig('commandExecution-recovery', Method.CmdExecRequestApproval, runId),
      secondTurn: {
        turnId: `b3a2b2-turn2-${runId}`,
        itemId: `b3a2b2-item2-${runId}`,
        approvalRequestId: `b3a2b2-approval2-${runId}`,
        afterApproval: [
          { type: 'itemStarted', text: `b3a2b2-scenario-content2-${runId}` },
          { type: 'itemCompleted', text: `b3a2b2-scenario-content2-${runId}` },
        ],
        turnStatus: 'completed',
      },
    }),
  },
  // B3a-2b-2 F2：單一 Claude fresh-start approval allow。
  //
  // **這一案不使用 Codex 的 wire 協定欄位**（threadId／turnId／approvalMethod 等是
  // fake codex app-server 的協定，與 Claude 的 MCP approval 無關）。`build()` 仍然
  // 存在只是為了讓登記表維持單一型別；**Claude 案的 build() 一定會 throw**，
  // 任何誤用 Codex 路徑處理 Claude 案的地方都會當場炸開，不會悄悄拿到一份
  // 語意不對的 config。Claude 案真正的期望值由 F1a 的
  // `buildClaudeApprovalExpectation(runId)` 提供（見 claudeApprovalProtocol.ts）。
  'claude-approval-allow': {
    name: 'claude-approval-allow',
    provider: 'claude',
    decision: 'accept',
    kind: 'approval',
    build: () => {
      throw new Error(
        'scenarios: claude-approval-allow 沒有 Codex wire 協定 config——'
        + 'Claude 案的期望值來自 buildClaudeApprovalExpectation(runId)，'
        + '呼叫到這裡代表誤用了 Codex 路徑處理 Claude 案',
      );
    },
  },
};

export function resolveScenario(name: string | undefined): ScenarioDef {
  if (!name) {
    throw new Error(
      'resolveScenario: E2E_SCENARIO 未設定——scenario 入口要求明確有效的 scenario，不回退 default',
    );
  }
  // F2（B3a-2b-2 Task D 限縮補正）：`SCENARIOS[name]` 是一般 object，`name`
  // 若為 `toString`／`constructor`／`__proto__`／`valueOf`／
  // `hasOwnProperty` 等任何 `Object.prototype` 成員都會從原型鏈拿到值、
  // 不會 throw——不符合「未知一律在 resolver 拒絕」的契約。`Object.hasOwn`
  // 只認真正登記在 SCENARIOS 自身的 key，繞開整條原型鏈。
  if (!Object.hasOwn(SCENARIOS, name)) {
    throw new Error(`resolveScenario: 未知 scenario "${name}"，可用：${Object.keys(SCENARIOS).join('、')}`);
  }
  return SCENARIOS[name];
}
