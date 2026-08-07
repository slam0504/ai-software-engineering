package codex

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeSrvCfg(t *testing.T, env ...string) Config {
	p, _ := filepath.Abs("../../testdata/fake-codex-appserver.sh")
	return Config{Binary: p, CWD: t.TempDir(), Env: env, TermGrace: 200 * time.Millisecond}
}

func TestStartAppServerBinaryNotFound(t *testing.T) {
	if _, err := StartAppServer(context.Background(), Config{Binary: "/nonexistent/codex", CWD: t.TempDir()}); err == nil {
		t.Fatal("must error on missing binary")
	}
}

func TestAppServerMidStreamDeath(t *testing.T) { // FAKE_DIE：handshake 後退出 7
	srv, err := StartAppServer(context.Background(), fakeSrvCfg(t, "FAKE_DIE=1"))
	if err != nil {
		t.Fatal(err)
	}
	select { // 剛啟動：存活（select-default 不觸發）
	case <-srv.Done():
		t.Fatal("server must be alive right after start")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if ex := srv.Wait(); ex.Code != 7 { // 死亡：Wait 取得退出碼
		t.Fatalf("exit = %d, want 7", ex.Code)
	}
	select { // v1.7：死亡後 Done 已關閉（非阻塞判定依據）
	case <-srv.Done():
	default:
		t.Fatal("Done must be closed after death")
	}
	if _, err := srv.Conn().Call(ctx, MethodThreadStart, map[string]any{}); err == nil {
		t.Fatal("Call after death must error")
	}
}

func TestAppServerStderrCaptured(t *testing.T) {
	srv, err := StartAppServer(context.Background(), fakeSrvCfg(t, "FAKE_STDERR=1", "FAKE_DIE=1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatal(err)
	}
	if ex := srv.Wait(); !strings.Contains(ex.StderrTail, "codex-stderr") {
		t.Fatalf("stderr tail = %q", ex.StderrTail)
	}
}

func TestAppServerStderrSnapshotWhileRunning(t *testing.T) { // v1.6：長駐 server 的 live 證據
	srv, err := StartAppServer(context.Background(), fakeSrvCfg(t, "FAKE_STDERR=1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Terminate(); srv.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(srv.StderrSnapshot(), "codex-stderr") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("StderrSnapshot must expose live stderr while server is running")
}

func srvGroupDead(pgid int) bool { return syscall.Kill(-pgid, 0) != nil }

func TestAppServerTerminateKillsGroup(t *testing.T) { // 忽略 SIGTERM 的孫程序也要整組收掉
	srv, err := StartAppServer(context.Background(), fakeSrvCfg(t, "FAKE_ORPHAN=1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = srv.Terminate()
	ex := srv.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated server must not exit 0")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill escalation too slow")
	}
	if !srvGroupDead(srv.PGID()) {
		t.Fatal("process group must be fully dead")
	}
}
