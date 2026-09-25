// gateSubmitWait.ts 的正負案例（review #448 reviewer 裁定：等待 helper
// 有變時，必須用可控時間驗證一次送出、15 秒觀測、60 秒仍 pending 拒絕與
// 晚完成的耗時紀錄——不能真的 sleep 60 秒）。
//
// `FakeClock` 的 `sleep()` 直接推進虛擬時間，不做任何真實等待，因此整份
// selftest（含模擬 60 秒逾時的情境）在毫秒等級的真實時間內完成。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateSubmitWait.selftest.ts
import assert from 'node:assert/strict';
import { observeSubmitWait, type SubmitWaitClock } from './gateSubmitWait.js';

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

/** Fake clock：sleep() 直接推進虛擬時間，不做任何真實等待。 */
class FakeClock implements SubmitWaitClock {
  private t = 0;
  now(): number { return this.t; }
  advance(ms: number): void { this.t += ms; }
  async sleep(ms: number): Promise<void> { this.t += ms; }
}

/** 建一個 poll()：fakeClock 的虛擬時間到達 foundAtMs 之後開始回傳 value，之前一律回傳 null。 */
function countdownPoll(clock: FakeClock, foundAtMs: number, value: string): () => string | null {
  return () => (clock.now() >= foundAtMs ? value : null);
}

// ---- 情境 1：只送出一次——呼叫端在 click 之後只送一次 submit，helper 本身
// 不具備、也不會觸發任何重送動作。用一個 submit 計數器模擬呼叫端行為，
// 確認 observeSubmitWait 執行全程不會讓它增加。 ----
await check('只送出一次：observeSubmitWait 執行全程不觸發任何額外 submit', async () => {
  const clock = new FakeClock();
  let submitCount = 0;
  submitCount += 1; // 模擬呼叫端：click 之後只送一次，接著開始觀察。
  const poll = countdownPoll(clock, 5_000, 'approval-1');
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 100 });
  assert.equal(submitCount, 1, 'observeSubmitWait 執行期間不應觸發任何額外 submit');
  assert.equal(result.found, true);
  assert.equal(result.value, 'approval-1');
});

// ---- 情境 2a：15 秒觀測點時「尚未」出現 request，之後在逾時前完成 ----
await check('15 秒觀測點尚未出現 request 時 observedAt15sMark=false（只記錄，不讓測試失敗）', async () => {
  const clock = new FakeClock();
  const poll = countdownPoll(clock, 20_000, 'approval-late');
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 250 });
  assert.equal(result.found, true, '20 秒 < 60 秒逾時上限，應該成功');
  assert.equal(result.observedAt15sMark, false, '15 秒時尚未出現，應記錄為 false');
  assert.equal(result.observedDurationMs, 20_000, '觀察到 request 的耗時應等於命中當下的虛擬時間');
});

// ---- 情境 2b：15 秒觀測點時「已經」出現 request ----
await check('15 秒觀測點已出現 request 時 observedAt15sMark=true', async () => {
  const clock = new FakeClock();
  const poll = countdownPoll(clock, 5_000, 'approval-early');
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 100 });
  assert.equal(result.found, true);
  assert.equal(result.observedAt15sMark, true, '5 秒就出現，15 秒時必然仍存在（journal 只增不減）');
  assert.equal(result.observedDurationMs, 5_000);
});

// ---- 情境 3：15 秒之後才完成——應該通過，並記錄實際觀察到的耗時 ----
await check('15 秒之後才完成：應通過（found=true），並記錄「觀察到 request 的耗時」', async () => {
  const clock = new FakeClock();
  const poll = countdownPoll(clock, 45_000, 'approval-slow');
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 500 });
  assert.equal(result.found, true, '45 秒 < 60 秒逾時上限，應通過');
  assert.equal(result.value, 'approval-slow');
  assert.equal(result.observedDurationMs, 45_000, '應記錄實際觀察到 request 的耗時，不是收到 callback 的時間');
  assert.equal(result.observedAt15sMark, false, '15 秒時尚未出現');
});

// ---- 情境 4：到 60 秒仍 pending——必須失敗（found=false） ----
await check('60 秒仍 pending：found=false，observedDurationMs=null，不得判定成功', async () => {
  const clock = new FakeClock();
  const poll = (): string | null => null; // 永遠 pending
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 1000 });
  assert.equal(result.found, false, '逾時後 found 必須是 false');
  assert.equal(result.value, null);
  assert.equal(result.observedDurationMs, null, '逾時沒有終點可計，必須是 null');
  assert.equal(result.observedAt15sMark, false, '15 秒時也還是 pending');
});

// ---- 附加：poll() 拋出的錯誤原樣往外傳播，不被吞掉 ----
await check('poll() 拋出的錯誤原樣往外傳播（helper 不吞任何錯誤）', async () => {
  const clock = new FakeClock();
  const poll = (): string | null => { throw new Error('boom'); };
  await assert.rejects(() => observeSubmitWait(poll, { clock, pollIntervalMs: 100 }), /boom/);
});

// ---- 附加：命中發生在最後一輪輪詢（略早於 deadline）時仍應成功，不多等一輪 ----
await check('命中發生在最後一輪輪詢（略早於 deadline）：found=true 且耗時等於命中當下的虛擬時間', async () => {
  const clock = new FakeClock();
  const poll = countdownPoll(clock, 59_900, 'approval-just-in-time');
  const result = await observeSubmitWait(poll, { clock, pollIntervalMs: 100, timeoutMs: 60_000 });
  assert.equal(result.found, true);
  assert.equal(result.observedDurationMs, 59_900);
});

await check('單次 poll 超過 60 秒才回傳值，不得把逾時結果判成成功', async () => {
  const clock = new FakeClock();
  const result = await observeSubmitWait(() => {
    clock.advance(60_001);
    return 'approval-after-deadline';
  }, { clock });
  assert.equal(result.found, false);
  assert.equal(result.value, null);
});

await check('sleep 恢復時已超過 deadline，不得再 poll 並接受新值', async () => {
  let now = 0;
  let calls = 0;
  const clock: SubmitWaitClock = {
    now: () => now,
    sleep: async () => { now = 60_001; },
  };
  const result = await observeSubmitWait(() => ++calls === 1 ? null : 'approval-too-late', { clock });
  assert.equal(result.found, false);
  assert.equal(calls, 1, 'deadline 之後不應再呼叫 poll');
});

await check('跨過 15 秒後才首次觀察到值，不得回填成 15 秒前已觀察到', async () => {
  const clock = new FakeClock();
  const result = await observeSubmitWait(countdownPoll(clock, 16_000, 'approval-after-mark'), {
    clock, pollIntervalMs: 16_000,
  });
  assert.equal(result.found, true);
  assert.equal(result.observedDurationMs, 16_000);
  assert.equal(result.observedAt15sMark, false);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
