package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedReadSeeker：依序回放腳本片段的 stub reader——readEnvelopeRange 收
// *os.File 時真實檔案系統造不出「讀到一半才錯」與「包裝 EOF」兩種形狀
// （darwin 目錄注入的 EISDIR 只出現在首讀），簽章放寬後由本 stub 注入。
type scriptedReadSeeker struct {
	chunks []struct {
		data string
		err  error
	}
	i int
}

func (s *scriptedReadSeeker) Read(p []byte) (int, error) {
	if s.i >= len(s.chunks) {
		return 0, io.EOF
	}
	c := &s.chunks[s.i]
	n := copy(p, c.data)
	c.data = c.data[n:] // 消耗式：chunk 大於 len(p) 時分次吐完，不靜默截斷（plan gate P2）
	if len(c.data) > 0 {
		return n, nil // 尚未吐完，錯誤留到本 chunk 消耗完那次一併回
	}
	s.i++
	return n, c.err
}

func (s *scriptedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return offset, nil
}

// 非 EOF 讀取錯誤必須回錯、不得把已讀內容當成功結果回傳（部分成功樣＝靜默
// 截頁）；錯誤脈絡雙斷言：offset 與 wsid 缺一必紅（spec §2 凍結「wrapped，
// 含 offset 與 wsid 脈絡」）。核心 mutation：非 EOF 分支改回 break → 本測試紅。
func TestReadEnvelopeRangeNonEOFErrorFailsLoud(t *testing.T) {
	lines := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e2","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e3","workspace_session_id":"w1","kind":"message"}` + "\n"
	r := &scriptedReadSeeker{chunks: []struct {
		data string
		err  error
	}{
		{data: lines},
		{err: errors.New("simulated EIO")},
	}}
	out, err := readEnvelopeRange(r, "w1", "", 42, -1)
	if err == nil {
		t.Fatal("非 EOF 讀取錯誤必須回錯，不得當 EOF 靜默截頁")
	}
	if out != nil {
		t.Fatalf("錯誤時不得回傳部分成功結果：%d 筆", len(out))
	}
	wantOffset := fmt.Sprintf("at %d", 42+int64(len(lines)))
	if !strings.Contains(err.Error(), wantOffset) {
		t.Fatalf("錯誤必須含讀取當下 offset（%s）：%v", wantOffset, err)
	}
	if !strings.Contains(err.Error(), "wsid=w1") {
		t.Fatalf("錯誤必須含 wsid 脈絡：%v", err)
	}
}

// 包裝 EOF 必須視為正常終點且同批殘行被收錄（spec §2；owner review P2——
// 真實檔案只產生裸 io.EOF，沒有這條 fixture，errors.Is→== 的 mutation 恆綠）。
func TestReadEnvelopeRangeWrappedEOFCollectsFinalLine(t *testing.T) {
	final := `{"event_id":"e9","workspace_session_id":"w1","kind":"message"}` // 無換行
	r := &scriptedReadSeeker{chunks: []struct {
		data string
		err  error
	}{
		{data: final, err: fmt.Errorf("wrapped: %w", io.EOF)},
	}}
	out, err := readEnvelopeRange(r, "w1", "", 0, -1)
	if err != nil {
		t.Fatalf("包裝 EOF 是正常終點，不得回錯：%v", err)
	}
	if len(out) != 1 || out[0].EventID != "e9" {
		t.Fatalf("同批殘行（合法 JSON＋WSID 相符＋無換行）必須被收錄：%+v", out)
	}
}

// 檔尾無換行的裸 EOF 殘行（既有行為迴歸鎖；mutation：殘行處理移到 break 之後
// → 紅）。fixture 條件依 spec §4 寫死：合法 JSON＋WSID 相符＋無換行——半截
// JSON 會被 malformed 跳過使 mutation 恆綠。
func TestReadEnvelopeRangeBareEOFFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{"event_id":"e2","workspace_session_id":"w1","kind":"message"}` // 最後一行無換行
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[1].EventID != "e2" {
		t.Fatalf("裸 EOF 同批殘行必須被收錄：%+v", out)
	}
}

// malformed 單行跳過（既有慣例迴歸鎖）。
func TestReadEnvelopeRangeSkipsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n" +
		`{broken json` + "\n" +
		`{"event_id":"e3","workspace_session_id":"w1","kind":"message"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].EventID != "e1" || out[1].EventID != "e3" {
		t.Fatalf("malformed 行跳過、其餘照回：%+v", out)
	}
}

// 「EOF 早於 end」寬容迴歸鎖（spec §2：讀到 EOF 為止、回部分結果、不回錯）。
func TestReadEnvelopeRangeEndBeyondFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"event_id":"e1","workspace_session_id":"w1","kind":"message"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnvelopeRange(f, "w1", "", 0, int64(len(content))+9999)
	if err != nil {
		t.Fatalf("EOF 早於 end 維持寬容、不回錯：%v", err)
	}
	if len(out) != 1 {
		t.Fatalf("應回部分結果：%+v", out)
	}
}

// 目錄 FD（首讀即 EISDIR——開檔即壞的形狀；讀到一半才錯由 scripted reader 蓋）。
func TestReadEnvelopeRangeDirectoryFDFailsLoud(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := readEnvelopeRange(f, "w1", "", 0, -1); err == nil {
		t.Fatal("目錄 FD 讀取失敗必須回錯")
	}
}
