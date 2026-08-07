package claude

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func fakeCfg(t *testing.T, env ...string) Config {
	p, _ := filepath.Abs("../../testdata/fake-claude.sh")
	return Config{Binary: p, CWD: t.TempDir(), Prompt: "x", Env: env, TermGrace: 200 * time.Millisecond}
}

func drain(s *Session) []contract.Kind {
	var ks []contract.Kind
	for ev := range s.Events() {
		ks = append(ks, ev.Kind)
	}
	return ks
}

func TestHappyPathAndArgv(t *testing.T) {
	s, err := Start(context.Background(), fakeCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	ks := drain(s)
	if len(ks) != 3 || ks[0] != contract.KindInit || ks[2] != contract.KindResult {
		t.Fatalf("kinds = %v", ks)
	}
	argv := strings.Join(s.Argv(), " ")
	for _, must := range []string{"--output-format stream-json", "--verbose", "--include-partial-messages"} {
		if !strings.Contains(argv, must) {
			t.Fatalf("argv missing %q: %s", must, argv)
		}
	}
	if ex := s.Wait(); ex.Code != 0 {
		t.Fatalf("exit = %d", ex.Code)
	}
}

func TestStartBinaryNotFound(t *testing.T) {
	if _, err := Start(context.Background(), Config{Binary: "/nonexistent/claude", CWD: t.TempDir(), Prompt: "x"}); err == nil {
		t.Fatal("must error on missing binary")
	}
}

func TestProcessDiesMidStream(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_DIE=1"))
	ks := drain(s)
	for _, k := range ks {
		if k == contract.KindResult {
			t.Fatal("must not reach result")
		}
	}
	if ex := s.Wait(); ex.Code != 7 {
		t.Fatalf("exit = %d, want 7", ex.Code)
	}
}

func TestStderrCaptured(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_STDERR=1"))
	drain(s)
	if ex := s.Wait(); !strings.Contains(ex.StderrTail, "boom-stderr") {
		t.Fatalf("stderr tail = %q", ex.StderrTail)
	}
}

func TestExitCodePropagates(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_EXIT=3"))
	drain(s)
	if ex := s.Wait(); ex.Code != 3 {
		t.Fatalf("exit = %d, want 3", ex.Code)
	}
}

func groupDead(pgid int) bool { return syscall.Kill(-pgid, 0) != nil } // ESRCH = 整組已消失

func TestTerminateKillsProcessGroup(t *testing.T) { // 孫程序忽略 SIGTERM 也必須被整組收掉
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_HANG=1", "FAKE_ORPHAN=1"))
	<-s.Events()
	start := time.Now()
	_ = s.Terminate()
	drain(s) // 孫程序持有 pipe 也不得讓 drain 卡住（supervisor 的 group SIGKILL 保證 EOF）
	ex := s.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated session must not exit 0")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill escalation too slow")
	}
	if !groupDead(s.PGID()) {
		t.Fatal("process group must be fully dead (orphan survived)")
	}
}

func TestOrphanDoesNotHangNormalExit(t *testing.T) { // 第五輪 P0 情境：正常結束 + 孫程序持有 stdout
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_ORPHAN=1"))
	doneCh := make(chan struct{})
	go func() { drain(s); s.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("drain/Wait hung on orphan-held pipes")
	}
	if !groupDead(s.PGID()) {
		t.Fatal("orphan must be reaped by supervisor on parent exit")
	}
}

func TestScannerErrorSurfaced(t *testing.T) { // v1.4：超長行 → KindStreamError，不是無聲截斷
	cfg := fakeCfg(t, "FAKE_BADLINE=1")
	cfg.MaxLineBytes = 1024
	s, _ := Start(context.Background(), cfg)
	var sawStreamErr bool
	for ev := range s.Events() {
		if ev.Kind == contract.KindStreamError {
			sawStreamErr = true
		}
	}
	s.Wait()
	if !sawStreamErr {
		t.Fatal("oversized line must surface KindStreamError")
	}
}
