package codex

import (
	"context"
	"errors"
	"os/exec"
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

// TestAppServerTerminateKillsGroup 驗「leader 收到 TERM 後退出、supervisor
// 在 cmd.Wait 返回後清除仍存活的 process group」——不是 escalation。
//
// #1 preflight 事實修正（backlog B1 rev8）：這條測試從未真的驗過 Terminate()
// 的 grace-timeout 升級分支。FAKE_ORPHAN 只讓孫程序 trap TERM，leader 本身不
// trap——group TERM 一到 leader 就死，Terminate() 內的 escalation select 永遠
// 走 <-p.exitedCh。原本的 kill escalation too slow 斷言因此從未驗到它宣稱要驗
// 的東西；已移除，不再宣稱驗到 escalation。deterministic escalation 契約改由
// internal/proc 的白箱測試承擔。
//
// 本測試如何把孫程序的死亡確定性歸因給 supervisor 收尾管線：把 TermGrace 拉到
// 遠大於整條測試生命週期（1 小時），escalation 分支的 grace timer 在測試結束前
// 不可能到期，因此對 group 送出 KILL 的只可能是 supervisor 在 cmd.Wait 返回後
// 的清孫程序路徑。這個歸因不需要存取 internal/proc 的未匯出 seam。
// 另外斷言 leader 死因為 SIGTERM，把「leader 自身也不是被 KILL 收掉的」一併釘死。
func TestAppServerTerminateKillsGroup(t *testing.T) {
	cfg := fakeSrvCfg(t, "FAKE_ORPHAN=1")
	cfg.TermGrace = time.Hour // 遠大於測試生命週期：escalation 分支在本測試中不可能到期
	srv, err := StartAppServer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Terminate(); srv.Wait() }) // 失敗路徑也要收乾淨；
	// Terminate() 內建「退出已記錄就不送訊號」的守衛，故對已死的 pgid 呼叫是安全的。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	// 真實時間 timeout 只作卡死保險（不作效能驗收，backlog #1 裁定移除 5s 效能
	// 斷言）：go test 的全域 -timeout 已是最終防線。
	ex := srv.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated server must not exit 0")
	}

	// leader 死因必須是 TERM 本身。
	var ee *exec.ExitError
	if !errors.As(ex.Err, &ee) {
		t.Fatalf("leader 必須死於訊號（*exec.ExitError），實得 %v", ex.Err)
	}
	ws, isWS := ee.Sys().(syscall.WaitStatus)
	if !isWS || !ws.Signaled() {
		t.Fatalf("leader 必須死於訊號終止，實得 isWS=%v", isWS)
	}
	if ws.Signal() != syscall.SIGTERM {
		t.Fatalf("leader 死因必須是 SIGTERM（未進入 escalation 分支），實得 %v", ws.Signal())
	}

	// 孫程序（trap TERM、sleep 30）也必須消失——在 TermGrace=1h 的前提下，只可能
	// 是 supervisor 收尾管線清掉的。
	if !srvGroupDead(srv.PGID()) {
		t.Fatal("process group must be fully dead")
	}
}
