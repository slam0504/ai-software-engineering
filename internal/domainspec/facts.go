// Package domainspec 是 pure-domain 型別與驗證（無 I/O，不依賴其他 internal 套件）。
// FactsSnapshot 統一以 Fact[T] presence wrapper 表達三態事實，DecodeFactsSnapshot／
// ValidateFactsSnapshot 是 strict decode 與 presence matrix 驗證的單一權威入口。
package domainspec

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Presence 是 Fact[T] 的三態旗標。
type Presence string

const (
	Known         Presence = "known"
	NotApplicable Presence = "not_applicable"
	Missing       Presence = "missing"
)

// Fact[T]：統一三態 presence wrapper（plan rev2——全部 fact 群組一致，
// 不再有「部分群組裸值、部分帶 presence」的混合形）。
// JSON 形式 {"presence":"known","value":<T>}；不變式 presence==known ⇔ value 非 nil。
type Fact[T any] struct {
	Presence Presence `json:"presence"`
	Value    *T       `json:"value"`
}

// UnmarshalJSON 對 wrapper 內層亦套用 DisallowUnknownFields（頂層 strict decode
// 的一部分——見 DecodeFactsSnapshot）。wrapper 不變式（presence⇔value 一致性）
// 留給 ValidateFactsSnapshot 統一檢查，避免驗證權威分散。
func (f *Fact[T]) UnmarshalJSON(data []byte) error {
	var shadow struct {
		Presence Presence `json:"presence"`
		Value    *T       `json:"value"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&shadow); err != nil {
		return fmt.Errorf("domainspec: invalid Fact wrapper: %w", err)
	}
	f.Presence = shadow.Presence
	f.Value = shadow.Value
	return nil
}

// wrapperInvariant 檢查 presence==known ⇔ value 非 nil；其餘 presence（not_applicable／
// missing）要求 value 必須是 nil。name 只用於錯誤訊息定位欄位。
func (f Fact[T]) wrapperInvariant(name string) error {
	switch f.Presence {
	case Known:
		if f.Value == nil {
			return fmt.Errorf("%s: presence=known but value is nil", name)
		}
	case NotApplicable, Missing:
		if f.Value != nil {
			return fmt.Errorf("%s: presence=%s but value is non-nil", name, f.Presence)
		}
	default:
		return fmt.Errorf("%s: invalid presence %q", name, f.Presence)
	}
	return nil
}

type Approver struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type Binding struct {
	Kind   string `json:"kind"`
	Role   string `json:"role"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type Request struct {
	Gate     string    `json:"gate"`
	Subject  string    `json:"subject"`
	Bindings []Binding `json:"bindings"`
}

type Entry struct {
	Exists     bool `json:"exists"`
	HasRequest bool `json:"has_request"`
	HasRecord  bool `json:"has_record"`
}

type Current struct {
	SpecManifest       string `json:"spec_manifest"`
	PlanManifest       string `json:"plan_manifest"`
	RiskPolicy         string `json:"risk_policy"`
	PermissionManifest string `json:"permission_manifest"`
}

type Impact struct {
	Contexts []string `json:"contexts"`
	Modules  []string `json:"modules"`
}

type PlanTask struct {
	ID              string `json:"id"`
	SourceIndex     int    `json:"source_index"` // plan 原始順序（spec §2：不得改 id 排序）
	MinimumRiskTier string `json:"minimum_risk_tier"`
	PlannerRiskTier string `json:"planner_risk_tier"`
	Impact          Impact `json:"impact"`
}

type PlanFacts struct {
	Tasks []PlanTask `json:"tasks"`
}

type RiskRule struct {
	Match Impact `json:"match"`
	Tier  string `json:"tier"`
}

type RiskPolicyFacts struct {
	DefaultTier string     `json:"default_tier"`
	Rules       []RiskRule `json:"rules"` // 保留來源順序（spec rev5）
}

type RiskSelection struct {
	TaskID           string `json:"task_id"`
	SelectedRiskTier string `json:"selected_risk_tier"`
	OverrideReason   string `json:"override_reason"`
}

type EscalationFact struct {
	EscalationID string `json:"escalation_id"` // production ULID（spec rev4）
	State        string `json:"state"`
	BlockScope   string `json:"block_scope"`
}

// FactsSnapshot：typed、非 map（spec §2）；每個 fact 群組帶三態 presence。
type FactsSnapshot struct {
	SchemaVersion   int    `json:"schema_version"`   // 本 spike 固定 1
	EvaluationPhase string `json:"evaluation_phase"` // "submit" | "decide"

	Decision        Fact[string]           `json:"decision"`
	Reason          Fact[string]           `json:"reason"`
	Approver        Fact[Approver]         `json:"approver"`
	Entry           Fact[Entry]            `json:"entry"`
	Request         Fact[Request]          `json:"request"`
	Current         Fact[Current]          `json:"current"`
	BaseCommitState Fact[string]           `json:"base_commit_state"` // "ok" | "missing"
	Plan            Fact[PlanFacts]        `json:"plan"`
	RiskPolicy      Fact[RiskPolicyFacts]  `json:"risk_policy"`
	RiskSelections  Fact[[]RiskSelection]  `json:"risk_selections"`
	Escalations     Fact[[]EscalationFact] `json:"escalations"`
}

// DecodeFactsSnapshot：strict decode（DisallowUnknownFields，含 wrapper 內層）＋
// ValidateFactsSnapshot，違反 fail loud（出口 1）。
func DecodeFactsSnapshot(data []byte) (*FactsSnapshot, error) {
	var s FactsSnapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("domainspec: decode facts snapshot: %w", err)
	}
	if err := ValidateFactsSnapshot(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// isKnownColumn 對應 presence matrix 的「known」欄：允許 known 或 missing
// （「本應 known 而缺」＝host 給不出，走 unknown 路徑）。
func isKnownColumn(p Presence) bool {
	return p == Known || p == Missing
}

// entryNonPending 判斷 R4 例外的觸發條件：entry 顯示非 pending
// （!exists || !has_request || has_record）。entry 非 known 或缺值時視為不適用例外。
func entryNonPending(e Fact[Entry]) bool {
	if e.Presence != Known || e.Value == nil {
		return false
	}
	return !e.Value.Exists || !e.Value.HasRequest || e.Value.HasRecord
}

// decideColumn 依 Decision.Value 選 decide 的三欄之一：approved／rejected／invalid。
// 只在 Decision.Presence==Known（故 Value 非 nil）時呼叫。
func decideColumn(decisionValue string) string {
	switch decisionValue {
	case "approved":
		return "approved"
	case "rejected":
		return "rejected"
	default:
		return "invalid"
	}
}

// ValidateFactsSnapshot（plan rev6——**單一權威驗證入口，防繞過**）：wrapper 不變式
// ＋presence matrix＋plan.tasks[i].SourceIndex == i 連續性。呼叫者：
//
//	DecodeFactsSnapshot（頂層 decode）；
//	DecodeCorpusCase（巢狀 snapshot 只觸發 Fact[T].UnmarshalJSON，不會經過頂層
//	  驗證——必須顯式呼叫）；
//	UPDATE_CORPUS generator（Go constructors 完全不經 JSON decoder）；
//	ReplayCorpus／DiffBundles 逐案例評估前（最後防線）。
func ValidateFactsSnapshot(s *FactsSnapshot) error {
	if s == nil {
		return fmt.Errorf("domainspec: nil snapshot")
	}
	if s.SchemaVersion != 1 {
		return fmt.Errorf("domainspec: unsupported schema_version %d", s.SchemaVersion)
	}

	wrapperChecks := []error{
		s.Decision.wrapperInvariant("decision"),
		s.Reason.wrapperInvariant("reason"),
		s.Approver.wrapperInvariant("approver"),
		s.Entry.wrapperInvariant("entry"),
		s.Request.wrapperInvariant("request"),
		s.Current.wrapperInvariant("current"),
		s.BaseCommitState.wrapperInvariant("base_commit_state"),
		s.Plan.wrapperInvariant("plan"),
		s.RiskPolicy.wrapperInvariant("risk_policy"),
		s.RiskSelections.wrapperInvariant("risk_selections"),
		s.Escalations.wrapperInvariant("escalations"),
	}
	for _, err := range wrapperChecks {
		if err != nil {
			return fmt.Errorf("domainspec: %w", err)
		}
	}

	switch s.EvaluationPhase {
	case "submit":
		if err := validateSubmitMatrix(s); err != nil {
			return err
		}
	case "decide":
		if err := validateDecideMatrix(s); err != nil {
			return err
		}
	default:
		return fmt.Errorf("domainspec: invalid evaluation_phase %q", s.EvaluationPhase)
	}

	return validatePlanSourceIndex(s)
}

// validateSubmitMatrix：presence matrix 的 submit 欄——decision 群組四項全
// not_applicable、request 為 known 欄、其餘群組全 not_applicable。
func validateSubmitMatrix(s *FactsSnapshot) error {
	if s.Decision.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: decision must be not_applicable, got %q", s.Decision.Presence)
	}
	if s.Reason.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: reason must be not_applicable, got %q", s.Reason.Presence)
	}
	if s.Approver.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: approver must be not_applicable, got %q", s.Approver.Presence)
	}
	if s.Entry.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: entry must be not_applicable, got %q", s.Entry.Presence)
	}
	if !isKnownColumn(s.Request.Presence) {
		return fmt.Errorf("domainspec: submit: request must be known or missing, got %q", s.Request.Presence)
	}
	if s.Current.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: current must be not_applicable, got %q", s.Current.Presence)
	}
	if s.BaseCommitState.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: base_commit_state must be not_applicable, got %q", s.BaseCommitState.Presence)
	}
	if s.Plan.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: plan must be not_applicable, got %q", s.Plan.Presence)
	}
	if s.RiskPolicy.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: risk_policy must be not_applicable, got %q", s.RiskPolicy.Presence)
	}
	if s.RiskSelections.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: risk_selections must be not_applicable, got %q", s.RiskSelections.Presence)
	}
	if s.Escalations.Presence != NotApplicable {
		return fmt.Errorf("domainspec: submit: escalations must be not_applicable, got %q", s.Escalations.Presence)
	}
	return nil
}

// validateDecideMatrix：presence matrix 的 decide 三欄（approved／rejected／invalid）。
// decision／reason／approver／entry／request／risk_selections／escalations 在三欄間
// 要求一致，只有 current／base_commit_state／plan／risk_policy 依欄不同
// （rev6：抽出為共用入口）。
func validateDecideMatrix(s *FactsSnapshot) error {
	if !isKnownColumn(s.Decision.Presence) {
		return fmt.Errorf("domainspec: decide: decision must be known or missing, got %q", s.Decision.Presence)
	}
	if !isKnownColumn(s.Reason.Presence) {
		return fmt.Errorf("domainspec: decide: reason must be known or missing, got %q", s.Reason.Presence)
	}
	if !isKnownColumn(s.Approver.Presence) {
		return fmt.Errorf("domainspec: decide: approver must be known or missing, got %q", s.Approver.Presence)
	}
	if !isKnownColumn(s.Entry.Presence) {
		return fmt.Errorf("domainspec: decide: entry must be known or missing, got %q", s.Entry.Presence)
	}

	nonPending := entryNonPending(s.Entry)

	if !isKnownColumn(s.Request.Presence) && !(nonPending && s.Request.Presence == NotApplicable) {
		return fmt.Errorf("domainspec: decide: request must be known or missing (or not_applicable under R4 entry-non-pending exception), got %q", s.Request.Presence)
	}
	if !isKnownColumn(s.RiskSelections.Presence) {
		return fmt.Errorf("domainspec: decide: risk_selections must be known or missing, got %q", s.RiskSelections.Presence)
	}
	if !isKnownColumn(s.Escalations.Presence) {
		return fmt.Errorf("domainspec: decide: escalations must be known or missing, got %q", s.Escalations.Presence)
	}

	// current／base_commit_state／plan／risk_policy 依 decide 欄不同；Decision 為
	// missing 時無從選欄，此群組跳過矩陣檢查（評估時本就走 unknown）。
	if s.Decision.Presence == Missing {
		return nil
	}

	col := decideColumn(*s.Decision.Value)
	groups := []struct {
		name     string
		presence Presence
	}{
		{"current", s.Current.Presence},
		{"base_commit_state", s.BaseCommitState.Presence},
		{"plan", s.Plan.Presence},
		{"risk_policy", s.RiskPolicy.Presence},
	}
	for _, g := range groups {
		if col == "approved" {
			if !isKnownColumn(g.presence) && !(nonPending && g.presence == NotApplicable) {
				return fmt.Errorf("domainspec: decide/approved: %s must be known or missing (or not_applicable under R4 entry-non-pending exception), got %q", g.name, g.presence)
			}
			continue
		}
		if g.presence != NotApplicable {
			return fmt.Errorf("domainspec: decide/%s: %s must be not_applicable, got %q", col, g.name, g.presence)
		}
	}
	return nil
}

// validatePlanSourceIndex（plan rev5）：plan.tasks[i].SourceIndex == i 連續性——
// source_index 是 primary precedence 的權威輸入（production 依 slice 序迴圈，
// gate2.go:142-143），錯置 fixture 可竄改 primary 且 digest 仍忠實保存錯誤。
func validatePlanSourceIndex(s *FactsSnapshot) error {
	if s.Plan.Presence != Known || s.Plan.Value == nil {
		return nil
	}
	for i, task := range s.Plan.Value.Tasks {
		if task.SourceIndex != i {
			return fmt.Errorf("domainspec: plan.tasks[%d].source_index = %d, want %d", i, task.SourceIndex, i)
		}
	}
	return nil
}
