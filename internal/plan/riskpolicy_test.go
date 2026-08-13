package plan

import (
	"strings"
	"testing"
)

func taskWithImpact(contexts, modules []string) Task {
	t := Task{ID: "T1", Title: "T"}
	t.Impact.Contexts = contexts
	t.Impact.Modules = modules
	return t
}

func TestComputeMinimum(t *testing.T) {
	pol := RiskPolicy{
		Version:     1,
		DefaultTier: "low",
	}
	pol.Rules = append(pol.Rules, struct {
		Match struct {
			Contexts []string `yaml:"contexts" json:"contexts"`
			Modules  []string `yaml:"modules" json:"modules"`
		} `yaml:"match" json:"match"`
		Tier string `yaml:"tier" json:"tier"`
	}{})
	pol.Rules[0].Match.Contexts = []string{"gate"}
	pol.Rules[0].Tier = "high"

	pol.Rules = append(pol.Rules, pol.Rules[0])
	pol.Rules[1].Match.Contexts = nil
	pol.Rules[1].Match.Modules = []string{"billing"}
	pol.Rules[1].Tier = "medium"

	cases := []struct {
		name string
		task Task
		want string
	}{
		{"no impact, no match -> default", taskWithImpact(nil, nil), "low"},
		{"context match -> rule tier", taskWithImpact([]string{"gate"}, nil), "high"},
		{"module match -> rule tier", taskWithImpact(nil, []string{"billing"}), "medium"},
		{"both rules match -> highest tier wins", taskWithImpact([]string{"gate"}, []string{"billing"}), "high"},
		{"non-overlapping impact -> default", taskWithImpact([]string{"other"}, []string{"other-mod"}), "low"},
		{"partial module match only -> matching rule tier", taskWithImpact(nil, []string{"billing", "other-mod"}), "medium"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pol.ComputeMinimum(c.task); got != c.want {
				t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestParseRiskPolicy(t *testing.T) {
	b := []byte("version: 1\ndefault_tier: low\nrules:\n  - match:\n      contexts: [gate]\n    tier: high\n")
	pol, err := ParseRiskPolicy(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pol.Version != 1 || pol.DefaultTier != "low" || len(pol.Rules) != 1 || pol.Rules[0].Tier != "high" {
		t.Fatalf("unexpected parse result: %+v", pol)
	}
}

func TestParseRiskPolicyRejectsUnknownField(t *testing.T) {
	_, err := ParseRiskPolicy([]byte("version: 1\ndefault_tier: low\nbogus_field: true\n"))
	if err == nil {
		t.Fatal("unknown field in risk policy must be rejected")
	}
}

// TestParseRiskPolicyRejectsUnknownRuleTier guards against a broken policy
// file silently poisoning ComputeMinimum: without this check, an illegal
// rule tier makes ComputeMinimum return "", and Validate then reports the
// task's minimum_risk_tier as "not matching recomputed value" — pointing
// diagnosis at the plan/task when the real defect is the policy file.
func TestParseRiskPolicyRejectsUnknownRuleTier(t *testing.T) {
	b := []byte("version: 1\ndefault_tier: low\nrules:\n  - match:\n      contexts: [gate]\n    tier: critical\n")
	_, err := ParseRiskPolicy(b)
	if err == nil {
		t.Fatal("unknown rule tier must be rejected")
	}
	if !strings.Contains(err.Error(), "risk policy") || !strings.Contains(err.Error(), "rule") {
		t.Fatalf("error must point at the risk policy rule, not the task: %v", err)
	}
	if strings.Contains(err.Error(), "task") {
		t.Fatalf("error must not blame the task: %v", err)
	}
}

// TestParseRiskPolicyRejectsUnknownDefaultTier mirrors the rule-tier case
// for an illegal default_tier — same fail-loud-at-load-time reasoning.
func TestParseRiskPolicyRejectsUnknownDefaultTier(t *testing.T) {
	b := []byte("version: 1\ndefault_tier: critical\n")
	_, err := ParseRiskPolicy(b)
	if err == nil {
		t.Fatal("unknown default_tier must be rejected")
	}
	if !strings.Contains(err.Error(), "risk policy") || !strings.Contains(err.Error(), "default_tier") {
		t.Fatalf("error must point at the risk policy default_tier, not the task: %v", err)
	}
	if strings.Contains(err.Error(), "task") {
		t.Fatalf("error must not blame the task: %v", err)
	}
}
