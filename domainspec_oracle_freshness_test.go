package main

// Task 7b：root-package oracle freshness harness（出口 5／6b root 側）。
//
// internal/domainspec/corpus.go 定義了 VerifyOracleFreshness 這個純函式，但它
// 刻意不含 recompute dispatcher——dispatcher 必須呼叫真正的 production seam
// （gate.Service／gatepolicy.Gate2Policy／escalation.BlockingFor／App.GateDecide），
// 而 `_test.go` 函式不是可 import 的 package surface，internal 測試 package 看
// 不到 root 的 App seam。本檔因此住在 repo root（package main），把 corpus.go
// 的純函式與這些 production seam 接起來。
//
// 每個 adapter 依 CorpusCase.Snapshot 重建 production 輸入（stub journal／
// loader／git runner 依既有測試 double 慣例：internal/gate/service_test.go、
// internal/gatepolicy/gate2_test.go），呼叫真正的 production 程式碼，把回傳的
// error 訊息用 (seam, pattern) → rule id 對映轉成 GoVerdict。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/domainspec"
	"github.com/slam0504/sdlc-workbench/internal/escalation"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/gatepolicy"
	"github.com/slam0504/sdlc-workbench/internal/plan"
)

const corpusDir = "internal/domainspec/testdata/corpus"

// ---- corpus 讀取（root 側 loadCorpus 對應版；internal 側同款 helper 見
// internal/domainspec/corpus_test.go:loadCorpus，兩邊各自持有一份是因為
// _test.go helper 不可 import）----

func loadRootCorpus(t *testing.T) []domainspec.CorpusCase {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("read %s: %v", corpusDir, err)
	}
	var cases []domainspec.CorpusCase
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(corpusDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		c, err := domainspec.DecodeCorpusCase(data)
		if err != nil {
			t.Fatalf("decode %s: %v", e.Name(), err)
		}
		cases = append(cases, c)
	}
	if err := domainspec.ValidateCorpus(cases); err != nil {
		t.Fatalf("validate corpus: %v", err)
	}
	return cases
}

// ---- 錯誤訊息 → rule id 對映（(seam, pattern) → rule id；同 seam 內一筆錯誤
// 命中多 rule 的 pattern 集合＝測試紅，見 classify）----

// classify 對 msg 逐一檢查 pats 內每個 rule id 的「全部子字串都必須出現」條件
// （AND 語意；多數 rule 只有一個子字串），恰好一個 rule 通過才算分類成功——
// 通過數 != 1（無匹配或模糊匹配）一律 fail loud，不猜測。
func classify(pats map[string][]string, msg string) (string, error) {
	var matched []string
	for ruleID, subs := range pats {
		all := true
		for _, s := range subs {
			if !strings.Contains(msg, s) {
				all = false
				break
			}
		}
		if all {
			matched = append(matched, ruleID)
		}
	}
	if len(matched) != 1 {
		sort.Strings(matched)
		return "", fmt.Errorf("domainspec oracle: ambiguous or unmatched rule classification: msg=%q matched=%v", msg, matched)
	}
	return matched[0], nil
}

var gateServiceSubmitPatterns = map[string][]string{
	"R5.submit": {"unknown gate"},
}

var gatepolicyValidatePatterns = map[string][]string{
	"R6.submit": {`prefix "plan:"`},
	"R7":        {"duplicate binding"},
	"R8":        {"missing required binding"},
	"R9":        {"does not match expected pattern"},
}

var gateServicePreparePatterns = map[string][]string{
	"R1":        {"unknown decision"},
	"R2":        {"requires reason"},
	"R4":        {"no pending request"},
	"R5.decide": {"unknown gate"},
}

var gatepolicyReconcilePatterns = map[string][]string{
	"R11": {"changed"},
	"R12": {"missing"},
}

// gatepolicyBuildPatterns：R29/R30/R31 三者的錯誤訊息共用大量靜態字面（都是
// "task %q: X_risk_tier %q below Y_risk_tier %q[...]"），單一子字串無法唯一
// 區分，因此用 AND 複合子字串（皆為緊鄰、不跨動態值的靜態片段）：
//   - R29 唯一：": planner_risk_tier \""（R28.planner 訊息在冒號後多一個
//     "unknown "，不含這個緊鄰片段）
//   - R30／R31 皆以 ": selected_risk_tier \"" 開頭，R30 用
//     "below minimum_risk_tier" 收尾、R31 用專屬的 "requires override_reason"
//     收尾唯一區分
var gatepolicyBuildPatterns = map[string][]string{
	"R21":          {"must not include risk selections"},
	"R6.decide":    {`prefix "plan:"`},
	"R24":          {"duplicate risk selection"},
	"R25":          {"missing risk selection"},
	"R26":          {"unknown task ids"},
	"R27":          {"recomputed minimum_risk_tier"},
	"R28.minimum":  {"unknown minimum_risk_tier"},
	"R28.planner":  {"unknown planner_risk_tier"},
	"R28.selected": {"unknown selected_risk_tier"},
	"R29":          {`: planner_risk_tier "`},
	"R30":          {`: selected_risk_tier "`, "below minimum_risk_tier"},
	"R31":          {"requires override_reason"},
}

var appGateDecidePatterns = map[string][]string{
	"R3":  {"git identity not configured"},
	"R24": {"duplicate risk selection"},
	"R30": {`: selected_risk_tier "`, "below minimum_risk_tier"},
	"R16": {"blocked by"},
}

// ---- snapshot → production 型別轉換 helper ----

func derefStr(f domainspec.Fact[string]) string {
	if f.Presence == domainspec.Known && f.Value != nil {
		return *f.Value
	}
	return ""
}

func bindingsFromSnapshot(bs []domainspec.Binding) []gate.Binding {
	out := make([]gate.Binding, len(bs))
	for i, b := range bs {
		out[i] = gate.Binding{Kind: b.Kind, Role: b.Role, Ref: b.Ref, Digest: b.Digest}
	}
	return out
}

func riskSelectionsFromSnapshot(f domainspec.Fact[[]domainspec.RiskSelection]) []gate.RiskSelection {
	if f.Presence != domainspec.Known || f.Value == nil {
		return nil
	}
	out := make([]gate.RiskSelection, len(*f.Value))
	for i, s := range *f.Value {
		out[i] = gate.RiskSelection{TaskID: s.TaskID, SelectedRiskTier: s.SelectedRiskTier, OverrideReason: s.OverrideReason}
	}
	return out
}

func approverFromSnapshot(f domainspec.Fact[domainspec.Approver]) gate.Approver {
	if f.Presence == domainspec.Known && f.Value != nil {
		id := f.Value.Name
		if id == "" {
			id = f.Value.Email
		}
		return gate.Approver{ID: id, Method: "app-local"}
	}
	return gate.Approver{}
}

func gate2ReqFromSnapshot(s *domainspec.FactsSnapshot) gate.GateRequest {
	r := s.Request.Value
	return gate.GateRequest{Gate: r.Gate, Subject: r.Subject, Bindings: bindingsFromSnapshot(r.Bindings)}
}

func planFromSnapshotTasks(tasks []domainspec.PlanTask) plan.Plan {
	out := make([]plan.Task, len(tasks))
	for i, t := range tasks {
		pt := plan.Task{ID: t.ID, MinimumRiskTier: t.MinimumRiskTier, PlannerRiskTier: t.PlannerRiskTier}
		pt.Impact.Contexts = t.Impact.Contexts
		pt.Impact.Modules = t.Impact.Modules
		out[i] = pt
	}
	return plan.Plan{Tasks: out}
}

// riskPolicyRuleType 是 plan.RiskPolicy.Rules 的元素型別（匿名 struct，欄位名／
// 型別／tag 需與 internal/plan/types.go 的 RiskPolicy 定義逐字相同，Go 才視為
// 同一型別、可以 append）。
type riskPolicyRuleType = struct {
	Match struct {
		Contexts []string `yaml:"contexts" json:"contexts"`
		Modules  []string `yaml:"modules" json:"modules"`
	} `yaml:"match" json:"match"`
	Tier string `yaml:"tier" json:"tier"`
}

// riskPolicyFromSnapshotFacts 直接以 Go struct 建構 plan.RiskPolicy（刻意不走
// plan.ParseRiskPolicy——後者在 load 時就拒絕未知 tier 字串，會擋掉
// R28.minimum 這類「tier 值本身不合法、要讓 BuildDecision 自己的防禦檢查接住」
// 的案例；gate2_test.go 的 newBuildDecisionPolicyWithPolicy 對「unknown risk
// tier」案例採同一手法）。
func riskPolicyFromSnapshotFacts(rp *domainspec.RiskPolicyFacts) plan.RiskPolicy {
	pol := plan.RiskPolicy{Version: 1, DefaultTier: rp.DefaultTier}
	for _, r := range rp.Rules {
		var rule riskPolicyRuleType
		rule.Match.Contexts = r.Match.Contexts
		rule.Match.Modules = r.Match.Modules
		rule.Tier = r.Tier
		pol.Rules = append(pol.Rules, rule)
	}
	return pol
}

func riskDecisionsToDomainspec(meta *gate.Metadata) []domainspec.RiskDecision {
	if meta == nil {
		return nil
	}
	out := make([]domainspec.RiskDecision, len(meta.RiskDecisions))
	for i, rd := range meta.RiskDecisions {
		out[i] = domainspec.RiskDecision{
			TaskID: rd.TaskID, MinimumRiskTier: rd.MinimumRiskTier, PlannerRiskTier: rd.PlannerRiskTier,
			SelectedRiskTier: rd.SelectedRiskTier, OverrideReason: rd.OverrideReason,
		}
	}
	return out
}

// ---- 共用 test double（各 seam adapter 共用；沿用既有 internal 測試慣例）----

func failFn() (string, error) { return "", fmt.Errorf("unexpected current* call") }

// noopEmitter 是 gate.Emitter 的最小 stand-in——本檔的 recompute 從不斷言事件
// 內容，只需要滿足介面。
type noopEmitter struct{}

func (noopEmitter) EmitGateEvent(kind string, bindings []gate.Binding, payload any) {}

// staticLoader 是 gatepolicy.PlanLoader 的固定回傳版——BuildDecision／
// ValidateRequest 在本檔的呼叫皆已知短路與否，key（commitOID/planID）不影響
// 回傳值，同 gate2_test.go 的 fakeLoader 但更精簡。
type staticLoader struct {
	pl  plan.Plan
	pol plan.RiskPolicy
}

func (l staticLoader) LoadAt(string, string) (plan.Plan, plan.RiskPolicy, error) {
	return l.pl, l.pol, nil
}

// neverCalledGit 是 plan.GitRunner 的 stand-in，供「production 這條路徑理論上
// 不該碰 git」的呼叫使用——同 gate2_test.go 的 nopGit，任何呼叫都 fail loud。
type neverCalledGit struct{}

func (neverCalledGit) Git(args ...string) ([]byte, error) {
	return nil, fmt.Errorf("domainspec oracle: unexpected git call: %v", args)
}

// neverCalledLoader 供「本次呼叫理論上不該讀 plan」的 seam（gatepolicy_reconcile
// 只測 ReconcileBindings，不涉及 loader）使用。
type neverCalledLoader struct{}

func (neverCalledLoader) LoadAt(string, string) (plan.Plan, plan.RiskPolicy, error) {
	return plan.Plan{}, plan.RiskPolicy{}, fmt.Errorf("domainspec oracle: unexpected LoadAt call")
}

// scriptedLineageGit 是 plan.GitRunner 的腳本化版本：merge-base 恆回報「是
// ancestor」、diff 恆回報單一筆落在 plan/** 內的 M（modify）紀錄——足以讓
// plan.VerifyLineage 對 gatepolicy_validate 的 clean-submit pass 案例成功，
// 而不必真的起一個 git repo（corpus snapshot 的 base_commit digest 本來就是
// 符號佔位值，不是可解析的真實 commit）。
type scriptedLineageGit struct{}

func (scriptedLineageGit) Git(args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("domainspec oracle: empty git args")
	}
	switch args[0] {
	case "merge-base":
		return nil, nil
	case "diff":
		return []byte("M\x00plan/P1.yaml\x00"), nil
	default:
		return nil, fmt.Errorf("domainspec oracle: unexpected git args: %v", args)
	}
}

// fakeExitError 滿足 exitCoder 鴨子型別（ExitCode() int），供
// ReconcileBindings 的 base_commit 分支測試——同
// gatepolicy/gate2_test.go:fakeExitError。
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

// existingCommitGit／missingCommitGit：base_commit_state=="ok"／"missing" 對應
// 的 `git rev-parse --verify --quiet` 回應——ok 時視為 commit 存在（成功），
// missing 時回報 exit 1（gatepolicy.Gate2Policy.ReconcileBindings 唯一認得
// 「commit 不存在」的訊號，見 gate2.go 的 exitCoder 判斷）。
type existingCommitGit struct{}

func (existingCommitGit) Git(args ...string) ([]byte, error) { return nil, nil }

type missingCommitGit struct{}

func (missingCommitGit) Git(args ...string) ([]byte, error) { return nil, fakeExitError{code: 1} }

// stubGate2Policy 是 gate.GatePolicy 的最小 stand-in，供 gate_service_prepare
// seam 的 registry 佔位（本檔涵蓋的 R1/R2/R4/R5.decide 四案例皆在觸及真正 policy
// 之前就短路，見各自的 production 檢查順序；仍註冊一個 policy 避免未來新增
// decide-phase 案例時因 registry 缺項而產生誤導性的 unknown-gate 假陽性）。
type stubGate2Policy struct{}

func (stubGate2Policy) ValidateRequest(gate.GateRequest) error { return nil }
func (stubGate2Policy) BuildDecision(gate.GateRequest, string, gate.DecisionInput) (*gate.Metadata, error) {
	return nil, nil
}
func (stubGate2Policy) SupersessionKey(g, s string) string { return g + "|" + s }
func (stubGate2Policy) ReconcileBindings(gate.ApprovalRecord) ([]gate.StaleCause, error) {
	return nil, nil
}

func appendGateOp(t *testing.T, j *gate.Journal, opID string, rec any) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal gate op record: %v", err)
	}
	if err := j.Append(gate.GateOp{OpID: opID, At: "t", Records: []json.RawMessage{raw}}); err != nil {
		t.Fatalf("append gate op: %v", err)
	}
}

// ---- Seam adapters（每個 seam 一個 recompute 函式；重建 production 輸入、
// 呼叫真正的 production 程式碼、把 error 轉成 GoVerdict）----

// recomputeGateServiceSubmit — R5.submit：gate.Service.Submit 對未知 gate 的
// 拒絕（沿 internal/gate/service_test.go 的 journal/emitter double 慣例）。
func recomputeGateServiceSubmit(t *testing.T, c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	t.Helper()
	s := c.Snapshot
	j, err := gate.OpenJournal(filepath.Join(t.TempDir(), "gate.jsonl"))
	if err != nil {
		return domainspec.GoVerdict{}, err
	}
	defer j.Close()
	svc := gate.NewService(j, gate.Registry{}, func() string { return "u1" }, func() string { return "t" }, noopEmitter{})
	_, err = svc.Submit(s.Request.Value.Gate, s.Request.Value.Subject, bindingsFromSnapshot(s.Request.Value.Bindings))
	if err == nil {
		return domainspec.GoVerdict{Outcome: domainspec.OutcomePass}, nil
	}
	ruleID, merr := classify(gateServiceSubmitPatterns, err.Error())
	if merr != nil {
		return domainspec.GoVerdict{}, merr
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
}

// recomputeGatepolicyValidate — R6.submit/R7/R8/R9 与 clean-submit pass：
// Gate2Policy.ValidateRequest。R6–R9 皆在 validateGate2Bindings 內短路，從不
// 觸及 loader／git；clean-submit 需要 lineage 真的通過，用
// scriptedLineageGit 腳本化滿足（見其型別註解）。
func recomputeGatepolicyValidate(c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	s := c.Snapshot
	req := gate2ReqFromSnapshot(s)
	p := gatepolicy.NewGate2Policy(staticLoader{}, scriptedLineageGit{}, failFn, failFn, failFn, failFn)
	if err := p.ValidateRequest(req); err != nil {
		ruleID, merr := classify(gatepolicyValidatePatterns, err.Error())
		if merr != nil {
			return domainspec.GoVerdict{}, merr
		}
		return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomePass}, nil
}

// recomputeGateServicePrepare — R1/R2/R4/R5.decide：gate.Service.PrepareDecision
// 對一個依 snapshot entry 狀態預置好的 journal 重跑。
func recomputeGateServicePrepare(t *testing.T, c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	t.Helper()
	s := c.Snapshot
	j, err := gate.OpenJournal(filepath.Join(t.TempDir(), "gate.jsonl"))
	if err != nil {
		return domainspec.GoVerdict{}, err
	}
	defer j.Close()

	const approvalID = "u1"
	req := gate.GateRequest{
		Type: "gate_request", SchemaVersion: 2, ApprovalID: approvalID,
		Gate: s.Request.Value.Gate, Subject: s.Request.Value.Subject,
		Bindings: bindingsFromSnapshot(s.Request.Value.Bindings), CreatedAt: "t",
	}
	appendGateOp(t, j, "op-req", req)
	if s.Entry.Presence == domainspec.Known && s.Entry.Value != nil && s.Entry.Value.HasRecord {
		rec := gate.ApprovalRecord{
			Type: "approval_record", SchemaVersion: 2, ApprovalID: approvalID,
			Gate: req.Gate, Subject: req.Subject, Decision: "approved", Bindings: req.Bindings, CreatedAt: "t",
		}
		appendGateOp(t, j, "op-rec", rec)
	}

	reg := gate.Registry{"gate2": stubGate2Policy{}}
	svc := gate.NewService(j, reg, func() string { return "u2" }, func() string { return "t" }, noopEmitter{})
	approver := approverFromSnapshot(s.Approver)
	input := gate.DecisionInput{RiskSelections: riskSelectionsFromSnapshot(s.RiskSelections)}
	_, err = svc.PrepareDecision(approvalID, derefStr(s.Decision), derefStr(s.Reason), approver, input)
	if err == nil {
		return domainspec.GoVerdict{Outcome: domainspec.OutcomePass}, nil
	}
	ruleID, merr := classify(gateServicePreparePatterns, err.Error())
	if merr != nil {
		return domainspec.GoVerdict{}, merr
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
}

// recomputeGatepolicyReconcile — R11/R12 与 alignment-R11：
// Gate2Policy.ReconcileBindings 對一個由 snapshot request 建的 pseudo-record
// 重跑；pass 分支（無 stale cause）額外呼叫 BuildDecision 求出真正的
// RiskDecisions，因為 alignment-R11 這筆的 go_verdict 是 pass＋非空
// risk_decisions，只有跑完整個 decide 才能重現。
func recomputeGatepolicyReconcile(c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	s := c.Snapshot
	req := s.Request.Value
	pseudo := gate.ApprovalRecord{Gate: req.Gate, Subject: req.Subject, Bindings: bindingsFromSnapshot(req.Bindings)}

	cur := s.Current.Value
	currentFn := func(v string) func() (string, error) {
		return func() (string, error) { return v, nil }
	}
	var git plan.GitRunner = existingCommitGit{}
	if derefStr(s.BaseCommitState) == "missing" {
		git = missingCommitGit{}
	}
	p := gatepolicy.NewGate2Policy(neverCalledLoader{}, git,
		currentFn(cur.PlanManifest), currentFn(cur.SpecManifest), currentFn(cur.RiskPolicy), currentFn(cur.PermissionManifest))

	causes, err := p.ReconcileBindings(pseudo)
	if err != nil {
		return domainspec.GoVerdict{}, err
	}
	if len(causes) > 0 {
		ruleID, merr := classify(gatepolicyReconcilePatterns, causes[0].Cause)
		if merr != nil {
			return domainspec.GoVerdict{}, merr
		}
		return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
	}

	meta, err := runBuildDecisionMeta(s)
	if err != nil {
		return domainspec.GoVerdict{}, fmt.Errorf("domainspec oracle: gatepolicy_reconcile pass branch: BuildDecision: %w", err)
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomePass, RiskDecisions: riskDecisionsToDomainspec(meta)}, nil
}

// runBuildDecisionMeta 是 gatepolicy_build 系列 adapter 的共用核心：以 snapshot
// 的 plan／risk_policy／risk_selections 重建 Gate2Policy.BuildDecision 的輸入，
// 直接呼叫 production 函式。
func runBuildDecisionMeta(s *domainspec.FactsSnapshot) (*gate.Metadata, error) {
	var pl plan.Plan
	var pol plan.RiskPolicy
	if s.Plan.Presence == domainspec.Known && s.Plan.Value != nil {
		pl = planFromSnapshotTasks(s.Plan.Value.Tasks)
	}
	if s.RiskPolicy.Presence == domainspec.Known && s.RiskPolicy.Value != nil {
		pol = riskPolicyFromSnapshotFacts(s.RiskPolicy.Value)
	}
	p := gatepolicy.NewGate2Policy(staticLoader{pl: pl, pol: pol}, neverCalledGit{}, failFn, failFn, failFn, failFn)
	req := gate2ReqFromSnapshot(s)
	input := gate.DecisionInput{RiskSelections: riskSelectionsFromSnapshot(s.RiskSelections)}
	return p.BuildDecision(req, derefStr(s.Decision), input)
}

// recomputeGatepolicyBuild — R21/R6.decide/R24–R31/R26 与 clean-decide-*：
// Gate2Policy.BuildDecision 直接重跑。
func recomputeGatepolicyBuild(c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	s := c.Snapshot
	meta, err := runBuildDecisionMeta(s)
	if err != nil {
		ruleID, merr := classify(gatepolicyBuildPatterns, err.Error())
		if merr != nil {
			return domainspec.GoVerdict{}, merr
		}
		return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomePass, RiskDecisions: riskDecisionsToDomainspec(meta)}, nil
}

// recomputeEscalation — R16 与 alignment-R16-hard-ignored：escalation.BlockingFor
// 對 snapshot escalations 重跑，scope 用真正的 production scopeForSubject
// （app.go）。每筆項目一律以 Hard:true 建構——BlockingFor 本就不看 Hard，這樣
// 同時對 isolated-R16 與 hard-ignored 對齊案例證明「blocking 判定忽略 hard」。
func recomputeEscalation(c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	s := c.Snapshot
	req := s.Request.Value
	scope := scopeForSubject(req.Gate, req.Subject)

	var entries []escalation.Entry
	if s.Escalations.Presence == domainspec.Known && s.Escalations.Value != nil {
		for _, e := range *s.Escalations.Value {
			entries = append(entries, escalation.Entry{
				Item:  escalation.Item{EscalationID: e.EscalationID, BlockScope: e.BlockScope, Hard: true},
				State: e.State,
			})
		}
	}
	if blocking := escalation.BlockingFor(entries, scope); len(blocking) > 0 {
		return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: "R16"}, nil
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomePass}, nil
}

// yamlStringList 把字串 slice 轉成 YAML flow-sequence 字面（[] 或
// [a, b]）——本檔僅用於 app_gatedecide adapter 重建 plan／risk-policy YAML，
// 元素恆為受控字面（tier 名稱／context 標籤),不需跳脫。
func yamlStringList(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	return "[" + strings.Join(ss, ", ") + "]"
}

// planAndRiskPolicyYAMLFromSnapshot 依 snapshot 的 plan／risk_policy facts 產出
// 可 commit 的 plan.yaml／risk-policy.yaml 內容——供 app_gatedecide seam 走
// 完整的 SubmitPlanForApproval／GateDecide production 路徑（不能像
// gatepolicy_build 那樣繞過 loader，因為 R3／precedence 案例的正確 verdict
// 只有跑過完整 gateDecide 循序管線才組得出來）。
func planAndRiskPolicyYAMLFromSnapshot(s *domainspec.FactsSnapshot, analysisBase string) (planYAML, riskPolicyYAML string) {
	var tasks strings.Builder
	for _, t := range s.Plan.Value.Tasks {
		tasks.WriteString("  - id: " + t.ID + "\n")
		tasks.WriteString("    title: Task " + t.ID + "\n")
		tasks.WriteString("    scenarios: []\n")
		tasks.WriteString("    depends_on: []\n")
		tasks.WriteString("    impact:\n")
		tasks.WriteString("      contexts: " + yamlStringList(t.Impact.Contexts) + "\n")
		tasks.WriteString("      modules: " + yamlStringList(t.Impact.Modules) + "\n")
		tasks.WriteString("    completion: []\n")
		tasks.WriteString("    minimum_risk_tier: " + t.MinimumRiskTier + "\n")
		tasks.WriteString("    planner_risk_tier: " + t.PlannerRiskTier + "\n")
		tasks.WriteString("    permissions_ref: permissions/" + t.ID + ".yaml\n")
		tasks.WriteString("    test_contract:\n")
		tasks.WriteString("      command: {executable: go, argv: [test]}\n")
		tasks.WriteString("      expected_failure: {test_ids: [" + t.ID + "], matcher: FAIL}\n")
	}
	planYAML = "plan_id: P1\n" +
		"analysis_base_commit: " + analysisBase + "\n" +
		"spec_manifest: sha256:" + strings.Repeat("a", 64) + "\n" +
		"risk_policy: sha256:" + strings.Repeat("a", 64) + "\n" +
		"tasks:\n" + tasks.String()

	var rules strings.Builder
	for _, r := range s.RiskPolicy.Value.Rules {
		rules.WriteString("  - match:\n")
		rules.WriteString("      contexts: " + yamlStringList(r.Match.Contexts) + "\n")
		rules.WriteString("      modules: " + yamlStringList(r.Match.Modules) + "\n")
		rules.WriteString("    tier: " + r.Tier + "\n")
	}
	rulesYAML := "[]\n"
	if rules.Len() > 0 {
		rulesYAML = "\n" + rules.String()
	}
	riskPolicyYAML = "version: 1\ndefault_tier: " + s.RiskPolicy.Value.DefaultTier + "\nrules: " + rulesYAML
	return planYAML, riskPolicyYAML
}

// recomputeAppGateDecide — R3 与 precedence-R3-vs-R24／precedence-R30-vs-R16：
// 沿 app_gate_test.go 的 App seam 慣例（newTestAppGit／newTestAppGitNoIdentity
// 的 gitIdentityOverride 手法），跑一次真正的 gate1 核可＋gate2 送核＋
// GateDecide。gate1 核可與 gate2 送核用 repo 本身的 git identity（newTestAppGit
// 已設好），只有最後受測的 decide 呼叫才覆寫成 snapshot 的 approver——否則
// snapshot 裡 approver 空值的案例會連 gate1 都核可不了，走不到 R3 這一步。
func recomputeAppGateDecide(t *testing.T, c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	t.Helper()
	s := c.Snapshot
	a := newTestAppGit(t)

	if _, err := a.SpecWrite("spec/glossary.md", "term v1", ""); err != nil {
		return domainspec.GoVerdict{}, err
	}
	commitAll(t, a)
	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		return domainspec.GoVerdict{}, err
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		return domainspec.GoVerdict{}, err
	}

	headOID := revParseHead(t, a)
	planYAML, riskPolicyYAML := planAndRiskPolicyYAMLFromSnapshot(s, headOID)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), planYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), riskPolicyYAML)
	for _, task := range s.Plan.Value.Tasks {
		writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", task.ID+".yaml"), "allow: []\n")
	}
	commitAllPlan(t, a)
	pendingID, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		return domainspec.GoVerdict{}, err
	}

	if s.Escalations.Presence == domainspec.Known && s.Escalations.Value != nil {
		for _, e := range *s.Escalations.Value {
			if e.BlockScope == "" {
				continue
			}
			if _, err := a.EscalationCreate("plan:P1", e.BlockScope, "domainspec oracle freshness fixture"); err != nil {
				return domainspec.GoVerdict{}, err
			}
		}
	}

	name, email := "", ""
	if s.Approver.Presence == domainspec.Known && s.Approver.Value != nil {
		name, email = s.Approver.Value.Name, s.Approver.Value.Email
	}
	a.gitIdentityOverride = func() (string, string, error) { return name, email, nil }

	decision := derefStr(s.Decision)
	reason := derefStr(s.Reason)
	sel := riskSelectionsFromSnapshot(s.RiskSelections)
	if err := a.GateDecide(pendingID, decision, reason, sel); err != nil {
		ruleID, merr := classify(appGateDecidePatterns, err.Error())
		if merr != nil {
			return domainspec.GoVerdict{}, merr
		}
		return domainspec.GoVerdict{Outcome: domainspec.OutcomeBlocked, PrimaryRuleID: ruleID}, nil
	}
	return domainspec.GoVerdict{Outcome: domainspec.OutcomePass}, nil
}

// recompute 是 VerifyOracleFreshness 的 dispatcher：依 OracleSeam 路由到對應
// adapter。host_boundary 只出現在 acquisition_failed 案例（phase↔seam 契約，
// corpus.go 的 submitPhaseSeams），VerifyOracleFreshness 只對 evaluated 案例呼叫
// recompute，所以這個分支理論上不會被觸發——仍顯式回錯而非 panic，符合 brief
// 「dispatcher 若真的被以 host_boundary 呼叫必須回清楚的錯誤」的要求。
func recompute(t *testing.T) func(domainspec.CorpusCase) (domainspec.GoVerdict, error) {
	return func(c domainspec.CorpusCase) (domainspec.GoVerdict, error) {
		switch c.OracleSeam {
		case "gate_service_submit":
			return recomputeGateServiceSubmit(t, c)
		case "gatepolicy_validate":
			return recomputeGatepolicyValidate(c)
		case "gate_service_prepare":
			return recomputeGateServicePrepare(t, c)
		case "gatepolicy_reconcile":
			return recomputeGatepolicyReconcile(c)
		case "gatepolicy_build":
			return recomputeGatepolicyBuild(c)
		case "escalation":
			return recomputeEscalation(c)
		case "app_gatedecide":
			return recomputeAppGateDecide(t, c)
		case "host_boundary":
			return domainspec.GoVerdict{}, fmt.Errorf("domainspec oracle freshness: host_boundary seam has no recompute (acquisition_failed only), case %q", c.Name)
		default:
			return domainspec.GoVerdict{}, fmt.Errorf("domainspec oracle freshness: unknown oracle_seam %q (case %q)", c.OracleSeam, c.Name)
		}
	}
}

// ---- Required tests ----

func TestOracleFreshnessAllFresh(t *testing.T) {
	cases := loadRootCorpus(t)
	mismatched, err := domainspec.VerifyOracleFreshness(cases, recompute(t))
	if err != nil {
		t.Fatalf("VerifyOracleFreshness: %v", err)
	}
	if len(mismatched) != 0 {
		t.Fatalf("oracle freshness mismatches (production disagrees with the frozen corpus verdict): %v", mismatched)
	}
}

func TestOracleFreshnessDetectsCorruption(t *testing.T) {
	cases := loadRootCorpus(t)
	corrupted := make([]domainspec.CorpusCase, len(cases))
	copy(corrupted, cases)

	const target = "isolated-R1"
	idx := -1
	for i, c := range corrupted {
		if c.Name == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("corpus case %q not found", target)
	}
	if corrupted[idx].GoVerdict.Outcome != domainspec.OutcomeBlocked {
		t.Fatalf("expected %q to be a blocked case, got %+v", target, corrupted[idx].GoVerdict)
	}
	flipped := domainspec.GoVerdict{Outcome: domainspec.OutcomePass}
	corrupted[idx].GoVerdict = &flipped

	mismatched, err := domainspec.VerifyOracleFreshness(corrupted, recompute(t))
	if err != nil {
		t.Fatalf("VerifyOracleFreshness: %v", err)
	}
	if len(mismatched) != 1 || mismatched[0] != target {
		t.Fatalf("want exactly [%s], got %v", target, mismatched)
	}
}

// writeCorpusBatch 是 corpus JSON 落檔的唯一入口——先 ValidateCorpus 整批，
// 任一筆違反 union 契約就整批不寫檔（plan rev7/rev8：驗完才開始寫）。
func writeCorpusBatch(dir string, cases []domainspec.CorpusCase) error {
	if err := domainspec.ValidateCorpus(cases); err != nil {
		return err
	}
	for _, c := range cases {
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal case %s: %w", c.Name, err)
		}
		path := filepath.Join(dir, c.Name+".json")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func TestGeneratorAbortsOnUnionInvalid(t *testing.T) {
	dir := t.TempDir()

	good := domainspec.CorpusCase{
		Name: "ok-case", Kind: domainspec.CorpusKindAcquisitionFailed,
		EvaluationPhase: "submit", OracleSeam: "host_boundary", Provenance: "host_boundary",
		Reason: "boom", Role: "none",
	}
	// bad：kind=evaluated 卻缺 snapshot/go_verdict/digest 且帶 reason——
	// union 契約明確禁止（validateEvaluatedCase）。
	bad := domainspec.CorpusCase{
		Name: "bad-case", Kind: domainspec.CorpusKindEvaluated,
		EvaluationPhase: "submit", OracleSeam: "host_boundary", Provenance: "host_boundary",
		Reason: "must not be set on an evaluated case", Role: "none",
	}

	if err := writeCorpusBatch(dir, []domainspec.CorpusCase{good, bad}); err == nil {
		t.Fatal("expected a union-invalid batch to be rejected")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("union-invalid batch must write zero files, found %v", entries)
	}
}

// TestRegenerateCorpusVerdicts：corpus JSON 的 go_verdict 欄位是 Task 7 手動
// 依 production 語意標定的（未曾被機械驗證過），本測試以本檔的 seam adapters
// 對每筆 evaluated 案例重新呼叫真正的 production 程式碼、重算 facts_digest，
// 全批次驗證通過後才覆寫回 testdata/corpus/*.json——這是這 36 筆 go_verdict
// 的第一次機械驗證／正規化。只在 UPDATE_CORPUS=1 時執行（CI 不跑）。
func TestRegenerateCorpusVerdicts(t *testing.T) {
	if os.Getenv("UPDATE_CORPUS") != "1" {
		t.Skip("set UPDATE_CORPUS=1 to mechanically regenerate corpus go_verdict/facts_digest")
	}
	cases := loadRootCorpus(t)
	rc := recompute(t)

	updated := make([]domainspec.CorpusCase, len(cases))
	for i, c := range cases {
		if c.Kind != domainspec.CorpusKindEvaluated {
			updated[i] = c
			continue
		}
		gv, err := rc(c)
		if err != nil {
			t.Fatalf("case %q: recompute: %v", c.Name, err)
		}
		digest, err := domainspec.SnapshotDigest(c.Snapshot)
		if err != nil {
			t.Fatalf("case %q: snapshot digest: %v", c.Name, err)
		}
		c.GoVerdict = &gv
		c.FactsDigest = digest
		updated[i] = c
	}

	if err := writeCorpusBatch(corpusDir, updated); err != nil {
		t.Fatalf("writeCorpusBatch: %v", err)
	}
	t.Logf("regenerated %d corpus cases in %s", len(updated), corpusDir)
}
