package appcore

import (
	"errors"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/ports"
)

// Task 21：After 必須是真正的等待來源注入點，不是裝飾參數——這四條測試釘住
// WaitQuiesce／CloseSequence 對外行為與內部呼叫的耦合（見 pump.go:23-68 doc）。

func TestWaitQuiesceUsesInjectedAfter(t *testing.T) {
	fired := make(chan time.Time, 1)
	var gotTimeout time.Duration
	after := func(d time.Duration) <-chan time.Time { gotTimeout = d; return fired }
	done := make(chan struct{})
	errC := make(chan error, 1)
	go func() { errC <- WaitQuiesce(done, 7*time.Second, after) }()
	fired <- time.Time{} // 受控觸發，不真的等 7 秒
	if err := <-errC; err == nil {
		t.Fatal("逾時必須回 error")
	}
	if gotTimeout != 7*time.Second {
		t.Fatalf("timeout 未傳入注入的 after：%v", gotTimeout)
	}
}

func TestCloseSequenceEscalatesWithInjectedAfter(t *testing.T) {
	quiesce := make(chan time.Time, 1)
	kill := make(chan time.Time, 1)
	calls := 0
	after := func(d time.Duration) <-chan time.Time {
		calls++
		if calls == 1 {
			return quiesce
		}
		return kill
	}
	var terminated bool
	done := make(chan struct{})
	errC := make(chan error, 1)
	go func() {
		_, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
			func() error { terminated = true; return nil },
			func() ports.Exit { return ports.Exit{} },
			func(ports.Exit) error { return nil }, after)
		errC <- err
	}()
	quiesce <- time.Time{} // 第一次逾時 → 升級 Terminate
	kill <- time.Time{}    // 第二次逾時 → 盡力 finalize
	<-errC
	if !terminated {
		t.Fatal("quiesce 逾時必須升級 Terminate")
	}
	if calls != 2 {
		t.Fatalf("必須恰有兩段 bounded window：%d", calls)
	}
}

// 錯誤契約回歸：finalize 的 error 必須仍進 errors.Join（pump.go:59-60）
func TestCloseSequenceStillPropagatesFinalizeError(t *testing.T) {
	done := make(chan struct{})
	close(done) // 直接 quiesce 成功，走 wait → finalize 路徑
	finErr := errors.New("finalize failed")
	_, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
		func() error { return nil },
		func() ports.Exit { return ports.Exit{Exited: true, Code: 0} },
		func(ports.Exit) error { return finErr }, RealAfter)
	if !errors.Is(err, finErr) {
		t.Fatalf("finalize error 必須仍回傳並 Join：%v", err)
	}
}

// 回傳型別回歸：仍是 ports.Exit，且 wait() 的 cached Exit 原樣回傳
func TestCloseSequenceReturnsPortsExit(t *testing.T) {
	done := make(chan struct{})
	close(done)
	want := ports.Exit{Exited: true, Code: 7, StderrTail: "tail"}
	got, err := CloseSequence(func() error { return nil }, done, time.Second, time.Second,
		func() error { return nil }, func() ports.Exit { return want },
		func(ports.Exit) error { return nil }, RealAfter)
	if err != nil || got != want {
		t.Fatalf("回傳契約不得改變：%+v %v", got, err)
	}
}
