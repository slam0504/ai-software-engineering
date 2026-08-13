package plan

import "testing"

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

func TestParseRejectsSelectedRiskTier(t *testing.T) { // §3.3：入 plan schema 即拒絕
	_, err := Parse([]byte("plan_id: P1\ntasks:\n  - id: T1\n    selected_risk_tier: high\n"))
	if err == nil {
		t.Fatal("selected_risk_tier in plan schema must be rejected")
	}
}
