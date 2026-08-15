package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- M3b Task 24：shutdown 總序（§3.6.5）＋並行與 bounded-window barrier ----

// shutdownFixture：多 session 工作區的 shutdown 場景——n 個 Claude（各自子行程、
// broker、lease）＋ n 個 Codex（共用同一條 fake conn）＋ 一個已發布的共用
// app-server generation owner（帶 always-on wire log）。
//
// 兩條 codex conn 是刻意的：owner 掛在 fakeWireControl 造出來的 fake server 上
// （shutdown 的 terminate／wait／finalize wire log 走它），session host 則掛在
// newFakeCodexConn 的 fake wire 上（dispatcher／interrupt 走它）。ensureAppServer
// 必須先跑——replaceCodexGeneration 發布成功時會把 a.codexConn 接到 owner 的 conn
// 上，之後再 wireCodexConn(fake) 才會讓 host 側的那條勝出。
type shutdownFixture struct {
	a      *App
	ctl    *fakeWireControl
	reg    *stubRegistry
	claude []appcore.WSID
	codex  []appcore.WSID
}

func seedSessions(t *testing.T, nClaude, nCodex int) *shutdownFixture {
	t.Helper()
	a, _, ctl := newTestAppWithFakeWire(t)
	reg := &stubRegistry{}
	a.wsReg = reg

	if _, err := a.ensureAppServer(); err != nil { // 共用 app-server generation＋always-on 錄流
		t.Fatalf("ensureAppServer: %v", err)
	}
	f := &shutdownFixture{a: a, ctl: ctl, reg: reg}

	var conn *codex.Conn
	if nCodex > 0 {
		var wire *fakeCodexWire
		conn, wire = newFakeCodexConn(t)
		a.wireCodexConn(conn) // 必須晚於 ensureAppServer（見型別 doc）
		threads := make([]string, nCodex)
		for i := range threads {
			threads[i] = fmt.Sprintf("th-%d", i+1)
		}
		newCodexScript(t, wire, threads...)
		handshakeFake(t, conn)
	}
	if nClaude > 0 {
		writeMultiTurnClaude(t, a)
	}
	for i := 0; i < nClaude; i++ {
		w := mustCreate(t, a, "claude")
		startClaudeOn(t, a, w)
		f.claude = append(f.claude, w)
	}
	for i := 0; i < nCodex; i++ {
		w := mustCreate(t, a, "codex")
		startCodexOn(t, a, conn, w)
		f.codex = append(f.codex, w)
	}
	if got := len(a.snapshotHosts()); got != nClaude+nCodex {
		t.Fatalf("precondition：應有 %d 個 host，實測 %d", nClaude+nCodex, got)
	}
	return f
}

// startClaudeOn／startCodexOn：以 production 的完整 start 交易
// （BeginNewSessionSubmit → provider 啟動 → AcceptSubmit）在**指定 WSID** 上開一個
// session。刻意不用 StartSession：它的 WSID 解析走 legacyWSIDFor（Task 26 前端改為
// 直接帶 WSID 之前的相容層），同一 provider 已有一個 host 時會解析回那個 host，
// 第二個 session 起就會拿到 ErrSessionActive。
func startClaudeOn(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	id, err := a.manager.BeginNewSessionSubmit(w, "task-c")
	if err != nil {
		t.Fatalf("BeginNewSessionSubmit(%s): %v", w, err)
	}
	commit, serr := a.startClaude(w, "hi", "", "")
	if serr != nil {
		t.Fatalf("startClaude(%s): %v", w, serr)
	}
	aerr := a.manager.AcceptSubmit(w, id, "", "hi")
	commit(aerr == nil)
	if aerr != nil {
		t.Fatalf("AcceptSubmit(%s): %v", w, aerr)
	}
	if a.hostFor(w) == nil {
		t.Fatalf("claude host %s 未發布", w)
	}
}

func startCodexOn(t *testing.T, a *App, conn *codex.Conn, w appcore.WSID) {
	t.Helper()
	id, err := a.manager.BeginNewSessionSubmit(w, "task-x")
	if err != nil {
		t.Fatalf("BeginNewSessionSubmit(%s): %v", w, err)
	}
	threadID, _, serr := a.startCodexHost(w, fakeCodexHost{conn}, "hi", "", "", "untrusted")
	if serr != nil {
		t.Fatalf("startCodexHost(%s): %v", w, serr)
	}
	if err := a.manager.AcceptSubmit(w, id, threadID, "hi"); err != nil {
		t.Fatalf("AcceptSubmit(%s): %v", w, err)
	}
	if a.hostFor(w) == nil {
		t.Fatalf("codex host %s 未發布", w)
	}
}

// makeStuck：把 host 的 pump done channel 換成永不關閉的 channel——CloseSequence
// 的「pump 卡死」情境（quiesce 逾時 → terminate → kill 逾時 → 以 Exit{Exited:false}
// 盡力 finalize）。用它而不是「讓子行程忽略 SIGTERM」：後者一旦被 group SIGKILL
// 收掉，pump 就會正常收乾，第二段 bounded window 根本不會發生。
//
// 只在 shutdown 開始前於測試 goroutine 執行，之後才由 teardown goroutine 讀取，
// happens-before 邊由 go statement 提供。
func makeStuck(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	h := a.hostFor(w)
	if h == nil {
		t.Fatalf("no host for %s", w)
	}
	h.pumpDone = make(chan struct{})
}

// fakeAfter：appcore.After 的受控替身——記錄每一次 After 呼叫、回傳可由測試觸發
// 的 channel。shutdown 的 bounded window 因此完全不依賴牆鐘。
type fakeAfter struct {
	mu       sync.Mutex
	cond     *sync.Cond
	out      []chan time.Time
	created  int
	rounds   int
	timedOut bool
}

func newFakeAfter() *fakeAfter {
	f := &fakeAfter{}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeAfter) After(time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := make(chan time.Time, 1)
	f.out = append(f.out, c)
	f.created++
	f.cond.Broadcast()
	return c
}

// waitForOutstanding：等「同時存在」的未觸發 timer 數量剛好是 n。並行性的證據就在
// 這個「同時」：串行實作一次只會有一個 timer 存在，永遠等不到 n。
//
// 逾時只走失敗路徑（AfterFunc 喚醒 cond），正常路徑不睡任何時間。
func (f *fakeAfter) waitForOutstanding(t *testing.T, n int) {
	t.Helper()
	timer := time.AfterFunc(15*time.Second, func() {
		f.mu.Lock()
		f.timedOut = true
		f.mu.Unlock()
		f.cond.Broadcast()
	})
	defer timer.Stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.out) != n && !f.timedOut {
		f.cond.Wait()
	}
	if f.timedOut {
		t.Fatalf("逾時：同時存在的 bounded-window timer 數 = %d，want %d（未並行？）", len(f.out), n)
	}
}

// fireAll：觸發目前全部未觸發的 timer，算一輪。
func (f *fakeAfter) fireAll() {
	f.mu.Lock()
	out := f.out
	f.out = nil
	f.rounds++
	f.mu.Unlock()
	for _, c := range out {
		c <- time.Now()
	}
}

func (f *fakeAfter) rounds_() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rounds
}

func (f *fakeAfter) totalCreated() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

// TestShutdownFollowsFrozenOrder：§3.6.5 的總序是凍結契約——用全序列比對，不是
// 「某兩步的相對順序」：中間少一步、多一步或對調，都必須紅。
//
// 特別是 wirelog_finalize 必須排在 manager_close **之前**：finalize 那一步會先
// terminate 共用 app-server，關閉期間最後那批 frame 導出的事件若在 Manager 關閉
// 之後才產生，只會拿到「manager closed: event dropped」的 UI 通知，永遠進不了
// audit（見 shutdown 的 doc）。
func TestShutdownFollowsFrozenOrder(t *testing.T) {
	f := seedSessions(t, 1, 1)
	seedApproval(t, f.a, f.claude[0]) // deny_approvals 要有真的可 deny 的對象

	var order []string
	f.a.hookShutdownStep = func(s string) { order = append(order, s) }
	f.a.shutdown(context.Background())

	want := []string{
		"reject_new_txn", "stop_watchers", "snapshot", "deny_approvals",
		"interrupt_terminate", "teardown_parallel", "codex_hosts_done",
		"server_terminate_wait", "wirelog_finalize", "manager_close",
		"index_flush_close", "registry_sync",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown 總序不符 §3.6.5：\n got=%v\nwant=%v", order, want)
	}
	// 每一步都要真的做了事（步驟名對得上、工作沒被跳過）
	if len(f.a.snapshotHosts()) != 0 {
		t.Fatal("全部 host 必須收乾")
	}
	if !f.a.manager.Closed() {
		t.Fatal("Manager 必須關閉")
	}
	if f.reg.syncCount() == 0 {
		t.Fatal("registry_sync 必須真的 Sync（§3.6.5 末步）")
	}
	if _, ok := f.a.codexSingle.Current(); ok {
		t.Fatal("共用 app-server 的 ownership 必須被 Take 走")
	}
}

// TestShutdownDeniesEveryPendingApprovalFailClosed：§3.6.5「全部 approval
// fail-closed deny」——跨 session 的每一筆都要被 deny（不是只處理第一筆），
// 且必須在 teardown 之前（deny 是 fail-closed 的前提：teardown 之後 broker
// 已關，等在對話框上的請求會收不到任何裁決）。
func TestShutdownDeniesEveryPendingApprovalFailClosed(t *testing.T) {
	f := seedSessions(t, 2, 0)
	id1 := seedApproval(t, f.a, f.claude[0])
	id2 := seedApproval(t, f.a, f.claude[1])

	var mu sync.Mutex
	var order []string
	f.a.hookShutdownStep = func(s string) {
		if s == "deny_approvals" || s == "teardown_parallel" {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
		}
	}
	f.a.hookTeardownEntered = func(appcore.WSID) {
		mu.Lock()
		order = append(order, "teardown_entered")
		mu.Unlock()
	}
	f.a.shutdown(context.Background())

	if f.a.pendingByID(id1) != nil || f.a.pendingByID(id2) != nil {
		t.Fatal("shutdown 之後不得殘留 pending approval")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) == 0 || order[0] != "deny_approvals" {
		t.Fatalf("deny 必須早於任何 teardown 進場：%v", order)
	}
}

// TestAllTeardownsRunConcurrently：8 個 host 的 teardown 必須同時進場（§3.6.5：
// 「goroutine＋WaitGroup 收斂；不得逐 session 串行」）。用 hookTeardownEntered
// barrier 而不是 CloseSequence timer——Codex 四個 session 共用一條 conn，本就不會
// 產生 per-host 的 timer。
//
// 串行實作會卡在第一個 host 的 barrier 上，永遠等不到第 2 個進場。
func TestAllTeardownsRunConcurrently(t *testing.T) {
	f := seedSessions(t, 4, 4)
	const n = 8
	entered := make(chan appcore.WSID, n)
	release := make(chan struct{})
	f.a.hookTeardownEntered = func(w appcore.WSID) { entered <- w; <-release }

	done := make(chan struct{})
	go func() { f.a.shutdown(context.Background()); close(done) }()

	seen := map[appcore.WSID]bool{}
	for k := 0; k < n; k++ {
		select {
		case w := <-entered:
			seen[w] = true
		case <-time.After(20 * time.Second):
			t.Fatalf("只有 %d 個 teardown 進場——未並行（§3.6.5）", k)
		}
	}
	if len(seen) != n {
		t.Fatalf("應有 8 個相異 host 進場：%d", len(seen))
	}
	close(release)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown 未收斂")
	}
	if got := len(f.a.snapshotHosts()); got != 0 {
		t.Fatalf("8 個 host 都必須收乾，殘留 %d", got)
	}
}

// TestStuckClaudeSessionsShareSingleBoundedWindow：§5.4「8 session 含卡死 Claude →
// shutdown 總時間仍為單一 bounded window」。
//
// 兩件事一起守：
//  1. 4 個卡死的 Claude 的 quiesce timer 必須**同時**存在（並行），kill timer 同理
//     ——共兩輪，不是 4×2 輪的串行。
//  2. Codex 的 4 個 session 共用一個 app-server，**不得**產生 per-host 的
//     CloseSequence timer——所以 timer 總數恰好是 4×2 = 8，不是 8×2 = 16。
func TestStuckClaudeSessionsShareSingleBoundedWindow(t *testing.T) {
	f := seedSessions(t, 4, 4)
	for _, w := range f.claude {
		makeStuck(t, f.a, w)
	}
	// 擋住自然收尾 reaper：interrupt 那一步的 sess.Terminate() 會讓子行程死掉、喚醒
	// 每個 host 自己的 reaper，它們共用同一份 teardown OnceValue。不擋的話 4 個
	// CloseSequence 是被 4 條 reaper goroutine 起出來的，timer 照樣「同時存在 4 個」
	// ——測到的是 reaper 的並行度，不是 §3.6.5 要求的 forcedShutdown 並行 teardown。
	releaseReaper := make(chan struct{})
	f.a.hookClaudeReaperBeforeEndFlow = func() { <-releaseReaper }
	defer close(releaseReaper)

	timers := newFakeAfter()
	f.a.afterFn = timers.After

	done := make(chan struct{})
	go func() { f.a.shutdown(context.Background()); close(done) }()

	timers.waitForOutstanding(t, 4) // 4 個 quiesce timer 同時存在＝並行且僅 Claude
	timers.fireAll()
	timers.waitForOutstanding(t, 4) // 4 個 kill timer 同時存在
	timers.fireAll()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		// timer 統計一併帶出：多出來的 per-host timer（例如有人替 Codex 也接上
		// CloseSequence）會讓收尾停在沒被觸發的那幾個 timer 上，這裡直接指出來
		t.Fatalf("shutdown 未收斂（已建立 %d 個 timer、觸發 %d 輪，仍有未觸發者）",
			timers.totalCreated(), timers.rounds_())
	}
	if got := timers.rounds_(); got != 2 {
		t.Fatalf("應為兩段 bounded window（quiesce＋kill），實測 %d 輪＝串行", got)
	}
	if got := timers.totalCreated(); got != 8 {
		t.Fatalf("只有 4 個卡死 Claude 各兩段 timer＝8；Codex 不應有 per-host timer：%d", got)
	}
	if got := len(f.a.snapshotHosts()); got != 0 {
		t.Fatalf("卡死者也必須收斂，殘留 %d 個 host", got)
	}
}

// TestCodexSharedServerTerminatedAfterAllHostsDrained：共用 app-server 的
// terminate／wait 必須排在**全部** Codex session host 收乾之後（§3.6.5），而 wire
// log 的 finalize 又必須排在 terminate／wait 之後（§3.4.2：錄到 server 生命終點）。
//
// 兩個步驟名夾住的是同一個 GenerationOwner.FinalizeWith 呼叫，所以另外用實際狀態
// 佐證，避免退化成「相鄰兩個標記的順序」這種恆真斷言：進入時 generation 尚未
// finalize、fake server 尚未收到 terminate；離開時兩者都已成立。
func TestCodexSharedServerTerminatedAfterAllHostsDrained(t *testing.T) {
	f := seedSessions(t, 0, 4)
	owner, ok := f.a.codexSingle.Current()
	if !ok {
		t.Fatal("precondition：共用 app-server owner 必須已發布")
	}
	f.ctl.reset() // 只看 shutdown 期間的 fake server 步驟

	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	hosts := make(chan appcore.WSID, 4)
	f.a.hookTeardownDone = func(w appcore.WSID) {
		hosts <- w
		record("host_done:" + string(w))
	}
	var enterFinalized, enterSteps, exitFinalized, exitSteps = false, []string(nil), false, []string(nil)
	f.a.hookShutdownStep = func(s string) {
		switch s {
		case "server_terminate_wait":
			enterFinalized, enterSteps = owner.Generation.Finalized(), f.ctl.order()
			record(s)
		case "wirelog_finalize":
			exitFinalized, exitSteps = owner.Generation.Finalized(), f.ctl.order()
			record(s)
		}
	}
	f.a.shutdown(context.Background())

	close(hosts)
	seen := map[appcore.WSID]bool{}
	for w := range hosts {
		seen[w] = true
	}
	if len(seen) != 4 {
		t.Fatalf("4 個 Codex host 都必須收乾：%d", len(seen))
	}

	mu.Lock()
	defer mu.Unlock()
	termIdx := indexOfStep(order, "server_terminate_wait")
	if termIdx < 4 {
		t.Fatalf("共用 app-server 必須在 4 個 host 全部收乾後才 terminate：%v", order)
	}
	for k := 0; k < termIdx; k++ {
		if !strings.HasPrefix(order[k], "host_done:") {
			t.Fatalf("terminate 之前只該有 host_done：%v", order)
		}
	}
	if wireIdx := indexOfStep(order, "wirelog_finalize"); wireIdx < termIdx {
		t.Fatalf("wire log 必須在 terminate／wait 之後 finalize（§3.4.2）：%v", order)
	}
	// 實際狀態佐證（§3.4.2 的內部順序由 internal/codex 保證，這裡守 App 層看得到的兩個邊界）
	if enterFinalized {
		t.Fatal("進入 server_terminate_wait 時 wire log 不得已 finalize")
	}
	if len(enterSteps) != 0 {
		t.Fatalf("進入 server_terminate_wait 時共用 server 不得已被收尾：%v", enterSteps)
	}
	if !exitFinalized {
		t.Fatal("wirelog_finalize 之後 wire log 必須已 finalize")
	}
	if !reflect.DeepEqual(exitSteps, []string{"terminate", "wait"}) {
		t.Fatalf("finalize 之前必須先 terminate → wait（§3.4.2）：%v", exitSteps)
	}
	if m := owner.Generation.FinalMeta(); m.ExitCode == nil {
		t.Fatalf("收尾證據（exit code）必須落進 wire log meta：%+v", m)
	}
}

func indexOfStep(order []string, want string) int {
	for i, s := range order {
		if s == want {
			return i
		}
	}
	return -1
}

// TestIndexAcceptsEventsUntilManagerClose：§3.6.5「Manager.Close 可能 flush pending
// conversation events，故 replay index 不得在 Manager.Close 之前停止接收」。
//
// 場景走 production 形狀：slot 停在 submitting（BeginSubmit 之後、Accept 之前），
// 此後的事件一律排進 pendingBuf，直到 Manager.Close 才 flush 落 audit——那一刻
// index 必須還接得到。斷言看**磁碟上的** checkpoint（不是記憶體值）：index.Flush
// 若被移到 Manager.Close 之前，磁碟 checkpoint 就會停在 flush 那批事件之前。
func TestIndexAcceptsEventsUntilManagerClose(t *testing.T) {
	f := seedSessions(t, 1, 0)
	a, w := f.a, f.claude[0]

	if _, err := a.manager.BeginSubmit(w); err != nil {
		t.Fatalf("BeginSubmit: %v", err)
	}
	// delta 刻意不是 turn boundary：boundary 事件會在 Observe 內就把 checkpoint
	// 落盤，那樣即使 Flush 被提前也測不出來。
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindDelta, Role: "assistant", Text: "queued-until-close"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	a.shutdown(context.Background())

	auditSize := fileSize(t, a.eventsPath())
	var cp struct {
		Offset      int64  `json:"offset"`
		LastEventID string `json:"last_event_id"`
	}
	b, err := os.ReadFile(filepath.Join(a.stateDir, "replay-index", "checkpoint.json"))
	if err != nil {
		t.Fatalf("讀 checkpoint：%v", err)
	}
	if err := json.Unmarshal(b, &cp); err != nil {
		t.Fatalf("解析 checkpoint：%v", err)
	}
	if cp.Offset != auditSize {
		t.Fatalf("replay index 不得在 Manager.Close 之前停止接收（§3.6.5）："+
			"checkpoint offset=%d，audit 檔尾=%d", cp.Offset, auditSize)
	}
	// 佐證 Close 真的 flush 了那筆排隊事件（否則上面的相等是「兩邊都沒動」的恆真）
	if !strings.Contains(string(readFileText(t, a.eventsPath())), "queued-until-close") {
		t.Fatal("排隊事件必須由 Manager.Close flush 進 audit")
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func readFileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
