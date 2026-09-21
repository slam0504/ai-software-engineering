// scenarioProtocolJudge.ts 的純函式測試（B3a-2b-2 Task C 驗收缺口修正，缺口
// 3）——重點是**負控制**：用固定的 fixture JSONL（截斷、錯序、錯 ID、錯
// decision、未知 method）證明 `judgeFullProtocol`／`judgeRunIdentity` 真的會
// 抓到，不是永遠回 [] 的空殼。不需要真跑 browser；固定 fixture 用
// `parseRunLog`（B3a-2b-1 交付、凍結不改的 verify.ts）解析後餵給本檔的判定
// 函式，走跟 spec 實際使用時相同的資料路徑。
//
// 執行：node frontend/e2e/support/scenario/scenarioProtocolJudge.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { Frame, Manifest, ScenarioConfig } from './protocol.ts';
import { Method } from './protocol.ts';
import { parseRunLog } from './verify.ts';
import { judgeFullProtocol, judgeRunIdentity } from './scenarioProtocolJudge.ts';

let passed = 0;
let failed = 0;
function check(name: string, fn: () => void): void {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-protocol-judge-selftest-'));

const runId = 'run123';
const cfg: ScenarioConfig = {
  scenario: 'commandExecution-allow',
  threadId: `b3a2b2-thread-${runId}`,
  turnId: `b3a2b2-turn-${runId}`,
  itemId: `b3a2b2-item-${runId}`,
  threadMode: 'start',
  approvalMethod: Method.CmdExecRequestApproval,
  approvalRequestId: `b3a2b2-approval-${runId}`,
  afterApproval: [
    { type: 'itemStarted', text: `b3a2b2-scenario-content-${runId}` },
    { type: 'itemCompleted', text: `b3a2b2-scenario-content-${runId}` },
  ],
  turnStatus: 'completed',
};

// buildValidWire：組出一份完全符合 cfg 的正控制 wire（c2s／s2c 交錯，方向與
// judgeFullProtocol 期望的序列一一對應）。回傳 { dir, frame } 陣列，呼叫端自
// 行編號 seq／ts 落成 JSONL。
function buildValidWire(): Array<{ dir: 'c2s' | 's2c'; frame: Frame }> {
  return [
    { dir: 'c2s', frame: { id: 1, method: Method.Initialize, params: {} } },
    { dir: 's2c', frame: { id: 1, result: {} } },
    { dir: 'c2s', frame: { method: Method.Initialized } },
    { dir: 'c2s', frame: { id: 2, method: Method.ThreadStart, params: {} } },
    { dir: 's2c', frame: { id: 2, result: { thread: { id: cfg.threadId } } } },
    { dir: 'c2s', frame: { id: 3, method: Method.TurnStart, params: { threadId: cfg.threadId } } },
    { dir: 's2c', frame: { id: 3, result: { turn: { id: cfg.turnId, status: 'inProgress' } } } },
    {
      dir: 's2c',
      frame: {
        id: cfg.approvalRequestId,
        method: cfg.approvalMethod,
        params: { threadId: cfg.threadId, turnId: cfg.turnId, itemId: cfg.itemId, startedAtMs: 1 },
      },
    },
    { dir: 'c2s', frame: { id: cfg.approvalRequestId, result: { decision: 'accept' } } },
    {
      dir: 's2c',
      frame: {
        method: Method.ItemStarted,
        params: { threadId: cfg.threadId, turnId: cfg.turnId, item: { id: cfg.itemId, text: cfg.afterApproval[0].text } },
      },
    },
    {
      dir: 's2c',
      frame: {
        method: Method.ItemCompleted,
        params: { threadId: cfg.threadId, turnId: cfg.turnId, item: { id: cfg.itemId, text: cfg.afterApproval[1].text } },
      },
    },
    {
      dir: 's2c',
      frame: { method: Method.TurnCompleted, params: { threadId: cfg.threadId, turn: { id: cfg.turnId, status: 'completed' } } },
    },
  ];
}

function writeWireLog(name: string, wire: Array<{ dir: 'c2s' | 's2c'; frame: Frame }>): string {
  const p = path.join(tmpDir, name);
  const lines = wire.map((e, i) => JSON.stringify({ seq: i + 1, ts: `t${i + 1}`, dir: e.dir, frame: e.frame }));
  fs.writeFileSync(p, lines.join('\n') + '\n');
  return p;
}

const expectation = { cfg, decision: 'accept' as const };

// --- 正控制：完全符合的 wire 應該回傳空陣列 ---
check('judgeFullProtocol 正控制：完整合法序列回傳空陣列', () => {
  const p = writeWireLog('valid.jsonl', buildValidWire());
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.deepEqual(violations, []);
});

// --- 負控制：截斷（reviewer 反例的直接重現：只留 approval decision 附近兩行）---
check('judgeFullProtocol 負控制：截斷（只留 approval request／response）必須判失敗', () => {
  const wire = buildValidWire();
  const truncated = wire.slice(7, 9); // 只留 requestApproval + decision response
  const p = writeWireLog('truncated.jsonl', truncated);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for truncated wire log');
  assert.ok(violations.some(v => v.includes('initialize')), `expected missing-initialize violation, got: ${violations}`);
});

// --- 負控制：錯序（把 turn/start 與 thread/start 的 response 互換）---
check('judgeFullProtocol 負控制：錯序必須判失敗', () => {
  const wire = buildValidWire();
  const reordered = [...wire];
  // 交換 turn/start 的 response（index 6）與 requestApproval（index 7），
  // 讓 approval 在 turn/start 收到回應之前就出現——違反凍結協定的既定順序。
  [reordered[6], reordered[7]] = [reordered[7], reordered[6]];
  const p = writeWireLog('reordered.jsonl', reordered);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for reordered wire log');
});

// --- 負控制：approval decision response 的 id 錯誤 ---
check('judgeFullProtocol 負控制：decision response id 錯誤必須判失敗（嚴格比對，非寬鬆轉型）', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => (i === 8 ? { ...e, frame: { ...e.frame, id: 'WRONG-ID' } } : e));
  const p = writeWireLog('wrong-id.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for wrong decision id');
  assert.ok(violations.some(v => v.includes('decision response id')), `expected id violation, got: ${violations}`);
});

// --- 負控制：decision 錯誤（accept 換成 decline，但預期是 accept）---
check('judgeFullProtocol 負控制：錯誤 decision 必須判失敗', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => (i === 8 ? { ...e, frame: { ...e.frame, result: { decision: 'decline' } } } : e));
  const p = writeWireLog('wrong-decision.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for wrong decision');
  assert.ok(violations.some(v => v.includes('decision 應為')), `expected decision violation, got: ${violations}`);
});

// --- 負控制：未知 method（在 turn/start 之後插入一個陌生的 s2c method，取代
// 原本應該出現的 requestApproval）---
check('judgeFullProtocol 負控制：未知 method 必須判失敗', () => {
  const wire = buildValidWire();
  const bad = [...wire];
  bad[7] = { dir: 's2c', frame: { id: cfg.approvalRequestId, method: 'not/a/real-method', params: {} } };
  const p = writeWireLog('unknown-method.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for unknown method');
  assert.ok(violations.some(v => v.includes(cfg.approvalMethod)), `expected method-mismatch violation, got: ${violations}`);
});

// --- 負控制：重複／多餘訊息（turn/completed 之後又出現一筆多餘的 s2c frame）---
check('judgeFullProtocol 負控制：完成後仍有多餘／重複訊息必須判失敗', () => {
  const wire = buildValidWire();
  const withExtra = [...wire, { dir: 's2c' as const, frame: { method: Method.TurnCompleted, params: {} } }];
  const p = writeWireLog('extra-after-done.jsonl', withExtra);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for trailing extra message');
  assert.ok(violations.some(v => v.includes('多餘')), `expected trailing-message violation, got: ${violations}`);
});

// --- B3a-2b-2 Task C 第三輪限縮補正（缺陷 3）：frame 形狀守門負控制 ---
// controller 實測重現：先前版本對這兩種竄改都回傳 0 violations。

check('judgeFullProtocol 負控制（缺陷3-a）：initialize response 混入不該有的 method 必須判失敗', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => (i === 1 ? { ...e, frame: { ...e.frame, method: 'unexpected/rpc' } } : e));
  const p = writeWireLog('resp-with-method.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for response carrying method');
  assert.ok(violations.some(v => v.includes('不應帶 method')), `expected shape violation, got: ${violations}`);
});

check('judgeFullProtocol 負控制（缺陷3-b）：initialize response 缺少 result（只剩 {id}）必須判失敗', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => {
    if (i !== 1) return e;
    const { result: _result, ...rest } = e.frame;
    return { ...e, frame: rest };
  });
  const p = writeWireLog('resp-missing-result.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for response missing result');
  assert.ok(violations.some(v => v.includes('缺少 result')), `expected missing-result violation, got: ${violations}`);
});

check('judgeFullProtocol 負控制（缺陷3-c，混合 frame）：request 混入不該有的 result 必須判失敗', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => (i === 0 ? { ...e, frame: { ...e.frame, result: {} } } : e));
  const p = writeWireLog('req-with-result.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for request carrying result');
  assert.ok(violations.some(v => v.includes('不應帶 result')), `expected shape violation, got: ${violations}`);
});

check('judgeFullProtocol 負控制（缺陷3-d，混合 frame）：response 同時帶 result 與 error 必須判失敗', () => {
  const wire = buildValidWire();
  const bad = wire.map((e, i) => (i === 1 ? { ...e, frame: { ...e.frame, error: { code: -1, message: 'x' } } } : e));
  const p = writeWireLog('resp-result-and-error.jsonl', bad);
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.ok(violations.length > 0, 'expected violations for response carrying both result and error');
  assert.ok(violations.some(v => v.includes('帶 error')), `expected shape violation, got: ${violations}`);
});

check('judgeFullProtocol 正控制（缺陷3 回歸）：合法真 log 形狀仍然通過（重跑一次正控制 wire 確認未被形狀守門誤傷）', () => {
  const p = writeWireLog('valid-regression.jsonl', buildValidWire());
  const entries = parseRunLog(p);
  const violations = judgeFullProtocol(entries, expectation);
  assert.deepEqual(violations, []);
});

// --- judgeRunIdentity：正控制與負控制 ---
function goodManifest(overrides: Partial<Manifest> = {}): Manifest {
  return {
    scenario: cfg.scenario,
    argv: ['app-server'],
    pid: 1,
    startedAt: 't0',
    endedAt: 't1',
    exitCode: 0,
    approvalMethod: cfg.approvalMethod,
    approvalRequestId: cfg.approvalRequestId,
    decisionReceived: 'accept',
    unknownMethodsSeen: [],
    fatalError: null,
    ...overrides,
  };
}

const identityExpectation = { runId, cfg, decision: 'accept' as const };

check('judgeRunIdentity 正控制：manifest／落地 config／記憶體 config 三者一致時回傳空陣列', () => {
  const violations = judgeRunIdentity(goodManifest(), cfg, identityExpectation);
  assert.deepEqual(violations, []);
});

check('judgeRunIdentity 負控制：落地 scenario-config.json 與記憶體 cfg 不一致必須判失敗', () => {
  const diskConfig = { ...cfg, threadId: 'some-other-thread-id' };
  const violations = judgeRunIdentity(goodManifest(), diskConfig, identityExpectation);
  assert.ok(violations.length > 0, 'expected violations for on-disk config mismatch');
  assert.ok(violations.some(v => v.includes('threadId')), `expected threadId mismatch violation, got: ${violations}`);
});

check('judgeRunIdentity 負控制：manifest.decisionReceived 與預期不符必須判失敗', () => {
  const violations = judgeRunIdentity(goodManifest({ decisionReceived: 'decline' }), cfg, identityExpectation);
  assert.ok(violations.length > 0, 'expected violations for decisionReceived mismatch');
});

check('judgeRunIdentity 負控制：identity 字串缺少本次 run 的後綴（跨執行殘留）必須判失敗', () => {
  const staleCfg: ScenarioConfig = { ...cfg, threadId: 'b3a2b2-thread-DIFFERENT-RUN' };
  const violations = judgeRunIdentity(goodManifest({ scenario: staleCfg.scenario }), staleCfg, { ...identityExpectation, cfg: staleCfg });
  assert.ok(violations.length > 0, 'expected violations for stale run identity');
  assert.ok(violations.some(v => v.includes('未帶本次 run 的 identity 後綴')), `expected stale-identity violation, got: ${violations}`);
});

check('judgeRunIdentity 負控制：落地 scenario-config.json 的 afterApproval 與獨立期望不符必須判失敗', () => {
  const tamperedDiskConfig: ScenarioConfig = { ...cfg, afterApproval: [] };
  const violations = judgeRunIdentity(goodManifest(), tamperedDiskConfig, identityExpectation);
  assert.ok(violations.length > 0, 'expected violations for tampered afterApproval on disk');
  assert.ok(violations.some(v => v.includes('afterApproval')), `expected afterApproval violation, got: ${violations}`);
});

// --- B3a-2b-2 Task C 第三輪限縮補正（缺陷 2）：成對負控制 ---
// 背景：先前 spec 把「待驗的 scenarioConfigOnDisk」同時當成 exp.cfg 餵給
// judgeFullProtocol／judgeRunIdentity，導致 disk-vs-expected 比對是在跟自己
// 比對、恆真。修法是期望值改用獨立來源（等同
// `resolveScenario(env.scenario).build(env.runId)`——這裡用手刻的 `cfg`
// 代表那份獨立來源，它本來就不是從任何落地檔案讀出來的，語意等價）。這裡驗證
// 「即使 wire 與落地 config 同時說謊（成對竄改）」，用獨立 cfg 判定的
// judgeFullProtocol／judgeRunIdentity 仍然都要抓到。
check('成對負控制（缺陷2）：wire 刪除 item 事件＋落地 config 竄改 afterApproval，必須同時被抓到', () => {
  const wire = buildValidWire();
  const withoutItemEvents = wire.filter(
    e => e.frame.method !== Method.ItemStarted && e.frame.method !== Method.ItemCompleted,
  );
  const p = writeWireLog('missing-items.jsonl', withoutItemEvents);
  const entries = parseRunLog(p);
  // expectation.cfg 是獨立來源（未被竄改），wire 被砍了 item 事件——protocol
  // 判定必須抓到缺漏，不能因為「期望值剛好也被改成沒有 item 事件」而放行。
  const protocolViolations = judgeFullProtocol(entries, expectation);
  assert.ok(protocolViolations.length > 0, 'expected protocol violations for missing item events even though expectation cfg is untampered');

  const tamperedDiskConfig: ScenarioConfig = { ...cfg, afterApproval: [] };
  const identityViolations = judgeRunIdentity(goodManifest(), tamperedDiskConfig, identityExpectation);
  assert.ok(identityViolations.length > 0, 'expected identity violations for tampered afterApproval on disk');
  assert.ok(identityViolations.some(v => v.includes('afterApproval')), `expected afterApproval violation, got: ${identityViolations}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
