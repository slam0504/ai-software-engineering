package domainspec

import (
	"os"
	"testing"
)

// gate2BundleStaticCostLimit／gate2BundleRuntimeCostLimit：R27 的雙層 exists
// 重算（純 CEL，無 host intersect 函式）在 fixedSizeCostEstimator（固定 size hint
// 64）下靜態估計成本達數千萬量級；runtime 實際資料量小，維持既有測試的
// 100_000 量級即可。
const (
	gate2BundleStaticCostLimit  = 50_000_000
	gate2BundleRuntimeCostLimit = 1_000_000
)

func loadGate2Bundle(t *testing.T) *CompiledBundle {
	t.Helper()
	data, err := os.ReadFile("testdata/gate2-bundle.yaml")
	if err != nil {
		t.Fatalf("read gate2-bundle.yaml: %v", err)
	}
	b, err := LoadBundle(data, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("load gate2-bundle.yaml: %v", err)
	}
	return b
}

// validSubmitSnapshot：合法 submit snapshot（validSubmitSnapshotJSON，facts_test.go）
// 的 decode 複本，供 submit-phase 隔離 fixture 逐條變異用。
func validSubmitSnapshot(t *testing.T) *FactsSnapshot {
	t.Helper()
	s, err := DecodeFactsSnapshot([]byte(validSubmitSnapshotJSON()))
	if err != nil {
		t.Fatalf("decode valid submit: %v", err)
	}
	return s
}

func evalGate2(t *testing.T, b *CompiledBundle, s *FactsSnapshot) *Result {
	t.Helper()
	r, err := Evaluate(b, s, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return r
}

// TestGate2BundleCleanSnapshotsPass：乾淨 decide/approved 與 submit snapshot
// 皆不得觸發任何 in-scope 規則（brief Step 1）。
func TestGate2BundleCleanSnapshotsPass(t *testing.T) {
	b := loadGate2Bundle(t)

	t.Run("decide/approved", func(t *testing.T) {
		r := evalGate2(t, b, mustSnapshot(t))
		if r.Truth != TruthTrue || r.Status != StatusOK {
			t.Fatalf("clean decide snapshot must be true/ok, got %s/%s violations=%+v", r.Truth, r.Status, r.Violations)
		}
	})

	t.Run("submit", func(t *testing.T) {
		r := evalGate2(t, b, validSubmitSnapshot(t))
		if r.Truth != TruthTrue || r.Status != StatusOK {
			t.Fatalf("clean submit snapshot must be true/ok, got %s/%s violations=%+v", r.Truth, r.Status, r.Violations)
		}
	})
}

// findBinding／removeBindingKind／setBindingDigest：request.bindings 變異小工具
// （per_kind 隔離 fixture 用）。
func setBindingDigest(s *FactsSnapshot, kind, digest string) {
	for i := range s.Request.Value.Bindings {
		if s.Request.Value.Bindings[i].Kind == kind {
			s.Request.Value.Bindings[i].Digest = digest
		}
	}
}

func removeBindingKind(s *FactsSnapshot, kind string) {
	kept := make([]Binding, 0, len(s.Request.Value.Bindings))
	for _, b := range s.Request.Value.Bindings {
		if b.Kind != kind {
			kept = append(kept, b)
		}
	}
	s.Request.Value.Bindings = kept
}

func strPtr(s string) *string { return &s }

// TestGate2BundleIsolatedRuleCoverage：表驅動隔離 fixture（brief Step 1 rev5——
// 唯一命中）。每條規則從乾淨底稿變異，斷言 truth==false 且 Violations 的
// distinct rule id 恰等於 {該 id}。
func TestGate2BundleIsolatedRuleCoverage(t *testing.T) {
	b := loadGate2Bundle(t)

	type tc struct {
		name    string
		ruleID  string
		submit  bool // true=submit-phase fixture, false=decide-phase
		wantIdx int  // per_task/per_kind 命中的 SourceIndex；非展開規則用 -1
		mutate  func(*FactsSnapshot)
	}

	cases := []tc{
		{
			name: "R5.submit/unknown-gate", ruleID: "R5.submit", submit: true, wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Request.Value.Gate = "bogus" },
		},
		{
			name: "R6.submit/bad-subject", ruleID: "R6.submit", submit: true, wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Request.Value.Subject = "nogood" },
		},
		{
			name: "R7/duplicate-binding", ruleID: "R7", submit: true, wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Request.Value.Bindings = append(s.Request.Value.Bindings, s.Request.Value.Bindings[0])
			},
		},
		{
			name: "R8/missing-plan-binding", ruleID: "R8", submit: true, wantIdx: 1, // required_kinds[1] == "plan"
			mutate: func(s *FactsSnapshot) { removeBindingKind(s, "plan") },
		},
		{
			name: "R9/bad-spec-manifest-digest", ruleID: "R9", submit: true, wantIdx: 0, // required_kinds[0] == "spec_manifest"
			mutate: func(s *FactsSnapshot) { setBindingDigest(s, "spec_manifest", "bogus") },
		},
		{
			name: "R3/no-git-identity", ruleID: "R3", wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Approver.Value.Name, s.Approver.Value.Email = "", "" },
		},
		{
			name: "R1/unknown-decision", ruleID: "R1", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("weird")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
			},
		},
		{
			name: "R2/rejected-empty-reason", ruleID: "R2", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("rejected")}
				s.Reason = Fact[string]{Presence: Known, Value: strPtr("")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
				s.RiskSelections = Fact[[]RiskSelection]{Presence: Known, Value: &[]RiskSelection{}}
			},
		},
		{
			name: "R4/entry-non-pending", ruleID: "R4", wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Entry.Value.HasRecord = true },
		},
		{
			name: "R5.decide/unknown-gate", ruleID: "R5.decide", wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Request.Value.Gate = "bogus" },
		},
		{
			name: "R11/stale-spec-manifest", ruleID: "R11", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				setBindingDigest(s, "spec_manifest", "sha256:"+repeatChar("1", 64))
			},
		},
		{
			name: "R12/base-commit-missing", ruleID: "R12", wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.BaseCommitState = Fact[string]{Presence: Known, Value: strPtr("missing")} },
		},
		{
			name: "R21/rejected-with-risk-selections", ruleID: "R21", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Decision = Fact[string]{Presence: Known, Value: strPtr("rejected")}
				s.Reason = Fact[string]{Presence: Known, Value: strPtr("not good enough")}
				s.Current = Fact[Current]{Presence: NotApplicable}
				s.BaseCommitState = Fact[string]{Presence: NotApplicable}
				s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
				s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
				// risk_selections 保留底稿的非空值（[{T1,low,""}]）以觸發本規則。
			},
		},
		{
			name: "R6.decide/bad-subject", ruleID: "R6.decide", wantIdx: -1,
			mutate: func(s *FactsSnapshot) { s.Request.Value.Subject = "nogood" },
		},
		{
			name: "R24/duplicate-task-id-selection", ruleID: "R24", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				sel := (*s.RiskSelections.Value)[0]
				*s.RiskSelections.Value = append(*s.RiskSelections.Value, sel)
			},
		},
		{
			name: "R25/missing-selection", ruleID: "R25", wantIdx: 0,
			mutate: func(s *FactsSnapshot) { *s.RiskSelections.Value = []RiskSelection{} },
		},
		{
			name: "R27/recompute-mismatch", ruleID: "R27", wantIdx: 0,
			mutate: func(s *FactsSnapshot) {
				s.RiskPolicy.Value.Rules = []RiskRule{
					{Match: Impact{Contexts: []string{"gate"}}, Tier: "medium"},
				}
			},
		},
		{
			name: "R28.minimum/invalid-minimum-tier", ruleID: "R28.minimum", wantIdx: 0,
			mutate: func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "extreme"
				s.RiskPolicy.Value.Rules = []RiskRule{}
				s.RiskPolicy.Value.DefaultTier = "extreme"
			},
		},
		{
			name: "R28.planner/invalid-planner-tier", ruleID: "R28.planner", wantIdx: 0,
			mutate: func(s *FactsSnapshot) { s.Plan.Value.Tasks[0].PlannerRiskTier = "extreme" },
		},
		{
			name: "R29/planner-below-minimum", ruleID: "R29", wantIdx: 0,
			mutate: func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "high"
				s.RiskPolicy.Value.Rules = []RiskRule{
					{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"},
				}
				s.Plan.Value.Tasks[0].PlannerRiskTier = "low"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "high"
			},
		},
		{
			name: "R28.selected/invalid-selected-tier", ruleID: "R28.selected", wantIdx: 0,
			mutate: func(s *FactsSnapshot) { (*s.RiskSelections.Value)[0].SelectedRiskTier = "extreme" },
		},
		{
			name: "R30/selected-below-minimum", ruleID: "R30", wantIdx: 0,
			mutate: func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].MinimumRiskTier = "high"
				s.RiskPolicy.Value.Rules = []RiskRule{
					{Match: Impact{Contexts: []string{"gate"}}, Tier: "high"},
				}
				s.Plan.Value.Tasks[0].PlannerRiskTier = "high"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "low"
				(*s.RiskSelections.Value)[0].OverrideReason = "because"
			},
		},
		{
			name: "R31/selected-below-planner-no-override", ruleID: "R31", wantIdx: 0,
			mutate: func(s *FactsSnapshot) {
				s.Plan.Value.Tasks[0].PlannerRiskTier = "high"
				(*s.RiskSelections.Value)[0].SelectedRiskTier = "low"
				(*s.RiskSelections.Value)[0].OverrideReason = ""
			},
		},
		{
			name: "R26/selection-references-unknown-task", ruleID: "R26", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				*s.RiskSelections.Value = append(*s.RiskSelections.Value, RiskSelection{
					TaskID: "T99", SelectedRiskTier: "low", OverrideReason: "",
				})
			},
		},
		{
			// R16 hard 語意驗證（brief）：state=open 的 escalation 只要 block_scope
			// 命中就一律擋（hard 旗標根本不在 facts schema 裡，無從影響本判定）。
			name: "R16/open-workspace-escalation-blocks", ruleID: "R16", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
					{EscalationID: "E1", State: "open", BlockScope: "workspace"},
				}}
			},
		},
		{
			// R15 scope-derivation 語意（controller ruling，fix commit）：CEL 端
			// 獨立由 request.gate/request.subject（gate2、"plan:P1"）重算出
			// "gate2:P1"，與 escalation 的 block_scope 完全相等才擋。
			name: "R16/gate2-derived-scope-match-blocks", ruleID: "R16", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
					{EscalationID: "E2", State: "open", BlockScope: "gate2:P1"},
				}}
			},
		},
		{
			// R15 scope-derivation 語意——test_contract_approval 分支
			// （task review fix commit）：production scopeForSubject 對 tca
			// 的 subject "task:<plan>/<task>" 去掉 "task:" 前綴、換成 "tca:"
			// 前綴；CEL 端獨立重算出 "tca:P1/T1"，與 escalation 的 block_scope
			// 完全相等才擋。
			name: "R16/tca-derived-scope-match-blocks", ruleID: "R16", wantIdx: -1,
			mutate: func(s *FactsSnapshot) {
				s.Request.Value.Gate = "test_contract_approval"
				s.Request.Value.Subject = "task:P1/T1"
				s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
					{EscalationID: "E4", State: "open", BlockScope: "tca:P1/T1"},
				}}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s *FactsSnapshot
			if c.submit {
				s = validSubmitSnapshot(t)
			} else {
				s = mustSnapshot(t)
			}
			c.mutate(s)
			r := evalGate2(t, b, s)
			if r.Truth != TruthFalse {
				t.Fatalf("mutated snapshot must be false, got %s (violations=%+v reason=%+v)", r.Truth, r.Violations, r.ReasonGraph)
			}
			distinct := map[string]bool{}
			for _, v := range r.Violations {
				distinct[v.RuleID] = true
			}
			if len(distinct) != 1 || !distinct[c.ruleID] {
				t.Fatalf("isolated case must trip exactly %s, got %v", c.ruleID, r.Violations)
			}
			if c.wantIdx != -1 {
				for _, v := range r.Violations {
					if v.RuleID == c.ruleID && v.SourceIndex != c.wantIdx {
						t.Fatalf("expected %s to fire at index %d, got %d", c.ruleID, c.wantIdx, v.SourceIndex)
					}
				}
			}
		})
	}
}

// TestGate2BundleR16ScopeDerivationIsNotOverInclusive：R15 scope-derivation
// 語意的反證面（controller ruling，fix commit）——escalation 若 block_scope
// 是「另一個 plan」的 derived scope（gate2:P2），不得誤擋 subject=plan:P1 的
// 決議；證明 R16 的 CEL 重算確實是逐 gate/subject 動態算出、不是巧合命中
// 固定字串（若退化成永遠 true 或只比對 "gate2:" 前綴，這個案例會被誤擋）。
func TestGate2BundleR16ScopeDerivationIsNotOverInclusive(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t) // request.subject == "plan:P1"
	s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
		{EscalationID: "E3", State: "open", BlockScope: "gate2:P2"},
	}}
	r := evalGate2(t, b, s)
	if r.Truth != TruthTrue || len(r.Violations) != 0 {
		t.Fatalf("escalation scoped to a different plan must not block: truth=%s violations=%+v", r.Truth, r.Violations)
	}
}

// TestGate2BundleR16TCAScopeDerivationIsNotOverInclusive：同上，針對
// test_contract_approval 分支的反證面（task review fix commit）——escalation
// 若 block_scope 是「另一個 tca id」的 derived scope（tca:P2/T1），不得誤擋
// subject=task:P1/T1 的 test_contract_approval 決議。
func TestGate2BundleR16TCAScopeDerivationIsNotOverInclusive(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	s.Request.Value.Gate = "test_contract_approval"
	s.Request.Value.Subject = "task:P1/T1"
	s.Escalations = Fact[[]EscalationFact]{Presence: Known, Value: &[]EscalationFact{
		{EscalationID: "E5", State: "open", BlockScope: "tca:P2/T1"},
	}}
	r := evalGate2(t, b, s)
	if r.Truth != TruthTrue || len(r.Violations) != 0 {
		t.Fatalf("escalation scoped to a different tca id must not block: truth=%s violations=%+v", r.Truth, r.Violations)
	}
}

// TestGate2BundleR6DecideRequiresGate2Guard：production 的 subject-shape
// 檢查活在 Gate2Policy.BuildDecision 內，只有 dispatch 已解出 gate=="gate2"
// 才會執行到（task review Important 1）。一個 approved 的 gate1 決議
// （subject 恆為 "workspace"，本就不是 "plan:<id>" 形狀）在補上 gate 守門前
// 會被 R6.decide 誤擋；補上後必須 truth==true。
func TestGate2BundleR6DecideRequiresGate2Guard(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	s.Request.Value.Gate = "gate1"
	s.Request.Value.Subject = "workspace"
	r := evalGate2(t, b, s)
	if r.Truth != TruthTrue || len(r.Violations) != 0 {
		t.Fatalf("approved gate1 decision must not trip R6.decide: truth=%s violations=%+v", r.Truth, r.Violations)
	}
}

func repeatChar(ch string, n int) string {
	out := make([]byte, 0, n*len(ch))
	for i := 0; i < n; i++ {
		out = append(out, ch...)
	}
	return string(out)
}
