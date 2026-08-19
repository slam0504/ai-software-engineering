package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/journal"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

// ---- §3.4.3 frame-level 歸屬的**非阻塞結算**（owner 2026-08-18 裁決）----
//
// 問題：session 收尾時要回答「這個 WSID 的每一份 generation 裡，哪幾筆 frame 是它
// 的」。本次這一代的答案在記憶體裡（live FrameIndex），但**先前**那幾代不在——而
// `SegmentSet` 是 append-only、永不裁剪的 journal，一個 WSID 每參與一次 app run 就多
// 一段，所以「先前那幾代」會隨使用時間無界成長。原本的實作把這段展開放在
// `closeWireSegment` 裡同步做，等於把一段無界 I/O 放進 `EndSession`（Wails RPC，UI 會
// 等）與 `forcedShutdown`（每個 session 各跑一次）。
//
// **owner 否決的兩個替代方案**：「只結算本次 app run」會破壞 §3.4.4 的跨 generation
// 完整 view；「只加量測」讓已知的無界 I/O 繼續阻塞收尾，不適合進 merge gate。
//
// 凍結的契約（六條）：
//
//  1. `SegmentSet.Append` 與**本次 range** 的結算仍同步完成——持久化邊界不變。
//  2. **歷史** frame 的展開移到單一背景 worker，不在 RPC 路徑上逐檔掃描。
//  3. per-generation cache：同一個 wire_log_id 在一個 app run 內最多重建一次，
//     多個 session 共用同一份結果。
//  4. 新 generation finalize 時直接產生 compact sidecar（`<id>.frames.json`）；
//     舊資料沒有 sidecar 才 fallback 重讀錄流，**完成後補建**。
//  5. 先同步留下 `frames_status=pending` 的稽核（含 view id／WSID／segments／label），
//     再由背景工作寫入 resolved／failed。**不是 fire-and-forget**——「證據還沒算完」
//     必須是可觀測狀態，否則把同步問題改成非同步之後，失敗會變得無跡可循。
//  6. shutdown 採 bounded drain；沒做完的工作留在 job journal，**下次啟動補完**。
//
// 為什麼 job 需要自己的 journal 而不是掃 audit：audit sink 可以是關閉的
// （`a.auditF == nil` 時 `a.audit` 是 no-op），拿一個可選的觀測輸出當工作佇列，
// 等於讓「關掉稽核」變成「靜默丟掉待辦」。

// wireFrameJob 是一筆待展開的歷史歸屬工作，也是 job journal 的 record 形狀。
// Done 記錄是同一個 view id 的第二筆 append（只帶 view_id ＋ done），replay 時把它
// 從待辦集合裡移除——append-only journal 的標準做法，與 internal/evidence 同形。
type wireFrameJob struct {
	ViewID string `json:"view_id"`
	Done   bool   `json:"done,omitempty"`

	WSID string `json:"wsid,omitempty"`
	// LiveGenID／LiveFrames：本次那一代在**收尾當下**的結算快照。
	//
	// **record 刻意不內嵌 segments**（review 指出的 O(N²)）：`SegmentSet` 永不裁剪，
	// 第 k 次收尾時 `For(wsid)` 已有 k 段，把它寫進 record 會讓 job journal 的總體
	// 大小變成 O(N²) bytes，而 openWireFrameJobs 是在 startup、UI 之前把整份讀進
	// 記憶體——等於把本票要消除的「無界 I/O 在使用者等待路徑上」搬到另一端。
	// segments 改由 worker resolve 當下向 SegmentSet 取（它本來就是 durable 權威）。
	//
	// LiveFrames **保留**（與 review 的字面裁決有出入，理由如下，請覆核）：它的大小
	// 是「本 session 自己的 frame 數」，不隨歷史成長，所以不構成 O(N²)。而它不能改成
	// resolve 時重算——那一代此刻可能還在被寫，同一個 WSID 若在同一代又起了新
	// session，重算會把**下一個 view 的 frame** 算進這一個 view，而且會被 cache 固化。
	// 收尾當下的快照才是這個 view 的正確答案。
	LiveGenID  string `json:"live_wire_log_id,omitempty"`
	LiveFrames []int  `json:"live_frames,omitempty"`

	// SegCount：收尾當下 `SegmentSet.For(wsid)` 的段數。
	//
	// **不存 segments、但必須存段數**（review 抓到的回歸）：resolve 是**延後**發生的，
	// 而 worker 可能被前一筆工作佔住（重建一份大 wire log ——正是 sidecar 存在的理由）。
	// 這段期間同一個 WSID 若又跨了一代收尾，resolve 當下的 `For(wsid)` 已經多出後來
	// 那幾段，於是：(a) 這個 view 的 frames 會含**收尾當下根本不存在**的 generation，
	// 稽核者拿 pendingWireLogs 去 join 會對不起來；(b) 那一代**還活著、還沒 finalize**，
	// 卻被寫出一份 sidecar——不正常退出時就永久固化成一份缺後半段的權威快路徑，而且
	// TruncatedTailBytes=0，連截斷出口都不會響。
	//
	// 一個 int 就精確還原了快照語意，且不改 frames 的語意（仍是「這一代裡我的全部
	// frame」）。順帶讓「SegmentSet 尾端截斷導致某代靜默消失」變成可偵測：實際段數
	// 少於快照即 fail loud，而 job record 本身不再自足（正確性依賴 wire-segments.jsonl
	// 在 resolve 當下的狀態，而該檔的尾端截斷是被容忍的）。
	SegCount int `json:"seg_count,omitempty"`
}

// wireGenAttr 是 per-generation cache 的一格：成功的歸屬，或**該代讀不出來的原因**。
// 錯誤一樣要快取——否則同一份壞掉的 wire log 會被每個 session 各重讀一次，而
// 契約第 3 條要的是「一個 app run 最多一次」，不是「成功時最多一次」。
type wireGenAttr struct {
	byWSID    map[string][]int
	err       error
	truncated int64
}

func (a *App) wireFrameJobsPath() string { return filepath.Join(a.stateDir, "wire-frame-jobs.jsonl") }

// openWireFrameJobs：開 job journal 並把上次沒做完的工作重新排進佇列（契約第 6 條的
// 「下次啟動補完」）。由 openWireSegments 在 startup 呼叫。
//
// 開檔失敗不阻擋啟動但 fail loud（比照 openWireSegments）：錄流本體與 wsid 欄位都不
// 受影響，缺的是「歷史展開的待辦紀錄」——本次 app run 的收尾仍會即時結算本代，只是
// 崩潰後補不回來。
func (a *App) openWireFrameJobs() {
	j, err := journal.Open(a.wireFrameJobsPath())
	if err != nil {
		a.audit("wire_frame_jobs_open_error", map[string]any{"error": err.Error()})
		a.noteStartupWarning("codex 錄流 frame 歸屬待辦紀錄開啟失敗（歷史歸屬展開停用，錄流與本代歸屬不受影響）：" + err.Error())
		return
	}
	pending := map[string]wireFrameJob{}
	var order []string
	for _, ln := range j.Lines() {
		var rec wireFrameJob
		if uerr := json.Unmarshal(ln, &rec); uerr != nil || rec.ViewID == "" {
			a.audit("wire_frame_jobs_bad_record", map[string]any{"line": string(ln)})
			continue
		}
		if rec.Done {
			delete(pending, rec.ViewID)
			continue
		}
		if _, seen := pending[rec.ViewID]; !seen {
			order = append(order, rec.ViewID)
		}
		pending[rec.ViewID] = rec
	}

	a.wireJobMu.Lock()
	a.wireJobJ = j
	for _, id := range order {
		if job, ok := pending[id]; ok {
			a.wireJobQueue = append(a.wireJobQueue, job)
		}
	}
	n := len(a.wireJobQueue)
	a.wireJobMu.Unlock()

	if n > 0 {
		a.audit("wire_frame_jobs_recovered", map[string]any{"count": n})
		a.ensureWireFrameWorker()
		a.signalWireFrameWorker()
	}
}

// enqueueWireFrameJob：把工作**先落盤再排隊**。順序不可顛倒——先排隊後落盤的話，
// 「已經開始做但沒記下來」的窗口內崩潰就永遠補不回來了。
//
// **已知 crash 窗口（review 指出，設計取捨）**：pending 稽核與這裡的 journal append
// 是兩筆記錄，不可能原子落盤。崩潰若正好落在「pending 稽核已寫、journal 的 fsync 尚未
// 完成」之間，稽核會永遠停在 pending 而磁碟上沒有任何待辦——契約 5（有 pending 痕跡）
// 仍成立，契約 6（下次啟動補完）在這個窗口內不成立。窗口涵蓋整段 fsync，不是奈秒級。
//
// 沒有反過來（先 journal 再 audit）的理由：那會換成「有待辦、沒有 pending 痕跡」，
// 稽核者看到的是一筆憑空出現的 resolved。兩者取其一，選擇讓**證據**先落地。
func (a *App) enqueueWireFrameJob(job wireFrameJob) {
	line, err := json.Marshal(job)
	if err == nil {
		err = a.appendWireFrameJobLine(line)
	}
	if err != nil {
		// 落盤失敗：這筆工作跨不過重啟。仍然排隊（本次 app run 還是會算出來），
		// 但必須吵——否則就是靜默降級成 fire-and-forget。
		a.audit("wire_frame_job_persist_error", map[string]any{
			"viewId": job.ViewID, "wsid": job.WSID, "error": err.Error()})
	}
	a.wireJobMu.Lock()
	a.wireJobQueue = append(a.wireJobQueue, job)
	a.wireJobMu.Unlock()
	a.ensureWireFrameWorker()
	a.signalWireFrameWorker()
}

func (a *App) appendWireFrameJobLine(line []byte) error {
	a.wireJobMu.Lock()
	j := a.wireJobJ
	a.wireJobMu.Unlock()
	if j == nil {
		return errors.New("wire frame job journal unavailable")
	}
	return j.Append(line)
}

// markWireFrameJobDone：view 的結果已寫進稽核，把待辦劃掉。
func (a *App) markWireFrameJobDone(viewID string) {
	line, err := json.Marshal(wireFrameJob{ViewID: viewID, Done: true})
	if err == nil {
		err = a.appendWireFrameJobLine(line)
	}
	if err != nil {
		// 劃不掉：下次啟動會重做一次同一個 view。重做是冪等的（再寫一筆相同的
		// resolved 稽核），比「以為做完其實沒有」安全，但一樣要留痕。
		a.audit("wire_frame_job_done_error", map[string]any{"viewId": viewID, "error": err.Error()})
	}
}

// ensureWireFrameWorker：懶啟動**單一**背景 worker（契約第 2 條）。單一是刻意的：
// 它同時就是 per-generation cache 的序列化點——同一個 generation 不可能被兩個
// goroutine 同時重建，契約第 3 條因此不需要額外的 in-flight 去重機制。
func (a *App) ensureWireFrameWorker() {
	a.wireJobMu.Lock()
	defer a.wireJobMu.Unlock()
	if a.wireJobSig != nil {
		return
	}
	a.wireJobSig = make(chan struct{}, 1)
	a.wireJobStop = make(chan struct{})
	a.wireJobExit = make(chan struct{})
	sig, stop, exit := a.wireJobSig, a.wireJobStop, a.wireJobExit
	go a.wireFrameWorker(sig, stop, exit)
}

func (a *App) signalWireFrameWorker() {
	a.wireJobMu.Lock()
	sig := a.wireJobSig
	a.wireJobMu.Unlock()
	if sig == nil {
		return
	}
	select {
	case sig <- struct{}{}:
	default: // 已有未取用的訊號：worker 醒來時會把整個佇列吃乾，不需要一對一
	}
}

// wireFrameWorker：唯一的背景 worker。exit 在它真正退出時關閉——收尾之後
// 「goroutine 有沒有真的走掉」才有可斷言的訊號（見
// TestShutdownDoesNotExpandPendingHistory 末段）。
func (a *App) wireFrameWorker(sig <-chan struct{}, stop <-chan struct{}, exit chan<- struct{}) {
	defer close(exit)
	for {
		a.wireJobMu.Lock()
		var job wireFrameJob
		got := len(a.wireJobQueue) > 0
		if got {
			job = a.wireJobQueue[0]
			a.wireJobQueue = a.wireJobQueue[1:]
			a.wireJobBusy = true
		}
		a.wireJobMu.Unlock()

		if !got {
			select {
			case <-sig:
				continue
			case <-stop:
				return
			}
		}
		a.resolveWireFrameJob(job)
		a.wireJobMu.Lock()
		a.wireJobBusy = false
		a.wireJobMu.Unlock()

		select { // 每做完一筆就檢查一次收工訊號（bounded drain 的粒度）
		case <-stop:
			return
		default:
		}
	}
}

// resolveWireFrameJob：把一個 view 的歷史 generation 展開，寫出 resolved／failed 稽核。
//
// **corrupt 的 generation 必須寫成 failed，不得偽裝成零 frame**（契約守門第 5 條）：
// 「這一代讀不出來」與「這一代沒有我的 frame」在證據上是完全不同的兩件事，後者會讓
// 稽核者以為證據完整。
func (a *App) resolveWireFrameJob(job wireFrameJob) {
	frames := map[string][]int{}
	if job.LiveGenID != "" {
		frames[job.LiveGenID] = job.LiveFrames // 本次那一代已在同步階段結算完
	}
	failures := map[string]string{}
	// 還原收尾當下的快照（見 wireFrameJob.SegCount）。少於快照代表 SegmentSet 掉了段
	// （尾端截斷被 journal 容忍、或開檔失敗回 nil）——**不得靜默算成「歷史只有這一代」**，
	// 那與守門 5 拒絕的「讀不出來偽裝成零 frame」是同一個形狀，只是換了入口。
	segs := a.wireSegmentsFor(job.WSID) // durable 權威；record 不內嵌（見 wireFrameJob）
	if len(segs) < job.SegCount {
		a.audit("codex_wire_segment_frames", map[string]any{
			"viewId": job.ViewID, "wsid": job.WSID, "frames": frames,
			"framesStatus": "failed",
			"segmentsError": fmt.Sprintf(
				"SegmentSet 目前只有 %d 段，收尾當下是 %d 段——歷史錄流證據缺段，無法還原這個 view",
				len(segs), job.SegCount)})
		a.markWireFrameJobDone(job.ViewID)
		return
	}
	segs = segs[:job.SegCount]
	for _, s := range segs {
		if _, done := frames[s.WireLogID]; done {
			continue
		}
		if _, done := failures[s.WireLogID]; done {
			continue
		}
		byWSID, err := a.wireGenAttribution(s.WireLogID)
		if err != nil {
			failures[s.WireLogID] = err.Error()
			continue
		}
		frames[s.WireLogID] = byWSID[job.WSID]
	}

	status := "resolved"
	rec := map[string]any{"viewId": job.ViewID, "wsid": job.WSID, "frames": frames}
	if len(failures) > 0 {
		status = "failed"
		rec["errors"] = failures
	}
	rec["framesStatus"] = status
	a.audit("codex_wire_segment_frames", rec)
	a.markWireFrameJobDone(job.ViewID)
}

// wireGenAttribution：某一代的歸屬，**per-app-run 只碰一次磁碟**（契約第 3 條）。
// 只由 worker goroutine 呼叫，因此 cache 不需要 in-flight 去重。
func (a *App) wireGenAttribution(wireLogID string) (map[string][]int, error) {
	a.wireIdxMu.Lock()
	if got, ok := a.wireIdxCache[wireLogID]; ok {
		a.wireIdxMu.Unlock()
		// cache 命中也要再記一次截斷：這是**這個 view 的答案缺了一段**，不是
		// 「這一代被讀過一次」的事實。只在第一次記的話，同一個 app run 內後續每一個
		// view 都會靜默（review 指出）。
		a.auditTruncatedAttribution(wireLogID, got.truncated, true)
		return got.byWSID, got.err
	}
	a.wireIdxMu.Unlock()

	if h := a.hookWireIndexLoad; h != nil {
		h(wireLogID) // 測試 barrier／計數點：這裡是唯一真的碰磁碟的地方
	}
	res, err := wirelog.LoadOrBuildAttribution(a.wireLogDir(), wireLogID)
	if err != nil && res.ByWSID == nil {
		err = fmt.Errorf("wire log %s 的 frame 歸屬讀不出來: %w", wireLogID, err)
	} else if err != nil {
		// 歸屬拿到了、只是 sidecar 補建失敗：不是證據缺口，記一筆就好。
		a.audit("wire_frame_sidecar_error", map[string]any{"wireLogId": wireLogID, "error": err.Error()})
		err = nil
	}

	a.wireIdxMu.Lock()
	if a.wireIdxCache == nil {
		a.wireIdxCache = map[string]wireGenAttr{}
	}
	a.wireIdxCache[wireLogID] = wireGenAttr{byWSID: res.ByWSID, err: err, truncated: res.TruncatedTailBytes}
	a.wireIdxMu.Unlock()
	if err == nil && !res.FromSidecar {
		a.audit("wire_frame_index_rebuilt", map[string]any{"wireLogId": wireLogID})
	}
	a.auditTruncatedAttribution(wireLogID, res.TruncatedTailBytes, res.FromSidecar)
	return res.ByWSID, err
}

// auditTruncatedAttribution：截斷的出口（review 指出）。由不完整的 wire log 建出的
// 歸屬會經 sidecar 成為後續每一次 app run 的快路徑；不讓它有出口就是把證據缺口靜默
// 固化。**每一次取得都記**——不只重建那一次，也不只 cache miss 那一次：截斷描述的是
// 「這個 view 的答案缺了一段」，每個 view 都該看得到。
func (a *App) auditTruncatedAttribution(wireLogID string, n int64, fromSidecar bool) {
	if n <= 0 {
		return
	}
	a.audit("wire_frame_index_truncated", map[string]any{"wireLogId": wireLogID,
		"truncatedTailBytes": n, "fromSidecar": fromSidecar,
		"note": "此代錄流檔尾不完整，歸屬答案缺一段（§3.4.5 檔尾截斷容忍）"})
}

// wireSegmentsFor：SegmentSet 的取值包裝（SegmentSet 開檔失敗時為 nil）。
func (a *App) wireSegmentsFor(wsid string) []wirelog.SegmentRef {
	if a.wireSegments == nil {
		return nil
	}
	return a.wireSegments.For(wsid)
}

// wireFrameJobsIdle：佇列空且沒有工作在跑。
func (a *App) wireFrameJobsIdle() bool {
	a.wireJobMu.Lock()
	defer a.wireJobMu.Unlock()
	return len(a.wireJobQueue) == 0 && !a.wireJobBusy
}

// wireFrameDrainWindow：shutdown 願意等背景工作收斂多久（契約第 6 條的 bounded）。
// 測試設 0 即「不等」，收尾時間因此與待辦數量無關且不依賴牆鐘。
func (a *App) wireFrameDrainWindow() time.Duration {
	if a.wireJobDrain != nil {
		return *a.wireJobDrain
	}
	return 2 * time.Second
}

// drainWireFrameJobs：shutdown 的 bounded drain。**回傳 worker 是否確實已經退出。**
//
// 收尾時間仍然 bounded（不把它綁回歷史資料量），但「等不到」不再是可以忽略的
// 事：`resolveWireFrameJob` 在執行途中不檢查 stop，它會寫 `audit.jsonl` 也會
// `WriteAttribution`（截斷式覆寫 sidecar）。若在它還活著時釋放 ownership lease，
// 第二個實例已經可以進場，而我們的殘留 goroutine 仍在寫同一份 state——單一
// writer 前提就破了（owner 2026-08-19 判定為 merge blocker）。
//
// 所以這裡把結果往上傳：
//
//	true  → 沒有 worker，或 worker 已在窗口內退出。呼叫端可以關 writer、正常釋放 lease。
//	false → worker 還活著。呼叫端**不得手動釋放 lease**，讓 process 在仍持鎖的
//	        狀態下結束，由 OS 同時回收 kernel lock 與 fd（contract §5）。
//
// 沒做完的工作仍在 journal 裡（done 那一筆還沒 append），下次啟動由
// openWireFrameJobs 補完。逾時或有殘留一律留稽核：被跳過的工作不能無聲。
//
// **已知事項（review 登記，未處置）**：
//   - 這裡用 `time.Sleep(10ms)` 輪詢等 idle。它在 production 收尾路徑上（不受測試的
//     barrier 約束），是本檔唯一的牆鐘相依；改成條件變數要多一組狀態，尚未評估值不值得。
//   - 收工之後 `wireJobSig` **不重置為 nil**，所以之後若還有 enqueue，工作會被排進一個
//     已經退出的 worker 的佇列（靜默不做，但仍會落盤，下次啟動補完）。production 走不到
//     ——`shuttingDown` 已擋掉所有會建立 session 的交易——故只登記不改。
func (a *App) drainWireFrameJobs() bool {
	a.wireJobMu.Lock()
	stop, exit, running := a.wireJobStop, a.wireJobExit, a.wireJobSig != nil
	a.wireJobMu.Unlock()
	if !running {
		return true // 沒有 worker＝沒有殘留 writer
	}
	window := a.wireFrameDrainWindow()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) && !a.wireFrameJobsIdle() {
		time.Sleep(10 * time.Millisecond)
	}
	a.wireJobMu.Lock()
	left := len(a.wireJobQueue)
	busy := a.wireJobBusy
	if a.wireJobStop != nil {
		close(stop)
		a.wireJobStop = nil
	}
	a.wireJobMu.Unlock()
	if left > 0 || busy {
		a.audit("wire_frame_jobs_deferred", map[string]any{
			"queued": left, "inFlight": busy,
			"note": "未完成的 frame 歸屬展開留在 wire-frame-jobs.jsonl，下次啟動補完"})
	}
	// stop 已關，idle 的 worker 會立刻退出；正卡在一筆 job 裡的則要做完那一筆
	// （resolveWireFrameJob 不可中斷）。等它——但一樣 bounded。等不到就回 false，
	// 由呼叫端保住 lease，不是靠「它應該很快就好了」放行。
	select {
	case <-exit:
		return true
	case <-time.After(window):
		a.audit("wire_frame_worker_still_running", map[string]any{
			"note": "背景 frame 展開 worker 未在收尾窗口內退出；保留 ownership lease，" +
				"由 process 結束時交還作業系統"})
		return false
	}
}

// closeWireFrameJobs：關 job journal handle（drain 之後）。
func (a *App) closeWireFrameJobs() {
	a.wireJobMu.Lock()
	j := a.wireJobJ
	a.wireJobJ = nil
	a.wireJobMu.Unlock()
	if j == nil {
		return
	}
	if err := j.Close(); err != nil {
		a.audit("wire_frame_jobs_close_error", map[string]any{"error": err.Error()})
	}
}
