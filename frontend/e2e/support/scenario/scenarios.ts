// B3a-2b-2 Task C：scenario 名稱 → ScenarioConfig 的唯一登記表。
//
// 裁定（票面既有）：scenario entry 要求明確有效的 scenario、一個 run-id 只跑
// 指定案——這裡沒有「預設 scenario」這回事，未指定或指定到不存在的名稱一律
// throw，不回退。本次（Task C）只登記 `commandExecution-allow` 這一案，其餘
// 三案（file-change-allow／deny／timeout 等）不在本次範圍內，不預先登記空殼。
import { Method } from './protocol.js';
import type { ScenarioConfig } from './protocol.js';

export interface ScenarioDef {
  name: string;
  build(runId: string): ScenarioConfig;
}

const SCENARIOS: Record<string, ScenarioDef> = {
  'commandExecution-allow': {
    name: 'commandExecution-allow',
    build(runId: string): ScenarioConfig {
      return {
        scenario: 'commandExecution-allow',
        threadId: `b3a2b2-thread-${runId}`,
        turnId: `b3a2b2-turn-${runId}`,
        itemId: `b3a2b2-item-${runId}`,
        threadMode: 'start', // 真 App 走第一輪 StartSession（新 session），對應 thread/start。
        approvalMethod: Method.CmdExecRequestApproval,
        approvalRequestId: `b3a2b2-approval-${runId}`,
        afterApproval: [
          { type: 'itemStarted', text: `b3a2b2-scenario-content-${runId}` },
          { type: 'itemCompleted', text: `b3a2b2-scenario-content-${runId}` },
        ],
        turnStatus: 'completed',
      };
    },
  },
};

export function resolveScenario(name: string | undefined): ScenarioDef {
  if (!name) {
    throw new Error(
      'resolveScenario: E2E_SCENARIO 未設定——scenario 入口要求明確有效的 scenario，不回退 default',
    );
  }
  const def = SCENARIOS[name];
  if (!def) {
    throw new Error(`resolveScenario: 未知 scenario "${name}"，可用：${Object.keys(SCENARIOS).join('、')}`);
  }
  return def;
}
