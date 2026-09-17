// browser 層網路判定（reviewer 必修項目，2026-09-15）。
//
// 程序層的網路取樣（networkSampler.ts）只能證明「取樣時點沒看到非 loopback
// 連線」，兩次取樣之間可能漏掉；這裡改在 BrowserContext 這一層直接攔截，
// 在連線真的建立之前就擋下——涵蓋 HTTP(S) fetch／XHR／資源載入與
// WebSocket。只在測試端安裝，不改 production 程式碼。
//
// 要求：
//   - 必須在任何 `page.goto` 之前對整個 BrowserContext 安裝（loopback 以外
//     一律視為違規，記錄原始 URL，讓整次執行失敗——不能只 abort 讓 suite
//     照樣綠燈）。
//   - Service Worker 會繞過 route／routeWebSocket，所以另外在 playwright
//     config 的 `use.serviceWorkers` 設為 'block'（不在這支模組內處理）。
//   - callback 不直接 throw（Playwright 的 route handler 內未捕捉的例外
//     不保證會讓測試失敗、也可能把錯誤吃掉），改成寫檔案＋讓 abort/close
//     照常執行，由 globalTeardown 統一判定。
//
// 缺口 A 修正（reviewer 二次審查，2026-09-15）：reviewer 用獨立 Node＋fake
// context 實測出兩個靜默放行的洞：
//   1. `appendViolation` 的 catch 吞掉寫入錯誤——`requestAborted=true`（連線
//      確實被擋下）但 `violationReported=false`（沒有任何地方看得到這件事
//      發生過），globalTeardown 讀證據檔看到的是「存在但是空的」，會誤判成
//      沒有違規。
//   2. `hasBrowserNetworkViolations` 在證據檔不存在時回傳 false——「這支模組
//      根本沒被呼叫過」跟「呼叫過、也真的沒有違規」變成同一種結果。
// 這兩個洞的共通點是「只信任跨行程的證據檔」：globalTeardown 跑在跟安裝
// guard 的 worker 不同的行程裡（見 F2 相關實測筆記），worker 行程結束後，
// 它自己的記憶體狀態就不存在了，globalTeardown 沒有辦法讀到——所以純粹
// 「補記憶體」救不了 globalTeardown 那一層，要兩層都補：
//   - worker 行程內：違規與寫檔失敗都先進記憶體（保證不會被 catch 吞掉），
//     並且**在同一個 worker 行程內、測試本體結束前**用 `expect` 直接把這兩
//     個記憶體陣列攤開檢查——不必等到 globalTeardown 跨行程判定，測試本體
//     自己就會紅（見 glossary.spec.ts 收尾那幾行）。這是 N14a（寫入失敗）
//     真正被抓到的地方，因為寫入失敗時證據檔會維持「存在但是空的」，單靠
//     globalTeardown 讀檔沒辦法分辨。
//   - globalTeardown 那一層（跨行程，只能看檔案）：證據檔不存在／讀取失敗
//     都改成直接判失敗，不再回傳「沒有違規」。這是 N14b（判定前被刪除）
//     被抓到的地方。
import type { BrowserContext } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const LOOPBACK_HOSTNAMES = new Set(['127.0.0.1', 'localhost', '::1', '[::1]']);

export function isLoopbackUrl(urlStr: string): boolean {
  try {
    const u = new URL(urlStr);
    return LOOPBACK_HOSTNAMES.has(u.hostname);
  } catch {
    return false; // 解析不出來的 URL 一律當非 loopback，寧可誤判違規也不要漏放
  }
}

export function violationsLogPath(artifactsDir: string): string {
  return path.join(artifactsDir, 'browser-network-violations.log');
}

interface GuardMemoryState {
  violations: string[];
  writeFailures: string[];
}

// 只存在於安裝 guard 的這個 worker 行程裡——globalTeardown 是另一個行程，
// 看不到這份記憶體，所以這份狀態的消費者是 glossary.spec.ts 自己
// （`getInMemoryGuardState`），不是 globalTeardown。
const memoryByDir = new Map<string, GuardMemoryState>();
function memState(artifactsDir: string): GuardMemoryState {
  let s = memoryByDir.get(artifactsDir);
  if (!s) {
    s = { violations: [], writeFailures: [] };
    memoryByDir.set(artifactsDir, s);
  }
  return s;
}

export function getInMemoryGuardState(artifactsDir: string): GuardMemoryState {
  return memState(artifactsDir);
}

function appendViolation(artifactsDir: string, kind: string, url: string): void {
  const line = `[${new Date().toISOString()}] ${kind}-VIOLATION: ${url}`;
  memState(artifactsDir).violations.push(line); // 先進記憶體，保證不會被下面的寫檔 catch 吞掉。
  try {
    fs.appendFileSync(violationsLogPath(artifactsDir), `${line}\n`);
  } catch (e) {
    const reason = `寫入 browser-network-violations.log 失敗（${kind} ${url}）：${String(e)}`;
    memState(artifactsDir).writeFailures.push(reason);
    // eslint-disable-next-line no-console
    console.error(`[networkGuard] ${reason}`); // 至少留在 Playwright 自己的輸出／trace 裡，不吞掉。
  }
}

// installNetworkGuard：**必須在 context 建立後、任何 page.goto 之前**呼叫。
export async function installNetworkGuard(context: BrowserContext, artifactsDir: string): Promise<void> {
  // 先確保證據檔存在（就算全程沒有違規，也留一份「本次有安裝」的空檔證據，
  // 跟「這支模組根本沒被呼叫過」區分開來）。建立失敗也記進記憶體的
  // writeFailures，讓 glossary.spec.ts 的收尾檢查抓得到。
  try {
    fs.mkdirSync(artifactsDir, { recursive: true });
    if (!fs.existsSync(violationsLogPath(artifactsDir))) fs.writeFileSync(violationsLogPath(artifactsDir), '');
  } catch (e) {
    memState(artifactsDir).writeFailures.push(`建立 browser-network-violations.log 失敗：${String(e)}`);
  }

  await context.route('**/*', async route => {
    const url = route.request().url();
    if (isLoopbackUrl(url)) {
      await route.continue();
      return;
    }
    appendViolation(artifactsDir, 'HTTP', url);
    await route.abort('blockedbyclient');
  });

  await context.routeWebSocket(() => true, ws => {
    const url = ws.url();
    if (isLoopbackUrl(url)) {
      ws.connectToServer(); // 不設 onMessage，預設雙向透通轉發——loopback 維持真實連線
      return;
    }
    appendViolation(artifactsDir, 'WS', url);
    void ws.close({ code: 4000, reason: 'blocked by e2e network guard (non-loopback)' });
  });
}

export interface BrowserGuardJudgement {
  failed: boolean;
  reason: string;
}

// judgeBrowserNetworkGuard：globalTeardown（跨行程）專用的判定，只能看檔案。
// 證據檔不存在／讀取失敗都直接判失敗——不能把「看不到證據」當成「沒有
// 違規」（N14b 專門驗證這一點）。真正的「寫入失敗但檔案存在且是空的」這種
// 情況（N14a）沒辦法只靠這一層抓到，要靠 worker 行程內的 in-memory 檢查
// （`getInMemoryGuardState`），見 glossary.spec.ts。
export function judgeBrowserNetworkGuard(artifactsDir: string): BrowserGuardJudgement {
  const file = violationsLogPath(artifactsDir);
  if (!fs.existsSync(file)) {
    return {
      failed: true,
      reason: `browser-network-violations.log 不存在（${file}）——無法證明 network guard 有安裝並執行過，判定失敗`,
    };
  }
  let content: string;
  try {
    content = fs.readFileSync(file, 'utf8');
  } catch (e) {
    return { failed: true, reason: `browser-network-violations.log 讀取失敗：${String(e)}` };
  }
  const trimmed = content.trim();
  if (trimmed.length > 0) {
    return { failed: true, reason: `browser-network-violations.log 有內容：\n${trimmed}` };
  }
  return { failed: false, reason: 'browser-network-violations.log 存在且為空，判定沒有違規' };
}
