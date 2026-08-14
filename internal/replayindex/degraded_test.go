package replayindex

import (
	"errors"
	"fmt"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ForceWriteErrForTest 是測試專用的故障注入鉤子：設定後，Observe 底下實際
// 觸及磁碟的寫入（checkpoint boundary／turn record）一律回傳這個錯誤，藉此
// 在不真的填滿磁碟的情況下模擬「index disk full」。傳 nil 清除注入。同慣例
// 見 internal/wirelog/wirelog_test.go 的 ForceWriteErrForTest。
func (idx *Index) ForceWriteErrForTest(err error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.forceWriteErr = err
}

// userMsg：canonical user message envelope（turn 起點），供本檔測試共用。
func userMsg(wsid string, k int) contract.Envelope {
	return contract.Envelope{
		EventID:            fmt.Sprintf("%s-user-%d", wsid, k),
		Kind:               string(contract.KindMessage),
		Role:               "user",
		WorkspaceSessionID: wsid,
	}
}

// workspaceNotice：degraded 通知本身進 audit 時的事件形狀——非 canonical
// user message、非 terminal state_change，因此不會被 turn 狀態機解讀成開／
// 收 turn，單純用來驗證通知路徑不會遞迴觸發新的 index 失敗。
func workspaceNotice(wsid string) contract.Envelope {
	return contract.Envelope{
		EventID:            wsid + "-notice",
		Kind:               string(contract.KindSystemOther),
		WorkspaceSessionID: wsid,
	}
}

// receipt：offset k 對應的 AppendReceipt，供本檔測試共用。
func receipt(k int) appcore.AppendReceipt {
	off := int64(k) * 10
	return appcore.AppendReceipt{
		StartOffset: off,
		EndOffset:   off + 10,
		EventID:     fmt.Sprintf("e-%d", k),
	}
}

func TestIndexFailureDoesNotBreakAuditAndNotifiesOnce(t *testing.T) {
	var notices int
	i, _ := OpenWith(t.TempDir(), Config{Notify: func(string) { notices++ }})
	i.ForceWriteErrForTest(errors.New("index disk full"))
	for k := 0; k < 5; k++ {
		if err := i.Observe(userMsg("w1", k), receipt(k)); err != nil {
			t.Fatalf("index 失敗不得讓 provider turn 失敗：%v", err)
		}
	}
	if !i.Degraded() {
		t.Fatal("必須 latch degraded")
	}
	if notices != 1 {
		t.Fatalf("每個 degraded generation 只發一次通知：%d", notices)
	}
	if off, _ := i.Checkpoint(); off != 0 {
		t.Fatalf("degraded 期間 checkpoint 不得前移：%d", off)
	}
}

func TestNotificationEventDoesNotRecurse(t *testing.T) {
	var notices int
	var i *Index
	i, _ = OpenWith(t.TempDir(), Config{Notify: func(string) {
		notices++
		_ = i.Observe(workspaceNotice("w1"), receipt(99)) // 通知本身也進 audit
	}})
	i.ForceWriteErrForTest(errors.New("boom"))
	_ = i.Observe(userMsg("w1", 0), receipt(0))
	if notices != 1 {
		t.Fatalf("通知不得觸發遞迴：%d", notices)
	}
}

func TestLatchBeforeNotify(t *testing.T) {
	var degradedAtNotify bool
	var i *Index
	i, _ = OpenWith(t.TempDir(), Config{Notify: func(string) { degradedAtNotify = i.Degraded() }})
	i.ForceWriteErrForTest(errors.New("boom"))
	_ = i.Observe(userMsg("w1", 0), receipt(0))
	if !degradedAtNotify {
		t.Fatal("必須先 latch、後通知（§3.5.4）")
	}
}

// TestClearDegradedOpensNextGeneration：解除 latch 後，下一次失敗仍要各自
// 發一次通知——「只發一次」是 per-generation 的守衛，不是全域用過就啞掉。
func TestClearDegradedOpensNextGeneration(t *testing.T) {
	var notices int
	i, _ := OpenWith(t.TempDir(), Config{Notify: func(string) { notices++ }})
	i.ForceWriteErrForTest(errors.New("boom"))

	if err := i.Observe(userMsg("w1", 0), receipt(0)); err != nil {
		t.Fatalf("index 失敗不得讓 provider turn 失敗：%v", err)
	}
	if !i.Degraded() || notices != 1 {
		t.Fatalf("第一個 generation 應 latch 且通知一次：degraded=%v notices=%d", i.Degraded(), notices)
	}

	// 同一 generation 內再失敗一次：不得再通知。
	if err := i.Observe(userMsg("w1", 1), receipt(1)); err != nil {
		t.Fatalf("degraded 期間 Observe 必須回 nil：%v", err)
	}
	if notices != 1 {
		t.Fatalf("同一 degraded generation 內不得重複通知：%d", notices)
	}

	i.ClearDegraded()
	if i.Degraded() {
		t.Fatal("ClearDegraded 後必須不再是 degraded")
	}

	// 下一個 generation 的失敗要能再通知一次。
	i.ForceWriteErrForTest(errors.New("boom again"))
	if err := i.Observe(userMsg("w2", 0), receipt(0)); err != nil {
		t.Fatalf("index 失敗不得讓 provider turn 失敗：%v", err)
	}
	if !i.Degraded() || notices != 2 {
		t.Fatalf("下一個 generation 應各自再通知一次：degraded=%v notices=%d", i.Degraded(), notices)
	}
}
