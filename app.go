package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"

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
)

type pendingApproval struct {
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

	auditMu sync.Mutex
	auditF  *os.File

	mu              sync.Mutex
	broker          *approval.Broker
	claudeSess      *claude.Session
	claudeSessionID string
	claudePumpDone  <-chan struct{}
	claudeLease     *appcore.RecordingLease
	// claudeTeardownFn：startClaude 為該 session 建的 sync.OnceValue 包裝
	// teardown（見 startClaude 內建立處＋claudeTeardown doc）——自然收尾 reaper
	// 與 forcedShutdown 共用同一份，保證同一個 session 的 CloseSequence 全程
	// 恰好真正執行一次；輸掉 BeginEndSession 競速的一方呼叫它只是阻塞等收斂
	// （bounded by CloseSequence 自身 quiesce/kill timeout），不會重跑第二次。
	claudeTeardownFn func() error

	codexSingle  codex.Single[*codex.Server]
	codexConn    *codex.Conn // wireCodexConn 記錄；interrupt 用（fake wire 測試同路徑）
	runner       *codex.ThreadRunner
	track        appcore.TurnTrack
	codexLease   *appcore.RecordingLease
	codexLoginID string

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
	hookClaudeTeardownBarrier          func() // 測試注入：claudeTeardownFn（shared OnceValue）真正執行開始時
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
	if srv, ok := a.codexSingle.Take(); ok { // 取出即清空 ownership，無後續回填
		_ = srv.Terminate()
		srv.Wait()
	}
	a.mu.Lock()
	br := a.broker
	a.mu.Unlock()
	if br != nil {
		_ = br.Close()
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
// switch 內對應分支）。claudeTeardownFn 是 startClaude 建的 sync.OnceValue：
// 兩條路徑共用同一份，任一方呼叫它都只會執行一次真正的 CloseSequence，另一方
// 只是阻塞等它做完（收斂上限即 CloseSequence 自身 quiesce/kill timeout），不會
// double-Close／double-Terminate／double-Finalize。
func (a *App) forcedShutdown() error {
	a.mu.Lock()
	sess := a.claudeSess
	teardownFn := a.claudeTeardownFn
	runner, klease := a.runner, a.codexLease
	a.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	if sess != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sess.Terminate()                                     // interrupt 先行：加速 CloseSequence quiesce
			if h := a.hookForcedShutdownClaudeBeforeFlow; h != nil { // 測試 barrier：見 App 欄位 doc
				h()
			}
			err := appcore.EndSessionFlow(a.manager, contract.ProviderClaude, nil, teardownFn)
			switch {
			case err == nil:
				// forced shutdown 自己贏得 BeginEndSession、teardown 已完成。
			case errors.Is(err, appcore.ErrEndInProgress):
				if h := a.hookForcedShutdownClaudeBenign; h != nil { // 測試 barrier：見上方 doc
					h()
				}
				if terr := teardownFn(); terr != nil { // 只等收斂；ErrEndInProgress 本身不計入錯誤
					errs[0] = terr
				}
			default:
				terr := teardownFn() // 其餘 lifecycle 擋住：直接收尾兜底（OnceValue 保證冪等）
				errs[0] = errors.Join(err, terr)
			}
		}()
	}
	if runner != nil || klease != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runner != nil && runner.ActiveTurnID() != "" { // interrupt active turn（best effort）
				a.mu.Lock()
				conn := a.codexConn
				a.mu.Unlock()
				if params, perr := a.track.InterruptParams(); perr == nil && conn != nil {
					ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_, _ = conn.Call(ictx, codex.MethodTurnInterrupt, params)
					cancel()
				}
			}
			if err := appcore.EndSessionFlow(a.manager, contract.ProviderCodex, nil, func() error {
				return a.codexTeardown(klease)
			}); err != nil {
				terr := a.codexTeardown(klease) // lifecycle 擋住：直接收（冪等）
				errs[1] = errors.Join(err, terr)
			}
		}()
	}
	wg.Wait() // 兩邊都必須被等待
	return errors.Join(errs[0], errs[1])
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
		if hasRiskClassificationError(errs) { // §3.8 (1)：risk 分類失敗（minimum 無法重算）
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

// hasRiskClassificationError：plan.Validate 的錯誤中是否含 §3.8 (1) 的 risk
// 分類失敗（minimum 重算不符、tier 名稱未知）。plan.Validate 回傳未型別化的
// inline errors，這裡以訊息片段比對——脆弱點已知（validate.go 措辭改動需同步），
// 換 typed error 屬 internal/plan 的 API 擴張，非本 task scope。
func hasRiskClassificationError(errs []error) bool {
	for _, e := range errs {
		msg := e.Error()
		if strings.Contains(msg, "does not match recomputed") ||
			strings.Contains(msg, "unknown minimum_risk_tier") ||
			strings.Contains(msg, "unknown planner_risk_tier") {
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

// RunEvidence 同步執行 planID/taskID 已核可（Gate 2 active）的 TestContract，
// 對 testCommit 的 committed tree 產生一筆 EvidenceRun，回傳 evidence_id。
// kind="negative_control" 時 mutationID 必填，其登記的 patch（RegisterMutation）
// 會被套用；kind="expected_red" 時 mutationID 必須為空（evidence.Run 本身拒絕
// 帶 mutation 的 expected_red，見 runner.go）。
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
func (a *App) RunEvidence(planID, taskID, testCommit, kind, mutationID string) (string, error) {
	if kind != "expected_red" && kind != "negative_control" {
		return "", fmt.Errorf("evidence: unknown kind %q", kind)
	}
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}

	entries, err := a.GateList()
	if err != nil {
		return "", err
	}
	planCommit, ok := activeGate2PlanCommit(entries, planID)
	if !ok {
		return "", fmt.Errorf("evidence: no active Gate 2 approval for plan %q", planID)
	}

	var mutationPatch []byte
	if kind == "negative_control" {
		if mutationID == "" {
			return "", errors.New("evidence: negative_control requires a mutation_id")
		}
		m, merr := a.evidenceJournal.GetMutation(mutationID)
		if merr != nil {
			return "", fmt.Errorf("evidence: load mutation %q: %w", mutationID, merr)
		}
		patch, oerr := evidence.OpenCAS(a.evidenceCASDir, m.Digest)
		if oerr != nil {
			return "", fmt.Errorf("evidence: open mutation patch: %w", oerr)
		}
		mutationPatch = patch
	} else if mutationID != "" {
		return "", errors.New("evidence: expected_red must not carry a mutation_id")
	}

	if err := a.beginAppTxn(); err != nil { // shutdown gate：拒新 run
		return "", err
	}
	defer a.endAppTxn()

	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()

	a.manager.EmitWorkspace(evidenceRunEventKind, nil, map[string]any{
		"phase": "started", "plan_id": planID, "task_id": taskID,
		"kind": kind, "test_commit": testCommit,
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
		"plan_id": planID, "task_id": taskID, "kind": kind,
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
			"核可 "+e.ApprovalID+"（"+e.Record.Gate+" "+e.Record.Subject+"）的綁定已 stale——需修正版重核",
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

// plannerEnforcementKey（§3.8 (9)）：PlannerAssist enforcement probe 失敗的
// condition key。現況 PlanAssist 沒有 enforcement probe 結果 hook（唯讀
// sandbox 由 provider 端 flags 強制、無失敗回報路徑）——觸發點待 probe 接線
// 後補（task-24-report.md 列明），key 與建立函式先凍結在此，不假接。
func plannerEnforcementKey(provider string) string { return "planner-enforcement:" + provider }

// escPlannerEnforcementFailedLocked：(9) 的建立函式（呼叫端持 workflowMu）。
func (a *App) escPlannerEnforcementFailedLocked(provider, detail string) (string, error) {
	return a.escCreateSystemLocked(plannerEnforcementKey(provider), "workspace", false,
		"PlannerAssist enforcement 失敗（"+provider+"）："+detail, "provider:"+provider)
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
//   - ownership 隔離：不寫 a.claudeSess／a.runner／a.codexConn（runner 為獨立 process）；
//     晚到舊 generation 事件（correlation 不符）丟棄並發 stream_error（fail loud）。
//   - once/token 收尾：result／abort／timeout／shutdown 任一先觸發即收一次。
//
// 事件經 Manager.EmitAssist 出口（scope=session、provider、correlation_id、
// purpose="spec_assist"）——保留稽核＋檔案級 event_id，但**不進 provider slot**
// （前端依 purpose 二次分流，不污染 reducer／Chat／totals）。
func (a *App) SpecAssist(provider, purpose, prompt string) (string, error) {
	if provider != "claude" && provider != "codex" {
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
	if provider != "claude" && provider != "codex" {
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
	return gen.correlationID, err
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

func (a *App) registerApproval(id, provider string, resolve func(bool, string) error) {
	a.apprMu.Lock()
	a.apprPending[id] = &pendingApproval{provider: provider, resolve: resolve}
	a.apprMu.Unlock()
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

func (a *App) pumpApprovals(br *approval.Broker, provider string) {
	for req := range br.Pending() {
		id := req.ID
		a.registerApproval(id, provider, func(allow bool, reason string) error {
			behavior := "deny"
			decision := "deny"
			if allow {
				behavior, decision = "allow", "allow"
			}
			err := br.Resolve(id, approval.Decision{Behavior: behavior, Message: reason})
			a.manager.EmitApprovalDecision(contract.ProviderClaude, a.claudeSessionIDSnapshot(), decision, reason)
			return err
		})
		a.manager.EmitApprovalRequest(contract.ProviderClaude, a.claudeSessionIDSnapshot(), req.ToolName, req.Input)
		a.emit("approval:request", map[string]any{
			"id": id, "provider": provider, "toolName": req.ToolName,
			"inputJson": string(req.Input),
		})
	}
}

func (a *App) claudeSessionIDSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claudeSessionID
}

// ---- session 綁定 ----

// StartSession：單一 ownership 交易——BeginNewSessionSubmit 先佔（輸家在建立任何
// process／recorder／pump 之前就失敗）→ provider 同步啟動 → Accept／Reject。
func (a *App) StartSession(provider, prompt, resume, recordCase, taskLabel, approvalPolicy string) error {
	if provider != "claude" && provider != "codex" {
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
	id, err := a.manager.BeginNewSessionSubmit(prov, taskLabel)
	if err != nil {
		return err // ErrSessionActive／ErrSubmitActive 原樣回 UI
	}
	if h := a.hookBeforeProviderStart; h != nil { // 測試 barrier：ownership 已取得、provider 未啟動
		h()
	}
	switch prov {
	case contract.ProviderClaude:
		commit, serr := a.startClaude(prompt, resume, recordCase)
		if serr != nil {
			_ = a.manager.RejectSubmit(prov, id)
			return serr
		}
		if h := a.hookAfterProviderStart; h != nil {
			h()
		}
		aerr := a.manager.AcceptSubmit(prov, id, "", prompt)
		commit(aerr == nil) // 自然結束 goroutine 據此決定走 EndSessionFlow 或直接清理
		if aerr == nil {    // Accept 成功才 commit（staged candidate；D6）
			if cerr := a.restore.CommitResume("claude", a.claudeSessionIDSnapshot(), taskLabel); cerr != nil {
				a.failLoudRestore(contract.ProviderClaude, cerr) // session 保持 active、Start 照樣成功
			}
		}
		return aerr
	default: // codex
		threadID, alreadyEnded, serr := a.startCodex(prompt, resume, recordCase, approvalPolicy)
		if serr != nil {
			_ = a.manager.RejectSubmit(prov, id)
			return serr
		}
		if h := a.hookAfterProviderStart; h != nil {
			h()
		}
		if err := a.manager.AcceptSubmit(prov, id, threadID, prompt); err != nil {
			// 第三輪 P1-5：runner／lease 已發布——Accept 失敗必須回收，
			// 否則 shutdown snapshot 之後才發布的資源會漏收（破壞
			// 「全部 finalize 後才 Manager.Close」保證）
			a.mu.Lock()
			klease := a.codexLease
			a.mu.Unlock()
			terr := a.codexTeardown(klease) // 冪等：清 runner/lease/track＋session:done
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
	if provider != "claude" && provider != "codex" {
		return fmt.Errorf("unknown provider %q", provider)
	}
	pv := contract.Provider(provider)
	a.mu.Lock()
	sess, runner := a.claudeSess, a.runner
	a.mu.Unlock()
	id, err := a.manager.BeginSubmit(pv)
	if err != nil {
		return err
	}
	switch pv {
	case contract.ProviderClaude:
		if sess == nil {
			_ = a.manager.RejectSubmit(pv, id)
			return errors.New("no active claude session")
		}
		if err := sess.Send(prompt); err != nil {
			_ = a.manager.RejectSubmit(pv, id)
			return err
		}
		return a.manager.AcceptSubmit(pv, id, a.claudeSessionIDSnapshot(), prompt)
	default: // codex
		if runner == nil {
			_ = a.manager.RejectSubmit(pv, id)
			return errors.New("no active codex thread")
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		if _, _, err := runner.StartTurn(ctx, prompt); err != nil {
			_ = a.manager.RejectSubmit(pv, id)
			return err
		}
		return a.manager.AcceptSubmit(pv, id, runner.ThreadID(), prompt)
	}
}

// EndSession：指定 provider 的收尾編排（appcore.EndSessionFlow）。冪等；
// ErrProviderBusy 等真實錯誤原樣回 UI。
//
// review P1（fix/lifecycle-app-txn）：整段納入 app transaction（beginAppTxn／
// endAppTxn，沿 app.go:213 慣例，同 StartSession／ensureAppServer／B1 probe）。
// 修前的 gap：EndSession 完全不在 shutdown gate 之內——使用者已呼叫的 EndSession
// 若跟 shutdown() 同時發生，shutdown 的 inflight.Wait() 不會等它，會直接往下讀
// a.claudeSess 進 forcedShutdown；forcedShutdown 見到 BeginEndSession 失敗會
// 套用 ErrEndInProgress benign 規則（review P2），但那條規則的前提是「贏家是
// startClaude 內建的自然收尾 reaper、且與 forcedShutdown 共用同一份
// claudeTeardownFn」——這裡贏家其實是 EndSession 自己建的另一份獨立閉包，
// forcedShutdown 等的 teardownFn 從未被觸發，於是變成對同一個 session 並行跑
// 兩份 CloseSequence、重複發 session:done。納入 app transaction 後這個 race
// window 直接消失：EndSession 在 shuttingDown 之後一律被拒（不會冒出新的
// in-flight teardown）；EndSession 若已在 shuttingDown 之前開始，
// inflight.Wait() 保證等它完整返回（含 teardown／FinishEndSession）才會往下
// 讀 a.claudeSess——forcedShutdown 執行時看到的必是 EndSession 已經收乾淨的
// 狀態，兩者時間上不再重疊。
//
// 沒有讓 EndSession 改共用 claudeTeardownFn（那個 OnceValue 只服務
// forcedShutdown「BeginEndSession 失敗仍重跑 teardown 兜底」的模式）：
// EndSession 本身沒有這種 fallback，appcore.BeginEndSession 失敗時
// EndSessionFlow 根本不會呼叫 teardown、直接把錯誤原樣回給呼叫端，不管閉包
// 是否共用都不會有第二次真正執行——與自然收尾 reaper 競速（跟 shutdown 無關
// 的既有情境，見 fix/shutdown-end-in-progress report concern 3）因此維持原行為
// 未變動，殘留面已知、非本輪修復範圍。
func (a *App) EndSession(provider string) error {
	if provider != "claude" && provider != "codex" {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：見上方 doc
		return err
	}
	defer a.endAppTxn()
	a.mu.Lock()
	sess, done, clease := a.claudeSess, a.claudePumpDone, a.claudeLease
	runner, klease := a.runner, a.codexLease
	a.mu.Unlock()
	if provider == "claude" {
		return appcore.EndSessionFlow(a.manager, contract.ProviderClaude, nil, a.claudeTeardown(sess, done, clease))
	}
	busy := func() bool { return runner != nil && runner.ActiveTurnID() != "" }
	return appcore.EndSessionFlow(a.manager, contract.ProviderCodex, busy, func() error {
		return a.codexTeardown(klease)
	})
}

func (a *App) TerminateSession(provider string) error {
	switch provider {
	case "claude":
		a.mu.Lock()
		sess := a.claudeSess
		a.mu.Unlock()
		if sess == nil {
			return errors.New("no active claude session")
		}
		return sess.Terminate()
	case "codex": // 長駐 server 不關，只中斷 turn
		params, err := a.track.InterruptParams()
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
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
}

// ---- Claude 線 ----

func approvalTimeout() time.Duration { return approval.BrokerTimeout() }

// startClaude 啟動 provider 並回傳 commit callback：呼叫端於 AcceptSubmit 成敗後
// 以 commit(accepted) 通知自然結束 goroutine——快速退出（auth／參數錯誤）時
// goroutine 會等 start 交易 commit/abort 才收尾，不會在 phase=starting 空轉。
func (a *App) startClaude(prompt, resume, recordCase string) (func(accepted bool), error) {
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
	sock := filepath.Join(a.stateDir, "approval.sock")
	_ = os.Remove(sock)
	a.mu.Lock()
	if a.broker != nil {
		_ = a.broker.Close()
	}
	a.mu.Unlock()
	br, err := approval.NewBroker(sock, approvalTimeout(), a.auditWriterFor())
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.broker = br
	a.mu.Unlock()
	committed := false // 未 commit ownership 的 rollback：後續任何失敗都回收 broker
	defer func() {
		if committed {
			return
		}
		_ = br.Close()
		a.mu.Lock()
		if a.broker == br {
			a.broker = nil
		}
		a.mu.Unlock()
	}()
	br.SetTimeoutHook(func(id string) { // 逾時 deny 後收掉 UI 的過期彈窗
		a.apprMu.Lock()
		delete(a.apprPending, id)
		a.apprMu.Unlock()
		a.manager.EmitApprovalDecision(contract.ProviderClaude, a.claudeSessionIDSnapshot(), "timeout", "")
		a.emit("approval:dismiss", map[string]any{"id": id, "cause": "timeout"})
	})
	go a.pumpApprovals(br, "claude")

	self, _ := os.Executable()
	if o := os.Getenv("WORKBENCH_MCP_COMMAND_OVERRIDE"); o != "" { // A6 注入點
		self = o
	}
	mcpCfg := filepath.Join(a.stateDir, "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":%q,"args":["mcp-approval","--socket",%q]}}}`, self, sock)
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

	// pump：錄流 tap ＋ init 綁定 registry → 一律經 Manager.Emit
	done := appcore.Pump(sess.Events(), func(ev contract.Event) {
		if rec != nil {
			if lerr := rec.Line(ev.Raw); lerr != nil {
				a.manager.Emit(contract.Event{Provider: contract.ProviderClaude,
					Kind: contract.KindStreamError, Raw: []byte(lerr.Error()), Err: lerr})
			}
		}
		if info := claude.ParseInit(ev); info != nil {
			_ = a.registry.Bind(info.SessionID, cwd)
			a.mu.Lock()
			a.claudeSessionID = info.SessionID
			a.mu.Unlock()
			a.commitClaudeResume(sess, info.SessionID) // accepted generation 才寫（late init guard）
		}
		a.manager.Emit(ev)
	})

	// claudeTeardownFn：shared sync.OnceValue（review P2）——這個 goroutine
	// 下方的自然收尾 reaper 與 forcedShutdown（見其 doc）都可能對同一個 session
	// 呼叫 teardown；用同一份 memoized 執行保證 CloseSequence 對 sess/lease
	// 全程恰好真正跑一次，另一方呼叫只是阻塞等收斂，不會 double-Close／
	// double-Terminate／double-Finalize。
	teardownFn := sync.OnceValue(a.claudeTeardown(sess, done, lease))

	a.mu.Lock()
	a.claudeSess, a.claudePumpDone, a.claudeLease = sess, done, lease
	a.claudeTeardownFn = teardownFn
	a.claudeSessionID = ""
	a.mu.Unlock()

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
		<-done // committed：等自然結束／崩潰（pump 收乾）再走同一收尾編排
		a.mu.Lock()
		current := a.claudeSess == sess
		a.mu.Unlock()
		if !current { // EndSession 已接手
			return
		}
		if h := a.hookClaudeReaperBeforeEndFlow; h != nil { // 測試 barrier：見 App 欄位 doc
			h()
		}
		if err := appcore.EndSessionFlow(a.manager, contract.ProviderClaude, nil, teardownFn); err != nil {
			a.audit("claude_natural_end_error", map[string]any{"error": err.Error()})
		}
	}()
	committed = true
	return func(accepted bool) { commitCh <- accepted }, nil
}

// claudeTeardown：CloseSequence（close → quiesce → 必要時 terminate → Wait →
// lease.Finalize(ex)），並發 session:done（Exit 為證據）。自然收尾 reaper／
// forcedShutdown 一律經 startClaude 建的 sync.OnceValue（a.claudeTeardownFn）
// 呼叫這個 factory 回傳的閉包，保證兩者對同一個 session 共用同一份真正執行
// ——見 startClaude／forcedShutdown 的 doc（review P2）。EndSession／
// NewSession 仍直接呼叫這個 factory 回傳的新閉包（各自持有排他的
// BeginEndSession token，不在 review P2 描述的競速對之內，刻意未改動，見
// review report concern 3）。
func (a *App) claudeTeardown(sess *claude.Session, done <-chan struct{},
	lease *appcore.RecordingLease) func() error {
	return func() error {
		if sess == nil {
			return errors.New("no active claude session")
		}
		if h := a.hookClaudeTeardownBarrier; h != nil { // 測試 barrier：見 App 欄位 doc；shared OnceValue 唯一一次真正執行的起點
			h()
		}
		fin := func(ex ports.Exit) error {
			if lease != nil {
				return lease.Finalize(ex)
			}
			return nil
		}
		ex, err := appcore.CloseSequence(sess.Close, done, 5*time.Second, 10*time.Second,
			sess.Terminate, sess.Wait, fin)
		a.mu.Lock()
		if a.claudeSess == sess {
			a.claudeSess, a.claudePumpDone, a.claudeLease = nil, nil, nil
			a.claudeTeardownFn = nil
		}
		br := a.broker
		a.broker = nil
		a.mu.Unlock()
		if br != nil {
			_ = br.Close()
		}
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

func (a *App) ensureAppServer() (*codex.Server, error) {
	// server-create 交易：check 與 Ensure 對 shutdown 原子（TOCTOU 關閉）——
	// AuthStatus／StartLogin／Logout 等所有經此入口的路徑一體適用
	if err := a.beginAppTxn(); err != nil {
		return nil, err
	}
	defer a.endAppTxn()
	if h := a.hookInServerTxn; h != nil { // 測試 barrier：交易已登記、Ensure 未開始
		h()
	}
	return a.codexSingle.Ensure(func() (*codex.Server, error) {
		srv, err := codex.StartAppServer(a.ctx, codex.Config{Binary: a.codexCLIPath(),
			CWD: a.workspaceDir, Env: a.childEnv()})
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		if err := srv.Handshake(hctx, clientInfo()); err != nil { // start 失敗不保留 instance（Ensure 契約）
			_ = srv.Terminate()
			srv.Wait()
			return nil, err
		}
		a.wireCodexConn(srv.Conn())
		return srv, nil
	})
}

// currentAppServer 取得既有長駐 server（不重建）。
func (a *App) currentAppServer() (*codex.Server, error) {
	return a.codexSingle.Ensure(func() (*codex.Server, error) {
		return nil, errors.New("codex app-server not running")
	})
}

func (a *App) currentRunner() *codex.ThreadRunner {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runner
}

func (a *App) wireCodexConn(conn *codex.Conn) {
	a.mu.Lock()
	a.codexConn = conn
	a.mu.Unlock()
	conn.OnNotification(func(method string, params json.RawMessage) {
		switch method {
		case codex.MethodAccountLoginCompleted, codex.MethodAccountUpdated:
			a.emit("auth:status", map[string]any{"provider": "codex",
				"event": method, "payload": string(params)})
			a.audit("codex_auth", map[string]any{"method": method, "params": json.RawMessage(params)})
		case codex.MethodTurnStarted:
			a.track.NoteStarted(params) // TerminateSession 需要 turnId
			a.manager.Emit(codex.MapEvent(method, params))
		case codex.MethodTurnCompleted:
			a.track.NoteEnded()
			_, turnID := appcore.ParseTurnStarted(params) // 同 schema：turn.id
			if r := a.currentRunner(); r != nil {
				r.NoteTurnEnded(turnID) // 解 busy；不動 recorder（session-scoped 錄流）
			}
			a.manager.Emit(codex.MapEvent(method, params))
		default:
			a.manager.Emit(codex.MapEvent(method, params))
		}
	})
	conn.OnUnknown(func(raw []byte) {
		a.manager.Emit(contract.Event{Provider: contract.ProviderCodex,
			Kind: contract.KindUnknown, Raw: append([]byte(nil), raw...)})
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
func (a *App) codexApproval(method string, params json.RawMessage) map[string]string {
	id := fmt.Sprintf("codex-%d", time.Now().UnixNano())
	threadID := ""
	if r := a.currentRunner(); r != nil {
		threadID = r.ThreadID()
	}
	type codexDecision struct {
		allow  bool
		reason string
	}
	ch := make(chan codexDecision, 1)
	a.registerApproval(id, "codex", func(allow bool, reason string) error {
		ch <- codexDecision{allow, reason} // reason（如 Esc 的 "esc"）保留進 envelope
		return nil
	})
	a.audit("codex_approval_request", map[string]any{"id": id, "method": method, "raw_params": json.RawMessage(params)})
	a.manager.EmitApprovalRequest(contract.ProviderCodex, threadID, method, params)
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
	a.manager.EmitApprovalDecision(contract.ProviderCodex, threadID, uiDecision, reason)
	a.audit("codex_approval_decision", map[string]any{"id": id, "decision": decision})
	return map[string]string{"decision": decision}
}

// codexHost：startCodexHost 對長駐 server 的最小依賴（fake wire 測試注入點）。
type codexHost interface {
	Conn() *codex.Conn
	Argv() []string
	StderrSnapshot() string
}

func (a *App) startCodex(prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	if a.codexHostOverride != nil { // 測試 seam：fake wire 走同一 production 分支
		return a.startCodexHost(a.codexHostOverride, prompt, resume, recordCase, approvalPolicy)
	}
	srv, err := a.ensureAppServer()
	if err != nil {
		return "", false, err
	}
	return a.startCodexHost(srv, prompt, resume, recordCase, approvalPolicy)
}

// startCodexHost：EnsureThread＋StartTurn bounded synchronous（ctx 30s；turn/start
// response 立即回）。回傳 threadID 供 AcceptSubmit。
//
// runner 於 EnsureThread 成功後、StartTurn 前發布至 a.runner——notification
// handler（turn/completed→NoteTurnEnded、approval→ThreadID）在首輪 response
// 尚未消化時就找得到 runner；completed-before-response 由 earlyEnded latch 對消。
// 後續任何失敗原子 rollback（a.runner 清回 nil）。
func (a *App) startCodexHost(host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	conn := host.Conn()
	if approvalPolicy == "" { // M0 驗證定位沿用：commandExecution 一律 requestApproval
		approvalPolicy = "untrusted"
	}
	runner := codex.NewThreadRunner(conn)
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	// 錄流先於 EnsureThread 開啟——thread/start｜resume 屬 session wire 的一部分，
	// 必須進錄流（W6：codex resume 以 JSON-RPC 錄流佐證）。
	var lease *appcore.RecordingLease
	var rec *recorder.Recorder
	if recordCase != "" {
		var rerr error
		rec, rerr = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".jsonl")
		if rerr != nil { // 可見的 session 失敗，不無聲降級
			return "", false, rerr
		}
		if berr := conn.BeginRecording(rec.Line); berr != nil {
			cerr := rec.CloseWith(recorder.Meta{Provider: "codex", RecordedAt: time.Now().UTC().Format(time.RFC3339)})
			return "", false, errors.Join(berr, cerr)
		}
		lease = appcore.NewRecordingLease(rec, conn.StopRecording,
			func(ports.Exit) recorder.Meta { // 長駐 server 不隨 session 退出：live snapshot、ExitCode nil
				return recorder.Meta{Provider: "codex", CLIVersion: a.cliVersion("codex"),
					Argv: host.Argv(), CWD: a.workspaceDir,
					RecordedAt:          time.Now().UTC().Format(time.RFC3339),
					ProcessStillRunning: true, StderrTail: host.StderrSnapshot()}
			})
	}
	finalizeLease := func() {
		if lease != nil {
			_ = lease.Finalize(ports.Exit{Exited: false})
		}
	}

	threadID, err := runner.EnsureThread(ctx, resume, approvalPolicy)
	if err != nil {
		finalizeLease() // 錄流已開：EnsureThread 失敗須收尾
		return "", false, err
	}

	a.mu.Lock()
	a.runner = runner // 發布：首輪事件的 handler ownership
	a.mu.Unlock()
	rollback := func() {
		finalizeLease()
		a.mu.Lock()
		if a.runner == runner {
			a.runner = nil
		}
		a.mu.Unlock()
	}

	_, alreadyEnded, err := runner.StartTurn(ctx, prompt)
	if err != nil {
		rollback() // 含 finalizeLease
		return "", false, err
	}

	a.mu.Lock()
	a.codexLease = lease
	a.mu.Unlock()

	// init envelope（M0 行為保留）：UI 的 sessionId／taskId 來源。此刻 submit
	// 仍 pending → 進 queue，Accept 後依序 flush（user → waiting → init）。
	a.manager.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindInit,
		SessionID: threadID, Raw: fmt.Appendf(nil, `{"threadId":%q}`, threadID)})

	if lease != nil { // fatal：wire EOF（server 死亡）時仍收尾錄流（冪等由 lease 保證）
		go func() {
			<-conn.Done()
			_ = lease.Finalize(ports.Exit{Exited: false})
		}()
	}
	return threadID, alreadyEnded, nil
}

// codexTeardown：長駐 server 不關；lease.Finalize(Exited=false) 收錄流，
// 清 runner／track 並發 session:done。
func (a *App) codexTeardown(lease *appcore.RecordingLease) error {
	var err error
	if lease != nil {
		err = lease.Finalize(ports.Exit{Exited: false})
	}
	a.mu.Lock()
	a.runner, a.codexLease = nil, nil
	a.mu.Unlock()
	a.track.NoteEnded()
	stderr := ""
	if srv, serr := a.currentAppServer(); serr == nil {
		stderr = srv.StderrSnapshot()
	}
	var recErrText string
	if err != nil {
		recErrText = err.Error()
	}
	a.emit("session:done", map[string]any{"provider": "codex",
		"processStillRunning": true, "stderrTail": stderr, "recorderError": recErrText})
	return err
}

// RestartCodexServerRecorded：B1 受控重啟 probe（薄封裝 codex.RunHandshakeProbe，
// 生命週期 Begin → Handshake → Stop → CloseWith 與四階段失敗處置在 M0 Task 8 以測試固定）。
func (a *App) RestartCodexServerRecorded(recordCase string) error {
	if err := a.beginAppTxn(); err != nil { // probe 直接操作 codexSingle：同樣入 gate
		return err
	}
	defer a.endAppTxn()
	newRec := func() (*recorder.Recorder, error) {
		return recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".jsonl")
	}
	start := func() (*codex.Server, error) {
		return codex.StartAppServer(a.ctx, codex.Config{Binary: a.codexCLIPath(),
			CWD: a.workspaceDir, Env: a.childEnv()})
	}
	err := codex.RunHandshakeProbe(a.ctx, &a.codexSingle, newRec, start, clientInfo())
	if err != nil {
		a.audit("codex_probe_failed", map[string]any{"case": recordCase, "error": err.Error()})
		return err
	}
	srv, serr := a.currentAppServer() // probe 成功必有長駐 server；接上 handlers
	if serr != nil {
		return serr
	}
	a.wireCodexConn(srv.Conn())
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
// (1) sess 仍是目前 session（late init 於 NewSession 之後 → pointer 不符、不寫）
// (2) session 已 accepted（init-before-Accept 只暫存於 claudeSessionID，
//
//	由 StartSession Accept 成功後補 commit）。
func (a *App) commitClaudeResume(sess *claude.Session, sessionID string) {
	a.mu.Lock()
	current := a.claudeSess == sess
	a.mu.Unlock()
	if !current || !a.manager.SessionActive(contract.ProviderClaude) {
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
// 沒有 fallback 兜底重跑，故同樣不需要共用 claudeTeardownFn）。
func (a *App) NewSession(provider string) error {
	if provider != "claude" && provider != "codex" {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err := a.beginAppTxn(); err != nil { // shutdown gate：見 EndSession doc
		return err
	}
	defer a.endAppTxn()
	pv := contract.Provider(provider)
	a.mu.Lock()
	sess, done, clease := a.claudeSess, a.claudePumpDone, a.claudeLease
	runner, klease := a.runner, a.codexLease
	a.mu.Unlock()

	var rtok appcore.ResetToken
	tok, err := a.manager.BeginEndSession(pv)
	switch {
	case err == nil: // active session：teardown 後原子轉入 resetting
		if pv == contract.ProviderCodex && runner != nil && runner.ActiveTurnID() != "" {
			cerr := a.manager.CancelEndSession(pv, tok)
			return errors.Join(appcore.ErrProviderBusy, cerr)
		}
		var tearErr error
		if pv == contract.ProviderClaude {
			tearErr = a.claudeTeardown(sess, done, clease)()
		} else {
			tearErr = a.codexTeardown(klease)
		}
		if tearErr != nil { // 第三輪 P1-2：收尾失敗立即返回——lifecycle 以
			// FinishEndSession 收束回 idle、restore entry 保留、UI 不重設
			finErr := a.manager.FinishEndSession(pv, tok)
			return errors.Join(tearErr, finErr)
		}
		rtok, err = a.manager.FinishEndSessionIntoReset(pv, tok)
		if err != nil {
			return err
		}
	case errors.Is(err, appcore.ErrNoSession): // 無 active session：直接進 resetting
		rtok, err = a.manager.BeginReset(pv)
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
	finErr := a.manager.FinishReset(pv, rtok) // restore 失敗仍 FinishReset 回 idle
	if rerr != nil {
		return errors.Join(rerr, finErr) // 失敗回錯：UI 不重設
	}
	return finErr
}
