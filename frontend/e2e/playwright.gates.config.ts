// B3a-2a：Gate 1／Gate 2／STALE 三條 browser E2E 流程的獨立入口（design v4
// §2.3）。鏡射 playwright.scenario.config.ts 的整體結構（1 worker／0
// retries／`globalTeardown` 沿用既有 `./global-teardown.ts`），但 flow
// resolver 刻意採嚴格語意（缺失／空字串／未知值一律拒絕，**含 `--list`**）
// ——不套用 scenario 入口「未知名稱回傳中性預設值」的既有慣例（reviewer
// #424 (d) 裁定，見 support/gates/gateRouting.ts 開頭說明）。
import { defineConfig } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { GATE_FLOW_SPEC_FILE, resolveGateFlow } from './support/gates/gateRouting.js';
import { isOfflineSandboxEnabled } from './support/offlineSandbox.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');
const useChromium = process.env.E2E_BROWSER === 'chromium';

/**
 * review #427 additional corrections：gates 入口本輪不支援
 * `E2E_OFFLINE_SANDBOX`——鏡射 `global-setup.scenario.ts` 對同一旗標的既有
 * 處理方式（scenario 入口同樣未接 offline sandbox wrapper，選擇明確拒絕，
 * 不是悄悄降級成一般 browser）。跟 default／controls 不同：那兩支 config
 * 在 `use.launchOptions` 接了完整的 sandbox wrapper／executablePath；gates
 * 入口本輪未做這件事，若放行旗標會讓它被接受卻悄悄開一般 Chrome，跟其他
 * 入口在 sandbox 保護上的行為不一致。在 config 載入期（含 `--list`）與
 * `global-setup.gates.ts` 都做這個檢查，兩處各自独立拒絕。
 */
function rejectOfflineSandboxOrLog(): void {
  if (!isOfflineSandboxEnabled()) return;
  const message = 'playwright.gates.config.ts: E2E_OFFLINE_SANDBOX=1 但 gates 入口尚未支援 offline sandbox 模式'
    + '（未接 sandbox wrapper／executablePath，鏡射 global-setup.scenario.ts 對同一旗標的既有裁定）——'
    + '明確拒絕、不啟動，避免旗標被接受卻悄悄開一般 Chrome。';
  if (process.env.E2E_ARTIFACTS_DIR) {
    fs.mkdirSync(process.env.E2E_ARTIFACTS_DIR, { recursive: true });
    fs.appendFileSync(path.join(process.env.E2E_ARTIFACTS_DIR, 'harness.log'), `[${new Date().toISOString()}] ${message}\n`);
  }
  throw new Error(message);
}

function resolveGateFlowOrLog(): ReturnType<typeof resolveGateFlow> {
  rejectOfflineSandboxOrLog();
  try {
    return resolveGateFlow(process.env.E2E_GATE);
  } catch (e) {
    // config 載入期失敗（含 `--list`）：globalSetup 還沒機會建立
    // HarnessLogger，沿用 run-e2e.mjs 對 offline-sandbox 前置失敗的既有
    // pattern——E2E_ARTIFACTS_DIR 存在（npm run test:e2e:gates 一定會有，
    // run-e2e.mjs 在 spawn playwright 之前就設定好）就直接 append 一行
    // harness.log；不存在（直接呼叫 playwright 且未透過 npm wrapper）就只
    // 剩 stderr／exit code，這是既有入口本來就承認的支援邊界。
    if (process.env.E2E_ARTIFACTS_DIR) {
      fs.mkdirSync(process.env.E2E_ARTIFACTS_DIR, { recursive: true });
      fs.appendFileSync(
        path.join(process.env.E2E_ARTIFACTS_DIR, 'harness.log'),
        `[${new Date().toISOString()}] playwright.gates.config.ts: E2E_GATE 驗證失敗（config 載入期，未 spawn 任何程序）：${e instanceof Error ? e.message : String(e)}\n`,
      );
    }
    throw e;
  }
}

const gateFlow = resolveGateFlowOrLog();

export default defineConfig({
  testDir: path.join(__dirname, 'gates'),
  testMatch: [GATE_FLOW_SPEC_FILE[gateFlow]],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  globalSetup: './global-setup.gates.ts',
  globalTeardown: './global-teardown.ts',
  outputDir: path.join(artifactsDir, 'playwright'),
  // review #427 必修缺陷 6：加 JSON reporter，鏡射
  // playwright.controls.config.ts 既有的做法（同一個檔名慣例
  // `playwright-results.json`），供事後檢視每個 test 的判定結果——
  // 不是 gate 專屬證據（那部分由 support/gates/gateEvidence.ts 在
  // afterEach 內另外保存），是 Playwright 執行本身的標準機讀輸出。
  reporter: [['list'], ['json', { outputFile: path.join(artifactsDir, 'playwright-results.json') }]],
  use: {
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // 同 playwright.config.ts／playwright.scenario.config.ts：關掉 Service
    // Worker，讓 browser 層網路判定（support/networkGuard.ts）涵蓋得到。
    serviceWorkers: 'block',
    ...(useChromium ? {} : { channel: 'chrome' }),
    launchOptions: {
      // 與其餘三支 config 同一組旗標（F1 裁定：只關 Chrome 自己的背景網路
      // 雜訊，不動 sandbox、不加 host-resolver-rules）。
      args: [
        '--disable-background-networking',
        '--disable-component-update',
        '--disable-domain-reliability',
        '--disable-sync',
        '--disable-client-side-phishing-detection',
        '--disable-background-timer-throttling',
        '--disable-backgrounding-occluded-windows',
        '--disable-renderer-backgrounding',
        '--disable-ipc-flooding-protection',
        '--disable-breakpad',
        '--disable-crash-reporter',
        '--disable-default-apps',
        '--disable-search-engine-choice-screen',
        '--metrics-recording-only',
        '--no-pings',
        '--disable-features=Translate,OptimizationHints,OptimizationHintsFetching,'
          + 'OptimizationTargetPrediction,OptimizationGuideModelDownloading,'
          + 'AutofillServerCommunication,CertificateTransparencyComponentUpdater,'
          + 'MediaRouter,DialMediaRouteProvider,CastMediaRouteProvider,PushMessaging,'
          + 'NetworkTimeServiceQuerying,SafeBrowsingV4,PasswordLeakDetection',
      ],
    },
  },
});
