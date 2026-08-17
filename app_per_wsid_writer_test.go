package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// ---- M3b：per-WSID durable metadata writer（resume identity／task label／view
// boundary）。owner 2026-08-17 裁決 D1-D6 ----
//
// **這批測試的核心風險是失效形狀 (F)：只覆蓋單一 process 生命週期。**
// resume 與 view boundary 的價值全在跨重啟——同一個 App 實例「假裝重啟」守不到
// 那一維。所以每一條 durable 保證都用 restartApp（同一組目錄重開 App、重讀磁碟）
// 或新開的 wsregistry.Store 實例來驗。

// bootRegistry：走 production 的啟動序列把真實 registry 接上 App
//（Open → Migrate → restore dormant → backfill），不是塞一個 stub。
func bootRegistry(t *testing.T, a *App) *wsregistry.Store {
	t.Helper()
	if _, err := a.loadSessionRegistry(); err != nil {
		t.Fatalf("loadSessionRegistry: %v", err)
	}
	store, ok := a.wsReg.(*wsregistry.Store)
	if !ok {
		t.Fatalf("loadSessionRegistry 必須接上真實 Store，got %T", a.wsReg)
	}
	return store
}

// registerWSID：wsidFor 造出來的 slot 只存在於 Manager（它刻意不碰 registry）。
// production 的 CreateSession 一次寫兩邊，所以要驗 per-WSID durable metadata 的
// 測試必須自己補上這筆 entry。
func registerWSID(t *testing.T, a *App, w appcore.WSID, provider string) {
	t.Helper()
	if err := a.wsReg.Put(wsregistry.Entry{WSID: string(w), Provider: provider,
		TaskLabel: "seed-" + provider, CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
}

// restartApp：同一組 workspace／stateDir 重開一個 App 並跑啟動序列——**真正的
// 跨重啟**（新的 Manager、新的 registry Store、新的 claude sessions.json handle，
// 全部從磁碟重讀）。
func restartApp(t *testing.T, a *App) (*App, *uiCapture) {
	t.Helper()
	b, ui := newTestAppIn(t, a.workspaceDir, a.stateDir)
	b.toolsDirPath = a.toolsDirPath
	// audit 要早於 bootRegistry：backfill 的稽核軌跡就發在啟動序列裡
	enableAudit(t, b)
	bootRegistry(t, b)
	return b, ui
}

// registryEntry：測試斷言用的取值（找不到直接 fail，避免零值被誤讀成「已清空」）。
func registryEntryOf(t *testing.T, a *App, w appcore.WSID) wsregistry.Entry {
	t.Helper()
	e, ok := a.wsReg.Get(string(w))
	if !ok {
		t.Fatalf("registry 應有 %s 的 entry", w)
	}
	return e
}

// TestResumeIdentityIsPerWSIDAndSurvivesRestart：整張票的主要保證。
// 同 provider 兩個 session 各自 commit 出不同的 provider session id，重啟後
// 兩邊都還在、而且沒有互相污染。
//
// mutation（各自只打壞一條）：
//   - CommitResume 改回 provider-keyed（兩筆共用一個 key）→ A、B 讀到同一個 id。
//   - 把 wsReg 寫入拿掉 → 重啟後兩筆都是空字串。
func TestResumeIdentityIsPerWSIDAndSurvivesRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)

	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A 的 resume 已寫進自己的 entry", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}

	writeInitClaude(t, a, "sess-B") // 換一個會回報不同 session id 的 fake CLI
	wB := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B 的 resume 已寫進自己的 entry", func() bool {
		e, _ := a.wsReg.Get(string(wB))
		return e.ResumeSessionID == "sess-B"
	})
	if err := a.EndSession(string(wB)); err != nil {
		t.Fatal(err)
	}

	// 跨重啟：新的 App／新的 Store 實例讀磁碟
	b, _ := restartApp(t, a)
	if got := registryEntryOf(t, b, wA).ResumeSessionID; got != "sess-A" {
		t.Fatalf("重啟後 A 的續聊身分必須還在且是自己的：%q", got)
	}
	if got := registryEntryOf(t, b, wB).ResumeSessionID; got != "sess-B" {
		t.Fatalf("重啟後 B 的續聊身分必須還在且是自己的：%q", got)
	}
	if got := registryEntryOf(t, b, wA).TaskLabel; got != "task-a" {
		t.Fatalf("task label 必須跟著持久化（D3：StartSession 帶入）：%q", got)
	}
	if got := registryEntryOf(t, b, wB).TaskLabel; got != "task-b" {
		t.Fatalf("task label 必須跟著持久化：%q", got)
	}
}

// TestBothSessionsAutoResumeAfterRestart：owner 列的第一項後果——同 provider 兩個
// session 重啟後**各自**自動接續。斷言看 fake CLI 落檔的 argv，接錯對話正是從
// argv 那一刻起變成事實的。
//
// mutation：registryResume 改成讀任何一個 provider 級來源 → 兩條 argv 帶同一個 id。
func TestBothSessionsAutoResumeAfterRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)

	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	writeInitClaude(t, a, "sess-B")
	wB := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B commit", func() bool {
		e, _ := a.wsReg.Get(string(wB))
		return e.ResumeSessionID == "sess-B"
	})
	if err := a.EndSession(string(wB)); err != nil {
		t.Fatal(err)
	}

	b, _ := restartApp(t, a)
	// 重啟後兩個 session 都是 dormant slot；各自以空 resume 啟動
	argvA := writeInitClaude(t, b, "sess-A")
	if err := b.StartSession(string(wA), "again A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A 自動接續自己的對話", func() bool {
		raw, err := os.ReadFile(argvA)
		return err == nil && strings.Contains(string(raw), "--resume sess-A")
	})
	if err := b.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	argvB := writeInitClaude(t, b, "sess-B")
	if err := b.StartSession(string(wB), "again B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.EndSession(string(wB)) })
	waitFor(t, "B 自動接續自己的對話", func() bool {
		raw, err := os.ReadFile(argvB)
		return err == nil && strings.Contains(string(raw), "--resume sess-B")
	})
	raw, err := os.ReadFile(argvB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "--resume sess-A") {
		t.Fatalf("B 不得接到 A 的對話：%q", raw)
	}
}

// TestSiblingKeepsResumeWhenAnotherSessionIsRemoved：owner 列的第二項後果。
// 舊實作在 RemoveSession 裡無條件 ClearResume（provider-keyed），代價是存活的
// 手足一併失去續聊——那是 Task 26 明文記載的已知代價，這張票要把它關掉。
//
// mutation：把 tombstone 之後那段還原成 provider 級清除 → B 重啟後不再接續。
func TestSiblingKeepsResumeWhenAnotherSessionIsRemoved(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)

	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	writeInitClaude(t, a, "sess-B")
	wB := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B commit", func() bool {
		e, _ := a.wsReg.Get(string(wB))
		return e.ResumeSessionID == "sess-B"
	})
	if err := a.EndSession(string(wB)); err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	if got := registryEntryOf(t, a, wB).ResumeSessionID; got != "sess-B" {
		t.Fatalf("移除手足不得動到存活 session 的續聊身分：%q", got)
	}
	// 跨重啟：B 仍然接得回自己的對話
	b, _ := restartApp(t, a)
	argvB := writeInitClaude(t, b, "sess-B")
	if err := b.StartSession(string(wB), "again B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.EndSession(string(wB)) })
	waitFor(t, "存活手足重啟後仍自動接續", func() bool {
		raw, err := os.ReadFile(argvB)
		return err == nil && strings.Contains(string(raw), "--resume sess-B")
	})
}

// TestRemovedSessionResumeIsNotReadableAfterRestart：反向保證——tombstone 之後
// 那個 id 不得再被任何人拿去續聊，重啟後也一樣（舊實作靠 ClearResume 抹掉
// provider entry；現在靠 tombstone ＋ registryResume 的 RemovedAt 判斷）。
//
// mutation：registryResume 拿掉 RemovedAt 檢查 → 這條轉紅。
func TestRemovedSessionResumeIsNotReadableAfterRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSession(string(wA)); err != nil {
		t.Fatal(err)
	}

	b, _ := restartApp(t, a)
	if got := b.registryResume(wA); got != "" {
		t.Fatalf("已移除的 session 重啟後不得再回報續聊身分：%q", got)
	}
	// entry 本身仍在磁碟上（tombstone 必須留著，否則 index 重建會讓它復活）
	if e, ok := b.wsReg.Get(string(wA)); !ok || e.RemovedAt == "" {
		t.Fatalf("tombstone 必須保留：%+v ok=%v", e, ok)
	}
}

// ---- D4：view boundary 的 writer ＋ reader ----

// TestNewSessionViewBoundaryHidesOlderTurns：owner 凍結語意——「開新對話」之後
// LoadTurnsBefore 只回傳 boundary 之後開始的 turn。這一項的關鍵在於它**同時**
// 需要 writer（NewSession 寫 ViewStartEventID）與 reader（LoadTurnsBefore 過濾）：
// 舊實作即使寫了也沒有任何讀取端消費，所以 New 在跨重載那一維完全沒有效果。
//
// mutation（三個各自只打壞一條）：
//  1. 拿掉 NewSession 的 wsReg.ResetView → boundary 為空 → 舊 turn 全部回來。
//  2. 拿掉 LoadTurnsBefore 的 rec.FirstEventID 過濾 → 舊 turn 回來。
//  3. 拿掉尾端 tail 的 after 過濾 → 未完成 turn 的舊事件回來。
func TestNewSessionViewBoundaryHidesOlderTurns(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")
	for i := 0; i < 3; i++ {
		emitCompleteTurn(t, a, w, "old-turn")
	}
	before, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if countCompleteTurns(before) != 3 {
		t.Fatalf("前提不成立：boundary 之前應載得到 3 個 turn，got %d", countCompleteTurns(before))
	}

	if err := a.NewSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if got := registryEntryOf(t, a, w).ViewStartEventID; got == "" {
		t.Fatal("NewSession 必須持久化 view boundary")
	}
	after, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("開新對話後尾端載入不得跨越 boundary，got %d envelopes: %+v", len(after), after)
	}

	// 新的一輪必須看得到（過濾不得把整個 pane 封死）
	emitCompleteTurn(t, a, w, "new-turn")
	fresh, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if countCompleteTurns(fresh) != 1 {
		t.Fatalf("boundary 之後的新 turn 必須載得到，got %d", countCompleteTurns(fresh))
	}
	for _, e := range fresh {
		if e.Text == "old-turn" {
			t.Fatalf("boundary 之前的事件不得回來：%+v", e)
		}
	}

	// 未完成的目前 turn（沒有 turn record，靠逐筆 EventID 過濾）
	emitUserMessage(t, a, w, "still running")
	withOpen, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOpenTurn(withOpen) {
		t.Fatal("未結束 turn 仍必須載入（§3.8）")
	}
	for _, e := range withOpen {
		if e.Text == "old-turn" {
			t.Fatalf("尾端未完成 turn 的載入同樣不得跨越 boundary：%+v", e)
		}
	}
}

// TestViewBoundarySurvivesRestart：(F) 那一維。boundary 只存在記憶體的話，重啟
// 後舊對話就會整段復活——這正是 owner 觀察到的症狀。
func TestViewBoundarySurvivesRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")
	emitCompleteTurn(t, a, w, "old-turn")
	if err := a.NewSession(string(w)); err != nil {
		t.Fatal(err)
	}

	b, _ := restartApp(t, a)
	got, err := b.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("重啟後舊 turn 不得復活，got %d: %+v", len(got), got)
	}
}

// TestViewBoundaryBlocksUpwardPaging：owner 明文「尾端載入及向上分頁都不能跨越
// boundary」。cursor 分頁是另一條 code path（beforeEventID 非空時提早返回，
// 不走尾端那段），所以要單獨守。
//
// **mutation 要打在正題那句（第二個斷言），不是前提校驗**（reviewer 2026-08-17
// 更正）：拿掉 record 層過濾雖然也讓這條轉紅，但紅在上面那句「前提不成立」——
// 那只證明 boundary 有生效，沒證明**分頁**這條路徑有守門。真正只打壞這條的
// mutation 是「boundary 只在尾端載入時套用」（`if beforeEventID != "" {
// viewStart = "" }`）：實測只有本測試轉紅、且紅在下面的
// 「向上分頁不得跨越 view boundary」，其餘三條 view boundary 測試全綠。
func TestViewBoundaryBlocksUpwardPaging(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")
	for i := 0; i < 3; i++ {
		emitCompleteTurn(t, a, w, "old-turn")
	}
	if err := a.NewSession(string(w)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		emitCompleteTurn(t, a, w, "new-turn")
	}
	page, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if countCompleteTurns(page) != 2 || len(page) == 0 {
		t.Fatalf("前提不成立：boundary 之後應有 2 個 turn，got %d", countCompleteTurns(page))
	}
	// 以目前 view 最舊那個 turn 的第一筆事件當 cursor 往上翻
	up, err := a.LoadTurnsBefore(string(w), page[0].EventID, turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(up) != 0 {
		t.Fatalf("向上分頁不得跨越 view boundary，got %d: %+v", len(up), up)
	}
}

// TestViewBoundaryFiltersOpenTurnTail：boundary 落在**未完成 turn 中間**的形狀。
// 未完成 turn 沒有 turn record 可整筆判定，只能逐筆比對 EventID——這是與上面
// record-level 過濾完全不同的一條 code path，必須單獨守。
//
// mutation：把尾端 readEnvelopeRange 的 after 參數改回 "" → 只有這條轉紅
//（record-level 那幾條仍綠，證明兩者守的不是同一段）。
func TestViewBoundaryFiltersOpenTurnTail(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")

	emitUserMessage(t, a, w, "before-boundary") // 開一個 turn（不收尾）
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindDelta, Text: "boundary-event", Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, open := a.replayIndex.OpenTurnStart(string(w)); !open {
		t.Fatal("前提不成立：必須真的有一個未完成 turn，否則測不到這條路徑")
	}
	mid, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(mid) < 2 {
		t.Fatalf("前提不成立：boundary 設定前應載得到未完成 turn 的多筆事件，got %d", len(mid))
	}
	boundary := mid[len(mid)-1].EventID // 落在未完成 turn 的中間

	if err := a.wsReg.ResetView(string(w), boundary); err != nil {
		t.Fatal(err)
	}
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindDelta, Text: "after-boundary", Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := a.LoadTurnsBefore(string(w), "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	var sawAfter bool
	for _, e := range got {
		if e.EventID <= boundary {
			t.Fatalf("未完成 turn 的尾端同樣只能保留 event ID 大於 boundary 的事件：%+v", e)
		}
		if e.Text == "after-boundary" {
			sawAfter = true
		}
	}
	if !sawAfter {
		t.Fatalf("boundary 之後的事件必須留下，got %d: %+v", len(got), got)
	}
}

// ---- D2：升級 backfill ----

// seedLegacyResume：模擬「已經跑過現行 M3b build」的狀態——registry 有 entry
// 但 ResumeSessionID 是空字串，續聊身分停在 provider-keyed 的 restore.json。
func seedLegacyResume(t *testing.T, a *App, provider, id string) {
	t.Helper()
	if err := a.restore.CommitResume(provider, id, "legacy-task"); err != nil {
		t.Fatal(err)
	}
}

func TestResumeBackfillFillsSoleSessionAndSurvivesRestart(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")
	// 把 registry 打回「升級前」的樣子：entry 在、resume 空、marker 未設
	seedLegacyResume(t, a, "claude", "legacy-sess")
	clearBackfillMarker(t, a)

	b, _ := restartApp(t, a)
	if got := registryEntryOf(t, b, w).ResumeSessionID; got != "legacy-sess" {
		t.Fatalf("升級後既有 session 必須保住續聊（D2），got %q", got)
	}
	// D6：搬完就清空 restore.json 的續聊身分（檔案保留）
	if got := b.restore.Get("claude").ResumeSessionID; got != "" {
		t.Fatalf("backfill 後 restore.json 的 resume 必須清空，got %q", got)
	}
	if _, err := os.Stat(b.restore.path); err != nil {
		t.Fatalf("restore.json 不得被刪除（M3a 使用者的最後備份）：%v", err)
	}
}

// clearBackfillMarker：把 resume_backfilled marker 從磁碟上抹掉，模擬「這台機器
// 還沒跑過 backfill」。直接改檔是刻意的——marker 沒有 production 的重設入口。
func clearBackfillMarker(t *testing.T, a *App) {
	t.Helper()
	path := filepath.Join(a.stateDir, "workspace-sessions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.ReplaceAll(string(raw), `"resume_backfilled": true`, `"resume_backfilled": false`)
	if out == string(raw) {
		t.Fatalf("前提不成立：檔案裡找不到 resume_backfilled marker：%s", raw)
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 同 provider 有兩個 session 時**不猜、不填**——provider-keyed 的 legacy 記錄
// 指不出是哪一個，猜錯就是把一個 session 接到別人的對話上。
func TestResumeBackfillSkipsWhenProviderIsAmbiguous(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	bootRegistry(t, a)
	wA := mustCreate(t, a, "claude")
	wB := mustCreate(t, a, "claude")
	seedLegacyResume(t, a, "claude", "legacy-sess")
	clearBackfillMarker(t, a)

	b, _ := restartApp(t, a)
	for _, w := range []appcore.WSID{wA, wB} {
		if got := registryEntryOf(t, b, w).ResumeSessionID; got != "" {
			t.Fatalf("不明確時不得回填（%s）：%q", w, got)
		}
	}
	if !auditHas(t, a.stateDir, "resume_backfill_skipped") {
		t.Fatal("跳過 backfill 必須留稽核軌跡，不得靜默")
	}
}

// marker 的存在理由：使用者按過「開新對話」（resume 已被清空）之後重啟，不得被
// stale 的 restore.json 值再次回填、靜默復活舊對話。「只填空值」單獨擋不住這條。
//
// mutation：把 BackfillResume 的 marker 拿掉（只留「只填空值」）→ 這條轉紅。
func TestResumeBackfillDoesNotRunTwiceAfterNewSession(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := mustCreate(t, a, "claude")
	seedLegacyResume(t, a, "claude", "legacy-sess")
	clearBackfillMarker(t, a)

	b, _ := restartApp(t, a) // 第一次 backfill：填進去
	if got := registryEntryOf(t, b, w).ResumeSessionID; got != "legacy-sess" {
		t.Fatalf("前提不成立：第一次應補上，got %q", got)
	}
	// 使用者按「開新對話」：續聊身分被清空
	if err := b.NewSession(string(w)); err != nil {
		t.Fatal(err)
	}
	// 把 restore.json 的舊值放回去（模擬 D6 清除失敗、或使用者手上有舊備份）
	seedLegacyResume(t, b, "claude", "legacy-sess")

	c, _ := restartApp(t, b)
	if got := registryEntryOf(t, c, w).ResumeSessionID; got != "" {
		t.Fatalf("marker 已設，backfill 不得重跑並復活舊對話，got %q", got)
	}
}

// ---- D1：claude sessions.json 的 WSID 綁定 ----

// TestClaudeResumeRefusedForForeignWSID：owner 新增的安全要求——resume 時 cwd 與
// WSID **兩者都要符合**，WSID 不符必須 fail loud，不得只靠 cwd 放行。
//
// mutation：把 startClaude 的 boundWSID 比對拿掉 → 這條轉紅（而只比 cwd 的既有
// TestRestoredResumeReachesProvider 仍綠，證明兩條守的不是同一件事）。
func TestClaudeResumeRefusedForForeignWSID(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	cwd, _ := claude.NormalizeCWD(a.workspaceDir)
	waitFor(t, "sess-A 已綁到 A 的 WSID", func() bool {
		_, wsid, ok := a.registry.Lookup("sess-A")
		return ok && wsid == string(wA)
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}

	wB := mustCreate(t, a, "claude")
	err := a.StartSession(string(wB), "hi B", "sess-A", "", "task-b", "") // 明確貼上 A 的 id
	if err == nil {
		t.Fatal("resume 到別人的 session 必須 fail loud")
	}
	if !strings.Contains(err.Error(), "belongs to workspace session") {
		t.Fatalf("錯誤訊息必須指出是 WSID 不符（不是 cwd）：%v", err)
	}
	// 前提校驗：cwd 是相符的，所以擋下來的確實是 WSID 那一項
	if bound, _, ok := a.registry.Lookup("sess-A"); !ok || bound != cwd {
		t.Fatalf("前提不成立：cwd 應相符：%q %v", bound, ok)
	}
}

// 跨重啟：WSID 綁定必須寫在磁碟上，重啟後仍擋得住。
func TestClaudeResumeWSIDGuardSurvivesRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-A")
	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "綁定落檔", func() bool {
		_, wsid, ok := a.registry.Lookup("sess-A")
		return ok && wsid == string(wA)
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}

	b, _ := restartApp(t, a)
	wB := mustCreate(t, b, "claude")
	writeInitClaude(t, b, "sess-B")
	if err := b.StartSession(string(wB), "hi B", "sess-A", "", "task-b", ""); err == nil {
		t.Fatal("重啟後 WSID guard 必須仍擋得住（綁定是磁碟事實）")
	}
}

// 舊記錄（本 build 之前綁定、沒有 wsid 欄位）必須放行並留稽核軌跡——一律拒絕
// 會讓升級後所有既有對話失去續聊，正是 D2 backfill 要保住的東西。
func TestClaudeResumeAllowsLegacyBindingWithoutWSID(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	bootRegistry(t, a)
	argvFile := writeInitClaude(t, a, "legacy-sess") // 內部以空 wsid 預綁
	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "hi", "legacy-sess", "", "task-a", ""); err != nil {
		t.Fatalf("舊記錄必須放行：%v", err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitFor(t, "argv 帶 resume", func() bool {
		raw, err := os.ReadFile(argvFile)
		return err == nil && strings.Contains(string(raw), "--resume legacy-sess")
	})
	if !auditHas(t, a.stateDir, "claude_resume_legacy_binding") {
		t.Fatal("放行舊記錄必須留稽核軌跡")
	}
	// 放行之後 init 的 Bind 會補齊欄位，此後這條分支對該 id 不再走到
	waitFor(t, "綁定補齊 WSID", func() bool {
		_, wsid, ok := a.registry.Lookup("legacy-sess")
		return ok && wsid == string(w)
	})
}

// ---- tombstone × 寫入的全序（取代 resumeWriteAllowed 的 check-then-act）----

// TestTombstonedWSIDCannotWriteResumeFromLateInit：commitClaudeResume 那個寫入端。
// 不設 barrier 時 Accept 早於 init，StartSession 那端的 CommitResume 拿到的是空
// 字串（空 resume 保留既有值），所以真正被守的是 late init 這條。
func TestTombstonedWSIDCannotWriteResumeFromLateInit(t *testing.T) {
	a, ui := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-X")
	w := mustCreate(t, a, "claude")
	// 孤兒：registry 已 tombstone，Manager slot 還在（§3.6.2 的殘餘窗口）
	if err := a.wsReg.Remove(string(w), "user_removed"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.State(w); err != nil {
		t.Fatalf("前提不成立：孤兒 slot 必須還在 Manager 裡：%v", err)
	}
	if err := a.StartSession(string(w), "hi", "", "", "task-x", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "一輪跑完（init ＋ result 都到）", func() bool {
		return len(ui.findEnvKind("result")) >= 1
	})
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if got := registryEntryOf(t, a, w).ResumeSessionID; got != "" {
		t.Fatalf("已 tombstone 的 WSID 不得寫回 resume：%q", got)
	}
}

// StartSession 的 **claude 分支**寫入端：以 hookAfterProviderStart 等到 init 回填
// host.sessionID 才放行 Accept，這個寫入端才真的被執行到（失效形狀 D）。
func TestTombstonedWSIDCannotWriteResumeFromStartSessionClaude(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-X")
	w := mustCreate(t, a, "claude")
	if err := a.wsReg.Remove(string(w), "user_removed"); err != nil {
		t.Fatal(err)
	}
	a.hookAfterProviderStart = func() {
		waitFor(t, "init 抵達（Accept 之前）", func() bool {
			return a.hostSessionID(a.hostFor(w)) == "sess-X"
		})
	}
	if err := a.StartSession(string(w), "hi", "", "", "task-x", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	if got := a.hostSessionID(a.hostFor(w)); got != "sess-X" {
		t.Fatalf("前提不成立：Accept 當下 host.sessionID 必須非空，got %q", got)
	}
	e := registryEntryOf(t, a, w)
	if e.ResumeSessionID != "" {
		t.Fatalf("已 tombstone 的 WSID 不得由 StartSession 補寫 resume：%q", e.ResumeSessionID)
	}
	if e.TaskLabel == "task-x" {
		t.Fatalf("guard 擋掉整筆 commit，task label 同樣不得被寫入：%q", e.TaskLabel)
	}
}

// StartSession 的 **codex 分支**寫入端：threadID 恆非空，不需要 barrier。
func TestTombstonedWSIDCannotWriteResumeFromStartSessionCodex(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	w := wsidFor(t, a, contract.ProviderCodex) // startCodexForTest 啟動的就是這個 WSID
	registerWSID(t, a, w, "codex")
	if err := a.wsReg.Remove(string(w), "user_removed"); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "", "task-x")
	t.Cleanup(func() { _ = a.EndSession(string(w)) })

	if h := a.hostFor(w); h == nil || h.runner == nil || h.runner.ThreadID() == "" {
		t.Fatalf("前提不成立：codex host 必須已發布且 threadID 非空：%+v", h)
	}
	e := registryEntryOf(t, a, w)
	if e.ResumeSessionID != "" {
		t.Fatalf("已 tombstone 的 WSID 不得由 StartSession 補寫 codex resume：%q", e.ResumeSessionID)
	}
	if e.TaskLabel == "task-x" {
		t.Fatalf("guard 擋掉整筆 commit，task label 同樣不得被寫入：%q", e.TaskLabel)
	}
}
