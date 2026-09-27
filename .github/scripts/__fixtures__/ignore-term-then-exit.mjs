#!/usr/bin/env node
// 假子程序（僅供 ci-e2e-wrapper.selftest.mjs 使用）：忽略第一次 SIGTERM（模擬
// child 對 TERM 沒有反應），之後靠自己的 setTimeout 在有限時間內自行結束
// （避免測試留下真正的孤兒行程）。用來驗證 wrapper 的
// timeout-no-clean-exit 判定：deadline 送 TERM 後，grace 時間內 child 仍
// 存活，wrapper 應該誠實回報 timeout-no-clean-exit，不升級成 SIGKILL。
const selfExitMs = Number(process.argv[2] ?? '1500');
process.on('SIGTERM', () => {
  process.stderr.write('fixture: SIGTERM 收到但忽略\n');
});
setTimeout(() => {
  process.stdout.write('fixture: 自行逾時結束\n');
  process.exit(0);
}, selfExitMs);
