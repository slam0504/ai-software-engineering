// recoveryJudge.ts 的純函式測試（B3a-2b-2 Task E1 R1 修正）——獨立 selftest，
// 對齊 scenarioProtocolJudge.selftest.ts 的慣例：本檔只用合成的 RunLogEntry[]
// 直接餵給 judgeRecoverySequence／judgeRecoveryManifest，不啟動真子程序（真
// 子程序的黑箱測試在 fakeAppServer.selftest.ts，那邊透過 parseRunLog() 解析
// 真實 log 檔後同樣呼叫這裡匯出的函式——兩邊共用同一份判定，不是各自一份）。
//
// 背景：reviewer 用工作樹裡原樣抽出的區域 judge 實測，(a)(b)(c)(d) 四個反例
// 全部 PASS——本檔逐一重現並驗證修正後的共用模組真的會擋下。
//
// 執行：node frontend/e2e/support/scenario/recoveryJudge.selftest.ts
import assert from 'node:assert/strict';
import type { Frame, Manifest, RunLogEntry } from './protocol.ts';
import { judgeRecoveryManifest, judgeRecoverySequence } from './recoveryJudge.ts';
import type { RecoveryExpectation, RecoveryRoundExpectation } from './recoveryJudge.ts';

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

// ---- judgeRecoverySequence 自身的負控制：驗證 judge 能在「結構合法、但
// identity 錯配」的合成 log 上獨立抓到問題——跟上面「driver 送錯參數讓 fake
// 自己 fail」是兩種不同的錯誤來源，這裡刻意繞過 fake（synthetic log），只測
// judge 本身，證明兩者可分辨（不是靠 fake 的 exit code 偽裝判定生效）。----

// syntheticGenuineLog：兩輪都帶完整的 afterApproval 內容事件（(d) 反例的
// 對照正控制——先前的版本連正控制自己都沒有內容事件，等於從未驗過）。
function syntheticGenuineLog(): { log: import('./protocol.ts').RunLogEntry[]; expected: RecoveryExpectation } {
  const threadId = 'thread-synthetic-1';
  const approvalMethod = 'item/commandExecution/requestApproval';
  const round1: RecoveryRoundExpectation = {
    turnId: 't1',
    itemId: 'i1',
    approvalRequestId: 'a1',
    decision: 'accept',
    afterApproval: [{ type: 'itemCompleted', text: 'round1-done' }],
    turnStatus: 'completed',
  };
  const round2: RecoveryRoundExpectation = {
    turnId: 't2',
    itemId: 'i2',
    approvalRequestId: 'a2',
    decision: 'accept',
    afterApproval: [{ type: 'itemCompleted', text: 'round2-done' }],
    turnStatus: 'completed',
  };
  const expected: RecoveryExpectation = { threadId, approvalMethod, round1, round2 };
  let seq = 0;
  const entry = (dir: 'c2s' | 's2c', frame: Frame) => ({ seq: ++seq, ts: new Date(seq).toISOString(), dir, frame });
  const log = [
    entry('c2s', { id: 1, method: 'initialize', params: {} }),
    entry('s2c', { id: 1, result: {} }),
    entry('c2s', { method: 'initialized' }),
    entry('c2s', { id: 2, method: 'thread/start', params: {} }),
    entry('s2c', { id: 2, result: { thread: { id: threadId } } }),
    entry('c2s', { id: 3, method: 'turn/start', params: { threadId } }),
    entry('s2c', { id: 3, result: { turn: { id: round1.turnId, status: 'inProgress' } } }),
    entry('s2c', { id: round1.approvalRequestId, method: approvalMethod, params: { threadId, turnId: round1.turnId, itemId: round1.itemId } }),
    entry('c2s', { id: round1.approvalRequestId, result: { decision: 'accept' } }),
    entry('s2c', {
      method: 'item/completed',
      params: { threadId, turnId: round1.turnId, item: { type: 'agentMessage', id: round1.itemId, text: 'round1-done' } },
    }),
    entry('s2c', { method: 'turn/completed', params: { threadId, turn: { id: round1.turnId, status: 'completed' } } }),
    entry('c2s', { id: 10, method: 'thread/resume', params: { threadId } }),
    entry('s2c', { id: 10, result: { thread: { id: threadId } } }),
    entry('c2s', { id: 11, method: 'turn/start', params: { threadId } }),
    entry('s2c', { id: 11, result: { turn: { id: round2.turnId, status: 'inProgress' } } }),
    entry('s2c', { id: round2.approvalRequestId, method: approvalMethod, params: { threadId, turnId: round2.turnId, itemId: round2.itemId } }),
    entry('c2s', { id: round2.approvalRequestId, result: { decision: 'accept' } }),
    entry('s2c', {
      method: 'item/completed',
      params: { threadId, turnId: round2.turnId, item: { type: 'agentMessage', id: round2.itemId, text: 'round2-done' } },
    }),
    entry('s2c', { method: 'turn/completed', params: { threadId, turn: { id: round2.turnId, status: 'completed' } } }),
  ];
  return { log, expected };
}

check('judgeRecoverySequence 正控制：結構完整合法（含兩輪 afterApproval 內容）的合成 log 回傳空陣列', () => {
  const { log, expected } = syntheticGenuineLog();
  const violations = judgeRecoverySequence(log, expected);
  assert.deepEqual(violations, [], `expected no violations, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoverySequence 負控制：兩輪 threadId 錯配（round2 resume 帶另一個 threadId）必須被 judge 自己抓到（非 fake 拒絕）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.frame?.method === 'thread/resume'
      ? { ...e, frame: { ...e.frame, params: { threadId: 'ANOTHER-THREAD' } } }
      : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(
    violations.some(v => v.includes('threadId mismatch')),
    `expected a threadId mismatch violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 負控制：第二輪被截斷（log 在 round2 turn/completed 前結束）必須判失敗', () => {
  const { log, expected } = syntheticGenuineLog();
  const truncated = log.slice(0, log.length - 1);
  const violations = judgeRecoverySequence(truncated, expected);
  assert.ok(
    violations.some(v => v.includes('truncated')),
    `expected a truncation violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 負控制：兩輪 approvalRequestId 錯配（round2 approval 沿用 round1 id）必須判失敗', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.frame?.method === 'item/commandExecution/requestApproval' && (e.frame.params as Record<string, unknown>)?.turnId === 't2'
      ? { ...e, frame: { ...e.frame, id: 'a1' } }
      : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected violations for reused round-1 approvalRequestId, got: ${JSON.stringify(violations)}`);
});

// ---- R1 反例 (a)(b)(c)(d) ＋ 錯 ID 型別 ＋ 截斷／錯序：reviewer 逐項要求的
// 負控制，證明修正後的 judge 真的擋下這些先前全部 PASS 的反例。----

check('judgeRecoverySequence 反例 (a)：approval method 改成 evil/requestApproval 必須被擋（先前用 .includes 寬鬆比對會 PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.frame?.method === 'item/commandExecution/requestApproval' && (e.frame.params as Record<string, unknown>)?.turnId === 't1'
      ? { ...e, frame: { ...e.frame, method: 'evil/requestApproval' } }
      : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected (a) to be rejected, got: ${JSON.stringify(violations)}`);
  assert.ok(
    violations.some(v => v.includes('evil/requestApproval')),
    `expected violation to mention the wrong method, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 反例 (b)：initialize response 加上 error（result 仍保留）必須被擋（先前沒核對 response 形狀會 PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.seq === 2 ? { ...e, frame: { ...e.frame, error: { code: -1, message: 'injected' } } } : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected (b) to be rejected, got: ${JSON.stringify(violations)}`);
  assert.ok(
    violations.some(v => v.includes('error')),
    `expected violation to mention the injected error field, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 反例 (c)：turn/completed 的 status 改成 failed 必須被擋（先前沒核對 turn.status 會 PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.frame?.method === 'turn/completed' && (e.frame.params as Record<string, unknown> & { turn?: { id?: string } })?.turn?.id === 't1'
      ? { ...e, frame: { ...e.frame, params: { ...(e.frame.params as Record<string, unknown>), turn: { id: 't1', status: 'failed' } } } }
      : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected (c) to be rejected, got: ${JSON.stringify(violations)}`);
  assert.ok(
    violations.some(v => v.includes('status')),
    `expected violation to mention the status mismatch, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 反例 (d)：拿掉兩輪的 afterApproval 內容事件必須被擋（先前的 while 迴圈會略過未知 s2c 事件、PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const stripped = log.filter(e => e.frame?.method !== 'item/completed');
  const violations = judgeRecoverySequence(stripped, expected);
  assert.ok(violations.length > 0, `expected (d) to be rejected, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoverySequence 負控制：round2 approval response id 型別錯（number 1 對字串 "a2"）必須被擋（型別保留比對，不寬鬆轉型）', () => {
  const { log, expected } = syntheticGenuineLog();
  const numericExpected: RecoveryExpectation = {
    ...expected,
    round2: { ...expected.round2, approvalRequestId: 'a2' },
  };
  // 把 round2 approval 的 request／response id 都改成 number 2（結構仍然合法，
  // 只是型別跟 expected 的字串 'a2' 不同）。
  const tampered = log.map(e => {
    if (e.frame?.method === 'item/commandExecution/requestApproval' && (e.frame.params as Record<string, unknown>)?.turnId === 't2') {
      return { ...e, frame: { ...e.frame, id: 2 } };
    }
    if (e.dir === 'c2s' && e.frame?.id === 'a2' && e.frame?.result !== undefined) {
      return { ...e, frame: { ...e.frame, id: 2 } };
    }
    return e;
  });
  const violations = judgeRecoverySequence(tampered, numericExpected);
  assert.ok(violations.length > 0, `expected wrong-id-type to be rejected, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoverySequence 負控制：第二輪錯序（turn/start response 提前插到 approval request 之前重複出現）必須被擋', () => {
  const { log, expected } = syntheticGenuineLog();
  const turnStartRespIdx = log.findIndex(e => e.dir === 's2c' && e.frame?.id === 11);
  const approvalReqIdx = log.findIndex(e => e.frame?.method === 'item/commandExecution/requestApproval' && (e.frame.params as Record<string, unknown>)?.turnId === 't2');
  const reordered = [...log];
  const [dup] = reordered.splice(turnStartRespIdx, 1);
  reordered.splice(approvalReqIdx, 0, dup); // 插到 approval request 前面，製造重複＋錯序
  const violations = judgeRecoverySequence(reordered, expected);
  assert.ok(violations.length > 0, `expected reordered/duplicated frame to be rejected, got: ${JSON.stringify(violations)}`);
});


// ---- judgeRecoveryManifest 自身的正／負控制 ----

function baseManifest(overrides: Partial<Manifest> = {}): Manifest {
  return {
    scenario: 'recovery-judge-selftest',
    argv: ['app-server'],
    pid: 1,
    startedAt: '2026-01-01T00:00:00.000Z',
    endedAt: '2026-01-01T00:00:01.000Z',
    exitCode: 0,
    approvalMethod: 'item/commandExecution/requestApproval',
    approvalRequestId: 'a1',
    decisionReceived: 'accept',
    unknownMethodsSeen: [],
    fatalError: null,
    secondTurn: { approvalMethod: 'item/commandExecution/requestApproval', approvalRequestId: 'a2', decisionReceived: 'accept', resumeAccepted: true },
    ...overrides,
  };
}

check('judgeRecoveryManifest 正控制：兩輪都合法的 manifest 回傳空陣列', () => {
  const violations = judgeRecoveryManifest(baseManifest(), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.deepEqual(violations, [], `expected no violations, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoveryManifest 正控制：單輪（round2=null）且 manifest.secondTurn 為 null 回傳空陣列', () => {
  const violations = judgeRecoveryManifest(baseManifest({ secondTurn: null }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: null,
  });
  assert.deepEqual(violations, [], `expected no violations, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoveryManifest 負控制：round1 的 decisionReceived 被第二輪覆寫（頂層欄位不再是 accept）必須被擋', () => {
  const violations = judgeRecoveryManifest(baseManifest({ decisionReceived: 'decline' }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.ok(violations.length > 0, `expected a violation, got: ${JSON.stringify(violations)}`);
});

check('judgeRecoveryManifest 負控制：secondTurn.resumeAccepted 為 false 必須被擋（不能把「resume 失敗」偷偷標記成功）', () => {
  const violations = judgeRecoveryManifest(
    baseManifest({ secondTurn: { approvalMethod: 'item/commandExecution/requestApproval', approvalRequestId: 'a2', decisionReceived: 'accept', resumeAccepted: false } }),
    {
      approvalMethod: 'item/commandExecution/requestApproval',
      round1: { approvalRequestId: 'a1', decision: 'accept' },
      round2: { approvalRequestId: 'a2', decision: 'accept' },
    },
  );
  assert.ok(
    violations.some(v => v.includes('resumeAccepted')),
    `expected a resumeAccepted violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoveryManifest 負控制：兩輪 approvalRequestId 相同（identity 重用）必須被擋', () => {
  const violations = judgeRecoveryManifest(
    baseManifest({ approvalRequestId: 'a1', secondTurn: { approvalMethod: 'item/commandExecution/requestApproval', approvalRequestId: 'a1', decisionReceived: 'accept', resumeAccepted: true } }),
    {
      approvalMethod: 'item/commandExecution/requestApproval',
      round1: { approvalRequestId: 'a1', decision: 'accept' },
      round2: { approvalRequestId: 'a1', decision: 'accept' },
    },
  );
  assert.ok(
    violations.some(v => v.includes('identity reuse')),
    `expected an identity-reuse violation, got: ${JSON.stringify(violations)}`,
  );
});

// ---- B3a-2b-2b 缺陷修正：reviewer 用 syntheticGenuineLog 正控制實測，
// response 帶 params 與 item.type 非 agentMessage 兩例，經 parseRunLog 再呼叫
// 實際匯出的 judgeRecoverySequence，都回傳 violations=[]——本節逐一重現並
// 驗證修正後真的會擋下。----

check('judgeRecoverySequence 缺陷 1 反例：initialize response 帶 params 必須被擋（先前 assertResponseShape 沒核對 params 會 PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.seq === 2 ? { ...e, frame: { ...e.frame, params: { unexpected: true } } } : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected response-with-params to be rejected, got: ${JSON.stringify(violations)}`);
  assert.ok(
    violations.some(v => v.includes('不應帶 params')),
    `expected violation to mention the unexpected params field, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoverySequence 缺陷 1 反例：round1 第一個內容事件 item.type 改成 commandExecution 必須被擋（先前沒核對 item.type 會 PASS）', () => {
  const { log, expected } = syntheticGenuineLog();
  const tampered = log.map(e =>
    e.frame?.method === 'item/completed' && (e.frame.params as Record<string, unknown> & { turnId?: string })?.turnId === 't1'
      ? {
          ...e,
          frame: {
            ...e.frame,
            params: {
              ...(e.frame.params as Record<string, unknown>),
              item: { ...(e.frame.params as Record<string, unknown> & { item: Record<string, unknown> }).item, type: 'commandExecution' },
            },
          },
        }
      : e,
  );
  const violations = judgeRecoverySequence(tampered, expected);
  assert.ok(violations.length > 0, `expected wrong item.type to be rejected, got: ${JSON.stringify(violations)}`);
  assert.ok(
    violations.some(v => v.includes('item.type mismatch')),
    `expected violation to mention item.type mismatch, got: ${JSON.stringify(violations)}`,
  );
});

// ---- 缺陷 2 反例：manifest 的 endedAt／exitCode／fatalError／
// unknownMethodsSeen 各自單欄位反例——reviewer 實測兩輪 identity 保持正確，
// 但這四欄位任一個壞掉，先前的 judgeRecoveryManifest 仍回傳 []。----

check('judgeRecoveryManifest 缺陷 2 反例：exitCode 非 0（17）必須被擋（identity 仍正確也不能算通過）', () => {
  const violations = judgeRecoveryManifest(baseManifest({ exitCode: 17 }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.ok(
    violations.some(v => v.includes('exitCode mismatch')),
    `expected an exitCode violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoveryManifest 缺陷 2 反例：fatalError 非 null 必須被擋', () => {
  const violations = judgeRecoveryManifest(baseManifest({ fatalError: 'boom' }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.ok(
    violations.some(v => v.includes('fatalError present')),
    `expected a fatalError violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoveryManifest 缺陷 2 反例：unknownMethodsSeen 非空必須被擋', () => {
  const violations = judgeRecoveryManifest(baseManifest({ unknownMethodsSeen: ['evil/method'] }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.ok(
    violations.some(v => v.includes('unknownMethodsSeen not empty')),
    `expected an unknownMethodsSeen violation, got: ${JSON.stringify(violations)}`,
  );
});

check('judgeRecoveryManifest 缺陷 2 反例：endedAt 為 null（run 根本沒收尾）必須被擋', () => {
  const violations = judgeRecoveryManifest(baseManifest({ endedAt: null }), {
    approvalMethod: 'item/commandExecution/requestApproval',
    round1: { approvalRequestId: 'a1', decision: 'accept' },
    round2: { approvalRequestId: 'a2', decision: 'accept' },
  });
  assert.ok(
    violations.some(v => v.includes('run not ended')),
    `expected an endedAt violation, got: ${JSON.stringify(violations)}`,
  );
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
