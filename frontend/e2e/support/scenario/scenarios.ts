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
  // decision：本案例「允許」或「拒絕」——瀏覽器該點 approval-allow 還是
  // approval-deny，以及 wire／audit 判定端該核對哪個 decision 值。
  decision: 'accept' | 'decline';
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
    decision: 'accept',
    build: (runId: string) => buildConfig('commandExecution-allow', Method.CmdExecRequestApproval, runId),
  },
  'commandExecution-deny': {
    name: 'commandExecution-deny',
    decision: 'decline',
    build: (runId: string) => buildConfig('commandExecution-deny', Method.CmdExecRequestApproval, runId),
  },
  'fileChange-allow': {
    name: 'fileChange-allow',
    decision: 'accept',
    build: (runId: string) => buildConfig('fileChange-allow', Method.FileChangeRequestApproval, runId),
  },
  'fileChange-deny': {
    name: 'fileChange-deny',
    decision: 'decline',
    build: (runId: string) => buildConfig('fileChange-deny', Method.FileChangeRequestApproval, runId),
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
