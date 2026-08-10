package appcore

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

// RecorderHandle 是 *recorder.Recorder 的最小介面（測試用 stub）。
type RecorderHandle interface {
	Line(b []byte) error
	CloseWith(m recorder.Meta) error
}

// RecordingLease：session-scoped 錄流的收尾 ownership——多個收尾來源
// （EndSession／new session／shutdown／fatal）併發觸發時 stop 恰一次、
// CloseWith 恰一次、首個錯誤保留、後續呼叫冪等回傳同一結果。
//
// Finalize 攜帶 ports.Exit：首次呼叫的 ex 進 metaFn（後續呼叫的 ex 忽略）；
// ex.Exited=false 時 metaFn 依規約讓 Meta.ExitCode 維持 nil（未知不偽裝 exit 0）。
type RecordingLease struct {
	once   sync.Once
	stop   func() error
	rec    RecorderHandle
	metaFn func(ports.Exit) recorder.Meta
	err    error
	done   atomic.Bool
}

func NewRecordingLease(rec RecorderHandle, stop func() error,
	metaFn func(ports.Exit) recorder.Meta) *RecordingLease {
	return &RecordingLease{rec: rec, stop: stop, metaFn: metaFn}
}

func (l *RecordingLease) Finalize(ex ports.Exit) error {
	l.once.Do(func() {
		stopErr := l.stop()
		closeErr := l.rec.CloseWith(l.metaFn(ex)) // stop 失敗仍 CloseWith（meta 盡力寫）
		l.err = errors.Join(stopErr, closeErr)
		l.done.Store(true)
	})
	return l.err
}

func (l *RecordingLease) Finalized() bool { return l.done.Load() }
