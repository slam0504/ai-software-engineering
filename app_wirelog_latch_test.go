package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/proc"
)

// ---- M3b Task 13：recorder error latch＋in-process 受控復原（§3.4.6-7）----

// fakeCodexServer 是 codex.ProbeTarget 的 package main 版替身：帶一條真的
// codex.Conn（in-memory pipes），因此 always-on 錄流的 attach／detach、stdout
// 汲取完成（Conn.Done）與 wire log 寫入都走 production 路徑，只有「spawn 一個
// codex 子行程」被換掉。收尾相關呼叫都記進共用的步驟序列，§3.4.7 的順序契約
// 才驗得到 App 與 codex 兩層合起來的實際順序。
type fakeCodexServer struct {
	conn      *codex.Conn
	s2c       io.Writer // 測試用：由「server」端推一筆 frame 進 readLoop
	closeWire func()
	steps     func(string)
	hsErr     error

	done      chan struct{}
	closeOnce sync.Once
}

func (s *fakeCodexServer) Done() <-chan struct{} { return s.done }
func (s *fakeCodexServer) Conn() *codex.Conn     { return s.conn }
func (s *fakeCodexServer) BeginRecording(sink func([]byte) error) error {
	s.steps("attach")
	return s.conn.BeginRecording(sink)
}
func (s *fakeCodexServer) StopRecording() error { return s.conn.StopRecording() }
func (s *fakeCodexServer) Handshake(context.Context, codex.ClientInfo) error {
	s.steps("handshake")
	return s.hsErr
}
func (s *fakeCodexServer) Terminate() error {
	s.steps("terminate")
	s.die()
	return nil
}
func (s *fakeCodexServer) Wait() proc.Exit {
	s.steps("wait")
	<-s.done
	return proc.Exit{Code: 0, StderrTail: "fake-stderr"}
}
func (s *fakeCodexServer) StderrSnapshot() string { return "fake-stderr" }
func (s *fakeCodexServer) Argv() []string         { return []string{"fake-codex", "app-server"} }

// die 模擬 process 退出：關掉兩端 pipe，讓 readLoop 讀到 EOF（Conn.Done 關閉，
// 即 §3.4.2 的「stdout 汲取完成」訊號），並關 Done()。
func (s *fakeCodexServer) die() {
	s.closeOnce.Do(func() {
		s.closeWire()
		close(s.done)
	})
}

// fakeWireControl 是 codexServerFactory 的測試控制器：步驟序列、每一代 server
// 的把手，以及 start／handshake 的故障注入。
type fakeWireControl struct {
	mu            sync.Mutex
	steps         []string
	servers       []*fakeCodexServer
	failStart     error
	failHandshake bool
}

func (c *fakeWireControl) note(step string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = append(c.steps, step)
}

func (c *fakeWireControl) order() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.steps...)
}

func (c *fakeWireControl) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = nil
}

func (c *fakeWireControl) last() *fakeCodexServer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.servers) == 0 {
		return nil
	}
	return c.servers[len(c.servers)-1]
}

func (c *fakeWireControl) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.servers)
}

func (c *fakeWireControl) newServer(t *testing.T) (codex.ProbeTarget, error) {
	c.mu.Lock()
	failStart, failHS := c.failStart, c.failHandshake
	c.mu.Unlock()
	if failStart != nil {
		return nil, failStart
	}
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	s := &fakeCodexServer{
		conn:      codex.NewConn(c2sW, s2cR),
		s2c:       s2cW,
		closeWire: func() { _ = s2cW.Close(); _ = c2sW.Close(); _ = c2sR.Close() },
		steps:     c.note,
		done:      make(chan struct{}),
	}
	if failHS {
		s.hsErr = errors.New("handshake refused")
	}
	go func() { _, _ = io.Copy(io.Discard, c2sR) }() // 排掉 client 送出的 frame
	t.Cleanup(s.die)
	c.mu.Lock()
	c.servers = append(c.servers, s)
	c.mu.Unlock()
	return s, nil
}

// newTestAppWithFakeWire：newTestApp ＋ fake app-server 工廠 ＋ 步驟探針 ＋
// 可讀的 audit sink（受控 replacement 的 wasActive=false 補發只在 audit 留痕）。
func newTestAppWithFakeWire(t *testing.T) (*App, *uiCapture, *fakeWireControl) {
	t.Helper()
	a, ui := newTestApp(t)
	a.wsReg = &stubRegistry{}
	ctl := &fakeWireControl{}
	a.hookWireStep = ctl.note
	a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
	f, err := os.Create(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a.auditF = f
	t.Cleanup(func() { _ = f.Close() })
	return a, ui, ctl
}

// wireNotices 取出 workspace lane 上本 component 的通知（latch／復原都走這裡）。
func wireNotices(t *testing.T, ui *uiCapture, component string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, env := range ui.findEnvKind(string(contract.KindStreamError)) {
		var p map[string]string
		if json.Unmarshal(env.Payload, &p) != nil {
			continue
		}
		if p["component"] == component {
			out = append(out, p)
		}
	}
	return out
}

func TestLatchBlocksNewSessionButNotRecovery(t *testing.T) {
	a, ui, ctl := newTestAppWithFakeWire(t)
	a.latchWireRecorder(errors.New("disk full"))

	if _, err := a.CreateSession("codex", "t"); err == nil {
		t.Fatal("latch 必須拒絕新 Codex session")
	} else if !errors.Is(err, errWireLogDegraded) {
		t.Fatalf("拒絕原因必須是錄流 latch：%v", err)
	}
	if _, err := a.CreateSession("claude", "t"); err != nil {
		t.Fatalf("latch 不得波及 Claude：%v", err)
	}
	if err := a.RecoverCodexRecording(); err != nil {
		t.Fatalf("latch 不得擋受控復原：%v", err)
	}
	if a.wireLatched() {
		t.Fatal("新 generation 的 recorder 掛載、handshake 與發布全部成功後應解除 latch")
	}
	if ctl.count() != 1 {
		t.Fatalf("復原必須起一個新 generation 的 server：%d", ctl.count())
	}
	// 解除之後新 session 立刻可建
	if _, err := a.CreateSession("codex", "t2"); err != nil {
		t.Fatalf("latch 解除後必須恢復建立：%v", err)
	}
	if len(wireNotices(t, ui, "codex_wire_log")) != 2 { // degraded ＋ recovered
		t.Fatalf("latch 與復原各發一次 workspace 通知：%v", wireNotices(t, ui, "codex_wire_log"))
	}
}

func TestRecoveryOrderAndFailureKeepsLatch(t *testing.T) {
	a, _, ctl := newTestAppWithFakeWire(t)
	if _, err := a.ensureAppServer(); err != nil { // 先有一個已發布的 generation
		t.Fatalf("ensureAppServer: %v", err)
	}
	oldOwner, ok := a.codexSingle.Current()
	if !ok {
		t.Fatal("precondition: 必須已發布 owner")
	}
	a.latchWireRecorder(errors.New("disk full"))
	ctl.reset()
	ctl.mu.Lock()
	ctl.failHandshake = true
	ctl.mu.Unlock()

	if err := a.RecoverCodexRecording(); err == nil {
		t.Fatal("handshake 失敗必須回錯")
	}
	if !a.wireLatched() {
		t.Fatal("失敗必須保留 latch")
	}
	if _, ok := a.codexSingle.Take(); ok {
		t.Fatal("不得留下未發布 server")
	}
	// 前 8 步是 §3.4.2／§3.4.7 的凍結順序；後兩步是「任一步失敗即 dispose 新 server」。
	want := []string{"drain", "terminate", "wait", "finalize_old", "new_wire_log_id",
		"start", "attach", "handshake", "terminate", "wait"}
	if got := ctl.order(); !reflect.DeepEqual(got, want) {
		t.Fatalf("順序必須符合 §3.4.2／§3.4.7：\n got=%v\nwant=%v", got, want)
	}
	if !oldOwner.Generation.Finalized() {
		t.Fatal("舊 generation 必須在新 generation 建立之前就 finalize")
	}
	if m := oldOwner.Generation.FinalMeta(); m.ExitCode == nil { // 受控收尾一定跑過 Wait
		t.Fatalf("舊 generation 的 meta 必須帶收尾證據：%+v", m)
	}
	// 失敗的新 generation 同樣保留 wire_log_id 與收尾證據（§3.4.7）
	metas, _ := filepath.Glob(filepath.Join(a.wireLogDir(), "*.meta.json"))
	if len(metas) != 2 {
		t.Fatalf("新舊 generation 都必須留下 meta：%v", metas)
	}
}

func TestRecoveryRefusedWhileLiveHostOrInFlightTurn(t *testing.T) {
	for _, c := range []string{"live_host", "in_flight_turn"} {
		t.Run(c, func(t *testing.T) {
			a, _, ctl := newTestAppWithFakeWire(t)
			conn, wire := newFakeCodexConn(t)
			a.wireCodexConn(conn)
			mustCreate(t, a, "codex")
			startCodexForTest(t, a, wire, conn, "", "task-x")
			if c == "in_flight_turn" {
				mustBeginCodexTurn(t, a, wire)
			}
			a.latchWireRecorder(errors.New("x"))

			err := a.RecoverCodexRecording()
			if err == nil {
				t.Fatal("存在 live host／in-flight turn 時必須拒絕（§3.4.7）")
			}
			if c == "in_flight_turn" && !strings.Contains(err.Error(), "in-flight turn") {
				t.Fatalf("in-flight turn 必須是被拒的原因：%v", err)
			}
			if !a.wireLatched() {
				t.Fatal("拒絕不得改變 latch 狀態")
			}
			if ctl.count() != 0 {
				t.Fatal("被拒的復原不得起新 server")
			}
			if got := ctl.order(); len(got) != 0 {
				t.Fatalf("被拒的復原不得動到既有 generation：%v", got)
			}
		})
	}
}

// mustBeginCodexTurn：起一個不會完成的 turn（busy），沿用
// TestShutdownForcedWaitsForBoth 的 wire 形狀。
func mustBeginCodexTurn(t *testing.T, a *App, wire *fakeCodexWire) {
	t.Helper()
	wire.setOnReq(func(f codex.Frame) {
		if f.Method == codex.MethodTurnStart {
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-busy", "status": "inProgress"}}})
		}
	})
	if err := a.SendMessage("codex", "long task"); err != nil {
		t.Fatal(err)
	}
	h := a.hostFor(a.legacyWSIDFor(contract.ProviderCodex))
	if h == nil {
		t.Fatal("precondition: codex host 必須已發布")
	}
	h.track.NoteStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-busy"}}`))
	if h.runner.ActiveTurnID() == "" {
		t.Fatal("precondition: turn 必須是 in-flight")
	}
}

func TestLatchNotifiesOncePerGeneration(t *testing.T) {
	a, ui, _ := newTestAppWithFakeWire(t)
	a.latchWireRecorder(errors.New("e1"))
	a.latchWireRecorder(errors.New("e2"))
	if n := len(wireNotices(t, ui, "codex_wire_log")); n != 1 {
		t.Fatalf("每 generation 只發一次通知：%d", n)
	}
	if err := a.codexWireGate(); err == nil || !strings.Contains(err.Error(), "e1") {
		t.Fatalf("latch 必須保留首因：%v", err)
	}
	// 換一個 generation（受控復原）之後，新的失敗必須能再發一次
	if err := a.RecoverCodexRecording(); err != nil {
		t.Fatal(err)
	}
	a.latchWireRecorder(errors.New("e3"))
	if n := len(wireNotices(t, ui, "codex_wire_log")); n != 3 { // e1 degraded ＋ recovered ＋ e3 degraded
		t.Fatalf("新 generation 必須能再發一次：%d", n)
	}
}

// Task 12 交棒 (1)：經 ensureAppServer 發布的 owner 一樣要有死亡 reaper，
// 否則那條路徑上的 wire log 在 server 意外死亡時永遠不會 finalize（且靜默）。
func TestEnsureAppServerPublishesWatchedOwner(t *testing.T) {
	a, _, ctl := newTestAppWithFakeWire(t)
	srv, err := a.ensureAppServer()
	if err != nil {
		t.Fatalf("ensureAppServer: %v", err)
	}
	o, ok := a.codexSingle.Current()
	if !ok || o.Server != srv {
		t.Fatal("ensureAppServer 必須發布 owner")
	}
	if o.Generation == nil {
		t.Fatal("production generation 必須帶 always-on 錄流（§3.4.1）")
	}
	// 第二次呼叫沿用同一個存活 server，不建新 generation
	if again, err := a.ensureAppServer(); err != nil || again != srv {
		t.Fatalf("存活的 server 必須沿用：%v %v", again, err)
	}
	if ctl.count() != 1 {
		t.Fatalf("不得重複 spawn：%d", ctl.count())
	}

	gen := o.Generation
	ctl.last().die() // 意外死亡：測試只關 Done／pipe，收尾必須由 production reaper 做
	waitFor(t, "死亡 generation 被 reaper finalize", gen.Finalized)
	if _, ok := a.codexSingle.Current(); ok {
		t.Fatal("死亡的 owner 必須被 reaper 自 Single 取走")
	}
	if _, err := os.Stat(filepath.Join(a.wireLogDir(), gen.ID()+".meta.json")); err != nil {
		t.Fatalf("wire log meta 必須落盤：%v", err)
	}
	waitFor(t, "意外死亡進 audit", func() bool { return auditHas(t, a.stateDir, "codex_server_died") })
}

// Task 12 交棒 (2)：受控 replacement 之後，舊 owner 的 watcher 仍會以
// wasActive=false 補發一次——App 不得把它當成「現役 server 意外死亡」。
func TestControlledReplacementDoesNotReportDeath(t *testing.T) {
	a, ui, ctl := newTestAppWithFakeWire(t)
	if _, err := a.ensureAppServer(); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverCodexRecording(); err != nil { // 受控 replacement
		t.Fatal(err)
	}
	waitFor(t, "舊 owner 的 watcher 回報", func() bool {
		return auditHas(t, a.stateDir, "codex_generation_finalized")
	})
	if auditHas(t, a.stateDir, "codex_server_died") {
		t.Fatal("受控 replacement 的補發不得被當成意外死亡（wasActive=false）")
	}
	if n := len(wireNotices(t, ui, "codex_app_server")); n != 0 {
		t.Fatalf("受控 replacement 不得發死亡通知：%d", n)
	}
	if a.wireLatched() {
		t.Fatal("受控 replacement 不得 re-latch")
	}
	o, ok := a.codexSingle.Current()
	if !ok || o.Server != ctl.last() {
		t.Fatal("新 owner 必須仍在位（舊 watcher 的 epoch 不符，不得取走）")
	}
	if ctl.count() != 2 {
		t.Fatalf("必須恰好兩代 server：%d", ctl.count())
	}
}

// 受控 replacement／復原之後，handlers 必須接到新 generation 的連線上，
// 否則新 server 的通知不會進任何 session（舊 conn 已死）。
func TestRecoveryRewiresConnAndWireLog(t *testing.T) {
	a, _, ctl := newTestAppWithFakeWire(t)
	if err := a.RecoverCodexRecording(); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	got := a.codexConn
	a.mu.Unlock()
	if got != ctl.last().Conn() {
		t.Fatal("發布後必須把 handlers 接到新 generation 的 conn")
	}
	o, _ := a.codexSingle.Current()
	// always-on：錄流在 handshake 之前就掛好，server 推來的 frame 直接落進該
	// generation 的錄流檔（無法歸屬的 frame 也照寫，§3.4.5）
	if _, err := ctl.last().s2c.Write([]byte(`{"method":"` + codex.MethodAccountRateLimitsUpdated + `","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.wireLogDir(), o.Generation.ID()+".jsonl")
	waitFor(t, "s2c frame 進 connection-wide 錄流", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(b), codex.MethodAccountRateLimitsUpdated)
	})
}

// latch 期間既有 session 仍可 bounded 收尾（§3.4.6），只有「新 session」被擋。
func TestLatchAllowsExistingSessionTeardown(t *testing.T) {
	a, _, _ := newTestAppWithFakeWire(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	w := mustCreate(t, a, "codex")
	startCodexForTest(t, a, wire, conn, "", "task-x")
	a.latchWireRecorder(errors.New("disk full"))

	if err := a.EndSession("codex"); err != nil {
		t.Fatalf("latch 不得擋既有 session 的收尾：%v", err)
	}
	if a.hostFor(w) != nil {
		t.Fatal("收尾後 host 必須已移除")
	}
}

// startCodex（provider 啟動）同樣被 latch 擋：dormant session 重啟等於新的
// 不可稽核活動，不屬於「既有 session bounded 收尾」。
func TestLatchBlocksProviderStart(t *testing.T) {
	a, _, _ := newTestAppWithFakeWire(t)
	conn, _ := newFakeCodexConn(t)
	a.codexHostOverride = fakeCodexHost{conn}
	w := mustCreate(t, a, "codex")
	a.latchWireRecorder(errors.New("disk full"))
	_, _, err := a.startCodex(w, "hi", "", "", "untrusted")
	if !errors.Is(err, errWireLogDegraded) {
		t.Fatalf("latch 必須擋住 provider 啟動：%v", err)
	}
}
