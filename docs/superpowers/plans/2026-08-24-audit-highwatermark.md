# auditHighWatermark 空高水位污染 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `auditHighWatermark` 讀取失敗不再靜默回 `""`：簽章擴充 `(last, scanned, err)`；ResetView 寫入路徑失敗即停（不寫 registry、FinishReset 照常、可重試）；startup snapshot 路徑降級但留完整軌跡（`restore_watermark_unavailable`），fallback 消費三條件凍結。

**Tech Stack:** Go（restore.go、app.go），Go testing。

**Spec:** `docs/superpowers/specs/2026-08-24-audit-highwatermark-design.md`——最終 snapshot **commit 8927a43**（f33e964 rev1→d629290 rev2→5a4abf2 rev3→8927a43 rev4，owner APPROVED；§4 裁決＝降級＋軌跡）

## Global Constraints

- §2 簽章表：NotExist→`("",false,nil)`；open error（非 NotExist）→err；`Scanner.Err()`→err 不回部分值；完整掃描（含空檔）→`(last|"",true,nil)`。
- §3 ResetView 契約：`err!=nil` 或 `scanned==false` → 不呼叫 ResetView、boundary 與 LegacyTranscript 皆不變、具名 audit `reset_view_watermark_failed`（同筆含 `provider`／`wsid`／`path`／`error` 四欄，NotExist 合成可操作 error）、掛既有 `rerr`、**FinishReset 照常執行**；錯誤**不得**經 `noteRegistryUncertainErr`。
- §4 snapshot 契約：`err!=nil` 或 `scanned==false` → `restore_watermark_unavailable` audit（含 path＋error）＋startup warning、`""` 作 fallback 傳入、繼續啟動不列 blocker；fallback 僅在 restore.json 不存在／malformed（持久化）／缺 provider（僅記憶體）被消費，完整存在不消費不改 boundary。
- caller 全清單：production 2（app.go:2242、:9084）＋測試 4（app_test.go:206／:1214、app_restore_dormant_test.go:59、app_legacy_transcript_test.go:389——最後一處是單值賦值，適配補兩個 `_`）。
- app 層注入打 scan error 分支（events.jsonl 換目錄）；open error 分支僅 unit 層 ENOTDIR（父路徑普通檔案）。snapshot 測試 fixture 前提：migration marker 已設（未遷移 workspace 被 C4a 防護先擋，§4 複合失效形狀）。
- gofmt 乾淨（觸碰檔案）；台灣用語書面中文 doc／commit。

---

### Task 1: auditHighWatermark 簽章擴充＋unit 測試

**Files:**
- Modify: `restore.go:137-155`（auditHighWatermark）；6 個 caller 最小適配（**gate 校正：5/6 個 caller 巢狀在別的呼叫引數內**（openRestoreStore 第二引數×4、ResetView 引數×1），須抽成前置語句 `hw, _, _ := auditHighWatermark(...)` 再傳入；唯 app_legacy_transcript_test.go:389 是單值賦值、補兩個 `_` 即可。gate 已實測抽取後 build/vet 乾淨。語意接線在 Task 2／3）
- Test: `restore_watermark_test.go`（新檔，package main）

**Interfaces:**
- Produces: `auditHighWatermark(eventsPath string) (last string, scanned bool, err error)`——§2 表逐格；`scanned==true`＝開檔成功＋完整掃描＋`Scanner.Err()==nil`。

- [ ] **Step 1: Write the failing test**

```go
func TestAuditHighWatermark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// NotExist：函式層回報「未能確認」，非錯誤。
	if last, scanned, err := auditHighWatermark(path); last != "" || scanned || err != nil {
		t.Fatalf("NotExist 應回 (\"\", false, nil)：%q %v %v", last, scanned, err)
	}
	// 完整掃描：空檔與有內容各一格。
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if last, scanned, err := auditHighWatermark(path); last != "" || !scanned || err != nil {
		t.Fatalf("空檔應回 (\"\", true, nil)：%q %v %v", last, scanned, err)
	}
	lines := `{"event_id":"e1"}` + "\n" + `{broken` + "\n" + `{"event_id":"e2"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if last, scanned, err := auditHighWatermark(path); last != "e2" || !scanned || err != nil {
		t.Fatalf("完整掃描（malformed 跳過）應回 (e2, true, nil)：%q %v %v", last, scanned, err)
	}
}

// open error（非 NotExist）確定性注入：父路徑為普通檔案 → 讀子路徑 → ENOTDIR
// （spec §5：chmod 需 root skip、目錄注入命中的是 Scanner.Err()，皆不用）。
func TestAuditHighWatermarkOpenErrorFailsLoud(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	last, scanned, err := auditHighWatermark(filepath.Join(parent, "events.jsonl"))
	if err == nil || scanned || last != "" {
		t.Fatalf("ENOTDIR open error 必須回錯且不回值：%q %v %v", last, scanned, err)
	}
}

// Scanner.Err()（>16MiB 單行）→ 回錯、不回部分值。mutation：忽略 Scanner.Err()
// 改回現狀 → 本測試紅（截讀偏舊值對寫入路徑是持久化污染，spec §2）。
func TestAuditHighWatermarkScanErrorFailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1"}` + "\n" + strings.Repeat("x", 17*1024*1024)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	last, scanned, err := auditHighWatermark(path)
	if err == nil || scanned {
		t.Fatalf("Scanner.Err() 必須回錯：%v %v", scanned, err)
	}
	if last != "" {
		t.Fatalf("不得回部分值（截讀偏舊）：%q", last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestAuditHighWatermark -v`
Expected: FAIL——簽章不符編譯失敗；僅改簽章（回傳補 `true, nil`／NotExist `false, nil`、行為不動）後重跑：`ScanErrorFailsLoud` 紅（現行忽略 Scanner.Err()）、`OpenErrorFailsLoud` 紅（現行 open 失敗回 ""）、第一條的 NotExist／完整掃描格綠——兩段式記錄進 report（沿用 replay 票慣例）。

- [ ] **Step 3: Write minimal implementation**

`auditHighWatermark` 依 §2 表改寫：open 失敗分 NotExist（`"",false,nil`）與其他（`"",false,err` wrapped 含 path）；迴圈後檢查 `sc.Err()` 非 nil → `"",false,err`；否則 `last,true,nil`。六個 caller 最小適配（`hw, _, _ :=` 或補 `_`）。doc 更新：三值語意、兩 caller 契約見 spec §3/§4。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestAuditHighWatermark -count=1 -v && go build ./... && go vet ./...`
Expected: 全綠；vet 乾淨（測試 caller 適配齊）

- [ ] **Step 5: Commit**

```bash
git add restore.go restore_watermark_test.go app.go app_test.go app_restore_dormant_test.go app_legacy_transcript_test.go
git commit -m "feat(app): auditHighWatermark 簽章擴充 (last, scanned, err)——讀取失敗不再靜默回空"
```

---

### Task 2: startup snapshot 路徑——watermark 不可用軌跡（§4）

**Files:**
- Modify: `app.go:2242` 一帶（openStateWriters 內 watermark 消費）
- Test: `app_restore_dormant_test.go`

**Interfaces:**
- Produces: `err!=nil || !scanned` → `a.audit("restore_watermark_unavailable", map[string]any{"path": ..., "error": ...})`（NotExist 合成可操作說明）＋`a.noteStartupWarning(...)`，以 `""` 傳入 `openRestoreStore`，繼續啟動。audit 設施此時已就緒（spec gate 核實：auditF 在 :2198 先開）。

- [ ] **Step 1: Write the failing test**

**測試層級（gate P1 校正——不得用 `newTestAppAt`）**：`newTestAppAt` 自己呼叫
`openRestoreStore`（app_restore_dormant_test.go:59）、根本不經本 task 要改的
`openStateWriters`（app.go:2242），且 events.jsonl 為目錄時它會在 sink 建立
`t.Fatal`（:33-36）——照用會拿到假綠或紅錯地方。**無條件**改為直接呼叫
`a.openStateWriters(newTestStateLease(stateDir))`（既有 pattern：
app_audit_lifecycle_test.go:76／app_startup_state_test.go:236；gate 已實測此入口
在 events.jsonl 為目錄下跑通、auditF 先開、restore.json 落 `""`）。
**多段啟動的關閉紀律（owner review rev2 P1）**：每一段流程凍結為
「建立 App → openStateWriters → 驗證 → **shutdown** → 修復檔案 → 下一次啟動」
——前一個 App 的 audit／event sink／replay writer 若仍持有檔案 handle，就不是
真實重啟，且 persistence 結果可能受多組 handle 干擾；shutdown 依
app_audit_lifecycle_test.go:79 的既有做法在各段**當場明確執行**，不得把三次
shutdown 全部 defer 到測試結束。`newTestStateLease` 會略過真實 flock，正因如此
段與段之間的隔離只能靠確實關閉。
**不需要 migration marker fixture**（gate P2：C4a 防護在 restoreSessions 層、
不在本路徑；佈了無作用，為滿足它升級到完整 startup 反而引入 lease／workspace
resolve 成本）。

四條測試：

1. `TestStartupWatermarkUnavailableDegradesWithTrace`：restore.json **不存在**＋
   events.jsonl 為目錄 → `openStateWriters` 完成（不 panic）、audit 逐行含
   `restore_watermark_unavailable` 且 data 含 path＋error、
   **`startupErrText()` 含 warning 字樣**（§4 凍結 audit＋warning 兩者；
   appendMessage 是累加不是 first-wins，sink 錯誤不會吃掉它——gate 核實）、
   restore.json 快照 view_start 為 `""`（fallback 被消費並持久化）。
2. `TestStartupWatermarkUnavailableCompleteRestoreUntouched`：restore.json
   **已完整存在**（兩 provider 皆非空 view_start）＋events.jsonl 為目錄 →
   audit 仍發（不可用是事實）、**既有 boundary 不變**（fallback 未被消費）。
3. `TestStartupWatermarkAvailableNoTrace`（gate P2 反向格）：events.jsonl 正常
   （含至少一筆 event）→ `openStateWriters` 後 **audit 無**
   `restore_watermark_unavailable`——無條件發事件的 mutation 在此紅。
4. `TestStartupWatermarkPersistentDegradeShape`：情境 1 驗證後 **shutdown 第一個
   App** → 修復 events.jsonl（回填原始內容）→ 二次「建 App → openStateWriters」
   → 快照仍 `""`（不自動修復）→ **shutdown** → 刪除 restore.json → 三次啟動 →
   快照為當時正確高水位（唯一修復路徑；期望值**從檔案內容算**最後一筆
   event_id）→ shutdown。三段各自當場關閉（見上方關閉紀律）。

另補一條 malformed 格（§4 fallback 三消費條件的第二條）：
`TestStartupWatermarkUnavailableMalformedRestoreRebuilt`——restore.json 為壞 JSON
＋events.jsonl 為目錄 → 重建並持久化 `""`。「缺 provider 條目（僅記憶體）」格
**不測**：既有 openRestoreStore 行為、本票零改動，且該格在 freshEntries 產出的
檔案上恆為 no-op——不測理由記入 report。

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run 'TestStartupWatermark' -v`
Expected: FAIL——1／2／5 紅在 audit 斷言（現行無該事件）、1 另紅在 warning 斷言；3 反向格在實作前**綠**（現行本來就無事件——它是防未來 mutation 的反向鎖，RED 意義由 1 的正向紅補足）；4 的修復形狀依實作前行為記錄

- [ ] **Step 3: Write minimal implementation**

app.go:2242 改 `hw, scanned, werr := auditHighWatermark(...)`；`werr != nil || !scanned` → audit＋warning、`hw` 保持 `""`。不動 openRestoreStore 本體。

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run 'TestStartupWatermark|TestRestore' -count=1 && go build ./... && go vet ./...`
Expected: 全綠（既有 restore 測試不破）

- [ ] **Step 5: Commit**

```bash
git add app.go app_restore_dormant_test.go
git commit -m "feat(app): startup watermark 不可用留軌跡——restore_watermark_unavailable＋warning，fallback 語意不變"
```

---

### Task 3: ResetView 寫入路徑——失敗即停（§3）

**Files:**
- Modify: `app.go:9084` 一帶（reset 流程的 ResetView 呼叫前判定）
- Test: `app_legacy_transcript_test.go`（gate 校正理由：legacy flag fixture（`seedLegacyTranscriptFixtureApp`）在此檔、實測 `a.NewSession("w1")` 直接可觸發 reset 流程；既有 reset 測試實際住在 app_test.go 的 `TestNewSession*`，但本 task 的斷言依賴 legacy fixture）；audit 四欄位斷言 helper 沿用 `auditHasOp` pattern（app_registry_uncertain_test.go:364）擴一個四欄位版本（`auditHasOp` 只比對 `data["op"]`，新事件無 op 欄）

**Interfaces:**
- Produces: 呼叫 `a.wsReg.ResetView` 前：`hw, scanned, werr := auditHighWatermark(...)`；`werr != nil || !scanned` → `a.audit("reset_view_watermark_failed", map[string]any{"provider":…, "wsid":…, "path":…, "error":…})`（NotExist 合成錯誤內容）、`rerr = fmt.Errorf(...)`（**不經 noteRegistryUncertainErr**）、跳過 ResetView；FinishReset 照常（結構既有，app.go:9096 在 rerr 判斷前）。

- [ ] **Step 1: Write the failing test**

1. `TestResetViewWatermarkFailureStopsWrite`：既有 legacy fixture（flag=true、boundary 非空）→ app 建好後把 events.jsonl 換目錄 → `a.NewSession("w1")` → 回錯；registryOnDisk 斷言 boundary 與 LegacyTranscript **皆未變**；audit 逐行定位 `reset_view_watermark_failed` 且同筆 data 四欄位齊（provider／wsid／path／error 皆非空）。**lifecycle 回 idle 不設獨立探針**（owner review rev2 P1：`BeginNewSessionSubmit` 成功會把 manager 推進 submitting、未 RejectSubmit 就會擋掉下一步重試——探針本身破壞重試前置狀態）；idle 的證明就是下一步「修復後 `NewSession` 重試成功」本身。
2. 修復 events.jsonl → 重試 → 成功且 boundary 為正確高水位、flag 清除（既有 ResetView 語意）。
   **修復手法（gate P2 校正）**：換目錄**前**先 `orig, _ := os.ReadFile(ep)` 保存原始內容，修復時回填 `orig`——用空檔「修復」會得到 `("", true, nil)`，寫入的 boundary 仍是 `""`、與失敗態不可判別。期望高水位**從 orig 內容算**最後一筆 event_id（sink 持已 unlink 的舊 inode，之後的 live emission 不會進新檔，不得從 emission 推）。
3. NotExist 變體：刪 events.jsonl → 同樣停止不寫、error 欄位含合成說明（斷言非空且含「不存在」類字樣）。
4. **空檔格（gate P2——§3 凍結第三格，漏測會放行打壞全新 workspace 的 naive guard）**：`TestResetViewWatermarkEmptyFileWritesEmpty`——events.jsonl 為**存在的空檔**（truncate）→ `scanned==true` → ResetView 照常寫入 `""`（成功、不回錯、無 watermark audit）；mutation `if werr != nil || !scanned || hw == ""` 在此紅。

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestResetViewWatermark -v`
Expected: FAIL——現行 watermark 失敗回 `""` 照寫，boundary 被改成空（斷言紅在「皆未變」）

- [ ] **Step 3: Write minimal implementation**

依 Interfaces；錯誤訊息含 path 與原因。doc 引 spec §3（session 已收尾、顯示暫時不一致、非原子失敗——註解如實）。

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run 'TestResetViewWatermark|TestNewSession|TestLoadTurnsBefore' -count=1 && go build ./... && go vet ./...`
Expected: 全綠（gate 校正：repo 無 `TestReset*`——既有 reset 流程測試叫 `TestNewSession*`，app_test.go:1083 起）

- [ ] **Step 5: Commit**

```bash
git add app.go <測試檔依實際位置>
git commit -m "feat(app): ResetView watermark 失敗即停——不寫 boundary、四欄位 audit、FinishReset 照常"
```

---

### Task 4: 迴歸

- [ ] Step 1: `go test . -run 'TestAuditHighWatermark|TestStartupWatermark|TestResetViewWatermark|TestLegacy|TestLoadTurnsBefore|TestRestore|TestDegradedStartup' -count=1`
- [ ] Step 2: `go build ./... && go vet ./... && go test -race ./internal/wsregistry/ ./internal/replayindex/ -count=1 && go test . -count=1 -timeout 900s`（牆鐘名單規則照舊）
- [ ] Step 3: 無修正則無 commit。

---

## Self-Review

- §2 表四格：Task 1 三條 unit（NotExist／空檔／完整＋malformed、ENOTDIR open error、Scanner.Err() 不回部分值＋mutation）✓
- §3 契約：Task 3（停止不寫＋四欄位 audit＋FinishReset 後可重試＋NotExist 合成＋修復重試）✓；不經 noteRegistryUncertainErr 為實作指示 ✓
- §4 契約：Task 2（降級軌跡＋fallback 消費／不消費兩向＋持久降級與唯一修復路徑重啟形狀）✓；fixture 前提 marker 已設 ✓
- 簽章一致：`(last string, scanned bool, err error)` Task 1 定義、Task 2/3 消費；6 caller 清單見 Global Constraints。
- Task 2 測試層級已由 gate 實測凍結：直接 `a.openStateWriters(newTestStateLease(stateDir))`，不用 `newTestAppAt`（該 helper 不經 :2242 且 sink 失敗即 t.Fatal——假綠陷阱）。
- Task 3 觸發方式已凍結：`a.NewSession(wsid)`（既有用法 app_test.go:1099）；「回 idle 可重試」**不用** `BeginNewSessionSubmit` 探針（會推進 submitting、破壞重試前置——owner review rev2 P1），以修復後 `NewSession` 重試成功直接證明。
- **「不得經 noteRegistryUncertainErr」無可觀測差異、無法以測試守**（gate 補充：該 helper 對非 uncertain 錯誤原樣回傳、不 latch）——僅能靠 code review；task review 與 final review 的 checklist 明列此項，關票時不得宣稱有測試覆蓋。
- §4 反向格（scanned==true 無軌跡）、§3 空檔格、malformed 格皆已入測試清單；「缺 provider 僅記憶體」格不測（既有行為零改動＋該格在 freshEntries 檔案上恆 no-op），report 記不測理由。
