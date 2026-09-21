// B3a-2b-2 F1b：假 Claude CLI 的 selftest。
//
// 兩層範圍，**刻意分清楚**：
//   (a) 純函式（argv／stdin 契約／mcp config／binary 雜湊／wrapper／事件形狀）
//   (b) **runtime 元件測試**：用一個受本檔控制的 synthetic MCP child 實際 spawn，
//       驗 `runMcpRoundTrip` 與 `judgeRoundTripSuccess` 的行為。
//       ⚠️ synthetic child **不是**真 workbench binary；這一層的證據只能算 runtime
//       元件測試，**不構成真子程序（case A/B/C）的驗收**。
//
// 執行（cwd = frontend）：
//   node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs \
//        e2e/support/scenario/fakeClaudeCli.selftest.ts
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { PassThrough } from 'node:stream';
import { buildClaudeApprovalExpectation } from './claudeApprovalProtocol.ts';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import {
  PERMISSION_PROMPT_TOOL,
  SETTINGS_JSON,
  buildAssistantEvent,
  buildInitEvent,
  buildResultEvent,
  expectedConversationArgv,
  judgeRoundTripSuccess,
  parsePsRow,
  probeChildOs,
  readFirstLineBounded,
  readMcpConfigPathFromArgv,
  runMcpRoundTrip,
  validateConversationArgv,
  validateMcpConfig,
  validateUserStreamJson,
  validateVersionArgv,
  verifyCommandBinary,
  withDeadline,
  judgePathAgainstPolicy,
  APP_MCP_CONFIG_BASENAME_RE,
  type McpRoundTrip,
  type OsProbe,
} from './fakeClaudeCli.ts';
import { createScenarioClaudeCli, renderMcpConfig } from './claudeScenarioCli.ts';

let passed = 0;
const failures: string[] = [];
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}
async function acheck(name: string, fn: () => Promise<void>): Promise<void> {
  try { await fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'f1b-selftest-'));
const MCP_CFG = path.join(tmp, 'mcp.json');
const SOCK = path.join(tmp, 'approval.sock');

// ===========================================================================
// (a) 純函式
// ===========================================================================

// --- argv：先用**不經被測函式**的手寫固定序列驗 expectedConversationArgv ----
check('argv：expectedConversationArgv 與手寫固定序列完全相同（含 --verbose／--include-partial-messages／--settings）', () => {
  const FIXED = [
    '-p', '--input-format', 'stream-json', '--output-format', 'stream-json',
    '--verbose', '--include-partial-messages',
    '--settings', '{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}',
    '--permission-prompt-tool', 'mcp__workbench__approval_prompt',
    '--mcp-config', MCP_CFG, '--strict-mcp-config',
  ];
  assert.deepStrictEqual(expectedConversationArgv({ mcpConfigPath: MCP_CFG }), FIXED);
});
check('argv：production 常數與 app.go 實值相同', () => {
  assert.equal(SETTINGS_JSON, '{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}');
  assert.equal(PERMISSION_PROMPT_TOOL, 'mcp__workbench__approval_prompt');
});

const EXPECTED = expectedConversationArgv({ mcpConfigPath: MCP_CFG });

check('正控制：完全相同的 argv 無違規', () => {
  assert.deepEqual(validateConversationArgv([...EXPECTED], EXPECTED), []);
});
check('反例：多一個參數必須被擋', () => {
  const v = validateConversationArgv([...EXPECTED, '--dangerously-skip-permissions'], EXPECTED);
  assert.ok(v.some(x => x.includes('未知參數')), JSON.stringify(v));
  assert.ok(v.some(x => x.includes('長度')), JSON.stringify(v));
});
check('反例：缺一個參數必須被擋', () => {
  assert.ok(validateConversationArgv(EXPECTED.slice(0, -1), EXPECTED).some(x => x.includes('長度')));
});
check('反例：重複參數必須被擋', () => {
  assert.ok(validateConversationArgv([...EXPECTED, '--verbose'], EXPECTED).some(x => x.includes('重複出現')));
});
check('反例：順序不同必須被擋（不得當成集合比較）', () => {
  const a = [...EXPECTED];
  [a[0], a[1]] = [a[1], a[0]];
  assert.ok(validateConversationArgv(a, EXPECTED).some(x => x.includes('argv[0]')));
});
check('反例：本案帶 --resume 必須被擋（fresh start）', () => {
  assert.ok(validateConversationArgv([...EXPECTED, '--resume', 'sess-1'], EXPECTED).some(x => x.includes('不得含 --resume')));
});
check('反例：漏掉 --verbose 這類容易被設計稿遺漏的參數必須被擋', () => {
  assert.ok(validateConversationArgv(EXPECTED.filter(x => x !== '--verbose'), EXPECTED).length > 0);
});
check('--version：恰一參數才通過', () => {
  assert.deepEqual(validateVersionArgv(['--version']), []);
  assert.ok(validateVersionArgv(['--version', '--json']).length > 0);
  assert.ok(validateVersionArgv([]).length > 0);
});

// --- stdin 契約：**不得等 EOF**（reviewer #353 的第 1 點） -------------------
const PROMPT = 'b3a2b2-f1b-prompt';
const goodStdin = JSON.stringify({
  type: 'user',
  message: { role: 'user', content: [{ type: 'text', text: PROMPT }] },
});

await acheck('stdin：完整首行已送但 **stdin 仍開啟**，仍必須讀到並繼續（不得等 EOF）', async () => {
  const s = new PassThrough();
  const p = readFirstLineBounded(s, 2000);
  s.write(`${goodStdin}\n`);
  // **刻意不呼叫 s.end()**——真 App MultiTurn=true 會一直開著 stdin。
  const r = await p;
  assert.equal(r.line, goodStdin);
  assert.ok(!s.writableEnded, 'stream 應仍開啟，證明沒有靠 EOF 才完成');
});
await acheck('stdin：首行之後的殘餘內容要保留（rest）', async () => {
  const s = new PassThrough();
  const p = readFirstLineBounded(s, 2000);
  s.write(`${goodStdin}\nLEFTOVER`);
  const r = await p;
  assert.equal(r.rest, 'LEFTOVER');
});
await acheck('stdin：在收到完整一行之前 EOF 必須失敗（不得當成空行通過）', async () => {
  const s = new PassThrough();
  const p = readFirstLineBounded(s, 2000);
  s.write('{"type":"user"');
  s.end();
  await assert.rejects(p, /之前就結束/);
});
await acheck('stdin：逾時必須失敗並帶出已收內容', async () => {
  const s = new PassThrough();
  const p = readFirstLineBounded(s, 120);
  s.write('partial-no-newline');
  await assert.rejects(p, /逾時/);
});
await acheck('stdin：串流錯誤必須失敗', async () => {
  const s = new PassThrough();
  const p = readFirstLineBounded(s, 2000);
  s.emit('error', new Error('boom'));
  await assert.rejects(p, /讀取錯誤/);
});

check('正控制：合法 user stream-json 無違規', () => {
  assert.deepEqual(validateUserStreamJson(goodStdin, PROMPT), []);
});
check('反例：prompt 與預定不符必須被擋', () => {
  assert.ok(validateUserStreamJson(goodStdin, 'other-prompt').some(x => x.includes('預定 prompt 不符')));
});
check('反例：type 不是 user 必須被擋', () => {
  const s = JSON.stringify({ type: 'assistant', message: { role: 'user', content: [{ type: 'text', text: PROMPT }] } });
  assert.ok(validateUserStreamJson(s, PROMPT).some(x => x.includes('type 應為')));
});
check('反例：stdin 不是合法 JSON 必須被擋', () => {
  assert.ok(validateUserStreamJson('not json', PROMPT).some(x => x.includes('不是合法 JSON')));
});

// --- mcp config ------------------------------------------------------------
const fakeBin = path.join(tmp, 'workbench-stub');
fs.writeFileSync(fakeBin, '#!/bin/sh\nexit 0\n');
fs.chmodSync(fakeBin, 0o755);
const fakeBinReal = fs.realpathSync(fakeBin);
const fakeBinSha = crypto.createHash('sha256').update(fs.readFileSync(fakeBin)).digest('hex');
const EXPECTATION = { commandPath: fakeBinReal, commandSha256: fakeBinSha,
  socket: { kind: 'fixed' as const, path: SOCK } };

check('正控制：renderMcpConfig 產生的 config 能通過驗證（驅動端與 App 格式一致）', () => {
  const { violations, resolved } = validateMcpConfig(renderMcpConfig(fakeBinReal, SOCK), EXPECTATION);
  assert.deepEqual(violations, []);
  assert.ok(resolved !== null);
  assert.equal(resolved?.command, fakeBinReal);
  assert.deepStrictEqual(resolved?.args, ['mcp-approval', '--socket', SOCK]);
});
check('renderMcpConfig 與 app.go startClaude 的字串版面相同（手寫固定字面值對照）', () => {
  assert.equal(
    renderMcpConfig('/tmp/bin/app', '/tmp/s.sock'),
    '{"mcpServers":{"workbench":{"type":"stdio","command":"/tmp/bin/app","args":["mcp-approval","--socket","/tmp/s.sock"]}}}',
  );
});
check('config 的 command 是 symlink 時，須以 canonical path 比對', () => {
  const link = path.join(tmp, 'workbench-link');
  if (!fs.existsSync(link)) fs.symlinkSync(fakeBin, link);
  assert.deepEqual(validateMcpConfig(renderMcpConfig(link, SOCK), EXPECTATION).violations, []);
});
check('反例：command 與核定值不符必須被擋（case B 的判定依據）', () => {
  const other = path.join(tmp, 'other-bin');
  fs.writeFileSync(other, 'x');
  assert.ok(validateMcpConfig(renderMcpConfig(other, SOCK), EXPECTATION).violations
    .some(x => x.includes('canonical path 與核定值不符')));
});
check('反例：command 指到不存在的檔案必須被擋', () => {
  const r = validateMcpConfig(renderMcpConfig(path.join(tmp, 'nope'), SOCK), EXPECTATION);
  assert.ok(r.violations.some(x => x.includes('無法解析為實際檔案')));
  assert.equal(r.resolved, null);
});
check('反例：args 形狀不對必須被擋', () => {
  const raw = `{"mcpServers":{"workbench":{"type":"stdio","command":${JSON.stringify(fakeBinReal)},"args":["mcp-approval","--sock","/tmp/other.sock"]}}}`;
  assert.ok(validateMcpConfig(raw, EXPECTATION).violations.some(x => x.includes('args 應為')));
});
check('反例：socket 值不符固定期望必須被擋（socketPath policy）', () => {
  const raw = renderMcpConfig(fakeBinReal, '/tmp/other.sock');
  assert.ok(validateMcpConfig(raw, EXPECTATION).violations.some(x => x.includes('socket 應為')));
});
check('反例：socket 換成別的 run 的路徑必須被擋（跨執行殘留）', () => {
  const raw = renderMcpConfig(fakeBinReal, path.join(tmp, 'stale-run.sock'));
  assert.ok(validateMcpConfig(raw, EXPECTATION).violations.some(x => x.includes('socket 應為')));
});
// --- F2：動態路徑 policy（互斥聯集 ＋ canonical 判定） ---
const stateDir = path.join(tmp, 'state');
fs.mkdirSync(stateDir, { recursive: true });
const APP_POLICY = { commandPath: fakeBinReal, commandSha256: fakeBinSha,
  socket: { kind: 'appStateDir' as const, stateDir } };

check('F2 policy：socket 在 stateDir 內且符合 approval-<n>.sock → 通過', () => {
  fs.writeFileSync(path.join(stateDir, 'approval-3.sock'), '');
  const r = validateMcpConfig(renderMcpConfig(fakeBinReal, path.join(stateDir, 'approval-3.sock')), APP_POLICY);
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
});
check('F2 policy：**traversal 逃逸必須被擋**（.../.workbench/../../outside.sock）', () => {
  const evil = path.join(stateDir, '..', '..', 'outside.sock');
  const v = validateMcpConfig(renderMcpConfig(fakeBinReal, evil), APP_POLICY).violations;
  assert.ok(v.some(x => x.includes('canonical 父目錄')), `traversal 必須被擋，實際 ${JSON.stringify(v)}`);
});
check('F2 policy：symlink 逃逸必須被擋', () => {
  const linkDir = path.join(tmp, 'link-to-elsewhere');
  const outside = path.join(tmp, 'outside-dir');
  fs.mkdirSync(outside, { recursive: true });
  if (!fs.existsSync(linkDir)) fs.symlinkSync(outside, linkDir);
  const v = validateMcpConfig(renderMcpConfig(fakeBinReal, path.join(linkDir, 'approval-1.sock')), APP_POLICY).violations;
  assert.ok(v.some(x => x.includes('canonical 父目錄')), JSON.stringify(v));
});
check('F2 policy：檔名不符 approval-<n>.sock 必須被擋', () => {
  const v = validateMcpConfig(renderMcpConfig(fakeBinReal, path.join(stateDir, 'whatever.sock')), APP_POLICY).violations;
  assert.ok(v.some(x => x.includes('命名契約')), JSON.stringify(v));
});
check('F2 policy：相對路徑必須被擋', () => {
  const v = validateMcpConfig(renderMcpConfig(fakeBinReal, 'relative/approval-1.sock'), APP_POLICY).violations;
  assert.ok(v.some(x => x.includes('絕對路徑')), JSON.stringify(v));
});
check('F2 policy：fixed policy 仍是全等比對（F1b 契約未放寬）', () => {
  assert.deepEqual(judgePathAgainstPolicy(SOCK, { kind: 'fixed', path: SOCK }, /.*/, 'x'), []);
  assert.ok(judgePathAgainstPolicy('/other', { kind: 'fixed', path: SOCK }, /.*/, 'x').length > 0);
});
check('F2 policy：config 路徑的 appStateDir 判定同樣擋 traversal 與錯檔名', () => {
  const pol = { kind: 'appStateDir' as const, stateDir };
  fs.writeFileSync(path.join(stateDir, 'mcp-ws1.json'), '{}');
  assert.deepEqual(judgePathAgainstPolicy(path.join(stateDir, 'mcp-ws1.json'), pol,
    APP_MCP_CONFIG_BASENAME_RE, 'cfg'), []);
  assert.ok(judgePathAgainstPolicy(path.join(stateDir, '..', 'mcp-ws1.json'), pol,
    APP_MCP_CONFIG_BASENAME_RE, 'cfg').some(x => x.includes('canonical 父目錄')));
  assert.ok(judgePathAgainstPolicy(path.join(stateDir, 'other.json'), pol,
    APP_MCP_CONFIG_BASENAME_RE, 'cfg').some(x => x.includes('命名契約')));
});

check('F2 policy：未知 kind 必須被擋', () => {
  assert.ok(judgePathAgainstPolicy('/x', { kind: 'whatever', path: '/x' } as unknown, /.*/, 'p')
    .some(x => x.includes('未知的 policy kind')));
});
check('F2 policy：缺欄位必須被擋', () => {
  assert.ok(judgePathAgainstPolicy('/x', { kind: 'fixed' } as unknown, /.*/, 'p').some(x => x.includes('path 應為非空字串')));
  assert.ok(judgePathAgainstPolicy('/x', { kind: 'appStateDir' } as unknown, /.*/, 'p').some(x => x.includes('stateDir 應為非空字串')));
});
check('F2 policy：兩種 policy 欄位混用必須被擋', () => {
  assert.ok(judgePathAgainstPolicy('/x', { kind: 'fixed', path: '/x', stateDir: '/y' } as unknown, /.*/, 'p')
    .some(x => x.includes('不得並存')));
});
check('F2 policy：policy 不是物件必須被擋', () => {
  assert.ok(judgePathAgainstPolicy('/x', null as unknown, /.*/, 'p').some(x => x.includes('應為物件')));
});
check('F2 policy：**leaf symlink 逃逸必須被擋**（dirname 合法但 leaf 指到外面）', () => {
  const outside = path.join(tmp, 'outside.json');
  fs.writeFileSync(outside, '{}');
  const leafLink = path.join(stateDir, 'mcp-leaklink.json');
  if (!fs.existsSync(leafLink)) fs.symlinkSync(outside, leafLink);
  const v = judgePathAgainstPolicy(leafLink, { kind: 'appStateDir', stateDir },
    APP_MCP_CONFIG_BASENAME_RE, 'cfg');
  assert.ok(v.some(x => x.includes('symlink')), `leaf symlink 必須被擋，實際 ${JSON.stringify(v)}`);
});
check('F2 policy：leaf 不存在必須被擋，不得當成通過', () => {
  const v = judgePathAgainstPolicy(path.join(stateDir, 'mcp-missing.json'),
    { kind: 'appStateDir', stateDir }, APP_MCP_CONFIG_BASENAME_RE, 'cfg');
  assert.ok(v.some(x => x.includes('不存在')), JSON.stringify(v));
});

// --- F2：config 路徑必須來自 argv ---
check('F2 argv：從 --mcp-config 取得真 App 的 config 路徑', () => {
  const a = expectedConversationArgv({ mcpConfigPath: '/tmp/app/mcp-ws1.json' });
  assert.deepEqual(readMcpConfigPathFromArgv(a), { path: '/tmp/app/mcp-ws1.json', violations: [] });
});
check('F2 argv：缺 --mcp-config 必須被擋', () => {
  assert.ok(readMcpConfigPathFromArgv(['-p', '--verbose']).violations.some(x => x.includes('缺少 --mcp-config')));
});
check('F2 argv：多個 --mcp-config 必須被擋', () => {
  assert.ok(readMcpConfigPathFromArgv(['--mcp-config', '/a.json', '--mcp-config', '/b.json'])
    .violations.some(x => x.includes('多個 --mcp-config')));
});
check('F2 argv：--mcp-config 後面接旗標或空值必須被擋', () => {
  assert.ok(readMcpConfigPathFromArgv(['--mcp-config', '--strict-mcp-config']).violations.some(x => x.includes('不合法')));
  assert.ok(readMcpConfigPathFromArgv(['--mcp-config']).violations.some(x => x.includes('不合法')));
});
check('反例：多一個 mcp server（非唯一 workbench）必須被擋', () => {
  const raw = `{"mcpServers":{"workbench":{"type":"stdio","command":${JSON.stringify(fakeBinReal)},"args":["mcp-approval","--socket",${JSON.stringify(SOCK)}]},"evil":{"type":"stdio","command":"/bin/sh","args":[]}}}`;
  assert.ok(validateMcpConfig(raw, EXPECTATION).violations.some(x => x.includes('唯一一個名為 "workbench"')));
});
check('反例：type 不是 stdio 必須被擋', () => {
  const raw = `{"mcpServers":{"workbench":{"type":"http","command":${JSON.stringify(fakeBinReal)},"args":["mcp-approval","--socket",${JSON.stringify(SOCK)}]}}}`;
  assert.ok(validateMcpConfig(raw, EXPECTATION).violations.some(x => x.includes('type 應為 "stdio"')));
});
check('正控制：binary 雜湊與核定值相符', () => {
  assert.deepEqual(verifyCommandBinary(fakeBinReal, fakeBinSha), []);
});
check('反例：binary 內容被換掉（雜湊不符）必須被擋', () => {
  const tampered = path.join(tmp, 'tampered');
  fs.writeFileSync(tampered, 'different content');
  assert.ok(verifyCommandBinary(tampered, fakeBinSha).some(x => x.includes('SHA256 與核定值不符')));
});
check('反例：binary 讀不到必須被擋，不得當成通過', () => {
  assert.ok(verifyCommandBinary(path.join(tmp, 'missing-bin'), fakeBinSha).some(x => x.includes('無法讀取')));
});

// --- wrapper ---------------------------------------------------------------
check('wrapper：產生在 app.go claudeCLIPathIn 的版面上、可執行、且烤入 run 專屬位置', () => {
  const toolsDir = path.join(tmp, 'tools');
  const cli = createScenarioClaudeCli(toolsDir, 'run-1', '/abs/fakeClaudeCli.ts', {
    mcpConfigPath: MCP_CFG, expectationPath: path.join(tmp, 'exp.json'), evidenceDir: path.join(tmp, 'ev'),
  });
  assert.equal(cli.claudeBin, path.join(toolsDir, 'claude-cli', 'node_modules', '.bin', 'claude'));
  assert.ok((fs.statSync(cli.claudeBin).mode & 0o111) !== 0, 'wrapper 應可執行');
  const body = fs.readFileSync(cli.claudeBin, 'utf8');
  assert.ok(body.includes(`export FAKE_CLAUDE_MCP_CONFIG='${MCP_CFG}'`), body);
  assert.ok(body.includes(`export FAKE_CLAUDE_EVIDENCE_DIR='${path.join(tmp, 'ev')}'`), body);
  assert.ok(body.includes(`export FAKE_CLAUDE_EXPECTATION='${path.join(tmp, 'exp.json')}'`), body);
  assert.ok(body.includes('exec node '), body);
});
check('wrapper：run 專屬值必須烤在腳本內（逐項驗 export 名稱與值，不驗數量）', () => {
  const toolsDir = path.join(tmp, 'tools2');
  const cli = createScenarioClaudeCli(toolsDir, 'run-2', '/abs/x.ts', {
    mcpConfigPath: '/a/b.json', expectationPath: '/a/e.json', evidenceDir: '/a/ev',
  });
  const body = fs.readFileSync(cli.claudeBin, 'utf8');
  const exported = new Map<string, string>();
  for (const line of body.split('\n')) {
    const m = /^export (FAKE_CLAUDE_[A-Z_]+)='(.*)'$/.exec(line);
    if (m !== null) exported.set(m[1], m[2]);
  }
  // 契約：這些名稱與值都必須烤進腳本，**不能靠父 env 繼承**（App childEnv 只有 PATH）。
  const required: Record<string, string> = {
    FAKE_CLAUDE_VERSION: cli.claudeVersion,
    FAKE_CLAUDE_MCP_CONFIG: '/a/b.json',
    FAKE_CLAUDE_EXPECTATION: '/a/e.json',
    FAKE_CLAUDE_EVIDENCE_DIR: '/a/ev',
    FAKE_CLAUDE_RUN_ID: 'run-2',
    FAKE_CLAUDE_ARGV_LOG: cli.argvLog,
  };
  for (const [k, v] of Object.entries(required)) {
    assert.equal(exported.get(k), v, `export ${k} 的值應為 ${JSON.stringify(v)}，實際 ${JSON.stringify(exported.get(k))}`);
  }
  assert.ok(cli.argvLog.startsWith(toolsDir), 'argv 紀錄應落在 toolsDir 內');
  assert.equal(cli.invocationsLog, path.join(toolsDir, 'invocations.log'), '必須寫共用的 invocations.log');
});

check('stream-json：init／assistant／result 事件形狀', () => {
  assert.deepStrictEqual(JSON.parse(buildInitEvent('s1')),
    { type: 'system', subtype: 'init', session_id: 's1' });
  assert.deepStrictEqual(JSON.parse(buildAssistantEvent('s1', 'hello')),
    { type: 'assistant', session_id: 's1', message: { role: 'assistant', content: [{ type: 'text', text: 'hello' }] } });
  assert.deepStrictEqual(JSON.parse(buildResultEvent('s1', 'hello')),
    { type: 'result', subtype: 'success', session_id: 's1', result: 'hello' });
});

// ===========================================================================
// (b) runtime 元件測試：受控 synthetic MCP child
//     ⚠️ 這不是真 workbench binary，只能算 runtime 元件測試。
// ===========================================================================
const EXP = buildClaudeApprovalExpectation('20260921T000000Z-f1btest');
const childPath = path.join(tmp, 'syntheticMcpChild.mjs');
fs.writeFileSync(childPath, `
// 受 selftest 控制的 synthetic MCP child（**不是**真 workbench binary）。
const mode = process.argv[2] ?? 'allow';
const out = o => process.stdout.write(JSON.stringify(o) + '\\n');
if (mode === 'silent') { process.exit(0); }
let buf = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', c => {
  buf += c;
  for (;;) {
    const i = buf.indexOf('\\n');
    if (i < 0) break;
    const line = buf.slice(0, i); buf = buf.slice(i + 1);
    let f; try { f = JSON.parse(line); } catch { continue; }
    if (f.method === 'initialize') {
      if (mode === 'badjson') process.stdout.write('THIS IS NOT JSON\\n');
      out({ jsonrpc: '2.0', id: f.id, result: { protocolVersion: '2025-06-18',
        capabilities: { logging: {}, tools: { listChanged: true } },
        serverInfo: { name: 'workbench', version: '0.0.1' } } });
    } else if (f.method === 'tools/call') {
      const payload = mode === 'deny'
        ? { behavior: 'deny', message: 'synthetic deny' }
        : { behavior: 'allow', updatedInput: f.params.arguments.input };
      out({ jsonrpc: '2.0', id: f.id, result: { content: [{ type: 'text', text: JSON.stringify(payload) }] } });
      if (mode === 'extra') out({ jsonrpc: '2.0', method: 'notifications/message', params: { level: 'info' } });
      if (mode === 'partial') process.stdout.write('{"jsonrpc":"2.0"');
      if (mode === 'exit7') setTimeout(() => process.exit(7), 60);
    }
  }
});
process.stdin.on('end', () => { if (mode !== 'exit7') process.exit(0); });
`);

const childCfg = (mode: string): { raw: string; command: string; args: string[] } =>
  ({ raw: '(synthetic)', command: process.execPath, args: [childPath, mode] });

const TEST_GRACE = { pipeWaitMs: 500, termWaitMs: 2000, killWaitMs: 1000 };
async function round(mode: string, timeoutMs = 4000): Promise<McpRoundTrip> {
  return runMcpRoundTrip(childCfg(mode), EXP,
    { timeoutMs, settleMs: 150, drainMs: 2000, grace: TEST_GRACE });
}

await acheck('runtime：正常 allow 的 synthetic child → 五筆、無傳輸違規、判定通過、子程序乾淨退出', async () => {
  const r = await round('allow');
  assert.equal(r.error, null, `error=${r.error}`);
  assert.equal(r.events.length, 5, JSON.stringify(r.events.map(e => e.dir)));
  assert.deepEqual(r.transportViolations, []);
  assert.deepEqual(judgeRoundTripSuccess(r, EXP), []);
  assert.equal(r.observation.exitCode, 0);
  assert.equal(r.observation.unreaped, false);
  assert.equal(r.observation.psDuring?.state, 'present', '往返期間 ps 應查得到');
  assert.equal(r.observation.psDuring?.parsed?.ppid, process.pid, '子程序 ppid 應為本行程');
  assert.equal(r.observation.psAfter?.state, 'absent', '退出後 ps 應明確查不到');
  assert.equal(r.observation.stdoutDrained, true, 'stdout 應在期限內結束');
});
await acheck('runtime：deny 的 child 不得被當成成功（#353 第 2 點）', async () => {
  const r = await round('deny');
  const v = judgeRoundTripSuccess(r, EXP);
  assert.ok(v.some(x => x.includes('[mcp]') && x.includes('behavior 應為 "allow"')), JSON.stringify(v));
});
await acheck('runtime：child 多送一行未預期 frame 必須進 transcript 並被判失敗（#353 第 3 點）', async () => {
  const r = await round('extra');
  assert.equal(r.events.length, 6, `額外 frame 必須留在 transcript：${JSON.stringify(r.events.map(e => e.dir))}`);
  assert.equal(r.rawStdoutLines.length, 3, JSON.stringify(r.rawStdoutLines));
  const v = judgeRoundTripSuccess(r, EXP);
  assert.ok(v.some(x => x.includes('恰好 5 筆')), JSON.stringify(v));
});
await acheck('runtime：child 送出無法解析的行必須留原文並判失敗', async () => {
  const r = await round('badjson');
  assert.ok(r.rawStdoutLines.some(l => l.includes('THIS IS NOT JSON')), JSON.stringify(r.rawStdoutLines));
  assert.ok(r.transportViolations.some(x => x.includes('無法解析為 JSON')), JSON.stringify(r.transportViolations));
  assert.ok(judgeRoundTripSuccess(r, EXP).some(x => x.startsWith('[transport]')));
});
await acheck('runtime：child 結束時殘留半行必須被記錄並判失敗', async () => {
  const r = await round('partial');
  assert.ok(r.transportViolations.some(x => x.includes('殘留未換行的半行')), JSON.stringify(r.transportViolations));
  assert.ok(judgeRoundTripSuccess(r, EXP).some(x => x.startsWith('[transport]')));
});
await acheck('runtime：child 非零退出必須判失敗', async () => {
  const r = await round('exit7');
  const v = judgeRoundTripSuccess(r, EXP);
  assert.ok(v.some(x => x.includes('exit code 應為 0')), `${JSON.stringify(v)}｜obs=${JSON.stringify(r.observation)}`);
});
await acheck('runtime：child 立刻退出、完全不回覆 → 逾時且診斷保留，不當成成功', async () => {
  const r = await round('silent', 600);
  assert.ok(r.error !== null && r.error.includes('逾時'), `error=${r.error}`);
  assert.ok(judgeRoundTripSuccess(r, EXP).some(x => x.startsWith('[roundtrip]')));
  assert.ok(r.observation.cleanupSteps.length > 0, '清理步驟必須留下');
});
await acheck('runtime：spawn 失敗（command 不存在）必須留診斷、不得擲出', async () => {
  const r = await runMcpRoundTrip(
    { raw: '(synthetic)', command: path.join(tmp, 'definitely-not-here'), args: [] }, EXP,
    { timeoutMs: 600, settleMs: 50, drainMs: 1000, grace: TEST_GRACE });
  const v = judgeRoundTripSuccess(r, EXP);
  assert.ok(r.observation.spawnError !== null || r.error !== null,
    `spawn 失敗必須留下診斷：${JSON.stringify(r.observation)}`);
  assert.ok(v.length > 0, '必須判失敗');
});

// --- OS 三態：probeChildOs 與 parsePsRow -----------------------------------
check('parsePsRow：正常單列可解析', () => {
  assert.deepStrictEqual(parsePsRow(' 123  45  123 Ss  '), { pid: 123, ppid: 45, pgid: 123, stat: 'Ss' });
});
check('parsePsRow：欄位不足或非數字必須回 null，不得硬湊', () => {
  assert.equal(parsePsRow('123 45'), null);
  assert.equal(parsePsRow('abc def ghi S'), null);
});
check('probeChildOs：本行程自己 → present 且 pid 相符', () => {
  const p = probeChildOs(process.pid);
  assert.equal(p.state, 'present', JSON.stringify(p));
  assert.equal(p.parsed?.pid, process.pid);
});
check('probeChildOs：pid 過大（ps 報錯）→ error，**不得**回報成 absent', () => {
  const p = probeChildOs(999999);
  assert.equal(p.state, 'error', JSON.stringify(p));
  assert.ok((p.stderr ?? '').length > 0 || (p.error ?? '').length > 0);
});
await acheck('probeChildOs：已退出的子程序 → absent', async () => {
  const c = spawn('/bin/sh', ['-c', 'exit 0'], { stdio: 'ignore' });
  const pid = c.pid;
  await new Promise<void>(res => { c.once('exit', () => res()); });
  assert.ok(pid !== undefined);
  const p = probeChildOs(pid as number);
  assert.equal(p.state, 'absent', JSON.stringify(p));
});

// --- judgeRoundTripSuccess 的 OS 分支（合成 round 物件精準打點） -------------
const okProbe = (pid: number, ppid: number): OsProbe =>
  ({ state: 'present', raw: `${pid} ${ppid} ${pid} S`, stderr: null, status: 0, error: null,
     parsed: { pid, ppid, pgid: pid, stat: 'S' } });
function syntheticRound(over: Partial<McpRoundTrip['observation']>): McpRoundTrip {
  return {
    events: [], rawStdoutLines: [], transportViolations: [], error: null, childStderr: '',
    observation: {
      selfPid: 1000, observedPid: 123, spawnError: null,
      psDuring: okProbe(123, 1000),
      psAfter: { state: 'absent', raw: null, stderr: null, status: 1, error: null, parsed: null },
      exitCode: 0, exitSignal: null, stdoutDrained: true, stderrDrained: true,
      cleanupSteps: [], unreaped: false, ...over,
    },
  };
}
check('judge：合成正控制在 OS 面向無違規（只剩 transcript 的 [mcp]）', () => {
  const v = judgeRoundTripSuccess(syntheticRound({}), EXP);
  assert.ok(!v.some(x => x.startsWith('[os]')), JSON.stringify(v));
  assert.ok(!v.some(x => x.startsWith('[cleanup]')), JSON.stringify(v));
});
check('judge：**退出後 ps 觀測失敗（EACCES）必須判失敗**——查不成 ≠ 查不到', () => {
  const v = judgeRoundTripSuccess(syntheticRound({
    psAfter: { state: 'error', raw: null, stderr: null, status: null,
      error: 'EACCES: ps execution denied', parsed: null },
  }), EXP);
  assert.ok(v.some(x => x.includes('[os]') && x.includes('再探失敗')), JSON.stringify(v));
});
check('judge：退出後 ps 仍查得到該 pid 必須判失敗（exit event 本身不算 OS 核對）', () => {
  const v = judgeRoundTripSuccess(syntheticRound({ psAfter: okProbe(123, 1000) }), EXP);
  assert.ok(v.some(x => x.includes('退出後 ps 仍查得到')), JSON.stringify(v));
});
check('judge：完全沒做退出後再探必須判失敗', () => {
  assert.ok(judgeRoundTripSuccess(syntheticRound({ psAfter: null }), EXP)
    .some(x => x.includes('退出後沒有做 OS 再探')));
});
check('judge：往返期間觀測失敗必須判失敗，且訊息要說明不得當成不存在', () => {
  const v = judgeRoundTripSuccess(syntheticRound({
    psDuring: { state: 'error', raw: null, stderr: null, status: null,
      error: 'EACCES: ps execution denied', parsed: null },
  }), EXP);
  assert.ok(v.some(x => x.includes('OS 觀測失敗') && x.includes('不得當成程序不存在')), JSON.stringify(v));
});
check('judge：往返期間 ps 查不到（absent）也必須判失敗', () => {
  const v = judgeRoundTripSuccess(syntheticRound({
    psDuring: { state: 'absent', raw: null, stderr: null, status: 1, error: null, parsed: null },
  }), EXP);
  assert.ok(v.some(x => x.includes('往返期間 ps 查不到')), JSON.stringify(v));
});
check('judge：子程序 ppid 不是本行程必須判失敗（不是我們持有的那一個）', () => {
  const v = judgeRoundTripSuccess(syntheticRound({ psDuring: okProbe(123, 999) }), EXP);
  assert.ok(v.some(x => x.includes('不是本行程')), JSON.stringify(v));
});
check('judge：ps 回報的 pid 與 spawn 取得的 pid 不符必須判失敗', () => {
  const v = judgeRoundTripSuccess(syntheticRound({ psDuring: okProbe(456, 1000) }), EXP);
  assert.ok(v.some(x => x.includes('與 spawn 取得的 pid')), JSON.stringify(v));
});
check('judge：stdout 未收齊必須判失敗（不得以「沖了當下 buffer」宣稱完整）', () => {
  assert.ok(judgeRoundTripSuccess(syntheticRound({ stdoutDrained: false }), EXP)
    .some(x => x.includes('無法宣稱已取得完整 raw output')));
});
check('judge：stderr 未收齊必須判失敗', () => {
  assert.ok(judgeRoundTripSuccess(syntheticRound({ stderrDrained: false }), EXP)
    .some(x => x.includes('無法宣稱已取得完整 stderr')));
});
check('judge：unreaped 必須判失敗', () => {
  assert.ok(judgeRoundTripSuccess(syntheticRound({ unreaped: true }), EXP)
    .some(x => x.includes('未能收乾淨')));
});
check('judge：被訊號終止必須判失敗', () => {
  assert.ok(judgeRoundTripSuccess(syntheticRound({ exitCode: null, exitSignal: 'SIGKILL' }), EXP)
    .some(x => x.includes('不應被訊號終止')));
});

// ===========================================================================
// (c) **真正執行 CLI main** 的測試
//     reviewer #355：「註解說有共用 cleanup」「掛了 handler」都不算完成，
//     必須由實際啟動 main 的證據支持。
//     ⚠️ 這裡的 MCP 子程序仍是受控 mock，不是真 workbench binary。
// ===========================================================================
const CLI_PATH = fileURLToPath(new URL('./fakeClaudeCli.ts', import.meta.url));
const cliRoot = path.join(tmp, 'cli');
fs.mkdirSync(cliRoot, { recursive: true });

/** 本檔自己建立的每個子程序的退出證據。 */
const childLedger: Array<Record<string, unknown>> = [];

const mockSrc = path.join(cliRoot, 'mockMcp.mjs');
fs.writeFileSync(mockSrc, `
// 受 selftest 控制的 mock MCP 子程序（**不是**真 workbench binary）。
import fs from 'node:fs';
const mode = process.argv[2];
const pidFile = process.argv[3];
const stageFile = process.argv[4];
if (pidFile) fs.writeFileSync(pidFile, String(process.pid));
// 可辨識的 stderr 輸出：讓測試能斷言 stderr 真的被收下來，而不是只有空檔。
process.stderr.write('MOCK-MCP-STDERR-MARKER pid=' + process.pid + '\\n');
const stage = v => { if (stageFile) fs.writeFileSync(stageFile, v); };
const out = o => process.stdout.write(JSON.stringify(o) + '\\n');
let buf = '';
let keepAlive = null;
process.stdin.setEncoding('utf8');
process.stdin.on('data', c => {
  buf += c;
  for (;;) {
    const i = buf.indexOf('\\n');
    if (i < 0) break;
    const line = buf.slice(0, i); buf = buf.slice(i + 1);
    let f; try { f = JSON.parse(line); } catch { continue; }
    if (f.method === 'initialize') {
      out({ jsonrpc: '2.0', id: f.id, result: { protocolVersion: '2025-06-18',
        capabilities: { logging: {}, tools: { listChanged: true } },
        serverInfo: { name: 'workbench', version: '0.0.1' } } });
      stage('initialized');
    } else if (f.method === 'tools/call') {
      stage('toolcall');
      if (mode === 'stall') {
        // 刻意不回覆、也不因 stdin 關閉而退出——只有真的收到 SIGTERM 才會死。
        keepAlive = setInterval(() => {}, 1000);
        return;
      }
      out({ jsonrpc: '2.0', id: f.id, result: { content: [{ type: 'text',
        text: JSON.stringify({ behavior: 'allow', updatedInput: f.params.arguments.input }) }] } });
    }
  }
});
process.stdin.on('end', () => { if (mode !== 'stall') process.exit(0); });
`);

interface CliRun {
  code: number | null; signal: string | null; stdout: string; stderr: string;
  evidenceDir: string; mockPid: number | null;
}

async function runCli(o: {
  name: string; mode: string;
  /** true＝用 F2 的 appStateDir policy（config/socket 落在模擬的 App stateDir）。 */
  appStateDir?: boolean;
  preCreate?: (evidenceDir: string) => void;
  afterStart?: (cli: ChildProcessWithoutNullStreams, pidOf: () => number | null,
    stageOf: () => string | null) => Promise<void>;
  overallMs?: number;
}): Promise<CliRun> {
  const runDir = path.join(cliRoot, o.name);
  const evidenceDir = path.join(runDir, 'evidence');
  fs.mkdirSync(evidenceDir, { recursive: true });
  const pidFile = path.join(runDir, 'mock.pid');
  const stageFile = path.join(runDir, 'mock.stage');
  const sock = path.join(runDir, 'approval.sock');

  // 「已核定的 binary」＝一個 shell wrapper，exec 成 node 跑 mock（exec 保留同一個 pid）
  const bin = path.join(runDir, 'workbench-mock');
  fs.writeFileSync(bin, `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} ${JSON.stringify(mockSrc)} ${JSON.stringify(o.mode)} ${JSON.stringify(pidFile)} ${JSON.stringify(stageFile)}\n`);
  fs.chmodSync(bin, 0o755);
  const binReal = fs.realpathSync(bin);
  const binSha = crypto.createHash('sha256').update(fs.readFileSync(bin)).digest('hex');

  // F2 模式：模擬真 App 的 stateDir 版面——config 為 <stateDir>/mcp-<WSID>.json、
  // socket 為 <stateDir>/approval-<n>.sock，兩者都必須實際存在且非 symlink。
  let mcpConfigPath: string;
  let sockPath = sock;
  let configPolicy: unknown;
  let socketPolicy: unknown;
  if (o.appStateDir === true) {
    const stateDir = path.join(runDir, '.workbench');
    fs.mkdirSync(stateDir, { recursive: true });
    mcpConfigPath = path.join(stateDir, 'mcp-wsF2.json');
    sockPath = path.join(stateDir, 'approval-1.sock');
    fs.writeFileSync(sockPath, '');  // leaf 必須存在才驗得了身分
    configPolicy = { kind: 'appStateDir', stateDir };
    socketPolicy = { kind: 'appStateDir', stateDir };
  } else {
    mcpConfigPath = path.join(runDir, 'mcp.json');
    configPolicy = { kind: 'fixed', path: mcpConfigPath };
    socketPolicy = { kind: 'fixed', path: sock };
  }
  fs.writeFileSync(mcpConfigPath, renderMcpConfig(binReal, sockPath));
  const expectationPath = path.join(runDir, 'expectation.json');
  fs.writeFileSync(expectationPath, JSON.stringify({
    approval: EXP, prompt: PROMPT,
    mcpConfigPolicy: configPolicy,
    mcp: { commandPath: binReal, commandSha256: binSha, socket: socketPolicy },
    timeoutMs: 4000, stdinTimeoutMs: 4000, settleMs: 120, drainMs: 2000,
    // 縮短的只是**等待長度**，收尾順序（關 pipe → TERM → KILL）完全相同。
    grace: { pipeWaitMs: 400, termWaitMs: 2500, killWaitMs: 1000 },
  }, null, 2));

  o.preCreate?.(evidenceDir);

  const cli = spawn(process.execPath, [CLI_PATH, ...expectedConversationArgv({ mcpConfigPath })], {
    stdio: ['pipe', 'pipe', 'pipe'],
    env: { PATH: process.env.PATH ?? '', FAKE_CLAUDE_EVIDENCE_DIR: evidenceDir,
      FAKE_CLAUDE_EXPECTATION: expectationPath, FAKE_CLAUDE_MCP_CONFIG: mcpConfigPath },
  }) as ChildProcessWithoutNullStreams;
  let stdout = ''; let stderr = '';
  cli.stdout.setEncoding('utf8'); cli.stdout.on('data', (c: string) => { stdout += c; });
  cli.stderr.setEncoding('utf8'); cli.stderr.on('data', (c: string) => { stderr += c; });
  // **stdin 寫入首行後刻意保持開啟**（真 App MultiTurn=true 就是這樣）。
  cli.stdin.write(`${JSON.stringify({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: PROMPT }] } })}\n`);

  const pidOf = (): number | null => {
    try { return Number(fs.readFileSync(pidFile, 'utf8').trim()); } catch { return null; }
  };
  const exited = new Promise<{ code: number | null; signal: string | null }>(res => {
    cli.once('exit', (code, signal) => res({ code, signal }));
  });
  const stageOf = (): string | null => {
    try { return fs.readFileSync(stageFile, 'utf8').trim(); } catch { return null; }
  };
  if (o.afterStart) await o.afterStart(cli, pidOf, stageOf);

  const r = await withDeadline(exited, o.overallMs ?? 15000);
  let code: number | null = null; let signal: string | null = null;
  if (r === null) {
    // 測試自己的有界收尾：不讓 CLI 卡住整個 suite。
    try { cli.kill('SIGKILL'); } catch { /* 已死 */ }
    const r2 = await withDeadline(exited, 3000);
    code = r2?.code ?? null; signal = r2?.signal ?? null;
    childLedger.push({ test: o.name, role: 'cli', outcome: '逾時，由測試 SIGKILL', code, signal });
  } else {
    code = r.code; signal = r.signal;
    childLedger.push({ test: o.name, role: 'cli', code, signal });
  }
  try { cli.stdin.end(); } catch { /* 已關 */ }
  return { code, signal, stdout, stderr, evidenceDir, mockPid: pidOf() };
}

/** 測試自己的外層收尾：確保本檔建立的 mock 不會留成孤兒。 */
async function reapIfAlive(testName: string, pid: number | null): Promise<OsProbe | null> {
  if (pid === null) { childLedger.push({ test: testName, role: 'mock', outcome: '未取得 pid' }); return null; }
  let p = probeChildOs(pid);
  if (p.state === 'present') {
    try { process.kill(pid, 'SIGKILL'); } catch { /* 已死 */ }
    for (let i = 0; i < 40 && p.state === 'present'; i += 1) {
      await new Promise<void>(res => { setTimeout(res, 50); });
      p = probeChildOs(pid);
    }
    childLedger.push({ test: testName, role: 'mock', pid, outcome: `測試外層清理後 state=${p.state}` });
  } else {
    childLedger.push({ test: testName, role: 'mock', pid, outcome: `已自行退出，state=${p.state}` });
  }
  return p;
}

async function waitAbsent(pid: number, ms: number): Promise<OsProbe> {
  const until = Date.now() + ms;
  let p = probeChildOs(pid);
  while (p.state === 'present' && Date.now() < until) {
    await new Promise<void>(res => { setTimeout(res, 50); });
    p = probeChildOs(pid);
  }
  return p;
}

await acheck('CLI main 正控制：stdin 保持開啟也能走完 init → 往返 → 判定 → result，exit 0', async () => {
  let run: CliRun | null = null;
  try {
    run = await runCli({ name: 'ok', mode: 'allow' });
    assert.equal(run.code, 0, `stderr=${run.stderr}｜stdout=${run.stdout}`);
    assert.ok(run.stdout.includes('"subtype":"init"'), run.stdout);
    assert.ok(run.stdout.includes(EXP.completionText), run.stdout);
    assert.ok(run.stdout.includes('"type":"result"'), run.stdout);
    const judged = JSON.parse(fs.readFileSync(path.join(run.evidenceDir, 'judgement.json'), 'utf8')) as { problems: string[] };
    assert.deepEqual(judged.problems, []);
    const obs = JSON.parse(fs.readFileSync(path.join(run.evidenceDir, 'mcp-child.json'), 'utf8')) as Record<string, any>;
    assert.equal(obs.psDuring?.state, 'present');
    assert.equal(obs.psAfter?.state, 'absent');
    assert.equal(obs.stderrDrained, true);
    const errTxt = fs.readFileSync(path.join(run.evidenceDir, 'mcp-child.stderr.txt'), 'utf8');
    assert.ok(errTxt.includes('MOCK-MCP-STDERR-MARKER'), `stderr 應有實際內容，實際 ${JSON.stringify(errTxt)}`);
  } finally {
    await reapIfAlive('ok', run?.mockPid ?? null);
  }
});

/**
 * 中斷路徑：**先確認 mock 已產生可辨識輸出**（stage=toolcall，代表 initialize
 * 回覆已經回來、tools/call 也已送出）才送訊號，這樣中斷當下必然已有 transcript
 * 與 raw stdout 可保存——才驗得出「中斷有沒有切斷證據」。
 */
async function interruptCase(name: string, sig: 'SIGTERM' | 'SIGINT'): Promise<void> {
  let run: CliRun | null = null;
  let mockPid: number | null = null;
  try {
    run = await runCli({
      name, mode: 'stall',
      afterStart: async (cli, pidOf, stageOf) => {
        for (let i = 0; i < 200 && stageOf() !== 'toolcall'; i += 1) {
          await new Promise<void>(res => { setTimeout(res, 25); });
        }
        assert.equal(stageOf(), 'toolcall', 'mock 應已收到 tools/call（已產生可辨識輸出）');
        mockPid = pidOf();
        assert.ok(mockPid !== null, 'mock 應已啟動並寫出 pid');
        assert.equal(probeChildOs(mockPid as number).state, 'present', `${sig} 之前 mock 應活著`);
        cli.kill(sig);
      },
    });
    mockPid = mockPid ?? run.mockPid;

    // (a) 退出與不送成功
    assert.notEqual(run.code, 0, `必須非零退出｜stderr=${run.stderr}`);
    assert.equal(run.code, 23, `應為 EXIT_SIGNALLED=23｜stderr=${run.stderr}`);
    assert.ok(!run.stdout.includes(EXP.completionText), '被訊號中止不得送完成內容');
    assert.ok(!run.stdout.includes('"type":"result"'), '被訊號中止不得送 result');

    // (b) child 確實消失
    const after = await waitAbsent(mockPid as number, 5000);
    assert.equal(after.state, 'absent',
      `${sig} 後 mock 仍在（state=${after.state} raw=${after.raw}）——子程序被丟成孤兒`);

    // (c) **中斷前已取得的證據必須都有落檔，而且不是空檔**
    const ev = run.evidenceDir;
    const tr = JSON.parse(fs.readFileSync(path.join(ev, 'mcp-transcript.json'), 'utf8')) as
      Array<{ seq: number; dir: string; frame: Record<string, any> }>;
    assert.ok(tr.length >= 4,
      `中斷當下應已有 initialize 往返＋initialized＋tools/call 共 4 筆，實際 ${tr.length}`);
    assert.ok(tr.some(e => e.dir === 's2c' && e.frame?.result?.protocolVersion === '2025-06-18'),
      `transcript 應含 mock 回的 initialize response：${JSON.stringify(tr.map(e => e.dir))}`);
    assert.ok(tr.some(e => e.dir === 'c2s' && e.frame?.method === 'tools/call'),
      'transcript 應含已送出的 tools/call');

    const rawOut = fs.readFileSync(path.join(ev, 'mcp-stdout-raw.txt'), 'utf8');
    assert.ok(rawOut.includes('protocolVersion'), `raw stdout 應有實際內容，實際 ${JSON.stringify(rawOut)}`);

    const errTxt = fs.readFileSync(path.join(ev, 'mcp-child.stderr.txt'), 'utf8');
    assert.ok(errTxt.includes('MOCK-MCP-STDERR-MARKER'),
      `child stderr 應有實際內容而非空檔，實際 ${JSON.stringify(errTxt)}`);

    const obs = JSON.parse(fs.readFileSync(path.join(ev, 'mcp-child.json'), 'utf8')) as Record<string, any>;
    assert.equal(obs.psAfter?.state, 'absent', 'child observation 應記錄退出後 ps 查不到');
    assert.equal(obs.unreaped, false);
    assert.ok(Array.isArray(obs.cleanupSteps) && obs.cleanupSteps.length > 0, '應記錄收尾步驟');
    assert.equal(obs.stdoutDrained, true);
    assert.equal(obs.stderrDrained, true);

    const judged = JSON.parse(fs.readFileSync(path.join(ev, 'judgement.json'), 'utf8')) as { problems: string[] };
    assert.ok(judged.problems.length > 0, '被中斷的往返不得判成通過');

    const fail = JSON.parse(fs.readFileSync(path.join(ev, 'failure.json'), 'utf8')) as Record<string, any>;
    assert.ok(String(fail.reason).includes(sig), `failure.json 應記錄 ${sig}`);
    assert.ok(Array.isArray(fail.cleanupSteps) && fail.cleanupSteps.length > 0);
  } finally {
    await reapIfAlive(name, mockPid);
  }
}

await acheck("CLI main：**F2 appStateDir policy 的完整正控制**——動態 config/socket 走完整往返（受控 MCP，非真 App）", async () => {
  let run: CliRun | null = null;
  try {
    run = await runCli({ name: 'f2-appstate', mode: 'allow', appStateDir: true });
    assert.equal(run.code, 0, `stderr=${run.stderr}｜stdout=${run.stdout}`);
    assert.ok(run.stdout.includes(EXP.completionText), run.stdout);
    // 假 CLI 消費的是 argv 帶進來的動態 config，且落在模擬的 stateDir 內
    const cfgSeen = fs.readFileSync(path.join(run.evidenceDir, 'mcp-config.path.txt'), 'utf8').trim();
    assert.ok(cfgSeen.endsWith(`${path.sep}.workbench${path.sep}mcp-wsF2.json`), cfgSeen);
    const judged = JSON.parse(fs.readFileSync(path.join(run.evidenceDir, 'judgement.json'), 'utf8')) as { problems: string[] };
    assert.deepEqual(judged.problems, []);
    const obs = JSON.parse(fs.readFileSync(path.join(run.evidenceDir, 'mcp-child.json'), 'utf8')) as Record<string, any>;
    assert.equal(obs.psDuring?.state, 'present');
    assert.equal(obs.psAfter?.state, 'absent');
  } finally {
    await reapIfAlive('f2-appstate', run?.mockPid ?? null);
  }
});

await acheck('CLI main：**SIGTERM 必須清掉 child 且保存中斷前的全部證據**（#357）', async () => {
  await interruptCase('sigterm', 'SIGTERM');
});
await acheck('CLI main：**SIGINT 必須清掉 child 且保存中斷前的全部證據**（#357）', async () => {
  await interruptCase('sigint', 'SIGINT');
});

await acheck('CLI main：必要證據寫入失敗（judgement.json 為目錄 → EISDIR）必須非零退出且不送 success（#355 第 2 點）', async () => {
  let run: CliRun | null = null;
  try {
    run = await runCli({
      name: 'eisdir', mode: 'allow',
      preCreate: dir => { fs.mkdirSync(path.join(dir, 'judgement.json'), { recursive: true }); },
    });
    assert.notEqual(run.code, 0, `必須非零退出｜stdout=${run.stdout}｜stderr=${run.stderr}`);
    assert.ok(!run.stdout.includes(EXP.completionText), '不得送預定完成內容');
    assert.ok(!run.stdout.includes('"type":"result"'), '不得送 result success');
    assert.ok(run.stderr.length > 0, 'stderr 必須有診斷');
  } finally {
    await reapIfAlive('eisdir', run?.mockPid ?? null);
  }
});

// ledger 寫在**本次 mkdtemp 目錄內**——寫死共用路徑會讓不同人的驗證互相覆寫
// （reviewer #357 實際踩到：獨立重跑蓋掉了上一輪的 ledger）。
const ledgerPath = path.join(tmp, 'test-children.json');
fs.writeFileSync(ledgerPath, `${JSON.stringify(childLedger, null, 2)}\n`);
console.log(`\n測試建立的子程序退出證據：${ledgerPath}`);
console.log(JSON.stringify(childLedger, null, 2));

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
console.log(`tmp dir（保留供檢視）：${tmp}`);
