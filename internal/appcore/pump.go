package appcore

import (
	"errors"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
)

// Pump 把事件 channel 逐一送進 emit；channel 關閉時關閉回傳的 done。
func Pump(events <-chan contract.Event, emit func(contract.Event)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			emit(ev)
		}
	}()
	return done
}

// WaitQuiesce 等 pump 結束；逾時回 error（呼叫端據此升級 Terminate）。
func WaitQuiesce(done <-chan struct{}, timeout time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("appcore: pump quiesce timeout")
	}
}

// CloseSequence：claude session 收尾固定順序（正常與 Terminate 升級路徑共用）：
//
//	close() → WaitQuiesce(done, quiesceTimeout)
//	  逾時 → terminate()，再以 killTimeout 等第二次；仍未關 →
//	  不呼叫 wait()（可能同樣阻塞），以 Exit{Exited:false} 盡力 finalize——
//	  稽核端（meta ExitCode）維持 nil；回含兩次 timeout 的 error
//	  （quiesce timeout 一律保留，不被 terminate 吞掉）。
//	done 已關 → exit = wait()（cached Exit）→ finalize(exit)。
func CloseSequence(closeFn func() error, done <-chan struct{},
	quiesceTimeout, killTimeout time.Duration,
	terminate func() error, wait func() ports.Exit,
	finalize func(ports.Exit) error) (ports.Exit, error) {
	closeErr := closeFn()
	qErr := WaitQuiesce(done, quiesceTimeout) // 原始 timeout 一律保留
	var termErr error
	if qErr != nil {
		termErr = terminate()
		if killErr := WaitQuiesce(done, killTimeout); killErr != nil {
			// pump 卡死：wait() 可能同樣阻塞——以 Exit{Exited:false} 盡力 finalize
			unknown := ports.Exit{Exited: false}
			finErr := finalize(unknown)
			return unknown, errors.Join(closeErr, qErr, termErr,
				errors.New("appcore: pump did not quiesce after terminate"), finErr)
		}
	}
	ex := wait() // done 已關：cached Exit（finalize 的 exit 證據）
	finErr := finalize(ex)
	return ex, errors.Join(closeErr, qErr, termErr, finErr)
}

var ErrProviderBusy = errors.New("appcore: provider busy; cannot end session now")

// EndSessionFlow：EndSession 的單一編排（per workspace session）。busyCheck 為
// teardown 前置檢查（nil = 無）；true → CancelEndSession + ErrProviderBusy（phase
// 復原、Cancel 錯誤保留）。teardown 一旦開始，無論成敗都 FinishEndSession；回傳
// errors.Join(teardownErr, finishErr)。無 active session 冪等回 nil。
//
// M3b §3.3：slot 一律由 WSID 解析（原 provider-keyed 相容層已於 Task 9 刪除），
// 同一 provider 的多個 session 因此各自獨立收尾。未知／未 commit 的 WSID 回
// ErrSessionNotFound（fail loud，不當成「無 session」冪等吞掉）。
func EndSessionFlow(m *Manager, w WSID, busyCheck func() bool, teardown func() error) error {
	tok, err := m.BeginEndSession(w)
	if errors.Is(err, ErrNoSession) {
		return nil // 冪等
	}
	if err != nil {
		return err
	}
	if busyCheck != nil && busyCheck() {
		cerr := m.CancelEndSession(w, tok) // Cancel 錯誤保留、不吞
		return errors.Join(ErrProviderBusy, cerr)
	}
	tearErr := teardown()
	finErr := m.FinishEndSession(w, tok)
	return errors.Join(tearErr, finErr)
}
