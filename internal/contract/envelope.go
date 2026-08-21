package contract

import (
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ulidMu   sync.Mutex
	ulidLast int64
	ulidSeq  uint32
)

// NewULID：48-bit ms timestamp → 10 字 + 10 byte 隨機（前 2 byte 帶同毫秒序號保單調）。
func NewULID(t time.Time) string {
	ms := t.UnixMilli()
	ulidMu.Lock()
	if ms == ulidLast {
		ulidSeq++
	} else {
		ulidLast, ulidSeq = ms, 0
	}
	seq := ulidSeq
	ulidMu.Unlock()

	var b [26]byte
	v := uint64(ms)
	for i := 9; i >= 0; i-- {
		b[i] = crockford[v&31]
		v >>= 5
	}
	var rnd [10]byte
	_, _ = rand.Read(rnd[:])
	rnd[0], rnd[1] = byte(seq>>8), byte(seq)
	var acc uint64
	bits := 0
	j := 10
	for _, r := range rnd {
		acc = acc<<8 | uint64(r)
		bits += 8
		for bits >= 5 && j < 26 {
			bits -= 5
			b[j] = crockford[(acc>>uint(bits))&31]
			j++
		}
	}
	for ; j < 26; j++ {
		b[j] = crockford[0]
	}
	return string(b[:])
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedInput  int64 `json:"cached_input_tokens,omitempty"`
}

// Binding：workspace 事件攜帶的檔案/資源綁定（spec_manifest 等）＋內容 digest。
type Binding struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// Envelope 是事件 schema v1（app-plan §5.2）：UI 與稽核 JSONL 的統一載體。additive-only。
type Envelope struct {
	EventID        string          `json:"event_id"` // ULID，同毫秒單調
	TS             string          `json:"ts"`       // RFC3339Nano UTC
	Provider       string          `json:"provider"`
	SessionID      string          `json:"session_id,omitempty"`
	Role           string          `json:"role,omitempty"`
	TaskID         string          `json:"task_id,omitempty"`
	Kind           string          `json:"kind"`
	Text           string          `json:"text,omitempty"`
	Thinking       string          `json:"thinking,omitempty"`
	IsError        bool            `json:"is_error,omitempty"`
	CostUSD        float64         `json:"cost_usd,omitempty"`
	Usage          *Usage          `json:"usage,omitempty"`           // Manager 輸出時一律為累計 snapshot
	UsageSemantics string          `json:"usage_semantics,omitempty"` // session_total | provider_latest
	State          string          `json:"state,omitempty"`
	Error          string          `json:"error,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"` // 非合法 JSON 原文以 JSON string fallback

	// M2 新增（additive；Stage A §3.4c）：workspace scope 事件用（gate/binding），
	// 與 provider 事件共用同一 Envelope schema，但不進任何 provider slot。
	Scope         string          `json:"scope,omitempty"`          // "workspace" 表非 provider 事件
	Bindings      []Binding       `json:"bindings,omitempty"`       // gate/binding 事件攜帶的資源綁定
	Payload       json.RawMessage `json:"payload,omitempty"`        // 結構化 payload（不塞進 Text）
	CorrelationID string          `json:"correlation_id,omitempty"` // 跨事件關聯 id（例如 approval_id）
	Purpose       string          `json:"purpose,omitempty"`        // 事件用途說明

	// M3b 新增（additive；§3.1.5）：host-side 穩定 session identity。
	// Conversation lane 的每個 Envelope 自 BeginSubmit 起必填；workspace lane
	// 的 Gate／SpecAssist／PlanAssist one-shot 維持空值、不計入 slot（§3.1.6）。
	WorkspaceSessionID string `json:"workspace_session_id,omitempty"`
}

// Wrap：Event → Envelope。role 規則：ev.Role 明確標注優先；否則 Kind==delta →
// "assistant"、其餘 → "system"（不猜 message 的角色）。ev.Raw 非合法 JSON 時以
// JSON string fallback，保證 Envelope 必可 marshal。
func Wrap(ev Event, taskID string) Envelope {
	now := time.Now()
	role := ev.Role
	if role == "" {
		if ev.Kind == KindDelta {
			role = "assistant"
		} else {
			role = "system"
		}
	}
	env := Envelope{
		EventID: NewULID(now), TS: now.UTC().Format(time.RFC3339Nano),
		Provider: string(ev.Provider), SessionID: ev.SessionID, Role: role,
		TaskID: taskID, Kind: string(ev.Kind), Text: ev.Text, Thinking: ev.Thinking,
		IsError: ev.IsError, CostUSD: ev.CostUSD, Usage: ev.Usage,
	}
	if len(ev.Raw) > 0 {
		if json.Valid(ev.Raw) {
			env.Raw = json.RawMessage(ev.Raw)
		} else {
			s, _ := json.Marshal(string(ev.Raw))
			env.Raw = json.RawMessage(s)
		}
	}
	if ev.Err != nil {
		env.Error = ev.Err.Error()
	}
	return env
}
