package appcore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// all：memSink 目前收到的全部 envelope 快照（memSink 本體定義於 manager_test.go，
// 遷移期不得更動）。刻意不提供 last()——一次 Emit 可能產生 message ＋ reducer 合成的
// state_change 兩筆，只看最後一筆會斷言到非預期的那筆。
func (s *memSink) all() []contract.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.Envelope, len(s.rows))
	copy(out, s.rows)
	return out
}

func TestReserveSessionLimitIsAtomic(t *testing.T) {
	m := New(Config{Sink: &memSink{}})
	var ok, limited int64
	var wg sync.WaitGroup
	for range 5 {
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
	// result 會讓 reducer 進 done，因而**合成**一筆 state_change——上面那筆 role-less
	// message 對 reducer 是中性的，只發它就驗不到合成路徑有沒有填 WSID。
	if err := m.EmitWS(w, contract.Event{Kind: contract.KindResult, Provider: "claude", Raw: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	rows := sink.all()
	// §3.1.5：這兩次 emit 產生的**每一筆** envelope（含 reducer 合成的 state_change）
	// 都要帶 WSID，否則多 session 上線後前端無法歸戶到 session tab。
	for _, e := range rows {
		if e.WorkspaceSessionID != string(w) {
			t.Fatalf("每筆 envelope 都要填 WSID：kind=%q wsid=%q", e.Kind, e.WorkspaceSessionID)
		}
	}
	// 明確鎖定各自那筆，不依賴「最後一筆剛好是它」；同時確保合成路徑真的有被涵蓋。
	if got := lastOfKind(t, rows, "message").WorkspaceSessionID; got != string(w) {
		t.Fatalf("Emit 必須填 WSID：%q", got)
	}
	if got := lastOfKind(t, rows, string(contract.KindStateChange)).WorkspaceSessionID; got != string(w) {
		t.Fatalf("合成的 state_change 也必須填 WSID：%q", got)
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
