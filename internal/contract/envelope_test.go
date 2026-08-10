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
