package gate

import (
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/spec"
)

func TestGate1PolicyReconcile(t *testing.T) {
	cur := "sha256:" + strings.Repeat("c", 64)
	p := NewGate1Policy(func() (string, error) { return cur, nil })
	rec := ApprovalRecord{Gate: "gate1", Bindings: []Binding{
		{Kind: "spec_manifest", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	causes, err := p.ReconcileBindings(rec)
	if err != nil || len(causes) != 1 || causes[0].Cause != "spec_manifest changed" {
		t.Fatalf("expected stale cause, got %v %v", causes, err)
	}
	perr := NewGate1Policy(func() (string, error) { return "", spec.ErrConcurrentModification })
	if _, err := perr.ReconcileBindings(rec); err == nil {
		t.Fatal("read error must fail closed, not stale") // §3.9
	}
}
