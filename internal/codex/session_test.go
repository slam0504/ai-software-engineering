package codex

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
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

// ---- B2c-6：process-group oracle 有界輪詢（B2c-4 裁定記錄 D4：只有 ESRCH
// 算群組消失；EPERM／其他 errno 記錄但繼續輪詢，不偽裝成 gone） ----

// pollGroupGone 每輪先 probe：ESRCH 立即回報 gone；nil／EPERM／其他 errno 記錄
// 進 seq 並計數 EPERM，再判斷 deadline；還有時間才 sleep(min(backoff, remaining))，
// backoff 序列 1/2/4/8/16/32/64 ms 之後固定 64 ms。
//
//nolint:staticcheck // ST1008: 回傳順序（gone, epermCount, last, seq）是 B2c-6 design gate 核准的固定形狀，四套件須逐字一致
func pollGroupGone(probe func() error, deadline time.Time, now func() time.Time, sleep func(time.Duration)) (gone bool, epermCount int, last error, seq []string) {
	const maxBackoff = 64 * time.Millisecond
	backoff := time.Millisecond
	for {
		err := probe()
		last = err
		seq = append(seq, errnoToken(err))
		if errors.Is(err, syscall.ESRCH) {
			return true, epermCount, last, seq
		}
		if errors.Is(err, syscall.EPERM) {
			epermCount++
		}
		if !now().Before(deadline) {
			return false, epermCount, last, seq
		}
		d := backoff
		if remaining := deadline.Sub(now()); d > remaining {
			d = remaining
		}
		sleep(d)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// errnoToken 把 probe 的錯誤正規化成人類可讀 token，供 requireGroupGone 的失敗
// 訊息與 table test 斷言使用。
func errnoToken(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, syscall.ESRCH):
		return "ESRCH"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.EINVAL):
		return "EINVAL"
	default:
		return err.Error()
	}
}

// groupOracleSnapshot 直接執行 ps（不經 shell），依第二欄 PGID 在 Go 內篩選——
// `ps -g` 在 Linux procps 對純數字選的是 session、非 process group，不可用。
func groupOracleSnapshot(pgid int) string {
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=,stat=,command=").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<ps failed: %v>\n%s", err, out)
	}
	want := strconv.Itoa(pgid)
	var rows []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != want {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("<no rows for pgid %d>", pgid)
	}
	return strings.Join(rows, "\n")
}

// requireGroupGone 是 production 呼叫點用的外層：2 s deadline、真實時鐘，失敗
// 附 errno 序列與 ps 快照（B2c-6 D2）。
func requireGroupGone(t *testing.T, pgid int, context string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	probe := func() error { return syscall.Kill(-pgid, 0) }
	gone, epermCount, last, seq := pollGroupGone(probe, deadline, time.Now, time.Sleep)
	if !gone {
		t.Fatalf("%s: process group %d not gone within 2s: errno sequence=%v (EPERM=%d, last=%v); ps rows for pgid:\n%s",
			context, pgid, seq, epermCount, last, groupOracleSnapshot(pgid))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPollGroupGoneTableCases 五案（design gate rev2 D1）：fake clock 只由 fake
// sleep 推進，不真的睡、不忙迴圈。
func TestPollGroupGoneTableCases(t *testing.T) {
	t.Run("eperm_eperm_esrch_gone", func(t *testing.T) {
		errs := []error{syscall.EPERM, syscall.EPERM, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if epermCount != 2 {
			t.Fatalf("epermCount = %d, want 2", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"EPERM", "EPERM", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
		if want := []time.Duration{time.Millisecond, 2 * time.Millisecond}; !equalDurations(sleeps, want) {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	})

	t.Run("nil_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return nil
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if last != nil {
			t.Fatalf("last = %v, want nil", last)
		}
		wantSleeps := []time.Duration{}
		for _, ms := range []int{1, 2, 4, 8, 16, 32} {
			wantSleeps = append(wantSleeps, time.Duration(ms)*time.Millisecond)
		}
		for range 30 {
			wantSleeps = append(wantSleeps, 64*time.Millisecond)
		}
		wantSleeps = append(wantSleeps, 17*time.Millisecond) // 2000 - (63 + 64*30) = 17 的截短值
		if !equalDurations(sleeps, wantSleeps) {
			t.Fatalf("sleeps = %v (len=%d), want %v (len=%d)", sleeps, len(sleeps), wantSleeps, len(wantSleeps))
		}
		if probeCount != len(tokens) {
			t.Fatalf("probeCount = %d, len(seq) = %d, want equal", probeCount, len(tokens))
		}
		if len(tokens) != 38 {
			t.Fatalf("len(seq) = %d, want 38 (37 sleeps + 1 final probe after deadline)", len(tokens))
		}
		for _, tok := range tokens {
			if tok != "nil" {
				t.Fatalf("seq contains non-nil token %q: %v", tok, tokens)
			}
		}
	})

	t.Run("eperm_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return syscall.EPERM
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if !errors.Is(last, syscall.EPERM) {
			t.Fatalf("last = %v, want EPERM", last)
		}
		if epermCount != probeCount {
			t.Fatalf("epermCount = %d, probeCount = %d, want equal", epermCount, probeCount)
		}
		if len(tokens) != probeCount {
			t.Fatalf("len(seq) = %d, probeCount = %d, want equal", len(tokens), probeCount)
		}
	})

	t.Run("immediate_esrch_zero_sleeps", func(t *testing.T) {
		probe := func() error { return syscall.ESRCH }
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepCalls := 0
		sleepFn := func(time.Duration) { sleepCalls++ }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if sleepCalls != 0 {
			t.Fatalf("sleepCalls = %d, want 0", sleepCalls)
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})

	t.Run("mixed_errno_sequence_normalizes_tokens", func(t *testing.T) {
		errs := []error{nil, syscall.EPERM, syscall.EINVAL, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, _, _, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if want := []string{"nil", "EPERM", "EINVAL", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})
}

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
	requireGroupGone(t, srv.PGID(), "process group must be fully dead")
}
