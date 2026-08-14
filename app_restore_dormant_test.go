package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
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
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sink, err := appcore.NewJSONLSink(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a.manager = appcore.New(appcore.Config{Sink: sink})
	t.Cleanup(func() { _ = a.manager.Close() })
	rs, err := openRestoreStore(filepath.Join(stateDir, "restore.json"), auditHighWatermark(a.eventsPath()))
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
//（stateDir 唯讀）必須 fail loud（§3.2.6）。本 task 不做 provider 啟動，
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

	if a.startupErr != "" {
		t.Fatalf("乾淨 workspace 不應有 startup error：%s", a.startupErr)
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
