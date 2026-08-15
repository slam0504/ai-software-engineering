package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/replayindex"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// ---- 測試專用讀取入口（同 replayindex 的慣例：欄位在 production 檔宣告，
// 存取器只存在於 _test.go）----

// rebuildStartsForTest：RuntimeRebuild 實際被呼叫的次數。
func (a *App) rebuildStartsForTest() int {
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	return a.rebuildStarts
}

// backoffDelaysIncreasingForTest：每次未收斂之後的等待是否嚴格遞增（不得
// busy-loop，§3.5.7）。少於兩次重試視為不成立——沒有遞增可言。
func (a *App) backoffDelaysIncreasingForTest() bool {
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	if len(a.rebuildDelays) < 2 {
		return false
	}
	for i := 1; i < len(a.rebuildDelays); i++ {
		if a.rebuildDelays[i] <= a.rebuildDelays[i-1] {
			return false
		}
	}
	return true
}

// ---- 測試工具 ----

// dormantWSID：在 Manager 上掛一個指定 WSID 的 committed slot（等同啟動時從
// registry 還原）。Emit 需要 committed slot，且 WSID 要可預測。
func dormantWSID(t *testing.T, a *App, wsid string, p contract.Provider) appcore.WSID {
	t.Helper()
	if err := a.manager.RestoreDormant(appcore.WSID(wsid), p); err != nil {
		t.Fatal(err)
	}
	return appcore.WSID(wsid)
}

// enableAuditFile：newTestApp 預設不開 audit.jsonl（a.audit 因此是 no-op）。
// 要斷言診斷軌跡的測試自己開。
func enableAuditFile(t *testing.T, a *App) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	a.auditF = f
	t.Cleanup(func() { _ = f.Close() })
}

// emitCompleteTurn：一個完整 turn——canonical user message 起、result 導出的
// terminal state_change=done 止（§3.5.9）。
func emitCompleteTurn(t *testing.T, a *App, w appcore.WSID, text string) {
	t.Helper()
	emitUserMessage(t, a, w, text)
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindResult, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func emitUserMessage(t *testing.T, a *App, w appcore.WSID, text string) {
	t.Helper()
	if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindMessage, Role: "user", Text: text,
		Raw: []byte(`{"source":"workbench_user_input"}`)}); err != nil {
		t.Fatal(err)
	}
}

// newTestAppWithTurns：wsid 上有 n 個完整 turn 的 App。
func newTestAppWithTurns(t *testing.T, wsid string, n int) (*App, appcore.WSID) {
	t.Helper()
	a, _ := newTestApp(t)
	w := dormantWSID(t, a, wsid, contract.ProviderClaude)
	for i := 0; i < n; i++ {
		emitCompleteTurn(t, a, w, "turn-"+string(rune('a'+i%26)))
	}
	return a, w
}

func countCompleteTurns(envs []contract.Envelope) int {
	n := 0
	for _, e := range envs {
		if e.Kind == string(contract.KindStateChange) &&
			(e.State == string(contract.StateDone) || e.State == string(contract.StateFailed)) {
			n++
		}
	}
	return n
}

func isCanonicalUser(e contract.Envelope) bool {
	return e.Kind == string(contract.KindMessage) && e.Role == "user"
}

// hasOpenTurn：最後一筆 terminal state_change 之後還有 canonical user message
// ⇒ 視窗尾端帶著一個未結束的 turn。
func hasOpenTurn(envs []contract.Envelope) bool {
	lastTerminal := -1
	for i, e := range envs {
		if e.Kind == string(contract.KindStateChange) &&
			(e.State == string(contract.StateDone) || e.State == string(contract.StateFailed)) {
			lastTerminal = i
		}
	}
	for _, e := range envs[lastTerminal+1:] {
		if isCanonicalUser(e) {
			return true
		}
	}
	return false
}

// truncatedMidTurn：視窗第一筆不是 canonical user message ⇒ 從 turn 中間截斷
// （§3.8 明文禁止）。
func truncatedMidTurn(envs []contract.Envelope) bool {
	return len(envs) > 0 && !isCanonicalUser(envs[0])
}

func overlaps(a, b []contract.Envelope) bool {
	seen := map[string]bool{}
	for _, e := range a {
		seen[e.EventID] = true
	}
	for _, e := range b {
		if seen[e.EventID] {
			return true
		}
	}
	return false
}

// ---- rebuild 編排（§3.5.7 的呼叫端責任）----

// TestOnlyOneActiveRebuild：degraded 期間每一筆事件都可能觸發通知，排程這一
// 側若不 single-flight，就會疊出一堆互相以 ErrRebuildInProgress 回錯的重試鏈。
func TestOnlyOneActiveRebuild(t *testing.T) {
	a, _ := newTestApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookRebuildEntered = func() {
		close(entered) // 第二次進來就 panic——那本身就是 single-flight 被破壞
		<-release
	}
	for k := 0; k < 5; k++ {
		a.scheduleRebuild("test")
	}
	<-entered
	if got := a.rebuildStartsForTest(); got != 1 {
		t.Fatalf("同一時刻只能有一個 active rebuild：%d", got)
	}
	close(release)
	waitFor(t, "rebuild 收斂", func() bool { return !a.rebuildInFlight() })
}

// TestNotConvergedTriggersBackoffRetry：ErrRebuildNotConverged 的處置是
// 「保留 latch ＋ backoff 重試」，且等待必須遞增（不得 busy-loop）。
func TestNotConvergedTriggersBackoffRetry(t *testing.T) {
	a, _ := newTestApp(t)
	a.rebuildBackoffBase = time.Millisecond // 只縮短等待，不改變遞增形狀
	a.hookRebuildResult = func(n int) error {
		if n < 3 {
			return replayindex.ErrRebuildNotConverged
		}
		return nil
	}
	a.scheduleRebuild("test")
	waitFor(t, "rebuild 收斂", func() bool { return !a.rebuildInFlight() })
	if got := a.rebuildStartsForTest(); got != 3 {
		t.Fatalf("未收斂應 backoff 重試至成功：%d", got)
	}
	// 這裡刻意**不**斷言 Degraded()==false：hookRebuildResult 取代了真正的
	// RuntimeRebuild，index 從頭到尾沒被 latch 過，那個斷言恆為 true 而不是在
	// 驗任何東西。「重建成功會解除 latch」由走真實路徑的
	// TestIndexDegradedNotifyDoesNotDeadlockAndRecovers 負責。
	if !a.backoffDelaysIncreasingForTest() {
		t.Fatal("重試必須遞增 backoff，不得 busy-loop")
	}
}

// TestRebuildErrorDoesNotRetryForever：未收斂以外的錯誤（I/O、用錯前提）不
// 重試——重試對它們沒有新的成功理由，只會把錯誤洗成雜訊。
func TestRebuildErrorDoesNotRetryForever(t *testing.T) {
	a, _ := newTestApp(t)
	enableAuditFile(t, a)
	a.hookRebuildResult = func(int) error { return errors.New("disk gone") }
	a.scheduleRebuild("test")
	waitFor(t, "rebuild 收斂", func() bool { return !a.rebuildInFlight() })
	if got := a.rebuildStartsForTest(); got != 1 {
		t.Fatalf("非未收斂的錯誤不得重試：%d", got)
	}
	if !auditHas(t, a.stateDir, "replay_index_rebuild_error") {
		t.Fatal("重建失敗必須 fail loud 留下 audit 軌跡")
	}
}

// TestShutdownCancelsRebuild：重建是背景工作，不得擋住 shutdown；shutdown 也
// 必須真的把它收掉（不留 goroutine 在 backoff 裡等）。
func TestShutdownCancelsRebuild(t *testing.T) {
	a, _ := newTestApp(t)
	a.rebuildBackoffBase = 30 * time.Second // 停在 backoff 等待：只有 cancel 能解除
	entered := make(chan struct{})
	var once atomic.Bool
	a.hookRebuildEntered = func() {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
	}
	a.hookRebuildResult = func(int) error { return replayindex.ErrRebuildNotConverged }
	a.scheduleRebuild("test")
	<-entered

	done := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("rebuild 不得阻擋 shutdown")
	}
	if a.rebuildInFlight() {
		t.Fatal("shutdown 必須取消 rebuild")
	}
}

// TestScheduleRebuildRejectedAfterShutdown：Manager.Close 會 flush pending
// queue，那些事件仍走 Observe——若此時 index 才第一次寫失敗，通知會在收斂之後
// 又起一條沒有人會等的 goroutine。
func TestScheduleRebuildRejectedAfterShutdown(t *testing.T) {
	a, _ := newTestApp(t)
	a.shutdown(context.Background())
	a.scheduleRebuild("after shutdown")
	if a.rebuildInFlight() {
		t.Fatal("shutdown 之後不得再排新的重建")
	}
	if got := a.rebuildStartsForTest(); got != 0 {
		t.Fatalf("shutdown 之後不得再呼叫 RuntimeRebuild：%d", got)
	}
}

// TestScheduleRebuildRejectedOnceCancelled：關閉排程入口的判準必須是
// cancelRebuild 自己在 rebuildMu 下設的旗標，不是另一把鎖下的 a.shuttingDown。
//
// 用 a.shuttingDown 判斷時，「讀完放掉 shutMu」到「取得 rebuildMu」之間有窗口：
// shutdown 若正好在那段時間內設起旗標並跑完 cancelRebuild（那一刻 rebuildCancel
// 還是 nil，直接返回），排程仍會起一條沒有人取消、也沒有人等待的 goroutine，跨
// 過 Manager.Close 與 index.Flush 繼續跑 RuntimeRebuild。
//
// 這裡直接鎖住那個窗口收束後的不變量：**cancelRebuild 之後的排程一律不得起
// goroutine**，與 a.shuttingDown 是否已經設起無關。以 shutMu 為判準的寫法在這
// 條會紅（此時 shuttingDown 還是 false，排程會被放行）。
func TestScheduleRebuildRejectedOnceCancelled(t *testing.T) {
	a, _ := newTestApp(t)
	a.hookRebuildResult = func(int) error { return nil }

	a.cancelRebuild() // shutdown 序列裡的那一步（此時尚未有任何 in-flight 重建）
	a.scheduleRebuild("degraded notice racing shutdown")

	if a.rebuildInFlight() {
		t.Fatal("cancelRebuild 之後不得再起新的重建 goroutine")
	}
	if got := a.rebuildStartsForTest(); got != 0 {
		t.Fatalf("cancelRebuild 之後不得再呼叫 RuntimeRebuild：%d", got)
	}
}

// TestCancelRebuildWaitsForRacingSchedule：同一個窗口的另一半——排程先進的那條
// 交錯。cancelRebuild 必須等它收斂，不能因為「取鎖時 rebuildDone 還是 nil」就直
// 接返回，留下跨過 Manager.Close 的 goroutine。
func TestCancelRebuildWaitsForRacingSchedule(t *testing.T) {
	a, _ := newTestApp(t)
	release := make(chan struct{})
	finished := make(chan struct{})
	a.hookRebuildResult = func(int) error {
		<-release
		close(finished)
		return nil
	}
	a.scheduleRebuild("in flight")
	waitFor(t, "rebuild 進場", func() bool { return a.rebuildStartsForTest() == 1 })

	done := make(chan struct{})
	go func() { a.cancelRebuild(); close(done) }()
	select {
	case <-done:
		t.Fatal("cancelRebuild 必須等 in-flight 的那一輪收斂")
	case <-time.After(50 * time.Millisecond): // 只確認它還沒返回，不是同步手段
	}
	close(release)
	<-done
	select {
	case <-finished:
	default:
		t.Fatal("cancelRebuild 返回時那一輪必須已經跑完")
	}
	if a.rebuildInFlight() {
		t.Fatal("cancelRebuild 返回後不得仍有 in-flight 重建")
	}
}

// ---- degraded → 通知 → 重建 → 解除 latch 的完整 production 路徑 ----

// checkpointOnDisk：直接讀 replay-index/checkpoint.json（不經 Index）。
func checkpointOnDisk(t *testing.T, stateDir string) (offset int64, lastEventID string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "replay-index", "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cf struct {
		Offset      int64  `json:"offset"`
		LastEventID string `json:"last_event_id"`
	}
	if err := json.Unmarshal(b, &cf); err != nil {
		t.Fatal(err)
	}
	return cf.Offset, cf.LastEventID
}

// degradeIndex：以**真實的寫入失敗**（index 目錄轉唯讀）latch degraded——不用
// 測試鉤子，因為這條測試要驗的正是 production 通知路徑：Observe 於 Manager
// mutex 內失敗 → latch → 解鎖後 Notify → App 排程重建。回傳解除唯讀的函式。
func degradeIndex(t *testing.T, a *App, w appcore.WSID) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限，無法用唯讀目錄重現 index 寫入失敗")
	}
	dir := filepath.Join(a.stateDir, "replay-index")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	emitUserMessage(t, a, w, "turn that trips the index") // turn boundary → checkpoint 落盤失敗
	if !a.replayIndex.Degraded() {
		restore()
		t.Fatal("index 寫入失敗必須 latch degraded（§3.5.4）")
	}
	return restore
}

// TestIndexDegradedNotifyDoesNotDeadlockAndRecovers：一次跑完 §3.5.4 →
// §3.5.7 的整條 production 路徑。
//
// 這條同時是 Notify 重入契約的迴歸測試：Notify 是在**持有 Manager mutex 時**
// 被呼叫的（Observe 在 writeAndEmitLocked 裡），callback 若碰任何 Manager 入
// 口就是當場自我死鎖——emitUserMessage 能返回本身就是斷言。
func TestIndexDegradedNotifyDoesNotDeadlockAndRecovers(t *testing.T) {
	a, ui := newTestApp(t)
	enableAuditFile(t, a)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)

	ready := make(chan struct{})
	a.hookRebuildEntered = func() { <-ready } // 先擋住自動排程的那一輪

	restore := degradeIndex(t, a, w)
	if !a.rebuildInFlight() {
		t.Fatal("degraded 通知必須觸發重建排程")
	}
	restore()
	close(ready)

	waitFor(t, "rebuild 收斂", func() bool { return !a.rebuildInFlight() })
	if a.replayIndex.Degraded() {
		t.Fatal("重建成功必須解除 degraded latch")
	}
	// 通知本身要看得到（UI ＋ audit），否則 degraded 只能靠猜。
	// 通知走的是 a.emit（UI 出口），不是 Manager 的 Emit——後者會回寫
	// events.jsonl，而 Notify 是在 Manager mutex 內被觸發的，碰它就死鎖。
	var sawNotice bool
	for _, ev := range ui.find("workbench:event") {
		env, ok := ev.data.(contract.Envelope)
		if ok && env.Kind == string(contract.KindStreamError) && strings.Contains(env.Error, "degraded") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatal("degraded 必須發出 UI 通知")
	}
	if !auditHas(t, a.stateDir, "replay_index_degraded") {
		t.Fatal("degraded 必須留下 audit 軌跡")
	}
	// 解除 latch 之後 index 必須真的重新接手：新的完整 turn 要進得了 index。
	emitCompleteTurn(t, a, w, "after recovery")
	recs, err := a.replayIndex.RecentTurns("w1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("接回 writer 之後的新 turn 必須入 index")
	}
}

// TestAppendLandsMidRebuildScan：Task 19 把重建掃描切成分段、段間釋放 idx.mu，
// 目的就是讓 append 路徑（emitMu → Observe → idx.mu）不會因為重建掃描而長時間
// 停住。接線之後這個目的必須實際成立——不是只驗 replayindex 內部的分段計數，
// 而是驗真正的 append 路徑。
//
// 判準是**順序**不是時間：一筆真實 append 必須在鎖外掃描**還沒掃到本輪高水位**
// 之前就返回。
//
//   - 分段版：append 由第一次段間鉤子觸發，最多被下一段擋住 → 返回時 cursor 約
//     一兩段（數百 KB），遠小於 4MB 的高水位。
//   - 不分段（scanSegmentBytes 調大到涵蓋整份檔案）：段間鉤子要等整段掃完才會
//     第一次觸發，append 因此從一開始就落在 cursor 已達檔尾之後 → **恆紅**。
//
// 前一版用「單筆最長阻塞 / 重建全長」的比率斷言，餘裕不足（熱檔案快取下重建全
// 長掉到 ~390ms，任何無關的 scheduler／GC 停頓卡個 100ms 就誤紅），已改掉：這
// 條守的是 §3.5.7 的核心不變量，最不該變成「紅了先猜是不是 flake」的那一條。
func TestAppendLandsMidRebuildScan(t *testing.T) {
	a, _ := newTestApp(t)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)

	ready := make(chan struct{})
	a.hookRebuildEntered = func() { <-ready }
	restore := degradeIndex(t, a, w)
	restore()

	// 灌 audit：一律經 Manager／sink，不直接寫檔——auditEnd 讀的是 sink 的
	// atomic offset，繞過去寫會讓它低報檔尾（那是 finalCatchUpAndAttach 明文
	// fail loud 的契約違反，測到的就不是本條要驗的東西了）。
	payload := strings.Repeat("x", 2<<10)
	const bulkEvents = 2000 // ≈4MB ≈16 段（scanSegmentBytes = 256KB）
	for i := 0; i < bulkEvents; i++ {
		a.manager.EmitWorkspace("bulk", nil, map[string]string{"p": payload})
	}
	highWater := a.eventSink.End() // 本輪掃描的高水位：放行之前的 audit 檔尾

	// 段間鉤子（掃完一段、idx.mu 已釋放時觸發）：第一次進來就派一條 goroutine
	// 做一筆真實 append，並在鉤子內等它**確實開始跑**才放行下一段——否則
	// goroutine 可能遲到，變成量到排程延遲而不是鎖行為。
	var once sync.Once
	appendCursor := make(chan int64, 1)
	a.replayIndex.SetScanSegmentHookForTest(func() {
		once.Do(func() {
			running := make(chan struct{})
			go func() {
				close(running)
				a.manager.EmitWorkspace("live", nil, map[string]string{"i": "x"})
				appendCursor <- a.replayIndex.RebuildCursorForTest()
			}()
			<-running
		})
	})

	close(ready) // 放行重建：從 checkpoint 起掃 ≈4MB
	cursor := <-appendCursor
	waitFor(t, "rebuild 收斂", func() bool { return !a.rebuildInFlight() })

	segments := a.replayIndex.LockSegmentsForTest()
	t.Logf("append 返回時 cursor=%d（高水位 %d），本輪共分 %d 段", cursor, highWater, segments)
	if cursor >= highWater {
		t.Fatalf("append 一直等到掃描抵達高水位才返回（cursor=%d >= %d）"+
			"——分段釋放 idx.mu 沒有生效，整條 provider 事件管線會被重建掃描停住",
			cursor, highWater)
	}
	if segments < 2 {
		t.Fatalf("本輪只分了 %d 段，這條測試沒有真的測到段間窗口", segments)
	}
	if a.replayIndex.Degraded() {
		t.Fatal("重建應收斂並解除 degraded")
	}
}

// TestShutdownFlushesIndexCheckpoint：checkpoint 落盤被節流到 turn boundary
// （replayindex.Observe），所以正常關閉時最後一段可能還沒落盤——效果跟 crash
// 一樣。§3.6.5 的總序把 index flush 排在 Manager.Close 之後，這條驗它真的有
// 被呼叫、而且涵蓋 boundary 之後的事件。
func TestShutdownFlushesIndexCheckpoint(t *testing.T) {
	a, _ := newTestApp(t)
	w := dormantWSID(t, a, "w1", contract.ProviderClaude)

	emitUserMessage(t, a, w, "open a turn") // boundary：checkpoint 落盤
	boundaryOffset, _ := checkpointOnDisk(t, a.stateDir)
	for i := 0; i < 5; i++ { // 非 boundary：只前進記憶體 checkpoint
		if err := a.manager.Emit(w, contract.Event{Provider: contract.ProviderClaude,
			Kind: contract.KindDelta, Text: "chunk", Raw: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := checkpointOnDisk(t, a.stateDir); got != boundaryOffset {
		t.Fatalf("節流前提改變了：非 boundary 事件不該落盤 checkpoint（%d → %d）", boundaryOffset, got)
	}

	a.shutdown(context.Background())

	fi, err := os.Stat(a.eventsPath())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := checkpointOnDisk(t, a.stateDir)
	if got != fi.Size() {
		t.Fatalf("shutdown 必須 flush checkpoint 到關閉當下的進度：checkpoint=%d audit=%d",
			got, fi.Size())
	}
}

// ---- §3.8 視窗化載入 ----

// TestRestoreLoadsLast20TurnsPlusOpenTurn：釘選 pane 首次載入＝最近 20 個完整
// turn ＋未結束的目前 turn，且不得從 turn 中間截斷。
func TestRestoreLoadsLast20TurnsPlusOpenTurn(t *testing.T) {
	a, w := newTestAppWithTurns(t, "w1", 25)
	emitUserMessage(t, a, w, "still running") // 未結束的目前 turn

	got, err := a.LoadTurnsBefore("w1", "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if n := countCompleteTurns(got); n != turnPageSize {
		t.Fatalf("釘選 pane 應載最近 20 個完整 turn：%d", n)
	}
	if !hasOpenTurn(got) || truncatedMidTurn(got) {
		t.Fatal("未結束 turn 必須一併載入且不得從中間截斷（§3.8）")
	}
}

// TestPagingUsesBeforeEventIDCursor：向上捲以每次 20 turn 分頁，cursor 是
// before_event_id，兩頁不得重疊。
func TestPagingUsesBeforeEventIDCursor(t *testing.T) {
	a, _ := newTestAppWithTurns(t, "w1", 45)
	first, err := a.LoadTurnsBefore("w1", "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.LoadTurnsBefore("w1", first[0].EventID, turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if n := countCompleteTurns(page); n != turnPageSize || overlaps(first, page) {
		t.Fatalf("每頁 20 turn 且不得重疊：%d overlap=%v", n, overlaps(first, page))
	}
	if truncatedMidTurn(page) {
		t.Fatal("分頁同樣不得從 turn 中間截斷")
	}
	if hasOpenTurn(page) {
		t.Fatal("未結束 turn 只屬於尾端視窗，不該出現在往前的分頁裡")
	}
}

// TestWindowExcludesOtherSessions：turn record 記的是**全域** events.jsonl 的
// byte range，多 session 並行時別人的事件會夾在同一段裡——不過濾就會把別的
// 對話混進這個 pane。
func TestWindowExcludesOtherSessions(t *testing.T) {
	a, _ := newTestApp(t)
	w1 := dormantWSID(t, a, "w1", contract.ProviderClaude)
	w2 := dormantWSID(t, a, "w2", contract.ProviderCodex)

	emitUserMessage(t, a, w1, "w1 asks")
	if err := a.manager.Emit(w2, contract.Event{Provider: contract.ProviderCodex,
		Kind: contract.KindDelta, Text: "w2 streams", Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := a.manager.Emit(w1, contract.Event{Provider: contract.ProviderClaude,
		Kind: contract.KindResult, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	got, err := a.LoadTurnsBefore("w1", "", turnPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("w1 應載得到它自己的 turn")
	}
	for _, e := range got {
		if e.WorkspaceSessionID != "w1" {
			t.Fatalf("視窗混入其他 session 的事件：%+v", e)
		}
	}
}

// TestLoadTurnsBeforeWithoutIndexFailsLoud：index 不可用時一律 fail loud，
// 不退回全掃 events.jsonl——那正是 §3.5 要消滅的行為。
func TestLoadTurnsBeforeWithoutIndexFailsLoud(t *testing.T) {
	a, _ := newTestApp(t)
	a.replayIndex = nil
	if _, err := a.LoadTurnsBefore("w1", "", turnPageSize); !errors.Is(err, errNoReplayIndex) {
		t.Fatalf("index 不可用必須 fail loud：%v", err)
	}
}

// TestVerifyFailureMakesWindowFailLoud：啟動期 VerifyOrRebuild 失敗之後，index
// 可能停在「掃到一半」的狀態——接下來第一筆 live 事件會把 checkpoint 直接推到
// 它自己的 offset，跳過中間沒掃到的那段，留下靜默且永久的缺口。此時
// LoadTurnsBefore 只會少回幾個 turn 而不會回錯，使用者分辨不出來，所以必須
// latch 成 fail loud 並提示重啟。
func TestVerifyFailureMakesWindowFailLoud(t *testing.T) {
	dir := t.TempDir()
	seedEvents(t, dir,
		`{"event_id":"ev-1","provider":"claude","kind":"message","role":"user","workspace_session_id":"w1","text":"hi"}`,
		`{"event_id":"ev-2","provider":"claude","kind":"state_change","workspace_session_id":"w1","state":"done"}`)
	seedRegistry(t, dir, wsregistry.Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	a := newTestAppAt(t, dir)

	// 壞掉的 checkpoint.json 讓 verifyOrRebuildLocked 在解析階段就回錯（同一個
	// latch 對 rescan 中途失敗也成立，只是那個時序在測試裡造不出來）。
	if err := os.WriteFile(filepath.Join(dir, "replay-index", "checkpoint.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.restoreSessions(); err != nil {
		t.Fatalf("index 驗證失敗不得阻擋啟動（它是快取，不是權威）：%v", err)
	}
	if !strings.Contains(a.startupErr, "重啟") {
		t.Fatalf("啟動警告必須給出可操作指引：%q", a.startupErr)
	}
	if _, err := a.LoadTurnsBefore("w1", "", turnPageSize); !errors.Is(err, errIndexUnverified) {
		t.Fatalf("驗證失敗後的視窗載入必須 fail loud，不得靜默回不完整的視窗：%v", err)
	}
}
