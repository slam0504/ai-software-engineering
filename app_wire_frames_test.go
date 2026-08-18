package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
)

// ---- M3b §3.4.3／§5.2 第 2 條：frame-level 歸屬（錄流 frame 不串線）----
//
// 這批測試守的是 SegmentSet（§3.4.4）**原理上答不出來**的那一格。兩者的分工：
//
//	SegmentSet.For(w)   → 「w 橫跨哪些 generation 的哪些 frame range」
//	FrameIndex.WSIDOf(n)→ 「**重疊 range 裡的第 n 筆 frame** 到底屬於誰」
//
// 並行 session 共用同一條 codex.Conn，range 必然互相涵蓋（見
// TestWireSegmentsConcurrentRangeIsNotExclusive：w1 的單一 range 實測吞掉 w2 整
// 段），所以「不串線」若只驗 set-level 就是沒驗。
//
// **oracle 不是受測對象**（失效形狀 H）：期望值由 wire log 的 frame **原文**
// ＋測試自己知道的事實（哪個 thread id 屬於哪個 WSID、thread/start 的送出順序）
// 推導，完全不讀 wsid 欄位、也不呼叫 FrameIndex；斷言才拿它去比對每一行實際寫下
// 的 wsid。

// ---- fixture：可注入惡意順序的 fake app-server ----

// framingWire 是 codexServerFactory 的測試控制器。與 app_wire_segments_test.go 的
// respondingWire 的差別在於它必須產生**六種 frame 形狀**，因此多了 pending start
// 窗口與 completed-before-response 兩個注入點，並把本代的 wire 端交出來讓測試自己
// 推送 approval 請求、跨 session 通知與 server 級廣播。
type framingWire struct {
	mu         sync.Mutex
	wire       *fakeCodexWire // 最近一代 server 的「server 端」
	nextThread string
	turnSeq    atomic.Int32

	// startedInPendingWindow：true 時在 thread/start 的 response **之前**先送一筆
	// thread/started 通知——client 此刻還不知道這個 threadId，只有 pending start
	// 登記能歸屬它。
	startedInPendingWindow bool
	// completeBeforeResponse：true 時把 turn/completed 排在 turn/start 的 response
	// 之前送（惡意順序；response 抵達時該 turn 早已 unbind）。
	completeBeforeResponse bool
}

func (c *framingWire) setNextThread(id string) {
	c.mu.Lock()
	c.nextThread = id
	c.mu.Unlock()
}

func (c *framingWire) serverWire(t *testing.T) *fakeCodexWire {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wire == nil {
		t.Fatal("尚未建立任何 fake app-server")
	}
	return c.wire
}

func (c *framingWire) newServer(t *testing.T) (codex.ProbeTarget, error) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	w := &fakeCodexWire{t: t, out: s2cW}
	s := &fakeCodexServer{
		conn:      codex.NewConn(c2sW, s2cR),
		s2c:       s2cW,
		closeWire: func() { _ = s2cW.Close(); _ = c2sW.Close(); _ = c2sR.Close() },
		steps:     func(string) {},
		done:      make(chan struct{}),
	}
	s.hsFn = func(ctx context.Context, ci codex.ClientInfo) error { return s.conn.Handshake(ctx, ci) }
	go func() {
		sc := bufio.NewScanner(c2sR)
		for sc.Scan() {
			var f codex.Frame
			if json.Unmarshal(sc.Bytes(), &f) != nil {
				continue
			}
			if f.Method == "" || f.ID == nil { // 只回 client request
				continue
			}
			c.respond(w, f)
		}
		_ = sc.Err() // cleanup 關 pipe 屬預期收尾
	}()
	t.Cleanup(s.die)
	c.mu.Lock()
	c.wire = w
	c.mu.Unlock()
	return s, nil
}

func (c *framingWire) respond(w *fakeCodexWire, f codex.Frame) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(f.Params, &p)
	switch f.Method {
	case codex.MethodInitialize:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodThreadStart:
		c.mu.Lock()
		tid, pending := c.nextThread, c.startedInPendingWindow
		c.mu.Unlock()
		if pending {
			w.send(map[string]any{"method": codex.MethodThreadStarted,
				"params": map[string]any{"threadId": tid}})
		}
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"thread": map[string]any{"id": tid}}})
	case codex.MethodThreadResume:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"thread": map[string]any{"id": p.ThreadID}}})
	case codex.MethodTurnStart:
		turnID := fmt.Sprintf("%s-turn-%d", p.ThreadID, c.turnSeq.Add(1))
		completed := map[string]any{"method": codex.MethodTurnCompleted,
			"params": map[string]any{"threadId": p.ThreadID,
				"turn": map[string]any{"id": turnID, "status": "completed"}}}
		resp := map[string]any{"id": *f.ID, "result": map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress"}}}
		c.mu.Lock()
		early := c.completeBeforeResponse
		c.mu.Unlock()
		if early {
			w.send(completed)
			w.send(resp)
			return
		}
		w.send(resp)
		w.send(completed)
	}
}

func bootFramingApp(t *testing.T, a *App, ctl *framingWire) {
	t.Helper()
	enableAudit(t, a)
	bootRegistry(t, a)
	a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
}

// ---- oracle ----

// wireRowFull 是 wire log JSONL 的完整一行（app_wire_segments_test.go 的
// wireFrame 只解 frame＋raw，這裡還要 dir 與 wsid）。
type wireRowFull struct {
	Frame int             `json:"frame"`
	Dir   string          `json:"dir"`
	WSID  string          `json:"wsid"`
	Raw   json.RawMessage `json:"raw"`
}

func readWireRows(t *testing.T, a *App, wireLogID string) []wireRowFull {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.wireLogDir(), wireLogID+".jsonl"))
	if err != nil {
		t.Fatalf("讀 wire log %s：%v", wireLogID, err)
	}
	var out []wireRowFull
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row wireRowFull
		if jerr := json.Unmarshal([]byte(line), &row); jerr != nil {
			t.Fatalf("wire log %s 壞行：%v（%s）", wireLogID, jerr, line)
		}
		out = append(out, row)
	}
	return out
}

// expectWireOwners：**獨立 oracle**——每一筆 frame 應該屬於誰，只由三件事推導：
//
//  1. frame 原文裡提到的 thread id（threadOwner 是測試自己安排的對照表）；
//  2. request↔response 的 id 配對（response 只有 {id,result}，沒有任何 identity，
//     其歸屬定義上就是「發出那筆 request 的人」）；
//  3. thread/start 的送出順序（那一筆連 threadId 都還沒有——thread id 是它的回傳
//     值——所以只有測試知道第 n 筆 thread/start 是誰送的）。
//
// 完全不讀 wsid 欄位、不呼叫 FrameIndex。
func expectWireOwners(rows []wireRowFull, threadOwner map[string]string, startOrder []string) map[int]string {
	want := map[int]string{}
	req := map[string]string{} // "<request 方向>:<id 原文>" → owner
	nextStart := 0
	oppose := func(dir string) string {
		if dir == "c2s" {
			return "s2c"
		}
		return "c2s"
	}
	for _, r := range rows {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(r.Raw, &f)
		id := strings.TrimSpace(string(f.ID))
		owner := ""
		for th, w := range threadOwner {
			if strings.Contains(string(r.Raw), `"`+th+`"`) {
				owner = w
			}
		}
		switch {
		case id != "" && f.Method == "": // response：繼承對應 request
			owner = req[oppose(r.Dir)+":"+id]
		case id != "" && f.Method == codex.MethodThreadStart:
			if nextStart < len(startOrder) {
				owner = startOrder[nextStart]
			}
			nextStart++
			req[r.Dir+":"+id] = owner
		case id != "": // 其餘 request（含 s2c 的 requestApproval）
			req[r.Dir+":"+id] = owner
		}
		want[r.Frame] = owner
	}
	return want
}

// framesOwnedBy：wire log 原文裡 wsid 欄位等於 w 的 frame 編號（依檔案順序）。
func framesOwnedBy(rows []wireRowFull, w string) []int {
	var out []int
	for _, r := range rows {
		if r.WSID == w {
			out = append(out, r.Frame)
		}
	}
	return out
}

// findFrame：符合述詞的第一筆 frame（找不到即 fail loud——precondition 不成立時
// 後面的斷言什麼都量不到）。
func findFrame(t *testing.T, rows []wireRowFull, what string, pred func(wireRowFull) bool) wireRowFull {
	t.Helper()
	for _, r := range rows {
		if pred(r) {
			return r
		}
	}
	t.Fatalf("precondition：錄流裡找不到 %s，這條測試量不到那一格", what)
	return wireRowFull{}
}

func rawHas(r wireRowFull, s string) bool { return strings.Contains(string(r.Raw), s) }

func rawMethod(r wireRowFull) string {
	var f struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(r.Raw, &f)
	return f.Method
}

func rawHasID(r wireRowFull) bool {
	var f struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(r.Raw, &f)
	return len(f.ID) > 0 && strings.TrimSpace(string(f.ID)) != "null"
}

// ---- 測試 ----

// TestWireFramesAttributeEachInterleavedSessionSeparately：merge gate 條件 1／2／
// 4／5／6 的主測項。
//
// 場景刻意讓兩個 session 的 frame **真的交錯**（不是先後跑完再比對）：w1 起了
// session 之後**不收尾**，w2 整段在同一個 generation 上起跑；w2 跑完第一輪之後才
// 插入 w1 的通知，接著 w2 再跑第二輪、收一次 approval，最後才依序收尾。
//
// 兩處刻意的**不對稱**（避免 TestWireSegmentsSurviveServerDeath 踩過的「兩邊數字
// 剛好一樣，斷言沒有辨識力」）：
//   - w1 一輪、w2 兩輪 → 兩者的 frame 數必然不同；
//   - w1 的第一輪走 completed-before-response ＋ pending start 窗口，w2 走正常順序
//     → 兩者的 frame 形狀序列也不同。
//
// 六種 frame 形狀（gate 條件 1）都在下方逐一斷言：c2s／s2c／notification／
// approval／pending-start／completed-before-response，另加一筆無 identity 的
// server 級廣播驗「歸屬不到就留空、但照寫」（gate 條件 5）。
func TestWireFramesAttributeEachInterleavedSessionSeparately(t *testing.T) {
	a, ui := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)

	w1 := mustCreate(t, a, "codex")
	w2 := mustCreate(t, a, "codex")

	// w1：pending start 窗口內先送 thread/started，第一輪走 completed-before-response。
	ctl.mu.Lock()
	ctl.startedInPendingWindow, ctl.completeBeforeResponse = true, true
	ctl.mu.Unlock()
	ctl.setNextThread("t-alpha")
	if err := a.StartSession(string(w1), "hi alpha", "", "rec-alpha", "task-alpha", ""); err != nil {
		t.Fatalf("StartSession(w1): %v", err)
	}
	waitTurnSettled(t, a, w1)

	// w2：整段落在 w1 的窗口內（w1 尚未收尾），走正常順序、跑兩輪。
	ctl.mu.Lock()
	ctl.startedInPendingWindow, ctl.completeBeforeResponse = false, false
	ctl.mu.Unlock()
	ctl.setNextThread("t-beta")
	if err := a.StartSession(string(w2), "hi beta", "", "rec-beta", "task-beta", ""); err != nil {
		t.Fatalf("StartSession(w2): %v", err)
	}
	waitTurnSettled(t, a, w2)

	// 交錯點：w1 的通知落在 w2 兩輪之間。
	wire := ctl.serverWire(t)
	wire.send(map[string]any{"method": codex.MethodAgentMessageDelta,
		"params": map[string]any{"threadId": "t-alpha", "delta": "alpha-mid"}})
	waitFor(t, "w1 的中段通知抵達", func() bool {
		return containsText(envsForWSID(ui, w1), "alpha-mid")
	})

	if err := a.SendMessage(string(w2), "beta second turn"); err != nil {
		t.Fatalf("SendMessage(w2): %v", err)
	}
	waitTurnSettled(t, a, w2)

	// approval：s2c server request（帶 threadId／turnId）＋ 我方的 c2s 回應。
	wire.send(map[string]any{"id": "srv-appr-1", "method": codex.MethodCmdExecRequestApproval,
		"params": map[string]any{"threadId": "t-beta", "turnId": "t-beta-turn-2",
			"itemId": "exec-1", "command": "ls"}})
	apprID := waitForApprovalID(t, ui)
	if err := a.ResolveApproval(apprID, false, "no"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	// server 級廣播：沒有任何 identity，依定義歸屬不到（gate 條件 5）。
	wire.send(map[string]any{"method": codex.MethodAccountRateLimitsUpdated,
		"params": map[string]any{"limit": 42}})
	waitFor(t, "廣播抵達", func() bool {
		for _, r := range readWireRows(t, a, currentWireLogID(a)) {
			if rawHas(r, codex.MethodAccountRateLimitsUpdated) {
				return true
			}
		}
		return false
	})

	genID := currentWireLogID(a)
	if err := a.EndSession(string(w2)); err != nil {
		t.Fatalf("EndSession(w2): %v", err)
	}
	if err := a.EndSession(string(w1)); err != nil {
		t.Fatalf("EndSession(w1): %v", err)
	}

	rows := readWireRows(t, a, genID)
	want := expectWireOwners(rows,
		map[string]string{"t-alpha": string(w1), "t-beta": string(w2)},
		[]string{string(w1), string(w2)})

	// ---- gate 條件 1：六種 frame 形狀逐一 ----
	// 第一筆 c2s thread/start 就是 w1 送的那一筆——它的 params 裡**沒有任何
	// identity**（thread id 是它的回傳值），所以只認方法名與順序。
	startFrame := findFrame(t, rows, "c2s thread/start", func(r wireRowFull) bool {
		return r.Dir == "c2s" && rawMethod(r) == codex.MethodThreadStart
	})
	if rawHas(startFrame, "t-alpha") || rawHas(startFrame, "t-beta") {
		t.Fatalf("precondition：thread/start 不得帶 thread identity，否則測不到 pending 退路：%s", startFrame.Raw)
	}
	if startFrame.WSID != string(w1) {
		t.Fatalf("[c2s] thread/start 必須歸屬送出它的 session：#%d wsid=%q want %q（它連 threadId 都還沒有）",
			startFrame.Frame, startFrame.WSID, w1)
	}
	startResp := findFrame(t, rows, "s2c thread/start response", func(r wireRowFull) bool {
		return r.Dir == "s2c" && rawMethod(r) == "" && rawHasID(r) && rawHas(r, "t-alpha")
	})
	if startResp.WSID != string(w1) {
		t.Fatalf("[s2c] response 必須繼承對應 request 的歸屬：#%d wsid=%q want %q",
			startResp.Frame, startResp.WSID, w1)
	}
	notif := findFrame(t, rows, "s2c 中段 notification（無 request id）", func(r wireRowFull) bool {
		return r.Dir == "s2c" && rawMethod(r) == codex.MethodAgentMessageDelta && rawHas(r, "alpha-mid")
	})
	if notif.WSID != string(w1) {
		t.Fatalf("[notification] 無 request id 的通知必須以 thread identity 精確歸屬：#%d wsid=%q want %q",
			notif.Frame, notif.WSID, w1)
	}
	apprReq := findFrame(t, rows, "s2c requestApproval", func(r wireRowFull) bool {
		return r.Dir == "s2c" && rawMethod(r) == codex.MethodCmdExecRequestApproval
	})
	if apprReq.WSID != string(w2) {
		t.Fatalf("[approval] 核可請求必須歸屬提出它的 session：#%d wsid=%q want %q",
			apprReq.Frame, apprReq.WSID, w2)
	}
	apprResp := findFrame(t, rows, "c2s approval 回應", func(r wireRowFull) bool {
		return r.Dir == "c2s" && rawMethod(r) == "" && rawHas(r, "srv-appr-1")
	})
	if apprResp.WSID != string(w2) {
		t.Fatalf("[approval] 我方回應必須與請求同一 session：#%d wsid=%q want %q",
			apprResp.Frame, apprResp.WSID, w2)
	}
	pending := findFrame(t, rows, "pending start 窗口內的 thread/started", func(r wireRowFull) bool {
		return rawMethod(r) == codex.MethodThreadStarted
	})
	if pending.Frame >= startResp.Frame {
		t.Fatalf("precondition：thread/started 必須排在 thread/start 的 response 之前（#%d vs #%d），"+
			"否則走的是 threadId 索引而不是 pending 登記", pending.Frame, startResp.Frame)
	}
	if pending.WSID != string(w1) {
		t.Fatalf("[pending-start] 窗口內的通知必須歸屬到正在啟動的 session：#%d wsid=%q want %q",
			pending.Frame, pending.WSID, w1)
	}
	completed := findFrame(t, rows, "w1 的 turn/completed", func(r wireRowFull) bool {
		return rawMethod(r) == codex.MethodTurnCompleted && rawHas(r, "t-alpha")
	})
	turnResp := findFrame(t, rows, "w1 的 turn/start response", func(r wireRowFull) bool {
		return r.Dir == "s2c" && rawMethod(r) == "" && rawHas(r, "t-alpha-turn-")
	})
	if completed.Frame >= turnResp.Frame {
		t.Fatalf("precondition：turn/completed 必須排在 turn/start 的 response 之前（#%d vs #%d）",
			completed.Frame, turnResp.Frame)
	}
	if completed.WSID != string(w1) || turnResp.WSID != string(w1) {
		t.Fatalf("[completed-before-response] 惡意順序下兩筆都必須歸 w1：completed#%d=%q resp#%d=%q",
			completed.Frame, completed.WSID, turnResp.Frame, turnResp.WSID)
	}

	// ---- gate 條件 5：歸屬不到的廣播照寫、wsid 留空 ----
	bc := findFrame(t, rows, "server 級廣播", func(r wireRowFull) bool {
		return rawMethod(r) == codex.MethodAccountRateLimitsUpdated
	})
	if bc.WSID != "" {
		t.Fatalf("無 identity 的廣播不得被歸給任何 session：#%d wsid=%q", bc.Frame, bc.WSID)
	}

	// ---- gate 條件 4：逐 frame 查詢，各歸各的 ----
	for _, r := range rows {
		if r.WSID != want[r.Frame] {
			t.Fatalf("frame #%d（%s）歸屬錯誤：wsid=%q，want %q\n  raw=%s",
				r.Frame, r.Dir, r.WSID, want[r.Frame], r.Raw)
		}
	}

	f1, f2 := framesOwnedBy(rows, string(w1)), framesOwnedBy(rows, string(w2))
	if len(f1) == 0 || len(f2) == 0 {
		t.Fatalf("precondition：兩個 session 都必須有自己的 frame（w1=%v w2=%v）", f1, f2)
	}
	if len(f1) == len(f2) {
		t.Fatalf("precondition：兩個 session 的 frame 數必須不同，否則對稱 fixture 讓斷言失去辨識力（w1=%v w2=%v）",
			f1, f2)
	}
	// 真的交錯：w1 有 frame 落在 w2 的第一筆與最後一筆之間。
	interleaved := false
	for _, f := range f1 {
		if f > f2[0] && f < f2[len(f2)-1] {
			interleaved = true
		}
	}
	if !interleaved {
		t.Fatalf("precondition：兩個 session 的 frame 必須真的交錯，否則測不到串線（w1=%v w2=%v）", f1, f2)
	}
	// set-level 讀不出這件事：w1 的 range 內確實含 w2 的 frame（這正是 §3.4.3
	// 不能被 §3.4.4 取代的理由；哪天實作改成切段，這裡會紅，屆時一起重審）。
	seg1 := a.wireSegments.For(string(w1))
	if len(seg1) != 1 {
		t.Fatalf("w1 應只有一段：%+v", seg1)
	}
	foreignInRange := 0
	for _, f := range f2 {
		if f >= seg1[0].StartFrame && f <= seg1[0].EndFrame {
			foreignInRange++
		}
	}
	if foreignInRange == 0 {
		t.Fatalf("precondition：w1 的 range 必須含 w2 的 frame，否則本測試與 set-level 證據等價（seg=%+v w2=%v）",
			seg1[0], f2)
	}

	// ---- 證據出口：codex_wire_segments 的 frames 欄位就是這份逐 frame 答案 ----
	view := auditSegments(t, a, w1)
	if view.Exclusive {
		t.Fatalf("並行情境下 range 不得標成排他：%+v", view)
	}
	if got := view.Frames[genID]; !equalInts(got, f1) {
		t.Fatalf("稽核出口的 frames 必須等於 w1 在這一代真正擁有的 frame：%v（want %v）", got, f1)
	}
	view2 := auditSegments(t, a, w2)
	if got := view2.Frames[genID]; !equalInts(got, f2) {
		t.Fatalf("稽核出口的 frames（w2）：%v（want %v）", got, f2)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWireFrameOwnersSurviveAppRestart：merge gate 條件 3——**歸屬資料不得只存在
// 記憶體**（失效形狀 F：跨重啟／跨 process 那一維）。
//
// 第一個 App 跑完一輪 session 就整個關掉；第二個 App 以同一組 workspace／stateDir
// 重開，記憶體裡沒有任何前一代的 Generation handle。同一個 WSID 在新 generation
// 再跑一輪收尾時，production 的證據出口（closeWireSegment → wireFramesOf）必須把
// **上一次執行那一代**的逐 frame 歸屬也讀回來——來源只能是磁碟上每一行的 wsid 欄位
// （RebuildFrameIndex）。
//
// 「假裝重啟」（同一個 App 清掉記憶體）守不到這一維，所以這裡一定要開第二個 App。
func TestWireFrameOwnersSurviveAppRestart(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)

	w := mustCreate(t, a, "codex")
	other := mustCreate(t, a, "codex")

	// 兩個 session 都在第一代上跑過：重建之後必須只認出自己的那些 frame，
	// 不是「這一代的全部 frame」。
	ctl.setNextThread("t-keep")
	if err := a.StartSession(string(w), "hi", "", "rec-keep", "task-keep", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w)
	ctl.setNextThread("t-gone")
	if err := a.StartSession(string(other), "hi", "", "rec-gone", "task-gone", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, other)
	gen1 := currentWireLogID(a)
	if err := a.EndSession(string(other)); err != nil {
		t.Fatal(err)
	}
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}

	rows1 := readWireRows(t, a, gen1)
	before := framesOwnedBy(rows1, string(w))
	foreign := framesOwnedBy(rows1, string(other))
	if len(before) == 0 || len(foreign) == 0 {
		t.Fatalf("precondition：第一代必須同時有兩個 session 的 frame（w=%v other=%v）", before, foreign)
	}
	a.shutdown(a.ctx)

	// 真正的重啟：新的 App、新的 Manager、新的 SegmentSet，記憶體裡沒有 gen1。
	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl2 := &framingWire{}
	bootFramingApp(t, b, ctl2)
	if b.wireGen != nil {
		t.Fatal("precondition：重啟後不得殘留任何 generation handle")
	}

	ctl2.setNextThread("t-keep")
	if err := b.StartSession(string(w), "again", "t-keep", "rec-keep", "task-keep", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, b, w)
	gen2 := currentWireLogID(b)
	if gen2 == gen1 {
		t.Fatal("precondition：重啟必須開新 generation")
	}
	if err := b.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}

	view := auditSegments(t, b, w)
	if len(view.Segments) != 2 {
		t.Fatalf("w 必須橫跨重啟前後兩代：%+v", view.Segments)
	}
	got := view.Frames[gen1]
	if !equalInts(got, before) {
		t.Fatalf("重啟後必須從磁碟重建出上一次執行的逐 frame 歸屬：%v（want %v）", got, before)
	}
	for _, f := range foreign {
		for _, g := range got {
			if f == g {
				t.Fatalf("重建出來的歸屬混進了他 session 的 frame #%d", f)
			}
		}
	}
	if len(view.Frames[gen2]) == 0 {
		t.Fatalf("本次執行那一代的歸屬也必須在：%+v", view.Frames)
	}
}

// TestWireFrameAttributionUsesSameRouterAsDispatch：歸屬與事件分流必須是同一份
// 查找表。兩份表在路由規則變動時必然漂移，而漂移的後果是「事件送對 session、
// 錄流證據卻歸錯」——最難察覺的形狀。
//
// 這裡以「dispatcher 判定歸屬不到的 frame」為探針：同一筆 frame，dispatcher fail
// loud，錄流就必須留空，兩者不得各說各話。
func TestWireFrameAttributionUsesSameRouterAsDispatch(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)

	w := mustCreate(t, a, "codex")
	ctl.setNextThread("t-one")
	if err := a.StartSession(string(w), "hi", "", "", "task-one", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w)

	stray := []byte(`{"threadId":"t-unknown","delta":"stray"}`)
	if err := a.dispatchCodexNotification(codex.MethodAgentMessageDelta, stray); err == nil {
		t.Fatal("precondition：這筆 frame 對 dispatcher 而言必須是歸屬不到的")
	}
	raw := []byte(`{"method":"` + codex.MethodAgentMessageDelta + `","params":` + string(stray) + `}`)
	if got := a.resolveWireFrameWSID("s2c", raw); got != "" {
		t.Fatalf("dispatcher 歸屬不到的 frame，錄流不得自己猜一個 session：%q", got)
	}

	// 反向：dispatcher 歸屬得到的，錄流也必須歸得到同一個 WSID。
	ok := []byte(`{"method":"` + codex.MethodAgentMessageDelta + `","params":{"threadId":"t-one","delta":"x"}}`)
	if got := a.resolveWireFrameWSID("s2c", ok); got != string(w) {
		t.Fatalf("錄流歸屬與 dispatcher 不一致：%q，want %q", got, w)
	}
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
}

// TestWireFrameRequestIDMapIsScopedToGeneration：換 generation 時 request id →
// WSID 的登記必須清空。request id 由 codex.Conn 從 1 起算，新連線的 id=1 response
// 若繼承到上一代某筆請求的登記，就是把新 server 的 frame 歸給舊 session。
func TestWireFrameRequestIDMapIsScopedToGeneration(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)

	w := mustCreate(t, a, "codex")
	a.rememberWireRequestWSID("c2s", "1", string(w))
	if got := a.takeWireRequestWSID("c2s", "1"); got != string(w) {
		t.Fatalf("登記必須取得回來：%q", got)
	}

	a.rememberWireRequestWSID("c2s", "1", string(w))
	if _, err := a.newWireGeneration(); err != nil {
		t.Fatal(err)
	}
	if got := a.takeWireRequestWSID("c2s", "1"); got != "" {
		t.Fatalf("換 generation 後不得殘留上一代的 request 登記：%q", got)
	}
	// 方向不得相撞：c2s 與 s2c 各自獨立配發 request id。
	a.rememberWireRequestWSID("s2c", "1", string(w))
	if got := a.takeWireRequestWSID("c2s", "1"); got != "" {
		t.Fatalf("s2c 的登記不得被 c2s 的 response 取走：%q", got)
	}
}

// 保底：wsid 為空的 WSID 不得被當成一個可查詢的集合（否則呼叫端會把 broadcast
// 與解不開的 frame 當成某個 session 的證據）。
func TestUnattributedFramesAreNotAQueryableSession(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	w := mustCreate(t, a, "codex")
	ctl.setNextThread("t-only")
	if err := a.StartSession(string(w), "hi", "", "", "task-only", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w)
	gen := a.wireGen
	if gen == nil {
		t.Fatal("precondition：需要一份 live generation")
	}
	if got := gen.FrameIndex().FramesOf(""); got != nil {
		t.Fatalf("空 WSID 不得回傳任何 frame：%v", got)
	}
	total, attributed := gen.FrameIndex().Totals()
	if total == 0 || attributed == 0 || attributed >= total {
		t.Fatalf("precondition：這一代必須同時有已歸屬與未歸屬的 frame（total=%d attributed=%d）",
			total, attributed)
	}
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
}
