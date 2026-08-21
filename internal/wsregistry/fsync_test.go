package wsregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- M3b Task 2a：registry 持久化的 fsync 契約（owner 2026-08-17 凍結）----
//
// **這批測試量得到什麼、量不到什麼（失效形狀 (E)，先講清楚）**：
// 一般測試環境沒有真的斷電，所以沒有任何一條測試能證明「資料真的落盤了」。
// 這裡守的是兩件可觀測的事——
//  1. 四個步驟**確實被呼叫、而且照凍結順序**（TestPersistCallsFourStepsInOrder）；
//  2. 每一步失敗時的**處置**對不對（後面四條注入測試）。
// 「fsync 測試綠」不等於「斷電安全已驗證」，後者需要真實的 power-cut 實驗。

// ForceStepHookForTest：persistLocked 四個步驟的測試專用接點（同
// internal/wirelog、internal/replayindex 的 ForceWriteErrForTest 慣例——鉤子宣告
// 在 _test.go，production 檔只留一個恆為 nil 的欄位）。
//
// hook 每經過一個步驟被呼叫一次；回非 nil 即代表該步驟失敗、且**真正的檔案操作
// 不會執行**。傳 nil 清除注入，但已經 latch 的 uncertain 不會因此解除。
func (s *Store) ForceStepHookForTest(hook func(fsyncStep) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = hook
}

// recordSteps：裝一個只記錄、不注入失敗的 hook，回傳讀取已記錄步驟的函式。
func recordSteps(s *Store) func() []fsyncStep {
	var seen []fsyncStep
	s.ForceStepHookForTest(func(step fsyncStep) error {
		seen = append(seen, step)
		return nil
	})
	return func() []fsyncStep { return seen }
}

// failAt：只在指定步驟回錯的 hook。
func failAt(s *Store, target fsyncStep, err error) {
	s.ForceStepHookForTest(func(step fsyncStep) error {
		if step == target {
			return err
		}
		return nil
	})
}

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "workspace-sessions.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

// diskEntry：直接從磁碟上的 JSON 讀一筆 entry（不經過記憶體，這正是要對照的那一邊）。
func diskEntry(t *testing.T, path, wsid string) (Entry, bool) {
	t.Helper()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open 磁碟內容失敗：%v", err)
	}
	return s2.Get(wsid)
}

// TestPersistCallsFourStepsInOrder：凍結契約的第 1 條——同目錄暫存檔 → 暫存檔
// Sync → rename → parent directory Sync，缺一不可、順序不可換。
//
// 這是唯一直接守「有沒有呼叫 fsync」的測試；沒有它，兩個 Sync 就只是註解裡的
// 保證（失效形狀 (C)）。
//
// mutation（各自只打紅這一條）：
//   - 拿掉 writeTempLocked 的 f.Sync()→ 記到 [write rename directory-sync]，
//     在「步驟序列不符」那一行紅。
//   - 拿掉 persistLocked 的 syncDir → 記到 [write file-sync rename]，同一行紅。
//   - 把 rename 移到 file-sync 之前 → 順序不符，同一行紅。
func TestPersistCallsFourStepsInOrder(t *testing.T) {
	s, _ := openStore(t)
	seen := recordSteps(s)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	got := seen()
	want := []fsyncStep{stepWrite, stepFileSync, stepRename, stepDirSync}
	if len(got) != len(want) {
		t.Fatalf("落盤步驟序列不符：got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("落盤步驟序列不符：got %v want %v", got, want)
		}
	}
}

// TestPersistIsNotDebouncedOrBatched：凍結契約的第 5 條——每一次 lifecycle 寫入
// 都要自己走完四步，不得為了省 sync 合併。
//
// mutation：在 persistLocked 前面加「距離上次落盤 < N 就跳過」的 debounce →
// 第二次 Put 記到 0 步，在「每次寫入都必須各自落盤」那一行紅。
func TestPersistIsNotDebouncedOrBatched(t *testing.T) {
	s, _ := openStore(t)
	seen := recordSteps(s)
	for _, w := range []string{"w1", "w2", "w3"} {
		if err := s.Put(Entry{WSID: w, Provider: "codex", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(seen()); n != 12 { // 3 次寫入 × 4 步
		t.Fatalf("每次寫入都必須各自落盤（3×4=12 步），got %d：%v", n, seen())
	}
}

// TestWriteFailureRollsBackAndDoesNotLatch：注入點 1。
// commit 明確沒有發生 → 舊值仍具權威性 → 沿用既有回滾語意，**不** latch。
//
// mutation：
//   - 把 stepWrite 的注入接點拿掉（改成無條件 f.Write）→ Put 成功，在「必須
//     回錯」那一行紅。
//   - 讓 write 失敗也 latch → 在「不得 latch」那一行紅。
//   - 拿掉回滾 → 在「記憶體必須退回舊值」那一行紅。
func TestWriteFailureRollsBackAndDoesNotLatch(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepWrite, errors.New("disk full"))
	err := s.SetResume("w1", "new")
	if err == nil {
		t.Fatal("write 失敗必須回錯")
	}
	if errors.Is(err, ErrRegistryUncertain) || s.Uncertain() {
		t.Fatalf("write 失敗 commit 明確沒發生，不得 latch uncertain：%v", err)
	}
	if e, _ := s.Get("w1"); e.ResumeSessionID != "old" {
		t.Fatalf("記憶體必須退回舊值，got %q", e.ResumeSessionID)
	}
	if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "old" {
		t.Fatalf("磁碟必須維持舊值，got %q", e.ResumeSessionID)
	}
	// latch 沒觸發 → 注入清除後照樣可以繼續寫。
	s.ForceStepHookForTest(nil)
	if err := s.SetResume("w1", "new"); err != nil {
		t.Fatalf("未 latch 時應可繼續寫入：%v", err)
	}
}

// TestFileSyncFailureRollsBackAndDoesNotLatch：注入點 2。
// 暫存檔 fsync 失敗 → rename 不得執行 → 目標檔完全沒被碰過。
//
// mutation：
//   - 把 f.Sync() 的錯誤吞掉（只記不回）→ rename 照跑，在「磁碟必須維持舊值」
//     那一行紅。
//   - 讓 file-sync 失敗也 latch → 在「不得 latch」那一行紅。
func TestFileSyncFailureRollsBackAndDoesNotLatch(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepFileSync, errors.New("fsync EIO"))
	err := s.SetResume("w1", "new")
	if err == nil {
		t.Fatal("暫存檔 fsync 失敗必須回錯")
	}
	if errors.Is(err, ErrRegistryUncertain) || s.Uncertain() {
		t.Fatalf("rename 尚未執行，不得 latch uncertain：%v", err)
	}
	if e, _ := s.Get("w1"); e.ResumeSessionID != "old" {
		t.Fatalf("記憶體必須退回舊值，got %q", e.ResumeSessionID)
	}
	if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "old" {
		t.Fatalf("磁碟必須維持舊值（rename 不得在 fsync 失敗後執行），got %q", e.ResumeSessionID)
	}
}

// TestRenameFailureRollsBackAndLeavesNoTempFile：注入點 3。
// rename 失敗 → commit 沒發生 → 回滾、不 latch，且不留半成品暫存檔。
//
// mutation：
//   - 拿掉失敗路徑的 os.Remove(tmp) → 在「不得留下暫存檔」那一行紅。
//   - 讓 rename 失敗也 latch → 在「不得 latch」那一行紅。
func TestRenameFailureRollsBackAndLeavesNoTempFile(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepRename, errors.New("rename EXDEV"))
	err := s.SetResume("w1", "new")
	if err == nil {
		t.Fatal("rename 失敗必須回錯")
	}
	if errors.Is(err, ErrRegistryUncertain) || s.Uncertain() {
		t.Fatalf("rename 沒成功，不得 latch uncertain：%v", err)
	}
	if e, _ := s.Get("w1"); e.ResumeSessionID != "old" {
		t.Fatalf("記憶體必須退回舊值，got %q", e.ResumeSessionID)
	}
	if _, serr := os.Stat(path + ".tmp"); serr == nil {
		t.Fatal("失敗路徑不得留下暫存檔")
	}
}

// TestDirectorySyncFailureLatchesUncertainAndRefusesAllWrites：注入點 4 的
// **專屬守門測試**（owner 凍結契約第 3、4 條）。
//
// rename 已經成功——新值很可能已經在磁碟上。所以這一格既不能沿用一般 rollback
// （等於宣稱舊值仍具權威性，但下次啟動載入的會是新值），也不能報成功。
//
// 一條測試守四件事，每件各有自己只打紅正題的 mutation：
//  1. 回 ErrRegistryUncertain — mutation：dir-sync 失敗改回普通 error →「必須回
//     ErrRegistryUncertain」那一行紅。
//  2. **不回滾**記憶體 — mutation：persistOrRollback 拿掉 ErrRegistryUncertain
//     例外（一律回滾）→「記憶體不得退回舊值」那一行紅（其餘三項照樣綠）。
//  3. latch 後拒絕所有後續寫入 — mutation：拿掉 persistOrRollback／Sync 開頭的
//     uncertain 檢查 →「latch 後 %s 必須被拒絕」那一行紅。
//  4. 讀取放行 — mutation：把 latch 檢查加進 Get／Live →「讀取必須放行」那一行紅。
func TestDirectorySyncFailureLatchesUncertainAndRefusesAllWrites(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepDirSync, errors.New("dir fsync EIO"))

	err := s.SetResume("w1", "new")
	if !errors.Is(err, ErrRegistryUncertain) {
		t.Fatalf("directory sync 失敗必須回 ErrRegistryUncertain，got %v", err)
	}
	if !s.Uncertain() {
		t.Fatal("directory sync 失敗必須 latch uncertain")
	}
	// 訊號要能讓使用者知道發生什麼、該怎麼辦。
	if msg := err.Error(); !strings.Contains(msg, "重啟") || !strings.Contains(msg, "不確定") {
		t.Fatalf("錯誤訊息要說明「結果不確定」與「請重啟」：%s", msg)
	}

	// (2) 不回滾：rename 已成功，退回舊值等於宣稱一個 process 內無法證明的事實。
	if e, _ := s.Get("w1"); e.ResumeSessionID != "new" {
		t.Fatalf("記憶體不得退回舊值（rename 已成功），got %q", e.ResumeSessionID)
	}

	// 注入清除也不解除 latch——單向，只有重啟能復原。
	s.ForceStepHookForTest(nil)
	if !s.Uncertain() {
		t.Fatal("uncertain 是單向 latch，清除注入不得解除")
	}

	// (3) 所有寫入入口一律拒絕。
	writes := map[string]func() error{
		"Put":               func() error { return s.Put(Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"}) },
		"Remove":            func() error { return s.Remove("w1", "user_removed") },
		"DeleteUncommitted": func() error { return s.DeleteUncommitted("w1") },
		"CommitResume":      func() error { return s.CommitResume("w1", "x", "lbl") },
		"SetResume":         func() error { return s.SetResume("w1", "y") },
		"ResetView":         func() error { return s.ResetView("w1", "evt") },
		"SetLayout":         func() error { return s.SetLayout(Layout{Pins: []string{"w1"}}) },
		"BackfillResume":    func() error { return s.BackfillResume(map[string]string{"w1": "z"}) },
		"MarkMigrated":      func() error { return s.MarkMigrated([]Entry{{WSID: "w9"}}) },
		"Sync":              func() error { return s.Sync() },
	}
	for name, fn := range writes {
		if werr := fn(); !errors.Is(werr, ErrRegistryUncertain) {
			t.Fatalf("latch 後 %s 必須被拒絕，got %v", name, werr)
		}
	}
	// 被拒絕的寫入沒有碰檔案系統，記憶體要停在 latch 當下、不得被半套寫入污染。
	if e, ok := s.Get("w1"); !ok || e.ResumeSessionID != "new" || e.RemovedAt != "" {
		t.Fatalf("被拒絕的寫入不得改動記憶體：%+v ok=%v", e, ok)
	}
	if len(s.Layout().Pins) != 0 {
		t.Fatalf("被拒絕的 SetLayout 不得改動記憶體：%+v", s.Layout())
	}

	// (4) 讀取放行：uncertain 的是「有沒有 durable」，不是「記憶體被破壞」。
	if e, ok := s.Get("w1"); !ok || e.Provider != "claude" {
		t.Fatalf("讀取必須放行（Get）：%+v ok=%v", e, ok)
	}
	if len(s.Live()) != 1 {
		t.Fatalf("讀取必須放行（Live）：%+v", s.Live())
	}

	// rename 確實成功過：磁碟上是新值。這正是「舊值不再具權威性」的證據。
	if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "new" {
		t.Fatalf("rename 已成功，磁碟應為新值，got %q", e.ResumeSessionID)
	}
}

// TestRestartClearsUncertainAndLoadsDiskContent：失效形狀 (F) 的守門——latch 的
// 復原路徑只存在於「跨 process 重啟」那一維，同一個 Store 實例上永遠看不到。
//
// 重啟＝重新 Open：讀磁碟上實際的內容（rename 成功的那一份），新的 Store 不帶
// latch，寫入恢復。
//
// mutation：
//   - 把 uncertain 做成 package 級變數（跨實例共用）→「重啟後不得帶著 latch」
//     那一行紅。
//   - 讓 latch 觸發時同時回寫舊值到磁碟 →「重啟後應載入磁碟上的新值」那一行紅。
func TestRestartClearsUncertainAndLoadsDiskContent(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepDirSync, errors.New("dir fsync EIO"))
	if err := s.SetResume("w1", "new"); !errors.Is(err, ErrRegistryUncertain) {
		t.Fatalf("前提：directory sync 失敗要 latch，got %v", err)
	}

	// ---- 重啟 ----
	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("重啟後應能重新載入 registry：%v", err)
	}
	if restarted.Uncertain() {
		t.Fatal("重啟後不得帶著 latch（latch 是 process 內的事實，不持久化）")
	}
	e, ok := restarted.Get("w1")
	if !ok || e.ResumeSessionID != "new" {
		t.Fatalf("重啟後應載入磁碟上的新值（rename 已成功），got %+v ok=%v", e, ok)
	}
	if err := restarted.SetResume("w1", "after-restart"); err != nil {
		t.Fatalf("重啟後寫入應恢復：%v", err)
	}
	if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "after-restart" {
		t.Fatalf("重啟後的寫入應落盤，got %q", e.ResumeSessionID)
	}
}
