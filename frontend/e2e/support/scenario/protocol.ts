// B3a-2b-1：Codex scenario fake 的共用 wire 型別與常數。
//
// 對齊 internal/codex/rpc.go 的 Frame（JSON-RPC 2.0 語意但省略 `jsonrpc` 欄位）與
// internal/codex/methods.go 的方法名子集——只列本包實際用到的方法，不宣稱完整
// enum（同 methods.go 的既有免責聲明）。
//
// RequestID 刻意用 `unknown`（原始 JSON 值，string 或 number）而非正規化成
// number：pinned schema 的 RequestId 是 string | number union，Go 端
// RequestID.UnmarshalJSON／MarshalJSON 對兩者都保留原始位元組不轉型，這裡跟著
// 保留，不在 TS 側先轉型別（否則就沒辦法驗證「型別保留」這件事本身）。
export type RawId = string | number;

export interface Frame {
  id?: RawId;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string };
}

export const Method = {
  Initialize: 'initialize',
  Initialized: 'initialized',
  ThreadStart: 'thread/start',
  ThreadResume: 'thread/resume',
  TurnStart: 'turn/start',
  ItemStarted: 'item/started',
  ItemCompleted: 'item/completed',
  TurnCompleted: 'turn/completed',
  CmdExecRequestApproval: 'item/commandExecution/requestApproval',
  FileChangeRequestApproval: 'item/fileChange/requestApproval',
} as const;

export type ApprovalMethod =
  | typeof Method.CmdExecRequestApproval
  | typeof Method.FileChangeRequestApproval;

export type Decision = 'accept' | 'decline';

// ScenarioConfig：單一明確場景／每次子行程一份（B3a-2b-1 範圍表 §介面）。
export interface ScenarioConfig {
  scenario: string;
  threadId: string;
  turnId: string;
  itemId: string;
  approvalMethod: ApprovalMethod;
  // approvalRequestId：刻意用字串（非數字字面量）——正面證明 server→client
  // request 的 id 型別經過真正的 Go Conn 往返後不被轉型（見 B3a2b1 probe）。
  approvalRequestId: string;
  // afterApproval：approval response 抵達後要送出的內容事件序列（可辨識內容，
  // 對應 item/started＋item/completed），最後一律以 turn/completed 收尾。
  afterApproval: Array<{ type: 'itemStarted' | 'itemCompleted'; text: string }>;
  turnStatus: 'completed' | 'failed';
}

export interface RunLogEntry {
  seq: number;
  ts: string;
  dir: 'c2s' | 's2c' | 'meta';
  note?: string;
  frame?: Frame;
}

export interface Manifest {
  scenario: string;
  argv: string[];
  pid: number;
  startedAt: string;
  endedAt: string | null;
  exitCode: number | null;
  approvalMethod: string | null;
  approvalRequestId: RawId | null;
  decisionReceived: string | null;
  unknownMethodsSeen: string[];
  fatalError: string | null;
}

export function nowIso(): string {
  return new Date().toISOString();
}

// splitFrames：把累積的 stdin buffer 依 `\n` 切成完整行，回傳（已完成的行陣列,
// 剩餘未完成的 buffer）——同 Go bufio.Scanner 對 JSONL 的切法，空白行略過。
export function splitFrames(buf: string): { lines: string[]; rest: string } {
  const parts = buf.split('\n');
  const rest = parts.pop() ?? '';
  const lines = parts.map(l => l.trim()).filter(l => l.length > 0);
  return { lines, rest };
}
