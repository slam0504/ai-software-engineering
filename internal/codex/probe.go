package codex

import (
	"context"
	"errors"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

// probeTarget 是 B1 handshake probe 的最小介面（*Server 以薄委派滿足；測試用 stub）。
type probeTarget interface {
	Alive
	BeginRecording(sink func([]byte) error) error
	StopRecording() error
	Handshake(ctx context.Context, ci ClientInfo) error
	Terminate() error
	Wait() proc.Exit
	StderrSnapshot() string
	Argv() []string
}

// RunHandshakeProbe 是 B1 受控重啟 probe 的編排（RestartCodexServerRecorded 的可測核心）。
// 整段 replacement 在 single.WithExclusive 的單一互斥交易內完成（v1.9 P0）：
// probe 進行中 Ensure／Take 一律阻塞，不存在無主 server 競態。
// 資源歸屬（v1.8 P0）：Start 成功後、Handshake 成功前的任何失敗——含 BeginRecording
// 失敗——一律 Terminate → Wait → ownership 清空，以 Exit 填 meta。
func RunHandshakeProbe[T probeTarget](ctx context.Context, single *Single[T],
	newRec func() (*recorder.Recorder, error), start func() (T, error), ci ClientInfo) error {
	return single.WithExclusive(func(cur T, ok bool) (T, bool, error) {
		var zero T
		if ok { // 1. 被替換的每個 server 都有 dispose
			_ = cur.Terminate()
			cur.Wait()
		}
		rec, err := newRec()
		if err != nil { // 2. 尚無新 server
			return zero, false, err
		}
		srv, err := start()
		if err != nil { // 3. meta 記錯誤、無 exit_code
			cerr := rec.CloseWith(recorder.Meta{Provider: "codex",
				RecordedAt: time.Now().UTC().Format(time.RFC3339),
				StderrTail: "probe start failed: " + err.Error()})
			return zero, false, errors.Join(err, cerr)
		}
		dispose := func(stage error) (T, bool, error) { // Terminate → Wait → meta 以 Exit 填值
			_ = srv.Terminate()
			ex := srv.Wait()
			cerr := rec.CloseWith(recorder.Meta{Provider: "codex", Argv: srv.Argv(),
				RecordedAt: time.Now().UTC().Format(time.RFC3339),
				ExitCode:   &ex.Code, StderrTail: ex.StderrTail})
			return zero, false, errors.Join(stage, cerr)
		}
		if err := srv.BeginRecording(rec.Line); err != nil { // 4. 不得留下未 handshake 的 server
			return dispose(err)
		}
		if err := srv.Handshake(ctx, ci); err != nil { // 5. Stop → dispose
			stopErr := srv.StopRecording()
			t, keep, jerr := dispose(err)
			return t, keep, errors.Join(stopErr, jerr)
		}
		// 6. 成功：Stop → CloseWith（still running）→ keep=true 回填
		stopErr := srv.StopRecording()
		cerr := rec.CloseWith(recorder.Meta{Provider: "codex", Argv: srv.Argv(),
			RecordedAt:          time.Now().UTC().Format(time.RFC3339),
			ProcessStillRunning: true, StderrTail: srv.StderrSnapshot()})
		return srv, true, errors.Join(stopErr, cerr)
	})
}
