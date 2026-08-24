# Replay reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `readEnvelopeRange` 停止把非 EOF 讀取錯誤當 EOF 靜默截頁：非 EOF 回錯（含 offset＋wsid 脈絡）並經 `loadTurnsBefore` 傳播；EOF（含包裝 EOF 與同批殘行）、malformed 跳過、「EOF 早於 end」寬容全部維持現狀並以迴歸鎖固定；同時補上本票會短路掉的 legacy scan error 分支守門。

**Architecture:** 簽章放寬 `readEnvelopeRange(r io.ReadSeeker, ...)`（兩個呼叫點傳 `*os.File` 零改動）使 stub reader 可注入「讀到一半才錯」與「包裝 EOF」；EOF 判定一律 `errors.Is(rerr, io.EOF)`；錯誤 wrap 格式凍結為含 `at <cur>`（讀取當下 offset，較 range start 更能定位壞行）與 `wsid=<wsid>`。

**Tech Stack:** Go（rebuild_orchestrator.go），Go testing。

**Spec:** `docs/superpowers/specs/2026-08-24-replay-reliability-design.md`——最終 snapshot **commit a30b31a**（c3ba039 rev1→4fb3d5f rev1.1→1d0f5fb rev2→038ec3c rev3→a30b31a rev3.1，owner APPROVED 2026-08-24）

## Global Constraints

- spec §2 凍結表：`errors.Is(rerr, io.EOF)` 正常終點（先處理同批殘行再 break）；非 EOF 回錯；malformed 單行跳過；「EOF 早於 end」寬容（b/c/e 殘餘風險已記，不加 runtime 訊號）。
- production 呼叫點恰兩處（rebuild_orchestrator.go:330 per-record、:341 open-turn tail），皆已是 `return nil, err` 形——本票不改呼叫點錯誤處理，只改 `readEnvelopeRange` 本體與測試。
- spec §3 義務：legacy scan error 分支守門測試必須同票交付（本票會讓既有兩個目錄注入測試短路、原守門失效——gate 實測吞 lerr 後全套全綠）。
- 過渡期 UI 行為（空白 pane、unhandled rejection）為 spec 明文接受，本票不動 frontend、不加 audit。
- gofmt 乾淨（觸碰檔案）；台灣用語書面中文 doc／commit。

---

### Task 1: readEnvelopeRange——非 EOF fail loud（簽章放寬＋unit 測試）

**Files:**
- Modify: `rebuild_orchestrator.go:417-441`（readEnvelopeRange）
- Test: `rebuild_orchestrator_test.go`（新檔，unit 測試直呼 readEnvelopeRange；若 repo 已有同名檔則沿用）

**Interfaces:**
- Produces: `readEnvelopeRange(r io.ReadSeeker, wsid, after string, start, end int64) ([]contract.Envelope, error)`——第一參數由 `*os.File` 放寬（呼叫點零改動）；讀取迴圈：`rerr != nil && !errors.Is(rerr, io.EOF)` → `return nil, fmt.Errorf("app: read events.jsonl at %d (wsid=%s): %w", cur, wsid, rerr)`（cur＝讀取當下 offset）；EOF 維持「處理同批殘行→break」既有順序。

- [ ] **Step 1: Write the failing test**

```go
// scriptedReadSeeker：依序回放腳本片段的 stub reader——readEnvelopeRange 收
// *os.File 時真實檔案系統造不出「讀到一半才錯」與「包裝 EOF」兩種形狀
// （darwin 目錄注入的 EISDIR 只出現在首讀），簽章放寬後由本 stub 注入。
type scriptedReadSeeker struct {
	chunks []struct {
		data string
		err  error
	}
	i int
}

func (s *scriptedReadSeeker) Read(p []byte) (int, error) {
	if s.i >= len(s.chunks) {
		return 0, io.EOF
	}
	c := &s.chunks[s.i]
	n := copy(p, c.data)
	c.data = c.data[n:] // 消耗式：chunk 大於 len(p) 時分次吐完，不靜默截斷（plan gate P2）
	if len(c.data) > 0 {
		return n, nil // 尚未吐完，錯誤留到本 chunk 消耗完那次一併回
	}
	s.i++
	return n, c.err
}

func (s *scriptedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return offset, nil
}

// 非 EOF 讀取錯誤必須回錯、不得把已讀內容當成功結果回傳（部分成功樣＝靜默
// 截頁）；錯誤脈絡雙斷言：offset 與 wsid 缺一必紅（spec §2 凍結「wrapped，
// 含 offset 與 wsid 脈絡」）。核心 mutation：非 EOF 分支改回 break → 本測試紅。
func TestReadEnvelopeRangeNonEOFErrorFailsLoud(t *testing.T) {
	lines := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e2","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e3","workspace_session_id":"w1","kind":"message"}` + "\n"
	r := &scriptedReadSeeker{chunks: []struct {
		data string
		err  error
	}{
		{data: lines},
		{err: errors.New("simulated EIO")},
	}}
	out, err := readEnvelopeRange(r, "w1", "", 42, -1)
	if err == nil {
		t.Fatal("非 EOF 讀取錯誤必須回錯，不得當 EOF 靜默截頁")
	}
	if out != nil {
		t.Fatalf("錯誤時不得回傳部分成功結果：%d 筆", len(out))
	}
	wantOffset := fmt.Sprintf("at %d", 42+int64(len(lines)))
	if !strings.Contains(err.Error(), wantOffset) {
		t.Fatalf("錯誤必須含讀取當下 offset（%s）：%v", wantOffset, err)
	}
	if !strings.Contains(err.Error(), "wsid=w1") {
		t.Fatalf("錯誤必須含 wsid 脈絡：%v", err)
	}
}

// 包裝 EOF 必須視為正常終點且同批殘行被收錄（spec §2；owner review P2——
// 真實檔案只產生裸 io.EOF，沒有這條 fixture，errors.Is→== 的 mutation 恆綠）。
func TestReadEnvelopeRangeWrappedEOFCollectsFinalLine(t *testing.T) {
	final := `{"event_id":"e9","workspace_session_id":"w1","kind":"message"}` // 無換行
	r := &scriptedReadSeeker{chunks: []struct {
		data string
		err  error
	}{
		{data: final, err: fmt.Errorf("wrapped: %w", io.EOF)},
	}}
	out, err := readEnvelopeRange(r, "w1", "", 0, -1)
	if err != nil {
		t.Fatalf("包裝 EOF 是正常終點，不得回錯：%v", err)
	}
	if len(out) != 1 || out[0].EventID != "e9" {
		t.Fatalf("同批殘行（合法 JSON＋WSID 相符＋無換行）必須被收錄：%+v", out)
	}
}

// 檔尾無換行的裸 EOF 殘行（既有行為迴歸鎖；mutation：殘行處理移到 break 之後
// → 紅）。fixture 條件依 spec §4 寫死：合法 JSON＋WSID 相符＋無換行——半截
// JSON 會被 malformed 跳過使 mutation 恆綠。
func TestReadEnvelopeRangeBareEOFFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e2","workspace_session_id":"w1","kind":"message"}` // 最後一行無換行
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[1].EventID != "e2" {
		t.Fatalf("裸 EOF 同批殘行必須被收錄：%+v", out)
	}
}

// malformed 單行跳過（既有慣例迴歸鎖）。
func TestReadEnvelopeRangeSkipsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{broken json` + "\n" +
		`{"event_id":"e3","workspace_session_id":"w1","kind":"message"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].EventID != "e1" || out[1].EventID != "e3" {
		t.Fatalf("malformed 行跳過、其餘照回：%+v", out)
	}
}

// 「EOF 早於 end」寬容迴歸鎖（spec §2：讀到 EOF 為止、回部分結果、不回錯）。
func TestReadEnvelopeRangeEndBeyondFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, int64(len(content))+9999)
	if err != nil {
		t.Fatalf("EOF 早於 end 維持寬容、不回錯：%v", err)
	}
	if len(out) != 1 {
		t.Fatalf("應回部分結果：%+v", out)
	}
}

// 目錄 FD（首讀即 EISDIR——開檔即壞的形狀；讀到一半才錯由 scripted reader 蓋）。
func TestReadEnvelopeRangeDirectoryFDFailsLoud(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := readEnvelopeRange(f, "w1", "", 0, -1); err == nil {
		t.Fatal("目錄 FD 讀取失敗必須回錯")
	}
}
```

- [ ] **Step 2: Run test to verify it fails（兩段，各自記錄——plan gate P2：舊簽章下整包 build failed、零測試執行，「兩紅四綠」在單次跑不可能觀察到）**

(a) 舊簽章下：`go test . -run TestReadEnvelopeRange -v`
Expected: `FAIL [build failed]`（`*scriptedReadSeeker` 不符 `*os.File`）——證明 stub 確實需要簽章放寬。
(b) **只做簽章放寬**（`io.ReadSeeker`，零行為改動）後重跑同指令：
Expected: `NonEOFErrorFailsLoud` 紅（err==nil）、`DirectoryFDFailsLoud` 紅；其餘 4 條迴歸鎖綠——「改動前行為」的實測證據在此留存（gate 已實測為此結果）。

- [ ] **Step 3: Write minimal implementation**

`readEnvelopeRange` 簽章第一參數改 `io.ReadSeeker`；迴圈尾端：

```go
if rerr != nil {
    if !errors.Is(rerr, io.EOF) {
        return nil, fmt.Errorf("app: read events.jsonl at %d (wsid=%s): %w", cur, wsid, rerr)
    }
    break // EOF（含包裝）＝正常終點；同批殘行已在上方處理
}
```

doc comment 更新：非 EOF fail loud 的理由（spec §1）、EOF 早於 end 寬容與殘餘風險歸屬（spec §2 表）、`io.ReadSeeker` 是為了測試注入（production 恆傳 `*os.File`）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestReadEnvelopeRange -count=1 -v && go build ./... && go vet ./...`
Expected: 六條全綠、build/vet 乾淨（呼叫點零改動）

- [ ] **Step 5: Commit**

```bash
git add rebuild_orchestrator.go rebuild_orchestrator_test.go
git commit -m "fix(app): readEnvelopeRange 非 EOF 讀取錯誤 fail loud——不再當 EOF 靜默截頁"
```

---

### Task 2: app 層傳播斷言＋legacy scan error 分支守門

**Files:**
- Modify: `app_legacy_transcript_test.go`（既有 `TestLoadTurnsBeforeScanErrorPropagates`／`TestLoadTurnsBeforeScanErrorDoesNotClearFlag` 補必要斷言＋來源註解更正；新增 legacy 守門測試＋fixture）

**Interfaces:**
- Consumes: Task 1 的錯誤格式（`read events.jsonl at`＋`wsid=`）。
- Produces: 既有兩測試補「錯誤訊息含 `read events.jsonl at` 與 `wsid=`」必要斷言（無此斷言則修前修後皆綠、對本票 mutation 零鑑別力——spec §4）；新測試 `TestLoadTurnsBeforeLegacyScanErrorStillGuarded`（spec §3 義務）。

- [ ] **Step 1: Add regression/mutation guard tests and strengthen existing assertions**（本 task 非 RED 起手：三條測試在 Task 1 完成後預期全綠——它們是迴歸／mutation 守門，鑑別力由 gate 的吞 lerr mutation 實測證明，不由自然基線的紅證明）

```go
// spec §3 義務：本票讓既有兩個目錄注入測試改由 turn-read 路徑先回錯，原本
// 守 legacy scan error 分支的測試被短路（spec gate 實測：吞 lerr 全套全綠）。
// fixture：index 零 turn record、events.jsonl 只有 boundary 後的無 WSID legacy
// 事件——readEnvelopeRange 根本不被呼叫，目錄注入時錯誤只可能來自
// scanLegacyWindow。mutation（吞 lerr）→ err==nil → 紅。
func TestLoadTurnsBeforeLegacyScanErrorStillGuarded(t *testing.T) {
	a := seedLegacyOnlyNoTurnsApp(t) // flag=true、ViewStart 非空、僅 legacy 事件、無任何 turn
	events := filepath.Join(a.stateDir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.LoadTurnsBefore("w1", "", 20)
	if err == nil {
		t.Fatal("legacy 掃描失敗必須回錯（吞 lerr 的 mutation 在此紅）")
	}
	if strings.Contains(err.Error(), "read events.jsonl at") {
		t.Fatalf("錯誤應來自 scanLegacyWindow 而非 turn-read（fixture 無 turn record）：%v", err)
	}
}
```

helper `seedLegacyOnlyNoTurnsApp`：**一行 wrapper**——`return seedLegacyTranscriptFixtureApp(t, true, 0, "0000000000")`（plan gate 實測：既有參數化 helper 對 n=0 直接產出所需形狀——recs=0／hasOlder=false／open=false／registry flag=true＋ViewStart 非空／events.jsonl 只有 boundary 後無 WSID 事件，含 wsReg 接線）。**不要**複製 helper body 另寫一份（fixture 家族同一參數化 helper 是既有風格，分岔是自找的維護面）。

既有兩測試各補（Task 1 落地後才會綠——來源改為 turn-read）：

```go
	if !strings.Contains(err.Error(), "read events.jsonl at") || !strings.Contains(err.Error(), "wsid=") {
		t.Fatalf("錯誤必須含 turn-read 的 offset 與 wsid 脈絡：%v", err)
	}
```

並把兩測試的來源註解從「錯誤來自 scanLegacyWindow」更正為「錯誤來自 turn-read 路徑（readEnvelopeRange fail loud）；legacy 分支守門見 TestLoadTurnsBeforeLegacyScanErrorStillGuarded」。

- [ ] **Step 2: Run baseline and verify guards pass**

Run: `go test . -run 'TestLoadTurnsBeforeLegacyScanErrorStillGuarded|TestLoadTurnsBeforeScanErrorPropagates|TestLoadTurnsBeforeScanErrorDoesNotClearFlag' -v`
Expected: 三條全綠（自然基線不會紅：守門測試守的 legacy 分支現行即 fail loud；既有兩測試的新斷言在 Task 1 已落地下綠。鑑別力證據＝gate 的吞 lerr mutation 實測：只有守門測試紅、其餘全綠）

- [ ] **Step 3: Implementation**

僅測試與註解變更，無 production 改動。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run 'TestLoadTurnsBefore' -count=1 -v`
Expected: 全綠（既有 12 個＋新增 1 個＝13——`-run` 前綴同時匹配 app_replayindex_test.go 的 `TestLoadTurnsBeforeWithoutIndexFailsLoud`，plan gate 以 `go test -list` 實測計數）

- [ ] **Step 5: Commit**

```bash
git add app_legacy_transcript_test.go
git commit -m "test(app): turn-read 錯誤脈絡必要斷言＋legacy scan error 分支守門（index 零 turn fixture）"
```

---

### Task 3: 迴歸

- [ ] **Step 1: Targeted**

Run: `go test . -run 'TestReadEnvelopeRange|TestLoadTurnsBefore|TestLegacyTranscriptEndToEndHydrate|TestScanLegacyWindow|TestDegradedStartup' -count=1 -v`
Expected: 全綠

- [ ] **Step 2: 全套**

Run: `go build ./... && go vet ./... && go test -race ./internal/wsregistry/ ./internal/replayindex/ -count=1 && go test . -count=1 -timeout 900s`
Expected: 全綠（牆鐘不穩定名單規則照舊：名單內紅單獨重跑判定、名單外如實回報）

- [ ] **Step 3: Commit（僅當 Step 1-2 產生修正）**

無修正則本 task 無 commit。

---

## Self-Review

**1. Spec coverage：**
- §2 凍結表逐格：非 EOF 回錯＋雙脈絡（Task 1 `NonEOFErrorFailsLoud`）、包裝 EOF（`WrappedEOFCollectsFinalLine`——`errors.Is`→`==` mutation 必紅）、裸 EOF 殘行（`BareEOFFinalLine`——殘行後移 mutation 必紅）、malformed 跳過（`SkipsMalformedLine`）、EOF 早於 end 寬容（`EndBeyondFileTolerated`）✓
- §3 傳播＋守門：既有兩測試補必要斷言（`read events.jsonl at`＋`wsid=`）、legacy 分支守門（`LegacyScanErrorStillGuarded`，index 零 turn fixture）✓；過渡期 UI 行為為 spec 明文接受、本票零 frontend 改動 ✓
- §4 簽章放寬（`io.ReadSeeker`）：Task 1 定義、scripted reader 消費 ✓

**2. Type consistency：** `readEnvelopeRange(r io.ReadSeeker, wsid, after string, start, end int64)`——兩個呼叫點傳 `*os.File` 型別相容零改動（Task 1 Step 4 的 build/vet 驗證）。

**3. 已知風險與邊界：**
- 守門測試是迴歸鎖（現行 legacy 分支已 fail loud、測試起手即綠）——其價值由 spec gate 的 mutation 實測背書（吞 lerr → 紅），report 需記錄 mutation 驗證或引 spec gate 證據。
- `cur` 作為錯誤 offset（非 range start）是 plan 層決定：更能定位壞行；unit 測試以可計算的 `start+len(lines)` 斷言。
- scripted reader 的 `Seek` 為 no-op stub——`readEnvelopeRange` 只在開頭 Seek 一次、回傳值未被消費（現行實作只檢查 error），若實作改為依 Seek 回傳值計 cur，stub 回 `offset` 亦正確。

**整合審查點**：小票（2 個實作 task），不設中途 checkpoint；Task 3 全套迴歸＋final review 收尾。
