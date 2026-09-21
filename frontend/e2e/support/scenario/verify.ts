// B3a-2b-1：純函式判定工具——測試端用來抓「approval 記錄跟預期不符」與
// 「per-run log／manifest 缺失或壞掉」。不對真 codex／claude 做任何事，
// 純粹讀取本包 fake 自己寫出來的證據並比對。
import fs from 'node:fs';
import type { Manifest, RawId, RunLogEntry } from './protocol.ts';

export interface ExpectedApproval {
  requestId: RawId;
  method: string;
  decision: 'accept' | 'decline';
}

// judgeApproval：逐欄位比對，回傳違規清單（空陣列＝符合）。id 用嚴格 `===`
// （型別＋值都要符合）——這正是「request id 保留型別」在判定端的鏡射：如果
// 中途有人把 id 轉型（number → string 或反過來），這裡要抓得到，不能因為
// `1 == "1"` 這種寬鬆比較被放過。
export function judgeApproval(actual: Manifest, expected: ExpectedApproval): string[] {
  const violations: string[] = [];
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

// parseManifest／parseRunLog：**不吞錯**——缺檔、JSON 壞掉、必要欄位缺漏一律
// throw，呼叫端（測試）用 assert.throws 抓；不像大多數 parser 那樣回
// null／undefined 讓呼叫端自己忘記檢查。
export function parseManifest(path: string): Manifest {
  let raw: string;
  try {
    raw = fs.readFileSync(path, 'utf8');
  } catch (e) {
    throw new Error(`parseManifest: cannot read ${path}: ${(e as Error).message}`);
  }
  let m: Manifest;
  try {
    m = JSON.parse(raw) as Manifest;
  } catch (e) {
    throw new Error(`parseManifest: malformed json in ${path}: ${(e as Error).message}`);
  }
  const required: Array<keyof Manifest> = ['scenario', 'argv', 'pid', 'startedAt'];
  for (const k of required) {
    if (m[k] === undefined) {
      throw new Error(`parseManifest: missing required field "${String(k)}" in ${path}`);
    }
  }
  if (m.exitCode === null) {
    throw new Error(`parseManifest: exitCode still null in ${path} (fake did not finish)`);
  }
  return m;
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
    let e: RunLogEntry;
    try {
      e = JSON.parse(line) as RunLogEntry;
    } catch (err) {
      throw new Error(`parseRunLog: malformed json at line ${i + 1} of ${path}: ${(err as Error).message}`);
    }
    if (typeof e.seq !== 'number' || typeof e.ts !== 'string' || typeof e.dir !== 'string') {
      throw new Error(`parseRunLog: missing required field at line ${i + 1} of ${path}: ${line}`);
    }
    if (e.seq <= prevSeq) {
      throw new Error(`parseRunLog: seq out of order at line ${i + 1} of ${path}: ${e.seq} <= ${prevSeq}`);
    }
    prevSeq = e.seq;
    entries.push(e);
  }
  return entries;
}
