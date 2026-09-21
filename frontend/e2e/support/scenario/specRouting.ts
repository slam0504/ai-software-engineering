// B3a-2b-2 Task E2：playwright.scenario.config.ts 的 `testMatch` 原本固定
// `*.spec.ts`——新增 `codexSessionRecovery.spec.ts` 之後，`scenarios/` 目錄
// 下有兩支 spec，一次 invocation 會兩案同跑，不符合「一個 run-id 只跑指定
// 案」的既有契約（scenarios.ts resolveScenario 的裁定原本只管 config 選
// 哪一案，沒管 playwright 該收集哪一份 spec 檔）。
//
// 本模組是純函式：依 `E2E_SCENARIO` 對應的 `ScenarioDef.kind`
// （scenarios.ts 的單一登記表，不另建第二份名稱清單）決定唯一該收集的 spec
// 檔名。缺失／未知名稱**不**在這裡拒絕——那個契約已經由
// global-setup.scenario.ts 的 `resolveScenario()` 在啟動前 throw 並留
// harness.log（見該檔），這裡只需要回傳一個不影響那個既有行為的中性預設值
// （挑 approval spec），讓 playwright 仍能載入 config、把失敗留給
// globalSetup 產生正確的錯誤與證據。
import { resolveScenario } from './scenarios.js';

export const APPROVAL_SPEC_FILE = 'codexApproval.spec.ts';
export const RECOVERY_SPEC_FILE = 'codexSessionRecovery.spec.ts';

export function resolveScenarioSpecFile(scenarioName: string | undefined): string {
  let def: ReturnType<typeof resolveScenario>;
  try {
    def = resolveScenario(scenarioName);
  } catch {
    // 缺失／未知：globalSetupScenario 會在啟動前 throw、留 harness.log——
    // 這裡的挑選結果不影響那個既有契約，回傳中性預設值即可。
    return APPROVAL_SPEC_FILE;
  }
  return def.kind === 'recovery' ? RECOVERY_SPEC_FILE : APPROVAL_SPEC_FILE;
}
