package replayindex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

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
	turns, err := i.RecentTurns("w1", 10)
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
	if turns, err := i.RecentTurns("w1", 5); err != nil || len(turns) != 1 {
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
	if turns, err := i.RecentTurns("w1", 5); err != nil || len(turns) != 0 {
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
	if turns, err := i.RecentTurns("w1", 5); err != nil || len(turns) != 0 {
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

	recent, err := i.RecentTurns("w1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("RecentTurns(2)：%+v", recent)
	}
	before, err := i.TurnsBefore("w1", recent[0].FirstEventID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].StartOffset != 0 {
		t.Fatalf("TurnsBefore 應回傳更早的 turn：%+v", before)
	}

	// 空字串 cursor 等同 RecentTurns（首次載入無 cursor）。
	first, err := i.TurnsBefore("w1", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].StartOffset != recent[0].StartOffset {
		t.Fatalf("空 cursor 應等同 RecentTurns：%+v", first)
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
