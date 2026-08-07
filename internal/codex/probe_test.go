package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type stubServer struct {
	done     chan struct{}
	beginErr error
	hsErr    error
	stopErr  error
	hsGate   chan struct{} // 非 nil：Handshake 阻塞至關閉

	mu         sync.Mutex
	begins     int
	stops      int
	terminates int
	waits      int
}

func newStubServer() *stubServer { return &stubServer{done: make(chan struct{})} }

func (s *stubServer) Done() <-chan struct{} { return s.done }
func (s *stubServer) BeginRecording(sink func([]byte) error) error {
	s.mu.Lock()
	s.begins++
	s.mu.Unlock()
	if s.beginErr != nil {
		return s.beginErr
	}
	_ = sink([]byte(`{"dir":"c2s","frame":{"id":1,"method":"initialize"}}`))
	return nil
}
func (s *stubServer) StopRecording() error {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
	return s.stopErr
}
func (s *stubServer) Handshake(ctx context.Context, ci ClientInfo) error {
	if s.hsGate != nil {
		<-s.hsGate
	}
	return s.hsErr
}
func (s *stubServer) Terminate() error {
	s.mu.Lock()
	s.terminates++
	s.mu.Unlock()
	return nil
}
func (s *stubServer) Wait() proc.Exit {
	s.mu.Lock()
	s.waits++
	s.mu.Unlock()
	return proc.Exit{Code: 9, StderrTail: "stub-stderr"}
}
func (s *stubServer) StderrSnapshot() string { return "live-stderr" }
func (s *stubServer) Argv() []string         { return []string{"codex", "app-server"} }

func (s *stubServer) callCounts() (begins, stops, terminates, waits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.stops, s.terminates, s.waits
}

func probeRecFactory(t *testing.T) (func() (*recorder.Recorder, error), string) {
	t.Helper()
	dir := t.TempDir()
	return func() (*recorder.Recorder, error) { return recorder.New(dir, "codex-probe", ".jsonl") }, dir
}

func readProbeMeta(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "codex-probe.meta.json"))
	if err != nil {
		t.Fatalf("meta must exist: %v", err)
	}
	return string(b)
}

func TestProbeNewRecFails(t *testing.T) {
	var single Single[*stubServer]
	boom := errors.New("no space")
	var started int32
	err := RunHandshakeProbe(context.Background(), &single,
		func() (*recorder.Recorder, error) { return nil, boom },
		func() (*stubServer, error) { atomic.AddInt32(&started, 1); return newStubServer(), nil },
		ClientInfo{Name: "t"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if atomic.LoadInt32(&started) != 0 {
		t.Fatal("start must not run when recorder creation fails")
	}
	if _, ok := single.Take(); ok {
		t.Fatal("ownership must stay empty")
	}
}

func TestProbeStartFails(t *testing.T) {
	var single Single[*stubServer]
	newRec, dir := probeRecFactory(t)
	boom := errors.New("spawn failed")
	err := RunHandshakeProbe(context.Background(), &single, newRec,
		func() (*stubServer, error) { return nil, boom }, ClientInfo{Name: "t"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	meta := readProbeMeta(t, dir)
	if strings.Contains(meta, `"exit_code"`) {
		t.Fatalf("start-failure meta must have no exit_code: %s", meta)
	}
	if !strings.Contains(meta, "spawn failed") {
		t.Fatalf("meta must record the start error: %s", meta)
	}
	if _, ok := single.Take(); ok {
		t.Fatal("ownership must stay empty")
	}
}

func TestProbeBeginRecordingFails(t *testing.T) { // v1.8 P0：Begin 失敗也要 dispose
	var single Single[*stubServer]
	newRec, dir := probeRecFactory(t)
	stub := newStubServer()
	stub.beginErr = errors.New("begin failed")
	err := RunHandshakeProbe(context.Background(), &single, newRec,
		func() (*stubServer, error) { return stub, nil }, ClientInfo{Name: "t"})
	if err == nil || !strings.Contains(err.Error(), "begin failed") {
		t.Fatalf("err = %v", err)
	}
	_, _, terms, waits := stub.callCounts()
	if terms != 1 || waits != 1 {
		t.Fatalf("must Terminate+Wait exactly once: term=%d wait=%d", terms, waits)
	}
	meta := readProbeMeta(t, dir)
	if !strings.Contains(meta, `"exit_code": 9`) || !strings.Contains(meta, "stub-stderr") {
		t.Fatalf("meta must carry Exit evidence: %s", meta)
	}
	if _, ok := single.Take(); ok {
		t.Fatal("ownership must stay empty")
	}
}

func TestProbeHandshakeFails(t *testing.T) {
	var single Single[*stubServer]
	newRec, dir := probeRecFactory(t)
	stub := newStubServer()
	stub.hsErr = errors.New("handshake refused")
	err := RunHandshakeProbe(context.Background(), &single, newRec,
		func() (*stubServer, error) { return stub, nil }, ClientInfo{Name: "t"})
	if err == nil || !strings.Contains(err.Error(), "handshake refused") {
		t.Fatalf("err = %v", err)
	}
	_, stops, terms, waits := stub.callCounts()
	if stops != 1 || terms != 1 || waits != 1 {
		t.Fatalf("must Stop then dispose: stop=%d term=%d wait=%d", stops, terms, waits)
	}
	meta := readProbeMeta(t, dir)
	if !strings.Contains(meta, `"exit_code": 9`) {
		t.Fatalf("meta must carry exit code: %s", meta)
	}
	if _, ok := single.Take(); ok {
		t.Fatal("ownership must stay empty")
	}
}

func TestProbeSuccess(t *testing.T) {
	var single Single[*stubServer]
	newRec, dir := probeRecFactory(t)
	stub := newStubServer()
	if err := RunHandshakeProbe(context.Background(), &single, newRec,
		func() (*stubServer, error) { return stub, nil }, ClientInfo{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	_, stops, terms, _ := stub.callCounts()
	if stops != 1 || terms != 0 {
		t.Fatalf("success path: stop=%d term=%d", stops, terms)
	}
	meta := readProbeMeta(t, dir)
	if !strings.Contains(meta, `"process_still_running": true`) || strings.Contains(meta, `"exit_code"`) {
		t.Fatalf("success meta must mark still-running without exit_code: %s", meta)
	}
	if v, ok := single.Take(); !ok || v != stub {
		t.Fatal("ownership must be the probed server")
	}
}

func TestProbeExcludesEnsure(t *testing.T) { // v1.9 P0：probe 與 Ensure 互斥
	var single Single[*stubServer]
	old := newStubServer() // 既有存活 server：probe 必須 dispose 它
	if _, err := single.Ensure(func() (*stubServer, error) { return old, nil }); err != nil {
		t.Fatal(err)
	}
	newRec, _ := probeRecFactory(t)
	stub := newStubServer()
	stub.hsGate = make(chan struct{})
	probeDone := make(chan error, 1)
	go func() {
		probeDone <- RunHandshakeProbe(context.Background(), &single, newRec,
			func() (*stubServer, error) { return stub, nil }, ClientInfo{Name: "t"})
	}()
	waitFor(t, func() bool { b, _, _, _ := stub.callCounts(); return b == 1 }, "probe reached BeginRecording")

	var ensureStarts int32
	ensureDone := make(chan *stubServer, 1)
	go func() {
		v, _ := single.Ensure(func() (*stubServer, error) {
			atomic.AddInt32(&ensureStarts, 1)
			return newStubServer(), nil
		})
		ensureDone <- v
	}()
	select {
	case <-ensureDone:
		t.Fatal("Ensure must block while probe holds the exclusive lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(stub.hsGate)
	if err := <-probeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-ensureDone:
		if v != stub {
			t.Fatal("Ensure must return the probe's server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure still blocked after probe finished")
	}
	if atomic.LoadInt32(&ensureStarts) != 0 {
		t.Fatal("Ensure start must not run (probe installed the server)")
	}
	if term, wait := func() (int, int) { _, _, tm, w := old.callCounts(); return tm, w }(); term != 1 || wait != 1 {
		t.Fatalf("replaced server must be disposed: term=%d wait=%d", term, wait)
	}
	if v, ok := single.Take(); !ok || v != stub {
		t.Fatal("ownership must be unique and equal to probe's server")
	}
}
