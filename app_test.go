package main

import (
	"strings"
	"testing"
)

// review P1 迴歸：turn/interrupt 必填 threadId + turnId（TurnInterruptParams.json），
// Terminate 的參數必須由 turn/started 追蹤而來。
func TestCodexInterruptParamsLifecycle(t *testing.T) {
	a := NewApp()

	if _, err := a.codexInterruptParams(); err == nil {
		t.Fatal("no active turn must refuse interrupt (missing turnId)")
	}

	a.noteTurnStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-9","items":[],"status":"inProgress"}}`))
	params, err := a.codexInterruptParams()
	if err != nil {
		t.Fatal(err)
	}
	if params["threadId"] != "t1" || params["turnId"] != "turn-9" {
		t.Fatalf("interrupt params = %v, want threadId+turnId", params)
	}

	a.noteTurnEnded()
	if _, err := a.codexInterruptParams(); err == nil || !strings.Contains(err.Error(), "turnId") {
		t.Fatalf("after turn/completed interrupt must refuse again, got %v", err)
	}
}

func TestParseTurnStarted(t *testing.T) {
	th, tu := parseTurnStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-9"}}`))
	if th != "t1" || tu != "turn-9" {
		t.Fatalf("parse = %q %q", th, tu)
	}
	if th, tu := parseTurnStarted([]byte(`not json`)); th != "" || tu != "" {
		t.Fatalf("malformed params must yield empty ids, got %q %q", th, tu)
	}
}
