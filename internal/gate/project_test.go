package gate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var opCounter int

func opWith(t *testing.T, recs ...any) GateOp {
	t.Helper()
	opCounter++
	op := GateOp{OpID: fmt.Sprintf("op-%d", opCounter), At: "t"}
	for _, r := range recs {
		var typed any
		switch v := r.(type) {
		case GateRequest:
			v.Type = "gate_request"
			typed = v
		case ApprovalRecord:
			v.Type = "approval_record"
			typed = v
		case Transition:
			v.Type = "transition"
			typed = v
		default:
			t.Fatalf("opWith: unsupported record type %T", r)
		}
		b, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("opWith: marshal record: %v", err)
		}
		op.Records = append(op.Records, b)
	}
	return op
}

func mustProject(t *testing.T, ops []GateOp) []GateEntry {
	t.Helper()
	es, err := Project(ops)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return es
}

func entryByID(entries []GateEntry, id string) GateEntry {
	for _, e := range entries {
		if e.ApprovalID == id {
			return e
		}
	}
	panic(fmt.Sprintf("entryByID: approval %q not found", id))
}

func gate1B(mDigest, bDigest string) []Binding {
	return []Binding{
		{"spec_manifest", "spec/", mDigest},
		{"base_commit", "HEAD", bDigest},
	}
}

func hex64() string { return strings.Repeat("a", 64) }
func hex40() string { return strings.Repeat("a", 40) }

func TestProjectPendingThenActiveThenStale(t *testing.T) {
	ops := []GateOp{
		opWith(t, GateRequest{ApprovalID: "A", Gate: "gate1", SpecManifestDigest: "sha256:x", BaseCommit: "git:sha1:c1"}),
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: []Binding{{"spec_manifest", "spec/", "sha256:x"}, {"base_commit", "HEAD", "git:sha1:c1"}}}),
	}
	e := entryByID(mustProject(t, ops), "A")
	if e.State != Active {
		t.Fatalf("want active, got %s", e.State)
	}
	ops = append(ops, opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "changed"}))
	e = entryByID(mustProject(t, ops), "A")
	if e.State != Stale {
		t.Fatalf("want stale, got %s", e.State)
	}
	// stale 不復活：再加同 digest 也不變回 active
	ops = append(ops, opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "noop"}))
	if entryByID(mustProject(t, ops), "A").State != Stale {
		t.Fatal("stale must not revive")
	}
}

func TestProjectSupersede(t *testing.T) {
	ops := []GateOp{
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: gate1B("sha256:x", "git:sha1:c1")},
			Transition{ApprovalID: "A", To: "superseded", Cause: "new approval"}),
		opWith(t, ApprovalRecord{ApprovalID: "B", Gate: "gate1", Decision: "approved",
			Bindings: gate1B("sha256:y", "git:sha1:c2")}),
	}
	es := mustProject(t, ops)
	if entryByID(es, "A").State != Superseded || entryByID(es, "B").State != Active {
		t.Fatal("want A superseded, B active — at most one active")
	}
}

func TestValidateGate1Bindings(t *testing.T) {
	if err := ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:"+hex40())); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	// 重複 kind 拒絕
	dup := []Binding{{"spec_manifest", "spec/", "sha256:" + hex64()}, {"spec_manifest", "spec/", "sha256:" + hex64()}, {"base_commit", "HEAD", "git:sha1:" + hex40()}}
	if ValidateGate1Bindings(dup) == nil {
		t.Fatal("duplicate kind must be rejected")
	}
	// 缺 base_commit 拒絕
	if ValidateGate1Bindings([]Binding{{"spec_manifest", "spec/", "sha256:" + hex64()}}) == nil {
		t.Fatal("missing base_commit must be rejected")
	}
	// 短 SHA 拒絕
	if ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:abc123")) == nil {
		t.Fatal("short SHA must be rejected")
	}
}
