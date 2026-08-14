import type { evidence, escalation } from '../wailsjs/go/models'

// Envelope v1（M1 凍結契約）：欄位名對齊 Go contract.Envelope 的 json tag。
export interface Usage { input_tokens: number; output_tokens: number; cached_input_tokens?: number }
export interface ApprovalBinding { kind: string; role?: string; ref: string; digest: string }
export interface Envelope {
  event_id: string; ts: string; provider: string; session_id?: string; role?: string
  task_id?: string; kind: string; text?: string; thinking?: string; is_error?: boolean
  cost_usd?: number; usage?: Usage; usage_semantics?: string; state?: string
  error?: string; raw?: unknown
  // M2 Stage A：scope/purpose 分流用（§3.4c）＋ gate/assist payload 欄位
  scope?: string; bindings?: ApprovalBinding[]; payload?: unknown
  correlation_id?: string; purpose?: string
}
// gate.ts 的 projection entry（Task 14 console 消費）
export interface GateEntry {
  approval_id: string; state: string; gate?: string; subject?: string
  bindings?: ApprovalBinding[]; base_commit?: string
}
// GateDecisionContext 回傳的單一 task risk 列（Task 15，committed plan，唯讀）
export interface GateDecisionTask {
  task_id: string; title: string; minimum_risk_tier: string; planner_risk_tier: string
}
// GateDecide 第四參數的元素形狀——逐字鏡射 wailsjs gate.RiskSelection（PascalCase，
// 無 json tag 的 Go struct 直出，非 snake_case）
export interface RiskSelection {
  TaskID: string; SelectedRiskTier: string; OverrideReason: string
}
export interface ChatItem { role: 'user' | 'assistant'; text: string; thinking: string; streaming: boolean }
export interface TimelineItem { env: Envelope; group?: number }
// EvidenceCommitCandidates 回傳的單一候選（Task 22，main.CommitInfo 的
// snake_case-free 手動鏡射——CommitInfo 本身只有 oid/subject 兩個欄位，
// 不像 EvidenceRun 有 19 個欄位，值得直接手鏡射而非 import wailsjs 型別）。
export interface CommitCandidate { oid: string; subject: string }
// Bindings：session store（stores/session.ts）唯一消費的形狀——只有
// StartSession／SendMessage，維持原樣不擴大（session.test.ts 的 mock 只給這兩
// 個欄位，見 EvidenceBindings 的分離理由）。
export interface Bindings {
  StartSession(provider: string, prompt: string, resume: string, recordCase: string,
    taskLabel: string, approvalPolicy: string): Promise<void>
  SendMessage(provider: string, prompt: string): Promise<void>
}
// WorkspaceSessionBindings：M3b Task 4 純新增的 CreateSession——回傳新 session
// 的 WSID。獨立於 Bindings（session store 用）之外，理由同 EvidenceBindings：
// session store 目前只消費 StartSession／SendMessage，其 test mock 也只給這兩個
// 欄位；WSID 化的 store 切換是 Task 26 的原子改動，這裡不提前擴大 Bindings。
export interface WorkspaceSessionBindings {
  CreateSession(provider: string, taskLabel: string): Promise<string>
}
// CodexRecordingBindings：M3b Task 13 純新增的 RecoverCodexRecording——§3.4.6
// recorder error latch 的 in-process 復原入口（latch 期間新 Codex session 被拒，
// 這是唯一的解除路徑）。獨立成一個介面，理由同 WorkspaceSessionBindings：
// session store 不消費它，UI 接線是後續 task 的事，這裡不提前擴大 Bindings。
export interface CodexRecordingBindings {
  RecoverCodexRecording(): Promise<void>
}
// EvidenceBindings：Task 22 TCA workspace 六個多參數 Go 綁定——同 SendMessage
// 的既定教訓（M1.5 review P1-1），每個 adapter 都逐參數轉發，順序鎖在
// bindings.test.ts。獨立於 Bindings（session store 用）之外，makeBindings()
// 回傳的物件同時滿足兩者，但 App.vue 把這六個方法個別當 prop 傳給
// TcaWorkspace／GateConsole／EvidenceDetail，不整包依賴 session 的
// Bindings 形狀。
export interface EvidenceBindings {
  RegisterMutation(taskRef: string, patch: string): Promise<string>
  RunEvidence(expectedGate2ApprovalID: string, planID: string, taskID: string,
    testCommit: string, kind: string, mutationID: string): Promise<string>
  EvidenceGet(evidenceID: string): Promise<evidence.EvidenceRun>
  SubmitTestContract(planID: string, taskID: string, testCommit: string,
    expectedRedID: string, negativeControlID: string, mutationID: string): Promise<string>
  ValidateTestCommit(planID: string, taskID: string, testCommit: string): Promise<void>
  EvidenceCommitCandidates(planID: string): Promise<CommitCandidate[]>
}
// EscalationBindings：Task 25 EscalationInbox 四個綁定——同 EvidenceBindings
// 的既定教訓，逐參數轉發、順序鎖在 bindings.test.ts。
export interface EscalationBindings {
  EscalationList(): Promise<escalation.Entry[]>
  EscalationCreate(sourceRef: string, blockScope: string, summary: string): Promise<string>
  EscalationAck(id: string): Promise<void>
  EscalationResolve(id: string, resolution: string, reason: string): Promise<void>
}
