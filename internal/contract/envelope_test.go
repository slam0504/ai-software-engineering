package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestULIDMonotonicAndSortable(t *testing.T) {
	t0 := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	a, b := NewULID(t0), NewULID(t0) // 同毫秒仍遞增
	c := NewULID(t0.Add(time.Second))
	if !(a < b && b < c) {
		t.Fatalf("ulid order broken: %s %s %s", a, b, c)
	}
	if len(a) != 26 || strings.ContainsAny(a, "ILOU") {
		t.Fatalf("bad ulid %s", a)
	}
}

func TestWrapFillsEnvelope(t *testing.T) {
	env := Wrap(Event{Provider: ProviderClaude, Kind: KindDelta, SessionID: "s1",
		Raw: []byte(`{"x":1}`), Text: "hi"}, "task-42")
	if env.EventID == "" || env.TS == "" {
		t.Fatal("event_id / ts must be filled")
	}
	if env.Provider != "claude" || env.Kind != string(KindDelta) ||
		env.SessionID != "s1" || env.TaskID != "task-42" || env.Text != "hi" {
		t.Fatalf("fields not mapped: %+v", env)
	}
	b, err := json.Marshal(env)
	if err != nil || !strings.Contains(string(b), `"task_id":"task-42"`) {
		t.Fatalf("marshal: %v %s", err, b)
	}
}

func TestWrapRolePrecedence(t *testing.T) {
	if env := Wrap(Event{Provider: ProviderCodex, Kind: KindMessage, Role: "tool", Raw: []byte(`{}`)}, ""); env.Role != "tool" {
		t.Fatalf("explicit role must win: %q", env.Role)
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "assistant", Raw: []byte(`{}`)}, ""); env.Role != "assistant" {
		t.Fatal("assistant role must pass through")
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte(`{}`)}, ""); env.Role != "assistant" {
		t.Fatal("delta fallback must be assistant")
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindMessage, Raw: []byte(`{}`)}, ""); env.Role != "system" {
		t.Fatal("unlabelled message must fallback to system, not assistant")
	}
}

func TestWrapMalformedRawStillMarshals(t *testing.T) {
	ev := Event{Provider: ProviderClaude, Kind: KindMalformed,
		Raw: []byte(`{"type":"resul`), Err: errors.New("unexpected EOF")}
	env := Wrap(ev, "")
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("envelope with invalid raw must marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	var s string
	if err := json.Unmarshal(back.Raw, &s); err != nil || !strings.Contains(s, "resul") {
		t.Fatalf("raw fallback = %s (%v)", back.Raw, err)
	}
	if back.Error == "" {
		t.Fatal("error string must survive")
	}
}

func TestWrapValidRawKeptVerbatim(t *testing.T) {
	env := Wrap(Event{Provider: ProviderCodex, Kind: KindMessage, Raw: []byte(`{"a":1}`)}, "")
	if string(env.Raw) != `{"a":1}` {
		t.Fatalf("valid raw must be verbatim: %s", env.Raw)
	}
}

func TestEnvelopeWorkspaceFieldsOmitemptyAndPopulated(t *testing.T) {
	empty := Envelope{EventID: "e1", TS: "t1", Kind: "message"}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"scope"`, `"bindings"`, `"payload"`, `"correlation_id"`, `"purpose"`} {
		if strings.Contains(string(b), key) {
			t.Fatalf("zero-value additive field must be omitted, found %s in %s", key, b)
		}
	}

	filled := Envelope{
		EventID: "e2", TS: "t2", Kind: string(KindGateRequest),
		Scope:         "workspace",
		Bindings:      []Binding{{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:x"}},
		Payload:       json.RawMessage(`{"approval_id":"A"}`),
		CorrelationID: "A",
		Purpose:       "gate approval",
	}
	fb, err := json.Marshal(filled)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(fb, &back); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if back.Scope != "workspace" || len(back.Bindings) != 1 ||
		back.Bindings[0].Kind != "spec_manifest" || back.Bindings[0].Ref != "spec/" || back.Bindings[0].Digest != "sha256:x" ||
		back.CorrelationID != "A" || back.Purpose != "gate approval" {
		t.Fatalf("workspace fields not roundtripped: %+v", back)
	}
	if !strings.Contains(string(fb), `"kind":"gate_request"`) {
		t.Fatalf("KindGateRequest constant mismatch: %s", fb)
	}
	if KindBindingStale != "binding_stale" {
		t.Fatalf("KindBindingStale constant mismatch: %q", KindBindingStale)
	}
}

func TestEnvelopeCarriesWorkspaceSessionID(t *testing.T) {
	b, err := json.Marshal(Envelope{EventID: "e1", Kind: "message", WorkspaceSessionID: "01JWSID"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"workspace_session_id":"01JWSID"`) {
		t.Fatalf("欄位未序列化: %s", b)
	}
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil || back.WorkspaceSessionID != "01JWSID" {
		t.Fatalf("round-trip 失敗: %v %+v", err, back)
	}
	b2, _ := json.Marshal(Envelope{EventID: "e2", Kind: "message"})
	if strings.Contains(string(b2), "workspace_session_id") {
		t.Fatalf("空值不應序列化（舊 journal 相容）: %s", b2)
	}
}
