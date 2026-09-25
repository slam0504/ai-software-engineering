// B3a-2a：gates 專屬的證據保存（review #427 必修缺陷 6）。
//
// **背景**：`global-teardown.ts:364-366`（`captureFixtureSnapshot` 只存
// git log／status／glossary，接著 `fs.rmSync(env.workspaceDir, ...)`
// 整個刪掉 fixture）是 default／controls／scenario／gates 四個入口共用的
// 既有邏輯——本輪任務明確限制「共用 teardown 不改」。gate.jsonl／
// audit.jsonl／events.jsonl 活在 `<workspaceDir>/.workbench/` 底下，會被
// 這個共用清除動作一併刪掉，若不在那之前另外保存，run 結束後就再也看不
// 到原始 journal 內容，只剩測試斷言本身的失敗訊息（沒有底層證據可回溯）。
//
// **修法**：spec 的 `test.afterEach`（Playwright 保證不論通過或失敗都會
// 執行，且在 `globalTeardown` 之前執行——這時 fixture 還沒被刪）呼叫
// `GateEvidenceRecorder.flush()`，把 gate.jsonl／audit.jsonl／events.jsonl
// 的原始內容、測試過程中累積記錄的快照（期望/實際 bindings、C1/C2/C3、
// probe payload 序列、每個步驟的判定結果）寫進 `artifactsDir`（不是會被
// teardown 刪除的 `workspaceDir`）。
//
// **單一寫入者**：每個 run 的 artifactsDir 本身就是 run-id 專屬、全新的
// 目錄（run-e2e.mjs 的既有設計），`flush()` 只會被對應 spec 的
// `afterEach` 呼叫恰好一次；`flush()` 內部用 `fs.writeFileSync` 前先檢查
// 目標檔案是否已存在，若已存在（代表同一個 run 內被呼叫了兩次，理論上
//不該發生）直接 throw，不靜默覆寫。
//
// **證據寫入失敗時整個 run 要失敗**：`flush()` 不吞任何錯誤——所有 fs 操
// 作失敗都直接往外 throw，讓呼叫端（spec 的 afterEach）的例外讓這次
// test run 被記為失敗，不是「盡力而為」的 best-effort 寫入。
import fs from 'node:fs';
import path from 'node:path';

export interface RecordedStep {
  at: string; // ISO timestamp
  label: string;
  data: unknown;
}

/**
 * 單一 journal 檔的讀取結果——**review #4（#430）必修缺陷 1 修正**：舊版
 * `snapshotJournals` 把「檔案不存在」與「讀取失敗（例如 EISDIR）」都吞成
 * 同一個 `null`，reviewer 的探針把 `audit.jsonl` 換成目錄製造 EISDIR，
 * `flush()` 仍寫出 `finalStatus=passed`、`audit_jsonl=null`——看起來像
 * 「這個 run 沒有 audit.jsonl」，實際上是讀取本身壞掉，兩者語意完全不
 * 同。改成明確三態：
 *   - `ok`：讀到完整內容；
 *   - `missing`：檔案不存在（ENOENT）——只在明確的早期失敗階段合法；
 *   - `read_error`：檔案存在但讀取失敗（EISDIR、權限錯誤等），保留原始
 *     錯誤訊息，不能靜默轉成 null。
 */
export type JournalReadStatus =
  | { status: 'ok'; content: string }
  | { status: 'missing' }
  | { status: 'read_error'; error: string };

function isEnoent(e: unknown): boolean {
  return typeof e === 'object' && e !== null && 'code' in e && (e as { code?: unknown }).code === 'ENOENT';
}

function readJournalFile(p: string): JournalReadStatus {
  let content: string;
  try {
    content = fs.readFileSync(p, 'utf8');
  } catch (e) {
    if (isEnoent(e)) return { status: 'missing' };
    return { status: 'read_error', error: e instanceof Error ? e.message : String(e) };
  }
  return { status: 'ok', content };
}

export class GateEvidenceRecorder {
  private readonly steps: RecordedStep[] = [];
  private flushed = false;
  private readonly flow: 'gate1' | 'gate2' | 'stale';
  private readonly runId: string;

  constructor(flow: 'gate1' | 'gate2' | 'stale', runId: string) {
    this.flow = flow;
    this.runId = runId;
  }

  /** 記錄任意一個步驟的資料（期望/實際 bindings、commit SHA、probe payload 序列、判定結果等）。 */
  record(label: string, data: unknown): void {
    this.steps.push({ at: new Date().toISOString(), label, data });
  }

  /**
   * 讀取目前 `.workbench/` 底下三個 journal 檔的讀取結果（三態，見
   * `JournalReadStatus`）。回傳值供 `flush()` 判斷是否符合
   * `finalStatus=passed` 的完整性要求。
   */
  snapshotJournals(workspaceDir: string): Record<'gate_jsonl' | 'audit_jsonl' | 'events_jsonl', JournalReadStatus> {
    const wbDir = path.join(workspaceDir, '.workbench');
    const result: Record<'gate_jsonl' | 'audit_jsonl' | 'events_jsonl', JournalReadStatus> = {
      gate_jsonl: readJournalFile(path.join(wbDir, 'gate.jsonl')),
      audit_jsonl: readJournalFile(path.join(wbDir, 'audit.jsonl')),
      events_jsonl: readJournalFile(path.join(wbDir, 'events.jsonl')),
    };
    this.record('journals-snapshot', result);
    return result;
  }

  /**
   * 寫出所有累積的記錄與最終的 journal 快照到 `artifactsDir`（不是
   * `workspaceDir`——後者會被共用 teardown 刪除）。**不論測試通過或失
   * 敗，呼叫端都必須呼叫這個函式**（在 `test.afterEach` 內，無條件呼
   * 叫，不是只在失敗時才呼叫）。
   *
   * **review #4（#430）必修缺陷 1 修正**：`finalStatus==='passed'` 時，
   * 三個 journal 都必須是 `status:'ok'`——一個成功結案的 gate flow，
   * gate.jsonl（Gate1 至少已核可）／audit.jsonl／events.jsonl（App 啟動
   * 後就會建立）理論上必然都存在且可讀，任何一個是 `missing` 或
   * `read_error` 都代表證據本身有問題，這種情況下**不允許**判定為
   * `passed`——即使 test body 的斷言全部通過。非 `passed` 的狀態（`failed`
   * 等）允許 journal 不完整（例如流程在建立 gate.jsonl 之前就失敗了），
   * 但仍然把每個 journal 的實際讀取結果（含 `read_error` 的原始錯誤訊
   * 息）如實保存，不是無條件轉成 `null`。
   *
   * 證據寫出仍然先發生（方便事後鑑識），完整性檢查在寫出**之後**才做；
   * 若判定不通過，`flush()` 仍然 throw，讓呼叫端（`test.afterEach`）把
   * 這個 run 判定為失敗（見 review #4 必修缺陷 2 的 hook 修正）。
   */
  flush(artifactsDir: string, workspaceDir: string, finalStatus: 'passed' | 'failed' | 'timedOut' | 'interrupted' | 'skipped' | undefined): void {
    if (this.flushed) {
      throw new Error(`GateEvidenceRecorder.flush: 這個 recorder（flow=${this.flow}）已經 flush 過一次，不得重複呼叫（單一寫入者）`);
    }
    this.flushed = true;
    const journals = this.snapshotJournals(workspaceDir);
    const bad = (Object.entries(journals) as Array<[string, JournalReadStatus]>).filter(([, r]) => r.status !== 'ok');
    // test body 通過不代表證據完整；檔案與最後拋出的錯誤必須表達同一結果。
    const recordedStatus = finalStatus === 'passed' && bad.length > 0 ? 'failed' : finalStatus;
    this.record('final-status', recordedStatus);

    fs.mkdirSync(artifactsDir, { recursive: true });
    const evidencePath = path.join(artifactsDir, 'gate-evidence.json');
    if (fs.existsSync(evidencePath)) {
      throw new Error(`GateEvidenceRecorder.flush: ${evidencePath} 已存在——不得覆寫既有（可能是失敗或舊的）判定證據`);
    }
    const payload = {
      flow: this.flow,
      runId: this.runId,
      bodyStatus: finalStatus,
      finalStatus: recordedStatus,
      steps: this.steps,
    };
    // 先寫到暫存檔再 rename——避免寫到一半中斷留下半份 JSON（fail loud：
    // 寫入本身失敗會直接 throw，不吞錯誤，讓呼叫端這次 run 判定失敗）。
    const tmpPath = `${evidencePath}.tmp-${process.pid}`;
    fs.writeFileSync(tmpPath, JSON.stringify(payload, null, 2));
    fs.renameSync(tmpPath, evidencePath);

    if (finalStatus === 'passed') {
      if (bad.length > 0) {
        const detail = bad.map(([k, r]) => `${k}=${r.status}${r.status === 'read_error' ? `（${r.error}）` : ''}`).join('、');
        throw new Error(`GateEvidenceRecorder.flush: finalStatus=passed 但以下 journal 無法完整讀取，不得判定為 passed：${detail}（證據已寫至 ${evidencePath} 供事後鑑識）`);
      }
    }
  }
}
