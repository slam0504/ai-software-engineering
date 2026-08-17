package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// ---- M3b Task 8：Claude 遷入 sessionHost＋per-WSID socket／MCP ----

// mustCreate：建立一個 workspace session 並回傳 WSID（wsReg 用 stub——本檔驗的是
// per-WSID ownership，不是 registry 持久化）。
func mustCreate(t *testing.T, a *App, provider string) appcore.WSID {
	t.Helper()
	if a.wsReg == nil {
		a.wsReg = &stubRegistry{}
	}
	w, err := a.CreateSession(provider, "task-"+provider)
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", provider, err)
	}
	return appcore.WSID(w)
}

// mustStartClaude：走 production 的 exported binding 啟動 claude，並確認 host
// 掛在傳入的 WSID 上（Task 26 之後 binding 直接收 WSID，不再有解析層）。
func mustStartClaude(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	writeMultiTurnClaude(t, a)
	if err := a.StartSession(string(w), "hi", "", "", "task-"+string(w), ""); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if a.hostFor(w) == nil {
		t.Fatalf("host 必須掛在 StartSession 帶入的 WSID %s 上", w)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
}

// seedApprovalSeq：seedApproval 的 id 去重計數器——同一個 WSID 在單一測試內可能
// 被呼叫多次（例如 FIFO promotion 測試同一 session 送出兩筆待核可），純用 WSID
// 組 id 會撞號、讓後一筆蓋掉前一筆的 apprPending 登記。
var seedApprovalSeq atomic.Int64

// seedApproval：走真實 approval 路徑——dial 該 host 自己的 approval socket 送一筆
// 請求，等 pumpApprovals 把它登記進 apprPending，回傳 approval id。
func seedApproval(t *testing.T, a *App, w appcore.WSID) string {
	t.Helper()
	h := a.hostFor(w)
	if h == nil {
		t.Fatalf("no session host for %s", w)
	}
	id := "appr-" + string(w) + "-" + strconv.FormatInt(seedApprovalSeq.Add(1), 10)
	go func() { // client 端等裁決回覆才收線（與 mcp-approval 的行為一致）
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
	}()
	waitFor(t, "approval registered", func() bool { return a.pendingByID(id) != nil })
	return id
}

// §3.3 的根本問題迴歸：socket 與 MCP config 曾固定是 <stateDir>/approval.sock
// 與 mcp.json，第二個 session 啟動會直接覆寫第一個的檔案。兩個 claude session
// 必須各自擁有 socket／mcp／broker／子行程。
func TestTwoClaudeSessionsDoNotShareSocketOrMCP(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)
	w1, w2 := mustCreate(t, a, "claude"), mustCreate(t, a, "claude")

	commit1, err := a.startClaude(w1, "p1", "", "")
	if err != nil {
		t.Fatalf("startClaude(w1): %v", err)
	}
	t.Cleanup(func() { commit1(false) }) // abort 路徑：reaper 立即 teardown、收乾子行程
	commit2, err := a.startClaude(w2, "p2", "", "")
	if err != nil {
		t.Fatalf("startClaude(w2): %v", err)
	}
	t.Cleanup(func() { commit2(false) })

	h1, h2 := a.hostFor(w1), a.hostFor(w2)
	if h1 == nil || h2 == nil {
		t.Fatalf("兩個 WSID 都必須各自登記 sessionHost：%v %v", h1, h2)
	}
	if h1.sockPath == h2.sockPath || h1.mcpPath == h2.mcpPath {
		t.Fatal("第二個 session 會覆寫第一個（§3.3）")
	}
	if h1.broker == h2.broker || h1.sess == h2.sess {
		t.Fatal("broker／子行程必須 per-WSID")
	}
	for _, p := range []string{h1.sockPath, h2.sockPath, h1.mcpPath, h2.mcpPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("路徑未建立：%s", p)
		}
	}
	// 真正的隔離證據：w2 啟動之後，兩個 socket 都還要 dial 得到、各自把 approval
	// 登記回自己的 WSID。上面的指標比較在 per-host 架構下恆為真，擋不住「第二個
	// session 把第一個的 socket 蓋掉」——那正是 §3.3 要修的形狀。
	id1, id2 := seedApproval(t, a, w1), seedApproval(t, a, w2)
	if pa := a.pendingByID(id1); pa == nil || pa.wsid != w1 {
		t.Fatalf("w1 的 broker 在 w2 啟動後必須仍可用：%+v", pa)
	}
	if pa := a.pendingByID(id2); pa == nil || pa.wsid != w2 {
		t.Fatalf("w2 的 approval 必須回自己的 WSID：%+v", pa)
	}
	if err := a.ResolveApproval(id1, false, ""); err != nil {
		t.Fatalf("resolve w1: %v", err)
	}
	if err := a.ResolveApproval(id2, false, ""); err != nil {
		t.Fatalf("resolve w2: %v", err)
	}
}

// unix sockaddr 的 sun_path 上限約 104 bytes（macOS／Linux 同量級）。newTestApp
// 刻意用 /tmp 短路徑迴避這條限制（見 app_test.go 內註解），因此一般測試撐不到
// 上限；production 的 resolveWorkspace() 用 `<cwd 或 home>/.workbench`，中等深度
// 的專案目錄就有 70-90 bytes，socket 檔名只要多幾十 bytes 就會 bind 失敗、
// Claude session 整個開不起來。本測試把 stateDir 撐到 production 量級守住它。
func TestClaudeApprovalSocketFitsInLongStateDir(t *testing.T) {
	a, _ := newTestApp(t)
	writeMultiTurnClaude(t, a)

	const targetLen = 80 // production `<cwd>/.workbench` 的實測量級
	base, err := os.MkdirTemp("/tmp", "wb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	pad := targetLen - len(base) - 1
	if pad < 1 {
		t.Fatalf("base 路徑已超過目標長度：%s", base)
	}
	deep := filepath.Join(base, strings.Repeat("d", pad))
	if err := os.MkdirAll(filepath.Join(deep, "recordings"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.stateDir = deep

	w := mustCreate(t, a, "claude")
	// 自我校驗：這個 stateDir 必須真的長到會讓「帶完整 WSID 的 socket 檔名」爆掉，
	// 否則本測試守不住任何東西。
	if wsidForm := filepath.Join(deep, "approval-"+string(w)+".sock"); len(wsidForm) < 104 {
		t.Fatalf("stateDir 不夠長，測試無效（帶 WSID 的路徑只有 %d bytes）", len(wsidForm))
	}

	commit, err := a.startClaude(w, "p", "", "")
	if err != nil {
		t.Fatalf("長 stateDir 下 approval socket 必須 bind 得起來：%v", err)
	}
	t.Cleanup(func() { commit(false) })
	h := a.hostFor(w)
	if len(h.sockPath) >= 104 {
		t.Fatalf("socket 路徑撐破 sun_path 上限：%d bytes（%s）", len(h.sockPath), h.sockPath)
	}
	id := seedApproval(t, a, w) // 真的 dial 得到才算數
	if err := a.ResolveApproval(id, false, "test"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

// approval 必須帶提出請求的 WSID——多 session 之後 provider 不足以定位它該回哪個
// slot（§3.3）。
func TestClaudeApprovalCarriesWSID(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	w := mustCreate(t, a, "claude")
	commit, err := a.startClaude(w, "p", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { commit(false) })

	id := seedApproval(t, a, w)
	if pa := a.pendingByID(id); pa == nil || pa.wsid != w {
		t.Fatalf("approval 必須帶 WSID：%+v", pa)
	}
	// Task 26：UI 事件本身也必須帶 wsid——前端依它做 pane 路由（§3.6.4），
	// 只有 apprPending 帶著沒用，對話框看不到。
	//
	// **必須等條件、不能直接讀**（Task 31 驗收時打紅的既有測試競態）：`seedApproval`
	// 等的是 `pendingByID(id) != nil`，而 production 的 `pumpApprovals` 是
	// registerApproval → EmitApprovalRequest → emit("approval:request") 三步，
	// 登記完成**嚴格早於** UI 事件送出。直接讀等於跟 pump goroutine 賽跑，負載高時
	// 會偽陽。沿用 app_test.go:367 既有慣例（同一個事件、同一種等法）。
	waitFor(t, "approval:request dialog event", func() bool { return len(ui.find("approval:request")) > 0 })
	reqs := ui.find("approval:request")
	if len(reqs) == 0 {
		t.Fatal("必須發出 approval:request UI 事件")
	}
	d := reqs[len(reqs)-1].data.(map[string]any)
	if d["wsid"] != string(w) {
		t.Fatalf("approval:request 必須帶 wsid=%s：%+v", w, d)
	}
	if err := a.ResolveApproval(id, false, "test"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// dismiss 同樣帶 wsid ＋ reason（remove／shutdown 兩種恢復觸發只有 reason 分得出來）
	dis := ui.find("approval:dismiss")
	if len(dis) == 0 {
		t.Fatal("resolve 後必須發出 approval:dismiss")
	}
	dd := dis[len(dis)-1].data.(map[string]any)
	if dd["wsid"] != string(w) || dd["reason"] != "test" {
		t.Fatalf("approval:dismiss 必須帶 wsid 與 reason：%+v", dd)
	}
}

// session:done 必須帶 wsid——多 session 之後 provider 不足以決定該收哪個 pane
// 的 busy 狀態（前端 applyDone 依它路由）。
func TestSessionDoneCarriesWSID(t *testing.T) {
	a, ui := newTestApp(t)
	writeMultiTurnClaude(t, a)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session:done", func() bool { return len(ui.find("session:done")) > 0 })
	for _, e := range ui.find("session:done") {
		d := e.data.(map[string]any)
		if d["wsid"] != string(w) {
			t.Fatalf("session:done 必須帶 wsid=%s：%+v", w, d)
		}
	}
}

// 反射守門：Claude 的六個 App 級單例欄位必須真的消失，不是「還在但沒人用」。
func TestNoClaudeSingletonFieldsRemain(t *testing.T) {
	tp := reflect.TypeOf(App{})
	for _, name := range []string{"broker", "claudeSess", "claudeSessionID",
		"claudePumpDone", "claudeLease", "claudeTeardownFn"} {
		if _, ok := tp.FieldByName(name); ok {
			t.Fatalf("Claude 單例欄位 %s 應已刪除（§3.3）", name)
		}
	}
}

// Task 26 原子切換的 Go 端守門：exported binding 的第一參數改為 WSID。
//
// 反射只看得到 string（provider 與 WSID 同型別），因此這裡守的是**可觀察行為**：
// 傳 provider 名稱必須失敗、傳真正的 WSID 必須成功。舊的
// TestExportedBindingSignatureUnchanged（Task 8 遷移窗口的守門「簽名不得改」）
// 由本測試取代——切換後留著必然自相矛盾。
func TestExportedBindingsAddressByWSID(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)

	// provider 名稱不再是合法位址（此前 legacyWSIDFor 會把它解析成某個 slot）
	for _, name := range []string{"claude", "codex"} {
		if err := a.SendMessage(name, "hi"); !errors.Is(err, appcore.ErrSessionNotFound) {
			t.Fatalf("SendMessage(%q) 必須以 ErrSessionNotFound 拒絕，got %v", name, err)
		}
		if err := a.EndSession(name); !errors.Is(err, appcore.ErrSessionNotFound) {
			t.Fatalf("EndSession(%q) 必須以 ErrSessionNotFound 拒絕，got %v", name, err)
		}
		if err := a.NewSession(name); !errors.Is(err, appcore.ErrSessionNotFound) {
			t.Fatalf("NewSession(%q) 必須以 ErrSessionNotFound 拒絕，got %v", name, err)
		}
		if err := a.TerminateSession(name); !errors.Is(err, appcore.ErrSessionNotFound) {
			t.Fatalf("TerminateSession(%q) 必須以 ErrSessionNotFound 拒絕，got %v", name, err)
		}
		if err := a.StartSession(name, "hi", "", "", "t", ""); !errors.Is(err, appcore.ErrSessionNotFound) {
			t.Fatalf("StartSession(%q) 必須以 ErrSessionNotFound 拒絕，got %v", name, err)
		}
	}
	// 真正的 WSID 仍可用（同一個 session）。先等 mustStartClaude 那一輪收尾
	// ——§1.1 的 in-flight guard（Task 30）會把還沒收尾的第二筆擋掉，那不是本
	// 測試要驗的東西。
	waitTurnSettled(t, a, w)
	if err := a.SendMessage(string(w), "hi"); err != nil {
		t.Fatalf("WSID 定址的 SendMessage 必須成功：%v", err)
	}
}

// resolveWSID：尚未 CommitCreate 的 reservation 不得被 exported binding 定址。
//
// SendMessage／EndSession／NewSession／StartSession 這幾條就算 resolveWSID 放行
// 也會被 Manager 的 committedSlotLocked 兜底成同一個 error——**TerminateSession
// 不會**：它只讀 hostFor(w)，reservation 沒有 host，回的是「no active claude
// session」這種與「這個 WSID 不存在」語意不同的錯誤。因此把 TerminateSession
// 一併納入斷言，這條測試才真的在守 resolveWSID 的驗證，而不是重複驗 Manager。
func TestExportedBindingsRejectUncommittedReservation(t *testing.T) {
	a, _ := newTestApp(t)
	w, _, err := a.manager.ReserveSession(contract.ProviderClaude)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.manager.ProviderOf(w); !ok {
		t.Fatal("前提不成立：ProviderOf 應認得未 commit 的 reservation")
	}
	if err := a.SendMessage(string(w), "hi"); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("未 commit 的 reservation 必須以 ErrSessionNotFound 拒絕，got %v", err)
	}
	if err := a.TerminateSession(string(w)); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("TerminateSession 必須先以 ErrSessionNotFound 拒絕（不得落到 host 查詢）：%v", err)
	}
}

// 🔴 Critical 迴歸（Task 26 review）：restore.json 是 provider-keyed，而本票的
// 建立入口讓「同 provider 多 session」第一次可從 UI 到達。若 StartSession 仍無
// 條件套用該 provider 的 resume，一個全新、從未對話過的 session B 會被靜默接到
// session A 的對話上（argv 出現 --resume <A 的 session id>）。
//
// 斷言看的是 **fake CLI 落檔的 argv**，不是內部旗標——「接錯對話」正是從 argv
// 那一刻起變成事實的。
func TestSecondSessionOfSameProviderDoesNotInheritResume(t *testing.T) {
	a, ui := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	argvFile := writeInitClaude(t, a, "sess-A")

	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wA)) })
	waitFor(t, "A 的 resume 已 commit 進自己的 entry", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})

	// 第二個 claude session：resume 參數留空，且從未對話過
	wB := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wB)) })

	waitFor(t, "兩次 start 的 argv 都已落檔", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Count(string(b), "--mcp-config") >= 2
	})
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("前提不成立：需要兩次 start 的 argv，got %q", string(b))
	}
	// 第一個 session 本來就沒有 resume 可接（restore 當時是空的）
	if strings.Contains(lines[0], "--resume") {
		t.Fatalf("前提不成立：第一個 session 不該帶 resume：%q", lines[0])
	}
	if strings.Contains(lines[1], "--resume") {
		t.Fatalf("同 provider 第二個 session 不得繼承 provider-keyed 的 resume（會接到別人的對話）：%q", lines[1])
	}
	// per-WSID writer 之後不再需要「不明確就跳過並通知使用者」那套：B 讀的是
	// **自己的** entry，本來就是空的，沒有東西可跳過、也沒有理由打擾使用者。
	// 取而代之的正向保證是 A 的續聊身分完全沒被動到。
	if got, _ := a.wsReg.Get(string(wA)); got.ResumeSessionID != "sess-A" {
		t.Fatalf("B 啟動不得動到 A 的續聊身分：%q", got.ResumeSessionID)
	}
	_ = ui
}

// NewSession 的 ResetView 同樣是 provider-keyed 的破壞性寫入：對 B 按「開新對話」
// 不得清掉 A 的 resume id。
// D5 等價改寫：舊實作的 ResetView 是 provider-keyed 的破壞性寫入，只能在
// 「不明確」時整段跳過（代價：New 在跨重啟那一維沒有效果）。per-WSID 之後
// **無條件執行**，保證換成「只影響自己那一筆」。
func TestNewSessionDoesNotResetAnotherSessionsRestoreEntry(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	// A 必須真的 commit 出一個 resume id，才驗得到「B 的 New 不得清掉它」
	writeInitClaude(t, a, "sess-A")

	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wA)) })
	waitFor(t, "A 的 resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID != ""
	})
	beforeEntry, _ := a.wsReg.Get(string(wA))
	before := beforeEntry.ResumeSessionID

	wB := mustCreate(t, a, "claude") // dormant，從未 start
	if err := a.NewSession(string(wB)); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.wsReg.Get(string(wA)); got.ResumeSessionID != before {
		t.Fatalf("對 B 開新對話不得清掉 A 的 resume：%q → %q", before, got.ResumeSessionID)
	}
	// 而 B 自己的 view boundary 必須真的前移（不再跳過）
	if got, _ := a.wsReg.Get(string(wB)); got.ViewStartEventID == "" {
		t.Fatal("多 session 情境下 New 仍必須為自己前移 view boundary，不得跳過")
	}
}

// providerRestoreUnambiguous 的判定取 Manager slot 與 registry live entry 的
// **聯集上界**。兩邊各自都會漏，所以兩個非對稱情境都要驗——只驗「CreateSession
// 兩次」是驗不出來的（那條路徑同時寫進兩邊，任一邊單獨都足以偵測）。
func TestProviderRestoreUnambiguousUsesBothSources(t *testing.T) {
	pv := contract.ProviderClaude

	t.Run("零個／恰一個 session 視為明確", func(t *testing.T) {
		a, _ := newTestApp(t)
		a.wsReg = &stubRegistry{}
		if !a.providerRestoreUnambiguous(pv) {
			t.Fatal("零個 session 時必須視為明確")
		}
		mustCreate(t, a, "claude")
		if !a.providerRestoreUnambiguous(pv) {
			t.Fatal("恰一個 session 時必須視為明確")
		}
		if _, err := a.CreateSession("codex", "t"); err != nil {
			t.Fatal(err)
		}
		if !a.providerRestoreUnambiguous(pv) {
			t.Fatal("另一 provider 的 session 不得影響判定")
		}
		if _, err := a.CreateSession("claude", "t2"); err != nil {
			t.Fatal(err)
		}
		if a.providerRestoreUnambiguous(pv) {
			t.Fatal("同 provider 兩個 session 必須視為不明確")
		}
	})

	// registry 有兩筆、Manager 只還原了其中一筆（或一筆都沒有）——只看 Manager
	// slot 會誤判成「只有一個」，而那筆沒被還原的 dormant session 一樣是使用者的
	// session，restore.json 同樣指不清楚。
	t.Run("registry 兩筆但 Manager 無對應 slot", func(t *testing.T) {
		a, _ := newTestApp(t)
		reg := &stubRegistry{}
		a.wsReg = reg
		for _, id := range []string{"01DORMANTA000000000000001", "01DORMANTB000000000000001"} {
			if err := reg.Put(wsregistry.Entry{WSID: id, Provider: "claude",
				CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
		}
		if got := a.manager.SlotCount(pv); got != 0 {
			t.Fatalf("前提不成立：Manager 應為 0 個 slot，got %d", got)
		}
		if a.providerRestoreUnambiguous(pv) {
			t.Fatal("registry 兩筆 live entry 時必須視為不明確（Manager 看不到它們）")
		}
	})

	// 兩邊各有一筆但**是不同的 WSID**：max(|slots|,|live|) 會得到 1 而誤判成
	// 明確——那是聯集的下界不是上界。這個狀態今天可達：RemoveSession 的殘餘窗口
	// （tombstone 已落盤、釋放名額失敗）就會留下一個不在 registry live 裡的孤兒
	// slot，而使用者另外還有一個正常的 session。
	t.Run("兩邊各一筆但 WSID 不相交", func(t *testing.T) {
		a, _ := newTestApp(t)
		reg := &stubRegistry{}
		a.wsReg = reg
		// Manager 有 X（不在 registry）
		_, tok, err := a.manager.ReserveSession(pv)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.manager.CommitCreate(tok); err != nil {
			t.Fatal(err)
		}
		// registry 有 Y（未還原成 slot）
		if err := reg.Put(wsregistry.Entry{WSID: "01ONLYINREGISTRY000000001", Provider: "claude",
			CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		if a.manager.SlotCount(pv) != 1 || len(reg.Live()) != 1 {
			t.Fatal("前提不成立：兩邊各應恰有一筆")
		}
		if a.providerRestoreUnambiguous(pv) {
			t.Fatal("兩邊各一筆但不相交＝實際有兩個 session，必須視為不明確")
		}
	})

	// Manager 有兩個 slot、registry 一筆都沒有（registry 未接線或該筆尚未落盤）
	// ——只看 registry 會誤判成「只有一個」。
	t.Run("Manager 兩個 slot 但 registry 為空", func(t *testing.T) {
		a, _ := newTestApp(t)
		a.wsReg = &stubRegistry{}
		for i := 0; i < 2; i++ {
			w, tok, err := a.manager.ReserveSession(pv)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.manager.CommitCreate(tok); err != nil {
				t.Fatal(err)
			}
			_ = w
		}
		if len(a.wsReg.Live()) != 0 {
			t.Fatal("前提不成立：registry 應為空")
		}
		if a.providerRestoreUnambiguous(pv) {
			t.Fatal("Manager 兩個 slot 時必須視為不明確（registry 看不到它們）")
		}
	})
}

// 🟠 Important 迴歸（Task 26 review round-2）的 per-WSID 等價（D5）：
// A 建立 → 對話（resume = sess-A）→ End → Remove（tombstone）→ 建立全新 B →
// StartSession(B, resume="")。B 不得拿到 --resume sess-A。
//
// 舊實作靠「移除時無條件清掉 provider-keyed 的 restore 身分」達成，代價是存活的
// 手足一併失去續聊；現在靠 tombstone ＋ per-WSID 讀取，B 讀的本來就是自己的
// entry（空的），而 A 的 entry 就算還留著 id 也已被 tombstone 擋住。
func TestRemovedSessionsResumeIsNotInheritedByNextSession(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	argvFile := writeInitClaude(t, a, "sess-A")

	wA := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A 的 resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	// tombstone 之後那個身分不得再被讀成續聊來源（entry 本身刻意留著）
	if got := a.registryResume(wA); got != "" {
		t.Fatalf("已移除的 session 不得再回報續聊身分，got %q", got)
	}
	if e, ok := a.wsReg.Get(string(wA)); !ok || e.ResumeSessionID != "sess-A" {
		t.Fatalf("tombstone 不需要抹掉 entry 內容（讀取端已擋）：%+v ok=%v", e, ok)
	}

	wB := mustCreate(t, a, "claude")
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wB)) })

	waitFor(t, "B 的 argv 已落檔", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Count(string(b), "--mcp-config") >= 2
	})
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if strings.Contains(lines[len(lines)-1], "--resume") {
		t.Fatalf("已移除 session 的 resume 不得被下一個新 session 繼承：%q", lines[len(lines)-1])
	}
}

// 正向缺口（review round-2 Minor 5）：registry 非 nil、恰一筆 live entry 時，
// 續聊必須照舊生效——收窄不得順手把正常路徑一起封掉。既有的
// TestAutoResumeAfterPlainEnd 走的是 wsReg == nil 分支，涵蓋不到這條。
func TestSoleRegistrySessionStillResumes(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	argvFile := writeInitClaude(t, a, "sess-A")

	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(w))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
	if len(a.wsReg.Live()) != 1 {
		t.Fatalf("前提不成立：registry 應恰有一筆 live entry，got %d", len(a.wsReg.Live()))
	}
	if err := a.StartSession(string(w), "again", "", "", "task-a", ""); err != nil { // resume 參數空
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitFor(t, "第二次 start 自動接續", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Contains(string(b), "--resume sess-A")
	})
}

// D5 等價改寫（原 N3：ClearResume 必須無條件）。
// 舊實作的無條件清除是為了防「移除 A → B 讀到 A 的 id」，代價寫在 Task 26 的
// 註解裡：**存活的手足一併失去續聊**。per-WSID 之後保證反轉——手足必須留住自己
// 的續聊身分，而 B 讀的本來就不是 A 的 entry。
//
// mutation：把 RemoveSession 的 tombstone 之後補回 provider 級清除 → 這條轉紅。
func TestRemoveKeepsSiblingResumeAndBLeavesAsIs(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	argvFile := writeInitClaude(t, a, "sess-A")

	wA := mustCreate(t, a, "claude")
	wB := mustCreate(t, a, "claude") // 手足：移除 A 時仍在
	if err := a.StartSession(string(wA), "hi A", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A 的 resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(wA))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	// B 也先跑過一輪，拿到自己的續聊身分
	if err := a.StartSession(string(wB), "hi B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B 的 resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(wB))
		return e.ResumeSessionID != ""
	})
	if err := a.EndSession(string(wB)); err != nil {
		t.Fatal(err)
	}
	beforeB, _ := a.wsReg.Get(string(wB))

	if err := a.RemoveSession(string(wA)); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.wsReg.Get(string(wB)); got.ResumeSessionID != beforeB.ResumeSessionID {
		t.Fatalf("移除手足不得讓存活 session 失去續聊：%q → %q",
			beforeB.ResumeSessionID, got.ResumeSessionID)
	}
	if err := a.StartSession(string(wB), "again B", "", "", "task-b", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(wB)) })
	waitFor(t, "B 仍自動接續自己的對話", func() bool {
		b, err := os.ReadFile(argvFile)
		return err == nil && strings.Contains(string(b), "--resume "+beforeB.ResumeSessionID)
	})
}

// D5 等價改寫（原 N1：ClearResume 必須早於 decrement_count）。
// 清除步驟已經不存在，取代它的是 tombstone_persist 本身——它同樣必須早於
// decrement_count，因為釋放名額失敗會留下孤兒 slot，而此刻那個 WSID 的續聊身分
// 必須已經讀不到了。
func TestTombstoneLandsBeforeDecrementCount(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	writeInitClaude(t, a, "sess-A")

	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(w))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
	a.hookRemoveStep = func(step string) {
		if step != "decrement_count" {
			return
		}
		if _, err := a.manager.BeginNewSessionSubmit(w, "block"); err != nil {
			t.Errorf("前提不成立：無法把 slot 推離 idle：%v", err)
		}
	}
	err := a.RemoveSession(string(w))
	if err == nil {
		t.Fatal("前提不成立：decrement_count 應失敗（否則測不到時點）")
	}
	if !strings.Contains(err.Error(), "已 tombstone 但釋放名額失敗") {
		t.Fatalf("前提不成立：失敗點必須是 decrement_count：%v", err)
	}
	// 這就是時點的意義：名額沒釋放、slot 還在，但續聊身分已經讀不到
	if got := a.registryResume(w); got != "" {
		t.Fatalf("tombstone 必須發生在 decrement_count 之前，got %q", got)
	}
}

// D5 等價改寫（原 N2：清除失敗必須 audit ＋ workspace 通知兩路 fail loud）。
// 清除步驟消失後，同一個位置的 Fail Loud 約束落在 tombstone_persist 上：它失敗
// 代表 session **沒有**被移除，必須整個 RemoveSession 回錯並保留 slot，讓使用者
// 能重試——靜默成功才是這條規則要防的東西。
func TestRemoveFailsLoudWhenTombstonePersistFails(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	writeInitClaude(t, a, "sess-A")

	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "hi", "", "", "task-a", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resume 已 commit", func() bool {
		e, _ := a.wsReg.Get(string(w))
		return e.ResumeSessionID == "sess-A"
	})
	if err := a.EndSession(string(w)); err != nil {
		t.Fatal(err)
	}
	reg.removeErr = errors.New("boom")
	err := a.RemoveSession(string(w))
	if err == nil {
		t.Fatal("tombstone persist 失敗必須讓整個移除失敗（session 其實還在）")
	}
	if !strings.Contains(err.Error(), "tombstone persist 失敗") {
		t.Fatalf("錯誤必須指出失敗點：%v", err)
	}
	if _, serr := a.manager.State(w); serr != nil {
		t.Fatalf("slot 必須保留可重試：%v", serr)
	}
}

