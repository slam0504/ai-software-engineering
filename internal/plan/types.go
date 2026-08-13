// Package plan defines the plan document schema, a deterministic validator,
// and risk-policy recomputation (§3.5). It is a pure domain package: no I/O,
// no dependency on internal/gate (architecture freeze — plan stays
// technology-neutral so gate2 policy can depend on it, not vice versa).
package plan

// Command is the executable invocation a task's test contract runs.
type Command struct {
	Executable string   `yaml:"executable" json:"executable"`
	Argv       []string `yaml:"argv" json:"argv"`
}

// ExpectedFailure lists the test IDs a contract expects to fail before the
// task's implementation, and the substring matcher used to confirm the
// failure reason. Regex matching is deliberately out of scope (YAGNI).
type ExpectedFailure struct {
	TestIDs []string `yaml:"test_ids" json:"test_ids"`
	Matcher string   `yaml:"matcher" json:"matcher"`
}

// TestContract binds a task to the command that proves it and the failure
// it must reproduce pre-implementation.
type TestContract struct {
	Command         Command         `yaml:"command" json:"command"`
	ExpectedFailure ExpectedFailure `yaml:"expected_failure" json:"expected_failure"`
}

// Task is a single unit of work in a plan.
type Task struct {
	ID        string   `yaml:"id" json:"id"`
	Title     string   `yaml:"title" json:"title"`
	Scenarios []string `yaml:"scenarios" json:"scenarios"`
	DependsOn []string `yaml:"depends_on" json:"depends_on"`
	Impact    struct {
		Contexts []string `yaml:"contexts" json:"contexts"`
		Modules  []string `yaml:"modules" json:"modules"`
	} `yaml:"impact" json:"impact"`
	Completion      []string     `yaml:"completion" json:"completion"`
	MinimumRiskTier string       `yaml:"minimum_risk_tier" json:"minimum_risk_tier"`
	PlannerRiskTier string       `yaml:"planner_risk_tier" json:"planner_risk_tier"`
	PermissionsRef  string       `yaml:"permissions_ref" json:"permissions_ref"`
	TestContract    TestContract `yaml:"test_contract" json:"test_contract"`
}

// Plan is the top-level plan document.
type Plan struct {
	PlanID             string `yaml:"plan_id" json:"plan_id"`
	AnalysisBaseCommit string `yaml:"analysis_base_commit" json:"analysis_base_commit"`
	SpecManifest       string `yaml:"spec_manifest" json:"spec_manifest"`
	RiskPolicy         string `yaml:"risk_policy" json:"risk_policy"`
	Tasks              []Task `yaml:"tasks" json:"tasks"`
}

// tierOrder defines the total order over risk tiers (low < medium < high),
// used to compare a task's planner tier against its recomputed minimum.
var tierOrder = map[string]int{"low": 1, "medium": 2, "high": 3}

// RiskPolicy maps task impact (contexts/modules) to a minimum risk tier.
type RiskPolicy struct {
	Version     int    `yaml:"version" json:"version"`
	DefaultTier string `yaml:"default_tier" json:"default_tier"`
	Rules       []struct {
		Match struct {
			Contexts []string `yaml:"contexts" json:"contexts"`
			Modules  []string `yaml:"modules" json:"modules"`
		} `yaml:"match" json:"match"`
		Tier string `yaml:"tier" json:"tier"`
	} `yaml:"rules" json:"rules"`
}
