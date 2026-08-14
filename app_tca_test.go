package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

// ---- SubmitTestContract（Task 21：M3a §3.4／§4-5）----
//
// setupTCAEvidence 重用 app_evidence_test.go 的 setupApprovedEvidencePlan 基盤
// （active gate1／gate2、run_test.sh 恆印 "FAIL: TestX" 並 exit 1），跑一組
// expected_red＋negative_control（mutate other.txt，不觸及 oracle surface）
// ——ClassifyExpectedRed／ClassifyNegativeControl 判準相同（matcher.go），兩者
// 都會落在 Result="passed"，恰是 SubmitTestContract 的必要輸入。

func setupTCAEvidence(t *testing.T, a *App, planID string) (planCommit, redID, negID, mutationID string) {
	t.Helper()
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	writeFile(t, filepath.Join(a.workspaceDir, "other.txt"), "unrelated content\n")
	planCommit = setupApprovedEvidencePlan(t, a, planID)
	approvalID := activeApprovalIDFor(t, a, planID)

	var err error
	redID, err = a.RunEvidence(approvalID, planID, "T1", planCommit, "expected_red", "")
	if err != nil {
		t.Fatalf("RunEvidence expected_red: %v", err)
	}

	writeFile(t, filepath.Join(a.workspaceDir, "other.txt"), "unrelated content\nmutated\n")
	patch, err := exec.Command("git", "-C", a.workspaceDir, "diff", "HEAD").Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	runGit(t, a, "checkout", "--", ".")

	mutationID, err = a.RegisterMutation(planID+"/T1", string(patch))
	if err != nil {
		t.Fatalf("RegisterMutation: %v", err)
	}
	negID, err = a.RunEvidence(approvalID, planID, "T1", planCommit, "negative_control", mutationID)
	if err != nil {
		t.Fatalf("RunEvidence negative_control: %v", err)
	}
	return planCommit, redID, negID, mutationID
}

func TestSubmitTestContractApproveHappyPath(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	planCommit, redID, negID, mutationID := setupTCAEvidence(t, a, "P1")

	tcaID, err := a.SubmitTestContract("P1", "T1", planCommit, redID, negID, mutationID)
	if err != nil {
		t.Fatalf("SubmitTestContract: %v", err)
	}
	if tcaID == "" {
		t.Fatal("SubmitTestContract must return a non-empty approval id")
	}

	if err := a.GateDecide(tcaID, "approved", "ok", nil); err != nil {
		t.Fatalf("GateDecide(tca approved): %v", err)
	}
	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(list, tcaID) != "active" {
		t.Fatalf("want tca approval active after approve, got %q", stateOf(list, tcaID))
	}
}

func TestSubmitTestContractWithoutActiveGate2Rejects(t *testing.T) {
	a, ui := newTestAppEvidence(t)
	_ = ui
	if _, err := a.SubmitTestContract("P1", "T1", strings.Repeat("0", 40), "red", "neg", "mut"); err == nil {
		t.Fatal("SubmitTestContract without an active Gate 2 approval for the plan must reject")
	}
}

// TestTCADecideRejectsWhenGate2SupersededBeforeDecide covers the "mutation-
// before-decide" scenario from task-21-brief.md's Step 1 table: a TCA
// request submitted while gate2 is active, but by the time it is decided the
// gate2_approval it anchors to has been superseded (a second gate2 approval
// for the same plan went through in between). PrepareDecision's
// current-binding validation (gate.Service, Task 5) must catch this via
// TCAPolicy.ReconcileBindings — the pending TCA request's own gate2_approval
// binding now points at a no-longer-active record.
func TestTCADecideRejectsWhenGate2SupersededBeforeDecide(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	planCommit, redID, negID, mutationID := setupTCAEvidence(t, a, "P1")

	tcaID, err := a.SubmitTestContract("P1", "T1", planCommit, redID, negID, mutationID)
	if err != nil {
		t.Fatalf("SubmitTestContract: %v", err)
	}

	// Resubmit and approve a second gate2 for the same plan/subject — its
	// SupersessionKey ("gate2|plan:P1") matches the first, so approving it
	// supersedes the gate2 approval the pending TCA request's gate2_approval
	// binding points at (CommitDecision's scoped-supersession, service.go).
	gate2ID, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatalf("resubmit gate2: %v", err)
	}
	sel := []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}
	if err := a.GateDecide(gate2ID, "approved", "reapproved", sel); err != nil {
		t.Fatalf("approve second gate2: %v", err)
	}

	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(list, gate2ID) != "active" {
		t.Fatalf("second gate2 approval must be active, got %q", stateOf(list, gate2ID))
	}

	if err := a.GateDecide(tcaID, "approved", "ok", nil); err == nil {
		t.Fatal("TCA decide must reject once its bound gate2_approval has been superseded (current-binding validation)")
	}
}
