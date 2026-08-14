package wirelog

import (
	"errors"
	"os"
	"path/filepath"
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
