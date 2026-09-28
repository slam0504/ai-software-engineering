// fixture-stubborn.mjs — b3a-ci2-platform-negative 診斷用良性 Node fixture
// （新增診斷檔，非 91226366 既有內容）。
//
// 由 run-b-driver.mjs 自己 spawn 並持有；刻意忽略 SIGTERM（no-op handler，
// 模擬「不理會 SIGTERM」的受測程序，用來觀察原版 ci-e2e-wrapper.mjs 的
// grace-timeout（status=timeout-no-clean-exit）路徑——wrapper 本身不送
// SIGKILL，只負責自己在 grace 到期時寫最終 status 並結束）。
// 明確的自我 deadline：無論有沒有收到訊號，最晚在 6 秒後一定自行結束，這是
// 「有界 fixture」的退出證據來源，不依賴任何外部 kill -9。
process.stdout.write(`[fixture-stubborn] pid=${process.pid} started\n`);
process.on('SIGTERM', () => {
  process.stdout.write(`[fixture-stubborn] pid=${process.pid} received SIGTERM, ignoring by design\n`);
});
let exited = false;
function selfExit(reason) {
  if (exited) return;
  exited = true;
  process.stdout.write(`[fixture-stubborn] pid=${process.pid} exiting: ${reason}\n`);
  process.exit(0);
}
setTimeout(() => selfExit('self-deadline 6s reached (bounded fixture, ignores SIGTERM by design)'), 6_000).unref?.();
setInterval(() => {}, 1_000_000);
