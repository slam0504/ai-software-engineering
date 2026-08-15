// Package appcore 是 app 層的可測核心：唯一序列化事件入口（Manager）、
// submission coordinator、session lifecycle 狀態機、錄流收尾 ownership
// （RecordingLease）與 provider pump 的 quiesce 契約。
package appcore

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync/atomic"

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
// **寫入端不是 goroutine-safe**：offset 是單一 writer 的累加器，正確性依賴
// 呼叫端序列化每一次 Write。目前唯一呼叫端是 Manager.writeAndEmitLocked，其
// 序列化由 Manager 自身的單一 mutex 保證（全 repo 只有這一處 Sink.Write
// 呼叫點）。若未來出現繞過 Manager 直接使用 JSONLSink 的呼叫端（例如獨立
// 的 export／migration 工具），該呼叫端必須自行序列化寫入。
//
// offset 的**型別**是 atomic.Int64，但這不是為了支援多 writer——它服務的是
// 唯一的併發**讀取**端 End()（見其 doc）：replay index 的 runtime 重建要在
// 不持任何鎖的情況下讀「audit 權威檔尾」，若用一般 int64，那次讀取會與
// Write 的累加構成 data race（`-race` 會抓到）。加鎖不行：重建的第 4 步是
// 在**已持有 Manager mutex 時**呼叫 End()，重用同一把鎖會自我死鎖
// （sync.Mutex 不可重入），詳見 replayindex.auditEndFunc 的呼叫端契約。
//
// 另假設同一時間只有一個 process／handle 持有這個檔案的寫入權——offset
// 是本機累加值，不會偵測其他 process 對同一檔案的並行寫入。
type JSONLSink struct {
	f      jsonlWriter
	offset atomic.Int64
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
	s := &JSONLSink{f: f}
	s.offset.Store(off)
	return s, nil
}

// End：目前已受理的 append 進度（= 最後一筆成功寫入的 EndOffset；尚未寫過
// 任何一筆時即開檔當下的檔案長度）。JSONLSink 無 buffer，直接 Fprintf 到
// 檔案，所以這個值就是 audit 權威檔尾。
//
// 這是 replayindex.auditEndFunc 的來源：該契約要求「不得取得 emit mutex、
// 且不持任何鎖時也能安全讀取」，所以這裡刻意只做一次 atomic load、不加鎖。
// 併發讀到的必然是某次 Write 前或後的值（單一 writer，無撕裂）：偏舊只會讓
// 重建把殘量估得保守，重建的第 4 步在持有 emit mutex 時重讀，那一刻不可能
// 有 in-flight 的 Write，讀到的即精確值。
func (s *JSONLSink) End() int64 { return s.offset.Load() }

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
			s.offset.Store(off)
		}
		return AppendReceipt{}, err
	}
	start := s.offset.Load()
	end := start + int64(n)
	s.offset.Store(end)
	return AppendReceipt{StartOffset: start, EndOffset: end, EventID: env.EventID}, nil
}

func (s *JSONLSink) Close() error { return s.f.Close() }
