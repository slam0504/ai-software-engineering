# DomainSpec kernel spike（Gate 2 shadow evaluator）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立 `internal/domainspec` 純 domain kernel——typed FactsSnapshot＋canonical digest、YAML+CEL rule bundle、兩階段聚合 evaluator、corpus 重放與 Go oracle 比對 harness，逐項滿足 spec §5 八項出口條件後產出 GO／NO-GO 收斂報告。

**Architecture:** 新 package 無 I/O（依 `internal/plan` 先例）；所有 I/O 結果由 host（測試 harness）解析成 facts 再進 evaluator。全部 fact 群組使用統一 `Fact[T]` 三態 presence wrapper（plan rev2），decode 依 phase／decision presence matrix 驗證。Bundle 載入時完成 CEL compile／type-check／static cost／同 phase depends_on／SCC 驗證，並從 checked AST 抽出每條規則引用的 fact 變數集（missing-leaf 判定用，不做 runtime 追蹤）。Evaluator 依 rev6 傳遞封閉語意跑 eligibility → per-target priority → 全域 truth；missing → unknown、not_applicable → not eligible（不影響 truth）；risk 規則對 `plan.tasks` 逐 task 實體化（綁 `task`＋`source_index`）。比對端以四層 primary precedence 從 violation 列表選 primary；pass 案例另以 shadow RiskDecisions 逐欄比對 oracle 輸出。

**Tech Stack:** Go 1.25、**`cel.dev/cel-go` v0.32.0（新依賴、明確 pin——v0.32.0 起 module path 由 `github.com/google/cel-go` 改名，舊 path `@latest` 會直接失敗（plan gate 實測）；不用 `@latest`。備援：若 module proxy 取用異常，改 pin 舊 path 最後一版 `github.com/google/cel-go` v0.31.0，commit message 記錄選擇）**、`ext.Strings()` 供 substring、`gopkg.in/yaml.v3`（已有）、Go testing。import path 依 v0.32.0 實際 layout 為準（compile 即驗證）。

**Spec:** `docs/superpowers/specs/2026-08-26-domainspec-spike-design.md`——最終 snapshot **commit e09c3fe**（rev1→rev6，design gate APPROVED 2026-08-26）；附件 `2026-08-26-domainspec-rule-inventory.md` 同 commit。

## Global Constraints

- **不接管**：production 零改動——`gateDecide`、`internal/gate`、`internal/gatepolicy`、`internal/escalation`、`internal/plan` 全部只讀；出口 8 以 `git diff` 佐證。root package 只允許新增 **test 檔**（R3 oracle adapter，見 Task 7），不動任何非測試檔。
- **不動 `spec/` scope**：bundle 放 `internal/domainspec/testdata/gate2-bundle.yaml`；發現需要 schema migration 或擴 `spec/rules/**` → **立即 NO-GO**（spec §6 強制停止條件）。
- 時間盒 **3 個 session**（Session 1：Task 1–4；Session 2：Task 5–7；Session 3：Task 8–9＋收斂）；屆期未齊八項出口即帶 diff 報告收 NO-GO，不延展。
- 每條規則引用的 production file:line 實作時先驗仍成立（spec 附錄 A 慣例）。
- 三態 presence 全群組統一 wrapper；**missing → truth=unknown、not_applicable → 規則 not eligible（不影響 truth）**——兩者不可互混（plan gate rev2 P1）。
- error 不併入 unknown：`status=evaluation_error` 與 `truth=unknown` 互不吞併。
- gofmt 乾淨（觸碰檔案）；台灣用語書面中文 doc／commit；`go test ./internal/domainspec/... -count=1` 為每 task 通過門檻，收斂前跑全套 `go test ./...`。
- 已知豁免（收斂報告必列）：R15（scope 導出）derivation 內嵌於 R16 `when`，coverage 以 scope-sensitive 案例＋豁免說明交代；R13／R17／R19／R20／lineage 屬 host 層（spec §1 不進 CEL 清單）。R32 不再豁免——由 Task 6 shadow RiskDecisions 逐欄比對承擔（plan rev2）。

---

### Task 1: FactsSnapshot——統一 presence wrapper＋presence matrix＋strict decode（出口 1）

**Files:**
- Create: `internal/domainspec/facts.go`
- Test: `internal/domainspec/facts_test.go`

**Interfaces:**
- Produces（後續全部 task 依賴）：

```go
package domainspec

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

    Decision       Fact[string]           `json:"decision"`
    Reason         Fact[string]           `json:"reason"`
    Approver       Fact[Approver]         `json:"approver"`
    Entry          Fact[Entry]            `json:"entry"`
    Request        Fact[Request]          `json:"request"`
    Current        Fact[Current]          `json:"current"`
    BaseCommitState Fact[string]          `json:"base_commit_state"` // "ok" | "missing"
    Plan           Fact[PlanFacts]        `json:"plan"`
    RiskPolicy     Fact[RiskPolicyFacts]  `json:"risk_policy"`
    RiskSelections Fact[[]RiskSelection]  `json:"risk_selections"`
    Escalations    Fact[[]EscalationFact] `json:"escalations"`
}

// DecodeFactsSnapshot：strict decode（DisallowUnknownFields，含 wrapper 內層）＋
// wrapper 不變式＋presence matrix 驗證，違反 fail loud（出口 1）。
func DecodeFactsSnapshot(data []byte) (*FactsSnapshot, error)
```

- **Presence matrix（plan rev2 凍結；decode 驗證）**——`known` 欄允許 `missing`（「本應 known 而缺」＝host 給不出，走 unknown 路徑）；`not_applicable` 欄**只**允許 `not_applicable`：

| fact 群組 | submit | decide／approved | decide／rejected |
|---|---|---|---|
| decision／reason／approver／entry | not_applicable | known | known |
| **request** | **known**（R5.submit–R9 的輸入，不得 not_applicable） | known | known |
| current／base_commit_state／plan／risk_policy | not_applicable | known | not_applicable（production rejected 不跑 Reconcile／LoadAt） |
| risk_selections | not_applicable | known | known（R21 的輸入——rejected 帶 selections 即違規） |
| escalations | not_applicable | known | known（gateDecide blocking 檢查兩種 decision 都跑） |

  decide 欄依 `Decision.Value` 選；`Decision` 為 missing 時只驗 phase 無關的列（request 等），decision 相依列跳過矩陣檢查（該 snapshot 評估時本就走 unknown）。

- [ ] **Step 1: Write the failing tests**

```go
package domainspec

import (
    "strings"
    "testing"
)

// wrapper 形式的合法 decide/approved snapshot（各 task 測試共用底稿）
func validSnapshotJSON() string {
    return `{
      "schema_version": 1, "evaluation_phase": "decide",
      "decision": {"presence":"known","value":"approved"},
      "reason": {"presence":"known","value":""},
      "approver": {"presence":"known","value":{"name":"u","email":"u@x"}},
      "entry": {"presence":"known","value":{"exists":true,"has_request":true,"has_record":false}},
      "request": {"presence":"known","value":{"gate":"gate2","subject":"plan:P1","bindings":[
        {"kind":"base_commit","role":"","ref":"HEAD","digest":"git:sha1:` + strings.Repeat("a", 40) + `"}]}},
      "current": {"presence":"known","value":{"spec_manifest":"sha256:` + strings.Repeat("0", 64) + `","plan_manifest":"","risk_policy":"","permission_manifest":""}},
      "base_commit_state": {"presence":"known","value":"ok"},
      "plan": {"presence":"known","value":{"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}},
      "risk_policy": {"presence":"known","value":{"default_tier":"low","rules":[{"match":{"contexts":["gate"],"modules":[]},"tier":"low"}]}},
      "risk_selections": {"presence":"known","value":[{"task_id":"T1","selected_risk_tier":"low","override_reason":""}]},
      "escalations": {"presence":"known","value":[]}
    }`
}

func mustSnapshot(t *testing.T) *FactsSnapshot {
    t.Helper()
    s, err := DecodeFactsSnapshot([]byte(validSnapshotJSON()))
    if err != nil {
        t.Fatalf("decode valid: %v", err)
    }
    return s
}

func TestDecodeFactsSnapshotValid(t *testing.T) {
    s := mustSnapshot(t)
    if s.EvaluationPhase != "decide" || s.Plan.Value == nil || s.Plan.Value.Tasks[0].SourceIndex != 0 {
        t.Fatalf("unexpected snapshot: %+v", s)
    }
}

func TestDecodeFactsSnapshotRejectsUnknownField(t *testing.T) {
    for _, inject := range []struct{ old, new string }{
        {`"schema_version": 1`, `"schema_version": 1, "bogus": true`},          // 頂層
        {`"presence":"known","value":"approved"`, `"presence":"known","value":"approved","x":1`}, // wrapper 內層
    } {
        j := strings.Replace(validSnapshotJSON(), inject.old, inject.new, 1)
        if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
            t.Fatalf("unknown field must be rejected: %s", inject.new)
        }
    }
}

func TestDecodeFactsSnapshotWrapperInvariant(t *testing.T) {
    // presence=known 但 value=null → 拒；presence=not_applicable 但 value 非 null → 拒
    for _, bad := range []struct{ old, new string }{
        {`"plan": {"presence":"known","value":{"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}}`,
            `"plan": {"presence":"known","value":null}`},
        {`"escalations": {"presence":"known","value":[]}`,
            `"escalations": {"presence":"not_applicable","value":[]}`},
    } {
        j := strings.Replace(validSnapshotJSON(), bad.old, bad.new, 1)
        if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
            t.Fatalf("wrapper invariant must reject: %s", bad.new)
        }
    }
}

func TestDecodeFactsSnapshotPresenceMatrix(t *testing.T) {
    // decide/rejected：plan 必須 not_applicable——known 即拒（matrix）
    j := validSnapshotJSON()
    j = strings.Replace(j, `"value":"approved"`, `"value":"rejected"`, 1)
    if _, err := DecodeFactsSnapshot([]byte(j)); err == nil {
        t.Fatal("rejected with plan=known must violate presence matrix")
    }
    // submit：request 不得 not_applicable
    j2 := strings.Replace(validSnapshotJSON(), `"evaluation_phase": "decide"`, `"evaluation_phase": "submit"`, 1)
    j2 = strings.Replace(j2,
        `"request": {"presence":"known"`, `"request": {"presence":"not_applicable"`, 1)
    if _, err := DecodeFactsSnapshot([]byte(j2)); err == nil {
        t.Fatal("submit with request=not_applicable must be rejected（R5.submit–R9 需要 request facts）")
    }
    // decide/approved：plan=missing 合法（本應 known 而缺 → unknown 路徑）
    j3 := strings.Replace(validSnapshotJSON(),
        `"plan": {"presence":"known","value":{"tasks":[{"id":"T1","source_index":0,"minimum_risk_tier":"low","planner_risk_tier":"low","impact":{"contexts":["gate"],"modules":[]}}]}}`,
        `"plan": {"presence":"missing","value":null}`, 1)
    if _, err := DecodeFactsSnapshot([]byte(j3)); err != nil {
        t.Fatalf("missing in a known-column must be legal: %v", err)
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

- [ ] **Step 2: Run to verify failure**——`go test ./internal/domainspec/ -run TestDecodeFactsSnapshot -v`，預期 compile error。
- [ ] **Step 3: Implement**——`Fact[T]` 自訂 `UnmarshalJSON`（內層亦 `DisallowUnknownFields`）；`DecodeFactsSnapshot` 頂層 strict decode → wrapper 不變式 → presence matrix（依上表；submit 亦要求 decision 群組四項全 not_applicable）。
- [ ] **Step 4: Run to verify pass**——同指令全綠；`gofmt -l internal/domainspec/` 空輸出。
- [ ] **Step 5: Commit**——`feat(domainspec): 統一 presence wrapper FactsSnapshot＋presence matrix strict decode（出口 1）`

---

### Task 2: Canonical 序列化＋sha256 digest（出口 7 前半）

**Files:**
- Create: `internal/domainspec/canonical.go`
- Test: `internal/domainspec/canonical_test.go`

**Interfaces:**
- Consumes: Task 1 全部型別（wrapper 存取形：`s.Plan.Value.Tasks` 等）。
- Produces:

```go
// CanonicalJSON：spec §2 canonical 規則正規化後輸出 deterministic JSON（就地複本，不改輸入）：
//   bindings 依 (kind, role, ref, digest) 全序排序（重複不去重）；
//   risk_selections 依 (task_id, selected_risk_tier, override_reason)（重複不去重——R24 可觀測違規輸入）；
//   escalations 依 escalation_id；
//   plan.tasks 保留原始順序（source_index 具語意）；risk_policy.rules 保留來源順序；
//   impact/match 的 contexts、modules 字典序排序後去重（spec rev6）；
//   nil 集合輸出 []（marshal 前把 nil slice 換空 slice；wrapper value 為 nil slice 同樣處理）。
// key 順序：encoding/json 依 struct 欄位宣告序，本身 deterministic。
func CanonicalJSON(s *FactsSnapshot) ([]byte, error)

// SnapshotDigest = "sha256:" + hex(sha256(CanonicalJSON))。
func SnapshotDigest(s *FactsSnapshot) (string, error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestCanonicalReorderAndDupSameDigest(t *testing.T) {
    // 出口 7（rev6）：contexts 重排＋含重複值 → 相同 digest
    a, b := mustSnapshot(t), mustSnapshot(t)
    a.Plan.Value.Tasks[0].Impact.Contexts = []string{"audit", "gate"}
    b.Plan.Value.Tasks[0].Impact.Contexts = []string{"gate", "gate", "audit"}
    da, _ := SnapshotDigest(a)
    db, _ := SnapshotDigest(b)
    if da != db {
        t.Fatalf("set-semantics fields must canonicalize: %s != %s", da, db)
    }
}

func TestCanonicalKeepsDuplicateSelections(t *testing.T) {
    s := mustSnapshot(t)
    sels := []RiskSelection{
        {TaskID: "T1", SelectedRiskTier: "low"}, {TaskID: "T1", SelectedRiskTier: "low"},
    }
    s.RiskSelections.Value = &sels
    j, _ := CanonicalJSON(s)
    if got := strings.Count(string(j), `"task_id":"T1"`); got != 2 {
        t.Fatalf("duplicate selections must survive canonicalization, got %d", got)
    }
}

func TestCanonicalTasksKeepSourceOrder(t *testing.T) {
    s := mustSnapshot(t)
    s.Plan.Value.Tasks = []PlanTask{
        {ID: "T9", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "low"},
        {ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low"},
    }
    j, _ := CanonicalJSON(s)
    if strings.Index(string(j), `"id":"T9"`) > strings.Index(string(j), `"id":"T1"`) {
        t.Fatal("plan.tasks must keep source order, not id order")
    }
}

func TestCanonicalNilAndEmptySliceEqual(t *testing.T) {
    a, b := mustSnapshot(t), mustSnapshot(t)
    var nilEsc []EscalationFact
    emptyEsc := []EscalationFact{}
    a.Escalations.Value = &nilEsc
    b.Escalations.Value = &emptyEsc
    da, _ := SnapshotDigest(a)
    db, _ := SnapshotDigest(b)
    if da != db {
        t.Fatal("nil and empty slices must produce identical canonical form")
    }
}
```

- [ ] **Step 2: Run to verify failure**——`go test ./internal/domainspec/ -run TestCanonical -v`。
- [ ] **Step 3: Implement**——deep-copy → 正規化（`sort.Slice`；contexts/modules `sort.Strings`＋`slices.Compact`；nil slice → 空 slice）→ `json.Marshal`；`crypto/sha256`。
- [ ] **Step 4: Run to verify pass**＋gofmt。
- [ ] **Step 5: Commit**——`feat(domainspec): canonical JSON＋sha256 digest（出口 7 排序／去重／tie-break）`

---

### Task 3: Bundle 型別＋YAML strict 載入驗證（出口 2）

**Files:**
- Create: `internal/domainspec/bundle.go`
- Test: `internal/domainspec/bundle_test.go`
- Modify: `go.mod`／`go.sum`

**Interfaces:**
- Consumes: 無（獨立於 facts；CEL env 變數宣告在本 task 凍結）。
- Produces:

```go
type Rule struct {
    ID        string   `yaml:"id"`
    Phase     string   `yaml:"phase"`      // "submit" | "decide"（單值，spec rev4）
    When      string   `yaml:"when"`       // CEL 布林式
    Effect    string   `yaml:"effect"`     // "deny" | "allow"
    Target    string   `yaml:"target"`     // "decision.eligibility" | "risk.task"（逐 task 實體化為 risk.T<id>）
    DependsOn []string `yaml:"depends_on"` // 限同 phase（spec rev5）
    Priority  int      `yaml:"priority"`
    Verdict   string   `yaml:"verdict"`
    Refs      string   `yaml:"refs"`
    PerTask   bool     `yaml:"per_task"`
    // 四層 primary precedence 靜態 rank（spec §4；Task 6 使用）：
    StepRank  int    `yaml:"step_rank"`
    Stage     string `yaml:"stage"`      // "none" | "pre_loop" | "task_loop" | "post_loop"
    CheckRank int    `yaml:"check_rank"` // 僅 stage=task_loop 有意義
}

type Bundle struct {
    SchemaVersion int    `yaml:"schema_version"`
    Rules         []Rule `yaml:"rules"`
}

type CompiledRule struct {
    Rule
    RefVars map[string]bool // checked AST 頂層 fact 變數引用集（missing/not_applicable 判定；載入時決定）
    // 私有：ast *cel.Ast（Program 於 Evaluate 首次以 runtimeCostLimit 建立並快取）
}

type CompiledBundle struct {
    Digest string // "sha256:" + hex(sha256(canonical YAML→JSON))
    Rules  []CompiledRule
    ByID   map[string]*CompiledRule
}

// LoadBundle：strict YAML（KnownFields）→ enum／id 唯一／depends_on 存在且同 phase →
// CEL compile/type-check（輸出必須 bool）→ static cost estimate 超限拒收 →
// SCC 無環 → RefVars 抽取 → digest。
func LoadBundle(yamlSrc []byte, staticCostLimit uint64) (*CompiledBundle, error)

// celEnv：凍結變數宣告（全 bundle 共用）——
//   evaluation_phase/decision/reason/base_commit_state: string
//   approver: map(string,string); entry: map(string,bool)
//   request/plan/risk_policy/task/sel: map(string,dyn)（sel 可為 null）
//   current: map(string,string)
//   risk_selections: list(map(string,string)); escalations: list(map(string,string))
//   presence: map(string,string)  // 群組名 → "known"/"not_applicable"/"missing"
//   tier_order: map(string,int)   // bundle 常數（host 注入）
// 純函式 extension：tier_rank(string) -> int（未知 tier 回 -1；spec §3 限純函式）
func celEnv() (*cel.Env, error)
```

- [ ] **Step 1: 安裝依賴（pin，不用 @latest）**——`go get cel.dev/cel-go@v0.32.0 && go mod tidy`；若 proxy 取用異常改 `go get github.com/google/cel-go@v0.31.0`（舊 path 最後一版）。獨立 commit：`build: pin cel-go 依賴（DomainSpec spike）`，message 記錄實際選擇與原因。
- [ ] **Step 2: Write the failing tests**（`miniBundle` helper＋六條：載入成功含 RefVars／digest、型別錯拒收、static cost 超限拒收、**跨 phase depends_on 拒收**、循環拒收、未知 YAML 欄位拒收——測試碼與 plan rev1 相同，`miniBundle` 規則 `RA(phase=decide, when="decision == 'approved'", step_rank=2, stage=none)`）：

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
    step_rank: 2
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
    if !strings.HasPrefix(b.Digest, "sha256:") {
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
        t.Fatal("cross-phase depends_on must be rejected at load（spec rev5／出口 2）")
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
    step_rank: 2
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
    step_rank: 2
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

- [ ] **Step 3: Run to verify failure**。
- [ ] **Step 4: Implement**——strict YAML（`yaml.NewDecoder`＋`KnownFields(true)`）；CEL `env.Compile` → `iss.Err()`／輸出型別 `cel.BoolType`；static cost `env.EstimateCost(ast, estimator)`（固定 size hints 的最小 estimator，list hint 64）超限拒收；DFS 三色標記驗無環；RefVars 走訪 checked AST 收集宣告過的頂層 ident；digest 同 Task 2 做法。
- [ ] **Step 5: Run to verify pass**＋gofmt。
- [ ] **Step 6: Commit**——`feat(domainspec): rule bundle strict 載入——CEL compile／static cost／同 phase depends_on／SCC（出口 2）`

---

### Task 4: 兩階段聚合 evaluator＋reason graph（出口 3、4）

**Files:**
- Create: `internal/domainspec/eval.go`
- Test: `internal/domainspec/eval_test.go`

**Interfaces:**
- Consumes: Task 1 `FactsSnapshot`、Task 3 `CompiledBundle`。
- Produces:

```go
type Truth string  // "true" | "false" | "unknown" | "conflict"
type Status string // "ok" | "evaluation_error"

type Violation struct {
    RuleID      string
    Target      string // 實體化後（risk.T<id>）
    SourceIndex int    // per_task 綁定的 task source_index；非 per_task 為 -1
    Verdict     string
}

type ReasonEntry struct {
    RuleID  string
    Target  string
    Outcome string // "matched" | "not_matched" | "not_eligible" | "unknown" | "error"
    Cause   string // not_eligible：哪個 dependency＋false/unknown/not_eligible 哪一種（spec rev6）；
                   // 或 "fact <group> not_applicable" / "fact <group> missing"
}

type Result struct {
    Truth              Truth
    Status             Status
    UnknownLeaves      []string
    MatchedRuleIDs     []string
    ConflictingRuleIDs []string
    Violations         []Violation   // 完整列表（explain＋Task 6 primary 輸入）
    ReasonGraph        []ReasonEntry // 依 bundle 規則序＋task source_index，deterministic
}

// Evaluate：spec §3 兩階段聚合。presence 語意（plan rev2 P1 凍結，兩者不可互混）：
//   rule.RefVars ∩ {presence==missing 的群組} ≠ ∅ → when=unknown（不執行 CEL；
//     群組名記入 unknown_leaves）→ 全域 truth 至少 unknown。
//   rule.RefVars ∩ {presence==not_applicable 的群組} ≠ ∅ → 規則 not eligible
//     （cause "fact <group> not_applicable"），不影響 truth。
//   per_task 規則：Plan presence==missing → 整組 when=unknown（"plan" 入 unknown_leaves）；
//     ==not_applicable → 整組 not eligible；==known → 逐 task 實體化
//     （activation 疊 task/sel；sel 由 task_id 對 risk_selections 首筆配對，無則 null）。
//   eligibility 傳遞封閉（spec rev6）：eligible ⇔ 所有 dependency eligible 且 when=true；
//     dependency false/unknown/not eligible → 下游 not eligible（when 不評估）。
//   runtime cost：Program 帶 cel.CostLimit(runtimeCostLimit)；eval error →
//     status=evaluation_error（該規則 outcome="error"），error 不併入 unknown。
//   全域 truth：conflict → 有效 deny(false) → unknown → true。
func Evaluate(b *CompiledBundle, s *FactsSnapshot, runtimeCostLimit uint64) (*Result, error)
```

- [ ] **Step 1: Write the failing tests**（出口 3 全部＋出口 4；`dagBundle` fixture 與 plan rev1 相同——A(when=false)→B→C 三層）

```go
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
    step_rank: 2
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
    step_rank: 2
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
    step_rank: 2
    stage: none
`

func TestEvaluateNotEligibleTransitive(t *testing.T) {
    b, _ := LoadBundle([]byte(dagBundle), 1_000_000)
    r, err := Evaluate(b, mustSnapshot(t), 100_000)
    if err != nil {
        t.Fatal(err)
    }
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

func TestEvaluateMissingLeafYieldsUnknown(t *testing.T) {
    b, _ := LoadBundle(miniBundle(""), 1_000_000) // RA references `decision`
    s := mustSnapshot(t)
    s.Decision = Fact[string]{Presence: Missing}
    r, _ := Evaluate(b, s, 100_000)
    if r.Truth != "unknown" || r.Status != "ok" {
        t.Fatalf("missing leaf must yield unknown, got %s/%s", r.Truth, r.Status)
    }
    if len(r.UnknownLeaves) == 0 || r.UnknownLeaves[0] != "decision" {
        t.Fatalf("unknown_leaves must name the missing group: %v", r.UnknownLeaves)
    }
}

func TestEvaluateNotApplicableIsNotUnknown(t *testing.T) {
    // plan rev2 P1：not_applicable → not eligible，不得污染 truth 為 unknown
    b, _ := LoadBundle(miniBundle(""), 1_000_000)
    s := mustSnapshot(t)
    // 換成 rejected 形（plan 群組 not_applicable、decision 群組 known）
    rej := "rejected"
    reason := "why"
    s.Decision = Fact[string]{Presence: Known, Value: &rej}
    s.Reason = Fact[string]{Presence: Known, Value: &reason}
    s.Current = Fact[Current]{Presence: NotApplicable}
    s.BaseCommitState = Fact[string]{Presence: NotApplicable}
    s.Plan = Fact[PlanFacts]{Presence: NotApplicable}
    s.RiskPolicy = Fact[RiskPolicyFacts]{Presence: NotApplicable}
    r, _ := Evaluate(b, s, 100_000)
    // RA（when: decision=='approved'）評估為 false → not matched；truth=true 且非 unknown
    if r.Truth != "true" || len(r.UnknownLeaves) != 0 {
        t.Fatalf("not_applicable must not leak into unknown: %s %v", r.Truth, r.UnknownLeaves)
    }
}

func TestEvaluatePerTaskPlanMissingYieldsUnknown(t *testing.T) {
    perTask := `  - id: RT
    phase: decide
    when: "sel == null"
    effect: deny
    target: risk.task
    per_task: true
    priority: 10
    verdict: "RT"
    refs: "test"
    step_rank: 3
    stage: task_loop
    check_rank: 0
`
    b, _ := LoadBundle(miniBundle(perTask), 1_000_000)
    s := mustSnapshot(t)
    s.Plan = Fact[PlanFacts]{Presence: Missing}
    r, _ := Evaluate(b, s, 100_000)
    if r.Truth != "unknown" {
        t.Fatalf("per_task with plan missing must yield unknown（不得 not-eligible 成 true）, got %s", r.Truth)
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
    step_rank: 2
    stage: none
`
    b, _ := LoadBundle(miniBundle(conflictExtra), 1_000_000)
    r, _ := Evaluate(b, mustSnapshot(t), 100_000)
    if r.Truth != "conflict" || r.Status != "ok" {
        t.Fatalf("same-priority opposing effects must conflict, got %s/%s", r.Truth, r.Status)
    }
    if len(r.ConflictingRuleIDs) != 2 {
        t.Fatalf("conflicting ids: %v", r.ConflictingRuleIDs)
    }
}

func TestEvaluateRuntimeCostLimitIsError(t *testing.T) {
    b, _ := LoadBundle(miniBundle(""), 1_000_000)
    r, _ := Evaluate(b, mustSnapshot(t), 0) // runtime limit 0 → 必爆
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
    s := mustSnapshot(t)
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
- [ ] **Step 3: Implement**——activation 由 canonical 正規化複本建 `map[string]any`＋`presence` map；拓撲序（同層依 bundle 檔內順序）；per_task 展開；per-target 最高 priority 裁決＋conflict；全域 truth 依序；Program 以 runtimeCostLimit 首次建立後快取於 CompiledRule。
- [ ] **Step 4: Run to verify pass**＋gofmt；`go test ./internal/domainspec/ -count=2`。
- [ ] **Step 5: Commit**——`feat(domainspec): 兩階段聚合 evaluator——missing→unknown／not_applicable→not eligible／傳遞封閉（出口 3、4）`

---

### Task 5: gate2-bundle.yaml——in-scope 規則全數落 CEL

**Files:**
- Create: `internal/domainspec/testdata/gate2-bundle.yaml`
- Test: `internal/domainspec/gate2_bundle_test.go`

**Interfaces:**
- Consumes: Task 3 `LoadBundle`、Task 4 `Evaluate`。
- Produces: 正式 bundle。規則清單（refs 實作時逐條驗 file:line）：

| id | phase | target | stage | check_rank | 語意（when 要點） |
|---|---|---|---|---|---|
| R5.submit | submit | decision.eligibility | none | - | `!(request.gate in ["gate1","gate2","test_contract_approval"])`（service.go:46-48） |
| R6.submit | submit | decision.eligibility | none | - | gate2 且 subject 非 `plan:` 前綴或 id 空（gate2.go:92-95） |
| R7 | submit | decision.eligibility | none | - | bindings (kind,role) 重複（gate2.go:284-291） |
| R8 | submit | decision.eligibility | none | - | 必備 5 kind 缺一或 role != ""（gate2.go:292-305） |
| R9 | submit | decision.eligibility | none | - | digest 格式不符 regex（gate2.go:28-31、296-299；CEL `matches`） |
| R3 | decide | decision.eligibility | none | - | approver.name=="" && approver.email==""（app.go:5810-5821——gateDecide **步驟 2，先於 PrepareDecision**） |
| R1 | decide | decision.eligibility | none | - | `!(decision in ["approved","rejected"])`（service.go:86-88） |
| R2 | decide | decision.eligibility | none | - | rejected 且 reason==""（service.go:89-91） |
| R4 | decide | decision.eligibility | none | - | `!entry.exists \|\| !entry.has_request \|\| entry.has_record`（service.go:98-101） |
| R5.decide | decide | decision.eligibility | none | - | 同 R5.submit 式（service.go:103-105；**無** approved guard） |
| R11 | decide | decision.eligibility | none | - | approved 且任一 manifest bound digest != "" 且 != current 現值（gate2.go:215-232；只計 Role=="" binding——gate2.go:261-268） |
| R12 | decide | decision.eligibility | none | - | approved 且 base_commit_state=="missing"（gate2.go:234-248） |
| R21 | decide | decision.eligibility | none | - | rejected 且 size(risk_selections)>0（gate2.go:114-118） |
| R6.decide | decide | decision.eligibility | none | - | `decision=="approved" &&` subject 形狀違規（gate2.go:124-127） |
| R24 | decide | decision.eligibility | pre_loop | - | approved 且 risk_selections 依 task_id 有重複（gate2.go:134-140） |
| R25 | decide | risk.task | task_loop | 0 | per_task：`sel == null`（gate2.go:144-147） |
| R27 | decide | risk.task | task_loop | 1 | per_task：CEL 重算 minimum（rules 交集 match → tier_rank max，無命中 → default）!= task.minimum_risk_tier（gate2.go:150-153；riskpolicy.go:4-41） |
| R28.minimum | decide | risk.task | task_loop | 2 | per_task：tier_rank(task.minimum_risk_tier) < 0（gate2.go:154-157） |
| R28.planner | decide | risk.task | task_loop | 3 | per_task：tier_rank(task.planner_risk_tier) < 0（gate2.go:158-161） |
| R29 | decide | risk.task | task_loop | 4 | per_task：planner < minimum（gate2.go:162-164） |
| R28.selected | decide | risk.task | task_loop | 5 | per_task：sel != null 且 tier_rank(sel.selected_risk_tier) < 0（gate2.go:165-168） |
| R30 | decide | risk.task | task_loop | 6 | per_task：selected < minimum（gate2.go:169-171） |
| R31 | decide | risk.task | task_loop | 7 | per_task：selected < planner 且 sel.override_reason == ''（gate2.go:172-174） |
| R26 | decide | decision.eligibility | post_loop | - | approved 且存在 selection 的 task_id 不在 plan.tasks（gate2.go:183-190） |
| R16 | decide | decision.eligibility | none | - | escalations 存在 state!="resolved" 且 block_scope!="" 且（=="workspace" 或 == 導出 scope）；scope 導出內嵌（R15 豁免記載）：gate1→"workspace"、gate2+plan:X→"gate2:"+X、未知→"workspace"（app.go:5895-5912；project.go:81-96——**忽略 hard 旗標**） |

  - **step_rank 凍結表（plan rev2 修正——依 gateDecide 十一步實序，R3 先於 PrepareDecision、R16 在整個 PrepareDecision 之後）**：
    - submit：R5.submit=0 → R6.submit=1 → R7=2 → R8=3 → R9=4（Submit → ValidateRequest → validateGate2Bindings 內部序；R8／R9 在迴圈內穿插，實作時對照 gate2.go:283-305 定 rank，不符以 production 為準）。
    - decide：**R3=1**（gateDecide 步驟 2）→ R1=2 → R2=3 → R4=4 → R5.decide=5（PrepareDecision:86-105）→ R11=6 → R12=7（ReconcileBindings 現值比對，approved）→ R21=8 → R6.decide=9（BuildDecision:114-127）→ R24／task-loop／R26=10（build stage 層內再分 pre_loop/task_loop/post_loop）→ **R16=11**（gateDecide 步驟 8-9，於 PrepareDecision **完成後**才檢查）。
    - 實作時對照 gateDecide 十一步＋service.go 實序整表再驗一次，發現不符以 production 為準修 bundle 並在 commit message 記錄。
  - risk 規則（R24 起含 per_task 全部）guard 皆含 `decision == "approved"`（rejected 早退，gate2.go:114-119）。
  - R27 的 CEL 重算不用 host 函式做交集——`exists` 雙層 comprehension（mapping mutation 可測）；tier 比較用 `tier_rank`。

- [ ] **Step 1: Write the failing test**——表驅動隔離 fixture（測試碼同 plan rev1 `TestGate2BundleIsolatedRuleCoverage`：每條規則一組 Mutate → truth=false 且 Violations 含該 id；乾淨 snapshot → true）。fixture 由 `mustSnapshot` 複本逐條變異；submit fixture `evaluation_phase="submit"`、decision 群組 not_applicable。R16 案例含一筆 hard 語意驗證（state=open 一律擋，不看 hard）。
- [ ] **Step 2: Run to verify failure**（bundle 檔不存在）。
- [ ] **Step 3: Implement**——依上表逐條寫 YAML；R9 regex 沿 gate2.go:28-31。
- [ ] **Step 4: Run to verify pass**＋gofmt＋`go vet ./internal/domainspec/`。
- [ ] **Step 5: Commit**——`feat(domainspec): gate2 rule bundle——in-scope 規則落 CEL＋隔離 fixture 全覆蓋`

---

### Task 6: Primary precedence selector＋比對契約（含 shadow RiskDecisions）（出口 5 契約）

**Files:**
- Create: `internal/domainspec/compare.go`
- Test: `internal/domainspec/compare_test.go`

**Interfaces:**
- Consumes: Task 4 `Result`、Task 3 Rule ranks、Task 1 `FactsSnapshot`。
- Produces:

```go
type Outcome string // "pass" | "blocked"（spec §4——駁回成功也是 pass）

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
func BuildShadowRiskDecisions(s *FactsSnapshot) []RiskDecision

var stageRank = map[string]int{"none": 0, "pre_loop": 1, "task_loop": 2, "post_loop": 3}

// PrimaryViolation：四層 precedence（spec §4）——
// (step_rank, stageRank[stage], source_index(-1 視為 0), check_rank) 字典序最小者。
func PrimaryViolation(b *CompiledBundle, r *Result) (Violation, bool)

// CompareCase（plan rev2 擴充 pass 側逐欄比對）：
//   go=blocked：CEL truth 必須 "false" 且 PrimaryViolation.RuleID == gv.PrimaryRuleID。
//   go=pass：CEL truth 必須 "true" 且 BuildShadowRiskDecisions(s) 與 gv.RiskDecisions
//     逐欄 deep-equal（含順序——雙方皆 task_id 排序；R32 證據）。
//   status=evaluation_error → 一律不一致。
func CompareCase(b *CompiledBundle, s *FactsSnapshot, r *Result, gv GoVerdict) (ok bool, detail string)
```

- [ ] **Step 1: Write the failing tests**

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
        // 跨 gate step（plan rev2 新增）：
        {"submit beats decide", []Violation{
            {RuleID: "R1", SourceIndex: -1}, {RuleID: "R7", SourceIndex: -1}}, "R7"},
        {"R3 beats PrepareDecision internals", []Violation{
            {RuleID: "R11", SourceIndex: -1}, {RuleID: "R3", SourceIndex: -1}}, "R3"},
        {"BuildDecision beats R16", []Violation{
            {RuleID: "R16", SourceIndex: -1}, {RuleID: "R30", SourceIndex: 0}}, "R30"},
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

func TestBuildShadowRiskDecisionsSortedAndComplete(t *testing.T) {
    s := mustSnapshot(t)
    s.Plan.Value.Tasks = []PlanTask{
        {ID: "T2", SourceIndex: 0, MinimumRiskTier: "low", PlannerRiskTier: "high"},
        {ID: "T1", SourceIndex: 1, MinimumRiskTier: "low", PlannerRiskTier: "low"},
    }
    sels := []RiskSelection{
        {TaskID: "T2", SelectedRiskTier: "low", OverrideReason: "ok by owner"},
        {TaskID: "T1", SelectedRiskTier: "low"},
    }
    s.RiskSelections.Value = &sels
    got := BuildShadowRiskDecisions(s)
    if len(got) != 2 || got[0].TaskID != "T1" || got[1].TaskID != "T2" {
        t.Fatalf("must be task_id sorted（R32）: %+v", got)
    }
    if got[1].PlannerRiskTier != "high" || got[1].OverrideReason != "ok by owner" {
        t.Fatalf("five columns must be populated: %+v", got[1])
    }
}

func TestCompareCaseContract(t *testing.T) {
    b := bundleWithRanks(t)
    s := mustSnapshot(t)
    blocked := &Result{Truth: "false", Status: "ok",
        Violations: []Violation{{RuleID: "R24", SourceIndex: -1}}}
    if ok, _ := CompareCase(b, s, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R24"}); !ok {
        t.Fatal("matching primary must compare equal")
    }
    if ok, _ := CompareCase(b, s, blocked, GoVerdict{Outcome: "blocked", PrimaryRuleID: "R30"}); ok {
        t.Fatal("primary mismatch must be reported")
    }
    // pass 側逐欄比對（plan rev2）：RiskDecisions 內容不符必須不一致
    pass := &Result{Truth: "true", Status: "ok"}
    good := GoVerdict{Outcome: "pass", RiskDecisions: BuildShadowRiskDecisions(s)}
    if ok, _ := CompareCase(b, s, pass, good); !ok {
        t.Fatal("identical risk decisions must compare equal")
    }
    bad := GoVerdict{Outcome: "pass", RiskDecisions: []RiskDecision{{TaskID: "T1", SelectedRiskTier: "high"}}}
    if ok, _ := CompareCase(b, s, pass, bad); ok {
        t.Fatal("risk decision content mismatch must be reported（R32 證據）")
    }
    errRes := &Result{Truth: "true", Status: "evaluation_error"}
    if ok, _ := CompareCase(b, s, errRes, good); ok {
        t.Fatal("evaluation_error must never compare equal")
    }
}
```

- [ ] **Step 2: Run to verify failure**；**Step 3: Implement**；**Step 4: pass＋gofmt**。
- [ ] **Step 5: Commit**——`feat(domainspec): 四層 primary precedence＋shadow RiskDecisions 逐欄比對契約（出口 5 契約面）`

---

### Task 7: Corpus manifest＋Go oracle harness＋coverage 報告（出口 5 主體）

**Files:**
- Create: `internal/domainspec/corpus.go`（manifest 型別＋重放＋freshness）
- Create: `internal/domainspec/corpus_oracle_test.go`（oracle adapters——test 檔，import `internal/gatepolicy`／`internal/gate`／`internal/escalation`；domainspec 本體維持零依賴）
- Create: `domainspec_r3_oracle_test.go`（**repo root、package main test 檔**——R3 approver 檢查位於 app.go gateDecide 路徑，`internal/` 無法呼叫；沿既有 root 測試慣例（如 app_wsid_test.go）以 App 測試 helper 產生 R3 verdict。實作時先確認可測 seam；若 root helper 無法無 GUI 直呼，R3 oracle 改列豁免＋合成案例並於收斂報告記載）
- Create: `internal/domainspec/testdata/corpus/*.json`
- Test: `internal/domainspec/corpus_test.go`

**Interfaces:**
- Consumes: Task 1–6 全部。
- Produces:

```go
type CorpusCase struct {
    Name            string         `json:"name"`
    Kind            string         `json:"kind"` // "evaluated" | "acquisition_failed"
    EvaluationPhase string         `json:"evaluation_phase"`
    OracleSource    string         `json:"oracle_source"` // "gate_service_submit" | "gatepolicy_validate" | "gatepolicy_reconcile" | "gate_service_prepare" | "gatepolicy_build" | "escalation" | "app_gatedecide" | "a9_workspace" | "host_boundary"
    Snapshot        *FactsSnapshot `json:"snapshot"`
    GoVerdict       *GoVerdict     `json:"go_verdict"`
    Reason          string         `json:"reason"`
    CoversRules     []string       `json:"covers_rules"`
}

type CoverageReport struct {
    Consistent, Inconsistent, Exempt int
    Mismatches     []string
    CoveredRules   map[string]int // per phase-specific id（隔離案例計數）
    UncoveredRules []string       // in-scope 清單減 covered；必須空或對應豁免表
}
func ReplayCorpus(b *CompiledBundle, cases []CorpusCase, runtimeCostLimit uint64) (*CoverageReport, error)

// VerifyOracleFreshness（plan rev2——出口 6b 的獨立可驗證 guard）：
// 對每筆 evaluated 案例以 recompute 重跑 oracle，回傳固化 verdict 與重跑結果
// 不一致的案例名。recompute 由 oracle adapter（test 檔）注入。
func VerifyOracleFreshness(cases []CorpusCase, recompute func(CorpusCase) (GoVerdict, error)) ([]string, error)
```

- **Oracle adapters（plan rev2 依 seam 重列——每個 seam 一個 adapter 函式，phase 歧義由 seam 消除）**：
  - `gate_service_submit`：`gate.Service.Submit`（stub policy／journal，沿 service 既有測試 double）→ **R5.submit**。
  - `gatepolicy_validate`：`Gate2Policy.ValidateRequest`（stub `PlanLoader`／`GitRunner`，沿 gate2_test.go double）→ R6.submit、R7–R9。
  - `gate_service_prepare`：`gate.Service.PrepareDecision`（stub journal 預置 pending entry）→ R1、R2、R4、**R5.decide**。
  - `gatepolicy_reconcile`：**`Gate2Policy.ReconcileBindings`**（pseudo-record＋stub digest 讀值——R11／R12 的實際 seam，plan rev2 補；PrepareDecision approved 分支同時可觀測 causes[0] 首錯序）→ R11、R12。
  - `gatepolicy_build`：`Gate2Policy.BuildDecision` → R21、**R6.decide**、R24–R31（含 R28 三檢查點）、R26；pass 案例的 `RiskDecisions` 即取其 `*gate.Metadata.RiskDecisions` 逐欄轉 `RiskDecision`。
  - `escalation`：`escalation.BlockingFor`（scope 由案例 gate＋subject 對照 production `scopeForSubject` 值固化）→ R16（含 hard 案例）。
  - `app_gatedecide`：root test 檔（R3）——App helper 以空 git identity 觸發 approver 拒絕。
  - 訊息形狀對映：`map[oracleSeam]map[string]string`——**(seam, message pattern) → rule id**；R5／R6 的 submit／decide 由 seam 天然區分（單一全域字串表無法區分同文案，plan rev2 修正）。同 seam 內一筆錯誤命中多 pattern → 測試紅（對映必須唯一）。
- 案例集（(d) 留 Task 8）：每條 in-scope 規則隔離違規案例＋乾淨 pass 案例（submit／decide 各一，approved pass 案例帶 RiskDecisions 逐欄固化）；precedence 案例**五筆**（spec 三筆：R24+R30、R30@0+R25@1、R31+R26；plan rev2 加跨 gate step 兩筆：R3+R24→R3、R30+R16→R30）；對齊警訊各一（R16 hard、R11 Role!="" 不參與）；`acquisition_failed` 兩筆（LoadAt 錯、rev-parse fatal）；dirty-tree host boundary 一筆（Exempt）。

- [ ] **Step 1: Write the failing tests**——`TestReplayCorpusAllConsistent`（Mismatches 空、Inconsistent==0）；`TestCoverageComplete`（UncoveredRules 逐一比對豁免表 `exemptRules`，非豁免即紅）；`TestOracleFreshnessAllFresh`（recompute 全一致、回傳空）；`TestOracleFreshnessDetectsCorruption`（**程式化腐化**：複製 corpus、翻轉一筆固化 verdict 的 Outcome → VerifyOracleFreshness 必須點名該案例——出口 6b 可獨立驗證的紅路徑，取代 rev1「註解掉重呼」的手動實驗）。
- [ ] **Step 2: Run to verify failure**；**Step 3: Implement**；**Step 4: `go test ./internal/domainspec/ -count=1` 全綠＋root 測試檔 `go test -run TestDomainspecR3Oracle . -count=1`＋gofmt**。
- [ ] **Step 5: Commit**——`feat(domainspec): corpus manifest＋seam-aware oracle harness＋freshness guard（出口 5 主體）`

---

### Task 8: A9 真實案例＋baseline／candidate bundle diff（出口 5 收尾）

**Files:**
- Create: `internal/domainspec/testdata/corpus/a9-*.json`（來源 (d)）
- Create: `internal/domainspec/bundlediff.go`＋`bundlediff_test.go`

**Interfaces:**
- Consumes: Task 7 `ReplayCorpus`、`CorpusCase`。
- Produces:

```go
type FlipRow struct {
    CaseName                          string
    BaselineOutcome, CandidateOutcome Truth
    BaselineUnknown, CandidateUnknown []string
    BaselineStatus, CandidateStatus   Status
}
// 只列有翻轉的案例；三欄（outcome/unknown/error）任一變即列（spec §5 出口 5）。
func DiffBundles(baseline, candidate *CompiledBundle, cases []CorpusCase, limit uint64) ([]FlipRow, error)
```

- **A9 案例（plan rev2 依 journal 實況修正——gate.jsonl 僅兩筆 approved record；stale 走 escalation、superseded-by 是 resolved reason，無 rejected record）**，三筆真實：
  1. 初次 approved（pass，含 risk_decisions 固化）。
  2. **stale blocked**——以 journal＋manifest 記錄的 commit OID 重建「舊 pending 對新 current」時點的 facts（current digest 重算補齊），R11 firing（blocked）。
  3. 修正版 approved＋override_reason（pass）。
  「rejected pass」不是 A9 真實事件——如需該形狀，列**合成案例**歸 source (b)（`gatepolicy_build`），不得標 `a9_workspace`。workspace 或 journal 缺 → 該來源標 `acquisition_failed` 豁免並於收斂報告記載，不阻塞其他出口。
- [ ] **Step 1: failing test**——`TestDiffBundlesDetectsFlip`：candidate = baseline YAML 對 R31 `when` 拿掉 override 分量（`strings.Replace`）→ FlipRow 必含 R31 隔離案例；baseline==candidate → 空表。
- [ ] **Step 2: fail**；**Step 3: Implement**；**Step 4: pass＋gofmt**。
- [ ] **Step 5: Commit**——`feat(domainspec): A9 真實案例（approved／stale blocked／override approved）＋bundle diff 翻轉表`

---

### Task 9: Mutation 鑑別力＋不接管佐證＋GO／NO-GO 收斂報告（出口 6、8）

**Files:**
- Create: `internal/domainspec/mutation_test.go`
- Create: `docs/superpowers/specs/2026-08-26-domainspec-spike-results.md`

**Interfaces:**
- Consumes: Task 5 bundle、Task 7 harness＋freshness、Task 8 diff。
- Produces: 八項出口逐項證據＋GO／NO-GO 判定。

- [ ] **Step 1: Write mutation tests（出口 6）**

```go
func TestMutationBundleRuleFlipsCaught(t *testing.T) {
    // (a) 改一條 bundle 規則（R31 拿掉 override 檢查）→ DiffBundles 必抓翻轉
    src, _ := os.ReadFile("testdata/gate2-bundle.yaml")
    mutated := bytes.Replace(src,
        []byte(`sel.override_reason == ''`), []byte(`false`), 1)
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
// (b) harness guard 鑑別（plan rev2——全程式化，無手動實驗）：
//   TestOracleFreshnessDetectsCorruption（Task 7）＝腐化固化 verdict 必被點名；
//   TestCompareCaseContract 的 mismatch／error 分支（Task 6）＝比對邏輯弱化必紅。
//   收斂報告引用這兩組測試名作為 6b 證據。
```

- [ ] **Step 2: 驗全套**——`go test ./... -count=1`（全 repo）＋`gofmt -l .` 空。
- [ ] **Step 3: 不接管佐證（出口 8）**——`git diff <spike 起點 commit>..HEAD --stat -- . ':(exclude)internal/domainspec' ':(exclude)docs' ':(exclude)go.mod' ':(exclude)go.sum' ':(exclude)domainspec_r3_oracle_test.go'` 輸出必須空；go.mod/go.sum 增項僅 cel-go（diff 內容貼進報告）；root 新增檔僅 `domainspec_r3_oracle_test.go`（test 檔，非 production 路徑）。
- [ ] **Step 4: 收斂報告**——逐項列八個出口的證據（測試名＋輸出摘要）、coverage 表（per phase-specific id）、豁免表（R15／host 層項／acquisition_failed／dirty-tree；**R32 不豁免**——引 shadow RiskDecisions 逐欄比對證據）、剩餘風險（step_rank 人工凍結、A9 來源可用性、R3 root seam 可測性）、GO／NO-GO 判定與 M4.5 建議。措辭遵守證據鏈慣例：只寫實測結果，未驗證項明標。
- [ ] **Step 5: Commit**——`docs(domainspec): spike 收斂報告——八項出口逐項證據＋GO/NO-GO`

---

## Self-Review（已跑）

1. **Spec coverage**：出口 1→Task 1；出口 2→Task 3；出口 3、4→Task 4；出口 5→Task 5–8；出口 6→Task 9（6a）＋Task 6／7（6b 程式化 guard）；出口 7→Task 2＋Task 3 digest；出口 8→Task 9。§1 對齊警訊兩筆→Task 7；§2 presence matrix／canonical→Task 1、2；§3 代數與聚合→Task 3、4；§4 比較契約（含 pass 側 RiskDecisions）→Task 6、7。
2. **Placeholder scan**：無 TBD；R8／R9 rank 細序、step_rank 整表覆核、R3 root seam 可測性為**明標實作時驗證事項**（附 fallback 路徑），非 placeholder。
3. **Type consistency**：`Fact[T]` 存取形（`.Value`／`.Presence`）在 Task 2／4／5／6 fixture 一致；`RiskDecision`（五欄）與 `RiskSelection`（三欄）分離，`GoVerdict.RiskDecisions` 用前者；`CompareCase` 簽章含 snapshot（Task 6／7 一致）。

## 修訂記錄

- rev2（2026-08-26，plan review gate 6 P1 收斂）：
  - P1：presence 統一 `Fact[T]` wrapper 覆蓋全部群組＋phase／decision presence matrix
    （submit 的 request 必須 known）；evaluator 凍結 missing→unknown、
    not_applicable→not eligible 兩分不互混，補 `TestEvaluateNotApplicableIsNotUnknown`
    與 `TestEvaluatePerTaskPlanMissingYieldsUnknown`。
  - P1：step_rank 凍結表修正——R3=1（gateDecide 步驟 2，先於 PrepareDecision）、
    R16=11（PrepareDecision 完成後）；precedence 案例加跨 gate step 兩筆
    （R3+R24→R3、R30+R16→R30）。
  - P1：新增五欄 `RiskDecision`＋`BuildShadowRiskDecisions`（task_id 排序）；
    `CompareCase` pass 側逐欄比對，R32 由豁免改為有證據。
  - P1：oracle adapter 依 seam 重列——補 `gatepolicy_reconcile`（R11／R12 實際
    seam）、root package test adapter（R3，含 fallback）；訊息對映改
    (seam, pattern) 雙鍵，消除 R5／R6 同文案歧義。
  - P1：cel-go 依賴 pin `cel.dev/cel-go` v0.32.0（module path 已改名，`@latest`
    舊 path 實測失敗）；備援 pin 舊 path v0.31.0。
  - P1：A9 案例改為 journal 實況三筆（initial approved／stale blocked 重建／
    override approved）；rejected pass 移列合成案例；出口 6b 改
    `VerifyOracleFreshness`＋程式化腐化測試，取代手動實驗。
- rev1（2026-08-26）：初版。
