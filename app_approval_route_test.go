package main

import (
	"reflect"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- M3b Task 25：approval 依 WSID 路由＋FIFO promotion（§3.6.4） ----

// approvalEventFor：從 ui 已收到的 audit envelope 流裡找出 Kind==approval 且
// CorrelationID 為該 approval id 的那一筆。同一 WSID 可能同時有多筆 pending
// approval（見下方測試 id1／id3 皆屬 w1），只靠 WSID／SessionID 選不出精確的
// 一筆，必須靠 CorrelationID（Manager.EmitApprovalRequest 現在會回填 approval
// id）。registerApproval 與 EmitApprovalRequest 是 pumpApprovals 迴圈內兩個先
// 後步驟、分屬不同 goroutine 觀察，seedApproval 的 waitFor 只保證前者完成，
// 這裡另外 waitFor 涵蓋兩者之間的窗口。
func approvalEventFor(t *testing.T, ui *uiCapture, id string) contract.Envelope {
	t.Helper()
	var found contract.Envelope
	waitFor(t, "approval envelope for "+id, func() bool {
		for _, env := range ui.findEnvKind(string(contract.KindApproval)) {
			if env.CorrelationID == id {
				found = env
				return true
			}
		}
		return false
	})
	return found
}

// TestApprovalCarriesWSIDAndFIFOPromotion（§3.6.4）：approval 一律依提出請求的
// WSID 路由（多 session 下 provider 不足以定位）；多筆同時 pending 時，
// promotionOrder 必須照登記順序（FIFO）回報，不是任意 map 迭代序或依 id 字串
// 排序；每筆 approval 對外的稽核事件也要能精確歸戶到那一筆（CorrelationID），
// 而不只是歸戶到 WSID。
func TestApprovalCarriesWSIDAndFIFOPromotion(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	w1, w2 := mustCreate(t, a, "claude"), mustCreate(t, a, "claude")
	// 兩個 concurrent claude session：走 a.startClaude 直接建（同
	// TestTwoClaudeSessionsDoNotShareSocketOrMCP 的作法）——只需要 host ＋ broker，
	// 不需要整段 StartSession 交易。
	commit1, err := a.startClaude(w1, "p1", "", "")
	if err != nil {
		t.Fatalf("startClaude(w1): %v", err)
	}
	t.Cleanup(func() { commit1(false) })
	commit2, err := a.startClaude(w2, "p2", "", "")
	if err != nil {
		t.Fatalf("startClaude(w2): %v", err)
	}
	t.Cleanup(func() { commit2(false) })

	id1, id2, id3 := seedApproval(t, a, w1), seedApproval(t, a, w2), seedApproval(t, a, w1)

	if got := a.pendingByID(id1).wsid; got != w1 {
		t.Fatalf("approval 必須依 WSID 路由：%v", got)
	}
	if got := a.pendingByID(id2).wsid; got != w2 {
		t.Fatalf("approval 必須依 WSID 路由：%v", got)
	}
	if got := a.pendingByID(id3).wsid; got != w1 {
		t.Fatalf("approval 必須依 WSID 路由：%v", got)
	}

	if got := a.promotionOrder(); !reflect.DeepEqual(got, []string{id1, id2, id3}) {
		t.Fatalf("多筆待核可 FIFO promotion：%v", got)
	}

	ev1 := approvalEventFor(t, ui, id1)
	if ev1.WorkspaceSessionID != string(w1) {
		t.Fatalf("approval 事件必須帶 WSID：%+v", ev1)
	}
	ev3 := approvalEventFor(t, ui, id3)
	if ev3.WorkspaceSessionID != string(w1) {
		t.Fatalf("approval 事件必須帶 WSID：%+v", ev3)
	}
	if ev1.EventID == ev3.EventID {
		t.Fatalf("id1／id3 同屬 w1 但是兩筆不同 approval，事件不得混同：%+v vs %+v", ev1, ev3)
	}

	if err := a.ResolveApproval(id1, false, ""); err != nil {
		t.Fatalf("resolve id1: %v", err)
	}
	if got := a.promotionOrder(); !reflect.DeepEqual(got, []string{id2, id3}) {
		t.Fatalf("resolve 後 FIFO 順序必須跟著移除該筆：%v", got)
	}
	if err := a.ResolveApproval(id2, false, ""); err != nil {
		t.Fatalf("resolve id2: %v", err)
	}
	if err := a.ResolveApproval(id3, false, ""); err != nil {
		t.Fatalf("resolve id3: %v", err)
	}
}
