# Legacy hydrate 收尾 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 關掉 hydrate 主線遺留的兩個可觀測性／效能尾巴：(1) backfill 失敗補具名 audit 事件；(2) `LegacyTranscript=true` 但 window 空時每次首載重複掃全檔——成功掃描確定零筆時清旗標，且清除語意嚴格區分「成功零筆／未掃描／缺檔／錯誤」四分支（owner 2026-08-23 裁決）。

**Architecture:** `scanLegacyWindow` 簽章擴充 `scanned bool`（NotExist 與成功零筆自此可區分，既有三個呼叫端行為不變）；新增 `Store.ClearLegacyTranscript`（單向窄寫入、冪等不落盤、哨兵慣例、登記 latch 枚舉表）；`loadTurnsBefore` 合併分支在 `scanned && len==0` 時清旗標，persist 失敗回錯（fail loud，owner 裁決）；backfill 失敗路徑補 `legacy_transcript_backfill_failed` audit。

**Tech Stack:** Go（internal/wsregistry、restore.go、rebuild_orchestrator.go、app.go），Go testing。

**Spec:** `docs/superpowers/specs/2026-08-22-legacy-transcript-hydrate-design.md` §3 語意例外／§4 失敗軌跡（含統一稽核順序）／§5a scanned／§6a（含 persist 失敗三語意表）——最終 snapshot **commit cdf2dbe**（4b993fe 初版→7fe9d2e rev2→cdf2dbe rev3，執行者以 cdf2dbe 為準）

## Global Constraints

- §6a 四分支凍結：`scanned==true && len==0` → 清；`ViewStartEventID==""`（未掃描）→ 不清；NotExist（`scanned==false`）→ 不清；open／scan error → 不清、回錯、可重試。
- persist 失敗依 §6a **三語意表**（對齊 `persistOrRollback` 既有契約 store.go:262-275）：步驟 1-3 失敗＝回滾（flag 維持 true）可重試；已 latched＝回滾＋`ErrRegistryUncertain` 需重啟；本次 Clear 的 directory-sync 失敗＝**不回滾**（記憶體停在 false）＋latch 需重啟——不得籠統宣稱「旗標仍 true」。三種都回錯。
- `ClearLegacyTranscript` 僅清旗標、不動 boundary；已 false 冪等跳過不落盤；`ErrEntryNotFound`／`ErrTombstoned` 哨兵；登記 uncertain latch `writes` 枚舉表（fsync_test.go:254 附近）。
- `scanLegacyWindow` 簽章變更的 caller 以 repo-wide `rg 'scanLegacyWindow\(' -g '*.go'` 為準（目前 8 處：production app.go:1283／app.go:1814／rebuild_orchestrator.go:362、定義 restore.go:206、TestScanLegacyWindow 內 4 處）。漏任何一處 commit 即 build 壞。
- 既有測試不得削弱；gofmt 乾淨（觸碰檔案）；台灣用語書面中文 doc／commit。

---

### Task 1: wsregistry — ClearLegacyTranscript

**Files:**
- Modify: `internal/wsregistry/store.go`（緊鄰 `ResetView`）、`internal/wsregistry/fsync_test.go`（latch `writes` 枚舉表加一行＋`:232` 的 `Put` 補 `LegacyTranscript: true` 讓新條目是真變更）
- Modify: `app.go`（`sessionRegistry` interface 加 `ClearLegacyTranscript(wsid string) error`，約 :676——`var _ sessionRegistry = (*wsregistry.Store)(nil)` 既有斷言隨 build 驗證）
- Modify: `app_wsid_test.go`（**plan gate P1**：`stubRegistry`（:14）實作 `sessionRegistry`、被 30+ 處測試使用——interface 加方法後必須同步補 stub 方法（受既有 `mutateErr` 驅動，對齊其他 mutator stub 的寫法），否則本 task 的 commit 會讓 root package 測試編譯失敗，且 `go build ./...` 抓不到（test 檔要 `go vet ./...` 或 `go test` 才編譯）
- Test: `internal/wsregistry/store_test.go`（round-trip＋哨兵）、`internal/wsregistry/fsync_test.go`（三個 step-hook 注入測試＋枚舉表）

**Interfaces:**
- Produces: `Store.ClearLegacyTranscript(wsid string) error`——entry 不存在回 `ErrEntryNotFound`、tombstone 回 `ErrTombstoned`（per-WSID durable writer 哨兵慣例，見 store.go:32-35 doc）；flag 已 false 冪等 return nil 不落盤（`SetLayout` 冪等前例）；flag 為 true 時 mutate 清 false、persist 失敗回滾。

- [ ] **Step 1: Write the failing test**

```go
func TestClearLegacyTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLegacyTranscript("w1"); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Get("w1"); e.LegacyTranscript {
		t.Fatal("清除後 flag 應為 false")
	}
	s2, _ := Open(path)
	if e, _ := s2.Get("w1"); e.LegacyTranscript {
		t.Fatal("清除未持久化")
	}
	if e, _ := s2.Get("w1"); e.ViewStartEventID != "v1" {
		t.Fatalf("清旗標不得動 boundary：%q", e.ViewStartEventID)
	}
	// 哨兵：不存在／tombstone 是良性跳過訊號，不是一般錯誤。
	if err := s.ClearLegacyTranscript("nope"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("不存在應回 ErrEntryNotFound：%v", err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLegacyTranscript("w1"); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstone 應回 ErrTombstoned：%v", err)
	}
}

// 以下三個注入測試放 `internal/wsregistry/fsync_test.go`（recordSteps／failAt／
// ForceStepHookForTest 同檔、對齊既有 step-hook 測試慣例）。**不用 chmod**：
// root 可繞過權限，冪等守衛或回滾壞掉時測試照樣綠（owner review 2026-08-24 P1）。

// 冪等：flag 已 false 時零 persist step（確定性）。
func TestClearLegacyTranscriptIdempotentNoPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"}, // HasLegacyTranscript=false → flag=false
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	seen := recordSteps(s)
	if err := s.ClearLegacyTranscript("w1"); err != nil {
		t.Fatalf("flag 已 false 應冪等跳過：%v", err)
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("冪等路徑不得觸碰檔案系統：%d steps", n)
	}
}

// §6a 三語意之一：rename 前（stepWrite）失敗 → 回錯＋回滾，process 內可重試。
func TestClearLegacyTranscriptWriteFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepWrite, errors.New("disk full"))
	if err := s.ClearLegacyTranscript("w1"); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if e, _ := s.Get("w1"); !e.LegacyTranscript {
		t.Fatal("rename 前失敗必須回滾（flag 仍 true）")
	}
	s.ForceStepHookForTest(nil)
	if err := s.ClearLegacyTranscript("w1"); err != nil {
		t.Fatalf("解除注入後重試必須成功：%v", err)
	}
}

// §6a 三語意之三：directory-sync 失敗 → 不回滾、latch、後續寫入一律拒絕
// （owner review 2026-08-24 P1：不得宣稱旗標仍 true）。
//
// 「latch 後拒絕」的斷言必須用**另一筆 flag=true 的 entry**（w2）：w1 在
// dirSync 失敗後記憶體已停在 false（不回滾），再 Clear w1 會先撞冪等守衛回
// nil——那條斷言在正確實作下永遠打不綠，而「把 latch 檢查搬到冪等之前」看似
// 能打綠、實則推翻本 plan 凍結的 SetLayout 順序（gate 複驗 P1，兩候選皆實測）。
// 用 w2 同時補上三語意表「已 latched → 回滾」那格的旗標回滾斷言（枚舉表只驗
// 回錯與其他欄位、沒驗 flag 回滾）。
func TestClearLegacyTranscriptDirSyncFailureLatches(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
		"codex":  {ViewStartEventID: "v1", HasLegacyTranscript: true},
	}, func() string { n++; return fmt.Sprintf("w%d", n) }); err != nil {
		t.Fatal(err)
	}
	// Live() 依 CreatedAt→WSID 排序；兩筆 CreatedAt 相同，w1 < w2。
	failAt(s, stepDirSync, errors.New("dir sync EIO"))
	if err := s.ClearLegacyTranscript("w1"); !errors.Is(err, ErrRegistryUncertain) {
		t.Fatalf("dirSync 失敗必須回 ErrRegistryUncertain：%v", err)
	}
	if e, _ := s.Get("w1"); e.LegacyTranscript {
		t.Fatal("rename 已成功，記憶體不得退回 true（步驟 4 不回滾）")
	}
	if !s.Uncertain() {
		t.Fatal("必須 latch")
	}
	s.ForceStepHookForTest(nil)
	if err := s.ClearLegacyTranscript("w2"); !errors.Is(err, ErrRegistryUncertain) {
		t.Fatalf("latch 後寫入一律拒絕：%v", err)
	}
	if e, _ := s.Get("w2"); !e.LegacyTranscript {
		t.Fatal("已 latched 的寫入必須回滾（w2 flag 仍 true——三語意表第二格）")
	}
}
```

另在 `internal/wsregistry/fsync_test.go` 的 uncertain latch `writes` 枚舉表加：

```go
"ClearLegacyTranscript": func() error { return s.ClearLegacyTranscript("w1") },
```

（**順序凍結（plan gate 校正）**：沿用 `SetLayout` 慣例——**冪等比對先於 latch 檢查**（store.go:565-575 doc：「不改變任何可觀測狀態，所以刻意排在 latch 檢查之前」；latch gate 在 `persistOrRollback` :275-278）。因此枚舉表條目必須是**真變更**才會走到 latch 拒絕：把 fsync_test.go:232 的 `Put(Entry{WSID:"w1", ...})` 補 `LegacyTranscript: true`——同表的 `SetLayout` 條目（:261）刻意傳非 no-op 的 Layout，正是「表內條目必須是真變更」的既有慣例。不自創第三種順序。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run TestClearLegacyTranscript -v`
Expected: FAIL — `s.ClearLegacyTranscript undefined`（編譯失敗，含 fsync 枚舉表新行）

- [ ] **Step 3: Write minimal implementation**

`Store.ClearLegacyTranscript`：取 mu；哨兵判定（不存在→ErrEntryNotFound、`RemovedAt!=""`→ErrTombstoned）；冪等比對先於 latch（Step 1 括號註記）；flag=true 時存舊值、設 false、`persistOrRollback`。`sessionRegistry` interface（app.go:676）加同名方法；`stubRegistry`（app_wsid_test.go:14）補同名方法（受 `mutateErr` 驅動，對齊既有 mutator stub）。doc 說明：§6a 窄寫入、僅清旗標不動 boundary、冪等語意。

- [ ] **Step 4: Run test to verify it passes**

Run: `go vet ./... && go test ./internal/wsregistry/ -count=1`
Expected: PASS——**必須用 `go vet ./...`**：`go build ./...` 不編譯 test 檔，抓不到 stubRegistry 缺方法（plan gate 實測：漏補 stub 時 build 綠、vet 立刻報 `*stubRegistry does not implement sessionRegistry`）

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/store.go internal/wsregistry/store_test.go internal/wsregistry/fsync_test.go app.go app_wsid_test.go
git commit -m "feat(wsregistry): ClearLegacyTranscript——§6a 窄寫入（冪等、哨兵、回滾、latch 登記）"
```

---

### Task 2: scanLegacyWindow 簽章擴充 scanned bool

**Files:**
- Modify: `restore.go:206`（scanLegacyWindow）、`app.go:1283`（legacyEntries）、`app.go:1814`（backfillLegacyTranscript）、`rebuild_orchestrator.go:362`（loadTurnsBefore，本 task 最小適配 `legacy, _, lerr :=`；scanned 的實際使用在 Task 3）
- Test: `app_restore_dormant_test.go`（TestScanLegacyWindow 內 4 處呼叫改三回傳值＋新增 scanned 斷言）

**Interfaces:**
- Produces: `scanLegacyWindow(eventsPath, provider, viewStart string) ([]contract.Envelope, bool, error)`——`scanned==true` ＝ 開檔成功＋完整掃描＋`Scanner.Err()==nil`；NotExist → `(nil, false, nil)`；error → `(nil, false, err)`。

- [ ] **Step 1: Write the failing test**（既有 TestScanLegacyWindow 擴充，逐處改三回傳值並加 scanned 斷言）

既有四處呼叫改接 `(got, scanned, err)`，並加斷言：
- 正常掃描（有結果）：`scanned==true`。
- NotExist：`scanned==false`（維持 `envs==nil && err==nil`）。
- 目錄注入（Scanner.Err()）與 open_error subtest：`scanned==false` 且 `err!=nil`。
- 新增 case：檔案存在但過濾後零筆（只放 codex 事件、查 claude）→ `got==nil`／len 0 且 `scanned==true`——這是 §6a 清旗標的判定前提，NotExist 與它必須可區分。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestScanLegacyWindow -v`
Expected: FAIL — 簽章不符（編譯失敗）

- [ ] **Step 3: Write minimal implementation**

`scanLegacyWindow` 回傳加 `scanned`：NotExist 與各 error 路徑回 false，掃描完整走完回 true。三個 production caller 最小適配（`window, _, werr :=`／`legacy, _, lerr :=`）——行為不變，scanned 留給 Task 3。改完跑 `rg 'scanLegacyWindow\(' -g '*.go'` 確認無漏網（應仍 8 處、全部三回傳值）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./... && go vet ./... && go test . -run 'TestScanLegacyWindow|TestTranscriptOnlyMigrationScanErrorDoesNotFreeze|TestBackfillLegacyTranscript|TestLoadTurnsBefore' -count=1`
Expected: PASS（三個 caller 路徑迴歸全綠）

- [ ] **Step 5: Commit**

```bash
git add restore.go app.go rebuild_orchestrator.go app_restore_dormant_test.go
git commit -m "feat: scanLegacyWindow 簽章擴充 scanned——NotExist 與成功零筆可區分（§5a）"
```

---

### Task 3: loadTurnsBefore — §6a 空 window 清旗標

**Files:**
- Modify: `rebuild_orchestrator.go`（合併分支＋新增 wsregistry import）
- Test: `app_legacy_transcript_test.go`、`app_registry_uncertain_test.go`（uncertain 稽核覆蓋表加 legacy_flag_clear 一列）

**Interfaces:**
- Consumes: `scanned`（Task 2）、`a.wsReg.ClearLegacyTranscript`（Task 1，經 sessionRegistry interface）。
- Produces: 合併分支在 `lerr==nil && scanned && len(legacy)==0` 時呼叫 `ClearLegacyTranscript(wsid)`：哨兵（`ErrEntryNotFound`／`ErrTombstoned`）視為良性跳過；其他錯誤（persist 失敗、uncertain latch）→ `loadTurnsBefore` 回錯。`len(legacy)>0` 照舊前綴；空 ViewStart guard 分支不變（未掃描、不清）。

- [ ] **Step 1: Write the failing test**

```go
// §6a 主測試：window 空時首載成功、旗標清除且持久化、之後不再掃描。
func TestLoadTurnsBeforeEmptyWindowClearsFlagAndStopsScanning(t *testing.T) {
	a := seedLegacyFewTurnsAppNoLegacyEvents(t, 5) // flag=true、ViewStart 非空、events.jsonl 只有 WSID turn、無無 WSID 事件
	w := "w1"
	p1, err := a.LoadTurnsBefore(w, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p1) {
		t.Fatal("window 空不該有 legacy 前綴")
	}
	if e, _ := registryOnDisk(t, a.stateDir).Get(w); e.LegacyTranscript {
		t.Fatal("成功掃描零筆後旗標應清除並持久化")
	}
	// 「不再掃描」的確定性 mutation 守門（plan gate 校正——len(p2)==len(p1)
	// 對任何 mutation 恆真，檔案破壞注入又依賴 readEnvelopeRange 吞錯行為、
	// replay reliability 票修掉後會誤紅）：追加一筆 boundary 之後的無 WSID
	// claude 事件。旗標若沒被真的清掉（或合併分支忽略 flag），第二次首載會
	// 掃到它並前綴 → 打紅；正確實作下分支被 flag gate 掉、不掃、不出現。
	f, err := os.OpenFile(filepath.Join(a.stateDir, "events.jsonl"), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event_id":"zz-late-legacy","provider":"claude","kind":"message","role":"user","text":"late-legacy"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	p2, err := a.LoadTurnsBefore(w, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p2) {
		t.Fatal("旗標清除後不得再掃 legacy window（追加的無 WSID 事件不應出現）")
	}
}

// NotExist 不清：缺檔不等於成功掃描零筆。
// 覆蓋說明（plan gate 校正）：loadTurnsBefore 自己會先 os.Open(events.jsonl)，
// NotExist 在合併分支**之前**就早退（rebuild_orchestrator.go:313-319）——本測試
// 驗的是「整條路徑對缺檔不清旗標」這個使用者可見契約，不是 scanned 的
// mutation 守門；scanned==false 的契約由 Task 2 的 unit 測試守。scanned 在
// 本 caller 的實際作用是「早退 open 與 legacy 掃描之間檔案被移除」的 TOCTOU
// 防禦窗口——無法以確定性測試打紅，依 spec §6a 凍結保留。
func TestLoadTurnsBeforeMissingEventsDoesNotClearFlag(t *testing.T) {
	a := seedLegacyFewTurnsAppNoLegacyEvents(t, 5)
	events := filepath.Join(a.stateDir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadTurnsBefore("w1", "", 20); err != nil {
		t.Fatalf("NotExist 非錯誤：%v", err)
	}
	if e, _ := registryOnDisk(t, a.stateDir).Get("w1"); !e.LegacyTranscript {
		t.Fatal("NotExist 不得清旗標")
	}
}

// scan error 不清（既有 TestLoadTurnsBeforeScanErrorPropagates 擴斷言即可，若該測試
// fixture 的 window 非空則另建本測試）：回錯且旗標仍 true。
func TestLoadTurnsBeforeScanErrorDoesNotClearFlag(t *testing.T) {
	a := seedLegacyFewTurnsAppNoLegacyEvents(t, 5)
	events := filepath.Join(a.stateDir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadTurnsBefore("w1", "", 20); err == nil {
		t.Fatal("scan error 必須回錯")
	}
	if e, _ := registryOnDisk(t, a.stateDir).Get("w1"); !e.LegacyTranscript {
		t.Fatal("scan error 不得清旗標")
	}
}

// persist error（app 層）：chmod stateDir 的失敗落在步驟 1（暫存檔建立，
// writeTempLocked 的 os.OpenFile——早於 stepWrite hook 被呼叫）→ §6a 三語意
// 之一的「回滾、flag 維持 true、可重試」。
// dirSync 不回滾＋latch 的語意由 Task 1 的 wsregistry unit 覆蓋（step hook 是
// wsregistry package 內的 test-only 接點，app 層拿不到）。root skip 保留：
// 此注入依賴權限，root 會繞過（unit 層已有確定性版本，app 層此測驗的是
// 錯誤傳播到 loadTurnsBefore 回傳值這一段）。
func TestLoadTurnsBeforeClearPersistFailureFailsLoud(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限，無法重現 persist 失敗")
	}
	a := seedLegacyFewTurnsAppNoLegacyEvents(t, 5)
	if err := os.Chmod(a.stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.stateDir, 0o700) })
	if _, err := a.LoadTurnsBefore("w1", "", 20); err == nil {
		t.Fatal("清旗標 persist 失敗必須回錯（fail loud）")
	}
	if err := os.Chmod(a.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadTurnsBefore("w1", "", 20); err != nil {
		t.Fatalf("修復後重試必須成功：%v", err)
	}
	if e, _ := registryOnDisk(t, a.stateDir).Get("w1"); e.LegacyTranscript {
		t.Fatal("重試成功後旗標應清除")
	}
}
```

既有 `TestLoadTurnsBeforeEmptyViewStartDoesNotPrefixLegacy` 擴一條斷言：呼叫後
`registryOnDisk` 讀 flag 仍 true（空 ViewStart 未掃描、不清）。

（主測試追加事件的形狀需與 `hasLegacyText` 的實際判準對齊——動手前先讀該 helper：
若以「無 WSID」判定則如上即可，若以特定 text 判定則把 text 換成它認得的字樣。）

helper `seedLegacyFewTurnsAppNoLegacyEvents(t, n)`：沿用 `seedLegacyFewTurnsApp` 手法但
events.jsonl **不放**無 WSID 事件（只有 n 個 WSID turn）；registry entry flag=true、
ViewStart 非空（"00" 前綴慣例）；`a.wsReg = registryOnDisk(t, dir)` 接線（既有慣例）。
注意：本 task 測試會經 `a.wsReg` 寫入（清旗標），而 `registryOnDisk` 每次呼叫開新
Store——斷言一律用**重新開一份** `registryOnDisk(t, dir)` 讀磁碟，不讀 `a.wsReg`
的記憶體狀態，避免測到快取。persist 失敗測試中 `a.wsReg` 的 Store 與斷言用的 Store
是不同實例，但回滾語意由 store 自身保證、磁碟是唯一權威，斷言仍成立。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run 'TestLoadTurnsBeforeEmptyWindowClearsFlagAndStopsScanning|TestLoadTurnsBeforeMissingEventsDoesNotClearFlag|TestLoadTurnsBeforeScanErrorDoesNotClearFlag|TestLoadTurnsBeforeClearPersistFailureFailsLoud|TestRegistryUncertainAuditCoversStubbableWrites' -v`
Expected: FAIL — 目前不清旗標（主測試「不再掃描」段紅）；其餘分支測試在主實作前也紅或編譯錯；uncertain 覆蓋表新列在接線前紅

- [ ] **Step 3: Write minimal implementation**

合併分支改寫：

```go
if !hasOlder && a.wsReg != nil {
	if e, ok := a.wsReg.Get(wsid); ok && e.LegacyTranscript && e.ViewStartEventID != "" {
		legacy, scanned, lerr := scanLegacyWindow(a.eventsPath(), e.Provider, e.ViewStartEventID)
		if lerr != nil {
			return nil, lerr
		}
		if len(legacy) > 0 {
			out = append(legacy, out...)
		} else if scanned {
			// §6a：成功掃描確定零筆才清旗標（scanned 擋「早退 open 與掃描之間
			// 檔案被移除」的 TOCTOU 窗口；NotExist 主路徑在本函式開頭已早退）。
			// persist 失敗 fail loud——registry 寫不進去時掩蓋只會更晚發現。
			if cerr := a.wsReg.ClearLegacyTranscript(wsid); cerr != nil &&
				!errors.Is(cerr, wsregistry.ErrEntryNotFound) && !errors.Is(cerr, wsregistry.ErrTombstoned) {
				return nil, a.noteRegistryUncertainErr("legacy_flag_clear", wsid, cerr)
			}
		}
	}
}
```

doc 註解引 §6a 四分支與 owner 裁決。**新 registry 寫入點依既有慣例接線（plan gate P2）**：
錯誤經 `a.noteRegistryUncertainErr("legacy_flag_clear", wsid, cerr)`（app.go:734-751 doc
明定「任一 registry 寫入」的統一稽核，既有七個呼叫點），並在
`TestRegistryUncertainAuditCoversStubbableWrites`（app_registry_uncertain_test.go）加一列
（Task 1 補的 stub 方法正好可驅動）、該測試檔的「呼叫點數」說明一併更新。新列的
fixture 三前提（plan gate 實測確認可落地）：stub entry 的 `LegacyTranscript=true` 且
`ViewStartEventID` 非空（如 "0000000000"）、events.jsonl 存在（`newTestApp`＋
`mustCreate` 之後即存在）、window 為空——三者齊備才會走到清旗標呼叫點，缺一列就
驅動不起來，不得因此默默略過該列。哨兵良性
跳過是否比照 `noteRegistryWriteResult`（app.go:1191-1197）留 `session_metadata_write_skipped`
——先讀該 helper 的既有消費者再決定，對齊慣例、不強加。
`import`：`errors` 已有（rebuild_orchestrator.go:8）；**`wsregistry` 需新增**（現行
imports 無它——plan gate 校正）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run 'TestLoadTurnsBefore|TestRegistryUncertainAuditCoversStubbableWrites' -count=1 -v`
Expected: PASS（含既有 7 個、新增 4 個、uncertain 覆蓋表新列）

- [ ] **Step 5: Commit**

```bash
git add rebuild_orchestrator.go app_legacy_transcript_test.go app_registry_uncertain_test.go
git commit -m "feat(app): loadTurnsBefore 空 window 清旗標——§6a 四分支、persist 失敗 fail loud"
```

---

### Task 4: backfill 失敗具名 audit

**Files:**
- Modify: `app.go`（loadSessionRegistry 的 backfillLegacyTranscript 錯誤分支，約 :1704）
- Modify: `app_registry_uncertain_test.go`（呼叫點數說明＋「具體 Store、尚無跨 package 注入」覆蓋缺口註記——新呼叫點吃具體 Store 不可 stub，只更新註記、不加表列）
- Test: `app_restore_dormant_test.go`

**Interfaces:**
- Produces: 錯誤分支依 spec §4 順序（owner review 2026-08-24 P2）：(1) 先
  `err = a.noteRegistryUncertainErr("legacy_transcript_backfill", "", err)`——既有契約
  （app.go:734）要求任何 registry 寫入回 `ErrRegistryUncertain` 都走統一稽核，非
  uncertain 錯誤原樣通過；(2) 再 `a.audit("legacy_transcript_backfill_failed", map[string]any{"error": err.Error()})`
  （對齊 `resume_backfill_failed` app.go:1749 慣例）；(3) 最後 `noteStartupBlocker`。
  成功路徑不發 audit（§4：結果可由 registry 直接觀測）。
- 同步更新 `app_registry_uncertain_test.go` 的呼叫點數說明與「具體 Store、尚無跨
  package 注入」已知覆蓋缺口註記（新增 `legacy_transcript_backfill` 呼叫點）。

**測試 helper 事實（plan gate 查證，防混淆）**：Task 4 測試用的 `newTestAppAt`
**有**開 audit.jsonl（app_restore_dormant_test.go:50-57 `a.auditF = af`）——「預設不開
audit、需 `enableAudit`」的是另一個 helper `newTestApp`（見 app_test.go enableAudit
doc）。本 task 不需要也不得多呼叫 `enableAudit`（會重複開檔）。

- [ ] **Step 1: Write the failing test**

```go
// §4 失敗軌跡：backfill 失敗除 startup blocker 外要留 audit.jsonl 持久軌跡。
// fixture 用多候選（確定性觸發 fail loud），經 restoreSessions 走真實接線。
func TestBackfillLegacyTranscriptFailureLeavesAudit(t *testing.T) {
	dir := seedMigratedTwoClaudeFixture(t)
	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatalf("backfill 失敗不阻擋啟動：%v", err)
	}
	if !auditHas(t, dir, "legacy_transcript_backfill_failed") {
		t.Fatal("backfill 失敗必須留下具名 audit 事件")
	}
}

// 反向：成功路徑不發該事件。
func TestBackfillLegacyTranscriptSuccessNoFailureAudit(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t)
	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if !registryOnDisk(t, dir).LegacyTranscriptBackfilled() {
		t.Fatal("前提：backfill 應成功")
	}
	if auditHas(t, dir, "legacy_transcript_backfill_failed") {
		t.Fatal("成功路徑不得發失敗 audit")
	}
}
```

（`auditHas` 為既有 helper（app_replayindex_test.go 有用例）；若簽章不同依既有定義調整呼叫。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run 'TestBackfillLegacyTranscriptFailureLeavesAudit|TestBackfillLegacyTranscriptSuccessNoFailureAudit' -v`
Expected: FAIL — 失敗測試紅（無 audit 事件）；反向測試綠

- [ ] **Step 3: Write minimal implementation**

錯誤分支依 Interfaces 的三步順序改寫：`noteRegistryUncertainErr` → 具名 audit → `noteStartupBlocker`；並更新 app_registry_uncertain_test.go 的呼叫點數說明與覆蓋缺口註記。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestBackfillLegacyTranscript -count=1`
Expected: PASS（含既有 7 個）

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go app_registry_uncertain_test.go
git commit -m "feat(app): backfill 失敗補具名 audit（legacy_transcript_backfill_failed，§4 失敗軌跡）"
```

---

### Task 5: 迴歸

- [ ] **Step 1: Targeted 迴歸**

Run: `go test . -run 'TestScanLegacyWindow|TestTranscriptOnly|TestBackfillLegacyTranscript|TestLoadTurnsBefore|TestLegacyTranscriptEndToEndHydrate|TestLegacyJournalWithoutWSIDAttributes' -count=1 -v`
Expected: 全綠

- [ ] **Step 2: 全套迴歸**

Run: `go build ./... && go vet ./... && go test -race ./internal/wsregistry/ -count=1 && go test . -count=1 -timeout 900s`
Expected: 全綠（牆鐘不穩定測試依 memory 具名清單，紅則單獨重跑判定）

- [ ] **Step 3: Commit（若 Step 1-2 產生任何修正）**

無修正則本 task 不產生 commit。

---

## Self-Review

**1. Spec coverage（§6a／§4／§5a／§3）：**
- §6a 四分支：成功零筆清＋不再掃描 mutation 守門（Task 3 主測試，追加事件探針）、空 ViewStart 不清（既有 I1 測試擴斷言）、NotExist 不清（**聯合覆蓋**：loadTurnsBefore 層驗整條路徑早退不清（Task 3）＋`scanned==false` 契約由 Task 2 unit 守；scanned 在此 caller 是 TOCTOU 防禦、無確定性打紅手段，plan gate 已審）、scan error 不清（Task 3）、persist 失敗三語意（Task 1 unit：stepWrite 回滾／dirSync 不回滾＋latch／latch 後拒絕；Task 3 app 層：stepWrite 類失敗傳播到 loadTurnsBefore 回錯＋重試）✓
- §6a 寫入口（冪等／哨兵／回滾／latch 枚舉）：Task 1 ✓；不動 boundary：Task 1 斷言 ✓
- §5a scanned 簽章＋NotExist 區分＋既有 caller 行為不變：Task 2 ✓
- §4 失敗軌跡（統一稽核 → 具名 audit → blocker 三步順序）正反向：Task 4 ✓
- §3 語意例外／§4 措辭：spec 修訂已入最終 snapshot cdf2dbe，無 code 對應 ✓

**2. Type consistency：**
- `scanLegacyWindow → ([]contract.Envelope, bool, error)`：Task 2 定義、Task 3 消費一致；Task 2 過渡期三個 production caller 以 `_` 忽略 scanned。
- `ClearLegacyTranscript(wsid string) error`：Task 1 定義（Store＋interface）、Task 3 經 `a.wsReg` 消費一致。

**3. 已知風險與邊界：**
- fsync latch 枚舉表的順序問題（latch vs 冪等）已在 Task 1 Step 1 明文指示以 `SetLayout` 既有慣例為準，不自創順序——實作者需先讀 store.go:519-521 確認。
- Task 3 的清除發生在讀路徑：spec §6a 已凍結此取捨（含 persist 失敗 fail loud 的理由）。
- `seedLegacyFewTurnsAppNoLegacyEvents` 是新 helper：與既有 `seedLegacyFewTurnsApp` 的差異只有「不放無 WSID 事件」，實作時若能以參數化共用則共用、不強求（fixture 形狀可讀性優先，final review 曾裁決反對過度參數化 seeder）。

**整合審查點**：Task 1-3 完成後（簽章鏈＋清旗標四分支）做一次；Task 4-5 收尾後全鏈驗證由 Task 5 全套迴歸涵蓋，小票不另開第二次整合審查。
