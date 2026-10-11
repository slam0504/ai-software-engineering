package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/assist"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 R2／§5.4：SpecAssist／PlanAssist 的閘門 ----
//
// 拒絕時要同時證明四件事：回 workspace_confirmation_required；本次的 context 已被取消
// （不依賴 gen 建立之後才安排的 teardown）；gen 沒有登記、assistActive 沒有新增項目；
// runner／factory／preflight／gate journal 一個都沒碰。既有的 root＋purpose 形狀測試
// （TestAssistImplementationsTakeRootContextFirst）不修改，也必須維持通過。

func assistActiveCount(a *App) int {
	a.assistMu.Lock()
	defer a.assistMu.Unlock()
	return len(a.assistActive)
}

func countingRunner(calls *atomic.Int32) assist.Runner {
	return runnerFunc(func(_ context.Context, _ string, sink func(contract.Envelope)) error {
		calls.Add(1)
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderClaude,
			Kind: contract.KindDelta, Text: "draft", Raw: []byte(`{}`)}, ""))
		return nil
	})
}

func TestSpecAssistGate_RejectedCancelsContextAndRegistersNothing(t *testing.T) {
	var runs atomic.Int32
	runner := countingRunner(&runs)
	a := newTestAppAssist(t, runner)
	var factoryCalls atomic.Int32
	a.assistRunnerFactory = func(string) (assist.Runner, error) { factoryCalls.Add(1); return runner, nil }
	makePending(a)
	var rejectedCtx context.Context
	a.hookAssistGateRejected = func(ctx context.Context) { rejectedCtx = ctx }

	for _, provider := range []string{"claude", "codex"} {
		rejectedCtx = nil
		corr, err := a.SpecAssist(provider, "spec_assist", "draft")
		requireConfirmationError(t, err)
		if corr != "" {
			t.Fatalf("%s：拒絕時不得有 correlation id（gen 未建立）：%q", provider, corr)
		}
		if rejectedCtx == nil {
			t.Fatalf("%s：拒絕分支必須走到 hookAssistGateRejected", provider)
		}
		if !errors.Is(rejectedCtx.Err(), context.Canceled) {
			t.Fatalf("%s：本次呼叫的 context 必須已被取消（Err=%v）", provider, rejectedCtx.Err())
		}
		if n := assistActiveCount(a); n != 0 {
			t.Fatalf("%s：拒絕時 assistActive 不得新增項目（%d）", provider, n)
		}
	}
	if runs.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("拒絕時不得建立或執行 runner（runs=%d factory=%d）", runs.Load(), factoryCalls.Load())
	}

	// 既有檢查的優先順序不變：未知 provider 仍先回 unknown provider。
	if _, err := a.SpecAssist("bogus", "spec_assist", "draft"); err == nil ||
		!strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("未知 provider 必須維持 unknown provider：%v", err)
	}

	// 拒絕沒有洩漏任何獨佔性狀態：確認之後同一個 provider 的 Assist 可以正常執行。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SpecAssist("claude", "spec_assist", "draft"); err != nil {
		t.Fatalf("確認之後 SpecAssist 必須通過（不得有 ErrAssistActive 洩漏）：%v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("確認之後 runner 應被執行一次：%d", runs.Load())
	}
}

func TestPlanAssistGate_RejectedBeforeEnsureGateAndCancelsContext(t *testing.T) {
	var runs atomic.Int32
	a, _ := newTestAppGitAssist(t, countingRunner(&runs))
	plantPlannerBin(t, a, "claude", "2.1.223 (Claude Code)")
	makePending(a)
	var rejectedCtx context.Context
	a.hookAssistGateRejected = func(ctx context.Context) { rejectedCtx = ctx }

	// 沒有 active Gate 1：若確認閘排在 ensureGate／gateList 之後，這裡會先回
	// 「無生效規格核可」。回的是確認錯誤，就證明閘門在 ensureGate 之前。
	corr, err := a.PlanAssist("claude", "draft plan")
	requireConfirmationError(t, err)
	if corr != "" {
		t.Fatalf("拒絕時不得有 correlation id：%q", corr)
	}
	if rejectedCtx == nil || !errors.Is(rejectedCtx.Err(), context.Canceled) {
		t.Fatalf("本次呼叫的 context 必須已被取消（由既有的 defer cancel）：%v", rejectedCtx)
	}
	if n := assistActiveCount(a); n != 0 {
		t.Fatalf("拒絕時 assistActive 不得新增項目：%d", n)
	}
	if _, serr := os.Stat(filepath.Join(a.stateDir, "gate.jsonl")); !os.IsNotExist(serr) {
		t.Fatal("拒絕時不得觸發 ensureGate（gate.jsonl 不應被建立）")
	}
	if runs.Load() != 0 {
		t.Fatal("拒絕時不得執行 runner")
	}

	if _, err := a.PlanAssist("bogus", "draft plan"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("未知 provider 必須維持 unknown provider：%v", err)
	}

	// 確認之後：既有的前置檢查照舊（沒有 Gate 1 → 原本的錯誤），核可 Gate 1 後完整通過。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PlanAssist("claude", "draft plan"); err == nil || !strings.Contains(err.Error(), "Gate 1") {
		t.Fatalf("確認之後必須回到既有的前置檢查（無 Gate 1）：%v", err)
	}
	approveGate1Spec(t, a)
	if _, err := a.PlanAssist("claude", "draft plan"); err != nil {
		t.Fatalf("確認且前置條件齊備後 PlanAssist 必須通過：%v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("runner 應被執行一次：%d", runs.Load())
	}
}
