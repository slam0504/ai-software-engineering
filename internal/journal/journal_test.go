package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalAppendRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append([]byte(`{"op_id":"A"}`)); err != nil {
		t.Fatal(err)
	}
	j2, err := Open(p) // 重啟載入
	if err != nil {
		t.Fatal(err)
	}
	if got := len(j2.Lines()); got != 1 {
		t.Fatalf("want 1 line after reload, got %d", got)
	}
}

func TestJournalMidfileMalformedRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	os.WriteFile(p, []byte("{bad}\n{\"op_id\":\"x\"}\n"), 0o644)
	if _, err := Open(p); err == nil {
		t.Fatal("mid-file malformed must be rejected (fail loud)")
	}
}

func TestJournalFinalMalformedRepairThenAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	good := `{"op_id":"o1"}` + "\n"
	os.WriteFile(p, []byte(good+`{"op_id":"o2` /* 截斷 */), 0o644)
	j, err := Open(p) // final malformed → quarantine + truncate 修復
	if err != nil {
		t.Fatalf("final malformed should repair, got %v", err)
	}
	if j.Degraded() {
		t.Fatal("successful repair must not be degraded")
	}
	if err := j.Append([]byte(`{"op_id":"o2b"}`)); err != nil {
		t.Fatal(err)
	}
	j2, err := Open(p) // 再重啟仍完整（壞 tail 已修）
	if err != nil {
		t.Fatalf("reload after repair failed: %v", err)
	}
	if got := len(j2.Lines()); got != 2 {
		t.Fatalf("want 2 lines (o1 + appended), got %d", got)
	}
	quarantine, err := os.ReadFile(p + ".quarantine")
	if err != nil {
		t.Fatal("bad tail must be quarantined as evidence")
	}
	if string(quarantine) != `{"op_id":"o2` {
		t.Fatalf("quarantine content mismatch: %q", quarantine)
	}
}

func TestJournalTornFinalNewlineRepaired(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	// A complete, validly-parseable line, but WITHOUT its trailing '\n'.
	// Append always writes line+'\n' in one Sync'd call, so a file missing
	// that trailing newline means the write was never durably committed —
	// it must be treated as a torn tail even though the JSON itself parses.
	torn := `{"op_id":"o1"}`
	os.WriteFile(p, []byte(torn), 0o644)

	j, err := Open(p)
	if err != nil {
		t.Fatalf("torn final newline should repair, got %v", err)
	}
	if j.Degraded() {
		t.Fatal("successful repair must not be degraded")
	}
	if got := len(j.Lines()); got != 0 {
		t.Fatalf("torn (never-committed) line must be discarded, got %d lines", got)
	}
	quarantine, err := os.ReadFile(p + ".quarantine")
	if err != nil {
		t.Fatal("torn tail must be quarantined as evidence")
	}
	if string(quarantine) != torn {
		t.Fatalf("quarantine content mismatch: %q", quarantine)
	}

	if err := j.Append([]byte(`{"op_id":"o1b"}`)); err != nil {
		t.Fatal(err)
	}

	j2, err := Open(p)
	if err != nil {
		t.Fatalf("reload after repair failed (mid-file corruption?): %v", err)
	}
	if got := len(j2.Lines()); got != 1 {
		t.Fatalf("want 1 line (only the appended one; torn line discarded), got %d", got)
	}
}

func TestJournalDegradedOnWriteError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	// Close the underlying file handle out from under the Journal so the
	// next write fails, simulating a write/sync error mid-operation.
	if err := j.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.Append([]byte(`{"op_id":"A"}`)); err == nil {
		t.Fatal("append after underlying file closed must fail")
	}
	if !j.Degraded() {
		t.Fatal("journal must be degraded after a write error")
	}
	if err := j.Append([]byte(`{"op_id":"B"}`)); err != ErrDegraded {
		t.Fatalf("further appends on a degraded journal must return ErrDegraded, got %v", err)
	}
}

func TestJournalLinesReturnsCopy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append([]byte(`{"op_id":"A"}`)); err != nil {
		t.Fatal(err)
	}
	lines := j.Lines()
	lines[0][0] = 'X' // 竄改回傳的 copy 不應影響內部狀態
	again := j.Lines()
	if string(again[0]) == string(lines[0]) {
		t.Fatal("Lines() must return an independent copy, not a shared slice")
	}
}
