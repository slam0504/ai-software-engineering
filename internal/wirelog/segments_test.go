package wirelog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// TestSegmentsSpanTwoGenerations：同一個 WSID 可以在 app-server restart（新
// generation／新 wire_log_id）之後跨 generation 延續，For 依 Append 順序回傳
// （§3.4.4）。也驗證 For(w1) 不混入 w2 的 frame range。
func TestSegmentsSpanTwoGenerations(t *testing.T) {
	set := NewSegmentSet()
	if err := set.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 10}); err != nil {
		t.Fatal(err)
	}
	if err := set.Append("w2", SegmentRef{WireLogID: "g1", StartFrame: 11, EndFrame: 20}); err != nil {
		t.Fatal(err)
	}
	if err := set.Append("w1", SegmentRef{WireLogID: "g2", StartFrame: 1, EndFrame: 5}); err != nil {
		t.Fatal(err)
	}

	got := set.For("w1")
	if len(got) != 2 || got[0].WireLogID != "g1" || got[1].WireLogID != "g2" {
		t.Fatalf("同 WSID 必須跨 generation 有序延續：%+v", got)
	}
	for _, r := range got {
		if r.WireLogID == "g1" && (r.StartFrame < 1 || r.EndFrame > 10) {
			t.Fatalf("混入他 session 的 frame：%+v", r)
		}
	}
}

// TestForDoesNotMixOtherSessionFrames：直接驗證 For(w1) 從不回傳屬於 w2 的
// SegmentRef，不只是靠 frame range 邊界間接推論。
func TestForDoesNotMixOtherSessionFrames(t *testing.T) {
	set := NewSegmentSet()
	_ = set.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 10})
	_ = set.Append("w2", SegmentRef{WireLogID: "g1", StartFrame: 11, EndFrame: 20})

	got := set.For("w1")
	if len(got) != 1 || got[0] != (SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 10}) {
		t.Fatalf("For(w1) 不得含 w2 的 segment：%+v", got)
	}
	if got2 := set.For("w2"); len(got2) != 1 || got2[0].StartFrame != 11 {
		t.Fatalf("For(w2) 錯誤：%+v", got2)
	}
}

// TestForReturnsCopy：呼叫端改了回傳值不得影響內部狀態（Task 7 同類問題）。
func TestForReturnsCopy(t *testing.T) {
	set := NewSegmentSet()
	_ = set.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 2})
	got := set.For("w1")
	got[0].EndFrame = 999
	if set.For("w1")[0].EndFrame != 2 {
		t.Fatal("For 必須回傳副本")
	}
}

// TestForUnknownWSIDReturnsNil：從未 Append 過的 wsid，For 回傳 nil（非
// panic、非空 map 產生的零值 slice 混淆)。
func TestForUnknownWSIDReturnsNil(t *testing.T) {
	set := NewSegmentSet()
	if got := set.For("nope"); got != nil {
		t.Fatalf("未知 wsid 應回傳 nil，got %+v", got)
	}
}

// ---- 持久化：OpenSegmentSet（coordinator Task 10 review 裁決） ----

// TestOpenSegmentSetPersistsAndReloadsAfterRestart：落盤後重啟（新的
// OpenSegmentSet 呼叫）必須還原出跟 crash 前一致的 ordered segments——
// §3.4.4 的 []SegmentRef 是唯一 durable 的 session 級錄流證據，不能只活在記憶體。
func TestOpenSegmentSetPersistsAndReloadsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segments.jsonl")

	s1, err := OpenSegmentSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Append("w1", SegmentRef{WireLogID: "g2", StartFrame: 1, EndFrame: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenSegmentSet(path) // 模擬重啟：重新載入
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	got := s2.For("w1")
	if len(got) != 2 || got[0].WireLogID != "g1" || got[1].WireLogID != "g2" {
		t.Fatalf("重啟後必須還原出一致的 ordered segments：%+v", got)
	}
}

// TestOpenSegmentSetTailTruncationTolerated：比照 Task 10 RebuildFrameIndex
// 與 internal/journal 的分級——檔尾（最後一行）不完整是 crash 最典型的後果，
// 必須容忍、回傳有效前綴，不得整份失敗。
func TestOpenSegmentSetTailTruncationTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segments.jsonl")
	good := `{"wsid":"w1","wire_log_id":"g1","start_frame":1,"end_frame":10}` + "\n"
	torn := `{"wsid":"w1","wire_log_id":"g2","start_frame":1,"end_frame":5` // 截斷，無結尾
	if err := os.WriteFile(path, []byte(good+torn), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenSegmentSet(path)
	if err != nil {
		t.Fatalf("檔尾截斷必須容忍，不應整份失敗：%v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got := s.For("w1")
	if len(got) != 1 || got[0].WireLogID != "g1" {
		t.Fatalf("有效前綴必須被保留：%+v", got)
	}
}

// TestOpenSegmentSetMidFileCorruptionFailsLoud：壞行之後還有有效行＝中段損
// 壞，不是 crash 的典型後果，必須 fail loud。
func TestOpenSegmentSetMidFileCorruptionFailsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segments.jsonl")
	content := `{"wsid":"w1","wire_log_id":"g1","start_frame":1,"end_frame":10}` + "\n" +
		`not-json-garbage` + "\n" +
		`{"wsid":"w1","wire_log_id":"g2","start_frame":1,"end_frame":5}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenSegmentSet(path); err == nil {
		t.Fatal("中段損壞必須 fail loud，不得跳過或整份丟棄")
	}
}

// TestOpenSegmentSetMissingWSIDFailsLoud：語法合法 JSON 但缺 wsid，屬於本
// package 層級的損壞（journal 層只驗語法），必須 fail loud，比照
// internal/evidence.OpenJournal 對未知 record 形狀的處置。
func TestOpenSegmentSetMissingWSIDFailsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segments.jsonl")
	content := `{"wire_log_id":"g1","start_frame":1,"end_frame":10}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenSegmentSet(path); err == nil {
		t.Fatal("缺 wsid 的 record 必須 fail loud")
	}
}

// TestSegmentSetAppendLatchesDegraded：持久化寫入失敗後 latch degraded，後續
// Append 一律拒絕（不得靜默降級成純記憶體），比照 internal/evidence 與
// internal/gate 的 journal-backed 型別。
func TestSegmentSetAppendLatchesDegraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segments.jsonl")
	s, err := OpenSegmentSet(path)
	if err != nil {
		t.Fatal(err)
	}
	// 關掉底層檔案 handle，模擬下一次寫入失敗（journal 套件測試同款手法）。
	if err := s.j.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 1}); err == nil {
		t.Fatal("底層寫入失敗必須回錯")
	}
	if !s.Degraded() {
		t.Fatal("寫入失敗後必須 latch degraded")
	}
	if err := s.Append("w1", SegmentRef{WireLogID: "g1", StartFrame: 2, EndFrame: 2}); !errors.Is(err, ErrSegmentSetDegraded) {
		t.Fatalf("degraded 後續 Append 必須拒絕並回 ErrSegmentSetDegraded，got %v", err)
	}
	if got := s.For("w1"); len(got) != 0 {
		t.Fatalf("失敗的 Append 不得留下部分內存狀態：%+v", got)
	}
}

// TestSegmentSetAppendRejectsEmptyWSID：空 wsid 沒有意義（無法回答「這段錄
// 流屬於哪個 session」），必須直接拒絕，不落盤、不進記憶體。
func TestSegmentSetAppendRejectsEmptyWSID(t *testing.T) {
	set := NewSegmentSet()
	if err := set.Append("", SegmentRef{WireLogID: "g1", StartFrame: 1, EndFrame: 1}); err == nil {
		t.Fatal("空 wsid 必須拒絕")
	}
}

// TestAppendConcurrentSafe 對應 Task 8 前例（Generation.Line 因「seq 在鎖內配
// 發、寫檔在鎖外」被抓到，見 wirelog_test.go 的 TestLineConcurrentSafe）：Append
// 同樣是「持久化寫入＋記憶體更新」需要落在同一段鎖內的操作，卻沒有專門的併發測
// 試跟上——本 task 其餘測試都是循序呼叫，只是順帶跑過 -race，沒有真的施壓。
//
// 多 goroutine 併發 Append，同時涵蓋同一 WSID（多個 writer 互相競爭同一個
// key）與各自獨立的 WSID（測併發 map 寫入不同 key）。用「in-memory 的 live For()
// 順序」對照「Close 後重新 OpenSegmentSet 從 journal replay 出的順序」是否完全
// 一致，取代 TestLineConcurrentSafe 那種「frame 編號嚴格遞增」的做法——SegmentSet
// 沒有 frame 編號可驗，但若記憶體更新與落盤沒有落在同一段鎖內，「哪個呼叫先進
// 記憶體」跟「哪個呼叫先落盤」可能不是同一個勝出者，兩份順序就會分岔，這正是
// Task 8 那個 bug 換了個面貌（記憶體 vs 磁碟，而非 frame 編號 vs 寫入）。
func TestAppendConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "segments.jsonl")
	s, err := OpenSegmentSet(path)
	if err != nil {
		t.Fatal(err)
	}

	// goroutines/perGoroutine 刻意壓小：journal.Append 每次都會 fsync，量大只是拉長
	// 測試時間，這條測試要證明的是「有並行寫入同一個 journal」而非量大（40 次
	// Append 已足夠讓 race detector／順序斷言有機會抓到鎖範圍縮小）。
	const goroutines = 4
	const perGoroutine = 10 // 偶數，一半進 "shared"、一半進各自獨立的 wsid
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				n := i*perGoroutine + j // 全域唯一，可用來核對有沒有遺失／重複
				wsid := fmt.Sprintf("w-%d", i)
				if j%2 == 0 {
					wsid = "shared"
				}
				ref := SegmentRef{WireLogID: "g1", StartFrame: n, EndFrame: n}
				if err := s.Append(wsid, ref); err != nil {
					t.Errorf("Append: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	liveShared := s.For("shared")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSegmentSet(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	diskShared := reopened.For("shared")

	if !reflect.DeepEqual(liveShared, diskShared) {
		t.Fatalf("同一 WSID 的 live 順序與 disk replay 順序不一致（記憶體更新與落盤沒有落在同一段鎖內）：\nlive=%+v\ndisk=%+v", liveShared, diskShared)
	}

	wantStart := map[int]bool{}
	for i := 0; i < goroutines; i++ {
		for j := 0; j < perGoroutine; j += 2 {
			wantStart[i*perGoroutine+j] = false
		}
	}
	if len(liveShared) != len(wantStart) {
		t.Fatalf("shared wsid 的 segment 數=%d，want %d（並行寫入下遺失或多寫）", len(liveShared), len(wantStart))
	}
	for _, ref := range liveShared {
		seen, ok := wantStart[ref.StartFrame]
		if !ok {
			t.Fatalf("非預期的 StartFrame=%d 出現在 shared wsid", ref.StartFrame)
		}
		if seen {
			t.Fatalf("StartFrame=%d 在 shared wsid 重複出現", ref.StartFrame)
		}
		wantStart[ref.StartFrame] = true
	}
	for start, seen := range wantStart {
		if !seen {
			t.Fatalf("StartFrame=%d 遺失", start)
		}
	}

	for i := 0; i < goroutines; i++ {
		got := reopened.For(fmt.Sprintf("w-%d", i))
		if len(got) != perGoroutine/2 {
			t.Fatalf("wsid=w-%d 的 segment 數=%d，want %d", i, len(got), perGoroutine/2)
		}
	}
}
