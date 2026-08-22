# Legacy transcript 首次 hydrate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** legacy-migrated WSID 首次 hydrate 時顯示 pre-migration 的舊 transcript，與 post-migration 新 turn 正確合併、排序、分頁。

**Architecture:** 明確的持久化標記 `Entry.LegacyTranscript` 識別擁有舊 transcript 的 WSID（不靠推導）；backend `loadTurnsBefore` 在「最舊 WSID turn 頁」前綴 legacy window（只取無 WSID 事件，避免與 turn index 重複）；一次性 backfill 為既有升級使用者補標記；error-returning `scanLegacyWindow` 三路徑共用，I/O 錯誤一律 fail loud 不落 marker。frontend 零改動、`RestoreViews` 保留不動。

**Tech Stack:** Go（internal/wsregistry、app.go、rebuild_orchestrator.go、restore.go、internal/replayindex），Go testing。

**Spec:** `docs/superpowers/specs/2026-08-22-legacy-transcript-hydrate-design.md`（commit d330c9d）

**Plan 修訂記錄：**
- rev3（2026-08-22，plan gate 新 P1）：darwin 上 `os.Open(目錄)` 會成功、錯誤到讀取才出現——原標成「open error」的目錄注入實際全走 `Scanner.Err()` 分支，真 open error（非 NotExist）無測試打到。Task 5／Task 6 改用 `chmod 0o000`（euid==0 skip，沿用 app_restore_dormant_test.go:190 前例）覆蓋 open 分支，三處註解措辭改與實際注入路徑相符；P2：Task 7 接線保留 `noteStartupBlocker` 並註明與 spec「warning」措辭的取捨；Self-Review 行號修正。
- rev2（2026-08-22，plan review 4 P1）：Task 7 測試改用 `registryOnDisk` 取 Store（`newTestAppAt` 不接 `a.wsReg`，原寫法會 nil panic）；Task 6 改用真正 transcript-only fixture、經 `loadSessionRegistry` 驗證掃描錯誤不落 migrated marker＋修復後可重試；Task 4／7 補齊 spec §4／§8 凍結分支測試（零候選 marker、ViewStart 不精確相等、tombstone／provider 不算候選、scanner 錯誤不落 marker、已 backfilled 不重跑、persist 失敗全回滾、uncertain latch 枚舉表）；Task 8 納入 root package 的 `TurnsBefore` callers（`rebuild_orchestrator.go:302`、`app_replayindex_test.go:413`）避免 commit 後 build 壞掉；Task 10 fixture 改 pre-fix 形狀、經 `restoreSessions` 驗證 backfill 接線（同時涵蓋 startup wiring）。

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
- Test: `internal/wsregistry/store_test.go`、`internal/wsregistry/fsync_test.go`（uncertain latch 的 `writes` 枚舉表 fsync_test.go:254 加入 `BackfillLegacyTranscript`）

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

// spec §4 凍結分支：零候選（掃描成功、確定無待補）仍落 marker——「已檢查過」
// 的語意，下次啟動不再重跑。
func TestBackfillLegacyTranscriptZeroCandidateStillSetsMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript(nil); err != nil {
		t.Fatal(err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("零候選仍應落 marker（代表已檢查過）")
	}
	s2, _ := Open(path)
	if !s2.LegacyTranscriptBackfilled() {
		t.Fatal("marker 未持久化")
	}
}

// spec §4 凍結分支：tombstone 不算候選——wsids 內已 Remove 的 entry 跳過、
// 不標記，marker 照常落盤。
func TestBackfillLegacyTranscriptSkipsTombstone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"},
		"codex":  {ResumeSessionID: "y"},
	}, func() string { n++; return fmt.Sprintf("w%d", n) }); err != nil {
		t.Fatal(err)
	}
	var removed, kept string
	for _, e := range s.Live() {
		if e.Provider == "codex" {
			removed = e.WSID
		} else {
			kept = e.WSID
		}
	}
	if err := s.Remove(removed, "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{kept, removed}); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get(kept)
	if !e.LegacyTranscript {
		t.Fatal("live 候選應被標記")
	}
	de, _ := s.Get(removed)
	if de.LegacyTranscript {
		t.Fatal("tombstone entry 不得被標記")
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("marker 應落盤")
	}
}

// spec §4 凍結分支（不可逆錯誤防護的核心）：persist 失敗時 entry 標記與 marker
// **同時**回滾——marker 單獨留下會讓下次啟動不再重試、標記永久缺失。
// 注入手法沿用 TestPersistFailureRollsBackMemory（chmod 父目錄唯讀）。
func TestBackfillLegacyTranscriptPersistFailureRollsBackBoth(t *testing.T) {
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
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if s.LegacyTranscriptBackfilled() {
		t.Fatal("persist 失敗後 marker 必須回滾（否則永不重試）")
	}
	if e, _ := s.Get("w1"); e.LegacyTranscript {
		t.Fatal("persist 失敗後 entry 標記必須回滾")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err != nil {
		t.Fatalf("修復後重試必須成功：%v", err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("重試後 marker 應落盤")
	}
}
```

另外在 `internal/wsregistry/fsync_test.go:254` 的 uncertain latch `writes` 枚舉表加一行（`BackfillLegacyTranscript` 是新的 mutator，latch 後必須一律拒絕）：

```go
"BackfillLegacyTranscript": func() error { return s.BackfillLegacyTranscript([]string{"w1"}) },
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wsregistry/ -run 'TestBackfillLegacyTranscript' -v`
Expected: FAIL — `s.BackfillLegacyTranscript undefined`（含 fsync_test.go 的枚舉表新行，編譯失敗）

- [ ] **Step 3: Write minimal implementation**（仿 `BackfillResume` 的 `persistOrRollback` 模式）

新增方法：取 s.mu；對每個 wsid，跳過不存在或 `RemovedAt != ""` 的 entry，其餘存舊值到 `old` map 後設 `e.LegacyTranscript = true` 寫回；記 `oldMarker`，設 `s.file.LegacyTranscriptBackfilled = true`；`return s.persistOrRollback(func(){ 還原 old entries 與 oldMarker })`。doc 說明：marker 與 entry 標記同生共死、跳過 tombstone、空候選仍設 marker（代表已檢查過）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/wsregistry/ -count=1`
Expected: PASS（含四個新測試與 fsync latch 枚舉表）

- [ ] **Step 5: Commit**

```bash
git add internal/wsregistry/store.go internal/wsregistry/store_test.go internal/wsregistry/fsync_test.go
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
	// scan error：darwin 上 os.Open(目錄) 會成功，錯誤在讀取時才出現
	// （Scanner.Err()="is a directory"）——這條打的是 Scanner.Err() 分支。
	if _, err := scanLegacyWindow(dir, "claude", ""); err == nil {
		t.Fatal("目錄讀取失敗必須回 error，不得靜默回 nil")
	}
	// 真 open error（EACCES，非 NotExist）：spec §5a 凍結「開檔失敗→回 error」。
	// 沒有這條的話，把 open 錯誤全當 NotExist 吞掉的 mutation 在整份測試存活——
	// 而那正是 transcript-only 使用者被永久遷成空 entries 的路徑。
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過檔案權限，無法重現 open error")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := scanLegacyWindow(path, "claude", ""); err == nil {
		t.Fatal("open error（非 NotExist）必須回 error，不得當 NotExist 吞掉")
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
- Produces: `legacyEntries()` 用 `scanLegacyWindow` 判斷 window、填 `HasLegacyTranscript`；掃描錯誤時回 error（呼叫端 loadSessionRegistry:1613 已 return，故 Migrate 不執行、marker 不落盤）。測試經 `restoreSessions()` → `loadSessionRegistry` 驗證「不遷移、可重試」的完整語意，不只驗 `legacyEntries()` 回錯。

- [ ] **Step 1: Write the failing test**（rev2：真正 transcript-only fixture＋經 loadSessionRegistry 驗證 open error 與 Scanner.Err() 都不凍結 migration）

`seedLegacyJournalFixture`（app_invariants_test.go:171）**不是** transcript-only——restore.json 帶 `sess-legacy`／`task-legacy`，掃描失敗時 entry 仍會因 resume/task 非空被遷移，測不到 spec §3 的核心風險「transcript-only 使用者被永久遷成空 entries」。另建 fixture：

```go
// seedTranscriptOnlyLegacyFixture：M3a 形狀、但 claude 只有 legacy window——
// 無 resume identity、無 task。這是 spec §3 的高風險族群：掃描錯誤若被吞，
// 該 provider 三者皆空 → 跳過遷移 → migrated=true 空 entries → 舊歷史永久遺失。
func seedTranscriptOnlyLegacyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"delta","role":"assistant","text":"ok"}`,
		`{"event_id":"ev-3","provider":"claude","kind":"result"}`)
	b, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: "ev-0"},
		"codex":  {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTranscriptOnlyMigrationScanErrorDoesNotFreeze(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過檔案權限，無法重現 open error")
	}
	dir := seedTranscriptOnlyLegacyFixture(t)
	a := newTestAppAt(t, dir) // 先建 App（sink 要開得了原始 events.jsonl）再破壞檔案
	events := filepath.Join(dir, "events.jsonl")
	orig, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	assertNotMigrated := func(phase string) {
		t.Helper()
		reg := registryOnDisk(t, dir)
		if reg.Migrated() {
			t.Fatalf("%s：掃描失敗不得落 migrated marker（永不重試＝舊歷史永久遺失）", phase)
		}
		if n := len(reg.Live()); n != 0 {
			t.Fatalf("%s：掃描失敗不得寫入 entries：%d", phase, n)
		}
	}
	// (1) 真 open error（EACCES，非 NotExist）：darwin 上 os.Open(目錄) 會成功、
	// 錯誤到讀取才出現，換目錄注入打不到 open 分支——用 chmod 0o000 才是 open error。
	if err := os.Chmod(events, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restoreSessions(); err == nil {
		t.Fatal("open error 時 loadSessionRegistry 必須回錯（不得靜默跳過遷移）")
	}
	assertNotMigrated("open error")
	// (2) scan error：單行超過 scanner buffer 上限（16MiB）→ Scanner.Err()=ErrTooLong。
	if err := os.Chmod(events, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	huge := append(append([]byte{}, orig...), []byte(strings.Repeat("x", 17*1024*1024))...)
	if err := os.WriteFile(events, huge, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restoreSessions(); err == nil {
		t.Fatal("Scanner.Err() 非 nil 時必須回錯（不得把讀到一半當完整 window）")
	}
	assertNotMigrated("scan error")
	// (3) 修復後重試：正常遷移、transcript-only entry 帶 LegacyTranscript=true。
	if err := os.WriteFile(events, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	live, err := a.restoreSessions()
	if err != nil {
		t.Fatalf("修復後重試必須成功：%v", err)
	}
	if len(live) != 1 || live[0].Provider != "claude" {
		t.Fatalf("transcript-only 應恰遷移出一個 claude entry：%+v", live)
	}
	reg := registryOnDisk(t, dir)
	e, ok := reg.Get(live[0].WSID)
	if !ok || !e.LegacyTranscript {
		t.Fatalf("transcript-only entry 必須帶 LegacyTranscript=true：%+v ok=%v", e, ok)
	}
	if !reg.Migrated() {
		t.Fatal("重試成功後 migrated marker 應落盤")
	}
}
```

（同一個 App 連續呼叫 `restoreSessions` 模擬「下次啟動重試」是安全的簡化：失敗發生在 `legacyEntries` 階段、`Migrate` 與 `RestoreDormant` 都未執行，且 `loadSessionRegistry` 每次呼叫都重新 `wsregistry.Open` 讀磁碟。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestTranscriptOnlyMigrationScanErrorDoesNotFreeze -v`
Expected: FAIL — 目前 `legacyEntries` 用 `replayViewWindow` 吞錯回空，(1)(2) 不回錯且 claude 三者皆空被跳過、`Migrate` 落 marker（正是 spec §3 描述的永久遺失路徑）

- [ ] **Step 3: Write minimal implementation**

`legacyEntries()`（app.go:1274）改寫：迴圈內先 `window, werr := scanLegacyWindow(a.eventsPath(), p, e.ViewStartEventID)`，`werr != nil` 直接 `return nil, werr`；`hasTranscript := len(window) > 0`；跳過條件改為 `e.ResumeSessionID == "" && e.TaskID == "" && !hasTranscript`；`out[p]` 的 `LegacyEntry` 加 `HasLegacyTranscript: hasTranscript`。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run 'TestTranscriptOnlyMigrationScanErrorDoesNotFreeze|TestLegacyJournalWithoutWSIDAttributes' -v`
Expected: PASS（既有 seedLegacyJournalFixture 路徑〔resume＋task＋window〕同時不破）

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go
git commit -m "fix(app): legacyEntries 改用 scanLegacyWindow——掃描失敗阻止 migration、不落 marker"
```

---

### Task 7: backfillLegacyTranscript（app，五條件精確比對）

**Files:**
- Modify: `app.go`（新增 `backfillLegacyTranscript`，緊鄰 `backfillResumeFromLegacy` app.go:1713）、`loadSessionRegistry`（接線，`backfillResumeFromLegacy(store)` 呼叫點之後）
- Test: `app_restore_dormant_test.go`

**Interfaces:**
- Consumes: `scanLegacyWindow`（Task 5）、`Store.BackfillLegacyTranscript`（Task 4）、`Store.LegacyTranscriptBackfilled`（Task 1）、`Store.Live()`、`a.restore.Get`。
- Produces: `backfillLegacyTranscript(store *wsregistry.Store) error`——`!LegacyTranscriptBackfilled` 時，以 restore.json 快照對每 provider 精確比對五條件找恰一候選；零候選略過、多候選 fail loud（marker 不落盤）、scan error fail loud；成功則 `store.BackfillLegacyTranscript(候選)`。在 `loadSessionRegistry` 的 `backfillResumeFromLegacy(store)` 之後呼叫。

**測試取 Store 的方式（rev2）**：`newTestAppAt` 不接 `a.wsReg`（app_restore_dormant_test.go:23——它只裝 restore／manager／replay index；`a.wsReg = store` 在 loadSessionRegistry app.go:1688 才發生），direct unit test 一律用既有 `registryOnDisk(t, dir)`（app_restore_dormant_test.go:108）自行開 Store 傳入。startup wiring（loadSessionRegistry 是否真的呼叫到）由 Task 10 的 pre-fix 形狀端對端測試覆蓋。

- [ ] **Step 1: Write the failing test**（五條件與各凍結分支：恰一候選、多候選、ViewStart 不精確相等、tombstone／provider 排除、scan error、已 backfilled 不重跑）

```go
func TestBackfillLegacyTranscriptMultiCandidateFailsLoud(t *testing.T) {
	dir := seedMigratedTwoClaudeFixture(t)
	a := newTestAppAt(t, dir)
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err == nil {
		t.Fatal("多候選必須 fail loud")
	}
	if store.LegacyTranscriptBackfilled() {
		t.Fatal("多候選時 marker 不得落盤（可重試）")
	}
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			t.Fatal("多候選時任何 entry 都不得被標記")
		}
	}
}

func TestBackfillLegacyTranscriptSingleCandidate(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t)
	a := newTestAppAt(t, dir)
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatal(err)
	}
	if !store.LegacyTranscriptBackfilled() {
		t.Fatal("成功後 marker 應落盤")
	}
	var flagged []wsregistry.Entry
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			flagged = append(flagged, e)
		}
	}
	// fixture 內同 ViewStart 的 codex entry 存在但 events.jsonl 無 codex legacy
	// 事件——恰標記一個且是 claude，同時證明 provider 條件。
	if len(flagged) != 1 || flagged[0].Provider != "claude" {
		t.Fatalf("恰一候選應標記一個 claude entry：%+v", flagged)
	}
}

// spec §4 凍結分支：ViewStart 不精確相等（差一字元）不算候選 → 零候選 →
// marker 落盤、entry 不動（安全略過，不是錯誤）。
func TestBackfillLegacyTranscriptViewStartMismatchIsZeroCandidate(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixtureViewStart(t, "ev-0x") // registry=ev-0x、restore.json=ev-0
	a := newTestAppAt(t, dir)
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatal(err)
	}
	if !store.LegacyTranscriptBackfilled() {
		t.Fatal("零候選（掃描成功）仍應落 marker")
	}
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			t.Fatal("ViewStart 不精確相等不得標記")
		}
	}
}

// spec §4 凍結分支：tombstone 不算候選——同 ViewStart 的第二個 claude entry
// 已 Remove 時不構成多候選，live 那個照常標記。
func TestBackfillLegacyTranscriptTombstoneNotCandidate(t *testing.T) {
	dir := seedMigratedTwoClaudeFixture(t)
	reg := registryOnDisk(t, dir)
	tomb := reg.Live()[1].WSID
	if err := reg.Remove(tomb, "user_removed"); err != nil {
		t.Fatal(err)
	}
	a := newTestAppAt(t, dir)
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatalf("tombstone 排除後應為恰一候選：%v", err)
	}
	if !store.LegacyTranscriptBackfilled() {
		t.Fatal("marker 應落盤")
	}
}

// spec §4 凍結分支：scanner I/O 錯誤 → fail loud、marker 不落盤、entry 不動
// （不得誤判成零候選——那會固化成永不重試）。注入是目錄讀取失敗（走
// Scanner.Err() 分支）；open error 分支由 Task 5／Task 6 的 chmod 0o000 覆蓋，
// 兩者在 backfillLegacyTranscript 是同一條 error return。
func TestBackfillLegacyTranscriptScanErrorKeepsMarkerClear(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t)
	a := newTestAppAt(t, dir)
	events := filepath.Join(dir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err == nil {
		t.Fatal("scan error 必須 fail loud，不得當零候選")
	}
	if store.LegacyTranscriptBackfilled() {
		t.Fatal("scan error 時 marker 不得落盤（可重試）")
	}
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			t.Fatal("scan error 時 entry 不得被標記")
		}
	}
}

// spec §4 凍結分支：冪等——marker 已落盤後整段跳過（events.jsonl 已破壞仍
// 不回錯，證明沒有重新掃描）。
func TestBackfillLegacyTranscriptIdempotentAfterMarker(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t)
	if err := registryOnDisk(t, dir).BackfillLegacyTranscript(nil); err != nil {
		t.Fatal(err)
	}
	a := newTestAppAt(t, dir)
	events := filepath.Join(dir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatalf("marker 已落盤應 early return、不重掃：%v", err)
	}
}
```

fixture helper（Step 3 一併建立，registry 用 `seedRegistry`／`wsregistry.Open`＋`MarkMigrated` 直接落盤，events／restore.json 沿用 `seedLegacyJournalFixture` 手法）：
- `seedMigratedLegacyClaudeFixture`：events.jsonl 有 claude 無 WSID 事件（event_id > "ev-0"）；restore.json claude entry 的 ViewStart="ev-0"；workspace-sessions.json migrated=true、一個 claude live entry ViewStart="ev-0"、一個 codex live entry ViewStart="ev-0"（同 ViewStart 但無 codex legacy 事件——provider 條件的反例）、皆無 legacy_transcript、legacy_transcript_backfilled=false。
- `seedMigratedLegacyClaudeFixtureViewStart(t, vs)`：同上（無 codex entry），但 claude entry 的 ViewStart=vs（與 restore.json 的 "ev-0" 不同）。
- `seedMigratedTwoClaudeFixture`：同 `seedMigratedLegacyClaudeFixture`（無 codex entry），但兩個 claude live entry 的 ViewStart 都="ev-0"（多候選）。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestBackfillLegacyTranscript -v`
Expected: FAIL — `a.backfillLegacyTranscript undefined`

- [ ] **Step 3: Write minimal implementation**

新增 `backfillLegacyTranscript`：`store.LegacyTranscriptBackfilled() || a.restore == nil` early return nil。對每個 `legacyProviders` 的 p：`re := a.restore.Get(p)`；`window, werr := scanLegacyWindow(a.eventsPath(), p, re.ViewStartEventID)`，`werr != nil` return werr（不落 marker）；`len(window)==0` continue；掃 `store.Live()` 收集 `e.Provider==p && e.ViewStartEventID==re.ViewStartEventID` 的 WSID 到 `match`；`switch len(match)`：0 略過、1 加入 candidates、default `return fmt.Errorf(...多候選不猜...)`。最後 `return store.BackfillLegacyTranscript(candidates)`。
接線 `loadSessionRegistry`：`backfillResumeFromLegacy(store)` 之後加 `if err := a.backfillLegacyTranscript(store); err != nil { a.noteStartupBlocker("...legacy transcript 標記補寫失敗..." + err.Error()) }`。（spec §4 寫「startup warning」；這裡沿用同類前例 `backfillResumeFromLegacy` 失敗的 `noteStartupBlocker`（app.go:1736）——blocker 只影響訊息排序、不阻擋啟動（app.go:1294-1316），spec 的「不阻擋啟動」語意不變，取排序優先讓使用者先看到。）

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestBackfillLegacyTranscript -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go
git commit -m "feat(app): backfillLegacyTranscript——restore.json 快照五條件精確比對、多候選 fail loud"
```

---

### Task 8: replayindex — TurnsBefore 增加 hasOlder

**Files:**
- Modify: `internal/replayindex/index.go:453-475`（TurnsBefore）
- Modify: `rebuild_orchestrator.go:302`（root package caller，本 task 先最小適配 `recs, _, err :=` 保持編譯；hasOlder 的實際使用在 Task 9）
- Test: `internal/replayindex/index_test.go`（含同套件既有直接呼叫改三回傳值）、`app_replayindex_test.go:413`（root package 測試 caller 改三回傳值）

**Caller 完整性（rev2）**：簽章變更的影響面以 repo-wide `rg '\.TurnsBefore\(' --glob '*.go'` 為準（目前共 5 處：index_test.go:170/179/331、rebuild_orchestrator.go:302、app_replayindex_test.go:413），不得只搜 `internal/replayindex/`——漏掉 root package 會讓本 task 的 commit 直接 build 失敗。

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

`TurnsBefore`（index.go:453）改簽章為 `([]TurnRecord, bool, error)`：`readTurnFileLocked` 錯誤回 `nil, false, err`；`beforeEventID==""` 回 `capTail(all, n), len(all) > n, nil`；找不到 cut（`cut <= 0`）回 `nil, false, nil`；否則回 `capTail(all[:cut], n), cut > n, nil`。所有 caller 以 repo-wide `rg '\.TurnsBefore\(' --glob '*.go'` 逐一改接：index_test.go 三處、`app_replayindex_test.go:413` 改三回傳值；`rebuild_orchestrator.go:302` 最小適配 `recs, _, err :=`（hasOlder 留給 Task 9 使用）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go build ./... && go test ./internal/replayindex/ -count=1 && go test . -run TestIndexDegradedNotifyDoesNotDeadlockAndRecovers -count=1`
Expected: PASS（root package 編譯通過、新測試與改過的既有測試全綠）

- [ ] **Step 5: Commit**

```bash
git add internal/replayindex/index.go internal/replayindex/index_test.go rebuild_orchestrator.go app_replayindex_test.go
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

（helper `seedLegacyPlus25TurnsApp`／`hasLegacyText`／`ascendingByEventID`／`assertAllTurnsPresent` 於此建立。fixture：migrated registry、claude WSID w1、LegacyTranscript=true、ViewStart=V；events.jsonl 有 V 之後的無 WSID legacy 事件，再有 25 個帶 w1 的完整 turn；index 已建 25 turn record。**rev2**：`newTestAppAt` 不接 `a.wsReg`，helper 最後必須 `a.wsReg = registryOnDisk(t, dir)`，否則新增的 `a.wsReg.Get` 會 nil panic——與 Task 7 同一類 setup 陷阱。）

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestLoadTurnsBeforeMergesLegacyAtOldestPage -v`
Expected: FAIL — 目前不合併 legacy，p2 無 legacy 文字

- [ ] **Step 3: Write minimal implementation**

`loadTurnsBefore`（rebuild_orchestrator.go）改寫：`TurnsBefore` 接三回傳值；緊接加 `if beforeEventID != "" && len(recs) == 0 { return nil, nil }`；既有 readEnvelopeRange 迴圈與 open-turn tail 邏輯保留（合成單一出口 `out`）；在最終 return 前加 `if !hasOlder && a.wsReg != nil { if e, ok := a.wsReg.Get(wsid); ok && e.LegacyTranscript { legacy, lerr := scanLegacyWindow(a.eventsPath(), e.Provider, e.ViewStartEventID); if lerr != nil { return nil, lerr }; out = append(legacy, out...) } }`；最後 `sort.SliceStable(out, func(i,j int) bool { return out[i].EventID < out[j].EventID })`。若既有程式在 `beforeEventID != ""` 分支提早 return，須併入此統一出口以確保排序與早退都經過（`import "sort"`）。

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
- Consumes: 全鏈（startup wiring → backfill 標記 → loadTurnsBefore 合併）。

- [ ] **Step 1: Write the failing test**（rev2：fixture 是 **pre-fix 形狀**——已遷移 registry、**無任何** `legacy_transcript` 標記、`legacy_transcript_backfilled=false`，即既有升級使用者重啟當下的磁碟狀態；經 `restoreSessions()` 讓 `loadSessionRegistry` → `backfillLegacyTranscript` 接線真正跑過，才 hydrate。直接預設 `LegacyTranscript=true` 的 fixture 只驗載入合併、驗不到接線）

```go
func TestLegacyTranscriptEndToEndHydrate(t *testing.T) {
	a := seedPreFixMigratedTwoSessionApp(t)
	live, err := a.restoreSessions() // → loadSessionRegistry → backfillLegacyTranscript
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("fixture 應還原兩個 claude session：%+v", live)
	}
	// startup wiring 生效的證據：marker 落盤、恰 w1 被標記（w2 ViewStart 不同、
	// 非候選——五條件比對在真實接線下成立）。
	reg := registryOnDisk(t, a.stateDir)
	if !reg.LegacyTranscriptBackfilled() {
		t.Fatal("loadSessionRegistry 必須接到 backfillLegacyTranscript（marker 未落盤＝沒接線）")
	}
	if e, _ := reg.Get("w1"); !e.LegacyTranscript {
		t.Fatal("backfill 應標記 legacy 的 w1")
	}
	if e, _ := reg.Get("w2"); e.LegacyTranscript {
		t.Fatal("後建的 w2 不得被標記")
	}
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

（`seedPreFixMigratedTwoSessionApp`：registry 以 `seedRegistry`〔`MarkMigrated`，天然不帶新欄位＝pre-fix 形狀〕寫入 w1（claude、ViewStart="ev-0"）與 w2（claude、ViewStart 為後建高水位）；restore.json claude entry ViewStart="ev-0"；events.jsonl 依序：無 WSID legacy 事件（> "ev-0"）→ w1 的 WSID turn（≤20 個，讓首載即最舊 turn 頁）→ w2 的 WSID turn；replay index 依 Task 9 helper 手法建好 turn record；回傳 `newTestAppAt` 的 App（尚未 restoreSessions）。）

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
- §8 測試逐項（rev2 對照）：
  - transcript-only 正常遷移＋open error／Scanner.Err() 不凍結：Task 6 ✓
  - Migrate flag 正反向：Task 2 ✓；schema 預設值／round-trip：Task 1 ✓；ResetView 清標記：Task 3 ✓
  - backfill 凍結分支——零候選落 marker：Task 4＋Task 7（ViewStart mismatch）✓；tombstone／provider 不算候選：Task 4＋Task 7 ✓；多候選 fail loud：Task 7 ✓；scanner 錯誤不落 marker：Task 7 ✓；冪等（marker 後不重跑）：Task 7 ✓；persist 失敗 entry＋marker 全回滾：Task 4 ✓；uncertain latch 枚舉表：Task 4 ✓
  - 跨頁分頁＋去重＋反向＋cursor 早退：Task 9 ✓
  - 端對端（pre-fix 形狀、startup wiring 實跑）＋同 provider 第二 session 不誤接：Task 10 ✓
  - 既有契約迴歸：Task 10 Step 3-4 ✓

**2. Placeholder scan：** 無 TBD／TODO；code 步驟給實際 test code 與明確 impl 指示。fixture helper 於首次使用的 Task 建立、沿用既有 `seedLegacyJournalFixture` 手法。

**3. Type consistency：**
- `TurnsBefore` → `([]TurnRecord, bool, error)`：Task 8 定義、Task 9 消費一致。
- `scanLegacyWindow(eventsPath, provider, viewStart string) ([]contract.Envelope, error)`：Task 5 定義，Task 6／7／9 消費一致。
- `Store.BackfillLegacyTranscript([]string) error`：Task 4 定義、Task 7 消費一致。
- `Entry.LegacyTranscript`／`fileFormat.LegacyTranscriptBackfilled`／`Store.LegacyTranscriptBackfilled()`：Task 1 定義，Task 2/3/4/7/9 消費一致。
- `LegacyEntry.HasLegacyTranscript`：Task 2 定義、Task 6 填入一致。

**待實作時確認的既有型別**（不阻擋 plan）：`Entry.Provider` 為 `string`（store.go:69），Task 9 傳 `e.Provider` 不需轉型；`a.wsReg.Get(wsid)` 回 `(Entry, bool)`（`a.wsReg` 欄位 app.go:151、sessionRegistry interface app.go:676 起）。

**整合審查點（owner 指定）**：Task 4–7 完成後做一次整合審查（schema→migrate→scanner→backfill→接線）；Task 8–9 完成後再做一次（hasOlder→合併→排序→cursor 早退）。
