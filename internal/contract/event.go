package contract

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type Kind string

const (
	KindInit        Kind = "init"         // session 建立（含 provider session id）
	KindMessage     Kind = "message"      // 完整訊息（assistant / user）
	KindDelta       Kind = "delta"        // 串流片段（text / thinking）
	KindToolUse     Kind = "tool_use"     // 工具呼叫與結果摘要
	KindApproval    Kind = "approval"     // 核可請求（配 ApprovalRequest）
	KindRetry       Kind = "retry"        // provider 重試中
	KindResult      Kind = "result"       // turn 結束（成功或失敗）
	KindSystemOther Kind = "system_other" // 已認得但 M0 不處理的系統事件
	KindUnknown     Kind = "unknown"      // 不認得：Raw 保留、不中斷
	KindMalformed   Kind = "malformed"    // 解析失敗：Raw 保留、Err 必填
	KindStreamError Kind = "stream_error" // 傳輸層錯誤（scanner / rpc）
)

type Event struct {
	Provider  Provider
	Kind      Kind
	SessionID string
	Raw       []byte // provider wire 原文，一律保留
	Text      string // delta / message 的文字
	Thinking  string
	IsError   bool    // result 用
	CostUSD   float64 // result 用（provider 有提供才填）
	Err       error
}

func (e Event) Valid() bool {
	switch e.Provider {
	case ProviderClaude, ProviderCodex:
	default:
		return false
	}
	if e.Kind == "" || len(e.Raw) == 0 {
		return false
	}
	if e.Kind == KindMalformed && e.Err == nil {
		return false
	}
	return true
}

type ApprovalRequest struct {
	ID        string
	Provider  Provider
	ToolName  string // best-effort；原文在 RawParams
	Input     []byte
	RawParams []byte // provider 端請求原文（contract probe）
}

type ApprovalDecision struct {
	ID           string
	Behavior     string // allow | deny
	Message      string
	UpdatedInput []byte
}
