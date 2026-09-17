// 停止程序（§2.3）：所有結束路徑共用，全程有上限。
//   1. 對主 pgid 送 SIGTERM，同時對非同 pgid 的後代個別送 SIGTERM，最多等 10s
//   2. 仍有殘存 → 對主 pgid 送 SIGKILL、非同 pgid 後代個別 SIGKILL，最多等 5s
//   3. 核對：所有追蹤 pid 消失
//
// **只對狀態檔記錄的 pid 動作**；任何非自家程序一律不送信號。
//
// R4（reviewer 必修項目，2026-09-15）：原本 `waitForDeath` 對每個 pid 各自
// 同步呼叫一次 `ps`（沒有逾時），在 pid 數量多時，單一輪「還有沒有活著」的
// 檢查本身就可能花掉數秒，導致 TERM／KILL 階段實際耗時遠超過名目上的
// 10s／5s。改成每一輪只做一次批次快照、用單調時鐘＋剩餘時間算下一輪等待、
// TERM／KILL 階段耗時分開記錄。查詢本身的逾時也綁定這一輪剩餘的 deadline
// （不會被單次查詢撐過名目上限）；容許誤差 +100ms（Node 事件迴圈排程與量
// 測本身的極小開銷）。
//
// R1 全面重寫（reviewer 三次審查，2026-09-16，實際原碼＋mock 重現後確認）：
// 先前的版本雖然已經不再接受外部傳入的 pgid，但仍有兩個洞：
//   (1) 呼叫端（`processTree.doStop()`／`global-teardown.ts` 的檔案重建
//       分支）直接把 `run-state.json` 的歷史追蹤清單（`st.processes`）當成
//       「已驗證存活」傳進來——那份清單只是「這次執行曾經追蹤過的 pid」，
//       不是「現在還活著、身分還對得上」的驗證結果。
//   (2) KILL 升級時沿用 TERM 階段一開始算好的 `verifiedGroupPgids`，沒有
//       依 TERM 之後**實際還在**的殘存者重新確認——reviewer 用真實原碼＋
//       mock 重現：root 41001／pgid 41001 與 escaped child 41002／pgid
//       41002，TERM 之後所有快照都只剩 child（root 那個 group 已經整個
//       消失），舊碼卻仍然送出 `KILL -41001`——對一個沒有任何已驗證存活
//       成員背書的舊群組送信號，正是這裡自己的規則明文禁止的行為。
// 改成：**`stopProcessGroup` 自己內部做身分驗證，不相信呼叫端傳進來的清單
// 已經驗證過**。
//
// 複核 #34（reviewer 五個必修＋兩項診斷，2026-09-16，未通過真實原碼重現）：
// R1 版本本身還有五個洞（A1–A5），核心教訓是「送信號前後都要能區分『確實
// 不存在』『身分不符』『身分不明（觀測不完整或觀測本身失敗）』三種完全不同
// 的狀況，只有前一種等同『安全，不用管』，後兩種都不能被無聲吞掉、也都不能
// 當成『可以送信號的已驗證目標』」：
//   A1 初次身分不符只有 log＋continue，沒有進 `abandoned`，會被誤判成
//      `clean=true`——改成初次核對不符也要記進 `abandonedAll`，不能只寫 log。
//   A2 TERM 等待期間 ps 觀測失敗時，舊碼直接沿用觀測失敗前那一輪（可能是
//      好幾輪之前、甚至是 TERM 送出前）的舊名單當成「已重新驗證仍存活」拿去
//      送 KILL——「未知」被當成「已驗證存活」。改成觀測失敗時，除了仍持有
//      Node `ChildProcess` 控制代碼（`heldChild`）、可以繞過 ps 直接確認的
//      那個特定 pid 之外，其餘全部移進 `unconfirmed`，不送任何後續信號、
//      不升級 KILL（除非還有其他已確認存活的目標背書）。
//   A3 身分比對只看 pgid／command，沒看開始時間——同 pid／pgid／command 但
//      其實是不同一次啟動的行程會被誤判成同一個目標。改成 pid＋pgid＋
//      command＋開始時間（`ps` 的 `lstart=`）四要素齊備才算「已驗證相符」。
//   A4 `isDying` 把「command 顯示成括號包住的名稱」直接當成死亡證據——依
//      `/usr/share/man/man1/ps.1:401-421`，`<defunct>` 才是 zombie，括號
//      是「argv 取不到或跟 ucomm 不一致」，不是死亡證據。改成用 `stat`
//      欄位分辨真正的 zombie，括號命令列一律歸類成「身分觀測不完整」
//      （`unconfirmed`），不假設死亡也不假設身分相符。
//   A5 `killGroup`／`killPid` 把 `process.kill` 跟 `log.log` 包在同一個
//      try，catch 裡又呼叫可能拋出的 `log.log`（`HarnessLogger.log` 直接
//      `appendFileSync`，磁碟寫入失敗會拋出）——第一個信號送出後 log 寫入
//      失敗就會整個拋出，漏掉其餘目標的信號與等待／升級。改成所有 log 呼叫
//      一律經過 `safeLog`（絕不拋出，寫入失敗記進記憶體中的 `diagFailures`
//      並印到 stderr），讓「診斷紀錄不完整」跟「清乾淨其餘可安全處理的目標」
//      這兩件事分開，前者只讓最終 `clean` 判定為 false，不阻斷後者。
import { ChildProcess } from 'node:child_process';
import { HarnessLogger } from './logger.js';
import { type ProcRowWithCommand, snapshotProcessTableWithCommand, ToolObservationError } from './psUtil.js';
// `TrackedProc` 在這個檔案裡只當型別用，改成 `import type`：一方面語意更
// 精確，另一方面 `stopProcedure.selftest.ts`（reviewer 複核 #34 要求的小型
// 停止程序測試）用 Node 原生 TS 直接執行，`runState.ts` 的 `RunStateStore`
// 用了 constructor 參數屬性簡寫（strip-only 模式不支援），值匯入會強迫載入
// 整個模組而炸掉；`import type` 在編譯期就整個被抹除，不會觸發這個限制。
import type { TrackedProc } from './runState.js';

export interface StopResult {
  clean: boolean;
  residualPids: number[]; // TERM／KILL 結束時仍判定「活著、身分四要素仍相符」的目標——沒送到、沒等到死亡。
  abandonedPids: number[]; // 過程中身分確認不符（pgid／command／開始時間對不上），放棄追蹤的目標——不等於死亡確認，也不等於還在追殺。
  // 複核 #34／A2／A4 新增：跟 `abandonedPids`（已經確認身分不符）不同，這裡
  // 是「無法確認」——包含批次觀測本身失敗那一輪裡沒有可靠持有依據的目標，
  // 以及 command 顯示成括號、argv 取不到、無法核對身分四要素的目標。這批
  // 目標**不會被送任何信號**（不知道是不是我方目標，不能動它），但也不算
  // 已經清乾淨。
  unconfirmedPids: number[];
  observationFailed: boolean; // 過程中至少有一輪批次觀測（送信號前的驗證，或 TERM／KILL 等待期間）本身失敗，不是「查得到但沒有符合結果」。
  // 複核 #34／A5 新增：過程中是否有任何一次 harness.log 寫入本身失敗。不影響
  // 已經送出的信號（那些照常執行），但代表這次執行的診斷紀錄本身不完整，
  // 讓最終 `clean` 判定為 false。
  diagnosticWriteFailed: boolean;
  termPhaseMs: number;
  killPhaseMs: number;
  escalatedToKill: boolean;
}

// 可注入的相依（僅供 `stopProcedure.selftest.ts` 用假的 ps／kill 替換，構造
// A1–A5 各種情境，不需要真的啟動 Wails／spawn 大量真實行程）。正式呼叫端
// 一律不傳，用預設值（真正的 `snapshotProcessTableWithCommand`／
// `process.kill`）。
export interface StopProcedureDeps {
  snapshot: (timeoutMs: number) => ProcRowWithCommand[];
  kill: (pid: number, signal: NodeJS.Signals) => void;
}

const realDeps: StopProcedureDeps = {
  snapshot: snapshotProcessTableWithCommand,
  kill: (pid, signal) => process.kill(pid, signal),
};

function sleep(ms: number): Promise<void> {
  return new Promise(r => setTimeout(r, ms));
}

function nowNs(): bigint {
  return process.hrtime.bigint();
}

function msSince(startNs: bigint): number {
  return Number((nowNs() - startNs) / 1_000_000n);
}

// safeLog（A5 修正）：`HarnessLogger.log` 直接 `appendFileSync`，磁碟寫入
// 失敗會拋出。這裡包一層絕不拋出的版本：寫入失敗時記進呼叫端傳入的
// `diagFailures`（供最終 `StopResult.diagnosticWriteFailed` 使用）並盡量印
// 到 stderr，但**不會**讓呼叫端（尤其是送信號的迴圈）被這裡的例外中斷。
function safeLog(log: HarnessLogger, msg: string, diagFailures: string[]): void {
  try {
    log.log(msg);
  } catch (e) {
    const reason = `harness.log 寫入失敗（不影響已執行的信號／查詢本身，但這部分過程紀錄不完整）：${String(e)}；原訊息：${msg}`;
    diagFailures.push(reason);
    try {
      process.stderr.write(`[stopProcedure] ${reason}\n`);
    } catch {
      // stderr 本身也寫不出去就真的沒有其他補救管道了，至少 diagFailures 這個記憶體陣列還留著。
    }
  }
}

function killGroup(pgid: number, signal: NodeJS.Signals, log: HarnessLogger, kill: StopProcedureDeps['kill'], diagFailures: string[]): void {
  if (pgid <= 0) return;
  try {
    kill(-pgid, signal);
    safeLog(log, `送 ${signal} 給 pgid=${pgid}`, diagFailures);
  } catch (e) {
    safeLog(log, `送 ${signal} 給 pgid=${pgid} 失敗（可能已經不存在）：${String(e)}`, diagFailures);
  }
}

function killPid(pid: number, signal: NodeJS.Signals, log: HarnessLogger, kill: StopProcedureDeps['kill'], diagFailures: string[]): void {
  try {
    kill(pid, signal);
    safeLog(log, `送 ${signal} 給非同 pgid 的 pid=${pid}`, diagFailures);
  } catch (e) {
    safeLog(log, `送 ${signal} 給 pid=${pid} 失敗（可能已經不存在）：${String(e)}`, diagFailures);
  }
}

// heldChildAlive：用 Node 自己對 ChildProcess 的追蹤判斷存活，不靠 ps——只
// 要我們還持有這個物件、Node 還沒回報 exit，作業系統就保證這個 pid 目前
// 仍然是我們自己 spawn 的那個行程本身，沒有 pid 重用的疑慮。這是 A2 所指
// 「仍有可靠的持有依據」的唯一來源——ps 觀測失敗時，只有這個特定 pid 還能
// 被信任，其餘一律歸入 `unconfirmed`。
function heldChildAlive(child: ChildProcess): boolean {
  return child.exitCode === null && child.signalCode === null;
}

export interface LiveTarget {
  pid: number;
  pgid: number;
  command: string;
  startedAt: string;
  samePgid: boolean;
}

type Classification = 'dead' | 'match' | 'mismatch' | 'unconfirmed';

// classifyRow（A1／A3／A4 修正核心，複核 #34）：把「一個追蹤目標」對照「這一
// 輪批次快照裡找到的那一列（或找不到）」分類成四種、且僅四種結果：
//   - dead：查無此 pid，或依 `stat` 確認是 zombie（`Z`）／command 是
//     `<defunct>`——這是唯一「安全，不用再管」的結果。
//   - match：pid／pgid／command／startedAt 四要素全部相符（或屬於
//     `<exiting>` 這種「正在退出、已知還沒消失，但 command 本身不再是原本
//     的命令列」的過渡態，此時不比對 command 字串）——已驗證仍是我方目標，
//     可以留在追蹤清單裡繼續等待或送信號。
//   - mismatch：查得到、不是 dead，但四要素比對不上——確認不再是我方目標，
//     放棄追蹤（`abandoned`），但**不等於乾淨**，也絕對不送信號給它。
//   - unconfirmed：command 顯示成單純括號包住（argv 取不到或跟 ucomm 不
//     一致，依 ps(1) 定義不是死亡證據，也不能拿來核對 command 是否相符）——
//     無法判斷是死是活、是不是我方目標，一律不送信號，記進 `unconfirmed`。
function classifyRow(
  t: { pgid: number; command: string; startedAt: string },
  cur: ProcRowWithCommand | undefined,
): { classification: Classification; detail: string } {
  if (!cur) return { classification: 'dead', detail: '查無此 pid（合法的已死亡結果）' };

  const isZombieStat = cur.stat.includes('Z');
  const isDefunctCommand = cur.command === '<defunct>' || cur.command.endsWith(' <defunct>');
  if (isZombieStat || isDefunctCommand) {
    return { classification: 'dead', detail: `zombie（stat=${cur.stat} command=${cur.command}），依 ps(1) 定義等同已死` };
  }

  const argvUnavailable = /^\([^()]+\)$/.test(cur.command);
  if (argvUnavailable) {
    return {
      classification: 'unconfirmed',
      detail:
        `command=${cur.command}（stat=${cur.stat}）：依 /usr/share/man/man1/ps.1:401-421，`
        + 'command 顯示成括號代表 argv 取不到或跟 ucomm 不一致，不是死亡證據，身分觀測不完整',
    };
  }

  const isExiting = cur.command === '<exiting>'; // ps(1)：正在退出、阻塞中，確認仍存活，只是 command 字串不再是原本的樣子。
  const commandMatches = isExiting ? true : cur.command === t.command;
  if (cur.pgid !== t.pgid || !commandMatches || cur.startedAt !== t.startedAt) {
    return {
      classification: 'mismatch',
      detail:
        `記錄 pgid=${t.pgid} command=${t.command} startedAt=${t.startedAt}；`
        + `目前 pgid=${cur.pgid} command=${cur.command} startedAt=${cur.startedAt}（stat=${cur.stat}）`,
    };
  }
  return { classification: 'match', detail: '' };
}

// verifyInitialTargets：送任何信號之前的第一次驗證。用一次批次快照核對每個
// tracked 目標「現在活著」且身分四要素相符；heldChild 若給了且 pid 對得上，
// 那一筆改用 Node 自己的 exitCode／signalCode 判斷存活（不查 ps），但
// pgid／command／startedAt 仍然沿用 tracked 記錄本身（spawn 當下就確定為真
// 的資訊，不是待驗證的東西）。
function verifyInitialTargets(
  tracked: TrackedProc[],
  heldChild: ChildProcess | null,
  timeoutMs: number,
  log: HarnessLogger,
  deps: StopProcedureDeps,
  diagFailures: string[],
): { verified: LiveTarget[]; abandoned: LiveTarget[]; unconfirmed: LiveTarget[]; observationFailed: boolean } {
  let table: ProcRowWithCommand[] | null = null;
  let observationFailed = false;
  const needsPsCheck = tracked.some(t => !(heldChild && heldChild.pid === t.pid));
  if (needsPsCheck) {
    try {
      table = deps.snapshot(timeoutMs);
    } catch (e) {
      const reason = e instanceof ToolObservationError ? e.message : String(e);
      safeLog(log, `驗證停止目標身分時批次觀測失敗：${reason}`, diagFailures);
      observationFailed = true;
    }
  }
  const byPid = table ? new Map(table.map(r => [r.pid, r])) : null;
  const verified: LiveTarget[] = [];
  const abandoned: LiveTarget[] = [];
  const unconfirmed: LiveTarget[] = [];
  for (const t of tracked) {
    const live: LiveTarget = { pid: t.pid, pgid: t.pgid, command: t.command, startedAt: t.startedAt, samePgid: t.samePgid };
    if (heldChild && heldChild.pid === t.pid) {
      if (heldChildAlive(heldChild)) {
        verified.push(live);
      } else {
        safeLog(log, `pid=${t.pid} 是我們持有的 ChildProcess，Node 回報已經結束（exitCode=${heldChild.exitCode} signalCode=${heldChild.signalCode}），不送信號`, diagFailures);
      }
      continue;
    }
    if (observationFailed) {
      // 這批 ps 驗證整體失敗：除了上面 heldChild 那個特例，其餘目標這一輪
      // 完全沒有被重新確認過——不知道是死是活、身分還對不對，一律歸入
      // unconfirmed，不對任何非持有目標送信號。
      unconfirmed.push(live);
      continue;
    }
    const cur = byPid?.get(t.pid);
    const { classification, detail } = classifyRow(t, cur);
    if (classification === 'dead') continue;
    if (classification === 'match') {
      verified.push(live);
      continue;
    }
    if (classification === 'unconfirmed') {
      safeLog(log, `pid=${t.pid} 送信號前身分觀測不完整，不送信號：${detail}`, diagFailures);
      unconfirmed.push(live);
      continue;
    }
    // mismatch
    safeLog(log, `pid=${t.pid} 送信號前身分核對不符，視為已非我方追蹤的目標，不送信號：${detail}`, diagFailures);
    abandoned.push(live);
  }
  return { verified, abandoned, unconfirmed, observationFailed };
}

// waitForDeathVerified：批次查詢＋單調時鐘，**同時**核對存活與身分四要素。
// 四種結果分開處理：
//   - 查不到／確認 zombie＝已死，合法結束，直接移出清單。
//   - 查得到但身分確認不符＝視為已非我方目標，放棄追蹤，記進 `abandoned`。
//   - 查得到但身分觀測不完整（argv 取不到）＝不知道是不是我方目標，記進
//     `unconfirmed`，不再對它送信號。
//   - 查得到且身分仍相符＝留在 `remaining`，下一輪繼續等，也是唯一可以在
//     KILL 階段被拿來重新升級信號的來源。
// A2 修正核心：這一輪的批次觀測本身失敗時，**不能**把「上一輪還活著」的舊
// 資訊當成「這一輪已經重新驗證」——`remaining` 裡除了仍有可靠持有依據
// （`heldChild`）的那個特定 pid，其餘全部移進 `unconfirmed`，不會被外層
// 拿去送 KILL。
//
// 診斷 1 修正（reviewer 複核 #34，2026-09-16，N13 重跑實測 `050030Z-70c019`
// 實際數字確認）：deadline 迴圈原本只在 `budgetMs <= 0` 時才不再多打一次
// 查詢——但每一輪的 `sleep` 已經用 `Math.min(POLL_INTERVAL_MS, timeLeftMs)`
// 精準睡到只剩極少量時間，導致目標撐滿整個階段上限（例如這裡 N13 刻意構造
// 的忽略 TERM 情境）時，**最後一輪查詢幾乎必然只拿到遠小於一次
// `ps -Ao ...` 全系統掃描所需時間的預算**（實測：`budgetMs=315ms` 時
// 這次的 `ps` 仍然逾時）。這不是「高負載才會發生的巧合」，是迴圈設計本身
// 在目標撐滿整個 grace period 時，結構性地把「grace 到期」偽裝成「工具觀測
// 失敗」——reviewer 明確要求這兩者要分開處理。修正：剩餘 budget 小於
// `POLL_INTERVAL_MS`（跟輪詢間隔用同一個常數，不是另外發明一個新門檻）時，
// 直接視同到期，不再嘗試這注定極可能失敗的最後一次查詢——`remaining`
// （最後一次成功查詢確認仍存活、身分仍相符的目標）就是這個階段誠實的
// 殘存結果，不會被錯誤地降級成「無法確認」。**沒有放寬 TERM／KILL 的名目
// 上限（10s／5s）本身，也沒有放寬 R4 提到的 +100ms 容差**——只是不在最後
// 一小段注定失敗的時間裡硬打一次查詢。
const POLL_INTERVAL_MS = 300;

async function waitForDeathVerified(
  targets: LiveTarget[],
  timeoutMs: number,
  log: HarnessLogger,
  phase: string,
  heldChild: ChildProcess | null,
  deps: StopProcedureDeps,
  diagFailures: string[],
): Promise<{ residual: LiveTarget[]; abandoned: LiveTarget[]; unconfirmed: LiveTarget[]; elapsedMs: number; observationFailed: boolean }> {
  const start = nowNs();
  let remaining = [...targets];
  const abandoned: LiveTarget[] = [];
  const unconfirmed: LiveTarget[] = [];
  let observationFailed = false;

  while (remaining.length > 0) {
    const budgetMs = timeoutMs - msSince(start);
    if (budgetMs <= 0) break; // 已到 deadline，不再多打一次查詢；remaining 在這裡是「最後一次成功觀測仍存活、身分仍相符」的殘存，不是身分不明。
    if (budgetMs < POLL_INTERVAL_MS) {
      // 診斷 1 修正：剩餘時間比一次輪詢間隔還短，不足以合理期待一次
      // `ps -Ao ...` 全系統掃描能在期限內完成——不硬打這一注定極可能失敗
      // 的查詢，直接視同到期，讓 `remaining`（上一輪已經成功驗證仍存活的
      // 目標）誠實地當成這個階段的殘存，不要把「grace 到期」偽裝成「工具
      // 觀測失敗」。
      safeLog(
        log,
        `stopProcedure（${phase}）剩餘 budget=${budgetMs}ms 小於輪詢間隔 ${POLL_INTERVAL_MS}ms，`
        + '不再嘗試這一輪查詢（避免用注定極可能失敗的極短逾時偽造成「觀測失敗」），'
        + `視同到期，殘存 pid=[${remaining.map(t => t.pid).join(',')}]`,
        diagFailures,
      );
      break;
    }
    let table: ProcRowWithCommand[];
    try {
      table = deps.snapshot(budgetMs);
    } catch (e) {
      const reason = e instanceof ToolObservationError ? e.message : String(e);
      const elapsedSoFar = msSince(start);
      safeLog(
        log,
        `stopProcedure（${phase}）等待期間批次觀測失敗（本輪剩餘 budget=${budgetMs}ms、`
        + `這一輪開始至今已耗時 ${elapsedSoFar}ms、階段總上限 ${timeoutMs}ms）：${reason}——`
        + '除了持有 ChildProcess 控制代碼的目標外，其餘目標這一輪未被重新驗證，改列為未確認（不是已驗證存活，也不是已確認死亡），不會被拿去送 KILL',
        diagFailures,
      );
      observationFailed = true;
      const stillReliable: LiveTarget[] = [];
      for (const t of remaining) {
        if (heldChild && heldChild.pid === t.pid) {
          if (heldChildAlive(heldChild)) {
            stillReliable.push(t); // Node 自己的追蹤不受這次 ps 失敗影響，仍然可靠。
          }
          // else：Node 回報已經結束，合法死亡，不用管。
        } else {
          unconfirmed.push(t);
        }
      }
      remaining = stillReliable;
      break;
    }
    const byPid = new Map(table.map(r => [r.pid, r]));
    const stillAlive: LiveTarget[] = [];
    for (const t of remaining) {
      // late-write-after-stop 修正（B3a-1 offline attempt-1 crash-after-pass，
      // 2026-09-17，複核 1b）：先前這裡對 heldChild 也套用跟其他目標一樣的
      // ps 批次快照分類，跟 verifyInitialTargets／上面 observationFailed 分支
      // 的既有規則不一致。`ps` 是另外 spawn 一支子行程即時查詢核心行程表，
      // 跟 Node 自己「這個 ChildProcess 有沒有 emit('exit')」是兩條完全獨立、
      // 沒有同步保證的觀測路徑——ps 可能先回報「查無此 pid」，但 Node 對這個
      // 直接子行程的 onexit／emit('exit') 回呼（會觸發 global-setup.ts 註冊的
      // listener，寫一行 harness.log）還沒被事件迴圈處理到。若這裡誤信 ps
      // 判它已死、把它從 `remaining` 移除，`stopProcessGroup` 會提早回報
      // 乾淨，globalTeardown 接著可能已經把整個證據目錄刪掉；那個遲到的
      // `emit('exit')` listener 之後才觸發，對著已刪除的 harness.log 呼叫
      // `appendFileSync`，丟出未捕捉的 ENOENT，整個命令非零結束（實際堆疊見
      // frontend/e2e/.artifacts/1b-final-20260917T021716Z/item3-offline/
      // attempt-1-crash-after-pass/stdout.log）。
      // 修法：跟 verifyInitialTargets 用同一條規則——heldChild 這個特定 pid
      // 永遠以 Node 自己的 exitCode／signalCode（`heldChildAlive`）為準，
      // 忽略這一輪 ps 快照對它的判斷。這不是新引入的等待、也沒有放寬
      // TERM／KILL 名目上限——只是把「這個 pid 何時算已確認死亡」的判斷來源
      // 換成與 exit listener 同一條事件處理路徑的那一個。
      // 機制更正（reviewer mailroom #61，2026-09-17）：先前這裡寫成
      // 「`exitCode` 只會在所有同步 exit listener 跑完後才變成非 null」，
      // **那是錯的**。實讀本機 Node v26.8.1 的
      // `internal/child_process`：`_handle.onexit` 先設定
      // `this.signalCode`／`this.exitCode`，之後才 `this.emit('exit', …)`。
      // 本修法成立的正確依據是：目前的 exit listener 是同步寫 log，Node 在
      // 同一段事件處理內把同步 listener 跑完，外部等待迴圈的後續
      // continuation 才可能執行。這並不保證未來改成 async listener、或所有
      // stdio close 都已完成——若之後新增非同步 writer，這個前提要重新檢視
      //（見 stopProcedure.selftest.ts 與 lateWriteAfterStop.selftest.ts 的重現）。
      if (heldChild && heldChild.pid === t.pid) {
        if (heldChildAlive(heldChild)) stillAlive.push(t);
        // else：Node 自行確認已死，屆時 exit listener 保證已經跑完，安全地
        // 不再追蹤這個 pid。
        continue;
      }
      const cur = byPid.get(t.pid);
      const { classification, detail } = classifyRow(t, cur);
      if (classification === 'dead') continue;
      if (classification === 'unconfirmed') {
        safeLog(log, `stopProcedure（${phase}）等待期間 pid=${t.pid} 身分觀測不完整，不再視為可安全動作的目標：${detail}`, diagFailures);
        unconfirmed.push(t);
        continue;
      }
      if (classification === 'mismatch') {
        safeLog(log, `stopProcedure（${phase}）等待期間偵測到 pid=${t.pid} 身分已變，不再視為我方目標，停止對它動作：${detail}`, diagFailures);
        abandoned.push(t);
        continue;
      }
      stillAlive.push(t);
    }
    remaining = stillAlive;
    if (remaining.length === 0) break;
    const timeLeftMs = timeoutMs - msSince(start);
    if (timeLeftMs <= 0) break;
    await sleep(Math.min(300, timeLeftMs));
  }

  return { residual: remaining, abandoned, unconfirmed, elapsedMs: msSince(start), observationFailed };
}

// stopProcessGroup：`tracked` 是呼叫端的歷史追蹤清單（例如
// `run-state.json` 的 `processes`，或 staleRun.ts 已經逐一核對過四要素的
// 子集）——**這個函式自己不信任它已經驗證過**，送任何信號之前一律重新驗證
// 一次。`heldChild` 是呼叫端如果還直接持有 root 的 `ChildProcess` 物件就
// 傳進來（見上方模組說明），沒有就傳 `null`。`deps` 只供 selftest 注入假的
// ps／kill，正式呼叫端一律不傳。
export async function stopProcessGroup(
  tracked: TrackedProc[],
  log: HarnessLogger,
  heldChild: ChildProcess | null = null,
  deps: StopProcedureDeps = realDeps,
): Promise<StopResult> {
  const diagFailures: string[] = [];
  const initial = verifyInitialTargets(tracked, heldChild, 5_000, log, deps, diagFailures);
  // 複核 #34 簡化：`verifyInitialTargets` 現在自己就會把「送信號前這輪批次
  // 觀測失敗」時每個非 heldChild 目標正確歸進 `unconfirmed`（見上方函式），
  // 不需要在這裡另外開一個特殊的早退分支——讓它照正常流程往下走：能確認
  // 存活的（通常只有 heldChild）照常送 TERM，其餘留在 `unconfirmedAll`，
  // 最終 `clean` 一樣會是 false，且原因（`observationFailed`／
  // `unconfirmedPids`）跟正常路徑用同一組欄位呈現，不用兩套邏輯各自維護。
  if (initial.observationFailed) {
    safeLog(log, '停止程序送出信號前的身分驗證批次觀測失敗，除了持有 ChildProcess 控制代碼的目標外，其餘目標視為身分無法確認（無法觀測不能宣稱乾淨）', diagFailures);
  }

  // A1 修正核心：初次核對就已經確認身分不符的目標，從一開始就進
  // `abandonedAll`，不能只寫 log 就放過——這是 reviewer 複核 #34 反例
  // （tracked command=worker，目前同 pid command=other）直接對應的修正點。
  const abandonedAll: LiveTarget[] = [...initial.abandoned];
  const unconfirmedAll: LiveTarget[] = [...initial.unconfirmed];

  if (initial.unconfirmed.length > 0) {
    safeLog(log, `送信號前有 ${initial.unconfirmed.length} 個目標身分觀測不完整，不送信號：${initial.unconfirmed.map(t => t.pid).join(',')}`, diagFailures);
  }

  const initialGroupPgids = [...new Set(initial.verified.filter(t => t.samePgid).map(t => t.pgid))];
  const initialOther = initial.verified.filter(t => !t.samePgid);
  if (initialGroupPgids.length === 0 && initialOther.length === 0) {
    safeLog(log, '沒有已驗證存活且身分四要素相符的目標，本次停止程序不送任何信號', diagFailures);
  }

  for (const pgid of initialGroupPgids) killGroup(pgid, 'SIGTERM', log, deps.kill, diagFailures);
  for (const t of initialOther) killPid(t.pid, 'SIGTERM', log, deps.kill, diagFailures);

  const termResult = await waitForDeathVerified(initial.verified, 10_000, log, 'TERM', heldChild, deps, diagFailures);
  safeLog(
    log,
    `TERM 階段耗時 ${termResult.elapsedMs}ms，殘存 pid=[${termResult.residual.map(t => t.pid).join(',')}]`
    + (termResult.unconfirmed.length > 0 ? `，未確認 pid=[${termResult.unconfirmed.map(t => t.pid).join(',')}]` : ''),
    diagFailures,
  );

  let residual = termResult.residual;
  abandonedAll.push(...termResult.abandoned);
  unconfirmedAll.push(...termResult.unconfirmed);
  let killPhaseMs = 0;
  let escalatedToKill = false;
  let observationFailed = initial.observationFailed || termResult.observationFailed;

  if (residual.length > 0) {
    escalatedToKill = true;
    // R1／A2 修正核心：KILL 的 group pgid 集合從 TERM 之後**重新驗證仍存活**
    // 的 `residual` 重新算——這裡的 `residual` 現在保證是「這一輪批次觀測
    // 成功、身分四要素也核對過」的子集（觀測失敗時的目標已經被移進
    // `unconfirmed`，不會出現在這裡），不會再發生「觀測失敗卻被當成已驗證」
    // 的狀況。
    const residualGroupPgids = [...new Set(residual.filter(t => t.samePgid).map(t => t.pgid))];
    const residualOther = residual.filter(t => !t.samePgid);
    safeLog(
      log,
      `TERM 階段結束仍有已驗證存活的殘存 pid=${residual.map(t => t.pid).join(',')}，升級 SIGKILL`
      + `（依 TERM 後重新驗證的名單：group pgid=[${residualGroupPgids.join(',')}]，不沿用一開始的名單）`,
      diagFailures,
    );
    for (const pgid of residualGroupPgids) killGroup(pgid, 'SIGKILL', log, deps.kill, diagFailures);
    for (const t of residualOther) killPid(t.pid, 'SIGKILL', log, deps.kill, diagFailures);

    const killResult = await waitForDeathVerified(residual, 5_000, log, 'KILL', heldChild, deps, diagFailures);
    safeLog(
      log,
      `KILL 階段耗時 ${killResult.elapsedMs}ms，殘存 pid=[${killResult.residual.map(t => t.pid).join(',')}]`
      + (killResult.unconfirmed.length > 0 ? `，未確認 pid=[${killResult.unconfirmed.map(t => t.pid).join(',')}]` : ''),
      diagFailures,
    );
    residual = killResult.residual;
    abandonedAll.push(...killResult.abandoned);
    unconfirmedAll.push(...killResult.unconfirmed);
    killPhaseMs = killResult.elapsedMs;
    observationFailed = observationFailed || killResult.observationFailed;
  } else if (termResult.unconfirmed.length > 0) {
    safeLog(
      log,
      `TERM 階段結束沒有已驗證存活的殘存目標，但有 ${termResult.unconfirmed.length} 個目標身分未確認`
      + `（觀測失敗或身分觀測不完整），依規則不對未知目標送信號，不升級 KILL：`
      + `${termResult.unconfirmed.map(t => t.pid).join(',')}`,
      diagFailures,
    );
  }

  if (abandonedAll.length > 0) {
    safeLog(
      log,
      `停止程序期間有 ${abandonedAll.length} 個目標身分確認不符、放棄追蹤，不算乾淨收尾：`
      + `${abandonedAll.map(t => t.pid).join(',')}`,
      diagFailures,
    );
  }
  if (unconfirmedAll.length > 0) {
    safeLog(
      log,
      `停止程序期間有 ${unconfirmedAll.length} 個目標身分始終無法確認（不是已驗證存活、也不是已確認死亡），不算乾淨收尾：`
      + `${unconfirmedAll.map(t => t.pid).join(',')}`,
      diagFailures,
    );
  }

  const diagnosticWriteFailed = diagFailures.length > 0;
  if (diagnosticWriteFailed) {
    // 這行本身也用 safeLog：就算連這行都寫不出去，diagFailures 陣列跟上面
    // 已經寫過 stderr 的內容還在，不會整個遺失。
    safeLog(log, `停止程序期間有 ${diagFailures.length} 次診斷寫入失敗（不影響已送出的信號本身，但這部分過程紀錄不完整）：${diagFailures.join('; ')}`, diagFailures);
  }

  // clean：沒有殘存、沒有觀測失敗、沒有任何「身分確認不符」或「身分無法
  // 確認」的目標、也沒有任何診斷寫入失敗。
  const clean = residual.length === 0
    && !observationFailed
    && abandonedAll.length === 0
    && unconfirmedAll.length === 0
    && !diagnosticWriteFailed;

  return {
    clean,
    residualPids: residual.map(t => t.pid),
    abandonedPids: abandonedAll.map(t => t.pid),
    unconfirmedPids: unconfirmedAll.map(t => t.pid),
    observationFailed,
    diagnosticWriteFailed,
    termPhaseMs: termResult.elapsedMs,
    killPhaseMs,
    escalatedToKill,
  };
}
