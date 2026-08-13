# M3a 計畫與測試契約閉環 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 Gate 2（計畫核可）＋ Test Contract Approval（本機 evidence runner）＋ 升級收件匣＋ STALE 契約擴及 plan／oracle-surface 的可稽核 fail-closed 閉環。

**Architecture:** 既有 `internal/gate` 泛化為 GatePolicy registry 驅動的多 gate 引擎（單一 `gate.jsonl`、共用 journal／projection／tail 修復）；新增純 domain `internal/plan`（YAML 權威＋確定性驗證器）、`internal/evidence`（CAS＋detached worktree runner＋matcher）、`internal/escalation`（append-only 三態）；app.go 以 workspace workflow mutex 線性化所有 blocking 狀態生產路徑；前端新增 PlanWorkspace／DagPane／EscalationInbox 並擴充 GateConsole。

**Tech Stack:** Go 1.26（既有）、Wails v2、Vue 3 + Pinia + vue-i18n、CodeMirror 6（既有）、mermaid strict（既有）、fsnotify（既有）、`gopkg.in/yaml.v3`（新增）、git CLI（worktree／rev-list／diff）。

**Spec:** `docs/superpowers/specs/2026-08-12-m3a-plan-test-contract-design.md`（rev4，四輪 closure review APPROVED 2026-08-12）。每個 task 的驗收對照該 spec 的凍結契約（§號皆指該檔）。

**執行約束（owner 2026-08-12 核定）**：採 Subagent-Driven——**單一 writer**（不平行寫入）、每個 task 完成後主代理 review、每個 Phase 的收尾 gate 全綠後才進下一 Phase。**每個 task 的 commit 必須保持 app 可執行**（跨層簽名變更在同一 task 內同步前端呼叫點與 wailsjs bindings）。

## Global Constraints

- **契約 additive-only**：`contract.Envelope` 與 `gate.Binding` 只加欄位；舊 `gate.jsonl` v1 記錄由 projector 正規化讀取，**不回寫**（§3.1）。
- **稽核只追加**：gate／evidence／escalation journal 全部 append-only＋tail 修復；狀態一律 projection 重算。
- **Supersession scope**：新核可只 supersede 相同 `(gate, subject)` 的 active 核可（§3.1）。
- **Commit 身分三分**（§3.0）：`analysis_base_commit`（plan YAML 內）／`plan_commit`（Gate 2 `base_commit` binding）／`test_commit`（TCA `oracle_surface.ref`）；lineage：`analysis_base_commit..plan_commit` 限 `plan/**`、`plan_commit..test_commit` 限 oracle-surface 路徑。
- **Risk 單一權威**（§3.3）：committed plan 只存 `minimum`／`planner` 兩層；`selected`／`override_reason` 只存在 Gate 2 決議輸入與 `ApprovalRecordV2.metadata`，入 plan schema 即拒絕。
- **核可權威順序**（§3.10）：GateDecide → reconcile → 硬性 validator → blocking escalation 檢查 → append；workflow mutex 覆蓋 GateDecide／reconcile／escalation create·resolve／evidence finalize；lock ordering：workflow mutex → gate journal → escalation journal。
- **CAS 落盤順序**（§3.7）：temp file → 寫入＋算 digest → file Sync＋Close → atomic rename → directory Sync → append＋Sync journal。
- **Evidence runner 邊界**（§4）：結構化 `executable+argv[]`、系統暫存目錄 detached worktree、process group timeout、輸出超限＝`result:error`、恰一次 finalize、啟動清 orphan；**非 sandbox、非 CI enforcement**。
- **digest 格式**：manifest／CAS＝`sha256:<64hex>`；commit 類 Binding.digest＝`git:<algo>:<full oid>`（禁短 SHA）；reference 類欄位（oracle_surface.ref、EvidenceRun.test_commit）＝完整裸 Git OID（spec §3.4 erratum 2026-08-13）。
- **i18n**：所有新 UI 字串進 `zh-TW`＋`en` 雙 locale，維持 key parity（既有 `locales.parity.test.ts` 會抓）。
- **驗證基線**：每個 Phase 收尾 gate 跑 `go vet ./...`、`go test -race ./... -count=1`、`npm --prefix frontend run test`、`npm --prefix frontend run build`；最終加 `wails build`。不得只驗新測試。

---

## File Structure

**修改 Go（Phase 1）：**
- `internal/gate/types.go` — `Binding.Role`、`GateRequest`／`ApprovalRecord` v2 欄位、`Rejected` 終態、`Metadata`。
- `internal/gate/project.go` — v1 正規化、rejected 終態、`(kind,role)` 驗證 helper。
- `internal/gate/policy.go`（新）— `GatePolicy` interface、registry、gate1 policy。
- `internal/gate/record_digest.go`（新）— ApprovalRecord canonical JSON SHA-256。
- `internal/gate/service.go` — Submit／Decide／Reconcile 泛化。
- `internal/journal/journal.go`（新，自 `internal/gate/journal.go` 抽出）— 泛用 append-only JSONL。

**新增 Go（Phase 2）：**
- `internal/spec/scope.go` — `Scope` 參數化（spec／plan 雙 scope）。
- `internal/plan/types.go`、`internal/plan/parse.go`、`internal/plan/validate.go`、`internal/plan/riskpolicy.go`、`internal/plan/lineage.go`——**plan 不 import gate**（技術中立）。
- `internal/gatepolicy/gate2.go` — Gate 2 policy（application package：import gate＋plan＋spec，**避免 gate→plan→gate cycle**）。

**新增 Go（Phase 3）：**
- `internal/evidence/oracle.go`、`cas.go`、`worktree.go`、`runner.go`、`matcher.go`、`journal.go`、`types.go`。
- `internal/gatepolicy/tca.go` — TCA policy（import gate＋plan＋evidence；gate package 維持技術中立、不 import 任何 domain package）。

**新增 Go（Phase 4）：**
- `internal/escalation/types.go`、`journal.go`、`project.go`、`service.go`。

**修改 Go（跨 Phase）：**
- `app.go` — plan／evidence／TCA／escalation 綁定、plan watcher、workflow mutex。
- `internal/contract/event.go` — 新增 `KindEscalation` Kind（`escalation` 已在 plan 契約保留名單）。

**前端：**
- 新增 `frontend/src/stores/plan.ts`、`frontend/src/stores/escalation.ts`。
- 新增 `frontend/src/components/PlanWorkspace.vue`、`DagPane.vue`、`EscalationInbox.vue`。
- 新增 `frontend/src/lib/planDag.ts`（plan→mermaid 純函式）。
- 修改 `frontend/src/components/GateConsole.vue`（gate2／tca 卡片、risk decision 輸入）、`frontend/src/types.ts`、`frontend/src/App.vue`、`frontend/src/i18n/locales/zh-TW.ts`＋`en.ts`。

**文件：**
- `docs/architecture/features/plan-gate.feature`、`test-contract.feature`、`escalation.feature`。
- `docs/architecture/diagrams/`：context map 更新、`plan-aggregate.mmd`、`seq-tca.mmd`。

---

## Phase 1 — Gate 引擎泛化（契約凍結）

### Task 0: 凍結真實 v1 journal fixture（必須在任何 gate code 變更前完成）

**Files:**
- Create: `internal/gate/testdata/m2-gate-v1.jsonl`
- Create: `internal/gate/testdata/README.md`（記載 fixture 來源與去敏內容）

**步驟：**

- [ ] **Step 1: 取實際 M2 journal**：從本機 M2 E2E 驗收使用過的 workspace 複製 `.workbench/gate.jsonl`（M2 驗收留有實際核可／STALE 記錄）。**保留原始 bytes 形狀**（GateOp 包裝、欄位序、既有 ULID），只去敏：reason 文字改為中性字串、如有絕對路徑替換為 `/tmp/ws`。去敏用逐行 JSON 重寫會破壞「真實 bytes」佐證——因此**只以文字替換處理 reason／路徑值，不重排 JSON**。
- [ ] **Step 2: 補 rejected 案例**：現行 M2 UI 可產生 rejected record——在拋棄式 workspace 用**現行（未改動）code path** 實際操作一次 rejected 決定，把該 workspace 的 `gate.jsonl` 中 rejected 相關 ops 追加進 fixture。在 `testdata/README.md` 標明哪些行來自實際 M2 workspace、哪些行是本步驟以現行 code 補產（同為 v1 code 輸出，非手工合成 JSON）。
- [ ] **Step 3: 煙霧驗證**：寫最小測試——`OpenJournal(fixture 副本)` 成功、`Project(ops)` 無錯、entries 數與 README 記載一致。
- [ ] **Step 4: Commit**：`test(gate): 凍結真實 M2 v1 journal fixture（去敏，M3a replay 基準）`

### Task 1: Binding role 與 (kind,role) 驗證 helper

**Files:**
- Modify: `internal/gate/types.go`（`Binding` 加 `Role`）
- Modify: `internal/gate/project.go`（抽出 `validateBindingSet`）
- Test: `internal/gate/project_test.go`

**Interfaces:**
- Consumes: 既有 `Binding{Kind, Ref, Digest}`、`ValidateGate1Bindings`。
- Produces: `Binding{Kind, Role, Ref, Digest}`（`Role` 為 `json:"role,omitempty"`）；`type BindingReq struct{ Kind, Role string; DigestRe *regexp.Regexp }`；`func validateBindingSet(bs []Binding, required []BindingReq) error`——以 `(kind, role)` 判唯一、缺必填或 digest 格式不符回錯。

- [ ] **Step 1: 寫失敗測試**（`(kind,role)` 唯一性＋舊 `role=""` 相容）

```go
func TestBindingKindRoleUniqueness(t *testing.T) {
	bs := []Binding{
		{Kind: "evidence_run", Role: "expected_red", Ref: "evidence:01A", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: "evidence_run", Role: "negative_control", Ref: "evidence:01B", Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	req := []BindingReq{
		{Kind: "evidence_run", Role: "expected_red", DigestRe: reSHA256},
		{Kind: "evidence_run", Role: "negative_control", DigestRe: reSHA256},
	}
	if err := validateBindingSet(bs, req); err != nil {
		t.Fatalf("distinct roles must pass: %v", err)
	}
	bs[1].Role = "expected_red" // 同 (kind,role) 重複
	if err := validateBindingSet(bs, req); err == nil {
		t.Fatal("duplicate (kind,role) must fail")
	}
}

func TestGate1LegacyEmptyRoleStillValid(t *testing.T) {
	bs := []Binding{
		{Kind: "spec_manifest", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: "base_commit", Digest: "git:sha1:" + strings.Repeat("b", 40)},
	}
	if err := ValidateGate1Bindings(bs); err != nil {
		t.Fatalf("legacy role=\"\" must stay valid: %v", err)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗**：`go test ./internal/gate/ -run TestBinding -v` → FAIL（`BindingReq` 未定義）。
- [ ] **Step 3: 實作**：`Binding` 加 `Role string \`json:"role,omitempty"\``；`validateBindingSet` 以 `map[[2]string]bool` 查重、逐 required 檢查存在與 `DigestRe.MatchString(digest)`；改寫 `ValidateGate1Bindings` 為 `validateBindingSet(bs, gate1Reqs)` 的薄包裝（`gate1Reqs`＝spec_manifest+sha256、base_commit+gitOID，role 皆 `""`）。
- [ ] **Step 4: 跑測試通過**：`go test -race ./internal/gate/ -count=1` 全綠（既有測試不得變紅）。
- [ ] **Step 5: Commit**：`git add internal/gate && git commit -m "feat(gate): Binding role 與 (kind,role) 驗證 helper（M3a §3.2）"`

### Task 2: GateRequest／ApprovalRecord v2 型別＋canonical record digest

**Files:**
- Modify: `internal/gate/types.go`
- Create: `internal/gate/record_digest.go`
- Test: `internal/gate/record_digest_test.go`

**Interfaces:**
- Produces:

```go
// types.go additive 欄位（v1 欄位保留供舊 journal decode）
type GateRequest struct {
	Type          string    `json:"_type"`
	SchemaVersion int       `json:"schema_version,omitempty"` // v2=2；v1 記錄無此欄
	ApprovalID    string    `json:"approval_id"`
	Gate          string    `json:"gate"`
	Subject       string    `json:"subject,omitempty"`
	Bindings      []Binding `json:"bindings,omitempty"`
	// v1 legacy（decode 用，v2 不再寫）：
	SpecManifestDigest string `json:"spec_manifest_digest,omitempty"`
	BaseCommit         string `json:"base_commit,omitempty"`
	CreatedAt          string `json:"created_at"`
}
type RiskDecision struct {
	TaskID          string `json:"task_id"`
	MinimumRiskTier string `json:"minimum_risk_tier"`
	PlannerRiskTier string `json:"planner_risk_tier"`
	SelectedRiskTier string `json:"selected_risk_tier"`
	OverrideReason  string `json:"override_reason,omitempty"`
}
type Metadata struct {
	RiskDecisions []RiskDecision `json:"risk_decisions,omitempty"`
}
type ApprovalRecord struct { // 加：SchemaVersion、Subject、Metadata
	Type          string    `json:"_type"`
	SchemaVersion int       `json:"schema_version,omitempty"`
	ApprovalID    string    `json:"approval_id"`
	Gate          string    `json:"gate"`
	Subject       string    `json:"subject,omitempty"`
	Decision      string    `json:"decision"`
	Approver      Approver  `json:"approver"`
	Reason        string    `json:"reason"`
	Bindings      []Binding `json:"bindings"`
	Metadata      *Metadata `json:"metadata,omitempty"`
	CreatedAt     string    `json:"created_at"`
}
const Rejected State = "rejected"
// record_digest.go
func RecordDigest(rec ApprovalRecord) (string, error) // canonical JSON（struct 欄位序固定）SHA-256，"sha256:<64hex>"
```

- [ ] **Step 1: 寫失敗測試**

```go
func TestRecordDigestDeterministicAndTamperEvident(t *testing.T) {
	rec := ApprovalRecord{Type: "approval_record", SchemaVersion: 2, ApprovalID: "01A",
		Gate: "gate2", Subject: "plan:M3A-001", Decision: "approved",
		Approver: Approver{ID: "u", Method: "app-local"}, Reason: "ok",
		Bindings: []Binding{{Kind: "plan", Digest: "sha256:" + strings.Repeat("a", 64)}},
		Metadata: &Metadata{RiskDecisions: []RiskDecision{{TaskID: "T1",
			MinimumRiskTier: "medium", PlannerRiskTier: "medium", SelectedRiskTier: "medium"}}},
		CreatedAt: "2026-08-12T00:00:00Z"}
	d1, err := RecordDigest(rec)
	if err != nil || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("digest: %v %q", err, d1)
	}
	d2, _ := RecordDigest(rec)
	if d1 != d2 {
		t.Fatal("digest must be deterministic")
	}
	rec.Metadata.RiskDecisions[0].SelectedRiskTier = "high" // metadata 竄改必須改變 digest（§3.4）
	d3, _ := RecordDigest(rec)
	if d3 == d1 {
		t.Fatal("metadata tamper must change digest")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗**：`go test ./internal/gate/ -run TestRecordDigest -v` → FAIL。
- [ ] **Step 3: 實作**：`RecordDigest` ＝ `json.Marshal(rec)`（Go struct 欄位序即 canonical，同 `spec.ManifestDigest` 慣例；transitions 本來就不在 record 內，滿足「不含 transitions」）→ `sha256` → `"sha256:"+hex`。
- [ ] **Step 4: 跑測試通過**（`-race`、全 package）。
- [ ] **Step 5: Commit**：`feat(gate): v2 request/record 型別與 canonical record digest（§3.1／§3.4）`

### Task 3: Projector v2 —— v1 正規化、rejected 終態、真實 M2 fixture replay

**Files:**
- Modify: `internal/gate/project.go`
- Test: `internal/gate/project_test.go`（使用 **Task 0 凍結的真實 fixture**，不得另行手寫 journal JSON）

**Interfaces:**
- Produces: `Project(ops)` 回傳的 `GateEntry.Request` 一律為正規化後 v2 形狀（`Subject=="workspace"`、`Bindings` 含 spec_manifest＋base_commit 兩筆）；`normalizeRequest(r GateRequest) GateRequest`（v1→v2，純函式）；rejected record → `State==Rejected` 終態。

- [ ] **Step 1: 寫失敗測試**（讀 Task 0 fixture；entries 期望值依 fixture README 記載的實際內容調整）

```go
func TestProjectNormalizesV1AndRejectedTerminal(t *testing.T) {
	data, _ := os.ReadFile("testdata/m2-gate-v1.jsonl")
	ops, _, bad := parseOps(data)
	if bad != nil {
		t.Fatalf("fixture must parse: %v", bad.err)
	}
	entries, err := Project(ops)
	if err != nil {
		t.Fatal(err)
	}
	req := entries[0].Request
	if req.Subject != "workspace" || len(req.Bindings) != 2 {
		t.Fatalf("v1 request must normalize to subject=workspace + 2 bindings, got %+v", req)
	}
	var rejected *GateEntry
	for i := range entries {
		if entries[i].Record != nil && entries[i].Record.Decision == "rejected" {
			rejected = &entries[i]
		}
	}
	if rejected == nil || rejected.State != Rejected {
		t.Fatalf("rejected must be terminal state, got %+v", rejected)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗**（現行：v1 request 無 Subject；rejected 停在 Pending——與 `project.go:46` 現況一致）。
- [ ] **Step 3: 實作**：`normalizeRequest`——`SchemaVersion==0 && SpecManifestDigest!=""` 時組 v2（gate1、workspace、兩筆 bindings），不動原始 raw；`approval_record` case 補 `if r.Decision == "rejected" && e.State == Pending { e.State = Rejected }`；transition switch 維持 stale/superseded 語意（rejected 之後不應有 transition，若有維持終態不復活）。
- [ ] **Step 4: 跑測試通過**（全 package `-race`；既有 service/journal 測試不得變紅）。
- [ ] **Step 5: Commit**：`feat(gate): projector v1 正規化＋rejected 終態＋M2 fixture replay（§3.1）`

### Task 4: GatePolicy registry＋gate1 policy 抽取

**Files:**
- Create: `internal/gate/policy.go`
- Test: `internal/gate/policy_test.go`

**Interfaces:**
- Produces:

```go
type StaleCause struct{ Cause, EvidenceRef string }
// DecisionInput：Decide 的受限輸入（§3.1）；bindings 一律由 Service 從 request 複製。
type DecisionInput struct {
	RiskSelections []RiskSelection // gate2 用；gate1/tca 為空
}
type RiskSelection struct{ TaskID, SelectedRiskTier, OverrideReason string }
type GatePolicy interface {
	ValidateRequest(req GateRequest) error
	// BuildDecision 驗證受限輸入並回傳要寫入 record 的 Metadata（§3.3）。
	// decision=="rejected" 時 input 必須為空（免 risk 輸入）。
	BuildDecision(req GateRequest, decision string, input DecisionInput) (*Metadata, error)
	SupersessionKey(gate, subject string) string // 預設 gate+"|"+subject
	ReconcileBindings(rec ApprovalRecord) ([]StaleCause, error)
}
type Registry map[string]GatePolicy // key = gate 名
func NewGate1Policy(current ManifestFn) GatePolicy
```

- [ ] **Step 1: 寫失敗測試**：gate1 policy 的 `ValidateRequest` 沿用 Task 1 的必填表；`BuildDecision("approved", 空 input)` 回 `nil` metadata；`ReconcileBindings` 在 manifest 不符時回 `[{Cause:"spec_manifest changed", EvidenceRef:<cur>}]`、讀取錯誤回 error 不回 stale（沿 `ReconcileGate1` 現行語意，`service.go:114-148`）。

```go
func TestGate1PolicyReconcile(t *testing.T) {
	cur := "sha256:" + strings.Repeat("c", 64)
	p := NewGate1Policy(func() (string, error) { return cur, nil })
	rec := ApprovalRecord{Gate: "gate1", Bindings: []Binding{
		{Kind: "spec_manifest", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	causes, err := p.ReconcileBindings(rec)
	if err != nil || len(causes) != 1 || causes[0].Cause != "spec_manifest changed" {
		t.Fatalf("expected stale cause, got %v %v", causes, err)
	}
	perr := NewGate1Policy(func() (string, error) { return "", spec.ErrConcurrentModification })
	if _, err := perr.ReconcileBindings(rec); err == nil {
		t.Fatal("read error must fail closed, not stale") // §3.9
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** → **Step 3: 實作**（gate1 policy struct 持 `current ManifestFn`）→ **Step 4: 全 package `-race` 通過** → **Step 5: Commit**：`feat(gate): GatePolicy registry＋gate1 policy（§2.1）`

### Task 5: Service 泛化——Submit(gate,subject)、Decide 複製 bindings、scoped supersession、泛化 Reconcile

**Files:**
- Modify: `internal/gate/service.go`
- Modify: `app.go:930-1143`（`ensureGate`／`SubmitForApproval`／`GateDecide` 呼叫點改新簽名；`GateDecide` Wails 綁定加 `riskSelections []gate.RiskSelection` 參數）
- Test: `internal/gate/service_test.go`

**Interfaces:**
- Produces:

```go
func NewService(j *Journal, reg Registry, ulid func() string, now func() string, em Emitter) *Service
func (s *Service) Submit(gateName, subject string, bindings []Binding) (string, error)
// Decide 拆為兩段，供 §3.10 的權威順序（reconcile→validator→blocker→append）在 app 層編排：
// PrepareDecision 在 mutex 內 Project→pending 檢查→normalizeRequest→
// **current-binding validation（僅 decision=="approved" 時執行）**（以 pending request 的
// bindings 建 pseudo-record 跑該 gate 的 policy.ReconcileBindings——Reconcile() 只掃 active
// projection，涵蓋不到 pending；待核期間 plan／oracle／上游 Gate 2 改變時，此步以任何
// stale cause 拒絕核可，fail closed。**rejected 不做此驗證**：依 spec 駁回只需 reason，
// 過期 request 必須仍可駁回）→ policy.BuildDecision（硬性 decision validator）→
// 回傳組好的 record（尚未 append）。
// CommitDecision 驗 prepared 仍為 pending 後 append record＋scoped supersession transitions。
// 兩段之間由呼叫端（app.workflowMu）保證無其他 blocking 狀態生產者插入。
type PreparedDecision struct{ Record ApprovalRecord }
func (s *Service) PrepareDecision(id, decision, reason string, approver Approver, input DecisionInput) (PreparedDecision, error)
func (s *Service) CommitDecision(p PreparedDecision) error
// Decide＝Prepare→Commit 的便捷包裝（無 blocker 檢查，僅供測試與 gate1 相容路徑）。
func (s *Service) Decide(id, decision, reason string, approver Approver, input DecisionInput) error
func (s *Service) Reconcile() error // 取代 ReconcileGate1：逐 active entry 依其 gate 的 policy.ReconcileBindings
func (s *Service) List() ([]GateEntry, error)
func (s *Service) Lookup(approvalID string) (*ApprovalRecord, State, error) // TCA gate2_approval resolver 用
```

- Consumes: Task 3 的 normalize（`Decide` 讀 request 一律經 `normalizeRequest`）、Task 4 registry。

- [ ] **Step 1: 寫失敗測試**（三個核心不變量）

```go
func TestSupersessionScopedByGateAndSubject(t *testing.T) {
	s := newTestServiceWithGates(t) // registry 含 gate1 + stub gate2/tca policy
	id1, _ := s.Submit("gate1", "workspace", gate1Bindings())
	_ = s.Decide(id1, "approved", "", approver(), DecisionInput{})
	id2, _ := s.Submit("test_contract_approval", "task:P1/T1", tcaStubBindings())
	_ = s.Decide(id2, "approved", "", approver(), DecisionInput{})
	entries, _ := s.List()
	if stateOf(entries, id1) != Active { // 核可 TCA 不得 supersede Gate 1（§3.1）
		t.Fatal("gate1 approval must survive TCA approval")
	}
	id3, _ := s.Submit("test_contract_approval", "task:P2/T1", tcaStubBindings())
	_ = s.Decide(id3, "approved", "", approver(), DecisionInput{})
	entries, _ = s.List()
	if stateOf(entries, id2) != Active { // 不同 plan 的同名 T1 不互相 supersede
		t.Fatal("different plan_id must not supersede")
	}
	id4, _ := s.Submit("test_contract_approval", "task:P1/T1", tcaStubBindings())
	_ = s.Decide(id4, "approved", "", approver(), DecisionInput{})
	entries, _ = s.List()
	if stateOf(entries, id2) != Superseded { // 同 (gate,subject) 才 supersede
		t.Fatal("same subject must supersede")
	}
}

func TestDecideCopiesBindingsFromRequest(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, _ := s.Submit("gate1", "workspace", gate1Bindings())
	_ = s.Decide(id, "approved", "", approver(), DecisionInput{})
	entries, _ := s.List()
	rec := recordOf(entries, id)
	if len(rec.Bindings) != 2 || rec.Subject != "workspace" || rec.SchemaVersion != 2 {
		t.Fatalf("record must copy gate/subject/bindings from request: %+v", rec)
	}
}

func TestRejectedNeedsOnlyReason(t *testing.T) {
	s := newTestServiceWithGates(t)
	id, _ := s.Submit("gate1", "workspace", gate1Bindings())
	if err := s.Decide(id, "rejected", "不完整", approver(), DecisionInput{}); err != nil {
		t.Fatalf("rejected must not require risk input: %v", err)
	}
}
```

- [ ] **Step 1b: 補兩測（pending 新鮮度）**：

```go
func TestStalePendingApproveFailsRejectSucceeds(t *testing.T) {
	s := newTestServiceWithGates(t) // gate1 policy 的 current manifest 可由測試切換
	id, _ := s.Submit("gate1", "workspace", gate1Bindings())
	s.mutateCurrentManifest() // 待核期間規格變更 → pending bindings 過期
	if _, err := s.PrepareDecision(id, "approved", "", approver(), DecisionInput{}); err == nil {
		t.Fatal("approve on stale pending must fail (current-binding validation)")
	}
	if _, err := s.PrepareDecision(id, "rejected", "已過期", approver(), DecisionInput{}); err != nil {
		t.Fatalf("reject on stale pending must still succeed: %v", err) // rejected 免驗證
	}
}
```

- [ ] **Step 2: 跑測試確認失敗**。
- [ ] **Step 3: 實作**：`Submit` 查 registry→`ValidateRequest`→寫 v2 request（SchemaVersion=2）。`PrepareDecision` 在 mutex 內 Project→找 pending→`normalizeRequest`→**approved 時執行 current-binding validation**（pseudo-record 跑 `policy.ReconcileBindings`，任何 stale cause 即回錯；rejected 跳過）→`policy.BuildDecision`→組 `ApprovalRecord{SchemaVersion:2, Gate/Subject/Bindings 複製自 request, Metadata}` 回傳 prepared。`CommitDecision` 驗仍 pending→append record→approved 時只對 `SupersessionKey` 相同的 active entry append superseded transition。`Decide`＝Prepare→Commit 包裝。`Reconcile` 逐 active entry 呼叫其 gate policy 的 `ReconcileBindings`，每個 cause append stale transition＋`EmitGateEvent("binding_stale", …)`（沿現行「至多一次」語意）。`List()` 改呼叫 `Reconcile()`。
- [ ] **Step 4: 更新 app.go 呼叫點**：`ensureGate` 以 `Registry{"gate1": NewGate1Policy(...)}` 建 Service；`SubmitForApproval` 改組 bindings 後呼叫 `Submit("gate1", "workspace", bindings)`；`GateDecide(approvalID, decision, reason string, riskSelections []gate.RiskSelection)`。
- [ ] **Step 5: 同 task 內同步前端（可執行基線）**：`wails dev` 或 `wails generate module` 重生 `frontend/wailsjs` bindings；`GateConsole.vue` 的 `GateDecide` 呼叫改為第四參數傳 `[]`（gate1 空 risk）；vitest 中對應 mock 簽名更新。**本 task commit 後 app 必須可跑 M2 既有 Gate 1 流程**（production adapter 測試：`app_gate_test.go` 走一輪 submit→decide→stale）。
- [ ] **Step 6: 全套通過**：`go vet ./... && go test -race ./... -count=1 && npm --prefix frontend run test && npm --prefix frontend run build`。
- [ ] **Step 7: Commit**：`feat(gate): service 泛化——scoped supersession＋Prepare/Commit 拆分＋前端同步（§3.1／§3.10）`

### Task 6: 抽出 `internal/journal` 泛用 append-only JSONL

**Files:**
- Create: `internal/journal/journal.go`＋`journal_test.go`（自 `internal/gate/journal.go` 平移：`OpenJournal`／`Append(line []byte)`／`Lines()`／tail 修復／degraded／quarantine，操作 raw JSON line）
- Modify: `internal/gate/journal.go` → 薄包裝（GateOp marshal/unmarshal + 內嵌 `journal.Journal`），對外 API 不變
- Test: 既有 `internal/gate/journal_test.go` 全數不變且通過（重構安全網）

**Interfaces:**
- Produces: `journal.Open(path string) (*Journal, error)`、`(*Journal).Append(line []byte) error`（單行＋`\n`＋Sync）、`Lines() [][]byte`、`Degraded() bool`、`Close()`；tail 修復語意與現行完全相同（中段 malformed fail loud、final 壞行 quarantine+truncate、無尾 `\n` 視為 torn final）。
- 理由：Phase 3 evidence journal 與 Phase 4 escalation journal 重用同一 crash 語意——三處使用，Rule of Three 成立。

- [ ] **Step 1: 平移 parseOps 邏輯為 raw-line 版並補測試**（沿用既有 journal_test.go 情境改寫到新 package：torn tail、mid-file malformed、degraded on write error）。
- [ ] **Step 2: gate.Journal 改包裝**，既有測試作為回歸網。
- [ ] **Step 3: 全套 `-race` 通過。**
- [ ] **Step 4: Commit**：`refactor(journal): 抽出泛用 append-only JSONL（gate/evidence/escalation 共用）`

**Phase 1 收尾 gate**：`go vet ./...`、`go test -race ./... -count=1`、`npm --prefix frontend run test`、`npm --prefix frontend run build` 全綠後 commit tag 訊息註明 Phase 1 完成。

---

## Phase 2 — Plan 資料層＋Plan Workspace＋PlannerAssist＋Gate 2

### Task 7: `spec.Scope` 參數化（spec／plan 雙 scope）

**Files:**
- Create: `internal/spec/scope.go`
- Modify: `internal/spec/manifest.go`、`snapshot.go`、`commit.go`、`gitrepo.go`（package-level scope 常數改經 `Scope` 注入；`NewGitRepo(root)` 改 `NewGitRepo(root, sc Scope)`，既有呼叫點傳 `SpecScope`）
- Test: `internal/spec/scope_test.go`＋既有測試全綠

**Interfaces:**
- Produces:

```go
type Scope struct {
	Version  int
	Patterns []string // canonical manifest 內容的一部分
	Roots    []string // git pathspec 用（等同現行 managedScopeRoots 角色）
	Match    func(rel string) bool
}
var SpecScope = Scope{Version: 1, Patterns: ScopePatterns, Roots: managedScopeRoots, Match: InScope}
var PlanScope = Scope{Version: 1, Patterns: []string{"plan/**"}, Roots: []string{"plan"},
	Match: func(rel string) bool { return rel == "plan" || strings.HasPrefix(strings.TrimPrefix(rel, "./"), "plan/") }}
func (sc Scope) ManifestDigest(entries []FileEntry) (string, error) // 取代 package-level（保留舊函式為 SpecScope 包裝）
```

- [ ] **Step 1: 寫失敗測試**：`PlanScope.ManifestDigest` 與 `SpecScope.ManifestDigest` 對同一 entries 產生**不同** digest（patterns 進 canonical 內容）；`PlanScope.Match("plan/M3A-001.yaml")==true`、`Match("spec/features/x.feature")==false`。
- [ ] **Step 2: 確認失敗** → **Step 3: 重構實作**（現有 package-level 函式改薄包裝委派 `SpecScope`，避免既有呼叫點大改；`GitRepo` 持 `sc Scope`，`activeScopePathspecs`／`checkNoOutOfScopeStaged`／`scopedDiffForDisplay` 改用 `r.sc.Roots`／`r.sc.Match`）。
- [ ] **Step 4: 既有 spec 測試全綠（重構安全網）＋新測試綠。**
- [ ] **Step 5: Commit**：`refactor(spec): Scope 參數化，spec/plan 雙 scope（§3.5）`

### Task 8: `internal/plan` —— YAML 解析＋確定性驗證器＋risk policy 重算

**Files:**
- Create: `internal/plan/types.go`、`parse.go`、`validate.go`、`riskpolicy.go`
- Test: `internal/plan/validate_test.go`、`riskpolicy_test.go`
- Modify: `go.mod`（加 `gopkg.in/yaml.v3`）

**Interfaces:**
- Produces:

```go
type Command struct {
	Executable string   `yaml:"executable" json:"executable"`
	Argv       []string `yaml:"argv" json:"argv"`
}
type ExpectedFailure struct {
	TestIDs []string `yaml:"test_ids" json:"test_ids"`
	Matcher string   `yaml:"matcher" json:"matcher"` // 子字串比對；正則不做（YAGNI）
}
type TestContract struct {
	Command         Command         `yaml:"command" json:"command"`
	ExpectedFailure ExpectedFailure `yaml:"expected_failure" json:"expected_failure"`
}
type Task struct {
	ID, Title       string
	Scenarios       []string
	DependsOn       []string `yaml:"depends_on"`
	Impact          struct{ Contexts, Modules []string }
	Completion      []string
	MinimumRiskTier string `yaml:"minimum_risk_tier"`
	PlannerRiskTier string `yaml:"planner_risk_tier"`
	PermissionsRef  string `yaml:"permissions_ref"`
	TestContract    TestContract `yaml:"test_contract"`
}
type Plan struct {
	PlanID             string `yaml:"plan_id"`
	AnalysisBaseCommit string `yaml:"analysis_base_commit"`
	SpecManifest       string `yaml:"spec_manifest"`
	RiskPolicy         string `yaml:"risk_policy"`
	Tasks              []Task
}
func Parse(b []byte) (Plan, error) // yaml.v3 KnownFields(true)：未知欄位（含 selected_risk_tier/override_reason）即錯（§3.3）
var tierOrder = map[string]int{"low": 1, "medium": 2, "high": 3}
type RiskPolicy struct {
	Version     int    `yaml:"version"`
	DefaultTier string `yaml:"default_tier"`
	Rules       []struct {
		Match struct{ Contexts, Modules []string } `yaml:"match"`
		Tier  string                               `yaml:"tier"`
	} `yaml:"rules"`
}
func ParseRiskPolicy(b []byte) (RiskPolicy, error)
func (p RiskPolicy) ComputeMinimum(t Task) string // 匹配 rules（context/module 交集非空）取最高 tier，無匹配用 default
// Validate：schema、DAG 無環、依賴存在、task ID 唯一、minimum 重算相符、planner>=minimum、
// scenario ref ∈ specScenarios。違反回收集式錯誤（全部列出，供編輯器 inline 顯示）。
func Validate(p Plan, policy RiskPolicy, specScenarios map[string]bool) []error
```

- [ ] **Step 1: 寫失敗測試**（六個拒絕情境＋一個通過情境）

```go
func TestValidateRejects(t *testing.T) {
	pol := RiskPolicy{Version: 1, DefaultTier: "medium"}
	base := validPlan() // helper：單 task T1、planner=minimum=medium、scenario E1
	scen := map[string]bool{"E1": true}
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"cycle", func(p *Plan) { p.Tasks[0].DependsOn = []string{"T1"} }},
		{"missing dep", func(p *Plan) { p.Tasks[0].DependsOn = []string{"T9"} }},
		{"dup id", func(p *Plan) { p.Tasks = append(p.Tasks, p.Tasks[0]) }},
		{"planner below minimum", func(p *Plan) { p.Tasks[0].PlannerRiskTier = "low" }},
		{"minimum mismatch", func(p *Plan) { p.Tasks[0].MinimumRiskTier = "low" }}, // 重算=medium
		{"unknown scenario", func(p *Plan) { p.Tasks[0].Scenarios = []string{"E9"} }},
	}
	for _, c := range cases {
		p := base
		p.Tasks = append([]Task(nil), base.Tasks...)
		c.mutate(&p)
		if errs := Validate(p, pol, scen); len(errs) == 0 {
			t.Fatalf("%s must be rejected", c.name)
		}
	}
	if errs := Validate(base, pol, scen); len(errs) != 0 {
		t.Fatalf("valid plan must pass: %v", errs)
	}
}

func TestParseRejectsSelectedRiskTier(t *testing.T) { // §3.3：入 plan schema 即拒絕
	_, err := Parse([]byte("plan_id: P1\ntasks:\n  - id: T1\n    selected_risk_tier: high\n"))
	if err == nil {
		t.Fatal("selected_risk_tier in plan schema must be rejected")
	}
}
```

- [ ] **Step 2: 確認失敗** → **Step 3: 實作**（DAG 環偵測用 DFS 三色標記；`Parse` 用 `yaml.Decoder.KnownFields(true)`）→ **Step 4: `-race` 通過** → **Step 5: Commit**：`feat(plan): YAML 解析＋確定性驗證器＋risk policy 重算（§3.5）`

### Task 9: lineage 驗證＋plan 兩階段 commit（Preview token 擴充）

**Files:**
- Create: `internal/plan/lineage.go`
- Modify: `internal/spec/commit.go`（`CommitToken` 加 `AnalysisBase string`；`PreviewSpecCommit`／`ConfirmSpecCommit` 已泛化為 scope-driven，plan repo 直接重用）
- Test: `internal/plan/lineage_test.go`（用 `t.TempDir()` 建真 git repo，同 `internal/spec/gitrepo_test.go` 慣例）

**Interfaces:**
- Produces:

```go
// lineage.go —— git CLI 皆經 spec.GitRepo 的 git() helper 模式（新增輕量 runner 亦可，介面如下）
type GitRunner interface{ Git(args ...string) ([]byte, error) }
// VerifyLineage：ancestor 必須是 descendant 的祖先，且 ancestor..descendant 範圍內
// 所有變更路徑（含 rename 的 old 與 new 兩側）全部滿足 allow（§3.0／§3.4 規則 2–3 共用）。
// 路徑列舉用 `git diff --name-status -z --find-renames ancestor..descendant`：
// R<score> 條目同時驗 old path 與 new path；-z 避免路徑含空白／非 ASCII 時解析錯誤。
func VerifyLineage(g GitRunner, ancestor, descendant string, allow func(path string) bool) error
```

- [ ] **Step 1: 寫失敗測試**

```go
func TestVerifyLineage(t *testing.T) {
	g := newTestRepo(t)                      // commit C0（code：main.go）
	g.commitFile("plan/P1.yaml", "v1")       // C1：只動 plan/**
	if err := VerifyLineage(g, g.oid("HEAD~1"), g.oid("HEAD"), spec.PlanScope.Match); err != nil {
		t.Fatalf("plan-only range must pass: %v", err)
	}
	g.commitFile("main.go", "x")             // C2：混入 code 變更
	if err := VerifyLineage(g, g.oid("HEAD~2"), g.oid("HEAD"), spec.PlanScope.Match); err == nil {
		t.Fatal("range with non-plan change must fail") // §3.0
	}
	if err := VerifyLineage(g, g.oid("HEAD"), g.oid("HEAD~2"), spec.PlanScope.Match); err == nil {
		t.Fatal("non-ancestor must fail")
	}
}

func TestVerifyLineageRenameSafety(t *testing.T) {
	g := newTestRepo(t) // C0：code 檔 src/a.go
	g.gitMv("src/a.go", "plan/a.yaml") // code→plan rename：old path 在 allow 外
	if err := VerifyLineage(g, g.oid("HEAD~1"), g.oid("HEAD"), spec.PlanScope.Match); err == nil {
		t.Fatal("code→plan rename must fail (old path outside allow)")
	}
	// oracle 用法的對偶測試（allow=OracleDecl.Match）在 Task 19 的 runner 測試補：
	// oracle→非 oracle rename 必須拒絕（new path 在 allow 外）。
}
```

- [ ] **Step 2: 確認失敗** → **Step 3: 實作**：ancestor 檢查用 `git merge-base --is-ancestor A B`（exit 0/1）；範圍路徑用 `git diff --name-status -z --find-renames A..B`，逐條解析 status（A/M/D 驗單一路徑；`R<score>`／`C<score>` 同時驗 old 與 new 兩側），任一路徑不滿足 `allow` 即拒。
- [ ] **Step 4: Preview token 擴充**：plan repo 的 `PreviewSpecCommit` 呼叫端（app.go，Task 12）在 token 額外綁 `AnalysisBase`（讀自 worktree plan YAML）；`ConfirmSpecCommit` 前重讀 plan YAML 的 `analysis_base_commit`，不符 token 即回 `ErrCommitStale`。HEAD 前移已由既有 `HeadOID` 比對涵蓋（`commit.go:187-198`）——補一個 barrier 測試：Preview 後另 commit 一筆再 Confirm，必須 `ErrCommitStale`。
- [ ] **Step 5: `-race` 通過** → **Step 6: Commit**：`feat(plan): lineage 驗證＋Preview token 綁 analysis base（§3.0）`

### Task 10: Gate 2 policy

**Files:**
- Create: `internal/gatepolicy/gate2.go`（application package——import `gate`＋`plan`＋`spec`；`internal/gate` 不得 import 任何 domain package，避免 import cycle）
- Test: `internal/gatepolicy/gate2_test.go`

**Interfaces:**
- Consumes: Task 4 `gate.GatePolicy`／`DecisionInput`、Task 8 `plan.Plan`／`RiskPolicy`、Task 9 `VerifyLineage`。
- Produces:

```go
// PlanLoader：由 plan_commit（base_commit binding 的 OID）讀 committed plan 與 risk policy。
type PlanLoader interface {
	LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error)
}
func NewGate2Policy(loader PlanLoader, g plan.GitRunner, // package gatepolicy
	currentPlanManifest func() (string, error),
	currentSpecManifest func() (string, error),
	currentRiskPolicyDigest func() (string, error),
	currentPermissionManifest func() (string, error)) gate.GatePolicy

var _ gate.GatePolicy = (*Gate2Policy)(nil) // 編譯期斷言
```

- `ValidateRequest`：必填 bindings（spec_manifest、plan、base_commit、risk_policy、permission_manifest，格式照 Task 1 helper）＋ subject 前綴 `plan:` ＋ lineage：`analysis_base_commit..plan_commit` 限 `plan/**`（§3.0）。
- `BuildDecision`（approved）：`loader.LoadAt(plan_commit, plan_id)` → 驗 task 集合與 `input.RiskSelections` **完全一致**（缺／多／重複 task_id 拒）→ 重算 minimum、驗 planner 相符 → `selected>=minimum`、`selected<planner` 需 override_reason → 組**依 task_id 排序**的 `Metadata.RiskDecisions`。rejected：input 必須為空。
- `ReconcileBindings`：spec_manifest／plan／risk_policy／permission_manifest 持續重算（current* 函式），`base_commit` 只驗 commit 存在（`git cat-file -e <oid>^{commit}`），不隨 HEAD 前移失效（§3.9）。

- [ ] **Step 1: 寫失敗測試**（task 集合不一致、override 缺理由、selected<minimum、base_commit 不隨 HEAD 前移 STALE、plan manifest 變更 STALE——五個情境，pattern 同 Task 8 表格式）。
- [ ] **Step 2: 確認失敗** → **Step 3: 實作** → **Step 4: `-race` 通過** → **Step 5: Commit**：`feat(plan): Gate 2 policy——decision validator＋metadata 組裝＋STALE 分類（§3.3／§3.9）`

### Task 11: PlannerAssist（唯讀探索 one-shot）

**Files:**
- Modify: `internal/assist/oneshot.go`（新增 `ClaudePlannerArgs()`＋`NewClaudePlanner`／`NewCodexPlanner`——Codex 重用 readOnly sandbox 原樣）
- Modify: `app.go`（新增 `PlanAssist(provider, prompt string) (string, error)`，鏡射 `SpecAssist`（app.go:1144）：corr_id、EmitAssist、reclaim；前置檢查——**非 plan 路徑 dirty 即拒絕**（fail closed），並把 `analysis_base_commit=HEAD` 注入 prompt 前綴與回傳）
- Test: `internal/assist/oneshot_test.go`、`app_assist_test.go`

**Interfaces:**
- Produces:

```go
// ClaudePlannerArgs：唯讀工具白名單（pin 2.1.223；spec §9 待驗證假設——live probe 步驟見 Step 5）。
// enforcement 仍在 argv：白名單只含唯讀工具，無 Write/Edit/Bash → 無法變更 workspace。
func ClaudePlannerArgs() []string {
	return []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--tools", "Read,Glob,Grep"}
}
```

- [ ] **Step 1: 寫失敗測試**（argv 級斷言，同既有 `ClaudeAssistArgs` 測試 pattern）：白名單不含 `Write`／`Edit`／`Bash`；Codex planner turn params 仍為 `sandboxPolicy={readOnly,networkAccess:false}`＋`approvalPolicy=never`。
- [ ] **Step 2: 確認失敗** → **Step 3: 實作**（`claudeAssist` struct 加 `args []string` 欄位，兩組建構子共用 `Run`；`PlanAssist` 前置檢查兩項，任一不符即拒：(a) **存在 active Gate 1**（`gate.List()` 內 gate1 active entry，缺即回錯「無生效規格核可——先完成 Gate 1」，§5 輸入即其 spec_manifest）；(b) `git status --porcelain` 非 plan 路徑有輸出即回錯「workspace 有未提交的非 plan 變更——PlannerAssist 需在乾淨 code tree 上分析」）。
- [ ] **Step 4: `-race` 通過。**
- [ ] **Step 5: Live probe（人工步驟，記錄於 PR）**：`tools/` 的 pin claude 2.1.223 跑 `claude -p --tools "Read,Glob,Grep" "list files under internal/"` 確認唯讀工具可用且 Write 被拒；失敗則該 provider PlannerAssist fail closed＋收件匣（§3.8 條件 9），不改架構。**probe 結果未驗證前，PR 描述標註「Claude 白名單行為未驗證」。**
- [ ] **Step 6: Commit**：`feat(assist): PlannerAssist 唯讀 one-shot（Claude 白名單／Codex readOnly）（§5）`

### Task 12: app.go plan 綁定＋plan watcher

**Files:**
- Modify: `app.go`（鏡射 spec 綁定群 `app.go:751-1028`：`PlanList`／`PlanRead`／`PlanWrite`（expectedDigest 樂觀鎖同 SpecWrite）／`PreviewPlanCommit`／`ConfirmPlanCommit`／`SubmitPlanForApproval(planID string)`；`watchSpecTree` 模式複製為 `watchPlanTree`（`plan/` 遞迴、通知層觸發 `gate.Reconcile`））
- Test: `app_spec_test.go` 模式新增 `app_plan_test.go`

**Interfaces:**
- Produces（Wails 綁定簽名，前端 Task 13／15 消費）：`PlanList() ([]FileNode, error)`、`PlanRead(rel string) (SpecFile, error)`、`PlanWrite(rel, content, expectedDigest string) (string, error)`、`PreviewPlanCommit() (SpecCommitPreview, error)`、`ConfirmPlanCommit(tok spec.CommitToken, message string) error`、`SubmitPlanForApproval(planID string) (string, error)`、**`GateDecisionContext(approvalID string) (GateDecisionContextDTO, error)`**。
- **Committed context 閉環（本 task 凍結）**：
  - `SubmitPlanForApproval`：**前置——存在 active Gate 1**，且 Gate 2 的 `spec_manifest` binding 值**直接取自該 active Gate 1 record 的 binding**（非重算 worktree；兩者不一致的情況由 Gate 1 STALE 機制而非 Gate 2 處理）→ dirty-tree 拒核（`BuildCommittedSnapshot` with PlanScope）→ 讀 committed plan → scenario 集合取自 **active Gate 1 綁定的 committed spec tree**（`ReadScopedHeadTree` at Gate 1 `base_commit`，解析 `spec/features/**` 的 Scenario 上一行 `@` tag 為 ID；無 tag 的 scenario 不可被 plan 引用）→ `plan.Validate`（fail 即拒）→ lineage 驗證 → 組五筆 bindings → `Submit("gate2", "plan:"+planID, bindings)`。
  - `GateDecisionContext(approvalID)`：後端從該 pending request 的 `base_commit`（plan_commit）用 `PlanLoader.LoadAt` 讀 **committed plan**，回傳 `{Tasks: [{task_id, title, minimum_risk_tier, planner_risk_tier}]}` 供 Gate 2 卡片渲染 risk 列。**UI 禁止以目前 worktree plan 推導 minimum／planner**（committed 才是核可對象）。

- [ ] **Step 1: 失敗測試**：無 active Gate 1 時送核被拒；dirty plan tree 送核被拒；成功送核後 `GateList` 出現 `gate:"gate2", subject:"plan:P1"` pending 項且 spec_manifest binding 等於 Gate 1 所綁值；`GateDecisionContext` 在送核後**修改 worktree plan** 仍回傳 committed 值（不受 worktree 影響）；**mutation-before-decide（Gate 2）**——送核後、核可前修改並 commit 新版 plan（plan manifest 變）→ `GateDecide` 被 current-binding validation 拒絕（Task 5）。
- [ ] **Step 2–4: TDD 循環＋全套 `-race`。**
- [ ] **Step 5: Commit**：`feat(app): plan workspace 綁定＋watcher＋Gate 2 送核（§7 Stage B）`

### Task 13: 前端 plan store＋PlanWorkspace

**Files:**
- Create: `frontend/src/stores/plan.ts`（鏡射 `assist.ts`＋spec 檔案清單模式：檔案清單、目前檔、草稿區（PlanAssist 輸出 by corr_id）、驗證錯誤陣列）
- Create: `frontend/src/components/PlanWorkspace.vue`（鏡射 `SpecWorkspace.vue`：CodeMirror（yaml 模式不裝新套件，用 plain text＋既有樣式）、AI 草稿區＋「套用草稿」、Preview／Confirm commit 對話、送核按鈕）
- Modify: `frontend/src/App.vue`（新分頁 tab）、`frontend/src/types.ts`、`frontend/src/i18n/locales/zh-TW.ts`＋`en.ts`（`planWorkspace.*` keys）
- Test: `frontend/src/components/PlanWorkspace.test.ts`

**Interfaces:**
- Consumes: Task 12 綁定；`assist` 事件 lane（purpose=`plan_draft` 路由進 plan 草稿區，沿 `gateRouting.ts` purpose 分流模式）。

- [ ] **Step 1: 失敗測試**（vitest）：送出 PlanAssist 後草稿區顯示 loading→輸出；「套用草稿」呼叫 `PlanWrite`；驗證錯誤 inline 顯示（mock 綁定回收集式錯誤）。
- [ ] **Step 2–4: TDD 循環；`npm --prefix frontend run test`＋`run build` 綠。**
- [ ] **Step 5: Commit**：`feat(frontend): PlanWorkspace＋plan store＋i18n（§6）`

### Task 14: DagPane（plan→mermaid projection）

**Files:**
- Create: `frontend/src/lib/planDag.ts`＋`planDag.test.ts`
- Create: `frontend/src/components/DagPane.vue`（重用 `DiagramPane.vue` 的 mermaid strict 渲染與檔案變更重渲染模式）
- Modify: `App.vue`（表示圖層加「任務 DAG」tab）、locales

**Interfaces:**
- Produces: `planToMermaid(p: PlanDoc): string` 純函式——輸出 `flowchart TD`，節點 `T1["T1 · 標題 · medium"]`、邊 `dep --> task`；節點點選由 DagPane emit `select-task(taskId)`，App 導航至 GateConsole 對應項。

- [ ] **Step 1: 失敗測試**：兩 task 一依賴 → mermaid 字串含 `T1 --> T2`；task 標題含 `"` 時跳脫不破圖。
- [ ] **Step 2–4: TDD＋vitest／build 綠。**
- [ ] **Step 5: Commit**：`feat(frontend): 任務 DAG projection 圖層（§6）`

### Task 15: GateConsole gate2 卡片＋risk decision 輸入

**Files:**
- Modify: `frontend/src/components/GateConsole.vue`、`frontend/src/stores/gate.ts`（entry 加 `gate`／`subject`／`bindings role` 顯示）、`frontend/src/types.ts`、locales
- Test: `frontend/src/components/GateConsole.test.ts`

**Interfaces:**
- Consumes: Task 5 `GateDecide(approvalID, decision, reason, riskSelections)` 綁定；**Task 12 `GateDecisionContext(approvalID)`**（committed plan 的 per-task minimum／planner——UI 不讀 worktree plan）。
- Produces: gate2 pending 卡片展開 per-task risk 選擇列（minimum／planner 唯讀顯示、selected 下拉、selected<planner 時 override_reason 必填欄）；核可送出組 `riskSelections`。

- [ ] **Step 1: 失敗測試**：selected<planner 未填理由時核可按鈕 disabled；rejected 只需 reason；gate1 卡片不顯示 risk 列（回歸）。
- [ ] **Step 2–4: TDD＋vitest／build 綠。**
- [ ] **Step 5: Commit**：`feat(frontend): GateConsole gate2 卡片＋risk decision（§3.3／§6）`

**Phase 2 收尾 gate**：全套四項驗證＋實機 `wails dev` 走一輪 Stage B（PlannerAssist→編修→commit→送核→核可），截圖存 `docs/spikes/evidence/`。

---

## Phase 3 — Oracle-surface＋Evidence Runner＋TCA

### Task 16: oracle-surface 宣告＋digest

**Files:**
- Create: `internal/evidence/oracle.go`＋`oracle_test.go`

**Interfaces:**
- Produces:

```go
type OracleDecl struct {
	Version  int      `yaml:"version"`
	Patterns []string `yaml:"patterns"` // 相對路徑 pattern，語意同 spec.Scope（前綴＋精確檔比對）
}
func ParseOracleDecl(b []byte) (OracleDecl, error)          // 位置：plan/oracle-surface.yaml（§3.6）
func (d OracleDecl) Match(rel string) bool
func (d OracleDecl) Scope() spec.Scope                       // 轉 Scope 以重用 canonical manifest／ReadScopedHeadTree
func OracleDigestAt(r *spec.GitRepo, d OracleDecl, commitOID string) (string, error) // 在指定 commit 的 tree 上算 digest
```

- [ ] **Step 1: 失敗測試**：**pattern 語意凍結——M3a 只支援 `dir/**`（目錄前綴）與精確檔路徑兩型**，其餘（中段 wildcard 如 `internal/*/testdata/**`、單段 `*.go`）在 `ParseOracleDecl` 即拒絕並回明確錯誤。測試：宣告 `["tests/**", "internal/evidence/testdata/**", "scripts/run_oracle.sh"]` 合法；`["internal/*/testdata/**"]` 拒絕；同一宣告在兩個 commit（測試檔內容不同）產生不同 digest；未知欄位拒絕。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(evidence): oracle-surface 宣告＋commit-tree digest（§3.6）`

### Task 17: CAS store＋mutation 登記

**Files:**
- Create: `internal/evidence/cas.go`＋`cas_test.go`、`internal/evidence/types.go`

**Interfaces:**
- Produces:

```go
// PutCAS：§3.7 凍結順序——同目錄 temp → 寫入＋算 digest → f.Sync()+Close →
// os.Rename 至 <dir>/sha256/<hex> → 開 dir fd Sync → 回 digest。
func PutCAS(dir string, data []byte) (digest string, path string, err error)
func OpenCAS(dir string, digest string) ([]byte, error) // 讀回並重算 digest 驗證（內容定址驗證，§3.9）
func CleanOrphanTemps(dir string) (removed []string, err error) // 啟動時清 *.tmp-*
type Mutation struct {
	MutationID string `json:"mutation_id"` // ULID
	TaskRef    string `json:"task_ref"`    // "P1/T1"
	Digest     string `json:"digest"`      // patch 內容 CAS digest
	CreatedAt  string `json:"created_at"`
}
```

- [ ] **Step 1: 失敗測試**：`PutCAS` 後檔案在 `sha256/<hex>` 且無 temp 殘留；`OpenCAS` 對被竄改的檔案（直接改 bytes）回錯；`CleanOrphanTemps` 只清 temp 不動 CAS 檔；**spec §3.7 全部 crash boundary 逐一注入＋重啟驗證**（測試以「手工佈置該時點的磁碟狀態→跑恢復→驗不變量」模擬，不變量＝journal 永不指向不存在的檔案、恢復冪等）：(a) file sync 後／rename 前——只有 temp 檔、無 CAS 檔、無 journal 行 → 恢復清 temp、無副作用；(b) rename 後／directory sync 前——CAS 檔在、無 journal 行 → 恢復保留 CAS 檔（無 journal 引用之 CAS 為無害 orphan，不清）、`PutCAS` 同內容重放冪等（同 digest 路徑）；(c) directory sync 後／journal append 前——同 (b) 狀態＋驗重放後 journal append 成功且引用存在的檔案。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(evidence): CAS store（temp→sync→rename→dirsync）＋mutation 登記（§3.7）`

### Task 18: detached worktree 生命週期

**Files:**
- Create: `internal/evidence/worktree.go`＋`worktree_test.go`（真 git repo 測試，`t.TempDir()`）

**Interfaces:**
- Produces:

```go
// Registry：append-only JSONL（internal/journal），worktree 生命週期以 durable transition 表達。
// **順序凍結：先推導路徑並持久化 intent，才允許任何檔案系統建立**——路徑為
// filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)（evidenceID=ULID 保證唯一，
// 不用 MkdirTemp：MkdirTemp 會先建目錄，intent 前 crash 即產生 registry 無法辨識的目錄）：
//   {"_type":"wt_intent","evidence_id":..,"dir":..,"at":..}   // ① 目錄尚不存在時先寫（durable）
//   （② git worktree add --detach <dir> <commit> 建立目錄）
//   {"_type":"wt_active","evidence_id":..,"at":..}            // ③ add 成功後
//   {"_type":"wt_removed","evidence_id":..,"at":..}           // ④ remove+prune 成功後
// crash 恢復依 projection：intent 無 active → 目錄可能不存在或半建，冪等 remove+prune＋標 removed；
// active 無 removed 且非 live → orphan，remove+prune＋標 removed。§4-4／§4-6。
type Worktree struct{ Dir string; EvidenceID string }
func NewWorktree(repoRoot, commitOID, registryPath, evidenceID string) (*Worktree, error) // intent→add→active
// ApplyPatch：先 `git -C dir apply --check`（驗可套用），路徑列舉用
// `git apply --numstat -z`（-z 凍結解析；rename/copy 條目帶 old＋new 兩路徑，**兩側皆回傳**
// 供呼叫端過 oracle 檢查——單驗一側可被 oracle→非 oracle rename 規避），
// 再 `git -C dir apply`；patch bytes 一律經受控 stdin 傳入，不落 shell 字串。
func (w *Worktree) ApplyPatch(patch []byte) (touched []string, err error)
func (w *Worktree) Remove(repoRoot, registryPath string) error // remove --force + prune + 標 removed
func CleanupOrphans(repoRoot, registryPath string, liveIDs map[string]bool) error
```

- [ ] **Step 1: 失敗測試**：worktree checkout 出指定 commit 的內容（非 HEAD）；`ApplyPatch` 對套不上的 patch 回錯且 worktree 未半套用（--check 先行）；**crash window 逐一測**：(a0) intent 已寫、目錄從未建立（add 前 crash）→ `CleanupOrphans` 冪等標 removed 不報錯；(a) intent 已寫＋目錄半建（add 中 crash：手動 mkdir）→ 目錄清除＋標 removed；(b) intent+active 無 removed 且非 live → 同上；(c) 已 removed 的不重複處理；全部情境後 `git worktree list` 無殭屍、系統暫存目錄無 `wb-evidence-*` 殘留。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(evidence): detached worktree 生命週期＋orphan 清理（§4）`

### Task 19: runner 執行＋matcher＋結果分類

**Files:**
- Create: `internal/evidence/runner.go`、`matcher.go`＋`runner_test.go`、`matcher_test.go`

**Interfaces:**
- Consumes: Task 16–18；`internal/proc`（process group TERM→KILL）。
- Produces:

```go
type EvidenceRun struct { // §3.7 凍結 schema
	EvidenceID          string        `json:"evidence_id"`
	Kind                string        `json:"kind"` // expected_red | negative_control
	Source              string        `json:"source"` // local_app
	BaseCommit          string        `json:"base_commit"`
	TestCommit          string        `json:"test_commit"`
	OracleSurfaceDigest string        `json:"oracle_surface_digest"`
	MutationDigest      string        `json:"mutation_digest,omitempty"`
	Command             plan.Command  `json:"command"`
	CWD                 string        `json:"cwd"` // 邏輯識別："worktree:<evidence_id>"
	StartedAt, FinishedAt string      `json:"started_at","finished_at"`
	ExitCode            int           `json:"exit_code"`
	ExpectedFailure     plan.ExpectedFailure `json:"expected_failure"`
	ObservedFailure     string        `json:"observed_failure"`
	StdoutDigest, StderrDigest string `json:"stdout_digest","stderr_digest"`
	RecordingRef        string        `json:"recording_ref"`
	RunnerVersion       string        `json:"runner_version"`
	Result              string        `json:"result"` // passed | failed | error
}
// RunSpec 只帶 identity 與輸入 artifact；**contract descriptor 與 oracle 宣告一律由 runner
// 從 plan_commit 的 committed plan 載入（ContextLoader），digest 由 runner 自算**——不接受
// 呼叫端傳入 OracleDigest／Command（防止與核可內容不一致）。
type ContextLoader interface {
	LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error)   // 同 Task 10 PlanLoader
	LoadOracleAt(commitOID string) (OracleDecl, error)                     // plan/oracle-surface.yaml at commit
}
type RunSpec struct {
	Kind, PlanID, TaskID       string
	PlanCommit, TestCommit     string
	MutationPatch              []byte // negative_control 必填
	Timeout                    time.Duration // 預設 10m
	OutputLimit                int           // 預設 4MB；超限＝result:error（§4-2）
}
func Run(ctx context.Context, repoRoot, casDir, registryPath string, ld ContextLoader, rs RunSpec, ulid func() string, now func() string) (EvidenceRun, error)
// EvidenceRunDigest：EvidenceRun canonical JSON（struct 欄位序）SHA-256——evidence_run
// binding 的 digest 與 §3.9 內容定址驗證的凍結演算法（同 gate.RecordDigest 慣例）。
func EvidenceRunDigest(run EvidenceRun) (string, error)
// matcher.go
// ClassifyExpectedRed：exit!=0 且 stdout+stderr 含 Matcher 且全部 TestIDs 出現 → passed；
// exit==0 → failed（測試沒紅）；exit!=0 但特徵不符（如編譯錯誤）→ error（§3.7）。
func ClassifyExpectedRed(exitCode int, output []byte, ef plan.ExpectedFailure) (result, observed string)
// ClassifyNegativeControl：同 expected-red 判準（mutation 必須被同組測試抓到）。
func ClassifyNegativeControl(exitCode int, output []byte, ef plan.ExpectedFailure) (result, observed string)
```

- Run 流程（§3.4／§4）：`ld.LoadAt(PlanCommit, PlanID)` 取 task 的 approved `TestContract`＋`ld.LoadOracleAt(PlanCommit)` 取 oracle 宣告 → 驗 test_commit 以 plan_commit 為祖先＋range 限 oracle 路徑（`plan.VerifyLineage` with `OracleDecl.Match`，rename 兩側皆驗）→ runner 於 test_commit tree 重算 `OracleSurfaceDigest` → 建 worktree（checkout test_commit）→ negative_control 時 ApplyPatch（`--check`＋`--numstat -z` 回傳的 touched 路徑——含 rename old／new 兩側——**任一命中 oracle 即拒**）→ `exec.CommandContext` 以 approved `Command` 結構化執行（`Dir=worktree`、`Env` 白名單：PATH/HOME/GOCACHE 等，去除 provider token 類）＋`proc` 式 process group、timeout kill → stdout/stderr tee 到 CAS（超限＝`result:error`）→ Classify → 填 `EvidenceRun` → worktree Remove。

- [ ] **Step 1: matcher 失敗測試**（表格式：紅燈特徵符合→passed；exit 0→failed；編譯錯誤輸出（無 matcher 特徵）→error；輸出超限→error）。
- [ ] **Step 2: runner 整合失敗測試**（fixture repo：C0 有 `run_test.sh`（approved plan 的 `Command{Executable:"sh",Argv:["run_test.sh"]}`）輸出 `FAIL: TestX` exit 1 → expected_red passed；timeout fixture（`sleep 60`、Timeout=1s）→ error 且 **worktree 目錄與 process group 皆零殘留**（`git worktree list` 乾淨、pgid kill 驗證同 proc 測試慣例）；**oracle→非 oracle rename 的 test_commit 拒絕**（Task 9 對偶）；**mutation patch rename 雙向拒絕**——patch 含 oracle→非 oracle rename 與非 oracle→oracle rename 各一測，皆因 touched 兩側檢查被拒；**EvidenceRunDigest 竄改測試**——改 record 任一欄位 digest 必變）。
- [ ] **Step 3–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(evidence): runner＋matcher＋結果分類（timeout／輸出超限 fail closed）（§3.7／§4）`

### Task 20: evidence journal＋恰一次 finalize＋app.go 綁定

**Files:**
- Create: `internal/evidence/journal.go`（用 Task 6 `internal/journal`；record＝`EvidenceRun` 或 `Mutation`，`_type` 區分）＋`journal_test.go`
- Modify: `app.go`：`RegisterMutation(taskRef string, patch string) (string, error)`、`RunEvidence(planID, taskID, testCommit, kind, mutationID string) (string, error)`（同步跑，回 evidence_id；執行中經 `EmitWorkspace` 發 `evidence_run` 進度事件——沿 workspace lane，additive）、`EvidenceGet(evidenceID string) (evidence.EvidenceRun, error)`
- Test: `app_evidence_test.go`

**Interfaces:**
- Produces: journal append 順序固定＝**CAS artifact 全部落盤後才 append**（Task 17 保證）＋「同一 evidence run 恰一次 finalize」——evidence_id 唯一鍵重複 append 拒絕（§4-5）。
- **App lifecycle ownership（本 task 凍結；不依賴 Task 24 的 workflowMu——本 task 自帶 registry mutex，Task 24 再整合 lock ordering）**：`RunEvidence` 進入即 `beginAppTxn()`（沿 `app.go:136` 慣例，shutdown 拒新工作）；執行 context 掛在 app shutdown context 之下；app 持 **active run registry**（`a.evidenceMu sync.Mutex`＋`map[evidenceID]context.CancelFunc`），finalize（journal append）與 registry 移除為同一臨界區——恰一次語意由此保證。**Shutdown 取消順序凍結**（鏡射 `reclaimAssists`，`app.go:1242`）：`shutdown()` 先呼叫 `reclaimEvidenceRuns()`（逐一 cancel active runs——runner 的 ctx cancel 路徑收拾 process group 與 worktree）**再** `a.inflight.Wait()` bounded wait——否則長時 runner 會讓 `inflight.Wait()` 卡死；逾時 forcedShutdown 路徑由 Task 18 的 `CleanupOrphans` 於下次啟動收拾。

- [ ] **Step 1: 失敗測試**：同 evidence_id append 兩次第二次回錯；journal replay 後 `EvidenceGet` 重建完整 record；`RunEvidence` 在 `plan_commit..test_commit` 混入非 oracle 路徑時拒絕；**shutdown channel-barrier production 測試**——fixture 命令為「寫入 started 檔後長眠」，測試等 started 檔出現（channel／輪詢檔案，非 time.Sleep 猜時間）後呼叫 `shutdown()`：斷言 `reclaimEvidenceRuns` 先於 `inflight.Wait` 完成（shutdown 在 bounded 時間內返回）、RunEvidence 以 error 收場、無 finalize 半寫入、process group 零殘留、下次啟動 `CleanupOrphans` 清 worktree。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(evidence): journal＋恰一次 finalize＋app 綁定（§4-5）`

### Task 21: TCA policy＋SubmitTestContract

**Files:**
- Create: `internal/gatepolicy/tca.go`＋`tca_test.go`（同 Task 10：gatepolicy import gate＋plan＋evidence，gate 維持技術中立）
- Modify: `app.go`：`SubmitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID string) (string, error)`——組七筆 bindings 後 `Submit("test_contract_approval", "task:"+planID+"/"+taskID, bindings)`

**Interfaces:**
- Consumes: Task 2 `RecordDigest`、Task 10 `PlanLoader`、Task 20 `EvidenceGet`／`OpenCAS`。
- Produces:

```go
// EvidenceStore／GateReader：TCA policy 的查詢 ports。
type EvidenceStore interface {
	Get(evidenceID string) (evidence.EvidenceRun, error)
	MutationDigest(mutationID string) (string, error)
}
type GateReader interface { // 供 gate2_approval resolver 查 projection＋原始 record（package gatepolicy：gate 型別需限定）
	Lookup(approvalID string) (rec *gate.ApprovalRecord, state gate.State, err error)
}
func NewTCAPolicy(ev EvidenceStore, gates GateReader, loader PlanLoader, g plan.GitRunner) gate.GatePolicy

var _ gate.GatePolicy = (*TCAPolicy)(nil) // 編譯期斷言（Gate2Policy 同，見 Task 10）
```

- `ValidateRequest` bindings 必填：`gate2_approval`（ref=`approval:<ULID>`、digest=sha256）＋`base_commit`＋`oracle_surface`（ref=git OID）＋`evidence_run`×2（role 區分）＋`mutation`。
- `BuildDecision`（approved）跑 §3.4 七條一致性 validator（role↔kind、雙 passed、三欄 snapshot 一致、`oracle_surface.ref==test_commit`、mutation digest 對齊、expected-red 無 mutation、descriptor 從 `gate2_approval` 所綁 record 的 `base_commit`（plan_commit）用 `loader.LoadAt` 讀——**禁讀 worktree**）。
- `ReconcileBindings`：`gate2_approval`＝digest 重算（`RecordDigest`）＋projection active 雙驗（§3.4）；`evidence_run`＝`EvidenceRunDigest` 重算（journal record）＋record 引用的 CAS artifact 重讀驗證；`mutation`＝CAS 重讀；`oracle_surface`＝**以核可時的 OracleDecl（自 plan_commit 載入）對目前 workspace 內容重算**（§3.9 持續重算——目前 oracle-surface 檔案被改即 STALE；重算沿 `BuildCurrentManifest` 的 sliding-window 慣例，讀取錯誤／併發修改回 error fail closed、不寫永久 STALE）；`base_commit`＝存在性。**注意：test_commit tree 上的重算只用於 runner 執行時（Task 19），resolver 的權威是目前內容**——否則 oracle 變更永不 STALE，與驗收 A5 矛盾。

- [ ] **Step 1: 失敗測試**（表格式覆蓋：兩筆 evidence 的 test_commit 不同→拒；expected-red 帶 mutation→拒；gate2 STALE 後 reconcile→TCA stale cause；record 竄改（`EvidenceRunDigest` 不符）→stale cause；**目前 oracle-surface 檔案修改→stale cause（A5 對應）**；oracle 重算讀取錯誤→error 不寫 stale；descriptor 與 committed plan 不符→拒；**mutation-before-decide（TCA）**——TCA 送核後、核可前 supersede 其 gate2_approval → `GateDecide` 被 current-binding validation 拒絕（Task 5））。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(gate): TCA policy——七條一致性＋gate2_approval 雙驗 resolver（§3.4／§3.9）`

### Task 22: TCA workspace（Stage C 操作入口）＋console UI＋evidence 詳情

**Files:**
- Create: `frontend/src/components/TcaWorkspace.vue`＋`TcaWorkspace.test.ts`——**Stage C 全程可從 app 完成的操作面**（A4／SC4 方向）：從 active Gate 2 的 committed plan 列出 tasks（`GateDecisionContext`）→ 每 task 一列：test_commit 輸入（下拉近期 commits＋手動輸入，送出前呼叫後端 lineage 預檢）、mutation patch 貼上→`RegisterMutation`、「跑 expected-red」「跑 negative-control」按鈕（`RunEvidence`，進度顯示 workspace lane 的 `evidence_run` 事件、完成顯示 result 徽章）、兩筆 evidence 皆 passed 後啟用「送核 TCA」→`SubmitTestContract`
- Create: `frontend/src/stores/evidence.ts`（mutation／evidence run 狀態，by plan/task）
- Create: `frontend/src/components/EvidenceDetail.vue`（EvidenceGet 顯示完整 record＋recording ref）
- Modify: `app.go`——UI 依賴的兩個後端綁定於本 task 新增：`ValidateTestCommit(planID, taskID, testCommit string) error`（lineage 預檢：呼叫 Task 9 `VerifyLineage`＋oracle 路徑檢查，只驗不執行）、`EvidenceCommitCandidates(planID string) ([]CommitInfo, error)`（`git log --format=%H%x00%s -n 20 <plan_commit>..HEAD`，`CommitInfo{OID, Subject string}`——近期 commit 下拉的資料源）
- Modify: `frontend/src/components/GateConsole.vue`（tca 卡片：subject、gate2_approval 連結、兩筆 evidence 摘要（kind／result／test_commit 短 SHA→點開詳情）、mutation digest）、`frontend/src/stores/gate.ts`、`types.ts`、`App.vue`（TcaWorkspace tab）、locales
- Test: `GateConsole.test.ts` 擴充＋`app_evidence_test.go` 補 production adapter 測試（Go 側：`RegisterMutation`／`RunEvidence`／`EvidenceGet`／`SubmitTestContract`／`ValidateTestCommit`／`EvidenceCommitCandidates` 走一輪）＋**`frontend/src/lib/bindings.test.ts`**（TS 側鎖定跨 Wails 參數形狀：mock wailsjs module，斷言每個 adapter 呼叫逐參數轉發、參數順序與名稱與 Go 綁定一致——Go test 驗不到 TS adapter 的轉發正確性）

- [ ] **Step 1: 失敗測試**：TcaWorkspace 對兩筆 evidence 未齊時「送核」disabled；result=error 顯示錯誤標示＋重跑按鈕；tca 卡片渲染兩筆 evidence role；production adapter 測試走 mutation→兩 run→送核全流程。
- [ ] **Step 2–4: TDD＋vitest／build＋`go test -race`。** → **Step 5: Commit**：`feat(frontend): TCA workspace（Stage C 入口）＋卡片＋evidence 詳情（§6／A4）`

**Phase 3 收尾 gate**：全套四項驗證＋實機走 Stage C 前段（宣告已在 Stage B 核可 → test commit → mutation → 兩筆 evidence → TCA 送核核可）。

---

## Phase 4 — 升級收件匣＋閉環 barrier＋E2E

### Task 23: `internal/escalation` journal＋projection

**Files:**
- Create: `internal/escalation/types.go`、`journal.go`（用 `internal/journal`）、`project.go`＋測試

**Interfaces:**
- Produces:

```go
type Item struct {
	Type         string `json:"_type"` // "escalation_item"
	EscalationID string `json:"escalation_id"` // ULID
	ConditionKey string `json:"condition_key"` // 系統項去重鍵，如 "stale:gate2:P1"；手動項空
	Occurrence   int    `json:"occurrence"`    // 同 condition 第 N 次（§3.8）
	Source       string `json:"source"`        // system | manual
	SourceRef    string `json:"source_ref"`    // plan_id/task_id/approval_id/evidence_id/event_id（手動必填）
	BlockScope   string `json:"block_scope"`   // workspace | gate2:<plan_id> | tca:<plan_id>/<task_id> | evidence:<id> | ""（非阻擋）
	Hard         bool   `json:"hard"`          // 硬性項：UI 不可手動 resolve（§3.8）
	Summary      string `json:"summary"`
	CreatedAt    string `json:"created_at"`
}
type ItemTransition struct {
	Type         string `json:"_type"` // "escalation_transition"
	EscalationID string `json:"escalation_id"`
	To           string `json:"to"` // acknowledged | resolved
	Resolution   string `json:"resolution,omitempty"` // resolved 必填；accepted_risk 等
	Reason       string `json:"reason,omitempty"`
	Actor        string `json:"actor,omitempty"`
	At           string `json:"at"`
}
type Entry struct{ Item Item; State string } // open|acknowledged|resolved
func Project(lines [][]byte) ([]Entry, error)
func BlockingFor(entries []Entry, scope string) []Entry // open/acknowledged 且 BlockScope 覆蓋 scope（workspace 覆蓋全部）
func OpenKeyed(entries []Entry, conditionKey string) *Entry // 未 resolved 的同 key 項（去重用）
```

- [ ] **Step 1: 失敗測試**：acknowledged 仍在 `BlockingFor` 內（不解除阻擋）；resolved 後同 conditionKey 再建 → `Occurrence==2` 新項（不因舊項 resolved 而漏報）；`OpenKeyed` 只對未 resolved 去重。
- [ ] **Step 2–4: TDD＋`-race`。** → **Step 5: Commit**：`feat(escalation): journal＋projection＋condition key／occurrence（§3.8）`

### Task 24: workflow mutex barrier＋自動來源接線

**Files:**
- Create: `internal/escalation/service.go`（`Service`：`CreateSystem(conditionKey, blockScope, hard bool, summary, sourceRef)`（OpenKeyed 去重、occurrence 遞增）、`CreateManual(sourceRef, blockScope, summary)`（sourceRef 必填）、`Ack`、`Resolve(id, resolution, reason, actor)`——**hard 項的 Resolve 只接受 `actor=="system"`**、`ResolveByKey(conditionKey, evidenceRef)`（系統重驗通過後解除））。**重入規約**：escalation Service 本身不取 workflowMu；app 層提供成對入口——public `EscalationCreate`／`EscalationResolve` 等先取 `workflowMu` 再呼叫內部 `createSystemLocked`／`resolveByKeyLocked`，而**已持有 workflowMu 的路徑（GateDecide 編排、Reconcile 的 stale 接線、evidence finalize）只准呼叫 `*Locked` 變體**——否則 GateDecide→Reconcile→emitter→public API 會對同一 mutex 重入死鎖。
- Modify: `app.go`：新增 `workflowMu sync.Mutex`；`GateDecide` 依 spec §3.10 凍結順序編排（用 Task 5 的 Prepare／Commit 拆分）：

```go
// GateDecide（app 層編排；順序 = spec §3.10：reconcile → validator → [stale 修復解除] → blocker → append）
a.workflowMu.Lock(); defer a.workflowMu.Unlock()
if err := svc.Reconcile(); err != nil { return err }                    // 1. reconcile bindings
prepared, err := svc.PrepareDecision(id, decision, reason, appr, input) // 2. 硬性 validator＋approved 的
if err != nil { return err }                                            //    current-binding validation
scope := scopeForSubject(prepared.Record.Gate, prepared.Record.Subject)
if prepared.Record.Decision == "approved" {                             // 2b. 修復解除（凍結時點）：
	// current-binding validation 已通過 ＝ 同 subject 的 stale 條件已被此修正版修復；
	// 在 blocker 檢查前系統解除舊 stale blocker。resolve 寫入失敗 → 拒絕核可（fail closed）。
	key := "stale:" + prepared.Record.Gate + ":" + prepared.Record.Subject
	if err := a.escResolveByKeyLocked(key, "superseded-by:"+prepared.Record.ApprovalID); err != nil {
		return err
	}
}
if items := escBlockingFor(scope); len(items) > 0 {                     // 3. blocking escalation
	return fmt.Errorf("blocked by %d escalation item(s): %s", len(items), summarize(items))
}
if a.decideBarrierHook != nil { a.decideBarrierHook() }                 // 測試 seam（見 Step 1）
return svc.CommitDecision(prepared)                                     // 4. append
```

（`escResolveByKeyLocked` 對不存在的 key 為 no-op nil——多數核可沒有舊 stale blocker。）

  `RunEvidence` 的 finalize、escalation Create／Ack／Resolve、watcher 觸發的 `Reconcile` 全部先取 `workflowMu`（lock ordering：workflowMu → gate journal → escalation journal）。
- **自動來源接線（spec §3.8 九條逐一對應，缺一即 spec 缺口；持 workflowMu 的路徑一律走 `createSystemLocked`）**：(1) plan.Validate 的 risk 分類失敗（minimum 無法重算）→ `createSystemLocked("risk-unclassifiable:<plan_id>", "gate2:<plan_id>", hard=true)`；(2) 送核缺必要 binding（ValidateRequest 失敗且來源為系統組裝）→ `"missing-binding:<gate>:<subject>"`；(3)(4) `Reconcile` 產生的 stale → `"stale:<gate>:<subject>"`（hard=true，scope 對應 gate2:<plan_id>／tca:<plan_id>/<task_id>）；(5) runner 逾時／環境錯誤／輸出超限 → `"evidence-error:<plan_id>/<task_id>/<kind>"`（**key 綁 plan/task/kind 而非 evidence_id**——新 run 成功即可 `ResolveByKey` 舊項，A8 才可實現；hard=false）；(6) expected-red 錯誤原因（result=error 且非環境類）→ 同 (5) key；(7) negative-control 未抓到（result=failed）→ `"negative-control-missed:<plan_id>/<task_id>"`；(8) journal degraded／read error → `"journal-degraded:<which>"`（workspace、hard=true）；(9) PlannerAssist enforcement 失敗 → `"planner-enforcement:<provider>"`。
- **啟動／讀取補建（§3.8）**：`Reconcile` 與 app startup 依權威狀態掃描——已 stale 的 active 核可、degraded journal 等若無對應未 resolved 項（`OpenKeyed`）即補建。
- **九條來源的權威修復條件（缺一即該 blocker 永久卡死替代核可，逐條凍結）**：(1) risk-unclassifiable → 新版 plan commit 後 `plan.Validate` 通過即 `resolveByKeyLocked`；(2) missing-binding → 同 subject 新 request `ValidateRequest` 通過；(3)(4) **stale:<gate>:<subject> → 修正版核可時解除（時點凍結於 GateDecide 編排 2b）**：approved 的 `PrepareDecision`（含 current-binding validation）成功後、blocker 檢查前，`resolveByKeyLocked` 解除同 subject 的舊 stale blocker，寫入失敗即拒絕核可——stale record 本身是終態，修復載體是修正版的核可流程，否則新版永遠被舊 blocker 擋住；(5)(6) evidence-error → 同 key 新 run result=passed（A8）；(7) negative-control-missed → 同上；(8) journal-degraded → 重啟後 journal 開啟成功且 tail 修復完成；(9) planner-enforcement → 該 provider probe 重新通過。每一條由對應的權威路徑（submit／validate／run finalize／startup）在持有 workflowMu 時呼叫 `resolveByKeyLocked`。
- Test: `app_escalation_test.go`

**Interfaces:**
- Consumes: Task 5 Service、Task 23。
- Produces: `EscalationList() ([]escalation.Entry, error)`、`EscalationCreate(sourceRef, blockScope, summary string) (string, error)`、`EscalationAck(id string) error`、`EscalationResolve(id, resolution, reason string) error`（Wails 綁定；hard 項回錯「僅系統可 resolve」）。

- [ ] **Step 1: 失敗測試**（核心 barrier）

```go
func TestGateDecideBlockedByEscalation(t *testing.T) {
	a := newTestApp(t) // 已有 gate2 pending P1（單 task T1，minimum=planner=medium）
	_, _ = a.EscalationCreate("plan:P1", "gate2:P1", "人工阻擋")
	err := a.GateDecide(pendingID, "approved", "",
		[]gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}) // 有效 risk 輸入——
	// 用 nil 會先死在 risk validator，測不到 blocker（假陽性）
	if err == nil || !strings.Contains(err.Error(), "blocked by") {
		t.Fatalf("blocking escalation must veto approval, got %v", err) // §3.10
	}
}

func TestGateDecideBarrierWindowInjected(t *testing.T) {
	// 可重現的 injected barrier（無 time.Sleep、無排程競態）：三個 test seam——
	// a.decideBarrierHook：GateDecide 在 blocker 檢查後、CommitDecision 前（Task 24 編排碼）；
	// a.onWorkflowMuAttempt：public EscalationCreate 於 mutex.Lock() **前**呼叫；
	// a.onWorkflowMuAcquired：public EscalationCreate 取得 mutex 後、寫入前呼叫。
	// 順序控制：先啟動 Decide 等它進窗口（持有 mutex），才啟動 blocker 並等 attempted
	// 訊號——保證 blocker 的 Lock() 嘗試發生在 release 之前、必然排在 decide 之後。
	a := newTestApp(t)
	inWindow := make(chan struct{})   // decide 已過 blocker 檢查、持有 mutex
	release := make(chan struct{})    // 放行 CommitDecision
	attempted := make(chan struct{})  // blocker 已到達 Lock() 前
	created := make(chan error, 1)
	var stateSeenByBlocker gate.State
	a.onWorkflowMuAttempt = func() { close(attempted) }
	a.onWorkflowMuAcquired = func() { // blocker 真正進入臨界區的時點
		entries, _ := a.gateSvc.List()
		stateSeenByBlocker = stateOf(entries, pendingID)
	}
	a.decideBarrierHook = func() { close(inWindow); <-release }
	decideDone := make(chan error, 1)
	go func() {
		decideDone <- a.GateDecide(pendingID, "approved", "",
			[]gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}})
	}()
	<-inWindow // decide 持有 mutex、位於窗口內
	go func() { _, err := a.EscalationCreate("plan:P1", "gate2:P1", "窗口內阻擋"); created <- err }()
	<-attempted    // blocker 已嘗試取鎖（在 release 前）——此刻必然阻塞於 mutex
	close(release) // 放行 append
	if err := <-decideDone; err != nil {
		t.Fatalf("decide must succeed: %v", err)
	}
	if err := <-created; err != nil {
		t.Fatalf("blocker create after window: %v", err)
	}
	if stateSeenByBlocker != gate.Active { // blocker 進臨界區時核可已 durable——無 TOCTOU 窗口
		t.Fatalf("blocker must observe post-append state, saw %v", stateSeenByBlocker)
	}
}
// 執行：go test -race -count=30 ./... -run TestGateDecideBarrier（收尾 gate 必跑）

func TestStaleBlockerReleasedByReplacementApproval(t *testing.T) { // P1：stale blocker 不得永久擋住修正版
	a := newTestApp(t)
	a.approveGate2(t, "P1")            // v1 核可
	a.mutateSpec(t)                    // spec 變更 → Gate 2 stale → hard blocker（stale:gate2:plan:P1）
	a.reapproveGate1AndRevisePlan(t)   // 修復：Gate 1 重核、plan 修訂 commit
	id2 := a.submitPlan(t, "P1")       // 修正版送核（送核本身不解除 blocker）
	if item := a.openItemByKey("stale:gate2:plan:P1"); item == nil {
		t.Fatal("blocker must remain until replacement approval") // 解除時點在 GateDecide 2b
	}
	if err := a.GateDecide(id2, "approved", "",
		[]gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}); err != nil {
		t.Fatalf("replacement approval must succeed: %v", err) // 2b 解除 → blocker check 通過 → append
	}
	if item := a.openItemByKey("stale:gate2:plan:P1"); item != nil {
		t.Fatal("replacement approval must system-resolve the stale blocker")
	}
}

func TestHardEscalationNotManuallyResolvable(t *testing.T) {
	a := newTestApp(t)
	id := a.systemStale(t) // 觸發 stale → hard item
	if err := a.EscalationResolve(id, "accepted_risk", "想跳過"); err == nil {
		t.Fatal("hard item must not be user-resolvable") // §3.8
	}
}

func TestEvidenceErrorAutoResolvedByRerun(t *testing.T) { // A8 閉環
	a := newTestApp(t)
	a.runEvidenceError(t, "P1", "T1", "expected_red") // 編譯失敗 fixture → error item open
	a.runEvidenceOK(t, "P1", "T1", "expected_red")    // 修復後重跑成功
	if item := a.openItemByKey("evidence-error:P1/T1/expected_red"); item != nil {
		t.Fatal("successful rerun must system-resolve the error item")
	}
}
```

- [ ] **Step 2–4: TDD＋全套 `-race`；barrier 測試以 `-race -count=30` 重複執行。**
- [ ] **Step 5: Commit**：`feat(app): workflow mutex barrier＋escalation 自動來源（§3.10／§3.8）`

### Task 25: EscalationInbox UI

**Files:**
- Create: `frontend/src/stores/escalation.ts`、`frontend/src/components/EscalationInbox.vue`（open／acknowledged 分區、來源 ref 導航、ack／resolve 表單（resolution code＋理由）、hard 項無 resolve 按鈕、blocking 標示）
- Modify: `App.vue`（下方面板與 GateConsole 並列 tab＋未處理計數 badge）、Plan／Gate／Evidence 各畫面加「建立升級項目」按鈕（帶當前 sourceRef）、locales
- Test: `EscalationInbox.test.ts`

- [ ] **Step 1: 失敗測試**：hard 項不渲染 resolve 控件；resolve 未填 reason 時送出 disabled；手動建立未帶 sourceRef 時綁定被拒（mock 驗證）。
- [ ] **Step 2–4: TDD＋vitest／build。** → **Step 5: Commit**：`feat(frontend): 升級收件匣（§3.8／§6）`

### Task 26: BDD features＋diagrams＋README

**Files:**
- Create: `docs/architecture/features/plan-gate.feature`（Stage B 閉環、lineage 拒絕、risk override、STALE）、`test-contract.feature`（evidence 兩型、七條一致性拒核、Gate 2 STALE 連動）、`escalation.feature`（fail-closed→修復→重驗→解除）
- Create: `docs/architecture/diagrams/plan-aggregate.mmd`（Plan／Gate2／TCA aggregate 關係）、`seq-tca.mmd`（test commit→mutation→兩 run→TCA 送核 sequence）；更新 `c4-container.mmd` 加 plan／evidence／escalation
- Modify: `README.md`（M3 列改「✅ M3a 核心閉環（多 session 並看延後至 M3b）」、功能段補 Gate 2／TCA／收件匣、架構樹補三個新 package、mermaid 圖嵌入）

- [ ] **Step 1: 依已實作行為撰寫**（Gherkin scenario 名對照 spec §7 流程與 §8 情境；圖與 code 對齊，偏差即修圖）。
- [ ] **Step 2: Mermaid 語法驗證測試**：新增 `frontend/src/lib/diagramSyntax.test.ts`——vitest 內以 `mermaid.parse()` 逐一驗 `docs/architecture/diagrams/*.mmd`（含新增與既有檔，glob 讀入），parse 失敗即測試失敗（frontend build 不會驗獨立 .mmd，此測試補該缺口）。README 內嵌 mermaid 區塊以同法抽出驗證。
- [ ] **Step 3: Commit**：`docs: M3a features／diagrams／README＋.mmd 語法測試（BDD→DDD 收尾）`

### Task 27: E2E 驗收矩陣＋最終 gate

**Files:**
- Create: `docs/spikes/m3a-results.md`（驗收記錄；截圖進 `docs/spikes/evidence/`）

**驗收矩陣（實機 `wails dev`，逐項打勾記錄於 m3a-results.md）：**

| # | 情境 | 預期 |
|---|---|---|
| A1 | Stage B 全程：PlannerAssist→編修→驗證→commit→送核→per-task risk 核可 | Gate 2 active，metadata 含排序 risk_decisions |
| A2 | Gate 2 核可後建 test commit（HEAD 前移） | Gate 2 **不** STALE（§3.9 歷史錨點） |
| A3 | 改 spec/ 檔案 | Gate 2 STALE＋收件匣 hard 項 |
| A4 | Stage C：test commit→mutation→expected_red＋negative_control→TCA 送核→核可 | TCA active；evidence 詳情可回溯 recording |
| A5 | 改 oracle-surface 檔案 | TCA STALE（oracle 重算）＋收件匣 |
| A6 | Gate 2 被新版 plan supersede | 所屬 TCA 連動 STALE |
| A7 | 故意讓測試編譯失敗跑 expected_red | result=error＋收件匣項；TCA 送核被拒 |
| A8 | 修復 A7→重跑 evidence→系統 resolve→重送核 | 閉環解除阻擋（§1 fail-closed 閉環） |
| A9 | 手動建立升級項目（Gate 2 卡片）→核可被擋→resolve→核可通過 | §3.10 barrier |
| A10 | App 重啟 | gate／evidence／escalation projection 完整重建；orphan worktree／temp 清理 |

- [ ] **Step 1: 逐項執行並記錄**（含失敗與偏差；不得只寫 PASS）。
- [ ] **Step 2: 最終 gate**：`go vet ./...`、`go test -race ./... -count=1`、**`go test -race -count=30 ./... -run 'TestGateDecideBarrier|TestStaleBlockerReleased'`**（Task 24 競態壓力）、`npm --prefix frontend run test`、`npm --prefix frontend run build`、`wails build`、`./scripts/bundle-clis.sh` 後實機 .app 冒煙。
- [ ] **Step 3: Commit**：`docs: M3a 驗收結果`；併版（merge to main）依 repo 慣例由 owner 決定。

---

## Self-Review 記錄（v4）

**v4 修訂（第三輪 plan 審閱 5 P1＋1 P2）**：Task 5 Step 3 同步 Prepare/Commit 新流程＋current-binding validation 限 approved（rejected 免驗，補 stale pending 核可失敗／駁回成功雙測）；gatepolicy 內 `gate.ApprovalRecord`／`gate.State` 限定名＋兩個 policy 的 `var _ gate.GatePolicy` 編譯期斷言；stale blocker 解除時點凍結於 GateDecide 編排 2b（approved Prepare 成功後、blocker 檢查前 `resolveByKeyLocked`，寫入失敗拒核；測試改為核可後驗證解除、送核當下 blocker 仍在）；barrier 測試改「先 Decide 進窗口→再啟動 blocker→等 `onWorkflowMuAttempt` 訊號→release」消除排程競態；Task 22 補 `ValidateTestCommit`／`EvidenceCommitCandidates` 後端綁定＋`bindings.test.ts` 鎖 TS adapter 參數形狀；Task 27 最終 gate 補 `-race -count=30` 競態壓力指令。

## Self-Review 記錄（v3）

**v3 修訂（第二輪 plan 審閱 9 P1）**：Task 5 PrepareDecision 加 pending request current-binding validation（Task 12／21 各補 mutation-before-decide 測試）；Gate 2／TCA policy 移至 `internal/gatepolicy`（消除 gate→plan→gate import cycle，gate 維持技術中立）；Task 22 擴為 TCA workspace（Stage C 前端操作入口＋production adapter 測試）；Task 18 intent 先於任何目錄建立（棄 MkdirTemp）＋(a0) crash window；Task 17 補 §3.7 三個 crash boundary 注入測試；Task 20 自帶 evidenceMu＋`reclaimEvidenceRuns()` 先於 `inflight.Wait()`＋channel-barrier 測試；Task 24 九條來源逐一凍結權威修復條件（stale→修正版送核系統解除，含測試）＋`createSystemLocked`／`resolveByKeyLocked` 重入規約＋barrier 測試改 channel seam（棄 time.Sleep、改驗 blocker 臨界區內觀察到的 gate 狀態）；Task 18/19 mutation patch `--numstat -z` rename 雙側驗證＋雙向 rename 測試。

## Self-Review 記錄（v2）

- **Spec 覆蓋**：§3.0→Task 9/10；§3.1→Task 0–5；§3.2→Task 1；§3.3→Task 8/10/12/15；§3.4→Task 19/21；§3.5→Task 7/8/12；§3.6→Task 16；§3.7→Task 17/19/20；§3.8→Task 23–25（九條自動來源逐一對應 Task 24）；§3.9→Task 4/10/21（oracle 權威＝目前內容）；§3.10→Task 5（Prepare/Commit 拆分）＋Task 24（編排）；§4→Task 18/19/20（lifecycle ownership）；§5→Task 11（active Gate 1 前置）；§6→Task 13–15/22/25；§7→A1–A10；§8→各 task 測試＋Task 26/27；§9 假設→Task 11 Step 5／Task 18。
- **v2 修訂（第一輪 plan 審閱 9 P1＋2 P2）**：Task 0 真實 fixture；Task 5 前端同步＋Prepare/Commit；Task 9 rename-safe lineage＋`spec.PlanScope` 修正；Task 11/12 committed context（active Gate 1、Gate 1 snapshot scenario、GateDecisionContext）；Task 16 pattern 語意凍結；Task 18 intent→active→removed registry＋`git apply --check`；Task 19 ContextLoader＋EvidenceRunDigest；Task 20 app lifecycle ownership＋shutdown barrier；Task 21 oracle 目前內容權威；Task 24 §3.10 順序編排＋injected barrier＋有效 risk 輸入＋九條來源＋`ResolveByKey`；Task 26 .mmd parse 測試。
- **Placeholder 掃描**：無 TBD／「適當處理」；所有 code step 附實際內容。
- **型別一致**：`gate.RiskSelection`（Task 4 定義、Task 5/15/24 消費）；`gate.PreparedDecision`（Task 5 定義、Task 24 消費）；`plan.Command`／`ExpectedFailure`（Task 8 定義、Task 19 消費）；`spec.Scope`／`spec.PlanScope`（Task 7 定義、Task 9/12/16 消費）；`internal/journal`（Task 6 定義、Task 18/20/23 消費）；`ContextLoader.LoadAt` 與 Task 10 `PlanLoader` 同簽名；`EvidenceRunDigest`（Task 19 定義、Task 21 消費）。
