package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditHighWatermark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// NotExist：函式層回報「未能確認」，非錯誤。
	if last, scanned, err := auditHighWatermark(path); last != "" || scanned || err != nil {
		t.Fatalf("NotExist 應回 (\"\", false, nil)：%q %v %v", last, scanned, err)
	}
	// 完整掃描：空檔與有內容各一格。
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if last, scanned, err := auditHighWatermark(path); last != "" || !scanned || err != nil {
		t.Fatalf("空檔應回 (\"\", true, nil)：%q %v %v", last, scanned, err)
	}
	lines := `{"event_id":"e1"}` + "\n" + `{broken` + "\n" + `{"event_id":"e2"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if last, scanned, err := auditHighWatermark(path); last != "e2" || !scanned || err != nil {
		t.Fatalf("完整掃描（malformed 跳過）應回 (e2, true, nil)：%q %v %v", last, scanned, err)
	}
}

// open error（非 NotExist）確定性注入：父路徑為普通檔案 → 讀子路徑 → ENOTDIR
// （spec §5：chmod 需 root skip、目錄注入命中的是 Scanner.Err()，皆不用）。
func TestAuditHighWatermarkOpenErrorFailsLoud(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	last, scanned, err := auditHighWatermark(filepath.Join(parent, "events.jsonl"))
	if err == nil || scanned || last != "" {
		t.Fatalf("ENOTDIR open error 必須回錯且不回值：%q %v %v", last, scanned, err)
	}
}

// Scanner.Err()（>16MiB 單行）→ 回錯、不回部分值。mutation：忽略 Scanner.Err()
// 改回現狀 → 本測試紅（截讀偏舊值對寫入路徑是持久化污染，spec §2）。
func TestAuditHighWatermarkScanErrorFailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1"}` + "\n" + strings.Repeat("x", 17*1024*1024)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	last, scanned, err := auditHighWatermark(path)
	if err == nil || scanned {
		t.Fatalf("Scanner.Err() 必須回錯：%v %v", scanned, err)
	}
	if last != "" {
		t.Fatalf("不得回部分值（截讀偏舊）：%q", last)
	}
}
