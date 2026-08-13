package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		{Kind: "spec_manifest", Ref: "spec/", Digest: mDigest},
		{Kind: "base_commit", Ref: "HEAD", Digest: bDigest},
	}
}

func hex64() string { return strings.Repeat("a", 64) }
func hex40() string { return strings.Repeat("a", 40) }

func TestProjectPendingThenActiveThenStale(t *testing.T) {
	ops := []GateOp{
		opWith(t, GateRequest{ApprovalID: "A", Gate: "gate1", SpecManifestDigest: "sha256:x", BaseCommit: "git:sha1:c1"}),
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: []Binding{
				{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:x"},
				{Kind: "base_commit", Ref: "HEAD", Digest: "git:sha1:c1"},
			}}),
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

func TestProjectStaleAfterSupersededDoesNotDowngrade(t *testing.T) {
	ops := []GateOp{
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "approved",
			Bindings: gate1B("sha256:x", "git:sha1:c1")}),
		opWith(t, Transition{ApprovalID: "A", To: "superseded", Cause: "new approval"}),
		opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "changed"}),
	}
	if got := entryByID(mustProject(t, ops), "A").State; got != Superseded {
		t.Fatalf("want superseded (stale must not downgrade), got %s", got)
	}
}

func TestProjectRejectedNotDowngradedByStale(t *testing.T) {
	ops := []GateOp{
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "rejected"}),
		opWith(t, Transition{ApprovalID: "A", To: "stale", Cause: "changed"}),
	}
	if got := entryByID(mustProject(t, ops), "A").State; got != Rejected {
		t.Fatalf("want rejected (stale must not overwrite terminal rejected), got %s", got)
	}
}

func TestProjectRejectedNotDowngradedBySuperseded(t *testing.T) {
	ops := []GateOp{
		opWith(t, ApprovalRecord{ApprovalID: "A", Gate: "gate1", Decision: "rejected"}),
		opWith(t, Transition{ApprovalID: "A", To: "superseded", Cause: "new approval"}),
	}
	if got := entryByID(mustProject(t, ops), "A").State; got != Rejected {
		t.Fatalf("want rejected (superseded must not overwrite terminal rejected), got %s", got)
	}
}

func TestProjectUnknownTypeErrors(t *testing.T) {
	ops := []GateOp{
		{OpID: "op-x", At: "t", Records: []json.RawMessage{
			json.RawMessage(`{"_type":"bogus","approval_id":"A"}`),
		}},
	}
	if _, err := Project(ops); err == nil {
		t.Fatal("want error for unknown record _type, got nil")
	}
}

func TestValidateGate1Bindings(t *testing.T) {
	if err := ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:"+hex40())); err != nil {
		t.Fatalf("valid should pass: %v", err)
	}
	// 重複 kind 拒絕
	dup := []Binding{
		{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:" + hex64()},
		{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:" + hex64()},
		{Kind: "base_commit", Ref: "HEAD", Digest: "git:sha1:" + hex40()},
	}
	if ValidateGate1Bindings(dup) == nil {
		t.Fatal("duplicate kind must be rejected")
	}
	// 缺 base_commit 拒絕
	if ValidateGate1Bindings([]Binding{{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:" + hex64()}}) == nil {
		t.Fatal("missing base_commit must be rejected")
	}
	// 短 SHA 拒絕
	if ValidateGate1Bindings(gate1B("sha256:"+hex64(), "git:sha1:abc123")) == nil {
		t.Fatal("short SHA must be rejected")
	}
}

func TestBindingKindRoleUniqueness(t *testing.T) {
	bs := []Binding{
		{Kind: "evidence_run", Role: "expected_red", Ref: "evidence:01A", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: "evidence_run", Role: "negative_control", Ref: "evidence:01B", Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	req := []BindingReq{
		{Kind: "evidence_run", Role: "expected_red", DigestRe: reSHA256},
		{Kind: "evidence_run", Role: "negative_control", DigestRe: reSHA256},
	}
	if err := validateBindingSet(bs, req); err != nil {
		t.Fatalf("distinct roles must pass: %v", err)
	}
	bs[1].Role = "expected_red" // 同 (kind,role) 重複
	if err := validateBindingSet(bs, req); err == nil {
		t.Fatal("duplicate (kind,role) must fail")
	}
}

func TestProjectNormalizesV1AndRejectedTerminal(t *testing.T) {
	data, err := os.ReadFile("testdata/m2-gate-v1.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// parseOps moved into internal/journal as part of the M3a Task 6
	// generalization; reload via OpenJournal from a scratch copy instead of
	// calling the (now generic, raw-line) parser directly.
	p := filepath.Join(t.TempDir(), "gate.jsonl")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write scratch copy: %v", err)
	}
	j, err := OpenJournal(p)
	if err != nil {
		t.Fatalf("fixture must parse: %v", err)
	}
	entries, err := Project(j.Ops())
	if err != nil {
		t.Fatal(err)
	}
	req := entries[0].Request
	if req.Subject != "workspace" || len(req.Bindings) != 2 {
		t.Fatalf("v1 request must normalize to subject=workspace + 2 bindings, got %+v", req)
	}
	var rejected *GateEntry
	for i := range entries {
		if entries[i].Record != nil && entries[i].Record.Decision == "rejected" {
			rejected = &entries[i]
		}
	}
	if rejected == nil || rejected.State != Rejected {
		t.Fatalf("rejected must be terminal state, got %+v", rejected)
	}
}

func TestGate1LegacyEmptyRoleStillValid(t *testing.T) {
	bs := []Binding{
		{Kind: "spec_manifest", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: "base_commit", Digest: "git:sha1:" + strings.Repeat("b", 40)},
	}
	if err := ValidateGate1Bindings(bs); err != nil {
		t.Fatalf("legacy role=\"\" must stay valid: %v", err)
	}
}
