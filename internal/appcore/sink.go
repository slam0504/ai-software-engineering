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

// jsonlWriter：JSONLSink 對底層檔案的最小需求，讓測試能注入會短寫的 fake
// （production 一律是 *os.File）。
type jsonlWriter interface {
	io.Writer
	io.Seeker
	io.Closer
}

// JSONLSink：O_APPEND 檔案實作。offset 於開檔時以 Seek(0, io.SeekEnd) 取得
// 初始值，之後單純累加寫入 byte 數——重開既有檔案時第一筆的 StartOffset
// 必須接續既有檔案長度，不能從 0 開始。
//
// 不是 goroutine-safe：offset 累加器沒有自己的鎖，正確性完全依賴呼叫端
// 序列化每一次 Write。目前唯一呼叫端是 Manager.writeAndEmitLocked，其
// 序列化由 Manager 自身的單一 mutex 保證（全 repo 只有這一處 Sink.Write
// 呼叫點）。若未來出現繞過 Manager 直接使用 JSONLSink 的呼叫端（例如獨立
// 的 export／migration 工具），該呼叫端必須自行序列化寫入。
//
// 另假設同一時間只有一個 process／handle 持有這個檔案的寫入權——offset
// 是本機累加值，不會偵測其他 process 對同一檔案的並行寫入。
type JSONLSink struct {
	f      jsonlWriter
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
		// 短寫可能已把部分 byte 落到檔案：如果不校正，s.offset 會永久落後
		// 於檔案實際長度，之後每一筆 receipt 都會偏移（§3.5.2 凍結 receipt
		// 是 ground truth，不能漂移）。用 Seek 重新對齊到檔案實際長度；若
		// 連 Seek 都失敗，放棄校正並優先回報原始寫入錯誤（fail loud）。
		if off, serr := s.f.Seek(0, io.SeekEnd); serr == nil {
			s.offset = off
		}
		return AppendReceipt{}, err
	}
	start := s.offset
	end := start + int64(n)
	s.offset = end
	return AppendReceipt{StartOffset: start, EndOffset: end, EventID: env.EventID}, nil
}

func (s *JSONLSink) Close() error { return s.f.Close() }
