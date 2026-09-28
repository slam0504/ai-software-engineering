// fixture-grandchild-holds-pipe.mjs — b3a-ci2-platform-negative 診斷用良性
// Node fixture（新增診斷檔，非 91226366 既有內容；wrapper 的直接子行程）。
//
// 目的：重現原版 ci-e2e-wrapper.mjs 頭註明列的既有限制——「這支 wrapper 只
// 追蹤自己直接 spawn 的那個子行程本身；後代行程樹的清理不歸它管」。這裡讓
// wrapper 的直接子行程（本檔案）自己再 spawn 一個孫行程、用 `stdio:'inherit'`
// 讓孫行程繼承同一組管線 fd（跟 wrapper 相連的 stdout/stderr pipe），然後
// 立刻結束自己——孫行程仍握著那組 pipe 的寫入端，wrapper 這邊的
// `child.on('close', ...)` 因此不會觸發（Node 的 'close' 事件要等所有持有
// 該 pipe 寫入端的行程都關閉），即使 wrapper 自己直接 spawn 的子行程（本檔
// 案）早已經 `exit`。
//
// 明確的自我 deadline：孫行程（fixture-grandchild-holds-pipe.child.mjs）有
// 自己的 4 秒自我結束時限，不依賴任何外部訊號；本檔案自己在 spawn 完孫行程
// 後立刻結束，不佔用行程。
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const childScript = fileURLToPath(new URL('./fixture-grandchild-holds-pipe.child.mjs', import.meta.url));
process.stdout.write(`[immediate-child] pid=${process.pid} started, spawning grandchild that inherits this process's stdio pipes\n`);
const grandchild = spawn(process.execPath, [childScript], {
  stdio: 'inherit',
  detached: true,
});
grandchild.unref();
process.stdout.write(`[immediate-child] pid=${process.pid} spawned grandchild pid=${grandchild.pid}, exiting immediately (grandchild still holds stdio pipe open)\n`);
process.exit(0);
