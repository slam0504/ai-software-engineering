package gate

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeEmitter records emitted gate events; safe for concurrent use.
type fakeEmitter struct {
	mu     sync.Mutex
	events []fakeEvent
}

type fakeEvent struct {
	kind     string
	bindings []Binding
	payload  any
}

func (f *fakeEmitter) EmitGateEvent(kind string, bindings []Binding, payload any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fakeEvent{kind: kind, bindings: bindings, payload: payload})
}

// opsForTest exposes the service's journal ops for test-only projection.
func (s *Service) opsForTest() []GateOp { return s.j.Ops() }

// mutateCurrentManifest flips gate1's current-manifest reader to a digest
// that won't match any binding built by gate1Bindings()/gate1BWith(), so any
// pending gate1 request becomes stale mid-flight (test-only; requires the
// registry built by newTestServiceWithGates).
func (s *Service) mutateCurrentManifest() {
	p, ok := s.reg["gate1"].(*gate1Policy)
	if !ok {
		panic("mutateCurrentManifest: gate1 policy missing or wrong type")
	}
	p.current = func() (string, error) { return "sha256:" + hex64b(), nil }
}

// counterULID returns a monotonically increasing, unique-per-call id
// ("u1", "u2", ...); safe under concurrent use.
func counterULID() func() string {
	var n int64
	return func() string {
		return fmt.Sprintf("u%d", atomic.AddInt64(&n, 1))
	}
}

func newTestServiceWithCurrent(t *testing.T, currentFn ManifestFn) (*Service, *fakeEmitter) {
	t.Helper()
	j, err := OpenJournal(filepath.Join(t.TempDir(), "gate.jsonl"))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	em := &fakeEmitter{}
	now := func() string { return "t" }
	reg := Registry{"gate1": NewGate1Policy(currentFn)}
	s := NewService(j, reg, counterULID(), now, em)
	return s, em
}

func newTestService(t *testing.T) (*Service, *fakeEmitter) {
	t.Helper()
	return newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
}

// stubPolicy is a minimal GatePolicy stand-in for a not-yet-implemented gate
// (e.g. TCA, Task 21) — no binding validation, no risk metadata, default
// SupersessionKey, never stale. Only used to exercise Service's per-gate
// generalization (registry lookup, scoped supersession) ahead of the real
// policy landing.
type stubPolicy struct{}

func (stubPolicy) ValidateRequest(req GateRequest) error { return nil }
func (stubPolicy) BuildDecision(req GateRequest, decision string, input DecisionInput) (*Metadata, error) {
	return nil, nil
}
func (stubPolicy) SupersessionKey(gateName, subject string) string { return gateName + "|" + subject }
func (stubPolicy) ReconcileBindings(rec ApprovalRecord) ([]StaleCause, error) {
	return nil, nil
}

// newTestServiceWithGates builds a Service whose registry has "gate1"
// (backed by a fixed current-manifest digest that matches gate1Bindings())
// plus a "test_contract_approval" stub gate, for tests exercising
// cross-gate behavior (scoped supersession, generalized Submit/Decide).
func newTestServiceWithGates(t *testing.T) *Service {
	t.Helper()
	j, err := OpenJournal(filepath.Join(t.TempDir(), "gate.jsonl"))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	em := &fakeEmitter{}
	now := func() string { return "t" }
	current := func() (string, error) { return "sha256:" + hex64(), nil }
	reg := Registry{
		"gate1":                  NewGate1Policy(current),
		"test_contract_approval": stubPolicy{},
	}
	return NewService(j, reg, counterULID(), now, em)
}

func mustProjectOps(t *testing.T, s *Service) []GateEntry {
	t.Helper()
	es, err := Project(s.opsForTest())
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return es
}

func stateOf(entries []GateEntry, id string) State {
	return entryByID(entries, id).State
}

func recordOf(entries []GateEntry, id string) ApprovalRecord {
	e := entryByID(entries, id)
	if e.Record == nil {
		panic(fmt.Sprintf("recordOf: approval %q has no record", id))
	}
	return *e.Record
}

func approver() Approver { return Approver{ID: "u", Method: "app-local"} }

// hex64b returns a 64-hex string that differs from hex64().
func hex64b() string { return strings.Repeat("b", 64) }

// gate1BWith returns valid gate1 bindings with spec_manifest digest m and a
// fixed valid base_commit.
func gate1BWith(m string) []Binding {
	return gate1B(m, "git:sha1:"+hex40())
}

// gate1Bindings returns valid gate1 bindings matching the fixed current
// manifest digest used by newTestService/newTestServiceWithGates.
func gate1Bindings() []Binding {
	return gate1B("sha256:"+hex64(), "git:sha1:"+hex40())
}

// tcaStubBindings returns an arbitrary binding set accepted by stubPolicy
// (which does no validation).
func tcaStubBindings() []Binding {
	return []Binding{{Kind: "test_report", Ref: "evidence:01", Digest: "sha256:" + hex64()}}
}

func TestDecideOnlyOnceUnderConcurrency(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("gate1", "workspace", gate1Bindings())
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, DecisionInput{}); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("exactly one Decide must succeed, got %d", ok)
	}
}

func TestReconcileAppendsSingleStaleThenNoRevive(t *testing.T) {
	digest := "sha256:" + hex64()
	changed := "sha256:" + hex64b()
	cur := digest
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return cur, nil })
	id, _ := s.Submit("gate1", "workspace", gate1BWith(digest))
	_ = s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, DecisionInput{})
	cur = changed // spec changed
	if err := s.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(); err != nil { // concurrent/re-entrant call must still yield only one transition
		t.Fatal(err)
	}
	e := entryByID(mustProjectOps(t, s), id)
	if e.State != Stale {
		t.Fatalf("want stale, got %s", e.State)
	}
	cur = digest // reverting must not revive
	_ = s.Reconcile()
	if entryByID(mustProjectOps(t, s), id).State != Stale {
		t.Fatal("stale must not revive")
	}
}

// TestReconcileSingleStaleUnderConcurrency is the spec §7 barrier: a watcher
// (Reconcile) and GateList (List, which also reconciles) racing against
// each other must produce exactly ONE stale transition for the affected
// approval id — never a duplicate. Unlike
// TestReconcileAppendsSingleStaleThenNoRevive (which calls Reconcile
// sequentially and only checks the projected terminal state), this test
// fires 8 goroutines concurrently and counts raw "stale" transition records
// in the journal, which would catch a bug that appends a redundant stale
// transition under a genuine race.
func TestReconcileSingleStaleUnderConcurrency(t *testing.T) {
	digest := "sha256:" + hex64()
	changed := "sha256:" + hex64b()
	var curMu sync.Mutex
	cur := digest
	s, _ := newTestServiceWithCurrent(t, func() (string, error) {
		curMu.Lock()
		defer curMu.Unlock()
		return cur, nil
	})
	id, _ := s.Submit("gate1", "workspace", gate1BWith(digest))
	if err := s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, DecisionInput{}); err != nil {
		t.Fatal(err)
	}

	curMu.Lock()
	cur = changed // spec changed, before any concurrent reconcile starts
	curMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				if _, err := s.List(); err != nil {
					t.Error(err)
				}
			} else if err := s.Reconcile(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	staleCount := 0
	for _, op := range s.opsForTest() {
		for _, raw := range op.Records {
			var probe struct {
				Type       string `json:"_type"`
				ApprovalID string `json:"approval_id"`
				To         string `json:"to"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("unmarshal record: %v", err)
			}
			if probe.Type == "transition" && probe.ApprovalID == id && probe.To == "stale" {
				staleCount++
			}
		}
	}
	if staleCount != 1 {
		t.Fatalf("want exactly 1 stale transition under concurrency, got %d", staleCount)
	}
}

func TestRejectNeedsReason(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("gate1", "workspace", gate1BWith("sha256:"+hex64()))
	if err := s.Decide(id, "rejected", "", Approver{ID: "u", Method: "app-local"}, DecisionInput{}); err == nil {
		t.Fatal("rejected must require reason")
	}
}

// TestSupersessionScopedByGateAndSubject is the §3.1 core invariant: only
// active entries sharing the SAME (gate, subject) supersession key get
// superseded by a new approval — approving a TCA request must never
// supersede an unrelated gate1 approval, and TCA approvals for different
// plan/task subjects must not supersede each other.
func TestSupersessionScopedByGateAndSubject(t *testing.T) {
	s := newTestServiceWithGates(t)
	id1, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id1, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	id2, err := s.Submit("test_contract_approval", "task:P1/T1", tcaStubBindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id2, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(entries, id1) != Active { // 核可 TCA 不得 supersede Gate 1（§3.1）
		t.Fatal("gate1 approval must survive TCA approval")
	}
	id3, _ := s.Submit("test_contract_approval", "task:P2/T1", tcaStubBindings())
	if err := s.Decide(id3, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	entries, _ = s.List()
	if stateOf(entries, id2) != Active { // 不同 plan 的同名 T1 不互相 supersede
		t.Fatal("different plan_id must not supersede")
	}
	id4, _ := s.Submit("test_contract_approval", "task:P1/T1", tcaStubBindings())
	if err := s.Decide(id4, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	entries, _ = s.List()
	if stateOf(entries, id2) != Superseded { // 同 (gate,subject) 才 supersede
		t.Fatal("same subject must supersede")
	}
}

func TestDecideCopiesBindingsFromRequest(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	rec := recordOf(entries, id)
	if len(rec.Bindings) != 2 || rec.Subject != "workspace" || rec.SchemaVersion != 2 {
		t.Fatalf("record must copy gate/subject/bindings from request: %+v", rec)
	}
}

func TestRejectedNeedsOnlyReason(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id, "rejected", "不完整", approver(), DecisionInput{}); err != nil {
		t.Fatalf("rejected must not require risk input: %v", err)
	}
}

// ---- Lookup (used by the TCA gate2_approval resolver) --------------------

func TestServiceLookupReturnsRecordAndActiveState(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	rec, state, err := s.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if state != Active {
		t.Fatalf("want Active, got %s", state)
	}
	if rec == nil || rec.ApprovalID != id {
		t.Fatalf("want record for %s, got %+v", id, rec)
	}
}

func TestServiceLookupPendingHasNilRecord(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	rec, state, err := s.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if state != Pending {
		t.Fatalf("want Pending, got %s", state)
	}
	if rec != nil {
		t.Fatalf("pending approval must have a nil record, got %+v", rec)
	}
}

func TestServiceLookupUnknownIDErrors(t *testing.T) {
	s := newTestServiceWithGates(t)
	if _, _, err := s.Lookup("does-not-exist"); err == nil {
		t.Fatal("Lookup on an unknown approval id must error")
	}
}

// TestCommitDecisionSupersessionRecordAndTransitionShareOneGateOp is the raw
// journal counterpart to TestSupersessionScopedByGateAndSubject: CommitDecision's
// doc comment promises the new approval_record and every "superseded"
// transition it triggers land in a single gate_op (service.go:131,
// s.appendOp(recs...) called once with the whole recs slice) — not two
// separate appends that a crash between them could split. This reads
// s.opsForTest() (raw GateOp.Records) directly, since the projection alone
// can't distinguish "two ops" from "one op" once both are Active/Superseded.
func TestCommitDecisionSupersessionRecordAndTransitionShareOneGateOp(t *testing.T) {
	s := newTestServiceWithGates(t)
	id1, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id1, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	id2, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(id2, "approved", "", approver(), DecisionInput{}); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(entries, id1) != Superseded {
		t.Fatal("id1 must be superseded by id2's approval (same gate1|workspace supersession key)")
	}
	if stateOf(entries, id2) != Active {
		t.Fatal("id2 must be active")
	}

	var opWithRecord, opWithTransition *GateOp
	for i, op := range s.opsForTest() {
		for _, raw := range op.Records {
			var probe struct {
				Type       string `json:"_type"`
				ApprovalID string `json:"approval_id"`
				To         string `json:"to"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("unmarshal record: %v", err)
			}
			if probe.Type == "approval_record" && probe.ApprovalID == id2 {
				opWithRecord = &s.opsForTest()[i]
			}
			if probe.Type == "transition" && probe.ApprovalID == id1 && probe.To == "superseded" {
				opWithTransition = &s.opsForTest()[i]
			}
		}
	}
	if opWithRecord == nil {
		t.Fatal("no GateOp found containing id2's approval_record")
	}
	if opWithTransition == nil {
		t.Fatal("no GateOp found containing id1's superseded transition")
	}
	if opWithRecord.OpID != opWithTransition.OpID {
		t.Fatalf("id2's approval_record (op %s) and id1's superseded transition (op %s) must land in the SAME GateOp",
			opWithRecord.OpID, opWithTransition.OpID)
	}
}

func TestStalePendingApproveFailsRejectSucceeds(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, err := s.Submit("gate1", "workspace", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	s.mutateCurrentManifest() // 待核期間規格變更 → pending bindings 過期
	if _, err := s.PrepareDecision(id, "approved", "", approver(), DecisionInput{}); err == nil {
		t.Fatal("approve on stale pending must fail (current-binding validation)")
	}
	if _, err := s.PrepareDecision(id, "rejected", "已過期", approver(), DecisionInput{}); err != nil {
		t.Fatalf("reject on stale pending must still succeed: %v", err) // rejected 免驗證
	}
}
