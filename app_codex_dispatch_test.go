package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
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
		if e.Scope == "workspace" && e.Kind == codexBroadcastEventKind {
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
		if e.Kind == codexBroadcastEventKind && strings.Contains(string(e.Payload), "garbage") {
			sawRaw = true
		}
	}
	ui.mu.Unlock()
	if !sawRaw {
		t.Fatal("解不開的 frame 必須以 raw 形式保留在 workspace lane")
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
