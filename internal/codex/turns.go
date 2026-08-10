package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

var ErrTurnActive = errors.New("codex: turn already active")

// ThreadRunner 管理單一 thread 的串行多輪。
//
// # 生命週期語意（凍結；M0 錄流實據）
//
// turn/start 的 response **立即**回 `turn{id,status:"inProgress"}`（turn 仍在跑）；
// 解鎖只由匹配 active turn id 的 NoteTurnEnded（completed/failed/interrupt 收尾）
// 觸發。completed 通知可能先於 StartTurn 消化 response（consumer 端競態）——
// 以 earlyEnded latch 對消：NoteTurnEnded 於 pending 期間 latch，StartTurn 解析
// response 後發現已 latch 即回 alreadyEnded=true（呼叫端直接執行 turn 收尾）。
// response 缺 turn id 一律 error（不回空 id + nil）。
type ThreadRunner struct {
	conn         *Conn
	mu           sync.Mutex
	threadID     string
	activeTurnID string
	pending      bool
	earlyEnded   map[string]bool
}

func NewThreadRunner(conn *Conn) *ThreadRunner {
	return &ThreadRunner{conn: conn, earlyEnded: map[string]bool{}}
}

func (t *ThreadRunner) ThreadID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.threadID
}

func (t *ThreadRunner) ActiveTurnID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeTurnID
}

// EnsureThread：threadID 空 → thread/start；resume 非空 → thread/resume。冪等。
func (t *ThreadRunner) EnsureThread(ctx context.Context, resume, approvalPolicy string) (string, error) {
	t.mu.Lock()
	if t.threadID != "" {
		id := t.threadID
		t.mu.Unlock()
		return id, nil
	}
	t.mu.Unlock()
	method := MethodThreadStart
	params := map[string]any{"approvalPolicy": approvalPolicy}
	if resume != "" {
		method = MethodThreadResume
		params["threadId"] = resume
	}
	res, err := t.conn.Call(ctx, method, params)
	if err != nil {
		return "", err
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &tr); err != nil || tr.Thread.ID == "" {
		return "", errors.New("codex: thread id missing in response")
	}
	t.mu.Lock()
	t.threadID = tr.Thread.ID
	t.mu.Unlock()
	return tr.Thread.ID, nil
}

// StartTurn：同鎖佔位 → Call（response 立即回）→ 解析 turn id。
// earlyEnded 含該 id → 對消、alreadyEnded=true。busy → ErrTurnActive。
func (t *ThreadRunner) StartTurn(ctx context.Context, prompt string) (string, bool, error) {
	t.mu.Lock()
	if t.pending || t.activeTurnID != "" {
		t.mu.Unlock()
		return "", false, ErrTurnActive
	}
	t.pending = true // 送 wire 前佔位：barrier 下只有一個贏家
	threadID := t.threadID
	t.mu.Unlock()

	res, err := t.conn.Call(ctx, MethodTurnStart, map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	})
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = false
	if err != nil {
		return "", false, err
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if uerr := json.Unmarshal(res, &tr); uerr != nil || tr.Turn.ID == "" {
		return "", false, errors.New("codex: turn id missing in turn/start response")
	}
	if t.earlyEnded[tr.Turn.ID] { // completed 先到：對消，不設 active
		delete(t.earlyEnded, tr.Turn.ID)
		return tr.Turn.ID, true, nil
	}
	t.activeTurnID = tr.Turn.ID
	return tr.Turn.ID, false, nil
}

// NoteTurnEnded：completed/failed/interrupt 收尾通知呼叫。匹配 active → 解鎖回
// true；pending 中（active 尚未寫入）→ latch 回 false；其他不匹配 → false。
func (t *ThreadRunner) NoteTurnEnded(turnID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending && t.activeTurnID == "" { // response 尚未消化：latch
		t.earlyEnded[turnID] = true
		return false
	}
	if t.activeTurnID == "" || turnID != t.activeTurnID {
		return false
	}
	t.activeTurnID = ""
	return true
}
