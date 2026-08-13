package plan

import (
	"errors"
	"testing"
)

func validPlan() Plan {
	t1 := Task{
		ID:              "T1",
		Title:           "Task One",
		Scenarios:       []string{"E1"},
		DependsOn:       nil,
		MinimumRiskTier: "medium",
		PlannerRiskTier: "medium",
	}
	return Plan{
		PlanID: "P1",
		Tasks:  []Task{t1},
	}
}

func TestValidateRejects(t *testing.T) {
	pol := RiskPolicy{Version: 1, DefaultTier: "medium"}
	base := validPlan() // helper：單 task T1、planner=minimum=medium、scenario E1
	scen := map[string]bool{"E1": true}
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"cycle", func(p *Plan) { p.Tasks[0].DependsOn = []string{"T1"} }},
		{"missing dep", func(p *Plan) { p.Tasks[0].DependsOn = []string{"T9"} }},
		{"dup id", func(p *Plan) { p.Tasks = append(p.Tasks, p.Tasks[0]) }},
		{"planner below minimum", func(p *Plan) { p.Tasks[0].PlannerRiskTier = "low" }},
		{"minimum mismatch", func(p *Plan) { p.Tasks[0].MinimumRiskTier = "low" }}, // 重算=medium
		{"unknown scenario", func(p *Plan) { p.Tasks[0].Scenarios = []string{"E9"} }},
	}
	for _, c := range cases {
		p := base
		p.Tasks = append([]Task(nil), base.Tasks...)
		c.mutate(&p)
		if errs := Validate(p, pol, scen); len(errs) == 0 {
			t.Fatalf("%s must be rejected", c.name)
		}
	}
	if errs := Validate(base, pol, scen); len(errs) != 0 {
		t.Fatalf("valid plan must pass: %v", errs)
	}
}

// TestValidateRiskErrorsWrapSentinel guards §3.8 (1): every risk-tier
// classification failure (minimum recompute mismatch, planner below
// minimum, unknown tier name) must be identifiable via errors.Is(err,
// ErrRiskUnclassifiable) — app.go's escalation path (isRiskUnclassifiable)
// depends on this typed classification instead of message-substring
// matching.
func TestValidateRiskErrorsWrapSentinel(t *testing.T) {
	pol := RiskPolicy{Version: 1, DefaultTier: "medium"}
	base := validPlan()
	scen := map[string]bool{"E1": true}
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"planner below minimum", func(p *Plan) { p.Tasks[0].PlannerRiskTier = "low" }},
		{"minimum mismatch", func(p *Plan) { p.Tasks[0].MinimumRiskTier = "low" }}, // 重算=medium
		{"unknown minimum tier", func(p *Plan) { p.Tasks[0].MinimumRiskTier = "nonsense" }},
		{"unknown planner tier", func(p *Plan) { p.Tasks[0].PlannerRiskTier = "nonsense" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base
			p.Tasks = append([]Task(nil), base.Tasks...)
			c.mutate(&p)
			errs := Validate(p, pol, scen)
			found := false
			for _, e := range errs {
				if errors.Is(e, ErrRiskUnclassifiable) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: want an error wrapping ErrRiskUnclassifiable, got %v", c.name, errs)
			}
		})
	}
}

// TestValidateNonRiskErrorsDoNotWrapSentinel guards the flip side: an
// unrelated validation failure (unknown scenario reference) must not be
// mistaken for a risk-classification failure via errors.Is.
func TestValidateNonRiskErrorsDoNotWrapSentinel(t *testing.T) {
	pol := RiskPolicy{Version: 1, DefaultTier: "medium"}
	base := validPlan()
	scen := map[string]bool{"E1": true}
	p := base
	p.Tasks = append([]Task(nil), base.Tasks...)
	p.Tasks[0].Scenarios = []string{"E9"} // unknown scenario, not a risk error
	errs := Validate(p, pol, scen)
	if len(errs) == 0 {
		t.Fatal("test setup: expected an unknown-scenario validation error")
	}
	for _, e := range errs {
		if errors.Is(e, ErrRiskUnclassifiable) {
			t.Fatalf("unknown-scenario error must not wrap ErrRiskUnclassifiable: %v", e)
		}
	}
}

func TestParseRejectsSelectedRiskTier(t *testing.T) { // §3.3：入 plan schema 即拒絕
	_, err := Parse([]byte("plan_id: P1\ntasks:\n  - id: T1\n    selected_risk_tier: high\n"))
	if err == nil {
		t.Fatal("selected_risk_tier in plan schema must be rejected")
	}
}
