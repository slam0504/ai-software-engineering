package codex

import (
	"encoding/json"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func TestMapEventBranchesOnItemType(t *testing.T) { // item 事件依 params.item.type 二級分流
	cases := []struct {
		name     string
		method   string
		params   string
		want     contract.Kind
		text     string
		thinking string
		role     string
	}{
		{"cmd_started", MethodItemStarted, `{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"ls"}}`, contract.KindToolUse, "ls", "", ""},
		{"cmd_completed", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"echo hi","status":"completed"}}`, contract.KindToolUse, "echo hi", "", ""},
		{"file_change", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i2","type":"fileChange"}}`, contract.KindToolUse, "fileChange", "", ""},
		{"mcp_tool", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i3","type":"mcpToolCall"}}`, contract.KindToolUse, "mcpToolCall", "", ""},
		{"web_search", MethodItemStarted, `{"threadId":"t1","item":{"id":"i4","type":"webSearch"}}`, contract.KindToolUse, "webSearch", "", ""},
		{"agent_msg_completed", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i5","type":"agentMessage","text":"hello"}}`, contract.KindMessage, "hello", "", "assistant"},
		{"agent_msg_started", MethodItemStarted, `{"threadId":"t1","item":{"id":"i5","type":"agentMessage"}}`, contract.KindSystemOther, "", "", ""},
		{"user_msg", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i6","type":"userMessage","text":"hi"}}`, contract.KindMessage, "hi", "", "tool"},
		{"reasoning", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i7","type":"reasoning","summary":"thinking hard"}}`, contract.KindMessage, "", "thinking hard", "assistant"},
		{"plan", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i8","type":"plan"}}`, contract.KindSystemOther, "", "", ""},
		{"context_compaction", MethodItemStarted, `{"threadId":"t1","item":{"id":"i9","type":"contextCompaction"}}`, contract.KindSystemOther, "", "", ""},
		{"unknown_item_type", MethodItemCompleted, `{"threadId":"t1","item":{"id":"i10","type":"banana"}}`, contract.KindUnknown, "", "", ""},
		{"delta", MethodAgentMessageDelta, `{"threadId":"t1","itemId":"i5","delta":"chunk"}`, contract.KindDelta, "chunk", "", ""},
		{"turn_completed_ok", MethodTurnCompleted, `{"threadId":"t1","turn":{"id":"turn1","status":"completed"}}`, contract.KindResult, "", "", ""},
		{"turn_diff", MethodTurnDiffUpdated, `{"threadId":"t1","diff":"..."}`, contract.KindSystemOther, "", "", ""},
		{"token_usage", MethodThreadTokenUsageUpdated, `{"threadId":"t1","tokenUsage":{"total":{"totalTokens":23058,"inputTokens":22986,"cachedInputTokens":11008,"outputTokens":72}}}`, contract.KindUsage, "", "", ""},
		{"rate_limits", MethodAccountRateLimitsUpdated, `{"threadId":"t1"}`, contract.KindSystemOther, "", "", ""},
		{"unknown_method", "future/method", `{"threadId":"t1"}`, contract.KindUnknown, "", "", ""},
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
			if ev.Role != c.role {
				t.Fatalf("role = %q, want %q", ev.Role, c.role)
			}
		})
	}
	if ev := MapEvent(MethodTurnCompleted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn1","status":"failed","error":{"message":"x"}}}`)); !ev.IsError {
		t.Fatal("failed turn must map IsError")
	}
	if ev := MapEvent(MethodThreadTokenUsageUpdated, json.RawMessage(`{"threadId":"t1","tokenUsage":{"total":{"inputTokens":10,"cachedInputTokens":4,"outputTokens":2}}}`)); ev.Usage == nil || ev.Usage.InputTokens != 10 || ev.Usage.OutputTokens != 2 || ev.Usage.CachedInput != 4 {
		t.Fatalf("codex usage = %+v", ev.Usage)
	}
}

func TestMapEventToolText(t *testing.T) { // per-type 摘要（雙 provider 強制）
	cmd := MapEvent(MethodItemCompleted, json.RawMessage(`{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"echo hi","status":"completed"}}`))
	if cmd.Kind != contract.KindToolUse || cmd.Text != "echo hi" {
		t.Fatalf("command text: %s %q", cmd.Kind, cmd.Text)
	}
	mcp := MapEvent(MethodItemStarted, json.RawMessage(`{"threadId":"t1","item":{"id":"i2","type":"mcpToolCall","server":"github","tool":"create_issue","arguments":{"title":"x"}}}`))
	if mcp.Text != `github.create_issue({"title":"x"})` {
		t.Fatalf("mcp summary: %q", mcp.Text)
	}
	ws := MapEvent(MethodItemStarted, json.RawMessage(`{"threadId":"t1","item":{"id":"i3","type":"webSearch","query":"golang mutex"}}`))
	if ws.Text != "webSearch(golang mutex)" {
		t.Fatalf("webSearch summary: %q", ws.Text)
	}
	fc := MapEvent(MethodItemCompleted, json.RawMessage(`{"threadId":"t1","item":{"id":"i4","type":"fileChange","changes":[{"path":"a.go"},{"path":"b.go"}]}}`))
	if fc.Text != "fileChange(a.go +1)" {
		t.Fatalf("fileChange summary: %q", fc.Text)
	}
	// MCP 必要欄位各自缺失 → 一律型別名 fallback（無半成品摘要）
	for name, params := range map[string]string{
		"all_missing":  `{"threadId":"t1","item":{"id":"i5","type":"mcpToolCall"}}`,
		"no_server":    `{"threadId":"t1","item":{"id":"i6","type":"mcpToolCall","tool":"create_issue","arguments":{"a":1}}}`,
		"no_tool":      `{"threadId":"t1","item":{"id":"i7","type":"mcpToolCall","server":"github","arguments":{"a":1}}}`,
		"no_arguments": `{"threadId":"t1","item":{"id":"i8","type":"mcpToolCall","server":"github","tool":"create_issue"}}`,
	} {
		if ev := MapEvent(MethodItemStarted, json.RawMessage(params)); ev.Text != "mcpToolCall" {
			t.Fatalf("%s must fallback to type name, got %q", name, ev.Text)
		}
	}
}
