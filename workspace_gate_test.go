package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 R2：閘門（startSession／sendMessage）----
//
// 每個拒絕案例都斷言「沒有任何 session／provider 啟動副作用」，而且拒絕之後確認、再啟動
// 必須成功——後者證明 Manager 的 submit 狀態沒有被拒絕的那一次洩漏（ErrSessionActive／
// ErrSubmitActive）。

func requireConfirmationError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errWorkspaceConfirmationRequired) {
		t.Fatalf("必須回 workspace_confirmation_required，實得 %v", err)
	}
	if !strings.HasPrefix(err.Error(), "workspace_confirmation_required") {
		t.Fatalf("錯誤訊息必須以 workspace_confirmation_required 開頭：%q", err.Error())
	}
}

func fileSizeOrZero(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// writeCountingClaude：fake claude CLI——每次被 exec 記一行 spawn.log，每收到一行 stdin 記一行
// input.log。拒絕案例用「spawn.log 不存在」證明沒有 exec，用 input.log 的行數證明沒有送出 turn。
func writeCountingClaude(t *testing.T, a *App) (spawnLog, inputLog string) {
	t.Helper()
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	spawnLog = filepath.Join(a.stateDir, "spawn.log")
	inputLog = filepath.Join(a.stateDir, "input.log")
	script := "#!/bin/sh\necho spawn >> " + spawnLog + "\nwhile read -r line; do\n" +
		"echo \"$line\" >> " + inputLog + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return spawnLog, inputLog
}

func lineCount(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// startArtifacts：session 啟動會留下的檔案——broker socket、MCP config、recorder 檔案。拒絕前後
// 各取一次比對（resume 案例先前的執行留下的檔案不算；只有「拒絕的那一次」不得新增）。
func startArtifacts(t *testing.T, a *App) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"mcp-*.json", "approval-*.sock", "recordings/*"} {
		m, _ := filepath.Glob(filepath.Join(a.stateDir, pattern))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

// armFakeCodex：與 startCodexForTest 相同的 fake app-server 回應器＋握手，但**不**呼叫
// StartSession——拒絕案例要在 wire 已就緒的情況下呼叫，這樣任何漏過閘門的 thread/start、
// turn/start 都看得見。
func armFakeCodex(t *testing.T, a *App) *fakeCodexWire {
	t.Helper()
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn) // 通知（turn/completed 等）才會路由到 session；與既有 fake wire 測試相同
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
			wire.send(map[string]any{"method": codex.MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": turnID, "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	a.codexHostOverride = fakeCodexHost{conn}
	return wire
}

func countMethod(w *fakeCodexWire, method string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, f := range w.seen {
		if f.Method == method {
			n++
		}
	}
	return n
}

func TestStartSessionGate_ClaudeRejectedWithoutSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, resume string
		priorRun     bool // resume 的真實形狀：這個 session 先前跑完並結束過（reducer 停在 done）
	}{{"首輪", "", false}, {"resume", "sess-old", true}} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			a.wsReg = &stubRegistry{}
			spawnLog, _ := writeCountingClaude(t, a)
			w := mustCreate(t, a, "claude")
			if tc.priorRun {
				// 先前的執行發生在確認之前不需要的 workspace 狀態（NotRequired）。結束之後 reducer 停在
				// done；若拒絕的啟動偷偷呼叫過 BeginNewSessionSubmit（即使事後 RejectSubmit），
				// newSessionLocked 會把 reducer 重設成 idle——下面的狀態比對因此抓得到。
				if err := a.StartSession(string(w), "prior", "", "", "t", ""); err != nil {
					t.Fatal(err)
				}
				waitTurnSettled(t, a, w)
				if err := a.EndSession(string(w)); err != nil {
					t.Fatal(err)
				}
				spawnsBefore := lineCount(t, spawnLog)
				if spawnsBefore != 1 {
					t.Fatalf("前提：先前的執行 exec 過一次 CLI，實得 %d", spawnsBefore)
				}
			}
			makePending(a)
			providerStarts := 0
			a.hookBeforeProviderStart = func() { providerStarts++ }
			stateBefore, _ := a.manager.State(w)
			eventsBefore := fileSizeOrZero(t, a.eventsPath())
			artifactsBefore := startArtifacts(t, a)

			err := a.StartSession(string(w), "hi", tc.resume, "rec-case", "task", "")
			requireConfirmationError(t, err)
			if providerStarts != 0 {
				t.Fatal("拒絕時不得走到 BeginNewSessionSubmit 之後（hookBeforeProviderStart 被呼叫）")
			}
			if a.hostFor(w) != nil {
				t.Fatal("拒絕時不得建立 host")
			}
			wantSpawns := 0
			if tc.priorRun {
				wantSpawns = 1 // 只有先前那一次
			}
			if got := lineCount(t, spawnLog); got != wantSpawns {
				t.Fatalf("拒絕時不得 exec claude CLI（spawn.log %d 行，want %d）", got, wantSpawns)
			}
			if got := startArtifacts(t, a); !reflect.DeepEqual(got, artifactsBefore) {
				t.Fatalf("拒絕時不得新增 broker socket／MCP config／recorder 檔案：%v → %v", artifactsBefore, got)
			}
			if got, _ := a.manager.State(w); got != stateBefore {
				t.Fatalf("Manager 狀態不得改變：%v → %v", stateBefore, got)
			}
			if after := fileSizeOrZero(t, a.eventsPath()); after != eventsBefore {
				t.Fatalf("拒絕時不得寫入 events.jsonl（%d → %d）", eventsBefore, after)
			}

			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			if err := a.StartSession(string(w), "hi", "", "", "task", ""); err != nil {
				t.Fatalf("確認之後必須可以啟動（證明 submit 狀態沒有被拒絕的那一次洩漏）：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
		})
	}
}

func TestStartSessionGate_CodexRejectedWithoutThreadOrTurn(t *testing.T) {
	for _, tc := range []struct{ name, resume, policy string }{
		{"首輪 untrusted", "", "untrusted"},
		{"首輪 approvalPolicy=never", "", "never"},
		{"resume never", "thread-old", "never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			wire := armFakeCodex(t, a)
			w := wsidFor(t, a, contract.ProviderCodex)
			makePending(a)
			providerStarts := 0
			a.hookBeforeProviderStart = func() { providerStarts++ }
			stateBefore, _ := a.manager.State(w)

			err := a.StartSession(string(w), "hi", tc.resume, "rec-case", "task", tc.policy)
			requireConfirmationError(t, err)
			if providerStarts != 0 {
				t.Fatal("拒絕時不得走到 BeginNewSessionSubmit 之後")
			}
			for _, m := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
				if n := countMethod(wire, m); n != 0 {
					t.Fatalf("拒絕時不得送出 %s（%d 次）", m, n)
				}
			}
			if a.hostFor(w) != nil {
				t.Fatal("拒絕時不得建立 host")
			}
			if got, _ := a.manager.State(w); got != stateBefore {
				t.Fatalf("Manager 狀態不得改變：%v → %v", stateBefore, got)
			}

			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			if err := a.StartSession(string(w), "hi", "", "", "task", tc.policy); err != nil {
				t.Fatalf("確認之後必須可以啟動：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
			if countMethod(wire, codex.MethodThreadStart) != 1 || countMethod(wire, codex.MethodTurnStart) != 1 {
				t.Fatal("確認之後的啟動必須送出 thread/start 與 turn/start 各一次")
			}
		})
	}
}

// 一個確認，所有 session 共用（R4）。
func TestStartSessionGate_OneConfirmationCoversEverySession(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	writeCountingClaude(t, a)
	w1 := mustCreate(t, a, "claude")
	w2 := mustCreate(t, a, "claude")
	makePending(a)
	for _, w := range []appcore.WSID{w1, w2} {
		requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", ""))
	}
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	for _, w := range []appcore.WSID{w1, w2} {
		if err := a.StartSession(string(w), "hi", "", "", "t", ""); err != nil {
			t.Fatalf("同一個確認必須涵蓋所有 session：%v", err)
		}
		w := w
		t.Cleanup(func() { _ = a.EndSession(string(w)) })
	}
}

// 防禦性的 sendMessage 閘門：先在 NotRequired 下啟動 session，再把狀態翻成 Pending
// （模擬「未來有 restore 自動啟動 host」之類讓 active session 早於確認存在的情況）。
func TestSendMessageGate_ClaudeRejectedWithoutSendingTurn(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	_, inputLog := writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "first", "", "", "t", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitTurnSettled(t, a, w)
	if got := lineCount(t, inputLog); got != 1 {
		t.Fatalf("前提：首輪送出 1 行，實得 %d", got)
	}

	makePending(a)
	requireConfirmationError(t, a.SendMessage(string(w), "second"))
	if got := lineCount(t, inputLog); got != 1 {
		t.Fatalf("拒絕時不得把訊息送進 CLI（input.log %d 行）", got)
	}
	if st, _ := a.manager.State(w); st != contract.StateDone {
		t.Fatalf("拒絕時 Manager 狀態不得改變（BeginSubmit 不得被呼叫）：%v", st)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "second"); err != nil {
		t.Fatalf("確認之後必須可送出（證明 submit 狀態沒有洩漏）：%v", err)
	}
	waitFor(t, "第二輪送進 CLI", func() bool { return lineCount(t, inputLog) == 2 })
}

func TestSendMessageGate_CodexRejectedWithoutTurnStart(t *testing.T) {
	a, _ := newTestApp(t)
	wire := armFakeCodex(t, a)
	w := wsidFor(t, a, contract.ProviderCodex)
	if err := a.StartSession(string(w), "first", "", "", "t", "never"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitTurnSettled(t, a, w)
	if got := countMethod(wire, codex.MethodTurnStart); got != 1 {
		t.Fatalf("前提：首輪 1 次 turn/start，實得 %d", got)
	}

	makePending(a)
	requireConfirmationError(t, a.SendMessage(string(w), "second"))
	if got := countMethod(wire, codex.MethodTurnStart); got != 1 {
		t.Fatalf("拒絕時不得送出 turn/start（%d 次）", got)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "second"); err != nil {
		t.Fatalf("確認之後必須可送出：%v", err)
	}
	if got := countMethod(wire, codex.MethodTurnStart); got != 2 {
		t.Fatalf("確認之後第二輪必須送出 turn/start：%d", got)
	}
}

// 不改變各入口既有檢查的錯誤優先順序（spec R2 Pre；Review Focus 3）。
func TestGate_PreservesExistingErrorPrecedence(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	// 未知的 wsid：resolveWSID 的 ErrSessionNotFound 先於確認閘（startSession／sendMessage 皆然）。
	if err := a.StartSession("no-such-wsid", "hi", "", "", "t", ""); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("StartSession 未知 wsid 必須維持 ErrSessionNotFound：%v", err)
	}
	if err := a.SendMessage("no-such-wsid", "hi"); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("SendMessage 未知 wsid 必須維持 ErrSessionNotFound：%v", err)
	}

	// startSession 原有的 registryUncertain 檢查先於確認閘。
	reg.mu.Lock()
	reg.uncertain = true
	reg.mu.Unlock()
	if err := a.StartSession(string(w), "hi", "", "", "t", ""); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("registryUncertain 必須先於確認閘：%v", err)
	}
	// 確認閘不會把 registryUncertain 加到 sendMessage：沒有 active session 時，
	// 既有行為是 BeginSubmit 的錯誤，而不是 registryUncertain；Pending 時則是確認閘。
	if err := a.SendMessage(string(w), "hi"); errors.Is(err, errRegistryUncertain) {
		t.Fatalf("不得把 registryUncertain 加到 sendMessage：%v", err)
	}
}

// 整條 production 啟動路徑（acquireStateLease → resolveWorkspace → 確認狀態 → 閘門）：
// 其餘測試用 resolve() 直接把 App 放進指定狀態，只有這一條驗證「production 真的接了線」。
// 反例（spec §6.3）：env 指向「家目錄的隔離替身目錄」——HOME 與 WORKBENCH_WORKSPACE 都設成
// 同一個隔離目錄——必須要求確認，送出訊息必須被拒。不放 Playwright E2E 的理由見計畫 Task 8。
func TestProductionStartupPath_EnvEqualsHomeRequiresConfirmation(t *testing.T) {
	home := realDir(t, filepath.Join(t.TempDir(), "home"))
	t.Setenv("HOME", home)
	t.Setenv("WORKBENCH_WORKSPACE", home)

	a := NewApp()
	a.emitUI = func(string, any) {} // 不碰 wails runtime
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })
	if a.startupErrText() != "" {
		t.Fatalf("前提：乾淨的隔離目錄不應有 startup error：%s", a.startupErrText())
	}

	st := mustStatus(t, a)
	if !st.Required || st.Confirmed || st.Workspace != home || st.Source != "env" ||
		!reflect.DeepEqual(st.Reasons, []string{wsReasonIsHome}) {
		t.Fatalf("env=$HOME 必須要求確認，且原因是 is_home：%+v", st)
	}
	w, err := a.CreateSession("claude", "t") // 建立 session 不是 AI 工作，Pending 時仍可進行
	if err != nil {
		t.Fatalf("Pending 時 CreateSession 必須可用：%v", err)
	}
	requireConfirmationError(t, a.StartSession(w, "hi", "", "", "t", ""))
	requireConfirmationError(t, a.SendMessage(w, "hi"))

	if err := a.ConfirmWorkspace(st.Workspace); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, a); !st.Confirmed {
		t.Fatalf("確認之後：%+v", st)
	}
}

// 同一條路徑的對照組：env 指向家目錄「底下」的專案（HOME 與 workspace 不同）→ 不需確認，
// 行為與 B4 之前相同（E2E／CI 的 env 注入形狀）。
func TestProductionStartupPath_EnvProjectNeedsNoConfirmation(t *testing.T) {
	root := realDir(t, t.TempDir())
	home := realDir(t, filepath.Join(root, "home"))
	project := realDir(t, filepath.Join(home, "projects", "foo"))
	t.Setenv("HOME", home)
	t.Setenv("WORKBENCH_WORKSPACE", project)

	a := NewApp()
	a.emitUI = func(string, any) {}
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	st := mustStatus(t, a)
	if st.Required || st.Confirmed || st.Workspace != project || len(st.Reasons) != 0 {
		t.Fatalf("家目錄底下的專案不需確認：%+v", st)
	}
}
