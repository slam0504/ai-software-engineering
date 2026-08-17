package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

// sessionHost：per-WSID 版本的「單例 ownership」（M3b Phase 2，§3.3）——把原本
// 掛在 App 上的 broker／claudeSess／claudeLease／runner／track 等欄位，逐 provider
// 搬進以 WSID 為 key 的 registry。Task 8 遷入 Claude 側、Task 9 遷入 Codex 側，
// App 上對應的九個單例欄位隨之全部刪除。
//
// Claude 與 Codex 的 host 形狀刻意不同，因為兩者的隔離維度不同：Claude 每個
// session 是獨立子行程（sess／broker／socket／MCP config 全部 per-host），Codex
// 所有 session 共用同一條 codex.Conn，per-host 的是 runner／threadID／track／
// lease，隔離改由 App 的 codex dispatcher 依 threadId／turnId 分流達成。
//
// # 欄位併發規約（Task 8 凍結）
//
// 除 sessionID 外的欄位一律「publish 前寫定、publish 後不可變」：startClaude 在
// putHost 之前把 sess／sockPath／mcpPath／broker／pumpDone／lease／teardownFn 全部
// 填好才登記，putHost 的 a.mu 釋放即為 happens-before 邊界，之後的讀者（hostFor／
// snapshotHosts／hostsOf 拿到的指標）可以在鎖外安全讀取。
//
// 這條規約由「不把 host 交給 goroutine 直接使用」來維持（review Important #2）：
// startClaude 的兩條 pump goroutine 在 publish 之前就啟動，因此只拿到窄值（wsid／
// provider／broker）與兩個 closure；它們接觸得到的 host 狀態只有 sessionID（經
// hostSessionID／setHostSessionID，在 a.mu 下）與建構時就寫定的 wsid。goroutine
// 作用域內沒有 host 識別字可寫，所以不會、也無法讀到 publish 前才填入的 sess／
// pumpDone／lease／teardownFn——closure 仍然捕獲 host 指標，安全性來自「它們只走
// a.mu 下的存取器」，不是來自「拿不到指標」。日後在這兩條 goroutine 內新增對 host
// 其他欄位的存取，必須重新檢查這條規約。
//
// sessionID 是唯一 publish 後仍會變動的欄位（claude init 抵達時回填），一律經
// hostSessionID／setHostSessionID 在 a.mu 下存取。
//
// 處置（teardown、關 broker、釋放 socket index）一律先 takeHost 取出——取出後沒有
// 任何新讀者能拿到該指標，才在鎖外獨佔處置（見 dropHost doc）。
type sessionHost struct {
	wsid     appcore.WSID
	provider contract.Provider

	sess *claude.Session
	// sockPath：per-host approval broker socket。檔名用 approval-<sockIndex>.sock
	// 而**不是** WSID：unix sockaddr 的 sun_path 只有 ~104 bytes，26 字元的 ULID
	// 會讓 production 的 `<cwd>/.workbench` 直接 bind 失敗（review Critical）。
	// socket 是 ephemeral runtime 資源、不是 identity——identity 在 registry。
	//
	// 已知預算：approval-0.sock（15 bytes）比 M3b 之前的 approval.sock（13 bytes）
	// 長 2 bytes，因此 stateDir 的可用長度約 88 bytes。這個修正把檔名從 40 bytes
	// 壓回 15，但沒有徹底消除這個維度——極深的 workspace 路徑仍可能撐破上限。
	sockPath   string
	sockIndex  int    // reserveSockIndex 配到的槽位（-1 = 未配置）
	mcpPath    string // per-WSID MCP config（普通檔案，不受 sun_path 限制）
	broker     *approval.Broker
	pumpDone   <-chan struct{}
	teardownFn func() error
	lease      *appcore.RecordingLease

	// ---- Codex 側（Task 9）----
	//
	// runner／threadID 在 publishCodexHost 之前就寫定、之後不可變（同上方規約）。
	// track 是個帶自己 mutex 的值型別，publish 後由 dispatcher 與 TerminateSession
	// 併發存取——一律以 &h.track 操作，不得複製 sessionHost。
	runner   *codex.ThreadRunner
	threadID string
	track    appcore.TurnTrack

	// ---- §3.4.4 session 級錄流證據（[]WireSegmentRef）----
	//
	// wireGen／wireStart 記錄「本 session 開始時掛著的 connection-wide 錄流
	// generation，以及它在那份錄流裡的起始 frame」。兩者都在 publishCodexHost
	// **之前**由 beginWireSegment 寫定、之後不可變（同上方規約）。
	//
	// 刻意存 *wirelog.Generation 而不只是 wire_log_id：收尾當下 a.wireGen 可能
	// 已經指向下一個 generation（server 意外死亡 → ensureAppServer 重建），拿
	// 那一份的 frame 計數當尾界會把別的 generation 的 frame 算進來。Generation
	// 的 frame 計數在 Finalize 之後凍結，握著原本那份就永遠讀得到正確尾界。
	//
	// wireSegOnce 讓「這個 session 至多留下一段 SegmentRef」——teardown 有多條
	// 路徑（EndSession／forcedShutdown／StartTurn rollback）且 lease 慣例是冪等。
	wireGen     *wirelog.Generation
	wireStart   int
	wireSegOnce sync.Once

	sessionID string
}

// maxApprovalSockets：同時存在的 approval socket 上限——2 provider ×
// appcore.MaxSessionsPerProvider。free-list 因此天然有界，配不到即 fail loud
// （理論上不可達：Manager 的 slot 上限先擋住）。
const maxApprovalSockets = 2 * appcore.MaxSessionsPerProvider

// approvalSockPath：short-path socket 檔名（見 sessionHost.sockPath doc）。
func approvalSockPath(stateDir string, index int) string {
	return filepath.Join(stateDir, "approval-"+strconv.Itoa(index)+".sock")
}

// reserveSockIndex：配置一個目前沒人在用的最小 socket index 給 h，並把 h 登記為
// 該 index 的擁有者。唯一性由**配置**保證（不是靠雜湊碰撞機率），釋放一律經
// releaseSockIndex 的 identity check。
//
// 刻意不用「掃描 sessionHosts 找沒被用到的 index」：host 要等全部欄位填妥才
// publish，掃描會讓兩個併發的 startClaude 配到同一個 index。獨立的 owner map 讓
// 「檢查 ＋ 佔用」在同一個臨界區內原子完成。
func (a *App) reserveSockIndex(h *sessionHost) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sockIndexOwner == nil {
		a.sockIndexOwner = map[int]*sessionHost{}
	}
	for i := 0; i < maxApprovalSockets; i++ {
		if a.sockIndexOwner[i] == nil {
			a.sockIndexOwner[i] = h
			return i, nil
		}
	}
	return -1, fmt.Errorf("app: approval socket 槽位已滿（上限 %d）", maxApprovalSockets)
}

// releaseSockIndex：歸還 h 佔用的 socket index（未配置或已被別人接手一律 no-op）。
// identity check 是必要的：同一個 host 的 teardown 可能被走兩條路徑各跑一次
// （OnceValue 之外還有 EndSession／NewSession 各自的 fresh 閉包），若無條件依
// index 歸還，會把已經配給下一個 session 的槽位放掉，兩個 session 就會共用 socket。
func (a *App) releaseSockIndex(h *sessionHost) {
	if h == nil || h.sockIndex < 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sockIndexOwner[h.sockIndex] == h {
		delete(a.sockIndexOwner, h.sockIndex)
	}
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
// 指標指向的物件。teardown 會把 host 持有的資源處置掉——關掉 broker（listener
// 一關就 unlink socket 檔案）、finalize lease、歸還 socket index——處置之後這些
// 欄位的值仍在，但指向的東西已經不能用了。若同時有別的路徑經 hostFor 拿到同一
// 指標去 dial 那個 socket 或 Send 那個 sess，就會操作到正在消失的資源。因此處置
// 一律「先在鎖內取出（此後沒有新讀者能取得該指標）→ 再於鎖外處置」，沿用 repo
// 既有 codex.Single.Take() 的取出即獨佔語意。
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
