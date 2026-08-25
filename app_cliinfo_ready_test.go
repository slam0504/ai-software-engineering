package main

import (
	"context"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

// ---- CLIInfo ready 契約（spec: docs/superpowers/specs/2026-08-25-cliinfo-late-connect-design.md）----
//
// ready ＝ 唯一 startup owner 對快照的寫入已全部完成（owner 函式主體已返回）。
// 這裡守 §4 的 Go 測試格：ready 的四個時序格、事件恰發布一次、以及單一
// finalizer 的凍結順序（ready 旗標 → emit → endStartupLifecycle）。

// TestCLIInfoReadyFollowsStartupOwnerLifecycle（格 1＋2）
//
// startup 停在欄位發布中途（owner 未終止）：ready 必須回 "false"——呼叫端此刻
// 拿到的空欄位是「還沒定案」，不是「定案了就是空」。owner 收斂後同一個呼叫必須
// 回 "true"。
//
// 預期紅：CLIInfo 回傳 map 沒有 ready key（實得 ""）。
func TestCLIInfoReadyFollowsStartupOwnerLifecycle(t *testing.T) {
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
	if got := a.CLIInfo()["ready"]; got != "false" {
		t.Fatalf(`startup 進行中 ready 必須是 "false"，實得 %q`, got)
	}
	close(release)
	<-done
	if got := a.CLIInfo()["ready"]; got != "true" {
		t.Fatalf(`startup owner 收斂後 ready 必須是 "true"，實得 %q`, got)
	}
}

// TestCLIInfoReadyTrueWhenMigrationBlocked（格 3）
//
// migration 無法判定權威 → startup 提前返回、一個 state binding 都不開。ready
// 仍必須定案為 "true"：ready 表示「值已定案」，不是「一切健康」；晚連線者不能
// 因為 startup 被擋就永遠等不到 ready。
func TestCLIInfoReadyTrueWhenMigrationBlocked(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	seedLegacyEscalation(t, legacyDirOf(a), "legacy-side")
	seedLegacyEscalation(t, a.stateDir, "current-side")

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if a.wsReg != nil {
		t.Fatal("測試前提不成立：migration 衝突必須真的擋下 startup")
	}
	if got := a.CLIInfo()["ready"]; got != "true" {
		t.Fatalf(`migration 被擋的 startup 也必須定案 ready="true"，實得 %q`, got)
	}
}

// TestCLIInfoReadyTrueWhenLeaseBlocked（格 4）
//
// lease 被另一個持有者占住 → startup 在 openStateWriters 之前就提前返回。
// 同格 3：ready 仍必須定案為 "true"。
func TestCLIInfoReadyTrueWhenLeaseBlocked(t *testing.T) {
	a := auditLifecycleApp(t)
	a.emitUI = func(string, any) {}
	l, err := singleinstance.Acquire(a.stateDir)
	if err != nil {
		t.Fatalf("測試前提：先占住 lease 失敗：%v", err)
	}
	t.Cleanup(func() { _ = l.Release() })

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if a.registry != nil || a.manager != nil {
		t.Fatal("測試前提不成立：lease 被占時 startup 必須被擋、不得開 writer")
	}
	if got := a.CLIInfo()["ready"]; got != "true" {
		t.Fatalf(`lease 被擋的 startup 也必須定案 ready="true"，實得 %q`, got)
	}
}

// TestCLIReadyEventPublishedExactlyOncePerOwner
//
// cli-ready 事件的發布權只屬於唯一 startup owner：完整跑完的 owner 恰發布一次；
// 之後被拒的 startup 呼叫（ownership 不成立）一次都不得發布。
func TestCLIReadyEventPublishedExactlyOncePerOwner(t *testing.T) {
	a := auditLifecycleApp(t)
	count := 0
	a.emitUI = func(name string, _ any) {
		if name == "workbench:cli-ready" {
			count++
		}
	}
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })
	if count != 1 {
		t.Fatalf("startup owner 必須恰發布一次 cli-ready，實得 %d 次", count)
	}

	a.startup(context.Background()) // 被拒：ownership 已用掉
	if count != 1 {
		t.Fatalf("被拒的 startup 不得發布 cli-ready，實得 %d 次", count)
	}
}

// TestCLIReadyEventOrderContract（finalizer 凍結順序）
//
// 單一 defer 閉包內的順序是契約：①同鎖落 ready 旗標 → ②emit → ③
// endStartupLifecycle。在注入的 emitUI（＝②的當下）驗兩個邊界：
//   - ①→②：此刻 CLIInfo 必須已回 ready="true"（mutation：旗標晚於 emit 落地，紅）。
//   - ②→③：此刻 startup lifecycle 必須仍在 running（mutation：
//     endStartupLifecycle 提前到 emit 之前，紅）。
func TestCLIReadyEventOrderContract(t *testing.T) {
	a := auditLifecycleApp(t)
	seen := false
	var readyAtEmit string
	var runningAtEmit bool
	a.emitUI = func(name string, _ any) {
		if name != "workbench:cli-ready" {
			return
		}
		seen = true
		readyAtEmit = a.CLIInfo()["ready"]
		a.shutMu.Lock()
		runningAtEmit = a.startupRunning
		a.shutMu.Unlock()
	}

	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if !seen {
		t.Fatal("測試前提不成立：owner 收斂時必須發布 cli-ready")
	}
	if readyAtEmit != "true" {
		t.Fatalf(`順序①→②被破壞：emit 當下 CLIInfo 必須已回 ready="true"，實得 %q`, readyAtEmit)
	}
	if !runningAtEmit {
		t.Fatal("順序②→③被破壞：emit 當下 startup lifecycle 必須仍在 running——" +
			"owner 的收斂訊號（endStartupLifecycle）要在事件之後，shutdown 才不會與 ready 發布交錯")
	}
}
