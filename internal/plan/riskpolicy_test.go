package plan

import "testing"

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
