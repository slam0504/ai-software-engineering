package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/escalation"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// ---- Task 24 測試基盤（鏡射 app_evidence_test.go／app_gate_test.go 慣例）----

// setupPendingGate2Plan：鏡射 setupApprovedEvidencePlan，但停在 gate2 送核
// （pending）——barrier／blocker 測試需要一個待核 gate2。run_test.sh 由呼叫端
// 先寫好（同 setupApprovedEvidencePlan 慣例）。
func setupPendingGate2Plan(t *testing.T, a *App, planID string) (pendingID string) {
	t.Helper()
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v1")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "baseline")

	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	headOID := revParseHead(t, a)

	writeFile(t, filepath.Join(a.workspaceDir, "plan", planID+".yaml"), evidencePlanYAML(planID, headOID))
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), testRiskPolicyYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", "T1.yaml"), "allow: []\n")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "oracle-surface.yaml"), "version: 1\npatterns: [run_test.sh]\n")
	commitAllPlan(t, a)

	id, err := a.SubmitPlanForApproval(planID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// openItemByKey：收件匣中 conditionKey 的未 resolved 項（無則 nil）。
func openItemByKey(t *testing.T, a *App, key string) *escalation.Entry {
	t.Helper()
	entries, err := a.EscalationList()
	if err != nil {
		t.Fatalf("EscalationList: %v", err)
	}
	return escalation.OpenKeyed(entries, key)
}

// mediumSel：gate2 決議的有效 risk 輸入——用 nil 會先死在 risk validator，
// 測不到 blocker（假陽性，brief 明示）。
func mediumSel() []gate.RiskSelection {
	return []gate.RiskSelection{{TaskID: "T1", SelectedRiskTier: "medium"}}
}

const redOKScript = "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n"

// ---- §3.10：blocking escalation 否決核可 ----

func TestGateDecideBlockedByEscalation(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	pendingID := setupPendingGate2Plan(t, a, "P1")

	if _, err := a.EscalationCreate("plan:P1", "gate2:P1", "人工阻擋"); err != nil {
		t.Fatal(err)
	}
	err := a.GateDecide(pendingID, "approved", "", mediumSel())
	if err == nil || !strings.Contains(err.Error(), "blocked by") {
		t.Fatalf("blocking escalation must veto approval, got %v", err) // §3.10
	}
	list, lerr := a.GateList()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if stateOf(list, pendingID) != string(gate.Pending) {
		t.Fatalf("vetoed approval must stay pending, got %s", stateOf(list, pendingID))
	}
}

// ---- §3.10：injected barrier——blocker 只能排在 decide 的臨界區之後 ----

func TestGateDecideBarrierWindowInjected(t *testing.T) {
	// 可重現的 injected barrier（無 time.Sleep、無排程競態）：三個 test seam——
	// a.decideBarrierHook：GateDecide 在 blocker 檢查後、CommitDecision 前；
	// a.onWorkflowMuAttempt：public EscalationCreate 於 mutex.Lock() 前呼叫；
	// a.onWorkflowMuAcquired：public EscalationCreate 取得 mutex 後、寫入前呼叫。
	// 順序控制：先啟動 decide 等它進窗口（持有 mutex），才啟動 blocker 並等
	// attempted 訊號——保證 blocker 的 Lock() 嘗試發生在 release 之前、必然排在
	// decide 之後。
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	pendingID := setupPendingGate2Plan(t, a, "P1")

	inWindow := make(chan struct{})  // decide 已過 blocker 檢查、持有 mutex
	release := make(chan struct{})   // 放行 CommitDecision
	attempted := make(chan struct{}) // blocker 已到達 Lock() 前
	created := make(chan error, 1)
	var stateSeenByBlocker string
	a.onWorkflowMuAttempt = func() { close(attempted) }
	a.onWorkflowMuAcquired = func() { // blocker 真正進入臨界區的時點
		list, err := a.GateList()
		if err != nil {
			t.Errorf("GateList in blocker critical section: %v", err)
			return
		}
		stateSeenByBlocker = stateOf(list, pendingID)
	}
	a.decideBarrierHook = func() { close(inWindow); <-release }

	decideDone := make(chan error, 1)
	go func() {
		decideDone <- a.GateDecide(pendingID, "approved", "", mediumSel())
	}()
	<-inWindow // decide 持有 mutex、位於窗口內
	go func() {
		_, err := a.EscalationCreate("plan:P1", "gate2:P1", "窗口內阻擋")
		created <- err
	}()
	<-attempted    // blocker 已嘗試取鎖（在 release 前）——此刻必然阻塞於 mutex
	close(release) // 放行 append
	if err := <-decideDone; err != nil {
		t.Fatalf("decide must succeed: %v", err)
	}
	if err := <-created; err != nil {
		t.Fatalf("blocker create after window: %v", err)
	}
	if stateSeenByBlocker != string(gate.Active) { // blocker 進臨界區時核可已 durable——無 TOCTOU 窗口
		t.Fatalf("blocker must observe post-append state, saw %q", stateSeenByBlocker)
	}
}

// ---- §3.8 (3)(4)：stale blocker 由修正版核可解除（時點凍結於 2b） ----

func TestStaleBlockerReleasedByReplacementApproval(t *testing.T) { // P1：stale blocker 不得永久擋住修正版
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	setupApprovedEvidencePlan(t, a, "P1") // v1：gate1＋gate2 皆核可

	// spec 變更 → gate1／gate2 stale → hard blockers（權威掃描補建）
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v2")
	commitAll(t, a)
	a.reconcileGate1NotifyOnly()
	if openItemByKey(t, a, "stale:gate1:workspace") == nil {
		t.Fatal("stale gate1 must create a workspace-scope hard blocker")
	}
	if openItemByKey(t, a, "stale:gate2:plan:P1") == nil {
		t.Fatal("stale gate2 must create a gate2-scope hard blocker")
	}

	// 修復：Gate 1 重核（其 2b 解除 stale:gate1:workspace）
	gate1ID2, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID2, "approved", "re-approve", nil); err != nil {
		t.Fatalf("gate1 re-approval must succeed (gate2-scoped blocker must not cover it): %v", err)
	}
	if openItemByKey(t, a, "stale:gate1:workspace") != nil {
		t.Fatal("gate1 re-approval must system-resolve the gate1 stale blocker")
	}

	// plan 修訂（analysis_base 換新 HEAD）＋修正版送核——送核本身不解除 blocker
	headOID2 := revParseHead(t, a)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), evidencePlanYAML("P1", headOID2))
	commitAllPlan(t, a)
	id2, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatal(err)
	}
	if openItemByKey(t, a, "stale:gate2:plan:P1") == nil {
		t.Fatal("blocker must remain until replacement approval") // 解除時點在 GateDecide 2b
	}
	if err := a.GateDecide(id2, "approved", "", mediumSel()); err != nil {
		t.Fatalf("replacement approval must succeed: %v", err) // 2b 解除 → blocker check 通過 → append
	}
	if openItemByKey(t, a, "stale:gate2:plan:P1") != nil {
		t.Fatal("replacement approval must system-resolve the stale blocker")
	}

	// 後續掃描不得復活已修復的 blocker（同 subject 已有 Active 修正版）
	a.reconcileGate1NotifyOnly()
	if openItemByKey(t, a, "stale:gate2:plan:P1") != nil {
		t.Fatal("a later reconcile must not re-create the resolved stale blocker")
	}
}

// ---- M3a.1 Task 11（spec §3.5）：stale summary 措辭凍結——引導必須建立修正版
// 重新送核，而非誤導操作者以為還原檔案內容能恢復舊核可 ----

func TestStaleEscalationSummaryGuidesReplacementResubmission(t *testing.T) {
	a := newTestAppGit(t)
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
	commitAll(t, a)
	a.reconcileGate1NotifyOnly() // 權威掃描：stale → hard item

	item := openItemByKey(t, a, "stale:gate1:workspace")
	if item == nil {
		t.Fatal("stale reconcile must create a hard escalation item")
	}
	if !strings.Contains(item.Item.Summary, "必須建立修正版並重新送核") {
		t.Fatalf("stale summary must instruct creating a replacement and resubmitting, got %q", item.Item.Summary)
	}
	if !strings.Contains(item.Item.Summary, "還原檔案內容不會讓舊核可恢復生效") {
		t.Fatalf("stale summary must warn that reverting file content does not revive the old approval, got %q", item.Item.Summary)
	}
}

// ---- §3.8：hard 項不可手動 resolve；ack 不解除 block ----

func TestHardEscalationNotManuallyResolvable(t *testing.T) {
	a := newTestAppGit(t)
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
	commitAll(t, a)
	a.reconcileGate1NotifyOnly() // 權威掃描：stale → hard item

	item := openItemByKey(t, a, "stale:gate1:workspace")
	if item == nil {
		t.Fatal("stale reconcile must create a hard escalation item")
	}
	if !item.Item.Hard {
		t.Fatal("stale item must be hard")
	}
	if err := a.EscalationResolve(item.Item.EscalationID, "accepted_risk", "想跳過"); err == nil {
		t.Fatal("hard item must not be user-resolvable") // §3.8
	}
	if err := a.EscalationAck(item.Item.EscalationID); err != nil {
		t.Fatal(err)
	}
	if openItemByKey(t, a, "stale:gate1:workspace") == nil {
		t.Fatal("acknowledged must not release the block (§3.8)")
	}
}

// ---- §3.8 (5)(6)＋A8：evidence-error 由同 key 新 run 成功自動解除 ----

func TestEvidenceErrorAutoResolvedByRerun(t *testing.T) { // A8 閉環
	a, _ := newTestAppEvidence(t)
	// 編譯失敗 fixture：exit != 0 且輸出無 matcher（FAIL）→ result=error
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'compile err: syntax'\nexit 2\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1")
	approvalID := activeApprovalIDFor(t, a, "P1")

	id1, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "expected_red", "")
	if err != nil {
		t.Fatalf("RunEvidence (error fixture): %v", err)
	}
	run1, err := a.EvidenceGet(id1)
	if err != nil {
		t.Fatal(err)
	}
	if run1.Result != "error" {
		t.Fatalf("fixture must classify as error, got %q (%q)", run1.Result, run1.ObservedFailure)
	}
	item := openItemByKey(t, a, "evidence-error:P1/T1/expected_red")
	if item == nil {
		t.Fatal("error run must open an evidence-error item")
	}
	if item.Item.Hard {
		t.Fatal("evidence-error item must be hard=false (auto-resolvable by a passing rerun)")
	}

	// 修復後重跑成功（新 commit 只動 oracle 檔，lineage 合法）
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	runGit(t, a, "add", "run_test.sh")
	runGit(t, a, "commit", "-m", "fix test script")
	fixedCommit := revParseHead(t, a)
	id2, err := a.RunEvidence(approvalID, "P1", "T1", fixedCommit, "expected_red", "")
	if err != nil {
		t.Fatalf("RunEvidence (fixed): %v", err)
	}
	run2, err := a.EvidenceGet(id2)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Result != "passed" {
		t.Fatalf("fixed rerun must pass, got %q (%q)", run2.Result, run2.ObservedFailure)
	}
	if openItemByKey(t, a, "evidence-error:P1/T1/expected_red") != nil {
		t.Fatal("successful rerun must system-resolve the error item")
	}

	// 再壞一次：resolved 不復活——同 key 開新項、occurrence 遞增
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), "#!/bin/sh\necho 'compile err again'\nexit 2\n")
	runGit(t, a, "add", "run_test.sh")
	runGit(t, a, "commit", "-m", "break test script again")
	if _, err := a.RunEvidence(approvalID, "P1", "T1", revParseHead(t, a), "expected_red", ""); err != nil {
		t.Fatal(err)
	}
	item2 := openItemByKey(t, a, "evidence-error:P1/T1/expected_red")
	if item2 == nil {
		t.Fatal("second error run must open a new item under the same key")
	}
	if item2.Item.EscalationID == item.Item.EscalationID {
		t.Fatal("resolved item must not be revived — a new occurrence must mint a new id")
	}
	if item2.Item.Occurrence != 2 {
		t.Fatalf("Occurrence = %d, want 2", item2.Item.Occurrence)
	}
}

// ---- §3.8 (5) 環境錯誤子類（review Medium）：command 無法啟動 ----

func TestEvidenceStartFailureOpensEscalationItem(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	// committed plan 的 test_contract command 指向不存在的 executable——
	// runCommand 的 cmd.Start() 失敗屬 runner-level error，不產 EvidenceRun。
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v1")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "baseline")
	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	headOID := revParseHead(t, a)
	badPlan := strings.ReplaceAll(evidencePlanYAML("P1", headOID),
		"executable: sh", "executable: /nonexistent-task24-binary")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), badPlan)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), testRiskPolicyYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", "T1.yaml"), "allow: []\n")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "oracle-surface.yaml"), "version: 1\npatterns: [run_test.sh]\n")
	commitAllPlan(t, a)
	planCommit1 := revParseHead(t, a)
	gate2ID, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate2ID, "approved", "ok", mediumSel()); err != nil {
		t.Fatal(err)
	}

	if _, err := a.RunEvidence(gate2ID, "P1", "T1", planCommit1, "expected_red", ""); err == nil {
		t.Fatal("RunEvidence must fail when the contract command cannot even start")
	}
	item := openItemByKey(t, a, "evidence-error:P1/T1/expected_red")
	if item == nil {
		t.Fatal("a runner-level start failure must open an evidence-error item (§3.8 (5) 環境錯誤)")
	}
	if item.Item.Hard {
		t.Fatal("evidence-error item must be hard=false")
	}

	// 修復：plan 修正 executable → gate2 修正版核可 → 同 key 新 run passed 解除（A8）
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), evidencePlanYAML("P1", headOID))
	commitAllPlan(t, a)
	planCommit2 := revParseHead(t, a)
	gate2ID2, err := a.SubmitPlanForApproval("P1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate2ID2, "approved", "ok", mediumSel()); err != nil {
		t.Fatalf("replacement gate2 approval must succeed (evidence-error is tca-scoped, not gate2-scoped): %v", err)
	}
	id, err := a.RunEvidence(gate2ID2, "P1", "T1", planCommit2, "expected_red", "")
	if err != nil {
		t.Fatalf("RunEvidence (fixed contract): %v", err)
	}
	run, err := a.EvidenceGet(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "passed" {
		t.Fatalf("fixed rerun must pass, got %q (%q)", run.Result, run.ObservedFailure)
	}
	if openItemByKey(t, a, "evidence-error:P1/T1/expected_red") != nil {
		t.Fatal("a passing rerun under the same key must system-resolve the start-failure item")
	}
}

// ---- §3.8 (1)：risk-unclassifiable 的開啟與權威修復 ----

func TestRiskUnclassifiableEscalationLifecycle(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v1")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "baseline")
	gate1ID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID, "approved", "ok", nil); err != nil {
		t.Fatal(err)
	}
	headOID := revParseHead(t, a)

	// minimum_risk_tier 與 policy 重算不符（default_tier=medium，宣告 low）
	badPlan := strings.ReplaceAll(evidencePlanYAML("P1", headOID),
		"minimum_risk_tier: medium", "minimum_risk_tier: low")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), badPlan)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "risk-policy.yaml"), testRiskPolicyYAML)
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "permissions", "T1.yaml"), "allow: []\n")
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "oracle-surface.yaml"), "version: 1\npatterns: [run_test.sh]\n")
	commitAllPlan(t, a)
	if _, err := a.SubmitPlanForApproval("P1"); err == nil {
		t.Fatal("plan with an unrecomputable minimum tier must fail validation")
	}
	if openItemByKey(t, a, "risk-unclassifiable:P1") == nil {
		t.Fatal("risk classification failure must open a risk-unclassifiable item")
	}

	// 修復：修正版 plan commit 通過 plan.Validate → 系統解除
	writeFile(t, filepath.Join(a.workspaceDir, "plan", "P1.yaml"), evidencePlanYAML("P1", headOID))
	commitAllPlan(t, a)
	if _, err := a.SubmitPlanForApproval("P1"); err != nil {
		t.Fatalf("fixed plan must submit: %v", err)
	}
	if openItemByKey(t, a, "risk-unclassifiable:P1") != nil {
		t.Fatal("a validating replacement plan must system-resolve the risk-unclassifiable item")
	}
}

// ---- §3.10 fail closed：escalation journal 寫入失敗 → 核可被拒 ----

func TestEscalationJournalWriteFailureRejectsApproval(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), redOKScript)
	setupApprovedEvidencePlan(t, a, "P1")

	// spec 變更 → stale blockers（escalation journal 此刻仍健康）
	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v2")
	commitAll(t, a)
	a.reconcileGate1NotifyOnly()
	if openItemByKey(t, a, "stale:gate1:workspace") == nil {
		t.Fatal("stale gate1 blocker must exist before the write-failure injection")
	}
	gate1ID2, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}

	// 模擬 escalation journal 寫入失敗：關掉 underlying file——之後任何 append
	// 都會失敗（並讓 journal 進 degraded）。2b 的 stale 修復解除寫不進去時，
	// Gate 必須拒絕核可（§3.10 fail closed）。
	if err := a.escJournal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.GateDecide(gate1ID2, "approved", "re-approve", nil); err == nil {
		t.Fatal("approval must be rejected when the escalation journal write fails (§3.10)")
	}
	list, lerr := a.GateList()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if stateOf(list, gate1ID2) != string(gate.Pending) {
		t.Fatalf("rejected-by-write-failure approval must stay pending, got %s", stateOf(list, gate1ID2))
	}
}

// ---- §3.8：收件匣不可用（Project 失敗）→ 標不可用＋核可 fail closed ----

func TestEscalationUnavailableFailsClosed(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	// 預埋 schema 違規行（合法 JSON、未知 _type）——ensureEscalation 首次開啟
	// 就載入它，之後任何 Project 必失敗。
	// escalation journal 綁受 ownership lease 保護的 a.stateDir（見 ensureGate 的
	// doc）——不是 workspaceDir/.workbench，兩者在 tmp fallback 下不同值。
	writeFile(t, filepath.Join(a.stateDir, "escalation.jsonl"), `{"_type":"bogus"}`+"\n")

	writeFile(t, filepath.Join(a.workspaceDir, "spec", "glossary.md"), "term v1")
	runGit(t, a, "add", "-A")
	runGit(t, a, "commit", "-m", "baseline")
	svc, err := a.ensureGate()
	if err != nil {
		t.Fatal(err)
	}
	// 直接經 gate.Service 造 pending（app 的 submit 包裝會先撞收件匣錯誤——
	// 這裡要驗的是「已存在的 pending 在收件匣不可用時不得被核可」）。
	manifestDigest, baseCommit, err := spec.BuildCommittedSnapshot(a.specRepo)
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.Submit("gate1", "workspace", gate1Bindings(manifestDigest, baseCommit))
	if err != nil {
		t.Fatal(err)
	}

	if _, lerr := a.EscalationList(); lerr == nil {
		t.Fatal("EscalationList must fail loud when the journal cannot be projected — never an empty inbox")
	}
	if derr := a.GateDecide(id, "approved", "", nil); derr == nil {
		t.Fatal("approval must be rejected while the escalation inbox is unavailable (fail closed)")
	}
}
