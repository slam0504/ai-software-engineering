package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- Task 12：spec/ 遞迴 watcher（通知層，spec §4）----

// TestWatcherTriggersReconcileOnSpecChange：核可後改動已納管檔 → watcher
// debounce 後觸發 ReconcileGate1()，GateList 讀到 stale（brief 逐字測試；
// waitFor 沿用既有 app_test.go 的 (t, what string, cond) 簽名，不重造第二個
// 同名 helper）。
func TestWatcherTriggersReconcileOnSpecChange(t *testing.T) {
	a := newTestAppGit(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok"); err != nil {
		t.Fatal(err)
	}
	a.watchSpecTree()
	a.SpecWrite("spec/glossary.md", "v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)
	waitFor(t, "gate goes stale after watcher-triggered reconcile", func() bool {
		l, _ := a.GateList()
		return stateOf(l, id) == "stale"
	})
}

// TestWatcherClosesOnShutdown：shutdown 必須關掉 watcher goroutine（stop
// close 後 done 必須被關閉），不留 orphan goroutine——沒有這個保證，長跑的
// app 每次 NewSession／重啟都會多一個永遠不退出的 watcher goroutine。
func TestWatcherClosesOnShutdown(t *testing.T) {
	a := newTestAppGit(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	commitAll(t, a)
	a.watchSpecTree()

	a.mu.Lock()
	done := a.specWatchDone
	a.mu.Unlock()
	if done == nil {
		t.Fatal("watchSpecTree must start the watch goroutine when spec/ exists")
	}

	a.stopSpecWatch()

	select {
	case <-done:
	default:
		t.Fatal("watcher goroutine must have exited after stopSpecWatch")
	}
	a.mu.Lock()
	stop := a.specWatchStop
	a.mu.Unlock()
	if stop != nil {
		t.Fatal("specWatchStop must be cleared after stopSpecWatch")
	}
}

// TestWatcherReAddsNewSubdirectory：spec §4 明確要求新目錄 Create 時
// re-add——新建 spec/features/ 之後在其中寫檔也必須觸發 reconcile，不能只
// 覆蓋 watchSpecTree() 啟動當下已存在的子樹。
func TestWatcherReAddsNewSubdirectory(t *testing.T) {
	a := newTestAppGit(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok"); err != nil {
		t.Fatal(err)
	}
	a.watchSpecTree()

	if err := os.MkdirAll(filepath.Join(a.workspaceDir, "spec", "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	// re-add 是 watcher goroutine 內部非同步處理 Create 事件的結果；不預先等待，
	// 靠下方 waitFor 的重試視窗吸收（watcher 若沒 re-add，這裡永遠不會變 stale
	// 而逾時失敗，斷言仍然有效）。
	a.SpecWrite("spec/features/login.feature", "Feature: login", "")
	commitAll(t, a)
	waitFor(t, "gate goes stale after change under newly created subdirectory", func() bool {
		l, _ := a.GateList()
		return stateOf(l, id) == "stale"
	})
}
