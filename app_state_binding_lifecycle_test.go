package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
	}
	for _, c := range calls {
		if err := c.run(); err == nil {
			t.Errorf("%s：lease 釋放之後不得再受理 state 操作", c.name)
		}
	}
	if after := escalationDigest(t, a); after != before {
		t.Fatal("lease 釋放之後 escalation.jsonl 不得再被改寫")
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
