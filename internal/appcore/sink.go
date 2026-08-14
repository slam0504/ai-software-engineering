// Package appcore 是 app 層的可測核心：唯一序列化事件入口（Manager）、
// submission coordinator、session lifecycle 狀態機、錄流收尾 ownership
// （RecordingLease）與 provider pump 的 quiesce 契約。
package appcore

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// AppendReceipt：一筆事件在稽核 JSONL 中實際落地的 byte 範圍與 event id
// （spec §3.5.2）。replay index 一律以 receipt 為準，不得自行推算 offset。
type AppendReceipt struct {
	StartOffset int64
	EndOffset   int64
	EventID     string
}

// AuditSink 是稽核 JSONL 的出口；Write 錯誤由 Manager latch 並 fail-loud。
type AuditSink interface {
	Write(env contract.Envelope) (AppendReceipt, error)
	Close() error
}

// JSONLSink：O_APPEND 檔案實作。offset 於開檔時以 Seek(0, io.SeekEnd) 取得
// 初始值，之後單純累加寫入 byte 數——重開既有檔案時第一筆的 StartOffset
// 必須接續既有檔案長度，不能從 0 開始。
type JSONLSink struct {
	f      *os.File
	offset int64
}

func NewJSONLSink(path string) (*JSONLSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	off, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &JSONLSink{f: f, offset: off}, nil
}

func (s *JSONLSink) Write(env contract.Envelope) (AppendReceipt, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return AppendReceipt{}, err
	}
	n, err := fmt.Fprintf(s.f, "%s\n", b)
	if err != nil {
		return AppendReceipt{}, err
	}
	start := s.offset
	end := start + int64(n)
	s.offset = end
	return AppendReceipt{StartOffset: start, EndOffset: end, EventID: env.EventID}, nil
}

func (s *JSONLSink) Close() error { return s.f.Close() }
