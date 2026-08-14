package main

import (
	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// sessionHost：per-WSID 版本的「單例 ownership」（M3b Phase 2，§3.3）——把目前
// 掛在 App 上的 broker／claudeSess／claudeLease／runner／track 等欄位，逐 provider
// 搬進以 WSID 為 key 的 registry。本檔（Task 7）只新增這個型別與存取方法；既有
// App 單例欄位一個都不動，也還沒有任何呼叫端寫入 sessionHosts——那是 Task 8（Claude）
// 與 Task 9（Codex）的事。
type sessionHost struct {
	wsid     appcore.WSID
	provider contract.Provider

	sess       *claude.Session
	sockPath   string // per-WSID approval broker socket（§3.3；每個 WSID 各自獨立）
	mcpPath    string // per-WSID Codex MCP socket（同上）
	broker     *approval.Broker
	pumpDone   <-chan struct{}
	teardownFn func() error
	lease      *appcore.RecordingLease
	threadID   string
	track      appcore.TurnTrack
	sessionID  string
}

// hostFor：回傳 wsid 對應的 sessionHost；不存在回 nil。呼叫端讀到的是指標本身
// （非副本）——host 內部欄位仍可能被其他持鎖路徑修改，這點與既有單例欄位的
// 使用慣例一致，讀者仍需自行注意欄位層級的併發存取規約。
func (a *App) hostFor(wsid appcore.WSID) *sessionHost {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionHosts[wsid]
}

// putHost：登記／覆寫 wsid 對應的 sessionHost。
func (a *App) putHost(h *sessionHost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionHosts == nil {
		a.sessionHosts = make(map[appcore.WSID]*sessionHost)
	}
	a.sessionHosts[h.wsid] = h
}

// dropHost：移除 wsid 對應的 sessionHost（不存在為 no-op）。
func (a *App) dropHost(wsid appcore.WSID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessionHosts, wsid)
}

// snapshotHosts：回傳目前所有 sessionHost 的副本切片（非底層 map 的別名）——
// shutdown 需要在不持有 a.mu 的情況下逐一 teardown，若回傳的是 map 迭代中的引用
// 或共享底層陣列，teardown 過程中其他 goroutine 對 map 的並行寫入會造成資料競爭
// 或漏掉／重複 teardown；因此這裡明確配置一個新 slice 並複製指標。
func (a *App) snapshotHosts() []*sessionHost {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*sessionHost, 0, len(a.sessionHosts))
	for _, h := range a.sessionHosts {
		out = append(out, h)
	}
	return out
}

// hostsOf：回傳指定 provider 的 sessionHost 副本切片（理由同 snapshotHosts）。
func (a *App) hostsOf(provider contract.Provider) []*sessionHost {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*sessionHost
	for _, h := range a.sessionHosts {
		if h.provider == provider {
			out = append(out, h)
		}
	}
	return out
}
