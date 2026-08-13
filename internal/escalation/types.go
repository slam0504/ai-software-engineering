// Package escalation implements the escalation inbox (§3.8): an
// append-only journal of escalation items and their state transitions
// (open → acknowledged → resolved), plus the pure projection/query
// functions used to derive blocking state and condition-key dedup from
// journal lines. It mirrors internal/gate and internal/evidence's
// journal+projection shape, built on the shared internal/journal.
package escalation

// Item is an "escalation_item" journal record: the append-only creation of
// one escalation inbox entry. System-sourced items carry a ConditionKey
// (e.g. "stale:gate2:P1") used for dedup among not-yet-resolved items
// (§3.8); manual items leave it empty and must instead set SourceRef.
type Item struct {
	Type         string `json:"_type"`         // "escalation_item"
	EscalationID string `json:"escalation_id"` // ULID
	ConditionKey string `json:"condition_key"` // 系統項去重鍵，如 "stale:gate2:P1"；手動項空
	Occurrence   int    `json:"occurrence"`    // 同 condition 第 N 次（§3.8）
	Source       string `json:"source"`        // system | manual
	SourceRef    string `json:"source_ref"`    // plan_id/task_id/approval_id/evidence_id/event_id（手動必填）
	BlockScope   string `json:"block_scope"`   // workspace | gate2:<plan_id> | tca:<plan_id>/<task_id> | evidence:<id> | ""（非阻擋）
	Hard         bool   `json:"hard"`          // 硬性項：UI 不可手動 resolve（§3.8）
	Summary      string `json:"summary"`
	CreatedAt    string `json:"created_at"`
}

// ItemTransition is an "escalation_transition" journal record: append-only
// state change for an existing Item. To must be "acknowledged" or
// "resolved"; a "resolved" transition must carry Resolution, Reason and
// Actor (§3.8) — Project enforces this (see project.go), the journal layer
// itself does not.
type ItemTransition struct {
	Type         string `json:"_type"` // "escalation_transition"
	EscalationID string `json:"escalation_id"`
	To           string `json:"to"`                   // acknowledged | resolved
	Resolution   string `json:"resolution,omitempty"` // resolved 必填；accepted_risk 等
	Reason       string `json:"reason,omitempty"`
	Actor        string `json:"actor,omitempty"`
	At           string `json:"at"`
}

// State constants for Entry.State (§3.8's three-state disposition:
// open → acknowledged → resolved).
const (
	StateOpen         = "open"
	StateAcknowledged = "acknowledged"
	StateResolved     = "resolved"
)

// Entry is the projected state of one escalation item: its creation record
// plus the current disposition state derived by replaying every
// ItemTransition targeting it.
type Entry struct {
	Item  Item
	State string // open|acknowledged|resolved
}
