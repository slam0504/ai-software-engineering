package codex

import (
	"encoding/json"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func TestMapEventBranchesOnItemType(t *testing.T) { // v1.5：item 事件依 params.item.type 二級分流
	cases := []struct {
		name         string
		method       string
		params       string
		want         contract.Kind
		text         string
		thinking     string
	}{
		{"cmd_started", MethodItemStarted, `{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"ls"}}`, contract.KindToolUse, "", ""},
		{"cmd_completed", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i1","type":"commandExecution","status":"completed"}}`, contract.KindToolUse, "", ""},
		{"file_change", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i2","type":"fileChange"}}`, contract.KindToolUse, "", ""},
		{"mcp_tool", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i3","type":"mcpToolCall"}}`, contract.KindToolUse, "", ""},
		{"web_search", MethodItemStarted, `{"threadId":"t1","item":{"id":"i4","type":"webSearch"}}`, contract.KindToolUse, "", ""},
		{"agent_msg_completed", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i5","type":"agentMessage","text":"hello"}}`, contract.KindMessage, "hello", ""},
		{"agent_msg_started", MethodItemStarted, `{"threadId":"t1","item":{"id":"i5","type":"agentMessage"}}`, contract.KindSystemOther, "", ""},
		{"user_msg", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i6","type":"userMessage","text":"hi"}}`, contract.KindMessage, "hi", ""},
		{"reasoning", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i7","type":"reasoning","summary":"thinking hard"}}`, contract.KindMessage, "", "thinking hard"},
		{"plan", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i8","type":"plan"}}`, contract.KindSystemOther, "", ""},
		{"context_compaction", MethodItemStarted, `{"threadId":"t1","item":{"id":"i9","type":"contextCompaction"}}`, contract.KindSystemOther, "", ""},
		{"unknown_item_type", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i10","type":"banana"}}`, contract.KindUnknown, "", ""},
		{"delta", MethodAgentMessageDelta, `{"threadId":"t1","itemId":"i5","delta":"chunk"}`, contract.KindDelta, "chunk", ""},
		{"turn_completed_ok", MethodTurnCompleted, `{"threadId":"t1","turn":{"id":"turn1","status":"completed"}}`, contract.KindResult, "", ""},
		{"turn_diff", MethodTurnDiffUpdated, `{"threadId":"t1","diff":"..."}`, contract.KindSystemOther, "", ""},
		{"unknown_method", "future/method", `{"threadId":"t1"}`, contract.KindUnknown, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := MapEvent(c.method, json.RawMessage(c.params))
			if ev.Kind != c.want {
				t.Fatalf("kind = %s, want %s", ev.Kind, c.want)
			}
			if !ev.Valid() {
				t.Fatalf("event must satisfy contract.Valid: %+v", ev)
			}
			if ev.Provider != contract.ProviderCodex {
				t.Fatal("provider must be codex")
			}
			if ev.SessionID != "t1" {
				t.Fatalf("sessionID = %q, want t1", ev.SessionID)
			}
			if ev.Text != c.text {
				t.Fatalf("text = %q, want %q", ev.Text, c.text)
			}
			if ev.Thinking != c.thinking {
				t.Fatalf("thinking = %q, want %q", ev.Thinking, c.thinking)
			}
		})
	}
	if ev := MapEvent(MethodTurnCompleted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn1","status":"failed","error":{"message":"x"}}}`)); !ev.IsError {
		t.Fatal("failed turn must map IsError")
	}
}
