package claude

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/proc"
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

var _ ports.Turns = (*Session)(nil)       // 編譯期介面契約
var _ ports.Diagnostics = (*Session)(nil) // Argv 診斷能力

// 牆鐘相依處置（backlog B1a-2）：waitResult 的局部 deadline 由 5s 放寬到 15s。
// preflight 240 筆量測（單獨 -count=20 共 60 筆：max 383ms；./internal/... 並行
// 負載下 ×3 輪共 180 筆：max 29ms）**沒有**證明 5s 不足，也沒有觀察到任何逾時。
// 383ms 是本輪唯一觀察到的冷啟動樣本，**CI runner 仍未驗證**——這個缺口留給
// B1a-4 的整合負載矩陣，本票不宣稱已消除。
func TestMultiTurnSendAndTurnBoundaries(t *testing.T) {
	cfg := fakeCfg(t, "FAKE_MULTI=1")
	cfg.MultiTurn = true
	cfg.Prompt = "first"
	s, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// failure-safe 回收：waitResult 的 deadline t.Fatal 會跳過函式尾端的
	// Close／drain／Wait，殘存的 fake CLI 會污染同套件後續測試（mutation 3a 就是
	// 刻意走這條路）。Terminate 在退出已記錄時是 no-op（proc.Terminate 的
	// `if p.exited` 守衛，避免對已回收、可能被重用的 pgid 再送訊號），Wait 回傳
	// supervisor 快取值、可重複呼叫——因此正常路徑既有的 graceful close 不受影響。
	t.Cleanup(func() {
		_ = s.Terminate()
		s.Wait()
	})
	var results int
	events := s.Events()
	waitResult := func() {
		// 15s 只是卡死診斷的保險絲，成功判準是收到 KindResult 事件，不是「跑得夠快」。
		// 沿用 app_test.go 的 waitFor 先例（同一種 fake CLI spawn 壓力下，5s 在
		// -race 全套並行時實測會偶發逾時）。維持局部 deadline、不退回只靠
		// `go test -timeout`：局部失敗能指出「卡在第幾輪的哪個等待」，package
		// timeout 只會丟出整包 goroutine dump。
		deadline := time.After(15 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatal("stream closed before result")
				}
				if ev.Kind == contract.KindResult {
					results++
					return
				}
			case <-deadline:
				t.Fatal("no result within 15s")
			}
		}
	}
	waitResult() // 第 1 輪（Start 的 prompt）
	if err := s.Send("second"); err != nil {
		t.Fatal(err)
	}
	waitResult()
	if err := s.Send("third"); err != nil {
		t.Fatal(err)
	}
	waitResult()
	if results != 3 {
		t.Fatalf("results = %d", results)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range events { // drain 至 EOF
	}
	if ex := s.Wait(); ex.Code != 0 || !ex.Exited {
		t.Fatalf("exit = %+v", ex)
	}
}

func TestSendAfterCloseErrors(t *testing.T) {
	cfg := fakeCfg(t, "FAKE_MULTI=1")
	cfg.MultiTurn = true
	s, _ := Start(context.Background(), cfg)
	_ = s.Close()
	if err := s.Send("x"); err == nil {
		t.Fatal("Send after Close must error")
	}
	drain(s)
	s.Wait()
}

func TestSingleTurnBehaviorUnchanged(t *testing.T) { // M0 回歸
	s, err := Start(context.Background(), fakeCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	ks := drain(s)
	if len(ks) != 3 {
		t.Fatalf("single-turn kinds = %v", ks)
	}
	if err := s.Send("x"); err == nil {
		t.Fatal("single-turn Send must error (stdin closed)")
	}
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

// D5(a)：toPortsExit 三態——一般結束、非零 exit 帶 stderr、有界清理未完成
// （B2c-4 CleanupIncomplete 揭露）。
func TestToPortsExitThreeStates(t *testing.T) {
	cases := []struct {
		name string
		in   proc.Exit
		want ports.Exit
	}{
		{"normal-exit", proc.Exit{Code: 0}, ports.Exit{Exited: true, Code: 0}},
		{"nonzero-with-stderr", proc.Exit{Code: 7, StderrTail: "boom"},
			ports.Exit{Exited: true, Code: 7, StderrTail: "boom"}},
		{"cleanup-incomplete", proc.Exit{Code: 1, CleanupIncomplete: true},
			ports.Exit{Exited: true, Code: 1, CleanupIncomplete: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toPortsExit(c.in); got != c.want {
				t.Fatalf("toPortsExit(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// lineThenErrReader 送出一行資料後，之後每次 Read 都回傳固定錯誤——用來在不
// 啟真實 process 的情況下驗證 pump 的 scanner 錯誤路徑（D5(b)）。
type lineThenErrReader struct {
	data []byte
	err  error
	sent bool
}

func (r *lineThenErrReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

// D5(b) 揭露契約：fake reader 回傳 proc.ErrCleanupIncomplete，證明 supervisor
// 有界清理的 sentinel 錯誤真的會沿 pump 傳到 Claude 既有的 KindStreamError 路徑
// （B2c-4 CleanupIncomplete → B2c-5 supervisor sentinel）。events channel 用
// production 的 `defer close(events); pump(...)` 型態驅動，而非測試手動 close，
// 確保驗到的是實際呼叫端會走的收尾順序。
func TestPumpFakeReaderEmitsStreamErrorThenCallsOnStreamErr(t *testing.T) {
	wantErr := proc.ErrCleanupIncomplete
	r := &lineThenErrReader{data: []byte("hello\n"), err: wantErr}
	events := make(chan contract.Event, 8)
	onStreamErrCalls := 0

	func() {
		defer close(events)
		pump(r, 1024, events, func() { onStreamErrCalls++ })
	}()

	var kinds []contract.Kind
	var streamErrRaw string
	var streamErrErr error
	for ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == contract.KindStreamError {
			streamErrRaw = string(ev.Raw)
			streamErrErr = ev.Err
		}
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != contract.KindStreamError {
		t.Fatalf("KindStreamError 必須是最後一個事件（先發事件再呼叫 onStreamErr）：%v", kinds)
	}
	if !strings.Contains(streamErrRaw, "cleanup incomplete") {
		t.Fatalf("KindStreamError 的 Raw 必須含 proc.ErrCleanupIncomplete 文字：%q", streamErrRaw)
	}
	if !errors.Is(streamErrErr, proc.ErrCleanupIncomplete) {
		t.Fatalf("KindStreamError 的 Err 必須滿足 errors.Is(ErrCleanupIncomplete)：%v", streamErrErr)
	}
	if onStreamErrCalls != 1 {
		t.Fatalf("onStreamErr 必須恰呼叫一次：got %d", onStreamErrCalls)
	}
	// events channel 必須在 stream-error 事件之後、production 的 defer close 生效時關閉。
	if _, ok := <-events; ok {
		t.Fatal("events channel 必須在 KindStreamError 之後關閉")
	}
}

// 正常路徑（無 scanner 錯誤）不得呼叫 onStreamErr——對照案例，避免上面那條
// 測試的斷言只是巧合。
func TestPumpFakeReaderNormalEOFDoesNotCallOnStreamErr(t *testing.T) {
	r := strings.NewReader("hello\nworld\n")
	events := make(chan contract.Event, 8)
	onStreamErrCalls := 0

	pump(r, 1024, events, func() { onStreamErrCalls++ })
	close(events)

	for ev := range events {
		if ev.Kind == contract.KindStreamError {
			t.Fatalf("正常 EOF 不應出現 KindStreamError：%+v", ev)
		}
	}
	if onStreamErrCalls != 0 {
		t.Fatalf("正常 EOF 不得呼叫 onStreamErr：got %d", onStreamErrCalls)
	}
}
