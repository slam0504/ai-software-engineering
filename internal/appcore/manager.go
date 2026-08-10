package appcore

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

var (
	ErrSubmitActive    = errors.New("appcore: submission already in progress")
	ErrSessionActive   = errors.New("appcore: session already active; end it first")
	ErrStaleSubmission = errors.New("appcore: stale submission id")
	ErrNoSession       = errors.New("appcore: no active session")
	ErrStartInProgress = errors.New("appcore: session start in progress")
	ErrEndInProgress   = errors.New("appcore: session end in progress")
	ErrStaleSession    = errors.New("appcore: stale session token")
	ErrClosed          = errors.New("appcore: manager closed")
)

// SubmissionID：coordinator 的唯一 ownership token。
type SubmissionID struct{ gen, seq uint64 }

// SessionToken：session lifecycle token（generation + end 序號）；
// Cancel／Finish 只認目前 outstanding 的一枚。
type SessionToken struct{ gen, seq uint64 }

type Config struct {
	Sink                  AuditSink                    // 必填
	Emit                  func(env contract.Envelope) // 必填：UI 出口
	ClaudeUsageCumulative bool                        // Task 4 VERDICT=per-turn → false（累加制）
}

type pendingEntry struct {
	ev             contract.Event
	resolveApprove bool // EmitApprovalDecision 的 reducer side effect 隨事件入列
}

type sessionPhase int

const (
	phaseIdle sessionPhase = iota
	phaseStarting
	phaseActive
	phaseEnding
)

// Manager 是唯一序列化事件入口（單一 mutex：wrap→totals→sink→emit→state_change
// 同鎖完成，event_id 輸出序嚴格遞增——含 fail-loud 路徑）。
//
// # Session lifecycle（單一 mutex 下的狀態機）
//
//	idle --BeginNewSessionSubmit--> starting --AcceptSubmit--> active
//	starting --RejectSubmit--> idle
//	active --BeginEndSession--> ending --FinishEndSession(token)--> idle
//	ending --CancelEndSession(token)--> active
//
// 非法轉移一律 sentinel error；stale token 一律 no-op error。
type Manager struct {
	mu         sync.Mutex
	cfg        Config
	reducer    *contract.Reducer
	taskID     string
	totalCost  float64
	totalUsage contract.Usage
	auditErr   error
	closed     bool

	gen            uint64 // 換代遞增：舊 SubmissionID／SessionToken 全部失效
	seq            uint64
	submitting     *SubmissionID // nil = 無 owner
	fromNewSession bool          // reservation 來自 BeginNewSessionSubmit
	phase          sessionPhase
	sessionGen     uint64
	endSeq         uint64
	endTok         *SessionToken // 目前 outstanding 的 end token（nil = 無）
	pendingBuf     []pendingEntry
}

func New(cfg Config) *Manager {
	return &Manager{cfg: cfg, reducer: contract.NewReducer()}
}

// newSessionLocked：flush 殘留 queue（掛舊 task）→ 換代 → 重設。
func (m *Manager) newSessionLocked(taskID string) {
	m.flushLocked()
	m.gen++ // 舊 SubmissionID／SessionToken 失效
	m.submitting, m.fromNewSession = nil, false
	m.phase, m.endTok = phaseIdle, nil
	m.reducer.Reset()
	m.taskID = taskID
	m.totalCost, m.totalUsage = 0, contract.Usage{}
}

func (m *Manager) NewSession(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newSessionLocked(taskID)
}

// BeginNewSessionSubmit：StartSession 的單一 ownership 交易——active 檢查、換代
// 與 reservation 在同一 mutex 內完成；併發 StartSession 恰一個取得 ownership，
// 輸家在建立任何 process／recorder／pump 之前就收到 error。
func (m *Manager) BeginNewSessionSubmit(taskID string) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	switch m.phase {
	case phaseActive, phaseEnding:
		return SubmissionID{}, ErrSessionActive
	case phaseStarting:
		return SubmissionID{}, ErrSubmitActive
	}
	if m.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	m.newSessionLocked(taskID)
	m.seq++
	id := SubmissionID{gen: m.gen, seq: m.seq}
	m.submitting, m.fromNewSession = &id, true
	m.phase = phaseStarting
	return id, nil
}

// BeginEndSession：進入 ending 並取得 token。starting → ErrStartInProgress
// （Start 未 Accept 前 End 不得無聲成功）；pending submit → ErrSubmitActive
// （teardown 不得與 pending submit 重疊）。
func (m *Manager) BeginEndSession() (SessionToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionToken{}, ErrClosed
	}
	switch m.phase {
	case phaseIdle:
		return SessionToken{}, ErrNoSession
	case phaseStarting:
		return SessionToken{}, ErrStartInProgress
	case phaseEnding:
		return SessionToken{}, ErrEndInProgress
	}
	if m.submitting != nil {
		return SessionToken{}, ErrSubmitActive
	}
	m.phase = phaseEnding
	m.endSeq++
	tok := SessionToken{gen: m.sessionGen, seq: m.endSeq}
	m.endTok = &tok
	return tok, nil
}

// CancelEndSession：ending → active 復原（teardown 前）；stale token no-op error。
func (m *Manager) CancelEndSession(t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phaseEnding || m.endTok == nil || *m.endTok != t {
		return ErrStaleSession
	}
	m.phase = phaseActive
	m.endTok = nil
	return nil
}

// FinishEndSession：收尾完成；stale token 一律 no-op error。
func (m *Manager) FinishEndSession(t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phaseEnding || m.endTok == nil || *m.endTok != t {
		return ErrStaleSession
	}
	m.phase = phaseIdle
	m.endTok = nil
	return nil
}

func (m *Manager) SessionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.phase == phaseActive
}

func (m *Manager) Emit(ev contract.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先——不 queue、不 Apply、不動 totals
		m.emitClosedDroppedLocked(string(ev.Kind), string(ev.Provider))
		return
	}
	if m.submitting != nil { // coordinator queue
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(ev)
}

// BeginSubmit：既有 session 的後續輪。僅 phaseActive 允許——idle → ErrNoSession、
// starting → ErrStartInProgress、ending → ErrEndInProgress（teardown 期間不得
// 啟動新 provider request）。
func (m *Manager) BeginSubmit() (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	switch m.phase {
	case phaseIdle:
		return SubmissionID{}, ErrNoSession
	case phaseStarting:
		return SubmissionID{}, ErrStartInProgress
	case phaseEnding:
		return SubmissionID{}, ErrEndInProgress
	}
	if m.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	m.seq++
	id := SubmissionID{gen: m.gen, seq: m.seq}
	m.submitting = &id
	return id, nil
}

func (m *Manager) checkOwnerLocked(id SubmissionID) error {
	if m.closed {
		return ErrClosed
	}
	if m.submitting == nil || *m.submitting != id || id.gen != m.gen {
		return ErrStaleSubmission
	}
	return nil
}

// AcceptSubmit：provider 接受後呼叫——canonical user envelope 先行 → queue 依序
// flush（順序保證 user → state_change(waiting) → provider events）。
func (m *Manager) AcceptSubmit(id SubmissionID, provider contract.Provider, sessionID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkOwnerLocked(id); err != nil {
		return err
	}
	if m.fromNewSession { // StartSession 路徑：provider 接受即 session 存活
		m.phase = phaseActive
		m.sessionGen = id.gen
	}
	m.submitting, m.fromNewSession = nil, false
	m.emitLocked(contract.Event{Provider: provider, Kind: contract.KindMessage,
		Role: "user", SessionID: sessionID, Text: text,
		Raw: []byte(`{"source":"workbench_user_input"}`)})
	m.flushLocked()
	return nil
}

func (m *Manager) RejectSubmit(id SubmissionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkOwnerLocked(id); err != nil {
		return err
	}
	if m.fromNewSession { // StartSession 失敗：回 idle
		m.phase = phaseIdle
	}
	m.submitting, m.fromNewSession = nil, false
	m.flushLocked()
	return nil
}

func (m *Manager) flushLocked() {
	buf := m.pendingBuf
	m.pendingBuf = nil
	for _, e := range buf {
		m.emitLocked(e.ev)
		if e.resolveApprove {
			if st, changed := m.reducer.ResolveApproval(); changed {
				m.emitStateLocked(e.ev.Provider, e.ev.SessionID, st)
			}
		}
	}
}

func (m *Manager) EmitApprovalRequest(provider contract.Provider, sessionID, toolName string, raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先
		m.emitClosedDroppedLocked(string(contract.KindApproval), string(provider))
		return
	}
	ev := contract.Event{Provider: provider, Kind: contract.KindApproval,
		SessionID: sessionID, Text: toolName, Raw: raw}
	if m.submitting != nil { // approval 同樣入 queue
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(ev)
}

func (m *Manager) EmitApprovalDecision(provider contract.Provider, sessionID, decision, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先
		m.emitClosedDroppedLocked(string(contract.KindApprovalDecision), string(provider))
		return
	}
	ev := contract.Event{Provider: provider, Kind: contract.KindApprovalDecision,
		SessionID: sessionID, Text: decision, Thinking: reason,
		Raw: []byte(`{"decision":"` + decision + `"}`)}
	if m.submitting != nil {
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev, resolveApprove: true})
		return
	}
	m.emitLocked(ev)
	if st, changed := m.reducer.ResolveApproval(); changed {
		m.emitStateLocked(provider, sessionID, st)
	}
}

func (m *Manager) emitLocked(ev contract.Event) {
	semantics := ""
	switch {
	case ev.Kind == contract.KindUsage && ev.Usage != nil: // codex snapshot：覆寫
		m.totalUsage = *ev.Usage
		semantics = "provider_latest"
	case ev.Kind == contract.KindResult:
		m.totalCost += ev.CostUSD
		if ev.Usage != nil {
			if m.cfg.ClaudeUsageCumulative {
				m.totalUsage = *ev.Usage
				semantics = "provider_latest"
			} else {
				m.totalUsage.InputTokens += ev.Usage.InputTokens
				m.totalUsage.OutputTokens += ev.Usage.OutputTokens
				m.totalUsage.CachedInput += ev.Usage.CachedInput
				semantics = "session_total"
			}
		}
	}
	env := contract.Wrap(ev, m.taskID)
	if ev.Kind == contract.KindUsage || ev.Kind == contract.KindResult {
		snap := m.totalUsage
		env.Usage = &snap // 輸出一律累計 snapshot
		env.UsageSemantics = semantics
	}
	m.writeAndEmitLocked(env)
	if st, changed := m.reducer.Apply(ev); changed {
		m.emitStateLocked(ev.Provider, ev.SessionID, st)
	}
}

func (m *Manager) emitStateLocked(provider contract.Provider, sessionID string, st contract.SessionState) {
	env := contract.Wrap(contract.Event{Provider: provider, Kind: contract.KindStateChange,
		SessionID: sessionID, Raw: []byte(`{"state":"` + string(st) + `"}`)}, m.taskID)
	env.State = string(st)
	m.writeAndEmitLocked(env)
}

func (m *Manager) writeAndEmitLocked(env contract.Envelope) {
	// closed 已在所有公開入口最先攔截；emitLocked 不可能於 closed 後執行。
	sinkErr := m.cfg.Sink.Write(env)
	m.cfg.Emit(env) // 原 envelope 先出（ID 較小），合成事件後出——輸出序嚴格遞增
	if sinkErr != nil {
		if m.auditErr == nil {
			m.auditErr = sinkErr
		}
		m.cfg.Emit(contract.Envelope{ // 只走 UI，不回寫 sink（防遞迴）
			EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Provider: env.Provider, Kind: string(contract.KindStreamError),
			Error: "audit sink: " + sinkErr.Error(),
		})
	}
}

// emitClosedDroppedLocked：close 後的唯一輸出——單一 UI stream_error（不寫 sink）。
func (m *Manager) emitClosedDroppedLocked(kind, provider string) {
	m.cfg.Emit(contract.Envelope{
		EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: provider, Kind: string(contract.KindStreamError),
		Error: "manager closed: event dropped (kind=" + kind + ")",
	})
}

func (m *Manager) Totals() (float64, contract.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalCost, m.totalUsage
}

func (m *Manager) State() contract.SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reducer.Current()
}

func (m *Manager) AuditErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auditErr
}

// Close：同一 mutex、closed 旗標。pending submission 存在時採顯式 abort+flush：
// sink 關閉前把 queue 事件全部落 audit（無 user envelope）＋ fail-loud 通知。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if m.submitting != nil || len(m.pendingBuf) > 0 {
		n := len(m.pendingBuf)
		m.submitting, m.fromNewSession = nil, false
		if m.phase == phaseStarting {
			m.phase = phaseIdle
		}
		m.flushLocked() // sink 尚未關：queue 事件全數落 audit + UI
		m.cfg.Emit(contract.Envelope{
			EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Kind:    string(contract.KindStreamError),
			Error:   fmt.Sprintf("manager closing during pending submission: %d queued events flushed without user acceptance", n),
		})
	}
	m.closed = true
	return m.cfg.Sink.Close()
}
