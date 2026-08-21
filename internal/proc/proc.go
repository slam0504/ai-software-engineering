package proc

import (
	"context"
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
}

// Proc 以獨立 process group 啟動子程序，背景 supervisor 是唯一收尾路徑：
// 子程序一退出即 group SIGKILL（清掉持有 pipe 的孫程序 → reader 的 EOF 保證到來）
// → 收完 stderr → 快取 Exit。Wait() 只回傳快取結果，與汲取「完成」無順序依賴。
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
	p := &Proc{cmd: cmd, pgid: cmd.Process.Pid, grace: grace, Stdin: stdin, Stdout: outR,
		exitedCh: make(chan struct{}), doneCh: make(chan struct{})}

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
		_ = p.SignalGroup(syscall.SIGKILL) // 子程序已退出 → 立即清整組殘存孫程序
		wg.Wait()                          // stderr 讀到 EOF（group kill 保證）
		errR.Close()
		ex := Exit{Code: cmd.ProcessState.ExitCode(), StderrTail: p.stderrTail(), Err: werr}
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

// Done 在 supervisor 收尾完成（Exit 已快取）後關閉；select-default 即為非阻塞存活判定（v1.7）。
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
	go func() {
		select {
		case <-p.exitedCh:
		case <-time.After(p.grace):
			p.mu.Lock()
			if !p.exited {
				if p.SignalGroup(syscall.SIGKILL) == nil {
					p.killSent = true
				}
			}
			p.mu.Unlock()
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
