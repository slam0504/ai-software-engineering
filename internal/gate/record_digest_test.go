package gate

import (
	"strings"
	"testing"
)

func TestRecordDigestDeterministicAndTamperEvident(t *testing.T) {
	rec := ApprovalRecord{Type: "approval_record", SchemaVersion: 2, ApprovalID: "01A",
		Gate: "gate2", Subject: "plan:M3A-001", Decision: "approved",
		Approver: Approver{ID: "u", Method: "app-local"}, Reason: "ok",
		Bindings: []Binding{{Kind: "plan", Digest: "sha256:" + strings.Repeat("a", 64)}},
		Metadata: &Metadata{RiskDecisions: []RiskDecision{{TaskID: "T1",
			MinimumRiskTier: "medium", PlannerRiskTier: "medium", SelectedRiskTier: "medium"}}},
		CreatedAt: "2026-08-12T00:00:00Z"}
	d1, err := RecordDigest(rec)
	if err != nil || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("digest: %v %q", err, d1)
	}
	d2, _ := RecordDigest(rec)
	if d1 != d2 {
		t.Fatal("digest must be deterministic")
	}
	rec.Metadata.RiskDecisions[0].SelectedRiskTier = "high" // metadata 竄改必須改變 digest（§3.4）
	d3, _ := RecordDigest(rec)
	if d3 == d1 {
		t.Fatal("metadata tamper must change digest")
	}
}
