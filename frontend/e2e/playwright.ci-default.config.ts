// B3a-CI-1：default 套件的 CI 專用設定覆蓋檔（decision470 收斂實作選擇 #4）。
//
// 沿用 `playwright.config.ts` 的完整設定物件（testDir/testMatch/testIgnore/
// fullyParallel/workers/retries/timeout/globalSetup/globalTeardown/outputDir/
// use 等一律不變），只在既有 list reporter 之外新增 JSON reporter，供 CI
// evaluator 做逐案結果核對（不得只解析終端「passed」字串冒充逐案核對，見
// decision470 第 9 行）。
//
// 合併語意（先讀 Playwright 1.63.0 原始碼確認，見 progress.md Step 2）：
// `defineConfig(base, override)`（node_modules/playwright/lib/common/index.js
// 的 `defineConfig`）對頂層 key 是淺層覆寫——`reporter` 不會被深合併，這裡
// 整段指定等於「新增」；未在 override 列出的頂層 key（testDir/testMatch/
// testIgnore/workers/retries/timeout/globalSetup/globalTeardown/use...）
// 保證沿用 baseConfig 原值。不改 testMatch/testIgnore、retries、timeout、
// 瀏覽器或 provider 行為。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import baseConfig from './playwright.config.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');

export default defineConfig(baseConfig, {
  reporter: [
    ['list'],
    ['json', { outputFile: path.join(artifactsDir, 'playwright-results.json') }],
  ],
});
