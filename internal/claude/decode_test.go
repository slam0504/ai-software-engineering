package claude

import (
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		name string
		line string
		want contract.Kind
	}{
		{"init", `{"type":"system","subtype":"init","session_id":"abc-123","model":"m","capabilities":["interrupt_receipt_v1"],"mcp_servers":[{"name":"workbench","status":"connected"}]}`, contract.KindInit},
		{"init_mcp_error", `{"type":"system","subtype":"init","session_id":"abc-123","mcp_servers":[],"mcp_server_errors":[{"name":"workbench","type":"invalid_config","message":"bad"}]}`, contract.KindInit},
		{"text_delta", `{"type":"stream_event","session_id":"abc-123","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}`, contract.KindDelta},
		{"thinking_delta", `{"type":"stream_event","session_id":"abc-123","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}}`, contract.KindDelta},
		{"assistant", `{"type":"assistant","session_id":"abc-123","message":{"role":"assistant","content":[]}}`, contract.KindMessage},
		{"user", `{"type":"user","session_id":"abc-123","message":{"role":"user","content":[]}}`, contract.KindMessage},
		{"result_ok", `{"type":"result","subtype":"success","session_id":"abc-123","result":"done","total_cost_usd":0.012,"is_error":false}`, contract.KindResult},
		{"result_err", `{"type":"result","subtype":"error_during_execution","session_id":"abc-123","is_error":true}`, contract.KindResult},
		{"api_retry", `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":2000,"error_status":529,"error":"overloaded","uuid":"u1","session_id":"abc-123"}`, contract.KindRetry},
		{"system_other", `{"type":"system","subtype":"plugin_install","status":"started"}`, contract.KindSystemOther},
		{"rate_limit", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"},"session_id":"abc-123"}`, contract.KindSystemOther},
		{"unknown", `{"type":"banana","x":1}`, contract.KindUnknown},
		{"malformed", `{"type":"resul`, contract.KindMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Decode([]byte(c.line))
			if ev.Kind != c.want {
				t.Fatalf("kind = %s, want %s", ev.Kind, c.want)
			}
			if ev.Provider != contract.ProviderClaude || string(ev.Raw) != c.line {
				t.Fatal("provider / raw not preserved")
			}
			if !ev.Valid() {
				t.Fatal("decoded event must satisfy contract.Valid")
			}
		})
	}
	if ev := Decode([]byte(cases[2].line)); ev.Text != "Hel" {
		t.Fatalf("text = %q", ev.Text)
	}
	if ev := Decode([]byte(cases[3].line)); ev.Thinking != "hm" {
		t.Fatalf("thinking = %q", ev.Thinking)
	}
	if ev := Decode([]byte(cases[7].line)); !ev.IsError {
		t.Fatal("is_error not mapped")
	}
	init := ParseInit(Decode([]byte(cases[1].line)))
	if init == nil || len(init.MCPServerErrors) != 1 || init.MCPServerErrors[0].Type != "invalid_config" {
		t.Fatalf("init parse: %+v", init)
	}
}

func TestDecodeResultUsage(t *testing.T) {
	line := `{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0.1,"usage":{"input_tokens":4,"output_tokens":714,"cache_read_input_tokens":84414}}`
	ev := Decode([]byte(line))
	if ev.Usage == nil || ev.Usage.InputTokens != 4 || ev.Usage.OutputTokens != 714 || ev.Usage.CachedInput != 84414 {
		t.Fatalf("usage = %+v", ev.Usage)
	}
}

func TestDecodeRoles(t *testing.T) {
	a := Decode([]byte(`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))
	if a.Role != "assistant" {
		t.Fatalf("assistant role = %q", a.Role)
	}
	u := Decode([]byte(`{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`))
	if u.Role != "tool" {
		t.Fatalf("user-echo role = %q", u.Role)
	}
}

func TestDecodeAssistantToolUseSummary(t *testing.T) {
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"touch x"}}]}}`
	ev := Decode([]byte(line))
	if ev.Kind != contract.KindToolUse {
		t.Fatalf("tool-only assistant must map KindToolUse, got %s", ev.Kind)
	}
	if !strings.Contains(ev.Text, "Bash") || !strings.Contains(ev.Text, "touch x") {
		t.Fatalf("text must carry name+input excerpt: %q", ev.Text)
	}
	mixed := Decode([]byte(`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","input":{}}]}}`))
	if mixed.Kind != contract.KindMessage || mixed.Text != "hi" { // 含 text 維持 M0 行為
		t.Fatalf("mixed content must stay message: %s %q", mixed.Kind, mixed.Text)
	}
}

func TestDecodeToolSummaryTruncationAndMulti(t *testing.T) { // 80-rune 截斷、+N
	long := strings.Repeat("字", 100)
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","name":"Write","input":{"path":"` + long + `"}},` +
		`{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	ev := Decode([]byte(line))
	if ev.Kind != contract.KindToolUse {
		t.Fatalf("kind = %s", ev.Kind)
	}
	if !strings.Contains(ev.Text, "Write(") || !strings.Contains(ev.Text, "…") {
		t.Fatalf("long input must be truncated with ellipsis: %q", ev.Text)
	}
	if !strings.HasSuffix(ev.Text, " +1") {
		t.Fatalf("multi-tool must append +N: %q", ev.Text)
	}
	inner := ev.Text[strings.Index(ev.Text, "(")+1 : strings.LastIndex(ev.Text, ")")]
	if n := len([]rune(inner)); n > 81 { // 節錄總長上限 81 rune（80 內容 + 刪節號）
		t.Fatalf("excerpt too long: %d runes (cap 81)", n)
	}
}

func TestDecodeMessageKeepsText(t *testing.T) { // review P2：完成訊息不得只剩 placeholder
	line := `{"type":"assistant","session_id":"abc-123","message":{"role":"assistant","content":[{"type":"text","text":"first "},{"type":"tool_use","name":"Bash"},{"type":"text","text":"second"}]}}`
	ev := Decode([]byte(line))
	if ev.Kind != contract.KindMessage || ev.Text != "first second" {
		t.Fatalf("kind=%s text=%q, want message text concatenated", ev.Kind, ev.Text)
	}
}
