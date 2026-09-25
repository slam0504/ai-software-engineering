// B3a-2a：review #4（#430）必修缺陷 2 的離線負控制專用 Playwright
// config——見 gateAfterEachHookNegativeControl.spec.ts 開頭說明。
//
// 刻意不沿用 playwright.gates.config.ts：這個負控制完全離線，不起
// App、不開 browser、沒有 page fixture，也不需要
// global-setup.gates.ts／global-teardown.ts（那兩個檔案本輪任務明確禁止
// 修改，這裡也用不到）。
//
// outputDir 必須由呼叫端（gateAfterEachHook.selftest.ts）透過
// `GATE_HOOK_NEGCTRL_OUTPUT_DIR` 環境變數指定——不給預設值退回
// worktree，避免在 worktree 留下 test-results/。
import { defineConfig } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outputDir = process.env.GATE_HOOK_NEGCTRL_OUTPUT_DIR;
if (!outputDir) {
  throw new Error(
    'gateAfterEachHookNegativeControl.config.ts: 必須設定 GATE_HOOK_NEGCTRL_OUTPUT_DIR'
    + '（避免 Playwright 用預設的 test-results/ 在 worktree 留下殘留檔）',
  );
}

export default defineConfig({
  testDir: __dirname,
  testMatch: ['gateAfterEachHookNegativeControl.spec.ts'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  outputDir: path.join(outputDir, 'test-results'),
  reporter: [['list'], ['json', { outputFile: path.join(outputDir, 'results.json') }]],
});
