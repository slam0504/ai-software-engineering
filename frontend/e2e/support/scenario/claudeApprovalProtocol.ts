// B3a-2b-2 F1a：Claude approval（單一 allow 案）的**共用協定資料**。
//
// 範圍嚴格限定在 reviewer #347 裁定的「單一 Claude approval allow 案」：
// **只實作這一案需要的資料與型別，不做通用 MCP framework。**
//
// 契約來源（一手讀碼，非推測）：
//   internal/approval/mcpserver_test.go:73   initialize，protocolVersion="2025-06-18"
//   internal/approval/mcpserver_test.go:75   notifications/initialized（**不是** method="initialized"）
//   internal/approval/mcpserver_test.go:80   tools/call{name:"approval_prompt",
//                                            arguments:{tool_name,input}}
//   internal/approval/mcpserver.go:52        回覆是 CallToolResult.Content[0] 的 TextContent
//   internal/approval/mcpserver.go:70-76     text 內是 {behavior,message?,updatedInput?}
//                                            —— **不含 broker id**（mcpserver_test.go
//                                            明確檢查不洩漏 internal id）
//   internal/approval/broker.go:77-79        audit 每列 {ts,kind,data}
//   internal/approval/broker.go:119-120      allow 而 UpdatedInput 為空時，broker 補上 req.Input
//   internal/approval/broker.go Request/Decision 欄位名
//
// **broker/UI approval id 由 `newULID()` 隨機產生**（mcpserver.go:68），
// **builder 不可預先指定**——本檔只提供「可預先指定」的那些欄位，
// 隨機 id 一律當 observation 處理（見 claudeApprovalJudge.ts）。

export const MCP_PROTOCOL_VERSION = '2025-06-18';
export const APPROVAL_TOOL_NAME = 'approval_prompt';
/** MCP server 身分：internal/approval/mcpserver.go:41 `mcp.Implementation{Name:"workbench"}`。 */
export const MCP_SERVER_NAME = 'workbench';

/** 一筆 MCP transcript 事件。dir 以「假 claude（MCP client）」為視角。 */
export interface McpEvent {
  seq: number;
  dir: 'c2s' | 's2c';
  frame: Record<string, unknown>;
}

/** broker audit 的一列（broker.go:78 的 {ts,kind,data}）。 */
export interface BrokerAuditRecord {
  ts?: unknown;
  kind?: unknown;
  data?: unknown;
}

/**
 * 核定的 approval 決策。**這是協定層的值**（MCP payload 與 broker audit 用的
 * `behavior`），與 `ScenarioDef.decision` 的 `accept`／`decline` 是兩套詞彙，
 * 由 `protocolDecisionFor()` 明確轉換，不靠字串巧合。
 */
export type ApprovalDecision = 'allow' | 'deny';

/**
 * `ScenarioDef.decision`（accept／decline）→ 協定層 behavior（allow／deny）。
 * **未知值一律 throw**，不提供寬鬆預設——靜默回 allow 會讓一個壞掉的 deny 案
 * 走完整條 allow 判定而通過。
 */
export function protocolDecisionFor(scenarioDecision: string): ApprovalDecision {
  if (scenarioDecision === 'accept') return 'allow';
  if (scenarioDecision === 'decline') return 'deny';
  throw new Error(
    `protocolDecisionFor: 未知的 scenario decision ${JSON.stringify(scenarioDecision)}`
    + '——只接受 "accept"／"decline"，不回退',
  );
}

/**
 * 本案的**獨立期望**——全部由 runId 決定，與待驗輸出分開。
 * 不含 broker id：那是隨機的，只能在執行期觀察。
 */
export interface ClaudeApprovalExpectation {
  /** provider session id（假 claude 的 init 事件會回報這個值） */
  sessionId: string;
  /** MCP initialize 的 request id */
  initializeRequestId: number;
  /** MCP tools/call 的 request id */
  toolCallRequestId: number;
  /** 送進 approval 的 tool_name */
  toolName: string;
  /** input 內帶 runId 的唯一標記——用來把 MCP request 與 broker request 連起來 */
  inputMarker: string;
  /** 完整 input 物件（MCP arguments.input，也是 allow 時的 updatedInput） */
  input: Record<string, unknown>;
  /**
   * **本案核定的決策**（必填、無預設）。判定端一律以它分流；缺值或未知值
   * 都必須被拒絕，**不得靜默當成 allow**（reviewer #403 第 3、4 點）。
   */
  decision: ApprovalDecision;
  /**
   * deny 案核定的拒絕理由——由**既有** approval dialog 的 reason 輸入框填入
   * （ApprovalDialog.vue 的 `v-model="reason"`，預設空字串，App 原樣傳成
   * `Decision.Message`，MCP 回覆的 `message` 是 omitempty）。
   *
   * **注意**：production 並不要求 deny 一定有 message——空理由的 deny 是合法的。
   * 這個欄位只是**本 scenario 自己固定一個 run 專屬理由**，好讓判定能逐字核對
   * 「使用者這次按下的 deny」而不是任何一個合法 deny，也才分得出 fail-closed
   * 自動 deny。allow 案必須不帶這個欄位。
   */
  denyReason?: string;
  /** 假 claude 收到決策之後送出的可辨識完成文字（allow 與 deny 各自不同） */
  completionText: string;
}

/** 受版控 builder：同一個 runId 永遠得到同一份期望。 */
export function buildClaudeApprovalExpectation(runId: string): ClaudeApprovalExpectation {
  const marker = `b3a2b2-claude-approval-${runId}`;
  return {
    sessionId: `b3a2b2-claude-session-${runId}`,
    initializeRequestId: 1,
    toolCallRequestId: 2,
    toolName: 'Bash',
    inputMarker: marker,
    input: { command: `printf '%s' ${marker}` },
    decision: 'allow',
    completionText: `b3a2b2-claude-content-${runId}`,
  };
}

/**
 * 受版控 builder：**單一 Claude deny 案**（B3a-2b-2，reviewer #403 核定）。
 *
 * 與 allow builder 刻意完全分開，不是「allow 加個旗標」：兩案的 marker、input
 * 與完成內容都不同，任何一邊被拿去冒充另一邊都會在 marker 上當場露餡。
 * `denyReason` 是 run 專屬字串，且刻意**不含** `fail closed`——fail-closed 的
 * 自動 deny（broker 逾時／socket 不可達）訊息固定帶那段文字，判定據此分辨。
 */
export function buildClaudeDenyExpectation(runId: string): ClaudeApprovalExpectation {
  const marker = `b3a2b2-claude-deny-${runId}`;
  return {
    sessionId: `b3a2b2-claude-session-${runId}`,
    initializeRequestId: 1,
    toolCallRequestId: 2,
    toolName: 'Bash',
    inputMarker: marker,
    input: { command: `printf '%s' ${marker}` },
    decision: 'deny',
    denyReason: `b3a2b2-claude-deny-reason-${runId}`,
    completionText: `b3a2b2-claude-denied-content-${runId}`,
  };
}

// ---------------------------------------------------------------------------
// B3a-2b-2 E1（Claude recovery 最小檢查點）：兩輪 start → resume 的期望。
//
// **與上面的單輪 builder 完全分開，不改它**：F1a／F2 既有的單輪契約（恰一次
// 對話呼叫、argv 不得含 --resume）原樣保留，本段只新增兩輪案專用的期望。
//
// Claude 的 resume 身分與 Codex 不同：resume id 不是 App 自己造的 threadId，
// 而是**CLI 在 init 事件自己宣告**、再由 App 綁進 registry（app.go:7504
// registry.Bind ／ commitClaudeResume → wsReg.SetResume）。因此兩輪共用同一個
// sessionId S：第一輪由假 CLI 宣告 S，第二輪必須由真 App 以 `--resume S` 帶回。
// **S 只能來自這個固定 builder**，不得從第二輪的待驗 argv 反填。
// ---------------------------------------------------------------------------

/** 一輪對話的完整期望。 */
export interface ClaudeRoundExpectation {
  /** 1-based 輪次；與排他 claim 取得的輪次必須一致。 */
  round: number;
  /** 本輪 stdin 首行的 user text（逐字比對）。 */
  prompt: string;
  /** null＝fresh start（argv 不得出現 --resume）；字串＝argv 必須恰好帶 `--resume <值>`。 */
  resume: string | null;
  /** 本輪的 approval／完成內容期望。 */
  approval: ClaudeApprovalExpectation;
}

export interface ClaudeRecoveryExpectation {
  /** 兩輪共用的 provider session id（第一輪宣告、第二輪 resume）。 */
  sessionId: string;
  rounds: ClaudeRoundExpectation[];
}

/**
 * 受版控 builder：同一個 runId 永遠得到同一份兩輪期望。
 *
 * 兩輪刻意**每一項可辨識內容都不同**（prompt／inputMarker／input／完成文字），
 * 只有 sessionId 相同——這樣「沿用第一輪證據冒充第二輪」在任何一個欄位上都會
 * 當場露餡。
 */
export function buildClaudeRecoveryExpectation(runId: string): ClaudeRecoveryExpectation {
  const sessionId = `b3a2b2-claude-session-${runId}`;
  const round = (n: number, resume: string | null): ClaudeRoundExpectation => {
    const marker = `b3a2b2-claude-approval-r${n}-${runId}`;
    return {
      round: n,
      prompt: `b3a2b2-claude-prompt-r${n}-${runId}`,
      resume,
      approval: {
        sessionId,
        initializeRequestId: 1,
        toolCallRequestId: 2,
        toolName: 'Bash',
        inputMarker: marker,
        input: { command: `printf '%s' ${marker}` },
        decision: 'allow',
        completionText: `b3a2b2-claude-content-r${n}-${runId}`,
      },
    };
  };
  return { sessionId, rounds: [round(1, null), round(2, sessionId)] };
}
