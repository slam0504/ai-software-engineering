package gatepolicy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// --- fakes ---------------------------------------------------------------

type loaderEntry struct {
	plan plan.Plan
	pol  plan.RiskPolicy
}

// fakeLoader is a PlanLoader test double keyed by "commitOID|planID", plus
// an optional forced error to simulate a read failure.
type fakeLoader struct {
	entries map[string]loaderEntry
	err     error
}

func (f *fakeLoader) LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error) {
	if f.err != nil {
		return plan.Plan{}, plan.RiskPolicy{}, f.err
	}
	e, ok := f.entries[commitOID+"|"+planID]
	if !ok {
		return plan.Plan{}, plan.RiskPolicy{}, fmt.Errorf("fakeLoader: no plan at %s/%s", commitOID, planID)
	}
	return e.plan, e.pol, nil
}

// nopGit is a plan.GitRunner stub for tests that must not invoke git at
// all (e.g. BuildDecision, which never runs lineage or existence checks) —
// any call fails loudly rather than silently succeeding.
type nopGit struct{}

func (nopGit) Git(args ...string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected git call: %v", args)
}

func failFn() (string, error) { return "", fmt.Errorf("unexpected current* call") }

// stubGit is a plan.GitRunner that always returns err, for exercising
// ReconcileBindings' base_commit failure-classification branches without
// needing a real git process to exit non-zero for a specific reason.
type stubGit struct{ err error }

func (s *stubGit) Git(args ...string) ([]byte, error) { return nil, s.err }

// fakeExitError implements the exitCoder shape (ExitCode() int) that
// *exec.ExitError satisfies, so tests can simulate a specific process exit
// code without shelling out.
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

// --- real git repo helper (mirrors internal/plan/lineage_test.go) --------

type testGitRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testGitRepo {
	t.Helper()
	dir := t.TempDir()
	g := &testGitRepo{t: t, dir: dir}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		g.run(args...)
	}
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	g.run("add", "-A")
	g.run("commit", "-m", "C0: code baseline")
	return g
}

func (g *testGitRepo) run(args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %s", args, out)
	}
}

func (g *testGitRepo) Git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	return cmd.Output()
}

func (g *testGitRepo) oid(rev string) string {
	g.t.Helper()
	out, err := g.Git("rev-parse", rev)
	if err != nil {
		g.t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func (g *testGitRepo) commitFile(path, content string) {
	g.t.Helper()
	full := filepath.Join(g.dir, filepath.FromSlash(path))
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		g.t.Fatalf("write %s: %v", path, err)
	}
	g.run("add", "-A")
	g.run("commit", "-m", "commit "+path)
}

// --- ValidateRequest -------------------------------------------------------

const (
	fakeSHA = "sha256:" + "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
)

func validGate2Bindings(baseCommitOID string) []gate.Binding {
	return []gate.Binding{
		{Kind: "spec_manifest", Digest: fakeSHA},
		{Kind: "plan", Digest: fakeSHA},
		{Kind: "base_commit", Digest: "git:sha1:" + baseCommitOID},
		{Kind: "risk_policy", Digest: fakeSHA},
		{Kind: "permission_manifest", Digest: fakeSHA},
	}
}

func TestGate2ValidateRequestBindingsSchema(t *testing.T) {
	g := newTestRepo(t)
	oid := g.oid("HEAD")
	loader := &fakeLoader{err: errors.New("must not be called: binding schema check happens first")}
	p := NewGate2Policy(loader, g, failFn, failFn, failFn, failFn)

	// missing a required binding
	bad := validGate2Bindings(oid)[1:] // drop spec_manifest
	req := gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: bad}
	if err := p.ValidateRequest(req); err == nil || !strings.Contains(err.Error(), "missing required binding") {
		t.Fatalf("missing binding must be rejected, got %v", err)
	}

	// duplicate (kind, role)
	dup := append(validGate2Bindings(oid), gate.Binding{Kind: "spec_manifest", Digest: fakeSHA})
	req = gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: dup}
	if err := p.ValidateRequest(req); err == nil || !strings.Contains(err.Error(), "duplicate binding") {
		t.Fatalf("duplicate binding must be rejected, got %v", err)
	}

	// bad digest format
	badDigest := validGate2Bindings(oid)
	badDigest[0].Digest = "not-a-digest"
	req = gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: badDigest}
	if err := p.ValidateRequest(req); err == nil || !strings.Contains(err.Error(), "does not match expected pattern") {
		t.Fatalf("bad digest format must be rejected, got %v", err)
	}
}

func TestGate2ValidateRequestSubjectPrefix(t *testing.T) {
	g := newTestRepo(t)
	oid := g.oid("HEAD")
	loader := &fakeLoader{err: errors.New("must not be called: subject check happens first")}
	p := NewGate2Policy(loader, g, failFn, failFn, failFn, failFn)

	req := gate.GateRequest{Gate: "gate2", Subject: "workspace", Bindings: validGate2Bindings(oid)}
	if err := p.ValidateRequest(req); err == nil || !strings.Contains(err.Error(), `prefix "plan:"`) {
		t.Fatalf("non plan: subject must be rejected, got %v", err)
	}

	req = gate.GateRequest{Gate: "gate2", Subject: "plan:", Bindings: validGate2Bindings(oid)}
	if err := p.ValidateRequest(req); err == nil {
		t.Fatal("empty plan id must be rejected")
	}
}

// TestGate2ValidateRequestLineage covers §3.0: analysis_base_commit must be
// an ancestor of plan_commit (the base_commit binding), and the range
// between them may only touch plan/**.
func TestGate2ValidateRequestLineage(t *testing.T) {
	g := newTestRepo(t) // C0: code baseline (main.go)
	c0 := g.oid("HEAD")
	g.commitFile("plan/P1.yaml", "v1") // C1: plan-only change
	c1 := g.oid("HEAD")
	g.commitFile("main.go", "x") // C2: mixes in a code change
	c2 := g.oid("HEAD")

	plan1 := plan.Plan{PlanID: "P1", AnalysisBaseCommit: c0, Tasks: []plan.Task{
		{ID: "T1", Title: "T1", MinimumRiskTier: "medium", PlannerRiskTier: "medium"},
	}}
	loader := &fakeLoader{entries: map[string]loaderEntry{
		c1 + "|P1": {plan: plan1},
		c2 + "|P1": {plan: plan1}, // same committed plan content, but bound to the contaminated commit
	}}
	p := NewGate2Policy(loader, g, failFn, failFn, failFn, failFn)

	req := gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: validGate2Bindings(c1)}
	if err := p.ValidateRequest(req); err != nil {
		t.Fatalf("plan-only lineage range must pass: %v", err)
	}

	req = gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: validGate2Bindings(c2)}
	if err := p.ValidateRequest(req); err == nil {
		t.Fatal("lineage range with a non-plan change must be rejected") // §3.0
	}
}

// --- BuildDecision ---------------------------------------------------------

func planWithTasks(tasks ...plan.Task) plan.Plan {
	return plan.Plan{PlanID: "P1", Tasks: tasks}
}

func riskTask(id, minimum, planner string) plan.Task {
	return plan.Task{ID: id, Title: id, MinimumRiskTier: minimum, PlannerRiskTier: planner}
}

func newBuildDecisionPolicy(pl plan.Plan) gate.GatePolicy {
	loader := &fakeLoader{entries: map[string]loaderEntry{
		"C1|P1": {plan: pl, pol: plan.RiskPolicy{Version: 1, DefaultTier: "medium"}}, // no rules -> ComputeMinimum always "medium"
	}}
	return NewGate2Policy(loader, nopGit{}, failFn, failFn, failFn, failFn)
}

// newBuildDecisionPolicyWithPolicy is newBuildDecisionPolicy generalized to
// an explicit RiskPolicy, for cases that need a non-default recomputation
// (a matching rule, or a policy that bypasses plan.ParseRiskPolicy's
// load-time tier validation — see the "unknown tier" case below).
func newBuildDecisionPolicyWithPolicy(pl plan.Plan, pol plan.RiskPolicy) gate.GatePolicy {
	loader := &fakeLoader{entries: map[string]loaderEntry{
		"C1|P1": {plan: pl, pol: pol},
	}}
	return NewGate2Policy(loader, nopGit{}, failFn, failFn, failFn, failFn)
}

// TestGate2BuildDecisionRejectionPaths covers BuildDecision's three
// task-level rejection checks (gate2.go:151-167), each independent of the
// happy-path/malformed-input cases already covered by
// TestGate2BuildDecisionApproved:
//   - recomputed minimum_risk_tier (via a matching RiskPolicy rule) diverges
//     from the committed plan's minimum_risk_tier
//   - planner_risk_tier below the (matching) minimum_risk_tier
//   - an unknown risk tier reaching BuildDecision's own tierOrder lookup
//     (constructed directly rather than via plan.ParseRiskPolicy, which
//     already rejects unknown tiers at load time — this exercises gate2's
//     own defense-in-depth check)
func TestGate2BuildDecisionRejectionPaths(t *testing.T) {
	cases := []struct {
		name    string
		pl      plan.Plan
		pol     plan.RiskPolicy
		sel     []gate.RiskSelection
		wantErr string
	}{
		{
			name: "rule recomputes high but committed plan says medium",
			pl: func() plan.Plan {
				task := riskTask("T1", "medium", "high")
				task.Impact.Contexts = []string{"gate"}
				return planWithTasks(task)
			}(),
			pol: func() plan.RiskPolicy {
				pol, err := plan.ParseRiskPolicy([]byte(
					"version: 1\ndefault_tier: medium\nrules:\n  - match:\n      contexts: [gate]\n    tier: high\n"))
				if err != nil {
					t.Fatal(err)
				}
				return pol
			}(),
			sel:     []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "high"}},
			wantErr: "recomputed minimum_risk_tier",
		},
		{
			name: "planner_risk_tier below recomputed minimum",
			pl:   planWithTasks(riskTask("T1", "medium", "low")),
			pol:  plan.RiskPolicy{Version: 1, DefaultTier: "medium"}, // no rules -> recomputed "medium" matches committed
			sel:  []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}},
			// planner "low" < recomputed minimum "medium"
			wantErr: "planner_risk_tier",
		},
		{
			name: "unknown risk tier",
			pl:   planWithTasks(riskTask("T1", "critical", "critical")),
			// constructed directly (not parsed) so an out-of-band tier can
			// reach BuildDecision's tierOrder lookup: recomputed default
			// "critical" matches the committed minimum "critical", so the
			// mismatch check passes and BuildDecision itself must reject it.
			pol:     plan.RiskPolicy{Version: 1, DefaultTier: "critical"},
			sel:     []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "critical"}},
			wantErr: "unknown minimum_risk_tier",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newBuildDecisionPolicyWithPolicy(c.pl, c.pol)
			req := gate2Req()
			if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{RiskSelections: c.sel}); err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("expected error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func gate2Req() gate.GateRequest {
	return gate.GateRequest{Gate: "gate2", Subject: "plan:P1", Bindings: validGate2Bindings("C1")}
}

func TestGate2BuildDecisionRejectedRequiresEmptyInput(t *testing.T) {
	p := newBuildDecisionPolicy(planWithTasks(riskTask("T1", "medium", "medium")))
	req := gate2Req()

	if meta, err := p.BuildDecision(req, "rejected", gate.DecisionInput{}); err != nil || meta != nil {
		t.Fatalf("empty input on rejected must succeed with nil metadata, got %v %v", meta, err)
	}

	nonEmpty := gate.DecisionInput{RiskSelections: []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}}
	if _, err := p.BuildDecision(req, "rejected", nonEmpty); err == nil {
		t.Fatal("rejected decision with risk selections must be rejected")
	}
}

func TestGate2BuildDecisionApproved(t *testing.T) {
	// Deliberately out of task_id order (T2 before T1): BuildDecision must
	// sort its output by task_id rather than relying on the committed
	// plan's task order, so the happy-path assertion below fails if the
	// sort.Slice call in BuildDecision is ever dropped.
	pl := planWithTasks(
		riskTask("T2", "medium", "high"),
		riskTask("T1", "medium", "medium"),
	)
	req := gate2Req()

	cases := []struct {
		name    string
		sel     []gate.RiskSelection
		wantErr string // substring; empty means expect success
	}{
		{
			name: "task set missing an entry",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "medium"},
			},
			wantErr: "missing risk selection",
		},
		{
			name: "task set has an extra unknown task id",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "medium"},
				{TaskID: "T2", SelectedRiskTier: "high"},
				{TaskID: "T9", SelectedRiskTier: "medium"},
			},
			wantErr: "unknown task ids",
		},
		{
			name: "task set has a duplicate task id",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "medium"},
				{TaskID: "T1", SelectedRiskTier: "medium"},
				{TaskID: "T2", SelectedRiskTier: "high"},
			},
			wantErr: "duplicate risk selection",
		},
		{
			name: "selected below planner without override reason",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "medium"},
				{TaskID: "T2", SelectedRiskTier: "medium"}, // < planner "high", no reason
			},
			wantErr: "requires override_reason",
		},
		{
			name: "selected below minimum",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "low"}, // < minimum "medium"
				{TaskID: "T2", SelectedRiskTier: "high"},
			},
			wantErr: "below minimum_risk_tier",
		},
		{
			name: "happy path: selected below planner with reason is accepted",
			sel: []gate.RiskSelection{
				{TaskID: "T1", SelectedRiskTier: "medium"},
				{TaskID: "T2", SelectedRiskTier: "medium", OverrideReason: "risk assessed lower after review"},
			},
			wantErr: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newBuildDecisionPolicy(pl)
			meta, err := p.BuildDecision(req, "approved", gate.DecisionInput{RiskSelections: c.sel})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				want := &gate.Metadata{RiskDecisions: []gate.RiskDecision{
					{TaskID: "T1", MinimumRiskTier: "medium", PlannerRiskTier: "medium", SelectedRiskTier: "medium"},
					{TaskID: "T2", MinimumRiskTier: "medium", PlannerRiskTier: "high", SelectedRiskTier: "medium", OverrideReason: "risk assessed lower after review"},
				}}
				if len(meta.RiskDecisions) != 2 || meta.RiskDecisions[0] != want.RiskDecisions[0] || meta.RiskDecisions[1] != want.RiskDecisions[1] {
					t.Fatalf("unexpected metadata: %+v", meta)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("expected error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// --- ReconcileBindings -------------------------------------------------

func TestGate2ReconcileBindings(t *testing.T) {
	g := newTestRepo(t)
	bound := g.oid("HEAD")
	g.commitFile("other.txt", "x") // HEAD moves past the bound commit

	current := map[string]string{
		"spec_manifest":       fakeSHA,
		"plan":                fakeSHA,
		"risk_policy":         fakeSHA,
		"permission_manifest": fakeSHA,
	}
	currentFn := func(kind string) func() (string, error) {
		return func() (string, error) { return current[kind], nil }
	}
	newPolicy := func() gate.GatePolicy {
		return NewGate2Policy(&fakeLoader{}, g,
			currentFn("plan"), currentFn("spec_manifest"), currentFn("risk_policy"), currentFn("permission_manifest"))
	}
	rec := gate.ApprovalRecord{Gate: "gate2", Subject: "plan:P1", Bindings: validGate2Bindings(bound)}

	t.Run("base_commit does not go stale when HEAD moves forward", func(t *testing.T) {
		causes, err := newPolicy().ReconcileBindings(rec)
		if err != nil || len(causes) != 0 {
			t.Fatalf("all current, HEAD moved past bound commit: expected no stale causes, got %v %v", causes, err)
		}
	})

	t.Run("base_commit stale when the commit no longer exists", func(t *testing.T) {
		missing := strings.Repeat("f", 40)
		missingRec := rec
		missingRec.Bindings = validGate2Bindings(missing)
		causes, err := newPolicy().ReconcileBindings(missingRec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var got *gate.StaleCause
		for i, c := range causes {
			if c.Cause == "base_commit missing" {
				got = &causes[i]
			}
		}
		if got == nil {
			t.Fatalf("missing commit must report base_commit missing, got %v", causes)
		}
		if got.EvidenceRef != missing {
			t.Fatalf("stale cause EvidenceRef must be the missing oid, got %q", got.EvidenceRef)
		}
	})

	t.Run("base_commit reconcile fails closed on a fatal git error (exit 128), not stale", func(t *testing.T) {
		// exit 128 is what `git rev-parse --verify --quiet` returns for
		// fatal/unreachable-repo errors (verified against a real git repo:
		// `git -C <non-repo-dir> rev-parse --verify --quiet <sha>^{commit}`
		// exits 128), as opposed to exit 1 for "commit not found" — the
		// same split VerifyLineage relies on for merge-base --is-ancestor.
		fatal := &stubGit{err: fakeExitError{code: 128}}
		p := NewGate2Policy(&fakeLoader{}, fatal,
			currentFn("plan"), currentFn("spec_manifest"), currentFn("risk_policy"), currentFn("permission_manifest"))
		causes, err := p.ReconcileBindings(rec)
		if err == nil {
			t.Fatalf("fatal git error must fail closed, not report stale, got causes=%v", causes)
		}
	})

	t.Run("base_commit reconcile fails closed on a non-ExitError failure", func(t *testing.T) {
		notExitErr := &stubGit{err: errors.New("network unreachable")}
		p := NewGate2Policy(&fakeLoader{}, notExitErr,
			currentFn("plan"), currentFn("spec_manifest"), currentFn("risk_policy"), currentFn("permission_manifest"))
		if _, err := p.ReconcileBindings(rec); err == nil {
			t.Fatal("non-ExitError git failure must fail closed, not report stale")
		}
	})

	t.Run("plan manifest changed", func(t *testing.T) {
		save := current["plan"]
		current["plan"] = "sha256:" + strings.Repeat("b", 64)
		defer func() { current["plan"] = save }()
		causes, err := newPolicy().ReconcileBindings(rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		found := false
		for _, c := range causes {
			if c.Cause == "plan changed" {
				found = true
			}
		}
		if !found {
			t.Fatalf("changed plan manifest must report plan changed, got %v", causes)
		}
	})

	t.Run("current* read error fails closed", func(t *testing.T) {
		p := NewGate2Policy(&fakeLoader{}, g,
			currentFn("plan"), func() (string, error) { return "", errors.New("boom") },
			currentFn("risk_policy"), currentFn("permission_manifest"))
		if _, err := p.ReconcileBindings(rec); err == nil {
			t.Fatal("current* read error must fail closed, not report stale") // §3.9
		}
	})
}
