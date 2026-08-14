package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"reflect"
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

// seedApproval：走真實 approval 路徑——dial 該 host 的 per-WSID socket 送一筆
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
	if got := m.Type.In(1).Kind(); got != reflect.String {
		t.Fatalf("SendMessage 第一參數型別不得改變：%v", got)
	}
	if err := a.SendMessage("claude", "hi"); err != nil {
		t.Fatalf("provider-keyed exported binding 必須仍可用：%v", err)
	}
}

// legacyWSIDFor 的三段解析順序（coordinator 2026-08-15 凍結）。
func TestLegacyWSIDForResolutionOrder(t *testing.T) {
	a, _ := newTestApp(t)
	pv := contract.ProviderClaude

	// 3) 無 host、無 CreateSession → legacy slot WSID（且對 ...WS 入口可解析）
	legacy := a.legacyWSIDFor(pv)
	if legacy == "" {
		t.Fatal("legacy slot WSID 不得為空")
	}
	if _, err := a.manager.BeginNewSessionSubmitWS(legacy, "task"); err != nil {
		t.Fatalf("legacy WSID 必須對 ...WS 入口可解析：%v", err)
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
