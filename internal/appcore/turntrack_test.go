package appcore

import (
	"strings"
	"testing"
)

// review P1 迴歸（M0 遷入）：turn/interrupt 必填 threadId + turnId，
// Terminate 的參數必須由 turn/started 追蹤而來。
func TestCodexInterruptParamsLifecycle(t *testing.T) {
	var tr TurnTrack

	if _, err := tr.InterruptParams(); err == nil {
		t.Fatal("no active turn must refuse interrupt (missing turnId)")
	}

	tr.NoteStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-9","items":[],"status":"inProgress"}}`))
	params, err := tr.InterruptParams()
	if err != nil {
		t.Fatal(err)
	}
	if params["threadId"] != "t1" || params["turnId"] != "turn-9" {
		t.Fatalf("interrupt params = %v, want threadId+turnId", params)
	}

	tr.NoteEnded()
	if _, err := tr.InterruptParams(); err == nil || !strings.Contains(err.Error(), "turnId") {
		t.Fatalf("after turn/completed interrupt must refuse again, got %v", err)
	}
}

func TestParseTurnStarted(t *testing.T) {
	th, tu := ParseTurnStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-9"}}`))
	if th != "t1" || tu != "turn-9" {
		t.Fatalf("parse = %q %q", th, tu)
	}
	if th, tu := ParseTurnStarted([]byte(`not json`)); th != "" || tu != "" {
		t.Fatalf("malformed params must yield empty ids, got %q %q", th, tu)
	}
}
