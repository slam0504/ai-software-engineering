package domainspec

import (
	"fmt"
	"sort"

	"cel.dev/cel-go/cel"
)

// Truth 是 Evaluate 的全域裁決結果；全域 truth 依序 conflict → deny(false) →
// unknown → true（spec §3，owner 凍結；error 不併入 unknown——見 Status）。
type Truth string

const (
	TruthTrue     Truth = "true"
	TruthFalse    Truth = "false"
	TruthUnknown  Truth = "unknown"
	TruthConflict Truth = "conflict"
)

// Status 標示評估過程本身是否成功（與 Truth 正交）：runtime cost 超限等
// evaluation-time 錯誤只反映在 Status，不折入 Truth／UnknownLeaves（owner 凍結）。
type Status string

const (
	StatusOK              Status = "ok"
	StatusEvaluationError Status = "evaluation_error"
)

// Violation 是單條規則單一實體化實例的完整命中紀錄（deny／allow 皆收，供
// explain 與 Task 6 primary 裁決使用）。
type Violation struct {
	RuleID      string `json:"rule_id"`
	Target      string `json:"target"`       // 實體化後（risk.T<id>／binding.<kind>）
	SourceIndex int    `json:"source_index"` // per_task→task source_index；per_kind→required-kind index；否則 -1
	Verdict     string `json:"verdict"`
}

// ReasonEntry 是單一規則實例的評估軌跡（無論是否命中）。
type ReasonEntry struct {
	RuleID  string `json:"rule_id"`
	Target  string `json:"target"`
	Outcome string `json:"outcome"` // "matched" | "not_matched" | "not_eligible" | "unknown" | "error"
	Cause   string `json:"cause"`
}

// Result 是 Evaluate 的完整輸出——ReasonGraph 依 bundle 規則序＋task
// source_index／kind index 排列，UnknownLeaves／MatchedRuleIDs／
// ConflictingRuleIDs 皆排序後輸出（出口 4：deterministic bytes）。
type Result struct {
	Truth              Truth         `json:"truth"`
	Status             Status        `json:"status"`
	UnknownLeaves      []string      `json:"unknown_leaves"`
	MatchedRuleIDs     []string      `json:"matched_rule_ids"`
	ConflictingRuleIDs []string      `json:"conflicting_rule_ids"`
	Violations         []Violation   `json:"violations"`
	ReasonGraph        []ReasonEntry `json:"reason_graph"`
}

// factGroupOrder 是 FactsSnapshot 的 fact 群組（Fact[T] wrapper 欄位）在 CEL
// 頂層變數命名空間中的名稱，依 struct 宣告序排列——用來判定 rule.RefVars 與
// presence 的交集，以及決定「第一個」missing／not_applicable 群組（cause 訊息
// 的 deterministic 選取）。task／sel／presence／tier_order／req_* 不是 fact
// 群組（不受 missing/not_applicable 語意約束），刻意不列入。
var factGroupOrder = []string{
	"decision", "reason", "approver", "entry", "request",
	"current", "base_commit_state", "plan", "risk_policy",
	"risk_selections", "escalations",
}

// instanceOutcome 是單一規則實例（normal 規則的唯一實例、或 per_task／
// per_kind 展開後的其中一個實例）的評估結果——index -1 代表 normal 規則的唯一
// 實例、或 per_task／per_kind 因 Plan／Request presence 非 known 而整組
// 短路的 group-wide 實例。
type instanceOutcome struct {
	index   int
	target  string
	outcome string // "matched" | "not_matched" | "not_eligible" | "unknown" | "error"
	cause   string
}

// ruleState 收集單條規則（依 rule ID）所有實例的結果，供依賴解析（depends_on）
// 與最終 ReasonGraph 組裝使用。
type ruleState struct {
	instances map[int]instanceOutcome
}

// buildPresenceMap 抽出 FactsSnapshot 每個 fact 群組的 Presence，鍵為
// factGroupOrder 使用的 CEL 頂層變數名。
func buildPresenceMap(s *FactsSnapshot) map[string]Presence {
	return map[string]Presence{
		"decision":          s.Decision.Presence,
		"reason":            s.Reason.Presence,
		"approver":          s.Approver.Presence,
		"entry":             s.Entry.Presence,
		"request":           s.Request.Presence,
		"current":           s.Current.Presence,
		"base_commit_state": s.BaseCommitState.Presence,
		"plan":              s.Plan.Presence,
		"risk_policy":       s.RiskPolicy.Presence,
		"risk_selections":   s.RiskSelections.Presence,
		"escalations":       s.Escalations.Presence,
	}
}

func derefString(f Fact[string]) string {
	if f.Presence == Known && f.Value != nil {
		return *f.Value
	}
	return ""
}

func approverActivation(f Fact[Approver]) map[string]string {
	if f.Presence == Known && f.Value != nil {
		return map[string]string{"name": f.Value.Name, "email": f.Value.Email}
	}
	return map[string]string{}
}

func entryActivation(f Fact[Entry]) map[string]bool {
	if f.Presence == Known && f.Value != nil {
		return map[string]bool{
			"exists": f.Value.Exists, "has_request": f.Value.HasRequest, "has_record": f.Value.HasRecord,
		}
	}
	return map[string]bool{}
}

func currentActivation(f Fact[Current]) map[string]string {
	if f.Presence == Known && f.Value != nil {
		return map[string]string{
			"spec_manifest": f.Value.SpecManifest, "plan_manifest": f.Value.PlanManifest,
			"risk_policy": f.Value.RiskPolicy, "permission_manifest": f.Value.PermissionManifest,
		}
	}
	return map[string]string{}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func impactActivation(im Impact) map[string]any {
	return map[string]any{"contexts": toAnySlice(im.Contexts), "modules": toAnySlice(im.Modules)}
}

func requestActivation(f Fact[Request]) map[string]any {
	if f.Presence == Known && f.Value != nil {
		bindings := make([]any, len(f.Value.Bindings))
		for i, b := range f.Value.Bindings {
			bindings[i] = map[string]string{"kind": b.Kind, "role": b.Role, "ref": b.Ref, "digest": b.Digest}
		}
		return map[string]any{"gate": f.Value.Gate, "subject": f.Value.Subject, "bindings": bindings}
	}
	return map[string]any{}
}

func taskActivationMap(t PlanTask) map[string]any {
	return map[string]any{
		"id":                t.ID,
		"source_index":      int64(t.SourceIndex),
		"minimum_risk_tier": t.MinimumRiskTier,
		"planner_risk_tier": t.PlannerRiskTier,
		"impact":            impactActivation(t.Impact),
	}
}

func planActivation(f Fact[PlanFacts]) map[string]any {
	if f.Presence == Known && f.Value != nil {
		tasks := make([]any, len(f.Value.Tasks))
		for i, t := range f.Value.Tasks {
			tasks[i] = taskActivationMap(t)
		}
		return map[string]any{"tasks": tasks}
	}
	return map[string]any{}
}

func riskPolicyActivation(f Fact[RiskPolicyFacts]) map[string]any {
	if f.Presence == Known && f.Value != nil {
		rules := make([]any, len(f.Value.Rules))
		for i, r := range f.Value.Rules {
			rules[i] = map[string]any{"match": impactActivation(r.Match), "tier": r.Tier}
		}
		return map[string]any{"default_tier": f.Value.DefaultTier, "rules": rules}
	}
	return map[string]any{}
}

func riskSelectionsActivation(f Fact[[]RiskSelection]) []map[string]string {
	if f.Presence == Known && f.Value != nil {
		out := make([]map[string]string, len(*f.Value))
		for i, sel := range *f.Value {
			out[i] = map[string]string{
				"task_id": sel.TaskID, "selected_risk_tier": sel.SelectedRiskTier, "override_reason": sel.OverrideReason,
			}
		}
		return out
	}
	return []map[string]string{}
}

func escalationsActivation(f Fact[[]EscalationFact]) []map[string]string {
	if f.Presence == Known && f.Value != nil {
		out := make([]map[string]string, len(*f.Value))
		for i, e := range *f.Value {
			out[i] = map[string]string{"escalation_id": e.EscalationID, "state": e.State, "block_scope": e.BlockScope}
		}
		return out
	}
	return []map[string]string{}
}

// findSelection 依 task_id 對（已 canonical 排序的）risk_selections 取首筆
// 配對，無則回傳 nil（CEL 端讀到 null——spec §3 per_task 展開語意）。
func findSelection(f Fact[[]RiskSelection], taskID string) any {
	if f.Presence != Known || f.Value == nil {
		return nil
	}
	for _, sel := range *f.Value {
		if sel.TaskID == taskID {
			return map[string]string{
				"task_id": sel.TaskID, "selected_risk_tier": sel.SelectedRiskTier, "override_reason": sel.OverrideReason,
			}
		}
	}
	return nil
}

// buildBaseActivation 由 canonical 正規化複本建構 celEnv 宣告的全部頂層變數
// （task／sel／req_kind／req_index／req_pattern 先填佔位預設值，per_task／
// per_kind 展開時逐實例覆寫——見 withOverrides）。
func buildBaseActivation(cp *FactsSnapshot, presence map[string]Presence) map[string]any {
	presenceActivation := make(map[string]string, len(presence))
	for k, v := range presence {
		presenceActivation[k] = string(v)
	}
	return map[string]any{
		"evaluation_phase":  cp.EvaluationPhase,
		"decision":          derefString(cp.Decision),
		"reason":            derefString(cp.Reason),
		"base_commit_state": derefString(cp.BaseCommitState),
		"approver":          approverActivation(cp.Approver),
		"entry":             entryActivation(cp.Entry),
		"request":           requestActivation(cp.Request),
		"plan":              planActivation(cp.Plan),
		"risk_policy":       riskPolicyActivation(cp.RiskPolicy),
		"task":              map[string]any{},
		"sel":               nil,
		"current":           currentActivation(cp.Current),
		"risk_selections":   riskSelectionsActivation(cp.RiskSelections),
		"escalations":       escalationsActivation(cp.Escalations),
		"presence":          presenceActivation,
		"tier_order":        tierRankOrder, // bundle.go：production tier order（low/medium/high）
		"req_kind":          "",
		"req_index":         int64(0),
		"req_pattern":       "",
	}
}

func withOverrides(base map[string]any, overrides map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

// refFactGroups 回傳 refVars 與 factGroupOrder 的交集，依 factGroupOrder 固定
// 序——用於 deterministic 選取「第一個」missing／not_applicable 群組。
func refFactGroups(refVars map[string]bool) []string {
	var out []string
	for _, g := range factGroupOrder {
		if refVars[g] {
			out = append(out, g)
		}
	}
	return out
}

// checkOwnPresence 是規則本身（非 per_task／per_kind 結構性 gate）的 presence
// 語意（plan rev2 P1 凍結）：RefVars∩missing→unknown（missingGroups 全數回傳，
// 供 unknown_leaves 累積）；RefVars∩not_applicable→not_eligible（不影響
// truth）。outcome=="" 代表兩者皆未觸發，可以繼續往下評估 CEL。
func checkOwnPresence(refVars map[string]bool, presence map[string]Presence) (outcome, cause string, missingGroups []string) {
	groups := refFactGroups(refVars)
	for _, g := range groups {
		if presence[g] == Missing {
			missingGroups = append(missingGroups, g)
		}
	}
	if len(missingGroups) > 0 {
		return "unknown", fmt.Sprintf("fact %s missing", missingGroups[0]), missingGroups
	}
	for _, g := range groups {
		if presence[g] == NotApplicable {
			return "not_eligible", fmt.Sprintf("fact %s not_applicable", g), nil
		}
	}
	return "", "", nil
}

// structuralGate 是 per_task（群組="plan"）／per_kind（群組="request"）規則的
// 整組短路判定（plan rev3）：Plan／Request presence!=known 時，整條規則只產出
// 一個 group-wide 實例（index -1），不逐 task／kind 展開。
func structuralGate(presence Presence, group string) (outcome, cause string, groupWide bool) {
	switch presence {
	case Missing:
		return "unknown", fmt.Sprintf("fact %s missing", group), true
	case NotApplicable:
		return "not_eligible", fmt.Sprintf("fact %s not_applicable", group), true
	default:
		return "", "", false
	}
}

// dependencyLabel 把依賴實例的 outcome 轉成 spec rev6 cause 用語
// （false／unknown／not_eligible／error）。
func dependencyLabel(outcome string) string {
	switch outcome {
	case "not_matched":
		return "false"
	default:
		return outcome // "unknown" | "not_eligible" | "error"
	}
}

// lookupDependency 依 depRule 的基數精確配對實例，不做 index 猜測（controller
// ruling，Task 4：LoadBundle 的 depends_on cardinality 驗證已保證合法組合只剩
// 兩種——見 validateRules）：
//   - depRule 是 scalar（非 per_task／per_kind）：唯一實例固定在 index -1，
//     與呼叫方（callerIndex）的基數無關；
//   - depRule 與呼叫方同基數（per_task↔per_task／per_kind↔per_kind）：exact
//     index 配對（同一 snapshot 下兩者展開的索引集合恆相同——見 Evaluate 的
//     per_task／per_kind 展開，皆源自同一 cp.Plan.Value.Tasks／b.RequiredKinds）。
//
// 找不到實例（理論上不應發生，LoadBundle 已擋下基數不合法的組合）視為
// unresolved，交由呼叫方 fail loud，不再退而猜測 index。
func lookupDependency(states map[string]*ruleState, depRule *CompiledRule, callerIndex int) (instanceOutcome, bool) {
	rs, ok := states[depRule.ID]
	if !ok {
		return instanceOutcome{}, false
	}
	if !depRule.PerTask && !depRule.PerKind {
		r, ok := rs.instances[-1]
		return r, ok
	}
	r, ok := rs.instances[callerIndex]
	return r, ok
}

// checkDependencies 是 eligibility 傳遞封閉（spec rev6）：eligible ⇔ 所有
// dependency eligible 且 when=true；依賴 false／unknown／not_eligible／error
// 皆使下游 not eligible（when 不評估），cause 記錄哪個依賴、哪種狀態。實例
// unresolved（不應發生，見 lookupDependency 註解）一律 fail loud，不猜測。
func checkDependencies(rule *CompiledRule, callerIndex int, states map[string]*ruleState, byID map[string]*CompiledRule) (eligible bool, cause string) {
	for _, dep := range rule.DependsOn {
		depRule, ok := byID[dep]
		if !ok {
			return false, fmt.Sprintf("dependency %s instance unresolved", dep)
		}
		r, found := lookupDependency(states, depRule, callerIndex)
		if !found {
			return false, fmt.Sprintf("dependency %s instance unresolved", dep)
		}
		if r.outcome != "matched" {
			return false, fmt.Sprintf("dependency %s %s", dep, dependencyLabel(r.outcome))
		}
	}
	return true, ""
}

// evalInstance 是單一規則實例的兩階段聚合第一階段：依賴 eligibility → 本身
// presence → CEL when（runtime cost 超限或型別非 bool → outcome="error"，
// 不併入 unknown——owner 凍結）。
func evalInstance(rule *CompiledRule, index int, target string, activation map[string]any, states map[string]*ruleState, byID map[string]*CompiledRule, presence map[string]Presence, prog cel.Program, unknownGroups map[string]bool) instanceOutcome {
	if eligible, cause := checkDependencies(rule, index, states, byID); !eligible {
		return instanceOutcome{index: index, target: target, outcome: "not_eligible", cause: cause}
	}
	if outcome, cause, missing := checkOwnPresence(rule.RefVars, presence); outcome != "" {
		for _, g := range missing {
			unknownGroups[g] = true
		}
		return instanceOutcome{index: index, target: target, outcome: outcome, cause: cause}
	}
	val, _, err := prog.Eval(activation)
	if err != nil {
		return instanceOutcome{index: index, target: target, outcome: "error", cause: err.Error()}
	}
	matched, ok := val.Value().(bool)
	if !ok {
		return instanceOutcome{index: index, target: target, outcome: "error", cause: fmt.Sprintf("when did not evaluate to bool: %v", val.Value())}
	}
	if matched {
		return instanceOutcome{index: index, target: target, outcome: "matched"}
	}
	return instanceOutcome{index: index, target: target, outcome: "not_matched"}
}

// recordInstance 把單一實例結果併入全域累積（Violations／MatchedRuleIDs／
// evaluation_error 旗標）。Violations 只收 deny 命中（controller ruling，
// Task 4）：allow 命中若混進 Violations，會在 Task 6 的 PrimaryViolation
// 四層裁決裡冒充成一條「違規」，可能用較小的 rank tuple 蓋過真正擋下請求的
// deny——allow 命中仍完整保留在 MatchedRuleIDs／ReasonGraph，只是不進
// Violations 這份「deny 完整列表」。
func recordInstance(ir instanceOutcome, rule *CompiledRule, violations *[]Violation, matchedIDs map[string]bool, statusError *bool) {
	switch ir.outcome {
	case "matched":
		matchedIDs[rule.ID] = true
		if rule.Effect == "deny" {
			*violations = append(*violations, Violation{
				RuleID: rule.ID, Target: ir.target, SourceIndex: ir.index, Verdict: rule.Verdict,
			})
		}
	case "error":
		*statusError = true
	}
}

// topoOrder 對 rules 的 depends_on 圖做拓撲排序，同層（無序依賴關係）依 bundle
// 檔內原始順序（Kahn's algorithm，ready set 恆保持遞增序，每輪取最小 index）。
// LoadBundle 已保證無環。
func topoOrder(rules []CompiledRule) []int {
	n := len(rules)
	idxByID := make(map[string]int, n)
	for i, r := range rules {
		idxByID[r.ID] = i
	}
	inDegree := make([]int, n)
	dependents := make([][]int, n)
	for i, r := range rules {
		for _, dep := range r.DependsOn {
			dj := idxByID[dep]
			dependents[dj] = append(dependents[dj], i)
			inDegree[i]++
		}
	}
	ready := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.Ints(ready)
	order := make([]int, 0, n)
	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]
		order = append(order, next)
		for _, dep := range dependents[next] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				i := sort.SearchInts(ready, dep)
				ready = append(ready, 0)
				copy(ready[i+1:], ready[i:])
				ready[i] = dep
			}
		}
	}
	return order
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildReasonGraph 依 bundle 規則序（b.Rules 原始順序，非 topoOrder 的評估
// 序）＋每條規則內的實例 index（task source_index／kind index；group-wide
// 短路實例固定 -1）輸出 deterministic ReasonGraph（出口 4）。未評估的規則
// （phase 不符）不產生條目。
func buildReasonGraph(b *CompiledBundle, states map[string]*ruleState) []ReasonEntry {
	var out []ReasonEntry
	for _, rule := range b.Rules {
		rs, ok := states[rule.ID]
		if !ok {
			continue
		}
		indices := make([]int, 0, len(rs.instances))
		for idx := range rs.instances {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			ir := rs.instances[idx]
			out = append(out, ReasonEntry{RuleID: rule.ID, Target: ir.target, Outcome: ir.outcome, Cause: ir.cause})
		}
	}
	return out
}

// matchedEntry 是單一命中實例對其實體化 target 的貢獻（stage 2 per-target
// priority 裁決的輸入）。
type matchedEntry struct {
	ruleID   string
	priority int
	effect   string
}

// Evaluate 是 spec §3 兩階段聚合：stage 1 逐規則實例（依 topoOrder，保證依賴
// 先於下游）判定 eligibility／presence／CEL when；stage 2 依實體化 target 分組，
// 同 target 內取最高 priority 的命中集合裁決 deny／allow／conflict，最終依
// conflict → deny(false) → unknown → true 化簡為全域 Truth。
//
// Program 刻意在本次呼叫內、依當次 runtimeCostLimit 重建（plan rev4）——不得
// 跨 Evaluate 呼叫快取，否則第二次呼叫會誤沿用第一次的 cost limit
// （TestEvaluateRuntimeCostLimitNotCached）。
func Evaluate(b *CompiledBundle, s *FactsSnapshot, runtimeCostLimit uint64) (*Result, error) {
	if b == nil || s == nil {
		return nil, fmt.Errorf("domainspec: evaluate: nil bundle or snapshot")
	}

	cp := deepCopySnapshot(s)
	normalizeSnapshot(cp)

	presence := buildPresenceMap(cp)
	baseActivation := buildBaseActivation(cp, presence)

	env, err := celEnv()
	if err != nil {
		return nil, err
	}

	order := topoOrder(b.Rules)
	states := make(map[string]*ruleState, len(b.Rules))
	statusError := false
	unknownGroups := map[string]bool{}
	matchedIDs := map[string]bool{}
	conflictIDs := map[string]bool{}
	var violations []Violation
	targetMatches := map[string][]matchedEntry{}

	for _, idx := range order {
		rule := &b.Rules[idx]
		if rule.Phase != s.EvaluationPhase {
			continue
		}

		prog, progErr := env.Program(rule.ast, cel.CostLimit(runtimeCostLimit))
		if progErr != nil {
			return nil, fmt.Errorf("domainspec: evaluate: rule %q: build program: %w", rule.ID, progErr)
		}

		rs := &ruleState{instances: map[int]instanceOutcome{}}
		states[rule.ID] = rs

		record := func(ir instanceOutcome, target string) {
			recordInstance(ir, rule, &violations, matchedIDs, &statusError)
			if ir.outcome == "matched" {
				targetMatches[target] = append(targetMatches[target], matchedEntry{rule.ID, rule.Priority, rule.Effect})
			}
		}

		switch {
		case rule.PerTask:
			gateOutcome, gateCause, groupWide := structuralGate(presence["plan"], "plan")
			if groupWide {
				ir := instanceOutcome{index: -1, target: rule.Target, outcome: gateOutcome, cause: gateCause}
				rs.instances[-1] = ir
				if gateOutcome == "unknown" {
					unknownGroups["plan"] = true
				}
				continue
			}
			var tasks []PlanTask
			if cp.Plan.Value != nil {
				tasks = cp.Plan.Value.Tasks
			}
			for _, task := range tasks {
				activation := withOverrides(baseActivation, map[string]any{
					"task": taskActivationMap(task),
					"sel":  findSelection(cp.RiskSelections, task.ID),
				})
				target := "risk.T" + task.ID
				ir := evalInstance(rule, task.SourceIndex, target, activation, states, b.ByID, presence, prog, unknownGroups)
				rs.instances[task.SourceIndex] = ir
				record(ir, target)
			}

		case rule.PerKind:
			gateOutcome, gateCause, groupWide := structuralGate(presence["request"], "request")
			if groupWide {
				ir := instanceOutcome{index: -1, target: rule.Target, outcome: gateOutcome, cause: gateCause}
				rs.instances[-1] = ir
				if gateOutcome == "unknown" {
					unknownGroups["request"] = true
				}
				continue
			}
			for ki, rk := range b.RequiredKinds {
				activation := withOverrides(baseActivation, map[string]any{
					"req_kind": rk.Kind, "req_index": int64(ki), "req_pattern": rk.Pattern,
				})
				target := "binding." + rk.Kind
				ir := evalInstance(rule, ki, target, activation, states, b.ByID, presence, prog, unknownGroups)
				rs.instances[ki] = ir
				record(ir, target)
			}

		default:
			target := rule.Target
			ir := evalInstance(rule, -1, target, baseActivation, states, b.ByID, presence, prog, unknownGroups)
			rs.instances[-1] = ir
			record(ir, target)
		}
	}

	// stage 2：per-target 最高 priority 裁決＋conflict。
	anyDenied := false
	for _, entries := range targetMatches {
		maxPriority := entries[0].priority
		for _, e := range entries[1:] {
			if e.priority > maxPriority {
				maxPriority = e.priority
			}
		}
		hasDeny, hasAllow := false, false
		var top []matchedEntry
		for _, e := range entries {
			if e.priority == maxPriority {
				top = append(top, e)
				if e.effect == "deny" {
					hasDeny = true
				} else if e.effect == "allow" {
					hasAllow = true
				}
			}
		}
		if hasDeny && hasAllow {
			for _, e := range top {
				conflictIDs[e.ruleID] = true
			}
		} else if hasDeny {
			anyDenied = true
		}
	}

	truth := TruthTrue
	switch {
	case len(conflictIDs) > 0:
		truth = TruthConflict
	case anyDenied:
		truth = TruthFalse
	case len(unknownGroups) > 0:
		truth = TruthUnknown
	}

	status := StatusOK
	if statusError {
		status = StatusEvaluationError
	}

	return &Result{
		Truth:              truth,
		Status:             status,
		UnknownLeaves:      sortedKeys(unknownGroups),
		MatchedRuleIDs:     sortedKeys(matchedIDs),
		ConflictingRuleIDs: sortedKeys(conflictIDs),
		Violations:         violations,
		ReasonGraph:        buildReasonGraph(b, states),
	}, nil
}
