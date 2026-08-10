package appcore

import (
	"encoding/json"
	"fmt"
	"sync"
)

// TurnTrack：codex turn/interrupt 的參數追蹤（schema 必填 threadId + turnId），
// 自 M0 app.go 遷入。turn/started 記錄、turn/completed 清除。
type TurnTrack struct {
	mu       sync.Mutex
	threadID string
	turnID   string
}

func ParseTurnStarted(params []byte) (threadID, turnID string) {
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	return p.ThreadID, p.Turn.ID
}

func (t *TurnTrack) NoteStarted(params []byte) {
	th, tu := ParseTurnStarted(params)
	t.mu.Lock()
	if th != "" {
		t.threadID = th
	}
	t.turnID = tu
	t.mu.Unlock()
}

func (t *TurnTrack) NoteEnded() {
	t.mu.Lock()
	t.turnID = ""
	t.mu.Unlock()
}

func (t *TurnTrack) InterruptParams() (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.threadID == "" || t.turnID == "" {
		return nil, fmt.Errorf("no active codex turn (threadId=%q, turnId=%q)", t.threadID, t.turnID)
	}
	return map[string]any{"threadId": t.threadID, "turnId": t.turnID}, nil
}
