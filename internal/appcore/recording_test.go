package appcore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type countingRec struct {
	closes atomic.Int32
	fail   error
}

func (c *countingRec) Line(b []byte) error { return nil }
func (c *countingRec) CloseWith(m recorder.Meta) error {
	c.closes.Add(1)
	return c.fail
}

func TestLeaseFinalizeExactlyOnceUnderBarrier(t *testing.T) {
	rec := &countingRec{}
	var stops atomic.Int32
	var gotExit ports.Exit
	l := NewRecordingLease(rec, func() error { stops.Add(1); return nil },
		func(ex ports.Exit) recorder.Meta {
			gotExit = ex
			m := recorder.Meta{Provider: "claude"}
			if ex.Exited { // 未知 exit → ExitCode 維持 nil
				code := ex.Code
				m.ExitCode = &code
			}
			return m
		})
	begin := make(chan struct{})
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range 8 { // EndSession/new session/shutdown/fatal 併發觸發
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			errs[i] = l.Finalize(ports.Exit{Exited: true, Code: 143})
		}(i)
	}
	close(begin)
	wg.Wait()
	if stops.Load() != 1 || rec.closes.Load() != 1 {
		t.Fatalf("stop=%d close=%d, want exactly once", stops.Load(), rec.closes.Load())
	}
	for _, e := range errs { // 全部呼叫者拿到同一（nil）結果
		if e != nil {
			t.Fatalf("idempotent result mismatch: %v", e)
		}
	}
	if !gotExit.Exited || gotExit.Code != 143 { // meta 的 ExitCode 來自 Finalize 的 Exit
		t.Fatalf("meta exit = %+v, want Exited 143", gotExit)
	}
	if !l.Finalized() {
		t.Fatal("finalized flag")
	}
}

func TestLeaseUnknownExitLeavesMetaNil(t *testing.T) { // stuck 路徑稽核證據
	rec := &countingRec{}
	var gotMeta recorder.Meta
	l := NewRecordingLease(rec, func() error { return nil },
		func(ex ports.Exit) recorder.Meta {
			m := recorder.Meta{Provider: "claude", StderrTail: ex.StderrTail}
			if ex.Exited {
				code := ex.Code
				m.ExitCode = &code
			}
			gotMeta = m
			return m
		})
	if err := l.Finalize(ports.Exit{Exited: false}); err != nil {
		t.Fatal(err)
	}
	if gotMeta.ExitCode != nil { // 未知不得寫成 exit 0
		t.Fatalf("unknown exit must leave meta ExitCode nil, got %d", *gotMeta.ExitCode)
	}
}

func TestLeaseFirstErrorRetained(t *testing.T) {
	boom := errors.New("close failed")
	rec := &countingRec{fail: boom}
	l := NewRecordingLease(rec, func() error { return nil },
		func(ports.Exit) recorder.Meta { return recorder.Meta{} })
	if err := l.Finalize(ports.Exit{}); !errors.Is(err, boom) {
		t.Fatalf("first error: %v", err)
	}
	if err := l.Finalize(ports.Exit{Exited: true, Code: 9}); !errors.Is(err, boom) { // 冪等回同一錯誤、ex 忽略
		t.Fatalf("repeat must return same error: %v", err)
	}
	if rec.closes.Load() != 1 {
		t.Fatal("CloseWith must not repeat")
	}
}

func TestLeaseStopErrorJoined(t *testing.T) {
	stopErr := errors.New("stop failed")
	rec := &countingRec{}
	l := NewRecordingLease(rec, func() error { return stopErr },
		func(ports.Exit) recorder.Meta { return recorder.Meta{} })
	if err := l.Finalize(ports.Exit{}); !errors.Is(err, stopErr) {
		t.Fatalf("stop error must surface: %v", err)
	}
	if rec.closes.Load() != 1 { // stop 失敗仍要 CloseWith（meta 盡力寫）
		t.Fatal("CloseWith must still run after stop error")
	}
}
