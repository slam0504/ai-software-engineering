package claude

import (
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
