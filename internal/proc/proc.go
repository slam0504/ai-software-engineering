package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Binary    string
	Args      []string
	Dir       string
	Env       []string      // 附加於 os.Environ()
	TermGrace time.Duration // 預設 5s
}

type Exit struct {
	Code       int
	StderrTail string
	Err        error
	// CleanupIncomplete：supervisor 的有界清理在 1 s 預算內未能確認 process group
	// 消失（B2c-4 §3 O1）。true 時 stdout／stderr 的本端 read end 已被強制關閉，
	// Err 維持既有「子程序死因」語意、不混入 cleanup 狀態。
	CleanupIncomplete bool
}

// ErrCleanupIncomplete：supervisor 強制解除本端 pipe 等待時，p.Stdout 的讀取錯誤
// 以此包裝，讓呼叫端能用 errors.Is 分辨「supervisor 放棄清理」與其他 I/O 錯誤
// （B2c-4 D3／D8）。
var ErrCleanupIncomplete = errors.New("proc: cleanup incomplete: process group not confirmed dead within 1s budget; stdout/stderr force-closed")

// Proc 以獨立 process group 啟動子程序，背景 supervisor 是唯一收尾路徑：子程序
// 一退出即送 group SIGKILL，並在固定時間表內重送與確認（有界清理，B2c-4 §3
// O1）——不保證單次或有限次 KILL 就讓群組終止；1 s 預算內無法確認消失時，強制
// 解除本端對 stdout／stderr 的等待並以 Exit.CleanupIncomplete 揭露，讓 Wait()／
// Done() 有界收斂。
// 契約（v1.6）：呼叫端必須在 Start 後並行持續汲取 Stdout——supervisor 不做 stdout
// spool，若無人讀，子程序輸出超過 pipe buffer 會卡在 write、永不退出。
type Proc struct {
	cmd      *exec.Cmd
	pgid     int
	grace    time.Duration
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	mu       sync.Mutex
	stderr   []byte // 最後 64KB
	exit     Exit
	exitedCh chan struct{} // 子程序本體已退出
	doneCh   chan struct{} // Exit 已快取（stderr 收完）
	// exited／canceled：兩者都在 p.mu 底下，因此有**因果關係**而不只是沒有 data
	// race（reviewer 2026-08-20）。
	//
	// select 在兩個 channel 同時 ready 時是隨機挑的，所以「取消分支被選到」不代表
	// 取消真的影響了什麼——實測 10,000 次會重現 /usr/bin/true 已自然結束、稍後的
	// 取消卻把結果變成 context canceled。判準因此改成：**在同一個臨界區內**確認
	// 「退出尚未被記錄」，才標記取消並終止；退出已被記錄就什麼都不做。
	exited    bool
	canceled  bool
	exitReady bool // exit 已寫入（在 p.mu 下）
	// 死因仲裁的證據（reviewer 2026-08-21）：ExitCode()==-1 只說「被訊號收掉」，
	// 分不出訊號是子程序自己造成（kill -KILL $$、SIGSEGV）還是我們的 Terminate。
	// 所以記下「我們實際送過哪些訊號」與「致死的是哪個訊號」，讓 CanceledByContext
	// 能做事實比對，而不是把所有訊號死亡都記到取消頭上。
	termSent bool           // Terminate 路徑真的送出過 group SIGTERM
	killSent bool           // grace 逾時升級真的送出過 group SIGKILL（退出後的清掃 KILL 不算：影響不了已定案的死因）
	fatalSig syscall.Signal // 子程序被訊號致死時的那個訊號；0 = 正常退出
	// after／onSignal：backlog B1a-1 的未匯出 timer／signal-event seam，僅同套件
	// white-box 測試可注入（proc_test.go）。讀寫一律經 seamAfter／seamOnSignal 在
	// p.mu 下完成；回傳值本身在解鎖後才被呼叫／使用，避免 observer 持鎖呼叫、也
	// 避免與測試注入的欄位寫入產生 -race。nil 時分別退回 real timer（time.After）
	// 與 no-op。
	after    afterFunc
	onSignal signalObserverFunc
	// cleanupAfter／groupProbe／cleanupSignal：B2c-5 有界清理狀態機的三個 nil-safe
	// seam（沿 B1a-1 seamAfter／seamOnSignal 慣例），僅同套件 white-box 測試可
	// 注入。cleanupAfter 與 after 分離，避免與 Terminate() 的 escalation timer
	// 注入互相干擾。cleanupSignal 只用於 cleanupGroup 路徑；Terminate／escalation
	// 仍走 SignalGroup。nil 時分別退回 time.After／syscall.Kill(-pgid, 0)／
	// syscall.Kill(-pgid, sig)。
	cleanupAfter  afterFunc
	groupProbe    func(pgid int) error
	cleanupSignal func(pgid int, sig syscall.Signal) error
	// forcedClosed／callerClosedStdout：B2c-4 D8 的薄包裝旗標，皆在 p.mu 下讀寫。
	// forcedClosed 由 supervisor 在有界清理預算耗盡時設定；callerClosedStdout 由
	// stdoutReader.Close() 設定，先設旗標再關底層，讓「呼叫端先 Close()」的意圖
	// 優先於強制關閉的錯誤映射。
	forcedClosed       bool
	callerClosedStdout bool
}

// stdoutReader 是 p.Stdout 的薄包裝（型別對外仍為 io.ReadCloser，B2c-4 D2）：
// Read 只有在 supervisor 已設定 forcedClosed 且呼叫端尚未自行 Close() 時，才把
// 底層的 os.ErrClosed 映射為 ErrCleanupIncomplete；Close 先在 p.mu 下設
// callerClosedStdout，再關底層，並把 os.ErrClosed 正規化為 nil（其他錯誤照常
// 回傳），讓連續兩次 Close() 皆成功。
type stdoutReader struct {
	f *os.File
	p *Proc
}

func (r *stdoutReader) Read(b []byte) (int, error) {
	n, err := r.f.Read(b)
	if err != nil && errors.Is(err, os.ErrClosed) {
		r.p.mu.Lock()
		forced, callerClosed := r.p.forcedClosed, r.p.callerClosedStdout
		r.p.mu.Unlock()
		if forced && !callerClosed {
			return n, fmt.Errorf("%w: %v", ErrCleanupIncomplete, err)
		}
	}
	return n, err
}

func (r *stdoutReader) Close() error {
	r.p.mu.Lock()
	r.p.callerClosedStdout = true
	r.p.mu.Unlock()
	err := r.f.Close()
	if err != nil && errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// afterFunc 是 Terminate() 的計時器 seam；型別對齊 internal/appcore/pump.go 的
// After／RealAfter 慣例。nil 時退回 time.After（見 seamAfter）。
type afterFunc func(time.Duration) <-chan time.Time

// signalEvent 區分 Proc 內部「實際送出過訊號」的時刻——只在對應的 SignalGroup／
// cleanupSignal 呼叫成功後才發出。
type signalEvent int

const (
	sigEventTermSent                signalEvent = iota // Terminate() 的 group SIGTERM 送出成功
	sigEventEscalationKill                             // Terminate() grace 逾時升級的 group SIGKILL 送出成功
	sigEventSupervisorCleanupKill                      // supervisor 收尾管線的第一次清孫程序 group SIGKILL 送出成功
	sigEventSupervisorCleanupRekill                    // 有界清理排程中的重送 group SIGKILL 送出成功（只在實際成功送出時發；B2c-4 D9：不新增 gave-up 事件）
)

// signalObserverFunc 是訊號事件的 seam；nil 時退回 no-op（見 seamOnSignal）。
// 呼叫規約：不得在持有 p.mu 時呼叫，也不參與任何 production 判定——純觀察用途。
type signalObserverFunc func(signalEvent)

// seamAfter／seamOnSignal：nil-safe 存取子。欄位讀取在 p.mu 下完成，但回傳值
// 本身在解鎖後才被呼叫／使用。
func (p *Proc) seamAfter() afterFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.after != nil {
		return p.after
	}
	return time.After
}

func (p *Proc) seamOnSignal() signalObserverFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onSignal != nil {
		return p.onSignal
	}
	return func(signalEvent) {}
}

// seamCleanupAfter／seamGroupProbe／seamCleanupSignal：B2c-5 有界清理狀態機的
// nil-safe 存取子，規約同 seamAfter／seamOnSignal——欄位讀取在 p.mu 下完成，
// 回傳值在解鎖後才被呼叫。
func (p *Proc) seamCleanupAfter() afterFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleanupAfter != nil {
		return p.cleanupAfter
	}
	return time.After
}

func (p *Proc) seamGroupProbe() func(int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.groupProbe != nil {
		return p.groupProbe
	}
	return func(pgid int) error { return syscall.Kill(-pgid, 0) }
}

func (p *Proc) seamCleanupSignal() func(int, syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleanupSignal != nil {
		return p.cleanupSignal
	}
	return func(pgid int, sig syscall.Signal) error { return syscall.Kill(-pgid, sig) }
}

// classifyKillErr 把 kill(-pgid, ...) 的回傳值分類為 B2c-4 §3 O1 狀態機的三路：
// esrch（群組已消失）、eperm（可能是 P_REF_NEW 建立窗口，見 B2c-4 假設 A3，非
// 終止）、nilErr（送出／探測成功）。其他 errno 三者皆為 false，與 eperm 同樣視
// 為非終止、繼續排程。用 errors.Is 而非型別斷言，涵蓋 wrap 過的錯誤。
func classifyKillErr(err error) (esrch, eperm, nilErr bool) {
	if err == nil {
		return false, false, true
	}
	if errors.Is(err, syscall.ESRCH) {
		return true, false, false
	}
	if errors.Is(err, syscall.EPERM) {
		return false, true, false
	}
	return false, false, false
}

// cleanupOffsets／cleanupFinalConfirm：B2c-4 D2 凍結的固定絕對偏移時間表，
// 不開放 Config 覆寫。
var cleanupOffsets = []time.Duration{
	1 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond,
	16 * time.Millisecond, 32 * time.Millisecond, 64 * time.Millisecond, 128 * time.Millisecond,
	256 * time.Millisecond, 512 * time.Millisecond,
}

const cleanupFinalConfirm = time.Second

// cleanupGroup 實作 B2c-4 §3 O1 凍結的有界清理狀態機：第一次 cleanup KILL
// **返回**的 monotonic 時刻為 base（不限成功），依 base 的固定絕對偏移探測群組
// 是否消失並視情況重送，最後於 1 s 做不送訊號的確認。回傳 incomplete=true 代表
// 預算耗盡、呼叫端須強制解除本端 pipe 等待（fail-loud，B2c-4 D3）。事件只在
// 對應的 cleanupSignal 呼叫**成功**（nilErr）時發、且在鎖外發。
func (p *Proc) cleanupGroup() (incomplete bool) {
	signal := p.seamCleanupSignal()
	probe := p.seamGroupProbe()
	after := p.seamCleanupAfter()

	firstErr := signal(p.pgid, syscall.SIGKILL)
	base := time.Now()
	esrch, _, nilErr := classifyKillErr(firstErr)
	if esrch {
		return false // 群組確定不存在：完成，不啟動排程
	}
	if nilErr {
		p.seamOnSignal()(sigEventSupervisorCleanupKill)
	}
	// EPERM／其他 errno：不發事件，仍啟動排程。

	for _, off := range cleanupOffsets {
		<-after(time.Until(base.Add(off)))
		perr := probe(p.pgid)
		pesrch, _, pnilErr := classifyKillErr(perr)
		if pesrch {
			return false
		}
		if !pnilErr {
			continue // EPERM／其他 errno：本輪不送 KILL，繼續下一個偏移
		}
		rerr := signal(p.pgid, syscall.SIGKILL)
		resrch, _, rnilErr := classifyKillErr(rerr)
		switch {
		case rnilErr:
			p.seamOnSignal()(sigEventSupervisorCleanupRekill)
		case resrch:
			return false
		default:
			// rekill 回 EPERM／其他 errno：不發事件，繼續下一個偏移。
		}
	}

	<-after(time.Until(base.Add(cleanupFinalConfirm)))
	finalErr := probe(p.pgid)
	fesrch, _, _ := classifyKillErr(finalErr)
	return !fesrch // 非 ESRCH（nil／EPERM／其他 errno）一律 CleanupIncomplete
}

const stderrCap = 64 * 1024

func (p *Proc) appendStderr(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stderr = append(p.stderr, b...)
	if n := len(p.stderr); n > stderrCap {
		p.stderr = p.stderr[n-stderrCap:]
	}
}

func (p *Proc) stderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.stderr)
}

func Start(ctx context.Context, cfg Config) (*Proc, error) {
	// **進場 fail fast**：ctx 已取消就連 child 都不該起。先前這道只在 Output 有，
	// internal/claude、codex、assist 直接走 Start 的路徑照樣會啟動有副作用的子程序
	// 再由 watcher 事後終止（reviewer 2026-08-21）。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(cfg.Binary, cfg.Args...) // 不用 CommandContext：ctx 取消必須殺整組（見下）
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// 自建 os.Pipe，不用 cmd.StdoutPipe：cmd.Wait 會在程序退出時關閉 StdoutPipe，
	// 孫程序尚未寫完的輸出會被截走；自建 pipe + group SIGKILL 保證
	// 「所有 write end 關閉 → reader 讀到 EOF」的順序成立。
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil { // binary 不存在等啟動失敗在這裡浮現
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, err
	}
	outW.Close() // 父程序不留 write end，否則 EOF 永不到來
	errW.Close()

	grace := cfg.TermGrace
	if grace == 0 {
		grace = 5 * time.Second
	}
	p := &Proc{cmd: cmd, pgid: cmd.Process.Pid, grace: grace, Stdin: stdin,
		exitedCh: make(chan struct{}), doneCh: make(chan struct{})}
	p.Stdout = &stdoutReader{f: outR, p: p}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stderr reader
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := errR.Read(buf)
			if n > 0 {
				p.appendStderr(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	go func() { // supervisor：唯一呼叫 cmd.Wait 的地方
		werr := cmd.Wait()
		p.mu.Lock()
		p.exited = true // 先記錄事實，再開放觀察（見 exited 欄位 doc）
		p.mu.Unlock()
		close(p.exitedCh)
		incomplete := p.cleanupGroup() // 有界清理狀態機（B2c-4 §3 O1）：子程序已退出 → 清整組殘存孫程序
		if incomplete {
			// 預算耗盡：fail-loud——強制解除本端對 stdout／stderr 的等待，讓
			// wg.Wait() 與呼叫端的 reader 有界收斂（B2c-4 D3／D8）。
			p.mu.Lock()
			p.forcedClosed = true
			p.mu.Unlock()
			_ = errR.Close() // 強制解除等待：底層 close 錯誤與 fail-loud 判斷無關
			_ = outR.Close()
		}
		wg.Wait()        // stderr 讀到 EOF（group kill 保證，或強制關閉造成的 closed error）
		_ = errR.Close() // 冪等：已被上面的強制關閉路徑關過時，Close 只回已忽略的 os.ErrClosed
		ex := Exit{Code: cmd.ProcessState.ExitCode(), StderrTail: p.stderrTail(), Err: werr, CleanupIncomplete: incomplete}
		var fatal syscall.Signal // 0 = 正常退出
		if ws, isWS := cmd.ProcessState.Sys().(syscall.WaitStatus); isWS && ws.Signaled() {
			fatal = ws.Signal()
		}
		p.mu.Lock()
		p.exit, p.exitReady, p.fatalSig = ex, true, fatal
		p.mu.Unlock()
		close(p.doneCh)
	}()
	go func() { // 覆寫 ctx 取消語意：走 Terminate（整組），不是單程序 kill
		select {
		case <-ctx.Done():
			p.cancelRequested()
		case <-p.exitedCh:
		}
	}()
	return p, nil
}

// cancelRequested：ctx 取消當下的決策。抽成具名方法，讓「退出已被記錄就什麼都
// 不做」有確定性 oracle（TestCancelRequestedAfterRecordedExitIsANoOp）——先前這段
// 內嵌在 watcher 裡，把 `!p.exited` mutation 成永遠 terminate 沒有任何測試會紅
// （reviewer 2026-08-21）。
//
// 在同一個臨界區內確認「退出尚未被記錄」才標記取消並終止；已記錄就不再對 group
// 送訊號——那一組已經死了，pgid 可能已被重用。
func (p *Proc) cancelRequested() {
	p.mu.Lock()
	terminate := !p.exited // 已經自然結束的話，這次取消什麼都沒改變
	if terminate {
		p.canceled = true
	}
	p.mu.Unlock()
	if terminate {
		_ = p.Terminate()
	}
}

func (p *Proc) SignalGroup(sig syscall.Signal) error { return syscall.Kill(-p.pgid, sig) }

func (p *Proc) PGID() int { return p.pgid }

// StderrSnapshot 回傳目前的 stderr tail（v1.6：長駐程序仍在跑時取證用，不等待退出）。
func (p *Proc) StderrSnapshot() string { return p.stderrTail() }

// CanceledByContext：這次執行是不是**因為 ctx 取消而被終止**（而不是自己跑完）。
//
// 三層判定，因為「取消分支被選到」證明不了因果（reviewer 2026-08-20／2026-08-21）：
//
//	(1) 取消當下 p.exited 尚未被記錄（在 p.mu 內確認，見 cancelRequested）——避免
//	    對已經結束的行程再送一次訊號。這一層是防禦，不是判定：子程序已經死、
//	    cmd.Wait 尚未返回的窗口它擋不掉。
//	(2) 正常退出（**任何** exit code，不只 0）就不算被取消——正常退出不是訊號收
//	    掉的。
//	(3) 訊號致死時比對死因：只有死於**我們真的送出過**的那個訊號（Terminate 的
//	    TERM、grace 逾時升級的 KILL）才算被取消。子程序自己 kill -KILL $$、SIGSEGV
//	    這類自然 crash 恰與取消交錯時，先前一律被記到取消頭上（reviewer
//	    2026-08-21：199/200 次錯分類）。
//
// **已知取捨**：
//   - 子程序攔下 TERM 後正常退出（任何 code）會被判成「沒有被取消」。對呼叫端而言
//     「它自己正常收工」與「被我們要求收工而正常收工」在結果上等價。
//   - 我們送過 TERM（或升級 KILL）而子程序**同時**自己死於同名訊號，兩者在 wait
//     status 上無法區分，會被判成「被取消」。這一格比先前「所有訊號死亡都算取消」
//     窄得多，且只在取消真的送過訊號時才可能發生。
//   - 取消送的是 **group** 訊號：即使 leader 攔下 TERM 正常退出、被判成「沒有被
//     取消」，孫程序仍可能已被那次 group TERM（與退出後的清掃 KILL）收掉——分類
//     說的是 leader 的死因，不代表 group 沒被打擾。
func (p *Proc) CanceledByContext() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.canceled {
		return false
	}
	if !p.exitReady {
		return true // 取消已觸發終止、結局尚未記錄——保守判為被取消
	}
	if p.exit.Code >= 0 { // 正常退出：ExitCode() 對被訊號終止者回 -1
		return false
	}
	switch p.fatalSig { // 死因仲裁：只認我們真的送過的訊號
	case syscall.SIGTERM:
		return p.termSent
	case syscall.SIGKILL:
		return p.killSent
	default:
		return false // 死於我們沒送過的訊號＝自然 crash
	}
}

// Done 在 Exit 已快取（stderr EOF 或有界清理強制解除本端 pipe 等待其一，B2c-4
// D8）後關閉；select-default 即為非阻塞存活判定（v1.7）。
func (p *Proc) Done() <-chan struct{} { return p.doneCh }

func (p *Proc) Terminate() error { // group SIGTERM → grace 內未退出 → group SIGKILL
	// 送訊號與記錄 termSent 在**同一個臨界區**、且只有 syscall 成功才記錄
	// （reviewer 2026-08-21 第二輪）：先記再送的話，送失敗（group 已消失）也會留下
	// 「送過 TERM」的假事實，同名的自然 signal death 就可能被誤判成取消；記錄若在
	// 鎖外，supervisor 也可能搶在 termSent 落地前公布 exitReady，讓
	// CanceledByContext 讀到半套事實。syscall.Kill 不阻塞，短暫持鎖可接受。
	p.mu.Lock()
	if p.exited { // 退出已記錄：那一組已死、pgid 可能被重用，不再送訊號
		p.mu.Unlock()
		return nil
	}
	err := p.SignalGroup(syscall.SIGTERM)
	if err == nil {
		p.termSent = true
	}
	p.mu.Unlock()
	if err != nil {
		return err // group 已不可達：不記錄、也不排 KILL 升級（pgid 重用風險）
	}
	// 事件只在 SignalGroup 成功後、解鎖後才發。
	p.seamOnSignal()(sigEventTermSent)
	after := p.seamAfter() // 在 goroutine 啟動前固定，避免與後續欄位寫入產生 -race
	go func() {
		select {
		case <-p.exitedCh:
		case <-after(p.grace):
			p.mu.Lock()
			killed := false
			if !p.exited {
				if p.SignalGroup(syscall.SIGKILL) == nil {
					p.killSent = true
					killed = true
				}
			}
			p.mu.Unlock()
			if killed {
				p.seamOnSignal()(sigEventEscalationKill)
			}
		}
	}()
	return nil
}

// Wait 回傳 supervisor 快取的 Exit；任意時點、任意次數可呼叫。
func (p *Proc) Wait() Exit {
	<-p.doneCh
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// Output 跑一個 one-shot 指令並收完 stdout，語意與 `exec.CommandContext(...).Output()`
// 對齊，但**收尾走 process group**（reviewer 2026-08-20）。
//
// 為什麼不直接用 exec.CommandContext：它在 ctx 取消時只殺直接 child，孫程序照樣
// 活著；而孫程序若持有 stdout/stderr 的 write end，父程序的 Output／Wait 會繼續
// 阻塞——取消因此不保證收斂。這裡沿用本套件既有政策：ctx 取消 → group SIGTERM →
// grace 內未退出 → group SIGKILL；子程序退出後再 group SIGKILL 清殘存孫程序，
// reader 的 EOF 才有保證。
//
// 回傳 (stdout, Exit, err)。err 只在啟動失敗、讀取失敗或 ctx 已取消時非 nil；
// 指令自身的非零退出碼由 Exit.Code／Exit.Err 表達（同 exec 的 ExitError 慣例，
// 由呼叫端決定要不要當錯誤）。**ctx 取消時 err 會 wrap ctx.Err()**，呼叫端據此
// 分辨「被收尾取消」與「指令真的失敗」。
func Output(ctx context.Context, cfg Config) ([]byte, Exit, error) {
	// ctx 已取消時 Start 會 fail fast（reviewer 2026-08-20 的 `/usr/bin/touch`
	// 實測；2026-08-21 上移進 Start，所有呼叫端一體適用）。
	p, err := Start(ctx, cfg)
	if err != nil {
		return nil, Exit{}, err
	}
	_ = p.Stdin.Close() // one-shot：不餵輸入，早關避免對方等 EOF
	out, rerr := io.ReadAll(p.Stdout)
	ex := p.Wait()
	// 只有「取消真的觸發了終止」才回 ctx 錯誤。事後查 ctx.Err() 會把「子程序早已
	// exit 0、之後才取消」也錯報成 canceled（reviewer 2026-08-20）。
	if p.CanceledByContext() {
		return out, ex, ctx.Err()
	}
	if rerr != nil {
		return out, ex, rerr
	}
	return out, ex, nil
}
