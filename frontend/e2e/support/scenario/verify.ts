// B3a-2b-1：純函式判定工具——測試端用來抓「approval 記錄跟預期不符」與
// 「per-run log／manifest 缺失或壞掉」。不對真 codex／claude 做任何事，
// 純粹讀取本包 fake 自己寫出來的證據並比對。
import fs from 'node:fs';
import type { Frame, Manifest, RawId, RunLogEntry } from './protocol.ts';

export interface ExpectedApproval {
  requestId: RawId;
  method: string;
  decision: 'accept' | 'decline';
}

// judgeApproval：驗收判定（不是只比對 identity 欄位）。回傳違規清單（空陣列＝
// 通過）。id 用嚴格 `===`（型別＋值都要符合）——這正是「request id 保留型別」
// 在判定端的鏡射：如果中途有人把 id 轉型（number → string 或反過來），這裡要
// 抓得到，不能因為 `1 == "1"` 這種寬鬆比較被放過。
//
// R1 修正（B3a2b1-2b-1 缺陷修正）：先前只比對 3 個 identity 欄位，manifest 只要
// identity 對得上就回 []，即使 run 根本失敗（exitCode 非 0）、有 fatalError、
// 有 unknownMethodsSeen、甚至還沒結束（endedAt 為 null）都會被判定為通過。
// 現在明確核對「已結束、exitCode=0、無 fatal／unknown」與 identity 是否相符，
// 兩者缺一都算違規——不能只解析出可讀的失敗 manifest 就當作驗收成功。
export function judgeApproval(actual: Manifest, expected: ExpectedApproval): string[] {
  const violations: string[] = [];
  if (actual.endedAt === null) {
    violations.push('run not ended: endedAt is null');
  }
  if (actual.exitCode !== 0) {
    violations.push(`exitCode mismatch: got ${JSON.stringify(actual.exitCode)} want 0`);
  }
  if (actual.fatalError !== null) {
    violations.push(`fatalError present: ${JSON.stringify(actual.fatalError)}`);
  }
  if (actual.unknownMethodsSeen.length > 0) {
    violations.push(`unknownMethodsSeen not empty: ${JSON.stringify(actual.unknownMethodsSeen)}`);
  }
  if (actual.approvalRequestId !== expected.requestId) {
    violations.push(
      `approvalRequestId mismatch: got ${JSON.stringify(actual.approvalRequestId)} want ${JSON.stringify(expected.requestId)}`,
    );
  }
  if (actual.approvalMethod !== expected.method) {
    violations.push(`approvalMethod mismatch: got ${actual.approvalMethod} want ${expected.method}`);
  }
  if (actual.decisionReceived !== expected.decision) {
    violations.push(`decisionReceived mismatch: got ${actual.decisionReceived} want ${expected.decision}`);
  }
  return violations;
}

// isRawId／isManifestShape：型別守門——**不得用 `as Manifest` 當驗證**（R1）。
// 逐欄位檢查型別與方向（null 允許的欄位明確列出 null 分支），缺欄位或型別錯誤
// 一律視為不合格的 manifest。
function isRawId(v: unknown): v is RawId {
  return typeof v === 'string' || typeof v === 'number';
}

function isManifestShape(m: unknown, path: string): asserts m is Manifest {
  if (typeof m !== 'object' || m === null || Array.isArray(m)) {
    throw new Error(`parseManifest: not a JSON object in ${path}`);
  }
  const o = m as Record<string, unknown>;
  const fail = (field: string, why: string): never => {
    throw new Error(`parseManifest: field "${field}" ${why} in ${path}`);
  };
  if (typeof o.scenario !== 'string') fail('scenario', 'missing or not a string');
  if (!Array.isArray(o.argv) || !o.argv.every(x => typeof x === 'string')) fail('argv', 'missing or not string[]');
  if (typeof o.pid !== 'number') fail('pid', 'missing or not a number');
  if (typeof o.startedAt !== 'string') fail('startedAt', 'missing or not a string');
  if (!('endedAt' in o) || (o.endedAt !== null && typeof o.endedAt !== 'string')) fail('endedAt', 'missing or not string|null');
  if (!('exitCode' in o) || (o.exitCode !== null && typeof o.exitCode !== 'number')) fail('exitCode', 'missing or not number|null');
  if (!('approvalMethod' in o) || (o.approvalMethod !== null && typeof o.approvalMethod !== 'string')) {
    fail('approvalMethod', 'missing or not string|null');
  }
  if (!('approvalRequestId' in o) || (o.approvalRequestId !== null && !isRawId(o.approvalRequestId))) {
    fail('approvalRequestId', 'missing or not string|number|null');
  }
  if (!('decisionReceived' in o) || (o.decisionReceived !== null && typeof o.decisionReceived !== 'string')) {
    fail('decisionReceived', 'missing or not string|null');
  }
  if (!Array.isArray(o.unknownMethodsSeen) || !o.unknownMethodsSeen.every(x => typeof x === 'string')) {
    fail('unknownMethodsSeen', 'missing or not string[]');
  }
  if (!('fatalError' in o) || (o.fatalError !== null && typeof o.fatalError !== 'string')) {
    fail('fatalError', 'missing or not string|null');
  }
}

// parseManifest／parseRunLog：**不吞錯**——缺檔、JSON 壞掉、必要欄位缺漏或型別
//不對一律 throw，呼叫端（測試）用 assert.throws 抓；不像大多數 parser 那樣回
// null／undefined 讓呼叫端自己忘記檢查。
//
// R1 修正：本函式只負責「解析出可讀的 manifest 供診斷」——exitCode 非 0、
// fatalError 非 null 的失敗 manifest 一樣要能解析出來（否則失敗測試無法診斷
// 原因）；「這次 run 是否算驗收成功」一律交給 judgeApproval 判定，兩者不得
// 混在一起。exitCode／endedAt 仍是 null 代表 fake 根本沒收尾（既非成功也非
// 可診斷的失敗），這種半成品狀態才在本函式拒絕。
export function parseManifest(path: string): Manifest {
  let raw: string;
  try {
    raw = fs.readFileSync(path, 'utf8');
  } catch (e) {
    throw new Error(`parseManifest: cannot read ${path}: ${(e as Error).message}`);
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (e) {
    throw new Error(`parseManifest: malformed json in ${path}: ${(e as Error).message}`);
  }
  isManifestShape(parsed, path);
  const m = parsed;
  if (m.exitCode === null) {
    throw new Error(`parseManifest: exitCode still null in ${path} (fake did not finish)`);
  }
  return m;
}

const RUN_LOG_DIRS = new Set(['c2s', 's2c', 'meta']);

// isFrameShape：c2s／s2c 紀錄必須帶一個型別合法的 frame（R1：先前完全沒驗
// frame 存在與否或欄位型別／方向，讓 `dir:"bogus"`、缺 frame 的紀錄都被接受）。
function isFrameShape(f: unknown): f is Frame {
  if (typeof f !== 'object' || f === null || Array.isArray(f)) return false;
  const o = f as Record<string, unknown>;
  if ('id' in o && o.id !== undefined && typeof o.id !== 'string' && typeof o.id !== 'number') return false;
  if ('method' in o && o.method !== undefined && typeof o.method !== 'string') return false;
  if ('error' in o && o.error !== undefined) {
    const err = o.error as Record<string, unknown>;
    if (typeof err !== 'object' || err === null || typeof err.code !== 'number' || typeof err.message !== 'string') {
      return false;
    }
  }
  return true;
}

export function parseRunLog(path: string): RunLogEntry[] {
  let raw: string;
  try {
    raw = fs.readFileSync(path, 'utf8');
  } catch (e) {
    throw new Error(`parseRunLog: cannot read ${path}: ${(e as Error).message}`);
  }
  const lines = raw.split('\n').map(l => l.trim()).filter(l => l.length > 0);
  if (lines.length === 0) {
    throw new Error(`parseRunLog: empty log ${path}`);
  }
  const entries: RunLogEntry[] = [];
  let prevSeq = 0;
  for (const [i, line] of lines.entries()) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (err) {
      throw new Error(`parseRunLog: malformed json at line ${i + 1} of ${path}: ${(err as Error).message}`);
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error(`parseRunLog: entry at line ${i + 1} of ${path} is not a JSON object: ${line}`);
    }
    const e = parsed as Record<string, unknown>;
    if (typeof e.seq !== 'number' || !Number.isInteger(e.seq) || typeof e.ts !== 'string' || typeof e.dir !== 'string') {
      throw new Error(`parseRunLog: missing/malformed required field at line ${i + 1} of ${path}: ${line}`);
    }
    if (!RUN_LOG_DIRS.has(e.dir)) {
      throw new Error(`parseRunLog: dir "${e.dir}" is not one of c2s|s2c|meta at line ${i + 1} of ${path}`);
    }
    if ((e.dir === 'c2s' || e.dir === 's2c') && !isFrameShape(e.frame)) {
      throw new Error(`parseRunLog: dir "${e.dir}" missing/malformed frame at line ${i + 1} of ${path}: ${line}`);
    }
    if (e.seq <= prevSeq) {
      throw new Error(`parseRunLog: seq out of order at line ${i + 1} of ${path}: ${e.seq} <= ${prevSeq}`);
    }
    prevSeq = e.seq;
    entries.push(e as unknown as RunLogEntry);
  }
  return entries;
}
