// Package wirelog 實作 Codex app-server 的 connection-wide wire log（M3b §3.4）。
//
// 現行 codex.Conn 只容許單一 recorder sink，「每 session 一份實體錄流」不可直接
// 成立，故凍結為 connection-wide：每個 app-server generation 一份 always-on 錄流
// （handshake 前即開始，不採 recordCase reference count 啟停），session 級的錄流
// 證據改用有序的 []SegmentRef 表示（Task 11）。
//
// 本檔只提供基礎設施：一份 generation 的錄流檔（Generation）與可重建的 frame
// index。不接線到 codex.Conn 或 App（Task 12/13）。
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

// Generation 是一份 app-server generation 的 always-on 錄流檔＋可重建 frame
// index。每次 app-server restart（含 B1 受控 restart）開新 generation、新
// wire_log_id；不對舊 generation 續寫。
type Generation struct {
	mu  sync.Mutex
	id  string
	dir string
	f   *os.File

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
func NewGeneration(dir, id string) (*Generation, error) {
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
	return &Generation{id: id, dir: dir, f: f, idx: newFrameIndex()}, nil
}

// ID 回傳本 generation 的 wire_log_id。
func (g *Generation) ID() string { return g.id }

// Line 錄下一筆 wire frame 原文並回傳寫入結果。會被 read loop（s2c）與多個送
// 訊息的 goroutine（c2s）同時呼叫——frame 編號配發、JSON 封裝與實際寫入必須完
// 整落在同一段鎖內，否則並行呼叫可能讓 frame 編號與檔案內容的順序錯位、甚至讓
// 兩筆寫入交錯成損毀的 JSONL 行。
//
// 無法歸屬（尚未知道 WSID）的 frame 仍照樣寫入、不得丟棄（§3.4.5）；WSID 的歸
// 屬透過 Attribute 事後標記，寫入當下一律留空。
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
		row := wireRow{Frame: frame, Dir: dir, WSID: "", Raw: json.RawMessage(raw)}
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
	return nil
}

// Attribute 事後標記某個 FrameKey 對應的所有已寫入 frame 屬於哪個 WSID
// （threadID／turnID → WSID 歸屬，§3.4.3）。歸屬判定邏輯（如何從 raw frame 解出
// threadID／turnID 再查回 WSID）由呼叫端負責，本套件不重放判定過程——僅更新記
// 憶體內的 FrameIndex，不回頭改寫已落盤的 JSONL 行。
//
// 已知限制：RebuildFrameIndex 只讀 wire log 本身，故不會還原 Attribute 標記的
// 結果；崩潰後重建的 FrameIndex 是「未歸屬」狀態。是否需要 crash-safe 的歸屬持
// 久化留給接線 Task（12/13）依實際需求評估。
//
// 分工（coordinator 裁決）：本欄位僅 best-effort 記憶體註記，**不是** durable
// 的 session 級錄流證據——錄流 sink 掛在 codex.Conn 內，寫入當下未必解析得出
// WSID（pending start 窗口內尤其如此，thread 綁定還沒回來）。durable 權威是
// §3.4.4 的 []SegmentRef（見 SegmentRef 的 doc）；若讓 frame line 的 wsid 欄位
// 與 SegmentSet 都宣稱是歸屬權威，就是雙來源。
func (g *Generation) Attribute(key FrameKey, wsid string) {
	g.idx.attribute(key, wsid)
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

// Err 回傳目前 latch 住的寫入錯誤（可能在 Finalize 之前就已 latch）。
func (g *Generation) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writeErr
}

// FrameIndex 回傳本 generation 目前的 frame index（存活 view，會隨後續 Line／
// Attribute 呼叫更新）。
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
