package replayindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// readCheckpointOffset：直接解析 checkpoint.json 的 offset 欄位（package 內
// 測試可直接用 checkpointFile，不必另建 exported 讀取路徑）。
func readCheckpointOffset(t *testing.T, path string) int64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cf checkpointFile
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	return cf.Offset
}

// feedUserMsg：canonical user message（turn 起點）。
func feedUserMsg(t *testing.T, i *Index, wsid string, off int64) {
	t.Helper()
	id := fmt.Sprintf("%s-user-%d", wsid, off)
	env := contract.Envelope{
		EventID: id, Kind: string(contract.KindMessage), Role: "user",
		WorkspaceSessionID: wsid,
	}
	receipt := appcore.AppendReceipt{StartOffset: off, EndOffset: off + 10, EventID: id}
	if err := i.Observe(env, receipt); err != nil {
		t.Fatalf("feedUserMsg: %v", err)
	}
}

// feed：任意 kind／state 的事件，不預設 role。
func feed(t *testing.T, i *Index, wsid, kind, state string, off int64) {
	t.Helper()
	id := fmt.Sprintf("%s-%s-%d", wsid, kind, off)
	env := contract.Envelope{
		EventID: id, Kind: kind, State: state, WorkspaceSessionID: wsid,
	}
	receipt := appcore.AppendReceipt{StartOffset: off, EndOffset: off + 10, EventID: id}
	if err := i.Observe(env, receipt); err != nil {
		t.Fatalf("feed(%s): %v", kind, err)
	}
}

// feedCompleteTurn：一個完整 turn（user → delta → result → state_change=done），
// 自 base 起連續佔用 4 個 10-byte 事件。
func feedCompleteTurn(t *testing.T, i *Index, wsid string, base int64) {
	t.Helper()
	feedUserMsg(t, i, wsid, base)
	feed(t, i, wsid, string(contract.KindDelta), "", base+10)
	feed(t, i, wsid, string(contract.KindResult), "", base+20)
	feed(t, i, wsid, string(contract.KindStateChange), string(contract.StateDone), base+30)
}

func TestTurnBoundaryDefinition(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	obs := func(kind, role, state, id string, off int64) {
		t.Helper()
		if err := i.Observe(contract.Envelope{EventID: id, Kind: kind, Role: role, State: state,
			WorkspaceSessionID: "w1"},
			appcore.AppendReceipt{StartOffset: off, EndOffset: off + 10, EventID: id}); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	obs("init", "system", "", "e1", 0)              // 無 canonical user message → 不成 turn
	obs("message", "user", "", "e2", 10)             // turn 起
	obs("delta", "assistant", "", "e3", 20)
	obs("result", "system", "", "e4", 30)
	obs("state_change", "system", "done", "e5", 40) // terminal：turn 止
	turns, err := i.recentTurns("w1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("init 不得被猜成一個 turn：%d", len(turns))
	}
	if turns[0].StartOffset != 10 || turns[0].EndOffset != 50 ||
		turns[0].FirstEventID != "e2" || turns[0].LastEventID != "e5" {
		t.Fatalf("turn 範圍／首末 event ID 錯：%+v", turns[0])
	}
}

func TestStreamErrorClosesTurnAsFailed(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedUserMsg(t, i, "w1", 0)
	feed(t, i, "w1", string(contract.KindStreamError), "", 10)
	feed(t, i, "w1", string(contract.KindStateChange), string(contract.StateFailed), 20)
	if turns, err := i.recentTurns("w1", 5); err != nil || len(turns) != 1 {
		t.Fatalf("failed turn 也算完整 turn：%d %v", len(turns), err)
	}
}

func TestOpenTurnNotIndexed(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedUserMsg(t, i, "w1", 0)
	feed(t, i, "w1", string(contract.KindDelta), "", 10)
	if turns, err := i.recentTurns("w1", 5); err != nil || len(turns) != 0 {
		t.Fatalf("未完成 turn 不得入 index（§3.5.8）：%d %v", len(turns), err)
	}
	if off, ok := i.OpenTurnStart("w1"); !ok || off != 0 {
		t.Fatalf("open_turn_start_offset 必須保存：%d %v", off, ok)
	}
}

func TestSessionDoneIsNotATurn(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feed(t, i, "w1", "session_done", "", 0)
	if turns, err := i.recentTurns("w1", 5); err != nil || len(turns) != 0 {
		t.Fatalf("session:done 不單獨構成 turn：%d %v", len(turns), err)
	}
}

func TestPerWSIDFilesAreSeparate(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	feedCompleteTurn(t, i, "w1", 0)
	feedCompleteTurn(t, i, "w2", 100)
	for _, w := range []string{"w1", "w2"} {
		if _, err := os.Stat(filepath.Join(dir, w+".turns.jsonl")); err != nil {
			t.Fatalf("每 WSID 一個 index 檔：%v", err)
		}
	}
}

// 以下兩則測試涵蓋 brief 未逐字列出、但本 task Interfaces 明列的
// TurnsBefore／checkpoint open_turn_start_offset 跨 Open 存續。

func TestTurnsBeforePagination(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedCompleteTurn(t, i, "w1", 0)
	feedCompleteTurn(t, i, "w1", 100)
	feedCompleteTurn(t, i, "w1", 200)

	recent, err := i.recentTurns("w1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("recentTurns(2)：%+v", recent)
	}
	before, _, err := i.TurnsBefore("w1", recent[0].FirstEventID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].StartOffset != 0 {
		t.Fatalf("TurnsBefore 應回傳更早的 turn：%+v", before)
	}

	// 空字串 cursor 等同尾端視窗（首次載入無 cursor）。
	first, _, err := i.TurnsBefore("w1", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].StartOffset != recent[0].StartOffset {
		t.Fatalf("空 cursor 應等同尾端視窗：%+v", first)
	}
}

// newTestIndexWithTurns：開一個 Index、灌入 wsid 的 n 個完整 turn，供
// TurnsBefore 分頁／hasOlder 測試使用。
func newTestIndexWithTurns(t *testing.T, wsid string, n int) *Index {
	t.Helper()
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < n; k++ {
		feedCompleteTurn(t, i, wsid, int64(k*40))
	}
	return i
}

// TestTurnsBeforeReportsHasOlder：hasOlder 表示此頁回傳之後還有更舊的 turn
// （可分頁總數 > 回傳數）。25 個 turn 首載 20 個應 hasOlder=true；剩下 5 個
// 全部回傳後應 hasOlder=false。
func TestTurnsBeforeReportsHasOlder(t *testing.T) {
	idx := newTestIndexWithTurns(t, "w1", 25)
	recs, hasOlder, err := idx.TurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 20 || !hasOlder {
		t.Fatalf("25 turn 首載 20 個、應 hasOlder=true：n=%d older=%v", len(recs), hasOlder)
	}
	oldestFirst := recs[0].FirstEventID
	recs2, hasOlder2, err := idx.TurnsBefore("w1", oldestFirst, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs2) != 5 || hasOlder2 {
		t.Fatalf("剩 5 turn、應 hasOlder=false：n=%d older=%v", len(recs2), hasOlder2)
	}
}

// TestCheckpointOnlyPersistsAtTurnBoundary：mutation 驗證——非終止事件
// （delta 等）不得觸發 checkpoint.json 落盤（內容與 mtime 皆不變），只有
// turn boundary（開 turn／收 turn）才落盤。記憶體中的 Checkpoint() 不受此
// 節流影響，逐事件即時更新。
func TestCheckpointOnlyPersistsAtTurnBoundary(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "checkpoint.json")

	feedUserMsg(t, i, "w1", 0) // 開 turn：boundary，必落盤
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	for k := 0; k < 5; k++ {
		feed(t, i, "w1", string(contract.KindDelta), "", int64(10+k*10))
	}
	afterDeltas, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterDeltasInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterDeltas) != string(before) {
		t.Fatalf("非終止事件不得改動 checkpoint 檔內容：\nbefore=%s\nafter=%s", before, afterDeltas)
	}
	if !afterDeltasInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("非終止事件不得改動 checkpoint 檔 mtime：before=%v after=%v", beforeInfo.ModTime(), afterDeltasInfo.ModTime())
	}
	if off, _ := i.checkpoint(); off != 60 {
		t.Fatalf("記憶體 checkpoint 應逐事件即時更新、不受落盤節流影響：%d", off)
	}

	feed(t, i, "w1", string(contract.KindStateChange), string(contract.StateDone), 60) // 收 turn：boundary，必落盤
	afterClose, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterClose) == string(before) {
		t.Fatal("turn 收尾必須落盤 checkpoint")
	}
}

// TestFlushPersistsCurrentOffset：mutation 驗證——正常關閉不該依賴 crash
// 修復機制補 checkpoint。開一個 turn（此時磁碟落盤），餵幾個 delta（節流下
// 磁碟停在原地、記憶體已領先），呼叫 Flush()，斷言磁碟已追上記憶體的即時
// offset；並驗證 Flush 冪等（無新事件時重複呼叫，內容不再變動）。
func TestFlushPersistsCurrentOffset(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "checkpoint.json")

	feedUserMsg(t, i, "w1", 0) // boundary：磁碟落盤 offset=10
	staleOnDisk := readCheckpointOffset(t, path)

	for k := 0; k < 3; k++ {
		feed(t, i, "w1", string(contract.KindDelta), "", int64(10+k*10))
	}
	if got := readCheckpointOffset(t, path); got != staleOnDisk {
		t.Fatalf("前置條件錯：非終止事件不應改動磁碟，got=%d want=%d", got, staleOnDisk)
	}
	memOff, _ := i.checkpoint()
	if memOff == staleOnDisk {
		t.Fatal("前置條件錯：記憶體 offset 應已領先磁碟，否則本測試無法區分 Flush 有沒有生效")
	}

	if err := i.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := readCheckpointOffset(t, path); got != memOff {
		t.Fatalf("Flush 後磁碟必須反映即時 offset：got=%d want=%d", got, memOff)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Flush(); err != nil {
		t.Fatalf("Flush 必須冪等（無新事件時重複呼叫不得報錯）：%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("無新事件時重複 Flush 內容應不變：before=%s after=%s", before, after)
	}
}

func TestOpenTurnStartSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	i1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	feedUserMsg(t, i1, "w1", 0) // turn 開了但沒收尾

	i2, err := OpenWith(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	off, ok := i2.OpenTurnStart("w1")
	if !ok || off != 0 {
		t.Fatalf("checkpoint 的 open_turn_start_offset 必須跨 Open 存續：%d %v", off, ok)
	}
}

// ---- 測試專用讀取／驅動入口（M3b owner review 修正 3 的裁決）----
//
// 這三個存取器原本是 Index 的 **exported** 方法，機械掃描抓到它們 production
// 呼叫端為零。逐一對照 spec 之後的結論是**都不是缺口**：
//
//   - `RecentTurns`：§3.5.1「從檔尾反向讀最近 20 筆」／§3.8「各載入最近 20 個
//     完整 turn」確實是凍結需求，但 production 是由 `TurnsBefore(wsid, "", n)`
//     滿足的（app.LoadTurnsBefore → TurnsBefore ＋ OpenTurnStart，見
//     rebuild_orchestrator.go）。RecentTurns 只是 cursor 為空時的同義入口，
//     多一條公開 API 而已。
//   - `Checkpoint`：§3.5.5 要求的是 checkpoint 落盤與啟動驗證，那由
//     loadCheckpoint／flushCheckpoint 與內部欄位完成；這個 getter 沒有需求。
//   - `ClearDegraded`：§3.5.4「解除 latch 以成功重建為準」由 rebuild.go 的
//     `clearDegradedLocked()` 完成（finalCatchUpAndAttach 已持鎖）。exported
//     的無鎖包裝只有測試在用。
//
// 因此三者都是**沒有需求支撐的公開 API**，本票移出 production 檔；觀測價值仍
// 在（~25 個測試呼叫點），依 package 既有慣例（見 runtime_rebuild_test.go 的
// CatchUpAttemptsForTest）以未匯出存取器留在 _test.go。

// recentTurns：wsid 最近（檔尾）至多 n 筆完整 turn，時間遞增排列。
// 等同 TurnsBefore(wsid, "", n)，測試用的簡寫。
func (idx *Index) recentTurns(wsid string, n int) ([]TurnRecord, error) {
	recs, _, err := idx.TurnsBefore(wsid, "", n)
	return recs, err
}

// checkpoint：目前已索引的 audit byte offset 與最後一筆處理過的 event id。
func (idx *Index) checkpoint() (int64, string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.checkpointOffset, idx.checkpointLastEventID
}

// clearDegraded：解除 degraded latch（production 走 clearDegradedLocked，
// 呼叫端已持鎖；這裡補上取鎖，供測試直接驅動）。
func (idx *Index) clearDegraded() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.clearDegradedLocked()
}
