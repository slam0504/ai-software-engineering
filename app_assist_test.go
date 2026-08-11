package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	if err := a.SpecAssist("claude", "spec_assist", "draft2"); !errors.Is(err, ErrAssistActive) {
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

func TestCodexAssistCannotEscalateOrMutateSessionView(t *testing.T) {
	fake := fakeCodexEscalatingRunner(t)
	a := newTestAppAssist(t, fake)
	var providerEvents int
	a.emitUI = func(_ string, data any) {
		if isProviderView(data) {
			providerEvents++
		}
	}
	err := a.SpecAssist("codex", "spec_assist", "draft")
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
	if err := a.SpecAssist("codex", "spec_assist", "draft"); err != nil {
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
