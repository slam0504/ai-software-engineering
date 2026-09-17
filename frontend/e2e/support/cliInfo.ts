// 啟動後核對（§2.2）：透過 `window.go.main.App.CLIInfo()` 逐欄核對，而不是只看
// 標題列的 `tools: env` 文字——那不足以證明用的是預期的假 CLI。
//
// M3b 經驗：CLIInfo 是 onMounted 後才 resolve 的 async wails binding call，
// `workbench:cli-ready` 事件發出前欄位可能還沒齊，所以用輪詢而不是單次讀取。
import type { Page } from '@playwright/test';

export interface CLIInfoResult {
  toolsDir: string;
  toolsSource: string;
  claudeVersion: string;
  codexVersion: string;
  node: string;
  workspace: string;
  workspaceSource: string;
  startupError: string;
  ready: string;
}

declare global {
  interface Window {
    go?: { main?: { App?: { CLIInfo?: () => Promise<CLIInfoResult> } } };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise(r => setTimeout(r, ms));
}

export async function waitForCliReady(page: Page, timeoutMs = 30_000): Promise<CLIInfoResult> {
  const deadline = Date.now() + timeoutMs;
  let last: CLIInfoResult | null = null;
  while (Date.now() < deadline) {
    last = await page.evaluate(async () => {
      const fn = window.go?.main?.App?.CLIInfo;
      if (!fn) return null;
      return fn();
    });
    if (last && last.ready === 'true') return last;
    await sleep(250);
  }
  throw new Error(`等待 CLIInfo().ready === "true" 逾時（${timeoutMs}ms）；最後一次讀到：${JSON.stringify(last)}`);
}
