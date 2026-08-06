package main

// spike quality: to be rebuilt in M1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type pendingApproval struct {
	provider string
	resolve  func(allow bool, reason string) error
}

// App struct
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

	auditMu sync.Mutex
	auditF  *os.File

	mu         sync.Mutex
	claudeSess *claude.Session
	broker     *approval.Broker
	activeProv string

	codexSingle   codex.Single[*codex.Server]
	codexThreadID string
	codexTurnID   string // turn/interrupt 必填（schema：threadId + turnId）
	codexRec      *recorder.Recorder
	codexActive   bool
	codexLoginID  string

	apprMu      sync.Mutex
	apprPending map[string]*pendingApproval
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
	a.toolsDirPath, a.toolsSource = resolveToolsDir(a.workspaceDir)
	a.nodePath = resolveNodePath()
	a.audit("startup", map[string]any{"workspace": a.workspaceDir, "workspace_source": a.workspaceSrc,
		"startup_error": a.startupErr, "node_path": a.nodePath,
		"tools_dir":     a.toolsDirPath, "tools_source": a.toolsSource, "node": a.nodeVersion()})
	a.diagramPath = filepath.Join(a.workspaceDir, "docs", "sample.mmd")
	a.watchDiagram(a.diagramPath)
}

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
					runtime.EventsEmit(a.ctx, "diagram:changed", string(b))
				}
			}
		}
	}()
}

func (a *App) shutdown(ctx context.Context) {
	if srv, ok := a.codexSingle.Take(); ok { // 取出即清空 ownership，無後續回填
		_ = srv.Terminate()
		srv.Wait()
	}
	a.mu.Lock()
	sess, br := a.claudeSess, a.broker
	a.mu.Unlock()
	if sess != nil {
		_ = sess.Terminate()
		sess.Wait()
	}
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

func toUIEvent(ev contract.Event) map[string]any {
	m := map[string]any{"provider": string(ev.Provider), "kind": string(ev.Kind),
		"sessionId": ev.SessionID, "text": ev.Text, "thinking": ev.Thinking,
		"isError": ev.IsError, "costUsd": ev.CostUSD, "raw": string(ev.Raw)}
	if ev.Err != nil {
		m["error"] = ev.Err.Error()
	}
	return m
}

// CLIInfo 回報 CLI 解析路徑與版本（Task 12 隔離 smoke 的證據面）。
func (a *App) CLIInfo() map[string]string {
	return map[string]string{
		"toolsDir": a.toolsDirPath, "toolsSource": a.toolsSource,
		"claudeVersion": a.cliVersion("claude"), "codexVersion": a.cliVersion("codex"),
		"node": a.nodeVersion(), "workspace": a.workspaceDir,
		"workspaceSource": a.workspaceSrc, "startupError": a.startupErr,
	}
}

// ---- approvals（雙 provider 共用 UI 流）----

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
			if allow {
				behavior = "allow"
			}
			return br.Resolve(id, approval.Decision{Behavior: behavior, Message: reason})
		})
		runtime.EventsEmit(a.ctx, "approval:request", map[string]any{
			"id": id, "provider": provider, "toolName": req.ToolName,
			"inputJson": string(req.Input),
		})
	}
}

// ---- session 綁定 ----

func (a *App) StartSession(provider, prompt, resume, recordCase string) error {
	switch provider {
	case "claude":
		return a.startClaude(prompt, resume, recordCase)
	case "codex":
		return a.startCodex(prompt, resume, recordCase)
	default:
		return fmt.Errorf("unknown provider %q", provider)
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
	case "codex": // 長駐 server 不關，只中斷 turn（v1.5）
		params, err := a.codexInterruptParams()
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

// ---- codex turn 追蹤（turn/interrupt 的 schema 必填 threadId + turnId）----

func parseTurnStarted(params []byte) (threadID, turnID string) {
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	return p.ThreadID, p.Turn.ID
}

func (a *App) noteTurnStarted(params []byte) {
	th, tu := parseTurnStarted(params)
	a.mu.Lock()
	if th != "" {
		a.codexThreadID = th
	}
	a.codexTurnID = tu
	a.mu.Unlock()
}

func (a *App) noteTurnEnded() {
	a.mu.Lock()
	a.codexTurnID = ""
	a.mu.Unlock()
}

func (a *App) codexInterruptParams() (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.codexThreadID == "" || a.codexTurnID == "" {
		return nil, fmt.Errorf("no active codex turn (threadId=%q, turnId=%q)", a.codexThreadID, a.codexTurnID)
	}
	return map[string]any{"threadId": a.codexThreadID, "turnId": a.codexTurnID}, nil
}

// ---- Claude 線 ----

func approvalTimeout() time.Duration { return approval.BrokerTimeout() }

func (a *App) startClaude(prompt, resume, recordCase string) error {
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return err
	}
	if a.registry == nil {
		return fmt.Errorf("session registry unavailable (startup error: %s)", a.startupErr)
	}
	if resume != "" { // resume mismatch 拒絕
		if bound, ok := a.registry.CWD(resume); !ok || bound != cwd {
			return fmt.Errorf("resume refused: session %s bound to %q, current %q", resume, bound, cwd)
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
		return err
	}
	a.mu.Lock()
	a.broker = br
	a.mu.Unlock()
	go a.pumpApprovals(br, "claude")

	self, _ := os.Executable()
	if o := os.Getenv("WORKBENCH_MCP_COMMAND_OVERRIDE"); o != "" { // A6 注入點
		self = o
	}
	mcpCfg := filepath.Join(a.stateDir, "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":%q,"args":["mcp-approval","--socket",%q]}}}`, self, sock)
	if err := os.WriteFile(mcpCfg, []byte(cfg), 0o644); err != nil {
		return err
	}
	sess, err := claude.Start(a.ctx, claude.Config{
		Binary: a.claudeCLIPath(), CWD: cwd, Prompt: prompt, Resume: resume,
		MCPConfigPath: mcpCfg, PermissionPromptTool: "mcp__workbench__approval_prompt",
		// probe 必問：ask 優先於 allow、所有 mode 有效；defaultMode 蓋掉使用者
		// 環境的 plan 預設（實測 permissionMode:plan 會擋掉全部寫入、A2 無法執行）
		SettingsJSON: `{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}`,
		Env:          a.childEnv(),
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.claudeSess, a.activeProv = sess, "claude"
	a.mu.Unlock()
	go a.pumpClaude(sess, cwd, recordCase)
	return nil
}

func (a *App) pumpClaude(sess *claude.Session, cwd, recordCase string) {
	var rec *recorder.Recorder
	if recordCase != "" {
		var recErr error
		rec, recErr = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".ndjson")
		if recErr != nil { // v1.4：Recorder 初始化失敗 = 可見的 session 失敗，不無聲降級
			_ = sess.Terminate()
			sess.Wait()
			runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "claude", "recorderError": recErr.Error()})
			return
		}
	}
	for ev := range sess.Events() {
		if rec != nil {
			if err := rec.Line(ev.Raw); err != nil {
				runtime.EventsEmit(a.ctx, "bridge:event", map[string]any{"kind": "recorder_error", "error": err.Error()})
			}
		}
		if info := claude.ParseInit(ev); info != nil {
			_ = a.registry.Bind(info.SessionID, cwd)
		}
		runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(ev))
	}
	ex := sess.Wait()
	var recErrText string
	if rec != nil { // v1.4：CloseWith（含底層 close 與 meta 寫入）錯誤進 session:done
		if err := rec.CloseWith(recorder.Meta{Provider: "claude", CLIVersion: a.cliVersion("claude"),
			Argv: sess.Argv(), CWD: cwd, RecordedAt: time.Now().UTC().Format(time.RFC3339),
			ExitCode: &ex.Code, StderrTail: ex.StderrTail}); err != nil { // v1.7：*int，claude 回合必已退出
			recErrText = err.Error()
		}
	}
	a.mu.Lock()
	a.claudeSess = nil
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "claude",
		"exitCode": ex.Code, "stderrTail": ex.StderrTail, "recorderError": recErrText})
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
		a.wireCodexHandlers(srv)
		return srv, nil
	})
}

// currentAppServer 取得既有長駐 server（不重建）。
func (a *App) currentAppServer() (*codex.Server, error) {
	return a.codexSingle.Ensure(func() (*codex.Server, error) {
		return nil, errors.New("codex app-server not running")
	})
}

func (a *App) wireCodexHandlers(srv *codex.Server) {
	conn := srv.Conn()
	conn.OnNotification(func(method string, params json.RawMessage) {
		switch method {
		case codex.MethodAccountLoginCompleted, codex.MethodAccountUpdated:
			runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "codex",
				"event": method, "payload": string(params)})
			a.audit("codex_auth", map[string]any{"method": method, "params": json.RawMessage(params)})
		case codex.MethodTurnStarted:
			a.noteTurnStarted(params) // Terminate 需要 turnId
			runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(codex.MapEvent(method, params)))
		case codex.MethodTurnCompleted:
			a.noteTurnEnded()
			runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(codex.MapEvent(method, params)))
			a.finishCodexTurn(srv, params)
		default:
			runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(codex.MapEvent(method, params)))
		}
	})
	conn.OnUnknown(func(raw []byte) {
		ev := contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUnknown, Raw: append([]byte(nil), raw...)}
		runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(ev))
	})
	conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case codex.MethodCmdExecRequestApproval, codex.MethodFileChangeRequestApproval:
			return a.codexApproval(method, params), nil
		default:
			return nil, fmt.Errorf("unsupported server request %s (M0 subset)", method)
		}
	})
}

// codexApproval：核可請求 → 同一 ApprovalDialog → allow=accept / deny=decline；逾時 decline（fail closed）。
func (a *App) codexApproval(method string, params json.RawMessage) map[string]string {
	id := fmt.Sprintf("codex-%d", time.Now().UnixNano())
	ch := make(chan bool, 1)
	a.registerApproval(id, "codex", func(allow bool, reason string) error {
		ch <- allow
		return nil
	})
	var p struct {
		ItemID string `json:"itemId"`
	}
	_ = json.Unmarshal(params, &p)
	a.audit("codex_approval_request", map[string]any{"id": id, "method": method, "raw_params": json.RawMessage(params)})
	runtime.EventsEmit(a.ctx, "approval:request", map[string]any{
		"id": id, "provider": "codex", "toolName": method, "inputJson": string(params)})
	decision := "decline"
	select {
	case allow := <-ch:
		if allow {
			decision = "accept"
		}
	case <-time.After(approvalTimeout()):
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		a.audit("codex_approval_timeout", map[string]any{"id": id})
	}
	a.audit("codex_approval_decision", map[string]any{"id": id, "decision": decision})
	return map[string]string{"decision": decision}
}

func (a *App) startCodex(prompt, resume, recordCase string) error {
	srv, err := a.ensureAppServer()
	if err != nil {
		return err
	}
	conn := srv.Conn()

	var rec *recorder.Recorder
	if recordCase != "" {
		rec, err = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".jsonl")
		if err != nil { // 可見的 session 失敗，不無聲降級
			runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "codex", "recorderError": err.Error()})
			return err
		}
		if err := conn.BeginRecording(rec.Line); err != nil {
			cerr := rec.CloseWith(recorder.Meta{Provider: "codex", RecordedAt: time.Now().UTC().Format(time.RFC3339)})
			msg := errors.Join(err, cerr).Error()
			runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "codex", "recorderError": msg})
			return err
		}
	}
	a.mu.Lock()
	a.codexRec, a.codexActive, a.activeProv = rec, true, "codex"
	a.mu.Unlock()

	go func() {
		// B6：resume 走 thread/resume（schema 必填 threadId），否則 thread/start
		method, params := codex.MethodThreadStart, map[string]any{}
		if resume != "" {
			method, params = codex.MethodThreadResume, map[string]any{"threadId": resume}
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		res, err := conn.Call(ctx, method, params)
		cancel()
		if err != nil {
			a.finishCodexTurnWithError(srv, method+": "+err.Error())
			return
		}
		var tr struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(res, &tr)
		a.mu.Lock()
		a.codexThreadID = tr.Thread.ID
		a.mu.Unlock()
		runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(contract.Event{
			Provider: contract.ProviderCodex, Kind: contract.KindInit,
			SessionID: tr.Thread.ID, Raw: res}))
		// turn/start 的 response 在 turn 結束才回來；收尾由 turn/completed 通知驅動
		tctx, tcancel := context.WithTimeout(a.ctx, 30*time.Minute)
		defer tcancel()
		if _, err := conn.Call(tctx, codex.MethodTurnStart, map[string]any{
			"threadId": tr.Thread.ID,
			"input":    []map[string]any{{"type": "text", "text": prompt}},
		}); err != nil {
			a.finishCodexTurnWithError(srv, "turn/start: "+err.Error())
		}
	}()
	return nil
}

// finishCodexTurn：v1.6 順序固定——先 StopRecording（原子 detach）再 CloseWith；
// 長駐 server 不隨回合退出，meta 記 process_still_running + live stderr snapshot。
func (a *App) finishCodexTurn(srv *codex.Server, params json.RawMessage) {
	a.mu.Lock()
	rec, active := a.codexRec, a.codexActive
	a.codexRec, a.codexActive = nil, false
	a.mu.Unlock()
	if !active {
		return
	}
	var recErrText string
	if rec != nil {
		stopErr := srv.Conn().StopRecording()
		cerr := rec.CloseWith(recorder.Meta{Provider: "codex", CLIVersion: a.cliVersion("codex"),
			Argv: srv.Argv(), CWD: a.workspaceDir, RecordedAt: time.Now().UTC().Format(time.RFC3339),
			ProcessStillRunning: true, StderrTail: srv.StderrSnapshot()})
		if err := errors.Join(stopErr, cerr); err != nil {
			recErrText = err.Error()
		}
	}
	var p struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "codex",
		"turnStatus": p.Turn.Status, "processStillRunning": true,
		"stderrTail": srv.StderrSnapshot(), "recorderError": recErrText})
}

func (a *App) finishCodexTurnWithError(srv *codex.Server, msg string) {
	a.mu.Lock()
	rec, active := a.codexRec, a.codexActive
	a.codexRec, a.codexActive = nil, false
	a.mu.Unlock()
	var recErrText string
	if active && rec != nil {
		stopErr := srv.Conn().StopRecording()
		cerr := rec.CloseWith(recorder.Meta{Provider: "codex", Argv: srv.Argv(),
			RecordedAt: time.Now().UTC().Format(time.RFC3339),
			ProcessStillRunning: true, StderrTail: srv.StderrSnapshot()})
		if err := errors.Join(stopErr, cerr); err != nil {
			recErrText = err.Error()
		}
	}
	runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "codex",
		"error": msg, "processStillRunning": true, "recorderError": recErrText})
}

// RestartCodexServerRecorded：B1 受控重啟 probe（薄封裝 codex.RunHandshakeProbe，
// 生命週期 Begin → Handshake → Stop → CloseWith 與四階段失敗處置在 Task 8 以測試固定）。
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
	a.wireCodexHandlers(srv)
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
		runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "codex",
			"event": "login_started", "authUrl": lr.AuthURL})
		return nil
	case "claude":
		// 官方命令 claude auth login 為互動式（fixture claude-auth-help.txt）：
		// 系統終端機 fallback + 每 5s 輪詢 auth status、5 分鐘逾時（v1.5）。
		script := fmt.Sprintf("tell application \"Terminal\" to do script %q",
			a.claudeCLIPath()+" auth login")
		if err := exec.Command("osascript", "-e", "tell application \"Terminal\" to activate").Run(); err != nil {
			return fmt.Errorf("open terminal: %w", err)
		}
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			return fmt.Errorf("launch login in terminal: %w", err)
		}
		runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "claude", "event": "terminal_opened"})
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
	runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "codex", "event": "login_cancelled"})
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
				runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "claude", "event": "logged_in"})
				return
			}
		}
	}
	runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "claude", "event": "login_pending_timeout"})
}

func (a *App) Logout(provider string) error {
	switch provider {
	case "claude":
		out, err := exec.Command(a.claudeCLIPath(), "auth", "logout").CombinedOutput()
		runtime.EventsEmit(a.ctx, "auth:status", map[string]any{"provider": "claude",
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
