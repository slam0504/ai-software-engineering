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
	a.dropHost("w1")
	if a.hostFor("w1") != nil || len(a.snapshotHosts()) != 2 {
		t.Fatal("dropHost 未移除")
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
