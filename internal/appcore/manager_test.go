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

func (s *memSink) Write(e contract.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.rows = append(s.rows, e)
	return nil
}
func (s *memSink) Close() error { return nil }

func newTestManager(sink *memSink) (*Manager, *[]contract.Envelope, *sync.Mutex) {
	var got []contract.Envelope
	var mu sync.Mutex
	m := New(Config{Sink: sink, Emit: func(e contract.Envelope) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}})
	return m, &got, &mu
}

// startActive：以 StartSession 交易把 manager 帶到 phaseActive，並完成 boot turn
// （Emit result → reducer 進 done）——多輪的真實前置狀態：下一輪在上一輪 result
// 解鎖後送出，user message 必須觸發 done → waiting。
func startActive(t *testing.T, m *Manager) {
	t.Helper()
	id, err := m.BeginNewSessionSubmit("task-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s0", "boot"); err != nil {
		t.Fatal(err)
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
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
	m, got, _ := newTestManager(&memSink{})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 1}})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 3}})
	if _, u := m.Totals(); u.InputTokens != 15 || u.OutputTokens != 3 { // snapshot 覆寫：非 25
		t.Fatalf("codex snapshot must overwrite: %+v", u)
	}
	m.NewSession("t2")
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.1, Usage: &contract.Usage{InputTokens: 5, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.2, Usage: &contract.Usage{InputTokens: 7, OutputTokens: 4}})
	cost, u := m.Totals()
	if u.InputTokens != 12 || u.OutputTokens != 6 || cost < 0.299 || cost > 0.301 { // per-turn 累加（Task 4 VERDICT）
		t.Fatalf("claude must accumulate: %+v cost=%v", u, cost)
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
	m := New(Config{Sink: &memSink{}, ClaudeUsageCumulative: true,
		Emit: func(e contract.Envelope) { mu.Lock(); got = append(got, e); mu.Unlock() }})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 5}})
	if _, u := m.Totals(); u.InputTokens != 15 || u.OutputTokens != 5 { // 覆寫：非 25
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
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 1, Usage: &contract.Usage{InputTokens: 9}})
	_, _ = m.BeginSubmit() // 殘留的 pending 也要被 reset 清掉
	m.NewSession("task-B")
	if cost, u := m.Totals(); cost != 0 || u.InputTokens != 0 {
		t.Fatal("totals must reset")
	}
	if m.State() != contract.StateIdle {
		t.Fatal("reducer must reset")
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	if m.State() != contract.StateStreaming { // reset 後事件直通
		t.Fatal("events must flow after reset")
	}
}

func TestSubmitCoordinatorOrdering(t *testing.T) { // provider 事件先到也不得排在 user 前
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got) // boot 事件基準線
	mu.Unlock()
	id, err := m.BeginSubmit()
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
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
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

func TestStartSessionOwnershipBarrier(t *testing.T) { // production path 的原子交易
	m, _, _ := newTestManager(&memSink{})
	var providerStarts atomic.Int32
	begin := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-begin
			id, err := m.BeginNewSessionSubmit("task-" + string(rune('0'+i)))
			if err != nil {
				results <- err
				return
			}
			providerStarts.Add(1) // 模擬 process/recorder/pump 建立
			results <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
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
	if !m.SessionActive() {
		t.Fatal("winner's session must be active")
	}
	if _, err := m.BeginNewSessionSubmit("task-next"); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("active session must reject new StartSession: %v", err)
	}
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit("task-next"); err != nil { // End 後可再開
		t.Fatal(err)
	}
}

func TestEndDuringStartingIsRejected(t *testing.T) { // deterministic 命中 starting
	m, _, _ := newTestManager(&memSink{})
	providerStarted := make(chan struct{})
	release := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		id, err := m.BeginNewSessionSubmit("task-A")
		if err != nil {
			startDone <- err
			return
		}
		close(providerStarted) // 模擬 process/recorder/pump 已建立
		<-release
		startDone <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	}()
	<-providerStarted // 確定命中 starting phase
	if _, err := m.BeginEndSession(); !errors.Is(err, ErrStartInProgress) {
		t.Fatalf("End during starting must be ErrStartInProgress, got %v", err)
	}
	close(release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if !m.SessionActive() {
		t.Fatal("session must be active after Accept")
	}
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
}

func TestCancelEndSessionRestoresActive(t *testing.T) { // ending 復原
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelEndSession(tok); err != nil { // teardown 前置檢查失敗 → 復原
		t.Fatal(err)
	}
	if !m.SessionActive() {
		t.Fatal("cancel must restore active phase")
	}
	if err := m.FinishEndSession(tok); !errors.Is(err, ErrStaleSession) { // 已 Cancel 的 token 失效
		t.Fatalf("finish after cancel must be stale: %v", err)
	}
	tok2, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok2); err != nil {
		t.Fatal(err)
	}
	if m.SessionActive() {
		t.Fatal("session must be inactive after finish")
	}
}

func TestStaleEndAfterNewSessionIsNoop(t *testing.T) { // 舊 End 不得清掉新 session
	m, _, _ := newTestManager(&memSink{})
	idA, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(idA, contract.ProviderClaude, "sA", "hi")
	tokA, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tokA); err != nil { // A 正常收尾
		t.Fatal(err)
	}
	idB, err := m.BeginNewSessionSubmit("task-B")
	if err != nil {
		t.Fatal(err)
	}
	_ = m.AcceptSubmit(idB, contract.ProviderClaude, "sB", "hi")
	if err := m.FinishEndSession(tokA); !errors.Is(err, ErrStaleSession) { // 延遲重放的舊 End
		t.Fatalf("stale end must error: %v", err)
	}
	if !m.SessionActive() { // B 不受影響
		t.Fatal("stale end must not deactivate new session")
	}
}

func TestSendThenEndBarrier(t *testing.T) { // pending submit 期間 End 必拒
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	id, err := m.BeginSubmit()
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{})
	release := make(chan struct{})
	acceptDone := make(chan error, 1)
	go func() {
		close(sent) // 模擬 provider call 已送出（尚未 Accept）
		<-release
		acceptDone <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "round")
	}()
	<-sent
	if _, err := m.BeginEndSession(); !errors.Is(err, ErrSubmitActive) { // teardown 不得與 pending submit 重疊
		t.Fatalf("End during pending submit must be ErrSubmitActive: %v", err)
	}
	close(release)
	if err := <-acceptDone; err != nil { // Accept 不受影響、無晚到寫入
		t.Fatal(err)
	}
	if err := EndSessionFlow(m, nil, func() error { return nil }); err != nil { // 之後可正常 End
		t.Fatal(err)
	}
	if m.SessionActive() {
		t.Fatal("session must end cleanly after accept")
	}
}

func TestEndThenSendRejected(t *testing.T) { // ending 期間 Send 必拒
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); !errors.Is(err, ErrEndInProgress) { // teardown 期間不得起新 request
		t.Fatalf("Send during ending must be ErrEndInProgress: %v", err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); !errors.Is(err, ErrNoSession) { // 結束後 Send 需先開新 session
		t.Fatalf("Send after end must be ErrNoSession: %v", err)
	}
}

func TestEndSessionFlowBusyThenRetry(t *testing.T) { // busy 不殘留 ending
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hi")
	busy := true
	teardowns := 0
	err := EndSessionFlow(m, func() bool { return busy }, func() error { teardowns++; return nil })
	if !errors.Is(err, ErrProviderBusy) {
		t.Fatalf("busy must surface: %v", err)
	}
	if teardowns != 0 {
		t.Fatal("teardown must not run when busy")
	}
	if !m.SessionActive() { // phase 恢復 active，不殘留 ending
		t.Fatal("busy end must restore active")
	}
	busy = false
	if err := EndSessionFlow(m, func() bool { return busy }, func() error { teardowns++; return nil }); err != nil {
		t.Fatalf("retry end must succeed: %v", err)
	}
	if teardowns != 1 || m.SessionActive() {
		t.Fatalf("teardown=%d active=%v", teardowns, m.SessionActive())
	}
}

func TestEndSessionFlowTeardownErrorStillFinishes(t *testing.T) {
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	boom := errors.New("teardown failed")
	err := EndSessionFlow(m, nil, func() error { return boom })
	if !errors.Is(err, boom) { // teardown 錯誤保留
		t.Fatalf("teardown error must surface: %v", err)
	}
	if m.SessionActive() { // 不殘留 ending / active——已 Finish
		t.Fatal("teardown error must still finish end")
	}
	if _, err := m.BeginNewSessionSubmit("task-B"); err != nil { // 可再開新 session
		t.Fatalf("new session after failed teardown: %v", err)
	}
}

func TestEndSessionFlowIdempotentNoSession(t *testing.T) {
	m, _, _ := newTestManager(&memSink{})
	if err := EndSessionFlow(m, nil, func() error {
		t.Fatal("teardown must not run without session")
		return nil
	}); err != nil {
		t.Fatalf("no-session end must be nil: %v", err)
	}
}

func TestApprovalDuringSubmitQueued(t *testing.T) { // approval 不繞過 coordinator
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit()
	m.EmitApprovalRequest(contract.ProviderCodex, "t1", "commandExecution", []byte(`{"k":"v"}`))
	mu.Lock()
	if len(*got) != base {
		mu.Unlock()
		t.Fatal("approval during pending must be queued, not emitted")
	}
	mu.Unlock()
	if m.State() == contract.StateAwaitingApproval { // side effect 也必須延後
		t.Fatal("reducer must not transition before flush")
	}
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
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
	if m.State() != contract.StateAwaitingApproval {
		t.Fatalf("final state = %s", m.State())
	}
}

func TestApprovalDecisionDuringSubmitQueued(t *testing.T) { // resolveApprove 分支
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	stateBefore := m.State()
	id, _ := m.BeginSubmit()
	m.EmitApprovalRequest(contract.ProviderCodex, "t1", "commandExecution", []byte(`{}`))
	m.EmitApprovalDecision(contract.ProviderCodex, "t1", "allow", "")
	if m.State() != stateBefore { // flush 前 reducer 不動
		t.Fatalf("reducer moved before flush: %s", m.State())
	}
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
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
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	begin := make(chan struct{})
	results := make(chan error, 2)
	ids := make(chan SubmissionID, 2)
	for range 2 {
		go func() {
			<-begin
			id, err := m.BeginSubmit()
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
	if err := m.RejectSubmit(winner); err != nil { // 贏家可正常結束
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); err != nil { // 結束後可再 Begin
		t.Fatal(err)
	}
}

func TestStaleAcceptAfterNewSessionIsNoop(t *testing.T) { // generation 失效
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	id, _ := m.BeginSubmit()
	m.NewSession("new-task") // 舊 ID 失效
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "stale hello"); !errors.Is(err, ErrStaleSubmission) {
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
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit()
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindSystemOther, Raw: []byte("{}")})
	if err := m.RejectSubmit(id); err != nil {
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
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
	id, _ := m.BeginSubmit()
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "round 2"); err != nil {
		t.Fatal(err)
	}
	if m.State() != contract.StateWaiting {
		t.Fatalf("after submit state = %s, want waiting", m.State())
	}
}

func TestApprovalFlowThroughEnvelopes(t *testing.T) {
	m, got, mu := newTestManager(&memSink{})
	m.EmitApprovalRequest(contract.ProviderClaude, "s1", "Bash", []byte(`{"k":"v"}`))
	if m.State() != contract.StateAwaitingApproval {
		t.Fatalf("state = %s", m.State())
	}
	m.EmitApprovalDecision(contract.ProviderClaude, "s1", "timeout", "")
	if m.State() != contract.StateToolRunning {
		t.Fatalf("timeout must leave awaiting: %s", m.State())
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
	m, got, mu := newTestManager(sink)
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
	m, got, mu := newTestManager(sink)
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
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, err := m.BeginSubmit()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hello there"); err != nil {
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
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, _ := m.BeginSubmit() // pending 中 Close
	_ = m.Close()
	mu.Lock()
	uiAfterClose := len(*got) // abort 通知已在此之前發出
	mu.Unlock()
	sink.mu.Lock()
	rowsAfterClose := len(sink.rows)
	sink.mu.Unlock()
	stateBefore := m.State()
	_, usageBefore := m.Totals()
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 99}})
	m.EmitApprovalRequest(contract.ProviderClaude, "s1", "Bash", []byte(`{}`))
	m.EmitApprovalDecision(contract.ProviderClaude, "s1", "allow", "")
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("accept after close must ErrClosed: %v", err)
	}
	if m.State() != stateBefore { // reducer 未動
		t.Fatal("state mutated after close")
	}
	if _, u := m.Totals(); u != usageBefore { // totals 未動
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
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, _ := m.BeginSubmit()
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindDelta,
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
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("late accept must ErrClosed: %v", err)
	}
}

func TestCloseEmitBarrier(t *testing.T) { // Close 與 Emit 交錯
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
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
	m, got, mu := newTestManager(&memSink{})
	m.NewSession("old-task")
	ch := make(chan contract.Event, 8)
	done := Pump(ch, m.Emit)
	ch <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")}
	ch <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")}
	close(ch) // provider 收尾（EndSession 的 Close → EOF）
	if err := WaitQuiesce(done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	m.NewSession("new-task") // quiesce 完成後才換代
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
