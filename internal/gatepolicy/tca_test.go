package gatepolicy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/evidence"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// --- fakes (extend gate2_test.go's fakeLoader/nopGit/stubGit/fakeExitError/testGitRepo) ---

type fakeEvidenceStore struct {
	runs   map[string]evidence.EvidenceRun
	muts   map[string]evidence.Mutation
	getErr map[string]error
	mutErr map[string]error
}

func (f *fakeEvidenceStore) Get(id string) (evidence.EvidenceRun, error) {
	if err, ok := f.getErr[id]; ok {
		return evidence.EvidenceRun{}, err
	}
	r, ok := f.runs[id]
	if !ok {
		return evidence.EvidenceRun{}, fmt.Errorf("fakeEvidenceStore: no run %s", id)
	}
	return r, nil
}

func (f *fakeEvidenceStore) Mutation(id string) (evidence.Mutation, error) {
	if err, ok := f.mutErr[id]; ok {
		return evidence.Mutation{}, err
	}
	m, ok := f.muts[id]
	if !ok {
		return evidence.Mutation{}, fmt.Errorf("fakeEvidenceStore: no mutation %s", id)
	}
	return m, nil
}

type fakeGateEntry struct {
	rec   *gate.ApprovalRecord
	state gate.State
}

type fakeGateReader struct {
	entries map[string]fakeGateEntry
	err     error
}

func (f *fakeGateReader) Lookup(id string) (*gate.ApprovalRecord, gate.State, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	e, ok := f.entries[id]
	if !ok {
		return nil, "", fmt.Errorf("fakeGateReader: no entry %s", id)
	}
	return e.rec, e.state, nil
}

// fakeTCALoader extends fakeLoader (LoadAt) with LoadOracleAt.
type fakeTCALoader struct {
	*fakeLoader
	oracle    map[string]evidence.OracleDecl
	oracleErr error
}

func (f *fakeTCALoader) LoadOracleAt(commitOID string) (evidence.OracleDecl, error) {
	if f.oracleErr != nil {
		return evidence.OracleDecl{}, f.oracleErr
	}
	d, ok := f.oracle[commitOID]
	if !ok {
		return evidence.OracleDecl{}, fmt.Errorf("fakeTCALoader: no oracle decl at %s", commitOID)
	}
	return d, nil
}

// oracleStub is the injected currentOracleDigest func, mutable after
// construction so ReconcileBindings tests can flip digest/err between calls
// without rebuilding the whole scenario.
type oracleStub struct {
	digest string
	err    error
}

func (o *oracleStub) fn(evidence.OracleDecl) (string, error) {
	if o.err != nil {
		return "", o.err
	}
	return o.digest, nil
}

// --- fixed scenario values --------------------------------------------------

const tcaApprovalID = "01ARZ3NDEKTSV4RRFFQ69G5FAV" // valid-shaped ULID (26 chars, Crockford base32)

var (
	tcaBaseCommitOID = strings.Repeat("c", 40)
	tcaTestCommitOID = strings.Repeat("d", 40)
	tcaOracleDigest  = "sha256:" + strings.Repeat("e", 64)
)

func validTCAPlan() plan.Plan {
	return plan.Plan{PlanID: "P1", Tasks: []plan.Task{
		{ID: "T1", Title: "T1", TestContract: plan.TestContract{
			Command:         plan.Command{Executable: "sh", Argv: []string{"run.sh"}},
			ExpectedFailure: plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL"},
		}},
	}}
}

func validGate2Record() *gate.ApprovalRecord {
	return &gate.ApprovalRecord{
		Type: "approval_record", SchemaVersion: 2, ApprovalID: tcaApprovalID, Gate: "gate2",
		Subject: "plan:P1", Decision: "approved",
		Approver:  gate.Approver{ID: "tester", Method: "app-local"},
		Reason:    "ok",
		Bindings:  validGate2Bindings(tcaBaseCommitOID),
		CreatedAt: "2024-01-01T00:00:00Z",
	}
}

func validTCAEvidenceRun(kind string) evidence.EvidenceRun {
	return evidence.EvidenceRun{
		EvidenceID: "evid-" + kind, Kind: kind, Source: "local_app",
		BaseCommit: tcaBaseCommitOID, TestCommit: tcaTestCommitOID, OracleSurfaceDigest: tcaOracleDigest,
		Command:         plan.Command{Executable: "sh", Argv: []string{"run.sh"}},
		ExpectedFailure: plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL"},
		Result:          "passed",
		StdoutDigest:    "sha256:" + strings.Repeat("1", 64),
		StderrDigest:    "sha256:" + strings.Repeat("2", 64),
		RunnerVersion:   "v1",
	}
}

func validTCAMutation() evidence.Mutation {
	return evidence.Mutation{MutationID: "mut-1", TaskRef: "P1/T1", Digest: "sha256:" + strings.Repeat("3", 64), CreatedAt: "2024-01-01T00:00:00Z"}
}

// tcaScenario bundles a self-consistent, approval-ready TCA request; each
// field is independently mutable by a test case before calling build().
type tcaScenario struct {
	gate2Rec      *gate.ApprovalRecord
	gate2State    gate.State
	pl            plan.Plan
	redRun        evidence.EvidenceRun
	negRun        evidence.EvidenceRun // MutationDigest filled in by build()
	mutation      evidence.Mutation
	oracle        *oracleStub
	oracleLoadErr error
	getErr        map[string]error
	mutErr        map[string]error
	gateErr       error
	git           plan.GitRunner
}

func newValidTCAScenario() *tcaScenario {
	return &tcaScenario{
		gate2Rec: validGate2Record(), gate2State: gate.Active,
		pl: validTCAPlan(), redRun: validTCAEvidenceRun("expected_red"), negRun: validTCAEvidenceRun("negative_control"),
		mutation: validTCAMutation(), oracle: &oracleStub{digest: tcaOracleDigest}, git: &stubGit{},
	}
}

// build assembles the policy and a matching gate.GateRequest whose bindings
// are freshly digested from the scenario's current field values — so a test
// that mutates redRun/negRun/pl before calling build() exercises BuildDecision's
// semantic checks (not digest verification, which would trivially "pass"
// since the digest is recomputed from the same mutated content).
func (s *tcaScenario) build(t *testing.T) (*TCAPolicy, gate.GateRequest) {
	t.Helper()
	neg := s.negRun
	neg.MutationDigest = s.mutation.Digest

	gate2Digest, err := gate.RecordDigest(*s.gate2Rec)
	if err != nil {
		t.Fatal(err)
	}
	redDigest, err := evidence.EvidenceRunDigest(s.redRun)
	if err != nil {
		t.Fatal(err)
	}
	negDigest, err := evidence.EvidenceRunDigest(neg)
	if err != nil {
		t.Fatal(err)
	}

	ev := &fakeEvidenceStore{
		runs:   map[string]evidence.EvidenceRun{"evid-expected_red": s.redRun, "evid-negative_control": neg},
		muts:   map[string]evidence.Mutation{"mut-1": s.mutation},
		getErr: s.getErr, mutErr: s.mutErr,
	}
	gates := &fakeGateReader{err: s.gateErr, entries: map[string]fakeGateEntry{
		tcaApprovalID: {rec: s.gate2Rec, state: s.gate2State},
	}}
	gate2BaseDigest := bindingDigest(s.gate2Rec.Bindings, "base_commit")
	planCommitOID := gitOID(gate2BaseDigest)
	loader := &fakeTCALoader{
		fakeLoader: &fakeLoader{entries: map[string]loaderEntry{planCommitOID + "|P1": {plan: s.pl}}},
		oracleErr:  s.oracleLoadErr,
		oracle:     map[string]evidence.OracleDecl{planCommitOID: {Version: 1, Patterns: []string{"run.sh"}}},
	}
	p := NewTCAPolicy(ev, gates, loader, s.git, s.oracle.fn).(*TCAPolicy)

	bindings := []gate.Binding{
		{Kind: "gate2_approval", Ref: "approval:" + tcaApprovalID, Digest: gate2Digest},
		{Kind: "base_commit", Ref: "plan_commit", Digest: gate2BaseDigest},
		{Kind: "oracle_surface", Ref: s.redRun.TestCommit, Digest: s.redRun.OracleSurfaceDigest},
		{Kind: "evidence_run", Role: "expected_red", Ref: "evid-expected_red", Digest: redDigest},
		{Kind: "evidence_run", Role: "negative_control", Ref: "evid-negative_control", Digest: negDigest},
		{Kind: "mutation", Ref: "mut-1", Digest: s.mutation.Digest},
	}
	return p, gate.GateRequest{Gate: "test_contract_approval", Subject: "task:P1/T1", Bindings: bindings}
}

func mutateBinding(bs []gate.Binding, kind, role string, mutate func(*gate.Binding)) {
	for i := range bs {
		if bs[i].Kind == kind && bs[i].Role == role {
			mutate(&bs[i])
			return
		}
	}
}

func approvalFromReq(req gate.GateRequest) gate.ApprovalRecord {
	return gate.ApprovalRecord{Gate: req.Gate, Subject: req.Subject, Bindings: req.Bindings}
}

// --- ValidateRequest ---------------------------------------------------

func TestTCAValidateRequestSubjectShape(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)

	for _, subject := range []string{"task:P1", "task:/T1", "task:P1/", "plan:P1", "task:"} {
		req.Subject = subject
		if err := p.ValidateRequest(req); err == nil {
			t.Errorf("subject %q must be rejected as malformed", subject)
		}
	}

	req.Subject = "task:P1/T1"
	if err := p.ValidateRequest(req); err != nil {
		t.Fatalf("well-formed subject must pass binding-schema-valid request: %v", err)
	}
}

func TestTCAValidateRequestBindingsSchema(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)

	t.Run("missing required binding", func(t *testing.T) {
		bad := req
		bad.Bindings = req.Bindings[1:] // drop gate2_approval
		if err := p.ValidateRequest(bad); err == nil || !strings.Contains(err.Error(), "missing required binding") {
			t.Fatalf("missing binding must be rejected, got %v", err)
		}
	})

	t.Run("duplicate (kind,role)", func(t *testing.T) {
		bad := req
		bad.Bindings = append(append([]gate.Binding{}, req.Bindings...), req.Bindings[0])
		if err := p.ValidateRequest(bad); err == nil || !strings.Contains(err.Error(), "duplicate binding") {
			t.Fatalf("duplicate binding must be rejected, got %v", err)
		}
	})

	t.Run("bad digest format", func(t *testing.T) {
		bad := req
		bad.Bindings = append([]gate.Binding{}, req.Bindings...)
		mutateBinding(bad.Bindings, "mutation", "", func(b *gate.Binding) { b.Digest = "not-a-digest" })
		if err := p.ValidateRequest(bad); err == nil || !strings.Contains(err.Error(), "does not match expected pattern") {
			t.Fatalf("bad digest format must be rejected, got %v", err)
		}
	})

	t.Run("gate2_approval ref must be approval:<ULID>", func(t *testing.T) {
		bad := req
		bad.Bindings = append([]gate.Binding{}, req.Bindings...)
		mutateBinding(bad.Bindings, "gate2_approval", "", func(b *gate.Binding) { b.Ref = "not-an-approval-ref" })
		if err := p.ValidateRequest(bad); err == nil || !strings.Contains(err.Error(), "does not match expected pattern") {
			t.Fatalf("malformed gate2_approval ref must be rejected, got %v", err)
		}
	})

	t.Run("oracle_surface ref must be a bare git OID", func(t *testing.T) {
		bad := req
		bad.Bindings = append([]gate.Binding{}, req.Bindings...)
		mutateBinding(bad.Bindings, "oracle_surface", "", func(b *gate.Binding) { b.Ref = "not-an-oid" })
		if err := p.ValidateRequest(bad); err == nil || !strings.Contains(err.Error(), "does not match expected pattern") {
			t.Fatalf("malformed oracle_surface ref must be rejected, got %v", err)
		}
	})

	t.Run("valid request passes", func(t *testing.T) {
		if err := p.ValidateRequest(req); err != nil {
			t.Fatalf("valid TCA request must pass ValidateRequest: %v", err)
		}
	})
}

// --- BuildDecision -------------------------------------------------------

func TestTCABuildDecisionRejectedRequiresEmptyInput(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)

	if meta, err := p.BuildDecision(req, "rejected", gate.DecisionInput{}); err != nil || meta != nil {
		t.Fatalf("empty input on rejected must succeed with nil metadata, got %v %v", meta, err)
	}
	nonEmpty := gate.DecisionInput{RiskSelections: []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}}
	if _, err := p.BuildDecision(req, "rejected", nonEmpty); err == nil {
		t.Fatal("rejected decision with risk selections must be rejected")
	}
}

func TestTCABuildDecisionApprovedHappyPath(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)
	meta, err := p.BuildDecision(req, "approved", gate.DecisionInput{})
	if err != nil {
		t.Fatalf("valid TCA request must approve, got %v", err)
	}
	if meta != nil {
		t.Fatalf("TCA BuildDecision must return nil metadata (no risk_decisions), got %+v", meta)
	}
}

func TestTCABuildDecisionApprovedRejectsRiskSelections(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)
	sel := gate.DecisionInput{RiskSelections: []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}}
	if _, err := p.BuildDecision(req, "approved", sel); err == nil {
		t.Fatal("TCA does not accept risk selections even when approved")
	}
}

// TestTCABuildDecisionConsistencyValidator covers the §3.4 eight-condition
// validator (brief's frozen seven + the ledger's eighth mutation<->task
// check), each as an independent mutation of an otherwise-valid scenario.
func TestTCABuildDecisionConsistencyValidator(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(s *tcaScenario)
		mutateB func(bindings []gate.Binding) // applied after build(), to the assembled request
		wantErr string
	}{
		{
			name:    "two evidence runs' test_commit differ",
			mutate:  func(s *tcaScenario) { s.negRun.TestCommit = strings.Repeat("f", 40) },
			wantErr: "base_commit/test_commit/oracle_surface_digest mismatch",
		},
		{
			name:    "two evidence runs' base_commit differ",
			mutate:  func(s *tcaScenario) { s.negRun.BaseCommit = strings.Repeat("f", 40) },
			wantErr: "base_commit/test_commit/oracle_surface_digest mismatch",
		},
		{
			name:    "two evidence runs' oracle_surface_digest differ",
			mutate:  func(s *tcaScenario) { s.negRun.OracleSurfaceDigest = "sha256:" + strings.Repeat("9", 64) },
			wantErr: "base_commit/test_commit/oracle_surface_digest mismatch",
		},
		{
			// review HIGH finding: both runs agreeing with EACH OTHER is not
			// enough — they must also match gate2_approval's own plan_commit.
			// Otherwise evidence gathered under an older gate2 (stale code
			// snapshot) would still satisfy this check.
			name: "both evidence runs consistent but base_commit != gate2_approval's plan_commit",
			mutate: func(s *tcaScenario) {
				s.redRun.BaseCommit = strings.Repeat("f", 40)
				s.negRun.BaseCommit = strings.Repeat("f", 40)
			},
			wantErr: "does not match gate2_approval",
		},
		{
			name:    "expected_red carries a mutation_digest",
			mutate:  func(s *tcaScenario) { s.redRun.MutationDigest = s.mutation.Digest },
			wantErr: "must not carry a mutation_digest",
		},
		{
			name:    "negative_control result is not passed",
			mutate:  func(s *tcaScenario) { s.negRun.Result = "failed" },
			wantErr: "must have result",
		},
		{
			name:    "expected_red result is not passed",
			mutate:  func(s *tcaScenario) { s.redRun.Result = "failed" },
			wantErr: "must have result",
		},
		{
			name:    "expected_red binding's run kind is wrong",
			mutate:  func(s *tcaScenario) { s.redRun.Kind = "negative_control" },
			wantErr: "expected_red binding's evidence run kind",
		},
		{
			name:    "negative_control binding's run kind is wrong",
			mutate:  func(s *tcaScenario) { s.negRun.Kind = "expected_red" },
			wantErr: "negative_control binding's evidence run kind",
		},
		{
			name:    "descriptor command mismatch vs committed plan",
			mutate:  func(s *tcaScenario) { s.pl.Tasks[0].TestContract.Command.Argv = []string{"other.sh"} },
			wantErr: "command does not match",
		},
		{
			name:    "descriptor expected_failure mismatch vs committed plan",
			mutate:  func(s *tcaScenario) { s.pl.Tasks[0].TestContract.ExpectedFailure.Matcher = "OTHER" },
			wantErr: "expected_failure does not match",
		},
		{
			name:    "committed plan's matcher/test_ids empty (defense in depth)",
			mutate:  func(s *tcaScenario) { s.pl.Tasks[0].TestContract.ExpectedFailure = plan.ExpectedFailure{} },
			wantErr: "empty matcher/test_ids",
		},
		{
			name:    "mutation.task_ref does not match plan_id/task_id",
			mutate:  func(s *tcaScenario) { s.mutation.TaskRef = "P1/T9" },
			wantErr: "task_ref",
		},
		{
			name: "gate2_approval binding digest tampered",
			mutateB: func(bs []gate.Binding) {
				mutateBinding(bs, "gate2_approval", "", func(b *gate.Binding) { b.Digest = fakeSHA })
			},
			wantErr: "gate2_approval binding digest",
		},
		{
			name: "TCA base_commit binding does not match gate2_approval's base_commit",
			mutateB: func(bs []gate.Binding) {
				mutateBinding(bs, "base_commit", "", func(b *gate.Binding) { b.Digest = "git:sha1:" + strings.Repeat("9", 40) })
			},
			wantErr: "base_commit binding does not match",
		},
		{
			name: "oracle_surface.ref != test_commit",
			mutateB: func(bs []gate.Binding) {
				mutateBinding(bs, "oracle_surface", "", func(b *gate.Binding) { b.Ref = strings.Repeat("9", 40) })
			},
			wantErr: "oracle_surface binding ref",
		},
		{
			name: "mutation binding digest does not match registered mutation",
			mutateB: func(bs []gate.Binding) {
				mutateBinding(bs, "mutation", "", func(b *gate.Binding) { b.Digest = fakeSHA })
			},
			wantErr: "mutation binding digest",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newValidTCAScenario()
			if c.mutate != nil {
				c.mutate(s)
			}
			p, req := s.build(t)
			if c.mutateB != nil {
				c.mutateB(req.Bindings)
			}
			if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestTCABuildDecisionGate2NotApproved(t *testing.T) {
	s := newValidTCAScenario()
	s.gate2Rec.Decision = "rejected"
	p, req := s.build(t)
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil || !strings.Contains(err.Error(), "not an approved record") {
		t.Fatalf("gate2 record that is not an approved decision must be rejected, got %v", err)
	}
}

// --- ReconcileBindings -----------------------------------------------------

func TestTCAReconcileBindingsGate2Approval(t *testing.T) {
	s := newValidTCAScenario()
	p, req := s.build(t)
	rec := approvalFromReq(req)

	t.Run("gate2 no longer active -> stale cause", func(t *testing.T) {
		s2 := newValidTCAScenario()
		s2.gate2State = gate.Stale
		p2, req2 := s2.build(t)
		causes, err := p2.ReconcileBindings(approvalFromReq(req2))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "gate2_approval not active") {
			t.Fatalf("want 'gate2_approval not active' stale cause, got %v", causes)
		}
	})

	t.Run("gate2 record content changed (RecordDigest mismatch) -> stale cause", func(t *testing.T) {
		s2 := newValidTCAScenario()
		p2, req2 := s2.build(t)
		// simulate the gate2 record having been amended after TCA bound its
		// digest — Lookup now returns different content for the same id.
		s2.gate2Rec.Reason = "amended after TCA bound its digest"
		causes, err := p2.ReconcileBindings(approvalFromReq(req2))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "gate2_approval changed") {
			t.Fatalf("want 'gate2_approval changed' stale cause, got %v", causes)
		}
	})

	t.Run("GateReader read error fails closed", func(t *testing.T) {
		s2 := newValidTCAScenario()
		s2.gateErr = errors.New("boom")
		p2, req2 := s2.build(t)
		if _, err := p2.ReconcileBindings(approvalFromReq(req2)); err == nil {
			t.Fatal("GateReader read error must fail closed, not report stale")
		}
	})

	t.Run("happy path: no stale causes", func(t *testing.T) {
		causes, err := p.ReconcileBindings(rec)
		if err != nil || len(causes) != 0 {
			t.Fatalf("valid TCA record must reconcile clean, got %v %v", causes, err)
		}
	})
}

func TestTCAReconcileBindingsEvidenceRun(t *testing.T) {
	t.Run("record content changed (EvidenceRunDigest mismatch) -> stale cause", func(t *testing.T) {
		s := newValidTCAScenario()
		p, req := s.build(t)
		rec := approvalFromReq(req)
		// tamper the journal-side record after the digest was bound.
		tampered := s.redRun
		tampered.ObservedFailure = "amended after finalize"
		p.ev.(*fakeEvidenceStore).runs["evid-expected_red"] = tampered

		causes, err := p.ReconcileBindings(rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "evidence_run (expected_red) changed") {
			t.Fatalf("want evidence_run stale cause, got %v", causes)
		}
	})

	t.Run("CAS artifact / journal read error fails closed", func(t *testing.T) {
		s := newValidTCAScenario()
		s.getErr = map[string]error{"evid-negative_control": errors.New("stderr artifact missing")}
		p, req := s.build(t)
		if _, err := p.ReconcileBindings(approvalFromReq(req)); err == nil {
			t.Fatal("evidence store read error must fail closed, not report stale")
		}
	})
}

func TestTCAReconcileBindingsMutation(t *testing.T) {
	t.Run("mutation content changed -> stale cause", func(t *testing.T) {
		s := newValidTCAScenario()
		p, req := s.build(t)
		rec := approvalFromReq(req)
		tampered := s.mutation
		tampered.Digest = "sha256:" + strings.Repeat("8", 64)
		p.ev.(*fakeEvidenceStore).muts["mut-1"] = tampered

		causes, err := p.ReconcileBindings(rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "mutation changed") {
			t.Fatalf("want mutation stale cause, got %v", causes)
		}
	})

	t.Run("mutation CAS/journal read error fails closed", func(t *testing.T) {
		s := newValidTCAScenario()
		s.mutErr = map[string]error{"mut-1": errors.New("cas corrupt")}
		p, req := s.build(t)
		if _, err := p.ReconcileBindings(approvalFromReq(req)); err == nil {
			t.Fatal("mutation read error must fail closed, not report stale")
		}
	})
}

// TestTCAReconcileBindingsOracleSurface covers §3.9/A5: the oracle-surface
// declaration is loaded from the gate2 approval's plan_commit, but its
// digest is recomputed against the CURRENT workspace on every reconcile —
// editing an oracle file after approval must go stale even though
// test_commit's committed tree never changes.
func TestTCAReconcileBindingsOracleSurface(t *testing.T) {
	t.Run("current oracle content changed -> stale cause (A5)", func(t *testing.T) {
		s := newValidTCAScenario()
		p, req := s.build(t)
		rec := approvalFromReq(req)
		s.oracle.digest = "sha256:" + strings.Repeat("7", 64) // worktree edited post-approval

		causes, err := p.ReconcileBindings(rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "oracle_surface changed") {
			t.Fatalf("want oracle_surface stale cause, got %v", causes)
		}
	})

	t.Run("current oracle recompute read error fails closed, not stale", func(t *testing.T) {
		s := newValidTCAScenario()
		p, req := s.build(t)
		rec := approvalFromReq(req)
		s.oracle.err = errors.New("concurrent modification")

		if _, err := p.ReconcileBindings(rec); err == nil {
			t.Fatal("oracle recompute read error must fail closed, not report stale")
		}
	})

	t.Run("oracle decl load-at-plan_commit error fails closed", func(t *testing.T) {
		s := newValidTCAScenario()
		s.oracleLoadErr = errors.New("git show failed")
		p, req := s.build(t)
		if _, err := p.ReconcileBindings(approvalFromReq(req)); err == nil {
			t.Fatal("oracle decl load error must fail closed, not report stale")
		}
	})
}

func TestTCAReconcileBindingsBaseCommit(t *testing.T) {
	g := newTestRepo(t)
	bound := g.oid("HEAD")
	g.commitFile("other.txt", "x") // HEAD moves past the bound commit

	newScenario := func() *tcaScenario {
		s := newValidTCAScenario()
		s.git = g
		s.gate2Rec.Bindings = validGate2Bindings(bound)
		s.redRun.BaseCommit = bound
		s.negRun.BaseCommit = bound
		return s
	}

	t.Run("base_commit does not go stale when HEAD moves forward", func(t *testing.T) {
		s := newScenario()
		p, req := s.build(t)
		causes, err := p.ReconcileBindings(approvalFromReq(req))
		if err != nil || len(causes) != 0 {
			t.Fatalf("HEAD moved past bound commit: expected no stale causes, got %v %v", causes, err)
		}
	})

	t.Run("base_commit stale when the commit no longer exists", func(t *testing.T) {
		s := newScenario()
		missing := strings.Repeat("f", 40)
		s.gate2Rec.Bindings = validGate2Bindings(missing)
		p, req := s.build(t)
		causes, err := p.ReconcileBindings(approvalFromReq(req))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasCause(causes, "base_commit missing") {
			t.Fatalf("want base_commit missing stale cause, got %v", causes)
		}
	})

	t.Run("fatal git error fails closed, not stale", func(t *testing.T) {
		s := newScenario()
		s.git = &stubGit{err: fakeExitError{code: 128}}
		p, req := s.build(t)
		if _, err := p.ReconcileBindings(approvalFromReq(req)); err == nil {
			t.Fatal("fatal git error must fail closed, not report stale")
		}
	})
}

func hasCause(causes []gate.StaleCause, cause string) bool {
	for _, c := range causes {
		if c.Cause == cause {
			return true
		}
	}
	return false
}
