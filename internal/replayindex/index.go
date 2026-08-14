// Package replayindex：per-WSID 的 turn 索引（M3b spec §3.5）。events.jsonl 是
// append-only 的唯一權威且永不裁切；本套件只是**快取**——記錄某個 WSID 的
// 第 N 個完整 turn 佔了哪段 audit byte range，讓重啟／UI 只需讀最近 N 個
// 完整 turn，不必全掃 events.jsonl。
//
// 本檔（Task 15）只做 turn boundary 狀態機＋目錄形狀＋checkpoint 持久化。
// degraded latch／防遞迴通知（Task 16）、crash consistency 三態修復
// （Task 17）、損壞分級（Task 18）、runtime 重建交接（Task 19）、App 接線
// （Task 20）皆為後續 task，本檔不涉及。
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

// Config：replayindex 的可選設定。Notify 是 degraded latch 的復原／異常通知
// 出口，由 Task 16 接上防遞迴的 latch-then-notify 邏輯；本 task 僅宣告並保
// 存這個欄位，目前沒有任何程式路徑呼叫它。
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
// 狀態機。持鎖涵蓋狀態轉移與所有實際檔案 I/O。持久化失敗一律回錯（fail
// loud）——這個 task 沒有 degraded latch，呼叫端必須自行處理錯誤；latch 化
// 是 Task 16 的範圍。
func (idx *Index) Observe(env contract.Envelope, receipt appcore.AppendReceipt) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := idx.applyTurnState(env, receipt); err != nil {
		return err
	}
	idx.checkpointOffset = receipt.EndOffset
	idx.checkpointLastEventID = receipt.EventID
	return idx.writeCheckpointFile()
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
func (idx *Index) applyTurnState(env contract.Envelope, receipt appcore.AppendReceipt) error {
	wsid := env.WorkspaceSessionID
	if wsid == "" {
		return nil
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
	case st.open && isTerminalStateChange(env):
		rec := TurnRecord{
			StartOffset:  st.startOffset,
			EndOffset:    receipt.EndOffset,
			FirstEventID: st.firstEventID,
			LastEventID:  receipt.EventID,
		}
		if err := idx.appendTurnRecord(wsid, rec); err != nil {
			return err
		}
		st.open = false
		st.startOffset = 0
		st.firstEventID = ""
	}
	return nil
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
// （尚無並行存取）單執行緒呼叫。
func (idx *Index) writeCheckpointFile() error {
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
func (idx *Index) appendTurnRecord(wsid string, rec TurnRecord) error {
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
