package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/replayindex"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// newTestAppAt：綁定指定 stateDir 的最小 App——只裝 loadSessionRegistry 與
// CreateSession 真正會用到的東西（manager、restore store）。刻意不共用
// newTestApp：本檔多數測試要「同一個 stateDir 重開第二個 App」模擬重啟，
// 而 newTestApp 每次都自建新的 temp stateDir。
func newTestAppAt(t *testing.T, stateDir string) *App {
	t.Helper()
	a := NewApp()
	a.ctx = context.Background()
	a.stateDir = stateDir
	a.lease = newTestStateLease(stateDir)
	a.setPhase(phaseReady) // 同 newTestAppIn：夾具代表 startup 已完成
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sink, err := appcore.NewJSONLSink(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a.eventSink = sink
	// Task 20：replay index 一併接上——§3.2.4 的後半段（index 驗證、incomplete
	// turn 修復）以它為唯一判定來源，helper 不接就等於把整段序列測空。emitUI
	// 與 Manager 的 Emit 也要給（修復會 emit 事件，nil func 會直接 panic）。
	idx, err := replayindex.OpenWith(filepath.Join(stateDir, "replay-index"),
		replayindex.Config{Notify: a.onIndexDegraded})
	if err != nil {
		t.Fatal(err)
	}
	a.replayIndex = idx
	a.emitUI = func(string, any) {}
	a.manager = appcore.New(appcore.Config{Sink: sink,
		Emit:  func(contract.Envelope) {},
		Index: indexOrNil(idx)})
	t.Cleanup(func() { _ = a.manager.Close() })
	af, err := os.OpenFile(filepath.Join(stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	a.auditF = af
	t.Cleanup(func() { _ = af.Close() })
	hw, _, _ := auditHighWatermark(a.eventsPath())
	rs, err := openRestoreStore(filepath.Join(stateDir, "restore.json"), hw)
	if err != nil {
		t.Fatal(err)
	}
	a.restore = rs
	return a
}

// seedRegistry：把 entries 以「已遷移」狀態寫進 workspace-sessions.json
// （MarkMigrated 是 entries ＋ marker 的原子寫入，正好等於「上一輪已完成遷移」）。
func seedRegistry(t *testing.T, stateDir string, entries ...wsregistry.Entry) {
	t.Helper()
	s, err := wsregistry.Open(filepath.Join(stateDir, "workspace-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMigrated(entries); err != nil {
		t.Fatal(err)
	}
}

// newTestAppWithRegistry：已遷移、已有 entries 的 registry ＋ 綁它的 App。
func newTestAppWithRegistry(t *testing.T, entries ...wsregistry.Entry) *App {
	t.Helper()
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	seedRegistry(t, dir, entries...)
	return a
}

// seedLegacyRestoreJSON：只有 M3a 舊 restore.json、沒有 workspace-sessions.json
// 的 stateDir（claude 有 resume identity＋taskID；codex 為空 entry，依 §3.2.5
// 不得建立 legacy session）。
func seedLegacyRestoreJSON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: "ev-1", ResumeSessionID: "sess-legacy", TaskID: "task-legacy"},
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

// registryOnDisk：直接從磁碟重讀 registry（不經 App），用來驗證持久化結果。
func registryOnDisk(t *testing.T, stateDir string) *wsregistry.Store {
	t.Helper()
	s, err := wsregistry.Open(filepath.Join(stateDir, "workspace-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

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
		if a.manager.IsActive(appcore.WSID(e.WSID)) {
			t.Fatalf("必須以 dormant 還原，不得為 active：%s", e.WSID)
		}
	}
	if len(a.apprPending) != 0 {
		t.Fatal("dormant 還原不得有 pending approval")
	}
	if a.manager.SlotCount("claude") != 1 || a.manager.SlotCount("codex") != 1 {
		t.Fatal("dormant 仍佔名額")
	}
	if a.wsReg == nil {
		t.Fatal("載入成功後 registry 必須已接上 App（否則 CreateSession 會被 errNoSessionRegistry 早退）")
	}
}

// TestLoadRegistryIsIdempotent：§3.2.4 要求 crash 後重跑啟動序列收斂到相同狀態。
// 同一個 App 重跑 load 不得重複佔名額（RestoreDormant 的冪等性靠這條守住）。
func TestLoadRegistryIsIdempotent(t *testing.T) {
	a := newTestAppWithRegistry(t,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	if _, err := a.loadSessionRegistry(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.loadSessionRegistry(); err != nil {
		t.Fatalf("重跑啟動序列必須冪等：%v", err)
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("重跑不得重複佔名額：%d", got)
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
		t.Fatalf("legacy 應遷出 1 個 session（codex 空 entry 不建立）：%+v", e1)
	}
	if e1[0].Provider != "claude" || e1[0].ResumeSessionID != "sess-legacy" ||
		e1[0].TaskLabel != "task-legacy" || e1[0].ViewStartEventID != "ev-1" {
		t.Fatalf("legacy 欄位對映錯誤：%+v", e1[0])
	}
	a2 := newTestAppAt(t, dir)
	e2, err := a2.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(e2) != 1 || e2[0].WSID != e1[0].WSID {
		t.Fatalf("重啟不得產生第二枚 WSID：%+v vs %+v", e2, e1)
	}
}

// TestMigrationPersistFailureBlocksProviderStart：MarkMigrated 落盤失敗
// （stateDir 唯讀）必須 fail loud（§3.2.6）。本 task 不做 provider 啟動，
// 「不啟動 provider」在現行程式碼的唯一可觀測形式是 registry 未接上 App ——
// CreateSession 因此一律早退，不會有任何新 session 被建到未遷移的 registry 上。
func TestMigrationPersistFailureBlocksProviderStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限，無法用唯讀目錄重現 persist 失敗")
	}
	dir := seedLegacyRestoreJSON(t)
	a := newTestAppAt(t, dir)
	// 先建出未遷移的 registry 檔（Open 對既有檔案不寫入），再把目錄轉唯讀，
	// 讓後續 MarkMigrated 的 temp file 寫入必然失敗。
	if _, err := wsregistry.Open(filepath.Join(dir, "workspace-sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := a.loadSessionRegistry(); err == nil {
		t.Fatal("migration persist 失敗必須 fail loud（§3.2.6），呼叫端據此不啟動 provider")
	}
	if a.wsReg != nil {
		t.Fatal("遷移未完成的 registry 不得接上 App")
	}
	if _, err := a.CreateSession("claude", "t"); !errors.Is(err, errNoSessionRegistry) {
		t.Fatalf("registry 未接線時 CreateSession 必須早退：%v", err)
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("失敗的啟動流程不得佔名額：%d", got)
	}
}

func TestRemovedTombstoneNotRestored(t *testing.T) {
	a := newTestAppWithRegistry(t,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t", RemovedAt: "t2", RemoveReason: "user_removed"})
	entries, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("tombstone 不得還原：%+v", entries)
	}
	if a.manager.SlotCount("claude") != 0 {
		t.Fatal("removed 不計 slot")
	}
}

// TestCreateSessionBeforeRegistryLoadIsRejected：啟動序的第一道防線——
// Migrate 必須先於任何 Put。MarkMigrated 是整批取代，若 CreateSession 能在
// 遷移前寫進 registry，那筆 entry 會被遷移無聲蒸發。這裡驗證那條路徑根本
// 不可達（wsReg 只在整段載入序列成功後才接線）。
func TestCreateSessionBeforeRegistryLoadIsRejected(t *testing.T) {
	dir := seedLegacyRestoreJSON(t)
	a := newTestAppAt(t, dir)
	if _, err := a.CreateSession("claude", "too-early"); !errors.Is(err, errNoSessionRegistry) {
		t.Fatalf("registry 載入前的 CreateSession 必須被拒：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace-sessions.json")); !os.IsNotExist(err) {
		t.Fatalf("被拒的 CreateSession 不得建立／寫入 registry：%v", err)
	}
	// 遷移仍照常進行（前一步若真的寫進去，Migrate 的第二道 guard 會拒絕執行）。
	live, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatalf("遷移必須不受影響：%v", err)
	}
	if len(live) != 1 || live[0].TaskLabel != "task-legacy" {
		t.Fatalf("legacy 遷移結果不符：%+v", live)
	}
}

// TestMigrationPrecedesCreateSessionPut：順序的第二個面向——同一個 stateDir 上
// 「先遷移、後建立」的結果必須在重啟後同時看得到兩筆。若實作把 Migrate 放到
// CreateSession 的 Put 之後（或每次啟動都重跑遷移），MarkMigrated 的整批取代
// 會把新建的那筆抹掉，這裡會少一筆。
func TestMigrationPrecedesCreateSessionPut(t *testing.T) {
	dir := seedLegacyRestoreJSON(t)
	a1 := newTestAppAt(t, dir)
	live, err := a1.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	legacyWSID := live[0].WSID
	// Put 之前遷移就必須已完成並落盤——這是順序本身的直接斷言（marker 已寫，
	// 代表後續任何 Put 都不可能再被 MarkMigrated 的整批取代掃掉）。
	if !registryOnDisk(t, dir).Migrated() {
		t.Fatal("CreateSession（Put）之前遷移 marker 必須已落盤")
	}
	newWSID, err := a1.CreateSession("codex", "after-migration")
	if err != nil {
		t.Fatal(err)
	}

	a2 := newTestAppAt(t, dir) // 重啟
	live2, err := a2.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(live2) != 2 {
		t.Fatalf("legacy ＋ 新建 session 都必須存活：%+v", live2)
	}
	seen := map[string]bool{}
	for _, e := range live2 {
		seen[e.WSID] = true
	}
	if !seen[legacyWSID] || !seen[newWSID] {
		t.Fatalf("遺失 entry：legacy=%s new=%s live=%+v", legacyWSID, newWSID, live2)
	}
}

// TestStartupWiresSessionRegistry：接線是否真的發生，責任在啟動流程本身——
// CreateSession 的 wsReg==nil guard 只是防禦，不能拿來當「已接線」的證據。
func TestStartupWiresSessionRegistry(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("WORKBENCH_WORKSPACE", ws)
	a := NewApp()
	a.emitUI = func(string, any) {} // 不碰 wails runtime
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if a.startupErrText() != "" {
		t.Fatalf("乾淨 workspace 不應有 startup error：%s", a.startupErrText())
	}
	if a.wsReg == nil {
		t.Fatal("startup 必須把 session registry 接上 App")
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "workspace-sessions.json")); err != nil {
		t.Fatalf("startup 必須建立／載入 workspace-sessions.json：%v", err)
	}
	if _, err := a.CreateSession("claude", "post-startup"); err != nil {
		t.Fatalf("startup 之後 CreateSession 必須可用：%v", err)
	}
}

// auditHas：audit.jsonl 是否出現指定 kind 的紀錄。
func auditHas(t *testing.T, stateDir, kind string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), `"kind":"`+kind+`"`)
}

// TestUnknownProviderEntrySkipped（owner 2026-08-14 決策 2）：單筆無法解析
// provider 的 entry 不該讓整個 app 開不起來。跳過該筆、不刪除、不阻擋啟動，
// 但必須留下診斷軌跡（audit ＋ 啟動警告，含被跳過的筆數與 WSID）。
func TestUnknownProviderEntrySkipped(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1"},
		wsregistry.Entry{WSID: "wX", Provider: "gemini", CreatedAt: "t2"})

	live, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatalf("單筆未知 provider 不得阻擋啟動：%v", err)
	}
	if len(live) != 1 || live[0].WSID != "w1" {
		t.Fatalf("回傳值不得包含被跳過的 entry：%+v", live)
	}
	if a.wsReg == nil {
		t.Fatal("啟動仍應完成接線")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("已知 provider 仍須還原：%d", got)
	}
	if got := a.manager.SlotCount("gemini"); got != 0 {
		t.Fatalf("未知 provider 不得佔名額：%d", got)
	}
	if !strings.Contains(a.startupErrText(), "wX") || !strings.Contains(a.startupErrText(), "跳過 1 筆") {
		t.Fatalf("啟動警告必須含被跳過的筆數與 WSID：%q", a.startupErrText())
	}
	if !auditHas(t, dir, "session_registry_unknown_provider") {
		t.Fatal("必須留下 audit 診斷軌跡")
	}
	// 非破壞性：被跳過的 entry 仍在磁碟上（該 provider 若回歸即可還原）。
	if _, ok := registryOnDisk(t, dir).Get("wX"); !ok {
		t.Fatal("被跳過的 entry 不得從 registry 刪除")
	}
}

// TestProviderOverLimitRestoresNothing（owner 2026-08-14 決策 3）：Manager 沒有
// 移除 committed slot 的 API（Task 22 才有），半還原無法回滾——所以先全量驗證、
// 再全量還原。某 provider 超限時必須「一筆都沒還原」，兩個 provider 的
// SlotCount 都要是 0（若是邊驗邊還原，claude 會先被還原 4 筆才失敗）。
func TestProviderOverLimitRestoresNothing(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	entries := []wsregistry.Entry{}
	for i := 0; i < appcore.MaxSessionsPerProvider+1; i++ {
		entries = append(entries, wsregistry.Entry{
			WSID: fmt.Sprintf("w%d", i), Provider: "claude", CreatedAt: fmt.Sprintf("t%d", i)})
	}
	entries = append(entries, wsregistry.Entry{WSID: "wc", Provider: "codex", CreatedAt: "t9"})
	seedRegistry(t, dir, entries...)

	_, err := a.loadSessionRegistry()
	if err == nil {
		t.Fatal("超限必須 fail loud，不得靜默丟棄多餘 entry")
	}
	// 先斷言「一筆都不還原」——這是兩段式的核心，邊驗邊還原會在這裡露餡
	//（claude 先被還原到上限才失敗）。
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("驗證不過必須一筆都不還原（claude）：%d", got)
	}
	if got := a.manager.SlotCount("codex"); got != 0 {
		t.Fatalf("驗證不過必須一筆都不還原（codex）：%d", got)
	}
	if a.wsReg != nil {
		t.Fatal("驗證失敗不得接線")
	}
	for _, want := range []string{
		fmt.Sprintf("有 %d 筆 live claude session", appcore.MaxSessionsPerProvider+1),
		fmt.Sprintf("上限為 %d", appcore.MaxSessionsPerProvider),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("錯誤訊息必須說明 provider／筆數／上限，缺 %q：%v", want, err)
		}
	}
}

// seedRegistryRaw：直接寫出 workspace-sessions.json（已遷移狀態）。
// 手動編輯才可能出現的壞資料——WSID 欄位為空、兩個 map key 帶同一個 WSID——
// 沒辦法經 MarkMigrated 造出來（它會擋掉），只能直接寫檔。
func seedRegistryRaw(t *testing.T, stateDir string, entries map[string]map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"schema_version": 2, "migrated": true, "entries": entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "workspace-sessions.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInvalidEntriesSkippedWithoutPartialRestore（review #1）：手動編輯可造出
// 兩種「Pass 1 放行、Pass 2 中途才爆」的 entry——WSID 欄位為空
// （RestoreDormant 回 ErrSessionNotFound）與重複 WSID（回 ErrProviderMismatch）。
// 兩者都必須在 Pass 1 就被跳過，否則前面幾筆已還原、之後才失敗，就留下
// 「Manager 有 slot、wsReg 為 nil」的半還原狀態。
func TestInvalidEntriesSkippedWithoutPartialRestore(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	seedRegistryRaw(t, dir, map[string]map[string]any{
		"w1":  {"wsid": "w1", "provider": "claude", "created_at": "t1"}, // 正常，會先被還原
		"bad": {"wsid": "", "provider": "claude", "created_at": "t2"},   // 空 WSID
		"dup": {"wsid": "w1", "provider": "codex", "created_at": "t3"},  // 重複 WSID、provider 還不同
	})

	live, err := a.loadSessionRegistry()
	if err != nil {
		// 先驗「一筆都不還原」的不變量：邊驗邊還原會在這裡露餡（w1 已進 Manager）。
		if c, x := a.manager.SlotCount("claude"), a.manager.SlotCount("codex"); c != 0 || x != 0 {
			t.Fatalf("半還原：載入回錯卻留下 slot（claude=%d codex=%d）", c, x)
		}
		t.Fatalf("壞 entry 應跳過而非阻擋啟動：%v", err)
	}
	if len(live) != 1 || live[0].WSID != "w1" {
		t.Fatalf("只有合法的那筆該被還原：%+v", live)
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("合法 entry 仍須還原：%d", got)
	}
	if got := a.manager.SlotCount("codex"); got != 0 {
		t.Fatalf("重複 WSID 的那筆不得佔名額：%d", got)
	}
	if !strings.Contains(a.startupErrText(), "跳過 2 筆") ||
		!strings.Contains(a.startupErrText(), "empty wsid") ||
		!strings.Contains(a.startupErrText(), "duplicate wsid") {
		t.Fatalf("啟動警告必須含筆數與兩種原因：%q", a.startupErrText())
	}
	if !auditHas(t, dir, "session_registry_invalid_entry") {
		t.Fatal("必須留下 audit 診斷軌跡")
	}
	if _, ok := registryOnDisk(t, dir).Get("dup"); !ok {
		t.Fatal("被跳過的 entry 不得從 registry 刪除")
	}
}

// TestFatalErrorNotMaskedBySkipWarning（review #2）：noteStartupWarning 只填
// 第一則。若「跳過 N 筆」的非致命警告在 Pass 1 就佔走名額，之後的致命失敗
// 訊息會被丟棄——UI 只看得到一句聽起來沒事的警告，實際上 registry 載入整個
// 失敗、CreateSession 全掛。audit 兩筆都要在（診斷軌跡的價值正是在失敗時）。
func TestFatalErrorNotMaskedBySkipWarning(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	entries := map[string]map[string]any{
		"wX": {"wsid": "wX", "provider": "gemini", "created_at": "t0"}, // 未知 provider
	}
	for i := 0; i <= appcore.MaxSessionsPerProvider; i++ { // 同時超限
		k := fmt.Sprintf("w%d", i)
		entries[k] = map[string]any{"wsid": k, "provider": "claude", "created_at": fmt.Sprintf("t%d", i+1)}
	}
	seedRegistryRaw(t, dir, entries)

	_, err := a.loadSessionRegistry()
	if err == nil {
		t.Fatal("超限必須 fail loud")
	}
	if a.startupErrText() != "" {
		t.Fatalf("載入失敗時非致命警告不得先佔用 UI 名額：%q", a.startupErrText())
	}
	// startup() 的處置：致命訊息因此才拿得到唯一的名額。
	a.noteStartupWarning("session registry load failed: " + err.Error())
	if !strings.Contains(a.startupErrText(), "load failed") ||
		!strings.Contains(a.startupErrText(), "上限為") {
		t.Fatalf("UI 必須看到致命那則：%q", a.startupErrText())
	}
	if !auditHas(t, dir, "session_registry_unknown_provider") {
		t.Fatal("跳過的診斷軌跡仍必須留在 audit（即使載入失敗）")
	}
}

// TestMalformedRegistryErrorIsActionable（owner 2026-08-14 決策 1）：維持 fail
// loud、零自動修復，但錯誤訊息要可操作——完整路徑、備份後移除的指引、以及
// 「稽核歷史不受影響」的說明。壞檔必須原封不動（不改名、不重建）。
func TestMalformedRegistryErrorIsActionable(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	path := filepath.Join(dir, "workspace-sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := a.loadSessionRegistry()
	if err == nil {
		t.Fatal("malformed registry 必須 fail loud")
	}
	for _, want := range []string{path, "備份", "events.jsonl"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("錯誤訊息缺可操作資訊 %q：%v", want, err)
		}
	}
	if a.wsReg != nil {
		t.Fatal("載入失敗不得接線")
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || string(b) != "{not json" {
		t.Fatalf("壞檔必須原封不動（不改名、不重建）：%q err=%v", string(b), rerr)
	}
	if ents, derr := os.ReadDir(dir); derr == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "workspace-sessions.json.") {
				t.Fatalf("不得自動改名／留下衍生檔：%s", e.Name())
			}
		}
	}
}

// TestLegacyEntriesRequiresRestoreStore：轉換層的 fail loud——restore store 尚未
// 開啟就遷移，會以「零 legacy entry」標記 migrated，舊 session 從此不可能再被
// 遷出（marker 單向）。這種載入順序 bug 必須回錯，不得靜默當成空資料。
func TestLegacyEntriesRequiresRestoreStore(t *testing.T) {
	dir := seedLegacyRestoreJSON(t)
	a := newTestAppAt(t, dir)
	a.restore = nil
	if _, err := a.loadSessionRegistry(); err == nil {
		t.Fatal("restore store 未開啟就遷移必須 fail loud")
	}
	if registryOnDisk(t, dir).Migrated() {
		t.Fatal("失敗的遷移不得標記 migrated")
	}
}

// seedEvents：往 stateDir/events.jsonl 追加最小可辨識的 provider 事件。
func seedEvents(t *testing.T, stateDir string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(stateDir, "events.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLegacyEmptyViewWindowNotMigrated：restore.json 不存在（或 malformed 被
// 重建）時 openRestoreStore 會用 audit high-watermark 幫**兩個** provider 都補齊
// entry（restore.go:42-56）。那種 entry 的 ViewStartEventID 非空，但 window 內
// 必然零事件——依 §3.2.5 不得建立 legacy session，否則只用過 claude 的使用者
// 升級後會憑空多出一個 codex session 並吃掉名額。
func TestLegacyEmptyViewWindowNotMigrated(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedEvents(t, dir,
		`{"event_id":"ev-a","provider":"claude","kind":"message"}`,
		`{"event_id":"ev-b","provider":"claude","kind":"result"}`)
	a := newTestAppAt(t, dir) // 無 restore.json → 兩個 provider 都補到 watermark ev-b
	live, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("空 view window 不得建立 legacy session：%+v", live)
	}
	if a.manager.SlotCount("claude") != 0 || a.manager.SlotCount("codex") != 0 {
		t.Fatal("空 entry 不得佔名額")
	}
}

// TestLegacyViewWindowWithEventsMigrated：上一條的反向護欄——window 內真的有
// 事件時（即使沒有 resume identity／taskID）必須遷移，不得被過濾掉。
func TestLegacyViewWindowWithEventsMigrated(t *testing.T) {
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-a","provider":"claude","kind":"message"}`,
		`{"event_id":"ev-b","provider":"claude","kind":"result"}`)
	b, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: "ev-a"}, // ev-b 落在 window 內
		"codex":  {ViewStartEventID: "ev-b"}, // window 空
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestAppAt(t, dir)
	live, err := a.loadSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Provider != "claude" || live[0].ViewStartEventID != "ev-a" {
		t.Fatalf("window 內有事件的 provider 必須遷移（且只有它）：%+v", live)
	}
}

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
	got, scanned, err := scanLegacyWindow(path, "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if !scanned {
		t.Fatal("正常掃描應回 scanned==true")
	}
	if len(got) != 2 || got[0].EventID != "e1" || got[1].EventID != "e2" {
		t.Fatalf("只應取無 WSID 的 claude 事件：%+v", got)
	}
	if envs, scanned, err := scanLegacyWindow(filepath.Join(dir, "nope.jsonl"), "claude", ""); err != nil || envs != nil || scanned {
		t.Fatalf("檔案不存在應回 (nil,false,nil)：%v %v %v", envs, scanned, err)
	}
	// scan error：darwin 上 os.Open(目錄) 會成功，錯誤在讀取時才出現
	// （Scanner.Err()="is a directory"）——這條打的是 Scanner.Err() 分支。
	if _, scanned, err := scanLegacyWindow(dir, "claude", ""); err == nil || scanned {
		t.Fatal("目錄讀取失敗必須回 error 且 scanned==false，不得靜默回 nil")
	}
	// 真 open error（EACCES，非 NotExist）：spec §5a 凍結「開檔失敗→回 error」。
	// 沒有這條的話，把 open 錯誤全當 NotExist 吞掉的 mutation 在整份測試存活——
	// 而那正是 transcript-only 使用者被永久遷成空 entries 的路徑。subtest 內才
	// skip：root 只失去這一段，不會把整個 TestScanLegacyWindow 標成 skipped。
	t.Run("open_error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 會繞過檔案權限，無法重現 open error")
		}
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		if _, scanned, err := scanLegacyWindow(path, "claude", ""); err == nil || scanned {
			t.Fatal("open error（非 NotExist）必須回 error 且 scanned==false，不得當 NotExist 吞掉")
		}
	})
	// 檔案存在但過濾後零筆（只放 codex 事件、查 claude）：scanned==true 且
	// got 為零筆——這是 §6a 清旗標判定「NotExist」與「掃描成功但零筆」的
	// 唯一區分依據，NotExist 必須回 scanned==false 才能與此案例互斥。
	t.Run("scanned_true_zero_matches", func(t *testing.T) {
		zeroPath := filepath.Join(dir, "codex-only.jsonl")
		if err := os.WriteFile(zeroPath, []byte(`{"event_id":"e9","provider":"codex","kind":"message","text":"only-codex"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, scanned, err := scanLegacyWindow(zeroPath, "claude", "")
		if err != nil {
			t.Fatal(err)
		}
		if !scanned {
			t.Fatal("檔案存在、掃描完整但零筆比對應回 scanned==true")
		}
		if len(got) != 0 {
			t.Fatalf("查無相符 provider 事件時 got 應為零筆：%+v", got)
		}
	})
}

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
	dir := seedTranscriptOnlyLegacyFixture(t)
	a := newTestAppAt(t, dir) // 先建 App（sink 要開得了原始 events.jsonl）再破壞檔案
	events := filepath.Join(dir, "events.jsonl")
	orig, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	assertNotMigrated := func(t *testing.T, phase string) {
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
	// subtest 內才 skip：root 下 chmod 與 restoreSessions 都不執行、狀態未動，
	// (2)(3) 的前提不受影響——Scanner.Err()／marker 斷言／修復重試在 root 仍照跑。
	t.Run("open_error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 會繞過檔案權限，無法重現 open error")
		}
		if err := os.Chmod(events, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(events, 0o644) })
		if _, err := a.restoreSessions(); err == nil {
			t.Fatal("open error 時 loadSessionRegistry 必須回錯（不得靜默跳過遷移）")
		}
		assertNotMigrated(t, "open error")
	})
	// (2) scan error：單行超過 scanner buffer 上限（16MiB）→ Scanner.Err()=ErrTooLong。
	// 與權限無關，root 也照跑。
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
	assertNotMigrated(t, "scan error")
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

// spec §8 degraded startup 防護：events.jsonl 不存在（sink 開檔失敗的降級啟動）
// 不得燒掉 migrated marker——修好環境後重試要能正常遷移。
func TestDegradedStartupMissingEventsDoesNotBurnMigratedMarker(t *testing.T) {
	dir := seedTranscriptOnlyLegacyFixture(t)
	a := newTestAppAt(t, dir)
	events := filepath.Join(dir, "events.jsonl")
	orig, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restoreSessions(); err == nil {
		t.Fatal("events.jsonl 不存在時 legacyEntries 必須回錯（不得判成確定無 transcript）")
	}
	reg := registryOnDisk(t, dir)
	if reg.Migrated() {
		t.Fatal("scanned==false 不得落 migrated marker（一次性、燒掉不可重跑）")
	}
	if n := len(reg.Live()); n != 0 {
		t.Fatalf("不得寫入 entries：%d", n)
	}
	if err := os.WriteFile(events, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	live, err := a.restoreSessions()
	if err != nil {
		t.Fatalf("恢復後重試必須成功：%v", err)
	}
	reg = registryOnDisk(t, dir)
	e, ok := reg.Get(live[0].WSID)
	if len(live) != 1 || !ok || !e.LegacyTranscript {
		t.Fatalf("重試後應正常遷移並帶 flag：%+v", live)
	}
}

// spec §8 degraded startup 防護：已遷移使用者的 backfill 在 events.jsonl 不存在
// 時不得以空候選燒掉 LegacyTranscriptBackfilled。
func TestDegradedStartupMissingEventsDoesNotBurnBackfillMarker(t *testing.T) {
	dir := seedMigratedLegacyClaudeFixture(t) // boundary 非空（"ev-0"）
	a := newTestAppAt(t, dir)
	events := filepath.Join(dir, "events.jsonl")
	orig, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restoreSessions(); err != nil {
		t.Fatalf("backfill 失敗不阻擋啟動：%v", err)
	}
	if registryOnDisk(t, dir).LegacyTranscriptBackfilled() {
		t.Fatal("scanned==false 不得落 backfill marker（一次性、燒掉不可重跑）")
	}
	if err := os.WriteFile(events, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	reg := registryOnDisk(t, dir)
	if !reg.LegacyTranscriptBackfilled() {
		t.Fatal("恢復後重試應正常落 marker")
	}
}

// seedMigratedLegacyClaudeFixture：已遷移的 stateDir——events.jsonl 有
// claude 無 WSID 事件（event_id > restore.json 的 ViewStartEventID="ev-0"）；
// workspace-sessions.json 已 migrated、一個 claude live entry
// ViewStartEventID="ev-0"（唯一候選）、一個 codex live entry 同樣
// ViewStartEventID="ev-0"（但 events.jsonl 無 codex legacy 事件——provider
// 條件的反例：ViewStart 相同也不構成候選），兩者皆無 legacy_transcript、
// legacy_transcript_backfilled=false。本系列 "ev-*" fixture 不得加入真實
// ULID turn：字典序大於 ULID 首碼 "01"，共存會破壞排序假設，需要 turn 共存
// 時改用 app_legacy_transcript_test.go 的 "00" 前綴慣例。
func seedMigratedLegacyClaudeFixture(t *testing.T) string {
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
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1", ViewStartEventID: "ev-0"},
		wsregistry.Entry{WSID: "w2", Provider: "codex", CreatedAt: "t2", ViewStartEventID: "ev-0"})
	return dir
}

// seedMigratedLegacyClaudeFixtureViewStart：同 seedMigratedLegacyClaudeFixture
// （無 codex entry），但 claude entry 的 ViewStartEventID=vs——與 restore.json
// 快照的 "ev-0" 不同，驗證 ViewStart 必須精確相等（差一字元即不算候選）。
func seedMigratedLegacyClaudeFixtureViewStart(t *testing.T, vs string) string {
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
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1", ViewStartEventID: vs})
	return dir
}

// seedMigratedTwoClaudeFixture：同 seedMigratedLegacyClaudeFixture（無 codex
// entry），但兩個 claude live entry 的 ViewStartEventID 都="ev-0"——多候選。
func seedMigratedTwoClaudeFixture(t *testing.T) string {
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
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1", ViewStartEventID: "ev-0"},
		wsregistry.Entry{WSID: "w2", Provider: "claude", CreatedAt: "t2", ViewStartEventID: "ev-0"})
	return dir
}

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

// seedMigratedEmptyViewStartTwoClaudeFixture：restore.json 的 claude 快照
// ViewStartEventID=""（首次啟動、events.jsonl 尚無內容時 freshEntries 的初始
// 值——見 restore.go:56、137-141），events.jsonl 另有無 WSID 的 claude legacy
// 事件；registry 已 migrated，兩個 claude live entry 的 ViewStartEventID 都
// 是 ""——若不擋空字串比對，會被誤判成「同一個 boundary 有兩個候選」而卡死
// （owner 否決的失效模式），本 fixture 用來重現那個形狀。
func seedMigratedEmptyViewStartTwoClaudeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"delta","role":"assistant","text":"ok"}`,
		`{"event_id":"ev-3","provider":"claude","kind":"result"}`)
	b, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: ""},
		"codex":  {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1", ViewStartEventID: ""},
		wsregistry.Entry{WSID: "w2", Provider: "claude", CreatedAt: "t2", ViewStartEventID: ""})
	return dir
}

// integration review 2026-08-22 Critical：條件 3 曾把空字串 ViewStart 當有效
// 比對值，導致快照為 "" 的已遷移使用者永遠多候選 fail loud、marker 永不落
// 盤（不會自癒）。guard（ViewStartEventID=="" 時該 provider 直接略過比對）
// 修好後，這條必須回 nil、marker 落盤、兩個 entry 都不被標記。
func TestBackfillLegacyTranscriptEmptyViewStartSkipsProvider(t *testing.T) {
	dir := seedMigratedEmptyViewStartTwoClaudeFixture(t)
	a := newTestAppAt(t, dir)
	store := registryOnDisk(t, dir)
	if err := a.backfillLegacyTranscript(store); err != nil {
		t.Fatalf("空字串 ViewStart 無可信比對證據，必須略過（等同零候選），不得 fail loud：%v", err)
	}
	if !store.LegacyTranscriptBackfilled() {
		t.Fatal("略過（零候選語意）仍應落 marker")
	}
	for _, e := range store.Live() {
		if e.LegacyTranscript {
			t.Fatal("空字串 ViewStart 不得被當成候選標記")
		}
	}
}

// §4 失敗軌跡：backfill 失敗除 startup blocker 外要留 audit.jsonl 持久軌跡。
// fixture 用多候選（確定性觸發 fail loud），經 restoreSessions 走真實接線。
func TestBackfillLegacyTranscriptFailureLeavesAudit(t *testing.T) {
	dir := seedMigratedTwoClaudeFixture(t)
	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatalf("backfill 失敗不阻擋啟動：%v", err)
	}
	// spec §4：具名事件必須帶非空的 error 內容（本 fixture 為多候選）——只驗
	// kind 存在的話，把 error 欄位清掉的 mutation 仍會綠（owner review P2）。
	// 逐行找該 kind 的那一筆，斷言同一行含失敗原因。
	b, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"kind":"legacy_transcript_backfill_failed"`) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("backfill 失敗必須留下具名 audit 事件")
	}
	if !strings.Contains(line, "候選") {
		t.Fatalf("audit 的 error 內容必須含失敗原因（多候選）：%s", line)
	}
	// startup blocker 必須對使用者可見（一次性訊息那一半，audit 是持久那一半）。
	if !strings.Contains(a.startupErrText(), "legacy transcript 標記補寫失敗") {
		t.Fatalf("startup blocker 必須可見：%q", a.startupErrText())
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

// ---- §4 startup watermark 不可用軌跡 ----
//
// 這一段刻意**不用 newTestAppAt**：它自己呼叫 openRestoreStore（見
// newTestAppAt 內 hw, _, _ := auditHighWatermark(...)），根本不經本檔要驗的
// openStateWriters（app.go:2242 一帶），而且 events.jsonl 為目錄時它在建
// sink 那一步就會 t.Fatal——照用會拿到假綠或紅錯地方。改為直接組最小 App，
// 走 a.openStateWriters(newTestStateLease(stateDir))（同 pattern：
// app_audit_lifecycle_test.go:76／app_startup_state_test.go:236）。
//
// 多段啟動一律「建 App → openStateWriters → 驗證 → shutdown → 修復檔案 →
// 下一次啟動」，shutdown 當場明確呼叫，不 defer 到測試結束——上一個 App 的
// writer handle 若還留著，下一次就不是真實重啟。

// startupWatermarkApp：openStateWriters 專用的最小 App。
func startupWatermarkApp(t *testing.T, stateDir string) *App {
	t.Helper()
	a := NewApp()
	a.ctx = context.Background()
	a.stateDir = stateDir
	return a
}

// auditLine：audit.jsonl 裡第一筆 kind 相符的整行（找不到回傳 ""）。
func auditLine(t *testing.T, stateDir, kind string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	needle := `"kind":"` + kind + `"`
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

// readRestoreJSON：直接從磁碟讀 restore.json（不經 App），驗證持久化結果。
func readRestoreJSON(t *testing.T, stateDir string) map[string]restoreEntry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "restore.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]restoreEntry
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// lastEventIDOnDisk：獨立重算 events.jsonl 最後一筆 event_id——不呼叫
// auditHighWatermark（受測邏輯的一部分），避免期望值與實作互相佐證。
func lastEventIDOnDisk(t *testing.T, stateDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			EventID string `json:"event_id"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.EventID != "" {
			last = e.EventID
		}
	}
	if last == "" {
		t.Fatal("fixture 至少要有一筆帶 event_id 的合法行")
	}
	return last
}

// TestStartupWatermarkUnavailableDegradesWithTrace：restore.json 不存在＋
// events.jsonl 為目錄（逼 auditHighWatermark 讀不到，不需權限技巧）——
// openStateWriters 必須完成（不 panic、不擋啟動），且同時留下 audit 與
// 啟動警告兩份軌跡（§4 凍結：appendMessage 是累加，不是 first-wins，sink
// 錯誤不會把它蓋掉），fallback 的 "" 必須被消費並持久化到 restore.json。
func TestStartupWatermarkUnavailableDegradesWithTrace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := startupWatermarkApp(t, dir)
	if !a.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("watermark 讀不到不得阻擋啟動，startupErr=%q", a.startupErrText())
	}
	a.shutdown(context.Background())

	line := auditLine(t, dir, "restore_watermark_unavailable")
	if line == "" {
		t.Fatal("watermark 不可用必須留下具名 audit 事件")
	}
	if !strings.Contains(line, filepath.Join(dir, "events.jsonl")) {
		t.Fatalf("audit data 必須含實際路徑，實得：%s", line)
	}
	if !strings.Contains(line, `"error"`) {
		t.Fatalf("audit data 必須含 error 說明，實得：%s", line)
	}
	if !strings.Contains(a.startupErrText(), "稽核高水位掃描失敗") {
		t.Fatalf("startupErrText 必須同時帶啟動警告，實得 %q", a.startupErrText())
	}

	entries := readRestoreJSON(t, dir)
	if entries["claude"].ViewStartEventID != "" || entries["codex"].ViewStartEventID != "" {
		t.Fatalf("watermark 不可用時 fallback 必須以空字串持久化，實得 %+v", entries)
	}
}

// TestStartupWatermarkUnavailableCompleteRestoreUntouched：restore.json 已
// 完整存在（兩 provider 皆非空 view_start）＋events.jsonl 為目錄——watermark
// 不可用是事實，audit 仍要發；但 openRestoreStore 只補缺項（restore.go:63-67），
// 既有 boundary 不得被 fallback 覆寫。
func TestStartupWatermarkUnavailableCompleteRestoreUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := map[string]restoreEntry{
		"claude": {ViewStartEventID: "existing-claude"},
		"codex":  {ViewStartEventID: "existing-codex"},
	}
	b, err := json.Marshal(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	a := startupWatermarkApp(t, dir)
	if !a.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("watermark 讀不到不得阻擋啟動，startupErr=%q", a.startupErrText())
	}
	a.shutdown(context.Background())

	if auditLine(t, dir, "restore_watermark_unavailable") == "" {
		t.Fatal("watermark 不可用是事實，即使 boundary 不變也必須發 audit")
	}
	entries := readRestoreJSON(t, dir)
	if entries["claude"].ViewStartEventID != "existing-claude" || entries["codex"].ViewStartEventID != "existing-codex" {
		t.Fatalf("既有 boundary 不得被 fallback 覆寫，實得 %+v", entries)
	}
}

// TestStartupWatermarkAvailableNoTrace（反向格）：events.jsonl 正常可讀
// （含至少一筆事件）→ openStateWriters 後不得發 restore_watermark_unavailable。
// 防未來把這條 audit 改成無條件發的 mutation。
func TestStartupWatermarkAvailableNoTrace(t *testing.T) {
	dir := t.TempDir()
	seedEvents(t, dir, `{"event_id":"ev-1","provider":"claude","kind":"message"}`)

	a := startupWatermarkApp(t, dir)
	if !a.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("正常 events.jsonl 不應阻擋啟動，startupErr=%q", a.startupErrText())
	}
	a.shutdown(context.Background())

	if auditLine(t, dir, "restore_watermark_unavailable") != "" {
		t.Fatal("watermark 可正常掃描時不得發 restore_watermark_unavailable")
	}
}

// TestStartupWatermarkUnavailableMalformedRestoreRebuilt：restore.json 為壞
// JSON＋events.jsonl 為目錄——openRestoreStore 的 malformed 分支（§4 fallback
// 三消費條件之二）以 "" 重建並持久化，openStateWriters 仍要完成。
func TestStartupWatermarkUnavailableMalformedRestoreRebuilt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := startupWatermarkApp(t, dir)
	if !a.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("malformed restore.json 不得阻擋啟動，startupErr=%q", a.startupErrText())
	}
	a.shutdown(context.Background())

	if auditLine(t, dir, "restore_watermark_unavailable") == "" {
		t.Fatal("watermark 不可用必須留下具名 audit 事件（與 malformed 重建各自獨立）")
	}
	entries := readRestoreJSON(t, dir)
	if entries["claude"].ViewStartEventID != "" || entries["codex"].ViewStartEventID != "" {
		t.Fatalf("重建後必須以空字串持久化，實得 %+v", entries)
	}
}

// TestStartupWatermarkPersistentDegradeShape：三段啟動各自當場 shutdown——
// events.jsonl 為目錄 → 修復（回填原始事件內容，但 restore.json 已有 entry，
// 不會自動修復）→ 刪除 restore.json（唯一修復路徑）→ 第三次啟動快照才等於
// 檔案上真正的最後一筆 event_id。
func TestStartupWatermarkPersistentDegradeShape(t *testing.T) {
	dir := t.TempDir()

	// 第一段：events.jsonl 為目錄 → 降級留軌跡，restore.json 以 "" 落地。
	if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	a1 := startupWatermarkApp(t, dir)
	if !a1.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("第一段：watermark 讀不到不得阻擋啟動，startupErr=%q", a1.startupErrText())
	}
	if auditLine(t, dir, "restore_watermark_unavailable") == "" {
		t.Fatal("第一段：必須留下 watermark 不可用的稽核軌跡")
	}
	entries1 := readRestoreJSON(t, dir)
	if entries1["claude"].ViewStartEventID != "" || entries1["codex"].ViewStartEventID != "" {
		t.Fatalf("第一段：fallback 必須以空字串持久化，實得 %+v", entries1)
	}
	a1.shutdown(context.Background())

	// 修復 events.jsonl：拿掉目錄、回填原始事件內容。restore.json 沒有動，
	// 已經有 entry（即使值是 ""），openRestoreStore 只補缺項，不會自動修復。
	if err := os.RemoveAll(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"delta"}`,
		`{"event_id":"ev-3","provider":"claude","kind":"result"}`)

	a2 := startupWatermarkApp(t, dir)
	if !a2.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("第二段：openStateWriters 必須成功，startupErr=%q", a2.startupErrText())
	}
	entries2 := readRestoreJSON(t, dir)
	if entries2["claude"].ViewStartEventID != "" || entries2["codex"].ViewStartEventID != "" {
		t.Fatalf("第二段：修復 events.jsonl 不會回頭改既有 restore.json 快照，實得 %+v", entries2)
	}
	a2.shutdown(context.Background())

	// 刪除 restore.json——唯一的修復路徑：openRestoreStore 走 NotExist 分支，
	// 以此刻真正掃描到的高水位重新 freshEntries。期望值從檔案內容獨立算出。
	if err := os.Remove(filepath.Join(dir, "restore.json")); err != nil {
		t.Fatal(err)
	}
	want := lastEventIDOnDisk(t, dir)

	a3 := startupWatermarkApp(t, dir)
	if !a3.openStateWriters(newTestStateLease(dir)) {
		t.Fatalf("第三段：openStateWriters 必須成功，startupErr=%q", a3.startupErrText())
	}
	entries3 := readRestoreJSON(t, dir)
	if entries3["claude"].ViewStartEventID != want || entries3["codex"].ViewStartEventID != want {
		t.Fatalf("第三段：刪除 restore.json 後應以檔案上真正的高水位重建，want=%q 實得 %+v", want, entries3)
	}
	a3.shutdown(context.Background())
}
