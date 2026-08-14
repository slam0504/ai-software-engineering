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
