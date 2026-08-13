// Package assist implements SpecAssist：隔離的一次性（one-shot）AI 草擬呼叫，
// 帶 provider 端強制的「零 workspace 變更」保證（Stage A §5.1／§8-risk-1）。
//
// 安全不變量：provider-enforced zero workspace mutation。強制點在 process
// 啟動參數（argv）與 wire params，**不是** prompt 文字：
//
//   - Claude：獨立 one-shot process，argv 帶 `--tools ""`（空 allowed-tools ＝
//     無工具 → 無法變更 workspace）。這是 NEW 隔離 process，不是長駐 session。
//   - Codex：獨立 ephemeral thread，turn wire 同時帶
//     `sandboxPolicy={type:"readOnly",networkAccess:false}` ＋ `approvalPolicy="never"`；
//     任何 escalation／approval request 一律 fail closed（回錯、terminate），
//     絕不讓使用者核可升級而破壞 zero-mutation。
//
// enforcement 證據以 argv／wire 建構斷言（ClaudeAssistArgs／CodexAssistTurnParams），
// 非 behavioral（見 oneshot_test.go）。
package assist

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/proc"
)

// Runner 驅動單一隔離 one-shot 草擬，並把 provider 輸出以 envelope 逐一送進 sink。
// zero workspace mutation 的強制在 argv／wire（見 package doc），不在 prompt。
// Run 於失敗回 error，包含 provider 的任何 escalation／approval request（fail closed）。
type Runner interface {
	Run(ctx context.Context, prompt string, sink func(contract.Envelope)) error
}

// ClaudeAssistArgs 回傳隔離 Claude one-shot 的 argv（不含 binary）。`--tools ""`
// 設定**空** allowed-tools list——argv 級強制該 process 無任何工具、因此無法變更
// workspace。強制在此，不在 prompt 文字。
func ClaudeAssistArgs() []string {
	return []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--tools", "", // 空 allowed-tools = 無工具（argv 級 zero-mutation 強制）
	}
}

// ClaudePlannerArgs 回傳 PlannerAssist 隔離 Claude one-shot 的 argv（不含
// binary）：唯讀工具白名單（pin 2.1.223；M3a.1 Task 0 live probe 已驗證——
// Write 在 CLI 層被拒、written.txt 未產生，見 docs/spikes/m3a1-planner-probe.md）。
// enforcement 仍在 argv：白名單只含 Read/Glob/Grep，無 Write/Edit/Bash →
// 無法變更 workspace。`--setting-sources ""` 隔離使用者全域／project／local
// settings 的 hook（probe 附註的 SessionStart 副作用；Task 7 實測隔離生效），
// 堵住 hook 這條不受 --tools 過濾的寫入路徑。此 argv 必須與 preflight 凍結
// 基準 probeApprovedClaudeArgs（preflight.go）逐字一致，偏離即 fail closed。
func ClaudePlannerArgs() []string {
	return []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--setting-sources", "", // 空 setting sources：隔離 hook 副作用（見上）
		"--tools", "Read,Glob,Grep", // 唯讀白名單（argv 級強制，非 prompt 文字）
	}
}

// codexReadOnlySandbox：pinned schema SandboxPolicy 的 readOnly variant
// （schemas/codex/v2/TurnStartParams.json，networkAccess 預設 false）。
func codexReadOnlySandbox() map[string]any {
	return map[string]any{"type": "readOnly", "networkAccess": false}
}

// CodexAssistThreadParams 回傳 ephemeral thread 的 thread/start params：
// approvalPolicy=never（無升級路徑）＋ readOnly sandbox。resume 空＝全新 thread。
func CodexAssistThreadParams(resume string) map[string]any {
	p := map[string]any{
		"approvalPolicy": "never",
		"sandboxPolicy":  codexReadOnlySandbox(),
	}
	if resume != "" {
		p["threadId"] = resume
	}
	return p
}

// CodexAssistTurnParams 回傳 turn/start params：readOnly sandbox（無寫入）＋
// network 關閉，且 approvalPolicy=never（無 escalation 路徑）——wire 級 zero
// workspace mutation 強制。
func CodexAssistTurnParams(threadID, prompt string) map[string]any {
	return map[string]any{
		"threadId":       threadID,
		"input":          []map[string]any{{"type": "text", "text": prompt}},
		"sandboxPolicy":  codexReadOnlySandbox(),
		"approvalPolicy": "never",
	}
}

func assistClientInfo() codex.ClientInfo {
	return codex.ClientInfo{Name: "sdlc-workbench-assist", Version: "0.0.1"}
}

// ---- Claude 隔離 one-shot ----

type claudeAssist struct {
	bin  string
	cwd  string
	env  []string
	args []string // argv 白名單來源：ClaudeAssistArgs（SpecAssist）或 ClaudePlannerArgs（PlannerAssist）
}

// NewClaudeAssist 回傳以獨立 one-shot Claude process 草擬的 Runner（argv 含
// `--tools ""`）。此為 NEW 隔離 process，不是長駐 session。
func NewClaudeAssist(bin, cwd string, env []string) Runner {
	return &claudeAssist{bin: bin, cwd: cwd, env: env, args: ClaudeAssistArgs()}
}

// NewClaudePlanner 回傳 PlannerAssist 用的獨立 one-shot Claude Runner（argv 帶
// 唯讀工具白名單 ClaudePlannerArgs）。與 NewClaudeAssist 共用 Run／process
// 生命週期，僅 argv 白名單不同——enforcement 一樣在 argv，不在 prompt。
func NewClaudePlanner(bin, cwd string, env []string) Runner {
	return &claudeAssist{bin: bin, cwd: cwd, env: env, args: ClaudePlannerArgs()}
}

func (c *claudeAssist) Run(ctx context.Context, prompt string, sink func(contract.Envelope)) error {
	p, err := proc.Start(ctx, proc.Config{Binary: c.bin, Args: c.args, Dir: c.cwd, Env: c.env})
	if err != nil {
		return err
	}
	// one-shot：送 prompt（stream-json user message）後即關 stdin。
	msg, _ := json.Marshal(map[string]any{"type": "user",
		"message": map[string]any{"role": "user",
			"content": []map[string]string{{"type": "text", "text": prompt}}}})
	if _, werr := fmt.Fprintf(p.Stdin, "%s\n", msg); werr != nil {
		_ = p.Terminate()
		p.Wait()
		return werr
	}
	_ = p.Stdin.Close()

	events := make(chan contract.Event, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(p.Stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			events <- claude.Decode(sc.Bytes())
		}
		if serr := sc.Err(); serr != nil { // 傳輸層錯誤（>16MB 行／pipe read）是驗收證據，不可吞
			events <- contract.Event{Provider: contract.ProviderClaude,
				Kind: contract.KindStreamError, Raw: []byte(serr.Error()), Err: serr}
			_ = p.Terminate() // stream 已不可信，收掉整組（對齊 session.go 慣例）
		}
	}()

	for {
		select {
		case ev, ok := <-events:
			if !ok { // stdout EOF：process 已收尾
				ex := p.Wait()
				return ex.Err
			}
			sink(contract.Wrap(ev, ""))
		case <-ctx.Done(): // cancel／timeout／shutdown reclaim：terminate 整組並排乾
			_ = p.Terminate()
			for range events {
			}
			p.Wait()
			return ctx.Err()
		}
	}
}

// ---- Codex 隔離 ephemeral thread（獨立 app-server process）----

type codexAssist struct {
	bin string
	cwd string
	env []string
}

// NewCodexAssist 回傳以獨立 ephemeral Codex thread 草擬的 Runner。為求隔離與
// fail-closed，它啟動**自己的** app-server process（獨立 conn／handler），
// 不發布到 App 的 a.runner／a.codexConn。turn wire 帶 readOnly sandbox ＋
// approvalPolicy=never；任何 escalation／approval request 一律 fail closed。
func NewCodexAssist(bin, cwd string, env []string) Runner {
	return &codexAssist{bin: bin, cwd: cwd, env: env}
}

// NewCodexPlanner 回傳 PlannerAssist 用的獨立 ephemeral Codex Runner。turn
// wire（CodexAssistTurnParams）本已是 readOnly sandbox＋approvalPolicy=never，
// 對唯讀探索與草擬皆已足夠——不需要另一組唯讀白名單，同構造即可。
func NewCodexPlanner(bin, cwd string, env []string) Runner {
	return NewCodexAssist(bin, cwd, env)
}

func (c *codexAssist) Run(ctx context.Context, prompt string, sink func(contract.Envelope)) error {
	srv, err := codex.StartAppServer(ctx, codex.Config{Binary: c.bin, CWD: c.cwd, Env: c.env})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Terminate(); srv.Wait() }()
	conn := srv.Conn()

	escalated := make(chan *EnforcementViolation, 1)
	conn.OnServerRequest(func(method string, _ json.RawMessage) (any, error) {
		// fail closed：readOnly＋never 下不應出現任何 approval／escalation；若仍
		// 收到，回錯讓 provider 中止，絕不讓使用者核可升級（破壞 zero-mutation）。
		// typed violation：app 層 errors.As 判定後建 planner-enforcement hard 項。
		v := &EnforcementViolation{Provider: "codex", Detail: method}
		select {
		case escalated <- v:
		default:
		}
		return nil, v
	})
	// preferViolation：任何 return 前先 non-blocking drain escalated——violation
	// 與 wire error 可能並存（handler 已填 channel 但 Call 先因 error response
	// 返回；或 final select 多分支同時 ready 隨機取），runtime 違規不得被同時
	// 發生的一般錯誤降級（app 層 errors.As 分類的 fail-closed 對偶）。
	preferViolation := func(err error) error {
		select {
		case v := <-escalated:
			return v
		default:
			return err
		}
	}
	turnDone := make(chan struct{})
	conn.OnNotification(func(method string, params json.RawMessage) {
		sink(contract.Wrap(codex.MapEvent(method, params), ""))
		if method == codex.MethodTurnCompleted {
			select {
			case <-turnDone:
			default:
				close(turnDone)
			}
		}
	})
	conn.OnUnknown(func(raw []byte) {
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderCodex,
			Kind: contract.KindUnknown, Raw: append([]byte(nil), raw...)}, ""))
	})

	hctx, hcancel := context.WithTimeout(ctx, 30*time.Second)
	defer hcancel()
	if herr := conn.Handshake(hctx, assistClientInfo()); herr != nil {
		return preferViolation(herr)
	}

	sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
	defer scancel()
	res, err := conn.Call(sctx, codex.MethodThreadStart, CodexAssistThreadParams(""))
	if err != nil {
		return preferViolation(err)
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(res, &tr) != nil || tr.Thread.ID == "" {
		return preferViolation(errors.New("assist: codex thread id missing in thread/start response"))
	}
	if _, err := conn.Call(sctx, codex.MethodTurnStart, CodexAssistTurnParams(tr.Thread.ID, prompt)); err != nil {
		return preferViolation(err)
	}

	select {
	case <-turnDone: // turn/completed：草擬結束（若違規已發生仍 fail closed）
		return preferViolation(nil)
	case v := <-escalated: // escalation/approval：fail closed（typed violation）
		return v
	case <-ctx.Done(): // cancel／timeout／shutdown reclaim
		return preferViolation(ctx.Err())
	case <-conn.Done(): // wire EOF（server 死亡）
		return preferViolation(errors.New("assist: codex connection closed before turn completed"))
	}
}
