// 前次執行殘留處理（§2.3、負控制 N11a／N11b／N11c）。
//
// 身分核對四要素：pid＋啟動時間＋命令列＋pgid 歸屬。四者之一無法取得、或
// 觀測本身失敗（ToolObservationError），就視為「無法證明歸屬」，走「身分
// 不符」路徑——失敗結束、保留診斷、**不送任何信號**。只有四者全部相符，
// 才視為前次執行自己的殘留並清理。
//
// R3（reviewer 必修項目，2026-09-15）修正三個問題：
//   1. 先前 root 一旦消失就直接把 .active-run.json 當過期指標刪掉、不再管
//      任何東西——但前次執行留下的非同 pgid 後代完全可能還活著（root 死了
//      不代表整棵樹都死了）。現在對 **每一個** 追蹤到的 pid（含 root）逐一
//      核對存活與身分，不因為 root 死了就跳過其餘 pid。
//   2. 先前只核對 root 身分相符，就對整份 processes 清單逐一送信號——沒有
//      個別核對每個後代 pid 是否已經被系統重用給不相關的程序。現在對每個
//      仍存活的 tracked pid 都各自核對四要素，只清「逐一核對通過」的那些。
//   3. 先前 run-state.json 遺失／損毀時會退回「只清 root」——這是在資訊不
//      足時邊做邊猜，違反「無法證明歸屬就失敗並保留診斷」。現在遺失／損毀
//      直接失敗，不送任何信號。
import fs from 'node:fs';
import path from 'node:path';
import { HarnessLogger } from './logger.js';
import { isAlive, processCommand, processPgid, processStartedAt, ToolObservationError } from './psUtil.js';
import { type ActiveRunPointer, readActiveRunPointer, removeActiveRunPointer, type RunState, type TrackedProc } from './runState.js';
import { stopProcessGroup } from './stopProcedure.js';

export type StaleRunOutcome =
  | { kind: 'none' }
  | { kind: 'cleaned'; pointer: ActiveRunPointer; cleanedCount: number }
  | { kind: 'stale-pointer-only' };

export class StaleRunConflictError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'StaleRunConflictError';
  }
}

interface CurrentIdentity {
  startedAt: string | null;
  command: string | null;
  pgid: number | null;
}

// readCurrentIdentity：任何一項觀測失敗都讓整個結果視為「無法取得」——
// 呼叫端據此走「無法證明歸屬」路徑，不會把「觀測失敗」誤判成某個欄位剛好
// 是 null（也就是「查得到但沒有這個值」）。
function readCurrentIdentity(pid: number, log: HarnessLogger): CurrentIdentity | 'observation-failed' {
  try {
    return {
      startedAt: processStartedAt(pid),
      command: processCommand(pid),
      pgid: processPgid(pid),
    };
  } catch (e) {
    const reason = e instanceof ToolObservationError ? e.message : String(e);
    log.log(`核對 pid=${pid} 身分時觀測失敗（無法取得 ps 資訊）：${reason}`);
    return 'observation-failed';
  }
}

export async function handleStaleRun(artifactsRoot: string, log: HarnessLogger): Promise<StaleRunOutcome> {
  const pointer = readActiveRunPointer(artifactsRoot);
  if (!pointer) return { kind: 'none' };

  log.log(`發現前次執行殘留指標 .active-run.json：run=${pointer.runId} rootPid=${pointer.rootPid}`);

  // R3 第三點＋R1：run-state.json 遺失或損毀、或跟 pointer 對不上，就直接
  // 失敗，不退回「只清 root」。結構驗證＋pointer/state 一致性驗證都在
  // `loadValidatedPreviousRunState` 裡（global-teardown.ts 的純檔案 fallback
  // 也共用同一份，見該檔案內註解），這裡不重複實作。
  const prevState = loadValidatedPreviousRunState(pointer, log);
  const prevProcesses = prevState.processes;

  // R3 第一、二點：對每一個追蹤到的 pid（含 root）逐一核對存活與四要素身分，
  // 不因為 root 死了就跳過其餘 pid；不因為 root 身分符合就假設其餘 pid 也符合。
  const toClean: TrackedProc[] = [];
  const mismatched: TrackedProc[] = [];
  let anyObservationFailed = false;

  for (const proc of prevProcesses) {
    let alive: boolean;
    try {
      alive = isAlive(proc.pid);
    } catch (e) {
      const reason = e instanceof ToolObservationError ? e.message : String(e);
      log.log(`核對 pid=${proc.pid} 是否存活時觀測失敗：${reason}`);
      anyObservationFailed = true;
      mismatched.push(proc);
      continue;
    }
    if (!alive) continue; // 這個 pid 確實已經不存在，不用管它。

    const current = readCurrentIdentity(proc.pid, log);
    if (current === 'observation-failed') {
      anyObservationFailed = true;
      mismatched.push(proc);
      continue;
    }
    const matches = current.startedAt === proc.startedAt && current.command === proc.command && current.pgid === proc.pgid;
    if (matches) {
      toClean.push(proc);
    } else {
      log.log(
        `pid=${proc.pid} 身分核對不符：recorded startedAt=${proc.startedAt} command=${proc.command} pgid=${proc.pgid}；`
        + `current startedAt=${current.startedAt} command=${current.command} pgid=${current.pgid}`,
      );
      mismatched.push(proc);
    }
  }

  if (mismatched.length > 0) {
    log.log(
      `前次殘留有 ${mismatched.length} 個 pid 無法證明歸屬（身分不符或觀測失敗：`
      + `${mismatched.map(p => p.pid).join(',')}）。不送任何信號，這些存活程序維持不動。`,
    );
    throw new StaleRunConflictError(
      `前次執行殘留的 ${mismatched.length} 個 pid 無法安全清理（身分不符或觀測失敗）；未送任何信號。`
      + `診斷見 harness.log；如確認是誘餌／無關程序，請手動處理後再重跑。`,
    );
  }
  void anyObservationFailed; // 已經併入 mismatched 走失敗路徑，這裡留著只為了語意清楚。

  if (toClean.length === 0) {
    log.log('前次殘留追蹤到的所有 pid 皆已不存在，指標視為過期，清除後繼續');
    removeActiveRunPointer(artifactsRoot);
    return { kind: 'stale-pointer-only' };
  }

  log.log(`身分核對全數相符，清理 ${toClean.length} 個仍存活的前次殘留 pid：${toClean.map(p => p.pid).join(',')}`);
  const result = await stopProcessGroup(toClean, log);
  if (!result.clean) {
    // R1 追加修正（reviewer 五次審查，2026-09-16，N13 重跑抓到）：`clean`
    // 可能因為殘存、放棄追蹤（身分中途變了）、或觀測失敗三種原因之一變成
    // false——錯誤訊息要講清楚是哪一種，不能只印殘存 pid（可能剛好是空的）。
    throw new StaleRunConflictError(
      `前次殘留身分核對相符，但清理未完全成功：殘存 pid=${result.residualPids.join(',')}、`
      + `放棄追蹤（身分確認不符）pid=${result.abandonedPids.join(',')}、`
      + `身分無法確認 pid=${result.unconfirmedPids.join(',')}、`
      + `觀測失敗=${result.observationFailed}、診斷寫入失敗=${result.diagnosticWriteFailed}`,
    );
  }
  removeActiveRunPointer(artifactsRoot);
  return { kind: 'cleaned', pointer, cleanedCount: toClean.length };
}

// loadPreviousProcessesOrFail：run-state.json 讀不到／解析失敗／structure 不
// 完整，一律視為「state 遺失或損毀」，直接拋出（不送任何信號、不退回只清
// root）。
//
// 缺口 C 修正（reviewer 二次審查，2026-09-15）：先前只檢查
// `Array.isArray(prev.processes)`，空陣列或欄位不完整的元素（例如 pid 不是
// 正整數、command／startedAt 是空字串、跟 pointer 的 runId／rootPid 對不上）
// 都會被當成合法 state 繼續往下走——這樣「structure 看起來像陣列」就能矇混
// 過關，不是真的「證明得了歸屬」。改成完整結構驗證：
//   - `processes` 必須是非空陣列（空陣列＝沒有東西可以證明，等同損毀）。
//   - 每個元素的 pid／pgid／ppid 必須是正整數。
//   - command／startedAt 必須是非空字串（身分核對要用得到）。
//   - `runState.runId` 必須等於 pointer 的 runId；且 processes 裡要有一筆
//     pid 等於 pointer 的 rootPid（root 本身要在清單裡，不然整份紀錄跟
//     pointer 對不上，沒辦法信任）。
// 任何一項不符，都視為損毀，直接拋出。
//
// 回傳完整 `RunState`（不只 processes／rootPgid）：global-teardown.ts 的純
// 檔案 fallback（缺陷 B，reviewer 複核 #52）也要共用這份驗證，而那兩處分支
// 除了 processes 還需要 `ports` 才能核對埠是否釋放，不應該另外重新
// `JSON.parse` 一次同一個檔案。
function loadPreviousProcessesOrFail(pointer: ActiveRunPointer, log: HarnessLogger): RunState {
  const file = path.join(pointer.artifactsDir, 'run-state.json');
  let raw: string;
  try {
    raw = fs.readFileSync(file, 'utf8');
  } catch (e) {
    log.log(`前次 run-state.json 讀取失敗（${file}）：${String(e)}`);
    throw new StaleRunConflictError(`前次 run-state.json 遺失，無法證明任何 pid 的完整歸屬，不送任何信號：${String(e)}`);
  }
  let prev: RunState;
  try {
    prev = JSON.parse(raw) as RunState;
  } catch (e) {
    log.log(`前次 run-state.json 解析失敗（${file}）：${String(e)}`);
    throw new StaleRunConflictError(`前次 run-state.json 損毀，無法證明任何 pid 的完整歸屬，不送任何信號：${String(e)}`);
  }

  const fail = (reason: string): never => {
    log.log(`前次 run-state.json structure 驗證失敗（${file}）：${reason}`);
    throw new StaleRunConflictError(`前次 run-state.json 損毀（${reason}），無法證明任何 pid 的完整歸屬，不送任何信號`);
  };

  if (!Array.isArray(prev.processes)) fail('processes 不是陣列');
  if (prev.processes.length === 0) fail('processes 是空陣列（沒有東西可以證明歸屬）');

  const isPositiveInt = (n: unknown): n is number => typeof n === 'number' && Number.isInteger(n) && n > 0;
  for (const [i, p] of prev.processes.entries()) {
    if (!p || typeof p !== 'object') fail(`processes[${i}] 不是物件`);
    if (!isPositiveInt(p.pid)) fail(`processes[${i}].pid 不是正整數：${JSON.stringify(p.pid)}`);
    if (!isPositiveInt(p.pgid)) fail(`processes[${i}].pgid 不是正整數：${JSON.stringify(p.pgid)}`);
    if (typeof p.ppid !== 'number' || !Number.isInteger(p.ppid) || p.ppid < 0) fail(`processes[${i}].ppid 不是合法非負整數：${JSON.stringify(p.ppid)}`);
    if (typeof p.command !== 'string' || p.command.trim().length === 0) fail(`processes[${i}].command 是空字串或缺失（pid=${p.pid}）`);
    if (typeof p.startedAt !== 'string' || p.startedAt.trim().length === 0) fail(`processes[${i}].startedAt 是空字串或缺失（pid=${p.pid}）`);
    if (typeof p.samePgid !== 'boolean') fail(`processes[${i}].samePgid 不是布林值（pid=${p.pid}）`);
  }

  if (prev.runId !== pointer.runId) {
    fail(`run-state.json 的 runId（${prev.runId}）跟 .active-run.json 的 runId（${pointer.runId}）不一致`);
  }
  if (!prev.processes.some(p => p.pid === pointer.rootPid)) {
    fail(`processes 裡找不到 pointer 記錄的 rootPid=${pointer.rootPid}`);
  }
  if (!isPositiveInt(prev.rootPgid)) {
    fail(`run-state.json 的 rootPgid 不是正整數：${JSON.stringify(prev.rootPgid)}`);
  }
  // R1 缺口 C 修正（reviewer 三次審查，2026-09-16）：先前只核對 root 那一筆
  // entry 跟 pointer 是否一致，沒有核對 `RunState.rootPgid`（跟任何一筆
  // entry 的 `pgid` 都是獨立欄位，沒人保證互相一致），也沒有逐筆核對
  // `samePgid` 是否真的等於 `pgid === rootPgid`——reviewer 實測重現：
  // pointer 與 root entry 都還是同一個 pgid，只把 `state.rootPgid` 改成
  // 別的值，先前的檢查完全沒抓到，`handleStaleRun` 照樣回傳 cleaned。這裡
  // 逐筆核對 `samePgid` 分類是否自洽；`rootPgid` 跟 pointer／root entry 是
  // 否一致留給呼叫端（`handleStaleRun`）在讀到 `rootPgid` 之後核對，因為
  // 那邊才有 `pointer.rootPgid` 可以比對。
  for (const [i, p] of prev.processes.entries()) {
    const expectedSamePgid = p.pgid === prev.rootPgid;
    if (p.samePgid !== expectedSamePgid) {
      fail(
        `processes[${i}]（pid=${p.pid}）的 samePgid=${p.samePgid} 跟「pgid（${p.pgid}）是否等於 `
        + `rootPgid（${prev.rootPgid}）」（應為 ${expectedSamePgid}）不一致，資料不自洽`,
      );
    }
  }

  return prev;
}

// loadValidatedPreviousRunState：`loadPreviousProcessesOrFail` 只驗證
// run-state.json 自身的結構與自洽性（含 runId／rootPid／samePgid）。這裡
// 補上它跟 pointer（.active-run.json）之間的交叉一致性核對（R1 修正＋缺口
// C，reviewer 三次審查，2026-09-16）：pointer.rootPgid／RunState.rootPgid／
// root entry.pgid 三者是否一致、root entry.samePgid 是否為 true、root
// entry 的 command／startedAt 是否跟 pointer 記錄的一致。任何一項不符，
// 直接失敗、不送任何信號——兩份紀錄互相矛盾時，沒有可信的基準能判斷
// 「哪一份是對的」。
//
// 這是 `handleStaleRun`（前次執行殘留）與 global-teardown.ts 純檔案
// fallback（缺陷 B，reviewer 複核 #52：`empty-processes-no-runtime` 等
// 隔離案例）共用的同一套驗證，不得各自平行實作一份。
export function loadValidatedPreviousRunState(pointer: ActiveRunPointer, log: HarnessLogger): RunState {
  const prev = loadPreviousProcessesOrFail(pointer, log);

  if (prev.rootPgid !== pointer.rootPgid) {
    log.log(`run-state.json 的 RunState.rootPgid（${prev.rootPgid}）與 pointer.rootPgid（${pointer.rootPgid}）不一致，資料不自洽，不送任何信號`);
    throw new StaleRunConflictError(
      `run-state.json 的 RunState.rootPgid（${prev.rootPgid}）與 .active-run.json 的 rootPgid（${pointer.rootPgid}）不一致，`
      + `無法信任這份殘留紀錄，不送任何信號`,
    );
  }
  const rootEntry = prev.processes.find(p => p.pid === pointer.rootPid);
  if (!rootEntry) {
    // 理論上不會發生：loadPreviousProcessesOrFail 已經核對過 processes 裡
    // 找得到 rootPid。留著防禦性檢查，訊息說清楚以防萬一。
    throw new StaleRunConflictError(`processes 裡找不到 pointer 記錄的 rootPid=${pointer.rootPid}，無法核對一致性，不送任何信號`);
  }
  if (rootEntry.pgid !== pointer.rootPgid || rootEntry.pgid !== prev.rootPgid) {
    log.log(
      `pointer.rootPgid（${pointer.rootPgid}）／RunState.rootPgid（${prev.rootPgid}）與 run-state.json 記錄的 `
      + `root entry pgid（${rootEntry.pgid}）三者不一致，資料不自洽，不送任何信號`,
    );
    throw new StaleRunConflictError(
      `pointer、RunState.rootPgid、root entry 對 root pgid 的記錄不一致`
      + `（pointer=${pointer.rootPgid}，RunState.rootPgid=${prev.rootPgid}，root entry.pgid=${rootEntry.pgid}），`
      + `無法信任這份殘留紀錄，不送任何信號`,
    );
  }
  if (!rootEntry.samePgid) {
    log.log(`run-state.json 裡 root（pid=${pointer.rootPid}）的 samePgid 不是 true，資料不自洽，不送任何信號`);
    throw new StaleRunConflictError(`run-state.json 裡 root 的 samePgid 不是 true，資料不自洽，不送任何信號`);
  }
  if (rootEntry.command !== pointer.rootCommand || rootEntry.startedAt !== pointer.rootStartedAt) {
    log.log(
      `pointer 記錄的 root command／startedAt 與 run-state.json 不一致`
      + `（pointer: command=${pointer.rootCommand} startedAt=${pointer.rootStartedAt}；`
      + `state: command=${rootEntry.command} startedAt=${rootEntry.startedAt}），不送任何信號`,
    );
    throw new StaleRunConflictError('pointer 與 run-state.json 對 root command／startedAt 的記錄不一致，無法信任這份殘留紀錄，不送任何信號');
  }

  return prev;
}
