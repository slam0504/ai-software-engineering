package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/yaml.v3"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/assist"
	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/escalation"
	"github.com/slam0504/sdlc-workbench/internal/evidence"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/gatepolicy"
	"github.com/slam0504/sdlc-workbench/internal/plan"
	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
	"github.com/slam0504/sdlc-workbench/internal/spec"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// pendingApproval：等待使用者裁決的 approval。wsid 是提出請求的那個 workspace
// session（M3b §3.3）——多 session 之後 provider 不再足以定位 approval 該回哪個
// slot／哪個 broker，決議事件必須發回原 session。
type pendingApproval struct {
	wsid     appcore.WSID
	provider string
	resolve  func(allow bool, reason string) error
}

// App 是薄綁定層：workspace／CLI 解析、Wails 事件出口與 provider 接線。
// 序列化、coordinator、lifecycle 與錄流收尾全部在 internal/appcore。
type App struct {
	ctx          context.Context
	workspaceDir string
	stateDir     string
	workspaceSrc string
	startupErr   string
	toolsDirPath string
	toolsSource  string
	nodePath     string
	diagramPath  string

	registry *claude.Registry
	manager  *appcore.Manager
	restore  *restoreStore

	// M3b §3.1：workspace session 的 durable metadata store（Task 2 的
	// wsregistry.Store；由 Task 6 的 registry 載入流程接上）。介面而非具體型別，
	// 讓建立交易的補償路徑可以用 stub 注入 persist 失敗。
	wsReg sessionRegistry

	// createDegradedMu／createDegradedSet：per-provider create-degraded latch
	// （§3.1）——CommitCreate 與 registry 回滾雙雙失敗時，Manager 與 registry
	// 已無法證明一致，該 provider 自此拒絕新建，直到 app restart 由 registry
	// 為權威還原。刻意沒有 in-process 解除路徑；既有 session 完全不受影響。
	createDegradedMu  sync.Mutex
	createDegradedSet map[contract.Provider]bool

	// hookForceCommitCreateError：測試注入——讓 commitCreate 直接回這個錯誤，
	// 用來驅動「CommitCreate 失敗」的兩條補償路徑（真實 CommitCreate 只在
	// closed／stale token 時失敗，兩者都無法在不破壞 Manager 狀態下重現）。
	hookForceCommitCreateError error

	auditMu sync.Mutex
	auditF  *os.File

	// mu：sessionHosts registry ＋ codex dispatcher 索引的互斥。
	mu sync.Mutex

	// codexSingle 持有的是 generation ownership 單位（Task 12／13，§3.4.2）：
	// 「app-server instance ＋ 該 generation 的 always-on 錄流」綁在一起，死亡
	// reaper／受控 replacement／shutdown 三條路徑才都拿得到那份 wire log 去
	// finalize。所有發布一律經 codex.RunOwnedHandshake（唯一會回傳 epoch、因此
	// 唯一掛得上 WatchGeneration 的入口），不再用 Single.Ensure。
	codexSingle  codex.Single[*codex.GenerationOwner]
	codexConn    *codex.Conn // wireCodexConn 記錄；interrupt 用（fake wire 測試同路徑）
	codexLoginID string

	// codexServerMu 序列化「建立或替換 codex app-server generation」的三條路徑
	// （ensureAppServer／RestartCodexServerRecorded／RecoverCodexRecording）。
	// Single.mu 本身只保護單次 replacement 交易，擋不住 ensureAppServer 的
	// check-then-act（兩個併發呼叫都看到「沒有 server」就會各起一個 process，
	// 其中一個隨即被另一個的 RunOwnedHandshake 收掉，白白多一次 spawn 與一份
	// 空 generation）。鎖序凍結為 codexServerMu → Single.mu，反向一律禁止；
	// onCodexGenerationFinalized（watcher callback）因此不得取這把鎖。
	codexServerMu sync.Mutex

	// codexServerFactory：測試注入的 app-server 工廠（nil = production 的
	// codex.StartAppServer）。fake wire 因此能走完整的 RunOwnedHandshake 編排
	// （finalize 舊 generation → 新 wire_log_id → start → attach → handshake →
	// 發布），不必真的 spawn codex CLI。
	codexServerFactory func() (codex.ProbeTarget, error)

	// ---- Codex connection-wide wire log（§3.4.6-7；Task 13）----
	//
	// wireMu 保護以下三個欄位。wireGen 是目前 generation 的錄流 handle（供
	// checkWireRecorder 輪詢 latch 住的寫入錯誤）；wireErr 是 App 層的 recorder
	// error latch——非 nil 即拒絕新 Codex session，只有「新 generation 的
	// recorder 掛載、handshake 與發布全部成功」才清除（§3.4.6），不因時間或重試
	// 次數自動解除。wireSeq 讓同一秒內的多次 replacement 也能拿到唯一 wire_log_id。
	wireMu  sync.Mutex
	wireGen *wirelog.Generation
	wireErr error
	wireSeq int

	// hookWireStep：測試注入——受控復原／replacement 的步驟順序探針（§3.4.7 的
	// 順序是凍結契約）。production 恆為 nil。
	hookWireStep func(step string)

	// sessionHosts（M3b Phase 2，§3.3）：per-WSID 的單例 ownership registry。
	// Task 8 遷入 Claude 側（broker／sess／sessionID／pumpDone／lease／teardownFn），
	// Task 9 遷入 Codex 側（runner／track／lease／threadID）——App 上已無任何
	// per-session 單例欄位。存取一律經 session_host.go 的 hostFor／putHost／
	// dropHost／takeHost／snapshotHosts／hostsOf，在 a.mu 下操作。
	sessionHosts map[appcore.WSID]*sessionHost

	// ---- Codex dispatcher（M3b §3.3；Task 9）----
	//
	// Codex 與 Claude 的根本差異：所有 session **共用同一條 codex.Conn**，因此
	// 隔離不是靠獨立行程，而是靠「共用連線上的每個 frame 都要被歸屬到正確的
	// WSID」。原本的 currentRunner()（路由到「當前那個」）在多 session 下必然
	// 串線，已刪除。三個索引都在 a.mu 下讀寫，查找順序見 codexWSIDFor。
	codexTurnWSID   map[string]appcore.WSID // turnId → WSID（turn/started 綁、turn/completed 解）
	codexThreadWSID map[string]appcore.WSID // threadId → WSID（EnsureThread 成功即綁、teardown 解）

	// codexPendingStarts：thread/start｜resume 送出到 response 抵達之間的登記。
	// 這段窗口內 server 可能先送 thread/started 之類帶著「client 還不知道的
	// threadId」的通知，光靠 codexThreadWSID 無法歸屬（見 codexWSIDFor）。
	codexPendingStarts map[uint64]appcore.WSID
	codexPendingSeq    uint64

	// codexStartMu：序列化 EnsureThread 那一段 RPC，使同一時間至多一筆 pending
	// start。理由見 codexWSIDFor——pending 窗口內的通知只帶 threadId，沒有任何
	// 欄位能把它對應回某一筆 in-flight 的 thread/start，兩筆並行 pending start 就
	// 是「原理上無法歸屬」。與其在正常併發下 fail loud，寧可讓第二個 start 等前
	// 一個的 response（EnsureThread 自帶 30s ctx，等待有界）。
	codexStartMu sync.Mutex

	// sockIndexOwner：approval socket 槽位的 free-list（index → 佔用它的 host）。
	// 在 a.mu 下讀寫，存取一律經 reserveSockIndex／releaseSockIndex。與
	// sessionHosts 分開是刻意的——host 要等欄位填妥才 publish，光靠掃描
	// sessionHosts 無法讓併發的 startClaude 原子地佔住槽位。
	sockIndexOwner map[int]*sessionHost

	// lastCreatedWSID：每個 provider 最近一次 CreateSession 成功的 WSID，供
	// legacyWSIDFor 的第 2 順位使用（見其 doc）。在 a.mu 下讀寫；Task 26 前端
	// 改為直接帶 WSID 之後連同 legacyWSIDFor 一併刪除。
	lastCreatedWSID map[contract.Provider]appcore.WSID

	apprMu      sync.Mutex
	apprPending map[string]*pendingApproval

	// shutdown gate（第四輪 review P1）：shutdown 先拒新 StartSession、等已取得
	// start ownership 的交易 accept／abort 完成，才 snapshot／teardown／Close／Take——
	// 堵住「Take() 之後 Ensure() 重新回填 server」的窗口。
	shutMu       sync.Mutex
	shuttingDown bool
	inflight     sync.WaitGroup

	emitUI                  func(name string, data any) // 測試注入；nil = wails runtime
	hookAfterProviderStart  func()                      // 測試注入：provider 啟動與 Accept 之間的 barrier
	hookDuringReset         func()                      // 測試注入：NewSession 的 teardown 完成與 restore reset 之間
	hookBeforeProviderStart func()                      // 測試注入：start ownership 取得後、provider 啟動前
	hookInServerTxn         func()                      // 測試注入：server 交易已登記、Ensure 未開始
	codexHostOverride       codexHost                   // 測試注入：fake wire 走 production StartSession 分支

	// 以下三個 hook 只服務 claude 自然收尾 reaper／forcedShutdown 的 ErrEndInProgress
	// benign 裁定（review P2：TestShutdownForcedWaitsForBoth flaky）——讓測試能
	// deterministic 驅動兩個時序分支，不靠 time.Sleep 猜。
	hookClaudeTeardownBarrier          func() // 測試注入：任一 claudeTeardown 真正執行的進入點（OnceValue 與 fresh 閉包皆經此）
	hookClaudeReaperBeforeEndFlow      func() // 測試注入：reaper 的 <-done 解除之後、呼叫 EndSessionFlow 之前
	hookForcedShutdownClaudeBeforeFlow func() // 測試注入：forcedShutdown 的 sess.Terminate() 之後、呼叫 EndSessionFlow 之前
	hookForcedShutdownClaudeBenign     func() // 測試注入：forcedShutdown 判定 ErrEndInProgress 為 benign、等待收斂之前

	// Gate 1（M2 Stage A：spec §3.5／§5.4）——spec.GitRepo ＋ gate.Service，
	// ensureGate() 惰性初始化，journal 落在 workspace 的 .workbench/gate.jsonl
	// （gitignored app state；不隨測試覆寫的 stateDir 漂移，永遠綁 workspace 本身）。
	specRepo            *spec.GitRepo
	gateSvc             *gate.Service
	gateJournal         *gate.Journal
	gateOnce            sync.Once
	gateInitErr         error
	gitIdentityOverride func() (name, email string, err error) // 測試注入：略過真實 git config 查詢

	// Plan workspace（Task 12：spec §7 Stage B）——planRepo 為 PlanScope 版
	// spec.GitRepo（plan/ 樹的 Preview/Confirm 兩階段 commit）；planGit／
	// planLoader 供 gate2 policy 與 SubmitPlanForApproval 的 committed-context
	// 讀取（git show／lineage），皆在 ensureGate() 內與 specRepo 同步惰性初始化。
	planRepo   *spec.GitRepo
	planGit    appGitRunner
	planLoader appPlanLoader

	// SpecAssist（Task 11：Stage A §5.1）——per-provider 至多一個 active 隔離
	// one-shot。assistActive 在 assistMu 下管理獨佔性；每個 generation 的 cancel
	// 供 shutdown reclaim，once 保證 result／abort／timeout／shutdown 任一先觸發
	// 即收一次（清 active flag＋endAppTxn＋close done）。
	assistMu            sync.Mutex
	assistActive        map[string]*assistGen
	assistRunnerFactory func(provider string) (assist.Runner, error) // 測試注入：換 fake Runner
	hookAssistBeforeTxn func()                                       // 測試注入：gen 已入 assistActive、beginAppTxn 未開始

	// PlanAssist provider capability preflight（M3a.1 Task 7：spec §3.4）——
	// key=binPath+"|"+完整 binary SHA-256 hex（不截斷）；只快取 OK 結果
	// （失敗每次重驗，binary 換回 pin 版即恢復）。binary 內容變更 → digest
	// 變 → miss 重驗。
	preflightMu    sync.Mutex
	preflightCache map[string]assist.PreflightResult

	// spec/ watcher（Task 12：spec §4 通知層）——遞迴監看納管樹，debounce 後
	// 觸發 Reconcile()。specWatchStop／specWatchDone 在 a.mu 下管理，供
	// shutdown 收斂：close(specWatchStop) 通知 goroutine 退出，goroutine defer
	// 呼叫 watcher.Close() 後才 close(specWatchDone)，shutdown 等 done 保證
	// watcher 真的關閉、goroutine 真的結束（不留 orphan）。
	specWatchStop chan struct{}
	specWatchDone chan struct{}

	// plan/ watcher（Task 12：watchSpecTree 模式鏡射，見其上方 doc——差異僅
	// 監看根換成 plan/、變更事件為 "plan:changed"）。獨立的 stop／done 欄位：
	// 兩個 watcher 各自獨立 goroutine，可同時運行、各自被 shutdown 收斂。
	planWatchStop chan struct{}
	planWatchDone chan struct{}

	// Evidence run（Task 20：M3a §4-5）——evidenceMu 保護 active run registry
	// （evidence_id → cancel func，供 shutdown reclaim，鏡射 assistActive）；
	// finalize（journal append）與 registry 移除在 RunEvidence 內同一臨界區
	// 完成，這是「恰一次 finalize」保證的落點。evidenceJournal／
	// evidenceCASDir／evidenceRegistryPath 於 startup 惰性建立於
	// .workbench/evidence/ 下（journal＝evidence.jsonl，worktree registry＝
	// worktrees.jsonl，同時做一次 CleanupOrphans／CleanOrphanTemps）。
	evidenceMu           sync.Mutex
	evidenceActive       map[string]context.CancelFunc
	evidenceJournal      *evidence.Journal
	evidenceCASDir       string
	evidenceRegistryPath string

	// evidenceContextLoaderOverride：測試注入，換掉 RunEvidence 傳給
	// evidence.Run 的 evidence.ContextLoader（production 用 a.planLoader）。
	// 唯一用途是讓測試能在 LoadAt／LoadOracleAt（ulid mint 之前執行）安插一個
	// barrier，重現「beginAppTxn 成功到 evidenceActive 登記之間」的 TOCTOU
	// 窗（task-20 review M1）。
	evidenceContextLoaderOverride evidence.ContextLoader

	// Escalation（Task 24：spec §3.8／§3.10）——workflowMu 是跨 journal 編排的
	// 唯一互斥：GateDecide 的凍結順序（reconcile→validator→stale 修復解除→
	// blocker→append）、escalation 收件匣的全部寫入、evidence finalize 的自動
	// 來源接線、watcher 觸發的 Reconcile 全部序列化在它底下。
	//
	// lock ordering：workflowMu → gate journal（gate.Service 內部 mu）→
	// escalation journal（escalation.Service 內部 mu）。evidenceMu 與 workflowMu
	// 不巢狀——RunEvidence 的 finalize 臨界區（evidenceMu）先完整結束，才另取
	// workflowMu 做 §3.8 (5)(6)(7) 接線（見 wireEvidenceEscalation）。
	//
	// 重入規約：public EscalationCreate／EscalationAck／EscalationResolve／
	// EscalationList 先取 workflowMu；已持有 workflowMu 的路徑（GateDecide 編排、
	// reconcileLocked、wireEvidenceEscalation）只准呼叫 esc*Locked 內部變體，
	// 否則同一 mutex 重入即死鎖。
	workflowMu sync.Mutex
	escSvc     *escalation.Service
	escJournal *escalation.Journal
	escOnce    sync.Once
	escInitErr error
	gateReg    gate.Registry // ensureGate 建好的 policy registry（§3.8 (2) 的 pre-validation 用）

	decideBarrierHook    func() // 測試注入：GateDecide blocker 檢查後、CommitDecision 前
	onWorkflowMuAttempt  func() // 測試注入：public EscalationCreate 於 workflowMu.Lock() 前
	onWorkflowMuAcquired func() // 測試注入：public EscalationCreate 取得 workflowMu 後、寫入前

	// runEvidenceCASHook（M3a.1 T8，§3.3.2）：測試注入，RunEvidence
	// beginAppTxn 成功後、workflowMu.Lock 前觸發——刻意早於 Lock（而非沿
	// decideBarrierHook 落在鎖內的位置），讓 hook 本體可以呼叫 GateDecide 之
	// 類同樣要取 workflowMu 的操作來模擬「按下與讀取之間換版」而不致死鎖，
	// 見 RunEvidence 函式 doc。
	runEvidenceCASHook func()
}

// assistGen：單一 SpecAssist 一次性執行的 generation（correlation 貫穿其全部事件）。
type assistGen struct {
	correlationID string
	cancel        context.CancelFunc
	done          chan struct{} // teardown 完成後關閉（shutdown reclaim 等待點）
	once          sync.Once
}

// ErrAssistActive：同一 provider 已有 active SpecAssist（獨佔性；第二個請求回此）。
var ErrAssistActive = errors.New("assist already active for provider")

// assistTimeout：單一 one-shot 草擬的上限（timeout 為 once/token 收尾觸發之一）。
const assistTimeout = 3 * time.Minute

// beginAppTxn：shutdown gate 入場（第五輪 review P1 泛化）——涵蓋**所有**可能
// 建立／替換 codex server 或啟動 provider 的操作（StartSession、ensureAppServer、
// B1 probe）。check＋in-flight 登記在同一 shutMu 內原子：TOCTOU 關閉——
// 若 check 通過，shutdown 的 Wait 必等本交易離場，Take 一定在 Ensure 之後執行，
// 任何回填的 server 都會被 Take 收走；shuttingDown 之後的新交易一律被拒。
func (a *App) beginAppTxn() error {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	if a.shuttingDown {
		return errors.New("app shutting down")
	}
	a.inflight.Add(1)
	return nil
}

func (a *App) endAppTxn() { a.inflight.Done() }

// ---- workspace session 建立交易（M3b §3.1）----

// sessionRegistry：wsregistry.Store 的 App 側視角（*wsregistry.Store 直接滿足）。
// 抽成介面只為一個目的——建立交易的補償路徑必須能被測試逐條驅動，而 persist
// 失敗無法在真實 Store 上穩定重現。
type sessionRegistry interface {
	Put(e wsregistry.Entry) error
	DeleteUncommitted(wsid string) error
	Remove(wsid, reason string) error
	Get(wsid string) (wsregistry.Entry, bool)
	Live() []wsregistry.Entry
	Sync() error
}

var _ sessionRegistry = (*wsregistry.Store)(nil)

// errCreateDegraded：該 provider 的建立路徑已進入 degraded latch。刻意沒有
// in-process 解除路徑——見 setCreateDegraded。
var errCreateDegraded = errors.New("app: session create degraded（需重啟 app 復原）")

// errNoSessionRegistry：registry 尚未載入就呼叫 CreateSession。理論上不會發生
// （啟動流程先載入 registry 才開放 UI），但 nil 介面直接呼叫會 panic 在
// ReserveSession 之後、名額已被佔走的位置——fail loud 早退比 panic 洩名額好。
var errNoSessionRegistry = errors.New("app: session registry not loaded")

// createDegraded：該 provider 是否已進入 create-degraded latch。
func (a *App) createDegraded(p contract.Provider) bool {
	a.createDegradedMu.Lock()
	defer a.createDegradedMu.Unlock()
	return a.createDegradedSet[p]
}

// setCreateDegraded：把 provider 標成 create-degraded（單向；只有 app restart
// 能解除）。觸發點只有一個——CommitCreate 失敗且 registry 回滾也失敗：此時
// Manager 側的 slot 是否該存在、registry 磁碟上那筆 entry 是否該留，兩邊都
// 已無法互相證明。AbortCreate 會讓記憶體退回名額但磁碟仍有 entry（重啟即
// 復活成 ghost session）；逕自當成建立成功則 Manager 側沒有可信狀態可據。
// 保留名額 ＋ latch 是唯一能收斂的選擇：重啟後 registry 是權威，那筆 entry
// 會被還原成 dormant，名額自然歸位。
func (a *App) setCreateDegraded(p contract.Provider) {
	a.createDegradedMu.Lock()
	defer a.createDegradedMu.Unlock()
	if a.createDegradedSet == nil {
		a.createDegradedSet = map[contract.Provider]bool{}
	}
	a.createDegradedSet[p] = true
}

// commitCreate：manager.CommitCreate 的可注入包裝（見 hookForceCommitCreateError）。
func (a *App) commitCreate(tok appcore.CreateToken) error {
	if err := a.hookForceCommitCreateError; err != nil {
		return err
	}
	return a.manager.CommitCreate(tok)
}

// CreateSession 建立一個新的 workspace session，回傳其 WSID（純新增 binding；
// 既有 provider-keyed 的 StartSession／SendMessage／EndSession 不受影響）。
//
// 編排順序凍結（§3.1）：beginAppTxn → ReserveSession → wsReg.Put ＋ atomic
// persist → CommitCreate → endAppTxn。registry 先於 CommitCreate 落盤，因為
// 「磁碟有、記憶體無」可以在重啟時由 registry 權威還原成 dormant，反之
// 「記憶體有、磁碟無」重啟即整個 session 消失。
//
// 三條失敗路徑：
//  1. Put 失敗：AbortCreate 退回名額，errors.Join 回報。
//  2. CommitCreate 失敗、DeleteUncommitted 成功：AbortCreate 退回名額。回滾用
//     DeleteUncommitted 而非 Remove——建立失敗不該在 registry 留 tombstone，
//     那是「使用者明確移除」的語意。
//  3. CommitCreate 失敗、DeleteUncommitted 也失敗：**不** AbortCreate、保留
//     名額、該 provider 進 create-degraded latch（見 setCreateDegraded）。
func (a *App) CreateSession(provider, taskLabel string) (string, error) {
	if err := a.beginAppTxn(); err != nil { // shutdown 柵欄
		return "", err
	}
	defer a.endAppTxn()
	// provider 白名單（同 StartSession／SendMessage／EndSession 的既有 guard）：
	// 未知 provider 若放行，會被 Put 寫進 durable registry，重啟後 RestoreDormant
	// 拿到無人能接手的 provider，那筆 entry 永久卡住。
	if !knownProvider(provider) {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	p := contract.Provider(provider)
	if a.createDegraded(p) {
		return "", errCreateDegraded
	}
	if p == contract.ProviderCodex { // §3.4.6：錄流 latch 期間拒絕新 Codex session
		if err := a.codexWireGate(); err != nil {
			return "", err
		}
	}
	if a.wsReg == nil { // 名額尚未被佔用，直接早退
		return "", errNoSessionRegistry
	}
	w, tok, err := a.manager.ReserveSession(p)
	if err != nil {
		return "", err
	}
	if err := a.wsReg.Put(wsregistry.Entry{
		WSID: string(w), Provider: provider, TaskLabel: taskLabel,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return "", errors.Join(err, a.manager.AbortCreate(tok))
	}
	if cerr := a.commitCreate(tok); cerr != nil {
		if rerr := a.wsReg.DeleteUncommitted(string(w)); rerr != nil {
			a.setCreateDegraded(p) // 雙失敗：保留名額、latch，等 app restart（§3.1）
			// WSID 一併帶進錯誤：這筆 reservation 被刻意保留、不 emit、不寫 audit，
			// registry 那筆 entry 也還在磁碟上——錯誤字串是 post-mortem 對帳的唯一線索。
			return "", errors.Join(cerr, rerr,
				fmt.Errorf("app: create degraded, orphan reservation wsid=%s: %w", w, errCreateDegraded))
		}
		return "", errors.Join(cerr, a.manager.AbortCreate(tok))
	}
	a.mu.Lock()
	if a.lastCreatedWSID == nil {
		a.lastCreatedWSID = map[contract.Provider]appcore.WSID{}
	}
	a.lastCreatedWSID[p] = w // legacyWSIDFor 第 2 順位（見其 doc）
	a.mu.Unlock()
	return string(w), nil
}

// legacyWSIDFor：exported provider-keyed binding（StartSession／SendMessage／
// EndSession／NewSession／TerminateSession）在遷移窗口內的 provider → WSID 解析
// 器。這些 binding 的簽名要維持到 Task 26 才與前端原子切換，但內部的 Claude
// ownership 與 lifecycle 已全面 WSID 定址，中間需要這一層。
//
// 解析順序凍結（coordinator 2026-08-15，Task 9 補入第 3 順位）：
//  1. 該 provider 恰有一個 live sessionHost → 回它的 WSID；
//  2. 否則 → 回該 provider 最近一次 CreateSession 的 WSID；
//  3. 否則 → registry 中該 provider 恰有一個 live（非 tombstone）entry，且它已被
//     還原成 Manager 的 committed slot → 回它的 WSID；
//  4. 都沒有 → 回 Manager 的 legacy slot WSID（讀取時隱式建立，其 slot.wsid
//     留空，envelope 輸出與 M3a 完全一致）。
//
// 理由：exported binding 在遷移窗口（Task 8-25）內必須維持「操作使用者當前那個
// session」的既有可觀察行為。窗口內前端仍是單 session／provider，第 1 條恆成立。
// 同一個 session 的整段生命週期必須解析到同一個 WSID，否則 start 交易與收尾會
// 落在不同 slot——這也是所有 exported binding 一律經本函式（而非各自猜）的原因。
//
// 第 3 順位（registry tier）的必要性：Task 5／6 把使用者既有的 session 遷進
// registry 並在啟動時還原成 dormant slot，但使用者實際上仍工作在第 4 順位的
// legacy slot 上——那筆遷移出來的 entry 從此不反映現實，到 Task 26 前端 WSID 化
// 時會多出一筆過時的 session。加這一層，遷移出來的 entry 才真的被接手。
// 「恰一個」是刻意的：多筆時無從判斷使用者指的是哪個，落到第 4 順位反而是安全的
// 已知行為（多 session 本來就要等 Task 26 前端帶 WSID 才有意義）。
//
// 「已被還原成 committed slot」的檢查不可省：registry 是磁碟權威，Manager 才是
// lifecycle 入口能解析的對象。若回一個沒被 RestoreDormant 掛回去的 WSID，之後每個
// 呼叫都會拿到 ErrSessionNotFound——比落到第 4 順位糟。
// Task 26 前端切換後整層刪除。
func (a *App) legacyWSIDFor(p contract.Provider) appcore.WSID {
	if hs := a.hostsOf(p); len(hs) == 1 {
		return hs[0].wsid
	}
	a.mu.Lock()
	w, ok := a.lastCreatedWSID[p]
	a.mu.Unlock()
	if ok {
		return w
	}
	if rw, ok := a.soleRegistryWSID(p); ok {
		return rw
	}
	return a.manager.LegacyWSID(p)
}

// soleRegistryWSID：registry 中該 provider 唯一的 live entry（見 legacyWSIDFor
// 第 3 順位）。唯讀——不建立 slot、不寫 registry。
func (a *App) soleRegistryWSID(p contract.Provider) (appcore.WSID, bool) {
	if a.wsReg == nil {
		return "", false
	}
	var found appcore.WSID
	for _, e := range a.wsReg.Live() {
		if contract.Provider(e.Provider) != p {
			continue
		}
		if found != "" { // 兩筆以上：無從判斷，交給下一順位
			return "", false
		}
		found = appcore.WSID(e.WSID)
	}
	if found == "" {
		return "", false
	}
	if _, err := a.manager.State(found); err != nil { // 未還原成 committed slot
		return "", false
	}
	return found, true
}

// noteWSEmitError：...WS 出口的錯誤處置（Emit／approval 共用）。舊的 provider-keyed
// 入口沒有回傳值，遷到 ...WS 之後多了三種錯誤：ErrClosed 是 shutdown 的正常收尾
// （Manager 自己已發過 closed-drop 通知，不重複記）；其餘（WSID 解析不到、
// provider 不符）是接線 bug，一律 fail loud 進 audit，不靜默吞掉。
func (a *App) noteWSEmitError(op string, w appcore.WSID, err error) {
	if err == nil || errors.Is(err, appcore.ErrClosed) {
		return
	}
	a.audit("ws_emit_error", map[string]any{"op": op, "wsid": string(w), "error": err.Error()})
}

// ---- 啟動：session registry 載入／遷移／dormant 還原（M3b §3.2.2／§3.2.4-6）----

// legacyProviders：legacy 遷移的來源 provider（restore.json 的兩個固定 key，
// 同 restore.go:52 的補齊清單）。
var legacyProviders = []string{"claude", "codex"}

// legacyEntries：把 M3a provider-keyed restore.json 轉成 wsregistry.Migrate 的
// 輸入。轉換是呼叫端的責任（wsregistry 不碰檔案）；資料來源是 App 已持有的
// restoreStore，不重讀檔案——restore.json 的 ownership 只有一份。
//
// 「空 entry 不建立、不佔 slot」（§3.2.5）的判準刻意用 spec 的字面語意——
// resume identity／taskID 皆空**且 view window 內沒有事件**——而不是「
// ViewStartEventID 是否為空」：restore.json 不存在或 malformed 時
// openRestoreStore 會用 audit high-watermark 幫**兩個** provider 都補齊 entry
//（restore.go:42-56），那種 entry 的 ViewStartEventID 非空但 window 內必然
// 零事件。只看欄位非空的話，只用過 claude 的使用者升級後會憑空多出一個
// codex session 並吃掉一個名額。window 判定直接重用 replayViewWindow，因為
// 「view window 裡有沒有事件」的定義就該和實際重放的視窗完全一致。
//
// 與 wsregistry.Migrate 判準的一格分歧（owner 2026-08-15 裁決：接受為已知
// 行為，兩邊都不改）——「window 內有事件、但 ViewStartEventID 為空且無
// resume／taskID」這一格：本函式判定該遷（window 非空），而 Migrate 的欄位
// 檢查（三者皆空即跳過）會把它靜默丟掉，於是該 provider 不獲得 legacy
// session。不改的理由是兩個 filter 各自擋的洞都是真的：這裡的 window 檢查擋
// 「憑空多出一個使用者從沒用過的 codex session 並吃掉名額」；Migrate 的欄位
// 檢查擋「ViewStartEventID 為空時 view window 等於該 provider 的**全部歷史**」，
// 而 §3.2.5 明文禁止把全部歷史丟進 legacy session。放寬任一邊，就會重新打開
// 它原本擋住的、更嚴重的那個洞。這一格的後果限於極窄族群（用過 codex 但從未
// Accept、也沒按過 New Session）：升級後不獲得 legacy session、改為全新開始，
// events.jsonl 稽核歷史完整——影響是 view 連續性，不是資料。
//
// restore store 尚未開啟（a.restore == nil）一律回錯，不當成空資料：遷移
// marker 是單向的，以零 legacy entry 標記 migrated 會讓舊 session 永遠不可能
// 再被遷出。這是載入順序 bug，必須 fail loud。
func (a *App) legacyEntries() (map[string]wsregistry.LegacyEntry, error) {
	if a.restore == nil {
		return nil, errors.New("app: restore store not opened before session registry migration")
	}
	out := make(map[string]wsregistry.LegacyEntry, len(legacyProviders))
	for _, p := range legacyProviders {
		e := a.restore.Get(p)
		if e.ResumeSessionID == "" && e.TaskID == "" &&
			len(replayViewWindow(a.eventsPath(), p, e.ViewStartEventID)) == 0 {
			continue
		}
		out[p] = wsregistry.LegacyEntry{
			ViewStartEventID: e.ViewStartEventID,
			ResumeSessionID:  e.ResumeSessionID,
			TaskID:           e.TaskID,
		}
	}
	return out, nil
}

// noteStartupWarning：把非致命的啟動警告接到既有 startupErr 通道（UI 經
// CLIInfo().startupError 讀得到）。沿用 startup() 既有慣例——只填第一則，
// 不覆寫更早的訊息。
func (a *App) noteStartupWarning(msg string) {
	if a.startupErr == "" {
		a.startupErr = msg
	}
}

// knownProviders：可建立／可還原 session 的 provider 白名單（同 CreateSession
// 的 guard）。與 legacyProviders 語意不同——後者是「legacy 遷移的來源」，只在
// restore.json 轉接時走訪。per-provider 限額檢查一律走這份，未來加第三個
// provider 時只改這裡即可；若讓兩份清單各自硬編，新 provider 的超限檢查會
// 靜默漏掉、然後在 Pass 2 撞 ErrSessionLimit 造成半還原。
var knownProviders = []string{"claude", "codex"}

func knownProvider(p string) bool { return slices.Contains(knownProviders, p) }

// loadSessionRegistry：§3.2.4 啟動修復序列的前半段——載入／遷移 registry →
// 還原 Manager dormant slots。後半段（index 驗證／重建 → incomplete turn 修復
// → 才開放 UI 與 provider 啟動）見 Task 20。
//
// a.wsReg 只在整段序列成功後才接線，這是「Migrate 先於任何 Put」的第一道
// 防線：CreateSession 唯一的 registry 入口是 a.wsReg，nil 即早退
//（errNoSessionRegistry）。若在 Open 之後就接線，遷移失敗的那次啟動仍可
// Put，那筆 entry 會在下次啟動被 MarkMigrated 的整批取代語意無聲蒸發（或撞上
// Migrate 的第二道 guard，讓 app 從此無法完成遷移）。wsregistry.Migrate 內的
// entryCount guard 是最後防線，不是第一道。
//
// 失敗處置一律 fail loud、不自動修復（owner 2026-08-14 裁決）：
//   - registry 檔 malformed：wsregistry.Open 本身就不重建（重建等於抹掉使用者
//     所有 session），這裡只把錯誤升級成可操作指引——檔案完整路徑、「備份後
//     移除該檔即可以空清單啟動」、以及 events.jsonl 稽核歷史不受影響。刻意
//     **不**自動把壞檔改名：若 corruption 其實是暫時性／環境性的讀取錯誤，
//     改名會讓一份沒壞的檔案靜默消失，比不修復更危險。
//   - migration 落盤失敗：回錯、不標記 migrated、不啟動 provider（§3.2.6）。
//
// 還原分兩段（先全量驗證、再全量還原）：Manager 目前沒有移除 committed slot
// 的 API（Task 22 才有），「還原到一半失敗再回滾」根本做不到，所以用驗證取代
// 回滾——驗證不過就一筆都不還原，不留 Manager 與 registry 不一致的半還原狀態。
func (a *App) loadSessionRegistry() ([]wsregistry.Entry, error) {
	path := filepath.Join(a.stateDir, "workspace-sessions.json")
	store, err := wsregistry.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session registry 載入失敗：%w\n"+
			"檔案：%s\n"+
			"此檔是所有 workspace session 的權威記錄，刻意不自動重建或改名——重建等於抹掉全部 session。\n"+
			"請先備份該檔；確認無法修復後移除它，即可以空 session 清單重新啟動。\n"+
			"events.jsonl 的稽核歷史完整未受影響，不會因此遺失", err, path)
	}
	// Migrated() 在 Migrate 內也會再檢查一次（冪等由那裡保證）；這裡先擋一層
	// 只為省掉 legacyEntries 對 events.jsonl 的掃描——正常啟動每次都會走到。
	if !store.Migrated() {
		legacy, lerr := a.legacyEntries()
		if lerr != nil {
			return nil, lerr
		}
		if _, merr := wsregistry.Migrate(store, legacy,
			func() string { return contract.NewULID(time.Now()) }); merr != nil {
			return nil, merr
		}
	}

	// Pass 1：只分類與驗證，不動任何 Manager 狀態。
	//
	// 三類壞資料一律「跳過該筆、不刪除、不阻擋啟動」（決策 2 的精神）——
	// workspace-sessions.json 是使用者可手動編輯的檔案，單筆無法還原的 entry
	// 不該讓整個 app 開不起來，跳過也是非破壞性的（entry 仍在磁碟，修好或該
	// provider 回歸即可還原）。三類都必須在這裡擋掉而不是留給 Pass 2，否則
	// 「前面幾筆已還原、中途才失敗」就會留下 Manager 有 slot 而 wsReg 為 nil
	// 的半還原狀態，正是兩段式要消滅的東西。
	live := store.Live()
	restorable := make([]wsregistry.Entry, 0, len(live))
	perProvider := map[string]int{}
	seen := map[string]bool{}
	var unknownProv, invalid []string
	for _, e := range live {
		switch {
		// WSID 欄位為空：JSON map key 與 Entry.WSID 是兩回事，key 有值而欄位
		// 空是合法 JSON，但 RestoreDormant 對空 WSID 回 ErrSessionNotFound。
		case e.WSID == "":
			invalid = append(invalid, fmt.Sprintf("(empty wsid, provider=%q)", e.Provider))
		// 兩個 map key 帶同一個 WSID：provider 不同時 RestoreDormant 回
		// ErrProviderMismatch；相同時兩筆會靜默共用一個 slot。都不放行。
		case seen[e.WSID]:
			invalid = append(invalid, fmt.Sprintf("%s(duplicate wsid)", e.WSID))
		case !knownProvider(e.Provider):
			unknownProv = append(unknownProv, fmt.Sprintf("%s(provider=%q)", e.WSID, e.Provider))
		default:
			seen[e.WSID] = true
			perProvider[e.Provider]++
			restorable = append(restorable, e)
		}
	}
	// 診斷軌跡先發：之後就算限額驗證失敗直接回錯，audit 也留得下來。
	if len(unknownProv) > 0 {
		a.audit("session_registry_unknown_provider",
			map[string]any{"count": len(unknownProv), "skipped": unknownProv, "path": path})
	}
	if len(invalid) > 0 {
		a.audit("session_registry_invalid_entry",
			map[string]any{"count": len(invalid), "skipped": invalid, "path": path})
	}
	for _, p := range knownProviders { // 固定順序：錯誤訊息不得隨 map 迭代漂移
		if n := perProvider[p]; n > appcore.MaxSessionsPerProvider {
			return nil, fmt.Errorf("session registry 有 %d 筆 live %s session，上限為 %d："+
				"一筆都不還原（避免 Manager 與 registry 半還原不一致）。\n"+
				"檔案：%s——請備份後手動移除多餘 entry", n, p, appcore.MaxSessionsPerProvider, path)
		}
	}

	// Pass 2：驗證通過才真的還原。
	for _, e := range restorable {
		if rerr := a.manager.RestoreDormant(appcore.WSID(e.WSID), contract.Provider(e.Provider)); rerr != nil {
			return nil, fmt.Errorf("app: restore dormant wsid=%s provider=%s: %w", e.WSID, e.Provider, rerr)
		}
	}
	// 跳過筆數的警告刻意留到最後才寫：noteStartupWarning 只填第一則，若在
	// Pass 1 就寫入，之後任何致命失敗（限額、Pass 2）的訊息都會被丟棄，UI 只
	// 看得到一句聽起來沒事的「跳過 N 筆」，實際上整個 registry 載入失敗、
	// CreateSession 全掛。audit 不受此限（在前面就發），診斷軌跡的價值正是
	// 在失敗時。
	if n := len(unknownProv) + len(invalid); n > 0 {
		a.noteStartupWarning(fmt.Sprintf(
			"session registry: 跳過 %d 筆無法還原的 entry（未刪除，仍在 %s）：%s",
			n, path, strings.Join(slices.Concat(unknownProv, invalid), ", ")))
	}
	a.wsReg = store
	return restorable, nil
}

// emit：UI 事件唯一出口（wails EventsEmit 的可注入包裝）。
func (a *App) emit(name string, data any) {
	if a.emitUI != nil {
		a.emitUI(name, data)
		return
	}
	runtime.EventsEmit(a.ctx, name, data)
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{apprPending: map[string]*pendingApproval{},
		assistActive:   map[string]*assistGen{},
		preflightCache: map[string]assist.PreflightResult{},
		evidenceActive: map[string]context.CancelFunc{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	var wsErr error
	a.workspaceDir, a.stateDir, a.workspaceSrc, wsErr = resolveWorkspace()
	if wsErr != nil { // fail loud：UI 與 audit 都要看得到
		a.startupErr = "workspace init failed: " + wsErr.Error()
	}
	if r, rerr := claude.OpenRegistry(filepath.Join(a.stateDir, "sessions.json")); rerr == nil {
		a.registry = r
	} else if a.startupErr == "" {
		a.startupErr = "registry init failed: " + rerr.Error()
	}
	if f, ferr := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
		a.auditF = f
	}
	sink, serr := appcore.NewJSONLSink(filepath.Join(a.stateDir, "events.jsonl"))
	if serr != nil {
		if a.startupErr == "" {
			a.startupErr = "event sink init failed: " + serr.Error()
		}
		sink = nil
	}
	var auditSink appcore.AuditSink = sink
	if sink == nil { // manager 必須存在；sink 失敗已 fail loud 於 startupErr
		auditSink = failedSink{reason: serr}
	}
	a.manager = appcore.New(appcore.Config{
		Sink: auditSink,
		Emit: func(env contract.Envelope) { a.emit("workbench:event", env) },
		// Task 4 live probe VERDICT=per-turn（turn2 output 9 << turn1 642）→ 累加制
		ClaudeUsageCumulative: false,
	})
	rs, rserr := openRestoreStore(filepath.Join(a.stateDir, "restore.json"), auditHighWatermark(a.eventsPath()))
	a.restore = rs
	if rserr != nil { // malformed 重建等一律 fail loud（不無聲）
		a.audit("restore_store_warning", map[string]any{"error": rserr.Error()})
	}
	// M3b §3.2.4 前半段：載入／遷移 workspace-sessions.json → 還原 dormant slots。
	// 必須在 restore store 開啟之後（legacy 遷移的來源）與 manager 建立之後。
	// 失敗一律 fail loud：a.wsReg 維持 nil，CreateSession 早退，不以猜測的狀態
	// 繼續（§3.2.6）。
	if _, lerr := a.loadSessionRegistry(); lerr != nil {
		a.audit("session_registry_error", map[string]any{"error": lerr.Error()})
		a.noteStartupWarning("session registry load failed: " + lerr.Error())
	}
	a.toolsDirPath, a.toolsSource = resolveToolsDir(a.workspaceDir)
	a.nodePath = resolveNodePath()
	a.audit("startup", map[string]any{"workspace": a.workspaceDir, "workspace_source": a.workspaceSrc,
		"startup_error": a.startupErr, "node_path": a.nodePath,
		"tools_dir": a.toolsDirPath, "tools_source": a.toolsSource, "node": a.nodeVersion()})
	a.diagramPath = filepath.Join(a.workspaceDir, "docs", "sample.mmd")
	a.watchDiagram(a.diagramPath)
	a.watchSpecTree() // spec §4 通知層：spec/ 遞迴監看，變更 debounce 後觸發 Reconcile()
	a.watchPlanTree() // Task 12：plan/ 遞迴監看，鏡射 watchSpecTree（同一 Reconcile()，涵蓋 gate1／gate2）
	a.startupEvidence()
	// Task 24（§3.8 啟動補建）：依權威狀態掃描——已 stale 的核可、degraded
	// journal 若無對應未 resolved escalation 項即補建、journal 重開成功即系統
	// 解除 journal-degraded 項。只在 gate journal 已存在（workspace 曾用過
	// gate）時觸發：全新／非 git workspace 不強迫 ensureGate 的 git 依賴在
	// startup 就 fail loud。
	if _, statErr := os.Stat(filepath.Join(a.workspaceDir, ".workbench", "gate.jsonl")); statErr == nil {
		a.reconcileGate1NotifyOnly()
	}
}

// startupEvidence（Task 20）：惰性 gate/plan 之外少數在 startup 就建立的狀態——
// evidence journal／CAS／worktree registry 路徑都落在 .workbench/evidence/
// 下，且 CleanupOrphans／CleanOrphanTemps 必須在任何 RunEvidence 呼叫之前跑
// 過一次，才能收乾淨上次程序異常結束留下的 worktree／temp 殘留（brief 凍結：
// 下次啟動兜底逾時 forcedShutdown 未清乾淨的窗口）。liveIDs 傳空 map：啟動當下
// 不可能有任何 in-flight run，registry 裡的每個 active 項目都是孤兒。這幾步
// 全是檔案操作＋（僅在真有孤兒殘留時）git worktree 指令；一個尚未 git init 的
// 全新 workspace（registry 檔不存在或是空檔）不會觸發任何 git 呼叫，因此可以
// 安全地在 ensureGate() 惰性初始化之前無條件執行。
func (a *App) startupEvidence() {
	dir := filepath.Join(a.workspaceDir, ".workbench", "evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if a.startupErr == "" {
			a.startupErr = "evidence dir init failed: " + err.Error()
		}
		return
	}
	a.evidenceCASDir = filepath.Join(dir, "cas")
	a.evidenceRegistryPath = filepath.Join(dir, "worktrees.jsonl")
	if j, jerr := evidence.OpenJournal(filepath.Join(dir, "evidence.jsonl")); jerr == nil {
		a.evidenceJournal = j
	} else if a.startupErr == "" {
		a.startupErr = "evidence journal init failed: " + jerr.Error()
	}
	if oerr := evidence.CleanupOrphans(a.workspaceDir, a.evidenceRegistryPath, map[string]bool{}); oerr != nil && a.startupErr == "" {
		a.startupErr = "evidence orphan worktree cleanup failed: " + oerr.Error()
	}
	if _, terr := evidence.CleanOrphanTemps(a.evidenceCASDir); terr != nil && a.startupErr == "" {
		a.startupErr = "evidence orphan temp cleanup failed: " + terr.Error()
	}
}

// failedSink：events.jsonl 開檔失敗時的替身——每次寫入回同一錯誤，
// Manager 會 latch 並以 stream_error fail loud（不無聲丟稽核）。
type failedSink struct{ reason error }

func (s failedSink) Write(contract.Envelope) error { return s.reason }
func (s failedSink) Close() error                  { return nil }

// ReadDiagram 回傳目前圖檔內容（Mermaid pane 初始載入）。
func (a *App) ReadDiagram() (string, error) {
	b, err := os.ReadFile(a.diagramPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (a *App) watchDiagram(path string) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	_ = w.Add(filepath.Dir(path))
	go func() {
		for ev := range w.Events {
			if ev.Name == path && ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if b, err := os.ReadFile(path); err == nil {
					a.emit("diagram:changed", string(b))
				}
			}
		}
	}()
}

// specWatchDebounce：連續 fsnotify 事件合併視窗——macOS 對 atomic save 常見
// 短時間內成對送出 Create／Rename，避免每個事件各自觸發一次 reconcile。
const specWatchDebounce = 200 * time.Millisecond

// watchSpecTree 對納管的 spec/ 樹（spec.ScopePatterns 範圍）遞迴監看，變更
// debounce 後觸發 Reconcile()（spec §4：通知層）。只重用 watchDiagram 的
// 概念，修正其三個缺陷：(1) 這裡遞迴 Add 整棵子樹、新目錄 Create 時再 Add，
// 不只監看單一 parent；(2) watcher／Add／讀取錯誤一律 fail-loud（audit＋
// EmitWorkspace stream_error），不吞聲；(3) 透過 specWatchStop／specWatchDone
// 掛進 shutdown，goroutine 保證退出、watcher.Close() 保證被呼叫。
//
// 這是 NOTIFICATION 層，不是權威：觸發只呼叫 Reconcile()（best-effort），
// 目的是讓已核可的 Gate 1 儘快在 UI 顯示 STALE；權威重算永遠在讀取路徑
// （gate.Service.List／GateList，Task 10），watcher 失敗不影響正確性。
//
// fix round 2（acceptance smoke 發現的落差）：spec/ 在 watchSpecTree() 啟動
// 當下可能還不存在（例如全新 workspace，尚未有人寫過第一個納管檔）——一律
// Add workspace root，才能觀察到 <root>/spec 這個 CREATE；spec/ 若已存在則
// 額外遞迴 Add 進去（同今行為）。root 底下同時有 .workbench/（gate journal／
// events 持續寫入）與 .git/，事件必須在 runSpecWatch 過濾到 spec/ 子樹以內，
// 否則 reconcile 寫 journal → 觸發自己的 watch 事件 → 無窮迴圈。
func (a *App) watchSpecTree() {
	a.stopSpecWatch() // 冪等：若先前已在監看（重複呼叫），先收掉舊的、不留 orphan goroutine

	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		a.failLoudSpecWatch("normalize workspace: " + err.Error())
		return
	}
	specRoot := filepath.Join(root, "spec")

	w, err := fsnotify.NewWatcher()
	if err != nil {
		a.failLoudSpecWatch("fsnotify.NewWatcher: " + err.Error())
		return
	}
	if aerr := w.Add(root); aerr != nil {
		a.failLoudSpecWatch("watch workspace root: " + aerr.Error())
		// 繼續：root 沒加成功仍嘗試把已存在的 spec/ 加進去，能看多少算多少。
	}
	if _, statErr := os.Stat(specRoot); statErr == nil {
		if aerr := addRecursiveWatch(w, specRoot); aerr != nil {
			a.failLoudSpecWatch("watch spec/: " + aerr.Error())
			// 已成功 Add 的目錄仍持續監看；不因單一子目錄失敗放棄整棵樹。
		}
	} else if !os.IsNotExist(statErr) {
		a.failLoudSpecWatch("stat spec/: " + statErr.Error())
	}
	// spec/ 尚不存在也不 return：root 已在監看範圍內，之後 spec/ 被建立會是
	// root 上的一個 CREATE 事件，runSpecWatch 收到後會遞迴 Add 進去。

	stop := make(chan struct{})
	done := make(chan struct{})
	a.mu.Lock()
	a.specWatchStop = stop
	a.specWatchDone = done
	a.mu.Unlock()

	go a.runSpecWatch(w, specRoot, stop, done)
}

// addRecursiveWatch 對 dir 底下每個目錄（含自身）呼叫 watcher.Add；WalkDir
// 中途錯誤（例如 race 中被刪）記錄第一個但不中止整趟 walk，盡量多加。
func addRecursiveWatch(w *fsnotify.Watcher, dir string) error {
	var firstErr error
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil // 略過這個節點，繼續 walk 其餘部分
		}
		if !d.IsDir() {
			return nil
		}
		if aerr := w.Add(path); aerr != nil && firstErr == nil {
			firstErr = aerr
		}
		return nil
	})
	return firstErr
}

// specEventInScope：事件路徑是否落在 spec/ 子樹內（含 spec/ 自身）——watcher
// 必須 Add workspace root 才能觀察到 spec/ 被晚建立，但 root 底下同時有
// .workbench/（gate journal／events 持續寫入）與 .git/：reconcile 觸發的
// journal append 若被自己的 watch 事件再次撿到，會形成無窮迴圈。所有
// reconcile／emit 都必須先經這個過濾。
func specEventInScope(specRoot, name string) bool {
	return name == specRoot || strings.HasPrefix(name, specRoot+string(filepath.Separator))
}

// runSpecWatch：watcher 事件迴圈——只處理落在 spec/ 子樹內的事件
// （specEventInScope；root 上其餘事件如 .workbench/、.git/ 一律忽略，避免
// reconcile 寫 journal 又觸發自己的 watch 造成無窮迴圈）。debounce 合併連續
// 事件後觸發 Reconcile()；spec/ 子樹內 Create 為目錄時 re-add（遞迴涵蓋
// 新子樹，也涵蓋 spec/ 自身被晚建立的情況）；Rename／Remove 的路徑可能已
// 消失，不視為 fatal，一樣 debounce 後 reconcile（消失或變更皆由重算反映）；
// error channel 的錯誤 fail-loud 但不中止迴圈；stop 關閉時退出，defer 保證
// watcher.Close() 恰好呼叫一次、done 保證 shutdown 等得到。
func (a *App) runSpecWatch(w *fsnotify.Watcher, specRoot string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer w.Close()
	var debounceC <-chan time.Time
	var lastChanged string
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !specEventInScope(specRoot, ev.Name) {
				continue // root 上的非 spec/ 事件（.workbench/、.git/ 等）：忽略，避免自我觸發迴圈
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() {
					if aerr := addRecursiveWatch(w, ev.Name); aerr != nil {
						a.failLoudSpecWatch("watch new dir: " + aerr.Error())
					}
				}
			}
			lastChanged = ev.Name
			debounceC = time.After(specWatchDebounce)
		case werr, ok := <-w.Errors:
			if !ok {
				return
			}
			a.failLoudSpecWatch("fsnotify: " + werr.Error())
		case <-debounceC:
			debounceC = nil
			a.reconcileGate1NotifyOnly()
			// Task 16 fix round 1（spec §5.2）：spec/ 樹（含 context-map/*.mmd）變更時
			// 額外送一個輕量 UI 訊號，讓 DiagramPane 等監看層自行重讀重渲染——與上面
			// 的 reconcile 共用同一個 debounce 視窗，不影響 reconcile 邏輯或 fail-loud
			// 錯誤處理。payload 只是「目前所知的最後變更路徑」，非權威；接收端一律
			// 重讀自己目前開啟的檔案，不依賴 payload 內容。
			a.emit("spec:changed", lastChanged)
		}
	}
}

// reconcileGate1NotifyOnly：watcher 觸發的 best-effort reconcile——失敗只
// fail-loud UI，不影響權威（GateList 讀取路徑永遠自己重算一次）。名稱沿用
// M2 舊名（gate1 尚是唯一 gate 時所取），但 svc.Reconcile() 本身早已泛化為
// 對 Registry 內所有已註冊 gate（gate1／gate2／tca）重算 stale——
// watchSpecTree／watchPlanTree（Task 12）共用同一個呼叫點，任一棵樹變更都會
// 讓所有 gate 的綁定一併被重新檢查。
//
// Task 24：watcher 觸發的 Reconcile 也屬 workflowMu 序列化的編排路徑——先取
// workflowMu 再走 reconcileLocked（§3.8 stale／journal-degraded 補建接在同一
// 呼叫點；重入規約見 workflowMu 欄位 doc）。
func (a *App) reconcileGate1NotifyOnly() {
	svc, err := a.ensureGate()
	if err != nil {
		a.failLoudSpecWatch("ensureGate: " + err.Error())
		return
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if err := a.reconcileLocked(svc); err != nil {
		a.failLoudSpecWatch("Reconcile: " + err.Error())
	}
}

// failLoudSpecWatch：spec watcher 錯誤的唯一出口——audit 落一筆稽核 ＋
// workspace-scope stream_error（EmitWorkspace，同 gateEmitter 的出口）。只影響
// NOTIFICATION，權威重算（GateList）不受任何 watcher 錯誤影響。
func (a *App) failLoudSpecWatch(msg string) {
	a.audit("spec_watch_error", map[string]any{"error": msg})
	if a.manager != nil {
		a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{"error": msg})
	}
}

// stopSpecWatch：shutdown／重新啟動 watch 前的收尾——關閉 stop 訊號、等
// goroutine 真正退出（其 defer 已呼叫 watcher.Close()），保證不留背景
// goroutine、watcher 一定被關閉。未啟動過時是 no-op。
func (a *App) stopSpecWatch() {
	a.mu.Lock()
	stop := a.specWatchStop
	done := a.specWatchDone
	a.specWatchStop = nil
	a.specWatchDone = nil
	a.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if done != nil {
		<-done
	}
}

// ---- plan/ watcher（Task 12：watchSpecTree 模式鏡射；spec §7 Stage B 通知層）----
//
// 結構逐字對照 watchSpecTree／runSpecWatch／specEventInScope／stopSpecWatch／
// failLoudSpecWatch（見上方各自 doc comment，此處不重複展開設計理由）；差異僅：
// 監看根換成 <root>/plan、事件過濾用 planEventInScope、UI 訊號為 "plan:changed"、
// audit kind 為 "plan_watch_error"、stop／done 換 a.planWatchStop／a.planWatchDone。
// reconcile 呼叫點與 watchSpecTree 共用同一個 a.reconcileGate1NotifyOnly()——
// svc.Reconcile() 本身對 gate1／gate2 一視同仁，不需要兩份 reconcile 邏輯。

func (a *App) watchPlanTree() {
	a.stopPlanWatch()

	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		a.failLoudPlanWatch("normalize workspace: " + err.Error())
		return
	}
	planRoot := filepath.Join(root, "plan")

	w, err := fsnotify.NewWatcher()
	if err != nil {
		a.failLoudPlanWatch("fsnotify.NewWatcher: " + err.Error())
		return
	}
	if aerr := w.Add(root); aerr != nil {
		a.failLoudPlanWatch("watch workspace root: " + aerr.Error())
	}
	if _, statErr := os.Stat(planRoot); statErr == nil {
		if aerr := addRecursiveWatch(w, planRoot); aerr != nil {
			a.failLoudPlanWatch("watch plan/: " + aerr.Error())
		}
	} else if !os.IsNotExist(statErr) {
		a.failLoudPlanWatch("stat plan/: " + statErr.Error())
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	a.mu.Lock()
	a.planWatchStop = stop
	a.planWatchDone = done
	a.mu.Unlock()

	go a.runPlanWatch(w, planRoot, stop, done)
}

// planEventInScope mirrors specEventInScope — see its doc comment for why
// the workspace root must stay in scope for the initial CREATE of plan/, and
// why .workbench/／.git/ events on root must be filtered out here (self-
// triggering reconcile → journal append → watch event loop).
func planEventInScope(planRoot, name string) bool {
	return name == planRoot || strings.HasPrefix(name, planRoot+string(filepath.Separator))
}

func (a *App) runPlanWatch(w *fsnotify.Watcher, planRoot string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer w.Close()
	var debounceC <-chan time.Time
	var lastChanged string
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !planEventInScope(planRoot, ev.Name) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() {
					if aerr := addRecursiveWatch(w, ev.Name); aerr != nil {
						a.failLoudPlanWatch("watch new dir: " + aerr.Error())
					}
				}
			}
			lastChanged = ev.Name
			debounceC = time.After(specWatchDebounce)
		case werr, ok := <-w.Errors:
			if !ok {
				return
			}
			a.failLoudPlanWatch("fsnotify: " + werr.Error())
		case <-debounceC:
			debounceC = nil
			a.reconcileGate1NotifyOnly()
			a.emit("plan:changed", lastChanged)
		}
	}
}

// failLoudPlanWatch：plan watcher 錯誤的唯一出口，鏡射 failLoudSpecWatch
// （見其 doc comment），audit kind 改 "plan_watch_error" 以便區分兩棵樹。
func (a *App) failLoudPlanWatch(msg string) {
	a.audit("plan_watch_error", map[string]any{"error": msg})
	if a.manager != nil {
		a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{"error": msg})
	}
}

// stopPlanWatch mirrors stopSpecWatch — see its doc comment.
func (a *App) stopPlanWatch() {
	a.mu.Lock()
	stop := a.planWatchStop
	done := a.planWatchDone
	a.planWatchStop = nil
	a.planWatchDone = nil
	a.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if done != nil {
		<-done
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.shutMu.Lock()
	a.shuttingDown = true // 1) 拒新 StartSession／ensureAppServer／SpecAssist／EndSession／NewSession（review P1）
	a.shutMu.Unlock()
	a.stopSpecWatch()                          // 1a) 停 spec/ watcher：先收斂，避免與後續 manager.Close() 競態
	a.stopPlanWatch()                          // 1a′) 停 plan/ watcher，同上理由
	a.reclaimAssists()                         // 1b) cancel 每個 in-flight SpecAssist（bounded：runner 界限內退出）
	a.reclaimEvidenceRuns()                    // 1b′) cancel 每個 in-flight RunEvidence（task-20：同上理由，必須早於 inflight.Wait）
	a.inflight.Wait()                          // 2) 等已取得 ownership 的交易（含 assist teardown 的 endAppTxn）完成
	if err := a.forcedShutdown(); err != nil { // 3) 並行 forced path（M1.5 plan D4）
		a.audit("shutdown_forced_error", map[string]any{"error": err.Error()})
	}
	if a.manager != nil {
		_ = a.manager.Close() // 全部 finalize 之後才關 sink（pending queue abort+flush 兜底）
	}
	if o, ok := a.codexSingle.Take(); ok { // 取出即清空 ownership，無後續回填
		// §3.4.2 的收尾總序（terminate → wait → stdout 汲取完成 → detach →
		// finalize wire log）全在 FinalizeWith 內，與死亡 reaper／受控
		// replacement 共用同一份實作；冪等，watcher 已收過就直接回原結果。
		_ = o.FinalizeWith(nil)
	}
	// 殘留 host 的 broker：teardown 成功的 host 已把自己從 registry 取出並關掉
	// broker，留在這裡的是 teardown 失敗／從未進入收尾的那些。**這是 best effort、
	// 不是完整收尾**——只關 broker（釋放 socket），不 Terminate sess、不 finalize
	// lease：走到這一步代表 forcedShutdown 的 CloseSequence 已經失敗過一次，同樣
	// 的操作在此重試沒有新的成功理由，而 app 即將退出、OS 會回收子行程。錄流
	// meta 缺失是已知後果（CloseSequence 失敗時 finalize 本來就沒跑成）。行為與
	// 遷移前的單例版同構。
	for _, h := range a.snapshotHosts() {
		if h.broker != nil {
			_ = h.broker.Close()
		}
	}
}

// forcedShutdown：shutdown 專用並行收尾（正常 EndSessionFlow 會被 busy／pending
// submit 擋住，無法保證 E8）。每個 active provider：先 interrupt／terminate active
// turn → 走收尾；EndSessionFlow 被 lifecycle 狀態拒絕時直接 teardown 兜底（lease
// 冪等）。兩邊都被等待、錯誤 errors.Join 保留、一邊失敗不跳過另一邊。
//
// claude 側 ErrEndInProgress 裁定（review P2，TestShutdownForcedWaitsForBoth
// flaky 的根因）：sess.Terminate() 會讓 process 死掉，喚醒 startClaude 內建的
// 自然收尾 reaper（見其 doc）——它與這裡幾乎同時嘗試 BeginEndSession，輸家收到
// ErrEndInProgress。這個 session 本就在收尾中，teardown 的目的（結束 session、
// finalize 錄流）正被贏家那條路徑達成，不是 forced shutdown 真的失敗，故裁定
// 為 benign、不計入 shutdown 錯誤——但仍要確認 teardown 真的收斂才能返回（見
// switch 內對應分支）。host.teardownFn 是 startClaude 建的 sync.OnceValue：
// 兩條路徑共用同一份，任一方呼叫它都只會執行一次真正的 CloseSequence，另一方
// 只是阻塞等它做完（收斂上限即 CloseSequence 自身 quiesce/kill timeout），不會
// double-Close／double-Terminate／double-Finalize。
//
// M3b §3.3：claude 側改為逐 sessionHost 並行收尾——每個 host 有自己的子行程、
// broker、lease 與 WSID slot，彼此獨立；一個收尾失敗不跳過其他，錯誤全部
// errors.Join 保留。
func (a *App) forcedShutdown() error {
	claudeHosts := a.hostsOf(contract.ProviderClaude)
	codexHosts := a.hostsOf(contract.ProviderCodex)

	var wg sync.WaitGroup
	errs := make([]error, len(claudeHosts)+len(codexHosts))
	for i, ch := range claudeHosts {
		if ch.sess == nil || ch.teardownFn == nil { // 未完成 publish 的 host 不可能存在（見 sessionHost doc）
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ch.sess.Terminate()                                  // interrupt 先行：加速 CloseSequence quiesce
			if h := a.hookForcedShutdownClaudeBeforeFlow; h != nil { // 測試 barrier：見 App 欄位 doc
				h()
			}
			err := appcore.EndSessionFlow(a.manager, ch.wsid, nil, ch.teardownFn)
			switch {
			case err == nil:
				// forced shutdown 自己贏得 BeginEndSession、teardown 已完成。
			case errors.Is(err, appcore.ErrEndInProgress):
				if h := a.hookForcedShutdownClaudeBenign; h != nil { // 測試 barrier：見上方 doc
					h()
				}
				if terr := ch.teardownFn(); terr != nil { // 只等收斂；ErrEndInProgress 本身不計入錯誤
					errs[i] = terr
				}
			default:
				terr := ch.teardownFn() // 其餘 lifecycle 擋住：直接收尾兜底（OnceValue 保證冪等）
				errs[i] = errors.Join(err, terr)
			}
		}()
	}
	// codex 側同構（Task 9）：所有 session 共用同一條 conn 與長駐 server，因此
	// 這裡不 Terminate server（shutdown() 在全部 finalize 之後才 Take＋Terminate），
	// 只逐 session interrupt 自己的 turn 再走同一套收尾。一個失敗不跳過其他。
	for i, ch := range codexHosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ch.runner != nil && ch.runner.ActiveTurnID() != "" { // interrupt active turn（best effort）
				a.mu.Lock()
				conn := a.codexConn
				a.mu.Unlock()
				if params, perr := ch.track.InterruptParams(); perr == nil && conn != nil {
					ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_, _ = conn.Call(ictx, codex.MethodTurnInterrupt, params)
					cancel()
				}
			}
			if err := appcore.EndSessionFlow(a.manager, ch.wsid, nil, func() error {
				return a.codexTeardown(ch)
			}); err != nil {
				terr := a.codexTeardown(ch) // lifecycle 擋住：直接收（lease.Finalize 冪等）
				errs[len(claudeHosts)+i] = errors.Join(err, terr)
			}
		}()
	}
	wg.Wait() // 每一邊都必須被等待
	return errors.Join(errs...)
}

// ---- helpers ----

// resolveWorkspace：env WORKBENCH_WORKSPACE → 可寫的 cwd（Finder 啟動時 cwd 是 "/"，
// 不可寫）→ home。第一個能建出 .workbench/recordings 的候選勝出。
func resolveWorkspace() (workspace, state, source string, err error) {
	type cand struct{ dir, src string }
	var cands []cand
	if d := os.Getenv("WORKBENCH_WORKSPACE"); d != "" {
		cands = append(cands, cand{d, "env"})
	}
	if cwd, cerr := os.Getwd(); cerr == nil && cwd != "/" {
		cands = append(cands, cand{cwd, "cwd"})
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		cands = append(cands, cand{home, "home"})
	}
	var lastErr error
	for _, c := range cands {
		n, nerr := claude.NormalizeCWD(c.dir)
		if nerr != nil {
			lastErr = nerr
			continue
		}
		st := filepath.Join(n, ".workbench")
		if merr := os.MkdirAll(filepath.Join(st, "recordings"), 0o755); merr != nil {
			lastErr = merr
			continue
		}
		_ = os.MkdirAll(filepath.Join(st, "probe"), 0o755) // A2/A3 探針落點
		return n, st, c.src, nil
	}
	tmp := os.TempDir()
	st := filepath.Join(tmp, "sdlc-workbench", ".workbench")
	if merr := os.MkdirAll(filepath.Join(st, "recordings"), 0o755); merr != nil {
		return tmp, st, "tmp", errors.Join(lastErr, merr)
	}
	return tmp, st, "tmp", lastErr
}

// resolveToolsDir：env WORKBENCH_TOOLS_DIR → bundle Resources/tools → repo tools/（dev fallback）。
func resolveToolsDir(workspace string) (string, string) {
	if d := os.Getenv("WORKBENCH_TOOLS_DIR"); d != "" {
		return d, "env"
	}
	if exe, err := os.Executable(); err == nil {
		bundle := filepath.Join(filepath.Dir(exe), "..", "Resources", "tools")
		if st, err := os.Stat(bundle); err == nil && st.IsDir() {
			return bundle, "bundle"
		}
	}
	return filepath.Join(workspace, "tools"), "dev-repo"
}

func (a *App) claudeCLIPath() string {
	return filepath.Join(a.toolsDirPath, "claude-cli", "node_modules", ".bin", "claude")
}

func (a *App) codexCLIPath() string {
	return filepath.Join(a.toolsDirPath, "codex-cli", "node_modules", ".bin", "codex")
}

// resolveNodePath：GUI app（Finder 啟動）不繼承 shell PATH，node 常在
// /usr/local/bin 或 /opt/homebrew/bin。codex CLI 是 node script（claude 為
// native binary），找不到 node 時 Codex 線必掛。
func resolveNodePath() string {
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	for _, c := range []string{"/usr/local/bin/node", "/opt/homebrew/bin/node"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// childEnv：把 node 所在目錄前置到子程序 PATH（duplicate PATH 以後者為準）。
func (a *App) childEnv() []string {
	if a.nodePath == "" {
		return nil
	}
	return []string{"PATH=" + filepath.Dir(a.nodePath) + ":" + os.Getenv("PATH")}
}

func (a *App) nodeVersion() string {
	if a.nodePath == "" {
		return "missing (not on app PATH; codex CLI needs node)"
	}
	out, err := exec.Command(a.nodePath, "--version").Output()
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

func (a *App) cliVersion(provider string) string {
	bin := a.claudeCLIPath()
	if provider == "codex" {
		bin = a.codexCLIPath()
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), a.childEnv()...) // codex CLI 是 node script
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (a *App) audit(kind string, v any) {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	if a.auditF == nil {
		return
	}
	rec, _ := json.Marshal(map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind, "data": v})
	fmt.Fprintf(a.auditF, "%s\n", rec)
}

type auditWriter struct{ a *App }

func (w auditWriter) Write(p []byte) (int, error) {
	w.a.auditMu.Lock()
	defer w.a.auditMu.Unlock()
	if w.a.auditF == nil {
		return len(p), nil
	}
	return w.a.auditF.Write(p)
}

func (a *App) auditWriterFor() auditWriter { return auditWriter{a} }

func clientInfo() codex.ClientInfo {
	return codex.ClientInfo{Name: "sdlc-workbench", Version: "0.0.1"}
}

// CLIInfo 回報 CLI 解析路徑與版本（隔離 smoke 的證據面）。
func (a *App) CLIInfo() map[string]string {
	return map[string]string{
		"toolsDir": a.toolsDirPath, "toolsSource": a.toolsSource,
		"claudeVersion": a.cliVersion("claude"), "codexVersion": a.cliVersion("codex"),
		"node": a.nodeVersion(), "workspace": a.workspaceDir,
		"workspaceSource": a.workspaceSrc, "startupError": a.startupErr,
	}
}

// ---- workspace 檔案（canonical 邊界）----

type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // workspace 相對路徑
	IsDir bool   `json:"isDir"`
}

var listExcluded = map[string]bool{".git": true, ".workbench": true, "node_modules": true, "build": true}

// resolveInWorkspace：rel → canonical 絕對路徑；EvalSymlinks 後必須仍在
// workspace root 內（symlink 指外一律擋）。
func (a *App) resolveInWorkspace(rel string) (string, error) {
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		// Clean 會把 /.. 中和成 root——顯式拒絕，不無聲重導
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return resolved, nil
}

func (a *App) ListWorkspace(rel string) ([]FileNode, error) {
	dir, err := a.resolveInWorkspace(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []FileNode
	for _, e := range entries {
		name := e.Name()
		if listExcluded[name] || strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, FileNode{Name: name,
			Path: filepath.Join(filepath.Clean("/"+rel), name)[1:], IsDir: e.IsDir()})
	}
	return out, nil
}

func (a *App) ReadWorkspaceFile(rel string) (string, error) {
	p, err := a.resolveInWorkspace(rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", rel)
	}
	if st.Size() > 1<<20 {
		return "", fmt.Errorf("%q too large (%d bytes > 1MB)", rel, st.Size())
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1)) // Stat 之外的雙保險
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", fmt.Errorf("%q too large", rel)
	}
	return string(b), nil
}

// ---- 納管 spec 檔（spec/features/**、spec/nfr/**、spec/glossary.md、spec/context-map/**）----

// specDigestPrefix：SpecRead／SpecWrite 共用的 digest 格式，對齊
// internal/spec.ManifestDigest 與 internal/gate 既有的 "sha256:<64hex>" 慣例
// （見 internal/gate/project.go reSHA256）——不是自創格式。
const specDigestPrefix = "sha256:"

func specDigestOf(raw []byte) string { return specDigestPrefix + spec.HashBytes(raw) }

// ErrSpecWriteConflict：既有納管檔的 expected_digest 與目前內容不符（optimistic
// concurrency 撞鎖）。新檔（磁碟上尚不存在）帶非空 expected_digest 視同過期假設，
// 同樣回這個錯誤。
var ErrSpecWriteConflict = errors.New("spec write conflict: expected_digest does not match current file")

// SpecList 列出納管 spec 樹（spec.InScope 過濾），供前端 spec 瀏覽器初始載入。
// 沿用 internal/spec.GitRepo.ReadScopedWorktree 的 walk 慣例：spec/ 尚不存在時
// 回空清單、不是錯誤。
func (a *App) SpecList() ([]FileNode, error) {
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	specRoot := filepath.Join(root, "spec")
	var out []FileNode
	err = filepath.WalkDir(specRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			if os.IsNotExist(werr) && path == specRoot {
				return nil // spec/ 尚未建立：無納管檔
			}
			return werr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() || !spec.InScope(rel) {
			return nil
		}
		out = append(out, FileNode{Name: d.Name(), Path: rel, IsDir: false})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SpecFile：SpecRead 的 JSON-friendly 回傳。Wails v2 對 Go 端多回傳值只保留
// 第一個（App.d.ts 會生成 Promise<string>、實際 runtime 回陣列），前端無法穩定
// 拿到 digest——SpecWrite 的 expectedDigest 卻仰賴它。單一 struct 回傳沒有這個
// multi-return 陷阱（見 Task 14 review parked issue）。
type SpecFile struct {
	Content string `json:"content"`
	Digest  string `json:"digest"`
}

// SpecRead 讀既有納管檔；Digest 為 specDigestOf(raw bytes)，與 SpecWrite 的
// expected_digest／回傳值同格式。
func (a *App) SpecRead(rel string) (SpecFile, error) {
	if !spec.InScope(rel) {
		return SpecFile{}, fmt.Errorf("path %q is not a managed spec file", rel)
	}
	p, err := a.resolveInWorkspace(rel)
	if err != nil {
		return SpecFile{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return SpecFile{}, err
	}
	return SpecFile{Content: string(raw), Digest: specDigestOf(raw)}, nil
}

// deepestExistingAncestor walks up from dir until it finds a path segment
// that already exists on disk (os.Lstat succeeds; does NOT follow the final
// symlink component — presence is all that matters here). Every component
// below the returned ancestor is guaranteed not to exist yet, so it cannot be
// a pre-planted symlink. Terminates at root at the latest (root always exists).
func deepestExistingAncestor(dir, root string) (string, error) {
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if dir == root || parent == dir {
			return dir, nil
		}
		dir = parent
	}
}

// SpecWrite 寫納管檔（新檔或既有檔覆寫），atomic rename＋optimistic concurrency。
//
// 不能重用 resolveInWorkspace：它對「完整目標路徑」做 EvalSymlinks，新檔尚不
// 存在時必然出錯。改為驗證 PARENT 目錄——但驗證必須在任何檔案系統異動之前完成：
// 若 target 的某個中繼目錄（例如納管的 "spec/features"）是預先植入、指向
// workspace 外部的 symlink，MkdirAll 會直接跟著 symlink 在外部建立真實目錄，
// 內容寫入雖然被 InScope／containment 擋住，目錄本身早就逃逸了（deterministic
// escape，不需要 race——一個固定的 symlink 每次都會觸發）。修法：先找 PARENT
// 最深的「已存在」祖先目錄（其下所有路徑段必然尚不存在，不可能是 symlink），
// 對這個祖先做 EvalSymlinks 確認仍在 root 之內，通過才 MkdirAll——之後新建的
// 每一段路徑都在已驗證 contained 的祖先之下、且是全新建立（不是 symlink）。
func (a *App) SpecWrite(rel, content, expectedDigest string) (newDigest string, err error) {
	if !spec.InScope(rel) {
		return "", fmt.Errorf("path %q is not a managed spec file", rel)
	}
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		// resolveInWorkspace 同款顯式拒絕（Clean 會把 /.. 中和成 root，不能無聲重導）
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	target := filepath.Join(root, filepath.Clean("/"+rel))
	parent := filepath.Dir(target)

	if raw, rerr := os.ReadFile(target); rerr == nil {
		if expectedDigest != specDigestOf(raw) {
			return "", ErrSpecWriteConflict
		}
	} else if os.IsNotExist(rerr) {
		if expectedDigest != "" { // 新檔：非空 expected_digest＝呼叫端假設過期
			return "", ErrSpecWriteConflict
		}
	} else {
		return "", rerr
	}

	// containment 驗證在任何 mutation 之前：找 parent 最深已存在祖先、EvalSymlinks
	// 驗證仍在 root 內，通過才允許 MkdirAll（見上方函式註解——擋 pre-existing
	// symlink 逃逸，不是事後補救）。
	ancestor, aerr := deepestExistingAncestor(parent, root)
	if aerr != nil {
		return "", aerr
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if resolvedAncestor != root && !strings.HasPrefix(resolvedAncestor, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if canonicalParent != root && !strings.HasPrefix(canonicalParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	finalTarget := filepath.Join(canonicalParent, filepath.Base(target))

	tmp, err := os.CreateTemp(canonicalParent, ".spec-write-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // rename 成功後 no-op（原路徑已不存在）
	if _, werr := tmp.WriteString(content); werr != nil {
		_ = tmp.Close()
		return "", werr
	}
	if cerr := tmp.Close(); cerr != nil {
		return "", cerr
	}
	if rerr := os.Rename(tmpPath, finalTarget); rerr != nil {
		return "", rerr
	}
	return specDigestOf([]byte(content)), nil
}

// ---- Gate 1（M2 Stage A：spec §3.5／§5.4）----

// gateEmitter：gate.Emitter → Manager.EmitWorkspace，轉 []gate.Binding →
// []contract.Binding（同欄位，無新語意）。
type gateEmitter struct{ a *App }

func (g gateEmitter) EmitGateEvent(kind string, bindings []gate.Binding, payload any) {
	cb := make([]contract.Binding, len(bindings))
	for i, b := range bindings {
		cb[i] = contract.Binding{Kind: b.Kind, Ref: b.Ref, Digest: b.Digest}
	}
	g.a.manager.EmitWorkspace(kind, cb, payload)
}

// ensureGate 惰性初始化 gate.Service／spec.GitRepo：journal 落在 workspace 的
// .workbench/gate.jsonl（spec §5.4：第 2 層 app state、gitignored）——刻意綁
// a.workspaceDir 而非 a.stateDir，兩者production 下同值，但測試會為
// unix socket 路徑長度另配 stateDir，Gate journal 仍必須跟著 workspace 走。
func (a *App) ensureGate() (*gate.Service, error) {
	a.gateOnce.Do(func() {
		root, err := claude.NormalizeCWD(a.workspaceDir)
		if err != nil {
			a.gateInitErr = err
			return
		}
		wbDir := filepath.Join(root, ".workbench")
		if merr := os.MkdirAll(wbDir, 0o755); merr != nil {
			a.gateInitErr = merr
			return
		}
		j, jerr := gate.OpenJournal(filepath.Join(wbDir, "gate.jsonl"))
		if jerr != nil {
			a.gateInitErr = jerr
			return
		}
		a.specRepo = spec.NewGitRepo(root, spec.SpecScope)
		a.planRepo = spec.NewGitRepo(root, spec.PlanScope)
		a.planGit = appGitRunner{root: root}
		a.planLoader = appPlanLoader{git: a.planGit}
		a.gateJournal = j
		currentSpecManifest := func() (string, error) { return spec.BuildCurrentManifest(a.specRepo) }
		currentPlanManifest := func() (string, error) {
			return spec.BuildCurrentManifestScoped(a.planRepo, spec.PlanScope)
		}
		currentRiskPolicyDigest := func() (string, error) { return worktreeRiskPolicyDigest(root) }
		currentPermissionManifest := func() (string, error) { return worktreePermissionManifestDigest(root) }
		// currentOracleDigest（Task 21：§3.9 持續重算）：decl 一律來自 TCA 綁定的
		// gate2 approval plan_commit（TCALoader.LoadOracleAt），這裡只負責對「目前
		// 工作區」重算其 manifest digest——鏡射 currentPlanManifest／
		// currentSpecManifest 建 spec.GitRepo(root, scope) 再走
		// BuildCurrentManifestScoped 的既定用法，scope 換成 decl.Scope()（每次呼叫
		// 依 decl 動態決定，不像 Spec／PlanScope 是套件層級常數）。
		currentOracleDigest := func(decl evidence.OracleDecl) (string, error) {
			return spec.BuildCurrentManifestScoped(spec.NewGitRepo(root, decl.Scope()), decl.Scope())
		}
		ulidFn := func() string { return contract.NewULID(time.Now()) }
		nowFn := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
		reg := gate.Registry{
			"gate1": gate.NewGate1Policy(currentSpecManifest),
			"gate2": gatepolicy.NewGate2Policy(a.planLoader, a.planGit,
				currentPlanManifest, currentSpecManifest, currentRiskPolicyDigest, currentPermissionManifest),
			"test_contract_approval": gatepolicy.NewTCAPolicy(appEvidenceStore{a: a}, appGateReader{a: a},
				a.planLoader, a.planGit, currentOracleDigest),
		}
		a.gateReg = reg // Task 24：submitGateRequest 的 §3.8 (2) pre-validation 用
		a.gateSvc = gate.NewService(j, reg, ulidFn, nowFn, gateEmitter{a})
	})
	return a.gateSvc, a.gateInitErr
}

// gate1Bindings：Gate 1 標準綁定組合（spec §3.5）——committed spec 快照的
// spec_manifest digest ＋ base_commit（spec.BuildCommittedSnapshot 的回傳值）。
func gate1Bindings(manifestDigest, baseCommit string) []gate.Binding {
	return []gate.Binding{
		{Kind: "spec_manifest", Ref: "spec/", Digest: manifestDigest},
		{Kind: "base_commit", Ref: "HEAD", Digest: baseCommit},
	}
}

// SubmitForApproval 以目前 committed spec 快照送出 Gate 1 核可申請。
// dirty tree／HEAD 位移等錯誤原樣自 spec.BuildCommittedSnapshot 傳回。
func (a *App) SubmitForApproval() (string, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return "", err
	}
	manifestDigest, baseCommit, err := spec.BuildCommittedSnapshot(a.specRepo)
	if err != nil {
		return "", err
	}
	return a.submitGateRequest(svc, "gate1", "workspace", gate1Bindings(manifestDigest, baseCommit))
}

// ---- SpecCommit（Task 15：spec §5.1 兩階段 commit UI，wraps internal/spec.GitRepo）----

// SpecCommitPreview：PreviewSpecCommit 的 JSON-friendly 回傳。同 SpecRead 的
// multi-return 教訓——spec.GitRepo.PreviewSpecCommit 回傳 (CommitToken, string,
// error) 三個值，Wails 只保留第一個，struct 回傳才能把 diff 穩定帶給前端。
type SpecCommitPreview struct {
	Token spec.CommitToken `json:"token"`
	Diff  string           `json:"diff"`
}

// PreviewSpecCommit 回傳目前納管樹相對 HEAD 的 diff，並附上綁定當下狀態的
// CommitToken——前端保留此 token，未改動地傳給 ConfirmSpecCommit。
func (a *App) PreviewSpecCommit() (SpecCommitPreview, error) {
	if _, err := a.ensureGate(); err != nil { // 惰性初始化 a.specRepo（同 SubmitForApproval 路徑）
		return SpecCommitPreview{}, err
	}
	tok, diff, err := a.specRepo.PreviewSpecCommit()
	if err != nil {
		return SpecCommitPreview{}, err
	}
	return SpecCommitPreview{Token: tok, Diff: diff}, nil
}

// ConfirmSpecCommit 以 PreviewSpecCommit 回傳的 token 提交納管樹異動；token
// 與目前 repo 狀態不符（HEAD 移動或內容變更）回 spec.ErrCommitStale。
func (a *App) ConfirmSpecCommit(tok spec.CommitToken, message string) error {
	if _, err := a.ensureGate(); err != nil {
		return err
	}
	return a.specRepo.ConfirmSpecCommit(tok, message)
}

// ---- Plan workspace（Task 12：plan/ 綁定＋Gate 2 送核；spec §7 Stage B）----
//
// PlanList／PlanRead／PlanWrite／PreviewPlanCommit／ConfirmPlanCommit 逐字鏡射
// 上方 SpecList／SpecRead／SpecWrite／PreviewSpecCommit／ConfirmSpecCommit（見
// 各自 doc comment 的設計理由，此處不重複展開），scope 換成 spec.PlanScope、
// repo 換成 a.planRepo。SubmitPlanForApproval／GateDecisionContext 是committed
// context 閉環的核心：兩者都只信任 committed（git object database）內容，絕不
// 以 worktree 目前狀態代替已核可／待核的版本。

// appGitRunner 實作 plan.GitRunner（同時也是 gatepolicy.NewGate2Policy 的
// g 參數），走裸 exec.Command(...).Output()、直接回傳未包裝的錯誤——不同於
// spec.GitRepo.git()：後者對 *exec.ExitError 用 %s 攤平成純文字錯誤，會讓
// errors.As(*exec.ExitError) 在呼叫端全部失效（task-12 brief 明確警示）。
// plan.VerifyLineage（merge-base --is-ancestor 的 exit 1 判斷）與
// gatepolicy.Gate2Policy.ReconcileBindings（rev-parse --verify 的 exit 1 vs
// exit 128 判斷）都仰賴 *exec.ExitError 留在 error chain 內，所以這裡刻意
// 不重用 spec.GitRepo 的私有 git()。
type appGitRunner struct{ root string }

func (r appGitRunner) Git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	return cmd.Output()
}

// appPlanLoader 實作 gatepolicy.PlanLoader：讀 committed plan／risk policy，
// 一律經 `git show <oid>:plan/<planID>.yaml`／`git show <oid>:plan/risk-policy.yaml`
// ——絕不讀 worktree，確保 Gate 2 送核與之後任何 decide-time 重驗都綁在同一份
// 不可變的 commit 內容上（committed context 閉環）。檔名慣例：plan/<plan_id>.yaml。
type appPlanLoader struct{ git plan.GitRunner }

func (l appPlanLoader) LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error) {
	planRaw, err := l.git.Git("show", commitOID+":plan/"+planID+".yaml")
	if err != nil {
		return plan.Plan{}, plan.RiskPolicy{}, fmt.Errorf("plan: load plan %q at %s: %w", planID, commitOID, err)
	}
	pl, err := plan.Parse(planRaw)
	if err != nil {
		return plan.Plan{}, plan.RiskPolicy{}, err
	}
	riskRaw, err := l.git.Git("show", commitOID+":plan/risk-policy.yaml")
	if err != nil {
		return plan.Plan{}, plan.RiskPolicy{}, fmt.Errorf("plan: load risk policy at %s: %w", commitOID, err)
	}
	pol, err := plan.ParseRiskPolicy(riskRaw)
	if err != nil {
		return plan.Plan{}, plan.RiskPolicy{}, err
	}
	return pl, pol, nil
}

// LoadOracleAt 實作 evidence.ContextLoader：讀 committed oracle-surface 宣告，
// 同 LoadAt 一律 `git show <oid>:plan/oracle-surface.yaml`——絕不讀 worktree，
// 理由與 LoadAt 相同（committed context 閉環，見上方 doc）。
func (l appPlanLoader) LoadOracleAt(commitOID string) (evidence.OracleDecl, error) {
	raw, err := l.git.Git("show", commitOID+":plan/oracle-surface.yaml")
	if err != nil {
		return evidence.OracleDecl{}, fmt.Errorf("plan: load oracle surface at %s: %w", commitOID, err)
	}
	return evidence.ParseOracleDecl(raw)
}

// appEvidenceStore implements gatepolicy.EvidenceStore over a's evidence
// journal/CAS. Holds a reference to *App (not a value snapshot of
// a.evidenceJournal/a.evidenceCASDir) because those fields are populated by
// startupEvidence(), a function independent of ensureGate()'s lazy init —
// reading through a keeps every call bound to whatever the fields hold at
// call time. Get/Mutation each re-verify the CAS artifacts their journal
// record references (stdout/stderr for a run, the patch for a mutation)
// before returning, folding §3.9's "CAS artifact reread" into the two reads
// TCAPolicy already needs (see gatepolicy.EvidenceStore's doc comment).
type appEvidenceStore struct{ a *App }

func (s appEvidenceStore) Get(evidenceID string) (evidence.EvidenceRun, error) {
	if s.a.evidenceJournal == nil {
		return evidence.EvidenceRun{}, errors.New("evidence: not initialized")
	}
	run, err := s.a.evidenceJournal.Get(evidenceID)
	if err != nil {
		return evidence.EvidenceRun{}, err
	}
	if _, verr := evidence.OpenCAS(s.a.evidenceCASDir, run.StdoutDigest); verr != nil {
		return evidence.EvidenceRun{}, fmt.Errorf("evidence: verify stdout artifact for %s: %w", evidenceID, verr)
	}
	if _, verr := evidence.OpenCAS(s.a.evidenceCASDir, run.StderrDigest); verr != nil {
		return evidence.EvidenceRun{}, fmt.Errorf("evidence: verify stderr artifact for %s: %w", evidenceID, verr)
	}
	return run, nil
}

func (s appEvidenceStore) Mutation(mutationID string) (evidence.Mutation, error) {
	if s.a.evidenceJournal == nil {
		return evidence.Mutation{}, errors.New("evidence: not initialized")
	}
	m, err := s.a.evidenceJournal.GetMutation(mutationID)
	if err != nil {
		return evidence.Mutation{}, err
	}
	if _, verr := evidence.OpenCAS(s.a.evidenceCASDir, m.Digest); verr != nil {
		return evidence.Mutation{}, fmt.Errorf("evidence: verify mutation artifact %s: %w", mutationID, verr)
	}
	return m, nil
}

// appGateReader implements gatepolicy.GateReader by delegating to
// a.gateSvc.Lookup. A thin indirection (rather than passing a.gateSvc
// directly) because the registry that wires TCAPolicy is itself being built
// inside the same ensureGate() call that will assign a.gateSvc — by the time
// any TCAPolicy method runs (always after ensureGate() has returned),
// a.gateSvc is set.
type appGateReader struct{ a *App }

func (r appGateReader) Lookup(approvalID string) (*gate.ApprovalRecord, gate.State, error) {
	return r.a.gateSvc.Lookup(approvalID)
}

// planIDFromSubject／bindingDigest／gitOIDFromDigest mirror
// internal/gatepolicy 的同名未匯出 helper——app 層無法引用其他 package 的未
// 匯出識別字，複製這三個各 4-10 行的小函式比為單一呼叫端擴大 gatepolicy 的
// export surface 更省事（同 gitStatusPath 複製 internal/spec 私有邏輯的先例）。
func planIDFromSubject(subject string) (string, bool) {
	id, ok := strings.CutPrefix(subject, "plan:")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func bindingDigest(bs []gate.Binding, kind string) string {
	for _, b := range bs {
		if b.Kind == kind && b.Role == "" {
			return b.Digest
		}
	}
	return ""
}

func gitOIDFromDigest(digest string) string {
	if s, ok := strings.CutPrefix(digest, "git:sha1:"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(digest, "git:sha256:"); ok {
		return s
	}
	return digest
}

// activeGate1Binding 在 GateList() 的 projection 中找 active gate1 項，回傳
// 其 spec_manifest／base_commit binding digest（從 Bindings 讀，不是
// GateEntryDTO.SpecManifestDigest／BaseCommit ——那兩個欄位鏡射 v1 legacy
// GateRequest 頂層欄位，Submit() 的 v2 路徑從未填過，對 v2 送出的 gate1 請求
// 永遠是空字串；Bindings 才是 pending／active 兩種狀態下都正確的來源）。
func activeGate1Binding(entries []GateEntryDTO) (specManifest, baseCommit string, ok bool) {
	for _, e := range entries {
		if e.Gate != "gate1" || e.State != string(gate.Active) {
			continue
		}
		return bindingDigest(e.Bindings, "spec_manifest"), bindingDigest(e.Bindings, "base_commit"), true
	}
	return "", "", false
}

// activeGate2PlanCommit：RunEvidence 唯一信任的 plan_commit 來源——active
// gate2（subject="plan:"+planID）綁定的 base_commit，即 SubmitPlanForApproval
// 當下的 planHeadOID。evidence.Run 的 ContextLoader 一律以此為 rs.PlanCommit
// 讀取 committed TestContract／oracle-surface，絕不信任 caller 傳入或目前
// worktree 的 plan 內容（鏡射 GateDecisionContext 的 committed context 閉環）。
func activeGate2PlanCommit(entries []GateEntryDTO, planID string) (planCommit string, ok bool) {
	subject := "plan:" + planID
	for _, e := range entries {
		if e.Gate != "gate2" || e.State != string(gate.Active) || e.Subject != subject {
			continue
		}
		return gitOIDFromDigest(bindingDigest(e.Bindings, "base_commit")), true
	}
	return "", false
}

// activeGate2ApprovalID：SubmitTestContract 唯一信任的 gate2_approval 來源
// ——active gate2（subject="plan:"+planID）的 approval_id，用來反查完整
// ApprovalRecord（RecordDigest 需要全部欄位，GateEntryDTO 不夠）。
func activeGate2ApprovalID(entries []GateEntryDTO, planID string) (approvalID string, ok bool) {
	subject := "plan:" + planID
	for _, e := range entries {
		if e.Gate != "gate2" || e.State != string(gate.Active) || e.Subject != subject {
			continue
		}
		return e.ApprovalID, true
	}
	return "", false
}

// worktreePlanDoc 在 worktree 的 plan/ 底下找「唯一」一份可解析為 plan.Plan
// 的 plan/*.yaml（直接子層，不遞迴進 permissions/）——risk-policy.yaml／
// 未來的 oracle-surface.yaml 等其他 plan/ scope 內的 YAML 因 schema 不同
// （plan.Parse 的 KnownFields(true) 會擋掉未知欄位）自然被排除，不需要另外
// 硬編排除檔名。零份或多於一份視為無法判定，fail loud——M3a 假設一個
// workspace 同時只有一份 active plan 草稿（多 plan／多 session 屬 M3b）。
func worktreePlanDoc(root string) (relPath string, doc plan.Plan, err error) {
	matches, err := filepath.Glob(filepath.Join(root, "plan", "*.yaml"))
	if err != nil {
		return "", plan.Plan{}, err
	}
	var found []string
	var docs []plan.Plan
	for _, m := range matches {
		raw, rerr := os.ReadFile(m)
		if rerr != nil {
			continue
		}
		p, perr := plan.Parse(raw)
		if perr != nil || p.PlanID == "" {
			continue
		}
		found = append(found, m)
		docs = append(docs, p)
	}
	switch len(found) {
	case 0:
		return "", plan.Plan{}, errors.New("plan: no plan document found under plan/ (expected exactly one plan/<plan_id>.yaml)")
	case 1:
		rel, rerr := filepath.Rel(root, found[0])
		if rerr != nil {
			return "", plan.Plan{}, rerr
		}
		return filepath.ToSlash(rel), docs[0], nil
	default:
		return "", plan.Plan{}, fmt.Errorf("plan: ambiguous plan documents under plan/: %v", found)
	}
}

// worktreeRiskPolicyDigest reads plan/risk-policy.yaml straight from the
// worktree (§3.9 "持續重算" — the risk_policy binding's current comparator).
func worktreeRiskPolicyDigest(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "plan", "risk-policy.yaml"))
	if err != nil {
		return "", err
	}
	return specDigestOf(raw), nil
}

// permissionManifestScope: canonical digest formula for the permission_manifest
// binding — an ad-hoc Scope (not spec.PlanScope itself; only the permission
// files a plan's tasks actually reference, not every file under plan/) reusing
// the same {scope_version,patterns,files} canonical formula every other
// manifest-shaped binding in this codebase uses (spec.Scope.ManifestDigest).
var permissionManifestScope = spec.Scope{Version: 1, Patterns: []string{"plan/permissions/**"}}

// taskPermissionRefs collects each task's permissions_ref (in task order;
// duplicates across tasks are fine — permissionRefEntries dedups by path).
func taskPermissionRefs(pl plan.Plan) []string {
	refs := make([]string, 0, len(pl.Tasks))
	for _, t := range pl.Tasks {
		refs = append(refs, t.PermissionsRef)
	}
	return refs
}

// permissionRefEntries builds the sorted-by-ManifestDigest FileEntry list for
// refs (paths relative to plan/, e.g. "permissions/T1.yaml"), reading raw
// bytes via read. An empty ref or a read failure (missing file) fails loud —
// §「所有 task permissions_ref 檔案的 canonical manifest digest，缺檔即拒」.
func permissionRefEntries(refs []string, read func(relToPlan string) ([]byte, error)) ([]spec.FileEntry, error) {
	seen := map[string]bool{}
	var entries []spec.FileEntry
	for _, ref := range refs {
		if ref == "" {
			return nil, errors.New("plan: task permissions_ref must not be empty")
		}
		full := "plan/" + ref
		if seen[full] {
			continue
		}
		seen[full] = true
		raw, err := read(ref)
		if err != nil {
			return nil, fmt.Errorf("plan: permissions_ref %q: %w", ref, err)
		}
		entries = append(entries, spec.FileEntry{Path: full, SHA256: spec.HashBytes(raw)})
	}
	return entries, nil
}

// worktreePermissionManifestDigest recomputes the permission_manifest
// binding's current digest against the worktree's current plan document and
// worktree permission files (§3.9 "持續重算").
func worktreePermissionManifestDigest(root string) (string, error) {
	_, pl, err := worktreePlanDoc(root)
	if err != nil {
		return "", err
	}
	entries, err := permissionRefEntries(taskPermissionRefs(pl), func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, "plan", filepath.FromSlash(rel)))
	})
	if err != nil {
		return "", err
	}
	return permissionManifestScope.ManifestDigest(entries)
}

// parseScenarioTags extracts scenario IDs from Gherkin feature content: the
// @Tag token(s) on the line immediately above a "Scenario:"/"Scenario Outline:"
// line become that scenario's ID(s). A scenario with no tag line directly
// above it contributes no ID — such a scenario cannot be referenced by a
// plan task (task-12 brief: 無 tag 的 scenario 不可引用).
func parseScenarioTags(content string) map[string]bool {
	ids := map[string]bool{}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Scenario:") && !strings.HasPrefix(trimmed, "Scenario Outline:") {
			continue
		}
		if i == 0 {
			continue
		}
		prev := strings.TrimSpace(lines[i-1])
		if !strings.HasPrefix(prev, "@") {
			continue
		}
		for _, tok := range strings.Fields(prev) {
			if id, ok := strings.CutPrefix(tok, "@"); ok && id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

// gate1ScenarioIDs enumerates every spec/features/** file at gate1HeadOID
// (the active Gate 1 approval's committed spec tree — never the worktree)
// and unions parseScenarioTags across all of them. A missing spec/features/
// directory at that commit is not an error — `git ls-tree` on a non-existent
// pathspec returns empty output, exit 0 — it just yields no scenario IDs.
func gate1ScenarioIDs(g plan.GitRunner, gate1HeadOID string) (map[string]bool, error) {
	out, err := g.Git("ls-tree", "-r", "--name-only", gate1HeadOID, "--", "spec/features")
	if err != nil {
		return nil, fmt.Errorf("plan: list spec/features at %s: %w", gate1HeadOID, err)
	}
	ids := map[string]bool{}
	for _, path := range strings.Split(string(out), "\n") {
		if path == "" {
			continue
		}
		content, err := g.Git("show", gate1HeadOID+":"+path)
		if err != nil {
			return nil, fmt.Errorf("plan: read %s at %s: %w", path, gate1HeadOID, err)
		}
		for id := range parseScenarioTags(string(content)) {
			ids[id] = true
		}
	}
	return ids, nil
}

// PlanList 列出納管 plan 樹（spec.PlanScope.Match 過濾），鏡射 SpecList。
func (a *App) PlanList() ([]FileNode, error) {
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	planRoot := filepath.Join(root, "plan")
	var out []FileNode
	err = filepath.WalkDir(planRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			if os.IsNotExist(werr) && path == planRoot {
				return nil // plan/ 尚未建立：無納管檔
			}
			return werr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() || !spec.PlanScope.Match(rel) {
			return nil
		}
		out = append(out, FileNode{Name: d.Name(), Path: rel, IsDir: false})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PlanRead 讀既有納管 plan 檔；Digest 格式同 SpecRead（specDigestOf）。
func (a *App) PlanRead(rel string) (SpecFile, error) {
	if !spec.PlanScope.Match(rel) {
		return SpecFile{}, fmt.Errorf("path %q is not a managed plan file", rel)
	}
	p, err := a.resolveInWorkspace(rel)
	if err != nil {
		return SpecFile{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return SpecFile{}, err
	}
	return SpecFile{Content: string(raw), Digest: specDigestOf(raw)}, nil
}

// ErrPlanWriteConflict：PlanWrite 版的 ErrSpecWriteConflict（同一 optimistic
// concurrency 語意，見其 doc comment），獨立錯誤值供呼叫端以 errors.Is 區分
// 是 spec 樹還是 plan 樹撞鎖。
var ErrPlanWriteConflict = errors.New("plan write conflict: expected_digest does not match current file")

// PlanWrite 逐字鏡射 SpecWrite（atomic rename＋optimistic concurrency＋
// symlink-escape containment；完整威脅模型與逐步理由見 SpecWrite 的 doc
// comment，此處不重複），差異僅：scope 檢查換 spec.PlanScope.Match、衝突錯誤
// 換 ErrPlanWriteConflict、暫存檔前綴換 ".plan-write-*.tmp"。
func (a *App) PlanWrite(rel, content, expectedDigest string) (newDigest string, err error) {
	if !spec.PlanScope.Match(rel) {
		return "", fmt.Errorf("path %q is not a managed plan file", rel)
	}
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	target := filepath.Join(root, filepath.Clean("/"+rel))
	parent := filepath.Dir(target)

	if raw, rerr := os.ReadFile(target); rerr == nil {
		if expectedDigest != specDigestOf(raw) {
			return "", ErrPlanWriteConflict
		}
	} else if os.IsNotExist(rerr) {
		if expectedDigest != "" {
			return "", ErrPlanWriteConflict
		}
	} else {
		return "", rerr
	}

	ancestor, aerr := deepestExistingAncestor(parent, root)
	if aerr != nil {
		return "", aerr
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if resolvedAncestor != root && !strings.HasPrefix(resolvedAncestor, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if canonicalParent != root && !strings.HasPrefix(canonicalParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	finalTarget := filepath.Join(canonicalParent, filepath.Base(target))

	tmp, err := os.CreateTemp(canonicalParent, ".plan-write-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, werr := tmp.WriteString(content); werr != nil {
		_ = tmp.Close()
		return "", werr
	}
	if cerr := tmp.Close(); cerr != nil {
		return "", cerr
	}
	if rerr := os.Rename(tmpPath, finalTarget); rerr != nil {
		return "", rerr
	}
	return specDigestOf([]byte(content)), nil
}

// PreviewPlanCommit 回傳目前 plan/ 樹相對 HEAD 的 diff與 CommitToken；
// token.AnalysisBase 取自 worktree 唯一 plan 文件的 analysis_base_commit——
// 讀不到（plan/ 沒有或有多份候選文件）或欄位為空即拒絕（§3.0：lineage 驗證
// 的起點必須先在 worktree 就確立，Confirm 時再核對是否漂移）。
func (a *App) PreviewPlanCommit() (SpecCommitPreview, error) {
	if _, err := a.ensureGate(); err != nil { // 惰性初始化 a.planRepo
		return SpecCommitPreview{}, err
	}
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return SpecCommitPreview{}, err
	}
	_, pl, err := worktreePlanDoc(root)
	if err != nil {
		return SpecCommitPreview{}, fmt.Errorf("preview plan commit: %w", err)
	}
	if pl.AnalysisBaseCommit == "" {
		return SpecCommitPreview{}, errors.New("plan: analysis_base_commit missing in worktree plan — run PlannerAssist and keep its value before commit")
	}
	tok, diff, err := a.planRepo.PreviewSpecCommit()
	if err != nil {
		return SpecCommitPreview{}, err
	}
	tok.AnalysisBase = pl.AnalysisBaseCommit
	return SpecCommitPreview{Token: tok, Diff: diff}, nil
}

// ConfirmPlanCommit 以 PreviewPlanCommit 回傳的 token 提交 plan/ 樹異動。除
// planRepo.ConfirmSpecCommit 既有的 HeadOID／TreeDigest staleness 檢查外，
// 額外重讀 worktree plan 的 analysis_base_commit：與 token.AnalysisBase 不符
// （含此時讀不到）視同 token 過期，回 spec.ErrCommitStale——commit 期間
// analysis_base_commit 被改動，Preview 當下核對過的 lineage 起點已不可信。
func (a *App) ConfirmPlanCommit(tok spec.CommitToken, message string) error {
	if _, err := a.ensureGate(); err != nil {
		return err
	}
	root, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return err
	}
	_, pl, err := worktreePlanDoc(root)
	if err != nil || pl.AnalysisBaseCommit == "" || pl.AnalysisBaseCommit != tok.AnalysisBase {
		return spec.ErrCommitStale
	}
	return a.planRepo.ConfirmSpecCommit(tok, message)
}

// ---- analysis_base bump（Task 5，spec §3.2）----
//
// PreviewAnalysisBaseBump／ConfirmAnalysisBaseBump 是唯讀-then-write-back 的
// 兩段式流程：Preview 只讀（不動任何檔案），把驗證過的 old／head／後端自算
// 的 buffer digest 綁進 BumpToken；Confirm 拿著這個 token 重驗一次目前狀態
// （buffer／planRel／HEAD 皆須與 Preview 當下一致），通過後才用字串定位把
// buffer 內那一行的值換成新的 HEAD，回傳 updatedBuffer——不寫檔、不
// commit，落地交給呼叫端（wailsjs 重生留給 Task 6 前端任務）。digest 一律
// 後端 sha256(buffer) 自算：BumpToken 沒有欄位讓前端塞自己算的值，Confirm
// 重算 currentBuffer 才拿去跟 token 比對，前端不可能偽造出一個能通過驗證
// 的 token。Confirm 通過後、儲存前 HEAD 再動的 TOCTOU，由呼叫端接下來要走
// 的 ConfirmPlanCommit 的 commit token 鏈（HeadOID／TreeDigest staleness
// 檢查）接手防護——本函式只保證回傳當下 updatedBuffer 的內容正確。

// BumpToken binds a PreviewAnalysisBaseBump result to the exact plan path,
// old analysis_base_commit value, HEAD, and buffer digest at preview time —
// every field ConfirmAnalysisBaseBump re-verifies before touching anything,
// so a stale preview (HEAD moved, buffer edited elsewhere, wrong plan) fails
// loud instead of silently replacing the wrong value.
type BumpToken struct {
	PlanRel      string `json:"plan_rel"`
	Old          string `json:"old"`
	Head         string `json:"head"`
	BufferDigest string `json:"buffer_digest"` // 後端 sha256(buffer)，hex
}

// BumpPreview is PreviewAnalysisBaseBump's result. NoBumpNeeded true means
// no token was issued (Token stays the zero value) — old already equals
// HEAD, or every path old..HEAD touched stays inside plan/**, so there is
// nothing for a bump to move.
type BumpPreview struct {
	Token        BumpToken    `json:"token"`
	Old          string       `json:"old"`
	Head         string       `json:"head"`
	Commits      []CommitInfo `json:"commits"`
	TouchedFiles []string     `json:"touched_files"`
	NoBumpNeeded bool         `json:"no_bump_needed"`
}

// fullOIDPattern rejects abbreviated/short OIDs — a bump must anchor to an
// unambiguous, fully-qualified commit id, never a prefix that could resolve
// to a different object as the repository grows.
var fullOIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// bumpPlanDoc is a minimal decode target for extracting analysis_base_commit
// out of a plan buffer — deliberately not plan.Parse's KnownFields(true)
// schema: bump only needs this one field and must not reject on unrelated
// schema drift elsewhere in an in-progress editor buffer (task-5-brief:
// buffer 解析 analysis_base_commit，yaml 解析取值即可).
type bumpPlanDoc struct {
	AnalysisBaseCommit string `yaml:"analysis_base_commit"`
}

// parseAnalysisBaseCommit extracts buffer's analysis_base_commit and
// validates it is a full 40-hex commit OID — a prerequisite for the
// existence／ancestor checks PreviewAnalysisBaseBump performs next. Neither
// check touches git; a malformed/empty value fails here without spawning a
// process.
func parseAnalysisBaseCommit(buffer string) (string, error) {
	var doc bumpPlanDoc
	if err := yaml.Unmarshal([]byte(buffer), &doc); err != nil {
		return "", fmt.Errorf("plan: bump: parse buffer: %w", err)
	}
	old := doc.AnalysisBaseCommit
	if !fullOIDPattern.MatchString(old) {
		return "", fmt.Errorf("plan: bump: analysis_base_commit %q is not a full commit id — re-run PlannerAssist", old)
	}
	return old, nil
}

// bumpExitCoder matches *exec.ExitError's ExitCode() method structurally —
// same trick as plan.VerifyLineage／gatepolicy.Gate2Policy.ReconcileBindings
// — so an expected git exit code ("commit missing", "not an ancestor") can
// be told apart from a fatal/unrecognized failure without importing os/exec.
type bumpExitCoder interface{ ExitCode() int }

// verifyCommitExists checks oid names a real commit object, via
// `git rev-parse --verify --quiet <oid>^{commit}` — the same existence
// check gatepolicy.Gate2Policy.ReconcileBindings uses for base_commit
// (exit 1 = missing, anything else = fatal, fail closed).
func verifyCommitExists(g plan.GitRunner, oid string) error {
	if _, err := g.Git("rev-parse", "--verify", "--quiet", oid+"^{commit}"); err != nil {
		var ec bumpExitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 1 {
			return fmt.Errorf("plan: bump: analysis_base_commit %s not found in this repository — re-run PlannerAssist", oid)
		}
		return err
	}
	return nil
}

// verifyIsAncestor checks ancestor is a git ancestor of descendant, via
// `git merge-base --is-ancestor` — same exit-code split as
// plan.VerifyLineage, but deliberately not VerifyLineage itself: that
// function also rejects any touched path outside an allow scope, the
// opposite of what a bump needs (a bump exists precisely because something
// outside plan/** changed).
func verifyIsAncestor(g plan.GitRunner, ancestor, descendant string) error {
	if _, err := g.Git("merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		var ec bumpExitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 1 {
			return fmt.Errorf("plan: bump: analysis_base_commit %s is not an ancestor of HEAD — re-run PlannerAssist", ancestor)
		}
		return err
	}
	return nil
}

// splitBumpNULFields splits a `git diff --name-status -z` record stream on
// NUL. Mirrors internal/plan/lineage.go's unexported splitNULFields
// (duplicated rather than exported cross-package for this single caller,
// to keep plan's I/O-free package boundary unchanged) — real output always
// ends with a trailing NUL after the last field, which must be dropped.
func splitBumpNULFields(out []byte) []string {
	trimmed := bytes.TrimRight(out, "\x00")
	if len(trimmed) == 0 {
		return nil
	}
	return strings.Split(string(trimmed), "\x00")
}

// bumpTouchedFiles lists paths changed in old..head via
// `git diff --name-status -z --find-renames` — same record shape
// plan.VerifyLineage parses (NUL-delimited; an R/C rename/copy record
// carries two paths, everything else carries one). paths is the flat list
// for display (a rename contributes its new path only — what an operator
// wants to see changed); allPlanOnly folds in BOTH sides of every
// rename/copy record, because a path that moved from outside plan/** into
// plan/** (or vice versa) is a real change to the non-plan tree even though
// its post-rename path alone would satisfy spec.PlanScope.Match (review F2:
// `git diff --name-only` alone only reports the new path and would
// misclassify such a move as plan-only).
func bumpTouchedFiles(g plan.GitRunner, old, head string) (paths []string, allPlanOnly bool, err error) {
	out, err := g.Git("diff", "--name-status", "-z", "--find-renames", old+".."+head)
	if err != nil {
		return nil, false, err
	}
	fields := splitBumpNULFields(out)
	allPlanOnly = true
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		switch status[0] {
		case 'R', 'C':
			if i+1 >= len(fields) {
				return nil, false, fmt.Errorf("plan: bump: malformed diff entry: status %q missing paths", status)
			}
			oldPath, newPath := fields[i], fields[i+1]
			i += 2
			paths = append(paths, newPath)
			if !spec.PlanScope.Match(oldPath) || !spec.PlanScope.Match(newPath) {
				allPlanOnly = false
			}
		default:
			if i >= len(fields) {
				return nil, false, fmt.Errorf("plan: bump: malformed diff entry: status %q missing path", status)
			}
			p := fields[i]
			i++
			paths = append(paths, p)
			if !spec.PlanScope.Match(p) {
				allPlanOnly = false
			}
		}
	}
	return paths, allPlanOnly, nil
}

// PreviewAnalysisBaseBump（§3.2）reads buffer's analysis_base_commit (never
// trusts a caller-supplied value) and validates it is a full, existing
// commit OID that is an ancestor of the current HEAD — any failure rejects
// with guidance to re-run PlannerAssist. old == HEAD, or every path
// old..HEAD touched staying inside plan/**, both mean there is nothing to
// bump (NoBumpNeeded: true, no token issued). Otherwise returns a BumpToken
// (carrying a backend-computed sha256(buffer) digest, never a
// caller-supplied one) plus the commit log and touched files for the
// operator to review before confirming.
func (a *App) PreviewAnalysisBaseBump(planRel, buffer string) (BumpPreview, error) {
	if _, err := a.ensureGate(); err != nil { // 惰性初始化 a.planGit
		return BumpPreview{}, err
	}
	old, err := parseAnalysisBaseCommit(buffer)
	if err != nil {
		return BumpPreview{}, err
	}
	if err := verifyCommitExists(a.planGit, old); err != nil {
		return BumpPreview{}, err
	}
	headOut, err := a.planGit.Git("rev-parse", "HEAD")
	if err != nil {
		return BumpPreview{}, err
	}
	head := strings.TrimSpace(string(headOut))

	if old == head {
		return BumpPreview{Old: old, Head: head, NoBumpNeeded: true}, nil
	}
	if err := verifyIsAncestor(a.planGit, old, head); err != nil {
		return BumpPreview{}, err
	}

	touched, allPlanOnly, err := bumpTouchedFiles(a.planGit, old, head)
	if err != nil {
		return BumpPreview{}, err
	}
	if allPlanOnly {
		return BumpPreview{Old: old, Head: head, TouchedFiles: touched, NoBumpNeeded: true}, nil
	}

	logOut, err := a.planGit.Git("log", "--format=%H%x00%s", "-n", "50", old+".."+head)
	if err != nil {
		return BumpPreview{}, err
	}

	tok := BumpToken{PlanRel: planRel, Old: old, Head: head, BufferDigest: spec.HashBytes([]byte(buffer))}
	return BumpPreview{
		Token:        tok,
		Old:          old,
		Head:         head,
		Commits:      parseCommitCandidates(logOut),
		TouchedFiles: touched,
	}, nil
}

// analysisBaseCommitKey is the plan schema's analysis_base_commit key,
// including its trailing colon — the string-anchor every step below keys
// off of (isAnalysisBaseCommitLine's prefix check and
// parseAnalysisBaseCommitLine's split point).
const analysisBaseCommitKey = "analysis_base_commit:"

// isAnalysisBaseCommitLine reports whether line's trimmed content begins
// with the plan schema's analysis_base_commit key. This is only a
// structural/textual filter — a multi-line block scalar's content line can
// coincidentally satisfy it too (review F1); parseAnalysisBaseCommitLine's
// extracted value must additionally equal the expected old value before a
// line is trusted as the real key.
func isAnalysisBaseCommitLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), analysisBaseCommitKey)
}

// parseAnalysisBaseCommitLine splits a line isAnalysisBaseCommitLine has
// already confirmed into: prefix (indentation + key + colon + separating
// whitespace, kept verbatim), quote (0 for a bare/plain scalar, or the
// quote byte — a double or single quote character — the value is wrapped
// in), value (the scalar's literal content — dequoted, so it is directly
// comparable to a BumpToken's Old/Head, which are always bare Go strings
// regardless of how the source YAML quoted them), and rest (everything
// after the value: inline comment,
// trailing whitespace, kept verbatim). ok is false for anything this
// deliberately-not-a-YAML-parser line scan cannot safely handle — no value
// at all, or an unterminated quote — so callers never index into a failed
// parse (review F3: the previous regex-based version indexed a nil
// submatch and panicked on exactly this shape).
func parseAnalysisBaseCommitLine(line string) (prefix string, quote byte, value string, rest string, ok bool) {
	idx := strings.Index(line, analysisBaseCommitKey)
	if idx < 0 {
		return "", 0, "", "", false
	}
	prefix = line[:idx+len(analysisBaseCommitKey)]
	remainder := line[idx+len(analysisBaseCommitKey):]
	sp := 0
	for sp < len(remainder) && (remainder[sp] == ' ' || remainder[sp] == '\t') {
		sp++
	}
	prefix += remainder[:sp]
	remainder = remainder[sp:]
	if remainder == "" {
		return "", 0, "", "", false
	}
	if remainder[0] == '"' || remainder[0] == '\'' {
		q := remainder[0]
		end := strings.IndexByte(remainder[1:], q)
		if end < 0 {
			return "", 0, "", "", false
		}
		return prefix, q, remainder[1 : 1+end], remainder[1+end+1:], true
	}
	if end := strings.IndexAny(remainder, " \t"); end >= 0 {
		return prefix, 0, remainder[:end], remainder[end:], true
	}
	return prefix, 0, remainder, "", true
}

// replaceAnalysisBaseCommitLine returns line with only its
// analysis_base_commit value replaced by newVal, preserving the original
// quote characters verbatim if the value was quoted (review F4: a bare
// `(\S+)` replacement previously swallowed a `"..."` value's closing quote,
// corrupting the YAML). ok is false when line does not parse as an
// analysis_base_commit line at all (see parseAnalysisBaseCommitLine).
func replaceAnalysisBaseCommitLine(line, newVal string) (string, bool) {
	prefix, quote, _, rest, ok := parseAnalysisBaseCommitLine(line)
	if !ok {
		return "", false
	}
	if quote != 0 {
		return prefix + string(quote) + newVal + string(quote) + rest, true
	}
	return prefix + newVal + rest, true
}

// ConfirmAnalysisBaseBump（§3.2）re-verifies every field of tok against the
// caller's current state — planRel unchanged, currentBuffer's backend-
// recomputed digest still matches (never trusts a caller-supplied digest),
// HEAD unmoved since preview — then scans currentBuffer for
// analysis_base_commit lines. Any such line whose extracted (dequoted)
// value does not equal tok.Old rejects immediately and specifically: it
// is either malformed (review F3) or, as it stands, most likely
// unrelated text that coincidentally starts with the same key text — e.g.
// inside a block scalar (review F1) — which the previous count-only "恰
// 一處" check could not tell apart from the real key, permanently rejecting
// (re-running preview never changes a buffer's own text, so that was a
// dead-end retry loop, not a recoverable staleness error). Once every
// matching line's value is confirmed consistent, exactly one such line must
// remain (0 or ≥2 still rejects: string-position replacement is only
// well-defined with exactly one). On success, replaces only that line's
// value with tok.Head — preserving quotes if the value was quoted (review
// F4) — and returns the full updatedBuffer; every other byte, including
// comments and formatting, untouched. Does not write to disk or commit; the
// caller decides when to land the result.
func (a *App) ConfirmAnalysisBaseBump(tok BumpToken, planRel, currentBuffer string) (string, error) {
	if _, err := a.ensureGate(); err != nil {
		return "", err
	}
	if planRel != tok.PlanRel {
		return "", errors.New("plan: bump: plan path changed since preview — re-run preview")
	}
	if spec.HashBytes([]byte(currentBuffer)) != tok.BufferDigest {
		return "", errors.New("plan: bump: buffer changed since preview — re-run preview")
	}
	headOut, err := a.planGit.Git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(headOut)) != tok.Head {
		return "", errors.New("plan: bump: HEAD moved since preview — re-run preview")
	}

	lines := strings.Split(currentBuffer, "\n")
	var matchIdx []int
	for i, l := range lines {
		if !isAnalysisBaseCommitLine(l) {
			continue
		}
		_, _, value, _, ok := parseAnalysisBaseCommitLine(l)
		if !ok {
			return "", fmt.Errorf("plan: bump: line %d looks like an analysis_base_commit key but its value could not be parsed: %q — re-run preview", i+1, l)
		}
		if value != tok.Old {
			return "", fmt.Errorf("plan: bump: line %d looks like an analysis_base_commit key but holds %q, not the expected old value %q — likely unrelated text coincidentally starting with the same key (e.g. inside a block scalar); edit the buffer to remove the ambiguity, then re-run preview", i+1, value, tok.Old)
		}
		matchIdx = append(matchIdx, i)
	}
	if len(matchIdx) != 1 {
		return "", fmt.Errorf("plan: bump: buffer must contain exactly one analysis_base_commit line holding the expected old value %q, found %d — re-run preview", tok.Old, len(matchIdx))
	}

	newLine, ok := replaceAnalysisBaseCommitLine(lines[matchIdx[0]], tok.Head)
	if !ok {
		return "", fmt.Errorf("plan: bump: line %d could not be rewritten: %q", matchIdx[0]+1, lines[matchIdx[0]])
	}
	lines[matchIdx[0]] = newLine
	return strings.Join(lines, "\n"), nil
}

// gate2Bindings assembles the five §3.3 required Gate 2 bindings.
func gate2Bindings(specManifest, planManifest, baseCommit, riskPolicyDigest, permissionManifestDigest string) []gate.Binding {
	return []gate.Binding{
		{Kind: "spec_manifest", Ref: "spec/", Digest: specManifest},
		{Kind: "plan", Ref: "plan/", Digest: planManifest},
		{Kind: "base_commit", Ref: "HEAD", Digest: baseCommit},
		{Kind: "risk_policy", Ref: "plan/risk-policy.yaml", Digest: riskPolicyDigest},
		{Kind: "permission_manifest", Ref: "plan/permissions/", Digest: permissionManifestDigest},
	}
}

// SubmitPlanForApproval 送出 Gate 2 核可申請（committed context 閉環，凍結
// 順序，見 task-12-brief.md）：
//  1. 存在 active Gate 1；spec_manifest／base_commit 直接取自其 binding
//     （不重算 worktree——兩者不一致由 Gate 1 STALE 機制處理，非本函式職責）
//  2. plan/ scope dirty-tree 拒核（BuildCommittedSnapshot with PlanScope）
//  3. 讀 committed plan（LoadAt(plan_commit=HEAD, planID)）
//  4. scenario 集合取自 active Gate 1 綁定的 committed spec tree
//  5. plan.Validate（fail 即拒）
//  6. lineage 驗證（analysis_base_commit..plan_commit 限 plan/**）
//  7. 組五筆 bindings → Submit("gate2","plan:"+planID,bindings)
func (a *App) SubmitPlanForApproval(planID string) (string, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return "", err
	}

	entries, err := a.GateList()
	if err != nil {
		return "", err
	}
	gate1SpecManifest, gate1BaseCommit, ok := activeGate1Binding(entries)
	if !ok {
		return "", errors.New("plan: no active Gate 1 approval — approve the spec before submitting a plan for Gate 2")
	}

	planManifestDigest, planBaseCommit, err := spec.BuildCommittedSnapshotScoped(a.planRepo, spec.PlanScope)
	if err != nil {
		return "", err
	}
	planHeadOID := gitOIDFromDigest(planBaseCommit)

	pl, pol, err := a.planLoader.LoadAt(planHeadOID, planID)
	if err != nil {
		return "", fmt.Errorf("plan: load committed plan %q at %s: %w", planID, planHeadOID, err)
	}
	if pl.PlanID != planID {
		return "", fmt.Errorf("plan: committed plan.yaml plan_id %q does not match filename-derived plan ID %q", pl.PlanID, planID)
	}

	gate1HeadOID := gitOIDFromDigest(gate1BaseCommit)
	specScenarios, err := gate1ScenarioIDs(a.planGit, gate1HeadOID)
	if err != nil {
		return "", err
	}

	if errs := plan.Validate(pl, pol, specScenarios); len(errs) > 0 {
		verr := fmt.Errorf("plan: validation failed（scenario not found 時，檢查該 scenario 是否以上一行 @tag 命名——parseScenarioTags 只認上一行的 @tag）: %w", errors.Join(errs...))
		if isRiskUnclassifiable(errs) { // §3.8 (1)：risk 分類失敗（minimum 無法重算）
			a.workflowMu.Lock()
			_, cerr := a.escCreateSystemLocked("risk-unclassifiable:"+planID, "gate2:"+planID, true,
				"plan "+planID+" 的 risk 分類驗證失敗（minimum 無法重算）", "plan:"+planID)
			a.workflowMu.Unlock()
			if cerr != nil {
				return "", errors.Join(verr, cerr)
			}
		}
		return "", verr
	}
	// (1) 權威修復：新版 committed plan 通過 plan.Validate 即系統解除同 key
	// （無未 resolved 項時 no-op）。
	a.workflowMu.Lock()
	riskResolveErr := a.escResolveByKeyLocked("risk-unclassifiable:"+planID, "validated:"+planHeadOID)
	a.workflowMu.Unlock()
	if riskResolveErr != nil {
		return "", riskResolveErr
	}

	if err := plan.VerifyLineage(a.planGit, pl.AnalysisBaseCommit, planHeadOID, spec.PlanScope.Match); err != nil {
		return "", err
	}

	riskPolicyRaw, err := a.planGit.Git("show", planHeadOID+":plan/risk-policy.yaml")
	if err != nil {
		return "", fmt.Errorf("plan: read committed risk policy at %s: %w", planHeadOID, err)
	}
	riskPolicyDigest := specDigestOf(riskPolicyRaw)

	permEntries, err := permissionRefEntries(taskPermissionRefs(pl), func(rel string) ([]byte, error) {
		return a.planGit.Git("show", planHeadOID+":plan/"+rel)
	})
	if err != nil {
		return "", err
	}
	permissionManifestDigest, err := permissionManifestScope.ManifestDigest(permEntries)
	if err != nil {
		return "", err
	}

	bindings := gate2Bindings(gate1SpecManifest, planManifestDigest, planBaseCommit, riskPolicyDigest, permissionManifestDigest)
	return a.submitGateRequest(svc, "gate2", "plan:"+planID, bindings)
}

// isRiskUnclassifiable：plan.Validate 的錯誤中是否含 §3.8 (1) 的 risk 分類
// 失敗（minimum 重算不符、planner 低於 minimum、tier 名稱未知）。改用
// errors.Is 對 plan.ErrRiskUnclassifiable 判定（review 補強），取代先前以訊息
// 片段比對的脆弱作法。
func isRiskUnclassifiable(errs []error) bool {
	for _, e := range errs {
		if errors.Is(e, plan.ErrRiskUnclassifiable) {
			return true
		}
	}
	return false
}

// ---- Evidence run（Task 20：M3a §4-5，wraps internal/evidence）----

// RegisterMutation 把一份 negative_control 用的 patch 存進 evidence CAS
// store，再把描述它的 Mutation 記錄 append 進 evidence journal，回傳新產生的
// mutation_id。RunEvidence 的 negative_control 路徑只接受 mutation_id（不接受
// 原始 patch bytes）——patch 內容一律經由此登記，落盤與可稽核的紀錄同時成立
// （鏡射 Task 17 CAS＋Task 20 journal 的既定順序：CAS 先落盤才 append）。
func (a *App) RegisterMutation(taskRef, patch string) (string, error) {
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}
	digest, _, err := evidence.PutCAS(a.evidenceCASDir, []byte(patch))
	if err != nil {
		return "", fmt.Errorf("evidence: put mutation patch in CAS: %w", err)
	}
	m := evidence.Mutation{
		MutationID: contract.NewULID(time.Now()),
		TaskRef:    taskRef,
		Digest:     digest,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := a.evidenceJournal.AppendMutation(m); err != nil {
		return "", fmt.Errorf("evidence: append mutation: %w", err)
	}
	return m.MutationID, nil
}

// ErrStaleGeneration：RunEvidence 的 CAS 換版失敗——呼叫端（TcaWorkspace）
// 讀取 active Gate 2 approval 當下的 approval_id（expectedGate2ApprovalID）到
// 這次呼叫實際取得 workflowMu 之間，那筆 approval 已被換版（gate2 supersede：
// 新 SubmitPlanForApproval→GateDecide 核可）。錯誤訊息前端原文顯示（§3.3.2）。
var ErrStaleGeneration = errors.New("evidence: gate2 approval changed since view was loaded")

// RunEvidence 同步執行 planID/taskID 已核可（Gate 2 active）的 TestContract，
// 對 testCommit 的 committed tree 產生一筆 EvidenceRun，回傳 evidence_id。
// kind="negative_control" 時 mutationID 必填，其登記的 patch（RegisterMutation）
// 會被套用；kind="expected_red" 時 mutationID 必須為空（evidence.Run 本身拒絕
// 帶 mutation 的 expected_red，見 runner.go）。
//
// expectedGate2ApprovalID：呼叫端（TcaWorkspace）觀察到的 active Gate 2
// approval_id 快照——RunEvidence 以它跟自己在 workflowMu 下重讀的權威值做 CAS
// 比對（§3.3.2）：不符即代表使用者按下按鈕後、這次呼叫真正取得 workflowMu
// 之前，該 plan 的 Gate 2 已換版，回 ErrStaleGeneration，且**零 side
// effect**——不發 started event、不載入 mutation、不建 worktree（凍結順序見
// 下方 Step 2 分段）。
//
// Lifecycle ownership（task-20-brief.md 凍結，不依賴 Task 24 的
// workflowMu）：beginAppTxn() 是 shutdown gate 的入場點（沿 app.go:152 慣例，
// shutdown 後拒新 run）；執行 context 衍生自 a.ctx（app 的 shutdown-scoped
// context，同 SpecAssist／StartSession 的既定用法），供 reclaimEvidenceRuns
// 手動 cancel。evidence.Run 內部才會 mint evidence_id（ulid callback），所以
// active-run registry 的登記時機挪進 ulid callback 本身。
//
// beginAppTxn 成功到 ulid callback 執行之間，evidence.Run 已經先跑了
// LoadAt／LoadOracleAt／VerifyLineage／OracleDigestAt 這串 git 呼叫——這段
// 窗口 evidenceActive 還沒有這筆 run 的登記，若 shutdown 的 reclaimEvidenceRuns
// snapshot 剛好落在這裡，它會拿到空清單、cancel 永遠送不到這個 run（review
// M1）。ulid callback 因此在登記進 a.evidenceActive 之後，於 shutMu 下複查
// a.shuttingDown：若已經在 shutdown 中（不論 reclaimEvidenceRuns 是否已經跑
// 過、還是根本還沒開始），就自我 cancel——不依賴任何人「之後」再來 cancel
// 一次，因為 reclaimEvidenceRuns 那一次性的 snapshot 可能已經錯過。
// registry 移除與 journal finalize（AppendEvidenceRun）在同一個 evidenceMu
// 臨界區內完成：這是「恰一次 finalize」的落點。若 ctx 在 Run 返回時已被取消
// （shutdown reclaim 或上述自我 cancel 造成），即使 evidence.Run 本身回傳了
// 一筆語意完整的 EvidenceRun（ctx 取消走的是 abortReason="context
// canceled"，不是 Go error），也視為未完成、不 finalize——一個被 shutdown
// 中止的 run 不能被當成有效證據收進 journal。
//
// M3a.1 T8（§3.3.2）凍結順序：beginAppTxn → workflowMu.Lock → 讀取並固定
// 權威 active gate2 approval_id／plan_commit（GateList 挪到 beginAppTxn 之
// 後、workflowMu 下讀——不再信任呼叫前的快照）→ CAS 比對 expected（不符→
// Unlock→endAppTxn→ErrStaleGeneration）→ workflowMu.Unlock → Step 2b：
// shutMu 下重查 shuttingDown（沿 pre-ulid 窗自我 cancel 先例——CAS 通過後到
// started event 之間若 shutdown 已開始，零副作用返回，不指望 ulid callback
// 那次複查還來得及，因為這個 run 這時甚至還沒發過 started event）→ started
// event／mutation 載入／worktree 建立／run → finalize → endAppTxn。
// runEvidenceCASHook（測試 seam，沿 decideBarrierHook 命名慣例）在
// beginAppTxn 成功後、workflowMu.Lock 前觸發——特意早於 Lock，讓 hook 內部
// 可呼叫 GateDecide 之類同樣取 workflowMu 的操作換版，而不會跟本呼叫自己的
// Lock 死鎖。
func (a *App) RunEvidence(expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID string) (string, error) {
	if kind != "expected_red" && kind != "negative_control" {
		return "", fmt.Errorf("evidence: unknown kind %q", kind)
	}
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}
	if kind == "negative_control" {
		if mutationID == "" {
			return "", errors.New("evidence: negative_control requires a mutation_id")
		}
	} else if mutationID != "" {
		return "", errors.New("evidence: expected_red must not carry a mutation_id")
	}

	if err := a.beginAppTxn(); err != nil { // shutdown gate：拒新 run
		return "", err
	}
	defer a.endAppTxn()

	if h := a.runEvidenceCASHook; h != nil { // 測試 seam：見上方函式 doc
		h()
	}

	a.workflowMu.Lock()
	entries, err := a.GateList()
	if err != nil {
		a.workflowMu.Unlock()
		return "", err
	}
	approvalID, aok := activeGate2ApprovalID(entries, planID)
	planCommit, pok := activeGate2PlanCommit(entries, planID)
	if !aok || !pok {
		a.workflowMu.Unlock()
		return "", fmt.Errorf("evidence: no active Gate 2 approval for plan %q", planID)
	}
	if approvalID != expectedGate2ApprovalID { // CAS：不符即換版，零 side effect
		a.workflowMu.Unlock()
		return "", ErrStaleGeneration
	}
	a.workflowMu.Unlock()

	// Step 2b（review M1 pre-ulid 窗自我 cancel 的同一先例）：CAS 通過後、
	// started event 之前重查 shutdown——已進 shutdown 即零副作用返回，不發
	// started、不載入 mutation、不建 worktree。
	a.shutMu.Lock()
	shuttingDown := a.shuttingDown
	a.shutMu.Unlock()
	if shuttingDown {
		return "", errors.New("app shutting down")
	}

	var mutationPatch []byte
	if kind == "negative_control" {
		m, merr := a.evidenceJournal.GetMutation(mutationID)
		if merr != nil {
			return "", fmt.Errorf("evidence: load mutation %q: %w", mutationID, merr)
		}
		patch, oerr := evidence.OpenCAS(a.evidenceCASDir, m.Digest)
		if oerr != nil {
			return "", fmt.Errorf("evidence: open mutation patch: %w", oerr)
		}
		mutationPatch = patch
	}

	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()

	a.manager.EmitWorkspace(evidenceRunEventKind, nil, map[string]any{
		"phase": "started", "plan_id": planID, "task_id": taskID,
		"kind": kind, "test_commit": testCommit, "gate2_approval_id": approvalID,
	})

	var evidenceID string
	ulidFn := func() string {
		id := contract.NewULID(time.Now())
		a.evidenceMu.Lock()
		a.evidenceActive[id] = cancel
		a.evidenceMu.Unlock()
		evidenceID = id

		// review M1：登記後立刻複查 shuttingDown——若 shutdown 已經開始（不論
		// reclaimEvidenceRuns 的 snapshot 是落在登記之前還是之後），自我
		// cancel，不指望還會有第二次 reclaim 機會來 cancel 這筆剛登記的 run。
		a.shutMu.Lock()
		shuttingDown := a.shuttingDown
		a.shutMu.Unlock()
		if shuttingDown {
			cancel()
		}
		return id
	}
	nowFn := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

	ld := evidence.ContextLoader(a.planLoader)
	if a.evidenceContextLoaderOverride != nil { // 測試注入：見 evidenceContextLoaderOverride 欄位 doc
		ld = a.evidenceContextLoaderOverride
	}
	rs := evidence.RunSpec{
		Kind: kind, PlanID: planID, TaskID: taskID,
		PlanCommit: planCommit, TestCommit: testCommit, MutationPatch: mutationPatch,
	}
	run, runErr := evidence.Run(ctx, a.workspaceDir, a.evidenceCASDir, a.evidenceRegistryPath,
		ld, rs, ulidFn, nowFn)
	if runErr == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			runErr = fmt.Errorf("evidence: run canceled: %w", ctxErr)
		}
	}

	a.evidenceMu.Lock()
	if evidenceID != "" {
		delete(a.evidenceActive, evidenceID)
	}
	var appendErr error
	if runErr == nil {
		appendErr = a.evidenceJournal.AppendEvidenceRun(run)
	}
	a.evidenceMu.Unlock()

	finalErr := runErr
	if finalErr == nil {
		finalErr = appendErr
	}

	// Task 24（§3.8 (5)(6)(7)＋A8）：finalize 成功（run 已 durable）才接線
	// escalation 自動來源。workflowMu 在 evidenceMu 臨界區之外另取（lock
	// ordering：兩把鎖不巢狀，見 workflowMu 欄位 doc）；escalation 寫入失敗
	// fail loud——run 記錄本身已 durable，但自動來源沒記上不得無聲。
	if finalErr == nil {
		if escErr := a.wireEvidenceEscalation(planID, taskID, kind, evidenceID, run.Result); escErr != nil {
			finalErr = escErr
		}
	} else if runErr != nil && ctx.Err() == nil {
		// review Medium（§3.8 (5) 環境錯誤子類補洞）：command 無法啟動等
		// runner-level error 不產 EvidenceRun（journal 未寫、上面的 result 分流
		// 走不到）——同 key 開 evidence-error 項，key 同構故 A8 的「同 key 新
		// run passed → resolveByKey」自然涵蓋解除。shutdown cancel
		// （ctx.Err()!=nil）不開項：那是 reclaim，不是環境錯誤。appendErr
		// （runErr==nil 但 journal 寫入失敗）也不在此開項——那屬 (8)
		// journal-degraded，由 reconcileLocked 的掃描補建。
		a.workflowMu.Lock()
		_, cerr := a.escCreateSystemLocked("evidence-error:"+planID+"/"+taskID+"/"+kind,
			"tca:"+planID+"/"+taskID, false,
			"evidence run（"+kind+"）啟動失敗："+runErr.Error(), "run:"+planID+"/"+taskID+"/"+kind)
		a.workflowMu.Unlock()
		if cerr != nil {
			finalErr = errors.Join(finalErr, cerr)
		}
	}

	payload := map[string]any{
		"phase": "finished", "evidence_id": evidenceID,
		"plan_id": planID, "task_id": taskID, "kind": kind, "gate2_approval_id": approvalID,
	}
	if finalErr != nil {
		payload["error"] = finalErr.Error()
	} else {
		payload["result"] = run.Result
	}
	a.manager.EmitWorkspace(evidenceRunEventKind, nil, payload)

	if finalErr != nil {
		return "", finalErr
	}
	return evidenceID, nil
}

// evidenceRunEventKind：RunEvidence 進度事件的 EmitWorkspace kind——additive
// only（每次呼叫各發一筆 started／finished，從不覆寫既有事件）。
const evidenceRunEventKind = "evidence_run"

// EvidenceGet 回傳 journal 內 evidenceID 對應的完整 EvidenceRun（含 journal
// 重播後重建的紀錄）。
func (a *App) EvidenceGet(evidenceID string) (evidence.EvidenceRun, error) {
	if a.evidenceJournal == nil {
		return evidence.EvidenceRun{}, errors.New("evidence: not initialized")
	}
	return a.evidenceJournal.Get(evidenceID)
}

// CommitInfo is a single candidate for TcaWorkspace's test_commit dropdown
// (Task 22): OID/Subject only — the UI shows a short OID + subject, the full
// value goes into ValidateTestCommit/RunEvidence untouched.
type CommitInfo struct {
	OID     string `json:"oid"`
	Subject string `json:"subject"`
}

// ValidateTestCommit is the UI's pre-flight lineage check (Task 22, §6/A4):
// before spending a worktree checkout + command run on RunEvidence, let
// TcaWorkspace surface a plan_commit..testCommit lineage error immediately.
// It reuses exactly the checks evidence.Run performs before ever touching a
// worktree — LoadAt (task must exist in the committed plan) and
// LoadOracleAt+plan.VerifyLineage (every path touched in that range must
// stay within the declared oracle surface) — against the same active Gate 2
// plan_commit RunEvidence trusts (activeGate2PlanCommit), never the
// caller-supplied testCommit's own ancestry. Validate only: no worktree, no
// command execution.
func (a *App) ValidateTestCommit(planID, taskID, testCommit string) error {
	entries, err := a.GateList()
	if err != nil {
		return err
	}
	planCommit, ok := activeGate2PlanCommit(entries, planID)
	if !ok {
		return fmt.Errorf("evidence: no active Gate 2 approval for plan %q", planID)
	}
	pl, _, err := a.planLoader.LoadAt(planCommit, planID)
	if err != nil {
		return err
	}
	found := false
	for _, t := range pl.Tasks {
		if t.ID == taskID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("evidence: task %q not found in plan %q at %s", taskID, planID, planCommit)
	}
	oracleDecl, err := a.planLoader.LoadOracleAt(planCommit)
	if err != nil {
		return err
	}
	return plan.VerifyLineage(a.planGit, planCommit, testCommit, oracleDecl.Match)
}

// EvidenceCommitCandidates lists the most recent commits after the active
// Gate 2 plan_commit (Task 22): the data source for TcaWorkspace's
// test_commit dropdown — `git log --format=%H%x00%s -n 20 <plan_commit>..HEAD`,
// newest first (git log's default order). Returns an empty (non-nil) slice
// when the range has no commits, never nil, so the frontend can render it
// without a null-check.
func (a *App) EvidenceCommitCandidates(planID string) ([]CommitInfo, error) {
	entries, err := a.GateList()
	if err != nil {
		return nil, err
	}
	planCommit, ok := activeGate2PlanCommit(entries, planID)
	if !ok {
		return nil, fmt.Errorf("evidence: no active Gate 2 approval for plan %q", planID)
	}
	out, err := a.planGit.Git("log", "--format=%H%x00%s", "-n", "20", planCommit+"..HEAD")
	if err != nil {
		return nil, err
	}
	return parseCommitCandidates(out), nil
}

// parseCommitCandidates parses `git log --format=%H%x00%s` output (one
// "<oid>\x00<subject>" record per line, newline-delimited — git log's
// default record separator, unlike the -z NUL-delimited format
// plan.VerifyLineage's diff parsing needs).
func parseCommitCandidates(out []byte) []CommitInfo {
	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return []CommitInfo{}
	}
	lines := strings.Split(trimmed, "\n")
	result := make([]CommitInfo, 0, len(lines))
	for _, ln := range lines {
		parts := strings.SplitN(ln, "\x00", 2)
		ci := CommitInfo{OID: parts[0]}
		if len(parts) > 1 {
			ci.Subject = parts[1]
		}
		result = append(result, ci)
	}
	return result
}

// tcaBindings assembles the six §3.4 required test_contract_approval
// bindings (Task 21). gate2ApprovalID/gate2RecordDigest/gate2BaseCommitDigest
// anchor this contract to the specific gate2 ApprovalRecord it was decided
// under (§3.0); the evidence_run/mutation digests are read straight from
// already-computed journal/CAS values — SubmitTestContract never re-derives
// or pre-validates them itself, that is entirely TCAPolicy's job.
func tcaBindings(gate2ApprovalID, gate2RecordDigest, gate2BaseCommitDigest string,
	testCommit, oracleSurfaceDigest string,
	redEvidenceID, redDigest, negEvidenceID, negDigest, mutationID, mutationDigest string) []gate.Binding {
	return []gate.Binding{
		{Kind: "gate2_approval", Ref: "approval:" + gate2ApprovalID, Digest: gate2RecordDigest},
		{Kind: "base_commit", Ref: "plan_commit", Digest: gate2BaseCommitDigest},
		{Kind: "oracle_surface", Ref: testCommit, Digest: oracleSurfaceDigest},
		{Kind: "evidence_run", Role: "expected_red", Ref: redEvidenceID, Digest: redDigest},
		{Kind: "evidence_run", Role: "negative_control", Ref: negEvidenceID, Digest: negDigest},
		{Kind: "mutation", Ref: mutationID, Digest: mutationDigest},
	}
}

// SubmitTestContract 送出 TCA（test_contract_approval）核可申請（Task 21，
// §3.4）：讀 active gate2（subject="plan:"+planID）取得其完整 ApprovalRecord
// ——gate2_approval binding 的 ref/digest 來源，base_commit binding 原樣複製
// 其 base_commit（§3.0 錨定，TCAPolicy.BuildDecision 另外覆核兩者相符）；再讀
// expectedRedID／negativeControlID 兩筆 EvidenceRun（EvidenceRunDigest 現算）
// 與 mutationID 的 Mutation（digest 直接取 CAS digest），組六筆 bindings 後
// Submit——bindings 本身是否彼此一致（role/kind、兩筆 passed、descriptor 等）
// 全交給 TCAPolicy.ValidateRequest／BuildDecision，這裡只負責組裝已有的值。
func (a *App) SubmitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID string) (string, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return "", err
	}
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}

	entries, err := a.GateList()
	if err != nil {
		return "", err
	}
	gate2ApprovalID, ok := activeGate2ApprovalID(entries, planID)
	if !ok {
		return "", fmt.Errorf("evidence: no active Gate 2 approval for plan %q", planID)
	}
	gate2Rec, _, err := svc.Lookup(gate2ApprovalID)
	if err != nil {
		return "", err
	}
	if gate2Rec == nil {
		return "", fmt.Errorf("gate: gate2 approval %q has no record", gate2ApprovalID)
	}
	gate2RecordDigest, err := gate.RecordDigest(*gate2Rec)
	if err != nil {
		return "", err
	}
	gate2BaseCommitDigest := bindingDigest(gate2Rec.Bindings, "base_commit")

	redRun, err := a.evidenceJournal.Get(expectedRedID)
	if err != nil {
		return "", fmt.Errorf("evidence: load expected_red evidence %q: %w", expectedRedID, err)
	}
	redDigest, err := evidence.EvidenceRunDigest(redRun)
	if err != nil {
		return "", err
	}
	negRun, err := a.evidenceJournal.Get(negativeControlID)
	if err != nil {
		return "", fmt.Errorf("evidence: load negative_control evidence %q: %w", negativeControlID, err)
	}
	negDigest, err := evidence.EvidenceRunDigest(negRun)
	if err != nil {
		return "", err
	}
	m, err := a.evidenceJournal.GetMutation(mutationID)
	if err != nil {
		return "", fmt.Errorf("evidence: load mutation %q: %w", mutationID, err)
	}

	bindings := tcaBindings(gate2ApprovalID, gate2RecordDigest, gate2BaseCommitDigest,
		testCommit, redRun.OracleSurfaceDigest,
		expectedRedID, redDigest, negativeControlID, negDigest, mutationID, m.Digest)
	return a.submitGateRequest(svc, "test_contract_approval", "task:"+planID+"/"+taskID, bindings)
}

// reclaimEvidenceRuns：shutdown 對 in-flight RunEvidence 的收束——cancel 每個
// active run 的 context（鏡射 reclaimAssists，task-20-brief.md 凍結順序：必須
// 早於 inflight.Wait，RunEvidence 持 txn，否則長時 runner 會讓 Wait 死等）。
// runner 的 ctx cancel 路徑（evidence.Run→runCommand）負責收拾 process group
// 與 worktree；下次啟動的 CleanupOrphans／CleanOrphanTemps 兜底任何未收乾淨
// 的殘留（見 startupEvidence）。
func (a *App) reclaimEvidenceRuns() {
	a.evidenceMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(a.evidenceActive))
	for _, c := range a.evidenceActive {
		cancels = append(cancels, c)
	}
	a.evidenceMu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// GateDecisionTaskDTO：GateDecisionContext 的單一 task risk 列，供 Gate 2
// 卡片渲染 risk decision 輸入介面。
type GateDecisionTaskDTO struct {
	TaskID          string `json:"task_id"`
	Title           string `json:"title"`
	MinimumRiskTier string `json:"minimum_risk_tier"`
	PlannerRiskTier string `json:"planner_risk_tier"`
}

type GateDecisionContextDTO struct {
	Tasks []GateDecisionTaskDTO `json:"tasks"`
}

// GateDecisionContext 回傳 approvalID（gate2 的 pending request 或 approved
// record）所綁 committed plan 之 task risk 列。一律從該筆的 base_commit
// （plan_commit）binding 用 PlanLoader.LoadAt 讀 committed plan——絕不讀
// worktree：送核後修改 worktree plan 不得改變這裡的回傳值（committed 才是
// 核可對象），前端不得以目前 worktree plan 推導 minimum／planner。
func (a *App) GateDecisionContext(approvalID string) (GateDecisionContextDTO, error) {
	entries, err := a.GateList()
	if err != nil {
		return GateDecisionContextDTO{}, err
	}
	var found *GateEntryDTO
	for i := range entries {
		if entries[i].ApprovalID == approvalID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		return GateDecisionContextDTO{}, fmt.Errorf("gate: approval id %q not found", approvalID)
	}
	planID, ok := planIDFromSubject(found.Subject)
	if !ok {
		return GateDecisionContextDTO{}, fmt.Errorf("gate: approval %q is not a gate2 plan approval (subject %q)", approvalID, found.Subject)
	}
	planCommit := gitOIDFromDigest(bindingDigest(found.Bindings, "base_commit"))
	if planCommit == "" {
		return GateDecisionContextDTO{}, fmt.Errorf("gate: approval %q missing base_commit binding", approvalID)
	}
	pl, _, err := a.planLoader.LoadAt(planCommit, planID)
	if err != nil {
		return GateDecisionContextDTO{}, err
	}
	tasks := make([]GateDecisionTaskDTO, 0, len(pl.Tasks))
	for _, t := range pl.Tasks {
		tasks = append(tasks, GateDecisionTaskDTO{TaskID: t.ID, Title: t.Title,
			MinimumRiskTier: t.MinimumRiskTier, PlannerRiskTier: t.PlannerRiskTier})
	}
	return GateDecisionContextDTO{Tasks: tasks}, nil
}

// GateEntryDTO：GateList 的 JSON-friendly projection（前端消費）。
type GateEntryDTO struct {
	ApprovalID         string         `json:"approval_id"`
	State              string         `json:"state"`
	Gate               string         `json:"gate,omitempty"`
	Subject            string         `json:"subject,omitempty"` // Task 12：gate2 為 "plan:<plan_id>"（v1 gate1 請求無此欄位）
	SpecManifestDigest string         `json:"spec_manifest_digest,omitempty"`
	BaseCommit         string         `json:"base_commit,omitempty"`
	CreatedAt          string         `json:"created_at,omitempty"`
	Bindings           []gate.Binding `json:"bindings,omitempty"`
	Decision           string         `json:"decision,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	Approver           *gate.Approver `json:"approver,omitempty"`
	JournalDegraded    bool           `json:"journal_degraded,omitempty"`
}

// GateList 回傳 Gate 1 projection。Service.List 內部先 Reconcile 才
// Project——projection 永不信任快取的 active（spec §4 權威層）。journal 進入
// degraded（append 失敗過）時每筆都標示，供 UI 提示「核可仍可讀但暫不可寫」。
func (a *App) GateList() ([]GateEntryDTO, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return nil, err
	}
	entries, err := svc.List()
	if err != nil {
		return nil, err
	}
	degraded := a.gateJournal != nil && a.gateJournal.Degraded()
	out := make([]GateEntryDTO, 0, len(entries))
	for _, e := range entries {
		dto := GateEntryDTO{ApprovalID: e.ApprovalID, State: string(e.State), JournalDegraded: degraded}
		if e.Request != nil {
			dto.Gate = e.Request.Gate
			dto.Subject = e.Request.Subject
			dto.SpecManifestDigest = e.Request.SpecManifestDigest
			dto.BaseCommit = e.Request.BaseCommit
			dto.CreatedAt = e.Request.CreatedAt
			dto.Bindings = e.Request.Bindings // v2 request carries bindings even while still pending
		}
		if e.Record != nil {
			if dto.Gate == "" {
				dto.Gate = e.Record.Gate
			}
			if dto.Subject == "" {
				dto.Subject = e.Record.Subject
			}
			dto.Bindings = e.Record.Bindings
			dto.Decision = e.Record.Decision
			dto.Reason = e.Record.Reason
			approver := e.Record.Approver
			dto.Approver = &approver
		}
		out = append(out, dto)
	}
	return out, nil
}

// gitConfigValue：`git -C workspace config <key>`；missing key → ""（不是錯誤，
// 由呼叫端判斷是否視為身分缺失）。
func (a *App) gitConfigValue(key string) string {
	out, err := exec.Command("git", "-C", a.workspaceDir, "config", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitIdentity：approver 身分來源，預設查 git config；可測試覆寫
// （gitIdentityOverride）以避免依賴執行機器的全域 git 設定。
func (a *App) gitIdentity() (name, email string, err error) {
	if a.gitIdentityOverride != nil {
		return a.gitIdentityOverride()
	}
	return a.gitConfigValue("user.name"), a.gitConfigValue("user.email"), nil
}

// GateDecide 對 pending approval 記錄核可／駁回決議。approver 一律取 git
// identity——name／email 皆缺一律拒絕，不生成假 approver ID（spec §5.4）。
// 核可時的 bindings 由 Service 內部從 pending request 複製（不在 decide 當下
// 重掃 worktree——避免決議內容漂移到「核可時」而非「申請時」的快照，也不要求
// decide 當下 worktree 仍乾淨），並以 policy 的 current-binding validation
// 擋掉待核期間已過期的請求（§3.1）。riskSelections 供 gate2 用；gate1 傳空即可。
//
// Task 24（spec §3.10 凍結順序，app 層編排）：整段在 workflowMu 下執行——
// reconcile → validator（PrepareDecision）→ [approved 時 2b：stale 修復解除]
// → blocking escalation 檢查 → append（CommitDecision）。blocker 只能在
// workflowMu 之外排隊，不存在「檢查後、append 前」被插入的窗口。
func (a *App) GateDecide(approvalID, decision, reason string, riskSelections []gate.RiskSelection) error {
	svc, err := a.ensureGate()
	if err != nil {
		return err
	}
	name, email, err := a.gitIdentity()
	if err != nil {
		return err
	}
	id := name
	if id == "" {
		id = email
	}
	if id == "" {
		return errors.New("gate: git identity not configured — set git config user.name (or user.email) before approving")
	}
	approver := gate.Approver{ID: id, Method: "app-local"}

	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if err := a.reconcileLocked(svc); err != nil { // 1. reconcile bindings（含 §3.8 stale／journal-degraded 補建）
		return err
	}
	prepared, err := svc.PrepareDecision(approvalID, decision, reason, approver,
		gate.DecisionInput{RiskSelections: riskSelections}) // 2. 硬性 validator＋approved 的 current-binding validation
	if err != nil {
		return err
	}
	scope := scopeForSubject(prepared.Record.Gate, prepared.Record.Subject)
	if prepared.Record.Decision == "approved" { // 2b. 修復解除（凍結時點）：
		// current-binding validation 已通過 ＝ 同 subject 的 stale 條件已被此修正版
		// 修復；在 blocker 檢查前系統解除舊 stale blocker（stale record 本身是終態，
		// 修復載體是修正版的核可流程）。resolve 寫入失敗 → 拒絕核可（fail closed，
		// §3.10：escalation journal 寫不進去時 Gate 不得放行）。
		key := "stale:" + prepared.Record.Gate + ":" + prepared.Record.Subject
		if rerr := a.escResolveByKeyLocked(key, "superseded-by:"+prepared.Record.ApprovalID); rerr != nil {
			return rerr
		}
	}
	items, berr := a.escBlockingForLocked(scope) // 3. blocking escalation（Project 失敗＝收件匣不可用，一樣拒——不裝空）
	if berr != nil {
		return berr
	}
	if len(items) > 0 {
		return fmt.Errorf("blocked by %d escalation item(s): %s", len(items), summarizeEscalations(items))
	}
	if h := a.decideBarrierHook; h != nil { // 測試 seam：blocker 檢查後、append 前
		h()
	}
	return svc.CommitDecision(prepared) // 4. append
}

// ---- Escalation inbox（Task 24：spec §3.8／§3.10）----

// ensureEscalation 惰性初始化 escalation.Service，journal 落在 workspace 的
// .workbench/escalation.jsonl（同 ensureGate 之於 gate.jsonl——綁 workspace，
// 不隨測試 stateDir 漂移）。
func (a *App) ensureEscalation() (*escalation.Service, error) {
	a.escOnce.Do(func() {
		root, err := claude.NormalizeCWD(a.workspaceDir)
		if err != nil {
			a.escInitErr = err
			return
		}
		wbDir := filepath.Join(root, ".workbench")
		if merr := os.MkdirAll(wbDir, 0o755); merr != nil {
			a.escInitErr = merr
			return
		}
		j, jerr := escalation.OpenJournal(filepath.Join(wbDir, "escalation.jsonl"))
		if jerr != nil {
			a.escInitErr = jerr
			return
		}
		a.escJournal = j
		a.escSvc = escalation.NewService(j,
			func() string { return contract.NewULID(time.Now()) },
			func() string { return time.Now().UTC().Format(time.RFC3339Nano) })
	})
	return a.escSvc, a.escInitErr
}

// scopeForSubject 把 (gate, subject) 映射到 escalation block scope（§3.8）：
// gate1→"workspace"、gate2 "plan:<id>"→"gate2:<id>"、tca "task:<p>/<t>"→
// "tca:<p>/<t>"。未知 gate 一律映到 "workspace"（最寬 scope，fail closed）。
func scopeForSubject(gateName, subject string) string {
	switch gateName {
	case "gate1":
		return "workspace"
	case "gate2":
		if id, ok := planIDFromSubject(subject); ok {
			return "gate2:" + id
		}
		return "gate2:" + subject
	case "test_contract_approval":
		if rest, ok := strings.CutPrefix(subject, "task:"); ok {
			return "tca:" + rest
		}
		return "tca:" + subject
	default:
		return "workspace"
	}
}

// summarizeEscalations：blocker 拒絕訊息的項目摘要（key＋summary；手動項無
// key 用 escalation_id）。
func summarizeEscalations(items []escalation.Entry) string {
	parts := make([]string, 0, len(items))
	for _, e := range items {
		label := e.Item.ConditionKey
		if label == "" {
			label = e.Item.EscalationID
		}
		parts = append(parts, label+"（"+e.Item.Summary+"）")
	}
	return strings.Join(parts, "; ")
}

// esc*Locked：已持有 workflowMu 的路徑專用（重入規約見 workflowMu 欄位 doc）。

func (a *App) escCreateSystemLocked(conditionKey, blockScope string, hard bool, summary, sourceRef string) (string, error) {
	svc, err := a.ensureEscalation()
	if err != nil {
		return "", err
	}
	return svc.CreateSystem(conditionKey, blockScope, hard, summary, sourceRef)
}

func (a *App) escResolveByKeyLocked(conditionKey, evidenceRef string) error {
	svc, err := a.ensureEscalation()
	if err != nil {
		return err
	}
	return svc.ResolveByKey(conditionKey, evidenceRef)
}

// escBlockingForLocked 回傳覆蓋 scope 的未 resolved blocking 項。escalation
// 初始化失敗或 Project 失敗都回錯——收件匣不可用時 Gate 決議 fail closed，
// 絕不把「讀不到」當成「沒有 blocker」（§3.8）。
func (a *App) escBlockingForLocked(scope string) ([]escalation.Entry, error) {
	svc, err := a.ensureEscalation()
	if err != nil {
		return nil, err
	}
	entries, err := svc.Entries()
	if err != nil {
		return nil, err
	}
	return escalation.BlockingFor(entries, scope), nil
}

// reconcileLocked（呼叫端持 workflowMu）：svc.List()（= Reconcile＋Project）
// 之後依權威 projection 補建 §3.8 自動來源——
//
//	(3)(4) stale：State==Stale 且同 (gate,subject) 沒有 Active 修正版核可的
//	  項目 → "stale:<gate>:<subject>"（hard=true，scope 依 scopeForSubject）。
//	  同 key 去重由 escalation.Service.CreateSystem 保證，重複掃描冪等；已被
//	  修正版核可（同 subject 有 Active）者不補建——其修復時點凍結在 GateDecide
//	  2b，補建在這裡再開會讓已修復的 blocker 復活。
//	(8) journal degraded：gate／evidence journal 已 degraded → workspace hard
//	  項；journal 健康（例如重啟後重開成功）→ 系統解除同 key（修復條件）。
//	  escalation journal 自身 degraded 無法寫入自己的 journal-degraded 項——
//	  它的 fail-closed 由 esc* 呼叫端回錯（Gate 拒核）承擔。
//
// 接線點選擇（brief 給兩案：比對 Reconcile 前後 projection vs. 掛
// EmitGateEvent binding_stale hook）：選權威狀態掃描——binding_stale 事件在
// gate.Service 持內部 mu 時發出，hook 內回讀 gate（Lookup 補 gate/subject）
// 必死鎖，且事件是通知層、掃描才符合「啟動／讀取補建」的冪等語意。
func (a *App) reconcileLocked(svc *gate.Service) error {
	entries, err := svc.List()
	if err != nil {
		return err
	}
	// (8) journal-degraded 補建／修復
	if a.gateJournal != nil {
		if err := a.escJournalDegradedLocked("gate", a.gateJournal.Degraded(), ".workbench/gate.jsonl"); err != nil {
			return err
		}
	}
	if a.evidenceJournal != nil {
		if err := a.escJournalDegradedLocked("evidence", a.evidenceJournal.Degraded(), ".workbench/evidence/evidence.jsonl"); err != nil {
			return err
		}
	}
	// (3)(4) stale 補建
	active := map[string]bool{}
	for _, e := range entries {
		if e.State == gate.Active && e.Record != nil {
			active[e.Record.Gate+":"+e.Record.Subject] = true
		}
	}
	for _, e := range entries {
		if e.State != gate.Stale || e.Record == nil {
			continue
		}
		gs := e.Record.Gate + ":" + e.Record.Subject
		if active[gs] {
			continue
		}
		if _, cerr := a.escCreateSystemLocked("stale:"+gs, scopeForSubject(e.Record.Gate, e.Record.Subject), true,
			"核可 "+e.ApprovalID+"（"+e.Record.Gate+" "+e.Record.Subject+"）的綁定已 stale——"+
				"修正後必須建立修正版並重新送核；還原檔案內容不會讓舊核可恢復生效",
			e.ApprovalID); cerr != nil {
			return cerr
		}
	}
	return nil
}

// escJournalDegradedLocked：§3.8 (8) 單一 journal 的補建／修復（呼叫端持
// workflowMu）。degraded → workspace hard 項；健康 → 系統解除同 key（no-op
// 若本無未 resolved 項）。
func (a *App) escJournalDegradedLocked(which string, degraded bool, ref string) error {
	key := "journal-degraded:" + which
	if degraded {
		_, err := a.escCreateSystemLocked(key, "workspace", true,
			which+" journal 寫入已 degraded——重啟修復前拒絕新核可", ref)
		return err
	}
	return a.escResolveByKeyLocked(key, "journal-reopened")
}

// wireEvidenceEscalation：§3.8 (5)(6)(7) 的 evidence finalize 接線＋A8 修復。
// 呼叫端不得持 evidenceMu 或 workflowMu（lock ordering：finalize 臨界區結束
// 後才進來，evidenceMu 與 workflowMu 不巢狀）。key 綁 plan/task/kind 而非
// evidence_id——新 run 成功即可 ResolveByKey 舊項（A8）。
func (a *App) wireEvidenceEscalation(planID, taskID, kind, evidenceID, result string) error {
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	scope := "tca:" + planID + "/" + taskID
	errKey := "evidence-error:" + planID + "/" + taskID + "/" + kind
	ncKey := "negative-control-missed:" + planID + "/" + taskID
	switch result {
	case "passed": // A8：同 key 新 run 通過＝權威修復，系統解除
		if err := a.escResolveByKeyLocked(errKey, "passed:"+evidenceID); err != nil {
			return err
		}
		if kind == "negative_control" {
			return a.escResolveByKeyLocked(ncKey, "passed:"+evidenceID)
		}
		return nil
	case "error": // (5)(6)：runner 逾時／環境錯誤／輸出超限／expected-red error 原因
		_, err := a.escCreateSystemLocked(errKey, scope, false,
			"evidence run "+evidenceID+"（"+kind+"）結果為 error", "evidence:"+evidenceID)
		return err
	case "failed":
		if kind == "negative_control" { // (7)：negative control 未抓到 mutation
			_, err := a.escCreateSystemLocked(ncKey, scope, false,
				"negative control run "+evidenceID+" 未抓到 mutation（result=failed）", "evidence:"+evidenceID)
			return err
		}
		return nil
	default:
		return nil
	}
}

// submitGateRequest：svc.Submit 的 §3.8 (2) 接線包裝——系統組裝的送核請求先
// 過該 gate policy 的 ValidateRequest：失敗即開 "missing-binding:<gate>:<subject>"
// 項（同 key 去重）；通過且 Submit 成功即系統解除同 key（同 subject 新 request
// 通過驗證＝(2) 的權威修復條件）。Submit 內部會再驗一次同一 policy——重複驗證
// 是刻意的：app 層需要把「ValidateRequest 失敗」從其他 Submit 錯誤（journal
// 寫入失敗等）中區分出來，而不擴大 gate.Service 的介面。
func (a *App) submitGateRequest(svc *gate.Service, gateName, subject string, bindings []gate.Binding) (string, error) {
	key := "missing-binding:" + gateName + ":" + subject
	if policy, ok := a.gateReg[gateName]; ok {
		req := gate.GateRequest{Type: "gate_request", SchemaVersion: 2, Gate: gateName,
			Subject: subject, Bindings: bindings}
		if verr := policy.ValidateRequest(req); verr != nil {
			a.workflowMu.Lock()
			_, cerr := a.escCreateSystemLocked(key, scopeForSubject(gateName, subject), true,
				"系統組裝的送核請求缺必要 binding："+verr.Error(), subject)
			a.workflowMu.Unlock()
			if cerr != nil {
				return "", errors.Join(verr, cerr)
			}
			return "", verr
		}
	}
	id, err := svc.Submit(gateName, subject, bindings)
	if err != nil {
		return "", err
	}
	a.workflowMu.Lock()
	rerr := a.escResolveByKeyLocked(key, "request:"+id)
	a.workflowMu.Unlock()
	if rerr != nil { // 已送出但修復解除寫不進去：fail loud（不無聲留下已修復的 blocker）
		return "", rerr
	}
	return id, nil
}

// planner enforcement condition keys（§3.8 (9)；spec §3.4 erratum，owner
// 2026-08-14 裁決 key 分離）：
//   - preflight key：spawn 前靜態 preflight 驗不過 → 建立；同 provider 的
//     preflight 重新通過（runner 啟動前，時點不變）→ 系統解除。
//   - runtime key：runner 回 typed *assist.EnforcementViolation（Codex
//     runtime 違規）→ 建立；**只有一次完整 PlanAssist 成功結束（runner.Run
//     回 nil、全程無 violation）才系統解除**。一般錯誤／逾時／取消／
//     escalation 寫入失敗皆不解除。
//
// 分離理由：共用 key 時「上次已實證 runtime 違規」的 workspace blocker 會在
// 下次 PlanAssist 僅通過靜態 preflight、runner 尚未證明安全的窗口內被提前
// 解除，Gate decision 可能於該窗口通過——靜態 preflight 證明不了 runtime
// 行為已恢復。
func plannerPreflightKey(provider string) string {
	return "planner-enforcement-preflight:" + provider
}

func plannerRuntimeKey(provider string) string {
	return "planner-enforcement-runtime:" + provider
}

// escPlannerPreflightFailedLocked／escPlannerRuntimeViolationLocked：(9) 的
// 建立函式（呼叫端持 workflowMu）。皆 hard=true：安全不變量缺口僅系統可依
// 各自修復條件解除，UI 不可手動 resolve。
func (a *App) escPlannerPreflightFailedLocked(provider, detail string) (string, error) {
	return a.escCreateSystemLocked(plannerPreflightKey(provider), "workspace", true,
		"PlannerAssist preflight 失敗（"+provider+"）："+detail, "provider:"+provider)
}

func (a *App) escPlannerRuntimeViolationLocked(provider, detail string) (string, error) {
	return a.escCreateSystemLocked(plannerRuntimeKey(provider), "workspace", true,
		"PlannerAssist runtime enforcement 違規（"+provider+"）："+detail, "provider:"+provider)
}

// EscalationList 回傳收件匣 projection（Wails 綁定）。Project 失敗回錯——
// 收件匣標不可用，絕不裝空（§3.8）。
func (a *App) EscalationList() ([]escalation.Entry, error) {
	svc, err := a.ensureEscalation()
	if err != nil {
		return nil, err
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	return svc.Entries()
}

// EscalationCreate 建立手動 escalation 項（Wails 綁定；sourceRef 必填，
// blockScope 空字串＝非阻擋資訊項）。
func (a *App) EscalationCreate(sourceRef, blockScope, summary string) (string, error) {
	svc, err := a.ensureEscalation()
	if err != nil {
		return "", err
	}
	if h := a.onWorkflowMuAttempt; h != nil { // 測試 seam：Lock() 前
		h()
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if h := a.onWorkflowMuAcquired; h != nil { // 測試 seam：取得 mutex 後、寫入前
		h()
	}
	return svc.CreateManual(sourceRef, blockScope, summary)
}

// EscalationAck 標記已認知（不解除 block，§3.8）。
func (a *App) EscalationAck(id string) error {
	svc, err := a.ensureEscalation()
	if err != nil {
		return err
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	return svc.Ack(id)
}

// EscalationResolve 手動 resolve（Wails 綁定）。actor 一律取 git identity
// （同 GateDecide 的 approver 來源）；hard 項由 Service 拒絕（僅系統可 resolve）。
func (a *App) EscalationResolve(id, resolution, reason string) error {
	svc, err := a.ensureEscalation()
	if err != nil {
		return err
	}
	name, email, err := a.gitIdentity()
	if err != nil {
		return err
	}
	actor := name
	if actor == "" {
		actor = email
	}
	if actor == "" {
		return errors.New("escalation: git identity not configured — set git config user.name (or user.email) before resolving")
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	return svc.Resolve(id, resolution, reason, actor)
}

// ---- SpecAssist（Task 11：隔離 one-shot 草擬＋lifecycle；Stage A §5.1／§8-risk-1）----

// SpecAssist 以隔離的一次性 AI 呼叫草擬 spec 內容，帶 provider 端強制的零 workspace
// 變更保證（Claude `--tools ""`／Codex readOnly+never，見 internal/assist）。
//
// lifecycle 不變量：
//   - 獨佔性：每個 provider 至多一個 active；第二個併發請求回 ErrAssistActive。
//   - 交易閘：beginAppTxn 於啟動（shutdown 後拒新），endAppTxn 於收尾一次。
//   - shutdown reclaim：shutdown cancel in-flight one-shot、等其收尾（endAppTxn）
//     後才 Manager.Close（reclaimAssists＋inflight.Wait）。
//   - ownership 隔離：不碰 sessionHosts／a.codexConn（assist runner 為獨立 process）；
//     晚到舊 generation 事件（correlation 不符）丟棄並發 stream_error（fail loud）。
//   - once/token 收尾：result／abort／timeout／shutdown 任一先觸發即收一次。
//
// 事件經 Manager.EmitAssist 出口（scope=session、provider、correlation_id、
// purpose="spec_assist"）——保留稽核＋檔案級 event_id，但**不進 provider slot**
// （前端依 purpose 二次分流，不污染 reducer／Chat／totals）。
func (a *App) SpecAssist(provider, purpose, prompt string) (string, error) {
	if !knownProvider(provider) {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	// Pin the assist purpose at the emit boundary (defense in depth): this is the
	// isolated assist lane, so every emitted envelope MUST carry
	// purpose="spec_assist" regardless of the caller-supplied argument. Trusting
	// the caller would let a future caller passing "" or another value leak
	// assist (scope=session) events into the provider slot — restore.go's
	// replayViewWindow buckets by purpose, and EmitAssist has no purpose guard.
	purpose = "spec_assist"
	ctx, cancel := context.WithTimeout(a.ctx, assistTimeout)
	gen := &assistGen{correlationID: contract.NewULID(time.Now()), cancel: cancel, done: make(chan struct{})}

	// gen（含 cancel）必須早於 beginAppTxn 進 assistActive——shutdown 的 reclaim
	// 掃描此結構。若 txn 先登記進 inflight、gen 卻尚不可見，reclaim 會掃到空集合、
	// cancel 不到，inflight.Wait 卻等到 assistTimeout（~3min stall）。反轉順序後：
	// 任何會被 inflight.Wait 等到的 assist，其 gen 必已可被 reclaim cancel。
	a.assistMu.Lock()
	if _, exists := a.assistActive[provider]; exists { // 獨佔性：第二個併發請求被拒
		a.assistMu.Unlock()
		cancel()
		return "", ErrAssistActive
	}
	a.assistActive[provider] = gen
	a.assistMu.Unlock()

	if h := a.hookAssistBeforeTxn; h != nil { // 測試 barrier：gen 已可見、txn 未登記
		h()
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：拒新請求
		a.assistMu.Lock() // rollback：未取得交易閘，撤下 gen（reclaim 若已 cancel 亦冪等）
		if a.assistActive[provider] == gen {
			delete(a.assistActive, provider)
		}
		a.assistMu.Unlock()
		cancel()
		return "", err
	}

	// once/token 收尾：result／abort／timeout／shutdown 任一先觸發，恰好收一次。
	teardown := func() {
		gen.once.Do(func() {
			cancel()
			a.assistMu.Lock()
			if a.assistActive[provider] == gen {
				delete(a.assistActive, provider)
			}
			a.assistMu.Unlock()
			a.endAppTxn()
			close(gen.done)
		})
	}
	defer teardown()

	runner, err := a.newAssistRunner(provider)
	if err != nil {
		return gen.correlationID, err
	}

	prov := contract.Provider(provider)
	sink := func(env contract.Envelope) {
		a.assistMu.Lock()
		cur, ok := a.assistActive[provider]
		a.assistMu.Unlock()
		if !ok || cur != gen { // 晚到舊 generation：丟棄並 fail loud（不進 provider slot）
			a.manager.EmitAssist(prov, gen.correlationID, purpose,
				contract.Event{Provider: prov, Kind: contract.KindStreamError,
					Raw: []byte(`{"assist":"stale_generation_event_dropped"}`),
					Err: errors.New("assist: late event from stale generation dropped")})
			return
		}
		a.manager.EmitAssist(prov, gen.correlationID, purpose, assistEnvelopeToEvent(prov, env))
	}
	err = runner.Run(ctx, prompt, sink)
	return gen.correlationID, err
}

// newAssistRunner：production 造 provider 專屬隔離 one-shot Runner；測試以
// assistRunnerFactory 注入 fake。
func (a *App) newAssistRunner(provider string) (assist.Runner, error) {
	if a.assistRunnerFactory != nil {
		return a.assistRunnerFactory(provider)
	}
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	if provider == "claude" {
		return assist.NewClaudeAssist(a.claudeCLIPath(), cwd, a.childEnv()), nil
	}
	return assist.NewCodexAssist(a.codexCLIPath(), cwd, a.childEnv()), nil
}

// ---- PlanAssist（Task 11：PlannerAssist 唯讀探索 one-shot；spec §5）----

// nonPlanDirtyPaths 回傳 workspace 中「不在 plan/** 範圍內」的未提交變更路徑
// （staged／unstaged／untracked 皆算，--untracked-files=all 展開目錄到檔案級）。
// 空回傳表示除 plan/** 外整棵樹乾淨——PlannerAssist 唯讀分析的前置條件。
// .workbench/ 是 app state（gate journal 等），不屬受管 code，比照
// assertWorkspaceUnchanged（app_assist_test.go）同一慣例排除，不計入 dirty。
func (a *App) nonPlanDirtyPaths() ([]string, error) {
	out, err := exec.Command("git", "-C", a.workspaceDir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("assist: git status: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("assist: git status: %w", err)
	}
	var dirty []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		p := gitStatusPath(line)
		if p == "" || p == ".workbench" || strings.HasPrefix(p, ".workbench/") {
			continue
		}
		if !spec.PlanScope.Match(p) {
			dirty = append(dirty, p)
		}
	}
	return dirty, nil
}

// gitStatusPath 從 `git status --porcelain` 一行擷取路徑（"XY path" 或 rename
// 的 "XY old -> new" 取新路徑）。與 internal/spec/gitrepo.go 的私有 statusPath
// 邏輯相同——app 層無法引用該未匯出函式，此為此檔唯一需要解析 status 行之處，
// 複製比新增 spec 匯出面來得省事。
func gitStatusPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	rest := line[3:]
	if idx := strings.Index(rest, " -> "); idx >= 0 {
		rest = rest[idx+4:]
	}
	return strings.Trim(rest, "\"")
}

// activeGate1SpecManifestDigest 在 gate.List() 內找 gate1 的 active 項，回傳其
// spec_manifest binding digest；無 active gate1 項回 ok=false。
func activeGate1SpecManifestDigest(entries []GateEntryDTO) (digest string, ok bool) {
	for _, e := range entries {
		if e.Gate != "gate1" || e.State != string(gate.Active) {
			continue
		}
		return e.SpecManifestDigest, true
	}
	return "", false
}

// PlanAssist（Task 11：PlannerAssist 唯讀探索 one-shot；spec §5）以隔離的一次
// 性 AI 呼叫唯讀探索 workspace 並草擬 plan YAML。lifecycle 鏡射 SpecAssist
// （corr_id、EmitAssist、reclaim——見其 doc，此處不重複展開），差異僅：
//   - Runner 換唯讀白名單／sandbox（Claude ClaudePlannerArgs／Codex readOnly+
//     never 原樣，見 internal/assist）；
//   - purpose 固定 "plan_draft"（前端依 purpose 二次分流，不進 provider slot）；
//   - 呼叫 runner 前有兩項前置檢查，任一不符即 fail closed、完全不啟動
//     runner／不佔用 assist 獨佔性：
//     (a) 存在 active Gate 1（§5 輸入即其 spec_manifest digest；無則回錯）；
//     (b) workspace 除 plan/** 外無未提交變更（PlannerAssist 需在乾淨 code
//     tree 上分析——plan/** 本身允許 dirty，因為草稿要寫回這裡）。
//
// 通過後把 analysis_base_commit=HEAD（完整 OID）與 active Gate 1 的
// spec_manifest digest 明文注入 prompt 前綴：模型草擬的 plan YAML 之
// analysis_base_commit 欄位須等於此處注入的值（§9 契約，Task 12 的
// VerifyLineage 以此為 lineage 起點）。
func (a *App) PlanAssist(provider, prompt string) (string, error) {
	if !knownProvider(provider) {
		return "", fmt.Errorf("unknown provider %q", provider)
	}

	if _, err := a.ensureGate(); err != nil {
		return "", err
	}
	entries, err := a.GateList()
	if err != nil {
		return "", err
	}
	specManifestDigest, hasActiveGate1 := activeGate1SpecManifestDigest(entries)
	if !hasActiveGate1 {
		return "", errors.New("assist: 無生效規格核可——先完成 Gate 1")
	}

	dirty, err := a.nonPlanDirtyPaths()
	if err != nil {
		return "", err
	}
	if len(dirty) > 0 {
		return "", errors.New("assist: workspace 有未提交的非 plan 變更——PlannerAssist 需在乾淨 code tree 上分析")
	}

	// provider capability preflight（M3a.1 Task 7：spec §3.4）：spawn 前驗 pin
	// 版本＋argv 凍結基準。失敗 → fail closed：不啟動 runner、workflowMu 下建
	// hard preflight 項並回明確錯誤；escalation 寫入失敗仍不啟動且錯誤含
	// journal 失敗。重新通過 → 系統解除 preflight key（僅此 key——runtime
	// blocker 的修復條件是一次完整成功 run，見 plannerRuntimeKey doc）。
	pf, pferr := a.planPreflight(provider)
	if pferr != nil || !pf.OK {
		reason := pf.Reason
		if pferr != nil {
			reason = pferr.Error()
		}
		failErr := fmt.Errorf("%w：%s preflight 失敗：%s", assist.ErrEnforcementUnproven, provider, reason)
		a.workflowMu.Lock()
		_, cerr := a.escPlannerPreflightFailedLocked(provider, reason)
		a.workflowMu.Unlock()
		if cerr != nil {
			return "", errors.Join(failErr, cerr)
		}
		return "", failErr
	}
	a.workflowMu.Lock()
	rerr := a.escResolveByKeyLocked(plannerPreflightKey(provider), "preflight-pass:"+pf.BinaryDigest)
	a.workflowMu.Unlock()
	if rerr != nil { // 已重新通過但修復解除寫不進去：fail loud（不無聲留下已修復的 blocker）
		return "", rerr
	}

	headOID, err := a.specRepo.HeadCommit()
	if err != nil {
		return "", fmt.Errorf("assist: resolve HEAD: %w", err)
	}
	prompt = fmt.Sprintf(
		"analysis_base_commit=%s\nspec_manifest_digest=%s\n\n"+
			"以上為本次唯讀分析的基準 commit（analysis_base_commit）與目前生效 Gate 1 核可的"+
			" spec manifest digest；產出的 plan YAML 草稿其 analysis_base_commit 欄位須等於"+
			"上述 analysis_base_commit 值。\n\n%s",
		headOID, specManifestDigest, prompt)

	const purpose = "plan_draft"
	ctx, cancel := context.WithTimeout(a.ctx, assistTimeout)
	gen := &assistGen{correlationID: contract.NewULID(time.Now()), cancel: cancel, done: make(chan struct{})}

	// 同 SpecAssist：gen 先入 assistActive 才 beginAppTxn（shutdown reclaim 窗口
	// 不變量，見 SpecAssist doc）。共用同一張 a.assistActive／同一把 a.assistMu
	// ——每個 provider 至多一個 active（SpecAssist／PlanAssist 共用同一獨佔性
	// 與 reclaim 基礎設施，reclaimAssists 無需另行改動即涵蓋兩者）。
	a.assistMu.Lock()
	if _, exists := a.assistActive[provider]; exists {
		a.assistMu.Unlock()
		cancel()
		return "", ErrAssistActive
	}
	a.assistActive[provider] = gen
	a.assistMu.Unlock()

	if h := a.hookAssistBeforeTxn; h != nil {
		h()
	}
	if err := a.beginAppTxn(); err != nil {
		a.assistMu.Lock()
		if a.assistActive[provider] == gen {
			delete(a.assistActive, provider)
		}
		a.assistMu.Unlock()
		cancel()
		return "", err
	}

	teardown := func() {
		gen.once.Do(func() {
			cancel()
			a.assistMu.Lock()
			if a.assistActive[provider] == gen {
				delete(a.assistActive, provider)
			}
			a.assistMu.Unlock()
			a.endAppTxn()
			close(gen.done)
		})
	}
	defer teardown()

	runner, err := a.newPlanAssistRunner(provider)
	if err != nil {
		return gen.correlationID, err
	}

	prov := contract.Provider(provider)
	sink := func(env contract.Envelope) {
		a.assistMu.Lock()
		cur, ok := a.assistActive[provider]
		a.assistMu.Unlock()
		if !ok || cur != gen { // 晚到舊 generation：丟棄並 fail loud（不進 provider slot）
			a.manager.EmitAssist(prov, gen.correlationID, purpose,
				contract.Event{Provider: prov, Kind: contract.KindStreamError,
					Raw: []byte(`{"assist":"stale_generation_event_dropped"}`),
					Err: errors.New("assist: late event from stale generation dropped")})
			return
		}
		a.manager.EmitAssist(prov, gen.correlationID, purpose, assistEnvelopeToEvent(prov, env))
	}
	err = runner.Run(ctx, prompt, sink)
	// 誤分類禁止（§3.4）：preflight 通過後的 runner 啟動失敗／逾時是一般錯誤，
	// 不建 enforcement 項；只有 typed *EnforcementViolation（Codex 在
	// readOnly+never 下仍送 escalation/approval request）才建 runtime hard 項。
	var viol *assist.EnforcementViolation
	switch {
	case errors.As(err, &viol):
		a.workflowMu.Lock()
		_, cerr := a.escPlannerRuntimeViolationLocked(viol.Provider, viol.Detail)
		a.workflowMu.Unlock()
		if cerr != nil { // fail loud：違規發生但 journal 寫不進去，錯誤要帶出來
			err = errors.Join(err, cerr)
		}
	case err == nil:
		// runtime blocker 修復條件（spec §3.4 erratum 2026-08-14）：一次完整
		// PlanAssist 成功結束、全程無 violation——此刻才系統解除。一般錯誤／
		// 逾時／取消不會走到這裡（err != nil），blocker 續留。
		a.workflowMu.Lock()
		rerr := a.escResolveByKeyLocked(plannerRuntimeKey(provider), "clean-run:"+gen.correlationID)
		a.workflowMu.Unlock()
		if rerr != nil { // 已修復但解除寫不進去：fail loud（blocker 續留、錯誤帶出）
			err = rerr
		}
	}
	return gen.correlationID, err
}

// planPreflight（§3.4）：provider 的 PlanAssist spawn 前 capability preflight，
// 帶 digest-keyed 快取（見 preflightCache 欄位 doc）。cache miss 時 hash 會在
// Preflight* 內重算一次——只發生在 miss，維持單一實作比省一次 hash 值得。
func (a *App) planPreflight(provider string) (assist.PreflightResult, error) {
	bin := a.claudeCLIPath()
	if provider == "codex" {
		bin = a.codexCLIPath()
	}
	digest, err := assist.BinarySHA256(bin)
	if err != nil {
		return assist.PreflightResult{Provider: provider, BinaryPath: bin}, err
	}
	key := bin + "|" + digest
	a.preflightMu.Lock()
	cached, hit := a.preflightCache[key]
	a.preflightMu.Unlock()
	if hit {
		return cached, nil
	}
	var res assist.PreflightResult
	if provider == "claude" {
		res, err = assist.PreflightClaude(bin, assist.ClaudePlannerArgs())
	} else {
		res, err = assist.PreflightCodex(bin)
	}
	if err == nil && res.OK { // 只快取 OK：失敗每次重驗（恢復即刻可見）
		// key 用 res.BinaryDigest（Preflight* 驗證當下同一次 hash 的結果），
		// 不用上面 lookup 用的 digest——兩次 hash 之間 binary 若被抽換，存入
		// 的 key 仍與驗證結果自洽，抽換後的 binary 下次 lookup 必 miss 重驗。
		a.preflightMu.Lock()
		a.preflightCache[bin+"|"+res.BinaryDigest] = res
		a.preflightMu.Unlock()
	}
	return res, err
}

// newPlanAssistRunner：production 造 provider 專屬唯讀 PlannerAssist Runner；
// 測試沿用 assistRunnerFactory 注入 fake（同 newAssistRunner 的注入點——單一
// factory 欄位涵蓋 SpecAssist／PlanAssist 兩條 one-shot 路徑）。
func (a *App) newPlanAssistRunner(provider string) (assist.Runner, error) {
	if a.assistRunnerFactory != nil {
		return a.assistRunnerFactory(provider)
	}
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	if provider == "claude" {
		return assist.NewClaudePlanner(a.claudeCLIPath(), cwd, a.childEnv()), nil
	}
	return assist.NewCodexPlanner(a.codexCLIPath(), cwd, a.childEnv()), nil
}

// reclaimAssists：shutdown 對 in-flight SpecAssist 的收束——cancel 每個 active
// generation（runner 界限內退出 → teardown 清 active＋endAppTxn＋close done）。
// 必須早於 inflight.Wait（assist 持 txn，否則 Wait 死等）與 Manager.Close
// （稽核收尾在 sink 關閉前完成）。bounded 由 runner 尊重 ctx（proc TermGrace）保證。
func (a *App) reclaimAssists() {
	a.assistMu.Lock()
	gens := make([]*assistGen, 0, len(a.assistActive))
	for _, g := range a.assistActive {
		gens = append(gens, g)
	}
	a.assistMu.Unlock()
	for _, g := range gens {
		g.cancel()
	}
}

// assistEnvelopeToEvent：Runner 送出的 envelope → Event（EmitAssist 於出口重蓋
// 檔案級 event_id／ts／scope／correlation／purpose）。rich 內容原樣保留。
func assistEnvelopeToEvent(prov contract.Provider, env contract.Envelope) contract.Event {
	ev := contract.Event{
		Provider:  prov,
		Kind:      contract.Kind(env.Kind),
		SessionID: env.SessionID,
		Role:      env.Role,
		Text:      env.Text,
		Thinking:  env.Thinking,
		IsError:   env.IsError,
		CostUSD:   env.CostUSD,
		Usage:     env.Usage,
		Raw:       []byte(env.Raw),
	}
	if env.Error != "" {
		ev.Err = errors.New(env.Error)
	}
	if len(ev.Raw) == 0 { // Wrap 對空 Raw 無妨，但保守給合法 JSON
		ev.Raw = []byte(`{}`)
	}
	return ev
}

// ---- approvals（雙 provider 共用 UI 流；envelope 一律經 Manager）----

func (a *App) registerApproval(id string, w appcore.WSID, provider string, resolve func(bool, string) error) {
	a.apprMu.Lock()
	a.apprPending[id] = &pendingApproval{wsid: w, provider: provider, resolve: resolve}
	a.apprMu.Unlock()
}

// pendingByID：唯讀查詢 pending approval（不移除；ResolveApproval 才是消費端）。
func (a *App) pendingByID(id string) *pendingApproval {
	a.apprMu.Lock()
	defer a.apprMu.Unlock()
	return a.apprPending[id]
}

func (a *App) ResolveApproval(id string, allow bool, reason string) error {
	a.apprMu.Lock()
	p, ok := a.apprPending[id]
	delete(a.apprPending, id)
	a.apprMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending approval %s (timed out?)", id)
	}
	err := p.resolve(allow, reason)
	// 廣播 dismiss：dev 模式（原生視窗＋browser devserver）或多視窗下，
	// 未按下按鈕的前端也要收掉彈窗
	a.emit("approval:dismiss", map[string]any{"id": id, "cause": "resolved"})
	return err
}

// pumpApprovals：把某個 session 的 broker pending queue 轉成 UI 請求＋
// pendingApproval 登記（M3b §3.3：每個 session 各自一份 broker／socket，pump 因此
// 綁單一 session 而非 provider）。approval 事件一律發回該 session 的 WSID slot。
//
// 刻意收窄值而非整個 *sessionHost（review Important #2）：這條 goroutine 在 host
// publish 之前就啟動，拿到 host 指標就有機會讀到尚未寫入的欄位。sessionID 是由
// startClaude 提供、內部走 a.mu 的存取器。
func (a *App) pumpApprovals(w appcore.WSID, provider contract.Provider,
	br *approval.Broker, sessionID func() string) {
	for req := range br.Pending() {
		id := req.ID
		a.registerApproval(id, w, string(provider), func(allow bool, reason string) error {
			behavior := "deny"
			decision := "deny"
			if allow {
				behavior, decision = "allow", "allow"
			}
			err := br.Resolve(id, approval.Decision{Behavior: behavior, Message: reason})
			a.noteWSEmitError("approval_decision", w,
				a.manager.EmitApprovalDecision(w, sessionID(), decision, reason))
			return err
		})
		a.noteWSEmitError("approval_request", w,
			a.manager.EmitApprovalRequest(w, sessionID(), req.ToolName, req.Input))
		a.emit("approval:request", map[string]any{
			"id": id, "provider": string(provider), "toolName": req.ToolName,
			"inputJson": string(req.Input),
		})
	}
}

// ---- session 綁定 ----

// StartSession：單一 ownership 交易——BeginNewSessionSubmit 先佔（輸家在建立任何
// process／recorder／pump 之前就失敗）→ provider 同步啟動 → Accept／Reject。
func (a *App) StartSession(provider, prompt, resume, recordCase, taskLabel, approvalPolicy string) error {
	if !knownProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	prov := contract.Provider(provider)
	if resume == "" { // 第三輪 P1-3：view 未被 New 清除時自動接續（plan D6 resume 意圖）
		resume = a.restore.Get(provider).ResumeSessionID
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：拒新 Start
		return err
	}
	defer a.endAppTxn()
	// 兩個 provider 都已遷入 sessionHost（§3.3）：整段 start 交易一律 WSID 定址，
	// WSID 由 legacyWSIDFor 解析（Task 26 前端改為直接帶 WSID 後刪除該層）。
	w := a.legacyWSIDFor(prov)
	id, err := a.manager.BeginNewSessionSubmit(w, taskLabel)
	if err != nil {
		return err // ErrSessionActive／ErrSubmitActive 原樣回 UI
	}
	if h := a.hookBeforeProviderStart; h != nil { // 測試 barrier：ownership 已取得、provider 未啟動
		h()
	}
	switch prov {
	case contract.ProviderClaude:
		commit, serr := a.startClaude(w, prompt, resume, recordCase)
		if serr != nil {
			_ = a.manager.RejectSubmit(w, id)
			return serr
		}
		// host 指標先抓：commit() 之後 reaper 可能已把它自 registry 取走
		// （fast exit：done 已關、accepted 立刻收尾），此時 hostFor 會回 nil，
		// 但 init-before-Accept 暫存的 session id 仍在這個 host 上，補 commit
		// 必須讀得到它。
		host := a.hostFor(w)
		if h := a.hookAfterProviderStart; h != nil {
			h()
		}
		aerr := a.manager.AcceptSubmit(w, id, "", prompt)
		commit(aerr == nil) // 自然結束 goroutine 據此決定走 EndSessionFlow 或直接清理
		if aerr == nil {    // Accept 成功才 commit（staged candidate；D6）
			if cerr := a.restore.CommitResume("claude", a.hostSessionID(host), taskLabel); cerr != nil {
				a.failLoudRestore(contract.ProviderClaude, cerr) // session 保持 active、Start 照樣成功
			}
		}
		return aerr
	default: // codex
		threadID, alreadyEnded, serr := a.startCodex(w, prompt, resume, recordCase, approvalPolicy)
		if serr != nil {
			_ = a.manager.RejectSubmit(w, id)
			return serr
		}
		if h := a.hookAfterProviderStart; h != nil {
			h()
		}
		if err := a.manager.AcceptSubmit(w, id, threadID, prompt); err != nil {
			// 第三輪 P1-5：host（runner／lease／路由）已發布——Accept 失敗必須
			// 回收，否則 shutdown snapshot 之後才發布的資源會漏收（破壞
			// 「全部 finalize 後才 Manager.Close」保證）
			terr := a.codexTeardown(a.hostFor(w)) // 冪等：撤路由＋finalize＋session:done
			return errors.Join(err, terr)
		}
		if cerr := a.restore.CommitResume("codex", threadID, taskLabel); cerr != nil { // Accept 成功才 commit
			a.failLoudRestore(contract.ProviderCodex, cerr) // session 保持 active、Start 照樣成功
		}
		_ = alreadyEnded // completed 先到：busy 未設，無需額外收尾
		return nil
	}
}

// SendMessage：指定 provider 既有 session 的後續輪（僅該 slot phaseActive 允許；
// 錯誤原樣回 UI）。雙 session 並存：一個 provider busy 不影響另一個。
func (a *App) SendMessage(provider, prompt string) error {
	if !knownProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	pv := contract.Provider(provider)
	w := a.legacyWSIDFor(pv) // 兩個 provider 都已遷入 sessionHost（§3.3）：WSID 定址
	h := a.hostFor(w)
	if pv == contract.ProviderClaude {
		id, err := a.manager.BeginSubmit(w)
		if err != nil {
			return err
		}
		if h == nil || h.sess == nil {
			_ = a.manager.RejectSubmit(w, id)
			return errors.New("no active claude session")
		}
		if err := h.sess.Send(prompt); err != nil {
			_ = a.manager.RejectSubmit(w, id)
			return err
		}
		return a.manager.AcceptSubmit(w, id, a.hostSessionID(h), prompt)
	}
	id, err := a.manager.BeginSubmit(w)
	if err != nil {
		return err
	}
	if h == nil || h.runner == nil {
		_ = a.manager.RejectSubmit(w, id)
		return errors.New("no active codex thread")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if _, _, err := h.runner.StartTurn(ctx, prompt); err != nil {
		_ = a.manager.RejectSubmit(w, id)
		return err
	}
	return a.manager.AcceptSubmit(w, id, h.runner.ThreadID(), prompt)
}

// EndSession：指定 provider 的收尾編排（appcore.EndSessionFlow）。冪等；
// ErrProviderBusy 等真實錯誤原樣回 UI。
//
// review P1（fix/lifecycle-app-txn）：整段納入 app transaction（beginAppTxn／
// endAppTxn，沿 app.go:213 慣例，同 StartSession／ensureAppServer／B1 probe）。
// 修前的 gap：EndSession 完全不在 shutdown gate 之內——使用者已呼叫的 EndSession
// 若跟 shutdown() 同時發生，shutdown 的 inflight.Wait() 不會等它，會直接往下讀
// 該 session 的 host（M3b 前為 a.claudeSess）進 forcedShutdown；forcedShutdown
// 見到 BeginEndSession 失敗會套用 ErrEndInProgress benign 規則（review P2），但
// 那條規則的前提是「贏家是 startClaude 內建的自然收尾 reaper、且與
// forcedShutdown 共用同一份 host.teardownFn」——這裡贏家其實是 EndSession 自己
// 建的另一份獨立閉包，forcedShutdown 等的 teardownFn 從未被觸發，於是變成對同一
// 個 session 並行跑兩份 CloseSequence、重複發 session:done。納入 app transaction
// 後這個 race window 直接消失：EndSession 在 shuttingDown 之後一律被拒（不會冒出
// 新的 in-flight teardown）；EndSession 若已在 shuttingDown 之前開始，
// inflight.Wait() 保證等它完整返回（含 teardown／FinishEndSession）才會往下
// snapshot host——forcedShutdown 執行時看到的必是 EndSession 已經收乾淨的
// 狀態（host 已被 takeHost 取出），兩者時間上不再重疊。
//
// 沒有讓 EndSession 改共用 host.teardownFn（那個 OnceValue 只服務
// forcedShutdown「BeginEndSession 失敗仍重跑 teardown 兜底」的模式）：
// EndSession 本身沒有這種 fallback，appcore.BeginEndSession 失敗時
// EndSessionFlow 根本不會呼叫 teardown、直接把錯誤原樣回給呼叫端，不管閉包
// 是否共用都不會有第二次真正執行——與自然收尾 reaper 競速（跟 shutdown 無關
// 的既有情境，見 fix/shutdown-end-in-progress report concern 3）因此維持原行為
// 未變動，殘留面已知、非本輪修復範圍。
func (a *App) EndSession(provider string) error {
	if !knownProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：見上方 doc
		return err
	}
	defer a.endAppTxn()
	w := a.legacyWSIDFor(contract.Provider(provider)) // 兩個 provider 都 WSID 定址（§3.3）
	h := a.hostFor(w)
	if provider == "claude" {
		return appcore.EndSessionFlow(a.manager, w, nil, a.claudeTeardown(h))
	}
	busy := func() bool { return h != nil && h.runner != nil && h.runner.ActiveTurnID() != "" }
	return appcore.EndSessionFlow(a.manager, w, busy, func() error { return a.codexTeardown(h) })
}

func (a *App) TerminateSession(provider string) error {
	if !knownProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	h := a.hostFor(a.legacyWSIDFor(contract.Provider(provider)))
	if provider == "claude" {
		if h == nil || h.sess == nil {
			return errors.New("no active claude session")
		}
		return h.sess.Terminate()
	}
	// codex：長駐 server 不關（其他 session 共用它），只中斷這個 session 的 turn
	if h == nil {
		return errors.New("no active codex session")
	}
	params, err := h.track.InterruptParams()
	if err != nil {
		return err
	}
	a.mu.Lock()
	conn := a.codexConn
	a.mu.Unlock()
	if conn == nil {
		return errors.New("codex app-server not running")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	_, err = conn.Call(ctx, codex.MethodTurnInterrupt, params)
	return err
}

// ---- Claude 線 ----

func approvalTimeout() time.Duration { return approval.BrokerTimeout() }

// startClaude 啟動 provider 並回傳 commit callback：呼叫端於 AcceptSubmit 成敗後
// 以 commit(accepted) 通知自然結束 goroutine——快速退出（auth／參數錯誤）時
// goroutine 會等 start 交易 commit/abort 才收尾，不會在 phase=starting 空轉。
//
// M3b §3.3：所有 ownership 收在 w 的 sessionHost 上——approval socket 與 MCP
// config 都帶 WSID（原本固定是 `<stateDir>/approval.sock`／`mcp.json`，第二個
// session 啟動會直接覆寫第一個的檔案，這是多 session 不可能成立的根本原因）。
//
// host 一律「填滿才 publish」（見 sessionHost doc 的併發規約）：sess／broker／
// lease／teardownFn 全部就緒才 putHost，因此 registry 裡不會出現半成品，讀者
// 可以在鎖外安全讀取這些欄位。這條規約靠「publish 之前啟動的兩條 pump goroutine
// 都拿不到 host 指標」維持——它們只收窄值與兩個在 a.mu 下操作的 closure。
// start 失敗時 host 從未進 registry，rollback 只需關 broker ＋ 歸還 socket 槽位。
func (a *App) startClaude(w appcore.WSID, prompt, resume, recordCase string) (func(accepted bool), error) {
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return nil, err
	}
	if a.registry == nil {
		return nil, fmt.Errorf("session registry unavailable (startup error: %s)", a.startupErr)
	}
	if resume != "" { // resume mismatch 拒絕
		if bound, ok := a.registry.CWD(resume); !ok || bound != cwd {
			return nil, fmt.Errorf("resume refused: session %s bound to %q, current %q", resume, bound, cwd)
		}
	}
	host := &sessionHost{
		wsid: w, provider: contract.ProviderClaude, sockIndex: -1,
		mcpPath: filepath.Join(a.stateDir, "mcp-"+string(w)+".json"),
	}
	// socket 槽位由 free-list 配置，因此同一個 WSID 重啟時新舊 host 必然拿到不同
	// 路徑——不需要（也不允許）在這裡去關還留在 registry 裡的舊 broker：那是
	// take-then-dispose 契約禁止的形狀，而且舊 broker 的 listener 一關就會 unlink
	// socket 檔案，若新舊剛好同路徑，反而會把剛建好的新 socket 刪掉、approval
	// 靜默失效。舊 host 一律由它自己的 teardown 收（關 broker ＋ 歸還槽位）。
	idx, err := a.reserveSockIndex(host)
	if err != nil {
		return nil, err // fail loud：不降級成共用 socket
	}
	host.sockIndex, host.sockPath = idx, approvalSockPath(a.stateDir, idx)
	_ = os.Remove(host.sockPath) // 前一輪殘留的 socket 檔（行程被 kill 時不會 unlink）
	br, err := approval.NewBroker(host.sockPath, approvalTimeout(), a.auditWriterFor())
	if err != nil {
		a.releaseSockIndex(host)
		return nil, err
	}
	host.broker = br
	committed := false // 未 commit ownership 的 rollback：後續任何失敗都回收 broker
	defer func() {
		if committed {
			return
		}
		_ = br.Close() // host 尚未 putHost（只在最後一步 publish），無需自 registry 撤回
		a.releaseSockIndex(host)
	}()

	// 以下兩條 goroutine 在 host publish 之前就啟動，因此刻意只給窄值與 closure、
	// 不給 host 指標（見 sessionHost 併發規約）：sessionID／noteSessionID 內部一律
	// 走 a.mu 下的存取器，goroutine 本體碰不到尚未寫入的 sess／pumpDone／lease。
	sessionID := func() string { return a.hostSessionID(host) }
	noteSessionID := func(id string) {
		a.setHostSessionID(host, id)
		a.commitClaudeResume(host, id) // accepted generation 才寫（late init guard）
	}
	br.SetTimeoutHook(func(id string) { // 逾時 deny 後收掉 UI 的過期彈窗
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		a.noteWSEmitError("approval_decision", w,
			a.manager.EmitApprovalDecision(w, sessionID(), "timeout", ""))
		a.emit("approval:dismiss", map[string]any{"id": id, "cause": "timeout"})
	})
	go a.pumpApprovals(w, contract.ProviderClaude, br, sessionID)

	self, _ := os.Executable()
	if o := os.Getenv("WORKBENCH_MCP_COMMAND_OVERRIDE"); o != "" { // A6 注入點
		self = o
	}
	mcpCfg := host.mcpPath
	cfg := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":%q,"args":["mcp-approval","--socket",%q]}}}`, self, host.sockPath)
	if err := os.WriteFile(mcpCfg, []byte(cfg), 0o644); err != nil {
		return nil, err
	}

	var rec *recorder.Recorder
	var lease *appcore.RecordingLease
	if recordCase != "" {
		rec, err = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".ndjson")
		if err != nil { // Recorder 初始化失敗 = 可見的 session 失敗，不無聲降級
			return nil, err
		}
	}

	sess, err := claude.Start(a.ctx, claude.Config{
		Binary: a.claudeCLIPath(), CWD: cwd, Prompt: prompt, Resume: resume,
		MCPConfigPath: mcpCfg, PermissionPromptTool: "mcp__workbench__approval_prompt",
		// ask 優先於 allow、defaultMode 蓋掉使用者環境的 plan 預設
		SettingsJSON: `{"permissions":{"defaultMode":"default","ask":["Bash(touch *)"]}}`,
		Env:          a.childEnv(),
		MultiTurn:    true, // M1：stdin 保持開啟，SendMessage 逐輪送 user message
	})
	if err != nil {
		if rec != nil {
			_ = rec.CloseWith(recorder.Meta{Provider: "claude", RecordedAt: time.Now().UTC().Format(time.RFC3339)})
		}
		return nil, err
	}
	if rec != nil {
		lease = appcore.NewRecordingLease(rec, func() error { return nil },
			func(ex ports.Exit) recorder.Meta {
				m := recorder.Meta{Provider: "claude", CLIVersion: a.cliVersion("claude"),
					Argv: sess.Argv(), CWD: cwd,
					RecordedAt: time.Now().UTC().Format(time.RFC3339), StderrTail: ex.StderrTail}
				if ex.Exited { // 未知結局不偽裝 exit code（meta ExitCode 維持 nil）
					code := ex.Code
					m.ExitCode = &code
				}
				return m
			})
	}

	// pump：錄流 tap ＋ init 綁定 registry → 一律經 Manager.Emit（該 WSID slot）
	done := appcore.Pump(sess.Events(), func(ev contract.Event) {
		if rec != nil {
			if lerr := rec.Line(ev.Raw); lerr != nil {
				a.noteWSEmitError("emit", w, a.manager.Emit(w, contract.Event{
					Provider: contract.ProviderClaude,
					Kind:     contract.KindStreamError, Raw: []byte(lerr.Error()), Err: lerr}))
			}
		}
		if info := claude.ParseInit(ev); info != nil {
			_ = a.registry.Bind(info.SessionID, cwd)
			noteSessionID(info.SessionID)
		}
		a.noteWSEmitError("emit", w, a.manager.Emit(w, ev))
	})

	// teardownFn：shared sync.OnceValue（review P2）——這個 goroutine 下方的
	// 自然收尾 reaper 與 forcedShutdown（見其 doc）都可能對同一個 session
	// 呼叫 teardown；用同一份 memoized 執行保證 CloseSequence 對 sess/lease
	// 全程恰好真正跑一次，另一方呼叫只是阻塞等收斂，不會 double-Close／
	// double-Terminate／double-Finalize。遷入 host 之後保證不變：這份 OnceValue
	// 只掛在 host.teardownFn，兩條路徑都由該 host 取得同一個閉包。
	teardownFn := sync.OnceValue(a.claudeTeardown(host))
	host.sess, host.pumpDone, host.lease, host.teardownFn = sess, done, lease, teardownFn
	a.putHost(host) // 全部欄位就緒才 publish（見 sessionHost 併發規約）

	commitCh := make(chan bool, 1)
	go func() { // reaper：先等 start 交易結果，再決定收尾路徑
		accepted := <-commitCh
		if !accepted {
			// 交易 abort：MultiTurn CLI 可能仍在等下一輪輸入（done 不會自己關），
			// 不能等 EOF——立即 teardown（CloseSequence 關 stdin → 界限內收乾）。
			if err := teardownFn(); err != nil {
				a.audit("claude_aborted_start_cleanup_error", map[string]any{"error": err.Error()})
			}
			return
		}
		<-done                    // committed：等自然結束／崩潰（pump 收乾）再走同一收尾編排
		if a.hostFor(w) != host { // EndSession 已接手（或該 WSID 已換上新 host）
			return
		}
		if h := a.hookClaudeReaperBeforeEndFlow; h != nil { // 測試 barrier：見 App 欄位 doc
			h()
		}
		if err := appcore.EndSessionFlow(a.manager, w, nil, teardownFn); err != nil {
			a.audit("claude_natural_end_error", map[string]any{"error": err.Error()})
		}
	}()
	committed = true
	return func(accepted bool) { commitCh <- accepted }, nil
}

// claudeTeardown：CloseSequence（close → quiesce → 必要時 terminate → Wait →
// lease.Finalize(ex)），並發 session:done（Exit 為證據）。自然收尾 reaper／
// forcedShutdown 一律經 startClaude 建的 sync.OnceValue（host.teardownFn）
// 呼叫這個 factory 回傳的閉包，保證兩者對同一個 session 共用同一份真正執行
// ——見 startClaude／forcedShutdown 的 doc（review P2）。EndSession／
// NewSession 仍直接呼叫這個 factory 回傳的新閉包（各自持有排他的
// BeginEndSession token，不在 review P2 描述的競速對之內，刻意未改動，見
// review report concern 3）。
//
// host 為 nil（該 WSID 沒有 live session）回「no active claude session」——維持
// 遷移前 sess == nil 的同一形狀（NewSession 的 teardown 失敗路徑依賴它）。
func (a *App) claudeTeardown(host *sessionHost) func() error {
	return func() error {
		if host == nil || host.sess == nil {
			return errors.New("no active claude session")
		}
		if h := a.hookClaudeTeardownBarrier; h != nil { // 測試 barrier：見 App 欄位 doc；任一呼叫端（OnceValue 或 fresh 閉包）真正執行的進入點
			h()
		}
		fin := func(ex ports.Exit) error {
			if host.lease != nil {
				return host.lease.Finalize(ex)
			}
			return nil
		}
		ex, err := appcore.CloseSequence(host.sess.Close, host.pumpDone, 5*time.Second, 10*time.Second,
			host.sess.Terminate, host.sess.Wait, fin)
		// take-then-dispose（見 dropHost doc）：先把 host 自 registry 取出——之後
		// 沒有新讀者能拿到它——才在鎖外處置。identity check 保證不會誤刪同一
		// WSID 上已換代的新 host；broker 與 socket 槽位都是本 host 自己的，無論
		// 是否仍在 registry 都由這裡負責回收（槽位歸還讓位給下一次 start）。
		a.takeHost(host)
		if host.broker != nil {
			_ = host.broker.Close()
		}
		a.releaseSockIndex(host)
		var recErrText string
		if err != nil {
			recErrText = err.Error()
		}
		payload := map[string]any{"provider": "claude", "stderrTail": ex.StderrTail,
			"recorderError": recErrText}
		if ex.Exited {
			payload["exitCode"] = ex.Code
		}
		a.emit("session:done", payload)
		return err
	}
}

// ---- Codex 線 ----

// errCodexNotRunning：沒有已發布的長駐 server（不重建的讀取路徑用）。
var errCodexNotRunning = errors.New("codex app-server not running")

// ensureAppServer：取得長駐 app-server；沒有存活的就建一個新 generation。
//
// M3b §3.4：不再用 Single.Ensure——它不回傳 epoch，經它建立的 instance 掛不上
// WatchGeneration 死亡 reaper，那條路徑上的 wire log 在 server 意外死亡時永遠
// 不會 finalize（而且完全靜默）。改為「codexServerMu 內 check-then-act ＋
// RunOwnedHandshake 發布」：Ensure 的「存活就沿用」語意由這裡的 Current＋Done
// 檢查提供，序列化由 codexServerMu 提供（Ensure 原本是靠 Single.mu 提供的）。
//
// 鎖序：codexServerMu → Single.mu（RunOwnedHandshake 內部自持），呼叫端不得
// 再包一層 codexSingle 的鎖。
func (a *App) ensureAppServer() (codex.ProbeTarget, error) {
	// server-create 交易：check 與建立對 shutdown 原子（TOCTOU 關閉）——
	// AuthStatus／StartLogin／Logout 等所有經此入口的路徑一體適用
	if err := a.beginAppTxn(); err != nil {
		return nil, err
	}
	defer a.endAppTxn()
	if h := a.hookInServerTxn; h != nil { // 測試 barrier：交易已登記、建立未開始
		h()
	}
	a.codexServerMu.Lock()
	defer a.codexServerMu.Unlock()
	if o, ok := a.codexSingle.Current(); ok && o.Server != nil {
		select {
		case <-o.Done(): // 已死：它自己的 watcher 負責收尾，這裡直接建新 generation
		default:
			return o.Server, nil
		}
	}
	if err := a.replaceCodexGeneration(); err != nil {
		return nil, err
	}
	o, ok := a.codexSingle.Current()
	if !ok {
		return nil, errCodexNotRunning
	}
	return o.Server, nil
}

// currentAppServer 取得既有長駐 server（不重建、不清空 ownership）。
func (a *App) currentAppServer() (codex.ProbeTarget, error) {
	o, ok := a.codexSingle.Current()
	if !ok || o.Server == nil {
		return nil, errCodexNotRunning
	}
	return o.Server, nil
}

// ---- Codex connection-wide wire log：generation 生命週期＋recorder error latch ----
//
// §3.4.1／§3.4.6-7。每個 app-server generation 一份 always-on 錄流（wire_log_id
// 在掛 recorder 之前配置），錄到該 server 的生命終點才 finalize；寫入失敗即
// latch、發 workspace 通知並拒絕新 Codex session，只有受控復原能解除。

// errWireLogDegraded：latch 生效時新 Codex session 的拒絕原因。
var errWireLogDegraded = errors.New("codex wire log degraded: 已停止建立新 Codex session，請先執行錄流復原")

// wireLogDir：connection-wide wire log 的落點——每個 generation 一份
// <wire_log_id>.jsonl ＋ <wire_log_id>.meta.json（沿用 recorder 的檔案形狀）。
// 與 session 級錄流（recordings/）分開放：兩者的生命週期單位不同，一個是
// app-server generation、一個是 session。
func (a *App) wireLogDir() string { return filepath.Join(a.stateDir, "wire-logs") }

// wireStep：受控復原／replacement 的步驟探針（測試注入，見 hookWireStep）。
func (a *App) wireStep(step string) {
	if h := a.hookWireStep; h != nil {
		h(step)
	}
}

// newWireGeneration 配置新的 wire_log_id 並開檔。序號讓同一秒內的多次
// replacement 也不會撞 id（wirelog.NewGeneration 與 recorder.New 同慣例，同名
// 會直接覆寫舊檔，不做去重保護）。
func (a *App) newWireGeneration() (*wirelog.Generation, error) {
	a.wireMu.Lock()
	a.wireSeq++
	id := fmt.Sprintf("codex-wire-%s-%03d", time.Now().UTC().Format("20060102T150405"), a.wireSeq)
	a.wireMu.Unlock()
	return wirelog.NewGeneration(a.wireLogDir(), id)
}

// wireLatched 回報 recorder error latch 是否生效。
func (a *App) wireLatched() bool {
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	return a.wireErr != nil
}

// latchWireRecorder：錄流寫入失敗的唯一入口（§3.4.6）——latch 首個錯誤、立刻發
// workspace 通知，自此拒絕新 Codex session（既有 session 仍可 bounded 收尾）。
//
// 「每個 degraded generation 只發一次通知」由 nil → non-nil 的狀態轉移保證：
// latch 只在新 generation 全部成功時清除（clearWireLatch），所以同一個
// generation 內至多發生一次轉移。刻意不另外放一個 per-generation sync.Once
// ——那是同一件事的第二份狀態，兩者一旦不同步就會多發或漏發。
func (a *App) latchWireRecorder(err error) {
	if err == nil {
		return
	}
	a.wireMu.Lock()
	first := a.wireErr == nil
	if first {
		a.wireErr = err
	}
	id := ""
	if a.wireGen != nil {
		id = a.wireGen.ID()
	}
	a.wireMu.Unlock()
	if !first { // 同一 generation 的後續錯誤：latch 已保留首因，不重複通知
		return
	}
	a.audit("codex_wire_log_degraded", map[string]any{"wireLogId": id, "error": err.Error()})
	a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
		"component": "codex_wire_log", "wireLogId": id, "error": err.Error()})
}

// clearWireLatch 在新 generation 的 recorder 掛載、handshake 與發布全部成功之後
// 解除 latch——這是 §3.4.6 凍結的**唯一**解除條件（不因時間或重試次數自動解除）。
func (a *App) clearWireLatch(gen *wirelog.Generation) {
	a.wireMu.Lock()
	was := a.wireErr
	a.wireErr, a.wireGen = nil, gen
	a.wireMu.Unlock()
	if was == nil {
		return
	}
	id := ""
	if gen != nil {
		id = gen.ID()
	}
	a.audit("codex_wire_log_recovered", map[string]any{"wireLogId": id, "previousError": was.Error()})
	a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
		"component": "codex_wire_log", "wireLogId": id, "event": "recovered"})
}

// checkWireRecorder 把目前 generation latch 住的寫入錯誤升級成 App 層 latch。
//
// 為什麼要輪詢：錄流 sink 掛在 codex.Conn 內部，寫入錯誤只 latch 進
// wirelog.Generation.writeErr（Conn.record 看到 sink 回錯只記進 recErr，不通知
// 任何人），沒有推播管道。故 App 在兩個決策點取用：每一筆入站 frame 之後
// （wireCodexConn 的 handler）與新 Codex session 的建立閘門。c2s 寫入失敗因此
// 最遲在下一筆入站 frame 或下一次建立 session 時被看見。
func (a *App) checkWireRecorder() {
	a.wireMu.Lock()
	gen := a.wireGen
	a.wireMu.Unlock()
	if gen == nil {
		return
	}
	a.latchWireRecorder(gen.Err())
}

// codexWireGate：latch 生效時拒絕新 Codex session（§3.4.6）。既有 session 的
// 收尾與**受控復原**（RecoverCodexRecording）刻意不經此閘門——復原正是唯一的
// 解除路徑，擋掉它 latch 就永遠解不開。
func (a *App) codexWireGate() error {
	a.checkWireRecorder()
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	if a.wireErr != nil {
		return fmt.Errorf("%w：%v", errWireLogDegraded, a.wireErr)
	}
	return nil
}

// onCodexGenerationFinalized 是 WatchGeneration 的回呼。
//
// **wasActive == false 不是異常**：每次受控 replacement 之後都會補發一次——舊
// owner 的 watcher 仍掛在舊 process 上，該 process 退出時就會以 false 回報。把
// 它當意外死亡處理（發事件、re-latch、觸發重啟）會在每次正常 restart 後誤觸，
// 故這裡只記 audit、不進任何反應路徑。
func (a *App) onCodexGenerationFinalized(err error, wasActive bool) {
	payload := map[string]any{"wasActive": wasActive}
	if err != nil {
		payload["error"] = err.Error()
	}
	if !wasActive {
		a.audit("codex_generation_finalized", payload)
		return
	}
	a.audit("codex_server_died", payload) // 現役 server 意外死亡：唯一該 fail loud 的分支
	a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
		"component": "codex_app_server", "error": "codex app-server exited unexpectedly"})
}

// replaceCodexGeneration 是「建立或替換一個 codex app-server generation」的唯一
// 實作，三個呼叫端共用（ensureAppServer／RestartCodexServerRecorded／
// RecoverCodexRecording）。順序凍結於 codex.RunOwnedHandshake 內部：
// 收尾舊 owner（terminate → wait → drain → detach → finalize 舊 generation）→
// 配置新 wire_log_id → start → 掛 recorder → handshake → 發布（§3.4.2／§3.4.7）。
//
// **全段由 RunOwnedHandshake 自己的 WithExclusiveEpoch 單層互斥交易保護，這裡
// 不得再包一層 codexSingle 的鎖**——同一把 mutex，巢狀即死鎖。呼叫端必須先持有
// a.codexServerMu（見該欄位 doc）。
//
// 發布判定看 Single 本身、不看 err：RunOwnedHandshake 的契約是「err != nil 不
// 代表沒有發布」（新 server handshake 成功但舊 owner 收尾失敗時 ownership 已經
// 換手）。反過來失敗時 Single 必為空（keep=false 會清空 ownership），所以
// Current() 非空即代表本次已發布。
func (a *App) replaceCodexGeneration() error {
	_, hadOld := a.codexSingle.Current()
	newGen := func() (*wirelog.Generation, error) {
		if hadOld { // RunOwnedHandshake 在建新 generation 之前已收完舊 owner
			a.wireStep("finalize_old")
		}
		a.wireStep("new_wire_log_id")
		return a.newWireGeneration()
	}
	start := func() (codex.ProbeTarget, error) {
		a.wireStep("start")
		if a.codexServerFactory != nil { // 測試注入：fake wire 走同一段 production 編排
			return a.codexServerFactory()
		}
		srv, err := codex.StartAppServer(a.ctx, codex.Config{Binary: a.codexCLIPath(),
			CWD: a.workspaceDir, Env: a.childEnv()})
		if err != nil {
			return nil, err // 具體型別的 nil 不可直接回傳（會變成非 nil 介面值）
		}
		return srv, nil
	}
	hctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	err := codex.RunOwnedHandshake(hctx, &a.codexSingle, newGen, start, clientInfo(),
		a.onCodexGenerationFinalized)
	o, published := a.codexSingle.Current()
	if published {
		a.wireCodexConn(o.Server.Conn()) // 發布成功即接上 handlers
	}
	if err != nil {
		return err // §3.4.6：任一步失敗即 dispose 新 server 並保留 latch
	}
	a.clearWireLatch(o.Generation)
	return nil
}

// refuseIfCodexLive：受控 replacement／復原的拒絕條件（§3.4.7）——存在 live
// Codex host 或 in-flight turn 一律拒絕，只有 dormant session 不阻擋。
// in-flight turn 先判：它是兩者中較嚴重、訊息也較精確的那一個。
func (a *App) refuseIfCodexLive() error {
	hosts := a.hostsOf(contract.ProviderCodex)
	for _, h := range hosts {
		if h.runner != nil && h.runner.ActiveTurnID() != "" {
			return fmt.Errorf("codex 受控 replacement 被拒：session %s 仍有 in-flight turn（§3.4.7）", h.wsid)
		}
	}
	if len(hosts) > 0 {
		return fmt.Errorf("codex 受控 replacement 被拒：仍有 %d 個 live host（§3.4.7）", len(hosts))
	}
	return nil
}

// RecoverCodexRecording 是 §3.4.6 recorder error latch 的 in-process 復原入口，
// 同時是 §3.4.7 受控 app-server replacement 的最小方案。
//
// 為什麼它不能被 latch 擋住：latch 的唯一解除條件是「新 generation 的 recorder
// 掛載、handshake 與發布全部成功」，而新 generation 的觸發（新 session、
// ensureAppServer）都在被 latch 擋掉的那些路徑上——復原入口自己再被擋一次，
// latch 就永遠解不開。
//
// 順序：收乾 live host（此處為「確認沒有 live host／in-flight turn」，有就拒絕）
// → terminate → wait → finalize 舊 generation → 配置新 wire_log_id → 起新
// server → 掛 recorder → handshake → 發布；中段由 replaceCodexGeneration 委派
// 給 RunOwnedHandshake。拒絕不改變 latch 狀態。
func (a *App) RecoverCodexRecording() error {
	if err := a.beginAppTxn(); err != nil { // 與 ensureAppServer／B1 probe 同一 shutdown 閘門
		return err
	}
	defer a.endAppTxn()
	a.codexServerMu.Lock()
	defer a.codexServerMu.Unlock()
	if err := a.refuseIfCodexLive(); err != nil {
		a.audit("codex_wire_recovery_refused", map[string]any{"error": err.Error()})
		return err
	}
	a.wireStep("drain")
	if err := a.replaceCodexGeneration(); err != nil {
		a.audit("codex_wire_recovery_failed", map[string]any{"error": err.Error()})
		return err
	}
	a.audit("codex_wire_recovery_ok", map[string]any{})
	return nil
}

// ---- Codex dispatcher（M3b §3.3；Task 9）----
//
// 共用 codex.Conn 上的每個 s2c frame 都必須被歸屬到唯一的 WSID，否則多 session
// 之間會串線。分流表：
//
//	帳號層廣播  account/login/completed｜account/updated
//	            → 既有語意（auth:status ＋ audit），不進 WSID 路由、不 fail loud
//	server 級廣播 codexBroadcastNotifications（見其 doc）
//	            → workspace lane，不進 WSID 路由、不 fail loud
//	thread-scoped 其餘 ServerNotifications ＋兩種 requestApproval
//	            → codexWSIDFor 歸屬；歸屬不到一律 fail loud
//	未知 method（OnUnknown）
//	            → 帶 identity 則同上；完全不帶 identity 視為 server 級廣播
//
// 「歸屬不到就 fail loud」與「廣播不 fail loud」的界線是刻意的（coordinator
// 2026-08-15 凍結，依 Task 0 live probe 實據）：account/rateLimits/updated 與
// remoteControl/status/changed 在真實 server 上**本來就不帶 threadId**，把它們
// 當成歸屬缺口會讓 app 在正常運作下持續報錯；反過來，本應帶 identity 的 frame
// 一旦歸屬不到，靜默丟棄或猜一個 session 都是資料正確性問題，必須吵。

// codexBroadcastNotifications：server／帳號層的廣播通知——不屬於任何 thread，
// 依定義沒有 threadId。account/rateLimits/updated 與 remoteControl/status/changed
// 由 Task 0 的 live probe 實測確認（`notif_missing_identity_methods`）；後者不在
// codex.ServerNotifications 白名單內，會走 OnUnknown，因此兩條路徑都要查這張表。
var codexBroadcastNotifications = map[string]bool{
	codex.MethodAccountRateLimitsUpdated:   true,
	codex.MethodRemoteControlStatusChanged: true,
}

// codexFrameIdentity：s2c frame 的 identity 欄位。turnId 有兩種 wire 形狀——
// turn/started｜turn/completed 是巢狀的 turn.id，requestApproval 是扁平的 turnId
// （pinned schema 覆核），兩者都要認。
//
// 巢狀形狀刻意委派給 appcore.ParseTurnStarted（TurnTrack.NoteStarted 用的同一份）：
// 兩處解析同一個 wire shape，各寫一份在 shape 變動時必然漂移。
func codexFrameIdentity(params []byte) (threadID, turnID string) {
	threadID, nested := appcore.ParseTurnStarted(params) // threadId ＋ 巢狀 turn.id
	var p struct {
		TurnID string `json:"turnId"`
	}
	_ = json.Unmarshal(params, &p)
	turnID = p.TurnID
	if turnID == "" {
		turnID = nested
	}
	return threadID, turnID
}

// codexWSIDFor：identity → WSID，查找順序 turnId → threadId → pending start。
//
// 第三順位的語意：thread/start｜resume 送出到 response 抵達之間，server 可能先
// 送帶著「client 尚不知道的 threadId」的通知（thread/started 即是），此時前兩個
// 索引必然落空。這個窗口由 codexStartMu 保證至多一筆 pending start，因此「恰好
// 一筆」即可唯一歸屬；出現兩筆以上代表 codexStartMu 的不變量被破壞，寧可回
// false 讓上層 fail loud，也不猜。
//
// **第三順位要求 identity 非空**（review Important）：pending tier 的存在理由是
// 「frame 帶著 client 還不知道的 threadId」——它從來不是為「完全沒有 identity 的
// frame」準備的。少了這個條件，一筆本應帶 identity 卻缺漏的白名單通知（server bug）
// 只要剛好落在 pending 窗口內，就會被靜默塞進「正在啟動的那個 session」，正是凍結
// (1) 後半要 fail loud 的案例；approval 走同一條路徑，後果是核可對話框掛到錯的
// session——本 task 的核心動機形狀。
//
// 已知殘留窗口：pending start 進行中時，一筆來自「剛被 teardown、路由已撤掉的
// 舊 thread」的晚到 frame 會被歸到這筆 pending start。兩者在 wire 上無法區分
// （晚到 frame 帶的就是一個查不到的 threadId），而另一個選項——fail loud——會把
// 正常的 pending start 通知也一起吵掉。窗口只有一次 thread/start 往返、且只影響
// 一筆已結束 session 的殘留事件，取捨後選擇歸屬。
func (a *App) codexWSIDFor(threadID, turnID string) (appcore.WSID, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if turnID != "" {
		if w, ok := a.codexTurnWSID[turnID]; ok {
			return w, true
		}
	}
	if threadID != "" {
		if w, ok := a.codexThreadWSID[threadID]; ok {
			return w, true
		}
	}
	if (threadID != "" || turnID != "") && len(a.codexPendingStarts) == 1 {
		for _, w := range a.codexPendingStarts {
			return w, true
		}
	}
	return "", false
}

// beginCodexPendingStart／endCodexPendingStart：pending start 登記（見 codexWSIDFor）。
func (a *App) beginCodexPendingStart(w appcore.WSID) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.codexPendingStarts == nil {
		a.codexPendingStarts = map[uint64]appcore.WSID{}
	}
	a.codexPendingSeq++
	a.codexPendingStarts[a.codexPendingSeq] = w
	return a.codexPendingSeq
}

func (a *App) endCodexPendingStart(seq uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.codexPendingStarts, seq)
}

// publishCodexHost：把 host 登記進 registry 並同時綁定 threadId → WSID。兩者必須
// 在同一個臨界區完成：分兩次鎖的話，中間抵達的通知會歸屬到 WSID 卻查不到 host
// （turn/completed 因此漏掉 NoteTurnEnded，busy 永久殘留）。
func (a *App) publishCodexHost(h *sessionHost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionHosts == nil {
		a.sessionHosts = make(map[appcore.WSID]*sessionHost)
	}
	if a.codexThreadWSID == nil {
		a.codexThreadWSID = map[string]appcore.WSID{}
	}
	a.sessionHosts[h.wsid] = h
	if h.threadID != "" {
		a.codexThreadWSID[h.threadID] = h.wsid
	}
}

// bindCodexTurn／unbindCodexTurn：turnId → WSID 的次級索引。thread-scoped frame
// 幾乎都帶 threadId（Task 0 live probe：107 筆通知中 102 筆帶、5 筆是廣播），
// 這層是給「只帶 turnId」的形狀用的，同時讓 turn 收尾能精確定位。
func (a *App) bindCodexTurn(turnID string, w appcore.WSID) {
	if turnID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.codexTurnWSID == nil {
		a.codexTurnWSID = map[string]appcore.WSID{}
	}
	a.codexTurnWSID[turnID] = w
}

func (a *App) unbindCodexTurn(turnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.codexTurnWSID, turnID)
}

// forgetCodexHostRouting：teardown 時撤掉該 host 的全部路由（thread 與它名下的
// turn）。identity check 與 takeHost 同理：同一個 threadId 若已被下一輪 resume
// 綁到別的 WSID，不得誤刪。
func (a *App) forgetCodexHostRouting(h *sessionHost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h.threadID != "" && a.codexThreadWSID[h.threadID] == h.wsid {
		delete(a.codexThreadWSID, h.threadID)
	}
	for turnID, w := range a.codexTurnWSID {
		if w == h.wsid {
			delete(a.codexTurnWSID, turnID)
		}
	}
}

// failLoudCodexDispatch：歸屬失敗的唯一出口（audit ＋ workspace lane stream_error）。
// 走 workspace lane 而非某個 session slot：這筆 frame 的 session 正是查不到的那個，
// 隨便挑一個 slot 發就是它要防的那種串線。
// a.manager 在這條路徑上必定非 nil：dispatch 只在 wireCodexConn 之後可達，而
// wireCodexConn 只在 startup 建好 manager 之後才被呼叫。刻意不加 nil guard——
// 同一條路徑上的 emitCodexBroadcast／Manager.Emit 也都沒有，只在其中一處加會讓
// 「哪些地方可能 nil」變成一個沒人說得清的問題。
func (a *App) failLoudCodexDispatch(msg string) {
	a.audit("codex_dispatch_error", map[string]any{"error": msg})
	a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{"error": msg})
}

// emitCodexBroadcast：server／帳號層廣播的出口——workspace lane，不碰任何 slot。
func (a *App) emitCodexBroadcast(method string, params json.RawMessage) {
	a.manager.EmitWorkspace(string(contract.KindCodexBroadcast), nil, map[string]any{
		"provider": "codex", "method": method, "params": json.RawMessage(params)})
}

// emitCodexUnknownBroadcast：OnUnknown 的廣播出口——payload 帶**完整 raw frame**。
// 這條路徑上的 frame 連 method／params 都可能解不出來（JSON 壞掉、或既無 id 也無
// method），遷移前的 KindUnknown 事件是保留 raw 的，換到 workspace lane 之後同樣
// 不能把它丟掉，否則故障排查時證據會憑空消失。
func (a *App) emitCodexUnknownBroadcast(raw []byte) {
	a.manager.EmitWorkspace(string(contract.KindCodexBroadcast), nil, map[string]any{
		"provider": "codex", "raw": string(raw)})
}

// dispatchCodexNotification：thread-scoped 通知的分流入口。回傳非 nil 代表本 frame
// 無法歸屬且**本應帶 identity**（呼叫端 fail loud）；廣播類一律回 nil。
func (a *App) dispatchCodexNotification(method string, params json.RawMessage) error {
	switch {
	case method == codex.MethodAccountLoginCompleted || method == codex.MethodAccountUpdated:
		a.emit("auth:status", map[string]any{"provider": "codex",
			"event": method, "payload": string(params)})
		a.audit("codex_auth", map[string]any{"method": method, "params": json.RawMessage(params)})
		return nil
	case codexBroadcastNotifications[method]:
		a.emitCodexBroadcast(method, params)
		return nil
	}
	threadID, turnID := codexFrameIdentity(params)
	w, ok := a.codexWSIDFor(threadID, turnID)
	if !ok {
		return fmt.Errorf("codex: 無法歸屬的 notification %s（threadId=%q turnId=%q）", method, threadID, turnID)
	}
	switch method {
	case codex.MethodTurnStarted:
		a.bindCodexTurn(turnID, w)
		if h := a.hostFor(w); h != nil {
			h.track.NoteStarted(params) // TerminateSession 需要 turnId
		}
	case codex.MethodTurnCompleted:
		if h := a.hostFor(w); h != nil {
			h.track.NoteEnded()
			if h.runner != nil {
				h.runner.NoteTurnEnded(turnID) // 解 busy；不動 recorder（session-scoped 錄流）
			}
		}
		a.unbindCodexTurn(turnID)
	}
	a.noteWSEmitError("emit", w, a.manager.Emit(w, codex.MapEvent(method, params)))
	return nil
}

// dispatchCodexUnknown：OnUnknown 的分流（未列入 codex.ServerNotifications 的通知、
// 或解不開的 frame）。帶 identity 就照 thread-scoped 規則走（歸屬不到 fail loud）；
// 完全不帶 identity 則歸為 server 級廣播——這類 frame 連 method 白名單都不在，host
// 無從判斷它「本應」帶不帶 identity，把它一律當成缺口會製造大量假警報
// （remoteControl/status/changed 正是這個形狀）。
func (a *App) dispatchCodexUnknown(raw []byte) error {
	var f struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(raw, &f)
	if codexBroadcastNotifications[f.Method] {
		a.emitCodexBroadcast(f.Method, f.Params)
		return nil
	}
	threadID, turnID := codexFrameIdentity(f.Params)
	if threadID == "" && turnID == "" {
		a.emitCodexUnknownBroadcast(raw)
		return nil
	}
	w, ok := a.codexWSIDFor(threadID, turnID)
	if !ok {
		return fmt.Errorf("codex: 無法歸屬的 unknown frame（method=%q threadId=%q turnId=%q）",
			f.Method, threadID, turnID)
	}
	a.noteWSEmitError("emit", w, a.manager.Emit(w, contract.Event{Provider: contract.ProviderCodex,
		Kind: contract.KindUnknown, Raw: append([]byte(nil), raw...)}))
	return nil
}

func (a *App) wireCodexConn(conn *codex.Conn) {
	a.mu.Lock()
	a.codexConn = conn
	a.mu.Unlock()
	conn.OnNotification(func(method string, params json.RawMessage) {
		if err := a.dispatchCodexNotification(method, params); err != nil {
			a.failLoudCodexDispatch(err.Error())
		}
		a.checkWireRecorder() // 每筆入站 frame 後取用錄流 latch（見 checkWireRecorder）
	})
	conn.OnUnknown(func(raw []byte) {
		if err := a.dispatchCodexUnknown(raw); err != nil {
			a.failLoudCodexDispatch(err.Error())
		}
		a.checkWireRecorder()
	})
	conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case codex.MethodCmdExecRequestApproval, codex.MethodFileChangeRequestApproval:
			return a.codexApproval(method, params), nil
		default:
			return nil, fmt.Errorf("unsupported server request %s", method)
		}
	})
}

// codexApproval：核可請求 → 同一 ApprovalDialog → allow=accept / deny=decline；逾時 decline（fail closed）。
//
// M3b §3.3：identity 路由。approval request 的 params 帶 threadId／turnId／itemId
// （pinned schema 兩種 requestApproval 的 required 皆含前二者；fileChange 形態另有
// Task 0 的 live frame 佐證），因此 approval 一律歸屬到提出請求的那個 WSID——原本
// 靠 currentRunner() 取「當前那個」的做法在多 session 下會把核可對話框送到錯的
// session，是 P1 級正確性問題。歸屬不到即 fail loud ＋ fail closed（decline），
// 不猜一個 session。
func (a *App) codexApproval(method string, params json.RawMessage) map[string]string {
	id := fmt.Sprintf("codex-%d", time.Now().UnixNano())
	threadID, turnID := codexFrameIdentity(params)
	w, ok := a.codexWSIDFor(threadID, turnID)
	if !ok {
		a.failLoudCodexDispatch(fmt.Sprintf(
			"codex: 無法歸屬的 approval 請求 %s（threadId=%q turnId=%q）→ decline", method, threadID, turnID))
		a.audit("codex_approval_unattributable",
			map[string]any{"id": id, "method": method, "raw_params": json.RawMessage(params)})
		return map[string]string{"decision": "decline"}
	}
	type codexDecision struct {
		allow  bool
		reason string
	}
	ch := make(chan codexDecision, 1)
	a.registerApproval(id, w, "codex", func(allow bool, reason string) error {
		ch <- codexDecision{allow, reason} // reason（如 Esc 的 "esc"）保留進 envelope
		return nil
	})
	a.audit("codex_approval_request", map[string]any{"id": id, "method": method,
		"wsid": string(w), "raw_params": json.RawMessage(params)})
	a.noteWSEmitError("approval_request", w,
		a.manager.EmitApprovalRequest(w, threadID, method, params))
	a.emit("approval:request", map[string]any{
		"id": id, "provider": "codex", "toolName": method, "inputJson": string(params)})
	decision := "decline"
	uiDecision := "deny"
	reason := ""
	select {
	case d := <-ch:
		reason = d.reason
		if d.allow {
			decision, uiDecision = "accept", "allow"
		}
	case <-time.After(approvalTimeout()):
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		uiDecision = "timeout"
		a.audit("codex_approval_timeout", map[string]any{"id": id})
		a.emit("approval:dismiss", map[string]any{"id": id, "cause": "timeout"})
	}
	a.noteWSEmitError("approval_decision", w,
		a.manager.EmitApprovalDecision(w, threadID, uiDecision, reason))
	a.audit("codex_approval_decision", map[string]any{"id": id, "decision": decision})
	return map[string]string{"decision": decision}
}

// codexHost：startCodexHost 對長駐 server 的最小依賴（fake wire 測試注入點）。
//
// Task 13 之後只剩 Conn()：Argv()／StderrSnapshot() 原本只服務 session-scoped
// 錄流的 meta，而 §3.4.4 已把 codex 的錄流證據整個移到 connection-wide wire log
// （那份 meta 由 GenerationOwner.FinalizeWith 從 ProbeTarget 取得）。
type codexHost interface {
	Conn() *codex.Conn
}

func (a *App) startCodex(w appcore.WSID, prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	// §3.4.6：latch 期間不得再開新的 provider session——那等於在明知錄流已壞的
	// 情況下累積不可稽核的活動。既有 session 的收尾（EndSession／turn 完成）不
	// 經此路徑，受控復原也不經（見 RecoverCodexRecording）。
	if err := a.codexWireGate(); err != nil {
		return "", false, err
	}
	if a.codexHostOverride != nil { // 測試 seam：fake wire 走同一 production 分支
		return a.startCodexHost(w, a.codexHostOverride, prompt, resume, recordCase, approvalPolicy)
	}
	srv, err := a.ensureAppServer()
	if err != nil {
		return "", false, err
	}
	return a.startCodexHost(w, srv, prompt, resume, recordCase, approvalPolicy)
}

// startCodexHost：EnsureThread＋StartTurn bounded synchronous（ctx 30s；turn/start
// response 立即回）。回傳 threadID 供 AcceptSubmit。
//
// M3b §3.3 的發布順序（凍結）：
//
//  1. beginCodexPendingStart：**送出 thread/start 之前**登記。這段窗口內 server 會
//     送帶著 client 尚不知道的 threadId 的通知，pending 登記是它們唯一的歸屬依據。
//     整段 EnsureThread 由 codexStartMu 序列化，保證至多一筆 pending（見 codexWSIDFor）。
//  2. publishCodexHost（EnsureThread 成功後、StartTurn 前）：host 與 threadId → WSID
//     在同一臨界區內生效，首輪事件因此找得到 runner／track——completed-before-response
//     由 ThreadRunner 的 earlyEnded latch 對消，前提正是 handler 這時已能命中 runner。
//  3. pending 登記直到 host publish 完成才解除（defer 順序）：中間沒有「兩個索引都
//     查不到」的空隙。
//
// host 填滿才 publish、publish 後不可變（見 sessionHost 併發規約）：runner／
// threadID 都在 publishCodexHost 之前寫定。StartTurn 失敗即 takeHost rollback，
// registry 不留半成品。
//
// **錄流（§3.4.4，Task 13 凍結）**：這裡**不再** attach 任何 session-scoped
// recorder。一條 codex.Conn 只容許一個 sink，而該 sink 已經是 §3.4.1 的
// connection-wide always-on wire log（由 GenerationOwner 在 handshake 之前掛上、
// 錄到 server 生命終點）；session 若再 attach 一次只會拿到「recording already in
// progress」，整個 session 起不來。recordCase 因此降為 **label**：進 audit 與
// sessionHost.recordLabel 供觀測，不控制 recorder attach。W6 的「codex resume 以
// JSON-RPC 錄流佐證」由 wire log 承載（覆蓋面更大：不再只有帶 recordCase 的
// session 才錄）；session 級的 []WireSegmentRef 聚合是後續 task 的工作。
func (a *App) startCodexHost(w appcore.WSID, host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	conn := host.Conn()
	if approvalPolicy == "" { // M0 驗證定位沿用：commandExecution 一律 requestApproval
		approvalPolicy = "untrusted"
	}
	runner := codex.NewThreadRunner(conn)
	if recordCase != "" { // label-only：留下可觀測軌跡，不影響任何錄流行為
		a.audit("codex_record_label", map[string]any{"wsid": string(w), "label": recordCase})
	}

	// pending start 窗口：登記 → 送 thread/start｜resume → response 抵達 → host
	// publish 完成才解除（見函式 doc 的發布順序）。codexStartMu 讓同一時間至多
	// 一筆 pending，pending 歸屬因此唯一。
	a.codexStartMu.Lock()
	pendingSeq := a.beginCodexPendingStart(w)
	pendingDone := false
	endPending := func() {
		if !pendingDone {
			pendingDone = true
			a.endCodexPendingStart(pendingSeq)
			a.codexStartMu.Unlock()
		}
	}
	defer endPending()

	// 30s 預算在**取得鎖之後**才起算（review Minor）：建在 Lock 之前的話，第二筆
	// start 在鎖上等前一筆 EnsureThread 的時間會全數從自己的預算扣掉，可能連
	// thread/start 都還沒送出就 context deadline exceeded——有界但訊息誤導。
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	threadID, err := runner.EnsureThread(ctx, resume, approvalPolicy)
	if err != nil {
		return "", false, err
	}

	h := &sessionHost{wsid: w, provider: contract.ProviderCodex, sockIndex: -1,
		runner: runner, threadID: threadID, recordLabel: recordCase}
	a.publishCodexHost(h) // 發布：首輪事件的 handler ownership（host ＋ threadId 路由）
	endPending()          // host 已可經 threadId 命中：pending 窗口到此為止

	_, alreadyEnded, err := runner.StartTurn(ctx, prompt)
	if err != nil {
		a.takeHost(h) // rollback：registry 不留半成品
		a.forgetCodexHostRouting(h)
		return "", false, err
	}

	// init envelope（M0 行為保留）：UI 的 sessionId／taskId 來源。此刻 submit
	// 仍 pending → 進 queue，Accept 後依序 flush（user → waiting → init）。
	a.noteWSEmitError("emit", w, a.manager.Emit(w, contract.Event{
		Provider: contract.ProviderCodex, Kind: contract.KindInit,
		SessionID: threadID, Raw: fmt.Appendf(nil, `{"threadId":%q}`, threadID)}))

	// 舊版在這裡起一條 goroutine 等 conn.Done() 收尾 session 錄流；§3.4.4 之後
	// codex 已無 session-scoped 錄流，而 wire EOF（server 死亡）的收尾由
	// GenerationOwner 的 WatchGeneration reaper 負責（§3.4.2），不需要 per-session
	// 的第二個 watcher。
	return threadID, alreadyEnded, nil
}

// codexTeardown：長駐 server 不關（其他 session 還在用同一條 conn）；
// lease.Finalize(Exited=false) 收錄流、撤掉該 host 的路由與 registry 登記，
// 再發 session:done。
//
// take-then-dispose（見 dropHost doc）：先 takeHost 取出——此後沒有新讀者能拿到
// 這個 host——才撤路由與 finalize；順序反過來的話，dispatcher 可能剛好把一個
// frame 路由到一個 lease 已經 finalize 的 host。
//
// host 為 nil：**仍發 session:done**（與 Task 9 之前的 codexTeardown(nil lease)
// 逐字一致）。review 指出這是 UI 可見行為、不在凍結清單內，裁決要求二選一，這裡
// 選「恢復發送」——這條路徑的唯一可達形狀是「Manager slot 仍 phaseActive、但 host
// 已不在 registry」（EndSessionFlow 只在 BeginEndSession 成功時才呼叫 teardown）。
// 那正是 UI 最需要 session:done 的時刻：不發的話，收尾流程會把 slot 收回 idle，
// 而前端那個 session 永遠停在「執行中」。沒有 host 就沒有 lease／track 可收，
// 因此只發事件、回 nil。
func (a *App) codexTeardown(h *sessionHost) error {
	if h == nil {
		a.emitCodexSessionDone("")
		return nil
	}
	a.takeHost(h)
	a.forgetCodexHostRouting(h)
	var err error
	if h.lease != nil {
		err = h.lease.Finalize(ports.Exit{Exited: false}) // 冪等：重複 teardown 不會重複收尾
	}
	h.track.NoteEnded()
	var recErrText string
	if err != nil {
		recErrText = err.Error()
	}
	a.emitCodexSessionDone(recErrText)
	return err
}

// emitCodexSessionDone：codex 收尾的 UI 事件（長駐 server 不隨 session 退出，
// 因此 processStillRunning 恆為 true、stderr 取 live snapshot）。
func (a *App) emitCodexSessionDone(recorderErr string) {
	stderr := ""
	if srv, serr := a.currentAppServer(); serr == nil {
		stderr = srv.StderrSnapshot()
	}
	a.emit("session:done", map[string]any{"provider": "codex",
		"processStillRunning": true, "stderrTail": stderr, "recorderError": recorderErr})
}

// RestartCodexServerRecorded：B1 受控重啟 probe。
//
// M3b §3.4 之後它與 RecoverCodexRecording 是同一件事的兩個入口，共用
// replaceCodexGeneration：錄流不再是 probe-scoped（原本成功前會 StopRecording ＋
// CloseWith），而是交棒給 connection-level 的 always-on wire log，錄到該 server
// 終止為止。recordCase 因此**只剩 label**（進 audit），不再控制 recorder attach
// （§3.4.4）；拒絕條件與復原入口相同（§3.4.7：live host／in-flight turn 一律拒絕）。
func (a *App) RestartCodexServerRecorded(recordCase string) error {
	if err := a.beginAppTxn(); err != nil { // probe 直接操作 codexSingle：同樣入 gate
		return err
	}
	defer a.endAppTxn()
	a.codexServerMu.Lock()
	defer a.codexServerMu.Unlock()
	if err := a.refuseIfCodexLive(); err != nil {
		a.audit("codex_probe_refused", map[string]any{"case": recordCase, "error": err.Error()})
		return err
	}
	a.wireStep("drain")
	if err := a.replaceCodexGeneration(); err != nil {
		a.audit("codex_probe_failed", map[string]any{"case": recordCase, "error": err.Error()})
		return err
	}
	a.audit("codex_probe_ok", map[string]any{"case": recordCase})
	return nil
}

// ---- 官方登入（app 不收密碼、不保管 token）----

func (a *App) AuthStatus(provider string) (string, error) {
	switch provider {
	case "claude":
		out, err := exec.Command(a.claudeCLIPath(), "auth", "status").CombinedOutput()
		return strings.TrimSpace(string(out)), err
	case "codex":
		srv, err := a.ensureAppServer()
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		res, err := srv.Conn().Call(ctx, codex.MethodAccountRead, map[string]any{})
		if err != nil {
			return "", err
		}
		return string(res), nil
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
}

func (a *App) StartLogin(provider string) error {
	switch provider {
	case "codex":
		srv, err := a.ensureAppServer() // 登入與 session 重用同一長駐 server，登入後不重啟
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		res, err := srv.Conn().Call(ctx, codex.MethodAccountLoginStart, map[string]any{"type": "chatgpt"})
		if err != nil {
			return err
		}
		var lr struct {
			LoginID string `json:"loginId"`
			AuthURL string `json:"authUrl"`
		}
		_ = json.Unmarshal(res, &lr)
		a.mu.Lock()
		a.codexLoginID = lr.LoginID
		a.mu.Unlock()
		if lr.AuthURL != "" {
			runtime.BrowserOpenURL(a.ctx, lr.AuthURL)
		}
		a.emit("auth:status", map[string]any{"provider": "codex",
			"event": "login_started", "authUrl": lr.AuthURL})
		return nil
	case "claude":
		// 官方命令 claude auth login 為互動式（fixture claude-auth-help.txt）：
		// 系統終端機 fallback + 每 5s 輪詢 auth status、5 分鐘逾時。
		script := fmt.Sprintf("tell application \"Terminal\" to do script %q",
			a.claudeCLIPath()+" auth login")
		if err := exec.Command("osascript", "-e", "tell application \"Terminal\" to activate").Run(); err != nil {
			return fmt.Errorf("open terminal: %w", err)
		}
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			return fmt.Errorf("launch login in terminal: %w", err)
		}
		a.emit("auth:status", map[string]any{"provider": "claude", "event": "terminal_opened"})
		go a.pollClaudeAuth()
		return nil
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

// CancelLogin 取消進行中的 codex 官方登入（account/login/cancel，schema 必填 loginId）。
func (a *App) CancelLogin(provider string) error {
	if provider != "codex" {
		return fmt.Errorf("cancel login not supported for %q (claude login runs in the system terminal)", provider)
	}
	a.mu.Lock()
	loginID := a.codexLoginID
	a.mu.Unlock()
	if loginID == "" {
		return errors.New("no login in progress")
	}
	srv, err := a.currentAppServer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if _, err := srv.Conn().Call(ctx, codex.MethodAccountLoginCancel, map[string]any{"loginId": loginID}); err != nil {
		return err
	}
	a.mu.Lock()
	a.codexLoginID = ""
	a.mu.Unlock()
	a.emit("auth:status", map[string]any{"provider": "codex", "event": "login_cancelled"})
	return nil
}

func (a *App) pollClaudeAuth() {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		out, err := exec.Command(a.claudeCLIPath(), "auth", "status").Output()
		if err == nil {
			var st struct {
				LoggedIn bool `json:"loggedIn"`
			}
			if json.Unmarshal(out, &st) == nil && st.LoggedIn {
				a.emit("auth:status", map[string]any{"provider": "claude", "event": "logged_in"})
				return
			}
		}
	}
	a.emit("auth:status", map[string]any{"provider": "claude", "event": "login_pending_timeout"})
}

func (a *App) Logout(provider string) error {
	switch provider {
	case "claude":
		out, err := exec.Command(a.claudeCLIPath(), "auth", "logout").CombinedOutput()
		a.emit("auth:status", map[string]any{"provider": "claude",
			"event": "logged_out", "detail": strings.TrimSpace(string(out))})
		return err
	case "codex":
		srv, err := a.ensureAppServer()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		_, err = srv.Conn().Call(ctx, codex.MethodAccountLogout, map[string]any{})
		return err // account/updated 通知會轉 auth:status
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

// ---- M1.5：重啟恢復與 NewSession ----

// failLoudRestore：restore commit 失敗的凍結語意（plan D6 第三輪 P1-2）——
// session 保持 active、呼叫照樣成功，僅以 stream_error fail loud。
func (a *App) failLoudRestore(p contract.Provider, err error) {
	a.emit("workbench:event", contract.Envelope{
		EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: string(p), Kind: string(contract.KindStreamError),
		Error: "restore store: " + err.Error(),
	})
	a.audit("restore_store_error", map[string]any{"provider": string(p), "error": err.Error()})
}

// commitClaudeResume：claude init 抵達時 commit resumeSessionID。guard：
// (1) host 仍是該 WSID 目前登記的 host（late init 於 NewSession 之後 → 指標
// 不符、不寫）
// (2) 該 WSID 的 session 已 accepted（init-before-Accept 只暫存於 host.sessionID，
//
//	由 StartSession Accept 成功後補 commit）。
func (a *App) commitClaudeResume(host *sessionHost, sessionID string) {
	if host == nil || a.hostFor(host.wsid) != host || !a.manager.IsActive(host.wsid) {
		return
	}
	if err := a.restore.CommitSessionID("claude", sessionID); err != nil {
		a.failLoudRestore(contract.ProviderClaude, err)
	}
}

// RestoreViews：啟動時的 view 重放來源（唯讀——不 spawn provider、不回寫 audit）。
func (a *App) RestoreViews() map[string]RestoredView {
	out := map[string]RestoredView{}
	for _, p := range []string{"claude", "codex"} {
		e := a.restore.Get(p)
		out[p] = RestoredView{
			Envelopes:       replayViewWindow(a.eventsPath(), p, e.ViewStartEventID),
			ResumeSessionID: e.ResumeSessionID,
			TaskID:          e.TaskID,
		}
	}
	return out
}

// NewSession：New 專用原子流程（plan D4）。收尾成功才重設恢復視窗；失敗回錯、
// UI 不重設；另一 provider 完全不受影響。resetting phase 涵蓋
// 「teardown → restore reset」整段（期間 Start 回 ErrResetInProgress）。
//
// review P1（fix/lifecycle-app-txn）：整段納入 app transaction（beginAppTxn／
// endAppTxn）——理由與 EndSession 上方 doc 完全相同（同一類 shutdown race，
// NewSession 也是「BeginEndSession 成功才會呼叫 teardown、失敗就直接回錯」，
// 沒有 fallback 兜底重跑，故同樣不需要共用 host.teardownFn）。
func (a *App) NewSession(provider string) error {
	if !knownProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：見 EndSession doc
		return err
	}
	defer a.endAppTxn()
	pv := contract.Provider(provider)

	// 兩個 provider 都已遷入 sessionHost（§3.3）：整段 lifecycle 走同一個 WSID 的
	// 入口。同一個 session 的 start／teardown 必須落在同一個 slot，StartSession
	// 既然由 legacyWSIDFor 解析，這裡也必須用同一個解析結果。
	w := a.legacyWSIDFor(pv)
	host := a.hostFor(w)

	var rtok appcore.ResetToken
	tok, err := a.manager.BeginEndSession(w)
	switch {
	case err == nil: // active session：teardown 後原子轉入 resetting
		if pv == contract.ProviderCodex && host != nil && host.runner != nil &&
			host.runner.ActiveTurnID() != "" {
			cerr := a.manager.CancelEndSession(w, tok)
			return errors.Join(appcore.ErrProviderBusy, cerr)
		}
		var tearErr error
		if pv == contract.ProviderClaude {
			tearErr = a.claudeTeardown(host)()
		} else {
			tearErr = a.codexTeardown(host)
		}
		if tearErr != nil { // 第三輪 P1-2：收尾失敗立即返回——lifecycle 以
			// FinishEndSession 收束回 idle、restore entry 保留、UI 不重設
			finErr := a.manager.FinishEndSession(w, tok)
			return errors.Join(tearErr, finErr)
		}
		rtok, err = a.manager.FinishEndSessionIntoReset(w, tok)
		if err != nil {
			return err
		}
	case errors.Is(err, appcore.ErrNoSession): // 無 active session：直接進 resetting
		rtok, err = a.manager.BeginReset(w)
		if err != nil {
			return err
		}
	default:
		return err
	}

	if h := a.hookDuringReset; h != nil { // 測試 barrier：teardown 完成與 restore reset 之間
		h()
	}
	rerr := a.restore.ResetView(provider, auditHighWatermark(a.eventsPath()))
	finErr := a.manager.FinishReset(w, rtok) // restore 失敗仍 FinishReset 回 idle
	if rerr != nil {
		return errors.Join(rerr, finErr) // 失敗回錯：UI 不重設
	}
	return finErr
}
