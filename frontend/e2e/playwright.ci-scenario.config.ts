// B3a-CI-1：scenario 套件的 CI 專用設定覆蓋檔（decision470 收斂實作選擇 #4）。
//
// 沿用 `playwright.scenario.config.ts` 的完整設定物件（含依 `E2E_SCENARIO`
// 算出的 `testMatch: [scenarioSpecFile]`——這個計算在 import 當下就會執行，
// 跟直接跑原 config 的既有行為一致，見 progress.md Step 2 對繼承語意的核
// 對）。只在既有 list reporter 之外新增 JSON reporter，供 CI evaluator 做
// 逐案結果核對。不改 testMatch、retries、timeout、瀏覽器或 provider 行為。
//
// 合併語意同 playwright.ci-default.config.ts：`defineConfig(base, override)`
// 頂層 key 淺層覆寫，`reporter` 整段被此檔取代（等於新增），其餘欄位沿用
// baseConfig 原值。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import baseConfig from './playwright.scenario.config.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifactsDir = process.env.E2E_ARTIFACTS_DIR ?? path.join(__dirname, '.artifacts', 'no-run-id');

export default defineConfig(baseConfig, {
  reporter: [
    ['list'],
    ['json', { outputFile: path.join(artifactsDir, 'playwright-results.json') }],
  ],
});
