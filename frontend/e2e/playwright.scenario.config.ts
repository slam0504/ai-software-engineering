// B3a-2b-2 Task C：Codex 單一 approval 的 browser 整合檢查點——獨立
// scenario 套件。跟預設 `playwright.config.ts`／`playwright.controls.config.ts`
// 分開（票面裁定：default／controls 禁止 E2E_SCENARIO），只跑
// `e2e/scenarios/**/*.spec.ts`；globalSetup 換成
// `./global-setup.scenario.ts`（要求 E2E_SCENARIO 明確有效），globalTeardown
// 沿用既有 `./global-teardown.ts`（沒有假設特定 spec 內容，通用於
// run-env／run-state／process-tree／network 這幾層既有契約）。
//
// 1 worker／0 retries（沿用既有裁定，不平行、不重試）。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');
const useChromium = process.env.E2E_BROWSER === 'chromium';

export default defineConfig({
  testDir: path.join(__dirname, 'scenarios'),
  testMatch: ['*.spec.ts'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  globalSetup: './global-setup.scenario.ts',
  globalTeardown: './global-teardown.ts',
  outputDir: path.join(artifactsDir, 'playwright'),
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // 同 playwright.config.ts：關掉 Service Worker，讓 browser 層網路判定
    // （support/networkGuard.ts）涵蓋得到。
    serviceWorkers: 'block',
    ...(useChromium ? {} : { channel: 'chrome' }),
    launchOptions: {
      // 與 playwright.config.ts／playwright.controls.config.ts 同一組旗標
      // （F1 裁定：只關 Chrome 自己的背景網路雜訊，不動 sandbox、不加
      // host-resolver-rules）。
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
