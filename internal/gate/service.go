package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrNotPending        = errors.New("gate: no pending request for approval id")
	ErrRejectNeedsReason = errors.New("gate: rejected decision requires reason")
	ErrUnknownGate       = errors.New("gate: unknown gate")
)

// Emitter is notified of gate events AFTER the corresponding journal append
// has durably succeeded. Emitter failures never roll back the journal.
type Emitter interface {
	EmitGateEvent(kind string, bindings []Binding, payload any)
}

// Service is the gate application layer: it ties the Journal (crash-durable
// append log) and Project (pure reducer) together to provide
// Submit/PrepareDecision/CommitDecision/Decide/List/Reconcile with the
// concurrency guarantees required by spec §3.2/§3.3/§3.5. Per-gate rules
// (request validation, decision metadata, supersession scoping, staleness)
// live in the Registry's GatePolicy implementations — Service itself is
// gate-agnostic (§2.1/§3.1).
type Service struct {
	mu   sync.Mutex
	j    *Journal
	reg  Registry
	ulid func() string
	now  func() string
	em   Emitter
}

func NewService(j *Journal, reg Registry, ulid func() string, now func() string, em Emitter) *Service {
	return &Service{j: j, reg: reg, ulid: ulid, now: now, em: em}
}

// Submit appends a gate_request op under gateName and returns its approval
// id. The request is validated (in v2 shape) against gateName's policy
// before anything is written; unknown gates are rejected outright.
func (s *Service) Submit(gateName, subject string, bindings []Binding) (string, error) {
	policy, ok := s.reg[gateName]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownGate, gateName)
	}
	req := GateRequest{Type: "gate_request", SchemaVersion: 2, Gate: gateName,
		Subject: subject, Bindings: bindings}
	if err := policy.ValidateRequest(req); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req.ApprovalID = s.ulid()
	req.CreatedAt = s.now()
	if err := s.appendOp(req); err != nil {
		return "", err
	}
	s.em.EmitGateEvent("gate_request", bindings, map[string]string{"approval_id": req.ApprovalID, "gate": gateName})
	return req.ApprovalID, nil
}

// PreparedDecision is the not-yet-durable result of PrepareDecision, ready
// to be appended by CommitDecision.
type PreparedDecision struct{ Record ApprovalRecord }

// PrepareDecision validates a decision against a pending approval and
// builds (but does not append) its ApprovalRecord.
//
// Concurrency: runs entirely under s.mu — Project → pending check →
// normalizeRequest → (approved only) current-binding validation → hard
// decision validation, all observe a single consistent journal snapshot.
//
// Current-binding validation (approved only): Reconcile only scans the
// active projection, so it cannot catch a pending request whose bindings
// went stale (plan/oracle/upstream gate changed) while awaiting approval.
// PrepareDecision closes that gap by running the pending request's gate
// policy ReconcileBindings against a pseudo-record built from the request's
// own gate/subject/bindings; any stale cause or read error fails the
// approval closed. Rejected decisions skip this — a rejection only needs a
// reason and must still succeed on an otherwise-expired request.
func (s *Service) PrepareDecision(id, decision, reason string, approver Approver, input DecisionInput) (PreparedDecision, error) {
	if decision != "approved" && decision != "rejected" {
		return PreparedDecision{}, fmt.Errorf("gate: unknown decision %q, want \"approved\" or \"rejected\"", decision)
	}
	if decision == "rejected" && reason == "" {
		return PreparedDecision{}, ErrRejectNeedsReason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return PreparedDecision{}, err
	}
	e := findEntry(entries, id)
	if e == nil || e.Record != nil || e.Request == nil { // must be pending: has request, no record yet
		return PreparedDecision{}, ErrNotPending
	}
	req := normalizeRequest(*e.Request)
	policy, ok := s.reg[req.Gate]
	if !ok {
		return PreparedDecision{}, fmt.Errorf("%w %q", ErrUnknownGate, req.Gate)
	}
	if decision == "approved" {
		pseudo := ApprovalRecord{Gate: req.Gate, Subject: req.Subject, Bindings: req.Bindings}
		causes, rerr := policy.ReconcileBindings(pseudo)
		if rerr != nil {
			return PreparedDecision{}, rerr // fail closed (§3.9)
		}
		if len(causes) > 0 {
			return PreparedDecision{}, fmt.Errorf("gate: pending request bindings are stale: %s", causes[0].Cause)
		}
	}
	meta, berr := policy.BuildDecision(req, decision, input)
	if berr != nil {
		return PreparedDecision{}, berr
	}
	rec := ApprovalRecord{Type: "approval_record", SchemaVersion: 2, ApprovalID: id, Gate: req.Gate,
		Subject: req.Subject, Decision: decision, Approver: approver, Reason: reason,
		Bindings: req.Bindings, Metadata: meta, CreatedAt: s.now()}
	return PreparedDecision{Record: rec}, nil
}

// CommitDecision re-verifies the prepared record's approval id is still
// pending, then durably appends it. On approval, it also appends a
// "superseded" transition for every currently-Active entry whose own gate
// policy computes the same SupersessionKey as the new record — scoped by
// (gate, subject) rather than gate-wide, so e.g. approving a TCA request
// for one plan/task never supersedes an unrelated gate1 approval or a TCA
// approval for a different plan/task (§3.1). The record and any
// supersession transitions land in a single gate_op.
func (s *Service) CommitDecision(p PreparedDecision) error {
	rec := p.Record
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	e := findEntry(entries, rec.ApprovalID)
	if e == nil || e.Record != nil || e.Request == nil {
		return ErrNotPending
	}
	policy, ok := s.reg[rec.Gate]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownGate, rec.Gate)
	}
	recs := []any{rec}
	if rec.Decision == "approved" {
		newKey := policy.SupersessionKey(rec.Gate, rec.Subject)
		for _, prev := range entries {
			if prev.State != Active || prev.Record == nil {
				continue
			}
			prevPolicy, pok := s.reg[prev.Record.Gate]
			if !pok {
				continue // unknown gate on an existing active record: leave it alone rather than guess
			}
			if prevPolicy.SupersessionKey(prev.Record.Gate, prev.Record.Subject) == newKey {
				recs = append(recs, Transition{Type: "transition", ApprovalID: prev.ApprovalID,
					To: "superseded", At: s.now(), Cause: "new approved " + rec.Gate + " " + rec.ApprovalID})
			}
		}
	}
	if err := s.appendOp(recs...); err != nil {
		return err
	}
	s.em.EmitGateEvent("approval_decision", rec.Bindings, map[string]any{
		"approval_id": rec.ApprovalID, "gate": rec.Gate, "decision": rec.Decision,
		"approver": rec.Approver, "reason": rec.Reason})
	return nil
}

// Decide is the Prepare→Commit convenience wrapper — no blocker checks
// beyond what Prepare/Commit already do, for tests and the gate1
// compatibility path (spec §3.10 leaves the full reconcile→validator→
// blocker→append ordering to app-level callers that need blockers).
func (s *Service) Decide(id, decision, reason string, approver Approver, input DecisionInput) error {
	p, err := s.PrepareDecision(id, decision, reason, approver, input)
	if err != nil {
		return err
	}
	return s.CommitDecision(p)
}

// List reconciles every gate's bindings against current state, then returns
// the projection.
func (s *Service) List() ([]GateEntry, error) {
	if err := s.Reconcile(); err != nil {
		return nil, err
	}
	return Project(s.j.Ops())
}

// Lookup returns the (possibly nil, if still pending) record and current
// state for approvalID. Used by the TCA gate2_approval resolver to look up
// an approval by id without pulling the whole projection.
func (s *Service) Lookup(approvalID string) (*ApprovalRecord, State, error) {
	entries, err := Project(s.j.Ops())
	if err != nil {
		return nil, "", err
	}
	e := findEntry(entries, approvalID)
	if e == nil {
		return nil, "", fmt.Errorf("gate: approval id %q not found", approvalID)
	}
	return e.Record, e.State, nil
}

// Reconcile marks any Active entry whose bindings its gate's policy reports
// as stale — replacing the old gate1-only ReconcileGate1. Each entry is
// checked against `reg[entry's gate]`'s ReconcileBindings; a read error
// from any policy aborts the whole call WITHOUT appending further stale
// transitions (fail closed, not permanent stale — already-appended
// transitions from earlier entries in this pass stand, matching the
// at-most-once semantics below). The active-check and the append happen
// atomically under s.mu (the whole pass holds the lock throughout) so
// racing calls produce at most one stale transition per entry.
func (s *Service) Reconcile() error {
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
		policy, ok := s.reg[e.Record.Gate]
		if !ok {
			return fmt.Errorf("%w %q", ErrUnknownGate, e.Record.Gate)
		}
		causes, rerr := policy.ReconcileBindings(*e.Record)
		if rerr != nil {
			return rerr // fail closed (§3.9)
		}
		for _, c := range causes {
			tr := Transition{Type: "transition", ApprovalID: e.ApprovalID, To: "stale",
				At: s.now(), Cause: c.Cause, EvidenceRef: c.EvidenceRef}
			if err := s.appendOp(tr); err != nil {
				return err
			}
			// durable append succeeded before we notify; Emitter failure never rolls back the journal
			s.em.EmitGateEvent("binding_stale", nil, map[string]string{
				"approval_id": e.ApprovalID, "to": "stale",
				"cause": c.Cause, "evidence_ref": c.EvidenceRef})
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
