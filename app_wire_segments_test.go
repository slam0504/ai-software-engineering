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

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
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
	if err := a.StartSession(string(w), "hi "+thread, resume, "", "task-"+thread, ""); err != nil {
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

// auditSegments：稽核可讀出口——codex_wire_segments 記錄的最後一份 view。
// §3.4.4 沒有要求 UI 呈現 []WireSegmentRef，證據的消費者因此是稽核而不是畫面；
// 這個 helper 就是那一格的讀者。
func auditSegments(t *testing.T, a *App, wsid appcore.WSID) []wirelog.SegmentRef {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	var got []wirelog.SegmentRef
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Kind string `json:"kind"`
			Data struct {
				WSID     string               `json:"wsid"`
				Segments []wirelog.SegmentRef `json:"segments"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Kind != "codex_wire_segments" {
			continue
		}
		if rec.Data.WSID != string(wsid) {
			continue
		}
		got, found = rec.Data.Segments, true
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
	if got := auditSegments(t, a, w1); len(got) != 2 || got[0] != segs[0] || got[1] != segs[1] {
		t.Fatalf("稽核記錄的 view 必須是完整有序的兩段：%+v（want %+v）", got, segs)
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
	runCodexSession(t, a, ctl, w2, "t-b", "")
	if currentWireLogID(a) == gen1ID {
		t.Fatal("precondition：死亡重建必須開新 generation，否則這條測試量不到尾界來源")
	}

	if err := a.EndSession(string(w1)); err != nil { // 尾界在 a.wireGen 已換人之後才結算
		t.Fatal(err)
	}

	segs := a.wireSegments.For(string(w1))
	if len(segs) != 1 || segs[0].WireLogID != gen1ID {
		t.Fatalf("w1 的段必須留在自己那份 generation：%+v（gen1=%s）", segs, gen1ID)
	}
	if want := len(readWireLog(t, a, gen1ID)) - 1; segs[0].EndFrame != want {
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
