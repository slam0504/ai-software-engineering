// app_freeze_test.go——B6 Task 9：approval freeze latch 的 App 側（B5 spec
// §4.2(2)(3)）。white-box in-package（package main）：owner 2026-09-02 裁示，
// 走 mustCreate＋registerApproval 直接注入可觀測、可失敗的 resolver，不採
// seedApproval（額外引入 host／socket／broker／goroutine／timeout，讓
// mutation 的失敗來源不再單一）。

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
)

// resolveArgs：resolve stub 收到的一次呼叫參數，供斷言 drain／resolveApproval
// 真的把 allow／reason 傳到底——原範本只驗 pending 已清空，測不到 p.resolve
// 有沒有被正確呼叫（B 型斷言太弱）。
type resolveArgs struct {
	allow  bool
	reason string
}

// newResolveStub 建立一個記錄呼叫參數的 pendingApproval.resolve stub；forceErr
// 非 nil 時每次呼叫都回傳該錯誤（模擬 best-effort resolve 失敗）。calls 指標
// 讓呼叫端事後檢視完整呼叫序列。
func newResolveStub(forceErr error) (resolve func(bool, string) error, calls *[]resolveArgs) {
	var got []resolveArgs
	return func(allow bool, reason string) error {
		got = append(got, resolveArgs{allow: allow, reason: reason})
		return forceErr
	}, &got
}

// startSessionWithPendingApproval：建立 committed WSID（mustCreate），並透過
// registerApproval 直接注入一筆可觀測、可失敗的 pending approval。
func startSessionWithPendingApproval(t *testing.T, a *App, id string, forceErr error) (appcore.WSID, *[]resolveArgs) {
	t.Helper()
	w := mustCreate(t, a, "claude")
	resolve, calls := newResolveStub(forceErr)
	a.registerApproval(id, w, "claude", resolve)
	return w, calls
}

// assertNoApprovalDeadlock：在 goroutine 跑一個會取 apprMu 的操作，2 秒逾時未
// 完成視為鎖洩漏。單純比對回傳值測不到死鎖，必須用逾時偵測。
func assertNoApprovalDeadlock(t *testing.T, op func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { op(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("apprMu 疑似洩漏：後續 approval 操作卡死")
	}
}

// TestFreezeImplementationSessionDrainsAndBlocksAllow（B5 §4.2(2)(3)）：freeze
// 對目標 WSID 既有 pending 做原子 drain——map／apprOrder 同步移除，且真的以
// allow=false＋正確 reason 呼叫 resolve；freeze 之後對同一 WSID 新註冊的
// pending，allow 被擋、deny 仍合法、未知 id 維持既有 not-found 形狀不變。
func TestFreezeImplementationSessionDrainsAndBlocksAllow(t *testing.T) {
	a, _ := newTestApp(t)
	w, calls := startSessionWithPendingApproval(t, a, "appr-1", nil)

	a.workflowMu.Lock()
	err := a.freezeImplementationSession(w, "taskrun stale")
	a.workflowMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("pending_removed_from_map", func(t *testing.T) { // mutation #3
		if pa := a.pendingByID("appr-1"); pa != nil {
			t.Fatalf("pending 應已 drain：%+v", pa)
		}
	})

	t.Run("appr_order_synced", func(t *testing.T) { // mutation #4
		for _, id := range a.promotionOrder() {
			if id == "appr-1" {
				t.Fatal("drain 後 apprOrder 仍含已清空的 id（apprOrder 未同步）")
			}
		}
	})

	t.Run("resolve_called_with_deny_and_reason", func(t *testing.T) { // mutation #5
		if len(*calls) != 1 {
			t.Fatalf("drain 必須恰好呼叫一次 resolve，got %d 次", len(*calls))
		}
		if got := (*calls)[0]; got.allow != false || got.reason != "taskrun stale" {
			t.Fatalf("drain 必須以 allow=false、reason=taskrun stale 呼叫 resolve，got %+v", got)
		}
	})

	t.Run("late_pending_allow_blocked_deny_legal", func(t *testing.T) { // mutation #1
		// owner 2026-09-02 裁定：本 subtest 改用獨立的 App／committed WSID／
		// freeze setup，不沿用外層共用 a／w。mutation #9（resolveApproval
		// 的 frozen-allow 早退分支漏 apprMu.Unlock()）命中時，本 subtest
		// 第一次 allow 呼叫會讓 apprMu 永久鎖住；若沿用外層共用 a，鎖會繼
		// 續污染同一個 App 上排在它之後的 unknown_id_not_found_unchanged
		// subtest（該 subtest 的 resolveApproval 呼叫沒有逾時保護），整條
		// 測試指令只會卡死撞外層 go test timeout。改成獨立 App 後，即使
		// 本 subtest 自身因 mutation #9 結構性連帶紅，洩漏的鎖也只留在這
		// 個 subtest 私有的 App 實例上，不會波及任何其他 subtest 或測試。
		fa, _ := newTestApp(t)
		fw := mustCreate(t, fa, "claude")
		fa.workflowMu.Lock()
		err := fa.freezeImplementationSession(fw, "taskrun stale")
		fa.workflowMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}

		lateResolve, lateCalls := newResolveStub(nil)
		fa.registerApproval("appr-2-late", fw, "claude", lateResolve)
		if err := fa.resolveApproval("appr-2-late", true, ""); err == nil {
			t.Fatal("frozen 後 allow 應拒絕")
		}
		if len(*lateCalls) != 0 {
			t.Fatal("allow 被拒絕時不應呼叫 resolve")
		}

		// mutation #9 命中的是與 TestFrozenAllowLeavesLockAndPendingUsable
		// 相同的早退分支缺陷：deny 呼叫在鎖洩漏時會對 apprMu.Lock() 永久
		// 阻塞，須用逾時偵測而非直接呼叫；完成後才檢查回傳錯誤（owner
		// 2026-09-02 裁定第 2 點）。
		var denyErr error
		assertNoApprovalDeadlock(t, func() {
			denyErr = fa.resolveApproval("appr-2-late", false, "user deny")
		})
		if denyErr != nil {
			t.Fatalf("同一筆 pending 的 deny 仍合法：%v", denyErr)
		}
		if got := (*lateCalls)[0]; got.allow != false || got.reason != "user deny" {
			t.Fatalf("deny 呼叫參數未正確傳遞：%+v", got)
		}
	})

	t.Run("unknown_id_not_found_unchanged", func(t *testing.T) {
		if err := a.resolveApproval("no-such-id", true, ""); err == nil ||
			!strings.Contains(err.Error(), "no pending approval") {
			t.Fatalf("未知 approval 應回既有 not-found 錯誤形狀，got %v", err)
		}
	})
}

// TestFreezeOnlyDrainsTargetWSID（B5 §4.2(2)）：drain 只能動到目標 WSID——用
// 未凍結的第二個 WSID 當對照組，證明 freeze 不會波及其他 session 的 pending。
func TestFreezeOnlyDrainsTargetWSID(t *testing.T) {
	a, _ := newTestApp(t)
	w1, calls1 := startSessionWithPendingApproval(t, a, "appr-w1", nil)
	_, calls2 := startSessionWithPendingApproval(t, a, "appr-w2", nil)

	a.workflowMu.Lock()
	err := a.freezeImplementationSession(w1, "taskrun stale")
	a.workflowMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// mutation #2：drain 迴圈的 `p.wsid != w` 過濾被拿掉——對照組的 pending
	// 也被誤 drain。
	if pa := a.pendingByID("appr-w2"); pa == nil {
		t.Fatal("未凍結的對照組 WSID 的 pending 不應被 drain")
	}
	if len(*calls2) != 0 {
		t.Fatal("未凍結的對照組 WSID 的 pending 不應被 resolve")
	}
	// 目標 WSID 仍照常被 drain（證明本測試量到的是「隔離」而非「freeze 整
	// 個失效」）。
	if pa := a.pendingByID("appr-w1"); pa != nil {
		t.Fatal("目標 WSID 的 pending 應已 drain")
	}
	if len(*calls1) != 1 {
		t.Fatalf("目標 WSID 應被 resolve 一次，got %d", len(*calls1))
	}
}

// TestResolveApprovalDenyAlwaysLegalWhenFrozen（B5 §4.2(3)）：freeze latch
// 僅阻擋 allow=true；deny 永遠合法、不受旗標阻擋。
func TestResolveApprovalDenyAlwaysLegalWhenFrozen(t *testing.T) {
	a, _ := newTestApp(t)
	w, _ := startSessionWithPendingApproval(t, a, "appr-1", nil)

	a.workflowMu.Lock()
	err := a.freezeImplementationSession(w, "stale")
	a.workflowMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	resolve, calls := newResolveStub(nil)
	a.registerApproval("appr-late", w, "claude", resolve)

	// mutation #8：`if allow && a.apprFrozen[p.wsid]` 的 `&&` 被改成 `||`——
	// deny（allow=false）在 frozen WSID 上也會被誤擋。
	if err := a.resolveApproval("appr-late", false, "user deny"); err != nil {
		t.Fatalf("deny 永遠合法（B5 §4.2(3)）：%v", err)
	}
	if len(*calls) != 1 || (*calls)[0].allow != false {
		t.Fatalf("deny 未正確送達 resolve：%+v", *calls)
	}
}

// TestFrozenAllowLeavesLockAndPendingUsable（B5 §4.2(3)）：frozen-allow 分支
// 拒絕時（1）不移除 pending——同筆之後仍可 deny；（2）apprMu 不得洩漏。
func TestFrozenAllowLeavesLockAndPendingUsable(t *testing.T) {
	a, _ := newTestApp(t)
	w, _ := startSessionWithPendingApproval(t, a, "appr-1", nil)

	a.workflowMu.Lock()
	_ = a.freezeImplementationSession(w, "stale")
	a.workflowMu.Unlock()

	resolve, calls := newResolveStub(nil)
	a.registerApproval("appr-x", w, "claude", resolve)
	if err := a.resolveApproval("appr-x", true, ""); err == nil {
		t.Fatal("frozen allow 應拒絕")
	}
	if len(*calls) != 0 {
		t.Fatal("allow 被拒絕時不應呼叫 resolve")
	}

	// mutation #9：frozen-allow 分支漏 Unlock()——apprMu 洩漏，任何後續
	// approval 操作（含同筆 deny）都會卡死。用 goroutine＋逾時偵測，避免鎖
	// 真的洩漏時整條測試卡死到全域 test timeout 才失敗。
	var denyErr error
	assertNoApprovalDeadlock(t, func() {
		denyErr = a.resolveApproval("appr-x", false, "later deny")
	})
	if denyErr != nil {
		t.Fatalf("同筆 deny 應成功：%v", denyErr)
	}
}

// TestResolveApprovalNotFoundDoesNotLeakLock：not-found 分支同樣須解鎖——與
// frozen-allow 分支是不同的早退路徑，個別可能各自漏 Unlock，須分開驗證。
func TestResolveApprovalNotFoundDoesNotLeakLock(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude") // 先建好 WSID：mustCreate 會呼叫 t.Fatalf，
	// *testing.T 的 Fatal／FailNow 只能從跑該測試的 goroutine 呼叫，不能擺進下面
	// 的逾時保護 goroutine 內。

	if err := a.resolveApproval("never-registered", true, ""); err == nil {
		t.Fatal("未知 approval 應回 not-found 錯誤")
	}

	// mutation #7：resolveApproval 的 not-found 分支漏 a.apprMu.Unlock()——
	// apprMu 洩漏，後續任何取 apprMu 的操作（含 registerApproval 本身）都會
	// 卡死。整段都要包進逾時偵測，不能只包最後一次 resolveApproval——否則
	// 鎖真的洩漏時，卡死會先發生在 registerApproval，逾時偵測完全量不到。
	// registerApproval 不呼叫 t.Fatal，可安全在背景 goroutine 執行。
	resolve, calls := newResolveStub(nil)
	assertNoApprovalDeadlock(t, func() {
		a.registerApproval("appr-1", w, "claude", resolve)
		_ = a.resolveApproval("appr-1", false, "ok")
	})
	if len(*calls) != 1 {
		t.Fatal("鎖未洩漏時，deny 應正常送達 resolve")
	}
}

// TestFreezeDrainBestEffortFailureIsJoinedWithoutRollback（B5 §4.2(2)：deny
// 契約——不可回滾＝旗標＋drain；resolve(false) best-effort，失敗合併回傳但
// 不解除 freeze、不放回 pending）。
func TestFreezeDrainBestEffortFailureIsJoinedWithoutRollback(t *testing.T) {
	a, _ := newTestApp(t)
	injected := errors.New("resolve failed: injected test failure")
	w, calls := startSessionWithPendingApproval(t, a, "appr-1", injected)

	a.workflowMu.Lock()
	err := a.freezeImplementationSession(w, "taskrun stale")
	a.workflowMu.Unlock()

	// mutation #6：errors.Join(errs...) 被改成永遠回 nil——best-effort 失敗
	// 被吞掉。
	if err == nil || !strings.Contains(err.Error(), "injected test failure") {
		t.Fatalf("best-effort resolve 失敗必須合併回傳，got %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("即使失敗，drain 仍必須嘗試呼叫一次 resolve，got %d", len(*calls))
	}

	// 不可回滾：drain（map／apprOrder 已清空）與旗標設定即使 resolve 失敗也
	// 不得回滾——pending 不得被放回、allow 仍必須被擋。
	if pa := a.pendingByID("appr-1"); pa != nil {
		t.Fatal("resolve 失敗不得讓 pending 被放回 map")
	}
	resolve2, calls2 := newResolveStub(nil)
	a.registerApproval("appr-2", w, "claude", resolve2)
	if err := a.resolveApproval("appr-2", true, ""); err == nil {
		t.Fatal("resolve 失敗不得讓旗標被回滾——同 WSID 後續 allow 仍應被擋")
	}
	if len(*calls2) != 0 {
		t.Fatal("allow 被擋時不應呼叫 resolve")
	}
}
