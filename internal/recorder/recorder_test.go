package recorder

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWritesNDJSONAndFullMeta(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "claude-case1", ".ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Line([]byte(`{"type":"result"}`)); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := r.CloseWith(Meta{Provider: "claude", CLIVersion: "2.x",
		Argv: []string{"claude", "-p", "--verbose"}, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "claude-case1.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{`"argv"`, `"--verbose"`, `"exit_code": 0`, `"provider"`} {
		if !strings.Contains(string(b), must) {
			t.Fatalf("meta lacks %s: %s", must, b)
		}
	}
}

func TestExitCodeOnlyWhenExited(t *testing.T) { // v1.7：執行中無 exit_code；退出碼 0 必須保留
	dir := t.TempDir()
	r, _ := New(dir, "codex-running", ".jsonl")
	if err := r.CloseWith(Meta{Provider: "codex", ProcessStillRunning: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "codex-running.meta.json"))
	if strings.Contains(string(b), `"exit_code"`) {
		t.Fatalf("running meta must omit exit_code entirely: %s", b)
	}
	if !strings.Contains(string(b), `"process_still_running": true`) {
		t.Fatalf("running meta must mark process_still_running: %s", b)
	}
}

func TestNewValidatesCaseNameAndExt(t *testing.T) { // v1.6：白名單 + basename + prefix↔ext 一致
	dir := t.TempDir()
	for _, bad := range []struct{ name, ext string }{
		{"claude-x", ".txt"},          // 副檔名白名單
		{"case1", ".ndjson"},          // 無 provider prefix
		{"../claude-evil", ".ndjson"}, // 路徑分隔符
		{"sub/claude-x", ".ndjson"},
		{"codex-x", ".ndjson"}, // prefix ↔ ext 不一致
		{"claude-x", ".jsonl"},
	} {
		if _, err := New(dir, bad.name, bad.ext); err == nil {
			t.Fatalf("must reject %q %s", bad.name, bad.ext)
		}
	}
	if _, err := New(dir, "codex-ok", ".jsonl"); err != nil {
		t.Fatal(err)
	}
}

func TestLineConcurrentSafe(t *testing.T) { // v1.6：c2s／s2c 並行 tee；go test -race 驗證
	r, err := New(t.TempDir(), "codex-race", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.Line([]byte(`{"dir":"s2c","frame":{}}`))
			}
		}()
	}
	wg.Wait()
	if err := r.CloseWith(Meta{Provider: "codex", ProcessStillRunning: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLinePropagatesWriteError(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "claude-case2", ".ndjson")
	if err != nil {
		t.Fatal(err)
	}
	r.f.Close() // 人為造成底層檔案不可寫
	if err := r.Line([]byte("x")); err == nil {
		t.Fatal("Line must propagate write error")
	}
	if r.Err() == nil {
		t.Fatal("Err must latch")
	}
	// v1.4：有 latched 錯誤時 CloseWith 必須「meta 照寫 + 回傳非 nil」，錯誤不得只留在 meta
	if err := r.CloseWith(Meta{}); err == nil {
		t.Fatal("CloseWith must return the latched error")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "claude-case2.meta.json"))
	if !strings.Contains(string(b), `"recorder_error"`) {
		t.Fatalf("meta must still carry recorder_error: %s", b)
	}
}

func TestCloseWithPropagatesCloseError(t *testing.T) { // v1.4：底層 close 失敗不可吞
	dir := t.TempDir()
	r, _ := New(dir, "claude-case3", ".ndjson")
	r.f.Close() // 先關掉 → CloseWith 的 close 會失敗
	if err := r.CloseWith(Meta{}); err == nil {
		t.Fatal("CloseWith must propagate underlying close error")
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-case3.meta.json")); err != nil {
		t.Fatal("meta must be written even when close fails")
	}
}
