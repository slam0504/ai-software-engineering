// gateProbe.ts 的正負案例。
//
// `probeFileName` 是唯一完全不依賴 browser 的純函式，直接測。
// `registerProbeListener`／`waitForProbeMatch`／`ProbeHandle.dispose` 依賴
// Playwright 的 `Page.evaluate`——真實瀏覽器行為（`window.runtime` 是否真
// 的可用、debounce 時序）仍待 browser run 驗證；但 `page.evaluate(fn, arg)`
// 本質上就是「用 arg 呼叫 fn」，這裡用一個最小的 fake Page（`evaluate` 直
// 接在目前 Node context 呼叫 fn，搭配一個 fake `window.runtime`）測試
// callback 過濾邏輯（只接受完全相符的 payload）、取消函式的呼叫（不是
// EventsOff）、以及 matchedCount／waitForProbeMatch 的計數與逾時行為——
// 這些是純 TS 邏輯，跟「browser 環境是否真的能執行」無關（review #427
// additional corrections）。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateProbe.selftest.ts
import assert from 'node:assert/strict';
import type { Page } from '@playwright/test';
import { probeAbsolutePath, probeFileName, registerProbeListener, waitForProbeMatch } from './gateProbe.js';
import { specInScope } from './gate2Fixture.js';

let passed = 0;
let failed = 0;
function check(name: string, fn: () => void | Promise<void>): Promise<void> | void {
  const finish = (): void => { passed += 1; console.log(`ok - ${name}`); };
  const fail = (e: unknown): void => { failed += 1; console.error(`FAIL - ${name}`); console.error(e); };
  try {
    const r = fn();
    if (r instanceof Promise) return r.then(finish, fail);
    finish();
  } catch (e) {
    fail(e);
  }
}

/**
 * 最小 fake Page：`evaluate(fn, arg)` 直接在目前 Node context 呼叫
 * `fn(arg)`——這跟真實 Playwright 的序列化／跨 context 執行不完全等價
 * （那部分留給 browser run），但 gateProbe.ts 的 callback 內容本身是純邏
 * 輯（比對 payload、呼叫取消函式），可以在這個 seam 下驗證。
 */
class FakePage {
  async evaluate<Arg, R>(fn: (arg: Arg) => R, arg: Arg): Promise<R> {
    return fn(arg);
  }
}

interface FakeRuntime {
  EventsOnMultiple: (name: string, cb: (payload: unknown) => void, max: number) => () => void;
}

interface FakeWindowState {
  cancelCalls: number;
  lastCallback?: (payload: unknown) => void;
  offCalled: boolean;
}

/** 回傳的是**同一個** mutable state 物件（不是呼叫當下的快照）——呼叫端要在
 * `registerProbeListener` 執行「之後」讀 `state.lastCallback`，那時才會被填入。 */
function installFakeWindow(): FakeWindowState {
  const state: FakeWindowState = { cancelCalls: 0, lastCallback: undefined, offCalled: false };
  const runtime: FakeRuntime & { EventsOff: (name: string) => void } = {
    EventsOnMultiple: (_name: string, cb: (payload: unknown) => void, _max: number) => {
      state.lastCallback = cb;
      return () => { state.cancelCalls += 1; };
    },
    EventsOff: (_name: string) => { state.offCalled = true; },
  };
  (globalThis as unknown as { window?: unknown }).window = { runtime };
  return state;
}

function uninstallFakeWindow(): void {
  delete (globalThis as unknown as { window?: unknown }).window;
}

// ---- probeFileName（純函式，沿用既有覆蓋） ----
check('probeFileName 內含 runId', () => {
  const name = probeFileName('20260924T120000Z-abc123');
  assert.match(name, /20260924T120000Z-abc123/);
});

check('probeFileName 每次呼叫產生不同名稱（nonce 不重複，避免多次探針互相撞名）', () => {
  const a = probeFileName('run1');
  const b = probeFileName('run1');
  assert.notEqual(a, b);
});

check('探針檔相對路徑（spec/<probeFileName>）不落入 SpecScope.Match（不得污染 spec_manifest digest）', () => {
  const name = probeFileName('run1');
  assert.equal(specInScope(`spec/${name}`), false, `spec/${name} 不應被 SpecScope 判定為 in-scope`);
});

// ---- registerProbeListener：callback 過濾邏輯（fake seam） ----
await check('正例：payload 完全相符時 matchedCount 遞增', async () => {
  const state = installFakeWindow();
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    assert.ok(state.lastCallback, 'EventsOnMultiple 應該被呼叫並保存 callback');
    state.lastCallback!('/expected/path');
    assert.equal(await handle.matchedCount(), 1);
  } finally {
    uninstallFakeWindow();
  }
});

await check('負案例：不相關／舊的 payload（路徑不符）不應被計入', async () => {
  const state = installFakeWindow();
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    state.lastCallback!('/some/other/unrelated/path'); // 不相關事件
    state.lastCallback!('/expected/path-but-longer'); // 前綴相符但不是完全相同
    state.lastCallback!('/old/save/event/from/earlier/commit'); // 模擬前一個 commit／save 尚未送達的舊事件
    assert.equal(await handle.matchedCount(), 0, '三筆都不應被計入');
  } finally {
    uninstallFakeWindow();
  }
});

await check('正例：多筆事件中只有相符的那筆被計入，計數不受不相關事件干擾', async () => {
  const state = installFakeWindow();
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    state.lastCallback!('/unrelated/1');
    state.lastCallback!('/expected/path');
    state.lastCallback!('/unrelated/2');
    state.lastCallback!('/expected/path'); // 相符事件觸發兩次也應各自計入
    assert.equal(await handle.matchedCount(), 2);
  } finally {
    uninstallFakeWindow();
  }
});

// ---- review #4（#430）必修缺陷 4：實際收到的 payload 序列必須被保存 ----
await check('正例：allEvents() 保留實際收到的完整 payload 序列（含不符合的事件），依收到順序排列，且各自標記 matched', async () => {
  const state = installFakeWindow();
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    state.lastCallback!('/unrelated/old-event');
    state.lastCallback!('/expected/path');
    state.lastCallback!('/unrelated/another-old-event');
    const events = await handle.allEvents();
    assert.equal(events.length, 3, 'allEvents 應保留全部三筆事件，不是只有相符的那一筆');
    assert.equal(events[0].payload, '/unrelated/old-event');
    assert.equal(events[0].matched, false);
    assert.equal(events[1].payload, '/expected/path');
    assert.equal(events[1].matched, true);
    assert.equal(events[2].payload, '/unrelated/another-old-event');
    assert.equal(events[2].matched, false);
    // 時間順序：後面的事件的 receivedAt 不應早於前面的事件。
    assert.ok(events[1].receivedAt >= events[0].receivedAt);
    assert.ok(events[2].receivedAt >= events[1].receivedAt);
  } finally {
    uninstallFakeWindow();
  }
});

await check('負案例：完全沒有收到任何事件時 allEvents() 回傳空陣列（不是 undefined 或 throw）', async () => {
  const state = installFakeWindow();
  void state;
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    const events = await handle.allEvents();
    assert.deepEqual(events, []);
  } finally {
    uninstallFakeWindow();
  }
});

// ---- dispose：必須呼叫取消函式，不是 EventsOff（review #2 必修缺陷 1 的回歸驗證） ----
await check('dispose 呼叫的是 EventsOnMultiple 回傳的取消函式，且不呼叫 EventsOff', async () => {
  const cancelState = { calls: 0 };
  const runtime = {
    EventsOnMultiple: (_name: string, _cb: (payload: unknown) => void, _max: number) => {
      return () => { cancelState.calls += 1; };
    },
    EventsOff: () => { throw new Error('dispose 不應呼叫 EventsOff（review #2 必修缺陷 1：會清掉 App 自己的 listener）'); },
  };
  (globalThis as unknown as { window?: unknown }).window = { runtime };
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    await handle.dispose();
    assert.equal(cancelState.calls, 1, '取消函式應被呼叫恰好一次');
  } finally {
    uninstallFakeWindow();
  }
});

await check('dispose 兩次呼叫是安全的（第二次是 no-op，不 throw）', async () => {
  const cancelState = { calls: 0 };
  const runtime = {
    EventsOnMultiple: (_n: string, _cb: (p: unknown) => void, _m: number) => () => { cancelState.calls += 1; },
  };
  (globalThis as unknown as { window?: unknown }).window = { runtime };
  try {
    const fakePage = new FakePage() as unknown as Page;
    const handle = await registerProbeListener(fakePage, '/expected/path');
    await handle.dispose();
    await assert.doesNotReject(() => handle.dispose());
    assert.equal(cancelState.calls, 1, '第二次 dispose 不應再呼叫取消函式');
  } finally {
    uninstallFakeWindow();
  }
});

// ---- waitForProbeMatch：計數與逾時行為 ----
await check('正例：matchedCount 已經 >=1 時立即回傳 true（不用等到逾時）', async () => {
  const handle = { matchedCount: async () => 1, allEvents: async () => [], dispose: async () => {} };
  const start = Date.now();
  const result = await waitForProbeMatch(handle, 3000, 50);
  assert.equal(result, true);
  assert.ok(Date.now() - start < 500, '應該立即回傳，不應該等滿逾時');
});

await check('負案例：matchedCount 永遠是 0 時，逾時後回傳 false（不得判定為成功）', async () => {
  const handle = { matchedCount: async () => 0, allEvents: async () => [], dispose: async () => {} };
  const result = await waitForProbeMatch(handle, 300, 50);
  assert.equal(result, false);
});

await check('正例：matchedCount 延遲後才變成 1，仍應在逾時前偵測到', async () => {
  let count = 0;
  setTimeout(() => { count = 1; }, 150);
  const handle = { matchedCount: async () => count, allEvents: async () => [], dispose: async () => {} };
  const result = await waitForProbeMatch(handle, 3000, 50);
  assert.equal(result, true);
});

// ---- probeAbsolutePath：串接 workspaceRoot／spec/ 前綴 ----
check('probeAbsolutePath 落在 <root>/spec/ 底下，檔名含 runId', () => {
  const p = probeAbsolutePath('/tmp/fixture-root', 'run-xyz');
  assert.match(p, /^\/tmp\/fixture-root\/spec\/\.e2e-reconcile-probe-run-xyz-[0-9a-f]+$/);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
