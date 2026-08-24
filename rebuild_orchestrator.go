package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/replayindex"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// errNoReplayIndex：這次啟動沒有可用的 replay index（開檔或驗證失敗）。
// 視窗化載入以 index 為唯一來源，缺它一律 fail loud、不退回全掃 events.jsonl
// ——那正是 §3.5 要消滅的行為。
var errNoReplayIndex = errors.New("app: replay index 未啟用（本次啟動已 fail loud，見 startup 警告）")

// errIndexUnverified：index 內容不可信（可能有靜默缺口，見 App.indexUnverified
// 的 doc）。回錯而不是回一份可能少 turn 的視窗——後者使用者分辨不出來。
//
// 兩個成因，復原方式不同：
//   - 啟動期 VerifyOrRebuild 失敗：重啟即可讓啟動期重建再跑一次。
//   - registry 載入失敗（restoreSessions 提前 return，index_verify 從未執行）：
//     單純重啟無效，同一份損毀的 workspace-sessions.json 下次啟動會走同一條
//     early return。要先依 startup 警告處理該檔案。
//
// in-process 沒有安全的復原路徑，兩者都得重啟。
var errIndexUnverified = errors.New("app: replay index 未通過啟動驗證，內容不可信；若 startup 警告指出 registry 損毀，請先依該警告處理 workspace-sessions.json，再重啟")

// indexOrNil：*replayindex.Index → appcore.TurnIndex，nil 指標回 nil 介面。
// 直接把 typed nil 塞進介面欄位會讓 `cfg.Index != nil` 恆為真，接著在第一筆
// 事件上對 nil 指標取 idx.mu 而 panic——這是 Go 的經典陷阱，不是防禦性檢查。
func indexOrNil(idx *replayindex.Index) appcore.TurnIndex {
	if idx == nil {
		return nil
	}
	return idx
}

// ---- degraded 通知 → 重建排程（§3.5.4 → §3.5.7）----

// onIndexDegraded：replayindex.Config.Notify 的接點。
//
// **這個 callback 不得呼叫任何 Manager 入口**（凍結約束，違反即死鎖）：
// Observe 是在 Manager 的序列化 mutex 內被呼叫的，而 Notify 由 Observe 觸發
// ——此刻 Manager.mu 仍在手上，任何 EmitWorkspace／Emit 都會當場自我重入。
// 所以這裡只做三件不碰 Manager 的事：
//
//  1. a.audit：寫 audit.jsonl（自己的 mutex 與檔案 handle，與事件管線無關）；
//  2. a.emit：只走 UI 出口（wails EventsEmit），不回寫 events.jsonl；
//  3. scheduleRebuild：只登記＋起 goroutine 就返回，真正取 emit mutex 的是
//     那條 goroutine，等 Observe 這一輪把鎖放掉之後才會拿到。
//
// 通知本身「每個 degraded generation 只發一次」由 replayindex 那側保證
// （degradedNotified），這裡不需要再去重。
func (a *App) onIndexDegraded(msg string) {
	a.audit("replay_index_degraded", map[string]any{"detail": msg})
	a.emit("workbench:event", contract.Envelope{
		EventID: contract.NewULID(time.Now()),
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Scope:   "workspace",
		Kind:    string(contract.KindStreamError),
		Error:   msg,
	})
	a.scheduleRebuild(msg)
}

// backoff 參數。first attempt 之後每次加倍、封頂 maxRebuildBackoff——重建未收
// 斂代表 audit 正被高頻 append（使用者正在用），重試必須讓路，不得 busy-loop
// （§3.5.7）。
const (
	defaultRebuildBackoffBase = 500 * time.Millisecond
	maxRebuildBackoff         = 30 * time.Second
)

// scheduleRebuild：登記一輪 runtime 重建（§3.5.7）。**single-flight**：已有一
// 輪在跑就直接返回，不排隊、不疊加。
//
// 為什麼排程這一側也必須 single-flight（replayindex.beginRebuildRun 已經會對
// 重入回 ErrRebuildInProgress）：未收斂的處置是 backoff 重試，而 degraded 期
// 間每一筆事件都可能觸發通知。若每則通知都起一條重試鏈，這些鏈會彼此以
// ErrRebuildInProgress 互相打回，然後各自 backoff、各自再試——實際重建進度沒
// 有變快，卻疊出無界的 goroutine 與無界的檔案掃描。
//
// cancelRebuild 之後一律拒絕：Manager.Close 會 flush pending queue，那些事件仍
// 會走 Observe，若此時 index 才第一次寫失敗，通知會在 shutdown 收斂之後又起一
// 條沒有人會等的 goroutine。
//
// 「拒絕」的判準刻意是 rebuildClosed（cancelRebuild 在**同一把 rebuildMu 下**
// 設的），不是 a.shuttingDown：後者要另取 shutMu，讀完放鎖到取得 rebuildMu 之
// 間有窗口——shutdown 若正好在那段時間內把旗標設起來並跑完 cancelRebuild（那
// 一刻 rebuildCancel 還是 nil，直接返回），這條排程仍會起一條沒有人取消、也沒
// 有人等待的 goroutine，跨過 Manager.Close 與 index.Flush 繼續跑
// RuntimeRebuild。同一把鎖之後兩種交錯都安全：schedule 先進 → cancelRebuild 讀
// 得到 done 並等它；cancel 先進 → schedule 直接被拒。
func (a *App) scheduleRebuild(reason string) {
	a.rebuildMu.Lock()
	if a.rebuildClosed || a.rebuildRunning {
		a.rebuildMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.rebuildRunning, a.rebuildCancel, a.rebuildDone = true, cancel, done
	a.rebuildMu.Unlock()

	go a.runRebuildLoop(ctx, reason, done)
}

// rebuildInFlight：目前是否有一輪重建（含 backoff 等待）進行中。
func (a *App) rebuildInFlight() bool {
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	return a.rebuildRunning
}

// cancelRebuild：shutdown 用——關閉排程入口、取消重試迴圈並**等它真的收斂**。
//
// rebuildClosed 與「讀取 cancel／done」在同一個臨界區內完成，這是
// scheduleRebuild 那側不再有 check-then-act 窗口的前提（見其 doc）。
//
// 取消的粒度是「兩次 RuntimeRebuild 之間」：正在進行的那一次會跑完才返回。
// 這是刻意的，也是有界的——RuntimeRebuild 自身受 MaxCatchUpAttempts 與收斂上
// 限約束，而且中途放手會留下「掃到一半、cursor 已前移但 latch 未解」的狀態，
// 下一次啟動反正要重掃，提前中斷買不到東西。真正需要被砍斷的是 backoff 等
// 待（可能長達 maxRebuildBackoff），那由 ctx 立即解除。
func (a *App) cancelRebuild() {
	a.rebuildMu.Lock()
	a.rebuildClosed = true
	cancel, done := a.rebuildCancel, a.rebuildDone
	a.rebuildMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// runRebuildLoop：重建重試迴圈本體。
//
// 三種收束（全部只發生在這裡，呼叫端不需要處理重建錯誤）：
//   - 成功 → 結束（latch 已在 RuntimeRebuild 內解除）。
//   - ErrRebuildNotConverged → backoff 後重試；latch 保留、索引進度保留
//     （rebuildCursor 不歸零，重試不重跑 bulk）。
//   - 其他錯誤（含 ErrNotDegraded／I/O 失敗）→ 不重試，audit fail loud。
//     ErrNotDegraded 代表 index 其實是健康的（例如通知來自 §3.5.6 的中段損壞
//     復原，那條路徑本來就不 latch），沒有東西要重建。
func (a *App) runRebuildLoop(ctx context.Context, reason string, done chan struct{}) {
	defer a.finishRebuild(done)

	for attempt := 1; ; attempt++ {
		a.noteRebuildStart()
		if h := a.hookRebuildEntered; h != nil {
			h()
		}
		err := a.runRebuildOnce(attempt)
		switch {
		case err == nil:
			a.audit("replay_index_rebuilt", map[string]any{"reason": reason, "attempts": attempt})
			return
		case errors.Is(err, replayindex.ErrRebuildNotConverged):
			d := a.noteRebuildBackoff(attempt)
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		default:
			if !errors.Is(err, replayindex.ErrNotDegraded) {
				a.audit("replay_index_rebuild_error",
					map[string]any{"reason": reason, "attempts": attempt, "error": err.Error()})
			}
			return
		}
	}
}

// runRebuildOnce：一次 RuntimeRebuild。emitMu 是 Manager 序列化 audit append
// 與 Observe 的那把鎖；auditEnd 見 auditEnd() 的說明。
func (a *App) runRebuildOnce(attempt int) error {
	if h := a.hookRebuildResult; h != nil {
		return h(attempt)
	}
	if a.replayIndex == nil || a.manager == nil {
		return errNoReplayIndex
	}
	return a.replayIndex.RuntimeRebuild(a.eventsPath(), a.manager.EmitLocker(), a.auditEnd)
}

// auditEnd：replayindex.auditEndFunc 的實作——回報 audit 權威檔尾。
//
// 契約要求它「在已持有 emit mutex 時也會被呼叫，所以不得再取那把鎖」，同時
// 「在不持任何鎖時也會被呼叫，所以要自行保證讀取安全」。兩者的交集只有一種
// 作法：讀一個 atomic 值。這裡讀的是 JSONLSink 內部那個 atomic offset
// （appcore.JSONLSink.End），它就是「已受理的 append 進度」本身，不是對檔案
// 大小的另一次猜測——用 os.Stat 也不會取鎖，但那是繞過 sink 去問作業系統，
// sink 若日後加上 buffer 就會低報，而低報是 finalCatchUpAndAttach 明文 fail
// loud 的契約違反。
func (a *App) auditEnd() (int64, error) {
	if a.eventSink == nil {
		return 0, errors.New("app: event sink 未開啟，無法取得 audit 檔尾")
	}
	return a.eventSink.End(), nil
}

func (a *App) noteRebuildStart() {
	a.rebuildMu.Lock()
	defer a.rebuildMu.Unlock()
	a.rebuildStarts++
}

// noteRebuildBackoff：記錄並回傳第 attempt 次未收斂之後要等的時間。
func (a *App) noteRebuildBackoff(attempt int) time.Duration {
	base := a.rebuildBackoffBase
	if base <= 0 {
		base = defaultRebuildBackoffBase
	}
	d := base
	for i := 1; i < attempt && d < maxRebuildBackoff; i++ {
		d *= 2
	}
	if d > maxRebuildBackoff {
		d = maxRebuildBackoff
	}
	a.rebuildMu.Lock()
	a.rebuildDelays = append(a.rebuildDelays, d)
	a.rebuildMu.Unlock()
	return d
}

func (a *App) finishRebuild(done chan struct{}) {
	a.rebuildMu.Lock()
	if a.rebuildDone == done { // 只有自己那一輪才清旗標
		a.rebuildRunning = false
	}
	a.rebuildMu.Unlock()
	close(done)
}

// ---- §3.8 視窗化載入 ----

// turnPageSize：§3.8 凍結的分頁大小（釘選 pane 首次載入與向上分頁都是 20 個
// 完整 turn）。前端不得自行放大。
const turnPageSize = 20

// LoadTurnsBefore：§3.8 的視窗化載入／向上分頁（Wails binding，純新增）。
//
//   - beforeEventID 為空 → 尾端視窗：最近 n 個完整 turn ＋**未結束的目前
//     turn**（§3.8 明文要求一併載入，且不得從 turn 中間截斷）。
//   - beforeEventID 為某個已載入 turn 的第一筆 event id → 它之前的 n 個完整
//     turn（cursor 分頁；不含未結束 turn，那永遠只在尾端）。
//
// 回傳依 event id 遞增排列，與 cursor 那一頁不重疊（TurnsBefore 以 cursor 為
// 開區間上界）。cursor 已經是最舊時回空 slice、非錯誤。
//
// 為什麼要逐筆比對 WorkspaceSessionID：turn record 記的是**全域** events.jsonl
// 的 byte range，而多 session 並行時別的 WSID 的事件會夾在同一段範圍內。不過
// 濾就會把別人的對話混進這個 pane。
//
// **view boundary（owner 2026-08-17 D4 凍結）**：只回傳該 WSID 的
// ViewStartEventID **之後開始**的 turn，尾端載入與向上分頁都不得跨越它。
// 「開新對話」＝建立新的 view 世代；只清前端記憶體而不在這裡過濾的話，舊 turn
// 會在重新釘選、分頁或重啟後復活。判準沿用既有語意——只有 event ID **大於**
// boundary 的事件屬於目前 view。
//
// 完成的 turn 以 record 的 FirstEventID 整筆判定（要嘛全進、要嘛全不進），
// 不逐筆過濾：§3.8 明文不得從 turn 中間截斷。未完成的尾端 turn 沒有 record，
// 只能逐筆比對 EventID。
func (a *App) LoadTurnsBefore(wsid, beforeEventID string, n int) ([]contract.Envelope, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.loadTurnsBefore(wsid, beforeEventID, n)
}

// loadTurnsBefore：LoadTurnsBefore 的實作。交易閘是必要的而不只是形式——它讀
// a.replayIndex，而那是 startup 才發布的（reviewer 2026-08-20 實測與 startup
// 並行時是真的 data race）。
func (a *App) loadTurnsBefore(wsid, beforeEventID string, n int) ([]contract.Envelope, error) {
	if a.replayIndex == nil {
		return nil, errNoReplayIndex
	}
	if a.indexUnverified.Load() {
		return nil, errIndexUnverified
	}
	if wsid == "" {
		return nil, errors.New("app: LoadTurnsBefore 需要 workspace session id")
	}
	if n <= 0 {
		n = turnPageSize
	}
	recs, hasOlder, err := a.replayIndex.TurnsBefore(wsid, beforeEventID, n)
	if err != nil {
		return nil, err
	}
	if beforeEventID != "" && len(recs) == 0 {
		// cursor 指向 index 查無的 turn——分頁到底的訊號（legacy window 沒有
		// turn record，回到它自己的 event id 一定落在這裡）。回空、非錯誤，
		// 不當成全新 workspace 靜默重來。
		return nil, nil
	}
	viewStart := a.viewBoundary(wsid)

	f, err := os.Open(a.eventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 全新 workspace：沒有事件可載
		}
		return nil, fmt.Errorf("app: 開啟 events.jsonl: %w", err)
	}
	defer f.Close()

	var out []contract.Envelope
	for _, rec := range recs {
		if viewStart != "" && rec.FirstEventID <= viewStart {
			continue // boundary 之前開始的 turn 不屬於目前 view
		}
		envs, rerr := readEnvelopeRange(f, wsid, "", rec.StartOffset, rec.EndOffset)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, envs...)
	}
	if beforeEventID == "" {
		// §3.5.8：未完成 turn 不入 index，直接從 open_turn_start_offset 之後的
		// audit suffix 取得。較舊的分頁（beforeEventID!=""）永遠不含尾端未結束
		// turn，那只可能出現在最新頁。
		if start, open := a.replayIndex.OpenTurnStart(wsid); open {
			tail, terr := readEnvelopeRange(f, wsid, viewStart, start, -1)
			if terr != nil {
				return nil, terr
			}
			out = append(out, tail...)
		}
	}
	// legacy window 只在最舊 turn 頁（hasOlder==false）前綴一次（spec §5
	// 凍結的合併語意）：非最舊頁若也帶 legacy，使用者上滑會看到同一段歷史
	// 重複出現。a.wsReg 未接線時沒有可信的 LegacyTranscript／Provider／
	// ViewStart 來源，寧可少顯示也不要猜（同 viewBoundary 的降級方向）。
	//
	// ViewStartEventID=="" 一律不前綴（integration review 2026-08-23 I1）：
	// Migrate 可能建出空 ViewStart＋LegacyTranscript=true 的 entry（首啟空
	// events.jsonl、使用者從未 ResetView、resume 非空放行 Migrate）。空字串
	// 對 scanLegacyWindow 等於「不做 boundary 過濾」，會把該 provider 的
	// 全部歷史一次前綴進最舊頁——違反 m3b §3.2.5「不得把全部歷史丟進 legacy
	// session」。guard 比照 backfillLegacyTranscript 的空 boundary 前例
	// （app.go：re.ViewStartEventID == "" 時直接跳過該 provider）：無可信
	// boundary 來源＝無可信比對證據，不猜、不前綴。
	//
	// §6a 四分支（closeout C3，owner 裁決）：
	//   1. scan error（lerr!=nil）：原樣回錯，不清旗標。
	//   2. len(legacy)>0：照舊前綴，不清旗標（還沒掃到底，可能還有更舊事件）。
	//   3. len(legacy)==0 且 scanned==true：成功掃描確定零筆，清旗標並持久化；
	//      往後首載不再進這個合併分支。
	//   4. len(legacy)==0 且 scanned==false（NotExist）：不清旗標——缺檔不等於
	//      掃描過。這個分支在本函式開頭 os.Open 就已早退，不會走到這裡；
	//      scanned 仍保留給「早退 open 與這裡之間檔案被移除」的 TOCTOU 窗口。
	// 清旗標的 persist 失敗（分支 3 內）比照既有 registry 寫入慣例走
	// noteRegistryUncertainErr fail loud；哨兵（ErrEntryNotFound／
	// ErrTombstoned）是良性跳過，不當錯誤回報。
	if !hasOlder && a.wsReg != nil {
		if e, ok := a.wsReg.Get(wsid); ok && e.LegacyTranscript && e.ViewStartEventID != "" {
			legacy, scanned, lerr := scanLegacyWindow(a.eventsPath(), e.Provider, e.ViewStartEventID)
			if lerr != nil {
				return nil, lerr
			}
			if len(legacy) > 0 {
				out = append(legacy, out...)
			} else if scanned {
				// §6a：成功掃描確定零筆才清旗標（scanned 擋「早退 open 與掃描之間
				// 檔案被移除」的 TOCTOU 窗口；NotExist 主路徑在本函式開頭已早退）。
				// persist 失敗 fail loud——registry 寫不進去時掩蓋只會更晚發現。
				if cerr := a.wsReg.ClearLegacyTranscript(wsid); cerr != nil &&
					!errors.Is(cerr, wsregistry.ErrEntryNotFound) && !errors.Is(cerr, wsregistry.ErrTombstoned) {
					return nil, a.noteRegistryUncertainErr("legacy_flag_clear", wsid, cerr)
				}
			}
		}
	}
	// 所有頁（含無 legacy 前綴者）皆排序：防禦性保證。ULID 單調遞增時為 no-op，
	// 時鐘回撥造成非單調時仍保證輸出依 event_id 遞增。
	sort.SliceStable(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return out, nil
}

// viewBoundary：該 WSID 目前的 view 起點（registry 的 durable ViewStartEventID）。
// registry 未接線或查無此筆時回空字串＝不過濾——沒有可信的 boundary 來源時
// 少過濾（多顯示一段歷史）比多過濾（訊息憑空消失）安全。
func (a *App) viewBoundary(wsid string) string {
	if a.wsReg == nil {
		return ""
	}
	e, ok := a.wsReg.Get(wsid)
	if !ok {
		return ""
	}
	return e.ViewStartEventID
}

// readEnvelopeRange：讀 f 的 [start, end) 這段（end < 0 = 讀到檔尾），回傳其
// 中屬於 wsid 的 envelope。after 非空時額外只留 EventID 大於它的（view
// boundary；只有沒有 turn record 可整筆判定的尾端未完成 turn 需要）。
// 無法解析的行跳過不中斷（同 replayViewWindow 的既有慣例：稽核匯出走完整檔案，
// UI 視窗不因單一壞行整段消失）。
//
// 非 EOF 讀取錯誤一律 fail loud：把已讀內容當成功結果回傳等同靜默截頁，呼叫
// 端與使用者都無法分辨「這段本來就這麼短」與「讀到一半壞掉」。EOF（含被其他
// error 包裝的 io.EOF）才是正常終點，用 errors.Is 判定；end 早於實際檔尾這種
// 「EOF 提前發生」的情況仍維持既有寬容——回目前已收集到的部分結果、不回錯
// （spec §2）：那是呼叫端給的 range 本身過寬，不是讀取失敗，殘餘風險由呼叫端
// 對 range 來源（如 index 記錄的 offset）負責。
//
// 第一參數型別是 io.ReadSeeker 而非 *os.File，純為讓測試能注入「讀到一半才錯」
// 與「包裝 EOF」這兩種真實檔案系統造不出的形狀；production 呼叫端恆傳 *os.File。
func readEnvelopeRange(f io.ReadSeeker, wsid, after string, start, end int64) ([]contract.Envelope, error) {
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("app: seek events.jsonl to %d: %w", start, err)
	}
	r := bufio.NewReader(f)
	cur := start
	var out []contract.Envelope
	for end < 0 || cur < end {
		line, rerr := r.ReadBytes('\n')
		if n := int64(len(line)); n > 0 {
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var env contract.Envelope
				if json.Unmarshal(trimmed, &env) == nil && env.WorkspaceSessionID == wsid &&
					(after == "" || env.EventID > after) {
					out = append(out, env)
				}
			}
			cur += n
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, fmt.Errorf("app: read events.jsonl at %d (wsid=%s): %w", cur, wsid, rerr)
			}
			break // EOF（含包裝）＝正常終點；同批殘行已在上方處理
		}
	}
	return out, nil
}
