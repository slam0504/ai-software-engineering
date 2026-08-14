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
	g, _ := NewGeneration(t.TempDir(), "g1")
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
	g, _ := NewGeneration(dir, "g1")
	_ = g.Line(DirServerToClient, []byte(`{"unknown":"no-thread"}`))
	_ = g.Finalize(recorder.Meta{Provider: "codex"})
	b, _ := os.ReadFile(filepath.Join(dir, "g1.jsonl"))
	if !strings.Contains(string(b), "no-thread") {
		t.Fatal("無法歸屬的 frame 不得丟棄（§3.4.5）")
	}
}

func TestLineErrorLatchesAndStaysLatched(t *testing.T) {
	g, _ := NewGeneration(t.TempDir(), "g1")
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
	g, _ := NewGeneration(dir, "g1")
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
	g, err := NewGeneration(dir, "g1")
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
	g, err := NewGeneration(dir, "g1")
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
