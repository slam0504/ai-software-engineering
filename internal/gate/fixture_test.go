package gate

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestM2GateV1FixtureSmoke is the M3a Task 0 compatibility baseline: it
// freezes real M2 v1 journal bytes (see testdata/README.md) and asserts the
// current (pre-generalization) OpenJournal/Project code path still parses
// them cleanly. Any future change to internal/gate that breaks this test is
// a v1 replay compatibility regression.
func TestM2GateV1FixtureSmoke(t *testing.T) {
	src, err := os.Open(filepath.Join("testdata", "m2-gate-v1.jsonl"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer src.Close()

	// Copy into a scratch dir so OpenJournal's append-handle doesn't touch
	// testdata (OpenJournal opens the path O_WRONLY|O_APPEND for writing).
	dst := filepath.Join(t.TempDir(), "gate.jsonl")
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create scratch copy: %v", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close scratch copy: %v", err)
	}

	j, err := OpenJournal(dst)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer j.Close()

	ops := j.Ops()
	const wantOps = 6 // see testdata/README.md
	if len(ops) != wantOps {
		t.Fatalf("want %d ops, got %d", wantOps, len(ops))
	}

	entries, err := Project(ops)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	const wantEntries = 3 // see testdata/README.md
	if len(entries) != wantEntries {
		t.Fatalf("want %d entries, got %d", wantEntries, len(entries))
	}
}
