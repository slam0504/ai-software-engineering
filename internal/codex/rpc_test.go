package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// fakeServer 是 Go in-test fake：強制 (a) 收到含 jsonrpc 欄位的 frame 即回 error、
// (b) initialize 前任何 request 回「Not initialized」、(c) initialized 通知前拒絕其他請求。
type fakeServer struct {
	t     *testing.T
	outMu sync.Mutex
	out   io.Writer
	state int32 // 0=new 1=init-received 2=ready

	mu     sync.Mutex
	raw    [][]byte // 收到的每個 raw frame
	sent   [][]byte // 送出的每個 frame
	onReq  func(f Frame)
	onResp func(f Frame) // client 對 server request 的回覆
}

type fakePair struct {
	conn   *Conn
	fake   *fakeServer
	rawC2S io.Writer // 繞過 client 狀態機直接寫 wire（雙保險測試用）
}

func newFakePair(t *testing.T) fakePair {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	f := &fakeServer{t: t, out: s2cW}
	f.onReq = func(Frame) {}
	f.onResp = func(Frame) {}
	conn := NewConn(c2sW, s2cR)
	go f.serve(c2sR)
	t.Cleanup(func() { c2sW.Close(); s2cW.Close() })
	return fakePair{conn: conn, fake: f, rawC2S: c2sW}
}

func (f *fakeServer) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Errorf("fake send marshal: %v", err)
		return
	}
	f.mu.Lock()
	f.sent = append(f.sent, b)
	f.mu.Unlock()
	f.outMu.Lock()
	defer f.outMu.Unlock()
	f.out.Write(append(b, '\n'))
}

func (f *fakeServer) sentContains(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.sent {
		if bytes.Contains(b, []byte(substr)) {
			return true
		}
	}
	return false
}

func (f *fakeServer) rawFrames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.raw))
	copy(out, f.raw)
	return out
}

func (f *fakeServer) serve(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		f.mu.Lock()
		f.raw = append(f.raw, line)
		f.mu.Unlock()
		if bytes.Contains(line, []byte(`"jsonrpc"`)) { // wire 純度：官方 wire 省略 jsonrpc 欄位
			f.send(map[string]any{"id": 0, "error": map[string]any{"code": -32600, "message": "jsonrpc field forbidden"}})
			continue
		}
		var fr Frame
		if json.Unmarshal(line, &fr) != nil {
			continue
		}
		switch {
		case fr.Method == "initialize" && fr.ID != nil:
			if atomic.LoadInt32(&f.state) >= 1 {
				f.send(map[string]any{"id": *fr.ID, "error": map[string]any{"code": -32000, "message": "Already initialized"}})
				continue
			}
			atomic.StoreInt32(&f.state, 1)
			f.send(map[string]any{"id": *fr.ID, "result": map[string]any{}})
		case fr.Method == "initialized" && fr.ID == nil:
			atomic.StoreInt32(&f.state, 2)
		case fr.ID != nil && fr.Method != "":
			if atomic.LoadInt32(&f.state) != 2 {
				f.send(map[string]any{"id": *fr.ID, "error": map[string]any{"code": -32002, "message": "Not initialized"}})
				continue
			}
			f.onReq(fr)
		case fr.ID != nil:
			f.onResp(fr)
		}
	}
}

func doHandshake(t *testing.T, conn *Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting: " + msg)
}

func TestHandshakeOrderEnforced(t *testing.T) {
	p := newFakePair(t)
	ctx := context.Background()
	// client 狀態機：未 Handshake 就 Call → 錯誤（不經 wire）
	if _, err := p.conn.Call(ctx, MethodThreadStart, map[string]any{}); err == nil {
		t.Fatal("Call before handshake must fail client-side")
	}
	// fake 端雙保險：繞過 client 直接寫 request → 收「Not initialized」
	io.WriteString(p.rawC2S, `{"id":99,"method":"thread/start","params":{}}`+"\n")
	waitFor(t, func() bool { return p.fake.sentContains("Not initialized") }, "fake must reject pre-init request")
	doHandshake(t, p.conn)
	p.fake.onReq = func(fr Frame) {
		p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
	}
	if _, err := p.conn.Call(ctx, MethodThreadStart, map[string]any{}); err != nil {
		t.Fatalf("Call after handshake: %v", err)
	}
}

func TestWireOmitsJSONRPCField(t *testing.T) {
	p := newFakePair(t)
	doHandshake(t, p.conn)
	p.fake.onReq = func(fr Frame) {
		p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{}})
	}
	if _, err := p.conn.Call(context.Background(), MethodThreadStart, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	frames := p.fake.rawFrames()
	if len(frames) < 3 { // initialize、initialized、thread/start
		t.Fatalf("expected >=3 frames, got %d", len(frames))
	}
	for _, b := range frames {
		if bytes.Contains(b, []byte(`"jsonrpc"`)) {
			t.Fatalf("frame must omit jsonrpc field: %s", b)
		}
	}
}

func TestTurnStreamMapsToContract(t *testing.T) {
	p := newFakePair(t)
	var evMu sync.Mutex
	var events []contract.Event
	p.conn.OnNotification(func(method string, params json.RawMessage) {
		evMu.Lock()
		events = append(events, MapEvent(method, params))
		evMu.Unlock()
	})
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			var tp struct {
				ThreadID string           `json:"threadId"`
				Input    []map[string]any `json:"input"`
			}
			if json.Unmarshal(fr.Params, &tp) != nil || tp.ThreadID != "t1" || len(tp.Input) != 1 {
				p.fake.t.Errorf("turn/start params must carry threadId and item-array input: %s", fr.Params)
			}
			p.fake.send(map[string]any{"method": MethodItemStarted, "params": map[string]any{"threadId": "t1",
				"item": map[string]any{"id": "i1", "type": "commandExecution", "command": "echo hi", "status": "inProgress"}}})
			p.fake.send(map[string]any{"method": MethodItemCompleted, "params": map[string]any{"threadId": "t1",
				"item": map[string]any{"id": "i1", "type": "commandExecution", "command": "echo hi", "status": "completed"}}})
			p.fake.send(map[string]any{"method": MethodAgentMessageDelta, "params": map[string]any{"threadId": "t1", "itemId": "i2", "delta": "hi"}})
			p.fake.send(map[string]any{"method": MethodItemCompleted, "params": map[string]any{"threadId": "t1",
				"item": map[string]any{"id": "i2", "type": "agentMessage", "text": "hi"}}})
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{}})
			p.fake.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1",
				"turn": map[string]any{"id": "turn1", "status": "completed"}}})
		}
	}
	doHandshake(t, p.conn)
	ctx := context.Background()
	res, err := p.conn.Call(ctx, MethodThreadStart, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(res, &tr) != nil || tr.Thread.ID != "t1" {
		t.Fatalf("thread id must be at result.thread.id: %s", res)
	}
	if _, err := p.conn.Call(ctx, MethodTurnStart,
		map[string]any{"threadId": "t1", "input": []map[string]any{{"type": "text", "text": "hello"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		evMu.Lock()
		defer evMu.Unlock()
		return len(events) >= 5
	}, "all turn events mapped")
	evMu.Lock()
	defer evMu.Unlock()
	wantKinds := []contract.Kind{contract.KindToolUse, contract.KindToolUse, contract.KindDelta,
		contract.KindMessage, contract.KindResult}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event[%d] kind = %s, want %s", i, events[i].Kind, want)
		}
		if !events[i].Valid() {
			t.Fatalf("event[%d] must satisfy contract.Valid", i)
		}
	}
	if events[2].Text != "hi" {
		t.Fatalf("delta text = %q", events[2].Text)
	}
	if events[4].IsError {
		t.Fatal("completed turn must not be IsError")
	}
}

func TestApprovalAllowDenyTimeout(t *testing.T) {
	p := newFakePair(t)
	decisions := make(chan json.RawMessage, 4)
	p.fake.onResp = func(fr Frame) { decisions <- fr.Result }
	doHandshake(t, p.conn)

	sendApproval := func(id int64) {
		p.fake.send(map[string]any{"id": id, "method": MethodCmdExecRequestApproval,
			"params": map[string]any{"threadId": "t1", "turnId": "turn1", "itemId": "i1"}})
	}
	readDecision := func() string {
		select {
		case d := <-decisions:
			return string(d)
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting decision")
			return ""
		}
	}

	// allow → accept
	p.conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		if method != MethodCmdExecRequestApproval {
			t.Errorf("unexpected method %s", method)
		}
		return map[string]string{"decision": "accept"}, nil
	})
	sendApproval(100)
	if d := readDecision(); !bytes.Contains([]byte(d), []byte("accept")) {
		t.Fatalf("allow must map to accept: %s", d)
	}

	// deny → decline
	p.conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		return map[string]string{"decision": "decline"}, nil
	})
	sendApproval(101)
	if d := readDecision(); !bytes.Contains([]byte(d), []byte("decline")) {
		t.Fatalf("deny must map to decline: %s", d)
	}

	// handler 逾時 → 自動 decline（fail closed；app 層 pattern：select timeout）
	p.conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		never := make(chan struct{})
		select {
		case <-never:
			return map[string]string{"decision": "accept"}, nil
		case <-time.After(50 * time.Millisecond):
			return map[string]string{"decision": "decline"}, nil
		}
	})
	sendApproval(102)
	if d := readDecision(); !bytes.Contains([]byte(d), []byte("decline")) {
		t.Fatalf("timeout must fail closed to decline: %s", d)
	}
}

func TestApprovalStringRequestIDRoundTrip(t *testing.T) { // RequestId = string | int64（schema union）
	p := newFakePair(t)
	responses := make(chan Frame, 1)
	p.fake.onResp = func(fr Frame) { responses <- fr }
	p.conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		return map[string]string{"decision": "accept"}, nil
	})
	doHandshake(t, p.conn)
	// fake 以 string ID 發核可請求（schema 允許；不得掉進 OnUnknown）
	p.fake.send(map[string]any{"id": "srv-abc", "method": MethodCmdExecRequestApproval,
		"params": map[string]any{"threadId": "t1", "turnId": "turn1", "itemId": "i1"}})
	select {
	case fr := <-responses:
		if fr.ID == nil || fr.ID.Key() != `"srv-abc"` {
			t.Fatalf("response must echo original string id, got %v", fr.ID)
		}
		if !bytes.Contains(fr.Result, []byte("accept")) {
			t.Fatalf("decision lost: %s", fr.Result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("string-ID server request never answered (fell into OnUnknown?)")
	}
	// wire 原文驗證：回覆 frame 的 id 是 string，不是數字
	waitFor(t, func() bool {
		for _, b := range p.fake.rawFrames() {
			if bytes.Contains(b, []byte(`"id":"srv-abc"`)) {
				return true
			}
		}
		return false
	}, "raw response frame must carry string id verbatim")
}

func TestServerErrorSurfaced(t *testing.T) {
	p := newFakePair(t)
	p.fake.onReq = func(fr Frame) {
		p.fake.send(map[string]any{"id": *fr.ID, "error": map[string]any{"code": -32001, "message": "boom"}})
	}
	doHandshake(t, p.conn)
	_, err := p.conn.Call(context.Background(), MethodThreadStart, map[string]any{})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("-32001")) {
		t.Fatalf("error frame must surface code: %v", err)
	}
}

func TestUnknownMethodKeptRaw(t *testing.T) {
	p := newFakePair(t)
	got := make(chan []byte, 1)
	p.conn.OnUnknown(func(raw []byte) {
		select {
		case got <- append([]byte(nil), raw...):
		default:
		}
	})
	doHandshake(t, p.conn)
	p.fake.send(map[string]any{"method": "totally/unknown", "params": map[string]any{"threadId": "t1"}})
	var raw []byte
	select {
	case raw = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("unknown notification must reach OnUnknown")
	}
	if !bytes.Contains(raw, []byte("totally/unknown")) {
		t.Fatalf("raw not preserved: %s", raw)
	}
	ev := MapEvent("totally/unknown", json.RawMessage(`{"threadId":"t1"}`))
	if ev.Kind != contract.KindUnknown || !ev.Valid() {
		t.Fatalf("unknown method must map to valid KindUnknown: %+v", ev)
	}
}

func TestRecordingSessionScoped(t *testing.T) {
	p := newFakePair(t)
	notified := make(chan struct{}, 16)
	p.conn.OnNotification(func(string, json.RawMessage) { notified <- struct{}{} })

	var mu sync.Mutex
	var rec1 [][]byte
	if err := p.conn.BeginRecording(func(b []byte) error {
		mu.Lock()
		rec1 = append(rec1, append([]byte(nil), b...))
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	doHandshake(t, p.conn) // handshake frames 應被錄下（B1 probe 情境）
	if err := p.conn.StopRecording(); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	mu.Lock()
	len1 := len(rec1)
	mu.Unlock()
	if len1 == 0 {
		t.Fatal("first recording must capture handshake frames")
	}
	// trailing notification：不得進已停止的 sink
	p.fake.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1",
		"turn": map[string]any{"id": "x", "status": "completed"}}})
	<-notified // 已處理（record 在 dispatch 前，同步完成）
	mu.Lock()
	if len(rec1) != len1 {
		mu.Unlock()
		t.Fatal("trailing notification leaked into stopped sink")
	}
	mu.Unlock()

	// 第二次錄流：sink 回 error → Stop 回傳該錯誤並重設
	sinkErr := errors.New("disk full")
	if err := p.conn.BeginRecording(func(b []byte) error { return sinkErr }); err != nil {
		t.Fatal(err)
	}
	p.fake.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1",
		"turn": map[string]any{"id": "y", "status": "completed"}}})
	<-notified
	if err := p.conn.StopRecording(); err == nil || !errors.Is(err, sinkErr) {
		t.Fatalf("second stop must return latched sink error, got %v", err)
	}

	// 第三次錄流：latch 不跨 session 殘留
	var rec3 int
	if err := p.conn.BeginRecording(func(b []byte) error { mu.Lock(); rec3++; mu.Unlock(); return nil }); err != nil {
		t.Fatal(err)
	}
	p.fake.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1",
		"turn": map[string]any{"id": "z", "status": "completed"}}})
	<-notified
	if err := p.conn.StopRecording(); err != nil {
		t.Fatalf("third stop must be nil (latch reset), got %v", err)
	}
	mu.Lock()
	if rec3 == 0 {
		mu.Unlock()
		t.Fatal("third recording must capture new frames")
	}
	mu.Unlock()
}

func TestRecordingConcurrentSafe(t *testing.T) {
	p := newFakePair(t)
	p.conn.OnNotification(func(string, json.RawMessage) {})
	p.fake.onReq = func(fr Frame) {
		p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{}})
	}
	doHandshake(t, p.conn)

	var stopped atomic.Bool
	var lines atomic.Int64
	if err := p.conn.BeginRecording(func(b []byte) error {
		if stopped.Load() {
			t.Error("sink called after StopRecording returned")
		}
		lines.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // c2s：並行 Call
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _ = p.conn.Call(context.Background(), MethodThreadStart, map[string]any{})
		}
	}()
	go func() { // s2c：並行 notification
		defer wg.Done()
		for i := 0; i < 50; i++ {
			p.fake.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1",
				"turn": map[string]any{"id": "n", "status": "completed"}}})
		}
	}()
	wg.Wait()
	if err := p.conn.StopRecording(); err != nil {
		t.Fatal(err)
	}
	stopped.Store(true)
	if lines.Load() == 0 {
		t.Fatal("recording captured nothing")
	}
}
