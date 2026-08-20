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
		close(p.exitedCh)
		_ = p.SignalGroup(syscall.SIGKILL) // 子程序已退出 → 立即清整組殘存孫程序
		wg.Wait()                          // stderr 讀到 EOF（group kill 保證）
		errR.Close()
		p.exit = Exit{Code: cmd.ProcessState.ExitCode(), StderrTail: p.stderrTail(), Err: werr}
		close(p.doneCh)
	}()
	go func() { // 覆寫 ctx 取消語意：走 Terminate（整組），不是單程序 kill
		select {
		case <-ctx.Done():
			_ = p.Terminate()
		case <-p.exitedCh:
		}
	}()
	return p, nil
}

func (p *Proc) SignalGroup(sig syscall.Signal) error { return syscall.Kill(-p.pgid, sig) }

func (p *Proc) PGID() int { return p.pgid }

// StderrSnapshot 回傳目前的 stderr tail（v1.6：長駐程序仍在跑時取證用，不等待退出）。
func (p *Proc) StderrSnapshot() string { return p.stderrTail() }

// Done 在 supervisor 收尾完成（Exit 已快取）後關閉；select-default 即為非阻塞存活判定（v1.7）。
func (p *Proc) Done() <-chan struct{} { return p.doneCh }

func (p *Proc) Terminate() error { // group SIGTERM → grace 內未退出 → group SIGKILL
	err := p.SignalGroup(syscall.SIGTERM)
	go func() {
		select {
		case <-p.exitedCh:
		case <-time.After(p.grace):
			_ = p.SignalGroup(syscall.SIGKILL)
		}
	}()
	return err
}

// Wait 回傳 supervisor 快取的 Exit；任意時點、任意次數可呼叫。
func (p *Proc) Wait() Exit {
	<-p.doneCh
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
	p, err := Start(ctx, cfg)
	if err != nil {
		return nil, Exit{}, err
	}
	_ = p.Stdin.Close() // one-shot：不餵輸入，早關避免對方等 EOF
	out, rerr := io.ReadAll(p.Stdout)
	ex := p.Wait()
	if cerr := ctx.Err(); cerr != nil {
		return out, ex, cerr
	}
	if rerr != nil {
		return out, ex, rerr
	}
	return out, ex, nil
}
