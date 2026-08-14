package plan

import (
	"errors"
	"fmt"
)

// ErrRiskUnclassifiable is the sentinel every risk-tier classification
// failure Validate reports wraps (minimum recompute mismatch, planner tier
// below minimum, unknown tier name) — §3.8 (1). Callers identify these via
// errors.Is rather than matching on message text.
var ErrRiskUnclassifiable = errors.New("plan: risk tier unclassifiable")

// Validate performs deterministic, collection-style checks over a plan:
// schema completeness (plan_id/task id/title non-empty), DAG acyclicity,
// dependency existence, task ID uniqueness, risk-tier recomputation
// (policy.ComputeMinimum(t) must equal t.MinimumRiskTier), the
// planner-tier floor (planner_risk_tier >= minimum_risk_tier), and scenario
// references (must exist in specScenarios). Every violation is collected —
// Validate never stops at the first error — so an editor can render all
// problems inline at once. selected_risk_tier/override_reason are not part
// of the plan schema and are rejected earlier, at Parse time (§3.3); this
// function does not need to know about them.
func Validate(p Plan, policy RiskPolicy, specScenarios map[string]bool) []error {
	var errs []error

	if p.PlanID == "" {
		errs = append(errs, fmt.Errorf("plan_id must not be empty"))
	}

	seen := make(map[string]bool, len(p.Tasks))
	ids := make(map[string]bool, len(p.Tasks))
	for i, t := range p.Tasks {
		if t.ID == "" {
			errs = append(errs, fmt.Errorf("tasks[%d]: id must not be empty", i))
		} else {
			if seen[t.ID] {
				errs = append(errs, fmt.Errorf("task %q: duplicate task id", t.ID))
			}
			seen[t.ID] = true
			ids[t.ID] = true
		}
		if t.Title == "" {
			errs = append(errs, fmt.Errorf("task %q: title must not be empty", t.ID))
		}
	}

	for _, t := range p.Tasks {
		for _, dep := range t.DependsOn {
			if !ids[dep] {
				errs = append(errs, fmt.Errorf("task %q: depends_on references unknown task %q", t.ID, dep))
			}
		}
	}

	errs = append(errs, detectCycles(p.Tasks)...)

	for _, t := range p.Tasks {
		for _, sc := range t.Scenarios {
			if !specScenarios[sc] {
				errs = append(errs, fmt.Errorf("task %q: scenario %q not found in spec scenarios", t.ID, sc))
			}
		}

		_, minKnown := tierOrder[t.MinimumRiskTier]
		if !minKnown {
			errs = append(errs, fmt.Errorf("task %q: unknown minimum_risk_tier %q: %w", t.ID, t.MinimumRiskTier, ErrRiskUnclassifiable))
		}
		_, plannerKnown := tierOrder[t.PlannerRiskTier]
		if !plannerKnown {
			errs = append(errs, fmt.Errorf("task %q: unknown planner_risk_tier %q: %w", t.ID, t.PlannerRiskTier, ErrRiskUnclassifiable))
		}

		if want := policy.ComputeMinimum(t); want != t.MinimumRiskTier {
			errs = append(errs, fmt.Errorf("task %q: minimum_risk_tier %q does not match recomputed %q: %w", t.ID, t.MinimumRiskTier, want, ErrRiskUnclassifiable))
		}

		if minKnown && plannerKnown && tierOrder[t.PlannerRiskTier] < tierOrder[t.MinimumRiskTier] {
			errs = append(errs, fmt.Errorf("task %q: planner_risk_tier %q below minimum_risk_tier %q: %w", t.ID, t.PlannerRiskTier, t.MinimumRiskTier, ErrRiskUnclassifiable))
		}
	}

	return errs
}

// detectCycles reports dependency cycles in tasks' depends_on edges via DFS
// with three-color marking (white/gray/black). Edges to unknown task IDs
// are skipped here — that is reported separately as a missing-dependency
// error — so a dangling reference never masquerades as a cycle.
func detectCycles(tasks []Task) []error {
	const (
		white = iota
		gray
		black
	)

	known := make(map[string]bool, len(tasks))
	order := make([]string, 0, len(tasks))
	adj := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		if !known[t.ID] {
			known[t.ID] = true
			order = append(order, t.ID)
		}
		adj[t.ID] = append(adj[t.ID], t.DependsOn...)
	}

	color := make(map[string]int, len(order))
	var errs []error
	var visit func(id string)
	visit = func(id string) {
		color[id] = gray
		for _, dep := range adj[id] {
			if !known[dep] {
				continue
			}
			switch color[dep] {
			case gray:
				errs = append(errs, fmt.Errorf("dependency cycle detected: %s -> %s", id, dep))
			case white:
				visit(dep)
			}
		}
		color[id] = black
	}
	for _, id := range order {
		if color[id] == white {
			visit(id)
		}
	}
	return errs
}
