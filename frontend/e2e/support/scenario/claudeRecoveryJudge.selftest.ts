// B3a-2b-2 E1：Claude 兩輪 start→resume 跨輪判定的離線正負控制。
//
// 負控制刻意對準 reviewer #391 點名的四種假通過形狀：
//   (a) 缺檔／缺輪
//   (b) 重複（兩份證據都自稱第一輪）與輪次調換
//   (c) **整份 round1 冒充 round2**（同 pid、同 approval id、同 marker）
//   (d) argv 少／錯／重複 resume，以及第一輪不該有 resume 卻帶了
// 另外加上 S 的三處綁定（init 宣告／App registry／第二輪 argv）各自被破壞的反例。
import assert from 'node:assert/strict';
import { buildClaudeRecoveryExpectation } from './claudeApprovalProtocol.ts';
import { buildInitEvent, expectedConversationArgv } from './fakeClaudeCli.ts';
import {
  judgeClaudeRecovery, readClaudeRegistryBinding, readWorkspaceResume,
  type ClaudeRecoveryInput, type ClaudeRoundEvidence,
} from './claudeRecoveryJudge.ts';

let passed = 0;
const failures: string[] = [];
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}

const RUN = 'e1test';
const EXPECT = buildClaudeRecoveryExpectation(RUN);
const S = EXPECT.sessionId;
const WSID = 'ws-e1';
const CWD = '/private/tmp/ws-e1';
const CFG = `/w/.workbench/mcp-${WSID}.json`;
const APPROVAL_IDS = ['aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'];

const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x)) as T;

function transcriptFor(k: number): any[] {
  const exp = EXPECT.rounds[k].approval;
  return [
    { seq: 0, dir: 'c2s', frame: { jsonrpc: '2.0', id: 1, method: 'initialize',
      params: { protocolVersion: '2025-06-18' } } },
    { seq: 1, dir: 's2c', frame: { jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-06-18',
      capabilities: { logging: {}, tools: { listChanged: true } },
      serverInfo: { name: 'workbench', version: '0.0.1' } } } },
    { seq: 2, dir: 'c2s', frame: { jsonrpc: '2.0', method: 'notifications/initialized' } },
    { seq: 3, dir: 'c2s', frame: { jsonrpc: '2.0', id: 2, method: 'tools/call',
      params: { name: 'approval_prompt', arguments: { tool_name: exp.toolName, input: exp.input } } } },
    { seq: 4, dir: 's2c', frame: { jsonrpc: '2.0', id: 2, result: { content: [{ type: 'text',
      text: JSON.stringify({ behavior: 'allow', updatedInput: exp.input }) }] } } },
  ];
}
function auditFor(k: number): any[] {
  const exp = EXPECT.rounds[k].approval;
  const id = APPROVAL_IDS[k];
  return [
    { ts: 't1', kind: 'request', data: { id, tool_name: exp.toolName, input: exp.input,
      raw_params: { name: 'approval_prompt', arguments: { tool_name: exp.toolName, input: exp.input } } } },
    { ts: 't2', kind: 'decision', data: { id, behavior: 'allow', updatedInput: exp.input } },
  ];
}
// **fixture 必須是 production 的實際完整格式**（reviewer #393 P1-1）：
// round.json／init.json／mcp-child.json 三者的欄位集合與實際落地檔一致
// （mcp-child.json 取自 CLI selftest 的真實輸出版面），否則形狀驗證等於沒驗。
function roundEvidence(k: number): ClaudeRoundEvidence {
  const r = EXPECT.rounds[k];
  const selfPid = 1000 + k;
  const childPid = 2000 + k;
  return {
    roundRecord: {
      round: r.round, maxRounds: 2, claimDir: `/rounds/round-${r.round}`, pid: selfPid,
      startedAt: '2026-09-21T00:00:00.000Z',
      argv: expectedConversationArgv({ mcpConfigPath: CFG, resume: r.resume }),
    },
    initRecord: { sessionId: S, round: r.round, line: buildInitEvent(S) },
    argv: expectedConversationArgv({ mcpConfigPath: CFG, resume: r.resume }),
    transcript: transcriptFor(k),
    child: {
      selfPid, observedPid: childPid, spawnError: null,
      psDuring: {
        state: 'present', raw: `${childPid} ${selfPid} ${selfPid} S`, stderr: null, status: 0, error: null,
        parsed: { pid: childPid, ppid: selfPid, pgid: selfPid, stat: 'S' },
      },
      psAfter: { state: 'absent', raw: null, stderr: null, status: 1, error: null, parsed: null },
      exitCode: 0, exitSignal: null, stdoutDrained: true, stderrDrained: true,
      cleanupSteps: ['關閉自己持有的 stdin pipe', '子程序退出：code=0 signal=null', '退出後再探 ps：state=absent'],
      unreaped: false,
    },
    mcpConfigPath: CFG,
    domApprovalId: APPROVAL_IDS[k],
    brokerAudit: auditFor(k),
  };
}
function base(): ClaudeRecoveryInput {
  return {
    expectation: EXPECT,
    rounds: [roundEvidence(0), roundEvidence(1)],
    domWsid: WSID,
    appBoundResume: S,
    registryBinding: { cwd: CWD, wsid: WSID },
    canonicalCwd: CWD,
  };
}
const has = (v: string[], s: string): boolean => v.some(x => x.includes(s));

// --- builder 本身 ----------------------------------------------------------
check('builder：兩輪共用同一個 sessionId，第一輪 resume=null、第二輪 resume=S', () => {
  assert.equal(EXPECT.rounds.length, 2);
  assert.equal(EXPECT.rounds[0].resume, null);
  assert.equal(EXPECT.rounds[1].resume, S);
  assert.equal(EXPECT.rounds[0].approval.sessionId, S);
  assert.equal(EXPECT.rounds[1].approval.sessionId, S);
});
check('builder：兩輪的 prompt／marker／input／完成文字全部不同（沿用第一輪必露餡）', () => {
  const a = EXPECT.rounds[0]; const b = EXPECT.rounds[1];
  assert.notEqual(a.prompt, b.prompt);
  assert.notEqual(a.approval.inputMarker, b.approval.inputMarker);
  assert.notEqual(a.approval.completionText, b.approval.completionText);
  assert.notDeepEqual(a.approval.input, b.approval.input);
});
check('builder：同一個 runId 兩次呼叫結果相同（受版控、可重現）', () => {
  assert.deepEqual(buildClaudeRecoveryExpectation(RUN), EXPECT);
});
check('builder：**不同 runId 必須得到不同的 S**（跨執行殘留不得互相冒充）', () => {
  assert.notEqual(buildClaudeRecoveryExpectation('other').sessionId, S);
});
check('第二輪 argv 恰好在既有序列尾端多出 --resume S（session.go:45-47）', () => {
  const fresh = expectedConversationArgv({ mcpConfigPath: CFG });
  const res = expectedConversationArgv({ mcpConfigPath: CFG, resume: S });
  assert.deepEqual(res.slice(0, fresh.length), fresh);
  assert.deepEqual(res.slice(fresh.length), ['--resume', S]);
});

// --- 正控制 ----------------------------------------------------------------
check('正控制：兩輪完整證據無違規', () => {
  const r = judgeClaudeRecovery(base());
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations, null, 2));
  assert.deepEqual(r.agreedApprovalIds, APPROVAL_IDS);
  assert.equal(r.sharedMcpConfigPath, CFG);
});

// --- (a) 缺檔／缺輪 --------------------------------------------------------
check('(a) 只有第一輪證據必須被擋', () => {
  const i = base(); i.rounds = [i.rounds[0]];
  assert.ok(has(judgeClaudeRecovery(i).violations, '與核定輪數'));
});
check('(a) 第二輪缺 init.json 必須被擋', () => {
  const i = base(); i.rounds[1].initRecord = null;
  assert.ok(has(judgeClaudeRecovery(i).violations, 'init.json 應為物件'));
});
check('(a) 第二輪的 mcp-child.json 是空物件必須被擋（缺欄位逐項報出）', () => {
  const i = base(); i.rounds[1].child = {};
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, 'selfPid 應為正整數'), JSON.stringify(v));
  assert.ok(has(v, '缺 psDuring 欄位'), JSON.stringify(v));
  assert.ok(has(v, '缺 psAfter 欄位'), JSON.stringify(v));
});
check('(a) 某一輪的 transcript 被裁成四筆必須被擋（走已審的協定判定）', () => {
  const i = base(); i.rounds[1].transcript = clone(transcriptFor(1)).slice(0, 4);
  assert.ok(has(judgeClaudeRecovery(i).violations, '[round2] [mcp]'));
});

// --- (b) 重複與輪次調換 ----------------------------------------------------
check('(b) 兩份證據都自稱第一輪必須被擋', () => {
  const i = base(); (i.rounds[1].roundRecord as any).round = 1;
  assert.ok(has(judgeClaudeRecovery(i).violations, 'round.json 的 round 欄位應為 2'));
});
check('(b) 輪次調換（第一輪擺第二輪的證據）必須被擋', () => {
  const i = base(); i.rounds = [roundEvidence(1), roundEvidence(0)];
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, 'round.json 的 round 欄位應為 1'), JSON.stringify(v));
  assert.ok(has(v, '[round1] argv'), JSON.stringify(v));
});

// --- (c) 整份 round1 冒充 round2 -------------------------------------------
check('(c) **整份第一輪證據當成第二輪**必須被擋（pid／approval id／marker 全部露餡）', () => {
  const i = base();
  const dup = roundEvidence(0);
  (dup.roundRecord as any).round = 2; // 只把輪次欄位改掉，其餘照抄第一輪
  i.rounds[1] = dup;
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, '[round2] argv'), JSON.stringify(v));          // 少了 --resume
  assert.ok(has(v, 'CLI pid 相同'), JSON.stringify(v));
  assert.ok(has(v, 'MCP 子程序 pid 相同'), JSON.stringify(v));
  // 內容也對不上：第一輪的 marker 送不出第二輪的期望（已審的協定／broker 判定擋下）
  assert.ok(has(v, '與獨立期望不符'), JSON.stringify(v));
});
check('(c) 第二輪內容正確、只有 approval id 沿用第一輪，也必須被擋', () => {
  const i = base();
  // 除了 approval id 之外全部是貨真價實的第二輪證據——只把 UI 觀察值與
  // broker audit 的 id 換成第一輪那一顆。
  const dupId = APPROVAL_IDS[0];
  i.rounds[1].domApprovalId = dupId;
  i.rounds[1].brokerAudit = auditFor(1).map(r => ({ ...r, data: { ...r.data, id: dupId } }));
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, 'approval id 相同'), JSON.stringify(v));
});
check('(c) 只把第二輪的 pid 換成第一輪的（其餘正確）也必須被擋', () => {
  const i = base();
  const p0 = (i.rounds[0].child as any).selfPid;
  (i.rounds[1].child as any).selfPid = p0;
  (i.rounds[1].child as any).psDuring.parsed.ppid = p0;
  (i.rounds[1].roundRecord as any).pid = p0; // 連 claim 紀錄一起改，避免被同源檢查先擋
  assert.ok(has(judgeClaudeRecovery(i).violations, 'CLI pid 相同'));
});
check('(c) 第一輪的程序在第二輪仍存活（psAfter=present）必須被擋', () => {
  const i = base();
  (i.rounds[0].child as any).psAfter = { state: 'present', raw: '1000 1 1000 S', stderr: null, status: 0,
    error: null, parsed: { pid: 1000, ppid: 1, pgid: 1000, stat: 'S' } };
  assert.ok(has(judgeClaudeRecovery(i).violations, '仍查得到該 pid'));
});
check('(c) OS 再探失敗（error）不得當成已消失', () => {
  const i = base();
  (i.rounds[0].child as any).psAfter = { state: 'error', raw: null, stderr: 'boom', status: 2,
    error: 'ps 以非預期方式結束', parsed: null };
  assert.ok(has(judgeClaudeRecovery(i).violations, '查不成 ≠ 查不到'));
});

// --- (d) argv 的 resume ----------------------------------------------------
check('(d) 第二輪**缺** --resume 必須被擋', () => {
  const i = base();
  i.rounds[1].argv = expectedConversationArgv({ mcpConfigPath: CFG });
  assert.ok(has(judgeClaudeRecovery(i).violations, '卻沒有 --resume'));
});
check('(d) 第二輪 --resume 的值**不是 S** 必須被擋', () => {
  const i = base();
  i.rounds[1].argv = expectedConversationArgv({ mcpConfigPath: CFG, resume: 'other-session' });
  assert.ok(has(judgeClaudeRecovery(i).violations, '[round2] argv['));
});
check('(d) 第二輪 --resume **重複出現**必須被擋', () => {
  const i = base();
  i.rounds[1].argv = [...expectedConversationArgv({ mcpConfigPath: CFG, resume: S }), '--resume', S];
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, '重複出現'), JSON.stringify(v));
});
check('(d) 第一輪**不該有** resume 卻帶了必須被擋', () => {
  const i = base();
  i.rounds[0].argv = expectedConversationArgv({ mcpConfigPath: CFG, resume: S });
  assert.ok(has(judgeClaudeRecovery(i).violations, '不得含 --resume'));
});

// --- S 的三處綁定 ----------------------------------------------------------
check('S：第一輪 init 宣告的 session id 不等於核定 S 必須被擋', () => {
  const i = base(); (i.rounds[0].initRecord as any).sessionId = 'declared-something-else';
  assert.ok(has(judgeClaudeRecovery(i).violations, '不等於核定的 S'));
});
check('S：App workspace registry 沒綁到 S 必須被擋', () => {
  const i = base(); i.appBoundResume = null;
  assert.ok(has(judgeClaudeRecovery(i).violations, '沒有把第一輪宣告的 session 綁成續聊身分'));
});
check('S：claude registry 缺綁定必須被擋', () => {
  const i = base(); i.registryBinding = null;
  assert.ok(has(judgeClaudeRecovery(i).violations, '沒有'));
});
check('S：registry 綁到別的 WSID 必須被擋', () => {
  const i = base(); i.registryBinding = { cwd: CWD, wsid: 'ws-other' };
  assert.ok(has(judgeClaudeRecovery(i).violations, '與 UI 觀察到的'));
});
check('S：registry 綁到別的 cwd 必須被擋', () => {
  const i = base(); i.registryBinding = { cwd: '/elsewhere', wsid: WSID };
  assert.ok(has(judgeClaudeRecovery(i).violations, '不等於 canonical workspace'));
});

// --- config 路徑 -----------------------------------------------------------
check('config：兩輪的 --mcp-config 不同必須被擋（同一個 WSID 應共用同一份）', () => {
  const i = base();
  const other = `/w/.workbench/mcp-${WSID}-2.json`;
  i.rounds[1].argv = expectedConversationArgv({ mcpConfigPath: other, resume: S });
  assert.ok(has(judgeClaudeRecovery(i).violations, '路徑不同'));
});
check('config：**兩輪共用同一份 config 是正常的**，不得因為相同就判失敗', () => {
  assert.deepEqual(judgeClaudeRecovery(base()).violations, []);
});


// --- P1-1（reviewer #393）：壞的程序／身分證據必須被拒 --------------------
// 舊版自己手刻了一份較寬鬆的程序判定，以下每一條都曾經 violations=[]。
// 現在一律走與 CLI 同一份的 judgeChildObservation ＋落地 JSON 的形狀驗證。
const childCases: Array<[string, (i: ClaudeRecoveryInput) => void, string]> = [
  ['psDuring=null（往返期間沒做 OS 觀測）', i => { (i.rounds[1].child as any).psDuring = null; }, '沒有做 OS 身分觀測'],
  ['psDuring.state=error（查不成 ≠ 查不到）', i => {
    (i.rounds[1].child as any).psDuring = { state: 'error', raw: null, stderr: 'boom', status: 2, error: 'ps 無法執行', parsed: null };
  }, 'OS 觀測失敗'],
  ['exitCode 非 0', i => { (i.rounds[1].child as any).exitCode = 17; }, 'exit code 應為 0'],
  ['exitSignal 非 null', i => { (i.rounds[1].child as any).exitSignal = 'SIGKILL'; }, '不應被訊號終止'],
  ['unreaped=true', i => { (i.rounds[1].child as any).unreaped = true; }, '未能收乾淨'],
  ['stdout 未 drain', i => { (i.rounds[1].child as any).stdoutDrained = false; }, 'stdout 未在期限內結束'],
  ['stderr 未 drain', i => { (i.rounds[1].child as any).stderrDrained = false; }, 'stderr 未在期限內結束'],
  ['spawnError 非 null', i => { (i.rounds[1].child as any).spawnError = 'ENOENT'; }, '[spawn]'],
  ['ppid 不是本輪 CLI', i => { (i.rounds[1].child as any).psDuring.parsed.ppid = 7777; }, '不是本行程'],
];
for (const [name, mut, needle] of childCases) {
  check(`P1-1 程序證據：${name} 必須被擋`, () => {
    const i = base(); mut(i);
    const v = judgeClaudeRecovery(i).violations;
    assert.ok(has(v, needle), JSON.stringify(v));
  });
}

const shapeCases: Array<[string, (i: ClaudeRecoveryInput) => void, string]> = [
  ['缺 psAfter 欄位', i => { delete (i.rounds[1].child as any).psAfter; }, '缺 psAfter 欄位'],
  ['缺 psDuring 欄位', i => { delete (i.rounds[1].child as any).psDuring; }, '缺 psDuring 欄位'],
  ['psAfter.state 是未知值', i => { (i.rounds[1].child as any).psAfter.state = 'maybe'; }, 'state 應為 present'],
  ['psAfter 缺 parsed 欄位', i => { delete (i.rounds[1].child as any).psAfter.parsed; }, '缺 parsed 欄位'],
  ['selfPid 不是正整數', i => { (i.rounds[1].child as any).selfPid = 0; }, 'selfPid 應為正整數'],
  ['selfPid 是字串', i => { (i.rounds[1].child as any).selfPid = '1001'; }, 'selfPid 應為正整數'],
  ['unreaped 不是布林', i => { (i.rounds[1].child as any).unreaped = 'false'; }, 'unreaped 應為布林'],
  ['cleanupSteps 不是字串陣列', i => { (i.rounds[1].child as any).cleanupSteps = 'x'; }, 'cleanupSteps 應為字串陣列'],
  ['psDuring.parsed.ppid 不是正整數', i => { (i.rounds[1].child as any).psDuring.parsed.ppid = -1; }, 'parsed.ppid 應為正整數'],
  ['mcp-child.json 是 null', i => { (i.rounds[1] as any).child = null; }, 'mcp-child.json 應為物件'],
];
for (const [name, mut, needle] of shapeCases) {
  check(`P1-1 形狀驗證：${name} 必須被擋（JSON 沒有型別保證）`, () => {
    const i = base(); mut(i);
    const v = judgeClaudeRecovery(i).violations;
    assert.ok(has(v, needle), JSON.stringify(v));
  });
}

check('P1-1 claim 與程序證據同源：round.json 的 pid 與 child.selfPid 不符必須被擋', () => {
  const i = base(); (i.rounds[1].roundRecord as any).pid = 9999;
  assert.ok(has(judgeClaudeRecovery(i).violations, '與 mcp-child.selfPid'));
});
check('P1-1 claim 與程序證據同源：round.json 缺 pid 必須被擋', () => {
  const i = base(); delete (i.rounds[1].roundRecord as any).pid;
  assert.ok(has(judgeClaudeRecovery(i).violations, 'round.json 的 pid 應為正整數'));
});

// --- P1-1：init 必須以原始 line 為準 ---------------------------------------
check('P1-1 init：**原始 line 的 session_id 被改掉、便利欄位不變**必須被擋', () => {
  const i = base();
  (i.rounds[0].initRecord as any).line = JSON.stringify({ type: 'system', subtype: 'init', session_id: 'WRONG' });
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, 'init 事件實際宣告的 session_id'), JSON.stringify(v));
  assert.ok(has(v, '與便利欄位 sessionId'), JSON.stringify(v));
});
check('P1-1 init：缺原始 line 必須被擋（不得只憑便利欄位）', () => {
  const i = base(); delete (i.rounds[0].initRecord as any).line;
  assert.ok(has(judgeClaudeRecovery(i).violations, '缺原始 init 事件 line'));
});
check('P1-1 init：line 的 type／subtype 不符 production 形狀必須被擋', () => {
  const i = base();
  (i.rounds[0].initRecord as any).line = JSON.stringify({ type: 'assistant', session_id: S });
  const v = judgeClaudeRecovery(i).violations;
  assert.ok(has(v, 'type 應為 "system"'), JSON.stringify(v));
  assert.ok(has(v, 'subtype 應為 "init"'), JSON.stringify(v));
});
check('P1-1 init：line 不是合法 JSON 必須被擋', () => {
  const i = base(); (i.rounds[0].initRecord as any).line = 'NOT JSON';
  assert.ok(has(judgeClaudeRecovery(i).violations, '不是合法 JSON'));
});
check('P1-1 init：round 欄位與本輪不符必須被擋', () => {
  const i = base(); (i.rounds[1].initRecord as any).round = 1;
  assert.ok(has(judgeClaudeRecovery(i).violations, 'init.json 的 round'));
});
check('P1-1 init：正控制的 line 就是 production 的 buildInitEvent 輸出（fixture 為實際完整格式）', () => {
  assert.equal((base().rounds[0].initRecord as any).line, buildInitEvent(S));
});

// --- App 端落地檔解析 ------------------------------------------------------
check('解析：sessions.json 取得 S 的 cwd/wsid', () => {
  const text = JSON.stringify({ [S]: { cwd: CWD, wsid: WSID, created_at: 't' } });
  assert.deepEqual(readClaudeRegistryBinding(text, S), { cwd: CWD, wsid: WSID });
});
check('解析：sessions.json 沒有該 id／內容壞掉一律回 null，不硬湊', () => {
  assert.equal(readClaudeRegistryBinding(JSON.stringify({ other: { cwd: '/x', wsid: 'w' } }), S), null);
  assert.equal(readClaudeRegistryBinding('NOT JSON', S), null);
  assert.equal(readClaudeRegistryBinding(JSON.stringify({ [S]: { cwd: 1, wsid: 2 } }), S), null);
});
check('解析：workspace-sessions.json 取得該 WSID 的 resume_session_id', () => {
  const text = JSON.stringify({ schema_version: 2, entries: { [WSID]: { wsid: WSID, resume_session_id: S } } });
  assert.equal(readWorkspaceResume(text, WSID), S);
});
check('解析：resume_session_id 為空字串或缺欄位回 null，不當成綁定成功', () => {
  assert.equal(readWorkspaceResume(JSON.stringify({ entries: { [WSID]: { resume_session_id: '' } } }), WSID), null);
  assert.equal(readWorkspaceResume(JSON.stringify({ entries: { [WSID]: {} } }), WSID), null);
  assert.equal(readWorkspaceResume('{}', WSID), null);
  assert.equal(readWorkspaceResume('NOT JSON', WSID), null);
});

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
