package wsregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreOnlyPersistsDurableWhitelist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workspace-sessions.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, forbidden := range []string{"starting", "active", "busy", "approval_pending", "ending"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("runtime state 不得持久化，出現 %q: %s", forbidden, b)
		}
	}
	if !strings.Contains(string(b), `"schema_version": 2`) {
		t.Fatalf("需帶 schema_version=2: %s", b)
	}
}

func TestRemoveKeepsTombstoneButDeleteUncommittedDoesNot(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "codex", CreatedAt: "t"})
	_ = s.Put(Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"})
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Get("w1"); !ok || e.RemovedAt == "" || e.RemoveReason != "user_removed" {
		t.Fatalf("使用者移除必須留 tombstone：%+v ok=%v", e, ok)
	}
	if err := s.DeleteUncommitted("w2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("DeleteUncommitted 必須整筆刪除（建立失敗不得永久留痕）")
	}
	if len(s.Live()) != 0 {
		t.Fatalf("tombstone 與已刪除都不得出現在 Live()：%+v", s.Live())
	}
}

func TestPersistFailureRollsBackMemory(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Put(Entry{WSID: "w2", Provider: "claude", CreatedAt: "t"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("persist 失敗後記憶體必須回滾")
	}
}
