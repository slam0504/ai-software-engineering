package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type pendingApproval struct {
	provider string
	resolve  func(allow bool, reason string) error
}

// App 是薄綁定層：workspace／CLI 解析、Wails 事件出口與 provider 接線。
// 序列化、coordinator、lifecycle 與錄流收尾全部在 internal/appcore。
type App struct {
	ctx          context.Context
	workspaceDir string
	stateDir     string
	workspaceSrc string
	startupErr   string
	toolsDirPath string
	toolsSource  string
	nodePath     string
	diagramPath  string

	registry *claude.Registry
	manager  *appcore.Manager

	auditMu sync.Mutex
	auditF  *os.File

	mu              sync.Mutex
	activeProv      string
	broker          *approval.Broker
	claudeSess      *claude.Session
	claudeSessionID string
	claudePumpDone  <-chan struct{}
	claudeLease     *appcore.RecordingLease

	codexSingle  codex.Single[*codex.Server]
	runner       *codex.ThreadRunner
	track        appcore.TurnTrack
	codexLease   *appcore.RecordingLease
	codexLoginID string

	apprMu      sync.Mutex
	apprPending map[string]*pendingApproval

	emitUI                 func(name string, data any) // 測試注入；nil = wails runtime
	hookAfterProviderStart func()                      // 測試注入：provider 啟動與 Accept 之間的 barrier
}

// emit：UI 事件唯一出口（wails EventsEmit 的可注入包裝）。
func (a *App) emit(name string, data any) {
	if a.emitUI != nil {
		a.emitUI(name, data)
		return
	}
	runtime.EventsEmit(a.ctx, name, data)
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{apprPending: map[string]*pendingApproval{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	var wsErr error
	a.workspaceDir, a.stateDir, a.workspaceSrc, wsErr = resolveWorkspace()
	if wsErr != nil { // fail loud：UI 與 audit 都要看得到
		a.startupErr = "workspace init failed: " + wsErr.Error()
	}
	if r, rerr := claude.OpenRegistry(filepath.Join(a.stateDir, "sessions.json")); rerr == nil {
		a.registry = r
	} else if a.startupErr == "" {
		a.startupErr = "registry init failed: " + rerr.Error()
	}
	if f, ferr := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
		a.auditF = f
	}
	sink, serr := appcore.NewJSONLSink(filepath.Join(a.stateDir, "events.jsonl"))
	if serr != nil {
		if a.startupErr == "" {
			a.startupErr = "event sink init failed: " + serr.Error()
		}
		sink = nil
	}
	var auditSink appcore.AuditSink = sink
	if sink == nil { // manager 必須存在；sink 失敗已 fail loud 於 startupErr
		auditSink = failedSink{reason: serr}
	}
	a.manager = appcore.New(appcore.Config{
		Sink: auditSink,
		Emit: func(env contract.Envelope) { a.emit("workbench:event", env) },
		// Task 4 live probe VERDICT=per-turn（turn2 output 9 << turn1 642）→ 累加制
		ClaudeUsageCumulative: false,
	})
	a.toolsDirPath, a.toolsSource = resolveToolsDir(a.workspaceDir)
	a.nodePath = resolveNodePath()
	a.audit("startup", map[string]any{"workspace": a.workspaceDir, "workspace_source": a.workspaceSrc,
		"startup_error": a.startupErr, "node_path": a.nodePath,
		"tools_dir":     a.toolsDirPath, "tools_source": a.toolsSource, "node": a.nodeVersion()})
	a.diagramPath = filepath.Join(a.workspaceDir, "docs", "sample.mmd")
	a.watchDiagram(a.diagramPath)
}

// failedSink：events.jsonl 開檔失敗時的替身——每次寫入回同一錯誤，
// Manager 會 latch 並以 stream_error fail loud（不無聲丟稽核）。
type failedSink struct{ reason error }

func (s failedSink) Write(contract.Envelope) error { return s.reason }
func (s failedSink) Close() error                  { return nil }

// ReadDiagram 回傳目前圖檔內容（Mermaid pane 初始載入）。
func (a *App) ReadDiagram() (string, error) {
	b, err := os.ReadFile(a.diagramPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (a *App) watchDiagram(path string) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	_ = w.Add(filepath.Dir(path))
	go func() {
		for ev := range w.Events {
			if ev.Name == path && ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if b, err := os.ReadFile(path); err == nil {
					a.emit("diagram:changed", string(b))
				}
			}
		}
	}()
}

func (a *App) shutdown(ctx context.Context) {
	if err := a.EndSession(); err != nil { // active session 走同一收尾編排
		a.audit("shutdown_end_session_error", map[string]any{"error": err.Error()})
	}
	if a.manager != nil {
		_ = a.manager.Close() // pending queue 由 abort+flush 兜底
	}
	if srv, ok := a.codexSingle.Take(); ok { // 取出即清空 ownership，無後續回填
		_ = srv.Terminate()
		srv.Wait()
	}
	a.mu.Lock()
	br := a.broker
	a.mu.Unlock()
	if br != nil {
		_ = br.Close()
	}
}

// ---- helpers ----

// resolveWorkspace：env WORKBENCH_WORKSPACE → 可寫的 cwd（Finder 啟動時 cwd 是 "/"，
// 不可寫）→ home。第一個能建出 .workbench/recordings 的候選勝出。
func resolveWorkspace() (workspace, state, source string, err error) {
	type cand struct{ dir, src string }
	var cands []cand
	if d := os.Getenv("WORKBENCH_WORKSPACE"); d != "" {
		cands = append(cands, cand{d, "env"})
	}
	if cwd, cerr := os.Getwd(); cerr == nil && cwd != "/" {
		cands = append(cands, cand{cwd, "cwd"})
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		cands = append(cands, cand{home, "home"})
	}
	var lastErr error
	for _, c := range cands {
		n, nerr := claude.NormalizeCWD(c.dir)
		if nerr != nil {
			lastErr = nerr
			continue
		}
		st := filepath.Join(n, ".workbench")
		if merr := os.MkdirAll(filepath.Join(st, "recordings"), 0o755); merr != nil {
			lastErr = merr
			continue
		}
		_ = os.MkdirAll(filepath.Join(st, "probe"), 0o755) // A2/A3 探針落點
		return n, st, c.src, nil
	}
	tmp := os.TempDir()
	st := filepath.Join(tmp, "sdlc-workbench", ".workbench")
	if merr := os.MkdirAll(filepath.Join(st, "recordings"), 0o755); merr != nil {
		return tmp, st, "tmp", errors.Join(lastErr, merr)
	}
	return tmp, st, "tmp", lastErr
}

// resolveToolsDir：env WORKBENCH_TOOLS_DIR → bundle Resources/tools → repo tools/（dev fallback）。
func resolveToolsDir(workspace string) (string, string) {
	if d := os.Getenv("WORKBENCH_TOOLS_DIR"); d != "" {
		return d, "env"
	}
	if exe, err := os.Executable(); err == nil {
		bundle := filepath.Join(filepath.Dir(exe), "..", "Resources", "tools")
		if st, err := os.Stat(bundle); err == nil && st.IsDir() {
			return bundle, "bundle"
		}
	}
	return filepath.Join(workspace, "tools"), "dev-repo"
}

func (a *App) claudeCLIPath() string {
	return filepath.Join(a.toolsDirPath, "claude-cli", "node_modules", ".bin", "claude")
}

func (a *App) codexCLIPath() string {
	return filepath.Join(a.toolsDirPath, "codex-cli", "node_modules", ".bin", "codex")
}

// resolveNodePath：GUI app（Finder 啟動）不繼承 shell PATH，node 常在
// /usr/local/bin 或 /opt/homebrew/bin。codex CLI 是 node script（claude 為
// native binary），找不到 node 時 Codex 線必掛。
func resolveNodePath() string {
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	for _, c := range []string{"/usr/local/bin/node", "/opt/homebrew/bin/node"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// childEnv：把 node 所在目錄前置到子程序 PATH（duplicate PATH 以後者為準）。
func (a *App) childEnv() []string {
	if a.nodePath == "" {
		return nil
	}
	return []string{"PATH=" + filepath.Dir(a.nodePath) + ":" + os.Getenv("PATH")}
}

func (a *App) nodeVersion() string {
	if a.nodePath == "" {
		return "missing (not on app PATH; codex CLI needs node)"
	}
	out, err := exec.Command(a.nodePath, "--version").Output()
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

func (a *App) cliVersion(provider string) string {
	bin := a.claudeCLIPath()
	if provider == "codex" {
		bin = a.codexCLIPath()
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), a.childEnv()...) // codex CLI 是 node script
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (a *App) audit(kind string, v any) {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	if a.auditF == nil {
		return
	}
	rec, _ := json.Marshal(map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind, "data": v})
	fmt.Fprintf(a.auditF, "%s\n", rec)
}

type auditWriter struct{ a *App }

func (w auditWriter) Write(p []byte) (int, error) {
	w.a.auditMu.Lock()
	defer w.a.auditMu.Unlock()
	if w.a.auditF == nil {
		return len(p), nil
	}
	return w.a.auditF.Write(p)
}

func (a *App) auditWriterFor() auditWriter { return auditWriter{a} }

func clientInfo() codex.ClientInfo {
	return codex.ClientInfo{Name: "sdlc-workbench", Version: "0.0.1"}
}

// CLIInfo 回報 CLI 解析路徑與版本（隔離 smoke 的證據面）。
func (a *App) CLIInfo() map[string]string {
	return map[string]string{
		"toolsDir": a.toolsDirPath, "toolsSource": a.toolsSource,
		"claudeVersion": a.cliVersion("claude"), "codexVersion": a.cliVersion("codex"),
		"node": a.nodeVersion(), "workspace": a.workspaceDir,
		"workspaceSource": a.workspaceSrc, "startupError": a.startupErr,
	}
}

// ---- workspace 檔案（canonical 邊界）----

type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // workspace 相對路徑
	IsDir bool   `json:"isDir"`
}

var listExcluded = map[string]bool{".git": true, ".workbench": true, "node_modules": true, "build": true}

// resolveInWorkspace：rel → canonical 絕對路徑；EvalSymlinks 後必須仍在
// workspace root 內（symlink 指外一律擋）。
func (a *App) resolveInWorkspace(rel string) (string, error) {
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		// Clean 會把 /.. 中和成 root——顯式拒絕，不無聲重導
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return resolved, nil
}

func (a *App) ListWorkspace(rel string) ([]FileNode, error) {
	dir, err := a.resolveInWorkspace(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []FileNode
	for _, e := range entries {
		name := e.Name()
		if listExcluded[name] || strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, FileNode{Name: name,
			Path: filepath.Join(filepath.Clean("/"+rel), name)[1:], IsDir: e.IsDir()})
	}
	return out, nil
}

func (a *App) ReadWorkspaceFile(rel string) (string, error) {
	p, err := a.resolveInWorkspace(rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", rel)
	}
	if st.Size() > 1<<20 {
		return "", fmt.Errorf("%q too large (%d bytes > 1MB)", rel, st.Size())
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1)) // Stat 之外的雙保險
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", fmt.Errorf("%q too large", rel)
	}
	return string(b), nil
}

// ---- approvals（雙 provider 共用 UI 流；envelope 一律經 Manager）----

func (a *App) registerApproval(id, provider string, resolve func(bool, string) error) {
	a.apprMu.Lock()
	a.apprPending[id] = &pendingApproval{provider: provider, resolve: resolve}
	a.apprMu.Unlock()
}

func (a *App) ResolveApproval(id string, allow bool, reason string) error {
	a.apprMu.Lock()
	p, ok := a.apprPending[id]
	delete(a.apprPending, id)
	a.apprMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending approval %s (timed out?)", id)
	}
	return p.resolve(allow, reason)
}

func (a *App) pumpApprovals(br *approval.Broker, provider string) {
	for req := range br.Pending() {
		id := req.ID
		a.registerApproval(id, provider, func(allow bool, reason string) error {
			behavior := "deny"
			decision := "deny"
			if allow {
				behavior, decision = "allow", "allow"
			}
			err := br.Resolve(id, approval.Decision{Behavior: behavior, Message: reason})
			a.manager.EmitApprovalDecision(contract.ProviderClaude, a.claudeSessionIDSnapshot(), decision, reason)
			return err
		})
		a.manager.EmitApprovalRequest(contract.ProviderClaude, a.claudeSessionIDSnapshot(), req.ToolName, req.Input)
		a.emit("approval:request", map[string]any{
			"id": id, "provider": provider, "toolName": req.ToolName,
			"inputJson": string(req.Input),
		})
	}
}

func (a *App) claudeSessionIDSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claudeSessionID
}

// ---- session 綁定 ----

// StartSession：單一 ownership 交易——BeginNewSessionSubmit 先佔（輸家在建立任何
// process／recorder／pump 之前就失敗）→ provider 同步啟動 → Accept／Reject。
func (a *App) StartSession(provider, prompt, resume, recordCase, taskLabel, approvalPolicy string) error {
	id, err := a.manager.BeginNewSessionSubmit(taskLabel)
	if err != nil {
		return err // ErrSessionActive／ErrSubmitActive 原樣回 UI
	}
	switch provider {
	case "claude":
		commit, serr := a.startClaude(prompt, resume, recordCase)
		if serr != nil {
			_ = a.manager.RejectSubmit(id)
			return serr
		}
		if h := a.hookAfterProviderStart; h != nil {
			h()
		}
		aerr := a.manager.AcceptSubmit(id, contract.ProviderClaude, "", prompt)
		commit(aerr == nil) // 自然結束 goroutine 據此決定走 EndSessionFlow 或直接清理
		return aerr
	case "codex":
		threadID, alreadyEnded, serr := a.startCodex(prompt, resume, recordCase, approvalPolicy)
		if serr != nil {
			_ = a.manager.RejectSubmit(id)
			return serr
		}
		if err := a.manager.AcceptSubmit(id, contract.ProviderCodex, threadID, prompt); err != nil {
			return err
		}
		_ = alreadyEnded // completed 先到：busy 未設，無需額外收尾
		return nil
	default:
		_ = a.manager.RejectSubmit(id)
		return fmt.Errorf("unknown provider %q", provider)
	}
}

// SendMessage：既有 session 的後續輪（僅 phaseActive 允許；錯誤原樣回 UI）。
func (a *App) SendMessage(prompt string) error {
	a.mu.Lock()
	prov, sess, runner := a.activeProv, a.claudeSess, a.runner
	a.mu.Unlock()
	id, err := a.manager.BeginSubmit()
	if err != nil {
		return err
	}
	switch prov {
	case "claude":
		if sess == nil {
			_ = a.manager.RejectSubmit(id)
			return errors.New("no active claude session")
		}
		if err := sess.Send(prompt); err != nil {
			_ = a.manager.RejectSubmit(id)
			return err
		}
		return a.manager.AcceptSubmit(id, contract.ProviderClaude, a.claudeSessionIDSnapshot(), prompt)
	case "codex":
		if runner == nil {
			_ = a.manager.RejectSubmit(id)
			return errors.New("no active codex thread")
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		if _, _, err := runner.StartTurn(ctx, prompt); err != nil {
			_ = a.manager.RejectSubmit(id)
			return err
		}
		return a.manager.AcceptSubmit(id, contract.ProviderCodex, runner.ThreadID(), prompt)
	default:
		_ = a.manager.RejectSubmit(id)
		return errors.New("no active session")
	}
}

// EndSession：唯一收尾編排（appcore.EndSessionFlow）。冪等；ErrProviderBusy
// 等真實錯誤原樣回 UI（前端「New」須成功後才 reset）。
func (a *App) EndSession() error {
	a.mu.Lock()
	prov := a.activeProv
	sess, done, clease := a.claudeSess, a.claudePumpDone, a.claudeLease
	runner, klease := a.runner, a.codexLease
	a.mu.Unlock()
	switch prov {
	case "claude":
		return appcore.EndSessionFlow(a.manager, nil, a.claudeTeardown(sess, done, clease))
	case "codex":
		busy := func() bool { return runner != nil && runner.ActiveTurnID() != "" }
		return appcore.EndSessionFlow(a.manager, busy, func() error {
			return a.codexTeardown(klease)
		})
	default: // 無 active provider：交由 EndSessionFlow 冪等處理
		return appcore.EndSessionFlow(a.manager, nil, func() error { return nil })
	}
}

func (a *App) TerminateSession() error {
	a.mu.Lock()
	prov, sess := a.activeProv, a.claudeSess
	a.mu.Unlock()
	switch prov {
	case "claude":
		if sess == nil {
			return errors.New("no active claude session")
		}
		return sess.Terminate()
	case "codex": // 長駐 server 不關，只中斷 turn
		params, err := a.track.InterruptParams()
		if err != nil {
			return err
		}
		srv, err := a.currentAppServer()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		_, err = srv.Conn().Call(ctx, codex.MethodTurnInterrupt, params)
		return err
	default:
		return errors.New("no active session")
	}
}

// ---- Claude 線 ----

func approvalTimeout() time.Duration { return approval.BrokerTimeout() }

// startClaude 啟動 provider 並回傳 commit callback：呼叫端於 AcceptSubmit 成敗後
// 以 commit(accepted) 通知自然結束 goroutine——快速退出（auth／參數錯誤）時
// goroutine 會等 start 交易 commit/abort 才收尾，不會在 phase=starting 空轉。
func (a *App) startClaude(prompt, resume, recordCase string) (func(accepted bool), error) {
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	if a.registry == nil {
		return nil, fmt.Errorf("session registry unavailable (startup error: %s)", a.startupErr)
	}
	if resume != "" { // resume mismatch 拒絕
		if bound, ok := a.registry.CWD(resume); !ok || bound != cwd {
			return nil, fmt.Errorf("resume refused: session %s bound to %q, current %q", resume, bound, cwd)
		}
	}
	sock := filepath.Join(a.stateDir, "approval.sock")
	_ = os.Remove(sock)
	a.mu.Lock()
	if a.broker != nil {
		_ = a.broker.Close()
	}
	a.mu.Unlock()
	br, err := approval.NewBroker(sock, approvalTimeout(), a.auditWriterFor())
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.broker = br
	a.mu.Unlock()
	committed := false // 未 commit ownership 的 rollback：後續任何失敗都回收 broker
	defer func() {
		if committed {
			return
		}
		_ = br.Close()
		a.mu.Lock()
		if a.broker == br {
			a.broker = nil
		}
		a.mu.Unlock()
	}()
	br.SetTimeoutHook(func(id string) { // 逾時 deny 後收掉 UI 的過期彈窗
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		a.manager.EmitApprovalDecision(contract.ProviderClaude, a.claudeSessionIDSnapshot(), "timeout", "")
		a.emit("approval:dismiss", map[string]any{"id": id, "cause": "timeout"})
	})
	go a.pumpApprovals(br, "claude")

	self, _ := os.Executable()
	if o := os.Getenv("WORKBENCH_MCP_COMMAND_OVERRIDE"); o != "" { // A6 注入點
		self = o
	}
	mcpCfg := filepath.Join(a.stateDir, "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":%q,"args":["mcp-approval","--socket",%q]}}}`, self, sock)
	if err := os.WriteFile(mcpCfg, []byte(cfg), 0o644); err != nil {
		return nil, err
	}

	var rec *recorder.Recorder
	var lease *appcore.RecordingLease
	if recordCase != "" {
		rec, err = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".ndjson")
		if err != nil { // Recorder 初始化失敗 = 可見的 session 失敗，不無聲降級
			return nil, err
		}
	}

	sess, err := claude.Start(a.ctx, claude.Config{
		Binary: a.claudeCLIPath(), CWD: cwd, Prompt: prompt, Resume: resume,
		MCPConfigPath: mcpCfg, PermissionPromptTool: "mcp__workbench__approval_prompt",
		// ask 優先於 allow、defaultMode 蓋掉使用者環境的 plan 預設
		SettingsJSON: `{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}`,
		Env:          a.childEnv(),
		MultiTurn:    true, // M1：stdin 保持開啟，SendMessage 逐輪送 user message
	})
	if err != nil {
		if rec != nil {
			_ = rec.CloseWith(recorder.Meta{Provider: "claude", RecordedAt: time.Now().UTC().Format(time.RFC3339)})
		}
		return nil, err
	}
	if rec != nil {
		lease = appcore.NewRecordingLease(rec, func() error { return nil },
			func(ex ports.Exit) recorder.Meta {
				m := recorder.Meta{Provider: "claude", CLIVersion: a.cliVersion("claude"),
					Argv: sess.Argv(), CWD: cwd,
					RecordedAt: time.Now().UTC().Format(time.RFC3339), StderrTail: ex.StderrTail}
				if ex.Exited { // 未知結局不偽裝 exit code（meta ExitCode 維持 nil）
					code := ex.Code
					m.ExitCode = &code
				}
				return m
			})
	}

	// pump：錄流 tap ＋ init 綁定 registry → 一律經 Manager.Emit
	done := appcore.Pump(sess.Events(), func(ev contract.Event) {
		if rec != nil {
			if lerr := rec.Line(ev.Raw); lerr != nil {
				a.manager.Emit(contract.Event{Provider: contract.ProviderClaude,
					Kind: contract.KindStreamError, Raw: []byte(lerr.Error()), Err: lerr})
			}
		}
		if info := claude.ParseInit(ev); info != nil {
			_ = a.registry.Bind(info.SessionID, cwd)
			a.mu.Lock()
			a.claudeSessionID = info.SessionID
			a.mu.Unlock()
		}
		a.manager.Emit(ev)
	})

	a.mu.Lock()
	a.claudeSess, a.claudePumpDone, a.claudeLease = sess, done, lease
	a.claudeSessionID, a.activeProv = "", "claude"
	a.mu.Unlock()

	commitCh := make(chan bool, 1)
	go func() { // reaper：先等 start 交易結果，再決定收尾路徑
		accepted := <-commitCh
		if !accepted {
			// 交易 abort：MultiTurn CLI 可能仍在等下一輪輸入（done 不會自己關），
			// 不能等 EOF——立即 teardown（CloseSequence 關 stdin → 界限內收乾）。
			if err := a.claudeTeardown(sess, done, lease)(); err != nil {
				a.audit("claude_aborted_start_cleanup_error", map[string]any{"error": err.Error()})
			}
			return
		}
		<-done // committed：等自然結束／崩潰（pump 收乾）再走同一收尾編排
		a.mu.Lock()
		current := a.claudeSess == sess
		a.mu.Unlock()
		if !current { // EndSession 已接手
			return
		}
		if err := appcore.EndSessionFlow(a.manager, nil, a.claudeTeardown(sess, done, lease)); err != nil {
			a.audit("claude_natural_end_error", map[string]any{"error": err.Error()})
		}
	}()
	committed = true
	return func(accepted bool) { commitCh <- accepted }, nil
}

// claudeTeardown：CloseSequence（close → quiesce → 必要時 terminate → Wait →
// lease.Finalize(ex)），並發 session:done（Exit 為證據）。
func (a *App) claudeTeardown(sess *claude.Session, done <-chan struct{},
	lease *appcore.RecordingLease) func() error {
	return func() error {
		if sess == nil {
			return errors.New("no active claude session")
		}
		fin := func(ex ports.Exit) error {
			if lease != nil {
				return lease.Finalize(ex)
			}
			return nil
		}
		ex, err := appcore.CloseSequence(sess.Close, done, 5*time.Second, 10*time.Second,
			sess.Terminate, sess.Wait, fin)
		a.mu.Lock()
		if a.claudeSess == sess {
			a.claudeSess, a.claudePumpDone, a.claudeLease = nil, nil, nil
			a.activeProv = ""
		}
		br := a.broker
		a.broker = nil
		a.mu.Unlock()
		if br != nil {
			_ = br.Close()
		}
		var recErrText string
		if err != nil {
			recErrText = err.Error()
		}
		payload := map[string]any{"provider": "claude", "stderrTail": ex.StderrTail,
			"recorderError": recErrText}
		if ex.Exited {
			payload["exitCode"] = ex.Code
		}
		a.emit("session:done", payload)
		return err
	}
}

// ---- Codex 線 ----

func (a *App) ensureAppServer() (*codex.Server, error) {
	return a.codexSingle.Ensure(func() (*codex.Server, error) {
		srv, err := codex.StartAppServer(a.ctx, codex.Config{Binary: a.codexCLIPath(),
			CWD: a.workspaceDir, Env: a.childEnv()})
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		if err := srv.Handshake(hctx, clientInfo()); err != nil { // start 失敗不保留 instance（Ensure 契約）
			_ = srv.Terminate()
			srv.Wait()
			return nil, err
		}
		a.wireCodexConn(srv.Conn())
		return srv, nil
	})
}

// currentAppServer 取得既有長駐 server（不重建）。
func (a *App) currentAppServer() (*codex.Server, error) {
	return a.codexSingle.Ensure(func() (*codex.Server, error) {
		return nil, errors.New("codex app-server not running")
	})
}

func (a *App) currentRunner() *codex.ThreadRunner {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runner
}

func (a *App) wireCodexConn(conn *codex.Conn) {
	conn.OnNotification(func(method string, params json.RawMessage) {
		switch method {
		case codex.MethodAccountLoginCompleted, codex.MethodAccountUpdated:
			a.emit("auth:status", map[string]any{"provider": "codex",
				"event": method, "payload": string(params)})
			a.audit("codex_auth", map[string]any{"method": method, "params": json.RawMessage(params)})
		case codex.MethodTurnStarted:
			a.track.NoteStarted(params) // TerminateSession 需要 turnId
			a.manager.Emit(codex.MapEvent(method, params))
		case codex.MethodTurnCompleted:
			a.track.NoteEnded()
			_, turnID := appcore.ParseTurnStarted(params) // 同 schema：turn.id
			if r := a.currentRunner(); r != nil {
				r.NoteTurnEnded(turnID) // 解 busy；不動 recorder（session-scoped 錄流）
			}
			a.manager.Emit(codex.MapEvent(method, params))
		default:
			a.manager.Emit(codex.MapEvent(method, params))
		}
	})
	conn.OnUnknown(func(raw []byte) {
		a.manager.Emit(contract.Event{Provider: contract.ProviderCodex,
			Kind: contract.KindUnknown, Raw: append([]byte(nil), raw...)})
	})
	conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case codex.MethodCmdExecRequestApproval, codex.MethodFileChangeRequestApproval:
			return a.codexApproval(method, params), nil
		default:
			return nil, fmt.Errorf("unsupported server request %s", method)
		}
	})
}

// codexApproval：核可請求 → 同一 ApprovalDialog → allow=accept / deny=decline；逾時 decline（fail closed）。
func (a *App) codexApproval(method string, params json.RawMessage) map[string]string {
	id := fmt.Sprintf("codex-%d", time.Now().UnixNano())
	threadID := ""
	if r := a.currentRunner(); r != nil {
		threadID = r.ThreadID()
	}
	ch := make(chan bool, 1)
	a.registerApproval(id, "codex", func(allow bool, reason string) error {
		ch <- allow
		return nil
	})
	a.audit("codex_approval_request", map[string]any{"id": id, "method": method, "raw_params": json.RawMessage(params)})
	a.manager.EmitApprovalRequest(contract.ProviderCodex, threadID, method, params)
	a.emit("approval:request", map[string]any{
		"id": id, "provider": "codex", "toolName": method, "inputJson": string(params)})
	decision := "decline"
	uiDecision := "deny"
	select {
	case allow := <-ch:
		if allow {
			decision, uiDecision = "accept", "allow"
		}
	case <-time.After(approvalTimeout()):
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		uiDecision = "timeout"
		a.audit("codex_approval_timeout", map[string]any{"id": id})
		a.emit("approval:dismiss", map[string]any{"id": id, "cause": "timeout"})
	}
	a.manager.EmitApprovalDecision(contract.ProviderCodex, threadID, uiDecision, "")
	a.audit("codex_approval_decision", map[string]any{"id": id, "decision": decision})
	return map[string]string{"decision": decision}
}

// codexHost：startCodexHost 對長駐 server 的最小依賴（fake wire 測試注入點）。
type codexHost interface {
	Conn() *codex.Conn
	Argv() []string
	StderrSnapshot() string
}

func (a *App) startCodex(prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	srv, err := a.ensureAppServer()
	if err != nil {
		return "", false, err
	}
	return a.startCodexHost(srv, prompt, resume, recordCase, approvalPolicy)
}

// startCodexHost：EnsureThread＋StartTurn bounded synchronous（ctx 30s；turn/start
// response 立即回）。回傳 threadID 供 AcceptSubmit。
//
// runner 於 EnsureThread 成功後、StartTurn 前發布至 a.runner——notification
// handler（turn/completed→NoteTurnEnded、approval→ThreadID）在首輪 response
// 尚未消化時就找得到 runner；completed-before-response 由 earlyEnded latch 對消。
// 後續任何失敗原子 rollback（a.runner 清回 nil）。
func (a *App) startCodexHost(host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	conn := host.Conn()
	if approvalPolicy == "" { // M0 驗證定位沿用：commandExecution 一律 requestApproval
		approvalPolicy = "untrusted"
	}
	runner := codex.NewThreadRunner(conn)
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	threadID, err := runner.EnsureThread(ctx, resume, approvalPolicy)
	if err != nil {
		return "", false, err
	}

	a.mu.Lock()
	a.runner = runner // 發布：首輪事件的 handler ownership
	a.mu.Unlock()
	rollback := func() {
		a.mu.Lock()
		if a.runner == runner {
			a.runner = nil
		}
		a.mu.Unlock()
	}

	var lease *appcore.RecordingLease
	if recordCase != "" {
		rec, rerr := recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".jsonl")
		if rerr != nil { // 可見的 session 失敗，不無聲降級
			rollback()
			return "", false, rerr
		}
		if berr := conn.BeginRecording(rec.Line); berr != nil {
			cerr := rec.CloseWith(recorder.Meta{Provider: "codex", RecordedAt: time.Now().UTC().Format(time.RFC3339)})
			rollback()
			return "", false, errors.Join(berr, cerr)
		}
		lease = appcore.NewRecordingLease(rec, conn.StopRecording,
			func(ports.Exit) recorder.Meta { // 長駐 server 不隨 session 退出：live snapshot、ExitCode nil
				return recorder.Meta{Provider: "codex", CLIVersion: a.cliVersion("codex"),
					Argv: host.Argv(), CWD: a.workspaceDir,
					RecordedAt:          time.Now().UTC().Format(time.RFC3339),
					ProcessStillRunning: true, StderrTail: host.StderrSnapshot()}
			})
	}

	_, alreadyEnded, err := runner.StartTurn(ctx, prompt)
	if err != nil {
		if lease != nil {
			_ = lease.Finalize(ports.Exit{Exited: false})
		}
		rollback()
		return "", false, err
	}

	a.mu.Lock()
	a.codexLease, a.activeProv = lease, "codex"
	a.mu.Unlock()

	if lease != nil { // fatal：wire EOF（server 死亡）時仍收尾錄流（冪等由 lease 保證）
		go func() {
			<-conn.Done()
			_ = lease.Finalize(ports.Exit{Exited: false})
		}()
	}
	return threadID, alreadyEnded, nil
}

// codexTeardown：長駐 server 不關；lease.Finalize(Exited=false) 收錄流，
// 清 runner／track 並發 session:done。
func (a *App) codexTeardown(lease *appcore.RecordingLease) error {
	var err error
	if lease != nil {
		err = lease.Finalize(ports.Exit{Exited: false})
	}
	a.mu.Lock()
	a.runner, a.codexLease = nil, nil
	if a.activeProv == "codex" {
		a.activeProv = ""
	}
	a.mu.Unlock()
	a.track.NoteEnded()
	stderr := ""
	if srv, serr := a.currentAppServer(); serr == nil {
		stderr = srv.StderrSnapshot()
	}
	var recErrText string
	if err != nil {
		recErrText = err.Error()
	}
	a.emit("session:done", map[string]any{"provider": "codex",
		"processStillRunning": true, "stderrTail": stderr, "recorderError": recErrText})
	return err
}

// RestartCodexServerRecorded：B1 受控重啟 probe（薄封裝 codex.RunHandshakeProbe，
// 生命週期 Begin → Handshake → Stop → CloseWith 與四階段失敗處置在 M0 Task 8 以測試固定）。
func (a *App) RestartCodexServerRecorded(recordCase string) error {
	newRec := func() (*recorder.Recorder, error) {
		return recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".jsonl")
	}
	start := func() (*codex.Server, error) {
		return codex.StartAppServer(a.ctx, codex.Config{Binary: a.codexCLIPath(),
			CWD: a.workspaceDir, Env: a.childEnv()})
	}
	err := codex.RunHandshakeProbe(a.ctx, &a.codexSingle, newRec, start, clientInfo())
	if err != nil {
		a.audit("codex_probe_failed", map[string]any{"case": recordCase, "error": err.Error()})
		return err
	}
	srv, serr := a.currentAppServer() // probe 成功必有長駐 server；接上 handlers
	if serr != nil {
		return serr
	}
	a.wireCodexConn(srv.Conn())
	a.audit("codex_probe_ok", map[string]any{"case": recordCase})
	return nil
}

// ---- 官方登入（app 不收密碼、不保管 token）----

func (a *App) AuthStatus(provider string) (string, error) {
	switch provider {
	case "claude":
		out, err := exec.Command(a.claudeCLIPath(), "auth", "status").CombinedOutput()
		return strings.TrimSpace(string(out)), err
	case "codex":
		srv, err := a.ensureAppServer()
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		res, err := srv.Conn().Call(ctx, codex.MethodAccountRead, map[string]any{})
		if err != nil {
			return "", err
		}
		return string(res), nil
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
}

func (a *App) StartLogin(provider string) error {
	switch provider {
	case "codex":
		srv, err := a.ensureAppServer() // 登入與 session 重用同一長駐 server，登入後不重啟
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		res, err := srv.Conn().Call(ctx, codex.MethodAccountLoginStart, map[string]any{"type": "chatgpt"})
		if err != nil {
			return err
		}
		var lr struct {
			LoginID string `json:"loginId"`
			AuthURL string `json:"authUrl"`
		}
		_ = json.Unmarshal(res, &lr)
		a.mu.Lock()
		a.codexLoginID = lr.LoginID
		a.mu.Unlock()
		if lr.AuthURL != "" {
			runtime.BrowserOpenURL(a.ctx, lr.AuthURL)
		}
		a.emit("auth:status", map[string]any{"provider": "codex",
			"event": "login_started", "authUrl": lr.AuthURL})
		return nil
	case "claude":
		// 官方命令 claude auth login 為互動式（fixture claude-auth-help.txt）：
		// 系統終端機 fallback + 每 5s 輪詢 auth status、5 分鐘逾時。
		script := fmt.Sprintf("tell application \"Terminal\" to do script %q",
			a.claudeCLIPath()+" auth login")
		if err := exec.Command("osascript", "-e", "tell application \"Terminal\" to activate").Run(); err != nil {
			return fmt.Errorf("open terminal: %w", err)
		}
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			return fmt.Errorf("launch login in terminal: %w", err)
		}
		a.emit("auth:status", map[string]any{"provider": "claude", "event": "terminal_opened"})
		go a.pollClaudeAuth()
		return nil
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

// CancelLogin 取消進行中的 codex 官方登入（account/login/cancel，schema 必填 loginId）。
func (a *App) CancelLogin(provider string) error {
	if provider != "codex" {
		return fmt.Errorf("cancel login not supported for %q (claude login runs in the system terminal)", provider)
	}
	a.mu.Lock()
	loginID := a.codexLoginID
	a.mu.Unlock()
	if loginID == "" {
		return errors.New("no login in progress")
	}
	srv, err := a.currentAppServer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if _, err := srv.Conn().Call(ctx, codex.MethodAccountLoginCancel, map[string]any{"loginId": loginID}); err != nil {
		return err
	}
	a.mu.Lock()
	a.codexLoginID = ""
	a.mu.Unlock()
	a.emit("auth:status", map[string]any{"provider": "codex", "event": "login_cancelled"})
	return nil
}

func (a *App) pollClaudeAuth() {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		out, err := exec.Command(a.claudeCLIPath(), "auth", "status").Output()
		if err == nil {
			var st struct {
				LoggedIn bool `json:"loggedIn"`
			}
			if json.Unmarshal(out, &st) == nil && st.LoggedIn {
				a.emit("auth:status", map[string]any{"provider": "claude", "event": "logged_in"})
				return
			}
		}
	}
	a.emit("auth:status", map[string]any{"provider": "claude", "event": "login_pending_timeout"})
}

func (a *App) Logout(provider string) error {
	switch provider {
	case "claude":
		out, err := exec.Command(a.claudeCLIPath(), "auth", "logout").CombinedOutput()
		a.emit("auth:status", map[string]any{"provider": "claude",
			"event": "logged_out", "detail": strings.TrimSpace(string(out))})
		return err
	case "codex":
		srv, err := a.ensureAppServer()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		_, err = srv.Conn().Call(ctx, codex.MethodAccountLogout, map[string]any{})
		return err // account/updated 通知會轉 auth:status
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}
