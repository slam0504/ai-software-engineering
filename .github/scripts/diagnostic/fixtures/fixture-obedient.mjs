// fixture-obedient.mjs — b3a-ci2-platform-negative 診斷用良性 Node fixture
// （新增診斷檔，非 91226366 既有內容）。
//
// 由 run-b-driver.mjs 自己 spawn 並持有；收到 SIGTERM 立刻正常結束（模擬
// 「守規矩」的受測程序，走原版 ci-e2e-wrapper.mjs 的 deadline→SIGTERM→子行程
// 自行結束這條正常逾時路徑）。明確的自我 deadline：即使從未收到任何訊號，
// 也在 30 秒後自行結束，不會無限期佔用行程（有界 fixture）。
process.stdout.write(`[fixture-obedient] pid=${process.pid} started\n`);
let exited = false;
function selfExit(reason) {
  if (exited) return;
  exited = true;
  process.stdout.write(`[fixture-obedient] pid=${process.pid} exiting: ${reason}\n`);
  process.exit(0);
}
process.on('SIGTERM', () => selfExit('received SIGTERM'));
setTimeout(() => selfExit('self-deadline 30s reached (no signal received)'), 30_000).unref?.();
setInterval(() => {}, 1_000_000);
