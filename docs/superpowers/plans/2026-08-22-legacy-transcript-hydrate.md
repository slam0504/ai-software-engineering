# Legacy transcript 首次 hydrate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** legacy-migrated WSID 首次 hydrate 時顯示 pre-migration 的舊 transcript，與 post-migration 新 turn 正確合併、排序、分頁。

**Architecture:** 明確的持久化標記 `Entry.LegacyTranscript` 識別擁有舊 transcript 的 WSID（不靠推導）；backend `loadTurnsBefore` 在「最舊 WSID turn 頁」前綴 legacy window（只取無 WSID 事件，避免與 turn index 重複）；一次性 backfill 為既有升級使用者補標記；error-returning `scanLegacyWindow` 三路徑共用，I/O 錯誤一律 fail loud 不落 marker。frontend 零改動、`RestoreViews` 保留不動。

**Tech Stack:** Go（internal/wsregistry、app.go、rebuild_orchestrator.go、restore.go、internal/replayindex），Go testing。

**Spec:** `docs/superpowers/specs/2026-08-22-legacy-transcript-hydrate-design.md`（commit d330c9d）

## Global Constraints

- schema 變更一律 additive、不升 `schema_version`：舊檔缺欄位即 `false`（沿用 `ResumeBackfilled` 慣例，store.go:86 doc）。
- marker 與 entry 標記同一次持久化交易寫入，失敗全回滾（沿用 `BackfillResume` 的 `persistOrRollback` 模式）。
- fail loud：任何 I/O／掃描錯誤不得誤判成「零候選／無 legacy」而落 marker；回 error、可重試。
- legacy window 只取 `WorkspaceSessionID == ""`（pre-migration 事件），避免與 turn index 的 WSID 事件重複。
- `RestoreViews`、`replayViewWindow`、turn index §3.5.9（無 WSID 不入 index）維持不變；frontend 零改動。
- 台灣用語書面中文；commit message 依 repo 慣例。

---

## File Structure

- `internal/wsregistry/store.go` — 加 `Entry.LegacyTranscript`、`fileFormat.LegacyTranscriptBackfilled`、`ResetView` 清標記、`BackfillLegacyTranscript` marker method。
- `internal/wsregistry/migrate.go` — `LegacyEntry.HasLegacyTranscript`、`Migrate` 依此設 flag。
- `restore.go` — 新增 `scanLegacyWindow`（error-returning、只取無 WSID）。
- `app.go` — `legacyEntries()` 改用 `scanLegacyWindow`＋填 `HasLegacyTranscript`；新增 `backfillLegacyTranscript(store)`＋接進 `loadSessionRegistry`。
- `internal/replayindex/index.go` — `TurnsBefore` 增加 `hasOlder` 回傳。
- `rebuild_orchestrator.go` — `loadTurnsBefore` 合併 legacy window（cursor 早退、hasOlder 前綴、排序）。
- 測試：各 `*_test.go` 同套件；端對端在新 `app_legacy_transcript_test.go`。

---

### Task 1: wsregistry schema — Entry.LegacyTranscript ＋ file marker

**Files:**
- Modify: `internal/wsregistry/store.go:67-76`（Entry）、`internal/wsregistry/store.go:90-95`（fileFormat）
- Test: `internal/wsregistry/store_test.go`

**Interfaces:**
- Produces: `Entry.LegacyTranscript bool`（json `legacy_transcript`）、`fileFormat.LegacyTranscriptBackfilled bool`（json `legacy_transcript_backfilled`）；`Store.LegacyTranscriptBackfilled() bool` getter。

- [ ] **Step 1: Write the failing test**（schema round-trip，舊檔預設 false）

```go
func TestLegacyTranscriptSchemaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace-sessions.json")
	old := `{"schema_version":2,"entries":{"w1":{"wsid":"w1","provider":"claude"}},"migrated":true}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.LegacyTranscriptBackfilled() {
		t.Fatal("舊檔缺 marker 應預設 false")
	}
	e, _ := s.Get("w1")
	if e.LegacyTranscript {
		t.Fatal("舊 entry 缺欄位應預設 false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run TestLegacyTranscriptSchemaRoundTrip -v`
Expected: FAIL — `s.LegacyTranscriptBackfilled undefined` / `e.LegacyTranscript undefined`（編譯失敗）

- [ ] **Step 3: Write minimal implementation**

`Entry`（store.go:67-76）加欄位（緊接 `ViewStartEventID` 之後）：`LegacyTranscript bool` json tag `legacy_transcript`。
`fileFormat`（store.go:90-95）加欄位：`LegacyTranscriptBackfilled bool` json tag `legacy_transcript_backfilled`。
新增 getter（緊鄰 `ResumeBackfilled()`），s.mu 保護回傳 `s.file.LegacyTranscriptBackfilled`。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wsregistry/ -run TestLegacyTranscriptSchemaRoundTrip -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/store.go internal/wsregistry/store_test.go
git commit -m "feat(wsregistry): 加 Entry.LegacyTranscript 與 file-level backfilled marker（additive schema）"
```

---

### Task 2: wsregistry — Migrate 依 HasLegacyTranscript 設 flag

**Files:**
- Modify: `internal/wsregistry/migrate.go:11-14`（LegacyEntry）、`internal/wsregistry/migrate.go:72-79`（Entry 建構）
- Test: `internal/wsregistry/migrate_test.go`

**Interfaces:**
- Consumes: `Entry.LegacyTranscript`（Task 1）。
- Produces: `LegacyEntry.HasLegacyTranscript bool`；`Migrate` 對每個遷移 entry 設 `LegacyTranscript = le.HasLegacyTranscript`。

- [ ] **Step 1: Write the failing test**

```go
func TestMigrateSetsLegacyTranscriptFlag(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
		"codex":  {ResumeSessionID: "sess-x", HasLegacyTranscript: false},
	}
	n := 0
	out, err := Migrate(s, legacy, func() string { n++; return fmt.Sprintf("w%d", n) })
	if err != nil {
		t.Fatal(err)
	}
	byProv := map[string]Entry{}
	for _, e := range out {
		byProv[e.Provider] = e
	}
	if !byProv["claude"].LegacyTranscript {
		t.Fatal("有 legacy window 的 entry 應設 LegacyTranscript=true")
	}
	if byProv["codex"].LegacyTranscript {
		t.Fatal("resume-only（無 window）的 entry 不得設 LegacyTranscript")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run TestMigrateSetsLegacyTranscriptFlag -v`
Expected: FAIL — `LegacyEntry.HasLegacyTranscript undefined`（編譯失敗）

- [ ] **Step 3: Write minimal implementation**

`LegacyEntry`（migrate.go:11）加 `HasLegacyTranscript bool` 欄位。
`Migrate` 的 Entry 建構（migrate.go:72）在 `ViewStartEventID` 後加 `LegacyTranscript: le.HasLegacyTranscript,`。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wsregistry/ -run TestMigrateSetsLegacyTranscriptFlag -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/migrate.go internal/wsregistry/migrate_test.go
git commit -m "feat(wsregistry): Migrate 依 LegacyEntry.HasLegacyTranscript 設 entry flag"
```

---

### Task 3: wsregistry — ResetView 清 LegacyTranscript

**Files:**
- Modify: `internal/wsregistry/store.go:405-409`（ResetView）
- Test: `internal/wsregistry/store_test.go`

**Interfaces:**
- Consumes: `Entry.LegacyTranscript`（Task 1）。
- Produces: `ResetView` 於同一 `mutate`（原子 persist）內清 `LegacyTranscript=false`。

- [ ] **Step 1: Write the failing test**

```go
func TestResetViewClearsLegacyTranscript(t *testing.T) {
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
	if err := s.ResetView("w1", "hiwater"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("w1")
	if e.LegacyTranscript {
		t.Fatal("前移 boundary 後 LegacyTranscript 必須清除（新 view 世代不含 legacy）")
	}
	if e.ViewStartEventID != "hiwater" {
		t.Fatalf("boundary 應前移：%q", e.ViewStartEventID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run TestResetViewClearsLegacyTranscript -v`
Expected: FAIL — `LegacyTranscript` 仍為 true

- [ ] **Step 3: Write minimal implementation**

`ResetView`（store.go:405）的 mutate closure 內加一行 `e.LegacyTranscript = false`（與既有 `e.ViewStartEventID, e.ResumeSessionID = viewStartEventID, ""` 同一 closure）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wsregistry/ -run TestResetViewClearsLegacyTranscript -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/store.go internal/wsregistry/store_test.go
git commit -m "feat(wsregistry): ResetView 前移 boundary 同交易清 LegacyTranscript"
```

---

### Task 4: wsregistry — BackfillLegacyTranscript（marker＋entries 原子回滾）

**Files:**
- Modify: `internal/wsregistry/store.go`（緊鄰 `BackfillResume`）
- Test: `internal/wsregistry/store_test.go`

**Interfaces:**
- Consumes: `Entry.LegacyTranscript`、`fileFormat.LegacyTranscriptBackfilled`（Task 1）。
- Produces: `Store.BackfillLegacyTranscript(wsids []string) error`——把 `wsids` 內 live 且未 tombstone 的 entry 標記 `LegacyTranscript=true`，並設 `LegacyTranscriptBackfilled=true`，同一 persist；失敗回滾。空候選仍設 marker。

- [ ] **Step 1: Write the failing test**

```go
func TestBackfillLegacyTranscriptAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err != nil {
		t.Fatal(err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("marker 應落盤")
	}
	e, _ := s.Get("w1")
	if !e.LegacyTranscript {
		t.Fatal("候選 entry 應被標記")
	}
	s2, _ := Open(filepath.Join(dir, "ws.json"))
	if !s2.LegacyTranscriptBackfilled() {
		t.Fatal("marker 未持久化")
	}
	e2, _ := s2.Get("w1")
	if !e2.LegacyTranscript {
		t.Fatal("entry 標記未持久化")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run TestBackfillLegacyTranscriptAtomic -v`
Expected: FAIL — `s.BackfillLegacyTranscript undefined`

- [ ] **Step 3: Write minimal implementation**（仿 `BackfillResume` 的 `persistOrRollback` 模式）

新增方法：取 s.mu；對每個 wsid，跳過不存在或 `RemovedAt != ""` 的 entry，其餘存舊值到 `old` map 後設 `e.LegacyTranscript = true` 寫回；記 `oldMarker`，設 `s.file.LegacyTranscriptBackfilled = true`；`return s.persistOrRollback(func(){ 還原 old entries 與 oldMarker })`。doc 說明：marker 與 entry 標記同生共死、跳過 tombstone、空候選仍設 marker（代表已檢查過）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wsregistry/ -run TestBackfillLegacyTranscriptAtomic -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/store.go internal/wsregistry/store_test.go
git commit -m "feat(wsregistry): BackfillLegacyTranscript——候選標記＋marker 原子回滾"
```

---

### Task 5: scanLegacyWindow（error-returning、只取無 WSID）

**Files:**
- Modify: `restore.go`（緊鄰 `replayViewWindow`，restore.go:167）
- Test: `app_restore_dormant_test.go`（同 package main）

**Interfaces:**
- Produces: `scanLegacyWindow(eventsPath, provider, viewStart string) ([]contract.Envelope, error)`——provider 相符、`EventID > viewStart`、排除 `Scope=="workspace"`／`Purpose=="spec_assist"`、只保留 `WorkspaceSessionID == ""`；開檔失敗（非 NotExist）或 `Scanner.Err()` 非 nil → 回 error；檔案不存在 → `(nil, nil)`；malformed 單行跳過。

- [ ] **Step 1: Write the failing test**

```go
func TestScanLegacyWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	lines := []string{
		`{"event_id":"e1","provider":"claude","kind":"message","text":"legacy-old"}`,
		`{"event_id":"e2","provider":"claude","kind":"message","text":"legacy-in","workspace_session_id":""}`,
		`{"event_id":"e3","provider":"claude","kind":"message","text":"post","workspace_session_id":"w1"}`,
		`{"event_id":"e4","provider":"codex","kind":"message","text":"other"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanLegacyWindow(path, "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].EventID != "e1" || got[1].EventID != "e2" {
		t.Fatalf("只應取無 WSID 的 claude 事件：%+v", got)
	}
	if envs, err := scanLegacyWindow(filepath.Join(dir, "nope.jsonl"), "claude", ""); err != nil || envs != nil {
		t.Fatalf("檔案不存在應回 (nil,nil)：%v %v", envs, err)
	}
	if _, err := scanLegacyWindow(dir, "claude", ""); err == nil {
		t.Fatal("開檔失敗必須回 error，不得靜默回 nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestScanLegacyWindow -v`
Expected: FAIL — `scanLegacyWindow undefined`

- [ ] **Step 3: Write minimal implementation**

以 `replayViewWindow`（restore.go:167）為藍本新增 `scanLegacyWindow`，差異：(1) 回 `([]contract.Envelope, error)`；(2) `os.Open` 失敗時 `os.IsNotExist` 回 `(nil,nil)`、否則回 wrapped error；(3) 過濾條件加 `e.WorkspaceSessionID != "" { continue }`；(4) 迴圈後 `if err := sc.Err(); err != nil { return nil, ... }`。其餘（provider／EventID／scope／viewStart 過濾、malformed 跳過、buffer 上限）與 `replayViewWindow` 一致。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestScanLegacyWindow -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add restore.go app_restore_dormant_test.go
git commit -m "feat: scanLegacyWindow——error-returning、只取無 WSID 的 legacy window"
```

---

### Task 6: legacyEntries 改用 scanLegacyWindow ＋填 HasLegacyTranscript

**Files:**
- Modify: `app.go:1274-1291`（legacyEntries）
- Test: `app_restore_dormant_test.go`

**Interfaces:**
- Consumes: `scanLegacyWindow`（Task 5）、`LegacyEntry.HasLegacyTranscript`（Task 2）。
- Produces: `legacyEntries()` 用 `scanLegacyWindow` 判斷 window、填 `HasLegacyTranscript`；掃描錯誤時回 error（呼叫端 loadSessionRegistry:1613 已 return，故 Migrate 不執行、marker 不落盤）。

- [ ] **Step 1: Write the failing test**（transcript-only migration＋scan error 阻止遷移）

```go
func TestLegacyEntriesScanErrorAbortsMigration(t *testing.T) {
	dir := seedLegacyJournalFixture(t)
	a := newTestAppAt(t, dir)
	events := filepath.Join(dir, "events.jsonl")
	backup := events + ".bak"
	if err := os.Rename(events, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.legacyEntries(); err == nil {
		t.Fatal("scanLegacyWindow 失敗時 legacyEntries 必須回 error（不得靜默空 map）")
	}
	os.Remove(events)
	if err := os.Rename(backup, events); err != nil {
		t.Fatal(err)
	}
	le, err := a.legacyEntries()
	if err != nil {
		t.Fatal(err)
	}
	if !le["claude"].HasLegacyTranscript {
		t.Fatal("transcript-only fixture 應判定 HasLegacyTranscript=true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestLegacyEntriesScanErrorAbortsMigration -v`
Expected: FAIL — 目前用 `replayViewWindow` 吞錯回空，不回 error

- [ ] **Step 3: Write minimal implementation**

`legacyEntries()`（app.go:1274）改寫：迴圈內先 `window, werr := scanLegacyWindow(a.eventsPath(), p, e.ViewStartEventID)`，`werr != nil` 直接 `return nil, werr`；`hasTranscript := len(window) > 0`；跳過條件改為 `e.ResumeSessionID == "" && e.TaskID == "" && !hasTranscript`；`out[p]` 的 `LegacyEntry` 加 `HasLegacyTranscript: hasTranscript`。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestLegacyEntriesScanErrorAbortsMigration -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go
git commit -m "fix(app): legacyEntries 改用 scanLegacyWindow——掃描失敗阻止 migration、不落 marker"
```

---

### Task 7: backfillLegacyTranscript（app，五條件精確比對）

**Files:**
- Modify: `app.go`（新增 `backfillLegacyTranscript`，緊鄰 `backfillResumeFromLegacy` app.go:1713）、`loadSessionRegistry`（接線，`backfillResumeFromLegacy(store)` 呼叫點之後）
- Test: `app_restore_dormant_test.go`；測試存取器 `wsRegForTest`（app_test.go 或既有 helper 檔）

**Interfaces:**
- Consumes: `scanLegacyWindow`（Task 5）、`Store.BackfillLegacyTranscript`（Task 4）、`Store.LegacyTranscriptBackfilled`（Task 1）、`Store.Live()`、`a.restore.Get`。
- Produces: `backfillLegacyTranscript(store *wsregistry.Store) error`——`!LegacyTranscriptBackfilled` 時，以 restore.json 快照對每 provider 精確比對五條件找恰一候選；零候選略過、多候選 fail loud（marker 不落盤）、scan error fail loud；成功則 `store.BackfillLegacyTranscript(候選)`。在 `loadSessionRegistry` 的 `backfillResumeFromLegacy(store)` 之後呼叫。`wsRegForTest() *wsregistry.Store` 測試存取器。

- [ ] **Step 1: Write the failing test**（多候選 fail loud、marker 不落盤；恰一候選成功）

```go
func TestBackfillLegacyTranscriptMultiCandidateFailsLoud(t *testing.T) {
	dir := seedMigratedTwoClaudeFixture(t)
	a := newTestAppAt(t, dir)
	store := a.wsRegForTest()
	if err := a.backfillLegacyTranscript(store); err == nil {
		t.Fatal("多候選必須 fail loud")
	}
	if store.LegacyTranscriptBackfilled() {
		t.Fatal("多候選時 marker 不得落盤（可重試）")
	}
}

func TestBackfillLegacyTranscriptSingleCandidate(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t)
	a := newTestAppAt(t, dir)
	store := a.wsRegForTest()
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatal(err)
	}
	if !store.LegacyTranscriptBackfilled() {
		t.Fatal("成功後 marker 應落盤")
	}
	var flagged int
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("恰一候選應標記一個：%d", flagged)
	}
}
```

fixture helper（Step 3 一併建立，沿用 `seedLegacyJournalFixture` 手法）：
- `seedMigratedLegacyClaudeFixture`：events.jsonl 有 claude 無 WSID 事件；restore.json claude entry 的 ViewStart=V；workspace-sessions.json migrated=true、一個 claude live entry ViewStart=V、無 legacy_transcript、legacy_transcript_backfilled=false。
- `seedMigratedTwoClaudeFixture`：同上但兩個 claude live entry 的 ViewStart 都=V（多候選）。
- `wsRegForTest() *wsregistry.Store { return a.wsReg }`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestBackfillLegacyTranscript -v`
Expected: FAIL — `a.backfillLegacyTranscript undefined`

- [ ] **Step 3: Write minimal implementation**

新增 `backfillLegacyTranscript`：`store.LegacyTranscriptBackfilled() || a.restore == nil` early return nil。對每個 `legacyProviders` 的 p：`re := a.restore.Get(p)`；`window, werr := scanLegacyWindow(a.eventsPath(), p, re.ViewStartEventID)`，`werr != nil` return werr（不落 marker）；`len(window)==0` continue；掃 `store.Live()` 收集 `e.Provider==p && e.ViewStartEventID==re.ViewStartEventID` 的 WSID 到 `match`；`switch len(match)`：0 略過、1 加入 candidates、default `return fmt.Errorf(...多候選不猜...)`。最後 `return store.BackfillLegacyTranscript(candidates)`。
接線 `loadSessionRegistry`：`backfillResumeFromLegacy(store)` 之後加 `if err := a.backfillLegacyTranscript(store); err != nil { a.noteStartupBlocker("...legacy transcript 標記補寫失敗..." + err.Error()) }`。
存取器 `wsRegForTest`。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestBackfillLegacyTranscript -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go app_test.go
git commit -m "feat(app): backfillLegacyTranscript——restore.json 快照五條件精確比對、多候選 fail loud"
```

---

### Task 8: replayindex — TurnsBefore 增加 hasOlder

**Files:**
- Modify: `internal/replayindex/index.go:453-475`（TurnsBefore）
- Test: `internal/replayindex/index_test.go`；同套件既有直接呼叫 `TurnsBefore` 的測試改接三回傳值

**Interfaces:**
- Produces: `TurnsBefore(wsid, beforeEventID string, n int) (recs []TurnRecord, hasOlder bool, err error)`——`hasOlder` 表示此頁回傳之後還有更舊的 WSID turn（可分頁總數 > 回傳數）。

- [ ] **Step 1: Write the failing test**

```go
func TestTurnsBeforeReportsHasOlder(t *testing.T) {
	idx := newTestIndexWithTurns(t, "w1", 25)
	recs, hasOlder, err := idx.TurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 20 || !hasOlder {
		t.Fatalf("25 turn 首載 20 個、應 hasOlder=true：n=%d older=%v", len(recs), hasOlder)
	}
	oldestFirst := recs[0].FirstEventID
	recs2, hasOlder2, err := idx.TurnsBefore("w1", oldestFirst, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 5 || hasOlder2 {
		t.Fatalf("剩 5 turn、應 hasOlder=false：n=%d older=%v", len(recs2), hasOlder2)
	}
}
```

（`newTestIndexWithTurns` 若既有測試無對應 helper，於此建立：建 index、Observe 25 個 user message＋terminal state_change turn，或直接寫 turn file。沿用 index_test.go 既有建法。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replayindex/ -run TestTurnsBeforeReportsHasOlder -v`
Expected: FAIL — `TurnsBefore` 回兩值、簽章不符（編譯失敗）

- [ ] **Step 3: Write minimal implementation**

`TurnsBefore`（index.go:453）改簽章為 `([]TurnRecord, bool, error)`：`readTurnFileLocked` 錯誤回 `nil, false, err`；`beforeEventID==""` 回 `capTail(all, n), len(all) > n, nil`；找不到 cut（`cut <= 0`）回 `nil, false, nil`；否則回 `capTail(all[:cut], n), cut > n, nil`。同步改同套件既有直接呼叫 `TurnsBefore` 的測試為三回傳值（`grep -n '\.TurnsBefore(' internal/replayindex/`）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replayindex/ -count=1`
Expected: PASS（含新測試與改過的既有測試）

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/index.go internal/replayindex/index_test.go
git commit -m "feat(replayindex): TurnsBefore 增加 hasOlder 回傳（最舊 turn 頁判定）"
```

---

### Task 9: loadTurnsBefore 合併 legacy window

**Files:**
- Modify: `rebuild_orchestrator.go:289-345`（loadTurnsBefore）
- Test: `app_legacy_transcript_test.go`（新）

**Interfaces:**
- Consumes: `TurnsBefore`（Task 8，三回傳值）、`scanLegacyWindow`（Task 5）、`Entry.LegacyTranscript`（Task 1）、`a.wsReg.Get`（entry 查 provider／ViewStart，回 `(Entry, bool)`）。
- Produces: `loadTurnsBefore` 在 `entry.LegacyTranscript && hasOlder==false` 的頁前綴 legacy window，依 event_id 排序、去重；`beforeEventID!="" && len(recs)==0` 早退回 `(nil,nil)`。

- [ ] **Step 1: Write the failing test**（跨頁：legacy + 25 turns 三頁，reviewer 指定）

```go
func TestLoadTurnsBeforeMergesLegacyAtOldestPage(t *testing.T) {
	a := seedLegacyPlus25TurnsApp(t)
	w := "w1"
	p1, err := a.LoadTurnsBefore(w, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p1) {
		t.Fatal("首載（還有更舊 turn）不得含 legacy")
	}
	cursor := p1[0].EventID
	p2, err := a.LoadTurnsBefore(w, cursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLegacyText(p2) {
		t.Fatal("最舊 turn 頁必須前綴 legacy")
	}
	if !ascendingByEventID(p2) {
		t.Fatal("合併後必須 event_id 遞增")
	}
	if p2[0].WorkspaceSessionID != "" {
		t.Fatal("legacy（無 WSID）應排在最前")
	}
	p3, err := a.LoadTurnsBefore(w, p2[0].EventID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(p3) != 0 {
		t.Fatalf("legacy cursor 應回空、停止分頁：%d", len(p3))
	}
	assertAllTurnsPresent(t, append(append([]contract.Envelope{}, p1...), p2...), 25)
}
```

（helper `seedLegacyPlus25TurnsApp`／`hasLegacyText`／`ascendingByEventID`／`assertAllTurnsPresent` 於此建立。fixture：migrated registry、claude WSID w1、LegacyTranscript=true、ViewStart=V；events.jsonl 有 V 之後的無 WSID legacy 事件，再有 25 個帶 w1 的完整 turn；index 已建 25 turn record。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestLoadTurnsBeforeMergesLegacyAtOldestPage -v`
Expected: FAIL — 目前不合併 legacy，p2 無 legacy 文字

- [ ] **Step 3: Write minimal implementation**

`loadTurnsBefore`（rebuild_orchestrator.go）改寫：`TurnsBefore` 接三回傳值；緊接加 `if beforeEventID != "" && len(recs) == 0 { return nil, nil }`；既有 readEnvelopeRange 迴圈與 open-turn tail 邏輯保留（合成單一出口 `out`）；在最終 return 前加 `if !hasOlder { if e, ok := a.wsReg.Get(wsid); ok && e.LegacyTranscript { legacy, lerr := scanLegacyWindow(a.eventsPath(), e.Provider, e.ViewStartEventID); if lerr != nil { return nil, lerr }; out = append(legacy, out...) } }`；最後 `sort.SliceStable(out, func(i,j int) bool { return out[i].EventID < out[j].EventID })`。若既有程式在 `beforeEventID != ""` 分支提早 return，須併入此統一出口以確保排序與早退都經過（`import "sort"`）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestLoadTurnsBeforeMergesLegacyAtOldestPage -v`
Expected: PASS

- [ ] **Step 5: Add 反向測試並跑**

```go
func TestLoadTurnsBeforeNonLegacyHasNoWSIDlessEvents(t *testing.T) {
	a := seedNonLegacyApp(t)
	got, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.WorkspaceSessionID == "" {
			t.Fatal("非 legacy WSID 不得含任何無 WSID 事件（反向）")
		}
	}
}
```
Run: `go test . -run TestLoadTurnsBefore -v`
Expected: PASS（含反向）

- [ ] **Step 6: Commit**

```bash
git add rebuild_orchestrator.go app_legacy_transcript_test.go
git commit -m "feat(app): loadTurnsBefore 於最舊 turn 頁前綴 legacy window（cursor 早退、排序、去重）"
```

---

### Task 10: 端對端——legacy transcript 首次 hydrate 可見＋同 provider 多 session 不誤接

**Files:**
- Test: `app_legacy_transcript_test.go`（接續 Task 9）

**Interfaces:**
- Consumes: 全鏈（migration flag → backfill → loadTurnsBefore 合併）。

- [ ] **Step 1: Write the failing test**（全鏈已實作，作為迴歸鎖）

```go
func TestLegacyTranscriptEndToEndHydrate(t *testing.T) {
	a := seedMigratedLegacyPlusSecondClaude(t)
	p1, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLegacyText(p1) {
		t.Fatal("legacy WSID 首次 hydrate 必須顯示舊 transcript")
	}
	p2, err := a.LoadTurnsBefore("w2", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p2 {
		if e.WorkspaceSessionID == "" {
			t.Fatal("同 provider 的第二個 session 不得誤接 legacy 舊歷史（owner 否決推導的核心風險）")
		}
	}
}
```

（`seedMigratedLegacyPlusSecondClaude`：w1 為 legacy（LegacyTranscript=true、boundary 後有無 WSID transcript）；w2 為後建 claude（LegacyTranscript=false、ViewStart 為建立時高水位、之後只有 w2 自己的 WSID 事件）。）

- [ ] **Step 2: Run test to verify it passes**

Run: `go test . -run TestLegacyTranscriptEndToEndHydrate -v`
Expected: PASS（全鏈已實作；若紅則定位缺口）

- [ ] **Step 3: 既有契約迴歸**

Run: `go test . -run 'TestLegacyJournalWithoutWSIDAttributes|TestRestore' -v && go test ./internal/wsregistry/ ./internal/replayindex/ -count=1`
Expected: 全綠（RestoreViews provider-keyed 測試不破、turn index §3.5.9 不變）

- [ ] **Step 4: 全套迴歸**

Run: `go build ./... && go vet ./... && go test -race ./internal/wsregistry/ ./internal/replayindex/ -count=1 && go test . -count=1 -timeout 900s`
Expected: 全綠（牆鐘不穩定測試依 memory 具名清單，紅則單獨重跑判定）

- [ ] **Step 5: Commit**

```bash
git add app_legacy_transcript_test.go
git commit -m "test(app): legacy transcript 首次 hydrate 端對端＋同 provider 多 session 不誤接"
```

---

## Self-Review

**1. Spec coverage：**
- §3 schema：Task 1 ✓；migration flag：Task 2 ✓；legacyEntries 用 scanLegacyWindow：Task 6 ✓
- §4 backfill 五條件＋多候選 fail loud＋原子 marker：Task 4＋Task 7 ✓
- §5 合併＋cursor 早退＋hasOlder 前綴＋排序：Task 8＋Task 9 ✓；§5a scanLegacyWindow：Task 5 ✓
- §6 ResetView 清標記：Task 3 ✓
- §8 測試（migration scan error、reverse、跨頁、backfill 多候選、端對端、既有契約）：Task 6/2/9/7/10 ✓

**2. Placeholder scan：** 無 TBD／TODO；code 步驟給實際 test code 與明確 impl 指示。fixture helper 於首次使用的 Task 建立、沿用既有 `seedLegacyJournalFixture` 手法。

**3. Type consistency：**
- `TurnsBefore` → `([]TurnRecord, bool, error)`：Task 8 定義、Task 9 消費一致。
- `scanLegacyWindow(eventsPath, provider, viewStart string) ([]contract.Envelope, error)`：Task 5 定義，Task 6／7／9 消費一致。
- `Store.BackfillLegacyTranscript([]string) error`：Task 4 定義、Task 7 消費一致。
- `Entry.LegacyTranscript`／`fileFormat.LegacyTranscriptBackfilled`／`Store.LegacyTranscriptBackfilled()`：Task 1 定義，Task 2/3/4/7/9 消費一致。
- `LegacyEntry.HasLegacyTranscript`：Task 2 定義、Task 6 填入一致。

**待實作時確認的既有型別**（不阻擋 plan）：`Entry.Provider` 為 `string`（store.go:69），Task 9 傳 `e.Provider` 不需轉型；`a.wsReg.Get(wsid)` 回 `(Entry, bool)`（app.go:1169）。

**整合審查點（owner 指定）**：Task 4–7 完成後做一次整合審查（schema→migrate→scanner→backfill→接線）；Task 8–9 完成後再做一次（hasOlder→合併→排序→cursor 早退）。
