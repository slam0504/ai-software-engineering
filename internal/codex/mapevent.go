package codex

import (
	"encoding/json"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// MapEvent 把 s2c 通知映射到 provider-neutral contract 事件。
// 本表為 M0 支援子集（依 pinned schema 覆核）：item 事件依 params.item.type
// 二級分流，未列型別與未知 method 落 KindUnknown（raw 保留）。
func MapEvent(method string, params json.RawMessage) contract.Event {
	raw, _ := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{method, params})
	ev := contract.Event{Provider: contract.ProviderCodex, Raw: raw}
	var meta struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &meta)
	ev.SessionID = meta.ThreadID

	switch method {
	case MethodAgentMessageDelta:
		ev.Kind = contract.KindDelta
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(params, &p) == nil {
			ev.Text = p.Delta
		}
	case MethodItemStarted, MethodItemCompleted:
		var p struct {
			Item struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Summary string `json:"summary"`
			} `json:"item"`
		}
		_ = json.Unmarshal(params, &p)
		switch p.Item.Type {
		case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
			ev.Kind = contract.KindToolUse
		case "agentMessage", "userMessage":
			if method == MethodItemCompleted {
				ev.Kind = contract.KindMessage
				ev.Text = p.Item.Text
			} else {
				ev.Kind = contract.KindSystemOther
			}
		case "reasoning":
			if method == MethodItemCompleted {
				ev.Kind = contract.KindMessage
				ev.Thinking = p.Item.Summary
			} else {
				ev.Kind = contract.KindSystemOther
			}
		case "plan", "contextCompaction":
			ev.Kind = contract.KindSystemOther
		default:
			ev.Kind = contract.KindUnknown
		}
	case MethodTurnCompleted:
		ev.Kind = contract.KindResult
		var p struct {
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &p) == nil {
			ev.IsError = p.Turn.Status == "failed"
		}
	case MethodTurnDiffUpdated, MethodTurnStarted, MethodThreadStarted,
		MethodServerRequestResolved, MethodAccountLoginCompleted, MethodAccountUpdated,
		MethodThreadStatusChanged, MethodThreadTokenUsageUpdated,
		MethodAccountRateLimitsUpdated, MethodMCPServerStartupStatus,
		MethodThreadGoalCleared:
		ev.Kind = contract.KindSystemOther
	default:
		ev.Kind = contract.KindUnknown
	}
	return ev
}
