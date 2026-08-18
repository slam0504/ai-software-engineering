package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/claude"
)

// 稽核寫入器的生命週期守門（owner 2026-08-18 凍結語意）。
//
// 三條守門的分工：
//
//	①  取得 lease 後 startup 確實建立 audit writer（本檔守門 1）
//	①b audit writer 開啟失敗必須阻擋 startup（本檔守門 2）
//	②  ready 之後 auditF 不見＝不變量破壞，必須 fail loud（本檔守門 3）
//	③  沒取得 lease 的第二個 process 不建立 audit file、但仍有拒絕訊息
//	    —— 已由 main_singleinstance_test.go 的
//	    TestBlockedBareEntryMutatesNothingOnDisk（磁碟快照差異為空，audit.jsonl
//	    不得出現）＋橫幅斷言守住，不在此重複。
//
// oracle 一律不是受測對象自陳：守門 1 用磁碟上 audit.jsonl 的實際內容判定
// writer 真的通了（不是看 auditState 欄位），守門 2 用「後續 writer 有沒有被
// 建立」判定 startup 真的被擋（不是看回傳值）。

// auditLifecycleApp：只帶 openStateWriters 需要的最小前置——刻意不用
// newTestAppIn，因為那個 helper 會先把 registry／sink／audit 都開好，openStateWriters
// 自己開了什麼就量不到了（失效形狀 (B)：同一條 setup 同時滿足多個前提）。
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

// TestStartupWithLeaseOpensAuditWriter（守門 1）
//
// 正題斷言：openStateWriters 之後，a.audit() 寫的那筆事件**出現在磁碟上的
// audit.jsonl 裡**。mutation「拿掉 openStateWriters 裡的 a.auditF = f」預期紅在
// 這一條——不是紅在「auditState 不等於 ready」，那是受測對象自陳。
func TestStartupWithLeaseOpensAuditWriter(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := newTestStateLease(a.stateDir)

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
	if breaks := a.auditInvariantBreaks(); len(breaks) != 0 {
		t.Fatalf("正常路徑不得回報不變量破壞，實得 %v", breaks)
	}
}

// TestAuditOpenFailureBlocksStartup（守門 2）
//
// 用「audit.jsonl 這個路徑是目錄」逼 OpenFile 失敗（EISDIR）——不需要改權限，
// 在 CI 的 root 環境也照樣失敗。
//
// 正題斷言：events.jsonl **不存在**。也就是 startup 真的停在 audit 這一步，而
// 不是「回了 false 但後面照樣開了 writer」。回傳值與 startupErr 只是補充。
func TestAuditOpenFailureBlocksStartup(t *testing.T) {
	a := auditLifecycleApp(t)
	if err := os.MkdirAll(filepath.Join(a.stateDir, "audit.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	lease := newTestStateLease(a.stateDir)

	// 刻意用 Errorf 不用 Fatalf：回傳值只是補充證據，正題斷言是下面的磁碟事實，
	// 早退會讓 mutation 紅在補充證據上（gate 規則 2：要紅在正題那一條）。
	if a.openStateWriters(lease) {
		t.Error("audit writer 開啟失敗時 openStateWriters 必須回 false（fail closed）")
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("audit 開啟失敗必須阻擋 startup——後續 writer 不得被建立，events.jsonl stat=%v", err)
	}
	if !strings.Contains(a.startupErr, "稽核寫入器開啟失敗") {
		t.Fatalf("拒絕原因必須是使用者看得懂的訊息，實得 %q", a.startupErr)
	}
}

// TestAuditWriterLostAfterReadyFailsLoud（守門 3）
//
// ready 之後把 writer 拿掉，模擬「不該發生但發生了」。以前這條路徑是靜默
// return——那正是失效形狀 (C)（保證只寫在註解裡）。
//
// 正題斷言：auditInvariantBreaks() 收到這筆事件種類 ＋ startupErr 出現使用者
// 看得到的說明。兩者都不是「audit() 有沒有 panic」——owner 明確排除無條件 panic。
func TestAuditWriterLostAfterReadyFailsLoud(t *testing.T) {
	a := auditLifecycleApp(t)
	lease := newTestStateLease(a.stateDir)
	if !a.openStateWriters(lease) {
		t.Fatalf("前提：openStateWriters 必須成功，startupErr=%q", a.startupErr)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })
	a.startupErr = "" // 前提隔離：橫幅斷言要量的是本次破壞寫進去的訊息

	a.auditMu.Lock()
	f := a.auditF
	a.auditF = nil
	a.auditMu.Unlock()
	t.Cleanup(func() { _ = f.Close() })

	a.audit("session_removed", map[string]any{"marker": "guard-3"})

	breaks := a.auditInvariantBreaks()
	if len(breaks) != 1 || breaks[0] != "session_removed" {
		t.Fatalf("ready 之後 writer 消失必須被記為不變量破壞（含事件種類），實得 %v", breaks)
	}
	if !strings.Contains(a.startupErr, "不變量破壞") {
		t.Fatalf("不變量破壞必須有使用者可見的說明，實得 %q", a.startupErr)
	}
}

// TestAuditWithoutLeaseStaysSilent（反向 mutation：好行為沒被誤殺）
//
// 沒有 lease ＝ unavailable。這時丟棄稽核是**正確行為**，不得被記成不變量破壞
// ——否則守門 3 會把每一個「還沒啟動完成」的呼叫都誤判成事故。
func TestAuditWithoutLeaseStaysSilent(t *testing.T) {
	a := auditLifecycleApp(t)

	a.audit("session_removed", map[string]any{"marker": "no-lease"})

	if breaks := a.auditInvariantBreaks(); len(breaks) != 0 {
		t.Fatalf("尚未取得 lease 時丟棄稽核是正確行為，不得回報破壞，實得 %v", breaks)
	}
	if a.startupErr != "" {
		t.Fatalf("尚未取得 lease 時不得寫入不變量破壞橫幅，實得 %q", a.startupErr)
	}
	if _, err := os.Stat(filepath.Join(a.stateDir, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("沒有 lease 不得建立 audit.jsonl，stat=%v", err)
	}
}
