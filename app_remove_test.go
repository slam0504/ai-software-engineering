package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- M3b Task 22：移除＝tombstone＋名額釋放時點（§3.6.1-2） ----

// mustEmitTurn：直接經 Manager.Emit 送一個完整 turn（canonical user message →
// result，導出 terminal state_change=done——見 replayindex 的 turn boundary 狀
// 態機：applyTurnState 只認這兩個邊界）。不需要真的啟動 provider 子行程：這裡
// 只是要讓 events.jsonl／replay index 對這個 WSID 留下一筆真實審計紀錄，供
// TestRemovedTombstoneSurvivesRestartAndRebuild 驗證「移除後、即使 rebuild 從
// audit 重新掃到這個 WSID，也不得復活」（§3.6.1）。
func mustEmitTurn(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	p, ok := a.manager.ProviderOf(w)
	if !ok {
		t.Fatalf("mustEmitTurn: unknown wsid %s", w)
	}
	if err := a.manager.Emit(w, contract.Event{Provider: p, Kind: contract.KindMessage, Role: "user", Text: "hi"}); err != nil {
		t.Fatalf("mustEmitTurn user message: %v", err)
	}
	if err := a.manager.Emit(w, contract.Event{Provider: p, Kind: contract.KindResult}); err != nil {
		t.Fatalf("mustEmitTurn result: %v", err)
	}
}

// TestRemoveReleasesSlotOnlyAfterAllStepsSucceed（§3.6.2）：釋放名額（Manager 側
// 刪除 slot）必須在 teardown／lease finalize／tombstone persist 全部成功之後才
// 發生——不是「先扣、失敗再補」。這裡用 tombstone persist 失敗（wsReg.Remove 注
// 入錯誤）驅動：即使 teardown 已經真的把 claude 子行程收乾，名額仍必須保留。
func TestRemoveReleasesSlotOnlyAfterAllStepsSucceed(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	a.wsReg = &stubRegistry{removeErr: errors.New("tombstone persist failed")}
	if err := a.RemoveSession(string(w)); err == nil {
		t.Fatal("tombstone persist 失敗必須 fail loud")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("任一步失敗必須保留 slot（§3.6.2）：%d", got)
	}
}

// TestRemoveDeniesPendingApprovals（§3.6.3）：待移除 WSID 若有顯示中的
// approval，必須 fail-closed deny（reason=session_removed）——不是留給使用者
// 事後才發現「session 沒了、彈窗還在」。用 seedApproval 走真實 broker／socket
// 路徑（而不是直接操作 apprPending map），確定 deny 真的經 ResolveApproval 送回
// provider 端，不是只清了記憶體索引。
func TestRemoveDeniesPendingApprovals(t *testing.T) {
	a, ui := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	id := seedApproval(t, a, w)

	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if pa := a.pendingByID(id); pa != nil {
		t.Fatalf("移除必須清空該 WSID 的待核可登記：%+v", pa)
	}
	var found bool
	for _, env := range ui.findEnvKind("approval_decision") {
		if env.WorkspaceSessionID == string(w) && env.Text == "deny" && env.Thinking == "session_removed" {
			found = true
		}
	}
	if !found {
		t.Fatal("待核可必須以 reason=session_removed fail-closed deny 送出決定事件（§3.6.3）")
	}
}

// TestRemoveGateBlocksSideEffectsWhenNotRemovable（review round-2 Important）：
// 失敗的移除不得留下傷害——若 slot 有 in-flight submission（teardown 這次注定
// 會在 BeginEndSession 撞到 ErrSubmitActive），deny_approvals／cleanup_files
// 不得在那之前先做。用 a.manager.BeginSubmit 直接把 slot 推進
// 「submitting != nil」狀態（不必真的並行送出訊息），確定性地重現。
func TestRemoveGateBlocksSideEffectsWhenNotRemovable(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	id := seedApproval(t, a, w)
	mcpPath := filepath.Join(a.stateDir, "mcp-"+string(w)+".json")

	subID, err := a.manager.BeginSubmit(w)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveSession(string(w)); err == nil {
		t.Fatal("in-flight submission 期間移除必須 fail loud")
	}
	if pa := a.pendingByID(id); pa == nil {
		t.Fatal("失敗的移除不得 deny 既有 approval（§3.6.2：失敗不得留下副作用）")
	}
	if _, err := os.Stat(mcpPath); err != nil {
		t.Fatalf("失敗的移除不得刪除 MCP config：%v", err)
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("失敗的移除必須保留 slot：%d", got)
	}

	// 讓 slot 回到可正常收尾的狀態，t.Cleanup 的 EndSession 才收得乾淨。
	if err := a.manager.RejectSubmit(w, subID); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveOrderIsFrozen（§3.6.2）：六步凍結順序——釋放名額（decrement_count）
// 必須是最後一步。lease_finalize／cleanup_files 是獨立步驟探針，即使對 claude
// 而言 lease finalize 的實際工作已經包在 teardown（CloseSequence）內完成，順序
// 上仍必須排在 teardown 之後、cleanup_files 之前。
func TestRemoveOrderIsFrozen(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	var order []string
	a.hookRemoveStep = func(s string) { order = append(order, s) }
	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	want := []string{"deny_approvals", "teardown", "lease_finalize",
		"cleanup_files", "tombstone_persist", "decrement_count"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("釋放名額必須是最後一步（§3.6.2）：\n got=%v\nwant=%v", order, want)
	}
	// 步驟名有跑到不代表真的做了事：teardown 必須真的把 host 收乾並從 registry
	// 摘除（take-then-dispose），不是只呼叫了一個空的 hook。
	if h := a.hostFor(w); h != nil {
		t.Fatalf("teardown 之後 host 必須已從 registry 移除：%+v", h)
	}
}

// TestRemoveCleansUpPerWSIDMCPConfig（risk item 3：「Claude per-WSID socket／MCP
// config 的檔案數與清理」）：startClaude 寫入的 `mcp-<wsid>.json` 在 Task 22 之前
// 從未被刪除過——teardown 只處理 socket（broker.Close() 連帶 unlink），MCP
// config 是純粹的殘留檔。cleanup_files 步驟必須真的補上這個清理。
func TestRemoveCleansUpPerWSIDMCPConfig(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	mcpPath := filepath.Join(a.stateDir, "mcp-"+string(w)+".json")
	if _, err := os.Stat(mcpPath); err != nil {
		t.Fatalf("前置條件：啟動後 MCP config 必須存在：%v", err)
	}
	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("移除後 per-WSID MCP config 必須清除：%v", err)
	}
}

// TestRemovedTombstoneSurvivesRestartAndRebuild（§3.6.1）：tombstone 必須是
// durable 且對 replay index 重建免疫——不只是「registry 沒還原」，即使
// VerifyOrRebuild 重新從 audit 掃到這個 WSID，也不得把它復活成 live session。
// 用真實 wsregistry.Store（經 loadSessionRegistry 接線，非 stub）跨兩個
// newTestAppAt 實例模擬 app 重啟，因為要驗的正是磁碟上的 tombstone。
func TestRemovedTombstoneSurvivesRestartAndRebuild(t *testing.T) {
	dir := t.TempDir()
	a1 := newTestAppAt(t, dir)
	if _, err := a1.loadSessionRegistry(); err != nil {
		t.Fatal(err)
	}
	w, err := a1.CreateSession("codex", "t")
	if err != nil {
		t.Fatal(err)
	}
	mustEmitTurn(t, a1, appcore.WSID(w))
	if err := a1.RemoveSession(w); err != nil {
		t.Fatal(err)
	}
	// tombstone，不是刪除 record（§3.6.1）：entry 必須仍在磁碟上、帶
	// removed_at／remove_reason——不是被 DeleteUncommitted 那樣整筆抹掉。只驗
	// 「重啟不復活」測不出這個混用，因為兩種寫法在 Live() 之後看起來一樣。
	onDisk, ok := registryOnDisk(t, dir).Get(w)
	if !ok {
		t.Fatal("移除必須留 tombstone entry，不得整筆刪除（誤用 DeleteUncommitted）")
	}
	if onDisk.RemovedAt == "" || onDisk.RemoveReason == "" {
		t.Fatalf("tombstone 必須記 removed_at／remove_reason：%+v", onDisk)
	}

	a2 := newTestAppAt(t, dir)
	if live, rerr := a2.restoreSessions(); rerr != nil || len(live) != 0 {
		t.Fatalf("removed session 不得復活：live=%+v err=%v", live, rerr)
	}
	if verr := a2.replayIndex.VerifyOrRebuild(a2.eventsPath()); verr != nil {
		t.Fatal(verr)
	}
	if live, rerr := a2.restoreSessions(); rerr != nil || len(live) != 0 {
		t.Fatalf("index rebuild 看到 audit 中的 WSID 也不得復活（§3.6.1）：live=%+v err=%v", live, rerr)
	}
	if got := a2.manager.SlotCount("codex"); got != 0 {
		t.Fatalf("removed 不計 slot：%d", got)
	}
}

// TestRemoveXNewShareOwnershipToken（§3.6.2）：Remove × New 共用同一個
// provider-scoped ownership token——deterministic barrier。刻意留一個空 slot
// （佔 3/4），讓 Create 在無競態時「必定成功」：如此 Create 若立即失敗或未取得
// token，就是實作沒有共用 token，而不是被名額擋掉。
//
// review round-2：victim 必須是「最後一個 mustCreate 出來的」且真的
// mustStartClaude——若 victim 只是 dormant（不 mustStartClaude），
// endSessionFlowForWSID 會在 BeginEndSession 就撞到 ErrNoSession 提早
// return，teardown 整個閉包（CloseSequence 的 5s/10s 上界、takeHost、
// broker.Close()、releaseSockIndex）一次都不會被呼叫——等於沒把「teardown 內層
// 的鎖與上界」納進 Create 排隊等 token 的並行窗口，token 臨界區內只驗到最外層
// 的 crToken，測不到 teardown 內層是否也正確地在臨界區內執行。用「最後一個」
// 是沿用當初 legacyWSIDFor 解析順位的寫法；Task 26 之後 mustStartClaude 直接
// 帶 victim 的 WSID，選第幾個已不影響命中，保留原選擇不改動測試意圖。
func TestRemoveXNewShareOwnershipToken(t *testing.T) {
	a, _ := newTestApp(t)
	var hosts []appcore.WSID
	for k := 0; k < appcore.MaxSessionsPerProvider-1; k++ { // 佔 3/4，留一個空位
		hosts = append(hosts, mustCreate(t, a, "claude"))
	}
	victim := hosts[len(hosts)-1]
	mustStartClaude(t, a, victim)

	removeHoldingToken := make(chan struct{})
	releaseRemove := make(chan struct{})
	createWaitingForToken := make(chan struct{})
	var createTookTokenWhileRemoveHeld atomic.Bool

	a.hookRemoveHoldingToken = func() { close(removeHoldingToken); <-releaseRemove }
	a.hookCreateWaitingForToken = func() { close(createWaitingForToken) }
	a.hookCreateAcquiredToken = func() {
		if a.removeTokenHeldForTest() {
			createTookTokenWhileRemoveHeld.Store(true)
		}
	}

	removeErr := make(chan error, 1)
	go func() { removeErr <- a.RemoveSession(string(victim)) }()
	<-removeHoldingToken

	createErr := make(chan error, 1)
	go func() { _, err := a.CreateSession("claude", "new"); createErr <- err }()

	select { // Create 必須「在等 token」，而不是直接失敗或直接成功
	case <-createWaitingForToken:
	case err := <-createErr:
		t.Fatalf("Create 未等待共用 token 就返回（err=%v）——未序列化", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Create 未進場")
	}

	close(releaseRemove)
	if err := <-removeErr; err != nil {
		t.Fatal(err)
	}
	if err := <-createErr; err != nil { // 只讀一次
		t.Fatalf("Remove 放行後 Create 應成功（有空 slot）：%v", err)
	}
	if createTookTokenWhileRemoveHeld.Load() {
		t.Fatal("Create 在 Remove 仍持有 token 時取得 token＝未序列化")
	}
	if got := a.manager.SlotCount("claude"); got != appcore.MaxSessionsPerProvider-1 {
		t.Fatalf("移除一個＋新增一個，名額應維持 3：%d", got)
	}
	// victim 是真的 started session：teardown 必須真的把它收乾，不是卡在
	// hookRemoveHoldingToken 的窗口內被跳過。
	if h := a.hostFor(victim); h != nil {
		t.Fatalf("teardown 之後 host 必須已從 registry 移除：%+v", h)
	}
}

// ---- M3b Task 23：待核可時移除——全部 deny＋部分失敗保留 dormant slot（§3.6.3）----
//
// denyApprovalsForRemove／cleanupRemovedFiles 本身已在 Task 22（9efd9f3／
// 23f59f6）落地；這裡補的是 Task 22 沒寫到的兩個行為：(1) 多筆 pending
// approval 必須「每一筆都被嘗試」而不是處理第一筆就代表全部，(2) 其中一筆
// deny 失敗時，best-effort 仍要跑完（errors.Join 回傳、teardown／lease
// finalize 照常、slot 保留）。

// seedPendingApprovals：seedApproval（app_claude_multi_test.go）的多筆版本，走
// 真實 broker／socket——用於「全部成功」情境，才需要真的驗到 broker 端收到
// n 筆決定回覆；「部分失敗」情境改用 registerApproval 直接注入（見下方
// TestPartialDenyFailureKeepsDormantSlot 的 doc，理由是逾時／goroutine 洩漏
// 風險）。回傳依 id 字串排序後的清單——與 denyApprovalsForRemove 內部
// slices.Sort 的走訪順序一致，方便斷言處理順序。
func seedPendingApprovals(t *testing.T, a *App, w appcore.WSID, n int) []string {
	t.Helper()
	h := a.hostFor(w)
	if h == nil {
		t.Fatalf("no session host for %s", w)
	}
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("appr-%s-%d", w, i)
		ids[i] = id
		go func(id string) { // client 端等裁決回覆才收線，同 seedApproval
			conn, err := net.Dial("unix", h.sockPath)
			if err != nil {
				return
			}
			defer conn.Close()
			b, _ := json.Marshal(approval.Request{ID: id, ToolName: "Bash",
				Input: json.RawMessage(`{"command":"ls"}`)})
			if _, werr := conn.Write(append(b, '\n')); werr != nil {
				return
			}
			var d approval.Decision
			_ = json.NewDecoder(bufio.NewReader(conn)).Decode(&d)
		}(id)
	}
	for _, id := range ids {
		waitFor(t, "approval "+id+" registered", func() bool { return a.pendingByID(id) != nil })
	}
	sort.Strings(ids)
	return ids
}

// TestRemoveDeniesAllPendingApprovalsAndCleansFiles（§3.6.3／§3.3）：
// TestRemoveDeniesPendingApprovals（Task 22）只驗過單筆——n=1 測不出「每一筆
// 都要被 deny」，一筆處理完迴圈本來就結束了。這裡把待核可數量拉到 3，同時把
// TestRemoveCleansUpPerWSIDMCPConfig 沒驗過的 socket 檔（broker.Close() 內建
// unlink）一併釘住——risk item 3 原文明確點名「socket／MCP config 的檔案數與
// 清理」兩者，之前的測試只驗了後者。
func TestRemoveDeniesAllPendingApprovalsAndCleansFiles(t *testing.T) {
	a, ui := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	h := a.hostFor(w)
	sockPath, mcpPath := h.sockPath, h.mcpPath
	ids := seedPendingApprovals(t, a, w, 3)

	if err := a.RemoveSession(string(w)); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if pa := a.pendingByID(id); pa != nil {
			t.Fatalf("移除必須清空待核可登記 %s：%+v", id, pa)
		}
	}
	var denyCount int
	for _, env := range ui.findEnvKind("approval_decision") {
		if env.WorkspaceSessionID == string(w) && env.Text == "deny" && env.Thinking == "session_removed" {
			denyCount++
		}
	}
	if denyCount != 3 {
		t.Fatalf("3 筆待核可都必須以 reason=session_removed fail-closed deny 送出決定事件：got %d", denyCount)
	}
	for _, p := range []string{sockPath, mcpPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("移除必須清除 per-WSID 檔案 %s（§3.3）", p)
		}
	}
}

// TestPartialDenyFailureKeepsDormantSlot（§3.6.2／§3.6.3）：deny_approvals 是
// best-effort——單筆失敗不得讓其餘筆數陪葬（提早中斷），也不得讓移除因此靜默
// 假裝成功。用 registerApproval 直接注入（略過真實 broker／socket）：這條測
// 的是 denyApprovalsForRemove 的 orchestration（逐筆 recover＋errors.Join），
// 不是 broker 接線本身——後者已由 TestRemoveDeniesAllPendingApprovalsAndCleansFiles
// 覆蓋。若走真實 socket，被跳過那筆的 client 會卡在 broker 逾時（預設 120s）
// 才收到回覆，讓測試不必要地慢，且該 goroutine 在 host teardown 之後仍會留著
// 等逾時（broker.Close() 只停止 accept，不強制關已建立的連線）。
//
// removability gate（Task 22 review round-2，Manager.Removable）跑在 deny 之
// 前——先顯式呼叫一次確認 gate 這裡真的放行，確定這條測試測到的是 deny 分
// 支本身，不是被 gate 擋在門口就直接失敗（gate 擋下的情境已有
// TestRemoveGateBlocksSideEffectsWhenNotRemovable 覆蓋，兩者不得混淆）。
func TestPartialDenyFailureKeepsDormantSlot(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	if err := a.manager.Removable(w); err != nil {
		t.Fatalf("前置條件：gate 必須先放行，測的才是 deny 分支：%v", err)
	}

	const n = 3
	ids := make([]string, n)
	attempted := make([]atomic.Bool, n)
	for i := 0; i < n; i++ {
		i := i
		id := fmt.Sprintf("appr-%s-%d", w, i)
		ids[i] = id
		a.registerApproval(id, w, "claude", func(allow bool, reason string) error {
			attempted[i].Store(true)
			if i == 1 { // 中間那筆注入失敗——若實作在第一個錯誤就中斷，index 2 測得出來
				return errors.New("deny failed: injected test failure")
			}
			return nil
		})
	}

	err := a.RemoveSession(string(w))
	if err == nil {
		t.Fatal("deny 部分失敗不得靜默移除")
	}
	if !strings.Contains(err.Error(), "deny failed") {
		t.Fatalf("錯誤未完整 Join（看不到被注入的失敗原因）：%v", err)
	}
	for i, id := range ids { // 每一筆都必須被嘗試——不得在第一個錯誤就中斷
		if !attempted[i].Load() {
			t.Fatalf("approval %s（index %d）未被嘗試 deny", id, i)
		}
		if pa := a.pendingByID(id); pa != nil {
			t.Fatalf("approval %s 即使 deny 失敗，也必須從 pending 移除（不得卡死 UI）：%+v", id, pa)
		}
	}

	if h := a.hostFor(w); h != nil {
		t.Fatal("deny 部分失敗時仍須完成 teardown（§3.6.3）：host 必須已收尾")
	}
	if got := a.manager.SlotCount("claude"); got != 1 {
		t.Fatalf("必須保留 dormant slot 供重試移除：%d", got)
	}
	e, ok := a.wsReg.Get(string(w))
	if !ok || e.RemovedAt != "" {
		t.Fatalf("未成功移除時 registry entry 必須保留且不得標 tombstone：ok=%v entry=%+v", ok, e)
	}
}
