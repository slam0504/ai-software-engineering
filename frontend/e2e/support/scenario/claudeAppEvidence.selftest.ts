// B3a-2b-2 F2：App 端證據挑選與三方一致性的離線正負控制。
import assert from 'node:assert/strict';
import { buildClaudeApprovalExpectation, buildClaudeDenyExpectation } from './claudeApprovalProtocol.ts';
import {
  judgeClaudeUiConsistency, parseAuditLines, selectBrokerAuditForApproval, wsidFromMcpConfigPath,
} from './claudeAppEvidence.ts';

let passed = 0;
const failures: string[] = [];
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}

const EXP = buildClaudeApprovalExpectation('f2test');
const ID = '0123456789abcdef0123456789abcdef';
const WSID = 'ws-7';
const CFG = `/w/.workbench/mcp-${WSID}.json`;

const goodTranscript = (): any[] => [
  { seq: 0, dir: 'c2s', frame: { jsonrpc: '2.0', id: 1, method: 'initialize',
    params: { protocolVersion: '2025-06-18' } } },
  { seq: 1, dir: 's2c', frame: { jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-06-18',
    capabilities: { logging: {}, tools: { listChanged: true } }, serverInfo: { name: 'workbench', version: '0.0.1' } } } },
  { seq: 2, dir: 'c2s', frame: { jsonrpc: '2.0', method: 'notifications/initialized' } },
  { seq: 3, dir: 'c2s', frame: { jsonrpc: '2.0', id: 2, method: 'tools/call',
    params: { name: 'approval_prompt', arguments: { tool_name: EXP.toolName, input: EXP.input } } } },
  { seq: 4, dir: 's2c', frame: { jsonrpc: '2.0', id: 2, result: { content: [{ type: 'text',
    text: JSON.stringify({ behavior: 'allow', updatedInput: EXP.input }) }] } } },
];
const goodAudit = (): any[] => [
  { ts: 't1', kind: 'request', data: { id: ID, tool_name: EXP.toolName, input: EXP.input,
    raw_params: { name: 'approval_prompt', arguments: { tool_name: EXP.toolName, input: EXP.input } } } },
  { ts: 't2', kind: 'decision', data: { id: ID, behavior: 'allow', updatedInput: EXP.input } },
];
const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x));
const base = () => ({ transcript: goodTranscript(), brokerAudit: goodAudit(),
  domWsid: WSID, domApprovalId: ID, mcpConfigPath: CFG, exp: EXP });

// --- audit 挑選 ------------------------------------------------------------
check('挑選：只取本次 approval id 的 broker 行，App 其他 audit 行被排除', () => {
  const all = [...goodAudit(), { ts: 't0', kind: 'session_start', data: { wsid: WSID } },
    { ts: 't3', kind: 'request', data: { id: 'other-id' } }];
  const got = selectBrokerAuditForApproval(all, ID);
  assert.equal(got.length, 2);
  assert.deepEqual((got as any[]).map(x => x.kind), ['request', 'decision']);
});
check('挑選：**timeout 行的 data 是裸字串 id**，必須也被選到（否則逾時會被過濾掉）', () => {
  const all = [goodAudit()[0], { ts: 't9', kind: 'timeout', data: ID }, goodAudit()[1]];
  const got = selectBrokerAuditForApproval(all, ID);
  assert.equal(got.length, 3, JSON.stringify(got));
  assert.deepEqual((got as any[]).map(x => x.kind), ['request', 'timeout', 'decision']);
});
check('挑選：空 id 一律回空陣列，不得誤選', () => {
  assert.deepEqual(selectBrokerAuditForApproval(goodAudit(), ''), []);
});
check('解析：無法解析的行被留成診斷，不靜默丟棄', () => {
  const r = parseAuditLines('{"a":1}\nNOT JSON\n\n{"b":2}\n');
  assert.equal(r.records.length, 2);
  assert.deepEqual(r.unparsable, ['NOT JSON']);
});

// --- WSID ------------------------------------------------------------------
check('WSID：自 mcp-<WSID>.json 解析', () => {
  assert.equal(wsidFromMcpConfigPath(CFG), WSID);
  assert.equal(wsidFromMcpConfigPath('/w/.workbench/mcp-a-b-c.json'), 'a-b-c');
});
check('WSID：檔名不符樣式回 null，不硬湊', () => {
  assert.equal(wsidFromMcpConfigPath('/w/.workbench/other.json'), null);
  assert.equal(wsidFromMcpConfigPath('/w/.workbench/mcp-.json'), null);
});

// --- 三方一致 --------------------------------------------------------------
check('正控制：三方一致且協定無違規', () => {
  const r = judgeClaudeUiConsistency(base());
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
  assert.equal(r.agreedApprovalId, ID);
  assert.equal(r.configWsid, WSID);
});
check('反例：DOM approval id 與 broker audit 不符必須被擋', () => {
  const r = judgeClaudeUiConsistency({ ...base(), domApprovalId: 'different-id' });
  assert.ok(r.violations.some(x => x.includes('與 DOM 觀察到的')), JSON.stringify(r.violations));
  assert.equal(r.agreedApprovalId, null);
});
check('反例：DOM 缺 approval id 必須被擋', () => {
  assert.ok(judgeClaudeUiConsistency({ ...base(), domApprovalId: null })
    .violations.some(x => x.includes('未取得 data-test-approval-id')));
});
check('反例：DOM 缺 wsid 必須被擋', () => {
  assert.ok(judgeClaudeUiConsistency({ ...base(), domWsid: null })
    .violations.some(x => x.includes('未取得 data-test-wsid')));
});
check('反例：config 檔名的 WSID 與 DOM 不符必須被擋', () => {
  assert.ok(judgeClaudeUiConsistency({ ...base(), mcpConfigPath: '/w/.workbench/mcp-other.json' })
    .violations.some(x => x.includes('與 DOM 的 data-test-wsid')));
});
check('反例：config 路徑不符 mcp-<WSID>.json 樣式必須被擋', () => {
  assert.ok(judgeClaudeUiConsistency({ ...base(), mcpConfigPath: '/w/.workbench/x.json' })
    .violations.some(x => x.includes('無法自 config 路徑解析 WSID')));
});
check('反例：協定層違規（transcript 被裁成四筆）必須由內部的已審判定擋下', () => {
  const t = clone(goodTranscript()).slice(0, 4);
  assert.ok(judgeClaudeUiConsistency({ ...base(), transcript: t })
    .violations.some(x => x.includes('[mcp]') && x.includes('恰好 5 筆')));
});
check('反例：audit 出現 timeout 行必須被擋（不得當成乾淨的兩列）', () => {
  const a = [goodAudit()[0], { ts: 't9', kind: 'timeout', data: ID }, goodAudit()[1]];
  assert.ok(judgeClaudeUiConsistency({ ...base(), brokerAudit: a })
    .violations.some(x => x.includes('[broker]')));
});
check('反例：broker audit 為空必須被擋，不得因為沒有觀察值就放行', () => {
  const r = judgeClaudeUiConsistency({ ...base(), brokerAudit: [] });
  assert.ok(r.violations.some(x => x.includes('無法建立三方一致')), JSON.stringify(r.violations));
  assert.equal(r.agreedApprovalId, null);
});

// --- deny 案的三方一致（reviewer #403） -------------------------------------
const DENY_EXP = buildClaudeDenyExpectation('f2test');
const denyTranscript = (): any[] => [
  { seq: 0, dir: 'c2s', frame: { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18' } } },
  { seq: 1, dir: 's2c', frame: { jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-06-18',
    capabilities: { logging: {}, tools: { listChanged: true } }, serverInfo: { name: 'workbench', version: '0.0.1' } } } },
  { seq: 2, dir: 'c2s', frame: { jsonrpc: '2.0', method: 'notifications/initialized' } },
  { seq: 3, dir: 'c2s', frame: { jsonrpc: '2.0', id: 2, method: 'tools/call',
    params: { name: 'approval_prompt', arguments: { tool_name: DENY_EXP.toolName, input: DENY_EXP.input } } } },
  { seq: 4, dir: 's2c', frame: { jsonrpc: '2.0', id: 2, result: { content: [{ type: 'text',
    text: JSON.stringify({ behavior: 'deny', message: DENY_EXP.denyReason }) }] } } },
];
const denyAudit = (): any[] => [
  { ts: 't1', kind: 'request', data: { id: ID, tool_name: DENY_EXP.toolName, input: DENY_EXP.input,
    raw_params: { name: 'approval_prompt', arguments: { tool_name: DENY_EXP.toolName, input: DENY_EXP.input } } } },
  { ts: 't2', kind: 'decision', data: { id: ID, behavior: 'deny', message: DENY_EXP.denyReason } },
];
const denyBase = () => ({ transcript: denyTranscript(), brokerAudit: denyAudit(),
  domWsid: WSID, domApprovalId: ID, mcpConfigPath: CFG, exp: DENY_EXP });

check('deny 正控制：三方一致且協定無違規', () => {
  const r = judgeClaudeUiConsistency(denyBase());
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
  assert.equal(r.agreedApprovalId, ID);
});
check('deny 反例：DOM approval id 與 broker audit 不符必須被擋', () => {
  const r = judgeClaudeUiConsistency({ ...denyBase(), domApprovalId: 'different-id' });
  assert.ok(r.violations.some(x => x.includes('與 DOM 觀察到的')), JSON.stringify(r.violations));
  assert.equal(r.agreedApprovalId, null);
});
check('deny 反例：**完全沒有 UI 點擊**（audit 只有 request、沒有 decision）必須被擋', () => {
  const r = judgeClaudeUiConsistency({ ...denyBase(), brokerAudit: [denyAudit()[0]] });
  assert.ok(r.violations.some(x => x.includes('[broker]')), JSON.stringify(r.violations));
});
check('deny 反例：整份 allow 證據配 deny 期望必須被擋', () => {
  const r = judgeClaudeUiConsistency({ ...denyBase(), transcript: goodTranscript(), brokerAudit: goodAudit() });
  assert.ok(r.violations.some(x => x.includes('behavior 應為 "deny"')), JSON.stringify(r.violations));
});
check('allow 案不受影響：allow 正控制仍無違規', () => {
  assert.deepEqual(judgeClaudeUiConsistency(base()).violations, []);
});

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
