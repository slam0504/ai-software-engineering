// networkLineParser.ts 的純函式測試。不放進 vitest 預設 suite——`vitest.config.ts`
// 明確排除 `e2e/**`（避免 vitest 把 Playwright 專用的 harness／spec 檔當成單元
// 測試撿起來跑，那些檔案用的是 Playwright 自己的 test／expect，混進去會整包炸掉）。
// 這裡改用 Node 原生的 TS 支援（node v22+／實測 v26.8.1）直接執行：
//   node frontend/e2e/support/networkLineParser.selftest.ts
// 不需要額外套件、不動 vitest.config.ts 既有的排除規則。
import assert from 'node:assert/strict';
// 注意：這支腳本用 Node 原生 TS 支援直接執行（不經 Playwright／tsc 的模組
// 解析），所以這裡刻意用 `.ts` 實際副檔名匯入（跟專案其餘檔案慣用的
// `.js`匯入寫法不同，那是給 Playwright 的 esbuild-based loader 用的）。
import { isLoopbackHost, parseNetworkLine } from './networkLineParser.ts';

let passed = 0;
function check(name: string, fn: () => void): void {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    console.error(`FAIL - ${name}`);
    console.error(e);
    process.exitCode = 1;
  }
}

// 案例 1：095243Z 那筆實際的 CLOSED 原始行（N9b 那次觀察到的 sdlc-work *:* CLOSED）。
check('095243Z 實際 CLOSED *:* 原始行 → no-remote，不判違規', () => {
  const line = 'sdlc-work 62396 eason_tseng   51u  IPv4 0x25624855001ad479      0t0  TCP *:* (CLOSED)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'no-remote');
});

// 案例 2：IPv4 loopback 的已連線 line（本機非 loopback、遠端是 loopback）——
// 這是先前「對整行套 regex」會出錯的關鍵案例：整行含兩個位址，只有遠端那個
// 才是該看的。
check('IPv4 loopback 遠端（本機非 loopback）→ connected，不違規', () => {
  const line = 'node 12345 user 21u IPv4 0x1 0t0 TCP 192.168.1.5:53408->127.0.0.1:8080 (ESTABLISHED)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'connected');
  if (r.kind === 'connected') {
    assert.equal(r.remoteHost, '127.0.0.1');
    assert.equal(r.violation, false);
  }
});

// 案例 3：IPv6 loopback 遠端。
check('IPv6 loopback 遠端 [::1] → connected，不違規', () => {
  const line = 'node 12345 user 21u IPv6 0x1 0t0 TCP [::1]:53409->[::1]:8080 (ESTABLISHED)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'connected');
  if (r.kind === 'connected') {
    assert.equal(r.remoteHost, '::1');
    assert.equal(r.violation, false);
  }
});

// 案例 4：外部遠端（本機剛好是 loopback 格式的位址也不能救它——這裡本機故意
// 用 127.0.0.1 開頭，驗證「不能對整行套 regex」這個修正點）。
check('外部遠端（本機端含 127，仍要判違規）→ connected，違規', () => {
  const line = 'node 12345 user 21u IPv4 0x1 0t0 TCP 127.0.0.1:53410->74.125.23.84:443 (ESTABLISHED)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'connected');
  if (r.kind === 'connected') {
    assert.equal(r.remoteHost, '74.125.23.84');
    assert.equal(r.violation, true);
  }
});

// 案例 5：wildcard LISTEN（0.0.0.0／*／:: 都算非 loopback，一律判違規）。
check('wildcard LISTEN（*:5173）→ listen，違規', () => {
  const line = 'node 12345 user 20u IPv4 0x1 0t0 TCP *:5173 (LISTEN)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'listen');
  if (r.kind === 'listen') {
    assert.equal(r.localHost, '*');
    assert.equal(r.violation, true);
  }
});
check('wildcard LISTEN（0.0.0.0:5173）→ listen，違規', () => {
  const line = 'node 12345 user 20u IPv4 0x1 0t0 TCP 0.0.0.0:5173 (LISTEN)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'listen');
  if (r.kind === 'listen') assert.equal(r.violation, true);
});
check('wildcard LISTEN（[::]:5173）→ listen，違規', () => {
  const line = 'node 12345 user 20u IPv6 0x1 0t0 TCP [::]:5173 (LISTEN)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'listen');
  if (r.kind === 'listen') assert.equal(r.violation, true);
});
check('loopback LISTEN（127.0.0.1:5173）→ listen，不違規', () => {
  const line = 'node 12345 user 20u IPv4 0x1 0t0 TCP 127.0.0.1:5173 (LISTEN)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'listen');
  if (r.kind === 'listen') assert.equal(r.violation, false);
});

// 案例 5.5：TCP 狀態帶底線（施工中對 controls 連跑實測撞到的真實行：
// SYN_SENT 被 `[A-Z]+` 誤判成 unparseable，修正成 `[A-Z0-9_]+`）。
check('狀態帶底線（SYN_SENT）→ 仍要能解析成 connected，不能變成 unparseable', () => {
  const line = 'Google 46860 eason_tseng 27u IPv4 0x1 0t0 TCP 192.168.130.37:56432->142.250.157.138:443 (SYN_SENT)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'connected');
  if (r.kind === 'connected') {
    assert.equal(r.remoteHost, '142.250.157.138');
    assert.equal(r.violation, true);
  }
});
check('狀態帶數字（FIN_WAIT_2）→ 仍要能解析', () => {
  const line = 'node 12345 user 21u IPv4 0x1 0t0 TCP 127.0.0.1:53410->127.0.0.1:8080 (FIN_WAIT_2)';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'connected');
});

// 案例 6：未知格式——不能跳過，要保留原文並判為 unparseable（呼叫端要把這個
// 當觀測失敗處理，不是「沒有違規」）。
check('未知格式（沒有 TCP／UDP 欄位）→ unparseable', () => {
  const line = 'this is not a valid lsof line at all';
  const r = parseNetworkLine(line, false);
  assert.equal(r.kind, 'unparseable');
  assert.equal(r.raw, line);
});

// N9a（treatLoopbackAsExternal）：只對 connected／listen 生效，把 loopback
// 也強制判違規；no-remote／unparseable 不受影響（沒有位址可比對，強制不了）。
check('N9a：loopback 已連線在 treatLoopbackAsExternal=true 下也判違規', () => {
  const line = 'node 12345 user 21u IPv4 0x1 0t0 TCP 127.0.0.1:53410->127.0.0.1:8080 (ESTABLISHED)';
  const r = parseNetworkLine(line, true);
  assert.equal(r.kind, 'connected');
  if (r.kind === 'connected') assert.equal(r.violation, true);
});
check('N9a：no-remote 不受 treatLoopbackAsExternal 影響（依然只是診斷）', () => {
  const line = 'sdlc-work 62396 eason_tseng   51u  IPv4 0x25624855001ad479      0t0  TCP *:* (CLOSED)';
  const r = parseNetworkLine(line, true);
  assert.equal(r.kind, 'no-remote');
});

check('isLoopbackHost：涵蓋 localhost／::1／127.x', () => {
  assert.equal(isLoopbackHost('localhost'), true);
  assert.equal(isLoopbackHost('::1'), true);
  assert.equal(isLoopbackHost('127.0.0.1'), true);
  assert.equal(isLoopbackHost('127.5.5.5'), true);
  assert.equal(isLoopbackHost('*'), false);
  assert.equal(isLoopbackHost('0.0.0.0'), false);
  assert.equal(isLoopbackHost('74.125.23.84'), false);
});

console.log(`\n${passed} 項通過`);
if (process.exitCode) {
  console.error('有測項失敗');
} else {
  console.log('全部通過');
}
