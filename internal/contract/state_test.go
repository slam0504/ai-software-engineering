package contract

import "testing"

func applyKind(t *testing.T, r *Reducer, kind Kind, isErr bool, role string, want SessionState) {
	t.Helper()
	got, _ := r.Apply(Event{Provider: ProviderClaude, Kind: kind, IsError: isErr, Role: role, Raw: []byte("{}")})
	if got != want {
		t.Fatalf("%s(role=%q,isErr=%v) -> %s, want %s", kind, role, isErr, got, want)
	}
}

func TestReducerHappyPath(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindInit, false, "", StateIdle)
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindToolUse, false, "", StateToolRunning)
	applyKind(t, r, KindApproval, false, "", StateAwaitingApproval)
	if st, changed := r.ResolveApproval(); st != StateToolRunning || !changed {
		t.Fatalf("allow resolve -> %s", st)
	}
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindResult, false, "", StateDone)
}

func TestReducerFailedResult(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindResult, true, "", StateFailed)
}

func TestReducerMalformedIsNonTerminal(t *testing.T) { // M0 定義：malformed 不中斷
	r := NewReducer()
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMalformed, Raw: []byte("x")}); changed {
		t.Fatal("malformed must not change state")
	}
	applyKind(t, r, KindStreamError, false, "", StateFailed)
}

func TestReducerUserMessageEntersWaiting(t *testing.T) { // 第二輪送出不得停在 done
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindResult, Raw: []byte("{}")})
	got, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "user", Raw: []byte("{}")})
	if got != StateWaiting || !changed {
		t.Fatalf("user message -> %s", got)
	}
	if st, _ := r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")}); st != StateStreaming {
		t.Fatal("waiting -> streaming on first delta")
	}
}

func TestReducerToolEchoIsNeutral(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "tool", Raw: []byte("{}")}); changed {
		t.Fatal("tool echo must be neutral")
	}
}

func TestReducerDenyAndTimeoutResolve(t *testing.T) {
	for range 2 {
		r := NewReducer()
		r.Apply(Event{Provider: ProviderClaude, Kind: KindApproval, Raw: []byte("{}")})
		if st, _ := r.ResolveApproval(); st != StateToolRunning {
			t.Fatalf("resolve -> %s", st)
		}
	}
}

func TestReducerResolveOutsideApprovalIsNoop(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	if _, changed := r.ResolveApproval(); changed {
		t.Fatal("resolve without pending approval must be noop")
	}
}

func TestReducerResetForNewSession(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindStreamError, Raw: []byte("{}")})
	r.Reset()
	if r.Current() != StateIdle {
		t.Fatalf("after reset = %s", r.Current())
	}
}

func TestReducerRetryRecovers(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindRetry, false, "", StateRetrying)
	applyKind(t, r, KindDelta, false, "", StateStreaming)
}

func TestReducerNeutralKinds(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	for _, k := range []Kind{KindSystemOther, KindUnknown, KindUsage, KindApprovalDecision} {
		if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: k, Raw: []byte("{}")}); changed {
			t.Fatalf("%s must be neutral", k)
		}
	}
}
