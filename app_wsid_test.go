package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	removeErr error
	mutateErr error
	syncErr   error
	layoutErr error

	layout wsregistry.Layout

	entries              map[string]wsregistry.Entry
	deletedUncommitted   bool
	removedWithTombstone bool
	syncs                int
	uncertain            bool
}

// Uncertain：鏡射 wsregistry.Store 的 uncertain latch（rename 成功但 directory
// sync 失敗）。stub 恆回 false 會讓所有「latch 期間必須拒絕」的斷言假綠。
func (s *stubRegistry) Uncertain() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uncertain
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
	// 只在該 wsid 真的存在時才記旗標——真實 Store 的 DeleteUncommitted 對不存在
	// 的 wsid 是冪等 no-op，無條件設旗標會讓「Put 寫錯 WSID」的實作照樣通過
	// TestCommitFailureRollsBackRegistryWithoutTombstone。
	if _, ok := s.entries[wsid]; !ok {
		return nil
	}
	s.deletedUncommitted = true
	delete(s.entries, wsid)
	return nil
}

// Remove：鏡射真實 wsregistry.Store 的 tombstone 語意（review round-2 Minor
// #2）——留下 entry、填 RemovedAt／RemoveReason，不整筆刪除。之前直接
// delete(s.entries, wsid) 讓 stub 的 Get() 語意與 production 相反（真實
// Store.Remove 之後 Get 仍回 ok=true，帶 tombstone 欄位），會誤導任何斷言
// 「移除後 Get 仍看得到 tombstone entry」的測試。
func (s *stubRegistry) Remove(wsid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removeErr != nil {
		return s.removeErr
	}
	e, ok := s.entries[wsid]
	if !ok {
		e = wsregistry.Entry{WSID: wsid}
	}
	e.RemovedAt, e.RemoveReason = "removed", reason
	if s.entries == nil {
		s.entries = map[string]wsregistry.Entry{}
	}
	s.entries[wsid] = e
	s.removedWithTombstone = true
	return nil
}

// mutate：鏡射 wsregistry.Store.mutate 的哨兵語意（entry 不存在／已 tombstone
// 各回一個可辨識的錯誤）。stub 若對這兩種情況靜默成功，所有「已移除的 WSID 不得
// 寫回續聊身分」的斷言都會變成假綠。
func (s *stubRegistry) mutate(wsid string, fn func(*wsregistry.Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutateErr != nil {
		return s.mutateErr
	}
	e, ok := s.entries[wsid]
	if !ok {
		return fmt.Errorf("%w: %q", wsregistry.ErrEntryNotFound, wsid)
	}
	if e.RemovedAt != "" {
		return fmt.Errorf("%w: %q", wsregistry.ErrTombstoned, wsid)
	}
	fn(&e)
	s.entries[wsid] = e
	return nil
}

func (s *stubRegistry) CommitResume(wsid, resumeSessionID, taskLabel string) error {
	return s.mutate(wsid, func(e *wsregistry.Entry) {
		if resumeSessionID != "" {
			e.ResumeSessionID = resumeSessionID
		}
		if taskLabel != "" {
			e.TaskLabel = taskLabel
		}
	})
}

func (s *stubRegistry) SetResume(wsid, resumeSessionID string) error {
	return s.mutate(wsid, func(e *wsregistry.Entry) { e.ResumeSessionID = resumeSessionID })
}

func (s *stubRegistry) ResetView(wsid, viewStartEventID string) error {
	return s.mutate(wsid, func(e *wsregistry.Entry) {
		e.ViewStartEventID, e.ResumeSessionID = viewStartEventID, ""
	})
}

func (s *stubRegistry) ClearLegacyTranscript(wsid string) error {
	return s.mutate(wsid, func(e *wsregistry.Entry) { e.LegacyTranscript = false })
}

// SetLayout／Layout：鏡射真實 Store 的 pane pins 深拷貝語意（見 store.go
// SetLayout 的說明）。layoutErr 注入**非 latch** 的一般寫入失敗（磁碟滿、權限、
// rename 失敗），由 TestSetPaneLayoutReportsPlainWriteFailure 使用——latch 分支
// 走的是 uncertain 旗標，兩者的處置不同（前者原樣回報、後者早退），要分開驗。
func (s *stubRegistry) SetLayout(l wsregistry.Layout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.layoutErr != nil {
		return s.layoutErr
	}
	s.layout = wsregistry.Layout{Pins: append([]string(nil), l.Pins...), Focused: l.Focused}
	return nil
}

func (s *stubRegistry) Layout() wsregistry.Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wsregistry.Layout{Pins: append([]string(nil), s.layout.Pins...), Focused: s.layout.Focused}
}

func (s *stubRegistry) Get(wsid string) (wsregistry.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[wsid]
	return e, ok
}

// Live：排除 tombstone（RemovedAt 非空），同 wsregistry.Store.Live 的語意。
func (s *stubRegistry) Live() []wsregistry.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wsregistry.Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.RemovedAt != "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Sync：計次（shutdown 總序的 registry_sync 那一步要能驗到「真的 Sync 過」，
// 光看步驟名發出來不夠——見 TestShutdownFollowsFrozenOrder）。
func (s *stubRegistry) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs++
	return s.syncErr
}

func (s *stubRegistry) syncCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs
}

// TestCreateSessionHappyPathWritesEntryAndHoldsSlot：stub 級的快樂路徑——
// 鎖住 Put 的欄位對映（WSID／Provider／TaskLabel 不得對調或寫錯來源）與
// 「commit 成功後名額確實被佔住」。end-to-end 版本要等 Task 6 接上真實 registry。
func TestCreateSessionHappyPathWritesEntryAndHoldsSlot(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	wsid, err := a.CreateSession("claude", "my-task")
	if err != nil {
		t.Fatalf("快樂路徑不得回錯：%v", err)
	}
	if wsid == "" {
		t.Fatal("成功建立必須回傳非空 WSID")
	}
	e, ok := reg.Get(wsid)
	if !ok {
		t.Fatalf("registry 必須以回傳的 WSID 為 key 存有該 entry：%q", wsid)
	}
	if e.WSID != wsid {
		t.Fatalf("Entry.WSID 與回傳值不一致：%q vs %q", e.WSID, wsid)
	}
	if e.Provider != "claude" {
		t.Fatalf("Entry.Provider 錯誤：%q", e.Provider)
	}
	if e.TaskLabel != "my-task" {
		t.Fatalf("Entry.TaskLabel 錯誤：%q", e.TaskLabel)
	}
	if e.CreatedAt == "" {
		t.Fatal("Entry.CreatedAt 必須寫入")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("commit 成功後應佔住一個名額：%d", got)
	}
	if reg.deletedUncommitted || reg.removedWithTombstone {
		t.Fatal("快樂路徑不得觸發任何回滾")
	}
}

// TestCreateSessionRejectsUnknownProvider：未知 provider 若放行會被寫進 durable
// registry，重啟後 RestoreDormant 拿到無人能接手的 provider、該 entry 永久卡住。
func TestCreateSessionRejectsUnknownProvider(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	if _, err := a.CreateSession("clade", "t"); err == nil {
		t.Fatal("未知 provider 必須 fail loud")
	}
	if len(reg.Live()) != 0 {
		t.Fatalf("未知 provider 不得寫進 registry：%+v", reg.Live())
	}
	if got := a.manager.SlotCount("clade"); got != 0 {
		t.Fatalf("未知 provider 不得佔名額：%d", got)
	}
}

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
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi claude", "", "claude-degraded", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(wsidStr(t, a, "claude")) })
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
	if err := a.SendMessage(wsidStr(t, a, "claude"), "still works"); err != nil {
		t.Fatalf("degraded 不得影響既有 session：%v", err)
	}
}

// TestCreateSessionRejectedByShutdownBarrier：§3.1「shutdown 以 beginAppTxn 為
// 柵欄：shutdown 開始後拒絕新 app txn，故不會插入 reservation 與 persist 之間」
// ——spec §5.1 最後一條（Reserve × shutdown barrier）。Task 31 驗收時發現這條
// 凍結契約在 App 層沒有任何測試釘住（其他入口有 TestShutdownGateBlocksLateCodexStart
// ／TestShutdownGateBlocksLateEnsure，但都守不到 CreateSession 這一格）。
//
// **窗口刻意停在 shutdown 的第一步**（`reject_new_txn`：shuttingDown 已翻 true、
// Manager 尚未 Close）。若改成「shutdown 全部跑完後才呼叫 CreateSession」，
// Manager.ReserveSession 的 ErrClosed 會結構性兜底——拿掉 beginAppTxn 那道柵欄
// 測試照樣綠，等於什麼都沒守。下面的 Reserve／AbortCreate 探針就是這個窗口成立
// 與否的反向守門。
func TestCreateSessionRejectedByShutdownBarrier(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg

	entered := make(chan struct{})
	release := make(chan struct{})
	a.hookShutdownStep = func(step string) {
		if step != "reject_new_txn" {
			return
		}
		close(entered)
		<-release
	}
	shutDone := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(shutDone) }()
	<-entered // 柵欄已升起、shutdown 其餘步驟尚未開始

	// 反向守門：Manager 必須還開著，否則本測試被 ErrClosed 兜底（見上方 doc）。
	_, tok, err := a.manager.ReserveSession("claude")
	if err != nil {
		t.Fatalf("窗口不成立——Manager 已 Close，本測試會被 ErrClosed 兜底：%v", err)
	}
	if err := a.manager.AbortCreate(tok); err != nil {
		t.Fatal(err)
	}

	if _, err := a.CreateSession("claude", "late"); err == nil ||
		!strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("shutdown 開始後不得再建立 session（§3.1 app txn 柵欄）：%v", err)
	}
	if got := reg.Live(); len(got) != 0 {
		t.Fatalf("被柵欄拒絕的建立不得留下 registry entry：%v", got)
	}
	if got := a.manager.SlotCount("claude"); got != 0 {
		t.Fatalf("被柵欄拒絕的建立不得吃名額：%d", got)
	}

	close(release)
	<-shutDone
}

// ---- Task 26：ListSessions（registry × working slot 的調和）----

// registry 是「有哪些 session」的權威、Manager slot 是「能不能操作」的權威，
// 兩邊不一致時必須據實呈現而不是把落單的那筆藏起來——否則稽核裡有事件、UI 卻
// 沒有這個 session，使用者也無從重試移除。
func TestListSessionsReconcilesRegistryWithSlots(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg

	live := mustCreate(t, a, "claude") // registry ＋ committed slot 都有
	// registry 有、Manager 沒有（RestoreDormant 未掛回去／名額釋放失敗留下的落差）
	orphan := "01ORPHAN00000000000000001"
	if err := reg.Put(wsregistry.Entry{WSID: orphan, Provider: "codex",
		TaskLabel: "orphaned", ResumeSessionID: "th-1", CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// tombstone 不得出現在清單（§3.6.1：removed 不顯示）
	buried := mustCreate(t, a, "codex")
	if err := reg.Remove(string(buried), "user_removed"); err != nil {
		t.Fatal(err)
	}

	got, err := a.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	byWSID := map[string]SessionInfo{}
	for _, s := range got {
		byWSID[s.WSID] = s
	}
	if len(got) != 2 {
		t.Fatalf("清單應為 registry 的 live entries：%+v", got)
	}
	if _, ok := byWSID[string(buried)]; ok {
		t.Fatal("tombstone 不得出現在清單")
	}
	if s := byWSID[string(live)]; !s.Available || s.State == "" || s.Provider != "claude" {
		t.Fatalf("有 slot 的 session 必須標為可操作並帶 phase：%+v", s)
	}
	if s := byWSID[orphan]; s.Available || s.State != "" {
		t.Fatalf("registry 有、Manager 無 slot 的 session 必須標為不可操作：%+v", s)
	}
	if s := byWSID[orphan]; s.TaskLabel != "orphaned" || s.ResumeSessionID != "th-1" {
		t.Fatalf("durable metadata 必須原樣帶出：%+v", s)
	}
	// 順序穩定（ULID 遞增），不隨 map 迭代漂移
	for i := 1; i < len(got); i++ {
		if got[i-1].WSID >= got[i].WSID {
			t.Fatalf("清單順序必須以 WSID 遞增：%+v", got)
		}
	}
}

// registry 尚未接線時必須 fail loud——回空清單會讓使用者以為 session 都不見了。
func TestListSessionsFailsLoudWithoutRegistry(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.ListSessions(); !errors.Is(err, errNoSessionRegistry) {
		t.Fatalf("registry 不可用必須 fail loud：%v", err)
	}
}
