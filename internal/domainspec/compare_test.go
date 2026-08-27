package domainspec

import "testing"

// bundleWithRanks：Task 6 brief 指定的 helper 名稱——直接沿用既有
// loadGate2Bundle（gate2_bundle_test.go，static cost limit 50_000_000，
// 比 brief 建議的 5_000_000 更寬鬆）載入同一份 testdata/gate2-bundle.yaml，
// 避免重複的檔案讀取/LoadBundle 樣板（對齊既有 pattern，不重造第二份）。
func bundleWithRanks(t *testing.T) *CompiledBundle {
	t.Helper()
	return loadGate2Bundle(t)
}

func TestPrimaryPrecedenceFourLayers(t *testing.T) {
	b := bundleWithRanks(t) // helper：載入 gate2-bundle.yaml
	cases := []struct {
		name string
		vs   []Violation
		want string
	}{
		// spec §4 coverage 三筆逐層案例的 selector 版：
		{"pre-loop beats task-loop", []Violation{
			{RuleID: "R30", SourceIndex: 0}, {RuleID: "R24", SourceIndex: -1}}, "R24"},
		{"source_index beats rule number", []Violation{
			{RuleID: "R25", SourceIndex: 1}, {RuleID: "R30", SourceIndex: 0}}, "R30"},
		{"task-loop beats post-loop", []Violation{
			{RuleID: "R26", SourceIndex: -1}, {RuleID: "R31", SourceIndex: 2}}, "R31"},
		// 跨 gate step（plan rev2 新增）：
		{"submit beats decide", []Violation{
			{RuleID: "R1", SourceIndex: -1}, {RuleID: "R7", SourceIndex: -1}}, "R7"},
		// kind occurrence rank（plan rev3——較早 kind 的 digest 錯先於較晚 kind 的 missing）：
		{"earlier-kind R9 beats later-kind R8", []Violation{
			{RuleID: "R8", SourceIndex: 1}, {RuleID: "R9", SourceIndex: 0}}, "R9"},
		{"R3 beats PrepareDecision internals", []Violation{
			{RuleID: "R11", SourceIndex: -1}, {RuleID: "R3", SourceIndex: -1}}, "R3"},
		{"BuildDecision beats R16", []Violation{
			{RuleID: "R16", SourceIndex: -1}, {RuleID: "R30", SourceIndex: 0}}, "R30"},
		{"in-task check rank", []Violation{
			{RuleID: "R30", SourceIndex: 0}, {RuleID: "R29", SourceIndex: 0}}, "R29"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := PrimaryViolation(b, &Result{Violations: tc.vs})
			if !ok || v.RuleID != tc.want {
				t.Fatalf("want %s got %+v", tc.want, v)
			}
		})
	}
}

func TestBuildShadowRiskDecisionsSortedAndComplete(t *testing.T) {
	s := mustSnapshot(t)
	s.Plan.Value.Tasks = []PlanTask{
		{ID: "T2", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "high"},
		{ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low"},
	}
	sels := []RiskSelection{
		{TaskID: "T2", SelectedRiskTier: "low", OverrideReason: "ok by owner"},
		{TaskID: "T1", SelectedRiskTier: "low"},
	}
	s.RiskSelections.Value = &sels
	got := BuildShadowRiskDecisions(s)
	if len(got) != 2 || got[0].TaskID != "T1" || got[1].TaskID != "T2" {
		t.Fatalf("must be task_id sorted（R32）: %+v", got)
	}
	if got[1].PlannerRiskTier != "high" || got[1].OverrideReason != "ok by owner" {
		t.Fatalf("five columns must be populated: %+v", got[1])
	}
}

func TestCompareCaseContract(t *testing.T) {
	b := bundleWithRanks(t)
	s := mustSnapshot(t)
	blocked := &Result{Truth: "false", Status: "ok",
		Violations: []Violation{{RuleID: "R24", SourceIndex: -1}}}
	if ok, _ := CompareCase(b, s, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R24"}); !ok {
		t.Fatal("matching primary must compare equal")
	}
	if ok, _ := CompareCase(b, s, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R30"}); ok {
		t.Fatal("primary mismatch must be reported")
	}
	// pass 側逐欄比對（plan rev2）：RiskDecisions 內容不符必須不一致
	pass := &Result{Truth: "true", Status: "ok"}
	good := GoVerdict{Outcome: "pass", RiskDecisions: BuildShadowRiskDecisions(s)}
	if ok, _ := CompareCase(b, s, pass, good); !ok {
		t.Fatal("identical risk decisions must compare equal")
	}
	bad := GoVerdict{Outcome: "pass", RiskDecisions: []RiskDecision{{TaskID: "T1", SelectedRiskTier: "high"}}}
	if ok, _ := CompareCase(b, s, pass, bad); ok {
		t.Fatal("risk decision content mismatch must be reported（R32 證據）")
	}
	errRes := &Result{Truth: "true", Status: "evaluation_error"}
	if ok, _ := CompareCase(b, s, errRes, good); ok {
		t.Fatal("evaluation_error must never compare equal")
	}
}
