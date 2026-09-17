// E2E offline 驗證模式（reviewer 授權實作，複核 #34 第六／七次，2026-09-16）：
// 明確 opt-in（`E2E_OFFLINE_SANDBOX=1`）才會啟用，**預設路徑完全不受影響**。
// 啟用時，Wails／app 後代與正式系統 Chrome 後代都會被包在同一個
// loopback-only 的 macOS `sandbox-exec` profile 內；harness／observer（這個
// Node 行程本身，含它呼叫的 `ps`／`lsof`）維持在 sandbox 外——這是
// reviewer 複核 #34 第四次的 S1 發現（`ps` 在 sandbox 內會因為
// `execvp Operation not permitted` 失效）之後，用第六次複核授權的受控診斷
// 驗證過可行的架構，見
// `frontend/e2e/.artifacts/1b-evidence-20260916/s1-controlled-diagnostic.txt`。
//
// **不得靜默回退成無 sandbox 的瀏覽器**：任何前置條件不滿足都要在啟動前
// 直接拋錯，不吞掉。
//
// P1 修正（reviewer 複核 #34 第八次，2026-09-16，實測用 fs mock／隔離路徑
// 重現「profile 不可讀」「sandbox-exec 不可執行」「Chrome 不存在」三種情境
// 都被原本的檢查放行）：
//   - profile：原本對 `.sb` 檔做 X_OK 檢查，且用空 catch 吞掉錯誤——`.sb`
//     檔本來就不需要執行位元，X_OK 對它沒有意義；改成確認是**可讀的一般
//     檔案**（`R_OK`＋`isFile()`），**不吞任何存取錯誤**。
//   - wrapper／`sandbox-exec`／系統 Chrome：原本只有 wrapper 檢查了
//     `X_OK`，`sandbox-exec` 只檢查存在（`existsSync`，不驗證可執行），
//     系統 Chrome 完全沒有在這裡檢查（要等 wrapper 真的執行時才會發現，
//     那時 Wails 可能已經啟動）——現在三者都在這裡統一用同一種「可執行的
//     一般檔案」檢查，**都在任何 Wails／Chrome 啟動之前**完成。
//   - 統一絕對路徑：`processTree.ts` 原本 spawn 裸的字串 `'sandbox-exec'`
//     （靠呼叫端當下的 `PATH` 解析），跟這裡驗證的 `/usr/bin/sandbox-exec`
//     不是同一個解析契約，理論上可能命中 `PATH` 上的另一支同名執行檔。
//     現在兩邊統一用這裡匯出的 `SANDBOX_EXEC_PATH`（絕對路徑常數）。
//   - 四個路徑都可以透過 `OfflineSandboxPaths` 參數覆寫（預設用真正的絕對
//     路徑）——**不是新增抽象層**，純粹是讓 selftest 能指向隔離的假路徑
//     （真的建立唯讀／不可執行／不存在的暫存檔案）驗證失敗情境，不用碰
//     系統檔案，正式呼叫端一律不傳、用預設值。
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export function isOfflineSandboxEnabled(): boolean {
  return process.env.E2E_OFFLINE_SANDBOX === '1';
}

export function sandboxProfilePath(): string {
  return path.join(__dirname, 'sandboxProfiles', 'loopback-only.sb');
}

export function chromeWrapperPath(): string {
  return path.join(__dirname, 'sandboxProfiles', 'chromeWrapper.sh');
}

// 絕對路徑常數：`processTree.ts` 實際 spawn 時跟這裡的預檢用**同一個值**，
// 不是各自解析（見上方模組說明）。
export const SANDBOX_EXEC_PATH = '/usr/bin/sandbox-exec';
export const SYSTEM_CHROME_PATH = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';

export interface OfflineSandboxPaths {
  profile: string;
  wrapper: string;
  sandboxExec: string;
  chrome: string;
}

export function defaultOfflineSandboxPaths(): OfflineSandboxPaths {
  return {
    profile: sandboxProfilePath(),
    wrapper: chromeWrapperPath(),
    sandboxExec: SANDBOX_EXEC_PATH,
    chrome: SYSTEM_CHROME_PATH,
  };
}

function assertReadableFile(p: string, label: string): void {
  let stat: fs.Stats;
  try {
    stat = fs.statSync(p);
  } catch (e) {
    throw new Error(`E2E_OFFLINE_SANDBOX=1 但找不到${label}：${p}（${String(e)}；不會靜默改用無 sandbox 的路徑，直接失敗）`);
  }
  if (!stat.isFile()) {
    throw new Error(`E2E_OFFLINE_SANDBOX=1 但${label}不是一般檔案：${p}`);
  }
  fs.accessSync(p, fs.constants.R_OK); // 不可讀時這裡直接拋出（不吞錯），錯誤訊息由 Node 產生，包含路徑與原因。
}

function assertExecutableFile(p: string, label: string): void {
  let stat: fs.Stats;
  try {
    stat = fs.statSync(p);
  } catch (e) {
    throw new Error(`E2E_OFFLINE_SANDBOX=1 但找不到${label}：${p}（${String(e)}；不會靜默改用無 sandbox 的路徑，直接失敗）`);
  }
  if (!stat.isFile()) {
    throw new Error(`E2E_OFFLINE_SANDBOX=1 但${label}不是一般檔案：${p}`);
  }
  fs.accessSync(p, fs.constants.X_OK); // 不可執行時這裡直接拋出，不吞錯。
}

// validateOfflineSandboxPrereqs：啟動前呼叫，確認 profile（可讀的一般檔案）
// ／wrapper／`sandbox-exec`／系統 Chrome（皆為可執行的一般檔案）四者都
// **在任何 Wails／Chrome 啟動之前**備妥；`browser` 是這次執行實際會用的
// 瀏覽器類型，目前只支援已驗證過的系統 Chrome，其餘組合直接拋錯。`paths`
// 只供 selftest 覆寫成隔離的假路徑，正式呼叫端一律不傳。
export function validateOfflineSandboxPrereqs(
  browser: 'chrome' | 'chromium',
  paths: OfflineSandboxPaths = defaultOfflineSandboxPaths(),
): void {
  if (browser !== 'chrome') {
    throw new Error(
      `E2E_OFFLINE_SANDBOX=1 目前只支援已驗證的系統 Chrome（E2E_BROWSER 未設定或非 'chromium'），`
      + `不支援 E2E_BROWSER=${browser} 這個組合，不嘗試執行（不擴大到跨瀏覽器／跨平台）。`,
    );
  }
  assertReadableFile(paths.profile, 'sandbox profile');
  assertExecutableFile(paths.wrapper, 'Chrome sandbox wrapper');
  assertExecutableFile(paths.sandboxExec, 'sandbox-exec');
  assertExecutableFile(paths.chrome, '系統 Chrome 執行檔');
}
