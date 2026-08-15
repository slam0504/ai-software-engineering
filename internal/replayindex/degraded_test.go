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

// TestNotificationEventDoesNotRecurse：Notify callback 內再呼叫 Observe，注
// 入的事件必須是「真的會走到寫入路徑」的 canonical user message（開
// turn）——用一個 turn 狀態機在分類階段就會直接忽略的事件（例如
// system_other）測不出「先通知、後 latch」寫反的問題：那種事件不論 latch
// 是否已生效都不會呼叫 writeCheckpointFile／appendTurnRecord，遞迴永遠沒有
// 機會級聯，測試會恆真通過（re-review 2026-08-15 指出的缺陷，brief 原版即
// 是這個弱化寫法）。這裡改用同樣會觸發開 turn 的 userMsg，讓遞迴真的有機會
// 撞上 forceWriteErr 再次失敗、再次嘗試通知，才真正驗證到 latch 必須先於
// Notify 生效這件事。
func TestNotificationEventDoesNotRecurse(t *testing.T) {
	var notices int
	var i *Index
	i, _ = OpenWith(t.TempDir(), Config{Notify: func(string) {
		notices++
		_ = i.Observe(userMsg("w1", 1), receipt(1)) // 通知本身也進 audit，且會嘗試開 turn
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

// TestOpenTurnFailureRollsBackTurnState：mutation 驗證（re-review 2026-08-15
// Important finding）——開 turn 時 writeCheckpointFile 失敗，不能只回滾
// checkpointOffset，連 turns[wsid] 的 open/startOffset/firstEventID 也要一起
// 回滾。ClearDegraded 只重置 degraded／degradedErr／degradedNotified 三個欄
// 位，不會動 turns map；若開 turn失敗那次留下 turns[wsid].open==true，latch
// 解除後同一個 wsid 再送 canonical user message，狀態機會誤判成「已經在
// turn 中」而直接忽略（!st.open 條件不成立），該 turn 就永遠不會入 index。
func TestOpenTurnFailureRollsBackTurnState(t *testing.T) {
	i, _ := OpenWith(t.TempDir(), Config{})
	i.ForceWriteErrForTest(errors.New("boom"))

	feedUserMsg(t, i, "w1", 0)
	if !i.Degraded() {
		t.Fatal("必須 latch degraded")
	}
	if _, ok := i.OpenTurnStart("w1"); ok {
		t.Fatal("開 turn 失敗必須連同 turns map 一起回滾，不能留下已開的 turn")
	}

	i.ForceWriteErrForTest(nil)
	i.ClearDegraded()

	// 回滾若沒做，這裡 st.open 仍是 true，狀態機會把下面這則 canonical user
	// message 直接忽略，永遠不會開新 turn。
	feedUserMsg(t, i, "w1", 100)
	if off, ok := i.OpenTurnStart("w1"); !ok || off != 100 {
		t.Fatalf("回滾後應能重新開 turn：off=%d ok=%v", off, ok)
	}

	feed(t, i, "w1", string(contract.KindStateChange), string(contract.StateDone), 110)
	turns, err := i.RecentTurns("w1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].StartOffset != 100 {
		t.Fatalf("回滾後的 turn 應能正常收尾入 index：%+v", turns)
	}
}
