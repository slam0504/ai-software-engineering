package wirelog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

// ForceWriteErrForTest 是測試專用的故障注入鉤子：下一次（且僅下一次意義上——
// 呼叫端可重複設值）Line 的實際寫入會回傳這個錯誤，藉此在不真的填滿磁碟的情況
// 下模擬「disk full」等寫入失敗。傳 nil 清除注入，但已經 latch 進 g.writeErr 的
// 錯誤不會因此清除（§3.4.6）。
func (g *Generation) ForceWriteErrForTest(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forceErr = err
}

func TestFrameKeyNeedsDirection(t *testing.T) {
	g, _ := NewGeneration(t.TempDir(), "g1", nil)
	_ = g.Line(DirClientToServer, []byte(`{"id":7,"method":"thread/start"}`))
	_ = g.Line(DirServerToClient, []byte(`{"id":7,"method":"approval/request"}`))
	idx := g.FrameIndex()
	c2s := idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirClientToServer, RequestID: "7"})
	s2c := idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirServerToClient, RequestID: "7"})
	if len(c2s) != 1 || len(s2c) != 1 || c2s[0] == s2c[0] {
		t.Fatal("同 requestID 不同 direction 必須可區分（§3.4.3）")
	}
}

func TestUnattributedFrameStillWritten(t *testing.T) {
	dir := t.TempDir()
	g, _ := NewGeneration(dir, "g1", nil)
	_ = g.Line(DirServerToClient, []byte(`{"unknown":"no-thread"}`))
	_ = g.Finalize(recorder.Meta{Provider: "codex"})
	b, _ := os.ReadFile(filepath.Join(dir, "g1.jsonl"))
	if !strings.Contains(string(b), "no-thread") {
		t.Fatal("無法歸屬的 frame 不得丟棄（§3.4.5）")
	}
}

func TestLineErrorLatchesAndStaysLatched(t *testing.T) {
	g, _ := NewGeneration(t.TempDir(), "g1", nil)
	g.ForceWriteErrForTest(errors.New("disk full"))
	if err := g.Line(DirClientToServer, []byte("x")); err == nil {
		t.Fatal("寫入失敗必須回錯")
	}
	g.ForceWriteErrForTest(nil)
	if g.Err() == nil {
		t.Fatal("錯誤必須 latch，不因後續成功而清除（§3.4.6）")
	}
}

func TestFrameIndexIsRebuildable(t *testing.T) {
	dir := t.TempDir()
	g, _ := NewGeneration(dir, "g1", nil)
	_ = g.Line(DirClientToServer, []byte(`{"id":1}`))
	_ = g.Line(DirServerToClient, []byte(`{"id":1}`))
	_ = g.Finalize(recorder.Meta{Provider: "codex"})
	rebuilt, err := RebuildFrameIndex(filepath.Join(dir, "g1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt.Snapshot(), g.FrameIndex().Snapshot()) {
		t.Fatal("frame index 必須可由 wire log 完整重建（§3.4.3）")
	}
}

// TestLineConcurrentSafe 對應 Task 8 review 抓到的同類問題（seq 在鎖內配發但
// Fprintf 在鎖外）：Line 會被 read loop（s2c）與多個送訊息的 goroutine（c2s）同
// 時呼叫，frame 編號配發＋寫入必須完整落在同一段鎖內，否則並行呼叫會讓 JSONL
// 行交錯損毀，或讓 frame 編號與檔案內容的順序錯位。用 -race 抓資料競態；並實測
// 每個 frame 編號恰好出現一次、每行都是可解析的完整 JSON、且**frame 編號在檔案
// 裡嚴格遞增**（= 寫入順序與編號順序一致）——這是「編號配發與寫入落在同一段鎖
// 內」保證的不變量，把編號配發與寫入拆成兩段鎖會讓這個順序不再保證（已用 mutation
// 驗證：拆開後 -race 在 OS 層級的 write() 仍不見得會抓到交錯損毀，但順序不變量會
// 被打破）。
func TestLineConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	g, err := NewGeneration(dir, "g1", nil)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				d := DirClientToServer
				if j%2 == 0 {
					d = DirServerToClient
				}
				raw := []byte(fmt.Sprintf(`{"id":%d,"g":%d,"n":%d}`, i*perGoroutine+j, i, j))
				if err := g.Line(d, raw); err != nil {
					t.Errorf("Line: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()

	if err := g.Finalize(recorder.Meta{Provider: "codex", ProcessStillRunning: true}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "g1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	lines := 0
	lastFrame := -1
	for sc.Scan() {
		lines++
		var row struct {
			Frame int             `json:"frame"`
			Raw   json.RawMessage `json:"raw"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d 不是合法 JSON（並行寫入交錯損毀）：%v\n%s", lines, err, sc.Bytes())
		}
		if seen[row.Frame] {
			t.Fatalf("frame %d 重複——編號配發與寫入沒有落在同一段鎖內", row.Frame)
		}
		seen[row.Frame] = true
		if row.Frame <= lastFrame {
			t.Fatalf("frame 順序錯位：line %d 的 frame=%d，前一行是 %d——編號配發與寫入沒有落在同一段鎖內", lines, row.Frame, lastFrame)
		}
		lastFrame = row.Frame
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	want := goroutines * perGoroutine
	if lines != want {
		t.Fatalf("行數=%d，want %d（並行寫入下遺失或多寫了 frame）", lines, want)
	}
	for i := 0; i < want; i++ {
		if !seen[i] {
			t.Fatalf("frame %d 缺漏", i)
		}
	}
}

// TestFinalizeIsIdempotent：後續 task（server 意外死亡的 reaper、B1 受控
// restart、shutdown 總序）會從多條路徑呼叫 Finalize，重複呼叫不得二次關檔（會
// 回傳 close-of-closed-file 錯誤蓋掉第一次的真正結果）、也不得用第二次傳入的
// meta 覆寫已寫出的 meta 檔。
func TestFinalizeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	g, err := NewGeneration(dir, "g1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Line(DirClientToServer, []byte(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}

	err1 := g.Finalize(recorder.Meta{Provider: "codex"})
	err2 := g.Finalize(recorder.Meta{Provider: "should-not-overwrite"})
	if err1 != nil {
		t.Fatalf("first Finalize: %v", err1)
	}
	if err2 != err1 {
		t.Fatalf("second Finalize must return the same cached result, got err1=%v err2=%v", err1, err2)
	}
	if got := g.FinalMeta().Provider; got != "codex" {
		t.Fatalf("second Finalize call must not overwrite meta, got provider=%q", got)
	}
	if !g.Finalized() {
		t.Fatal("Finalized() must report true after Finalize")
	}

	b, err := os.ReadFile(filepath.Join(dir, "g1.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"codex"`) || strings.Contains(string(b), "should-not-overwrite") {
		t.Fatalf("meta.json must reflect only the first Finalize call: %s", b)
	}
}

// TestRebuildFrameIndexTailTruncationIsTolerated 對應 coordinator review 的
// Important：app-server 意外死亡最典型的後果就是最後一行 JSONL 沒寫完，這正是
// RebuildFrameIndex 存在的動機（reaper／受控 restart／啟動修復）。比照 §3.5.6
// replay index 的損壞分級——檔尾（僅最後一行）不完整必須容忍，回傳有效前綴，
// 不得整份放棄。
func TestRebuildFrameIndexTailTruncationIsTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g1.jsonl")
	// 第二行故意不完整（缺結尾、無結尾換行）模擬 crash 當下寫一半的 frame。
	content := `{"frame":0,"dir":"c2s","wsid":"","raw":{"id":1}}` + "\n" +
		`{"frame":1,"dir":"s2c","wsid":"","raw":{"id":2}` // 截斷，無結尾 } 也無換行
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := RebuildFrameIndex(path)
	if err != nil {
		t.Fatalf("檔尾截斷必須容忍、回傳有效前綴，不應整份失敗：%v", err)
	}
	got := idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirClientToServer, RequestID: "1"})
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("有效前綴（frame 0）必須被保留：%v", got)
	}
	if idx.TruncatedTailBytes() == 0 {
		t.Fatal("必須回報丟棄了多少位元組（檔尾截斷）")
	}
}

// TestRebuildFrameIndexMidFileCorruptionFailsLoud：壞行之後還有有效行＝中段損
// 壞，不是 crash 的典型後果，必須 fail loud，不得靜默跳過或整份放棄部分結果。
func TestRebuildFrameIndexMidFileCorruptionFailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g1.jsonl")
	content := `{"frame":0,"dir":"c2s","wsid":"","raw":{"id":1}}` + "\n" +
		`not-json-garbage` + "\n" +
		`{"frame":2,"dir":"c2s","wsid":"","raw":{"id":3}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RebuildFrameIndex(path); err == nil {
		t.Fatal("中段損壞（後面還有有效行）必須 fail loud，不得跳過或整份丟棄")
	}
}

// TestAttributionIsPerFrameNotPerKey：§3.4.3 的歸屬**必須逐 frame**。
//
// notification 沒有 request id，同方向的每一筆都落在 RequestID:"" 這同一個
// FrameKey bucket 底下。若以 key 為單位標記歸屬（本套件原本的 Attribute 就是這個
// 形狀），兩個 session 交錯的通知會被整批標成最後一次判定的那個 WSID——正是「錄流
// frame 歸屬串線」的具體形狀。
//
// fixture 刻意不對稱（w-a 三筆、w-b 兩筆、順序交錯）：數量或順序對稱時，整批誤標
// 與逐筆正確在斷言上可能量不出差異。
func TestAttributionIsPerFrameNotPerKey(t *testing.T) {
	dir := t.TempDir()
	owners := []string{"w-a", "w-b", "w-a", "w-b", "w-a"}
	i := 0
	g, err := NewGeneration(dir, "g1", func(Direction, []byte) string {
		w := owners[i]
		i++
		return w
	})
	if err != nil {
		t.Fatal(err)
	}
	for range owners { // 全部都是無 id 的 notification：FrameKey 完全相同
		if err := g.Line(DirServerToClient, []byte(`{"method":"agent/message/delta"}`)); err != nil {
			t.Fatal(err)
		}
	}
	idx := g.FrameIndex()
	for frame, want := range owners {
		if got := idx.WSIDOf(frame); got != want {
			t.Fatalf("frame %d 歸屬 %q，want %q（同一個 FrameKey 底下被整批標成同一個 WSID？）",
				frame, got, want)
		}
	}
	if got := idx.FramesOf("w-a"); len(got) != 3 || got[0] != 0 || got[2] != 4 {
		t.Fatalf("FramesOf(w-a)=%v，want [0 2 4]", got)
	}
	if got := idx.FramesOf("w-b"); len(got) != 2 {
		t.Fatalf("FramesOf(w-b)=%v，want 兩筆", got)
	}
	if len(idx.Lookup(FrameKey{WireLogID: "g1", Direction: DirServerToClient})) != len(owners) {
		t.Fatal("precondition：五筆必須真的落在同一個 FrameKey bucket，否則測不到整批誤標")
	}

	// 歸屬必須跟著落盤：重建出來的 index 與記憶體一致（§3.4.3「可重建」的歸屬那一半）。
	if err := g.Finalize(recorder.Meta{Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := RebuildFrameIndex(filepath.Join(dir, "g1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt.Snapshot(), idx.Snapshot()) {
		t.Fatalf("重建後的歸屬必須與記憶體一致：\n got=%+v\nwant=%+v", rebuilt.Snapshot(), idx.Snapshot())
	}
}
