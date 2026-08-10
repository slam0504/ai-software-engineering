// Package appcore 是 app 層的可測核心：唯一序列化事件入口（Manager）、
// submission coordinator、session lifecycle 狀態機、錄流收尾 ownership
// （RecordingLease）與 provider pump 的 quiesce 契約。
package appcore

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// AuditSink 是稽核 JSONL 的出口；Write 錯誤由 Manager latch 並 fail-loud。
type AuditSink interface {
	Write(env contract.Envelope) error
	Close() error
}

// JSONLSink：O_APPEND 檔案實作。
type JSONLSink struct{ f *os.File }

func NewJSONLSink(path string) (*JSONLSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &JSONLSink{f: f}, nil
}

func (s *JSONLSink) Write(env contract.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.f, "%s\n", b)
	return err
}

func (s *JSONLSink) Close() error { return s.f.Close() }
