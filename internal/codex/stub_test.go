package codex

import (
	"context"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/proc"
)

// stubServer 是 ProbeTarget 的測試替身（原 probe_test.go；Task 13 刪掉
// probe-scoped 入口後，這裡只留 owner 版測試共用的 stub 基礎設施）。
type stubServer struct {
	done     chan struct{}
	beginErr error
	hsErr    error
	stopErr  error
	hsGate   chan struct{} // 非 nil：Handshake 阻塞至關閉
	exit     proc.Exit     // Wait 回傳值（預設 9／stub-stderr）
	onBegin  func()        // 非 nil：BeginRecording 進入時呼叫（順序斷言用）
	onStop   func()        // 非 nil：StopRecording 進入時呼叫（順序斷言用）
	conn     *Conn         // 非 nil：收尾時必須等它的 Done（stdout 汲取完成）

	mu         sync.Mutex
	begins     int
	stops      int
	terminates int
	waits      int
	calls      []string // 收尾相關呼叫的實際順序
	dieOnce    sync.Once
}

func newStubServer() *stubServer {
	return &stubServer{done: make(chan struct{}), exit: proc.Exit{Code: 9, StderrTail: "stub-stderr"}}
}

// die 模擬 server 意外死亡（只關 Done，不做其他事）——死亡收尾必須由 production
// reaper 負責，測試不得代勞。
func (s *stubServer) die() { s.dieOnce.Do(func() { close(s.done) }) }

func (s *stubServer) Done() <-chan struct{} { return s.done }
func (s *stubServer) Conn() *Conn           { return s.conn } // 預設 nil：無 stdout 可等
func (s *stubServer) BeginRecording(sink func([]byte) error) error {
	s.note("begin")
	if s.onBegin != nil {
		s.onBegin()
	}
	if s.beginErr != nil {
		return s.beginErr
	}
	_ = sink([]byte(`{"dir":"c2s","frame":{"id":1,"method":"initialize"}}`))
	return nil
}
func (s *stubServer) StopRecording() error {
	s.note("stop")
	if s.onStop != nil {
		s.onStop()
	}
	return s.stopErr
}
func (s *stubServer) Handshake(ctx context.Context, ci ClientInfo) error {
	if s.hsGate != nil {
		<-s.hsGate
	}
	return s.hsErr
}
func (s *stubServer) Terminate() error {
	s.note("terminate")
	return nil
}
func (s *stubServer) Wait() proc.Exit {
	s.note("wait")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// note 記錄收尾相關呼叫的實際順序（§3.4.2 的順序斷言靠它）。
func (s *stubServer) note(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	switch call {
	case "begin":
		s.begins++
	case "stop":
		s.stops++
	case "terminate":
		s.terminates++
	case "wait":
		s.waits++
	}
}

func (s *stubServer) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *stubServer) StderrSnapshot() string { return "live-stderr" }
func (s *stubServer) Argv() []string         { return []string{"codex", "app-server"} }

func (s *stubServer) callCounts() (begins, stops, terminates, waits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.stops, s.terminates, s.waits
}
