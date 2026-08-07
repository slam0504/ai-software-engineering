package claude

import (
	"encoding/json"
	"strings"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type MCPServerError struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
}
type InitInfo struct {
	SessionID       string            `json:"session_id"`
	Model           string            `json:"model"`
	Capabilities    []string          `json:"capabilities"`
	MCPServers      []MCPServerStatus `json:"mcp_servers"`
	MCPServerErrors []MCPServerError  `json:"mcp_server_errors"`
}

func Decode(line []byte) contract.Event {
	raw := append([]byte(nil), line...)
	ev := contract.Event{Provider: contract.ProviderClaude, Raw: raw}
	var head struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		ev.Kind, ev.Err = contract.KindMalformed, err
		return ev
	}
	ev.SessionID = head.SessionID
	switch head.Type {
	case "system":
		switch head.Subtype {
		case "init":
			ev.Kind = contract.KindInit
		case "api_retry":
			ev.Kind = contract.KindRetry
		default:
			ev.Kind = contract.KindSystemOther
		}
	case "assistant", "user":
		ev.Kind = contract.KindMessage
		var body struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &body) == nil { // 完成訊息保留本文，UI 才不會只剩 placeholder
			var sb strings.Builder
			for _, c := range body.Message.Content {
				if c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
			ev.Text = sb.String()
		}
	case "stream_event":
		ev.Kind = contract.KindDelta
		var body struct {
			Event struct {
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			} `json:"event"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.Text, ev.Thinking = body.Event.Delta.Text, body.Event.Delta.Thinking
		}
	case "rate_limit_event": // 訂閱 rate limit 狀態（A9 實測錄流出現，每 session 一筆）
		ev.Kind = contract.KindSystemOther
	case "result":
		ev.Kind = contract.KindResult
		var body struct {
			IsError      bool    `json:"is_error"`
			TotalCostUSD float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.IsError, ev.CostUSD = body.IsError, body.TotalCostUSD
		}
	default:
		ev.Kind = contract.KindUnknown
	}
	return ev
}

// ParseInit 只對 KindInit 事件回傳完整 init 資訊，其餘回 nil。
func ParseInit(ev contract.Event) *InitInfo {
	if ev.Kind != contract.KindInit {
		return nil
	}
	info := &InitInfo{}
	if json.Unmarshal(ev.Raw, info) != nil {
		return nil
	}
	return info
}
