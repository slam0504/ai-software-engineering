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
	ErrResetInProgress = errors.New("appcore: view reset in progress")
	ErrStaleSession    = errors.New("appcore: stale session token")
	ErrStaleReset      = errors.New("appcore: stale reset token")
	ErrClosed          = errors.New("appcore: manager closed")
)

// SubmissionID：coordinator 的唯一 ownership token（per slot）。
type SubmissionID struct{ gen, seq uint64 }

// SessionToken：session lifecycle token（generation + end 序號）；
// Cancel／Finish 只認目前 outstanding 的一枚。
type SessionToken struct{ gen, seq uint64 }

// ResetToken：NewSession 的 reset ownership token（M1.5；plan §5.4）。
type ResetToken struct{ gen, seq uint64 }

type Config struct {
	Sink                  AuditSink                   // 必填
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
	phaseResetting // M1.5：NewSession 的「teardown → restore reset」ownership 區段
)

// slot：單一 provider 的 session 狀態容器（M1.5 plan §5.3）。不對外暴露——
// Manager 是唯一 aggregate root，所有操作帶 provider 進同一 mutex。
type slot struct {
	reducer    *contract.Reducer
	taskID     string
	totalCost  float64
	totalUsage contract.Usage

	gen            uint64 // 換代遞增：舊 SubmissionID／SessionToken／ResetToken 全部失效
	seq            uint64
	submitting     *SubmissionID
	fromNewSession bool
	phase          sessionPhase
	sessionGen     uint64
	endSeq         uint64
	endTok         *SessionToken
	resetSeq       uint64
	resetTok       *ResetToken
	pendingBuf     []pendingEntry
}

func newSlot() *slot { return &slot{reducer: contract.NewReducer()} }

// Manager 是唯一序列化事件入口：單一 mutex 下完成 wrap→slot totals→sink→emit→
// state_change，輸出 event_id **檔案級**嚴格遞增（跨 provider；含 fail-loud 路徑）。
//
// # Per-slot session lifecycle（M1.5 plan §5.4）
//
//	idle --BeginNewSessionSubmit--> starting --AcceptSubmit--> active
//	starting --RejectSubmit--> idle
//	active --BeginEndSession--> ending --FinishEndSession--> idle
//	ending --CancelEndSession--> active
//	idle --BeginReset--> resetting --FinishReset--> idle
//	ending --FinishEndSessionIntoReset--> resetting（原子、無 idle 縫隙）
//
// resetting 期間 BeginSubmit／BeginNewSessionSubmit／BeginEndSession／第二個
// BeginReset 一律 ErrResetInProgress。非法轉移一律 sentinel error；stale token
// 一律 no-op error。跨 provider：一個 slot 的 pending submit 不阻塞另一個 slot。
type Manager struct {
	mu       sync.Mutex
	cfg      Config
	slots    map[contract.Provider]*slot
	auditErr error
	closed   bool
}

func New(cfg Config) *Manager {
	return &Manager{cfg: cfg, slots: map[contract.Provider]*slot{}}
}

func (m *Manager) slotLocked(p contract.Provider) *slot {
	sl, ok := m.slots[p]
	if !ok {
		sl = newSlot()
		m.slots[p] = sl
	}
	return sl
}

// newSessionLocked：flush 該 slot 殘留 queue（掛舊 task）→ 換代 → 重設。
func (m *Manager) newSessionLocked(p contract.Provider, sl *slot, taskID string) {
	m.flushLocked(sl)
	sl.gen++
	sl.submitting, sl.fromNewSession = nil, false
	sl.phase, sl.endTok, sl.resetTok = phaseIdle, nil, nil
	sl.reducer.Reset()
	sl.taskID = taskID
	sl.totalCost, sl.totalUsage = 0, contract.Usage{}
}

func (m *Manager) NewSession(p contract.Provider, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newSessionLocked(p, m.slotLocked(p), taskID)
}

// BeginNewSessionSubmit：StartSession 的單一 ownership 交易（per slot）。
func (m *Manager) BeginNewSessionSubmit(p contract.Provider, taskID string) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	sl := m.slotLocked(p)
	switch sl.phase {
	case phaseActive, phaseEnding:
		return SubmissionID{}, ErrSessionActive
	case phaseStarting:
		return SubmissionID{}, ErrSubmitActive
	case phaseResetting:
		return SubmissionID{}, ErrResetInProgress
	}
	if sl.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	m.newSessionLocked(p, sl, taskID)
	sl.seq++
	id := SubmissionID{gen: sl.gen, seq: sl.seq}
	sl.submitting, sl.fromNewSession = &id, true
	sl.phase = phaseStarting
	return id, nil
}

// BeginEndSession：進入 ending 並取得 token（per slot）。
func (m *Manager) BeginEndSession(p contract.Provider) (SessionToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionToken{}, ErrClosed
	}
	sl := m.slotLocked(p)
	switch sl.phase {
	case phaseIdle:
		return SessionToken{}, ErrNoSession
	case phaseStarting:
		return SessionToken{}, ErrStartInProgress
	case phaseEnding:
		return SessionToken{}, ErrEndInProgress
	case phaseResetting:
		return SessionToken{}, ErrResetInProgress
	}
	if sl.submitting != nil {
		return SessionToken{}, ErrSubmitActive
	}
	sl.phase = phaseEnding
	sl.endSeq++
	tok := SessionToken{gen: sl.sessionGen, seq: sl.endSeq}
	sl.endTok = &tok
	return tok, nil
}

// CancelEndSession：ending → active 復原（teardown 前）；stale token no-op error。
func (m *Manager) CancelEndSession(p contract.Provider, t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if sl.phase != phaseEnding || sl.endTok == nil || *sl.endTok != t {
		return ErrStaleSession
	}
	sl.phase = phaseActive
	sl.endTok = nil
	return nil
}

// FinishEndSession：收尾完成；stale token 一律 no-op error。
func (m *Manager) FinishEndSession(p contract.Provider, t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if sl.phase != phaseEnding || sl.endTok == nil || *sl.endTok != t {
		return ErrStaleSession
	}
	sl.phase = phaseIdle
	sl.endTok = nil
	return nil
}

// BeginReset：idle → resetting（NewSession 於無 active session 時的入口）。
func (m *Manager) BeginReset(p contract.Provider) (ResetToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ResetToken{}, ErrClosed
	}
	sl := m.slotLocked(p)
	switch sl.phase {
	case phaseStarting:
		return ResetToken{}, ErrStartInProgress
	case phaseActive:
		return ResetToken{}, ErrSessionActive
	case phaseEnding:
		return ResetToken{}, ErrEndInProgress
	case phaseResetting:
		return ResetToken{}, ErrResetInProgress
	}
	if sl.submitting != nil {
		return ResetToken{}, ErrSubmitActive
	}
	return m.enterResetLocked(sl), nil
}

// FinishEndSessionIntoReset：ending → resetting 原子轉移（無 idle 縫隙）——
// NewSession 於有 active session 時，收尾完成即直接持有 reset ownership。
func (m *Manager) FinishEndSessionIntoReset(p contract.Provider, t SessionToken) (ResetToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if sl.phase != phaseEnding || sl.endTok == nil || *sl.endTok != t {
		return ResetToken{}, ErrStaleSession
	}
	sl.endTok = nil
	return m.enterResetLocked(sl), nil
}

func (m *Manager) enterResetLocked(sl *slot) ResetToken {
	sl.phase = phaseResetting
	sl.resetSeq++
	tok := ResetToken{gen: sl.gen, seq: sl.resetSeq}
	sl.resetTok = &tok
	return tok
}

// FinishReset：resetting → idle；stale token 回 ErrStaleReset no-op。
func (m *Manager) FinishReset(p contract.Provider, t ResetToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if sl.phase != phaseResetting || sl.resetTok == nil || *sl.resetTok != t {
		return ErrStaleReset
	}
	sl.phase = phaseIdle
	sl.resetTok = nil
	return nil
}

func (m *Manager) SessionActive(p contract.Provider) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.slotLocked(p).phase == phaseActive
}

func (m *Manager) Emit(ev contract.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先——不 queue、不 Apply、不動 totals
		m.emitClosedDroppedLocked(string(ev.Kind), string(ev.Provider))
		return
	}
	sl := m.slotLocked(ev.Provider)
	if sl.submitting != nil { // 只 queue 該 slot（跨 provider 不互相阻塞）
		sl.pendingBuf = append(sl.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(sl, ev)
}

// BeginSubmit：既有 session 的後續輪（僅該 slot phaseActive 允許）。
func (m *Manager) BeginSubmit(p contract.Provider) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	sl := m.slotLocked(p)
	switch sl.phase {
	case phaseIdle:
		return SubmissionID{}, ErrNoSession
	case phaseStarting:
		return SubmissionID{}, ErrStartInProgress
	case phaseEnding:
		return SubmissionID{}, ErrEndInProgress
	case phaseResetting:
		return SubmissionID{}, ErrResetInProgress
	}
	if sl.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	sl.seq++
	id := SubmissionID{gen: sl.gen, seq: sl.seq}
	sl.submitting = &id
	return id, nil
}

func (m *Manager) checkOwnerLocked(sl *slot, id SubmissionID) error {
	if m.closed {
		return ErrClosed
	}
	if sl.submitting == nil || *sl.submitting != id || id.gen != sl.gen {
		return ErrStaleSubmission
	}
	return nil
}

// AcceptSubmit：canonical user envelope 先行 → 該 slot queue 依序 flush。
func (m *Manager) AcceptSubmit(p contract.Provider, id SubmissionID, sessionID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if err := m.checkOwnerLocked(sl, id); err != nil {
		return err
	}
	if sl.fromNewSession { // StartSession 路徑：provider 接受即 session 存活
		sl.phase = phaseActive
		sl.sessionGen = id.gen
	}
	sl.submitting, sl.fromNewSession = nil, false
	m.emitLocked(sl, contract.Event{Provider: p, Kind: contract.KindMessage,
		Role: "user", SessionID: sessionID, Text: text,
		Raw: []byte(`{"source":"workbench_user_input"}`)})
	m.flushLocked(sl)
	return nil
}

func (m *Manager) RejectSubmit(p contract.Provider, id SubmissionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	if err := m.checkOwnerLocked(sl, id); err != nil {
		return err
	}
	if sl.fromNewSession { // StartSession 失敗：回 idle
		sl.phase = phaseIdle
	}
	sl.submitting, sl.fromNewSession = nil, false
	m.flushLocked(sl)
	return nil
}

func (m *Manager) flushLocked(sl *slot) {
	buf := sl.pendingBuf
	sl.pendingBuf = nil
	for _, e := range buf {
		m.emitLocked(sl, e.ev)
		if e.resolveApprove {
			if st, changed := sl.reducer.ResolveApproval(); changed {
				m.emitStateLocked(sl, e.ev.Provider, e.ev.SessionID, st)
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
	sl := m.slotLocked(provider)
	ev := contract.Event{Provider: provider, Kind: contract.KindApproval,
		SessionID: sessionID, Text: toolName, Raw: raw}
	if sl.submitting != nil { // approval 同樣入該 slot queue
		sl.pendingBuf = append(sl.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(sl, ev)
}

func (m *Manager) EmitApprovalDecision(provider contract.Provider, sessionID, decision, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先
		m.emitClosedDroppedLocked(string(contract.KindApprovalDecision), string(provider))
		return
	}
	sl := m.slotLocked(provider)
	ev := contract.Event{Provider: provider, Kind: contract.KindApprovalDecision,
		SessionID: sessionID, Text: decision, Thinking: reason,
		Raw: []byte(`{"decision":"` + decision + `"}`)}
	if sl.submitting != nil {
		sl.pendingBuf = append(sl.pendingBuf, pendingEntry{ev: ev, resolveApprove: true})
		return
	}
	m.emitLocked(sl, ev)
	if st, changed := sl.reducer.ResolveApproval(); changed {
		m.emitStateLocked(sl, provider, sessionID, st)
	}
}

func (m *Manager) emitLocked(sl *slot, ev contract.Event) {
	semantics := ""
	switch {
	case ev.Kind == contract.KindUsage && ev.Usage != nil: // codex snapshot：覆寫
		sl.totalUsage = *ev.Usage
		semantics = "provider_latest"
	case ev.Kind == contract.KindResult:
		sl.totalCost += ev.CostUSD
		if ev.Usage != nil {
			if m.cfg.ClaudeUsageCumulative {
				sl.totalUsage = *ev.Usage
				semantics = "provider_latest"
			} else {
				sl.totalUsage.InputTokens += ev.Usage.InputTokens
				sl.totalUsage.OutputTokens += ev.Usage.OutputTokens
				sl.totalUsage.CachedInput += ev.Usage.CachedInput
				semantics = "session_total"
			}
		}
	}
	env := contract.Wrap(ev, sl.taskID)
	if ev.Kind == contract.KindUsage || ev.Kind == contract.KindResult {
		snap := sl.totalUsage
		env.Usage = &snap // 輸出一律該 slot 的累計 snapshot
		env.UsageSemantics = semantics
	}
	m.writeAndEmitLocked(env)
	if st, changed := sl.reducer.Apply(ev); changed {
		m.emitStateLocked(sl, ev.Provider, ev.SessionID, st)
	}
}

func (m *Manager) emitStateLocked(sl *slot, provider contract.Provider, sessionID string, st contract.SessionState) {
	env := contract.Wrap(contract.Event{Provider: provider, Kind: contract.KindStateChange,
		SessionID: sessionID, Raw: []byte(`{"state":"` + string(st) + `"}`)}, sl.taskID)
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

func (m *Manager) Totals(p contract.Provider) (float64, contract.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.slotLocked(p)
	return sl.totalCost, sl.totalUsage
}

func (m *Manager) State(p contract.Provider) contract.SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.slotLocked(p).reducer.Current()
}

func (m *Manager) AuditErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auditErr
}

// Close：同一 mutex、closed 旗標。對**所有** slot 執行 abort+flush（sink 關閉前
// queue 事件全部落 audit、無 user envelope、fail-loud 通知），之後才關 sink。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	for p, sl := range m.slots {
		if sl.submitting != nil || len(sl.pendingBuf) > 0 {
			n := len(sl.pendingBuf)
			sl.submitting, sl.fromNewSession = nil, false
			if sl.phase == phaseStarting {
				sl.phase = phaseIdle
			}
			m.flushLocked(sl) // sink 尚未關：queue 事件全數落 audit + UI
			m.cfg.Emit(contract.Envelope{
				EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
				Provider: string(p), Kind: string(contract.KindStreamError),
				Error: fmt.Sprintf("manager closing during pending submission: %d queued events flushed without user acceptance", n),
			})
		}
	}
	m.closed = true
	return m.cfg.Sink.Close()
}
