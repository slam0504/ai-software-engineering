package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- production 接線測試基盤 ----

type uiEvent struct {
	name string
	data any
}

type uiCapture struct {
	mu     sync.Mutex
	events []uiEvent
	envs   []contract.Envelope
}

func (c *uiCapture) emit(name string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, uiEvent{name, data})
}

func (c *uiCapture) emitEnv(env contract.Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.envs = append(c.envs, env)
}

func (c *uiCapture) find(name string) []uiEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []uiEvent
	for _, e := range c.events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

func (c *uiCapture) findEnvKind(kind string) []contract.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []contract.Envelope
	for _, e := range c.envs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newTestApp(t *testing.T) (*App, *uiCapture) {
	t.Helper()
	a := NewApp()
	a.ctx = context.Background()
	ws, err := claude.NormalizeCWD(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.workspaceDir = ws
	// stateDir 用短路徑：unix socket（approval.sock）有 ~104 byte sockaddr 上限，
	// macOS 的 t.TempDir()（/var/folders/…）會超過。
	short, err := os.MkdirTemp("/tmp", "wb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	a.stateDir = filepath.Join(short, ".workbench")
	if err := os.MkdirAll(filepath.Join(a.stateDir, "recordings"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.toolsDirPath = filepath.Join(ws, "tools")
	reg, err := claude.OpenRegistry(filepath.Join(a.stateDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.registry = reg
	ui := &uiCapture{}
	a.emitUI = ui.emit
	sink, err := appcore.NewJSONLSink(filepath.Join(a.stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a.manager = appcore.New(appcore.Config{Sink: sink, Emit: ui.emitEnv})
	return a, ui
}

// fakeCodexWire：package main 版 fake app-server（in-memory pipes；production
// 的 wireCodexConn／startCodexHost 走同一 *codex.Conn）。
type fakeCodexWire struct {
	t     *testing.T
	outMu sync.Mutex
	out   io.Writer
	mu    sync.Mutex
	onReq func(f codex.Frame)
	seen  []codex.Frame
}

func (w *fakeCodexWire) sawMethod(method string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.seen {
		if f.Method == method {
			return true
		}
	}
	return false
}

func (w *fakeCodexWire) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.t.Errorf("fake send marshal: %v", err)
		return
	}
	w.outMu.Lock()
	defer w.outMu.Unlock()
	_, _ = w.out.Write(append(b, '\n'))
}

func (w *fakeCodexWire) setOnReq(h func(f codex.Frame)) {
	w.mu.Lock()
	w.onReq = h
	w.mu.Unlock()
}

func newFakeCodexConn(t *testing.T) (*codex.Conn, *fakeCodexWire) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	w := &fakeCodexWire{t: t, out: s2cW}
	conn := codex.NewConn(c2sW, s2cR)
	go func() {
		sc := bufio.NewScanner(c2sR)
		for sc.Scan() {
			var f codex.Frame
			if json.Unmarshal(sc.Bytes(), &f) != nil {
				continue
			}
			if f.Method != "" && f.ID != nil { // client request；response 給 server request 的忽略
				w.mu.Lock()
				w.seen = append(w.seen, f)
				h := w.onReq
				w.mu.Unlock()
				if h != nil {
					h(f)
				}
			}
		}
		_ = sc.Err() // cleanup 關 pipe 屬預期收尾，非測試失敗
	}()
	t.Cleanup(func() { _ = c2sW.Close(); _ = s2cW.Close() })
	return conn, w
}

type fakeCodexHost struct{ conn *codex.Conn }

func (h fakeCodexHost) Conn() *codex.Conn      { return h.conn }
func (h fakeCodexHost) Argv() []string         { return []string{"fake-codex"} }
func (h fakeCodexHost) StderrSnapshot() string { return "" }

// P1 迴歸：首輪 turn/completed 先於 turn/start response 抵達時，notification
// handler 必須能透過已發布的 a.runner 命中 earlyEnded latch——不殘留 busy、
// 第二輪可送；同一空窗中的 approval 也必須帶 thread ID。
func TestCodexFirstTurnCompletedBeforeResponse(t *testing.T) {
	a, ui := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	var turnSeq atomic.Int32
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case codex.MethodTurnStart:
			n := turnSeq.Add(1)
			turnID := fmt.Sprintf("turn-%d", n)
			if n == 1 { // 惡意順序：approval 請求與 completed 都先於 response
				wire.send(map[string]any{"id": 990, "method": codex.MethodCmdExecRequestApproval,
					"params": map[string]any{"itemId": "item-1"}})
				wire.send(map[string]any{"method": codex.MethodTurnCompleted,
					"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": turnID, "status": "completed"}}})
			}
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		}
	})

	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}

	threadID, alreadyEnded, err := a.startCodexHost(fakeCodexHost{conn}, "hi", "", "", "untrusted")
	if err != nil || threadID != "t1" {
		t.Fatalf("start: %q %v", threadID, err)
	}
	if !alreadyEnded {
		t.Fatal("completed-before-response must reconcile as alreadyEnded via published runner")
	}
	r := a.currentRunner()
	if r == nil || r.ActiveTurnID() != "" {
		t.Fatalf("runner must be published and not busy, got %v", r)
	}
	if _, _, err := r.StartTurn(context.Background(), "round two"); err != nil { // 第二輪可送
		t.Fatalf("second turn must start: %v", err)
	}
	// approval envelope 於 codexApproval 阻塞前已 Emit；驗證帶 thread ID 後
	// 在主 goroutine 解決 pending approval（避免留下等 timeout 的 goroutine）。
	waitFor(t, "approval envelope with thread id", func() bool {
		for _, e := range ui.findEnvKind(string(contract.KindApproval)) {
			if e.SessionID == "t1" {
				return true
			}
		}
		return false
	})
	if envs := ui.findEnvKind(string(contract.KindInit)); len(envs) != 1 || envs[0].SessionID != "t1" {
		t.Fatalf("start must emit exactly one init envelope with thread id, got %+v", envs)
	}
	waitFor(t, "approval:request dialog event", func() bool { return len(ui.find("approval:request")) > 0 })
	d := ui.find("approval:request")[0].data.(map[string]any)
	if err := a.ResolveApproval(d["id"].(string), true, ""); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
}

// P1 迴歸（第二輪 review）：start 交易 abort（如 shutdown 與 StartSession 交錯，
// Accept 得 ErrClosed）時，reaper 不得等 process EOF——MultiTurn CLI 仍在等
// 輸入，必須立即 teardown：process 界限內退出、lease finalized、
// claudeSess／broker 全清除。
func TestClaudeAbortedStartIsReclaimed(t *testing.T) {
	a, ui := newTestApp(t)
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	// long-running fake：讀首則訊息後等 stdin EOF 才退出（不主動結束）
	script := "#!/bin/sh\nread -r _line\ncat >/dev/null\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a.hookAfterProviderStart = func() { _ = a.manager.Close() } // Accept 前 Close → ErrClosed → commit(false)

	err := a.StartSession("claude", "hi", "", "claude-abort", "task-x", "")
	if !errors.Is(err, appcore.ErrClosed) {
		t.Fatalf("aborted start must surface ErrClosed, got %v", err)
	}
	waitFor(t, "session:done after abort", func() bool { return len(ui.find("session:done")) > 0 })
	waitFor(t, "state reclaimed", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.claudeSess == nil && a.broker == nil
	})
	meta, err := os.ReadFile(filepath.Join(a.stateDir, "recordings", "claude-abort.meta.json"))
	if err != nil {
		t.Fatalf("lease must be finalized (meta written): %v", err)
	}
	if !strings.Contains(string(meta), `"exit_code": 0`) { // CloseSequence 關 stdin → 自然退出
		t.Fatalf("meta must record exit 0, got: %s", meta)
	}
}

// P1 迴歸：claude 快速退出（auth／參數錯誤等）不得被接受成「active 的死亡
// session」——自然結束 goroutine 等 start 交易 commit 後收尾，最終回 idle、
// 發出 session:done、可建立下一個 session。
func TestClaudeFastExitDoesNotLeaveDeadActiveSession(t *testing.T) {
	a, ui := newTestApp(t)
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nread -r _line\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"bye\"}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a.hookAfterProviderStart = func() { // deterministic：pump 收乾後才 Accept
		a.mu.Lock()
		done := a.claudePumpDone
		a.mu.Unlock()
		<-done
	}

	if err := a.StartSession("claude", "hi", "", "", "task-x", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session:done", func() bool { return len(ui.find("session:done")) > 0 })
	waitFor(t, "manager idle", func() bool {
		_, err := a.manager.BeginSubmit(contract.ProviderClaude)
		return errors.Is(err, appcore.ErrNoSession)
	})
	id, err := a.manager.BeginNewSessionSubmit(contract.ProviderClaude, "task-y") // 下一個 session 可建立
	if err != nil {
		t.Fatalf("next session must be startable: %v", err)
	}
	_ = a.manager.RejectSubmit(contract.ProviderClaude, id)
}

func TestWorkspaceReadSecurity(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.md"), []byte("# hi"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
	os.Symlink(outside, filepath.Join(root, "linkdir"))
	big := make([]byte, 1<<20+1)
	os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644)

	a := NewApp()
	a.workspaceDir, _ = claude.NormalizeCWD(root)

	if got, err := a.ReadWorkspaceFile("ok.md"); err != nil || got != "# hi" {
		t.Fatalf("normal read: %v %q", err, got)
	}
	if _, err := a.ReadWorkspaceFile("../etc/passwd"); err == nil {
		t.Fatal("dot-dot escape must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("link.txt"); err == nil {
		t.Fatal("symlink file to outside must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("linkdir/secret.txt"); err == nil {
		t.Fatal("symlink dir traversal must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("big.bin"); err == nil {
		t.Fatal(">1MB must be rejected before full read")
	}
	if _, err := a.ListWorkspace(".."); err == nil {
		t.Fatal("list escape must be rejected")
	}
}

// ---- M1.5-T2：雙 session 並存與 forced shutdown ----

// writeMultiTurnClaude：每讀一行輸出一輪 assistant+result；stdin 關閉即退出。
func writeMultiTurnClaude(t *testing.T, a *App) {
	t.Helper()
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nwhile read -r _line; do\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// startCodexForTest：以 production 交易（BeginNewSessionSubmit→startCodexHost→Accept）
// 建立 codex session（StartSession 的 codex 分支等價，host 換 fake wire）。
func startCodexForTest(t *testing.T, a *App, wire *fakeCodexWire, conn *codex.Conn, recordCase, task string) {
	t.Helper()
	var turnSeq atomic.Int32
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart, codex.MethodThreadResume:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case codex.MethodTurnStart:
			turnID := fmt.Sprintf("turn-%d", turnSeq.Add(1))
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
			// 隨即完成 turn（解 busy）；busy 情境由個別測試覆蓋 onReq
			wire.send(map[string]any{"method": codex.MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": turnID, "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	id, err := a.manager.BeginNewSessionSubmit(contract.ProviderCodex, task)
	if err != nil {
		t.Fatal(err)
	}
	threadID, _, err := a.startCodexHost(fakeCodexHost{conn}, "hi codex", "", recordCase, "untrusted")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.manager.AcceptSubmit(contract.ProviderCodex, id, threadID, "hi codex"); err != nil {
		t.Fatal(err)
	}
}

func TestDualSessionsConcurrently(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	if err := a.StartSession("claude", "hi claude", "", "claude-dual", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "codex-dual", "task-x")
	if !a.manager.SessionActive(contract.ProviderClaude) || !a.manager.SessionActive(contract.ProviderCodex) {
		t.Fatal("both sessions must be active concurrently")
	}

	// 各自 SendMessage 一輪（claude 等 result 解鎖後送；codex turn/start 立即回）
	waitFor(t, "claude first result", func() bool {
		return len(ui.findEnvKind("result")) >= 1
	})
	if err := a.SendMessage("claude", "round 2"); err != nil {
		t.Fatalf("claude send: %v", err)
	}
	if err := a.SendMessage("codex", "round 2"); err != nil {
		t.Fatalf("codex send while claude busy: %v", err) // 跨 provider 不阻塞
	}
	waitFor(t, "claude second result", func() bool { return len(ui.findEnvKind("result")) >= 2 })

	// 事件 provider／task 隔離
	ui.mu.Lock()
	for _, e := range ui.envs {
		if e.Provider == "claude" && e.TaskID != "" && e.TaskID != "task-c" {
			ui.mu.Unlock()
			t.Fatalf("claude envelope carries wrong task: %+v", e)
		}
		if e.Provider == "codex" && e.TaskID != "" && e.TaskID != "task-x" {
			ui.mu.Unlock()
			t.Fatalf("codex envelope carries wrong task: %+v", e)
		}
	}
	ui.mu.Unlock()

	if err := a.EndSession("claude"); err != nil {
		t.Fatalf("end claude: %v", err)
	}
	if err := a.EndSession("codex"); err != nil {
		t.Fatalf("end codex: %v", err)
	}
	for _, name := range []string{"claude-dual.meta.json", "codex-dual.meta.json"} { // 錄流各自收尾
		if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", name)); err != nil {
			t.Fatalf("recording meta %s: %v", name, err)
		}
	}
}

func TestEndOneProviderLeavesOtherActive(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession("claude", "hi", "", "", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "", "task-x")
	if err := a.EndSession("claude"); err != nil {
		t.Fatal(err)
	}
	if a.manager.SessionActive(contract.ProviderClaude) {
		t.Fatal("claude must be ended")
	}
	if !a.manager.SessionActive(contract.ProviderCodex) { // 另一 provider 不受影響
		t.Fatal("codex must stay active")
	}
	if err := a.EndSession("codex"); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownForcedWaitsForBoth(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession("claude", "hi", "", "claude-fsd", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "codex-fsd", "task-x")
	// 起一個不完成的 turn（busy）；interrupt 回應正常
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodTurnStart:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-busy", "status": "inProgress"}}})
		case codex.MethodTurnInterrupt:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		}
	})
	if err := a.SendMessage("codex", "long task"); err != nil {
		t.Fatal(err)
	}
	a.track.NoteStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-busy"}}`))
	if a.currentRunner().ActiveTurnID() == "" {
		t.Fatal("precondition: codex turn must be active")
	}

	if err := a.forcedShutdown(); err != nil {
		t.Fatalf("forced shutdown: %v", err)
	}
	if !wire.sawMethod(codex.MethodTurnInterrupt) { // busy turn 先被 interrupt
		t.Fatal("forced shutdown must interrupt the active codex turn")
	}
	for _, name := range []string{"claude-fsd.meta.json", "codex-fsd.meta.json"} { // 兩邊 lease 都 finalize
		if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", name)); err != nil {
			t.Fatalf("lease not finalized (%s): %v", name, err)
		}
	}
	if len(ui.find("session:done")) < 2 { // 兩邊 session:done 都發出
		t.Fatalf("session:done count = %d, want >= 2", len(ui.find("session:done")))
	}
	if a.manager.SessionActive(contract.ProviderClaude) || a.manager.SessionActive(contract.ProviderCodex) {
		t.Fatal("both sessions must be ended")
	}
}

func TestShutdownJoinsErrors(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession("claude", "hi", "", "", "task-c", ""); err != nil { // claude 無錄流
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "codex-joinerr", "task-x")
	// 弄壞 codex meta 寫入（claude 無錄流不受影響）
	if err := os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o755) })

	err := a.forcedShutdown()
	if err == nil { // codex meta 寫失敗必須以 errors.Join 浮出
		t.Fatal("codex lease error must surface")
	}
	waitFor(t, "claude session:done despite codex error", func() bool {
		for _, e := range ui.find("session:done") {
			if d, ok := e.data.(map[string]any); ok && d["provider"] == "claude" {
				return true
			}
		}
		return false
	})
	if a.manager.SessionActive(contract.ProviderClaude) || a.manager.SessionActive(contract.ProviderCodex) {
		t.Fatal("one side's error must not skip the other side's teardown")
	}
}

func TestShutdownHungProviderIsBounded(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession("claude", "hi", "", "", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "", "task-x")
	wire.setOnReq(func(f codex.Frame) {
		if f.Method == codex.MethodTurnStart {
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-hang", "status": "inProgress"}}})
		}
		// interrupt 完全無回應（hang）：靠 5s ctx timeout 兜底
	})
	if err := a.SendMessage("codex", "long task"); err != nil {
		t.Fatal(err)
	}
	a.track.NoteStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-hang"}}`))

	start := time.Now()
	_ = a.forcedShutdown() // interrupt timeout 屬 best-effort，不影響收尾
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("forced shutdown must be bounded, took %v", elapsed)
	}
	if a.manager.SessionActive(contract.ProviderClaude) || a.manager.SessionActive(contract.ProviderCodex) {
		t.Fatal("hung interrupt must not block either teardown")
	}
}
