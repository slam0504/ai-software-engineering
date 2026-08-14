package appcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// TestJSONLSinkReceiptMatchesFileOffsets：AppendReceipt 的 offset 必須與檔案
// 實際 byte 範圍一致，且連續兩筆寫入的 offset 要接續（spec §3.5.2）。
func TestJSONLSinkReceiptMatchesFileOffsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	s, err := NewJSONLSink(p)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := s.Write(contract.Envelope{EventID: "e1", Kind: "message"})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Write(contract.Envelope{EventID: "e2", Kind: "message"})
	_ = s.Close()
	b, _ := os.ReadFile(p)
	if r1.StartOffset != 0 || r2.StartOffset != r1.EndOffset {
		t.Fatalf("offset 必須連續：%+v %+v", r1, r2)
	}
	if int64(len(b)) != r2.EndOffset {
		t.Fatalf("EndOffset 必須等於檔案長度：%d vs %d", r2.EndOffset, len(b))
	}
	if !strings.Contains(string(b[r1.StartOffset:r1.EndOffset]), `"e1"`) {
		t.Fatal("receipt 範圍未涵蓋該筆")
	}
}

// TestSinkReopenContinuesOffsets：重開既有檔案時 offset 必須接續現有檔案長度，
// 不能從 0 開始（O_APPEND 開檔＋開檔時 Seek 取初始 offset）。
func TestSinkReopenContinuesOffsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	s1, _ := NewJSONLSink(p)
	r1, _ := s1.Write(contract.Envelope{EventID: "e1", Kind: "message"})
	_ = s1.Close()
	s2, _ := NewJSONLSink(p)
	r2, _ := s2.Write(contract.Envelope{EventID: "e2", Kind: "message"})
	if r2.StartOffset != r1.EndOffset {
		t.Fatalf("重開後 offset 未接續：%+v %+v", r1, r2)
	}
}
