package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// seedInterruptedTurn：一個「上次執行到一半就死掉」的 workspace——w1 有
// canonical user message ＋ delta，沒有任何 terminal state_change（§3.2.3 的
// stuck busy 情境）。registry 以已遷移狀態帶上 w1。
func seedInterruptedTurn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","workspace_session_id":"w1","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"delta","role":"assistant","workspace_session_id":"w1","text":"par"}`)
	seedRegistry(t, dir, wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	return dir
}

// readEvents：events.jsonl 的全部 envelope（malformed 行會讓測試直接失敗——
// 修復序列寫出來的東西必須每一行都可解析）。
func readEvents(t *testing.T, stateDir string) []contract.Envelope {
	t.Helper()
	f, err := os.Open(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer f.Close()
	var out []contract.Envelope
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e contract.Envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("events.jsonl 有無法解析的行：%q", sc.Text())
		}
		out = append(out, e)
	}
	return out
}

func tail(evs []contract.Envelope, n int) []contract.Envelope {
	if len(evs) <= n {
		return evs
	}
	return evs[len(evs)-n:]
}

// TestStartupOrderIsFrozen：§3.2.4 的順序是凍結契約。特別是 index_verify 必
// 須早於 detect_incomplete（偵測來源是 index 的 open turn 狀態，沒對齊就問不
// 出正確答案），而 open_ui 必須排最後。
func TestStartupOrderIsFrozen(t *testing.T) {
	a := newTestAppAt(t, seedInterruptedTurn(t))
	var order []string
	a.hookStartupStep = func(s string) { order = append(order, s) }
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	want := []string{"registry_load", "migrate", "restore_dormant",
		"index_verify", "detect_incomplete", "emit_stream_error", "open_ui"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("啟動修復順序不符 §3.2.4：\n got=%v\nwant=%v", order, want)
	}
}

// TestStartupRepairEmitsStreamErrorThenFailed：§3.2.3——未完成 turn 補一筆帶
// WSID 的 stream_error，由 reducer 追發 state_change=failed 結束該 turn。
func TestStartupRepairEmitsStreamErrorThenFailed(t *testing.T) {
	dir := seedInterruptedTurn(t)
	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	ev := readEvents(t, dir)
	if n := len(ev); n < 2 ||
		ev[n-2].Kind != "stream_error" || ev[n-2].WorkspaceSessionID != "w1" ||
		ev[n-1].Kind != "state_change" || ev[n-1].State != "failed" ||
		ev[n-1].WorkspaceSessionID != "w1" {
		t.Fatalf("末二筆應為 stream_error → state_change=failed：%+v", tail(ev, 3))
	}
	if ev[len(ev)-2].Error != errAppRestartInterrupted.Error() {
		t.Fatalf("修復事件必須可辨識為 app restart 修復：%q", ev[len(ev)-2].Error)
	}
}

// TestStartupRepairIsIdempotent：crash 於修復中途、重啟重跑不得疊加
// （§3.2.3 明文要求）。第二個 App 走完整段序列後事件數必須完全不變。
func TestStartupRepairIsIdempotent(t *testing.T) {
	dir := seedInterruptedTurn(t)
	a1 := newTestAppAt(t, dir)
	if _, err := a1.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	first := readEvents(t, dir)
	a2 := newTestAppAt(t, dir) // 模擬 crash 後重跑
	if _, err := a2.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if second := readEvents(t, dir); len(second) != len(first) {
		t.Fatalf("重跑必須冪等，事件數 %d → %d", len(first), len(second))
	}
}

// TestStartupRepairIsIdempotentAfterIndexLoss：上一條走的是「index 還在、
// checkpoint 也還在」的順風情境。真正的 crash 可能連 index 都不可信（或整個
// 被清掉），此時冪等性只能來自 audit 本身——VerifyOrRebuild 重掃 events.jsonl
// 之後必須看到那個 turn 已經被 failed 收掉，不得再補一次。
func TestStartupRepairIsIdempotentAfterIndexLoss(t *testing.T) {
	dir := seedInterruptedTurn(t)
	a1 := newTestAppAt(t, dir)
	if _, err := a1.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	first := readEvents(t, dir)
	if err := os.RemoveAll(filepath.Join(dir, "replay-index")); err != nil {
		t.Fatal(err)
	}
	a2 := newTestAppAt(t, dir)
	if _, err := a2.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if second := readEvents(t, dir); len(second) != len(first) {
		t.Fatalf("index 全失後重跑仍須冪等（權威是 audit），事件數 %d → %d", len(first), len(second))
	}
}

// TestUIOpensOnlyAfterRepair：§3.2.4 的最後一條——修復完才開放 UI 與 provider
// 啟動。若順序顛倒，使用者送出的新 turn 會與修復事件交錯。
func TestUIOpensOnlyAfterRepair(t *testing.T) {
	a := newTestAppAt(t, seedInterruptedTurn(t))
	var uiOpenedAt, repairedAt int
	a.hookStartupStep = func(s string) {
		switch s {
		case "emit_stream_error":
			repairedAt = len(readEvents(t, a.stateDir))
		case "open_ui":
			uiOpenedAt = len(readEvents(t, a.stateDir))
		}
	}
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if uiOpenedAt <= repairedAt {
		t.Fatalf("必須修復完才開放 UI 與 provider 啟動（§3.2.4）：open_ui=%d repair=%d",
			uiOpenedAt, repairedAt)
	}
}

// TestCompleteTurnIsNotRepaired：反向護欄——上次乾淨收尾的 session 不得被補上
// 一筆憑空的 stream_error（那會讓使用者看到不存在的失敗）。
func TestCompleteTurnIsNotRepaired(t *testing.T) {
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","workspace_session_id":"w1","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"state_change","workspace_session_id":"w1","state":"done"}`)
	seedRegistry(t, dir, wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})

	a := newTestAppAt(t, dir)
	var steps []string
	a.hookStartupStep = func(s string) { steps = append(steps, s) }
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	if got := len(readEvents(t, dir)); got != 2 {
		t.Fatalf("已完成的 turn 不得被修復：事件數 %d", got)
	}
	for _, s := range steps {
		if s == "emit_stream_error" {
			t.Fatal("沒有未完成 turn 時不得走到 emit_stream_error")
		}
	}
}

// TestRepairCoversEveryIncompleteWSID：多 session 工作區的常態是好幾個 WSID
// 同時各有一個 open turn——修復必須逐個補，不能只處理第一個。
func TestRepairCoversEveryIncompleteWSID(t *testing.T) {
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","workspace_session_id":"w1","text":"a"}`,
		`{"event_id":"ev-2","provider":"codex","kind":"message","role":"user","workspace_session_id":"w2","text":"b"}`,
		`{"event_id":"ev-3","provider":"claude","kind":"message","role":"user","workspace_session_id":"w3","text":"c"}`,
		`{"event_id":"ev-4","provider":"claude","kind":"state_change","workspace_session_id":"w3","state":"done"}`)
	seedRegistry(t, dir,
		wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"},
		wsregistry.Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"},
		wsregistry.Entry{WSID: "w3", Provider: "claude", CreatedAt: "t"})

	a := newTestAppAt(t, dir)
	if _, err := a.restoreSessions(); err != nil {
		t.Fatal(err)
	}
	repaired := map[string]int{}
	for _, e := range readEvents(t, dir) {
		if e.Kind == "stream_error" && e.Error == errAppRestartInterrupted.Error() {
			repaired[e.WorkspaceSessionID]++
		}
	}
	if repaired["w1"] != 1 || repaired["w2"] != 1 {
		t.Fatalf("每個未完成 turn 的 WSID 都要恰好修復一次：%+v", repaired)
	}
	if repaired["w3"] != 0 {
		t.Fatalf("已完成的 w3 不得被修復：%+v", repaired)
	}
	// provider 必須跟著該 WSID 的 slot 走（Manager 會對不符的 provider fail
	// loud，這裡直接驗輸出）。
	for _, e := range readEvents(t, dir) {
		if e.Kind == "stream_error" && e.WorkspaceSessionID == "w2" && e.Provider != "codex" {
			t.Fatalf("修復事件的 provider 必須是該 session 的 provider：%+v", e)
		}
	}
}
