package domainspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadCorpus：testdata/corpus/*.json → DecodeCorpusCase → ValidateCorpus（host
// 層目錄走訪，corpus.go 本身無 I/O）。
func loadCorpus(t *testing.T) []CorpusCase {
	t.Helper()
	entries, err := os.ReadDir("testdata/corpus")
	if err != nil {
		t.Fatalf("read testdata/corpus: %v", err)
	}
	var cases []CorpusCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata/corpus", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		c, err := DecodeCorpusCase(data)
		if err != nil {
			t.Fatalf("decode %s: %v", e.Name(), err)
		}
		cases = append(cases, c)
	}
	if err := ValidateCorpus(cases); err != nil {
		t.Fatalf("validate corpus: %v", err)
	}
	return cases
}

// corpusEvaluatedCase：generator 共用尾段——驗 snapshot、算 facts_digest、組
// evaluated CorpusCase（bundle_digest 固定取呼叫端傳入的 baseline b.Digest）。
func corpusEvaluatedCase(t *testing.T, b *CompiledBundle, name string, s *FactsSnapshot, seam, provenance, role string, covers []string, gv GoVerdict) CorpusCase {
	t.Helper()
	if err := ValidateFactsSnapshot(s); err != nil {
		t.Fatalf("case %s: invalid snapshot: %v", name, err)
	}
	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("case %s: snapshot digest: %v", name, err)
	}
	gvCopy := gv
	return CorpusCase{
		Name:            name,
		Kind:            CorpusKindEvaluated,
		EvaluationPhase: s.EvaluationPhase,
		OracleSeam:      seam,
		Provenance:      provenance,
		FactsDigest:     digest,
		BundleDigest:    b.Digest,
		Snapshot:        s,
		GoVerdict:       &gvCopy,
		Role:            role,
		CoversRules:     covers,
	}
}

// isolatedSpec／isolatedSpecs：每條 in-scope 規則一筆隔離違規 fixture，mutate
// 形狀沿用 gate2_bundle_test.go 的 TestGate2BundleIsolatedRuleCoverage 既證
// shapes（唯一命中，已由該測試驗證）。
type isolatedSpec struct {
	ruleID     string
	submit     bool
	seam       string
	provenance string
	mutate     func(*FactsSnapshot)
}

func isolatedSpecs() []isolatedSpec {
	return []isolatedSpec{
		{"R5.submit", true, "gate_service_submit", "gate_service_tests",
			func(s *FactsSnapshot) { s.Request.Value.Gate = "bogus" }},
		{"R6.submit", true, "gatepolicy_validate", "gatepolicy_tests",
			func(s *FactsSnapshot) { s.Request.Value.Subject = "nogood" }},
		{"R7", true, "gatepolicy_validate", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Request.Value.Bindings = append(s.Request.Value.Bindings, s.Request.Value.Bindings[0])
			}},
		{"R8", true, "gatepolicy_validate", "gatepolicy_tests",
			func(s *FactsSnapshot) { removeBindingKind(s, "plan") }},
		{"R9", true, "gatepolicy_validate", "gatepolicy_tests",
			func(s *FactsSnapshot) { setBindingDigest(s, "spec_manifest", "bogus") }},
		{"R3", false, "app_gatedecide", "synthetic",
			func(s *FactsSnapshot) { s.Approver.Value.Name, s.Approver.Value.Email = "", "" }},
		{"R1", false, "gate_service_prepare", "gate_service_tests",
			func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("weird")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
			}},
		{"R2", false, "gate_service_prepare", "gate_service_tests",
			func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("rejected")}
				s.Reason = Fact[string]{Presence: Known, Value: strPtr("")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
				s.RiskSelections = Fact[[]RiskSelection]{Presence: Known, Value: &[]RiskSelection{}}
			}},
		{"R4", false, "gate_service_prepare", "gate_service_tests",
			func(s *FactsSnapshot) { s.Entry.Value.HasRecord = true }},
		{"R5.decide", false, "gate_service_prepare", "gate_service_tests",
			func(s *FactsSnapshot) { s.Request.Value.Gate = "bogus" }},
		{"R11", false, "gatepolicy_reconcile", "gatepolicy_tests",
			func(s *FactsSnapshot) { setBindingDigest(s, "spec_manifest", "sha256:"+repeatChar("1", 64)) }},
		{"R12", false, "gatepolicy_reconcile", "gatepolicy_tests",
			func(s *FactsSnapshot) { s.BaseCommitState = Fact[string]{Presence: Known, Value: strPtr("missing")} }},
		{"R21", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("rejected")}
				s.Reason = Fact[string]{Presence: Known, Value: strPtr("not good enough")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
			}},
		{"R6.decide", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) { s.Request.Value.Subject = "nogood" }},
		{"R24", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				sel := (*s.RiskSelections.Value)[0]
				*s.RiskSelections.Value = append(*s.RiskSelections.Value, sel)
			}},
		{"R25", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) { *s.RiskSelections.Value = []RiskSelection{} }},
		{"R27", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "medium"}}
			}},
		{"R28.minimum", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "extreme"
				s.RiskPolicy.Value.Rules = []RiskRule{}
				s.RiskPolicy.Value.DefaultTier = "extreme"
			}},
		{"R28.planner", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) { s.Plan.Value.Tasks[0].PlannerRiskTier = "extreme" }},
		{"R29", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "high"
				s.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
				s.Plan.Value.Tasks[0].PlannerRiskTier = "low"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "high"
			}},
		{"R28.selected", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) { (*s.RiskSelections.Value)[0].SelectedRiskTier = "extreme" }},
		{"R30", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "high"
				s.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
				s.Plan.Value.Tasks[0].PlannerRiskTier = "high"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "low"
				(*s.RiskSelections.Value)[0].OverrideReason = "because"
			}},
		{"R31", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].PlannerRiskTier = "high"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "low"
				(*s.RiskSelections.Value)[0].OverrideReason = ""
			}},
		{"R26", false, "gatepolicy_build", "gatepolicy_tests",
			func(s *FactsSnapshot) {
				*s.RiskSelections.Value = append(*s.RiskSelections.Value, RiskSelection{
					TaskID: "T99", SelectedRiskTier: "low", OverrideReason: "",
				})
			}},
		{"R16", false, "escalation", "escalation_tests",
			func(s *FactsSnapshot) {
				s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
					{EscalationID: "E1", State: "open", BlockScope: "workspace"},
				}}
			}},
	}
}

func buildIsolatedCorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	var out []CorpusCase
	for _, spec := range isolatedSpecs() {
		var s *FactsSnapshot
		if spec.submit {
			s = validSubmitSnapshot(t)
		} else {
			s = mustSnapshot(t)
		}
		spec.mutate(s)
		out = append(out, corpusEvaluatedCase(t, b, "isolated-"+spec.ruleID, s, spec.seam, spec.provenance,
			"isolated", []string{spec.ruleID}, GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: spec.ruleID}))
	}
	return out
}

// twoTaskOutputSnapshot：role=output 的 R32 排序證據形狀——≥2 個 task、plan
// 來源順序刻意非 task_id 序（T2 在 T1 前），其餘沿 mustSnapshot 基準保持乾淨。
func twoTaskOutputSnapshot(t *testing.T) *FactsSnapshot {
	t.Helper()
	s := mustSnapshot(t)
	s.Plan.Value.Tasks = []PlanTask{
		{ID: "T2", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "low", Impact: Impact{Contexts: []string{"gate"}}},
		{ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low", Impact: Impact{Contexts: []string{"gate"}}},
	}
	sels := []RiskSelection{
		{TaskID: "T2", SelectedRiskTier: "low", OverrideReason: ""},
		{TaskID: "T1", SelectedRiskTier: "low", OverrideReason: ""},
	}
	s.RiskSelections.Value = &sels
	return s
}

func buildCleanCorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	var out []CorpusCase

	submitClean := validSubmitSnapshot(t)
	out = append(out, corpusEvaluatedCase(t, b, "clean-submit", submitClean,
		"gatepolicy_validate", "gatepolicy_tests", "none", nil, GoVerdict{Outcome: OutcomePass}))

	decideApproved := twoTaskOutputSnapshot(t)
	out = append(out, corpusEvaluatedCase(t, b, "clean-decide-approved", decideApproved,
		"gatepolicy_build", "synthetic", "output", nil,
		GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(decideApproved)}))

	rejected, err := DecodeFactsSnapshot([]byte(validRejectedSnapshotJSON()))
	if err != nil {
		t.Fatalf("decode valid rejected: %v", err)
	}
	out = append(out, corpusEvaluatedCase(t, b, "clean-decide-rejected", rejected,
		"gatepolicy_build", "synthetic", "none", nil, GoVerdict{Outcome: OutcomePass}))

	return out
}

// buildPrecedenceCorpusCases：六筆 precedence 案例（spec 三筆＋跨 gate step
// 兩筆＋kind occurrence 一筆），每筆的 CoversRules 首位為預期 primary。
func buildPrecedenceCorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	var out []CorpusCase

	// 1. R24 + R30 → R24（同 step_rank，R24 pre_loop 先於 R30 task_loop）。
	s1 := mustSnapshot(t)
	s1.Plan.Value.Tasks[0].MinimumRiskTier = "high"
	s1.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
	s1.Plan.Value.Tasks[0].PlannerRiskTier = "high"
	(*s1.RiskSelections.Value)[0].SelectedRiskTier = "low"
	(*s1.RiskSelections.Value)[0].OverrideReason = "because"
	dupSel := (*s1.RiskSelections.Value)[0]
	*s1.RiskSelections.Value = append(*s1.RiskSelections.Value, dupSel)
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R24-vs-R30", s1,
		"gatepolicy_build", "synthetic", "precedence", []string{"R24", "R30"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R24"}))

	// 2. R30@task0 + R25@task1 → R30（同 step_rank/stage，source_index 0<1）。
	s2 := mustSnapshot(t)
	s2.Plan.Value.Tasks = []PlanTask{
		{ID: "T1", SourceIndex: 0, MinimumRiskTier: "high", PlannerRiskTier: "high", Impact: Impact{Contexts: []string{"gate"}}},
		{ID: "T2", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low", Impact: Impact{}},
	}
	s2.RiskPolicy.Value.DefaultTier = "low"
	s2.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
	sels2 := []RiskSelection{{TaskID: "T1", SelectedRiskTier: "low", OverrideReason: "because"}}
	s2.RiskSelections.Value = &sels2
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R30-at-T1-vs-R25-at-T2", s2,
		"gatepolicy_build", "synthetic", "precedence", []string{"R30", "R25"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R30"}))

	// 3. R31 + R26 → R31（R31 task_loop 先於 R26 post_loop）。
	s3 := mustSnapshot(t)
	s3.Plan.Value.Tasks[0].PlannerRiskTier = "high"
	(*s3.RiskSelections.Value)[0].SelectedRiskTier = "low"
	(*s3.RiskSelections.Value)[0].OverrideReason = ""
	*s3.RiskSelections.Value = append(*s3.RiskSelections.Value, RiskSelection{
		TaskID: "T99", SelectedRiskTier: "low", OverrideReason: "",
	})
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R31-vs-R26", s3,
		"gatepolicy_build", "synthetic", "precedence", []string{"R31", "R26"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R31"}))

	// 4. R3 + R24 → R3（跨 gate step；R3 step_rank 1 遠早於 R24 step_rank 10）。
	s4 := mustSnapshot(t)
	s4.Approver.Value.Name, s4.Approver.Value.Email = "", ""
	dupSel4 := (*s4.RiskSelections.Value)[0]
	*s4.RiskSelections.Value = append(*s4.RiskSelections.Value, dupSel4)
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R3-vs-R24", s4,
		"app_gatedecide", "synthetic", "precedence", []string{"R3", "R24"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R3"}))

	// 5. R30 + R16 → R30（跨 gate step；R30 step_rank 10 早於 R16 step_rank 11）。
	s5 := mustSnapshot(t)
	s5.Plan.Value.Tasks[0].MinimumRiskTier = "high"
	s5.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
	s5.Plan.Value.Tasks[0].PlannerRiskTier = "high"
	(*s5.RiskSelections.Value)[0].SelectedRiskTier = "low"
	(*s5.RiskSelections.Value)[0].OverrideReason = "because"
	s5.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
		{EscalationID: "E30", State: "open", BlockScope: "workspace"},
	}}
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R30-vs-R16", s5,
		"app_gatedecide", "synthetic", "precedence", []string{"R30", "R16"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R30"}))

	// 6. spec_manifest digest 錯（idx0）＋ plan kind missing（idx1）→ R9（kind
	// occurrence rank：R9 較早的 kind index 先於 R8 較晚的 kind index）。
	s6 := validSubmitSnapshot(t)
	setBindingDigest(s6, "spec_manifest", "bogus")
	removeBindingKind(s6, "plan")
	out = append(out, corpusEvaluatedCase(t, b, "precedence-R9-vs-R8-kind-occurrence", s6,
		"gatepolicy_validate", "synthetic", "precedence", []string{"R9", "R8"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R9"}))

	return out
}

// buildAlignmentCorpusCases：對齊警訊各一（R16 hard、R11 role!="" 不參與）。
func buildAlignmentCorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	var out []CorpusCase

	// R16 hard 對齊警訊：facts schema 沒有 hard 欄位，本案例只固化 CEL 端的
	// block_scope 判定結果；「忽略 hard」的另一半證據（production 端以
	// escalation.Item{Hard:true} 呼叫 BlockingFor 仍回 blocked）留給 Task 8
	// root freshness harness 的 escalation seam adapter 重算比對。
	s1 := mustSnapshot(t)
	s1.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
		{EscalationID: "EH-hard", State: "open", BlockScope: "gate2:P1"},
	}}
	out = append(out, corpusEvaluatedCase(t, b, "alignment-R16-hard-ignored", s1,
		"escalation", "escalation_tests", "alignment", []string{"R16"},
		GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R16"}))

	// R11 role!="" 不參與：額外附加一個 role!="" 的 spec_manifest binding，digest
	// 與 current 不符——R11 的 CEL when 只篩 role=="" 的 binding，額外 binding
	// 必須被忽略、truth 仍為 true。
	s2 := mustSnapshot(t)
	s2.Request.Value.Bindings = append(s2.Request.Value.Bindings, Binding{
		Kind: "spec_manifest", Role: "secondary", Ref: "", Digest: "sha256:" + repeatChar("2", 64),
	})
	out = append(out, corpusEvaluatedCase(t, b, "alignment-R11-role-not-participating", s2,
		"gatepolicy_reconcile", "gatepolicy_tests", "alignment", []string{"R11"},
		GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s2)}))

	return out
}

// buildAcquisitionFailedCorpusCases：acquisition_failed 三筆——host 邊界讀取
// 失敗（LoadAt 錯、rev-parse fatal）與 dirty-tree（submit 前 worktree 非乾淨）。
// phase↔seam 契約凍結 host_boundary 只在 submit 合法，三筆一律 phase=submit。
func buildAcquisitionFailedCorpusCases() []CorpusCase {
	return []CorpusCase{
		{
			Name: "acquisition-failed-loadat-error", Kind: CorpusKindAcquisitionFailed,
			EvaluationPhase: "submit", OracleSeam: "host_boundary", Provenance: "host_boundary",
			Role: "none",
			Reason: "LoadAt(spec_manifest/plan/risk_policy) 讀取 base_commit 對應 blob 失敗" +
				"（對照 gate2.go:270-281 LoadAt 錯誤路徑）：facts 無法組成，計入 Exempt。",
		},
		{
			Name: "acquisition-failed-revparse-fatal", Kind: CorpusKindAcquisitionFailed,
			EvaluationPhase: "submit", OracleSeam: "host_boundary", Provenance: "host_boundary",
			Role: "none",
			Reason: "git rev-parse --verify --quiet 對 base_commit 回傳非 exit0/exit1 的 fatal 錯誤" +
				"（對照 gate2.go:234-248，非 stale 分支）：facts 無法組成，計入 Exempt。",
		},
		{
			Name: "acquisition-failed-dirty-worktree", Kind: CorpusKindAcquisitionFailed,
			EvaluationPhase: "submit", OracleSeam: "host_boundary", Provenance: "host_boundary",
			Role:   "none",
			Reason: "submit 前 worktree 非乾淨（dirty tree），host 邊界檢查中止：facts 無法組成，計入 Exempt。",
		},
	}
}

// ---- A9 真實案例（provenance a9_workspace，出口 5 收尾）----
//
// 來源：~/playground/wb-accept-a9g2（唯讀擷取，一次性 offline 步驟，未修改該
// workspace 任何檔案）——
//
//   - .workbench/gate.jsonl：op_id 01M0XZEHN4000... 的 gate_request／
//     approval_record（初次核可，02:47:36／02:47:54）、transition to stale
//     （02:48:43，cause="plan changed"，evidence_ref=579859...）、
//     op_id 01M0XZJR9M0002... 的 gate_request／approval_record（修正版核可，
//     02:49:54／02:50:29，metadata.risk_decisions 含 override_reason）。
//   - .workbench/escalation.jsonl：escalation_item（stale，hard=true，
//     block_scope=gate2:a9，source_ref=舊 approval_id）、escalation_transition
//     to resolved（reason=superseded-by:新 approval_id）——證實 plan rev2 修正
//     後的事實：無 rejected record，stale 完全走 escalation 通道。
//   - git log（唯讀 `git show`）：84125c6（plan/a9.yaml 初版：minimum_risk_tier=
//     medium／planner_risk_tier=medium；plan/risk-policy.yaml：default_tier=
//     medium、rules:[]）、bed3640（同檔案 planner_risk_tier 上調至 high，
//     觸發第二次送核；risk-policy.yaml 未變）。
//
// 三筆 FactsSnapshot 的 binding digest／git commit OID／risk_decisions 逐欄
// 直接抄自上述 journal／git show 輸出（無合成值）；go_verdict 依 production
// 語意手動標定（推導過程見下方個別函式註解），落地後由
// domainspec_oracle_freshness_test.go 的 gatepolicy_build／gatepolicy_reconcile
// seam adapter（真正呼叫 Gate2Policy.BuildDecision／ReconcileBindings）機械
// 驗證——若驗證發現手動標定有誤，屬 shadow misalignment，必須調查後如實記載
// （不得為了讓測試綠燈而悄悄改標定去遷就結果）。
//
// entry facts 三筆一律 {exists:true, has_request:true, has_record:false}：
// 沿用整個 corpus（含 clean-decide-approved／isolated-R11／R12／
// alignment-R11-role-not-participating 等既有 decide-phase 案例）的唯一慣例
// ——has_record=true 只在專門的「R4」隔離案例出現（R4 的 CEL when 直接命中
// entry.has_record）。ReconcileBindings 本身也不讀 entry（只有
// gate_service_prepare／PrepareDecision 這個不同的 seam 才檢查 has_record），
// 兩者一致，不是巧合遷就。
const (
	a9SpecDigest       = "sha256:f3e6751e859ccc67509eedb1e9052225d517f1e359c9ca72a609565e9e821a5d"
	a9RiskPolicyDigest = "sha256:696e50f199e780567007611967c04cf08d39002e57432243b2ba11b15e61cbfb"
	a9PermDigest       = "sha256:769cbef3e93f638d382acc19c3c2decf13a281b83454a2cb0b68424a1bb0fa92"
	a9Plan1Digest      = "sha256:5404e2d89372e25187315fb99dea7f0f2d8307e797694363a14c13440de49f81"
	a9Plan2Digest      = "sha256:579859307eda271d04ad88e604a832a6a88a01f3296623f40d6e285f26b531a6"
	a9BaseCommit1      = "git:sha1:84125c667473cc10ca0f9fd1ebbde54ff373763b"
	a9BaseCommit2      = "git:sha1:bed3640660df4a7e470e5e0335ce5897da4c9f56"
	a9OverrideReason   = "walking skeleton 僅動 gate journal 附加路徑，回歸面窄；抽驗用途接受中風險控管"
)

func a9RequestBindings(planDigest, baseCommit string) []Binding {
	return []Binding{
		{Kind: "spec_manifest", Role: "", Ref: "spec/", Digest: a9SpecDigest},
		{Kind: "plan", Role: "", Ref: "plan/", Digest: planDigest},
		{Kind: "base_commit", Role: "", Ref: "HEAD", Digest: baseCommit},
		{Kind: "risk_policy", Role: "", Ref: "plan/risk-policy.yaml", Digest: a9RiskPolicyDigest},
		{Kind: "permission_manifest", Role: "", Ref: "plan/permissions/", Digest: a9PermDigest},
	}
}

func a9Entry() *Entry { return &Entry{Exists: true, HasRequest: true, HasRecord: false} }

func a9Approver() *Approver { return &Approver{Name: "eason_tseng", Email: ""} }

// a9InitialApprovedSnapshot（a9-1-initial-approved）：approval_id
// 01M0XZEHN4000DRB364G09H7NM 初次核可當下（84125c6：T1 minimum=medium／
// planner=medium，risk-policy 無 rule，選擇 selected=medium 無 override）。
// bindings＝current（尚未 stale），R11 不應命中；R27（recomputed minimum 對
// risk_policy default_tier）／R29-R31（tier 比較）皆因三值相等而不命中——
// 全域應為 pass，risk_decisions 逐位元組等於 gate.jsonl 記錄的
// metadata.risk_decisions。
func a9InitialApprovedSnapshot() *FactsSnapshot {
	return &FactsSnapshot{
		SchemaVersion: 1, EvaluationPhase: "decide",
		Decision: Fact[string]{Presence: Known, Value: strPtr("approved")},
		Reason:   Fact[string]{Presence: Known, Value: strPtr("")},
		Approver: Fact[Approver]{Presence: Known, Value: a9Approver()},
		Entry:    Fact[Entry]{Presence: Known, Value: a9Entry()},
		Request: Fact[Request]{Presence: Known, Value: &Request{
			Gate: "gate2", Subject: "plan:a9", Bindings: a9RequestBindings(a9Plan1Digest, a9BaseCommit1),
		}},
		Current: Fact[Current]{Presence: Known, Value: &Current{
			SpecManifest: a9SpecDigest, PlanManifest: a9Plan1Digest,
			RiskPolicy: a9RiskPolicyDigest, PermissionManifest: a9PermDigest,
		}},
		BaseCommitState: Fact[string]{Presence: Known, Value: strPtr("ok")},
		Plan: Fact[PlanFacts]{Presence: Known, Value: &PlanFacts{Tasks: []PlanTask{
			{ID: "T1", SourceIndex: 0, MinimumRiskTier: "medium", PlannerRiskTier: "medium",
				Impact: Impact{Contexts: []string{"gate"}, Modules: []string{"internal/gate"}}},
		}}},
		RiskPolicy: Fact[RiskPolicyFacts]{Presence: Known, Value: &RiskPolicyFacts{DefaultTier: "medium", Rules: []RiskRule{}}},
		RiskSelections: Fact[[]RiskSelection]{Presence: Known, Value: &[]RiskSelection{
			{TaskID: "T1", SelectedRiskTier: "medium", OverrideReason: ""},
		}},
		Escalations: Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{}},
	}
}

// a9StaleBlockedSnapshot（a9-2-stale-blocked-r11）：以 escalation.jsonl 記錄的
// stale 事件重建「舊 pending（實為已核可）對新 current」時點的 facts——
// request.bindings 維持 01M0XZEHN4000DRB364G09H7NM 核可當下凍結的值
// （plan digest=a9Plan1Digest／base_commit=84125c6），current.plan_manifest
// 換成 gate.jsonl transition 記錄的 evidence_ref（a9Plan2Digest，即修正版
// plan 內容的即時 digest，早於它被 commit 成 bed3640）；spec_manifest／
// risk_policy／permission_manifest 三者 current 與 bound 相同（journal 的
// transition 只有一筆 cause="plan changed"，代表其餘三者未變）。
//
// 手動核對 25 條規則：僅 R11（bound plan digest != current plan_manifest）
// 命中；base_commit_state=ok（84125c6 仍是有效 commit，§3.9 歷史錨點不因
// HEAD 前進而 stale）→ 不觸發 base_commit missing 分支；plan/risk_policy/
// risk_selections 維持初次核可的值（medium/medium／無 rule／selected=medium
// 無 override）→ R27/R29/R30/R31 均不命中。角色因此合法標為 isolated，
// covers_rules=[R11]（唯一符合條件的真實案例，不是為了湊 coverage 硬套）。
func a9StaleBlockedSnapshot() *FactsSnapshot {
	return &FactsSnapshot{
		SchemaVersion: 1, EvaluationPhase: "decide",
		Decision: Fact[string]{Presence: Known, Value: strPtr("approved")},
		Reason:   Fact[string]{Presence: Known, Value: strPtr("")},
		Approver: Fact[Approver]{Presence: Known, Value: a9Approver()},
		Entry:    Fact[Entry]{Presence: Known, Value: a9Entry()},
		Request: Fact[Request]{Presence: Known, Value: &Request{
			Gate: "gate2", Subject: "plan:a9", Bindings: a9RequestBindings(a9Plan1Digest, a9BaseCommit1),
		}},
		Current: Fact[Current]{Presence: Known, Value: &Current{
			SpecManifest: a9SpecDigest, PlanManifest: a9Plan2Digest, // ← stale：evidence_ref
			RiskPolicy: a9RiskPolicyDigest, PermissionManifest: a9PermDigest,
		}},
		BaseCommitState: Fact[string]{Presence: Known, Value: strPtr("ok")},
		Plan: Fact[PlanFacts]{Presence: Known, Value: &PlanFacts{Tasks: []PlanTask{
			{ID: "T1", SourceIndex: 0, MinimumRiskTier: "medium", PlannerRiskTier: "medium",
				Impact: Impact{Contexts: []string{"gate"}, Modules: []string{"internal/gate"}}},
		}}},
		RiskPolicy: Fact[RiskPolicyFacts]{Presence: Known, Value: &RiskPolicyFacts{DefaultTier: "medium", Rules: []RiskRule{}}},
		RiskSelections: Fact[[]RiskSelection]{Presence: Known, Value: &[]RiskSelection{
			{TaskID: "T1", SelectedRiskTier: "medium", OverrideReason: ""},
		}},
		Escalations: Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{}},
	}
}

// a9CorrectedApprovedSnapshot（a9-3-corrected-approved-override）：approval_id
// 01M0XZJR9M0002FYPQFQH63E14 修正版核可（bed3640：T1 planner 上調至 high，
// minimum 不變仍 medium），selected=medium＋override_reason 非空（journal
// metadata 逐字抄錄）。bindings＝current（新送核，未 stale）。手動核對：
// selected(2) < planner(3) 為真，但 override_reason 非空 → R31 不命中
// （override 例外正確生效）；其餘規則同 a9InitialApprovedSnapshot 分析不命中
// → 全域 pass，risk_decisions 逐位元組等於 gate.jsonl 記錄值。
func a9CorrectedApprovedSnapshot() *FactsSnapshot {
	return &FactsSnapshot{
		SchemaVersion: 1, EvaluationPhase: "decide",
		Decision: Fact[string]{Presence: Known, Value: strPtr("approved")},
		Reason:   Fact[string]{Presence: Known, Value: strPtr("")},
		Approver: Fact[Approver]{Presence: Known, Value: a9Approver()},
		Entry:    Fact[Entry]{Presence: Known, Value: a9Entry()},
		Request: Fact[Request]{Presence: Known, Value: &Request{
			Gate: "gate2", Subject: "plan:a9", Bindings: a9RequestBindings(a9Plan2Digest, a9BaseCommit2),
		}},
		Current: Fact[Current]{Presence: Known, Value: &Current{
			SpecManifest: a9SpecDigest, PlanManifest: a9Plan2Digest,
			RiskPolicy: a9RiskPolicyDigest, PermissionManifest: a9PermDigest,
		}},
		BaseCommitState: Fact[string]{Presence: Known, Value: strPtr("ok")},
		Plan: Fact[PlanFacts]{Presence: Known, Value: &PlanFacts{Tasks: []PlanTask{
			{ID: "T1", SourceIndex: 0, MinimumRiskTier: "medium", PlannerRiskTier: "high",
				Impact: Impact{Contexts: []string{"gate"}, Modules: []string{"internal/gate"}}},
		}}},
		RiskPolicy: Fact[RiskPolicyFacts]{Presence: Known, Value: &RiskPolicyFacts{DefaultTier: "medium", Rules: []RiskRule{}}},
		RiskSelections: Fact[[]RiskSelection]{Presence: Known, Value: &[]RiskSelection{
			{TaskID: "T1", SelectedRiskTier: "medium", OverrideReason: a9OverrideReason},
		}},
		Escalations: Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{}},
	}
}

func buildA9CorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	return []CorpusCase{
		corpusEvaluatedCase(t, b, "a9-1-initial-approved", a9InitialApprovedSnapshot(),
			"gatepolicy_build", "a9_workspace", "alignment", nil,
			GoVerdict{Outcome: OutcomePass, RiskDecisions: []RiskDecision{
				{TaskID: "T1", MinimumRiskTier: "medium", PlannerRiskTier: "medium", SelectedRiskTier: "medium", OverrideReason: ""},
			}}),
		corpusEvaluatedCase(t, b, "a9-2-stale-blocked-r11", a9StaleBlockedSnapshot(),
			"gatepolicy_reconcile", "a9_workspace", "isolated", []string{"R11"},
			GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R11"}),
		corpusEvaluatedCase(t, b, "a9-3-corrected-approved-override", a9CorrectedApprovedSnapshot(),
			"gatepolicy_build", "a9_workspace", "alignment", nil,
			GoVerdict{Outcome: OutcomePass, RiskDecisions: []RiskDecision{
				{TaskID: "T1", MinimumRiskTier: "medium", PlannerRiskTier: "high", SelectedRiskTier: "medium", OverrideReason: a9OverrideReason},
			}}),
	}
}

func buildAllCorpusCases(t *testing.T, b *CompiledBundle) []CorpusCase {
	t.Helper()
	var out []CorpusCase
	out = append(out, buildIsolatedCorpusCases(t, b)...)
	out = append(out, buildCleanCorpusCases(t, b)...)
	out = append(out, buildPrecedenceCorpusCases(t, b)...)
	out = append(out, buildAlignmentCorpusCases(t, b)...)
	out = append(out, buildA9CorpusCases(t, b)...)
	out = append(out, buildAcquisitionFailedCorpusCases()...)
	return out
}

// TestUpdateCorpusInternal：corpus JSON 首次固化／重新固化的 generator——只在
// UPDATE_CORPUS_INTERNAL=1 時執行（CI 不跑），從 Go case constructors 出發：
// 建 snapshot → ValidateFactsSnapshot（顯式，constructors 不經 JSON decoder）→
// 手動依 production 語意標定 go_verdict（不呼叫 Evaluate，避免用 CEL 端結果
// 反向填充 CEL 端的比對基準）→ SnapshotDigest／bundle digest → 全批次先過
// ValidateCorpus（驗完才落檔，任一筆違反即整批不寫檔）→ marshal 落檔。
func TestUpdateCorpusInternal(t *testing.T) {
	if os.Getenv("UPDATE_CORPUS_INTERNAL") != "1" {
		t.Skip("set UPDATE_CORPUS_INTERNAL=1 to regenerate internal/domainspec/testdata/corpus/*.json")
	}
	b := loadGate2Bundle(t)
	cases := buildAllCorpusCases(t, b)
	if err := ValidateCorpus(cases); err != nil {
		t.Fatalf("generated corpus failed validation, nothing written: %v", err)
	}

	dir := "testdata/corpus"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	existing, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range existing {
		if strings.HasSuffix(e.Name(), ".json") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				t.Fatalf("remove stale %s: %v", e.Name(), err)
			}
		}
	}
	for _, c := range cases {
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			t.Fatalf("marshal case %s: %v", c.Name, err)
		}
		path := filepath.Join(dir, c.Name+".json")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Logf("wrote %d corpus cases to %s", len(cases), dir)
}

// ---- Required tests (brief Step 1) ----

func TestReplayCorpusAllConsistent(t *testing.T) {
	b := loadGate2Bundle(t)
	cases := loadCorpus(t)
	report, err := ReplayCorpus(b, cases, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("replay corpus: %v", err)
	}
	if len(report.Mismatches) != 0 {
		t.Fatalf("unexpected mismatches: %v", report.Mismatches)
	}
	if report.Inconsistent != 0 {
		t.Fatalf("unexpected inconsistent count: %d", report.Inconsistent)
	}
}

// TestCorpusBundleDigestMatchesLiveBundle：corpus JSON 固化的 bundle_digest 是
// 字面值（非在測試時現算），必須逐筆等於目前 committed gate2-bundle.yaml 的
// LoadBundle digest——bundle 內容漂移時本測試要先於 ReplayCorpus 的整體錯誤，
// 在明顯的地方直接指出「哪個 fixture 該重新固化」。
func TestCorpusBundleDigestMatchesLiveBundle(t *testing.T) {
	b := loadGate2Bundle(t)
	cases := loadCorpus(t)
	for _, c := range cases {
		if c.Kind != CorpusKindEvaluated {
			continue
		}
		if c.BundleDigest != b.Digest {
			t.Errorf("case %q: fixed bundle_digest %s != live bundle digest %s (regenerate with UPDATE_CORPUS_INTERNAL=1)", c.Name, c.BundleDigest, b.Digest)
		}
	}
}

func TestCoverageComplete(t *testing.T) {
	b := loadGate2Bundle(t)
	cases := loadCorpus(t)
	report, err := ReplayCorpus(b, cases, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("replay corpus: %v", err)
	}
	exemptRules := map[string]bool{
		// R15：scope 導出邏輯內嵌在 R16 when 內，沒有獨立 rule id，由
		// alignment-R16-hard-ignored／precedence 的 scope-sensitive 案例佐證。
		"R15": true,
		// host 層（不進 CEL，spec §1）：讀取錯誤／supersession／lineage 等
		// I/O 與生命週期檢查，非 CEL 規則。
		"R13": true, "R17": true, "R19": true, "R20": true, "lineage": true,
	}
	for _, id := range report.UncoveredRules {
		if !exemptRules[id] {
			t.Fatalf("rule %q is uncovered and not in the exempt table", id)
		}
	}
	// R32（RiskDecisions 排序）不豁免：必須有 >=1 筆 OutputEvidence。
	if report.OutputEvidence < 1 {
		t.Fatalf("R32 output evidence must be >= 1, got %d", report.OutputEvidence)
	}
}

func TestCorpusUnionValidation(t *testing.T) {
	b := loadGate2Bundle(t)
	valid := mustSnapshot(t)
	digest, err := SnapshotDigest(valid)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	baseEvaluated := CorpusCase{
		Name: "x", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: valid,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(valid)},
		Role:      "none",
	}
	failed := CorpusCase{
		Name: "y", Kind: CorpusKindAcquisitionFailed, EvaluationPhase: "submit",
		OracleSeam: "host_boundary", Provenance: "host_boundary", Reason: "boom", Role: "none",
	}

	mustRejectFrom := func(name string, base CorpusCase, mutate func(c *CorpusCase)) {
		t.Helper()
		c := base
		mutate(&c)
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if _, err := DecodeCorpusCase(data); err == nil {
			t.Fatalf("%s: must be rejected", name)
		}
	}

	mustRejectFrom("evaluated missing facts_digest", baseEvaluated, func(c *CorpusCase) { c.FactsDigest = "" })
	mustRejectFrom("evaluated missing bundle_digest", baseEvaluated, func(c *CorpusCase) { c.BundleDigest = "" })
	mustRejectFrom("evaluated with reason set", baseEvaluated, func(c *CorpusCase) { c.Reason = "should not be here" })
	mustRejectFrom("evaluated missing snapshot", baseEvaluated, func(c *CorpusCase) { c.Snapshot = nil })
	mustRejectFrom("evaluated missing go_verdict", baseEvaluated, func(c *CorpusCase) { c.GoVerdict = nil })

	mustRejectFrom("acquisition_failed missing reason", failed, func(c *CorpusCase) { c.Reason = "" })
	mustRejectFrom("acquisition_failed with snapshot", failed, func(c *CorpusCase) { c.Snapshot = valid })
	mustRejectFrom("acquisition_failed with go_verdict", failed, func(c *CorpusCase) {
		c.GoVerdict = &GoVerdict{Outcome: OutcomePass}
	})
	mustRejectFrom("acquisition_failed with facts_digest", failed, func(c *CorpusCase) { c.FactsDigest = digest })
	mustRejectFrom("acquisition_failed with bundle_digest", failed, func(c *CorpusCase) { c.BundleDigest = b.Digest })
}

func indexOfCase(t *testing.T, cases []CorpusCase, name string) int {
	t.Helper()
	for i, c := range cases {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("case %q not found", name)
	return -1
}

func TestCorpusDigestDriftFailsLoud(t *testing.T) {
	b := loadGate2Bundle(t)

	t.Run("facts_digest drift", func(t *testing.T) {
		cases := loadCorpus(t)
		idx := indexOfCase(t, cases, "isolated-R3")
		snap := *cases[idx].Snapshot // 淺層複本，避免動到 loadCorpus 的其他斷言用資料
		reason := "tampered-without-recomputing-digest"
		snap.Reason = Fact[string]{Presence: Known, Value: &reason}
		cases[idx].Snapshot = &snap // facts_digest 保留原值，未同步更新 → drift
		if _, err := ReplayCorpus(b, cases, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatal("mutated snapshot without digest update must be rejected")
		}
	})

	t.Run("bundle_digest mismatch", func(t *testing.T) {
		cases := loadCorpus(t)
		idx := indexOfCase(t, cases, "isolated-R3")
		cases[idx].BundleDigest = "sha256:" + repeatChar("f", 64)
		if _, err := ReplayCorpus(b, cases, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatal("bundle_digest mismatch must be rejected")
		}
	})
}

func TestCoverageRejectsMisdeclaredIsolated(t *testing.T) {
	b := loadGate2Bundle(t)
	// 沿用 precedence-R24-vs-R30 的 snapshot 形狀（實際 distinct violations 是
	// {R24,R30} 兩個），但宣稱是 isolated-R30——宣告不符實際證據，必須被拒。
	s := mustSnapshot(t)
	s.Plan.Value.Tasks[0].MinimumRiskTier = "high"
	s.RiskPolicy.Value.Rules = []RiskRule{{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"}}
	s.Plan.Value.Tasks[0].PlannerRiskTier = "high"
	(*s.RiskSelections.Value)[0].SelectedRiskTier = "low"
	(*s.RiskSelections.Value)[0].OverrideReason = "because"
	dup := (*s.RiskSelections.Value)[0]
	*s.RiskSelections.Value = append(*s.RiskSelections.Value, dup)

	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	c := CorpusCase{
		Name: "misdeclared-isolated", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R30"},
		Role:      "isolated", CoversRules: []string{"R30"},
	}
	if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("misdeclared isolated case (2 distinct violations) must be rejected")
	}
}

func TestCorpusRejectsBadSourceIndexEvenWithSelfConsistentDigest(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	// 竄改唯一 task 的 source_index，其餘欄位維持自洽——validatePlanSourceIndex
	// 拒絕的唯一觸發點。
	s.Plan.Value.Tasks[0].SourceIndex = 1

	digest, err := SnapshotDigest(s) // 就地重算：digest 自洽，但 snapshot 本身非法
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	c := CorpusCase{
		Name: "bad-source-index", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s)},
		Role:      "none",
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := DecodeCorpusCase(data); err == nil {
		t.Fatal("bad source_index must be rejected even with self-consistent facts_digest")
	}
	if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("ReplayCorpus must also reject bad source_index directly (bypassing DecodeCorpusCase)")
	}
}

func TestCoverageRejectsSingleViolationPrecedence(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	s.Approver.Value.Name, s.Approver.Value.Email = "", "" // 只觸發 R3 一項違規
	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	c := CorpusCase{
		Name: "misdeclared-precedence", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "app_gatedecide", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomeBlocked, PrimaryRuleID: "R3"},
		Role:      "precedence", CoversRules: []string{"R3", "R24"},
	}
	if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("precedence with only 1 actual distinct violation must be rejected")
	}
}

func TestOutputEvidenceRequiresMultiTaskNonIDOrder(t *testing.T) {
	b := loadGate2Bundle(t)

	buildCase := func(t *testing.T, name string, s *FactsSnapshot) CorpusCase {
		t.Helper()
		digest, err := SnapshotDigest(s)
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		return CorpusCase{
			Name: name, Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
			OracleSeam: "gatepolicy_build", Provenance: "synthetic",
			FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
			GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s)},
			Role:      "output",
		}
	}

	t.Run("single task", func(t *testing.T) {
		s := mustSnapshot(t) // 只有一個 task
		c := buildCase(t, "output-single-task", s)
		if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatal("role=output with < 2 tasks must be rejected")
		}
	})

	t.Run("already task_id sorted", func(t *testing.T) {
		s := mustSnapshot(t)
		s.Plan.Value.Tasks = []PlanTask{
			{ID: "T1", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "low", Impact: Impact{Contexts: []string{"gate"}}},
			{ID: "T2", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low", Impact: Impact{Contexts: []string{"gate"}}},
		}
		sels := []RiskSelection{
			{TaskID: "T1", SelectedRiskTier: "low", OverrideReason: ""},
			{TaskID: "T2", SelectedRiskTier: "low", OverrideReason: ""},
		}
		s.RiskSelections.Value = &sels
		c := buildCase(t, "output-already-sorted", s)
		if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatal("role=output with already task_id-sorted source order must be rejected")
		}
	})
}

// TestReplayAndDiffRejectInvalidCorpusDirect：以 Go constructor 建「digest
// 自洽但 phase↔seam 不符」的案例，不經 loadCorpus(t) 直接傳給 ReplayCorpus／
// DiffBundles——corpus-level 驗證不得只活在 helper，兩個直收 []CorpusCase 的
// 入口都要各自呼叫 ValidateCorpus。
func TestReplayAndDiffRejectInvalidCorpusDirect(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t) // decide phase snapshot
	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	// digest 自洽，但 phase(decide)↔seam(gatepolicy_validate，submit-only) 不符。
	c := CorpusCase{
		Name: "phase-seam-mismatch", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_validate", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s)},
		Role:      "none",
	}
	if _, err := ReplayCorpus(b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("ReplayCorpus must reject a phase/seam-illegal case passed directly (not via loadCorpus)")
	}
	if _, err := DiffBundles(b, b, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("DiffBundles must reject a phase/seam-illegal case passed directly (not via loadCorpus)")
	}
}

// TestUnionInvalidRejectedAtAllEntries：Go constructor 建兩種 union-invalid
// 案例（evaluated 帶 reason；acquisition_failed 帶 snapshot＋digest），直接傳
// 給 ReplayCorpus／VerifyOracleFreshness／DiffBundles 三個入口皆必須拒（stub
// recompute 驗證 freshness 半邊）。
func TestUnionInvalidRejectedAtAllEntries(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	evaluatedWithReason := CorpusCase{
		Name: "evaluated-with-reason", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s)},
		Role:      "none", Reason: "should not be set on an evaluated case",
	}
	acquisitionFailedWithSnapshot := CorpusCase{
		Name: "acquisition-failed-with-snapshot", Kind: CorpusKindAcquisitionFailed, EvaluationPhase: "submit",
		OracleSeam: "host_boundary", Provenance: "host_boundary", Role: "none",
		Reason: "boom", Snapshot: s, FactsDigest: digest,
	}

	stubRecompute := func(CorpusCase) (GoVerdict, error) {
		t.Fatal("recompute must not be invoked when corpus-level validation already fails")
		return GoVerdict{}, nil
	}

	for _, bad := range []CorpusCase{evaluatedWithReason, acquisitionFailedWithSnapshot} {
		cases := []CorpusCase{bad}
		if _, err := ReplayCorpus(b, cases, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatalf("ReplayCorpus must reject union-invalid case %q", bad.Name)
		}
		if _, err := VerifyOracleFreshness(cases, stubRecompute); err == nil {
			t.Fatalf("VerifyOracleFreshness must reject union-invalid case %q", bad.Name)
		}
		if _, err := DiffBundles(b, b, cases, gate2BundleRuntimeCostLimit); err == nil {
			t.Fatalf("DiffBundles must reject union-invalid case %q", bad.Name)
		}
	}
}
