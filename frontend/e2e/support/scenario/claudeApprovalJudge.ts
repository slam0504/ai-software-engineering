// B3a-2b-2 F1a：Claude approval 單一 allow 案的**共用判定**。
//
// 本檔是 **匯出的共用模組**——F1b（假 CLI ＋ 本機 MCP 子程序往返）與 F2（browser）
// 直接 import 同一份判定，**不做 private test-only judge**。這是 #285／#300 已確立
// 的教訓：spec 與 selftest 若各寫一份編排，selftest 全綠也抓不到 spec 的漏檢。
//
// 完整入口是 `judgeClaudeApprovalEvidence`：它**同時**呼叫 MCP transcript（含
// tools/call 回覆）、broker audit 與跨層關聯三項判定。下游**不得**自行挑幾個 helper
// 重組成較弱的路徑，也**不得**把 tools/call 回覆先裁切掉再送進來。
//
// 各判定彼此獨立、都必須各自拒絕缺失／重複／錯 id／錯型別／錯方向／錯序／
// 錯 tool 或 input／decision 不符／allow 缺或錯 updatedInput／error 或 isError／
// JSON-RPC 三種 frame 形狀互相污染／fail-closed 逾時／額外或截斷事件。
// **單一 allow 案不得因為看到任意合法的 deny 就 PASS。**

import { isDeepStrictEqual } from 'node:util';
import {
  APPROVAL_TOOL_NAME,
  MCP_PROTOCOL_VERSION,
  MCP_SERVER_NAME,
  type BrokerAuditRecord,
  type ClaudeApprovalExpectation,
  type McpEvent,
} from './claudeApprovalProtocol.ts';

function isPlainObject(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}
/**
 * 結構相等一律走 Node 的 deep strict equality。
 * **不以 JSON.stringify 比較**——那會把 key 順序當成語意差異，同時把 undefined
 * 與缺欄位混為一談。
 */
function deepEqual(a: unknown, b: unknown): boolean {
  return isDeepStrictEqual(a, b);
}

// ---------------------------------------------------------------------------
// 0. JSON-RPC frame 形狀互斥
//    request / notification / response 是三種互斥形狀；混用代表對端行為異常，
//    不能因為「該看的欄位剛好都對」就放行。
// ---------------------------------------------------------------------------
function checkJsonrpc(f: Record<string, unknown>, label: string): string[] {
  return f.jsonrpc === '2.0'
    ? []
    : [`${label}: jsonrpc 應為 "2.0"，實際 ${JSON.stringify(f.jsonrpc)}`];
}

/** request：必須有 method 與 id，**不得**帶 result 或 error。 */
function checkRequestShape(f: Record<string, unknown>, label: string): string[] {
  const v = checkJsonrpc(f, label);
  if (f.id === undefined) v.push(`${label}: request 必須帶 id`);
  if (f.result !== undefined) v.push(`${label}: request 不得帶 result（request/response 形狀互斥），實際 ${JSON.stringify(f.result)}`);
  if (f.error !== undefined) v.push(`${label}: request 不得帶 error，實際 ${JSON.stringify(f.error)}`);
  return v;
}

/** notification：必須有 method，**不得**帶 id、result 或 error。 */
function checkNotificationShape(f: Record<string, unknown>, label: string): string[] {
  const v = checkJsonrpc(f, label);
  if (f.id !== undefined) v.push(`${label}: notification 不得帶 id，實際 ${JSON.stringify(f.id)}`);
  if (f.result !== undefined) v.push(`${label}: notification 不得帶 result，實際 ${JSON.stringify(f.result)}`);
  if (f.error !== undefined) v.push(`${label}: notification 不得帶 error，實際 ${JSON.stringify(f.error)}`);
  return v;
}

/** response：必須有 result，**不得**帶 method、params 或 error。 */
function checkResponseShape(f: Record<string, unknown>, label: string): string[] {
  const v = checkJsonrpc(f, label);
  if (f.method !== undefined) v.push(`${label}: response 不得帶 method，實際 ${JSON.stringify(f.method)}`);
  if (f.params !== undefined) v.push(`${label}: response 不得帶 params（request/response 形狀互斥），實際 ${JSON.stringify(f.params)}`);
  if (f.error !== undefined) v.push(`${label}: response 不得帶 error，實際 ${JSON.stringify(f.error)}`);
  return v;
}

/**
 * initialize 的 result 必須是**協商成功**的 InitializeResult，不能只是「有 result」。
 *
 * 欄位依 **go.mod 實際解析到的 go-sdk v1.7.0**（`go list -m` 確認）實況，**未自創欄位**：
 *   mcp/protocol.go:1088     InitializeResult{capabilities, protocolVersion, serverInfo}
 *                            三者皆無 omitempty，必定序列化
 *   mcp/protocol.go:2214     Implementation{name, version} 為必填（title/description/
 *                            websiteUrl 為選填）
 *   mcp/server.go:1985       ProtocolVersion: negotiatedVersion(params.ProtocolVersion)
 *   mcp/shared.go:68         negotiatedVersion：client 版本在 supportedProtocolVersions
 *                            內**且** < "2026-07-28" 才原樣回傳，否則回 "2025-11-25"。
 *                            "2025-06-18" 兩個條件都符合，故本案回覆必為同一版本。
 *   mcp/server.go:615        capabilities() 必為非 nil，且 s.tools.len() > 0 時補上 tools
 *   internal/approval/mcpserver.go:41  Implementation{Name:"workbench", Version:"0.0.1"}
 */
function checkInitializeResult(result: unknown, label: string): string[] {
  if (!isPlainObject(result)) {
    return [`${label}: result 應為協商成功的 InitializeResult 物件，實際 ${JSON.stringify(result)}——null／非物件代表握手未成功`];
  }
  const v: string[] = [];
  if (result.protocolVersion !== MCP_PROTOCOL_VERSION) {
    v.push(`${label}: result.protocolVersion 應為 ${JSON.stringify(MCP_PROTOCOL_VERSION)}（client 送的版本受支援時原樣回傳），實際 ${JSON.stringify(result.protocolVersion)}`);
  }
  const caps = result.capabilities;
  if (!isPlainObject(caps)) {
    v.push(`${label}: result.capabilities 應為物件（server.go:454 capabilities() 必為非 nil），實際 ${JSON.stringify(caps)}`);
  } else if (!isPlainObject(caps.tools)) {
    v.push(`${label}: result.capabilities.tools 應為物件（server 已註冊 ${APPROVAL_TOOL_NAME}，必定宣告 tools capability），實際 ${JSON.stringify(caps.tools)}`);
  }
  const info = result.serverInfo;
  if (!isPlainObject(info)) {
    v.push(`${label}: result.serverInfo 應為 {name,version} 物件，實際 ${JSON.stringify(info)}`);
  } else {
    if (info.name !== MCP_SERVER_NAME) {
      v.push(`${label}: result.serverInfo.name 應為 ${JSON.stringify(MCP_SERVER_NAME)}（確認接上的是 workbench 自己的 MCP server），實際 ${JSON.stringify(info.name)}`);
    }
    if (typeof info.version !== 'string' || info.version === '') {
      v.push(`${label}: result.serverInfo.version 應為非空字串，實際 ${JSON.stringify(info.version)}`);
    }
  }
  return v;
}

// ---------------------------------------------------------------------------
// 1. MCP transcript 判定（**涵蓋整段往返，含 tools/call 回覆**）
// ---------------------------------------------------------------------------

/** tools/call request 在完整序列中的索引；其 response 緊接其後。 */
const TOOL_CALL_REQUEST_INDEX = 3;
/** 完整往返恰好五筆：initialize req／resp、notifications/initialized、tools/call req／resp。 */
const EXPECTED_LEN = 5;

export function judgeMcpTranscript(
  events: unknown,
  exp: ClaudeApprovalExpectation,
): string[] {
  if (!Array.isArray(events)) return [`transcript 必須是陣列，實際 ${typeof events}`];
  const v: string[] = [];
  const rows: McpEvent[] = [];
  events.forEach((e, i) => {
    if (!isPlainObject(e) || !isPlainObject(e.frame)
      || (e.dir !== 'c2s' && e.dir !== 's2c')
      || typeof e.seq !== 'number' || !Number.isInteger(e.seq) || e.seq < 0) {
      v.push(`第 ${i} 列不符合 McpEvent 基本型別：${JSON.stringify(e)}`);
      return;
    }
    rows.push(e as unknown as McpEvent);
  });
  if (v.length > 0) return v;
  for (let i = 1; i < rows.length; i += 1) {
    if (rows[i].seq <= rows[i - 1].seq) {
      return [`seq 未遞增：第 ${i} 列 seq=${rows[i].seq} 未大於前一列 ${rows[i - 1].seq}（不得排序掩蓋錯序）`];
    }
  }

  // 本案的完整有序序列——**恰好五筆**，多一筆少一筆都失敗。
  if (rows.length !== EXPECTED_LEN) {
    return [`transcript 應恰好 ${EXPECTED_LEN} 筆（initialize request／response、notifications/initialized、tools/call request／response），實際 ${rows.length} 筆——截斷或多餘事件都不接受`];
  }

  // [0] c2s initialize
  {
    const e = rows[0]; const f = e.frame; const L = 'initialize request';
    if (e.dir !== 'c2s') v.push(`${L}: 方向應為 c2s，實際 ${e.dir}`);
    v.push(...checkRequestShape(f, L));
    if (f.method !== 'initialize') v.push(`${L}: method 應為 "initialize"，實際 ${JSON.stringify(f.method)}`);
    if (f.id !== exp.initializeRequestId) v.push(`${L}: id 應為 ${exp.initializeRequestId}（型別與值皆須相符），實際 ${JSON.stringify(f.id)}`);
    const p = f.params;
    if (!isPlainObject(p) || p.protocolVersion !== MCP_PROTOCOL_VERSION) {
      v.push(`${L}: params.protocolVersion 應為 ${JSON.stringify(MCP_PROTOCOL_VERSION)}，實際 ${JSON.stringify(isPlainObject(p) ? p.protocolVersion : p)}`);
    }
  }
  // [1] s2c initialize response
  {
    const e = rows[1]; const f = e.frame; const L = 'initialize response';
    if (e.dir !== 's2c') v.push(`${L}: 方向應為 s2c，實際 ${e.dir}`);
    v.push(...checkResponseShape(f, L));
    if (f.id !== exp.initializeRequestId) v.push(`${L}: id 應與 request 相同（${exp.initializeRequestId}），實際 ${JSON.stringify(f.id)}`);
    v.push(...checkInitializeResult(f.result, L));
  }
  // [2] c2s notifications/initialized（**notification：不得帶 id／result／error**）
  {
    const e = rows[2]; const f = e.frame; const L = 'notifications/initialized';
    if (e.dir !== 'c2s') v.push(`${L}: 方向應為 c2s，實際 ${e.dir}`);
    v.push(...checkNotificationShape(f, L));
    if (f.method !== 'notifications/initialized') {
      v.push(`${L}: method 應為 "notifications/initialized"（不是 "initialized"），實際 ${JSON.stringify(f.method)}`);
    }
  }
  // [3] c2s tools/call request
  {
    const e = rows[TOOL_CALL_REQUEST_INDEX]; const f = e.frame; const L = 'tools/call request';
    if (e.dir !== 'c2s') v.push(`${L}: 方向應為 c2s，實際 ${e.dir}`);
    v.push(...checkRequestShape(f, L));
    if (f.method !== 'tools/call') v.push(`${L}: method 應為 "tools/call"，實際 ${JSON.stringify(f.method)}`);
    if (f.id !== exp.toolCallRequestId) v.push(`${L}: id 應為 ${exp.toolCallRequestId}，實際 ${JSON.stringify(f.id)}`);
    const p = f.params;
    if (!isPlainObject(p)) { v.push(`${L}: params 應為物件`); }
    else {
      if (p.name !== APPROVAL_TOOL_NAME) v.push(`${L}: params.name 應為 ${JSON.stringify(APPROVAL_TOOL_NAME)}，實際 ${JSON.stringify(p.name)}`);
      const a = p.arguments;
      if (!isPlainObject(a)) v.push(`${L}: params.arguments 應為物件`);
      else {
        if (a.tool_name !== exp.toolName) v.push(`${L}: arguments.tool_name 應為 ${JSON.stringify(exp.toolName)}，實際 ${JSON.stringify(a.tool_name)}`);
        if (!deepEqual(a.input, exp.input)) v.push(`${L}: arguments.input 與獨立期望不符：實際 ${JSON.stringify(a.input)} 期望 ${JSON.stringify(exp.input)}`);
      }
    }
  }
  // [4] s2c tools/call response —— **由本判定內部委派**，caller 不必也不得另外裁切。
  {
    const e = rows[TOOL_CALL_REQUEST_INDEX + 1]; const L = 'tools/call response';
    if (e.dir !== 's2c') v.push(`${L}: 方向應為 s2c，實際 ${e.dir}`);
    v.push(...judgeMcpToolCallResult(e.frame, exp));
  }
  return v;
}

/**
 * tools/call 的回覆單獨判定：result.content[0] 是 TextContent，
 * text 內是 {behavior,message?,updatedInput?}——**不是裸 Decision，且不得含 broker id**。
 *
 * 這是 `judgeMcpTranscript` 的內部組件，保留匯出只為了讓針對回覆的反例能精準定位；
 * **完整判定請呼叫 `judgeMcpTranscript` 或 `judgeClaudeApprovalEvidence`。**
 */
export function judgeMcpToolCallResult(
  frame: unknown,
  exp: ClaudeApprovalExpectation,
): string[] {
  if (!isPlainObject(frame)) return ['tools/call response 必須是物件'];
  const v: string[] = checkResponseShape(frame, 'tools/call response');
  if (frame.id !== exp.toolCallRequestId) v.push(`tools/call response: id 應為 ${exp.toolCallRequestId}，實際 ${JSON.stringify(frame.id)}`);
  const r = frame.result;
  if (!isPlainObject(r)) return v.concat(['tools/call response: 缺少 result 物件']);
  // CallToolResult.IsError 是 Go bool + omitempty（v1.7.0 mcp/protocol.go:305）：只會缺欄位或 false。
  if (r.isError !== undefined && r.isError !== false) {
    v.push(`tools/call response: result.isError 只能缺欄位或 false，實際 ${JSON.stringify(r.isError)}（字串 "true"／truthy 值一律不接受）`);
  }
  const content = r.content;
  if (!Array.isArray(content) || content.length !== 1) {
    return v.concat([`tools/call response: result.content 應恰有 1 筆，實際 ${JSON.stringify(content)}`]);
  }
  const c0 = content[0];
  if (!isPlainObject(c0) || c0.type !== 'text' || typeof c0.text !== 'string') {
    return v.concat([`tools/call response: content[0] 應為 {type:"text",text:string}，實際 ${JSON.stringify(c0)}`]);
  }
  let payload: unknown;
  try { payload = JSON.parse(c0.text as string); }
  catch (e) { return v.concat([`tools/call response: content[0].text 不是合法 JSON：${String(e)}`]); }
  if (!isPlainObject(payload)) return v.concat([`tools/call response: text payload 應為物件，實際 ${JSON.stringify(payload)}`]);
  // **本案是 allow**：不得因為看到任意合法的 deny 就通過。
  if (payload.behavior !== 'allow') {
    v.push(`tools/call response: behavior 應為 "allow"（本案為 allow 案，合法的 deny 也不算通過），實際 ${JSON.stringify(payload.behavior)}`);
  }
  if (payload.updatedInput === undefined) {
    v.push('tools/call response: allow 必須帶 updatedInput（broker.go:119-120 會補上 req.Input）');
  } else if (!deepEqual(payload.updatedInput, exp.input)) {
    v.push(`tools/call response: updatedInput 應等於原 input，實際 ${JSON.stringify(payload.updatedInput)}`);
  }
  // 回覆不得洩漏 internal broker id（mcpserver_test.go 明確檢查這點）
  if ('id' in payload) {
    v.push(`tools/call response: text payload 不得含 id（不可洩漏 internal broker id），實際 ${JSON.stringify(payload.id)}`);
  }
  return v;
}

// ---------------------------------------------------------------------------
// 2. broker audit 判定
// ---------------------------------------------------------------------------
export interface BrokerAuditJudgement {
  violations: string[];
  /** 執行期觀察到的隨機 broker id；判定通過時為非空字串。**這是 observation，不是 expected。** */
  observedBrokerId: string | null;
}

export function judgeBrokerAudit(
  records: unknown,
  exp: ClaudeApprovalExpectation,
): BrokerAuditJudgement {
  const v: string[] = [];
  if (!Array.isArray(records)) return { violations: [`audit 必須是陣列，實際 ${typeof records}`], observedBrokerId: null };
  const rows: BrokerAuditRecord[] = [];
  records.forEach((r, i) => {
    if (!isPlainObject(r)) { v.push(`audit 第 ${i} 列不是物件：${JSON.stringify(r)}`); return; }
    rows.push(r as BrokerAuditRecord);
  });
  if (v.length > 0) return { violations: v, observedBrokerId: null };

  // 本案恰好一組 request → decision；timeout 或多餘事件都失敗。
  const kinds = rows.map(r => r.kind);
  if (rows.length !== 2 || kinds[0] !== 'request' || kinds[1] !== 'decision') {
    return {
      violations: [`audit 應恰為 [request, decision] 兩列，實際 kinds=${JSON.stringify(kinds)}（timeout／重複／截斷／多餘事件都不接受）`],
      observedBrokerId: null,
    };
  }

  const reqData = rows[0].data;
  const decData = rows[1].data;
  if (!isPlainObject(reqData)) v.push('audit request.data 應為物件');
  if (!isPlainObject(decData)) v.push('audit decision.data 應為物件');
  if (v.length > 0) return { violations: v, observedBrokerId: null };
  const req = reqData as Record<string, unknown>;
  const dec = decData as Record<string, unknown>;

  // request：以**預先指定的 tool_name/input** 連結 MCP request（id 是隨機的，不能當 expected）
  if (req.tool_name !== exp.toolName) v.push(`audit request.tool_name 應為 ${JSON.stringify(exp.toolName)}，實際 ${JSON.stringify(req.tool_name)}`);
  if (!deepEqual(req.input, exp.input)) v.push(`audit request.input 與獨立期望不符：實際 ${JSON.stringify(req.input)}`);

  // raw_params 是 broker 收到的**原始 MCP params**（mcpserver.go:45-50 原樣轉發）。
  // 它的 arguments 必須**同時**符合獨立期望、且與外層 Request 欄位一致——
  // 只驗「是物件」會讓內層被整包掉包而外層維持正確的情況通過。
  const rp = req.raw_params;
  if (!isPlainObject(rp)) v.push('audit request.raw_params 應為物件（含 name/arguments）');
  else {
    if (rp.name !== APPROVAL_TOOL_NAME) v.push(`audit request.raw_params.name 應為 ${JSON.stringify(APPROVAL_TOOL_NAME)}，實際 ${JSON.stringify(rp.name)}`);
    const ra = rp.arguments;
    if (!isPlainObject(ra)) {
      v.push(`audit request.raw_params.arguments 應為含 tool_name/input 的物件，實際 ${JSON.stringify(ra)}`);
    } else {
      // (a) 對獨立期望
      if (ra.tool_name !== exp.toolName) {
        v.push(`audit raw_params.arguments.tool_name 與獨立期望不符：應為 ${JSON.stringify(exp.toolName)}，實際 ${JSON.stringify(ra.tool_name)}`);
      }
      if (ra.input === undefined) v.push('audit raw_params.arguments 缺 input');
      else if (!deepEqual(ra.input, exp.input)) {
        v.push(`audit raw_params.arguments.input 與獨立期望不符：實際 ${JSON.stringify(ra.input)} 期望 ${JSON.stringify(exp.input)}`);
      }
      // (b) 對外層 Request 欄位——內外矛盾代表 broker 解析與原始 params 脫鉤
      if (ra.tool_name !== req.tool_name) {
        v.push(`audit raw_params.arguments.tool_name（${JSON.stringify(ra.tool_name)}）與外層 request.tool_name（${JSON.stringify(req.tool_name)}）矛盾`);
      }
      if (!deepEqual(ra.input, req.input)) {
        v.push(`audit raw_params.arguments.input（${JSON.stringify(ra.input)}）與外層 request.input（${JSON.stringify(req.input)}）矛盾`);
      }
    }
  }

  // broker id：只驗「非空、且 request 與 decision 同一個」——observation，不比對固定值
  const reqId = req.id;
  const decId = dec.id;
  if (typeof reqId !== 'string' || reqId === '') v.push(`audit request.id 應為非空字串（隨機 ULID），實際 ${JSON.stringify(reqId)}`);
  if (typeof decId !== 'string' || decId === '') v.push(`audit decision.id 應為非空字串，實際 ${JSON.stringify(decId)}`);
  if (typeof reqId === 'string' && typeof decId === 'string' && reqId !== decId) {
    v.push(`audit 的 request.id 與 decision.id 不同（${JSON.stringify(reqId)} vs ${JSON.stringify(decId)}）——無法建立 request↔decision 關聯`);
  }

  // decision：本案是 allow，且 allow 必須帶等於原 input 的 updatedInput
  if (dec.behavior !== 'allow') {
    v.push(`audit decision.behavior 應為 "allow"（本案為 allow 案），實際 ${JSON.stringify(dec.behavior)}`);
  }
  if (dec.updatedInput === undefined) v.push('audit decision: allow 必須帶 updatedInput');
  else if (!deepEqual(dec.updatedInput, exp.input)) v.push(`audit decision.updatedInput 應等於原 input，實際 ${JSON.stringify(dec.updatedInput)}`);
  if (typeof dec.message === 'string' && dec.message.includes('fail closed')) {
    v.push(`audit decision.message 顯示 fail-closed：${JSON.stringify(dec.message)}——本案不得以 fail-closed 收尾`);
  }

  const ok = v.length === 0 && typeof reqId === 'string' ? reqId : null;
  return { violations: v, observedBrokerId: ok };
}

// ---------------------------------------------------------------------------
// 3. 跨層關聯
// ---------------------------------------------------------------------------
/**
 * 跨層關聯：MCP request 的 arguments 與 broker audit 的 request **必須是同一筆**
 * （以預先指定的 tool_name ＋ 帶 runId 標記的 input 連結）。
 *
 * **前提（本函式只做部分比較，其餘由別的判定負責）：**
 *   - 只比對「識別這一筆 approval」需要的欄位；audit 的完整序列、decision 內容、
 *     broker id 一致性由 `judgeBrokerAudit` 判定，MCP 各 frame 形狀由
 *     `judgeMcpTranscript` 判定。單獨呼叫本函式**不構成完整判定**。
 *   - 需要 audit 第一列是 request 事件；不是的話直接判失敗而非略過。
 *   - DOM／WSID／registry 的整合關聯屬 F2，本檔不合成、也不宣稱已驗。
 */
export function judgeMcpBrokerCorrelation(
  mcpToolCallFrame: unknown,
  auditRecords: unknown,
  exp: ClaudeApprovalExpectation,
): string[] {
  const v: string[] = [];
  if (!isPlainObject(mcpToolCallFrame)) return ['關聯核對：MCP tools/call frame 應為物件'];
  const p = mcpToolCallFrame.params;
  const args = isPlainObject(p) ? p.arguments : undefined;
  if (!isPlainObject(args)) return ['關聯核對：MCP tools/call params.arguments 應為物件'];
  if (!Array.isArray(auditRecords) || auditRecords.length === 0) return ['關聯核對：audit 為空'];
  const first = auditRecords[0];
  if (!isPlainObject(first)) return ['關聯核對：audit 第一列不是物件'];
  if (first.kind !== 'request') {
    return [`關聯核對的前提是 audit 第一列為 request 事件，實際 kind=${JSON.stringify(first.kind)}`];
  }
  const reqData = first.data;
  if (!isPlainObject(reqData)) return ['關聯核對：audit 第一列 data 應為物件'];

  if (args.tool_name !== reqData.tool_name) {
    v.push(`關聯核對：MCP arguments.tool_name（${JSON.stringify(args.tool_name)}）與 broker request.tool_name（${JSON.stringify(reqData.tool_name)}）不符`);
  }
  if (!deepEqual(args.input, reqData.input)) {
    v.push(`關聯核對：MCP arguments.input 與 broker request.input 不符——無法確認是同一筆 approval`);
  }
  // broker 保存的原始 params 也必須是同一筆；只比外層欄位會漏掉內層被掉包的情況。
  const rp = reqData.raw_params;
  if (!isPlainObject(rp)) {
    v.push(`關聯核對：broker request.raw_params 應為物件，實際 ${JSON.stringify(rp)}`);
  } else if (!isPlainObject(rp.arguments)) {
    v.push(`關聯核對：broker request.raw_params.arguments 應為物件，實際 ${JSON.stringify(rp.arguments)}`);
  } else {
    if (args.tool_name !== rp.arguments.tool_name) {
      v.push(`關聯核對：MCP arguments.tool_name（${JSON.stringify(args.tool_name)}）與 broker raw_params.arguments.tool_name（${JSON.stringify(rp.arguments.tool_name)}）不符`);
    }
    if (!deepEqual(args.input, rp.arguments.input)) {
      v.push(`關聯核對：MCP arguments.input 與 broker raw_params.arguments.input 不符——原始 params 不是同一筆`);
    }
  }
  const marker = JSON.stringify(args.input ?? {});
  if (!marker.includes(exp.inputMarker)) {
    v.push(`關聯核對：input 未帶本次 runId 標記 ${JSON.stringify(exp.inputMarker)}——可能是跨執行殘留`);
  }
  return v;
}

// ---------------------------------------------------------------------------
// 4. 完整入口
// ---------------------------------------------------------------------------
export interface ClaudeApprovalEvidenceJudgement {
  /** 各層違規合併後的清單（以 [mcp]／[broker]／[correlation] 前綴標示來源層）。 */
  violations: string[];
  /** 執行期觀察到的隨機 broker id；**observation，不是 expected**。 */
  observedBrokerId: string | null;
}

/**
 * **本案的完整判定入口**：一次涵蓋整段 MCP 往返（五筆，含 tools/call 回覆）、
 * broker audit 完整序列，以及兩者的跨層關聯。
 *
 * F1b／F2 請直接呼叫本函式，**不要**自行挑 helper 重組——重組出來的路徑會比這裡弱。
 * tools/call frame 由本函式自 transcript 取出，**caller 不得預先裁切**。
 */
export function judgeClaudeApprovalEvidence(
  transcript: unknown,
  auditRecords: unknown,
  exp: ClaudeApprovalExpectation,
): ClaudeApprovalEvidenceJudgement {
  const violations: string[] = [];
  violations.push(...judgeMcpTranscript(transcript, exp).map(m => `[mcp] ${m}`));

  const audit = judgeBrokerAudit(auditRecords, exp);
  violations.push(...audit.violations.map(m => `[broker] ${m}`));

  const row = Array.isArray(transcript) ? transcript[TOOL_CALL_REQUEST_INDEX] : undefined;
  const frame = isPlainObject(row) ? row.frame : undefined;
  if (frame === undefined) {
    violations.push(`[correlation] transcript 第 ${TOOL_CALL_REQUEST_INDEX} 列（tools/call request）缺失，無法做跨層關聯`);
  } else {
    violations.push(...judgeMcpBrokerCorrelation(frame, auditRecords, exp).map(m => `[correlation] ${m}`));
  }

  return { violations, observedBrokerId: audit.observedBrokerId };
}
