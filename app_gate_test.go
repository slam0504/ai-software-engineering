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
	_, digest, err := a.SpecRead(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestGateLiveLoopSubmitApproveThenStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a) // 用 SpecCommit 或 helper commit 納管
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok"); err != nil {
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

func TestGateDecideRejectsWithoutGitIdentity(t *testing.T) {
	a := newTestAppGitNoIdentity(t)
	a.SpecWrite("spec/glossary.md", "x", "")
	commitAll(t, a)
	id, _ := a.SubmitForApproval()
	if err := a.GateDecide(id, "approved", ""); err == nil {
		t.Fatal("missing git identity must reject approval")
	}
}
