package replayindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- 測試專用讀取入口（同 degraded_test.go 的 ForceWriteErrForTest 慣例：
// 欄位在 production 檔宣告，測試用的存取器只存在於 _test.go）----

// CatchUpAttemptsForTest：本輪鎖外 catch-up 的嘗試次數。
func (idx *Index) CatchUpAttemptsForTest() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.catchUpAttempts
}

// UnlockRetriesForTest：本輪「取鎖後殘量又超限、立即解鎖重試」的次數。
func (idx *Index) UnlockRetriesForTest() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.unlockRetries
}

// MaxBytesScannedUnderLockForTest：本輪鎖內單次補掃處理量的最大值。
func (idx *Index) MaxBytesScannedUnderLockForTest() int64 {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.maxLockedScanBytes
}

// LockSegmentsForTest／RebuildCursorForTest／SetScanSegmentHookForTest 已搬到
// index.go（production 檔）——package main 的接線測試要用它們鎖「append 落在
// 掃描中間」這條順序斷言，跨 package 就拿不到 _test.go 裡的符號。見該處 doc。

// HoldingLockForTest：目前是否持有呼叫端傳入的 emit mutex。刻意不取 idx.mu
// ——注入的 auditEndFunc 可能在 idx.mu 已被持有時被呼叫（見 rebuild.go 的欄位
// 說明），走 idx.mu 會自我死鎖。
func (idx *Index) HoldingLockForTest() bool { return idx.holdingEmitLock.Load() }

// ---- 測試工具 ----

// realAuditEnd：以檔案實際大小作為 audit 檔尾，即 production 的正常行為。
func realAuditEnd(path string) auditEndFunc {
	return func() (int64, error) {
		info, err := os.Stat(path)
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
}

// appendRawEvents：直接以檔案 I/O 把事件 append 進 audit（**不**經過 Index／
// Observe）——模擬 degraded latch 期間 provider turn 仍持續寫 audit：權威檔照
// 常前進，index 因為 latch 完全沒跟上。
func appendRawEvents(t *testing.T, auditPath string, envs ...contract.Envelope) {
	t.Helper()
	f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, env := range envs {
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			t.Fatal(err)
		}
	}
}

// appendCompleteTurn：append 一個完整 turn（canonical user message ＋ terminal
// state_change）到 audit，event id 以 tag 命名保持唯一。
func appendCompleteTurn(t *testing.T, auditPath, wsid, tag string) {
	t.Helper()
	appendRawEvents(t, auditPath,
		contract.Envelope{
			EventID: wsid + "-user-" + tag, Kind: string(contract.KindMessage),
			Role: "user", WorkspaceSessionID: wsid,
		},
		contract.Envelope{
			EventID: wsid + "-done-" + tag, Kind: string(contract.KindStateChange),
			State: string(contract.StateDone), WorkspaceSessionID: wsid,
		},
	)
}

// overLimitBytes：剛好超過 byte 收斂上限的殘量。測試需要的條件只是「殘量 >
// MaxCatchUpBytes」，用剛好超過而非成倍的量——`-race -count=30` 下每多灌一倍都
// 是實打實的檔案 I/O ＋掃描成本。
const overLimitBytes = MaxCatchUpBytes + 64<<10

// appendBytes：append 至少 n bytes 的**合法** audit 事件。刻意用少量大事件
// （每筆約 64KB 的 delta）而非大量小事件：測試要的是「殘量在 byte 上限之上」
// 這個條件，用大事件能在同樣 byte 數下把 JSON 解析次數壓到最低。全部事件組完
// 才一次 append（只開一次檔），避免每筆重開檔案的 syscall 開銷。delta 事件不
// 會改變任何 turn 狀態（沒有 open turn 時直接略過），所以不會污染 turn record
// 的斷言。
func appendBytes(t *testing.T, auditPath string, n int) {
	t.Helper()
	const chunk = 64 << 10
	filler := strings.Repeat("x", chunk)
	var envs []contract.Envelope
	var written int
	for k := 0; written < n; k++ {
		env := contract.Envelope{
			EventID: fmt.Sprintf("fill-%d-%d", n, k), Kind: string(contract.KindDelta),
			WorkspaceSessionID: "wfill", Text: filler,
		}
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		written += len(b) + 1
		envs = append(envs, env)
	}
	appendRawEvents(t, auditPath, envs...)
}

// duplicateRanges：turns 裡「(StartOffset, LastEventID) 這個唯一鍵重複出現」的
// 筆數。重建／重試若把同一段 audit 索引兩次，就會在這裡現形。
func duplicateRanges(turns []TurnRecord) int {
	seen := map[string]bool{}
	dup := 0
	for _, rec := range turns {
		key := fmt.Sprintf("%d|%s", rec.StartOffset, rec.LastEventID)
		if seen[key] {
			dup++
			continue
		}
		seen[key] = true
	}
	return dup
}

func mustTurns(t *testing.T, i *Index, wsid string) []TurnRecord {
	t.Helper()
	turns, err := i.recentTurns(wsid, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return turns
}

// ---- barrier 測試 ----

// TestRebuildCoversPreLockWindow：TOCTOU。事件恰落在「鎖外補掃已完成、殘量已
// 判定達標、emit mutex 尚未取得」這個窗口——這正是「邊掃邊解除 latch」會漏掉
// 的那一段。第 5 步的鎖內補掃必須涵蓋它，且不得因為重掃而產生重複 record。
func TestRebuildCoversPreLockWindow(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var once sync.Once
	i.hookAfterResidualOKBeforeLock = func() {
		once.Do(func() { appendCompleteTurn(t, audit, "w1", "late-turn") })
	}
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	turns, err := i.recentTurns("w1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("鎖前窗口的事件必須被鎖內補掃涵蓋：%d", len(turns))
	}
	// 註：rev3 之後這條是**第二層**守衛。cursor 單調遞增（結構性預防）之外還
	// 隔了一層去重，所以就算 cursor 不變量被破壞，這裡也不會變紅——守 cursor
	// 的是 TestRebuildRetryResumesFromCursorWithoutRebulk 的段末斷言。
	if dup := duplicateRanges(turns); dup != 0 {
		t.Fatalf("不得產生重複 record：%d", dup)
	}
	if i.Degraded() {
		t.Fatal("成功重建應解除 latch")
	}
}

// TestUnlockedScanReleasesIndexMutexBetweenSegments：鎖外掃描必須分段釋放
// idx.mu。§3.5.7 的字面只約束 emitMu，但 Task 20 接線後 append 路徑是
// emitMu → Observe → idx.mu：整段掃描霸佔 idx.mu 會讓一個 append 執行緒**握著
// emitMu 卡在 idx.mu 上**，停住的是整條 provider 事件管線——與第 4 步「不得在
// 鎖內硬掃」禁止的是同一種形狀，只是換了一把鎖。
//
// 兩件事一起斷言，缺一不可：(1) 掃描量遠大於 scanSegmentBytes 時分段數確實隨之
// 增加（不是整段掃到底）；(2) 段與段之間 idx.mu 真的**是放開的**——用 TryLock
// 直接證明，而不是只信賴計數器有加。
func TestUnlockedScanReleasesIndexMutexBetweenSegments(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))

	const segments = 6
	appendBytes(t, audit, segments*scanSegmentBytes)

	heldBetweenSegments := 0
	i.hookBetweenScanSegments = func() {
		if !i.mu.TryLock() {
			heldBetweenSegments++
			return
		}
		i.mu.Unlock()
	}

	var emitMu sync.Mutex
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	if heldBetweenSegments != 0 {
		t.Fatalf("段與段之間必須真的釋放 idx.mu：%d 次仍持有", heldBetweenSegments)
	}
	if got := i.LockSegmentsForTest(); got < segments {
		t.Fatalf("掃描量 %d bytes 應至少分成 %d 段釋放 idx.mu，實際只有 %d 段",
			segments*scanSegmentBytes, segments, got)
	}
}

// TestRuntimeRebuildRequiresDegraded：RuntimeRebuild 只服務 degraded latch。
// 「同一段 audit 只掃一次、所以不需要去重」這個論證的隱含前提是重建期間
// Observe 停著；index 若是活的，Observe 會與 catch-up 交錯前移 checkpoint 並寫
// record，前提就破了。所以前提必須是可檢查的不變量，而且要 fail loud——靜默補
// 上 latch 會發出呼叫端沒預期的 degraded 通知，把「呼叫端用錯了」蓋掉。
func TestRuntimeRebuildRequiresDegraded(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var emitMu sync.Mutex
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); !errors.Is(err, ErrNotDegraded) {
		t.Fatalf("非 degraded 必須 fail loud：%v", err)
	}
	if i.Degraded() {
		t.Fatal("不得靜默補 latch")
	}
}

// TestRuntimeRebuildIsSingleFlight：同一時間只能有一輪 RuntimeRebuild。
// ErrNotDegraded 擋不住這個——兩輪並行時**兩邊都看到 degraded=true**。
//
// 並行的第二輪從段間窗口（hookBetweenScanSegments）進場，那正是 rev2 的分段釋
// 放拉長的窗口：分段之前並行者大部分時間被擋在 idx.mu 上，分段之後它隨時進得
// 來。用真的 goroutine ＋ channel 交握，不用 sleep：第一輪在段間停下來等第二輪
// 回覆，時序完全確定。
func TestRuntimeRebuildIsSingleFlight(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))

	var emitMu sync.Mutex
	var secondErr error
	sawSecond := false
	// 「只做一次」用 buffered channel 當 token，**不用 sync.Once**：single-flight
	// 若被拿掉，第二輪會跑進自己的段間 hook，而 sync.Once.Do 會讓它**卡住等第
	// 一次 Do 返回**，第一次 Do 又在等第二輪——測試變成 hang 而不是 FAIL，缺
	// 陷訊號會被埋掉。select/default 讓非第一個呼叫者直接略過。
	firstSegment := make(chan struct{}, 1)
	firstSegment <- struct{}{}
	i.hookBetweenScanSegments = func() {
		select {
		case <-firstSegment:
		default:
			return
		}
		done := make(chan error, 1)
		go func() { done <- i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)) }()
		secondErr = <-done
		sawSecond = true
	}

	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatalf("第一輪應正常完成：%v", err)
	}
	if !sawSecond {
		t.Fatal("測試前提不成立：第二輪從未在段間窗口進場")
	}
	if !errors.Is(secondErr, ErrRebuildInProgress) {
		t.Fatalf("重入必須 fail loud，不得兩輪並行：%v", secondErr)
	}
	if i.Degraded() {
		t.Fatal("第一輪成功應解除 latch")
	}
}

// TestRebuildNeverConvergesKeepsLatch：sustained-append (a)——鎖外 catch-up 始
// 終無法達標。hook 掛在殘量檢查「之前」，每輪都把殘量重新推到上限之上，因此
// 取鎖階段從未被觸及。鎖住三件事：迭代有固定嘗試界限（不 busy-loop）、未收斂
// 保留 degraded latch、以及「從未達標就絕不取鎖」（lockAcquired==0、鎖內掃描
// 量為 0）。
func TestRebuildNeverConvergesKeepsLatch(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var lockAcquired int
	i.hookAfterResidualOKBeforeLock = func() { lockAcquired++ }
	i.hookAfterUnlockedCatchUp = func() { appendBytes(t, audit, overLimitBytes) }

	err = i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit))
	if !errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("界限內未達標應回 ErrRebuildNotConverged：%v", err)
	}
	if !i.Degraded() {
		t.Fatal("未收斂必須保留 degraded latch")
	}
	if got := i.CatchUpAttemptsForTest(); got != MaxCatchUpAttempts {
		t.Fatalf("必須有嘗試界限、不得 busy-loop：%d", got)
	}
	if lockAcquired != 0 {
		t.Fatalf("殘量從未達標，不應進入取鎖階段：%d", lockAcquired)
	}
	if i.MaxBytesScannedUnderLockForTest() != 0 {
		t.Fatal("從未取鎖，鎖內不應有掃描")
	}
}

// TestRebuildRecordLimitBindsIndependently：收斂上限是 byte 與 record **雙**上
// 限，兩者各自獨立生效。這裡灌入的是大量小事件（delta 串流的常態形狀）：總量
// 遠低於 MaxCatchUpBytes，但筆數超過 MaxCatchUpRecords，仍必須判定未達標、不
// 取鎖。少了 record 上限，這種形狀的殘量會整批被放進鎖內補掃，鎖內要處理的
// record 數就沒有界限。
func TestRebuildRecordLimitBindsIndependently(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	var lockAcquired int
	i.hookAfterResidualOKBeforeLock = func() { lockAcquired++ }
	round := 0
	i.hookAfterUnlockedCatchUp = func() {
		round++
		envs := make([]contract.Envelope, 0, MaxCatchUpRecords+64)
		for k := 0; k < MaxCatchUpRecords+64; k++ {
			envs = append(envs, contract.Envelope{
				EventID: fmt.Sprintf("tiny-%d-%d", round, k), Kind: string(contract.KindDelta),
				WorkspaceSessionID: "wfill",
			})
		}
		appendRawEvents(t, audit, envs...)
	}

	sizeBefore := auditSize(t, audit)
	err = i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit))
	if !errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("筆數超過 record 上限即使 byte 量很小也不得達標：%v", err)
	}
	// 測試前提：每輪灌入量必須確實遠低於 byte 上限，否則測到的是 byte 上限。
	if perRound := (auditSize(t, audit) - sizeBefore) / int64(MaxCatchUpAttempts); perRound > MaxCatchUpBytes {
		t.Fatalf("測試前提不成立：每輪灌入 %d bytes 已超過 byte 上限 %d", perRound, MaxCatchUpBytes)
	}
	if lockAcquired != 0 {
		t.Fatalf("殘量筆數超限，不應進入取鎖階段：%d", lockAcquired)
	}
	if !i.Degraded() {
		t.Fatal("未收斂必須保留 degraded latch")
	}
}

// TestRebuildOverLimitUnderLockUnlocksAndRetries：sustained-append (b)——殘量
// 達標、取鎖後再次超限。用 auditEnd 注入模擬「等待 emitMu 期間其他 goroutine
// 持續 append」；production 中 append 必須持有同一把 emit mutex，鎖內 append
// 不可能發生，注入檔尾才是忠實重現這個窗口的方式。鎖住的是第 4 步的處置：
// 立即 unlock 回到鎖外重試，**不得在鎖內硬掃**（鎖內處理量必須始終低於凍結上
// 限），且重試不得產生重複 record。
func TestRebuildOverLimitUnderLockUnlocksAndRetries(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	var emitMu sync.Mutex
	real := realAuditEnd(audit)
	burst := 2
	fake := func() (int64, error) {
		end, err := real()
		if err != nil {
			return 0, err
		}
		if i.HoldingLockForTest() && burst > 0 {
			burst--
			return end + MaxCatchUpBytes*2, nil
		}
		return end, nil
	}
	if err := i.RuntimeRebuild(audit, &emitMu, fake); err != nil {
		t.Fatal(err)
	}
	if got := i.MaxBytesScannedUnderLockForTest(); got > MaxCatchUpBytes {
		t.Fatalf("鎖內處理量超過凍結上限：%d", got)
	}
	if i.UnlockRetriesForTest() < 2 {
		t.Fatalf("超限應立即解鎖重試：%d", i.UnlockRetriesForTest())
	}
	// 同 TestRebuildCoversPreLockWindow：rev3 之後這是第二層守衛，cursor 不變
	// 量本身由 TestRebuildRetryResumesFromCursorWithoutRebulk 的段末斷言守。
	if dup := duplicateRanges(mustTurns(t, i, "w1")); dup != 0 {
		t.Fatalf("重試不得產生重複 record：%d", dup)
	}
}

// TestRebuildUnderReportedAuditEndFailsLoud：鎖內補掃的終點夾在第 4 步通過檢查
// 的那個 end，而不是檔案真實 EOF——否則 auditEnd 低報時，鎖內處理量會靜默超過
// 凍結上限（maxLockedScanBytes 只記錄、不攔截）。但夾住之後不能就這樣接回：
// checkpoint 會停在低報的 end，latch 一解除 Observe 就從那裡繼續，中間那段永遠
// 沒人索引。所以低報必須 fail loud、保留 latch，不得靜默留缺口。
func TestRebuildUnderReportedAuditEndFailsLoud(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 1)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	before, _ := i.checkpoint()

	// 低報只發生在持有 emitMu 時，且要低報的正是「鎖前窗口」剛進來的那一
	// 段——鎖外 catch-up 一律掃到檔案真實 EOF，只有第 5 步的補掃會被 end 夾
	// 住，所以這是唯一能讓夾住造成缺口的形狀。
	var emitMu sync.Mutex
	real := realAuditEnd(audit)
	var once sync.Once
	i.hookAfterResidualOKBeforeLock = func() {
		once.Do(func() { appendCompleteTurn(t, audit, "w1", "unreported") })
	}
	preLockEnd := auditSize(t, audit)
	underReporting := func() (int64, error) {
		if i.HoldingLockForTest() {
			return preLockEnd, nil // 看不到鎖前窗口那一段
		}
		return real()
	}
	err = i.RuntimeRebuild(audit, &emitMu, underReporting)
	if err == nil || errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("auditEnd 低報必須 fail loud：%v", err)
	}
	if !i.Degraded() {
		t.Fatal("低報不得解除 latch——那會留下永遠沒人索引的缺口")
	}
	if off, _ := i.checkpoint(); off != before {
		t.Fatalf("低報不得前移 checkpoint：%d", off)
	}
}

// TestRebuildCursorIndependentOfCheckpoint：rebuild cursor 與 checkpoint 分
// 離。degraded 期間 checkpoint 依 §3.5.4 不得前移，但重建必須知道自己掃到哪
// 裡——兩者若共用同一個欄位，要嘛 checkpoint 在 latch 未解除時就前移（違反
// §3.5.4，且崩潰後會宣稱索引了實際沒寫進 turn file 的區段），要嘛 catch-up
// 每輪都得從頭重掃。
//
// 與 brief 原版的唯一差異：latch 之後補 append 一個完整 turn。原版
// seedAuditWithTurns 收尾時 checkpoint 恰好等於 audit 檔尾，degraded 期間又完
// 全沒有新事件，rebuild cursor 沒有任何可以前進的空間，`cursor > before` 這個
// 斷言恆不成立。補上這筆 append 才是 §3.5.4 真正要描述的情境：latch 期間權威
// 檔照常前進、checkpoint 停住不動。
func TestRebuildCursorIndependentOfCheckpoint(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	appendCompleteTurn(t, audit, "w1", "post-latch") // latch 期間 provider 仍在寫 audit
	before, _ := i.checkpoint()
	var emitMu sync.Mutex
	i.hookAfterUnlockedCatchUp = func() {
		if off, _ := i.checkpoint(); off != before {
			t.Errorf("degraded 期間 checkpoint 不得前移：%d → %d", before, off)
		}
		if i.RebuildCursorForTest() <= before {
			t.Error("rebuild cursor 必須獨立前進")
		}
	}
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	if after, _ := i.checkpoint(); after <= before {
		t.Fatal("成功接回後 checkpoint 才前移")
	}
}

// TestCrashDuringRuntimeRebuildDoesNotDuplicate：crash 落在 degraded 重建中途。
// 「cursor 單調遞增所以不可能重複」這個結構性論證只在**單一 process 生命週期
// 內**成立：rebuildCursor 永不落盤、degraded 期間 checkpoint 停住，但
// appendTurnRecord 是當場落盤的。所以重建進行到一半 crash，磁碟上會留下「turn
// record 已經比 checkpoint 前進」的狀態，重啟後 VerifyOrRebuild 從舊
// checkpoint 重掃同一段 audit，就有機會把同一個 turn 再寫一次。
//
// 首次實測（2026-08-15）確認**既有機制沒有涵蓋**：會產生一筆完全重複的 record
// （同一組 StartOffset／EndOffset／FirstEventID／LastEventID 出現兩次）。Task
// 17 的 index-ahead 偵測管的是「checkpoint 超前 audit」，
// checkpointTrustedLocked 只驗證 checkpoint 是否落在 audit 的真實行邊界、event
// id 對不對得上——它從不比對 turn file 的內容，所以「turn record 超前
// checkpoint」這個方向完全在它的視野之外：舊 checkpoint 被判定可信 →
// rescanFromLocked 從舊 offset 重掃 → 重建期間已落盤的 turn record 再寫一次。
//
// 修法（2026-08-15 裁決）：重建路徑依 `(StartOffset, LastEventID)` 唯一鍵去
// 重，見 index.go 的 beginRebuildDedupLocked。本測試即該修法的回歸測試。
func TestCrashDuringRuntimeRebuildDoesNotDuplicate(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	before, _ := i.checkpoint()
	appendCompleteTurn(t, audit, "w1", "post-latch") // latch 期間 provider 仍在寫 audit

	// 逼重建不收斂而中止：post-latch turn 已經被 bulk 索引、turn record 已落
	// 盤，但 checkpoint 依 §3.5.4 完全沒有前移。
	var emitMu sync.Mutex
	i.hookAfterUnlockedCatchUp = func() { appendBytes(t, audit, overLimitBytes) }
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); !errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("測試前提：本輪應未收斂：%v", err)
	}
	if off, _ := i.checkpoint(); off != before {
		t.Fatalf("測試前提：未收斂不得前移 checkpoint：%d", off)
	}
	if len(mustTurns(t, i, "w1")) != 3 {
		t.Fatalf("測試前提：post-latch turn 應已落盤：%d", len(mustTurns(t, i, "w1")))
	}

	// crash：process 死掉，rebuildCursor 隨記憶體消失；磁碟上只剩「舊
	// checkpoint ＋已經多寫了一筆的 turn file」。重啟走啟動期修復路徑。
	i2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := i2.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}

	turns := mustTurns(t, i2, "w1")
	if dup := duplicateRanges(turns); dup != 0 {
		t.Fatalf("重啟修復不得把重建期間已落盤的 turn record 再寫一次：dup=%d turns=%+v", dup, turns)
	}
	if len(turns) != 3 {
		t.Fatalf("重啟後應精確保有 3 個 turn、不重不漏：%d", len(turns))
	}
}

// TestRestartAfterSuccessfulAttachDoesNotDuplicate：上面那條 skip 掉的測試的**對
// 照組**。同樣是「重建 → 重啟 → VerifyOrRebuild」，唯一差別是重建這次有跑完
// 第 5 步（checkpoint 前移並落盤）。這條會過，證明 Important C 的重複不是
// 「seedRig ＋ VerifyOrRebuild 本來就會重複」這種與情境無關的雜訊，而**確實**
// 專屬於「crash 落在 checkpoint 尚未前移的那個窗口」。
func TestRestartAfterSuccessfulAttachDoesNotDuplicate(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	appendCompleteTurn(t, audit, "w1", "post-latch")

	var emitMu sync.Mutex
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}

	i2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := i2.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	turns := mustTurns(t, i2, "w1")
	if dup := duplicateRanges(turns); dup != 0 || len(turns) != 3 {
		t.Fatalf("成功接回後重啟不得重複或遺漏：dup=%d len=%d", dup, len(turns))
	}
}

// TestRebuildRetryResumesFromCursorWithoutRebulk：不收斂之後的 backoff 重試
// （§3.5.7「重試從已索引位置續掃，不重跑 bulk」）。第一輪被 hook 逼到嘗試界
// 限而中止，第二輪必須從 rebuildCursor 續掃——若重試把 cursor 重設回
// checkpointOffset（degraded 期間它並沒有前移），第一輪已經寫進 turn file 的
// record 會被完整重寫一次，duplicateRanges 就會抓到。
func TestRebuildRetryResumesFromCursorWithoutRebulk(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	i.latchDegraded(errors.New("seed"))
	before, _ := i.checkpoint()
	appendCompleteTurn(t, audit, "w1", "post-latch") // 第一輪 bulk 會索引到這個 turn

	var emitMu sync.Mutex
	i.hookAfterUnlockedCatchUp = func() { appendBytes(t, audit, overLimitBytes) }
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); !errors.Is(err, ErrRebuildNotConverged) {
		t.Fatalf("第一輪應未收斂：%v", err)
	}
	cursorAfterFirst := i.RebuildCursorForTest()
	if cursorAfterFirst <= before {
		t.Fatalf("第一輪應已推進 rebuild cursor：%d <= %d", cursorAfterFirst, before)
	}
	if off, _ := i.checkpoint(); off != before {
		t.Fatalf("未收斂不得前移 checkpoint：%d", off)
	}

	// 第二輪：停止灌入，殘量收斂得了。
	i.hookAfterUnlockedCatchUp = nil
	// **段末**斷言，不是只看輪末：cursor 若被重設回 checkpointOffset 再重掃，
	// 輪末它一樣會回到 EOF，所以「輪末 cursor 沒倒退」量不到重跑 bulk。而且
	// rev3 的去重會把重掃的 record 靜默略過，turn 數與 duplicateRanges 兩條也
	// 一起失去牙齒——這條直接綁「cursor 單調遞增」這個不變量本身，不經由 turn
	// 數、不受去重層影響。
	i.hookBetweenScanSegments = func() {
		if c := i.RebuildCursorForTest(); c < cursorAfterFirst {
			t.Errorf("重試不得倒退重跑 bulk：段末 cursor %d < %d", c, cursorAfterFirst)
		}
	}
	if err := i.RuntimeRebuild(audit, &emitMu, realAuditEnd(audit)); err != nil {
		t.Fatal(err)
	}
	if i.RebuildCursorForTest() < cursorAfterFirst {
		t.Fatalf("重試不得倒退重跑 bulk：%d < %d", i.RebuildCursorForTest(), cursorAfterFirst)
	}
	turns := mustTurns(t, i, "w1")
	if len(turns) != 3 { // 原本 2 個 turn ＋ latch 後補的 1 個
		t.Fatalf("重試後 turn 數應精確、不重不漏：%d", len(turns))
	}
	if dup := duplicateRanges(turns); dup != 0 {
		t.Fatalf("重試不得重跑 bulk 造成重複 record：%d", dup)
	}
	if i.Degraded() {
		t.Fatal("第二輪成功應解除 latch")
	}
}
