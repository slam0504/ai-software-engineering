package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ---- B2c-5：supervisor 有界清理狀態機的白箱測試（承 B2c-4 §3 O1 凍結） ----
//
// 兩種真實 Proc 載體：
//   - 載體 A（carrierNoOrphanScript）：leader 退出後沒有任何 descendant 持有
//     stdout／stderr write end，write end 隨 leader 退出自然關閉。用於狀態機
//     終於 CleanupIncomplete=false 的案例——不需要真的觸發 fail-loud。
//   - 載體 B（carrierDescendantScript）：受控 descendant（`exec sleep 3600`）
//     繼承並持有 stdout／stderr write end、同一 PGID、不會自行退出。因為
//     cleanupSignal 已被 seam 取代，真實訊號不會送出，descendant 會一直存活；
//     reader 不可能提前 EOF，wg.Wait() 也不可能自行完成，只有 supervisor 的
//     強制關閉能讓 Wait()／Done() 收斂。測試收尾一律以真實
//     syscall.Kill(-pgid, SIGKILL) 清除（不依賴 seam），並用 test-local bounded
//     poll、只認 ESRCH（B2c-4 假設 A3：EPERM 可能仍有 P_REF_NEW 成員）。
const (
	carrierNoOrphanScript   = `echo ready; read -r _; exit 0`
	carrierDescendantScript = `bash -c 'exec sleep 3600' & echo ready; read -r _; exit 0`
)

// waitBounded：p.Wait() 的有界包裝，逾時視為測試失敗（供本檔各案共用）。
func waitBounded(t *testing.T, p *Proc, d time.Duration) Exit {
	t.Helper()
	ch := make(chan Exit, 1)
	go func() { ch <- p.Wait() }()
	select {
	case ex := <-ch:
		return ex
	case <-time.After(d):
		t.Fatal("Wait() 逾時")
		return Exit{}
	}
}

// killDescendantAndReap：真實 Proc（載體 B 與兩層 fork 測試）共用收尾。leader
// 可能已透過 seam 攔截自然清理路徑自行退出（p.exited 為 true，killAndReap 的送
// KILL 分支不會觸發），也可能是 cleanupGroup 本身未能收斂的退化案例（兩層 fork
// 測試 regression）——兩者持有 pipe write end 的 descendant 都需要真實 group
// SIGKILL 才會消失。先探測群組是否已消失，避免對已經 ESRCH 的群組再送一次訊號；
// bounded poll 只認 ESRCH，不把 EPERM 當已消失；最後對 p.Wait() 做獨立時限的
// 等待（經 waitBounded），任一段逾時都是測試失敗，不讓測試程序無限期掛住。
func killDescendantAndReap(t *testing.T, p *Proc) {
	t.Helper()
	if err := syscall.Kill(-p.PGID(), 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(-p.PGID(), syscall.SIGKILL) // 真實訊號：不經 cleanupSignal seam
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-p.PGID(), 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant 群組 %d 未在時限內消失（最後一次探測錯誤：%v）", p.PGID(), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitBounded(t, p, 5*time.Second) // 有界等待 supervisor 收尾；不直接呼叫 p.Wait() 以免卡死測試程序
}

// errQueue：依呼叫順序回放一串錯誤的 seam fake，用 channel／count 記錄避免
// -race；超出預期呼叫次數時回傳 ESRCH（安全終止任何迴圈），實際呼叫次數另外
// 用 calls() 斷言，不讓資料不符時掛住或 panic 跨 goroutine。
type errQueue struct {
	mu  sync.Mutex
	seq []error
	n   int
}

func newErrQueue(seq ...error) *errQueue { return &errQueue{seq: seq} }

func (q *errQueue) next() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.n
	q.n++
	if i < len(q.seq) {
		return q.seq[i]
	}
	return syscall.ESRCH
}

func (q *errQueue) calls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// instantAfter：立即觸發的 cleanupAfter fake，用於不需要驗證 barrier 的案例。
func instantAfter() afterFunc {
	return func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
}

// barrierTimer：cleanupAfter 的 seam。前 releaseAt-1 次呼叫立即觸發；第
// releaseAt 次呼叫先關閉 reached、再阻塞在 release，讓測試能在真正走到最終
// 確認之前，確認 stdout reader／Done() 尚未提前返回（案例 (iv)）。
type barrierTimer struct {
	n         int32
	releaseAt int32
	reached   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newBarrierTimer(releaseAt int32) *barrierTimer {
	return &barrierTimer{releaseAt: releaseAt, reached: make(chan struct{}), release: make(chan struct{})}
}

// Release 冪等釋放 barrier（sync.Once）：正常路徑與 t.Cleanup 都呼叫它，讓一次
// 失敗（t.Fatal 提前跳出、正常路徑的 close 沒機會執行）也不會讓卡在 <-b.release
// 的 supervisor goroutine 永久洩漏。
func (b *barrierTimer) Release() {
	b.once.Do(func() { close(b.release) })
}

func (b *barrierTimer) after(time.Duration) <-chan time.Time {
	n := atomic.AddInt32(&b.n, 1)
	if n == b.releaseAt {
		close(b.reached)
		<-b.release
	}
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

// countEvents 消費並分類事件 channel（呼叫後 channel 即關閉，不可再用）。
// cleanupGroup 內所有事件都在 supervisor goroutine 同步發出、發生於
// doneCh 關閉之前（happens-before），因此 p.Wait() 返回後立即消費即可，不需
// 額外等待。
func countEvents(events chan signalEvent) (cleanupKill, rekill, other int) {
	close(events)
	for ev := range events {
		switch ev {
		case sigEventSupervisorCleanupKill:
			cleanupKill++
		case sigEventSupervisorCleanupRekill:
			rekill++
		default:
			other++
		}
	}
	return
}

// ---- 六案（B2c-4 §3 O1／plan Step 3 (i)-(vi)），皆用載體 A ----

func TestCleanupStateMachineSixCases(t *testing.T) {
	cases := []struct {
		name            string
		signalSeq       []error // 依序：第一次 cleanup KILL，其後每次 rekill
		probeSeq        []error // 依序：每個排程偏移的探測
		wantCleanupKill int
		wantRekill      int
		wantIncomplete  bool
	}{
		{
			name:            "i_first_kill_nil_then_immediate_esrch",
			signalSeq:       []error{nil},
			probeSeq:        []error{syscall.ESRCH},
			wantCleanupKill: 1, wantRekill: 0, wantIncomplete: false,
		},
		{
			name:            "ii_first_kill_nil_three_rekills_then_esrch",
			signalSeq:       []error{nil, nil, nil, nil}, // 第一次 KILL + 3 次 rekill
			probeSeq:        []error{nil, nil, nil, syscall.ESRCH},
			wantCleanupKill: 1, wantRekill: 3, wantIncomplete: false,
		},
		{
			name:            "iii_first_kill_nil_eperm_eperm_esrch",
			signalSeq:       []error{nil},
			probeSeq:        []error{syscall.EPERM, syscall.EPERM, syscall.ESRCH},
			wantCleanupKill: 1, wantRekill: 0, wantIncomplete: false,
		},
		{
			name:            "v_first_kill_eperm_then_rekill_nil_then_esrch",
			signalSeq:       []error{syscall.EPERM, nil}, // 第一次 KILL（EPERM）+ rekill（成功）
			probeSeq:        []error{nil, syscall.ESRCH},
			wantCleanupKill: 0, wantRekill: 1, wantIncomplete: false,
		},
		{
			name:            "vi_first_kill_esrch_no_scheduling",
			signalSeq:       []error{syscall.ESRCH},
			probeSeq:        nil,
			wantCleanupKill: 0, wantRekill: 0, wantIncomplete: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runCleanupCase(t, c.name, c.signalSeq, c.probeSeq, c.wantCleanupKill, c.wantRekill, c.wantIncomplete)
		})
	}
}

func runCleanupCase(t *testing.T, name string, signalSeq, probeSeq []error, wantCleanupKill, wantRekill int, wantIncomplete bool) {
	t.Helper()
	p := bashProc(t, context.Background(), carrierNoOrphanScript, time.Second)
	t.Cleanup(func() { killAndReap(t, p) })
	buf := make([]byte, 8)
	if _, err := p.Stdout.Read(buf); err != nil {
		t.Fatal(err)
	}

	signalQ := newErrQueue(signalSeq...)
	probeQ := newErrQueue(probeSeq...)
	events := make(chan signalEvent, 32)
	durCh := make(chan time.Duration, 32)

	p.mu.Lock()
	p.onSignal = func(ev signalEvent) { events <- ev }
	p.cleanupSignal = func(int, syscall.Signal) error { return signalQ.next() }
	p.groupProbe = func(int) error { return probeQ.next() }
	p.cleanupAfter = func(d time.Duration) <-chan time.Time {
		durCh <- d
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	p.mu.Unlock()

	_, rd := drainStdout(p)
	if _, err := p.Stdin.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	ex := waitBounded(t, p, 3*time.Second)
	rd.Wait()

	if ex.CleanupIncomplete != wantIncomplete {
		t.Fatalf("%s: CleanupIncomplete = %v, want %v", name, ex.CleanupIncomplete, wantIncomplete)
	}
	cleanupKill, rekill, other := countEvents(events)
	if other != 0 {
		t.Fatalf("%s: 非預期事件種類數 = %d", name, other)
	}
	if cleanupKill != wantCleanupKill {
		t.Fatalf("%s: sigEventSupervisorCleanupKill = %d, want %d", name, cleanupKill, wantCleanupKill)
	}
	if rekill != wantRekill {
		t.Fatalf("%s: sigEventSupervisorCleanupRekill = %d, want %d", name, rekill, wantRekill)
	}
	if got := signalQ.calls(); got != len(signalSeq) {
		t.Fatalf("%s: cleanupSignal 呼叫次數 = %d, want %d", name, got, len(signalSeq))
	}
	if got := probeQ.calls(); got != len(probeSeq) {
		t.Fatalf("%s: groupProbe 呼叫次數 = %d, want %d", name, got, len(probeSeq))
	}
	close(durCh)
	var durs []time.Duration
	for d := range durCh {
		durs = append(durs, d)
	}
	if len(durs) != len(probeSeq) {
		t.Fatalf("%s: cleanupAfter 呼叫次數 = %d, want %d", name, len(durs), len(probeSeq))
	}
	for i, d := range durs {
		if d > cleanupOffsets[i] {
			t.Fatalf("%s: 第 %d 次 cleanupAfter duration = %v，超過偏移上限 %v（容許負值或略小）", name, i+1, d, cleanupOffsets[i])
		}
	}
}

// ---- errno table-driven subcases (a)-(f)（plan Step 3 之 7） ----

func TestCleanupErrnoTableSubcases(t *testing.T) {
	t.Run("a_final_probe_eperm_incomplete", func(t *testing.T) {
		runErrnoIncompleteCase(t, "a", syscall.EPERM, syscall.EPERM)
	})
	t.Run("b_final_probe_other_errno_incomplete", func(t *testing.T) {
		runErrnoIncompleteCase(t, "b", syscall.EPERM, syscall.EINVAL)
	})
	t.Run("c_rekill_esrch_stops_immediately", func(t *testing.T) {
		runCleanupCase(t, "c",
			[]error{nil, syscall.ESRCH}, // 第一次 KILL 成功、rekill 回 ESRCH
			[]error{nil},                // 只有一次探測（觸發 rekill）
			1, 0, false)
	})
	t.Run("d_rekill_eperm_continues", func(t *testing.T) {
		runCleanupCase(t, "d",
			[]error{nil, syscall.EPERM}, // rekill 回 EPERM：不發事件、繼續
			[]error{nil, syscall.ESRCH}, // 下一個偏移完成
			1, 0, false)
	})
	t.Run("e_rekill_other_errno_continues", func(t *testing.T) {
		runCleanupCase(t, "e",
			[]error{nil, syscall.EINVAL}, // rekill 回其他 errno：同 (d)
			[]error{nil, syscall.ESRCH},
			1, 0, false)
	})
	t.Run("f_first_kill_other_errno_still_scheduled", func(t *testing.T) {
		runCleanupCase(t, "f",
			[]error{syscall.EINVAL, nil}, // 第一次 KILL 回其他 errno：不發事件、仍排程；等同 (v) 的 EPERM 路徑
			[]error{nil, syscall.ESRCH},
			0, 1, false)
	})
}

// runErrnoIncompleteCase：subcase (a)(b) 專用——第一次 KILL 成功、10 個偏移的
// 探測皆回 offsetProbeErr（非 nil，不觸發 rekill）、最終 1 s 確認回
// finalProbeErr（非 ESRCH）→ CleanupIncomplete=true。用載體 B 讓 fail-loud
// 真的解除一個被受控 descendant 卡住的 reader，而不只是布林值巧合為真。
func runErrnoIncompleteCase(t *testing.T, name string, offsetProbeErr, finalProbeErr error) {
	t.Helper()
	p := bashProc(t, context.Background(), carrierDescendantScript, time.Second)
	t.Cleanup(func() { killDescendantAndReap(t, p) })
	buf := make([]byte, 8)
	if _, err := p.Stdout.Read(buf); err != nil {
		t.Fatal(err)
	}

	probeSeq := make([]error, 0, len(cleanupOffsets)+1)
	for range cleanupOffsets {
		probeSeq = append(probeSeq, offsetProbeErr)
	}
	probeSeq = append(probeSeq, finalProbeErr)
	signalQ := newErrQueue(nil) // 第一次 KILL 成功；offset probe 皆非 nil，不會觸發 rekill
	probeQ := newErrQueue(probeSeq...)
	events := make(chan signalEvent, 32)

	p.mu.Lock()
	p.onSignal = func(ev signalEvent) { events <- ev }
	p.cleanupSignal = func(int, syscall.Signal) error { return signalQ.next() }
	p.groupProbe = func(int) error { return probeQ.next() }
	p.cleanupAfter = instantAfter()
	p.mu.Unlock()

	readErrCh := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, p.Stdout); readErrCh <- err }()
	if _, err := p.Stdin.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}

	ex := waitBounded(t, p, 3*time.Second)
	if !ex.CleanupIncomplete {
		t.Fatalf("%s: CleanupIncomplete = false, want true", name)
	}
	if got := probeQ.calls(); got != len(probeSeq) {
		t.Fatalf("%s: groupProbe 呼叫次數 = %d, want %d", name, got, len(probeSeq))
	}
	if got := signalQ.calls(); got != 1 {
		t.Fatalf("%s: cleanupSignal 呼叫次數 = %d, want 1（offset probe 皆非 nil，不該觸發 rekill）", name, got)
	}
	cleanupKill, rekill, other := countEvents(events)
	if other != 0 {
		t.Fatalf("%s: 非預期事件種類數 = %d", name, other)
	}
	if cleanupKill != 1 {
		t.Fatalf("%s: sigEventSupervisorCleanupKill = %d, want 1", name, cleanupKill)
	}
	if rekill != 0 {
		t.Fatalf("%s: sigEventSupervisorCleanupRekill = %d, want 0", name, rekill)
	}
	select {
	case err := <-readErrCh:
		if !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("%s: stdout reader 錯誤必須滿足 errors.Is(ErrCleanupIncomplete)，實得 %v", name, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: stdout reader 必須在強制關閉後返回", name)
	}
}

// ---- 案例 (iv)：載體 B，第 11 次注入 timer 停在 barrier ----

func TestCleanupFinalConfirmBarrierBlocksReaderAndDoneUntilReleased(t *testing.T) {
	p := bashProc(t, context.Background(), carrierDescendantScript, time.Second)
	t.Cleanup(func() { killDescendantAndReap(t, p) })
	buf := make([]byte, 8)
	if _, err := p.Stdout.Read(buf); err != nil {
		t.Fatal(err)
	}

	events := make(chan signalEvent, 32)
	bt := newBarrierTimer(11) // 第 11 次 after() 呼叫＝1 s 最終確認
	// 註冊順序在 killDescendantAndReap 之後：t.Cleanup 是 LIFO，因此 barrier 會先
	// 被釋放（讓卡在 <-b.release 的 supervisor 繼續走完），group cleanup 才接手
	// 清乾淨的 descendant；顛倒順序的話 group cleanup 的 p.Wait() 會卡到自己的
	// 5 s 上限（B2c-5 review finding 2）。
	t.Cleanup(bt.Release)
	p.mu.Lock()
	p.onSignal = func(ev signalEvent) { events <- ev }
	p.cleanupSignal = func(int, syscall.Signal) error { return nil }
	p.groupProbe = func(int) error { return nil }
	p.cleanupAfter = bt.after
	p.mu.Unlock()

	readErrCh := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, p.Stdout); readErrCh <- err }()
	if _, err := p.Stdin.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-bt.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier 未在時限內觸發（第 11 次 cleanupAfter 呼叫）")
	}
	// barrier 尚未釋放：並行 stdout reader 與 Done() 都不該提前返回。
	select {
	case err := <-readErrCh:
		t.Fatalf("barrier 尚未釋放，stdout reader 不該提前返回：%v", err)
	default:
	}
	select {
	case <-p.Done():
		t.Fatal("barrier 尚未釋放，Done() 不該提前關閉")
	default:
	}

	bt.Release()
	ex := waitBounded(t, p, 3*time.Second)
	if !ex.CleanupIncomplete {
		t.Fatal("最終 probe 回 nil，必須 CleanupIncomplete=true")
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Wait() 返回後 Done() 必須已關閉")
	}
	select {
	case err := <-readErrCh:
		if !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("stdout reader 錯誤必須滿足 errors.Is(ErrCleanupIncomplete)，實得 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdout reader 必須在強制關閉後返回")
	}
	if strings.Contains(ex.StderrTail, "cleanup") || strings.Contains(ex.StderrTail, "incomplete") {
		t.Fatalf("stderr tail 不該包含 supervisor 自身敘述：%q", ex.StderrTail)
	}
	cleanupKill, rekill, other := countEvents(events)
	if other != 0 {
		t.Fatalf("非預期事件種類數 = %d", other)
	}
	if cleanupKill != 1 {
		t.Fatalf("sigEventSupervisorCleanupKill = %d, want 1", cleanupKill)
	}
	if rekill != 10 {
		t.Fatalf("sigEventSupervisorCleanupRekill = %d, want 10", rekill)
	}
}

// 對照案例（載體 B）：呼叫端在 barrier 釋放前自行 Close() stdout——reader 得到
// 的錯誤不得滿足 errors.Is(ErrCleanupIncomplete)，但 CleanupIncomplete 仍為
// true（揭露不受呼叫端關閉影響）。
func TestCleanupFinalConfirmBarrierCallerCloseBeforeReleaseNotMapped(t *testing.T) {
	p := bashProc(t, context.Background(), carrierDescendantScript, time.Second)
	t.Cleanup(func() { killDescendantAndReap(t, p) })
	buf := make([]byte, 8)
	if _, err := p.Stdout.Read(buf); err != nil {
		t.Fatal(err)
	}

	bt := newBarrierTimer(11)
	// 註冊順序理由同上一案：LIFO 讓 barrier 先於 group cleanup 釋放。
	t.Cleanup(bt.Release)
	p.mu.Lock()
	p.cleanupSignal = func(int, syscall.Signal) error { return nil }
	p.groupProbe = func(int) error { return nil }
	p.cleanupAfter = bt.after
	p.mu.Unlock()

	readErrCh := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, p.Stdout); readErrCh <- err }()
	if _, err := p.Stdin.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-bt.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier 未在時限內觸發")
	}
	if err := p.Stdout.Close(); err != nil {
		t.Fatalf("呼叫端 Close() 失敗：%v", err)
	}
	bt.Release()

	ex := waitBounded(t, p, 3*time.Second)
	if !ex.CleanupIncomplete {
		t.Fatal("CleanupIncomplete 必須仍為 true（揭露不受呼叫端關閉影響）")
	}
	select {
	case err := <-readErrCh:
		if errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("呼叫端已先 Close()，讀取錯誤不得映射為 ErrCleanupIncomplete，實得 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdout reader 必須返回")
	}
}

// ---- Step 3b：兩層 fork 真實程序測試（B2c-4 凍結；供 B2c-7 以名稱指定 ×400）----

// TestSupervisorCleanupTwoLayerForkOrphan：與 testdata/fake-claude.sh:16 同形
// 的兩層 fork（子 shell 執行 AND-list 再 fork bash -c），不用任何 seam（真實
// 時鐘、真實訊號），驗證有界清理在既有 guard 形狀內仍能收斂。本機預期穩定
// PASS；CI 上的收斂效果由 B2c-7 以 ×400 驗證。
func TestSupervisorCleanupTwoLayerForkOrphan(t *testing.T) {
	const script = `[ -n "x" ] && bash -c 'trap "" TERM; sleep 30' & echo out; echo err >&2; exit 5`
	p := bashProc(t, context.Background(), script, time.Second)
	// killAndReap 故意在 p.exited 為 true 時不再送訊號（見其 doc），regression
	// 使 cleanupGroup 未能收斂時無法清掉殘存 descendant 群組；改用
	// killDescendantAndReap，它一律先探測、必要時真的送 group SIGKILL。
	t.Cleanup(func() { killDescendantAndReap(t, p) })
	out, rd := drainStdout(p)
	ex := waitBounded(t, p, 5*time.Second)
	if ex.Code != 5 {
		t.Fatalf("code = %d, want 5", ex.Code)
	}
	if ex.CleanupIncomplete {
		t.Fatal("兩層 fork orphan 案例預期本機穩定 PASS，不應 CleanupIncomplete")
	}
	rd.Wait()
	if !strings.Contains(out.String(), "out") {
		t.Fatalf("stdout = %q", out.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(-p.PGID(), 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group 未在 2s 內消失（最後一次探測錯誤：%v）", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- helper 單測：stdoutReader（B2c-4 D2／D8） ----

func TestStdoutReaderReadMapsOnlyWhenForcedAndNotCallerClosed(t *testing.T) {
	cases := []struct {
		name               string
		forcedClosed       bool
		callerClosedStdout bool
		wantMapped         bool
	}{
		{"neither_closed_by_supervisor_nor_caller", false, false, false},
		{"forced_only", true, false, true},
		{"forced_and_caller_closed", true, true, false},
		{"caller_closed_only", false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			_ = r.Close() // 讓 Read 回傳包了 os.ErrClosed 的錯誤
			p := &Proc{forcedClosed: c.forcedClosed, callerClosedStdout: c.callerClosedStdout}
			sr := &stdoutReader{f: r, p: p}
			_, rerr := sr.Read(make([]byte, 8))
			if rerr == nil {
				t.Fatal("want error, got nil")
			}
			if got := errors.Is(rerr, ErrCleanupIncomplete); got != c.wantMapped {
				t.Fatalf("errors.Is(err, ErrCleanupIncomplete) = %v, want %v（err=%v）", got, c.wantMapped, rerr)
			}
		})
	}
}

func TestStdoutReaderCloseIsIdempotentAndNormalizesErrClosed(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	p := &Proc{}
	sr := &stdoutReader{f: r, p: p}
	if err := sr.Close(); err != nil {
		t.Fatalf("第一次 Close() 必須成功，實得 %v", err)
	}
	p.mu.Lock()
	caller := p.callerClosedStdout
	p.mu.Unlock()
	if !caller {
		t.Fatal("Close() 必須設定 callerClosedStdout")
	}
	if err := sr.Close(); err != nil {
		t.Fatalf("連續第二次 Close() 必須把 os.ErrClosed 正規化為 nil，實得 %v", err)
	}
}

// TestStdoutReaderCloseOrderingDeterminesMapping：caller-first／supervisor-first
// 兩種順序各自的映射結果（B2c-4 D8）。
func TestStdoutReaderCloseOrderingDeterminesMapping(t *testing.T) {
	t.Run("caller_first_then_forced", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		p := &Proc{}
		sr := &stdoutReader{f: r, p: p}
		if err := sr.Close(); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		p.forcedClosed = true // 模擬 supervisor 稍後才強制關閉
		p.mu.Unlock()
		_, rerr := sr.Read(make([]byte, 8))
		if rerr == nil {
			t.Fatal("want error, got nil")
		}
		if errors.Is(rerr, ErrCleanupIncomplete) {
			t.Fatalf("呼叫端先 Close()，之後的 Read 不得映射為 ErrCleanupIncomplete：%v", rerr)
		}
	})
	t.Run("forced_first_then_caller_close", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = w.Close() }()
		p := &Proc{}
		sr := &stdoutReader{f: r, p: p}
		p.mu.Lock()
		p.forcedClosed = true
		p.mu.Unlock()
		_ = r.Close() // 模擬 supervisor 直接關底層（生產路徑：outR.Close()）
		_, rerr := sr.Read(make([]byte, 8))
		if !errors.Is(rerr, ErrCleanupIncomplete) {
			t.Fatalf("supervisor 先強制關閉，Read 必須映射為 ErrCleanupIncomplete，實得 %v", rerr)
		}
		if err := sr.Close(); err != nil {
			t.Fatalf("呼叫端事後 Close() 必須成功（正規化已關閉錯誤），實得 %v", err)
		}
	})
}

// ---- helper 單測：classifyKillErr ----

func TestClassifyKillErr(t *testing.T) {
	wrappedESRCH := fmt.Errorf("wrap: %w", syscall.ESRCH)
	cases := []struct {
		name                 string
		err                  error
		esrch, eperm, nilErr bool
	}{
		{"nil", nil, false, false, true},
		{"esrch", syscall.ESRCH, true, false, false},
		{"eperm", syscall.EPERM, false, true, false},
		{"wrapped_esrch", wrappedESRCH, true, false, false},
		{"other_errno", syscall.EINVAL, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			esrch, eperm, nilErr := classifyKillErr(c.err)
			if esrch != c.esrch || eperm != c.eperm || nilErr != c.nilErr {
				t.Fatalf("got (esrch=%v,eperm=%v,nilErr=%v), want (%v,%v,%v)",
					esrch, eperm, nilErr, c.esrch, c.eperm, c.nilErr)
			}
		})
	}
}
