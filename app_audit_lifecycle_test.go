package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/claude"
)

// 稽核寫入器的生命週期 ＋ 兩層 ownership 邊界的守門。
//
// **這一整檔都用 production 取鎖流程產生的 lease**（`a.acquireStateLease()` →
// 真的 `flock(2)`），不手造 `testOnly: true` capability。理由：test-only 那條路
// 只證明「檢查會通過」，證明不了 production 真的走得到；owner 2026-08-19 明確
// 要求守門用真 lease。
//
// oracle 一律不是受測對象自陳：
//   - 「writer 真的通了」→ 磁碟上 `audit.jsonl` 的實際內容
//   - 「startup 真的被擋」→ 下游 writer 的檔案**不存在** ＋ 下游物件仍為 nil
//   - 「不變量破壞被看見」→ `startupErr`（production 由 `CLIInfo` 餵給 UI 橫幅）
//   - 「零異動」→ state directory 的遞迴磁碟快照差異

// auditLifecycleApp：只帶最小前置的 App —— 刻意不用 newTestAppIn，因為那個
// helper 會先把 registry／sink／audit 都開好，`openStateWriters` 自己開了什麼
// 就量不到了（失效形狀 (B)：同一條 setup 同時滿足多個前提）。
//
// 回傳的 App **尚未取得 lease**；要 lease 的測試自己呼叫 acquireLease，這樣
// 「沒有 lease 會怎樣」與「有 lease 會怎樣」是兩條分得開的路徑。
func auditLifecycleApp(t *testing.T) *App {
	t.Helper()
	ws, err := claude.NormalizeCWD(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("/tmp", "wbaudit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })

	a := NewApp()
	a.ctx = context.Background()
	a.workspaceDir = ws
	a.stateDir = filepath.Join(short, ".workbench")
	if err := os.MkdirAll(filepath.Join(a.stateDir, "recordings"), 0o755); err != nil {
		t.Fatal(err)
	}
	return a
}

// acquireLease：走 production 的 `acquireStateLease`（真 flock），並保證測試結束
// 時釋放——否則同一支 test binary 的後續測試會被自己鎖住。
func acquireLease(t *testing.T, a *App) *stateLease {
	t.Helper()
	lease, err := a.acquireStateLease()
	if err != nil {
		t.Fatalf("取得 ownership lease 失敗：%v", err)
	}
	t.Cleanup(func() { _ = lease.release() })
	return lease
}

// ---- audit lifecycle ----

// TestStartupWithLeaseOpensAuditWriter（owner 守門 ①）
//
// 正題斷言：`a.audit()` 寫的事件**出現在磁碟上的 audit.jsonl**。
// 預期紅：拿掉 `openStateWriters` 裡的 `a.auditF = f` → 檔案存在但不含 marker。
func TestStartupWithLeaseOpensAuditWriter(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := acquireLease(t, a)

	if !a.openStateWriters(lease) {
		t.Fatalf("持有 lease 時 openStateWriters 必須成功，startupErr=%q", a.startupErr)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })

	a.audit("audit_lifecycle_probe", map[string]any{"marker": "guard-1"})

	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("取得 lease 後 startup 必須建立 audit writer，讀 audit.jsonl 失敗：%v", err)
	}
	if !strings.Contains(string(b), "guard-1") {
		t.Fatalf("audit() 寫的事件必須真的落到磁碟上的 audit.jsonl，實得：\n%s", b)
	}
	if strings.Contains(a.startupErr, "不變量破壞") {
		t.Fatalf("正常路徑不得回報不變量破壞，實得 %q", a.startupErr)
	}
}

// TestAuditOpenFailureBlocksStartup（owner 守門 ①b）
//
// 用「audit.jsonl 這個路徑是目錄」逼 OpenFile 失敗（EISDIR）——不需要改權限，
// root 環境也照樣失敗。
//
// 正題斷言：`events.jsonl` **不存在**。也就是 startup 真的停在 audit 這一步。
// 回傳值只是補充證據，所以用 Errorf 不用 Fatalf（早退會讓 mutation 紅在補充
// 證據上）。
func TestAuditOpenFailureBlocksStartup(t *testing.T) {
	a := auditLifecycleApp(t)
	if err := os.MkdirAll(filepath.Join(a.stateDir, "audit.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	lease := acquireLease(t, a)

	if a.openStateWriters(lease) {
		t.Error("audit writer 開啟失敗時 openStateWriters 必須回 false（fail closed）")
	}
	// audit 排在所有 writer 之前，所以「後續 writer 一個都不開」現在包含 registry。
	for _, name := range []string{"sessions.json", "events.jsonl", "wire-segments.jsonl"} {
		if _, err := os.Stat(filepath.Join(a.stateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("audit 開啟失敗必須阻擋 startup——後續 writer 不得被建立，%s stat=%v", name, err)
		}
	}
	if a.registry != nil || a.eventSink != nil || a.manager != nil {
		t.Fatalf("下游 opener 不得被呼叫：registry=%v eventSink=%v manager=%v",
			a.registry != nil, a.eventSink != nil, a.manager != nil)
	}
	if !strings.Contains(a.startupErr, "稽核寫入器開啟失敗") {
		t.Fatalf("拒絕原因必須是使用者看得懂的訊息，實得 %q", a.startupErr)
	}
}

// TestAuditWriterLostAfterReadyFailsLoud（owner 守門 ②）
//
// **刻意在已有另一則啟動警告的狀態下驗**：production 的 `startupErr` 幾乎不會是
// 空的（replay index 降級、registry 遷移跳過…都會先寫進去）。先前的寫法直接賦值
// `startupErr`，只有在它為空時才寫得進去——於是 production 常態下這則橫幅整段
// 消失，可觀察出口只剩桌面使用者看不到的 stderr（owner 2026-08-19）。
//
// 正題斷言：`startupErr` **同時**含既有警告與不變量破壞的說明。
func TestAuditWriterLostAfterReadyFailsLoud(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := acquireLease(t, a)
	if !a.openStateWriters(lease) {
		t.Fatalf("前提：openStateWriters 必須成功，startupErr=%q", a.startupErr)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })

	// production 常態：啟動途中已經留下一則良性警告。
	a.noteStartupWarning("既有的啟動警告")

	a.auditMu.Lock()
	f := a.auditF
	a.auditF = nil
	a.auditMu.Unlock()
	t.Cleanup(func() { _ = f.Close() })

	a.audit("session_removed", map[string]any{"marker": "guard-2"})

	if !strings.Contains(a.startupErr, "不變量破壞") {
		t.Fatalf("ready 之後 writer 消失必須產生使用者可見的說明，實得 %q", a.startupErr)
	}
	if !strings.Contains(a.startupErr, "session_removed") {
		t.Fatalf("說明必須帶出是哪一類稽核事件遺失，實得 %q", a.startupErr)
	}
	if !strings.Contains(a.startupErr, "既有的啟動警告") {
		t.Fatalf("既有啟動訊息不得被覆蓋（appendStartup 累加語意），實得 %q", a.startupErr)
	}

	// 去重：CLI stderr 的 io.Writer 破掉之後每一行都會走到這條路徑。
	before := a.startupErr
	for range 50 {
		if _, err := a.auditWriterFor().Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
	}
	if a.startupErr != before {
		t.Fatalf("不變量破壞的說明只能出一次，實得長度 %d → %d", len(before), len(a.startupErr))
	}
}

// TestAuditWithoutLeaseStaysSilent（反向 mutation：好行為沒被誤殺）
//
// 沒有 lease ＝ unavailable。這時丟棄稽核是**正確行為**，不得被記成不變量破壞
// ——否則守門 ② 會把每一個「還沒啟動完成」的呼叫都誤判成事故。
func TestAuditWithoutLeaseStaysSilent(t *testing.T) {
	a := auditLifecycleApp(t)

	a.audit("session_removed", map[string]any{"marker": "no-lease"})

	if a.startupErr != "" {
		t.Fatalf("尚未取得 lease 時丟棄稽核是正確行為，不得寫橫幅，實得 %q", a.startupErr)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("沒有 lease 不得建立 audit.jsonl，stat=%v", err)
	}
}

// TestAuditAfterCloseStaysSilent（反向 mutation：收尾之後不得被誤殺）
//
// shutdown 已在釋放 lease 之前收掉 writer，此後丟棄同樣正確。closeAuditWriter
// 把狀態帶回 auditUnavailable 而不是留在 ready，靠的就是這條——留在 ready 的話
// 每一次收尾後的稽核都會被記成不變量破壞（owner 2026-08-19 F6：刻意不為此另立
// 第三個狀態，它與 unavailable 的行為完全相同）。
func TestAuditAfterCloseStaysSilent(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := acquireLease(t, a)
	if !a.openStateWriters(lease) {
		t.Fatalf("前提：openStateWriters 必須成功，startupErr=%q", a.startupErr)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })
	a.noteStartupWarning("既有的啟動警告")

	a.closeAuditWriter()
	a.audit("session_removed", map[string]any{"marker": "after-close"})

	if strings.Contains(a.startupErr, "不變量破壞") {
		t.Fatalf("closed 之後丟棄稽核是正確行為，不得回報破壞，實得 %q", a.startupErr)
	}
}

// ---- 兩層 ownership 邊界：各自獨立的 in-process 守門 ----
//
// 為什麼是 in-process 而不是跨 process（owner 2026-08-19 裁決）：跨 process 的
// 路徑會先被 `App.startup` 的取鎖早退攔住，第二個實例根本走不到這兩層，於是跨
// process 測試證明不了內層檢查有效——它們是**結構性兜底**（失效形狀 (A)）。
//
// 兩層各用「lease 綁目錄 A、writer 嘗試開目錄 B」製造 ownership mismatch，
// 各自可被獨立打紅。

// leaseMismatchApp：lease 走 production 流程綁在目錄 A，之後把 a.stateDir 換成
// 目錄 B。回傳的 App 手上那份 lease 對 B **無效**。
func leaseMismatchApp(t *testing.T) (a *App, lease *stateLease, dirB string) {
	t.Helper()
	a = auditLifecycleApp(t)
	lease = acquireLease(t, a) // 綁在目錄 A（＝當時的 a.stateDir）

	short, err := os.MkdirTemp("/tmp", "wbmis")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	dirB = filepath.Join(short, ".workbench")
	if err := os.MkdirAll(filepath.Join(dirB, "recordings"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.stateDir = dirB
	return a, lease, dirB
}

// TestOpenStateWritersRejectsMismatchedLease（第 1 層）
//
// 正題斷言：目錄 B 的遞迴磁碟快照**零異動**，且下游 opener 一個都沒被呼叫
// （registry／eventSink／replayIndex／manager 全為 nil、auditState 仍 unavailable）。
//
// 預期紅：刪掉 `openStateWriters` 開頭的 `lease.ownsStateDir` 檢查 → 目錄 B 出現
// sessions.json／audit.jsonl／events.jsonl。
func TestOpenStateWritersRejectsMismatchedLease(t *testing.T) {
	a, lease, dirB := leaseMismatchApp(t)
	before := siSnapshot(t, dirB)

	if a.openStateWriters(lease) {
		t.Error("lease 綁的是別的目錄，openStateWriters 必須拒絕（fail closed）")
	}

	if diffs := siDiff(before, siSnapshot(t, dirB)); len(diffs) != 0 {
		t.Fatalf("被拒的 openStateWriters 不得變更 state directory 的任何磁碟事實，實得：\n%s",
			strings.Join(diffs, "\n"))
	}
	if a.registry != nil || a.eventSink != nil || a.replayIndex != nil || a.manager != nil {
		t.Fatalf("下游 opener 一個都不得被呼叫：registry=%v eventSink=%v replayIndex=%v manager=%v",
			a.registry != nil, a.eventSink != nil, a.replayIndex != nil, a.manager != nil)
	}
	if a.auditState != auditUnavailable {
		t.Fatalf("被拒時不得進入 ready，實得 auditState=%d", a.auditState)
	}
	if !strings.Contains(a.startupErr, "ownership lease") {
		t.Fatalf("拒絕原因必須 fail loud，實得 %q", a.startupErr)
	}
}

// TestOpenWireSegmentsRejectsMismatchedLease（第 2 層）
//
// 這一層獨立於第 1 層：測試直接呼叫 `openWireSegments`，繞過 openStateWriters。
//
// 正題斷言：目錄 B 零異動 ＋ `a.wireSegments` 仍為 nil。
// 預期紅：刪掉 `openWireSegments` 開頭的 `lease.ownsStateDir` 檢查 → 目錄 B 出現
// wire-segments.jsonl。
func TestOpenWireSegmentsRejectsMismatchedLease(t *testing.T) {
	a, lease, dirB := leaseMismatchApp(t)
	before := siSnapshot(t, dirB)

	a.openWireSegments(lease)

	if diffs := siDiff(before, siSnapshot(t, dirB)); len(diffs) != 0 {
		t.Fatalf("被拒的 openWireSegments 不得變更 state directory 的任何磁碟事實，實得：\n%s",
			strings.Join(diffs, "\n"))
	}
	if a.wireSegments != nil {
		t.Fatal("被拒時不得建立 SegmentSet")
	}
	if !strings.Contains(a.startupErr, "ownership lease") {
		t.Fatalf("拒絕原因必須 fail loud，實得 %q", a.startupErr)
	}
}

// TestValidLeaseOpensBothWriterLayers（正向對照：好行為沒被誤殺）
//
// 目錄相符的有效 lease 必須讓兩層都開得起來。少了這一條，兩層檢查各自改成
// 「一律拒絕」也會全綠。
func TestValidLeaseOpensBothWriterLayers(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := acquireLease(t, a)

	if !a.openStateWriters(lease) {
		t.Fatalf("目錄相符的有效 lease 必須開得起來，startupErr=%q", a.startupErr)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if a.registry == nil || a.eventSink == nil || a.manager == nil {
		t.Fatalf("下游 writer 必須被建立：registry=%v eventSink=%v manager=%v",
			a.registry != nil, a.eventSink != nil, a.manager != nil)
	}
	if a.wireSegments == nil {
		t.Fatal("第 2 層（SegmentSet）必須被建立")
	}
	if strings.Contains(a.startupErr, "ownership lease") {
		t.Fatalf("有效 lease 不得產生 ownership 拒絕訊息，實得 %q", a.startupErr)
	}
}
