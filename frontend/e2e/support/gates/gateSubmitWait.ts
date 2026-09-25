// B3a-2a：Gate2 送核（G2-P1／stale.spec.ts 的 Gate2 前置）等待 helper。
//
// **背景**（review #448 reviewer 裁定）：一次診斷 run（20260925T075938Z-b6d61a，
// artifacts／diag-out checksum 皆通過）量到 Gate2 送核在 E2E 環境下 native
// 端約 13.9 秒，其中 99.2% 是依序執行的 29 次 git wrapper 呼叫；原本 15 秒
// 的功能等待對這個樣本沒有餘裕。reviewer 准將 Gate2 送核（G2-P1 與
// stale.spec.ts 建立 Gate2 前置的相同送核操作）的等待上限調整為 60 秒，
// 這是「保留餘裕的操作上限」，不是由單一樣本得到的可靠最壞時間，也不改
// production 或 15 秒本身的 SLO 定性（S-N1 的 3 秒→15 秒是另一件事，見
// gateProbe.ts 呼叫端）。
//
// **只送一次**：這個 helper 只讀 journal（透過呼叫端傳入的 `poll`），不做
// 任何 UI 動作、不重送請求。呼叫端必須先完成唯一一次 submit（例如
// `submit-gate2` 的 click），緊接著呼叫這個 helper——`observeSubmitWait`
// 內部在被呼叫的當下讀一次時鐘當成 `pollStart`，這就是「click 之後」的
// 時間起點。
//
// **命名警告**：這裡量到的是「本地 journal 出現對應 gate_request 記錄」
// 的時間，讀的是檔案而不是 WebSocket response／callback 的送達時間，不能
// 稱為「收到 callback 的耗時」或「send-to-response latency」——一律稱為
// 「觀察到 request 的耗時」（`observedDurationMs`）。
//
// **時鐘／輪詢可注入**：`SubmitWaitClock` 讓 selftest 用 fake clock 驗證
// 60 秒逾時／15 秒觀測點等情境，不必真的 sleep 60 秒。
import { performance } from 'node:perf_hooks';

export interface SubmitWaitClock {
  now(): number;
  sleep(ms: number): Promise<void>;
}

export const realSubmitWaitClock: SubmitWaitClock = {
  now: () => performance.now(),
  sleep: (ms: number) => new Promise(resolve => setTimeout(resolve, ms)),
};

export interface SubmitWaitObservationResult {
  /** true 表示在逾時（預設 60 秒）前，poll() 回傳了非 null 的值。 */
  found: boolean;
  /** poll() 回傳的非 null 值；found=false 時為 null。 */
  value: string | null;
  /**
   * 「觀察到 request 的耗時」——從 pollStart（呼叫端 click 之後、呼叫這個
   * function 當下）到偵測到非 null 值那次輪詢為止的毫秒數。這是本地
   * journal 出現記錄的時間，不是 callback／send-to-response latency。
   * found=false 時為 null（逾時，沒有終點可計）。
   */
  observedDurationMs: number | null;
  /**
   * 是否在 pollStart + markMs（預設 15000ms）以前已觀察到非 null 值。
   * 若第一次命中已超過此時間，不能反推記錄更早就存在；false 僅代表
   * 未於門檻前觀察到，不代表當時 journal 裡一定沒有該筆記錄。
   * 此欄只記錄，不影響 found，也不是效能驗收條件。
   */
  observedAt15sMark: boolean;
}

export interface SubmitWaitOptions {
  /** 逾時毫秒數，預設 60000（reviewer 裁定：Gate2 送核等待上限 60 秒）。 */
  timeoutMs?: number;
  /** 輪詢間隔毫秒數，預設 200ms。 */
  pollIntervalMs?: number;
  /** 15 秒觀測點的毫秒數，預設 15000（對齊原本的功能等待上限，僅供觀察記錄）。 */
  markMs?: number;
  clock?: SubmitWaitClock;
}

/**
 * 有界輪詢直到 `poll()` 回傳非 null 或逾時（預設 60 秒）。時間起點是呼叫
 * 這個 function 當下（呼叫端必須在唯一一次 submit click 之後立刻呼叫）。
 *
 * 逾時仍是 null 時 `found=false`——呼叫端必須把 `found=false` 當測試失敗；
 * 這個 helper 本身不 throw、不重送請求、不吞任何 poll() 拋出的錯誤（原樣
 * 往外傳播）。
 *
 * journal 是只增不減的 append log（gate.jsonl 的既有性質，見
 * journalReader.ts）：若命中發生在 15 秒觀測點之前，屆時該筆記錄必然仍然
 * 存在，因此 `observedAt15sMark` 在這種情況下可以直接推得 true，不需要
 * 真的再等到 15 秒去重新讀一次。
 */
export async function observeSubmitWait(
  poll: () => string | null,
  options: SubmitWaitOptions = {},
): Promise<SubmitWaitObservationResult> {
  const timeoutMs = options.timeoutMs ?? 60_000;
  const pollIntervalMs = options.pollIntervalMs ?? 200;
  const markMs = options.markMs ?? 15_000;
  const clock = options.clock ?? realSubmitWaitClock;

  const pollStart = clock.now();
  const deadline = pollStart + timeoutMs;
  const markDeadline = pollStart + markMs;

  const timedOut = (): SubmitWaitObservationResult => ({
    found: false, value: null, observedDurationMs: null, observedAt15sMark: false,
  });

  for (;;) {
    // journal reader 為同步函式；它若跨過 deadline 才完成，該次結果仍需拒絕。
    // timeout 是結果接納上限，不宣稱能中斷作業系統中尚未返回的同步讀檔。
    if (clock.now() > deadline) return timedOut();
    const value = poll();
    const nowTs = clock.now();
    if (nowTs > deadline) return timedOut();

    if (value !== null) {
      const observedAt15sMark = nowTs <= markDeadline;
      return { found: true, value, observedDurationMs: nowTs - pollStart, observedAt15sMark };
    }

    if (nowTs >= deadline) {
      return timedOut();
    }

    await clock.sleep(Math.min(pollIntervalMs, deadline - nowTs));
  }
}
