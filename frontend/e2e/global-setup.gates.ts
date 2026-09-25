// B3a-2a：gates 入口（playwright.gates.config.ts）的 globalSetup——薄包裝
// （design v4 §2.4，reviewer #424 (d) 裁定）。不複製 `./global-setup.ts`
// 的 fixture／假 CLI／預檢／spawn／等待就緒／run-env／signal handler／
// teardown-on-failure 邏輯，全部交給既有 default globalSetup 處理：這裡只
// 做 gates 專屬的前置驗證（E2E_GATE 是否有效）與 flow descriptor 落地
// （artifacts/gate-flow.json），然後 `await` 既有 default globalSetup。
//
// `execution-entry.json` 維持 `{entry:'default'}`（由 default globalSetup
// 自己寫入，不在這裡另外標記 gate flow）——global-teardown.ts 的
// `determineExecutionMode()` 沿用既有的 `entry==='default'` 分支，gates
// 入口不需要新增 entry 值（design v4 §2.4）。
import fs from 'node:fs';
import path from 'node:path';
import defaultGlobalSetup from './global-setup.js';
import { GATE_FLOW_SPEC_FILE, resolveGateFlow } from './support/gates/gateRouting.js';
import { isOfflineSandboxEnabled } from './support/offlineSandbox.js';

export default async function globalSetupGates(): Promise<void> {
  const artifactsDir = process.env.E2E_ARTIFACTS_DIR;
  const runId = process.env.E2E_RUN_ID;

  // review #427 additional corrections：setup 再驗一次 offline-sandbox 拒絕
  // （config 載入期已經擋過一次，這裡確保直接呼叫 playwright 而繞過 config
  // 檢查路徑的情境——理論上不會發生，但「setup 再驗一次」是既有慣例，見
  // 下方 E2E_GATE 的同一句注解）。
  if (isOfflineSandboxEnabled()) {
    const message = 'globalSetupGates: E2E_OFFLINE_SANDBOX=1 但 gates 入口尚未支援 offline sandbox 模式——'
      + '明確拒絕、不啟動 app，避免旗標被接受卻悄悄開一般 Chrome。';
    if (artifactsDir) {
      fs.mkdirSync(artifactsDir, { recursive: true });
      fs.appendFileSync(path.join(artifactsDir, 'harness.log'), `[${new Date().toISOString()}] ${message}\n`);
    }
    throw new Error(message);
  }

  let flow: ReturnType<typeof resolveGateFlow>;
  try {
    flow = resolveGateFlow(process.env.E2E_GATE);
  } catch (e) {
    // config 載入當下（playwright.gates.config.ts）已經驗證過一次
    // E2E_GATE——這裡是「setup 再驗一次」（design v4 §2.2 對 (d) 的要求：
    // 不能在 config 與 spawn 之間默默換 flow）。理論上不該在這裡才第一次
    // 失敗，但仍照既有慣例（run-e2e.mjs 對 offline-sandbox 前置失敗的寫法）
    // 儘量留下 harness.log。
    if (artifactsDir) {
      fs.mkdirSync(artifactsDir, { recursive: true });
      fs.appendFileSync(
        path.join(artifactsDir, 'harness.log'),
        `[${new Date().toISOString()}] globalSetupGates: E2E_GATE 驗證失敗，未啟動 app：${e instanceof Error ? e.message : String(e)}\n`,
      );
    }
    throw e;
  }

  if (artifactsDir && runId) {
    // gates 專屬 flow descriptor——單一寫入者（本檔），spec 在 test
    // callback 內讀取核對自身 flow／本次 runId／指定 spec（design v4
    // §2.5）。寫在 default globalSetup 之前：即使後面失敗，這份標記仍能
    // 說明「這次執行原本要跑哪個 flow」。
    fs.mkdirSync(artifactsDir, { recursive: true });
    fs.writeFileSync(
      path.join(artifactsDir, 'gate-flow.json'),
      JSON.stringify({ runId, flow, specFile: GATE_FLOW_SPEC_FILE[flow] }, null, 2),
    );
  }

  // 薄包裝到此為止：其餘（fixture／假 CLI／預檢／spawn／等待就緒／
  // run-env／signal handler／teardown-on-failure）全部交給既有 default
  // globalSetup。gates 入口不設定 E2E_SCENARIO，default globalSetup 開頭
  // 的守門（global-setup.ts:55，只禁 E2E_SCENARIO）不會被觸發。
  await defaultGlobalSetup();
}
