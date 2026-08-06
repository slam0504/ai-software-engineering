package codex

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubAlive struct {
	done       chan struct{}
	mu         sync.Mutex
	terminated int
	waited     int
}

func (s *stubAlive) Done() <-chan struct{} { return s.done }
func (s *stubAlive) recordTerminate()      { s.mu.Lock(); s.terminated++; s.mu.Unlock() }
func (s *stubAlive) recordWait()           { s.mu.Lock(); s.waited++; s.mu.Unlock() }
func (s *stubAlive) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminated, s.waited
}

func newStubAlive(dead bool) *stubAlive {
	s := &stubAlive{done: make(chan struct{})}
	if dead {
		close(s.done)
	}
	return s
}

func TestSingleEnsureConcurrentRestartsOnce(t *testing.T) { // 第八輪 P1：實際併發測試
	var s Single[*stubAlive]
	if _, err := s.Ensure(func() (*stubAlive, error) { return newStubAlive(true), nil }); err != nil {
		t.Fatal(err) // 放入已死 stub
	}
	fresh := newStubAlive(false)
	var count int32
	start := func() (*stubAlive, error) {
		atomic.AddInt32(&count, 1)
		return fresh, nil
	}
	begin := make(chan struct{})
	var got [2]*stubAlive
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin // barrier：同時進入 Ensure
			v, err := s.Ensure(start)
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}(i)
	}
	close(begin)
	wg.Wait()
	if c := atomic.LoadInt32(&count); c != 1 {
		t.Fatalf("start called %d times, want exactly 1", c)
	}
	if got[0] != fresh || got[1] != fresh {
		t.Fatal("both goroutines must obtain the same new instance")
	}
}

func TestSingleEnsureReusesAlive(t *testing.T) {
	var s Single[*stubAlive]
	alive := newStubAlive(false)
	if _, err := s.Ensure(func() (*stubAlive, error) { return alive, nil }); err != nil {
		t.Fatal(err)
	}
	v, err := s.Ensure(func() (*stubAlive, error) {
		t.Fatal("start must not be called for alive instance")
		return nil, nil
	})
	if err != nil || v != alive {
		t.Fatalf("must reuse alive instance: %v %v", v, err)
	}
}

func TestSingleEnsureStartFailureLeavesEmpty(t *testing.T) {
	var s Single[*stubAlive]
	boom := errors.New("boom")
	if _, err := s.Ensure(func() (*stubAlive, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Take(); ok {
		t.Fatal("failed start must leave ownership empty")
	}
}

func TestWithExclusiveBlocksEnsure(t *testing.T) { // v1.9：fn 期間 Ensure 阻塞
	var s Single[*stubAlive]
	b := newStubAlive(false)
	entered := make(chan struct{})
	gate := make(chan struct{})
	weDone := make(chan error, 1)
	go func() {
		weDone <- s.WithExclusive(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
			close(entered)
			<-gate
			return b, true, nil
		})
	}()
	<-entered
	ensureDone := make(chan *stubAlive, 1)
	go func() {
		v, _ := s.Ensure(func() (*stubAlive, error) {
			t.Error("Ensure start must not be called (WithExclusive installed B)")
			return nil, nil
		})
		ensureDone <- v
	}()
	select {
	case <-ensureDone:
		t.Fatal("Ensure must block while WithExclusive holds the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	if err := <-weDone; err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-ensureDone:
		if v != b {
			t.Fatal("Ensure must observe WithExclusive's instance")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure still blocked after WithExclusive returned")
	}
}

func TestWithExclusiveReplaceDisposesOld(t *testing.T) {
	var s Single[*stubAlive]
	a := newStubAlive(false)
	if _, err := s.Ensure(func() (*stubAlive, error) { return a, nil }); err != nil {
		t.Fatal(err)
	}
	b := newStubAlive(false)
	err := s.WithExclusive(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
		if !ok || cur != a {
			t.Fatalf("fn must receive current instance: %v %v", cur, ok)
		}
		cur.recordTerminate()
		cur.recordWait()
		return b, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if term, wait := a.counts(); term != 1 || wait != 1 {
		t.Fatalf("old instance must be disposed: terminate=%d wait=%d", term, wait)
	}
	if v, ok := s.Take(); !ok || v != b {
		t.Fatal("ownership must be the replacement instance")
	}
}

func TestWithExclusiveFailureLeavesEmpty(t *testing.T) {
	var s Single[*stubAlive]
	boom := errors.New("probe failed")
	if err := s.WithExclusive(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
		return nil, false, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Take(); ok {
		t.Fatal("keep=false must leave ownership empty")
	}
}

func TestWithExclusiveKeepWithError(t *testing.T) { // stop/close 失敗但 server 留用
	var s Single[*stubAlive]
	b := newStubAlive(false)
	closeErr := errors.New("close failed")
	if err := s.WithExclusive(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
		return b, true, closeErr
	}); !errors.Is(err, closeErr) {
		t.Fatalf("err = %v", err)
	}
	if v, ok := s.Take(); !ok || v != b {
		t.Fatal("keep=true must retain instance even with error")
	}
}
