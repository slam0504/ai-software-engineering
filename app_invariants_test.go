package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

// ---- M3b Task 30：跨切面不變量（§5.7）----
//
// 本檔只放**跨越多個元件**的不變量：單一元件內部的行為由各自的 task 測試守。
// 每條測試都刻意帶一個「非空性」斷言（precondition）——不變量測試最容易退化成
// 空殼：受測路徑根本沒被走到時，`for range 空集合` 也會通過。

// readEventIDs：events.jsonl 的 event_id（依**檔案順序**，不排序）。
// 檔案順序正是不變量的受測對象，任何排序都會把它測掉。
//
// 直接沿用既有的 readEvents（app_startup_repair_test.go:30）：它已經是同樣的
// 大 buffer scanner、同樣對壞行 fail loud（並行寫入交錯就是那個形狀）、同樣
// 保留檔案順序。另外複製一份只會多一個要一起維護的解析器。
func readEventIDs(t *testing.T, stateDir string) []string {
	t.Helper()
	evs := readEvents(t, stateDir)
	ids := make([]string, 0, len(evs))
	for _, e := range evs {
		if e.EventID == "" {
			t.Fatalf("每一筆稽核事件都必須帶 event_id：%+v", e)
		}
		ids = append(ids, e.EventID)
	}
	return ids
}

// emitTurn：在 w 上經 production 的唯一事件出口送 n 筆事件。kind 刻意在
// delta ↔ result 之間交替——兩者在 reducer 上是不同狀態（streaming／done），
// 因此**每一筆**都會追發一筆合成 state_change，reducer cascade 的密度最大。
func emitTurn(t *testing.T, a *App, w appcore.WSID, n int) {
	t.Helper()
	p, ok := a.manager.ProviderOf(w)
	if !ok {
		t.Errorf("emitTurn: 未知 WSID %s", w)
		return
	}
	for i := 0; i < n; i++ {
		ev := contract.Event{Provider: p, Kind: contract.KindDelta, Text: "d"}
		if i%2 == 1 {
			ev = contract.Event{Provider: p, Kind: contract.KindResult}
		}
		if err := a.manager.Emit(w, ev); err != nil {
			t.Errorf("emitTurn(%s): %v", w, err)
			return
		}
	}
}

// allWSIDs：fixture 內全部 8 個 WSID（claude 4 ＋ codex 4）。
func (f *shutdownFixture) allWSIDs() []appcore.WSID {
	return append(append([]appcore.WSID(nil), f.claude...), f.codex...)
}

// TestEventIDMonotonicAcross8ParallelSessions：§2／Global Constraint「event_id
// 檔案級嚴格遞增」。M3b 把單一 slot 拆成 per-WSID registry，最容易破的正是這條
// ——只要有人把 event_id 配發或 sink 寫入移出那把單一 mutex，8 個 session 並行
// 送事件時檔案順序就會與 event_id 順序脫鉤。
//
// 斷言刻意分兩層：(1) 每一行都可解析（交錯寫入會產生壞行，先在 readEventIDs
// 擋掉）；(2) 檔案順序上 event_id 嚴格遞增。第二層才是不變量本體——只驗「id
// 集合遞增」而先排序過，等於把受測性質整個測掉。
func TestEventIDMonotonicAcross8ParallelSessions(t *testing.T) {
	f := seedSessions(t, 4, 4)
	a := f.a
	ws := f.allWSIDs()
	if len(ws) != 8 {
		t.Fatalf("precondition：必須有 8 個並行 session：%d", len(ws))
	}
	before := len(readEventIDs(t, a.stateDir))

	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func(w appcore.WSID) { defer wg.Done(); emitTurn(t, a, w, 20) }(w)
	}
	// workspace lane 同時併發：它走的是另一個公開入口（EmitWorkspace），與
	// session lane 共用同一把 mutex —— 少了這一條，本測試只覆蓋 session 入口。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			a.manager.EmitWorkspace(string(contract.KindStreamError), nil,
				map[string]string{"error": "invariant-probe"})
		}
	}()
	wg.Wait()

	ids := readEventIDs(t, a.stateDir)
	// 非空性：8×20 筆 trigger ＋ 各自的合成 state_change ＋ 20 筆 workspace。
	// 少於這個數量代表受測路徑沒被走到（例如 Emit 全部回錯），不得算通過。
	if got := len(ids) - before; got < 8*20+20 {
		t.Fatalf("受測路徑未被走到：本次只新增 %d 筆事件", got)
	}
	for k := 1; k < len(ids); k++ {
		if ids[k] <= ids[k-1] {
			t.Fatalf("event_id 檔案級單調被破壞（第 %d 行）：%s <= %s", k+1, ids[k], ids[k-1])
		}
	}
}

// TestReducerCascadeIsAtomicAcrossSessions：合成 state_change 與觸發它的那筆
// 事件必須在同一段鎖內連續落盤——中間不得插進任何其他 session 的事件。
//
// 為什麼要獨立守：event_id 單調只保證「順序遞增」，不保證「相鄰」。把 reducer
// cascade 移出 mutex（或改成 per-slot 鎖）之後 event_id 仍然遞增，但 A session
// 的 `delta → state_change` 之間會夾進 B session 的事件，稽核重放時 turn
// boundary 與 §3.2.3 的 open-turn 判定就會讀到錯的配對。
func TestReducerCascadeIsAtomicAcrossSessions(t *testing.T) {
	f := seedSessions(t, 4, 4)
	a := f.a

	var wg sync.WaitGroup
	for _, w := range f.allWSIDs() {
		wg.Add(1)
		go func(w appcore.WSID) { defer wg.Done(); emitTurn(t, a, w, 20) }(w)
	}
	wg.Wait()

	evs := readEvents(t, a.stateDir)
	cascades := 0
	for k, e := range evs {
		if e.Kind != string(contract.KindStateChange) {
			continue
		}
		cascades++
		if k == 0 {
			t.Fatalf("state_change 不可能是檔案第一筆（它必然由前一筆事件追發）：%+v", e)
		}
		// WSID 缺漏要先擋掉並給專屬訊息：`emitStateLocked` 忘記回填 WSID 時
		// 下面的比對同樣會不相等，但錯誤訊息會說「被其他 session 插隊」，把
		// 讀者指向完全錯的 root cause。（那條 regression 的正規守門在
		// appcore：TestEmitFillsWSIDAndRejectsProviderMismatch。）
		if e.WorkspaceSessionID == "" {
			t.Fatalf("合成 state_change 必須帶 WSID（§3.1.5，第 %d 行）：%+v", k+1, e)
		}
		if prev := evs[k-1]; prev.WorkspaceSessionID != e.WorkspaceSessionID {
			t.Fatalf("reducer cascade 被其他 session 的事件插隊（第 %d 行）：\n"+
				"trigger=%s/%s\ncascade=%s/%s",
				k+1, prev.WorkspaceSessionID, prev.Kind, e.WorkspaceSessionID, e.Kind)
		}
	}
	// 非空性門檻 8×20 的推導（實測約 168，餘裕不大，故寫清楚來源）：
	//   - emitTurn 的 delta↔result 交替讓**每一筆**都換狀態 → 20 筆／WSID；
	//     唯一的例外是第一筆 delta 撞上「該 slot 已經是 streaming」（claude 的
	//     fake CLI 在 start 那一輪就送過 assistant message），那筆不產生 change
	//     → 19 筆。
	//   - 但那種 WSID 的 start 交易本身至少貢獻 2 筆（idle→waiting、
	//     waiting→streaming），其餘 WSID 至少貢獻 1 筆（canonical user message）。
	//   兩者相加，每個 WSID 的下限都是 20 → 8×20 是安全下限，不是實測值。
	if cascades < 8*20 {
		t.Fatalf("受測路徑未被走到：只看到 %d 筆合成 state_change", cascades)
	}
}

// seedLegacyJournalFixture：M3a 形狀的 stateDir——events.jsonl 全部是**不帶
// workspace_session_id** 的舊事件，restore.json 只有 claude 有續聊身分。
// codex 是空 entry（§3.2.5：不得因此憑空多出一個 session）。
func seedLegacyJournalFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"delta","role":"assistant","text":"ok"}`,
		`{"event_id":"ev-3","provider":"claude","kind":"result"}`)
	b, err := json.Marshal(map[string]restoreEntry{
		"claude": {ViewStartEventID: "ev-0", ResumeSessionID: "sess-legacy", TaskID: "task-legacy"},
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

// TestLegacyJournalWithoutWSIDAttributes：升級路徑的跨切面不變量——M3a 的
// events.jsonl 一筆 WSID 都沒有，遷移後仍必須恰好產生一個 legacy session、
// 且它的 view window 重放得到那些無 WSID 的舊事件。
//
// 這條同時守住兩個相反方向的錯誤：多建（空 codex entry 憑空長出 session 並吃
// 掉名額）與少建（claude 的歷史對話升級後整段消失）。
func TestLegacyJournalWithoutWSIDAttributes(t *testing.T) {
	dir := seedLegacyJournalFixture(t)
	// fixture 形狀守門（Task 30 的受測前提）：舊事件必須真的沒有 WSID，
	// 否則整條測試測的是「已經遷移過的資料」，不是升級路徑。
	for _, e := range readEvents(t, dir) {
		if e.WorkspaceSessionID != "" {
			t.Fatalf("fixture 必須是 M3a 形狀（無 WSID）：%+v", e)
		}
	}

	a := newTestAppAt(t, dir)
	live, err := a.restoreSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Provider != "claude" {
		t.Fatalf("legacy 事件應歸屬遷移後的單一 legacy session：%+v", live)
	}
	if live[0].WSID == "" {
		t.Fatal("遷移出來的 entry 必須有 WSID")
	}
	if live[0].ResumeSessionID != "sess-legacy" || live[0].TaskLabel != "task-legacy" {
		t.Fatalf("legacy 續聊身分必須跟著遷移：%+v", live[0])
	}
	// RestoreViews 是 provider-keyed（見其 doc；plan 草稿寫成 WSID-keyed，
	// 但 Task 26 之後它已明文標為既有唯讀出口、不隨 WSID 化）。
	if n := len(mustRestoreViews(t, a)[live[0].Provider].Envelopes); n != 3 {
		t.Fatalf("legacy view window 內的 3 筆事件都應可重放：%d", n)
	}
	if a.manager.SlotCount("codex") != 0 {
		t.Fatalf("空的 legacy codex entry 不得建立 session（§3.2.5）：%d", a.manager.SlotCount("codex"))
	}
	if a.manager.SlotCount("claude") != 1 {
		t.Fatalf("legacy claude session 必須佔用恰好一個名額：%d", a.manager.SlotCount("claude"))
	}
}

// writeSilentClaude：fake claude CLI——讀進 stdin 但**永不回覆**。turn 因此
// 停在 in-flight（reducer waiting），不需要任何 sleep 或 timeout 就能穩定重現
// 「上一輪還沒結束」的狀態。
func writeSilentClaude(t *testing.T, a *App) {
	t.Helper()
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nwhile read -r _line; do :\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSecondInFlightTurnRejected：§1.1「每 session 至多一個進行中 turn」。
//
// Codex 端由 ThreadRunner 的 ErrTurnActive 擋住；Claude 是 stream-json stdin，
// 送第二筆不會被 CLI 拒絕，所以這條不變量必須由 appcore 的 submission
// coordinator 統一守（同一把 mutex，無 TOCTOU）。
//
// 三段刻意都驗：拒絕、拒絕**不得**留下副作用（否則 slot 從此送不出任何東西）、
// turn 結束後必須恢復——只驗中間那段的話，把 BeginSubmit 改成「永遠回錯」也會綠。
func TestSecondInFlightTurnRejected(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	writeSilentClaude(t, a)
	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "first", "", "", "task-c", ""); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })

	st, err := a.manager.State(w)
	if err != nil {
		t.Fatal(err)
	}
	if st != contract.StateWaiting {
		t.Fatalf("precondition：第一輪必須仍在進行中，實測 state=%s", st)
	}

	if err := a.SendMessage(string(w), "second"); err == nil {
		t.Fatal("每 session 至多一個進行中 turn（§1.1）")
	} else if !errors.Is(err, appcore.ErrTurnInFlight) {
		t.Fatalf("拒絕原因必須是 in-flight turn：%v", err)
	}
	// 被拒的第二筆不得留下任何痕跡。掃**全部**事件而不是只看最後一筆：
	// canonical user envelope 未必落在檔尾（provider stream 隨時可能追加事件），
	// 只看 evs[len-1] 既漏檢也會在空檔案時 index out of range。
	for _, e := range readEvents(t, a.stateDir) {
		if e.Text == "second" {
			t.Fatalf("被拒的第二筆不得寫進稽核（canonical user message 只屬於被接受的 turn）：%+v", e)
		}
	}

	// turn 結束後必須恢復：否則這條 guard 就是個永久 latch。
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindResult}); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "third"); err != nil {
		t.Fatalf("上一輪結束後必須可再送：%v", err)
	}
}

// TestInFlightTurnDoesNotBlockNewSession：§1.1 的 guard 不得變成死路。
//
// 反向護欄，與上一條成對：turn 卡住（provider 死了、result 永遠不來）時，
// 使用者仍必須能用「開新對話」脫身。若 guard 被誤放進
// beginNewSessionSubmitLocked（那條路徑的 newSessionLocked 會先 reducer.Reset），
// 卡住的 session 從此無法復原——上一條測試完全看不到這個形狀。
func TestInFlightTurnDoesNotBlockNewSession(t *testing.T) {
	a, _ := newTestApp(t)
	// 受控時鐘：NewSession 會走 claudeTeardown → appcore.CloseSequence 的 5s
	// quiesce 逾時，那是本測試裡唯一會參與 production 決策的牆鐘。改注入
	// fakeAfter 之後，逾時分支不再由真實時鐘決定——本測試驗的是「卡住的 turn
	// 不擋新對話」，不是「pump 能在 5 秒內收乾淨」。
	// 先例：app_shutdown_multi_test.go:353-354、app_lease_boundary_test.go:168-169。
	timers := newFakeAfter()
	a.afterFn = timers.After
	// 驗證邊界：fakeAfter 的 timer 除非測試自己 fireAll 否則永不觸發，因此真正的
	// pump 卡死不再有 5 秒的局部快速失敗，會一路落到 go test -timeout 才收場。
	// 這是本票接受的取捨（owner 2026-09-03 裁定），**不宣稱卡死診斷延遲已消除**。
	a.wsReg = &stubRegistry{}
	writeSilentClaude(t, a)
	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "stuck", "", "", "task-c", ""); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if st, _ := a.manager.State(w); st != contract.StateWaiting {
		t.Fatalf("precondition：turn 必須卡在進行中，實測 state=%s", st)
	}

	if err := a.NewSession(string(w)); err != nil {
		t.Fatalf("卡住的 turn 不得擋住「開新對話」：%v", err)
	}
	// 接線鑑別：CloseSequence 一定會呼叫 WaitQuiesce(done, 5s, after)，而 Go 的
	// select 在挑分支前會先求值每個 case 的 channel 運算元——因此 after(5s) 必被
	// 呼叫一次，與 done 是否已關無關。這一行是「seam 真的被接上」的確定性證據：
	// 拿掉上面的 a.afterFn = timers.After，這裡立刻紅，不需要慢 fixture。
	if n := timers.totalCreated(); n == 0 {
		t.Fatal("afterFn seam 未被接上：CloseSequence 應至少向注入時鐘要過一次 timer")
	}
	if a.manager.IsActive(w) {
		t.Fatal("開新對話之後 slot 必須回到 idle（不再 active）")
	}
	// 這一步才是 guard 位置的守門：reducer 此刻仍停在 waiting（reset 發生在
	// BeginNewSessionSubmit 內），若 guard 被誤加進那條路徑，這裡會 fail loud。
	if err := a.StartSession(string(w), "fresh", "", "", "task-c2", ""); err != nil {
		t.Fatalf("開新對話之後必須可再啟動：%v", err)
	}
	if !a.manager.IsActive(w) {
		t.Fatal("重新啟動之後 slot 必須是 active")
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
}

// TestWorkspaceLaneHasNoWSID：§3.1.6——workspace lane 與 conversation lane 是
// 兩條獨立的軌。workspace 事件不得帶 WSID，也不得佔用任何 session 名額。
//
// 反面守門刻意都在：測試同時建立兩個真的 session 並送 session lane 事件，
// 否則「沒有任何事件帶 WSID」也會讓迴圈通過（空殼形狀之一）。
func TestWorkspaceLaneHasNoWSID(t *testing.T) {
	a := newTestAppGit(t)
	wc := wsidFor(t, a, contract.ProviderClaude)
	wx := wsidFor(t, a, contract.ProviderCodex)
	before := a.manager.SlotCount("claude") + a.manager.SlotCount("codex")
	if before != 2 {
		t.Fatalf("precondition：兩個 session 名額：%d", before)
	}
	emitTurn(t, a, wc, 2)
	emitTurn(t, a, wx, 2)

	// production 的 workspace lane 出口：gate 的 SubmitForApproval 走
	// gateEmitter → Manager.EmitWorkspace。
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	if _, err := a.SubmitForApproval(); err != nil {
		t.Fatal(err)
	}

	var workspace, sessionWithWSID int
	for _, ev := range readEvents(t, a.stateDir) {
		if ev.Scope == "workspace" {
			workspace++
			if ev.WorkspaceSessionID != "" {
				t.Fatalf("workspace lane 不得帶 WSID（§3.1.6）：%+v", ev)
			}
			continue
		}
		if ev.WorkspaceSessionID != "" {
			sessionWithWSID++
		}
	}
	if workspace == 0 {
		t.Fatal("受測路徑未被走到：沒有任何 workspace lane 事件")
	}
	if sessionWithWSID == 0 {
		t.Fatal("空殼守門：同一份 events.jsonl 內必須有帶 WSID 的 conversation lane 事件，" +
			"否則上面的迴圈驗不到任何東西")
	}
	if got := a.manager.SlotCount("claude") + a.manager.SlotCount("codex"); got != before {
		t.Fatalf("workspace lane 不得計入 slot：%d → %d", before, got)
	}
}

// wireFrames：一份 generation 錄流檔的全部 frame（依檔案順序）。
func wireFrames(t *testing.T, path string) []struct {
	Frame int             `json:"frame"`
	Dir   string          `json:"dir"`
	Raw   json.RawMessage `json:"raw"`
} {
	t.Helper()
	type row = struct {
		Frame int             `json:"frame"`
		Dir   string          `json:"dir"`
		Raw   json.RawMessage `json:"raw"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []row
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var r row
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatalf("wire log 有無法解析的行：%q", ln)
		}
		out = append(out, r)
	}
	return out
}

// startCodexTurnOn：以 production 的完整 start 交易在 w 上開一個 codex session
// 並送出**指定 prompt** 的第一輪（startCodexOn 的 prompt 是寫死的 "hi"，而本
// 檔要靠 prompt 內容分辨兩個 session 的 frame）。
func startCodexTurnOn(t *testing.T, a *App, conn *codex.Conn, w appcore.WSID, prompt string) string {
	t.Helper()
	id, err := a.manager.BeginNewSessionSubmit(w, "task-"+prompt)
	if err != nil {
		t.Fatalf("BeginNewSessionSubmit(%s): %v", w, err)
	}
	threadID, _, serr := a.startCodexHost(w, fakeCodexHost{conn}, prompt, "", "", "untrusted")
	if serr != nil {
		t.Fatalf("startCodexHost(%s): %v", w, serr)
	}
	if err := a.manager.AcceptSubmit(w, id, threadID, prompt); err != nil {
		t.Fatalf("AcceptSubmit(%s): %v", w, err)
	}
	return threadID
}

// TestWireLogCapturesFramesOfEverySession：§3.4.1／§3.4.5——一個 app-server
// generation 一份 connection-wide 錄流，**兩個方向、所有 session** 的 frame 都
// 必須在裡面，且不得因為「當下解不出 WSID」而被丟棄。
//
// 跨切面之處：session A／B 各自的 thread/start 與 turn/start 是不同 host 發出
// 的（c2s），turn 完成通知是共用 conn 的 read loop 收的（s2c），三段合起來才
// 證明「共用一條 conn 的多 session 錄流不漏、不串線成兩份檔」。
func TestWireLogCapturesFramesOfEverySession(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	gen, err := wirelog.NewGeneration(a.wireLogDir(), "wire-e2e", a.resolveWireFrameWSID)
	if err != nil {
		t.Fatal(err)
	}
	srv := newFakeServerOn(conn)
	// production 的掛載點：always-on、早於 handshake（§3.4.1）。
	if _, err := codex.NewGenerationOwner(srv, gen); err != nil {
		t.Fatalf("always-on 錄流必須在 handshake 之前掛上：%v", err)
	}
	script := newCodexScript(t, wire, "th-A", "th-B")
	handshakeFake(t, conn)

	wa, wb := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	thA := startCodexTurnOn(t, a, conn, wa, "prompt-A")
	thB := startCodexTurnOn(t, a, conn, wb, "prompt-B")
	if thA == thB {
		t.Fatalf("precondition：兩個 session 必須拿到不同 thread：%s", thA)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wa)); _ = a.EndSession(string(wb)) })

	script.completeTurn(thA, thA+"-turn-1")
	script.completeTurn(thB, thB+"-turn-1")
	path := filepath.Join(a.wireLogDir(), "wire-e2e.jsonl")
	// waitFor 的 what 字串刻意指名 §3.4.5：這裡逾時**不是環境問題**，而是
	// 「有 frame 沒被錄下來」——turn/completed 是 notification（無 request id），
	// 正是最容易被「解不出歸屬就丟掉」那類 bug 吃掉的一種 frame。
	waitFor(t, "兩個 session 的 turn/completed 都進 connection-wide 錄流"+
		"（逾時＝frame 被丟棄，違反 §3.4.5「無法歸屬的 frame 仍須照寫」）", func() bool {
		b, rerr := os.ReadFile(path)
		return rerr == nil && strings.Count(string(b), codex.MethodTurnCompleted) >= 2
	})

	rows := wireFrames(t, path)
	if len(rows) == 0 {
		t.Fatal("受測路徑未被走到：錄流檔空的")
	}
	// (1) frame 編號 = 檔案順序，連續無洞：漏寫或並行交錯都會在這裡破。
	for i, r := range rows {
		if r.Frame != i {
			t.Fatalf("frame 編號必須與檔案順序一致且無洞：第 %d 行 frame=%d", i, r.Frame)
		}
		if len(r.Raw) == 0 {
			t.Fatalf("frame 原文不得為空：%+v", r)
		}
	}
	// (2) 兩個方向都要在（少了 c2s 就只錄到 server 說的話）。
	dirs := map[string]int{}
	for _, r := range rows {
		dirs[r.Dir]++
	}
	if dirs[string(wirelog.DirClientToServer)] == 0 || dirs[string(wirelog.DirServerToClient)] == 0 {
		t.Fatalf("兩個方向的 frame 都必須錄到：%+v", dirs)
	}
	// (3) 兩個 session 各自的 frame 都在同一份檔案內、且不得只錄到其中一個。
	blob := ""
	for _, r := range rows {
		blob += string(r.Raw)
	}
	for _, want := range []string{thA, thB, "prompt-A", "prompt-B",
		codex.MethodThreadStart, codex.MethodTurnStart, codex.MethodTurnCompleted} {
		if !strings.Contains(blob, want) {
			t.Fatalf("connection-wide 錄流缺 %q（%d frames）", want, len(rows))
		}
	}
	// (4) 只有一份實體錄流檔（§3.4：connection-wide，不是 per-session）。
	logs, _ := filepath.Glob(filepath.Join(a.wireLogDir(), "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("一個 generation 只能有一份錄流檔：%v", logs)
	}
}
