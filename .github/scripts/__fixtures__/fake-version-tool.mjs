#!/usr/bin/env node
// fake-version-tool.mjs — 假版本探測指令（僅供 ci-envelope.selftest.mjs
// 透過 CI_ENVELOPE_GO_CMD_OVERRIDE／CI_ENVELOPE_CHROME_CMD_OVERRIDE 使用）：
// 印出 process.env.FAKE_VERSION_TOOL_OUTPUT（預設一行假版本字串）到
// stdout，並以 process.env.FAKE_VERSION_TOOL_RC（預設 0）結束。不做其他
// I/O，不是真正的 go／Chrome。
const output = process.env.FAKE_VERSION_TOOL_OUTPUT ?? 'fake version tool 1.0.0\n';
process.stdout.write(output.endsWith('\n') ? output : `${output}\n`);
process.exit(Number(process.env.FAKE_VERSION_TOOL_RC ?? '0'));
