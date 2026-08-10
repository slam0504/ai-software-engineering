package contract

// SessionState 是 SC2「卡在哪」的推導狀態。
type SessionState string

const (
	StateIdle             SessionState = "idle"
	StateWaiting          SessionState = "waiting" // user message 已送、回覆未開始
	StateStreaming        SessionState = "streaming"
	StateToolRunning      SessionState = "tool_running"
	StateAwaitingApproval SessionState = "awaiting_approval"
	StateRetrying         SessionState = "retrying"
	StateDone             SessionState = "done"
	StateFailed           SessionState = "failed"
)

// Reducer 從事件流推導 session 狀態。非併發安全：僅供 appcore.Manager 於其
// 序列化 mutex 內使用。
//
// 轉移語意（凍結）：user message（Role=user）→ waiting（非中性——第二輪送出
// 不得停留 done）；assistant message/delta → streaming；tool echo（Role=tool）
// 與 malformed（M0 定義不中斷）、system_other、unknown、usage、approval_decision
// 為中性；result 依 IsError 分流 done/failed；stream_error 為 terminal failed。
// init 為**中性**（第一輪 review P1）：新 session 的 idle 由 Reset() 建立；
// claude 每輪皆發 init、coordinator flush 時 init 排在 user→waiting 之後，
// 若 init 改狀態會把 waiting 打回 idle（違反 SC2「卡在哪」）。
type Reducer struct{ cur SessionState }

func NewReducer() *Reducer               { return &Reducer{cur: StateIdle} }
func (r *Reducer) Current() SessionState { return r.cur }
func (r *Reducer) Reset()                { r.cur = StateIdle }

func (r *Reducer) Apply(ev Event) (SessionState, bool) {
	next := r.cur
	switch ev.Kind {
	case KindMessage:
		switch ev.Role {
		case "user":
			next = StateWaiting
		case "assistant":
			next = StateStreaming
		default: // tool echo / system：中性
		}
	case KindDelta:
		next = StateStreaming
	case KindToolUse:
		next = StateToolRunning
	case KindApproval:
		next = StateAwaitingApproval
	case KindRetry:
		next = StateRetrying
	case KindResult:
		if ev.IsError {
			next = StateFailed
		} else {
			next = StateDone
		}
	case KindStreamError:
		next = StateFailed
	default: // malformed / system_other / unknown / usage / approval_decision：中性
	}
	changed := next != r.cur
	r.cur = next
	return r.cur, changed
}

// ResolveApproval：allow/deny/timeout 同構——離開 awaiting、回 tool_running。
func (r *Reducer) ResolveApproval() (SessionState, bool) {
	if r.cur != StateAwaitingApproval {
		return r.cur, false
	}
	r.cur = StateToolRunning
	return r.cur, true
}
