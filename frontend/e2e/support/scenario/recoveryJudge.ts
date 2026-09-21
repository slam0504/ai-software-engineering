// B3a-2b-2 Task E1（R1 修正）：two-round（start→resume）recovery 的共用嚴格
// 判定模組。
//
// 背景（reviewer 用工作樹裡原樣抽出的區域 judge 實測，四個反例全部 PASS）：
// 先前版本把 `judgeRecoverySequence` 定義在 fakeAppServer.selftest.ts 內部，
// 沒有匯出、只有 fake 自己的正控制在用，而且判定本身不合格——
//   (a) approval method 改成 evil/requestApproval 仍過（method 檢查只用
//       `.includes('requestApproval')`，不是精確核對）；
//   (b) initialize response 加上 error、原 result 仍保留，仍過（沒有核對
//       response 不該帶 error 的形狀）；
//   (c) turn/completed 的 status 改成 failed 仍過（沒有核對 turn.status）；
//   (d) 既有正控制本來就沒有兩輪的 afterApproval 內容事件，卻仍 PASS——因為
//       judge 用 `while (e && e.dir === 's2c' && method !== 'turn/completed')`
//       略過所有未知 s2c 事件，等於完全沒驗內容。
//
// 本模組是獨立、真正共用的判定：fakeAppServer.selftest.ts 的正控制與未來
// E2 的 browser spec 都應該 import 這裡，不再各自手寫或半套用舊 judge。
// 嚴格程度對齊 scenarioProtocolJudge.ts#judgeFullProtocol 的
// assertRequestShape／assertResponseShape／assertNotificationShape 慣例
// （request 不該帶 result／error；response 不該帶 method；notification 不該
// 帶 id／result／error）——本檔獨立複製一份極小版本，不 import 該檔（授權
// 範圍只列本檔為新增，不含修改 scenarioProtocolJudge.ts）。
//
// 判定範圍：完整核對方向、method、id 型別與值、params／result 內容、
// afterApproval 的順序與內容、turnStatus，拒絕多餘欄位、截斷、重複／跨輪
// ID 重用；序列核對通過後仍有殘留 wire frame 一律判失敗。「能合法解析」
// （parseRunLog 不丟例外）從來不是本模組的成功判準。
import type { Frame, Manifest, RunLogEntry } from './protocol.ts';
import { Method } from './protocol.ts';
// 缺陷 2 修正：judgeRecoveryManifest 只 import 凍結的 judgeApproval，不修改
// verify.ts／verify.selftest.ts——round1（頂層欄位）的「已結束、exitCode=0、
// 無 fatal／unknown」核對直接複用既有判定，不在本檔重寫一份，round2
// （secondTurn）沒有獨立的 exitCode／endedAt 可核對（manifest 全域只有一份
// process 收尾狀態），因此沿用頂層欄位的 judgeApproval 結果即代表整個 run
// 是否成功收尾，round2 只再核對 secondTurn 自己的 identity／resumeAccepted。
import { judgeApproval } from './verify.ts';

export interface RecoveryRoundExpectation {
  turnId: string;
  itemId: string;
  approvalRequestId: string;
  decision: 'accept' | 'decline';
  afterApproval: Array<{ type: 'itemStarted' | 'itemCompleted'; text: string }>;
  turnStatus: 'completed' | 'failed';
}

export interface RecoveryExpectation {
  threadId: string;
  // approvalMethod：兩輪固定沿用同一個 method（協定契約 #5），因此是頂層
  // 單一欄位，不是每輪各自一個。
  approvalMethod: string;
  round1: RecoveryRoundExpectation;
  round2: RecoveryRoundExpectation;
}

function assertRequestShape(f: Frame): string | null {
  if (f.result !== undefined) return `request 不應帶 result，實際 ${JSON.stringify(f.result)}`;
  if (f.error !== undefined) return `request 不應帶 error，實際 ${JSON.stringify(f.error)}`;
  if (f.id === undefined) return 'request 應帶 id';
  return null;
}
function assertResponseShape(f: Frame): string | null {
  if (f.method !== undefined) return `response 不應帶 method，實際 ${JSON.stringify(f.method)}`;
  if (f.params !== undefined) return `response 不應帶 params，實際 ${JSON.stringify(f.params)}`;
  if (f.error !== undefined) return `response 帶 error：${JSON.stringify(f.error)}`;
  if (f.result === undefined) return 'response 缺少 result 欄位';
  return null;
}
function assertNotificationShape(f: Frame): string | null {
  if (f.id !== undefined) return `notification 不應帶 id，實際 ${JSON.stringify(f.id)}`;
  if (f.result !== undefined) return `notification 不應帶 result，實際 ${JSON.stringify(f.result)}`;
  if (f.error !== undefined) return `notification 不應帶 error，實際 ${JSON.stringify(f.error)}`;
  return null;
}

// idMatches：嚴格型別＋值比對（`===`），不接受寬鬆轉型比較——number 1 跟
// 字串 "1" 不算相等，用來抓「回應 id 型別被悄悄轉掉」這類反例。
function idMatches(a: unknown, b: unknown): boolean {
  return typeof a === typeof b && a === b;
}

export function judgeRecoverySequence(log: RunLogEntry[], expected: RecoveryExpectation): string[] {
  const violations: string[] = [];
  const wire = log.filter(e => (e.dir === 'c2s' || e.dir === 's2c') && e.frame);
  let cursor = 0;

  function expectFrame(label: string, dir: 'c2s' | 's2c', check: (f: Frame) => string | null): boolean {
    const entry = wire[cursor];
    if (!entry) {
      violations.push(`sequence truncated: expected ${label}, got end of log`);
      return false;
    }
    if (entry.dir !== dir) {
      violations.push(`${label}: expected dir=${dir}, got ${entry.dir} (seq=${entry.seq})`);
      return false;
    }
    const f = entry.frame as Frame;
    const err = check(f);
    if (err) {
      violations.push(`${label}: ${err}`);
      return false;
    }
    cursor += 1;
    return true;
  }

  let initId: unknown;
  let ok = expectFrame('initialize request', 'c2s', f => {
    const shapeErr = assertRequestShape(f);
    if (shapeErr) return shapeErr;
    if (f.method !== Method.Initialize) return `expected method=${Method.Initialize}, got ${JSON.stringify(f.method)}`;
    initId = f.id;
    return null;
  });
  if (ok) {
    ok = expectFrame('initialize response', 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, initId)) return `expected id=${JSON.stringify(initId)}, got ${JSON.stringify(f.id)}`;
      return null;
    });
  }
  if (ok) {
    ok = expectFrame('initialized notification', 'c2s', f => {
      const shapeErr = assertNotificationShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.Initialized) return `expected method=${Method.Initialized}, got ${JSON.stringify(f.method)}`;
      return null;
    });
  }
  if (!ok) return violations;

  function expectThreadEntry(method: string, label: string): boolean {
    let id: unknown;
    const reqOk = expectFrame(`${label} request`, 'c2s', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== method) return `expected method=${method}, got ${JSON.stringify(f.method)}`;
      const params = f.params as { threadId?: unknown } | undefined;
      if (method === Method.ThreadResume && params?.threadId !== expected.threadId) {
        return `threadId mismatch: got ${JSON.stringify(params?.threadId)} want ${JSON.stringify(expected.threadId)}`;
      }
      id = f.id;
      return null;
    });
    if (!reqOk) return false;
    return expectFrame(`${label} response`, 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, id)) return `expected id=${JSON.stringify(id)}, got ${JSON.stringify(f.id)}`;
      const result = f.result as { thread?: { id?: unknown } } | undefined;
      if (result?.thread?.id !== expected.threadId) {
        return `thread.id mismatch: got ${JSON.stringify(result?.thread?.id)} want ${JSON.stringify(expected.threadId)}`;
      }
      return null;
    });
  }

  function expectTurnAndApproval(round: RecoveryRoundExpectation, label: string): boolean {
    let turnReqId: unknown;
    let stepOk = expectFrame(`${label} turn/start request`, 'c2s', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.TurnStart) return `expected method=${Method.TurnStart}, got ${JSON.stringify(f.method)}`;
      const params = f.params as { threadId?: unknown } | undefined;
      if (params?.threadId !== expected.threadId) {
        return `turn/start threadId mismatch: got ${JSON.stringify(params?.threadId)} want ${JSON.stringify(expected.threadId)}`;
      }
      turnReqId = f.id;
      return null;
    });
    if (!stepOk) return false;

    stepOk = expectFrame(`${label} turn/start response`, 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, turnReqId)) return `expected id=${JSON.stringify(turnReqId)}, got ${JSON.stringify(f.id)}`;
      const result = f.result as { turn?: { id?: unknown; status?: unknown } } | undefined;
      if (result?.turn?.id !== round.turnId || result?.turn?.status !== 'inProgress') {
        return `expected turn.id=${JSON.stringify(round.turnId)} status=inProgress, got ${JSON.stringify(result?.turn)}`;
      }
      return null;
    });
    if (!stepOk) return false;

    stepOk = expectFrame(`${label} approval request`, 's2c', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      // (a) 反例：method 改成 evil/requestApproval 之類——精確核對，不是
      // `.includes('requestApproval')`。
      if (f.method !== expected.approvalMethod) return `expected method=${expected.approvalMethod}, got ${JSON.stringify(f.method)}`;
      if (!idMatches(f.id, round.approvalRequestId)) {
        return `expected id=${JSON.stringify(round.approvalRequestId)} (type ${typeof round.approvalRequestId}), `
          + `got ${JSON.stringify(f.id)} (type ${typeof f.id})`;
      }
      const params = f.params as { threadId?: unknown; turnId?: unknown; itemId?: unknown } | undefined;
      if (params?.threadId !== expected.threadId || params?.turnId !== round.turnId || params?.itemId !== round.itemId) {
        return `approval request identity mismatch: ${JSON.stringify(params)}`;
      }
      return null;
    });
    if (!stepOk) return false;

    stepOk = expectFrame(`${label} approval response`, 'c2s', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, round.approvalRequestId)) return `expected id=${JSON.stringify(round.approvalRequestId)}, got ${JSON.stringify(f.id)}`;
      const result = f.result as { decision?: unknown } | undefined;
      if (result?.decision !== round.decision) return `expected decision=${round.decision}, got ${JSON.stringify(result?.decision)}`;
      return null;
    });
    if (!stepOk) return false;

    // (d) 反例：afterApproval 逐一核對順序與內容，不略過任何未知 s2c 事件。
    for (const ev of round.afterApproval) {
      const evMethod = ev.type === 'itemStarted' ? Method.ItemStarted : Method.ItemCompleted;
      stepOk = expectFrame(`${label} ${evMethod}`, 's2c', f => {
        const shapeErr = assertNotificationShape(f);
        if (shapeErr) return shapeErr;
        if (f.method !== evMethod) return `expected method=${evMethod}, got ${JSON.stringify(f.method)}`;
        const params = f.params as {
          threadId?: unknown;
          turnId?: unknown;
          item?: { id?: unknown; text?: unknown; type?: unknown };
        } | undefined;
        if (params?.threadId !== expected.threadId) return `params.threadId mismatch: ${JSON.stringify(params?.threadId)}`;
        if (params?.turnId !== round.turnId) return `params.turnId mismatch: ${JSON.stringify(params?.turnId)}`;
        if (params?.item?.id !== round.itemId) return `item.id mismatch: ${JSON.stringify(params?.item?.id)}`;
        // 缺陷 1 修正：內容事件必須是 agentMessage，不接受其他 item.type
        // （reviewer 反例：改成 commandExecution 仍 PASS）。
        if (params?.item?.type !== 'agentMessage') {
          return `item.type mismatch: expected "agentMessage", got ${JSON.stringify(params?.item?.type)}`;
        }
        if (params?.item?.text !== ev.text) {
          return `item.text mismatch: expected ${JSON.stringify(ev.text)}, got ${JSON.stringify(params?.item?.text)}`;
        }
        return null;
      });
      if (!stepOk) return false;
    }

    // (c) 反例：turn/completed 的 status 核對，不是只核對 turn.id。
    stepOk = expectFrame(`${label} turn/completed`, 's2c', f => {
      const shapeErr = assertNotificationShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.TurnCompleted) return `expected method=${Method.TurnCompleted}, got ${JSON.stringify(f.method)}`;
      const params = f.params as { threadId?: unknown; turn?: { id?: unknown; status?: unknown } } | undefined;
      if (params?.threadId !== expected.threadId) return `params.threadId mismatch: ${JSON.stringify(params?.threadId)}`;
      if (params?.turn?.id !== round.turnId || params?.turn?.status !== round.turnStatus) {
        return `expected turn.id=${JSON.stringify(round.turnId)} status=${JSON.stringify(round.turnStatus)}, got ${JSON.stringify(params?.turn)}`;
      }
      return null;
    });
    return stepOk;
  }

  if (!expectThreadEntry(Method.ThreadStart, 'round1 thread/start')) return violations;
  if (!expectTurnAndApproval(expected.round1, 'round1')) return violations;
  if (!expectThreadEntry(Method.ThreadResume, 'round2 thread/resume')) return violations;
  if (!expectTurnAndApproval(expected.round2, 'round2')) return violations;

  if (cursor < wire.length) {
    const extra = wire.slice(cursor);
    violations.push(
      `sequence has ${extra.length} extra wire frame(s) after expected two-round completion: `
      + extra.map(e => `seq=${e.seq} dir=${e.dir} method=${JSON.stringify(e.frame?.method)} id=${JSON.stringify(e.frame?.id)}`).join('; '),
    );
  }
  return violations;
}

// judgeRecoveryManifest：manifest 層的獨立判定——第一輪必須停留在頂層欄位
// （decisionReceived／approvalRequestId／approvalMethod），第二輪必須落在
// `manifest.secondTurn` 子物件且 `resumeAccepted === true`，兩者的
// approvalRequestId 不得相同（identity 重用）。單輪場景（round2 為 null）
// 則要求 `manifest.secondTurn` 明確是 `null`。不是只在正控制裡手寫 assert，
// 讓 fake 的正控制與未來 E2 都走同一套判定。
export interface RecoveryManifestExpectation {
  approvalMethod: string;
  round1: { approvalRequestId: string; decision: 'accept' | 'decline' };
  round2: { approvalRequestId: string; decision: 'accept' | 'decline' } | null;
}

export function judgeRecoveryManifest(manifest: Manifest, expected: RecoveryManifestExpectation): string[] {
  // 缺陷 2 修正：round1／run-level 的核對直接組合既有 judgeApproval——它同時
  // 核對 endedAt／exitCode／fatalError／unknownMethodsSeen 與 identity 三欄位
  // （approvalRequestId／approvalMethod／decisionReceived），reviewer 反例
  // （exitCode=17、fatalError 非 null、unknownMethodsSeen 非空、endedAt=null）
  // 先前完全沒被核對到，這裡不再自己重寫一份，只加上訊息前綴標明是哪一輪。
  const approvalViolations = judgeApproval(manifest, {
    requestId: expected.round1.approvalRequestId,
    method: expected.approvalMethod,
    decision: expected.round1.decision,
  });
  const violations: string[] = approvalViolations.map(v => `round1/run-level: ${v}`);

  if (expected.round2 === null) {
    if (manifest.secondTurn !== null) {
      violations.push(`manifest.secondTurn must be null for single-turn scenarios, got ${JSON.stringify(manifest.secondTurn)}`);
    }
    return violations;
  }

  if (!manifest.secondTurn) {
    violations.push('manifest.secondTurn must be populated for two-round scenarios');
    return violations;
  }
  if (manifest.secondTurn.approvalMethod !== expected.approvalMethod) {
    violations.push(`manifest.secondTurn.approvalMethod mismatch: expected ${JSON.stringify(expected.approvalMethod)}, got ${JSON.stringify(manifest.secondTurn.approvalMethod)}`);
  }
  if (manifest.secondTurn.approvalRequestId !== expected.round2.approvalRequestId) {
    violations.push(
      `manifest.secondTurn.approvalRequestId mismatch: expected ${JSON.stringify(expected.round2.approvalRequestId)}, `
      + `got ${JSON.stringify(manifest.secondTurn.approvalRequestId)}`,
    );
  }
  if (manifest.secondTurn.decisionReceived !== expected.round2.decision) {
    violations.push(
      `manifest.secondTurn.decisionReceived mismatch: expected ${JSON.stringify(expected.round2.decision)}, `
      + `got ${JSON.stringify(manifest.secondTurn.decisionReceived)}`,
    );
  }
  if (manifest.secondTurn.resumeAccepted !== true) {
    violations.push(`manifest.secondTurn.resumeAccepted must be true, got ${JSON.stringify(manifest.secondTurn.resumeAccepted)}`);
  }
  if (manifest.secondTurn.approvalRequestId === expected.round1.approvalRequestId) {
    violations.push('manifest.secondTurn.approvalRequestId must differ from round1 approvalRequestId (identity reuse)');
  }
  return violations;
}
