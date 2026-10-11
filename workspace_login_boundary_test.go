package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 H3／§5.4：Codex 登入與狀態查詢不在閘門範圍內，且不會建立 thread 或 turn ----
//
// spec 的理由：AuthStatus／StartLogin 只送 account/read、account/login/start，沒有 prompt、
// 沒有 turn/start。這裡用 fake app-server 把「登入流程實際送出的每一個 method」記下來驗證，
// 而不是只相信原碼閱讀。對照組：確認之後的 StartSession 走同一個 fake server，必須看得到
// thread/start、turn/start——證明這個記錄器看得到它該看的東西（oracle 不是恆真）。
//
// 範圍之外（spec 明確不做）：app-server 仍以 workspace 為 cwd 被啟動，CLI 自身初始化時的
// 寫入行為**未驗證**；若 owner 要求連這種啟動也禁止，那是另一個邊界。

type methodLogWire struct {
	mu      sync.Mutex
	methods []string
	turnSeq atomic.Int32
}

func (c *methodLogWire) record(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods = append(c.methods, m)
}

func (c *methodLogWire) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
}

func (c *methodLogWire) respond(w *fakeCodexWire, f codex.Frame) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(f.Params, &p)
	switch f.Method {
	case codex.MethodInitialize:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodAccountRead:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
	case codex.MethodAccountLoginStart:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"loginId": "login-1", "authUrl": ""}})
	case codex.MethodAccountLogout:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodThreadStart:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t-login"}}})
	case codex.MethodTurnStart:
		turnID := fmt.Sprintf("turn-%d", c.turnSeq.Add(1))
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		w.send(map[string]any{"method": codex.MethodTurnCompleted, "params": map[string]any{
			"threadId": p.ThreadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
	}
}

func (c *methodLogWire) newServer(t *testing.T) (codex.ProbeTarget, error) {
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
			if json.Unmarshal(sc.Bytes(), &f) != nil || f.Method == "" || f.ID == nil {
				continue // 只處理 client request
			}
			c.record(f.Method)
			c.respond(w, f)
		}
	}()
	t.Cleanup(s.die)
	return s, nil
}

func TestCodexLoginAndStatusSendNoThreadOrTurn(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &methodLogWire{}
	a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
	enableAudit(t, a)
	makePending(a) // 未確認：登入與狀態查詢仍須可用（不在閘門範圍內）

	if _, err := a.AuthStatus("codex"); err != nil {
		t.Fatalf("Pending 時 AuthStatus 不得被閘門擋下：%v", err)
	}
	if err := a.StartLogin("codex"); err != nil {
		t.Fatalf("Pending 時 StartLogin 不得被閘門擋下：%v", err)
	}
	got := ctl.seen()
	for _, want := range []string{codex.MethodAccountRead, codex.MethodAccountLoginStart} {
		if !slices.Contains(got, want) {
			t.Fatalf("登入流程應送出 %s（記錄器必須看得到它）：%v", want, got)
		}
	}
	for _, forbidden := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
		if slices.Contains(got, forbidden) {
			t.Fatalf("登入與狀態查詢不得送出 %s：%v", forbidden, got)
		}
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 0 {
		t.Fatalf("登入與狀態查詢不被閘門攔截，也就不該寫 blocked audit：%d", n)
	}

	// 對照組：確認之後的 StartSession 走同一個 fake server，記錄器必須看得到 thread/start、turn/start。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	w := wsidFor(t, a, contract.ProviderCodex)
	if err := a.StartSession(string(w), "hi", "", "", "t", "untrusted"); err != nil {
		t.Fatalf("對照組 StartSession：%v", err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	got = ctl.seen()
	for _, want := range []string{codex.MethodThreadStart, codex.MethodTurnStart} {
		if !slices.Contains(got, want) {
			t.Fatalf("對照組必須看得到 %s，否則這個記錄器驗不到任何東西：%v", want, got)
		}
	}
}

// ---- 同類入口：Logout、RestartCodexServerRecorded、RecoverCodexRecording ----
//
// owner 裁定（2026-10-11）：這三個入口維持 spec 範圍、不加閘門；但要用同一個 fake app-server
// 驗證它們一樣不會送出 thread/start、turn/start。
//   - Logout："codex" 分支走 ensureAppServer，再送 account/logout。
//   - RestartCodexServerRecorded／RecoverCodexRecording：沒有 live host 時走 replaceCodexGeneration
//     （工廠起一個新 server＋initialize 握手），不送任何 thread／turn 相關 method。
// 同樣以確認後的 StartSession 當對照組，證明記錄器看得到 thread/start、turn/start。

func TestCodexServerEntryPointsSendNoThreadOrTurn(t *testing.T) {
	cases := []struct {
		name string
		run  func(a *App) error
		want string // 該入口必須實際送出（或握手）的 method，證明記錄器看得到它
	}{
		{"Logout", func(a *App) error { return a.Logout("codex") }, codex.MethodAccountLogout},
		{"RestartCodexServerRecorded", func(a *App) error { return a.RestartCodexServerRecorded("b4-case") }, codex.MethodInitialize},
		{"RecoverCodexRecording", func(a *App) error { return a.RecoverCodexRecording() }, codex.MethodInitialize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			ctl := &methodLogWire{}
			a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
			enableAudit(t, a)
			makePending(a) // 未確認：這些入口不在閘門範圍內，必須照常可用

			if err := c.run(a); err != nil {
				t.Fatalf("Pending 時 %s 不得被閘門擋下：%v", c.name, err)
			}
			got := ctl.seen()
			if !slices.Contains(got, c.want) {
				t.Fatalf("%s 應送出 %s（記錄器必須看得到它）：%v", c.name, c.want, got)
			}
			for _, forbidden := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
				if slices.Contains(got, forbidden) {
					t.Fatalf("%s 不得送出 %s：%v", c.name, forbidden, got)
				}
			}
			if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 0 {
				t.Fatalf("%s 不被閘門攔截，也就不該寫 blocked audit：%d", c.name, n)
			}

			// 對照組：確認之後的 StartSession 走同一個 fake server。
			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			w := wsidFor(t, a, contract.ProviderCodex)
			if err := a.StartSession(string(w), "hi", "", "", "t", "untrusted"); err != nil {
				t.Fatalf("對照組 StartSession：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
			got = ctl.seen()
			for _, want := range []string{codex.MethodThreadStart, codex.MethodTurnStart} {
				if !slices.Contains(got, want) {
					t.Fatalf("對照組必須看得到 %s：%v", want, got)
				}
			}
		})
	}
}
