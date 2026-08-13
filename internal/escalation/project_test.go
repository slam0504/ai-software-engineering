package escalation

import (
	"encoding/json"
	"testing"
)

func line(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustProject(t *testing.T, lines [][]byte) []Entry {
	t.Helper()
	es, err := Project(lines)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return es
}

func entryByID(entries []Entry, id string) Entry {
	for _, e := range entries {
		if e.Item.EscalationID == id {
			return e
		}
	}
	panic("entryByID: escalation " + id + " not found")
}

func TestProjectOpenItem(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "workspace", Summary: "s", CreatedAt: "t0"}),
	}
	entries := mustProject(t, lines)
	if len(entries) != 1 || entries[0].State != StateOpen {
		t.Fatalf("want 1 open entry, got %+v", entries)
	}
}

func TestProjectAcknowledgedStillBlocking(t *testing.T) {
	// §3.8: acknowledged 不解除阻擋——BlockingFor 仍須回報該項目。
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "acknowledged", Actor: "u1", At: "t1"}),
	}
	entries := mustProject(t, lines)
	e := entryByID(entries, "E1")
	if e.State != StateAcknowledged {
		t.Fatalf("want acknowledged, got %s", e.State)
	}
	blocking := BlockingFor(entries, "gate2:P1")
	if len(blocking) != 1 || blocking[0].Item.EscalationID != "E1" {
		t.Fatalf("acknowledged entry must still be blocking, got %+v", blocking)
	}
}

func TestProjectResolvedNotBlockingAndTerminal(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Resolution: "accepted_risk", Reason: "r", Actor: "u1", At: "t1"}),
		// 終態不復活：resolved 後再來一個 acknowledged 應被忽略。
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "acknowledged", Actor: "u1", At: "t2"}),
	}
	entries := mustProject(t, lines)
	e := entryByID(entries, "E1")
	if e.State != StateResolved {
		t.Fatalf("resolved must be terminal, got %s (revived by later transition)", e.State)
	}
	if blocking := BlockingFor(entries, "gate2:P1"); len(blocking) != 0 {
		t.Fatalf("resolved entry must not block, got %+v", blocking)
	}
}

func TestProjectResolvedThenNewOccurrenceNotMissed(t *testing.T) {
	// resolved 後同 conditionKey 再建 → Occurrence==2 新項；projection 不因舊項
	// resolved 而漏報，兩筆各自獨立存在（entries 以 escalation_id 為 key，不以
	// condition_key 合併/覆蓋）。
	key := "stale:gate2:P1"
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", ConditionKey: key, Occurrence: 1, Source: "system", BlockScope: "gate2:P1", Summary: "s1", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Resolution: "fixed", Reason: "r", Actor: "u1", At: "t1"}),
		line(t, Item{Type: "escalation_item", EscalationID: "E2", ConditionKey: key, Occurrence: 2, Source: "system", BlockScope: "gate2:P1", Summary: "s2", CreatedAt: "t2"}),
	}
	entries := mustProject(t, lines)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (old resolved occurrence + new open occurrence), got %d: %+v", len(entries), entries)
	}
	e1 := entryByID(entries, "E1")
	e2 := entryByID(entries, "E2")
	if e1.State != StateResolved {
		t.Fatalf("E1 want resolved, got %s", e1.State)
	}
	if e2.State != StateOpen || e2.Item.Occurrence != 2 {
		t.Fatalf("E2 want open occurrence 2, got state=%s occurrence=%d", e2.State, e2.Item.Occurrence)
	}
}

func TestOpenKeyedOnlyDedupsUnresolved(t *testing.T) {
	key := "stale:gate2:P1"
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", ConditionKey: key, Occurrence: 1, Source: "system", BlockScope: "gate2:P1", Summary: "s1", CreatedAt: "t0"}),
	}
	entries := mustProject(t, lines)

	if got := OpenKeyed(entries, key); got == nil || got.Item.EscalationID != "E1" {
		t.Fatalf("open item under key must be found for dedup, got %+v", got)
	}

	// 一旦 resolved，同 key 不再擋新建（OpenKeyed 回 nil）。
	lines = append(lines, line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Resolution: "fixed", Reason: "r", Actor: "u1", At: "t1"}))
	entries = mustProject(t, lines)
	if got := OpenKeyed(entries, key); got != nil {
		t.Fatalf("resolved entry under key must not dedup-block a new occurrence, got %+v", got)
	}

	// acknowledged（未 resolved）仍應被去重擋住。
	lines2 := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E3", ConditionKey: key, Occurrence: 1, Source: "system", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E3", To: "acknowledged", Actor: "u1", At: "t1"}),
	}
	entries2 := mustProject(t, lines2)
	if got := OpenKeyed(entries2, key); got == nil || got.Item.EscalationID != "E3" {
		t.Fatalf("acknowledged (unresolved) entry under key must still dedup, got %+v", got)
	}
}

func TestBlockingForWorkspaceOverridesAllScopes(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "event_id:ev1", BlockScope: "workspace", Summary: "s", CreatedAt: "t0"}),
	}
	entries := mustProject(t, lines)
	for _, scope := range []string{"gate2:P1", "tca:P1/T1", "evidence:ev9", "workspace"} {
		if blocking := BlockingFor(entries, scope); len(blocking) != 1 {
			t.Fatalf("workspace-scoped item must block scope %q, got %+v", scope, blocking)
		}
	}
}

func TestBlockingForExactScopeMatchOnly(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "system", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
	}
	entries := mustProject(t, lines)
	if blocking := BlockingFor(entries, "gate2:P2"); len(blocking) != 0 {
		t.Fatalf("scope mismatch must not block, got %+v", blocking)
	}
	if blocking := BlockingFor(entries, "gate2:P1"); len(blocking) != 1 {
		t.Fatalf("exact scope match must block, got %+v", blocking)
	}
}

func TestBlockingForEmptyBlockScopeNeverBlocks(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "event_id:ev1", BlockScope: "", Summary: "informational", CreatedAt: "t0"}),
	}
	entries := mustProject(t, lines)
	for _, scope := range []string{"workspace", "gate2:P1", ""} {
		if blocking := BlockingFor(entries, scope); len(blocking) != 0 {
			t.Fatalf("empty block_scope must never block (scope %q), got %+v", scope, blocking)
		}
	}
}

func TestProjectResolvedMissingResolutionFailsLoud(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		// Resolution 缺失。
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Reason: "r", Actor: "u1", At: "t1"}),
	}
	if _, err := Project(lines); err == nil {
		t.Fatal("resolved transition missing resolution must fail loud")
	}
}

func TestProjectResolvedMissingReasonFailsLoud(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Resolution: "accepted_risk", Actor: "u1", At: "t1"}),
	}
	if _, err := Project(lines); err == nil {
		t.Fatal("resolved transition missing reason must fail loud")
	}
}

func TestProjectResolvedMissingActorFailsLoud(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", BlockScope: "gate2:P1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "resolved", Resolution: "accepted_risk", Reason: "r", At: "t1"}),
	}
	if _, err := Project(lines); err == nil {
		t.Fatal("resolved transition missing actor must fail loud")
	}
}

func TestProjectUnknownTypeFailsLoud(t *testing.T) {
	lines := [][]byte{[]byte(`{"_type":"something_else"}`)}
	if _, err := Project(lines); err == nil {
		t.Fatal("unknown _type must fail loud")
	}
}

func TestProjectUnknownTransitionTargetFailsLoud(t *testing.T) {
	lines := [][]byte{
		line(t, Item{Type: "escalation_item", EscalationID: "E1", Source: "manual", SourceRef: "task_id:T1", Summary: "s", CreatedAt: "t0"}),
		line(t, ItemTransition{Type: "escalation_transition", EscalationID: "E1", To: "closed", At: "t1"}),
	}
	if _, err := Project(lines); err == nil {
		t.Fatal("unknown transition target must fail loud")
	}
}

func TestProjectMalformedJSONFailsLoud(t *testing.T) {
	lines := [][]byte{[]byte(`{not valid json`)}
	if _, err := Project(lines); err == nil {
		t.Fatal("unmarshal failure must fail loud")
	}
}
