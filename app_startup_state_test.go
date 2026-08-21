package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/escalation"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

// ---- 舊路徑 state 遷移（owner 2026-08-19 F2）----
//
// gate／escalation journal 與 evidence/ 從 workspaceDir/.workbench 改綁受 ownership
// lease 保護的 a.stateDir。舊路徑上的那一份**不得靜默忽略**：未解除的系統管控項目
// 一旦看不見，原本擋著的核可就默默放行了。
//
// auditLifecycleApp 的 workspaceDir 與 stateDir 天生不同值（後者在 /tmp 下的短
// 路徑），正好是 production 的 tmp fallback 形狀，不必另外造。

// legacyDirOf：舊路徑（M3b 之前的 journal 落點）。
func legacyDirOf(a *App) string { return filepath.Join(a.workspaceDir, ".workbench") }

// seedLegacyEscalation：在舊路徑寫一個**未解除的系統管控項目**，回傳它的 id。
// 走 escalation 套件的正式 API，不手刻 JSON——手刻的話這條測試會在格式演進時
// 變成綠燈假象。
func seedLegacyEscalation(t *testing.T, dir, conditionKey string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	j, err := escalation.OpenJournal(filepath.Join(dir, "escalation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	svc := escalation.NewService(j,
		func() string { n++; return contract.NewULID(time.Now().Add(time.Duration(n) * time.Millisecond)) },
		func() string { return "2026-08-19T00:00:00Z" })
	id, err := svc.CreateSystem(conditionKey, "workspace", true, "舊路徑留下的阻擋項", "spec#1")
	if err != nil {
		t.Fatal(err)
	}
	if cerr := j.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	return id
}

// TestLegacyStateMigratedBeforeJournalsOpen（F2 正題）
//
// 舊路徑有一個未解除的系統管控項目，新路徑什麼都沒有：啟動之後那個項目必須仍然
// 看得見（＝真的被搬過來，不是重新開一份空的）。
//
// 正題斷言：`EscalationList()` 讀得到同一個 conditionKey，且舊路徑的檔案已消失。
func TestLegacyStateMigratedBeforeJournalsOpen(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	legacy := legacyDirOf(a)
	seedLegacyEscalation(t, legacy, "planner-enforcement-preflight")

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if _, err := os.Stat(filepath.Join(legacy, "escalation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("遷移後舊路徑不得留下第二份 journal（兩個 writer 就從這裡開始），stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "escalation.jsonl")); err != nil {
		t.Fatalf("新路徑必須有那一份 journal：%v", err)
	}
	items, err := a.EscalationList()
	if err != nil {
		t.Fatalf("EscalationList: %v", err)
	}
	found := false
	for _, e := range items {
		if e.Item.ConditionKey == "planner-enforcement-preflight" {
			found = true
			if e.State != escalation.StateOpen {
				t.Fatalf("遷移不得改變項目狀態：未解除的項目必須仍是 open，實得 %q", e.State)
			}
			if !e.Item.Hard {
				t.Fatal("遷移不得改變項目屬性：系統管控的硬性項必須仍是 hard")
			}
		}
	}
	if !found {
		t.Fatalf("舊 journal 的系統管控項目必須在遷移後仍然看得見，實得 %d 筆：%+v", len(items), items)
	}
	if line := findAuditLine(t, a, "legacy_state_migrated"); !strings.Contains(line, "escalation.jsonl") {
		t.Fatalf("遷移必須留下稽核，實得：%s", line)
	}
}

// TestLegacyStateMigrationBlocksWhenBothSidesExist（F2 反面）
//
// 舊路徑與新路徑同時存在同一份 journal：兩邊都可能有唯一的記錄，合併語意沒有
// 定義，**不得自行選一份**。
//
// 正題斷言（三條分得開）：
//   - 啟動被擋下——`a.wsReg` 仍為 nil，CreateSession 走不到。
//   - 兩份檔案的內容都沒有被動過（沒有偷偷覆蓋）。
//   - 橫幅同時列出兩個路徑，使用者知道要去看哪裡。
func TestLegacyStateMigrationBlocksWhenBothSidesExist(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	legacy := legacyDirOf(a)
	seedLegacyEscalation(t, legacy, "legacy-side")
	seedLegacyEscalation(t, a.stateDir, "current-side")
	legacyDigest := fileDigest(t, filepath.Join(legacy, "escalation.jsonl"))
	currentDigest := fileDigest(t, filepath.Join(a.stateDir, "escalation.jsonl"))

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if a.wsReg != nil {
		t.Fatal("遷移無法判定權威時必須中止啟動：session registry 不得被接上")
	}
	if _, err := a.CreateSession("claude", "task"); err == nil {
		t.Fatal("啟動被擋下時不得開放建立 session")
	}
	if fileDigest(t, filepath.Join(legacy, "escalation.jsonl")) != legacyDigest ||
		fileDigest(t, filepath.Join(a.stateDir, "escalation.jsonl")) != currentDigest {
		t.Fatal("無法判定權威時兩份都不得被改動（不可自行選一份）")
	}
	banner := a.startupErrText()
	for _, want := range []string{"同時存在", legacy, a.stateDir} {
		if !strings.Contains(banner, want) {
			t.Fatalf("橫幅必須說明是哪兩個路徑衝突（缺 %q）：%s", want, banner)
		}
	}
	// 中止是**在任何惰性 journal 開啟之前**：啟動後段的欄位發布沒有跑到。
	if a.toolsDir() != "" {
		t.Fatalf("遷移未完成時不得繼續啟動序列，實得 toolsDir=%q", a.toolsDir())
	}
}

// ---- 全新 workspace 的取鎖（owner 2026-08-19 F4）----

// TestLeaseAcquirableOnBrandNewWorkspace
//
// `singleinstance.Acquire` 只 open <stateDir>/instance.lock，**它不會建目錄**
// （契約守門見 internal/singleinstance 的 TestAcquireRequiresExistingStateDir）。
// 所以 state directory 尚不存在的全新 workspace，取鎖前必須先把空目錄建出來，
// 否則第一次啟動會被當成「環境有問題」拒絕。
//
// 正題斷言：stateDir 不存在時 acquireStateLease 成功。
// 邊界斷言：這一步只建出空目錄 ＋ 鎖檔，**沒有任何 session 狀態**——把 MkdirAll
// 擴大成建 recordings/、開 registry 之類，會在這裡紅。
func TestLeaseAcquirableOnBrandNewWorkspace(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "wbnew")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	stateDir := filepath.Join(short, "brand-new", ".workbench")
	if _, serr := os.Stat(stateDir); !os.IsNotExist(serr) {
		t.Fatalf("測試前提：stateDir 必須尚未存在，stat=%v", serr)
	}

	a := NewApp()
	a.workspaceDir = filepath.Join(short, "brand-new")
	a.stateDir = stateDir
	lease, err := a.acquireStateLease()
	if err != nil {
		t.Fatalf("全新 workspace 必須取得得到 lease：%v", err)
	}
	t.Cleanup(func() { _ = lease.release() })
	if !lease.ownsStateDir(stateDir) {
		t.Fatal("取得的 lease 必須綁在這個 state directory 上")
	}

	ents, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{singleinstance.LockFileName}
	if len(got) != len(want) || (len(got) == 1 && got[0] != want[0]) {
		t.Fatalf("取鎖之前只允許建立空的 state directory，實得 %v（want %v）", got, want)
	}
}

// TestProductionResolverCreatesOnlyEmptyStateDirBeforeLease
//
// **走完整的 production 解析路徑**（`WORKBENCH_WORKSPACE` → `resolveWorkspace`
// → `acquireStateLease`），不預先指定 stateDir——reviewer 2026-08-19 P1 指出既有
// 測試都是先設好 a.stateDir，於是正式首次啟動真正走的那條路徑從來沒被量過，而
// 它會在取鎖之前就把 recordings/ 與 probe/ 建出來。
//
// 正題斷言（分成兩段，各自可獨立打紅）：
//   - 取得 lease 的那一刻，state directory 裡**只有**鎖檔。
//   - 走完 openStateWriters 之後，recordings/ 與 probe/ 才出現（功能沒有被砍掉，
//     只是搬到出示 lease 之後）。
func TestProductionResolverCreatesOnlyEmptyStateDirBeforeLease(t *testing.T) {
	ws, err := claude.NormalizeCWD(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBENCH_WORKSPACE", ws)

	a := NewApp()
	a.ctx = context.Background()
	lease, err := a.acquireStateLease() // 內含 resolveWorkspace（production 路徑）
	if err != nil {
		t.Fatalf("acquireStateLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.release() })
	if a.stateDir != filepath.Join(ws, ".workbench") {
		t.Fatalf("production resolver 應解析到 %s，實得 %s", filepath.Join(ws, ".workbench"), a.stateDir)
	}

	ents, err := os.ReadDir(a.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != singleinstance.LockFileName {
		t.Fatalf("取得 lease 的那一刻，state directory 只允許有鎖檔，實得 %v", got)
	}

	if !a.openStateWriters(lease) {
		t.Fatalf("openStateWriters 必須成功，startupErr=%q", a.startupErrText())
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })
	for _, d := range stateSubdirs {
		st, serr := os.Stat(filepath.Join(a.stateDir, d))
		if serr != nil || !st.IsDir() {
			t.Fatalf("出示 lease 之後必須把 %s 目錄建出來（骨架只是搬家，不是砍掉），stat=%v", d, serr)
		}
	}
}

// ---- 啟動資訊的併發存取（owner 2026-08-19 F5）----

// TestCLIInfoIsSynchronizedWithStartupPublish
//
// CLIInfo() 是 Wails binding，跑在 UI 的 goroutine 上；startup 在另一條 goroutine
// 上發布 toolsDirPath／toolsSource／nodePath／startupErr。這是正式執行時可達的
// 資料競爭，不是理論上的。
//
// 這條測試有兩個 oracle，缺一不可：
//   - **-race**：真正判定「讀寫是否走同一套同步機制」的是 race detector。拿掉
//     startupMu 之後，本條在 `go test -race` 下必紅。
//   - **確定性的窗口**：hookStartupPublish 讓 startup 停在**欄位發布中途**
//     （tools dir 已寫、node path 還沒），確定我們賽跑的對象就是接下來那一次
//     publishNodePath。沒有它的話兩條 goroutine 會不會真的重疊完全看排程，
//     race detector 可能整場都沒觀測到那一對存取（失效形狀 (E)）。
//
// **barrier 的擺法本身是有講究的**：放行之後**不得**先 `<-done` 再讀——那個
// channel 會替兩條 goroutine 建立 happens-before 邊，讀寫就被排序掉了，即使欄位
// 完全沒有保護 race detector 也不會出聲（第一版就是這樣，mutation 打不紅）。所以
// 放行後**立刻**讀，讀完才等收斂。
func TestCLIInfoIsSynchronizedWithStartupPublish(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	paused, release := make(chan struct{}), make(chan struct{})
	a.hookStartupPublish = func() {
		close(paused)
		<-release
	}
	done := make(chan struct{})
	go func() { a.startup(context.Background()); close(done) }()
	t.Cleanup(func() { a.shutdown(context.Background()) })

	<-paused
	mid := a.CLIInfo() // 發布中途：這一刻的快照必須自洽
	if mid["toolsDir"] == "" {
		t.Fatalf("測試前提不成立：暫停點必須落在 tools dir 已發布之後，實得 %+v", mid)
	}
	if !strings.HasPrefix(mid["node"], "missing") {
		t.Fatalf("暫停點必須落在 node path 發布之前（否則量不到發布中途的窗口），實得 %q", mid["node"])
	}

	close(release) // 放行：以下的讀取與 publishNodePath 真正併發，中間沒有任何同步邊
	var racing map[string]string
	for range 8 {
		racing = a.CLIInfo()
	}
	<-done

	after := a.CLIInfo()
	if racing["toolsDir"] != mid["toolsDir"] || after["toolsDir"] != mid["toolsDir"] {
		t.Fatalf("併發讀取不得看到不一致的 tools dir，mid=%q racing=%q after=%q",
			mid["toolsDir"], racing["toolsDir"], after["toolsDir"])
	}
	if after["toolsSource"] == "" {
		t.Fatalf("啟動完成後的快照必須補齊來源，實得 %+v", after)
	}
}
