# M3b 多 session 工作區 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 同 provider 多 session（每 provider 4 slot、共 8）＋雙 pane 並看，且重啟成本與 audit 事件總量脫鉤。

**Architecture:** 擴充既有單一 `appcore.Manager` 為 per-WSID session registry（保留單一 emit mutex 與檔案級 `event_id` 單調不變量）；App 的單例 ownership 以 **additive migration** 逐步搬進 `sessionHosts`；Codex 維持共用單一 `Conn`＋connection-wide wire log＋`threadID/turnID → WSID` dispatcher；新增 per-WSID byte-offset replay index 支撐 lazy 視窗重放。

**Tech Stack:** 既有全套（Go 1.26／Wails v2／Vue 3＋Pinia＋vue-i18n／vitest）。無新依賴。

**Spec:** `docs/superpowers/specs/2026-08-14-m3b-multi-session-design.md`（rev4，closure APPROVED 2026-08-14，`b63f168`）。全文 §號皆指該檔。

**執行約束**：Subagent-Driven、單一 writer、每 task 主代理 review。**Task 0（Codex 並行 live probe）是實作 gate——判定 NO-GO 即停止實作、退回 spec gate 由 owner 重裁；不得自行改成 provider-wide 串行或多 app-server 等替代架構（§7.2）**。

---

## Global Constraints

- **每個 commit 都必須是可執行基線**：`go build ./... && go vet ./... && go test -race ./... -count=1` 在**每個 task 結束時**全綠。改簽名一律用 additive 相容入口（新舊並存）→ 遷呼叫點 → 最後才刪舊入口，**不得留下中間的編譯失敗狀態**。
- **凍結常數不可設定**：`MaxSessionsPerProvider = 4`（§1.1、§3.1）；replay index 的 `MaxCatchUpBytes`／`MaxCatchUpRecords`／`MaxCatchUpAttempts`（Task 19）皆為具名 Go 常數，**不進 config、不讀環境變數**。
- **事件權威唯一**：`events.jsonl` append-only、完整不裁切；registry 與 replay index 都是快取／metadata（§3.2.7、§3.5.10）。
- **event_id 檔案級嚴格遞增**：所有新路徑一律經 `Manager.writeAndEmitLocked` 的單一 mutex（§2）。
- **Fail loud**：slot 超限、未知 WSID、provider 不符、registry persist 失敗、recorder 寫入失敗、migration persist 失敗一律回錯並顯示；**不靜默降級、不自動刪 session**。
- **無閒置回收**：只有使用者明確「關閉／移除」才釋放名額（§1 Slot 語意）。
- **Barrier 測試不得依賴 `time.Sleep`**：一律用 `hook*` 注入點或 channel barrier（沿 `app.go:96-107` 慣例）；併發測試一律 `-race`，關鍵競態測試另跑 `-count=30`。
- **Wails binding 契約**：任何新增／改簽名的 binding 都必須同 task 完成「重生 `frontend/wailsjs` → 更新 `frontend/src/lib/bindings.ts` 逐參數轉發 → 更新 `frontend/src/types.ts` → 補 `bindings.test.ts` 轉發斷言」四件事（`bindings.ts:9-12` 記載 M1.5 P1-1 單參數轉發造成的真 bug，元件 mock 抓不到）。
- **i18n**：新 UI 字串進 `zh-TW`＋`en` 雙 locale、key parity 測試綠、台灣慣用語；契約值（provider 名、WSID、event kind）維持原文。
- **收尾 gate**：`go vet ./...`／`go test -race ./... -count=1`／`npm --prefix frontend run test`／`npm --prefix frontend run build`／`wails build`。

---

## File Structure

**新增 Go 套件：**
- `internal/wsregistry/` — `workspace-sessions.json` 的唯一 ownership：durable metadata 白名單、schema v2、legacy 遷移 marker、tombstone。
- `internal/replayindex/` — per-WSID turn index、checkpoint、degraded latch、損壞分級、runtime 重建收斂。
- `internal/wirelog/` — Codex connection-wide wire log：generation、frame index、`SegmentRef`。
- `cmd/probe-codex-parallel/` — Task 0 的可重現 live probe driver。

**修改 Go：**
- `internal/appcore/manager.go` — per-WSID slot registry、三段建立交易、legacy 相容入口（Task 9 刪除）。
- `internal/appcore/sink.go` — `AuditSink.Write` 回傳 `AppendReceipt`。
- `internal/contract/envelope.go` — 新增 `WorkspaceSessionID`。
- `internal/codex/single.go`／`probe.go`／`session.go` — `codexGenerationOwner` ownership 型別、recorder 交棒。
- `app.go` — `sessionHosts` registry、per-WSID socket／mcp、Codex dispatcher、approval WSID 路由、shutdown 總序、啟動修復序列、rebuild 編排。
- `restore.go` — legacy 遷移來源（唯讀）。

**前端：** 新增 `SessionList.vue`／`PaneView.vue`／`DualPane.vue`＋測試；修改 `stores/session.ts`、`lib/bindings.ts`、`types.ts`、`App.vue`、`SettingsBar.vue`、`StatusBar.vue`、`ApprovalDialog.vue`、雙 locale。

**文件：** `docs/spikes/m3b-codex-parallel.md`（Task 0）、`docs/spikes/m3b-results.md`（Task 31）。

---

## Phase 0 — 實作 gate

### Task 0: Codex 單 app-server 多 thread 並行 live probe（NO-GO gate）

**Files:**
- Create: `cmd/probe-codex-parallel/main.go`
- Create: `docs/spikes/m3b-codex-parallel.md`

**Interfaces:**
- Produces: 可重現的 probe driver＋GO/NO-GO 判定。**GO** → Phase 1 起可執行；**NO-GO** → 停止實作、退回 spec gate（§7.2）。
- **判定範圍（凍結）**：只判 (a) 兩 thread 並行 turn 是否真並行、(b) notification／approval 是否帶足以歸屬 thread 的欄位、(c) 關閉語意是否可控。**`completed-before-response` 不列入 live probe 判定**——它是 host 對惡意／異常順序的容錯，真 server 不一定自然產生；該保證改由 Task 9 的 fake-wire malicious-order 測試鎖住。

- [ ] **Step 1: 驗 bundled binary 與 pin 版本**

```bash
./scripts/check-cli.sh          # 驗 tools/codex-cli 的 pin 版本＋輸出 sha256
CODEX_BIN="$(git rev-parse --show-toplevel)/tools/codex-cli/node_modules/.bin/codex"
test -x "$CODEX_BIN" || { echo "NO-GO: bundled codex binary 不存在或不可執行"; exit 1; }
```

輸出的版本與 sha256 逐字記進 spike 文件。**不得用 grep 猜版本**。

- [ ] **Step 2: 寫 probe driver**

`cmd/probe-codex-parallel/main.go`——用 `internal/codex` 的 production 型別（`Server`／`Conn`／`ThreadRunner`），凍結下列參數：

```go
const (
	probeTimeout   = 90 * time.Second // 整體上限
	turnTimeout    = 60 * time.Second // 單一 turn 上限
	approvalPolicy = "untrusted"      // 凍結：與 production 預設一致
	promptA        = "請只回覆字串 PROBE_A_DONE，不要使用任何工具。"
	promptB        = "請只回覆字串 PROBE_B_DONE，不要使用任何工具。"
)

func main() {
	tmp, err := os.MkdirTemp("", "probe-codex-*")   // 隔離工作目錄
	must(err)
	defer os.RemoveAll(tmp)                          // 清理

	wireLog, err := os.Create(filepath.Join(tmp, "wire.jsonl"))
	must(err)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	srv, err := codex.Start(ctx, codexBin, tmp)      // production 啟動路徑
	must(err)
	must(srv.BeginRecording(func(b []byte) error {   // 全程錄流＝證據
		_, werr := fmt.Fprintf(wireLog, "%s\n", b)
		return werr
	}))
	must(srv.Handshake(ctx, codex.ClientInfo{Name: "probe-codex-parallel"}))

	// 兩 thread 並行送 turn
	thA, thB := mustStartThread(ctx, srv), mustStartThread(ctx, srv)
	var wg sync.WaitGroup
	res := make([]turnResult, 2)
	for i, pair := range []struct{ th, prompt string }{{thA, promptA}, {thB, promptB}} {
		wg.Add(1)
		go func(i int, th, p string) {
			defer wg.Done()
			res[i] = runTurn(ctx, srv, th, p, turnTimeout, approvalPolicy)
		}(i, pair.th, pair.prompt)
	}
	wg.Wait()

	report(res, wireLog.Name())                      // 逐項 (a)(b)(c) 判定
	// 自然收尾：先 Terminate → Wait → StopRecording → Close
	_ = srv.Terminate()
	exit := srv.Wait()
	_ = srv.StopRecording()
	must(wireLog.Close())
	fmt.Printf("exit_code=%d stderr_tail=%q\n", exit.Code, exit.StderrTail)
}
```

**強制收尾分支**：driver 支援 `-force` 旗標——在兩 turn 進行中直接 `Terminate` 不等 turn 完成，用來觀察關閉期間的最後 frame 是否仍被錄到（§3.4.2 的前提）。

- [ ] **Step 3: 執行兩種收尾並蒐證**

```bash
go run ./cmd/probe-codex-parallel            2>&1 | tee /tmp/probe-natural.log
go run ./cmd/probe-codex-parallel -force     2>&1 | tee /tmp/probe-forced.log
```

逐項記錄：
- **(a) 真並行**：兩 turn 的 wire frame 是否時間上交錯（非 A 全部完成才出現 B 的 frame）。若被 server 串行化或第二 thread 被拒 → **NO-GO**。
- **(b) 可歸屬**：每筆 notification／approval 是否帶 `threadID` 或 `turnID`，不靠抵達順序即可歸屬。任一類事件無法歸屬 → **NO-GO**。
- **(c) 關閉語意**：natural 與 forced 兩種收尾下，`Terminate → Wait` 是否 bounded 收斂、wire log 是否錄到最後一筆 frame。不收斂 → **NO-GO**。

- [ ] **Step 4: 寫 spike 記錄**

`docs/spikes/m3b-codex-parallel.md`：CLI 版本＋sha256、完整凍結參數、兩次執行的 wire 節錄（交錯證據）、(a)(b)(c) 逐項判定與理由、GO/NO-GO。**如實記錄失敗**。

- [ ] **Step 5: Commit**

```bash
go vet ./... && go test -race ./... -count=1
git add cmd/probe-codex-parallel docs/spikes/m3b-codex-parallel.md
git commit -m "docs(spike): M3b Codex 多 thread 並行 live probe driver＋GO/NO-GO 判定（§7.2）"
```

---

## Phase 1 — 基礎型別與建立交易（全程 additive）

### Task 1: `contract.Envelope` 新增 `workspace_session_id`

**Files:**
- Modify: `internal/contract/envelope.go:71-96`
- Test: `internal/contract/envelope_test.go`

**Interfaces:**
- Produces: `Envelope.WorkspaceSessionID string \`json:"workspace_session_id,omitempty"\``。

- [ ] **Step 1: 失敗測試**

```go
func TestEnvelopeCarriesWorkspaceSessionID(t *testing.T) {
	b, err := json.Marshal(Envelope{EventID: "e1", Kind: "message", WorkspaceSessionID: "01JWSID"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"workspace_session_id":"01JWSID"`) {
		t.Fatalf("欄位未序列化: %s", b)
	}
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil || back.WorkspaceSessionID != "01JWSID" {
		t.Fatalf("round-trip 失敗: %v %+v", err, back)
	}
	b2, _ := json.Marshal(Envelope{EventID: "e2", Kind: "message"})
	if strings.Contains(string(b2), "workspace_session_id") {
		t.Fatalf("空值不應序列化（舊 journal 相容）: %s", b2)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/contract/ -run TestEnvelopeCarriesWorkspaceSessionID -v` → FAIL

- [ ] **Step 3: 加欄位**

```go
	// M3b 新增（additive；§3.1.5）：host-side 穩定 session identity。
	// Conversation lane 的每個 Envelope 自 BeginSubmit 起必填；workspace lane
	// 的 Gate／SpecAssist／PlanAssist one-shot 維持空值、不計入 slot（§3.1.6）。
	WorkspaceSessionID string `json:"workspace_session_id,omitempty"`
```

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/contract/
git commit -m "feat(contract): Envelope 新增 workspace_session_id（additive，§3.1.5）"
```

### Task 2: `internal/wsregistry` durable metadata store

**Files:**
- Create: `internal/wsregistry/store.go`、`internal/wsregistry/store_test.go`

**Interfaces:**
- Produces:
```go
type Entry struct {
	WSID             string `json:"wsid"`
	Provider         string `json:"provider"`
	ResumeSessionID  string `json:"resume_session_id,omitempty"`
	TaskLabel        string `json:"task_label,omitempty"`
	ViewStartEventID string `json:"view_start_event_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	RemovedAt        string `json:"removed_at,omitempty"`
	RemoveReason     string `json:"remove_reason,omitempty"`
}
type Layout struct {
	Pins    []string `json:"pins"`
	Focused string   `json:"focused"`
}
type Store struct{ /* mu、path、file */ }

func Open(path string) (*Store, error)
func (s *Store) Put(e Entry) error
func (s *Store) Remove(wsid, reason string) error        // 使用者移除：留 tombstone
func (s *Store) DeleteUncommitted(wsid string) error     // 建立失敗回滾：整筆刪除、不留 tombstone
func (s *Store) Get(wsid string) (Entry, bool)
func (s *Store) Live() []Entry
func (s *Store) SetLayout(l Layout) error
func (s *Store) Layout() Layout
func (s *Store) Migrated() bool
func (s *Store) MarkMigrated(entries []Entry) error
func (s *Store) Sync() error
```

- [ ] **Step 1: 失敗測試**

```go
func TestStoreOnlyPersistsDurableWhitelist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workspace-sessions.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, forbidden := range []string{"starting", "active", "busy", "approval_pending", "ending"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("runtime state 不得持久化，出現 %q: %s", forbidden, b)
		}
	}
	if !strings.Contains(string(b), `"schema_version": 2`) {
		t.Fatalf("需帶 schema_version=2: %s", b)
	}
}

func TestRemoveKeepsTombstoneButDeleteUncommittedDoesNot(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "codex", CreatedAt: "t"})
	_ = s.Put(Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"})

	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Get("w1"); !ok || e.RemovedAt == "" || e.RemoveReason != "user_removed" {
		t.Fatalf("使用者移除必須留 tombstone：%+v ok=%v", e, ok)
	}

	// 建立失敗回滾不得留 tombstone——否則失敗的建立會在 registry 永久留痕
	if err := s.DeleteUncommitted("w2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("DeleteUncommitted 必須整筆刪除")
	}
	if len(s.Live()) != 0 {
		t.Fatalf("tombstone 與已刪除都不得出現在 Live()：%+v", s.Live())
	}
}

func TestPersistFailureRollsBackMemory(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Put(Entry{WSID: "w2", Provider: "claude", CreatedAt: "t"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("persist 失敗後記憶體必須回滾")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wsregistry/ -v` → FAIL

- [ ] **Step 3: 實作** — 沿 `restore.go:68-78` 的 temp file + atomic rename + 0600 慣例；persist 失敗回滾記憶體（同 `restore.go:99-103`）。白名單靠型別強制：`Entry` 不含任何 runtime state 欄位。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/
git commit -m "feat(wsregistry): durable metadata store＋tombstone／DeleteUncommitted 分離（§3.2.1／§3.6.1）"
```

### Task 3: Manager per-WSID slot registry（additive，保留舊入口）

**Files:**
- Modify: `internal/appcore/manager.go:95-360`
- Test: `internal/appcore/manager_wsid_test.go`

**Interfaces:**
- Produces（**新入口與舊入口並存**）：
```go
type WSID string
type CreateToken struct{ wsid WSID; seq uint64 }

const MaxSessionsPerProvider = 4 // 凍結常數（§1.1）

var (
	ErrSessionLimit     = errors.New("appcore: session slot limit reached")
	ErrSessionNotFound  = errors.New("appcore: unknown workspace session")
	ErrStaleCreate      = errors.New("appcore: stale create token")
	ErrProviderMismatch = errors.New("appcore: event provider != slot provider")
)

// 新入口（WS 後綴）——Task 9 遷完全部呼叫點後改名為無後綴並刪除舊入口。
func (m *Manager) ReserveSession(p contract.Provider) (WSID, CreateToken, error)
func (m *Manager) CommitCreate(tok CreateToken) error
func (m *Manager) AbortCreate(tok CreateToken) error
func (m *Manager) RestoreDormant(w WSID, p contract.Provider) error
func (m *Manager) SlotCount(p contract.Provider) int
func (m *Manager) ProviderOf(w WSID) (contract.Provider, bool)
func (m *Manager) BeginNewSessionSubmitWS(w WSID, taskID string) (SubmissionID, error)
func (m *Manager) BeginSubmitWS(w WSID) (SubmissionID, error)
func (m *Manager) BeginEndSessionWS(w WSID) (SessionToken, error)
func (m *Manager) EmitWS(w WSID, ev contract.Event) error
// …既有每個 provider-keyed 方法都有對應的 WS 版本

// 相容入口（Task 9 刪除）：舊 provider-keyed 簽名維持不變，內部解析到該
// provider 的 legacy slot（沿用現行隱式建立行為，讓既有測試與 app.go 不受影響）。
func (m *Manager) legacyWSIDLocked(p contract.Provider) WSID
```

- [ ] **Step 1: 失敗測試**

```go
func TestReserveSessionLimitIsAtomic(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	var ok, limited int64
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := m.ReserveSession("claude"); err == nil {
				atomic.AddInt64(&ok, 1)
			} else if errors.Is(err, ErrSessionLimit) {
				atomic.AddInt64(&limited, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 4 || limited != 1 {
		t.Fatalf("要恰 4 個 reservation：ok=%d limited=%d", ok, limited)
	}
	if got := m.SlotCount("claude"); got != 4 {
		t.Fatalf("reservation 當下即應計入名額，got=%d", got)
	}
}

func TestAbortCreateReleasesSlot(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	_, tok, err := m.ReserveSession("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AbortCreate(tok); err != nil {
		t.Fatal(err)
	}
	if got := m.SlotCount("codex"); got != 0 {
		t.Fatalf("Abort 後名額未退回：%d", got)
	}
	if err := m.AbortCreate(tok); !errors.Is(err, ErrStaleCreate) {
		t.Fatalf("重複 Abort 應為 stale：%v", err)
	}
}

func TestUnknownWSIDNeverCreatesSlot(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	if _, err := m.BeginNewSessionSubmitWS("no-such", "t"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("未知 WSID 應 ErrSessionNotFound（廢除隱式建立）：%v", err)
	}
	if got := m.SlotCount("claude") + m.SlotCount("codex"); got != 0 {
		t.Fatalf("查詢未知 WSID 不得吃名額：%d", got)
	}
}

func TestEmitFillsWSIDAndRejectsProviderMismatch(t *testing.T) {
	sink := &memSink{}
	m := New(Config{Sink: sink})
	w, tok, _ := m.ReserveSession("claude")
	if err := m.CommitCreate(tok); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitWS(w, contract.Event{Kind: "message", Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := sink.last().WorkspaceSessionID; got != string(w) {
		t.Fatalf("Emit 必須填 WSID：%q", got)
	}
	if err := m.EmitWS(w, contract.Event{Kind: "message", Provider: "codex"}); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("provider 不符必須 fail loud：%v", err)
	}
}

// 相容入口：舊 provider-keyed 路徑在遷移期間必須完全不變
func TestLegacyProviderEntryStillWorks(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	if _, err := m.BeginNewSessionSubmit("claude", "t"); err != nil {
		t.Fatalf("舊入口在遷移期間必須可用：%v", err)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/appcore/ -run 'TestReserveSession|TestAbortCreate|TestUnknownWSID|TestEmitFills|TestLegacyProvider' -race -v` → FAIL

- [ ] **Step 3: 實作**

`Manager.slots` 改 `map[WSID]*slot`；`slot` 增 `provider contract.Provider`＋`committed bool`；新增 `legacy map[contract.Provider]WSID`＋`reserveSeq uint64`。

```go
func (m *Manager) ReserveSession(p contract.Provider) (WSID, CreateToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", CreateToken{}, ErrClosed
	}
	if m.countLocked(p) >= MaxSessionsPerProvider {
		return "", CreateToken{}, ErrSessionLimit
	}
	w := WSID(newULID())
	sl := newSlot()
	sl.provider = p
	m.slots[w] = sl // reservation 當下即佔名額：第 5 個併發不可穿透
	m.reserveSeq++
	return w, CreateToken{wsid: w, seq: m.reserveSeq}, nil
}

// committedSlotLocked：新入口一律走這裡——只讀不建（§3.1.4）。
func (m *Manager) committedSlotLocked(w WSID) (*slot, error) {
	sl, ok := m.slots[w]
	if !ok || !sl.committed {
		return nil, ErrSessionNotFound
	}
	return sl, nil
}

// legacyWSIDLocked：相容入口專用——沿用現行「讀取時隱式建立」行為，讓舊
// provider-keyed 呼叫點在遷移期間完全不受影響。Task 9 連同全部舊簽名刪除。
func (m *Manager) legacyWSIDLocked(p contract.Provider) WSID {
	if w, ok := m.legacy[p]; ok {
		return w
	}
	w := WSID("legacy-" + string(p))
	sl := newSlot()
	sl.provider, sl.committed = p, true
	m.slots[w], m.legacy[p] = sl, w
	return w
}
```

舊方法（`BeginNewSessionSubmit(p, ...)` 等）改為：取鎖 → `w := m.legacyWSIDLocked(p)` → 呼叫共用的 `...Locked(w, ...)` 內部函式。新 `...WS` 方法走 `committedSlotLocked`。**`countLocked` 不計入 legacy slot**（legacy 是遷移期產物，不佔使用者名額）。

- [ ] **Step 4: 全綠（既有 Manager 測試一字不改也要綠）**

Run: `go vet ./... && go test -race ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appcore/
git commit -m "feat(appcore): per-WSID slot registry＋三段建立交易（additive，舊入口暫留，§3.1）"
```

### Task 4: App 建立交易編排＋CommitCreate 雙失敗降級

**Files:**
- Modify: `app.go:47-107`（App struct 加欄位）、`app.go:230-240`
- Test: `app_wsid_test.go`

**Interfaces:**
- Consumes: Task 2 `wsregistry.Store`、Task 3 `ReserveSession`／`CommitCreate`／`AbortCreate`。
- Produces:
```go
type sessionRegistry interface {
	Put(e wsregistry.Entry) error
	DeleteUncommitted(wsid string) error
	Remove(wsid, reason string) error
	Get(wsid string) (wsregistry.Entry, bool)
	Live() []wsregistry.Entry
	Sync() error
}
func (a *App) CreateSession(provider, taskLabel string) (string, error) // Wails binding（Task 25 接前端）
func (a *App) createDegraded(p contract.Provider) bool
```

- [ ] **Step 1: 失敗測試三條**

```go
func TestCreateSessionRollsBackOnPersistFailure(t *testing.T) {
	a := newTestApp(t)
	a.wsReg = &stubRegistry{putErr: errors.New("disk full")}
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("persist 失敗必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("AbortCreate 應退回名額：%d", got)
	}
}

func TestCommitFailureRollsBackRegistryWithoutTombstone(t *testing.T) {
	a := newTestApp(t)
	a.hookForceCommitCreateError = errors.New("injected commit failure")
	reg := &stubRegistry{}
	a.wsReg = reg
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("注入 commit 失敗必須 fail loud")
	}
	if !reg.deletedUncommitted {
		t.Fatal("回滾必須用 DeleteUncommitted（不得留 tombstone）")
	}
	if reg.removedWithTombstone {
		t.Fatal("建立失敗不得走使用者移除路徑")
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("rollback 成功後應 AbortCreate 退回名額：%d", got)
	}
	if a.createDegraded("claude") {
		t.Fatal("rollback 成功不得進 degraded")
	}
}

// §3.1 雙失敗：不 AbortCreate、名額保留、進 create-degraded、既有 session 不受影響
func TestCommitAndRollbackBothFailEnterDegraded(t *testing.T) {
	a := newTestApp(t)
	existing := mustCreateLegacySession(t, a, "claude") // 先有一個可用 session
	a.hookForceCommitCreateError = errors.New("injected commit failure")
	a.wsReg = &stubRegistry{deleteErr: errors.New("rollback persist failed")}

	before := a.manager.SlotCount("claude")
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != before+1 {
		t.Fatalf("雙失敗必須保留名額（不得 AbortCreate）：%d → %d", before, got)
	}
	if !a.createDegraded("claude") {
		t.Fatal("必須進 create-degraded latch")
	}
	if _, err := a.CreateSession("claude", "t2"); !errors.Is(err, errCreateDegraded) {
		t.Fatalf("degraded 期間必須拒絕新建：%v", err)
	}
	if err := a.SendMessageWS(existing, "still works"); err != nil {
		t.Fatalf("degraded 不得影響既有 session：%v", err)
	}
	if a.createDegraded("codex") {
		t.Fatal("degraded 應 per-provider，不得波及另一 provider")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestCreateSession|TestCommitFailure|TestCommitAndRollback' -race -v` → FAIL

- [ ] **Step 3: 實作**

```go
var errCreateDegraded = errors.New("app: session create degraded（需重啟 app 復原）")

func (a *App) CreateSession(provider, taskLabel string) (string, error) {
	if err := a.beginAppTxn(); err != nil { // shutdown 柵欄（§3.1）
		return "", err
	}
	defer a.endAppTxn()
	p := contract.Provider(provider)
	if a.createDegraded(p) {
		return "", errCreateDegraded
	}
	w, tok, err := a.manager.ReserveSession(p)
	if err != nil {
		return "", err
	}
	if err := a.wsReg.Put(wsregistry.Entry{
		WSID: string(w), Provider: provider, TaskLabel: taskLabel,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return "", errors.Join(err, a.manager.AbortCreate(tok))
	}
	if cerr := a.commitCreate(tok); cerr != nil {
		if rerr := a.wsReg.DeleteUncommitted(string(w)); rerr != nil {
			// 雙失敗：不 Abort、保留名額、進 degraded；等 app restart 由 registry
			// 權威還原成 dormant（§3.2.2）
			a.setCreateDegraded(p)
			return "", errors.Join(cerr, rerr, errCreateDegraded)
		}
		return "", errors.Join(cerr, a.manager.AbortCreate(tok))
	}
	return string(w), nil
}

func (a *App) commitCreate(tok appcore.CreateToken) error {
	if a.hookForceCommitCreateError != nil {
		return a.hookForceCommitCreateError
	}
	return a.manager.CommitCreate(tok)
}
```

`createDegraded`／`setCreateDegraded` 以 `a.mu` 保護的 `map[contract.Provider]bool`；**無 in-process 解除路徑**（僅 app restart）。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_wsid_test.go
git commit -m "feat(app): 建立交易編排＋CommitCreate×rollback 雙失敗降級為 create-degraded（§3.1）"
```

### Task 5: legacy `restore.json` 遷移（一次性、冪等）

**Files:**
- Create: `internal/wsregistry/migrate.go`、`internal/wsregistry/migrate_test.go`

**Interfaces:**
- Produces:
```go
type LegacyEntry struct{ ViewStartEventID, ResumeSessionID, TaskID string }
func Migrate(s *Store, legacy map[string]LegacyEntry, newWSID func() string) ([]Entry, error)
```

- [ ] **Step 1: 失敗測試**

```go
func TestMigrateIsIdempotentAcrossRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	legacy := map[string]LegacyEntry{
		"claude": {ViewStartEventID: "e100", ResumeSessionID: "sess-a", TaskID: "task-a"},
		"codex":  {}, // 空 entry：不建立、不佔名額（§3.2.5）
	}
	n := 0
	gen := func() string { n++; return fmt.Sprintf("w%d", n) }

	s1, _ := Open(p)
	got1, err := Migrate(s1, legacy, gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got1) != 1 || got1[0].Provider != "claude" {
		t.Fatalf("只該遷 claude：%+v", got1)
	}
	if got1[0].ViewStartEventID != "e100" || got1[0].ResumeSessionID != "sess-a" || got1[0].TaskLabel != "task-a" {
		t.Fatalf("view window／resume／task 必須沿用：%+v", got1[0])
	}

	s2, _ := Open(p) // 模擬重啟
	got2, err := Migrate(s2, legacy, gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatalf("已遷移不得再建第二枚 WSID：%+v", got2)
	}
	if e, _ := s2.Get("w1"); e.WSID != "w1" {
		t.Fatalf("重啟後 WSID 必須相同：%+v", e)
	}
	if n != 1 {
		t.Fatalf("WSID 只能產生一次，產生了 %d 次", n)
	}
}

func TestMigratePersistFailureFailsLoud(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "ws.json"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := Migrate(s, map[string]LegacyEntry{"claude": {TaskID: "t"}},
		func() string { return "w1" }); err == nil {
		t.Fatal("migration persist 失敗必須 fail loud（呼叫端據此不啟動 provider）")
	}
	if s.Migrated() {
		t.Fatal("失敗不得標記 migrated")
	}
}

func TestRemovedLegacyIsNotRemigrated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ws.json")
	legacy := map[string]LegacyEntry{"claude": {TaskID: "t"}}
	s, _ := Open(p)
	out, _ := Migrate(s, legacy, func() string { return "w1" })
	_ = s.Remove(out[0].WSID, "user_removed")

	s2, _ := Open(p)
	again, _ := Migrate(s2, legacy, func() string { return "w2" })
	if len(again) != 0 {
		t.Fatalf("legacy 移除後不得再次遷入（§3.6.1）：%+v", again)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wsregistry/ -run 'TestMigrate|TestRemovedLegacy' -v` → FAIL

- [ ] **Step 3: 實作**

```go
func Migrate(s *Store, legacy map[string]LegacyEntry, newWSID func() string) ([]Entry, error) {
	if s.Migrated() {
		return nil, nil // §3.2.6：不得再由舊 entry 建出第二枚 WSID
	}
	var out []Entry
	for _, p := range []string{"claude", "codex"} { // 決定性順序
		le, ok := legacy[p]
		if !ok || (le.ResumeSessionID == "" && le.TaskID == "" && le.ViewStartEventID == "") {
			continue // 空 entry 不建立、不佔名額（§3.2.5）
		}
		out = append(out, Entry{
			WSID: newWSID(), Provider: p,
			ResumeSessionID: le.ResumeSessionID, TaskLabel: le.TaskID,
			ViewStartEventID: le.ViewStartEventID,
			CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		})
	}
	return out, s.MarkMigrated(out) // entries＋marker 原子一次寫
}
```

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/
git commit -m "feat(wsregistry): legacy restore.json 一次性遷移＋migration marker（§3.2.5-6）"
```

### Task 6: 啟動修復序列（冪等）

**Files:**
- Modify: `app.go:259-325`
- Test: `app_startup_repair_test.go`

**Interfaces:**
- Produces:
```go
func (a *App) restoreSessions() ([]wsregistry.Entry, error) // §3.2.4 凍結順序
func (a *App) repairIncompleteTurns(entries []wsregistry.Entry) error
```

- [ ] **Step 1: 失敗測試**

```go
func TestStartupRepairEmitsStreamErrorThenFailed(t *testing.T) {
	dir := t.TempDir()
	writeEvents(t, dir, []contract.Envelope{
		{EventID: "e1", Kind: "message", Role: "user", Provider: "claude", WorkspaceSessionID: "w1"},
		{EventID: "e2", Kind: "delta", Provider: "claude", WorkspaceSessionID: "w1"},
	})
	seedRegistry(t, dir, wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})

	a1 := newTestAppAt(t, dir)
	if _, err := a1.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	ev := readEvents(t, dir)
	// stream_error 先寫，reducer 追發的 state_change=failed 在其後——檢查末二筆
	if n := len(ev); n < 2 ||
		ev[n-2].Kind != "stream_error" || ev[n-2].WorkspaceSessionID != "w1" ||
		ev[n-1].Kind != "state_change" || ev[n-1].State != "failed" ||
		ev[n-1].WorkspaceSessionID != "w1" {
		t.Fatalf("末二筆應為 stream_error → state_change=failed：%+v", tail(ev, 3))
	}
}

func TestStartupRepairIsIdempotent(t *testing.T) {
	dir := seedInterruptedTurn(t)
	a1 := newTestAppAt(t, dir)
	if _, err := a1.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	first := readEvents(t, dir)

	a2 := newTestAppAt(t, dir) // 模擬 crash 後重跑同序列
	if _, err := a2.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if second := readEvents(t, dir); len(second) != len(first) {
		t.Fatalf("重跑必須冪等，事件數 %d → %d", len(first), len(second))
	}
}

func TestRestoreAllDormantNoRuntimeState(t *testing.T) {
	a := newTestAppWithRegistry(t, wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	entries, err := a.restoreSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("應還原 1 個 dormant session：%d", len(entries))
	}
	if a.hostFor("w1") != nil {
		t.Fatal("dormant 還原不得建立 host")
	}
	if len(a.apprPending) != 0 {
		t.Fatal("dormant 還原不得有 pending approval")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("dormant 仍佔名額：%d", got)
	}
}

func TestStartupOrderIsFrozen(t *testing.T) {
	a := newTestAppAt(t, seedInterruptedTurn(t))
	var order []string
	a.hookStartupStep = func(s string) { order = append(order, s) }
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	want := []string{"registry_load", "migrate", "restore_dormant", "index_verify", "detect_incomplete", "emit_stream_error"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("啟動修復順序不符 §3.2.4：\n got=%v\nwant=%v", order, want)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestStartup|TestRestoreAllDormant' -race -v` → FAIL

- [ ] **Step 3: 實作** — 依 §3.2.4 凍結順序；`repairIncompleteTurns` 的冪等判定：該 WSID 末筆已是 app-restart `stream_error` 或其導出的 `state_change=failed` 即跳過。`Manager.RestoreDormant` 在 mutex 內直接建 committed slot（registry 是權威），超限仍回 `ErrSessionLimit` 並 fail loud。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_startup_repair_test.go
git commit -m "feat(app): 啟動修復凍結序列＋stuck busy 冪等解除（§3.2.2-4）"
```

---

## Phase 2 — App ownership additive migration

> **本 phase 的遷移紀律**：Task 7 只**加** `sessionHosts`，舊單例欄位原封不動；Task 8 把 Claude 全部路徑遷到 host 後，**才**刪 Claude 單例欄位；Task 9 把 Codex 遷完後，**才**刪其餘單例欄位與 Task 3 的 Manager 相容入口。每個 task 結束時全套測試都是綠的。

### Task 7: `sessionHosts` registry（additive）

**Files:**
- Create: `session_host.go`、`session_host_test.go`
- Modify: `app.go:47-107`（**只加欄位，不刪**）

**Interfaces:**
- Produces:
```go
type sessionHost struct {
	wsid     appcore.WSID
	provider contract.Provider
	sess       *claude.Session
	sockPath   string
	mcpPath    string
	broker     *approval.Broker
	pumpDone   <-chan struct{}
	teardownFn func() error // sync.OnceValue
	lease      *appcore.RecordingLease
	threadID   string
	track      appcore.TurnTrack
	sessionID  string
}
func (a *App) hostFor(w appcore.WSID) *sessionHost
func (a *App) putHost(h *sessionHost)
func (a *App) dropHost(w appcore.WSID)
func (a *App) snapshotHosts() []*sessionHost
func (a *App) hostsOf(p contract.Provider) []*sessionHost
```

- [ ] **Step 1: 失敗測試**

```go
func TestSessionHostsRegistryBasics(t *testing.T) {
	a := newTestApp(t)
	a.putHost(&sessionHost{wsid: "w1", provider: "claude", sockPath: "/tmp/a.sock"})
	a.putHost(&sessionHost{wsid: "w2", provider: "claude", sockPath: "/tmp/b.sock"})
	a.putHost(&sessionHost{wsid: "w3", provider: "codex"})
	if a.hostFor("w1").sockPath == a.hostFor("w2").sockPath {
		t.Fatal("每個 WSID 必須有獨立 socket 路徑（§3.3）")
	}
	if len(a.snapshotHosts()) != 3 || len(a.hostsOf("claude")) != 2 {
		t.Fatal("snapshot／hostsOf 不正確")
	}
	a.dropHost("w1")
	if a.hostFor("w1") != nil || len(a.snapshotHosts()) != 2 {
		t.Fatal("dropHost 未移除")
	}
}

func TestSnapshotHostsIsRaceFree(t *testing.T) {
	a := newTestApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); a.putHost(&sessionHost{wsid: appcore.WSID(fmt.Sprint(i))}) }(i)
		go func() { defer wg.Done(); _ = a.snapshotHosts() }()
	}
	wg.Wait()
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run TestSessionHosts -race -v` → FAIL

- [ ] **Step 3: 實作** — `App` **新增** `sessionHosts map[appcore.WSID]*sessionHost`（`a.mu` 保護）。既有 `broker`／`claudeSess`／`runner`／`track`／`codexLease` 等欄位**全部保留不動**。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go session_host.go session_host_test.go
git commit -m "feat(app): 新增 sessionHosts registry（additive，單例欄位暫留，§3.3）"
```

### Task 8: Claude 遷入 host＋per-WSID socket／MCP＋刪 Claude 單例

**Files:**
- Modify: `app.go:3998-4285`（`startClaude`）、`app.go:4011`、`app.go:4050`、`app.go:3758-3809`
- Test: `app_claude_multi_test.go`

**Interfaces:**
- Consumes: Task 7 `sessionHost`、Task 3 `EmitWS`。
- Produces:
```go
func (a *App) startClaude(w appcore.WSID, prompt, resume, recordCase string) (func(accepted bool), error)
func (a *App) pumpApprovals(h *sessionHost)
// socket：filepath.Join(a.stateDir, "approval-"+string(w)+".sock")
// mcp   ：filepath.Join(a.stateDir, "mcp-"+string(w)+".json")
```
- **刪除**：`App.broker`／`claudeSess`／`claudeSessionID`／`claudePumpDone`／`claudeLease`／`claudeTeardownFn`。

- [ ] **Step 1: 失敗測試**

```go
func TestTwoClaudeSessionsDoNotShareSocketOrMCP(t *testing.T) {
	a := newTestApp(t)
	w1, w2 := mustCreate(t, a, "claude"), mustCreate(t, a, "claude")
	if _, err := a.startClaude(w1, "p1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.startClaude(w2, "p2", "", ""); err != nil {
		t.Fatal(err)
	}
	h1, h2 := a.hostFor(w1), a.hostFor(w2)
	if h1.sockPath == h2.sockPath || h1.mcpPath == h2.mcpPath {
		t.Fatal("第二個 session 會覆寫第一個（§3.3）")
	}
	if h1.broker == h2.broker || h1.sess == h2.sess {
		t.Fatal("broker／子行程必須 per-WSID")
	}
	for _, p := range []string{h1.sockPath, h2.sockPath, h1.mcpPath, h2.mcpPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("路徑未建立：%s", p)
		}
	}
}

func TestClaudeApprovalCarriesWSID(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	if _, err := a.startClaude(w, "p", "", ""); err != nil {
		t.Fatal(err)
	}
	id := seedApproval(t, a, w)
	if pa := a.pendingByID(id); pa == nil || pa.wsid != w {
		t.Fatalf("approval 必須帶 WSID：%+v", pa)
	}
}

func TestNoClaudeSingletonFieldsRemain(t *testing.T) {
	// 反射守門：Claude 單例欄位必須已刪除，避免遷移半途留下兩套 ownership
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"broker", "claudeSess", "claudeSessionID",
		"claudePumpDone", "claudeLease", "claudeTeardownFn"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Claude 單例欄位 %s 應已刪除（§3.3）", name)
		}
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestTwoClaudeSessions|TestClaudeApprovalCarries|TestNoClaudeSingleton' -race -v` → FAIL

- [ ] **Step 3: 實作** — 路徑組裝帶 WSID；broker／pump／lease／teardown（`sync.OnceValue`）建到 host；`registerApproval` 記錄 WSID；`EndSession`／`NewSession`／`SendMessage` 的 Claude 分支改走 `hostFor(w)`＋`EmitWS`。刪除六個單例欄位並修正全部引用。

**檔案清理不在本 task**——per-WSID socket／mcp 的移除清理屬 remove 生命週期，測試放 Task 22。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_claude_multi_test.go
git commit -m "feat(app): Claude 遷入 sessionHost＋per-WSID socket／MCP，刪除 Claude 單例（§3.3）"
```

### Task 9: Codex dispatcher＋`startCodexHost(wsid, …)`＋刪除相容 shim

**Files:**
- Modify: `app.go:4240-4455`、`internal/appcore/manager.go`（刪 `legacyWSIDLocked` 與舊簽名）
- Test: `app_codex_dispatch_test.go`

**Interfaces:**
- Produces:
```go
// WSID 進入 production start 路徑——pending start 在 response 抵達前即知歸屬。
func (a *App) startCodex(w appcore.WSID, prompt, resume, recordCase, approvalPolicy string) (string, bool, error)
func (a *App) startCodexHost(w appcore.WSID, host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error)
func (a *App) hostByThread(threadID string) *sessionHost
func (a *App) hostByTurn(turnID string) *sessionHost
```
- **刪除**：`App.runner`／`track`／`codexLease`／`currentRunner()`；`Manager` 的 `legacyWSIDLocked` 與全部 provider-keyed 舊簽名（`...WS` 方法同時改名為無後綴）。

- [ ] **Step 1: 失敗測試**

```go
func TestCodexTwoThreadsDoNotCrossWire(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	th1 := mustStartCodex(t, a, w1)
	th2 := mustStartCodex(t, a, w2)

	a.fakeWire.PushNotification(th2, `{"type":"delta","text":"for-w2"}`)
	a.fakeWire.PushApproval(th1, "appr-1")

	if !containsText(eventsFor(t, a, w2), "for-w2") {
		t.Fatal("notification 未歸屬 w2")
	}
	if containsText(eventsFor(t, a, w1), "for-w2") {
		t.Fatal("notification 串線到 w1")
	}
	if pa := a.pendingByID("appr-1"); pa == nil || pa.wsid != w1 {
		t.Fatalf("approval 必須帶 WSID：%+v", pa)
	}
}

// completed-before-response：走 production start 路徑的惡意順序容錯
// （Task 0 的 live probe 不判定此項——真 server 不一定自然產生此順序）
func TestCompletedBeforeResponseOnProductionPath(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	w := mustCreate(t, a, "codex")
	a.hookInServerTxn = func() {
		// start request 尚未回 response，就先推該 turn 的 completed
		a.fakeWire.PushCompletedForPendingStart()
	}
	if _, _, err := a.startCodexHost(w, a.codexHostOverride, "p", "", "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	if !containsKind(eventsFor(t, a, w), "result") {
		t.Fatal("completed-before-response 保證遺失")
	}
	if crossed := eventsForOtherThan(t, a, w); len(crossed) != 0 {
		t.Fatalf("pending start 期間的事件不得落到其他 WSID：%+v", crossed)
	}
}

func TestUnattributableFrameFailsLoud(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	mustStartCodex(t, a, mustCreate(t, a, "codex"))
	err := a.dispatchNotification("unknown-thread", []byte(`{"type":"delta"}`))
	if err == nil {
		t.Fatal("無法歸屬必須 fail loud，不得落到『當前』session")
	}
}

func TestNoLegacyShimRemains(t *testing.T) {
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"runner", "track", "codexLease"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Codex 單例欄位 %s 應已刪除", name)
		}
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestCodexTwoThreads|TestCompletedBeforeResponse|TestUnattributable|TestNoLegacyShim' -race -v` → FAIL

- [ ] **Step 3: 實作**

`a.mu` 下維護 `threadToWSID`／`turnToWSID`／`pendingStartToWSID map[string]appcore.WSID`（key 為 request id）。`startCodexHost` 收 WSID → 送 `thread/start` 前先登記 `pendingStartToWSID[reqID] = w` → response 抵達時補 `threadToWSID[threadID] = w`。所有 notification／approval／timeout／decision 依序查 `turnToWSID` → `threadToWSID` → `pendingStartToWSID`，查不到即 fail loud。

- [ ] **Step 4: 刪除相容層並確認無殘留**

```bash
grep -rn "currentRunner\|legacyWSIDLocked\|SubmitWS\|EmitWS" --include='*.go' . \
  && echo "仍有殘留——必須清乾淨或完成改名" || echo OK
```

- [ ] **Step 5: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 6: Commit**

```bash
git add app.go internal/appcore/ app_codex_dispatch_test.go
git commit -m "feat(app): Codex WSID dispatcher＋startCodexHost(wsid,…)，刪除 currentRunner 與 Manager 相容入口（§3.3）"
```

---

## Phase 3 — Codex connection-wide wire log

### Task 10: `internal/wirelog` generation＋frame index

**Files:**
- Create: `internal/wirelog/wirelog.go`、`frameindex.go`＋測試

**Interfaces:**
- Produces:
```go
type Direction string
const (DirClientToServer Direction = "c2s"; DirServerToClient Direction = "s2c")

type SegmentRef struct {
	WireLogID  string `json:"wire_log_id"`
	FirstFrame int64  `json:"first_frame"`
	LastFrame  int64  `json:"last_frame"`
}
type FrameKey struct{ WireLogID string; Direction Direction; RequestID string }

type Generation struct{ /* id、file、frameSeq、mu、latched error、finalMeta */ }
func NewGeneration(dir, id string) (*Generation, error) // handshake 前呼叫
func (g *Generation) ID() string
func (g *Generation) Line(dir Direction, b []byte) error
func (g *Generation) Attribute(frame int64, wsid string)
func (g *Generation) Finalize(m recorder.Meta) error
func (g *Generation) Finalized() bool
func (g *Generation) FinalMeta() recorder.Meta
func (g *Generation) Err() error
func (g *Generation) FrameIndex() *FrameIndex
func RebuildFrameIndex(path string) (*FrameIndex, error)
```

- [ ] **Step 1: 失敗測試**

```go
func TestFrameKeyNeedsDirection(t *testing.T) {
	g, _ := NewGeneration(t.TempDir(), "g1")
	_ = g.Line(DirClientToServer, []byte(`{"id":7,"method":"thread/start"}`))
	_ = g.Line(DirServerToClient, []byte(`{"id":7,"method":"approval/request"}`))
	idx := g.FrameIndex()
	c2s := idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirClientToServer, RequestID: "7"})
	s2c := idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirServerToClient, RequestID: "7"})
	if len(c2s) != 1 || len(s2c) != 1 || c2s[0] == s2c[0] {
		t.Fatal("同 requestID 不同 direction 必須可區分（§3.4.3）")
	}
}

func TestUnattributedFrameStillWritten(t *testing.T) {
	dir := t.TempDir()
	g, _ := NewGeneration(dir, "g1")
	_ = g.Line(DirServerToClient, []byte(`{"unknown":"no-thread"}`))
	_ = g.Finalize(recorder.Meta{Provider: "codex"})
	b, _ := os.ReadFile(filepath.Join(dir, "g1.jsonl"))
	if !strings.Contains(string(b), "no-thread") {
		t.Fatal("無法歸屬的 frame 不得丟棄（§3.4.5）")
	}
}

func TestLineErrorLatchesAndStaysLatched(t *testing.T) {
	g, _ := NewGeneration(t.TempDir(), "g1")
	g.ForceWriteErrForTest(errors.New("disk full"))
	if err := g.Line(DirClientToServer, []byte("x")); err == nil {
		t.Fatal("寫入失敗必須回錯")
	}
	g.ForceWriteErrForTest(nil)
	if g.Err() == nil {
		t.Fatal("錯誤必須 latch，不因後續成功而清除（§3.4.6）")
	}
}

func TestFrameIndexIsRebuildable(t *testing.T) {
	dir := t.TempDir()
	g, _ := NewGeneration(dir, "g1")
	_ = g.Line(DirClientToServer, []byte(`{"id":1}`))
	_ = g.Line(DirServerToClient, []byte(`{"id":1}`))
	_ = g.Finalize(recorder.Meta{Provider: "codex"})
	rebuilt, err := RebuildFrameIndex(filepath.Join(dir, "g1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt.Snapshot(), g.FrameIndex().Snapshot()) {
		t.Fatal("frame index 必須可由 wire log 完整重建（§3.4.3）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wirelog/ -v` → FAIL

- [ ] **Step 3: 實作** — frame 以單調 `frameSeq` 編號；wire log 為 JSONL，每行含 `{frame, dir, wsid, raw}`。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wirelog/
git commit -m "feat(wirelog): connection-wide wire log generation＋可重建 frame index（§3.4.1-5）"
```

### Task 11: `[]SegmentRef` 跨 generation 歸屬

**Files:**
- Modify: `internal/wirelog/wirelog.go`
- Test: `internal/wirelog/segments_test.go`

**Interfaces:**
- Produces: `type SegmentSet struct{…}`；`NewSegmentSet()`；`(*SegmentSet) Append(wsid string, ref SegmentRef)`；`(*SegmentSet) For(wsid string) []SegmentRef`。

- [ ] **Step 1: 失敗測試**

```go
func TestSegmentsSpanTwoGenerations(t *testing.T) {
	set := NewSegmentSet()
	set.Append("w1", SegmentRef{WireLogID: "g1", FirstFrame: 1, LastFrame: 10})
	set.Append("w2", SegmentRef{WireLogID: "g1", FirstFrame: 11, LastFrame: 20})
	set.Append("w1", SegmentRef{WireLogID: "g2", FirstFrame: 1, LastFrame: 5}) // restart 後延續

	got := set.For("w1")
	if len(got) != 2 || got[0].WireLogID != "g1" || got[1].WireLogID != "g2" {
		t.Fatalf("同 WSID 必須跨 generation 有序延續：%+v", got)
	}
	for _, r := range got {
		if r.WireLogID == "g1" && (r.FirstFrame < 1 || r.LastFrame > 10) {
			t.Fatalf("混入他 session 的 frame：%+v", r)
		}
	}
}

func TestForReturnsCopy(t *testing.T) {
	set := NewSegmentSet()
	set.Append("w1", SegmentRef{WireLogID: "g1", FirstFrame: 1, LastFrame: 2})
	got := set.For("w1")
	got[0].LastFrame = 999
	if set.For("w1")[0].LastFrame != 2 {
		t.Fatal("For 必須回傳副本，呼叫端不得改寫內部狀態")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wirelog/ -run TestSegments -v` → FAIL

- [ ] **Step 3: 實作** — `map[string][]SegmentRef`＋mutex；append 順序即時間序。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wirelog/
git commit -m "feat(wirelog): []SegmentRef 跨 generation 有序歸屬（§3.4.4）"
```

### Task 12: `codexGenerationOwner`——connection-level recorder ownership

**Files:**
- Create: `internal/codex/owner.go`
- Modify: `internal/codex/probe.go:24-71`
- Test: `internal/codex/owner_test.go`、`probe_test.go`

**Interfaces:**
- Produces:
```go
// codexGenerationOwner 是 Single 持有的 ownership 單位——server 與其 generation
// 綁在一起，故死亡／restart／shutdown 任一路徑都拿得到 generation 去 finalize。
type GenerationOwner struct {
	Server     probeTarget
	Generation *wirelog.Generation
}
func (o *GenerationOwner) Done() <-chan struct{} { return o.Server.Done() } // 滿足 Alive
// FinalizeWith：Terminate → Wait → Generation.Finalize（以 Exit 填 meta）。冪等。
func (o *GenerationOwner) FinalizeWith(stage error) error

// handoff=true（production／受控復原）：成功後不 Stop、不 Close，ownership 隨
// GenerationOwner 交給 Single，直到 server 終止才 finalize（§3.4.7）。
func RunHandshakeProbe(ctx context.Context, single *Single[*GenerationOwner],
	newGen func() (*wirelog.Generation, error), start func() (probeTarget, error),
	ci ClientInfo, handoff bool) error
```

- [ ] **Step 1: 失敗測試——三階段失敗＋意外 Done**

```go
func currentOwner(t *testing.T, s *Single[*GenerationOwner]) *GenerationOwner {
	t.Helper()
	o, ok := s.Take() // Single 無 Current()；用既有 Take 取出後視需要回填
	if !ok {
		return nil
	}
	_ = s.Ensure(func() (*GenerationOwner, error) { return o, nil })
	return o
}

func TestHandoffKeepsRecorderOpen(t *testing.T) {
	stub := newStubServer()
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	if err := RunHandshakeProbe(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{}, true); err != nil {
		t.Fatal(err)
	}
	if stub.stopRecordingCalls != 0 {
		t.Fatal("handoff 模式不得 StopRecording（§3.4.7）")
	}
	if gen.Finalized() {
		t.Fatal("handoff 模式不得在發布時 finalize")
	}
	o := currentOwner(t, &single)
	if o == nil || o.Generation != gen {
		t.Fatal("Single 必須持有 generation ownership")
	}
}

func TestGenerationIDAllocatedBeforeAttach(t *testing.T) {
	stub := newStubServer()
	var order []string
	stub.onBeginRecording = func() { order = append(order, "attach") }
	var single Single[*GenerationOwner]
	_ = RunHandshakeProbe(context.Background(), &single,
		func() (*wirelog.Generation, error) { order = append(order, "gen_id"); return newTestGeneration(t), nil },
		func() (probeTarget, error) { order = append(order, "start"); return stub, nil },
		ClientInfo{}, true)
	if !reflect.DeepEqual(order, []string{"gen_id", "start", "attach"}) {
		t.Fatalf("wire_log_id 必須在掛 recorder 前配置：%v", order)
	}
}

func TestThreeStageFailuresDisposeAndKeepEvidence(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*stubServer)
	}{
		{"start", func(s *stubServer) { s.startErr = errors.New("spawn failed") }},
		{"attach", func(s *stubServer) { s.beginRecordingErr = errors.New("attach failed") }},
		{"handshake", func(s *stubServer) { s.handshakeErr = errors.New("handshake refused") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubServer()
			stub.exit = proc.Exit{Code: 3, StderrTail: "boom"}
			c.setup(stub)
			var single Single[*GenerationOwner]
			gen := newTestGeneration(t)
			err := RunHandshakeProbe(context.Background(), &single,
				func() (*wirelog.Generation, error) { return gen, nil },
				func() (probeTarget, error) {
					if stub.startErr != nil {
						return nil, stub.startErr
					}
					return stub, nil
				}, ClientInfo{}, true)
			if err == nil {
				t.Fatal("失敗階段必須回錯")
			}
			if gen.ID() == "" {
				t.Fatal("失敗的 generation 仍須保留 wire_log_id")
			}
			if !gen.Finalized() {
				t.Fatal("失敗的 generation 必須 finalize（留收尾證據）")
			}
			if c.name != "start" { // start 失敗時沒有 server，無 exit code
				m := gen.FinalMeta()
				if m.ExitCode == nil || *m.ExitCode != 3 || !strings.Contains(m.StderrTail, "boom") {
					t.Fatalf("必須保留收尾證據：%+v", m)
				}
			}
			if o, ok := single.Take(); ok && o != nil {
				t.Fatal("不得留下未發布 server")
			}
		})
	}
}

func TestUnexpectedServerDeathFinalizesGeneration(t *testing.T) {
	stub := newStubServer()
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	_ = RunHandshakeProbe(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{}, true)

	stub.die() // 意外死亡：Conn.Done 關閉
	o := currentOwner(t, &single)
	if err := o.FinalizeWith(errors.New("server died")); err != nil {
		t.Fatal(err)
	}
	if !gen.Finalized() {
		t.Fatal("server 意外死亡也必須在 Done 後 finalize（§3.4.2）")
	}
	if err := o.FinalizeWith(nil); err != nil {
		t.Fatalf("FinalizeWith 必須冪等：%v", err)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/codex/ -run 'TestHandoff|TestGenerationID|TestThreeStage|TestUnexpectedServerDeath' -race -v` → FAIL

- [ ] **Step 3: 實作**

`newGen` 在 `start()` **之前**呼叫（wire_log_id 前置配置）；成功且 `handoff` 為真時 `return &GenerationOwner{Server: srv, Generation: gen}, true, nil`——不 `StopRecording`、不 `Finalize`。失敗一律 `owner.FinalizeWith(stageErr)`（Terminate → Wait → Finalize with Exit）後 `keep=false`。`handoff=false` 維持原 probe-scoped 行為，供既有 B1 錄流測試沿用。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/codex/
git commit -m "feat(codex): GenerationOwner 承載 connection-level recorder ownership＋三階段失敗 dispose（§3.4.7）"
```

### Task 13: recorder error latch＋in-process 受控復原

**Files:**
- Modify: `app.go:4456-4505`（`RestartCodexServerRecorded`）
- Test: `app_wirelog_latch_test.go`

**Interfaces:**
- Consumes: Task 12 `GenerationOwner`。
- Produces:
```go
func (a *App) wireLatched() bool
func (a *App) latchWireRecorder(err error)  // latch → 每 generation 一次 workspace 通知
func (a *App) RecoverCodexRecording() error // latch 下的 in-process 復原入口（§3.4.7）
```

- [ ] **Step 1: 失敗測試**

```go
func TestLatchBlocksNewSessionButNotRecovery(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	a.latchWireRecorder(errors.New("disk full"))
	if _, err := a.CreateSession("codex", "t"); err == nil {
		t.Fatal("latch 必須拒絕新 Codex session")
	}
	if _, err := a.CreateSession("claude", "t"); err != nil {
		t.Fatalf("latch 不得波及 Claude：%v", err)
	}
	if err := a.RecoverCodexRecording(); err != nil {
		t.Fatalf("latch 不得擋受控復原：%v", err)
	}
	if a.wireLatched() {
		t.Fatal("新 generation 全部成功後應解除 latch")
	}
}

func TestRecoveryOrderAndFailureKeepsLatch(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	var order []string
	a.hookWireStep = func(s string) { order = append(order, s) }
	a.latchWireRecorder(errors.New("disk full"))
	a.fakeWire.FailHandshake = true

	if err := a.RecoverCodexRecording(); err == nil {
		t.Fatal("handshake 失敗必須回錯")
	}
	if !a.wireLatched() {
		t.Fatal("失敗必須保留 latch")
	}
	if o, ok := a.codexSingle.Take(); ok && o != nil {
		t.Fatal("不得留下未發布 server")
	}
	want := []string{"drain", "terminate", "wait", "finalize_old", "new_wire_log_id", "start", "attach", "handshake"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("順序必須符合 §3.4.2／§3.4.7：\n got=%v\nwant=%v", order, want)
	}
}

func TestRecoveryRefusedWhileLiveHostOrInFlightTurn(t *testing.T) {
	for _, c := range []string{"live_host", "in_flight_turn"} {
		t.Run(c, func(t *testing.T) {
			a := newTestAppWithFakeWire(t)
			w := mustCreate(t, a, "codex")
			mustStartCodex(t, a, w)
			if c == "in_flight_turn" {
				mustBeginTurn(t, a, w)
			}
			a.latchWireRecorder(errors.New("x"))
			if err := a.RecoverCodexRecording(); err == nil {
				t.Fatal("存在 live host／in-flight turn 時必須拒絕（§3.4.7）")
			}
			if !a.wireLatched() {
				t.Fatal("拒絕不得改變 latch 狀態")
			}
		})
	}
}

func TestLatchNotifiesOncePerGeneration(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	var n int
	a.hookWorkspaceNotice = func(string) { n++ }
	a.latchWireRecorder(errors.New("e1"))
	a.latchWireRecorder(errors.New("e2"))
	a.latchWireRecorder(errors.New("e3"))
	if n != 1 {
		t.Fatalf("每 generation 只發一次通知：%d", n)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestLatch|TestRecovery' -race -v` → FAIL

- [ ] **Step 3: 實作** — `RecoverCodexRecording` 與 `RestartCodexServerRecorded` 共用同一段編排（`handoff=true`），全段在 `codexSingle.WithExclusive` 內；latch 以 per-generation `sync.Once` 保證單次通知；**全部成功才**解除 latch。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_wirelog_latch_test.go
git commit -m "feat(app): recorder error latch＋in-process 受控復原（§3.4.6-7）"
```

---

## Phase 4 — Replay index

### Task 14: `AuditSink.Write` 回傳 `AppendReceipt`

**Files:**
- Modify: `internal/appcore/sink.go:14-40`、`manager.go:500-510`、`app.go:282-287`、`app.go:351-354`
- Test: `internal/appcore/sink_test.go`

**Interfaces:**
- Produces:
```go
type AppendReceipt struct {
	StartOffset int64  `json:"start_offset"`
	EndOffset   int64  `json:"end_offset"`
	EventID     string `json:"event_id"`
}
type AuditSink interface {
	Write(env contract.Envelope) (AppendReceipt, error)
	Close() error
}
```

- [ ] **Step 1: 失敗測試**

```go
func TestJSONLSinkReceiptMatchesFileOffsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	s, err := NewJSONLSink(p)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := s.Write(contract.Envelope{EventID: "e1", Kind: "message"})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Write(contract.Envelope{EventID: "e2", Kind: "message"})
	_ = s.Close()

	b, _ := os.ReadFile(p)
	if r1.StartOffset != 0 || r2.StartOffset != r1.EndOffset {
		t.Fatalf("offset 必須連續：%+v %+v", r1, r2)
	}
	if int64(len(b)) != r2.EndOffset {
		t.Fatalf("EndOffset 必須等於檔案長度：%d vs %d", r2.EndOffset, len(b))
	}
	if !strings.Contains(string(b[r1.StartOffset:r1.EndOffset]), `"e1"`) {
		t.Fatal("receipt 範圍未涵蓋該筆")
	}
	if r1.EventID != "e1" {
		t.Fatalf("receipt EventID 錯：%s", r1.EventID)
	}
}

func TestSinkReopenContinuesOffsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	s1, _ := NewJSONLSink(p)
	r1, _ := s1.Write(contract.Envelope{EventID: "e1", Kind: "message"})
	_ = s1.Close()
	s2, _ := NewJSONLSink(p) // 重開：O_APPEND，offset 必須接續
	r2, _ := s2.Write(contract.Envelope{EventID: "e2", Kind: "message"})
	if r2.StartOffset != r1.EndOffset {
		t.Fatalf("重開後 offset 未接續：%+v %+v", r1, r2)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/appcore/ -run TestJSONLSink -v` → FAIL

- [ ] **Step 3: 實作** — `JSONLSink` 開檔時以 `f.Seek(0, io.SeekEnd)` 取得初始 offset；`failedSink.Write` 回 `AppendReceipt{}, s.reason`。

- [ ] **Step 4: 全綠（既有 audit 測試不得減少）** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appcore/ app.go
git commit -m "feat(appcore): AuditSink.Write 回傳 AppendReceipt（§3.5.2）"
```

### Task 15: `internal/replayindex` 目錄形狀＋turn boundary

**Files:**
- Create: `internal/replayindex/index.go`＋測試

**Interfaces:**
- Produces:
```go
type TurnRecord struct {
	StartOffset, EndOffset  int64
	FirstEventID, LastEventID string
}
type Config struct {
	Notify func(msg string) // 每個 degraded generation 只呼叫一次（Task 16）
}
type Index struct{ /* dir、checkpoint、mu、degraded、rebuildCursor */ }

func Open(dir string) (*Index, error)
func OpenWith(dir string, cfg Config) (*Index, error)
func (i *Index) Observe(env contract.Envelope, r appcore.AppendReceipt) error
func (i *Index) RecentTurns(wsid string, n int) ([]TurnRecord, error)
func (i *Index) TurnsBefore(wsid, beforeEventID string, n int) ([]TurnRecord, error)
func (i *Index) OpenTurnStart(wsid string) (int64, bool)
func (i *Index) Checkpoint() (offset int64, lastEventID string)
```

- [ ] **Step 1: 失敗測試**

```go
func TestTurnBoundaryDefinition(t *testing.T) {
	i, _ := Open(t.TempDir())
	obs := func(kind, role, state, id string, off int64) {
		_ = i.Observe(contract.Envelope{EventID: id, Kind: kind, Role: role, State: state,
			WorkspaceSessionID: "w1"},
			appcore.AppendReceipt{StartOffset: off, EndOffset: off + 10, EventID: id})
	}
	obs("init", "system", "", "e1", 0)          // 無 canonical user message → 不成 turn
	obs("message", "user", "", "e2", 10)        // turn 起
	obs("delta", "assistant", "", "e3", 20)
	obs("result", "system", "", "e4", 30)
	obs("state_change", "system", "done", "e5", 40) // terminal：turn 止

	turns, _ := i.RecentTurns("w1", 10)
	if len(turns) != 1 {
		t.Fatalf("init 不得被猜成一個 turn，應恰 1 個完整 turn：%d", len(turns))
	}
	if turns[0].StartOffset != 10 || turns[0].EndOffset != 50 {
		t.Fatalf("turn 範圍錯：%+v", turns[0])
	}
	if turns[0].FirstEventID != "e2" || turns[0].LastEventID != "e5" {
		t.Fatalf("首末 event ID 錯：%+v", turns[0])
	}
}

func TestStreamErrorClosesTurnAsFailed(t *testing.T) {
	i, _ := Open(t.TempDir())
	feedUserMsg(t, i, "w1", 0)
	feed(t, i, "w1", "stream_error", "", 10)
	feed(t, i, "w1", "state_change", "failed", 20)
	if turns, _ := i.RecentTurns("w1", 5); len(turns) != 1 {
		t.Fatalf("failed turn 也算完整 turn：%d", len(turns))
	}
}

func TestOpenTurnNotIndexed(t *testing.T) {
	i, _ := Open(t.TempDir())
	feedUserMsg(t, i, "w1", 0)
	feed(t, i, "w1", "delta", "", 10)
	if turns, _ := i.RecentTurns("w1", 5); len(turns) != 0 {
		t.Fatal("未完成 turn 不得入 index（§3.5.8）")
	}
	if off, ok := i.OpenTurnStart("w1"); !ok || off != 0 {
		t.Fatalf("open_turn_start_offset 必須保存：%d %v", off, ok)
	}
}

func TestSessionDoneIsNotATurn(t *testing.T) {
	i, _ := Open(t.TempDir())
	feed(t, i, "w1", "session_done", "", 0)
	if turns, _ := i.RecentTurns("w1", 5); len(turns) != 0 {
		t.Fatal("session:done 不單獨構成 turn")
	}
}

func TestPerWSIDFilesAreSeparate(t *testing.T) {
	dir := t.TempDir()
	i, _ := Open(dir)
	feedCompleteTurn(t, i, "w1", 0)
	feedCompleteTurn(t, i, "w2", 100)
	for _, w := range []string{"w1", "w2"} {
		if _, err := os.Stat(filepath.Join(dir, w+".turns.jsonl")); err != nil {
			t.Fatalf("每 WSID 一個 index 檔（§3.5 目錄形狀）：%v", err)
		}
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -v` → FAIL

- [ ] **Step 3: 實作** — `<dir>/checkpoint.json`（含每 WSID 的 `open_turn_start_offset`）＋`<dir>/<wsid>.turns.jsonl`；checkpoint 一律 atomic rename。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/
git commit -m "feat(replayindex): per-WSID turn index＋凍結 turn boundary 定義（§3.5.1／§3.5.9）"
```

### Task 16: degraded latch＋防遞迴通知

**Files:**
- Modify: `internal/replayindex/index.go`
- Test: `internal/replayindex/degraded_test.go`

**Interfaces:**
- Produces: `func (i *Index) Degraded() bool`；`func (i *Index) latchDegraded(err error)`；`Config.Notify` 生效。

> 本 task 排在損壞分級（Task 18）**之前**，因為 quarantine 的復原通知要用 `Config.Notify`。

- [ ] **Step 1: 失敗測試**

```go
func TestIndexFailureDoesNotBreakAuditAndNotifiesOnce(t *testing.T) {
	var notices int
	i, _ := OpenWith(t.TempDir(), Config{Notify: func(string) { notices++ }})
	i.ForceWriteErrForTest(errors.New("index disk full"))

	for k := 0; k < 5; k++ {
		if err := i.Observe(userMsg("w1", k), receipt(k)); err != nil {
			t.Fatalf("index 失敗不得讓 provider turn 失敗：%v", err)
		}
	}
	if !i.Degraded() {
		t.Fatal("必須 latch degraded")
	}
	if notices != 1 {
		t.Fatalf("每個 degraded generation 只發一次通知：%d", notices)
	}
	if off, _ := i.Checkpoint(); off != 0 {
		t.Fatalf("degraded 期間 checkpoint 不得前移：%d", off)
	}
}

func TestNotificationEventDoesNotRecurse(t *testing.T) {
	var notices int
	var i *Index
	i, _ = OpenWith(t.TempDir(), Config{Notify: func(string) {
		notices++
		// 通知事件本身照常進 audit → 再回灌 index
		_ = i.Observe(workspaceNotice("w1"), receipt(99))
	}})
	i.ForceWriteErrForTest(errors.New("boom"))
	_ = i.Observe(userMsg("w1", 0), receipt(0))
	if notices != 1 {
		t.Fatalf("通知不得觸發遞迴：%d", notices)
	}
}

func TestLatchBeforeNotify(t *testing.T) {
	var degradedAtNotify bool
	var i *Index
	i, _ = OpenWith(t.TempDir(), Config{Notify: func(string) { degradedAtNotify = i.Degraded() }})
	i.ForceWriteErrForTest(errors.New("boom"))
	_ = i.Observe(userMsg("w1", 0), receipt(0))
	if !degradedAtNotify {
		t.Fatal("必須先 latch、後通知（§3.5.4），否則通知會再觸發 index 失敗")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -run 'TestIndexFailure|TestNotification|TestLatchBefore' -race -v` → FAIL

- [ ] **Step 3: 實作** — 先 latch（writer 停止寫入）、後通知；`Observe` 在 degraded 時直接回 nil。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/
git commit -m "feat(replayindex): degraded latch＋每 generation 單次通知防遞迴（§3.5.4）"
```

### Task 17: crash consistency 三態修復（啟動期）

**Files:**
- Create: `internal/replayindex/rebuild.go`＋測試

**Interfaces:**
- Produces: `func (i *Index) VerifyOrRebuild(auditPath string) error`（啟動期用，無並行 append）。

- [ ] **Step 1: 失敗測試**

```go
func TestIndexBehindIsCaughtUp(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, _ := Open(dir)
	truncateIndexTo(t, dir, "w1", 1)
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("落後必須補掃：%d", len(turns))
	}
}

func TestIndexAheadIsRepaired(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, _ := Open(dir)
	writeBogusCheckpoint(t, dir, 1<<30, "e-nonexistent")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	off, last := i.Checkpoint()
	if off > auditSize(t, audit) {
		t.Fatalf("超前的 checkpoint 未修復：%d", off)
	}
	if last == "e-nonexistent" {
		t.Fatal("不可信 checkpoint 必須捨棄")
	}
}

func TestCheckpointPastOpenTurnRebuilds(t *testing.T) {
	dir, audit := seedAuditWithOpenTurn(t)
	i, _ := Open(dir)
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	off, ok := i.OpenTurnStart("w1")
	if !ok {
		t.Fatal("checkpoint 越過未完成 turn 時必須能重建其起點（§3.5.5）")
	}
	if got := firstEventIDAt(t, audit, off); got != "user-msg-1" {
		t.Fatalf("open turn 起點錯：%s", got)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -run 'TestIndexBehind|TestIndexAhead|TestCheckpointPast' -v` → FAIL

- [ ] **Step 3: 實作** — 落後掃 suffix 補；超前／超界／event ID 不符 → 交 Task 18 的分級處置。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/
git commit -m "feat(replayindex): crash consistency 三態修復（§3.5.3／§3.5.5）"
```

### Task 18: 損壞處置分級

**Files:**
- Create: `internal/replayindex/corrupt.go`＋測試

**Interfaces:**
- Consumes: Task 16 的 `Config.Notify`。
- Produces: `func (i *Index) inspect(wsid string) (lastValid int64, midCorrupt bool, err error)`；`func (i *Index) quarantine(wsid string) (movedTo string, err error)`。

- [ ] **Step 1: 失敗測試**

```go
func TestTailCorruptionTruncatesAndContinues(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	var notices int
	i, _ := OpenWith(dir, Config{Notify: func(string) { notices++ }})
	appendGarbage(t, dir, "w1", "{not json\n")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("尾端 corruption 應 truncate 至最後 valid 續用：%d", len(turns))
	}
	if quarantineExists(t, dir) {
		t.Fatal("尾端 corruption 不該 quarantine")
	}
	if notices != 0 {
		t.Fatalf("尾端 truncate 不需復原通知：%d", notices)
	}
}

func TestMidCorruptionQuarantinesAndRebuilds(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	var notices int
	i, _ := OpenWith(dir, Config{Notify: func(string) { notices++ }})
	corruptMiddleLine(t, dir, "w1")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if !quarantineExists(t, dir) {
		t.Fatal("中段 corruption 必須 quarantine（§3.5.6）")
	}
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("必須全量重建：%d", len(turns))
	}
	if notices != 1 {
		t.Fatalf("必須發一次復原通知：%d", notices)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -run 'TestTailCorruption|TestMidCorruption' -v` → FAIL

- [ ] **Step 3: 實作** — 逐行解析，第一個壞行之後**仍有 valid 行**即判定中段。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/
git commit -m "feat(replayindex): 損壞分級——尾端 truncate、中段 quarantine＋復原通知（§3.5.6）"
```

### Task 19: runtime 重建交接——收斂上限與原子接回

**Files:**
- Modify: `internal/replayindex/rebuild.go`
- Test: `internal/replayindex/runtime_rebuild_test.go`

**Interfaces:**
- Produces:
```go
// 凍結常數（§3.5.7）——不得改為設定
const (
	MaxCatchUpBytes    = 1 << 20
	MaxCatchUpRecords  = 512
	MaxCatchUpAttempts = 8
)

// auditEnd 為可注入的「目前 audit 檔尾」來源：production 傳實際檔案大小；
// 測試以此模擬「取鎖期間 audit 持續增長」，不需在鎖內直接 append（production
// 中 append 也必須持有同一把 emit mutex，鎖內 append 是不可能發生的情境）。
type auditEndFunc func() (int64, error)

// RuntimeRebuild：app 運行中的重建；emitMu 為 Manager 的 emit／index 互斥。
func (i *Index) RuntimeRebuild(auditPath string, emitMu sync.Locker, auditEnd auditEndFunc) error
var ErrRebuildNotConverged = errors.New("replayindex: catch-up 未收斂，保留 degraded latch")
```
- **rebuild cursor 獨立於 checkpoint**：degraded 期間 checkpoint 依 §3.5.4 不前移，故 catch-up 的續掃位置改用 `Index.rebuildCursor`（in-memory，只在成功接回時才寫進 checkpoint）。

- [ ] **Step 1: 失敗測試——三條 barrier**

```go
// (1) TOCTOU：事件恰落在「鎖外補掃完成、mutex 尚未取得」的窗口
func TestRebuildCoversPreLockWindow(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, _ := Open(dir)
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var once sync.Once
	i.hookAfterResidualOKBeforeLock = func() {
		once.Do(func() { appendCompleteTurn(t, audit, "w1", "late-turn") })
	}
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	turns, _ := i.RecentTurns("w1", 10)
	if len(turns) != 3 {
		t.Fatalf("鎖前窗口的事件必須被鎖內補掃涵蓋：%d", len(turns))
	}
	if dup := duplicateRanges(turns); dup != 0 {
		t.Fatalf("不得產生重複 record：%d", dup)
	}
	if i.Degraded() {
		t.Fatal("成功重建應解除 latch")
	}
}

// (2) sustained-append (a)：鎖外 catch-up 始終無法達標——hook 掛在殘量檢查「之前」
func TestRebuildNeverConvergesKeepsLatch(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, _ := Open(dir)
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var lockAcquired int
	i.hookAfterResidualOKBeforeLock = func() { lockAcquired++ }
	// 每輪鎖外 catch-up 結束時都灌入超過上限的殘量 → 殘量檢查永遠不通過
	i.hookAfterUnlockedCatchUp = func() { appendBytes(t, audit, MaxCatchUpBytes*2) }

	err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit))
	if !errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("界限內未達標應回 ErrRebuildNotConverged：%v", err)
	}
	if !i.Degraded() {
		t.Fatal("未收斂必須保留 degraded latch")
	}
	if got := i.CatchUpAttemptsForTest(); got != MaxCatchUpAttempts {
		t.Fatalf("必須有嘗試界限、不得 busy-loop：%d", got)
	}
	if lockAcquired != 0 {
		t.Fatalf("殘量從未達標，不應進入取鎖階段：%d", lockAcquired)
	}
	if i.MaxBytesScannedUnderLockForTest() != 0 {
		t.Fatal("從未取鎖，鎖內不應有掃描")
	}
}

// (3) sustained-append (b)：達標後取鎖時再次超限
// 以 auditEnd 注入模擬「等待 emitMu 期間其他 goroutine 持續 append」——
// 這正是 production 中取鎖阻塞窗口會發生的事。
func TestRebuildOverLimitUnderLockUnlocksAndRetries(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, _ := Open(dir)
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	real := realAuditEnd(audit)
	burst := 2
	fake := func() (int64, error) {
		end, err := real()
		if err != nil {
			return 0, err
		}
		if i.HoldingLockForTest() && burst > 0 { // 只在鎖內讀時放大
			burst--
			return end + MaxCatchUpBytes*2, nil
		}
		return end, nil
	}
	if err := i.RuntimeRebuild(audit, &emitMu, fake); err != nil {
		t.Fatal(err)
	}
	if got := i.MaxBytesScannedUnderLockForTest(); got > MaxCatchUpBytes {
		t.Fatalf("鎖內處理量超過凍結上限：%d", got)
	}
	if i.UnlockRetriesForTest() < 2 {
		t.Fatalf("超限應立即解鎖重試：%d", i.UnlockRetriesForTest())
	}
	if dup := duplicateRanges(mustTurns(t, i, "w1")); dup != 0 {
		t.Fatalf("重試不得產生重複 record：%d", dup)
	}
}

func TestRebuildCursorIndependentOfCheckpoint(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, _ := Open(dir)
	i.latchDegraded(errors.New("seed"))
	before, _ := i.Checkpoint()
	var emitMu sync.Mutex
	i.hookAfterUnlockedCatchUp = func() {
		if off, _ := i.Checkpoint(); off != before {
			t.Errorf("degraded 期間 checkpoint 不得前移：%d → %d", before, off)
		}
		if i.RebuildCursorForTest() <= before {
			t.Error("rebuild cursor 必須獨立前進")
		}
	}
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	if after, _ := i.Checkpoint(); after <= before {
		t.Fatal("成功接回後 checkpoint 才前移")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -run TestRebuild -race -v` → FAIL

- [ ] **Step 3: 實作五步序列**

```go
func (i *Index) RuntimeRebuild(auditPath string, emitMu sync.Locker, auditEnd auditEndFunc) error {
	if err := i.bulkRebuild(auditPath, auditEnd); err != nil { // 1. 掃至初始高水位（不持鎖）
		return err
	}
	for attempt := 0; attempt < MaxCatchUpAttempts; attempt++ {
		i.attempts++
		if err := i.catchUpUnlocked(auditPath, auditEnd); err != nil { // 2. 鎖外反覆 catch-up
			return err
		}
		if i.hookAfterUnlockedCatchUp != nil {
			i.hookAfterUnlockedCatchUp()
		}
		ok, err := i.residualWithinLimit(auditEnd)
		if err != nil {
			return err
		}
		if !ok {
			continue // 殘量仍超限：不取鎖，直接下一輪
		}
		if i.hookAfterResidualOKBeforeLock != nil {
			i.hookAfterResidualOKBeforeLock()
		}
		emitMu.Lock() // 3. 達標才取鎖
		i.setHoldingLock(true)
		ok, err = i.residualWithinLimit(auditEnd) // 4. 鎖內重讀
		if err != nil || !ok {
			i.setHoldingLock(false)
			emitMu.Unlock() // 超限即解鎖重試，不在鎖內硬掃
			if err != nil {
				return err
			}
			i.retries++
			continue
		}
		err = i.finalCatchUpAndAttach(auditPath, auditEnd) // 5. 補掃＋checkpoint＋writer＋解 latch
		i.setHoldingLock(false)
		emitMu.Unlock()
		return err
	}
	return ErrRebuildNotConverged // 保留 degraded latch，由 Task 20 的編排 backoff 重試
}
```

`catchUpUnlocked` 從 **`i.rebuildCursor`**（非 checkpoint）續掃；去重靠 `TurnRecord` 的 `(StartOffset, LastEventID)` 唯一鍵。

- [ ] **Step 4: 全綠＋競態穩定** — `go test ./internal/replayindex/ -race -count=30` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/
git commit -m "feat(replayindex): runtime 重建收斂上限＋原子接回＋兩種不收斂分支（§3.5.7）"
```

### Task 20: index 接線＋rebuild 編排（單一 active／取消／backoff）

**Files:**
- Modify: `internal/appcore/manager.go:500-510`、`app.go:275-325`
- Create: `rebuild_orchestrator.go`
- Test: `app_replayindex_test.go`

**Interfaces:**
- Produces:
```go
// scheduleRebuild：degraded 時觸發。同一時刻至多一個 active rebuild；
// ErrRebuildNotConverged 以指數 backoff 重試；shutdown 時取消並不阻擋收尾。
func (a *App) scheduleRebuild(reason string)
func (a *App) rebuildInFlight() bool
func (a *App) LoadTurnsBefore(wsid, beforeEventID string, n int) ([]contract.Envelope, error)
```

- [ ] **Step 1: 失敗測試**

```go
func TestOnlyOneActiveRebuild(t *testing.T) {
	a := newTestApp(t)
	release := make(chan struct{})
	a.hookRebuildEntered = func() { <-release }
	for k := 0; k < 5; k++ {
		a.scheduleRebuild("test")
	}
	if got := a.rebuildStartsForTest(); got != 1 {
		t.Fatalf("同一時刻只能有一個 active rebuild：%d", got)
	}
	close(release)
}

func TestNotConvergedTriggersBackoffRetry(t *testing.T) {
	a := newTestApp(t)
	a.hookRebuildResult = func(n int) error {
		if n < 3 {
			return replayindex.ErrRebuildNotConverged
		}
		return nil
	}
	a.scheduleRebuild("test")
	waitFor(t, func() bool { return !a.rebuildInFlight() }, "rebuild 收斂")
	if got := a.rebuildStartsForTest(); got != 3 {
		t.Fatalf("未收斂應 backoff 重試至成功：%d", got)
	}
	if a.replayIndex.Degraded() {
		t.Fatal("成功後應解除 degraded")
	}
	if !a.backoffDelaysIncreasingForTest() {
		t.Fatal("重試必須遞增 backoff，不得 busy-loop")
	}
}

func TestShutdownCancelsRebuild(t *testing.T) {
	a := newTestApp(t)
	entered := make(chan struct{})
	a.hookRebuildEntered = func() { close(entered) }
	a.hookRebuildResult = func(int) error { return replayindex.ErrRebuildNotConverged }
	a.scheduleRebuild("test")
	<-entered
	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild 不得阻擋 shutdown")
	}
	if a.rebuildInFlight() {
		t.Fatal("shutdown 必須取消 rebuild")
	}
}

func TestRestoreLoadsLast20TurnsPlusOpenTurn(t *testing.T) {
	a := newTestAppWithTurns(t, "w1", 25)
	got := a.RestoreViews()["w1"]
	if countCompleteTurns(got.Envelopes) != 20 {
		t.Fatalf("釘選 pane 應載最近 20 個完整 turn：%d", countCompleteTurns(got.Envelopes))
	}
	if !hasOpenTurn(got.Envelopes) {
		t.Fatal("未結束的目前 turn 必須一併載入（§3.8）")
	}
	if truncatedMidTurn(got.Envelopes) {
		t.Fatal("不得從 turn 中間截斷")
	}
}

func TestPagingUsesBeforeEventIDCursor(t *testing.T) {
	a := newTestAppWithTurns(t, "w1", 45)
	first := a.RestoreViews()["w1"].Envelopes
	page, err := a.LoadTurnsBefore("w1", first[0].EventID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if countCompleteTurns(page) != 20 {
		t.Fatalf("每頁 20 turn：%d", countCompleteTurns(page))
	}
	if overlaps(first, page) {
		t.Fatal("分頁不得重疊（event_id 去重）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestOnlyOneActive|TestNotConverged|TestShutdownCancels|TestRestoreLoads|TestPaging' -race -v` → FAIL

- [ ] **Step 3: 實作** — `writeAndEmitLocked` 取得 receipt 後在**同一 mutex 內**呼叫 `index.Observe`（滿足 §3.5.7 的 emit／index 同鎖前提）；編排以 `context.Context` 承載 shutdown 取消、`atomic.Bool` 保證單一 active。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appcore/manager.go app.go rebuild_orchestrator.go app_replayindex_test.go
git commit -m "feat(app): replay index 接線＋單一 active rebuild／backoff／shutdown 取消（§3.5）"
```

---

## Phase 5 — 移除、approval 與 shutdown

### Task 21: 移除＝tombstone＋名額釋放時點（含序列化 barrier）

**Files:**
- Modify: `app.go`
- Test: `app_remove_test.go`

**Interfaces:**
- Produces: `func (a *App) RemoveSession(wsid string) error`（Wails binding，Task 25 接前端）。

- [ ] **Step 1: 失敗測試**

```go
func TestRemoveReleasesSlotOnlyAfterAllStepsSucceed(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	a.wsReg = &stubRegistry{removeErr: errors.New("tombstone persist failed")}
	if err := a.RemoveSession(string(w)); err == nil {
		t.Fatal("tombstone persist 失敗必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("任一步失敗必須保留 slot（§3.6.2）：%d", got)
	}
}

func TestRemoveOrderIsFrozen(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	var order []string
	a.hookRemoveStep = func(s string) { order = append(order, s) }
	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	want := []string{"deny_approvals", "teardown", "lease_finalize", "cleanup_files", "tombstone_persist", "decrement_count"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("釋放名額必須是最後一步（§3.6.2）：\n got=%v\nwant=%v", order, want)
	}
}

func TestRemovedTombstoneSurvivesRestartAndRebuild(t *testing.T) {
	dir := t.TempDir()
	a1 := newTestAppAt(t, dir)
	w := mustCreate(t, a1, "codex")
	mustEmitTurn(t, a1, w) // audit 中留有該 WSID 的事件
	if err := a1.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	a2 := newTestAppAt(t, dir)
	if live, _ := a2.restoreSessions(); len(live) != 0 {
		t.Fatalf("removed session 不得復活：%+v", live)
	}
	if err := a2.replayIndex.VerifyOrRebuild(a2.eventsPath()); err != nil {
		t.Fatal(err)
	}
	if live, _ := a2.restoreSessions(); len(live) != 0 {
		t.Fatal("index rebuild 看到 audit 中的 WSID 也不得復活（§3.6.1）")
	}
	if got := a2.manager.SlotCount("codex"); got != 0 {
		t.Fatalf("removed 不計 slot：%d", got)
	}
}

// Remove × New 序列化 barrier：不是只看最終 count，而是證明兩者不重疊。
func TestRemoveXNewSerializedByOwnershipToken(t *testing.T) {
	a := newTestApp(t)
	for k := 0; k < appcore.MaxSessionsPerProvider; k++ {
		mustStartClaude(t, a, mustCreate(t, a, "claude")) // 佔滿 4 slot
	}
	victim := a.hostsOf("claude")[0].wsid

	inRemove := make(chan struct{})
	releaseRemove := make(chan struct{})
	var overlapped atomic.Bool
	a.hookRemoveHoldingToken = func() { // Remove 已取得 ownership token
		close(inRemove)
		<-releaseRemove
	}
	a.hookCreateHoldingToken = func() { // New 取得 token 時 Remove 若仍持有＝未序列化
		if a.removeTokenHeldForTest() {
			overlapped.Store(true)
		}
	}

	errC := make(chan error, 2)
	go func() { errC <- a.RemoveSession(string(victim)) }()
	<-inRemove
	newDone := make(chan error, 1)
	go func() { _, err := a.CreateSession("claude", "new"); newDone <- err }()

	select { // Remove 尚未放行前，New 必須因滿額被擋或阻塞——不得穿透
	case err := <-newDone:
		if err == nil {
			t.Fatal("Remove 未完成前名額尚未釋放，New 不得成功")
		}
	case <-time.After(200 * time.Millisecond): // 阻塞也是合格行為
	}
	close(releaseRemove)
	if err := <-errC; err != nil {
		t.Fatal(err)
	}
	<-newDone
	if overlapped.Load() {
		t.Fatal("Remove 與 New 的 ownership token 區段重疊＝未序列化")
	}
	if got := a.manager.SlotCount("claude"); got > appcore.MaxSessionsPerProvider {
		t.Fatalf("名額穿透：%d", got)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run TestRemove -race -v` → FAIL

- [ ] **Step 3: 實作** — 依 §3.6.2 凍結順序；Remove 與 Create 共用同一 provider-scoped ownership token。

- [ ] **Step 4: 全綠＋競態穩定** — `go test . -run TestRemoveXNew -race -count=30` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_remove_test.go
git commit -m "feat(app): 移除＝durable tombstone＋名額釋放時點＋Remove×New 序列化（§3.6.1-2）"
```

### Task 22: 待核可時移除（fail-closed deny）＋per-WSID 檔案清理

**Files:**
- Modify: `app.go`
- Test: `app_remove_approval_test.go`

**Interfaces:** Consumes Task 21 `RemoveSession`、Task 8 的 per-WSID socket／mcp 路徑。

- [ ] **Step 1: 失敗測試**

```go
func TestRemoveDeniesAllPendingApprovalsAndCleansFiles(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	sock, mcp := a.hostFor(w).sockPath, a.hostFor(w).mcpPath
	ids := seedPendingApprovals(t, a, w, 3) // 1 顯示中＋2 排隊

	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		d := recordedDecision(t, a, id)
		if d.allow || d.reason != "session_removed" {
			t.Fatalf("必須 fail-closed deny 且帶 reason：%+v", d)
		}
	}
	for _, p := range []string{sock, mcp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("移除必須清除 per-WSID 檔案 %s（§3.3）", p)
		}
	}
}

func TestPartialDenyFailureKeepsDormantSlot(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	ids := seedPendingApprovals(t, a, w, 3)
	a.failDenyForIndex(1) // 第二筆 deny 失敗

	err := a.RemoveSession(string(w))
	if err == nil {
		t.Fatal("deny 部分失敗不得靜默移除")
	}
	// 每一筆都必須被嘗試過——不得在第一個錯誤就中斷
	for _, id := range ids {
		if !a.denyAttempted(id) {
			t.Fatalf("approval %s 未被嘗試 deny", id)
		}
	}
	// 錯誤必須完整 Join，不得只留最後一個
	if !strings.Contains(err.Error(), "deny failed") {
		t.Fatalf("錯誤未 Join：%v", err)
	}
	if !a.providerTerminated(w) {
		t.Fatal("deny 部分失敗時仍須 terminate provider（§3.6.3）")
	}
	if a.hostFor(w) != nil {
		t.Fatal("host 必須已收尾")
	}
	if !a.leaseFinalized(w) {
		t.Fatal("recording lease 必須已 finalize")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("必須保留 dormant slot 供重試移除：%d", got)
	}
	if _, ok := a.wsReg.Get(string(w)); !ok {
		t.Fatal("未成功移除時 registry entry 必須保留（非 tombstone）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestRemoveDenies|TestPartialDeny' -race -v` → FAIL

- [ ] **Step 3: 實作** — best-effort deny 全部（逐筆 recover，不中斷）→ `errors.Join` 收集 → bounded teardown → lease finalize → 檔案清理；失敗仍 terminate、保留 dormant slot 與 registry entry。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_remove_approval_test.go
git commit -m "feat(app): 待核可時移除——全部 fail-closed deny＋部分失敗保留 dormant slot＋檔案清理（§3.6.3）"
```

### Task 23: shutdown 總序＋並行 teardown barrier

**Files:**
- Modify: `app.go:694-800`
- Test: `app_shutdown_multi_test.go`

**Interfaces:** Consumes Task 7 `snapshotHosts`、Task 12/13 wire log、Task 20 index。

- [ ] **Step 1: 失敗測試**

```go
func TestShutdownFollowsFrozenOrder(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	var order []string
	a.hookShutdownStep = func(s string) { order = append(order, s) }
	seedSessions(t, a, 4, 4)
	a.shutdown(context.Background())
	want := []string{
		"reject_new_txn", "stop_watchers", "snapshot", "deny_approvals",
		"interrupt_terminate", "teardown_parallel", "codex_hosts_done",
		"server_terminate_wait", "wirelog_finalize", "manager_close",
		"index_flush_close", "registry_sync",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown 總序不符 §3.6.5：\n got=%v\nwant=%v", order, want)
	}
}

// 並行 barrier：8 個 teardown 必須「同時進場」才放行——若實作是串行，
// 第二個 teardown 永遠等不到 barrier，測試以 timeout 失敗。
func TestTeardownsRunConcurrently(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 4, 4)
	const n = 8
	entered := make(chan struct{}, n)
	release := make(chan struct{})
	a.hookTeardownEntered = func(appcore.WSID) {
		entered <- struct{}{}
		<-release // 全部到齊前不放行
	}
	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()

	for k := 0; k < n; k++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("只有 %d 個 teardown 進場——teardown 未並行（§3.6.5）", k)
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown 未收斂")
	}
}

// 卡死者必須被 bounded window 收斂，且不把總時間乘以 session 數
func TestStuckHostConvergesWithinSingleBoundedWindow(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 4, 4)
	a.makeHostStuck(a.snapshotHosts()[0].wsid) // 一個 Claude 卡死，只靠 kill timeout 收斂
	start := a.fakeClock.Now()
	a.shutdown(context.Background())
	elapsed := a.fakeClock.Since(start)
	if elapsed > 2*a.closeSequenceTimeout() {
		t.Fatalf("shutdown 時間應為單一 bounded window，實測 %v（timeout=%v）",
			elapsed, a.closeSequenceTimeout())
	}
	if !a.allHostsTornDown() {
		t.Fatal("卡死者也必須收斂")
	}
}

func TestIndexAcceptsEventsUntilManagerClose(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 1, 0)
	a.hookAtManagerClose = func() { a.manager.FlushPendingForTest() }
	a.shutdown(context.Background())
	if a.replayIndex.MissedEventsForTest() != 0 {
		t.Fatal("replay index 不得在 Manager.Close 之前停止接收（§3.6.5）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run TestShutdown -race -v` → FAIL

- [ ] **Step 3: 實作** — 依 §3.6.5 逐行；per-session teardown 用 goroutine＋`WaitGroup`；卡死收斂靠既有 `CloseSequence` 的 quiesce/kill timeout（用 fake clock 驗證，不用 `time.Sleep`）。

- [ ] **Step 4: 全綠＋競態穩定** — `go test . -run 'TestTeardownsRunConcurrently|TestStuckHost' -race -count=30` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_shutdown_multi_test.go
git commit -m "feat(app): shutdown 總序凍結＋per-session 並行 teardown barrier（§3.6.5）"
```

### Task 24: approval WSID 路由（後端）

**Files:**
- Modify: `app.go:3758-3809`
- Test: `app_approval_route_test.go`

**Interfaces:** Produces：approval 事件攜帶 `workspace_session_id`；`func (a *App) promotionOrder() []string`。

- [ ] **Step 1: 失敗測試**

```go
func TestApprovalCarriesWSIDAndFIFOPromotion(t *testing.T) {
	a := newTestApp(t)
	w1, w2 := mustCreate(t, a, "claude"), mustCreate(t, a, "claude")
	mustStartClaude(t, a, w1)
	mustStartClaude(t, a, w2)
	id1, id2, id3 := seedApproval(t, a, w1), seedApproval(t, a, w2), seedApproval(t, a, w1)

	if got := a.pendingByID(id1).wsid; got != w1 {
		t.Fatalf("approval 必須依 WSID 路由：%v", got)
	}
	if got := a.promotionOrder(); !reflect.DeepEqual(got, []string{id1, id2, id3}) {
		t.Fatalf("多筆待核可 FIFO promotion（按 approval ID／WSID）：%v", got)
	}
	ev := approvalEventFor(t, a, id1)
	if ev.WorkspaceSessionID != string(w1) {
		t.Fatalf("approval 事件必須帶 WSID：%+v", ev)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run TestApprovalCarries -race -v` → FAIL

- [ ] **Step 3: 實作**

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_approval_route_test.go
git commit -m "feat(app): approval 依 WSID 路由＋FIFO promotion（§3.6.4）"
```

---

## Phase 6 — Wails bindings 與前端

### Task 25: Wails bindings 重生＋adapter 逐參數轉發

**Files:**
- Modify: `frontend/wailsjs/go/main/App.d.ts`／`App.js`（重生）、`frontend/src/lib/bindings.ts`、`frontend/src/types.ts`
- Test: `frontend/src/lib/bindings.test.ts`

**Interfaces:**
- Consumes: Task 4 `CreateSession`、Task 21 `RemoveSession`、Task 20 `LoadTurnsBefore`、Task 9 改為 WSID 的 `SendMessage`／`EndSession`／`NewSession`／`StartSession`。
- Produces: `Bindings` 介面新增／改簽名的 TypeScript 契約。

> **本 task 必做四件事**（`bindings.ts:9-12` 記載的 M1.5 P1-1 真 bug：單參數 adapter 把 provider 名當訊息送出，元件 mock 抓不到）。

- [ ] **Step 1: 失敗測試——逐參數轉發**

```ts
import { makeBindings } from './bindings'
import * as App from '../../wailsjs/go/main/App'
vi.mock('../../wailsjs/go/main/App')

it('每個 binding 都逐參數轉發，順序與 Go 簽章一致', () => {
  const b = makeBindings()
  b.CreateSession('claude', 'my-task')
  expect(App.CreateSession).toHaveBeenCalledWith('claude', 'my-task')

  b.StartSession('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')
  expect(App.StartSession).toHaveBeenCalledWith('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')

  b.SendMessage('w1', 'hello')
  expect(App.SendMessage).toHaveBeenCalledWith('w1', 'hello')

  b.EndSession('w1')
  expect(App.EndSession).toHaveBeenCalledWith('w1')

  b.RemoveSession('w1')
  expect(App.RemoveSession).toHaveBeenCalledWith('w1')

  b.LoadTurnsBefore('w1', 'e100', 20)
  expect(App.LoadTurnsBefore).toHaveBeenCalledWith('w1', 'e100', 20)
})

it('WSID 取代 provider 作為第一參數——不得誤傳 provider', () => {
  const b = makeBindings()
  b.SendMessage('01JWSIDABC', 'text')
  const [first] = vi.mocked(App.SendMessage).mock.calls.at(-1)!
  expect(first).toBe('01JWSIDABC')
  expect(['claude', 'codex']).not.toContain(first)
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npm --prefix frontend run test -- bindings` → FAIL

- [ ] **Step 3: 重生 bindings 並更新 adapter 與型別**

```bash
wails generate module     # 重生 frontend/wailsjs
```

`bindings.ts` 逐參數轉發新增／改簽名的六個 binding；`types.ts` 的 `Bindings` 介面同步（`StartSession`／`SendMessage`／`EndSession` 第一參數由 `provider` 改 `wsid: string`）。

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test && npm --prefix frontend run build` → PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/
git commit -m "feat(frontend): Wails bindings 重生＋adapter 逐參數轉發（CreateSession／RemoveSession／LoadTurnsBefore／WSID 化）"
```

### Task 26: session store 泛化為 per-WSID lane

**Files:**
- Modify: `frontend/src/stores/session.ts:40-253`
- Test: `frontend/src/stores/session.test.ts`

**Interfaces:**
- Produces:
```ts
export interface SessionMeta {
  wsid: string; provider: ProviderKey; taskLabel: string
  state: string; unread: number; busy: boolean; awaitingApproval: boolean
  removed: boolean
}
interface State {
  sessions: Record<string, SessionMeta>
  views: Record<string, SessionView>          // 只有釘選／曾切入的才有 transcript
  persistentPins: [string | null, string | null]
  pins: [string | null, string | null]        // 顯示用（transient 會暫時改寫）
  focused: 0 | 1
  scrollAnchors: Record<string, string>
}
```
- **契約凍結**：`busy`／`unread`／`awaitingApproval` 一律讀寫 `sessions[wsid]`（**不在 `views` 上**）——`views` 只承載 transcript（`chat`／`timeline`／`totals`）。

- [ ] **Step 1: 失敗測試**

```ts
it('apply 依 workspace_session_id 路由，不再依 provider', () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
  s.pin(0, 'w1'); s.pin(1, 'w2')
  s.apply({ event_id: 'e1', kind: 'delta', text: 'hello', provider: 'claude', workspace_session_id: 'w2' })
  expect(s.views['w2'].chat.at(-1)?.text).toBe('hello')
  expect(s.views['w1'].chat).toHaveLength(0)
})

it('busy／unread 只在 SessionMeta 上，views 不承載狀態', () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.pin(0, 'w1')
  s.apply({ event_id: 'e1', kind: 'message', role: 'user', provider: 'claude', workspace_session_id: 'w1' })
  expect(s.sessions['w1'].busy).toBe(true)
  expect((s.views['w1'] as Record<string, unknown>).busy).toBeUndefined()
})

it('非釘選 session 只更新 metadata 與 unread，不保留 transcript', () => {
  const s = useSession()
  s.registerSession({ wsid: 'w3', provider: 'codex', taskLabel: 'c' })
  s.apply({ event_id: 'e1', kind: 'result', provider: 'codex', workspace_session_id: 'w3' })
  expect(s.sessions['w3'].unread).toBe(1)
  expect(s.views['w3']).toBeUndefined()
})

it('解除釘選釋放 transcript，保留 metadata、已讀與捲動錨點', () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.pin(0, 'w1')
  s.apply({ event_id: 'e1', kind: 'delta', text: 'x', provider: 'claude', workspace_session_id: 'w1' })
  s.setScrollAnchor('w1', 'e1')
  s.unpin(0)
  expect(s.views['w1']).toBeUndefined()
  expect(s.sessions['w1'].unread).toBe(0)
  expect(s.scrollAnchors['w1']).toBe('e1')
})

it('focused pane 唯一，切換不重設捲動', () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2')
  s.setScrollAnchor('w1', 'e9')
  s.setFocus(1)
  expect(s.focused).toBe(1)
  expect(s.scrollAnchors['w1']).toBe('e9')
})

it('未釘選來源以 transient 顯示，六種觸發都恢復原釘選', () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  for (const trigger of ['allow', 'deny', 'timeout', 'dismiss', 'remove', 'shutdown']) {
    s.routeApproval('w3')
    expect(s.pins[1]).toBe('w3')
    expect(s.persistentPins[1]).toBe('w2')
    s.resolveApprovalPresentation(trigger)
    expect(s.pins[1]).toBe('w2')
  }
})

it('來源在另一 pane 時自動切 focus', () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  s.routeApproval('w2')
  expect(s.focused).toBe(1)
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npm --prefix frontend run test -- session` → FAIL

- [ ] **Step 3: 實作** — `providerOf` 改 `wsidOf(env)`；`submit`／`reset`／`pushError`／`applyDone` 全部作用於 focused pane 的 WSID。

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test` → PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src/stores/
git commit -m "feat(frontend): session store 泛化為 per-WSID lane＋pin／focus／transient 路由（§3.6.4／§3.7-8）"
```

### Task 27: `SessionList.vue`

**Files:**
- Create: `frontend/src/components/SessionList.vue`＋`SessionList.test.ts`
- Modify: 雙 locale

**Interfaces:** Consumes Task 26 store；emit `pin`／`create`／`remove`。

- [ ] **Step 1: 失敗測試**

```ts
it('只顯示既有 session，不畫固定 8 張空卡', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  const w = mount(SessionList, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  expect(w.findAll('[data-test=session-card]')).toHaveLength(1)
})

it('顯示 per-provider n / 4 計數', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
  const w = mount(SessionList, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  expect(w.find('[data-test=count-claude]').text()).toBe('2 / 4')
  expect(w.find('[data-test=count-codex]').text()).toBe('0 / 4')
})

it('達上限時建立按鈕停用', async () => {
  const s = useSession()
  for (let n = 1; n <= 4; n++) s.registerSession({ wsid: 'w' + n, provider: 'codex', taskLabel: '' })
  const w = mount(SessionList, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  expect(w.find('[data-test=create-codex]').attributes('disabled')).toBeDefined()
  expect(w.find('[data-test=create-claude]').attributes('disabled')).toBeUndefined()
})

it('卡片顯示 provider／狀態／unread／busy／待核可標記', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.sessions['w1'].unread = 3
  s.sessions['w1'].awaitingApproval = true
  const w = mount(SessionList, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  expect(w.find('[data-test=unread-w1]').text()).toBe('3')
  expect(w.find('[data-test=awaiting-w1]').exists()).toBe(true)
})

it('移除需確認並說明稽核保留', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  const w = mount(SessionList, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  await w.find('[data-test=remove-w1]').trigger('click')
  expect(w.find('[data-test=remove-confirm]').text()).toContain('稽核事件與錄流會永久保留')
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npm --prefix frontend run test -- SessionList` → FAIL

- [ ] **Step 3: 實作元件＋雙 locale 字串（key parity 測試須綠）**

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test` → PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src
git commit -m "feat(frontend): SessionList——既有 session 清單＋n/4 計數＋移除確認（§4）"
```

### Task 28: `DualPane.vue`＋`PaneView.vue`

**Files:**
- Create: `DualPane.vue`／`PaneView.vue`＋測試
- Modify: `App.vue`、`SettingsBar.vue`、`StatusBar.vue`

**Interfaces:** Consumes Task 26 store、既有 `ChatPanel`／`Timeline`。

- [ ] **Step 1: 失敗測試**

```ts
it('兩 pane 皆 live，背景 pane 持續更新', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
  s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: '' })
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
  s.apply({ event_id: 'e1', kind: 'delta', text: 'bg', provider: 'claude', workspace_session_id: 'w2' })
  await nextTick()
  expect(w.find('[data-test=pane-1]').text()).toContain('bg')
})

it('只有 focused pane 有 composer 與 SettingsBar 操作', async () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  expect(w.find('[data-test=pane-0] [data-test=composer]').exists()).toBe(true)
  expect(w.find('[data-test=pane-1] [data-test=composer]').exists()).toBe(false)
})

it('點另一 pane 切焦點但不卸載', async () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  const before = w.find('[data-test=pane-1]').element
  await w.find('[data-test=pane-1]').trigger('click')
  expect(s.focused).toBe(1)
  expect(w.find('[data-test=pane-1]').element).toBe(before) // 同一 DOM 節點＝未卸載
})

it('A 執行中仍可切 B 送出', async () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2')
  s.sessions['w1'].busy = true
  s.setFocus(1)
  await s.submit('hello')
  expect(s.bindings.SendMessage).toHaveBeenCalledWith('w2', 'hello')
})

it('SettingsBar 的 End／Terminate／New 只作用於 focused pane', async () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(1)
  const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
  await nextTick()
  await w.find('[data-test=end-session]').trigger('click')
  expect(s.bindings.EndSession).toHaveBeenCalledWith('w2')
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npm --prefix frontend run test -- DualPane` → FAIL

- [ ] **Step 3: 實作** — 固定 50/50 grid；`PaneView` 綁 WSID。

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test && npm --prefix frontend run build` → PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src
git commit -m "feat(frontend): 雙 pane 並看＋單一 focused pane 操作語意（§3.7／§4）"
```

### Task 29: lazy load 與向上分頁

**Files:**
- Modify: `PaneView.vue`、`stores/session.ts`
- Test: `PaneView.test.ts`

**Interfaces:** Consumes Task 25 的 `LoadTurnsBefore` binding。

- [ ] **Step 1: 失敗測試**

```ts
it('釘選時 lazy load 尾端視窗', async () => {
  const s = useSession()
  s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
  expect(s.views['w9']).toBeUndefined()
  await s.pin(0, 'w9')
  expect(s.bindings.LoadTurnsBefore).toHaveBeenCalledWith('w9', '', 20)
  expect(s.views['w9']).toBeDefined()
})

it('捲到頂以每次 20 turn 分頁並以 event_id 去重', async () => {
  const s = useSession()
  await s.pin(0, 'w9')
  const oldest = s.views['w9'].timeline[0].env.event_id
  vi.mocked(s.bindings.LoadTurnsBefore).mockResolvedValueOnce([
    { event_id: oldest, kind: 'message' },
    { event_id: 'older-1', kind: 'message' },
  ])
  await s.loadOlder('w9')
  const ids = s.views['w9'].timeline.map(i => i.env.event_id)
  expect(new Set(ids).size).toBe(ids.length)
  expect(ids).toContain('older-1')
})

it('分頁不重設捲動錨點', async () => {
  const s = useSession()
  await s.pin(0, 'w9')
  s.setScrollAnchor('w9', 'keep-me')
  await s.loadOlder('w9')
  expect(s.scrollAnchors['w9']).toBe('keep-me')
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npm --prefix frontend run test -- PaneView` → FAIL

- [ ] **Step 3: 實作**

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test` → PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src
git commit -m "feat(frontend): 釘選 lazy load＋向上 20 turn 分頁去重（§3.8）"
```

---

## Phase 7 — 不變量、迴歸與驗收

### Task 30: 跨切面不變量測試

**Files:** Test: `app_invariants_test.go`

- [ ] **Step 1: 失敗測試**

```go
func TestEventIDMonotonicAcross8ParallelSessions(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 4, 4)
	var wg sync.WaitGroup
	for _, w := range a.allWSIDs() {
		wg.Add(1)
		go func(w appcore.WSID) { defer wg.Done(); emitTurn(t, a, w, 20) }(w)
	}
	wg.Wait()
	ids := readEventIDs(t, a.eventsPath())
	for k := 1; k < len(ids); k++ {
		if ids[k] <= ids[k-1] {
			t.Fatalf("event_id 檔案級單調被破壞：%s <= %s", ids[k], ids[k-1])
		}
	}
}

func TestLegacyJournalWithoutWSIDAttributes(t *testing.T) {
	dir := seedLegacyJournalFixture(t) // M3a 時代 events.jsonl，無 workspace_session_id
	a := newTestAppAt(t, dir)
	live, err := a.restoreSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Provider != "claude" {
		t.Fatalf("legacy 事件應歸屬遷移後的 legacy session：%+v", live)
	}
	view := a.RestoreViews()[live[0].WSID]
	if len(view.Envelopes) == 0 {
		t.Fatal("legacy view window 內的事件應可重放")
	}
}

func TestSecondInFlightTurnRejected(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	if err := a.SendMessage(string(w), "first"); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "second"); err == nil {
		t.Fatal("每 session 至多一個進行中 turn（§1.1）")
	}
}

func TestWorkspaceLaneHasNoWSID(t *testing.T) {
	a := newTestApp(t)
	mustSubmitGate(t, a) // Gate 事件走 workspace lane
	for _, ev := range readEvents(t, a.stateDir) {
		if ev.Scope == "workspace" && ev.WorkspaceSessionID != "" {
			t.Fatalf("workspace lane 不得帶 WSID（§3.1.6）：%+v", ev)
		}
	}
	if got := a.manager.SlotCount("claude") + a.manager.SlotCount("codex"); got != 0 {
		t.Fatalf("workspace lane 不得計入 slot：%d", got)
	}
}
```

- [ ] **Step 2: 跑測試、修正到全綠** — `go test . -race -count=1 -v`

- [ ] **Step 3: Commit**

```bash
git add app_invariants_test.go
git commit -m "test: M3b 跨切面不變量——event_id 單調／legacy 歸屬／單一 in-flight turn／workspace lane 隔離（§5.7）"
```

### Task 31: E2E 驗收矩陣＋最終 gate

**Files:**
- Create: `docs/spikes/m3b-results.md`
- Modify: `README.md`

- [ ] **Step 1: 實機驗收逐項執行並記錄（§6）**

| # | 項目 | 判定 |
|---|---|---|
| A1 | 雙 provider × 多 session 並行（A 執行中切 B 送出） | |
| A2 | 雙 pane 並看與焦點切換、unread | |
| A3 | approval 跨 pane／未釘選 transient 路由 | |
| A4 | 8/8 上限拒絕＋`n / 4` 顯示＋關閉釋放名額 | |
| A5 | 重啟：釘選 lazy 恢復（20 turn）、非釘選 metadata、向上分頁 | |
| A6 | index 落後補掃／注入損壞 → 重建＋通知 | |
| A7 | 未完成 turn 重啟 → failed 解除 busy | |
| A8 | 舊 workspace 升級：legacy 歸屬、resume 可用、重啟不產生第二枚 WSID | |
| A9 | M2／M3a／M3a.1 迴歸抽驗（Gate 流程、TCA、收件匣、assists） | |
| A10 | Claude 4 session 常駐的 RAM／CPU 實測（§7.1） | |

- [ ] **Step 2: 收尾 gate 全綠**

```bash
go vet ./... && go test -race ./... -count=1 && \
npm --prefix frontend run test && npm --prefix frontend run build && wails build
```

- [ ] **Step 3: 寫結果文件** — A1-A10 逐項證據、缺口清單、PR 揭露事項。**未通過項如實記錄，不得標綠**。

- [ ] **Step 4: Commit**

```bash
git add docs/spikes/m3b-results.md README.md
git commit -m "docs: M3b E2E 驗收矩陣結果＋README 章節"
```

---

## Self-Review 記錄（v2）

**v1 → v2 修正（closure review 六項 P1＋四項 P2）**

| # | 問題 | 修正 |
|---|---|---|
| P1-1 | Task graph 無法維持每個 commit 可執行（Manager 改簽名未同步呼叫點、Task 3 用未建立的 registry、`go test -run` 仍編譯整個 package、Task 8 用 Task 21 的 API） | 改 additive migration：Task 3 保留 provider-keyed 相容入口、Task 9 才刪；`wsregistry` 前移至 Task 2；Phase 2 遷移紀律段落明訂三段順序；每個 task 的 Step 4 一律是全套 `go vet ./... && go test -race ./... -count=1`；per-WSID 檔案清理測試移到 Task 22 |
| P1-2 | Task 0 不可重現 | 落成 `cmd/probe-codex-parallel`；用 `scripts/check-cli.sh` 驗 bundled binary＋sha256；凍結 prompts／approval policy／兩段 timeout；自然與強制兩種收尾；wire log 即證據＋`defer os.RemoveAll` 清理；**`completed-before-response` 移出 live probe 判定**，改由 Task 9 fake-wire 測試 |
| P1-3 | Codex dispatcher 未把 WSID 傳入 production start | 凍結 `startCodex(w, …)`／`startCodexHost(w, …)`；新增 `pendingStartToWSID`；malicious-order 測試走 production path |
| P1-4 | recorder ownership 無可交棒型別、測試用不存在的 `Single.Current()` | 新增 `codex.GenerationOwner{Server, Generation}`＋`FinalizeWith`，由 `Single[*GenerationOwner]` 持有；測試改用既有 `Take()`（附 `currentOwner` helper）；補 start／attach／handshake 三階段失敗與意外 `Done` 測試 |
| P1-5 | runtime rebuild 測試與游標語意不成立 | 「始終無法達標」的 hook 改掛 `hookAfterUnlockedCatchUp`（殘量檢查之前）並斷言 `lockAcquired == 0`；鎖內增量改用 `auditEndFunc` 注入（不在鎖內 append）；新增獨立 `rebuildCursor` 與 `TestRebuildCursorIndependentOfCheckpoint`；Task 20 補單一 active／backoff／shutdown 取消編排與測試 |
| P1-6 | Wails bindings 缺席 | 新增 Task 25：重生 `wailsjs`、更新 `bindings.ts`＋`types.ts`、逐參數轉發測試（含「第一參數必須是 WSID 而非 provider」的回歸斷言） |
| P1-7 | Remove×New／shutdown 不是 barrier | Remove×New 改 channel barrier＋token 重疊偵測＋`-count=30`；shutdown 改「8 個 teardown 同時進場才放行」的 barrier，另加卡死者 bounded window（fake clock）與 `TestShutdownFollowsFrozenOrder` |
| P2-1 | Task 6 斷言末筆 | 改檢查末二筆（`stream_error` → `state_change=failed`） |
| P2-2 | 回滾重用會留 tombstone 的 Remove | 新增 `DeleteUncommitted`，與 `Remove` 分離並各自測試 |
| P2-3 | `busy` 型別契約不一致 | Task 26 凍結「狀態在 `SessionMeta`、transcript 在 `SessionView`」，並加專測；Task 28 改讀 `s.sessions['w1'].busy` |
| P2-4 | Task 22 斷言不足 | 補「每筆 deny 都被嘗試」「錯誤完整 Join」「host 已收尾」「lease 已 finalize」「slot 保留 dormant」「registry entry 保留非 tombstone」 |

**Spec 覆蓋**：§1／§2／§3.1-3.8／§4／§5／§6／§7.2 逐條對應 Task 0-31（對照表同 v1，Task 編號依新順序調整：wsregistry=Task 2、App 交易=Task 4、degraded latch=Task 16、損壞分級=Task 18）。

**型別一致性**：`appcore.WSID`、`AppendReceipt{StartOffset,EndOffset,EventID}`、`wsregistry.Entry`、`wirelog.SegmentRef`、`codex.GenerationOwner`、前端 `SessionMeta`／`SessionView`／`persistentPins`／`pins`／`focused` 全程一致。

**遺留的執行前提**：Task 3 的相容入口是**暫時**產物，Task 9 的 Step 4 有 `grep` 守門確保清乾淨；若 Task 9 之前中止，需在收尾前補刪。
