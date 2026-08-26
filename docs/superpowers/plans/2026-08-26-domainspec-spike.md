# DomainSpec kernel spike（Gate 2 shadow evaluator）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立 `internal/domainspec` 純 domain kernel——typed FactsSnapshot＋canonical digest、YAML+CEL rule bundle、兩階段聚合 evaluator、corpus 重放與 Go oracle 比對 harness，逐項滿足 spec §5 八項出口條件後產出 GO／NO-GO 收斂報告。

**Architecture:** 新 package 無 I/O（依 `internal/plan` 先例）；所有 I/O 結果由 host（測試 harness）解析成 facts 再進 evaluator。Bundle 載入時完成 CEL compile／type-check／static cost／同 phase depends_on／SCC 驗證，並從 checked AST 抽出每條規則引用的 fact 變數集（missing-leaf 判定用，不做 runtime 追蹤）。Evaluator 依 rev6 傳遞封閉語意跑 eligibility → per-target priority → 全域 truth；risk 規則對 `plan.tasks` 逐 task 實體化（綁 `task`＋`source_index`）。比對端以四層 primary precedence 從 violation 列表選 primary，對 Go oracle 的 pass／blocked＋primary error rule id＋RiskDecisions 逐案例比對。

**Tech Stack:** Go 1.25、`github.com/google/cel-go`（**新依賴**，實作時 `go get @latest` 解析版本；`ext.Strings()` 供 substring）、`gopkg.in/yaml.v3`（已有）、Go testing。

**Spec:** `docs/superpowers/specs/2026-08-26-domainspec-spike-design.md`——最終 snapshot **commit e09c3fe**（rev1→rev6，design gate APPROVED 2026-08-26）；附件 `2026-08-26-domainspec-rule-inventory.md` 同 commit。

## Global Constraints

- **不接管**：production 零改動——`gateDecide`、`internal/gate`、`internal/gatepolicy`、`internal/escalation`、`internal/plan` 全部只讀；出口 8 以 `git diff` 佐證。
- **不動 `spec/` scope**：bundle 放 `internal/domainspec/testdata/gate2-bundle.yaml`；發現需要 schema migration 或擴 `spec/rules/**` → **立即 NO-GO**（spec §6 強制停止條件）。
- 時間盒 **3 個 session**（Session 1：Task 1–4；Session 2：Task 5–7；Session 3：Task 8–9＋收斂）；屆期未齊八項出口即帶 diff 報告收 NO-GO，不延展。
- 每條規則引用的 production file:line 實作時先驗仍成立（spec 附錄 A 慣例）。
- 三態 presence：`known`／`not_applicable`／`missing`；acquisition failed 不產 snapshot、不評估（harness 層 tagged union）。
- error 不併入 unknown：`status=evaluation_error` 與 `truth=unknown` 互不吞併。
- gofmt 乾淨（觸碰檔案）；台灣用語書面中文 doc／commit；`go test ./internal/domainspec/... -count=1` 為每 task 通過門檻，收斂前跑全套 `go test ./...`。
- 已知豁免（收斂報告必列）：R32（RiskDecisions 排序決定性）為 harness 層檢查不進 CEL bundle；R15（scope 導出）derivation 內嵌於 R16 `when`，coverage 以 scope-sensitive 案例＋豁免說明交代；R13／R17／R19／R20／lineage 屬 host 層（spec §1 不進 CEL 清單）。

---

### Task 1: FactsSnapshot typed schema＋strict decode（出口 1）

**Files:**
- Create: `internal/domainspec/facts.go`
- Test: `internal/domainspec/facts_test.go`

**Interfaces:**
- Produces（後續全部 task 依賴）：

```go
package domainspec

// 三態 presence（spec §2）：nil pointer＝missing；NotApplicable 明確標記；否則 known。
type Presence string

const (
    Known         Presence = "known"
    NotApplicable Presence = "not_applicable"
    Missing       Presence = "missing"
)

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
    SourceIndex     int    `json:"source_index"` // plan 原始順序（spec §2 canonical：不得改 id 排序）
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
    Rules       []RiskRule `json:"rules"` // 保留來源順序（rev5）
}

type RiskSelection struct {
    TaskID           string `json:"task_id"`
    SelectedRiskTier string `json:"selected_risk_tier"`
    OverrideReason   string `json:"override_reason"`
}

type EscalationFact struct {
    EscalationID string `json:"escalation_id"` // production ULID（rev4）
    State        string `json:"state"`
    BlockScope   string `json:"block_scope"`
}

// FactsSnapshot：typed、非 map（spec §2）。presence 以 pointer 表達：
// nil＝該 fact 缺席；缺席的「語意」由對應 *Presence 欄位區分 not_applicable / missing。
type FactsSnapshot struct {
    SchemaVersion   int    `json:"schema_version"` // 本 spike 固定 1
    EvaluationPhase string `json:"evaluation_phase"` // "submit" | "decide"

    Decision *string   `json:"decision"`
    Reason   *string   `json:"reason"`
    Approver *Approver `json:"approver"`
    Entry    *Entry    `json:"entry"`
    Request  *Request  `json:"request"`

    Current         *Current         `json:"current"`
    CurrentPresence Presence         `json:"current_presence"` // known / not_applicable / missing
    BaseCommitState *string          `json:"base_commit_state"` // "ok" | "missing"（fact 值）
    BaseCommitPresence Presence      `json:"base_commit_presence"`
    Plan            *PlanFacts       `json:"plan"`
    PlanPresence    Presence         `json:"plan_presence"`
    RiskPolicy      *RiskPolicyFacts `json:"risk_policy"`
    RiskPolicyPresence Presence      `json:"risk_policy_presence"`

    RiskSelections []RiskSelection  `json:"risk_selections"`
    Escalations    []EscalationFact `json:"escalations"`
}

// DecodeFactsSnapshot：strict decode，未知欄位 fail loud（spec §2／出口 1）。
func DecodeFactsSnapshot(data []byte) (*FactsSnapshot, error)
```

- 一致性規則（實作於 Decode 後驗證，違反回 error）：`XPresence != Known` ⇔ 對應 pointer 為 nil；`EvaluationPhase` 僅允許 `"submit"`／`"decide"`；`SchemaVersion` 僅允許 1。

- [ ] **Step 1: Write the failing tests**

```go
package domainspec

import (
    "strings"
    "testing"
)

func validSnapshotJSON() string {
    return `{
      "schema_version": 1, "evaluation_phase": "decide",
      "decision": "approved", "reason": "", "approver": {"name":"u","email":"u@x"},
      "entry": {"exists":true,"has_request":true,"has_record":false},
      "request": {"gate":"gate2","subject":"plan:P1","bindings":[
        {"kind":"base_commit","role":"","ref":"HEAD","digest":"git:sha1:` + strings.Repeat("a", 40) + `"}]},
      "current": {"spec_manifest":"sha256:` + strings.Repeat("0", 64) + `","plan_manifest":"","risk_policy":"","permission_manifest":""},
      "current_presence": "known",
      "base_commit_state": "ok", "base_commit_presence": "known",
      "plan": {"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]},
      "plan_presence": "known",
      "risk_policy": {"default_tier":"low","rules":[{"match":{"contexts":["gate"],"modules":[]},"tier":"high"}]},
      "risk_policy_presence": "known",
      "risk_selections": [{"task_id":"T1","selected_risk_tier":"high","override_reason":""}],
      "escalations": []
    }`
}

func TestDecodeFactsSnapshotValid(t *testing.T) {
    s, err := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    if err != nil {
        t.Fatalf("decode valid: %v", err)
    }
    if s.EvaluationPhase != "decide" || s.Plan == nil || s.Plan.Tasks[0].SourceIndex != 0 {
        t.Fatalf("unexpected snapshot: %+v", s)
    }
}

func TestDecodeFactsSnapshotRejectsUnknownField(t *testing.T) {
    j := strings.Replace(validSnapshotJSON(), `"schema_version": 1`,
        `"schema_version": 1, "bogus_field": true`, 1)
    if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
        t.Fatal("unknown field must be rejected (spec 出口 1)")
    }
}

func TestDecodeFactsSnapshotPresenceConsistency(t *testing.T) {
    // plan_presence=known 但 plan=null → 不一致必須拒
    j := strings.Replace(validSnapshotJSON(),
        `"plan": {"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}`,
        `"plan": null`, 1)
    if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
        t.Fatal("presence=known with nil pointer must be rejected")
    }
}

func TestDecodeFactsSnapshotRejectsBadPhaseAndVersion(t *testing.T) {
    for _, bad := range []struct{ old, new string }{
        {`"evaluation_phase": "decide"`, `"evaluation_phase": "runtime"`},
        {`"schema_version": 1`, `"schema_version": 2`},
    } {
        j := strings.Replace(validSnapshotJSON(), bad.old, bad.new, 1)
        if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
            t.Fatalf("must reject %s", bad.new)
        }
    }
}
```

- [ ] **Step 2: Run to verify failure**——`go test ./internal/domainspec/ -run TestDecodeFactsSnapshot -v`，預期 compile error（型別未定義）。
- [ ] **Step 3: Implement**——`facts.go` 定義上述型別；`DecodeFactsSnapshot` 用 `json.NewDecoder(bytes.NewReader(data))` ＋ `dec.DisallowUnknownFields()`，decode 後跑一致性檢查（presence↔pointer、phase enum、schema_version==1；`Decision`／`Reason`／`Approver`／`Entry`／`Request` 於 submit phase 允許 nil（spec §2：submit corpus 的 decision facts 一律 not_applicable，以 nil 表達）——decide phase 則必須非 nil）。
- [ ] **Step 4: Run to verify pass**——同指令，全綠；`gofmt -l internal/domainspec/` 空輸出。
- [ ] **Step 5: Commit**——`feat(domainspec): typed FactsSnapshot＋strict decode（出口 1）`

---

### Task 2: Canonical 序列化＋sha256 digest（出口 7 前半）

**Files:**
- Create: `internal/domainspec/canonical.go`
- Test: `internal/domainspec/canonical_test.go`

**Interfaces:**
- Consumes: Task 1 全部型別。
- Produces:

```go
// CanonicalJSON：依 spec §2 canonical 規則正規化後輸出 deterministic JSON。
// 正規化（就地複本，不改輸入）：
//   bindings 依 (kind, role, ref, digest) 全序排序（重複不去重）；
//   risk_selections 依 (task_id, selected_risk_tier, override_reason)（重複不去重——R24 可觀測違規輸入）；
//   escalations 依 escalation_id；
//   plan.tasks 保留原始順序（source_index 具語意，不排序）；
//   risk_policy.rules 保留來源順序；
//   impact/match 的 contexts、modules 字典序排序後去重（rev6）；
//   nil 集合輸出 []（json tag 不足以達成——marshal 前把 nil slice 換成空 slice）。
// key 順序固定：encoding/json 對 struct 依欄位宣告序輸出，本身 deterministic。
func CanonicalJSON(s *FactsSnapshot) ([]byte, error)

// SnapshotDigest = "sha256:" + hex(sha256(CanonicalJSON))。
func SnapshotDigest(s *FactsSnapshot) (string, error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestCanonicalReorderAndDupSameDigest(t *testing.T) {
    // 出口 7（rev6）：contexts 重排＋含重複值 → 相同 digest
    a, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    b, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    b.Plan.Tasks[0].Impact.Contexts = []string{"gate", "gate", "audit"}
    a.Plan.Tasks[0].Impact.Contexts = []string{"audit", "gate"}
    da, _ := SnapshotDigest(a)
    db, _ := SnapshotDigest(b)
    if da != db {
        t.Fatalf("set-semantics fields must canonicalize: %s != %s", da, db)
    }
}

func TestCanonicalKeepsDuplicateSelections(t *testing.T) {
    // R24 違規輸入必須保留（不去重），且排序 deterministic
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    s.RiskSelections = []RiskSelection{
        {TaskID: "T1", SelectedRiskTier: "high"}, {TaskID: "T1", SelectedRiskTier: "high"},
    }
    j, _ := CanonicalJSON(s)
    if got := strings.Count(string(j), `"task_id":"T1"`); got != 2 {
        t.Fatalf("duplicate selections must survive canonicalization, got %d", got)
    }
}

func TestCanonicalTasksKeepSourceOrder(t *testing.T) {
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    s.Plan.Tasks = []PlanTask{
        {ID: "T9", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "low"},
        {ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low"},
    }
    j, _ := CanonicalJSON(s)
    if strings.Index(string(j), `"id":"T9"`) > strings.Index(string(j), `"id":"T1"`) {
        t.Fatal("plan.tasks must keep source order, not id order")
    }
}

func TestCanonicalNilAndEmptySliceEqual(t *testing.T) {
    a, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    b, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    a.Escalations = nil
    b.Escalations = []EscalationFact{}
    da, _ := SnapshotDigest(a)
    db, _ := SnapshotDigest(b)
    if da != db {
        t.Fatal("nil and empty slices must produce identical canonical form")
    }
}
```

- [ ] **Step 2: Run to verify failure**——`go test ./internal/domainspec/ -run TestCanonical -v`。
- [ ] **Step 3: Implement**——deep-copy snapshot → 正規化（`sort.Slice` 各集合；contexts/modules `sort.Strings` 後 `slices.Compact`；nil slice → `[]T{}`）→ `json.Marshal`；digest 用 `crypto/sha256`。
- [ ] **Step 4: Run to verify pass**＋gofmt。
- [ ] **Step 5: Commit**——`feat(domainspec): canonical JSON＋sha256 digest（出口 7 排序／去重／tie-break）`

---

### Task 3: Bundle 型別＋YAML strict 載入驗證（出口 2）

**Files:**
- Create: `internal/domainspec/bundle.go`
- Test: `internal/domainspec/bundle_test.go`
- Modify: `go.mod`／`go.sum`（`go get github.com/google/cel-go@latest`）

**Interfaces:**
- Consumes: 無（獨立於 facts；CEL env 的變數宣告在本 task 凍結）。
- Produces:

```go
type Rule struct {
    ID        string   `yaml:"id"`
    Phase     string   `yaml:"phase"`      // "submit" | "decide"（單值，rev4）
    When      string   `yaml:"when"`       // CEL 布林式
    Effect    string   `yaml:"effect"`     // "deny" | "allow"
    Target    string   `yaml:"target"`     // "decision.eligibility" | "risk.task"（risk.task 逐 task 實體化為 risk.T<id>）
    DependsOn []string `yaml:"depends_on"` // 限同 phase（rev5）
    Priority  int      `yaml:"priority"`
    Verdict   string   `yaml:"verdict"`
    Refs      string   `yaml:"refs"`
    PerTask   bool     `yaml:"per_task"`   // true：對 plan.tasks 逐 task 評估（綁 task/sel 變數）
    // 四層 primary precedence 的靜態 rank（spec §4；Task 6 使用）：
    StepRank  int    `yaml:"step_rank"`  // gate step：submit 規則 < decide 規則，decide 內依 PrepareDecision 檢查序
    Stage     string `yaml:"stage"`      // "none" | "pre_loop" | "task_loop" | "post_loop"
    CheckRank int    `yaml:"check_rank"` // task 內檢查序（僅 stage=task_loop 有意義）
}

type Bundle struct {
    SchemaVersion int    `yaml:"schema_version"`
    Rules         []Rule `yaml:"rules"`
}

type CompiledRule struct {
    Rule
    Program cel.Program
    // 從 checked AST 抽出的頂層 fact 變數引用集（missing-leaf 判定用，載入時決定、非 runtime 追蹤）
    RefVars map[string]bool
}

type CompiledBundle struct {
    Digest string // "sha256:" + hex(sha256(canonical YAML→JSON))
    Rules  []CompiledRule          // 依 bundle 檔內順序
    ByID   map[string]*CompiledRule
}

// LoadBundle：strict YAML decode（KnownFields）→ 逐條驗證 → CEL compile/type-check →
// static cost estimate 超限拒收 → depends_on 同 phase＋SCC 無環 → RefVars 抽取 → digest。
func LoadBundle(yamlSrc []byte, staticCostLimit uint64) (*CompiledBundle, error)

// celEnv：凍結的變數宣告（全 bundle 共用）。
//   evaluation_phase: string; decision: string; reason: string
//   approver: map(string, string); entry: map(string, bool)
//   request: map(string, dyn)          // gate/subject/bindings
//   current: map(string, string); base_commit_state: string
//   plan: map(string, dyn); risk_policy: map(string, dyn)
//   risk_selections: list(map(string, string)); escalations: list(map(string, string))
//   presence: map(string, string)      // "current"/"base_commit"/"plan"/"risk_policy" → 三態
//   task: map(string, dyn); sel: map(string, dyn)   // per_task 規則專用（sel 可為 null）
//   tier_order: map(string, int)                    // bundle 常數（host 注入）
// 純函式 extension：tier_rank(string) -> int（未知 tier 回 -1；spec §3 host extension 限純函式）
func celEnv() (*cel.Env, error)
```

- [ ] **Step 1: `go get github.com/google/cel-go@latest && go mod tidy`**，commit dependency bump 獨立一筆：`build: add cel-go dependency（DomainSpec spike）`。
- [ ] **Step 2: Write the failing tests**

```go
func miniBundle(extra string) []byte {
    return []byte(`schema_version: 1
rules:
  - id: RA
    phase: decide
    when: "decision == 'approved'"
    effect: deny
    target: decision.eligibility
    priority: 10
    verdict: "RA fired"
    refs: "test"
    step_rank: 1
    stage: none
` + extra)
}

func TestLoadBundleOK(t *testing.T) {
    b, err := LoadBundle(miniBundle(""), 1_000_000)
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if !b.Rules[0].RefVars["decision"] {
        t.Fatalf("RefVars must capture referenced fact vars: %+v", b.Rules[0].RefVars)
    }
    if b.Digest == "" || !strings.HasPrefix(b.Digest, "sha256:") {
        t.Fatal("bundle digest required")
    }
}

func TestLoadBundleRejectsTypeError(t *testing.T) {
    bad := bytes.Replace(miniBundle(""), []byte(`decision == 'approved'`), []byte(`decision + 1 == 2`), 1)
    if _, err := LoadBundle(bad, 1_000_000); err == nil {
        t.Fatal("type-check must reject string+int（出口 2）")
    }
}

func TestLoadBundleRejectsStaticCostOverLimit(t *testing.T) {
    if _, err := LoadBundle(miniBundle(""), 0); err == nil {
        t.Fatal("static cost over limit must be rejected（出口 2）")
    }
}

func TestLoadBundleRejectsCrossPhaseDependsOn(t *testing.T) {
    extra := `  - id: RB
    phase: submit
    when: "request.gate == 'gate2'"
    effect: deny
    target: decision.eligibility
    depends_on: [RA]
    priority: 10
    verdict: "RB"
    refs: "test"
    step_rank: 0
    stage: none
`
    if _, err := LoadBundle(miniBundle(extra), 1_000_000); err == nil {
        t.Fatal("cross-phase depends_on must be rejected at load（rev5／出口 2）")
    }
}

func TestLoadBundleRejectsCycle(t *testing.T) {
    extra := `  - id: RC
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [RD]
    priority: 10
    verdict: "RC"
    refs: "test"
    step_rank: 1
    stage: none
  - id: RD
    phase: decide
    when: "true"
    effect: deny
    target: decision.eligibility
    depends_on: [RC]
    priority: 10
    verdict: "RD"
    refs: "test"
    step_rank: 1
    stage: none
`
    if _, err := LoadBundle(miniBundle(extra), 1_000_000); err == nil {
        t.Fatal("dependency cycle must be rejected")
    }
}

func TestLoadBundleRejectsUnknownYAMLField(t *testing.T) {
    bad := bytes.Replace(miniBundle(""), []byte("verdict:"), []byte("bogus: x\n    verdict:"), 1)
    if _, err := LoadBundle(bad, 1_000_000); err == nil {
        t.Fatal("unknown YAML field must be rejected")
    }
}
```

- [ ] **Step 3: Run to verify failure**——`go test ./internal/domainspec/ -run TestLoadBundle -v`。
- [ ] **Step 4: Implement**——
  - strict YAML：`yaml.NewDecoder` ＋ `dec.KnownFields(true)`。
  - 逐條驗證 enum（phase／effect／target／stage）、id 唯一、depends_on 目標存在**且同 phase**。
  - CEL：`env.Compile(r.When)` → `iss.Err()` 拒收；輸出型別必須 `cel.BoolType`。
  - static cost：`env.EstimateCost(ast, estimator)`（estimator 為固定 size hints 的最小實作，list 長度 hint 64）；`est.Max > staticCostLimit` → 拒收。
  - SCC／無環：depends_on 圖上做 DFS 三色標記，back edge 即拒收。
  - RefVars：走訪 checked AST（`ast.NativeRep().Expr()`）收集頂層 ident（宣告過的變數名）。
  - digest：bundle YAML strict decode 後轉 canonical JSON（rules 依檔內順序、欄位宣告序）取 sha256。
  - `Program`：`env.Program(ast, cel.CostLimit(runtimeLimit), cel.CostTracking(...))` 延後到 Task 4 evaluator（Program 需 runtime limit 參數）——本 task 先存 `Ast`，`CompiledRule` 增私有欄位 `ast *cel.Ast`。
- [ ] **Step 5: Run to verify pass**＋gofmt。
- [ ] **Step 6: Commit**——`feat(domainspec): rule bundle strict 載入——CEL compile／static cost／同 phase depends_on／SCC（出口 2）`

---

### Task 4: 兩階段聚合 evaluator＋reason graph（出口 3、4）

**Files:**
- Create: `internal/domainspec/eval.go`
- Test: `internal/domainspec/eval_test.go`

**Interfaces:**
- Consumes: Task 1 `FactsSnapshot`、Task 3 `CompiledBundle`（含 `ast`）。
- Produces:

```go
type Truth string  // "true" | "false" | "unknown" | "conflict"
type Status string // "ok" | "evaluation_error"

type Violation struct {
    RuleID      string
    Target      string // 實體化後（risk.T<id>）
    SourceIndex int    // per_task 規則綁定的 task source_index；非 per_task 為 -1
    Verdict     string
}

type ReasonEntry struct {
    RuleID   string
    Target   string
    Outcome  string // "matched" | "not_matched" | "not_eligible" | "unknown" | "error"
    Cause    string // not_eligible：哪個 dependency＋false/unknown/not_eligible 哪一種（rev6）
}

type Result struct {
    Truth             Truth
    Status            Status
    UnknownLeaves     []string    // 排序後輸出（determinism）
    MatchedRuleIDs    []string    // 排序後輸出
    ConflictingRuleIDs []string
    Violations        []Violation // 完整列表（explain＋Task 6 primary 選擇的輸入）
    ReasonGraph       []ReasonEntry // 依 bundle 規則序＋task source_index，deterministic
}

// Evaluate：spec §3 兩階段聚合。
//   phase 過濾：只評估 rule.Phase == snapshot.EvaluationPhase 的規則。
//   per_task 規則對 plan.tasks 逐 task 實體化（activation 增 task/sel；sel 由 task_id
//   對 risk_selections 首筆配對，無則 null）；plan missing/not_applicable 時 per_task
//   規則整組 not eligible（cause 記 "plan facts absent"）。
//   missing-leaf 判定：rule.RefVars ∩ missingVars(snapshot) ≠ ∅ → when=unknown（不執行 CEL）。
//   eligibility 傳遞封閉（rev6）：eligible ⇔ 所有 dependency eligible 且 when=true；
//   dependency false/unknown/not eligible → 下游 not eligible（when 不評估）。
//   runtime cost：Program 帶 cel.CostLimit(runtimeCostLimit)；eval error → status=evaluation_error
//   （該規則記 ReasonEntry outcome="error"），truth 不受污染——error 不併入 unknown。
//   全域 truth：任一 target conflict → conflict；任一 target 有效 deny → false；
//   任一被評估規則 unknown → unknown；否則 true。
func Evaluate(b *CompiledBundle, s *FactsSnapshot, runtimeCostLimit uint64) (*Result, error)
```

- [ ] **Step 1: Write the failing tests**（出口 3 的四條＋出口 4）

```go
// fixture bundle：A(when=false)→B(depends_on A)→C(depends_on B) 三層 DAG（rev6）
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
    step_rank: 1
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
    step_rank: 1
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
    step_rank: 1
    stage: none
`

func TestEvaluateNotEligibleTransitive(t *testing.T) {
    b, _ := LoadBundle([]byte(dagBundle), 1_000_000)
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    r, err := Evaluate(b, s, 100_000)
    if err != nil {
        t.Fatal(err)
    }
    // A=false ⇒ B not eligible ⇒ C not eligible；三者皆不產生 deny ⇒ truth=true
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

func TestEvaluateUnknownFromMissingLeaf(t *testing.T) {
    b, _ := LoadBundle(miniBundle(""), 1_000_000) // RA references `decision`
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    s.Decision = nil // decide phase 本應 known 而缺 → missing
    s.Reason = nil
    // （Decode 一致性只驗 presence 欄位對；Decision 缺席在 decide 屬 missing——
    //   fixture 直接改 struct 模擬 host 給出的 missing 狀態）
    r, _ := Evaluate(b, s, 100_000)
    if r.Truth != "unknown" || r.Status != "ok" {
        t.Fatalf("missing leaf must yield unknown, got %s/%s", r.Truth, r.Status)
    }
    if len(r.UnknownLeaves) == 0 {
        t.Fatal("unknown_leaves must name the missing fact")
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
    step_rank: 1
    stage: none
`
    b, _ := LoadBundle(miniBundle(conflictExtra), 1_000_000) // RA deny＋RALLOW allow，同 target 同 priority
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    r, _ := Evaluate(b, s, 100_000)
    if r.Truth != "conflict" || r.Status != "ok" {
        t.Fatalf("same-priority opposing effects must conflict, got %s/%s", r.Truth, r.Status)
    }
    if len(r.ConflictingRuleIDs) != 2 {
        t.Fatalf("conflicting ids: %v", r.ConflictingRuleIDs)
    }
}

func TestEvaluateRuntimeCostLimitIsError(t *testing.T) {
    b, _ := LoadBundle(miniBundle(""), 1_000_000)
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    r, _ := Evaluate(b, s, 0) // runtime limit 0 → 必爆
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
    s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    r1, _ := Evaluate(b, s, 100_000)
    r2, _ := Evaluate(b, s, 100_000)
    j1, _ := json.Marshal(r1)
    j2, _ := json.Marshal(r2)
    if !bytes.Equal(j1, j2) {
        t.Fatal("reason graph must be byte-identical across runs")
    }
}
```

- [ ] **Step 2: Run to verify failure**。
- [ ] **Step 3: Implement**——
  - activation 建構：`FactsSnapshot` → `map[string]any`（canonical 正規化後的複本，保證 map 內容 deterministic；CEL map/list 轉 `[]any`／`map[string]any`）；`presence` map；`missingVars`：`XPresence == Missing` 的群組名＋decide phase 下 nil 的 decision facts。
  - 拓撲序：載入時已驗無環；評估依 depends_on 拓撲排序（同層依 bundle 檔內順序，deterministic）。
  - per_task 展開：`for _, task := range s.Plan.Tasks`，activation 疊加 `task`／`sel`；Violation.SourceIndex = task.SourceIndex。
  - per-target 裁決：收集 eligible＋when=true 的規則 → 依 target 分組 → 最高 priority 的 effect；同最高 priority 出現 allow＋deny → conflict。
  - 全域 truth 依序：conflict → deny(false) → unknown → true。
  - Program 快取在 CompiledRule（首次 Evaluate 以 runtimeCostLimit 建立；limit 不同重建——spike 接受）。
- [ ] **Step 4: Run to verify pass**＋gofmt；`go test ./internal/domainspec/ -count=2`（快取路徑也 deterministic）。
- [ ] **Step 5: Commit**——`feat(domainspec): 兩階段聚合 evaluator——傳遞封閉 eligibility＋conflict＋runtime cost（出口 3、4）`

---

### Task 5: gate2-bundle.yaml——in-scope 規則全數落 CEL

**Files:**
- Create: `internal/domainspec/testdata/gate2-bundle.yaml`
- Test: `internal/domainspec/gate2_bundle_test.go`

**Interfaces:**
- Consumes: Task 3 `LoadBundle`、Task 4 `Evaluate`。
- Produces: 正式 bundle。規則清單（id／phase／stage／check_rank；refs 實作時逐條驗 file:line）：

| id | phase | target | stage | check_rank | 語意（when 要點） |
|---|---|---|---|---|---|
| R5.submit | submit | decision.eligibility | none | - | `!(request.gate in ["gate1","gate2","test_contract_approval"])`（service.go:46-48） |
| R6.submit | submit | decision.eligibility | none | - | gate2 且 `!subject.startsWith("plan:")` 或 id 空（gate2.go:92-95） |
| R7 | submit | decision.eligibility | none | - | bindings (kind,role) 重複（gate2.go:284-291） |
| R8 | submit | decision.eligibility | none | - | 必備 5 kind 缺一或 role != ""（gate2.go:292-305） |
| R9 | submit | decision.eligibility | none | - | digest 格式不符 regex（gate2.go:296-299；CEL `matches`） |
| R1 | decide | decision.eligibility | none | - | `!(decision in ["approved","rejected"])`（service.go:86-88） |
| R2 | decide | decision.eligibility | none | - | rejected 且 reason==""（service.go:89-91） |
| R3 | decide | decision.eligibility | none | - | approver.name=="" && approver.email==""（app.go:5814-5820） |
| R4 | decide | decision.eligibility | none | - | `!entry.exists \|\| !entry.has_request \|\| entry.has_record`（service.go:98-101） |
| R5.decide | decide | decision.eligibility | none | - | 同 R5.submit 式（service.go:103-105；**無** approved guard） |
| R6.decide | decide | decision.eligibility | none | - | `decision=="approved" &&` subject 形狀違規（gate2.go:124-127） |
| R11 | decide | decision.eligibility | none | - | approved 且任一 manifest bound digest != "" 且 != current 現值（gate2.go:215-232；沿 R10 guard） |
| R12 | decide | decision.eligibility | none | - | approved 且 base_commit_state=="missing"（gate2.go:234-248） |
| R16 | decide | decision.eligibility | none | - | escalations 存在 state!="resolved" 且 block_scope!="" 且（=="workspace" 或 == 導出 scope）；scope 導出**內嵌**（R15 豁免記載）：gate1→"workspace"、gate2+plan:X→"gate2:"+X、未知→"workspace"（app.go:5895-5912；project.go:81-96——**忽略 hard 旗標**） |
| R21 | decide | decision.eligibility | none | - | rejected 且 size(risk_selections)>0（gate2.go:114-118） |
| R24 | decide | decision.eligibility | pre_loop | - | approved 且 risk_selections 依 task_id 有重複（gate2.go:134-140） |
| R25 | decide | risk.task | task_loop | 0 | per_task：`sel == null`（gate2.go:144-147） |
| R27 | decide | risk.task | task_loop | 1 | per_task：CEL 重算 minimum（risk_policy.rules 交集 match→tier_rank max，無命中→default）!= task.minimum_risk_tier（gate2.go:150-153；riskpolicy.go:4-41） |
| R28.minimum | decide | risk.task | task_loop | 2 | per_task：tier_rank(task.minimum_risk_tier) < 0（gate2.go:154-157） |
| R28.planner | decide | risk.task | task_loop | 3 | per_task：tier_rank(task.planner_risk_tier) < 0（gate2.go:158-161） |
| R29 | decide | risk.task | task_loop | 4 | per_task：planner < minimum（gate2.go:162-164） |
| R28.selected | decide | risk.task | task_loop | 5 | per_task：sel != null 且 tier_rank(sel.selected_risk_tier) < 0（gate2.go:165-168） |
| R30 | decide | risk.task | task_loop | 6 | per_task：selected < minimum（gate2.go:169-171） |
| R31 | decide | risk.task | task_loop | 7 | per_task：selected < planner 且 override_reason==""（gate2.go:172-174） |
| R26 | decide | decision.eligibility | post_loop | - | approved 且存在 selection 的 task_id 不在 plan.tasks（gate2.go:183-190） |

  - step_rank 凍結：submit 全部 0；decide 依 PrepareDecision 檢查序 R1=1、R2=2、R4=3、R5.decide=4、R11=5、R12=6、R3=7、R16=8（gateDecide 步驟 2 的 approver 與步驟 8-9 的 blocking 依 app.go 判定序）、R21=9、R6.decide=9、R24/R25…/R26=10（build stage 層內再分）。實作時對照 gateDecide 十一步＋service.go 實序驗一次，發現不符以 production 為準修 bundle 並在 commit message 記錄。
  - risk 規則（R25 起）guard 皆含 `decision == "approved"`（rejected 早退，gate2.go:114-119）。
  - R27 的 CEL 重算不用 host 函式做交集——用 `exists` 雙層 comprehension（mapping mutation 可測）；tier 比較用 `tier_rank`。

- [ ] **Step 1: Write the failing test**——表驅動隔離 fixture：每條規則一組（違規 snapshot → truth=false 且 Violations 含該 rule id；乾淨 snapshot → truth=true）。fixture 由 `validSnapshotJSON()` 複本逐條變異（例：R25 拿掉 selection；R30 selected 降到 minimum 之下；R24 疊重複 selection；R16 加 blocking escalation——含一筆 `hard` 語意案例：state=open 的 escalation 一律擋，不看 hard；R11 改 current digest）。

```go
func TestGate2BundleIsolatedRuleCoverage(t *testing.T) {
    src, _ := os.ReadFile("testdata/gate2-bundle.yaml")
    b, err := LoadBundle(src, 5_000_000)
    if err != nil {
        t.Fatalf("gate2 bundle must load: %v", err)
    }
    for _, tc := range gate2IsolatedCases() { // []struct{ RuleID string; Mutate func(*FactsSnapshot) }
        t.Run(tc.RuleID, func(t *testing.T) {
            s, _ := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
            tc.Mutate(s)
            r, _ := Evaluate(b, s, 500_000)
            if r.Truth != "false" {
                t.Fatalf("isolated violation must deny, got %s", r.Truth)
            }
            found := false
            for _, v := range r.Violations {
                if v.RuleID == tc.RuleID {
                    found = true
                }
            }
            if !found {
                t.Fatalf("violations %v must contain %s", r.Violations, tc.RuleID)
            }
        })
    }
}
```

- [ ] **Step 2: Run to verify failure**（bundle 檔不存在）。
- [ ] **Step 3: Implement**——依上表逐條寫 YAML；submit fixture 的 snapshot `evaluation_phase="submit"`、decision facts nil。R9 regex 直接沿 gate2.go:28-31 兩條 pattern。
- [ ] **Step 4: Run to verify pass**＋gofmt；`go vet ./internal/domainspec/`。
- [ ] **Step 5: Commit**——`feat(domainspec): gate2 rule bundle——in-scope 規則落 CEL＋隔離 fixture 全覆蓋`

---

### Task 6: Primary precedence selector＋比對契約型別（出口 5 契約）

**Files:**
- Create: `internal/domainspec/compare.go`
- Test: `internal/domainspec/compare_test.go`

**Interfaces:**
- Consumes: Task 4 `Result`（Violations）、Task 3 `Rule` 的 StepRank／Stage／CheckRank。
- Produces:

```go
type Outcome string // "pass" | "blocked"（spec §4——駁回成功也是 pass）

// GoVerdict：oracle 可觀測結果。
type GoVerdict struct {
    Outcome       Outcome        `json:"outcome"`
    PrimaryRuleID string         `json:"primary_rule_id"` // blocked 時必填（訊息形狀→rule id 由 harness 對映）
    RiskDecisions []RiskSelection `json:"risk_decisions"` // pass＋approved 時的決定性輸出（含 minimum/planner，比對用扁平鍵）
}

var stageRank = map[string]int{"none": 0, "pre_loop": 1, "task_loop": 2, "post_loop": 3}

// PrimaryViolation：四層 precedence（spec §4 rev4）——
// (step_rank, stageRank[stage], source_index(-1 視為 0), check_rank) 字典序最小者。
func PrimaryViolation(b *CompiledBundle, r *Result) (Violation, bool)

// CompareCase：CEL Result vs GoVerdict。
//   go=blocked：CEL truth 必須 false 且 PrimaryViolation.RuleID == go.PrimaryRuleID。
//   go=pass：CEL truth 必須 true（unknown/conflict/error 皆記不一致）。
//   status=evaluation_error → 一律不一致（error 不併入 unknown）。
func CompareCase(b *CompiledBundle, r *Result, gv GoVerdict) (ok bool, detail string)
```

- [ ] **Step 1: Write the failing tests**——直接以合成 Violations 驗四層排序（不跑 CEL）：

```go
func TestPrimaryPrecedenceFourLayers(t *testing.T) {
    b := bundleWithRanks(t) // helper：載入 gate2-bundle.yaml
    cases := []struct {
        name string
        vs   []Violation
        want string
    }{
        // spec §4 coverage 三筆逐層案例的 selector 版：
        {"pre-loop beats task-loop", []Violation{
            {RuleID: "R30", SourceIndex: 0}, {RuleID: "R24", SourceIndex: -1}}, "R24"},
        {"source_index beats rule number", []Violation{
            {RuleID: "R25", SourceIndex: 1}, {RuleID: "R30", SourceIndex: 0}}, "R30"},
        {"task-loop beats post-loop", []Violation{
            {RuleID: "R26", SourceIndex: -1}, {RuleID: "R31", SourceIndex: 2}}, "R31"},
        // 追加：submit 先於 decide；in-task check rank（同 task R29 先於 R30）
        {"submit beats decide", []Violation{
            {RuleID: "R1", SourceIndex: -1}, {RuleID: "R7", SourceIndex: -1}}, "R7"},
        {"in-task check rank", []Violation{
            {RuleID: "R30", SourceIndex: 0}, {RuleID: "R29", SourceIndex: 0}}, "R29"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            v, ok := PrimaryViolation(b, &Result{Violations: tc.vs})
            if !ok || v.RuleID != tc.want {
                t.Fatalf("want %s got %+v", tc.want, v)
            }
        })
    }
}

func TestCompareCaseContract(t *testing.T) {
    b := bundleWithRanks(t)
    blocked := &Result{Truth: "false", Status: "ok",
        Violations: []Violation{{RuleID: "R24", SourceIndex: -1}}}
    if ok, _ := CompareCase(b, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R24"}); !ok {
        t.Fatal("matching primary must compare equal")
    }
    if ok, _ := CompareCase(b, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R30"}); ok {
        t.Fatal("primary mismatch must be reported")
    }
    errRes := &Result{Truth: "true", Status: "evaluation_error"}
    if ok, _ := CompareCase(b, errRes, GoVerdict{Outcome: "pass"}); ok {
        t.Fatal("evaluation_error must never compare equal")
    }
}
```

- [ ] **Step 2: Run to verify failure**；**Step 3: Implement**（`sort.Slice` 單鍵組比較）；**Step 4: pass＋gofmt**。
- [ ] **Step 5: Commit**——`feat(domainspec): 四層 primary precedence selector＋比對契約（出口 5 契約面）`

---

### Task 7: Corpus manifest＋Go oracle harness＋coverage 報告（出口 5 主體）

**Files:**
- Create: `internal/domainspec/corpus.go`（manifest 型別＋重放）
- Create: `internal/domainspec/corpus_oracle_test.go`（oracle adapters——**test 檔**，准許 import `internal/gatepolicy`／`internal/gate`／`internal/escalation`；domainspec 本體維持零依賴）
- Create: `internal/domainspec/testdata/corpus/*.json`（案例檔）
- Test: `internal/domainspec/corpus_test.go`

**Interfaces:**
- Consumes: Task 1–6 全部。
- Produces:

```go
// CorpusCase：tagged union（spec §4 rev3）。Kind 決定有效欄位。
type CorpusCase struct {
    Name            string        `json:"name"`
    Kind            string        `json:"kind"` // "evaluated" | "acquisition_failed"
    EvaluationPhase string        `json:"evaluation_phase"`
    OracleSource    string        `json:"oracle_source"` // "gatepolicy" | "gate_service" | "escalation" | "a9_workspace" | "host_boundary"
    Snapshot        *FactsSnapshot `json:"snapshot"`     // evaluated 專用
    GoVerdict       *GoVerdict    `json:"go_verdict"`    // evaluated 專用
    Reason          string        `json:"reason"`        // acquisition_failed 專用
    CoversRules     []string      `json:"covers_rules"`  // coverage 歸屬（隔離案例單一 id；precedence 案例多 id）
}

// ReplayCorpus：逐案例 Evaluate＋CompareCase；回報告。
type CoverageReport struct {
    Consistent, Inconsistent, Exempt int
    Mismatches   []string            // case name＋detail
    CoveredRules map[string]int      // 隔離案例計數（per phase-specific id）
    UncoveredRules []string          // in-scope 清單減 covered；必須空或對應豁免表
}
func ReplayCorpus(b *CompiledBundle, cases []CorpusCase, runtimeCostLimit uint64) (*CoverageReport, error)
```

- oracle adapter（test 檔內）：每筆 evaluated 案例的 `GoVerdict` 由**實際呼叫 production 函式**產生後固化進 JSON——`gatepolicy.NewGate2Policy(stubLoader, stubGit, …).ValidateRequest/BuildDecision`（stub 沿 `internal/gatepolicy/gate2_test.go` 既有測試 double 形狀）＋`gate.Service` 對 R1–R5 面；escalation 面直接呼叫 `escalation.BlockingFor`。訊息形狀→rule id 的對映表寫在 test 檔（`errShapeToRule map[string]string`，以 `strings.Contains` 判定；一筆錯誤命中多 pattern 即測試失敗——對映必須唯一）。harness guard：**oracle 重跑**——JSON 固化值與當場重呼 production 的結果不一致即紅（出口 6b 的 guard 之一）。
- 案例集（spec §4 五類來源；(d) 留 Task 8）：
  - 每條 in-scope 規則一筆隔離違規案例＋共用乾淨 pass 案例（submit／decide 各一）。
  - 三筆 precedence 案例（同 Task 6 表：R24+R30、R30@0+R25@1、R31+R26）——oracle 端驗 production 首錯即回的實際錯誤對映到預期 primary。
  - 對齊警訊各一：R16 hard 旗標案例（hard=true 仍擋）；R11 的 `Role != ""` binding 不參與 digest 匹配案例（bindingDigest 只取 Role==""，gate2.go:261-268）。
  - `acquisition_failed` 兩筆：LoadAt 錯、rev-parse fatal（Exempt 計數，不入一致性統計）。
  - dirty-tree host boundary 一筆（submit phase、`oracle_source: "host_boundary"`、不計 CEL 一致性——Exempt）。

- [ ] **Step 1: Write the failing tests**——`TestReplayCorpusAllConsistent`（Mismatches 必須空、Inconsistent==0）；`TestCoverageComplete`（UncoveredRules 逐一比對豁免表 `exemptRules = map[string]string{"R15": "...", "R32": "...", …}`，非豁免即紅）；`TestOracleRefresh`（固化 GoVerdict 與重呼 production 一致）。
- [ ] **Step 2: Run to verify failure**；**Step 3: Implement**（corpus.go＋案例 JSON＋oracle adapter）；**Step 4: pass**——`go test ./internal/domainspec/ -count=1` 全綠＋gofmt。
- [ ] **Step 5: Commit**——`feat(domainspec): corpus manifest＋oracle harness＋coverage 報告（出口 5 主體）`

---

### Task 8: A9 真實案例＋baseline／candidate bundle diff（出口 5 收尾）

**Files:**
- Create: `internal/domainspec/testdata/corpus/a9-*.json`（來源 (d)）
- Create: `internal/domainspec/bundlediff.go`＋`bundlediff_test.go`

**Interfaces:**
- Consumes: Task 7 `ReplayCorpus`、`CorpusCase`。
- Produces:

```go
// DiffReport：同一 corpus 對 baseline/candidate 兩個 bundle 重放的翻轉表（spec §5 出口 5）。
type FlipRow struct {
    CaseName            string
    BaselineOutcome, CandidateOutcome string // truth
    BaselineUnknown, CandidateUnknown []string
    BaselineStatus, CandidateStatus   Status
}
// 只列有翻轉的案例；三欄（outcome/unknown/error）任一變即列。
func DiffBundles(baseline, candidate *CompiledBundle, cases []CorpusCase, limit uint64) ([]FlipRow, error)
```

- A9 案例做法：從 `~/playground/wb-accept-a9g2` 驗收 workspace 的三個 git commit＋`gate.jsonl`（2 筆 approval、risk_decisions＋override_reason）＋`escalation.jsonl`（stale auto＋superseded）**離線抽取** facts（current digest 依 manifest 記錄的 commit OID 重算補齊——一次性腳本抽取後固化為 JSON，不引入 runtime git 依賴；抽取步驟記錄在案例 JSON 的 `name`／註解欄）。至少三筆：approved＋override（pass）、stale 攔截（blocked，R11）、superseded 後 rejected（pass）。workspace 不存在或 journal 缺 → 該來源標 `acquisition_failed` 豁免並在收斂報告記載，**不阻塞其他出口**。
- [ ] **Step 1: failing test**——`TestDiffBundlesDetectsFlip`：candidate = baseline YAML 拿掉 R31 的 override 檢查（`strings.Replace` when 式）→ FlipRow 必含 R31 隔離案例；baseline==candidate → 空表。
- [ ] **Step 2: fail**；**Step 3: Implement**；**Step 4: pass＋gofmt**。
- [ ] **Step 5: Commit**——`feat(domainspec): A9 真實案例＋bundle diff 翻轉表（出口 5 收尾）`

---

### Task 9: Mutation 鑑別力＋不接管佐證＋GO／NO-GO 收斂報告（出口 6、8）

**Files:**
- Create: `internal/domainspec/mutation_test.go`
- Create: `docs/superpowers/specs/2026-08-26-domainspec-spike-results.md`（收斂報告）

**Interfaces:**
- Consumes: Task 5 bundle、Task 7 harness、Task 8 diff。
- Produces: 八項出口逐項證據＋GO／NO-GO 判定。

- [ ] **Step 1: Write mutation tests（出口 6）**

```go
func TestMutationBundleRuleFlipsCaught(t *testing.T) {
    // (a) 改一條 bundle 規則（R31 拿掉 override 檢查）→ DiffBundles 必抓翻轉
    src, _ := os.ReadFile("testdata/gate2-bundle.yaml")
    mutated := bytes.Replace(src,
        []byte(`sel.override_reason == ''`), []byte(`false`), 1) // R31 when 的 override 分量
    base, _ := LoadBundle(src, 5_000_000)
    cand, err := LoadBundle(mutated, 5_000_000)
    if err != nil {
        t.Fatalf("mutated bundle must still load: %v", err)
    }
    rows, _ := DiffBundles(base, cand, loadCorpus(t), 500_000)
    if len(rows) == 0 {
        t.Fatal("R31 mutation must flip at least its isolated case（出口 6a）")
    }
}
// (b) 「移除比對 harness 的 guard → 對應測試紅」以雙證據交付：
//   1) TestOracleRefresh（Task 7）本身即 guard——手動實驗（收斂報告記錄實測輸出）：
//      註解掉 oracle 重呼再跑，TestReplayCorpusAllConsistent 對固化值仍綠但
//      TestOracleRefresh 紅——證明 guard 承擔鑑別。
//   2) TestCompareCaseContract 的 error/mismatch 分支（Task 6）作為程式化保證。
```

- [ ] **Step 2: 驗全套**——`go test ./... -count=1`（全 repo，證明無 production 波及）＋`gofmt -l .` 空。
- [ ] **Step 3: 不接管佐證（出口 8）**——`git diff <spike 起點 commit>..HEAD --stat -- . ':(exclude)internal/domainspec' ':(exclude)docs' ':(exclude)go.mod' ':(exclude)go.sum'` 輸出必須空（go.mod/go.sum 只有 cel-go 增項，diff 內容貼進報告）；`gateDecide` 所在 app.go 零改動。
- [ ] **Step 4: 收斂報告**——逐項列八個出口條件的證據（測試名＋輸出摘要）、coverage 表（per phase-specific id）、豁免表（R15／R32／host 層項、acquisition_failed 案例）、剩餘風險（step_rank 對 production 判定序的人工凍結、A9 來源可用性）、GO／NO-GO 判定與後續建議（M4.5 scope 擴充事項）。報告措辭遵守證據鏈慣例：只寫實測結果，未驗證項明標。
- [ ] **Step 5: Commit**——`docs(domainspec): spike 收斂報告——八項出口逐項證據＋GO/NO-GO`

---

## Self-Review（已跑）

1. **Spec coverage**：出口 1→Task 1；出口 2→Task 3；出口 3、4→Task 4（三層 DAG、conflict、error、determinism）；出口 5→Task 5–8（隔離 coverage、precedence 三筆、corpus 五來源、diff 翻轉表）；出口 6→Task 9＋Task 7 guard；出口 7→Task 2＋Task 3 digest；出口 8→Task 9。§1 對齊警訊兩筆→Task 7 案例。§2 phase-aware／canonical 全部→Task 1、2。§3 代數與聚合→Task 3、4。§4 比較契約→Task 6、7。
2. **Placeholder scan**：無 TBD／「適當處理」；R27 CEL 式、step_rank 表為實作時需對照 production 再驗的**明標事項**（非 placeholder，是 spec 附錄 A 的既定慣例）。
3. **Type consistency**：`FactsSnapshot`／`CompiledBundle`／`Result`／`Violation`／`GoVerdict`／`CorpusCase` 簽章在 Consumes/Produces 逐 task 對齊；`RiskSelection` 於 GoVerdict 重用 Task 1 型別。
