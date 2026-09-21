// B3a-2b-2 E1：Claude 兩輪 start → resume 的**跨輪判定**。
//
// 定位：單輪內部的判定完全沿用已審的既有函式，**不另造較弱的第二份**——
//   - 協定／broker／UI 三方一致：`judgeClaudeUiConsistency`
//     （內部再呼叫 F1a 的 `judgeClaudeApprovalEvidence`）
//   - 子程序所有權與收尾：`judgeChildObservation`（自 `judgeRoundTripSuccess`
//     抽出的同一份，見 fakeClaudeCli.ts）
//   - argv：`validateConversationArgv` ＋ `expectedConversationArgv`
//
// reviewer #393 P1-1 實測到的缺口（本版修正）：舊版自己手刻了一份較寬鬆的
// 程序判定，psDuring 缺失／psDuring.state=error／exitCode!=0／exitSignal 非
// null／unreaped=true／stdout 未 drain／roundRecord.pid 與 child.selfPid 不符／
// init 原始 line 的 session_id 被改掉但便利欄位不變——八種壞證據全部 violations=[]。
// 修法是：先對落地 JSON 做**嚴格 runtime 形狀驗證**（JSON 沒有型別保證），再把
// 驗過形狀的物件交給上述既有判定。
//
// 本檔只加「兩輪之間」才成立的那些事實：
//   1. 輪次來自假 CLI 的**排他 claim**（round.json），不是由 argv 推導的。
//   2. 每輪 argv 必須逐位置等於該輪核定的期望——第一輪 fresh、第二輪
//      `--resume S`。S 來自固定 builder，不從第二輪待驗輸出反填。
//   3. S 的三處交叉核對：第一輪實際 init 宣告（**解析原始 line**）、App 綁定
//      （registry ／ workspace registry）、第二輪 argv。
//   4. 兩輪是**不同的程序身分**：CLI 自己的 pid 與 MCP 子程序的 pid 都必須不同。
//   5. 兩輪的可辨識內容（input marker／完成文字／broker approval id）必須不同，
//      而共用的 config 路徑必須**相同**（同一個 WSID 本來就共用；它不是輪次的
//      判別依據，所以這裡要求相同、不要求不同）。
//
// 契約來源（一手讀碼）：
//   internal/claude/session.go:45-47      Resume 非空時在序列尾端補 --resume <值>
//   app.go:7504                            registry.Bind(sessionID, cwd, wsid)
//   app.go:9076 commitClaudeResume         → wsReg.SetResume(wsid, sessionID)
//   internal/claude/registry.go:30-34      sessions.json = {id: {cwd,wsid,created_at}}
//   internal/wsregistry/store.go:67-79,92  workspace-sessions.json = {schema_version,entries}
//   fakeClaudeCli.ts buildInitEvent        {"type":"system","subtype":"init","session_id":<S>}
import { judgeClaudeUiConsistency } from './claudeAppEvidence.ts';
import {
  expectedConversationArgv, judgeChildObservation, validateConversationArgv,
  type ChildObservation, type OsProbe, type PsRow,
} from './fakeClaudeCli.ts';
import type { ClaudeRecoveryExpectation } from './claudeApprovalProtocol.ts';

/** 單輪的**已落地證據**（全部由呼叫端自該輪目錄讀出，本模組不碰檔案系統）。 */
export interface ClaudeRoundEvidence {
  /** round-<n>/round.json：claim 當下寫下的輪次事實。 */
  roundRecord: unknown;
  /** round-<n>/init.json：該輪 CLI 實際宣告的 session id ＋原始 init 事件。 */
  initRecord: unknown;
  /** round-<n>/argv.json。 */
  argv: unknown;
  /** round-<n>/mcp-transcript.json。 */
  transcript: unknown;
  /** round-<n>/mcp-child.json。 */
  child: unknown;
  /** round-<n>/mcp-config.path.txt（已 trim）。 */
  mcpConfigPath: string;
  /** 該輪 UI 觀察到的 approval id。 */
  domApprovalId: string | null;
  /** 該輪挑出的 broker audit 行。 */
  brokerAudit: unknown[];
}

export interface ClaudeRecoveryInput {
  /** 固定 builder 的兩輪期望——**唯一的期望來源**。 */
  expectation: ClaudeRecoveryExpectation;
  /** 依輪次排好的證據（長度必須等於 expectation.rounds.length）。 */
  rounds: ClaudeRoundEvidence[];
  /** UI 觀察到的 WSID（兩輪必須同一個）。 */
  domWsid: string | null;
  /** workspace-sessions.json 該 WSID entry 的 resume_session_id。 */
  appBoundResume: string | null;
  /** sessions.json 對 S 的綁定。 */
  registryBinding: { cwd: string; wsid: string } | null;
  /** canonical workspace（realpath）——registry 綁定的 cwd 必須等於它。 */
  canonicalCwd: string;
}

export interface ClaudeRecoveryJudgement {
  violations: string[];
  /** 每輪三方同意的 approval id（順序同 rounds）。 */
  agreedApprovalIds: Array<string | null>;
  /** 兩輪共用的 config 路徑；不一致時為 null。 */
  sharedMcpConfigPath: string | null;
}

function isPlainObject(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}

function isPositiveInt(x: unknown): x is number {
  return typeof x === 'number' && Number.isInteger(x) && x > 0;
}

function asStringArray(x: unknown): string[] | null {
  return Array.isArray(x) && x.every(v => typeof v === 'string') ? (x as string[]) : null;
}

function mcpConfigFromArgv(argv: string[]): string | null {
  const i = argv.indexOf('--mcp-config');
  if (i < 0 || argv.indexOf('--mcp-config', i + 1) >= 0) return null;
  const v = argv[i + 1];
  return typeof v === 'string' && v !== '' && !v.startsWith('--') ? v : null;
}

// ---------------------------------------------------------------------------
// 落地 JSON 的嚴格形狀驗證
//
// `JSON.parse(...) as T` 只是型別斷言、沒有任何 runtime 保證。缺欄位、型別錯、
// null 冒充物件都會讓後續的 property access 悄悄拿到 undefined 而「沒有違規」。
// 這裡把每一個會被判定讀到的欄位都驗過，缺一即違規。
// ---------------------------------------------------------------------------
const PROBE_STATES = new Set(['present', 'absent', 'error']);

function parsePsRowShape(x: unknown, label: string, v: string[]): PsRow | null {
  if (x === null) return null;
  if (!isPlainObject(x)) { v.push(`${label}.parsed 應為物件或 null，實際 ${JSON.stringify(x)}`); return null; }
  for (const k of ['pid', 'ppid', 'pgid'] as const) {
    if (!isPositiveInt(x[k])) v.push(`${label}.parsed.${k} 應為正整數，實際 ${JSON.stringify(x[k])}`);
  }
  if (typeof x.stat !== 'string') v.push(`${label}.parsed.stat 應為字串，實際 ${JSON.stringify(x.stat)}`);
  return x as unknown as PsRow;
}

function parseProbeShape(x: unknown, label: string, v: string[]): OsProbe | null {
  if (x === null) return null;
  if (!isPlainObject(x)) { v.push(`${label} 應為物件或 null，實際 ${JSON.stringify(x)}`); return null; }
  if (typeof x.state !== 'string' || !PROBE_STATES.has(x.state)) {
    v.push(`${label}.state 應為 present／absent／error，實際 ${JSON.stringify(x.state)}`);
  }
  for (const k of ['raw', 'stderr', 'error'] as const) {
    if (x[k] !== null && typeof x[k] !== 'string') {
      v.push(`${label}.${k} 應為字串或 null，實際 ${JSON.stringify(x[k])}`);
    }
  }
  if (x.status !== null && typeof x.status !== 'number') {
    v.push(`${label}.status 應為數字或 null，實際 ${JSON.stringify(x.status)}`);
  }
  if (!Object.hasOwn(x, 'parsed')) v.push(`${label} 缺 parsed 欄位`);
  else parsePsRowShape(x.parsed, label, v);
  return x as unknown as OsProbe;
}

/**
 * 驗 `mcp-child.json` 的完整形狀。**通過形狀之後才交給既有的
 * `judgeChildObservation`**——形狀沒過就不可能有意義地判定收尾。
 */
export function parseChildObservation(
  x: unknown, tag: string,
): { obs: ChildObservation | null; violations: string[] } {
  const v: string[] = [];
  if (!isPlainObject(x)) {
    return { obs: null, violations: [`${tag} mcp-child.json 應為物件，實際 ${JSON.stringify(x)}`] };
  }
  if (!isPositiveInt(x.selfPid)) v.push(`${tag} mcp-child.selfPid 應為正整數，實際 ${JSON.stringify(x.selfPid)}`);
  if (x.observedPid !== null && !isPositiveInt(x.observedPid)) {
    v.push(`${tag} mcp-child.observedPid 應為正整數或 null，實際 ${JSON.stringify(x.observedPid)}`);
  }
  if (x.spawnError !== null && typeof x.spawnError !== 'string') {
    v.push(`${tag} mcp-child.spawnError 應為字串或 null，實際 ${JSON.stringify(x.spawnError)}`);
  }
  for (const k of ['psDuring', 'psAfter'] as const) {
    if (!Object.hasOwn(x, k)) v.push(`${tag} mcp-child 缺 ${k} 欄位——沒有 OS 觀測就不算核對過`);
    else parseProbeShape(x[k], `${tag} mcp-child.${k}`, v);
  }
  if (x.exitCode !== null && typeof x.exitCode !== 'number') {
    v.push(`${tag} mcp-child.exitCode 應為數字或 null，實際 ${JSON.stringify(x.exitCode)}`);
  }
  if (x.exitSignal !== null && typeof x.exitSignal !== 'string') {
    v.push(`${tag} mcp-child.exitSignal 應為字串或 null，實際 ${JSON.stringify(x.exitSignal)}`);
  }
  for (const k of ['stdoutDrained', 'stderrDrained', 'unreaped'] as const) {
    if (typeof x[k] !== 'boolean') v.push(`${tag} mcp-child.${k} 應為布林，實際 ${JSON.stringify(x[k])}`);
  }
  if (asStringArray(x.cleanupSteps) === null) {
    v.push(`${tag} mcp-child.cleanupSteps 應為字串陣列，實際 ${JSON.stringify(x.cleanupSteps)}`);
  }
  return { obs: v.length === 0 ? (x as unknown as ChildObservation) : null, violations: v };
}

/**
 * 驗 `init.json`：**以保存的原始 line 為準**，便利欄位只是交叉核對的對象。
 * 只信 `sessionId` 這個便利欄位，會讓「原始事件宣告了別的 session、便利欄位
 * 卻寫對」整個過關（reviewer #393 P1-1 實測）。
 */
export function judgeInitRecord(
  rec: unknown, expectedSessionId: string, expectedRound: number, tag: string,
): string[] {
  const v: string[] = [];
  if (!isPlainObject(rec)) return [`${tag} init.json 應為物件，實際 ${JSON.stringify(rec)}`];
  if (rec.sessionId !== expectedSessionId) {
    v.push(`${tag} init.json 的 sessionId（${JSON.stringify(rec.sessionId)}）不等於核定的 S（${expectedSessionId}）`);
  }
  if (rec.round !== expectedRound) {
    v.push(`${tag} init.json 的 round（${JSON.stringify(rec.round)}）應為 ${expectedRound}`);
  }
  if (typeof rec.line !== 'string' || rec.line === '') {
    v.push(`${tag} init.json 缺原始 init 事件 line——不得只憑便利欄位判定 session 身分`);
    return v;
  }
  let parsed: unknown;
  try { parsed = JSON.parse(rec.line); }
  catch (e) { v.push(`${tag} init.json 的原始 line 不是合法 JSON：${String(e)}`); return v; }
  if (!isPlainObject(parsed)) {
    v.push(`${tag} init.json 的原始 line 應為物件，實際 ${JSON.stringify(parsed)}`);
    return v;
  }
  if (parsed.type !== 'system') v.push(`${tag} init 事件 type 應為 "system"，實際 ${JSON.stringify(parsed.type)}`);
  if (parsed.subtype !== 'init') v.push(`${tag} init 事件 subtype 應為 "init"，實際 ${JSON.stringify(parsed.subtype)}`);
  if (parsed.session_id !== expectedSessionId) {
    v.push(`${tag} init 事件實際宣告的 session_id（${JSON.stringify(parsed.session_id)}）不等於核定的 S（${expectedSessionId}）`);
  }
  if (parsed.session_id !== rec.sessionId) {
    v.push(`${tag} init 事件的 session_id（${JSON.stringify(parsed.session_id)}）與便利欄位 sessionId（${JSON.stringify(rec.sessionId)}）不一致`);
  }
  return v;
}

/** 自 sessions.json 原文取出某個 session id 的綁定。解析失敗回 null。 */
export function readClaudeRegistryBinding(
  text: string, sessionId: string,
): { cwd: string; wsid: string } | null {
  let parsed: unknown;
  try { parsed = JSON.parse(text); } catch { return null; }
  if (!isPlainObject(parsed)) return null;
  const e = parsed[sessionId];
  if (!isPlainObject(e)) return null;
  const cwd = e.cwd;
  const wsid = e.wsid;
  if (typeof cwd !== 'string' || typeof wsid !== 'string') return null;
  return { cwd, wsid };
}

/** 自 workspace-sessions.json 原文取出某個 WSID 的 resume_session_id。 */
export function readWorkspaceResume(text: string, wsid: string): string | null {
  let parsed: unknown;
  try { parsed = JSON.parse(text); } catch { return null; }
  if (!isPlainObject(parsed)) return null;
  const entries = parsed.entries;
  if (!isPlainObject(entries)) return null;
  const e = entries[wsid];
  if (!isPlainObject(e)) return null;
  const r = e.resume_session_id;
  return typeof r === 'string' && r !== '' ? r : null;
}

export function judgeClaudeRecovery(i: ClaudeRecoveryInput): ClaudeRecoveryJudgement {
  const violations: string[] = [];
  const agreedApprovalIds: Array<string | null> = [];
  const expected = i.expectation.rounds;

  if (i.rounds.length !== expected.length) {
    violations.push(
      `[round] 證據輪數（${i.rounds.length}）與核定輪數（${expected.length}）不符`
      + '——缺輪、多輪都不得通過',
    );
  }

  const configPaths: string[] = [];
  const selfPids: Array<number | null> = [];
  const childPids: Array<number | null> = [];
  const n = Math.min(i.rounds.length, expected.length);
  for (let k = 0; k < n; k += 1) {
    const ev = i.rounds[k];
    const exp = expected[k];
    const tag = `[round${exp.round}]`;

    // 1. 輪次事實來自排他 claim，不是 argv
    let claimedPid: number | null = null;
    if (!isPlainObject(ev.roundRecord)) {
      violations.push(`${tag} round.json 應為物件，實際 ${JSON.stringify(ev.roundRecord)}`);
    } else {
      if (ev.roundRecord.round !== exp.round) {
        violations.push(
          `${tag} round.json 的 round 欄位應為 ${exp.round}，實際 ${JSON.stringify(ev.roundRecord.round)}`
          + '——輪次由排他 claim 決定，證據必須自己說明它是第幾輪',
        );
      }
      if (!isPositiveInt(ev.roundRecord.pid)) {
        violations.push(`${tag} round.json 的 pid 應為正整數，實際 ${JSON.stringify(ev.roundRecord.pid)}`);
      } else {
        claimedPid = ev.roundRecord.pid;
      }
    }

    // 2. argv 逐位置全等（含本輪核定的 resume）
    const argv = asStringArray(ev.argv);
    if (argv === null) {
      violations.push(`${tag} argv.json 不是字串陣列：${JSON.stringify(ev.argv)}`);
    } else {
      const cfg = mcpConfigFromArgv(argv);
      if (cfg === null) {
        violations.push(`${tag} argv 無法取得唯一的 --mcp-config 值：${JSON.stringify(argv)}`);
      } else {
        configPaths.push(cfg);
        const av = validateConversationArgv(
          argv, expectedConversationArgv({ mcpConfigPath: cfg, resume: exp.resume }), exp.resume);
        for (const m of av) violations.push(`${tag} ${m}`);
      }
    }

    // 3. 本輪實際宣告的 session id（以原始 init 事件為準）
    for (const m of judgeInitRecord(ev.initRecord, i.expectation.sessionId, exp.round, tag)) {
      violations.push(m);
    }

    // 4. 單輪內部的協定／broker／UI 一致性：完全走已審的判定
    const ui = judgeClaudeUiConsistency({
      transcript: ev.transcript,
      brokerAudit: ev.brokerAudit,
      domWsid: i.domWsid,
      domApprovalId: ev.domApprovalId,
      mcpConfigPath: ev.mcpConfigPath,
      exp: exp.approval,
    });
    for (const m of ui.violations) violations.push(`${tag} ${m}`);
    agreedApprovalIds.push(ui.agreedApprovalId);

    // 5. 本輪的程序身分與收尾：先驗形狀，再走**與 CLI 同一份**的判定
    const parsedChild = parseChildObservation(ev.child, tag);
    for (const m of parsedChild.violations) violations.push(m);
    if (parsedChild.obs === null) {
      selfPids.push(null); childPids.push(null);
    } else {
      const o = parsedChild.obs;
      selfPids.push(o.selfPid); childPids.push(o.observedPid);
      if (o.spawnError !== null) violations.push(`${tag} [spawn] ${o.spawnError}`);
      for (const m of judgeChildObservation(o)) violations.push(`${tag} ${m}`);
      // claim 當下記下的 pid 必須就是留下這份觀測的那個程序
      if (claimedPid !== null && claimedPid !== o.selfPid) {
        violations.push(
          `${tag} round.json 的 pid（${claimedPid}）與 mcp-child.selfPid（${o.selfPid}）不符`
          + '——輪次 claim 與程序證據不是同一個程序留下的');
      }
    }
  }

  // --- 跨輪 ---------------------------------------------------------------
  if (n === expected.length && expected.length >= 2) {
    // 共用 config：同一個 WSID 本來就共用同一份 <stateDir>/mcp-<WSID>.json，
    // 因此這裡要求**相同**。它不是輪次判別依據（socket index 也會被回收重配）。
    const uniq = [...new Set(configPaths)];
    if (configPaths.length === expected.length && uniq.length !== 1) {
      violations.push(`[cross] 兩輪的 --mcp-config 路徑不同（${uniq.join('、')}）——同一個 WSID 應共用同一份 config`);
    }
    // 程序身分必須換新：第一輪已收尾的程序不得充當第二輪證據。
    if (selfPids.every(x => x !== null) && new Set(selfPids).size !== selfPids.length) {
      violations.push(`[cross] 兩輪的 CLI pid 相同（${JSON.stringify(selfPids)}）——第二輪不是新啟動的程序`);
    }
    if (childPids.every(x => x !== null) && new Set(childPids).size !== childPids.length) {
      violations.push(`[cross] 兩輪的 MCP 子程序 pid 相同（${JSON.stringify(childPids)}）——第二輪沒有新的 mcp-approval`);
    }
    // approval id 必須不同（broker 每次 newULID）。
    const ids = agreedApprovalIds.filter((x): x is string => typeof x === 'string' && x !== '');
    if (ids.length === expected.length && new Set(ids).size !== ids.length) {
      violations.push(`[cross] 兩輪的 approval id 相同（${JSON.stringify(ids)}）——整份第一輪證據被當成第二輪`);
    }
    // 可辨識內容必須不同（這是「沿用第一輪證據」最直接的照妖鏡）。
    const markers = expected.map(r => r.approval.inputMarker);
    const texts = expected.map(r => r.approval.completionText);
    if (new Set(markers).size !== markers.length || new Set(texts).size !== texts.length) {
      violations.push('[cross] 核定期望本身的兩輪 marker／完成文字重複——builder 失去辨識力，拒絕通過');
    }
  }

  // --- S 的 App 端綁定 -----------------------------------------------------
  const S = i.expectation.sessionId;
  if (i.appBoundResume !== S) {
    violations.push(
      `[app] workspace registry 的 resume_session_id（${JSON.stringify(i.appBoundResume)}）`
      + `不等於核定的 S（${S}）——App 沒有把第一輪宣告的 session 綁成續聊身分`);
  }
  if (i.registryBinding === null) {
    violations.push(`[app] claude registry（sessions.json）沒有 ${S} 的綁定`);
  } else {
    if (i.registryBinding.wsid !== (i.domWsid ?? '')) {
      violations.push(
        `[app] registry 綁定的 WSID（${i.registryBinding.wsid}）與 UI 觀察到的（${JSON.stringify(i.domWsid)}）不符`);
    }
    if (i.registryBinding.cwd !== i.canonicalCwd) {
      violations.push(
        `[app] registry 綁定的 cwd（${i.registryBinding.cwd}）不等於 canonical workspace（${i.canonicalCwd}）`);
    }
  }

  const uniqCfg = [...new Set(configPaths)];
  return {
    violations,
    agreedApprovalIds,
    sharedMcpConfigPath: uniqCfg.length === 1 ? uniqCfg[0] : null,
  };
}
