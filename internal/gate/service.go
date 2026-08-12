package gate

import (
	"encoding/json"
	"errors"
	"sync"
)

var (
	ErrNotPending        = errors.New("gate: no pending request for approval id")
	ErrRejectNeedsReason = errors.New("gate: rejected decision requires reason")
)

// ManifestFn returns the digest of the currently active spec manifest.
type ManifestFn func() (string, error)

// Emitter is notified of gate events AFTER the corresponding journal append
// has durably succeeded. Emitter failures never roll back the journal.
type Emitter interface {
	EmitGateEvent(kind string, bindings []Binding, payload any)
}

// Service is the gate application layer: it ties the Journal (crash-durable
// append log) and Project (pure reducer) together to provide Submit/Decide/
// List/ReconcileGate1 with the concurrency guarantees required by spec
// §3.2/§3.3/§3.5.
type Service struct {
	mu      sync.Mutex
	j       *Journal
	current ManifestFn
	ulid    func() string
	now     func() string
	em      Emitter
}

func NewService(j *Journal, current ManifestFn, ulid func() string, now func() string, em Emitter) *Service {
	return &Service{j: j, current: current, ulid: ulid, now: now, em: em}
}

// Submit appends a gate_request op and returns its approval id.
func (s *Service) Submit(manifestDigest, baseCommit string, bindings []Binding) (string, error) {
	if err := ValidateGate1Bindings(bindings); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.ulid()
	req := GateRequest{Type: "gate_request", ApprovalID: id, Gate: "gate1",
		SpecManifestDigest: manifestDigest, BaseCommit: baseCommit, CreatedAt: s.now()}
	if err := s.appendOp(req); err != nil {
		return "", err
	}
	s.em.EmitGateEvent("gate_request", bindings, map[string]string{"approval_id": id, "gate": "gate1"})
	return id, nil
}

// Decide records an approval/rejection decision for a pending approval id.
// Concurrency correctness: the pending-check and the append happen while
// holding s.mu, so concurrent Decide calls for the same id race on the
// mutex, not on the journal — exactly one observes the id as still pending.
func (s *Service) Decide(id, decision, reason string, approver Approver, bindings []Binding) error {
	if decision == "rejected" && reason == "" {
		return ErrRejectNeedsReason
	}
	if decision == "approved" {
		if err := ValidateGate1Bindings(bindings); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	e := findEntry(entries, id)
	if e == nil || e.Record != nil || e.Request == nil { // must be pending: has request, no record yet
		return ErrNotPending
	}
	recs := []any{ApprovalRecord{Type: "approval_record", ApprovalID: id, Gate: "gate1",
		Decision: decision, Approver: approver, Reason: reason, Bindings: bindings, CreatedAt: s.now()}}
	if decision == "approved" { // supersede any previously active approval in the same gate_op
		for _, prev := range entries {
			if prev.State == Active {
				recs = append(recs, Transition{Type: "transition", ApprovalID: prev.ApprovalID,
					To: "superseded", At: s.now(), Cause: "new approved gate1 " + id})
			}
		}
	}
	if err := s.appendOp(recs...); err != nil {
		return err
	}
	s.em.EmitGateEvent("approval_decision", bindings, map[string]any{
		"approval_id": id, "gate": "gate1", "decision": decision,
		"approver": approver, "reason": reason})
	return nil
}

// List reconciles gate1 bindings against the current manifest, then returns
// the projection.
func (s *Service) List() ([]GateEntry, error) {
	if err := s.ReconcileGate1(); err != nil {
		return nil, err
	}
	return Project(s.j.Ops())
}

// ReconcileGate1 marks any Active entry whose bound spec_manifest digest no
// longer matches the current manifest as stale. A ManifestFn read error
// (e.g. ErrConcurrentModification) is returned WITHOUT appending stale
// (fail closed, not permanent stale). The active-check and the append
// happen atomically under s.mu so racing calls produce at most one stale
// transition per entry.
func (s *Service) ReconcileGate1() error {
	cur, err := s.current()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.State != Active || e.Record == nil {
			continue
		}
		bound := ""
		for _, b := range e.Record.Bindings {
			if b.Kind == "spec_manifest" {
				bound = b.Digest
			}
		}
		if bound != "" && bound != cur { // active-check happened under lock above: at most one stale
			tr := Transition{Type: "transition", ApprovalID: e.ApprovalID, To: "stale",
				At: s.now(), Cause: "spec_manifest changed", EvidenceRef: cur}
			if err := s.appendOp(tr); err != nil {
				return err
			}
			// durable append succeeded before we notify; Emitter failure never rolls back the journal
			s.em.EmitGateEvent("binding_stale", nil, map[string]string{
				"approval_id": e.ApprovalID, "to": "stale",
				"cause": "spec_manifest changed", "evidence_ref": cur})
		}
	}
	return nil
}

func (s *Service) appendOp(recs ...any) error {
	raws, err := marshalRecords(recs...)
	if err != nil {
		return err
	}
	return s.j.Append(GateOp{OpID: s.ulid(), At: s.now(), Records: raws})
}

func marshalRecords(recs ...any) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(recs))
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func findEntry(entries []GateEntry, id string) *GateEntry {
	for i := range entries {
		if entries[i].ApprovalID == id {
			return &entries[i]
		}
	}
	return nil
}
