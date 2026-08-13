package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/assist"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- SpecAssist lifecycle barrier 測試（production path；fake Runner）----

// runnerFunc：把純函式當 assist.Runner（測試 fake）。
type runnerFunc func(ctx context.Context, prompt string, sink func(contract.Envelope)) error

func (f runnerFunc) Run(ctx context.Context, prompt string, sink func(contract.Envelope)) error {
	return f(ctx, prompt, sink)
}

// blockingRunner：Run 卡住直到 ctx 取消（獨佔性 barrier 用）。
func blockingRunner() assist.Runner {
	return runnerFunc(func(ctx context.Context, _ string, _ func(contract.Envelope)) error {
		<-ctx.Done()
		return ctx.Err()
	})
}

// runnerSignaling：Run 卡住直到 ctx 取消，取消時關閉 done（shutdown reclaim barrier 用）。
func runnerSignaling(done chan struct{}) assist.Runner {
	return runnerFunc(func(ctx context.Context, _ string, _ func(contract.Envelope)) error {
		<-ctx.Done()
		close(done)
		return ctx.Err()
	})
}

// fakeCodexEscalatingRunner：惡意 provider——嘗試 tool/write（只會進 assist 顯示
// 出口，App 不據此寫 workspace）並要求 escalation → Run 回錯（fail closed）。
func fakeCodexEscalatingRunner(t *testing.T) assist.Runner {
	t.Helper()
	return runnerFunc(func(_ context.Context, _ string, sink func(contract.Envelope)) error {
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderCodex,
			Kind: contract.KindToolUse, Text: "write spec/evil.md",
			Raw: []byte(`{"attempt":"mutate"}`)}, ""))
		return errors.New("assist: codex requested escalation/approval — failing closed")
	})
}

// newTestAppAssist：SpecAssist 測試基盤。把 Manager 事件走 production 出口
// （a.emit → a.emitUI），assist envelope 才能被 provider-view 斷言看見（並驗證
// 其 purpose 分流）；注入 fake Runner。
func newTestAppAssist(t *testing.T, r assist.Runner) *App {
	t.Helper()
	a, ui := newTestApp(t)
	sink, err := appcore.NewJSONLSink(filepath.Join(a.stateDir, "events-assist.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a.manager = appcore.New(appcore.Config{Sink: sink,
		Emit: func(env contract.Envelope) { a.emit("workbench:event", env) }})
	a.emitUI = ui.emit
	a.assistRunnerFactory = func(string) (assist.Runner, error) { return r, nil }
	return a
}

func waitAssistActive(t *testing.T, a *App, provider string) {
	t.Helper()
	waitFor(t, "assist active for "+provider, func() bool {
		a.assistMu.Lock()
		defer a.assistMu.Unlock()
		_, ok := a.assistActive[provider]
		return ok
	})
}

func (a *App) managerClosed() bool { return a.manager.Closed() }

// isProviderView：data 是否會進 provider session view。scope=workspace 與
// purpose=spec_assist（前端二次分流）均**不**屬 provider view；帶 provider 的
// UI map（approval:request／session:done 等）屬 provider view。
func isProviderView(data any) bool {
	switch v := data.(type) {
	case contract.Envelope:
		if v.Scope == "workspace" || v.Purpose == "spec_assist" {
			return false
		}
		return v.Provider != ""
	case map[string]any:
		_, hasProvider := v["provider"]
		return hasProvider
	}
	return false
}

// assertWorkspaceUnchanged：assist 不得寫入 workspace（草稿只進 UI／稽核）。
func assertWorkspaceUnchanged(t *testing.T, a *App) {
	t.Helper()
	entries, err := os.ReadDir(a.workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".workbench" {
			continue // app state，非 workspace 內容
		}
		t.Fatalf("assist must not mutate workspace, found: %s", e.Name())
	}
}

func TestSpecAssistExclusivePerProvider(t *testing.T) {
	a := newTestAppAssist(t, blockingRunner())
	go a.SpecAssist("claude", "spec_assist", "draft") //nolint:errcheck // 卡住至 reclaim
	waitAssistActive(t, a, "claude")
	if _, err := a.SpecAssist("claude", "spec_assist", "draft2"); !errors.Is(err, ErrAssistActive) {
		t.Fatalf("second concurrent assist must be rejected: %v", err)
	}
	a.reclaimAssists() // 收束：cancel 卡住的 one-shot（釋放 goroutine＋交易閘）
}

func TestShutdownWaitsForAndReclaimsSpecAssist(t *testing.T) {
	done := make(chan struct{})
	a := newTestAppAssist(t, runnerSignaling(done))
	go a.SpecAssist("claude", "spec_assist", "draft") //nolint:errcheck
	waitAssistActive(t, a, "claude")
	a.shutdown(context.Background()) // cancel + 等 bounded completion + 稽核收尾 → 才 Close
	select {
	case <-done:
	default:
		t.Fatal("shutdown must reclaim (cancel/terminate) the one-shot")
	}
	if a.manager != nil && !a.managerClosed() {
		t.Fatal("Manager.Close must happen after assist reclaimed")
	}
	a.assistMu.Lock()
	n := len(a.assistActive)
	a.assistMu.Unlock()
	if n != 0 {
		t.Fatalf("active flag must be cleared at teardown, got %d", n)
	}
}

// shutdown-during-startup 窗口：assist 已把 gen 入 assistActive、尚未 beginAppTxn
// 時 shutdown 介入——shutdown 不得被它 stall（此刻無 inflight），事後 beginAppTxn
// 被 gate 拒、gen rollback。反轉 insert／beginAppTxn 順序後此窗口關閉。
func TestShutdownReclaimClosesAssistStartupWindow(t *testing.T) {
	a := newTestAppAssist(t, blockingRunner())
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookAssistBeforeTxn = func() {
		close(entered)
		<-release
	}
	errCh := make(chan error, 1)
	go func() { _, err := a.SpecAssist("claude", "spec_assist", "draft"); errCh <- err }()
	<-entered // gen 已可見、beginAppTxn 未開始
	a.hookAssistBeforeTxn = nil

	shutDone := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(shutDone) }()
	select { // 不得被 startup 窗口內的 assist 卡住（無 inflight 可等）
	case <-shutDone:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown stalled on an assist still in its startup window")
	}
	close(release) // 放行 beginAppTxn → shuttingDown → 拒絕 → rollback、無 inflight
	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("in-window assist must be rejected by the shutdown gate, got %v", err)
	}
	a.assistMu.Lock()
	n := len(a.assistActive)
	a.assistMu.Unlock()
	if n != 0 {
		t.Fatalf("rolled-back assist must leave no active gen, got %d", n)
	}
}

func TestCodexAssistCannotEscalateOrMutateSessionView(t *testing.T) {
	fake := fakeCodexEscalatingRunner(t)
	a := newTestAppAssist(t, fake)
	var providerEvents int
	a.emitUI = func(_ string, data any) {
		if isProviderView(data) {
			providerEvents++
		}
	}
	_, err := a.SpecAssist("codex", "spec_assist", "draft")
	if err == nil {
		t.Fatal("escalation/approval must fail closed")
	}
	if providerEvents != 0 {
		t.Fatalf("assist events must not enter provider session view, got %d", providerEvents)
	}
	assertWorkspaceUnchanged(t, a)

	// 稽核仍完整：assist 事件經 Manager.EmitAssist 出口，帶 purpose=spec_assist、
	// scope=session、correlation_id，但不進 provider slot（totals 不受污染）。
	if cost, usage := a.manager.Totals(contract.ProviderCodex); cost != 0 || usage.OutputTokens != 0 {
		t.Fatalf("assist must not pollute provider totals: cost=%v usage=%+v", cost, usage)
	}
	if st := a.manager.State(contract.ProviderCodex); st != "" && st != contract.StateIdle {
		t.Fatalf("assist must not drive provider reducer, got state=%q", st)
	}
}

// 晚到舊 generation 事件（correlation 不符）丟棄並 fail loud——不進 provider slot。
func TestSpecAssistStaleGenerationEventDropped(t *testing.T) {
	var captured func(contract.Envelope)
	// runner 洩漏 sink 供 Run 之後（此 generation 已被取代／收尾）再次觸發。
	leak := runnerFunc(func(_ context.Context, _ string, sink func(contract.Envelope)) error {
		captured = sink
		return nil // 立即結束 → teardown 清 active flag
	})
	a := newTestAppAssist(t, leak)
	if _, err := a.SpecAssist("codex", "spec_assist", "draft"); err != nil {
		t.Fatal(err)
	}
	// 此刻無 active generation：晚到事件必被丟棄並發 stream_error（fail loud）。
	ui := &captureEnvs{}
	a.manager = appcore.New(appcore.Config{Sink: nullSink{}, Emit: ui.add})
	captured(contract.Wrap(contract.Event{Provider: contract.ProviderCodex,
		Kind: contract.KindDelta, Text: "late", Raw: []byte(`{}`)}, ""))
	var sawStreamErr bool
	for _, e := range ui.envs {
		if e.Kind == string(contract.KindStreamError) && e.Purpose == "spec_assist" {
			sawStreamErr = true
		}
		if e.Kind == string(contract.KindDelta) {
			t.Fatal("stale-generation content event must be dropped, not forwarded")
		}
	}
	if !sawStreamErr {
		t.Fatal("stale-generation event must fail loud with a stream_error")
	}
}

type captureEnvs struct{ envs []contract.Envelope }

func (c *captureEnvs) add(env contract.Envelope) { c.envs = append(c.envs, env) }

type nullSink struct{}

func (nullSink) Write(contract.Envelope) error { return nil }
func (nullSink) Close() error                  { return nil }

// ---- PlanAssist（Task 11：PlannerAssist 唯讀探索 one-shot）測試 ----

// newTestAppGitAssist：git init'd workspace（PlanAssist 的 Gate 1／git status
// 前置檢查與 headOID 都需要真實 git repo，同 newTestAppGit）＋ assist Runner
// 注入點；events 走 captureEnvs（同 TestSpecAssistStaleGenerationEventDropped
// 的簡化 pattern，直接掛 manager Emit，略過 a.emit／emitUI 中介層）。
func newTestAppGitAssist(t *testing.T, r assist.Runner) (*App, *captureEnvs) {
	t.Helper()
	a, _ := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")
	cap := &captureEnvs{}
	a.manager = appcore.New(appcore.Config{Sink: nullSink{}, Emit: cap.add})
	a.assistRunnerFactory = func(string) (assist.Runner, error) { return r, nil }
	return a, cap
}

// runnerMustNotBeCalled：precondition-failure 測試用 fake Runner——若被呼叫，
// 代表 fail-closed 前置檢查沒有真的擋在 runner 啟動之前（bug，而非驗證通過）。
func runnerMustNotBeCalled(t *testing.T) assist.Runner {
	t.Helper()
	return runnerFunc(func(context.Context, string, func(contract.Envelope)) error {
		t.Fatal("PlanAssist runner must not be invoked when a precondition check fails")
		return nil
	})
}

func TestPlanAssistRejectsWithoutActiveGate1(t *testing.T) {
	a, _ := newTestAppGitAssist(t, runnerMustNotBeCalled(t))
	_, err := a.PlanAssist("claude", "draft plan")
	if err == nil || !strings.Contains(err.Error(), "Gate 1") {
		t.Fatalf("PlanAssist without active Gate 1 must be rejected, got: %v", err)
	}
}

func TestPlanAssistRejectsNonPlanDirtyTree(t *testing.T) {
	a, _ := newTestAppGitAssist(t, runnerMustNotBeCalled(t))
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	// send＋approve 後 spec/ 已乾淨；再動一個非 plan 檔案且不提交 → non-plan dirty。
	if err := os.WriteFile(filepath.Join(a.workspaceDir, "README.md"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = a.PlanAssist("claude", "draft plan")
	if err == nil || !strings.Contains(err.Error(), "非 plan") {
		t.Fatalf("PlanAssist with non-plan dirty tree must be rejected, got: %v", err)
	}
}

// TestPlanAssistAllowsPlanDirtyTree is the positive counterpart to
// TestPlanAssistRejectsNonPlanDirtyTree: nonPlanDirtyPaths (app.go) filters
// out anything under spec.PlanScope ("plan/**") before deciding a tree is
// dirty, so an uncommitted change under plan/ must NOT block PlanAssist —
// only a dirty file outside plan/ does.
func TestPlanAssistAllowsPlanDirtyTree(t *testing.T) {
	var called bool
	capture := runnerFunc(func(_ context.Context, _ string, sink func(contract.Envelope)) error {
		called = true
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderClaude,
			Kind: contract.KindDelta, Text: "draft", Raw: []byte(`{}`)}, ""))
		return nil
	})
	a, _ := newTestAppGitAssist(t, capture)
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}

	// plan/ has an uncommitted change — must be allowed, not rejected as dirty.
	if err := os.MkdirAll(filepath.Join(a.workspaceDir, "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.workspaceDir, "plan", "P1.yaml"), []byte("plan_id: P1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.PlanAssist("claude", "draft plan"); err != nil {
		t.Fatalf("PlanAssist must not be rejected by an uncommitted plan/** change, got: %v", err)
	}
	if !called {
		t.Fatal("PlanAssist preconditions passed but the runner was never invoked")
	}
}

func TestPlanAssistInjectsAnalysisBaseAndSpecDigest(t *testing.T) {
	var capturedPrompt string
	capture := runnerFunc(func(_ context.Context, prompt string, sink func(contract.Envelope)) error {
		capturedPrompt = prompt
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderClaude,
			Kind: contract.KindDelta, Text: "draft", Raw: []byte(`{}`)}, ""))
		return nil
	})
	a, cap := newTestAppGitAssist(t, capture)
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, ok := activeGate1SpecManifestDigest(list)
	if !ok {
		t.Fatal("expected an active gate1 entry after approve")
	}
	headOID, err := a.specRepo.HeadCommit()
	if err != nil {
		t.Fatal(err)
	}

	corrID, err := a.PlanAssist("claude", "draft the plan")
	if err != nil {
		t.Fatalf("PlanAssist with active Gate 1 and a clean (non-plan) tree must succeed, got: %v", err)
	}
	if !strings.Contains(capturedPrompt, "analysis_base_commit="+headOID) {
		t.Fatalf("prompt must carry analysis_base_commit=%s, got: %s", headOID, capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "spec_manifest_digest="+wantDigest) {
		t.Fatalf("prompt must carry spec_manifest_digest=%s, got: %s", wantDigest, capturedPrompt)
	}
	if !strings.HasSuffix(capturedPrompt, "draft the plan") {
		t.Fatalf("caller's original prompt must be preserved verbatim as a suffix, got: %s", capturedPrompt)
	}

	var sawPlanDraft bool
	for _, env := range cap.envs {
		if env.CorrelationID != corrID {
			continue
		}
		if env.Purpose == "spec_assist" {
			t.Fatal("PlanAssist events must not carry purpose=spec_assist")
		}
		if env.Purpose == "plan_draft" {
			sawPlanDraft = true
		}
	}
	if !sawPlanDraft {
		t.Fatal("PlanAssist events must be emitted with purpose=plan_draft")
	}
}
