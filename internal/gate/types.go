package gate

import "encoding/json"

type State string

const (
	Pending    State = "pending"
	Active     State = "active"
	Stale      State = "stale"
	Superseded State = "superseded"
	Rejected   State = "rejected"
	Expired    State = "expired" // B5 §4.3：pending Gate 3 request 決議時重驗確認不符後的終態；僅 Pending 可轉入
)

type Binding struct {
	Kind   string `json:"kind"`
	Role   string `json:"role,omitempty"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}
type RiskDecision struct {
	TaskID           string `json:"task_id"`
	MinimumRiskTier  string `json:"minimum_risk_tier"`
	PlannerRiskTier  string `json:"planner_risk_tier"`
	SelectedRiskTier string `json:"selected_risk_tier"`
	OverrideReason   string `json:"override_reason,omitempty"`
}
type Metadata struct {
	RiskDecisions []RiskDecision `json:"risk_decisions,omitempty"`
}
type Approver struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}
type ApprovalRecord struct {
	Type          string    `json:"_type"` // "approval_record"
	SchemaVersion int       `json:"schema_version,omitempty"`
	ApprovalID    string    `json:"approval_id"`
	Gate          string    `json:"gate"`
	Subject       string    `json:"subject,omitempty"`
	Decision      string    `json:"decision"`
	Approver      Approver  `json:"approver"`
	Reason        string    `json:"reason"`
	Bindings      []Binding `json:"bindings"`
	Metadata      *Metadata `json:"metadata,omitempty"`
	CreatedAt     string    `json:"created_at"`
}
type Transition struct {
	Type        string `json:"_type"` // "transition"
	ApprovalID  string `json:"approval_id"`
	To          string `json:"to"`
	At          string `json:"at"`
	Cause       string `json:"cause"`
	EvidenceRef string `json:"evidence_ref"`
}
type GateRequest struct {
	Type               string    `json:"_type"` // "gate_request"
	SchemaVersion      int       `json:"schema_version,omitempty"`
	ApprovalID         string    `json:"approval_id"`
	Gate               string    `json:"gate"`
	Subject            string    `json:"subject,omitempty"`
	Bindings           []Binding `json:"bindings,omitempty"`
	SpecManifestDigest string    `json:"spec_manifest_digest,omitempty"`
	BaseCommit         string    `json:"base_commit,omitempty"`
	CreatedAt          string    `json:"created_at"`
}
type GateOp struct {
	OpID    string            `json:"op_id"`
	At      string            `json:"at"`
	Records []json.RawMessage `json:"records"`
}
type GateEntry struct {
	ApprovalID string
	State      State
	Record     *ApprovalRecord
	Request    *GateRequest
	// TerminalCause：本筆進入目前終態（stale/superseded/expired）那次
	// transition 的 Cause；只在該次 transition 實際改變 State 時寫入（見
	// project.go 的 changed 旗標）。Rejected 恆為空——拒絕原因承載於
	// Record.Reason，不重複進本欄位（Task 6b）。
	TerminalCause string
}
