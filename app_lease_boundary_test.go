package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

// ---- lease 邊界：兩個背景 writer 都停了才放手（owner 2026-08-19 F1）----
//
// 要守的不變量是「lease 釋放之後不得再有任何 state mutation」。背景 writer 有
// **兩個**，各自獨立：
//
//	frame 展開 worker → drainWireFrameJobs 回報（既有守門在 app_wire_frame_jobs_test.go）
//	Claude pump       → forcedShutdown 回報（本檔）
//
// oracle 一律不是 App 自陳的欄位：
//   - 「lease 放掉了沒」→ **同一個 process 重新 flock**。flock 綁 open file
//     description，同一個 process 用新的 fd 取鎖照樣會被自己擋住，所以成功與否
//     完全由 kernel 決定，跟 a.lease 被設成什麼無關。
//   - 「registry 還能不能寫」→ sessions.json 的**位元組內容**。
//
// 這一整組刻意用 production 取鎖流程（真 flock），不用 newTestStateLease：
// test-only capability 的 release() 是 no-op，用它的話「有沒有真的釋放」量不到。

// useRealLease：把測試 App 的 test-only lease 換成 production 流程取得的真 flock
// lease（見上方 doc）。回傳那份 lease 供反向斷言使用。
func useRealLease(t *testing.T, a *App) *stateLease {
	t.Helper()
	a.lease = nil
	lease, err := a.acquireStateLease()
	if err != nil {
		t.Fatalf("取得真 ownership lease 失敗：%v", err)
	}
	t.Cleanup(func() { _ = lease.release() }) // shutdown 已放掉時是 no-op
	return lease
}

// leaseReleased：shutdown 之後 lease 是不是真的放掉了——**同一個 process 重新
// Acquire**（見上方 doc）。取得成功就立刻還回去，不留給後續測試。
func leaseReleased(t *testing.T, stateDir string) bool {
	t.Helper()
	l, err := singleinstance.Acquire(stateDir)
	if err == nil {
		_ = l.Release()
		return true
	}
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return false
	}
	t.Fatalf("重新取鎖時發生非預期錯誤（測不出釋放與否）：%v", err)
	return false
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("讀 %s：%v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestShutdownReleasesLeaseWhenPumpsStopped（F1 情境 1：正向對照）
//
// 全部 pump 都收乾的正常收尾：lease 必須真的釋放，session registry 必須關閉。
//
// 沒有這條正向對照的話，「pump 卡住就保留」可以用「永遠保留」通過——那會讓每次
// 正常關閉都留下一把沒人放的鎖，使用者再也開不起第二次。
func TestShutdownReleasesLeaseWhenPumpsStopped(t *testing.T) {
	f := seedSessions(t, 1, 0)
	useRealLease(t, f.a)
	sessionsPath := filepath.Join(f.a.stateDir, "sessions.json")

	var steps []string
	f.a.hookShutdownStep = func(s string) { steps = append(steps, s) }
	f.a.shutdown(context.Background())

	if !leaseReleased(t, f.a.stateDir) {
		t.Fatal("pump 全部收乾的正常收尾必須釋放 ownership lease，實測仍被持有")
	}
	if !containsStep(steps, "instance_lease_release") {
		t.Fatalf("正常收尾必須走釋放路徑，實得步驟：%v", steps)
	}
	// 收尾順序：registry 在 lease 釋放之前就關了——之後任何遲到的 Bind 都改不了
	// 磁碟上那一份（owner 2026-08-19：確認背景工作結束 → 關 registry → 關 audit
	// writer → 釋放 lease）。
	before := fileDigest(t, sessionsPath)
	err := f.a.registry.Bind("late-session", f.a.workspaceDir, string(f.claude[0]))
	if !errors.Is(err, claude.ErrRegistryClosed) {
		t.Fatalf("收尾後的 Bind 必須回 ErrRegistryClosed，實得 %v", err)
	}
	if after := fileDigest(t, sessionsPath); after != before {
		t.Fatal("registry 關閉之後 sessions.json 不得再被改寫")
	}
}

// TestShutdownReleasesLeaseWhenTeardownFailsButPumpsStopped（F1 情境 1b：兩種
// 狀態必須分得開）
//
// 收尾**出錯**（錄流 meta 寫不進去——recordings 目錄改成唯讀）但 pump 正常收乾：
// 這是「檔案收尾失敗」，不是「goroutine 還活著」。lease 必須照常釋放。
//
// owner 2026-08-19 明確裁決：不要把所有 teardown error 都當成「pump 還活著」。
// 把兩者壓成 `pumpsStopped = (err == nil)` 的實作在這裡紅——那種寫法會讓每一次
// meta 寫入失敗都白白留著一把鎖，使用者從此開不起第二次。
func TestShutdownReleasesLeaseWhenTeardownFailsButPumpsStopped(t *testing.T) {
	a, _ := newTestApp(t)
	useRealLease(t, a)
	writeMultiTurnClaude(t, a)
	// recordCase 非空 ＝ 這個 session 帶錄流 lease，收尾時要寫 meta。
	if err := a.StartSession(wsidStr(t, a, "claude"), "hi", "", "claude-teardown-err", "task-c", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(a.stateDir, "recordings"), 0o755) })

	var steps []string
	a.hookShutdownStep = func(s string) { steps = append(steps, s) }
	a.shutdown(context.Background())

	if !containsStep(steps, "instance_lease_release") {
		t.Fatalf("收尾錯誤不等於背景 writer 還活著——lease 仍必須釋放，實得步驟：%v", steps)
	}
	if !leaseReleased(t, a.stateDir) {
		t.Fatal("pump 已收乾時，即使錄流 meta 寫入失敗也必須釋放 ownership lease")
	}
}

// TestShutdownRetainsLeaseWhenClaudePumpStuck（F1 情境 2）
//
// 單一 Claude session 的 pump 卡住（pumpDone 永不關閉）：teardown 仍會在 bounded
// window 內以逾時收尾並回錯，但那條 goroutine **還活著**，它會繼續寫 events sink
// 也會經 init 綁定改寫 sessions.json。這一刻釋放 lease，第二個實例就能在我們還在
// 寫的時候進場。
//
// 正題斷言：shutdown 之後**同一個 process 仍取不到鎖**（＝ lease 沒被釋放）。
//
// 另外兩條分得開的斷言，各自可被獨立打紅：
//   - 稽核明說是 pump 那一維（pumpsStopped=false、workerStopped=true）——把
//     teardown 的 error 拿來當「pump 還活著」的實作會在這裡露出來。
//   - registry **沒有**被關閉：保留路徑上那條還沒退出的 pump 仍持有效 lease，
//     它的寫入是合法的，關掉只會讓它靜默消失。
func TestShutdownRetainsLeaseWhenClaudePumpStuck(t *testing.T) {
	f := seedSessions(t, 1, 0)
	useRealLease(t, f.a)
	enableAudit(t, f.a)
	makeStuck(t, f.a, f.claude[0])

	// 擋住自然收尾 reaper：理由同 TestStuckClaudeSessionsShareSingleBoundedWindow
	// ——不擋的話收尾是被 reaper 起出來的，量到的不是 forcedShutdown 這條路徑。
	releaseReaper := make(chan struct{})
	f.a.hookClaudeReaperBeforeEndFlow = func() { <-releaseReaper }
	defer close(releaseReaper)

	timers := newFakeAfter()
	f.a.afterFn = timers.After
	var steps []string
	f.a.hookShutdownStep = func(s string) { steps = append(steps, s) }

	done := make(chan struct{})
	go func() { f.a.shutdown(context.Background()); close(done) }()
	timers.waitForOutstanding(t, 1) // quiesce
	timers.fireAll()
	timers.waitForOutstanding(t, 1) // kill
	timers.fireAll()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown 未收斂")
	}

	if leaseReleased(t, f.a.stateDir) {
		t.Fatal("pump 尚未結束時不得釋放 ownership lease——殘留 writer 會在第二個實例進場後繼續寫")
	}
	if !containsStep(steps, "instance_lease_retained") || containsStep(steps, "instance_lease_release") {
		t.Fatalf("必須走保留路徑而不是釋放路徑，實得步驟：%v", steps)
	}
	line := findAuditLine(t, f.a, "instance_lease_retained")
	for _, want := range []string{`"pumpsStopped":false`, `"workerStopped":true`} {
		if !strings.Contains(line, want) {
			t.Fatalf("稽核必須分得開是哪一個背景 writer 沒停（缺 %s）：%s", want, line)
		}
	}
	// 保留路徑不關 registry：尚未結束的 pump 在 lease 仍有效時完成寫入是正確行為。
	if err := f.a.registry.Bind("still-running", f.a.workspaceDir, string(f.claude[0])); err != nil {
		t.Fatalf("保留 lease 的路徑不得關閉 session registry，實得 %v", err)
	}
}

// TestLateInitBindRefusedAfterRegistryClosed（F1 情境 3）
//
// registry 關閉之後，**走 production pump callback 的**遲到 init 不得改寫
// sessions.json，而且不得靜默——`_ = a.registry.Bind(...)` 那種寫法在這裡會紅。
//
// 為什麼可以先關再開 session：收尾之後遲到的 callback 與「registry 已關、pump 還
// 在跑」是同一個狀態，這裡用先關再送 init 把那個狀態確定性地做出來，不必依賴
// shutdown 與 pump 的競速。
func TestLateInitBindRefusedAfterRegistryClosed(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	writeInitClaude(t, a, "late-init-sid")
	sessionsPath := filepath.Join(a.stateDir, "sessions.json")

	a.closeSessionRegistry() // production 收尾用的同一個入口
	before := fileDigest(t, sessionsPath)

	w := mustCreate(t, a, "claude")
	startClaudeOn(t, a, w)
	waitFor(t, "init 抵達 pump（session id 已回填）", func() bool {
		return a.hostSessionID(a.hostFor(w)) == "late-init-sid"
	})

	if after := fileDigest(t, sessionsPath); after != before {
		t.Fatal("registry 關閉後遲到的 init 綁定不得改寫 sessions.json")
	}
	line := findAuditLine(t, a, "claude_registry_bind_error")
	if !strings.Contains(line, `"closed":true`) || !strings.Contains(line, "late-init-sid") {
		t.Fatalf("寫入被拒必須 fail loud 並指出是哪一筆綁定，實得：%s", line)
	}
}

// containsStep：shutdown 步驟名是否出現過。
func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}

// findAuditLine：audit.jsonl 裡最後一筆指定 kind 的原始行（找不到即失敗——
// 「沒有這筆稽核」本身就是要打紅的情形）。
func findAuditLine(t *testing.T, a *App, kind string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	var found string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"kind":"`+kind+`"`) {
			found = line
		}
	}
	if found == "" {
		t.Fatalf("audit.jsonl 缺少 kind=%s 的稽核，實得：\n%s", kind, b)
	}
	return found
}
