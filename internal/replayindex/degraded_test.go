package replayindex

import (
	"errors"
	"fmt"
	"path/filepath"
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

// ---- unverified latch（owner review 2026-08-17）----
//
// 下面兩條各自釘住一條 durable 寫入路徑。**刻意不合成一條**：App 層的跨重啟
// 守門測試（app_replayindex_test.go 的
// TestRegistryLoadFailureDoesNotFossilizeIndexGapAcrossRestart）只斷言得到兩
// 條路徑的**連集**——只要其中一條仍是 no-op，磁碟 checkpoint 就不會前移，那條
// 測試照樣綠。要讓「Observe 不前移」與「Flush 不落盤」各自被 mutation 打紅，
// 必須分開量。

// TestUnverifiedObserveDoesNotTouchCheckpoint：unverified 期間 Observe 是空操
// 作——記憶體 checkpointOffset 不前移、boundary 事件也不寫 checkpoint.json。
// 這是缺口固化的第一步：前移之後那個 offset 會是 audit 的合法行邊界、event id
// 也對得上，下次啟動的 checkpointTrustedLocked 反而判定「可信」，它之前那段從
// 未索引的 audit 從此沒有任何人會補。
func TestUnverifiedObserveDoesNotTouchCheckpoint(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.MarkUnverified()

	// canonical user message 是 boundary 事件：沒有 latch 的話它會同時前移記憶
	// 體 checkpoint **並且**落盤。
	feedUserMsg(t, i, "w1", 500)

	if off, id := i.Checkpoint(); off != 0 || id != "" {
		t.Fatalf("unverified 期間 Observe 不得前移 checkpoint：off=%d id=%q", off, id)
	}
	if off := readCheckpointOffset(t, filepath.Join(dir, "checkpoint.json")); off != 0 {
		t.Fatalf("unverified 期間 Observe 不得寫 checkpoint.json：磁碟 offset=%d", off)
	}
	if _, open := i.OpenTurnStart("w1"); open {
		t.Fatal("unverified 期間連 turn 狀態機都不該跑（Observe 全程空操作）")
	}
}

// TestUnverifiedFlushDoesNotPersistCheckpoint：unverified 期間 Flush 不落盤。
//
// 這條與上一條是**不同的失效點**：checkpoint 落盤被節流到 turn boundary，所以
// 記憶體值可以合法地跑在磁碟前面（這裡用一個 non-boundary 事件造出這個常態差
// 距），而 shutdown 的 Flush 正是把記憶體值推上磁碟的那一步。latch 之後它必須
// 停手——沒對過帳的值一旦落盤，下次啟動就是拿它去驗證，缺口同樣被固化。
func TestUnverifiedFlushDoesNotPersistCheckpoint(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "checkpoint.json")
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	feedUserMsg(t, i, "w1", 0)                           // boundary：磁碟 checkpoint 前進到 10
	feed(t, i, "w1", string(contract.KindDelta), "", 10) // non-boundary：只有記憶體到 20
	if off := readCheckpointOffset(t, cp); off != 10 {
		t.Fatalf("前置條件：磁碟 checkpoint 應停在上一個 boundary（10），實際 %d", off)
	}
	if off, _ := i.Checkpoint(); off != 20 {
		t.Fatalf("前置條件：記憶體 checkpoint 應已跑在磁碟前面（20），實際 %d", off)
	}

	i.MarkUnverified()
	if err := i.Flush(); err != nil {
		t.Fatalf("Flush 在 unverified 下是 no-op，不該回錯：%v", err)
	}
	if off := readCheckpointOffset(t, cp); off != 10 {
		t.Fatalf("unverified 期間 Flush 不得落盤：磁碟 offset 被推到 %d", off)
	}
}

// TestUnverifiedIsNotDegraded：兩個 latch 語意不同，不得互相冒充——degraded 的
// 記憶體 checkpoint 是「最後一次成功索引到哪」的忠實快照（落盤安全、可續掃重
// 建），unverified 則是「根本沒對過帳」。若把 unverified 實作成 latch
// degraded，Observe 確實會停，但 Flush 照樣落盤（degraded 不擋 Flush），等於
// 用一個可能是 0 的值覆寫磁碟上原本可能還好的 checkpoint，比不修更糟。
func TestUnverifiedIsNotDegraded(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	i.MarkUnverified()
	if !i.Unverified() {
		t.Fatal("MarkUnverified 之後 Unverified() 必須為 true")
	}
	if i.Degraded() {
		t.Fatal("unverified 不得順手 latch degraded：兩者語意不同，見 unverified 欄位 doc")
	}
	// degraded 的解除入口不該把 unverified 一起解掉（它只能靠重啟後的
	// VerifyOrRebuild 在新的 Index 實例上解除）。
	i.ClearDegraded()
	if !i.Unverified() {
		t.Fatal("ClearDegraded 不得解除 unverified latch")
	}
}
