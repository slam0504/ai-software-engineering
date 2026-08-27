package domainspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"gopkg.in/yaml.v3"
)

// Rule 是 CEL rule bundle 的單條規則（spec §4／rev5）。DependsOn 限同 phase
// （載入時驗證，見 validateRules）；StepRank／Stage／CheckRank 是四層 primary
// precedence 的靜態 rank（Task 6 使用）。
type Rule struct {
	ID        string   `yaml:"id" json:"id"`
	Phase     string   `yaml:"phase" json:"phase"`           // "submit" | "decide"（單值，spec rev4）
	When      string   `yaml:"when" json:"when"`             // CEL 布林式
	Effect    string   `yaml:"effect" json:"effect"`         // "deny" | "allow"
	Target    string   `yaml:"target" json:"target"`         // "decision.eligibility" | "risk.task"（逐 task 實體化為 risk.T<id>） | "binding.kind"（逐 kind 實體化為 binding.<kind>）
	DependsOn []string `yaml:"depends_on" json:"depends_on"` // 限同 phase（spec rev5）
	Priority  int      `yaml:"priority" json:"priority"`
	Verdict   string   `yaml:"verdict" json:"verdict"`
	Refs      string   `yaml:"refs" json:"refs"`
	PerTask   bool     `yaml:"per_task" json:"per_task"`
	PerKind   bool     `yaml:"per_kind" json:"per_kind"` // plan rev3：對 required_kinds 逐 kind 實體化（R8/R9）
	// 四層 primary precedence 靜態 rank（spec §4；Task 6 使用）：
	StepRank  int    `yaml:"step_rank" json:"step_rank"`
	Stage     string `yaml:"stage" json:"stage"`           // "none" | "pre_loop" | "task_loop" | "post_loop"
	CheckRank int    `yaml:"check_rank" json:"check_rank"` // 僅 stage=task_loop 有意義
}

// RequiredKind 是 bundle 對輸入 binding kind 的宣告；Pattern 是 digest regex
// （沿 gate2.go:28-31）。
type RequiredKind struct {
	Kind    string `yaml:"kind" json:"kind"`
	Pattern string `yaml:"pattern" json:"pattern"` // digest regex（沿 gate2.go:28-31）
}

// Bundle 是 strict-decoded YAML 的原始形——欄位宣告序＝canonical JSON／digest 的序
// （見 bundleDigest）。
type Bundle struct {
	SchemaVersion int            `yaml:"schema_version" json:"schema_version"`
	RequiredKinds []RequiredKind `yaml:"required_kinds" json:"required_kinds"` // 順序＝gate2BindingReqs（gate2.go:42-48），per_kind 實體化與 precedence 依此
	Rules         []Rule         `yaml:"rules" json:"rules"`
}

// CompiledRule 是 LoadBundle 驗證通過後的規則——RefVars 是 checked AST 頂層 fact
// 變數引用集（missing/not_applicable 判定；載入時決定）。
//
// Program 刻意不在載入時建立、也不跨 Evaluate 快取：cel.CostLimit 是 Program
// 建構期 option（plan rev4），每次 Evaluate 依當次 runtimeCostLimit 重建（spike
// 規模可接受；若要快取必須以 limit 為 key）。Task 4 負責每次 Evaluate 呼叫時
// 以 ast 建立 Program。
type CompiledRule struct {
	Rule
	RefVars map[string]bool
	ast     *cel.Ast
}

// CompiledBundle 是 LoadBundle 的產出——RequiredKinds 已驗證（順序保留、kind
// 唯一、pattern 為合法 regexp），是 Evaluate 逐 kind 實體化的輸入（plan rev4）。
type CompiledBundle struct {
	Digest        string // "sha256:" + hex(sha256(canonical YAML→JSON))
	RequiredKinds []RequiredKind
	Rules         []CompiledRule
	ByID          map[string]*CompiledRule
}

var (
	validRulePhases  = map[string]bool{"submit": true, "decide": true}
	validRuleEffects = map[string]bool{"deny": true, "allow": true}
	validRuleTargets = map[string]bool{"decision.eligibility": true, "risk.task": true, "binding.kind": true}
	validRuleStages  = map[string]bool{"none": true, "pre_loop": true, "task_loop": true, "post_loop": true}
)

// celTopLevelVars 是 celEnv 宣告的頂層變數名集合——RefVars 抽取只收這個集合內
// 的 ident（排除 CEL 巢狀巨集自行綁定的迭代變數）。
var celTopLevelVars = map[string]bool{
	"evaluation_phase": true, "decision": true, "reason": true, "base_commit_state": true,
	"approver": true, "entry": true,
	"request": true, "plan": true, "risk_policy": true, "task": true, "sel": true,
	"current":         true,
	"risk_selections": true, "escalations": true,
	"presence": true, "tier_order": true,
	"req_kind": true, "req_index": true, "req_pattern": true,
}

// tierRankOrder 沿用既有 tierOrder convention（internal/gatepolicy/gate2.go、
// internal/plan/types.go：low < medium < high）；tier_rank 是純函式 extension，
// 未知 tier 一律回 -1（spec §3 限純函式）。
var tierRankOrder = map[string]int64{"low": 1, "medium": 2, "high": 3}

func tierRankValue(tier string) int64 {
	if rank, ok := tierRankOrder[tier]; ok {
		return rank
	}
	return -1
}

// celEnv：凍結變數宣告（全 bundle 共用，Task 4 evaluate 沿用同一份宣告）。
func celEnv() (*cel.Env, error) {
	env, err := cel.NewEnv(
		cel.Variable("evaluation_phase", cel.StringType),
		cel.Variable("decision", cel.StringType),
		cel.Variable("reason", cel.StringType),
		cel.Variable("base_commit_state", cel.StringType),
		cel.Variable("approver", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("entry", cel.MapType(cel.StringType, cel.BoolType)),
		cel.Variable("request", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("plan", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("risk_policy", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("task", cel.MapType(cel.StringType, cel.DynType)),
		// sel 宣告為 dyn（而非 map(string,dyn)）：R25 的 guard 需要 `sel == null`
		// 成立（Task 4 runtime binding；task_id 對 risk_selections 無配對時
		// sel 為 null），但 cel-go v0.32.0 type-checker 對具體 Map 型別 vs
		// null 的 `==` 比較不放行（只有 dyn／legacy nullable kind 才允許），
		// 故改宣告為 dyn（controller ruling，Task 4 發現並修正）。
		cel.Variable("sel", cel.DynType),
		cel.Variable("current", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("risk_selections", cel.ListType(cel.MapType(cel.StringType, cel.StringType))),
		cel.Variable("escalations", cel.ListType(cel.MapType(cel.StringType, cel.StringType))),
		cel.Variable("presence", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("tier_order", cel.MapType(cel.StringType, cel.IntType)),
		cel.Variable("req_kind", cel.StringType),
		cel.Variable("req_index", cel.IntType),
		cel.Variable("req_pattern", cel.StringType),
		cel.Function("tier_rank",
			cel.Overload("tier_rank_string", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(func(arg ref.Val) ref.Val {
					s, ok := arg.(types.String)
					if !ok {
						return types.NewErr("domainspec: tier_rank: expected string argument, got %v", arg.Type())
					}
					return types.Int(tierRankValue(string(s)))
				}),
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("domainspec: bundle: cel env: %w", err)
	}
	return env, nil
}

// fixedSizeCostEstimator 是 static cost estimate 用的最小 estimator：對任何
// CEL 無法從常值推知大小的 AST node（list/map/string），一律回固定 size hint
// 64（出口 2 規模足夠；不追求精確 runtime 大小）。EstimateCallCost 一律回 nil，
// 交給 CEL 內建 cost table 處理。
type fixedSizeCostEstimator struct{}

func (fixedSizeCostEstimator) EstimateSize(_ checker.AstNode) *checker.SizeEstimate {
	return &checker.SizeEstimate{Min: 0, Max: 64}
}

func (fixedSizeCostEstimator) EstimateCallCost(_, _ string, _ *checker.AstNode, _ []checker.AstNode) *checker.CallEstimate {
	return nil
}

// LoadBundle：strict YAML（KnownFields）→ enum／id 唯一／depends_on 存在且同 phase →
// CEL compile/type-check（輸出必須 bool）→ static cost estimate 超限拒收 →
// SCC 無環 → RefVars 抽取 → RequiredKinds 驗證 → digest。
func LoadBundle(yamlSrc []byte, staticCostLimit uint64) (*CompiledBundle, error) {
	var raw Bundle
	dec := yaml.NewDecoder(bytes.NewReader(yamlSrc))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("domainspec: bundle: decode: %w", err)
	}

	if err := validateRules(raw.Rules); err != nil {
		return nil, err
	}

	env, err := celEnv()
	if err != nil {
		return nil, err
	}
	estimator := fixedSizeCostEstimator{}

	asts := make([]*cel.Ast, len(raw.Rules))
	for i, rule := range raw.Rules {
		checkedAST, iss := env.Compile(rule.When)
		if iss.Err() != nil {
			return nil, fmt.Errorf("domainspec: bundle: rule %q: when compile: %w", rule.ID, iss.Err())
		}
		if checkedAST.OutputType() != cel.BoolType {
			return nil, fmt.Errorf("domainspec: bundle: rule %q: when must evaluate to bool, got %s", rule.ID, checkedAST.OutputType())
		}
		cost, err := env.EstimateCost(checkedAST, estimator)
		if err != nil {
			return nil, fmt.Errorf("domainspec: bundle: rule %q: estimate cost: %w", rule.ID, err)
		}
		if cost.Max > staticCostLimit {
			return nil, fmt.Errorf("domainspec: bundle: rule %q: static cost %d exceeds limit %d", rule.ID, cost.Max, staticCostLimit)
		}
		asts[i] = checkedAST
	}

	if err := detectDependencyCycles(raw.Rules); err != nil {
		return nil, err
	}

	if err := validateRequiredKinds(raw.RequiredKinds); err != nil {
		return nil, err
	}

	compiledRules := make([]CompiledRule, len(raw.Rules))
	byID := make(map[string]*CompiledRule, len(raw.Rules))
	for i, rule := range raw.Rules {
		compiledRules[i] = CompiledRule{
			Rule:    rule,
			RefVars: extractRefVars(asts[i]),
			ast:     asts[i],
		}
		byID[rule.ID] = &compiledRules[i]
	}

	digest, err := bundleDigest(&raw)
	if err != nil {
		return nil, err
	}

	return &CompiledBundle{
		Digest:        digest,
		RequiredKinds: raw.RequiredKinds,
		Rules:         compiledRules,
		ByID:          byID,
	}, nil
}

// validateRules 檢查 enum（phase／effect／target／stage）、per_kind／per_task
// 與 target 的一致性、id 唯一、depends_on 存在且同 phase（spec rev5——跨 phase
// 依賴在載入時就必須拒收）。
func validateRules(rules []Rule) error {
	seen := make(map[string]Rule, len(rules))
	for _, r := range rules {
		if !validRulePhases[r.Phase] {
			return fmt.Errorf("domainspec: bundle: rule %q: invalid phase %q", r.ID, r.Phase)
		}
		if !validRuleEffects[r.Effect] {
			return fmt.Errorf("domainspec: bundle: rule %q: invalid effect %q", r.ID, r.Effect)
		}
		if !validRuleTargets[r.Target] {
			return fmt.Errorf("domainspec: bundle: rule %q: invalid target %q", r.ID, r.Target)
		}
		if !validRuleStages[r.Stage] {
			return fmt.Errorf("domainspec: bundle: rule %q: invalid stage %q", r.ID, r.Stage)
		}
		// per_kind／per_task 與 target 一致性（controller ruling）：
		// per_kind:true ⇔ target=="binding.kind"；per_task:true ⇔ target=="risk.task"。
		if r.PerKind && r.Target != "binding.kind" {
			return fmt.Errorf("domainspec: bundle: rule %q: per_kind=true requires target %q, got %q", r.ID, "binding.kind", r.Target)
		}
		if !r.PerKind && r.Target == "binding.kind" {
			return fmt.Errorf("domainspec: bundle: rule %q: target %q requires per_kind=true", r.ID, r.Target)
		}
		if r.PerTask && r.Target != "risk.task" {
			return fmt.Errorf("domainspec: bundle: rule %q: per_task=true requires target %q, got %q", r.ID, "risk.task", r.Target)
		}
		if !r.PerTask && r.Target == "risk.task" {
			return fmt.Errorf("domainspec: bundle: rule %q: target %q requires per_task=true", r.ID, r.Target)
		}
		if _, dup := seen[r.ID]; dup {
			return fmt.Errorf("domainspec: bundle: duplicate rule id %q", r.ID)
		}
		seen[r.ID] = r
	}
	for _, r := range rules {
		for _, dep := range r.DependsOn {
			depRule, ok := seen[dep]
			if !ok {
				return fmt.Errorf("domainspec: bundle: rule %q: depends_on %q does not exist", r.ID, dep)
			}
			if depRule.Phase != r.Phase {
				return fmt.Errorf("domainspec: bundle: rule %q (phase %q): depends_on %q is in phase %q", r.ID, r.Phase, dep, depRule.Phase)
			}
		}
	}
	return nil
}

// detectDependencyCycles 對 depends_on 圖做 DFS 三色標記（white/gray/black）；
// 灰點被重訪代表出現環。
func detectDependencyCycles(rules []Rule) error {
	const (
		white = iota
		gray
		black
	)
	deps := make(map[string][]string, len(rules))
	for _, r := range rules {
		deps[r.ID] = r.DependsOn
	}
	color := make(map[string]int, len(rules))

	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case black:
			return nil
		case gray:
			return fmt.Errorf("domainspec: bundle: dependency cycle detected at rule %q", id)
		}
		color[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}

	for _, r := range rules {
		if color[r.ID] == white {
			if err := visit(r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateRequiredKinds：順序保留（呼叫者不重排 slice）、kind 唯一、pattern
// 必須是合法 regexp。
func validateRequiredKinds(kinds []RequiredKind) error {
	seen := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		if seen[k.Kind] {
			return fmt.Errorf("domainspec: bundle: duplicate required_kinds kind %q", k.Kind)
		}
		seen[k.Kind] = true
		if _, err := regexp.Compile(k.Pattern); err != nil {
			return fmt.Errorf("domainspec: bundle: required_kinds %q: invalid pattern %q: %w", k.Kind, k.Pattern, err)
		}
	}
	return nil
}

// extractRefVars 走訪 checked AST，收集出現在 celTopLevelVars 集合內、且未被
// comprehension 巨集（.exists()／.all() 等）迭代變數遮蔽的頂層 ident
// （missing/not_applicable 判定的輸入，Task 4 使用）。
//
// 手寫遞迴（而非 celast.PostOrderVisit 的無狀態 Visitor）是必要的：遮蔽關係
// 只在「目前這條路徑上是否處於某個 comprehension 的迴圈子樹」才成立，PostOrderVisit
// 的單一 callback 沒有攜帶路徑上下文，無法分辨 escalations.exists(entry, ...) 裡
// 的 entry 是巨集綁定的迭代變數、還是頂層同名變數。
func extractRefVars(checkedAST *cel.Ast) map[string]bool {
	refVars := make(map[string]bool)
	walkRefVars(checkedAST.NativeRep().Expr(), nil, refVars)
	return refVars
}

// walkRefVars 遞迴走訪；shadowed 是目前路徑上、由外層 comprehension 引入而
// 遮蔽同名頂層變數的迭代／累加變數名集合。comprehension 的 IterRange（被迭代
// 的來源運算式，如 escalations.exists(entry, ...) 裡的 escalations）不受
// 該 comprehension 自己引入的遮蔽影響——它在外層 scope 求值，只有
// LoopCondition／LoopStep／Result 才在遮蔽範圍內。
func walkRefVars(e celast.Expr, shadowed map[string]bool, refVars map[string]bool) {
	if e == nil {
		return
	}
	switch e.Kind() {
	case celast.IdentKind:
		name := e.AsIdent()
		if celTopLevelVars[name] && !shadowed[name] {
			refVars[name] = true
		}
	case celast.SelectKind:
		walkRefVars(e.AsSelect().Operand(), shadowed, refVars)
	case celast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			walkRefVars(call.Target(), shadowed, refVars)
		}
		for _, arg := range call.Args() {
			walkRefVars(arg, shadowed, refVars)
		}
	case celast.ListKind:
		for _, elem := range e.AsList().Elements() {
			walkRefVars(elem, shadowed, refVars)
		}
	case celast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			me := entry.AsMapEntry()
			walkRefVars(me.Key(), shadowed, refVars)
			walkRefVars(me.Value(), shadowed, refVars)
		}
	case celast.StructKind:
		for _, field := range e.AsStruct().Fields() {
			walkRefVars(field.AsStructField().Value(), shadowed, refVars)
		}
	case celast.ComprehensionKind:
		comp := e.AsComprehension()
		// IterRange 在外層 scope 求值，不受本層新增的遮蔽影響。
		walkRefVars(comp.IterRange(), shadowed, refVars)

		inner := make(map[string]bool, len(shadowed)+3)
		for k, v := range shadowed {
			inner[k] = v
		}
		inner[comp.IterVar()] = true
		if comp.HasIterVar2() {
			inner[comp.IterVar2()] = true
		}
		inner[comp.AccuVar()] = true

		walkRefVars(comp.LoopCondition(), inner, refVars)
		walkRefVars(comp.LoopStep(), inner, refVars)
		walkRefVars(comp.Result(), inner, refVars)
	}
}

// bundleDigest = "sha256:" + hex(sha256(canonical JSON))。canonical 形＝
// strict-decoded Bundle 依 Go 欄位宣告序 json.Marshal（Rules 保留檔案／解碼
// 順序，同 Task 2 SnapshotDigest 的作法）。
func bundleDigest(b *Bundle) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("domainspec: bundle: canonical marshal: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
