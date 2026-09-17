// 只供 `stopProcedure.selftest.ts` 這類「用 Node 原生 TS 直接執行、但受測模組
// 本身有跨檔案相依」的自測腳本使用（reviewer 複核 #34 要求的小型停止程序
// 測試）。專案裡的正式程式碼一律用 `./foo.js` 這種寫法互相匯入（給
// Playwright 自己的 esbuild-based loader用，見 stopProcedure.ts 等檔案頂端
// 的說明），但純用 `node file.ts` 直接執行時，Node 原生的 ESM resolver 不會
// 自動把 `.js` 轉回真正存在的 `.ts` 檔——這裡註冊一個最小的 resolve hook，
// 只在「相對路徑匯入、副檔名是 .js、但同名 .js 檔不存在、有同名 .ts 檔」時
// 才轉址，其餘一律照 Node 預設行為處理，不影響任何正式執行路徑（Playwright
// 測試本身完全不會載入這個檔案）。
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export async function resolve(specifier, context, nextResolve) {
  if (specifier.startsWith('.') && specifier.endsWith('.js') && context.parentURL) {
    const jsURL = new URL(specifier, context.parentURL);
    const tsSpecifier = `${specifier.slice(0, -3)}.ts`;
    const tsURL = new URL(tsSpecifier, context.parentURL);
    if (!existsSync(fileURLToPath(jsURL)) && existsSync(fileURLToPath(tsURL))) {
      return nextResolve(tsSpecifier, context);
    }
  }
  return nextResolve(specifier, context);
}
