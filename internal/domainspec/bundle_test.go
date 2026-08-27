package domainspec

import (
	"bytes"
	"strings"
	"testing"
)

func miniBundle(extra string) []byte {
	return []byte(`schema_version: 1
rules:
  - id: RA
    phase: decide
    when: "decision == 'approved'"
    effect: deny
    target: decision.eligibility
    priority: 10
    verdict: "RA fired"
    refs: "test"
    step_rank: 2
    stage: none
` + extra)
}

func TestLoadBundleOK(t *testing.T) {
	b, err := LoadBundle(miniBundle(""), 1_000_000)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !b.Rules[0].RefVars["decision"] {
		t.Fatalf("RefVars must capture referenced fact vars: %+v", b.Rules[0].RefVars)
	}
	if !strings.HasPrefix(b.Digest, "sha256:") {
		t.Fatal("bundle digest required")
	}
}

func TestLoadBundleRejectsTypeError(t *testing.T) {
	bad := bytes.Replace(miniBundle(""), []byte(`decision == 'approved'`), []byte(`decision + 1 == 2`), 1)
	if _, err := LoadBundle(bad, 1_000_000); err == nil {
		t.Fatal("type-check must reject string+int（出口 2）")
	}
}

func TestLoadBundleRejectsStaticCostOverLimit(t *testing.T) {
	if _, err := LoadBundle(miniBundle(""), 0); err == nil {
		t.Fatal("static cost over limit must be rejected（出口 2）")
	}
}

func TestLoadBundleRejectsCrossPhaseDependsOn(t *testing.T) {
	extra := `  - id: RB
    phase: submit
    when: "request.gate == 'gate2'"
    effect: deny
    target: decision.eligibility
    depends_on: [RA]
    priority: 10
    verdict: "RB"
    refs: "test"
    step_rank: 0
    stage: none
`
	if _, err := LoadBundle(miniBundle(extra), 1_000_000); err == nil {
		t.Fatal("cross-phase depends_on must be rejected at load（spec rev5／出口 2）")
	}
}

func TestLoadBundleRejectsCycle(t *testing.T) {
	extra := `  - id: RC
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [RD]
    priority: 10
    verdict: "RC"
    refs: "test"
    step_rank: 2
    stage: none
  - id: RD
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [RC]
    priority: 10
    verdict: "RD"
    refs: "test"
    step_rank: 2
    stage: none
`
	if _, err := LoadBundle(miniBundle(extra), 1_000_000); err == nil {
		t.Fatal("dependency cycle must be rejected")
	}
}

func TestLoadBundleRejectsUnknownYAMLField(t *testing.T) {
	bad := bytes.Replace(miniBundle(""), []byte("verdict:"), []byte("bogus: x\n    verdict:"), 1)
	if _, err := LoadBundle(bad, 1_000_000); err == nil {
		t.Fatal("unknown YAML field must be rejected")
	}
}

func TestLoadBundleRequiredKinds(t *testing.T) {
	// plan rev4：validated RequiredKinds 必須進 CompiledBundle（Evaluate 的實體化輸入）
	withKinds := append([]byte(`required_kinds:
  - kind: spec_manifest
    pattern: "^sha256:[0-9a-f]{64}$"
  - kind: base_commit
    pattern: "^git:(sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$"
`), miniBundle("")...)
	b, err := LoadBundle(withKinds, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.RequiredKinds) != 2 || b.RequiredKinds[0].Kind != "spec_manifest" {
		t.Fatalf("RequiredKinds must be preserved in order: %+v", b.RequiredKinds)
	}
	// kind 重複 → 拒；pattern 非法 regexp → 拒
	dup := bytes.Replace(withKinds, []byte("kind: base_commit"), []byte("kind: spec_manifest"), 1)
	if _, err := LoadBundle(dup, 1_000_000); err == nil {
		t.Fatal("duplicate required kind must be rejected")
	}
	badRe := bytes.Replace(withKinds, []byte(`"^sha256:[0-9a-f]{64}$"`), []byte(`"["`), 1)
	if _, err := LoadBundle(badRe, 1_000_000); err == nil {
		t.Fatal("invalid pattern regexp must be rejected")
	}
}
