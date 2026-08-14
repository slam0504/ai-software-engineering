package appcore

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
)

type memSink struct {
	mu   sync.Mutex
	rows []contract.Envelope
	fail error
}

func (s *memSink) Write(e contract.Envelope) (AppendReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return AppendReceipt{}, s.fail
	}
	s.rows = append(s.rows, e)
	return AppendReceipt{EventID: e.EventID}, nil
}
func (s *memSink) Close() error { return nil }

// tm：本檔的 provider → WSID 解析層（M3b Task 9）。
//
// Manager 的 provider-keyed 入口已於 Task 9 刪除，但本檔驗的是 coordinator／
// lifecycle／slot 隔離／event_id 單調性等**語意**，不是定址方式。因此這裡只把
// 「取得 WSID 的方式」換成正式的 ReserveSession ＋ CommitCreate（每個 provider
// 一個 committed slot，兩個 provider 即兩個獨立 slot，與遷移前的 legacy slot 對
// 應關係逐一相同），其餘斷言逐字不動——語意覆蓋不因這次遷移而減少。
//
// 兩個 slot 在建構時就 commit（而非首次使用時惰性建立）：本檔有多個測試在
// Close() 之後仍呼叫 Emit／State／Totals，惰性建立會在 closed 之後撞上
// ErrClosed，把「驗 closed 行為」變成「驗 slot 建不出來」。
//
// Emit 刻意維持 void 簽名：Pump(ch, m.Emit) 需要 func(contract.Event)，而
// close 後 Emit 回 ErrClosed 的行為由 Manager 自己的 closed-drop 通知覆蓋
// （見 TestEmitAfterCloseNoStateMutation 對 stream_error 筆數的斷言）。
type tm struct {
	*Manager
	ws map[contract.Provider]WSID
}

func newTM(t *testing.T, m *Manager) *tm {
	t.Helper()
	x := &tm{Manager: m, ws: map[contract.Provider]WSID{}}
	for _, p := range []contract.Provider{contract.ProviderClaude, contract.ProviderCodex} {
		w, tok, err := m.ReserveSession(p)
		if err != nil {
			t.Fatalf("ReserveSession(%s): %v", p, err)
		}
		if err := m.CommitCreate(tok); err != nil {
			t.Fatalf("CommitCreate(%s): %v", p, err)
		}
		x.ws[p] = w
	}
	return x
}

func (x *tm) w(p contract.Provider) WSID { return x.ws[p] }

func (x *tm) Emit(ev contract.Event) { _ = x.Manager.Emit(x.w(ev.Provider), ev) }

func (x *tm) NewSession(p contract.Provider, taskID string) { _ = x.Manager.NewSession(x.w(p), taskID) }

func (x *tm) BeginNewSessionSubmit(p contract.Provider, taskID string) (SubmissionID, error) {
	return x.Manager.BeginNewSessionSubmit(x.w(p), taskID)
}

func (x *tm) BeginSubmit(p contract.Provider) (SubmissionID, error) {
	return x.Manager.BeginSubmit(x.w(p))
}

func (x *tm) AcceptSubmit(p contract.Provider, id SubmissionID, sessionID, text string) error {
	return x.Manager.AcceptSubmit(x.w(p), id, sessionID, text)
}

func (x *tm) RejectSubmit(p contract.Provider, id SubmissionID) error {
	return x.Manager.RejectSubmit(x.w(p), id)
}

func (x *tm) BeginEndSession(p contract.Provider) (SessionToken, error) {
	return x.Manager.BeginEndSession(x.w(p))
}

func (x *tm) CancelEndSession(p contract.Provider, t SessionToken) error {
	return x.Manager.CancelEndSession(x.w(p), t)
}

func (x *tm) FinishEndSession(p contract.Provider, t SessionToken) error {
	return x.Manager.FinishEndSession(x.w(p), t)
}

func (x *tm) FinishEndSessionIntoReset(p contract.Provider, t SessionToken) (ResetToken, error) {
	return x.Manager.FinishEndSessionIntoReset(x.w(p), t)
}

func (x *tm) BeginReset(p contract.Provider) (ResetToken, error) {
	return x.Manager.BeginReset(x.w(p))
}

func (x *tm) FinishReset(p contract.Provider, t ResetToken) error {
	return x.Manager.FinishReset(x.w(p), t)
}

func (x *tm) SessionActive(p contract.Provider) bool { return x.Manager.IsActive(x.w(p)) }

func (x *tm) Totals(p contract.Provider) (float64, contract.Usage) {
	cost, usage, err := x.Manager.Totals(x.w(p))
	if err != nil {
		panic("tm.Totals: " + err.Error()) // committed slot 不可能解析不到
	}
	return cost, usage
}

func (x *tm) State(p contract.Provider) contract.SessionState {
	st, err := x.Manager.State(x.w(p))
	if err != nil {
		panic("tm.State: " + err.Error())
	}
	return st
}

func (x *tm) EmitApprovalRequest(p contract.Provider, sessionID, toolName string, raw []byte) {
	_ = x.Manager.EmitApprovalRequest(x.w(p), sessionID, toolName, raw)
}

func (x *tm) EmitApprovalDecision(p contract.Provider, sessionID, decision, reason string) {
	_ = x.Manager.EmitApprovalDecision(x.w(p), sessionID, decision, reason)
}

// endFlow：EndSessionFlow 的 provider → WSID 轉接（理由同 tm 型別 doc）。
func (x *tm) endFlow(p contract.Provider, busy func() bool, teardown func() error) error {
	return EndSessionFlow(x.Manager, x.w(p), busy, teardown)
}

func newTestManager(t *testing.T, sink *memSink) (*tm, *[]contract.Envelope, *sync.Mutex) {
	t.Helper()
	var got []contract.Envelope
	var mu sync.Mutex
	m := New(Config{Sink: sink, Emit: func(e contract.Envelope) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}})
	return newTM(t, m), &got, &mu
}

// startActive：以 StartSession 交易把指定 provider 的 slot 帶到 phaseActive，
// 並完成 boot turn（Emit result → reducer 進 done）。
func startActive(t *testing.T, m *tm, p contract.Provider) {
	t.Helper()
	id, err := m.BeginNewSessionSubmit(p, "task-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(p, id, "s0", "boot"); err != nil {
		t.Fatal(err)
	}
	m.Emit(contract.Event{Provider: p, Kind: contract.KindResult, Raw: []byte("{}")})
}

func lastOfKind(t *testing.T, rows []contract.Envelope, kind string) contract.Envelope {
	t.Helper()
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == kind {
			return rows[i]
		}
	}
	t.Fatalf("no envelope of kind %q", kind)
	return contract.Envelope{}
}

func TestUsageSemantics(t *testing.T) {
	m, got, _ := newTestManager(t, &memSink{})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 1}})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 3}})
	if _, u := m.Totals(contract.ProviderCodex); u.InputTokens != 15 || u.OutputTokens != 3 { // snapshot 覆寫：非 25
		t.Fatalf("codex snapshot must overwrite: %+v", u)
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.1, Usage: &contract.Usage{InputTokens: 5, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.2, Usage: &contract.Usage{InputTokens: 7, OutputTokens: 4}})
	cost, u := m.Totals(contract.ProviderClaude)
	if u.InputTokens != 12 || u.OutputTokens != 6 || cost < 0.299 || cost > 0.301 { // per-turn 累加（Task 4 VERDICT）
		t.Fatalf("claude must accumulate: %+v cost=%v", u, cost)
	}
	if _, u := m.Totals(contract.ProviderCodex); u.InputTokens != 15 { // slot 隔離：claude 不動 codex
		t.Fatalf("codex totals must be untouched by claude: %+v", u)
	}
	last := lastOfKind(t, *got, "result")
	if last.Usage == nil || last.Usage.InputTokens != 12 {
		t.Fatalf("emitted usage must be cumulative snapshot: %+v", last.Usage)
	}
	if last.UsageSemantics != "session_total" { // per-turn 累加 → session_total
		t.Fatalf("semantics = %q", last.UsageSemantics)
	}
}

func TestUsageSemanticsCumulativeOverwrite(t *testing.T) { // ClaudeUsageCumulative=true 分支
	var got []contract.Envelope
	var mu sync.Mutex
	m := newTM(t, New(Config{Sink: &memSink{}, ClaudeUsageCumulative: true,
		Emit: func(e contract.Envelope) { mu.Lock(); got = append(got, e); mu.Unlock() }}))
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 5}})
	if _, u := m.Totals(contract.ProviderClaude); u.InputTokens != 15 || u.OutputTokens != 5 { // 覆寫：非 25
		t.Fatalf("cumulative mode must overwrite: %+v", u)
	}
	mu.Lock()
	defer mu.Unlock()
	last := lastOfKind(t, got, "result")
	if last.UsageSemantics != "provider_latest" {
		t.Fatalf("semantics = %q", last.UsageSemantics)
	}
}

func TestNewSessionResetsAtomically(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	startActive(t, m, contract.ProviderClaude)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 1, Usage: &contract.Usage{InputTokens: 9}})
	_, _ = m.BeginSubmit(contract.ProviderClaude) // 殘留的 pending 也要被 reset 清掉
	m.NewSession(contract.ProviderClaude, "task-B")
	if cost, u := m.Totals(contract.ProviderClaude); cost != 0 || u.InputTokens != 0 {
		t.Fatal("totals must reset")
	}
	if m.State(contract.ProviderClaude) != contract.StateIdle {
		t.Fatal("reducer must reset")
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	if m.State(contract.ProviderClaude) != contract.StateStreaming { // reset 後事件直通
		t.Fatal("events must flow after reset")
	}
}

func TestSubmitCoordinatorOrdering(t *testing.T) { // provider 事件先到也不得排在 user 前
	m, got, mu := newTestManager(t, &memSink{})
	startActive(t, m, contract.ProviderCodex)
	mu.Lock()
	base := len(*got) // boot 事件基準線
	mu.Unlock()
	id, err := m.BeginSubmit(contract.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // provider goroutine 在 acceptance 前送出 delta + completed
		defer wg.Done()
		m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "early"})
		m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindResult, Raw: []byte("{}")})
	}()
	wg.Wait() // 事件已抵達（進 queue）
	mu.Lock()
	if len(*got) != base {
		mu.Unlock()
		t.Fatal("queued events must not emit before acceptance")
	}
	mu.Unlock()
	if err := m.AcceptSubmit(contract.ProviderCodex, id, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	kinds := []string{}
	for _, e := range (*got)[base:] {
		kinds = append(kinds, e.Kind+"/"+e.Role)
	}
	// 完整凍結序列（boot turn 已 done）：user → waiting → queued provider events
	if !strings.HasPrefix(strings.Join(kinds, ","),
		"message/user,state_change/system,delta/assistant") {
		t.Fatalf("order broken: %v", kinds)
	}
	if (*got)[base+1].State != string(contract.StateWaiting) {
		t.Fatalf("first state_change must be waiting: %+v", (*got)[base+1])
	}
}

// M1.5：coordinator flush 的 user → waiting → init 序列中 init 必須中性。
func TestInitDoesNotRegressState(t *testing.T) {
	m, got, mu := newTestManager(t, &memSink{})
	id, err := m.BeginNewSessionSubmit(contract.ProviderCodex, "task-init")
	if err != nil {
		t.Fatal(err)
	}
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindInit,
		SessionID: "t1", Raw: []byte(`{"threadId":"t1"}`)}) // 入 queue
	if err := m.AcceptSubmit(contract.ProviderCodex, id, "t1", "hi"); err != nil {
		t.Fatal(err)
	}
	if m.State(contract.ProviderCodex) != contract.StateWaiting { // flush 後仍 waiting
		t.Fatalf("state after user→waiting→init = %s, want waiting", m.State(contract.ProviderCodex))
	}
	mu.Lock()
	defer mu.Unlock()
	var seq []string
	for _, e := range *got {
		if e.Kind == "state_change" {
			seq = append(seq, e.State)
		}
	}
	if len(seq) != 1 || seq[0] != string(contract.StateWaiting) { // 恰一個 state_change：waiting
		t.Fatalf("state_change sequence = %v, want [waiting] only", seq)
	}
}

func TestStartSessionOwnershipBarrier(t *testing.T) { // production path 的原子交易
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	var providerStarts atomic.Int32
	begin := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-begin
			id, err := m.BeginNewSessionSubmit(p, "task-"+string(rune('0'+i)))
			if err != nil {
				results <- err
				return
			}
			providerStarts.Add(1) // 模擬 process/recorder/pump 建立
			results <- m.AcceptSubmit(p, id, "s1", "hi")
		}(i)
	}
	close(begin)
	e1, e2 := <-results, <-results
	winners := 0
	for _, e := range []error{e1, e2} {
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrSubmitActive) && !errors.Is(e, ErrSessionActive) {
			t.Fatalf("loser must fail with ownership error, got %v", e)
		}
	}
	if winners != 1 || providerStarts.Load() != 1 {
		t.Fatalf("winners=%d starts=%d", winners, providerStarts.Load())
	}
	if !m.SessionActive(p) {
		t.Fatal("winner's session must be active")
	}
	if _, err := m.BeginNewSessionSubmit(p, "task-next"); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("active session must reject new StartSession: %v", err)
	}
	tok, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(p, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit(p, "task-next"); err != nil { // End 後可再開
		t.Fatal(err)
	}
}

func TestEndDuringStartingIsRejected(t *testing.T) { // deterministic 命中 starting
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	providerStarted := make(chan struct{})
	release := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		id, err := m.BeginNewSessionSubmit(p, "task-A")
		if err != nil {
			startDone <- err
			return
		}
		close(providerStarted) // 模擬 process/recorder/pump 已建立
		<-release
		startDone <- m.AcceptSubmit(p, id, "s1", "hi")
	}()
	<-providerStarted // 確定命中 starting phase
	if _, err := m.BeginEndSession(p); !errors.Is(err, ErrStartInProgress) {
		t.Fatalf("End during starting must be ErrStartInProgress, got %v", err)
	}
	close(release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if !m.SessionActive(p) {
		t.Fatal("session must be active after Accept")
	}
	tok, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(p, tok); err != nil {
		t.Fatal(err)
	}
}

func TestCancelEndSessionRestoresActive(t *testing.T) { // ending 復原
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	id, _ := m.BeginNewSessionSubmit(p, "task-A")
	_ = m.AcceptSubmit(p, id, "s1", "hi")
	tok, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelEndSession(p, tok); err != nil { // teardown 前置檢查失敗 → 復原
		t.Fatal(err)
	}
	if !m.SessionActive(p) {
		t.Fatal("cancel must restore active phase")
	}
	if err := m.FinishEndSession(p, tok); !errors.Is(err, ErrStaleSession) { // 已 Cancel 的 token 失效
		t.Fatalf("finish after cancel must be stale: %v", err)
	}
	tok2, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(p, tok2); err != nil {
		t.Fatal(err)
	}
	if m.SessionActive(p) {
		t.Fatal("session must be inactive after finish")
	}
}

func TestStaleEndAfterNewSessionIsNoop(t *testing.T) { // 舊 End 不得清掉新 session
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	idA, _ := m.BeginNewSessionSubmit(p, "task-A")
	_ = m.AcceptSubmit(p, idA, "sA", "hi")
	tokA, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(p, tokA); err != nil { // A 正常收尾
		t.Fatal(err)
	}
	idB, err := m.BeginNewSessionSubmit(p, "task-B")
	if err != nil {
		t.Fatal(err)
	}
	_ = m.AcceptSubmit(p, idB, "sB", "hi")
	if err := m.FinishEndSession(p, tokA); !errors.Is(err, ErrStaleSession) { // 延遲重放的舊 End
		t.Fatalf("stale end must error: %v", err)
	}
	if !m.SessionActive(p) { // B 不受影響
		t.Fatal("stale end must not deactivate new session")
	}
}

func TestSendThenEndBarrier(t *testing.T) { // pending submit 期間 End 必拒
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	id, err := m.BeginSubmit(p)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{})
	release := make(chan struct{})
	acceptDone := make(chan error, 1)
	go func() {
		close(sent) // 模擬 provider call 已送出（尚未 Accept）
		<-release
		acceptDone <- m.AcceptSubmit(p, id, "s1", "round")
	}()
	<-sent
	if _, err := m.BeginEndSession(p); !errors.Is(err, ErrSubmitActive) { // teardown 不得與 pending submit 重疊
		t.Fatalf("End during pending submit must be ErrSubmitActive: %v", err)
	}
	close(release)
	if err := <-acceptDone; err != nil { // Accept 不受影響、無晚到寫入
		t.Fatal(err)
	}
	if err := m.endFlow(p, nil, func() error { return nil }); err != nil { // 之後可正常 End
		t.Fatal(err)
	}
	if m.SessionActive(p) {
		t.Fatal("session must end cleanly after accept")
	}
}

func TestEndThenSendRejected(t *testing.T) { // ending 期間 Send 必拒
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	tok, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(p); !errors.Is(err, ErrEndInProgress) { // teardown 期間不得起新 request
		t.Fatalf("Send during ending must be ErrEndInProgress: %v", err)
	}
	if err := m.FinishEndSession(p, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(p); !errors.Is(err, ErrNoSession) { // 結束後 Send 需先開新 session
		t.Fatalf("Send after end must be ErrNoSession: %v", err)
	}
}

func TestEndSessionFlowBusyThenRetry(t *testing.T) { // busy 不殘留 ending
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderCodex
	id, _ := m.BeginNewSessionSubmit(p, "task-A")
	_ = m.AcceptSubmit(p, id, "t1", "hi")
	busy := true
	teardowns := 0
	err := m.endFlow(p, func() bool { return busy }, func() error { teardowns++; return nil })
	if !errors.Is(err, ErrProviderBusy) {
		t.Fatalf("busy must surface: %v", err)
	}
	if teardowns != 0 {
		t.Fatal("teardown must not run when busy")
	}
	if !m.SessionActive(p) { // phase 恢復 active，不殘留 ending
		t.Fatal("busy end must restore active")
	}
	busy = false
	if err := m.endFlow(p, func() bool { return busy }, func() error { teardowns++; return nil }); err != nil {
		t.Fatalf("retry end must succeed: %v", err)
	}
	if teardowns != 1 || m.SessionActive(p) {
		t.Fatalf("teardown=%d active=%v", teardowns, m.SessionActive(p))
	}
}

func TestEndSessionFlowTeardownErrorStillFinishes(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	id, _ := m.BeginNewSessionSubmit(p, "task-A")
	_ = m.AcceptSubmit(p, id, "s1", "hi")
	boom := errors.New("teardown failed")
	err := m.endFlow(p, nil, func() error { return boom })
	if !errors.Is(err, boom) { // teardown 錯誤保留
		t.Fatalf("teardown error must surface: %v", err)
	}
	if m.SessionActive(p) { // 不殘留 ending / active——已 Finish
		t.Fatal("teardown error must still finish end")
	}
	if _, err := m.BeginNewSessionSubmit(p, "task-B"); err != nil { // 可再開新 session
		t.Fatalf("new session after failed teardown: %v", err)
	}
}

func TestEndSessionFlowIdempotentNoSession(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	if err := m.endFlow(contract.ProviderClaude, nil, func() error {
		t.Fatal("teardown must not run without session")
		return nil
	}); err != nil {
		t.Fatalf("no-session end must be nil: %v", err)
	}
}

func TestApprovalDuringSubmitQueued(t *testing.T) { // approval 不繞過 coordinator
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderCodex
	startActive(t, m, p)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit(p)
	m.EmitApprovalRequest(p, "t1", "commandExecution", []byte(`{"k":"v"}`))
	mu.Lock()
	if len(*got) != base {
		mu.Unlock()
		t.Fatal("approval during pending must be queued, not emitted")
	}
	mu.Unlock()
	if m.State(p) == contract.StateAwaitingApproval { // side effect 也必須延後
		t.Fatal("reducer must not transition before flush")
	}
	if err := m.AcceptSubmit(p, id, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	kinds := []string{}
	for _, e := range (*got)[base:] {
		kinds = append(kinds, e.Kind+"/"+e.State)
	}
	// 完整凍結序列：user → waiting → approval → awaiting_approval
	want := []string{"message/", "state_change/waiting", "approval/", "state_change/awaiting_approval"}
	for i, w := range want {
		parts := strings.SplitN(w, "/", 2)
		if kinds[i] != parts[0]+"/"+parts[1] {
			t.Fatalf("order[%d] = %s, want %s (all: %v)", i, kinds[i], w, kinds)
		}
	}
	if m.State(p) != contract.StateAwaitingApproval {
		t.Fatalf("final state = %s", m.State(p))
	}
}

func TestApprovalDecisionDuringSubmitQueued(t *testing.T) { // resolveApprove 分支
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderCodex
	startActive(t, m, p)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	stateBefore := m.State(p)
	id, _ := m.BeginSubmit(p)
	m.EmitApprovalRequest(p, "t1", "commandExecution", []byte(`{}`))
	m.EmitApprovalDecision(p, "t1", "allow", "")
	if m.State(p) != stateBefore { // flush 前 reducer 不動
		t.Fatalf("reducer moved before flush: %s", m.State(p))
	}
	if err := m.AcceptSubmit(p, id, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var seq []string
	for _, e := range (*got)[base:] {
		if e.Kind == "state_change" {
			seq = append(seq, "state:"+e.State)
		} else {
			seq = append(seq, e.Kind)
		}
	}
	want := []string{"message", "state:waiting", "approval", "state:awaiting_approval",
		"approval_decision", "state:tool_running"} // 完整凍結序列
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
}

func TestBeginSubmitExclusiveBarrier(t *testing.T) { // 唯一 ownership
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	begin := make(chan struct{})
	results := make(chan error, 2)
	ids := make(chan SubmissionID, 2)
	for range 2 {
		go func() {
			<-begin
			id, err := m.BeginSubmit(p)
			results <- err
			if err == nil {
				ids <- id
			}
		}()
	}
	close(begin)
	e1, e2 := <-results, <-results
	if !((e1 == nil && errors.Is(e2, ErrSubmitActive)) || (e2 == nil && errors.Is(e1, ErrSubmitActive))) {
		t.Fatalf("exactly one Begin must win: %v / %v", e1, e2)
	}
	winner := <-ids
	if err := m.RejectSubmit(p, winner); err != nil { // 贏家可正常結束
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(p); err != nil { // 結束後可再 Begin
		t.Fatal(err)
	}
}

func TestStaleAcceptAfterNewSessionIsNoop(t *testing.T) { // generation 失效
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	id, _ := m.BeginSubmit(p)
	m.NewSession(p, "new-task") // 舊 ID 失效
	if err := m.AcceptSubmit(p, id, "s1", "stale hello"); !errors.Is(err, ErrStaleSubmission) {
		t.Fatalf("stale accept must error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *got {
		if e.Text == "stale hello" {
			t.Fatal("stale accept must not emit its user envelope")
		}
		if e.TaskID == "new-task" && e.Kind == "message" {
			t.Fatal("no message may carry new task from stale submission")
		}
	}
}

func TestRejectSubmitEmitsNoUser(t *testing.T) {
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit(p)
	m.Emit(contract.Event{Provider: p, Kind: contract.KindSystemOther, Raw: []byte("{}")})
	if err := m.RejectSubmit(p, id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range (*got)[base:] {
		if e.Role == "user" {
			t.Fatal("rejected submit must not emit user envelope")
		}
	}
	if len(*got) != base+1 { // queue 照常 flush
		t.Fatalf("queued events must flush on reject: %d", len(*got)-base)
	}
}

func TestUserMessageEntersWaiting(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	startActive(t, m, p)
	m.Emit(contract.Event{Provider: p, Kind: contract.KindResult, Raw: []byte("{}")})
	id, _ := m.BeginSubmit(p)
	if err := m.AcceptSubmit(p, id, "s1", "round 2"); err != nil {
		t.Fatal(err)
	}
	if m.State(p) != contract.StateWaiting {
		t.Fatalf("after submit state = %s, want waiting", m.State(p))
	}
}

func TestApprovalFlowThroughEnvelopes(t *testing.T) {
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	m.EmitApprovalRequest(p, "s1", "Bash", []byte(`{"k":"v"}`))
	if m.State(p) != contract.StateAwaitingApproval {
		t.Fatalf("state = %s", m.State(p))
	}
	m.EmitApprovalDecision(p, "s1", "timeout", "")
	if m.State(p) != contract.StateToolRunning {
		t.Fatalf("timeout must leave awaiting: %s", m.State(p))
	}
	mu.Lock()
	defer mu.Unlock()
	var kinds []string
	for _, e := range *got {
		kinds = append(kinds, e.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "approval") || !strings.Contains(joined, "approval_decision") {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestAuditFailureIsLoud(t *testing.T) {
	sink := &memSink{fail: errors.New("disk full")}
	m, got, mu := newTestManager(t, sink)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	if m.AuditErr() == nil {
		t.Fatal("audit error must latch")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawStreamErr bool
	for _, e := range *got {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "disk full") {
			sawStreamErr = true
		}
	}
	if !sawStreamErr {
		t.Fatal("sink failure must surface to UI as stream_error envelope")
	}
	for i := 1; i < len(*got); i++ { // fail-loud 路徑 ID 仍嚴格遞增
		if (*got)[i].EventID <= (*got)[i-1].EventID {
			t.Fatalf("event_id order broken in fail-loud path at %d", i)
		}
	}
}

func TestConcurrentEmitOrderAndAdjacency(t *testing.T) {
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			for range 25 {
				m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
			}
		}()
	}
	close(begin)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(*got) < 200 {
		t.Fatalf("events lost: %d", len(*got))
	}
	for i := 1; i < len(*got); i++ {
		if (*got)[i].EventID <= (*got)[i-1].EventID {
			t.Fatalf("event_id order broken at %d", i)
		}
	}
	if (*got)[0].Kind != "delta" || (*got)[1].Kind != "state_change" {
		t.Fatalf("state_change must be adjacent: %s,%s", (*got)[0].Kind, (*got)[1].Kind)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != len(*got) {
		t.Fatalf("audit rows %d != emitted %d", len(sink.rows), len(*got))
	}
}

func TestEmitUserMessage(t *testing.T) { // AcceptSubmit 產生的 user envelope 同源進 UI 與稽核
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	p := contract.ProviderClaude
	startActive(t, m, p)
	id, err := m.BeginSubmit(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(p, id, "s1", "hello there"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	e := (*got)[len(*got)-2] // 末二：user message（其後跟 done→waiting 的 state_change）
	if e.Role != "user" || e.Kind != "message" || e.Text != "hello there" || e.SessionID != "s1" {
		t.Fatalf("user envelope = %+v", e)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != len(*got) {
		t.Fatal("user message must reach audit too")
	}
}

func TestEmitAfterCloseNoStateMutation(t *testing.T) { // closed 最先、內部狀態不變
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	p := contract.ProviderClaude
	startActive(t, m, p)
	id, _ := m.BeginSubmit(p) // pending 中 Close
	_ = m.Close()
	mu.Lock()
	uiAfterClose := len(*got) // abort 通知已在此之前發出
	mu.Unlock()
	sink.mu.Lock()
	rowsAfterClose := len(sink.rows)
	sink.mu.Unlock()
	stateBefore := m.State(p)
	_, usageBefore := m.Totals(p)
	m.Emit(contract.Event{Provider: p, Kind: contract.KindDelta, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 99}})
	m.EmitApprovalRequest(p, "s1", "Bash", []byte(`{}`))
	m.EmitApprovalDecision(p, "s1", "allow", "")
	if err := m.AcceptSubmit(p, id, "s1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("accept after close must ErrClosed: %v", err)
	}
	if m.State(p) != stateBefore { // reducer 未動
		t.Fatal("state mutated after close")
	}
	if _, u := m.Totals(p); u != usageBefore { // totals 未動
		t.Fatal("totals mutated after close")
	}
	sink.mu.Lock()
	if len(sink.rows) != rowsAfterClose { // close 後 sink 不得再寫
		sink.mu.Unlock()
		t.Fatal("sink written after close")
	}
	sink.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	aborted := 0
	for _, e := range (*got)[:uiAfterClose] {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closing during pending submission") {
			aborted++
		}
	}
	if aborted != 1 {
		t.Fatalf("abort notice count = %d, want 1", aborted)
	}
	dropped := 0
	for _, e := range (*got)[uiAfterClose:] {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closed: event dropped") {
			dropped++
		} else if e.Role == "user" {
			t.Fatal("no user envelope after close")
		}
	}
	if dropped != 3 { // 每個被丟棄的入口各一個 fail-loud
		t.Fatalf("dropped stream_error count = %d, want 3", dropped)
	}
}

func TestCloseDuringPendingFlushesLoudly(t *testing.T) { // close 前入列事件不遺失
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	p := contract.ProviderClaude
	startActive(t, m, p)
	id, _ := m.BeginSubmit(p)
	m.Emit(contract.Event{Provider: p, Kind: contract.KindDelta,
		Raw: []byte("{}"), Text: "queued-before-close"})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	var inSink bool
	for _, r := range sink.rows {
		if r.Kind == "delta" && r.Text == "queued-before-close" {
			inSink = true
		}
	}
	sink.mu.Unlock()
	if !inSink { // flush 發生在 sink 關閉前：audit 不失事件
		t.Fatal("queued event must reach sink before close")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDelta, sawNotice bool
	for _, e := range *got {
		if e.Kind == "delta" && e.Text == "queued-before-close" {
			sawDelta = true
		}
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closing during pending submission") {
			sawNotice = true
		}
	}
	if !sawDelta || !sawNotice {
		t.Fatalf("flush+notice must both surface: delta=%v notice=%v", sawDelta, sawNotice)
	}
	if err := m.AcceptSubmit(p, id, "t1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("late accept must ErrClosed: %v", err)
	}
}

func TestCloseEmitBarrier(t *testing.T) { // Close 與 Emit 交錯
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			for range 20 {
				m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-begin
		_ = m.Close()
	}()
	close(begin)
	wg.Wait() // -race 驗證無競態；close 後的 Emit 走 fail-loud
	sink.mu.Lock()
	sinkRows := len(sink.rows)
	sink.mu.Unlock()
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	sink.mu.Lock()
	if len(sink.rows) != sinkRows {
		sink.mu.Unlock()
		t.Fatal("Emit after Close must not write sink")
	}
	sink.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	last := (*got)[len(*got)-1]
	if last.Kind != string(contract.KindStreamError) || !strings.Contains(last.Error, "closed") {
		t.Fatalf("post-close emit must surface stream_error: %+v", last)
	}
}

func TestPumpQuiesceBeforeNewSession(t *testing.T) { // 晚到事件不進新 task
	m, got, mu := newTestManager(t, &memSink{})
	p := contract.ProviderClaude
	m.NewSession(p, "old-task")
	ch := make(chan contract.Event, 8)
	done := Pump(ch, m.Emit)
	ch <- contract.Event{Provider: p, Kind: contract.KindDelta, Raw: []byte("{}")}
	ch <- contract.Event{Provider: p, Kind: contract.KindResult, Raw: []byte("{}")}
	close(ch) // provider 收尾（EndSession 的 Close → EOF）
	if err := WaitQuiesce(done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	m.NewSession(p, "new-task") // quiesce 完成後才換代
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *got {
		if e.TaskID == "new-task" {
			t.Fatalf("old pump event leaked into new task: %+v", e)
		}
		if e.TaskID != "old-task" && e.TaskID != "" {
			t.Fatalf("unexpected task id: %+v", e)
		}
	}
}

func TestWaitQuiesceTimeout(t *testing.T) {
	ch := make(chan contract.Event)
	done := Pump(ch, func(contract.Event) {})
	if err := WaitQuiesce(done, 50*time.Millisecond); err == nil { // channel 未關 → 逾時
		t.Fatal("quiesce must time out when pump still running")
	}
	close(ch)
	if err := WaitQuiesce(done, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestCloseSequenceOrderTimeoutAndStuck(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	log := func(s string) func() error {
		return func() error { mu.Lock(); calls = append(calls, s); mu.Unlock(); return nil }
	}
	fin := func(gotExit *ports.Exit) func(ports.Exit) error {
		return func(ex ports.Exit) error {
			mu.Lock()
			calls = append(calls, "finalize")
			mu.Unlock()
			*gotExit = ex
			return nil
		}
	}

	// 正常路徑：done 已關 → close,wait,finalize；finalize 收到 wait 的 Exit
	done := make(chan struct{})
	close(done)
	var finExit ports.Exit
	ex, err := CloseSequence(log("close"), done, time.Second, time.Second, log("terminate"),
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true, Code: 0} },
		fin(&finExit))
	if err != nil || !ex.Exited || ex.Code != 0 || !finExit.Exited || finExit.Code != 0 {
		t.Fatalf("normal: %v %+v %+v", err, ex, finExit)
	}
	if strings.Join(calls, ",") != "close,wait,finalize" {
		t.Fatalf("order = %v", calls)
	}

	// 逾時升級路徑：quiesce 逾時 → terminate → done 關 → wait(143) → finalize(143)
	calls = nil
	hung := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); close(hung) }()
	ex, err = CloseSequence(log("close"), hung, 30*time.Millisecond, 5*time.Second,
		func() error { mu.Lock(); calls = append(calls, "terminate"); mu.Unlock(); return nil },
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true, Code: 143} },
		fin(&finExit))
	if err == nil || !strings.Contains(err.Error(), "quiesce timeout") { // timeout 不被吞
		t.Fatalf("quiesce timeout must surface: %v", err)
	}
	if !ex.Exited || ex.Code != 143 || !finExit.Exited || finExit.Code != 143 { // meta ExitCode 資料路徑
		t.Fatalf("exit evidence = %+v / %+v", ex, finExit)
	}
	if strings.Join(calls, ",") != "close,terminate,wait,finalize" {
		t.Fatalf("timeout order = %v", calls)
	}

	// 卡死路徑：done 永不關 → 界限內回錯、不呼叫 wait、仍盡力 finalize（Exited=false）
	calls = nil
	stuck := make(chan struct{}) // 永不 close
	start := time.Now()
	_, err = CloseSequence(log("close"), stuck, 20*time.Millisecond, 30*time.Millisecond,
		log("terminate"),
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true} },
		fin(&finExit))
	if err == nil || !strings.Contains(err.Error(), "did not quiesce") {
		t.Fatalf("stuck path must error: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stuck path must return within bounds")
	}
	joined := strings.Join(calls, ",")
	if strings.Contains(joined, "wait") || !strings.Contains(joined, "finalize") {
		t.Fatalf("stuck path must skip wait but still finalize: %v", calls)
	}
	if finExit.Exited { // 未知不得偽裝 exit
		t.Fatalf("stuck path finalize must receive Exited=false: %+v", finExit)
	}
}

// ---- M1.5 新增：slot 隔離與 reset phase ----

func TestSlotsIsolateUsageAndState(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	// 交錯 emit：claude 累加、codex 覆寫
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.5, Usage: &contract.Usage{InputTokens: 3, OutputTokens: 1}})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 100, OutputTokens: 10}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 120, OutputTokens: 12}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.25, Usage: &contract.Usage{InputTokens: 4, OutputTokens: 2}})

	cost, u := m.Totals(contract.ProviderClaude)
	if u.InputTokens != 7 || u.OutputTokens != 3 || cost < 0.749 || cost > 0.751 {
		t.Fatalf("claude accumulate isolated: %+v cost=%v", u, cost)
	}
	if _, u := m.Totals(contract.ProviderCodex); u.InputTokens != 120 || u.OutputTokens != 12 {
		t.Fatalf("codex overwrite isolated: %+v", u)
	}
	if m.State(contract.ProviderClaude) != contract.StateDone {
		t.Fatalf("claude state = %s, want done", m.State(contract.ProviderClaude))
	}
	if m.State(contract.ProviderCodex) != contract.StateIdle { // usage 中性：codex 未動
		t.Fatalf("codex state = %s, want idle", m.State(contract.ProviderCodex))
	}
}

func TestCrossProviderSubmitDoesNotBlock(t *testing.T) {
	m, got, mu := newTestManager(t, &memSink{})
	a, b := contract.ProviderClaude, contract.ProviderCodex
	startActive(t, m, a)
	idA, err := m.BeginSubmit(a) // A pending
	if err != nil {
		t.Fatal(err)
	}
	// B 的事件不入 A 的 queue：直接 emit
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	m.Emit(contract.Event{Provider: b, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "b-free"})
	mu.Lock()
	if len(*got) != base+2 { // delta + state_change(streaming)：立即發出、未被 A queue
		mu.Unlock()
		t.Fatalf("B events must not queue behind A's pending submit: %d", len(*got)-base)
	}
	mu.Unlock()
	// B 可完成整輪 submit 交易
	idB, err := m.BeginNewSessionSubmit(b, "task-b")
	if err != nil {
		t.Fatalf("B lifecycle must not be blocked by A: %v", err)
	}
	if err := m.AcceptSubmit(b, idB, "t1", "hi-b"); err != nil {
		t.Fatal(err)
	}
	// A 的 user-first 順序仍成立
	m.Emit(contract.Event{Provider: a, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "a-queued"})
	mu.Lock()
	preAccept := len(*got)
	mu.Unlock()
	if err := m.AcceptSubmit(a, idA, "s1", "hi-a"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	seq := []string{}
	for _, e := range (*got)[preAccept:] {
		seq = append(seq, e.Kind+"/"+e.Role)
	}
	if !strings.HasPrefix(strings.Join(seq, ","), "message/user,state_change/system,delta/assistant") {
		t.Fatalf("A user-first broken: %v", seq)
	}
}

func TestPerSlotGenerationInvalidation(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	a, b := contract.ProviderClaude, contract.ProviderCodex
	startActive(t, m, a)
	startActive(t, m, b)
	idA, _ := m.BeginSubmit(a)
	idB, _ := m.BeginSubmit(b)
	m.NewSession(a, "task-a2") // 只失效 A
	if err := m.AcceptSubmit(a, idA, "s1", "stale"); !errors.Is(err, ErrStaleSubmission) {
		t.Fatalf("A must be stale: %v", err)
	}
	if err := m.AcceptSubmit(b, idB, "t1", "still valid"); err != nil { // B 不受影響
		t.Fatalf("B must stay valid: %v", err)
	}
}

func TestFileLevelEventIDMonotonicAcrossProviders(t *testing.T) {
	sink := &memSink{}
	m, _, _ := newTestManager(t, sink)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		p := contract.ProviderClaude
		if i%2 == 1 {
			p = contract.ProviderCodex
		}
		wg.Add(1)
		go func(p contract.Provider) {
			defer wg.Done()
			<-begin
			for range 25 {
				m.Emit(contract.Event{Provider: p, Kind: contract.KindDelta, Raw: []byte("{}")})
			}
		}(p)
	}
	close(begin)
	wg.Wait()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) < 200 {
		t.Fatalf("events lost: %d", len(sink.rows))
	}
	for i := 1; i < len(sink.rows); i++ { // 檔案級不變量：跨 provider 嚴格遞增
		if sink.rows[i].EventID <= sink.rows[i-1].EventID {
			t.Fatalf("file-level event_id order broken at %d (%s <= %s)",
				i, sink.rows[i].EventID, sink.rows[i-1].EventID)
		}
	}
}

func TestCloseAbortsAllSlots(t *testing.T) {
	sink := &memSink{}
	m, got, mu := newTestManager(t, sink)
	a, b := contract.ProviderClaude, contract.ProviderCodex
	startActive(t, m, a)
	startActive(t, m, b)
	_, _ = m.BeginSubmit(a)
	_, _ = m.BeginSubmit(b)
	m.Emit(contract.Event{Provider: a, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "qa"})
	m.Emit(contract.Event{Provider: b, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "qb"})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	var sawA, sawB bool
	for _, r := range sink.rows {
		if r.Text == "qa" {
			sawA = true
		}
		if r.Text == "qb" {
			sawB = true
		}
	}
	rows := len(sink.rows)
	sink.mu.Unlock()
	if !sawA || !sawB { // 兩個 slot 的 queue 都在 sink 關閉前 flush
		t.Fatalf("both slots must flush to sink: a=%v b=%v", sawA, sawB)
	}
	mu.Lock()
	notices := 0
	for _, e := range *got {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closing during pending submission") {
			notices++
		}
	}
	mu.Unlock()
	if notices != 2 { // 每個 slot 各一個 abort 通知
		t.Fatalf("abort notices = %d, want 2", notices)
	}
	m.Emit(contract.Event{Provider: a, Kind: contract.KindDelta, Raw: []byte("{}")})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != rows { // close 後 sink 不可寫
		t.Fatal("sink written after close")
	}
}

func TestConcurrentDualSessionLifecycle(t *testing.T) { // -race：跨 slot lifecycle 互不干擾
	m, _, _ := newTestManager(t, &memSink{})
	a, b := contract.ProviderClaude, contract.ProviderCodex
	startActive(t, m, b)
	begin := make(chan struct{})
	errA := make(chan error, 1)
	errB := make(chan error, 1)
	go func() {
		<-begin
		id, err := m.BeginNewSessionSubmit(a, "task-a")
		if err != nil {
			errA <- err
			return
		}
		errA <- m.AcceptSubmit(a, id, "s1", "hi")
	}()
	go func() {
		<-begin
		errB <- m.endFlow(b, nil, func() error { return nil })
	}()
	close(begin)
	if err := <-errA; err != nil {
		t.Fatalf("A start: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("B end: %v", err)
	}
	if !m.SessionActive(a) || m.SessionActive(b) {
		t.Fatalf("A active=%v B active=%v, want true/false", m.SessionActive(a), m.SessionActive(b))
	}
}

func TestResetPhaseTransitions(t *testing.T) { // M1.5 plan §5.4（第三輪 P1-1）
	m, _, _ := newTestManager(t, &memSink{})
	p := contract.ProviderClaude

	// idle → BeginReset → resetting → FinishReset → idle
	tok, err := m.BeginReset(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit(p, "t"); !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("start during reset: %v", err)
	}
	if err := m.FinishReset(p, tok); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishReset(p, tok); !errors.Is(err, ErrStaleReset) { // stale token no-op
		t.Fatalf("stale reset must error: %v", err)
	}

	// ending → FinishEndSessionIntoReset → resetting（原子、無 idle 縫隙）
	id, _ := m.BeginNewSessionSubmit(p, "task-A")
	_ = m.AcceptSubmit(p, id, "s1", "hi")
	sessTok, err := m.BeginEndSession(p)
	if err != nil {
		t.Fatal(err)
	}
	rtok, err := m.FinishEndSessionIntoReset(p, sessTok)
	if err != nil {
		t.Fatal(err)
	}
	// barrier：resetting 期間注入 Start 必得 ErrResetInProgress（縫隙驗證）
	if _, err := m.BeginNewSessionSubmit(p, "task-B"); !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("start injected between end and reset-finish must be rejected: %v", err)
	}
	if err := m.FinishEndSession(p, sessTok); !errors.Is(err, ErrStaleSession) { // 舊 session token 已耗盡
		t.Fatalf("session token must be consumed by IntoReset: %v", err)
	}
	if err := m.FinishReset(p, rtok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit(p, "task-B"); err != nil { // reset 完可開新 session
		t.Fatal(err)
	}
}

func TestResetBlocksAllLifecycleEntries(t *testing.T) {
	m, _, _ := newTestManager(t, &memSink{})
	a, b := contract.ProviderClaude, contract.ProviderCodex
	tok, err := m.BeginReset(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(a); !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("BeginSubmit: %v", err)
	}
	if _, err := m.BeginNewSessionSubmit(a, "t"); !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("BeginNewSessionSubmit: %v", err)
	}
	if _, err := m.BeginEndSession(a); !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("BeginEndSession: %v", err)
	}
	if _, err := m.BeginReset(a); !errors.Is(err, ErrResetInProgress) { // 第二個 New
		t.Fatalf("second BeginReset: %v", err)
	}
	// 另一 provider 的 slot 完全不受影響
	idB, err := m.BeginNewSessionSubmit(b, "task-b")
	if err != nil {
		t.Fatalf("B must be unaffected: %v", err)
	}
	if err := m.AcceptSubmit(b, idB, "t1", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishReset(a, tok); err != nil {
		t.Fatal(err)
	}
}
