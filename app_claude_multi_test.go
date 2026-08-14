package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
// 掛在預期的 WSID 上（同時驗到 legacyWSIDFor 第 2 順位：最近一次 CreateSession）。
func mustStartClaude(t *testing.T, a *App, w appcore.WSID) {
	t.Helper()
	writeMultiTurnClaude(t, a)
	if err := a.StartSession("claude", "hi", "", "", "task-"+string(w), ""); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if a.hostFor(w) == nil {
		t.Fatalf("host 必須掛在 legacyWSIDFor 解析出的 WSID %s 上", w)
	}
	t.Cleanup(func() { _ = a.EndSession("claude") })
}

// seedApproval：走真實 approval 路徑——dial 該 host 自己的 approval socket 送一筆
// 請求，等 pumpApprovals 把它登記進 apprPending，回傳 approval id。
func seedApproval(t *testing.T, a *App, w appcore.WSID) string {
	t.Helper()
	h := a.hostFor(w)
	if h == nil {
		t.Fatalf("no session host for %s", w)
	}
	id := "appr-" + string(w)
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
	a, _ := newTestApp(t)
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
	if err := a.ResolveApproval(id, false, "test"); err != nil {
		t.Fatalf("resolve: %v", err)
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

// exported binding 在 Task 26 的原子切換之前不得改簽名，否則前端會中途壞掉。
func TestExportedBindingSignatureUnchanged(t *testing.T) {
	a, _ := newTestApp(t)
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	m, ok := reflect.TypeOf(&App{}).MethodByName("SendMessage")
	if !ok {
		t.Fatal("SendMessage 必須仍是 exported binding")
	}
	// 比 reflect.Type 本身而非 Kind()：named string type（如 appcore.WSID）的
	// Kind() 同樣是 reflect.String，用 Kind 守門擋不住 Task 26 最可能做的那個改動。
	if got := m.Type.In(1); got != reflect.TypeOf("") {
		t.Fatalf("SendMessage 第一參數型別不得改變：%v", got)
	}
	if err := a.SendMessage("claude", "hi"); err != nil {
		t.Fatalf("provider-keyed exported binding 必須仍可用：%v", err)
	}
}

// legacyWSIDFor 的四段解析順序（coordinator 2026-08-15 凍結；第 3 順位由 Task 9 補入）。
func TestLegacyWSIDForResolutionOrder(t *testing.T) {
	a, _ := newTestApp(t)
	pv := contract.ProviderClaude

	// 4) 無 host、無 CreateSession、registry 空 → legacy slot WSID（且可解析）
	legacy := a.legacyWSIDFor(pv)
	if legacy == "" {
		t.Fatal("legacy slot WSID 不得為空")
	}
	if _, err := a.manager.BeginNewSessionSubmit(legacy, "task"); err != nil {
		t.Fatalf("legacy WSID 必須對 WSID 入口可解析：%v", err)
	}

	// 2) 有 CreateSession 紀錄、尚無 host → 最近一次建立的 WSID
	w := mustCreate(t, a, "claude")
	if got := a.legacyWSIDFor(pv); got != w {
		t.Fatalf("第 2 順位應回最近一次 CreateSession 的 WSID：want %s got %s", w, got)
	}

	// 1) 恰有一個 live host → 它的 WSID（優先於 CreateSession 紀錄）
	other := mustCreate(t, a, "claude")
	a.putHost(&sessionHost{wsid: w, provider: pv})
	if got := a.legacyWSIDFor(pv); got != w {
		t.Fatalf("第 1 順位應回唯一 live host 的 WSID：want %s got %s", w, got)
	}
	if other == w {
		t.Fatal("CreateSession 必須產生相異 WSID")
	}
}

// 第 3 順位（Task 9）：Task 5／6 把使用者既有的 session 遷進 registry 並還原成
// dormant，但在這一層加入之前，使用者實際上仍工作在 legacy slot 上——那筆遷移
// 出來的 entry 從此不反映現實。本測試守住「遷移真的生效」。
func TestLegacyWSIDForPrefersSoleRestoredRegistryEntry(t *testing.T) {
	a, _ := newTestApp(t)
	pv := contract.ProviderCodex
	reg := &stubRegistry{}
	a.wsReg = reg

	// registry 有一筆 live entry，且已被啟動修復序列還原成 committed slot
	restored := appcore.WSID("01RESTOREDWSID000000000001")
	if err := reg.Put(wsregistry.Entry{WSID: string(restored), Provider: "codex",
		TaskLabel: "migrated", CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := a.manager.RestoreDormant(restored, pv); err != nil {
		t.Fatal(err)
	}
	if got := a.legacyWSIDFor(pv); got != restored {
		t.Fatalf("第 3 順位應回 registry 中唯一的 live entry：want %s got %s", restored, got)
	}

	// 未被還原成 committed slot 的 entry 不可回傳——否則之後每個 lifecycle 呼叫
	// 都會拿到 ErrSessionNotFound，比落到 legacy slot 更糟。
	a2, _ := newTestApp(t)
	reg2 := &stubRegistry{}
	a2.wsReg = reg2
	if err := reg2.Put(wsregistry.Entry{WSID: "01NOTRESTORED0000000000001", Provider: "codex",
		CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got := a2.legacyWSIDFor(pv)
	if got == "01NOTRESTORED0000000000001" {
		t.Fatal("未還原成 committed slot 的 entry 不得被解析出來")
	}
	if _, err := a2.manager.BeginNewSessionSubmit(got, "t"); err != nil {
		t.Fatalf("fallback 必須是可解析的 WSID：%v", err)
	}

	// 兩筆以上 live entry → 無從判斷，落到下一順位（legacy slot）
	a3, _ := newTestApp(t)
	reg3 := &stubRegistry{}
	a3.wsReg = reg3
	for _, id := range []string{"01TWOA0000000000000000001", "01TWOB0000000000000000001"} {
		if err := reg3.Put(wsregistry.Entry{WSID: id, Provider: "codex",
			CreatedAt: "2026-08-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		if err := a3.manager.RestoreDormant(appcore.WSID(id), pv); err != nil {
			t.Fatal(err)
		}
	}
	if got := a3.legacyWSIDFor(pv); got != a3.manager.LegacyWSID(pv) {
		t.Fatalf("多筆 live entry 時應落到 legacy slot，got %s", got)
	}
}
