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
	// 真正的 WSID 仍可用（同一個 session）
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
