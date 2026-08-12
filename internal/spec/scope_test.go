package spec

import "testing"

// TestSpecAndPlanScopeDigestDiffer guards the Task 7 契約：canonical manifest
// 內容必須含 scope patterns，讓不同 scope 對同一組 entries 產生不同 digest —
// 否則 spec scope 誤讀 plan 內容（或反之）不會被 STALE 偵測到。
func TestSpecAndPlanScopeDigestDiffer(t *testing.T) {
	entries := []FileEntry{{Path: "shared.yaml", SHA256: "aa"}}
	specDigest, err := SpecScope.ManifestDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := PlanScope.ManifestDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if specDigest == planDigest {
		t.Fatalf("SpecScope and PlanScope must digest differently: both got %s", specDigest)
	}
}

func TestPlanScopeMatch(t *testing.T) {
	if !PlanScope.Match("plan/M3A-001.yaml") {
		t.Error("want in-scope: plan/M3A-001.yaml")
	}
	if PlanScope.Match("spec/features/x.feature") {
		t.Error("want out-of-scope: spec/features/x.feature")
	}
}
