package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustOp(t *testing.T, recs ...any) GateOp {
	t.Helper()
	records := make([]json.RawMessage, 0, len(recs))
	for _, r := range recs {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, raw)
	}
	return GateOp{OpID: "op", At: "t", Records: records}
}

func TestJournalAppendRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	j, err := OpenJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(mustOp(t, GateRequest{Type: "gate_request", ApprovalID: "A", Gate: "gate1"})); err != nil {
		t.Fatal(err)
	}
	j2, _ := OpenJournal(p) // 重啟載入
	if len(j2.Ops()) != 1 {
		t.Fatalf("want 1 op after reload, got %d", len(j2.Ops()))
	}
}

func TestJournalMidfileMalformedRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	os.WriteFile(p, []byte("{bad}\n{\"op_id\":\"x\",\"records\":[]}\n"), 0o644)
	if _, err := OpenJournal(p); err == nil {
		t.Fatal("mid-file malformed must be rejected (fail loud)")
	}
}

func TestJournalFinalMalformedRepairThenAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	good := `{"op_id":"o1","at":"t","records":[]}` + "\n"
	os.WriteFile(p, []byte(good+`{"op_id":"o2` /* 截斷 */), 0o644)
	j, err := OpenJournal(p) // final malformed → quarantine + truncate 修復
	if err != nil {
		t.Fatalf("final malformed should repair, got %v", err)
	}
	if j.Degraded() {
		t.Fatal("successful repair must not be degraded")
	}
	if err := j.Append(mustOp(t, GateRequest{Type: "gate_request", ApprovalID: "B"})); err != nil {
		t.Fatal(err)
	}
	j2, err := OpenJournal(p) // 再重啟仍完整（壞 tail 已修）
	if err != nil {
		t.Fatalf("reload after repair failed: %v", err)
	}
	if len(j2.Ops()) != 2 {
		t.Fatalf("want 2 ops (o1 + appended), got %d", len(j2.Ops()))
	}
	if _, err := os.Stat(p + ".quarantine"); err != nil {
		t.Fatal("bad tail must be quarantined as evidence")
	}
}

func TestJournalTornFinalNewlineRepaired(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	// A complete, validly-parseable op line, but WITHOUT its trailing '\n'.
	// Append always writes line+'\n' in one Sync'd call, so a file missing
	// that trailing newline means the write was never durably committed —
	// it must be treated as a torn tail even though the JSON itself parses.
	torn := `{"op_id":"o1","at":"t","records":[]}`
	os.WriteFile(p, []byte(torn), 0o644)

	j, err := OpenJournal(p)
	if err != nil {
		t.Fatalf("torn final newline should repair, got %v", err)
	}
	if j.Degraded() {
		t.Fatal("successful repair must not be degraded")
	}
	if got := len(j.Ops()); got != 0 {
		t.Fatalf("torn (never-committed) line must be discarded, got %d ops", got)
	}
	if _, err := os.Stat(p + ".quarantine"); err != nil {
		t.Fatal("torn tail must be quarantined as evidence")
	}

	if err := j.Append(mustOp(t, GateRequest{Type: "gate_request", ApprovalID: "C"})); err != nil {
		t.Fatal(err)
	}

	j2, err := OpenJournal(p)
	if err != nil {
		t.Fatalf("reload after repair failed (mid-file corruption?): %v", err)
	}
	if len(j2.Ops()) != 1 {
		t.Fatalf("want 1 op (only the appended one; torn line discarded), got %d", len(j2.Ops()))
	}
}
