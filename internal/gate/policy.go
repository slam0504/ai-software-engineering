package gate

import "fmt"

// StaleCause describes why an active approval's bindings no longer match
// current state (mirrors the transition fields written by ReconcileGate1).
type StaleCause struct{ Cause, EvidenceRef string }

// DecisionInput is Decide's restricted input (§3.1). Bindings are always
// copied by Service from the request; policies never see raw request
// bindings here.
type DecisionInput struct {
	RiskSelections []RiskSelection // gate2 用；gate1/tca 為空
}

type RiskSelection struct{ TaskID, SelectedRiskTier, OverrideReason string }

// GatePolicy encapsulates the per-gate rules that used to be hardcoded in
// Service: request validation, decision metadata construction, the
// supersession key, and stale-binding reconciliation.
type GatePolicy interface {
	ValidateRequest(req GateRequest) error
	// BuildDecision 驗證受限輸入並回傳要寫入 record 的 Metadata（§3.3）。
	// decision=="rejected" 時 input 必須為空（免 risk 輸入）。
	BuildDecision(req GateRequest, decision string, input DecisionInput) (*Metadata, error)
	SupersessionKey(gate, subject string) string // 預設 gate+"|"+subject
	ReconcileBindings(rec ApprovalRecord) ([]StaleCause, error)
}

// Registry maps a gate name to its policy.
type Registry map[string]GatePolicy

type gate1Policy struct {
	current ManifestFn
}

// NewGate1Policy returns the GatePolicy for gate1 (spec_manifest/base_commit
// bindings), backed by current for reading the active manifest digest.
func NewGate1Policy(current ManifestFn) GatePolicy {
	return &gate1Policy{current: current}
}

func (p *gate1Policy) ValidateRequest(req GateRequest) error {
	return ValidateGate1Bindings(req.Bindings)
}

func (p *gate1Policy) BuildDecision(req GateRequest, decision string, input DecisionInput) (*Metadata, error) {
	if len(input.RiskSelections) > 0 {
		return nil, fmt.Errorf("gate1: risk selections not accepted")
	}
	return nil, nil
}

func (p *gate1Policy) SupersessionKey(gateName, subject string) string {
	return gateName + "|" + subject
}

// ReconcileBindings compares the record's bound spec_manifest digest against
// the current manifest. A current() read error is returned as-is (fail
// closed, per §3.9) rather than reported as a stale cause — mirrors
// ReconcileGate1's existing semantics (service.go:114-148).
func (p *gate1Policy) ReconcileBindings(rec ApprovalRecord) ([]StaleCause, error) {
	cur, err := p.current()
	if err != nil {
		return nil, err
	}
	bound := ""
	for _, b := range rec.Bindings {
		if b.Kind == "spec_manifest" {
			bound = b.Digest
		}
	}
	if bound != "" && bound != cur {
		return []StaleCause{{Cause: "spec_manifest changed", EvidenceRef: cur}}, nil
	}
	return nil, nil
}
