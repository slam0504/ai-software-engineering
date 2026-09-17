// macOS `ps`／`lsof` 的最小包裝層。只用系統既有工具，不引入額外套件——
// 這條 harness 本身也要遵守「執行期不需外部網路」，裝套件屬於準備階段（§2.1）。
//
// R2（reviewer 必修項目，2026-09-15）：先前這裡把「工具本身執行失敗（找不到
// 執行檔、逾時、權限錯誤）」跟「工具正常執行、只是沒有符合結果」混在一起，
// 一律當成空清單／false／not-alive——這會讓「觀測不到」被誤判成「乾淨」或
// 「沒有違規」。改成明確區分兩種情況：
//   - 合法的空結果：`ps -p <pid>` 找不到該 pid、`lsof -p <pids>` 找不到任何
//     開啟的網路 socket——這兩種工具本身的慣例都是以 exit code 1 表示「沒有
//     符合的結果」，exit code 0 才有輸出。
//   - 觀測失敗：找不到執行檔（ENOENT）、逾時、或任何其他非 0/1 的結束碼——
//     一律拋出 ToolObservationError，呼叫端必須把這個當成「無法觀測」處理
//     （fail loud），不能吞掉當成空結果。
// 所有外部命令都設逾時（預設 5s），避免卡死的子行程讓整條 harness 掛住。
//
// 缺口 B 修正（reviewer 二次審查，2026-09-15）：上面「exit 1 一律視為合法
// 空結果」這條規則本身有漏洞——reviewer 用一支測試專用的假 lsof（stderr 印
// `permission denied`、exit 1）實測，結果被當成合法空結果放行
// （`isPortListening=false`／`lsofForPids=''`），這正是「觀測不到」被誤判成
// 「乾淨」的同一種錯誤，只是換了個入口。真正的 `ps -p`／`lsof -iTCP:port`／
// `lsof -p pids` 查無結果時，慣例上 stderr 是空的（純粹用 exit code 表示
// 「沒有符合」，不會多印任何文字）。改成：exit 1 時同時檢查 stderr——stderr
// 為空（或只有空白）才視為合法空結果；stderr 有任何內容都視為觀測失敗，把
// stderr 內容原文帶進錯誤訊息，不嘗試部分信任 stdout（工具自己都在 stderr
// 抱怨了，就不是「盡量印出找得到的」那種語意）。
//
// L1（reviewer 複核 #34 第四次，2026-09-16，嚴重／fail-open）：`ps` 子程序
// 原本繼承呼叫端（使用者／CI）的完整環境，包含語系（`LC_ALL`／`LANG`）。
// reviewer 重現：`LC_ALL=zh_TW.UTF-8` 時，macOS 的 `ps` 把 `lstart=` 印成
// 「三  9/16 14:19:25 2026」這種完全不同的格式（本機也實測確認），跟原本只
// 接受英文格式的正規表達式對不上——**每一列都解析失敗，但函式本身只是悄悄
// `continue` 略過，回傳空陣列，不拋任何錯誤**。上層 `stopProcessGroup` 把
// 「查無此 pid（空表）」跟「觀測失敗（表根本解析不出來）」混成同一種結果，
// 於是把所有追蹤目標都判成「已經不在了」，`clean=true`、完全不送任何信號
// ——停止契約在非英文語系環境下整條靜默失效。
// 修正：
//   1. **只在這個檔案呼叫 `ps` 的子程序環境固定 `LC_ALL=C`**（`PS_ENV`，見
//      下方）——不動使用者或 app 本身的全域環境，只影響這幾個 `execFileSync`
//      呼叫看到的 env。單筆查詢（`processStartedAt` 等）跟批次快照
//      （`snapshotProcessTableWithCommand`）用同一個 `PS_ENV`，兩邊的
//      `lstart` 格式才會一致，`TrackedProc.startedAt` 才能跟批次快照的
//      `startedAt` 直接字串比對（A3 的四要素比對前提）。
//   2. **`ps -A` 的批次快照**：任何非空但解析不出來的列、或整份結果解析後
//      是空的，一律拋 `ToolObservationError`，不再悄悄 `continue` 放行——
//      這種系統一定至少有 `ps` 自己這個行程在跑，「解析後是空的」不是合法
//      的空結果，是觀測失敗。
//   3. 診斷訊息只記數量（非空列數 vs. 解析成功列數），**不把完整的程序表
//      內容（含所有命令列）寫進 log**，避免 log 過大也避免洩漏。
import { execFileSync } from 'node:child_process';

const DEFAULT_TIMEOUT_MS = 5000;

export class ToolObservationError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'ToolObservationError';
  }
}

interface ExecFileSyncError {
  code?: string; // 'ENOENT' 等
  status?: number | null;
  signal?: string | null;
  killed?: boolean;
  stdout?: Buffer | string;
  stderr?: Buffer | string;
  message?: string;
}

// L1 修正：只有 `ps` 呼叫用這個 env（在繼承的環境之上疊加 `LC_ALL=C`），
// `lsof` 呼叫不受影響——`lsof` 的輸出格式不像 `ps` 的 `lstart` 那樣受語系
// 影響，沒有必要跟著改，維持最小變更範圍。不修改 `process.env` 本身（不影響
// 這個 Node 行程或它 spawn 出來的其他子行程，例如真正的 `wails dev`／app）。
const PS_ENV: NodeJS.ProcessEnv = { ...process.env, LC_ALL: 'C' };

// runTool：exit 0 一定正常回傳；exit 1 且 stderr 為空（合法「沒有符合結果」
// 的慣例）才正常回傳；其餘（含「exit 1 但 stderr 有內容」）一律拋
// ToolObservationError。`env` 預設用呼叫端目前的環境（`process.env`），`ps`
// 專用的呼叫會明確傳入 `PS_ENV`。
function runTool(
  cmd: string,
  args: string[],
  timeoutMs = DEFAULT_TIMEOUT_MS,
  env: NodeJS.ProcessEnv = process.env,
): { stdout: string; exitCode: number } {
  try {
    const stdout = execFileSync(cmd, args, { encoding: 'utf8', stdio: 'pipe', timeout: timeoutMs, env });
    return { stdout, exitCode: 0 };
  } catch (e: unknown) {
    const err = e as ExecFileSyncError;
    if (err.code === 'ENOENT') {
      throw new ToolObservationError(`${cmd} 找不到執行檔（ENOENT）：${err.message ?? String(e)}`);
    }
    if (err.killed && (err.signal === 'SIGTERM' || err.signal === 'SIGKILL')) {
      throw new ToolObservationError(`${cmd} 執行逾時（${timeoutMs}ms 上限）：${err.message ?? String(e)}`);
    }
    if (typeof err.status === 'number' && err.status === 1) {
      const stderrText = err.stderr ? String(err.stderr).trim() : '';
      if (stderrText.length > 0) {
        // 缺口 B：真正的「查無結果」慣例上 stderr 是空的；exit 1 卻有 stderr
        // 輸出（例如權限錯誤）不是那個慣例，視為觀測失敗，不部分信任 stdout。
        throw new ToolObservationError(
          `${cmd} ${args.join(' ')} 以 exit 1 結束但有 stderr 輸出（不是「查無結果」的合法慣例）：${stderrText}`,
        );
      }
      // ps -p／lsof -p 慣例：exit 1 且 stderr 為空，代表「沒有符合的結果」，是合法的空結果。
      return { stdout: err.stdout ? String(err.stdout) : '', exitCode: 1 };
    }
    throw new ToolObservationError(
      `${cmd} ${args.join(' ')} 執行失敗（非預期的結束狀態，status=${err.status ?? '(none)'} signal=${err.signal ?? '(none)'}）：${err.message ?? String(e)}`,
    );
  }
}

export interface ProcRow {
  pid: number;
  ppid: number;
  pgid: number;
}

export interface ProcRowWithCommand extends ProcRow {
  // A3／A4 修正（reviewer 複核 #34，2026-09-16）：身分四要素（pid／pgid／
  // command／開始時間）原本只核對了 pid／pgid／command，漏了開始時間，同
  // pid／pgid／command 但其實是不同一次啟動的行程會被誤判成同一個目標。
  // `stat` 則是用來正確分辨「zombie（真的死了）」「argv 取不到（身分觀測
  // 不完整，不是死亡證據）」兩種完全不同的狀況——不能只靠 command 欄位的
  // 顯示格式猜測，見 /usr/share/man/man1/ps.1:401-421 對 `<defunct>`／
  // `<exiting>`／方括號／圓括號各自的定義。
  stat: string; // ps 的 stat= 欄位（例如 S／Ss／R+／Z），含 Z 才是真正的 zombie。
  startedAt: string; // ps 的 lstart= 欄位，跟 processStartedAt() 單一查詢用同一種格式，可直接字串比對。
  command: string;
}

// snapshotProcessTable／snapshotProcessTableWithCommand：`ps -A`（列出全部
// 行程）沒有「合法空結果」這回事——執行中的系統一定至少有自己這個 ps
// 行程在跑，非 0 結束碼一律視為觀測失敗，直接拋出。
// timeoutMs 可由呼叫端傳入（stopProcedure 的 deadline 迴圈用這個把「查詢本身
// 的逾時」限制在「這一輪剩餘時間」內，不讓單次 ps 呼叫把 TERM／KILL 階段
// 的名目上限（10s／5s）撐過去——查詢逾時就是 ToolObservationError，呼叫端
// 走「無法觀測」路徑，不會被誤判成「確認還活著」或「確認死了」）。
// parsePsRowsPlain：純函式，跟實際呼叫 `ps`的部分分開，方便直接餵合成的
// 字串做單元測試（不需要真的操控 `ps` 的輸出）。L1 修正：非空但解析不出來的
// 列、或整份解析後是空的，一律拋 `ToolObservationError`——`pid=,ppid=,pgid=`
// 都是純數字，理論上不受語系影響，但保留跟 `parsePsRowsWithCommand` 一致的
// fail-loud 原則（同一類風險，防禦性一致比較不會之後又漏掉一個入口）。
export function parsePsRowsPlain(stdout: string): ProcRow[] {
  const rows: ProcRow[] = [];
  let nonEmptyLineCount = 0;
  for (const line of stdout.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    nonEmptyLineCount += 1;
    const parts = t.split(/\s+/).map(Number);
    if (parts.length !== 3 || parts.some(Number.isNaN)) continue;
    rows.push({ pid: parts[0], ppid: parts[1], pgid: parts[2] });
  }
  if (rows.length !== nonEmptyLineCount || rows.length === 0) {
    throw new ToolObservationError(
      `ps -Ao pid=,ppid=,pgid= 有 ${nonEmptyLineCount} 個非空輸出列，但只解析出 ${rows.length} 筆——`
      + '不是合法的空結果（系統一定至少有 ps 自己這個行程），視為觀測失敗（不記錄實際輸出內容）',
    );
  }
  return rows;
}

export function snapshotProcessTable(timeoutMs = DEFAULT_TIMEOUT_MS): ProcRow[] {
  const { stdout, exitCode } = runTool('ps', ['-Ao', 'pid=,ppid=,pgid='], timeoutMs, PS_ENV);
  if (exitCode !== 0) {
    throw new ToolObservationError(`ps -Ao pid=,ppid=,pgid= 回傳非預期的 exit code=${exitCode}`);
  }
  return parsePsRowsPlain(stdout);
}

// 命令列放最後一欄，靠 regex 一次擷取到行尾（含內部空白），前三欄仍是純數字。
// 用於 F2：網路取樣要能依命令列分類（wails／app／vite／Chrome／取樣器自己），
// 不能只靠「不是 wails」這種排除法。
// timeoutMs 可由呼叫端傳入（見 snapshotProcessTable 同樣的理由：停止程序的
// deadline 迴圈要把「查詢本身的逾時」限制在剩餘時間內）。
// stat＋lstart 欄位格式（A3／A4 修正，2026-09-16）：`stat=` 是不含空白的單一
// token（例如 `Ss`／`R+`／`Z`），`lstart=` 則是固定格式的絕對啟動時間字串
// （strftime `%c` 風格，例如 `Wed Sep 16 13:00:46 2026`，日期個位數時前面會
// 多一個空白，例如 `Tue Sep  8 11:33:01 2026`）——都用實機 `ps` 輸出核對過。
// `command=` 仍然放最後、用 `(.*)$` 一路擷取到行尾（可能含任意空白），這樣
// 才能正確切開「lstart 這個多字詞欄位」跟「command 這個到行尾都算的欄位」。
const PS_ROW_RE = /^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\w{3}\s+\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4})\s+(.*)$/;

// parsePsRowsWithCommand：跟 `parsePsRowsPlain` 一樣抽成純函式（L1 修正，
// reviewer 複核 #34 第四次要求的 focused selftest：C 格式／zh_TW 格式／
// malformed 或空輸出都要能直接餵字串驗證，不用真的操控 `ps`）。
export function parsePsRowsWithCommand(stdout: string): ProcRowWithCommand[] {
  const rows: ProcRowWithCommand[] = [];
  let nonEmptyLineCount = 0;
  for (const line of stdout.split('\n')) {
    if (!line.trim()) continue;
    nonEmptyLineCount += 1;
    const m = PS_ROW_RE.exec(line);
    if (!m) continue;
    // 缺口修正（N13 重跑實測撞到，2026-09-16）：command 欄位用 `(.*)$` 一路
    // 擷取到行尾，`ps` 的批次輸出偶爾會在最後一欄留下多餘的尾端空白——跟
    // `processCommand()`（單一 pid 查詢，會 `.trim()`）比對命令列是否相符
    // 時，同一支程序會因為「有沒有尾端空白」這個無意義的差異被誤判成
    // 「身分已變」。這裡統一 trim，兩邊比較的才是同一種正規化後的字串。
    rows.push({
      pid: Number(m[1]),
      ppid: Number(m[2]),
      pgid: Number(m[3]),
      stat: m[4],
      startedAt: m[5],
      command: m[6].trim(),
    });
  }
  // L1 修正核心：非空但解析不出來的列（例如語系造成 lstart 格式跑掉），或
  // 整份解析後是空的，一律拋 ToolObservationError，不能悄悄放行成「查無
  // 資料」——那會被上層誤判成「所有追蹤目標都已經死亡」。只記數量，不把
  // 實際輸出（可能含完整程序表、大量命令列）寫進錯誤訊息。
  if (rows.length !== nonEmptyLineCount || rows.length === 0) {
    throw new ToolObservationError(
      `ps -Ao pid=,ppid=,pgid=,stat=,lstart=,command= 有 ${nonEmptyLineCount} 個非空輸出列，`
      + `但只解析出 ${rows.length} 筆——不是合法的空結果（系統一定至少有 ps 自己這個行程），`
      + '視為觀測失敗（常見原因：LC_ALL／LANG 語系影響 lstart 顯示格式；已固定 ps 子程序用 '
      + 'LC_ALL=C，若仍發生代表格式本身跟預期的正規表達式不符，需要人工排查；不記錄實際輸出內容）',
    );
  }
  return rows;
}

export function snapshotProcessTableWithCommand(timeoutMs = DEFAULT_TIMEOUT_MS): ProcRowWithCommand[] {
  const { stdout, exitCode } = runTool('ps', ['-Ao', 'pid=,ppid=,pgid=,stat=,lstart=,command='], timeoutMs, PS_ENV);
  if (exitCode !== 0) {
    throw new ToolObservationError(`ps -Ao pid=,ppid=,pgid=,stat=,lstart=,command= 回傳非預期的 exit code=${exitCode}`);
  }
  return parsePsRowsWithCommand(stdout);
}

// isAlive：exit 0（有輸出）＝存活；exit 1（無輸出）＝合法的「pid 不存在」。
// 其餘（ENOENT／逾時／其他非 0/1）拋出，呼叫端不得當成「不存活」處理。
export function isAlive(pid: number): boolean {
  const { stdout, exitCode } = runTool('ps', ['-o', 'pid=', '-p', String(pid)], DEFAULT_TIMEOUT_MS, PS_ENV);
  if (exitCode === 1) return false;
  return stdout.trim().length > 0;
}

// L1 修正：跟批次快照用同一個 PS_ENV（LC_ALL=C），這樣這裡回傳的 lstart
// 字串格式才會跟 `snapshotProcessTableWithCommand` 的 `startedAt` 一致，
// A3 的身分四要素比對（字串直接相等）才有意義——兩邊用不同語系查出來的
// lstart 格式不同，即使是同一個行程也會被誤判成「開始時間不同、身分不符」。
export function processStartedAt(pid: number): string | null {
  const { stdout, exitCode } = runTool('ps', ['-o', 'lstart=', '-p', String(pid)], DEFAULT_TIMEOUT_MS, PS_ENV);
  if (exitCode === 1) return null;
  const t = stdout.trim();
  return t || null;
}

export function processCommand(pid: number): string | null {
  const { stdout, exitCode } = runTool('ps', ['-o', 'command=', '-p', String(pid)], DEFAULT_TIMEOUT_MS, PS_ENV);
  if (exitCode === 1) return null;
  const t = stdout.trim();
  return t || null;
}

export function processPgid(pid: number): number | null {
  const { stdout, exitCode } = runTool('ps', ['-o', 'pgid=', '-p', String(pid)], DEFAULT_TIMEOUT_MS, PS_ENV);
  if (exitCode === 1) return null;
  const n = Number(stdout.trim());
  return Number.isNaN(n) ? null : n;
}

// descendantsOf：以單一時間點快照找出 rootPid 的整棵後代樹（含非同 pgid 的）。
// 泛型保留輸入 row 的實際形狀（例如 ProcRowWithCommand），呼叫端不必再轉型。
export function descendantsOf<T extends ProcRow>(rootPid: number, table: T[] = snapshotProcessTable() as T[]): T[] {
  const childrenOf = new Map<number, T[]>();
  for (const row of table) {
    const list = childrenOf.get(row.ppid) ?? [];
    list.push(row);
    childrenOf.set(row.ppid, list);
  }
  // R4 修正（reviewer 三次審查，2026-09-16，tsc 實跑抓到 TS2322）：先前宣告成
  // `ProcRow[]`（泛型下界），但函式簽章承諾回傳 `T[]`（保留呼叫端傳進來的實際
  // 形狀，例如 `ProcRowWithCommand[]`）——`ProcRow[]` 不能當 `T[]` 回傳（`T`
  // 可能是比 `ProcRow` 更窄的子型別），是真的型別錯誤，不是型別系統太嚴格。
  // 改成宣告成 `T[]`，讓累加器跟回傳型別、跟 `queue`（本來就已經正確是
  // `T[]`）保持一致。
  const result: T[] = [];
  const queue = [...(childrenOf.get(rootPid) ?? [])];
  const seen = new Set<number>();
  while (queue.length) {
    const row = queue.shift()!;
    if (seen.has(row.pid)) continue;
    seen.add(row.pid);
    result.push(row);
    for (const child of childrenOf.get(row.pid) ?? []) queue.push(child);
  }
  return result;
}

// isPortListening：只看本機 loopback 的 LISTEN 狀態，用來判斷 34115／vite 埠
// 是否被佔用或已釋放。exit 1（無輸出）＝合法的「沒有人在聽」；其餘失敗拋出，
// 呼叫端不得當成「埠已釋放」或「埠空閒」。
export function isPortListening(port: number): boolean {
  const { stdout, exitCode } = runTool('lsof', ['-nP', `-iTCP:${port}`, '-sTCP:LISTEN']);
  if (exitCode === 1) return false;
  return stdout.trim().length > 0;
}

// lsofForPids：**不能**用「exit code 1 ＝合法空結果」這個慣例——實測確認
// `lsof -p pid1,pid2` 給多個 pid 時，只要其中任何一個 pid 沒有符合的網路
// socket，lsof 就會用 exit code 1 結束，**即使其他 pid 確實有輸出**。
//   驗證：`lsof -nP -iTCP -iUDP -a -p "<有 LISTEN 的真實 pid>,999999"` 回傳
//   exit=1，但 stdout 仍然印出那個真實 pid 的 LISTEN 那一行。
// 先前把 exit 1 一律當成空結果，會把「有結果但混了一個沒匹配到的 pid」的
// 情況誤判成「完全沒有連線」——網路取樣因此可能長期漏放，不算違規。
// 這裡改成：exit 0／1 都信任 stdout 的實際內容（lsof 對這個查詢語意本來就
// 是「盡量印出找得到的」，不是「全有全無」）；只有 ENOENT／逾時／其他非
// 0/1 結束碼（`runTool` 已處理）才算觀測失敗、才拋出。
export function lsofForPids(pids: number[]): string {
  if (pids.length === 0) return '';
  const { stdout } = runTool('lsof', ['-nP', '-iTCP', '-iUDP', '-a', '-p', pids.join(',')]);
  return stdout;
}
