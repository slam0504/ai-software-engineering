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
