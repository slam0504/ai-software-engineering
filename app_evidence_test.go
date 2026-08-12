package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/evidence"
	"github.com/slam0504/sdlc-workbench/internal/gate"
)

// ---- test fixtures／helpers（鏡射 app_plan_test.go／app_gate_test.go 慣例）----

// newTestAppEvidence：App 綁一個新 git init'd temp workspace，並手動接上
// evidence journal／CAS dir／worktree registry（同 newTestAppGit／
// newTestAppAssist 手動接線 production 欄位的慣例——不呼叫真正的
// a.startup(ctx)，因為多數測試基盤本就不經 startup）。
func newTestAppEvidence(t *testing.T) (*App, *uiCapture) {
	t.Helper()
	a, ui := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")

	dir := filepath.Join(a.workspaceDir, ".workbench", "evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a.evidenceCASDir = filepath.Join(dir, "cas")
	a.evidenceRegistryPath = filepath.Join(dir, "worktrees.jsonl")
	j, err := evidence.OpenJournal(filepath.Join(dir, "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	a.evidenceJournal = j
	return a, ui
}

// evidencePlanYAML：鏡射 app_plan_test.go 的 testPlanYAML，但 test_contract
// 換成一個真的可在 worktree 內執行的 shell script（run_test.sh），供
// RunEvidence 端到端測試使用（testPlanYAML 的 `go test` 命令在裸 temp repo
// 內無法執行）。
func evidencePlanYAML(planID, analysisBase string) string {
	return "" +
		"plan_id: " + planID + "\n" +
		"analysis_base_commit: " + analysisBase + "\n" +
		"spec_manifest: sha256:" + strings.Repeat("a", 64) + "\n" +
		"risk_policy: sha256:" + strings.Repeat("a", 64) + "\n" +
		"tasks:\n" +
		"  - id: T1\n" +
		"    title: Task 1\n" +
		"    scenarios: []\n" +
		"    depends_on: []\n" +
		"    impact:\n" +
		"      contexts: []\n" +
		"      modules: []\n" +
		"    completion: []\n" +
		"    minimum_risk_tier: medium\n" +
		"    planner_risk_tier: medium\n" +
		"    permissions_ref: permissions/T1.yaml\n" +
		"    test_contract:\n" +
		"      command: {executable: sh, argv: [run_test.sh]}\n" +
		"      expected_failure: {test_ids: [TestX], matcher: FAIL}\n"
}

// setupApprovedEvidencePlan：commit spec/glossary.md＋run_test.sh，approve
// gate1，再 commit plan/<planID>.yaml（evidencePlanYAML）＋risk-policy／
// permissions／oracle-surface.yaml（oracle 宣告只涵蓋 run_test.sh），approve
// gate2。回傳核可後的 plan_commit（= gate2 的 base_commit binding）。
// run_test.sh 委由呼叫端先寫好內容再呼叫本函式（不同測試需要不同腳本行為）。
func setupApprovedEvidencePlan(t *testing.T, a *App, planID string) (planCommit string) {
	t.Helper()
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v1")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "baseline")

	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	headOID := revParseHead(t, a)

	writeFile(t, filepath.Join(a.workspaceDir, "plan", planID+".yaml"), evidencePlanYAML(planID, headOID))
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), testRiskPolicyYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", "T1.yaml"), "allow: []\n")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "oracle-surface.yaml"), "version: 1\npatterns: [run_test.sh]\n")
	commitAllPlan(t, a)
	planCommit = revParseHead(t, a)

	gate2ID, err := a.SubmitPlanForApproval(planID)
	if err != nil {
		t.Fatal(err)
	}
	sel := []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}
	if err := a.GateDecide(gate2ID, "approved", "ok", sel); err != nil {
		t.Fatal(err)
	}
	return planCommit
}

func assertNoZombieWorktrees(t *testing.T, root string) {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "worktree", "list").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Errorf("git worktree list has zombies: %s", out)
	}
}

// ---- RunEvidence: happy path（expected_red）＋EmitWorkspace 進度事件 ----

func TestRunEvidenceExpectedRed_AppendsJournalAndEmitsProgress(t *testing.T) {
	a, ui := newTestAppEvidence(t)
	// run_test.sh 必須先於 baseline commit 存在，這樣 gate2 的 plan/ lineage
	// 驗證（analysis_base_commit..plan_commit 限 plan/**）才不會因為它落在
	// plan_commit 這筆 commit 的 diff 內而被拒——它從一開始就在 baseline，
	// 之後只被 plan/ 底下的異動 commit 帶過、內容不變。
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	evidenceID, err := a.RunEvidence("P1", "T1", planCommit, "expected_red", "")
	if err != nil {
		t.Fatalf("RunEvidence: %v", err)
	}
	if evidenceID == "" {
		t.Fatal("RunEvidence must return a non-empty evidence_id")
	}

	run, err := a.EvidenceGet(evidenceID)
	if err != nil {
		t.Fatalf("EvidenceGet: %v", err)
	}
	if run.Result != "passed" {
		t.Fatalf("Result = %q, want passed (observed=%q)", run.Result, run.ObservedFailure)
	}
	if run.BaseCommit != planCommit || run.TestCommit != planCommit {
		t.Fatalf("BaseCommit/TestCommit = %q/%q, want both %q", run.BaseCommit, run.TestCommit, planCommit)
	}

	// 恰一次 finalize：同一 run 再次 append 必須被拒。
	if err := a.evidenceJournal.AppendEvidenceRun(run); !errors.Is(err, evidence.ErrDuplicateEvidenceID) {
		t.Fatalf("re-appending the same evidence_id must reject, got %v", err)
	}

	// EmitWorkspace 進度事件：kind=evidence_run，additive，開始/結束各一。
	events := ui.findEnvKind("evidence_run")
	if len(events) != 2 {
		t.Fatalf("want 2 evidence_run events (started+finished), got %d: %+v", len(events), events)
	}
	if events[0].Scope != "workspace" {
		t.Errorf("evidence_run event scope = %q, want workspace", events[0].Scope)
	}

	assertNoZombieWorktrees(t, a.workspaceDir)
}

// ---- RunEvidence: lineage 混入非 oracle 路徑必須拒絕 ----

func TestRunEvidenceRejectsNonOracleLineage(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	setupApprovedEvidencePlan(t, a, "P1")

	// plan_commit 之後另立一筆 commit，把 oracle 檔（run_test.sh）改名移出
	// oracle 宣告範圍——plan_commit..test_commit 的 lineage 驗證必須拒絕它。
	runGit(t, a, "mv", "run_test.sh", "not_oracle.sh")
	runGit(t, a, "commit", "-m", "rename run_test.sh out of oracle scope")
	testCommit := revParseHead(t, a)

	if _, err := a.RunEvidence("P1", "T1", testCommit, "expected_red", ""); err == nil {
		t.Fatal("RunEvidence must reject a lineage range that renames the oracle file out of scope")
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

func TestRunEvidenceRejectsWithoutActiveGate2(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	if _, err := a.RunEvidence("P1", "T1", strings.Repeat("0", 40), "expected_red", ""); err == nil {
		t.Fatal("RunEvidence without an active Gate 2 approval for the plan must reject")
	}
}

// ---- RegisterMutation ＋ RunEvidence negative_control ----

func TestRegisterMutationAndRunEvidenceNegativeControl(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	writeFile(t, filepath.Join(a.workspaceDir, "other.txt"), "unrelated content\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	// 建一份真的 unified diff：修改一個非 oracle 檔案，diff 之後把 worktree
	// 復原乾淨（鏡射 internal/evidence/runner_test.go 的 buildRenamePatch 手法）。
	writeFile(t, filepath.Join(a.workspaceDir, "other.txt"), "unrelated content\nmutated\n")
	patch, err := exec.Command("git", "-C", a.workspaceDir, "diff", "HEAD").Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	runGit(t, a, "checkout", "--", ".")

	mutationID, err := a.RegisterMutation("P1/T1", string(patch))
	if err != nil {
		t.Fatalf("RegisterMutation: %v", err)
	}
	if mutationID == "" {
		t.Fatal("RegisterMutation must return a non-empty mutation_id")
	}

	evidenceID, err := a.RunEvidence("P1", "T1", planCommit, "negative_control", mutationID)
	if err != nil {
		t.Fatalf("RunEvidence: %v", err)
	}
	run, err := a.EvidenceGet(evidenceID)
	if err != nil {
		t.Fatalf("EvidenceGet: %v", err)
	}
	if run.Kind != "negative_control" {
		t.Errorf("Kind = %q, want negative_control", run.Kind)
	}
	if run.MutationDigest == "" {
		t.Error("MutationDigest must be populated for a negative_control run")
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

// ---- shutdown channel-barrier: in-flight RunEvidence must be reclaimed ----

// TestShutdownReclaimsInFlightRunEvidence：fixture 命令寫入 "started" 檔後長
// 眠；測試輪詢等該檔出現（非 time.Sleep 猜時間）才呼叫 shutdown()，斷言：
// shutdown 在 bounded 時間內返回（reclaimEvidenceRuns 先於 inflight.Wait 完
// 成）、RunEvidence 以 error 收場、journal 沒有半寫入的 finalize、process
// group 零殘留、下次啟動的 CleanupOrphans 能把 worktree 清乾淨。
func TestShutdownReclaimsInFlightRunEvidence(t *testing.T) {
	a, _ := newTestAppEvidence(t)

	markerDir := t.TempDir()
	t.Setenv("TMPDIR", markerDir) // envAllowlist 放行 TMPDIR，指令與 os.TempDir() 都看得到
	const sleepMarker = "271828182"
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"),
		"#!/bin/sh\ntouch \"$TMPDIR/started\"\nsleep "+sleepMarker+"\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	errCh := make(chan error, 1)
	go func() {
		_, err := a.RunEvidence("P1", "T1", planCommit, "expected_red", "")
		errCh <- err
	}()

	startedPath := filepath.Join(markerDir, "started")
	waitDeadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("timed out waiting for the fixture command's started marker file")
		}
		time.Sleep(20 * time.Millisecond)
	}

	shutdownDone := make(chan struct{})
	shutdownStart := time.Now()
	go func() { a.shutdown(context.Background()); close(shutdownDone) }()
	select {
	case <-shutdownDone:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown stalled on an in-flight RunEvidence")
	}
	if elapsed := time.Since(shutdownStart); elapsed > 20*time.Second {
		t.Fatalf("shutdown took %s, want bounded (reclaimEvidenceRuns before inflight.Wait)", elapsed)
	}

	runErr := <-errCh
	if runErr == nil {
		t.Fatal("RunEvidence must end in error once shutdown cancels its context")
	}

	a.evidenceMu.Lock()
	nActive := len(a.evidenceActive)
	a.evidenceMu.Unlock()
	if nActive != 0 {
		t.Fatalf("active-run registry must be empty after reclaim, got %d", nActive)
	}

	// 無 finalize 半寫入：evidence journal 檔案完全沒有任何一行。
	journalPath := filepath.Join(a.workspaceDir, ".workbench", "evidence", "evidence.jsonl")
	data, rerr := os.ReadFile(journalPath)
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatalf("read evidence journal: %v", rerr)
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("evidence journal must have no entries for a canceled run, got: %s", data)
	}

	// process group 零殘留：長眠的子行程必須已被收乾淨。
	residueDeadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("pgrep", "-f", "sleep "+sleepMarker).Output()
		if len(strings.TrimSpace(string(out))) == 0 {
			break
		}
		if time.Now().After(residueDeadline) {
			t.Fatalf("leftover process still running after shutdown: %s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 下次啟動：CleanupOrphans 必須把任何殘留 worktree 收乾淨（冪等——即使
	// evidence.Run 自己的 defer 早已清過，這裡再跑一次也不該出錯或留東西）。
	if err := evidence.CleanupOrphans(a.workspaceDir, a.evidenceRegistryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}
