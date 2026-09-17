// 跨 globalSetup／globalTeardown 共用的執行期物件（同一個 Playwright 主行程內
// 的 module-level singleton）。globalTeardown 讀不到時（理論上的行程隔離）
// 會退回純檔案重建（見 global-teardown.ts），不依賴這裡一定有值。
// I1 修正（reviewer 六次審查，2026-09-16）：這四個都只當成 interface 欄位的
// 型別用，從未在這個檔案裡實際 new 出來——改成 `import type` 是純語法調整、
// 行為不變（編譯後本來就會被擦掉），目的是讓小測試腳本能用 Node 原生 TS
// strip-only 模式直接 import 這個檔案（間接被 global-teardown.ts 引用），
// 不會再因為 NetworkSampler／ProcessTree 等類別的 constructor 參數屬性簡寫
// 而丟出 ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX。
import type { HarnessLogger } from './logger.js';
import type { NetworkSampler } from './networkSampler.js';
import type { ProcessTree } from './processTree.js';
import type { RunStateStore } from './runState.js';
import type { FileState } from './artifactIntegrity.js';

interface Runtime {
  log: HarnessLogger | null;
  processTree: ProcessTree | null;
  networkSampler: NetworkSampler | null;
  runState: RunStateStore | null;
  artifactsDir: string | null;
  artifactsRoot: string | null;
  fixtureRoot: string | null;
  setupPid: number | null;
  // Q3：預檢通過後落地的「執行前」受版控產物內容快照，globalTeardown 用來
  // 跟「執行後」比對（見 artifactIntegrity.ts）。同行程優先讀這裡，讀不到
  // 才退回檔案（artifact-integrity-baseline.json）。
  artifactBaselineSnapshot: FileState[] | null;
}

export const runtime: Runtime = {
  log: null,
  processTree: null,
  networkSampler: null,
  runState: null,
  artifactsDir: null,
  artifactsRoot: null,
  fixtureRoot: null,
  setupPid: null,
  artifactBaselineSnapshot: null,
};
