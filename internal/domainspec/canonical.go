package domainspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// CanonicalJSON：spec §2 canonical 規則正規化後輸出 deterministic JSON（就地複本，不改輸入）：
//
//	bindings 依 (kind, role, ref, digest) 全序排序（重複不去重）；
//	risk_selections 依 (task_id, selected_risk_tier, override_reason)（重複不去重——R24 可觀測違規輸入）；
//	escalations 依 escalation_id；
//	plan.tasks 保留原始順序（source_index 具語意）；risk_policy.rules 保留來源順序；
//	impact/match 的 contexts、modules 字典序排序後去重（spec rev6）；
//	nil 集合輸出 []（marshal 前把 nil slice 換空 slice；wrapper value 為 nil slice 同樣處理）。
//
// key 順序：encoding/json 依 struct 欄位宣告序，本身 deterministic。
func CanonicalJSON(s *FactsSnapshot) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("domainspec: nil snapshot")
	}
	cp := deepCopySnapshot(s)
	normalizeSnapshot(cp)
	b, err := json.Marshal(cp)
	if err != nil {
		return nil, fmt.Errorf("domainspec: canonical marshal: %w", err)
	}
	return b, nil
}

// SnapshotDigest = "sha256:" + hex(sha256(CanonicalJSON))。
func SnapshotDigest(s *FactsSnapshot) (string, error) {
	b, err := CanonicalJSON(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// deepCopySnapshot 對輸入做欄位級別複本（不經 JSON round-trip）——JSON round-trip
// 會把「Value 指向 nil slice」與「Value 本身為 nil」都序列化成 null，兩者 unmarshal
// 回來後無法區分，導致 escalations/risk_selections 的 nil-vs-empty 正規化在複本階段
// 就先丟失資訊（TestCanonicalNilAndEmptySliceEqual 需要的正是這個區分）。
func deepCopySnapshot(s *FactsSnapshot) *FactsSnapshot {
	return &FactsSnapshot{
		SchemaVersion:   s.SchemaVersion,
		EvaluationPhase: s.EvaluationPhase,
		Decision:        Fact[string]{Presence: s.Decision.Presence, Value: clonePtr(s.Decision.Value)},
		Reason:          Fact[string]{Presence: s.Reason.Presence, Value: clonePtr(s.Reason.Value)},
		Approver:        Fact[Approver]{Presence: s.Approver.Presence, Value: clonePtr(s.Approver.Value)},
		Entry:           Fact[Entry]{Presence: s.Entry.Presence, Value: clonePtr(s.Entry.Value)},
		Request:         Fact[Request]{Presence: s.Request.Presence, Value: cloneRequest(s.Request.Value)},
		Current:         Fact[Current]{Presence: s.Current.Presence, Value: clonePtr(s.Current.Value)},
		BaseCommitState: Fact[string]{Presence: s.BaseCommitState.Presence, Value: clonePtr(s.BaseCommitState.Value)},
		Plan:            Fact[PlanFacts]{Presence: s.Plan.Presence, Value: clonePlanFacts(s.Plan.Value)},
		RiskPolicy:      Fact[RiskPolicyFacts]{Presence: s.RiskPolicy.Presence, Value: cloneRiskPolicyFacts(s.RiskPolicy.Value)},
		RiskSelections:  Fact[[]RiskSelection]{Presence: s.RiskSelections.Presence, Value: cloneSlicePtr(s.RiskSelections.Value)},
		Escalations:     Fact[[]EscalationFact]{Presence: s.Escalations.Presence, Value: cloneSlicePtr(s.Escalations.Value)},
	}
}

func clonePtr[T any](v *T) *T {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

// cloneSlicePtr 保留「pointer 本身是否為 nil」與「pointer 指向的 slice 是否為
// nil」兩個獨立維度——這正是 nil-vs-empty 正規化需要區分的輸入形。
func cloneSlicePtr[T any](v *[]T) *[]T {
	if v == nil {
		return nil
	}
	src := *v
	var out []T
	if src != nil {
		out = make([]T, len(src))
		copy(out, src)
	}
	return &out
}

func cloneStrings(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}

func cloneImpact(im Impact) Impact {
	return Impact{Contexts: cloneStrings(im.Contexts), Modules: cloneStrings(im.Modules)}
}

func cloneRequest(r *Request) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Bindings != nil {
		cp.Bindings = make([]Binding, len(r.Bindings))
		copy(cp.Bindings, r.Bindings)
	}
	return &cp
}

func clonePlanFacts(p *PlanFacts) *PlanFacts {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Tasks != nil {
		cp.Tasks = make([]PlanTask, len(p.Tasks))
		for i, task := range p.Tasks {
			task.Impact = cloneImpact(task.Impact)
			cp.Tasks[i] = task
		}
	}
	return &cp
}

func cloneRiskPolicyFacts(rp *RiskPolicyFacts) *RiskPolicyFacts {
	if rp == nil {
		return nil
	}
	cp := *rp
	if rp.Rules != nil {
		cp.Rules = make([]RiskRule, len(rp.Rules))
		for i, rule := range rp.Rules {
			rule.Match = cloneImpact(rule.Match)
			cp.Rules[i] = rule
		}
	}
	return &cp
}

// normalizeSnapshot 就地正規化複本（絕不能作用在原輸入上）。
func normalizeSnapshot(s *FactsSnapshot) {
	if s.Request.Presence == Known && s.Request.Value != nil {
		s.Request.Value.Bindings = normalizeBindings(s.Request.Value.Bindings)
	}

	if s.Plan.Presence == Known && s.Plan.Value != nil {
		tasks := s.Plan.Value.Tasks
		if tasks == nil {
			tasks = []PlanTask{}
		}
		for i := range tasks {
			normalizeImpact(&tasks[i].Impact)
		}
		s.Plan.Value.Tasks = tasks
	}

	if s.RiskPolicy.Presence == Known && s.RiskPolicy.Value != nil {
		rules := s.RiskPolicy.Value.Rules
		if rules == nil {
			rules = []RiskRule{}
		}
		for i := range rules {
			normalizeImpact(&rules[i].Match)
		}
		s.RiskPolicy.Value.Rules = rules
	}

	if s.RiskSelections.Presence == Known && s.RiskSelections.Value != nil {
		sels := *s.RiskSelections.Value
		if sels == nil {
			sels = []RiskSelection{}
		}
		sort.Slice(sels, func(i, j int) bool { return riskSelectionLess(sels[i], sels[j]) })
		*s.RiskSelections.Value = sels
	}

	if s.Escalations.Presence == Known && s.Escalations.Value != nil {
		escs := *s.Escalations.Value
		if escs == nil {
			escs = []EscalationFact{}
		}
		sort.Slice(escs, func(i, j int) bool { return escs[i].EscalationID < escs[j].EscalationID })
		*s.Escalations.Value = escs
	}
}

// normalizeBindings 依 (kind, role, ref, digest) 全序排序；重複不去重。
func normalizeBindings(bindings []Binding) []Binding {
	if bindings == nil {
		bindings = []Binding{}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindingLess(bindings[i], bindings[j]) })
	return bindings
}

// normalizeImpact 就地把 contexts／modules 排序後去重；nil 換成空 slice。
func normalizeImpact(im *Impact) {
	im.Contexts = sortDedupeStrings(im.Contexts)
	im.Modules = sortDedupeStrings(im.Modules)
}

func sortDedupeStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return slices.Compact(out)
}

func bindingLess(a, b Binding) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	if a.Ref != b.Ref {
		return a.Ref < b.Ref
	}
	return a.Digest < b.Digest
}

func riskSelectionLess(a, b RiskSelection) bool {
	if a.TaskID != b.TaskID {
		return a.TaskID < b.TaskID
	}
	if a.SelectedRiskTier != b.SelectedRiskTier {
		return a.SelectedRiskTier < b.SelectedRiskTier
	}
	return a.OverrideReason < b.OverrideReason
}
