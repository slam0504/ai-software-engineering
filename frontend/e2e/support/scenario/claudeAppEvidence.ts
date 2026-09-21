// B3a-2b-2 F2：Claude approval 的 **App 端證據挑選與三方一致性判定**。
//
// 兩件事：
//  1. 真 App 的 broker audit 是寫進 `<stateDir>/audit.jsonl`（app.go auditWriterFor），
//     裡面混著 App 自己的其他 audit 行。要交給 F1a 的 `judgeClaudeApprovalEvidence`
//     之前必須先挑出**本次 approval 的 broker 行**。
//  2. 三方一致：MCP transcript ↔ App audit ↔ UI（DOM）observed approval id。
//
// **broker id 是 observation，不預造**（mcpserver.go:68 `newULID()`）；三方一致
// 的判定方式是「三者必須是同一個非空值」，不是拿預先寫好的字面值去比。
import { judgeClaudeApprovalEvidence, type ClaudeApprovalEvidenceJudgement }
  from './claudeApprovalJudge.ts';
import type { ClaudeApprovalExpectation } from './claudeApprovalProtocol.ts';

function isPlainObject(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}

/**
 * 從 audit.jsonl 的所有行中挑出**與這個 approval id 相關的 broker 行**。
 *
 * 刻意涵蓋 `timeout`／`malformed_request`：broker 的 timeout 行 data 是**裸字串
 * id**（broker.go:114 `b.log("timeout", req.ID)`），若只認 `data.id` 物件形式，
 * 逾時事件會被過濾掉而讓判定誤以為「乾淨的 request→decision 兩列」。
 */
export function selectBrokerAuditForApproval(lines: unknown[], approvalId: string): unknown[] {
  if (approvalId === '') return [];
  return lines.filter(l => {
    if (!isPlainObject(l)) return false;
    const d = l.data;
    if (isPlainObject(d)) return d.id === approvalId;
    return typeof d === 'string' && d === approvalId;
  });
}

/** 解析 audit.jsonl 原文為逐行物件；無法解析的行保留成診斷用的標記物件。 */
export function parseAuditLines(text: string): { records: unknown[]; unparsable: string[] } {
  const records: unknown[] = [];
  const unparsable: string[] = [];
  for (const line of text.split('\n')) {
    if (line.trim() === '') continue;
    try { records.push(JSON.parse(line) as unknown); }
    catch { unparsable.push(line); }
  }
  return { records, unparsable };
}

export interface ClaudeUiConsistencyInput {
  /** 假 CLI 記錄的完整 MCP transcript（五筆，不裁切）。 */
  transcript: unknown;
  /** 已依 approval id 挑出的 broker audit 行。 */
  brokerAudit: unknown[];
  /** DOM `[data-test="approval-dialog"]` 的 data-test-wsid。 */
  domWsid: string | null;
  /** DOM 的 data-test-approval-id——**UI 這一方的獨立觀察值**。 */
  domApprovalId: string | null;
  /** 假 CLI 自 argv 取得的真 App config 路徑（檔名含 WSID）。 */
  mcpConfigPath: string;
  exp: ClaudeApprovalExpectation;
}

export interface ClaudeUiConsistencyResult extends ClaudeApprovalEvidenceJudgement {
  /** 三方都同意的 approval id；判定通過時為非空字串。 */
  agreedApprovalId: string | null;
  /** 自 config 檔名解析出的 WSID（mcp-<WSID>.json，app.go:7395）。 */
  configWsid: string | null;
}

/** 自 `<stateDir>/mcp-<WSID>.json` 解析 WSID。 */
export function wsidFromMcpConfigPath(p: string): string | null {
  const base = p.split('/').pop() ?? '';
  const m = /^mcp-(.+)\.json$/.exec(base);
  return m === null || m[1] === '' ? null : m[1];
}

/**
 * **完整判定入口**：內部呼叫已審的 `judgeClaudeApprovalEvidence`（不另造較弱路徑），
 * 再加上 UI 側的三方一致性。任一方缺失或不一致都判失敗。
 */
export function judgeClaudeUiConsistency(i: ClaudeUiConsistencyInput): ClaudeUiConsistencyResult {
  const base = judgeClaudeApprovalEvidence(i.transcript, i.brokerAudit, i.exp);
  const violations = [...base.violations];

  if (i.domApprovalId === null || i.domApprovalId === '') {
    violations.push('[ui] DOM 未取得 data-test-approval-id');
  }
  if (i.domWsid === null || i.domWsid === '') {
    violations.push('[ui] DOM 未取得 data-test-wsid');
  }
  const configWsid = wsidFromMcpConfigPath(i.mcpConfigPath);
  if (configWsid === null) {
    violations.push(`[ui] 無法自 config 路徑解析 WSID（預期 mcp-<WSID>.json）：${JSON.stringify(i.mcpConfigPath)}`);
  } else if (i.domWsid !== null && i.domWsid !== '' && configWsid !== i.domWsid) {
    violations.push(`[ui] config 檔名的 WSID（${configWsid}）與 DOM 的 data-test-wsid（${i.domWsid}）不符`);
  }

  // 三方一致：broker audit 觀察到的 id（由 judgeClaudeApprovalEvidence 回傳）
  // 必須與 DOM 的 observed id 相同且非空。
  let agreed: string | null = null;
  if (base.observedBrokerId === null || base.observedBrokerId === '') {
    violations.push('[ui] broker audit 未觀察到非空 approval id，無法建立三方一致');
  } else if (i.domApprovalId !== null && i.domApprovalId !== '') {
    if (base.observedBrokerId !== i.domApprovalId) {
      violations.push(`[ui] broker audit 的 approval id（${base.observedBrokerId}）與 DOM 觀察到的（${i.domApprovalId}）不符`);
    } else {
      agreed = base.observedBrokerId;
    }
  }

  return {
    violations,
    observedBrokerId: base.observedBrokerId,
    agreedApprovalId: violations.length === 0 ? agreed : null,
    configWsid,
  };
}
