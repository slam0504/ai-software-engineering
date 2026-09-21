// B3a-1 Browser E2E — 主要 smoke suite（§2.1）。
//
// 單一 worker、零重試、不平行（§2.1 裁定）。瀏覽器預設用系統 Chrome
// （`channel: 'chrome'`），設 E2E_BROWSER=chromium 時改用 Playwright 內建
// Chromium（需準備階段先 `npx playwright install chromium`）。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromeWrapperPath, isOfflineSandboxEnabled, validateOfflineSandboxPrereqs } from './support/offlineSandbox.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');
const useChromium = process.env.E2E_BROWSER === 'chromium';

// E2E offline 驗證模式（reviewer 授權實作，複核 #34 第六／七次，2026-09-16）：
// opt-in（`E2E_OFFLINE_SANDBOX=1`），預設路徑完全不受影響。這裡的檢查在
// config 載入當下（同步、任何 worker／test 開始之前）就會執行，前置條件
// 不滿足或 `E2E_BROWSER` 組合這個模式目前不支援，直接拋錯——**不會靜默
// 回退成無 sandbox 的瀏覽器**。
if (isOfflineSandboxEnabled()) {
  validateOfflineSandboxPrereqs(useChromium ? 'chromium' : 'chrome');
}

export default defineConfig({
  testDir: __dirname,
  testMatch: ['*.spec.ts'],
  // B3a-2b-2 Task C：scenario 套件有自己的入口
  // （playwright.scenario.config.ts／test:e2e:scenario），default 這裡明確
  // 排除，避免 default（禁止 E2E_SCENARIO 的入口，見 global-setup.ts 開頭
  // 守門）意外把 scenario spec 一起收集進來。
  testIgnore: ['controls/**', 'scenarios/**'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  outputDir: path.join(artifactsDir, 'playwright'),
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    // Service Worker 會繞過 context.route／routeWebSocket 的攔截，關掉才能讓
    // browser 層的網路判定（support/networkGuard.ts）真的涵蓋得到（reviewer
    // F1 裁定＋必修項目）。不動 sandbox 相關設定——chromiumSandbox 維持
    // Playwright 預設值，沒有手動加或拿掉任何 sandbox 旗標。
    serviceWorkers: 'block',
    ...(isOfflineSandboxEnabled() ? {} : (useChromium ? {} : { channel: 'chrome' })),
    launchOptions: {
      // offline 驗證模式：executablePath 指到 chromeWrapper.sh（`exec
      // sandbox-exec -f <profile> <系統 Chrome>`），跟 `channel: 'chrome'`
      // 互斥，所以上面的 `channel` 這裡不能同時設。只支援系統 Chrome
      // （驗證過的候選），`useChromium` 組合已經在上面 `validateOfflineSandboxPrereqs`
      // 擋掉。
      ...(isOfflineSandboxEnabled() ? { executablePath: chromeWrapperPath() } : {}),
      // 系統 Chrome 即使是全新 temp profile，預設仍會在背景打一些跟我們的
      // 頁面操作無關的連線（Safe Browsing 更新、GCM push、元件更新等）。
      // 這些旗標關掉的是 Chrome 內建背景服務本身（不影響我們自己頁面的
      // fetch／WebSocket 行為），列表與用途見 README／回報說明。
      // **F1 裁定（reviewer，2026-09-15）**：移除先前用來壓背景流量的
      // `--host-resolver-rules=MAP * 127.0.0.1`——那是「與 smoke 無關的
      // Chrome 行為旗標」，不應該為了讓程序取樣判定變乾淨而加這種副作用
      // 大的設定。網路是否合規改由 browser 層（HTTP／WebSocket route）與
      // 程序取樣（只對已辨識的 Chrome／Chromium 排除嚴格判定）分別把關。
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
