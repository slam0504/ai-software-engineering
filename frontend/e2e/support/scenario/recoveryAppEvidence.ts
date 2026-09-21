// B3a-2b-2 Task E2 補正：App 原始 wire 錄流的歸屬／完整序列（A）、
// generation 選擇唯一性（B）、落地 scenario-config.json 對上 builder（C）的
// 共用判定模組。codexSessionRecovery.spec.ts 與本檔的 selftest 呼叫同一組
// 函式，不各寫一份。
//
// 背景（reviewer 用原樣抽出的 spec 斷言實測，兩個反例全部 PASS）：
//   1. 原本的 App wire 檢查只用 `.find()` 撈兩輪的 approval request／
//      response 四個 frame，從未讀取 `AppWireRow.wsid`、也沒有驗完整序列
//      ——(a) 所有 row.wsid 改成 ANOTHER-WSID、(b) 截斷到只剩 4 個 approval
//      frame，兩者都 PASS。「fake 那份完整，不等於 App 錄流也完整且歸屬
//      正確」。
//   2. `scenarioConfigPath` 從未被讀取／交叉核對，env 欄位核對不能代替。
//
// 修正原則：
//   - A 的完整序列比對重用 E1 已驗收、凍結的 `judgeRecoverySequence`
//     （recoveryJudge.ts）——本檔只做「型別／原始順序核對」＋「WSID 歸屬核
//     對」＋「最小轉換」，不重寫一份手動序列比對。
//   - B 不依 mtime 猜最新；candidates.length !== 1 一律判失敗，並保留可讀的
//     歧義診斷。
//   - C 重用凍結的 `judgeRunIdentity`（scenarioProtocolJudge.ts）核對
//     round1 欄位與 manifest；`judgeRunIdentity` 不核對 `secondTurn`（它的
//     欄位集合本來就沒有這欄），這裡補一段獨立的 secondTurn 深比對，避免
//     「env 欄位核對通過」被誤當成「secondTurn 也核對過」。
import type { Frame, Manifest, RunLogEntry, ScenarioConfig } from './protocol.ts';
import { judgeRecoverySequence } from './recoveryJudge.ts';
import type { RecoveryExpectation } from './recoveryJudge.ts';
import { judgeRunIdentity } from './scenarioProtocolJudge.ts';
import type { GenerationCandidate } from './wireEvidence.ts';

// ---------------------------------------------------------------------------
// A：App 原始 wire 的歸屬與完整序列
// ---------------------------------------------------------------------------

export interface AppWireRow {
  frame: number;
  dir: 'c2s' | 's2c';
  wsid: string;
  raw: Frame;
}

function isPlainObject(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}

// isValidAppWireRow：最小型別核對——frame 是數字、dir 是 'c2s'|'s2c'、wsid 是
// 字串、raw 是物件。不假設輸入乾淨（輸入是外部落地的 JSONL，逐行
// `JSON.parse` 後型別未經驗證）。
function isValidAppWireRow(x: unknown): x is AppWireRow {
  if (!isPlainObject(x)) return false;
  // reviewer #325 缺陷 2：先前只看 `typeof x.frame === 'number'`，於是
  // frame=-1／1.5／NaN 都會過；`raw.id` 完全沒核對，把 initialize request 與
  // response 的 id 都改成 null 仍然回 []。這裡補最小型別核對——**不改 E1 凍結
  // 的 judge、也不另建通用 protocol framework**，只擋住 adapter 這層本來就該
  // 擋的東西。
  if (!(typeof x.frame === 'number' && Number.isInteger(x.frame) && x.frame >= 0)) return false;
  if (!(x.dir === 'c2s' || x.dir === 's2c')) return false;
  if (typeof x.wsid !== 'string') return false;
  if (!isPlainObject(x.raw)) return false;
  // `id` 可以不存在（notification），但存在時只能是 string／number——
  // 明確的 `null` 不是合法 JSON-RPC id。
  if ('id' in x.raw && x.raw.id !== undefined) {
    const id = x.raw.id;
    if (typeof id !== 'string' && typeof id !== 'number') return false;
  }
  return true;
}

export interface AppWireRecoveryExpectation {
  // domWsid：UI 端 ApprovalDialog／pane 讀到的 WSID（真實 App 內部 WSID
  // scheme，與 wire log 的 `wsid` 欄位同一個字串空間——見
  // internal/wirelog/wirelog.go 的 resolve()／PaneView.vue、
  // ApprovalDialog.vue 的 data-test-wsid）。
  domWsid: string;
  recovery: RecoveryExpectation;
}

// judgeAppWireRecovery：A 的共用判定。回傳 violation 字串陣列（空陣列＝通
// 過）。三層核對依序進行，任一層失敗就不再往下一層（下一層假設上一層已經
// 成立，避免對已知壞掉的輸入做無意義的深層比對）：
//   1. 型別／原始順序：逐列型別核對＋frame 編號必須嚴格遞增（不得重排、不
//      得用排序掩蓋錯序——只核對「輸入原本的順序合法」，不重新排序）。
//   2. WSID 歸屬：握手階段（wsid === ''）依現有記錄語意允許；任何 wsid 非
//      空的 session frame 都必須等於 domWsid，一個都不能是別的 WSID。
//   3. 最小轉換＋重用 judgeRecoverySequence：把 AppWireRow[] 轉成
//      RunLogEntry[]（frame 值原封不動搬進 `frame` 欄位，不做任何內容轉
//      換、不丟任何一列），交給 E1 已驗收的共用 judge 驗完整序列。
export function judgeAppWireRecovery(rows: unknown, exp: AppWireRecoveryExpectation): string[] {
  if (!Array.isArray(rows) || rows.length === 0) {
    return ['App 原始 wire 錄流為空或不是陣列，無法核對'];
  }

  const typeViolations: string[] = [];
  const validRows: AppWireRow[] = [];
  rows.forEach((r, idx) => {
    if (!isValidAppWireRow(r)) {
      typeViolations.push(`第 ${idx} 列不符合 AppWireRow 基本型別（frame:number／dir:'c2s'|'s2c'／wsid:string／raw:object）：${JSON.stringify(r)}`);
      return;
    }
    validRows.push(r);
  });
  if (typeViolations.length > 0) return typeViolations;

  const orderViolations: string[] = [];
  for (let i = 1; i < validRows.length; i += 1) {
    if (validRows[i].frame <= validRows[i - 1].frame) {
      orderViolations.push(
        `frame 順序錯亂：第 ${i} 列 frame=${validRows[i].frame} 未大於前一列 frame=${validRows[i - 1].frame}（不得用排序掩蓋錯序）`,
      );
    }
  }
  if (orderViolations.length > 0) return orderViolations;

  // reviewer #325 缺陷 1：先前的條件是 `row.wsid !== ''` 才比對，等於**用空字串
  // 當作「這是握手」的辨識依據**——於是把全部 wsid 清空、或只清空第二輪的
  // wsid，judge 仍然回 []。#318 明定「只有握手可以空，握手之後每一筆 session
  // frame 都必須等於 domWsid」。改成依**完整序列的前三筆握手位置／角色**判定：
  // frame index 0/1/2 分別是 c2s initialize、s2c initialize response、
  // c2s initialized（`judgeRecoverySequence` 會另外嚴格核對這三筆的方向與
  // method，這裡只負責 WSID 歸屬）。
  const HANDSHAKE_ROWS = 3;
  const wsidViolations: string[] = [];
  if (typeof exp.domWsid !== 'string' || exp.domWsid === '') {
    return [`domWsid 必須是非空字串，實際 ${JSON.stringify(exp.domWsid)}——不得以空 WSID 通過歸屬核對`];
  }
  if (validRows.length <= HANDSHAKE_ROWS) {
    return [`App wire 只有 ${validRows.length} 筆，不足以涵蓋握手（${HANDSHAKE_ROWS} 筆）之後的 session frames`];
  }
  validRows.forEach((row, idx) => {
    if (idx < HANDSHAKE_ROWS) return; // 握手三筆：wsid 依現有記錄語意允許為空
    if (row.wsid === '') {
      wsidViolations.push(
        `frame=${row.frame}（dir=${row.dir}，序列位置 ${idx}）的 wsid 是空字串——`
        + '握手之後的每一筆 session frame 都必須帶非空 WSID，空字串不得當成「這是握手」的辨識依據',
      );
      return;
    }
    if (row.wsid !== exp.domWsid) {
      wsidViolations.push(
        `frame=${row.frame}（dir=${row.dir}，序列位置 ${idx}）的 wsid=${JSON.stringify(row.wsid)} 與 DOM 取得的 domWsid=${JSON.stringify(exp.domWsid)} 不符`,
      );
    }
  });
  if (wsidViolations.length > 0) return wsidViolations;

  const transformed: RunLogEntry[] = validRows.map(r => ({
    seq: r.frame + 1,
    ts: 'not-recorded',
    dir: r.dir,
    frame: r.raw,
  }));
  return judgeRecoverySequence(transformed, exp.recovery);
}

// ---------------------------------------------------------------------------
// B：generation 選擇必須唯一
// ---------------------------------------------------------------------------

// judgeGenerationUniqueness：candidates 必須恰好一筆。0 筆與多筆都判失敗，
// 多筆時保留完整的歧義診斷（每個候選的檔名＋mtime），不得依 mtime 挑「最
// 新」的那個頂替。
export function judgeGenerationUniqueness(candidates: GenerationCandidate[]): string[] {
  if (candidates.length === 1) return [];
  if (candidates.length === 0) {
    return ['App 端原始 wire 錄流找不到任何內容含本案 identity（approvalMethod＋approvalRequestId）的 generation 檔'];
  }
  const diagnostics = candidates
    .map(c => `${c.file}（mtimeMs=${c.mtimeMs}／${new Date(c.mtimeMs).toISOString()}）`)
    .join('、');
  return [
    `App 端原始 wire 錄流有 ${candidates.length} 個 generation 檔的內容都符合本案 identity，無法唯一判定要用哪一個`
    + `（不得依 mtime 猜最新）——歧義候選：${diagnostics}`,
  ];
}

// ---------------------------------------------------------------------------
// C：落地 scenario-config.json 對上 builder（含 secondTurn）
// ---------------------------------------------------------------------------

export interface ConfigOnDiskExpectation {
  runId: string;
  // cfg：受版控 builder 的完整回傳值（含 secondTurn），不是落地檔案本身。
  cfg: ScenarioConfig;
  decision: 'accept' | 'decline';
}

// judgeConfigOnDiskAgainstBuilder：C 的共用判定。直接重用凍結模組
// `judgeRunIdentity` 核對 round1 欄位集合（scenario／threadId／turnId／
// itemId／threadMode／approvalMethod／approvalRequestId／turnStatus／
// afterApproval）與 manifest（scenario／approvalMethod／approvalRequestId／
// decisionReceived）——不重寫這一段。`judgeRunIdentity` 的欄位集合本來就不
//含 `secondTurn`（round1-only），這裡另外補一段獨立的深比對，讓「落地
// config 對上 builder」真的涵蓋 secondTurn，不是只靠 env 欄位核對頂替。
export function judgeConfigOnDiskAgainstBuilder(
  manifest: Manifest,
  scenarioConfigOnDisk: unknown,
  exp: ConfigOnDiskExpectation,
): string[] {
  const violations = judgeRunIdentity(manifest, scenarioConfigOnDisk, {
    runId: exp.runId,
    cfg: exp.cfg,
    decision: exp.decision,
  });

  if (isPlainObject(scenarioConfigOnDisk)) {
    const diskSecondTurn = JSON.stringify(scenarioConfigOnDisk.secondTurn);
    const expectedSecondTurn = JSON.stringify(exp.cfg.secondTurn);
    if (diskSecondTurn !== expectedSecondTurn) {
      violations.push(
        `scenario-config.json 落地內容與 builder 的 secondTurn 不符：落地=${diskSecondTurn} builder=${expectedSecondTurn}`,
      );
    }
  }
  // scenarioConfigOnDisk 若不是物件，judgeRunIdentity 已經在上面記過
  // 「不是 JSON 物件」的 violation，這裡不重複記。

  return violations;
}
