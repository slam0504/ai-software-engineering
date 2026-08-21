package appcore

import (
	"encoding/json"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// nopSink：EmitWorkspace 測試用的最小 AuditSink 實作（Write 一律成功）。
type nopSink struct{}

func (nopSink) Write(contract.Envelope) (AppendReceipt, error) { return AppendReceipt{}, nil }
func (nopSink) Close() error                                   { return nil }

func TestEmitWorkspaceScopedAndNoSlot(t *testing.T) {
	var got []contract.Envelope
	m := New(Config{Emit: func(e contract.Envelope) { got = append(got, e) },
		Sink: nopSink{}})
	m.EmitWorkspace("gate_request",
		[]contract.Binding{{Kind: "spec_manifest", Ref: "spec/", Digest: "sha256:x"}},
		map[string]string{"approval_id": "A"})
	if len(got) != 1 {
		t.Fatalf("want 1 workspace envelope, got %d", len(got))
	}
	e := got[0]
	if e.Scope != "workspace" || e.Provider != "" || e.SessionID != "" {
		t.Fatalf("workspace event must omit provider/session_id, got %+v", e)
	}
	if len(m.slots) != 0 {
		t.Fatalf("EmitWorkspace must not allocate or touch any provider slot; got %d slot(s)", len(m.slots))
	}
	if e.Kind != "gate_request" || len(e.Bindings) != 1 {
		t.Fatal("kind/bindings must be top-level")
	}
	var p map[string]string
	_ = json.Unmarshal(e.Payload, &p)
	if p["approval_id"] != "A" {
		t.Fatal("payload must carry approval_id, not Text")
	}
	if e.Text != "" {
		t.Fatal("must not stuff data into Text")
	}
}
