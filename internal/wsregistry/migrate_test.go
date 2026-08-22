package wsregistry

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateIsIdempotentAcrossRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	legacy := map[string]LegacyEntry{
		"claude": {ViewStartEventID: "e100", ResumeSessionID: "sess-a", TaskID: "task-a"},
		"codex":  {}, // 空 entry：不建立、不佔名額（§3.2.5）
	}
	n := 0
	gen := func() string { n++; return fmt.Sprintf("w%d", n) }

	s1, _ := Open(p)
	got1, err := Migrate(s1, legacy, gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got1) != 1 || got1[0].Provider != "claude" {
		t.Fatalf("只該遷 claude：%+v", got1)
	}
	if got1[0].ViewStartEventID != "e100" || got1[0].ResumeSessionID != "sess-a" || got1[0].TaskLabel != "task-a" {
		t.Fatalf("view window／resume／task 必須沿用：%+v", got1[0])
	}
	s2, _ := Open(p) // 模擬重啟
	got2, _ := Migrate(s2, legacy, gen)
	if len(got2) != 0 {
		t.Fatalf("已遷移不得再建第二枚 WSID：%+v", got2)
	}
	if e, _ := s2.Get("w1"); e.WSID != "w1" {
		t.Fatalf("重啟後 WSID 必須相同：%+v", e)
	}
	if n != 1 {
		t.Fatalf("WSID 只能產生一次，產生了 %d 次", n)
	}
}

func TestMigratePersistFailureFailsLoud(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "ws.json"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := Migrate(s, map[string]LegacyEntry{"claude": {TaskID: "t"}},
		func() string { return "w1" }); err == nil {
		t.Fatal("migration persist 失敗必須 fail loud")
	}
	if s.Migrated() {
		t.Fatal("失敗不得標記 migrated")
	}
}

// TestRemovedLegacyIsNotRemigrated 名稱講的是「防重遷」，但實際機制純粹靠
// Migrated() marker——Remove 完全不碰 s.file.Migrated，也沒有獨立的
// 「檢查該 provider 是否已被 tombstone」分支。這個測試真正守的是「Remove
// 不會意外重置 marker」；marker 在第一次 Migrate 成功時已經寫入，第二次
// Migrate 靠它 early return，與 tombstone 本身無關。
func TestRemovedLegacyIsNotRemigrated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	legacy := map[string]LegacyEntry{"claude": {TaskID: "t"}}
	s, _ := Open(p)
	out, _ := Migrate(s, legacy, func() string { return "w1" })
	_ = s.Remove(out[0].WSID, "user_removed")
	s2, _ := Open(p)
	if again, _ := Migrate(s2, legacy, func() string { return "w2" }); len(again) != 0 {
		t.Fatalf("legacy 移除後不得再次遷入（§3.6.1）：%+v", again)
	}
}

// TestMigrateRefusesWhenLiveEntriesExistWithoutMarker：第二層防禦
// （coordinator #1 裁決）。理論上 Migrated()==false 時 s.file.Entries 必為
// 空，但 workspace-sessions.json 可被手動編輯、或載入順序被破壞而先塞入
// entries；此時若仍放行 MarkMigrated，整批取代語意會把既有 entries 無聲
// 蒸發且回傳 nil error。Migrate 必須偵測到這個狀態並直接拒絕。
func TestMigrateRefusesWhenLiveEntriesExistWithoutMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	s, _ := Open(p)
	if err := s.Put(Entry{WSID: "existing-1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "existing-2", Provider: "codex", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	legacy := map[string]LegacyEntry{"claude": {TaskID: "t"}}
	out, err := Migrate(s, legacy, func() string { return "w1" })
	if err == nil {
		t.Fatalf("registry 已有 live entries 但 marker 未設時必須拒絕遷移，卻回傳 out=%+v", out)
	}
	if out != nil {
		t.Fatalf("拒絕路徑不得回傳任何 entries：%+v", out)
	}

	live := s.Live()
	if len(live) != 2 {
		t.Fatalf("既有 entries 不得被清空：%+v", live)
	}
	if s.Migrated() {
		t.Fatal("拒絕路徑不得標記 migrated")
	}
}

// TestMigrateRefusesWhenOnlyTombstonesExistWithoutMarker：guard 用
// entryCount()（含 tombstone）而非 Live()（排除 tombstone）的原因
// （coordinator 追加裁決）。registry 只剩 tombstone、沒有 live entry 時，
// Live() 回空——若 guard 只看 Live()，這個狀態會被誤判成「registry 是空
// 的」而放行遷移，MarkMigrated 的整批取代就會把 tombstone 無聲丟棄，等於
// 打開 §3.6.1 tombstone 機制要防的「已移除 session 復活」的洞。
func TestMigrateRefusesWhenOnlyTombstonesExistWithoutMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	s, _ := Open(p)
	if err := s.Put(Entry{WSID: "removed-1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("removed-1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if live := s.Live(); len(live) != 0 {
		t.Fatalf("前提不成立：Live() 應為空，實際 %+v", live)
	}

	legacy := map[string]LegacyEntry{"claude": {TaskID: "t"}}
	out, err := Migrate(s, legacy, func() string { return "w1" })
	if err == nil {
		t.Fatalf("registry 只剩 tombstone 但 marker 未設時必須拒絕遷移，卻回傳 out=%+v", out)
	}
	if out != nil {
		t.Fatalf("拒絕路徑不得回傳任何 entries：%+v", out)
	}

	e, ok := s.Get("removed-1")
	if !ok || e.RemovedAt == "" {
		t.Fatalf("tombstone 不得被清空：%+v (ok=%v)", e, ok)
	}
	if s.Migrated() {
		t.Fatal("拒絕路徑不得標記 migrated")
	}
}

// TestMigrateDeterministicOrderAcrossProviders：兩個 provider 都非空時，
// 走訪順序與 WSID 指派必須是決定性的（claude 先於 codex），不能依賴 map
// 迭代順序。brief 的三個測試都只有一個 provider 非空，沒有覆蓋到這個情境。
func TestMigrateDeterministicOrderAcrossProviders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	legacy := map[string]LegacyEntry{
		"claude": {TaskID: "task-claude"},
		"codex":  {TaskID: "task-codex"},
	}
	ids := []string{"w-a", "w-b"}
	i := 0
	gen := func() string { id := ids[i]; i++; return id }

	s, _ := Open(p)
	out, err := Migrate(s, legacy, gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("兩個 provider 皆非空，應各建一筆：%+v", out)
	}
	if out[0].Provider != "claude" || out[0].WSID != "w-a" {
		t.Fatalf("第一筆必須是 claude／w-a（決定性順序）：%+v", out[0])
	}
	if out[1].Provider != "codex" || out[1].WSID != "w-b" {
		t.Fatalf("第二筆必須是 codex／w-b（決定性順序）：%+v", out[1])
	}
}

func TestMigrateSetsLegacyTranscriptFlag(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
		"codex":  {ResumeSessionID: "sess-x", HasLegacyTranscript: false},
	}
	n := 0
	out, err := Migrate(s, legacy, func() string { n++; return fmt.Sprintf("w%d", n) })
	if err != nil {
		t.Fatal(err)
	}
	byProv := map[string]Entry{}
	for _, e := range out {
		byProv[e.Provider] = e
	}
	if !byProv["claude"].LegacyTranscript {
		t.Fatal("有 legacy window 的 entry 應設 LegacyTranscript=true")
	}
	if byProv["codex"].LegacyTranscript {
		t.Fatal("resume-only（無 window）的 entry 不得設 LegacyTranscript")
	}
}
