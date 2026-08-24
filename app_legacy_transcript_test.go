package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// seedLegacyTranscriptFixtureApp：legacy window（無 WSID 的 3 筆事件）＋
// wsid="w1" 的 turnCount 個完整 turn；registry 的 w1 entry 依 legacyTranscript
// 旗標決定是否可能觸發 legacy 合併，viewStart 直接餵給 entry 的
// ViewStartEventID（""＝I1 的空 boundary 反例 fixture）。
//
// event_id 刻意全部固定為 10 位數字字串（"0000000000".."0000000003"）：
// crockford ULID 字元集只有 0-9A-Z，真正的 turn event id 一律以目前時間戳的
// crockford 編碼開頭（現在的年份下恆為 "01" 開頭），比這裡固定的 "00" 開頭
// 大，藉此保證 legacy 事件在 event_id 遞增排序下恆小於任何真實 turn，
// 不必依賴檔案內實際寫入順序。
func seedLegacyTranscriptFixtureApp(t *testing.T, legacyTranscript bool, turnCount int, viewStart string) *App {
	t.Helper()
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"0000000001","provider":"claude","kind":"message","role":"user","text":"legacy-hi"}`,
		`{"event_id":"0000000002","provider":"claude","kind":"delta","role":"assistant","text":"legacy-ok"}`,
		`{"event_id":"0000000003","provider":"claude","kind":"result"}`)
	seedRegistry(t, dir, wsregistry.Entry{
		WSID: "w1", Provider: "claude", CreatedAt: "t1",
		ViewStartEventID: viewStart, LegacyTranscript: legacyTranscript,
	})
	a := newTestAppAt(t, dir)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)
	for i := 0; i < turnCount; i++ {
		emitCompleteTurn(t, a, w, fmt.Sprintf("turn-%02d", i))
	}
	// rev2 陷阱（同 Task 7）：newTestAppAt 不接 a.wsReg，loadTurnsBefore 的
	// legacy 分支要讀 a.wsReg.Get 判斷 LegacyTranscript／Provider／ViewStart，
	// 漏接會 nil panic。
	a.wsReg = registryOnDisk(t, dir)
	return a
}

// seedLegacyPlus25TurnsApp：legacy window ＋ w1 上 25 個完整 turn，用來驗證
// 跨頁分頁時 legacy window 只在最舊 turn 頁前綴一次。
func seedLegacyPlus25TurnsApp(t *testing.T) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, true, 25, "0000000000")
}

// seedNonLegacyApp：events.jsonl 內同樣有無 WSID 的 legacy 事件，但 w1 entry
// 的 LegacyTranscript=false——驗證反向：不得因為檔案裡剛好有無 WSID 事件就
// 誤合併進來。
func seedNonLegacyApp(t *testing.T) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, false, 5, "0000000000")
}

// seedLegacyFewTurnsApp：legacy window ＋ w1 上 turnCount（<20）個完整
// turn、ViewStartEventID 非空——I2 的主線案例 fixture：turn 數不到一頁，
// 首載（beforeEventID==""）本身就是 hasOlder==false 的最舊頁，第一頁就該
// 前綴 legacy，不必等第二頁才觸發合併分支。
func seedLegacyFewTurnsApp(t *testing.T, turnCount int) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, true, turnCount, "0000000000")
}

// seedLegacyEmptyViewStartApp：LegacyTranscript=true 但 ViewStartEventID==""
// ——I1 的反例 fixture。Migrate 可能建出這種 entry（首啟空 events.jsonl、
// 使用者從未 ResetView、resume 非空放行 Migrate），此時 scanLegacyWindow 若
// 不擋，viewStart=="" 等於不做 boundary 過濾，會把整個 provider 歷史前綴進
// 最舊頁，違反 m3b §3.2.5。
func seedLegacyEmptyViewStartApp(t *testing.T, turnCount int) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, true, turnCount, "")
}

// seedLegacyFewTurnsAppNoLegacyEvents：C3（§6a）fixture——與 seedLegacyFewTurnsApp
// 的差異只在 events.jsonl **不含**任何無 WSID 的 legacy 事件，只有 w1 的
// turnCount 個完整 turn；registry 的 w1 entry LegacyTranscript=true、
// ViewStartEventID="0000000000"（非空）。用來驗證「window 確定為空」這個
// §6a 清旗標的觸發條件——若沿用 seedLegacyFewTurnsApp，window 恆非空，永遠
// 走不到清旗標分支。
func seedLegacyFewTurnsAppNoLegacyEvents(t *testing.T, turnCount int) *App {
	t.Helper()
	dir := t.TempDir()
	seedRegistry(t, dir, wsregistry.Entry{
		WSID: "w1", Provider: "claude", CreatedAt: "t1",
		ViewStartEventID: "0000000000", LegacyTranscript: true,
	})
	a := newTestAppAt(t, dir)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)
	for i := 0; i < turnCount; i++ {
		emitCompleteTurn(t, a, w, fmt.Sprintf("turn-%02d", i))
	}
	// rev2 陷阱（同 seedLegacyTranscriptFixtureApp）：newTestAppAt 不接
	// a.wsReg，loadTurnsBefore 的合併分支要讀 a.wsReg.Get。
	a.wsReg = registryOnDisk(t, dir)
	return a
}

// seedLegacyOnlyNoTurnsApp：index 零 turn record，僅 boundary 後的無 WSID
// legacy 事件——readEnvelopeRange 根本不會被呼叫，目錄注入時錯誤只可能來自
// scanLegacyWindow（TestLoadTurnsBeforeLegacyScanErrorStillGuarded 用）。
func seedLegacyOnlyNoTurnsApp(t *testing.T) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, true, 0, "0000000000")
}

// hasLegacyText：envs 內是否含至少一筆無 WorkspaceSessionID 的事件——legacy
// window 的判準（scanLegacyWindow 只保留 WorkspaceSessionID=="" 的事件，見
// restore.go）。
func hasLegacyText(envs []contract.Envelope) bool {
	for _, e := range envs {
		if e.WorkspaceSessionID == "" {
			return true
		}
	}
	return false
}

func ascendingByEventID(envs []contract.Envelope) bool {
	for i := 1; i < len(envs); i++ {
		if envs[i].EventID < envs[i-1].EventID {
			return false
		}
	}
	return true
}

// assertAllTurnsPresent：合併後的兩頁聯集必須湊滿 want 個完整 turn（不去重
// 之外不得丟 turn）。借用既有的 countCompleteTurns（app_replayindex_test.go）
// 數 terminal state_change——legacy window 的 raw kind="result" 事件是直接
// seedEvents 寫入、未經 manager 轉換，不會被誤算進去。
func assertAllTurnsPresent(t *testing.T, envs []contract.Envelope, want int) {
	t.Helper()
	if got := countCompleteTurns(envs); got != want {
		t.Fatalf("合併後應涵蓋全部 %d 個完整 turn，實得 %d", want, got)
	}
}

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

// TestLoadTurnsBeforeEmptyViewStartDoesNotPrefixLegacy：integration review
// 2026-08-23 I1——entry 的 ViewStartEventID=="" 時，即使 LegacyTranscript=true
// 也不得前綴 legacy（無可信 boundary＝無可信比對證據，比照 §4 backfill
// guard；前綴整個 provider 歷史違反 m3b §3.2.5）。
func TestLoadTurnsBeforeEmptyViewStartDoesNotPrefixLegacy(t *testing.T) {
	a := seedLegacyEmptyViewStartApp(t, 5)
	got, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(got) {
		t.Fatal("ViewStartEventID 為空時不得前綴 legacy（無可信 boundary）")
	}
	// C3（§6a）擴充：空 ViewStart guard 分支不掃描，旗標不清。
	if e, _ := registryOnDisk(t, a.stateDir).Get("w1"); !e.LegacyTranscript {
		t.Fatal("空 ViewStart 未掃描，不得清旗標")
	}
}

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

// scan error 不清：回錯且旗標仍 true。錯誤來自 turn-read 路徑
// （readEnvelopeRange fail loud，Task 1）；legacy 分支（scanLegacyWindow）的
// scan error 守門見 TestLoadTurnsBeforeLegacyScanErrorStillGuarded。
func TestLoadTurnsBeforeScanErrorDoesNotClearFlag(t *testing.T) {
	a := seedLegacyFewTurnsAppNoLegacyEvents(t, 5)
	events := filepath.Join(a.stateDir, "events.jsonl")
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.LoadTurnsBefore("w1", "", 20)
	if err == nil {
		t.Fatal("scan error 必須回錯")
	}
	if !strings.Contains(err.Error(), "read events.jsonl at") || !strings.Contains(err.Error(), "wsid=") {
		t.Fatalf("錯誤必須含 turn-read 的 offset 與 wsid 脈絡：%v", err)
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

// TestLoadTurnsBeforeFirstLoadPrefixesLegacyWhenFewTurns：spec §5／§8 主線
// 案例——legacy 使用者 turn 數不到一頁（<20）時，首載（beforeEventID==""）
// 本身就是最舊頁，必須直接前綴 legacy，不必等第二頁才觸發合併分支
// （守住「分支條件誤寫成 beforeEventID != "" && !hasOlder && ...」這類
// mutation：那樣首載永遠跳過合併，這個測試會紅）。
func TestLoadTurnsBeforeFirstLoadPrefixesLegacyWhenFewTurns(t *testing.T) {
	a := seedLegacyFewTurnsApp(t, 5)
	got, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLegacyText(got) {
		t.Fatal("turn 數不到一頁時首載必須前綴 legacy")
	}
	if !ascendingByEventID(got) {
		t.Fatal("合併後必須 event_id 遞增")
	}
	if got[0].WorkspaceSessionID != "" {
		t.Fatal("legacy（無 WSID）應排在最前")
	}
	assertAllTurnsPresent(t, got, 5)
}

// seedPreFixMigratedTwoSessionApp：Task 10 端對端 fixture——**pre-fix 形狀**：
// registry 以 seedRegistry（MarkMigrated）寫入，天然不帶任何 legacy_transcript
// 標記／marker，模擬既有升級使用者重啟當下的磁碟狀態（migration 早已在舊 build
// 跑過，這個 build 才新增 backfill 這一段）。events.jsonl 依序：無 WSID 的
// legacy window → w1 的 WSID turn（5 個，< 20，讓首載即最舊 turn 頁）→ w2 的
// WSID turn。
//
// boundary 沿用 Task 9 helper 的手法（見 seedLegacyTranscriptFixtureApp 上方
// 註解）：固定用 "00" 開頭的字串而非任意標籤（如 "ev-0"）——crockford 字元集
// 無小寫，年份 2026 下 emitCompleteTurn 產生的真實 ULID 恆以 "01" 開頭，"00"
// 前綴保證字典序小於任何真實 turn 事件 id，view boundary 過濾（
// rec.FirstEventID <= viewStart）才不會誤傷真實 turn。
//
// w1 的 ViewStartEventID 與 restore.json 的 claude 快照精確相等（backfill 五
// 條件的候選）；w2 的 ViewStartEventID 是「後建高水位」——建立 w2 之前那一刻
// 的 events.jsonl 高水位（真實 ULID，必然不等於快照），因此 w2 不成候選，backfill
// 只會標記 w1（同 provider 第二個 session 不誤接）。
func seedPreFixMigratedTwoSessionApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	const viewStart = "0000000000"

	seedEvents(t, dir,
		`{"event_id":"0000000001","provider":"claude","kind":"message","role":"user","text":"legacy-hi"}`,
		`{"event_id":"0000000002","provider":"claude","kind":"delta","role":"assistant","text":"legacy-ok"}`,
		`{"event_id":"0000000003","provider":"claude","kind":"result"}`)

	restoreJSON, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: viewStart},
		"codex":  {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.json"), restoreJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	a := newTestAppAt(t, dir)
	w1 := dormantWSID(t, a, "w1", contract.ProviderClaude)
	for i := 0; i < 5; i++ {
		emitCompleteTurn(t, a, w1, fmt.Sprintf("w1-turn-%02d", i))
	}
	w2ViewStart := auditHighWatermark(a.eventsPath())
	w2 := dormantWSID(t, a, "w2", contract.ProviderClaude)
	emitCompleteTurn(t, a, w2, "w2-turn")

	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1", ViewStartEventID: viewStart},
		wsregistry.Entry{WSID: "w2", Provider: "claude", CreatedAt: "t2", ViewStartEventID: w2ViewStart},
	)
	return a
}

// TestLegacyTranscriptEndToEndHydrate：spec §8 端對端案例——pre-fix 形狀的
// fixture 經 restoreSessions() 讓 loadSessionRegistry → backfillLegacyTranscript
// 真正接線跑過，才驗證 hydrate 可見＋同 provider 第二個 session 不誤接。
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

// TestLoadTurnsBeforeScanErrorPropagates：integration review 2026-08-22 擴充
// ——events.jsonl 換成同名目錄（Scanner.Read 回 EISDIR）必須讓 LoadTurnsBefore
// 原樣傳播錯誤，不得被靜默吞掉、少給合併結果。錯誤現在來自 turn-read 路徑
// （readEnvelopeRange fail loud，Task 1）：本 fixture 有 turn record，
// turn-read 先於 legacy scan 觸發，短路了原本守 legacy scan error 分支的路徑；
// legacy 分支（scanLegacyWindow）的 scan error 守門另見
// TestLoadTurnsBeforeLegacyScanErrorStillGuarded。
//
// 用目錄取代 chmod 0o000：0o000 在部分環境（root／euid 特例）讀取仍會成功，
// 换成同名目錄則兩個平台（Linux／Darwin）都保證 os.Open 成功、Scanner.Scan()
// 讀取時回 "is a directory" 錯誤，不必依 euid 條件式跳過。
func TestLoadTurnsBeforeScanErrorPropagates(t *testing.T) {
	a := seedLegacyFewTurnsApp(t, 5) // LegacyTranscript=true、ViewStart 非空、5 turn（首載即最舊頁）
	eventsPath := a.eventsPath()
	if err := os.Remove(eventsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(eventsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.LoadTurnsBefore("w1", "", 20)
	if err == nil {
		t.Fatal("events.jsonl 換成目錄後錯誤必須傳播，不得靜默少給 legacy")
	}
	if !strings.Contains(err.Error(), "read events.jsonl at") || !strings.Contains(err.Error(), "wsid=") {
		t.Fatalf("錯誤必須含 turn-read 的 offset 與 wsid 脈絡：%v", err)
	}
}

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

// TestLoadTurnsBeforeLegacyRespectsViewBoundary：integration review 2026-08-22
// 擴充——events.jsonl 內同時有 boundary **之前**（event_id <= ViewStart）與
// **之後**的無 WSID 事件時，首載只能前綴 boundary 之後那段（scanLegacyWindow
// 的 `e.EventID <= viewStart` 過濾要真的接進 loadTurnsBefore 的合併路徑，不是
// 只在 scanLegacyWindow 自己的單元測試綠燈）。
func TestLoadTurnsBeforeLegacyRespectsViewBoundary(t *testing.T) {
	dir := t.TempDir()
	const boundary = "0000000005"
	seedEvents(t, dir,
		`{"event_id":"0000000003","provider":"claude","kind":"message","role":"user","text":"before-a"}`,
		`{"event_id":"0000000004","provider":"claude","kind":"delta","role":"assistant","text":"before-b"}`,
		`{"event_id":"0000000005","provider":"claude","kind":"result","text":"before-eq"}`,
		`{"event_id":"0000000006","provider":"claude","kind":"message","role":"user","text":"after-a"}`,
		`{"event_id":"0000000007","provider":"claude","kind":"result","text":"after-b"}`)
	seedRegistry(t, dir, wsregistry.Entry{
		WSID: "w1", Provider: "claude", CreatedAt: "t1",
		ViewStartEventID: boundary, LegacyTranscript: true,
	})
	a := newTestAppAt(t, dir)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)
	emitCompleteTurn(t, a, w, "turn-00")
	a.wsReg = registryOnDisk(t, dir)

	got, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var legacyIDs []string
	for _, e := range got {
		if e.WorkspaceSessionID == "" {
			legacyIDs = append(legacyIDs, e.EventID)
		}
	}
	if len(legacyIDs) != 2 || legacyIDs[0] != "0000000006" || legacyIDs[1] != "0000000007" {
		t.Fatalf("首載只應含 boundary（%s）之後的 legacy 事件，實得：%v", boundary, legacyIDs)
	}
}

// TestLoadTurnsBeforeAfterResetViewNoLegacy：integration review 2026-08-22
// 擴充——ResetView 清 LegacyTranscript（Task 3）之後，loadTurnsBefore 的合併
// 分支必須真的因此不再觸發（不是只驗 ResetView 自己寫對欄位）。boundary 刻意
// 傳回**同一個值**（"0000000000"，未前移）：若改傳更大的值，scanLegacyWindow
// 自己的 boundary 過濾就會讓 legacy window 變空，即使 ResetView 沒清
// LegacyTranscript 測試也會誤判綠燈（mutation 驗證過：把 ResetView 的
// `e.LegacyTranscript = false` 拿掉、boundary 前移版本仍然通過，同值版本才會
// 抓到）。同值一樣是合法呼叫——只驗證旗標清除這一個變因，25 個 turn 全數
// 保留可同時確認 boundary 過濾沒有誤傷任何真實 turn。
func TestLoadTurnsBeforeAfterResetViewNoLegacy(t *testing.T) {
	a := seedLegacyPlus25TurnsApp(t) // legacy window + w1 25 turn，LegacyTranscript=true、ViewStart="0000000000"

	store := registryOnDisk(t, a.stateDir)
	if err := store.ResetView("w1", "0000000000"); err != nil {
		t.Fatal(err)
	}
	a.wsReg = store // 依既有測試手法重新接線（同 Task 9 fixture 尾段）

	p1, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p1) {
		t.Fatal("ResetView 之後首載不得含 legacy（無 WSID）事件")
	}
	cursor := p1[0].EventID
	p2, err := a.LoadTurnsBefore("w1", cursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p2) {
		t.Fatal("ResetView 之後最舊 turn 頁也不得含 legacy（無 WSID）事件")
	}
	assertAllTurnsPresent(t, append(append([]contract.Envelope{}, p1...), p2...), 25)
}
