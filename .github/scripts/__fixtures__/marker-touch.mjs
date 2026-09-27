#!/usr/bin/env node
// marker-touch.mjs — review round 7（#491 G2）：僅供
// gha-runtime-guard.selftest.mjs 使用的假探測指令，用來**證明**它有沒有被
// 呼叫——一旦執行就在 process.env.MARKER_TOUCH_PATH 寫一個檔案，selftest
// 只要斷言這個路徑不存在，就代表 spawn 這支指令的呼叫端（例如
// generate-ci-envelope.mjs 的版本探測）在 spawn 之前就已經被
// gha-runtime-guard.mjs 擋下，不是「這支指令跑了但恰好輸出無害」。除了寫
// marker 之外，也印出一行假版本字串並以 rc0 結束，讓它同時能當
// CI_ENVELOPE_*_CMD_OVERRIDE 的替身用（跟 fake-version-tool.mjs 相容）。
import { writeFileSync } from 'node:fs';

const markerPath = process.env.MARKER_TOUCH_PATH;
if (markerPath) {
  writeFileSync(markerPath, `invoked at ${new Date().toISOString()}\n`);
}
process.stdout.write('marker-touch-fake-version 1.0.0\n');
process.exit(0);
