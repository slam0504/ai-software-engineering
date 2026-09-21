// B3a-2b-2 F1b：假 Claude CLI runtime（**固定一案：fresh-start ＋ 單一 allow**）。
//
// 定位：這支程式假裝自己是 managed claude CLI，被 `claudeScenarioCli.ts` 產生的
// wrapper 以**真 App 會用的 argv**啟動，然後：
//   1. 驗 argv 與 stdin 的 user stream-json 是否與預定一致（不符一律拒絕，不猜測）
//   2. 讀 `--mcp-config` 指到的設定，核對 command canonical path 與**核定 binary
//      SHA256**、args=[mcp-approval,--socket,<本次 socket>]
//   3. **依讀到的 config 實際 spawn 真 workbench binary 的 mcp-approval 子程序**
//   4. 走完整 MCP 往返並**原樣保存收到的每一行**
//   5. 只有在往返、判定、OS 身分核對與收尾**全部成功**之後，才送 assistant 與 result
//
// reviewer #353 指出並已修正的四個缺陷：
//   (1) stdin **不能等 EOF**。真 App 是 MultiTurn=true、stdin 保持開啟，等 EOF 會
//       永遠送不出 init。改成有界讀「第一行」，並分別處理 error／EOF／逾時。
//       **不得要求驅動端先關 stdin 來掩蓋這件事。**
//   (2) 成功不得無條件宣告。送 assistant/result 之前必須實際跑已審的
//       `judgeMcpTranscript`，且往返、OS 觀測、證據保存與收尾都成功；任何一項失敗
//       一律非零退出且**不送**預定完成內容。
//   (3) **收到的原始行一行都不能丟**。額外／重複／無法解析／結束時的半行都要反映
//       成失敗，不得只挑兩個想要的 response 湊成五筆。
//   (4) 共用、可重入的收尾路徑：child error／stdin error／訊號／任何例外都走同一條
//       cleanup，保留診斷後以期限終止。
//
// 三件刻意不做的事：
//   - **不執行 `input.command`**。它只是 approval 的展示資料，不是要跑的指令。
//   - **不從 config 自身推導白名單**。期望值由獨立 fixture 提供；config 只是被驗
//     的對象。讀 config 的結果直接決定實際 spawn 什麼，兩者不得脫鉤。
//   - **不把診斷寫進協定 stdout**。stdout 只有 stream-json 事件，其餘走 stderr
//     與證據檔。
//
// argv 來源是一手讀碼：internal/claude/session.go Config.args() 現況
// （-p／--input-format／--output-format／--verbose／--include-partial-messages／
//  --settings／--permission-prompt-tool／--mcp-config／--strict-mcp-config），
// 以及 app.go startClaude 傳入的 SettingsJSON 與 PermissionPromptTool 實值。
import { execFileSync, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import {
  APPROVAL_TOOL_NAME,
  MCP_PROTOCOL_VERSION,
  type ClaudeApprovalExpectation,
  type McpEvent,
} from './claudeApprovalProtocol.ts';
import { judgeMcpTranscript } from './claudeApprovalJudge.ts';

// --- production 常數（一手讀碼，不可在此改寫） -----------------------------
/** app.go startClaude：SettingsJSON 實值。 */
export const SETTINGS_JSON = '{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}';
/** app.go startClaude：PermissionPromptTool 實值。 */
export const PERMISSION_PROMPT_TOOL = 'mcp__workbench__approval_prompt';

/** 退出碼語意（驅動端據此分類，不靠 stdout 猜）。 */
export const EXIT_CONTRACT_VIOLATION = 19;  // argv／stdin／config 不符預定
export const EXIT_UNREAPED = 20;            // 子程序收不乾淨
export const EXIT_JUDGE_FAILED = 21;        // 往返／判定／OS 核對失敗
export const EXIT_UNEXPECTED = 22;          // 未預期例外
export const EXIT_SIGNALLED = 23;           // 收到 SIGTERM／SIGINT

// ---------------------------------------------------------------------------
// 1. argv
// ---------------------------------------------------------------------------
/**
 * 本案（fresh start、有 permission prompt tool、有 settings、**無 resume**）下，
 * internal/claude/session.go Config.args() 會產生的**完整**參數序列。
 */
export function expectedConversationArgv(opts: { mcpConfigPath: string }): string[] {
  return [
    '-p', '--input-format', 'stream-json', '--output-format', 'stream-json',
    '--verbose', '--include-partial-messages',
    '--settings', SETTINGS_JSON,
    '--permission-prompt-tool', PERMISSION_PROMPT_TOOL,
    '--mcp-config', opts.mcpConfigPath, '--strict-mcp-config',
  ];
}

/** `--version` 探測必須**恰好一個參數**（app.go:3365 exec.Command(bin,"--version")）。 */
export function validateVersionArgv(argv: string[]): string[] {
  if (argv.length === 1 && argv[0] === '--version') return [];
  return [`--version 探測必須恰好一個參數 "--version"，實際 ${JSON.stringify(argv)}`];
}

/**
 * 對話 argv 必須與期望**逐位置完全相同**：多餘、缺少、重複、順序不同、未知參數
 * 都失敗。本案明確不接受 --resume。
 */
export function validateConversationArgv(argv: unknown, expected: string[]): string[] {
  if (!Array.isArray(argv) || argv.some(a => typeof a !== 'string')) {
    return [`argv 必須是字串陣列，實際 ${JSON.stringify(argv)}`];
  }
  const a = argv as string[];
  const v: string[] = [];
  if (a.includes('--resume')) {
    v.push('本案是 fresh start，argv 不得含 --resume');
  }
  const known = new Set(expected.filter(x => x.startsWith('--') || x === '-p'));
  for (const tok of a) {
    if ((tok.startsWith('--') || tok === '-p') && !known.has(tok)) {
      v.push(`未知參數 ${JSON.stringify(tok)}（不在 Config.args() 現況內）`);
    }
  }
  const seen = new Map<string, number>();
  for (const tok of a) {
    if (tok.startsWith('--') || tok === '-p') seen.set(tok, (seen.get(tok) ?? 0) + 1);
  }
  for (const [tok, n] of seen) {
    if (n > 1) v.push(`參數 ${JSON.stringify(tok)} 重複出現 ${n} 次`);
  }
  if (a.length !== expected.length) {
    v.push(`argv 長度應為 ${expected.length}，實際 ${a.length}`);
  }
  const n = Math.min(a.length, expected.length);
  for (let i = 0; i < n; i += 1) {
    if (a[i] !== expected[i]) {
      v.push(`argv[${i}] 應為 ${JSON.stringify(expected[i])}，實際 ${JSON.stringify(a[i])}`);
    }
  }
  return v;
}

// ---------------------------------------------------------------------------
// 2. stdin（user stream-json）
// ---------------------------------------------------------------------------
export interface FirstLineResult { line: string; rest: string; }

/**
 * **有界**讀出 stdin 的第一行，**不等 EOF**。
 *
 * 真 App 以 MultiTurn=true 啟動 CLI，stdin 會一直開著（session.go Start() 送完
 * 第一行 user message 後保留 stdin 給後續輪次）。若在這裡等 EOF，CLI 就永遠不會
 * 送出 init——這是 reviewer #353 實測到的行為（完整首行寫入後保持 pipe 開啟，
 * 800ms 仍零 stdout）。因此**只等到換行為止**，並把三種壞情況分開回報：
 * 逾時、在收到完整一行之前 EOF、串流錯誤。
 */
export function readFirstLineBounded(
  stream: NodeJS.ReadableStream,
  timeoutMs: number,
): Promise<FirstLineResult> {
  return new Promise<FirstLineResult>((resolve, reject) => {
    let buf = '';
    let settled = false;
    const done = (): boolean => {
      if (settled) return true;
      settled = true;
      clearTimeout(timer);
      stream.removeListener('data', onData);
      stream.removeListener('end', onEnd);
      stream.removeListener('error', onError);
      return false;
    };
    const onData = (chunk: unknown): void => {
      buf += typeof chunk === 'string' ? chunk : String(chunk);
      const i = buf.indexOf('\n');
      if (i < 0) return;
      if (done()) return;
      resolve({ line: buf.slice(0, i), rest: buf.slice(i + 1) });
    };
    const onEnd = (): void => {
      if (done()) return;
      reject(new Error(`stdin 在收到完整一行之前就結束（已收 ${JSON.stringify(buf)}）`));
    };
    const onError = (e: unknown): void => {
      if (done()) return;
      reject(new Error(`stdin 讀取錯誤：${String(e)}`));
    };
    const timer = setTimeout(() => {
      if (done()) return;
      reject(new Error(`等待 stdin 首行逾時（${timeoutMs}ms，已收 ${JSON.stringify(buf)}）`));
    }, timeoutMs);
    stream.on('data', onData);
    stream.on('end', onEnd);
    stream.on('error', onError);
    if (typeof (stream as { resume?: () => void }).resume === 'function') {
      (stream as { resume: () => void }).resume();
    }
  });
}

/**
 * internal/claude/session.go Start() 送出的第一行：
 * {"type":"user","message":{"role":"user","content":[{"type":"text","text":<prompt>}]}}
 */
export function validateUserStreamJson(line: string, expectedPrompt: string): string[] {
  let parsed: unknown;
  try { parsed = JSON.parse(line); }
  catch (e) { return [`stdin 第一行不是合法 JSON：${String(e)}`]; }
  const v: string[] = [];
  const o = parsed as Record<string, unknown> | null;
  if (typeof o !== 'object' || o === null || Array.isArray(o)) return ['stdin 第一行應為物件'];
  if (o.type !== 'user') v.push(`stdin type 應為 "user"，實際 ${JSON.stringify(o.type)}`);
  const msg = o.message as Record<string, unknown> | undefined;
  if (typeof msg !== 'object' || msg === null) return v.concat(['stdin 缺 message 物件']);
  if (msg.role !== 'user') v.push(`stdin message.role 應為 "user"，實際 ${JSON.stringify(msg.role)}`);
  const content = msg.content;
  if (!Array.isArray(content) || content.length !== 1) {
    return v.concat([`stdin message.content 應恰有 1 筆，實際 ${JSON.stringify(content)}`]);
  }
  const c0 = content[0] as Record<string, unknown>;
  if (c0?.type !== 'text') v.push(`stdin content[0].type 應為 "text"，實際 ${JSON.stringify(c0?.type)}`);
  if (c0?.text !== expectedPrompt) {
    v.push(`stdin content[0].text 與預定 prompt 不符：實際 ${JSON.stringify(c0?.text)} 期望 ${JSON.stringify(expectedPrompt)}`);
  }
  return v;
}

// ---------------------------------------------------------------------------
// 3. MCP config
// ---------------------------------------------------------------------------
/**
 * 從 argv 取出 `--mcp-config` 的值——**這是真 App（session.go Config.args()）傳進來
 * 的 host.mcpPath**。F2 起 config 的來源只有這裡，不再由 `FAKE_CLAUDE_MCP_CONFIG`
 * 指向假 CLI 自造的檔案（reviewer #365：wrapper 不得強迫讀 F1b 自造 config）。
 */
export function readMcpConfigPathFromArgv(argv: string[]): { path: string | null; violations: string[] } {
  const i = argv.indexOf('--mcp-config');
  if (i < 0) return { path: null, violations: ['argv 缺少 --mcp-config，無法取得真 App 的 config 路徑'] };
  if (argv.indexOf('--mcp-config', i + 1) >= 0) {
    return { path: null, violations: ['argv 出現多個 --mcp-config，無法決定唯一 config'] };
  }
  const v = argv[i + 1];
  if (typeof v !== 'string' || v === '' || v.startsWith('--')) {
    return { path: null, violations: [`--mcp-config 的值不合法：${JSON.stringify(v)}`] };
  }
  return { path: v, violations: [] };
}

/**
 * **獨立 fixture 期望**——不由 config 自身推導。commandPath 是已核定真 binary 的
 * canonical path，commandSha256 是它被核定當下的雜湊。
 */
/**
 * 動態路徑的判定 policy。**刻意做成互斥的聯集**——不能兩種同時存在，
 * 也不能兩種都缺（reviewer #367）。
 *   fixed        ：F1b 離線案，路徑由驅動端固定，**全等比對**（既有契約未放寬）
 *   appStateDir  ：F2，路徑由真 App 動態決定，改驗「canonical 父目錄 ＋ App 檔名契約」
 */
export type PathPolicy =
  | { kind: 'fixed'; path: string }
  | { kind: 'appStateDir'; stateDir: string };

export interface McpConfigExpectation {
  commandPath: string;
  commandSha256: string;
  /** socket 路徑的判定 policy（JSON 載入，**runtime 驗形狀**）。 */
  socket: unknown;
}

/**
 * 依 policy 判定一個動態路徑。
 *
 * `appStateDir` 的判定**不用字串 startsWith**——那會被 `.../.workbench/../../x`
 * 這種 traversal 繞過（reviewer #367 實測 violations=[]）。改成：
 *   1. 必須是絕對路徑
 *   2. `realpath(dirname(p))` 必須**等於** `realpath(stateDir)`（吃掉 `..` 與 symlink）
 *   3. basename 必須符合 App 的實際命名契約
 */
export function judgePathAgainstPolicy(
  actual: string, policy: unknown, basenameRe: RegExp, label: string,
): string[] {
  // **policy 來自 JSON，TS union 不提供 runtime 保證**：先驗形狀（reviewer #369）。
  const shape = validatePathPolicy(policy, label);
  if (shape.violations.length > 0) return shape.violations;
  const pol = shape.policy as PathPolicy;

  if (pol.kind === 'fixed') {
    return actual === pol.path
      ? []
      : [`${label} 應為 ${JSON.stringify(pol.path)}，實際 ${JSON.stringify(actual)}`];
  }
  const v: string[] = [];
  if (!path.isAbsolute(actual)) {
    return [`${label} 應為絕對路徑，實際 ${JSON.stringify(actual)}`];
  }
  let stateReal: string;
  try { stateReal = fs.realpathSync(pol.stateDir); }
  catch (e) { return [`${label}: 無法解析 stateDir ${JSON.stringify(pol.stateDir)}：${String(e)}`]; }

  let parentReal: string;
  try { parentReal = fs.realpathSync(path.dirname(actual)); }
  catch (e) { return [`${label}: 無法解析父目錄 ${JSON.stringify(path.dirname(actual))}：${String(e)}`]; }
  if (parentReal !== stateReal) {
    v.push(`${label} 的 canonical 父目錄應為 ${JSON.stringify(stateReal)}，實際 ${JSON.stringify(parentReal)}`
      + `（原路徑 ${JSON.stringify(actual)}——traversal／symlink 逃逸一律拒絕）`);
  }
  const base = path.basename(actual);
  if (!basenameRe.test(base)) {
    v.push(`${label} 的檔名不符 App 命名契約 ${String(basenameRe)}，實際 ${JSON.stringify(base)}`);
  }

  // **leaf 本身也要驗**：只比 dirname 會讓 `<stateDir>/mcp-ws.json -> /outside.json`
  // 這種 leaf symlink 逃逸整個過關（reviewer #369 實測 violations=[]）。
  let lst: import('node:fs').Stats;
  try { lst = fs.lstatSync(actual); }
  catch (e) {
    v.push(`${label} 指向的檔案不存在或無法 lstat：${String(e)}`
      + '——appStateDir policy 要求 leaf 必須實際存在才能核對身分');
    return v;
  }
  if (lst.isSymbolicLink()) {
    v.push(`${label} 是 symlink，一律拒絕（${JSON.stringify(actual)} → ${JSON.stringify(safeReadlink(actual))}）`);
    return v;
  }
  try {
    const leafReal = fs.realpathSync(actual);
    if (path.dirname(leafReal) !== stateReal) {
      v.push(`${label} 的 canonical 路徑逃出 stateDir：${JSON.stringify(leafReal)}`);
    }
  } catch (e) {
    v.push(`${label}: 無法解析 canonical 路徑：${String(e)}`);
  }
  return v;
}

function safeReadlink(p: string): string {
  try { return fs.readlinkSync(p); } catch { return '(無法讀取)'; }
}

/**
 * policy 物件的 runtime 驗證：拒絕未知 kind、缺欄位、**兩種 policy 欄位混用**。
 * 這些都是 JSON 載入後才可能出現的形狀，TS 的 union 型別攔不到。
 */
export function validatePathPolicy(
  policy: unknown, label: string,
): { policy: PathPolicy | null; violations: string[] } {
  if (typeof policy !== 'object' || policy === null || Array.isArray(policy)) {
    return { policy: null, violations: [`${label}: policy 應為物件，實際 ${JSON.stringify(policy)}`] };
  }
  const o = policy as Record<string, unknown>;
  const hasPath = Object.hasOwn(o, 'path');
  const hasStateDir = Object.hasOwn(o, 'stateDir');
  if (hasPath && hasStateDir) {
    return { policy: null, violations: [`${label}: policy 同時帶 path 與 stateDir——兩種 policy 不得並存`] };
  }
  if (o.kind === 'fixed') {
    if (typeof o.path !== 'string' || o.path === '') {
      return { policy: null, violations: [`${label}: fixed policy 的 path 應為非空字串，實際 ${JSON.stringify(o.path)}`] };
    }
    return { policy: { kind: 'fixed', path: o.path }, violations: [] };
  }
  if (o.kind === 'appStateDir') {
    if (typeof o.stateDir !== 'string' || o.stateDir === '') {
      return { policy: null, violations: [`${label}: appStateDir policy 的 stateDir 應為非空字串，實際 ${JSON.stringify(o.stateDir)}`] };
    }
    return { policy: { kind: 'appStateDir', stateDir: o.stateDir }, violations: [] };
  }
  return { policy: null, violations: [`${label}: 未知的 policy kind ${JSON.stringify(o.kind)}（只接受 "fixed" 與 "appStateDir"）`] };
}

/** App 的實際命名：`approvalSockPath` → `approval-<index>.sock`（session_host.go:113-115）。 */
export const APP_SOCKET_BASENAME_RE = /^approval-\d+\.sock$/;
/** App 的實際命名：`host.mcpPath` → `mcp-<WSID>.json`（app.go:7395）。 */
export const APP_MCP_CONFIG_BASENAME_RE = /^mcp-.+\.json$/;

export interface ResolvedMcpConfig {
  /** config 原文（證據用，原樣保存）。 */
  raw: string;
  command: string;
  args: string[];
}

/**
 * 驗 config 內容。**回傳的 resolved 就是後續 spawn 實際要用的值**——驗過的東西
 * 與跑起來的東西必須是同一份，不得各走各的。
 */
export function validateMcpConfig(
  raw: string,
  expectation: McpConfigExpectation,
): { violations: string[]; resolved: ResolvedMcpConfig | null } {
  let parsed: unknown;
  try { parsed = JSON.parse(raw); }
  catch (e) { return { violations: [`mcp config 不是合法 JSON：${String(e)}`], resolved: null }; }
  const obj = parsed as any;
  const servers = obj?.mcpServers;
  if (typeof servers !== 'object' || servers === null || Array.isArray(servers)) {
    return { violations: ['mcp config 缺 mcpServers 物件'], resolved: null };
  }
  const names = Object.keys(servers);
  if (names.length !== 1 || names[0] !== 'workbench') {
    return { violations: [`mcp config 應恰有唯一一個名為 "workbench" 的 server，實際 ${JSON.stringify(names)}`], resolved: null };
  }
  const s = servers.workbench;
  if (typeof s !== 'object' || s === null) {
    return { violations: ['mcp config workbench 應為物件'], resolved: null };
  }
  const v: string[] = [];
  if (s.type !== 'stdio') v.push(`mcp config type 應為 "stdio"，實際 ${JSON.stringify(s.type)}`);
  if (typeof s.command !== 'string' || s.command === '') {
    return { violations: v.concat(['mcp config command 應為非空字串']), resolved: null };
  }
  if (!Array.isArray(s.args) || s.args.some((x: unknown) => typeof x !== 'string')) {
    return { violations: v.concat(['mcp config args 應為字串陣列']), resolved: null };
  }
  // command 必須與**獨立期望**的 canonical path 相同
  let canonical: string;
  try { canonical = fs.realpathSync(s.command); }
  catch (e) {
    v.push(`mcp config command 無法解析為實際檔案：${String(e)}`);
    return { violations: v, resolved: null };
  }
  if (canonical !== expectation.commandPath) {
    v.push(`mcp config command canonical path 與核定值不符：實際 ${JSON.stringify(canonical)} 核定 ${JSON.stringify(expectation.commandPath)}`);
  }
  // args 形狀固定：恰三個、前兩個是字面值；socket 值依互斥 policy 判定。
  if (s.args.length !== 3 || s.args[0] !== 'mcp-approval' || s.args[1] !== '--socket') {
    v.push(`mcp config args 應為 ["mcp-approval","--socket",<socket>]，實際 ${JSON.stringify(s.args)}`);
  } else {
    v.push(...judgePathAgainstPolicy(s.args[2] as string, expectation.socket,
      APP_SOCKET_BASENAME_RE, 'mcp config socket'));
  }
  return { violations: v, resolved: { raw, command: s.command, args: s.args as string[] } };
}

/** 核對 command 指到的檔案雜湊是否等於**核定的** binary SHA256。 */
export function verifyCommandBinary(commandPath: string, expectedSha256: string): string[] {
  let buf: Buffer;
  try { buf = fs.readFileSync(commandPath); }
  catch (e) { return [`無法讀取 command binary：${String(e)}`]; }
  const got = crypto.createHash('sha256').update(buf).digest('hex');
  if (got !== expectedSha256) {
    return [`command binary SHA256 與核定值不符：實際 ${got} 核定 ${expectedSha256}`];
  }
  return [];
}

// ---------------------------------------------------------------------------
// 4. stream-json 事件（只放本案需要的欄位）
// ---------------------------------------------------------------------------
export function buildInitEvent(sessionId: string): string {
  return JSON.stringify({ type: 'system', subtype: 'init', session_id: sessionId });
}
export function buildAssistantEvent(sessionId: string, text: string): string {
  return JSON.stringify({
    type: 'assistant', session_id: sessionId,
    message: { role: 'assistant', content: [{ type: 'text', text }] },
  });
}
export function buildResultEvent(sessionId: string, text: string): string {
  return JSON.stringify({ type: 'result', subtype: 'success', session_id: sessionId, result: text });
}

// ---------------------------------------------------------------------------
// 5. 子程序所有權與有界清理
// ---------------------------------------------------------------------------
/** `ps` 的三種結果。**「查不到」與「查不成」必須分開**——後者不得當成程序已消失。 */
export type OsProbeState = 'present' | 'absent' | 'error';

export interface PsRow { pid: number; ppid: number; pgid: number; stat: string; }

export interface OsProbe {
  state: OsProbeState;
  /** ps 的 stdout（已 trim）；present 時非空。 */
  raw: string | null;
  stderr: string | null;
  status: number | null;
  /** state==='error' 時的診斷；其餘為 null。 */
  error: string | null;
  parsed: PsRow | null;
}

/** 解析 `ps -o pid=,ppid=,pgid=,stat=` 的單列輸出。 */
export function parsePsRow(raw: string): PsRow | null {
  const t = raw.trim().split(/\s+/);
  if (t.length < 4) return null;
  const [pid, ppid, pgid] = [Number(t[0]), Number(t[1]), Number(t[2])];
  if (!Number.isInteger(pid) || !Number.isInteger(ppid) || !Number.isInteger(pgid)) return null;
  return { pid, ppid, pgid, stat: t[3] };
}

/**
 * 以 OS 為來源觀測子程序身分，**回傳三態**。
 *
 * 實測到的訊號（/bin/ps，darwin）：
 *   present：rc=0 且 stdout 非空
 *   absent ：rc=1 且 stdout 與 **stderr 都空**（真正的查無此程序）
 *   error  ：其餘一切——spawn 失敗（ENOENT/EACCES）、逾時、rc=1 但 stderr 有訊息
 *            （例如 "process id too large"）、rc=0 卻無輸出。
 *
 * **觀測失敗不得被當成「程序不存在」。** 這是 reviewer #355 第 3 點：舊版把所有
 * execFileSync 例外都回 raw=null，於是 EACCES 這種查不成的情況被判成收乾淨了。
 *
 * 注意 pid 有被作業系統重用的理論可能，這裡只當「本次持有期間」的佐證。
 */
export function probeChildOs(pid: number, timeoutMs = 5_000): OsProbe {
  try {
    const out = execFileSync('/bin/ps', ['-o', 'pid=,ppid=,pgid=,stat=', '-p', String(pid)],
      { encoding: 'utf8', timeout: timeoutMs });
    const raw = out.trim();
    if (raw === '') {
      return { state: 'error', raw: '', stderr: null, status: 0, parsed: null,
        error: 'ps 以 0 結束卻無輸出——非預期，不得當成查無程序' };
    }
    const parsed = parsePsRow(raw);
    if (parsed === null) {
      return { state: 'error', raw, stderr: null, status: 0, parsed: null,
        error: `ps 輸出無法解析為 pid/ppid/pgid/stat：${JSON.stringify(raw)}` };
    }
    return { state: 'present', raw, stderr: null, status: 0, error: null, parsed };
  } catch (e) {
    const err = e as { status?: number | null; signal?: string | null; code?: string;
      stdout?: unknown; stderr?: unknown; message?: string };
    const status = typeof err.status === 'number' ? err.status : null;
    const so = err.stdout === undefined || err.stdout === null ? '' : String(err.stdout).trim();
    const se = err.stderr === undefined || err.stderr === null ? '' : String(err.stderr).trim();
    if (err.code !== undefined) {
      return { state: 'error', raw: so === '' ? null : so, stderr: se === '' ? null : se, status,
        parsed: null, error: `ps 無法執行（code=${err.code}）：${err.message ?? ''}` };
    }
    if (err.signal !== undefined && err.signal !== null) {
      return { state: 'error', raw: so === '' ? null : so, stderr: se === '' ? null : se, status,
        parsed: null, error: `ps 被訊號中止（${err.signal}），視為觀測失敗` };
    }
    if (status === 1 && so === '' && se === '') {
      return { state: 'absent', raw: null, stderr: null, status: 1, error: null, parsed: null };
    }
    return { state: 'error', raw: so === '' ? null : so, stderr: se === '' ? null : se, status,
      parsed: null, error: `ps 以非預期方式結束（status=${String(status)}，stderr=${JSON.stringify(se)}）` };
  }
}

export interface ChildObservation {
  /** 本行程 pid——子程序的 ppid 必須等於它，否則不是我們持有的那一個。 */
  selfPid: number;
  /** 由本行程 spawn 直接取得——**實際觀測**。 */
  observedPid: number | null;
  /** spawn 本身失敗（ENOENT 等）的原文；非 null 代表子程序從未起來。 */
  spawnError: string | null;
  /** 往返期間的 OS 觀測，預期 present 且身分相符。 */
  psDuring: OsProbe | null;
  /** 退出**之後**再探，預期 absent。**單有 exit event 不算 OS 核對過。** */
  psAfter: OsProbe | null;
  exitCode: number | null;
  exitSignal: string | null;
  /** stdout 是否在期限內真正結束（資料收齊），而不是只沖了當下的 buffer。 */
  stdoutDrained: boolean;
  /** stderr 是否在期限內真正結束。 */
  stderrDrained: boolean;
  /** 清理過程的逐步紀錄（含每一步的結果）。 */
  cleanupSteps: string[];
  /** 無法確認收乾淨時為 true——呼叫端必須以非零退出。 */
  unreaped: boolean;
}

export interface CleanupGrace { pipeWaitMs: number; termWaitMs: number; killWaitMs: number; }
export const DEFAULT_GRACE: CleanupGrace = { pipeWaitMs: 10_000, termWaitMs: 10_000, killWaitMs: 5_000 };

export interface CleanupOutcome {
  ran: boolean;
  exitCode: number | null;
  exitSignal: string | null;
  unreaped: boolean;
  psAfter: OsProbe | null;
  steps: string[];
}

/**
 * **跨層共用的收尾把手。**
 *
 * reviewer #355 第 1 點：child 不能只藏在 `runMcpRoundTrip` 的 local scope，否則
 * 訊號路徑直接 `process.exit` 會把子程序丟成孤兒（實測 ppid 變 1）。這個 handle
 * 讓訊號／例外路徑能 (a) 取消仍在等待的往返、(b) 取得**與正常路徑同一個**
 * cleanup Promise，重複呼叫不會跑兩次收尾。
 */
export interface RoundTripHandle {
  child: ChildProcessWithoutNullStreams | null;
  cancelled: string | null;
  cancel(reason: string): void;
  onCancel(fn: () => void): void;
  registerCleanup(fn: () => Promise<CleanupOutcome>): void;
  cleanup(): Promise<CleanupOutcome>;
}

export function createRoundTripHandle(): RoundTripHandle {
  const listeners: Array<() => void> = [];
  let registered: (() => Promise<CleanupOutcome>) | null = null;
  let promise: Promise<CleanupOutcome> | null = null;
  const h: RoundTripHandle = {
    child: null,
    cancelled: null,
    cancel(reason: string): void {
      if (h.cancelled !== null) return;
      h.cancelled = reason;
      for (const f of listeners) { try { f(); } catch { /* 取消通知不得反過來炸掉收尾 */ } }
    },
    onCancel(fn: () => void): void {
      listeners.push(fn);
      if (h.cancelled !== null) fn();
    },
    registerCleanup(fn: () => Promise<CleanupOutcome>): void { registered = fn; },
    cleanup(): Promise<CleanupOutcome> {
      if (promise === null) {
        promise = registered === null
          ? Promise.resolve({ ran: false, exitCode: null, exitSignal: null, unreaped: false,
              psAfter: null, steps: ['沒有持有中的子程序，無需收尾'] })
          : registered();
      }
      return promise;
    },
  };
  return h;
}

/** 有界等待：逾時回傳 null，不讓任何收尾步驟變成無限等待。 */
export async function withDeadline<T>(p: Promise<T>, ms: number): Promise<T | null> {
  return new Promise<T | null>(res => {
    const t = setTimeout(() => res(null), ms);
    void p.then(v => { clearTimeout(t); res(v); }, () => { clearTimeout(t); res(null); });
  });
}

/**
 * 有界清理：先關掉**自己持有的** pipe 再等退出；仍在則 TERM→KILL，
 * 每一步都只對「本次 spawn 取得、且 pid 身分可核對」的子程序下手。
 *
 * **本函式取得 exit event 不等於 OS 已核對。** 它回報退出後的再探結果（三態），
 * 是否算收乾淨由呼叫端交叉核對（見 `judgeRoundTripSuccess`）。
 */
export async function closeAndReapChild(
  child: ChildProcessWithoutNullStreams,
  steps: string[],
  grace: CleanupGrace = DEFAULT_GRACE,
): Promise<{
  exitCode: number | null; exitSignal: string | null; unreaped: boolean; psAfter: OsProbe | null;
}> {
  const pid = child.pid;
  const exited = new Promise<{ code: number | null; signal: string | null }>(res => {
    if (child.exitCode !== null || child.signalCode !== null) {
      res({ code: child.exitCode, signal: child.signalCode });
      return;
    }
    child.once('exit', (code, signal) => res({ code, signal }));
    // spawn 失敗（ENOENT 等）時 Node 只發 'error' 與 'close'、**不發 'exit'**；
    // 只等 'exit' 會讓這裡白等完 TERM/KILL 的上限才收手。
    child.once('close', () => { res({ code: child.exitCode, signal: child.signalCode }); });
  });

  steps.push('關閉自己持有的 stdin pipe');
  try { child.stdin.end(); } catch (e) { steps.push(`stdin.end() 失敗：${String(e)}`); }

  const waitFor = async (ms: number): Promise<'exited' | 'timeout'> =>
    (await withDeadline(exited, ms)) === null ? 'timeout' : 'exited';

  const after = (): OsProbe | null => {
    if (pid === undefined) { steps.push('無 pid，無法退出後再探'); return null; }
    const p = probeChildOs(pid);
    steps.push(`退出後再探 ps：state=${p.state}${p.error === null ? '' : ` error=${p.error}`}`);
    return p;
  };

  let r = await waitFor(grace.pipeWaitMs);
  steps.push(`關 pipe 後等待 ${grace.pipeWaitMs}ms：${r}`);
  if (r === 'timeout') {
    if (pid === undefined) {
      steps.push('無 pid，無法送訊號——不對未知程序動手');
      return { exitCode: null, exitSignal: null, unreaped: true, psAfter: null };
    }
    steps.push(`送 SIGTERM 給本次持有的 pid=${pid}`);
    try { child.kill('SIGTERM'); } catch (e) { steps.push(`SIGTERM 失敗：${String(e)}`); }
    r = await waitFor(grace.termWaitMs);
    steps.push(`SIGTERM 後等待 ${grace.termWaitMs}ms：${r}`);
    if (r === 'timeout') {
      steps.push(`送 SIGKILL 給本次持有的 pid=${pid}`);
      try { child.kill('SIGKILL'); } catch (e) { steps.push(`SIGKILL 失敗：${String(e)}`); }
      r = await waitFor(grace.killWaitMs);
      steps.push(`SIGKILL 後等待 ${grace.killWaitMs}ms：${r}`);
      if (r === 'timeout') {
        steps.push('最終仍未退出：unreaped');
        return { exitCode: null, exitSignal: null, unreaped: true, psAfter: after() };
      }
    }
  }
  const f = await exited;
  steps.push(`子程序退出：code=${f.code} signal=${f.signal}`);
  return { exitCode: f.code, exitSignal: f.signal, unreaped: false, psAfter: after() };
}

// ---------------------------------------------------------------------------
// 6. MCP client（對真 mcp-approval 子程序）
// ---------------------------------------------------------------------------
export interface McpRoundTrip {
  /** 依**實際接收順序**排列；不裁切、不重排。 */
  events: McpEvent[];
  /** 子程序 stdout 收到的**每一行原文**，含無法解析的。 */
  rawStdoutLines: string[];
  /** 傳輸層違規：無法解析的行、結束時半行、串流錯誤、資料未收齊等。 */
  transportViolations: string[];
  /** 往返本身的錯誤（逾時、取消、spawn 失敗等）；null 代表往返走完。 */
  error: string | null;
  childStderr: string;
  observation: ChildObservation;
}

/** 往返走完後再等一小段，讓「額外的、不該出現的 frame」有機會現身被記錄。 */
export const SETTLE_MS = 250;
/** 收尾後等 stdout 真正結束的上限——目的是**把本次程序的資料收完**。 */
export const DRAIN_MS = 3_000;

export interface RoundTripOptions {
  timeoutMs?: number;
  settleMs?: number;
  drainMs?: number;
  grace?: CleanupGrace;
  /** 讓訊號／例外路徑也能碰到 child 與同一個 cleanup。 */
  handle?: RoundTripHandle;
}

/**
 * 依**已驗過的** resolved config 實際 spawn，走完整往返並**原樣記錄收到的每一行**。
 * 任何一步失敗都保留已取得的 events、raw 行與 stderr（診斷不得丟失）。
 *
 * settle 只是**有界抽樣**，用來讓多餘 frame 有機會現身；它**不**證明之後不會再有。
 * 真正「資料收齊」的依據是收尾後在 drain 期限內等到 stdout 結束（stdoutDrained）。
 */
export async function runMcpRoundTrip(
  resolved: ResolvedMcpConfig,
  exp: ClaudeApprovalExpectation,
  opts: RoundTripOptions = {},
): Promise<McpRoundTrip> {
  const timeoutMs = opts.timeoutMs ?? 30_000;
  const settleMs = opts.settleMs ?? SETTLE_MS;
  const drainMs = opts.drainMs ?? DRAIN_MS;
  const grace = opts.grace ?? DEFAULT_GRACE;
  const handle = opts.handle ?? createRoundTripHandle();

  const events: McpEvent[] = [];
  const rawStdoutLines: string[] = [];
  const transportViolations: string[] = [];
  const steps: string[] = [];
  const selfPid = process.pid;
  let seq = 0;
  let spawnError: string | null = null;
  let roundError: string | null = null;

  let child: ChildProcessWithoutNullStreams;
  try {
    child = spawn(resolved.command, resolved.args, {
      stdio: ['pipe', 'pipe', 'pipe'],
      // **不 detached**：子程序必須留在本行程的所有權之下。
      detached: false,
    }) as ChildProcessWithoutNullStreams;
  } catch (e) {
    return {
      events, rawStdoutLines, transportViolations,
      error: `spawn 立即失敗：${String(e)}`, childStderr: '',
      observation: {
        selfPid, observedPid: null, spawnError: String(e), psDuring: null, psAfter: null,
        exitCode: null, exitSignal: null, stdoutDrained: true, stderrDrained: true, unreaped: false,
        cleanupSteps: [`spawn 立即擲出：${String(e)}`],
      },
    };
  }
  handle.child = child;
  handle.registerCleanup(async () => {
    const r = await closeAndReapChild(child, steps, grace);
    return { ran: true, exitCode: r.exitCode, exitSignal: r.exitSignal,
      unreaped: r.unreaped, psAfter: r.psAfter, steps };
  });

  child.on('error', e => {
    spawnError = String(e);
    steps.push(`child error 事件：${String(e)}`);
  });

  let stderr = '';
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (c: string) => { stderr += c; });
  // 串流錯誤是**傳輸層失敗**，不能只記在 cleanupSteps 裡（reviewer #355）。
  child.stdin.on('error', e => { transportViolations.push(`child stdin 寫入錯誤：${String(e)}`); });
  child.stdout.on('error', e => { transportViolations.push(`child stdout 讀取錯誤：${String(e)}`); });
  child.stderr.on('error', e => { transportViolations.push(`child stderr 讀取錯誤：${String(e)}`); });

  const pid = child.pid ?? null;
  const psDuring = pid === null ? null : probeChildOs(pid);

  // --- reader：**每一行都留下**，能解析的依實際順序進 transcript ---
  let buf = '';
  const waiters: Array<{ need: number; done: () => void; fail: (e: Error) => void }> = [];
  const serverFrameCount = (): number => events.filter(e => e.dir === 's2c').length;
  const notify = (): void => {
    for (let i = waiters.length - 1; i >= 0; i -= 1) {
      if (serverFrameCount() >= waiters[i].need) { const w = waiters[i]; waiters.splice(i, 1); w.done(); }
    }
  };
  const flushResidual = (): void => {
    if (buf === '') return;
    transportViolations.push(`子程序 stdout 結束時殘留未換行的半行：${JSON.stringify(buf)}`);
    rawStdoutLines.push(buf);
    buf = '';
  };
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', (c: string) => {
    buf += c;
    for (;;) {
      const i = buf.indexOf('\n');
      if (i < 0) break;
      const line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      rawStdoutLines.push(line);
      if (line.trim() === '') {
        transportViolations.push(`子程序 stdout 出現空行（第 ${rawStdoutLines.length} 行）`);
        continue;
      }
      try {
        events.push({ seq: seq++, dir: 's2c', frame: JSON.parse(line) as Record<string, unknown> });
      } catch (e) {
        transportViolations.push(`子程序 stdout 第 ${rawStdoutLines.length} 行無法解析為 JSON：${String(e)}｜原文 ${JSON.stringify(line)}`);
      }
      notify();
    }
  });
  const stdoutClosed = new Promise<void>(res => {
    child.stdout.once('end', () => { flushResidual(); res(); });
    child.stdout.once('close', () => { flushResidual(); res(); });
  });
  const stderrClosed = new Promise<void>(res => {
    child.stderr.once('end', () => res());
    child.stderr.once('close', () => res());
  });

  // 取消：訊號／例外路徑一喊停，所有等待立刻結束，不必等滿逾時。
  handle.onCancel(() => {
    const reason = handle.cancelled ?? 'cancelled';
    while (waiters.length > 0) {
      const w = waiters.pop();
      if (w !== undefined) w.fail(new Error(`往返被取消：${reason}`));
    }
  });

  const send = (frame: Record<string, unknown>): void => {
    events.push({ seq: seq++, dir: 'c2s', frame });
    child.stdin.write(`${JSON.stringify(frame)}\n`);
  };
  const waitServerFrames = (need: number): Promise<void> => new Promise((res, rej) => {
    if (handle.cancelled !== null) { rej(new Error(`往返被取消：${handle.cancelled}`)); return; }
    if (serverFrameCount() >= need) { res(); return; }
    const t = setTimeout(() => {
      const i = waiters.findIndex(w => w.done === done);
      if (i >= 0) waiters.splice(i, 1);
      rej(new Error(`等待第 ${need} 筆 MCP 回覆逾時（${timeoutMs}ms，目前 ${serverFrameCount()} 筆）`));
    }, timeoutMs);
    const done = (): void => { clearTimeout(t); res(); };
    const fail = (e: Error): void => { clearTimeout(t); rej(e); };
    waiters.push({ need, done, fail });
  });

  try {
    send({ jsonrpc: '2.0', id: exp.initializeRequestId, method: 'initialize',
      params: { protocolVersion: MCP_PROTOCOL_VERSION, capabilities: {},
        clientInfo: { name: 'fake-claude-cli', version: '0' } } });
    await waitServerFrames(1);

    send({ jsonrpc: '2.0', method: 'notifications/initialized' });

    send({ jsonrpc: '2.0', id: exp.toolCallRequestId, method: 'tools/call',
      params: { name: APPROVAL_TOOL_NAME, arguments: { tool_name: exp.toolName, input: exp.input } } });
    await waitServerFrames(2);

    // 有界抽樣：讓多餘／重複的 frame 有機會被記錄，而不是靠「剛好沒讀到」蒙混。
    if (handle.cancelled === null && settleMs > 0) {
      await new Promise<void>(res => { setTimeout(res, settleMs); });
    }
  } catch (e) {
    roundError = String(e instanceof Error ? e.message : e);
    steps.push(`MCP 往返失敗：${roundError}`);
  }

  // **共用的同一個 cleanup**：訊號路徑若已先呼叫過，這裡拿到的是同一個 Promise。
  const reap = await handle.cleanup();
  // 收尾後在期限內等 stdout **與 stderr** 真正結束，才算把本次程序的資料收完。
  // 兩者併行等待，不讓期限相加。
  const [o1, e1] = await Promise.all([
    withDeadline(stdoutClosed, drainMs),
    withDeadline(stderrClosed, drainMs),
  ]);
  const drained = o1 !== null;
  const errDrained = e1 !== null;
  if (!drained) {
    transportViolations.push(`子程序 stdout 未在 ${drainMs}ms 內結束——raw output 可能不完整`);
    flushResidual();
  }
  if (!errDrained) {
    transportViolations.push(`子程序 stderr 未在 ${drainMs}ms 內結束——stderr 可能不完整`);
  }

  return {
    events, rawStdoutLines, transportViolations,
    error: roundError ?? (spawnError === null ? null : `child error：${spawnError}`),
    childStderr: stderr,
    observation: {
      selfPid, observedPid: pid, spawnError, psDuring,
      psAfter: reap.psAfter, exitCode: reap.exitCode, exitSignal: reap.exitSignal,
      stdoutDrained: drained, stderrDrained: errDrained,
      cleanupSteps: steps, unreaped: reap.unreaped,
    },
  };
}

/**
 * 把「往返是否真的成功」收斂成一份違規清單——**送 success 之前必須是空的**。
 * 這裡刻意呼叫已審的 `judgeMcpTranscript`（F1a 基準），不另造一套較弱的判定。
 */
export function judgeRoundTripSuccess(
  round: McpRoundTrip,
  exp: ClaudeApprovalExpectation,
): string[] {
  const v: string[] = [];
  if (round.error !== null) v.push(`[roundtrip] ${round.error}`);
  if (round.observation.spawnError !== null) v.push(`[spawn] ${round.observation.spawnError}`);
  v.push(...round.transportViolations.map(m => `[transport] ${m}`));
  v.push(...judgeMcpTranscript(round.events, exp).map(m => `[mcp] ${m}`));

  const o = round.observation;
  if (o.observedPid === null) {
    v.push('[os] spawn 未取得 pid');
  } else if (o.psDuring === null) {
    v.push('[os] 往返期間沒有做 OS 身分觀測');
  } else if (o.psDuring.state !== 'present') {
    // 「查不成」與「查不到」都不算通過，而且訊息要分得出來。
    v.push(o.psDuring.state === 'error'
      ? `[os] 往返期間的 OS 觀測失敗（不得當成程序不存在）：${o.psDuring.error ?? '(無診斷)'}`
      : '[os] 往返期間 ps 查不到該 pid——子程序不在預期狀態');
  } else if (o.psDuring.parsed === null) {
    v.push('[os] 往返期間的 ps 輸出無法解析');
  } else {
    if (o.psDuring.parsed.pid !== o.observedPid) {
      v.push(`[os] ps 回報的 pid（${o.psDuring.parsed.pid}）與 spawn 取得的 pid（${o.observedPid}）不符`);
    }
    if (o.psDuring.parsed.ppid !== o.selfPid) {
      v.push(`[os] 子程序 ppid（${o.psDuring.parsed.ppid}）不是本行程（${o.selfPid}）——不是我們持有的那一個`);
    }
  }

  if (o.unreaped) v.push('[cleanup] 子程序未能收乾淨');
  if (o.exitCode !== 0) v.push(`[cleanup] 子程序 exit code 應為 0，實際 ${JSON.stringify(o.exitCode)}`);
  if (o.exitSignal !== null) v.push(`[cleanup] 子程序不應被訊號終止，實際 ${JSON.stringify(o.exitSignal)}`);
  if (!o.stdoutDrained) v.push('[transport] stdout 未在期限內結束，無法宣稱已取得完整 raw output');
  if (!o.stderrDrained) v.push('[transport] stderr 未在期限內結束，無法宣稱已取得完整 stderr');

  // **單有 exit event 不算 OS 核對過**：退出後必須是 ps 明確查不到（absent）。
  if (o.psAfter === null) {
    v.push('[os] 退出後沒有做 OS 再探');
  } else if (o.psAfter.state === 'present') {
    v.push(`[os] 子程序退出後 ps 仍查得到該 pid：${o.psAfter.raw ?? ''}`);
  } else if (o.psAfter.state === 'error') {
    v.push(`[os] 退出後的 OS 再探失敗（查不成 ≠ 查不到，不得當成已消失）：${o.psAfter.error ?? '(無診斷)'}`);
  }
  return v;
}

// ---------------------------------------------------------------------------
// 7. main（只有被直接執行時才跑；被 selftest import 時不執行）
// ---------------------------------------------------------------------------
function isDirectRun(): boolean {
  const entry = process.argv[1];
  if (!entry) return false;
  try { return import.meta.url === pathToFileURL(fs.realpathSync(entry)).href; }
  catch { return false; }
}

/**
 * 證據寫入一律吞例外，但**把失敗記進 problems**——保存失敗不得被丟掉，
 * 更不得讓一次失敗的判定反轉成成功（reviewer #355 第 2 點）。
 */
function safeWrite(dir: string, name: string, content: string, problems: string[]): void {
  try {
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, name), content);
  } catch (e) {
    problems.push(`[evidence] 無法寫入 ${name}：${String(e)}`);
  }
}

/** 收尾的總期限：closeAndReapChild 自身已有界，這裡再加一層防止任何等待卡死。 */
export const CLEANUP_DEADLINE_MS = 30_000;
/** 等往返自己收尾（含 child cleanup 與雙串流 drain）並交還結果的期限。 */
export const FINALIZE_DEADLINE_MS = 35_000;

/**
 * 供最外層例外攔截使用的完成流程入口。cliMain 一建立就設定它，
 * 讓例外與訊號走**同一條**有界診斷路徑，而不是各自直接 exit。
 */
let activeComplete: ((code: number, reason: string, extra?: Record<string, unknown>) => Promise<void>) | null = null;

if (isDirectRun()) {
  void cliMain().catch(async (e: unknown) => {
    const reason = `未預期例外：${String(e instanceof Error ? (e.stack ?? e.message) : e)}`;
    if (activeComplete !== null) { await activeComplete(EXIT_UNEXPECTED, reason); return; }
    // 例外發生在共用流程建立之前（尚未 spawn 任何子程序）——照實記錄，不假裝有收尾。
    process.stderr.write(`fake-claude-cli: ${reason}\n`);
    const dir = process.env.FAKE_CLAUDE_EVIDENCE_DIR ?? '';
    if (dir) {
      const ignored: string[] = [];
      safeWrite(dir, 'failure.json', `${JSON.stringify({
        reason, code: EXIT_UNEXPECTED, argv: process.argv.slice(2),
        note: '例外發生於共用完成流程建立之前，當時尚未 spawn 任何子程序',
      }, null, 2)}\n`, ignored);
      for (const m of ignored) process.stderr.write(`fake-claude-cli: ${m}\n`);
    }
    process.exit(EXIT_UNEXPECTED);
  });
}

async function cliMain(): Promise<void> {
  const env = process.env;
  const argv = process.argv.slice(2);
  // **結構化 argv 紀錄**：argv 在這裡是原生字串陣列，直接寫成 JSON，
  // tripwire 不必對 wrapper 的 printf %q 輸出做不可靠拆字（reviewer #367）。
  const argvLogPath = env.FAKE_CLAUDE_ARGV_LOG ?? '';
  if (argvLogPath !== '') {
    try {
      fs.appendFileSync(argvLogPath,
        `${JSON.stringify({ ts: new Date().toISOString(), pid: process.pid, argv })}\n`);
    } catch (e) {
      process.stderr.write(`fake-claude-cli: 無法寫入 argv 紀錄：${String(e)}\n`);
    }
  }
  const evidenceDir = env.FAKE_CLAUDE_EVIDENCE_DIR ?? '';
  const handle = createRoundTripHandle();
  const writeProblems: string[] = [];
  let finishing = false;
  let approval: ClaudeApprovalExpectation | null = null;
  let roundPromise: Promise<McpRoundTrip> | null = null;
  // **memo 的是 Promise 而不是結果**：訊號路徑與正常路徑可能同時進入，
  // 只記結果的話兩邊都會看到尚未指派的 null 而各跑一次落檔與判定。
  let finalizePromise: Promise<{ round: McpRoundTrip | null; problems: string[] }> | null = null;

  /** 把**中斷當下已取得的**往返資料全部落檔——中斷不是略過證據的理由。 */
  const persistRound = (round: McpRoundTrip): void => {
    safeWrite(evidenceDir, 'mcp-transcript.json', `${JSON.stringify(round.events, null, 2)}\n`, writeProblems);
    safeWrite(evidenceDir, 'mcp-stdout-raw.txt', `${round.rawStdoutLines.join('\n')}\n`, writeProblems);
    safeWrite(evidenceDir, 'mcp-child.json', `${JSON.stringify(round.observation, null, 2)}\n`, writeProblems);
    safeWrite(evidenceDir, 'mcp-child.stderr.txt', round.childStderr, writeProblems);
  };

  /**
   * **單一可等待的完成流程**（正常、失敗、訊號、例外共用，且只跑一次）：
   *   取消 → 等往返自己收完（child cleanup ＋ stdout/stderr 有界 drain）
   *        → 落檔部分資料 → 判定 → 寫 judgement.json
   *
   * 不會死鎖：`runMcpRoundTrip` 只等 `handle.cleanup()`，從不反過來等這裡。
   */
  const runFinalize = async (): Promise<{ round: McpRoundTrip | null; problems: string[] }> => {
    let round: McpRoundTrip | null = null;
    if (roundPromise === null) {
      writeProblems.push('[cleanup] 尚未啟動 MCP 往返，無往返證據可保存');
    } else {
      round = await withDeadline(roundPromise, FINALIZE_DEADLINE_MS);
      if (round === null) {
        writeProblems.push('[cleanup] 往返未在期限內收尾，證據可能不完整');
        const c = await withDeadline(handle.cleanup(), CLEANUP_DEADLINE_MS);
        if (c === null || c.unreaped) writeProblems.push('[cleanup] 子程序未能收乾淨');
      }
    }
    if (round !== null) persistRound(round);
    const judged = round === null || approval === null ? [] : judgeRoundTripSuccess(round, approval);
    safeWrite(evidenceDir, 'judgement.json', `${JSON.stringify({ problems: judged }, null, 2)}\n`, writeProblems);
    return { round, problems: [...judged, ...writeProblems] };
  };

  const finalizeOnce = (): Promise<{ round: McpRoundTrip | null; problems: string[] }> => {
    if (finalizePromise === null) finalizePromise = runFinalize();
    return finalizePromise;
  };

  const complete = async (code: number, reason: string, extra: Record<string, unknown> = {}): Promise<void> => {
    if (finishing) return;
    finishing = true;
    handle.cancel(reason);
    const f = await finalizeOnce();
    let exitCode = code;
    if (f.round !== null && f.round.observation.unreaped) exitCode = EXIT_UNREAPED;
    // **診斷只走 stderr 與證據檔，不污染協定 stdout。**
    process.stderr.write(`fake-claude-cli: ${reason}\n`);
    if (f.round !== null) {
      const o = f.round.observation;
      process.stderr.write(`fake-claude-cli: cleanup exit=${String(o.exitCode)} signal=${String(o.exitSignal)} unreaped=${String(o.unreaped)} psAfter=${o.psAfter === null ? 'null' : o.psAfter.state} stdoutDrained=${String(o.stdoutDrained)} stderrDrained=${String(o.stderrDrained)}\n`);
    }
    if (evidenceDir) {
      const w: string[] = [];
      safeWrite(evidenceDir, 'failure.json', `${JSON.stringify({
        reason, code: exitCode, argv, problems: f.problems,
        cleanupSteps: f.round === null ? null : f.round.observation.cleanupSteps,
        ...extra,
      }, null, 2)}\n`, w);
      // failure.json 也寫不出去時，至少 stderr ＋非零退出，**不得反轉成成功**。
      for (const m of w) process.stderr.write(`fake-claude-cli: ${m}\n`);
    }
    process.exit(exitCode);
  };
  activeComplete = complete;
  process.on('SIGTERM', () => { void complete(EXIT_SIGNALLED, '收到 SIGTERM，走共用完成流程'); });
  process.on('SIGINT', () => { void complete(EXIT_SIGNALLED, '收到 SIGINT，走共用完成流程'); });

  if (argv.length >= 1 && argv[0] === '--version') {
    const vv = validateVersionArgv(argv);
    if (vv.length > 0) { await complete(EXIT_CONTRACT_VIOLATION, vv.join('; ')); return; }
    process.stdout.write(`${env.FAKE_CLAUDE_VERSION ?? 'fake-claude 0.0.0-e2e'}\n`);
    return;
  }

  const expectationPath = env.FAKE_CLAUDE_EXPECTATION ?? '';
  if (!evidenceDir || !expectationPath) {
    await complete(EXIT_CONTRACT_VIOLATION, '缺少 FAKE_CLAUDE_EVIDENCE_DIR／FAKE_CLAUDE_EXPECTATION（wrapper 應烤入）');
    return;
  }

  // 獨立 fixture：期望值來自這裡，**不從 config 推導**。
  const fixture = JSON.parse(fs.readFileSync(expectationPath, 'utf8')) as {
    approval: ClaudeApprovalExpectation;
    mcp: McpConfigExpectation;
    prompt: string;
    /** config 路徑的判定 policy（JSON 載入，**runtime 驗形狀**）。 */
    mcpConfigPolicy: unknown;
    timeoutMs?: number;
    stdinTimeoutMs?: number;
    settleMs?: number;
    drainMs?: number;
    grace?: CleanupGrace;
  };
  approval = fixture.approval;

  safeWrite(evidenceDir, 'argv.json', `${JSON.stringify(argv, null, 2)}\n`, writeProblems);

  // **順序刻意如此**（reviewer #367）：
  //   1. 先從 argv 取出 config 路徑（真 App 傳進來的 host.mcpPath）
  //   2. **先用獨立的 policy 校驗這個動態值**（canonical 父目錄 ＋ App 檔名契約）
  //   3. 再把這個已校驗的值代入**嚴格逐位置全等**的 argv 判定
  // 這樣動態路徑不會變成「跳過其餘 token/value/order/duplicate 檢查」的破口。
  const fromArgv = readMcpConfigPathFromArgv(argv);
  if (fromArgv.path === null) {
    await complete(EXIT_CONTRACT_VIOLATION, `無法自 argv 取得 mcp config 路徑：${fromArgv.violations.join('; ')}`,
      { argvConfigViolations: fromArgv.violations });
    return;
  }
  const cfgPath = fromArgv.path;
  const cfgPathViolations = judgePathAgainstPolicy(cfgPath, fixture.mcpConfigPolicy,
    APP_MCP_CONFIG_BASENAME_RE, 'mcp config 路徑');
  if (cfgPathViolations.length > 0) {
    await complete(EXIT_CONTRACT_VIOLATION,
      `argv 的 --mcp-config 路徑不符 policy：${cfgPathViolations.join('; ')}`, { cfgPathViolations });
    return;
  }
  const argvViolations = validateConversationArgv(argv,
    expectedConversationArgv({ mcpConfigPath: cfgPath }));
  if (argvViolations.length > 0) {
    await complete(EXIT_CONTRACT_VIOLATION, `argv 不符預定：${argvViolations.join('; ')}`, { argvViolations });
    return;
  }

  // stdin：**有界讀第一行，不等 EOF**（真 App MultiTurn 會保持 stdin 開啟）
  let first: FirstLineResult;
  try {
    first = await readFirstLineBounded(process.stdin, fixture.stdinTimeoutMs ?? 15_000);
  } catch (e) {
    await complete(EXIT_CONTRACT_VIOLATION, `讀取 stdin 首行失敗：${String(e instanceof Error ? e.message : e)}`);
    return;
  }
  // 首行已取得就停止讀 stdin：驅動端（真 App）會一直開著 stdin，若讓它保持
  // flowing 會留著 handle 讓本行程無法自然退出。
  try { process.stdin.pause(); process.stdin.unref(); } catch { /* 非 TTY／已關閉都無所謂 */ }
  safeWrite(evidenceDir, 'stdin.first-line.txt', `${first.line}\n`, writeProblems);
  if (first.rest !== '') safeWrite(evidenceDir, 'stdin.rest.txt', first.rest, writeProblems);
  const stdinViolations = validateUserStreamJson(first.line, fixture.prompt);
  if (stdinViolations.length > 0) {
    await complete(EXIT_CONTRACT_VIOLATION, `stdin 不符預定：${stdinViolations.join('; ')}`, { stdinViolations });
    return;
  }

  // 先送合法 init（此時 stdin 仍可能開著，這是正常的）
  process.stdout.write(`${buildInitEvent(fixture.approval.sessionId)}\n`);

  // 讀 config → 驗 → **用驗過的同一份 resolved 去 spawn**
  // config 路徑已在前段自 argv 取得並通過 policy 校驗。
  // FAKE_CLAUDE_MCP_CONFIG 由「來源」降為**可選的交叉核對**：有設就必須一致。
  const envCfg = env.FAKE_CLAUDE_MCP_CONFIG ?? '';
  if (envCfg !== '' && envCfg !== cfgPath) {
    await complete(EXIT_CONTRACT_VIOLATION,
      `FAKE_CLAUDE_MCP_CONFIG（${envCfg}）與 argv 的 --mcp-config（${cfgPath}）不一致`);
    return;
  }
  safeWrite(evidenceDir, 'mcp-config.path.txt', `${cfgPath}\n`, writeProblems);
  const raw = fs.readFileSync(cfgPath, 'utf8');
  safeWrite(evidenceDir, 'mcp-config.read.json', raw, writeProblems);
  const { violations: cfgViolations, resolved } = validateMcpConfig(raw, fixture.mcp);
  const shaViolations = resolved === null ? [] : verifyCommandBinary(resolved.command, fixture.mcp.commandSha256);
  const allCfg = [...cfgViolations, ...shaViolations];
  if (allCfg.length > 0 || resolved === null) {
    // **spawn 前**就拒絕（case B）
    await complete(EXIT_CONTRACT_VIOLATION,
      `mcp config 不符核定值，拒絕在 spawn 前繼續：${allCfg.join('; ')}`, { cfgViolations: allCfg });
    return;
  }

  roundPromise = runMcpRoundTrip(resolved, fixture.approval, {
    timeoutMs: fixture.timeoutMs ?? 30_000,
    settleMs: fixture.settleMs,
    drainMs: fixture.drainMs,
    grace: fixture.grace,
    handle,
  });
  await roundPromise;

  // **走同一條完成流程**：落檔、判定、judgement.json 都在這裡發生。
  const f = await finalizeOnce();
  if (f.problems.length > 0) {
    await complete(EXIT_JUDGE_FAILED, `往返未通過判定，不送完成內容：${f.problems.join('; ')}`,
      { problems: f.problems });
    return;
  }
  // 訊號可能在上面任一個 await 期間觸發；已進入完成流程就不得再送成功內容。
  if (finishing) return;

  // 只有到這裡——往返、判定、OS 核對、收尾、證據保存全部成功——才送完成內容
  process.stdout.write(`${buildAssistantEvent(fixture.approval.sessionId, fixture.approval.completionText)}\n`);
  process.stdout.write(`${buildResultEvent(fixture.approval.sessionId, fixture.approval.completionText)}\n`);
  // 收尾已完成，解除訊號監聽讓行程自然退出（不用 process.exit，避免截斷 stdout）。
  process.removeAllListeners('SIGTERM');
  process.removeAllListeners('SIGINT');
  activeComplete = null;
}
