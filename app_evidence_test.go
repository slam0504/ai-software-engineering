package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/evidence"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/plan"
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

	dir := filepath.Join(a.stateDir, "evidence")
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

// activeApprovalIDFor：測試 helper——取得 RunEvidence CAS 現在信任的權威
// active Gate 2 approval_id（M3a.1 T8，§3.3.2），鏡射 app.go
// activeGate2ApprovalID 同一 subject/gate/state 篩選條件。多數呼叫端已核可
// 剛好一次，直接用這個查詢當「按下當下讀到的 expected」即可；需要模擬換版
// 競態的測試改用 runEvidenceCASHook（見下方 channel-barrier 測試）。
func activeApprovalIDFor(t *testing.T, a *App, planID string) string {
	t.Helper()
	entries, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	id, ok := activeGate2ApprovalID(entries, planID)
	if !ok {
		t.Fatalf("no active Gate 2 approval for plan %q", planID)
	}
	return id
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
	approvalID := activeApprovalIDFor(t, a, "P1")

	evidenceID, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "expected_red", "")
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

	// review finding（同一 task 兩顆 run 按鈕互不 disable 時，前端只靠
	// pendingKey FIFO 配對 started/finished 會錯位）：finished payload 也必須
	// 帶 plan_id/task_id/kind，讓前端能直接用這三個欄位定位是哪一格，不必猜
	// 「最近一筆 started」。
	var finishedPayload map[string]any
	if err := json.Unmarshal(events[1].Payload, &finishedPayload); err != nil {
		t.Fatalf("unmarshal finished payload: %v", err)
	}
	if finishedPayload["plan_id"] != "P1" || finishedPayload["task_id"] != "T1" || finishedPayload["kind"] != "expected_red" {
		t.Errorf("finished payload = %+v, want plan_id=P1 task_id=T1 kind=expected_red", finishedPayload)
	}

	// M3a.1 T8（§3.3.2 Step 1(b)）：started／finished payload 都必須帶固定的
	// gate2_approval_id（CAS 通過當下鎖定的權威值），additive 補欄不動既有欄位。
	var startedPayload map[string]any
	if err := json.Unmarshal(events[0].Payload, &startedPayload); err != nil {
		t.Fatalf("unmarshal started payload: %v", err)
	}
	if startedPayload["gate2_approval_id"] != approvalID {
		t.Errorf("started payload gate2_approval_id = %v, want %q", startedPayload["gate2_approval_id"], approvalID)
	}
	if finishedPayload["gate2_approval_id"] != approvalID {
		t.Errorf("finished payload gate2_approval_id = %v, want %q", finishedPayload["gate2_approval_id"], approvalID)
	}

	assertNoZombieWorktrees(t, a.workspaceDir)
}

// ---- RunEvidence: lineage 混入非 oracle 路徑必須拒絕 ----

func TestRunEvidenceRejectsNonOracleLineage(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

	// plan_commit 之後另立一筆 commit，把 oracle 檔（run_test.sh）改名移出
	// oracle 宣告範圍——plan_commit..test_commit 的 lineage 驗證必須拒絕它。
	runGit(t, a, "mv", "run_test.sh", "not_oracle.sh")
	runGit(t, a, "commit", "-m", "rename run_test.sh out of oracle scope")
	testCommit := revParseHead(t, a)

	if _, err := a.RunEvidence(approvalID, "P1", "T1", testCommit, "expected_red", ""); err == nil {
		t.Fatal("RunEvidence must reject a lineage range that renames the oracle file out of scope")
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

func TestRunEvidenceRejectsWithoutActiveGate2(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	if _, err := a.RunEvidence("irrelevant", "P1", "T1", strings.Repeat("0", 40), "expected_red", ""); err == nil {
		t.Fatal("RunEvidence without an active Gate 2 approval for the plan must reject")
	}
}

// ---- RunEvidence CAS（M3a.1 T8，§3.3.2）：換版偵測——ErrStaleGeneration＋零 side effect ----

// TestRunEvidenceRejectsStaleGate2Approval covers task-8-brief.md Step 1(a):
// expectedGate2ApprovalID no longer matches the authoritative active Gate 2
// approval RunEvidence reads under workflowMu — must reject with
// ErrStaleGeneration before touching anything (no worktree, no started
// event, no journal line).
func TestRunEvidenceRejectsStaleGate2Approval(t *testing.T) {
	a, ui := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

	if _, err := a.RunEvidence(approvalID+"-stale", "P1", "T1", planCommit, "expected_red", ""); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("RunEvidence with a mismatched expected approval id must reject with ErrStaleGeneration, got %v", err)
	}

	if events := ui.findEnvKind("evidence_run"); len(events) != 0 {
		t.Fatalf("CAS mismatch must not emit any evidence_run event, got %d: %+v", len(events), events)
	}
	journalPath := filepath.Join(a.stateDir, "evidence", "evidence.jsonl")
	data, rerr := os.ReadFile(journalPath)
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatalf("read evidence journal: %v", rerr)
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("evidence journal must have no entries for a CAS-rejected run, got: %s", data)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

// TestRunEvidenceCASBarrierRejectsSupersededApproval covers task-8-brief.md
// Step 1(c): a channel-barrier test seam (runEvidenceCASHook, fired after
// beginAppTxn but before workflowMu.Lock — deliberately earlier than
// decideBarrierHook's own critical-section position, so the hook body can
// itself call GateDecide, which takes workflowMu, without deadlocking)
// reproduces "press (expected=A) races the actual gate2 supersede" — the
// hook resubmits and approves a second Gate 2 for the same plan (mirroring
// app_tca_test.go's TestTCADecideRejectsWhenGate2SupersededBeforeDecide)
// while RunEvidence is parked immediately before it reads the authoritative
// approval id. Run under -race to confirm the two goroutines' access to
// a.workflowMu-guarded state is race-free.
func TestRunEvidenceCASBarrierRejectsSupersededApproval(t *testing.T) {
	a, ui := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalA := activeApprovalIDFor(t, a, "P1") // "按下" 當下讀到的 expected

	inWindow := make(chan struct{})
	release := make(chan struct{})
	a.runEvidenceCASHook = func() { close(inWindow); <-release }

	runDone := make(chan error, 1)
	go func() {
		_, err := a.RunEvidence(approvalA, "P1", "T1", planCommit, "expected_red", "")
		runDone <- err
	}()
	<-inWindow // beginAppTxn 已成功，卡在 workflowMu.Lock 之前——尚未讀到權威值

	gate2ID, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatalf("resubmit gate2: %v", err)
	}
	if err := a.GateDecide(gate2ID, "approved", "reapproved", mediumSel()); err != nil {
		t.Fatalf("approve second gate2 (supersede): %v", err)
	}
	approvalB := activeApprovalIDFor(t, a, "P1")
	if approvalB == approvalA {
		t.Fatal("supersede must produce a new active approval id")
	}

	close(release) // 放行 RunEvidence：繼續往下取 workflowMu、讀到已換版的 B

	if runErr := <-runDone; !errors.Is(runErr, ErrStaleGeneration) {
		t.Fatalf("RunEvidence must reject with ErrStaleGeneration once gate2 is superseded mid-flight, got %v", runErr)
	}
	if events := ui.findEnvKind("evidence_run"); len(events) != 0 {
		t.Fatalf("CAS mismatch must not emit any evidence_run event, got %d: %+v", len(events), events)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

// TestRunEvidenceCASBarrierBoundedByShutdown covers task-8-brief.md Step 2c:
// the same runEvidenceCASHook parks RunEvidence before its CAS check
// (expected id unchanged this time — no supersede), shutdown is triggered
// while it's parked there, then release lets CAS pass — the assertion this
// test needs (bounded shutdown, RunEvidence ends in error, zero side effect)
// is only reachable because of Step 2b's post-CAS shutdown recheck: at the
// point the hook fires, beginAppTxn already succeeded and no evidenceActive
// registration exists yet for reclaimEvidenceRuns to cancel.
func TestRunEvidenceCASBarrierBoundedByShutdown(t *testing.T) {
	a, ui := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

	inWindow := make(chan struct{})
	release := make(chan struct{})
	a.runEvidenceCASHook = func() { close(inWindow); <-release }

	runDone := make(chan error, 1)
	go func() {
		_, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "expected_red", "")
		runDone <- err
	}()
	<-inWindow // beginAppTxn 已成功，卡在 CAS 檢查（workflowMu.Lock）之前

	shutdownDone := make(chan struct{})
	shutdownStart := time.Now()
	go func() { a.shutdown(context.Background()); close(shutdownDone) }()

	waitFor(t, "shutdown to set shuttingDown", func() bool {
		a.shutMu.Lock()
		defer a.shutMu.Unlock()
		return a.shuttingDown
	})
	close(release) // 放行：CAS 比對照樣通過（expected 未變），但 Step 2b 的重查會擋下

	select {
	case <-shutdownDone:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown stalled on a CAS-barrier-blocked RunEvidence")
	}
	if elapsed := time.Since(shutdownStart); elapsed > 20*time.Second {
		t.Fatalf("shutdown took %s, want bounded", elapsed)
	}

	if runErr := <-runDone; runErr == nil {
		t.Fatal("RunEvidence must end in error once shutdown is observed by the post-CAS Step 2b recheck")
	}
	if events := ui.findEnvKind("evidence_run"); len(events) != 0 {
		t.Fatalf("shutdown observed mid-CAS-barrier must not emit any evidence_run event, got %d: %+v", len(events), events)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}

// ---- RegisterMutation ＋ RunEvidence negative_control ----

func TestRegisterMutationAndRunEvidenceNegativeControl(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	writeFile(t, filepath.Join(a.workspaceDir, "other.txt"), "unrelated content\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

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

	evidenceID, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "negative_control", mutationID)
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
	approvalID := activeApprovalIDFor(t, a, "P1")

	errCh := make(chan error, 1)
	go func() {
		_, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "expected_red", "")
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
	journalPath := filepath.Join(a.stateDir, "evidence", "evidence.jsonl")
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

// ---- shutdown TOCTOU window: beginAppTxn 成功到 ulid mint 之間（review M1）----

// barrierContextLoader wraps a real evidence.ContextLoader and blocks inside
// its first LoadAt call until release is closed — LoadAt runs inside
// evidence.Run before the ulid callback ever fires (see runner.go's frozen
// step order), so parking here reliably reproduces the window between
// RunEvidence's beginAppTxn() succeeding and evidenceActive's registration:
// the exact gap review finding M1 flagged (a shutdown whose
// reclaimEvidenceRuns snapshot lands here sees an empty registry and can
// never deliver a cancel to this run).
type barrierContextLoader struct {
	inner   evidence.ContextLoader
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *barrierContextLoader) LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.inner.LoadAt(commitOID, planID)
}

func (b *barrierContextLoader) LoadOracleAt(commitOID string) (evidence.OracleDecl, error) {
	return b.inner.LoadOracleAt(commitOID)
}

// TestShutdownDuringPreUlidWindowStillBoundsRunEvidence reproduces review
// finding M1: shutdown must observe a bounded return and RunEvidence must
// end in error even when reclaimEvidenceRuns's cancel snapshot lands before
// evidenceActive has this run's entry — the fix is RunEvidence's ulid
// callback self-canceling once it sees a.shuttingDown already true.
func TestShutdownDuringPreUlidWindowStillBoundsRunEvidence(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

	entered := make(chan struct{})
	release := make(chan struct{})
	a.evidenceContextLoaderOverride = &barrierContextLoader{inner: a.planLoader, entered: entered, release: release}

	errCh := make(chan error, 1)
	go func() {
		_, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "expected_red", "")
		errCh <- err
	}()

	select { // beginAppTxn 已成功、still 卡在 LoadAt——ulid 尚未 mint，evidenceActive 空
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for RunEvidence to enter the barrier LoadAt call")
	}
	a.evidenceMu.Lock()
	nActiveBeforeShutdown := len(a.evidenceActive)
	a.evidenceMu.Unlock()
	if nActiveBeforeShutdown != 0 {
		t.Fatalf("evidenceActive must still be empty at this point (pre-ulid window), got %d", nActiveBeforeShutdown)
	}

	shutdownDone := make(chan struct{})
	shutdownStart := time.Now()
	go func() { a.shutdown(context.Background()); close(shutdownDone) }()

	// 等 shutdown 真的把 shuttingDown 設成 true——reclaimEvidenceRuns 的空
	// snapshot 這時已經跑過（或正在跑），驗證的正是「之後才登記」也還能被收。
	waitFor(t, "shutdown to set shuttingDown", func() bool {
		a.shutMu.Lock()
		defer a.shutMu.Unlock()
		return a.shuttingDown
	})
	close(release) // 放行 LoadAt：RunEvidence 繼續跑到 ulid callback，自我 cancel

	select {
	case <-shutdownDone:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown stalled: pre-ulid window run was never bounded (review M1 regression)")
	}
	if elapsed := time.Since(shutdownStart); elapsed > 20*time.Second {
		t.Fatalf("shutdown took %s, want bounded", elapsed)
	}

	if runErr := <-errCh; runErr == nil {
		t.Fatal("RunEvidence must end in error once shutdown observes it mid-flight, even in the pre-ulid window")
	}

	a.evidenceMu.Lock()
	nActiveAfter := len(a.evidenceActive)
	a.evidenceMu.Unlock()
	if nActiveAfter != 0 {
		t.Fatalf("active-run registry must be empty after reclaim, got %d", nActiveAfter)
	}
}

// ---- ValidateTestCommit (Task 22): validate-only lineage precheck ----

func TestValidateTestCommitAcceptsPlanCommitItself(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	// planCommit 對自己而言必為 ancestor（git merge-base --is-ancestor 自反），
	// 範圍內 diff 為空——同 RunEvidence 的 happy path 用的 testCommit 值
	// （TestRunEvidenceExpectedRed_AppendsJournalAndEmitsProgress）。
	if err := a.ValidateTestCommit("P1", "T1", planCommit); err != nil {
		t.Fatalf("ValidateTestCommit: %v", err)
	}
}

func TestValidateTestCommitRejectsNonOracleLineage(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	setupApprovedEvidencePlan(t, a, "P1")

	runGit(t, a, "mv", "run_test.sh", "not_oracle.sh")
	runGit(t, a, "commit", "-m", "rename run_test.sh out of oracle scope")
	testCommit := revParseHead(t, a)

	if err := a.ValidateTestCommit("P1", "T1", testCommit); err == nil {
		t.Fatal("ValidateTestCommit must reject a lineage range that renames the oracle file out of scope")
	}
}

func TestValidateTestCommitRejectsUnknownTask(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	if err := a.ValidateTestCommit("P1", "T-does-not-exist", planCommit); err == nil {
		t.Fatal("ValidateTestCommit must reject an unknown task_id")
	}
}

func TestValidateTestCommitRejectsWithoutActiveGate2(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	if err := a.ValidateTestCommit("P1", "T1", strings.Repeat("0", 40)); err == nil {
		t.Fatal("ValidateTestCommit without an active Gate 2 approval for the plan must reject")
	}
}

// ---- EvidenceCommitCandidates (Task 22): recent-commit dropdown data source ----

func TestEvidenceCommitCandidatesListsCommitsAfterPlanCommitNewestFirst(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	setupApprovedEvidencePlan(t, a, "P1")

	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho one\nexit 0\n")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "first candidate")
	first := revParseHead(t, a)

	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho two\nexit 0\n")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "second candidate")
	second := revParseHead(t, a)

	candidates, err := a.EvidenceCommitCandidates("P1")
	if err != nil {
		t.Fatalf("EvidenceCommitCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("want 2 candidates after plan_commit, got %d: %+v", len(candidates), candidates)
	}
	if candidates[0].OID != second || candidates[0].Subject != "second candidate" {
		t.Errorf("candidates[0] = %+v, want newest-first: oid=%s subject=%q", candidates[0], second, "second candidate")
	}
	if candidates[1].OID != first || candidates[1].Subject != "first candidate" {
		t.Errorf("candidates[1] = %+v, want oid=%s subject=%q", candidates[1], first, "first candidate")
	}
}

func TestEvidenceCommitCandidatesEmptyNotNilWhenNoNewCommits(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nexit 0\n")
	setupApprovedEvidencePlan(t, a, "P1")

	candidates, err := a.EvidenceCommitCandidates("P1")
	if err != nil {
		t.Fatalf("EvidenceCommitCandidates: %v", err)
	}
	if candidates == nil {
		t.Fatal("EvidenceCommitCandidates must return a non-nil empty slice, not nil, when there are no new commits")
	}
	if len(candidates) != 0 {
		t.Fatalf("want 0 candidates, got %d: %+v", len(candidates), candidates)
	}
}

func TestEvidenceCommitCandidatesRejectsWithoutActiveGate2(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	if _, err := a.EvidenceCommitCandidates("P1"); err == nil {
		t.Fatal("EvidenceCommitCandidates without an active Gate 2 approval for the plan must reject")
	}
}

// ---- orphan worktree real-SIGKILL E2E (M3a.1 T2, m3a-results.md gap 6) ----
//
// The earlier spike attempt (m3a-results.md gap A10) could not reliably hit
// the crash window between NewWorktree's durable wt_active write and
// evidence.Run's `defer w.Remove(...)`: a manual `kill -9` raced against a
// sub-second test_contract command and always lost. This reproduces the
// window deterministically instead of racing a human: a real OS process
// (re-exec'd via os.Args[0], the TestHelperProcess convention used
// throughout the Go standard library, e.g. os/exec's own tests) runs
// production evidence.Run directly — no App wrapper (frozen by the task
// brief: App-layer txn/journal already has its own coverage; the worktree
// lifecycle itself is evidence.Run's job) — this test polls the registry
// journal until some evidence id has both wt_intent and wt_active durable
// (the exact post-NewWorktree state before Run's defer runs), then SIGKILLs
// that process outright. Deferred functions never run on SIGKILL, so the
// worktree directory becomes a genuine orphan — recoverable only by a later
// CleanupOrphans call.
//
// Killing test_contract's own subprocess (`sh run_test.sh`) would NOT
// reproduce this: runner.go starts it in its own process group (Setpgid),
// and evidence.Run's defer Remove still runs to completion in the parent
// regardless of how that grandchild exits. Only killing the process that is
// actually executing evidence.Run — the helper process itself — skips the
// defer.

// helperProcessEnvVar switches this test binary's re-exec entry point,
// TestHelperProcessRunEvidence, from a normal no-op `go test` run into
// "run evidence.Run and block" mode.
const helperProcessEnvVar = "WB_EVIDENCE_HELPER_PROCESS"

// registryLine is a minimal local mirror of internal/evidence's unexported
// registryRecord JSON shape (worktree.go's package doc: `{"_type":...,
// "evidence_id":...,"at":...}`) — package main cannot import that
// unexported type, so this test reads the registry journal file directly.
type registryLine struct {
	Type       string `json:"_type"`
	EvidenceID string `json:"evidence_id"`
}

// TestHelperProcessRunEvidence is this test binary's re-exec entry point: a
// normal `go test` run returns immediately (helperProcessEnvVar unset), but
// invoked as `<binary> -test.run=^TestHelperProcessRunEvidence$` with that
// env var set to "1", it calls production evidence.Run directly — the exact
// call a.RunEvidence itself makes (app.go), reusing appPlanLoader/
// appGitRunner rather than reimplementing a second ContextLoader — and then
// blocks in Run until this test's parent SIGKILLs it.
func TestHelperProcessRunEvidence(t *testing.T) {
	if os.Getenv(helperProcessEnvVar) != "1" {
		return
	}
	ld := appPlanLoader{git: appGitRunner{root: os.Getenv("WB_HELPER_REPO_ROOT")}}
	rs := evidence.RunSpec{
		Kind:       "expected_red",
		PlanID:     os.Getenv("WB_HELPER_PLAN_ID"),
		TaskID:     os.Getenv("WB_HELPER_TASK_ID"),
		PlanCommit: os.Getenv("WB_HELPER_PLAN_COMMIT"),
		TestCommit: os.Getenv("WB_HELPER_TEST_COMMIT"),
	}
	ulidFn := func() string { return contract.NewULID(time.Now()) }
	nowFn := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	_, _ = evidence.Run(context.Background(),
		os.Getenv("WB_HELPER_REPO_ROOT"), os.Getenv("WB_HELPER_CAS_DIR"), os.Getenv("WB_HELPER_REGISTRY_PATH"),
		ld, rs, ulidFn, nowFn)
}

// waitForWorktreeActive polls registryPath (a deadline loop, never a fixed
// sleep) until some evidence id has both a durable wt_intent and wt_active
// line and no wt_removed line — the exact window this test needs to
// SIGKILL inside of — returning that id, or fails the test once deadline
// passes (diagnosticsOut, if non-nil, is included in the failure message so
// a helper-process crash before it ever reaches NewWorktree is visible
// instead of just timing out silently).
func waitForWorktreeActive(t *testing.T, registryPath string, deadline time.Time, diagnosticsOut *strings.Builder) string {
	t.Helper()
	for {
		if data, err := os.ReadFile(registryPath); err == nil {
			hasIntent, hasActive, hasRemoved := map[string]bool{}, map[string]bool{}, map[string]bool{}
			for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if ln == "" {
					continue
				}
				var rec registryLine
				if jerr := json.Unmarshal([]byte(ln), &rec); jerr != nil {
					continue
				}
				switch rec.Type {
				case "wt_intent":
					hasIntent[rec.EvidenceID] = true
				case "wt_active":
					hasActive[rec.EvidenceID] = true
				case "wt_removed":
					hasRemoved[rec.EvidenceID] = true
				}
			}
			for id := range hasActive {
				if hasIntent[id] && !hasRemoved[id] {
					return id
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for wt_intent+wt_active to become durable in the registry; helper process output so far:\n%s", diagnosticsOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// registryHasRemoved reports whether registryPath contains a wt_removed
// line for evidenceID.
func registryHasRemoved(t *testing.T, registryPath, evidenceID string) bool {
	t.Helper()
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		var rec registryLine
		if jerr := json.Unmarshal([]byte(ln), &rec); jerr != nil {
			t.Fatalf("malformed registry line %q: %v", ln, jerr)
		}
		if rec.Type == "wt_removed" && rec.EvidenceID == evidenceID {
			return true
		}
	}
	return false
}

// TestOrphanWorktreeRealSIGKILL_RecoveredByCleanupOrphans is the E2E
// reproduction described in this section's doc comment above.
func TestOrphanWorktreeRealSIGKILL_RecoveredByCleanupOrphans(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	// sleep 5 gives the polling loop below a wide safety margin to observe
	// wt_active and deliver SIGKILL well before the command would finish on
	// its own — the flakiness the earlier manual spike attempt hit.
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\nsleep 5\necho 'FAIL: TestX'\nexit 1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessRunEvidence$")
	cmd.Env = append(os.Environ(),
		helperProcessEnvVar+"=1",
		"WB_HELPER_REPO_ROOT="+a.workspaceDir,
		"WB_HELPER_CAS_DIR="+a.evidenceCASDir,
		"WB_HELPER_REGISTRY_PATH="+a.evidenceRegistryPath,
		"WB_HELPER_PLAN_ID=P1",
		"WB_HELPER_TASK_ID=T1",
		"WB_HELPER_PLAN_COMMIT="+planCommit,
		"WB_HELPER_TEST_COMMIT="+planCommit,
	)
	var helperOut strings.Builder
	cmd.Stdout, cmd.Stderr = &helperOut, &helperOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	evidenceID := waitForWorktreeActive(t, a.evidenceRegistryPath, time.Now().Add(15*time.Second), &helperOut)
	dir := filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)

	// SIGKILL the helper process itself — the process executing
	// evidence.Run — not any subprocess it spawned. Deferred functions
	// (including w.Remove's defer inside Run) never run on SIGKILL.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL helper process: %v", err)
	}
	_, _ = cmd.Process.Wait()
	reaped = true

	// A genuine orphan: the worktree directory survives (defer Remove never
	// ran) and the registry has no wt_removed for this evidence id.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("want orphaned worktree dir %q to still exist after SIGKILL, stat err = %v", dir, err)
	}
	if registryHasRemoved(t, a.evidenceRegistryPath, evidenceID) {
		t.Fatalf("registry already has wt_removed for %q — SIGKILL failed to skip evidence.Run's defer Remove", evidenceID)
	}

	// Recovery: a fresh call (simulating the next app startup) must
	// reconcile the orphan.
	if err := evidence.CleanupOrphans(a.workspaceDir, a.evidenceRegistryPath, nil); err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}
	if _, err := evidence.CleanOrphanTemps(a.evidenceCASDir); err != nil {
		t.Fatalf("CleanOrphanTemps: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("want orphaned worktree dir %q removed after CleanupOrphans, stat err = %v", dir, err)
	}
	if !registryHasRemoved(t, a.evidenceRegistryPath, evidenceID) {
		t.Errorf("registry must have wt_removed for %q after CleanupOrphans", evidenceID)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}
