package domainspec

import (
	"strings"
	"testing"
)

func TestCanonicalReorderAndDupSameDigest(t *testing.T) {
	// 出口 7（rev6）：contexts 重排＋含重複值 → 相同 digest
	a, b := mustSnapshot(t), mustSnapshot(t)
	a.Plan.Value.Tasks[0].Impact.Contexts = []string{"audit", "gate"}
	b.Plan.Value.Tasks[0].Impact.Contexts = []string{"gate", "gate", "audit"}
	da, _ := SnapshotDigest(a)
	db, _ := SnapshotDigest(b)
	if da != db {
		t.Fatalf("set-semantics fields must canonicalize: %s != %s", da, db)
	}
}

func TestCanonicalKeepsDuplicateSelections(t *testing.T) {
	s := mustSnapshot(t)
	sels := []RiskSelection{
		{TaskID: "T1", SelectedRiskTier: "low"}, {TaskID: "T1", SelectedRiskTier: "low"},
	}
	s.RiskSelections.Value = &sels
	j, _ := CanonicalJSON(s)
	if got := strings.Count(string(j), `"task_id":"T1"`); got != 2 {
		t.Fatalf("duplicate selections must survive canonicalization, got %d", got)
	}
}

func TestCanonicalTasksKeepSourceOrder(t *testing.T) {
	s := mustSnapshot(t)
	s.Plan.Value.Tasks = []PlanTask{
		{ID: "T9", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "low"},
		{ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low"},
	}
	j, _ := CanonicalJSON(s)
	if strings.Index(string(j), `"id":"T9"`) > strings.Index(string(j), `"id":"T1"`) {
		t.Fatal("plan.tasks must keep source order, not id order")
	}
}

func TestCanonicalNilAndEmptySliceEqual(t *testing.T) {
	a, b := mustSnapshot(t), mustSnapshot(t)
	var nilEsc []EscalationFact
	emptyEsc := []EscalationFact{}
	a.Escalations.Value = &nilEsc
	b.Escalations.Value = &emptyEsc
	da, _ := SnapshotDigest(a)
	db, _ := SnapshotDigest(b)
	if da != db {
		t.Fatal("nil and empty slices must produce identical canonical form")
	}
}
