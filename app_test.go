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
	"github.com/slam0504/sdlc-workbench/internal/replayindex"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
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
	// 15s：-race full suite 的 package 並行會擠壓 fake CLI spawn（實測 5s 偶發逾時
	// ——單獨 ×10 與 -p 1 全綠）；放寬等待上限、斷言不變
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// waitTurnSettled：等某個 WSID 的上一輪 turn 收尾（reducer 進 terminal）。
//
// Task 30 之後 §1.1「每 session 至多一個進行中 turn」由 appcore.BeginSubmit
// fail loud 守住，所以「第一輪還在跑就送第二筆」不再是可以靠運氣通過的事——
// 需要送第二輪（或直接 BeginSubmit）的測試必須先等第一輪落地。等的是**條件**
// 不是時間（同 waitFor 慣例），因此不引入 time.Sleep 式的牆鐘相依。
//
// **不把 StateIdle 算成收尾**：idle 是「還沒開始」而不是「已經結束」，收進來
// 的話這個 helper 對一個根本還沒 StartSession 的 WSID 會立刻回傳，等於什麼都
// 沒等（假綠 helper）。真的處在 idle 的 slot，BeginSubmit 本來就回 ErrNoSession，
// 不需要靠這裡放行。
func waitTurnSettled(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	waitFor(t, "turn 收尾（"+string(w)+"）", func() bool {
		st, err := a.manager.State(w)
		return err == nil && (st == contract.StateDone || st == contract.StateFailed)
	})
}

// newTestStateLease：**唯一的**測試用 ownership capability 產生點。
//
// 這個函式只存在於 _test.go，production binary 裡沒有它——所以「測試可以不
// 取真的 flock 就開 writer」這件事無法從 production 路徑重現。刻意不提供
// 「lease 為 nil 就跳過檢查」的語意：沒有 lease 就是拒絕（owner 2026-08-18
// 裁決），測試基盤必須明確出示這份 capability。
func newTestStateLease(stateDir string) *stateLease {
	return &stateLease{stateDir: stateDir, testOnly: true}
}

func newTestApp(t *testing.T) (*App, *uiCapture) {
	t.Helper()
	ws, err := claude.NormalizeCWD(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// stateDir 用短路徑：unix socket（approval.sock）有 ~104 byte sockaddr 上限，
	// macOS 的 t.TempDir()（/var/folders/…）會超過。
	short, err := os.MkdirTemp("/tmp", "wb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	return newTestAppIn(t, ws, filepath.Join(short, ".workbench"))
}

// newTestAppIn：綁定指定 workspace／stateDir 的完整測試 App。newTestApp 的本體，
// 抽出來是為了「同一組目錄再開第二個 App」＝**跨重啟**——per-WSID 續聊身分與
// view boundary 的價值全在重啟之後，同一個實例假裝重啟守不到那一維。
func newTestAppIn(t *testing.T, ws, stateDir string) (*App, *uiCapture) {
	t.Helper()
	a := NewApp()
	a.ctx = context.Background()
	a.workspaceDir = ws
	a.stateDir = stateDir
	// 測試基盤明確出示 ownership capability。沒有這一行，openStateWriters 與
	// 所有 writer 入口一律拒絕（owner 2026-08-18：production 的空 lease 一律
	// fail closed，不得恢復成「空值就跳過檢查」）。
	a.lease = newTestStateLease(stateDir)
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
	a.eventSink = sink
	// Task 20：replay index 接進 production 路徑（Manager 的同一個 mutex 內
	// Observe）。所有既有測試因此也走 index 接線，不必各自另外組裝。
	idx, err := replayindex.OpenWith(filepath.Join(a.stateDir, "replay-index"),
		replayindex.Config{Notify: a.onIndexDegraded})
	if err != nil {
		t.Fatal(err)
	}
	a.replayIndex = idx
	// **跨重啟時 audit sink 必須在 openWireSegments 之前接上**：production 的
	// startup 正是這個順序（auditF 早於 openWireSegments 一大段），而
	// openWireSegments 會啟動 frame 歸屬的背景 worker——上一次執行沒做完的待辦在
	// 那一刻就開始補完，稽核若還沒接上，補完的證據會靜默消失。
	// 條件是「檔案已存在」＝上一個 App 開過 audit：不影響「newTestApp 預設不開
	// audit」那條前提（見 enableAudit）。
	if _, serr := os.Stat(filepath.Join(a.stateDir, "audit.jsonl")); serr == nil {
		if f, ferr := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
			a.auditF = f
			a.auditState = auditReady
			t.Cleanup(func() { _ = f.Close() })
		}
	}
	// §3.4.4：走 production 的同一個開檔入口（不是另外組一個 SegmentSet）——
	// newTestAppIn 以同一組 stateDir 重開時，磁碟上既有的 segment 也就跟著
	// production 的 replay 路徑回來，跨重啟那一維才驗得到。
	a.openWireSegments(a.lease)
	a.manager = appcore.New(appcore.Config{Sink: sink, Emit: ui.emitEnv, Index: indexOrNil(idx)})
	rs, err := openRestoreStore(filepath.Join(a.stateDir, "restore.json"), auditHighWatermark(a.eventsPath()))
	if err != nil {
		t.Fatal(err)
	}
	a.restore = rs
	return a, ui
}

// ---- Task 26：WSID 定址的測試夾具 ----

// testWSIDs：per-App 的測試用 WSID 快取（見 wsidFor）。
var (
	testWSIDMu sync.Mutex
	testWSIDs  = map[*App]map[contract.Provider]appcore.WSID{}
)

// wsidFor：測試用的「這個 App 上該 provider 的那個 session」。Task 26 刪除
// legacyWSIDFor 之後，exported binding 一律要求呼叫端自己帶 WSID，測試因此需要
// 一個穩定的位址。
//
// 刻意走 Manager 真正的建立交易（ReserveSession → CommitCreate），不是另外複製
// 一份解析邏輯：拿到的是與 CreateSession 同一種 committed slot，受測路徑因此仍是
// production 路徑。同一個 (App, provider) 恆回同一個 WSID——取代舊 legacyWSIDFor
// 「同一個 session 的整段生命週期解析到同一個 slot」那個性質。
func wsidFor(t *testing.T, a *App, p contract.Provider) appcore.WSID {
	t.Helper()
	testWSIDMu.Lock()
	byProvider, ok := testWSIDs[a]
	if !ok {
		byProvider = map[contract.Provider]appcore.WSID{}
		testWSIDs[a] = byProvider
		t.Cleanup(func() {
			testWSIDMu.Lock()
			delete(testWSIDs, a)
			testWSIDMu.Unlock()
		})
	}
	w, cached := byProvider[p]
	testWSIDMu.Unlock()
	if cached {
		return w
	}
	w, tok, err := a.manager.ReserveSession(p)
	if err != nil {
		t.Fatalf("wsidFor(%s): ReserveSession: %v", p, err)
	}
	if err := a.manager.CommitCreate(tok); err != nil {
		t.Fatalf("wsidFor(%s): CommitCreate: %v", p, err)
	}
	testWSIDMu.Lock()
	byProvider[p] = w
	testWSIDMu.Unlock()
	return w
}

// enableAudit：newTestApp 預設不開 audit.jsonl（a.auditF 為 nil，a.audit 是
// no-op）。需要斷言 audit 軌跡的測試呼叫這個，開法與 startup 逐字一致。
func enableAudit(t *testing.T, a *App) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	a.auditMu.Lock()
	a.auditF = f
	// 與 production 的 openStateWriters 同步：開成功＝進入 ready。少了這一行，
	// 「ready 之後 writer 不見」的不變量在整個測試組裡都量不到（失效形狀 (E)）。
	a.auditState = auditReady
	a.auditMu.Unlock()
	t.Cleanup(func() { _ = f.Close() })
}

// wsidStr：wsidFor 的字串版（exported binding 收的是 string）。
func wsidStr(t *testing.T, a *App, provider string) string {
	t.Helper()
	return string(wsidFor(t, a, contract.Provider(provider)))
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
// handler 必須能透過已發布的 sessionHost.runner 命中 earlyEnded latch——不殘留
// busy、第二輪可送；同一空窗中的 approval 也必須歸屬到該 thread。
// 走 startCodexHost 直呼；production StartSession 路徑的同一形態見
// app_codex_dispatch_test.go 的 TestCodexCompletedBeforeResponseOnProductionPath。
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
				// approval 帶 threadId／turnId（pinned schema 的 required；Task 0
				// live frame 佐證）——Task 9 之後 approval 一律 identity 路由。
				wire.send(map[string]any{"id": 990, "method": codex.MethodCmdExecRequestApproval,
					"params": map[string]any{"threadId": "t1", "turnId": turnID, "itemId": "item-1"}})
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

	threadID, alreadyEnded, err := a.startCodexHost(wsidFor(t, a, contract.ProviderCodex), fakeCodexHost{conn}, "hi", "", "", "untrusted")
	if err != nil || threadID != "t1" {
		t.Fatalf("start: %q %v", threadID, err)
	}
	if !alreadyEnded {
		t.Fatal("completed-before-response must reconcile as alreadyEnded via published runner")
	}
	h := a.hostFor(wsidFor(t, a, contract.ProviderCodex))
	if h == nil || h.runner == nil || h.runner.ActiveTurnID() != "" {
		t.Fatalf("host／runner must be published and not busy, got %v", h)
	}
	r := h.runner
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
// sessionHost（含 sess／broker）自 registry 移除。
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

	err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-abort", "task-x", "")
	if !errors.Is(err, appcore.ErrClosed) {
		t.Fatalf("aborted start must surface ErrClosed, got %v", err)
	}
	waitFor(t, "session:done after abort", func() bool { return len(ui.find("session:done")) > 0 })
	waitFor(t, "state reclaimed", func() bool { // host 自 registry 取出即代表 sess／broker 都已收乾
		return a.hostFor(wsidFor(t, a, contract.ProviderClaude)) == nil
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
		<-a.hostFor(wsidFor(t, a, contract.ProviderClaude)).pumpDone
	}

	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "", "task-x", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session:done", func() bool { return len(ui.find("session:done")) > 0 })
	waitFor(t, "manager idle", func() bool {
		_, err := a.manager.BeginSubmit(wsidFor(t, a, contract.ProviderClaude))
		return errors.Is(err, appcore.ErrNoSession)
	})
	id, err := a.manager.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderClaude), "task-y") // 下一個 session 可建立
	if err != nil {
		t.Fatalf("next session must be startable: %v", err)
	}
	_ = a.manager.RejectSubmit(wsidFor(t, a, contract.ProviderClaude), id)
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

// writeInitClaude：fake claude CLI——每次啟動把 argv 逐行落檔、先發 init（帶
// session_id，讓 commitClaudeResume 有東西可 commit），之後每一輪回一個 result。
// 回傳 argv 落檔路徑：「有沒有接到別人的對話」只有在 argv 那一刻才變成事實，
// 內部旗標驗不到。
func writeInitClaude(t *testing.T, a *App, sessionID string) string {
	t.Helper()
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(a.stateDir, "argv-"+sessionID+".txt")
	script := "#!/bin/sh\necho \"$@\" >> " + argvFile + "\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"" + sessionID + "\"}'\n" +
		"while read -r _line; do\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := claude.NormalizeCWD(a.workspaceDir)
	_ = a.registry.Bind(sessionID, cwd, "") // wsid 未知（測試前置綁定）：production 的 init Bind 會補齊
	return argvFile
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
	a.codexHostOverride = fakeCodexHost{conn} // production StartSession codex 分支
	if err := a.StartSession(wsidStr(t, a, "codex"), "hi codex", "", recordCase, task, "untrusted"); err != nil {
		t.Fatal(err)
	}
	// 等第一輪真的落地才回傳（上面的腳本一 start 就送 turn/completed）。
	// StartSession 回傳只代表 Accept 成功，turn/completed 是另一條 goroutine 送
	// 進來的——不等就把「第二輪能不能送」變成排程競速。這條 wait 原本是被
	// StartSession 尾端那次 restore.json 落盤的 I/O 延遲意外兜住的；per-WSID
	// writer 把那次寫入移走（registry 未接線時直接返回）之後缺口就露出來了。
	waitTurnSettled(t, a, wsidFor(t, a, contract.ProviderCodex))
}

func TestDualSessionsConcurrently(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)

	if err := a.StartSession(wsidStr(t, a, "claude"), "hi claude", "", "claude-dual", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "codex-dual", "task-x")
	if !a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) || !a.manager.IsActive(wsidFor(t, a, contract.ProviderCodex)) {
		t.Fatal("both sessions must be active concurrently")
	}

	// 各自 SendMessage 一輪（claude 等 result 解鎖後送；codex turn/start 立即回）。
	// 等的是**該 WSID 自己的** turn 收尾：findEnvKind("result") 不分 provider，
	// codex 先回的 result 就會讓它提早放行，第二筆撞上 §1.1 的 in-flight guard。
	waitTurnSettled(t, a, wsidFor(t, a, contract.ProviderClaude))
	waitTurnSettled(t, a, wsidFor(t, a, contract.ProviderCodex))
	if err := a.SendMessage(wsidStr(t, a, "claude"), "round 2"); err != nil {
		t.Fatalf("claude send: %v", err)
	}
	if err := a.SendMessage(wsidStr(t, a, "codex"), "round 2"); err != nil {
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

	if err := a.EndSession(wsidStr(t, a, "claude")); err != nil {
		t.Fatalf("end claude: %v", err)
	}
	if err := a.EndSession(wsidStr(t, a, "codex")); err != nil {
		t.Fatalf("end codex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-dual.meta.json")); err != nil {
		t.Fatalf("claude 錄流必須收尾：%v", err)
	}
	// codex 側自 §3.4.4 起沒有 session-scoped 錄流（recordCase 只剩 label，證據
	// 在 connection-wide wire log），因此不得再有 recordings/codex-dual.*
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "codex-dual.meta.json")); !os.IsNotExist(err) {
		t.Fatalf("codex recordCase 應只是 label，不得產生 session 錄流：%v", err)
	}
}

func TestEndOneProviderLeavesOtherActive(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "", "task-x")
	if err := a.EndSession(wsidStr(t, a, "claude")); err != nil {
		t.Fatal(err)
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) {
		t.Fatal("claude must be ended")
	}
	if !a.manager.IsActive(wsidFor(t, a, contract.ProviderCodex)) { // 另一 provider 不受影響
		t.Fatal("codex must stay active")
	}
	if err := a.EndSession(wsidStr(t, a, "codex")); err != nil {
		t.Fatal(err)
	}
}

// TestShutdownForcedWaitsForBoth：分支 (b)——claude 自然收尾 reaper 不搶先
// （用 hookClaudeReaperBeforeEndFlow 卡住它，直到 forcedShutdown 已完整跑
// 完），forced teardown 本身必須正常完成兩邊收尾。reaper 是否搶先本是不受控的
// goroutine 排程競速（review P2：曾在完整 race suite 中量到一次 flaky FAIL）——
// 這裡用既有 test hook 把它釘死在單一分支，不再讓測試結果看排程臉色。
// 分支 (a)（自然收尾先贏、forced 撞見 benign ErrEndInProgress）見下方
// TestShutdownForcedBenignWhenNaturalEndRaces。
func TestShutdownForcedWaitsForBoth(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-fsd", "task-c", ""); err != nil {
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
	if err := a.SendMessage(wsidStr(t, a, "codex"), "long task"); err != nil {
		t.Fatal(err)
	}
	codexHost := a.hostFor(wsidFor(t, a, contract.ProviderCodex))
	if codexHost == nil {
		t.Fatal("precondition: codex session host must be published")
	}
	codexHost.track.NoteStarted([]byte(`{"threadId":"t1","turn":{"id":"turn-busy"}}`))
	if codexHost.runner.ActiveTurnID() == "" {
		t.Fatal("precondition: codex turn must be active")
	}

	// 分支 (b) barrier：卡住自然收尾 reaper，直到 forcedShutdown 整個跑完再放行
	// ——保證這裡驗證的是「forced teardown 自己做完」，不是恰巧贏了排程。
	releaseReaper := make(chan struct{})
	a.hookClaudeReaperBeforeEndFlow = func() { <-releaseReaper }

	if err := a.forcedShutdown(a.snapshotHosts()); err != nil {
		t.Fatalf("forced shutdown: %v", err)
	}
	close(releaseReaper) // 放行 reaper：BeginEndSession 此刻必為 ErrNoSession（session 已被 forced 收乾），no-op

	if !wire.sawMethod(codex.MethodTurnInterrupt) { // busy turn 先被 interrupt
		t.Fatal("forced shutdown must interrupt the active codex turn")
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-fsd.meta.json")); err != nil {
		t.Fatalf("claude lease not finalized: %v", err)
	}
	// codex 側自 §3.4.4 起沒有 session-scoped 錄流（recordCase 只剩 label）
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "codex-fsd.meta.json")); !os.IsNotExist(err) {
		t.Fatalf("codex recordCase 應只是 label，不得產生 session 錄流：%v", err)
	}
	if len(ui.find("session:done")) < 2 { // 兩邊 session:done 都發出
		t.Fatalf("session:done count = %d, want >= 2", len(ui.find("session:done")))
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) || a.manager.IsActive(wsidFor(t, a, contract.ProviderCodex)) {
		t.Fatal("both sessions must be ended")
	}
}

// TestShutdownForcedBenignWhenNaturalEndRaces：分支 (a)——claude 自然收尾
// reaper 搶先取得 BeginEndSession ownership（forcedShutdown 自己的
// sess.Terminate() 就是觸發它甦醒的原因），forcedShutdown 的 EndSessionFlow
// 撞見 ErrEndInProgress 時必須裁定為 benign（見 forcedShutdown doc：review
// P2），只等那份「唯一一次」的 teardown 收斂，不視為 shutdown 錯誤、也不會
// 對同一個 session 重跑第二次 CloseSequence（用 session:done 恰好一次佐證）。
//
// 用四個 test-only hook 把兩條 goroutine 的交錯釘死成確定性序列（不靠
// time.Sleep 猜時序）：
//  1. hookForcedShutdownClaudeBeforeFlow 卡住 forced 的 EndSessionFlow 呼叫，
//     直到已確認 reaper 已經進入 teardown（BeginEndSession 必已成功）。
//  2. hookClaudeTeardownBarrier 卡住 reaper 那份真正的 teardown 執行，直到已
//     確認 forced 也撞見了 ErrEndInProgress benign 分支。
//  3. hookForcedShutdownClaudeBenign 標記 forced 已進入 benign 分支。
func TestShutdownForcedBenignWhenNaturalEndRaces(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-benign", "task-c", ""); err != nil {
		t.Fatal(err)
	}

	teardownStarted := make(chan struct{})
	releaseTeardown := make(chan struct{})
	releaseForced := make(chan struct{})
	benignHit := make(chan struct{})

	a.hookClaudeTeardownBarrier = func() { // reaper 那份真正執行卡在這裡
		close(teardownStarted)
		<-releaseTeardown
	}
	a.hookForcedShutdownClaudeBeforeFlow = func() { <-releaseForced } // forced 卡住，直到 reaper 已握有 ownership
	a.hookForcedShutdownClaudeBenign = func() { close(benignHit) }

	fsResult := make(chan error, 1)
	go func() { fsResult <- a.forcedShutdown(a.snapshotHosts()) }() // sess.Terminate() 先跑，process 死掉喚醒 reaper；隨即卡在 hookForcedShutdownClaudeBeforeFlow

	<-teardownStarted      // reaper 已贏得 BeginEndSession、正卡在真正 teardown 之前
	close(releaseForced)   // 放行 forced：此刻呼叫 EndSessionFlow 必定撞見 ErrEndInProgress（reaper 尚未 FinishEndSession）
	<-benignHit            // 確認 forced 真的走到 benign 分支
	close(releaseTeardown) // 放行 reaper：完成唯一一次真正的 CloseSequence

	if err := <-fsResult; err != nil {
		t.Fatalf("forced shutdown must treat raced ErrEndInProgress as benign, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-benign.meta.json")); err != nil {
		t.Fatalf("lease must still be finalized by the natural-end path: %v", err)
	}
	claudeDone := 0
	for _, e := range ui.find("session:done") {
		if d, ok := e.data.(map[string]any); ok && d["provider"] == "claude" {
			claudeDone++
		}
	}
	if claudeDone != 1 { // shared OnceValue 保證 CloseSequence 只真正跑一次
		t.Fatalf("claude session:done count = %d, want exactly 1 (no double teardown)", claudeDone)
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) {
		t.Fatal("claude session must be ended")
	}
}

// M3b §3.4.4 之後 codex 已無 session-scoped 錄流，錄流寫入失敗只可能發生在
// claude 側——角色對調（claude 帶 recordCase、codex 不帶），驗的不變量相同：
// 一邊的收尾錯誤必須 errors.Join 浮出，且不得跳過另一邊的收尾。
func TestShutdownJoinsErrors(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-joinerr", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	startCodexForTest(t, a, wire, conn, "", "task-x") // codex 無 session 錄流
	// 弄壞 claude meta 寫入
	if err := os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o755) })

	err := a.forcedShutdown(a.snapshotHosts())
	if err == nil { // claude meta 寫失敗必須以 errors.Join 浮出
		t.Fatal("claude lease error must surface")
	}
	waitFor(t, "codex session:done despite claude error", func() bool {
		for _, e := range ui.find("session:done") {
			if d, ok := e.data.(map[string]any); ok && d["provider"] == "codex" {
				return true
			}
		}
		return false
	})
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) || a.manager.IsActive(wsidFor(t, a, contract.ProviderCodex)) {
		t.Fatal("one side's error must not skip the other side's teardown")
	}
}

func TestShutdownHungProviderIsBounded(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "", "task-c", ""); err != nil {
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
	if err := a.SendMessage(wsidStr(t, a, "codex"), "long task"); err != nil {
		t.Fatal(err)
	}
	a.hostFor(wsidFor(t, a, contract.ProviderCodex)).track.NoteStarted(
		[]byte(`{"threadId":"t1","turn":{"id":"turn-hang"}}`))

	start := time.Now()
	_ = a.forcedShutdown(a.snapshotHosts()) // interrupt timeout 屬 best-effort，不影響收尾
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("forced shutdown must be bounded, took %v", elapsed)
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) || a.manager.IsActive(wsidFor(t, a, contract.ProviderCodex)) {
		t.Fatal("hung interrupt must not block either teardown")
	}
}

// TestEndSessionInFlightShutdownNoDoubleTeardown（review P1，fix/lifecycle-app-txn）：
// EndSession 現在整段納入 app transaction（見其 doc）——shutdown 與一個已經在
// 途中的 EndSession 競速時，shutdown 的 inflight.Wait() 必須等 EndSession 完整
// 返回（含 teardown／FinishEndSession）才往下讀 a.claudeSess／進 forcedShutdown。
// 用既有的 hookClaudeTeardownBarrier（claudeTeardown 真正執行的唯一進入點，
// EndSession／NewSession／自然收尾 reaper／forcedShutdown 共用同一個 hook 位置）
// 卡住 EndSession 的 teardown 中段，確定性驅動兩條 goroutine 的交錯，不用
// time.Sleep 猜時序；bounded 斷言沿用既有 TestShutdownGateBlocksLateCodexStart
// 的 select+timeout 慣例（只用來斷言「此刻仍卡著」，不是拿來同步排序本身）。
func TestEndSessionInFlightShutdownNoDoubleTeardown(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-endrace", "task-c", ""); err != nil {
		t.Fatal(err)
	}

	var teardownCount atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookClaudeTeardownBarrier = func() { // claudeTeardown 真正執行的唯一進入點
		teardownCount.Add(1)
		close(entered)
		<-release
	}

	endErr := make(chan error, 1)
	go func() { endErr <- a.EndSession(wsidStr(t, a, "claude")) }()
	<-entered // EndSession 已握有 app-txn、正卡在真正 teardown（CloseSequence）之前

	shutDone := make(chan struct{})
	go func() {
		a.shutdown(context.Background())
		close(shutDone)
	}()
	select { // shutdown 必須等 EndSession 的 app-txn 離場，不能搶先讀 a.claudeSess
	case <-shutDone:
		t.Fatal("shutdown must wait for the in-flight EndSession transaction")
	case <-time.After(150 * time.Millisecond):
	}
	if a.manager.Closed() {
		t.Fatal("manager must not be closed while EndSession's app transaction is still in flight")
	}

	close(release) // 放行：EndSession 完成唯一一次真正的 CloseSequence
	if err := <-endErr; err != nil {
		t.Fatalf("EndSession must succeed: %v", err)
	}
	<-shutDone // 此刻 FinishEndSession／Manager.Close() 皆已跑完，順序見上方 doc

	if n := teardownCount.Load(); n != 1 { // 沒有第二份並行的 CloseSequence
		t.Fatalf("CloseSequence executed %d times, want exactly 1 (no double teardown)", n)
	}
	if !a.manager.Closed() {
		t.Fatal("manager must be closed after shutdown returns")
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) {
		t.Fatal("claude session must be ended")
	}
	claudeDone := 0
	for _, e := range ui.find("session:done") {
		if d, ok := e.data.(map[string]any); ok && d["provider"] == "claude" {
			claudeDone++
		}
	}
	if claudeDone != 1 { // forcedShutdown 對 claude 完全無事可做，不會重複發 session:done
		t.Fatalf("claude session:done count = %d, want exactly 1 (no duplicate)", claudeDone)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-endrace.meta.json")); err != nil {
		t.Fatalf("lease must be finalized: %v", err)
	}
}

// TestNewSessionInFlightShutdownNoDoubleTeardown：NewSession 版的上一個測試——
// NewSession 同樣整段納入 app transaction（見其 doc），teardown 完成後走
// FinishEndSessionIntoReset（不是 FinishEndSession），驗證方式相同。
func TestNewSessionInFlightShutdownNoDoubleTeardown(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-newrace", "task-c", ""); err != nil {
		t.Fatal(err)
	}

	var teardownCount atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookClaudeTeardownBarrier = func() {
		teardownCount.Add(1)
		close(entered)
		<-release
	}

	newErr := make(chan error, 1)
	go func() { newErr <- a.NewSession(wsidStr(t, a, "claude")) }()
	<-entered // NewSession 已握有 app-txn、正卡在真正 teardown（CloseSequence）之前

	shutDone := make(chan struct{})
	go func() {
		a.shutdown(context.Background())
		close(shutDone)
	}()
	select { // shutdown 必須等 NewSession 的 app-txn 離場
	case <-shutDone:
		t.Fatal("shutdown must wait for the in-flight NewSession transaction")
	case <-time.After(150 * time.Millisecond):
	}
	if a.manager.Closed() {
		t.Fatal("manager must not be closed while NewSession's app transaction is still in flight")
	}

	close(release)
	if err := <-newErr; err != nil {
		t.Fatalf("NewSession must succeed: %v", err)
	}
	<-shutDone

	if n := teardownCount.Load(); n != 1 {
		t.Fatalf("CloseSequence executed %d times, want exactly 1 (no double teardown)", n)
	}
	if !a.manager.Closed() {
		t.Fatal("manager must be closed after shutdown returns")
	}
	if a.manager.IsActive(wsidFor(t, a, contract.ProviderClaude)) {
		t.Fatal("claude session must not be active")
	}
	claudeDone := 0
	for _, e := range ui.find("session:done") {
		if d, ok := e.data.(map[string]any); ok && d["provider"] == "claude" {
			claudeDone++
		}
	}
	if claudeDone != 1 {
		t.Fatalf("claude session:done count = %d, want exactly 1 (no duplicate)", claudeDone)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-newrace.meta.json")); err != nil {
		t.Fatalf("lease must be finalized: %v", err)
	}
}

// ---- M1.5-T6：重啟恢復（view window／staged candidate）與 NewSession ----

func TestRestoreViewWindowReplay(t *testing.T) {
	a, _ := newTestApp(t)
	m := a.manager
	// claude 第一個 session：首輪 user envelope 的 session_id 為空（production 形狀）
	id, _ := m.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderClaude), "task-a")
	_ = m.AcceptSubmit(wsidFor(t, a, contract.ProviderClaude), id, "", "hello one")
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindSystemOther, Raw: []byte("{}")}) // 無 ID 雜訊
	_ = m.Emit(wsidFor(t, a, contract.ProviderCodex), contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "x-interleave"})
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindInit, SessionID: "sA", Raw: []byte("{}")})
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
	// End 後第二次 Start：同 view 兩個 session
	if err := appcore.EndSessionFlow(m, wsidFor(t, a, contract.ProviderClaude), nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	id2, _ := m.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderClaude), "task-a")
	_ = m.AcceptSubmit(wsidFor(t, a, contract.ProviderClaude), id2, "sB", "hello two")
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})

	views := a.RestoreViews()
	cl := views["claude"].Envelopes
	if len(cl) == 0 {
		t.Fatal("claude view must replay")
	}
	var kinds []string
	for i, e := range cl {
		if e.Provider != "claude" {
			t.Fatalf("cross-provider leak: %+v", e)
		}
		if i > 0 && cl[i].EventID <= cl[i-1].EventID {
			t.Fatal("replay must be event_id ordered")
		}
		kinds = append(kinds, e.Kind+"/"+e.Role)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "message/user") || !strings.Contains(joined, "system_other/") {
		t.Fatalf("empty-session-id user and no-id noise must be included: %v", kinds)
	}
	// 兩個 session 的 user message 都在（End 不清 view）
	users := 0
	for _, e := range cl {
		if e.Role == "user" {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("both sessions' user messages must replay, got %d", users)
	}
	// codex 交錯事件只出現在 codex view
	for _, e := range views["codex"].Envelopes {
		if e.Provider != "codex" {
			t.Fatalf("codex view leak: %+v", e)
		}
	}
}

// M3b per-WSID writer 之後：view 視窗前移與續聊身分清空都寫進**該 WSID 自己的**
// registry entry，不再是 provider-keyed 的 restore.json。同 provider 的手足因此
// 不受影響（舊實作只能在「不明確」時整段跳過，見 NewSession 的說明）。
func TestNewSessionResetsViewWindow(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeMultiTurnClaude(t, a)
	wc := wsidFor(t, a, contract.ProviderClaude)
	wx := wsidFor(t, a, contract.ProviderCodex)
	registerWSID(t, a, wc, "claude")
	registerWSID(t, a, wx, "codex")
	if err := a.StartSession(string(wc), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.wsReg.CommitResume(string(wc), "sA", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := a.wsReg.CommitResume(string(wx), "tX", "task-x"); err != nil {
		t.Fatal(err)
	}
	if err := a.NewSession(string(wc)); err != nil { // active session：End→IntoReset→reset
		t.Fatal(err)
	}
	got := registryEntryOf(t, a, wc)
	if got.ResumeSessionID != "" {
		t.Fatalf("resume must be cleared: %+v", got)
	}
	if got.ViewStartEventID == "" { // 視窗前進（消費端見 TestNewSessionViewBoundaryHidesOlderTurns）
		t.Fatalf("view window must be reset: %+v", got)
	}
	if other := registryEntryOf(t, a, wx); other.ResumeSessionID != "tX" { // 另一個 session 不受影響
		t.Fatalf("other session entry must be untouched: %+v", other)
	}
	if err := a.NewSession(string(wc)); err != nil { // 無 active session 仍能執行
		t.Fatalf("New without active session: %v", err)
	}
}

func TestNewSessionRestoreWriteFailureKeepsEntry(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := wsidFor(t, a, contract.ProviderClaude)
	registerWSID(t, a, w, "claude")
	if err := a.wsReg.CommitResume(string(w), "sA", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.stateDir, 0o500); err != nil { // temp file 建立失敗
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.stateDir, 0o755) })
	err := a.NewSession(string(w))
	if err == nil {
		t.Fatal("registry write failure must surface (UI must not reset)")
	}
	if got := registryEntryOf(t, a, w); got.ResumeSessionID != "sA" { // entry 回滾不變
		t.Fatalf("entry must be unchanged on failure: %+v", got)
	}
	_ = os.Chmod(a.stateDir, 0o755)
	if _, err := a.manager.BeginNewSessionSubmit(w, "t"); err != nil {
		t.Fatalf("slot must be back to idle after failed reset: %v", err)
	}
}

func TestResumeCandidateStagedThenCommitted(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	registerWSID(t, a, wsidFor(t, a, contract.ProviderCodex), "codex")
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	// stage（EnsureThread 成功）到 Accept 之間：entry 不得出現 thread id
	startCodexForTest(t, a, wire, conn, "", "task-x") // 內部 Accept 成功後 commit
	got := registryEntryOf(t, a, wsidFor(t, a, contract.ProviderCodex))
	if got.ResumeSessionID != "t1" || got.TaskLabel != "task-x" {
		t.Fatalf("commit after Accept: %+v", got)
	}
}

func TestResumeCandidateCommitOnAccept(t *testing.T) { // init before Accept + Accept 失敗
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := wsidFor(t, a, contract.ProviderClaude)
	registerWSID(t, a, w, "claude")
	writeMultiTurnClaude(t, a)
	a.hookAfterProviderStart = func() {
		// pump 已跑（init 可能已暫存於 claudeSessionID）；Accept 前 Close → Accept 失敗
		_ = a.manager.Close()
	}
	err := a.StartSession(string(w), "hi", "", "", "task-a", "")
	if !errors.Is(err, appcore.ErrClosed) {
		t.Fatalf("accept must fail: %v", err)
	}
	if got := registryEntryOf(t, a, w); got.ResumeSessionID != "" || got.TaskLabel != "seed-claude" {
		t.Fatalf("candidate must be discarded on accept failure: %+v", got)
	}
}

func TestEnsureThreadThenStartTurnFailure(t *testing.T) {
	a, _ := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case codex.MethodTurnStart:
			wire.send(map[string]any{"id": *f.ID, "error": map[string]any{"code": -32000, "message": "boom"}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	id, _ := a.manager.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderCodex), "task-x")
	_, _, err := a.startCodexHost(wsidFor(t, a, contract.ProviderCodex), fakeCodexHost{conn}, "hi", "", "", "untrusted")
	if err == nil {
		t.Fatal("StartTurn failure must surface")
	}
	_ = a.manager.RejectSubmit(wsidFor(t, a, contract.ProviderCodex), id)
	if got := a.restore.Get("codex"); got.ResumeSessionID != "" { // 候選丟棄
		t.Fatalf("candidate must be discarded: %+v", got)
	}
}

func TestFreshRestoreInitializesHighWatermark(t *testing.T) {
	a, _ := newTestApp(t)
	m := a.manager
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "old history"})
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
	// 模擬升級：既有 events.jsonl、restore.json 不存在
	if err := os.Remove(a.restore.path); err != nil {
		t.Fatal(err)
	}
	rs, err := openRestoreStore(a.restore.path, auditHighWatermark(a.eventsPath()))
	if err != nil {
		t.Fatal(err)
	}
	a.restore = rs
	if got := a.RestoreViews()["claude"].Envelopes; len(got) != 0 { // 歷史不當 view 重放
		t.Fatalf("fresh store must not replay history, got %d envelopes", len(got))
	}
}

func TestRestoreStoreConcurrentWrites(t *testing.T) { // barrier：兩筆 entry 都保留
	a, _ := newTestApp(t)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			if i == 0 {
				_ = a.restore.CommitResume("claude", "sA", "task-a")
			} else {
				_ = a.restore.CommitResume("codex", "tX", "task-x")
			}
		}(i)
	}
	close(begin)
	wg.Wait()
	rs, err := openRestoreStore(a.restore.path, "") // 重讀檔案驗 durable
	if err != nil {
		t.Fatal(err)
	}
	if rs.Get("claude").ResumeSessionID != "sA" || rs.Get("codex").ResumeSessionID != "tX" {
		t.Fatalf("both entries must survive concurrent writes: %+v / %+v",
			rs.Get("claude"), rs.Get("codex"))
	}
}

func TestRestoreToleratesMalformedTail(t *testing.T) {
	a, _ := newTestApp(t)
	m := a.manager
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "good"})
	// events.jsonl 加壞行：重放跳過該行、不中斷
	f, err := os.OpenFile(a.eventsPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{malformed trailing\n")
	_ = f.Close()
	if got := replayViewWindow(a.eventsPath(), "claude", ""); len(got) == 0 {
		t.Fatal("malformed tail must not break replay")
	}
	// restore.json 壞檔：重建、不讓全部恢復失敗（fail loud 由回傳 error 承載）
	if err := os.WriteFile(a.restore.path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, rerr := openRestoreStore(a.restore.path, "HW")
	if rerr == nil {
		t.Fatal("malformed restore.json must be reported loudly")
	}
	if rs == nil || rs.Get("claude").ViewStartEventID != "HW" { // 重建於 high-watermark
		t.Fatalf("store must be rebuilt: %+v", rs)
	}
}

// TestRestoreExcludesAssistEventsFromProviderView guards §5.1: on restart, an
// isolated SpecAssist event (scope=session, provider=claude, purpose=spec_assist)
// shares the provider but must NOT be bucketed into that provider's view window,
// or its delta/message would leak into the provider Chat and inflate totals.
func TestRestoreExcludesAssistEventsFromProviderView(t *testing.T) {
	a, _ := newTestApp(t)
	m := a.manager
	// 正常 provider session 訊息（走 Wrap → scope 空、purpose 空）——應被重放。
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindMessage,
		Raw: []byte("{}"), Text: "hello-from-provider"})
	// 隔離 SpecAssist 事件（帶文字＋usage）——絕不可進 provider view window。
	m.EmitAssist(contract.ProviderClaude, "corr-assist-1", "spec_assist",
		contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta,
			Raw: []byte("{}"), Text: "assist-draft-leak",
			Usage: &contract.Usage{InputTokens: 111, OutputTokens: 222}})

	got := replayViewWindow(a.eventsPath(), "claude", "")
	var sawProvider, sawAssist bool
	for _, e := range got {
		if e.Purpose == "spec_assist" || e.Text == "assist-draft-leak" {
			sawAssist = true
		}
		if e.Text == "hello-from-provider" {
			sawProvider = true
		}
	}
	if !sawProvider {
		t.Fatal("normal provider message must be replayed into the provider view window")
	}
	if sawAssist {
		t.Fatal("assist event leaked into provider view window (would inflate Chat/totals)")
	}
}

func TestRestoredResumeReachesProvider(t *testing.T) {
	a, _ := newTestApp(t)
	// claude：fake CLI 把 argv 落檔——斷言 --resume 真正進 argv
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(a.stateDir, "claude-argv.txt")
	script := "#!/bin/sh\necho \"$@\" > " + argvFile + "\nread -r _line\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.restore.CommitResume("claude", "resume-id-c", "task-a"); err != nil {
		t.Fatal(err)
	}
	// registry 需有綁定（resume mismatch 檢查）
	cwd, _ := claude.NormalizeCWD(a.workspaceDir)
	_ = a.registry.Bind("resume-id-c", cwd, "")
	restored := a.RestoreViews()["claude"].ResumeSessionID
	if restored != "resume-id-c" {
		t.Fatalf("restored resume id = %q", restored)
	}
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", restored, "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "argv file", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Contains(string(b), "--resume resume-id-c")
	})
	_ = a.EndSession(wsidStr(t, a, "claude"))

	// codex：fake wire 斷言 thread/resume（method + threadId）
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	if err := a.restore.CommitResume("codex", "resume-thread-x", "task-x"); err != nil {
		t.Fatal(err)
	}
	var sawResume atomic.Bool
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadResume:
			if strings.Contains(string(f.Params), "resume-thread-x") {
				sawResume.Store(true)
			}
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "resume-thread-x"}}})
		case codex.MethodTurnStart:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-1", "status": "inProgress"}}})
			wire.send(map[string]any{"method": codex.MethodTurnCompleted,
				"params": map[string]any{"threadId": "resume-thread-x", "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	id, _ := a.manager.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderCodex), "task-x")
	rx := a.RestoreViews()["codex"].ResumeSessionID
	threadID, _, err := a.startCodexHost(wsidFor(t, a, contract.ProviderCodex), fakeCodexHost{conn}, "hi", rx, "", "untrusted")
	if err != nil {
		t.Fatal(err)
	}
	_ = a.manager.AcceptSubmit(wsidFor(t, a, contract.ProviderCodex), id, threadID, "hi")
	if !sawResume.Load() {
		t.Fatal("thread/resume with restored id must reach the wire")
	}
}

func TestNewStartBarrier(t *testing.T) { // teardown 完成與 view reset 之間注入 Start
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := wsidFor(t, a, contract.ProviderClaude)
	registerWSID(t, a, w, "claude")
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(string(w), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	var injected error
	a.hookDuringReset = func() {
		injected = a.StartSession(string(w), "sneak", "", "", "task-sneak", "")
	}
	if err := a.NewSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(injected, appcore.ErrResetInProgress) { // 縫隙內的 Start 必被拒
		t.Fatalf("injected start must be ErrResetInProgress, got %v", injected)
	}
	// reset 完成後開的新 session 的 identity 不被清除
	a.hookDuringReset = nil
	if err := a.StartSession(string(w), "hi again", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	if got := registryEntryOf(t, a, w); got.TaskLabel != "task-b" {
		t.Fatalf("new session identity must survive: %+v", got)
	}
	_ = a.EndSession(string(w))
}

func TestRestoreViewsIsReadOnly(t *testing.T) {
	a, _ := newTestApp(t)
	m := a.manager
	_ = m.Emit(wsidFor(t, a, contract.ProviderClaude), contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	countLines := func() int {
		b, _ := os.ReadFile(a.eventsPath())
		return strings.Count(string(b), "\n")
	}
	before := countLines()
	_ = a.RestoreViews()
	if countLines() != before { // 不回寫 audit
		t.Fatal("RestoreViews must not write to the audit stream")
	}
	if len(a.snapshotHosts()) != 0 { // 零 provider starter 呼叫（兩個 provider 的 ownership 都收在 sessionHosts）
		t.Fatal("RestoreViews must not spawn providers")
	}
}

func TestLateClaudeInitCannotOverwriteNewGeneration(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	oldHost := a.hostFor(wsidFor(t, a, contract.ProviderClaude))
	if err := a.NewSession(wsidStr(t, a, "claude")); err != nil { // 換代：舊 generation 結束
		t.Fatal(err)
	}
	a.commitClaudeResume(oldHost, "stale-session-id") // 舊 pump 的 late init（同一 guard 函式）
	if got := a.restore.Get("claude"); got.ResumeSessionID == "stale-session-id" {
		t.Fatalf("late init from old generation must not overwrite: %+v", got)
	}
}

// ---- M1.5 第三輪 review P1 迴歸 ----

func TestNewSessionTeardownFailureKeepsRestore(t *testing.T) { // P1-2
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w := wsidFor(t, a, contract.ProviderClaude)
	registerWSID(t, a, w, "claude")
	// manager-only session（無 process）：claudeTeardown 必回錯——teardown 失敗形狀
	id, _ := a.manager.BeginNewSessionSubmit(w, "task-a")
	_ = a.manager.AcceptSubmit(w, id, "sA", "hi")
	if err := a.wsReg.CommitResume(string(w), "sA", "task-a"); err != nil {
		t.Fatal(err)
	}
	err := a.NewSession(string(w))
	if err == nil {
		t.Fatal("teardown failure must surface")
	}
	if got := registryEntryOf(t, a, w); got.ResumeSessionID != "sA" { // 續聊身分保留
		t.Fatalf("resume identity must be kept on teardown failure: %+v", got)
	}
	// lifecycle 已收束回 idle：可再開 session（不卡 ending/resetting）
	if _, err := a.manager.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderClaude), "task-b"); err != nil {
		t.Fatalf("slot must be idle after failed New: %v", err)
	}
}

func TestAutoResumeAfterPlainEnd(t *testing.T) { // P1-3：一般 End 後未重啟自動 resume
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	registerWSID(t, a, wsidFor(t, a, contract.ProviderClaude), "claude")
	registerWSID(t, a, wsidFor(t, a, contract.ProviderCodex), "codex")
	// claude：fresh start（init 落 sA）→ End → 再 submit（resume 空）→ argv --resume sA
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(a.stateDir, "argv-auto.txt")
	script := "#!/bin/sh\necho \"$@\" >> " + argvFile + "\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"auto-sA\"}'\n" +
		"while read -r _line; do\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, _ := claude.NormalizeCWD(a.workspaceDir)
	_ = a.registry.Bind("auto-sA", cwd, "")
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resume committed after init", func() bool {
		e, _ := a.wsReg.Get(wsidStr(t, a, "claude"))
		return e.ResumeSessionID == "auto-sA"
	})
	if err := a.EndSession(wsidStr(t, a, "claude")); err != nil { // 一般 End：不清續聊身分
		t.Fatal(err)
	}
	if err := a.StartSession(wsidStr(t, a, "claude"), "again", "", "", "task-a", ""); err != nil { // resume 參數空
		t.Fatal(err)
	}
	waitFor(t, "second start resumes automatically", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Contains(string(b), "--resume auto-sA")
	})
	_ = a.EndSession(wsidStr(t, a, "claude"))

	// codex：t1 committed → End → 再 StartSession（resume 空）→ thread/resume t1
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	var sawResume atomic.Bool
	var turnSeq atomic.Int32
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case codex.MethodThreadResume:
			if strings.Contains(string(f.Params), `"t1"`) {
				sawResume.Store(true)
			}
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
	if err := a.StartSession(wsidStr(t, a, "codex"), "hi", "", "", "task-x", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.EndSession(wsidStr(t, a, "codex")); err != nil {
		t.Fatal(err)
	}
	if err := a.StartSession(wsidStr(t, a, "codex"), "again", "", "", "task-x", ""); err != nil { // resume 參數空
		t.Fatal(err)
	}
	if !sawResume.Load() { // 自動帶 restore 的 t1 → thread/resume
		t.Fatal("plain End must auto-resume the codex thread on next start")
	}
	_ = a.EndSession(wsidStr(t, a, "codex"))
}

// P1-4（plan D6 凍結語意）：durable metadata 寫入失敗 → session 保持 active、
// StartSession 照樣成功，只以 stream_error fail loud。per-WSID writer 之後失敗的
// 是 registry 的 persist，凍結語意不變。
func TestRestoreCommitFailureKeepsSessionActive(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	w := wsidFor(t, a, contract.ProviderClaude)
	// persist 必失敗的真實 Store：目錄先建好讓 Open 成功，之後整個刪掉
	sub := filepath.Join(a.stateDir, "brittle")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := wsregistry.Open(filepath.Join(sub, "workspace-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.wsReg = store
	registerWSID(t, a, w, "claude")
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := a.StartSession(string(w), "hi", "", "", "task-a", ""); err != nil {
		t.Fatalf("StartSession must succeed despite registry write failure: %v", err)
	}
	if !a.manager.IsActive(w) { // session 保持 active
		t.Fatal("session must stay active")
	}
	waitFor(t, "restore failure stream_error", func() bool {
		for _, ev := range ui.find("workbench:event") { // failLoudRestore 直發 UI（不回寫 sink）
			if env, ok := ev.data.(contract.Envelope); ok &&
				env.Kind == string(contract.KindStreamError) && strings.Contains(env.Error, "restore store") {
				return true
			}
		}
		return false
	})
	_ = a.EndSession(wsidStr(t, a, "claude"))
}

func TestRestoreCommitFailureRollsBack(t *testing.T) { // P1-4：失敗變更不得被後續成功寫入夾帶
	a, _ := newTestApp(t)
	goodPath := a.restore.path
	a.restore.path = filepath.Join(a.stateDir, "no-such-dir", "restore.json")
	if err := a.restore.CommitResume("claude", "should-not-survive", "task-bad"); err == nil {
		t.Fatal("commit must fail")
	}
	a.restore.path = goodPath
	if err := a.restore.CommitResume("codex", "tX", "task-x"); err != nil { // 另一 provider 成功寫入
		t.Fatal(err)
	}
	rs, err := openRestoreStore(goodPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Get("claude").ResumeSessionID == "should-not-survive" { // 失敗變更未被持久化
		t.Fatalf("failed commit leaked to disk: %+v", rs.Get("claude"))
	}
	if rs.Get("codex").ResumeSessionID != "tX" {
		t.Fatalf("good commit must persist: %+v", rs.Get("codex"))
	}
}

func TestCodexAcceptFailureReclaimsResources(t *testing.T) { // P1-5
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
			turnID := fmt.Sprintf("turn-%d", turnSeq.Add(1))
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	a.codexHostOverride = fakeCodexHost{conn}
	w := wsidFor(t, a, contract.ProviderCodex)
	a.hookAfterProviderStart = func() { _ = a.manager.Close() } // host（runner/lease）已發布 → Accept 失敗
	err := a.StartSession(wsidStr(t, a, "codex"), "hi", "", "codex-acceptfail", "task-x", "untrusted")
	if !errors.Is(err, appcore.ErrClosed) {
		t.Fatalf("accept must fail with ErrClosed, got %v", err)
	}
	if h := a.hostFor(w); h != nil { // 已發布資源全部回收（host 自 registry 取出）
		t.Fatalf("codex host must be reclaimed: %+v", h)
	}
	if _, ok := a.codexWSIDFor("t1", ""); ok { // 路由一併撤掉
		t.Fatal("codex thread routing must be removed on teardown")
	}
	// §3.4.4：codex 已無 session-scoped 錄流可 finalize，recordCase 只是 label
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "codex-acceptfail.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("recordCase 不得再 attach session recorder：%v", err)
	}
	if len(ui.find("session:done")) == 0 {
		t.Fatal("teardown must emit session:done")
	}
}

// 第四輪 review P1 迴歸：shutdown gate——「BeginNewSessionSubmit 完成、
// ensureAppServer 尚未開始」窗口內啟動 shutdown：shutdown 等待交易離場、
// 晚到的 Start 被 gate 拒、不建立／回填新 server、lease 於 Manager.Close 前 finalize。
func TestShutdownGateBlocksLateCodexStart(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	// claude active session＋錄流：驗 shutdown 序列中 lease 在 Close 前 finalize
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-gate", "task-c", ""); err != nil {
		t.Fatal(err)
	}

	// 晚到的 codex Start：ownership 取得後、ensureAppServer 前停住
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookBeforeProviderStart = func() {
		close(entered)
		<-release
	}
	lateErr := make(chan error, 1)
	go func() {
		lateErr <- a.StartSession(wsidStr(t, a, "codex"), "late", "", "", "task-late", "untrusted")
	}()
	<-entered // 窗口固定：Begin 完成、provider 啟動未開始
	a.hookBeforeProviderStart = nil

	shutDone := make(chan struct{})
	go func() {
		a.shutdown(context.Background())
		close(shutDone)
	}()
	select { // shutdown 必須等待 in-flight 交易（尚未 Close manager）
	case <-shutDone:
		t.Fatal("shutdown must wait for the in-flight start transaction")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := a.manager.BeginSubmit(wsidFor(t, a, contract.ProviderCodex)); errors.Is(err, appcore.ErrClosed) {
		t.Fatal("manager must not be closed while a start transaction is in flight")
	}
	close(release) // 交易離場：ensureAppServer 被 gate 拒
	err := <-lateErr
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("late start must be rejected by the shutdown gate, got %v", err)
	}
	<-shutDone

	if _, ok := a.codexSingle.Take(); ok { // 無新 server 建立／回填
		t.Fatal("no codex server may be (re)filled after shutdown")
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "claude-gate.meta.json")); err != nil {
		t.Fatalf("lease must be finalized before Manager.Close: %v", err)
	}
	if err := a.StartSession(wsidStr(t, a, "codex"), "after", "", "", "t", ""); err == nil ||
		!strings.Contains(err.Error(), "shutting down") { // gate 持續拒新 Start
		t.Fatalf("post-shutdown start must be rejected: %v", err)
	}
}

// 第五輪 review P1 迴歸：非 StartSession 的 server 建立入口（AuthStatus 等經
// ensureAppServer）同樣受 gate 保護——check＋Ensure 對 shutdown 原子，
// 「讀到 false → shutdown Take → 才 Ensure 回填」的 TOCTOU 關閉。
func TestShutdownGateBlocksLateEnsure(t *testing.T) {
	a, _ := newTestApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookInServerTxn = func() { // 交易已登記、Ensure 未開始
		close(entered)
		<-release
	}
	authErr := make(chan error, 1)
	go func() {
		_, err := a.AuthStatus("codex") // 非 StartSession 入口
		authErr <- err
	}()
	<-entered
	a.hookInServerTxn = nil

	shutDone := make(chan struct{})
	go func() {
		a.shutdown(context.Background())
		close(shutDone)
	}()
	select { // shutdown 必須等待 in-flight server 交易
	case <-shutDone:
		t.Fatal("shutdown must wait for the in-flight server transaction")
	case <-time.After(150 * time.Millisecond):
	}
	close(release) // 交易離場（Ensure 會因無 codex binary 失敗——無 server 回填）
	<-authErr
	<-shutDone

	if _, ok := a.codexSingle.Take(); ok { // Take 之後不可能再回填
		t.Fatal("no codex server may be (re)filled after shutdown")
	}
	// shutdown 後所有 server 入口一律被拒
	if _, err := a.AuthStatus("codex"); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("post-shutdown AuthStatus must be rejected: %v", err)
	}
	if err := a.RestartCodexServerRecorded("codex-probe"); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("post-shutdown B1 probe must be rejected: %v", err)
	}
}

// W3 live 抓到的 gap 迴歸：codex approval 的 reason（如 Esc 的 "esc"）必須進
// approval_decision envelope（原 codex resolve callback 丟棄 reason）。
func TestCodexApprovalReasonReachesEnvelope(t *testing.T) {
	a, ui := newTestApp(t)
	// Task 9：approval 一律經 threadId 歸屬到某個 WSID，因此先掛一個 codex host
	// 與它的 thread 路由（等價於 startCodexHost 的 publishCodexHost 那一步）。
	w := wsidFor(t, a, contract.ProviderCodex)
	a.publishCodexHost(&sessionHost{wsid: w, provider: contract.ProviderCodex,
		sockIndex: -1, threadID: "t1"})
	done := make(chan map[string]string, 1)
	go func() {
		done <- a.codexApproval("item/commandExecution/requestApproval",
			[]byte(`{"threadId":"t1","turnId":"turn-1","command":"touch x"}`))
	}()
	var id string
	waitFor(t, "approval:request", func() bool {
		for _, e := range ui.find("approval:request") {
			if d, ok := e.data.(map[string]any); ok {
				id = d["id"].(string)
				return true
			}
		}
		return false
	})
	if err := a.ResolveApproval(id, false, "esc"); err != nil {
		t.Fatal(err)
	}
	<-done
	var got string
	for _, e := range ui.findEnvKind(string(contract.KindApprovalDecision)) {
		if e.Provider == "codex" {
			got = e.Thinking // reason
		}
	}
	if got != "esc" {
		t.Fatalf("codex approval reason must reach envelope, got %q", got)
	}
}

// W6 佐證迴歸：codex 錄流須涵蓋 thread/resume——否則 resume request 在錄流開始前
// 發出、無法以 JSON-RPC 錄流佐證。
//
// M3b §3.4.4 之後承載者換成 connection-wide wire log：錄流由
// codex.NewGenerationOwner 在 handshake **之前**掛上（always-on，不再由
// recordCase 控制 attach），涵蓋面比舊的 session-scoped 錄流更大。
func TestCodexRecordingCapturesThreadResume(t *testing.T) {
	a, _ := newTestApp(t)
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn)
	gen, err := wirelog.NewGeneration(a.wireLogDir(), "codex-resume-rec", a.resolveWireFrameWSID)
	if err != nil {
		t.Fatal(err)
	}
	srv := newFakeServerOn(conn)
	if _, err := codex.NewGenerationOwner(srv, gen); err != nil { // production attach（早於 handshake）
		t.Fatalf("always-on 錄流必須在 handshake 之前掛上：%v", err)
	}
	var sawResume atomic.Bool
	var turnSeq atomic.Int32
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadResume:
			sawResume.Store(true)
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t-resumed"}}})
		case codex.MethodTurnStart:
			turnID := fmt.Sprintf("turn-%d", turnSeq.Add(1))
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
			wire.send(map[string]any{"method": codex.MethodTurnCompleted,
				"params": map[string]any{"threadId": "t-resumed", "turn": map[string]any{"id": turnID, "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	id, _ := a.manager.BeginNewSessionSubmit(wsidFor(t, a, contract.ProviderCodex), "task-x")
	threadID, _, err := a.startCodexHost(wsidFor(t, a, contract.ProviderCodex), srv, "hi", "t-resumed", "codex-resume-rec", "untrusted")
	if err != nil {
		t.Fatal(err)
	}
	_ = a.manager.AcceptSubmit(wsidFor(t, a, contract.ProviderCodex), id, threadID, "hi")
	if !sawResume.Load() {
		t.Fatal("precondition: thread/resume must reach the wire")
	}
	// turn/completed 是 fake server 在 turn/start response **之後**才推的，dispatch
	// 落地與否不在 startCodexHost 的回傳保證內；不等它就 EndSession 會撞
	// 「provider busy」。等的是條件不是時間（repo 慣例，見 waitTurnSettled）。
	waitTurnSettled(t, a, wsidFor(t, a, contract.ProviderCodex))
	if err := a.EndSession(wsidStr(t, a, "codex")); err != nil { // 收尾 flush 錄流
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.wireLogDir(), "codex-resume-rec.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"thread/resume"`) { // wire log 涵蓋 thread/resume
		t.Fatalf("wire log must capture thread/resume, got:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "recordings", "codex-resume-rec.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("recordCase 只是 label，不得再產生 session-scoped 錄流：%v", err)
	}
}
