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
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

// ---- M3b §3.4.4／§5.2 第 2 條：session 級錄流證據 []WireSegmentRef ----
//
// 這批測試守的是「同一 WSID 橫跨兩個 generation 的 []WireSegmentRef **完整**且
// **不混入他 session frame**」。兩個形容詞是兩件事，各自有自己的斷言與 mutation：
//
//   - 完整：該 WSID 在 wire log 裡的每一筆 frame 都落在它自己的某一段 range 內。
//   - 不混入：它的 range 內不得出現屬於別的 session 的 frame，且 For(w) 不得回傳
//     別的 WSID 的 SegmentRef。
//
// **oracle 不是 SegmentSet 自己**（失效形狀 H）：判斷「這個 frame 屬於誰」一律回
// 去讀 <wire_log_id>.jsonl 的原文，比對 frame 裡帶的 threadId——SegmentSet 只提供
// 被檢驗的 range，不參與判定。
//
// 三種 generation 觸發都要覆蓋：B1 受控 restart、server 意外死亡後重建、app 重啟
// （跨 process，失效形狀 F——由 wire-logs/segments.jsonl 的 replay 守）。

// ---- fixture：會回話的 fake app-server ----

// respondingWire 是 codexServerFactory 的測試控制器。app_wirelog_latch_test.go 的
// fakeWireControl 只把 c2s 倒進 io.Discard（那批測試驗的是編排順序，server 不必
// 回話）；segment 的 frame range 必須由真的走完 thread/start → turn/start →
// turn/completed 的 session 產生，所以這裡每一代 server 都掛一個會回話的 responder。
type respondingWire struct {
	mu         sync.Mutex
	servers    []*fakeCodexServer
	nextThread string // 下一筆 thread/start 要回的 thread id（thread/resume 一律回傳呼叫端帶的那個）
	turnSeq    atomic.Int32
}

func (c *respondingWire) setNextThread(id string) {
	c.mu.Lock()
	c.nextThread = id
	c.mu.Unlock()
}

func (c *respondingWire) last() *fakeCodexServer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.servers) == 0 {
		return nil
	}
	return c.servers[len(c.servers)-1]
}

func (c *respondingWire) newServer(t *testing.T) (codex.ProbeTarget, error) {
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
	// 真的握手：後續的 thread/start｜turn/start 都經 rpc 層的 initialized 檢查，
	// 而且 initialize 的往返 frame 本來就該在 session 起點之前進錄流。
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
	c.servers = append(c.servers, s)
	c.mu.Unlock()
	return s, nil
}

func (c *respondingWire) respond(w *fakeCodexWire, f codex.Frame) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(f.Params, &p)
	switch f.Method {
	case codex.MethodInitialize:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodThreadStart:
		c.mu.Lock()
		tid := c.nextThread
		c.mu.Unlock()
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"thread": map[string]any{"id": tid}}})
	case codex.MethodThreadResume:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"thread": map[string]any{"id": p.ThreadID}}})
	case codex.MethodTurnStart:
		turnID := fmt.Sprintf("turn-%d", c.turnSeq.Add(1))
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		w.send(map[string]any{"method": codex.MethodTurnCompleted, "params": map[string]any{
			"threadId": p.ThreadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
	}
}

// bootWireApp：把 responding wire 接上一個已經有真實 registry 的 App。registry
// 必須是真的（不是 stubRegistry）——跨 app 重啟時同一個 WSID 要還原得回來，才談得上
// 「同一 WSID 跨 generation」。
func bootWireApp(t *testing.T, a *App, ctl *respondingWire) {
	t.Helper()
	enableAudit(t, a)
	bootRegistry(t, a)
	a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
}

// runCodexSession：走 production 的 StartSession（→ startCodex → ensureAppServer →
// startCodexHost）跑完一輪，再 EndSession 收尾。收尾點正是 segment 的尾界，所以
// 不能只 StartSession 就斷言。
func runCodexSession(t *testing.T, a *App, ctl *respondingWire, w appcore.WSID, thread, resume string) {
	t.Helper()
	ctl.setNextThread(thread)
	// recordCase 帶值：§3.4.4 末句要求它轉成該 view 的 label，不帶就測不到那一半。
	if err := a.StartSession(string(w), "hi "+thread, resume, "rec-"+thread, "task-"+thread, ""); err != nil {
		t.Fatalf("StartSession(%s): %v", w, err)
	}
	waitTurnSettled(t, a, w)
	if err := a.EndSession(string(w)); err != nil {
		t.Fatalf("EndSession(%s): %v", w, err)
	}
}

// currentWireLogID：目前 generation 的 wire_log_id（在 wireMu 下讀，與 production
// 同一把鎖——死亡 reaper 會併發改寫這個欄位）。
func currentWireLogID(a *App) string {
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	if a.wireGen == nil {
		return ""
	}
	return a.wireGen.ID()
}

// ---- oracle：直接讀 wire log 原文 ----

type wireFrame struct {
	Frame int             `json:"frame"`
	Raw   json.RawMessage `json:"raw"`
}

// readWireLog 讀某個 generation 的錄流原文。**這是本檔全部歸屬判定的唯一來源**：
// 「第 N 筆 frame 屬於哪個 thread」由 frame 原文回答，不由 SegmentSet 回答。
func readWireLog(t *testing.T, a *App, wireLogID string) []wireFrame {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.wireLogDir(), wireLogID+".jsonl"))
	if err != nil {
		t.Fatalf("讀 wire log %s：%v", wireLogID, err)
	}
	var out []wireFrame
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row wireFrame
		if jerr := json.Unmarshal([]byte(line), &row); jerr != nil {
			t.Fatalf("wire log %s 壞行：%v（%s）", wireLogID, jerr, line)
		}
		out = append(out, row)
	}
	return out
}

// framesMentioning：整份錄流裡提到某個 thread id 的 frame 編號。
func framesMentioning(rows []wireFrame, thread string) []int {
	var out []int
	for _, r := range rows {
		if strings.Contains(string(r.Raw), `"`+thread+`"`) {
			out = append(out, r.Frame)
		}
	}
	return out
}

// assertSegmentsCoverThread：**完整**——某 WSID 在它的每一份 generation 裡，
// 所有提到自己 thread id 的 frame 都必須落在自己的 range 內。
func assertSegmentsCoverThread(t *testing.T, a *App, segs []wirelog.SegmentRef, thread string) {
	t.Helper()
	covered := map[string]map[int]bool{}
	for _, s := range segs {
		if covered[s.WireLogID] == nil {
			covered[s.WireLogID] = map[int]bool{}
		}
		for f := s.StartFrame; f <= s.EndFrame; f++ {
			covered[s.WireLogID][f] = true
		}
	}
	total := 0
	for id, frames := range covered {
		rows := readWireLog(t, a, id)
		mine := framesMentioning(rows, thread)
		total += len(mine)
		for _, f := range mine {
			if !frames[f] {
				t.Fatalf("thread %s 的 frame #%d（wire log %s）沒有被自己的 []SegmentRef 涵蓋：segs=%+v",
					thread, f, id, segs)
			}
		}
	}
	if total == 0 {
		t.Fatalf("precondition：錄流裡找不到任何提到 thread %s 的 frame，這條測試量不到東西", thread)
	}
}

// assertSegmentsExcludeThread：**不混入**——某 WSID 的 range 內不得出現屬於別的
// session 的 frame。
func assertSegmentsExcludeThread(t *testing.T, a *App, segs []wirelog.SegmentRef, foreign string) {
	t.Helper()
	for _, s := range segs {
		rows := readWireLog(t, a, s.WireLogID)
		for _, r := range rows {
			if r.Frame < s.StartFrame || r.Frame > s.EndFrame {
				continue
			}
			if strings.Contains(string(r.Raw), `"`+foreign+`"`) {
				t.Fatalf("segment %+v 混入了他 session（thread %s）的 frame #%d：%s",
					s, foreign, r.Frame, r.Raw)
			}
		}
	}
}

// segmentView 是稽核出口 codex_wire_segments 的形狀。
type segmentView struct {
	WSID      string               `json:"wsid"`
	Label     string               `json:"label"`
	Segments  []wirelog.SegmentRef `json:"segments"`
	Exclusive bool                 `json:"exclusive"`
	Note      string               `json:"note"`
	// Frames（§3.4.3）：wire_log_id → 該代裡確實歸屬本 WSID 的 frame 編號。
	// range 是窗口、frames 是歸屬——並行時前者必然含他 session 的 frame，
	// 逐 frame 的答案只在這裡（見 app_wire_frames_test.go）。
	//
	// 同步階段**只含 live 那一代**；歷史那幾代由背景 worker 展開後另寫一筆
	// codex_wire_segment_frames（見 app_wire_frame_jobs_test.go）。FramesStatus
	// 就是「這份 view 的 frames 算完了沒」的可觀測狀態。
	Frames          map[string][]int `json:"frames"`
	ViewID          string           `json:"viewId"`
	FramesStatus    string           `json:"framesStatus"`
	PendingWireLogs []string         `json:"pendingWireLogs"`
}

// auditSegments：稽核可讀出口——codex_wire_segments 記錄的最後一份 view。
// §3.4.4 沒有要求 UI 呈現 []WireSegmentRef，證據的消費者因此是稽核而不是畫面；
// 這個 helper 就是那一格的讀者。
func auditSegments(t *testing.T, a *App, wsid appcore.WSID) segmentView {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	var got segmentView
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Kind string      `json:"kind"`
			Data segmentView `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Kind != "codex_wire_segments" {
			continue
		}
		if rec.Data.WSID != string(wsid) {
			continue
		}
		got, found = rec.Data, true
	}
	if !found {
		t.Fatalf("稽核裡沒有 %s 的 codex_wire_segments——session 級錄流證據沒有可讀出口", wsid)
	}
	return got
}

// ---- 測試 ----

// TestWireSegmentsSpanControlledRestart：§5.2 第 2 條的主測項，觸發面 = **B1 受控
// restart**。w1 在 gen1 跑一輪 → w2 也在 gen1 跑一輪 → B1 restart 開 gen2 →
// w1 以 resume 再跑一輪。之後 w1 的 []SegmentRef 必須橫跨兩個 wire_log_id、涵蓋
// 自己在兩份錄流裡的全部 frame、且不含 w2 的任何 frame。
//
// mutation（各自只打紅一條）：
//   - beginWireSegment 改到 publishCodexHost 之後才記起點 → 「完整」紅
//     （thread/start｜resume 的往返 frame 落在 range 外）。
//   - closeWireSegment 的 EndFrame 改成 h.wireStart → 「完整」紅（turn 的 frame 落在外）。
//   - closeWireSegment 的尾界改讀 a.wireGen（而非 h.wireGen）→ 「不混入」紅。
//   - For 改成回傳全部 WSID 的 segment → 段數／「不混入」紅。
func TestWireSegmentsSpanControlledRestart(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w1 := mustCreate(t, a, "codex")
	w2 := mustCreate(t, a, "codex")

	runCodexSession(t, a, ctl, w1, "t-w1", "")
	runCodexSession(t, a, ctl, w2, "t-w2", "")

	// B1 受控 restart：live host 已收乾，refuseIfCodexLive 放行 → 新 generation。
	if err := a.RestartCodexServerRecorded("b1-segment"); err != nil {
		t.Fatalf("B1 受控 restart：%v", err)
	}
	runCodexSession(t, a, ctl, w1, "t-w1", "t-w1") // 同一個 WSID、同一個 thread，新 generation

	segs := a.wireSegments.For(string(w1))
	if len(segs) != 2 {
		t.Fatalf("w1 必須留下兩段（每個 generation 一段）：%+v", segs)
	}
	if segs[0].WireLogID == segs[1].WireLogID {
		t.Fatalf("兩段必須落在不同 generation（B1 restart 開新 wire_log_id）：%+v", segs)
	}
	assertSegmentsCoverThread(t, a, segs, "t-w1")
	assertSegmentsExcludeThread(t, a, segs, "t-w2")

	seg2 := a.wireSegments.For(string(w2))
	if len(seg2) != 1 || seg2[0].WireLogID != segs[0].WireLogID {
		t.Fatalf("w2 只有第一個 generation 的一段：%+v（w1=%+v）", seg2, segs)
	}
	assertSegmentsCoverThread(t, a, seg2, "t-w2")
	assertSegmentsExcludeThread(t, a, seg2, "t-w1")

	// 稽核可讀：最後一次收尾寫下的 view 與記憶體／磁碟一致。
	view := auditSegments(t, a, w1)
	if len(view.Segments) != 2 || view.Segments[0] != segs[0] || view.Segments[1] != segs[1] {
		t.Fatalf("稽核記錄的 view 必須是完整有序的兩段：%+v（want %+v）", view.Segments, segs)
	}
	// §3.4.4 末句：recordCase 轉為**該 view 的 label**（不是另一條要靠 wsid join
	// 的獨立稽核線）。
	if view.Label != "rec-t-w1" {
		t.Fatalf("view 必須帶 recordCase label：%q", view.Label)
	}
	// 本測試三個 session 完全序列化（前一個收尾後才起下一個），range 是排他的。
	if !view.Exclusive {
		t.Fatalf("序列化情境下 range 必須標為排他：%+v", view)
	}
}

// TestWireSegmentsSurviveServerDeath：觸發面 = **server 意外死亡後重建**，同時是
// **尾界必須取自 h.wireGen 而不是 a.wireGen** 的守門。
//
// 時序刻意排成「w1 的 host 還活著時 generation 就被換掉」——這是三個觸發面裡唯一
// 做得到這件事的（B1／受控復原都被 refuseIfCodexLive 擋住 live host）：
//
//	w1 起 session（gen1）→ server 意外死亡 → w2 起 session（ensureAppServer 開 gen2）
//	→ **此時才** EndSession(w1)：a.wireGen 已指向 gen2，但 w1 的尾界必須是 gen1 自己的。
//
// 尾界寫死成 gen1 的 frame 總數（不是「小於等於」）：closeWireSegment 若改讀
// a.wireGen，這裡拿到的會是 gen2 的計數，而 gen2 此刻已經寫了 w2 的 frame，
// 數字必然不同。
func TestWireSegmentsSurviveServerDeath(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w1 := mustCreate(t, a, "codex")
	w2 := mustCreate(t, a, "codex")

	ctl.setNextThread("t-a")
	if err := a.StartSession(string(w1), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w1)
	gen1 := ctl.last()
	gen1ID := currentWireLogID(a)

	gen1.die() // 意外死亡：watcher 以 errServerDied finalize gen1
	waitFor(t, "gen1 已被 reaper 收尾", func() bool {
		o, ok := a.codexSingle.Current()
		return !ok || o.Generation.Finalized()
	})

	// w2 讓 ensureAppServer 重建 → a.wireGen 換成 gen2，而 w1 的 host 還在 registry。
	// **多送一輪**：兩代的 frame 數必須不同，尾界斷言才有辨識力（見下方 precondition
	// ——第一版兩代都是「handshake ＋ 一個 session 一輪」，frame 數剛好都是 8，
	// 「尾界改讀 a.wireGen」的 mutation 完全量不到，reviewer 抓到）。
	ctl.setNextThread("t-b")
	if err := a.StartSession(string(w2), "hi", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w2)
	if err := a.SendMessage(string(w2), "second turn"); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w2)
	if err := a.EndSession(string(w2)); err != nil {
		t.Fatal(err)
	}
	gen2ID := currentWireLogID(a)
	if gen2ID == gen1ID {
		t.Fatal("precondition：死亡重建必須開新 generation，否則這條測試量不到尾界來源")
	}
	gen1Frames, gen2Frames := len(readWireLog(t, a, gen1ID)), len(readWireLog(t, a, gen2ID))
	if gen1Frames == gen2Frames {
		t.Fatalf("precondition：兩代的 frame 數必須不同，否則尾界斷言沒有辨識力（gen1=%d gen2=%d）",
			gen1Frames, gen2Frames)
	}

	if err := a.EndSession(string(w1)); err != nil { // 尾界在 a.wireGen 已換人之後才結算
		t.Fatal(err)
	}

	segs := a.wireSegments.For(string(w1))
	if len(segs) != 1 || segs[0].WireLogID != gen1ID {
		t.Fatalf("w1 的段必須留在自己那份 generation：%+v（gen1=%s）", segs, gen1ID)
	}
	if want := gen1Frames - 1; segs[0].EndFrame != want {
		t.Fatalf("尾界必須是 gen1 自己的 frame 計數：EndFrame=%d，want %d（讀成 a.wireGen 就會偏掉）",
			segs[0].EndFrame, want)
	}
	assertSegmentsCoverThread(t, a, segs, "t-a")
	assertSegmentsExcludeThread(t, a, segs, "t-b")

	// 死亡重建之後同一個 WSID 再起一次：段落跨到新 generation。
	runCodexSession(t, a, ctl, w1, "t-a", "t-a")
	segs = a.wireSegments.For(string(w1))
	if len(segs) != 2 || segs[0].WireLogID == segs[1].WireLogID {
		t.Fatalf("w1 必須橫跨死亡前後兩個 generation：%+v", segs)
	}
	assertSegmentsCoverThread(t, a, segs, "t-a")
	assertSegmentsExcludeThread(t, a, segs, "t-b")
}

// TestWireSegmentsSurviveAppRestart：觸發面 = **app 重啟（跨 process）**——失效形狀
// (F) 的守門。第一個 App 記下 gen1 的那一段之後整個關掉（shutdown 走完總序），
// 第二個 App 以同一組 workspace／stateDir 重開：磁碟上的 SegmentRef 必須 replay
// 回來，新 generation 的第二段接在它後面。
//
// 「假裝重啟」（同一個 App 實例清掉記憶體）守不到這一維，所以這裡一定要開第二個
// App，且第二個 App 的 SegmentSet 必須由 production 的 openWireSegments 開出來。
//
// mutation：openWireSegments 改用 wirelog.NewSegmentSet（純記憶體）→ 這條紅、
// 單一 process 的兩條照樣綠。
func TestWireSegmentsSurviveAppRestart(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w := mustCreate(t, a, "codex")
	runCodexSession(t, a, ctl, w, "t-x", "")
	before := a.wireSegments.For(string(w))
	if len(before) != 1 {
		t.Fatalf("第一個 app 執行必須留下一段：%+v", before)
	}
	a.shutdown(a.ctx) // 走 production 總序（含 segment journal 收尾）

	// 真正的重啟：新的 App／新的 Manager／新的 registry Store／新的 SegmentSet。
	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl2 := &respondingWire{}
	bootWireApp(t, b, ctl2)

	if got := b.wireSegments.For(string(w)); len(got) != 1 || got[0] != before[0] {
		t.Fatalf("重啟後必須從磁碟 replay 回上一次執行的 segment：%+v（want %+v）", got, before)
	}

	runCodexSession(t, b, ctl2, w, "t-x", "t-x") // 新 process、新 generation

	segs := b.wireSegments.For(string(w))
	if len(segs) != 2 {
		t.Fatalf("同一 WSID 必須橫跨 app 重啟前後兩個 generation：%+v", segs)
	}
	if segs[0] != before[0] {
		t.Fatalf("重啟前那一段不得被改寫：%+v（want %+v）", segs[0], before[0])
	}
	if segs[0].WireLogID == segs[1].WireLogID {
		t.Fatalf("跨 app 重啟的兩段不得共用 wire_log_id（否則舊錄流已被新的覆寫）：%+v", segs)
	}
	// 舊 generation 的錄流檔仍在、且沒有被新 generation 覆寫掉。
	assertSegmentsCoverThread(t, b, segs[1:], "t-x")
	if rows := readWireLog(t, b, segs[0].WireLogID); len(rows) == 0 {
		t.Fatalf("重啟前那份 wire log 不得消失：%s", segs[0].WireLogID)
	}
}

// TestWireSegmentsConcurrentRangeIsNotExclusive：**並行情境的誠實邊界**（§5.2
// 第 2 條的「不混入」在並行下只成立於 set-level）。
//
// 場景是多 session 工作台的常態：w1 起了 session 跑完一輪但不收尾（長命 session），
// w2 在**同一個 generation** 上整段起跑收尾，最後才收 w1。
//
// 這條測試同時斷言三件事：
//
//  1. **set-level 的「不混入」仍成立**——For(w1) 不含 w2 的 SegmentRef，反之亦然。
//     並行情境此前連這一半都零覆蓋。
//  2. **frame-level 的汙染是真的**——w1 的 range 內確實含 w2 的 frame。刻意把它寫成
//     斷言而不是註解：這是已知邊界，哪天實作改成在別的 session 起／收的邊界切段，
//     這條會紅，那時必須連同證據出口的限定詞一起重審。
//  3. **證據出口有限定詞**——codex_wire_segments 的 exclusive=false ＋ note。
//     沒有它，稽核者會照排他證據讀一個實質是「整代錄流」的 range，違反 Fail Loud。
//
// mutation：beginWireSegment 拿掉重疊登記（wireOpenSegs 那段）→ 第 3 點紅、
// 前兩點照樣綠（限定詞是獨立的一格）。
func TestWireSegmentsConcurrentRangeIsNotExclusive(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w1 := mustCreate(t, a, "codex")
	w2 := mustCreate(t, a, "codex")

	ctl.setNextThread("t-long")
	if err := a.StartSession(string(w1), "hi", "", "rec-long", "task-long", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w1)

	// w1 尚未收尾：w2 整段落在 w1 的窗口內（reviewer 實測的形狀）。
	runCodexSession(t, a, ctl, w2, "t-short", "")

	if err := a.EndSession(string(w1)); err != nil {
		t.Fatal(err)
	}

	s1, s2 := a.wireSegments.For(string(w1)), a.wireSegments.For(string(w2))
	if len(s1) != 1 || len(s2) != 1 {
		t.Fatalf("兩個 session 各留一段：w1=%+v w2=%+v", s1, s2)
	}
	// (1) set-level：各自的 view 不得含對方的 SegmentRef。
	if s1[0] == s2[0] {
		t.Fatalf("For 不得回傳他 session 的 segment：w1=%+v w2=%+v", s1, s2)
	}
	for _, r := range s1 {
		if r == s2[0] {
			t.Fatalf("For(w1) 混入了 w2 的 segment：%+v", s1)
		}
	}

	// (2) frame-level 汙染是真的（已知邊界，見 doc）。
	polluted := 0
	for _, row := range readWireLog(t, a, s1[0].WireLogID) {
		if row.Frame < s1[0].StartFrame || row.Frame > s1[0].EndFrame {
			continue
		}
		if strings.Contains(string(row.Raw), `"t-short"`) {
			polluted++
		}
	}
	if polluted == 0 {
		t.Fatalf("precondition：並行汙染應該存在（w1 range=%+v）——若實作已改成邊界切段，"+
			"這條與證據出口的限定詞要一起重審", s1[0])
	}

	// (3) 證據出口必須帶限定詞。
	v1 := auditSegments(t, a, w1)
	if v1.Exclusive {
		t.Fatalf("並行情境下 range 非排他，稽核出口必須標明：%+v", v1)
	}
	if v1.Note == "" {
		t.Fatalf("非排他時必須附白話說明，不能只給一個 bool：%+v", v1)
	}
	if v2 := auditSegments(t, a, w2); v2.Exclusive {
		t.Fatalf("重疊是雙向的，被包住的那一段同樣非排他：%+v", v2)
	}
}

// TestWireSegmentNotRecordedForForeignConn：起點的 generation 必須由**本 session 的
// conn** 決定，不能讀全域 a.wireGen（review Minor）。
//
// 場景：a.wireGen 指著一份活著的 generation，但這個 session 掛在**另一條 conn** 上
// （production 的可達形狀是「ensureAppServer 取得 srv 之後、thread/start 送出之前，
// 另一個 goroutine 因 server 死亡換了代」——窄窗，但後果是假證據而不是漏證據）。
// 這個 session 一個 frame 都沒寫進 a.wireGen 那份錄流，卻會拿到一段涵蓋**別人
// frame** 的 range。
//
// 讓汙染看得見：w1 全程活著並在 w2 的窗口內送第二輪（frame 進真的那份錄流）。
// mutation：beginWireSegment 改回讀 a.wireGen → For(w2) 不再是空的 → 紅。
func TestWireSegmentNotRecordedForForeignConn(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w1 := mustCreate(t, a, "codex")
	w2 := mustCreate(t, a, "codex")

	ctl.setNextThread("t-real")
	if err := a.StartSession(string(w1), "hi", "", "", "task-real", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w1)
	if currentWireLogID(a) == "" {
		t.Fatal("precondition：必須有一份活著的 generation")
	}

	// w2 掛在一條與目前 generation 無關的 conn 上。
	foreign, fwire := newFakeCodexConn(t)
	var turnSeq atomic.Int32
	fwire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			fwire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart:
			fwire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"thread": map[string]any{"id": "t-foreign"}}})
		case codex.MethodTurnStart:
			turnID := fmt.Sprintf("fturn-%d", turnSeq.Add(1))
			fwire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
			fwire.send(map[string]any{"method": codex.MethodTurnCompleted, "params": map[string]any{
				"threadId": "t-foreign", "turn": map[string]any{"id": turnID, "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := foreign.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	a.wireCodexConn(foreign) // 外來 conn 也要掛 dispatcher，w2 的 turn/completed 才回得來
	a.codexHostOverride = fakeCodexHost{foreign}
	if err := a.StartSession(string(w2), "hi", "", "", "task-foreign", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w2)

	// w2 的窗口內，真的那份錄流有新 frame 進來（讀 a.wireGen 就會把它們吞進 w2 的 range）。
	if err := a.SendMessage(string(w1), "second turn"); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w1)

	if err := a.EndSession(string(w2)); err != nil {
		t.Fatal(err)
	}
	if got := a.wireSegments.For(string(w2)); len(got) != 0 {
		t.Fatalf("掛在別條 conn 上的 session 不得拿到 connection-wide 錄流的 range（假證據）：%+v", got)
	}
}

// TestWireSegmentEmptyRangeIsAuditedNotFabricated：range 為空時不得補一段假的、
// 也不得無聲跳過（Fail Loud）。
//
// **直接呼叫 closeWireSegment，不走完整 production 路徑**，並且是刻意的：起點改成
// 由 conn 反查 generation 之後，「有 generation 但期間零 frame」在 production 路徑上
// 已經不可達（EnsureThread 必然至少寫一筆 c2s）。分支仍保留當防禦，這條測試覆蓋
// 的就是那個防禦分支本身。
func TestWireSegmentEmptyRangeIsAuditedNotFabricated(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)

	gen, err := a.newWireGeneration()
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionHost{wsid: appcore.WSID("W-EMPTY"), provider: contract.ProviderCodex, sockIndex: -1}
	h.wireGen, h.wireStart = gen, gen.Frames() // 期間一個 frame 都沒寫

	a.closeWireSegment(h)

	if got := a.wireSegments.For("W-EMPTY"); len(got) != 0 {
		t.Fatalf("空 range 不得落盤成假證據：%+v", got)
	}
	if !auditHasKind(t, a.stateDir, "codex_wire_segment_empty") {
		t.Fatal("空 range 必須留下稽核，不得無聲跳過")
	}
}

// TestOpenWireSegmentsFailureDegradesLoudly：segment 索引開不起來時，fail loud
// （audit ＋ 啟動警告）但**不阻擋 codex session**——錄流本體（wire log）不受影響，
// 缺的只是 session 級歸屬索引。
func TestOpenWireSegmentsFailureDegradesLoudly(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	// 在 segment journal 的位置放一個目錄：OpenSegmentSet 必然開檔失敗。
	if err := a.wireSegments.Close(); err != nil { // newTestAppIn 已經開過一份
		t.Fatal(err)
	}
	if err := os.Remove(a.wireSegmentsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.wireSegmentsPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	a.wireSegments = nil
	a.openWireSegments(a.lease)

	if a.wireSegments != nil {
		t.Fatal("開檔失敗不得留下半成品的 SegmentSet")
	}
	if !auditHasKind(t, a.stateDir, "wire_segments_open_error") {
		t.Fatal("開檔失敗必須留下稽核")
	}
	if !strings.Contains(a.startupErrText(), "segment") {
		t.Fatalf("開檔失敗必須進啟動警告（UI 讀得到）：%q", a.startupErrText())
	}

	// 降級之後 codex session 仍要起得來、wire log 照錄。
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)
	w := mustCreate(t, a, "codex")
	runCodexSession(t, a, ctl, w, "t-degraded", "")
	if rows := readWireLog(t, a, currentWireLogID(a)); len(rows) == 0 {
		t.Fatal("segment 索引降級不得影響 wire log 本體")
	}
}

// TestWireLogIDUniqueAcrossAppRuns：跨 app 執行的 wire_log_id 不得相撞。
//
// 這是 []SegmentRef 跨 app 重啟能不能成立的前提：SegmentRef 只記 wire_log_id，
// 兩次執行的第一個 generation 若配到同一個 id，wirelog.NewGeneration 會直接覆寫
// （沿用 recorder.New 的慣例，不做去重），上一次執行留下的 SegmentRef 就會指向
// 一份內容已經被換掉的檔案——frame range 對到別人的 frame，而且沒有任何錯誤。
//
// 時間戳只到秒、序號每次啟動從 1 重數，所以「同一秒內重啟」是可達的（崩潰後立即
// 重開、使用者連按重啟）。這條測試把兩個 App 的第一次配置排在同一次呼叫序列裡，
// 撞秒的條件必然成立。
//
// mutation：newWireGeneration 拿掉 run token → 兩個 id 相同、且第一份檔案被截斷。
func TestWireLogIDUniqueAcrossAppRuns(t *testing.T) {
	a, _ := newTestApp(t)
	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir) // 同一組目錄＝同一台機器上的下一次執行

	genA, err := a.newWireGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if lerr := genA.Line(wirelog.DirClientToServer, []byte(`{"method":"run-a"}`)); lerr != nil {
		t.Fatal(lerr)
	}
	genB, err := b.newWireGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if genA.ID() == genB.ID() {
		t.Fatalf("兩次 app 執行的第一個 generation 不得共用 wire_log_id：%s", genA.ID())
	}
	// 內容層再確認一次：撞 id 的實際後果就是前一份被 os.Create 截斷。
	raw, rerr := os.ReadFile(filepath.Join(a.wireLogDir(), genA.ID()+".jsonl"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), "run-a") {
		t.Fatalf("上一次執行的錄流被新 generation 覆寫了：%q", raw)
	}
}

// TestWireSegmentAppendFailureFailsLoud：Append 失敗不得靜默——latch degraded 之後
// 每一次收尾都要留下稽核與 workspace 通知（§3.4.6 的 fail loud 精神；SegmentSet
// 自己的 latch 在 internal/wirelog 已測，這裡守的是 App 這一層有沒有把它接出來）。
func TestWireSegmentAppendFailureFailsLoud(t *testing.T) {
	a, ui := newTestApp(t)
	ctl := &respondingWire{}
	bootWireApp(t, a, ctl)

	w := mustCreate(t, a, "codex")
	ctl.setNextThread("t-fail")
	if err := a.StartSession(string(w), "hi", "", "", "task", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w)
	if err := a.wireSegments.Close(); err != nil { // 關掉底層 handle：下一次 Append 必失敗
		t.Fatal(err)
	}
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}

	if notes := wireNotices(t, ui, "codex_wire_segments"); len(notes) == 0 {
		t.Fatal("segment 落盤失敗必須發 workspace 通知，不得靜默")
	}
	if !auditHasKind(t, a.stateDir, "codex_wire_segment_error") {
		t.Fatal("segment 落盤失敗必須留下稽核")
	}
}

// auditHasKind：稽核裡有沒有某個 kind（auditHasOp 的 kind-only 版）。
func auditHasKind(t *testing.T, stateDir, kind string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["kind"] == kind {
			return true
		}
	}
	return false
}
