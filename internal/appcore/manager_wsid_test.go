package appcore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// last：memSink 最後一筆 envelope（memSink 本體定義於 manager_test.go，遷移期不得更動）。
func (s *memSink) last() contract.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return contract.Envelope{}
	}
	return s.rows[len(s.rows)-1]
}

func TestReserveSessionLimitIsAtomic(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	var ok, limited int64
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := m.ReserveSession("claude"); err == nil {
				atomic.AddInt64(&ok, 1)
			} else if errors.Is(err, ErrSessionLimit) {
				atomic.AddInt64(&limited, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 4 || limited != 1 {
		t.Fatalf("要恰 4 個 reservation：ok=%d limited=%d", ok, limited)
	}
	if got := m.SlotCount("claude"); got != 4 {
		t.Fatalf("reservation 當下即應計入名額：%d", got)
	}
}

func TestAbortCreateReleasesSlot(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	_, tok, err := m.ReserveSession("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AbortCreate(tok); err != nil {
		t.Fatal(err)
	}
	if got := m.SlotCount("codex"); got != 0 {
		t.Fatalf("Abort 後名額未退回：%d", got)
	}
	if err := m.AbortCreate(tok); !errors.Is(err, ErrStaleCreate) {
		t.Fatalf("重複 Abort 應為 stale：%v", err)
	}
}

func TestUnknownWSIDNeverCreatesSlot(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	if _, err := m.BeginNewSessionSubmitWS("no-such", "t"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("未知 WSID 應 ErrSessionNotFound（廢除隱式建立）：%v", err)
	}
	if got := m.SlotCount("claude") + m.SlotCount("codex"); got != 0 {
		t.Fatalf("查詢未知 WSID 不得吃名額：%d", got)
	}
}

func TestWSIDIsULID(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	w, _, _ := m.ReserveSession("claude")
	if len(string(w)) != 26 { // contract.NewULID 產生 26 字元 ULID
		t.Fatalf("WSID 必須是 contract.NewULID 產生的 ULID：%q", w)
	}
}

func TestEmitFillsWSIDAndRejectsProviderMismatch(t *testing.T) {
	sink := &memSink{}
	// UI 出口不是本測試關心的對象——用 sink 觀察即可；Config.Emit 是必填欄位，
	// New 不做 nil 退化（避免「忘記接 UI 出口」變成靜默降級），故此處明確接 no-op。
	m := New(Config{Sink: sink, Emit: func(contract.Envelope) {}})
	w, tok, _ := m.ReserveSession("claude")
	if err := m.CommitCreate(tok); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitWS(w, contract.Event{Kind: "message", Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := sink.last().WorkspaceSessionID; got != string(w) {
		t.Fatalf("Emit 必須填 WSID：%q", got)
	}
	if err := m.EmitWS(w, contract.Event{Kind: "message", Provider: "codex"}); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("provider 不符必須 fail loud：%v", err)
	}
}

func TestLegacyProviderEntryStillWorks(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	if _, err := m.BeginNewSessionSubmit("claude", "t"); err != nil {
		t.Fatalf("舊入口在遷移期間必須可用：%v", err)
	}
	if got := m.SlotCount("claude"); got != 0 {
		t.Fatalf("legacy slot 不得佔使用者名額：%d", got)
	}
}
