package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// ---- state binding 的 shutdown／startup lifecycle（reviewer 2026-08-19 P1）----
//
// 「lease 釋放之後不得再有任何 state mutation」不只涵蓋 session：gate、escalation
// 與 evidence 也是 state。原本只有 session 相關的 binding 進 shutdown 的 in-flight
// 交易，於是實測出「shutdown 已釋放 flock、第二個 process 已可取鎖之後，原
// EscalationCreate binding 仍寫得進 escalation.jsonl」。
//
// 這一組守兩件事：
//   - 收尾**之後**呼叫這些 binding 一律被拒，磁碟事實不變。
//   - 收尾**期間**已在進行中的寫入會被等待，lease 不會在它完成前放掉。

// escalationDigest：escalation journal 目前的內容摘要（不存在回空字串——「檔案
// 沒被建出來」與「內容沒變」是兩種都要驗的結果）。
func escalationDigest(t *testing.T, a *App) string {
	t.Helper()
	p := filepath.Join(a.stateDir, "escalation.jsonl")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return ""
	}
	return fileDigest(t, p)
}

// TestStateBindingsRejectedAfterShutdown
//
// 正題斷言：shutdown 之後每一個 state binding 都回錯，且 escalation.jsonl 的內容
// 一個 byte 都沒變。
//
// 覆蓋全部入口而不是只挑 EscalationCreate：reviewer 找到的是「有一個沒接上」，
// 逐一列舉才擋得住「下一個新增的 binding 又忘了接」。
func TestStateBindingsRejectedAfterShutdown(t *testing.T) {
	a, _ := newTestApp(t)
	useRealLease(t, a)
	if _, err := a.EscalationCreate("spec#1", "", "收尾前的項目"); err != nil {
		t.Fatalf("前提：收尾前必須寫得進去：%v", err)
	}
	before := escalationDigest(t, a)
	if before == "" {
		t.Fatal("前提不成立：escalation.jsonl 應該已經存在")
	}

	a.shutdown(context.Background())
	if !leaseReleased(t, a.stateDir) {
		t.Fatal("前提：正常收尾必須已經釋放 lease（否則這條測不到 lease 之後的寫入）")
	}

	calls := []struct {
		name string
		run  func() error
	}{
		{"EscalationCreate", func() error { _, err := a.EscalationCreate("spec#2", "", "收尾後"); return err }},
		{"EscalationAck", func() error { return a.EscalationAck("whatever") }},
		{"EscalationResolve", func() error { return a.EscalationResolve("whatever", "accepted_risk", "r") }},
		{"EscalationList", func() error { _, err := a.EscalationList(); return err }},
		{"GateList", func() error { _, err := a.GateList(); return err }},
		{"GateDecide", func() error { return a.GateDecide("id", "approve", "", nil) }},
		{"GateDecisionContext", func() error { _, err := a.GateDecisionContext("id"); return err }},
		{"SubmitForApproval", func() error { _, err := a.SubmitForApproval(); return err }},
		{"SubmitPlanForApproval", func() error { _, err := a.SubmitPlanForApproval("P1"); return err }},
		{"SubmitTestContract", func() error { _, err := a.SubmitTestContract("P1", "T1", "c", "e", "n", "m"); return err }},
		{"RunEvidence", func() error { _, err := a.RunEvidence("A1", "P1", "T1", "c", "expected_red", ""); return err }},
		{"EvidenceGet", func() error { _, err := a.EvidenceGet("E1"); return err }},
		{"EvidenceCommitCandidates", func() error { _, err := a.EvidenceCommitCandidates("P1"); return err }},
		// 以下九個是 reviewer 2026-08-19 用結構分析找出來的漏網之魚——
		// RegisterMutation 實測可在 lease 釋放後把 evidence.jsonl 從 0 寫到 209 bytes。
		{"RegisterMutation", func() error { _, err := a.RegisterMutation("P1/T1", "patch"); return err }},
		{"ValidateTestCommit", func() error { return a.ValidateTestCommit("P1", "T1", "c") }},
		{"PreviewSpecCommit", func() error { _, err := a.PreviewSpecCommit(); return err }},
		{"ConfirmSpecCommit", func() error { return a.ConfirmSpecCommit(spec.CommitToken{}, "m") }},
		{"PreviewPlanCommit", func() error { _, err := a.PreviewPlanCommit(); return err }},
		{"ConfirmPlanCommit", func() error { return a.ConfirmPlanCommit(spec.CommitToken{}, "m") }},
		{"PreviewAnalysisBaseBump", func() error { _, err := a.PreviewAnalysisBaseBump("p.md", ""); return err }},
		{"ConfirmAnalysisBaseBump", func() error { _, err := a.ConfirmAnalysisBaseBump(BumpToken{}, "p.md", ""); return err }},
		{"PlanAssist", func() error { _, err := a.PlanAssist("claude", "x"); return err }},
	}
	evidenceBefore := ""
	if _, serr := os.Stat(filepath.Join(a.stateDir, "evidence", "evidence.jsonl")); serr == nil {
		evidenceBefore = fileDigest(t, filepath.Join(a.stateDir, "evidence", "evidence.jsonl"))
	}
	for _, c := range calls {
		if err := c.run(); err == nil {
			t.Errorf("%s：lease 釋放之後不得再受理 state 操作", c.name)
		}
	}
	if after := escalationDigest(t, a); after != before {
		t.Fatal("lease 釋放之後 escalation.jsonl 不得再被改寫")
	}
	evidencePath := filepath.Join(a.stateDir, "evidence", "evidence.jsonl")
	if _, serr := os.Stat(evidencePath); serr == nil {
		if fileDigest(t, evidencePath) != evidenceBefore {
			t.Fatal("lease 釋放之後 evidence.jsonl 不得再被改寫")
		}
	} else if evidenceBefore != "" {
		t.Fatal("evidence.jsonl 不該消失")
	}
	if _, serr := os.Stat(filepath.Join(a.stateDir, "gate.jsonl")); serr == nil {
		t.Fatal("lease 釋放之後不得開出 gate.jsonl（ValidateTestCommit 實測會建）")
	}
}

// TestShutdownWaitsForInFlightEscalationWrite
//
// 收尾期間**已經進行中**的 escalation 寫入必須被等待——這是 in-flight 交易那一
// 半（上一條守的是「之後不得進來」）。
//
// 兩個分得開的 oracle：
//   - 順序：寫入實際發生的那一刻，一定早於 shutdown 走完 inflight.Wait() 之後
//     的第一個步驟（stop_watchers）。
//   - lease：寫入發生的那一刻，同一個 process 重新 Acquire 必須**失敗**——也就是
//     鎖還在我們手上。reviewer 實測的正是這一格反過來的情形。
//
// 同步全部走 channel barrier，沒有 time.Sleep：寫入卡在 onWorkflowMuAcquired，
// 直到測試確認 shutdown 已經進場（reject_new_txn）才放行。
func TestShutdownWaitsForInFlightEscalationWrite(t *testing.T) {
	a, _ := newTestApp(t)
	useRealLease(t, a)
	if _, err := a.EscalationCreate("spec#1", "", "暖機：讓 journal 先存在"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	inWrite, proceed := make(chan struct{}), make(chan struct{})
	var heldDuringWrite bool
	a.onWorkflowMuAcquired = func() {
		close(inWrite)
		<-proceed
		// 這一刻正要寫 escalation.jsonl：鎖必須還在我們手上。
		heldDuringWrite = !leaseReleased(t, a.stateDir)
		record("escalation_write")
	}
	steps := make(chan string, 32)
	a.hookShutdownStep = func(s string) {
		if s == "stop_watchers" {
			record("stop_watchers")
		}
		steps <- s
	}

	createErr := make(chan error, 1)
	go func() { _, err := a.EscalationCreate("spec#2", "", "收尾期間的項目"); createErr <- err }()
	<-inWrite

	shutdownDone := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(shutdownDone) }()
	for s := range steps { // 等 shutdown 真的進場（這一步在 inflight.Wait() 之前）
		if s == "reject_new_txn" {
			break
		}
	}
	go func() { // 步驟不能塞爆，剩下的照收
		for range steps {
		}
	}()

	close(proceed)
	if err := <-createErr; err != nil {
		t.Fatalf("收尾**之前**就已經取得交易的寫入必須被完成，不得中途被拒：%v", err)
	}
	<-shutdownDone

	if !heldDuringWrite {
		t.Fatal("escalation 寫入發生的那一刻 lease 必須仍被持有——否則第二個實例已經可以進場了")
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if got != "escalation_write,stop_watchers" {
		t.Fatalf("shutdown 必須等 in-flight 的 escalation 寫入完成才往下走，實得順序：%s", got)
	}
}

// TestStateBindingsRejectedBeforeStartupCompletes
//
// startup 還沒跑完（或根本沒跑）時，state binding 一律被拒。
//
// 這是 migration blocker 競態的**結構性解法**（reviewer 2026-08-19 P1）：Wails 在
// macOS 會並行執行 OnStartup 與 bindings，先前用另一把鎖上的 latch 擋，binding 可
// 能先通過閘門、startup 隨後才發現衝突，寫入照樣落地。改成 lifecycle 之後，
// 「還沒 ready」本身就是拒絕條件，衝突發現得早或晚都不影響結果。
//
// 正題斷言：未 startup 的 app 呼叫 EscalationCreate 被拒，且 escalation.jsonl
// 根本沒被建出來。
func TestStateBindingsRejectedBeforeStartupCompletes(t *testing.T) {
	a := auditLifecycleApp(t) // 只有目錄，沒有 startup
	if _, err := a.EscalationCreate("spec#1", "", "啟動未完成就寫"); err == nil {
		t.Fatal("startup 尚未完成時不得受理 state 操作")
	} else if !strings.Contains(err.Error(), "尚未完成啟動") {
		t.Fatalf("拒絕原因必須說明是啟動未完成，實得：%v", err)
	}
	if got := escalationDigest(t, a); got != "" {
		t.Fatal("被拒的呼叫不得建立 escalation.jsonl")
	}
}

// TestStateBindingConcurrentWithStartupIsRejected
//
// 上一條的併發版：startup **正在進行中**（確定性地停在欄位發布那一刻）時，另一
// 條 goroutine 的 binding 呼叫必須被拒；startup 跑完之後同一個呼叫才放行。
//
// 兩段合起來才是完整的守門——只驗「被拒」的話，一個永遠拒絕的實作也會過。
func TestStateBindingConcurrentWithStartupIsRejected(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	paused, release := make(chan struct{}), make(chan struct{})
	a.hookStartupPublish = func() {
		close(paused)
		<-release
	}
	done := make(chan struct{})
	go func() { a.startup(context.Background()); close(done) }()
	t.Cleanup(func() { a.shutdown(context.Background()) })

	<-paused
	if _, err := a.EscalationCreate("spec#1", "", "啟動進行中"); err == nil {
		t.Fatal("startup 進行中不得受理 state 操作")
	}
	if got := escalationDigest(t, a); got != "" {
		t.Fatal("啟動進行中的呼叫不得建立 escalation.jsonl")
	}
	close(release)
	<-done

	if _, err := a.EscalationCreate("spec#1", "", "啟動完成後"); err != nil {
		t.Fatalf("startup 完成後必須放行（否則守門只是永遠拒絕）：%v", err)
	}
}

// TestShutdownRetainsLeaseWhileStartupStillRunning
//
// reviewer 2026-08-19 的重現情境：startup 已開了一部分 writer、**還沒跑到
// startupEvidence** 時，shutdown 完整跑完並釋放 lease；放行 startup 之後它繼續
// 建立 evidence/evidence.jsonl——寫入落在 lease 之外，核心不變量不成立。
//
// phase 擋得住新的 binding，擋不住 startup 自己：startup 不是 binding。所以
// startup 也要有 lifecycle ownership，處理原則沿用背景 worker——**等不到就不
// 釋放 lease**。
//
// 正題斷言：shutdown 回來之後 lease **仍被持有**（同一個 process 重新 Acquire
// 失敗）。附帶斷言：走的是保留路徑，且稽核指出是 startup 這一維。
func TestShutdownRetainsLeaseWhileStartupStillRunning(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	zero := time.Duration(0) // 收尾不等 startup：確定性地量到「等不到」那條路徑
	a.startupDrain = &zero

	paused, release := make(chan struct{}), make(chan struct{})
	a.hookStartupPublish = func() { // startup 停在 startupEvidence 之前
		close(paused)
		<-release
	}
	startupDone := make(chan struct{})
	go func() { a.startup(context.Background()); close(startupDone) }()
	<-paused

	var steps []string
	a.hookShutdownStep = func(s string) { steps = append(steps, s) }
	a.shutdown(context.Background())

	if leaseReleased(t, a.stateDir) {
		t.Fatal("startup 尚未收斂時不得釋放 ownership lease——它隨時會再建出 writer")
	}
	if !containsStep(steps, "instance_lease_retained") || containsStep(steps, "instance_lease_release") {
		t.Fatalf("必須走保留路徑，實得步驟：%v", steps)
	}
	line := findAuditLine(t, a, "instance_lease_retained")
	if !strings.Contains(line, `"startupStopped":false`) {
		t.Fatalf("稽核必須指出是 startup 這一維沒停，實得：%s", line)
	}

	// retained 結局必須**立刻**發生：不得先把 watcher／manager 收掉再保留 lease，
	// 否則晚到的 startup 會在一個半收的 app 上把 watcher 重新建起來
	// （reviewer 2026-08-19 實測 shutdown 返回後 spec=true plan=true）。
	for _, step := range []string{"stop_watchers", "snapshot", "manager_close", "registry_sync"} {
		if containsStep(steps, step) {
			t.Fatalf("startup 未收斂時不得繼續收尾流程，實得走到 %q：%v", step, steps)
		}
	}
	if a.manager != nil && a.manager.Closed() {
		t.Fatal("retained 結局不得關閉 manager——startup 仍可能用到它")
	}

	close(release)
	<-startupDone
	// 損害控制：收尾已經開始之後，startup 不再補建 evidence journal。
	if _, err := os.Stat(filepath.Join(a.stateDir, "evidence", "evidence.jsonl")); err == nil {
		t.Fatal("收尾開始之後 startup 不得再建立 evidence journal")
	}
}

// TestSecondStartupIsRefusedNotSilentlyOverwritingOwner
//
// startup lifecycle 的 ownership 只能有一個。先前每次進場都重建 startupDone，
// 兩個並行時先結束的那個會關掉 channel、把 startupRunning 設回 false，於是
// shutdown 誤判「startup 都停了」而釋放 lease——但另一個可能還在開 writer
// （reviewer 2026-08-19）。
//
// 正題斷言：第一個 startup 還卡在半途時，第二個 startup 被拒（沒有接管
// ownership），而且此刻 shutdown 仍判定 startup 未收斂 → 保留 lease。
func TestSecondStartupIsRefusedNotSilentlyOverwritingOwner(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	zero := time.Duration(0)
	a.startupDrain = &zero

	paused, release := make(chan struct{}), make(chan struct{})
	a.hookStartupPublish = func() {
		close(paused)
		<-release
	}
	firstDone := make(chan struct{})
	go func() { a.startup(context.Background()); close(firstDone) }()
	<-paused

	// 第二個 startup：必須被拒，且**不得**動到 owner 的 lifecycle 狀態。
	a.startup(context.Background())
	if !strings.Contains(a.startupErrText(), "已有另一個啟動流程") {
		t.Fatalf("第二個 startup 必須被明確拒絕並說明原因，實得橫幅：%q", a.startupErrText())
	}

	var steps []string
	a.hookShutdownStep = func(s string) { steps = append(steps, s) }
	a.shutdown(context.Background())
	if leaseReleased(t, a.stateDir) {
		t.Fatal("第一個 startup 仍在跑時不得釋放 lease——第二個 startup 不該把 owner 的收斂訊號蓋掉")
	}
	if !containsStep(steps, "instance_lease_retained") {
		t.Fatalf("必須走保留路徑，實得步驟：%v", steps)
	}

	close(release)
	<-firstDone
}

// TestStartupRefusedAfterShutdown
//
// 反向：shutdown 已經跑完之後才被呼叫的 startup，一個 writer 都不得開。
//
// 沒有這條的話，「等 startup 收斂」可以用一個永遠不進場的實作通過，而真正的
// 漏洞（收尾後才開始的 startup）沒有被守到。
func TestStartupRefusedAfterShutdown(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	a.shutdown(context.Background()) // 尚未 startup 就收尾（合法：Wails 可能直接收）

	a.startup(context.Background())

	for _, name := range []string{"audit.jsonl", "sessions.json", "events.jsonl", "evidence"} {
		if _, err := os.Stat(filepath.Join(a.stateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("收尾之後的 startup 不得開啟任何 writer，%s stat=%v", name, err)
		}
	}
	if a.manager != nil || a.registry != nil {
		t.Fatal("收尾之後的 startup 不得接上任何下游物件")
	}
}

// TestMigrationBlockerFailsClosedForEveryStateBinding
//
// 遷移無法判定權威時，**不是只有啟動序列停下來**——每一個 state binding 都要
// fail closed，而且不得在新路徑上開出任何 journal。
//
// reviewer 實測：舊版只中止 startup，GateList() 照樣把新路徑的 gate.jsonl 開出
// 來、EscalationCreate() 照樣寫得進去，等於 production 自己選了新資料那一份。
//
// 正題斷言：呼叫這些 binding 之後，新路徑上**沒有** gate.jsonl，escalation.jsonl
// 的內容也沒變。
func TestMigrationBlockerFailsClosedForEveryStateBinding(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	legacy := legacyDirOf(a)
	seedLegacyEscalation(t, legacy, "legacy-side")
	seedLegacyEscalation(t, a.stateDir, "current-side")
	before := escalationDigest(t, a)

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	calls := []struct {
		name string
		run  func() error
	}{
		{"GateList", func() error { _, err := a.GateList(); return err }},
		{"GateDecide", func() error { return a.GateDecide("id", "approve", "", nil) }},
		{"SubmitForApproval", func() error { _, err := a.SubmitForApproval(); return err }},
		{"EscalationList", func() error { _, err := a.EscalationList(); return err }},
		{"EscalationCreate", func() error { _, err := a.EscalationCreate("spec#9", "", "不該寫得進去"); return err }},
		{"EscalationAck", func() error { return a.EscalationAck("whatever") }},
		{"RunEvidence", func() error { _, err := a.RunEvidence("A1", "P1", "T1", "c", "expected_red", ""); return err }},
	}
	for _, c := range calls {
		err := c.run()
		if err == nil {
			t.Errorf("%s：遷移未完成時必須 fail closed", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "遷移未完成") {
			t.Errorf("%s：拒絕原因必須指出是遷移未完成（使用者才知道要處理什麼），實得：%v", c.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "gate.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("遷移未完成時不得在新路徑開出 gate journal，stat=%v", err)
	}
	if after := escalationDigest(t, a); after != before {
		t.Fatal("遷移未完成時不得寫入新路徑的 escalation journal")
	}
}
