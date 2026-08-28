package main

import (
	"os/exec"
	"testing"
)

// ---- Gate 1 live-loop 測試基盤（temp git workspace）----

// newTestAppGit：App 綁一個新 git init'd temp workspace，本地 git config 帶
// user.name／user.email（供 GateDecide 的真實 git identity 查詢路徑；正常路徑
// 不覆寫，走 production 的 a.gitIdentity()）。
func newTestAppGit(t *testing.T) *App {
	t.Helper()
	a, _ := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")
	return a
}

// newTestAppGitNoIdentity：同 newTestAppGit，但 GateDecide 的身分查詢改走
// gitIdentityOverride 回傳空值——避免依賴執行機器有無全域 git config，同時
// repo 本身仍保留 identity（commitAll 需要它才能真的 commit）。
func newTestAppGitNoIdentity(t *testing.T) *App {
	t.Helper()
	a := newTestAppGit(t)
	a.gitIdentityOverride = func() (string, string, error) { return "", "", nil }
	return a
}

func runGit(t *testing.T, a *App, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", a.workspaceDir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitAll：納管 spec/ 全部異動 add＋commit（測試用；production 的
// SpecCommit 兩階段流程屬另一 task，未在此重造）。
func commitAll(t *testing.T, a *App) {
	t.Helper()
	runGit(t, a, "add", "-A", "--", "spec/")
	runGit(t, a, "commit", "-m", "spec update")
}

func stateOf(list []GateEntryDTO, id string) string {
	for _, e := range list {
		if e.ApprovalID == id {
			return e.State
		}
	}
	return ""
}

func digestOf(t *testing.T, a *App, path string) string {
	t.Helper()
	sf, err := a.SpecRead(path)
	if err != nil {
		t.Fatal(err)
	}
	return sf.Digest
}

func TestGateLiveLoopSubmitApproveThenStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a) // 用 SpecCommit 或 helper commit 納管
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	list, _ := a.GateList()
	if stateOf(list, id) != "active" {
		t.Fatalf("want active after approve")
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)
	list, _ = a.GateList() // reconcile 觸發 STALE
	if stateOf(list, id) != "stale" {
		t.Fatalf("want stale after spec change, got %s", stateOf(list, id))
	}
}

// gateOpsCount：white-box 讀 gate journal 目前 op 數（B6 Task 3 detect-only
// 迴歸樁——純讀取入口不得 append durable transition，見 B5 spec §3.2(0)）。
func gateOpsCount(t *testing.T, a *App) int {
	t.Helper()
	if a.gateJournal == nil {
		t.Fatal("gateJournal not initialized")
	}
	return len(a.gateJournal.Ops())
}

// TestGateListDetectOnlyDoesNotPersistStale：GateList（純讀取入口）呈現 stale
// 投影，但不得 append durable stale transition——durable stale 只准持
// workflowMu 的寫入路徑（gateListReconciled／reconcileLocked）產生。
func TestGateListDetectOnlyDoesNotPersistStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a) // 使 current manifest 與 record binding 不符

	opsBefore := gateOpsCount(t, a)
	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if got := stateOf(list, id); got != "stale" {
		t.Fatalf("讀取應呈現 stale, got %s", got)
	}
	if got := gateOpsCount(t, a); got != opsBefore {
		t.Fatalf("GateList 不得 append：ops %d→%d", opsBefore, got)
	}
}

func TestGateDecideRejectsWithoutGitIdentity(t *testing.T) {
	a := newTestAppGitNoIdentity(t)
	a.SpecWrite("spec/glossary.md", "x", "")
	commitAll(t, a)
	id, _ := a.SubmitForApproval()
	if err := a.GateDecide(id, "approved", "", nil); err == nil {
		t.Fatal("missing git identity must reject approval")
	}
}
