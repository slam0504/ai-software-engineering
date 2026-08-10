package codex

import (
	"encoding/json"
	"fmt"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// codexItem：item tagged-union 的 M1 支援子集欄位（pinned schema 覆核）。
type codexItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Summary   string          `json:"summary"`
	Command   string          `json:"command"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Query     string          `json:"query"`
	Changes   []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

// excerpt80：節錄規則（與 claude toolSummary 同一定義）：內容 ≤80 rune 原樣；
// 超過取前 80 rune 加刪節號「…」——節錄總長上限 81 rune。
func excerpt80(v string) string {
	if r := []rune(v); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return v
}

// codexToolSummary：per-type 工具摘要（工具名＋參數節錄）；wire 欄位缺失時
// 以型別名 fallback（如實顯示、V3 不記 FAIL）。
func codexToolSummary(it codexItem) string {
	switch it.Type {
	case "commandExecution":
		if it.Command != "" {
			return excerpt80(it.Command)
		}
	case "mcpToolCall":
		// 必要欄位（server、tool、arguments）全部存在才產生摘要，
		// 任一缺失 → 型別名 fallback（不產生 "server.()" 等半成品）
		if it.Server != "" && it.Tool != "" && len(it.Arguments) > 0 {
			return it.Server + "." + it.Tool + "(" + excerpt80(string(it.Arguments)) + ")"
		}
	case "webSearch":
		if it.Query != "" {
			return "webSearch(" + excerpt80(it.Query) + ")"
		}
	case "fileChange":
		if len(it.Changes) > 0 {
			paths := it.Changes[0].Path
			if len(it.Changes) > 1 {
				paths += fmt.Sprintf(" +%d", len(it.Changes)-1)
			}
			return "fileChange(" + excerpt80(paths) + ")"
		}
	}
	return it.Type
}

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
			Item codexItem `json:"item"`
		}
		_ = json.Unmarshal(params, &p)
		switch p.Item.Type {
		case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
			ev.Kind = contract.KindToolUse
			ev.Text = codexToolSummary(p.Item)
		case "agentMessage", "userMessage":
			if method == MethodItemCompleted {
				ev.Kind = contract.KindMessage
				ev.Text = p.Item.Text
				if p.Item.Type == "agentMessage" {
					ev.Role = "assistant"
				} else {
					ev.Role = "tool" // provider echo，不進 Chat
				}
			} else {
				ev.Kind = contract.KindSystemOther
			}
		case "reasoning":
			if method == MethodItemCompleted {
				ev.Kind = contract.KindMessage
				ev.Thinking = p.Item.Summary
				ev.Role = "assistant"
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
	case MethodThreadTokenUsageUpdated:
		ev.Kind = contract.KindUsage
		var pu struct {
			TokenUsage struct {
				Total struct {
					InputTokens  int64 `json:"inputTokens"`
					OutputTokens int64 `json:"outputTokens"`
					CachedInput  int64 `json:"cachedInputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &pu) == nil { // thread 累計 snapshot（wire 原語意）
			ev.Usage = &contract.Usage{InputTokens: pu.TokenUsage.Total.InputTokens,
				OutputTokens: pu.TokenUsage.Total.OutputTokens, CachedInput: pu.TokenUsage.Total.CachedInput}
		}
	case MethodTurnDiffUpdated, MethodTurnStarted, MethodThreadStarted,
		MethodServerRequestResolved, MethodAccountLoginCompleted, MethodAccountUpdated,
		MethodThreadStatusChanged,
		MethodAccountRateLimitsUpdated, MethodMCPServerStartupStatus,
		MethodThreadGoalCleared:
		ev.Kind = contract.KindSystemOther
	default:
		ev.Kind = contract.KindUnknown
	}
	return ev
}
