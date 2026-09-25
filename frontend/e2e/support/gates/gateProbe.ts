// B3a-2a：S-N1 的 browser 探針（design v4 §9.1）——唯一依賴 Playwright
// `Page` API 的模組，刻意跟 journalReader.ts（純 fs／JSON）、
// canonicalDigest.ts（純函式）分開，不把 Playwright helper 塞進純 judge。
//
// 本檔本身**不含**離線可測的核心邏輯分支（等待事件、寫探針檔都需要真的
// browser context／檔案系統時序），因此沒有獨立 selftest；`probeFileName`
// 這個純字串生成函式抽出去見下方，供 selftest 覆蓋。
import { randomBytes } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import type { Page } from '@playwright/test';

/**
 * 產生本次探針的唯一檔名（design v4 §9.1 步驟 3c：帶 run-id／nonce，避免
 * 跟前面 commit／save 尚未送達的舊事件混淆）。純函式，供 selftest 覆蓋。
 */
export function probeFileName(runId: string): string {
  const nonce = randomBytes(4).toString('hex');
  return `.e2e-reconcile-probe-${runId}-${nonce}`;
}

/**
 * 一筆實際收到的 `spec:changed` 事件——**review #4（#430）必修缺陷 4**：
 * 舊版只累加一個 counter，完全不保留「實際收到什麼」，S-N1 的中間過程
 * （不符合的事件、時間順序）無法回溯。`receivedAt` 用瀏覽器端
 * `Date.now()`，供事後比對相對順序（跨 Node／browser context 不保證絕對
 * 時鐘同步，只當作序列標記使用）。
 */
export interface ProbeEventRecord {
  payload: unknown;
  matched: boolean;
  receivedAt: number;
}

export interface ProbeHandle {
  /** 讀取目前已收到、且符合本次探針絕對路徑的事件次數。 */
  matchedCount(): Promise<number>;
  /** 讀取目前為止收到的**全部**事件（不論是否符合），依收到順序排列。 */
  allEvents(): Promise<ProbeEventRecord[]>;
  /** 解除 listener（design v4 §9.1 步驟 5：避免影響後續 S-P1 的事件計數）。 */
  dispose(): Promise<void>;
}

/**
 * 在探針檔寫入之前，先於頁面內註冊 `window.runtime.EventsOnMultiple('spec:changed', ...)`
 * listener，只累計 payload 等於 expectedAbsolutePath 的事件（design v4 §9.1
 * 步驟 3a／3d：spec:changed 的 payload 就是觸發 reconcile 那次 debounce 視窗
 * 內最後一個 fsnotify 事件的原始路徑字串，見 app.go:2624,2639）。
 *
 * **review #2 修正（必修缺陷 1）**：dispose 絕對不能呼叫
 * `window.runtime.EventsOff('spec:changed')`——`EventsOff` 會移除該事件名稱
 * 底下**全部**的 listener（`frontend/wailsjs/runtime/runtime.d.ts:51-53`：
 * `EventsOff(eventName, ...additionalEventNames)`，逐一取消整個 handler
 * 清單，不是只取消呼叫端自己那一個），這包括 production 自己掛在同一個
 * 事件名稱上的 listener（`App.vue:353` 的 `refreshEscalation`、
 * `DiagramPane.vue:63` 的 `load`）。若探針 dispose 時清掉這些，App 之後看到
 * spec 變動就不會再重新載入清單／圖表，會讓後續 S-P1 的 UI 觀測失真、也污
 * 染 App 本身行為。改用 `EventsOnMultiple` 呼叫本身**回傳的取消函式**
 * （`runtime.d.ts:45`：`EventsOnMultiple(...): () => void`）——這個函式只
 * 取消這一個 listener，不影響同名事件上的其他 listener。
 */
export async function registerProbeListener(page: Page, expectedAbsolutePath: string): Promise<ProbeHandle> {
  await page.evaluate((expected) => {
    const w = window as unknown as {
      __e2eProbeEvents?: Array<{ payload: unknown; matched: boolean; receivedAt: number }>;
      __e2eProbeCancel?: () => void;
      runtime: { EventsOnMultiple: (name: string, cb: (payload: unknown) => void, max: number) => () => void };
    };
    // review #4（#430）必修缺陷 4：保存**實際收到**的完整事件序列（不論是
    // 否符合），不是只累加一個 counter——不符合的事件、時間順序都要留得
    // 下來，才能在判定失敗或逾時時回溯「到底發生了什麼」。
    w.__e2eProbeEvents = [];
    const cb = (payload: unknown) => {
      w.__e2eProbeEvents!.push({ payload, matched: payload === expected, receivedAt: Date.now() });
    };
    // 保存回傳的取消函式——**不要**改用 EventsOff，見上方函式註解。
    w.__e2eProbeCancel = w.runtime.EventsOnMultiple('spec:changed', cb, -1);
  }, expectedAbsolutePath);

  return {
    matchedCount: async () => {
      const events = await page.evaluate(() => (window as unknown as { __e2eProbeEvents?: Array<{ matched: boolean }> }).__e2eProbeEvents ?? []);
      return events.filter(e => e.matched).length;
    },
    allEvents: () => page.evaluate(() => (window as unknown as { __e2eProbeEvents?: ProbeEventRecord[] }).__e2eProbeEvents ?? []),
    dispose: () => page.evaluate(() => {
      const w = window as unknown as { __e2eProbeCancel?: () => void };
      w.__e2eProbeCancel?.();
      w.__e2eProbeCancel = undefined;
    }),
  };
}

/**
 * 產生本次探針的絕對路徑，但**不寫入**——呼叫端必須先用這個路徑呼叫
 * `registerProbeListener`（listener 要在探針檔寫入之前就註冊好，design v4
 * §9.1 步驟 3a／3c 的順序要求），再呼叫 `writeProbeFileAt` 實際寫入。
 */
export function probeAbsolutePath(workspaceRoot: string, runId: string): string {
  return path.join(workspaceRoot, 'spec', probeFileName(runId));
}

/** 在指定的絕對路徑寫入探針檔內容：不 git add／不 commit，未提交的檔案寫入一樣觸發 fsnotify。 */
export function writeProbeFileAt(absolutePath: string): void {
  fs.mkdirSync(path.dirname(absolutePath), { recursive: true });
  fs.writeFileSync(absolutePath, `e2e S-N1 probe\n`);
}

/**
 * 有界輪詢直到 matchedCount() >= 1 或逾時（design v4 §9.1 步驟 3：預設 3
 * 秒逾時，遠超過 200ms debounce 上界）。逾時回傳 false，呼叫端必須把
 * false 當成失敗，不得判定為「反正沒 stale 就算過」。
 */
export async function waitForProbeMatch(handle: ProbeHandle, timeoutMs = 3000, pollIntervalMs = 100): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if ((await handle.matchedCount()) >= 1) return true;
    await new Promise(r => setTimeout(r, pollIntervalMs));
  }
  return (await handle.matchedCount()) >= 1;
}
