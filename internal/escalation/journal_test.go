package escalation

import (
	"path/filepath"
	"testing"
)

func TestJournalAppendAndProjectRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "escalation.jsonl")
	j, err := OpenJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendItem(Item{EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendTransition(ItemTransition{EscalationID: "E1", To: "acknowledged", Actor: "u1", At: "t1"}); err != nil {
		t.Fatal(err)
	}

	j2, err := OpenJournal(p) // 重啟載入
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Project(j2.Lines())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].State != StateAcknowledged {
		t.Fatalf("want 1 acknowledged entry after reload, got %+v", entries)
	}
}

func TestJournalAppendSetsType(t *testing.T) {
	// AppendItem/AppendTransition own stamping "_type" so callers can pass
	// zero-value Type — mirrors gate/evidence's journal wrappers.
	p := filepath.Join(t.TempDir(), "escalation.jsonl")
	j, err := OpenJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendItem(Item{EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", Summary: "s", CreatedAt: "t0"}); err != nil {
		t.Fatal(err)
	}
	entries, err := Project(j.Lines())
	if err != nil {
		t.Fatalf("Project must accept journal-appended lines without caller-set _type: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
}
