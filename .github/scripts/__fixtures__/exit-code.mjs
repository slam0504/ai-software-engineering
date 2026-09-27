#!/usr/bin/env node
// 假子程序（僅供 ci-e2e-wrapper.selftest.mjs 使用）：立即以
// process.argv[2]（預設 0）當 exit code 結束，先印一行 stdout／stderr 供
// out-file／ts-out-file 的內容核對。不做任何 I/O 以外的事，不是真正的
// harness。
const code = Number(process.argv[2] ?? '0');
process.stdout.write(`fixture exit-code stdout line code=${code}\n`);
process.stderr.write(`fixture exit-code stderr line code=${code}\n`);
process.exit(code);
