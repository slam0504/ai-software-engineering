// B3a-2b-2 Task C 第三輪限縮補正（缺陷 1）：執行模式判定，獨立成純函式模組。
//
// 背景：`global-teardown.ts` 全篇用 `.js` 副檔名 import（配合 Playwright 自己
// 的 TS loader／打包器，能把 `./foo.js` 解回 `./foo.ts`）。但這裡的
// `determineExecutionMode` 需要能被 `node xxx.selftest.ts` 直接 import 驗證
// 負控制（不透過整套 Playwright），Node 原生 TS stripping 不做副檔名remap、
// 解析不了 `.js` 指向 `.ts` 檔——因此獨立成這支模組，內部一律用 `.ts` 副檔名
// import（同 `scenarioProtocolJudge.ts` 既有模式：那支也是被 spec 用 `.js`
// import、自己內部用 `.ts` import protocol.ts）。`global-teardown.ts` 照舊用
// `.js` 匯入這支模組（Playwright loader 能解析，不受影響）。
//
// 第二輪遺留的缺陷（reviewer 第三次複核找到）：舊版 `determineExecutionMode`
// 只從落地檔讀 `raw.scenario` 一個欄位，其餘欄位只對傳入的 `env` 做 truthy
// 檢查（`!env[k]`），完全沒有交叉核對「run-env.json 檔案」與「env（process.env
// 路徑）」兩份來源是否一致、也沒驗過型別——導致四種反例都判錯（其一甚至把
// 「兩側 scenario 都是壞掉的數字」誤判成 default，見下方測試）。
//
// 修法：
//   1. 執行模式的「入口點」不再從 scenario 欄位 truthy 推斷，改讀
//      execution-entry.json——這是 global-setup.ts／global-setup.scenario.ts
//      各自在啟動最初就落地的入口標記，獨立於（可能被竄改／型別錯誤／缺漏
//      的）run-env.json scenario identity 欄位之外，正面回答「這次執行到底
//      是從哪個入口啟動的」，而不是靠資料完不完整去反推。
//   2. 確定是 scenario 入口之後，才核對 run-env.json 檔案（純檔案 fallback
//      路徑）與傳入的 env（process.env 路徑，由 readRunEnv() 決定）這兩份
//      來源的十個 identity 欄位：每一欄都要求「必要非空字串」且「兩邊值完全
//      相同」，任何一項不符（缺漏、型別錯誤、值不一致）都判定為
//      `scenario-broken`（fail closed），不得回退成 `default`。
import fs from 'node:fs';
import path from 'node:path';
import type { RunEnv } from './env.ts';
import type { HarnessLogger } from './logger.ts';

// 必須跟 env.ts 的 `readScenarioRunEnv` required 清單保持一致（那份是
// spec 用、這份是 teardown 用，兩邊各自獨立判定，欄位集合要同步）。
const REQUIRED_SCENARIO_FIELDS = [
  'scenarioThreadId', 'scenarioTurnId', 'scenarioItemId',
  'scenarioApprovalMethod', 'scenarioApprovalRequestId', 'scenarioDecision',
  'scenarioConfigPath', 'scenarioLogPath', 'scenarioManifestPath',
] as const satisfies ReadonlyArray<keyof RunEnv>;

// 十個 identity 欄位：scenario 本身＋上面九個。
const ALL_IDENTITY_FIELDS = ['scenario', ...REQUIRED_SCENARIO_FIELDS] as const;

export type ScenarioRunEnvLike = RunEnv & { scenario: string } & { [K in typeof REQUIRED_SCENARIO_FIELDS[number]]: string };

export type ExecutionMode =
  | { kind: 'default' }
  | { kind: 'scenario'; scenario: ScenarioRunEnvLike }
  | { kind: 'scenario-broken'; reason: string };

function isNonEmptyString(v: unknown): v is string {
  return typeof v === 'string' && v.length > 0;
}

// isPlainRecord：阻擋缺陷修正（B3a-2b-2 Task C）。`JSON.parse(...) as T` 只是
// 型別斷言，不是 runtime 檢查——`'null'`／`'[]'`／`'42'` 都是合法 JSON，
// `JSON.parse` 不會 throw，但結果分別是 `null`／array／number，不是預期的
// plain object。沒有這層檢查時，後續的 property access（`marker.entry`／
// `raw[field]`）對 `null` 會直接拋出未捕捉的 TypeError，讓呼叫端（尤其是
// global-teardown.ts）在還沒做完 bounded stop 之前就整個中止。這裡明確排除
// `null` 與 array，只接受非 null 的 plain object，其餘一律視為壞掉的落地檔。
function isPlainRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

interface ExecutionEntryMarker {
  entry?: unknown;
}

// export：讓 determineExecutionMode 能被獨立 selftest（純函式——只讀傳入的
// `env` 參數與 `artifactsDir` 下的 execution-entry.json／run-env.json，不依賴
// process.env／runtime／processTree 等 ambient 狀態），不需要透過完整的
// globalTeardown() 才能驗證負控制。刻意不呼叫 `readScenarioRunEnv()`（那個
// 函式會重新呼叫 `readRunEnv()`、重新讀一次 process.env／檔案），避免這裡的
// 判定結果偷偷依賴呼叫當下的 process.env 狀態，跟明確傳入的 `env` 參數不一致。
export function determineExecutionMode(env: RunEnv, artifactsDir: string, log: HarnessLogger): ExecutionMode {
  const markerPath = path.join(artifactsDir, 'execution-entry.json');
  let markerParsed: unknown;
  try {
    markerParsed = JSON.parse(fs.readFileSync(markerPath, 'utf8'));
  } catch (e) {
    return {
      kind: 'scenario-broken',
      reason: `無法讀取入口標記 execution-entry.json（${markerPath}），無法判定是 default 還是 scenario 入口，`
        + `不得默認為 default：${String(e)}`,
    };
  }
  if (!isPlainRecord(markerParsed)) {
    return {
      kind: 'scenario-broken',
      reason: `execution-entry.json（${markerPath}）內容不是合法的物件（型別為 ${markerParsed === null ? 'null' : Array.isArray(markerParsed) ? 'array' : typeof markerParsed}），無法判定入口`,
    };
  }
  const marker: ExecutionEntryMarker = markerParsed;

  if (marker.entry === 'default') {
    return { kind: 'default' };
  }
  if (marker.entry !== 'scenario') {
    return {
      kind: 'scenario-broken',
      reason: `execution-entry.json 的 entry 欄位值無法辨識（既非 'default' 也非 'scenario'）：${JSON.stringify(marker.entry)}`,
    };
  }

  // 入口標記確定是 scenario：接下來核對 run-env.json 檔案（純檔案 fallback
  // 路徑）與傳入的 env（process.env 路徑）這兩份來源的十個 identity 欄位。
  let rawParsed: unknown;
  try {
    rawParsed = JSON.parse(fs.readFileSync(path.join(artifactsDir, 'run-env.json'), 'utf8'));
  } catch (e) {
    return {
      kind: 'scenario-broken',
      reason: `入口標記為 scenario，但無法讀取 run-env.json 核對 identity 欄位：${String(e)}`,
    };
  }
  if (!isPlainRecord(rawParsed)) {
    return {
      kind: 'scenario-broken',
      reason: `入口標記為 scenario，但 run-env.json 內容不是合法的物件（型別為 ${rawParsed === null ? 'null' : Array.isArray(rawParsed) ? 'array' : typeof rawParsed}），無法核對 identity 欄位`,
    };
  }
  const raw: Record<string, unknown> = rawParsed;

  const mismatches: string[] = [];
  for (const field of ALL_IDENTITY_FIELDS) {
    const envVal = (env as unknown as Record<string, unknown>)[field];
    const fileVal = raw[field];
    const envOk = isNonEmptyString(envVal);
    const fileOk = isNonEmptyString(fileVal);
    if (!envOk || !fileOk) {
      mismatches.push(
        `${field}：env=${envOk ? JSON.stringify(envVal) : `缺失或型別錯誤（${JSON.stringify(envVal)}）`}、`
        + `file=${fileOk ? JSON.stringify(fileVal) : `缺失或型別錯誤（${JSON.stringify(fileVal)}）`}`,
      );
    } else if (envVal !== fileVal) {
      mismatches.push(`${field}：env=${JSON.stringify(envVal)} 與 file=${JSON.stringify(fileVal)} 不一致`);
    }
  }
  if (mismatches.length > 0) {
    const reason = `scenario identity 欄位驗證失敗（${mismatches.length} 項不符或缺漏）：${mismatches.join('；')}`;
    log.log(`determineExecutionMode：${reason}`);
    return { kind: 'scenario-broken', reason };
  }

  return { kind: 'scenario', scenario: env as ScenarioRunEnvLike };
}
