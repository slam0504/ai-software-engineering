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
// claudeSess／activeProv／broker 全清除。
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
		return a.claudeSess == nil && a.activeProv == "" && a.broker == nil
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
		_, err := a.manager.BeginSubmit()
		return errors.Is(err, appcore.ErrNoSession)
	})
	id, err := a.manager.BeginNewSessionSubmit("task-y") // 下一個 session 可建立
	if err != nil {
		t.Fatalf("next session must be startable: %v", err)
	}
	_ = a.manager.RejectSubmit(id)
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
