// §2.5 存檔判定受控對照共用：在測試端包裝 `window.go.main.App.SpecWrite`，
// 原呼叫之前先等固定毫秒數，再轉呼叫原函式。**只在測試端注入，不改
// production 程式碼**——生效依據是 F1（App.js 的 SpecWrite 在呼叫當下才讀取
// `window.go.main.App.SpecWrite`，見設計 §2.5）。
//
// 呼叫時保留原函式的 `this`／`args`（`orig.apply(this, args)`），不得改變呼叫
// 語意；同時把呼叫時間、args 摘要、實際延遲 ms 記到 `window[evidenceKey]`，
// 供測試自己核對「wrapper 確實生效」。
import type { Page } from '@playwright/test';

export interface WrapperEvidenceEntry {
  callTime: string;
  argsSummary: unknown[];
  actualDelayMs: number;
}

declare global {
  interface Window {
    [key: string]: unknown;
  }
}

export async function installSpecWriteDelay(page: Page, delayMs: number, evidenceKey: string): Promise<void> {
  await page.evaluate(
    ({ delayMs, evidenceKey }) => {
      const w = window as unknown as Record<string, unknown>;
      w[evidenceKey] = [];
      const app = (w.go as { main: { App: Record<string, unknown> } }).main.App;
      const orig = app.SpecWrite as (...args: unknown[]) => Promise<unknown>;
      app.SpecWrite = function wrapped(this: unknown, ...args: unknown[]): Promise<unknown> {
        const callTime = new Date().toISOString();
        const start = Date.now();
        return new Promise((resolve, reject) => {
          setTimeout(() => {
            const actualDelayMs = Date.now() - start;
            (w[evidenceKey] as unknown[]).push({
              callTime,
              argsSummary: args.map(a => (typeof a === 'string' ? a.slice(0, 120) : a)),
              actualDelayMs,
            });
            orig.apply(this, args).then(resolve, reject);
          }, delayMs);
        });
      };
    },
    { delayMs, evidenceKey },
  );
}

export async function collectWrapperEvidence(page: Page, evidenceKey: string): Promise<WrapperEvidenceEntry[]> {
  return page.evaluate(key => (window as unknown as Record<string, unknown>)[key] as WrapperEvidenceEntry[], evidenceKey);
}
