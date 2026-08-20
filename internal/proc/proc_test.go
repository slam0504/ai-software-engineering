package proc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func bashProc(t *testing.T, ctx context.Context, script string, grace time.Duration) *Proc {
	t.Helper()
	p, err := Start(ctx, Config{Binary: "/bin/bash", Args: []string{"-c", script}, Dir: t.TempDir(), TermGrace: grace})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// drainStdout 依 v1.6 汲取契約啟動並行 reader；回傳讀到的內容（reader 結束後才可讀取 buffer）。
func drainStdout(p *Proc) (*bytes.Buffer, *sync.WaitGroup) {
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(&buf, p.Stdout) }()
	return &buf, &wg
}

func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) != nil } // ESRCH = 整組已消失

// 孫程序忽略 SIGTERM 且繼承 stdout/stderr pipe——第五輪 P0 的核心情境。
const orphanScript = `bash -c 'trap "" TERM; sleep 30' & echo out; echo err >&2; exit 5`

func TestNormalExitReapsOrphanAndCachesExit(t *testing.T) {
	p := bashProc(t, context.Background(), orphanScript, time.Second)
	out, rd := drainStdout(p) // v1.6 契約：reader 並行汲取；Wait 不等汲取完成即可呼叫
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code != 5 {
			t.Fatalf("code = %d, want 5", ex.Code)
		}
		if !strings.Contains(ex.StderrTail, "err") {
			t.Fatalf("stderr tail = %q", ex.StderrTail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait hung: supervisor must reap group while reader is still draining")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("orphan must be killed when parent exits")
	}
	select { // v1.7：Wait 返回後 Done 必已關閉（非阻塞存活判定的依據）
	case <-p.Done():
	default:
		t.Fatal("Done must be closed after Wait returns")
	}
	rd.Wait() // group kill 後 EOF 必然到來
	if !strings.Contains(out.String(), "out") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestLargeOutputDoesNotDeadlock(t *testing.T) { // v1.6：> pipe buffer 的輸出在並行汲取下不死鎖
	p := bashProc(t, context.Background(), `head -c 2000000 /dev/zero | tr '\0' a; exit 0`, time.Second)
	out, rd := drainStdout(p)
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code != 0 {
			t.Fatalf("code = %d", ex.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("large output deadlocked")
	}
	rd.Wait()
	if out.Len() < 2000000 {
		t.Fatalf("stdout truncated: %d bytes", out.Len())
	}
}

func TestTerminateEscalatesToGroupKill(t *testing.T) {
	p := bashProc(t, context.Background(), `trap "" TERM; echo up; sleep 30`, 200*time.Millisecond)
	buf := make([]byte, 8)
	_, _ = p.Stdout.Read(buf) // 等 trap 生效
	_, rd := drainStdout(p)
	start := time.Now()
	_ = p.Terminate()
	ex := p.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated proc must not exit 0")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill escalation too slow")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("group must be fully dead")
	}
	rd.Wait()
}

func TestCtxCancelKillsWholeGroup(t *testing.T) { // v1.6：獨立 script、不會自行退出——真正測 ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	p := bashProc(t, ctx, `bash -c 'trap "" TERM; sleep 30' & echo ready; sleep 30`, 200*time.Millisecond)
	buf := make([]byte, 8)
	_, _ = p.Stdout.Read(buf) // 等 ready：orphan 已衍生、parent 未退出
	_, rd := drainStdout(p)
	cancel()
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code == 0 {
			t.Fatal("cancelled proc must not exit 0")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel must terminate whole group")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("group must be dead after ctx cancel")
	}
	rd.Wait()
}

// TestOutputCancellationKillsGrandchildren
//
// reviewer 2026-08-20：`exec.CommandContext` 在 ctx 取消時只殺**直接 child**，
// 孫行程照樣活著；孫行程若持有 stdout 的 write end，父行程的 Output／Wait 還會
// 繼續阻塞——取消因此不保證收斂。
//
// 正題斷言（兩條，分得開）：
//   - ctx 取消之後 Output **會返回**（不被孫行程持有的 pipe 卡住）。
//   - 那個忽略 TERM 的孫行程**已經不在**（group KILL 收掉了）。
func TestOutputCancellationKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// child：spawn 一個忽略 TERM/HUP 的孫行程（繼承 stdout），自己也不退出。
	script := "#!/bin/sh\n" +
		"( trap '' TERM HUP; echo $$ > " + pidFile + "; while true; do sleep 0.05; done ) &\n" +
		"trap '' TERM HUP\n" +
		"while true; do sleep 0.05; done\n"
	sh := filepath.Join(dir, "spawn.sh")
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := Output(ctx, Config{Binary: sh, TermGrace: 300 * time.Millisecond})
		done <- err
	}()
	// 等孫行程真的起來（pid 檔案出現）。
	deadline := time.Now().Add(20 * time.Second)
	var pid int
	for time.Now().Before(deadline) && pid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("測試前提不成立：孫行程沒有起來")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("取消之後 Output 沒有返回——孫行程持有的 pipe 把它卡住了")
	}
	// 孫行程必須已經被 group KILL 收掉（signal 0 ＝ 存活探測）。
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // 已不存在
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL) // 清乾淨再報告
	t.Fatal("忽略 TERM 的孫行程仍存活——取消必須走 process group（TERM → bounded KILL）")
}

// TestOutputDoesNotStartWhenContextAlreadyCanceled
//
// reviewer 2026-08-20：ctx 在呼叫前就已取消，先前照樣 Start——實測
// `/usr/bin/touch` 真的把檔案建出來了。取消的語意是「不要做」，不是「做了再殺」。
func TestOutputDoesNotStartWhenContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 用**不存在的 binary** 當探針：有 fail fast 時根本不會走到 Start，錯誤必然
	// 是 context.Canceled；沒有 fail fast 就會先撞到 exec 的「檔案不存在」。這個
	// 判準不依賴訊號送達的時機，因此是確定性的。
	if _, _, err := Output(ctx, Config{Binary: "/nonexistent/definitely-not-here"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 已取消時必須在 Start 之前就返回 context.Canceled，實得 %v", err)
	}
	// 附帶：真的可執行的指令也不得產生副作用。
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	if _, _, err := Output(ctx, Config{Binary: "/usr/bin/touch", Args: []string{marker}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("實得 %v", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("ctx 已取消時不得啟動子程序（檔案被建出來了）")
	}
}

// TestOutputDoesNotReportCancellationAfterCleanExit
//
// 子程序早已正常退出、stdout 也收完，**之後**才取消 context——那不是取消造成的
// 結果，不得錯報成 context canceled（reviewer 2026-08-20：先前在 Wait 之後查
// ctx.Err() 當下狀態，於是這種時序會回錯）。
func TestOutputDoesNotReportCancellationAfterCleanExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out, ex, err := Output(ctx, Config{Binary: "/bin/echo", Args: []string{"hi"}})
	cancel() // 指令早已收工，這一刻才取消
	if err != nil {
		t.Fatalf("正常收工的指令不得回錯誤，實得 %v", err)
	}
	if ex.Code != 0 || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("輸出／退出碼不符：out=%q code=%d", out, ex.Code)
	}
}

// TestCanceledByContextIsFalseAfterCleanExit
//
// 判定必須是「取消**真的觸發了終止**」，不是「事後 ctx 當下是不是 done」
// （reviewer 2026-08-20）。子程序先正常退出、之後才取消——這種時序不得被記成
// canceled。
//
// 驗在 Proc 這一層而不是 Output：Output 回來之後才 cancel 的話，事後查 ctx.Err()
// 的錯誤實作也會通過，量不到差別。這裡在**子程序已退出、Proc 仍在手上**的時點
// 取消，兩種實作的結果就分得開了。
func TestCanceledByContextIsFalseAfterCleanExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, Config{Binary: "/bin/echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin.Close()
	if _, rerr := io.ReadAll(p.Stdout); rerr != nil {
		t.Fatal(rerr)
	}
	if ex := p.Wait(); ex.Code != 0 {
		t.Fatalf("前提：指令必須正常收工，實得 code=%d", ex.Code)
	}

	cancel() // 子程序早已退出，這一刻才取消
	// watcher goroutine 若會誤設旗標，這裡給它機會跑到。
	for range 100 {
		if p.CanceledByContext() {
			t.Fatal("子程序已正常退出之後才取消，不得被記成 canceled")
		}
		time.Sleep(time.Millisecond)
	}
}
