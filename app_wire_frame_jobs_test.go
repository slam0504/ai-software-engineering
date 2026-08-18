package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	ViewID        string            `json:"viewId"`
	WSID          string            `json:"wsid"`
	FramesStatus  string            `json:"framesStatus"`
	Frames        map[string][]int  `json:"frames"`
	Errors        map[string]string `json:"errors"`
	SegmentsError string            `json:"segmentsError"`
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

	// 收工訊號確實有效：放行 barrier 之後 worker 必須真的退出，不留背景 goroutine。
	// shutdown 刻意不等它（等它＝把收尾時間綁回歷史資料量），所以「退出」這件事只能
	// 在這裡事後驗。
	bar.releaseAll()
	exit := a.wireJobExit
	if exit == nil {
		t.Fatal("worker 的退出訊號必須存在，否則沒人驗得到它有沒有真的走掉")
	}
	waitFor(t, "背景 worker 收到收工訊號後退出", func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	})
}

// ---- 守門 7：sidecar 讓已收尾的 generation 不必重讀錄流（契約第 4 條的**讀取**那一半）----

// auditKinds：某個 kind 的所有記錄（data 原文）。
func auditRecordsOfKind(t *testing.T, a *App, kind string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Kind != kind {
			continue
		}
		out = append(out, rec.Data)
	}
	return out
}

// rebuildCount：audit.jsonl 裡「重讀整份錄流」發生在某個 generation 上的次數。
//
// **一定要用增量比對**：跨重啟的測試裡好幾個 App 共寫同一份 audit.jsonl，用「有沒有
// 出現過」判斷會把前一個 App 的紀錄算進來（第一版就是這樣假紅的）。
func rebuildCount(t *testing.T, a *App, wireLogID string) int {
	t.Helper()
	n := 0
	for _, d := range auditRecordsOfKind(t, a, "wire_frame_index_rebuilt") {
		if id, _ := d["wireLogId"].(string); id == wireLogID {
			n++
		}
	}
	return n
}

// TestFinalizedGenerationIsReadFromSidecarNotRebuilt：契約第 4 條。
//
// 「finalize 有沒有寫檔」只是這條契約的一半——**讀取**與**補建**兩邊原本都沒有守門
// （review 實測：把讀 sidecar 與補建 sidecar 整段刪掉，六條守門一條都不紅）。
//
// 這裡守讀取：gen1 在 B1 restart 時被 finalize（sidecar 就此產生），app 重啟之後
// 展開 gen1 必須走 sidecar——出現 wire_frame_index_rebuilt 就代表又把整份錄流讀了一遍。
//
// mutation：LoadOrBuildAttribution 永遠不讀 sidecar（每次都 RebuildFrameIndex）→ 紅。
func TestFinalizedGenerationIsReadFromSidecarNotRebuilt(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-side", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-side"); err != nil { // gen1 finalize → 產生 sidecar
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.wireLogDir(), gen1+".frames.json")); err != nil {
		t.Fatalf("precondition：finalize 必須直接產生 sidecar：%v", err)
	}
	a.shutdown(a.ctx)

	// 重啟：記憶體 cache 全空，gen1 只能從磁碟取——但必須取 sidecar，不是重讀錄流。
	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl2 := &framingWire{}
	bootFramingApp(t, b, ctl2)
	noDrain(b)
	before := rebuildCount(t, b, gen1)
	runFramingSession(t, b, ctl2, w, "t-side", "t-side")
	view := auditSegments(t, b, w)
	res := waitForFrameResult(t, b, view.ViewID)
	if res.FramesStatus != "resolved" || len(res.Frames[gen1]) == 0 {
		t.Fatalf("precondition：gen1 必須真的被展開過：%+v", res)
	}
	if after := rebuildCount(t, b, gen1); after != before {
		t.Fatalf("已 finalize 的 generation %s 必須走 sidecar 快路徑，不得重讀整份錄流（%d → %d）",
			gen1, before, after)
	}
}

// TestRebuiltGenerationIsBackfilledForNextRun：契約第 4 條的**補建**那一半。
//
// 沒有 sidecar 的舊資料 fallback 重讀錄流之後必須補建，否則每一次 app run 都要
// 再讀一遍——「一個 app run 最多一次」擋不住跨 run 的重複。
//
// mutation：LoadOrBuildAttribution 重建後不寫 sidecar → 第二次重啟仍出現
// wire_frame_index_rebuilt → 紅。
func TestRebuiltGenerationIsBackfilledForNextRun(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-back", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-back"); err != nil {
		t.Fatal(err)
	}
	// 模擬「沒有 sidecar 的舊資料」。
	if err := os.Remove(filepath.Join(a.wireLogDir(), gen1+".frames.json")); err != nil {
		t.Fatal(err)
	}
	a.shutdown(a.ctx)

	b, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl2 := &framingWire{}
	bootFramingApp(t, b, ctl2)
	noDrain(b)
	beforeB := rebuildCount(t, b, gen1)
	runFramingSession(t, b, ctl2, w, "t-back", "t-back")
	waitForFrameResult(t, b, auditSegments(t, b, w).ViewID)
	if rebuildCount(t, b, gen1) <= beforeB {
		t.Fatal("precondition：沒有 sidecar 就必須重讀錄流，否則這條測不到補建")
	}
	if _, err := os.Stat(filepath.Join(b.wireLogDir(), gen1+".frames.json")); err != nil {
		t.Fatalf("重建之後必須補建 sidecar：%v", err)
	}
	b.shutdown(b.ctx)

	// 第三次執行：補建過了，不得再重讀。
	c, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl3 := &framingWire{}
	bootFramingApp(t, c, ctl3)
	noDrain(c)
	beforeC := rebuildCount(t, c, gen1)
	runFramingSession(t, c, ctl3, w, "t-back", "t-back")
	waitForFrameResult(t, c, auditSegments(t, c, w).ViewID)
	if after := rebuildCount(t, c, gen1); after != beforeC {
		t.Fatalf("補建過的 generation %s 不得再重讀整份錄流（%d → %d）", gen1, beforeC, after)
	}
}

// ---- 守門 8：pending 稽核必須**同步**寫（契約第 5 條的「先同步」）----

// TestPendingAuditIsWrittenSynchronously：契約第 5 條字面上的「**先同步**留下 pending
// 稽核」。
//
// **為什麼不能只斷言「EndSession 返回後檔案裡有那筆記錄」**（review 實測：把
// `a.audit(codex_wire_segments, rec)` 改成 `go a.audit(...)`，七條守門全綠、-count=5
// 仍全綠）：G1 之所以看得到 pending 而看不到 resolved，是因為 barrier 卡住 worker，
// 與 audit 是不是同步的無關；而「返回後檔案裡有」在非同步版底下只是輸掉一次排程競賽，
// 是機率不是保證。
//
// 這裡改成直接證明「**這筆稽核跑在 closeWireSegment 的 goroutine 上**」：hookAudit 在
// 呼叫端的 goroutine 上同步執行，抓它自己的 stack。換成 go a.audit 之後 stack 屬於
// 新的 goroutine，裡面不會有 closeWireSegment——確定性的紅，不是競賽。
// callerFramesOnly：只留 goroutine 自己的呼叫框，砍掉尾端的 "created by …"。
//
// **這一行是必須的**：`go a.audit(...)` 產生的 goroutine，其 stack trace 尾端會有
// `created by main.(*App).closeWireSegment.func1`——不砍掉的話，非同步版一樣「含有
// closeWireSegment」，斷言就變成永遠成立的假守門（第一版實測 mutation 全綠）。
func callerFramesOnly(stack string) string {
	if i := strings.Index(stack, "\ncreated by "); i >= 0 {
		return stack[:i]
	}
	return stack
}

func TestPendingAuditIsWrittenSynchronously(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	noDrain(a)

	var mu sync.Mutex
	var stacks []string
	a.hookAudit = func(kind string) {
		if kind != "codex_wire_segments" {
			return
		}
		buf := make([]byte, 16<<10)
		n := runtime.Stack(buf, false)
		mu.Lock()
		stacks = append(stacks, callerFramesOnly(string(buf[:n])))
		mu.Unlock()
	}

	// **兩個分支都要走到**：沒有歷史時走 framesStatus=resolved、有歷史時走 pending。
	// 只跑第一種的話，改動 pending 分支的 mutation 會全綠（第一版就是這個坑）。
	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-sync", "") // resolved 分支
	if err := a.RestartCodexServerRecorded("b1-sync"); err != nil {
		t.Fatal(err)
	}
	runFramingSession(t, a, ctl, w, "t-sync", "t-sync") // pending 分支
	if v := auditSegments(t, a, w); v.FramesStatus != "pending" {
		t.Fatalf("precondition：第二次收尾必須走 pending 分支：%+v", v)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(stacks) != 2 {
		t.Fatalf("precondition：兩次收尾各寫一筆 codex_wire_segments，實際 %d 筆", len(stacks))
	}
	for i, st := range stacks {
		if !strings.Contains(st, "closeWireSegment") {
			t.Fatalf("第 %d 筆 codex_wire_segments 必須在 closeWireSegment 的 goroutine 上同步寫出，實際 stack：\n%s",
				i+1, st)
		}
	}
}

// TestTruncatedGenerationIsAudited：截斷必須有出口（review 指出）。
//
// 由**檔尾不完整**的 wire log 建出來的歸屬是缺一段的答案，而 sidecar 會把它固化成
// 後續每一次 app run 的快路徑。`framesStatus` 仍是 resolved（§3.4.5 對檔尾截斷是
// 容忍的，不是失敗），所以若不另外留一筆稽核，「證據缺了一段」就完全沉默。
//
// mutation：wireGenAttribution 不發 wire_frame_index_truncated → 紅。
func TestTruncatedGenerationIsAudited(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-trunc", "")
	gen1 := currentWireLogID(a)
	if err := a.RestartCodexServerRecorded("b1-trunc"); err != nil {
		t.Fatal(err)
	}
	// sidecar 拿掉並在檔尾補一行寫到一半的 frame（app-server 意外死亡最典型的形狀）。
	if err := os.Remove(filepath.Join(a.wireLogDir(), gen1+".frames.json")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.wireLogDir(), gen1+".jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(b, []byte(`{"frame":99,"dir":"s2c"`)...), 0o644); err != nil {
		t.Fatal(err)
	}

	runFramingSession(t, a, ctl, w, "t-trunc", "t-trunc")
	view := auditSegments(t, a, w)
	res := waitForFrameResult(t, a, view.ViewID)
	if res.FramesStatus != "resolved" {
		t.Fatalf("precondition：檔尾截斷是容忍的，不是 failed（§3.4.5）：%+v", res)
	}
	var truncated []string
	for _, d := range auditRecordsOfKind(t, a, "wire_frame_index_truncated") {
		if id, _ := d["wireLogId"].(string); id == gen1 {
			if n, _ := d["truncatedTailBytes"].(float64); n <= 0 {
				t.Fatalf("截斷量必須寫進稽核：%+v", d)
			}
			truncated = append(truncated, id)
		}
	}
	if len(truncated) == 0 {
		t.Fatalf("由截斷的錄流建出的歸屬是缺一段的答案，必須有稽核出口（狀態仍是 resolved）")
	}

	// **第二次執行走的是 sidecar 快路徑**，答案一樣缺一段——截斷必須照樣有出口，
	// 否則「重建那一次吵過了」就變成之後永遠沉默（review 指出的 Minor）。
	beforeRestart := len(truncated)
	a.shutdown(a.ctx)
	b2, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	ctl2 := &framingWire{}
	bootFramingApp(t, b2, ctl2)
	noDrain(b2)
	runFramingSession(t, b2, ctl2, w, "t-trunc", "t-trunc")
	waitForFrameResult(t, b2, auditSegments(t, b2, w).ViewID)
	sidecarTruncations := 0
	for _, d := range auditRecordsOfKind(t, b2, "wire_frame_index_truncated") {
		if id, _ := d["wireLogId"].(string); id != gen1 {
			continue
		}
		sidecarTruncations++
		if from, _ := d["fromSidecar"].(bool); from {
			return // 走 sidecar 而且照樣吵了——這條要的就是這個
		}
	}
	t.Fatalf("經 sidecar 取得的截斷答案也必須留稽核（重建前 %d 筆、目前 %d 筆，皆非 sidecar）",
		beforeRestart, sidecarTruncations)
}

// ---- 守門 9：延後 resolve 期間新增的 generation 不得混進更早的 view ----

// TestDelayedResolveDoesNotLeakNewerGenerations：Critical 回歸的守門。
//
// resolve 是**延後**發生的，而 worker 會被前一筆工作佔住（重建一份大 wire log——正是
// sidecar 存在的理由）。這段期間同一個 WSID 若又跨了幾代收尾，resolve 當下的
// `SegmentSet.For(wsid)` 已經多出後來那幾段。兩層後果：
//
//  1. **證據污染**：這個 view 的 frames 會含收尾當下根本不存在的 generation，稽核者拿
//     同一筆的 pendingWireLogs 去 join 會對不起來（不需要崩潰就會發生）；
//  2. **為還活著的 generation 寫 sidecar**：正常關機時 Finalize 會覆蓋，不正常退出就
//     永久固化一份缺後半段的權威快路徑，而且 TruncatedTailBytes=0——連截斷出口都不會響。
//
// 斷言寫成 view 級的不變量而不是特定 id：**每個 view 的 frames 鍵集合恰好等於
// {LiveGenID} ∪ pendingWireLogs**。那正是稽核者 join 得起來的定義。
//
// mutation：拿掉 SegCount 的快照還原（resolve 當下用完整的 For(wsid)）→ 紅。
func TestDelayedResolveDoesNotLeakNewerGenerations(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-leak", "") // g1：無歷史，同步 resolved，不排工作

	// g2 收尾 → 第一筆待辦（歷史＝g1）。worker 就此卡在重建 g1 上。
	var views []segmentView
	var gens []string
	for i := 0; i < 3; i++ {
		if err := a.RestartCodexServerRecorded("b1-leak"); err != nil {
			t.Fatal(err)
		}
		runFramingSession(t, a, ctl, w, "t-leak", "t-leak")
		views = append(views, auditSegments(t, a, w))
		gens = append(gens, currentWireLogID(a))
	}
	<-bar.entered // 第一筆真的卡在重建點上（不是「還沒開始」就斷言完了）

	liveGen := gens[len(gens)-1]
	if _, err := os.Stat(filepath.Join(a.wireLogDir(), liveGen+".frames.json")); err == nil {
		t.Fatalf("還活著（未 finalize）的 generation %s 不得被寫出 sidecar", liveGen)
	}

	bar.releaseAll()
	for i, v := range views {
		if v.FramesStatus != "pending" {
			t.Fatalf("precondition：第 %d 個 view 必須是 pending：%+v", i+1, v)
		}
		res := waitForFrameResult(t, a, v.ViewID)
		if res.FramesStatus != "resolved" {
			t.Fatalf("第 %d 個 view 必須 resolved：%+v", i+1, res)
		}
		t.Logf("view %d：live=%v pending=%v → resolved frames=%v",
			i+1, keysOf(v.Frames), v.PendingWireLogs, keysOf(res.Frames))
		want := map[string]bool{}
		for _, id := range v.PendingWireLogs {
			want[id] = true
		}
		for id := range v.Frames { // 同步階段已結算的 live 那一代
			want[id] = true
		}
		// 用 Errorf 不用 Fatalf：這個回歸有**兩層後果**（證據污染＋為活著的
		// generation 寫 sidecar），中斷在第一層會讓第二層永遠觀測不到。
		for id := range res.Frames {
			if !want[id] {
				t.Errorf("第 %d 個 view 的結算含了收尾當下不存在的 generation %s——"+
					"稽核者拿 pendingWireLogs（%v）join 不起來：%v",
					i+1, id, v.PendingWireLogs, keysOf(res.Frames))
			}
		}
		if len(res.Frames) != len(want) {
			t.Errorf("第 %d 個 view 的結算必須恰好涵蓋 {live} ∪ pendingWireLogs：%v（want %v）",
				i+1, keysOf(res.Frames), keysOf2(want))
		}
	}

	// 還活著的那一代仍不得有 sidecar（沒有任何 view 該去讀它）。
	if _, err := os.Stat(filepath.Join(a.wireLogDir(), liveGen+".frames.json")); err == nil {
		t.Fatalf("結算完成後，還活著的 generation %s 仍不得有 sidecar", liveGen)
	}
}

func keysOf(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- 守門 10：SegmentSet 掉段時 fail loud，不得靜默算成「歷史只有這一代」----

// TestMissingSegmentsFailLoudNotSilentResolve：job record 改成不內嵌 segments 之後，
// 正確性就依賴 `wire-segments.jsonl` 在 **resolve 當下**的狀態——而那個檔的尾端截斷是
// **被容忍的**（`TestOpenSegmentSetTailTruncationTolerated`）。所以「重啟補完時少一段」
// 現在有路徑，而它的錯誤形狀正是守門 5 拒絕的那一種：把「證據缺了一段」寫成
// 「歷史真的只有這一代」。
//
// mutation：拿掉 `len(segs) < job.SegCount` 的 fail loud → 紅（會靜默 resolved）。
func TestMissingSegmentsFailLoudNotSilentResolve(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &framingWire{}
	bootFramingApp(t, a, ctl)
	bar := newWireLoadBarrier(t, a)
	noDrain(a)

	w := mustCreate(t, a, "codex")
	runFramingSession(t, a, ctl, w, "t-lost", "")
	if err := a.RestartCodexServerRecorded("b1-lost"); err != nil {
		t.Fatal(err)
	}
	runFramingSession(t, a, ctl, w, "t-lost", "t-lost")
	view := auditSegments(t, a, w)
	if view.FramesStatus != "pending" {
		t.Fatalf("precondition：必須是 pending：%+v", view)
	}
	<-bar.entered
	a.drainWireFrameJobs() // 「當掉」：待辦已落盤、done 從未寫入
	a.closeWireFrameJobs()

	// 模擬 wire-segments.jsonl 的尾端截斷（journal 容忍：quarantine ＋ truncate）。
	sp := a.wireSegmentsPath()
	b, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("precondition：至少要有兩段才砍得掉一段：%d", len(lines))
	}
	trimmed := strings.Join(lines[:len(lines)-1], "\n") + "\n" + lines[len(lines)-1][:5]
	if err := os.WriteFile(sp, []byte(trimmed), 0o644); err != nil {
		t.Fatal(err)
	}

	b2, _ := newTestAppIn(t, a.workspaceDir, a.stateDir)
	enableAudit(t, b2)
	res := waitForFrameResult(t, b2, view.ViewID)
	if res.FramesStatus != "failed" {
		t.Fatalf("SegmentSet 掉段必須 fail loud，不得靜默算成「歷史只有這一代」：%+v", res)
	}
	if !strings.Contains(res.SegmentsError, "缺段") {
		t.Fatalf("失敗原因必須指名是段數對不上：%q", res.SegmentsError)
	}
	bar.releaseAll()
}
