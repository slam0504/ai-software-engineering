package appcore

import (
	"encoding/json"
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

	// M3b §3.1：per-WSID slot registry ＋三段建立交易的 sentinel。
	ErrSessionLimit     = errors.New("appcore: session slot limit reached")
	ErrSessionNotFound  = errors.New("appcore: unknown workspace session")
	ErrStaleCreate      = errors.New("appcore: stale create token")
	ErrProviderMismatch = errors.New("appcore: event provider != slot provider")
)

// MaxSessionsPerProvider：凍結常數——不進 config、不讀環境變數（M3b §3.1.4）。
const MaxSessionsPerProvider = 4

// WSID：host-side 穩定 workspace session identity（contract.NewULID 產生）。
type WSID string

// CreateToken：ReserveSession → CommitCreate／AbortCreate 的一次性 ownership token。
// 已 commit／已 abort 的 token 再次使用一律 ErrStaleCreate。
type CreateToken struct {
	wsid WSID
	seq  uint64
}

// SubmissionID：coordinator 的唯一 ownership token（per slot）。
type SubmissionID struct{ gen, seq uint64 }

// SessionToken：session lifecycle token（generation + end 序號）；
// Cancel／Finish 只認目前 outstanding 的一枚。
type SessionToken struct{ gen, seq uint64 }

// ResetToken：NewSession 的 reset ownership token（M1.5；plan §5.4）。
type ResetToken struct{ gen, seq uint64 }

// TurnIndex：replay index 的 Manager 側視角（*replayindex.Index 滿足）。
// 介面而非具體型別是為了避免 import cycle——replayindex 依賴 appcore 的
// AppendReceipt，反向依賴不成立。
//
// 契約（§3.5.4／§3.5.7）：
//   - Observe 在 Manager 的序列化 mutex 內、**audit 寫入成功之後**被呼叫，
//     所以 index 看到的順序與 events.jsonl 的 byte 順序完全一致；
//   - **Observe 絕不能讓 provider turn 失敗**：實作端自行 latch degraded 並
//     回 nil，Manager 這側也不因它的回傳值改變任何行為（見 writeAndEmitLocked）。
type TurnIndex interface {
	Observe(env contract.Envelope, receipt AppendReceipt) error
}

type Config struct {
	Sink                  AuditSink                   // 必填
	Emit                  func(env contract.Envelope) // 必填：UI 出口
	ClaudeUsageCumulative bool                        // Task 4 VERDICT=per-turn → false（累加制）

	// Index：replay index（可為 nil——index 只是快取，沒有它 Manager 行為
	// 完全不變）。Task 20 接線；只有 App 的 production 啟動流程會填。
	Index TurnIndex
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

// slot：單一 workspace session 的狀態容器（M1.5 plan §5.3；M3b §3.1 改為 per-WSID）。
// 不對外暴露——Manager 是唯一 aggregate root，所有操作一律帶 WSID 進同一 mutex。
type slot struct {
	reducer    *contract.Reducer
	taskID     string
	totalCost  float64
	totalUsage contract.Usage

	// M3b §3.1：slot identity。wsid 供 emit 路徑回填 Envelope.WorkspaceSessionID；
	// legacy slot 的 wsid 留空，遷移窗口內落在它上面的 envelope 因此與 M3a 完全一致。
	wsid      WSID
	provider  contract.Provider
	committed bool   // false = 只保留名額，尚未 CommitCreate；一律拒絕
	isLegacy  bool   // LegacyWSID 惰性建立（Task 26 刪除）：不計入 countLocked
	createSeq uint64 // CreateToken 比對用

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
//
// # Slot registry（M3b §3.1）
//
// slot 以 WSID 為 key（同一 provider 最多 MaxSessionsPerProvider 個）。建立走
// ReserveSession → CommitCreate／AbortCreate 三段交易：reservation 當下即佔名額，
// 未 commit 的 slot 對新入口不可見。全部入口只讀不建（Task 9 已刪除 provider-keyed
// 簽名）；唯一的例外是 LegacyWSID，見其 doc。
type Manager struct {
	mu         sync.Mutex
	cfg        Config
	slots      map[WSID]*slot
	legacy     map[contract.Provider]WSID // LegacyWSID 的 provider → legacy slot（Task 26 刪除）
	reserveSeq uint64
	auditErr   error
	closed     bool
}

func New(cfg Config) *Manager {
	return &Manager{cfg: cfg, slots: map[WSID]*slot{}, legacy: map[contract.Provider]WSID{}}
}

// countLocked：該 provider 已佔用的名額（含尚未 commit 的 reservation）。
//
// legacy slot 是遷移期產物，不佔使用者名額：它是 app 層 legacyWSIDFor 最後順位
// 的落點（全新安裝、registry 空、無 live host 時），由 LegacyWSID 惰性建立。若把
// 它計入，遷移窗口內每個 provider 的可建立數就會從 4 掉到 3——使用者看得到的差異，
// 而 legacy slot 本身不是使用者建立的 session。
//
// 因此**遷移期每 provider 實際上限為 MaxSessionsPerProvider + 1 = 5**：m.legacy[p]
// 是一對一 map，每個 provider 最多一個 legacy slot。Task 26 刪除相容層後回歸 4。
func (m *Manager) countLocked(p contract.Provider) int {
	n := 0
	for _, sl := range m.slots {
		if sl.provider == p && !sl.isLegacy {
			n++
		}
	}
	return n
}

// ReserveSession：三段建立交易第一段——同一 mutex 內完成「檢查上限 → 產 WSID →
// 放進 slots」，第 5 個併發建立不可穿透。
func (m *Manager) ReserveSession(p contract.Provider) (WSID, CreateToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", CreateToken{}, ErrClosed
	}
	if m.countLocked(p) >= MaxSessionsPerProvider {
		return "", CreateToken{}, ErrSessionLimit
	}
	w := WSID(contract.NewULID(time.Now()))
	m.reserveSeq++
	sl := newSlot()
	sl.wsid, sl.provider, sl.createSeq = w, p, m.reserveSeq
	m.slots[w] = sl // reservation 當下即佔名額
	return w, CreateToken{wsid: w, seq: m.reserveSeq}, nil
}

// pendingCreateLocked：只認尚未 commit／abort 的 reservation；其餘一律 stale。
func (m *Manager) pendingCreateLocked(tok CreateToken) (*slot, error) {
	sl, ok := m.slots[tok.wsid]
	if !ok || sl.committed || tok.seq == 0 || sl.createSeq != tok.seq {
		return nil, ErrStaleCreate
	}
	return sl, nil
}

// CommitCreate：第二段——正式建立，slot 自此對新入口可見。
func (m *Manager) CommitCreate(tok CreateToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	sl, err := m.pendingCreateLocked(tok)
	if err != nil {
		return err
	}
	sl.committed = true
	return nil
}

// AbortCreate：第三段——建立失敗退回名額。
//
// 與 CommitCreate 不同，**刻意不檢查 m.closed**：shutdown 期間仍必須能退回
// reservation，否則「CommitCreate 失敗 → 補償 AbortCreate」這條路徑一旦與 Close
// 競態，該名額就永久洩漏（slot 留在 map 裡且永遠不會被 commit）。本方法只刪
// map entry，不 emit、不寫 sink，closed 後執行沒有副作用。Task 4 的建立交易
// 補償直接依賴這個語意。
func (m *Manager) AbortCreate(tok CreateToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.pendingCreateLocked(tok); err != nil {
		return err
	}
	delete(m.slots, tok.wsid)
	return nil
}

// RestoreDormant：把 registry 既存的 dormant session 掛回 slot（已 committed，
// 不經 reservation）。同 WSID＋同 provider 重複 restore 為 no-op（啟動修復序列在
// crash 後重跑必須冪等）；provider 不符、或該 WSID 正卡在一筆未完成的 reservation
// 一律 fail loud——後者若放行，呼叫端會誤判 restore 成功，之後每個 ...WS 呼叫都是
// ErrSessionNotFound。
func (m *Manager) RestoreDormant(w WSID, p contract.Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if w == "" {
		return ErrSessionNotFound
	}
	if sl, ok := m.slots[w]; ok {
		if sl.provider != p {
			return ErrProviderMismatch
		}
		if !sl.committed { // 撞上進行中的 reservation：不可當作 restore 成功
			return ErrStaleCreate
		}
		return nil
	}
	if m.countLocked(p) >= MaxSessionsPerProvider {
		return ErrSessionLimit
	}
	sl := newSlot()
	sl.wsid, sl.provider, sl.committed = w, p, true
	m.slots[w] = sl
	return nil
}

// SlotCount：該 provider 目前佔用的名額（reservation 亦計入；legacy slot 不計入）。
func (m *Manager) SlotCount(p contract.Provider) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.countLocked(p)
}

// ProviderOf：WSID → provider 的登錄查詢（含尚未 commit 的 reservation）。
func (m *Manager) ProviderOf(w WSID) (contract.Provider, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providerOfLocked(w)
}

// providerOfLocked：唯讀查詢，不建立任何 slot。closed 路徑用它補齊 drop 通知的
// provider（此時已不能走 committedSlotLocked）。
func (m *Manager) providerOfLocked(w WSID) (contract.Provider, bool) {
	sl, ok := m.slots[w]
	if !ok {
		return "", false
	}
	return sl.provider, true
}

// committedSlotLocked：新入口唯一的 slot 解析路徑——只讀不建。未 commit 或不存在
// 一律 ErrSessionNotFound（廢除舊 slotLocked 的「讀取時隱式建立」）。
func (m *Manager) committedSlotLocked(w WSID) (*slot, error) {
	sl, ok := m.slots[w]
	if !ok || !sl.committed {
		return nil, ErrSessionNotFound
	}
	return sl, nil
}

// legacyWSIDLocked：LegacyWSID 專用——「讀取時隱式建立」的 per-provider fallback
// slot。legacy slot 不計入 countLocked（見其 doc）。
func (m *Manager) legacyWSIDLocked(p contract.Provider) WSID {
	if w, ok := m.legacy[p]; ok {
		return w
	}
	w := WSID("legacy-" + string(p))
	sl := newSlot()
	sl.provider, sl.committed, sl.isLegacy = p, true, true
	m.slots[w], m.legacy[p] = sl, w
	return w
}

// LegacyWSID：provider → legacy slot WSID。Task 9 刪掉全部 provider-keyed 入口之後
// 它只剩一個呼叫端：app 層 legacyWSIDFor 的**最後順位**（全新安裝、registry 空、
// 該 provider 沒有 live host）。那個順位需要一個「保證對 WSID 入口可解析」的 WSID
// ——legacy slot 是惰性建立的，光靠字面 key 猜不到、也可能還不存在，故保留
// legacyWSIDLocked 的「讀取時隱式建立」語意。legacy slot 的 sl.wsid 留空，envelope
// 的 workspace_session_id 因此與 M3a 完全一致。與 legacyWSIDFor 一起在 Task 26 刪除。
func (m *Manager) LegacyWSID(p contract.Provider) WSID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.legacyWSIDLocked(p)
}

// newSessionLocked：flush 該 slot 殘留 queue（掛舊 task）→ 換代 → 重設。
func (m *Manager) newSessionLocked(sl *slot, taskID string) {
	m.flushLocked(sl)
	sl.gen++
	sl.submitting, sl.fromNewSession = nil, false
	sl.phase, sl.endTok, sl.resetTok = phaseIdle, nil, nil
	sl.reducer.Reset()
	sl.taskID = taskID
	sl.totalCost, sl.totalUsage = 0, contract.Usage{}
}

func (m *Manager) NewSession(w WSID, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	m.newSessionLocked(sl, taskID)
	return nil
}

// BeginNewSessionSubmit：StartSession 的單一 ownership 交易（per slot）。
func (m *Manager) BeginNewSessionSubmit(w WSID, taskID string) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return SubmissionID{}, err
	}
	return m.beginNewSessionSubmitLocked(sl, taskID)
}

func (m *Manager) beginNewSessionSubmitLocked(sl *slot, taskID string) (SubmissionID, error) {
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
	m.newSessionLocked(sl, taskID)
	sl.seq++
	id := SubmissionID{gen: sl.gen, seq: sl.seq}
	sl.submitting, sl.fromNewSession = &id, true
	sl.phase = phaseStarting
	return id, nil
}

// BeginEndSession：進入 ending 並取得 token（per slot）。
func (m *Manager) BeginEndSession(w WSID) (SessionToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionToken{}, ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return SessionToken{}, err
	}
	return m.beginEndSessionLocked(sl)
}

func (m *Manager) beginEndSessionLocked(sl *slot) (SessionToken, error) {
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

// endingOwnerLocked：ending 階段的 token 比對（Cancel／Finish／IntoReset 共用）。
func endingOwnerLocked(sl *slot, t SessionToken) error {
	if sl.phase != phaseEnding || sl.endTok == nil || *sl.endTok != t {
		return ErrStaleSession
	}
	return nil
}

// CancelEndSession：ending → active 復原（teardown 前）；stale token no-op error。
func (m *Manager) CancelEndSession(w WSID, t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	return m.cancelEndSessionLocked(sl, t)
}

func (m *Manager) cancelEndSessionLocked(sl *slot, t SessionToken) error {
	if err := endingOwnerLocked(sl, t); err != nil {
		return err
	}
	sl.phase = phaseActive
	sl.endTok = nil
	return nil
}

// FinishEndSession：收尾完成；stale token 一律 no-op error。
func (m *Manager) FinishEndSession(w WSID, t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	return m.finishEndSessionLocked(sl, t)
}

func (m *Manager) finishEndSessionLocked(sl *slot, t SessionToken) error {
	if err := endingOwnerLocked(sl, t); err != nil {
		return err
	}
	sl.phase = phaseIdle
	sl.endTok = nil
	return nil
}

// BeginReset：idle → resetting（NewSession 於無 active session 時的入口）。
func (m *Manager) BeginReset(w WSID) (ResetToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ResetToken{}, ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return ResetToken{}, err
	}
	return m.beginResetLocked(sl)
}

func (m *Manager) beginResetLocked(sl *slot) (ResetToken, error) {
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
func (m *Manager) FinishEndSessionIntoReset(w WSID, t SessionToken) (ResetToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return ResetToken{}, err
	}
	return m.finishEndSessionIntoResetLocked(sl, t)
}

func (m *Manager) finishEndSessionIntoResetLocked(sl *slot, t SessionToken) (ResetToken, error) {
	if err := endingOwnerLocked(sl, t); err != nil {
		return ResetToken{}, err
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
func (m *Manager) FinishReset(w WSID, t ResetToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	return m.finishResetLocked(sl, t)
}

func (m *Manager) finishResetLocked(sl *slot, t ResetToken) error {
	if sl.phase != phaseResetting || sl.resetTok == nil || *sl.resetTok != t {
		return ErrStaleReset
	}
	sl.phase = phaseIdle
	sl.resetTok = nil
	return nil
}

// IsActive：該 workspace session 是否處於 active phase；未 commit／不存在的 WSID
// 一律 false。
func (m *Manager) IsActive(w WSID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	return err == nil && sl.phase == phaseActive
}

// Emit：session-scope 事件的唯一出口——WSID 定址＋provider fail-loud 檢查。
func (m *Manager) Emit(w WSID, ev contract.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先：單一 UI stream_error、不寫 sink
		m.emitClosedDroppedLocked(string(ev.Kind), string(ev.Provider), w)
		return ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	if ev.Provider != sl.provider {
		return ErrProviderMismatch
	}
	m.queueOrEmitLocked(sl, pendingEntry{ev: ev})
	return nil
}

// queueOrEmitLocked：submitting 中只 queue 該 slot（跨 slot 不互相阻塞），
// 否則直接送出。所有 session-scope 事件出口（Emit／approval）共用這一段。
func (m *Manager) queueOrEmitLocked(sl *slot, e pendingEntry) {
	if sl.submitting != nil {
		sl.pendingBuf = append(sl.pendingBuf, e)
		return
	}
	m.emitLocked(sl, e.ev)
	if e.resolveApprove {
		if st, changed := sl.reducer.ResolveApproval(); changed {
			m.emitStateLocked(sl, e.ev.Provider, e.ev.SessionID, st)
		}
	}
}

// BeginSubmit：既有 session 的後續輪（僅該 slot phaseActive 允許）。
func (m *Manager) BeginSubmit(w WSID) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return SubmissionID{}, err
	}
	return m.beginSubmitLocked(sl)
}

func (m *Manager) beginSubmitLocked(sl *slot) (SubmissionID, error) {
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
func (m *Manager) AcceptSubmit(w WSID, id SubmissionID, sessionID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	return m.acceptSubmitLocked(sl, id, sessionID, text)
}

func (m *Manager) acceptSubmitLocked(sl *slot, id SubmissionID, sessionID, text string) error {
	if err := m.checkOwnerLocked(sl, id); err != nil {
		return err
	}
	if sl.fromNewSession { // StartSession 路徑：provider 接受即 session 存活
		sl.phase = phaseActive
		sl.sessionGen = id.gen
	}
	sl.submitting, sl.fromNewSession = nil, false
	m.emitLocked(sl, contract.Event{Provider: sl.provider, Kind: contract.KindMessage,
		Role: "user", SessionID: sessionID, Text: text,
		Raw: []byte(`{"source":"workbench_user_input"}`)})
	m.flushLocked(sl)
	return nil
}

func (m *Manager) RejectSubmit(w WSID, id SubmissionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	return m.rejectSubmitLocked(sl, id)
}

func (m *Manager) rejectSubmitLocked(sl *slot, id SubmissionID) error {
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

// EmitWorkspace：workspace scope 事件出口（gate/binding 等）——不帶 provider/session
// id、不碰任何 slot／reducer，只走 writeAndEmitLocked 取得檔案級單調 event_id。
func (m *Manager) EmitWorkspace(kind string, bindings []contract.Binding, payload any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先
		m.emitClosedDroppedLocked(kind, "", "") // workspace lane 不計入 slot（§3.1.6）
		return
	}
	raw, _ := json.Marshal(payload)
	now := time.Now()
	env := contract.Envelope{
		EventID:  contract.NewULID(now),
		TS:       now.UTC().Format(time.RFC3339Nano),
		Scope:    "workspace",
		Kind:     kind,
		Bindings: bindings,
		Payload:  raw,
	}
	m.writeAndEmitLocked(env)
}

// EmitAssist：SpecAssist 隔離 one-shot 的事件出口（Task 11；Stage A §5.1）。與
// EmitWorkspace 同構——同一 mutex 下走 writeAndEmitLocked 取得檔案級單調 event_id
// ＋稽核，但**不碰任何 slot／reducer／totals**：assist 事件雖 scope="session" 且帶
// provider，卻不得污染 provider session slot（前端依 purpose="spec_assist" 二次分流，
// 不進 reducer／Chat／totals／unread）。correlationID 貫穿該 one-shot 全部事件
// （init／delta／message／result／stream_error／abort），供前端關聯與稽核。additive-only。
func (m *Manager) EmitAssist(provider contract.Provider, correlationID, purpose string, evs ...contract.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先——與其他出口一致（單一 UI stream_error、不寫 sink）
		m.emitClosedDroppedLocked(string(contract.KindStreamError), string(provider), "") // assist one-shot 無 slot
		return
	}
	for _, ev := range evs {
		env := contract.Wrap(ev, "") // rich 內容保留＋新的檔案級單調 event_id
		env.Scope = "session"
		env.Provider = string(provider)
		env.CorrelationID = correlationID
		env.Purpose = purpose
		m.writeAndEmitLocked(env) // 不觸及 slot：無 totals／reducer／state_change
	}
}

// approvalRequestEvent／approvalDecisionEvent：新舊入口共用的事件組裝。
func approvalRequestEvent(p contract.Provider, sessionID, toolName string, raw []byte) pendingEntry {
	return pendingEntry{ev: contract.Event{Provider: p, Kind: contract.KindApproval,
		SessionID: sessionID, Text: toolName, Raw: raw}}
}

func approvalDecisionEvent(p contract.Provider, sessionID, decision, reason string) pendingEntry {
	return pendingEntry{ev: contract.Event{Provider: p, Kind: contract.KindApprovalDecision,
		SessionID: sessionID, Text: decision, Thinking: reason,
		Raw: []byte(`{"decision":"` + decision + `"}`)}, resolveApprove: true}
}

func (m *Manager) EmitApprovalRequest(w WSID, sessionID, toolName string, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先：drop 通知不因 WSID 是否存在而異
		p, _ := m.providerOfLocked(w)
		m.emitClosedDroppedLocked(string(contract.KindApproval), string(p), w)
		return ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	m.queueOrEmitLocked(sl, approvalRequestEvent(sl.provider, sessionID, toolName, raw))
	return nil
}

func (m *Manager) EmitApprovalDecision(w WSID, sessionID, decision, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // closed 最先：drop 通知不因 WSID 是否存在而異
		p, _ := m.providerOfLocked(w)
		m.emitClosedDroppedLocked(string(contract.KindApprovalDecision), string(p), w)
		return ErrClosed
	}
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return err
	}
	m.queueOrEmitLocked(sl, approvalDecisionEvent(sl.provider, sessionID, decision, reason))
	return nil
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
	env.WorkspaceSessionID = string(sl.wsid) // legacy slot 為空值：遷移期輸出不變
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
	env.WorkspaceSessionID = string(sl.wsid)
	m.writeAndEmitLocked(env)
}

func (m *Manager) writeAndEmitLocked(env contract.Envelope) {
	// closed 已在所有公開入口最先攔截；emitLocked 不可能於 closed 後執行。
	receipt, sinkErr := m.cfg.Sink.Write(env)
	if sinkErr == nil && m.cfg.Index != nil {
		// §3.5.4 latch-but-not-block（凍結取捨）：index 只是快取，**它壞掉
		// 絕不能讓 provider turn 失敗**。Observe 的契約是自行 latch degraded
		// 並回 nil，所以這裡既不看回傳值、也不改變後續任何一步——事件照樣
		// 出 UI、呼叫端照樣拿到成功。degraded 的可觀測性由 Config.Notify 那
		// 條路徑負責（呼叫端會據此排程重建），不是靠這裡的錯誤傳播。
		//
		// 只在 sinkErr == nil 時餵：receipt 在寫入失敗時是零值，餵給
		// index 會把 offset 0 當成真實位置，直接汙染 checkpoint。
		//
		// 位置固定在「同一個 mutex 內、Sink.Write 之後」：§3.5.7 的重建接回
		// 要靠「取得這把鎖 = 凍結 audit 進度且 index 不會再前移」才有原子性，
		// 把 Observe 移到鎖外就沒有那個保證了。
		_ = m.cfg.Index.Observe(env, receipt)
	}
	m.cfg.Emit(env) // 原 envelope 先出（ID 較小），合成事件後出——輸出序嚴格遞增
	if sinkErr != nil {
		if m.auditErr == nil {
			m.auditErr = sinkErr
		}
		m.cfg.Emit(contract.Envelope{ // 只走 UI，不回寫 sink（防遞迴）
			EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Provider: env.Provider, Kind: string(contract.KindStreamError),
			WorkspaceSessionID: env.WorkspaceSessionID, // §3.1.5：sink 失敗通知要能歸戶
			Error:              "audit sink: " + sinkErr.Error(),
		})
	}
}

// emitClosedDroppedLocked：close 後的唯一輸出——單一 UI stream_error（不寫 sink）。
// wsid 帶上呼叫端已知的 workspace session（§3.1.5），讓前端能把 drop 通知歸戶到
// 對應的 session tab；workspace／assist lane 無 slot，傳空值。
func (m *Manager) emitClosedDroppedLocked(kind, provider string, wsid WSID) {
	m.cfg.Emit(contract.Envelope{
		EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: provider, Kind: string(contract.KindStreamError),
		WorkspaceSessionID: string(wsid),
		Error:              "manager closed: event dropped (kind=" + kind + ")",
	})
}

func (m *Manager) Totals(w WSID) (float64, contract.Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return 0, contract.Usage{}, err
	}
	return sl.totalCost, sl.totalUsage, nil
}

func (m *Manager) State(w WSID) (contract.SessionState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, err := m.committedSlotLocked(w)
	if err != nil {
		return "", err
	}
	return sl.reducer.Current(), nil
}

func (m *Manager) AuditErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auditErr
}

// EmitLocker：序列化「audit append ＋ index.Observe」的那一把鎖，供 replay
// index 的 runtime 重建（§3.5.7 第 3 步）取得——重建的原子接回正是建立在
// 「持有這把鎖期間不可能有新的 append」之上。
//
// 呼叫端契約：持有它時**不得呼叫任何 Manager 方法**（sync.Mutex 不可重入，
// 當場自我死鎖），也不得跨 goroutine 傳遞 Unlock 的責任。目前唯一呼叫端是
// App 的 rebuild orchestrator。
func (m *Manager) EmitLocker() sync.Locker { return &m.mu }

// Closed 回報 Manager 是否已 Close（shutdown 收束序 assert 用）。
func (m *Manager) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// Close：同一 mutex、closed 旗標。對**所有** slot 執行 abort+flush（sink 關閉前
// queue 事件全部落 audit、無 user envelope、fail-loud 通知），之後才關 sink。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	for _, sl := range m.slots {
		if sl.submitting != nil || len(sl.pendingBuf) > 0 {
			n := len(sl.pendingBuf)
			sl.submitting, sl.fromNewSession = nil, false
			if sl.phase == phaseStarting {
				sl.phase = phaseIdle
			}
			m.flushLocked(sl) // sink 尚未關：queue 事件全數落 audit + UI
			m.cfg.Emit(contract.Envelope{
				EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
				Provider: string(sl.provider), Kind: string(contract.KindStreamError),
				WorkspaceSessionID: string(sl.wsid),
				Error:              fmt.Sprintf("manager closing during pending submission: %d queued events flushed without user acceptance", n),
			})
		}
	}
	m.closed = true
	return m.cfg.Sink.Close()
}
