package domainspec

import (
	"fmt"
	"sort"
)

// Outcome 是 oracle 觀察到的 case 結局（spec §4——駁回成功也是 pass，只有
// 「CEL truth 判成功但實際被擋下」這種矛盾才是 blocked）。
type Outcome string

const (
	OutcomePass    Outcome = "pass"
	OutcomeBlocked Outcome = "blocked"
)

// RiskDecision：完整決議輸出（plan rev2 新增——對齊 production gate.RiskDecision 五欄）。
type RiskDecision struct {
	TaskID           string `json:"task_id"`
	MinimumRiskTier  string `json:"minimum_risk_tier"`
	PlannerRiskTier  string `json:"planner_risk_tier"`
	SelectedRiskTier string `json:"selected_risk_tier"`
	OverrideReason   string `json:"override_reason"`
}

// GoVerdict：oracle 可觀測結果（固化進 corpus JSON）。
type GoVerdict struct {
	Outcome       Outcome        `json:"outcome"`
	PrimaryRuleID string         `json:"primary_rule_id"` // blocked 時必填
	RiskDecisions []RiskDecision `json:"risk_decisions"`  // pass＋approved 時必填；rejected pass 為空
}

// BuildShadowRiskDecisions：pass＋approved 案例的 shadow 輸出——依 plan.tasks 配對
// selection、帶 committed minimum/planner tier、依 task_id 排序（R32 的證據來源，
// 取代 rev1 的豁免）。decision != approved 或 plan 非 known → nil。
//
// 邊界語意（Task 6 選擇）：task 在 risk_selections 找不到配對 selection 時不
// panic，輸出該 task 的 SelectedRiskTier／OverrideReason 為空字串（而非整筆
// 跳過）——輸出長度恆等於 plan.tasks 長度，方便呼叫端察覺「task 數與 selection
// 數對不上」而非悄悄少一筆。pass 案例依 R25（task_loop 逐 task 檢查全部有
// selection）在正常流程下不會走到這個分支。
func BuildShadowRiskDecisions(s *FactsSnapshot) []RiskDecision {
	if s == nil {
		return nil
	}
	if s.Decision.Presence != Known || s.Decision.Value == nil || *s.Decision.Value != "approved" {
		return nil
	}
	if s.Plan.Presence != Known || s.Plan.Value == nil {
		return nil
	}

	selByTask := make(map[string]RiskSelection)
	if s.RiskSelections.Presence == Known && s.RiskSelections.Value != nil {
		for _, sel := range *s.RiskSelections.Value {
			selByTask[sel.TaskID] = sel
		}
	}

	out := make([]RiskDecision, 0, len(s.Plan.Value.Tasks))
	for _, task := range s.Plan.Value.Tasks {
		rd := RiskDecision{
			TaskID:          task.ID,
			MinimumRiskTier: task.MinimumRiskTier,
			PlannerRiskTier: task.PlannerRiskTier,
		}
		if sel, found := selByTask[task.ID]; found {
			rd.SelectedRiskTier = sel.SelectedRiskTier
			rd.OverrideReason = sel.OverrideReason
		}
		out = append(out, rd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

var stageRank = map[string]int{"none": 0, "pre_loop": 1, "task_loop": 2, "post_loop": 3}

// precedenceKey 是 PrimaryViolation 四層裁決的排序鍵；bundleIdx 不是 spec §4
// 的第五層語意，只是「四層完全打平時」的 determinism 保底（見 PrimaryViolation
// 說明）。
type precedenceKey struct {
	stepRank  int
	stageRank int
	srcIndex  int
	checkRank int
	bundleIdx int
}

func (a precedenceKey) less(b precedenceKey) bool {
	if a.stepRank != b.stepRank {
		return a.stepRank < b.stepRank
	}
	if a.stageRank != b.stageRank {
		return a.stageRank < b.stageRank
	}
	if a.srcIndex != b.srcIndex {
		return a.srcIndex < b.srcIndex
	}
	if a.checkRank != b.checkRank {
		return a.checkRank < b.checkRank
	}
	return a.bundleIdx < b.bundleIdx
}

// PrimaryViolation：四層 precedence（spec §4）——
// (step_rank, stageRank[stage], source_index(-1 視為 0), check_rank) 字典序最小者。
//
// bundleIdx tiebreak（Task 6 選擇）：四層鍵值可能完全打平——例如本檔測試把
// submit phase 與 decide phase 的 Violations 人為合併比較時，兩個 phase 各自
// 由 0（或 1）起算的 step_rank 剛好撞號。真實 Evaluate 呼叫一次只評估單一
// phase，Result.Violations 不會真的橫跨兩個 phase，故這個 tiebreak 只在合成
// 測試／未來跨 phase 合併情境下才會被觸發；用 b.Rules 的宣告序（bundle YAML
// 內 submit 先於 decide）打平，保底 determinism，不做猜測式仲裁。
//
// 找不到對應規則（v.RuleID 不在 b.ByID——理論上不應發生，Violations 只會來自
// 同一 bundle 的 Evaluate）的 violation 略過不參與排序，而非 panic：primary
// 裁決是唯讀查詢，寧可漏掉一筆異常輸入也不讓呼叫端崩潰。
//
// 空 Violations（例如 truth==true 或 unknown 的 Result）回傳 ok=false。
func PrimaryViolation(b *CompiledBundle, r *Result) (Violation, bool) {
	if b == nil || r == nil || len(r.Violations) == 0 {
		return Violation{}, false
	}

	ruleIndex := make(map[string]int, len(b.Rules))
	for i, cr := range b.Rules {
		ruleIndex[cr.ID] = i
	}

	var (
		best    Violation
		bestKey precedenceKey
		found   bool
	)
	for _, v := range r.Violations {
		rule, ok := b.ByID[v.RuleID]
		if !ok {
			continue
		}
		srcIndex := v.SourceIndex
		if srcIndex == -1 {
			srcIndex = 0
		}
		key := precedenceKey{
			stepRank:  rule.StepRank,
			stageRank: stageRank[rule.Stage],
			srcIndex:  srcIndex,
			checkRank: rule.CheckRank,
			bundleIdx: ruleIndex[v.RuleID],
		}
		if !found || key.less(bestKey) {
			best, bestKey, found = v, key, true
		}
	}
	return best, found
}

// CompareCase（plan rev2 擴充 pass 側逐欄比對）：
//
//	go=blocked：CEL truth 必須 "false" 且 PrimaryViolation.RuleID == gv.PrimaryRuleID。
//	go=pass：CEL truth 必須 "true" 且 BuildShadowRiskDecisions(s) 與 gv.RiskDecisions
//	  逐欄 deep-equal（含順序——雙方皆 task_id 排序；R32 證據）。
//	status=evaluation_error → 一律不一致。
//
// gv.Outcome 非 pass／blocked 的其他值（理論上不應出現，corpus 只固化這兩種
// Outcome）視為不一致，detail 說明無法辨識的 outcome，而非 panic。
func CompareCase(b *CompiledBundle, s *FactsSnapshot, r *Result, gv GoVerdict) (ok bool, detail string) {
	if r.Status == StatusEvaluationError {
		return false, fmt.Sprintf("cel status=evaluation_error 永遠不一致（go verdict outcome=%q）", gv.Outcome)
	}

	switch gv.Outcome {
	case OutcomeBlocked:
		if r.Truth != TruthFalse {
			return false, fmt.Sprintf("go verdict outcome=blocked 但 cel truth=%q（應為 false）", r.Truth)
		}
		pv, found := PrimaryViolation(b, r)
		if !found {
			return false, "go verdict outcome=blocked 但 cel 沒有任何 violation 可選出 primary"
		}
		if pv.RuleID != gv.PrimaryRuleID {
			return false, fmt.Sprintf("primary rule 不一致：cel=%q go=%q", pv.RuleID, gv.PrimaryRuleID)
		}
		return true, ""

	case OutcomePass:
		if r.Truth != TruthTrue {
			return false, fmt.Sprintf("go verdict outcome=pass 但 cel truth=%q（應為 true）", r.Truth)
		}
		want := BuildShadowRiskDecisions(s)
		if !riskDecisionsEqual(want, gv.RiskDecisions) {
			return false, fmt.Sprintf("risk decisions 不一致：shadow=%+v go=%+v", want, gv.RiskDecisions)
		}
		return true, ""

	default:
		return false, fmt.Sprintf("無法辨識的 go verdict outcome %q", gv.Outcome)
	}
}

// riskDecisionsEqual 逐欄＋依序比對兩份 RiskDecisions（R32 證據）。刻意不用
// reflect.DeepEqual：nil（BuildShadowRiskDecisions 對 rejected／非 known plan
// 的回傳值）與 JSON 解出的空 slice `[]RiskDecision{}`（GoVerdict doc：
// 「rejected pass 為空」）對 reflect.DeepEqual 是不同值，會把語意相同的
// 「沒有 risk decisions」誤判成不一致；長度比對＋逐欄 comparable 相等
// （RiskDecision 五欄皆 string，可比較）避開這個 nil-vs-empty 假陽性。
func riskDecisionsEqual(a, b []RiskDecision) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
