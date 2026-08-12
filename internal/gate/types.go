package gate

import "encoding/json"

type State string

const (
	Pending    State = "pending"
	Active     State = "active"
	Stale      State = "stale"
	Superseded State = "superseded"
)

type Binding struct {
	Kind   string `json:"kind"`
	Role   string `json:"role,omitempty"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}
type Approver struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}
type ApprovalRecord struct {
	Type       string    `json:"_type"` // "approval_record"
	ApprovalID string    `json:"approval_id"`
	Gate       string    `json:"gate"`
	Decision   string    `json:"decision"`
	Approver   Approver  `json:"approver"`
	Reason     string    `json:"reason"`
	Bindings   []Binding `json:"bindings"`
	CreatedAt  string    `json:"created_at"`
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
	Type               string `json:"_type"` // "gate_request"
	ApprovalID         string `json:"approval_id"`
	Gate               string `json:"gate"`
	SpecManifestDigest string `json:"spec_manifest_digest"`
	BaseCommit         string `json:"base_commit"`
	CreatedAt          string `json:"created_at"`
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
}
