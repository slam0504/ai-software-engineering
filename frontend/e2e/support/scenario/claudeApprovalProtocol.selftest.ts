// B3a-2b-2 F1a：Claude approval 單一 allow 案的 selftest。
//
// 執行（用現有 Node，**不改 package.json／md5**）：
//   node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs \
//        e2e/support/scenario/claudeApprovalProtocol.selftest.ts
//   （cwd = frontend）
//
// ⚠️ **正例是手寫的 synthetic 資料**，訊息形狀逐項抄自現行 Go 測試與實作
// （mcpserver_test.go:73/75/80、mcpserver.go:41/52/70-76、broker.go:77-79/119-120，
//   go-sdk **v1.7.0**（go.mod 實際解析版本）mcp/protocol.go:1088,2214,305／
//   server.go:615,1985／shared.go:68），
// **不是由被測 builder 自己生成後自證**。反例都是在同一份正例上**逐一改動一處**。
//
// builder 本身另有一組**不經 builder 的固定期望**對照（見「builder 自身」一節）：
// 其餘 fixture 可以沿用 builder 產出的已核定期望值，但不得把同一套推導邏輯
// 複製成正例來自證。
import assert from 'node:assert/strict';
import {
  buildClaudeApprovalExpectation,
  buildClaudeDenyExpectation,
  protocolDecisionFor,
  type ClaudeApprovalExpectation,
} from './claudeApprovalProtocol.ts';
import {
  judgeBrokerAudit,
  validateExpectationDecision,
  judgeClaudeApprovalEvidence,
  judgeMcpBrokerCorrelation,
  judgeMcpToolCallResult,
  judgeMcpTranscript,
} from './claudeApprovalJudge.ts';

let passed = 0;
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); process.exitCode = 1; }
}

const RUN_ID = '20260921T000000Z-f1atest';
const exp: ClaudeApprovalExpectation = buildClaudeApprovalExpectation(RUN_ID);

// --- builder 自身：**不經 builder** 的固定期望對照 --------------------------
// 這組字面值是人工依 claudeApprovalProtocol.ts 的規則展開寫出來的，不呼叫 builder；
// builder 若改壞（例如 marker 少了 runId、id 對調），這條會紅。
check('builder 自身：與手寫固定期望逐欄相符（不經 builder 推導）', () => {
  const MARKER = 'b3a2b2-claude-approval-20260921T000000Z-f1atest';
  const FIXED: ClaudeApprovalExpectation = {
    sessionId: 'b3a2b2-claude-session-20260921T000000Z-f1atest',
    initializeRequestId: 1,
    toolCallRequestId: 2,
    toolName: 'Bash',
    inputMarker: MARKER,
    input: { command: "printf '%s' " + MARKER },
    decision: 'allow',
    completionText: 'b3a2b2-claude-content-20260921T000000Z-f1atest',
  };
  assert.deepStrictEqual(buildClaudeApprovalExpectation('20260921T000000Z-f1atest'), FIXED);
});
check('builder 自身：不同 runId 必須產生不同 marker（不得寫死）', () => {
  const a = buildClaudeApprovalExpectation('runA');
  const b = buildClaudeApprovalExpectation('runB');
  assert.notEqual(a.inputMarker, b.inputMarker);
  assert.notDeepStrictEqual(a.input, b.input);
});

// --- 手寫 synthetic 正例（形狀來源見檔頭） --------------------------------
function goodInitializeResult(): any {
  return {
    protocolVersion: '2025-06-18',
    capabilities: { logging: {}, tools: { listChanged: true } },
    serverInfo: { name: 'workbench', version: '0.0.1' },
  };
}
function goodToolCallResult(): any {
  const payload = { behavior: 'allow', updatedInput: exp.input };
  return { jsonrpc: '2.0', id: exp.toolCallRequestId,
    result: { content: [{ type: 'text', text: JSON.stringify(payload) }] } };
}
/** 完整往返**五筆**：initialize req／resp、notifications/initialized、tools/call req／resp。 */
function goodTranscript(): any[] {
  return [
    { seq: 0, dir: 'c2s', frame: { jsonrpc: '2.0', id: exp.initializeRequestId, method: 'initialize',
      params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'fake-claude', version: '0' } } } },
    { seq: 1, dir: 's2c', frame: { jsonrpc: '2.0', id: exp.initializeRequestId, result: goodInitializeResult() } },
    { seq: 2, dir: 'c2s', frame: { jsonrpc: '2.0', method: 'notifications/initialized' } },
    { seq: 3, dir: 'c2s', frame: { jsonrpc: '2.0', id: exp.toolCallRequestId, method: 'tools/call',
      params: { name: 'approval_prompt', arguments: { tool_name: exp.toolName, input: exp.input } } } },
    { seq: 4, dir: 's2c', frame: goodToolCallResult() },
  ];
}
function goodAudit(brokerId = '01JBROKERULID0000000000000'): any[] {
  return [
    { ts: '2026-09-21T00:00:00.000000000Z', kind: 'request',
      data: { id: brokerId, tool_name: exp.toolName, input: exp.input,
        raw_params: { name: 'approval_prompt', arguments: { tool_name: exp.toolName, input: exp.input } } } },
    { ts: '2026-09-21T00:00:01.000000000Z', kind: 'decision',
      data: { id: brokerId, behavior: 'allow', updatedInput: exp.input } },
  ];
}
const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x));
const toolCallFrame = (t: any[]): any => t[3].frame;

// --- 正控制 ---------------------------------------------------------------
check('正控制：手寫 synthetic 五筆 transcript 無違規', () => {
  assert.deepEqual(judgeMcpTranscript(goodTranscript(), exp), []);
});
check('正控制：tools/call 回覆（allow＋updatedInput）無違規', () => {
  assert.deepEqual(judgeMcpToolCallResult(goodToolCallResult(), exp), []);
});
check('正控制：result.isError=false（明確 false）仍應通過', () => {
  const r = clone(goodToolCallResult()); r.result.isError = false;
  assert.deepEqual(judgeMcpToolCallResult(r, exp), []);
});
check('正控制：broker audit 無違規，且觀察到非空 broker id', () => {
  const r = judgeBrokerAudit(goodAudit(), exp);
  assert.deepEqual(r.violations, []);
  assert.ok(r.observedBrokerId && r.observedBrokerId.length > 0, 'observedBrokerId 應為非空');
});
check('正控制：MCP ↔ broker 關聯無違規', () => {
  assert.deepEqual(judgeMcpBrokerCorrelation(toolCallFrame(goodTranscript()), goodAudit(), exp), []);
});

// --- transcript：第五筆（tools/call response）必須被涵蓋 --------------------
check('反例：缺第五筆（tools/call response 被截斷）必須被擋', () => {
  const t = clone(goodTranscript()).slice(0, 4);
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('恰好 5 筆')));
});
check('反例：第五筆重複（多一筆 response）必須被擋', () => {
  const t = clone(goodTranscript());
  t.push({ seq: 5, dir: 's2c', frame: goodToolCallResult() });
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('恰好 5 筆')));
});
check('反例：第五筆方向錯（c2s）必須被擋', () => {
  const t = clone(goodTranscript()); t[4].dir = 'c2s';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('tools/call response: 方向應為 s2c')));
});
check('反例：第五筆 seq 未遞增必須被擋', () => {
  const t = clone(goodTranscript()); t[4].seq = 3;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('seq 未遞增')));
});
check('反例：transcript 完整判定必須抓到第五筆內容錯（deny）——證明有內部委派', () => {
  const t = clone(goodTranscript());
  t[4].frame.result.content[0].text = JSON.stringify({ behavior: 'deny', message: 'operator denied' });
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('behavior 應為 "allow"')));
});
check('反例：transcript 完整判定必須抓到第五筆 id 錯', () => {
  const t = clone(goodTranscript()); t[4].frame.id = 999;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('tools/call response: id 應為')));
});

// --- transcript：JSON-RPC 三種 frame 形狀互斥 ------------------------------
check('反例：initialize request 帶 result 必須被擋（request/response 形狀互斥）', () => {
  const t = clone(goodTranscript()); t[0].frame.result = goodInitializeResult();
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('request 不得帶 result')));
});
check('反例：tools/call request 帶 error 必須被擋', () => {
  const t = clone(goodTranscript()); t[3].frame.error = { code: -32000, message: 'x' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('request 不得帶 error')));
});
check('反例：notification 帶 error 必須被擋', () => {
  const t = clone(goodTranscript()); t[2].frame.error = { code: -1, message: 'x' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('notification 不得帶 error')));
});
check('反例：notification 帶 result 必須被擋', () => {
  const t = clone(goodTranscript()); t[2].frame.result = {};
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('notification 不得帶 result')));
});
check('反例：notification 帶 id 必須被擋', () => {
  const t = clone(goodTranscript()); t[2].frame.id = 99;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('notification 不得帶 id')));
});
check('反例：initialize response 帶 params 必須被擋', () => {
  const t = clone(goodTranscript()); t[1].frame.params = { protocolVersion: '2025-06-18' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('response 不得帶 params')));
});
check('反例：tools/call response 帶 params 必須被擋', () => {
  const t = clone(goodTranscript()); t[4].frame.params = { name: 'approval_prompt' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('tools/call response: response 不得帶 params')));
});
check('反例：response 帶 error 必須被擋（原 result 仍在也一樣）', () => {
  const t = clone(goodTranscript()); t[1].frame.error = { code: -1, message: 'x' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('response 不得帶 error')));
});

// --- transcript：initialize result 必須是協商成功的物件 ---------------------
check('反例：initialize result=null 必須被擋（不能只看「有 result」）', () => {
  const t = clone(goodTranscript()); t[1].frame.result = null;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('InitializeResult 物件')));
});
check('反例：initialize result 缺 capabilities 必須被擋', () => {
  const t = clone(goodTranscript()); delete t[1].frame.result.capabilities;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('result.capabilities 應為物件')));
});
check('反例：initialize result 的 capabilities 未宣告 tools 必須被擋', () => {
  const t = clone(goodTranscript()); t[1].frame.result.capabilities = { logging: {} };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('capabilities.tools')));
});
check('反例：initialize result 的 serverInfo.name 不是 workbench 必須被擋（接到別的 MCP server）', () => {
  const t = clone(goodTranscript()); t[1].frame.result.serverInfo.name = 'someone-else';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('serverInfo.name')));
});
check('反例：initialize result 缺 serverInfo 必須被擋', () => {
  const t = clone(goodTranscript()); delete t[1].frame.result.serverInfo;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('serverInfo')));
});
check('反例：initialize result 的 protocolVersion 不是 2025-06-18 必須被擋', () => {
  const t = clone(goodTranscript()); t[1].frame.result.protocolVersion = '2024-11-05';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('result.protocolVersion')));
});

// --- transcript：既有反例（沿用） ------------------------------------------
check('反例：initialize 的 protocolVersion 不是 2025-06-18 必須被擋', () => {
  const t = clone(goodTranscript()); t[0].frame.params.protocolVersion = '2024-01-01';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('params.protocolVersion')));
});
check('反例：用 method="initialized"（而非 notifications/initialized）必須被擋', () => {
  const t = clone(goodTranscript()); t[2].frame.method = 'initialized';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('notifications/initialized')));
});
check('反例：initialize response 的 id 與 request 不符必須被擋', () => {
  const t = clone(goodTranscript()); t[1].frame.id = 999;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('id 應與 request 相同')));
});
check('反例：id 型別錯（字串 "1" 對數字 1）必須被擋，不寬鬆轉型', () => {
  const t = clone(goodTranscript()); t[0].frame.id = '1';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('id 應為')));
});
check('反例：tools/call 的 name 不是 approval_prompt 必須被擋', () => {
  const t = clone(goodTranscript()); t[3].frame.params.name = 'evil_prompt';
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('params.name')));
});
check('反例：arguments.input 與獨立期望不符必須被擋', () => {
  const t = clone(goodTranscript()); t[3].frame.params.arguments.input = { command: 'rm -rf /' };
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('arguments.input')));
});
check('反例：seq 錯序必須被擋（不得排序掩蓋）', () => {
  const t = clone(goodTranscript()); const tmp = t[1]; t[1] = t[2]; t[2] = tmp;
  assert.ok(judgeMcpTranscript(t, exp).some(x => x.includes('seq 未遞增')));
});

// --- tools/call 回覆反例 ---------------------------------------------------
check('反例：behavior=deny 不得讓 allow 案通過（合法 deny 也算失敗）', () => {
  const r = clone(goodToolCallResult());
  r.result.content[0].text = JSON.stringify({ behavior: 'deny', message: 'operator denied' });
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('behavior 應為 "allow"')));
});
check('反例：allow 缺 updatedInput 必須被擋', () => {
  const r = clone(goodToolCallResult());
  r.result.content[0].text = JSON.stringify({ behavior: 'allow' });
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('必須帶 updatedInput')));
});
check('反例：allow 的 updatedInput 與原 input 不同必須被擋', () => {
  const r = clone(goodToolCallResult());
  r.result.content[0].text = JSON.stringify({ behavior: 'allow', updatedInput: { command: 'other' } });
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('updatedInput 應等於原 input')));
});
check('反例：text payload 洩漏 broker id 必須被擋', () => {
  const r = clone(goodToolCallResult());
  r.result.content[0].text = JSON.stringify({ behavior: 'allow', updatedInput: exp.input, id: '01JLEAKED' });
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('不得含 id')));
});
check('反例：result.isError=true 必須被擋', () => {
  const r = clone(goodToolCallResult()); r.result.isError = true;
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('isError')));
});
check('反例：result.isError="true"（字串）必須被擋，不做 truthy 寬鬆判斷', () => {
  const r = clone(goodToolCallResult()); r.result.isError = 'true';
  assert.ok(judgeMcpToolCallResult(r, exp).some(x => x.includes('isError')));
});
check('反例：回覆是裸 Decision（非 content/text 包裝）必須被擋', () => {
  const r: any = { jsonrpc: '2.0', id: exp.toolCallRequestId, result: { behavior: 'allow', updatedInput: exp.input } };
  assert.ok(judgeMcpToolCallResult(r, exp).length > 0);
});

// --- broker audit 反例 -----------------------------------------------------
check('反例：audit 只有 request（decision 缺失）必須被擋', () => {
  const a = clone(goodAudit()).slice(0, 1);
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('[request, decision]')));
});
check('反例：audit 出現 timeout 事件必須被擋', () => {
  const a = clone(goodAudit()); a.splice(1, 0, { ts: 't', kind: 'timeout', data: 'id' });
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('[request, decision]')));
});
check('反例：request.id 與 decision.id 不同必須被擋（關聯斷裂）', () => {
  const a = clone(goodAudit()); a[1].data.id = '01JDIFFERENT00000000000000';
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('無法建立 request↔decision 關聯')));
});
check('反例：broker id 為空字串必須被擋', () => {
  const a = clone(goodAudit()); a[0].data.id = ''; a[1].data.id = '';
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('非空字串')));
});
check('反例：audit decision 是 fail-closed deny 必須被擋', () => {
  const a = clone(goodAudit());
  a[1].data = { id: a[0].data.id, behavior: 'deny', message: 'approval broker unavailable (fail closed)' };
  const v = judgeBrokerAudit(a, exp).violations;
  assert.ok(v.some(x => x.includes('behavior 應為 "allow"')) || v.some(x => x.includes('fail-closed')));
});
check('反例：audit request.raw_params 缺 name 必須被擋', () => {
  const a = clone(goodAudit()); delete a[0].data.raw_params.name;
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('raw_params.name')));
});

// --- broker audit：raw_params.arguments 的**內容**必須被檢查 ----------------
// 這組直接對應 reviewer #349 的反例：外層 tool_name/input 維持正確，只把內層
// raw_params.arguments 整包換掉——舊版只驗「是物件」因此全綠。
check('反例：raw_params.arguments 被整包掉包（外層正確）必須被擋——對獨立期望', () => {
  const a = clone(goodAudit());
  a[0].data.raw_params.arguments = { tool_name: 'Other', input: { command: 'other' } };
  const v = judgeBrokerAudit(a, exp).violations;
  assert.ok(v.some(x => x.includes('raw_params.arguments.tool_name 與獨立期望不符')), JSON.stringify(v));
  assert.ok(v.some(x => x.includes('raw_params.arguments.input 與獨立期望不符')), JSON.stringify(v));
});
check('反例：raw_params.arguments 被整包掉包必須被擋——內外矛盾', () => {
  const a = clone(goodAudit());
  a[0].data.raw_params.arguments = { tool_name: 'Other', input: { command: 'other' } };
  const v = judgeBrokerAudit(a, exp).violations;
  assert.ok(v.some(x => x.includes('與外層 request.tool_name') && x.includes('矛盾')), JSON.stringify(v));
  assert.ok(v.some(x => x.includes('與外層 request.input') && x.includes('矛盾')), JSON.stringify(v));
});
check('反例：raw_params.arguments 缺 input 必須被擋', () => {
  const a = clone(goodAudit()); delete a[0].data.raw_params.arguments.input;
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('raw_params.arguments 缺 input')));
});
check('反例：raw_params.arguments 缺 tool_name 必須被擋', () => {
  const a = clone(goodAudit()); delete a[0].data.raw_params.arguments.tool_name;
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('raw_params.arguments.tool_name')));
});
check('反例：raw_params.arguments 不是物件必須被擋', () => {
  const a = clone(goodAudit()); a[0].data.raw_params.arguments = 'not-an-object';
  assert.ok(judgeBrokerAudit(a, exp).violations.some(x => x.includes('raw_params.arguments 應為含 tool_name/input 的物件')));
});

// --- 關聯反例 --------------------------------------------------------------
check('反例：MCP input 與 broker input 不同必須被擋（不是同一筆 approval）', () => {
  const a = clone(goodAudit()); a[0].data.input = { command: 'different' };
  assert.ok(judgeMcpBrokerCorrelation(toolCallFrame(goodTranscript()), a, exp).some(x => x.includes('無法確認是同一筆')));
});
check('反例：關聯核對也必須抓到 raw_params.arguments 被掉包', () => {
  const a = clone(goodAudit());
  a[0].data.raw_params.arguments = { tool_name: 'Other', input: { command: 'other' } };
  const v = judgeMcpBrokerCorrelation(toolCallFrame(goodTranscript()), a, exp);
  assert.ok(v.some(x => x.includes('raw_params.arguments.tool_name')), JSON.stringify(v));
  assert.ok(v.some(x => x.includes('raw_params.arguments.input')), JSON.stringify(v));
});
check('反例：audit 第一列不是 request 事件時，關聯核對必須判失敗而非略過', () => {
  const a = clone(goodAudit()); a[0].kind = 'decision';
  assert.ok(judgeMcpBrokerCorrelation(toolCallFrame(goodTranscript()), a, exp).some(x => x.includes('前提是 audit 第一列為 request')));
});
check('反例：input 未帶本次 runId 標記必須被擋（跨執行殘留）', () => {
  const other = buildClaudeApprovalExpectation('20260921T999999Z-other');
  const t = clone(goodTranscript()); t[3].frame.params.arguments.input = other.input;
  const a = clone(goodAudit());
  a[0].data.input = other.input;
  a[0].data.raw_params.arguments.input = other.input;
  assert.ok(judgeMcpBrokerCorrelation(toolCallFrame(t), a, exp).some(x => x.includes('runId 標記')));
});
check('結構比較用 deep equality：key 順序不同不得被當成不符', () => {
  const reordered = { alpha: 1, beta: 2, command: exp.input.command };
  const shuffled = { command: exp.input.command, beta: 2, alpha: 1 };
  const t = clone(goodTranscript()); t[3].frame.params.arguments.input = reordered;
  const a = clone(goodAudit());
  a[0].data.input = shuffled;
  a[0].data.raw_params.arguments.input = shuffled;
  // marker 仍在 command 內，因此唯一可能的違規只會來自結構比較；deep equality 下應為空。
  assert.deepEqual(judgeMcpBrokerCorrelation(toolCallFrame(t), a, exp), []);
});

// --- 完整入口：必須真的同時呼叫三項判定 ------------------------------------
check('正控制：完整入口對正例無違規，且回傳觀察到的 broker id', () => {
  const r = judgeClaudeApprovalEvidence(goodTranscript(), goodAudit(), exp);
  assert.deepEqual(r.violations, []);
  assert.ok(r.observedBrokerId && r.observedBrokerId.length > 0);
});
check('完整入口：必須抓到只存在於 MCP 層的缺陷', () => {
  const t = clone(goodTranscript()); t[1].frame.params = { protocolVersion: '2025-06-18' };
  const v = judgeClaudeApprovalEvidence(t, goodAudit(), exp).violations;
  assert.ok(v.length > 0 && v.every(x => x.startsWith('[mcp]')), JSON.stringify(v));
});
check('完整入口：必須抓到只存在於 broker 層的缺陷', () => {
  const a = clone(goodAudit()); a[1].data.behavior = 'deny';
  const v = judgeClaudeApprovalEvidence(goodTranscript(), a, exp).violations;
  assert.ok(v.length > 0 && v.every(x => x.startsWith('[broker]')), JSON.stringify(v));
});
check('完整入口：必須抓到跨層關聯缺陷（raw_params 掉包同時打到 broker 與 correlation）', () => {
  const a = clone(goodAudit());
  a[0].data.raw_params.arguments = { tool_name: 'Other', input: { command: 'other' } };
  const v = judgeClaudeApprovalEvidence(goodTranscript(), a, exp).violations;
  assert.ok(v.some(x => x.startsWith('[broker]')), JSON.stringify(v));
  assert.ok(v.some(x => x.startsWith('[correlation]')), JSON.stringify(v));
  assert.equal(judgeClaudeApprovalEvidence(goodTranscript(), a, exp).observedBrokerId, null);
});
check('完整入口：transcript 被預先裁切成四筆時必須失敗（caller 不得自行裁掉 response）', () => {
  const t = clone(goodTranscript()).slice(0, 4);
  const v = judgeClaudeApprovalEvidence(t, goodAudit(), exp).violations;
  assert.ok(v.some(x => x.includes('恰好 5 筆')), JSON.stringify(v));
});

// ---------------------------------------------------------------------------
// B3a-2b-2 deny 案（reviewer #403 核定）：**使用者明確拒絕**
//
// production 事實（一手讀碼，2026-09-22 覆核）：
//   ApprovalDialog.vue:16,55-62,90-93  reason 預設 **空字串**，由既有 input
//                                      v-model 綁定；decide(false) 把它原樣送進
//                                      ResolveApproval(id, false, reason)
//   app.go:6908-6916                   allow=false → behavior/decision = "deny"，
//                                      reason 原樣成為 Decision.Message
//   broker.go:20-25,118-120            Message 與 UpdatedInput 皆 omitempty；
//                                      **只有 allow 才補 UpdatedInput**
//   mcpserver.go:70-76                 回覆 {behavior,message?,updatedInput?}
//
// 因此「deny 一定有 message」**不是** production 契約——空理由的 deny 完全合法。
// 本 scenario 自己固定一個 run 專屬理由，判定才有辦法逐字認出「使用者這次按的
// 那個 deny」，並與 fail-closed 的自動 deny 分開。下面的空／錯 reason 反例
// **只對這個非空理由的案例成立**，不代表一般空理由 deny 非法。
const denyExp: ClaudeApprovalExpectation = buildClaudeDenyExpectation(RUN_ID);

check('deny builder：與手寫固定期望逐欄相符（不經 builder 推導）', () => {
  const M = 'b3a2b2-claude-deny-20260921T000000Z-f1atest';
  const FIXED: ClaudeApprovalExpectation = {
    sessionId: 'b3a2b2-claude-session-20260921T000000Z-f1atest',
    initializeRequestId: 1,
    toolCallRequestId: 2,
    toolName: 'Bash',
    inputMarker: M,
    input: { command: "printf '%s' " + M },
    decision: 'deny',
    denyReason: 'b3a2b2-claude-deny-reason-20260921T000000Z-f1atest',
    completionText: 'b3a2b2-claude-denied-content-20260921T000000Z-f1atest',
  };
  assert.deepStrictEqual(buildClaudeDenyExpectation(RUN_ID), FIXED);
});
check('deny builder：與 allow builder 的 marker／input／完成內容全部不同（不得互相冒充）', () => {
  assert.notEqual(denyExp.inputMarker, exp.inputMarker);
  assert.notDeepStrictEqual(denyExp.input, exp.input);
  assert.notEqual(denyExp.completionText, exp.completionText);
  assert.equal(exp.decision, 'allow');
  assert.equal(exp.denyReason, undefined);
});
check('deny builder：不同 runId 必須產生不同 reason 與 marker', () => {
  assert.notEqual(buildClaudeDenyExpectation('runA').denyReason, buildClaudeDenyExpectation('runB').denyReason);
  assert.notEqual(buildClaudeDenyExpectation('runA').inputMarker, buildClaudeDenyExpectation('runB').inputMarker);
});
check('decline→deny 映射明確，未知值一律 throw（不回退 allow）', () => {
  assert.equal(protocolDecisionFor('accept'), 'allow');
  assert.equal(protocolDecisionFor('decline'), 'deny');
  assert.throws(() => protocolDecisionFor('deny'), /未知的 scenario decision/);
  assert.throws(() => protocolDecisionFor(''), /未知的 scenario decision/);
  assert.throws(() => protocolDecisionFor('ACCEPT'), /未知的 scenario decision/);
});

// --- 核定決策本身的形狀（fail closed，無預設） ------------------------------
check('期望形狀：decision 缺失必須被拒（不得靜默當成 allow）', () => {
  const bad = { ...exp } as Record<string, unknown>; delete bad.decision;
  const v = validateExpectationDecision(bad as unknown as ClaudeApprovalExpectation);
  assert.ok(v.some(x => x.includes('不提供預設')), JSON.stringify(v));
});
check('期望形狀：decision 為未知值必須被拒', () => {
  const bad = { ...exp, decision: 'maybe' } as unknown as ClaudeApprovalExpectation;
  assert.ok(validateExpectationDecision(bad).length > 0);
});
check('期望形狀：deny 案缺 denyReason 必須被拒', () => {
  const bad = { ...denyExp, denyReason: undefined } as unknown as ClaudeApprovalExpectation;
  assert.ok(validateExpectationDecision(bad).some(x => x.includes('denyReason')));
});
check('期望形狀：deny 案的核定理由不得含 "fail closed"', () => {
  const bad = { ...denyExp, denyReason: 'nope (fail closed)' };
  assert.ok(validateExpectationDecision(bad).some(x => x.includes('fail closed')));
});
check('期望形狀：allow 案帶 denyReason 必須被拒（兩案互斥）', () => {
  const bad = { ...exp, denyReason: 'x' };
  assert.ok(validateExpectationDecision(bad).some(x => x.includes('互斥')));
});
check('期望形狀：兩個 builder 的輸出都必須通過形狀驗證', () => {
  assert.deepEqual(validateExpectationDecision(exp), []);
  assert.deepEqual(validateExpectationDecision(denyExp), []);
});

// --- deny 的 MCP 回覆與 broker audit -----------------------------------------
const denyPayload = (over: Record<string, unknown> = {}): unknown =>
  ({ behavior: 'deny', message: denyExp.denyReason, ...over });
const denyFrame = (payload: unknown): unknown => ({
  jsonrpc: '2.0', id: 2,
  result: { content: [{ type: 'text', text: JSON.stringify(payload) }] },
});
const denyTranscript = (payload: unknown = denyPayload()): unknown[] => [
  { seq: 0, dir: 'c2s', frame: { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18' } } },
  { seq: 1, dir: 's2c', frame: { jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-06-18',
    capabilities: { logging: {}, tools: { listChanged: true } }, serverInfo: { name: 'workbench', version: '0.0.1' } } } },
  { seq: 2, dir: 'c2s', frame: { jsonrpc: '2.0', method: 'notifications/initialized' } },
  { seq: 3, dir: 'c2s', frame: { jsonrpc: '2.0', id: 2, method: 'tools/call',
    params: { name: 'approval_prompt', arguments: { tool_name: denyExp.toolName, input: denyExp.input } } } },
  { seq: 4, dir: 's2c', frame: denyFrame(payload) },
];
const DENY_ID = 'ffffffffffffffffffffffffffffffff';
const denyAudit = (decOver: Record<string, unknown> = {}): unknown[] => [
  { ts: 't1', kind: 'request', data: { id: DENY_ID, tool_name: denyExp.toolName, input: denyExp.input,
    raw_params: { name: 'approval_prompt', arguments: { tool_name: denyExp.toolName, input: denyExp.input } } } },
  { ts: 't2', kind: 'decision', data: { id: DENY_ID, behavior: 'deny', message: denyExp.denyReason, ...decOver } },
];

check('deny 正控制：完整入口無違規，且觀察到 broker id', () => {
  const r = judgeClaudeApprovalEvidence(denyTranscript(), denyAudit(), denyExp);
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations, null, 2));
  assert.equal(r.observedBrokerId, DENY_ID);
});
check('deny 反例：MCP 回覆帶 updatedInput 必須被擋（broker 只對 allow 補值）', () => {
  const v = judgeMcpToolCallResult(denyFrame(denyPayload({ updatedInput: denyExp.input })), denyExp);
  assert.ok(v.some(x => x.includes('deny 不得帶 updatedInput')), JSON.stringify(v));
});
check('deny 反例：MCP 回覆的 updatedInput 為 null 也必須被擋（連欄位都不得存在）', () => {
  const v = judgeMcpToolCallResult(denyFrame(denyPayload({ updatedInput: null })), denyExp);
  assert.ok(v.some(x => x.includes('deny 不得帶 updatedInput')), JSON.stringify(v));
});
check('deny 反例：合法的 allow 回覆不算通過', () => {
  const v = judgeMcpToolCallResult(denyFrame({ behavior: 'allow', updatedInput: denyExp.input }), denyExp);
  assert.ok(v.some(x => x.includes('behavior 應為 "deny"')), JSON.stringify(v));
});
check('deny 反例：**理由不同的另一個合法 deny** 不算通過', () => {
  const v = judgeMcpToolCallResult(denyFrame(denyPayload({ message: '使用者其實是別的理由' })), denyExp);
  assert.ok(v.some(x => x.includes('應逐字等於核定理由')), JSON.stringify(v));
});
check('deny 反例：空理由的 deny 在本案不算通過（但不代表 production 禁止空理由）', () => {
  const v = judgeMcpToolCallResult(denyFrame({ behavior: 'deny' }), denyExp);
  assert.ok(v.some(x => x.includes('應逐字等於核定理由')), JSON.stringify(v));
});
check('deny 反例：**broker 逾時的 fail-closed 自動 deny** 不得冒充使用者拒絕', () => {
  const v = judgeMcpToolCallResult(denyFrame({ behavior: 'deny', message: 'approval timeout (fail closed)' }), denyExp);
  assert.ok(v.some(x => x.includes('fail-closed 自動拒絕')), JSON.stringify(v));
});
check('deny 反例：**socket 不可達的 fail-closed 自動 deny** 不得冒充使用者拒絕', () => {
  const v = judgeMcpToolCallResult(denyFrame({ behavior: 'deny', message: 'approval broker unavailable (fail closed)' }), denyExp);
  assert.ok(v.some(x => x.includes('fail-closed 自動拒絕')), JSON.stringify(v));
});
check('deny 反例：MCP 回覆仍不得洩漏 internal broker id', () => {
  const v = judgeMcpToolCallResult(denyFrame(denyPayload({ id: DENY_ID })), denyExp);
  assert.ok(v.some(x => x.includes('不得含 id')), JSON.stringify(v));
});
check('deny 反例：isError 規則對 deny 案同樣成立', () => {
  const f = denyFrame(denyPayload()) as { result: Record<string, unknown> };
  f.result.isError = true;
  assert.ok(judgeMcpToolCallResult(f, denyExp).some(x => x.includes('isError')));
});
check('deny 反例：audit decision 帶 updatedInput 必須被擋', () => {
  const v = judgeBrokerAudit(denyAudit({ updatedInput: denyExp.input }), denyExp).violations;
  assert.ok(v.some(x => x.includes('deny 不得帶 updatedInput')), JSON.stringify(v));
});
check('deny 反例：audit decision 是 allow 必須被擋', () => {
  const v = judgeBrokerAudit(denyAudit({ behavior: 'allow' }), denyExp).violations;
  assert.ok(v.some(x => x.includes('應為 "deny"')), JSON.stringify(v));
});
check('deny 反例：audit decision 的理由與核定不符必須被擋', () => {
  const v = judgeBrokerAudit(denyAudit({ message: '別的理由' }), denyExp).violations;
  assert.ok(v.some(x => x.includes('應逐字等於核定理由')), JSON.stringify(v));
});
check('deny 反例：audit decision 是 fail-closed 必須被擋', () => {
  const v = judgeBrokerAudit(denyAudit({ message: 'approval timeout (fail closed)' }), denyExp).violations;
  assert.ok(v.some(x => x.includes('fail-closed')), JSON.stringify(v));
});
check('deny 反例：**MCP 與 audit 的理由不一致**必須被擋（兩層必須同一筆決策）', () => {
  const v = judgeClaudeApprovalEvidence(
    denyTranscript(denyPayload({ message: '這一層寫別的' })), denyAudit(), denyExp).violations;
  assert.ok(v.some(x => x.startsWith('[mcp]') && x.includes('應逐字等於核定理由')), JSON.stringify(v));
});
check('deny 反例：audit 出現 timeout 列（fail-closed 路徑）必須被擋', () => {
  const a = [denyAudit()[0], { ts: 't9', kind: 'timeout', data: DENY_ID }, denyAudit()[1]];
  const v = judgeBrokerAudit(a, denyExp).violations;
  assert.ok(v.some(x => x.includes('[request, decision]')), JSON.stringify(v));
});
check('deny 反例：**完全沒有 audit**（socket 不可達時 broker 根本沒收到）必須被擋', () => {
  const v = judgeBrokerAudit([], denyExp).violations;
  assert.ok(v.length > 0, JSON.stringify(v));
});
check('allow 案的既有判定未被 deny 分流影響：allow 正控制仍無違規', () => {
  assert.deepEqual(judgeClaudeApprovalEvidence(goodTranscript(), goodAudit(), exp).violations, []);
});
check('allow 案收到 deny 仍必須被擋（既有規則未放寬）', () => {
  const v = judgeMcpToolCallResult(denyFrame(denyPayload()), exp);
  assert.ok(v.some(x => x.includes('behavior 應為 "allow"')), JSON.stringify(v));
});

console.log(`\n${passed} passed (any FAIL above sets process.exitCode=1)`);
