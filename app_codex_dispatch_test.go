package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- M3b Task 9：Codex dispatcher（共用 conn 上的 per-WSID 分流）----
//
// Claude 是每個 session 一個子行程，隔離靠行程；Codex 所有 session 共用同一條
// codex.Conn，隔離只能靠「每個 s2c frame 依 threadId／turnId 歸屬到正確 WSID」。
// 本檔守住那條分流：不串線、pending start 窗口、兩種 requestApproval 的 identity
// 路由、歸屬失敗 fail loud、server 級廣播不 fail loud。

// codexScript：fake app-server 的 per-thread 腳本——依 thread/start 的抵達順序
// 依序發出 threadIDs 裡的 id，turn id 以 "<threadID>-turn-N" 產生，讓斷言能從
// turn id 反推它屬於哪個 thread。
type codexScript struct {
	wire      *fakeCodexWire
	mu        sync.Mutex
	threadIDs []string
	nextStart int
	turnSeq   map[string]int
	// beforeStartResponse：thread/start response 送出**之前**執行（收到的 threadID
	// 已決定但 client 還不知道）——pending start 窗口的注入點。
	beforeStartResponse func(threadID string)
	// beforeTurnResponse：turn/start response 送出之前執行——completed-before-response
	// 這類惡意順序的注入點。
	beforeTurnResponse func(threadID, turnID string)
}

func newCodexScript(t *testing.T, wire *fakeCodexWire, threadIDs ...string) *codexScript {
	t.Helper()
	s := &codexScript{wire: wire, threadIDs: threadIDs, turnSeq: map[string]int{}}
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart, codex.MethodThreadResume:
			s.mu.Lock()
			th := s.threadIDs[min(s.nextStart, len(s.threadIDs)-1)]
			s.nextStart++
			hook := s.beforeStartResponse
			s.mu.Unlock()
			if hook != nil {
				hook(th)
			}
			wire.send(map[string]any{"id": *f.ID,
				"result": map[string]any{"thread": map[string]any{"id": th}}})
		case codex.MethodTurnStart:
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(f.Params, &p)
			s.mu.Lock()
			s.turnSeq[p.ThreadID]++
			turnID := fmt.Sprintf("%s-turn-%d", p.ThreadID, s.turnSeq[p.ThreadID])
			hook := s.beforeTurnResponse
			s.mu.Unlock()
			if hook != nil {
				hook(p.ThreadID, turnID)
			}
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		}
	})
	return s
}

// notify／completeTurn：s2c 通知的送出捷徑（wire 是 fake app-server 端）。
func (s *codexScript) notify(method string, params map[string]any) {
	s.wire.send(map[string]any{"method": method, "params": params})
}

func (s *codexScript) completeTurn(threadID, turnID string) {
	s.notify(codex.MethodTurnCompleted, map[string]any{"threadId": threadID,
		"turn": map[string]any{"id": turnID, "status": "completed"}})
}

// startCodexSession：走 production 的 startCodexHost（同 StartSession 的 codex
// 分支）在指定 WSID 上開一個 codex session，回傳 threadID。
func startCodexSession(t *testing.T, a *App, conn *codex.Conn, w appcore.WSID) string {
	t.Helper()
	threadID, _, err := a.startCodexHost(w, fakeCodexHost{conn}, "hi", "", "", "untrusted")
	if err != nil {
		t.Fatalf("startCodexHost(%s): %v", w, err)
	}
	return threadID
}

// envsForWSID：UI 收到的、歸屬於某個 WSID 的 envelope。
func envsForWSID(ui *uiCapture, w appcore.WSID) []contract.Envelope {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	var out []contract.Envelope
	for _, e := range ui.envs {
		if e.WorkspaceSessionID == string(w) {
			out = append(out, e)
		}
	}
	return out
}

func containsText(envs []contract.Envelope, text string) bool {
	for _, e := range envs {
		if strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

func containsKind(envs []contract.Envelope, kind string) bool {
	for _, e := range envs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// dispatchErrors：failLoudCodexDispatch 走 workspace lane 的 stream_error。
func dispatchErrors(ui *uiCapture) []contract.Envelope {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	var out []contract.Envelope
	for _, e := range ui.envs {
		// EmitWorkspace 把訊息放在 Payload（不是 Envelope.Error）——與
		// failLoudPlanWatch／failLoudSpecWatch 同形。
		if e.Scope == "workspace" && e.Kind == string(contract.KindStreamError) &&
			strings.Contains(string(e.Payload), "codex:") {
			out = append(out, e)
		}
	}
	return out
}

// 核心迴歸：共用 conn 上的兩個 thread 不得串線。原本 currentRunner()／
// provider-keyed Emit 會把兩個 session 的事件全部倒進同一個 slot。
func TestCodexTwoThreadsDoNotCrossWire(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	script := newCodexScript(t, wire, "th-1", "th-2")
	handshakeFake(t, conn)

	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	th1 := startCodexSession(t, a, conn, w1)
	th2 := startCodexSession(t, a, conn, w2)
	if th1 == th2 {
		t.Fatalf("兩個 session 必須拿到相異 thread：%s / %s", th1, th2)
	}

	// th2 的 delta 只能落在 w2；th1 的 approval 只能歸 w1。
	script.notify(codex.MethodAgentMessageDelta,
		map[string]any{"threadId": th2, "delta": "for-w2"})
	waitFor(t, "w2 收到自己的 delta", func() bool {
		return containsText(envsForWSID(ui, w2), "for-w2")
	})
	if containsText(envsForWSID(ui, w1), "for-w2") {
		t.Fatal("notification 串線到 w1")
	}

	apprDone := make(chan map[string]string, 1)
	go func() {
		apprDone <- a.codexApproval(codex.MethodFileChangeRequestApproval,
			[]byte(fmt.Sprintf(`{"threadId":%q,"turnId":%q,"itemId":"item-1"}`, th1, th1+"-turn-1")))
	}()
	id := waitForApprovalID(t, ui)
	if pa := a.pendingByID(id); pa == nil || pa.wsid != w1 {
		t.Fatalf("approval 必須帶提出請求的 WSID：%+v", pa)
	}
	if err := a.ResolveApproval(id, false, ""); err != nil {
		t.Fatal(err)
	}
	<-apprDone
	if containsKind(envsForWSID(ui, w2), string(contract.KindApproval)) {
		t.Fatal("approval 串線到 w2")
	}
	if errs := dispatchErrors(ui); len(errs) != 0 {
		t.Fatalf("正常分流不得 fail loud：%+v", errs)
	}
}

// item/commandExecution/requestApproval 的 identity 路由（Task 0 的 live probe
// 只驗到 fileChange 形態，command 形態只有 pinned schema 的 required 保證，故在
// 這裡以 fake wire 覆蓋）。送錯 session 是 P1 級正確性問題：使用者會在 A 的
// 對話框上核可 B 正在跑的指令。
func TestCodexCommandExecutionApprovalRoutesByIdentity(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	newCodexScript(t, wire, "th-1", "th-2")
	handshakeFake(t, conn)

	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	_ = startCodexSession(t, a, conn, w1)
	th2 := startCodexSession(t, a, conn, w2)

	done := make(chan map[string]string, 1)
	go func() {
		done <- a.codexApproval(codex.MethodCmdExecRequestApproval,
			[]byte(fmt.Sprintf(`{"threadId":%q,"turnId":%q,"itemId":"exec-1","command":"rm -rf /"}`,
				th2, th2+"-turn-1")))
	}()
	id := waitForApprovalID(t, ui)
	pa := a.pendingByID(id)
	if pa == nil || pa.wsid != w2 {
		t.Fatalf("commandExecution approval 必須歸屬到 th2 的 WSID %s：%+v", w2, pa)
	}
	if err := a.ResolveApproval(id, false, "nope"); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got["decision"] != "decline" {
		t.Fatalf("deny 必須回 decline：%+v", got)
	}
	if containsKind(envsForWSID(ui, w1), string(contract.KindApproval)) {
		t.Fatal("approval 串線到 w1")
	}
	if !containsKind(envsForWSID(ui, w2), string(contract.KindApproval)) {
		t.Fatal("approval envelope 必須落在 w2")
	}
}

// completed-before-response（走 production StartSession 路徑）：Task 0 的 live
// probe 不判定此項——真 server 未自然產生此順序——故由這裡的 fake wire 鎖住。
// 同時涵蓋 pending start 窗口：thread/started 在 thread/start 的 response 之前
// 抵達，此時 client 還不知道 threadId，只能靠 pending 登記歸屬。
func TestCodexCompletedBeforeResponseOnProductionPath(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	// 另一個 committed 但沒有 host 的 codex WSID：pending start 期間的事件若歸屬
	// 錯誤，最可能落到的就是這種「已存在的別的 slot」。刻意不讓它有 live host
	// ——legacyWSIDFor 第 1 順位會優先回唯一的 live host，那樣 StartSession 就會
	// 開在 wOther 上，測不到本測試要驗的東西。
	wOther := mustCreate(t, a, "codex")
	w := mustCreate(t, a, "codex") // 第 2 順位：最近一次 CreateSession
	script := newCodexScript(t, wire, "th-late")
	script.beforeStartResponse = func(threadID string) { // pending start 窗口
		script.notify(codex.MethodThreadStarted, map[string]any{"threadId": threadID})
	}
	script.beforeTurnResponse = func(threadID, turnID string) { // 惡意順序
		script.completeTurn(threadID, turnID)
	}
	handshakeFake(t, conn) // 握手必須在 script 安裝 onReq 之後（否則 initialize 無人回應）
	a.codexHostOverride = fakeCodexHost{conn}
	if err := a.StartSession("codex", "hi", "", "", "task-late", "untrusted"); err != nil {
		t.Fatal(err)
	}

	h := a.hostFor(w)
	if h == nil || h.runner == nil {
		t.Fatalf("host 必須掛在 %s 上：%+v", w, h)
	}
	if h.runner.ActiveTurnID() != "" { // earlyEnded latch 對消：不殘留 busy
		t.Fatalf("completed-before-response 保證遺失：busy=%q", h.runner.ActiveTurnID())
	}
	waitFor(t, "w 收到 result", func() bool {
		return containsKind(envsForWSID(ui, w), string(contract.KindResult))
	})
	// pending start 期間的 thread/started 與 turn/completed 都不得落到別的 WSID。
	for _, e := range envsForWSID(ui, wOther) {
		if e.Kind == string(contract.KindResult) || strings.Contains(string(e.Payload), "th-late") {
			t.Fatalf("pending start 期間的事件落到其他 WSID：%+v", e)
		}
	}
	if errs := dispatchErrors(ui); len(errs) != 0 {
		t.Fatalf("pending start 窗口內的通知必須歸屬得到，不得 fail loud：%+v", errs)
	}
	if err := a.EndSession("codex"); err != nil {
		t.Fatalf("end: %v", err)
	}
}

// 本應帶 identity 卻歸屬不到的 frame → fail loud（不得落到「當前那個」session）。
func TestCodexUnattributableFrameFailsLoud(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	newCodexScript(t, wire, "th-1")
	handshakeFake(t, conn)
	w := mustCreate(t, a, "codex")
	_ = startCodexSession(t, a, conn, w)

	err := a.dispatchCodexNotification(codex.MethodAgentMessageDelta,
		[]byte(`{"threadId":"unknown-thread","delta":"stray"}`))
	if err == nil {
		t.Fatal("無法歸屬必須 fail loud，不得落到『當前』session")
	}
	// 缺 identity 的 thread-scoped 通知同樣是缺口（pending start 窗口已關）。
	if err := a.dispatchCodexNotification(codex.MethodAgentMessageDelta,
		[]byte(`{"delta":"no-identity"}`)); err == nil {
		t.Fatal("thread-scoped 通知缺 identity 必須 fail loud")
	}
	if containsText(envsForWSID(ui, w), "stray") || containsText(envsForWSID(ui, w), "no-identity") {
		t.Fatal("歸屬不到的事件不得倒進既有 session")
	}
	// 經 conn 進來時走同一條路：workspace lane 的 stream_error。
	wire.send(map[string]any{"method": codex.MethodAgentMessageDelta,
		"params": map[string]any{"threadId": "another-unknown", "delta": "x"}})
	waitFor(t, "fail loud 進 workspace lane", func() bool { return len(dispatchErrors(ui)) > 0 })
}

// server／帳號層廣播本來就不帶 threadId（Task 0 live probe 實據）——歸類為廣播、
// 不進 WSID 路由、**不 fail loud**；照計畫字面「無法歸屬即 fail loud」實作會讓 app
// 在正常運作下持續報錯。remoteControl/status/changed 不在 codex.ServerNotifications
// 白名單內，會走 OnUnknown，因此兩條路徑都要驗。
func TestCodexServerBroadcastsDoNotFailLoud(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	newCodexScript(t, wire, "th-1")
	handshakeFake(t, conn)
	w := mustCreate(t, a, "codex")
	_ = startCodexSession(t, a, conn, w)
	before := len(envsForWSID(ui, w))

	if err := a.dispatchCodexNotification(codex.MethodAccountRateLimitsUpdated,
		[]byte(`{"rateLimits":{"primary":{"usedPercent":1}}}`)); err != nil {
		t.Fatalf("account/rateLimits/updated 是 server 級廣播，不得 fail loud：%v", err)
	}
	if err := a.dispatchCodexUnknown(
		[]byte(`{"method":"remoteControl/status/changed","params":{"status":"connected"}}`)); err != nil {
		t.Fatalf("remoteControl/status/changed 是 server 級廣播，不得 fail loud：%v", err)
	}
	if errs := dispatchErrors(ui); len(errs) != 0 {
		t.Fatalf("廣播不得產生 fail-loud 通知：%+v", errs)
	}
	if got := len(envsForWSID(ui, w)); got != before {
		t.Fatalf("廣播不得落進任何 session slot：%d → %d", before, got)
	}
	// 兩者都要真的送出去（走 workspace lane），不是被靜默丟掉。
	broadcasts := 0
	ui.mu.Lock()
	for _, e := range ui.envs {
		if e.Scope == "workspace" && e.Kind == string(contract.KindCodexBroadcast) {
			broadcasts++
		}
	}
	ui.mu.Unlock()
	if broadcasts != 2 {
		t.Fatalf("廣播必須落 workspace lane：got %d, want 2", broadcasts)
	}

	// 解不開的 frame（既無 method 也無 identity）同樣不 fail loud，但 raw 不得遺失
	// ——遷移前的 KindUnknown 事件保留 raw，換 lane 之後也必須保留。
	if err := a.dispatchCodexUnknown([]byte(`{"garbage":`)); err != nil {
		t.Fatalf("解不開的 frame 不得 fail loud：%v", err)
	}
	sawRaw := false
	ui.mu.Lock()
	for _, e := range ui.envs {
		if e.Kind == string(contract.KindCodexBroadcast) && strings.Contains(string(e.Payload), "garbage") {
			sawRaw = true
		}
	}
	ui.mu.Unlock()
	if !sawRaw {
		t.Fatal("解不開的 frame 必須以 raw 形式保留在 workspace lane")
	}
}

// review Important 迴歸（notification 形態）：pending start **窗口內**，缺 identity
// 的 frame 一樣必須 fail loud。pending tier 的存在理由是「frame 帶著 client 還不知道
// 的 threadId」，從來不是為「完全沒有 identity 的 frame」準備的；少了 identity 非空
// 的條件，一筆 server bug 造成缺 threadId 的白名單通知只要剛好落在窗口內，就會被靜默
// 塞進「正在啟動的那個 session」。
//
// 既有的 TestCodexUnattributableFrameFailsLoud 測不到這個形狀：它的缺 identity 斷言
// 跑在 start 完成之後，pending 早已清空。
func TestCodexMissingIdentityNotificationInPendingWindowFailsLoud(t *testing.T) {
	a, ui := newTestApp(t)
	probe := newPendingWindowProbe(t, a)

	var windowOpenErr, noIdentityErr error
	probe.script.beforeStartResponse = func(threadID string) {
		// 自我校驗：這一刻 pending 窗口確實開著（否則本測試什麼都守不住）。
		probe.noteWindowOpen(threadID)
		windowOpenErr = a.dispatchCodexNotification(codex.MethodThreadStarted,
			fmt.Appendf(nil, `{"threadId":%q}`, threadID))
		// 白名單內、但缺 identity 的 thread-scoped 通知
		noIdentityErr = a.dispatchCodexNotification(codex.MethodThreadStatusChanged,
			[]byte(`{"status":"idle"}`))
	}
	probe.start(t)
	probe.assertWindowWasOpen(t, windowOpenErr)

	if noIdentityErr == nil {
		t.Fatal("pending 窗口內缺 identity 的白名單通知必須 fail loud")
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	for _, e := range ui.envs {
		if e.Scope == "workspace" { // fail-loud 通知本來就走 workspace lane
			continue
		}
		if strings.Contains(string(e.Raw), codex.MethodThreadStatusChanged) {
			t.Fatalf("缺 identity 的通知落進 session slot：%+v", e)
		}
	}
}

// review Important 迴歸（approval 形態）：後果比 notification 更重——核可對話框會掛到
// 「正在啟動的那個 session」而不是 fail loud ＋ fail-closed decline。這正是本 task 的
// 核心動機形狀。
func TestCodexMissingIdentityApprovalInPendingWindowFailsClosed(t *testing.T) {
	a, ui := newTestApp(t)
	probe := newPendingWindowProbe(t, a)

	var (
		decision     string
		decided      bool
		dialogRaised bool
	)
	probe.script.beforeStartResponse = func(threadID string) {
		probe.noteWindowOpen(threadID)
		// 觀察必須完整發生在 pending 窗口**之內**：hook 一返回，wire 就會送出
		// thread/start response、窗口隨即關閉，之後才跑的 approval 測不到本測試
		// 要守的東西。
		//
		// codexApproval 本身開 goroutine 跑：歸屬成功的 approval 會阻塞等使用者
		// 裁決，在 hook 裡同步呼叫會連帶卡住 response，把「掛到錯的 session」這個
		// 症狀掩蓋成 EnsureThread 逾時。這裡改為在窗口內輪詢兩個確定性觀察點——
		// 「立即回 decline」（正確）或「掛出核可對話框」（錯誤）。
		dialogsBefore := len(ui.find("approval:request"))
		done := make(chan string, 1)
		go func() {
			done <- a.codexApproval(codex.MethodCmdExecRequestApproval,
				[]byte(`{"itemId":"exec-1","command":"rm -rf /"}`))["decision"]
		}()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case d := <-done:
				decision, decided = d, true
				return
			default:
			}
			if len(ui.find("approval:request")) != dialogsBefore {
				dialogRaised = true
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	probe.start(t)
	probe.assertWindowWasOpen(t, nil)

	if dialogRaised {
		t.Fatal("缺 identity 的 approval 不得掛出核可對話框（會掛到正在啟動的那個 session）")
	}
	if !decided {
		t.Fatal("pending 窗口內缺 identity 的 approval 必須立即 fail closed，不得掛起等待裁決")
	}
	if decision != "decline" {
		t.Fatalf("pending 窗口內缺 identity 的 approval 必須 fail closed：%q", decision)
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	for _, e := range ui.envs {
		if e.Scope != "workspace" && e.Kind == string(contract.KindApproval) {
			t.Fatalf("缺 identity 的 approval 落進 session slot：%+v", e)
		}
	}
}

// pendingWindowProbe：上面兩個測試共用的骨架——一個 codex WSID、一份 fake wire
// script，並在 thread/start 的 pending 窗口內提供注入點。noteWindowOpen ＋
// assertWindowWasOpen 是**測試自身的有效性檢查**：確認注入點真的跑了、而且那一刻
// pending tier 真的能歸屬（否則「缺 identity 會 fail loud」會因為窗口根本沒開而恆真）。
type pendingWindowProbe struct {
	app     *App
	script  *codexScript
	wsid    appcore.WSID
	hookRan bool
	gotWSID appcore.WSID
	gotOK   bool
}

func newPendingWindowProbe(t *testing.T, a *App) *pendingWindowProbe {
	t.Helper()
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	p := &pendingWindowProbe{app: a, script: newCodexScript(t, wire, "th-pending")}
	handshakeFake(t, conn)
	p.wsid = mustCreate(t, a, "codex")
	p.script.beforeTurnResponse = func(threadID, turnID string) { // 收掉 turn，末尾才 End 得掉
		p.script.completeTurn(threadID, turnID)
	}
	a.codexHostOverride = fakeCodexHost{conn}
	return p
}

func (p *pendingWindowProbe) noteWindowOpen(threadID string) {
	p.hookRan = true
	p.gotWSID, p.gotOK = p.app.codexWSIDFor(threadID, "")
}

func (p *pendingWindowProbe) start(t *testing.T) {
	t.Helper()
	a := p.app
	if err := a.StartSession("codex", "hi", "", "", "task-pending", "untrusted"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession("codex") })
}

func (p *pendingWindowProbe) assertWindowWasOpen(t *testing.T, windowOpenErr error) {
	t.Helper()
	if !p.hookRan {
		t.Fatal("pending 窗口的注入點未被執行，測試無效")
	}
	if !p.gotOK || p.gotWSID != p.wsid {
		t.Fatalf("自我校驗失敗：窗口內帶 identity 的 frame 必須歸屬到 %s，got %s ok=%v",
			p.wsid, p.gotWSID, p.gotOK)
	}
	if windowOpenErr != nil {
		t.Fatalf("自我校驗失敗：窗口內帶 identity 的通知不得 fail loud：%v", windowOpenErr)
	}
}

// 「同一時間至多一筆 pending start」是 codexWSIDFor 第三順位成立的前提，而它由
// codexStartMu 保證。三個多 session 測試都是依序啟動，從未併發驅動過這條不變量
// ——Task 26 前端能同時開兩個 codex session 之後，這裡是最先爆的地方。
func TestCodexConcurrentStartsKeepPendingInvariant(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	script := newCodexScript(t, wire, "th-c1", "th-c2")
	handshakeFake(t, conn)
	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")

	// 每次 thread/start 都在窗口內斷言 pending 只有一筆，並送一筆 thread/started
	// ——歸屬錯了就會在下方的交叉檢查裡現形。
	var maxPending atomic.Int64
	script.beforeStartResponse = func(threadID string) {
		a.mu.Lock()
		n := int64(len(a.codexPendingStarts))
		a.mu.Unlock()
		for {
			cur := maxPending.Load()
			if n <= cur || maxPending.CompareAndSwap(cur, n) {
				break
			}
		}
		script.notify(codex.MethodThreadStarted, map[string]any{"threadId": threadID})
	}

	type res struct {
		w      appcore.WSID
		thread string
		err    error
	}
	out := make(chan res, 2)
	begin := make(chan struct{})
	for _, w := range []appcore.WSID{w1, w2} {
		go func() {
			<-begin
			th, _, err := a.startCodexHost(w, fakeCodexHost{conn}, "hi", "", "", "untrusted")
			out <- res{w, th, err}
		}()
	}
	close(begin)
	r1, r2 := <-out, <-out
	for _, r := range []res{r1, r2} {
		if r.err != nil {
			t.Fatalf("併發 start 不得失敗（%s）：%v", r.w, r.err)
		}
	}
	if r1.thread == r2.thread {
		t.Fatalf("兩個併發 session 必須拿到相異 thread：%s / %s", r1.thread, r2.thread)
	}
	if got := maxPending.Load(); got != 1 {
		t.Fatalf("codexStartMu 必須保證同時至多一筆 pending start，實測峰值 %d", got)
	}

	// 每個 thread 各送一筆可辨識的 delta，確認事件不互串。
	script.notify(codex.MethodAgentMessageDelta,
		map[string]any{"threadId": r1.thread, "delta": "only-" + string(r1.w)})
	script.notify(codex.MethodAgentMessageDelta,
		map[string]any{"threadId": r2.thread, "delta": "only-" + string(r2.w)})
	waitFor(t, "兩個 WSID 各自收到自己的 delta", func() bool {
		return containsText(envsForWSID(ui, r1.w), "only-"+string(r1.w)) &&
			containsText(envsForWSID(ui, r2.w), "only-"+string(r2.w))
	})
	if containsText(envsForWSID(ui, r1.w), "only-"+string(r2.w)) ||
		containsText(envsForWSID(ui, r2.w), "only-"+string(r1.w)) {
		t.Fatal("併發 start 之後事件互串")
	}
	if errs := dispatchErrors(ui); len(errs) != 0 {
		t.Fatalf("併發 start 全程不得 fail loud：%+v", errs)
	}
}

// 反射守門：Codex 的三個 App 級單例欄位必須真的消失，不是「還在但沒人用」。
func TestNoCodexSingletonFieldsRemain(t *testing.T) {
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"runner", "track", "codexLease"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Codex 單例欄位 %s 應已刪除（§3.3）", name)
		}
	}
}

// handshakeFake：fake wire 的 initialize 握手（每個測試都要，抽出來避免重複）。
func handshakeFake(t *testing.T, conn *codex.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Handshake(ctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
}

// waitForApprovalID：等 approval:request 對話框事件並回傳其 id。
func waitForApprovalID(t *testing.T, ui *uiCapture) string {
	t.Helper()
	var id string
	waitFor(t, "approval:request", func() bool {
		for _, e := range ui.find("approval:request") {
			if d, ok := e.data.(map[string]any); ok {
				id, _ = d["id"].(string)
				return id != ""
			}
		}
		return false
	})
	return id
}
