package wsregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seedStore：兩筆 live entry 的 store（同 provider，正是 per-WSID writer 存在的
// 理由——provider-keyed 的記錄在這個形狀下指不出是哪一個）。
func seedStore(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "workspace-sessions.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"wA", "wB"} {
		if err := s.Put(Entry{WSID: w, Provider: "claude", TaskLabel: "label-" + w,
			CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	return s, p
}

// 跨重啟（本改動的核心風險維度 F）：resume identity 的價值全在重啟之後，所以
// 斷言一律走**新開的 Store 實例讀磁碟**，不是同一個實例的記憶體。
func reopen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCommitResumeIsPerWSIDAndSurvivesReopen(t *testing.T) {
	s, path := seedStore(t)
	if err := s.CommitResume("wA", "sess-A", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitResume("wB", "sess-B", "task-b"); err != nil {
		t.Fatal(err)
	}
	got := reopen(t, path)
	a, _ := got.Get("wA")
	b, _ := got.Get("wB")
	if a.ResumeSessionID != "sess-A" || b.ResumeSessionID != "sess-B" {
		t.Fatalf("兩個 session 的續聊身分必須各自持久化：%q / %q", a.ResumeSessionID, b.ResumeSessionID)
	}
	if a.TaskLabel != "task-a" || b.TaskLabel != "task-b" {
		t.Fatalf("task label 必須各自持久化：%q / %q", a.TaskLabel, b.TaskLabel)
	}
}

// 兩個空值語意（見 CommitResume doc）：空 resume 代表「這次還不知道 id」、空
// label 代表「這次沒帶標籤」，**都不得把既有值清掉**。清掉 label 等於 session
// 清單上的名字消失，清掉 resume 等於使用者莫名失去續聊。
func TestCommitResumeEmptyArgsPreserveExistingValues(t *testing.T) {
	s, path := seedStore(t)
	if err := s.CommitResume("wA", "sess-A", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitResume("wA", "", ""); err != nil {
		t.Fatal(err)
	}
	e, _ := reopen(t, path).Get("wA")
	if e.ResumeSessionID != "sess-A" {
		t.Fatalf("空 resume 不得清掉既有續聊身分，got %q", e.ResumeSessionID)
	}
	if e.TaskLabel != "task-a" {
		t.Fatalf("空 label 不得清掉既有標籤，got %q", e.TaskLabel)
	}
}

// SetResume 是 late init 的單一交易：只換 resume，label 與 view boundary 不動。
func TestSetResumeKeepsLabelAndViewStart(t *testing.T) {
	s, path := seedStore(t)
	if err := s.ResetView("wA", "01VIEWSTART00000000000001"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResume("wA", "sess-late"); err != nil {
		t.Fatal(err)
	}
	e, _ := reopen(t, path).Get("wA")
	if e.ResumeSessionID != "sess-late" {
		t.Fatalf("resume = %q", e.ResumeSessionID)
	}
	if e.TaskLabel != "label-wA" {
		t.Fatalf("SetResume 不得動 label，got %q", e.TaskLabel)
	}
	if e.ViewStartEventID != "01VIEWSTART00000000000001" {
		t.Fatalf("SetResume 不得動 view boundary，got %q", e.ViewStartEventID)
	}
}

// ResetView 是「開新對話」的單一交易：boundary 前移 ＋ resume 清空必須同一筆，
// 且只影響自己那一筆 entry（舊的 provider-keyed ResetView 會把手足一起清掉）。
func TestResetViewClearsOwnResumeOnly(t *testing.T) {
	s, path := seedStore(t)
	if err := s.CommitResume("wA", "sess-A", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitResume("wB", "sess-B", "task-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetView("wB", "01BOUNDARY000000000000001"); err != nil {
		t.Fatal(err)
	}
	got := reopen(t, path)
	b, _ := got.Get("wB")
	if b.ViewStartEventID != "01BOUNDARY000000000000001" || b.ResumeSessionID != "" {
		t.Fatalf("ResetView 必須同時前移 boundary 並清空自己的 resume：%+v", b)
	}
	if b.TaskLabel != "task-b" {
		t.Fatalf("ResetView 不得清掉 session 的名字，got %q", b.TaskLabel)
	}
	a, _ := got.Get("wA")
	if a.ResumeSessionID != "sess-A" || a.ViewStartEventID != "" {
		t.Fatalf("對 B 開新對話不得動到 A：%+v", a)
	}
}

// tombstone 與寫入之間的全序（取代 app 側的 check-then-act）：三個 mutator 都
// 必須在 store 自己的 mutex 內拒絕，且回傳可辨識的哨兵錯誤。
func TestMutatorsRejectTombstonedAndMissing(t *testing.T) {
	s, path := seedStore(t)
	if err := s.Remove("wA", "user_removed"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(string) error{
		"CommitResume": func(w string) error { return s.CommitResume(w, "x", "y") },
		"SetResume":    func(w string) error { return s.SetResume(w, "x") },
		"ResetView":    func(w string) error { return s.ResetView(w, "x") },
	}
	for name, call := range cases {
		if err := call("wA"); !errors.Is(err, ErrTombstoned) {
			t.Fatalf("%s 對 tombstone 必須回 ErrTombstoned，got %v", name, err)
		}
		if err := call("nope"); !errors.Is(err, ErrEntryNotFound) {
			t.Fatalf("%s 對不存在的 wsid 必須回 ErrEntryNotFound，got %v", name, err)
		}
	}
	e, _ := reopen(t, path).Get("wA")
	if e.ResumeSessionID != "" || e.ViewStartEventID != "" {
		t.Fatalf("被拒絕的寫入不得落盤：%+v", e)
	}
}

// persist 失敗回滾記憶體（同 Put／Remove 的既有慣例）：記憶體與磁碟不得分裂，
// 否則下一次成功的寫入會把這筆失敗變更一起持久化。
func TestMutatorPersistFailureRollsBackMemory(t *testing.T) {
	s, path := seedStore(t)
	if err := s.CommitResume("wA", "sess-A", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
	if err := s.SetResume("wA", "sess-clobber"); err == nil {
		t.Fatal("persist 失敗必須 fail loud")
	}
	if e, _ := s.Get("wA"); e.ResumeSessionID != "sess-A" {
		t.Fatalf("記憶體必須回滾，got %q", e.ResumeSessionID)
	}
}

func TestBackfillResumeOnlyFillsEmptyAndMarksOnce(t *testing.T) {
	s, path := seedStore(t)
	if err := s.CommitResume("wB", "already-mine", "task-b"); err != nil {
		t.Fatal(err)
	}
	if s.ResumeBackfilled() {
		t.Fatal("前提不成立：新 registry 不該已標記 backfilled")
	}
	if err := s.BackfillResume(map[string]string{"wA": "legacy-id", "wB": "legacy-id"}); err != nil {
		t.Fatal(err)
	}
	got := reopen(t, path)
	if !got.ResumeBackfilled() {
		t.Fatal("marker 必須持久化，否則每次啟動都會重跑 backfill")
	}
	a, _ := got.Get("wA")
	b, _ := got.Get("wB")
	if a.ResumeSessionID != "legacy-id" {
		t.Fatalf("空值必須被補上，got %q", a.ResumeSessionID)
	}
	if b.ResumeSessionID != "already-mine" {
		t.Fatalf("已有值不得被 legacy 覆寫，got %q", b.ResumeSessionID)
	}
}

// 沒有可填的 entry 時仍要設 marker——否則每次啟動都會對 stale 的 restore.json
// 重新判斷一次，而使用者按過「開新對話」之後那個判斷會回填一個已經被清掉的 id。
func TestBackfillResumeMarksEvenWithNothingToFill(t *testing.T) {
	s, path := seedStore(t)
	if err := s.BackfillResume(nil); err != nil {
		t.Fatal(err)
	}
	if !reopen(t, path).ResumeBackfilled() {
		t.Fatal("沒有東西可填時同樣必須標記，代表『已檢查過』")
	}
}

func TestBackfillResumeSkipsTombstoned(t *testing.T) {
	s, path := seedStore(t)
	if err := s.Remove("wA", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillResume(map[string]string{"wA": "legacy-id", "nope": "x"}); err != nil {
		t.Fatal(err)
	}
	e, _ := reopen(t, path).Get("wA")
	if e.ResumeSessionID != "" {
		t.Fatalf("已移除的 session 不得被 backfill 復活續聊，got %q", e.ResumeSessionID)
	}
}

// marker 與 entries 必須同生共死：persist 失敗時兩者都回滾。marker 是單向的，
// 若它先落地而 entries 沒有，那些 session 從此永遠補不回續聊。
func TestBackfillResumePersistFailureRollsBackMarkerAndEntries(t *testing.T) {
	s, path := seedStore(t)
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
	if err := s.BackfillResume(map[string]string{"wA": "legacy-id"}); err == nil {
		t.Fatal("persist 失敗必須 fail loud")
	}
	if s.ResumeBackfilled() {
		t.Fatal("marker 必須一起回滾（否則下次啟動不會重試）")
	}
	if e, _ := s.Get("wA"); e.ResumeSessionID != "" {
		t.Fatalf("entry 必須一起回滾，got %q", e.ResumeSessionID)
	}
}
