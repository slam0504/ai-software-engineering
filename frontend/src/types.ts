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
export interface Bindings {
  StartSession(provider: string, prompt: string, resume: string, recordCase: string,
    taskLabel: string, approvalPolicy: string): Promise<void>
  SendMessage(provider: string, prompt: string): Promise<void>
}
