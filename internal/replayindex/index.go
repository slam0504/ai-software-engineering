// Package replayindex：per-WSID 的 turn 索引（M3b spec §3.5）。events.jsonl 是
// append-only 的唯一權威且永不裁切；本套件只是**快取**——記錄某個 WSID 的
// 第 N 個完整 turn 佔了哪段 audit byte range，讓重啟／UI 只需讀最近 N 個
// 完整 turn，不必全掃 events.jsonl。
//
// 本檔（Task 15）打底 turn boundary 狀態機＋目錄形狀＋checkpoint 持久化；
// Task 16 疊上 degraded latch＋防遞迴通知（§3.5.4）：index 只是快取，它壞
// 掉絕不能讓 provider turn 失敗，所以寫入失敗一律 latch degraded、不回錯給
// Observe 呼叫端。crash consistency 三態修復（Task 17）、損壞分級
// （Task 18）、runtime 重建交接（Task 19）、App 接線（Task 20）仍是後續
// task，本檔不涉及——Task 16 只提供 latch 狀態與 ClearDegraded 解除入口，
// 實際重建邏輯由那幾個 task 補上。
package replayindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// TurnRecord：一個完整 turn 在稽核 JSONL 中的 byte range 與首末 event id。
// StartOffset／EndOffset 一律來自 AppendReceipt，不自行推算（§3.5.2）。
type TurnRecord struct {
	StartOffset  int64  `json:"start_offset"`
	EndOffset    int64  `json:"end_offset"`
	FirstEventID string `json:"first_event_id"`
	LastEventID  string `json:"last_event_id"`
}

// Config：replayindex 的可選設定。Notify 是 degraded latch 的異常通知出口
// （§3.5.4）：index 寫入失敗時，Observe 先把狀態 latch 成 degraded、再（於
// 釋放 idx.mu 之後）呼叫 Notify——順序不能反過來，否則 Notify 送進的通知事
// 件會在 latch 生效前回灌 Observe、再次寫入失敗、無限遞迴。同一個 degraded
// generation（由開始 latch 到 ClearDegraded 解除為止）只呼叫一次。可以是
// nil，此時 Observe 只 latch、不通知。
type Config struct {
	Notify func(string)
}

// turnState：單一 WSID 目前是否處於一個未結束的 turn。
type turnState struct {
	open         bool
	startOffset  int64
	firstEventID string
}

// Index：per-WSID turn 索引的唯一 ownership。單一 mutex 涵蓋狀態轉移與實際
// 檔案 I/O——Observe 全程持鎖，不做「配發在鎖內、I/O 在鎖外」的兩段式（Task
// 8／11 前例）。目前是獨立套件，唯一呼叫端是測試；Task 20 接線後呼叫端會在
// Manager 自身的序列化 mutex 下呼叫 Observe，但 Index 自己的鎖仍須完整、
// 不可假設外部一定持鎖。
type Index struct {
	mu  sync.Mutex
	dir string
	cfg Config

	checkpointOffset      int64
	checkpointLastEventID string
	turns                 map[string]*turnState // wsid -> 目前是否有 open turn

	// degraded latch（§3.5.4）：index 寫入失敗時 latch，不回錯給 Observe 呼叫
	// 端。degradedErr 是觸發 latch 的原因，僅供通知文字與未來狀態查詢使用。
	// degradedNotified 是「每個 degraded generation 只通知一次」的守衛，
	// ClearDegraded 解除 latch 時一併重置，開啟下一個 generation。
	degraded         bool
	degradedErr      error
	degradedNotified bool

	// forceWriteErr 是測試專用的故障注入鉤子，唯一設值入口是 degraded_test.go
	// 的 ForceWriteErrForTest；production 路徑永遠不會設它。同慣例見
	// internal/wirelog/wirelog.go 的 forceErr。
	forceWriteErr error
}

// checkpointFile：checkpoint.json 的完整內容（§3.5.5）。OpenTurns 保存每個
// WSID 的 open_turn_start_offset——否則 checkpoint 前進到未完成 turn 之後，
// terminal 事件抵達時就無法重建該 turn 的起點。
type checkpointFile struct {
	Offset      int64            `json:"offset"`
	LastEventID string           `json:"last_event_id"`
	OpenTurns   map[string]int64 `json:"open_turn_start_offsets"`
}

// Open：等同 OpenWith(dir, Config{})。
func Open(dir string) (*Index, error) {
	return OpenWith(dir, Config{})
}

// OpenWith：以指定 Config 開啟／建立 dir 下的 index。dir 不存在時建立；
// checkpoint.json 不存在時以空白狀態初始化並落盤（同 wsregistry.Open 慣
// 例）；存在但無法解析時直接回錯——checkpoint 是重啟後定位索引進度與未完
// 成 turn 起點的權威，靜默重建等於承認可能遺漏或錯置索引記錄。
//
// 已存在的 checkpoint 若記錄某 WSID 有 open turn，載入後 OpenTurnStart 會
// 回報該 offset，但 firstEventID 尚未知（checkpoint 依 §3.5.5 只保存
// offset）——重新推導 firstEventID 是 Task 17 VerifyOrRebuild 的責任，本
// task 不處理。
func OpenWith(dir string, cfg Config) (*Index, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("replayindex: mkdir %s: %w", dir, err)
	}
	idx := &Index{
		dir:   dir,
		cfg:   cfg,
		turns: map[string]*turnState{},
	}
	if err := idx.loadCheckpoint(); err != nil {
		return nil, err
	}
	return idx, nil
}

// loadCheckpoint：建構期間單執行緒呼叫，尚無並行存取，不需持鎖。
func (idx *Index) loadCheckpoint() error {
	path := idx.checkpointPath()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		var cf checkpointFile
		if jerr := json.Unmarshal(b, &cf); jerr != nil {
			return fmt.Errorf("replayindex: malformed checkpoint %s: %w", path, jerr)
		}
		idx.checkpointOffset = cf.Offset
		idx.checkpointLastEventID = cf.LastEventID
		for wsid, off := range cf.OpenTurns {
			idx.turns[wsid] = &turnState{open: true, startOffset: off}
		}
		return nil
	case os.IsNotExist(err):
		return idx.writeCheckpointFile()
	default:
		return fmt.Errorf("replayindex: read checkpoint %s: %w", path, err)
	}
}

// Observe：把一筆已落盤的稽核事件（env＋其 AppendReceipt）餵給 turn boundary
// 狀態機。持鎖涵蓋狀態轉移與所有實際檔案 I/O。
//
// degraded latch（§3.5.4，Task 16，凍結取捨）：index 只是快取，它壞掉絕不
// 能讓 provider turn 失敗，所以持久化失敗**不回錯**給呼叫端——這是本套件
// 對 fail-loud 慣例唯一的例外，取捨已在 spec 凍結。失敗時改為：
//  1. latch degraded（checkpoint 前移的部分回滾到失敗前的值，見下）；
//  2. 釋放 idx.mu 之後才呼叫 Config.Notify（先 latch、後通知，順序不可反
//     過來——Notify 送進的通知事件本身會再次呼叫 Observe，此時 latch 已生
//     效，Observe 一開頭就直接 return nil，不會再嘗試寫入、不會再次觸發
//     Notify，因此不會遞迴）；
//  3. 同一個 degraded generation 只通知一次（degradedNotified），直到
//     ClearDegraded 解除 latch、開啟下一個 generation。
//
// latch 之後每一次 Observe 呼叫都是空操作：不跑 turn 狀態機、不動記憶體
// checkpoint、直接 return nil，直到 ClearDegraded 被呼叫（重建成功的入口，
// Task 17-19 範圍，本 task 只提供這個入口）。
//
// checkpoint.json 只在 turn boundary（開 turn／收 turn）才落盤，不是每個事件
// 都寫：Task 20 接線後 Observe 會在 Manager 的全域 mutex 內被呼叫，一次回覆
// 可能有幾十到幾百個 delta，若每個 delta 都同步 marshal＋WriteFile＋Rename，
// 等於把磁碟 I/O 放大到整條事件管線的關鍵路徑上。節流到 turn boundary 不影
// 響正確性——§3.5.3 本就把「checkpoint 落後於實際 audit 進度」列為 crash 後
// 的預期情形，修復機制是「掃 audit suffix 補索引」，不要求 checkpoint 逐事
// 件同步。記憶體中的 checkpointOffset／checkpointLastEventID 仍逐事件更新
// （Checkpoint() 讀的是這份即時值），只有實際落盤動作被節流。
//
// 落後量精確描述：checkpointOffset／checkpointLastEventID 是單一全域欄位，
// 不是 per-WSID，所以落後量**不是**「最多一個 turn」——M3b 常態是多個 WSID
// 同時各自有 open turn 並行推進 delta，此時落後量是「所有目前開著的 turn
// 自各自上次 boundary 以來累積的事件量之和」。這個節流也打開了一個正常關
// 閉會踩到的缺口（不該依賴 crash 修復機制解決）：見 Flush。
func (idx *Index) Observe(env contract.Envelope, receipt appcore.AppendReceipt) error {
	idx.mu.Lock()

	if idx.degraded {
		idx.mu.Unlock()
		return nil
	}

	// 失敗時要把 checkpoint 前移的部分回滾到失敗前的值（degraded 期間
	// checkpoint 不得前移——這是對記憶體值的保證；磁碟值本就只在 boundary
	// 才落盤，寫入失敗代表磁碟本來就沒動過，不需要另外回滾）。
	prevOffset := idx.checkpointOffset
	prevEventID := idx.checkpointLastEventID

	boundary, err := idx.applyTurnState(env, receipt)
	if err == nil {
		idx.checkpointOffset = receipt.EndOffset
		idx.checkpointLastEventID = receipt.EventID
		if boundary {
			if werr := idx.writeCheckpointFile(); werr != nil {
				idx.checkpointOffset = prevOffset
				idx.checkpointLastEventID = prevEventID
				err = werr
			}
		}
	}

	if err == nil {
		idx.mu.Unlock()
		return nil
	}

	idx.latchDegraded(err)
	notify := idx.cfg.Notify
	shouldNotify := notify != nil && !idx.degradedNotified
	if shouldNotify {
		idx.degradedNotified = true
	}
	wsid := env.WorkspaceSessionID
	idx.mu.Unlock() // 必須先解鎖才能呼叫 Notify——callback 可能再呼叫 Observe（見上）

	if shouldNotify {
		notify(fmt.Sprintf("replayindex degraded (wsid=%s): %v", wsid, err))
	}
	return nil
}

// Degraded：index 目前是否處於 degraded latch（§3.5.4）。true 時 Observe 是
// 空操作、checkpoint 不再前移，需靠重建（Task 17-19）成功後呼叫
// ClearDegraded 解除。
func (idx *Index) Degraded() bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.degraded
}

// ClearDegraded：以成功重建為準解除 degraded latch（§3.5.4：「解除以成功重
// 建為準」）。本 task 不做重建本身，只提供這個入口給 Task 17-19 的 runtime
// 重建路徑呼叫。解除的同時開啟下一個 degraded generation——degradedNotified
// 一併重置，所以下一次寫入失敗仍會各自發一次通知，不會因為用過一次就永久
// 啞掉。
func (idx *Index) ClearDegraded() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.degraded = false
	idx.degradedErr = nil
	idx.degradedNotified = false
}

// latchDegraded：呼叫端須持有 idx.mu。把 index 標成 degraded；err 是觸發原
// 因，保存供未來狀態查詢／診斷用（目前只有 Observe 的通知文字會用到，即呼
// 叫端自行帶出 err 組訊息，不經由這裡）。只設狀態、不做 I/O、不呼叫
// Notify——Notify 的呼叫時機由 Observe 在釋放鎖之後才觸發，這裡不能提前呼
// 叫，否則會在 lock 仍持有時遞迴呼叫 Observe 而自我死鎖。
func (idx *Index) latchDegraded(err error) {
	idx.degraded = true
	idx.degradedErr = err
}

// applyTurnState：turn boundary 狀態機本體（§3.5.9，凍結）。
//
//   - 沒有 WorkspaceSessionID 的事件屬 workspace-scope，與任何 WSID 的 turn
//     無關，直接略過。
//   - 不在 turn 中：只有 canonical user message（Kind=message、Role=user）
//     開啟一個新 turn；其餘一律略過——沒有 canonical user message 的
//     init／unknown／session_done 等事件屬 session metadata，不得猜成一個
//     turn。
//   - 在 turn 中：只有 terminal state_change（State=done|failed）結束 turn
//     並落盤一筆 TurnRecord；其餘一律略過。`result` 事件本身不結束
//     turn——它只是導出 terminal state_change 的來源，真正的邊界仍是那筆
//     衍生的 state_change。`stream_error` 同理不在此處特判：依
//     contract.Reducer（internal/contract/state.go:58-59）的既有語意，
//     stream_error 一律衍生 state_change=failed，本狀態機只認衍生後的
//     terminal state_change，不需要對 stream_error 額外開特例；若上游未
//     衍生對應的 state_change，turn 就維持未結束（§3.5.8 未完成 turn 不入
//     index，交由重啟修復處理，非本 task 範圍）。
//
// 回傳值 boundary：這筆事件是否觸發了開 turn 或收 turn（Observe 靠它決定要
// 不要落盤 checkpoint——見 Observe 的節流說明）。
func (idx *Index) applyTurnState(env contract.Envelope, receipt appcore.AppendReceipt) (bool, error) {
	wsid := env.WorkspaceSessionID
	if wsid == "" {
		return false, nil
	}
	st, ok := idx.turns[wsid]
	if !ok {
		st = &turnState{}
		idx.turns[wsid] = st
	}
	switch {
	case !st.open && isCanonicalUserMessage(env):
		st.open = true
		st.startOffset = receipt.StartOffset
		st.firstEventID = receipt.EventID
		return true, nil
	case st.open && isTerminalStateChange(env):
		rec := TurnRecord{
			StartOffset:  st.startOffset,
			EndOffset:    receipt.EndOffset,
			FirstEventID: st.firstEventID,
			LastEventID:  receipt.EventID,
		}
		if err := idx.appendTurnRecord(wsid, rec); err != nil {
			return false, err
		}
		st.open = false
		st.startOffset = 0
		st.firstEventID = ""
		return true, nil
	}
	return false, nil
}

func isCanonicalUserMessage(env contract.Envelope) bool {
	return env.Kind == string(contract.KindMessage) && env.Role == "user"
}

func isTerminalStateChange(env contract.Envelope) bool {
	if env.Kind != string(contract.KindStateChange) {
		return false
	}
	return env.State == string(contract.StateDone) || env.State == string(contract.StateFailed)
}

// RecentTurns：wsid 最近（檔尾）至多 n 筆完整 turn，時間遞增排列。未完成
// turn 不含在內（§3.5.8）；wsid 尚無完整 turn 時回傳空 slice、非錯誤。
//
// 目前實作是 O(該 WSID 全部 turn 數)——readTurnFileLocked 整份 <wsid>.turns.jsonl
// 讀完解析、才在記憶體中取尾，不是從檔尾反向讀。這是刻意取捨，不是疏漏：
// turns.jsonl 每筆約 100 bytes，即使單一 WSID 累積一萬個 turn 也才 ~1MB，
// 比它取代的 events.jsonl（動輒上百 MB）小兩到三個數量級，現在寫反向讀取
// 器是為了還沒發生的問題增加複雜度。觸發改寫的條件：若實測某 WSID 的
// turns.jsonl 成長到單次全讀有感（例如同一 WSID 累積數萬筆以上、或量到
// I/O 延遲影響 UI 捲動），才值得換成真正的 tail read。
func (idx *Index) RecentTurns(wsid string, n int) ([]TurnRecord, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	all, err := idx.readTurnFileLocked(wsid)
	if err != nil {
		return nil, err
	}
	return capTail(all, n), nil
}

// TurnsBefore：beforeEventID 為某個已載入 turn 的 FirstEventID（分頁 cursor，
// 對應 §3.8「向上捲到頂」）；回傳其之前、時間遞增排列的至多 n 筆完整 turn。
// beforeEventID 為空字串時等同 RecentTurns（首次載入無 cursor）。cursor 找
// 不到對應 turn 時回傳空 slice、非錯誤——避免把「cursor 已經是最舊」與
// 「cursor 不存在」混為一種呼叫端無法分辨的錯誤。
//
// 與 RecentTurns 共用 readTurnFileLocked，同樣是 O(該 WSID 全部 turn 數) 的
// 全讀取捨，理由與改寫條件見 RecentTurns 的 doc。
func (idx *Index) TurnsBefore(wsid, beforeEventID string, n int) ([]TurnRecord, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	all, err := idx.readTurnFileLocked(wsid)
	if err != nil {
		return nil, err
	}
	if beforeEventID == "" {
		return capTail(all, n), nil
	}
	cut := -1
	for i, rec := range all {
		if rec.FirstEventID == beforeEventID {
			cut = i
			break
		}
	}
	if cut <= 0 {
		return nil, nil
	}
	return capTail(all[:cut], n), nil
}

// OpenTurnStart：wsid 目前未結束 turn 的起始 offset（若有）。對應
// checkpoint.json 的 open_turn_start_offset（§3.5.5）。
func (idx *Index) OpenTurnStart(wsid string) (int64, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	st, ok := idx.turns[wsid]
	if !ok || !st.open {
		return 0, false
	}
	return st.startOffset, true
}

// Checkpoint：目前已索引的 audit byte offset 與最後一筆處理過的 event id。
func (idx *Index) Checkpoint() (int64, string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.checkpointOffset, idx.checkpointLastEventID
}

// Flush：強制把記憶體中目前的 checkpoint 狀態（含所有 WSID 的
// open_turn_start_offset）落盤，冪等。
//
// Observe 把 checkpoint 落盤節流到 turn boundary（見 Observe 的說明）之後，
// 正常關閉也會踩到一個原本不存在的缺口：若 app 在某個 WSID 的 turn 仍開著
// 時被正常關閉，checkpoint 會停在上一個 boundary，效果跟 crash 沒有兩
// 樣，得靠啟動期「掃 audit suffix 補索引」的修復機制補回來。正常關閉不該
// 依賴 crash 修復機制——這個缺口是節流造成的，補救也該在本套件裡：呼叫端
// （Task 20 起，Manager 的收尾／shutdown 路徑）應在關閉序列裡呼叫 Flush，
// 確保 checkpoint 反映關閉當下記憶體中的即時 offset。
func (idx *Index) Flush() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.writeCheckpointFile()
}

func capTail(recs []TurnRecord, n int) []TurnRecord {
	if n <= 0 || len(recs) == 0 {
		return nil
	}
	if n < len(recs) {
		return recs[len(recs)-n:]
	}
	return recs
}

func (idx *Index) checkpointPath() string {
	return filepath.Join(idx.dir, "checkpoint.json")
}

func (idx *Index) turnFilePath(wsid string) string {
	return filepath.Join(idx.dir, wsid+".turns.jsonl")
}

// writeCheckpointFile：temp file + atomic rename、0600（沿用
// internal/wsregistry/store.go 的慣例）。呼叫端須持有 idx.mu，或於建構期間
// （尚無並行存取）單執行緒呼叫。forceWriteErr 非 nil 時故障注入優先於實際
// 寫入（測試專用，見 forceWriteErr 欄位說明）。
func (idx *Index) writeCheckpointFile() error {
	if idx.forceWriteErr != nil {
		return idx.forceWriteErr
	}
	cf := checkpointFile{
		Offset:      idx.checkpointOffset,
		LastEventID: idx.checkpointLastEventID,
		OpenTurns:   map[string]int64{},
	}
	for wsid, st := range idx.turns {
		if st.open {
			cf.OpenTurns[wsid] = st.startOffset
		}
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	path := idx.checkpointPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("replayindex: write checkpoint tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replayindex: rename checkpoint: %w", err)
	}
	return nil
}

// appendTurnRecord：<dir>/<wsid>.turns.jsonl 只在 terminal state_change 抵達
// 時 append 一筆——不是每個事件都寫。單筆小量寫入，不維持常駐檔案 handle。
// forceWriteErr 非 nil 時故障注入優先於實際寫入（測試專用，見 forceWriteErr
// 欄位說明）。
func (idx *Index) appendTurnRecord(wsid string, rec TurnRecord) error {
	if idx.forceWriteErr != nil {
		return idx.forceWriteErr
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := idx.turnFilePath(wsid)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("replayindex: open turn file %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
		return fmt.Errorf("replayindex: append turn record %s: %w", path, err)
	}
	return nil
}

// readTurnFileLocked：呼叫端須持有 idx.mu。缺檔視為「尚無完整 turn」，非
// 錯誤。中段損壞的分級處置（尾端 truncate vs 中段 quarantine）是 Task 18
// 範圍；本 task 對任何無法解析的行一律 fail loud。
func (idx *Index) readTurnFileLocked(wsid string) ([]TurnRecord, error) {
	path := idx.turnFilePath(wsid)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("replayindex: read %s: %w", path, err)
	}
	var out []TurnRecord
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec TurnRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("replayindex: malformed turn record in %s: %w", path, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("replayindex: scan %s: %w", path, err)
	}
	return out, nil
}
