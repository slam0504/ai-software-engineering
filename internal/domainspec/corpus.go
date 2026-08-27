package domainspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// CorpusCase：spec §4 manifest 的 tagged union 案例（"evaluated" | "acquisition_failed"）。
// OracleSeam 決定 freshness recompute 走哪個 production seam adapter（root
// freshness 檔，Task 8 接線）；Provenance 記來源出處（spec §4 五類＋
// host_boundary）。無 I/O：本檔只收/回傳值，目錄走訪與讀檔留在 _test.go helper。
type CorpusCase struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"` // "evaluated" | "acquisition_failed"
	EvaluationPhase string `json:"evaluation_phase"`

	OracleSeam string `json:"oracle_seam"` // "gate_service_submit" | "gatepolicy_validate" | "gatepolicy_reconcile" | "gate_service_prepare" | "gatepolicy_build" | "escalation" | "app_gatedecide" | "host_boundary"
	Provenance string `json:"provenance"`  // "gatepolicy_tests" | "gate_service_tests" | "escalation_tests" | "a9_workspace" | "synthetic" | "host_boundary"

	FactsDigest  string         `json:"facts_digest"`  // evaluated 必填＝SnapshotDigest(Snapshot)
	BundleDigest string         `json:"bundle_digest"` // evaluated 必填＝固化當時（baseline）的 bundle digest
	Snapshot     *FactsSnapshot `json:"snapshot"`
	GoVerdict    *GoVerdict     `json:"go_verdict"`
	Reason       string         `json:"reason"` // acquisition_failed 專用

	// coverage 角色宣告——宣告是「意圖」，計數只認實際證據（見 CoverageReport／
	// ReplayCorpus）；isolated 宣告與實際 violations 不符 → ReplayCorpus error。
	Role        string   `json:"role"`         // "isolated" | "precedence" | "output" | "alignment" | "none"
	CoversRules []string `json:"covers_rules"` // isolated：恰一個目標 rule id；precedence：完整預期 violation 集合（≥2），首位為預期 primary
}

const (
	CorpusKindEvaluated         = "evaluated"
	CorpusKindAcquisitionFailed = "acquisition_failed"
)

var validCorpusKinds = map[string]bool{
	CorpusKindEvaluated:         true,
	CorpusKindAcquisitionFailed: true,
}

var validOracleSeams = map[string]bool{
	"gate_service_submit":  true,
	"gatepolicy_validate":  true,
	"gatepolicy_reconcile": true,
	"gate_service_prepare": true,
	"gatepolicy_build":     true,
	"escalation":           true,
	"app_gatedecide":       true,
	"host_boundary":        true,
}

var validProvenances = map[string]bool{
	"gatepolicy_tests":   true,
	"gate_service_tests": true,
	"escalation_tests":   true,
	"a9_workspace":       true,
	"synthetic":          true,
	"host_boundary":      true,
}

var validCorpusRoles = map[string]bool{
	"isolated":   true,
	"precedence": true,
	"output":     true,
	"alignment":  true,
	"none":       true,
}

// submitPhaseSeams／decidePhaseSeams：phase↔seam 合法組合（brief 凍結）。
// host_boundary 只在 submit 合法——acquisition_failed 的三種來源（LoadAt 錯／
// rev-parse fatal／dirty tree）皆歸在 submit 前的 host 邊界檢查。
var submitPhaseSeams = map[string]bool{
	"gate_service_submit": true,
	"gatepolicy_validate": true,
	"host_boundary":       true,
}

var decidePhaseSeams = map[string]bool{
	"gatepolicy_reconcile": true,
	"gate_service_prepare": true,
	"gatepolicy_build":     true,
	"escalation":           true,
	"app_gatedecide":       true,
}

// ValidateCorpusCase：單筆案例的全部驗證權威（spec §4 tagged union 契約）——
// evaluated：Snapshot／GoVerdict／FactsDigest／BundleDigest 必填、Reason 必空，
// 對 Snapshot 呼叫 ValidateFactsSnapshot，重算 SnapshotDigest(Snapshot) 與
// FactsDigest 不符即拒（snapshot 漂移）；acquisition_failed：Reason 必填，
// Snapshot／GoVerdict／FactsDigest／BundleDigest 一律必空。另檢查
// OracleSeam／Provenance／Role enum、phase↔seam 合法組合、evaluated 的
// EvaluationPhase == Snapshot.EvaluationPhase、role↔covers_rules 形狀
// （isolated 恰一；precedence ≥2）。
func ValidateCorpusCase(c CorpusCase) error {
	name := c.Name
	if name == "" {
		return fmt.Errorf("domainspec: corpus case: name is required")
	}
	if !validCorpusKinds[c.Kind] {
		return fmt.Errorf("domainspec: corpus case %q: invalid kind %q", name, c.Kind)
	}
	if c.EvaluationPhase != "submit" && c.EvaluationPhase != "decide" {
		return fmt.Errorf("domainspec: corpus case %q: invalid evaluation_phase %q", name, c.EvaluationPhase)
	}
	if !validOracleSeams[c.OracleSeam] {
		return fmt.Errorf("domainspec: corpus case %q: invalid oracle_seam %q", name, c.OracleSeam)
	}
	if !validProvenances[c.Provenance] {
		return fmt.Errorf("domainspec: corpus case %q: invalid provenance %q", name, c.Provenance)
	}
	if !validCorpusRoles[c.Role] {
		return fmt.Errorf("domainspec: corpus case %q: invalid role %q", name, c.Role)
	}
	if err := checkSeamPhaseLegality(c.EvaluationPhase, c.OracleSeam); err != nil {
		return fmt.Errorf("domainspec: corpus case %q: %w", name, err)
	}
	if err := checkCoversRulesShape(c.Role, c.CoversRules); err != nil {
		return fmt.Errorf("domainspec: corpus case %q: %w", name, err)
	}

	switch c.Kind {
	case CorpusKindEvaluated:
		return validateEvaluatedCase(name, c)
	default: // CorpusKindAcquisitionFailed（validCorpusKinds 已擋下其餘值）
		return validateAcquisitionFailedCase(name, c)
	}
}

func checkSeamPhaseLegality(phase, seam string) error {
	switch phase {
	case "submit":
		if !submitPhaseSeams[seam] {
			return fmt.Errorf("oracle_seam %q is not legal for evaluation_phase submit", seam)
		}
	case "decide":
		if !decidePhaseSeams[seam] {
			return fmt.Errorf("oracle_seam %q is not legal for evaluation_phase decide", seam)
		}
	default:
		return fmt.Errorf("invalid evaluation_phase %q", phase)
	}
	return nil
}

func checkCoversRulesShape(role string, coversRules []string) error {
	switch role {
	case "isolated":
		if len(coversRules) != 1 {
			return fmt.Errorf("role=isolated requires exactly 1 covers_rules entry, got %d", len(coversRules))
		}
	case "precedence":
		if len(coversRules) < 2 {
			return fmt.Errorf("role=precedence requires >= 2 covers_rules entries, got %d", len(coversRules))
		}
	}
	return nil
}

func validateEvaluatedCase(name string, c CorpusCase) error {
	if c.Reason != "" {
		return fmt.Errorf("domainspec: corpus case %q: evaluated case must not set reason", name)
	}
	if c.Snapshot == nil {
		return fmt.Errorf("domainspec: corpus case %q: evaluated case requires snapshot", name)
	}
	if c.GoVerdict == nil {
		return fmt.Errorf("domainspec: corpus case %q: evaluated case requires go_verdict", name)
	}
	if c.FactsDigest == "" {
		return fmt.Errorf("domainspec: corpus case %q: evaluated case requires facts_digest", name)
	}
	if c.BundleDigest == "" {
		return fmt.Errorf("domainspec: corpus case %q: evaluated case requires bundle_digest", name)
	}
	if err := ValidateFactsSnapshot(c.Snapshot); err != nil {
		return fmt.Errorf("domainspec: corpus case %q: snapshot: %w", name, err)
	}
	if c.EvaluationPhase != c.Snapshot.EvaluationPhase {
		return fmt.Errorf("domainspec: corpus case %q: evaluation_phase %q != snapshot evaluation_phase %q", name, c.EvaluationPhase, c.Snapshot.EvaluationPhase)
	}
	digest, err := SnapshotDigest(c.Snapshot)
	if err != nil {
		return fmt.Errorf("domainspec: corpus case %q: snapshot digest: %w", name, err)
	}
	if digest != c.FactsDigest {
		return fmt.Errorf("domainspec: corpus case %q: facts_digest drift: recomputed %s != declared %s", name, digest, c.FactsDigest)
	}
	switch c.GoVerdict.Outcome {
	case OutcomeBlocked:
		if c.GoVerdict.PrimaryRuleID == "" {
			return fmt.Errorf("domainspec: corpus case %q: blocked go_verdict requires primary_rule_id", name)
		}
	case OutcomePass:
		// ok
	default:
		return fmt.Errorf("domainspec: corpus case %q: invalid go_verdict outcome %q", name, c.GoVerdict.Outcome)
	}
	return nil
}

func validateAcquisitionFailedCase(name string, c CorpusCase) error {
	if c.Reason == "" {
		return fmt.Errorf("domainspec: corpus case %q: acquisition_failed case requires reason", name)
	}
	if c.Snapshot != nil {
		return fmt.Errorf("domainspec: corpus case %q: acquisition_failed case must not set snapshot", name)
	}
	if c.GoVerdict != nil {
		return fmt.Errorf("domainspec: corpus case %q: acquisition_failed case must not set go_verdict", name)
	}
	if c.FactsDigest != "" {
		return fmt.Errorf("domainspec: corpus case %q: acquisition_failed case must not set facts_digest", name)
	}
	if c.BundleDigest != "" {
		return fmt.Errorf("domainspec: corpus case %q: acquisition_failed case must not set bundle_digest", name)
	}
	return nil
}

// DecodeCorpusCase：單檔 bytes → strict decode（DisallowUnknownFields；巢狀
// Snapshot 只觸發 Fact[T].UnmarshalJSON 的 wrapper 層 strict decode，不會經過
// ValidateFactsSnapshot——由下一步 ValidateCorpusCase 顯式呼叫）→ ValidateCorpusCase。
func DecodeCorpusCase(data []byte) (CorpusCase, error) {
	var c CorpusCase
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return CorpusCase{}, fmt.Errorf("domainspec: decode corpus case: %w", err)
	}
	if err := ValidateCorpusCase(c); err != nil {
		return CorpusCase{}, err
	}
	return c, nil
}

// ValidateCorpus：逐筆 ValidateCorpusCase → 跨案例驗證（name 唯一）。四個直接
// 收 []CorpusCase 的入口（ReplayCorpus／DiffBundles／VerifyOracleFreshness／
// generator）呼叫本函式即同時獲得單筆 union 防線——不得依賴呼叫端已驗過
// （Go constructor 可繞過 loadCorpus(t)）。
func ValidateCorpus(cases []CorpusCase) error {
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		if err := ValidateCorpusCase(c); err != nil {
			return err
		}
		if seen[c.Name] {
			return fmt.Errorf("domainspec: corpus: duplicate case name %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// CoverageReport：ReplayCorpus 的輸出——CoveredRules／UncoveredRules 只認實際
// 證據（見 ReplayCorpus coverage 計數規則），OutputEvidence 是 R32 排序證據的
// 獨立計數（不進 CoveredRules，R32 本身不是 CEL 規則 id）。
type CoverageReport struct {
	Consistent, Inconsistent, Exempt int
	Mismatches                       []string
	CoveredRules                     map[string]int
	OutputEvidence                   int
	UncoveredRules                   []string
}

// distinctRuleIDs 收集 Violations 的 distinct rule id 集合（isolated／precedence
// coverage 判定的輸入）。
func distinctRuleIDs(violations []Violation) map[string]bool {
	out := make(map[string]bool, len(violations))
	for _, v := range violations {
		out[v.RuleID] = true
	}
	return out
}

// sameRuleIDSet 判定 distinct 集合與 want 清單（去重後）是否完全相等。
func sameRuleIDSet(distinct map[string]bool, want []string) bool {
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	if len(distinct) != len(wantSet) {
		return false
	}
	for id := range wantSet {
		if !distinct[id] {
			return false
		}
	}
	return true
}

// validateOutputShape（role=output 的 R32 排序證據前置形狀，rev6）：snapshot
// 必須有已知 plan、≥2 個 task，且 plan 來源順序（Tasks slice 原始序，
// canonical.go 保證不重排）刻意非 task_id 序（如 T2 在 T1 前）。
func validateOutputShape(s *FactsSnapshot) error {
	if s == nil || s.Plan.Presence != Known || s.Plan.Value == nil || len(s.Plan.Value.Tasks) < 2 {
		return fmt.Errorf("role=output requires a known plan with >= 2 tasks")
	}
	tasks := s.Plan.Value.Tasks
	sorted := true
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].ID > tasks[i].ID {
			sorted = false
			break
		}
	}
	if sorted {
		return fmt.Errorf("role=output requires plan source order to NOT already be task_id-sorted")
	}
	return nil
}

// ReplayCorpus：第一步先 ValidateCorpus(cases)（違反即 error——直接收
// []CorpusCase 的入口不得依賴呼叫端已驗過，Go constructor 可繞過
// loadCorpus(t)）；再逐案例驗 case.BundleDigest == b.Digest（本函式只收
// baseline bundle；candidate 走 DiffBundles 獨立路徑，見 Task 8）、
// SnapshotDigest(case.Snapshot) == case.FactsDigest、ValidateFactsSnapshot
// （最後防線——自洽 digest 包不住非法 snapshot），再 Evaluate＋CompareCase。
//
// coverage 計數規則（covers_rules 宣告不可自我轉綠）：
//
//	isolated：實際 Violations 的 distinct rule id 必須恰等於 {covers_rules[0]}
//	  且 go verdict blocked、primary 一致——才計入 CoveredRules；宣告與實際不符
//	  → 整體 error（fail loud，不是略過）。
//	precedence：covers_rules 列完整預期 violation 集合——實際 distinct rule id
//	  必須與其完全相等且 ≥2（單一 violation 不構成「勝過」證據），再驗
//	  PrimaryViolation == covers_rules[0]；成立時對 covers_rules 內每個 id 計入
//	  CoveredRules（每個 id 在本案例都有「確實命中」的實際證據）。
//	output（R32 排序證據）：snapshot 必須 ≥2 個 task 且 plan 來源順序刻意非
//	  task_id 序；pass、雙方 RiskDecisions 逐欄相等（CompareCase 已驗）→
//	  OutputEvidence++。不滿足前置形狀的 output 宣告 → error。
func ReplayCorpus(b *CompiledBundle, cases []CorpusCase, runtimeCostLimit uint64) (*CoverageReport, error) {
	if b == nil {
		return nil, fmt.Errorf("domainspec: replay corpus: nil bundle")
	}
	if err := ValidateCorpus(cases); err != nil {
		return nil, err
	}

	report := &CoverageReport{CoveredRules: map[string]int{}}

	for _, c := range cases {
		if c.Kind == CorpusKindAcquisitionFailed {
			report.Exempt++
			continue
		}

		if c.BundleDigest != b.Digest {
			return nil, fmt.Errorf("domainspec: replay corpus: case %q: bundle_digest drift: case=%s baseline=%s", c.Name, c.BundleDigest, b.Digest)
		}
		digest, err := SnapshotDigest(c.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("domainspec: replay corpus: case %q: snapshot digest: %w", c.Name, err)
		}
		if digest != c.FactsDigest {
			return nil, fmt.Errorf("domainspec: replay corpus: case %q: facts_digest drift: recomputed %s != declared %s", c.Name, digest, c.FactsDigest)
		}
		if err := ValidateFactsSnapshot(c.Snapshot); err != nil {
			return nil, fmt.Errorf("domainspec: replay corpus: case %q: snapshot: %w", c.Name, err)
		}

		result, err := Evaluate(b, c.Snapshot, runtimeCostLimit)
		if err != nil {
			return nil, fmt.Errorf("domainspec: replay corpus: case %q: evaluate: %w", c.Name, err)
		}

		ok, _ := CompareCase(b, c.Snapshot, result, *c.GoVerdict)
		if ok {
			report.Consistent++
		} else {
			report.Inconsistent++
			report.Mismatches = append(report.Mismatches, c.Name)
		}

		distinct := distinctRuleIDs(result.Violations)

		switch c.Role {
		case "isolated":
			want := c.CoversRules[0]
			pv, found := PrimaryViolation(b, result)
			if len(distinct) != 1 || !distinct[want] || c.GoVerdict.Outcome != OutcomeBlocked || !found || pv.RuleID != want {
				return nil, fmt.Errorf("domainspec: replay corpus: case %q: role=isolated declares %s but actual distinct violations=%v outcome=%s", c.Name, want, sortedKeys(distinct), c.GoVerdict.Outcome)
			}
			report.CoveredRules[want]++

		case "precedence":
			if !sameRuleIDSet(distinct, c.CoversRules) || len(distinct) < 2 {
				return nil, fmt.Errorf("domainspec: replay corpus: case %q: role=precedence declares %v but actual distinct violations=%v", c.Name, c.CoversRules, sortedKeys(distinct))
			}
			pv, found := PrimaryViolation(b, result)
			if !found || pv.RuleID != c.CoversRules[0] {
				return nil, fmt.Errorf("domainspec: replay corpus: case %q: role=precedence expected primary %s, got %+v (found=%v)", c.Name, c.CoversRules[0], pv, found)
			}
			for _, id := range c.CoversRules {
				report.CoveredRules[id]++
			}

		case "output":
			if err := validateOutputShape(c.Snapshot); err != nil {
				return nil, fmt.Errorf("domainspec: replay corpus: case %q: role=output: %w", c.Name, err)
			}
			if ok && c.GoVerdict.Outcome == OutcomePass {
				report.OutputEvidence++
			}
		}
	}

	for _, rule := range b.Rules {
		if report.CoveredRules[rule.ID] == 0 {
			report.UncoveredRules = append(report.UncoveredRules, rule.ID)
		}
	}
	sort.Strings(report.UncoveredRules)
	sort.Strings(report.Mismatches)

	return report, nil
}

// goVerdictEqual 逐欄比對兩份 GoVerdict（VerifyOracleFreshness 的比對輸入）。
func goVerdictEqual(a, b GoVerdict) bool {
	return a.Outcome == b.Outcome && a.PrimaryRuleID == b.PrimaryRuleID && riskDecisionsEqual(a.RiskDecisions, b.RiskDecisions)
}

// VerifyOracleFreshness（出口 6b 的獨立可驗證 guard）：第一步先
// ValidateCorpus(cases)，違反即 error；再對每筆 evaluated 案例以 recompute
// 重跑 oracle，回傳固化 verdict 與重跑結果不一致的案例名（排序後輸出）。純
// 函式放這裡；recompute dispatcher（含全部 seam）由呼叫端（root freshness
// 檔）注入——internal 測試 package 接不到 root seam。
func VerifyOracleFreshness(cases []CorpusCase, recompute func(CorpusCase) (GoVerdict, error)) ([]string, error) {
	if err := ValidateCorpus(cases); err != nil {
		return nil, err
	}

	var mismatched []string
	for _, c := range cases {
		if c.Kind != CorpusKindEvaluated {
			continue
		}
		got, err := recompute(c)
		if err != nil {
			return nil, fmt.Errorf("domainspec: verify oracle freshness: case %q: recompute: %w", c.Name, err)
		}
		if !goVerdictEqual(got, *c.GoVerdict) {
			mismatched = append(mismatched, c.Name)
		}
	}
	sort.Strings(mismatched)
	return mismatched, nil
}
