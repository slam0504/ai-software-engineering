package main

import (
	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// sessionHost：per-WSID 版本的「單例 ownership」（M3b Phase 2，§3.3）——把原本
// 掛在 App 上的 broker／claudeSess／claudeLease／runner／track 等欄位，逐 provider
// 搬進以 WSID 為 key 的 registry。Task 8 已把 Claude 側全部遷入（六個單例欄位隨之
// 刪除）；Codex 的 runner／track／codexLease 仍在 App 上，Task 9 才搬。
//
// # 欄位併發規約（Task 8 凍結）
//
// 除 sessionID 外的欄位一律「publish 前寫定、publish 後不可變」：startClaude 在
// putHost 之前把 sess／sockPath／mcpPath／broker／pumpDone／lease／teardownFn 全部
// 填好才登記，putHost 的 a.mu 釋放即為 happens-before 邊界，之後的讀者（hostFor／
// snapshotHosts／hostsOf 拿到的指標）可以在鎖外安全讀取。
//
// sessionID 是唯一 publish 後仍會變動的欄位（claude init 抵達時回填），一律經
// hostSessionID／setHostSessionID 在 a.mu 下存取。
//
// teardown 會「寫」host（關 broker、finalize lease），因此必須先 takeHost 取出
// ——取出後沒有任何新讀者能拿到該指標，才在鎖外獨佔處置（見 dropHost doc）。
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
// （非副本）——欄位層級的併發規約見 sessionHost 型別 doc。
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

// dropHost：移除 wsid 對應的 sessionHost 並回傳被移除的那個（不存在回 nil）。
//
// take-then-dispose（Task 8 凍結）：a.mu 只保護「map 裡放的是哪些指標」，不保護
// 指標指向的物件；teardown 會寫 host（關 broker、finalize lease），若同時有別的
// 路徑經 hostFor 拿到同一指標讀 sess／broker 就是 race。因此處置一律「先在鎖內
// 取出（此後沒有新讀者能取得該指標）→ 再於鎖外 teardown」，沿用 repo 既有
// codex.Single.Take() 的取出即獨佔語意。
func (a *App) dropHost(wsid appcore.WSID) *sessionHost {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.sessionHosts[wsid]
	delete(a.sessionHosts, wsid)
	return h
}

// takeHost：identity-checked 版 dropHost——只有 registry 目前登記的正好是 h 時才
// 移除，回傳是否由本次呼叫取得處置權。teardown 用它而非 dropHost：同一個 WSID
// 上可能已經換上下一個 session 的 host（start 交易 abort 後 reaper 立即收尾，與
// 新的 startClaude 並行），無條件 delete 會把新 host 一併抹掉。
func (a *App) takeHost(h *sessionHost) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h == nil || a.sessionHosts[h.wsid] != h {
		return false
	}
	delete(a.sessionHosts, h.wsid)
	return true
}

// hostSessionID：讀 host 目前的 provider session id（publish 後仍會變動的唯一
// 欄位，見 sessionHost doc）。h 為 nil 回空字串——收尾與啟動交錯時呼叫端拿到
// nil host 是正常情形（原 claudeSessionIDSnapshot 在無 session 時同樣回空）。
func (a *App) hostSessionID(h *sessionHost) string {
	if h == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return h.sessionID
}

// setHostSessionID：provider init 抵達時回填 session id（見 hostSessionID）。
func (a *App) setHostSessionID(h *sessionHost, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h.sessionID = id
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

// hostsOf：回傳指定 provider 的 sessionHost 副本切片（理由同 snapshotHosts；
// 空集合一律回非 nil 空 slice，與 snapshotHosts 一致）。
func (a *App) hostsOf(provider contract.Provider) []*sessionHost {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*sessionHost, 0, len(a.sessionHosts))
	for _, h := range a.sessionHosts {
		if h.provider == provider {
			out = append(out, h)
		}
	}
	return out
}
