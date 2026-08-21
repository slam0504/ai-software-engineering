package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
)

func TestSessionHostsRegistryBasics(t *testing.T) {
	a, _ := newTestApp(t)
	a.putHost(&sessionHost{wsid: "w1", provider: "claude", sockPath: "/tmp/a.sock"})
	a.putHost(&sessionHost{wsid: "w2", provider: "claude", sockPath: "/tmp/b.sock"})
	a.putHost(&sessionHost{wsid: "w3", provider: "codex"})
	if a.hostFor("w1").sockPath == a.hostFor("w2").sockPath {
		t.Fatal("每個 WSID 必須有獨立 socket 路徑（§3.3）")
	}
	if len(a.snapshotHosts()) != 3 || len(a.hostsOf("claude")) != 2 {
		t.Fatal("snapshot／hostsOf 不正確")
	}
	if got := a.dropHost("w1"); got == nil || got.wsid != "w1" {
		t.Fatalf("dropHost 必須回傳被移除的 host（take-then-dispose）：%+v", got)
	}
	if a.hostFor("w1") != nil || len(a.snapshotHosts()) != 2 {
		t.Fatal("dropHost 未移除")
	}
	if a.dropHost("w1") != nil {
		t.Fatal("已移除的 wsid 必須回 nil")
	}
	if len(a.hostsOf("nobody")) != 0 || a.hostsOf("nobody") == nil {
		t.Fatal("hostsOf 空集合應回非 nil 空 slice（與 snapshotHosts 一致）")
	}
}

// takeHost 的 identity check：同一個 WSID 上換代之後，舊 host 的收尾不得把新
// host 一併抹掉（start 交易 abort 的 reaper 與新的 startClaude 會並行）。
func TestTakeHostOnlyRemovesTheSameHost(t *testing.T) {
	a, _ := newTestApp(t)
	old := &sessionHost{wsid: "w1", provider: "claude"}
	fresh := &sessionHost{wsid: "w1", provider: "claude"}
	a.putHost(old)
	a.putHost(fresh) // 換代
	if a.takeHost(old) {
		t.Fatal("舊 host 不得取得已換代 WSID 的處置權")
	}
	if a.hostFor("w1") != fresh {
		t.Fatal("新 host 被誤刪")
	}
	if !a.takeHost(fresh) || a.hostFor("w1") != nil {
		t.Fatal("目前登記的 host 必須可被取出")
	}
	if a.takeHost(nil) {
		t.Fatal("nil host 必須回 false")
	}
}

// socket 槽位 free-list：唯一性靠配置保證，歸還靠 identity check（見
// reserveSockIndex／releaseSockIndex doc）。
func TestSockIndexFreeList(t *testing.T) {
	a, _ := newTestApp(t)
	h1, h2 := &sessionHost{wsid: "w1", sockIndex: -1}, &sessionHost{wsid: "w2", sockIndex: -1}
	i1, err := a.reserveSockIndex(h1)
	if err != nil || i1 != 0 {
		t.Fatalf("第一個槽位應為 0：%d %v", i1, err)
	}
	i2, err := a.reserveSockIndex(h2)
	if err != nil || i2 != 1 {
		t.Fatalf("併發配置不得撞號：%d %v", i2, err)
	}
	h1.sockIndex, h2.sockIndex = i1, i2

	// identity check：h2 不能把 h1 的槽位放掉
	stale := &sessionHost{wsid: "w1", sockIndex: i1}
	a.releaseSockIndex(stale)
	if next, _ := a.reserveSockIndex(&sessionHost{sockIndex: -1}); next == i1 {
		t.Fatal("非擁有者不得歸還槽位（identity check 失效 → 兩個 session 共用 socket）")
	}

	a.releaseSockIndex(h1) // 真正的擁有者歸還後才可重用
	h3 := &sessionHost{wsid: "w3", sockIndex: -1}
	if got, err := a.reserveSockIndex(h3); err != nil || got != i1 {
		t.Fatalf("釋放後的槽位必須可重用：%d %v", got, err)
	}
}

// 上限用盡一律 fail loud，不降級成共用 socket。
func TestSockIndexExhaustionFailsLoud(t *testing.T) {
	a, _ := newTestApp(t)
	for i := 0; i < maxApprovalSockets; i++ {
		if _, err := a.reserveSockIndex(&sessionHost{sockIndex: -1}); err != nil {
			t.Fatalf("第 %d 個槽位不該失敗：%v", i, err)
		}
	}
	if _, err := a.reserveSockIndex(&sessionHost{sockIndex: -1}); err == nil {
		t.Fatal("槽位用盡必須回錯")
	}
}

func TestSnapshotHostsIsRaceFree(t *testing.T) {
	a, _ := newTestApp(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); a.putHost(&sessionHost{wsid: appcore.WSID(fmt.Sprint(i))}) }(i)
		go func() { defer wg.Done(); _ = a.snapshotHosts() }()
	}
	wg.Wait()
}
