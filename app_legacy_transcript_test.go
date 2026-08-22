package main

import (
	"fmt"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// seedLegacyTranscriptFixtureApp：legacy window（無 WSID 的 3 筆事件）＋
// wsid="w1" 的 turnCount 個完整 turn；registry 的 w1 entry 依 legacyTranscript
// 旗標決定是否可能觸發 legacy 合併。
//
// event_id 刻意全部固定為 10 位數字字串（"0000000000".."0000000003"）：
// crockford ULID 字元集只有 0-9A-Z，真正的 turn event id 一律以目前時間戳的
// crockford 編碼開頭（現在的年份下恆為 "01" 開頭），比這裡固定的 "00" 開頭
// 大，藉此保證 legacy 事件在 event_id 遞增排序下恆小於任何真實 turn，
// 不必依賴檔案內實際寫入順序。
func seedLegacyTranscriptFixtureApp(t *testing.T, legacyTranscript bool, turnCount int) *App {
	t.Helper()
	dir := t.TempDir()
	const viewStart = "0000000000"
	seedEvents(t, dir,
		`{"event_id":"0000000001","provider":"claude","kind":"message","role":"user","text":"legacy-hi"}`,
		`{"event_id":"0000000002","provider":"claude","kind":"delta","role":"assistant","text":"legacy-ok"}`,
		`{"event_id":"0000000003","provider":"claude","kind":"result"}`)
	seedRegistry(t, dir, wsregistry.Entry{
		WSID: "w1", Provider: "claude", CreatedAt: "t1",
		ViewStartEventID: viewStart, LegacyTranscript: legacyTranscript,
	})
	a := newTestAppAt(t, dir)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)
	for i := 0; i < turnCount; i++ {
		emitCompleteTurn(t, a, w, fmt.Sprintf("turn-%02d", i))
	}
	// rev2 陷阱（同 Task 7）：newTestAppAt 不接 a.wsReg，loadTurnsBefore 的
	// legacy 分支要讀 a.wsReg.Get 判斷 LegacyTranscript／Provider／ViewStart，
	// 漏接會 nil panic。
	a.wsReg = registryOnDisk(t, dir)
	return a
}

// seedLegacyPlus25TurnsApp：legacy window ＋ w1 上 25 個完整 turn，用來驗證
// 跨頁分頁時 legacy window 只在最舊 turn 頁前綴一次。
func seedLegacyPlus25TurnsApp(t *testing.T) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, true, 25)
}

// seedNonLegacyApp：events.jsonl 內同樣有無 WSID 的 legacy 事件，但 w1 entry
// 的 LegacyTranscript=false——驗證反向：不得因為檔案裡剛好有無 WSID 事件就
// 誤合併進來。
func seedNonLegacyApp(t *testing.T) *App {
	t.Helper()
	return seedLegacyTranscriptFixtureApp(t, false, 5)
}

// hasLegacyText：envs 內是否含至少一筆無 WorkspaceSessionID 的事件——legacy
// window 的判準（scanLegacyWindow 只保留 WorkspaceSessionID=="" 的事件，見
// restore.go）。
func hasLegacyText(envs []contract.Envelope) bool {
	for _, e := range envs {
		if e.WorkspaceSessionID == "" {
			return true
		}
	}
	return false
}

func ascendingByEventID(envs []contract.Envelope) bool {
	for i := 1; i < len(envs); i++ {
		if envs[i].EventID < envs[i-1].EventID {
			return false
		}
	}
	return true
}

// assertAllTurnsPresent：合併後的兩頁聯集必須湊滿 want 個完整 turn（不去重
// 之外不得丟 turn）。借用既有的 countCompleteTurns（app_replayindex_test.go）
// 數 terminal state_change——legacy window 的 raw kind="result" 事件是直接
// seedEvents 寫入、未經 manager 轉換，不會被誤算進去。
func assertAllTurnsPresent(t *testing.T, envs []contract.Envelope, want int) {
	t.Helper()
	if got := countCompleteTurns(envs); got != want {
		t.Fatalf("合併後應涵蓋全部 %d 個完整 turn，實得 %d", want, got)
	}
}

func TestLoadTurnsBeforeMergesLegacyAtOldestPage(t *testing.T) {
	a := seedLegacyPlus25TurnsApp(t)
	w := "w1"
	p1, err := a.LoadTurnsBefore(w, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasLegacyText(p1) {
		t.Fatal("首載（還有更舊 turn）不得含 legacy")
	}
	cursor := p1[0].EventID
	p2, err := a.LoadTurnsBefore(w, cursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLegacyText(p2) {
		t.Fatal("最舊 turn 頁必須前綴 legacy")
	}
	if !ascendingByEventID(p2) {
		t.Fatal("合併後必須 event_id 遞增")
	}
	if p2[0].WorkspaceSessionID != "" {
		t.Fatal("legacy（無 WSID）應排在最前")
	}
	p3, err := a.LoadTurnsBefore(w, p2[0].EventID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(p3) != 0 {
		t.Fatalf("legacy cursor 應回空、停止分頁：%d", len(p3))
	}
	assertAllTurnsPresent(t, append(append([]contract.Envelope{}, p1...), p2...), 25)
}

func TestLoadTurnsBeforeNonLegacyHasNoWSIDlessEvents(t *testing.T) {
	a := seedNonLegacyApp(t)
	got, err := a.LoadTurnsBefore("w1", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.WorkspaceSessionID == "" {
			t.Fatal("非 legacy WSID 不得含任何無 WSID 事件（反向）")
		}
	}
}
