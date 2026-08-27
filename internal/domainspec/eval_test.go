package domainspec

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const dagBundle = `schema_version: 1
rules:
  - id: A
    phase: decide
    when: "decision == 'nonexistent'"
    effect: deny
    target: decision.eligibility
    priority: 10
    verdict: "A"
    refs: "test"
    step_rank: 2
    stage: none
  - id: B
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [A]
    priority: 10
    verdict: "B"
    refs: "test"
    step_rank: 2
    stage: none
  - id: C
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [B]
    priority: 10
    verdict: "C"
    refs: "test"
    step_rank: 2
    stage: none
`

func TestEvaluateNotEligibleTransitive(t *testing.T) {
	b, _ := LoadBundle([]byte(dagBundle), 1_000_000)
	r, err := Evaluate(b, mustSnapshot(t), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Truth != "true" || r.Status != "ok" {
		t.Fatalf("got truth=%s status=%s", r.Truth, r.Status)
	}
	causes := map[string]string{}
	for _, e := range r.ReasonGraph {
		causes[e.RuleID] = e.Outcome + "/" + e.Cause
	}
	if !strings.Contains(causes["B"], "not_eligible") || !strings.Contains(causes["B"], "A") {
		t.Fatalf("B must be not_eligible caused by A: %v", causes)
	}
	if !strings.Contains(causes["C"], "not_eligible") || !strings.Contains(causes["C"], "B") {
		t.Fatalf("C must be not_eligible caused by B (transitive): %v", causes)
	}
}

func TestEvaluateMissingLeafYieldsUnknown(t *testing.T) {
	b, _ := LoadBundle(miniBundle(""), 1_000_000) // RA references `decision`
	s := mustSnapshot(t)
	s.Decision = Fact[string]{Presence: Missing}
	r, _ := Evaluate(b, s, 100_000)
	if r.Truth != "unknown" || r.Status != "ok" {
		t.Fatalf("missing leaf must yield unknown, got %s/%s", r.Truth, r.Status)
	}
	if len(r.UnknownLeaves) == 0 || r.UnknownLeaves[0] != "decision" {
		t.Fatalf("unknown_leaves must name the missing group: %v", r.UnknownLeaves)
	}
}

func TestEvaluateNotApplicableIsNotUnknown(t *testing.T) {
	// plan rev2 P1／rev4 修測試標的：規則必須**引用** not_applicable 的 fact 群組，
	// 才真正驗到「not_applicable → not eligible、不影響 truth」（rev3 版的 RA 只引用
	// decision，測不到本意）。
	const planRefBundle = `schema_version: 1
rules:
  - id: RPLAN
    phase: decide
    when: "size(plan.tasks) > 0"
    effect: deny
    target: decision.eligibility
    priority: 10
    verdict: "RPLAN"
    refs: "test"
    step_rank: 10
    stage: none
`
	b, _ := LoadBundle([]byte(planRefBundle), 1_000_000)
	s := mustSnapshot(t)
	// rejected 形：decision 群組 known、plan 群組 not_applicable
	rej := "rejected"
	reason := "why"
	s.Decision = Fact[string]{Presence: Known, Value: &rej}
	s.Reason = Fact[string]{Presence: Known, Value: &reason}
	s.Current = Fact[Current]{Presence: NotApplicable}
	s.BaseCommitState = Fact[string]{Presence: NotApplicable}
	s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
	s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
	r, _ := Evaluate(b, s, 100_000)
	if r.Truth != "true" || len(r.UnknownLeaves) != 0 {
		t.Fatalf("not_applicable must not leak into unknown: %s %v", r.Truth, r.UnknownLeaves)
	}
	var entry *ReasonEntry
	for i := range r.ReasonGraph {
		if r.ReasonGraph[i].RuleID == "RPLAN" {
			entry = &r.ReasonGraph[i]
		}
	}
	if entry == nil || entry.Outcome != "not_eligible" || !strings.Contains(entry.Cause, "not_applicable") {
		t.Fatalf("rule referencing not_applicable fact must be not_eligible: %+v", entry)
	}
}

func TestEvaluateRuntimeCostLimitNotCached(t *testing.T) {
	// plan rev4：CostLimit 是 Program 建構期 option——同一 bundle 先高 limit 再 0，
	// 第二次必須 evaluation_error（快取第一次 Program 會誤沿用舊限制）。
	b, _ := LoadBundle(miniBundle(""), 1_000_000)
	s := mustSnapshot(t)
	if r, _ := Evaluate(b, s, 100_000); r.Status != "ok" {
		t.Fatalf("first eval with high limit must be ok, got %s", r.Status)
	}
	if r, _ := Evaluate(b, s, 0); r.Status != "evaluation_error" {
		t.Fatalf("second eval with limit 0 must error, got %s", r.Status)
	}
}

func TestEvaluatePerTaskPlanMissingYieldsUnknown(t *testing.T) {
	// plan rev3：純 per-task bundle——不得夾帶會在 approved snapshot 命中的 deny 規則
	// （全域 truth deny>unknown，unknown 會被 false 蓋掉，測試就測不到本意）。
	const perTaskOnlyBundle = `schema_version: 1
rules:
  - id: RT
    phase: decide
    when: "sel == null"
    effect: deny
    target: risk.task
    per_task: true
    priority: 10
    verdict: "RT"
    refs: "test"
    step_rank: 10
    stage: task_loop
    check_rank: 0
`
	b, _ := LoadBundle([]byte(perTaskOnlyBundle), 1_000_000)
	s := mustSnapshot(t)
	s.Plan = Fact[PlanFacts]{Presence: Missing}
	r, _ := Evaluate(b, s, 100_000)
	if r.Truth != "unknown" {
		t.Fatalf("per_task with plan missing must yield unknown（不得 not-eligible 成 true）, got %s", r.Truth)
	}
}

func TestEvaluateConflict(t *testing.T) {
	conflictExtra := `  - id: RALLOW
    phase: decide
    when: "decision == 'approved'"
    effect: allow
    target: decision.eligibility
    priority: 10
    verdict: "allow fixture"
    refs: "test"
    step_rank: 2
    stage: none
`
	b, _ := LoadBundle(miniBundle(conflictExtra), 1_000_000)
	r, _ := Evaluate(b, mustSnapshot(t), 100_000)
	if r.Truth != "conflict" || r.Status != "ok" {
		t.Fatalf("same-priority opposing effects must conflict, got %s/%s", r.Truth, r.Status)
	}
	if len(r.ConflictingRuleIDs) != 2 {
		t.Fatalf("conflicting ids: %v", r.ConflictingRuleIDs)
	}
}

func TestEvaluateRuntimeCostLimitIsError(t *testing.T) {
	b, _ := LoadBundle(miniBundle(""), 1_000_000)
	r, _ := Evaluate(b, mustSnapshot(t), 0) // runtime limit 0 → 必爆
	if r.Status != "evaluation_error" {
		t.Fatalf("cost-limit breach must be evaluation_error, got %s", r.Status)
	}
	if r.Truth == "unknown" {
		t.Fatal("error 不併入 unknown（owner 凍結）")
	}
}

func TestEvaluateViolationsExcludeAllowMatches(t *testing.T) {
	// controller ruling（Task 4 review）：Violations 只收 deny 命中——allow
	// 命中若混進 Violations，會在 Task 6 的 PrimaryViolation 四層裁決裡冒充
	// 成一條「違規」，可能用較小的 rank tuple 蓋過真正擋下請求的 deny。
	// RD／RAT 分屬不同 target（decision.eligibility／risk.T1），無 conflict。
	const mixedBundle = `schema_version: 1
rules:
  - id: RD
    phase: decide
    when: "decision == 'approved'"
    effect: deny
    target: decision.eligibility
    priority: 10
    verdict: "RD"
    refs: "test"
    step_rank: 2
    stage: none
  - id: RAT
    phase: decide
    when: "true"
    effect: allow
    target: risk.task
    per_task: true
    priority: 10
    verdict: "RAT"
    refs: "test"
    step_rank: 10
    stage: task_loop
    check_rank: 0
`
	b, err := LoadBundle([]byte(mixedBundle), 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Evaluate(b, mustSnapshot(t), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || r.Violations[0].RuleID != "RD" {
		t.Fatalf("violations must contain only the deny match: %+v", r.Violations)
	}
	matched := map[string]bool{}
	for _, id := range r.MatchedRuleIDs {
		matched[id] = true
	}
	if !matched["RD"] || !matched["RAT"] {
		t.Fatalf("matched rule ids must include both deny and allow matches: %v", r.MatchedRuleIDs)
	}
}

func TestEvaluateDeterministicBytes(t *testing.T) {
	// 出口 4：同 facts 兩次評估，序列化輸出逐字節相等
	b, _ := LoadBundle([]byte(dagBundle), 1_000_000)
	s := mustSnapshot(t)
	r1, _ := Evaluate(b, s, 100_000)
	r2, _ := Evaluate(b, s, 100_000)
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if !bytes.Equal(j1, j2) {
		t.Fatal("reason graph must be byte-identical across runs")
	}
}

// TestEvaluatePerTaskSelMissingYieldsUnknown：final review 發現的 presence
// 缺口回歸測試。decide presence matrix 合法容許 risk_selections missing
// （facts.go validateDecideMatrix：isKnownColumn 允許 known 或 missing），此時
// findSelection 一律回傳 nil，跟「risk_selections known 但該 task 無配對選擇」
// 回傳的 nil 無法區分。R25（per_task，when: "decision == 'approved' && sel ==
// null"）若不把 RefVars 含 sel 的規則對應到 risk_selections 群組的 own-presence
// gate，就會把「未知」誤判成「已知為空」而 deny——spec §2 要求 missing →
// unknown，不是 false。
func TestEvaluatePerTaskSelMissingYieldsUnknown(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	s.RiskSelections = Fact[[]RiskSelection]{Presence: Missing}

	r := evalGate2(t, b, s)

	if r.Truth != TruthUnknown {
		t.Fatalf("risk_selections missing must yield unknown truth, got %s (violations=%+v)", r.Truth, r.Violations)
	}
	foundGroup := false
	for _, g := range r.UnknownLeaves {
		if g == "risk_selections" {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("unknown_leaves must contain risk_selections, got %v", r.UnknownLeaves)
	}
	for _, v := range r.Violations {
		if v.RuleID == "R25" {
			t.Fatalf("R25 must not fire deny when risk_selections is missing: %+v", r.Violations)
		}
	}
}
