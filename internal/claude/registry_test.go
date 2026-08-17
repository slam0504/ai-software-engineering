package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryBindLookup(t *testing.T) {
	r, err := OpenRegistry(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("s1", "/a/b", "01WSIDA0000000000000000001"); err != nil {
		t.Fatal(err)
	}
	cwd, wsid, ok := r.Lookup("s1")
	if !ok || cwd != "/a/b" {
		t.Fatalf("lookup = %q %v", cwd, ok)
	}
	if wsid != "01WSIDA0000000000000000001" {
		t.Fatalf("WSID 必須與 cwd 一起持久化，got %q", wsid)
	}
	if _, _, ok := r.Lookup("nope"); ok {
		t.Fatal("unknown id must miss")
	}
}

// 跨重啟：綁定的 WSID 必須是**磁碟上**的事實，不是 process 記憶體——resume
// guard 的價值全在重啟之後（重啟前記憶體裡本來就知道誰是誰）。
func TestRegistryWSIDSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	r, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("s1", "/a/b", "01WSIDA0000000000000000001"); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenRegistry(path) // 新實例讀磁碟＝跨重啟
	if err != nil {
		t.Fatal(err)
	}
	if _, wsid, ok := r2.Lookup("s1"); !ok || wsid != "01WSIDA0000000000000000001" {
		t.Fatalf("重啟後 WSID 綁定必須還在，got %q %v", wsid, ok)
	}
}

// 舊格式（本 build 之前寫下、沒有 wsid 欄位）必須解析成「wsid 未知」而不是
// 讀取失敗——呼叫端靠這個空字串分辨「不知道」與「知道且不符」。
func TestRegistryLegacyEntryHasEmptyWSID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path,
		[]byte(`{"s-old":{"cwd":"/a/b","created_at":"2026-08-01T00:00:00Z"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	cwd, wsid, ok := r.Lookup("s-old")
	if !ok || cwd != "/a/b" {
		t.Fatalf("舊記錄必須仍可解析：%q %v", cwd, ok)
	}
	if wsid != "" {
		t.Fatalf("舊記錄的 wsid 必須是空字串（未知），got %q", wsid)
	}
}
