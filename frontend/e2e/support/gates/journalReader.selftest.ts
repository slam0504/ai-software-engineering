// journalReader.ts 的正負案例。
//
// **review #3（#427）必修缺陷 1／7 核心修正證據**：正例的 gate.jsonl 內容
// 不是手刻的——是用 `/tmp/b3a2a-contract/cmd/b3a2a_journalgen`（透過 `go
// run -overlay=...` 匯入 worktree 真實的 `internal/gate` package、對
// `gate.GateOp{OpID,At,Records}`／`gate.GateRequest`／`gate.ApprovalRecord`／
// `gate.Transition` 呼叫**正式 `encoding/json.Marshal`** 產生）的逐字輸出。
// 這裡把那份輸出逐字複製進來當 golden fixture，不是 TS 自己猜測的樣本。
//
// **review #4（#430）必修缺陷 3 核心修正證據**：以下反例逐位元組重現
// reviewer 探針目錄（`/tmp/codex-review430-2makdz0h/`）的 fixture 檔：
// `truncated-baseline.jsonl`（`{"kind":`，8 bytes）、
// `wrong-baseline-shape.jsonl`（`{}\n`）、
// `missing-record-type.jsonl`（`records:[{}]`）、
// `scalar-record.jsonl`（`records:[42]`）、
// `missing-transition-fields.jsonl`（`records:[{_type:'transition'}]`，缺
// `approval_id`／`to`／`at`／`cause`）、`deleted-active-gate`（journal 在
// 兩次讀取之間被刪除）。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/journalReader.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  assertNoAuditSpecWatchError, assertNoWorkspaceStreamError, bindingsOfGateRequest, byteOffsetOf,
  captureAuditBaseline, diffNewTransitions, hasNewStaleTransitionFor, JournalReadError,
  latestApprovalRecordFor, latestGateRequestApprovalId, readAllRecordsByType,
  readAllTransitionsAllowMissing, readAllTransitionsRequireExists,
} from './journalReader.js';

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

function tmpFile(name: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gates-journal-selftest-'));
  return path.join(dir, name);
}

function op(records: unknown[]): string {
  return `${JSON.stringify({ op_id: 'op1', at: 't', records })}\n`;
}

// ---- golden 樣本：真實 Go json.Marshal 輸出（逐字複製，見檔頭說明） ----
const GOLDEN_GATE_REQUEST_GATE1 = '{"op_id":"op-1","at":"2026-09-24T00:00:00Z","records":[{"_type":"gate_request","schema_version":2,"approval_id":"A1","gate":"gate1","subject":"workspace","bindings":[{"kind":"spec_manifest","ref":"spec","digest":"sha256:aaa"},{"kind":"base_commit","ref":"base","digest":"git:sha1:deadbeef"}],"created_at":"2026-09-24T00:00:00Z"}]}\n';
const GOLDEN_APPROVAL_RECORD_GATE1_NIL_METADATA = '{"op_id":"op-2","at":"2026-09-24T00:00:01Z","records":[{"_type":"approval_record","schema_version":2,"approval_id":"A1","gate":"gate1","subject":"workspace","decision":"approved","approver":{"id":"tester","method":"manual"},"reason":"","bindings":[{"kind":"spec_manifest","ref":"spec","digest":"sha256:aaa"},{"kind":"base_commit","ref":"base","digest":"git:sha1:deadbeef"}],"created_at":"2026-09-24T00:00:01Z"}]}\n';
const GOLDEN_APPROVAL_RECORD_GATE2_RISK_DECISIONS = '{"op_id":"op-3","at":"2026-09-24T00:00:02Z","records":[{"_type":"approval_record","schema_version":2,"approval_id":"A2","gate":"gate2","subject":"plan:P1","decision":"approved","approver":{"id":"tester","method":"manual"},"reason":"","bindings":[{"kind":"plan","ref":"plan","digest":"sha256:bbb"}],"metadata":{"risk_decisions":[{"task_id":"T1","minimum_risk_tier":"medium","planner_risk_tier":"medium","selected_risk_tier":"medium"},{"task_id":"T2","minimum_risk_tier":"medium","planner_risk_tier":"high","selected_risk_tier":"medium","override_reason":"reviewed"}]},"created_at":"2026-09-24T00:00:02Z"}]}\n';
const GOLDEN_TRANSITION_STALE = '{"op_id":"op-4","at":"t","records":[{"_type":"transition","approval_id":"A1","to":"stale","at":"t","cause":"spec_manifest changed","evidence_ref":"sha256:x"}]}\n';
const GOLDEN_MULTI_RECORD_OP = '{"op_id":"op-5","at":"t2","records":[{"_type":"gate_request","schema_version":2,"approval_id":"A1","gate":"gate1","subject":"workspace","bindings":[{"kind":"spec_manifest","ref":"spec","digest":"sha256:aaa"},{"kind":"base_commit","ref":"base","digest":"git:sha1:deadbeef"}],"created_at":"2026-09-24T00:00:00Z"},{"_type":"transition","approval_id":"A1","to":"stale","at":"t","cause":"spec_manifest changed","evidence_ref":"sha256:x"}]}\n';
const GOLDEN_EMPTY_RECORDS_ARRAY = '{"op_id":"op-6","at":"t3","records":[]}\n';

// ---- 正例：真實 Go 輸出必須被正確解析 ----
check('readAllTransitionsRequireExists 對真實 Go transition golden 樣本，讀到 1 筆 stale（review #427 point 1 的核心迴歸案例）', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_TRANSITION_STALE);
  const t = readAllTransitionsRequireExists(p);
  assert.equal(t.length, 1);
  assert.equal(t[0].to, 'stale');
  assert.equal(t[0].approval_id, 'A1');
});

check('hasNewStaleTransitionFor 對真實 Go golden 樣本回傳 true（reviewer node-contract.log 原本回報 false）', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_TRANSITION_STALE);
  const t = readAllTransitionsRequireExists(p);
  assert.equal(hasNewStaleTransitionFor(t, 'A1'), true);
});

check('readAllRecordsByType 對真實 Go gate_request golden 樣本讀到 1 筆 gate1 記錄', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_GATE_REQUEST_GATE1);
  const reqs = readAllRecordsByType(p, 'gate_request');
  assert.equal(reqs.length, 1);
  assert.equal(reqs[0].gate, 'gate1');
  assert.equal(reqs[0].approval_id, 'A1');
});

check('latestGateRequestApprovalId 對真實 Go golden 樣本正確找出 A1', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_GATE_REQUEST_GATE1);
  assert.equal(latestGateRequestApprovalId(p, 'gate1'), 'A1');
  assert.equal(latestGateRequestApprovalId(p, 'gate2'), null);
});

check('bindingsOfGateRequest 對真實 Go golden 樣本正確攤平 bindings', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_GATE_REQUEST_GATE1);
  const b = bindingsOfGateRequest(p, 'A1');
  assert.equal(b.spec_manifest, 'sha256:aaa');
  assert.equal(b.base_commit, 'git:sha1:deadbeef');
});

check('latestApprovalRecordFor 對 Gate1 的 approval_record（metadata 因 omitempty 省略）正確回傳，且沒有 metadata 鍵', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_APPROVAL_RECORD_GATE1_NIL_METADATA);
  const rec = latestApprovalRecordFor(p, 'A1');
  assert.ok(rec);
  assert.equal(rec!.decision, 'approved');
  assert.equal('metadata' in rec!, false, 'Gate1 的 approval_record 因 omitempty 不應出現 metadata 鍵');
});

check('latestApprovalRecordFor 對 Gate2 的 approval_record 正確讀出 risk_decisions（含 override_reason 的 omitempty 案例）', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_APPROVAL_RECORD_GATE2_RISK_DECISIONS);
  const rec = latestApprovalRecordFor(p, 'A2');
  assert.ok(rec);
  const metadata = rec!.metadata as { risk_decisions: Array<Record<string, unknown>> };
  assert.equal(metadata.risk_decisions.length, 2);
  assert.equal('override_reason' in metadata.risk_decisions[0], false, 'T1 selected==planner，override_reason 應因 omitempty 不存在');
  assert.equal(metadata.risk_decisions[1].override_reason, 'reviewed');
});

check('readAllTransitionsRequireExists 對含多筆 record 的單行 op 正確攤平（gate_request + transition 混在同一個 op）', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_MULTI_RECORD_OP);
  assert.equal(readAllTransitionsRequireExists(p).length, 1);
  assert.equal(readAllRecordsByType(p, 'gate_request').length, 1);
});

check('正例：records 為空陣列（合法形狀，零筆 record）不視為壞掉', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, GOLDEN_EMPTY_RECORDS_ARRAY);
  assert.doesNotThrow(() => readAllTransitionsRequireExists(p));
  assert.equal(readAllTransitionsRequireExists(p).length, 0);
});

// ---- readAllTransitionsAllowMissing vs readAllTransitionsRequireExists（review #4 必修缺陷 3：兩種模式分開） ----
check('readAllTransitionsAllowMissing：gate.jsonl 不存在時回傳空陣列（Gate1 首次送核前的合法初始狀態）', () => {
  const p = tmpFile('does-not-exist.jsonl');
  assert.doesNotThrow(() => readAllTransitionsAllowMissing(p));
  assert.equal(readAllTransitionsAllowMissing(p).length, 0);
});

check('負案例（review #4 必修缺陷 3：對應 reviewer deleted-active-gate 探針的另一半）：readAllTransitionsRequireExists 對不存在的檔案必須 throw，不得回傳 []', () => {
  const p = tmpFile('does-not-exist.jsonl');
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError);
});

check('負案例（reviewer deleted-active-gate 探針重現）：active gate 的 journal 在兩次讀取之間被刪除，diffNewTransitions 的「after」讀取必須 throw，不能被 diff 成「沒有新增」', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{ _type: 'approval_record', approval_id: 'A1', gate: 'gate1', decision: 'approved' }]));
  const before = readAllTransitionsRequireExists(p); // 0 筆（approval_record 不是 transition），但檔案存在，讀取本身應成功
  assert.equal(before.length, 0);
  fs.unlinkSync(p); // 模擬 active gate 的 journal 檔案異常消失
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError,
    '檔案消失後重讀必須 throw（不是回傳 []，讓 diffNewTransitions 誤判成「沒有新增 transition」）');
});

// ---- byteOffsetOf vs captureAuditBaseline 的分工 ----
check('byteOffsetOf：不存在的檔案回傳 0（僅供 gate.jsonl 的合法初始狀態使用）', () => {
  assert.equal(byteOffsetOf(tmpFile('nope.jsonl')), 0);
});

check('負案例（review #3 必修缺陷 7：對應 reviewer missing-at-baseline.jsonl 案）：captureAuditBaseline 對不存在的 audit 檔案必須 throw，不得回傳 0', () => {
  const p = tmpFile('audit.jsonl');
  assert.throws(() => captureAuditBaseline(p), JournalReadError);
});

// ---- review #4（#430）必修缺陷 3：captureAuditBaseline 必須實際讀取＋驗證既有內容，不能只 stat ----
check('負案例（reviewer truncated-baseline.jsonl，`{"kind":`，8 bytes 無結尾換行）：captureAuditBaseline 必須 throw，不得把截斷 JSON 當成合法 baseline', () => {
  const p = tmpFile('audit.jsonl');
  fs.writeFileSync(p, '{"kind":'); // 逐位元組複製 reviewer 樣本
  assert.throws(() => captureAuditBaseline(p), JournalReadError);
});

check('負案例（reviewer wrong-baseline-shape.jsonl，`{}\\n`）：captureAuditBaseline 必須 throw，不得把形狀錯誤的內容當成合法 baseline', () => {
  const p = tmpFile('audit.jsonl');
  fs.writeFileSync(p, '{}\n');
  assert.throws(() => captureAuditBaseline(p), JournalReadError);
});

check('正例：captureAuditBaseline 對合法既有內容回傳正確 offset 與驗證過的 records 快照', () => {
  const p = tmpFile('audit.jsonl');
  const content = `${JSON.stringify({ ts: 't0', kind: 'harmless', data: {} })}\n`;
  fs.writeFileSync(p, content);
  const baseline = captureAuditBaseline(p);
  assert.equal(baseline.offset, Buffer.byteLength(content));
  assert.equal(baseline.records.length, 1);
  assert.equal(baseline.records[0].kind, 'harmless');
});

check('正例：captureAuditBaseline 對空檔案回傳 offset=0、records=[]（合法的「還沒有任何內容」，跟「內容壞掉」不同）', () => {
  const p = tmpFile('audit.jsonl');
  fs.writeFileSync(p, '');
  const baseline = captureAuditBaseline(p);
  assert.equal(baseline.offset, 0);
  assert.deepEqual(baseline.records, []);
});

// ---- review #4 必修缺陷 3：gate.jsonl 單筆 record 的必要欄位驗證（reviewer 三個探針案例逐位元組重現） ----
check('負案例（reviewer missing-record-type.jsonl，records:[{}]）：缺少 _type 的 record 必須被拒絕，不能被過濾掉當成沒有紀錄', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{}]));
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError);
});

check('負案例（reviewer scalar-record.jsonl，records:[42]）：純量 record 必須被拒絕', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([42]));
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError);
});

check('負案例（reviewer missing-transition-fields.jsonl，records:[{_type:"transition"}]）：_type 正確但缺 approval_id/to/at/cause 的 transition 必須被拒絕，不能被當成「筆數 0」或「有一筆但欄位是 undefined」放行', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{ _type: 'transition' }]));
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError);
});

check('負案例：_type 是未知字串（不是 gate_request/approval_record/transition 三種之一）必須被拒絕', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{ _type: 'something_else', approval_id: 'A1' }]));
  assert.throws(() => readAllTransitionsRequireExists(p), JournalReadError);
});

check('負案例：gate_request record 缺少 gate 欄位必須被拒絕', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{ _type: 'gate_request', approval_id: 'A1' }]));
  assert.throws(() => readAllRecordsByType(p, 'gate_request'), JournalReadError);
});

check('負案例：approval_record 缺少 decision 欄位必須被拒絕', () => {
  const p = tmpFile('gate.jsonl');
  fs.writeFileSync(p, op([{ _type: 'approval_record', approval_id: 'A1', gate: 'gate1' }]));
  assert.throws(() => readAllRecordsByType(p, 'approval_record'), JournalReadError);
});

// ---- 事件（events.jsonl）驗證 ----
check('正例：events.jsonl 新增內容含合法 envelope（event_id/ts/provider/kind 皆為字串）不觸發', () => {
  const p = tmpFile('events.jsonl');
  const baseline = captureAfterCreate(p);
  fs.appendFileSync(p, `${JSON.stringify({ event_id: '01X', ts: 't1', provider: '', kind: 'stream_error', scope: 'session' })}\n`);
  assert.doesNotThrow(() => assertNoWorkspaceStreamError(p, baseline));
});

check('負案例：events.jsonl 新增內容含 workspace/stream_error 時必須 throw', () => {
  const p = tmpFile('events.jsonl');
  const baseline = captureAfterCreate(p);
  fs.appendFileSync(p, `${JSON.stringify({ event_id: '01X', ts: 't1', provider: '', kind: 'stream_error', scope: 'workspace' })}\n`);
  assert.throws(() => assertNoWorkspaceStreamError(p, baseline), JournalReadError);
});

check('負案例：events.jsonl 一行缺少 event_id（正式 Envelope 無 omitempty，缺這個鍵代表形狀壞掉）必須 throw', () => {
  const p = tmpFile('events.jsonl');
  const baseline = captureAfterCreate(p);
  fs.appendFileSync(p, `${JSON.stringify({ ts: 't1', provider: '', kind: 'stream_error', scope: 'workspace' })}\n`);
  assert.throws(() => assertNoWorkspaceStreamError(p, baseline), JournalReadError);
});

check('負案例：audit.jsonl 新增內容含 {}（形狀錯誤）必須 throw，不得當成沒有錯誤', () => {
  const p = tmpFile('audit.jsonl');
  const baseline = captureAfterCreate(p);
  fs.appendFileSync(p, '{}\n');
  assert.throws(() => assertNoAuditSpecWatchError(p, baseline), JournalReadError);
});

function captureAfterCreate(p: string): number {
  fs.writeFileSync(p, '');
  return captureAuditBaseline(p).offset;
}

// ---- diffNewTransitions／hasNewStaleTransitionFor ----
check('diffNewTransitions：before 是 after 的前綴時，回傳差集', () => {
  const before = [{ _type: 'transition' as const, approval_id: 'A1', to: 'stale', at: 't1', cause: 'c', evidence_ref: 'e' }];
  const after = [...before, { _type: 'transition' as const, approval_id: 'A2', to: 'stale', at: 't2', cause: 'c2', evidence_ref: 'e2' }];
  const diff = diffNewTransitions(before, after);
  assert.equal(diff.length, 1);
  assert.equal(diff[0].approval_id, 'A2');
});

check('負案例：diffNewTransitions 偵測到既有記錄被竄改（非純 append）必須 throw', () => {
  const before = [{ _type: 'transition' as const, approval_id: 'A1', to: 'stale', at: 't1', cause: 'c', evidence_ref: 'e' }];
  const tampered = [{ _type: 'transition' as const, approval_id: 'A1', to: 'stale', at: 'TAMPERED', cause: 'c', evidence_ref: 'e' }];
  assert.throws(() => diffNewTransitions(before, tampered), JournalReadError);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
