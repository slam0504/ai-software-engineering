package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/assist"
)

// ---- B4 §5.5：audit 事件；§6.1：確認與啟動的並行行為 ----

func TestWorkspaceConfirmedAudit_OncePerTransitionWithFields(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	makePending(a)

	_ = a.ConfirmWorkspace(a.workspaceDir + "/wrong")          // 路徑不符：不得有成功 audit
	if _, err := a.WorkspaceConfirmationStatus(); err != nil { // 查詢 status 不寫 audit
		t.Fatal(err)
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmed")); n != 0 {
		t.Fatalf("確認之前（含失敗的確認、查詢 status）不得有 workspace_confirmed：%d", n)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil { // 重複確認：冪等，不重寫
		t.Fatal(err)
	}
	recs := auditRecordsOfKind(t, a, "workspace_confirmed")
	if len(recs) != 1 {
		t.Fatalf("workspace_confirmed 只在 Pending→Confirmed 寫一次，實得 %d 筆", len(recs))
	}
	if recs[0]["workspace"] != a.workspaceDir || recs[0]["source"] != "home" {
		t.Fatalf("欄位不符：%v", recs[0])
	}
	if !reflect.DeepEqual(recs[0]["reasons"], []any{wsReasonSourceHome, wsReasonIsHome}) {
		t.Fatalf("reasons 不符：%v", recs[0]["reasons"])
	}

	b, _ := newTestApp(t) // NotRequired：冪等成功，不寫 audit
	enableAudit(t, b)
	if err := b.ConfirmWorkspace(b.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if n := len(auditRecordsOfKind(t, b, "workspace_confirmed")); n != 0 {
		t.Fatalf("NotRequired 的 ConfirmWorkspace 不寫 audit：%d", n)
	}
}

func TestWorkspaceBlockedAudit_FieldsAndNoPrompt(t *testing.T) {
	a := newTestAppAssist(t, runnerMustNotBeCalled(t))
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	const secret = "TOP-SECRET-PROMPT-TEXT"
	requireConfirmationError(t, a.StartSession(string(w), secret, "", "", "t", ""))
	requireConfirmationError(t, a.SendMessage(string(w), secret))
	_, err := a.SpecAssist("codex", "spec_assist", secret)
	requireConfirmationError(t, err)
	_, err = a.PlanAssist("claude", secret)
	requireConfirmationError(t, err)

	recs := auditRecordsOfKind(t, a, "workspace_confirmation_blocked")
	if len(recs) != 4 {
		t.Fatalf("每一次被拒絕的請求寫一筆，期望 4 筆，實得 %d：%v", len(recs), recs)
	}
	want := []struct {
		op, provider string
		hasWSID      bool
	}{
		{"start_session", "claude", true}, {"send_message", "claude", true},
		{"spec_assist", "codex", false}, {"plan_assist", "claude", false},
	}
	for i, wnt := range want {
		r := recs[i]
		if r["op"] != wnt.op || r["provider"] != wnt.provider {
			t.Fatalf("第 %d 筆 op／provider 不符：%v", i, r)
		}
		if _, has := r["wsid"]; has != wnt.hasWSID {
			t.Fatalf("第 %d 筆 wsid 欄位的有無不符（want %v）：%v", i, wnt.hasWSID, r)
		}
		if r["workspace"] != a.workspaceDir || r["source"] != "home" ||
			!reflect.DeepEqual(r["reasons"], []any{wsReasonSourceHome, wsReasonIsHome}) {
			t.Fatalf("第 %d 筆 workspace／source／reasons 不符：%v", i, r)
		}
	}
	if raw := readAuditRaw(t, a); strings.Contains(raw, secret) {
		t.Fatal("audit 不得包含 prompt")
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	a.assistRunnerFactory = func(string) (assist.Runner, error) { return countingRunner(&runs), nil }
	if _, err := a.SpecAssist("codex", "spec_assist", "ok"); err != nil || runs.Load() != 1 {
		t.Fatalf("確認之後 SpecAssist 必須通過閘門並執行 runner：err=%v runs=%d", err, runs.Load())
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 4 {
		t.Fatalf("確認之後通過閘門的請求不得再寫 blocked：%d", n)
	}
}

// H4／spec §6.1：audit 必須在不持有 workspaceConfirm mutex 的情況下寫入。hookAudit 在
// a.audit 內、呼叫端的 goroutine 上執行——此刻若鎖還被持有，TryLock 會失敗。
func TestWorkspaceConfirmationAudit_WrittenOutsideMutex(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)
	var checked atomic.Int32
	a.hookAudit = func(kind string) {
		if kind != "workspace_confirmed" && kind != "workspace_confirmation_blocked" {
			return
		}
		checked.Add(1)
		if !a.workspaceConfirm.mu.TryLock() {
			t.Errorf("%s 的 audit 在持有 workspaceConfirm mutex 時被寫入", kind)
			return
		}
		a.workspaceConfirm.mu.Unlock()
	}
	requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", ""))
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if checked.Load() != 2 {
		t.Fatalf("兩種 audit 都必須被檢查到：%d", checked.Load())
	}
}

// 「只有在確認的狀態轉換完成之後，啟動才可能通過」——行為斷言，不只靠 -race。
// 做法：轉換之前先確認啟動被拒絕；再讓 Confirm 停在「狀態已轉換、audit 尚未寫完」這個點
// （hookAudit 阻塞），此時啟動必須已經可以通過——轉換已完成，而且閘門沒有持有鎖或等 audit。
func TestConfirmTransitionCompletesBeforeStartCanPass(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", "")) // 轉換之前：拒絕

	inAudit, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(doRelease) // 失敗路徑也要放行被擋住的 goroutine，否則 fixture 收尾會等不到 inflight 交易
	a.hookAudit = func(kind string) {
		if kind == "workspace_confirmed" {
			close(inAudit)
			<-release
		}
	}
	confirmDone := make(chan error, 1)
	go func() { confirmDone <- a.ConfirmWorkspace(a.workspaceDir) }()
	select {
	case <-inAudit:
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm 沒有走到 audit")
	}
	// 此刻：狀態已轉換、audit 被擋住。啟動必須通過，而且不能被 audit 擋住。
	// （刻意不在這裡直接讀 snapshot()：若實作錯把 audit 放進鎖內，讀 snapshot 會跟著死鎖，
	// 測試就卡住而不是失敗。）
	startDone := make(chan error, 1)
	go func() { startDone <- a.StartSession(string(w), "hi", "", "", "t", "") }()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("狀態轉換完成之後啟動必須通過：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("啟動被確認的 audit 擋住了（閘門不得等 audit，也不得持有鎖等 audit）")
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	doRelease()
	if err := <-confirmDone; err != nil {
		t.Fatal(err)
	}
}

// 併發壓力（搭配 -race）：多個 Confirm 同時進來只會有一筆 workspace_confirmed；閘門的結果
// 只可能是「通過」或「確認錯誤」，而且在同一條 goroutine 上一旦通過就不會再被拒絕。
func TestConcurrentConfirmAndGate_SingleAuditAndMonotonic(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	makePending(a)
	const confirmers, gaters, rounds = 16, 16, 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < confirmers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Errorf("ConfirmWorkspace: %v", err)
			}
		}()
	}
	for i := 0; i < gaters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			passed := false
			for r := 0; r < rounds; r++ {
				err := a.requireWorkspaceConfirmed("test", "claude", "")
				switch {
				case err == nil:
					passed = true
				case passed:
					t.Errorf("閘門通過之後又被拒絕（狀態倒退）：%v", err)
					return
				case !strings.HasPrefix(err.Error(), "workspace_confirmation_required"):
					t.Errorf("閘門只能回通過或確認錯誤：%v", err)
					return
				}
				if _, serr := a.WorkspaceConfirmationStatus(); serr != nil {
					t.Errorf("status: %v", serr)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmed")); n != 1 {
		t.Fatalf("併發確認必須只寫一筆 workspace_confirmed，實得 %d", n)
	}
	if err := a.requireWorkspaceConfirmed("test", "claude", ""); err != nil {
		t.Fatalf("全部完成後閘門必須通過：%v", err)
	}
}

// readAuditRaw：audit.jsonl 全文（找不到回空字串）。
func readAuditRaw(t *testing.T, a *App) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}
