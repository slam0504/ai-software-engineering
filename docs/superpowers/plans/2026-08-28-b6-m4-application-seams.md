# B6 M4 Application Seams Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev10（2026-08-31，配合 B5 spec rev9——Task 5 依施工事實核對的 owner 裁決重寫，改稱「review section 收斂」：VerifyReviewSection 補結構性重驗＋範圍聲明、perms map 查無／未知值 fail loud、submitted_at 正規化為 UTC RFC3339Nano、刪除空宣稱 Consumes、不再建 manifest 外殼與 digest（歸 C1）；估點維持 0.1 pt 不動）
> 狀態：**plan gate 通過**（2026-08-28 第七輪 Approved @ rev7——六輪 findings 全數收斂；B6a／B6b 可獨立執行，沿建議順序先 B6a 再 B6b。rev8／rev9／rev10 皆為 implementation follow-up 發現的 erratum／契約缺口，**未重開完整 plan gate**——rev8 收緊測試以符合 rev7 已核准的不變量、rev9 補完 exported verifier 自身應履行的 bijection 保證、rev10 配合 B5 spec rev9 補完 Task 5 尚未實作前發現的三項契約缺口＋範圍收斂，四者均非設計變更、rev10 明確**未重估**）
> 票源：Pre-M4 Readiness Backlog **B6a／B6b**（owner 於 plan gate 第三輪核准拆票：B6a 1.45 pt／B6b 0.6 pt；backlog rev6 已同步拆列。兩票**皆只依賴 B5**、各自獨立結案——B6a→B6b 為建議執行順序、非技術相依；原 B6 的 aggregate 狀態於**兩票皆完成**時關閉，由**後完成之票**負責確認，不固定綁在 B6b）

**Goal:** 依 B5 spec 建立 M4 所需的 application seams——Forge port、Gate 3 policy 骨架與 manifest 收斂邏輯、gate 讀取路徑 detect-only 遷移、wsregistry 綁定欄位、freeze latch 機制——不含 TaskRun domain 本體（C1a）、GitHub adapter（C1b）、Gate 3 UI（C1c）。

**Architecture:** 沿既有 ports-and-adapters 慣例：`internal/forge` 為純 port（型別＋interface＋fake）；Gate 3 policy 與 manifest 收斂進 `internal/gatepolicy`（`internal/gate` 維持零 domain import 凍結）；detect-only 投影加在 `gate.Service`；App 只注入 service／port reference，不新增 M4 domain mutable state。

**Tech Stack:** Go 1.25（stdlib only，無新外部依賴）；測試沿 `go vet ./...`＋`go test -race ./... -count=1` 慣例。

**Spec:** `docs/superpowers/specs/2026-08-27-taskrun-gate3-forge-contract-design.md`（rev9，design gate 通過；rev9 為窄幅 design re-review，未重開完整 design gate）

## B6／C1 範圍分界與估點（plan gate 第一輪已裁決）

| B5 spec 要求 | 歸屬 | 理由 |
|---|---|---|
| §3.2(0) gate journal 單一寫入者不變式——7 入口 detect-only 遷移 | **B6**（Task 2-3） | M4 觸及的既有 orchestration 抽取核心；TaskRun 建立（C1a）依賴此前置 |
| §6 Forge port 型別＋interface | **B6**（Task 1） | service 骨架；C1b 實作 GitHub adapter |
| §5.1(5)(6) canonical manifest 收斂（bijection／current-effective／eligibility） | **B6**（Task 4-5，rev10 措辭校正——Task 5 只產出 review **section**，manifest 外殼＋digest 歸 **C1**） | 純邏輯、無依賴、測試價值高；C1b/C1c 呼叫 |
| §5.2 `gate3_promotion` policy 註冊＋binding 形狀驗證＋重驗編排骨架 | **B6**（Task 6） | Gate 3 application service 骨架；深度依賴以 interface 注入、C1 接線 |
| §4.3 pending Gate 3 request 轉終態（mismatch／transient 區分＋expired transition 寫入入口） | **B6**（Task 6b，rev2 補——plan gate P1） | 缺此則 PrepareDecision 遇 mismatch 只能回錯、pending 永不收斂，B5 §4.3 無法成立 |
| §3.2(0) gate Submit 三送核路徑入 workflowMu | **B6**（Task 3b，rev2 補——plan gate P1） | 單一寫入者不變式涵蓋「任何 append」，rev1 只遷移了 Reconcile 面 |
| §3.1 wsregistry Entry `{task_run_id, snapshot_digest}`＋write-once 方法 | **B6**（Task 7） | 既有 store 的 seam 擴充（mutate 慣例）；C1a 呼叫 |
| §4.2(2)(3) freeze latch 機制＋admission 檢查（SendMessage／resolveApproval） | **B6**（Task 8-9） | 觸及既有 orchestration（Manager.BeginSubmit／resolveApproval）；觸發序列（cause 偵測→freeze）歸 C1a |
| §2 TaskRun snapshot／journal／狀態機／digest；§3.2 建立交易；§3.5 resume；§3.6 abandoned；§4.2 freeze 觸發序列與 repair | **C1a** | backlog 估點表明文「TaskRun snapshot（domain＋持久化）＋綁定注入」 |
| GitHub forge adapter、認證、rate limit | **C1b** | backlog 明文 |
| Gate 3 決議 UI＋evidence 顯示＋走查 | **C1c** | backlog 明文 |

**Owner 裁決（plan gate 第一輪）已回填**：(1) Task 8-9 留在 B6——latch 是既有 admission／approval orchestration 的 seam；cause 偵測、journal transition 與 startup／runtime repair 留 C1a。(2) 原估 1.4 pt 不得沿用，rev2 逐 task bottom-up 重算如下；未逾 2.0 pt 不拆票。

**Bottom-up 估點（rev3 重算；0.1 pt＝1 hr）**：

| Task | 內容 | pt | 拆票歸屬 |
|---|---|---|---|
| 1 | forge port＋fake | 0.1 | B6a |
| 2 | ListDetectOnly（含 unknown-gate fail closed） | 0.1 | B6a |
| 3 | 七入口 detect-only 遷移＋gateListReconciled＋既有測試語意更新 | 0.2 | B6a |
| 3b | submitGateRequest 唯一 Submit 呼叫點入 workflowMu＋TryLock probe 測試 | 0.15 | B6a |
| 4 | required-check manifest（含 RFC3339 嚴格解析） | 0.2 | B6a |
| 5 | review section 收斂（含 RFC3339 嚴格解析＋submitted_at 正規化；rev10 範圍收斂——不建 manifest 外殼／不算 digest，估點不動） | 0.1 | B6a |
| 6 | gate3 policy 骨架（含 subject 交叉驗證＋mismatch／transient 錯誤分類） | 0.2 | B6a |
| 6b | Gate 3 pending 終態封閉（三入口 State==Pending＋Expired＋terminal cause 投影＋gateDecide 接線） | 0.35 | B6a |
| 7 | wsregistry 1:1 雙向基數（跨 WSID 掃描＋partial-pair＋persistOrRollback failure matrix） | 0.2 | B6b |
| 8 | FreezeTurns＋兩種 admission 檢查 | 0.15 | B6b |
| 9 | approval freeze 旗標＋雙鎖設定端（含鎖不洩漏測試） | 0.2 | B6b |
| 10a | B6a 驗證＋驗收清單（獨立結案；若為後完成之票，確認 aggregate 關閉） | 0.05 | B6a |
| 10b | B6b 驗證＋驗收清單（獨立結案；若為後完成之票，確認 aggregate 關閉） | 0.05 | B6b |
| **合計** | | **2.05** | B6a **1.45**／B6b **0.6** |

**拆票（rev4 owner 核准；rev5 修 aggregate 結案順序中立）**：**B6a**＝gate 單一寫入者＋Gate 3 policy／manifest（Task 1-6b＋10a，1.45 pt）；**B6b**＝綁定持久化＋freeze latch seams（Task 7-9＋10b，0.6 pt）。兩票**各自獨立結案**：皆只依賴 B5；B6a→B6b 為**建議執行順序**（非技術相依）；各票有自己的驗證與驗收清單（Task 10a／10b）。**原 B6 的 aggregate 狀態只在兩票皆完成時關閉，由後完成之票於其收尾 task 確認**（不固定綁在 B6b——若 B6b 先做，B6a 收尾時確認）。backlog rev6 已同步：B6 拆列 B6a/B6b、C1 相依改 B6a＋B6b。

## Global Constraints

- Module：`github.com/slam0504/sdlc-workbench`；Go 1.25.0（go.mod 現值，A4 的 toolchain 變更與本票無關）。
- 驗證指令（每 task 結尾）：`go vet ./...` 與 `go test -race ./... -count=1`（受影響 package 先跑單包，commit 前跑全套）。
- 無新外部依賴；一律 stdlib。
- `internal/gate` 維持零 domain import（tca.go:1-12 架構凍結）——Gate 3 policy 只能進 `internal/gatepolicy`。
- Wails bindings 留在 `app.go`；internal packages 不得 import app 層。
- App aggregate 不新增 M4 domain mutable state；新測試注入點走 interface／fake（B6 驗收 4）。
- 註解與 doc comment 用繁體中文、對齊既有風格（見 app.go workflowMu doc 的密度）。
- Commit message 格式沿 repo 慣例：`feat(scope): ...`／`test(scope): ...`，一 task 一 commit（含測試）。

---

### Task 1: Forge port（`internal/forge`——型別、interface、fake）

**Files:**
- Create: `internal/forge/forge.go`
- Create: `internal/forge/fake.go`
- Test: `internal/forge/fake_test.go`

**Interfaces:**
- Consumes: 無（純 port，僅 stdlib）。
- Produces: `forge.Forge` interface 與全部型別（下列逐字），供 Task 4-6 與 C1b 使用。

- [ ] **Step 1: 寫 `forge.go`（型別與 interface，B5 §6 逐字對應）**

```go
// Package forge 定義 Workbench 對 forge（GitHub-first）的最小 port——
// 全部唯讀＋EnsurePullRequest，不含 merge（B5 spec §6）。實作為 C1b 的
// GitHub adapter；本套件僅型別與契約，零外部依賴。
package forge

import "context"

// RepoID／BranchRef／OID 型別明確區分：repo identity、branch ref、commit OID
//（B5 §6——防止字串混用）。
type RepoID struct {
	Owner string
	Repo  string
}
type BranchRef string // refs/heads/... 全名
type OID string       // git commit OID（hex）

type PRRef struct {
	Number int
}
type PRState struct {
	HeadOID OID
	BaseOID OID
	State   string // "open"／"closed"／"merged"
}
type PRMeta struct {
	Title string
	Body  string
}

// RequiredCheckRef 是 required check 的權威 key（B5 §5.1(5)）：
// AppID 為 nil ＝ 不限來源（對齊 branch protection API 的 {context, app_id}）。
type RequiredCheckRef struct {
	Context string
	AppID   *int64
}
type CheckRun struct {
	Name       string
	AppID      int64
	RunID      int64
	HeadOID    OID
	Status     string // "queued"／"in_progress"／"completed"
	Conclusion string // completed 時："success"／"failure"／...
	StartedAt  string // RFC3339
}
type RequiredChecks struct {
	Required []RequiredCheckRef
	Runs     []CheckRun
}

type Review struct {
	ReviewID        int64
	ReviewerLogin   string
	State           string // "APPROVED"／"CHANGES_REQUESTED"／"DISMISSED"／"COMMENTED"／"PENDING"
	ReviewedHeadOID OID
	SubmittedAt     string // RFC3339
}

// Permission 是 collaborator permission（B5 §5.1(6) eligibility）。
type Permission string

const (
	PermissionAdmin    Permission = "admin"
	PermissionMaintain Permission = "maintain"
	PermissionWrite    Permission = "write"
	PermissionRead     Permission = "read"
	PermissionNone     Permission = "none"
)

// Eligible 回傳該 permission 是否具 review 效力（write／maintain／admin）。
func (p Permission) Eligible() bool {
	return p == PermissionWrite || p == PermissionMaintain || p == PermissionAdmin
}

// Forge 錯誤語意 fail closed：讀取失敗＝無法決議，不得當作
// checks 未設定或 review 不存在（B5 §6）。
type Forge interface {
	// EnsurePullRequest 以 (repo, headRef, baseRef, taskRunID marker) 確定性收斂：
	// 既有 open PR 同 head/base 且 marker 相符→回傳之；marker 不符或同
	// head/base 多筆→fail loud；不存在→建立（body/label 帶 taskrun:<ULID>）。
	EnsurePullRequest(ctx context.Context, repo RepoID, headRef, baseRef BranchRef, taskRunID string, meta PRMeta) (PRRef, error)
	GetPullRequest(ctx context.Context, repo RepoID, pr PRRef) (PRState, error)
	GetRequiredChecks(ctx context.Context, repo RepoID, pr PRRef, head OID) (RequiredChecks, error)
	GetReviews(ctx context.Context, repo RepoID, pr PRRef) ([]Review, error)
	GetCollaboratorPermission(ctx context.Context, repo RepoID, login string) (Permission, error)
}
```

- [ ] **Step 2: 寫 `fake.go`（可注入回傳值的 fake，供本 repo 各包測試）**

```go
package forge

import "context"

// Fake 是測試用 Forge：每個方法回傳對應欄位；Err 非 nil 時一律回傳 Err
//（模擬 forge 讀取失敗的 fail-closed 路徑）。沿 gate stubPolicy 慣例：
// production-adjacent fake，不進 _test.go 因跨套件使用（gatepolicy 測試）。
type Fake struct {
	Err            error
	PR             PRRef
	PRState        PRState
	RequiredChecks RequiredChecks
	Reviews        []Review
	Permissions    map[string]Permission // login → permission；缺項回 PermissionNone
}

var _ Forge = (*Fake)(nil)

func (f *Fake) EnsurePullRequest(_ context.Context, _ RepoID, _, _ BranchRef, _ string, _ PRMeta) (PRRef, error) {
	if f.Err != nil {
		return PRRef{}, f.Err
	}
	return f.PR, nil
}
func (f *Fake) GetPullRequest(_ context.Context, _ RepoID, _ PRRef) (PRState, error) {
	if f.Err != nil {
		return PRState{}, f.Err
	}
	return f.PRState, nil
}
func (f *Fake) GetRequiredChecks(_ context.Context, _ RepoID, _ PRRef, _ OID) (RequiredChecks, error) {
	if f.Err != nil {
		return RequiredChecks{}, f.Err
	}
	return f.RequiredChecks, nil
}
func (f *Fake) GetReviews(_ context.Context, _ RepoID, _ PRRef) ([]Review, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Reviews, nil
}
func (f *Fake) GetCollaboratorPermission(_ context.Context, _ RepoID, login string) (Permission, error) {
	if f.Err != nil {
		return PermissionNone, f.Err
	}
	if p, ok := f.Permissions[login]; ok {
		return p, nil
	}
	return PermissionNone, nil
}
```

- [ ] **Step 3: 寫測試（fake 契約＋Eligible）**

```go
package forge

import (
	"context"
	"errors"
	"testing"
)

func TestFakeErrShortCircuitsAllMethods(t *testing.T) {
	f := &Fake{Err: errors.New("boom")}
	ctx := context.Background()
	if _, err := f.GetPullRequest(ctx, RepoID{}, PRRef{}); err == nil {
		t.Fatal("GetPullRequest 應回傳 Err")
	}
	if _, err := f.GetRequiredChecks(ctx, RepoID{}, PRRef{}, OID("x")); err == nil {
		t.Fatal("GetRequiredChecks 應回傳 Err")
	}
	if _, err := f.GetReviews(ctx, RepoID{}, PRRef{}); err == nil {
		t.Fatal("GetReviews 應回傳 Err")
	}
	if _, err := f.GetCollaboratorPermission(ctx, RepoID{}, "u"); err == nil {
		t.Fatal("GetCollaboratorPermission 應回傳 Err")
	}
}

func TestPermissionEligible(t *testing.T) {
	for p, want := range map[Permission]bool{
		PermissionAdmin: true, PermissionMaintain: true, PermissionWrite: true,
		PermissionRead: false, PermissionNone: false,
	} {
		if p.Eligible() != want {
			t.Fatalf("%s.Eligible()=%v, want %v", p, !want, want)
		}
	}
}

func TestFakePermissionDefaultNone(t *testing.T) {
	f := &Fake{}
	p, err := f.GetCollaboratorPermission(context.Background(), RepoID{}, "stranger")
	if err != nil || p != PermissionNone {
		t.Fatalf("缺項應回 PermissionNone, got %v %v", p, err)
	}
}
```

- [ ] **Step 4: 執行 `go test -race ./internal/forge/ -count=1`，預期 PASS；`go vet ./internal/forge/`**
- [ ] **Step 5: Commit**

```bash
git add internal/forge/
git commit -m "feat(forge): B6 Task 1——Forge port 型別、interface 與 fake（B5 spec §6）"
```

---

### Task 2: `gate.Service.ListDetectOnly`（B5 §3.2(0) detect-only 投影）

**Files:**
- Modify: `internal/gate/service.go`（在 `List` 之後新增方法）
- Test: `internal/gate/service_test.go`（追加）

**Interfaces:**
- Consumes: 既有 `Project(ops []GateOp) ([]GateEntry, error)`、`GatePolicy.ReconcileBindings(rec ApprovalRecord) ([]StaleCause, error)`。
- Produces: `func (s *Service) ListDetectOnly() ([]GateEntry, error)`——與 `List()` 呈現同等 stale 判定，但**不 append journal**。Task 3 的 app.go 讀取路徑呼叫它。

- [ ] **Step 1: 寫 failing test（detect-only 顯示 stale 且 journal 不長）**

沿 `newTestServiceWithCurrent` 既有 helper（service_test.go:55-107）：以會回報 mismatch 的 `current` 函數建 service，先 Submit＋Decide 造出 Active record，再改變 current 使 ReconcileBindings 產生 stale cause。

```go
func TestListDetectOnlyShowsStaleWithoutAppend(t *testing.T) {
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
	id := submitAndApprove(t, s) // 既有測試已有同型流程；若無此 helper，內聯 Submit+Decide 兩行
	// 讓 current 改變 → Active record 的 binding 與現值不符
	setCurrent(s, func() (string, error) { return "sha256:" + hex64b(), nil })

	before := len(s.opsForTest())
	entries, err := s.ListDetectOnly()
	if err != nil {
		t.Fatal(err)
	}
	if got := stateOf(entries, id); got != Stale {
		t.Fatalf("detect-only 應呈現 Stale, got %s", got)
	}
	if after := len(s.opsForTest()); after != before {
		t.Fatalf("detect-only 不得 append：ops %d→%d", before, after)
	}
	// 對照組：List() 會落 durable transition
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if after := len(s.opsForTest()); after == before {
		t.Fatal("List() 應 append stale transition")
	}
}
```

註：`submitAndApprove`／`setCurrent`／`stateOf` 若 service_test.go 尚無同名 helper，於同檔新增（`setCurrent` 直接重建 policy registry：`s.reg["gate1"] = NewGate1Policy(fn)`——white-box in-package 測試可直接賦值；`stateOf` 為 5 行線性掃描）。

- [ ] **Step 2: 跑 `go test -race ./internal/gate/ -run TestListDetectOnly -count=1`，預期 FAIL（方法不存在）**
- [ ] **Step 3: 實作 `ListDetectOnly`**

```go
// ListDetectOnly 回傳與「Reconcile 後 Project」等值的投影，但不 append
// journal（B5 spec §3.2(0)：gate journal 單一寫入者——durable stale
// transition 只准持 workflowMu 的寫入路徑落盤；純讀取入口一律走本方法）。
// policy 檢查與 Reconcile 相同：Active record 經 ReconcileBindings 有
// cause 即以 Stale 呈現；current-read 錯誤 fail closed 回傳 error。
func (s *Service) ListDetectOnly() ([]GateEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return nil, err
	}
	for i := range entries {
		e := &entries[i]
		if e.State != Active || e.Record == nil {
			continue
		}
		pol, ok := s.reg[e.Record.Gate]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownGate, e.Record.Gate) // 沿 Reconcile fail closed（rev2 修——continue 會 fail open 呈現 Active）
		}
		causes, err := pol.ReconcileBindings(*e.Record)
		if err != nil {
			return nil, err // fail closed，沿 Reconcile
		}
		if len(causes) > 0 {
			e.State = Stale
			// TerminalCause 欄位由 Task 6b 引入後，此處同步補
			// e.TerminalCause = causes[0].Cause（等值契約，見 Task 6b）
		}
	}
	return entries, nil
}
```

實作前先讀 `Reconcile()`（service.go:222-254）確認錯誤處理分支逐一對齊——production Reconcile 對 unknown gate 回傳 `ErrUnknownGate`（rev2 更正：rev1 的 `continue` 會讓 unknown active record 呈現 Active，與「同 Reconcile 判定」不相等）；有出入以 Reconcile 現行為準並在測試中固定。補測試：

```go
func TestListDetectOnlyUnknownGateFailsClosed(t *testing.T) {
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
	_ = submitAndApprove(t, s)
	delete(s.reg, "gate1") // white-box：模擬 record 的 gate 不在 registry
	before := len(s.opsForTest())
	if _, err := s.ListDetectOnly(); err == nil || !errors.Is(err, ErrUnknownGate) {
		t.Fatalf("unknown gate 應回 ErrUnknownGate, got %v", err)
	}
	if len(s.opsForTest()) != before {
		t.Fatal("回錯時 journal 不得增長")
	}
}
```

- [ ] **Step 4: 跑 Step 2 指令，預期 PASS；再跑 `go test -race ./internal/gate/ -count=1` 全包**
- [ ] **Step 5: Commit**

```bash
git add internal/gate/
git commit -m "feat(gate): B6 Task 2——ListDetectOnly detect-only 投影（B5 §3.2(0) 單一寫入者前置）"
```

---

### Task 3: app.go 七入口 detect-only 遷移＋寫入路徑保留 reconcile

**Files:**
- Modify: `app.go`——`gateList()`（app.go:5725-5762）、`runEvidence` 的 gateList 呼叫（app.go:5236-5251）
- Test: `app_gate_test.go`（追加）

**Interfaces:**
- Consumes: Task 2 的 `svc.ListDetectOnly()`；既有 `svc.List()`。
- Produces: `a.gateList()`（改為 detect-only，七個讀取入口自動生效——它們全呼叫 `a.gateList()`）；新增 `a.gateListReconciled()`（呼叫 `svc.List()`，doc 註明「caller 必須持 workflowMu」）供 `runEvidence` 與後續 C1a 寫入路徑使用。

- [ ] **Step 1: 寫 failing test（GateList 讀取不落 stale transition；RunEvidence 路徑仍落）**

沿 `app_gate_test.go` 的 `TestGateLiveLoopSubmitApproveThenStale` 既有 fixture 手法（造 Active gate1 → 改動 spec 使 manifest 改變）：

```go
func TestGateListDetectOnlyDoesNotPersistStale(t *testing.T) {
	a := newGateTestApp(t) // 沿既有 app_gate_test 的建構 helper
	approvalID := approveGate1(t, a)
	mutateSpecManifest(t, a) // 使 current manifest 與 record binding 不符

	opsBefore := gateOpsCount(t, a)
	entries, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if st := dtoState(entries, approvalID); st != "stale" {
		t.Fatalf("讀取應呈現 stale, got %s", st)
	}
	if got := gateOpsCount(t, a); got != opsBefore {
		t.Fatalf("GateList 不得 append：ops %d→%d", opsBefore, got)
	}
}
```

`gateOpsCount`：white-box 讀 `a.gateJournal.Ops()` 長度。`newGateTestApp`／`approveGate1`／`mutateSpecManifest` 沿既有測試檔同型 helper（該檔已有 submit→approve→stale 全流程可拆用；若 helper 為內聯，抽出共用）。

- [ ] **Step 2: 跑 `go test -race -run TestGateListDetectOnly -count=1 .`，預期 FAIL（現行 GateList 會 append）**
- [ ] **Step 3: 遷移 `gateList()` 並新增 `gateListReconciled()`**

`gateList()` 只改一行呼叫並更新 doc；DTO 映射邏輯抽成共用：

```go
// gateList：GateList 的本體（不進交易閘，見 beginTxn 的 doc）。
// B6 起為 detect-only 投影（B5 §3.2(0)）：呈現與 reconcile 後相同的
// stale 判定，但不落 durable transition——durable stale 只准持
// workflowMu 的寫入路徑（gateListReconciled／reconcileLocked）產生。
func (a *App) gateList() ([]GateEntryDTO, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return nil, err
	}
	entries, err := svc.ListDetectOnly()
	if err != nil {
		return nil, err
	}
	return a.gateEntriesToDTO(entries), nil
}

// gateListReconciled：Reconcile-before-Project 的 durable 版本。
// caller 必須持有 workflowMu（單一寫入者不變式）；目前呼叫者：
// runEvidence（app.go §3.8 接線）。C1a 的 TaskRun 建立臨界區沿用。
func (a *App) gateListReconciled() ([]GateEntryDTO, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return nil, err
	}
	entries, err := svc.List()
	if err != nil {
		return nil, err
	}
	return a.gateEntriesToDTO(entries), nil
}

// gateEntriesToDTO：原 gateList 的 DTO 映射本體，原樣搬移（含 degraded 標示）。
func (a *App) gateEntriesToDTO(entries []gate.GateEntry) []GateEntryDTO {
	// ……原 app.go:5730-5761 的迴圈原封搬入，零語意變更……
}
```

`runEvidence` 內原 `a.gateList()`（app.go:5236-5251，位於 workflowMu 臨界區）改呼叫 `a.gateListReconciled()`。其餘六個入口（submitPlanForApproval:5019、validateTestCommit:5433、evidenceCommitCandidates:5480、submitTestContract:5564、gateDecisionContext:5660、planAssist:6475）呼叫 `a.gateList()` 不動——遷移經由 `gateList()` 本體生效。

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；跑 `go test -race -count=1 .`（root 包全部 app tests）確認零回歸——特別注意 `TestGateLiveLoopSubmitApproveThenStale` 若斷言「讀取後 journal 有 stale transition」需依 B5 §3.2(0) 更新斷言（讀取不落盤、由寫入路徑落盤），並在 commit message 註明語意變更依據**
- [ ] **Step 5: Commit**

```bash
git add app.go app_gate_test.go
git commit -m "feat(app): B6 Task 3——gate 讀取路徑 detect-only 遷移；runEvidence 保留 durable reconcile（B5 §3.2(0)）"
```

---

### Task 3b: gate Submit writer seam（rev2 補；rev3 修——**唯一** Submit 呼叫點在 submitGateRequest wrapper 內）

**Files:**
- Modify: `app.go`——`submitGateRequest()`（**production 唯一的 `svc.Submit` 呼叫點**；gate1／gate2／TCA 三條送核路徑都先進這個 wrapper——rev2「三個呼叫點原地替換」判讀錯誤，照做會繞過 wrapper 的 missing-binding escalation 建立／解除契約）
- Test: `app_gate_test.go`（追加）

**Interfaces:**
- Consumes: 既有 `svc.Submit(gateName, subject string, bindings []Binding) (string, error)`；`submitGateRequest` 既有的 escalation 契約。
- Produces: `submitGateRequest` 內的唯一 `svc.Submit` 呼叫收進 `workflowMu` 臨界區（B5 §3.2(0)：任何 gate journal append 持鎖）；**三個既有 caller 完全不動**，escalation 建立／解除契約原樣保留。

- [ ] **Step 1: 寫 failing test（rev4 改**確定性 TryLock probe**——rev3 的 sleep barrier 在排程延遲超過 50ms 時，錯誤實作也會誤通過；rev8 改**序列斷言**——見下方 erratum 說明）**

以注入的 `GatePolicy` 在 `svc.Submit` 呼叫 `ValidateRequest` 時（必然位於 Submit 臨界區內）用 `workflowMu.TryLock()` 探測鎖是否已被持有——確定性、零 sleep，且不新增 backlog 禁止的 App ad-hoc test hook（probe 是測試注入的 policy，非 App aggregate 的 hook）：

**rev8 erratum**：probe 實際會觸發兩次——第一次是 `submitGateRequest` 自己的 app 層 pre-validation（應在鎖外）、第二次是 `svc.Submit` 內部再驗同一 policy（應在鎖內）。若只用單一 write-once-true 布林旗標（rev4-rev7 版本）記錄「是否曾經有一次持鎖」，一個「鎖只加在第一次呼叫（app 層 pre-validation）、`svc.Submit` 本身無鎖」的錯誤實作會讓第一次探測就把旗標設為 true，第二次探測不會再清回 false，測試因而**誤通過**（見 Task 3b follow-up 報告 Mutation B 實測證據）。改記錄兩次 probe 的完整序列並精確斷言次數與順序，而非只看「曾經持鎖」：

```go
// probePolicy：包裝實際 gate1 policy，僅在 ValidateRequest 時執行 probe。
type probePolicy struct {
	gate.GatePolicy
	probe func()
}

func (p probePolicy) ValidateRequest(req gate.GateRequest) error {
	p.probe()
	return p.GatePolicy.ValidateRequest(req)
}

func TestGateSubmitHoldsWorkflowMu(t *testing.T) {
	a := newGateTestApp(t)
	if _, err := a.ensureGate(); err != nil {
		t.Fatal(err)
	}
	// heldSequence 依序記錄兩次 probe 的觀測：第一次＝submitGateRequest 自己
	// 的 app 層 pre-validation（應在鎖外，false）；第二次＝svc.Submit 內部再
	// 驗同一 policy（應在臨界區內，true）。單一布林值無法區分「只有第一次
	// 持鎖」（錯誤實作）與「只有第二次持鎖」（正確實作）——必須斷言完整序列。
	var heldSequence []bool
	a.gateReg["gate1"] = probePolicy{GatePolicy: a.gateReg["gate1"], probe: func() {
		if a.workflowMu.TryLock() {
			a.workflowMu.Unlock()
			heldSequence = append(heldSequence, false)
			return
		}
		heldSequence = append(heldSequence, true)
	}}
	submitGate1(t, a) // 沿既有 gate1 送核 helper
	if len(heldSequence) != 2 || heldSequence[0] || !heldSequence[1] {
		t.Fatalf("workflowMu held-sequence across the two ValidateRequest probes = %v, want [false true]（B5 §3.2(0)）", heldSequence)
	}
}
```

- [ ] **Step 2: 跑 `go test -race -run TestGateSubmitHoldsWorkflowMu -count=1 .`，預期 FAIL（現行 Submit 不取 workflowMu，序列為 `[false false]`）**
- [ ] **Step 3: 實作——只改 `submitGateRequest` 內的唯一呼叫點**

於 `submitGateRequest` 中包住既有 `svc.Submit` 呼叫（escalation 建立／解除契約行不動、僅 Submit 行納入臨界區）：

```go
	// B5 spec §3.2(0)：Submit 的 request append 也是 gate journal append——
	// 單一寫入者不變式涵蓋任何 append。三條送核路徑（gate1/gate2/TCA）
	// 都經本 wrapper，此處是唯一 Submit 呼叫點；missing-binding escalation
	// 契約在鎖外原樣保留。
	a.workflowMu.Lock()
	id, err := svc.Submit(gateName, subject, bindings)
	a.workflowMu.Unlock()
```

（實作前確認 `submitGateRequest` 當下不持 workflowMu、且 Submit 前後的 escalation 邏輯與鎖無重入——`submitPlanForApproval` 的 escalation 分支在 wrapper 返回後才取鎖，app.go:5051，無巢狀。）

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；`go test -race -count=1 .` 全綠（三條送核路徑與 escalation 既有測試零回歸）**
- [ ] **Step 5: Commit**

```bash
git add app.go app_gate_test.go
git commit -m "feat(app): B6 Task 3b——gate Submit writer seam，三送核路徑入 workflowMu（B5 §3.2(0)）"
```

---

### Task 4: required-check manifest 收斂（`internal/gatepolicy/gate3_manifest.go`）

**Files:**
- Create: `internal/gatepolicy/gate3_manifest.go`
- Test: `internal/gatepolicy/gate3_manifest_test.go`

**Interfaces:**
- Consumes: Task 1 的 `forge.RequiredChecks`／`forge.CheckRun`／`forge.RequiredCheckRef`／`forge.OID`。
- Produces:
  - `type RequiredCheckManifest struct{ ManifestSchema int; RequiredChecks []RequiredCheckEntry; Runs []CheckRunEntry }`
  - `func BuildRequiredCheckManifest(rc forge.RequiredChecks, head forge.OID) (RequiredCheckManifest, error)`（attribution＋current-effective＋排序＋bijection，違規回 error）
  - `func VerifyRequiredCheckManifest(m RequiredCheckManifest, head forge.OID) error`（**rev9**：不依賴 `BuildRequiredCheckManifest` 已先執行，自行完成 §5.1(5) 全部 bijection 重驗——required key 唯一、每 run 的 key 存在於 required 集合且恰一筆、每 required key 必須被覆蓋、run_id 不得多重歸屬、attribution 重驗——外加全 success＋全 head match）
  - `func ManifestDigest(v any) (string, error)`（`"sha256:" + hex(sha256(json.Marshal))`——struct 宣告序＝spec 字面序，沿 domainspec canonical.go 先例；本 Task 與 Task 5 共用）

- [ ] **Step 1: 寫 failing tests（表格測試覆蓋 B5 §5.1(5) 全部規則）**

```go
package gatepolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/forge"
)

func i64(v int64) *int64 { return &v }

func TestBuildRequiredCheckManifest(t *testing.T) {
	head := forge.OID("aaaa")
	run := func(name string, app, id int64, started, concl string) forge.CheckRun {
		return forge.CheckRun{Name: name, AppID: app, RunID: id, HeadOID: head,
			Status: "completed", Conclusion: concl, StartedAt: started}
	}
	cases := []struct {
		name    string
		rc      forge.RequiredChecks
		wantErr string // 空字串＝成功
		check   func(t *testing.T, m RequiredCheckManifest)
	}{
		{name: "app_id 為 nil 可由任一 app 的 run 覆蓋（bijection 而非 key 相等）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if len(m.Runs) != 1 || m.Runs[0].RunAppID != 42 || m.Runs[0].RequiredAppID != nil {
					t.Fatalf("run 應記錄 required_app_id=nil 與 run_app_id=42：%+v", m.Runs)
				}
			}},
		{name: "同名多次執行取 started_at 最新（tie 取 run_id 大者）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "failure"),
					run("ci", 42, 2, "2026-08-28T02:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if m.Runs[0].RunID != 2 {
					t.Fatalf("current-effective 應為 run 2：%+v", m.Runs)
				}
			}},
		{name: "不同時區偏移依實際時間比較（rev2——字典序會選錯）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					// 03:00+08:00 ＝ 前一日 19:00Z，字典序卻大於 01:00Z——實際較舊
					run("ci", 42, 1, "2026-08-28T03:00:00+08:00", "failure"),
					run("ci", 42, 2, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if m.Runs[0].RunID != 2 {
					t.Fatalf("應依實際時間取 run 2（01:00Z 晚於 27 日 19:00Z）：%+v", m.Runs)
				}
			}},
		{name: "started_at 非 RFC3339 → fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "not-a-time", "success")}},
			wantErr: "非 RFC3339"},
		{name: "required_app_id 為 nil 且同名 run 來自多 app → 歸屬歧義 fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "success"),
					run("ci", 43, 2, "2026-08-28T02:00:00Z", "success")}},
			wantErr: "歸屬歧義"},
		{name: "required 缺對應 run → 無缺漏違反",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}, {Context: "lint"}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			wantErr: "missing"},
		{name: "forge 回傳重複 required key → fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}, {Context: "ci", AppID: i64(42)}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			wantErr: "重複"},
		{name: "非 required 的 run 不入 manifest（無多餘）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "success"),
					run("extra", 42, 9, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if len(m.Runs) != 1 {
					t.Fatalf("extra run 不得入 manifest：%+v", m.Runs)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := BuildRequiredCheckManifest(tc.rc, head)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.ManifestSchema != 1 {
				t.Fatalf("manifest_schema 必為 1")
			}
			tc.check(t, m)
		})
	}
}

func TestRequiredCheckManifestDigestOrderIndependent(t *testing.T) {
	head := forge.OID("aaaa")
	rc := forge.RequiredChecks{
		Required: []forge.RequiredCheckRef{{Context: "b", AppID: i64(1)}, {Context: "a", AppID: i64(1)}},
		Runs: []forge.CheckRun{
			{Name: "a", AppID: 1, RunID: 1, HeadOID: head, Status: "completed", Conclusion: "success", StartedAt: "2026-08-28T01:00:00Z"},
			{Name: "b", AppID: 1, RunID: 2, HeadOID: head, Status: "completed", Conclusion: "success", StartedAt: "2026-08-28T01:00:00Z"}}}
	m1, err := BuildRequiredCheckManifest(rc, head)
	if err != nil {
		t.Fatal(err)
	}
	// 反轉 forge 回傳順序
	rc.Required[0], rc.Required[1] = rc.Required[1], rc.Required[0]
	rc.Runs[0], rc.Runs[1] = rc.Runs[1], rc.Runs[0]
	m2, err := BuildRequiredCheckManifest(rc, head)
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := ManifestDigest(m1)
	d2, _ := ManifestDigest(m2)
	if d1 != d2 || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("forge 回傳順序不得影響 digest：%s vs %s", d1, d2)
	}
}

func TestVerifyRequiredCheckManifest(t *testing.T) {
	head := forge.OID("aaaa")
	base := RequiredCheckManifest{ManifestSchema: 1,
		RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
		Runs: []CheckRunEntry{{Context: "ci", RequiredAppID: i64(42), RunName: "ci",
			RunAppID: 42, RunID: 1, HeadSHA: "aaaa", Status: "completed", Conclusion: "success"}}}
	if err := VerifyRequiredCheckManifest(base, head); err != nil {
		t.Fatal(err)
	}
	pending := base
	pending.Runs = []CheckRunEntry{{Context: "ci", RequiredAppID: i64(42), RunName: "ci",
		RunAppID: 42, RunID: 1, HeadSHA: "aaaa", Status: "in_progress"}}
	if err := VerifyRequiredCheckManifest(pending, head); err == nil {
		t.Fatal("pending 應不符（三態皆為不符，B5 §5.3(3)）")
	}
	wrongHead := base
	wrongHead.Runs[0].HeadSHA = "bbbb"
	if err := VerifyRequiredCheckManifest(wrongHead, head); err == nil {
		t.Fatal("head 不符應 fail")
	}
}

// TestVerifyRequiredCheckManifestBijection（rev9 新增——verifier bijection
// erratum 修正）：全部案例以 literal 手刻 manifest 直接呼叫 Verify，不經
// Build，證明 Verify 自身履行 §5.1(5) 保證，不是借用 Build 的前提。
func TestVerifyRequiredCheckManifestBijection(t *testing.T) {
	head := forge.OID("aaaa")
	ok := func(context string, app *int64, runName string, runApp int64, runID int64, status, concl string) CheckRunEntry {
		return CheckRunEntry{Context: context, RequiredAppID: app, RunName: runName, RunAppID: runApp,
			RunID: runID, HeadSHA: "aaaa", Status: status, Conclusion: concl}
	}
	cases := []struct {
		name    string
		m       RequiredCheckManifest
		wantErr string
	}{
		{name: "等長但 key 不對應（owner 反例）——一項重複、另一項缺漏，必須 FAIL",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("ci", i64(42), "ci", 42, 2, "completed", "success"),
				}},
			wantErr: "重複覆蓋"},
		{name: "required key 重複（Verify 端自己拒絕，非借用 Build 前提）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
				}},
			wantErr: "重複"},
		{name: "run 的 required key 不存在於 required 集合（多餘）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("lint", i64(42), "lint", 42, 1, "completed", "success"),
				}},
			wantErr: "不存在於 required 集合"},
		{name: "同一 required key 被兩筆 run 重複覆蓋",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("ci", i64(42), "ci", 42, 2, "completed", "success"),
				}},
			wantErr: "重複覆蓋"},
		{name: "required key 缺漏（無對應 run）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
				}},
			wantErr: "missing"},
		{name: "多個 required key 同時缺漏 → 錯誤訊息確定性列出全部缺漏 key（排序，非 map 疊代序）——P2-1 follow-up",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{
					{Context: "zeta"}, {Context: "lint"}, {Context: "alpha", AppID: i64(1)}},
				Runs: nil},
			// 宣告序刻意與排序後序不同（zeta, lint, alpha）——若實作仍是
			// map 遍歷＋回傳單一 key，這個完整訊息斷言必定不吻合（不穩定
			// 或直接缺漏其餘 key）；只有排序後列出全部三個 key 才會通過。
			wantErr: "alpha\x001, lint\x00*, zeta\x00*"},
		{name: "同一 run_id 歸屬兩個不同 required key（多重歸屬）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("lint", i64(42), "lint", 42, 1, "completed", "success"),
				}},
			wantErr: "多重歸屬"},
		{name: "attribution 不符：run_name ≠ context",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "not-ci", 42, 1, "completed", "success"),
				}},
			wantErr: "attribution 不符"},
		{name: "attribution 不符：run_app_id ≠ required_app_id",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 43, 1, "completed", "success"),
				}},
			wantErr: "attribution 不符"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyRequiredCheckManifest(tc.m, head)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestManifestCanonicalJSONKeyOrder——canonical digest 跨實作契約的鍵序
// golden test（B6 Task 4 follow-up 2，owner 裁定）。三個 struct 的欄位
// 宣告序目前與 spec §5.1(5) 字面序一致（RequiredCheckEntry={context,
// app_id}；CheckRunEntry={context, required_app_id, run_name, run_app_id,
// run_id, head_sha, status, conclusion}；RequiredCheckManifest=
// {manifest_schema, required_checks, runs}），但先前沒有任何測試斷言
// json.Marshal 的實際鍵序——若日後有人調換欄位宣告順序，只有跨實作
// digest 比對失敗時才會暴露。
//
// 刻意不轉 map[string]any 比較：Go 的 map 沒有固定疊代序、也不記錄
// 原始 JSON 鍵序，轉成 map 後兩個鍵序不同但值集合相同的 JSON 會比較
// 相等，等於完全測不到鍵序漂移。canonical digest 的定義基礎正是「struct
// 宣告序＝spec 字面序＝json.Marshal 輸出序」，所以必須直接比對
// json.Marshal 輸出的精確位元組／字串，而非其反解後的資料值。
//
// fixture 為完整三層 struct，且 AppID／RequiredAppID 的 nil 與非 nil
// 兩種形狀各出現一次（ci 帶 app_id=42、lint 的 app_id／required_app_id
// 皆為 nil），確保 golden 字串同時覆蓋兩種指標序列化形狀。
func TestManifestCanonicalJSONKeyOrder(t *testing.T) {
	m := RequiredCheckManifest{
		ManifestSchema: 1,
		RequiredChecks: []RequiredCheckEntry{
			{Context: "ci", AppID: i64(42)},
			{Context: "lint", AppID: nil},
		},
		Runs: []CheckRunEntry{
			{Context: "ci", RequiredAppID: i64(42), RunName: "ci", RunAppID: 42, RunID: 1,
				HeadSHA: "aaaa", Status: "completed", Conclusion: "success"},
			{Context: "lint", RequiredAppID: nil, RunName: "lint", RunAppID: 99, RunID: 2,
				HeadSHA: "aaaa", Status: "completed", Conclusion: "success"},
		},
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"manifest_schema":1,"required_checks":[{"context":"ci","app_id":42},{"context":"lint","app_id":null}],"runs":[{"context":"ci","required_app_id":42,"run_name":"ci","run_app_id":42,"run_id":1,"head_sha":"aaaa","status":"completed","conclusion":"success"},{"context":"lint","required_app_id":null,"run_name":"lint","run_app_id":99,"run_id":2,"head_sha":"aaaa","status":"completed","conclusion":"success"}]}`
	if string(got) != want {
		t.Fatalf("canonical JSON 鍵序契約破裂：\n got=%s\nwant=%s", got, want)
	}
}
```

- [ ] **Step 2: 跑 `go test -race ./internal/gatepolicy/ -run 'RequiredCheck' -count=1`，預期 FAIL（型別不存在）**
- [ ] **Step 3: 實作**

```go
// gate3_manifest.go——B5 spec §5.1(5)(6) canonical manifest 收斂。
// Digest 沿 domainspec canonical 先例：struct 宣告序＝spec 字面序，
// encoding/json 的欄位序即 canonical 序；陣列由 Build* 排序後才進 struct。
package gatepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/forge"
)

type RequiredCheckEntry struct {
	Context string `json:"context"`
	AppID   *int64 `json:"app_id"`
}
type CheckRunEntry struct {
	Context       string `json:"context"`
	RequiredAppID *int64 `json:"required_app_id"`
	RunName       string `json:"run_name"`
	RunAppID      int64  `json:"run_app_id"`
	RunID         int64  `json:"run_id"`
	HeadSHA       string `json:"head_sha"`
	Status        string `json:"status"`
	Conclusion    string `json:"conclusion"`
}
type RequiredCheckManifest struct {
	ManifestSchema int                  `json:"manifest_schema"`
	RequiredChecks []RequiredCheckEntry `json:"required_checks"`
	Runs           []CheckRunEntry      `json:"runs"`
}

func ManifestDigest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func keyOf(ctx string, app *int64) string {
	if app == nil {
		return ctx + "\x00*"
	}
	return fmt.Sprintf("%s\x00%d", ctx, *app)
}

// BuildRequiredCheckManifest：attribution＋current-effective＋排序＋一對一
// coverage（B5 §5.1(5)）。任何違規（重複 required key、歸屬歧義、缺漏、
// 一 run 多歸屬）→ error，不產出部分 manifest。
func BuildRequiredCheckManifest(rc forge.RequiredChecks, head forge.OID) (RequiredCheckManifest, error) {
	seen := map[string]bool{}
	for _, r := range rc.Required {
		k := keyOf(r.Context, r.AppID)
		if seen[k] {
			return RequiredCheckManifest{}, fmt.Errorf("required check 重複 key：%s", k)
		}
		seen[k] = true
	}
	used := map[int64]string{} // run_id → 已歸屬的 required key（一 run 至多歸屬一 required）
	var runs []CheckRunEntry
	for _, r := range rc.Required {
		var candidates []forge.CheckRun
		apps := map[int64]bool{}
		for _, cr := range rc.Runs {
			if cr.Name != r.Context {
				continue
			}
			if r.AppID != nil && cr.AppID != *r.AppID {
				continue
			}
			candidates = append(candidates, cr)
			apps[cr.AppID] = true
		}
		if r.AppID == nil && len(apps) > 1 {
			return RequiredCheckManifest{}, fmt.Errorf("required %q（app_id 不限）歸屬歧義：%d 個不同 app 的同名 run", r.Context, len(apps))
		}
		if len(candidates) == 0 {
			return RequiredCheckManifest{}, fmt.Errorf("required %s missing：無可歸屬 run", keyOf(r.Context, r.AppID))
		}
		// current-effective：started_at 最新、tie run_id 大者。RFC3339 嚴格
		// parse 後以 time.Time 比較（rev2 修——字典序在不同時區偏移下
		// 不等於時間序）；格式錯誤 fail loud。
		effIdx := -1
		var effTime time.Time
		for i, c := range candidates {
			ts, perr := time.Parse(time.RFC3339, c.StartedAt)
			if perr != nil {
				return RequiredCheckManifest{}, fmt.Errorf("run %d started_at 非 RFC3339：%q", c.RunID, c.StartedAt)
			}
			if effIdx < 0 || ts.After(effTime) ||
				(ts.Equal(effTime) && c.RunID > candidates[effIdx].RunID) {
				effIdx, effTime = i, ts
			}
		}
		eff := candidates[effIdx]
		if prev, ok := used[eff.RunID]; ok {
			return RequiredCheckManifest{}, fmt.Errorf("run %d 多重歸屬：%s 與 %s", eff.RunID, prev, keyOf(r.Context, r.AppID))
		}
		used[eff.RunID] = keyOf(r.Context, r.AppID)
		runs = append(runs, CheckRunEntry{Context: r.Context, RequiredAppID: r.AppID,
			RunName: eff.Name, RunAppID: eff.AppID, RunID: eff.RunID, HeadSHA: string(eff.HeadOID),
			Status: eff.Status, Conclusion: eff.Conclusion})
	}
	required := append([]forge.RequiredCheckRef(nil), rc.Required...)
	sort.Slice(required, func(i, j int) bool { return keyOf(required[i].Context, required[i].AppID) < keyOf(required[j].Context, required[j].AppID) })
	sort.Slice(runs, func(i, j int) bool { return keyOf(runs[i].Context, runs[i].RequiredAppID) < keyOf(runs[j].Context, runs[j].RequiredAppID) })
	entries := make([]RequiredCheckEntry, len(required))
	for i, r := range required {
		entries[i] = RequiredCheckEntry{Context: r.Context, AppID: r.AppID}
	}
	return RequiredCheckManifest{ManifestSchema: 1, RequiredChecks: entries, Runs: runs}, nil
}

// VerifyRequiredCheckManifest：獨立重驗 §5.1(5) 全部驗證條件——**不依賴
// BuildRequiredCheckManifest 已先執行**（rev9 erratum 修正：exported
// verifier 必須自己履行宣稱的保證，不能借用 Build 的前提；原版僅比對
// len(RequiredChecks)==len(Runs) 判定 coverage，對「等長但 key 不對應
// （一項重複、另一項缺漏）」的輸入會誤判通過，違反 §5.1(5) bijection）。
// 逐條檢查：
//  1. required key (context, app_id) 必須唯一（Verify 自己拒絕重複，不
//     借用 Build 已去重的前提）。
//  2. 每筆 run 的 required key 必須存在於 required 集合，且同一 key 恰
//     一筆 run（不存在／重複覆蓋 fail loud）。
//  3. 每個 required key 最後都必須被覆蓋（無缺漏 fail loud）。
//  4. 同一 run_id 不得歸屬多個 required key（多重歸屬 fail loud）。
//  5. attribution 重驗：run_name == context，且 required_app_id == nil
//     或 run_app_id == required_app_id。
//  6. 全 completed+success、全 head_sha == promotion_head（既有規則）。
func VerifyRequiredCheckManifest(m RequiredCheckManifest, head forge.OID) error {
	if m.ManifestSchema != 1 {
		return fmt.Errorf("manifest_schema %d 不支援", m.ManifestSchema)
	}
	required := map[string]RequiredCheckEntry{}
	for _, rc := range m.RequiredChecks {
		k := keyOf(rc.Context, rc.AppID)
		if _, dup := required[k]; dup {
			return fmt.Errorf("required check 重複 key：%s", k)
		}
		required[k] = rc
	}
	covered := map[string]bool{}
	usedRun := map[int64]string{}
	for _, r := range m.Runs {
		k := keyOf(r.Context, r.RequiredAppID)
		rc, ok := required[k]
		if !ok {
			return fmt.Errorf("run %d 歸屬 required key %s 不存在於 required 集合（多餘）", r.RunID, k)
		}
		if covered[k] {
			return fmt.Errorf("required key %s 重複覆蓋：多筆 run 歸屬同一 required check", k)
		}
		if prevKey, dup := usedRun[r.RunID]; dup {
			return fmt.Errorf("run %d 多重歸屬：%s 與 %s", r.RunID, prevKey, k)
		}
		if r.RunName != rc.Context {
			return fmt.Errorf("run %d attribution 不符：run_name=%q ≠ context=%q", r.RunID, r.RunName, rc.Context)
		}
		if rc.AppID != nil && r.RunAppID != *rc.AppID {
			return fmt.Errorf("run %d attribution 不符：run_app_id=%d ≠ required_app_id=%d", r.RunID, r.RunAppID, *rc.AppID)
		}
		covered[k] = true
		usedRun[r.RunID] = k
		if r.Status != "completed" || r.Conclusion != "success" {
			return fmt.Errorf("required %q 非 success（status=%s conclusion=%s）", k, r.Status, r.Conclusion)
		}
		if r.HeadSHA != string(head) {
			return fmt.Errorf("required %q head %s ≠ promotion_head %s", k, r.HeadSHA, head)
		}
	}
	var missing []string
	for k := range required {
		if !covered[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		// 比照 gate2.go:183-189 先例：多筆缺漏時收集後排序再組訊息，
		// 避免 map 疊代序讓回報的缺漏 key 不確定；列出全部缺漏 key
		// 以提高診斷價值（owner 裁定方向，P2-1 follow-up）。
		sort.Strings(missing)
		return fmt.Errorf("required missing：無覆蓋 run：%s", strings.Join(missing, ", "))
	}
	return nil
}
```

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；`go vet ./internal/gatepolicy/`**
- [ ] **Step 5: Commit**

```bash
git add internal/gatepolicy/gate3_manifest.go internal/gatepolicy/gate3_manifest_test.go
git commit -m "feat(gatepolicy): B6 Task 4——required-check manifest 收斂（bijection／current-effective／(context,app_id) key，B5 §5.1(5)）"
```

---

### Task 5: review section 收斂（eligibility＋current-effective＋零 CHANGES_REQUESTED）

**rev10 範圍收斂（owner 裁決，B5 spec rev9 對應）**：本 Task **不建立 manifest 外殼、不計算 digest**——完整 `review_evidence_provenance` 還包含 evidence section，`manifest_schema: 1` 與最終 digest 留給 **C1** 於決議時組合。Task 5 只產出 review **section**（`[]ReviewEntry`）與其 Build／Verify。

**Files:**
- Modify: `internal/gatepolicy/gate3_manifest.go`（追加）
- Test: `internal/gatepolicy/gate3_manifest_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 的 `forge.Review`／`forge.Permission`。
- Produces:
  - `type ReviewEntry struct{ ReviewerLogin, Permission string; ReviewID int64; State, ReviewedHeadSHA, SubmittedAt string }`（`SubmittedAt` 為正規化後的 **UTC RFC3339Nano** canonical value，非 forge 原始字面值——見下）
  - `func BuildReviewSection(reviews []forge.Review, perms map[string]forge.Permission) ([]ReviewEntry, error)`——僅具效力 reviewer（write／maintain／admin）、每人一筆 current-effective（依 `submitted_at` 解析後的 `time.Time` 比較，tie 取 `review_id` 大者）、依 `login` 排序。**Fail-closed 規則（rev10——修正 perms map 零值語意造成的 CR 方向 fail-open，B5 §6）**：`state ∈ {APPROVED, CHANGES_REQUESTED, DISMISSED}` 的 review，其 reviewer 的 permission **key 必須存在**於 `perms`，缺漏（查無／未查詢）不得等同 `PermissionNone`，須 fail loud；permission 值須為已知列舉（`admin／maintain／write／read／none`），未知值（含空字串）→fail loud；`COMMENTED／PENDING` 不參與 current-effective，可不要求 permission；**未知 review state 不得靜默跳過**，須 fail loud。寫入 `SubmittedAt` 前正規化為 `ts.UTC().Format(time.RFC3339Nano)`（rev10 新增契約——固定 digest preimage，不用 `time.RFC3339`，避免丟失 fractional seconds）。
  - `func VerifyReviewSection(entries []ReviewEntry, head forge.OID) error`——**結構性重驗**（rev10，沿 Task 4 exported verifier 原則）：(1) `reviewer_login` 嚴格遞增；(2) `permission` 是已知列舉值且必須 eligible；(3) `state` 僅能是 `APPROVED／CHANGES_REQUESTED／DISMISSED`；(4) `submitted_at` 合法 RFC3339 且等於重新格式化的 canonical value；(5) ≥1 current-effective `APPROVED@head`、零 `CHANGES_REQUESTED`。**範圍聲明（doc-comment 明文，rev10）**：僅驗證 section 自身的 canonical／決議不變量，**不證明**其完整來自 Forge（例如 caller 把某具效力 reviewer 的 CHANGES_REQUESTED 整筆刪除後，剩餘 section 仍可能滿足全部五項檢查——這是 `[]ReviewEntry` 單獨無法證明的資訊缺口）；完整性由 **C1** 於決議時重新 `GetReviews`、查齊 permissions、`BuildReviewSection`、`VerifyReviewSection`、組合 manifest 並比對 digest 保證（B5 spec §5.3(5)）。

- [ ] **Step 1: 寫 failing tests**

```go
func rev(login, state, head, at string, id int64) forge.Review {
	return forge.Review{ReviewID: id, ReviewerLogin: login, State: state,
		ReviewedHeadOID: forge.OID(head), SubmittedAt: at}
}

func TestBuildReviewSection(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite, "eve": forge.PermissionRead}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T02:00:00Z", 2), // 後者 supersede
		rev("alice", "COMMENTED", "aaaa", "2026-08-28T03:00:00Z", 3),         // COMMENTED 不改變有效狀態
		rev("eve", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 4),            // 明確 read 權限：不入 section
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ReviewerLogin != "alice" || entries[0].State != "CHANGES_REQUESTED" {
		t.Fatalf("僅 alice 的 current-effective（CR）應入 section：%+v", entries)
	}
	if entries[0].SubmittedAt != "2026-08-28T02:00:00Z" {
		t.Fatalf("SubmittedAt 應正規化為 UTC RFC3339Nano：%q", entries[0].SubmittedAt)
	}
}

// TestBuildReviewSectionPermissionNoneExcluded：明確 none（key 存在、值為
// none）——安全排除，不入 section，不視為錯誤（區分於 key 缺漏，rev10）。
func TestBuildReviewSectionPermissionNoneExcluded(t *testing.T) {
	perms := map[string]forge.Permission{"bob": forge.PermissionNone}
	entries, err := BuildReviewSection([]forge.Review{
		rev("bob", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("明確 none 應安全排除：%+v", entries)
	}
}

// TestBuildReviewSectionPermissionKeyMissingFailsLoud：perms 完全沒有該
// reviewer 的 key（未查詢／查詢失敗）——不得等同 none，須 fail loud
//（B5 §6 fail-closed；rev10 修正原「無紀錄→None：不入」的 fail-open 缺口）。
func TestBuildReviewSectionPermissionKeyMissingFailsLoud(t *testing.T) {
	perms := map[string]forge.Permission{}
	_, err := BuildReviewSection([]forge.Review{
		rev("bob", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err == nil || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("permission key 缺漏應 fail loud，got %v", err)
	}
}

// TestBuildReviewSectionUnknownPermissionValueFailsLoud：key 存在但值非
// 已知列舉（含空字串）——fail loud，不得靜默視為不具效力。
func TestBuildReviewSectionUnknownPermissionValueFailsLoud(t *testing.T) {
	cases := map[string]forge.Permission{"typo": forge.Permission("writeXX"), "empty": forge.Permission("")}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			perms := map[string]forge.Permission{"bob": p}
			_, err := BuildReviewSection([]forge.Review{
				rev("bob", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
			}, perms)
			if err == nil || !strings.Contains(err.Error(), "未知") {
				t.Fatalf("未知 permission 值應 fail loud，got %v", err)
			}
		})
	}
}

// TestBuildReviewSectionUnknownStateFailsLoud：非白名單 review state 不得
// 靜默跳過（rev10 新規則）。
func TestBuildReviewSectionUnknownStateFailsLoud(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	_, err := BuildReviewSection([]forge.Review{
		rev("alice", "REQUEST_CHANGES_TYPO", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err == nil || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("未知 state 應 fail loud，got %v", err)
	}
}

// TestBuildReviewSectionPendingNotRequirePermission：COMMENTED／PENDING
// 不參與 current-effective，可不要求 permission（即使 key 缺漏也不報錯，
// 因為根本不查）。
func TestBuildReviewSectionPendingNotRequirePermission(t *testing.T) {
	perms := map[string]forge.Permission{}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "PENDING", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("alice", "COMMENTED", "aaaa", "2026-08-28T02:00:00Z", 2),
	}, perms)
	if err != nil {
		t.Fatalf("PENDING／COMMENTED 不應要求 permission：%v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("PENDING／COMMENTED 不入 section：%+v", entries)
	}
}

func TestBuildReviewSectionTimezoneAndParseError(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	// 03:00+08:00 實際早於 01:00Z——字典序會誤判為較新（rev2）
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T03:00:00+08:00", 1),
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 2),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].State != "APPROVED" {
		t.Fatalf("current-effective 應依實際時間為 APPROVED：%+v", entries)
	}
	if _, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "bad-time", 1)}, perms); err == nil {
		t.Fatal("submitted_at 非 RFC3339 應 fail loud")
	}
}

// TestBuildReviewSectionTieBreakReviewID：同一 reviewer 兩筆 review 的
// submitted_at 相同時，取 review_id 較大者為 current-effective。
func TestBuildReviewSectionTieBreakReviewID(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 5),
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 9),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].State != "APPROVED" || entries[0].ReviewID != 9 {
		t.Fatalf("tie-break 應取 review_id 較大者：%+v", entries)
	}
}

// TestBuildReviewSectionSubmittedAtNormalization：rev10 新增契約——寫入
// ReviewEntry 的 SubmittedAt 為 UTC RFC3339Nano canonical value，且
// 2026-08-28T01:00:00Z 與 2026-08-28T01:00:00+00:00 兩種輸入表示同一時刻，
// 必須產出完全相同的 section bytes（固定 digest preimage，避免決議時
// 重讀重算產生假 mismatch）。
func TestBuildReviewSectionSubmittedAtNormalization(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	a, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00+00:00", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("Z 與 +00:00 兩種表示應產出相同 section bytes：%s vs %s", ab, bb)
	}
	// 帶 fractional seconds 的輸入不得被截斷精度（用 RFC3339Nano，非 RFC3339）。
	frac, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00.123456789Z", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if frac[0].SubmittedAt != "2026-08-28T01:00:00.123456789Z" {
		t.Fatalf("RFC3339Nano 正規化不得丟失 fractional seconds：%q", frac[0].SubmittedAt)
	}
}

// TestBuildReviewSectionOrderIndependent：reviews 輸入順序反轉，section
// bytes 完全相同（B5 共同規則——forge 回傳順序不得影響 digest）；另外明確
// 斷言輸出順序為 alice, bob（canonical section 依 reviewer_login 字典序
// 升冪）。這兩個斷言證明不同性質：前者證明 Forge 輸入順序不影響輸出，
// 後者證明輸出本身確實依升冪排序——只留前者時「移除 sort」的 mutation
// 鑑別力是機率性的（Go map 疊代分布未承諾均勻，見 rev10 修訂記錄末尾）。
func TestBuildReviewSectionOrderIndependent(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite, "bob": forge.PermissionMaintain}
	reviews := []forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("bob", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 2),
	}
	m1, err := BuildReviewSection(reviews, perms)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1) != 2 || m1[0].ReviewerLogin != "alice" || m1[1].ReviewerLogin != "bob" {
		t.Fatalf("canonical section 須依 reviewer_login 字典序升冪：got=%+v", m1)
	}
	reversed := []forge.Review{reviews[1], reviews[0]}
	m2, err := BuildReviewSection(reversed, perms)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(m1)
	b2, _ := json.Marshal(m2)
	if string(b1) != string(b2) {
		t.Fatalf("輸入順序不得影響 section bytes：%s vs %s", b1, b2)
	}
}

// TestReviewEntryCanonicalJSONKeyOrder——canonical digest 跨實作契約的
// 鍵序 golden test（比照 Task 4 TestManifestCanonicalJSONKeyOrder 形狀）。
func TestReviewEntryCanonicalJSONKeyOrder(t *testing.T) {
	entries := []ReviewEntry{
		{ReviewerLogin: "alice", Permission: "write", ReviewID: 1, State: "APPROVED",
			ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
	}
	got, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"reviewer_login":"alice","permission":"write","review_id":1,"state":"APPROVED","reviewed_head_sha":"aaaa","submitted_at":"2026-08-28T01:00:00Z"}]`
	if string(got) != want {
		t.Fatalf("canonical JSON 鍵序契約破裂：\n got=%s\nwant=%s", got, want)
	}
}

func TestVerifyReviewSection(t *testing.T) {
	head := forge.OID("aaaa")
	ok := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "APPROVED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(ok, head); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReviewSection(nil, head); err == nil {
		t.Fatal("零 review 應不符")
	}
	staleHead := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "APPROVED", ReviewedHeadSHA: "bbbb", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(staleHead, head); err == nil {
		t.Fatal("過期 head 的 approval 不算")
	}
	withCR := []ReviewEntry{ok[0], {ReviewerLogin: "carol",
		Permission: "write", ReviewID: 2, State: "CHANGES_REQUESTED",
		ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(withCR, head); err == nil {
		t.Fatal("存在具效力 CHANGES_REQUESTED 應不符（owner 裁決：零 CR）")
	}
	dismissed := []ReviewEntry{ok[0], {ReviewerLogin: "dave", Permission: "write", ReviewID: 2,
		State: "DISMISSED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T02:00:00Z"}}
	if err := VerifyReviewSection(dismissed, head); err != nil {
		t.Fatalf("DISMISSED 不計入亦不阻擋：%v", err)
	}
}

// TestVerifyReviewSectionDismissedOnlyNoApproval：具效力 reviewer 的
// current-effective 是 DISMISSED、且無其他 APPROVED——不得通過（DISMISSED
// 不計入亦不阻擋 ≠ 視為 approval）。
func TestVerifyReviewSectionDismissedOnlyNoApproval(t *testing.T) {
	entries := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "DISMISSED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(entries, forge.OID("aaaa")); err == nil {
		t.Fatal("僅 DISMISSED、無 APPROVED 應不符")
	}
}

// TestVerifyReviewSectionStructuralInvariants：Verify 自身履行的四項結構
// 性檢查（rev10——沿 Task 4 exported verifier 原則），各自獨立負向案例；
// 全部以 literal 手刻 entries 直接呼叫 Verify，不經 Build。
func TestVerifyReviewSectionStructuralInvariants(t *testing.T) {
	head := forge.OID("aaaa")
	base := func() ReviewEntry {
		return ReviewEntry{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
			State: "APPROVED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}
	}
	cases := []struct {
		name    string
		entries []ReviewEntry
		wantErr string
	}{
		{name: "reviewer_login 非嚴格遞增（重複）",
			entries: []ReviewEntry{base(), base()},
			wantErr: "非嚴格遞增"},
		{name: "reviewer_login 非嚴格遞增（逆序）",
			entries: []ReviewEntry{
				{ReviewerLogin: "bob", Permission: "write", ReviewID: 1, State: "APPROVED",
					ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
				{ReviewerLogin: "alice", Permission: "write", ReviewID: 2, State: "APPROVED",
					ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
			},
			wantErr: "非嚴格遞增"},
		{name: "permission 未知列舉值",
			entries: func() []ReviewEntry { e := base(); e.Permission = "superuser"; return []ReviewEntry{e} }(),
			wantErr: "未知"},
		{name: "permission 為明確不具效力值（read）不應出現於 section",
			entries: func() []ReviewEntry { e := base(); e.Permission = "read"; return []ReviewEntry{e} }(),
			wantErr: "不具效力"},
		{name: "state 不在白名單",
			entries: func() []ReviewEntry { e := base(); e.State = "COMMENTED"; return []ReviewEntry{e} }(),
			wantErr: "白名單"},
		{name: "submitted_at 非 RFC3339",
			entries: func() []ReviewEntry { e := base(); e.SubmittedAt = "not-a-time"; return []ReviewEntry{e} }(),
			wantErr: "非 RFC3339"},
		{name: "submitted_at 非 canonical（+00:00 而非 Z）",
			entries: func() []ReviewEntry { e := base(); e.SubmittedAt = "2026-08-28T01:00:00+00:00"; return []ReviewEntry{e} }(),
			wantErr: "非 canonical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyReviewSection(tc.entries, head)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
```

- [ ] **Step 2: 跑 `go test -race ./internal/gatepolicy/ -run 'ReviewSection' -count=1`，預期 FAIL**
- [ ] **Step 3: 實作**

```go
type ReviewEntry struct {
	ReviewerLogin   string `json:"reviewer_login"`
	Permission      string `json:"permission"`
	ReviewID        int64  `json:"review_id"`
	State           string `json:"state"`
	ReviewedHeadSHA string `json:"reviewed_head_sha"`
	SubmittedAt     string `json:"submitted_at"`
}

// BuildReviewSection：每具效力 reviewer 至多一筆 current-effective review
//（B5 §5.1(6)）。eligibility＝permission ∈ {write,maintain,admin}；不具
// 效力者完全不入（approval 不放行、CHANGES_REQUESTED 亦不阻擋）。
// current-effective＝state ∈ {APPROVED,CHANGES_REQUESTED,DISMISSED} 中
// submitted_at（解析後 time.Time）最新者（tie 取 review_id 大者）；
// COMMENTED／PENDING 不參與、不入 section。
//
// Fail-closed 規則（rev10——修正 perms map 零值語意造成的 CR 方向
// fail-open，B5 §6）：
//   - state ∈ {APPROVED,CHANGES_REQUESTED,DISMISSED} 的 review，其
//     reviewer 的 permission key 必須存在於 perms；缺漏（查無／未查詢）
//     不得等同 PermissionNone，須 fail loud。
//   - permission 值必須是已知列舉（admin／maintain／write／read／none）；
//     未知值（含空字串）→fail loud。
//   - COMMENTED／PENDING 不參與 current-effective，可不要求 permission。
//   - 未知 review state（非上述五種）不得靜默跳過，須 fail loud。
//
// SubmittedAt 正規化（rev10 新增契約——固定 digest preimage）：寫入
// ReviewEntry 的 SubmittedAt 為解析後時間值的 UTC RFC3339Nano 表示
//（ts.UTC().Format(time.RFC3339Nano)，非 time.RFC3339——避免丟失
// fractional seconds）；current-effective 收斂比較仍用解析後的 time.Time。
func BuildReviewSection(reviews []forge.Review, perms map[string]forge.Permission) ([]ReviewEntry, error) {
	type effRev struct {
		r  forge.Review
		at time.Time
	}
	eff := map[string]effRev{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			p, ok := perms[r.ReviewerLogin]
			if !ok {
				return nil, fmt.Errorf("review %d（reviewer %s, state %s）缺少 permission 查詢結果，不得等同 none（fail closed，B5 §6）", r.ReviewID, r.ReviewerLogin, r.State)
			}
			switch p {
			case forge.PermissionAdmin, forge.PermissionMaintain, forge.PermissionWrite, forge.PermissionRead, forge.PermissionNone:
			default:
				return nil, fmt.Errorf("review %d（reviewer %s）permission 值未知：%q", r.ReviewID, r.ReviewerLogin, string(p))
			}
			if !p.Eligible() {
				continue
			}
		case "COMMENTED", "PENDING":
			continue
		default:
			return nil, fmt.Errorf("review %d（reviewer %s）未知 state：%q（不得靜默跳過）", r.ReviewID, r.ReviewerLogin, r.State)
		}
		ts, perr := time.Parse(time.RFC3339, r.SubmittedAt)
		if perr != nil {
			return nil, fmt.Errorf("review %d submitted_at 非 RFC3339：%q", r.ReviewID, r.SubmittedAt)
		}
		cur, ok := eff[r.ReviewerLogin]
		if !ok || ts.After(cur.at) || (ts.Equal(cur.at) && r.ReviewID > cur.r.ReviewID) {
			eff[r.ReviewerLogin] = effRev{r: r, at: ts}
		}
	}
	logins := make([]string, 0, len(eff))
	for l := range eff {
		logins = append(logins, l)
	}
	sort.Strings(logins)
	out := make([]ReviewEntry, 0, len(logins))
	for _, l := range logins {
		e := eff[l]
		out = append(out, ReviewEntry{ReviewerLogin: l, Permission: string(perms[l]),
			ReviewID: e.r.ReviewID, State: e.r.State, ReviewedHeadSHA: string(e.r.ReviewedHeadOID),
			SubmittedAt: e.at.UTC().Format(time.RFC3339Nano)})
	}
	return out, nil
}

// VerifyReviewSection：結構性重驗 section 自身的 canonical／決議不變量
//（rev10——沿 Task 4 exported verifier 原則：exported verifier 必須自行
// 履行其宣稱的契約，不能只依賴 Build 已先驗證的前提）：
//  1. reviewer_login 嚴格遞增（連帶保證排序與唯一性）。
//  2. permission 是已知列舉值且必須 eligible（write／maintain／admin）。
//  3. state 僅能是 APPROVED／CHANGES_REQUESTED／DISMISSED。
//  4. submitted_at 合法 RFC3339，且等於重新格式化的 UTC RFC3339Nano
//     canonical value（非 canonical 表示 → fail loud）。
//  5. 至少一筆 current-effective APPROVED @ head、零 CHANGES_REQUESTED；
//     DISMISSED 不計入亦不阻擋。
//
// 範圍聲明：本函式僅驗證 section 自身的 canonical／決議不變量，
// **不證明**其完整來自 Forge——例如 caller 把某具效力 reviewer 的
// CHANGES_REQUESTED 整筆刪除後，剩餘 section 仍可能滿足以上五項全部
// 檢查（遞增、permission 列舉且 eligible、state 白名單、canonical
// timestamp、零 CR），這是 []ReviewEntry 單獨無法證明的資訊缺口。
// 完整性由 C1 於決議時重新 GetReviews、查齊 permissions、
// BuildReviewSection、VerifyReviewSection、組合 manifest 並比對 digest
// 保證（B5 spec §5.3(5)）。
func VerifyReviewSection(entries []ReviewEntry, head forge.OID) error {
	approvedAtHead := false
	prevLogin := ""
	for i, e := range entries {
		if i > 0 && e.ReviewerLogin <= prevLogin {
			return fmt.Errorf("reviewer_login 非嚴格遞增：%q 之後接 %q", prevLogin, e.ReviewerLogin)
		}
		prevLogin = e.ReviewerLogin

		switch forge.Permission(e.Permission) {
		case forge.PermissionWrite, forge.PermissionMaintain, forge.PermissionAdmin:
		case forge.PermissionRead, forge.PermissionNone:
			return fmt.Errorf("reviewer %s permission=%s 不具效力，不應出現於 section", e.ReviewerLogin, e.Permission)
		default:
			return fmt.Errorf("reviewer %s permission 值未知：%q", e.ReviewerLogin, e.Permission)
		}

		switch e.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			return fmt.Errorf("reviewer %s state 不在白名單：%q", e.ReviewerLogin, e.State)
		}

		ts, perr := time.Parse(time.RFC3339, e.SubmittedAt)
		if perr != nil {
			return fmt.Errorf("reviewer %s submitted_at 非 RFC3339：%q", e.ReviewerLogin, e.SubmittedAt)
		}
		if canonical := ts.UTC().Format(time.RFC3339Nano); e.SubmittedAt != canonical {
			return fmt.Errorf("reviewer %s submitted_at 非 canonical UTC RFC3339Nano：%q（應為 %q）", e.ReviewerLogin, e.SubmittedAt, canonical)
		}

		switch e.State {
		case "CHANGES_REQUESTED":
			return fmt.Errorf("reviewer %s 有 current-effective CHANGES_REQUESTED（零 CR 條件）", e.ReviewerLogin)
		case "APPROVED":
			if e.ReviewedHeadSHA == string(head) {
				approvedAtHead = true
			}
		}
	}
	if !approvedAtHead {
		return fmt.Errorf("無 current-effective APPROVED 於 promotion_head %s", head)
	}
	return nil
}
```

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；跑 `go test -race ./internal/gatepolicy/ -count=1` 全包**
- [ ] **Step 5: Commit**

```bash
git add internal/gatepolicy/gate3_manifest.go internal/gatepolicy/gate3_manifest_test.go
git commit -m "feat(gatepolicy): B6 Task 5——review section 收斂（eligibility／current-effective／零 CR／submitted_at 正規化，B5 §5.1(6)）"
```

---

### Task 6: `gate3_promotion` policy 骨架＋註冊

**Files:**
- Create: `internal/gatepolicy/gate3.go`
- Test: `internal/gatepolicy/gate3_test.go`
- Modify: `app.go`（`ensureGate` 的 registry，app.go:3888-3894）

**Interfaces:**
- Consumes: `gate.GatePolicy` interface（policy.go）、`gate.Binding`／`gate.GateRequest`／`gate.StaleCause`；Task 4-5 的 Build/Verify（C1 於 deps 內呼叫，本 task 僅編排）。
- Produces:
  - `type Gate3Deps struct{ VerifyTaskRun func(taskRunID, snapshotDigest string) error; VerifyForge func(promotionHead, mainBase, requiredCheckDigest string) error; VerifyProvenance func(taskRunID, provenanceDigest, promotionHead string) error }`
  - `func NewGate3Policy(deps Gate3Deps) gate.GatePolicy`——註冊名 `"gate3_promotion"`、subject 形狀 `taskrun:<ULID>`；deps 為 nil 時對應檢查回「gate3: dependency not wired（C1）」error（fail closed）。

- [ ] **Step 1: 寫 failing tests**

```go
package gatepolicy

import (
	"errors"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

func gate3Bindings() []gate.Binding {
	d := "sha256:" + strings.Repeat("ab", 32)
	oid := "git:sha1:" + strings.Repeat("a", 40)
	return []gate.Binding{
		{Kind: "task_run", Ref: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Digest: d},
		{Kind: "promotion_head", Ref: oid, Digest: oid},
		{Kind: "main_base", Ref: oid, Digest: oid},
		{Kind: "oracle_surface", Digest: d},
		{Kind: "required_check_manifest", Digest: d},
		{Kind: "review_evidence_provenance", Digest: d},
	}
}

func TestGate3ValidateRequestBindingShapes(t *testing.T) {
	p := NewGate3Policy(Gate3Deps{})
	ok := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if err := p.ValidateRequest(ok); err != nil {
		t.Fatal(err)
	}
	missing := ok
	missing.Bindings = ok.Bindings[:5] // 少 review_evidence_provenance
	if err := p.ValidateRequest(missing); err == nil {
		t.Fatal("缺 binding 應拒絕")
	}
	badSubject := ok
	badSubject.Subject = "plan:x"
	if err := p.ValidateRequest(badSubject); err == nil {
		t.Fatal("subject 形狀錯誤應拒絕")
	}
	crossed := ok
	crossed.Subject = "taskrun:01BX5ZZKBKACTAV9WEVGEMMVRZ" // 形狀合法但 ≠ task_run binding
	if err := p.ValidateRequest(crossed); err == nil {
		t.Fatal("subject 與 task_run binding 不一致應拒絕（rev2 P1）")
	}
}

func TestGate3BuildDecisionApprovedRunsAllChecksFailClosed(t *testing.T) {
	var order []string
	deps := Gate3Deps{
		VerifyTaskRun:    func(id, dg string) error { order = append(order, "taskrun"); return nil },
		VerifyForge:      func(h, b, d string) error { order = append(order, "forge"); return nil },
		VerifyProvenance: func(id, d, h string) error { order = append(order, "provenance"); return nil },
	}
	p := NewGate3Policy(deps)
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "taskrun,forge,provenance" {
		t.Fatalf("重驗順序應為 §5.3 序：%v", order)
	}
	// 任一 deps 失敗 → fail closed
	deps.VerifyForge = func(h, b, d string) error { return errors.New("head moved") }
	p = NewGate3Policy(deps)
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil {
		t.Fatal("forge 重驗失敗應 fail closed")
	}
}

func TestGate3BuildDecisionRejectedSkipsReverify(t *testing.T) {
	called := false
	p := NewGate3Policy(Gate3Deps{VerifyTaskRun: func(id, dg string) error { called = true; return nil }})
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "rejected", gate.DecisionInput{}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("rejected 分支跳過重驗（B5 §5.3(6)）")
	}
}

func TestGate3ReconcileBindingsPendingPseudoRecordEmpty(t *testing.T) {
	// rev7（plan gate 第六輪 P1）：pending pseudo-record 必須回空——
	// PrepareDecision 的前置檢查會把 cause 轉成未包裝一般錯誤並跳過
	// BuildDecision（service.go:107），pending 會繞過 expired 永久滯留。
	p := NewGate3Policy(Gate3Deps{})
	causes, err := p.ReconcileBindings(gate.ApprovalRecord{
		Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Decision: "", // pending pseudo-record 形狀
	})
	if err != nil || len(causes) != 0 {
		t.Fatalf("pending pseudo-record 必須回 (nil, nil)：causes=%v err=%v", causes, err)
	}
}

func TestGate3NilDepsFailClosed(t *testing.T) {
	p := NewGate3Policy(Gate3Deps{})
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV", Bindings: gate3Bindings()}
	if _, err := p.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil ||
		!strings.Contains(err.Error(), "not wired") {
		t.Fatal("deps 未接線應 fail closed 且錯誤具名")
	}
}
```

- [ ] **Step 2: 跑 `go test -race ./internal/gatepolicy/ -run Gate3 -count=1`，預期 FAIL**
- [ ] **Step 3: 實作 `gate3.go`**

```go
// gate3.go——B5 spec §5 的 Gate 3 policy 骨架（B6）：binding 形狀驗證與
// 決議時重驗「編排」在此凍結；重驗的三組實體檢查（TaskRun currentness、
// forge 現時狀態、provenance 重建）以 Gate3Deps 注入，C1a/C1b/C1c 接線。
// deps 未接線時 fail closed——註冊存在但不可能誤放行。
package gatepolicy

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

var reTaskRunRef = regexp.MustCompile(`^taskrun:[0-9A-HJKMNP-TV-Z]{26}$`)

// ErrGate3Mismatch：重驗「已確認不符」的 sentinel（rev2——B5 §4.3 的
// mismatch／transient 區分）。deps 實作以 fmt.Errorf("…: %w", ErrGate3Mismatch)
// 標記**確認不符**（重驗完成且結果為不符→pending request 應轉終態）；
// 未包裝的 error＝transient（forge 讀取失敗等——無法決議、維持 pending，
// 不得轉終態）。BuildDecision 原樣傳遞（%w），呼叫端以 errors.Is 判斷。
var ErrGate3Mismatch = errors.New("gate3: 重驗確認不符")

type Gate3Deps struct {
	VerifyTaskRun    func(taskRunID, snapshotDigest string) error            // §5.3(1)
	VerifyForge      func(promotionHead, mainBase, requiredCheckDigest string) error // §5.3(2)(3)(4)
	VerifyProvenance func(taskRunID, provenanceDigest, promotionHead string) error   // §5.3(5)
}

type gate3BindingReq struct {
	kind     string
	digestRe *regexp.Regexp
	refRe    *regexp.Regexp
}

var gate3BindingReqs = []gate3BindingReq{
	{kind: "task_run", digestRe: reSHA256, refRe: reTaskRunRef},
	{kind: "promotion_head", digestRe: reGitOID},
	{kind: "main_base", digestRe: reGitOID},
	{kind: "oracle_surface", digestRe: reSHA256},
	{kind: "required_check_manifest", digestRe: reSHA256},
	{kind: "review_evidence_provenance", digestRe: reSHA256},
}

type Gate3Policy struct{ deps Gate3Deps }

var _ gate.GatePolicy = (*Gate3Policy)(nil)

func NewGate3Policy(deps Gate3Deps) gate.GatePolicy { return &Gate3Policy{deps: deps} }

func (p *Gate3Policy) ValidateRequest(req gate.GateRequest) error {
	if !reTaskRunRef.MatchString(req.Subject) {
		return fmt.Errorf("gate3: subject 形狀必須為 taskrun:<ULID>，得 %q", req.Subject)
	}
	found := map[string]gate.Binding{}
	// rev2（plan gate P1）：subject 必須與 task_run binding 完全相等——否則
	// subject A 可綁 TaskRun B：重驗 B、supersession／completed 卻依 A 處理。
	// （相等檢查在 bindings 迴圈後、以 found["task_run"] 執行，見下。）
	for _, b := range req.Bindings {
		if _, dup := found[b.Kind]; dup {
			return fmt.Errorf("gate3: binding %q 重複", b.Kind)
		}
		found[b.Kind] = b
	}
	for _, r := range gate3BindingReqs {
		b, ok := found[r.kind]
		if !ok {
			return fmt.Errorf("gate3: 缺 binding %q", r.kind)
		}
		if r.digestRe != nil && !r.digestRe.MatchString(b.Digest) {
			return fmt.Errorf("gate3: binding %q digest 形狀不符：%q", r.kind, b.Digest)
		}
		if r.refRe != nil && !r.refRe.MatchString(b.Ref) {
			return fmt.Errorf("gate3: binding %q ref 形狀不符：%q", r.kind, b.Ref)
		}
	}
	if len(req.Bindings) != len(gate3BindingReqs) {
		return fmt.Errorf("gate3: binding 數 %d ≠ %d（不得有未知 binding）", len(req.Bindings), len(gate3BindingReqs))
	}
	if tr := found["task_run"]; tr.Ref != req.Subject {
		return fmt.Errorf("gate3: subject %q 與 task_run binding %q 不一致", req.Subject, tr.Ref)
	}
	return nil
}

// BuildDecision：approved 分支執行 §5.3 決議時重驗（fail closed）；
// rejected 分支僅需 reason（由 Service 層既有驗證），跳過重驗。
func (p *Gate3Policy) BuildDecision(req gate.GateRequest, decision string, _ gate.DecisionInput) (*gate.Metadata, error) {
	if decision != "approved" {
		return nil, nil
	}
	b := map[string]gate.Binding{}
	for _, x := range req.Bindings {
		b[x.Kind] = x
	}
	if p.deps.VerifyTaskRun == nil || p.deps.VerifyForge == nil || p.deps.VerifyProvenance == nil {
		return nil, fmt.Errorf("gate3: dependency not wired（C1 接線前不可決議）")
	}
	taskRunID := b["task_run"].Ref[len("taskrun:"):]
	if err := p.deps.VerifyTaskRun(taskRunID, b["task_run"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(1) taskrun: %w", err)
	}
	if err := p.deps.VerifyForge(b["promotion_head"].Digest, b["main_base"].Digest, b["required_check_manifest"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(2-4) forge: %w", err)
	}
	if err := p.deps.VerifyProvenance(taskRunID, b["review_evidence_provenance"].Digest, b["promotion_head"].Digest); err != nil {
		return nil, fmt.Errorf("gate3 重驗(5) provenance: %w", err)
	}
	return nil, nil
}

func (p *Gate3Policy) SupersessionKey(gateName, subject string) string {
	return gateName + "\x00" + subject
}

// ReconcileBindings——**契約凍結（rev7，plan gate 第六輪 P1）**：
//
// production PrepareDecision 會先以 pending request 建 pseudo-record 呼叫
// 本方法做 staleness 前置檢查；一旦回傳 cause，會被轉成**未包裝**一般
// 錯誤並直接返回，BuildDecision 不執行（service.go:107）。若 pending 的
// TaskRun STALE 判定接在這裡，gateDecide 的 errors.Is(ErrGate3Mismatch)
// 分支永遠不會觸發——pending 永久滯留，違反 B5 §4.3。因此凍結：
//   - pending pseudo-record（rec.Decision == ""）**一律回空**——pending
//     Gate 3 的失效只走決議時重驗（BuildDecision → mismatch sentinel →
//     gateDecide ExpirePending；owner 裁決 6c 決議時重驗、不輪詢）。
//   - 已核可 record（rec.Decision == "approved"）才回 stale cause——C1a
//     接 TaskRun reader 後補「TaskRun STALE → gate3 record stale」；骨架期回空。
func (p *Gate3Policy) ReconcileBindings(rec gate.ApprovalRecord) ([]gate.StaleCause, error) {
	if rec.Decision == "" {
		return nil, nil // pending：決議時重驗承載，不得在此回 cause
	}
	return nil, nil // approved record：C1a 接線；骨架期回空
}
```

- [ ] **Step 4: 在 `ensureGate()` registry（app.go:3888-3894）追加一行**

```go
	"gate3_promotion": gatepolicy.NewGate3Policy(gatepolicy.Gate3Deps{}),
```

（deps 空＝fail closed，C1 接線時替換；本行使 registry 完整、Reconcile 對未來 gate3 record 不落 unknown-gate 分支。）

- [ ] **Step 5: 跑 Step 2 指令預期 PASS；`go test -race -count=1 .` root 包確認 ensureGate 無回歸**
- [ ] **Step 6: Commit**

```bash
git add internal/gatepolicy/gate3.go internal/gatepolicy/gate3_test.go app.go
git commit -m "feat(gatepolicy): B6 Task 6——gate3_promotion policy 骨架與註冊（B5 §5.2／§5.3，deps fail closed 待 C1）"
```

---

### Task 6b: Gate 3 pending 終態 seam（rev2 補——B5 §4.3 expired transition）

**Files:**
- Modify: `internal/gate/types.go`（State 常數＋`GateEntry.TerminalCause`）、`internal/gate/service.go`（`ExpirePending`＋`PrepareDecision`／`CommitDecision` 的 State 判定）、`internal/gate/project.go`（expired 投影＋terminal cause）
- Modify: `app.go`——`gateDecide`（app.go:5805-5855）錯誤分支；`GateEntryDTO`＋`gateEntriesToDTO`（Task 3 產物）補 `TerminalCause` 欄位映射
- Test: `internal/gate/service_test.go`、`app_gate_test.go`（追加）

**Interfaces:**
- Consumes: Task 6 的 `gatepolicy.ErrGate3Mismatch`；既有 `Transition`／`findEntry`／`appendOp`。
- Produces:
  - `gate.Expired State = "expired"`（終態；僅 pending 可轉入）。
  - **三入口同步封閉（rev3——只改 ExpirePending 不夠：`e.Record != nil || e.Request == nil` 的形狀檢查對 expired entry 仍判 pending，expired 後仍可 Prepare／Commit、重複 expire 仍 append）**：`ExpirePending`、`PrepareDecision`、`CommitDecision` 三者的 pending 判定一律改為**明確要求 `e.State == Pending`**（原形狀檢查保留為輔）。
  - `func (s *Service) ExpirePending(approvalID, cause string) error`——`State == Pending` 才 append `Transition{To: "expired", Cause}`；否則回 `ErrNotPending`（含已 expired——重複 expire 不新增 record）。
  - **Terminal cause 投影（rev3 P2；rev5 對齊 production precedence）**：`GateEntry` 增 `TerminalCause string`——projection 於 transition **實際改變 state** 時記下其 `Cause`；state precedence 沿 production 現行規則不得改壞（project.go:78、project_test.go:109）：Stale→Superseded 接受、Superseded→Stale 忽略、Rejected／Expired 之後全忽略。`GateEntryDTO` 同步增欄位。C1c 的失效原因呈現自此有 durable 出口。
  - `gateDecide` 錯誤分支：`errors.Is(err, gatepolicy.ErrGate3Mismatch)` → 同一 workflowMu 臨界區內 `svc.ExpirePending(approvalID, err.Error())`，回傳具名錯誤；transient error 原樣回傳、request 維持 pending。

- [ ] **Step 1: 寫 failing tests（gate 層）**

```go
func TestExpirePending(t *testing.T) {
	s, _ := newTestService(t)
	id, err := s.Submit("gate1", "spec", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExpirePending(id, "reverify mismatch"); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.ListDetectOnly()
	if st := stateOf(entries, id); st != Expired {
		t.Fatalf("pending 應轉 expired, got %s", st)
	}
	// 終態封閉（rev3）：expired 後 Prepare 與 Commit 均回 ErrNotPending
	if _, err := s.PrepareDecision(id, "approved", "", Approver{ID: "o", Method: "ui"}, DecisionInput{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expired 後 Prepare 應回 ErrNotPending, got %v", err)
	}
	// 重複 expire：回 ErrNotPending 且不新增 journal record
	opsBefore := len(s.opsForTest())
	if err := s.ExpirePending(id, "again"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("重複 expire 應回 ErrNotPending, got %v", err)
	}
	if len(s.opsForTest()) != opsBefore {
		t.Fatal("重複 expire 不得 append")
	}
	// terminal cause 投影（rev3 P2）：重新投影後 cause 仍在
	entries2, _ := s.ListDetectOnly()
	if c := terminalCauseOf(entries2, id); c != "reverify mismatch" {
		t.Fatalf("TerminalCause 應為 expire cause, got %q", c)
	}
	// 非 pending（已決議者）不可 expire
	id2 := submitAndApprove(t, s)
	if err := s.ExpirePending(id2, "x"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("active record 應回 ErrNotPending, got %v", err)
	}
}

func TestCommitDecisionFailsAfterExpire(t *testing.T) {
	// prepared decision 在 Commit 前被 expire → Commit 必須失敗（rev3）
	s, _ := newTestService(t)
	id, err := s.Submit("gate1", "spec", gate1Bindings())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareDecision(id, "approved", "", Approver{ID: "o", Method: "ui"}, DecisionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExpirePending(id, "raced"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDecision(prepared); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expire 後 Commit 應回 ErrNotPending, got %v", err)
	}
}

func TestTerminalCauseProjection(t *testing.T) {
	// rev5（P2）：precedence 沿 production（project.go:78）——Stale→Superseded
	// 接受、Superseded→Stale 忽略、Expired 後全忽略；cause 只隨實際 state
	// 變化更新。stale／superseded 的 cause 斷言取自 journal 內對應 transition
	// record（helper journalTransitionCause：掃 opsForTest 解碼 transition、
	// 取該 id 指定 To 的最新 Cause——自我一致，不猜 production 字串）。
	newSvc := func(t *testing.T) *Service {
		s, _ := newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
		return s
	}
	t.Run("expired 後全忽略且 cause 不覆寫", func(t *testing.T) {
		s := newSvc(t)
		id, err := s.Submit("gate1", "spec", gate1Bindings())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ExpirePending(id, "cause-expired"); err != nil {
			t.Fatal(err)
		}
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "superseded", At: "t2", Cause: "stray-a"})
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "stale", At: "t3", Cause: "stray-b"})
		entries, err := s.ListDetectOnly()
		if err != nil {
			t.Fatal(err)
		}
		if st := stateOf(entries, id); st != Expired {
			t.Fatalf("Expired 後 transition 應全忽略：%s", st)
		}
		if c := terminalCauseOf(entries, id); c != "cause-expired" {
			t.Fatalf("cause 不得被覆寫：%q", c)
		}
	})
	t.Run("rejected 後全忽略且 TerminalCause 維持空", func(t *testing.T) {
		// rev6（P2）：既有測試只驗 Rejected 的 state 不變——補新欄位斷言。
		// Rejected 的拒絕原因承載於 record.Reason，TerminalCause 應維持空。
		s := newSvc(t)
		id, err := s.Submit("gate1", "spec", gate1Bindings())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Decide(id, "rejected", "not good", Approver{ID: "o", Method: "ui"}, DecisionInput{}); err != nil {
			t.Fatal(err)
		}
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "stale", At: "t2", Cause: "stray-a"})
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "superseded", At: "t3", Cause: "stray-b"})
		entries, err := s.ListDetectOnly()
		if err != nil {
			t.Fatal(err)
		}
		if stateOf(entries, id) != Rejected {
			t.Fatal("Rejected 後 transition 應全忽略（既有 precedence）")
		}
		if c := terminalCauseOf(entries, id); c != "" {
			t.Fatalf("Rejected 的 TerminalCause 應維持空：%q", c)
		}
	})
	t.Run("stale→superseded 接受、superseded→stale 忽略", func(t *testing.T) {
		s := newSvc(t)
		id := submitAndApprove(t, s)
		setCurrent(s, func() (string, error) { return "sha256:" + hex64b(), nil })
		if _, err := s.List(); err != nil { // durable reconcile 落 stale transition
			t.Fatal(err)
		}
		entries, _ := s.ListDetectOnly()
		if stateOf(entries, id) != Stale {
			t.Fatal("前置：應 stale")
		}
		staleCause := terminalCauseOf(entries, id)
		if staleCause == "" || staleCause != journalTransitionCause(t, s, id, "stale") {
			t.Fatalf("TerminalCause 應等於 journal stale transition 的 Cause：%q", staleCause)
		}
		// Stale→Superseded：production 允許——state 與 cause 都更新
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "superseded", At: "t2", Cause: "new approved gate1 next"})
		entries2, _ := s.ListDetectOnly()
		if stateOf(entries2, id) != Superseded {
			t.Fatal("Stale→Superseded 應接受（project.go:78 precedence）")
		}
		if c := terminalCauseOf(entries2, id); c != "new approved gate1 next" {
			t.Fatalf("cause 應隨實際 state 變化更新：%q", c)
		}
		// Superseded→Stale：忽略——state 與 cause 皆不變（project_test.go:109 固定）
		_ = s.appendOp(Transition{Type: "transition", ApprovalID: id, To: "stale", At: "t3", Cause: "late-stale"})
		entries3, _ := s.ListDetectOnly()
		if stateOf(entries3, id) != Superseded {
			t.Fatal("Superseded→Stale 應忽略")
		}
		if c := terminalCauseOf(entries3, id); c != "new approved gate1 next" {
			t.Fatalf("被忽略的 transition 不得覆寫 cause：%q", c)
		}
	})
	t.Run("superseded（核可 supersede 路徑）cause 等於 journal record", func(t *testing.T) {
		s := newSvc(t)
		id := submitAndApprove(t, s)
		_ = submitAndApprove(t, s) // 同 subject 第二筆 approved → 前筆 superseded
		entries, _ := s.ListDetectOnly()
		if stateOf(entries, id) != Superseded {
			t.Fatal("應 superseded")
		}
		want := journalTransitionCause(t, s, id, "superseded")
		if c := terminalCauseOf(entries, id); c == "" || c != want {
			t.Fatalf("TerminalCause 應等於 journal transition Cause：%q vs %q", c, want)
		}
	})
}
```

- [ ] **Step 2: 跑 `go test -race ./internal/gate/ -run 'ExpirePending|CommitDecisionFailsAfterExpire|TerminalCauseProjection|ListDetectOnlyMatchesDurable' -count=1`，預期 FAIL（rev6——focused 指令涵蓋本 task 全部新測試，不只 ExpirePending）**
- [ ] **Step 3: 實作 gate 層**

types.go 加 `Expired State = "expired"`；`GateEntry` 增 `TerminalCause string`。project.go：(a) transition 套用邏輯補 expired 分支（pending entry 收到 `To: "expired"` transition → State=Expired；Expired 之後的 transition 全部忽略——與既有 Rejected 同級）；(b) **precedence 沿 production 現行規則（project.go:78），rev4「首個終態後全忽略」作廢**（會改壞既有 Stale→Superseded 允許路徑，project_test.go:109 固定 superseded 不得被 stale 降級）：Stale→Superseded 接受（state 更新）；Superseded→Stale 忽略；Rejected／Expired 後全忽略；(c) `TerminalCause` **只在 transition 實際改變 state 時更新**——被忽略的 transition 不得覆寫 state 或 cause。service.go——**三入口同步改 State 判定（rev3）**：

```go
// ExpirePending：pending request 轉 expired 終態（B5 spec §4.3）。
// rev3：pending 判定必須看 State——expired entry 的 Record/Request 形狀
// 與 pending 相同，僅形狀檢查會讓 expired 再被 expire／Prepare／Commit。
// 呼叫端（gateDecide）持 workflowMu——本方法的 append 屬單一寫入者路徑。
func (s *Service) ExpirePending(approvalID, cause string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := Project(s.j.Ops())
	if err != nil {
		return err
	}
	e := findEntry(entries, approvalID)
	if e == nil || e.State != Pending || e.Record != nil || e.Request == nil {
		return ErrNotPending
	}
	return s.appendOp(Transition{Type: "transition", ApprovalID: approvalID,
		To: string(Expired), At: s.now(), Cause: cause})
}
```

`PrepareDecision` 與 `CommitDecision` 的既有 pending 檢查（`e == nil || e.Record != nil || e.Request == nil`，service.go:143-146 同型兩處）同步加上 `|| e.State != Pending`——CommitDecision 每次重新 Project（service.go:139），故「Prepare 後被 expire、Commit 前」的競態由此攔截（TestCommitDecisionFailsAfterExpire 固定此行為）。

**同步修改 `ListDetectOnly`（rev6——「與 durable reconcile 等值」契約涵蓋 TerminalCause）**：Task 2 的實作在本 task 引入欄位後補投影——durable `Reconcile()` 會把 causes 寫成 transition（service.go:237）再由 Project 投影 cause，detect-only 若只設 State 會回傳 `TerminalCause=""`，與 durable 路徑不等值、B5 §4.3 的失效原因也遺失：

```go
		if len(causes) > 0 {
			e.State = Stale
			e.TerminalCause = causes[0].Cause // 多筆 cause 只有第一筆使 Active→Stale（rev5 precedence：cause 隨實際 state 變化）
		}
```

等值測試：

```go
func TestListDetectOnlyMatchesDurableReconcile(t *testing.T) {
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
	id := submitAndApprove(t, s)
	setCurrent(s, func() (string, error) { return "sha256:" + hex64b(), nil })
	before := len(s.opsForTest())
	detect, err := s.ListDetectOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.opsForTest()) != before {
		t.Fatal("detect-only 不得 append")
	}
	durable, err := s.List() // 隨後的 durable reconcile
	if err != nil {
		t.Fatal(err)
	}
	if stateOf(detect, id) != stateOf(durable, id) {
		t.Fatalf("state 不等值：detect=%s durable=%s", stateOf(detect, id), stateOf(durable, id))
	}
	dc, uc := terminalCauseOf(detect, id), terminalCauseOf(durable, id)
	if dc == "" || dc != uc {
		t.Fatalf("TerminalCause 不等值：detect=%q durable=%q", dc, uc)
	}
}
```

**新增測試 helper（rev6 補齊實作——service_test.go）**：

```go
func terminalCauseOf(entries []GateEntry, id string) string {
	for _, e := range entries {
		if e.ApprovalID == id {
			return e.TerminalCause
		}
	}
	return ""
}

// journalTransitionCause：掃 journal ops 解碼 transition record，回傳該
// approvalID 指定 To 的最新 Cause（ops 依序掃，最後符合者即最新）；
// 查無即 Fatal。
func journalTransitionCause(t *testing.T, s *Service, id, to string) string {
	t.Helper()
	cause := ""
	for _, op := range s.opsForTest() {
		for _, raw := range op.Records {
			var tr Transition
			if err := json.Unmarshal(raw, &tr); err != nil || tr.Type != "transition" {
				continue
			}
			if tr.ApprovalID == id && tr.To == to {
				cause = tr.Cause
			}
		}
	}
	if cause == "" {
		t.Fatalf("journal 無 %s→%s transition", id, to)
	}
	return cause
}
```

- [ ] **Step 4: 跑 Step 2 指令預期 PASS**
- [ ] **Step 5: 寫 failing test（app 層 gateDecide 接線）**

```go
func TestGateDecideGate3MismatchExpiresPending(t *testing.T) {
	a := newGateTestApp(t)
	// white-box：以 mismatch deps 覆寫 registry 的 gate3 policy
	a.gateReg["gate3_promotion"] = gatepolicy.NewGate3Policy(gatepolicy.Gate3Deps{
		VerifyTaskRun: func(id, dg string) error {
			return fmt.Errorf("snapshot 不符: %w", gatepolicy.ErrGate3Mismatch)
		},
		VerifyForge:      func(h, b, d string) error { return nil },
		VerifyProvenance: func(id, d, h string) error { return nil },
	})
	id := submitGate3Request(t, a) // 以形狀合法的六 binding 直接經 svc.Submit 造 pending
	// rev3：gateDecide 實際簽章第四參數為 []gate.RiskSelection（gate3 無
	// risk 選擇→nil）；approver 由 git identity 於內部取得，非參數。
	err := a.gateDecide(id, "approved", "", nil)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("mismatch 應轉終態並回具名錯誤：%v", err)
	}
	assertGateState(t, a, id, "expired")
	// transient（未包 sentinel）→ 維持 pending
	a.gateReg["gate3_promotion"] = gatepolicy.NewGate3Policy(gatepolicy.Gate3Deps{
		VerifyTaskRun:    func(id, dg string) error { return errors.New("forge 讀取逾時") },
		VerifyForge:      func(h, b, d string) error { return nil },
		VerifyProvenance: func(id, d, h string) error { return nil },
	})
	id2 := submitGate3Request(t, a)
	if err := a.gateDecide(id2, "approved", "", nil); err == nil {
		t.Fatal("transient 應回錯")
	}
	assertGateState(t, a, id2, "pending")
}
```

（`gateDecide` 簽章以 production 為準——實作 Step 前先讀 app.go:5805 實名與參數序，測試呼叫逐字對齊；上例以第二輪 gate 指認的 `[]gate.RiskSelection` 第四參數書寫。）

**rev7 契約備註**：本測試即「pending Gate 3＋TaskRun STALE → request 轉 expired」的直接回歸——TaskRun STALE 在決議時經 `VerifyTaskRun` 以 mismatch sentinel 呈現（Task 6 的 ReconcileBindings pending 契約保證此路徑不被 PrepareDecision 前置檢查繞過）；C1a 接線時 `VerifyTaskRun` 的實作即「讀 TaskRun journal 判 STALE→回 `ErrGate3Mismatch` 包裝錯誤」，不得改走 ReconcileBindings pending 分支。

```go
```

- [ ] **Step 6: 實作 gateDecide 錯誤分支（PrepareDecision 回錯處，仍在 workflowMu 內）**

```go
	// rev4：逐字對齊 production 現行呼叫（app.go:5828）——DecisionInput 以
	// 字面值建構，無 input 變數。僅插入錯誤分支，呼叫行不改：
	prepared, err := svc.PrepareDecision(approvalID, decision, reason, approver,
		gate.DecisionInput{RiskSelections: riskSelections})
	if err != nil {
		if errors.Is(err, gatepolicy.ErrGate3Mismatch) {
			if xerr := svc.ExpirePending(approvalID, err.Error()); xerr != nil {
				return fmt.Errorf("gate3 重驗不符且轉終態失敗（journal 收斂交 repair）：%w", errors.Join(err, xerr))
			}
			return fmt.Errorf("gate3 重驗不符，request 已轉 expired，需重新送核：%w", err)
		}
		return err
	}
```

- [ ] **Step 7: 跑 `go test -race -run 'Gate3Mismatch' -count=1 .` 預期 PASS；`go test -race -count=1 . ./internal/gate/` 全綠**
- [ ] **Step 8: Commit**

```bash
git add internal/gate/ app.go app_gate_test.go
git commit -m "feat(gate): B6 Task 6b——Expired 終態＋ExpirePending＋gateDecide mismatch 分流（B5 §4.3）"
```

---

### Task 7: wsregistry Entry 綁定欄位＋write-once 方法

**Files:**
- Modify: `internal/wsregistry/store.go`（Entry struct＋新方法）
- Test: `internal/wsregistry/store_test.go`（追加；沿該包既有測試檔）

**Interfaces:**
- Consumes: 既有 `mutate(wsid string, fn func(*Entry)) error`（store.go:359——單交易欄位更新＋tombstone 檢查）。
- Produces:
  - `Entry` 追加欄位：`TaskRunID string \`json:"task_run_id,omitempty"\``、`SnapshotDigest string \`json:"snapshot_digest,omitempty"\``（omitempty——舊檔零遷移）。
  - `func (s *Store) SetTaskRunBinding(wsid, taskRunID, snapshotDigest string) error`——**雙向 1:1**（rev2 修——B5 §3.1 基數是雙向的）：(a) 該 WSID 已綁不同 TaskRun → 拒絕；(b) 該 TaskRunID 已綁在**另一個 WSID**（含 tombstoned entry——已放棄的綁定不可轉移）→ 拒絕；(c) partial pair（TaskRunID 與 SnapshotDigest 只有其一非空）→ corruption，拒絕不覆寫；(d) 同值冪等成功（resume 回填路徑）。跨 WSID 掃描與寫入在**同一把 store 鎖**內完成。C1a 於 `commitSessionIdentity` 掛點呼叫。

- [ ] **Step 1: 寫 failing tests**

```go
func TestSetTaskRunBindingWriteOnce(t *testing.T) {
	s := newTestStore(t) // 沿該包既有 helper；若名稱不同，用現行建構慣例
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("w1")
	if e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || e.SnapshotDigest != "sha256:aa" {
		t.Fatalf("綁定欄位未落：%+v", e)
	}
	// 冪等：同值再寫成功（resume 依 journal 回填）
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatalf("同值應冪等：%v", err)
	}
	// 不同值：拒絕（不允許重綁，B5 §3.1）
	if err := s.SetTaskRunBinding("w1", "01BX5ZZKBKACTAV9WEVGEMMVRZ", "sha256:bb"); err == nil {
		t.Fatal("重綁應拒絕")
	}
}

func TestSetTaskRunBindingTombstoned(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil {
		t.Fatal("tombstoned entry 應拒絕")
	}
}

func TestSetTaskRunBindingCrossWSIDCardinality(t *testing.T) {
	s := newTestStore(t)
	for _, w := range []string{"w1", "w2", "w3"} {
		if err := s.Put(Entry{WSID: w, Provider: "claude", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	// 同一 TaskRunID 綁到另一個 WSID → 拒絕（1:1 雙向）
	if err := s.SetTaskRunBinding("w2", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil {
		t.Fatal("duplicate TaskRunID 跨 WSID 應拒絕")
	}
	// tombstoned 佔用也算佔用：w1 移除後 TaskRun 仍不可轉移到 w3（abandoned 不可再綁定）
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w3", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil {
		t.Fatal("tombstoned 佔用的 TaskRunID 不可轉移（B5 §3.6 abandoned 不可再綁定）")
	}
}

func TestSetTaskRunBindingPartialPairCorruption(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t",
		TaskRunID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}); err != nil { // SnapshotDigest 缺——partial pair
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil {
		t.Fatal("partial pair 應視為 corruption 拒絕，不得靜默補全")
	}
}
```

（持久化 round-trip 由 `mutate`→`persistLocked` 既有四步契約承載；該包既有測試已覆蓋 persist，僅需確認新欄位經 JSON round-trip 不丟——在第一個測試補 re-open 斷言：以同 path 重開 Store 後 `Get("w1")` 欄位仍在。）

- [ ] **Step 2: 跑 `go test -race ./internal/wsregistry/ -run TaskRunBinding -count=1`，預期 FAIL**
- [ ] **Step 3: 實作（Entry 加兩欄位＋方法）**

```go
// SetTaskRunBinding 寫入 implementation session 的 TaskRun 綁定（B5 spec
// §3.1 雙向 1:1 基數）。單一 s.mu 臨界區內完成全部檢查與寫入：
//   (a) 目標 entry 不存在／tombstoned → 拒絕（沿 mutate 語意）；
//   (b) 目標 entry 已綁不同值 → 拒絕（不允許重綁）；同值冪等成功；
//   (c) partial pair（TaskRunID 與 SnapshotDigest 僅其一非空）→ corruption，拒絕；
//   (d) taskRunID 已出現在任何其他 entry（含 tombstoned——abandoned 綁定
//       不可轉移，B5 §3.6）→ 拒絕。
// 權威階層（B5 §3.2(2)）：本欄位是 TaskRun journal 的 derived cache——
// 衝突一律以 journal 為準修復，不得反向補寫 journal。
//
// 實作骨架：不能用 mutate（其 fn 看不到其他 entries）——沿 mutate 的
// lock→檢查→改欄位→persistLocked 骨架（store.go:359-373）展開，於同一
// s.mu 臨界區先跨全 entries 掃描（fileFormat 的 entries 集合——實作時
// 對齊實際欄位名），再寫目標 entry 並 persistLocked。
func (s *Store) SetTaskRunBinding(wsid, taskRunID, snapshotDigest string) error {
	if taskRunID == "" || snapshotDigest == "" {
		return fmt.Errorf("wsregistry: task_run_id 與 snapshot_digest 不得為空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// rev3：durable 契約沿 mutate（store.go:359-373）逐字展開——所有欄位
	// mutation 必須走 persistOrRollback（store.go:262-284）：rename 前失敗
	// → 記憶體回滾 old；directory sync 後不確定 → ErrRegistryUncertain
	// latch（不回滾）。entry 取得與集合迭代對齊 s.file 的實際欄位名。
	old, ok := s.file.Entries[wsid] // 欄位存取形狀依 mutate 現行實作對齊
	if !ok {
		return fmt.Errorf("%w: %q", ErrEntryNotFound, wsid)
	}
	if old.RemovedAt != "" {
		return fmt.Errorf("%w: %q", ErrTombstoned, wsid)
	}
	if (old.TaskRunID == "") != (old.SnapshotDigest == "") {
		return fmt.Errorf("wsregistry: %q 綁定欄位 partial pair（corruption），拒絕寫入", wsid)
	}
	if old.TaskRunID != "" {
		if old.TaskRunID == taskRunID && old.SnapshotDigest == snapshotDigest {
			return nil // 冪等（resume 回填）——零變更不觸發 persist
		}
		return fmt.Errorf("wsregistry: %q 已綁定 taskrun %s，拒絕重綁", wsid, old.TaskRunID)
	}
	for w, e := range s.file.Entries {
		if w != wsid && e.TaskRunID == taskRunID {
			return fmt.Errorf("wsregistry: taskrun %s 已綁定於 %q（1:1 基數），拒絕", taskRunID, w)
		}
	}
	next := old
	next.TaskRunID = taskRunID
	next.SnapshotDigest = snapshotDigest
	s.file.Entries[wsid] = next
	// rev4：persistOrRollback 實際簽章接 rollback closure（store.go:274）——
	// rename 前失敗時由 closure 還原記憶體；dir-sync 不確定進 latch 不回滾。
	return s.persistOrRollback(func() {
		s.file.Entries[wsid] = old
	})
}
```

註：`s.file.Entries` 與 `persistOrRollback` 簽章以 store.go 現行為準逐字對齊（mutate 為唯一先例，**不新增抽象**）；分支語意（a)-(d) 與錯誤訊息為凍結內容。

補 **failure matrix 測試**（rev3——成功 round-trip 不足以驗證 durable seam）：沿該包既有 persist 失敗測試的 `hook func(fsyncStep) error` 注入慣例（store.go:102-127），對 `SetTaskRunBinding` 加兩列：

```go
func TestSetTaskRunBindingPersistFailureMatrix(t *testing.T) {
	// (1) rename 前失敗（temp write／file sync 注入錯誤）→ 記憶體回滾：
	//     Get 回傳 old（無綁定欄位）、方法回錯、後續操作不受影響
	// (2) directory sync 注入錯誤 → ErrRegistryUncertain latch：方法回
	//     uncertain、Store.Uncertain() == true、後續 mutation 一律被 latch 擋
	// 兩列皆沿既有 persist failure 測試的 hook 注入寫法（同檔既有案例為模板），
	// 斷言形狀對齊 mutate 的既有 failure 測試。
}
```

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；`go test -race ./internal/wsregistry/ -count=1` 全包**
- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/
git commit -m "feat(wsregistry): B6 Task 7——Entry TaskRun 綁定欄位＋SetTaskRunBinding write-once（B5 §3.1／§3.2(2)）"
```

---

### Task 8: `appcore.Manager` turn-admission freeze 旗標

**Files:**
- Modify: `internal/appcore/manager.go`（slot 加欄位＋`FreezeTurns`＋`BeginSubmit` 檢查）
- Test: `internal/appcore/manager_test.go`（追加；沿該包既有測試慣例）

**Interfaces:**
- Consumes: 既有 slot 結構與 `BeginSubmit(w WSID) (SubmissionID, error)`（manager.go:645）。
- Produces: `func (m *Manager) FreezeTurns(w WSID, during func()) error`——於 `m.mu` 臨界區內設 slot 的 `turnsFrozen = true`（monotonic，無解除方法）並呼叫 `during()`（仍持 `m.mu`——B5 §4.2(2) 雙鎖同持的 manager 側；`during` 內由 App 取 `apprMu` 設 approval 旗標）。**兩種 turn admission 都檢查**（rev2 修——只擋 `BeginSubmit` 可被 NewSession→StartSession 繞過）：`BeginSubmit` 與 `BeginNewSessionSubmit` 對 frozen slot 皆回具名 error。Task 9 與 C1a 使用。

- [ ] **Step 1: 寫 failing tests**

```go
func TestFreezeTurnsBlocksBeginSubmit(t *testing.T) {
	m := newTestManager(t) // 沿該包既有建構慣例（appcore.New + 最小 Config）
	w := reserveActive(t, m) // 既有測試應有 reserve→active 流程 helper；無則內聯
	ran := false
	if err := m.FreezeTurns(w, func() { ran = true }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("during 回呼必須在凍結時執行（雙鎖同持窗口）")
	}
	if _, err := m.BeginSubmit(w); err == nil {
		t.Fatal("frozen slot 的 BeginSubmit 應拒絕")
	}
}

func TestFreezeTurnsBlocksBeginNewSessionSubmit(t *testing.T) {
	// rev2（plan gate P1）：frozen WSID 走 NewSession→StartSession 的初始
	// prompt 也是新 turn——BeginNewSessionSubmit 必須同樣被擋。
	m := newTestManager(t)
	w := reserveIdle(t, m) // idle slot（可走 BeginNewSessionSubmit 的 phase）
	if err := m.FreezeTurns(w, func() {}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit(w, "task-1"); err == nil {
		t.Fatal("frozen slot 的 BeginNewSessionSubmit 應拒絕（StartSession 繞道封死）")
	}
}

func TestFreezeTurnsMonotonicAndUnknownWSID(t *testing.T) {
	m := newTestManager(t)
	if err := m.FreezeTurns(WSID("nope"), func() {}); err == nil {
		t.Fatal("未知 WSID 應回 error")
	}
	w := reserveActive(t, m)
	if err := m.FreezeTurns(w, func() {}); err != nil {
		t.Fatal(err)
	}
	// 重複凍結冪等成功（monotonic set-once）
	if err := m.FreezeTurns(w, func() {}); err != nil {
		t.Fatalf("重複凍結應冪等：%v", err)
	}
}
```

- [ ] **Step 2: 跑 `go test -race ./internal/appcore/ -run FreezeTurns -count=1`，預期 FAIL**
- [ ] **Step 3: 實作**

slot struct 加 `turnsFrozen bool`（zero value＝未凍結，restart 後由 C1a startup repair 依 journal 重建——B5 §4.2(2)）：

```go
// FreezeTurns 設定該 WSID 的 turn-admission 凍結旗標（monotonic——B5 spec
// §4.2(2)：set-once、生命週期內不清除；重啟後由 startup repair 自 TaskRun
// journal 重建）。during 在仍持 m.mu 時執行——freeze 設定端的雙鎖同持
// 窗口（manager 鎖→apprMu 固定順序）由呼叫端在 during 內取 apprMu 完成；
// 除此路徑外任何程式不得同時持有兩鎖。
func (m *Manager) FreezeTurns(w WSID, during func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, ok := m.slots[w]
	if !ok {
		return fmt.Errorf("appcore: FreezeTurns 未知 WSID %q", w)
	}
	sl.turnsFrozen = true
	if during != nil {
		during()
	}
	return nil
}
```

`BeginSubmit`（manager.go:645）與 `BeginNewSessionSubmit`（新 session 初始 prompt 的 admission——rev2 補）於各自既有 phase 檢查後追加相同檢查：

```go
	if sl.turnsFrozen {
		// rev3：SubmissionID 為 struct 非 string——零值用 SubmissionID{}
		return SubmissionID{}, fmt.Errorf("appcore: session %q 已凍結（TaskRun STALE fail closed），拒絕新 turn", w)
	}
```

- [ ] **Step 4: 跑 Step 2 指令預期 PASS；`go test -race ./internal/appcore/ -count=1` 全包**
- [ ] **Step 5: Commit**

```bash
git add internal/appcore/
git commit -m "feat(appcore): B6 Task 8——FreezeTurns monotonic 旗標＋BeginSubmit 拒絕（B5 §4.2(2)）"
```

---

### Task 9: App approval freeze 旗標＋雙鎖設定端＋resolveApproval 檢查

**Files:**
- Modify: `app.go`——`apprMu` 欄位群（app.go:401-406 附近加 `apprFrozen`）、`resolveApproval`（app.go:6783-6810）、新增 `freezeImplementationSession`
- Test: `app_freeze_test.go`（新檔，沿 app_*_test.go topic 檔慣例）

**Interfaces:**
- Consumes: Task 8 的 `manager.FreezeTurns(w, during)`；既有 `apprPending`／`apprOrder`（app.go:401-406）、`pendingApproval.resolve(allow bool, reason string)`（denyApprovalsForRemove 呼叫形狀，app.go:7076-7080）。
- Produces: `func (a *App) freezeImplementationSession(w appcore.WSID, reason string) error`——**caller 必須持 workflowMu**（單一寫入者）；內部 `manager.FreezeTurns(w, during)`，`during` 取 `apprMu` 設 `apprFrozen[w]`＋原子 drain 該 WSID pending；兩鎖釋放後鎖外逐筆 `resolve(false, reason)`（best-effort，合併錯誤回傳、不回滾旗標）。`resolveApproval` 對 `allow=true` 且 frozen 拒絕；`allow=false` 一律放行。C1a 的 STALE 觸發序列呼叫本方法。

- [ ] **Step 1: 前置確認（读码，不改碼）**：`pendingApproval` struct 是否已有 WSID 歸屬欄位（`denyApprovalsForRemove` 能按 WSID 逐筆 deny，推定有——找到該欄位名）；`resolve` 的簽名與鎖外呼叫慣例（app.go:7076-7080 現行形狀）。以下步驟以欄位名 `wsid` 書寫，實作時對齊實名。
- [ ] **Step 2: 寫 failing tests**

```go
// app_freeze_test.go——B6 Task 9：freeze latch 的 approval 側（B5 §4.2(2)(3)）。
func TestFreezeImplementationSessionDrainsAndBlocksAllow(t *testing.T) {
	a := newApprovalTestApp(t) // 沿 app_test.go 既有 approval 測試建構 helper
	w := startSessionWithPendingApproval(t, a, "appr-1") // 造出一筆 pending
	a.workflowMu.Lock()
	err := a.freezeImplementationSession(w, "taskrun stale")
	a.workflowMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// drained：pending 已不在，且已被 resolve(false)
	if _, ok := a.lookupPendingForTest("appr-1"); ok {
		t.Fatal("pending 應已 drain")
	}
	// rev2：以真實登記的 late pending 測 frozen（不存在的 ID 走 not-found
	// 路徑，測不到旗標）——模擬 latch 設定前已 admit 的 turn 之後續 approval
	registerLatePending(t, a, w, "appr-2-late")
	if err := a.resolveApprovalForTest("appr-2-late", true); err == nil {
		t.Fatal("frozen 後 allow 應拒絕")
	}
	if err := a.resolveApprovalForTest("appr-2-late", false); err != nil {
		t.Fatalf("同一筆 pending 的 deny 仍合法：%v", err)
	}
	// 不存在的 ID：維持既有 not-found 行為（不因 frozen 改變錯誤形狀）
	if err := a.resolveApprovalForTest("no-such-id", true); err == nil {
		t.Fatal("未知 approval 應回 not-found 錯誤")
	}
}

func TestResolveApprovalDenyAlwaysLegalWhenFrozen(t *testing.T) {
	a := newApprovalTestApp(t)
	w := startSessionWithPendingApproval(t, a, "appr-1")
	// 凍結但故意留一筆晚註冊的 pending（模擬 latch 設定前已 admit 的 turn 之後續 approval）
	a.workflowMu.Lock()
	_ = a.freezeImplementationSession(w, "stale")
	a.workflowMu.Unlock()
	registerLatePending(t, a, w, "appr-late")
	if err := a.resolveApprovalForTest("appr-late", false); err != nil {
		t.Fatalf("deny 永遠合法（B5 §4.2(3)）：%v", err)
	}
	registerLatePending(t, a, w, "appr-late2") // rev2：真實登記後再測 allow 被擋
	if err := a.resolveApprovalForTest("appr-late2", true); err == nil {
		t.Fatal("allow 應被旗標擋下")
	}
}
```

測試 helper（`newApprovalTestApp`／`startSessionWithPendingApproval`／`lookupPendingForTest`／`resolveApprovalForTest`／`registerLatePending`）沿 app_test.go 既有 approval 流程測試的建構方式抽取；white-box in-package（`package main` 測試檔）可直接操作 `apprPending`。

- [ ] **Step 3: 跑 `go test -race -run 'Freeze|ResolveApprovalDeny' -count=1 .`，預期 FAIL**
- [ ] **Step 4: 實作**

App struct（apprMu 欄位群旁）加：

```go
	// apprFrozen：per-WSID approval 凍結旗標（B5 spec §4.2(2)——freeze latch
	// 的 apprMu 鎖域側；monotonic set-once）。寫入唯一路徑＝
	// freezeImplementationSession（持 workflowMu 的 freeze 序列，經
	// manager.FreezeTurns 的 during 回呼在雙鎖同持下設定）。
	// resolveApproval 對 allow=true 檢查；allow=false 永遠合法。
	// key 用 appcore.WSID（rev2 修——rev1 的 map[string]bool 與
	// appcore.WSID 索引不相容，無法編譯）。
	apprFrozen map[appcore.WSID]bool
```

（`NewApp` 初始化 `apprFrozen: map[appcore.WSID]bool{}`，沿 apprPending 慣例。）

```go
// freezeImplementationSession——B5 spec §4.2(1)(b)(2)：freeze latch 設定端。
// caller 必須持有 workflowMu（單一寫入者）。鎖序固定：manager 鎖 →
// apprMu（同持，FreezeTurns 的 during 窗口內取得）→ 設兩旗標＋原子 drain
// → 逆序釋放 → 鎖外逐筆 resolve(false)（best-effort：失敗合併回傳＋不
// 回滾旗標——旗標與 drain 是不可回滾的記憶體操作，resolve 失敗不解除
// freeze）。除本路徑外，任何程式不得同時持有 manager 鎖與 apprMu。
func (a *App) freezeImplementationSession(w appcore.WSID, reason string) error {
	var drained []*pendingApproval
	err := a.manager.FreezeTurns(w, func() {
		a.apprMu.Lock()
		defer a.apprMu.Unlock()
		a.apprFrozen[w] = true
		for id, p := range a.apprPending {
			if p.wsid != w { // rev3：wsid 已是 appcore.WSID，直接比較，不做 string 轉換
				continue
			}
			drained = append(drained, p)
			delete(a.apprPending, id)
			a.removeApprOrderLocked(id) // 沿 unregisterApproval 的 apprOrder 同步慣例
		}
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range drained {
		if rerr := p.resolve(false, reason); rerr != nil { // 沿 denyApprovalsForRemove 形狀
			errs = append(errs, rerr)
		}
	}
	return errors.Join(errs...) // 合併回報；不回滾旗標
}
```

`resolveApproval`（app.go:6783-6810）在 `apprMu` 臨界區內、**先處理 `!ok` 再檢查旗標**（rev2 修 nil dereference；**rev3 修鎖洩漏**——production 以手動 `Lock`／`Unlock` 管理 `apprMu`，早退分支必須先 `Unlock`，否則之後所有 approval 操作死鎖）：

```go
	a.apprMu.Lock()
	p, ok := a.apprPending[id]
	if !ok {
		a.apprMu.Unlock() // rev3：手動鎖管理——早退必先解鎖
		return errNotFoundExisting // 沿既有 not-found 錯誤路徑（原碼行為不變）
	}
	if allow && a.apprFrozen[p.wsid] { // rev3：p.wsid 已是 appcore.WSID，直接索引
		a.apprMu.Unlock() // 早退解鎖；pending 保留在 map——同筆之後仍可 deny
		return fmt.Errorf("approval %s：session 已凍結（fail closed），僅可 deny", id)
	}
	// ……以下沿既有流程（自 map 移除、unregister、Unlock、鎖外 p.resolve）……
```

（檢查位置＝線性化點：`apprMu` 內、`p.resolve` 呼叫前——B5 §4.2(3)；`allow=false` 不檢查。frozen-allow 分支**不移除 pending**——同筆 deny 之後仍可完成。）

補鎖不洩漏回歸測試：

```go
func TestFrozenAllowLeavesLockAndPendingUsable(t *testing.T) {
	a := newApprovalTestApp(t)
	w := startSessionWithPendingApproval(t, a, "appr-1")
	a.workflowMu.Lock()
	_ = a.freezeImplementationSession(w, "stale")
	a.workflowMu.Unlock()
	registerLatePending(t, a, w, "appr-x")
	if err := a.resolveApprovalForTest("appr-x", true); err == nil {
		t.Fatal("frozen allow 應拒絕")
	}
	// rev3：allow 失敗後——(1) 同筆 deny 仍可完成（pending 未被移除、鎖未洩漏）
	if err := a.resolveApprovalForTest("appr-x", false); err != nil {
		t.Fatalf("同筆 deny 應成功：%v", err)
	}
	// (2) 其他 approval 操作不卡死（apprMu 未洩漏）——not-found 路徑立即返回
	done := make(chan struct{})
	go func() { _ = a.resolveApprovalForTest("no-such", false); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("apprMu 疑似洩漏：後續 approval 操作卡死")
	}
}
```

- [ ] **Step 5: 跑 Step 3 指令預期 PASS；`go test -race -count=1 .` root 包全綠（特別確認既有 approval／remove 測試零回歸——`denyApprovalsForRemove` 不受影響：它走自己的 deny 路徑且 deny 永遠合法）**
- [ ] **Step 6: Commit**

```bash
git add app.go app_freeze_test.go
git commit -m "feat(app): B6 Task 9——approval freeze 旗標＋雙鎖設定端＋resolveApproval fail-closed 檢查（B5 §4.2(2)(3)）"
```

---

### Task 10a: B6a 驗證與驗收（Task 1-6b 完成後執行；B6a 於此**獨立結案**）

**Files:**
- 無新增；驗證性 task。

- [ ] **Step 1: `go vet ./...`，預期零輸出**
- [ ] **Step 2: `go test -race ./... -count=1`，預期全綠（wall-clock 名單 `docs/spikes/m3b-results.md` §7 五條若偶發，依 B1 慣例標注不算回歸、重跑確認）**
- [ ] **Step 3: B6a 驗收清單逐項對照，寫入 commit message／PR 描述**：
  - (a) Forge port＋fake 完整（Task 1）；`internal/gate` 維持零 domain import。
  - (b) gate journal 單一寫入者：讀面七入口 detect-only（Task 2-3）＋寫面唯一 Submit 呼叫點入 workflowMu（Task 3b，TryLock probe 測試為證據）。
  - (c) Gate 3 policy／manifest／pending 終態封閉（Task 4-6b）：binding 形狀＋subject 交叉驗證、manifest 收斂確定性、Expired 三入口封閉、TerminalCause 投影。
  - (d) Wails binding 全部留在 app.go；App 未新增 M4 domain mutable state；TaskRun domain／journal 本體＝C1a。
- [ ] **Step 4: 宣告 **B6a 完成**（獨立結案）。若 B6b 已先完成（本票為後完成者），依兩票驗收清單合併確認**原 B6 aggregate 關閉**；否則 aggregate 留待後完成之票確認**

---

### Task 10b: B6b 驗證與驗收（Task 7-9 完成後執行；B6b 獨立結案）

**Files:**
- 無新增；驗證性 task。

- [ ] **Step 1: `go vet ./...`＋`go test -race ./... -count=1`，預期全綠**
- [ ] **Step 2: `cd frontend && npx vue-tsc --noEmit && npx vitest run`——兩票皆無前端變更，跑一次確認零影響**
- [ ] **Step 3: B6b 驗收清單逐項對照**：
  - (a) wsregistry 綁定欄位雙向 1:1＋persistOrRollback failure matrix（Task 7）。
  - (b) freeze latch：兩種 turn admission＋approval 旗標＋雙鎖設定端＋鎖不洩漏（Task 8-9）。
  - (c) apprFrozen／turnsFrozen 為 runtime latch 非 domain state——此判定請 review 者確認。
- [ ] **Step 4: 宣告 **B6b 完成**（獨立結案）。若 B6a 已先完成（本票為後完成者），依兩票驗收清單合併確認**原 B6 aggregate 關閉**；否則 aggregate 留待後完成之票確認**

## Self-Review 紀錄（rev2 更新）

1. **Spec coverage**：B5 §3.2(0)（Task 2-3 讀面＋Task 3b 寫面）、§4.3 pending 終態（Task 6b）、§5.1(5)（Task 4）、§5.1(6) review 節（Task 5）、§5.2/§5.3 編排＋mismatch 分類（Task 6）、§3.1 持久化與 1:1 基數（Task 7）、§4.2(2)(3) latch 機制（Task 8-9）。§2/§3.2-§3.6 建立交易與 repair/§4.1-4.2 觸發序列/§5.1(6) evidence 節重建/§6 GitHub 實作＝C1a/C1b/C1c。
2. **Placeholder scan**：Task 7 的 `entryLocked`／`entriesLocked`／`persistTargetLocked` 為示意名並附「沿 mutate 逐字展開、不新增抽象」指示；Task 9 的 `pendingApproval.wsid` 欄位名為讀碼確認項（denyApprovalsForRemove 按-WSID-deny 行為佐證存在）；其餘無 TBD 類留白。
3. **Type consistency**：`apprFrozen` 統一 `map[appcore.WSID]bool`（rev2 修正）；`BuildReviewSection` 回傳 `([]ReviewEntry, error)` 於 Produces 與實作一致；`ErrGate3Mismatch` 於 Task 6 定義、Task 6b 消費；`Expired` 於 Task 6b 定義、`ListDetectOnly`（Task 2）投影相容。

## 修訂記錄

- rev10（2026-08-31，**Task 5 尚未實作前的施工事實核對——B5 spec rev9 對應的契約補完＋範圍收斂**，owner 逐條裁決；**production contract 不受影響（Task 5 未實作）、未重開完整 plan gate、估點維持 0.1 pt 不動、未重估**）：
  - ①（沿 Task 4 已確立的「exported verifier 必須履行自己宣稱的契約」原則）`VerifyReviewSection` 原僅掃 state、無法察覺入參集合本身是錯的。補結構性重驗：`reviewer_login` 嚴格遞增、`permission` 已知列舉且 eligible、`state` 白名單、`submitted_at` 合法且已正規化。**owner 同時指出殘留缺口**：即使全部結構檢查通過，仍無法偵測 caller 把某 reviewer 的 CR 整筆刪除——`[]ReviewEntry` 單獨無法證明其完整來自 Forge，這是資訊不足非邏輯漏洞。doc-comment 明文此範圍聲明：完整性由 **C1** 於決議時重新 `GetReviews`、查齊 permissions、`BuildReviewSection`、`VerifyReviewSection`、組合 manifest 並比對 digest 保證（B5 spec §5.3(5)）。
  - ②（澄清既有 §6 語意，非新增契約）`perms map[string]forge.Permission` 的 Go 零值語意讓「權限查無／未查詢」與「已確認 read／none」無法區分，兩者皆變成 `Eligible()==false`，使 CHANGES_REQUESTED 方向 fail-open，與 §6 fail-closed 語意衝突。凍結：`state ∈ {APPROVED,CHANGES_REQUESTED,DISMISSED}` 的 review，其 reviewer permission key 必須存在，缺漏 fail loud；值須為已知列舉（含空字串 fail loud）；`write／maintain／admin` 入候選，明確 `read／none` 安全排除；`COMMENTED／PENDING` 不參與、可不要求 permission；未知 review state 不得靜默跳過。連帶測試修正：刪除原「Bob 無紀錄→None：不入」案例，改為三案例（明確 none、key 缺漏、未知值）。
  - ③（**本次唯一新增的 spec 級契約**，B5 spec 同步升 rev9）`ReviewEntry.SubmittedAt` 原存 forge 原始字串；RFC3339 允許同一時刻多種字面表示（`Z` 與 `+00:00`），未正規化會使決議時重讀重算產生不同 digest、假 mismatch、pending 誤判失效（§4.3）。凍結：寫入時正規化為 `ts.UTC().Format(time.RFC3339Nano)`（非 `time.RFC3339`，避免丟失 fractional seconds）；`BuildReviewSection` 仍以解析後 `time.Time` 比較收斂；`VerifyReviewSection` 新增「輸入字串等於重新格式化 canonical value」檢查。新增測試證明兩種表示產出完全相同 section bytes。
  - ④ Task 5 改稱「review section 收斂」（原稱「review manifest 收斂」）；**不建 manifest 外殼、不算 digest**（歸 C1）；刪除 Interfaces 內「Consumes: Task 4 的 `ManifestDigest`」空宣稱（原範本從未呼叫）；補 `ReviewEntry` 精確 JSON 鍵序 golden test（比照 Task 4 `TestManifestCanonicalJSONKeyOrder` 形狀）與 `reviews` 輸入順序反轉後 section bytes 相同測試；補齊 tie-break（同 submitted_at 不同 review_id）、`PENDING` 狀態、eligible reviewer 的 current-effective 為 `DISMISSED` 且無其他 approval、Verify 四項結構條件各自獨立負向案例。B6／C1 範圍分界表 `§5.1(5)(6)` 一列措辭校正為「manifest 外殼＋digest 歸 C1」。
  - Scope 邊界：本輪為 doc-only 契約落地（Task 5 尚未實作），不影響 Task 1-4 已完成內容；未重開完整 plan gate、未重估。
  - **補回漏載範本（owner 2026-08-31 裁示，doc-only）**：Task 4 Step 1 測試範本先前未收錄 `TestManifestCanonicalJSONKeyOrder`——該 golden test 已於 follow-up 2（`22088cb`）落地並通過驗收，但 follow-up 2 的 owner 指定範圍只涵蓋 Step 3 虛擬碼、Step 1 bijection 案例與 rev9 案例數，故未同步。依 plan 可重放性要求補入，內容與 `internal/gatepolicy/gate3_manifest_test.go` 的實際測試逐字一致；並補上該範本 import 區塊原本缺少的 `"encoding/json"`（照抄會編譯失敗）。**production contract 與程式碼均未變**。
  - **日期更正（owner 2026-08-31 裁示，不另升版）**：spec rev9／plan rev10／backlog rev7 三個條目原誤標 2026-08-28，實際落地日為 2026-08-31，已更正；版本號維持不變。
  - **Task 5 follow-up：`TestBuildReviewSectionOrderIndependent` 補明確升冪斷言（owner 2026-08-31 裁示，不另升版）**：原測試只比對「輸入反轉後 section bytes 相等」，使「移除 `sort.Strings(logins)`」的 mutation 鑑別力是機率性的——`BuildReviewSection` 把結果收進 map 再取 logins，無 sort 時輸出順序由 Go map 疊代隨機化決定、與輸入順序無關，兩次 Build 有相當機率恰好同序而讓 mutation 存活（實測 8 次樣本僅紅 7 次）。owner 否決「增加 reviewer 數量」方案——Go map 疊代分布未承諾均勻，樣本數不能可靠換算成 1/n!，仍是機率性證據。改為保留 bytes 相等斷言（證明 Forge 輸入順序不影響輸出）並另外明確斷言輸出為 `alice, bob`（證明 canonical section 依 `reviewer_login` 字典序升冪）；acceptance mutation 改為「升冪換降冪」（確定性、必紅），「完全移除 sort」降級為補充性、機率性 mutation，不作為 acceptance 依據。production code（`BuildReviewSection`／`VerifyReviewSection`）未變。詳見 follow-up 報告 `.superpowers/sdd/2026-08-28-b6-m4-application-seams/task-5-followup-report.md`。
- rev9（2026-08-28，**implementation 對照 spec 發現的 verifier bijection erratum**——`VerifyRequiredCheckManifest` 未履行 §5.1(5) bijection 保證，**production contract、scope、估點均未變，未重開完整 plan gate**）：
  - 反例：`RequiredChecks=[{ci,42},{lint,42}]`、`Runs=[{ci,42,run1,success},{ci,42,run2,success}]`——`lint` 完全無覆蓋、`ci` 有兩筆重複候選，長度相等（2==2）且全部 success／head match，但明確違反 §5.1(5) 一對一 bijection（無缺漏／無多餘／一 run 至多歸屬一 required）。原版 `VerifyRequiredCheckManifest` 僅比對 `len(RequiredChecks)==len(Runs)` 判定 coverage，對此輸入會誤判通過，與 §5.3(3)「不得以『目前存在的 runs 剛好都綠』替代集合完整性」的措辭直接衝突。
  - 修正範圍：`VerifyRequiredCheckManifest` 改為自行完成六項檢查、**不依賴 `BuildRequiredCheckManifest` 已先執行**——(1) required key 唯一（Verify 自己拒絕重複）；(2) 每筆 run 的 required key 必須存在於 required 集合、且同一 key 恰一筆 run；(3) 每個 required key 最後必須被覆蓋（無缺漏）；(4) 同一 run_id 不得歸屬多個 required key；(5) attribution 重驗（`run_name == context`，`required_app_id == nil` 或 `run_app_id == required_app_id`）；(6) 保留既有 completed/success＋promotion head 驗證。新增 `TestVerifyRequiredCheckManifestBijection` 表格測試，涵蓋一個 owner 反例＋八個獨立負向案例（Verify 端重複 required key、run 多餘、重複覆蓋、缺漏、多個 required key 同時缺漏之排序輸出、run_id 多重歸屬、兩種 attribution 不符形狀）。
  - 非漏洞判定：目前尚非可利用的 production 漏洞——§5.3(3) 決議時重驗是自 forge 重讀重建 manifest（走 `BuildRequiredCheckManifest`，其本身已結構性保證 bijection），再與 binding digest 比對，production 路徑不會把「等長但 key 不對應」的殘缺 manifest 送進 `Verify`。但 `VerifyRequiredCheckManifest` 是 exported 函式、簽章不要求輸入必經 Build（brief 原有的 `TestVerifyRequiredCheckManifest` 本身就是 literal 手刻 manifest 直接呼叫），沒有履行其宣稱的保證；Task 6 對 Verify 的接線方式（是否／如何被 C1b 的 `Gate3Deps.VerifyForge` 呼叫）尚未定案，故依 owner 裁定一次做對，不留 follow-up。
  - Scope 邊界：`BuildRequiredCheckManifest` 的部分排序鍵（僅 `keyOf(context, app_id)`，非全 tuple）維持不變——Build 排序前已拒絕重複 required key、且 bijection 保證陣列內鍵值唯一，不落入 domainspec canonical 先例（`escalationLess` 全 tuple 排序）要防範的「同鍵不同值、sort.Slice 非 stable」風險，故不需要跟進改動；`used[RunID]` 沿用單一 `run_id` 為 key（`forge.CheckRun.RunID` 於 repo 範圍內唯一識別一次 check run，目前為對齊 GitHub check-run id 語意的**假設**、非已驗證的契約——已於 forge.go 補上唯一性契約註解供 C1b adapter 對照；C1b 的 GitHub adapter 實作**尚未端到端驗證**此假設是否成立）；pending／failed 三態語意（`Status != "completed" || Conclusion != "success"`）維持現行做法，符合 §5.3(3)。production contract、scope、估點均未改，未重開完整 plan gate——這是補完 exported verifier 自身應履行的 bijection 保證，不是設計變更。

- rev8（2026-08-28，**implementation follow-up erratum**——Task 3b 測試鑑別力缺口，**production contract、scope、估點均未變，未重開完整 plan gate**）：
  - 背景：Task 3b（commit `4c62bc6`）把 `submitGateRequest` 內唯一的 `svc.Submit` 收進 `workflowMu` 臨界區；review 以 mutation 證實 **production 的鎖位置正確**（Mutation A：移除鎖→測試 FAIL；Mutation C：鎖過寬包住整個 wrapper→`-timeout` 揭露自我 deadlock），但**測試本身鑑別力不足**。
  - 缺口：`TestGateSubmitHoldsWorkflowMu` 原本用單一 write-once-true 布林 `heldDuringSubmit` 記錄「探測期間是否曾經持鎖」。probe 實際觸發兩次——第一次是 `submitGateRequest` 自己的 app 層 pre-validation（應在鎖外）、第二次是 `svc.Submit` 內部再驗同一 policy（應在鎖內）。**Mutation B**（把鎖從 `svc.Submit` 移到 app 層 pre-validation、`svc.Submit` 本身無鎖）實測序列為 `[true false]`：第一次探測就把旗標設為 true，第二次不會清回 false，舊測試因而**誤通過**（PASS，本應 FAIL）——無法區分「只有 pre-validation 持鎖」的錯誤實作與「只有 `svc.Submit` 持鎖」的正確實作。
  - 收斂：改記錄兩次 probe 的完整序列（`heldSequence []bool`），精確斷言呼叫次數恰為兩次、順序恰為 `[false true]`。Task 3b Step 1 虛擬碼同步更新（見上方程式碼區塊與 erratum 說明），避免留下已證實會誤通過的範本。三種情境驗證：正確實作（committed app.go）→ `[false true]` PASS；Mutation A（移除鎖）→ `[false false]` FAIL；Mutation B（鎖移到 pre-validation）→ `[true false]` FAIL。
  - Scope 邊界：`app.go` 未改動（production contract 不變）；Submit 與 escalation 之間的既有窗口維持 deferred（不在本次修正範圍）；未重開完整 plan gate——這是收緊測試以符合 rev7 已核准的不變量，不是設計變更。詳見 follow-up 報告 `.superpowers/sdd/2026-08-28-b6-m4-application-seams/task-3b-followup-report.md`。
- rev7（2026-08-28，plan gate 第六輪 1 P1 收斂）：
  - P1（pending mismatch 繞過 expired）：production PrepareDecision 對 pending pseudo-record 先呼叫 ReconcileBindings、得 cause 即轉未包裝一般錯誤且不執行 BuildDecision（service.go:107）——C1a 若把 TaskRun STALE 接在 ReconcileBindings，sentinel 分支永不觸發、pending 永久滯留（違反 B5 §4.3）。凍結契約（採 gate 建議方案一）：`Gate3Policy.ReconcileBindings` 對 pending pseudo-record（`Decision == ""`）一律回空，pending 失效統一由 BuildDecision 決議時重驗＋mismatch sentinel 承載；已核可 record 才回 stale cause（C1a 接線點）。補 `TestGate3ReconcileBindingsPendingPseudoRecordEmpty` 單元測試＋Task 6b 契約備註（`TestGateDecideGate3MismatchExpiresPending` 即 pending＋TaskRun STALE→expired 的直接回歸；C1a 的 VerifyTaskRun 實作不得改走 ReconcileBindings pending 分支）。
- rev6（2026-08-28，plan gate 第五輪 2 P1＋1 P2 收斂）：
  - P1 detect-only 等值缺口：Task 6b 明列同步修改 `ListDetectOnly`——stale 判定同時投影 `TerminalCause = causes[0].Cause`（durable Reconcile 寫 transition 後 Project 會投影 cause，service.go:237；只設 State 會與 durable 路徑不等值、失去 B5 §4.3 失效原因）；補 `TestListDetectOnlyMatchesDurableReconcile` 等值測試（state＋cause 與隨後 List() 相等、detect-only 前後 journal 不增長）。
  - P1 helper 編譯：`hex64("a")`／`hex64("b")` 全數改既有無參數 `hex64()`／`hex64b()`（project_test.go:67、service_test.go:133）；`validGate1Bindings()` 改既有 `gate1Bindings()`；補齊 `terminalCauseOf`／`journalTransitionCause` 具體實作（先前僅引用未定義）。
  - P2 Rejected 斷言：TerminalCauseProjection 補「rejected 後 stale/superseded 全忽略且 TerminalCause 維持空」subtest（拒絕原因承載於 record.Reason）；Task 6b focused 測試指令擴為涵蓋全部新測試。
- rev5（2026-08-28，plan gate 第四輪 2 P1＋1 P2 收斂）：
  - P1 TerminalCause precedence：rev4「首個終態後全忽略」作廢（改壞 production 允許的 Stale→Superseded，project.go:78）——改沿現行 precedence：Stale→Superseded 接受（state＋cause 更新）、Superseded→Stale 忽略、Rejected／Expired 後全忽略；cause 只隨實際 state 變化更新。表格測試重寫：三案例各自的 follow-up transition 依 precedence 斷言（含 Stale→Superseded 接受案例）；stale／superseded 的 cause 斷言取自 journal transition record（`journalTransitionCause` helper），不留空值。
  - P1 aggregate 結案順序中立：B6a／B6b 各自獨立結案；原 B6 aggregate 僅於兩票皆完成時關閉、由**後完成之票**於收尾 task 確認（Task 10a/10b 對稱條款；backlog B6a/B6b 同步）。
  - P2：backlog 估點標題補 rev6 標注、rev6 修訂記錄移至 rev5 之後（依檔內升冪序）；plan 估點表 Task 3b 改稱 TryLock probe test；TerminalCause 測試移除未使用的 wantCause 欄位。
- rev4（2026-08-28，plan gate 第三輪 4 P1＋1 P2 收斂）：
  - P1 persistOrRollback 簽章：改 rollback closure 形狀 `persistOrRollback(func(){ s.file.Entries[wsid] = old })`（store.go:274）。
  - P1 gateDecide 片段：逐字對齊 app.go:5828——`gate.DecisionInput{RiskSelections: riskSelections}` 字面建構，移除不存在的 `input` 變數。
  - P1 barrier 測試不確定性：sleep barrier 作廢，改**注入 probePolicy 於 ValidateRequest 時 `workflowMu.TryLock()` 探測**——確定性、零 sleep、不新增 App ad-hoc hook。
  - P1 拆票落地：Task 10 拆 10a（B6a 獨立結案）／10b（B6b 結案＋原 B6 整體驗收宣告）；B6a→B6b 改為建議執行順序非技術相依；backlog rev6 同步拆列 B6a/B6b、C1 相依改 B6a＋B6b（owner 已核准 1.45／0.6 pt）。
  - P2 TerminalCause 覆蓋：投影表格測試涵蓋 expired／stale／superseded 三來源＋「終態後被忽略的 transition 不得覆寫 cause」；projection 規則凍結為首個終態 transition 記 cause、其後一律忽略。
- rev3（2026-08-28，plan gate 第二輪 4 P1＋1 P2 收斂）：
  - P1 Expired 未封閉：`ExpirePending`／`PrepareDecision`／`CommitDecision` 三入口 pending 判定同步加 `State == Pending`（形狀檢查對 expired entry 誤判 pending）；補三測試——expire 後 Prepare/Commit 回 ErrNotPending、prepared-then-expired 的 Commit 失敗、重複 expire 不 append。app 測試與插入片段依 production 簽章重寫（第四參數 `[]gate.RiskSelection`、變數名對齊現行本體）。
  - P1 Submit call graph 誤判：production 唯一 `svc.Submit` 呼叫點在 `submitGateRequest` wrapper 內（三 caller 都經它）——rev2「三呼叫點替換」作廢，改只在 wrapper 內收鎖、caller 不動、escalation 契約保留；計數 hook 測試改 **barrier 測試**（預持 workflowMu 斷言無新 op、釋放後完成）。
  - P1 freeze 鎖洩漏／型別：resolveApproval 手動鎖管理——兩個早退分支補 `Unlock`、frozen-allow 不移除 pending（同筆可再 deny）；`p.wsid` 直接比較（已是 appcore.WSID）；Task 8 回傳 `SubmissionID{}`；補「frozen allow 後同筆 deny 可完成＋其他 approval 不卡死」測試。
  - P1 durable 契約：`SetTaskRunBinding` 改保留 old→寫 `s.file.Entries[wsid]`→`persistOrRollback(wsid, old)`（rename 前失敗回滾／dir-sync 不確定進 latch）；補 failure matrix 兩列測試（沿既有 hook 注入慣例）。
  - P2 terminal cause 無出口：`GateEntry.TerminalCause`＋DTO 欄位＋projection 於終態 transition 記錄 cause；測試斷言重新投影後 cause 仍在。
  - 估點重算 **2.05 pt**——逾 2.0 拆票線：提議拆 **B6a**（Task 1-6b，1.45 pt）／**B6b**（Task 7-9，0.6 pt），請 owner 裁決後落 backlog。
- rev2（2026-08-28，plan gate 第一輪 8 P1 收斂）：
  - P1 Gate 3 application service 缺段：新增 `ErrGate3Mismatch` mismatch／transient 錯誤分類（Task 6）＋Task 6b（`gate.Expired` 終態、`ExpirePending`、gateDecide mismatch 分流——B5 §4.3 落地）；TaskRun domain 仍留 C1a。
  - P1 Submit 未持鎖：新增 Task 3b writer seam，gate1／gate2／TCA 三送核路徑入 workflowMu＋hook 測試。
  - P1 unknown gate fail open：ListDetectOnly 改回傳 `ErrUnknownGate`（沿 Reconcile），補「回錯且 journal 不增長」測試（Task 2）。
  - P1 字典序時間比較：checks／reviews 的 current-effective 改 RFC3339 嚴格 parse＋`time.Time` 比較，格式錯誤 fail loud；測試改真實 timestamps＋時區偏移案例（Task 4-5）。
  - P1 subject 未交叉驗證：ValidateRequest 補 `req.Subject == task_run.Ref` 相等檢查＋負向測試（Task 6）。
  - P1 單向 write-once：SetTaskRunBinding 改單一鎖內跨 WSID 掃描的雙向 1:1（duplicate TaskRun／partial pair／tombstoned 佔用不可轉移）＋三組新測試（Task 7）。
  - P1 latch 繞道：`BeginNewSessionSubmit` 同步檢查 `turnsFrozen`＋frozen→StartSession 回歸測試（Task 8）。
  - P1 Task 9 程式碼缺陷：`apprFrozen` 改 `map[appcore.WSID]bool`；resolveApproval 先 `!ok` 再檢旗標；測試改真實登記 late pending。
  - Owner 裁決回填：Task 8-9 留 B6；bottom-up 重估 **1.9 pt**（原 1.4 pt 作廢；未逾 2.0 pt 不拆票）。
- rev1（2026-08-28）：初版——10 tasks＋B6/C1 範圍分界表。
