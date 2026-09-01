package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/gate"
)

// ---- Gate 1 live-loop 測試基盤（temp git workspace）----

// newTestAppGit：App 綁一個新 git init'd temp workspace，本地 git config 帶
// user.name／user.email（供 GateDecide 的真實 git identity 查詢路徑；正常路徑
// 不覆寫，走 production 的 a.gitIdentity()）。
func newTestAppGit(t *testing.T) *App {
	t.Helper()
	a, _ := newTestApp(t)
	runGit(t, a, "init")
	runGit(t, a, "config", "user.name", "Test User")
	runGit(t, a, "config", "user.email", "test@example.com")
	return a
}

// newTestAppGitNoIdentity：同 newTestAppGit，但 GateDecide 的身分查詢改走
// gitIdentityOverride 回傳空值——避免依賴執行機器有無全域 git config，同時
// repo 本身仍保留 identity（commitAll 需要它才能真的 commit）。
func newTestAppGitNoIdentity(t *testing.T) *App {
	t.Helper()
	a := newTestAppGit(t)
	a.gitIdentityOverride = func() (string, string, error) { return "", "", nil }
	return a
}

func runGit(t *testing.T, a *App, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", a.workspaceDir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitAll：納管 spec/ 全部異動 add＋commit（測試用；production 的
// SpecCommit 兩階段流程屬另一 task，未在此重造）。
func commitAll(t *testing.T, a *App) {
	t.Helper()
	runGit(t, a, "add", "-A", "--", "spec/")
	runGit(t, a, "commit", "-m", "spec update")
}

func stateOf(list []GateEntryDTO, id string) string {
	for _, e := range list {
		if e.ApprovalID == id {
			return e.State
		}
	}
	return ""
}

func digestOf(t *testing.T, a *App, path string) string {
	t.Helper()
	sf, err := a.SpecRead(path)
	if err != nil {
		t.Fatal(err)
	}
	return sf.Digest
}

func TestGateLiveLoopSubmitApproveThenStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a) // 用 SpecCommit 或 helper commit 納管
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	list, _ := a.GateList()
	if stateOf(list, id) != "active" {
		t.Fatalf("want active after approve")
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a)
	list, _ = a.GateList() // detect-only 投影：讀取當下呈現 stale（不落盤，見 TestGateListDetectOnlyDoesNotPersistStale）
	if stateOf(list, id) != "stale" {
		t.Fatalf("want stale after spec change, got %s", stateOf(list, id))
	}
}

// gateOpsCount：white-box 讀 gate journal 目前 op 數（B6 Task 3 detect-only
// 迴歸樁——純讀取入口不得 append durable transition，見 B5 spec §3.2(0)）。
func gateOpsCount(t *testing.T, a *App) int {
	t.Helper()
	if a.gateJournal == nil {
		t.Fatal("gateJournal not initialized")
	}
	return len(a.gateJournal.Ops())
}

// TestGateListDetectOnlyDoesNotPersistStale：GateList（純讀取入口）呈現 stale
// 投影，但不得 append durable stale transition——durable stale 只准持
// workflowMu 的寫入路徑（gateListReconciled／reconcileLocked）產生。
func TestGateListDetectOnlyDoesNotPersistStale(t *testing.T) {
	a := newTestAppGit(t) // temp git repo workspace + wired gate.Service
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a) // 使 current manifest 與 record binding 不符

	opsBefore := gateOpsCount(t, a)
	list, err := a.GateList()
	if err != nil {
		t.Fatal(err)
	}
	if got := stateOf(list, id); got != "stale" {
		t.Fatalf("讀取應呈現 stale, got %s", got)
	}
	if got := gateOpsCount(t, a); got != opsBefore {
		t.Fatalf("GateList 不得 append：ops %d→%d", opsBefore, got)
	}
}

// TestRunEvidencePersistsStaleGateTransition：RunEvidence（B6 Task 3 保留的
// durable 寫入路徑）在 workflowMu 下呼叫 a.gateListReconciled()——這一步若
// 偵測到 stale transition 會把它 append 進 gate journal，跟 GateList 的
// detect-only 投影不同。本測試只證明「RunEvidence 這條路徑確實落盤」；
// 「只有持 workflowMu 的寫入路徑才會落盤」是另一個全稱命題，正向測試證不了，
// 要守它需要另一道結構性檢查，不在本票範圍（B5 spec §3.2(0)）。
//
// 用一個不存在的 planID 呼叫 RunEvidence：app.go 的 runEvidence 在
// workflowMu.Lock 之後立刻呼叫 gateListReconciled()（durable append 在此
// 完成），才往下查找該 planID 的 active Gate 2 approval——查無此 plan 便在
// Unlock 後回傳「no active Gate 2 approval for plan」。斷言錯誤訊息確實是
// 這個 gate2 查找失敗分支，而不是 kind／mutationID／evidenceJournal 等更早
// 的前置檢查失敗，避免測試因為別的原因提早失敗而變成 vacuous pass。
func TestRunEvidencePersistsStaleGateTransition(t *testing.T) {
	a, _ := newTestAppEvidence(t) // 沿用既有「已接好 evidenceJournal」的建構方式
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	id, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(id, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	a.SpecWrite("spec/glossary.md", "term v2", digestOf(t, a, "spec/glossary.md"))
	commitAll(t, a) // 使 current manifest 與 record binding 不符

	opsBefore := gateOpsCount(t, a)
	const missingPlanID = "no-such-plan"
	wantErr := fmt.Sprintf("evidence: no active Gate 2 approval for plan %q", missingPlanID)
	_, err = a.RunEvidence("irrelevant-approval", missingPlanID, "T1", strings.Repeat("0", 40), "expected_red", "")
	if err == nil || err.Error() != wantErr {
		t.Fatalf("want gate2 查找失敗 error %q, got %v", wantErr, err)
	}
	if got := gateOpsCount(t, a); got <= opsBefore {
		t.Fatalf("RunEvidence 應 append durable stale transition：ops %d→%d", opsBefore, got)
	}
}

func TestGateDecideRejectsWithoutGitIdentity(t *testing.T) {
	a := newTestAppGitNoIdentity(t)
	a.SpecWrite("spec/glossary.md", "x", "")
	commitAll(t, a)
	id, _ := a.SubmitForApproval()
	if err := a.GateDecide(id, "approved", "", nil); err == nil {
		t.Fatal("missing git identity must reject approval")
	}
}

// probePolicy：包裝實際 gate policy，僅在 ValidateRequest 時執行 probe——測試
// 注入的 policy，不是 App aggregate 的 ad-hoc test hook（B6 Task 3b 施工約束）。
type probePolicy struct {
	gate.GatePolicy
	probe func()
}

func (p probePolicy) ValidateRequest(req gate.GateRequest) error {
	p.probe()
	return p.GatePolicy.ValidateRequest(req)
}

// TestGateSubmitHoldsWorkflowMu：B6 Task 3b——svc.Submit 的 request append 也
// 是 gate journal append，§3.2(0) 單一寫入者不變式要求任何 append 都持
// workflowMu。用 workflowMu.TryLock() 做確定性探測（零 sleep）：probe 會在
// submitGateRequest 鎖外的 pre-validation（第一次）與 svc.Submit 內部再驗一次
// 同一 policy（第二次）各觸發一次。
//
// Task 3b follow-up erratum：原本用單一 write-once-true 布林 heldDuringSubmit
// 只要「曾經有一次持鎖」就通過，無法區分「只有第一次（pre-validation）持
// 鎖、Submit 本身沒鎖」的錯誤實作與正確實作——兩者都會讓旗標變 true。改記錄
// 完整序列並精確斷言次數與順序：第一次（app 層 pre-validation）不應持鎖、
// 第二次（svc.Submit 內部再驗）應持鎖，序列須恰為 [false true]。
func TestGateSubmitHoldsWorkflowMu(t *testing.T) {
	a := newTestAppGit(t)
	a.SpecWrite("spec/glossary.md", "term v1", "")
	commitAll(t, a)
	if _, err := a.ensureGate(); err != nil {
		t.Fatal(err)
	}

	// heldSequence 依序記錄兩次 probe 的觀測：
	// 第一次＝submitGateRequest 自己的 app 層 pre-validation（應在鎖外，false）；
	// 第二次＝svc.Submit 內部再驗同一 policy（應在新加的臨界區內，true）。
	// 單一布林值無法區分「只有第一次持鎖」（錯誤實作）與「只有第二次持鎖」
	// （正確實作）——必須斷言完整序列，見 Mutation B。
	var heldSequence []bool
	a.gateReg["gate1"] = probePolicy{GatePolicy: a.gateReg["gate1"], probe: func() {
		if a.workflowMu.TryLock() {
			a.workflowMu.Unlock()
			heldSequence = append(heldSequence, false)
			return
		}
		heldSequence = append(heldSequence, true)
	}}

	if _, err := a.SubmitForApproval(); err != nil {
		t.Fatal(err)
	}
	if len(heldSequence) != 2 || heldSequence[0] || !heldSequence[1] {
		t.Fatalf("workflowMu held-sequence across the two ValidateRequest probes = %v, want [false true]（app 層 pre-validation 不應持鎖、svc.Submit 內部再驗時應持鎖，B5 §3.2(0)）", heldSequence)
	}
}

// TestEnsureGateRegistersGate3Promotion——B6 Task 6：ensureGate() 的
// registry 必須同時證明兩件事（owner 明訂）：
//  1. "gate3_promotion" 已註冊（registry 含該 key）；
//  2. nil deps（app.go 以零值 gatepolicy.Gate3Deps{} 註冊）對 approved
//     決議 fail closed——只證明「已註冊」不夠，還要證明註冊的是真的沒
//     接線、不可能誤放行的 policy 實例。
func TestEnsureGateRegistersGate3Promotion(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.ensureGate(); err != nil {
		t.Fatal(err)
	}
	policy, ok := a.gateReg["gate3_promotion"]
	if !ok {
		t.Fatal(`registry 缺 "gate3_promotion"`)
	}
	req := gate.GateRequest{Gate: "gate3_promotion", Subject: "taskrun:01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	if _, err := policy.BuildDecision(req, "approved", gate.DecisionInput{}); err == nil ||
		!strings.Contains(err.Error(), "not wired") {
		t.Fatalf("registry 註冊的 gate3_promotion 應為 nil-deps 版本、approved 決議 fail closed 且錯誤具名：%v", err)
	}
}
