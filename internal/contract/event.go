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

	// M1 新增（additive；app-plan §5.2 範圍內）
	KindUsage            Kind = "usage"             // token 用量事件
	KindStateChange      Kind = "state_change"      // state reducer 輸出
	KindApprovalDecision Kind = "approval_decision" // 核可決定入事件流

	// M2 新增（additive；Stage A §3.4c）：workspace scope 事件，不進 provider slot。
	KindGateRequest  Kind = "gate_request"  // gate 待決：Manager.EmitWorkspace 出口
	KindBindingStale Kind = "binding_stale" // binding digest 過期通知

	// M3b 新增（additive；§3.3）：codex 共用連線上的 server／帳號層廣播——不屬於
	// 任何 thread，因此不進任何 workspace session slot，走 EmitWorkspace 出口。
	KindCodexBroadcast Kind = "codex_broadcast"
)

type Event struct {
	Provider  Provider
	Kind      Kind
	SessionID string
	Role      string // ""|user|assistant|tool|system——adapter 明確標注（M1）
	Raw       []byte // provider wire 原文，一律保留
	Text      string // delta / message 的文字
	Thinking  string
	IsError   bool    // result 用
	CostUSD   float64 // result 用（provider 有提供才填）
	Usage     *Usage  // provider 有提供才填（wire 原語意；session 級收斂在 appcore）
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
