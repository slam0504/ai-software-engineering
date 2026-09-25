// B3a-2a：gate.jsonl／audit.jsonl／events.jsonl 的離線讀取＋窗口式 diff
// helper（design v4 §9）。純 Node fs／JSON，不依賴 Playwright，供
// gateProbe.ts（S-N1 探針流程）與 selftest 共用。
//
// **review #3（#427）必修缺陷 1 修正**：正式 JSON 欄位是 `op_id`／`at`／
// `records`（`internal/gate/types.go:68-71`
// `GateOp{OpID string \`json:"op_id"\`; At string \`json:"at"\`; Records
// []json.RawMessage \`json:"records"\`}`）——舊版誤用 PascalCase
// `OpID`／`At`／`Records`，對真實 Go 輸出永遠讀不到任何 records（見任務
// 回報「Go 契約驗證」一節的 golden 樣本與 selftest）。本檔全面改用小寫
// snake_case 欄位名，並在執行時驗證資料形狀：`records` 缺失或不是陣列一
// 律 throw，不用 `?? []` 當成空紀錄。
//
// **review #3 必修缺陷 7 修正**：`byteOffsetOf` 對缺檔回 0 的寬容行為**只**
// 適用於 gate.jsonl 在 Gate1 首次送核前尚未建立的情境（這是允許的合法狀
// 態）。audit.jsonl／events.jsonl 在 S-N1 執行當下（App 已啟動、Gate1／
// Gate2 都核可過）**必須已經存在**，改用 `captureBaselineOffset` 明確要
// 求檔案存在且可讀，缺檔直接 throw。窗口讀取也加上「新增內容非空時必須
// 以換行結尾」的檢查（對齊 production 的 `a.audit()`／`JSONLSink.Write`
// 都是 `Fprintf(f, "%s\n", b)`，缺尾端換行代表寫入被截斷），並對每一行的
// 資料形狀做正式驗證（audit record／event envelope 的必要欄位），不是只
// 靠 `JSON.parse` 加型別斷言。
import fs from 'node:fs';

export interface AuditRecord {
  ts: string;
  kind: string;
  data: unknown;
}

export interface EventEnvelope {
  event_id: string;
  ts: string;
  kind: string;
  provider: string;
  scope?: string;
  payload?: unknown;
  [k: string]: unknown;
}

export interface GateTransition {
  _type: 'transition';
  approval_id: unknown;
  to: unknown;
  at: unknown;
  cause: unknown;
  evidence_ref: unknown;
}

export interface GateOpRecord {
  _type: unknown;
  [k: string]: unknown;
}

/** 對齊 internal/gate/types.go:68-71 的正式 JSON 欄位名稱（op_id／at／records）。 */
export interface GateOpLine {
  op_id: unknown;
  at: unknown;
  records: GateOpRecord[];
}

export class JournalReadError extends Error {}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * `byteOffsetOf`：缺檔視為 0——**只能用於 gate.jsonl 在 Gate1 首次送核前
 * 尚未建立的情境**（review #3 必修缺陷 7：這是一個獨立、被允許的合法狀
 * 態，不能套用到 audit.jsonl／events.jsonl 的 baseline）。
 */
export function byteOffsetOf(path: string): number {
  try {
    return fs.statSync(path).size;
  } catch (e) {
    if (isEnoent(e)) return 0;
    throw e;
  }
}

function isEnoent(e: unknown): boolean {
  return typeof e === 'object' && e !== null && 'code' in e && (e as { code?: unknown }).code === 'ENOENT';
}

export interface Baseline<T> {
  offset: number;
  records: T[];
}

/**
 * audit.jsonl／events.jsonl 的 baseline 擷取——**review #4（#430）必修缺陷
 * 3 修正**：舊版只 `fs.statSync` 拿檔案大小當 offset，完全不驗證既有內容
 * 本身是否完整合法——reviewer 的探針證明 `{"kind":`（截斷 JSON）與 `{}\n`
 * （形狀錯誤但語法合法）都能被當成 baseline 接受。
 *
 * 改成：要求檔案存在、可讀；讀出**全部既有內容**；非空時必須以換行結
 * 尾（缺尾端換行視為寫入被截斷）；逐行 `JSON.parse` 後交給 `validate`
 * 做形狀驗證（跟窗口讀取用同一組驗證函式，`{}`／截斷 JSON 都會在這裡被
 * 拒絕）。回傳值除了 offset（供後續窗口讀取用）還有驗證過的既有內容快
 * 照本身（`records`），讓呼叫端能把這份 baseline 快照存進
 * `GateEvidenceRecorder`（review #4 必修缺陷 4）。
 */
export function captureBaseline<T>(path: string, label: string, validate: (parsed: unknown, rawLine: string) => T): Baseline<T> {
  let stat: fs.Stats;
  try {
    stat = fs.statSync(path);
  } catch (e) {
    throw new JournalReadError(`captureBaseline: ${label} 基準檔案不存在或無法讀取（${path}）：${e instanceof Error ? e.message : String(e)}——S-N1 執行時該檔案應已建立，缺檔不能當成「還沒有內容」`);
  }
  if (!stat.isFile()) {
    throw new JournalReadError(`captureBaseline: ${label}（${path}）不是一般檔案`);
  }
  let raw: Buffer;
  try {
    raw = fs.readFileSync(path);
  } catch (e) {
    throw new JournalReadError(`captureBaseline: ${label}（${path}）stat 成功但讀取失敗：${e instanceof Error ? e.message : String(e)}`);
  }
  if (raw.length !== stat.size) {
    throw new JournalReadError(`captureBaseline: ${label}（${path}）stat 回報的大小（${stat.size}）與實際讀到的位元組數（${raw.length}）不一致——可能在讀取期間被修改，無法安全建立 baseline`);
  }
  if (raw.length === 0) {
    return { offset: 0, records: [] };
  }
  const text = raw.toString('utf8');
  if (!text.endsWith('\n')) {
    throw new JournalReadError(`captureBaseline: ${label}（${path}）內容沒有以換行結尾——視為寫入被截斷，不得當作 baseline（缺尾端換行的截斷 JSON 必須被拒絕）`);
  }
  const lines = text.split('\n').filter(l => l.length > 0);
  const records: T[] = [];
  for (const line of lines) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (e) {
      throw new JournalReadError(`captureBaseline: ${label}（${path}）既有內容中有一行不是合法 JSON：${JSON.stringify(line)}（${e instanceof Error ? e.message : String(e)}）`);
    }
    records.push(validate(parsed, line));
  }
  return { offset: raw.length, records };
}

/** `captureBaseline` 的 audit.jsonl 專用便利包裝。 */
export function captureAuditBaseline(auditPath: string): Baseline<AuditRecord> {
  return captureBaseline(auditPath, 'audit.jsonl', validateAuditRecord);
}

/** `captureBaseline` 的 events.jsonl 專用便利包裝。 */
export function captureEventsBaseline(eventsPath: string): Baseline<EventEnvelope> {
  return captureBaseline(eventsPath, 'events.jsonl', validateEventEnvelope);
}

/**
 * 讀取 path 中「從 fromByteOffset 開始」的新增內容，逐行 JSON.parse 後交
 * 給 `validate` 做形狀驗證。任何一行解析失敗、形狀不合法、檔案不存在、
 * 讀取本身失敗、或新增內容非空但沒有以換行結尾（代表寫入被截斷），一律
 * throw JournalReadError——呼叫端必須把這個 throw 當作判定失敗，不得吞
 * 掉當成「沒有新內容」。
 */
export function readNewValidatedLines<T>(path: string, fromByteOffset: number, validate: (parsed: unknown, rawLine: string) => T, context: string): T[] {
  let raw: Buffer;
  try {
    raw = fs.readFileSync(path);
  } catch (e) {
    throw new JournalReadError(`${context}: 無法讀取 ${path}：${e instanceof Error ? e.message : String(e)}`);
  }
  if (raw.length < fromByteOffset) {
    throw new JournalReadError(
      `${context}: ${path} 目前長度（${raw.length}）小於基準 offset（${fromByteOffset}）——`
      + '檔案可能被截斷或替換，不是單純 append，無法安全窗口讀取',
    );
  }
  const newBytes = raw.subarray(fromByteOffset);
  if (newBytes.length === 0) return [];
  const text = newBytes.toString('utf8');
  if (!text.endsWith('\n')) {
    throw new JournalReadError(
      `${context}: ${path} 新增內容沒有以換行結尾——production 的寫入端（a.audit()／JSONLSink.Write／journal.Append）`
      + '每筆記錄後一律接換行，缺尾端換行代表這次寫入被截斷，不能當作完整內容處理',
    );
  }
  const lines = text.split('\n').filter(l => l.length > 0);
  const out: T[] = [];
  for (const line of lines) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (e) {
      throw new JournalReadError(`${context}: ${path} 新增內容中有一行不是合法 JSON：${JSON.stringify(line)}（${e instanceof Error ? e.message : String(e)}）`);
    }
    out.push(validate(parsed, line));
  }
  return out;
}

/** 已知的三種 gate journal record `_type`（`internal/gate/types.go`：GateRequest／ApprovalRecord／Transition）。 */
const KNOWN_RECORD_TYPES = ['gate_request', 'approval_record', 'transition'] as const;

function requireNonEmptyString(obj: Record<string, unknown>, key: string, typeLabel: string, rawLine: string): void {
  if (typeof obj[key] !== 'string' || obj[key] === '') {
    throw new JournalReadError(`validateGateOpRecord: ${typeLabel} record 缺少合法的 ${key}（實際：${JSON.stringify(obj[key])}）：${rawLine}`);
  }
}

/**
 * gate.jsonl 單筆 record 的形狀驗證——**review #4（#430）必修缺陷 3 修
 * 正**：舊版只驗證 `records` 是陣列，陣列裡的元素完全不驗證——
 * `readAllTransitions`／`readAllRecordsByType` 用 `rec._type === X` 過濾，
 * 讓 `{}`、`42`、`{_type:'transition'}`（缺 `approval_id`／`to`／`at`／
 * `cause`）這類壞掉的 record 被「過濾掉」而不是被拒絕——`diffNewTransitions`
 * 因此看不出任何差異，reviewer 的探針證實這會讓「journal 被刪除」這種
 * 應該 fail loud 的情境被誤判成「沒有新增 transition」。
 *
 * 改成：逐一验证每個 record 的 `_type` 必須是已知三種之一，且各自的必要
 * 欄位（對齊 `internal/gate/types.go` 沒有 `omitempty` 的欄位）必須存在
 * 且為非空字串——不符合就直接 throw，不是靜默跳過。刻意不做成通用 schema
 * framework（reviewer 明確裁定不需要），只驗這次任務會用到的三種形狀。
 */
function validateGateOpRecord(rec: unknown, rawLine: string): GateOpRecord {
  if (!isPlainObject(rec)) {
    throw new JournalReadError(`validateGateOpRecord: record 不是物件（實際：${JSON.stringify(rec)}）：${rawLine}`);
  }
  const type = rec._type;
  if (typeof type !== 'string' || !(KNOWN_RECORD_TYPES as readonly string[]).includes(type)) {
    throw new JournalReadError(`validateGateOpRecord: record 的 _type 不是已知的三種之一（實際：${JSON.stringify(type)}）：${rawLine}`);
  }
  if (type === 'gate_request') {
    requireNonEmptyString(rec, 'approval_id', 'gate_request', rawLine);
    requireNonEmptyString(rec, 'gate', 'gate_request', rawLine);
  } else if (type === 'approval_record') {
    requireNonEmptyString(rec, 'approval_id', 'approval_record', rawLine);
    requireNonEmptyString(rec, 'gate', 'approval_record', rawLine);
    requireNonEmptyString(rec, 'decision', 'approval_record', rawLine);
  } else {
    requireNonEmptyString(rec, 'approval_id', 'transition', rawLine);
    requireNonEmptyString(rec, 'to', 'transition', rawLine);
    requireNonEmptyString(rec, 'at', 'transition', rawLine);
    requireNonEmptyString(rec, 'cause', 'transition', rawLine);
  }
  return rec as GateOpRecord;
}

/**
 * gate.jsonl 每一行的形狀驗證——對齊 `internal/gate/types.go:68-71`：
 * `records` 缺失或不是陣列一律 throw（review #3 必修缺陷 1：不能用
 * `?? []` 當成空紀錄，那會讓「這行其實壞掉了」被誤判成「這行沒有任何
 * record」）。空陣列本身是合法形狀（一個 op 剛好零筆 record），只有
 * **欄位缺失或型別錯誤**才算壞掉。每個元素另外交給
 * `validateGateOpRecord` 逐一驗證（review #4 必修缺陷 3）。
 */
function validateGateOpLine(parsed: unknown, rawLine: string): GateOpLine {
  if (!isPlainObject(parsed)) {
    throw new JournalReadError(`validateGateOpLine: gate journal 一行不是物件：${rawLine}`);
  }
  if (!('records' in parsed) || !Array.isArray(parsed.records)) {
    throw new JournalReadError(`validateGateOpLine: gate journal 一行缺少合法的 records 陣列（op_id=${JSON.stringify(parsed.op_id)}，實際 records=${JSON.stringify(parsed.records)}）——不得當成空紀錄`);
  }
  const records = parsed.records.map(rec => validateGateOpRecord(rec, rawLine));
  return { op_id: parsed.op_id, at: parsed.at, records };
}

/**
 * audit.jsonl 一行的形狀驗證——對齊 `app.go` `a.audit()`：
 * `json.Marshal(map[string]any{"ts":..., "kind":..., "data": v})`，`ts`／
 * `kind` 皆為非空字串，`data` 鍵必為存在（值可為任意型別含 null）。
 */
function validateAuditRecord(parsed: unknown, rawLine: string): AuditRecord {
  if (!isPlainObject(parsed)) {
    throw new JournalReadError(`validateAuditRecord: audit record 不是物件：${rawLine}`);
  }
  if (typeof parsed.kind !== 'string' || parsed.kind === '') {
    throw new JournalReadError(`validateAuditRecord: audit record 缺少合法的 kind（實際：${JSON.stringify(parsed.kind)}）：${rawLine}`);
  }
  if (typeof parsed.ts !== 'string' || parsed.ts === '') {
    throw new JournalReadError(`validateAuditRecord: audit record 缺少合法的 ts（實際：${JSON.stringify(parsed.ts)}）：${rawLine}`);
  }
  if (!('data' in parsed)) {
    throw new JournalReadError(`validateAuditRecord: audit record 缺少 data 欄位：${rawLine}`);
  }
  return { ts: parsed.ts, kind: parsed.kind, data: parsed.data };
}

/**
 * events.jsonl 一行的形狀驗證——對齊 `internal/contract/envelope.go`
 * `Envelope`：`event_id`／`ts`／`provider`／`kind` 皆無 `omitempty`，
 * Go 端一律會序列化出這幾個鍵（即使值為空字串），因此可以拿來當作「這是
 * 一筆合法 envelope」的形狀判定依據。
 */
function validateEventEnvelope(parsed: unknown, rawLine: string): EventEnvelope {
  if (!isPlainObject(parsed)) {
    throw new JournalReadError(`validateEventEnvelope: event envelope 不是物件：${rawLine}`);
  }
  for (const key of ['event_id', 'ts', 'provider', 'kind'] as const) {
    if (typeof parsed[key] !== 'string') {
      throw new JournalReadError(`validateEventEnvelope: event envelope 缺少合法的 ${key}（實際：${JSON.stringify(parsed[key])}）：${rawLine}`);
    }
  }
  return parsed as unknown as EventEnvelope;
}

/**
 * 讀 audit.jsonl 新增內容，必要斷言沒有 kind==="spec_watch_error"（對應
 * app.go failLoudSpecWatch 的 a.audit 呼叫）。回傳新增的 record 陣列供呼叫
 * 端進一步檢視；發現 spec_watch_error 時 throw（不是回傳 boolean 讓呼叫端
 * 選擇性檢查）。
 */
export function assertNoAuditSpecWatchError(auditPath: string, fromByteOffset: number): AuditRecord[] {
  const records = readNewValidatedLines(auditPath, fromByteOffset, validateAuditRecord, 'assertNoAuditSpecWatchError');
  const bad = records.filter(r => r.kind === 'spec_watch_error');
  if (bad.length > 0) {
    throw new JournalReadError(`assertNoAuditSpecWatchError: audit.jsonl 新增內容含 ${bad.length} 筆 spec_watch_error：${JSON.stringify(bad)}`);
  }
  return records;
}

/**
 * 讀 events.jsonl 新增內容，必要斷言沒有 kind==="stream_error" &&
 * scope==="workspace"（對應 app.go failLoudSpecWatch 的 EmitWorkspace 呼叫，
 * contract.KindStreamError="stream_error"）。
 */
export function assertNoWorkspaceStreamError(eventsPath: string, fromByteOffset: number): EventEnvelope[] {
  const records = readNewValidatedLines(eventsPath, fromByteOffset, validateEventEnvelope, 'assertNoWorkspaceStreamError');
  const bad = records.filter(r => r.kind === 'stream_error' && r.scope === 'workspace');
  if (bad.length > 0) {
    throw new JournalReadError(`assertNoWorkspaceStreamError: events.jsonl 新增內容含 ${bad.length} 筆 workspace stream_error：${JSON.stringify(bad)}`);
  }
  return records;
}

function flattenTransitions(ops: GateOpLine[]): GateTransition[] {
  const out: GateTransition[] = [];
  for (const op of ops) {
    for (const rec of op.records) {
      if (rec._type === 'transition') out.push(rec as unknown as GateTransition);
    }
  }
  return out;
}

/**
 * 讀 gate.jsonl 全文，攤平出所有 transition 記錄（跨所有 GateOp 行）——
 * **寬容模式**：檔案不存在視為「尚未建立」，回傳空陣列。**只能用於明確
 * 的送核前初始狀態**（Gate1 首次送核前 gate journal 合法地還不存在），
 * 不能用於「gate 已經 active 之後」的檢查（review #4／#430 必修缺陷 3：
 * reviewer 的 `deleted-active-gate` 探針證明，若統一用寬容模式，active
 * gate 的 journal 檔案在兩次讀取之間被意外刪除，會被誤判成「沒有新增
 * transition」而不是一個必須 fail loud 的異常）。
 */
export function readAllTransitionsAllowMissing(gatePath: string): GateTransition[] {
  if (!fs.existsSync(gatePath)) return [];
  return flattenTransitions(readNewValidatedLines(gatePath, 0, validateGateOpLine, 'readAllTransitionsAllowMissing'));
}

/**
 * 讀 gate.jsonl 全文，攤平出所有 transition 記錄——**嚴格模式**：不做存在
 * 性預檢，檔案不存在時讓底層 `readNewValidatedLines` 的讀取失敗自然
 * 往外 throw。**S-N1／S-P1 的 before／after 都必須用這個版本**：呼叫這
 * 個函式的當下，Gate1（S-N1／S-P1 前置）已經核可過，gate.jsonl 理論上
 * 必然存在，若讀不到就是需要 fail loud 的異常，不是「還沒有 transition」
 * （review #4 必修缺陷 3）。
 */
export function readAllTransitionsRequireExists(gatePath: string): GateTransition[] {
  return flattenTransitions(readNewValidatedLines(gatePath, 0, validateGateOpLine, 'readAllTransitionsRequireExists'));
}

/**
 * 比對「探針前」與「探針後」兩次 readAllTransitions 的結果，回傳新增的
 * transition（以陣列長度差＋內容比對，不是只看長度——journal 是
 * append-only，新增的一定是 after 陣列尾端多出來的部分，這裡仍逐筆比對
 * before 是否為 after 的前綴，避免「長度剛好一樣但內容被替換」這種不該
 * 發生但值得防禦的情況）。
 */
export function diffNewTransitions(before: GateTransition[], after: GateTransition[]): GateTransition[] {
  if (after.length < before.length) {
    throw new JournalReadError('diffNewTransitions: after 的筆數少於 before——gate.jsonl 不是 append-only 地變長，無法安全比對');
  }
  for (let i = 0; i < before.length; i += 1) {
    if (JSON.stringify(before[i]) !== JSON.stringify(after[i])) {
      throw new JournalReadError(`diffNewTransitions: 第 ${i} 筆既有 transition 內容改變——journal 不應該是 append-only 以外的方式變動`);
    }
  }
  return after.slice(before.length);
}

/** 便利函式：新增的 transition 中，是否有任何一筆 to==="stale" 屬於指定 approvalId。 */
export function hasNewStaleTransitionFor(newTransitions: readonly GateTransition[], approvalId: string): boolean {
  return newTransitions.some(t => t.to === 'stale' && t.approval_id === approvalId);
}

/** 攤平 gate.jsonl 全部的 gate_request／approval_record（供 gate2.spec.ts／stale.spec.ts 共用的讀取邏輯）。 */
export function readAllRecordsByType(gatePath: string, type: 'gate_request' | 'approval_record'): Array<Record<string, unknown>> {
  if (!fs.existsSync(gatePath)) return [];
  const ops = readNewValidatedLines(gatePath, 0, validateGateOpLine, 'readAllRecordsByType');
  const out: Array<Record<string, unknown>> = [];
  for (const op of ops) {
    for (const rec of op.records) {
      if (isPlainObject(rec) && rec._type === type) out.push(rec);
    }
  }
  return out;
}

/** `readAllRecordsByType` 的窗口版（只看 fromByteOffset 之後新增的 op）——供拒絕案例「沒有新記錄」的斷言使用。 */
export function readNewRecordsByType(gatePath: string, fromByteOffset: number, type: 'gate_request' | 'approval_record'): Array<Record<string, unknown>> {
  const ops = readNewValidatedLines(gatePath, fromByteOffset, validateGateOpLine, 'readNewRecordsByType');
  const out: Array<Record<string, unknown>> = [];
  for (const op of ops) {
    for (const rec of op.records) {
      if (isPlainObject(rec) && rec._type === type) out.push(rec);
    }
  }
  return out;
}

// ---- 以下是三支 spec 共用的「找最新一筆記錄」便利函式——review #3 指出
// 三支 spec 各自手刻同一套（且都刻錯了）JSON.parse 迴圈是問題的根源之一，
// 集中到這裡只需要對一份實作寫 selftest。 ----

/** gate.jsonl 中，指定 gate 種類最新一筆 gate_request 的 approval_id；沒有任何一筆時回傳 null。 */
export function latestGateRequestApprovalId(gatePath: string, gate: 'gate1' | 'gate2'): string | null {
  const reqs = readAllRecordsByType(gatePath, 'gate_request').filter(r => r.gate === gate);
  if (reqs.length === 0) return null;
  return String(reqs[reqs.length - 1].approval_id);
}

/** 指定 approval_id 最新一筆 gate_request 的 bindings，攤平成 {kind: digest}。找不到時 throw。 */
export function bindingsOfGateRequest(gatePath: string, approvalId: string): Record<string, string> {
  const reqs = readAllRecordsByType(gatePath, 'gate_request').filter(r => r.approval_id === approvalId);
  if (reqs.length === 0) {
    throw new JournalReadError(`bindingsOfGateRequest: 找不到 approval_id=${approvalId} 的 gate_request`);
  }
  const rec = reqs[reqs.length - 1];
  const bindings = (rec.bindings as Array<{ kind: string; digest: string }> | undefined) ?? [];
  return Object.fromEntries(bindings.map(b => [b.kind, b.digest]));
}

/** 指定 approval_id 最新一筆 approval_record；沒有任何一筆時回傳 null。 */
export function latestApprovalRecordFor(gatePath: string, approvalId: string): Record<string, unknown> | null {
  const recs = readAllRecordsByType(gatePath, 'approval_record').filter(r => r.approval_id === approvalId);
  return recs.length > 0 ? recs[recs.length - 1] : null;
}
