#!/usr/bin/env node
// 假子程序（僅供 ci-e2e-wrapper.selftest.mjs 使用）：正常回應 SIGTERM（收到
// 就 exit 0），中間會先跑一段時間（模擬正在工作中）。用來驗證 wrapper 的
// external-signal → interrupted 判定：外部對 wrapper 送 SIGTERM，wrapper
// 轉送給這個 child，child 正常收尾。
const sleepMs = Number(process.argv[2] ?? '10000');
let exiting = false;
process.on('SIGTERM', () => {
  if (exiting) return;
  exiting = true;
  process.stdout.write('fixture: SIGTERM 收到，正常收尾\n');
  process.exit(0);
});
setTimeout(() => {
  if (!exiting) process.exit(0);
}, sleepMs);
