package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// ---- test fixtures／helpers（鏡射 app_gate_test.go／app_assist_test.go 慣例）----

// commitAllPlan：納管 plan/ 全部異動 add＋commit（測試用；production 走
// Preview/ConfirmPlanCommit 兩階段流程，未在此重造，同 commitAll 之於 spec/）。
func commitAllPlan(t *testing.T, a *App) {
	t.Helper()
	runGit(t, a, "add", "-A", "--", "plan/")
	runGit(t, a, "commit", "-m", "plan update")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func revParseHead(t *testing.T, a *App) string {
	t.Helper()
	out, err := exec.Command("git", "-C", a.workspaceDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

const testRiskPolicyYAML = "version: 1\ndefault_tier: medium\nrules: []\n"

// testPlanYAML builds a single-task plan document that passes plan.Validate
// under testRiskPolicyYAML (no rules -> ComputeMinimum always "medium").
func testPlanYAML(planID, analysisBase string, scenarios []string) string {
	scenList := "[]"
	if len(scenarios) > 0 {
		scenList = "[" + strings.Join(scenarios, ", ") + "]"
	}
	return "" +
		"plan_id: " + planID + "\n" +
		"analysis_base_commit: " + analysisBase + "\n" +
		"spec_manifest: sha256:" + strings.Repeat("a", 64) + "\n" +
		"risk_policy: sha256:" + strings.Repeat("a", 64) + "\n" +
		"tasks:\n" +
		"  - id: T1\n" +
		"    title: Task 1\n" +
		"    scenarios: " + scenList + "\n" +
		"    depends_on: []\n" +
		"    impact:\n" +
		"      contexts: []\n" +
		"      modules: []\n" +
		"    completion: []\n" +
		"    minimum_risk_tier: medium\n" +
		"    planner_risk_tier: medium\n" +
		"    permissions_ref: permissions/T1.yaml\n" +
		"    test_contract:\n" +
		"      command: {executable: go, argv: [test]}\n" +
		"      expected_failure: {test_ids: [T1], matcher: FAIL}\n"
}

func writePlanFixture(t *testing.T, a *App, planID, analysisBase string, scenarios []string) {
	t.Helper()
	writeFile(t, filepath.Join(a.workspaceDir, "plan", planID+".yaml"), testPlanYAML(planID, analysisBase, scenarios))
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), testRiskPolicyYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", "T1.yaml"), "allow: []\n")
}

// setupApprovedGate1AndPlan: commit spec/glossary.md（＋選填 feature 檔），approve
// gate1，再 commit 一份合法 plan（analysis_base_commit＝gate1 之 base commit）
// 與其 risk-policy.yaml／permissions/T1.yaml。回傳 gate1 approval id。
func setupApprovedGate1AndPlan(t *testing.T, a *App, planID string, featureContent string, scenarios []string) string {
	t.Helper()
	if featureContent != "" {
		if _, err := a.SpecWrite("spec/features/x.feature", featureContent, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.SpecWrite("spec/glossary.md", "term v1", ""); err != nil {
		t.Fatal(err)
	}
	commitAll(t, a)
	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	headOID := revParseHead(t, a)
	writePlanFixture(t, a, planID, headOID, scenarios)
	commitAllPlan(t, a)
	return gate1ID
}

func findGateEntry(list []GateEntryDTO, id string) *GateEntryDTO {
	for i := range list {
		if list[i].ApprovalID == id {
			return &list[i]
		}
	}
	return nil
}

// ---- appGitRunner：ExitError chain（task-12 brief 明確警示）----

func TestAppGitRunnerPreservesExitErrorChain(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init").Run(); err != nil {
		t.Fatal(err)
	}
	r := appGitRunner{root: dir}
	_, err := r.Git("rev-parse", "--verify", "--quiet", strings.Repeat("f", 40)+"^{commit}")
	if err == nil {
		t.Fatal("rev-parse of a nonexistent oid must fail")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("appGitRunner.Git must preserve *exec.ExitError in the error chain, got %v (%T)", err, err)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("want exit 1 for a missing commit, got %d", ee.ExitCode())
	}
}

// ---- parseScenarioTags（pure function）----

func TestParseScenarioTagsExtractsTagAboveScenario(t *testing.T) {
	content := "Feature: X\n\n" +
		"  @E1\n  Scenario: A\n    Given x\n\n" +
		"  Scenario: B (no tag)\n    Given y\n\n" +
		"  @E2 @wip\n  Scenario Outline: C\n    Given z\n"
	ids := parseScenarioTags(content)
	want := []string{"E1", "E2", "wip"}
	if len(ids) != len(want) {
		t.Fatalf("want %v, got %v", want, ids)
	}
	for _, k := range want {
		if !ids[k] {
			t.Fatalf("missing id %q in %v", k, ids)
		}
	}
	if ids["B (no tag)"] {
		t.Fatal("an untagged scenario must not contribute an id")
	}
}

// ---- PlanWrite／PlanRead（鏡射 SpecWrite／SpecRead 基本行為）----

func TestPlanWriteAndReadRoundTrip(t *testing.T) {
	a, _ := newTestApp(t)
	d, err := a.PlanWrite("plan/P1.yaml", "plan_id: P1\n", "")
	if err != nil {
		t.Fatal(err)
	}
	pf, err := a.PlanRead("plan/P1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if pf.Digest != d {
		t.Fatal("read digest must match write digest")
	}
}

func TestPlanWriteConflictOnStaleExpectedDigest(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.PlanWrite("plan/P1.yaml", "v1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PlanWrite("plan/P1.yaml", "v2", "sha256:wrong"); !errors.Is(err, ErrPlanWriteConflict) {
		t.Fatalf("stale expected_digest must conflict: %v", err)
	}
}

func TestPlanWriteRejectsOutOfScope(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.PlanWrite("app.go", "x", ""); err == nil {
		t.Fatal("out-of-scope write must reject")
	}
}

// ---- Preview/ConfirmPlanCommit：AnalysisBase binding／staleness ----

func TestPreviewAndConfirmPlanCommitBindsAnalysisBase(t *testing.T) {
	a := newTestAppGit(t)
	writeFile(t, filepath.Join(a.workspaceDir, "README.md"), "x")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "c0")
	headOID := revParseHead(t, a)

	writePlanFixture(t, a, "P1", headOID, nil)

	preview, err := a.PreviewPlanCommit()
	if err != nil {
		t.Fatal(err)
	}
	if preview.Token.AnalysisBase != headOID {
		t.Fatalf("token.AnalysisBase must equal worktree plan's analysis_base_commit, want %q got %q",
			headOID, preview.Token.AnalysisBase)
	}
	if err := a.ConfirmPlanCommit(preview.Token, "plan: add P1"); err != nil {
		t.Fatal(err)
	}

	// 拿新的 preview token，但在 Confirm 前把 worktree plan 的 analysis_base_commit
	// 改掉——token 與目前 worktree plan 不符，須回 ErrCommitStale。
	preview2, err := a.PreviewPlanCommit()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), testPlanYAML("P1", strings.Repeat("9", 40), nil))
	if err := a.ConfirmPlanCommit(preview2.Token, "plan: tweak"); !errors.Is(err, spec.ErrCommitStale) {
		t.Fatalf("changed analysis_base_commit before confirm must yield ErrCommitStale, got %v", err)
	}
}

func TestPreviewPlanCommitRejectsMissingAnalysisBase(t *testing.T) {
	a := newTestAppGit(t)
	// plan/ 底下沒有任何可解析為 plan.Plan 的檔案。
	if _, err := a.PreviewPlanCommit(); err == nil {
		t.Fatal("preview with no worktree plan document must reject")
	}
}

// ---- SubmitPlanForApproval：committed context 閉環（brief Step 1）----

func TestSubmitPlanForApprovalRejectsWithoutActiveGate1(t *testing.T) {
	a := newTestAppGit(t)
	writePlanFixture(t, a, "P1", strings.Repeat("0", 40), nil)
	commitAllPlan(t, a)

	_, err := a.SubmitPlanForApproval("P1")
	if err == nil || !strings.Contains(err.Error(), "Gate 1") {
		t.Fatalf("without active Gate 1 must reject, got %v", err)
	}
}

func TestSubmitPlanForApprovalRejectsDirtyPlanTree(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", nil)

	// 修改 plan 檔但不 commit：plan scope dirty。
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"),
		testPlanYAML("P1", revParseHead(t, a), nil)+"# dirty\n")

	if _, err := a.SubmitPlanForApproval("P1"); err == nil {
		t.Fatal("dirty plan tree must reject submission")
	}
}

func TestSubmitPlanForApprovalRejectsPlanIDMismatch(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", nil)

	// plan/P1.yaml（檔名派生 ID＝P1）內容的 plan_id 欄位改成 P2——
	// committed plan 與檔名派生 ID 不符，須拒核。
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"),
		testPlanYAML("P2", revParseHead(t, a), nil))
	commitAllPlan(t, a)

	if _, err := a.SubmitPlanForApproval("P1"); err == nil || !strings.Contains(err.Error(), "plan_id") {
		t.Fatalf("plan_id/filename mismatch must reject submission, got %v", err)
	}
}

func TestSubmitPlanForApprovalSucceeds(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", nil)

	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	wantSpecManifest, _, ok := activeGate1Binding(list)
	if !ok || wantSpecManifest == "" {
		t.Fatalf("expected an active gate1 entry with a spec_manifest binding, got ok=%v digest=%q", ok, wantSpecManifest)
	}

	id, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	list, err = a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	found := findGateEntry(list, id)
	if found == nil {
		t.Fatal("submitted gate2 entry not found in GateList")
	}
	if found.Gate != "gate2" || found.Subject != "plan:P1" || found.State != string(gate.Pending) {
		t.Fatalf("unexpected gate2 entry: %+v", found)
	}
	if got := bindingDigest(found.Bindings, "spec_manifest"); got != wantSpecManifest {
		t.Fatalf("gate2 spec_manifest binding must equal Gate 1's binding, want %q got %q", wantSpecManifest, got)
	}
}

func TestSubmitPlanForApprovalIncludesScenarioReferencedByTag(t *testing.T) {
	a := newTestAppGit(t)
	feature := "Feature: X\n\n  @E1\n  Scenario: does the thing\n    Given a thing\n"
	setupApprovedGate1AndPlan(t, a, "P1", feature, []string{"E1"})

	if _, err := a.SubmitPlanForApproval("P1"); err != nil {
		t.Fatalf("task referencing a tagged scenario must be accepted, got %v", err)
	}
}

func TestSubmitPlanForApprovalRejectsUnknownScenario(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", []string{"E9"}) // E9 未在任何 committed feature 宣告

	if _, err := a.SubmitPlanForApproval("P1"); err == nil {
		t.Fatal("task referencing an unknown/untagged scenario must be rejected")
	}
}

// ---- GateDecisionContext：committed 而非 worktree ----

func TestGateDecisionContextUsesCommittedPlanNotWorktree(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", nil)

	id, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatal(err)
	}

	// 送核後修改 worktree plan（不 commit，甚至不是合法 YAML）——
	// GateDecisionContext 完全不讀 worktree，這裡的破壞不得影響回傳值。
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), "not: even a valid plan document\n")

	ctx, err := a.GateDecisionContext(id)
	if err != nil {
		t.Fatalf("must not depend on worktree plan, got err: %v", err)
	}
	if len(ctx.Tasks) != 1 || ctx.Tasks[0].TaskID != "T1" || ctx.Tasks[0].Title != "Task 1" ||
		ctx.Tasks[0].MinimumRiskTier != "medium" || ctx.Tasks[0].PlannerRiskTier != "medium" {
		t.Fatalf("must reflect the committed plan bound at submit time, got %+v", ctx.Tasks)
	}
}

// ---- mutation-before-decide（Gate 2）：Task 5 的 current-binding validation ----

func TestGateDecideRejectsMutationBeforeDecide(t *testing.T) {
	a := newTestAppGit(t)
	setupApprovedGate1AndPlan(t, a, "P1", "", nil)

	id, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatal(err)
	}

	// 送核後、核可前：commit 一份新版 plan（plan manifest digest 改變）。
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"),
		testPlanYAML("P1", revParseHead(t, a), nil)+"# revised after submit\n")
	commitAllPlan(t, a)

	sel := []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}
	if err := a.GateDecide(id, "approved", "ok", sel); err == nil {
		t.Fatal("plan mutated after submit but before decide must reject GateDecide (current-binding validation)")
	}
}
