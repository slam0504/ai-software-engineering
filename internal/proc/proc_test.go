package proc

import (
	"bytes"
	"context"
	"io"
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
