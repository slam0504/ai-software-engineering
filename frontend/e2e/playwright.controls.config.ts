// B3a-1 Browser E2E — 存檔判定受控對照（§2.5）。獨立指令 `npm run test:e2e:controls`，
// 不列入預設 `test:e2e` 套件（owner 裁定，2026-09-15 rev3 §Q3）。共用同一組
// globalSetup／globalTeardown。
//
// 額外加 json reporter：對照 A 用 `test.fail()` 把預期中的失敗轉成通過，
// 驗證步驟需要從結構化結果裡撈出實際錯誤訊息（確認真的來自目標存檔斷言），
// 只看 list reporter 的文字輸出不夠可靠。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromeWrapperPath, isOfflineSandboxEnabled, validateOfflineSandboxPrereqs } from './support/offlineSandbox.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');
const useChromium = process.env.E2E_BROWSER === 'chromium';

// 跟 playwright.config.ts 同理：E2E offline 驗證模式的前置檢查，config 載入
// 當下就同步執行，不滿足直接拋錯，不靜默回退。
if (isOfflineSandboxEnabled()) {
  validateOfflineSandboxPrereqs(useChromium ? 'chromium' : 'chrome');
}

export default defineConfig({
  testDir: path.join(__dirname, 'controls'),
  testMatch: ['*.spec.ts'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  outputDir: path.join(artifactsDir, 'playwright'),
  reporter: [['list'], ['json', { outputFile: path.join(artifactsDir, 'playwright-results.json') }]],
  use: {
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // 與 playwright.config.ts 同理：Service Worker 會繞過 route 攔截，關掉
    // 才能讓 browser 層網路判定涵蓋得到；不動 sandbox 相關設定。
    serviceWorkers: 'block',
    ...(isOfflineSandboxEnabled() ? {} : (useChromium ? {} : { channel: 'chrome' })),
    launchOptions: {
      // 與 playwright.config.ts 同理：關掉 Chrome 內建的背景網路雜訊本身，
      // 不影響我們自己頁面的 fetch／WebSocket 行為。**F1 裁定**：不加
      // `--host-resolver-rules`。offline 驗證模式：executablePath 指到
      // chromeWrapper.sh，跟 `channel` 互斥。
      ...(isOfflineSandboxEnabled() ? { executablePath: chromeWrapperPath() } : {}),
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
