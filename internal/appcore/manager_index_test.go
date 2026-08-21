package appcore

import (
	"errors"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// fakeIndex：TurnIndex 的最小替身。fail 非 nil 時每次 Observe 都回錯——用來
// 驗「index 壞掉不得讓 provider turn 失敗」（§3.5.4 latch-but-not-block）。
type fakeIndex struct {
	mu        sync.Mutex
	seen      []AppendReceipt
	envs      []contract.Envelope
	fail      error
	reenter   func() // 於 Observe 內回呼（模擬 Notify 再打 Manager 入口）
	reentered bool
}

func (f *fakeIndex) Observe(env contract.Envelope, r AppendReceipt) error {
	f.mu.Lock()
	f.seen = append(f.seen, r)
	f.envs = append(f.envs, env)
	re := f.reenter
	f.mu.Unlock()
	if re != nil && !f.reentered {
		f.reentered = true
		re()
	}
	return f.fail
}

func (f *fakeIndex) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// TestIndexObservesEveryAuditedEnvelope：每一筆真的落進 audit 的 envelope 都
// 必須被餵給 index，且順序與 sink 寫入順序一致（index 一律以 AppendReceipt
// 為準，§3.5.2）。含 workspace lane——它沒有 WSID，但仍佔 audit byte range，
// index 必須看得到才不會讓 checkpoint 落在錯的位置。
func TestIndexObservesEveryAuditedEnvelope(t *testing.T) {
	idx := &fakeIndex{}
	sink := &memSink{}
	m := New(Config{Sink: sink, Emit: func(contract.Envelope) {}, Index: idx})
	x := newTM(t, m)

	if err := m.Emit(x.ws[contract.ProviderClaude], contract.Event{
		Provider: contract.ProviderClaude, Kind: contract.KindDelta, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	m.EmitWorkspace("gate:requested", nil, map[string]string{"k": "v"})

	idx.mu.Lock()
	defer idx.mu.Unlock()
	// delta 會再導出一筆 state_change（streaming），故共 3 筆 audit 寫入。
	if len(idx.envs) != len(sink.rows) || len(idx.envs) != 3 {
		t.Fatalf("index 必須看到每一筆落盤事件：index=%d sink=%d", len(idx.envs), len(sink.rows))
	}
	for i := range idx.envs {
		if idx.envs[i].EventID != sink.rows[i].EventID || idx.seen[i].EventID != sink.rows[i].EventID {
			t.Fatalf("第 %d 筆順序／receipt 不符：index=%s sink=%s",
				i, idx.envs[i].EventID, sink.rows[i].EventID)
		}
	}
}

// TestIndexFailureDoesNotFailProviderTurn（§3.5.4，凍結取捨）：index 寫入失敗
// 一律不回錯給呼叫端、不影響 UI 輸出、不寫額外的 stream_error——latch 與通知
// 是 index 自己那條路徑的責任。
func TestIndexFailureDoesNotFailProviderTurn(t *testing.T) {
	idx := &fakeIndex{fail: errors.New("index disk full")}
	var ui []contract.Envelope
	m := New(Config{Sink: &memSink{}, Emit: func(e contract.Envelope) { ui = append(ui, e) }, Index: idx})
	x := newTM(t, m)

	if err := m.Emit(x.ws[contract.ProviderClaude], contract.Event{
		Provider: contract.ProviderClaude, Kind: contract.KindDelta, Text: "hi"}); err != nil {
		t.Fatalf("index 失敗不得讓 provider turn 失敗：%v", err)
	}
	if m.AuditErr() != nil {
		t.Fatalf("index 失敗不是 audit 失敗，不得 latch auditErr：%v", m.AuditErr())
	}
	for _, e := range ui {
		if e.Kind == string(contract.KindStreamError) {
			t.Fatalf("index 失敗不得自行合成 stream_error（那是 Notify 的責任）：%+v", e)
		}
	}
	if len(ui) != 2 { // delta ＋ 導出的 state_change 照常送 UI
		t.Fatalf("UI 輸出不得因 index 失敗而改變：%d", len(ui))
	}
}

// TestIndexNotFedWhenSinkWriteFails：sink 寫入失敗時 receipt 是零值，餵給
// index 會把 offset 0 當成真實位置、直接汙染 checkpoint。
func TestIndexNotFedWhenSinkWriteFails(t *testing.T) {
	idx := &fakeIndex{}
	m := New(Config{Sink: &memSink{fail: errors.New("disk gone")},
		Emit: func(contract.Envelope) {}, Index: idx})
	x := newTM(t, m)

	_ = m.Emit(x.ws[contract.ProviderClaude], contract.Event{
		Provider: contract.ProviderClaude, Kind: contract.KindDelta, Text: "hi"})
	if got := idx.count(); got != 0 {
		t.Fatalf("寫入失敗的事件不得進 index：%d", got)
	}
	if m.AuditErr() == nil {
		t.Fatal("audit 失敗仍必須 latch（fail loud 不因 index 接線而改變）")
	}
}

// TestIndexObserveRunsUnderEmitLock：§3.5.7 的重建接回靠「取得 EmitLocker
// ＝ 凍結 audit 進度且 index 不會再前移」。這條不變量只有在 Observe 於同一
// 把鎖內被呼叫時才成立——用 TryLock 直接證明，不靠註解宣稱。
func TestIndexObserveRunsUnderEmitLock(t *testing.T) {
	idx := &fakeIndex{}
	m := New(Config{Sink: &memSink{}, Emit: func(contract.Envelope) {}, Index: idx})
	x := newTM(t, m)

	locker := m.EmitLocker().(*sync.Mutex)
	free := false
	idx.reenter = func() {
		if locker.TryLock() {
			free = true
			locker.Unlock()
		}
	}
	if err := m.Emit(x.ws[contract.ProviderClaude], contract.Event{
		Provider: contract.ProviderClaude, Kind: contract.KindDelta, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if free {
		t.Fatal("Observe 必須在 emit mutex 內被呼叫（否則重建接回沒有原子性）")
	}
}
