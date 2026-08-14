package main

import (
	"errors"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// stubRegistry：sessionRegistry 的測試替身——可注入 Put／DeleteUncommitted 失敗，
// 並記錄「回滾走的是 DeleteUncommitted 還是使用者移除的 Remove」。建立交易的補償
// 路徑一律走 DeleteUncommitted（不留 tombstone），removedWithTombstone 為 true 即
// 代表實作誤用了使用者移除路徑。
type stubRegistry struct {
	mu        sync.Mutex
	putErr    error
	deleteErr error

	entries              map[string]wsregistry.Entry
	deletedUncommitted   bool
	removedWithTombstone bool
}

func (s *stubRegistry) Put(e wsregistry.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	if s.entries == nil {
		s.entries = map[string]wsregistry.Entry{}
	}
	s.entries[e.WSID] = e
	return nil
}

func (s *stubRegistry) DeleteUncommitted(wsid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedUncommitted = true
	delete(s.entries, wsid)
	return nil
}

func (s *stubRegistry) Remove(wsid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removedWithTombstone = true
	return nil
}

func (s *stubRegistry) Get(wsid string) (wsregistry.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[wsid]
	return e, ok
}

func (s *stubRegistry) Live() []wsregistry.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wsregistry.Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out
}

func (s *stubRegistry) Sync() error { return nil }

func TestCreateSessionRollsBackOnPersistFailure(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{putErr: errors.New("disk full")}
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("persist 失敗必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("AbortCreate 應退回名額：%d", got)
	}
}

func TestCommitFailureRollsBackRegistryWithoutTombstone(t *testing.T) {
	a, _ := newTestApp(t)
	a.hookForceCommitCreateError = errors.New("injected commit failure")
	reg := &stubRegistry{}
	a.wsReg = reg
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("注入 commit 失敗必須 fail loud")
	}
	if !reg.deletedUncommitted {
		t.Fatal("回滾必須用 DeleteUncommitted（不得留 tombstone）")
	}
	if reg.removedWithTombstone {
		t.Fatal("建立失敗不得走使用者移除路徑")
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("rollback 成功後應 AbortCreate 退回名額：%d", got)
	}
	if a.createDegraded("claude") {
		t.Fatal("rollback 成功不得進 degraded")
	}
}

func TestCommitAndRollbackBothFailEnterDegraded(t *testing.T) {
	a, ui := newTestApp(t)
	// 既有 session 走 legacy 入口先建起來（degraded 不得影響它）——SendMessage
	// 需要真的有一個 active claude session，沿用 app_test.go 的 fake CLI 慣例。
	writeMultiTurnClaude(t, a)
	if err := a.StartSession("claude", "hi claude", "", "claude-degraded", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession("claude") })
	waitFor(t, "claude first result", func() bool { return len(ui.findEnvKind("result")) >= 1 })

	a.hookForceCommitCreateError = errors.New("injected commit failure")
	a.wsReg = &stubRegistry{deleteErr: errors.New("rollback persist failed")}
	before := a.manager.SlotCount("claude")
	if _, err := a.CreateSession("claude", "t"); err == nil {
		t.Fatal("必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != before+1 {
		t.Fatalf("雙失敗必須保留名額（不得 AbortCreate）：%d → %d", before, got)
	}
	if !a.createDegraded("claude") {
		t.Fatal("必須進 create-degraded latch")
	}
	if _, err := a.CreateSession("claude", "t2"); !errors.Is(err, errCreateDegraded) {
		t.Fatalf("degraded 期間必須拒絕新建：%v", err)
	}
	if a.createDegraded("codex") {
		t.Fatal("degraded 應 per-provider")
	}
	// 既有 session 不受影響：走 legacy 入口的既有路徑仍可送訊息
	if err := a.SendMessage("claude", "still works"); err != nil {
		t.Fatalf("degraded 不得影響既有 session：%v", err)
	}
}
