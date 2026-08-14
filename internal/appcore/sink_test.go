package appcore

import (
	"bytes"
	"errors"
	"io"
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

// shortWriteFile：模擬「短寫＋error」的底層檔案（例如 ENOSPC 寫到一半）——
// 第 failAt 次 Write 只落地 len(p)-shortBy bytes 就回錯，但那些 bytes 已經
// 真的寫進 buf（如同真實檔案系統）。Seek(0, io.SeekEnd) 回傳 buf 目前長度，
// 模擬 JSONLSink 短寫後用來重新校正 offset 的呼叫。
type shortWriteFile struct {
	buf       bytes.Buffer
	failAt    int
	shortBy   int
	callCount int
}

func (f *shortWriteFile) Write(p []byte) (int, error) {
	f.callCount++
	if f.callCount == f.failAt {
		n := len(p) - f.shortBy
		if n < 0 {
			n = 0
		}
		f.buf.Write(p[:n])
		return n, errors.New("simulated short write: no space left on device")
	}
	f.buf.Write(p)
	return len(p), nil
}

func (f *shortWriteFile) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekEnd && offset == 0 {
		return int64(f.buf.Len()), nil
	}
	return 0, errors.New("shortWriteFile: unsupported seek in test")
}

func (f *shortWriteFile) Close() error { return nil }

// TestJSONLSinkShortWriteRecalibratesOffset：短寫＋error 之後，offset 必須
// 重新校正為檔案實際長度，否則會永久落後、讓之後每一筆 receipt 都偏移
// （§3.5.2：receipt 是 ground truth，不得漂移）。
func TestJSONLSinkShortWriteRecalibratesOffset(t *testing.T) {
	fw := &shortWriteFile{failAt: 2, shortBy: 5}
	s := &JSONLSink{f: fw}

	if _, err := s.Write(contract.Envelope{EventID: "e1", Kind: "message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(contract.Envelope{EventID: "e2", Kind: "message"}); err == nil {
		t.Fatal("want short-write error")
	}
	wantOffset := int64(fw.buf.Len()) // 真實檔案長度：含短寫落地的殘餘 bytes
	if s.offset != wantOffset {
		t.Fatalf("短寫後 offset 未重新校正：got %d want %d", s.offset, wantOffset)
	}
	r3, err := s.Write(contract.Envelope{EventID: "e3", Kind: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if r3.StartOffset != wantOffset {
		t.Fatalf("下一筆 receipt 未接續校正後的 offset：got %d want %d", r3.StartOffset, wantOffset)
	}
}
