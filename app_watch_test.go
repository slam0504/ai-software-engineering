package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	t.Cleanup(a.stopSpecWatch) // 停 watcher goroutine，避免 t.TempDir() 清掉後它還在跑
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
	t.Cleanup(a.stopSpecWatch) // 停 watcher goroutine，避免 t.TempDir() 清掉後它還在跑

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

// TestWatcherPicksUpLateCreatedSpecDir：fix round 2（acceptance smoke 發現的
// 落差）——watchSpecTree() 啟動當下 spec/ 尚不存在（全新 workspace，尚未有人
// 寫過第一個納管檔）。watcher 必須觀察到 workspace root 上 spec/ 這個 CREATE
// 並遞迴 Add 進去，而不是啟動當下看一次「不存在」就永遠不看了。
//
// fix round 3（re-review 抓到 false positive）：斷言改成直接檢查 watcher 觸發
// 的 binding_stale envelope（同 TestWatcherIgnoresNonSpecWrites 的手法），
// wait/assert 路徑完全不呼叫 a.GateList()——GateList() 每次呼叫都會自己做
// 權威 reconcile（Task 10），先前版本用 waitFor+GateList 斷言，即使 watcher
// 完全沒接住晚建立的 spec/、單靠 GateList 自己的 reconcile 也會通過，等於沒測
// 到 watcher 本身。red-check：暫時還原成 early-return 的舊版 watchSpecTree，
// 這版斷言會 FAIL；復原修復後轉 PASS（見 task-12-report.md fix round 3）。
func TestWatcherPicksUpLateCreatedSpecDir(t *testing.T) {
	a, ui := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")

	a.watchSpecTree() // spec/ 還不存在時就先啟動——只看得到 workspace root
	t.Cleanup(a.stopSpecWatch)

	// spec/ 在 watchSpecTree() 之後才第一次被建立（SpecWrite 內部 MkdirAll）。
	if _, err := a.SpecWrite("spec/glossary.md", "v1", ""); err != nil {
		t.Fatal(err)
	}
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok"); err != nil {
		t.Fatal(err)
	}

	a.SpecWrite("spec/glossary.md", "v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)

	waitFor(t, "watcher emits binding_stale for the approval after a change under a spec/ tree created post-watchSpecTree()", func() bool {
		for _, ev := range ui.findEnvKind("binding_stale") {
			var payload struct {
				ApprovalID string `json:"approval_id"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.ApprovalID == id {
				return true
			}
		}
		return false
	})
}

// TestWatcherIgnoresNonSpecWrites：fix round 2——watcher 必須 Add workspace
// root 才能觀察到 spec/ 被晚建立（見上一測試），但 root 底下同時有
// .workbench/（gate journal／events 持續寫入）與其他檔案。若事件沒有過濾到
// spec/ 子樹，reconcile 觸發的 journal append 會被自己的 watch 事件再次撿到，
// 形成無窮迴圈。這裡直接檢查 UI envelope（不經 GateList，GateList 自己的
// 權威 reconcile 會混淆判斷來源），確保非 spec/ 寫入不會產生任何
// binding_stale 事件。
func TestWatcherIgnoresNonSpecWrites(t *testing.T) {
	a, ui := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")

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
	t.Cleanup(a.stopSpecWatch)

	wb := filepath.Join(a.workspaceDir, ".workbench")
	if err := os.MkdirAll(wb, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(wb, "junk"), []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.workspaceDir, "root-level.txt"), []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 給 watcher 一段遠大於 debounce（200ms）的時間處理——若過濾漏了任何一個
	// 非 spec/ 事件，這裡會來得及反映成一筆 binding_stale。
	time.Sleep(500 * time.Millisecond)

	if evs := ui.findEnvKind("binding_stale"); len(evs) != 0 {
		t.Fatalf("non-spec/ writes must not trigger binding_stale, got %d event(s)", len(evs))
	}
	l, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(l, id) != "active" {
		t.Fatalf("approval must remain active when only non-spec/ paths changed, got %s", stateOf(l, id))
	}
}
