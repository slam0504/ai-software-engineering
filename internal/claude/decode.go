package claude

import (
	"encoding/json"
	"fmt"
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
		if head.Type == "assistant" {
			ev.Role = "assistant"
		} else {
			ev.Role = "tool" // provider echo（tool_result 載體），不進 Chat
		}
		ev.Kind = contract.KindMessage
		var body struct {
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &body) == nil { // 完成訊息保留本文，UI 才不會只剩 placeholder
			var sb strings.Builder
			var tools []string
			for _, c := range body.Message.Content {
				switch c.Type {
				case "text":
					sb.WriteString(c.Text)
				case "tool_use":
					tools = append(tools, toolSummary(c.Name, c.Input))
				}
			}
			ev.Text = sb.String()
			// tool-only assistant → KindToolUse + 摘要（含 text 者維持 M0 行為）
			if head.Type == "assistant" && ev.Text == "" && len(tools) > 0 {
				ev.Kind = contract.KindToolUse
				ev.Text = tools[0]
				if len(tools) > 1 {
					ev.Text += fmt.Sprintf(" +%d", len(tools)-1)
				}
			}
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
			Usage        *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheRead    int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.IsError, ev.CostUSD = body.IsError, body.TotalCostUSD
			if body.Usage != nil { // 該輪 wire 值（session 級收斂在 appcore）
				ev.Usage = &contract.Usage{InputTokens: body.Usage.InputTokens,
					OutputTokens: body.Usage.OutputTokens, CachedInput: body.Usage.CacheRead}
			}
		}
	default:
		ev.Kind = contract.KindUnknown
	}
	return ev
}

// toolSummary：工具名 + input JSON 節錄（規則凍結：內容 ≤80 rune，超過取前 80 rune
// 加「…」，節錄總長上限 81 rune）。
func toolSummary(name string, input json.RawMessage) string {
	excerpt := string(input)
	if r := []rune(excerpt); len(r) > 80 {
		excerpt = string(r[:80]) + "…"
	}
	return name + "(" + excerpt + ")"
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
