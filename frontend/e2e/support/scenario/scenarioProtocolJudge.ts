// B3a-2b-2 Task C 驗收缺口修正（缺口 3）：完整 protocol 判定。
//
// 背景（reviewer 反例，`/tmp/b3a2b-review223-4i54VG/truncated-wire.jsonl`）：
// 既有判定鏈（scenarioTripwire.ts 只看 argv、spec 只 `find` approval request／
// response、verify.ts 的 parseRunLog 只保證 seq 遞增）合起來**仍然接受**一份
// 只保留 approval decision 那兩行、砍掉 initialize／thread/start／turn/start／
// afterApproval／turn/completed 的截斷 log——因為沒有任何一層核對「完整必要
// 步驟序列＋方向」。
//
// 本檔新增一個獨立的 scenario 層純判定模組（不是 verify.ts 的一部分——
// verify.ts 是 B3a-2b-1 交付、凍結不改）：`judgeFullProtocol` 用游標依序核對
// c2s／s2c 每一步的方向、method、id 型別與值、params 內容，任何一步不符（含
// 缺失、錯序、未知 method、id 型別被寬鬆轉換）都會在該步驟直接回報並停止；
// 全部步驟核對通過後，序列裡不得再殘留任何多餘／重複的 c2s／s2c 訊息。
//
// `judgeRunIdentity` 補另一層：manifest／落地的 scenario-config.json／記憶體
// 中的 ScenarioConfig 三者互相核對，並確認 identity 字串（threadId／turnId／
// itemId／approvalRequestId）都帶有本次 run 的 identity 後綴——避免讀到跨
// 執行殘留的證據卻被誤判為本次通過。
import type { Frame, Manifest, RunLogEntry, ScenarioConfig } from './protocol.ts';
import { Method } from './protocol.ts';

export interface ProtocolJudgeExpectation {
  cfg: ScenarioConfig;
  decision: 'accept' | 'decline';
}

type FrameCheck = (f: Frame) => string | null; // null＝通過，否則是違規訊息

// judgeFullProtocol：核對本案完整必要步驟序列（含方向）。任何缺失／錯序／
// 未知 method／id 型別或值不符，一律判失敗（非空 violations）。
export function judgeFullProtocol(entries: RunLogEntry[], exp: ProtocolJudgeExpectation): string[] {
  const cfg = exp.cfg;
  const wire = entries.filter(e => e.dir === 'c2s' || e.dir === 's2c');
  const violations: string[] = [];

  let cursor = 0;
  const captured: { initId?: unknown; threadId?: unknown; turnId?: unknown } = {};

  function expectFrame(label: string, dir: 'c2s' | 's2c', check: FrameCheck): boolean {
    const entry = wire[cursor];
    if (!entry) {
      violations.push(`protocol 序列缺失：預期「${label}」，但訊息序列已結束（缺失訊息）`);
      return false;
    }
    if (entry.dir !== dir) {
      violations.push(
        `protocol 序列方向錯誤：預期「${label}」方向=${dir}，實際 seq=${entry.seq} dir=${entry.dir}`
        + `（錯序或方向不符，frame=${JSON.stringify(entry.frame ?? null)}）`,
      );
      return false;
    }
    const f = entry.frame ?? {};
    const err = check(f);
    if (err) {
      violations.push(`protocol 序列不符：預期「${label}」（seq=${entry.seq}）：${err}`);
      return false;
    }
    cursor += 1;
    return true;
  }

  // 嚴格型別＋值比對：id 用 `===`（不接受 String() 之類的寬鬆轉型比較）。
  function idMatches(a: unknown, b: unknown): boolean {
    return typeof a === typeof b && a === b;
  }

  // B3a-2b-2 Task C 第三輪限縮補正（缺陷 3）：frame 形狀守門。先前的檢查只
  // 核對個別欄位的值（method 是否等於預期字串、id 是否相符），從未排除
  // 「response 不該帶的 method／params」或「request／notification 不該帶的
  // result／error」——controller 實測重現：合法 initialize response 硬塞一個
  // 陌生 `method`，或砍掉 `result` 只留 `{id}`，兩者都仍然 0 violations。
  // 這裡補上三種角色各自的形狀守門，在既有的個別欄位檢查之前執行。
  function assertResponseShape(f: Frame): string | null {
    if (f.method !== undefined) return `response 不應帶 method，實際 ${JSON.stringify(f.method)}`;
    if (f.params !== undefined) return `response 不應帶 params，實際 ${JSON.stringify(f.params)}`;
    if (f.error !== undefined) return `response 帶 error：${JSON.stringify(f.error)}`;
    if (f.result === undefined) return 'response 缺少 result 欄位';
    return null;
  }
  function assertRequestShape(f: Frame): string | null {
    if (f.result !== undefined) return `request 不應帶 result，實際 ${JSON.stringify(f.result)}`;
    if (f.error !== undefined) return `request 不應帶 error，實際 ${JSON.stringify(f.error)}`;
    return null;
  }
  function assertNotificationShape(f: Frame): string | null {
    if (f.result !== undefined) return `notification 不應帶 result，實際 ${JSON.stringify(f.result)}`;
    if (f.error !== undefined) return `notification 不應帶 error，實際 ${JSON.stringify(f.error)}`;
    return null;
  }

  const expectedThreadMethod = cfg.threadMode === 'resume' ? Method.ThreadResume : Method.ThreadStart;

  const stepsOk =
    expectFrame('c2s initialize', 'c2s', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.Initialize) return `method 應為 ${Method.Initialize}，實際 ${JSON.stringify(f.method)}`;
      if (f.id === undefined) return 'initialize 應帶 id';
      captured.initId = f.id;
      return null;
    })
    && expectFrame('s2c initialize response', 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, captured.initId)) return `response id 應嚴格等於 initialize 的 id（${JSON.stringify(captured.initId)}），實際 ${JSON.stringify(f.id)}`;
      return null;
    })
    && expectFrame('c2s initialized', 'c2s', f => {
      const shapeErr = assertNotificationShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.Initialized) return `method 應為 ${Method.Initialized}，實際 ${JSON.stringify(f.method)}`;
      if (f.id !== undefined) return 'initialized 是 notification，不應帶 id';
      return null;
    })
    && expectFrame(`c2s ${expectedThreadMethod}`, 'c2s', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== expectedThreadMethod) return `method 應為 ${expectedThreadMethod}，實際 ${JSON.stringify(f.method)}`;
      if (f.id === undefined) return `${expectedThreadMethod} 應帶 id`;
      captured.threadId = f.id;
      if (cfg.threadMode === 'resume') {
        const params = f.params as { threadId?: unknown } | undefined;
        if (params?.threadId !== cfg.threadId) return `resume params.threadId 應為 ${JSON.stringify(cfg.threadId)}，實際 ${JSON.stringify(params?.threadId)}`;
      }
      return null;
    })
    && expectFrame(`s2c ${expectedThreadMethod} response`, 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, captured.threadId)) return `response id 不符：預期 ${JSON.stringify(captured.threadId)}，實際 ${JSON.stringify(f.id)}`;
      const result = f.result as { thread?: { id?: unknown } } | undefined;
      if (result?.thread?.id !== cfg.threadId) return `thread.id 應為 ${JSON.stringify(cfg.threadId)}，實際 ${JSON.stringify(result?.thread?.id)}`;
      return null;
    })
    && expectFrame('c2s turn/start', 'c2s', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== Method.TurnStart) return `method 應為 ${Method.TurnStart}，實際 ${JSON.stringify(f.method)}`;
      if (f.id === undefined) return 'turn/start 應帶 id';
      captured.turnId = f.id;
      const params = f.params as { threadId?: unknown } | undefined;
      if (params?.threadId !== cfg.threadId) return `params.threadId 應為 ${JSON.stringify(cfg.threadId)}，實際 ${JSON.stringify(params?.threadId)}`;
      return null;
    })
    && expectFrame('s2c turn/start response', 's2c', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, captured.turnId)) return `response id 不符：預期 ${JSON.stringify(captured.turnId)}，實際 ${JSON.stringify(f.id)}`;
      const result = f.result as { turn?: { id?: unknown } } | undefined;
      if (result?.turn?.id !== cfg.turnId) return `turn.id 應為 ${JSON.stringify(cfg.turnId)}，實際 ${JSON.stringify(result?.turn?.id)}`;
      return null;
    })
    && expectFrame(`s2c ${cfg.approvalMethod}（requestApproval）`, 's2c', f => {
      const shapeErr = assertRequestShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== cfg.approvalMethod) return `method 應為 ${cfg.approvalMethod}，實際 ${JSON.stringify(f.method)}`;
      if (!idMatches(f.id, cfg.approvalRequestId)) {
        return `approval request id 應嚴格等於 ${JSON.stringify(cfg.approvalRequestId)}（型別 ${typeof cfg.approvalRequestId}），`
          + `實際 ${JSON.stringify(f.id)}（型別 ${typeof f.id}）`;
      }
      const params = f.params as { threadId?: unknown; turnId?: unknown; itemId?: unknown } | undefined;
      if (params?.threadId !== cfg.threadId) return `params.threadId 不符：預期 ${JSON.stringify(cfg.threadId)}，實際 ${JSON.stringify(params?.threadId)}`;
      if (params?.turnId !== cfg.turnId) return `params.turnId 不符：預期 ${JSON.stringify(cfg.turnId)}，實際 ${JSON.stringify(params?.turnId)}`;
      if (params?.itemId !== cfg.itemId) return `params.itemId 不符：預期 ${JSON.stringify(cfg.itemId)}，實際 ${JSON.stringify(params?.itemId)}`;
      return null;
    })
    && expectFrame('c2s approval decision response', 'c2s', f => {
      const shapeErr = assertResponseShape(f);
      if (shapeErr) return shapeErr;
      if (!idMatches(f.id, cfg.approvalRequestId)) {
        return `decision response id 應嚴格等於 ${JSON.stringify(cfg.approvalRequestId)}，實際 ${JSON.stringify(f.id)}`;
      }
      const result = f.result as { decision?: string } | undefined;
      if (result?.decision !== exp.decision) return `decision 應為 ${JSON.stringify(exp.decision)}，實際 ${JSON.stringify(result?.decision)}`;
      return null;
    });

  if (!stepsOk) return violations;

  for (const ev of cfg.afterApproval) {
    const evMethod = ev.type === 'itemStarted' ? Method.ItemStarted : Method.ItemCompleted;
    const ok = expectFrame(`s2c ${evMethod}`, 's2c', f => {
      const shapeErr = assertNotificationShape(f);
      if (shapeErr) return shapeErr;
      if (f.method !== evMethod) return `method 應為 ${evMethod}，實際 ${JSON.stringify(f.method)}`;
      if (f.id !== undefined) return `${evMethod} 是 notification，不應帶 id`;
      const params = f.params as {
        threadId?: unknown; turnId?: unknown; item?: { id?: unknown; text?: unknown };
      } | undefined;
      if (params?.threadId !== cfg.threadId) return `params.threadId 不符：${JSON.stringify(params?.threadId)}`;
      if (params?.turnId !== cfg.turnId) return `params.turnId 不符：${JSON.stringify(params?.turnId)}`;
      if (params?.item?.id !== cfg.itemId) return `item.id 不符：${JSON.stringify(params?.item?.id)}`;
      if (params?.item?.text !== ev.text) return `item.text 不符：預期 ${JSON.stringify(ev.text)}，實際 ${JSON.stringify(params?.item?.text)}`;
      return null;
    });
    if (!ok) return violations;
  }

  const completedOk = expectFrame('s2c turn/completed', 's2c', f => {
    const shapeErr = assertNotificationShape(f);
    if (shapeErr) return shapeErr;
    if (f.method !== Method.TurnCompleted) return `method 應為 ${Method.TurnCompleted}，實際 ${JSON.stringify(f.method)}`;
    if (f.id !== undefined) return 'turn/completed 是 notification，不應帶 id';
    const params = f.params as { threadId?: unknown; turn?: { id?: unknown; status?: unknown } } | undefined;
    if (params?.threadId !== cfg.threadId) return `params.threadId 不符：${JSON.stringify(params?.threadId)}`;
    if (params?.turn?.id !== cfg.turnId) return `turn.id 不符：${JSON.stringify(params?.turn?.id)}`;
    if (params?.turn?.status !== cfg.turnStatus) return `turn.status 不符：預期 ${JSON.stringify(cfg.turnStatus)}，實際 ${JSON.stringify(params?.turn?.status)}`;
    return null;
  });
  if (!completedOk) return violations;

  // 完整必要序列核對通過後，不得再殘留任何多餘／重複的 c2s／s2c 訊息。
  if (cursor < wire.length) {
    const extra = wire.slice(cursor);
    violations.push(
      `完整必要序列核對通過後，仍有 ${extra.length} 筆多餘／重複的 c2s／s2c 訊息未預期出現：`
      + extra.map(e => `seq=${e.seq} dir=${e.dir} method=${JSON.stringify(e.frame?.method)} id=${JSON.stringify(e.frame?.id)}`).join('；'),
    );
  }

  return violations;
}

export interface RunIdentityExpectation {
  runId: string;
  cfg: ScenarioConfig;
  decision: 'accept' | 'decline';
}

// judgeRunIdentity：manifest／落地的 scenario-config.json／記憶體中的
// ScenarioConfig 互相核對，並確認 identity 字串帶有本次 run 的後綴——避免
// 「檔案讀得到、內容自洽，但其實是別次執行留下的殘留」被誤判為本次證據。
export function judgeRunIdentity(
  manifest: Manifest,
  scenarioConfigOnDisk: unknown,
  exp: RunIdentityExpectation,
): string[] {
  const violations: string[] = [];
  const { runId, cfg, decision } = exp;

  if (typeof scenarioConfigOnDisk !== 'object' || scenarioConfigOnDisk === null || Array.isArray(scenarioConfigOnDisk)) {
    violations.push('scenario-config.json 落地內容不是 JSON 物件');
  } else {
    const disk = scenarioConfigOnDisk as Record<string, unknown>;
    const fields: Array<[string, unknown, unknown]> = [
      ['scenario', disk.scenario, cfg.scenario],
      ['threadId', disk.threadId, cfg.threadId],
      ['turnId', disk.turnId, cfg.turnId],
      ['itemId', disk.itemId, cfg.itemId],
      ['threadMode', disk.threadMode, cfg.threadMode],
      ['approvalMethod', disk.approvalMethod, cfg.approvalMethod],
      ['approvalRequestId', disk.approvalRequestId, cfg.approvalRequestId],
      ['turnStatus', disk.turnStatus, cfg.turnStatus],
    ];
    for (const [name, diskVal, memVal] of fields) {
      if (diskVal !== memVal) {
        violations.push(
          `scenario-config.json 落地內容與記憶體中的 ScenarioConfig 不符（欄位 ${name}）：`
          + `落地=${JSON.stringify(diskVal)} 記憶體=${JSON.stringify(memVal)}`,
        );
      }
    }
    // afterApproval 是陣列（B3a-2b-2 Task C 第三輪限縮補正，缺陷 2 的一部分）
    // ——不能用 `!==`（reference 比較對陣列永遠不等），改用序列化後比對內容；
    // 目的是讓「wire 被砍掉 item 事件、落地 config 的 afterApproval 也被竄改
    // 成 []」這種成對竄改能被抓到（見 scenarioProtocolJudge.selftest.ts 的
    // 成對負控制）。
    const diskAfterApproval = JSON.stringify(disk.afterApproval);
    const expectedAfterApproval = JSON.stringify(cfg.afterApproval);
    if (diskAfterApproval !== expectedAfterApproval) {
      violations.push(
        `scenario-config.json 落地內容與記憶體中的 ScenarioConfig 不符（欄位 afterApproval）：`
        + `落地=${diskAfterApproval} 記憶體=${expectedAfterApproval}`,
      );
    }
  }

  if (manifest.scenario !== cfg.scenario) {
    violations.push(`manifest.scenario 不符：manifest=${JSON.stringify(manifest.scenario)} config=${JSON.stringify(cfg.scenario)}`);
  }
  if (manifest.approvalMethod !== cfg.approvalMethod) {
    violations.push(`manifest.approvalMethod 不符：manifest=${JSON.stringify(manifest.approvalMethod)} config=${JSON.stringify(cfg.approvalMethod)}`);
  }
  if (manifest.approvalRequestId !== cfg.approvalRequestId) {
    violations.push(`manifest.approvalRequestId 不符：manifest=${JSON.stringify(manifest.approvalRequestId)} config=${JSON.stringify(cfg.approvalRequestId)}`);
  }
  if (manifest.decisionReceived !== decision) {
    violations.push(`manifest.decisionReceived 不符：manifest=${JSON.stringify(manifest.decisionReceived)} 預期=${JSON.stringify(decision)}`);
  }

  const runIdSuffix = `-${runId}`;
  const idFields: Array<[string, string]> = [
    ['threadId', cfg.threadId],
    ['turnId', cfg.turnId],
    ['itemId', cfg.itemId],
    ['approvalRequestId', cfg.approvalRequestId],
  ];
  for (const [name, val] of idFields) {
    if (!val.endsWith(runIdSuffix)) {
      violations.push(`${name}（${JSON.stringify(val)}）未帶本次 run 的 identity 後綴 ${JSON.stringify(runIdSuffix)}——可能讀到跨執行殘留的證據`);
    }
  }

  return violations;
}
