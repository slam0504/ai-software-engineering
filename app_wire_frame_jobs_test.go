package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
)

// ---- §3.4.3 歷史歸屬展開的非阻塞結算（owner 2026-08-18 契約）----
//
// 守的是六條契約：同步邊界不變、單一背景 worker、per-generation 只重建一次、
// finalize 直接產 sidecar、pending→resolved／failed 的可觀測狀態、bounded drain ＋
// 下次啟動補完。
//
// **時序陷阱（失效形狀 D）**：背景 worker 的測試很容易在「工作還沒開始」或「已經
// 做完」時斷言，受測路徑根本沒被走到。本檔一律用 barrier 卡在**真正的重建點**
// （hookWireIndexLoad 是唯一碰磁碟的地方），不用 time.Sleep。

// ---- fixture ----

// wireLoadBarrier：卡在歷史重建點的 barrier ＋ 計數器。
//
// **只卡第一次**：後續呼叫直接放行並計數。這讓「把展開改回同步／讓 shutdown 自己
// 把佇列做完」這類 mutation 打出來的是**計數變多**（乾淨的紅），而不是整個測試卡死。
type wireLoadBarrier struct {
	mu      sync.Mutex
	loads   []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newWireLoadBarrier(t *testing.T, a *App) *wireLoadBarrier {
	t.Helper()
	b := &wireLoadBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	a.hookWireIndexLoad = func(id string) {
		b.mu.Lock()
		b.loads = append(b.loads, id)
		first := len(b.loads) == 1
		b.mu.Unlock()
		if !first {
			return
		}
		close(b.entered)
		<-b.release
	}
	t.Cleanup(b.releaseAll) // 讓 worker 收工，否則 TempDir 清理時還有人在寫
	return b
}

func (b *wireLoadBarrier) releaseAll() { b.once.Do(func() { close(b.release) }) }

func (b *wireLoadBarrier) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.loads)
}

func (b *wireLoadBarrier) countOf(wireLogID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, id := range b.loads {
		if id == wireLogID {
			n++
		}
	}
	return n
}

// runFramingSession：走 production 的 StartSession → 一輪 turn → EndSession。
func runFramingSession(t *testing.T, a *App, ctl *framingWire, w appcore.WSID, thread, resume string) {
	t.Helper()
	ctl.setNextThread(thread)
	if err := a.StartSession(string(w), "hi "+thread, resume, "rec-"+thread, "task-"+thread, ""); err != nil {
		t.Fatalf("StartSession(%s): %v", w, err)
	}
	waitTurnSettled(t, a, w)
	if err := a.EndSession(string(w)); err != nil {
		t.Fatalf("EndSession(%s): %v", w, err)
	}
}

// noDrain：把 shutdown 的 bounded drain 窗口設成 0——「不等」。斷言因此不依賴牆鐘，
// 也不會因為 worker 卡在 barrier 上而讓收尾測試卡死。
func noDrain(a *App) {
	d := time.Duration(0)
	a.wireJobDrain = &d
}

// ---- 稽核／待辦讀取 ----

// frameResult 是背景工作寫出的 codex_wire_segment_frames 記錄。
type frameResult struct {
	ViewID       string            `json:"viewId"`
	WSID         string            `json:"wsid"`
	FramesStatus string            `json:"framesStatus"`
	Frames       map[string][]int  `json:"frames"`
	Errors       map[string]string `json:"errors"`
}

func auditFrameResults(t *testing.T, a *App) []frameResult {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	var out []frameResult
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Kind string      `json:"kind"`
			Data frameResult `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Kind != "codex_wire_segment_frames" {
			continue
		}
		out = append(out, rec.Data)
	}
	return out
}

func findFrameResult(t *testing.T, a *App, viewID string) (frameResult, bool) {
	t.Helper()
	for _, r := range auditFrameResults(t, a) {
		if r.ViewID == viewID {
			return r, true
		}
	}
	return frameResult{}, false
}

// waitForFrameResult：等背景工作把某個 view 的結果寫出來（等條件不等時間）。
func waitForFrameResult(t *testing.T, a *App, viewID string) frameResult {
	t.Helper()
	var got frameResult
	waitFor(t, "view "+viewID+" 的 frame 歸屬結算完成", func() bool {
		r, ok := findFrameResult(t, a, viewID)
		got = r
		return ok
	})
	return got
}

// pendingJobViewIDs：job journal 裡還沒被劃掉的待辦（＝下次啟動要補完的）。
// 刻意直接讀檔而不是問 App：跨重啟補完那一維的證據只能在磁碟上。
func pendingJobViewIDs(t *testing.T, a *App) []string {
	t.Helper()
	b, err := os.ReadFile(a.wireFrameJobsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("讀 wire-frame-jobs.jsonl：%v", err)
	}
	pending := map[string]bool{}
	var order []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec struct {
			ViewID string `json:"view_id"`
			Done   bool   `json:"done"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			t.Fatalf("job journal 壞行：%s", line)
		}
		if rec.Done {
			delete(pending, rec.ViewID)
			continue
		}
		if !pending[rec.ViewID] {
			order = append(order, rec.ViewID)
		}
		pending[rec.ViewID] = true
	}
	var out []string
	for _, id := range order {
		if pending[id] {
			out = append(out, id)
		}
	}
	return out
}

// ---- 守門 1：barrier 卡住歷史重建，EndSession 仍能及時返回 ----

// TestEndSessionReturnsWhileHistoryRebuildIsBlocked：契約第 1／2／5 條。
//
// w 在 gen1 跑過一輪、B1 restart 開 gen2、再跑一輪。第二次收尾時 segs 橫跨兩代，
// gen1 必須讀磁碟——barrier 就卡在那裡。`EndSession` 必須在 worker 還被卡住時就返回，
// 而且**已經留下 framesStatus=pending 的稽核**（契約第 5 條：證據還沒算完是可觀測
// 狀態，不是靠事後推測）。
//
// mutation：把歷史展開改回在 closeWireSegment 裡同步做 → 「EndSession 仍返回」那條
// waitFor 逾時紅。
func TestEndSessionReturnsWhileHistoryRebuildIsBlocked(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-hist", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-frames"); err != nil {
		t.Fatalf("B1 受控 restart：%v", err)
	}

	ctl.setNextThread("t-hist")
	if err := a.StartSession(string(w), "again", "t-hist", "rec-hist", "task-hist", ""); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, a, w)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.EndSession(string(w)); err != nil {
			t.Errorf("EndSession: %v", err)
		}
	}()
	waitFor(t, "歷史重建被 barrier 卡住時 EndSession 仍返回", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})

	view := auditSegments(t, a, w)
	if view.FramesStatus != "pending" {
		t.Fatalf("歷史還沒展開時必須留下 framesStatus=pending 的稽核：%+v", view)
	}
	if len(view.PendingWireLogs) != 1 || view.PendingWireLogs[0] != gen1 {
		t.Fatalf("pending 的 generation 必須指名是哪一代：%+v（gen1=%s）", view.PendingWireLogs, gen1)
	}
	if _, ok := findFrameResult(t, a, view.ViewID); ok {
		t.Fatal("worker 還卡在 barrier 上，不該已經有結算結果")
	}
	<-bar.entered // 受測路徑真的被走到（失效形狀 D：不是「還沒開始」就斷言完了）
	if got := view.Frames[currentWireLogID(a)]; len(got) == 0 {
		t.Fatalf("本代的結算必須同步完成（契約第 1 條）：%+v", view.Frames)
	}

	bar.releaseAll()
	res := waitForFrameResult(t, a, view.ViewID)
	if res.FramesStatus != "resolved" {
		t.Fatalf("放行後必須 resolved：%+v", res)
	}
}

// ---- 守門 2：多個 session 指向同一 generation，只讀取／重建一次 ----

// TestSharedGenerationIsExpandedOnce：契約第 3 條。
//
// w1／w2 都在 gen1 跑過，B1 restart 之後兩個各再跑一輪。兩筆背景工作都要展開 gen1，
// 但 gen1 只能碰一次磁碟。
//
// mutation：拿掉 wireIdxCache（每次都重新載入）→ countOf(gen1)==2 紅。
func TestSharedGenerationIsExpandedOnce(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	bar.releaseAll() // 本條不需要卡住，只數次數
	noDrain(a)

	w1, w2 := mustCreate(t, a, "codex"), mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w1, "t-s1", "")
	runFramingSession(t, a, ctl, w2, "t-s2", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-shared"); err != nil {
		t.Fatal(err)
	}
	runFramingSession(t, a, ctl, w1, "t-s1", "t-s1")
	v1 := auditSegments(t, a, w1)
	runFramingSession(t, a, ctl, w2, "t-s2", "t-s2")
	v2 := auditSegments(t, a, w2)

	r1 := waitForFrameResult(t, a, v1.ViewID)
	r2 := waitForFrameResult(t, a, v2.ViewID)
	if r1.FramesStatus != "resolved" || r2.FramesStatus != "resolved" {
		t.Fatalf("兩筆都要 resolved：%+v %+v", r1, r2)
	}
	if n := bar.countOf(gen1); n != 1 {
		t.Fatalf("同一個 generation 在一個 app run 內只能讀／重建一次，實際 %d 次（loads=%v）",
			n, bar.loads)
	}
	// 兩個 session 拿到的是**各自**的 frame，不是共用同一份（cache 共用的是「這一代的
	// 全部歸屬」，不是「某個 WSID 的答案」）。
	if len(r1.Frames[gen1]) == 0 || len(r2.Frames[gen1]) == 0 {
		t.Fatalf("兩者都必須有自己在 gen1 的 frame：%+v / %+v", r1.Frames, r2.Frames)
	}
	if equalInts(r1.Frames[gen1], r2.Frames[gen1]) {
		t.Fatalf("共用 cache 不得讓兩個 session 拿到同一份 frame：%v", r1.Frames[gen1])
	}
}

// ---- 守門 3：背景工作途中 crash，重啟後可由 pending 記錄補完 ----

// TestPendingFrameJobIsRecoveredAfterRestart：契約第 6 條，失效形狀 (F)。
//
// 第一個 App 的 worker 卡在 barrier 上就「當掉」（drain 窗口 0、關掉 job journal
// handle，等同 done 那一筆從未寫入）。第二個 App 以同一組 stateDir 重開，必須從
// job journal 把待辦讀回來並補完。
//
// **一定要開第二個 App**：同一個實例假裝重啟守不到跨 process 這一維。
//
// mutation：enqueueWireFrameJob 不落盤（只排記憶體佇列）→ 重啟後沒有待辦可補 → 紅。
func TestPendingFrameJobIsRecoveredAfterRestart(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-crash", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-crash"); err != nil {
		t.Fatal(err)
	}
	runFramingSession(t, a, ctl, w, "t-crash", "t-crash")
	view := auditSegments(t, a, w)
	if view.FramesStatus != "pending" {
		t.Fatalf("precondition：這一筆必須是 pending，否則沒有待辦可補：%+v", view)
	}
	<-bar.entered // worker 真的卡在重建點上（不是還沒開始）

	// 「當掉」：待辦已落盤、done 從未寫入。
	a.drainWireFrameJobs()
	a.closeWireFrameJobs()
	if got := pendingJobViewIDs(t, a); len(got) != 1 || got[0] != view.ViewID {
		t.Fatalf("磁碟上必須留著這筆待辦：%v（want [%s]）", got, view.ViewID)
	}
	if _, ok := findFrameResult(t, a, view.ViewID); ok {
		t.Fatal("precondition：崩潰前不該已經有結算結果")
	}

	// 真正的重啟：新的 App，記憶體裡沒有任何佇列。
	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	enableAudit(t, b)
	res := waitForFrameResult(t, b, view.ViewID)
	if res.FramesStatus != "resolved" {
		t.Fatalf("重啟補完必須 resolved：%+v", res)
	}
	if len(res.Frames[gen1]) == 0 {
		t.Fatalf("補完的結果必須含崩潰前那一代的 frame：%+v", res.Frames)
	}
	waitFor(t, "待辦被劃掉", func() bool { return len(pendingJobViewIDs(t, b)) == 0 })
	bar.releaseAll()
}

// ---- 守門 4：完整結果仍含所有歷史 generation ----

// TestResolvedFramesCoverEveryHistoricalGeneration：契約第 1／2 條的正確性那一半
// ——非阻塞不得換來「漏掉舊 frame」。
//
// w 橫跨三代（兩次 B1 restart），最後一次收尾的結算必須三代都在，且每一代的 frame
// 與該代 wire log 原文裡 wsid 欄位一致（oracle 是磁碟原文，不是被測的 cache）。
//
// mutation：resolveWireFrameJob 只展開 LiveGenID → 少兩代 → 紅。
func TestResolvedFramesCoverEveryHistoricalGeneration(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	bar.releaseAll()
	noDrain(a)

	w := mustCreate(t, a, "codex")
	var gens []string
	runFramingSession(t, a, ctl, w, "t-span", "")
	gens = append(gens, currentWireLogID(a))
	for i := 0; i < 2; i++ {
		if err := a.RestartCodexServerRecorded("b1-span"); err != nil {
			t.Fatal(err)
		}
		runFramingSession(t, a, ctl, w, "t-span", "t-span")
		gens = append(gens, currentWireLogID(a))
	}
	if gens[0] == gens[1] || gens[1] == gens[2] {
		t.Fatalf("precondition：三代必須互異：%v", gens)
	}

	view := auditSegments(t, a, w)
	res := waitForFrameResult(t, a, view.ViewID)
	if res.FramesStatus != "resolved" {
		t.Fatalf("必須 resolved：%+v", res)
	}
	if len(res.Frames) != len(gens) {
		t.Fatalf("結算必須涵蓋全部 %d 代：%+v", len(gens), res.Frames)
	}
	for _, g := range gens {
		want := framesOwnedBy(readWireRows(t, a, g), string(w)) // oracle：磁碟原文
		if len(want) == 0 {
			t.Fatalf("precondition：generation %s 裡必須有 w 的 frame", g)
		}
		if !equalInts(res.Frames[g], want) {
			t.Fatalf("generation %s 的 frame 不符：%v（want %v）", g, res.Frames[g], want)
		}
	}
}

// ---- 守門 5：corrupt generation 產生明確 failed，不得偽裝成零 frame ----

// TestCorruptGenerationYieldsFailedNotEmptyFrames：契約第 5 條。
//
// 「這一代讀不出來」與「這一代沒有我的 frame」在證據上是完全不同的兩件事。把前者
// 記成後者，稽核者會以為證據完整——正是這個里程碑反覆出現的失效形狀。
//
// mutation：resolveWireFrameJob 把重建錯誤吞掉並填 frames[id]=nil、狀態維持
// resolved → 紅。
func TestCorruptGenerationYieldsFailedNotEmptyFrames(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	bar.releaseAll()
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-rot", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-rot"); err != nil {
		t.Fatal(err)
	}

	// sidecar 拿掉 → 必須 fallback 重讀錄流；錄流中段插一行壞的 → 重建 fail loud。
	if err := os.Remove(filepath.Join(a.wireLogDir(), gen1+".frames.json")); err != nil {
		t.Fatalf("precondition：finalize 應已產生 sidecar：%v", err)
	}
	corruptWireLogMidFile(t, a, gen1)

	runFramingSession(t, a, ctl, w, "t-rot", "t-rot")
	view := auditSegments(t, a, w)
	res := waitForFrameResult(t, a, view.ViewID)
	if res.FramesStatus != "failed" {
		t.Fatalf("讀不出來的 generation 必須記成 failed，不得偽裝成零 frame：%+v", res)
	}
	if _, present := res.Frames[gen1]; present {
		t.Fatalf("讀不出來的那一代不得出現在 frames 裡（會被讀成「沒有我的 frame」）：%+v", res.Frames)
	}
	if !strings.Contains(res.Errors[gen1], gen1) {
		t.Fatalf("errors 必須指名是哪一代讀不出來：%+v", res.Errors)
	}
	if len(res.Frames[currentWireLogID(a)]) == 0 {
		t.Fatalf("讀得出來的那一代照樣要有結果：%+v", res.Frames)
	}
}

// corruptWireLogMidFile：在錄流中段插入一行壞 JSON（中段損壞＝RebuildFrameIndex
// 的 fail loud 分級，不是可容忍的檔尾截斷）。
func corruptWireLogMidFile(t *testing.T, a *App, wireLogID string) {
	t.Helper()
	p := filepath.Join(a.wireLogDir(), wireLogID+".jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("precondition：錄流至少要有 4 行才談得上中段：%d", len(lines))
	}
	mid := len(lines) / 2
	out := append([]string{}, lines[:mid]...)
	out = append(out, `{"frame":`)
	out = append(out, lines[mid:]...)
	if err := os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- 守門 6：forced shutdown 不隨歷史 generation 數量線性增加 ----

// TestShutdownDoesNotExpandPendingHistory：契約第 6 條。
//
// **不用牆鐘門檻**（這個 repo 已有三條牆鐘 flake，owner 指定改以狀態／同步訊號證明）：
// 證明的方式是「shutdown 期間**一次磁碟載入都沒有**，而佇列裡還躺著 N 筆待辦」。
// 既然歷史展開是唯一與 generation 數量成正比的工作，零次載入即等於 O(1)。
//
// mutation：drainWireFrameJobs 改成把剩下的佇列就地做完 → shutdown 期間的載入次數
// 變成 N → 紅（barrier 只卡第一次，後續放行，所以是乾淨的紅而不是卡死）。
func TestShutdownDoesNotExpandPendingHistory(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-many", "")
	for i := 0; i < 3; i++ { // 三次 restart → 之後三次收尾各留一筆待辦
		if err := a.RestartCodexServerRecorded("b1-many"); err != nil {
			t.Fatal(err)
		}
		runFramingSession(t, a, ctl, w, "t-many", "t-many")
	}
	<-bar.entered // 第一筆待辦真的卡在重建點上，其餘排在後面

	waitFor(t, "佇列裡累積了多筆待辦", func() bool { return len(pendingJobViewIDs(t, a)) >= 3 })
	before := bar.count()

	a.shutdown(a.ctx) // 走 production 總序（含 bounded drain）

	if after := bar.count(); after != before {
		t.Fatalf("shutdown 期間不得展開任何歷史 generation（O(1)）：載入次數 %d → %d", before, after)
	}
	left := pendingJobViewIDs(t, a)
	if len(left) < 2 {
		t.Fatalf("沒做完的待辦必須留在 job journal 供下次啟動補完：%v", left)
	}
	bar.releaseAll()
}
