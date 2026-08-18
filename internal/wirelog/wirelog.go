// Package wirelog 實作 Codex app-server 的 connection-wide wire log（M3b §3.4）。
//
// 現行 codex.Conn 只容許單一 recorder sink，「每 session 一份實體錄流」不可直接
// 成立，故凍結為 connection-wide：每個 app-server generation 一份 always-on 錄流
// （handshake 前即開始，不採 recordCase reference count 啟停），session 級的錄流
// 證據改用有序的 []SegmentRef 表示（Task 11）。
//
// 本檔提供基礎設施：一份 generation 的錄流檔（Generation）與可重建的 frame
// index。接線狀態（2026-08-17，§3.4.4 接線票之後）：
//
//   - Generation：由 codex.GenerationOwner 掛成 codex.Conn 的錄流 sink，
//     App 經 replaceCodexGeneration 建立／替換。
//   - SegmentSet（segments.go）：由 App 的 openWireSegments／beginWireSegment／
//     closeWireSegment 接線，落盤在 <stateDir>/wire-segments.jsonl。
//   - FrameIndex／RebuildFrameIndex／WSIDResolver：由 App 的
//     newWireGeneration（安裝 resolveWireFrameWSID）與 wireFramesOf（收尾時的
//     per-frame 證據，跨 generation 時經 RebuildFrameIndex 從磁碟重建）接線。
package wirelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

// Direction 標示 wire frame 的傳輸方向。§3.4.3 要求 frame index 的 key 必須含
// direction——client request 與 server request 的 ID 可能相撞，缺 direction 會
// 讓兩者的 frame 錯誤地合併在同一個 index bucket 底下。
type Direction string

const (
	DirClientToServer Direction = "c2s"
	DirServerToClient Direction = "s2c"
)

// SegmentRef 是一段 wire log 的參照：{wire_log_id, frame range}（§3.4.4）。
// Session 級錄流證據＝有序的 []SegmentRef，讓同一 WSID 可在 app-server restart
// 後跨 generation 延續。本 task 只定義型別；聚合／延續語意屬 Task 11 的 SegmentSet。
type SegmentRef struct {
	WireLogID  string `json:"wire_log_id"`
	StartFrame int    `json:"start_frame"` // 含
	EndFrame   int    `json:"end_frame"`   // 含
}

// wireRow 是 JSONL 單行的 envelope 形狀：{frame, dir, wsid, raw}。
type wireRow struct {
	Frame int             `json:"frame"`
	Dir   Direction       `json:"dir"`
	WSID  string          `json:"wsid"`
	Raw   json.RawMessage `json:"raw"`
}

// WSIDResolver 是 **write-time** 的 frame → WSID 歸屬判定（§3.4.3 的
// threadID／turnID→WSID 那一半）。由呼叫端（App）提供：本套件不知道 codex 的
// identity schema，也不持有路由表。
//
// 為什麼是 write-time 而不是事後標記：wire log 是 append-only 的 JSONL，歸屬若只
// 存在記憶體，crash／app 重啟後就整批消失（§3.4.3 要求 frame index「可重建」，而
// 唯一能重建它的來源就是 wire log 本身）。判定結果寫進該 frame 自己那一行的 wsid
// 欄位，RebuildFrameIndex 因此讀得回來。
//
// 判定不出來時回空字串——該 frame 照樣寫入、wsid 留空（§3.4.5），不得丟棄、也不得
// 猜一個 session。
//
// 呼叫時機：Generation.Line 在取得自己的鎖**之前**呼叫，因此 resolver 可以安全取
// App 的鎖（不會與 g.mu 形成環）；相對地 resolver 不得反過來呼叫任何會寫錄流的
// 路徑（那是 Line → resolver → Line 的遞迴）。
type WSIDResolver func(dir Direction, raw []byte) string

// Generation 是一份 app-server generation 的 always-on 錄流檔＋可重建 frame
// index。每次 app-server restart（含 B1 受控 restart）開新 generation、新
// wire_log_id；不對舊 generation 續寫。
type Generation struct {
	mu      sync.Mutex
	id      string
	dir     string
	f       *os.File
	resolve WSIDResolver

	nextFrame int
	writeErr  error // latched：首個寫入錯誤，不因後續成功而清除（§3.4.6）

	// forceErr 是測試專用的故障注入鉤子，唯一設值入口是 wirelog_test.go 的
	// ForceWriteErrForTest；production 路徑永遠不會設它。
	forceErr error

	finalized   bool
	finalizeErr error
	finalMeta   recorder.Meta

	idx *FrameIndex
}

// NewGeneration 建立一份 generation 的錄流檔（<dir>/<id>.jsonl）並回傳可寫入的
// handle。呼叫端須確保 id 在同一 dir 內全域唯一（如 wire_log_id／run id）；本函
// 式與 recorder.New 同慣例，不做去重保護，同名重跑會覆蓋舊檔。
//
// 呼叫端應在 handshake 之前就呼叫本函式（always-on，§3.4.1）——不是等發布成功
// 才建立，也不採 recordCase reference count 啟停。
//
// resolve 刻意作為**建構參數**而不是事後 setter：忘了掛 resolver 的後果是整份錄流
// 的 wsid 全空，而那個狀態與「真的無法歸屬」在檔案上長得一模一樣——沒有任何斷言分
// 得出來。放在建構子讓每個呼叫端都必須明確表態；nil 代表「本 generation 不做歸屬」
// （測試與非 codex 用途）。
func NewGeneration(dir, id string, resolve WSIDResolver) (*Generation, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("wirelog: invalid generation id %q", id)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return nil, err
	}
	return &Generation{id: id, dir: dir, f: f, resolve: resolve, idx: newFrameIndex()}, nil
}

// ID 回傳本 generation 的 wire_log_id。
func (g *Generation) ID() string { return g.id }

// Line 錄下一筆 wire frame 原文並回傳寫入結果。會被 read loop（s2c）與多個送
// 訊息的 goroutine（c2s）同時呼叫——frame 編號配發、JSON 封裝與實際寫入必須完
// 整落在同一段鎖內，否則並行呼叫可能讓 frame 編號與檔案內容的順序錯位、甚至讓
// 兩筆寫入交錯成損毀的 JSONL 行。
//
// WSID 歸屬在寫入當下由 resolve 判定並寫進該 frame 自己那一行（見 WSIDResolver）。
// 判定不出來的 frame 仍照樣寫入、wsid 留空、不得丟棄（§3.4.5）。
//
// **逐 frame，不是逐 FrameKey**：notification 沒有 request id，同方向的所有
// notification 會落在同一個 FrameKey bucket 底下；若以 key 為單位標記歸屬，兩個
// session 交錯的通知會被整批標成最後一次判定的那個 WSID。歸屬因此一律以 frame
// 編號為單位（idx.setWSID），FrameKey 只當索引鍵用。
//
// latch 後的行為（§3.4.6）：Line 進入時**不會**檢查 g.writeErr 做 short-circuit，
// 每次呼叫都照樣嘗試真實寫入。若底層問題是暫時性的（例如磁碟空間恢復），後續
// Line 呼叫仍可能成功並回傳 nil——但 Err() 會繼續回報第一次的失敗，不因此清除。
// latch 保證的是「首次失敗的原因不被覆寫」，不保證「之後所有呼叫必然失敗或被
// 擋」。是否要在 latch 後直接拒絕新寫入（例如只允許既有 session bounded 收尾、
// 擋新 session 建立）是呼叫端（Task 12/13 的 App 層）的政策決定，不在本套件內。
func (g *Generation) Line(dir Direction, raw []byte) error {
	if dir != DirClientToServer && dir != DirServerToClient {
		return fmt.Errorf("wirelog: invalid direction %q", dir)
	}

	// 歸屬判定刻意在 g.mu **之外**完成：resolver 會去取 App 的路由鎖，若在 g.mu 內
	// 呼叫就形成 g.mu → a.mu 的鎖序，與 App 既有的「先取 a.mu 再讀 generation」路徑
	// 互為反向。判定只看 frame 原文與當下路由表，不需要 frame 編號。
	wsid := ""
	if g.resolve != nil {
		wsid = g.resolve(dir, raw)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	frame := g.nextFrame
	g.nextFrame++

	var err error
	if g.forceErr != nil {
		// 故障注入優先於 marshal／實際寫入——模擬「底層寫入本身失敗」，不因
		// raw 是否為合法 JSON 而受影響。
		err = g.forceErr
	} else {
		row := wireRow{Frame: frame, Dir: dir, WSID: wsid, Raw: json.RawMessage(raw)}
		var b []byte
		b, err = json.Marshal(row)
		if err == nil {
			_, err = g.f.Write(append(b, '\n'))
		}
	}
	if err != nil {
		if g.writeErr == nil {
			g.writeErr = err
		}
		return err
	}

	key := FrameKey{WireLogID: g.id, Direction: dir, RequestID: extractRequestID(raw)}
	g.idx.add(key, frame)
	if wsid != "" {
		g.idx.setWSID(frame, wsid)
	}
	return nil
}

// Finalize 收尾本 generation：關閉錄流檔、寫入 meta（沿用 recorder.Meta 形
// 狀），並回傳收尾過程中遭遇的錯誤（latched 寫入錯誤＋close 錯誤＋meta 寫入錯
// 誤，errors.Join 後不吞）。
//
// 冪等：後續會有多條路徑呼叫（server 意外死亡的 reaper、B1 受控 restart、
// shutdown 總序），重複呼叫只回傳第一次呼叫的結果，不重複關檔、不重複寫 meta。
func (g *Generation) Finalize(meta recorder.Meta) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finalized {
		return g.finalizeErr
	}
	g.finalized = true

	closeErr := g.f.Close()
	if g.writeErr == nil && closeErr != nil {
		g.writeErr = closeErr
	}
	if g.writeErr != nil {
		meta.RecorderError = g.writeErr.Error()
	}

	b, _ := json.MarshalIndent(meta, "", "  ")
	metaErr := os.WriteFile(filepath.Join(g.dir, g.id+".meta.json"), b, 0o644)

	g.finalMeta = meta
	g.finalizeErr = errors.Join(g.writeErr, metaErr)
	return g.finalizeErr
}

// Finalized 回報 Finalize 是否已完成過一次。
func (g *Generation) Finalized() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.finalized
}

// FinalMeta 回傳 Finalize 寫入的 meta；Finalize 之前為零值。
func (g *Generation) FinalMeta() recorder.Meta {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.finalMeta
}

// Frames 回傳本 generation 目前已配發出去的 frame 數（＝下一個 frame 編號）。
//
// 這是 §3.4.4 session 級 []SegmentRef 的邊界來源：session 開始當下的 Frames()
// 即該段的 start_frame，收尾當下的 Frames()-1 即 end_frame。**Finalize 之後計數
// 凍結**（Finalize 不重設 nextFrame，且 Line 在關檔後只會寫失敗），所以「session
// 還活著、generation 已被收尾」的順序（server 意外死亡）仍讀得到正確的尾界，不會
// 把下一個 generation 的 frame 算進來。
func (g *Generation) Frames() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.nextFrame
}

// Err 回傳目前 latch 住的寫入錯誤（可能在 Finalize 之前就已 latch）。
func (g *Generation) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writeErr
}

// FrameIndex 回傳本 generation 目前的 frame index（存活 view，會隨後續 Line
// 呼叫更新）。
func (g *Generation) FrameIndex() *FrameIndex { return g.idx }

// extractRequestID 盡力從 raw JSON-RPC frame 解出 "id" 欄位的原文字串（number
// 與 string 兩種 schema union 皆支援）；找不到或格式不符時回空字串（notification
// 或無法解析的 frame 一律視為無 requestID，仍照常索引，不 fail loud——這只是索
// 引鍵的最佳努力萃取，不是frame 本身的正確性判斷）。
func extractRequestID(raw []byte) string {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.ID) == 0 {
		return ""
	}
	s := string(probe.ID)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}
