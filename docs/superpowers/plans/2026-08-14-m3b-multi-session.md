# M3b 多 session 工作區 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 同 provider 多 session（每 provider 4 slot、共 8）＋雙 pane 並看，且重啟成本與 audit 事件總量脫鉤。

**Architecture:** 擴充既有單一 `appcore.Manager` 為 per-WSID session registry（保留單一 emit mutex 與檔案級 `event_id` 單調不變量）；App 的單例 ownership 以 **additive migration** 逐步搬進 `sessionHosts`；Codex 維持共用單一 `Conn`＋connection-wide wire log＋`threadID/turnID → WSID` dispatcher；新增 per-WSID byte-offset replay index 支撐 lazy 視窗重放。

**Tech Stack:** 既有全套（Go 1.26／Wails v2／Vue 3＋Pinia＋vue-i18n／vitest）。無新依賴。

**Spec:** `docs/superpowers/specs/2026-08-14-m3b-multi-session-design.md`（rev4，closure review **APPROVED** 2026-08-14，`b63f168`）。全文 §號皆指該檔。

**執行約束**：Subagent-Driven、單一 writer、每 task 主代理 review。**Task 0（Codex 並行 live probe）是實作 gate——判定 NO-GO 即停止實作、退回 spec gate 由 owner 重裁；不得自行改成 provider-wide 串行或多 app-server 等替代架構（§7.2）**。

---

## Global Constraints

- **每個 commit 都必須是可執行基線**：`go build ./... && go vet ./... && go test -race ./... -count=1` 在**每個 task 結束時**全綠，且 app 在該 commit 下行為不退化。改簽名一律 additive（新舊並存）→ 遷呼叫點 → 最後才刪舊入口。
- **不得有向後依賴**：任何 task 的測試與實作只能使用**已完成 task** 提供的符號。撰寫 task 時逐一核對 Interfaces 的 Consumes 欄。
- **Wails 綁定的兩條規則**：
  1. **純新增的 binding**（`CreateSession`／`RemoveSession`／`LoadTurnsBefore`）在**新增它的那個 task 內**完成四件事：重生 `frontend/wailsjs` → 更新 `frontend/src/lib/bindings.ts` 逐參數轉發 → 更新 `frontend/src/types.ts` → 補 `bindings.test.ts` 轉發斷言。
  2. **改簽名的 binding**（`StartSession`／`SendMessage`／`EndSession`／`NewSession` 由 provider 改 WSID）**不得中途切換**——後端在 Task 9 之後保留一層 provider-keyed exported 包裝（內部解析到 legacy WSID），前端維持原呼叫；直到 Task 26 才**原子地**同時切換後端 exported 簽名、`wailsjs`、adapter、`types.ts` 與 session store，並刪除包裝層。
  （`bindings.ts:9-12` 記載 M1.5 P1-1 的真 bug：單參數 adapter 把 provider 名當訊息送出，元件 mock 抓不到。）
- **凍結常數不可設定**：`MaxSessionsPerProvider = 4`（§1.1）；`MaxCatchUpBytes`／`MaxCatchUpRecords`／`MaxCatchUpAttempts`（Task 19）皆為具名 Go 常數，**不進 config、不讀環境變數**。
- **事件權威唯一**：`events.jsonl` append-only、完整不裁切；registry 與 replay index 都是快取／metadata（§3.2.7、§3.5.10）。
- **event_id 檔案級嚴格遞增**：所有新路徑一律經 `Manager.writeAndEmitLocked` 的單一 mutex（§2）。
- **Fail loud**：slot 超限、未知 WSID、provider 不符、registry persist 失敗、recorder 寫入失敗、migration persist 失敗一律回錯並顯示。
- **無閒置回收**：只有使用者明確「關閉／移除」才釋放名額（§1 Slot 語意）。
- **Barrier 測試不得依賴 `time.Sleep` 或真實 timeout**：一律用 `hook*` 注入點、channel barrier 或可注入的 `After`；併發測試 `-race`，關鍵競態另跑 `-count=30`。
- **i18n**：新 UI 字串進 `zh-TW`＋`en` 雙 locale、key parity 測試綠；契約值（provider 名、WSID、event kind）維持原文。
- **收尾 gate**：`go vet ./...`／`go test -race ./... -count=1`／`npm --prefix frontend run test`／`npm --prefix frontend run build`／`wails build`。

---

## File Structure

**新增 Go：** `internal/wsregistry/`（durable metadata store＋legacy 遷移）、`internal/replayindex/`（turn index／degraded／損壞分級／runtime 重建）、`internal/wirelog/`（generation／frame index／`SegmentRef`）、`cmd/probe-codex-parallel/`（Task 0 driver）、`session_host.go`、`rebuild_orchestrator.go`。

**修改 Go：** `internal/appcore/manager.go`（per-WSID registry＋相容入口）、`internal/appcore/sink.go`（`AppendReceipt`）、`internal/appcore/pump.go`（`EndSessionFlow` WSID 化、`WaitQuiesce`／`CloseSequence` 可注入 `After`）、`internal/contract/envelope.go`、`internal/codex/{single,probe,session,owner}.go`、`app.go`、`restore.go`。

**前端：** 新增 `SessionList.vue`／`PaneView.vue`／`DualPane.vue`＋測試；修改 `stores/session.ts`、`lib/bindings.ts`、`types.ts`、`App.vue`、`SettingsBar.vue`、`StatusBar.vue`、雙 locale。

**文件：** `docs/spikes/m3b-codex-parallel.md`（Task 0）、`docs/spikes/m3b-results.md`（Task 30）。

---

## Phase 0 — 實作 gate

### Task 0: Codex 單 app-server 多 thread 並行 live probe（NO-GO gate）

**Files:**
- Create: `cmd/probe-codex-parallel/main.go`、`docs/spikes/m3b-codex-parallel.md`

**Interfaces:**
- Consumes: 既有 `codex.StartAppServer(ctx, codex.Config{Binary, CWD, Env, TermGrace})`（`internal/codex/session.go:26`）、`Server.BeginRecording`／`Handshake`／`Terminate`／`Wait`／`StopRecording`。
- Produces: 可重現的 probe driver＋GO/NO-GO 判定。
- **判定範圍（凍結）**：(a) 兩 thread 並行 turn 是否**真並行**、(b) notification **與 approval request** 是否帶足以歸屬的 thread／turn identity、(c) 自然與強制兩種收尾是否 bounded 收斂且錄到最後一筆 frame。**`completed-before-response` 不列入**——它是 host 對惡意／異常順序的容錯，真 server 不一定自然產生；改由 Task 9 的 fake-wire 測試鎖住。

- [ ] **Step 1: 驗 bundled binary 與 pin 版本**

```bash
./scripts/check-cli.sh          # 驗 tools/codex-cli pin 版本並輸出 sha256
CODEX_BIN="$(git rev-parse --show-toplevel)/tools/codex-cli/node_modules/.bin/codex"
test -x "$CODEX_BIN" || { echo "NO-GO: bundled codex binary 不存在或不可執行"; exit 1; }
```

版本與 sha256 逐字記進 spike 文件。**不得用 grep 猜版本**。

- [ ] **Step 2: 寫 probe driver**

`cmd/probe-codex-parallel/main.go`——binary 路徑由旗標傳入，全部走 production API：

```go
var (
	codexBin = flag.String("codex-bin", "", "bundled codex binary 路徑（必填）")
	force    = flag.Bool("force", false, "強制收尾分支：turn 進行中直接 Terminate")
)

// 凍結參數
const (
	probeTimeout    = 90 * time.Second
	turnTimeout     = 60 * time.Second
	approvalPolicy  = "untrusted" // 凍結：與 production 預設一致，確保會觸發核可
	promptA         = "請只回覆字串 PROBE_A_DONE，不要使用任何工具。"
	promptB         = "請只回覆字串 PROBE_B_DONE，不要使用任何工具。"
	// 第三個 turn 刻意觸發核可——(b) 的 approval 歸屬必須有實際 frame 才能驗
	promptApproval  = "請在目前工作目錄建立檔案 probe-approval.txt，內容為 PROBE。"
)

func main() {
	flag.Parse()
	if *codexBin == "" {
		fatal("必須以 -codex-bin 指定 bundled binary")
	}
	tmp, err := os.MkdirTemp("", "probe-codex-*")
	must(err)
	defer os.RemoveAll(tmp)

	wireLog, err := os.Create(filepath.Join(tmp, "wire.jsonl"))
	must(err)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	srv, err := codex.StartAppServer(ctx, codex.Config{
		Binary: *codexBin, CWD: tmp, TermGrace: 5 * time.Second,
	})
	must(err)
	must(srv.BeginRecording(func(b []byte) error { // 全程錄流即證據
		_, werr := fmt.Fprintf(wireLog, "%s\n", b)
		return werr
	}))
	must(srv.Handshake(ctx, codex.ClientInfo{Name: "probe-codex-parallel"}))

	// (b) 受控 approval turn **先跑**——server 必須仍活著才能開 thread。
	//     -force 會在 (a) 途中終止 server，故此項一律排在並行段之前。
	appr := runTurnDenyingApprovals(ctx, srv, mustStartThread(ctx, srv),
		promptApproval, turnTimeout, approvalPolicy)
	if _, err := os.Stat(filepath.Join(tmp, "probe-approval.txt")); err == nil {
		fatal("NO-GO: 核可被拒仍寫入檔案")
	}

	// (a) 兩 thread 並行送 turn
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
	if *force { // 強制收尾：不等 turn 完成（只影響 (a) 與 (c)，approval 已驗完）
		srv.Terminate()
	}
	wg.Wait()

	report(res, appr, wireLog.Name())

	// (c) 收尾：Terminate → Wait → StopRecording → Close
	_ = srv.Terminate()
	exit := srv.Wait()
	_ = srv.StopRecording()
	must(wireLog.Close())
	fmt.Printf("exit_code=%d stderr_tail=%q\n", exit.Code, exit.StderrTail)
}
```

**兩次執行的判定聚合（凍結）**：`-force` 那次仍會跑 approval turn（它排在終止之前），但若因終止時序導致該項無結論，**以 natural run 的 (b) 結果為準**；(a) 以 natural run 為準、(c) 需**兩次都通過**。最終 GO 是兩次結果的聚合，spike 文件必須分別記錄。

- [ ] **Step 3: 執行兩種收尾並蒐證**

```bash
CODEX_BIN="$(git rev-parse --show-toplevel)/tools/codex-cli/node_modules/.bin/codex"
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN"        2>&1 | tee /tmp/probe-natural.log
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN" -force 2>&1 | tee /tmp/probe-forced.log
```

**GO 條件（全部成立才 GO）：**
- **(a)** 兩 turn 的 wire frame 時間上交錯（非 A 全部完成才出現 B）。被串行化或第二 thread 被拒 → **NO-GO**。
- **(b)** notification **與 approval request** 都帶 `threadID` 或 `turnID`，不靠抵達順序即可歸屬。**未觀察到任何 approval request，或 request 缺 thread/turn identity → NO-GO**（不得因「兩個 prompt 都不用工具」而略過此項——第三個 turn 就是為此設計）。
- **(c)** natural 與 forced 兩種收尾都 bounded 收斂，wire log 錄到最後一筆 frame。

- [ ] **Step 4: 寫 spike 記錄**

`docs/spikes/m3b-codex-parallel.md`：CLI 版本＋sha256、完整凍結參數、兩次執行的 wire 節錄（交錯證據、approval request 原文）、(a)(b)(c) 逐項判定與理由、GO/NO-GO。**如實記錄失敗**。

- [ ] **Step 5: Commit**

```bash
go vet ./... && go test -race ./... -count=1
git add cmd/probe-codex-parallel docs/spikes/m3b-codex-parallel.md
git commit -m "docs(spike): M3b Codex 多 thread 並行 live probe driver＋GO/NO-GO 判定（§7.2）"
```

---

## Phase 1 — 基礎型別與建立交易（全程 additive）

### Task 1: `contract.Envelope` 新增 `workspace_session_id`

**Files:** Modify `internal/contract/envelope.go:71-96`；Test `internal/contract/envelope_test.go`

**Interfaces:** Produces `Envelope.WorkspaceSessionID string \`json:"workspace_session_id,omitempty"\``。

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

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/contract/ -run TestEnvelopeCarries -v` → FAIL

- [ ] **Step 3: 加欄位**

```go
	// M3b 新增（additive；§3.1.5）：host-side 穩定 session identity。
	// Conversation lane 的每個 Envelope 自 BeginSubmit 起必填；workspace lane
	// 的 Gate／SpecAssist／PlanAssist one-shot 維持空值、不計入 slot（§3.1.6）。
	WorkspaceSessionID string `json:"workspace_session_id,omitempty"`
```

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(contract): Envelope 新增 workspace_session_id（additive，§3.1.5）"`

### Task 2: `internal/wsregistry` durable metadata store

**Files:** Create `internal/wsregistry/store.go`＋`store_test.go`

**Interfaces:**
- Produces:
```go
type Entry struct {
	WSID, Provider, ResumeSessionID, TaskLabel, ViewStartEventID string
	CreatedAt, RemovedAt, RemoveReason                           string
}
type Layout struct{ Pins []string; Focused string }
type Store struct{ /* mu、path、file */ }

func Open(path string) (*Store, error)
func (s *Store) Put(e Entry) error
func (s *Store) Remove(wsid, reason string) error    // 使用者移除：留 tombstone
func (s *Store) DeleteUncommitted(wsid string) error // 建立失敗回滾：整筆刪除
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
	if err := s.DeleteUncommitted("w2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("DeleteUncommitted 必須整筆刪除（建立失敗不得永久留痕）")
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

- [ ] **Step 3: 實作** — 沿 `restore.go:68-78` 的 temp file＋atomic rename＋0600；persist 失敗回滾記憶體（同 `restore.go:99-103`）。白名單靠型別強制：`Entry` 不含任何 runtime state 欄位。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(wsregistry): durable metadata store＋tombstone／DeleteUncommitted 分離（§3.2.1／§3.6.1）"`

### Task 3: Manager per-WSID slot registry（additive，保留舊入口）

**Files:** Modify `internal/appcore/manager.go:95-360`；Test `internal/appcore/manager_wsid_test.go`

**Interfaces:**
- Consumes: `contract.NewULID(t time.Time)`（`internal/contract/envelope.go:19`——**appcore 沒有 `newULID`，一律用這支**）。
- Produces（**新舊入口並存**）：
```go
type WSID string
type CreateToken struct{ wsid WSID; seq uint64 }
const MaxSessionsPerProvider = 4
var (
	ErrSessionLimit     = errors.New("appcore: session slot limit reached")
	ErrSessionNotFound  = errors.New("appcore: unknown workspace session")
	ErrStaleCreate      = errors.New("appcore: stale create token")
	ErrProviderMismatch = errors.New("appcore: event provider != slot provider")
)
func (m *Manager) ReserveSession(p contract.Provider) (WSID, CreateToken, error)
func (m *Manager) CommitCreate(tok CreateToken) error
func (m *Manager) AbortCreate(tok CreateToken) error
func (m *Manager) RestoreDormant(w WSID, p contract.Provider) error
func (m *Manager) SlotCount(p contract.Provider) int
func (m *Manager) ProviderOf(w WSID) (contract.Provider, bool)
func (m *Manager) IsActiveWS(w WSID) bool
// 既有每個 provider-keyed 方法都新增對應的 `...WS(w WSID, …)` 版本
// 相容入口（Task 9 刪除）：舊 provider-keyed 簽名不變，內部解析 legacy slot
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
		t.Fatalf("reservation 當下即應計入名額：%d", got)
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

func TestWSIDIsULID(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	w, _, _ := m.ReserveSession("claude")
	if len(string(w)) != 26 { // contract.NewULID 產生 26 字元 ULID
		t.Fatalf("WSID 必須是 contract.NewULID 產生的 ULID：%q", w)
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

func TestLegacyProviderEntryStillWorks(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	if _, err := m.BeginNewSessionSubmit("claude", "t"); err != nil {
		t.Fatalf("舊入口在遷移期間必須可用：%v", err)
	}
	if got := m.SlotCount("claude"); got != 0 {
		t.Fatalf("legacy slot 不得佔使用者名額：%d", got)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/appcore/ -race -v` → FAIL

- [ ] **Step 3: 實作**

`slots map[WSID]*slot`；`slot` 增 `provider`＋`committed`；新增 `legacy map[contract.Provider]WSID`、`reserveSeq uint64`。

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
	w := WSID(contract.NewULID(time.Now())) // appcore 無 newULID，一律用 contract
	sl := newSlot()
	sl.provider = p
	m.slots[w] = sl // reservation 當下即佔名額
	m.reserveSeq++
	return w, CreateToken{wsid: w, seq: m.reserveSeq}, nil
}

func (m *Manager) committedSlotLocked(w WSID) (*slot, error) {
	sl, ok := m.slots[w]
	if !ok || !sl.committed {
		return nil, ErrSessionNotFound
	}
	return sl, nil
}

// legacyWSIDLocked：相容入口專用——沿用現行「讀取時隱式建立」行為。
// Task 9 連同全部舊簽名一併刪除。legacy slot 不計入 countLocked。
func (m *Manager) legacyWSIDLocked(p contract.Provider) WSID {
	if w, ok := m.legacy[p]; ok {
		return w
	}
	w := WSID("legacy-" + string(p))
	sl := newSlot()
	sl.provider, sl.committed, sl.isLegacy = p, true, true
	m.slots[w], m.legacy[p] = sl, w
	return w
}
```

- [ ] **Step 4: 全綠（既有 Manager 測試一字不改也要綠）** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(appcore): per-WSID slot registry＋三段建立交易（additive，舊入口暫留，§3.1）"`

### Task 4: App 建立交易＋create-degraded＋`CreateSession` binding

**Files:** Modify `app.go:47-107`／`230-240`；Test `app_wsid_test.go`、`frontend/src/lib/bindings.test.ts`

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
func (a *App) CreateSession(provider, taskLabel string) (string, error) // 純新增 binding
func (a *App) createDegraded(p contract.Provider) bool
```

- [ ] **Step 1: 失敗測試（Go）**

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

func TestCommitAndRollbackBothFailEnterDegraded(t *testing.T) {
	a := newTestApp(t)
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
	if a.createDegraded("codex") {
		t.Fatal("degraded 應 per-provider")
	}
	// 既有 session 不受影響：走 legacy 入口的既有路徑仍可送訊息
	if err := a.SendMessage("claude", "still works"); err != nil {
		t.Fatalf("degraded 不得影響既有 session：%v", err)
	}
}
```

- [ ] **Step 2: 失敗測試（binding 轉發）**

```ts
it('CreateSession 逐參數轉發', () => {
  const b = makeBindings()
  b.CreateSession('claude', 'my-task')
  expect(App.CreateSession).toHaveBeenCalledWith('claude', 'my-task')
})
```

- [ ] **Step 3: 跑測試確認失敗** — `go test . -run 'TestCreateSession|TestCommit' -race -v && npm --prefix frontend run test -- bindings` → FAIL

- [ ] **Step 4: 實作**

```go
var errCreateDegraded = errors.New("app: session create degraded（需重啟 app 復原）")

func (a *App) CreateSession(provider, taskLabel string) (string, error) {
	if err := a.beginAppTxn(); err != nil { // shutdown 柵欄
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
			a.setCreateDegraded(p) // 雙失敗：保留名額、latch，等 app restart（§3.1）
			return "", errors.Join(cerr, rerr, errCreateDegraded)
		}
		return "", errors.Join(cerr, a.manager.AbortCreate(tok))
	}
	return string(w), nil
}
```

同 task 完成 binding 四件事：`wails generate module` → `bindings.ts` → `types.ts` → 測試。

- [ ] **Step 5: 全綠** — `go vet ./... && go test -race ./... -count=1 && npm --prefix frontend run test && npm --prefix frontend run build` → PASS

- [ ] **Step 6: Commit** — `git commit -m "feat(app): 建立交易＋create-degraded 降級＋CreateSession binding（§3.1）"`

### Task 5: legacy `restore.json` 遷移（一次性、冪等）

**Files:** Create `internal/wsregistry/migrate.go`＋`migrate_test.go`

**Interfaces:** Produces `type LegacyEntry struct{ ViewStartEventID, ResumeSessionID, TaskID string }`；`func Migrate(s *Store, legacy map[string]LegacyEntry, newWSID func() string) ([]Entry, error)`。

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
	got2, _ := Migrate(s2, legacy, gen)
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
		t.Fatal("migration persist 失敗必須 fail loud")
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
	if again, _ := Migrate(s2, legacy, func() string { return "w2" }); len(again) != 0 {
		t.Fatalf("legacy 移除後不得再次遷入（§3.6.1）：%+v", again)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wsregistry/ -run 'TestMigrate|TestRemovedLegacy' -v` → FAIL

- [ ] **Step 3: 實作** — 決定性順序走訪 `claude`／`codex`；空 entry 跳過；`MarkMigrated(out)` 一次原子寫 entries＋marker。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(wsregistry): legacy restore.json 一次性遷移＋migration marker（§3.2.5-6）"`

### Task 6: registry 載入／遷移＋dormant 還原

> **範圍界定**：本 task **只做** registry 載入／遷移／`RestoreDormant`。§3.2.4 完整序列的後半（replay index 驗證、incomplete-turn 修復、開放 UI／provider）依賴 `replayindex` 與 `sessionHosts`，移到 **Task 20**（index 接線完成後）。

**Files:** Modify `app.go:259-325`；Test `app_restore_dormant_test.go`

**Interfaces:**
- Consumes: Task 2 `Store`、Task 5 `Migrate`、Task 3 `RestoreDormant`／`IsActiveWS`／`SlotCount`。
- Produces: `func (a *App) loadSessionRegistry() ([]wsregistry.Entry, error)`。

- [ ] **Step 1: 失敗測試**

```go
func TestLoadRegistryRestoresAllDormant(t *testing.T) {
	a := newTestAppWithRegistry(t,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"},
		wsregistry.Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"})
	entries, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("應還原 2 個 dormant session：%d", len(entries))
	}
	for _, e := range entries {
		if a.manager.IsActiveWS(appcore.WSID(e.WSID)) {
			t.Fatalf("必須以 dormant 還原，不得為 active：%s", e.WSID)
		}
	}
	if len(a.apprPending) != 0 {
		t.Fatal("dormant 還原不得有 pending approval")
	}
	if a.manager.SlotCount("claude") != 1 || a.manager.SlotCount("codex") != 1 {
		t.Fatal("dormant 仍佔名額")
	}
}

func TestLoadRegistryTriggersMigrationOnce(t *testing.T) {
	dir := seedLegacyRestoreJSON(t) // 只有舊 restore.json，無 workspace-sessions.json
	a1 := newTestAppAt(t, dir)
	e1, err := a1.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(e1) != 1 {
		t.Fatalf("legacy 應遷出 1 個 session：%+v", e1)
	}
	a2 := newTestAppAt(t, dir)
	e2, _ := a2.loadSessionRegistry()
	if len(e2) != 1 || e2[0].WSID != e1[0].WSID {
		t.Fatalf("重啟不得產生第二枚 WSID：%+v vs %+v", e2, e1)
	}
}

func TestMigrationPersistFailureBlocksProviderStart(t *testing.T) {
	dir := seedLegacyRestoreJSON(t)
	a := newTestAppAt(t, dir)
	a.hookForceMigratePersistError = errors.New("disk full")
	if _, err := a.loadSessionRegistry(); err == nil {
		t.Fatal("migration persist 失敗必須 fail loud（§3.2.6），呼叫端據此不啟動 provider")
	}
	if a.providersStarted() {
		t.Fatal("migration 失敗後不得啟動 provider")
	}
}

func TestRemovedTombstoneNotRestored(t *testing.T) {
	a := newTestAppWithRegistry(t,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t", RemovedAt: "t2", RemoveReason: "user_removed"})
	entries, _ := a.loadSessionRegistry()
	if len(entries) != 0 {
		t.Fatalf("tombstone 不得還原：%+v", entries)
	}
	if a.manager.SlotCount("claude") != 0 {
		t.Fatal("removed 不計 slot")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestLoadRegistry|TestMigrationPersist|TestRemovedTombstoneNot' -race -v` → FAIL

- [ ] **Step 3: 實作**

```go
// loadSessionRegistry：§3.2.4 的前半段——載入／遷移 registry → 還原 dormant slots。
// 後半段（index 驗證／重建 → incomplete turn 修復 → 開放 UI 與 provider）見 Task 20。
func (a *App) loadSessionRegistry() ([]wsregistry.Entry, error) {
	store, err := wsregistry.Open(filepath.Join(a.stateDir, "workspace-sessions.json"))
	if err != nil {
		return nil, err
	}
	a.wsReg = store
	if !store.Migrated() {
		if _, err := wsregistry.Migrate(store, a.legacyEntries(),
			func() string { return contract.NewULID(time.Now()) }); err != nil {
			return nil, err // fail loud，不啟動 provider（§3.2.6）
		}
	}
	live := store.Live()
	for _, e := range live {
		if err := a.manager.RestoreDormant(appcore.WSID(e.WSID), contract.Provider(e.Provider)); err != nil {
			return nil, err
		}
	}
	return live, nil
}
```

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(app): registry 載入／遷移＋dormant 還原（§3.2.2／§3.2.5-6）"`

---

## Phase 2 — App ownership additive migration

> **遷移紀律**：Task 7 只**加** `sessionHosts`；Task 8 遷完 Claude 才刪 Claude 單例；Task 9 遷完 Codex 才刪其餘單例與 Manager 相容入口。**exported Wails binding 的簽名在本 phase 完全不變**（Task 9 保留 provider-keyed 包裝層，Task 26 才原子切換）。

### Task 7: `sessionHosts` registry（additive）

**Files:** Create `session_host.go`＋`session_host_test.go`；Modify `app.go:47-107`（**只加欄位**）

**Interfaces:**
- Produces: `type sessionHost struct{ wsid; provider; sess; sockPath; mcpPath; broker; pumpDone; teardownFn; lease; threadID; track; sessionID }`；`hostFor`／`putHost`／`dropHost`／`snapshotHosts`／`hostsOf`。

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

- [ ] **Step 3: 實作** — `App` **新增** `sessionHosts map[appcore.WSID]*sessionHost`（`a.mu` 保護）；既有單例欄位全部保留不動。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(app): 新增 sessionHosts registry（additive，§3.3）"`

### Task 8: Claude 遷入 host＋per-WSID socket／MCP＋刪 Claude 單例

**Files:** Modify `app.go:3998-4285`／`4011`／`4050`／`3758-3809`；Test `app_claude_multi_test.go`

**Interfaces:**
- Consumes: Task 7 `sessionHost`、Task 3 `...WS` 方法。
- Produces: `func (a *App) startClaude(w appcore.WSID, prompt, resume, recordCase string) (func(accepted bool), error)`；`pumpApprovals(h *sessionHost)`；socket `approval-<wsid>.sock`、mcp `mcp-<wsid>.json`。
- **刪除**：`App.broker`／`claudeSess`／`claudeSessionID`／`claudePumpDone`／`claudeLease`／`claudeTeardownFn`。
- **不變**：exported `StartSession(provider, …)`／`SendMessage(provider, …)`／`EndSession(provider)` 簽名——內部透過 `a.legacyWSIDFor(provider)` 解析（Task 26 刪除）。

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
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"broker", "claudeSess", "claudeSessionID",
		"claudePumpDone", "claudeLease", "claudeTeardownFn"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Claude 單例欄位 %s 應已刪除（§3.3）", name)
		}
	}
}

func TestExportedBindingSignatureUnchanged(t *testing.T) {
	// exported binding 在 Task 26 的原子切換之前不得改簽名，否則前端會中途壞掉
	a := newTestApp(t)
	mustStartClaude(t, a, mustCreate(t, a, "claude"))
	m, _ := reflect.TypeOf(&App{}).MethodByName("SendMessage")
	if got := m.Type.In(1).Kind(); got != reflect.String {
		t.Fatalf("SendMessage 第一參數型別不得改變：%v", got)
	}
	if err := a.SendMessage("claude", "hi"); err != nil {
		t.Fatalf("provider-keyed exported binding 必須仍可用：%v", err)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestTwoClaudeSessions|TestClaudeApproval|TestNoClaudeSingleton|TestExportedBinding' -race -v` → FAIL

- [ ] **Step 3: 實作** — 路徑帶 WSID；broker／pump／lease／teardown（`sync.OnceValue`）建到 host；`registerApproval` 記 WSID；Claude 分支改走 `hostFor(w)`＋`...WS`；刪六個單例欄位並修正引用。per-WSID 檔案的**移除清理**屬 remove 生命週期，測試在 Task 23。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(app): Claude 遷入 sessionHost＋per-WSID socket／MCP，刪除 Claude 單例（§3.3）"`

### Task 9: Codex dispatcher＋刪除 Manager 相容入口

**Files:** Modify `app.go:4240-4455`、`internal/appcore/pump.go:69-84`（`EndSessionFlow`）、`internal/appcore/manager.go`、`internal/appcore/manager_test.go`、`manager_workspace_test.go`；Test `app_codex_dispatch_test.go`

**Interfaces:**
- Produces:
```go
func (a *App) startCodex(w appcore.WSID, prompt, resume, recordCase, approvalPolicy string) (string, bool, error)
func (a *App) startCodexHost(w appcore.WSID, host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error)
func (a *App) hostByThread(threadID string) *sessionHost
func (a *App) hostByTurn(turnID string) *sessionHost
func appcore.EndSessionFlow(m *Manager, w WSID, busyCheck func() bool, teardown func() error) error // 改 WSID
```
- **刪除清單（必須逐項清乾淨）**：
  1. `App.runner`／`App.track`／`App.codexLease`／`App.currentRunner()`
  2. `Manager.legacyWSIDLocked` 與全部 provider-keyed 方法（`...WS` 同步改名為無後綴）
  3. `appcore.EndSessionFlow` 的 `p contract.Provider` 參數（`pump.go:69`）
  4. `app.go` 內約 24 處 provider-keyed Manager 呼叫（`Emit`×7、`RejectSubmit`×6、`AcceptSubmit`×4、`FinishReset`／`FinishEndSession`／`CancelEndSession`／`BeginSubmit`／`BeginReset`／`BeginNewSessionSubmit`／`BeginEndSession` 各 1）
  5. `internal/appcore/manager_test.go`（1254 行）與 `manager_workspace_test.go` 的全部呼叫點——**語意不得減少**，只改取得 WSID 的方式（`Reserve`＋`Commit`）
- **保留到 Task 26**：`a.legacyWSIDFor(provider)` 與 provider-keyed exported binding 包裝層。

- [ ] **Step 1: 失敗測試**

```go
func TestCodexTwoThreadsDoNotCrossWire(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	th1, th2 := mustStartCodex(t, a, w1), mustStartCodex(t, a, w2)
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
// （Task 0 的 live probe 不判定此項）
func TestCompletedBeforeResponseOnProductionPath(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	w := mustCreate(t, a, "codex")
	a.hookInServerTxn = func() { a.fakeWire.PushCompletedForPendingStart() }
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
	if err := a.dispatchNotification("unknown-thread", []byte(`{"type":"delta"}`)); err == nil {
		t.Fatal("無法歸屬必須 fail loud，不得落到『當前』session")
	}
}

func TestNoSingletonsAndNoShimRemain(t *testing.T) {
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"runner", "track", "codexLease"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Codex 單例欄位 %s 應已刪除", name)
		}
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . ./internal/appcore/ -run 'TestCodexTwoThreads|TestCompletedBefore|TestUnattributable|TestNoSingletons' -race -v` → FAIL

- [ ] **Step 3: 實作 dispatcher** — `a.mu` 下維護 `threadToWSID`／`turnToWSID`／`pendingStartToWSID`（key 為 request id）。`startCodexHost` 送 `thread/start` 前先登記 pending，response 抵達補 `threadToWSID`。查找順序 `turnToWSID` → `threadToWSID` → `pendingStartToWSID`，查不到即 fail loud。

- [ ] **Step 4: 逐項遷移刪除清單並確認無殘留**

```bash
grep -rn "currentRunner\|legacyWSIDLocked" --include='*.go' . && echo "殘留" || echo OK
grep -rn "EndSessionFlow(.*contract.Provider" --include='*.go' . && echo "殘留" || echo OK
grep -rn "\.\(BeginSubmit\|BeginEndSession\|AcceptSubmit\|RejectSubmit\|Emit\)WS(" --include='*.go' . \
  && echo "WS 後綴未改名" || echo OK
```

- [ ] **Step 5: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 6: Commit** — `git commit -m "feat(app): Codex WSID dispatcher＋刪除 currentRunner／Manager 相容入口／EndSessionFlow WSID 化（§3.3）"`

---

## Phase 3 — Codex connection-wide wire log

### Task 10: `internal/wirelog` generation＋frame index

**Files:** Create `internal/wirelog/wirelog.go`／`frameindex.go`＋測試

**Interfaces:** Produces `Direction`／`SegmentRef`／`FrameKey`／`Generation`（`NewGeneration`／`ID`／`Line`／`Attribute`／`Finalize`／`Finalized`／`FinalMeta`／`Err`／`FrameIndex`）／`RebuildFrameIndex`。

> **後續更正（2026-08-18，frame-attribution 票；本節其餘內容維持歷史原貌不改寫）**：
> `Generation.Attribute`（事後、逐 `FrameKey`、僅記憶體）**已刪除**，改為 write-time 的
> `wirelog.WSIDResolver`——歸屬逐 frame 判定並寫進該 frame 那一行的 `wsid` 欄位，
> `RebuildFrameIndex` 因此連歸屬一起重建。`NewGeneration` 的簽名隨之變成三參數
> （`NewGeneration(dir, id, resolve)`），本節底下的範例仍是兩參數的舊寫法。
> 理由與 merge gate 見 `.superpowers/sdd/2026-08-14-m3b-multi-session/frame-attribution-report.md`。

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

- [ ] **Step 3: 實作** — JSONL，每行 `{frame, dir, wsid, raw}`；frame 單調編號。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(wirelog): connection-wide wire log generation＋可重建 frame index（§3.4.1-5）"`

### Task 11: `[]SegmentRef` 跨 generation 歸屬

**Files:** Modify `internal/wirelog/wirelog.go`；Test `segments_test.go`

**Interfaces:** Produces `SegmentSet`／`NewSegmentSet`／`Append`／`For`。

- [ ] **Step 1: 失敗測試**

```go
func TestSegmentsSpanTwoGenerations(t *testing.T) {
	set := NewSegmentSet()
	set.Append("w1", SegmentRef{WireLogID: "g1", FirstFrame: 1, LastFrame: 10})
	set.Append("w2", SegmentRef{WireLogID: "g1", FirstFrame: 11, LastFrame: 20})
	set.Append("w1", SegmentRef{WireLogID: "g2", FirstFrame: 1, LastFrame: 5})
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
		t.Fatal("For 必須回傳副本")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/wirelog/ -run TestSegments -v` → FAIL

- [ ] **Step 3: 實作** — `map[string][]SegmentRef`＋mutex。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(wirelog): []SegmentRef 跨 generation 有序歸屬（§3.4.4）"`

### Task 12: `GenerationOwner`＋死亡自動 finalize（新增入口，不動舊 probe）

**Files:** Create `internal/codex/owner.go`；Modify `internal/codex/single.go`（**只加方法**）；Test `internal/codex/owner_test.go`

> **additive 紀律**：現行 `RunHandshakeProbe`（probe-scoped）與 `App.codexSingle codex.Single[*codex.Server]`（`app.go:78`）**本 task 完全不動**——否則 `RestartCodexServerRecorded` 會編譯失敗。本 task 只新增 owner 版入口與 `Single` 的兩個方法；App 的 ownership 型別遷移與舊入口刪除在 **Task 13**。

**Interfaces:**
- Produces:
```go
type GenerationOwner struct {
	Server     probeTarget
	Generation *wirelog.Generation
}
func (o *GenerationOwner) Done() <-chan struct{} { return o.Server.Done() } // 滿足 Alive
func (o *GenerationOwner) FinalizeWith(stage error) error                   // 冪等
func (o *GenerationOwner) Finalized() bool

// Single 新增（additive；不改 `T Alive` 約束——泛型值不可直接用 == 比較，
// 故以單調 epoch 辨識持有者，避免加上 comparable 約束而牽動既有實例化）：
func (s *Single[T]) Epoch() uint64                    // 目前持有者的 epoch（無持有者=0）
func (s *Single[T]) CompareAndTakeEpoch(e uint64) (T, bool) // 僅當 epoch 相符才取出

// WithExclusiveEpoch：與 WithExclusive 同語意，但**在同一把鎖內**回傳本次發布
// 取得的 epoch。必須用它取 epoch——解鎖後才呼叫 Epoch() 有 TOCTOU：期間若已有
// 第二次 replacement 發布，第一個 watcher 會拿到第二個 owner 的 epoch，
// 之後在舊 server 死亡時錯誤取走新 generation。keep=false 時回 epoch=0。
func (s *Single[T]) WithExclusiveEpoch(fn func(cur T, ok bool) (T, bool, error)) (uint64, error)

// production 死亡收尾：發布 owner 後立即啟動。Done() 關閉 → CompareAndTakeEpoch
// → FinalizeWith。**無論是否仍為 active 持有者都會 finalize 自己的 generation，
// 且一律呼叫 onFinalized**（wasActive 標示是否仍是當時的持有者）——測試才能在
// stale 分支等到 callback 而不卡住。
func WatchGeneration(s *Single[*GenerationOwner], o *GenerationOwner, epoch uint64,
	onFinalized func(err error, wasActive bool))

// owner 版入口（新增；handoff 語意內建）。舊的 RunHandshakeProbe 維持原簽名
// 與 probe-scoped 行為，Task 13 遷完 App 後才刪。
func RunOwnedHandshake(ctx context.Context, single *Single[*GenerationOwner],
	newGen func() (*wirelog.Generation, error), start func() (probeTarget, error),
	ci ClientInfo, onFinalized func(err error, wasActive bool)) error
```

- [ ] **Step 1: 失敗測試——production 路徑，測試不得手動 finalize**

```go
func TestServerDeathAutoFinalizesGeneration(t *testing.T) {
	stub := newStubServer()
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	finalized := make(chan bool, 1) // wasActive
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{},
		func(_ error, wasActive bool) { finalized <- wasActive }); err != nil {
		t.Fatal(err)
	}
	stub.die() // 只做這件事——不得由測試呼叫 FinalizeWith
	select {
	case wasActive := <-finalized:
		if !wasActive {
			t.Fatal("死亡時仍是 active 持有者，wasActive 應為 true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done() 關閉後必須由 production reaper 自動 finalize（§3.4.2）")
	}
	if !gen.Finalized() {
		t.Fatal("generation 未 finalize，錄流 meta 會漏")
	}
	if _, ok := single.Take(); ok {
		t.Fatal("死亡的 owner 必須已從 Single 移除")
	}
}

func TestReplacementWaitsForOldGenerationFinalize(t *testing.T) {
	old := newStubServer()
	var single Single[*GenerationOwner]
	oldGen := newTestGeneration(t)
	_ = RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return oldGen, nil },
		func() (probeTarget, error) { return old, nil }, ClientInfo{}, func(error, bool) {})

	var order []string
	newGen := newTestGeneration(t)
	newGen.OnCreateForTest(func() { order = append(order, "new_gen_created") })
	oldGen.OnFinalizeForTest(func() { order = append(order, "old_finalized") })

	_ = RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newGen, nil },
		func() (probeTarget, error) { return newStubServer(), nil }, ClientInfo{}, func(error, bool) {})

	if len(order) < 2 || order[0] != "old_finalized" {
		t.Fatalf("replacement 必須等舊 generation finalize 完才發布：%v", order)
	}
}

func TestStaleReaperDoesNotClearNewGeneration(t *testing.T) {
	oldSrv := newStubServer()
	var single Single[*GenerationOwner]
	oldGen := newTestGeneration(t)
	staleDone := make(chan bool, 1) // wasActive
	_ = RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return oldGen, nil },
		func() (probeTarget, error) { return oldSrv, nil }, ClientInfo{},
		func(_ error, wasActive bool) { staleDone <- wasActive })

	// 先以 replacement 換掉 owner（epoch 遞增），之後才讓舊 server 死亡
	newSrv, newGen := newStubServer(), newTestGeneration(t)
	_ = RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newGen, nil },
		func() (probeTarget, error) { return newSrv, nil }, ClientInfo{}, func(error, bool) {})

	oldSrv.die()
	select { // stale 分支同樣會呼叫 onFinalized，測試不會卡住
	case wasActive := <-staleDone:
		if wasActive {
			t.Fatal("已被 replacement 取代，wasActive 應為 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale reaper 也必須呼叫 onFinalized")
	}

	o, ok := single.Take()
	if !ok || o.Generation != newGen {
		t.Fatal("stale reaper 不得清掉新 generation（CompareAndTakeEpoch 必須回 false）")
	}
	if newGen.Finalized() {
		t.Fatal("新 generation 不得被舊 reaper finalize")
	}
}

// epoch 必須在鎖內取得：第一個 owner 發布後、watcher 註冊前停住，
// 讓第二次 replacement 完成，再放行第一個 watcher。
func TestWatcherEpochCapturedUnderLock(t *testing.T) {
	var single Single[*GenerationOwner]
	firstAtBarrier := make(chan struct{})
	releaseFirst := make(chan struct{})
	// 不可用 sync.Once：Once.Do 在第一次 callback 未返回前會**阻塞**第二次呼叫，
	// 於是第二個 replacement 也卡住，而 releaseFirst 又要等它返回才關 → 死鎖。
	var calls atomic.Int32
	hookAfterPublishBeforeWatch = func() { // 套件層測試 hook
		if calls.Add(1) == 1 { // 只有第一次停在 barrier，後續呼叫直接通過
			close(firstAtBarrier)
			<-releaseFirst
		}
	}
	t.Cleanup(func() { hookAfterPublishBeforeWatch = nil })

	oldSrv, oldGen := newStubServer(), newTestGeneration(t)
	staleDone := make(chan bool, 1)
	go func() {
		_ = RunOwnedHandshake(context.Background(), &single,
			func() (*wirelog.Generation, error) { return oldGen, nil },
			func() (probeTarget, error) { return oldSrv, nil }, ClientInfo{},
			func(_ error, wasActive bool) { staleDone <- wasActive })
	}()
	<-firstAtBarrier

	// 第二次 replacement 在第一個 watcher 註冊前就完成發布
	newSrv, newGen := newStubServer(), newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newGen, nil },
		func() (probeTarget, error) { return newSrv, nil }, ClientInfo{},
		func(error, bool) {}); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)

	oldSrv.die()
	select {
	case wasActive := <-staleDone:
		if wasActive {
			t.Fatal("第一個 watcher 必須帶自己那次發布的 epoch，wasActive 應為 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale watcher 未回報")
	}
	o, ok := single.Take()
	if !ok || o.Generation != newGen {
		t.Fatal("舊 watcher 不得取走新 owner（epoch 必須在鎖內取得）")
	}
	if newGen.Finalized() {
		t.Fatal("新 generation 不得被舊 watcher finalize")
	}
}

func TestOldProbeEntryPointUnchanged(t *testing.T) {
	// 舊 probe-scoped 入口在 Task 13 之前必須維持原簽名與原行為，
	// 否則 App.codexSingle（Single[*codex.Server]）與 B1 呼叫點會編譯失敗
	stub := newStubServer()
	var single Single[*stubServer]
	if err := RunHandshakeProbe(context.Background(), &single,
		func() (*recorder.Recorder, error) { return newTestRecorder(t), nil },
		func() (*stubServer, error) { return stub, nil }, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	if stub.stopRecordingCalls != 1 {
		t.Fatalf("舊入口必須維持 probe-scoped（成功時 StopRecording）：%d", stub.stopRecordingCalls)
	}
}

func TestThreeStageFailuresDisposeAndKeepEvidence(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*stubServer)
	}{
		{"start", func(s *stubServer) { s.startErr = errors.New("spawn failed") }},
		{"attach", func(s *stubServer) { s.beginRecordingErr = errors.New("attach failed") }},
		{"handshake", func(s *stubServer) { s.handshakeErr = errors.New("handshake refused") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubServer()
			stub.exit = proc.Exit{Code: 3, StderrTail: "boom"}
			c.setup(stub)
			var single Single[*GenerationOwner]
			gen := newTestGeneration(t)
			err := RunOwnedHandshake(context.Background(), &single,
				func() (*wirelog.Generation, error) { return gen, nil },
				func() (probeTarget, error) {
					if stub.startErr != nil {
						return nil, stub.startErr
					}
					return stub, nil
				}, ClientInfo{}, func(error, bool) {})
			if err == nil {
				t.Fatal("失敗階段必須回錯")
			}
			if gen.ID() == "" || !gen.Finalized() {
				t.Fatal("失敗的 generation 仍須保留 wire_log_id 並 finalize")
			}
			if c.name != "start" {
				m := gen.FinalMeta()
				if m.ExitCode == nil || *m.ExitCode != 3 || !strings.Contains(m.StderrTail, "boom") {
					t.Fatalf("必須保留收尾證據：%+v", m)
				}
			}
			if _, ok := single.Take(); ok {
				t.Fatal("不得留下未發布 server")
			}
		})
	}
}

func TestHandoffKeepsRecorderOpenAndIDBeforeAttach(t *testing.T) {
	stub := newStubServer()
	var order []string
	stub.onBeginRecording = func() { order = append(order, "attach") }
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { order = append(order, "gen_id"); return gen, nil },
		func() (probeTarget, error) { order = append(order, "start"); return stub, nil },
		ClientInfo{}, func(error, bool) {}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"gen_id", "start", "attach"}) {
		t.Fatalf("wire_log_id 必須在掛 recorder 前配置：%v", order)
	}
	if stub.stopRecordingCalls != 0 || gen.Finalized() {
		t.Fatal("handoff 模式不得 Stop／Finalize（§3.4.7）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/codex/ -race -v` → FAIL

- [ ] **Step 3: 實作**

`Single` 新增 `epoch uint64`，每次成功發布（`WithExclusive` 回 `keep=true` 或 `Ensure` 建立）時遞增；`CompareAndTakeEpoch(e)` 在鎖內比對 `s.epoch == e && s.ok` 才取出。**不改 `T Alive` 約束**——泛型值不能直接 `==`，加 `comparable` 會牽動既有 `Single[*Server]`／`Single[*stubAlive]` 實例化。

```go
func WatchGeneration(s *Single[*GenerationOwner], o *GenerationOwner, epoch uint64,
	onFinalized func(err error, wasActive bool)) {
	go func() {
		<-o.Done()
		_, wasActive := s.CompareAndTakeEpoch(epoch) // false＝已被 replacement 取代
		err := o.FinalizeWith(errServerDied)         // 兩種情況都要 finalize 自己的 generation
		if onFinalized != nil {
			onFinalized(err, wasActive) // 一律呼叫——stale 分支也要，否則呼叫端會等不到
		}
	}()
}
```

`RunOwnedHandshake` 用 **`WithExclusiveEpoch`**：鎖內先 `cur.FinalizeWith(nil)` 收舊 owner → 建新 generation（`wire_log_id` 前置配置）→ start → attach → handshake → 發布，**由該呼叫直接回傳本次 epoch**，再以此 epoch 啟動 `WatchGeneration`（`hookAfterPublishBeforeWatch` 為兩者之間的測試 barrier）。**絕不可解鎖後再呼叫 `Epoch()`**——那會拿到後續 replacement 的 epoch。**舊 `RunHandshakeProbe` 一行不動。**

- [ ] **Step 4: 全綠＋競態穩定** — `go vet ./... && go test -race ./... -count=1` 並 `go test ./internal/codex/ -race -count=30` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(codex): GenerationOwner＋死亡自動 finalize reaper＋epoch CompareAndTake 防 stale（additive，§3.4.2／§3.4.7）"`

### Task 13: recorder error latch＋in-process 受控復原

**Files:** Modify `app.go:78`（`codexSingle` 型別）、`app.go:4456-4505`（`RestartCodexServerRecorded`）、`internal/codex/probe.go`（刪舊入口）；Test `app_wirelog_latch_test.go`

**Interfaces:**
- Consumes: Task 12 `GenerationOwner`／`RunOwnedHandshake`／`WatchGeneration`。
- Produces: `wireLatched`／`latchWireRecorder`／`RecoverCodexRecording`。
- **ownership 型別遷移（本 task 完成）**：`App.codexSingle` 由 `codex.Single[*codex.Server]` 改為 `codex.Single[*codex.GenerationOwner]`；`RestartCodexServerRecorded` 改走 `RunOwnedHandshake`；**遷完才刪** `codex.RunHandshakeProbe` 舊入口與其 probe-scoped 測試（語意由 owner 版測試接手）。
- **鎖層次（凍結）**：`RunOwnedHandshake` **內部自己持有 `WithExclusiveEpoch`**，App 呼叫端**不得**再包一層 `a.codexSingle.WithExclusive(...)`——巢狀會直接死鎖（`single.go:54-56` 同一把 mutex）。§3.4.7「全段在單一互斥交易內完成」指的就是 `RunOwnedHandshake` 這一層，不是要呼叫端另外加鎖。
- `a.codexConn` 的取得改為 `owner.Server`；既有 interrupt／fake-wire 路徑同步。

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
	if _, ok := a.codexSingle.Take(); ok {
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
	if n != 1 {
		t.Fatalf("每 generation 只發一次通知：%d", n)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestLatch|TestRecovery' -race -v` → FAIL

- [ ] **Step 3: 遷移 ownership 型別** — `App.codexSingle` 改 `codex.Single[*codex.GenerationOwner]`；`RestartCodexServerRecorded` 與 `RecoverCodexRecording` 共用 `RunOwnedHandshake`——**全段由 `RunOwnedHandshake` 內部的 `WithExclusiveEpoch` 單層互斥交易保護，App 呼叫端不得另行包鎖**（見上方「鎖層次（凍結）」）；latch 以 per-generation `sync.Once` 單次通知；全部成功才解除。

- [ ] **Step 4: 刪除舊入口並確認無殘留**

```bash
grep -rn "RunHandshakeProbe\b" --include='*.go' . && echo "舊入口殘留" || echo OK
grep -rn "Single\[\*codex.Server\]\|Single\[\*Server\]" --include='*.go' . && echo "舊 ownership 型別殘留" || echo OK
```

- [ ] **Step 5: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 6: Commit** — `git commit -m "feat(app): recorder error latch＋in-process 受控復原＋codexSingle 遷移為 GenerationOwner（§3.4.6-7）"`

---

## Phase 4 — Replay index

### Task 14: `AuditSink.Write` 回傳 `AppendReceipt`

**Files:** Modify `internal/appcore/sink.go:14-40`／`manager.go:500-510`／`app.go:282-287`／`351-354`；Test `sink_test.go`

**Interfaces:** Produces `AppendReceipt{StartOffset, EndOffset int64; EventID string}`；`AuditSink.Write(env) (AppendReceipt, error)`。

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
}

func TestSinkReopenContinuesOffsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	s1, _ := NewJSONLSink(p)
	r1, _ := s1.Write(contract.Envelope{EventID: "e1", Kind: "message"})
	_ = s1.Close()
	s2, _ := NewJSONLSink(p)
	r2, _ := s2.Write(contract.Envelope{EventID: "e2", Kind: "message"})
	if r2.StartOffset != r1.EndOffset {
		t.Fatalf("重開後 offset 未接續：%+v %+v", r1, r2)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/appcore/ -run TestJSONLSink -v` → FAIL

- [ ] **Step 3: 實作** — 開檔時 `f.Seek(0, io.SeekEnd)` 取初始 offset；`failedSink.Write` 回 `AppendReceipt{}, s.reason`。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(appcore): AuditSink.Write 回傳 AppendReceipt（§3.5.2）"`

### Task 15: turn index＋turn boundary

**Files:** Create `internal/replayindex/index.go`＋測試

**Interfaces:** Produces `TurnRecord`／`Config{Notify}`／`Index`（`Open`／`OpenWith`／`Observe`／`RecentTurns`／`TurnsBefore`／`OpenTurnStart`／`Checkpoint`）。

- [ ] **Step 1: 失敗測試**

```go
func TestTurnBoundaryDefinition(t *testing.T) {
	i, _ := Open(t.TempDir())
	obs := func(kind, role, state, id string, off int64) {
		_ = i.Observe(contract.Envelope{EventID: id, Kind: kind, Role: role, State: state,
			WorkspaceSessionID: "w1"},
			appcore.AppendReceipt{StartOffset: off, EndOffset: off + 10, EventID: id})
	}
	obs("init", "system", "", "e1", 0)              // 無 canonical user message → 不成 turn
	obs("message", "user", "", "e2", 10)            // turn 起
	obs("delta", "assistant", "", "e3", 20)
	obs("result", "system", "", "e4", 30)
	obs("state_change", "system", "done", "e5", 40) // terminal：turn 止
	turns, _ := i.RecentTurns("w1", 10)
	if len(turns) != 1 {
		t.Fatalf("init 不得被猜成一個 turn：%d", len(turns))
	}
	if turns[0].StartOffset != 10 || turns[0].EndOffset != 50 ||
		turns[0].FirstEventID != "e2" || turns[0].LastEventID != "e5" {
		t.Fatalf("turn 範圍／首末 event ID 錯：%+v", turns[0])
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
			t.Fatalf("每 WSID 一個 index 檔：%v", err)
		}
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -v` → FAIL

- [ ] **Step 3: 實作** — `<dir>/checkpoint.json`（含每 WSID 的 `open_turn_start_offset`）＋`<dir>/<wsid>.turns.jsonl`；checkpoint atomic rename。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(replayindex): per-WSID turn index＋凍結 turn boundary（§3.5.1／§3.5.9）"`

### Task 16: degraded latch＋防遞迴通知

**Files:** Modify `internal/replayindex/index.go`；Test `degraded_test.go`

**Interfaces:** Produces `Degraded()`／`latchDegraded(err)`；`Config.Notify` 生效。（排在損壞分級之前，因為 quarantine 的復原通知要用它。）

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
		_ = i.Observe(workspaceNotice("w1"), receipt(99)) // 通知本身也進 audit
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
		t.Fatal("必須先 latch、後通知（§3.5.4）")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/replayindex/ -run 'TestIndexFailure|TestNotification|TestLatchBefore' -race -v` → FAIL

- [ ] **Step 3: 實作** — 先 latch（writer 停寫）、後通知；`Observe` 在 degraded 時直接回 nil。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(replayindex): degraded latch＋每 generation 單次通知防遞迴（§3.5.4）"`

### Task 17: crash consistency 三態修復（啟動期）

**Files:** Create `internal/replayindex/rebuild.go`＋測試

**Interfaces:** Produces `func (i *Index) VerifyOrRebuild(auditPath string) error`。

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
	if off > auditSize(t, audit) || last == "e-nonexistent" {
		t.Fatalf("超前的不可信 checkpoint 未修復：%d %s", off, last)
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

- [ ] **Step 3: 實作** — 落後掃 suffix 補；超前／超界／event ID 不符交 Task 18 分級處置。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(replayindex): crash consistency 三態修復（§3.5.3／§3.5.5）"`

### Task 18: 損壞處置分級

**Files:** Create `internal/replayindex/corrupt.go`＋測試

**Interfaces:** Consumes Task 16 `Config.Notify`。Produces `inspect`／`quarantine`。

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
		t.Fatalf("尾端 corruption 應 truncate 續用：%d", len(turns))
	}
	if quarantineExists(t, dir) || notices != 0 {
		t.Fatal("尾端 corruption 不該 quarantine、不需通知")
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

- [ ] **Step 3: 實作** — 第一個壞行之後**仍有 valid 行**即判定中段。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(replayindex): 損壞分級——尾端 truncate、中段 quarantine＋復原通知（§3.5.6）"`

### Task 19: runtime 重建交接——收斂上限與原子接回

**Files:** Modify `internal/replayindex/rebuild.go`；Test `runtime_rebuild_test.go`

**Interfaces:**
- Produces:
```go
const (
	MaxCatchUpBytes    = 1 << 20
	MaxCatchUpRecords  = 512
	MaxCatchUpAttempts = 8
)
type auditEndFunc func() (int64, error)
func (i *Index) RuntimeRebuild(auditPath string, emitMu sync.Locker, auditEnd auditEndFunc) error
var ErrRebuildNotConverged = errors.New("replayindex: catch-up 未收斂，保留 degraded latch")
```
- **rebuild cursor 獨立於 checkpoint**：degraded 期間 checkpoint 依 §3.5.4 不前移，catch-up 續掃位置改用 in-memory `rebuildCursor`，只在成功接回時才寫進 checkpoint。

- [ ] **Step 1: 失敗測試——四條**

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

// (2) sustained-append (a)：鎖外 catch-up 始終無法達標
//     hook 掛在殘量檢查「之前」，並斷言從未進入取鎖階段
func TestRebuildNeverConvergesKeepsLatch(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, _ := Open(dir)
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var lockAcquired int
	i.hookAfterResidualOKBeforeLock = func() { lockAcquired++ }
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

// (3) sustained-append (b)：達標後取鎖時再次超限。
//     用 auditEnd 注入模擬「等待 emitMu 期間其他 goroutine 持續 append」——
//     production 中 append 必須持有同一把 emit mutex，鎖內 append 不可能發生。
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
		if i.HoldingLockForTest() && burst > 0 {
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

// (4) rebuild cursor 與 checkpoint 分離
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
			continue // 殘量仍超限：不取鎖
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
	return ErrRebuildNotConverged
}
```

`catchUpUnlocked` 從 `i.rebuildCursor` 續掃；去重靠 `(StartOffset, LastEventID)` 唯一鍵。

- [ ] **Step 4: 全綠＋競態穩定** — `go test ./internal/replayindex/ -race -count=30` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(replayindex): runtime 重建收斂上限＋原子接回＋兩種不收斂分支（§3.5.7）"`

### Task 20: index 接線＋rebuild 編排＋**啟動序列完整化**

**Files:** Modify `internal/appcore/manager.go:500-510`、`app.go:259-325`；Create `rebuild_orchestrator.go`；Test `app_replayindex_test.go`、`app_startup_repair_test.go`

**Interfaces:**
- Consumes: Task 6 `loadSessionRegistry`、Task 7 `sessionHosts`、Task 14 `AppendReceipt`、Task 15-19 `Index`。
- Produces:
```go
func (a *App) restoreSessions() ([]wsregistry.Entry, error) // §3.2.4 完整凍結序列
func (a *App) repairIncompleteTurns(entries []wsregistry.Entry) error
func (a *App) scheduleRebuild(reason string)
func (a *App) rebuildInFlight() bool
func (a *App) LoadTurnsBefore(wsid, beforeEventID string, n int) ([]contract.Envelope, error) // 純新增 binding
```

- [ ] **Step 1: 失敗測試（啟動序列）**

```go
func TestStartupOrderIsFrozen(t *testing.T) {
	a := newTestAppAt(t, seedInterruptedTurn(t))
	var order []string
	a.hookStartupStep = func(s string) { order = append(order, s) }
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	want := []string{"registry_load", "migrate", "restore_dormant",
		"index_verify", "detect_incomplete", "emit_stream_error", "open_ui"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("啟動修復順序不符 §3.2.4：\n got=%v\nwant=%v", order, want)
	}
}

func TestStartupRepairEmitsStreamErrorThenFailed(t *testing.T) {
	dir := seedInterruptedTurn(t) // w1 有 user message＋delta，無 terminal state_change
	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	ev := readEvents(t, dir)
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
	a2 := newTestAppAt(t, dir) // 模擬 crash 後重跑
	if _, err := a2.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if second := readEvents(t, dir); len(second) != len(first) {
		t.Fatalf("重跑必須冪等，事件數 %d → %d", len(first), len(second))
	}
}

func TestUIOpensOnlyAfterRepair(t *testing.T) {
	a := newTestAppAt(t, seedInterruptedTurn(t))
	var uiOpenedAt int
	var repairedAt int
	a.hookStartupStep = func(s string) {
		switch s {
		case "emit_stream_error":
			repairedAt = len(readEvents(t, a.stateDir))
		case "open_ui":
			uiOpenedAt = len(readEvents(t, a.stateDir))
		}
	}
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if uiOpenedAt < repairedAt {
		t.Fatal("必須修復完才開放 UI 與 provider 啟動（§3.2.4）")
	}
}
```

- [ ] **Step 2: 失敗測試（rebuild 編排與視窗）**

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
	if !hasOpenTurn(got.Envelopes) || truncatedMidTurn(got.Envelopes) {
		t.Fatal("未結束 turn 必須一併載入且不得從中間截斷（§3.8）")
	}
}

func TestPagingUsesBeforeEventIDCursor(t *testing.T) {
	a := newTestAppWithTurns(t, "w1", 45)
	first := a.RestoreViews()["w1"].Envelopes
	page, err := a.LoadTurnsBefore("w1", first[0].EventID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if countCompleteTurns(page) != 20 || overlaps(first, page) {
		t.Fatalf("每頁 20 turn 且不得重疊：%d", countCompleteTurns(page))
	}
}
```

- [ ] **Step 3: 失敗測試（binding 轉發）**

```ts
it('LoadTurnsBefore 逐參數轉發', () => {
  const b = makeBindings()
  b.LoadTurnsBefore('w1', 'e100', 20)
  expect(App.LoadTurnsBefore).toHaveBeenCalledWith('w1', 'e100', 20)
})
```

- [ ] **Step 4: 跑測試確認失敗** — `go test . -race -v && npm --prefix frontend run test -- bindings` → FAIL

- [ ] **Step 5: 實作**

`writeAndEmitLocked` 取得 receipt 後在**同一 mutex 內**呼叫 `index.Observe`（滿足 §3.5.7 的 emit／index 同鎖前提）。`restoreSessions` 完整化：

```go
func (a *App) restoreSessions() ([]wsregistry.Entry, error) {
	live, err := a.loadSessionRegistry() // Task 6：registry_load → migrate → restore_dormant
	if err != nil {
		return nil, err
	}
	if err := a.replayIndex.VerifyOrRebuild(a.eventsPath()); err != nil { // index_verify
		return nil, err
	}
	if err := a.repairIncompleteTurns(live); err != nil { // detect_incomplete → emit_stream_error
		return nil, err
	}
	a.openUIAndProviders() // open_ui：修復完才開放
	return live, nil
}
```

`repairIncompleteTurns` 冪等判定：該 WSID 末筆已是 app-restart `stream_error` 或其導出的 `failed` 即跳過。rebuild 編排以 `context.Context` 承載 shutdown 取消、`atomic.Bool` 保證單一 active。同 task 完成 `LoadTurnsBefore` 的 binding 四件事。

- [ ] **Step 6: 全綠** — `go vet ./... && go test -race ./... -count=1 && npm --prefix frontend run test && npm --prefix frontend run build` → PASS

- [ ] **Step 7: Commit** — `git commit -m "feat(app): replay index 接線＋啟動序列完整化＋單一 active rebuild／backoff／shutdown 取消（§3.2.4／§3.5）"`

---

## Phase 5 — 移除、approval 與 shutdown

### Task 21: 可注入的 `After`——`WaitQuiesce`／`CloseSequence`

**Files:** Modify `internal/appcore/pump.go:23-31`／`33-67`、`app.go:4176`；Test `internal/appcore/pump_test.go`

**Interfaces:**
- Produces:
```go
// After 是可注入的等待來源；production 傳 RealAfter，測試傳受控 timer。
type After func(d time.Duration) <-chan time.Time
func RealAfter(d time.Duration) <-chan time.Time { return time.After(d) }

// **純 additive**：只在參數尾端加 after；回傳型別、ports.Exit、finalize 的
// error 回傳與 errors.Join 傳播全部維持 pump.go:41-60 現行契約，一字不改。
func WaitQuiesce(done <-chan struct{}, timeout time.Duration, after After) error
func CloseSequence(closeFn func() error, done <-chan struct{},
	quiesceTimeout, killTimeout time.Duration,
	terminate func() error, wait func() ports.Exit,
	finalize func(ports.Exit) error,
	after After) (ports.Exit, error)
```

> 先做這個 task，Task 24 的 bounded-window barrier 才有東西可注入（`pump.go:27` 現在直接用 `time.After`）。

- [ ] **Step 1: 失敗測試**

```go
func TestWaitQuiesceUsesInjectedAfter(t *testing.T) {
	fired := make(chan time.Time, 1)
	var gotTimeout time.Duration
	after := func(d time.Duration) <-chan time.Time { gotTimeout = d; return fired }
	done := make(chan struct{})
	errC := make(chan error, 1)
	go func() { errC <- WaitQuiesce(done, 7*time.Second, after) }()
	fired <- time.Time{} // 受控觸發，不真的等 7 秒
	if err := <-errC; err == nil {
		t.Fatal("逾時必須回 error")
	}
	if gotTimeout != 7*time.Second {
		t.Fatalf("timeout 未傳入注入的 after：%v", gotTimeout)
	}
}

func TestCloseSequenceEscalatesWithInjectedAfter(t *testing.T) {
	quiesce := make(chan time.Time, 1)
	kill := make(chan time.Time, 1)
	calls := 0
	after := func(d time.Duration) <-chan time.Time {
		calls++
		if calls == 1 {
			return quiesce
		}
		return kill
	}
	var terminated bool
	done := make(chan struct{})
	errC := make(chan error, 1)
	go func() {
		_, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
			func() error { terminated = true; return nil },
			func() ports.Exit { return ports.Exit{} },
			func(ports.Exit) error { return nil }, after)
		errC <- err
	}()
	quiesce <- time.Time{} // 第一次逾時 → 升級 Terminate
	kill <- time.Time{}    // 第二次逾時 → 盡力 finalize
	<-errC
	if !terminated {
		t.Fatal("quiesce 逾時必須升級 Terminate")
	}
	if calls != 2 {
		t.Fatalf("必須恰有兩段 bounded window：%d", calls)
	}
}

// 錯誤契約回歸：finalize 的 error 必須仍進 errors.Join（pump.go:59-60）
func TestCloseSequenceStillPropagatesFinalizeError(t *testing.T) {
	done := make(chan struct{})
	close(done) // 直接 quiesce 成功，走 wait → finalize 路徑
	finErr := errors.New("finalize failed")
	_, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
		func() error { return nil },
		func() ports.Exit { return ports.Exit{Exited: true, Code: 0} },
		func(ports.Exit) error { return finErr }, RealAfter)
	if !errors.Is(err, finErr) {
		t.Fatalf("finalize error 必須仍回傳並 Join：%v", err)
	}
}

// 回傳型別回歸：仍是 ports.Exit，且 wait() 的 cached Exit 原樣回傳
func TestCloseSequenceReturnsPortsExit(t *testing.T) {
	done := make(chan struct{})
	close(done)
	want := ports.Exit{Exited: true, Code: 7, StderrTail: "tail"}
	got, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
		func() error { return nil }, func() ports.Exit { return want },
		func(ports.Exit) error { return nil }, RealAfter)
	if err != nil || got != want {
		t.Fatalf("回傳契約不得改變：%+v %v", got, err)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test ./internal/appcore/ -run 'TestWaitQuiesceUses|TestCloseSequenceEscalates' -race -v` → FAIL

- [ ] **Step 3: 實作** — **只在參數尾端加 `after After`**，`ports.Exit`／`finalize func(ports.Exit) error`／`errors.Join` 傳播順序全部照 `pump.go:41-60` 原樣保留。`app.go:4176` 傳 `appcore.RealAfter`，並新增 `App.afterFn`（測試可覆寫）。既有 `pump_test.go` 呼叫點同步加參數，**斷言一字不改**。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "refactor(appcore): WaitQuiesce／CloseSequence 可注入 After（為 shutdown bounded-window barrier 鋪路）"`

### Task 22: 移除＝tombstone＋名額釋放時點（含序列化 barrier）

**Files:** Modify `app.go`；Test `app_remove_test.go`、`frontend/src/lib/bindings.test.ts`

**Interfaces:** Produces `func (a *App) RemoveSession(wsid string) error`（純新增 binding，本 task 完成四件事）。

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
	want := []string{"deny_approvals", "teardown", "lease_finalize",
		"cleanup_files", "tombstone_persist", "decrement_count"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("釋放名額必須是最後一步（§3.6.2）：\n got=%v\nwant=%v", order, want)
	}
}

func TestRemovedTombstoneSurvivesRestartAndRebuild(t *testing.T) {
	dir := t.TempDir()
	a1 := newTestAppAt(t, dir)
	w := mustCreate(t, a1, "codex")
	mustEmitTurn(t, a1, w)
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

// Remove × New 共用 ownership token：deterministic barrier。
// 刻意留一個空 slot，讓 Create 在無競態時「必定成功」——如此 Create 若立即
// 失敗或未取得 token，就是實作沒有共用 token，而不是被名額擋掉。
func TestRemoveXNewShareOwnershipToken(t *testing.T) {
	a := newTestApp(t)
	var hosts []appcore.WSID
	for k := 0; k < appcore.MaxSessionsPerProvider-1; k++ { // 佔 3/4，留一個空位
		w := mustCreate(t, a, "claude")
		mustStartClaude(t, a, w)
		hosts = append(hosts, w)
	}
	victim := hosts[0]

	removeHoldingToken := make(chan struct{})
	releaseRemove := make(chan struct{})
	createWaitingForToken := make(chan struct{})
	var createTookTokenWhileRemoveHeld atomic.Bool

	a.hookRemoveHoldingToken = func() { close(removeHoldingToken); <-releaseRemove }
	a.hookCreateWaitingForToken = func() { close(createWaitingForToken) }
	a.hookCreateAcquiredToken = func() {
		if a.removeTokenHeldForTest() {
			createTookTokenWhileRemoveHeld.Store(true)
		}
	}

	removeErr := make(chan error, 1)
	go func() { removeErr <- a.RemoveSession(string(victim)) }()
	<-removeHoldingToken

	createErr := make(chan error, 1)
	go func() { _, err := a.CreateSession("claude", "new"); createErr <- err }()

	select { // Create 必須「在等 token」，而不是直接失敗或直接成功
	case <-createWaitingForToken:
	case err := <-createErr:
		t.Fatalf("Create 未等待共用 token 就返回（err=%v）——未序列化", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Create 未進場")
	}

	close(releaseRemove)
	if err := <-removeErr; err != nil {
		t.Fatal(err)
	}
	if err := <-createErr; err != nil { // 只讀一次
		t.Fatalf("Remove 放行後 Create 應成功（有空 slot）：%v", err)
	}
	if createTookTokenWhileRemoveHeld.Load() {
		t.Fatal("Create 在 Remove 仍持有 token 時取得 token＝未序列化")
	}
	if got := a.manager.SlotCount("claude"); got != appcore.MaxSessionsPerProvider-1 {
		t.Fatalf("移除一個＋新增一個，名額應維持 3：%d", got)
	}
}
```

- [ ] **Step 2: 失敗測試（binding）**

```ts
it('RemoveSession 逐參數轉發', () => {
  const b = makeBindings()
  b.RemoveSession('w1')
  expect(App.RemoveSession).toHaveBeenCalledWith('w1')
})
```

- [ ] **Step 3: 跑測試確認失敗** — `go test . -run TestRemove -race -v && npm --prefix frontend run test -- bindings` → FAIL

- [ ] **Step 4: 實作** — 依 §3.6.2 凍結順序；Remove 與 Create 共用同一 provider-scoped ownership token（`hookCreateWaitingForToken` 在嘗試取得 token 前呼叫、`hookCreateAcquiredToken` 在取得後呼叫）。同 task 完成 binding 四件事。

- [ ] **Step 5: 全綠＋競態穩定** — `go test . -run TestRemoveXNew -race -count=30` → PASS

- [ ] **Step 6: Commit** — `git commit -m "feat(app): 移除＝durable tombstone＋名額釋放時點＋Remove×New 共用 token（§3.6.1-2）"`

### Task 23: 待核可時移除＋per-WSID 檔案清理

**Files:** Modify `app.go`；Test `app_remove_approval_test.go`

- [ ] **Step 1: 失敗測試**

```go
func TestRemoveDeniesAllPendingApprovalsAndCleansFiles(t *testing.T) {
	a := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	sock, mcp := a.hostFor(w).sockPath, a.hostFor(w).mcpPath
	ids := seedPendingApprovals(t, a, w, 3)
	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if d := recordedDecision(t, a, id); d.allow || d.reason != "session_removed" {
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
	a.failDenyForIndex(1)
	err := a.RemoveSession(string(w))
	if err == nil {
		t.Fatal("deny 部分失敗不得靜默移除")
	}
	for _, id := range ids { // 每一筆都必須被嘗試——不得在第一個錯誤就中斷
		if !a.denyAttempted(id) {
			t.Fatalf("approval %s 未被嘗試 deny", id)
		}
	}
	if !strings.Contains(err.Error(), "deny failed") {
		t.Fatalf("錯誤未完整 Join：%v", err)
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
	if e, ok := a.wsReg.Get(string(w)); !ok || e.RemovedAt != "" {
		t.Fatal("未成功移除時 registry entry 必須保留且不得標 tombstone")
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run 'TestRemoveDenies|TestPartialDeny' -race -v` → FAIL

- [ ] **Step 3: 實作** — best-effort deny 全部（逐筆 recover 不中斷）→ `errors.Join` → bounded teardown → lease finalize → 檔案清理。

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(app): 待核可時移除全部 fail-closed deny＋部分失敗保留 dormant slot＋檔案清理（§3.6.3）"`

### Task 24: shutdown 總序＋並行與 bounded-window barrier

**Files:** Modify `app.go:694-800`；Test `app_shutdown_multi_test.go`

**Interfaces:** Consumes Task 7 `snapshotHosts`、Task 12/13 wire log、Task 20 index、**Task 21 可注入 `After`**。

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

// (1) 8 個 host 的 teardown 必須同時進場——Claude 與 Codex 都算，
//     但只用 hookTeardownEntered barrier，不假設每個 host 都有 CloseSequence timer。
func TestAllTeardownsRunConcurrently(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 4, 4)
	const n = 8
	entered := make(chan appcore.WSID, n)
	release := make(chan struct{})
	a.hookTeardownEntered = func(w appcore.WSID) { entered <- w; <-release }

	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()
	seen := map[appcore.WSID]bool{}
	for k := 0; k < n; k++ {
		select {
		case w := <-entered:
			seen[w] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("只有 %d 個 teardown 進場——未並行（§3.6.5）", k)
		}
	}
	if len(seen) != n {
		t.Fatalf("應有 8 個相異 host 進場：%d", len(seen))
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown 未收斂")
	}
}

// (2) bounded window 只適用「卡死的 Claude」——每個 Claude session 有自己的
//     子行程與 CloseSequence；Codex 四個 session 共用一個 app-server，
//     不會、也不該產生 per-host 的 CloseSequence timer（spec §3.6.5 凍結的
//     情境即「4 個 Claude 卡死時最壞仍為單一 bounded window」）。
func TestStuckClaudeSessionsShareSingleBoundedWindow(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 4, 4)
	for _, h := range a.hostsOf("claude") {
		a.makeHostStuck(h.wsid)
	}
	timers := newFakeAfter() // 記錄每次 After 呼叫並回傳受控 channel
	a.afterFn = timers.After

	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()

	timers.WaitForOutstanding(t, 4) // 4 個 quiesce timer 同時存在＝並行且僅 Claude
	timers.FireAll()
	timers.WaitForOutstanding(t, 4) // 4 個 kill timer 同時存在
	timers.FireAll()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown 未收斂")
	}
	if got := timers.Rounds(); got != 2 {
		t.Fatalf("應為兩段 bounded window（quiesce＋kill），實測 %d 輪＝串行", got)
	}
	if got := timers.TotalCreated(); got != 8 {
		t.Fatalf("只有 4 個卡死 Claude 各兩段 timer＝8；Codex 不應有 per-host timer：%d", got)
	}
	if !a.allHostsTornDown() {
		t.Fatal("卡死者也必須收斂")
	}
}

// (3) Codex：全部 session host 收乾之後，才 terminate／wait 共用 app-server。
//     hookTeardownDone 由 4 個並行 teardown goroutine 呼叫，hookShutdownStep 由
//     shutdown 主 goroutine 呼叫——順序紀錄必須加鎖，否則 -race 必報 data race。
func TestCodexSharedServerTerminatedAfterAllHostsDrained(t *testing.T) {
	a := newTestAppWithFakeWire(t)
	seedSessions(t, a, 0, 4)
	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	hosts := make(chan appcore.WSID, 4)
	a.hookTeardownDone = func(w appcore.WSID) {
		hosts <- w
		record("host_done:" + string(w))
	}
	a.hookShutdownStep = func(s string) {
		if s == "server_terminate_wait" || s == "wirelog_finalize" {
			record(s)
		}
	}
	a.shutdown(context.Background())

	close(hosts)
	seen := map[appcore.WSID]bool{}
	for w := range hosts {
		seen[w] = true
	}
	if len(seen) != 4 {
		t.Fatalf("4 個 Codex host 都必須收乾：%d", len(seen))
	}

	mu.Lock()
	defer mu.Unlock()
	termIdx := indexOf(order, "server_terminate_wait")
	if termIdx < 4 {
		t.Fatalf("共用 app-server 必須在 4 個 host 全部收乾後才 terminate：%v", order)
	}
	for k := 0; k < termIdx; k++ {
		if !strings.HasPrefix(order[k], "host_done:") {
			t.Fatalf("terminate 之前只該有 host_done：%v", order)
		}
	}
	if wireIdx := indexOf(order, "wirelog_finalize"); wireIdx < termIdx {
		t.Fatalf("wire log 必須在 terminate／wait 之後 finalize（§3.4.2）：%v", order)
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

- [ ] **Step 3: 實作** — 依 §3.6.5 逐行；per-session teardown 用 goroutine＋`WaitGroup`，全部把 `a.afterFn` 傳進 `CloseSequence`。

- [ ] **Step 4: 全綠＋競態穩定** — `go test . -run 'TestAllTeardowns|TestStuckClaude|TestCodexSharedServer' -race -count=30` → PASS（**不得出現真實 5s／10s 等待**）

- [ ] **Step 5: Commit** — `git commit -m "feat(app): shutdown 總序凍結＋並行 teardown 與單一 bounded window barrier（§3.6.5）"`

### Task 25: approval WSID 路由（後端）

**Files:** Modify `app.go:3758-3809`；Test `app_approval_route_test.go`

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
		t.Fatalf("多筆待核可 FIFO promotion：%v", got)
	}
	if ev := approvalEventFor(t, a, id1); ev.WorkspaceSessionID != string(w1) {
		t.Fatalf("approval 事件必須帶 WSID：%+v", ev)
	}
}
```

- [ ] **Step 2: 跑測試確認失敗** — `go test . -run TestApprovalCarries -race -v` → FAIL

- [ ] **Step 3: 實作**

- [ ] **Step 4: 全綠** — `go vet ./... && go test -race ./... -count=1` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(app): approval 依 WSID 路由＋FIFO promotion（§3.6.4）"`

---

## Phase 6 — 前端

### Task 26: exported binding WSID 化＋session store per-WSID lane（**原子切換**）

> 本 task 是 Global Constraints 第 2 條的原子切換點：後端 exported 簽名、`wailsjs`、adapter、`types.ts`、session store **必須同一個 commit 一起改**，否則前端會在中途壞掉。

**Files:** Modify `app.go`（刪 `legacyWSIDFor` 包裝層）、`frontend/wailsjs`（重生）、`frontend/src/lib/bindings.ts`、`types.ts`、`stores/session.ts`；Test `bindings.test.ts`、`session.test.ts`

**Interfaces:**
- Produces（後端）：`StartSession(wsid, prompt, resume, recordCase, taskLabel, approvalPolicy)`／`SendMessage(wsid, text)`／`EndSession(wsid)`／`NewSession(wsid)`。
- Produces（前端）：
```ts
export interface SessionMeta {
  wsid: string; provider: ProviderKey; taskLabel: string
  state: string; unread: number; busy: boolean; awaitingApproval: boolean; removed: boolean
}
interface State {
  sessions: Record<string, SessionMeta>
  views: Record<string, SessionView>   // 只有釘選／曾切入的才有 transcript
  persistentPins: [string | null, string | null]
  pins: [string | null, string | null]
  focused: 0 | 1
  scrollAnchors: Record<string, string>
}
```
- **契約凍結**：`busy`／`unread`／`awaitingApproval`／`state` 一律讀寫 `sessions[wsid]`；`views` 只承載 transcript（`chat`／`timeline`／`totals`）。

- [ ] **Step 1: 失敗測試（binding 轉發＋WSID 回歸）**

```ts
it('WSID 取代 provider 作為第一參數——不得誤傳 provider', () => {
  const b = makeBindings()
  b.SendMessage('01JWSIDABC', 'text')
  const [first] = vi.mocked(App.SendMessage).mock.calls.at(-1)!
  expect(first).toBe('01JWSIDABC')
  expect(['claude', 'codex']).not.toContain(first)
})

it('StartSession／EndSession 逐參數轉發且第一參數為 WSID', () => {
  const b = makeBindings()
  b.StartSession('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')
  expect(App.StartSession).toHaveBeenCalledWith('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')
  b.EndSession('w1')
  expect(App.EndSession).toHaveBeenCalledWith('w1')
})
```

- [ ] **Step 2: 失敗測試（store）**

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

- [ ] **Step 3: 跑測試確認失敗** — `npm --prefix frontend run test` → FAIL

- [ ] **Step 4: 原子切換** — 後端改 exported 簽名並刪 `legacyWSIDFor` → `wails generate module` → `bindings.ts` → `types.ts` → `session.ts`。

- [ ] **Step 5: 全綠** — `go vet ./... && go test -race ./... -count=1 && npm --prefix frontend run test && npm --prefix frontend run build` → PASS

- [ ] **Step 6: Commit** — `git commit -m "feat: exported binding WSID 化＋session store per-WSID lane（原子切換，§3.6.4／§3.7-8）"`

### Task 27: `SessionList.vue`

**Files:** Create `SessionList.vue`＋測試；Modify 雙 locale

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

it('卡片顯示 unread 與待核可標記', async () => {
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

- [ ] **Step 3: 實作元件＋雙 locale（key parity 綠）**

- [ ] **Step 4: 全綠** — `npm --prefix frontend run test` → PASS

- [ ] **Step 5: Commit** — `git commit -m "feat(frontend): SessionList——既有 session 清單＋n/4 計數＋移除確認（§4）"`

### Task 28: `DualPane.vue`＋`PaneView.vue`

**Files:** Create `DualPane.vue`／`PaneView.vue`＋測試；Modify `App.vue`、`SettingsBar.vue`、`StatusBar.vue`

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

it('只有 focused pane 有 composer', async () => {
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
  expect(w.find('[data-test=pane-1]').element).toBe(before)
})

it('A 執行中仍可切 B 送出', async () => {
  const s = useSession()
  s.pin(0, 'w1'); s.pin(1, 'w2')
  s.sessions['w1'].busy = true
  s.setFocus(1)
  await s.submit('hello')
  expect(s.bindings.SendMessage).toHaveBeenCalledWith('w2', 'hello')
})

it('SettingsBar 的 End 只作用於 focused pane', async () => {
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

- [ ] **Step 5: Commit** — `git commit -m "feat(frontend): 雙 pane 並看＋單一 focused pane 操作語意（§3.7／§4）"`

### Task 29: lazy load 與向上分頁

**Files:** Modify `PaneView.vue`、`stores/session.ts`；Test `PaneView.test.ts`

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

- [ ] **Step 5: Commit** — `git commit -m "feat(frontend): 釘選 lazy load＋向上 20 turn 分頁去重（§3.8）"`

---

## Phase 7 — 不變量、迴歸與驗收

### Task 30: 跨切面不變量測試

**Files:** Test `app_invariants_test.go`

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
	dir := seedLegacyJournalFixture(t)
	a := newTestAppAt(t, dir)
	live, err := a.restoreSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Provider != "claude" {
		t.Fatalf("legacy 事件應歸屬遷移後的 legacy session：%+v", live)
	}
	if len(a.RestoreViews()[live[0].WSID].Envelopes) == 0 {
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
	mustSubmitGate(t, a)
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

- [ ] **Step 3: Commit** — `git commit -m "test: M3b 跨切面不變量（§5.7）"`

### Task 31: E2E 驗收矩陣＋最終 gate

**Files:** Create `docs/spikes/m3b-results.md`；Modify `README.md`

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
| A9 | M2／M3a／M3a.1 迴歸抽驗 | |
| A10 | Claude 4 session 常駐的 RAM／CPU 實測（§7.1） | |

- [ ] **Step 2: 收尾 gate 全綠**

```bash
go vet ./... && go test -race ./... -count=1 && \
npm --prefix frontend run test && npm --prefix frontend run build && wails build
```

- [ ] **Step 3: 寫結果文件** — A1-A10 逐項證據、缺口清單、PR 揭露事項。**未通過項如實記錄，不得標綠**。

- [ ] **Step 4: Commit** — `git commit -m "docs: M3b E2E 驗收矩陣結果＋README 章節"`

---

## Self-Review 記錄（v6）

**v5 → v6 修正（一項 P1）**

| # | 問題 | 修正 |
|---|---|---|
| P1-1 | `TestWatcherEpochCapturedUnderLock` 用 `sync.Once` 當 barrier 會確定性死鎖——`Once.Do` 在第一次 callback 返回前會阻塞第二次呼叫，於是第二個 replacement 也卡住，而 `releaseFirst` 又要等 replacement 返回才關閉 | 改用 `atomic.Int32` 計數：`if calls.Add(1) == 1` 才停在 barrier，後續呼叫直接通過；並在測試內加註為何不能用 `sync.Once` |

**v4 → v5 修正（兩項 P1＋一項補述）**

| # | 問題 | 修正 |
|---|---|---|
| P1-1 | epoch 在解鎖後才取得，有 TOCTOU——期間若已有第二次 replacement，第一個 watcher 會拿到第二個 owner 的 epoch，之後錯誤取走新 generation | 新增純 additive 的 `Single.WithExclusiveEpoch(fn) (uint64, error)`，**在同一把鎖內發布並回傳該次 epoch**；`RunOwnedHandshake` 改用它，並明寫「絕不可解鎖後再呼叫 `Epoch()`」；新增 `TestWatcherEpochCapturedUnderLock` barrier（第一個 owner 發布後、watcher 註冊前停住 → 第二次 replacement 完成 → 放行，斷言舊 watcher 不得清掉新 owner、新 generation 未被 finalize） |
| P1-2 | `TestCodexSharedServerTerminatedAfterAllHostsDrained` 的 `order` 被 4 個並行 teardown goroutine 與 shutdown 主 goroutine 同時 append，`-race -count=30` 必報 data race | 順序紀錄改為 mutex 保護的 `record()`；host 完成另以 buffered channel 收集並斷言 4 個相異 WSID；保留「terminate 之前只該有 host_done」與「wirelog_finalize 在 terminate 之後」兩條斷言 |
| 補述 | 實作者可能誤讀「全段在單一互斥交易內」而在 App 端再包一層 `WithExclusive` | Task 13 新增「鎖層次（凍結）」：`RunOwnedHandshake` 內部自持 `WithExclusiveEpoch`，呼叫端不得再加鎖（`single.go:54-56` 同一把 mutex，巢狀即死鎖） |

**v3 → v4 修正（四項 P1＋兩項小修）**

| # | 問題 | 修正 |
|---|---|---|
| P1-1 | Task 0 的 `-force` 分支會在終止 server 後才跑 approval turn | approval turn 移到並行段**之前**（server 必活著），`-force` 只影響 (a)(c)；新增「兩次執行的判定聚合」凍結段落：(b) 以 natural run 為準、(c) 兩次都須通過 |
| P1-2 | Task 12 改動 `RunHandshakeProbe` 會讓 App（`Single[*codex.Server]`）編譯失敗；`s.cur == want` 泛型不可比較；stale 分支未呼叫 `onFinalized` 導致測試卡住；`RunHandshakeProbeWithWatch` 未列入契約 | Task 12 改為**純新增** `RunOwnedHandshake`＋`WatchGeneration`，舊 `RunHandshakeProbe` 一行不動並加 `TestOldProbeEntryPointUnchanged` 守門；`CompareAndTake` 改為 **epoch 版**（`Epoch()`／`CompareAndTakeEpoch`），不動 `T Alive` 約束；`onFinalized(err, wasActive)` **兩種分支都呼叫**，stale 測試改等 callback 並斷言 `wasActive==false`；App ownership 型別遷移與舊入口刪除移到 **Task 13**（含 grep 守門） |
| P1-3 | Task 21 誤把 `ports.Exit` 改成 `proc.Exit`、`finalize` 不再回錯 | 簽名改為**純 additive**（只在尾端加 `after After`），`ports.Exit`／`finalize func(ports.Exit) error`／`errors.Join` 傳播照 `pump.go:41-60` 原樣；新增 `TestCloseSequenceStillPropagatesFinalizeError` 與 `TestCloseSequenceReturnsPortsExit` 兩條契約回歸 |
| P1-4 | Task 24 把 4 個 Codex session 當成 4 個獨立 `CloseSequence`（共用一個 app-server） | 拆成三條：`TestAllTeardownsRunConcurrently`（8 host 用 `hookTeardownEntered` barrier，不假設 timer）、`TestStuckClaudeSessionsShareSingleBoundedWindow`（只 4 個卡死 Claude → 4 quiesce＋4 kill、`TotalCreated()==8` 斷言 Codex 無 per-host timer）、`TestCodexSharedServerTerminatedAfterAllHostsDrained`（全部 host 收乾才 terminate／wait，wire log 最後 finalize） |
| 小修 1 | `TestExportedBindingSignatureUnchanged` 用了未宣告的 `a` | 補 `a := newTestApp(t)`＋`mustStartClaude` 前置 |
| 小修 2 | 多處寫「Task 25 原子切換」，實際是 Task 26 | 全域約束與 Task 8／9 共四處統一為 Task 26 |

**v2 → v3 修正（六項 P1＋兩項 P2）**

| # | 問題 | 修正 |
|---|---|---|
| P1-1 | Task 6 向後依賴 `hostFor()`（Task 7）與 `replayindex`（Task 15-18） | Task 6 縮小為 `loadSessionRegistry`（只做 registry 載入／遷移／`RestoreDormant`），測試改用 `IsActiveWS`／`SlotCount` 斷言 dormant；§3.2.4 後半（index 驗證 → incomplete 修復 → 開放 UI）併入 **Task 20**，含 `TestStartupOrderIsFrozen`／`TestUIOpensOnlyAfterRepair` |
| P1-2 | Task 0 driver 用不存在的 `codex.Start`、binary 未傳入、approval 歸屬無實際 frame | 改 `codex.StartAppServer(ctx, codex.Config{Binary: *codexBin, CWD: tmp, TermGrace: …})`；新增 `-codex-bin` 旗標；新增第三個**受控 approval turn**（要求建檔 → probe 拒絕）；GO 條件明訂「未觀察到 approval request 或缺 thread/turn identity → NO-GO」，並驗證被拒後檔案不存在 |
| P1-3 | `GenerationOwner` 只證明手動收尾；`Single.Ensure` 遇死亡會直接覆寫、漏 finalize | 新增 `Single.CompareAndTake`＋production `WatchGeneration` reaper；測試改為只呼叫 `stub.die()`，斷言自動 finalize、replacement 等舊 generation finalize 完才發布、stale reaper 不清新 generation |
| P1-4 | Task 9 刪 shim 的清單不完整；binding 延後違反契約 | Task 9 列出五項刪除清單（含 `pump.go:69` `EndSessionFlow`、`app.go` 24 處呼叫、`manager_test.go` 1254 行）並加 `grep` 守門；binding 分兩類——純新增在各自 task（4／20／22）完成四件事，改簽名的保留 provider-keyed 包裝層到 **Task 26 原子切換**，Task 8 加 `TestExportedBindingSignatureUnchanged` 守門 |
| P1-5 | Remove×New 測試會死鎖且允許 Create 立即失敗 | 改留一個空 slot（Create 無競態時必成功）；新增 `hookCreateWaitingForToken`／`hookCreateAcquiredToken`；用 `select` 區分「在等 token」與「直接返回」，直接返回即判失敗；結果 channel **各只讀一次** |
| P1-6 | fake clock 未接到 production timeout（`pump.go:27` 用 `time.After`） | 新增 **Task 21**：`WaitQuiesce`／`CloseSequence` 加可注入 `After`（`RealAfter` 為 production 預設）；Task 24 改用 `timers.WaitForOutstanding(8)` → `FireAll()` 兩輪，證明並行＋單一 bounded window，且不產生真實等待 |
| P2-1 | `newULID()` 在 appcore 不存在 | 一律用 `contract.NewULID(time.Now())`（`envelope.go:19`），並加 `TestWSIDIsULID` |
| P2-2 | spec 狀態列與 plan 不一致 | spec 狀態列同步為「rev4，closure review APPROVED（2026-08-14，`b63f168`）」 |

**任務數**：32（Task 0-31）。Phase 5 因新增可注入 `After` 而多一個 task，Phase 6 因 binding 原子切換與 store 合併而少一個。

**Spec 覆蓋**：§1／§2／§3.1-3.8／§4／§5／§6／§7.2 逐條對應。啟動序列 §3.2.4 拆在 Task 6（前半）與 Task 20（後半），兩處都有凍結順序測試。

**型別一致性**：`appcore.WSID`、`appcore.After`／`RealAfter`、`AppendReceipt`、`wsregistry.Entry`、`wirelog.SegmentRef`、`codex.GenerationOwner`、前端 `SessionMeta`／`SessionView`／`persistentPins`／`pins`／`focused` 全程一致。

**遺留執行前提**：Task 3 的 Manager 相容入口與 Task 8-25 的 provider-keyed exported binding 包裝層都是**暫時**產物，分別由 Task 9 與 Task 26 的 `grep` 守門與測試確保清乾淨。
