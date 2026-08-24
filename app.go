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
	"sync/atomic"
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
	"github.com/slam0504/sdlc-workbench/internal/journal"
	"github.com/slam0504/sdlc-workbench/internal/plan"
	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/proc"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
	"github.com/slam0504/sdlc-workbench/internal/replayindex"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
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
	// startupMu 保護**啟動資訊這一組欄位**：startupErr／startupBlockers／
	// toolsDirPath／toolsSource／nodePath（owner 2026-08-19）。
	//
	// 這不是理論上的競態：這五個欄位在 startup 的 goroutine 上發布，而 CLIInfo()
	// 是 Wails binding——UI 一開視窗就可能在**另一條 goroutine** 上讀它們，
	// startupAfterWriters 正在寫 toolsDirPath／nodePath 的同一刻。
	//
	// 規約：
	//   - 一律經本檔的存取器讀寫，不直接碰欄位（測試的單執行緒前置除外）。
	//   - **鎖內只複製字串，不做 I/O**——node --version／CLI version probe 都要
	//     先取快照、放鎖之後才 exec（見 startupSnapshot 的呼叫端）。
	//   - 鎖序：auditMu → startupMu（noteAuditInvariantBrokenLocked 持 auditMu
	//     時會呼叫 appendStartup）。所以持有 startupMu 期間**不得**呼叫 a.audit()
	//     或任何會取 auditMu 的東西，否則就是反向鎖序。
	// startupData：啟動資訊的**唯一**持有處（見 startupState）。七個欄位與那把鎖
	// 都封在裡面，App 上不再有散落的 startup 欄位——「有沒有持鎖」因此不再是控制
	// 流程分析的問題，而是型別可及性的問題（reviewer 2026-08-20）。
	startupData startupState
	// workspaceSnap／workspaceSrcSnap：workspaceDir／workspaceSrc 的**受鎖副本**。
	//
	// CLIInfo 是唯一容許在 startup 期間執行的 binding（啟動診斷），所以它讀到的
	// 每一個欄位都必須受同一把鎖保護。原生欄位 a.workspaceDir 由
	// acquireStateLease 在啟動途中寫入，其餘讀者都是已進交易閘的 binding——那條
	// 路徑有 shutMu 的 release/acquire 邊，安全；只有 CLIInfo 沒有，所以另外留
	// 一份快照給它（reviewer 2026-08-20）。
	// lease：開啟／持有 state writer 的 ownership capability。
	//
	// **nil ＝沒有 capability ＝一律拒絕**，不是「沒設就當作沒問題」。所有
	// writer 初始化入口都要求出示一份對得上 stateDir 的 lease（見 stateLease
	// 與 openStateWriters）。
	lease *stateLease
	// workspaceErr：resolveWorkspace 的錯誤，由 acquireStateLease 快取（它是
	// canonical state directory 的唯一解析點），startup 讀來 fail loud。
	workspaceErr error
	// startupBlockers：已經插到最前面的第一則 blocker（見 appendStartup）。
	// 只用來判斷「要不要再插一次」——若每一則 blocker 都前插，多則之間的順序會
	// 變成反向（後到的排最前）。
	//
	// **已知代價**：第二則之後的 blocker 會排在既有的良性 warning **後面**，
	// 也就是嚴重度排序對它們不成立。目前不可達（兩個 blocker 呼叫點在同一次
	// 啟動中互斥：registry 載入失敗會直接 return，走不到 backfill），所以刻意
	// 不為此加一份 blocker／warning 分開累積的資料結構。新增第三個 blocker
	// 呼叫點時要重新檢查這個前提。
	diagramPath string

	registry *claude.Registry
	manager  *appcore.Manager
	restore  *restoreStore

	// M3b §3.5：per-WSID turn 索引（Task 15-19 的 replayindex.Index）。nil 代
	// 表這次啟動沒有可用的 index——index 只是快取，缺它不影響 audit 權威性，
	// 但 §3.8 的視窗化載入與 §3.2.3 的 incomplete turn 偵測會一併停用（兩者
	// 都以 index 為唯一來源，不另做一份掃描實作）。
	replayIndex *replayindex.Index

	// indexUnverified：啟動期 index 未通過驗證的 latch——涵蓋兩種來源：
	// (a) VerifyOrRebuild 執行後失敗；(b) registry 載入失敗讓 restoreSessions
	// 提前 return，`index_verify` 根本沒跑到（見 restoreSessions 的 early return）。
	// 兩者留下的都是「index 從未與 audit 對齊」，後果相同。
	//
	// 為什麼「失敗就不能再信」而不是「失敗但還能用」：verifyOrRebuildLocked
	// 是**邊做邊改狀態**的——先清 checkpoint／turns、必要時 quarantine turn
	// files，最後才 rescanFromLocked。若它在 rescan 中途失敗，記憶體裡留下的是
	// 「掃到一半」的狀態；接著第一筆 live 事件進 Observe 就會把 checkpointOffset
	// 直接推到該筆的 receipt.EndOffset，**跳過中間那段沒掃到的 audit**——那段
	// 的 turn record 從此沒人會補（下次啟動的 rescanFrom 也是從新 checkpoint
	// 起掃），index 出現靜默且永久的缺口，而 LoadTurnsBefore 只會少回幾個
	// turn、不會回錯。
	//
	// 所以這裡 latch 成「不可信」，讓 LoadTurnsBefore fail loud 並提示重啟。
	// 刻意**不**排程 runtime 重建：RuntimeRebuild 的 bulkRebuild 永遠從
	// max(rebuildCursor, checkpointOffset) 起掃，而此刻那個值本身就是毒的，
	// 重建只會把缺口固化。需要的是「reset turn files ＋從 offset 0 全量」語意，
	// 那條路徑目前只有啟動期的 VerifyOrRebuild 有。
	//
	// atomic：寫在啟動序列（單執行緒），讀在 binding（Wails 另一條 goroutine）。
	indexUnverified atomic.Bool

	// eventSink：events.jsonl 的具體 sink。Manager 只需要 appcore.AuditSink，
	// 這裡另外留一份具體型別**只為了 End()**——replayindex 的 auditEndFunc 契
	// 約要求「不得取 emit mutex、且不持鎖時也能安全讀」，唯一滿足的來源就是
	// sink 內部那個 atomic offset（見 appcore.JSONLSink.End）。開檔失敗時為
	// nil，此時不接 index。
	eventSink *appcore.JSONLSink

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

	// crTokenMu／crTokens：Remove × New 共用的 provider-scoped ownership token
	// （§3.6.2）——CreateSession 與 RemoveSession 對同一 provider 互斥序列化，
	// 兩者的「持有期間」分別涵蓋各自整段編排（Reserve→Commit／deny_approvals→
	// decrement_count），而不只是其中一步，否則兩者仍可能交錯看到半完成狀態。
	//
	// 鎖序（凍結；避免 plan 階段抓到的那個 deadlock）：token 一律是兩條路徑
	// 最先取得的鎖，且在成功取得之前兩者都不持有任何其他 App 內部鎖
	// （a.mu／apprMu／manager 內部 mutex／createDegradedMu 皆不會在等 token 期間
	// 被持有）——因此不存在「A 等 B 持有的鎖、B 等 A 持有的鎖」的反向鎖序，
	// 兩條路徑只可能互相等待這一把鎖本身，不會死鎖。crTokenMu 只保護 map 本身
	// 的建立，不是要序列化的那把鎖，取得後立即釋放，不跨臨界區持有。
	//
	// 持有時間上界（review round-2 Minor #3）：RemoveSession 的 teardown
	// （claudeTeardown 的 CloseSequence）跑在 token 臨界區內，一次移除最長可能
	// 卡住同一 provider 的 CreateSession／另一個 RemoveSession 達 quiesce(5s)+
	// kill(10s) ≈ 15s。這是 §3.6.2「token 涵蓋整段編排」的必然代價，不是 bug；
	// token 不是輕量互斥，呼叫端（含未來新增的入口）不得假設它總是瞬間釋放。
	crTokenMu sync.Mutex
	crTokens  map[contract.Provider]*sync.Mutex

	// removeTokenHeld：測試專用——RemoveSession 目前是否持有 provider token
	// （TestRemoveXNewShareOwnershipToken 用來斷言 Create 不會在 Remove 持有
	// 期間搶到）。由 removeTokenHeldForTest 讀取；production 無讀者。
	removeTokenHeld atomic.Bool

	// hookRemoveStep：測試注入——RemoveSession §3.6.2 凍結順序的探針（同
	// startupStep／wireStep 慣例：步驟名代表「跑到了」，不代表每一步都做了新
	// 工作——lease_finalize 對 claude 是 teardown 內已完成的檢查點、對 codex
	// 無 session-scoped lease 故恆為 no-op）。production 恆為 nil。
	hookRemoveStep func(step string)

	// hookShutdownStep：測試注入——shutdown §3.6.5 凍結總序的探針。步驟名一律在
	// 該步的工作**完成之後**發出，兩個例外都寫在發出點旁邊：teardown_parallel
	// 標記「並行階段開始」（必須早於任何 per-host hook 才有確定性順序），
	// server_terminate_wait 標記「進入共用 app-server 收尾」——它與 wirelog_finalize
	// 夾住同一個 GenerationOwner.FinalizeWith 呼叫（terminate → wait → drain →
	// detach → finalize 是它內部的凍結順序，見 internal/codex/owner.go 與其
	// owner_test.go），App 層看得到的只有「進入」與「已完成」兩個邊界。
	// production 恆為 nil。
	hookShutdownStep func(step string)

	// hookTeardownEntered／hookTeardownDone：測試注入——每個 per-session teardown
	// goroutine 的進場／收斂點（Claude 與 Codex 共用）。並行性 barrier 用它而不是
	// CloseSequence 的 timer：Codex 四個 session 共用同一條 conn 與同一個 app-server，
	// 本就不會、也不該產生 per-host 的 CloseSequence timer。
	//
	// 這兩個 hook 由各自的 teardown goroutine 呼叫，與 hookShutdownStep（shutdown
	// 主 goroutine）不同 goroutine——同時記錄兩者的測試必須自行加鎖。production 恆為 nil。
	hookTeardownEntered func(w appcore.WSID)
	hookTeardownDone    func(w appcore.WSID)

	// hookRemoveHoldingToken：測試注入——RemoveSession 取得 provider token 之後
	// 呼叫，用來確定性地讓測試把 Remove 卡在「持有 token、尚未完成」的窗口。
	// hookCreateWaitingForToken／hookCreateAcquiredToken：CreateSession 對應的
	// 等待／取得探針。三者皆 production 恆為 nil。
	hookRemoveHoldingToken    func()
	hookCreateWaitingForToken func()
	hookCreateAcquiredToken   func()

	auditMu sync.Mutex
	auditF  *os.File
	// auditState：稽核寫入器的生命週期（owner 2026-08-18）。不要只靠
	// `auditF != nil` 猜狀態——那個判斷把「還沒取得 lease，本來就不該寫」與
	// 「已經 ready 卻寫不出去」壓成同一件事，而後者是不變量破壞。
	//
	//	auditUnavailable → 尚未取得 lease／啟動被拒，**或**收尾已在釋放 lease 之前
	//	                   收掉 writer。兩者都不該寫 state audit，丟棄是正確行為；
	//	                   拒絕原因由 startupErr／stderr／UI 呈現，不在這裡重複。
	//	auditReady       → 已取得 lease 且 writer 開啟成功。此後 auditF 為 nil 是
	//	                   不變量破壞，必須 fail loud（見 audit）。
	//
	// 只有兩態，理由見 auditLifecycle 常數區塊。
	auditState auditLifecycle
	// auditBrokenNoted：不變量破壞的橫幅／stderr 已出過一次（去重，見
	// noteAuditInvariantBrokenLocked）。
	auditBrokenNoted bool
	// hookAudit：測試注入，在 audit 寫入之前於**呼叫端的 goroutine** 上同步執行。
	// 唯一用途是驗證「某筆稽核是同步寫的」——把 a.audit 換成 go a.audit 之後，hook
	// 會跑在別的 goroutine 上，stack 裡就不會有呼叫端。production 恆為 nil。
	hookAudit func(kind string)

	// mu：sessionHosts registry ＋ codex dispatcher 索引的互斥。
	//
	// **規約（§3.4.3 之後升級成 liveness 前提）：mu 底下不得做任何會阻塞的工作**
	// ——不等 channel、不做 I/O、不呼叫 codex（既有慣例是「取 mu 讀出 conn → 放鎖 →
	// 才 Call」，見 forcedShutdown 與 InterruptTurn）。理由從「避免長臨界區」變成
	// 「否則 codex readLoop 會停住」：frame 歸屬判定（resolveWireFrameWSID）是在
	// codex.Conn 的 readLoop goroutine 上取這把鎖的，鎖一卡住就沒有任何 s2c frame
	// 進得來，整條連線的所有 session 一起停。
	//
	// **這條規約目前只有這段註解，沒有守門測試**（review 掃過所有臨界區確認現況合規）。
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
	wireRun string // 本次 app 執行的 wire_log_id run token（見 newWireGeneration）

	// wireSegments（§3.4.4）：session 級錄流證據——每個 WSID 一份有序的
	// []SegmentRef，讓同一 WSID 在 B1 受控 restart、server 意外死亡或 **app
	// 重啟**之後仍能跨 generation 延續（單一 wire_log_id 涵蓋不了）。
	//
	// 只在 openWireSegments（startup）寫一次，之後只讀；型別自帶鎖，故不受
	// wireMu 保護。nil 代表開檔失敗（已 fail loud，見 openWireSegments）——此時
	// 不記段，其餘功能不受影響。
	//
	// 刻意獨立成 wire-logs/segments.jsonl 而不寫進 session registry：registry 是
	// mutable current-state ＋ 受 uncertain latch 約束（latch 期間一律不寫），而
	// SegmentRef 是「這個 WSID 曾擁有這段 frame range」的只增事實，被 latch 擋掉
	// 就是永久的證據缺口。形狀與 internal/evidence／internal/gate 的 journal 同款。
	wireSegments *wirelog.SegmentSet

	// wireReqMu／wireReqWSID（§3.4.3 frame-level 歸屬）：帶 request id 的 frame 的
	// 歸屬，供**方向相反的**那筆 response frame 繼承。key 是 "<request 的方向>:<id
	// 原文>"——c2s 與 s2c 各自獨立配發 request id，同一數值在兩個方向是完全不同的
	// RPC 交換（FrameKey 要求含 direction 是同一個理由）。
	//
	// 為什麼 response 不能靠 identity 判定：response frame 只有 {id, result|error}，
	// params 裡的 threadId／turnId 都不在裡面。completed-before-response 尤其明顯——
	// turn/start 的 response 抵達時該 turn 早已 unbind，identity 查回去必然落空。
	//
	// 生命週期：每配一個新 generation 就整個清掉（newWireGeneration）——request id
	// 由 codex.Conn 從 1 起算，換 conn 就換一套編號；沒有回應的請求（逾時、連線死亡）
	// 的殘留登記也在此一併釋放。
	wireReqMu   sync.Mutex
	wireReqWSID map[string]string

	// ---- §3.4.3 歷史歸屬展開的背景 worker（owner 2026-08-18 契約，見 wire_frames.go）----
	//
	// wireJobMu 保護佇列／journal handle／worker 的三條 channel 與 busy 旗標。
	// wireJobSig 非 nil 即代表 worker 已啟動（懶啟動，見 ensureWireFrameWorker）。
	wireJobMu    sync.Mutex
	wireJobJ     *journal.Journal
	wireJobQueue []wireFrameJob
	wireJobBusy  bool
	wireJobSig   chan struct{}
	wireJobStop  chan struct{}
	wireJobExit  chan struct{}
	// wireJobDrain：shutdown 的 bounded drain 窗口（nil＝production 預設）。測試設
	// 0 即「不等」——收尾時間因此與待辦數量無關，斷言不必靠牆鐘。
	wireJobDrain *time.Duration

	// wireIdxCache：wire_log_id → 該代的歸屬（或讀不出來的原因）。契約第 3 條的
	// 「一個 app run 最多重建一次、多個 session 共用」就是這一格。
	wireIdxMu    sync.Mutex
	wireIdxCache map[string]wireGenAttr

	// hookWireIndexLoad：測試注入的 barrier／計數點。production 恆為 nil。這是
	// **唯一**真的碰磁碟取歷史歸屬的地方，因此也是「一個 app run 只讀一次」的計數點。
	hookWireIndexLoad func(wireLogID string)

	// wireOpenSegs：wire_log_id → 目前在該 generation 上開著 segment 的 host。
	// 在 wireMu 下讀寫。只服務一件事——判定「這一段期間有沒有別的 session 也在
	// 同一代上活著」，好在證據出口標明 range 非排他（見 closeWireSegment）。
	wireOpenSegs map[string]map[appcore.WSID]*sessionHost

	// hookWireStep：測試注入——受控復原／replacement 的步驟順序探針（§3.4.7 的
	// 順序是凍結契約）。production 恆為 nil。
	hookWireStep func(step string)

	// hookAfterCodexPublish：測試注入——RunOwnedHandshake 已返回、
	// replaceCodexGeneration 讀 Single.Current() 之前的 barrier。用來確定性地
	// 重現「新 server 在發布之後、被讀取之前就死亡」——那個窗口內 owner 自己的
	// watcher 會以 CompareAndTakeEpoch 搶先取走它，Current() 因此回 ok=false 而
	// RunOwnedHandshake 卻回 nil。production 恆為 nil。
	hookAfterCodexPublish func()

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

	apprMu      sync.Mutex
	apprPending map[string]*pendingApproval
	// apprOrder：apprPending 的登記順序（§3.6.4：多筆待核可 FIFO promotion）——
	// map 迭代順序不保證，promotionOrder 靠這份 slice 回答「該先顯示哪一筆」。
	// 在 apprMu 下與 apprPending 同步增刪（見 registerApproval／unregisterApproval）。
	apprOrder []string

	// shutdown gate（第四輪 review P1）：shutdown 先拒新 StartSession、等已取得
	// start ownership 的交易 accept／abort 完成，才 snapshot／teardown／Close／Take——
	// 堵住「Take() 之後 Ensure() 重新回填 server」的窗口。
	shutMu sync.Mutex
	// phase：app 的 lifecycle 狀態（見 appPhase）。**與 inflight 計數共用
	// shutMu**，所以「檢查 phase ＋ 登記交易」是原子的——binding 不可能在
	// startup 尚未完成或已被判定 blocked 的情況下擠進來（reviewer 2026-08-19 P1：
	// Wails 在 macOS 會並行執行 OnStartup 與 bindings，先前的 latch 是另一把鎖，
	// 於是 binding 通過閘門之後 startup 才發現衝突，寫入照樣落地）。
	phase    appPhase
	inflight sync.WaitGroup
	// startupRunning／startupDone：startup 自己的 lifecycle ownership。
	//
	// 為什麼 startup 也要被管（reviewer 2026-08-19 P1）：phase 擋得住**新的
	// binding**，擋不住 startup 自己——實測「startup 停在 startupEvidence 之前 →
	// shutdown 完整跑完並釋放 lease → 放行 startup → startup 繼續建立
	// evidence/evidence.jsonl」，寫入落在 lease 之外，核心不變量因此不成立。
	//
	// 處理原則與背景 worker 一致：shutdown bounded 等它停；等不到就**不釋放
	// lease**，讓 process 帶著鎖結束（見 shutdown 第 13 步）。
	startupRunning bool
	startupDone    chan struct{}
	// startupStarted：**曾經**取得過 startup ownership。永久旗標，不隨結束重設
	// ——ownership 只發一次（reviewer 2026-08-20）：先前只看「目前是否 running」，
	// 於是 begin → end → begin 的第二次呼叫仍然成立，會重新開啟並覆寫 writers、
	// Manager 與 channel。startup 是一次性的啟動序列，不是可重入的操作。
	startupStarted bool
	// startupDrain：shutdown 等 startup 收斂的窗口（nil＝production 預設）。
	// 測試設短值即可確定性地量到「等不到就保留 lease」那條路徑。
	startupDrain *time.Duration
	// stateBlocked：state 操作被永久擋下的原因（空字串＝沒有）。**與 phase 同一
	// 把鎖**（shutMu）：兩者分開設會有一段時間窗，phase 已 blocked 但原因還沒
	// 寫進去，使用者拿到的是泛用的「尚未完成啟動」而不是真正的原因
	// （reviewer 2026-08-19）。
	stateBlocked string

	// ---- replay index 的 runtime 重建排程（§3.5.7；rebuild_orchestrator.go）----
	//
	// rebuildMu 保護以下六個欄位。single-flight 由 rebuildRunning 保證：
	// replayindex.RuntimeRebuild 自己也會對重入回 ErrRebuildInProgress，但
	// 「不得疊呼叫」的責任在排程這一側——ErrRebuildNotConverged 的 backoff 重
	// 試若每次 degraded 通知都另起一條，會直接疊出多條互相回錯的重試鏈。
	rebuildMu      sync.Mutex
	rebuildRunning bool
	rebuildClosed  bool // cancelRebuild 已執行：排程入口自此關閉（見 scheduleRebuild）
	rebuildCancel  context.CancelFunc
	rebuildDone    chan struct{}   // 本輪重試迴圈結束時關閉（shutdown 的等待點）
	rebuildStarts  int             // RuntimeRebuild 實際被呼叫的次數（測試斷言用）
	rebuildDelays  []time.Duration // 每次未收斂之後實際等待的 backoff（同上）

	// rebuildBackoffBase：backoff 首段長度（0 = defaultRebuildBackoffBase）。
	// 測試注入小值，production 不設。
	rebuildBackoffBase time.Duration

	// hookStartupStep／hookRebuildEntered／hookRebuildResult：測試注入。
	// hookStartupStep 是 §3.2.4 凍結啟動順序的探針；hookRebuildEntered 落在
	// 每次 RuntimeRebuild 呼叫之前；hookRebuildResult 非 nil 時**取代**真正的
	// RuntimeRebuild（參數是本輪第幾次嘗試，1-based），用來驅動未收斂→backoff
	// →成功這條時序而不必真的把 audit 灌到不收斂。production 恆為 nil。
	hookStartupStep    func(step string)
	hookRebuildEntered func()
	hookRebuildResult  func(attempt int) error

	// hookStartupPublish：測試注入——startupAfterWriters **正在發布啟動資訊的
	// 中途**（tools dir 已寫入、node path 還沒），用來把 CLIInfo() 的併發呼叫確定
	// 性地擠進那個窗口，證明讀寫走同一套同步機制（見 App.startupMu）。刻意不放在
	// 兩次發布的臨界區內部：那樣只會做出一個必然死鎖的 barrier，量不到任何東西。
	// production 恆為 nil。
	hookStartupPublish func()

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

	// afterFn：claudeTeardown 的 appcore.CloseSequence 等待來源，nil = appcore.RealAfter
	// （production）。測試覆寫成受控 timer，讓 shutdown quiesce/kill 逾時測試不必依賴牆鐘
	// （Task 21；為 Task 24 bounded-window barrier 鋪路）。
	afterFn appcore.After

	// Gate 1（M2 Stage A：spec §3.5／§5.4）——spec.GitRepo ＋ gate.Service，
	// ensureGate() 惰性初始化，journal 落在 **a.stateDir** 的 gate.jsonl
	// （gitignored app state）。綁 stateDir 而不是 workspaceDir/.workbench 的理由、
	// 以及舊路徑殘留的處置，見 ensureGate 與 migrateLegacyState 的 doc。
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
	// 即收一次（清 active flag＋endTxn＋close done）。
	assistMu     sync.Mutex
	assistActive map[string]*assistGen
	// procRootCtx／procRootCancel：所有 assist 執行的**根 context**，由
	// shutdown 在第 1 步 cancel（見 reclaimAssists）。
	//
	// 為什麼需要它（reviewer 2026-08-20）：assist 的實作原本自己開一筆交易，而且
	// 刻意排在 assistActive 登記**之後**——因為 shutdown 的 reclaim 掃的是那個
	// registry，若交易先登記、gen 還看不見，reclaim 會掃到空集合、cancel 不到，
	// inflight.Wait 卻要等到 assistTimeout（約 3 分鐘的 stall）。
	//
	// 改成薄包裝之後，交易在 binding 進場時就登記了，那個順序前提不再成立。與其
	// 讓內外兩層交易競逐（外層取得之後 phase 可能翻成 shuttingDown，內層隨即以
	// 「shutting down」拒絕，操作做到一半才失敗），不如把 liveness 從 registry
	// 掃描改成 context 樹：shutdown 一 cancel 根 context，**不論登記到哪一步**，
	// 進行中的 assist 都會立刻收斂。registry 掃描退為補強。
	procRootCtx         context.Context
	procRootCancel      context.CancelFunc
	assistRunnerFactory func(provider string) (assist.Runner, error) // 測試注入：換 fake Runner
	hookAssistBeforeTxn func()                                       // 測試注入：gen 已入 assistActive、beginTxn 未開始

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
	// evidenceCASDir／evidenceRegistryPath 於 startup 建立於受 ownership lease
	// 保護的 <stateDir>/evidence/ 下（journal＝evidence.jsonl，worktree registry＝
	// worktrees.jsonl，同時做一次 CleanupOrphans／CleanOrphanTemps）。
	evidenceMu           sync.Mutex
	evidenceActive       map[string]context.CancelFunc
	evidenceJournal      *evidence.Journal
	evidenceCASDir       string
	evidenceRegistryPath string

	// evidenceContextLoaderOverride：測試注入，換掉 RunEvidence 傳給
	// evidence.Run 的 evidence.ContextLoader（production 用 a.planLoader）。
	// 唯一用途是讓測試能在 LoadAt／LoadOracleAt（ulid mint 之前執行）安插一個
	// barrier，重現「beginTxn 成功到 evidenceActive 登記之間」的 TOCTOU
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
	// beginTxn 成功後、workflowMu.Lock 前觸發——刻意早於 Lock（而非沿
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

// beginTxn：**唯一**的交易閘。所有會改動 durable state 的 Wails binding 都從這裡
// 進場，成功時呼叫端必須 defer endTxn()。
//
// 為什麼只有一個閘（reviewer 2026-08-20）：先前分成 beginTxn（只擋
// shuttingDown）與 beginTxn（擋非 ready），於是
//
//   - `NewApp().StartSession()` 在 startup 之前通過 beginTxn，接著因 Manager
//     尚未初始化而 panic；
//   - SendMessage／ResolveApproval 這類「不碰 gate 欄位、但會經 Manager 寫
//     events.jsonl 與 replay index」的操作根本沒有閘，收尾後仍寫得進去。
//
// 兩個閘就是兩套判準，而要守的不變量只有一個：**lease 之外不得有 durable
// 寫入**。所以收斂成一個，phase 判定也只有一份。
//
// 「檢查 lifecycle ＋ 登記 in-flight 交易」在同一個 shutMu 臨界區內完成：
//
//	(1) 交易登記 → shutdown 的 inflight.Wait() 會等這一筆做完才往下走。
//	(2) lifecycle → 只有 ready 放行。Wails 在 macOS 會**並行**執行 OnStartup 與
//	    bindings，用另一把鎖上的 latch 擋不住「binding 先過閘、startup 隨後才發現
//	    衝突」這個順序。
//
// **只掛在 exported binding 上**，內部呼叫走各自的 unexported 版本——巢狀的
// begin/end 雖然計數上安全，但 phase 可能在內外兩次之間翻成 shuttingDown，於是
// 操作做到一半才失敗，那是新造出來的失敗模式。
//
// 涵蓋範圍不靠手寫清單維護：見 app_binding_surface_test.go 的結構守門。
func (a *App) beginTxn() error {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	switch a.phase {
	case phaseReady:
		a.inflight.Add(1)
		return nil
	case phaseBlocked:
		return errors.New(a.stateBlockedReasonLocked())
	case phaseShuttingDown:
		return errors.New("app shutting down")
	default:
		return errors.New("app 尚未完成啟動：在啟動序列完成之前不受理任何會改動狀態的操作")
	}
}

func (a *App) endTxn() { a.inflight.Done() }

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
	// per-WSID durable metadata writer（M3b spec §3.2.1 白名單的 resume
	// identity／task label／view boundary 三項）
	CommitResume(wsid, resumeSessionID, taskLabel string) error
	SetResume(wsid, resumeSessionID string) error
	ResetView(wsid, viewStartEventID string) error
	// ClearLegacyTranscript：§6a 窄寫入——只清 LegacyTranscript 旗標，不動
	// 其他欄位；冪等（flag 已 false 不落盤）。
	ClearLegacyTranscript(wsid string) error
	// pane pins／focused pane（§3.2.1 白名單的最後兩項，見 PaneLayout／SetPaneLayout）
	SetLayout(l wsregistry.Layout) error
	Layout() wsregistry.Layout
	// Uncertain：registry 的上一次 commit 結果是否不確定（見 registryUncertain）。
	Uncertain() bool
}

var _ sessionRegistry = (*wsregistry.Store)(nil)

// errCreateDegraded：該 provider 的建立路徑已進入 degraded latch。刻意沒有
// in-process 解除路徑——見 setCreateDegraded。
var errCreateDegraded = errors.New("app: session create degraded（需重啟 app 復原）")

// errNoSessionRegistry：registry 尚未載入就呼叫 CreateSession。理論上不會發生
// （啟動流程先載入 registry 才開放 UI），但 nil 介面直接呼叫會 panic 在
// ReserveSession 之後、名額已被佔走的位置——fail loud 早退比 panic 洩名額好。
var errNoSessionRegistry = errors.New("app: session registry not loaded")

// errRegistryUncertain：registry 的上一次寫入 commit 結果不確定
// （wsregistry.ErrRegistryUncertain：檔案已 rename、parent directory sync 失敗）。
//
// 與另外兩個 latch 的語意差異見 wsregistry.ErrRegistryUncertain 的 doc。這裡只
// 補 app 側的處置：**拒絕範圍是 mutation ＋ 會建立／銷毀 durable session 身分的
// lifecycle，讀取一律放行**。
//
// 為什麼讀取放行：uncertain 的是「上一次寫入有沒有 durable」，不是「記憶體內容
// 有沒有被破壞」。已載入的記憶體資料仍然是這個 process 一路看下來的狀態，擋掉
// 讀取只會讓 UI 在最需要說明現況的時候變成空白，使用者連「發生了什麼」都看不到。
//
// 為什麼 mutation／lifecycle 拒絕：這些操作的正確性建立在「registry 寫得進去」
// 之上——建立一個寫不進去的 session、移除一個 tombstone 可能沒落盤的 session、
// 或起一個續聊身分無法被記下來的對話，都會在重啟後變成使用者分辨不出來的錯誤
// 狀態（ghost session／復活的已移除 session／接回舊對話）。
//
// 單向，沒有 in-process 解除路徑：重啟時 Open 讀磁碟上實際的內容，那就是復原
// 路徑（新的 Store 不帶 latch）。
var errRegistryUncertain = fmt.Errorf(
	"app: session registry 上一次寫入的結果不確定，建立／移除／開始對話／開新對話已停用；請重啟 app（重啟後 registry 以磁碟上的 workspace-sessions.json 為準重新載入）：%w",
	wsregistry.ErrRegistryUncertain)

// registryUncertain：registry 是否已進入 uncertain latch。registry 未接線時回
// false——那是 errNoSessionRegistry 的範圍，兩個錯誤要能分辨。
func (a *App) registryUncertain() bool {
	return a.wsReg != nil && a.wsReg.Uncertain()
}

// noteRegistryUncertainErr：**任一** registry 寫入回 ErrRegistryUncertain 時的
// 統一稽核，原樣回傳 err 讓呼叫端直接 `return`／繼續既有錯誤處置。
//
// rev2 review I2：這個標籤原本只掛在 noteRegistryWriteResult（per-WSID writer）
// 上，但 latch 可能由**任何**寫入首次設下——CreateSession 的 Put、NewSession 的
// ResetView、RemoveSession 的 Remove、啟動期 backfill、shutdown 的 Sync。
// 少了那些呼叫點，post-mortem 讀 audit 時答不出「latch 是何時、被哪一次寫入
// 設下的」，只看得到之後一連串被拒絕的操作。
//
// 只記錄、不改變控制流：latch 之後的處置各入口不同（早退／回錯／跳過），
// 集中在這裡反而會把那些差異抹平。
func (a *App) noteRegistryUncertainErr(op, wsid string, err error) error {
	if errors.Is(err, wsregistry.ErrRegistryUncertain) {
		a.audit("session_registry_uncertain", map[string]any{
			"op": op, "wsid": wsid, "error": err.Error()})
	}
	return err
}

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

// crToken：Remove × New 共用的 provider-scoped ownership token（見 crTokens
// 欄位 doc 的鎖序凍結）。惰性建立，crTokenMu 只保護 map 本身、不是要序列化的
// 那把鎖。
func (a *App) crToken(p contract.Provider) *sync.Mutex {
	a.crTokenMu.Lock()
	defer a.crTokenMu.Unlock()
	if a.crTokens == nil {
		a.crTokens = map[contract.Provider]*sync.Mutex{}
	}
	tok, ok := a.crTokens[p]
	if !ok {
		tok = &sync.Mutex{}
		a.crTokens[p] = tok
	}
	return tok
}

// removeTokenHeldForTest：測試專用（見 removeTokenHeld 欄位 doc）。
func (a *App) removeTokenHeldForTest() bool { return a.removeTokenHeld.Load() }

// CreateSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) CreateSession(provider, taskLabel string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.createSession(provider, taskLabel)
}

// CreateSession 建立一個新的 workspace session，回傳其 WSID（純新增 binding；
// 既有 provider-keyed 的 StartSession／SendMessage／EndSession 不受影響）。
//
// 編排順序凍結（§3.1）：beginTxn → ReserveSession → wsReg.Put ＋ atomic
// persist → CommitCreate → endTxn。registry 先於 CommitCreate 落盤，因為
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
func (a *App) createSession(provider, taskLabel string) (string, error) {
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
	// registry uncertain：在 ReserveSession 之前早退。放行的話 Put 會被 store
	// 拒絕、名額白佔一輪，而且錯誤要等到補償路徑跑完才浮出來。
	if a.registryUncertain() {
		return "", errRegistryUncertain
	}
	// Remove × New 共用 provider token（§3.6.2）：等待與持有涵蓋 Reserve→Commit
	// 整段，與 RemoveSession 對同一 provider 互斥（見 crTokens 欄位 doc 的鎖序）。
	crt := a.crToken(p)
	if h := a.hookCreateWaitingForToken; h != nil {
		h()
	}
	crt.Lock()
	if h := a.hookCreateAcquiredToken; h != nil {
		h()
	}
	defer crt.Unlock()
	w, tok, err := a.manager.ReserveSession(p)
	if err != nil {
		return "", err
	}
	if err := a.wsReg.Put(wsregistry.Entry{
		WSID: string(w), Provider: provider, TaskLabel: taskLabel,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return "", errors.Join(a.noteRegistryUncertainErr("create_put", string(w), err),
			a.manager.AbortCreate(tok))
	}
	if cerr := a.commitCreate(tok); cerr != nil {
		if rerr := a.noteRegistryUncertainErr("create_rollback", string(w),
			a.wsReg.DeleteUncommitted(string(w))); rerr != nil {
			a.setCreateDegraded(p) // 雙失敗：保留名額、latch，等 app restart（§3.1）
			// WSID 一併帶進錯誤：這筆 reservation 被刻意保留、不 emit、不寫 audit，
			// registry 那筆 entry 也還在磁碟上——錯誤字串是 post-mortem 對帳的唯一線索。
			return "", errors.Join(cerr, rerr,
				fmt.Errorf("app: create degraded, orphan reservation wsid=%s: %w", w, errCreateDegraded))
		}
		return "", errors.Join(cerr, a.manager.AbortCreate(tok))
	}
	return string(w), nil
}

// SessionInfo：ListSessions 回傳的單筆 session 描述（前端 session 清單的唯一
// 資料來源）。
//
// **registry 與 working slot 的權責分工（Task 26 裁決）**：
//   - registry（workspace-sessions.json）是「這個 workspace 有哪些 session」的
//     durable 權威——清單一律以它為準逐筆列出（tombstone 不列）。
//   - Manager slot 是 lifecycle 入口能解析的對象；WSID 能不能被 StartSession／
//     SendMessage／EndSession 定址，只有它說了算。
//
// 兩邊本來就可能不一致（例：tombstone 已落盤但 RemoveSession 遞減名額失敗、或
// registry 有 entry 但 RestoreDormant 沒把它掛回去）。**不隱藏、不猜**：以
// Available=false ＋空 State 據實呈現一筆「有紀錄但目前不可操作」的 session，
// 讓使用者看得到並可重試移除；反過來把它從清單濾掉，會讓稽核裡有事件、UI 卻
// 沒有這個 session，等於把不一致藏起來。
//
// 只帶 durable metadata ＋ slot 可解析性：busy／待核可／unread 是 §3.2.1 明列
// 的 in-memory 推導值，由前端 store 從 runtime envelope 推導，不從這裡回傳。
type SessionInfo struct {
	WSID            string `json:"wsid"`
	Provider        string `json:"provider"`
	TaskLabel       string `json:"task_label"`
	ResumeSessionID string `json:"resume_session_id"`
	CreatedAt       string `json:"created_at"`
	Available       bool   `json:"available"` // Manager 有對應的 committed slot
	// State：reducer 的 contract.SessionState（idle／waiting／streaming／
	// tool_running／awaiting_approval／retrying／done／failed）——**不是** slot
	// phase（starting／active／ending／resetting，那是 Manager 內部的 lifecycle
	// 狀態機，沒有對外出口）。與 conversation lane 的 state_change envelope 同一個
	// reducer，前端因此可以直接覆寫、值域一致。Available=false 時為空字串。
	State string `json:"state"`
}

// ListSessions：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) ListSessions() ([]SessionInfo, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.listSessions()
}

// ListSessions：前端 session 清單的來源（唯讀——不建立 slot、不寫 registry）。
// 排序以 WSID（ULID，建立時間單調）遞增，清單順序因此穩定、不隨 map 迭代漂移。
//
// registry 尚未接線（malformed／載入失敗，見 loadSessionRegistry 的 fail loud）
// 時回 errNoSessionRegistry：此時 CreateSession 也是同一個錯誤，UI 顯示的是
// 「registry 不可用」而不是「沒有 session」——後者會讓使用者以為 session 沒了。
func (a *App) listSessions() ([]SessionInfo, error) {
	if a.wsReg == nil {
		return nil, errNoSessionRegistry
	}
	entries := a.wsReg.Live()
	out := make([]SessionInfo, 0, len(entries))
	for _, e := range entries {
		info := SessionInfo{
			WSID: e.WSID, Provider: e.Provider, TaskLabel: e.TaskLabel,
			ResumeSessionID: e.ResumeSessionID, CreatedAt: e.CreatedAt,
		}
		if st, err := a.manager.State(appcore.WSID(e.WSID)); err == nil {
			info.Available, info.State = true, string(st)
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(x, y SessionInfo) int { return strings.Compare(x.WSID, y.WSID) })
	return out, nil
}

// ---- pane pins／focused pane 的持久化（§3.2.1 白名單、§3.8 啟動重建）----

// maxPinnedPanes：§3.7 凍結的並看 pane 數（固定 50/50 兩格，M3b 明確不含
// N-pane）。與前端 store 的 `pins: [a, b]` 兩格陣列同一個常數。
const maxPinnedPanes = 2

// PaneLayout：pane pins 與 focused pane 的 durable 排列。
//
// 為什麼要持久化（§3.2.1 白名單明列、§3.8 啟動只重建兩個釘選 pane）：pins 是
// 「重啟後要花錢重建哪兩個 pane」的唯一輸入。不存的話每次重啟兩個 pane 都是空
// 的、使用者得重新釘選一次，而 §3.8 的整套視窗化載入也就沒有起點。
//
// **不是 runtime state**：這裡只有使用者明確選定的排列，沒有 busy／unread／
// approval pending，白名單不因此鬆動。
//
// Pins 固定兩格、空字串代表該 pane 沒有釘選（不是「未設定」——沒有第三種狀態，
// 所以不用 pointer／omitempty）。Focused 是 Pins 其中一個 WSID，或空字串代表
// focused pane 目前是空的。
type PaneLayout struct {
	Pins    []string `json:"pins"`
	Focused string   `json:"focused"`
}

// PaneLayout：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) PaneLayout() (PaneLayout, error) {
	if err := a.beginTxn(); err != nil {
		return PaneLayout{}, err
	}
	defer a.endTxn()
	return a.paneLayout()
}

// PaneLayout：讀出目前的釘選排列（唯讀——不建立 slot、不寫 registry）。
//
// **latch 期間刻意不擋**：errRegistryUncertain 的處置是「mutation 拒絕、讀取一律
// 放行」（見該變數 doc）。擋掉讀取只會讓 latch 之後的重啟連上一次的釘選都拿不到。
//
// 已 tombstone／已不存在的 WSID 在這裡就地濾成空字串，不留給前端判斷：registry
// 是「這個 WSID 還算不算數」的權威，而 pins 是使用者最後一次操作的快照，兩者
// 之間本來就可能落差（另一個路徑移除了 session、SetPaneLayout 又剛好失敗）。濾
// 掉之後 focused 若指向被濾掉的那一格，一併降級成空字串——否則會回傳一個指向空
// pane 的 focused，前端得再解釋一次同一件事。
func (a *App) paneLayout() (PaneLayout, error) {
	if a.wsReg == nil {
		return PaneLayout{}, errNoSessionRegistry
	}
	l := a.wsReg.Layout()
	pins := make([]string, maxPinnedPanes)
	for i := 0; i < maxPinnedPanes && i < len(l.Pins); i++ {
		w := l.Pins[i]
		if w == "" {
			continue
		}
		if e, ok := a.wsReg.Get(w); !ok || e.RemovedAt != "" {
			continue // 已移除／已不在 registry：該 pane 還原成空
		}
		pins[i] = w
	}
	out := PaneLayout{Pins: pins, Focused: l.Focused}
	if out.Focused != "" && !slices.Contains(pins, out.Focused) {
		out.Focused = ""
	}
	return out, nil
}

// SetPaneLayout：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) SetPaneLayout(pins []string, focused string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.setPaneLayout(pins, focused)
}

// SetPaneLayout：使用者改變釘選或焦點時的 durable 寫入。
//
// **寫入頻率與 debounce（本票判斷，見 pane-pins-report.md）**：觸發點是離散的
// 使用者手勢（點 session 卡片、點另一個 pane、Cmd+1/2），不是資料流，速率上限就
// 是人手點擊；每次成本是 wsregistry 的四步落盤，且整段在 binding 的 goroutine
// 上、不在畫面更新路徑。因此**不做 debounce／batching**，與 persistLocked 那條
// 契約同一個結論（省下的 sync 換不到有意義的東西），而且 debounce 會多欠一筆
// 「shutdown 前要 flush」的義務——§3.6.5 的 shutdown 總序是凍結的，為一個 UI
// 偏好去動它不成比例。
//
// **失敗不擋路**：這裡照常 fail loud 回錯（含 latch 期間的 errRegistryUncertain），
// 但呼叫端（前端 store 的 persistLayout）只把錯誤送進 notices lane，不回滾使用者
// 剛做的釘選——pins 是 UI 偏好，不是 resume correctness，寫不進去的正確降級是
// 「這次的排列重啟後會遺失」，不是「不准釘選」。
//
// 走 beginTxn 的理由：§3.6.5 把 `session registry Sync` 凍結為 shutdown 的最後
// 一步，晚於它抵達的寫入不是遺失就是把已 flush 的內容再弄髒。shutdown 柵欄是既有
// 且唯一的擋法。
func (a *App) setPaneLayout(pins []string, focused string) error {
	if a.wsReg == nil {
		return errNoSessionRegistry
	}
	if len(pins) > maxPinnedPanes {
		return fmt.Errorf("app: SetPaneLayout 最多 %d 個 pane，got %d", maxPinnedPanes, len(pins))
	}
	// focused 必須是 pins 其中一格：不是的話代表呼叫端的兩個欄位已經不同步，
	// 寫進去只會在下一次啟動還原出一個指向不存在 pane 的焦點。
	if focused != "" && !slices.Contains(pins, focused) {
		return fmt.Errorf("app: SetPaneLayout focused %q 不在 pins %v 之中", focused, pins)
	}
	if a.registryUncertain() {
		return errRegistryUncertain
	}
	return a.noteRegistryUncertainErr("set_layout", focused,
		a.wsReg.SetLayout(wsregistry.Layout{Pins: pins, Focused: focused}))
}

// resolveWSID：exported WSID binding 共用的解析入口——WSID → provider，且保證
// 該 WSID 對應一個 Manager 能定址的 committed slot。Task 26 的原子切換之後，
// 前端一律直接帶 WSID（不再有 provider → WSID 的猜測層），這裡只做驗證。
//
// 解析不到一律 ErrSessionNotFound 包上呼叫端的操作名：使用者可能拿著一個已被
// 移除／尚未還原的 WSID（例如另一個視窗剛移除它），必須是明確錯誤而不是靜默
// 落到某個別的 session 上。
// 用 State 而非只用 ProviderOf 做存在性檢查：ProviderOf 連「尚未 CommitCreate
// 的 reservation」都認得，而那種 slot 對所有 lifecycle 入口都是
// ErrSessionNotFound——放行只會把錯誤延後到下一行才炸。
func (a *App) resolveWSID(op, wsid string) (appcore.WSID, contract.Provider, error) {
	w := appcore.WSID(wsid)
	if _, err := a.manager.State(w); err != nil {
		return "", "", fmt.Errorf("app: %s %s: %w", op, wsid, err)
	}
	p, ok := a.manager.ProviderOf(w)
	if !ok { // State 已成立卻查不到 provider＝slot registry 自相矛盾，fail loud
		return "", "", fmt.Errorf("app: %s %s: %w", op, wsid, appcore.ErrSessionNotFound)
	}
	return w, p, nil
}

// providerRestoreUnambiguous：provider-keyed 的 restore.json 是否能一一對應到
// 呼叫端的那個 WSID。
//
// **per-WSID writer 落地後，唯一的呼叫端是升級 backfill**（backfillResumeFromLegacy）：
// 每一輪的續聊身分現在都寫進該 WSID 自己的 registry entry，StartSession 不再需要
// 從 provider-keyed 記錄猜測。留著它的理由只有一個且要說準——backfill 的輸入
// 恰恰還是那份 provider-keyed 的 restore.json，「這個 provider 的 id 該搬給誰」
// 與本函式問的是同一個問題，判準也必須是同一個。
//
// restore.json（resume session id ＋ view 視窗）是 M3a 留下的 per-provider 結構。
// 同 provider 只有一個 session 時它仍是正確的續聊來源；一旦有第二個，它的語意
// 退化成「上一次是誰在講話」，套到別的 WSID 上就是把一個全新的 session 靜默接到
// 別人的對話上。
//
// 計數取 Manager slot 與 registry live entry 的**精確聯集**（以 WSID 為身分），
// 不是 max(|slots|, |live|)——後者是聯集的下界不是上界：兩邊各有一筆但**是不同
// 的 WSID** 時（Manager 有 X、registry live 只有 Y），max 得到 1 而誤判成明確。
//
// **這個改動單調收緊、不可能重新打開「並存」那條缺陷**：因為
// backed ≤ min(live, slots)，恆有 union = live + slots - backed ≥ max(live, slots)，
// 所以凡是 max 判為不明確的，聯集也一定判為不明確。這是它最重要的性質。
//
// 觸發那個誤判需要**四個條件同時成立**，不是「今天就會發生」：(1) Task 27 把
// RemoveSession 接上 UI；(2) Remove × Start 撞上 §3.6.2 的 TOCTOU 殘餘窗口，留下
// 一個 tombstone 已落盤、slot 卻還在的孤兒；(3) 同 provider **另有**一筆
// live-but-no-slot 的 entry（loadSessionRegistry Pass 1 跳過的壞 entry、或未還原
// 的 dormant）——只有孤兒 slot 的話 live=0、union=1，判定仍是明確；(4) 移除路徑的
// ClearResume 本身也失敗（否則 restore 早就空了，讀什麼都是空）。真正關掉時間軸
// 那幾條的是 RemoveSession 裡的**無條件 ClearResume**，本函式是第二道防線。
//
// 兩邊都要看的理由：Manager 看不到尚未還原成 slot 的 dormant entry，registry 未
// 接線（nil）時什麼都看不到。registry 為 nil 時只看 Manager 是安全的——
// CreateSession 同樣被 errNoSessionRegistry 擋著，UI 開不出第二個 session。
//
// 兩個已知的不精確處，方向都安全、刻意不修：
//   - backed 只比對 registry entry 的 provider，不比對 slot 的 provider。
//     production 兩者恆一致（CreateSession 同源寫入；RestoreDormant 對不上會回
//     ErrProviderMismatch），故不構成缺陷。
//   - SlotCount 含尚未 CommitCreate 的 reservation，而 manager.State 走
//     committedSlotLocked 會排除它 → backed 少算、union 高估。高估只會讓判定更
//     保守（少接續一次），不會漏放。
func (a *App) providerRestoreUnambiguous(p contract.Provider) bool {
	slots := a.manager.SlotCount(p)
	if slots > 1 {
		return false
	}
	if a.wsReg == nil {
		return true
	}
	live, backed := 0, 0
	for _, e := range a.wsReg.Live() {
		if contract.Provider(e.Provider) != p {
			continue
		}
		live++
		if _, err := a.manager.State(appcore.WSID(e.WSID)); err == nil {
			backed++ // 這筆 live entry 同時也是一個 Manager slot（交集）
		}
	}
	return live+(slots-backed) <= 1 // |A ∪ B| = |live| + |只存在於 Manager 的 slot|
}

// registryResume：StartSession 的 resume 參數留空時的續聊來源——**該 WSID 自己**
// 的 durable 續聊身分，不再是 provider-keyed 的猜測。
//
// 這是舊 providerResumeFallback 的取代品，同時也是它整套「不明確就不接續」防線
// 之所以可以拆掉的原因：來源本身就以 WSID 定址，「指不出是哪一個 session」這個
// 前提消失了。tombstone 一律回空字串（entry 還在磁碟上，但那個 session 已被使用者
// 移除，它的 id 不該再被任何人拿去續聊）。
func (a *App) registryResume(w appcore.WSID) string {
	if a.wsReg == nil { // registry 未接線：沒有可信來源，開新對話（安全方向）
		return ""
	}
	e, ok := a.wsReg.Get(string(w))
	if !ok || e.RemovedAt != "" {
		return ""
	}
	return e.ResumeSessionID
}

// commitSessionIdentity：Accept 成功後把該 WSID 的續聊身分與 task label 寫進
// durable registry（spec §3.2.1 白名單的 resume identity ＋ task label）。
func (a *App) commitSessionIdentity(w appcore.WSID, p contract.Provider, resumeID, taskLabel string) {
	if a.wsReg == nil {
		return
	}
	a.noteRegistryWriteResult(w, p, "commit_resume", a.wsReg.CommitResume(string(w), resumeID, taskLabel))
}

// noteRegistryWriteResult：per-WSID metadata 寫入的統一處置。
//
// 兩類分開：entry 不存在／已 tombstone 是**良性跳過**（判定發生在 store mutex
// 內，與 Remove 有全序——這正是取代掉 resumeWriteAllowed 那個 check-then-act 的
// 地方），只留 audit 軌跡，不打擾使用者；真正的 persist 失敗才 fail loud，沿用
// failLoudRestore 的凍結語意（session 保持 active、呼叫照樣成功）。
func (a *App) noteRegistryWriteResult(w appcore.WSID, p contract.Provider, op string, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, wsregistry.ErrEntryNotFound), errors.Is(err, wsregistry.ErrTombstoned):
		a.audit("session_metadata_write_skipped", map[string]any{
			"op": op, "wsid": string(w), "provider": string(p), "reason": err.Error()})
	case errors.Is(err, wsregistry.ErrRegistryUncertain):
		// 自己的稽核標籤：這不是一次普通的寫入失敗，而是「不知道有沒有寫成功」
		// 的 latch。混進 restore_store_error 會讓 post-mortem 讀成可重試的 IO 錯誤。
		// 這裡不走 noteRegistryUncertainErr：per-WSID writer 這條額外知道
		// provider，丟掉那一欄是無聲的診斷降級。標籤與其他呼叫點一致。
		a.audit("session_registry_uncertain", map[string]any{
			"op": op, "wsid": string(w), "provider": string(p), "error": err.Error()})
		// 使用者可見出口＝**workspace lane**（同 failLoudCodexDispatch 的形狀）。
		//
		// 這裡刻意不用 a.emit 直接送一個 session-scope envelope：那個形狀沒有
		// workspace_session_id，前端 routeEnvelope 會判成 session lane，而
		// session store 的 apply() 對空 WSID 只做 `unrouted++` 然後丟棄——
		// unrouted 至今沒有任何渲染端，等於這條 fail loud 到不了使用者
		// （rev2 走證據鏈時抓到的缺口）。workspace lane 會進 notices，而
		// notices 被合併進**任何** focused pane 的 timeline：latch 是 registry
		// 全域的事實，本來就不該只在某一個 pane 看得到。
		//
		// payload 用 component ＋ error 兩個 key：Timeline.vue 的 summary()
		// 正是讀這兩個（頂層 Error 是 omitempty，EmitWorkspace 從不填它）。
		// wsid／op 一併帶著，讓使用者知道是哪一個 session 的哪一次寫入。
		a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
			"component": "session-registry",
			"wsid":      string(w),
			"op":        op,
			"error":     errRegistryUncertain.Error(),
		})
	default:
		a.failLoudRestore(p, err)
	}
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
// （restore.go:42-56），那種 entry 的 ViewStartEventID 非空但 window 內必然
// 零事件。只看欄位非空的話，只用過 claude 的使用者升級後會憑空多出一個
// codex session 並吃掉一個名額。window 判定改用 scanLegacyWindow（非
// replayViewWindow）：掃描失敗（開檔或 Scanner.Err()）必須讓本函式回 error，
// 不能把「讀不到」誤判成「window 內沒有事件」而靜默跳過遷移——那正是
// transcript-only 使用者被永久遷成空 entries 的路徑。
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
		window, scanned, werr := scanLegacyWindow(a.eventsPath(), p, e.ViewStartEventID)
		if werr != nil {
			return nil, werr
		}
		if !scanned {
			return nil, fmt.Errorf("app: events.jsonl 無法掃描（scanned=false，可能為降級啟動），不得判定 legacy transcript 存在與否：provider=%s", p)
		}
		hasTranscript := len(window) > 0
		if e.ResumeSessionID == "" && e.TaskID == "" && !hasTranscript {
			continue
		}
		out[p] = wsregistry.LegacyEntry{
			ViewStartEventID:    e.ViewStartEventID,
			ResumeSessionID:     e.ResumeSessionID,
			TaskID:              e.TaskID,
			HasLegacyTranscript: hasTranscript,
		}
	}
	return out, nil
}

// noteStartupWarning／noteStartupBlocker：把啟動期訊息接到既有 startupErr
// 通道（UI 經 CLIInfo().startupError 讀得到）。
//
// **累積、不是只填第一則**（Task 2a rev2 review I1）：原本的 first-wins 會把
// 後到的訊息靜默丟掉，而啟動序列裡最嚴重的那則正好排在後面——
// `loadSessionRegistry` 先寫「跳過 N 筆無法還原的 entry」，之後才呼叫
// `backfillResumeFromLegacy`，所以 registry 在 backfill 落盤時進了 uncertain
// latch 的話，**啟動成功、registry 已停止寫入，而使用者一則相關訊息都看不到**。
// 被丟掉的訊息等於沒有 fail loud。
//
// **排序是嚴重度優先，不是時序**（rev3 review I1）：`.meta` 那一行是
// `white-space: nowrap; text-overflow: ellipsis` 的單行版面，第一則含完整絕對
// 路徑（>100 字元）時，串在後面的第二句在一般視窗寬度下會被裁掉、只有 hover
// title 看得到。所以 blocker（registry 停止寫入／載入失敗這類「不處理就一直
// 壞下去」的）一律排到最前面，warning（降級但還能用的）往後接。刻意不去重做
// `.meta` 的版面——那超出這張票。
func (a *App) noteStartupWarning(msg string) { a.appendStartup(msg, false) }

// noteStartupBlocker：嚴重度較高、必須先被讀到的那類（registry 寫入已停止、
// registry 載入失敗）。同嚴重度之間仍照時序。
func (a *App) noteStartupBlocker(msg string) { a.appendStartup(msg, true) }

func (a *App) appendStartup(msg string, blocker bool) { a.startupData.appendMessage(msg, blocker) }

// ---- 啟動資訊（見 App.startupData）----

// startupInfo：啟動資訊的一份快照。CLIInfo 與所有會 exec 外部指令的路徑一律
// 「取一次快照 → 之後只用快照」，不讓短暫的資料保護變成跨行程呼叫的長臨界區。
type startupInfo struct {
	startupErr  string
	blockers    string
	toolsDir    string
	toolsSource string
	nodePath    string
	workspace   string
	workspaceSr string
}

// startupState：startupInfo ＋ 保護它的那把鎖，封成一個型別。
//
// # 為什麼封成型別，而不是繼續驗控制流程（reviewer 2026-08-20）
//
// 先前六個欄位散在 App 上，「有沒有在鎖內存取」只能靠測試做語彙層級的判斷，於是
// 這些寫法一律通得過：鎖另一個 App instance 的 mutex、提早 Unlock 之後才讀、
// writer 在 closure 內 Lock／Unlock 而真正的寫入落在 closure 之外、持鎖執行外部
// 指令。要把這些全部擋住得寫一個真正的逃逸與別名分析——那不會比它要守的東西可靠。
//
// 封裝之後這些問題消失在型別層：info 是 unexported、只有下面幾個方法碰得到；每個
// 方法都是「Lock → defer Unlock → 純欄位運算」的固定形狀，鎖的範圍與方法的範圍
// 一致，receiver 就是被鎖的那一個。剩下要驗的只有「這幾個方法的形狀」與「沒有別
// 的地方碰得到 info」，兩者都能逐字比對（見 TestStartupStateIsTheOnlyAccessPath）。
type startupState struct {
	mu   sync.Mutex
	info startupInfo
}

// snapshot：整份複製。**唯一**的讀取出口。
func (s *startupState) snapshot() startupInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// appendMessage：啟動訊息累加（第一則 blocker 插到最前面，見 noteStartupWarning）。
func (s *startupState) appendMessage(msg string, blocker bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.info.startupErr == "":
		s.info.startupErr = msg
	case blocker && s.info.blockers == "":
		s.info.startupErr = msg + "；" + s.info.startupErr
	default:
		s.info.startupErr += "；" + msg
	}
	if blocker && s.info.blockers == "" {
		s.info.blockers = msg
	}
}

// setErrOnce：只記第一則啟動錯誤。
func (s *startupState) setErrOnce(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.info.startupErr == "" {
		s.info.startupErr = msg
	}
}

func (s *startupState) publishTools(dir, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.toolsDir, s.info.toolsSource = dir, source
}

func (s *startupState) publishNode(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.nodePath = path
}

func (s *startupState) publishWorkspace(dir, src string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.workspace, s.info.workspaceSr = dir, src
}

// ---- App 這一側只是轉呼叫（既有呼叫端不必全部改寫）----

func (a *App) startupSnapshot() startupInfo { return a.startupData.snapshot() }

// startupErrText：目前的啟動訊息（UI 橫幅與稽核用）。
func (a *App) startupErrText() string { return a.startupData.snapshot().startupErr }

// setStartupErrOnce：只記第一則啟動錯誤——沿用 repo 既有的
// `if a.startupErr == "" { ... }` 慣例。要**累加**語意的請走
// noteStartupWarning／noteStartupBlocker。
func (a *App) setStartupErrOnce(msg string) { a.startupData.setErrOnce(msg) }

// publishToolsDir／publishNodePath：startupAfterWriters 的欄位發布點。刻意分成
// 兩個呼叫——兩者的解析各自獨立，中間正是 CLIInfo 可能插進來的窗口，測試用
// hookStartupPublish 停在那裡驗證讀寫走同一套同步機制。
func (a *App) publishToolsDir(dir, source string) { a.startupData.publishTools(dir, source) }

func (a *App) publishNodePath(p string) { a.startupData.publishNode(p) }

// publishWorkspace：把 workspace 解析結果複製進受鎖的快照——CLIInfo 是唯一容許在
// startup 期間執行的 binding，它讀到的每個欄位都必須受同一把鎖保護。
func (a *App) publishWorkspace(dir, src string) { a.startupData.publishWorkspace(dir, src) }

// blockStateBindings：把 app 切到「不得開放任何 state 操作」——**原因與 phase 在
// 同一個臨界區內一起設定**（見 stateBlocked 欄位 doc）。設定後不再解除：解除的
// 正確方式是人工處理完衝突再重新啟動。
func (a *App) blockStateBindings(reason string) {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	if a.stateBlocked == "" {
		a.stateBlocked = reason
	}
	if a.phase != phaseShuttingDown {
		a.phase = phaseBlocked
	}
}

// stateBlockedReasonLocked：被擋下的原因文字（呼叫端必須持有 shutMu）。
func (a *App) stateBlockedReasonLocked() string {
	if a.stateBlocked == "" {
		return "app 啟動未完成，暫不受理 state 操作"
	}
	return a.stateBlocked
}

// stateBlockedErr：state 操作是否被擋下（nil ＝ 沒有）。惰性初始化路徑
// （ensureGate／ensureEscalation）用它擋內部呼叫者；exported binding 走
// beginTxn 的 lifecycle 判定。
func (a *App) stateBlockedErr() error {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	if a.stateBlocked == "" {
		return nil
	}
	return errors.New(a.stateBlocked)
}

// beginStartupLifecycle：startup 進場登記。回 false ＝ 不得進場，**一個 writer
// 都不能開**。兩種情形：
//
//	已在收尾    → shutdown 已經開始，這次 startup 什麼都不該做。
//	已有 owner  → 已經有人取得過 ownership（不論是否已結束）。**明確拒絕**
//	              （reviewer 2026-08-19）：先前每次進場都重建 startupDone，兩個
//	              並行時先結束的那個會關掉 channel、把 startupRunning 設回 false，
//	              於是 shutdown 誤判「startup 都停了」而釋放 lease——但另一個可能
//	              還在開 writer。改用永久旗標之後，連「第一次已經結束、再呼叫一次」
//	              也一併拒絕：那會重新開啟並覆寫 writers 與 Manager。
func (a *App) beginStartupLifecycle() bool {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	if a.phase == phaseShuttingDown || a.startupStarted {
		return false
	}
	a.startupStarted = true
	a.startupRunning = true
	a.startupDone = make(chan struct{})
	return true
}

// endStartupLifecycle：startup owner 離場（不論成功、被擋或中途失敗）。只有真正
// 取得 ownership 的那一次才會呼叫到——被拒的 startup 直接 return，不進這裡。
func (a *App) endStartupLifecycle() {
	a.shutMu.Lock()
	done := a.startupDone
	a.startupRunning = false
	a.startupDone = nil
	a.shutMu.Unlock()
	if done != nil {
		close(done)
	}
}

// awaitStartupStopped：bounded 等 startup 收斂。回 false ＝ 仍在跑，呼叫端**不得
// 釋放 lease**（處理原則同背景 frame worker）。
func (a *App) awaitStartupStopped(window time.Duration) bool {
	a.shutMu.Lock()
	running, done := a.startupRunning, a.startupDone
	a.shutMu.Unlock()
	if !running || done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(window):
		return false
	}
}

// startupDrainWindow：見 startupDrain 欄位（測試設 0 即「不等」）。
func (a *App) startupDrainWindow() time.Duration {
	if a.startupDrain != nil {
		return *a.startupDrain
	}
	return 2 * time.Second
}

// appPhase：app 的 lifecycle 狀態（見 App.phase）。
//
//	initializing → startup 尚未完成（或已失敗）。state binding 一律拒絕：這一刻
//	               遷移可能還沒跑、writer 可能還沒開，放行等於在未知狀態上寫。
//	ready        → startup 全部完成。唯一開放 state 操作的狀態。
//	blocked      → 啟動期發現無法自行判定的資料衝突（目前是舊路徑遷移）。終態，
//	               只能由人工處理後重新啟動解除。
//	shuttingDown → 收尾中。
type appPhase int

const (
	phaseInitializing appPhase = iota
	phaseReady
	phaseBlocked
	phaseShuttingDown
)

// setPhase：lifecycle 轉換（規則寫在這裡，不散在呼叫端）。
//
//	→ shuttingDown：永遠允許（收尾優先於一切）。
//	→ blocked：除非已在收尾。
//	→ ready：**只能從 initializing 來**——blocked 是終態，不得被後續的
//	  「啟動成功了」蓋掉。
func (a *App) setPhase(p appPhase) {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	switch {
	case p == phaseShuttingDown:
	case a.phase == phaseShuttingDown:
		return
	case p == phaseReady && a.phase != phaseInitializing:
		return
	}
	a.phase = p
}

// phaseNow：目前的 lifecycle 狀態（診斷／損害控制用；不要拿它做「檢查後再動作」
// 的判斷——那種判斷必須在 shutMu 內一次完成，見 beginTxn）。
func (a *App) phaseNow() appPhase {
	a.shutMu.Lock()
	defer a.shutMu.Unlock()
	return a.phase
}

// toolsDir／nodePathValue：單一欄位的讀取器（CLI 路徑組裝、childEnv 用）。
func (a *App) toolsDir() string { return a.startupData.snapshot().toolsDir }

func (a *App) nodePathValue() string { return a.startupData.snapshot().nodePath }

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
// （errNoSessionRegistry）。若在 Open 之後就接線，遷移失敗的那次啟動仍可
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
	a.startupStep("registry_load")
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
	a.startupStep("migrate")
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
	a.startupStep("restore_dormant")
	for _, e := range restorable {
		if rerr := a.manager.RestoreDormant(appcore.WSID(e.WSID), contract.Provider(e.Provider)); rerr != nil {
			return nil, fmt.Errorf("app: restore dormant wsid=%s provider=%s: %w", e.WSID, e.Provider, rerr)
		}
	}
	// 跳過筆數是**降級但還能用**的那一類，所以走 noteStartupWarning（往後接），
	// 不是 blocker。rev2 之前這裡有一段「刻意留到最後才寫、因為 first-wins」的
	// rationale——noteStartupWarning 改成累積＋嚴重度優先之後那個理由已經死了，
	// 而它造成的順序正好把良性那則放在 `.meta` 單行版面唯一看得見的位置
	// （rev3 review Minor）。順序現在由嚴重度決定，不由呼叫位置決定。
	if n := len(unknownProv) + len(invalid); n > 0 {
		a.noteStartupWarning(fmt.Sprintf(
			"session registry: 跳過 %d 筆無法還原的 entry（未刪除，仍在 %s）：%s",
			n, path, strings.Join(slices.Concat(unknownProv, invalid), ", ")))
	}
	a.wsReg = store
	// backfill 必須在 a.wsReg 接線之後：判準（providerRestoreUnambiguous）同時看
	// Manager slot 與 registry live entry，兩邊此刻才都就位（Pass 2 剛還原完）。
	// 它本身不是「載入序列的一部分」，失敗不阻擋啟動——見函式 doc。
	a.backfillResumeFromLegacy(store)
	// legacy transcript 補寫同理不阻擋啟動：找不到唯一候選只代表使用者捲到底
	// 看不到 legacy 對話那段，不是資料完整性問題（events.jsonl 稽核歷史不受
	// 影響）。noteStartupBlocker 沿用 backfillResumeFromLegacy 失敗的前例——
	// blocker 只影響訊息排序，不阻擋啟動（見 noteStartupBlocker doc）。
	if err := a.backfillLegacyTranscript(store); err != nil {
		err = a.noteRegistryUncertainErr("legacy_transcript_backfill", "", err)
		a.audit("legacy_transcript_backfill_failed", map[string]any{"error": err.Error()})
		a.noteStartupBlocker("session registry: legacy transcript 標記補寫失敗（部分舊對話捲動到底時可能看不到更早的歷史訊息）：" + err.Error())
	}
	return restorable, nil
}

// backfillResumeFromLegacy：一次性升級補寫（owner 2026-08-17 D2）——把已經跑過
// 現行 M3b build 的使用者留在 provider-keyed restore.json 的續聊身分，搬進對應的
// per-WSID entry。
//
// 不做的話，今天能自動續聊的既有 session 升級後全部開新對話（events.jsonl 完整，
// 但續聊斷掉）——那是使用者可見的行為退步。
//
// 判準沿用 providerRestoreUnambiguous：「該 provider 恰有一筆 live session」才搬，
// 多筆時**不猜、不填**，失敗方向與它取代掉的 providerResumeFallback 完全一致
// （少一次續聊 vs 接到別人的對話）。
//
// 冪等：靠 store 的 resume_backfilled marker（單向）＋「只填空值」兩層。單靠後者
// 不夠——使用者按過 NewSession（resume 已清空）之後重啟，會被 stale 的 restore.json
// 值再次回填、靜默復活舊對話。
//
// 失敗一律不阻擋啟動：backfill 是續聊的便利性，不是資料完整性。persist 失敗時
// marker 未設，下次啟動會重試。
func (a *App) backfillResumeFromLegacy(store *wsregistry.Store) {
	if store.ResumeBackfilled() || a.restore == nil {
		return
	}
	fill := map[string]string{}
	for _, p := range knownProviders {
		id := a.restore.Get(p).ResumeSessionID
		if id == "" {
			continue
		}
		if !a.providerRestoreUnambiguous(contract.Provider(p)) {
			a.audit("resume_backfill_skipped", map[string]any{
				"provider": p, "candidate": id, "reason": "multiple sessions for provider"})
			continue
		}
		for _, e := range store.Live() {
			if e.Provider == p && e.ResumeSessionID == "" {
				fill[e.WSID] = id
			}
		}
	}
	if err := a.noteRegistryUncertainErr("resume_backfill", "", store.BackfillResume(fill)); err != nil {
		a.audit("resume_backfill_failed", map[string]any{"error": err.Error()})
		a.noteStartupBlocker("session registry: 續聊身分升級補寫失敗（本次啟動不自動接續舊對話）：" + err.Error())
		return
	}
	if len(fill) > 0 {
		wsids := make([]string, 0, len(fill))
		for wsid := range fill {
			wsids = append(wsids, wsid)
		}
		slices.Sort(wsids) // 固定順序：audit 訊息不得隨 map 迭代漂移
		a.audit("resume_backfilled", map[string]any{"count": len(fill), "wsids": wsids})
	}
	// D6：搬完就把 restore.json 的續聊身分清空（檔案保留，作為 M3a 使用者的最後
	// 備份與 legacy 遷移輸入）。清除失敗只是留下一份永遠不會再被讀的舊值——
	// marker 已經落盤，backfill 不會重跑，所以不 fail loud 成使用者可見錯誤。
	for _, p := range knownProviders {
		if err := a.restore.ClearResume(p); err != nil {
			a.audit("restore_clear_failed", map[string]any{"provider": p, "error": err.Error()})
		}
	}
}

// backfillLegacyTranscript：legacy transcript 的一次性補寫——用 restore.json
// 快照對每個 legacy provider 精確比對五條件，找出恰好一個候選 WSID 後標記
// LegacyTranscript=true（讓 hydrate 之後知道哪個 entry 要接 legacy 對話）。
//
// 五條件：(1) live（tombstone 不算候選——Live() 本身已排除）、(2) provider
// 與掃描的 provider 相同、(3) entry 的 ViewStartEventID 與 restore.json 該
// provider 的快照**精確相等**（差一字元即不算，不做前綴／模糊比對）、(4) 該
// provider 在此 boundary 之後確有無 WSID 的 legacy 事件（scanLegacyWindow 非
// 空——沒有事件就沒有 transcript 好接，不構成候選）、(5) 每個 provider 恰有
// 一個候選——0 個安全略過，2 個以上不猜、fail loud。
//
// 快照 ViewStartEventID 為空字串時，該 provider 直接略過比對（等同零候選，
// 不落入五條件判定、也不掃描 events.jsonl）——空字串在這個系統代表「沒有
// boundary」，不是一個可信的比對值：CreateSession 建 entry 時不設
// ViewStartEventID（唯一寫入者是 ResetView），而首次啟動時若 events.jsonl
// 為空，restore.json 快照會被 freshEntries(auditHighWatermark) 初始化成 ""
// （restore.go:56、137-141）。放行空字串比對會讓「該 provider 目前沒有可信
// boundary」被誤判成「找到了」：同快照為 "" 的多個 entry 會被誤判成多候選，
// 使 marker 永遠卡在未落盤、每次啟動都重新 fail loud；若當下恰好只剩一筆
// ViewStart="" 的 entry，則會把整段 pre-migration 歷史誤標給它——owner 已否
// 決的失效模式。略過＝零候選是 §4 已接受的降級語意：無可信比對證據就不猜，
// 該 provider 這次拿不到 legacy 標記，但不阻擋 marker 落盤（其他 provider
// 仍照常判定）。
//
// 零候選（掃描成功、但沒有 entry 對得上任一 provider）仍視為「已完成一次檢
// 查」，marker 照樣落盤：不落的話每次啟動都要重新掃一次 events.jsonl。
//
// scan error 與多候選都必須 fail loud、marker 不落盤（可重試、不占用單向
// marker 的唯一一次機會）——兩者都不能被誤判成「零候選」，那會把「這次讀
// 不到」或「猜不出來」固化成「永遠沒有」。
//
// 冪等：LegacyTranscriptBackfilled() 為 true 時 early return，不重掃
// events.jsonl（見 TestBackfillLegacyTranscriptIdempotentAfterMarker）。
func (a *App) backfillLegacyTranscript(store *wsregistry.Store) error {
	if store.LegacyTranscriptBackfilled() || a.restore == nil {
		return nil
	}
	var candidates []string
	for _, p := range legacyProviders {
		re := a.restore.Get(p)
		if re.ViewStartEventID == "" {
			continue // 無可信 boundary，不猜（見上方 doc）——guard 放在掃描之前，省一次全檔掃描
		}
		window, scanned, werr := scanLegacyWindow(a.eventsPath(), p, re.ViewStartEventID)
		if werr != nil {
			return werr
		}
		if !scanned {
			return fmt.Errorf("app: legacy transcript backfill: events.jsonl 無法掃描（scanned=false，可能為降級啟動），不落 backfill marker：provider=%s", p)
		}
		if len(window) == 0 {
			continue
		}
		var match []string
		for _, e := range store.Live() {
			if e.Provider == p && e.ViewStartEventID == re.ViewStartEventID {
				match = append(match, e.WSID)
			}
		}
		switch len(match) {
		case 0:
			continue
		case 1:
			candidates = append(candidates, match[0])
		default:
			return fmt.Errorf("legacy transcript backfill: provider %s 有 %d 個候選 entry "+
				"view_start_event_id=%q，不猜：%v", p, len(match), re.ViewStartEventID, match)
		}
	}
	return store.BackfillLegacyTranscript(candidates)
}

// markIndexUnverified：把 replay index 切到 unverified latch（寫入端停手，見
// replayindex 的 unverified 欄位 doc）＋發出 fail-loud 訊號。
//
// 訊號留在 App 層而不是 replayindex 的 Config.Notify：那個出口接的是
// degraded → runtime 重建排程，而這個狀態需要的恰恰是**不要**自動重建；把兩
// 者混進同一條通知路徑，使用者會收到一則語意錯誤的 degraded 通知，實際的重建
// 又會以 ErrNotDegraded 空轉一輪。
func (a *App) markIndexUnverified(reason string) {
	if a.replayIndex == nil {
		return
	}
	a.replayIndex.MarkUnverified()
	a.audit("replay_index_unverified", map[string]any{"reason": reason})
}

// startupStep：§3.2.4 凍結啟動順序的探針。步驟名代表「這個階段跑到了」，
// 不是「這個階段做了事」——`migrate` 在 registry 已遷移時仍會發（該階段的
// 工作就是判定要不要遷），`index_verify` 同理。唯一的例外是
// `emit_stream_error`：它命名的是一個**動作**，只有真的補了修復事件才發，
// 否則「重跑必須冪等」那條測試就會看到一個沒有事件的步驟。
func (a *App) startupStep(step string) {
	if a.hookStartupStep != nil {
		a.hookStartupStep(step)
	}
}

// restoreSessions：§3.2.4 的完整凍結序列。
//
//	registry_load → migrate → restore_dormant   （loadSessionRegistry，Task 6）
//	→ index_verify → detect_incomplete → emit_stream_error
//	→ open_ui（修復完才開放 UI 與 provider 啟動）
//
// 順序不可調換的兩個理由：(1) incomplete turn 的判定來源是 replay index 的
// per-WSID open turn 狀態，index 必須先與 audit 對齊才問得出正確答案；
// (2) 修復本身會 emit 事件、也會再寫回 index，若 UI／provider 已經開放，
// 使用者送出的新 turn 會與修復事件交錯，`stream_error → failed` 可能落在新
// turn 中間。
//
// 錯誤處置分兩級（刻意不同）：
//   - registry 載入／遷移失敗 → 回錯，呼叫端據此不啟動 provider（§3.2.6）。
//   - index 驗證／重建失敗 → **不回錯**。index 是快取，讓它擋住整個 app 啟
//     動就是把快取升級成權威；改為 audit ＋ 啟動警告，跳過 incomplete turn
//     修復（沒有可信的偵測來源就不猜）並照常開放 UI。
func (a *App) restoreSessions() ([]wsregistry.Entry, error) {
	live, err := a.loadSessionRegistry()
	if err != nil {
		// registry 載入失敗 → `index_verify` 這一步**從未執行**，但 a.replayIndex
		// 早在 startup() 就已建立並接進 Manager 的 Config.Index：Observe 會無條件
		// 把 checkpointOffset 推到第一筆 live 事件的 receipt.EndOffset（workspace
		// lane 的 gate／assist／通知不需要 session slot，照樣會發），shutdown 的
		// Flush() 再把它落盤——下次啟動的 checkpoint 反而變成「可信」，中間那段
		// audit 從此沒人會補。這與「驗證跑了但失敗」是同一個靜默且永久的缺口
		// （見 indexUnverified 欄位 doc），只是缺口來自「驗證根本沒跑」。
		//
		// 同樣只 latch、不排程 runtime 重建：bulkRebuild 從
		// max(rebuildCursor, checkpointOffset) 起掃，而驗證沒跑過時那個值本身
		// 不可信，自動重建只會把缺口固化。
		//
		// **兩層 latch 缺一不可**：a.indexUnverified 是 App 層的，只擋讀
		// （LoadTurnsBefore fail loud）；擋寫的是 index 自己的 unverified
		// latch——Observe 與 Flush 都不知道 App 有那個旗標，沒有這一行，磁碟
		// checkpoint 仍會被推過那段沒索引到的 audit，下次啟動驗證反而「通
		// 過」，缺口被永久固化（見 replayindex 的 unverified 欄位 doc）。
		a.indexUnverified.Store(true)
		a.markIndexUnverified("registry 載入失敗，index_verify 從未執行：" + err.Error())
		return nil, err
	}

	a.startupStep("index_verify")
	indexUsable := a.replayIndex != nil
	if indexUsable {
		if verr := a.replayIndex.VerifyOrRebuild(a.eventsPath()); verr != nil {
			indexUsable = false
			a.indexUnverified.Store(true) // latch：視窗化載入自此 fail loud（見欄位 doc）
			// 同上，寫入端也要停：verifyOrRebuildLocked 是邊做邊改狀態的，中途
			// 失敗留下的記憶體 checkpoint 同樣沒有對帳過，再讓它落盤會把「掃到
			// 一半」凍成下次啟動看起來可信的起掃點。
			a.markIndexUnverified("VerifyOrRebuild 失敗：" + verr.Error())
			a.audit("replay_index_verify_error", map[string]any{"error": verr.Error()})
			a.noteStartupWarning("replay index 驗證／重建失敗，視窗化載入已停用（未完成 turn 修復本次跳過）；" +
				"請重啟 app 讓啟動期重建再跑一次：" + verr.Error())
		}
	}
	if indexUsable {
		if rerr := a.repairIncompleteTurns(live); rerr != nil {
			// 修復本身失敗不阻擋啟動：受影響的只有那個 WSID 的 view 會停在未
			// 完成 turn，其餘 session 完全正常。但必須 fail loud。
			a.audit("startup_repair_error", map[string]any{"error": rerr.Error()})
			a.noteStartupWarning("未完成 turn 修復失敗：" + rerr.Error())
		}
	} else {
		a.startupStep("detect_incomplete") // 階段仍走到，只是沒有可信來源可偵測
	}

	a.openUIAndProviders()
	return live, nil
}

// errAppRestartInterrupted：§3.2.3 的修復事件內容。文字是使用者在 transcript
// 裡會看到的那一行，措辭凍結——前端與稽核都靠它辨識這是重啟修復、不是
// provider 真的回報了錯誤。
var errAppRestartInterrupted = errors.New("app restart: interrupted turn")

// repairIncompleteTurns：§3.2.3 stuck busy 解除。某個 WSID 的最後一個 turn 未
// 完成時，經 Manager emit 一筆帶 WSID 的 stream_error，由 reducer 追發
// `state_change=failed` 結束該 turn。
//
// 「最後一個 turn 未完成」的唯一判定來源是 replay index 的 open turn 狀態——
// 它就是 audit 的重放結果（VerifyOrRebuild 剛把它與 events.jsonl 對齊過），
// 不另外實作一份 audit 尾掃描。
//
// **冪等**（§3.2.3 明文要求，crash 於修復中途、重啟重跑不得疊加）由同一個
// 判定來源保證，不需要額外的「末筆是不是 app-restart stream_error」比對：
// 補上的 stream_error 會導出 terminal `state_change=failed`，turn boundary 狀
// 態機看到它就把該 WSID 的 turn 收掉。所以修復過的 WSID 下一次啟動不再有
// open turn（不論是同一個 process 內重跑，或 crash 後重啟——後者由
// VerifyOrRebuild 重掃 audit 得到相同結論）。
//
// 逐 WSID 的失敗不中斷其他 WSID（errors.Join 收集）：一個 session 的 slot 出
// 問題不該讓其他 session 停在未完成 turn。WSID 排序後處理，讓多 session 的
// 修復事件順序穩定、可重現。
func (a *App) repairIncompleteTurns(entries []wsregistry.Entry) error {
	a.startupStep("detect_incomplete")

	var incomplete []wsregistry.Entry
	for _, e := range entries {
		if _, open := a.replayIndex.OpenTurnStart(e.WSID); open {
			incomplete = append(incomplete, e)
		}
	}
	if len(incomplete) == 0 {
		return nil
	}
	slices.SortFunc(incomplete, func(x, y wsregistry.Entry) int { return strings.Compare(x.WSID, y.WSID) })

	a.startupStep("emit_stream_error")
	var errs []error
	for _, e := range incomplete {
		if err := a.manager.Emit(appcore.WSID(e.WSID), contract.Event{
			Provider: contract.Provider(e.Provider),
			Kind:     contract.KindStreamError,
			Err:      errAppRestartInterrupted,
			Raw:      []byte(`{"source":"app_restart_repair"}`),
		}); err != nil {
			errs = append(errs, fmt.Errorf("wsid=%s: %w", e.WSID, err))
		}
	}
	a.audit("startup_repaired_incomplete_turns",
		map[string]any{"count": len(incomplete) - len(errs), "failed": len(errs)})
	return errors.Join(errs...)
}

// openUIAndProviders：§3.2.4 的最後一步——修復序列全部跑完才開放。
//
// 目前的「開放」形式：發一則 workspace 級 UI 訊號。它不是門閂，門閂在結構
// 上——startup() 是同步的，而 provider 相關入口（CreateSession → StartSession）
// 全部要求 a.wsReg 已接線，那是本序列成功走完才發生的事。這裡刻意不另外加
// 一道 beginTxn 級的 ready 閘：那會改變所有既有入口的行為，遠超本 task 的
// 範圍，且在同步啟動下買不到額外保證。
func (a *App) openUIAndProviders() {
	a.startupStep("open_ui")
	a.emit("workbench:ready", map[string]any{
		"replay_index":  a.replayIndex != nil,
		"startup_error": a.startupErrText(),
	})
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

// stateLease：開啟任何 state writer 所需的 ownership capability。
//
// 為什麼是 capability 而不是一個 bool 檢查：single-instance 保護的是**資料正
// 確性**——JSONLSink 的 offset 是本機累加值、registry 與 replay index
// checkpoint 走 temp write + rename，全部以「同一時間只有一個 writer」為前提。
// 這種保證不能靠編排慣例（「呼叫端記得先取鎖」），因為換一種入口寫法就會漏
// 掉。所以 writer 初始化的入口一律要求出示一份對得上 a.stateDir 的 lease。
//
// **不可偽造**：兩個授權來源都是 unexported 且只有各自的產生點設得起來——
//   - lock：`singleinstance.Lock` 的欄位全 unexported，`&Lock{}` 這種零值的
//     Held() 為 false；只有 Acquire 拿得到真的 flock。
//   - testOnly：唯一設定點是 app_test.go 的 newTestStateLease，**production
//     binary 裡不存在那個函式**。
//
// 零值（以及 nil）一律無效，刻意沒有「沒設就跳過檢查」這種語意。
type stateLease struct {
	stateDir string
	lock     *singleinstance.Lock
	testOnly bool
}

// ownsStateDir：這份 lease 是否授權寫入 dir。nil／零值／目錄對不上／已釋放
// 一律 false。
func (l *stateLease) ownsStateDir(dir string) bool {
	if l == nil || dir == "" || l.stateDir != dir {
		return false
	}
	if l.testOnly {
		return true
	}
	return l.lock.Held() && l.lock.StateDir() == dir
}

func (l *stateLease) release() error {
	if l == nil {
		return nil
	}
	return l.lock.Release() // nil lock（test lease）也安全
}

// acquireStateLease：production 唯一的 ownership lease 取得入口。
//
// canonical state directory 的解析是它的一部分、不由 caller 傳入——鎖必須綁在
// **實際會被寫入的那個目錄**上，否則「鎖對了但寫到別處」這種走鐘沒人擋得住。
//
// 冪等：同一個 App 重複呼叫回同一份 lease。runWailsUI 會在開視窗**之前**先呼
// 叫一次（讓第二個實例連視窗都不開），App.startup 進場再呼叫一次自行驗證；
// 兩者拿到同一把鎖，不會自我排斥。
func (a *App) acquireStateLease() (*stateLease, error) {
	if a.lease != nil {
		return a.lease, nil
	}
	if a.stateDir == "" {
		a.workspaceDir, a.stateDir, a.workspaceSrc, a.workspaceErr = resolveWorkspace()
	}
	a.publishWorkspace(a.workspaceDir, a.workspaceSrc)
	// **建立空的 state directory 是取鎖之前唯一允許的冪等初始化**（owner
	// 2026-08-19）。singleinstance.Acquire 只 open <stateDir>/instance.lock，
	// **它不會建目錄**——全新 workspace（`.workbench` 尚未存在）於是連鎖都取不到，
	// 而那條路徑會被當成「環境有問題」拒絕啟動。
	//
	// 這一步不違反「取鎖之前不得動任何 state」：MkdirAll 對已存在的目錄是 no-op
	// （不改 mtime、不建任何檔案），被擋下的第二個實例因此仍然零磁碟異動；建出
	// 來的也只是一個空目錄，不含任何 session 狀態。除此之外的任何初始化都必須
	// 排在取得 lease 之後。
	if merr := os.MkdirAll(a.stateDir, 0o755); merr != nil {
		return nil, fmt.Errorf("建立 state directory %s 失敗：%w", a.stateDir, merr)
	}
	lock, err := singleinstance.Acquire(a.stateDir)
	if err != nil {
		return nil, err
	}
	a.lease = &stateLease{stateDir: a.stateDir, lock: lock}
	return a.lease, nil
}

// instanceLeaseBlocker：取不到 lease 時 UI 橫幅要看到的內容。
func instanceLeaseBlocker(stateDir string, err error) string {
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return "Workbench 已在執行中（" + stateDir + "）：另一個實例正持有這個 workspace 的" +
			"單一實例鎖，本次啟動沒有開啟任何 session 狀態。請切換到已經開著的視窗。"
	}
	return "無法取得 workspace 的單一實例鎖（" + stateDir + "）：" + err.Error() +
		"；為了不讓兩個實例同時寫同一份稽核與 registry，取不到鎖一律拒絕開啟 session 狀態。"
}

func (a *App) startup(ctx context.Context) {
	// startup 自己也在 lifecycle 之內：收尾已經開始就一個 writer 都不開，而且
	// shutdown 會等這一段收斂才決定要不要釋放 lease（見 startupRunning 欄位）。
	if !a.beginStartupLifecycle() {
		// 被拒：收尾已開始，或已經有另一個 startup owner。兩種都不得開任何
		// writer；被跳過的步驟不能無聲，所以留一則橫幅（audit 此刻不一定開著）。
		a.noteStartupBlocker("拒絕啟動：收尾已開始，或這個實例已經啟動過一次——" +
			"啟動序列只執行一次，本次不開啟任何 session 狀態。")
		return
	}
	defer a.endStartupLifecycle()
	a.ctx = ctx
	// canonical state directory 解析完成後的**第一件事**：取得 ownership lease。
	//
	// 這裡自行取得／驗證而不是相信 caller：startup 是可以被直接呼叫的
	// （`runWailsUI(NewApp())` 就是這個形狀），保護資料正確性的東西不能放在
	// 呼叫端的編排慣例裡。acquireStateLease 冪等，所以 runWailsUI 先取過也不
	// 會在這裡自我排斥。
	lease, lerr := a.acquireStateLease()
	if a.workspaceErr != nil { // fail loud：UI 與 audit 都要看得到
		a.setStartupErrOnce("workspace init failed: " + a.workspaceErr.Error())
	}
	if lerr != nil {
		// **fail closed**：registry／audit／events sink／replay index／wire log
		// ／SegmentSet／migration 一個都不開，一個 byte 的 state 都不寫。
		// a.lease 維持 nil，所以 shutdown 也不會去釋放別人的鎖。
		a.noteStartupBlocker(instanceLeaseBlocker(a.stateDir, lerr))
		return
	}
	if !a.openStateWriters(lease) {
		return
	}
	a.startupAfterWriters()
}

// openStateWriters：啟動期 state writer 的開啟點（registry／audit／events
// sink／replay index／wire log／SegmentSet／Manager／restore store）。
//
// **不是全部的 writer**：evidence journal／CAS／worktree registry（startupEvidence）
// 與 gate／escalation journal 走各自的惰性初始化，只是路徑同樣綁在受 lease 保護的
// a.stateDir 底下（見 ensureGate 的 doc）。這裡不宣稱唯一——先前的 doc 這樣寫，
// 但它不是真的。
//
// 必須出示對得上 a.stateDir 的 lease，否則一個檔案都不開、直接回 false
// （fail closed）。這是「沒有 lease 就無法呼叫 writer initializer」那條契約的
// 落點：拿掉 lease 參數或改成從 a 自己讀，capability 就退化成註解。
func (a *App) openStateWriters(lease *stateLease) bool {
	if !lease.ownsStateDir(a.stateDir) {
		a.noteStartupBlocker("拒絕開啟 session 狀態：沒有這個 state directory 的 ownership lease（" +
			a.stateDir + "）")
		return false
	}
	// 目錄骨架排在 lease 檢查之後、任何 writer 之前（見 resolveWorkspace 的
	// doc：取鎖之前只允許建出空的 state directory）。建不出來一律 fail closed
	// ——recordings/ 不存在時錄流會在每個 session 啟動時才失敗，那是更難診斷的
	// 晚期失效。
	for _, d := range stateSubdirs {
		if merr := os.MkdirAll(filepath.Join(a.stateDir, d), 0o755); merr != nil {
			a.noteStartupBlocker("拒絕開啟 session 狀態：建立 " + d + " 目錄失敗（" +
				filepath.Join(a.stateDir, d) + "）：" + merr.Error())
			return false
		}
	}
	// audit **排在所有 writer 之前**：它開不起來就要 return false，排在後面的話
	// 前面已經開的 handle（registry）沒有人關——那個 fd 與它建出來的
	// sessions.json 會一路活到 process 結束（review 🟡6）。
	//
	// 開啟失敗**阻擋 startup**（owner 2026-08-18）：lease 已經在手、已經進入
	// writer initialization，此時寫不出稽核就不是「這次不記錄」而是後續每一步都
	// 失去可稽核性。這裡是 unavailable → ready 的唯一轉換點。
	f, ferr := os.OpenFile(filepath.Join(a.stateDir, "audit.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if ferr != nil {
		a.noteStartupBlocker("拒絕開啟 session 狀態：稽核寫入器開啟失敗（" +
			filepath.Join(a.stateDir, "audit.jsonl") + "）：" + ferr.Error())
		return false
	}
	a.auditMu.Lock()
	a.auditF = f
	a.auditState = auditReady
	a.auditMu.Unlock()
	if r, rerr := claude.OpenRegistry(filepath.Join(a.stateDir, "sessions.json")); rerr == nil {
		a.registry = r
	} else {
		a.setStartupErrOnce("registry init failed: " + rerr.Error())
	}
	sink, serr := appcore.NewJSONLSink(filepath.Join(a.stateDir, "events.jsonl"))
	if serr != nil {
		a.setStartupErrOnce("event sink init failed: " + serr.Error())
		sink = nil
	}
	var auditSink appcore.AuditSink = sink
	if sink == nil { // manager 必須存在；sink 失敗已 fail loud 於 startupErr
		auditSink = failedSink{reason: serr}
	}
	a.eventSink = sink
	// replay index（§3.5）：必須在 Manager 之前開，才能一併接進 Config.Index。
	// 開檔／checkpoint 解析失敗**不阻擋啟動**——index 是快取，audit 權威不受
	// 影響；但要 fail loud（audit ＋ 啟動警告），因為此後 §3.8 視窗化載入與
	// incomplete turn 偵測都會停用。sink 失敗時一併不接：沒有可信的 receipt
	// 來源，index 只會累積錯的 offset。
	if sink != nil {
		idx, ierr := replayindex.OpenWith(filepath.Join(a.stateDir, "replay-index"),
			replayindex.Config{Notify: a.onIndexDegraded})
		if ierr == nil {
			a.replayIndex = idx
		} else {
			a.audit("replay_index_open_error", map[string]any{"error": ierr.Error()})
			a.noteStartupWarning("replay index 開啟失敗（視窗化載入停用，稽核不受影響）：" + ierr.Error())
		}
	}
	// §3.4.4 session 級錄流證據：必須在任何 codex session 起得來之前開，且它自己
	// 就是跨 app 重啟的載入點（磁碟上前幾次執行的 segment 在這裡 replay 回來）。
	a.openWireSegments(lease)
	a.manager = appcore.New(appcore.Config{
		Sink: auditSink,
		Emit: func(env contract.Envelope) { a.emit("workbench:event", env) },
		// Task 4 live probe VERDICT=per-turn（turn2 output 9 << turn1 642）→ 累加制
		ClaudeUsageCumulative: false,
		Index:                 indexOrNil(a.replayIndex),
	})
	hw, _, _ := auditHighWatermark(a.eventsPath())
	rs, rserr := openRestoreStore(filepath.Join(a.stateDir, "restore.json"), hw)
	a.restore = rs
	if rserr != nil { // malformed 重建等一律 fail loud（不無聲）
		a.audit("restore_store_warning", map[string]any{"error": rserr.Error()})
	}
	return true
}

// startupAfterWriters：writer 全部開好之後的啟動序列（migration／還原／
// watcher／evidence）。只有 openStateWriters 回 true 才會走到——契約要求
// migration 也在「取得 lease 之後」。
func (a *App) startupAfterWriters() {
	// 舊路徑遷移排在**所有惰性 journal 開啟之前**（見 migrateLegacyState）。
	// 失敗一律中止啟動：a.wsReg 維持 nil，CreateSession 早退（§3.2.6）。
	if !a.migrateLegacyState() {
		return
	}
	// M3b §3.2.4 完整凍結序列：載入／遷移 registry → 還原 dormant slots →
	// 驗證／重建 replay index → 偵測 incomplete turn → emit stream_error →
	// 才開放 UI 與 provider 啟動。必須在 restore store 開啟之後（legacy 遷移
	// 的來源）與 manager 建立之後。失敗一律 fail loud：a.wsReg 維持 nil，
	// CreateSession 早退，不以猜測的狀態繼續（§3.2.6）。
	if _, lerr := a.restoreSessions(); lerr != nil {
		_ = a.noteRegistryUncertainErr("registry_load", "", lerr) // 只補稽核，錯誤處置不變
		a.audit("session_registry_error", map[string]any{"error": lerr.Error()})
		a.noteStartupBlocker("session registry load failed: " + lerr.Error())
	}
	toolsDir, toolsSource := resolveToolsDir(a.workspaceDir)
	a.publishToolsDir(toolsDir, toolsSource)
	if h := a.hookStartupPublish; h != nil { // 測試 barrier：欄位發布期間（見欄位 doc）
		h()
	}
	a.publishNodePath(resolveNodePath())
	si := a.startupSnapshot() // 稽核用同一份快照，不逐欄位各取一次鎖
	a.audit("startup", map[string]any{"workspace": a.workspaceDir, "workspace_source": a.workspaceSrc,
		"startup_error": si.startupErr, "node_path": si.nodePath,
		"tools_dir": si.toolsDir, "tools_source": si.toolsSource, "node": nodeVersionOf(si.nodePath)})
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
	// **ready 設在 reconcile 之前**：reconcileGate1NotifyOnly 自己就要開 gate
	// journal，而惰性初始化只認 blocked latch、不認 phase（內部呼叫者不走
	// beginTxn）。先進 ready 讓兩者的判準一致：走到這裡代表遷移已完成、
	// writer 已開，state 操作可以放行。
	a.setPhase(phaseReady)
	if _, statErr := os.Stat(filepath.Join(a.stateDir, "gate.jsonl")); statErr == nil {
		a.reconcileGate1NotifyOnly()
	}
}

// legacyStateNames：M3b 之前落在 workspaceDir/.workbench、本次改綁 a.stateDir 的
// app state。三者都會改變核可結果，所以都要遷移而不是各自重新開一份空的：
//
//	gate.jsonl       → 已核可／已 stale 的 gate 記錄。
//	escalation.jsonl → 阻擋事項收件匣。**未解除的系統管控項目在這裡**——漏掉它
//	                   等於把原本擋著的核可默默放行。
//	evidence/        → evidence journal／CAS／worktree registry。TCA policy 直接
//	                   讀 CAS，看不到就等於證據不存在。
var legacyStateNames = []string{"gate.jsonl", "escalation.jsonl", "evidence"}

// migrateLegacyState：把舊路徑（workspaceDir/.workbench）上的 app state 搬到受
// ownership lease 保護的 a.stateDir。回傳 false ＝**啟動必須中止**。
//
// 為什麼一定要遷移而不是忽略（owner 2026-08-19）：舊 escalation journal 裡若還有
// 未解除的系統管控項目，忽略它就是「這一版之後那些阻擋全部消失」，核可結果因此
// 改變——這不是相容性問題，是資料正確性問題。
//
// 位置：**取得 lease 之後、任何新 journal 開啟之前**。前者是因為搬檔本身就是
// state mutation，沒有 ownership 不得進行；後者是因為 ensureGate／ensureEscalation
// ／startupEvidence 一旦先開過，目的地就會憑空多出一份空的，遷移從此永遠撞上
// 「來源與目的地同時存在」。
//
// 兩種失敗都中止啟動、都說明原因，**不自行選一份**：
//
//	來源與目的地同時存在 → 兩份都可能有唯一的記錄，合併語意（哪一份的 stale
//	                       marker 有效、同一個 escalation id 以誰為準）沒有定義。
//	                       猜錯的後果是核可判定用了錯的證據，而且沒有人會發現。
//	rename 失敗          → 搬到一半的狀態無法自證，繼續啟動只會在錯的資料上跑。
//
// 常態（stateDir ＝ workspaceDir/.workbench）直接跳過：同一個路徑沒有東西要搬。
// 兩者不同值只發生在 resolveWorkspace 的 tmp fallback 與測試自訂 stateDir。
func (a *App) migrateLegacyState() bool {
	legacyDir := filepath.Join(a.workspaceDir, ".workbench")
	if legacyDir == a.stateDir || a.workspaceDir == "" {
		return true
	}
	for _, name := range legacyStateNames {
		src := filepath.Join(legacyDir, name)
		if _, serr := os.Lstat(src); serr != nil {
			if os.IsNotExist(serr) {
				continue
			}
			return a.blockLegacyMigration(name, "無法讀取舊路徑（"+src+"）："+serr.Error())
		}
		dst := filepath.Join(a.stateDir, name)
		if _, derr := os.Lstat(dst); derr == nil {
			return a.blockLegacyMigration(name, "舊路徑（"+src+"）與新路徑（"+dst+
				"）同時存在，無法判斷哪一份是權威；請人工確認後保留一份再重新啟動")
		} else if !os.IsNotExist(derr) {
			return a.blockLegacyMigration(name, "無法讀取新路徑（"+dst+"）："+derr.Error())
		}
		if rerr := os.Rename(src, dst); rerr != nil {
			return a.blockLegacyMigration(name, "從 "+src+" 搬到 "+dst+" 失敗："+rerr.Error())
		}
		a.audit("legacy_state_migrated", map[string]any{"name": name, "from": src, "to": dst})
	}
	return true
}

// blockLegacyMigration：遷移失敗的唯一出口——稽核 ＋ UI 橫幅，回 false 讓
// startupAfterWriters 原地中止。
func (a *App) blockLegacyMigration(name, why string) bool {
	msg := "拒絕啟動：" + name + " 的舊路徑遷移未完成——" + why +
		"。在遷移完成之前不開放 session，也不開放核可與阻擋事項操作，" +
		"以免用不完整的記錄繼續執行。"
	// **latch 與 phase 先設，audit 與橫幅後發**：兩者都要取鎖、寫檔，中間的每
	// 一微秒都是「binding 還進得來」的窗口（reviewer 2026-08-19）。光是中止啟動
	// 序列也不夠：Wails binding 仍掛著，UI 一按就會經惰性初始化把新路徑的
	// journal 開起來（見 stateBlocked 欄位 doc）。
	a.blockStateBindings(msg) // 原因與 phase 在同一個臨界區內一起設
	a.audit("legacy_state_migration_blocked", map[string]any{"name": name, "reason": why})
	a.noteStartupBlocker(msg)
	return false
}

// startupEvidence（Task 20）：惰性 gate/plan 之外少數在 startup 就建立的狀態——
// evidence journal／CAS／worktree registry 路徑都落在 <stateDir>/evidence/
// 下，且 CleanupOrphans／CleanOrphanTemps 必須在任何 RunEvidence 呼叫之前跑
// 過一次，才能收乾淨上次程序異常結束留下的 worktree／temp 殘留（brief 凍結：
// 下次啟動兜底逾時 forcedShutdown 未清乾淨的窗口）。liveIDs 傳空 map：啟動當下
// 不可能有任何 in-flight run，registry 裡的每個 active 項目都是孤兒。這幾步
// 全是檔案操作＋（僅在真有孤兒殘留時）git worktree 指令；一個尚未 git init 的
// 全新 workspace（registry 檔不存在或是空檔）不會觸發任何 git 呼叫，因此可以
// 安全地在 ensureGate() 惰性初始化之前無條件執行。
func (a *App) startupEvidence() {
	// 收尾已經開始就不要再建 writer。這是**減少損害**，不是不變量本身——真正
	// 保住不變量的是「startup 沒停就不釋放 lease」（見 shutdown 第 1a／13 步）。
	if a.phaseNow() == phaseShuttingDown {
		return
	}
	// 綁 a.stateDir（受 ownership lease 保護），不是 workspaceDir/.workbench
	// ——理由見 ensureGate 的 doc（tmp fallback 下兩者不同值）。舊路徑上的殘留由
	// migrateLegacyState 在啟動更早的一步處置，這裡不再讀 workspaceDir。
	dir := filepath.Join(a.stateDir, "evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.setStartupErrOnce("evidence dir init failed: " + err.Error())
		return
	}
	a.evidenceCASDir = filepath.Join(dir, "cas")
	a.evidenceRegistryPath = filepath.Join(dir, "worktrees.jsonl")
	if j, jerr := evidence.OpenJournal(filepath.Join(dir, "evidence.jsonl")); jerr != nil {
		a.setStartupErrOnce("evidence journal init failed: " + jerr.Error())
	} else {
		a.evidenceJournal = j
	}
	if oerr := evidence.CleanupOrphans(a.workspaceDir, a.evidenceRegistryPath, map[string]bool{}); oerr != nil {
		a.setStartupErrOnce("evidence orphan worktree cleanup failed: " + oerr.Error())
	}
	if _, terr := evidence.CleanOrphanTemps(a.evidenceCASDir); terr != nil {
		a.setStartupErrOnce("evidence orphan temp cleanup failed: " + terr.Error())
	}
}

// failedSink：events.jsonl 開檔失敗時的替身——每次寫入回同一錯誤，
// Manager 會 latch 並以 stream_error fail loud（不無聲丟稽核）。
type failedSink struct{ reason error }

func (s failedSink) Write(contract.Envelope) (appcore.AppendReceipt, error) {
	return appcore.AppendReceipt{}, s.reason
}
func (s failedSink) Close() error { return nil }

// ReadDiagram：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) ReadDiagram() (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.readDiagram()
}

// ReadDiagram 回傳目前圖檔內容（Mermaid pane 初始載入）。
func (a *App) readDiagram() (string, error) {
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

// shutdown：§3.6.5 的凍結總序。每一步跑完發一次 shutdownStep（測試注入的
// hookShutdownStep 是唯一讀者，見 TestShutdownFollowsFrozenOrder）——順序本身是
// 契約，不是實作細節。
//
// **與 spec 的一處對齊（M3b Task 24）**：Codex wire log 的 finalize 原本排在
// Manager.Close **之後**，現改為之前。理由是 finalize 那一步會先 terminate 共用
// app-server，所以它是**事件生產者**，必須排在 sink 關閉之前——這也正是原本註解
// 寫的「全部 finalize 之後才關 sink」的本意。
//
// 修掉的**不是** wire log 本身（§3.4.2「關閉期間最後的 frame 不得漏錄」在兩種順序
// 下都成立：完整性由 FinalizeWith 內部的 drain→detach→finalize 自己保證，與
// Manager.Close 的相對位置無關），而是**由那批 frame 導出的 events.jsonl 稽核事件**：
// Manager 關閉後 emitCodexBroadcast／emitCodexUnknownBroadcast／Manager.Emit 一律走
// emitClosedDroppedLocked——只發一則 UI stream_error、不寫 sink。（歸屬失敗那一類另有
// failLoudCodexDispatch 自己的 a.audit，走獨立的 audit.jsonl，不受影響。）
//
// 新順序有 happens-before 保證、不只是「比較合理」：Conn.readLoop 把 notification
// handler **同步**跑在 readLoop goroutine 上，而 FinalizeWith 的 drainStdout 等的正是
// Conn.Done()（readLoop 讀到 EOF 才關）——所以 FinalizeWith 返回時，末段 frame 的
// dispatcher 回呼都已經返回，那些事件必然落在 Manager.Close() 之前。
//
// （已知殘留：OnServerRequest 走 `go c.handleServerRequest(...)`，那條 approval
// goroutine 仍可能在 Manager.Close() 之後 emit。修它要動 internal/codex，不在本 task
// 範圍。）
//
// 反向依賴不存在：FinalizeWith 只寫自己的 wire log 檔與 meta，不經過 Manager。
//
// 收斂上界（**不隨 session 數放大**）：
//   - interrupt／terminate ≤ 5s：Claude 的 proc.Terminate 非阻塞（SIGTERM 後由
//     背景 goroutine 做 grace→SIGKILL 升級）；Codex 的 turn/interrupt RPC 自帶
//     5s ctx，8 個 session 全部並行。
//   - per-session teardown ≤ 15s：CloseSequence 的 quiesce 5s ＋ kill 10s，逐 host
//     並行——4 個卡死的 Claude 仍是**單一** 15s 窗口，不乘以 session 數。Codex 側的
//     teardown 沒有等待（共用 conn，不關 server）。
//   - 共用 app-server 收尾 ≤ 10s：FinalizeWith 內的 Terminate→Wait（proc grace 5s
//     後 SIGKILL）＋ 等 stdout 汲取的 wireDrainTimeout 5s。**只有一份**——4 個 Codex
//     session 共用同一條 codex.Conn 與同一個 app-server，不是每個 session 各一份。
//   - Manager.Close／index flush／registry Sync：純檔案 I/O，無等待。
//
// 合計約 30s 上界，與 session 數無關（§5.4：「8 session 含一個卡死 Claude →
// shutdown 總時間仍為單一 bounded window」）。
func (a *App) shutdown(ctx context.Context) {
	a.shutMu.Lock()
	a.phase = phaseShuttingDown // 1) 拒新 StartSession／ensureAppServer／SpecAssist／EndSession／NewSession（review P1）
	a.shutMu.Unlock()
	// 立刻 cancel assist 根 context：**排在 reclaimAssists 之前**，因為 reclaim
	// 掃的 registry 可能還沒收到剛進場那一筆（見 procRootCtx 的 doc）。
	a.cancelProcRoot()
	a.shutdownStep("reject_new_txn")

	// 1a) 等 startup 收斂。startup 不是 binding，phase 擋不住它——它可能正停在
	// 半途，稍後繼續建立 evidence journal 之類的 writer。等不到就記下來，第 13
	// 步據此**不釋放 lease**（處理原則同背景 frame worker）。
	// **等不到就直接進 retained 結局，不再往下收任何資源**（reviewer 2026-08-19）：
	// 先前只是記下來繼續跑，於是 watcher 被停、manager／wire 被關，晚到的 startup
	// 隨後又把 spec／plan watcher 重新建起來——資源狀態比不收更糟。retained 路徑
	// 的原則是「保留所有 startup 仍可能用到的東西，交由 process exit 一起回收」。
	if !a.awaitStartupStopped(a.startupDrainWindow()) {
		a.audit("instance_lease_retained", map[string]any{
			"startupStopped": false,
			"reason":         "startup 未在收尾窗口內結束",
			"note": "lease、session registry、audit writer 與所有 startup 仍可能使用的資源" +
				"一併留到 process 結束，由作業系統回收"})
		a.shutdownStep("instance_lease_retained")
		return
	}

	a.stopSpecWatch()       // 2) 停 spec/ watcher：先收斂，避免與後續 manager.Close() 競態
	a.stopPlanWatch()       // 2a) 停 plan/ watcher，同上理由
	a.reclaimAssists()      // 2b) cancel 每個 in-flight SpecAssist（bounded：runner 界限內退出）
	a.reclaimEvidenceRuns() // 2c) cancel 每個 in-flight RunEvidence（task-20：同上理由，必須早於 inflight.Wait）
	a.cancelRebuild()       // 2d) 取消 replay index 重建重試迴圈並等它收斂（見其 doc；必須早於 inflight.Wait）
	a.inflight.Wait()       // 2e) 等已取得 ownership 的交易（含 assist teardown 的 endTxn）完成
	a.shutdownStep("stop_watchers")

	// 3) snapshot：host 與 pending approval 各取一份快照，之後的每一步都用這兩份。
	//
	// 兩份快照的完整性保證**不同，不要混為一談**：
	//   - host：先拒新交易（reject_new_txn）再快照，之後不可能有新的 host 進來
	//     ——建立 host 的入口一律經 beginTxn。
	//   - approval：**沒有這個保證**。registerApproval 的兩條來源（Claude 的
	//     pumpApprovals、Codex 的 OnServerRequest → codexApproval）都不經
	//     shuttingDown 閘門，快照之後仍可能登記新的 approval，那些不會拿到下一步
	//     的顯式 deny。它們仍是 fail-closed，但兜底的是各自的 broker／RPC timeout
	//     與子行程退出，不是這裡的 deny。
	hosts := a.snapshotHosts()
	apprIDs := a.pendingApprovalIDs(nil)
	a.shutdownStep("snapshot")

	// 4) 全部 approval fail-closed deny（§3.6.5）。單筆失敗不中斷其餘筆，也不中斷
	// 後續收尾——與 §3.6.3 的移除路徑同一裁決（「deny 部分失敗時仍 terminate provider」）。
	if err := a.denyApprovals(apprIDs, "shutdown"); err != nil {
		a.audit("shutdown_deny_approvals_error", map[string]any{"error": err.Error()})
	}
	a.shutdownStep("deny_approvals")

	// 5-7) interrupt／terminate → 並行 teardown → 全部 host 收乾（步驟名由 forcedShutdown 發出）。
	// pumpsStopped 與 err 是兩種狀態，分開處置（見 forcedShutdown 的 doc 與第 13 步）。
	pumpsStopped, ferr := a.forcedShutdown(hosts)
	if ferr != nil {
		a.audit("shutdown_forced_error", map[string]any{"error": ferr.Error()})
	}

	// 8-9) 共用 app-server：terminate → wait（Conn.Done）→ finalize wire log。
	// §3.4.2 的收尾總序（terminate → wait → stdout 汲取完成 → detach → finalize
	// wire log）全在 FinalizeWith 內，與死亡 reaper／受控 replacement 共用同一份
	// 實作；冪等，watcher 已收過就直接回原結果。Take 取出即清空 ownership，無後續
	// 回填（shuttingDown 已擋掉所有會重建 server 的交易）。
	a.shutdownStep("server_terminate_wait")
	if o, ok := a.codexSingle.Take(); ok {
		if err := o.FinalizeWith(nil); err != nil {
			a.audit("shutdown_wire_log_finalize_error", map[string]any{"error": err.Error()})
		}
	}
	a.shutdownStep("wirelog_finalize")

	// §3.4.4：segment journal 的 handle 收尾。排在 wirelog_finalize 之後、
	// manager_close 之前——最後一批 Append 發生在步驟 5-7 的 codexTeardown 裡，
	// 這時已經全部收斂。刻意不另立 shutdownStep：關一個 append-only handle 沒有
	// 順序契約要守，加一個步驟名只會讓既有的總序斷言多一個無意義的節點。
	// §3.4.3：歷史歸屬展開的 bounded drain（owner 契約第 6 條）。**排在
	// wireSegments.Close 之前**——drain 期間 worker 不碰 SegmentSet（job 自帶
	// segments 快照），但把 handle 關在還可能有 append 的東西之前就是找麻煩。
	// 未完成的工作留在 job journal，下次啟動補完；但 worker 有沒有真的退出要往
	// 下傳——它還活著時**不得釋放 lease**（見 drainWireFrameJobs 的 doc 與第 13 步）。
	workerStopped := a.drainWireFrameJobs()
	a.closeWireFrameJobs()
	if a.wireSegments != nil {
		if err := a.wireSegments.Close(); err != nil {
			a.audit("wire_segments_close_error", map[string]any{"error": err.Error()})
		}
	}

	// 10) 全部事件生產者都已停止，才關 sink（pending queue abort+flush 兜底）
	if a.manager != nil {
		_ = a.manager.Close()
	}
	a.shutdownStep("manager_close")

	// 11) replay index flush／checkpoint（§3.6.5 總序：**在 Manager.Close 之後**）。
	// Manager.Close 會 flush pending queue，那些事件仍要進 index，所以 index
	// 不得在它之前停止接收。
	//
	// 為什麼正常關閉需要這一步：checkpoint 落盤被節流到 turn boundary（見
	// replayindex.Observe），關閉當下若有 WSID 的 turn 還開著，磁碟 checkpoint
	// 會停在上一個 boundary——效果跟 crash 一樣，得靠下次啟動的補掃修復。正常
	// 關閉不該依賴 crash 修復機制。Index 沒有 Close：turn 檔逐次開關、checkpoint
	// 以 atomic rename 落盤，Flush 之後沒有待關閉的 handle。
	//
	// index 處於 unverified latch 時 Flush 是 no-op（見 replayindex 的欄位
	// doc：沒對過帳的 checkpoint 落盤只會把缺口固化）。被跳過的步驟不能無
	// 聲，這裡照樣留一筆稽核。
	if a.replayIndex != nil {
		if a.replayIndex.Unverified() {
			a.audit("replay_index_flush_skipped", map[string]any{
				"reason": "index 未通過啟動驗證，checkpoint 留給下次啟動對帳"})
		} else if err := a.replayIndex.Flush(); err != nil {
			a.audit("replay_index_flush_error", map[string]any{"error": err.Error()})
		}
	}
	a.shutdownStep("index_flush_close")

	// 12) session registry Sync：tombstone／entry 的最後一次落盤（§3.6.5 末步）。
	// uncertain latch 期間刻意跳過、只留稽核（同上方 index_flush 的處置形狀）：
	// 記憶體現況本身已經不知道是不是磁碟上那一份，再寫一次只會把未知固化。
	if a.wsReg != nil {
		if a.registryUncertain() {
			a.audit("shutdown_registry_sync_skipped", map[string]any{
				"reason": "registry commit 結果不確定，最終落盤交給下次啟動以磁碟內容為準"})
		} else if err := a.noteRegistryUncertainErr("shutdown_sync", "", a.wsReg.Sync()); err != nil {
			a.audit("shutdown_registry_sync_error", map[string]any{"error": err.Error()})
		}
	}
	a.shutdownStep("registry_sync")

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

	// 13) 關 audit writer → 釋放 single-instance ownership lease：**總序的最後兩步**。
	//
	// 排在這裡不是美觀問題：manager（events sink）／replay index checkpoint／
	// session registry／wire segments 的最後一次落盤全部在上面幾步，任何提早
	// 釋放都會讓第二個 process 在我們還在寫的時候進來，AppendReceipt 的 offset
	// 與 registry 的 temp/rename 前提就破了。
	//
	// **要守的不變量是「lease 釋放之後不得再有任何 state mutation」**，不是 fd
	// 的開關狀態（owner 2026-08-19）。所以釋放 lease 的前提是**兩個背景 writer
	// 都確定停了**，不是其中一個：
	//
	//	workerStopped → 背景 frame 展開 worker（寫 audit.jsonl、覆寫 sidecar）
	//	pumpsStopped  → 每個 Claude host 的 pump goroutine（寫 events sink、
	//	                經 init 綁定改寫 sessions.json）
	//
	// startup 本身也是一種背景 writer，但它在第 1a 步就處理掉了——等不到就直接
	// 早退，連下面這些收尾都不做。
	//
	// 任一為 false 就走保留路徑：**一律不手動釋放**，process 在仍持鎖的狀態下
	// 結束，OS 同時回收 kernel lock 與 fd，殘留寫入因此永遠落在「我們仍持有
	// lease」的期間內。這條路徑刻意**不關 registry、也不關 audit writer**——把
	// 它們關掉只會讓還沒退出的 writer 從「合法寫入」變成「靜默消失」或多一筆
	// 沒人處理的錯誤，而磁碟事實的正確性靠的是鎖還在，不是 handle 關了。
	//
	// contract §5：不得為了滿足 shutdown timeout 而提早釋放 lease。
	if !workerStopped || !pumpsStopped {
		a.audit("instance_lease_retained", map[string]any{
			"workerStopped": workerStopped, "pumpsStopped": pumpsStopped,
			"reason": "背景 writer 未在收尾窗口內退出（frame 展開 worker／Claude pump）",
			"note":   "lease、session registry 與 audit writer 一併留到 process 結束，由作業系統回收"})
		a.shutdownStep("instance_lease_retained")
		return
	}
	// 正常收尾順序（owner 2026-08-19）：確認背景工作已結束 → 關 session
	// registry → 關 audit writer → 釋放 lease。registry 排在 audit 之前，遲到的
	// Bind 才有地方留下 ErrRegistryClosed 那筆稽核。
	a.closeSessionRegistry()
	a.closeAuditWriter()
	// 釋放之後把 a.lease 設 nil：lease 是 capability，收掉之後任何 writer
	// 初始化都必須重新取得（Lock.Held() 在 Release 之後為 false，這裡再補一層
	// 讓重複 shutdown 也安全）。startup 取鎖失敗那條路徑 a.lease 本來就是 nil，
	// 所以不會誤放別人的鎖。
	//
	// 釋放失敗只能走 stderr：audit writer 已經關了，而這一刻再開回去寫就是我們
	// 正要禁止的那件事（lease 邊界之後的 state mutation）。
	if err := a.lease.release(); err != nil {
		fmt.Fprintln(os.Stderr, "sdlc-workbench: 釋放 single-instance lease 失敗："+err.Error())
	}
	a.lease = nil
	a.shutdownStep("instance_lease_release")
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
//
// M3b §3.6.5（Task 24）：拆成兩段，對齊凍結總序「全部 session 先 interrupt／
// terminate → per-session teardown 並行」。兩段各自並行、各自等收斂：第一段只發
// 訊號（Claude 的 proc.Terminate 非阻塞、Codex 的 interrupt RPC 自帶 5s ctx），
// 第二段才是會等 quiesce／kill 的 CloseSequence。hosts 由呼叫端在 snapshot 那一步
// 取得（同一份快照貫穿整段 shutdown），不在這裡重新掃 registry。
//
// # 兩個回傳值是**兩種不同的狀態**，不可互相代替（owner 2026-08-19）
//
//	pumpsStopped → 每個 Claude host 的 pump goroutine 是否都確實結束。
//	err          → 收尾途中的錯誤（CloseSequence／lease finalize／錄流 meta…）。
//
// 檔案收尾失敗與「goroutine 還活著」是兩件事：前者代表這次收尾留下不完整的
// 證據，後者代表**還有一個 writer 在跑**，lease 因此不得釋放（見 shutdown 第
// 13 步）。把 err != nil 當成「pump 還活著」會讓每一次 meta 寫入失敗都白白保住
// lease；反過來拿 err == nil 當成「pump 都停了」則會在真的有殘留 goroutine 時
// 放行——那正是這次要堵的洞。所以兩者分開回報。
func (a *App) forcedShutdown(hosts []*sessionHost) (pumpsStopped bool, err error) {
	var claudeHosts, codexHosts []*sessionHost
	for _, h := range hosts {
		switch h.provider {
		case contract.ProviderClaude:
			if h.sess == nil || h.teardownFn == nil { // 未完成 publish 的 host 不可能存在（見 sessionHost doc）
				continue
			}
			claudeHosts = append(claudeHosts, h)
		case contract.ProviderCodex:
			codexHosts = append(codexHosts, h)
		}
	}

	// 第一段：全部 session 先 interrupt／terminate（並行；不做任何收尾動作）。
	var iwg sync.WaitGroup
	for _, ch := range claudeHosts {
		iwg.Add(1)
		go func() {
			defer iwg.Done()
			_ = ch.sess.Terminate() // 加速後續 CloseSequence 的 quiesce
		}()
	}
	// codex 側：所有 session 共用同一條 conn 與長駐 server，因此這裡不 Terminate
	// server（shutdown() 在全部 host 收乾之後才 Take＋Terminate），只 interrupt
	// 各自的 active turn（best effort，逾時不影響收尾）。
	for _, ch := range codexHosts {
		iwg.Add(1)
		go func() {
			defer iwg.Done()
			if ch.runner == nil || ch.runner.ActiveTurnID() == "" {
				return
			}
			a.mu.Lock()
			conn := a.codexConn
			a.mu.Unlock()
			if params, perr := ch.track.InterruptParams(); perr == nil && conn != nil {
				ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = conn.Call(ictx, codex.MethodTurnInterrupt, params)
				cancel()
			}
		}()
	}
	iwg.Wait()
	a.shutdownStep("interrupt_terminate")

	// 第二段：per-session teardown 並行（goroutine＋WaitGroup 收斂；不得逐 session
	// 串行——4 個卡死的 Claude 必須共用單一 bounded window）。步驟名在 goroutine
	// 啟動**之前**發出，才與 per-host 的 hookTeardownEntered 有確定性順序。
	var wg sync.WaitGroup
	errs := make([]error, len(claudeHosts)+len(codexHosts))
	a.shutdownStep("teardown_parallel")
	for i, ch := range claudeHosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer a.teardownHook(a.hookTeardownDone, ch.wsid)
			a.teardownHook(a.hookTeardownEntered, ch.wsid)
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
	for i, ch := range codexHosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer a.teardownHook(a.hookTeardownDone, ch.wsid)
			a.teardownHook(a.hookTeardownEntered, ch.wsid)
			if err := appcore.EndSessionFlow(a.manager, ch.wsid, nil, func() error {
				return a.codexTeardown(ch.wsid, ch)
			}); err != nil {
				terr := a.codexTeardown(ch.wsid, ch) // lifecycle 擋住：直接收（lease.Finalize 冪等）
				errs[len(claudeHosts)+i] = errors.Join(err, terr)
			}
		}()
	}
	wg.Wait() // 每一邊都必須被等待
	// 步驟名是 §3.6.5 的凍結字串（第 7 步「全部 Codex session host 收完」），但這裡
	// **實際等的是全部 host、含 Claude**——單一 WaitGroup，Codex 收乾必然涵蓋其中。
	// 刻意不拆成兩個 WaitGroup 只為讓 Claude 與共用 app-server 收尾重疊那幾秒：多一
	// 份狀態、換不到 bounded window 的改善（Manager.Close 本來就得等全部 host）。
	a.shutdownStep("codex_hosts_done")
	return a.claudePumpsStopped(claudeHosts), errors.Join(errs...)
}

// claudePumpsStopped：每個 Claude host 的 pump goroutine 是否都**確實**已結束。
//
// 判準是 host.pumpDone 這個 channel 有沒有關（appcore.Pump 在 event channel 收
// 乾之後才關它），不是 teardown 的回傳值——CloseSequence 會在 quiesce／kill 逾時
// 之後仍以 Exit{Exited:false} 盡力 finalize 並回錯，那條路徑上 pump 可能仍卡在
// 一次 Emit 裡沒退出。
//
// 「無法確認」一律當成未結束（fail closed）：pumpDone 為 nil 代表這個 host 沒有
// 可查證的 pump 收斂訊號，此時宣稱它停了就是在猜。Codex host 不在判定範圍——
// 它們沒有 per-host pump goroutine（共用同一條 conn 與 dispatcher）。
func (a *App) claudePumpsStopped(claudeHosts []*sessionHost) bool {
	stopped := true
	for _, ch := range claudeHosts {
		if ch.pumpDone != nil {
			select {
			case <-ch.pumpDone:
				continue
			default:
			}
		}
		stopped = false
		a.audit("shutdown_claude_pump_still_running", map[string]any{
			"wsid":      string(ch.wsid),
			"confirmed": ch.pumpDone != nil,
			"note":      "pump goroutine 未在收尾窗口內結束；保留 ownership lease"})
	}
	return stopped
}

// shutdownStep：§3.6.5 凍結總序的探針（同 startupStep／removeStep 慣例）。
func (a *App) shutdownStep(step string) {
	if a.hookShutdownStep != nil {
		a.hookShutdownStep(step)
	}
}

// teardownHook：per-session teardown 的進場／收斂探針（見 hookTeardownEntered doc）。
func (a *App) teardownHook(h func(appcore.WSID), w appcore.WSID) {
	if h != nil {
		h(w)
	}
}

// ---- helpers ----

// resolveWorkspace：env WORKBENCH_WORKSPACE → 可寫的 cwd（Finder 啟動時 cwd 是 "/"，
// 不可寫）→ home。第一個能建出 `.workbench` 的候選勝出。
//
// **只建那個空目錄，不建 recordings/ 與 probe/**（reviewer 2026-08-19 P1）：這個
// 函式跑在取得 ownership lease **之前**（acquireStateLease 要先知道鎖要綁在哪
// 裡），所以它建出來的每一個東西都是「取鎖前的 state mutation」。空的 state
// directory 是唯一允許的例外（見 acquireStateLease）；recordings/ 與 probe/ 是
// session 狀態的落點，改由 openStateWriters 在**出示 lease 之後**建立。
//
// 可寫性的候選判準因此從「建得出 recordings」變成「建得出 .workbench」——同樣
// 是一次真的 MkdirAll，擋得住唯讀候選，但不留下多餘的目錄。
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
		if merr := os.MkdirAll(st, 0o755); merr != nil {
			lastErr = merr
			continue
		}
		return n, st, c.src, nil
	}
	tmp := os.TempDir()
	st := filepath.Join(tmp, "sdlc-workbench", ".workbench")
	if merr := os.MkdirAll(st, 0o755); merr != nil {
		return tmp, st, "tmp", errors.Join(lastErr, merr)
	}
	return tmp, st, "tmp", lastErr
}

// stateSubdirs：session 狀態的目錄骨架。**必須在出示 lease 之後才建立**——
// 見 resolveWorkspace 的 doc。
//
//	recordings/ → 錄流檔（recorder.New 的落點）
//	probe/      → A2/A3 探針落點
var stateSubdirs = []string{"recordings", "probe"}

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

func (a *App) claudeCLIPath() string { return claudeCLIPathIn(a.toolsDir()) }

func (a *App) codexCLIPath() string { return codexCLIPathIn(a.toolsDir()) }

// *CLIPathIn：從一份**已經複製出來的** tools dir 組路徑（見 App.startupMu 的
// 規約：exec 一律在鎖外，用同一份快照）。
func claudeCLIPathIn(toolsDir string) string {
	return filepath.Join(toolsDir, "claude-cli", "node_modules", ".bin", "claude")
}

func codexCLIPathIn(toolsDir string) string {
	return filepath.Join(toolsDir, "codex-cli", "node_modules", ".bin", "codex")
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
func (a *App) childEnv() []string { return childEnvFor(a.nodePathValue()) }

func childEnvFor(nodePath string) []string {
	if nodePath == "" {
		return nil
	}
	return []string{"PATH=" + filepath.Dir(nodePath) + ":" + os.Getenv("PATH")}
}

func (a *App) nodeVersion() string { return nodeVersionOf(a.nodePathValue()) }

// nodeVersionOf：`node --version`。**參數是路徑而不是 *App**——這個函式會 exec 一
// 個外部行程，不得在持有 startupMu 的情況下呼叫（見欄位 doc）。
func nodeVersionOf(nodePath string) string {
	if nodePath == "" {
		return "missing (not on app PATH; codex CLI needs node)"
	}
	out, err := exec.Command(nodePath, "--version").Output()
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

func (a *App) cliVersion(provider string) string {
	return cliVersionFrom(a.startupSnapshot(), provider)
}

// cliVersionFrom：同 nodeVersionOf——先拿快照、放鎖之後才 exec。
func cliVersionFrom(si startupInfo, provider string) string {
	bin := claudeCLIPathIn(si.toolsDir)
	if provider == "codex" {
		bin = codexCLIPathIn(si.toolsDir)
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(), childEnvFor(si.nodePath)...) // codex CLI 是 node script
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// auditLifecycle：見 App.auditState 的 doc。
type auditLifecycle int

const (
	auditUnavailable auditLifecycle = iota // 尚未取得 lease／啟動被拒；或收尾已把 writer 收掉
	auditReady                             // lease 在手且 writer 已開
)

// 刻意只有兩態（owner 2026-08-19）。曾經有第三個 auditClosed，但它與
// auditUnavailable 的**行為完全相同**——兩者都讓 audit() 靜默丟棄，沒有任何分流
// 讀得出差別。同名不同義的狀態只是把「湊完整」寫成程式碼。真正需要被分辨的是
// session registry 那一側：關閉之後 Bind() 從寫磁碟改成回 ErrRegistryClosed，
// 兩種狀態各自有可觀察的正式行為（見 claude.Registry.Close）。
//
// 這裡的取捨是：稽核在 lease 釋放前後都不該再寫，丟棄本來就是對的；registry 的
// 遲到寫入則必須被呼叫端看見。

// closeSessionRegistry：shutdown 總序的「關 registry」那一步——排在確認背景工作
// 已結束之後、關 audit writer 之前（見 shutdown 第 13 步）。
//
// Close 與 Bind 共用 registry 內部同一把 mutex，所以它回來時「正在寫 sessions.json
// 的那一筆」必然已經落盤，之後任何遲到的 pump callback 只會拿到 ErrRegistryClosed。
// a.registry 刻意**不設 nil**：設 nil 會讓遲到的呼叫端走到「registry 不存在」那條
// 早退路徑，錯誤就靜默了，而我們要的正好相反。
func (a *App) closeSessionRegistry() {
	if a.registry == nil {
		return
	}
	if err := a.registry.Close(); err != nil {
		a.audit("session_registry_close_error", map[string]any{"error": err.Error()})
	}
}

// closeAuditWriter：shutdown 總序倒數第二步（contract §5 第 4 步）。**只有在確定
// 沒有殘留 writer goroutine 時才會被呼叫**——見 shutdown 第 13 步。
//
// 回到 auditUnavailable：此後的 audit() 丟棄是正確行為，不能報成不變量破壞
// （lease 即將釋放，本來就不該再有稽核寫入）。
func (a *App) closeAuditWriter() {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	if a.auditF == nil {
		return
	}
	if err := a.auditF.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "sdlc-workbench: 關閉稽核寫入器失敗："+err.Error())
	}
	a.auditF = nil
	a.auditState = auditUnavailable
}

// noteAuditInvariantBrokenLocked：ready 之後卻沒有 writer——這不是「沒開 audit」
// 而是不變量破壞，必須讓人看得到。稽核本身寫不出去，所以出口只能是 stderr 與
// 啟動橫幅；兩者都用，因為 UI 此刻可能已經關了。
//
// 橫幅走 appendStartup(msg, true)：與 repo 其他 blocker 一致，**既有訊息不會把它
// 吃掉**（owner 2026-08-19：先前直接賦值 startupErr 的寫法在已有任何啟動警告時
// 會讓這則整段消失，可觀察出口只剩桌面使用者看不到的 stderr）。
//
// once 是必要的 production 行為而不是測試便利：auditWriter 是 CLI stderr 的
// io.Writer，破掉之後每一行輸出都會走到這裡，不去重會把橫幅灌爆。
//
// 呼叫端必須持有 auditMu。
func (a *App) noteAuditInvariantBrokenLocked(kind string) {
	if a.auditBrokenNoted {
		return
	}
	a.auditBrokenNoted = true
	msg := "稽核寫入器在 ready 之後消失（kind=" + kind + "）：這是不變量破壞，" +
		"本次執行之後的稽核事件都不會被記錄"
	fmt.Fprintln(os.Stderr, "sdlc-workbench: "+msg)
	a.appendStartup(msg, true)
}

func (a *App) audit(kind string, v any) {
	if h := a.hookAudit; h != nil {
		h(kind) // 測試注入：在**呼叫端的 goroutine 上**同步執行，用來證明某筆稽核是同步寫的
	}
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	if a.auditF == nil {
		// unavailable：沒有 lease 就不該寫 state audit（拒絕原因由 startupErr／
		// stderr／UI 呈現）；shutdown 收掉 writer 之後回到同一個狀態，兩者丟棄都
		// 是正確行為。
		// ready：writer 不該不見——fail loud（owner 2026-08-18）。
		if a.auditState == auditReady {
			a.noteAuditInvariantBrokenLocked(kind)
		}
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
		// 與 audit() 同一條判準：unavailable 丟棄是對的，ready 之後不見是不變量破壞。
		if w.a.auditState == auditReady {
			w.a.noteAuditInvariantBrokenLocked("cli-stream")
		}
		return len(p), nil
	}
	return w.a.auditF.Write(p)
}

func (a *App) auditWriterFor() auditWriter { return auditWriter{a} }

func clientInfo() codex.ClientInfo {
	return codex.ClientInfo{Name: "sdlc-workbench", Version: "0.0.1"}
}

// CLIInfo 回報 CLI 解析路徑與版本（隔離 smoke 的證據面）。
//
// 這是 Wails binding：跑在 UI 的 goroutine 上，與 startup 發布這些欄位的那一刻
// 併發（owner 2026-08-19 判定為正式執行時可達的資料競爭）。所以**先取一份快照**
// ——五個欄位一次複製完、放掉 startupMu，之後的 CLI/node 版本探測都用快照裡的
// 路徑去 exec，不把跨行程呼叫留在臨界區內。
func (a *App) CLIInfo() map[string]string {
	si := a.startupSnapshot()
	return map[string]string{
		"toolsDir": si.toolsDir, "toolsSource": si.toolsSource,
		"claudeVersion": cliVersionFrom(si, "claude"), "codexVersion": cliVersionFrom(si, "codex"),
		"node": nodeVersionOf(si.nodePath), "workspace": si.workspace,
		"workspaceSource": si.workspaceSr, "startupError": si.startupErr,
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

// ListWorkspace：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) ListWorkspace(rel string) ([]FileNode, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.listWorkspace(rel)
}

func (a *App) listWorkspace(rel string) ([]FileNode, error) {
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

// ReadWorkspaceFile：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) ReadWorkspaceFile(rel string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.readWorkspaceFile(rel)
}

func (a *App) readWorkspaceFile(rel string) (string, error) {
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

// SpecList：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) SpecList() ([]FileNode, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.specList()
}

// SpecList 列出納管 spec 樹（spec.InScope 過濾），供前端 spec 瀏覽器初始載入。
// 沿用 internal/spec.GitRepo.ReadScopedWorktree 的 walk 慣例：spec/ 尚不存在時
// 回空清單、不是錯誤。
func (a *App) specList() ([]FileNode, error) {
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

// SpecRead：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) SpecRead(rel string) (SpecFile, error) {
	if err := a.beginTxn(); err != nil {
		return SpecFile{}, err
	}
	defer a.endTxn()
	return a.specRead(rel)
}

// SpecRead 讀既有納管檔；Digest 為 specDigestOf(raw bytes)，與 SpecWrite 的
// expected_digest／回傳值同格式。
func (a *App) specRead(rel string) (SpecFile, error) {
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

// SpecWrite：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) SpecWrite(rel, content, expectedDigest string) (newDigest string, err error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.specWrite(rel, content, expectedDigest)
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
func (a *App) specWrite(rel, content, expectedDigest string) (newDigest string, err error) {
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

// ensureGate 惰性初始化 gate.Service／spec.GitRepo：journal 落在 **a.stateDir**
// 的 gate.jsonl（spec §5.4：第 2 層 app state、gitignored）。
//
// **綁 a.stateDir 而不是 workspaceDir/.workbench**（owner 2026-08-19）：
// ownership lease 鎖的是 a.stateDir，而 `resolveWorkspace` 的 tmp fallback 會回
// `workspace=os.TempDir()`、`state=<tmp>/sdlc-workbench/.workbench`——兩者**不同
// 值**。舊寫法會讓 gate journal 落在 lease 保護範圍之外，「同一份 journal 的所有
// writer 都被同一把鎖排他」這個保證因此不成立。git repo 仍走 workspaceDir
// （root），只有 journal 路徑跟著受保護的 state root。
//
// 舊路徑上既有的 gate.jsonl **不是靜默忽略**：startupAfterWriters 會在任何惰性
// 初始化之前先把它搬過來，搬不動就中止啟動（見 migrateLegacyState）。所以走到
// 這裡時，a.stateDir 上的那一份就是唯一權威。
func (a *App) ensureGate() (*gate.Service, error) {
	// 遷移中止時連**開檔**都不做：這裡是 gate journal 唯一的開啟點，擋在 once
	// 之前才擋得住所有內部呼叫者（spec/plan watcher 的 reconcile 也走這裡），
	// 不只是 exported binding（reviewer 2026-08-19 P1）。刻意不進 gateOnce：
	// 被擋下的那幾次不得把 once 消耗掉。
	if err := a.stateBlockedErr(); err != nil {
		return nil, err
	}
	a.gateOnce.Do(func() {
		root, err := claude.NormalizeCWD(a.workspaceDir)
		if err != nil {
			a.gateInitErr = err
			return
		}
		wbDir := a.stateDir
		if merr := os.MkdirAll(wbDir, 0o755); merr != nil {
			a.gateInitErr = merr
			return
		}
		j, jerr := gate.OpenJournal(filepath.Join(wbDir, "gate.jsonl"))
		if jerr != nil {
			a.gateInitErr = jerr
			return
		}
		a.specRepo = spec.NewGitRepoCtx(a.procRoot(), root, spec.SpecScope)
		a.planRepo = spec.NewGitRepoCtx(a.procRoot(), root, spec.PlanScope)
		a.planGit = appGitRunner{root: root, ctx: a.procRoot()}
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
			return spec.BuildCurrentManifestScoped(spec.NewGitRepoCtx(a.procRoot(), root, decl.Scope()), decl.Scope())
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

// SubmitForApproval：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) SubmitForApproval() (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.submitForApproval()
}

// SubmitForApproval 以目前 committed spec 快照送出 Gate 1 核可申請。
// dirty tree／HEAD 位移等錯誤原樣自 spec.BuildCommittedSnapshot 傳回。
func (a *App) submitForApproval() (string, error) {
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

// PreviewSpecCommit：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) PreviewSpecCommit() (SpecCommitPreview, error) {
	if err := a.beginTxn(); err != nil {
		return SpecCommitPreview{}, err
	}
	defer a.endTxn()
	return a.previewSpecCommit()
}

// PreviewSpecCommit 回傳目前納管樹相對 HEAD 的 diff，並附上綁定當下狀態的
// CommitToken——前端保留此 token，未改動地傳給 ConfirmSpecCommit。
func (a *App) previewSpecCommit() (SpecCommitPreview, error) {
	if _, err := a.ensureGate(); err != nil { // 惰性初始化 a.specRepo（同 SubmitForApproval 路徑）
		return SpecCommitPreview{}, err
	}
	tok, diff, err := a.specRepo.PreviewSpecCommit()
	if err != nil {
		return SpecCommitPreview{}, err
	}
	return SpecCommitPreview{Token: tok, Diff: diff}, nil
}

// ConfirmSpecCommit：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) ConfirmSpecCommit(tok spec.CommitToken, message string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.confirmSpecCommit(tok, message)
}

// ConfirmSpecCommit 以 PreviewSpecCommit 回傳的 token 提交納管樹異動；token
// 與目前 repo 狀態不符（HEAD 移動或內容變更）回 spec.ErrCommitStale。
func (a *App) confirmSpecCommit(tok spec.CommitToken, message string) error {
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
// appGitRunner：gate／plan 的 git 讀取。**帶 ctx**——這些呼叫全部發生在 binding
// 的交易內，shutdown 一 cancel procRoot 就必須跟著收斂，否則一個卡住的 git 會讓
// inflight.Wait 無限等下去（reviewer 2026-08-20）。
type appGitRunner struct {
	root string
	ctx  context.Context
}

func (r appGitRunner) Git(args ...string) ([]byte, error) {
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	out, ex, err := proc.Output(ctx, proc.Config{Binary: "git", Args: append([]string{"-C", r.root}, args...)})
	if err != nil {
		return nil, err // 保留 context.Canceled／DeadlineExceeded 的 identity
	}
	if ex.Err != nil {
		// **保留 *exec.ExitError 的鏈**：呼叫端用 errors.As 分辨「exit 1＝找不到／
		// 非祖先」與真正的執行失敗（見 TestAppGitRunnerPreservesExitErrorChain）。
		return nil, fmt.Errorf("git %v: %w", args, ex.Err)
	}
	if ex.Code != 0 {
		return nil, fmt.Errorf("git %v: exit %d: %s", args, ex.Code, strings.TrimSpace(ex.StderrTail))
	}
	return out, nil
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

// PlanList：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) PlanList() ([]FileNode, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.planList()
}

// PlanList 列出納管 plan 樹（spec.PlanScope.Match 過濾），鏡射 SpecList。
func (a *App) planList() ([]FileNode, error) {
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

// PlanRead：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 唯讀也要進閘（reviewer 2026-08-20）：它讀的是 startup 才發布的資源，
// 而 Wails 的 OnStartup 與 bindings 會並行——啟動期讀到的是半發布的狀態。
func (a *App) PlanRead(rel string) (SpecFile, error) {
	if err := a.beginTxn(); err != nil {
		return SpecFile{}, err
	}
	defer a.endTxn()
	return a.planRead(rel)
}

// PlanRead 讀既有納管 plan 檔；Digest 格式同 SpecRead（specDigestOf）。
func (a *App) planRead(rel string) (SpecFile, error) {
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

// PlanWrite：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) PlanWrite(rel, content, expectedDigest string) (newDigest string, err error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.planWrite(rel, content, expectedDigest)
}

// PlanWrite 逐字鏡射 SpecWrite（atomic rename＋optimistic concurrency＋
// symlink-escape containment；完整威脅模型與逐步理由見 SpecWrite 的 doc
// comment，此處不重複），差異僅：scope 檢查換 spec.PlanScope.Match、衝突錯誤
// 換 ErrPlanWriteConflict、暫存檔前綴換 ".plan-write-*.tmp"。
func (a *App) planWrite(rel, content, expectedDigest string) (newDigest string, err error) {
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

// PreviewPlanCommit：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) PreviewPlanCommit() (SpecCommitPreview, error) {
	if err := a.beginTxn(); err != nil {
		return SpecCommitPreview{}, err
	}
	defer a.endTxn()
	return a.previewPlanCommit()
}

// PreviewPlanCommit 回傳目前 plan/ 樹相對 HEAD 的 diff與 CommitToken；
// token.AnalysisBase 取自 worktree 唯一 plan 文件的 analysis_base_commit——
// 讀不到（plan/ 沒有或有多份候選文件）或欄位為空即拒絕（§3.0：lineage 驗證
// 的起點必須先在 worktree 就確立，Confirm 時再核對是否漂移）。
func (a *App) previewPlanCommit() (SpecCommitPreview, error) {
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

// ConfirmPlanCommit：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) ConfirmPlanCommit(tok spec.CommitToken, message string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.confirmPlanCommit(tok, message)
}

// ConfirmPlanCommit 以 PreviewPlanCommit 回傳的 token 提交 plan/ 樹異動。除
// planRepo.ConfirmSpecCommit 既有的 HeadOID／TreeDigest staleness 檢查外，
// 額外重讀 worktree plan 的 analysis_base_commit：與 token.AnalysisBase 不符
// （含此時讀不到）視同 token 過期，回 spec.ErrCommitStale——commit 期間
// analysis_base_commit 被改動，Preview 當下核對過的 lineage 起點已不可信。
func (a *App) confirmPlanCommit(tok spec.CommitToken, message string) error {
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

// PreviewAnalysisBaseBump：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) PreviewAnalysisBaseBump(planRel, buffer string) (BumpPreview, error) {
	if err := a.beginTxn(); err != nil {
		return BumpPreview{}, err
	}
	defer a.endTxn()
	return a.previewAnalysisBaseBump(planRel, buffer)
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
func (a *App) previewAnalysisBaseBump(planRel, buffer string) (BumpPreview, error) {
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

// ConfirmAnalysisBaseBump：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) ConfirmAnalysisBaseBump(tok BumpToken, planRel, currentBuffer string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.confirmAnalysisBaseBump(tok, planRel, currentBuffer)
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
func (a *App) confirmAnalysisBaseBump(tok BumpToken, planRel, currentBuffer string) (string, error) {
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

// SubmitPlanForApproval：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) SubmitPlanForApproval(planID string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.submitPlanForApproval(planID)
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
func (a *App) submitPlanForApproval(planID string) (string, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return "", err
	}

	entries, err := a.gateList()
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

// RegisterMutation：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) RegisterMutation(taskRef, patch string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.registerMutation(taskRef, patch)
}

// RegisterMutation 把一份 negative_control 用的 patch 存進 evidence CAS
// store，再把描述它的 Mutation 記錄 append 進 evidence journal，回傳新產生的
// mutation_id。RunEvidence 的 negative_control 路徑只接受 mutation_id（不接受
// 原始 patch bytes）——patch 內容一律經由此登記，落盤與可稽核的紀錄同時成立
// （鏡射 Task 17 CAS＋Task 20 journal 的既定順序：CAS 先落盤才 append）。
func (a *App) registerMutation(taskRef, patch string) (string, error) {
	// nil journal 有兩種成因（真的沒初始化／遷移中止讓 startupEvidence 沒跑），
	// 所以檢查排在交易閘之後——同 RunEvidence。
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

// RunEvidence：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) RunEvidence(expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.runEvidence(expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID)
}

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
// workflowMu）：beginTxn() 是 shutdown gate 的入場點（沿 app.go:152 慣例，
// shutdown 後拒新 run）；執行 context 衍生自 a.ctx（app 的 shutdown-scoped
// context，同 SpecAssist／StartSession 的既定用法），供 reclaimEvidenceRuns
// 手動 cancel。evidence.Run 內部才會 mint evidence_id（ulid callback），所以
// active-run registry 的登記時機挪進 ulid callback 本身。
//
// beginTxn 成功到 ulid callback 執行之間，evidence.Run 已經先跑了
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
// M3a.1 T8（§3.3.2）凍結順序：beginTxn → workflowMu.Lock → 讀取並固定
// 權威 active gate2 approval_id／plan_commit（GateList 挪到 beginTxn 之
// 後、workflowMu 下讀——不再信任呼叫前的快照）→ CAS 比對 expected（不符→
// Unlock→endTxn→ErrStaleGeneration）→ workflowMu.Unlock → Step 2b：
// shutMu 下重查 shuttingDown（沿 pre-ulid 窗自我 cancel 先例——CAS 通過後到
// started event 之間若 shutdown 已開始，零副作用返回，不指望 ulid callback
// 那次複查還來得及，因為這個 run 這時甚至還沒發過 started event）→ started
// event／mutation 載入／worktree 建立／run → finalize → endTxn。
// runEvidenceCASHook（測試 seam，沿 decideBarrierHook 命名慣例）在
// beginTxn 成功後、workflowMu.Lock 前觸發——特意早於 Lock，讓 hook 內部
// 可呼叫 GateDecide 之類同樣取 workflowMu 的操作換版，而不會跟本呼叫自己的
// Lock 死鎖。
func (a *App) runEvidence(expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID string) (string, error) {
	if kind != "expected_red" && kind != "negative_control" {
		return "", fmt.Errorf("evidence: unknown kind %q", kind)
	}
	if kind == "negative_control" {
		if mutationID == "" {
			return "", errors.New("evidence: negative_control requires a mutation_id")
		}
	} else if mutationID != "" {
		return "", errors.New("evidence: expected_red must not carry a mutation_id")
	}

	// **排在交易閘之後**：evidenceJournal 為 nil 有兩種成因——真的沒初始化，
	// 以及遷移中止讓 startupEvidence 根本沒跑。後者要回「遷移未完成」，回
	// 「not initialized」等於把使用者該處理的事藏起來（reviewer 2026-08-19 P1）。
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}

	if h := a.runEvidenceCASHook; h != nil { // 測試 seam：見上方函式 doc
		h()
	}

	a.workflowMu.Lock()
	entries, err := a.gateList()
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
	shuttingDown := a.phase == phaseShuttingDown
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
		shuttingDown := a.phase == phaseShuttingDown
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

// EvidenceGet：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EvidenceGet(evidenceID string) (evidence.EvidenceRun, error) {
	if err := a.beginTxn(); err != nil {
		return evidence.EvidenceRun{}, err
	}
	defer a.endTxn()
	return a.evidenceGet(evidenceID)
}

// EvidenceGet 回傳 journal 內 evidenceID 對應的完整 EvidenceRun（含 journal
// 重播後重建的紀錄）。
func (a *App) evidenceGet(evidenceID string) (evidence.EvidenceRun, error) {
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

// ValidateTestCommit：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) ValidateTestCommit(planID, taskID, testCommit string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.validateTestCommit(planID, taskID, testCommit)
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
func (a *App) validateTestCommit(planID, taskID, testCommit string) error {
	entries, err := a.gateList()
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

// EvidenceCommitCandidates：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EvidenceCommitCandidates(planID string) ([]CommitInfo, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.evidenceCommitCandidates(planID)
}

// EvidenceCommitCandidates lists the most recent commits after the active
// Gate 2 plan_commit (Task 22): the data source for TcaWorkspace's
// test_commit dropdown — `git log --format=%H%x00%s -n 20 <plan_commit>..HEAD`,
// newest first (git log's default order). Returns an empty (non-nil) slice
// when the range has no commits, never nil, so the frontend can render it
// without a null-check.
func (a *App) evidenceCommitCandidates(planID string) ([]CommitInfo, error) {
	entries, err := a.gateList()
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

// SubmitTestContract：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) SubmitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.submitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID)
}

// SubmitTestContract 送出 TCA（test_contract_approval）核可申請（Task 21，
// §3.4）：讀 active gate2（subject="plan:"+planID）取得其完整 ApprovalRecord
// ——gate2_approval binding 的 ref/digest 來源，base_commit binding 原樣複製
// 其 base_commit（§3.0 錨定，TCAPolicy.BuildDecision 另外覆核兩者相符）；再讀
// expectedRedID／negativeControlID 兩筆 EvidenceRun（EvidenceRunDigest 現算）
// 與 mutationID 的 Mutation（digest 直接取 CAS digest），組六筆 bindings 後
// Submit——bindings 本身是否彼此一致（role/kind、兩筆 passed、descriptor 等）
// 全交給 TCAPolicy.ValidateRequest／BuildDecision，這裡只負責組裝已有的值。
func (a *App) submitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID string) (string, error) {
	svc, err := a.ensureGate()
	if err != nil {
		return "", err
	}
	if a.evidenceJournal == nil {
		return "", errors.New("evidence: not initialized")
	}

	entries, err := a.gateList()
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

// GateDecisionContext：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) GateDecisionContext(approvalID string) (GateDecisionContextDTO, error) {
	if err := a.beginTxn(); err != nil {
		return GateDecisionContextDTO{}, err
	}
	defer a.endTxn()
	return a.gateDecisionContext(approvalID)
}

// GateDecisionContext 回傳 approvalID（gate2 的 pending request 或 approved
// record）所綁 committed plan 之 task risk 列。一律從該筆的 base_commit
// （plan_commit）binding 用 PlanLoader.LoadAt 讀 committed plan——絕不讀
// worktree：送核後修改 worktree plan 不得改變這裡的回傳值（committed 才是
// 核可對象），前端不得以目前 worktree plan 推導 minimum／planner。
func (a *App) gateDecisionContext(approvalID string) (GateDecisionContextDTO, error) {
	entries, err := a.gateList()
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
//
// 這是 Wails binding，所以走 beginTxn（shutdown 等待 ＋ migration blocker）；
// app 內部要同一份 projection 的地方一律呼叫 gateList，不重入交易閘。
func (a *App) GateList() ([]GateEntryDTO, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.gateList()
}

// gateList：GateList 的本體（不進交易閘，見 beginTxn 的 doc）。
func (a *App) gateList() ([]GateEntryDTO, error) {
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

// GateDecide：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) GateDecide(approvalID, decision, reason string, riskSelections []gate.RiskSelection) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.gateDecide(approvalID, decision, reason, riskSelections)
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
func (a *App) gateDecide(approvalID, decision, reason string, riskSelections []gate.RiskSelection) error {
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

// ensureEscalation 惰性初始化 escalation.Service，journal 落在 **a.stateDir** 的
// escalation.jsonl（同 ensureGate 之於 gate.jsonl——一律綁受 ownership lease
// 保護的 state root，理由見 ensureGate）。
//
// 舊路徑的 escalation.jsonl 同樣由 migrateLegacyState 在啟動早期搬過來：那份
// journal 裡可能還有未解除的系統管控項目，靜默忽略會直接改變核可結果。
func (a *App) ensureEscalation() (*escalation.Service, error) {
	if err := a.stateBlockedErr(); err != nil { // 同 ensureGate：擋在 once 之前
		return nil, err
	}
	a.escOnce.Do(func() {
		if _, err := claude.NormalizeCWD(a.workspaceDir); err != nil {
			a.escInitErr = err
			return
		}
		wbDir := a.stateDir
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
		if err := a.escJournalDegradedLocked("gate", a.gateJournal.Degraded(), filepath.Join(a.stateDir, "gate.jsonl")); err != nil {
			return err
		}
	}
	if a.evidenceJournal != nil {
		if err := a.escJournalDegradedLocked("evidence", a.evidenceJournal.Degraded(), filepath.Join(a.stateDir, "evidence", "evidence.jsonl")); err != nil {
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

// EscalationList：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EscalationList() ([]escalation.Entry, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.escalationList()
}

// EscalationList 回傳收件匣 projection（Wails 綁定）。Project 失敗回錯——
// 收件匣標不可用，絕不裝空（§3.8）。
func (a *App) escalationList() ([]escalation.Entry, error) {
	svc, err := a.ensureEscalation()
	if err != nil {
		return nil, err
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	return svc.Entries()
}

// EscalationCreate：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EscalationCreate(sourceRef, blockScope, summary string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.escalationCreate(sourceRef, blockScope, summary)
}

// EscalationCreate 建立手動 escalation 項（Wails 綁定；sourceRef 必填，
// blockScope 空字串＝非阻擋資訊項）。
func (a *App) escalationCreate(sourceRef, blockScope, summary string) (string, error) {
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

// EscalationAck：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EscalationAck(id string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.escalationAck(id)
}

// EscalationAck 標記已認知（不解除 block，§3.8）。
func (a *App) escalationAck(id string) error {
	svc, err := a.ensureEscalation()
	if err != nil {
		return err
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	return svc.Ack(id)
}

// EscalationResolve：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) EscalationResolve(id, resolution, reason string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.escalationResolve(id, resolution, reason)
}

// EscalationResolve 手動 resolve（Wails 綁定）。actor 一律取 git identity
// （同 GateDecide 的 approver 來源）；hard 項由 Service 拒絕（僅系統可 resolve）。
func (a *App) escalationResolve(id, resolution, reason string) error {
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

// SpecAssist：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) SpecAssist(provider, purpose, prompt string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.specAssist(provider, purpose, prompt)
}

// SpecAssist 以隔離的一次性 AI 呼叫草擬 spec 內容，帶 provider 端強制的零 workspace
// 變更保證（Claude `--tools ""`／Codex readOnly+never，見 internal/assist）。
//
// lifecycle 不變量：
//   - 獨佔性：每個 provider 至多一個 active；第二個併發請求回 ErrAssistActive。
//   - 交易閘：beginTxn 於啟動（shutdown 後拒新），endTxn 於收尾一次。
//   - shutdown reclaim：shutdown cancel in-flight one-shot、等其收尾（endTxn）
//     後才 Manager.Close（reclaimAssists＋inflight.Wait）。
//   - ownership 隔離：不碰 sessionHosts／a.codexConn（assist runner 為獨立 process）；
//     晚到舊 generation 事件（correlation 不符）丟棄並發 stream_error（fail loud）。
//   - once/token 收尾：result／abort／timeout／shutdown 任一先觸發即收一次。
//
// 事件經 Manager.EmitAssist 出口（scope=session、provider、correlation_id、
// purpose="spec_assist"）——保留稽核＋檔案級 event_id，但**不進 provider slot**
// （前端依 purpose 二次分流，不污染 reducer／Chat／totals）。
func (a *App) specAssist(provider, purpose, prompt string) (string, error) {
	if !knownProvider(provider) {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	// root context 在任何其他敘述之前取得（同 planAssist 的理由；形狀由
	// TestAssistImplementationsTakeRootContextFirst 驗）。
	ctx, cancel := context.WithTimeout(a.procRoot(), assistTimeout)
	// Pin the assist purpose at the emit boundary (defense in depth): this is the
	// isolated assist lane, so every emitted envelope MUST carry
	// purpose="spec_assist" regardless of the caller-supplied argument. Trusting
	// the caller would let a future caller passing "" or another value leak
	// assist (scope=session) events into the provider slot — restore.go's
	// replayViewWindow buckets by purpose, and EmitAssist has no purpose guard.
	purpose = "spec_assist"
	gen := &assistGen{correlationID: contract.NewULID(time.Now()), cancel: cancel, done: make(chan struct{})}

	// gen（含 cancel）必須早於 beginTxn 進 assistActive——shutdown 的 reclaim
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
	// **不再開內層交易**：binding 薄包裝進場時就登記了（見 procRootCtx 的
	// doc）。內外兩層各自 admission 會讓操作做到一半才失敗。

	// once/token 收尾：result／abort／timeout／shutdown 任一先觸發，恰好收一次。
	teardown := func() {
		gen.once.Do(func() {
			cancel()
			a.assistMu.Lock()
			if a.assistActive[provider] == gen {
				delete(a.assistActive, provider)
			}
			a.assistMu.Unlock()
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

// after：claudeTeardown 的 appcore.CloseSequence 等待來源，nil（production）
// 回 appcore.RealAfter；測試以 afterFn 注入受控 timer（見 App 欄位 doc）。
func (a *App) after() appcore.After {
	if a.afterFn != nil {
		return a.afterFn
	}
	return appcore.RealAfter
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
func (a *App) nonPlanDirtyPaths(ctx context.Context) ([]string, error) {
	// process group 收尾（見 proc.Output）：CommandContext 只殺直接 child，
	// 忽略 TERM 的孫程序會讓取消不收斂（reviewer 2026-08-20）。
	out, ex, err := proc.Output(ctx, proc.Config{Binary: "git",
		Args: []string{"-C", a.workspaceDir, "status", "--porcelain", "--untracked-files=all"}})
	if err != nil {
		return nil, fmt.Errorf("assist: git status: %w", err) // 保留 context.Canceled identity
	}
	if ex.Err != nil {
		return nil, fmt.Errorf("assist: git status: %s: %w", strings.TrimSpace(ex.StderrTail), ex.Err) // 保留 typed error
	}
	if ex.Code != 0 {
		return nil, fmt.Errorf("assist: git status: exit %d: %s", ex.Code, strings.TrimSpace(ex.StderrTail))
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

// PlanAssist：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 這個形狀由 app_binding_surface_test.go 逐一驗證；不得在此處插入任何其他
// 敘述（含參數驗證），那會讓別的失敗訊息蓋掉 lifecycle 的拒絕原因。
func (a *App) PlanAssist(provider, prompt string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.planAssist(provider, prompt)
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
func (a *App) planAssist(provider, prompt string) (string, error) {
	if !knownProvider(provider) {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	// **root context 必須在任何會阻塞的前置工作之前取得**（reviewer 2026-08-20）：
	// 下面的 gate reconcile、gateList、git status、binary SHA、preflight（最多
	// 30s）、escalation journal、HeadCommit 全都會 spawn 子行程或寫檔。先前 ctx
	// 到最後才建立，於是一個忽略 TERM 的 git 就能讓 shutdown 卡在 inflight.Wait
	// ——cancel 根 context 也影響不到還沒拿到它的工作。
	ctx, cancel := context.WithTimeout(a.procRoot(), assistTimeout)
	defer cancel() // 下面成功走到 run 時會把 cancel 交給 gen，這裡的 defer 冪等

	if _, err := a.ensureGate(); err != nil {
		return "", err
	}
	entries, err := a.gateList()
	if err != nil {
		return "", err
	}
	specManifestDigest, hasActiveGate1 := activeGate1SpecManifestDigest(entries)
	if !hasActiveGate1 {
		return "", errors.New("assist: 無生效規格核可——先完成 Gate 1")
	}

	dirty, err := a.nonPlanDirtyPaths(ctx)
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
	pf, pferr := a.planPreflight(ctx, provider)
	// **收尾取消不是 enforcement failure**（reviewer 2026-08-20）：先前把所有
	// preflight error 都包成 ErrEnforcementUnproven 並寫一筆 hard escalation，
	// 於是 shutdown 取消正在跑的 `claude --version` 之後，journal 會留下一筆說
	// 「這個 provider 的 enforcement 前提未證明」的阻擋項——那是錯的，而且是
	// hard 項，使用者無法自行解除。
	//
	// 判準是**根 context**：被 shutdown／使用者取消就原樣回傳（保留
	// context.Canceled 的 identity），只有 preflight 自己那 30 秒 deadline 或真正
	// 的驗證失敗才算 enforcement failure。
	if cerr := ctx.Err(); cerr != nil {
		return "", cerr
	}
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
	gen := &assistGen{correlationID: contract.NewULID(time.Now()), cancel: cancel, done: make(chan struct{})}

	// 同 SpecAssist：gen 先入 assistActive 才 beginTxn（shutdown reclaim 窗口
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
	// **不再開內層交易**：binding 薄包裝進場時就登記了（見 procRootCtx 的
	// doc）。內外兩層各自 admission 會讓操作做到一半才失敗。

	teardown := func() {
		gen.once.Do(func() {
			cancel()
			a.assistMu.Lock()
			if a.assistActive[provider] == gen {
				delete(a.assistActive, provider)
			}
			a.assistMu.Unlock()
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
func (a *App) planPreflight(ctx context.Context, provider string) (assist.PreflightResult, error) {
	bin := a.claudeCLIPath()
	if provider == "codex" {
		bin = a.codexCLIPath()
	}
	digest, err := assist.BinarySHA256(ctx, bin)
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
		res, err = assist.PreflightClaude(ctx, bin, assist.ClaudePlannerArgs())
	} else {
		res, err = assist.PreflightCodex(ctx, bin)
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
// generation（runner 界限內退出 → teardown 清 active＋endTxn＋close done）。
// 必須早於 inflight.Wait（assist 持 txn，否則 Wait 死等）與 Manager.Close
// （稽核收尾在 sink 關閉前完成）。bounded 由 runner 尊重 ctx（proc TermGrace）保證。
// procRoot：**所有可取消子行程工作**的根 context（assist run、gate 的 git 呼叫、
// preflight 的 --version 探測）。惰性建立；shutdown 之後回一個已 cancel 的。
func (a *App) procRoot() context.Context {
	a.assistMu.Lock()
	defer a.assistMu.Unlock()
	if a.procRootCtx == nil {
		a.procRootCtx, a.procRootCancel = context.WithCancel(context.Background())
	}
	return a.procRootCtx
}

// cancelProcRoot：cancel 根 context，並保證之後建立的工作也拿到已 cancel 的那一份
// （收尾後才進場的子行程不得真的跑起來）。
func (a *App) cancelProcRoot() {
	a.assistMu.Lock()
	if a.procRootCtx == nil {
		a.procRootCtx, a.procRootCancel = context.WithCancel(context.Background())
	}
	cancel := a.procRootCancel
	a.assistMu.Unlock()
	cancel()
}

// reclaimAssists：cancel 根 context（權威手段）＋ 逐一 cancel 已登記的 gen
// （補強，讓已在跑的那些立即收到訊號而不必等 ctx 傳播）。
func (a *App) reclaimAssists() {
	a.cancelProcRoot()
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
	a.apprOrder = append(a.apprOrder, id)
	a.apprMu.Unlock()
}

// removeApprOrderLocked：從 apprOrder 移除 id——呼叫端須持有 apprMu，且與
// apprPending 的刪除同一把鎖內完成，避免 promotionOrder 短暫看到已從 map 消失
// 但仍留在 order 裡的 id。找不到（例如重複刪除）視為 no-op。
func (a *App) removeApprOrderLocked(id string) {
	for i, oid := range a.apprOrder {
		if oid == id {
			a.apprOrder = append(a.apprOrder[:i], a.apprOrder[i+1:]...)
			return
		}
	}
}

// promotionOrder：目前 pending approval 的登記順序快照（§3.6.4：多筆待核可
// FIFO promotion）——供前端／呼叫端決定同時有多筆待核可時該先顯示哪一筆。
func (a *App) promotionOrder() []string {
	a.apprMu.Lock()
	defer a.apprMu.Unlock()
	out := make([]string, len(a.apprOrder))
	copy(out, a.apprOrder)
	return out
}

// pendingByID：唯讀查詢 pending approval（不移除；ResolveApproval 才是消費端）。
func (a *App) pendingByID(id string) *pendingApproval {
	a.apprMu.Lock()
	defer a.apprMu.Unlock()
	return a.apprPending[id]
}

// ResolveApproval：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) ResolveApproval(id string, allow bool, reason string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.resolveApproval(id, allow, reason)
}

func (a *App) resolveApproval(id string, allow bool, reason string) error {
	a.apprMu.Lock()
	p, ok := a.apprPending[id]
	if ok {
		delete(a.apprPending, id)
		a.removeApprOrderLocked(id)
	}
	a.apprMu.Unlock()
	if !ok {
		return fmt.Errorf("no pending approval %s (timed out?)", id)
	}
	err := p.resolve(allow, reason)
	// 廣播 dismiss：dev 模式（原生視窗＋browser devserver）或多視窗下，
	// 未按下按鈕的前端也要收掉彈窗
	// reason 一併帶出：§3.6.4 凍結的六種「恢復原釘選」觸發裡，remove 與 shutdown
	// 都是經 denyApprovals → 這條 resolved 路徑出去的，只看 cause 分不出來。
	a.emit("approval:dismiss", map[string]any{
		"id": id, "wsid": string(p.wsid), "cause": "resolved", "reason": reason})
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
				a.manager.EmitApprovalDecision(w, id, sessionID(), decision, reason))
			return err
		})
		a.noteWSEmitError("approval_request", w,
			a.manager.EmitApprovalRequest(w, id, sessionID(), req.ToolName, req.Input))
		// wsid：前端依它把對話框路由到來源 session 的 pane（§3.6.4；未釘選時
		// transient secondary presentation）。provider 保留供顯示用。
		a.emit("approval:request", map[string]any{
			"id": id, "wsid": string(w), "provider": string(provider), "toolName": req.ToolName,
			"inputJson": string(req.Input),
		})
	}
}

// ---- session 綁定 ----

// StartSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) StartSession(wsid, prompt, resume, recordCase, taskLabel, approvalPolicy string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.startSession(wsid, prompt, resume, recordCase, taskLabel, approvalPolicy)
}

// StartSession：單一 ownership 交易——BeginNewSessionSubmit 先佔（輸家在建立任何
// process／recorder／pump 之前就失敗）→ provider 同步啟動 → Accept／Reject。
func (a *App) startSession(wsid, prompt, resume, recordCase, taskLabel, approvalPolicy string) error {
	// Task 26 原子切換：WSID 直接來自前端（session 由 CreateSession 建立、
	// ListSessions 列出），不再有 provider → WSID 的解析層。
	w, prov, err := a.resolveWSID("start session", wsid)
	if err != nil {
		return err
	}
	// registry uncertain：在 BeginNewSessionSubmit／provider 啟動之前早退。
	// 放行的話會起一個 provider 子行程、拿到新的 resume id，而 CommitResume
	// 一定寫不進去——重啟後這個 session 會接回**上一次**的對話，使用者分辨不出來。
	if a.registryUncertain() {
		return errRegistryUncertain
	}
	if resume == "" { // 第三輪 P1-3：view 未被 New 清除時自動接續（plan D6 resume 意圖）
		resume = a.registryResume(w)
	}
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
			a.commitSessionIdentity(w, contract.ProviderClaude, a.hostSessionID(host), taskLabel)
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
			terr := a.codexTeardown(w, a.hostFor(w)) // 冪等：撤路由＋finalize＋session:done
			return errors.Join(err, terr)
		}
		// Accept 成功才 commit（已 tombstone 的 WSID 由 store 在自己的 mutex 內拒絕）
		a.commitSessionIdentity(w, contract.ProviderCodex, threadID, taskLabel)
		_ = alreadyEnded // completed 先到：busy 未設，無需額外收尾
		return nil
	}
}

// SendMessage：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) SendMessage(wsid, prompt string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.sendMessage(wsid, prompt)
}

// SendMessage：指定 WSID 既有 session 的後續輪（僅該 slot phaseActive 允許；
// 錯誤原樣回 UI）。多 session 並存：一個 session busy 不影響另一個。
func (a *App) sendMessage(wsid, prompt string) error {
	w, pv, err := a.resolveWSID("send message", wsid)
	if err != nil {
		return err
	}
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

// EndSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) EndSession(wsid string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.endSession(wsid)
}

// EndSession：指定 provider 的收尾編排（appcore.EndSessionFlow）。冪等；
// ErrProviderBusy 等真實錯誤原樣回 UI。
//
// review P1（fix/lifecycle-app-txn）：整段納入 app transaction（beginTxn／
// endTxn，沿 app.go:213 慣例，同 StartSession／ensureAppServer／B1 probe）。
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
func (a *App) endSession(wsid string) error {
	w, p, err := a.resolveWSID("end session", wsid)
	if err != nil {
		return err
	}
	return a.endSessionFlowForWSID(w, p)
}

// endSessionFlowForWSID：provider-dispatch 的收尾編排本體，原內聯於 EndSession
// ——RemoveSession（Task 22）需要對任意 WSID 跑同一段收尾，抽出以供兩者共用。
func (a *App) endSessionFlowForWSID(w appcore.WSID, p contract.Provider) error {
	h := a.hostFor(w)
	if p == contract.ProviderClaude {
		return appcore.EndSessionFlow(a.manager, w, nil, a.claudeTeardown(h))
	}
	busy := func() bool { return h != nil && h.runner != nil && h.runner.ActiveTurnID() != "" }
	return appcore.EndSessionFlow(a.manager, w, busy, func() error { return a.codexTeardown(w, h) })
}

// removeStep：§3.6.2 凍結順序的探針（同 startupStep／hookWireStep 慣例——步驟名
// 代表「編排跑到了這裡」，不代表每一步都做了新工作；見 hookRemoveStep 欄位 doc）。
func (a *App) removeStep(step string) {
	if a.hookRemoveStep != nil {
		a.hookRemoveStep(step)
	}
}

// pendingApprovalIDs：目前待決 approval 的 id 快照（match 為 nil 代表全部）。
// 在 apprMu 下取得並排序，順序因此是決定性的、不隨 map 迭代漂移；呼叫端拿到的是
// 快照，之後才逐筆 deny——ResolveApproval 本身會重新取 apprMu，在鎖內呼叫會自我死鎖。
func (a *App) pendingApprovalIDs(match func(*pendingApproval) bool) []string {
	a.apprMu.Lock()
	var ids []string
	for id, p := range a.apprPending {
		if match == nil || match(p) {
			ids = append(ids, id)
		}
	}
	a.apprMu.Unlock()
	slices.Sort(ids)
	return ids
}

// denyApprovals：對快照到的 approval 逐筆 best-effort fail-closed deny。單筆失敗
// 不中斷其餘筆數，錯誤以 errors.Join 收集回傳；呼叫端不因此中斷 teardown（§3.6.3
// 原文：「deny 部分失敗時仍 terminate provider」，§3.6.5 的 shutdown 同此裁決）。
func (a *App) denyApprovals(ids []string, reason string) error {
	var errs []error
	for _, id := range ids {
		// 走實作而不是 exported binding：收尾路徑本身就在 shutdown 裡，交易閘
		// 此刻必然拒絕（phase 已是 shuttingDown），呼叫 binding 會讓 fail-closed
		// 的 deny 一筆都送不出去。
		if err := a.resolveApproval(id, false, reason); err != nil {
			errs = append(errs, fmt.Errorf("approval %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// denyApprovalsForRemove：§3.6.3——待移除 WSID 的全部顯示中／排隊 approval，一律
// best-effort fail-closed deny（reason=session_removed）。
func (a *App) denyApprovalsForRemove(w appcore.WSID) error {
	return a.denyApprovals(
		a.pendingApprovalIDs(func(p *pendingApproval) bool { return p.wsid == w }),
		"session_removed")
}

// cleanupRemovedFiles：per-WSID 殘留檔案清理（risk item 3：「Claude per-WSID
// socket／MCP config 的檔案數與清理」）。socket 檔已由 teardown 內的
// broker.Close() 處理（listener 一關就 unlink）；這裡補的是 teardown 不管的
// 那一份——per-WSID 的 MCP config 檔（`mcp-<wsid>.json`）本身從未被刪除過，見
// startClaude 對 host.mcpPath 的寫入。純粹依 WSID 算路徑，不需要 host 指標
// （此時 host 已由 teardown take-then-dispose 處置掉）；不存在（codex／從未啟動
// 過的 dormant session）視為已達目標狀態，不算錯誤。
func (a *App) cleanupRemovedFiles(w appcore.WSID) error {
	path := filepath.Join(a.stateDir, "mcp-"+string(w)+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("app: cleanup mcp config %s: %w", path, err)
	}
	return nil
}

// RemoveSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) RemoveSession(wsid string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.removeSession(wsid)
}

// RemoveSession：使用者明確移除（§3.6.1-3；純新增 binding）——registry 留
// tombstone（`removed_at`／`remove_reason`），不是刪除 record；與建立失敗回滾的
// DeleteUncommitted（不留痕）語意不同，不得混用。
//
// 凍結順序（§3.6.2）：deny_approvals → teardown → lease_finalize →
// cleanup_files → tombstone_persist → decrement_count——釋放名額（Manager 側
// 刪除 slot）必須是最後一步，任一步失敗都保留 slot（fail loud，使用者可重試
// 移除）。lease_finalize 對 claude 是 teardown（CloseSequence）內已完成的檢查
// 點，不是另一次呼叫；codex 沒有 session-scoped lease，同樣是 no-op 檢查點——
// 兩者都只是步驟探針，不代表這裡對它們各自另做了一次副作用。
//
// 六步之前另有一道 manager.Removable 前置檢查（review round-2 Important；純
// 新增、不重排凍結的六步）：deny_approvals／cleanup_files 對外都有可見副作用
// （approval 被 deny、MCP config 被刪），若沒有先確認 teardown 這次真的會成
// 功，就有可能在 teardown 失敗、slot 保留（session 照常存活）的情況下，已經
// 對一個仍存活的 session 造成不可逆的傷害——「任一步失敗都保留 slot」的精神
// 是失敗不得留下傷害，不只是不遞減名額。
//
// Remove × New 同一 provider 序列化（見 crTokens 欄位 doc 的鎖序凍結）：token
// 涵蓋整段編排，過程中不會有 CreateSession 為同一 provider reserve／commit 新
// slot，也不會有另一個 RemoveSession 併發跑同一 provider 的收尾。
//
// 殘餘 TOCTOU 窗口（review round-2 Important，登記給 Task 26）：token **不**
// 涵蓋 StartSession——manager.Removable 只唯讀、不做狀態轉移，它回來之後到
// 真正呼叫 BeginEndSession 之間，另一條路徑（例如 StartSession）仍可能把同一
// WSID 的 phase 推成 starting。若恰好卡在這個窗口，tombstone_persist 可能已
// 成功落盤，但隨後 manager.RemoveSession 因 phase 非 idle 而回
// ErrSessionNotIdle——結果是 registry 說已移除、Manager 卻仍佔著名額、上面還
// 起了一個新的 provider 子行程，且重啟後這筆不會被 registry 還原（tombstone
// 已經生效）。錯誤本身是 fail loud 的，但 in-process 無法自行收斂；徹底關閉
// 需要一個獨立的 removing phase（會動到 phase 狀態機），超出本次改動範圍。
// 今天不可達：exported binding 只在 bindings.ts／bindings.test.ts 有 wiring，
// 前端沒有任何呼叫路徑會在使用者可觸及的時間點於同一 WSID 上讓 Remove 與
// StartSession 真正競速。
func (a *App) removeSession(wsid string) error {
	w := appcore.WSID(wsid)
	p, ok := a.manager.ProviderOf(w)
	if !ok {
		return fmt.Errorf("app: remove session %s: %w", wsid, appcore.ErrSessionNotFound)
	}
	if a.wsReg == nil {
		return errNoSessionRegistry
	}
	// registry uncertain：必須早於 deny_approvals／teardown／cleanup_files。
	// 那三步有不可逆的對外副作用，而 tombstone_persist 一定會被 store 拒絕
	// ——放行等於對一個仍會存活的 session 先造成傷害，違反上面「任一步失敗
	// 都不得留下傷害」的凍結精神。
	if a.registryUncertain() {
		return errRegistryUncertain
	}

	crt := a.crToken(p)
	crt.Lock()
	a.removeTokenHeld.Store(true)
	defer func() {
		a.removeTokenHeld.Store(false)
		crt.Unlock()
	}()
	if h := a.hookRemoveHoldingToken; h != nil {
		h()
	}

	if err := a.manager.Removable(w); err != nil {
		return fmt.Errorf("app: remove session %s: %w（slot 保留，可重試移除；未觸碰 approval／檔案）", wsid, err)
	}

	a.removeStep("deny_approvals")
	denyErr := a.denyApprovalsForRemove(w)

	a.removeStep("teardown")
	tearErr := a.endSessionFlowForWSID(w, p)

	a.removeStep("lease_finalize")

	a.removeStep("cleanup_files")
	cleanupErr := a.cleanupRemovedFiles(w)

	if err := errors.Join(denyErr, tearErr, cleanupErr); err != nil {
		return fmt.Errorf("app: remove session %s: %w（slot 保留，可重試移除）", wsid, err)
	}

	a.removeStep("tombstone_persist")
	if err := a.noteRegistryUncertainErr("tombstone_persist", wsid,
		a.wsReg.Remove(wsid, "user_removed")); err != nil {
		// uncertain latch 是唯一「重試必然再被拒」的失敗原因（下一次呼叫會被
		// RemoveSession 開頭的 gate 直接擋掉），所以不能沿用「可重試移除」那句
		// ——兩句並列會讓使用者先去按重試，而正確處置是重啟（rev2 review M3）。
		if errors.Is(err, wsregistry.ErrRegistryUncertain) {
			return fmt.Errorf("app: remove session %s: tombstone persist 失敗：%w（slot 保留；重試無用，請依上述訊息重啟 app）", wsid, err)
		}
		return fmt.Errorf("app: remove session %s: tombstone persist 失敗：%w（slot 保留，可重試移除）", wsid, err)
	}

	// 這裡曾經有一段無條件的 restore.ClearResume（provider-keyed）：續聊身分是
	// provider 層的，移除 A 之後不清掉，下一個新建的 session 就會接到 A 的對話；
	// 而清掉的代價是同 provider 存活的手足**一併失去續聊**（Task 26 review
	// round-2 明文記載的已知代價）。
	//
	// per-WSID writer 落地後兩邊都不需要了：續聊身分存在各自的 entry 裡，
	// tombstone 之後 registryResume 對這個 WSID 回空字串，而手足的 entry 從頭到尾
	// 沒被碰過。tombstone_persist 這一步本身就是清除。

	a.removeStep("decrement_count")
	if err := a.manager.RemoveSession(w); err != nil {
		return fmt.Errorf("app: remove session %s: 已 tombstone 但釋放名額失敗：%w", wsid, err)
	}

	return nil
}

// TerminateSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) TerminateSession(wsid string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.terminateSession(wsid)
}

func (a *App) terminateSession(wsid string) error {
	w, p, err := a.resolveWSID("terminate session", wsid)
	if err != nil {
		return err
	}
	h := a.hostFor(w)
	if p == contract.ProviderClaude {
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
		return nil, fmt.Errorf("session registry unavailable (startup error: %s)", a.startupErrText())
	}
	if resume != "" { // resume mismatch 拒絕：cwd ＋ WSID **兩者都要相符**（§3.3 D1）
		boundCWD, boundWSID, ok := a.registry.Lookup(resume)
		if !ok || boundCWD != cwd {
			return nil, fmt.Errorf("resume refused: session %s bound to %q, current %q", resume, boundCWD, cwd)
		}
		// 只比 cwd 擋得住「resume 到別的工作目錄」，擋不住「resume 到同一個
		// workspace 裡別人的 session」——後者正是多 session 之後才第一次可達的
		// 錯接形狀（使用者手動貼一個 id、或 backfill 判準出錯）。fail loud。
		if boundWSID != "" && boundWSID != string(w) {
			return nil, fmt.Errorf("resume refused: session %s belongs to workspace session %s, current %s",
				resume, boundWSID, w)
		}
		if boundWSID == "" {
			// 本 build 之前綁定的舊記錄沒有 wsid 欄位。「不知道」不等於「不符」：
			// 一律拒絕會讓升級後所有既有對話失去續聊（正是 D2 backfill 要保住的
			// 東西）。放行並留稽核軌跡；下一次 init 的 Bind 會補齊欄位，此後這條
			// 分支對該 id 永不再走到。
			a.audit("claude_resume_legacy_binding", map[string]any{
				"wsid": string(w), "resume": resume, "cwd": cwd})
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
		a.removeApprOrderLocked(id)
		a.apprMu.Unlock()
		a.noteWSEmitError("approval_decision", w,
			a.manager.EmitApprovalDecision(w, id, sessionID(), "timeout", ""))
		a.emit("approval:dismiss", map[string]any{"id": id, "wsid": string(w), "cause": "timeout"})
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
			// §3.3 D1：綁定帶 WSID。回傳值不得丟棄——收尾之後遲到的 init 會拿到
			// claude.ErrRegistryClosed，那正是「pump 還活著」的證據，必須留下
			// 稽核而不是靜默通過（owner 2026-08-19）。其餘寫入失敗（磁碟滿、
			// 權限）同樣要看得到：綁定沒寫成，下次 resume 就對不到這個 WSID。
			if berr := a.registry.Bind(info.SessionID, cwd, string(w)); berr != nil {
				a.audit("claude_registry_bind_error", map[string]any{
					"wsid": string(w), "sessionId": info.SessionID, "error": berr.Error(),
					"closed": errors.Is(berr, claude.ErrRegistryClosed)})
			}
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
			host.sess.Terminate, host.sess.Wait, fin, a.after())
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
		payload := map[string]any{"provider": "claude", "wsid": string(host.wsid),
			"stderrTail": ex.StderrTail, "recorderError": recErrText}
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
	// **不自己開交易**（reviewer 2026-08-20）：進得來的路徑都是已經持有交易的
	// binding 薄包裝（AuthStatus／StartLogin／Logout／StartSession／
	// RestartCodexServerRecorded／RecoverCodexRecording）。內層再開一次的話，
	// phase 可能在內外兩次之間翻成 shuttingDown，server 就會建到一半才失敗。
	// 「check 與建立對 shutdown 原子」這個性質改由外層那一筆交易提供。
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
	// 收尾已開始就**不建新 generation**（既有存活的仍照樣沿用，見上面那段）。
	//
	// 這是**資源建立政策**，不是第二個 admission：它不登記交易、不會讓已經被放行
	// 的操作在別的地方被否決一次，擋的只有「收尾之後才生出一個新的子行程」。
	// 沒有它的話，一筆在 shutdown 之前就進場、之後才走到這裡的 start 交易會真的
	// spawn 一個 app-server；那個 server 雖然會被 shutdown 的 Take 收走，但期間
	// 已經動過磁碟與行程表，而且與 watcher 收斂競態。
	if a.phaseNow() == phaseShuttingDown {
		return nil, errors.New("app shutting down")
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

// ---- §3.4.4 session 級錄流證據：[]WireSegmentRef ----

// wireSegmentsPath：per-WSID segment 的 append-only journal 落點。
//
// 刻意**不放進 wire-logs/**：那個目錄的內容物是「一個 generation 一份實體錄流」，
// §3.4 的 connection-wide 不變量（TestWireLogCapturesFramesOfEverySession 的第 4
// 項）就是靠「該目錄下的 .jsonl 恰好一份」守的；把索引檔混進去會讓那條斷言失去
// 意義。segment 是索引不是錄流，放 stateDir 根目錄。
func (a *App) wireSegmentsPath() string { return filepath.Join(a.stateDir, "wire-segments.jsonl") }

// openWireSegments：startup 的 SegmentSet 開檔（跨 app 重啟的載入點——磁碟上既有
// 的 segment 在這裡 replay 回記憶體，For 因此從第一刻起就涵蓋前幾次執行的
// generation）。
//
// 失敗不阻擋啟動、但 fail loud（audit ＋ 啟動警告），比照 replay index 的處置：
// 錄流本體（wire log）仍照錄，缺的是 session 級歸屬索引。刻意不 fallback 到
// NewSegmentSet 的純記憶體版——那會讓「有記錄」與「記錄活不過重啟」在行為上無法
// 區分，正是 §3.4.4 要求 durable 的理由。
func (a *App) openWireSegments(lease *stateLease) {
	// SegmentSet 與 job journal 都是 append-only writer，同樣受 ownership
	// lease 管轄。capability **由參數傳入、不從 a.lease 自己讀**——後者正是
	// openStateWriters 的 doc 說的「退化成註解」的寫法：呼叫端不必出示任何東西，
	// 檢查就變成受測對象自己跟自己對帳。這個入口除了 openStateWriters 之外還被
	// 測試基盤直接呼叫，所以檢查留在這一層，兩層各自可被獨立打紅。
	if !lease.ownsStateDir(a.stateDir) {
		a.noteStartupBlocker("拒絕開啟 codex 錄流 segment 索引：沒有這個 state directory 的 ownership lease（" +
			a.stateDir + "）")
		return
	}
	if err := os.MkdirAll(a.stateDir, 0o755); err != nil {
		a.audit("wire_segments_open_error", map[string]any{"error": err.Error()})
		a.noteStartupWarning("codex 錄流 segment 索引開啟失敗（session 級錄流歸屬停用，wire log 本體不受影響）：" + err.Error())
		return
	}
	s, err := wirelog.OpenSegmentSet(a.wireSegmentsPath())
	if err != nil {
		a.audit("wire_segments_open_error", map[string]any{"error": err.Error()})
		a.noteStartupWarning("codex 錄流 segment 索引開啟失敗（session 級錄流歸屬停用，wire log 本體不受影響）：" + err.Error())
		return
	}
	a.wireSegments = s
	// 歷史歸屬展開的待辦紀錄（契約第 6 條：上次沒做完的在這裡補排進佇列）。
	a.openWireFrameJobs()
}

// beginWireSegment：登記 h 在目前 generation 的錄流起點。
//
// **必須在送出 thread/start｜resume 之前呼叫**：那筆 c2s frame、以及 pending start
// 窗口內抵達的 s2c 通知都屬於這個 session，起點晚一步（例如等到 publishCodexHost）
// 就會把它們漏在 range 外——W6 的「codex resume 以錄流佐證」正是靠 range 涵蓋
// thread/resume 才成立。
//
// **generation 必須由本 session 的 conn 決定，不能讀全域 a.wireGen**（review
// Minor）：a.wireGen 是「目前這一代」，但這個 session 綁的是 ensureAppServer 當下
// 那一台 server；兩步之間若有另一個 goroutine 因 server 死亡換代，讀 a.wireGen 會
// 把 range 記在**這個 session 一個 frame 都沒寫過的那一代**上——不是漏證據，是假
// 證據。GenerationOwner 本來就把 server↔generation 綁成同一個 ownership 單位，
// 直接用 conn 反查即可（generationForConn）。
//
// 查不到（測試 seam codexHostOverride 的外來 conn、錄流尚未建立）時不記段：
// 沒有對得上的 connection-wide 錄流就沒有 frame range 可指，記一段只是造假。
func (a *App) beginWireSegment(h *sessionHost, conn *codex.Conn) {
	gen := a.generationForConn(conn)
	if gen == nil {
		return
	}
	h.wireGen = gen
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	h.wireStart = gen.Frames()
	// 並行重疊登記（§3.4.4 的誠實邊界）：同一 generation 上同時開著兩段以上時，
	// 每一段的 frame range 都不是排他的——共用一條 codex.Conn，連續 range 不可能
	// 互斥。這裡把重疊事實記進**雙方**（新來的與既有的），收尾時才有辦法在證據
	// 出口標明「此 range 非排他」，而不是讓稽核者照排他證據讀。
	if a.wireOpenSegs == nil {
		a.wireOpenSegs = map[string]map[appcore.WSID]*sessionHost{}
	}
	open := a.wireOpenSegs[gen.ID()]
	if open == nil {
		open = map[appcore.WSID]*sessionHost{}
		a.wireOpenSegs[gen.ID()] = open
	}
	if len(open) > 0 {
		h.wireOverlap.Store(true)
		for _, other := range open {
			other.wireOverlap.Store(true)
		}
	}
	open[h.wsid] = h
}

// generationForConn：conn 對應的 generation。server↔generation 的綁定權威是
// codexSingle 持有的 GenerationOwner，不是 a.wireGen（後者只是「最近一次發布的
// 那一代」，供 checkWireRecorder 輪詢用）。
func (a *App) generationForConn(conn *codex.Conn) *wirelog.Generation {
	if conn == nil {
		return nil
	}
	o, ok := a.codexSingle.Current()
	if !ok || o.Server == nil || o.Server.Conn() != conn {
		return nil
	}
	return o.Generation
}

// releaseWireSegment：把 h 自「目前開著段」的登記中移除，並回報這一段期間是否
// 曾與別的 session 重疊。
func (a *App) releaseWireSegment(h *sessionHost, genID string) bool {
	a.wireMu.Lock()
	defer a.wireMu.Unlock()
	if open := a.wireOpenSegs[genID]; open != nil {
		delete(open, h.wsid)
		if len(open) == 0 {
			delete(a.wireOpenSegs, genID)
		}
	}
	return h.wireOverlap.Load()
}

// closeWireSegment：session 收尾時把 [start, end] 這一段 SegmentRef 落盤，並把該
// WSID 的**完整有序 view** 寫進 audit（§3.4.4 的稽核可讀出口）。
//
// 尾界取自 h.wireGen 自己的 frame 計數，不是 a.wireGen：server 意外死亡後
// ensureAppServer 會把 a.wireGen 換成新 generation，用它算尾界會把別的 generation
// 的 frame 併進這一段。
//
// 冪等（h.wireSegOnce）：EndSession、forcedShutdown 與 StartTurn rollback 都可能
// 對同一個 host 呼叫收尾。
func (a *App) closeWireSegment(h *sessionHost) {
	h.wireSegOnce.Do(func() {
		gen := h.wireGen
		if gen == nil {
			return
		}
		overlapped := a.releaseWireSegment(h, gen.ID())
		if a.wireSegments == nil {
			return
		}
		ref := wirelog.SegmentRef{WireLogID: gen.ID(), StartFrame: h.wireStart, EndFrame: gen.Frames() - 1}
		if ref.EndFrame < ref.StartFrame {
			// 這個 session 期間該 generation 一個 frame 都沒寫（錄流已 latch 住寫入
			// 錯誤時可達）。不得補一段空 range 假裝有證據，但也不能無聲跳過。
			a.audit("codex_wire_segment_empty", map[string]any{
				"wsid": string(h.wsid), "wireLogId": ref.WireLogID, "startFrame": ref.StartFrame})
			return
		}
		if err := a.wireSegments.Append(string(h.wsid), ref); err != nil {
			a.audit("codex_wire_segment_error", map[string]any{
				"wsid": string(h.wsid), "wireLogId": ref.WireLogID, "error": err.Error()})
			a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
				"component": "codex_wire_segments", "wsid": string(h.wsid), "error": err.Error()})
			return
		}
		// 寫的是**整份** view 而不只本次新增的那一段：§3.4.4 要回答的是「這個
		// WSID 的錄流散落在哪幾個 generation 的哪些 frame range」，增量回答不了
		// 跨 generation 的問題，而稽核者不該被迫自己重組。
		//
		// label：§3.4.4 末句「既有 recordCase 轉為**該 view 的 label**」——label
		// 屬於這份 view，不是另一條要靠 wsid 事後 join 的獨立稽核線。
		//
		// exclusive／note：**這一格是 Fail Loud 的要求**。range 是「本 session 在
		// 該 generation 存活期間的 frame 窗口」；同一 generation 上若有並行 session，
		// 它們的 frame 就落在這個窗口內。沒有這個限定詞，稽核者會把 range 當排他
		// 證據讀——長命 session 的單一 range 甚至會吞掉它之後在同一代起的每一個
		// session 的全部 frame，實質等於「整代錄流」。
		//
		// frames（§3.4.3 frame-level 歸屬）：range 是**窗口**、frames 是**歸屬**。
		// 上面那個限定詞只說得出「這個 range 不排他」，說不出「那 range 裡哪幾筆
		// 才是我的」——後者是 §5.2「錄流 frame 歸屬不串線」真正要的答案，也是
		// set-level 證據原理上答不出來的一格。
		//
		// **同步只做本代**（owner 契約第 1／2 條）：`gen` 的 FrameIndex 就在記憶體
		// 裡，結算它是 O(歸屬筆數)；先前那幾代要讀磁碟，而 `segs` 會隨這個 WSID 參與
		// 過的 app run 數無界成長（SegmentSet 是永不裁剪的 journal），放在這裡就是把
		// 無界 I/O 放進 EndSession（Wails RPC）與 forcedShutdown。歷史那一段改由
		// 背景 worker 展開（wire_frames.go）。
		segs := a.wireSegments.For(string(h.wsid))
		liveFrames := gen.FrameIndex().FramesOf(string(h.wsid))
		viewID := contract.NewULID(time.Now())
		rec := map[string]any{
			"viewId": viewID, "wsid": string(h.wsid), "label": h.recordLabel,
			"segments": segs, "exclusive": !overlapped,
			"frames": map[string][]int{gen.ID(): liveFrames}}
		if overlapped {
			rec["note"] = "本次 range 期間同一 generation 上有並行 session：" +
				"range 內含他 session 的 frame，不得當作排他證據讀（§3.4.4 已知邊界）；" +
				"逐 frame 的歸屬見 frames"
		}
		// framesStatus（契約第 5 條）：「證據還沒算完」必須是可觀測狀態，不能靠事後
		// 推測。沒有歷史 generation 要展開時直接 resolved，不排背景工作。
		pending := pendingWireLogIDs(segs, gen.ID())
		if len(pending) == 0 {
			rec["framesStatus"] = "resolved"
			a.audit("codex_wire_segments", rec)
			return
		}
		rec["framesStatus"] = "pending"
		rec["pendingWireLogs"] = pending
		a.audit("codex_wire_segments", rec)
		a.enqueueWireFrameJob(wireFrameJob{ViewID: viewID, WSID: string(h.wsid),
			LiveGenID: gen.ID(), LiveFrames: liveFrames, SegCount: len(segs)})
	})
}

// pendingWireLogIDs：segs 裡除了 live 那一代之外、還需要讀磁碟才知道歸屬的 generation
// （去重、保持 segs 的順序——證據出口不得有不決定性的順序）。
func pendingWireLogIDs(segs []wirelog.SegmentRef, liveID string) []string {
	seen := map[string]bool{liveID: true}
	var out []string
	for _, s := range segs {
		if seen[s.WireLogID] {
			continue
		}
		seen[s.WireLogID] = true
		out = append(out, s.WireLogID)
	}
	return out
}

// wireStep：受控復原／replacement 的步驟探針（測試注入，見 hookWireStep）。
func (a *App) wireStep(step string) {
	if h := a.hookWireStep; h != nil {
		h(step)
	}
}

// newWireGeneration 配置新的 wire_log_id 並開檔。序號讓同一秒內的多次
// replacement 也不會撞 id（wirelog.NewGeneration 與 recorder.New 同慣例，同名
// 會直接覆寫舊檔，不做去重保護）。
//
// **run token（§3.4.4 接線時補）**：秒級時間戳＋序號只在同一個 app 執行內唯一，
// 序號每次啟動都從 1 重數——崩潰後立即重開、或使用者連續重啟，兩次執行的第一個
// generation 會落在同一秒而配到同一個 id，新檔直接覆寫舊檔。此前這只是「舊錄流
// 被蓋掉」，接上 []SegmentRef 之後更嚴重：上一次執行留下的 SegmentRef 會指向一
// 份內容已被換掉的 wire log，frame range 對應到別人的 frame。run token 是 app
// 啟動後第一次配置 generation 時取一次的 ULID 尾碼，把唯一性擴到跨執行。
func (a *App) newWireGeneration() (*wirelog.Generation, error) {
	a.wireMu.Lock()
	if a.wireRun == "" {
		u := contract.NewULID(time.Now())
		a.wireRun = strings.ToLower(u[len(u)-8:]) // 尾碼即 ULID 的 randomness 段
	}
	a.wireSeq++
	id := fmt.Sprintf("codex-wire-%s-%s-%03d", time.Now().UTC().Format("20060102T150405"),
		a.wireRun, a.wireSeq)
	a.wireMu.Unlock()
	// request id 隨新 conn 從 1 重新起算：舊 generation 的殘留登記必須在新 sink
	// 開始寫之前清掉，否則新連線的 id=1 response 會繼承到上一代某筆請求的歸屬。
	a.resetWireRequestWSID()
	return wirelog.NewGeneration(a.wireLogDir(), id, a.resolveWireFrameWSID)
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

// noteWireGeneration 把錄流監控對象換成新 generation。
//
// **只要 owner 有發布就必須更新，與 RunOwnedHandshake 是否回錯無關**：它的契約
// 明寫「err != nil 不代表沒有發布」——新 server handshake 成功、舊 owner 收尾失敗
// （drain 逾時、detach 或 meta 寫入失敗）就是這個組合。那時新 server 已經在服務，
// 若 wireGen 還指著舊的、已 Finalize 的 handle，checkWireRecorder 會永遠輪詢一個
// 死掉的 generation ⇒ 新 generation 的寫入失敗永遠不會 latch、不會通知，正是本
// task 要防的靜默降級。清 latch（clearWireLatch）才是「全部成功」才做的事，兩者
// 刻意分開。
func (a *App) noteWireGeneration(gen *wirelog.Generation) {
	a.wireMu.Lock()
	a.wireGen = gen
	a.wireMu.Unlock()
}

// clearWireLatch 在新 generation 的 recorder 掛載、handshake 與發布全部成功之後
// 解除 latch——這是 §3.4.6 凍結的**唯一**解除條件（不因時間或重試次數自動解除）。
// 監控對象的切換由 noteWireGeneration 負責（見其 doc：兩者的條件不同）。
func (a *App) clearWireLatch() {
	a.wireMu.Lock()
	was := a.wireErr
	a.wireErr = nil
	id := ""
	if a.wireGen != nil {
		id = a.wireGen.ID()
	}
	a.wireMu.Unlock()
	if was == nil {
		return
	}
	a.audit("codex_wire_log_recovered", map[string]any{"wireLogId": id, "previousError": was.Error()})
	a.manager.EmitWorkspace(string(contract.KindStreamError), nil, map[string]string{
		"component": "codex_wire_log", "wireLogId": id, "event": "recovered"})
}

// checkWireRecorder 把目前 generation latch 住的寫入錯誤升級成 App 層 latch。
//
// 為什麼要輪詢：錄流 sink 掛在 codex.Conn 內部，寫入錯誤只 latch 進
// wirelog.Generation.writeErr（Conn.record 看到 sink 回錯只記進 recErr，不通知
// 任何人），沒有推播管道。
//
// **取用點（精確覆蓋面）**：只有 wireCodexConn 安裝的 OnNotification 與
// OnUnknown 兩條 handler，以及新 Codex session 的建立閘門（codexWireGate）。
// **不含** client Call 的 response frame（readLoop 直接送進 pending map）、
// 也不含 OnServerRequest 的 approval 請求——那兩條路徑同樣會經 Conn.record 寫
// 錄流，但錯誤要等下一筆 notification／unknown frame 或下一次建立 session 才會
// 被看見。c2s 寫入失敗同理。這是有界的延遲，不是遺漏：latch 本身不會被清掉。
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
	if h := a.hookAfterCodexPublish; h != nil { // 測試 barrier：見該欄位 doc
		h()
	}
	o, published := a.codexSingle.Current()
	if published {
		a.wireCodexConn(o.Server.Conn()) // 發布成功即接上 handlers
		a.noteWireGeneration(o.Generation)
	}
	if err != nil {
		return err // §3.4.6：任一步失敗即 dispose 新 server 並保留 latch
	}
	if !published {
		// err == nil 但 Single 已空：新 server 在「發布」與這裡的 Current() 之間
		// 就死了，它自己的 watcher（RunOwnedHandshake 返回後立即啟動）以
		// CompareAndTakeEpoch 搶先取走並 finalize 了那份 generation。此時既沒有
		// 可用的 server 也沒有活著的錄流，**不得清 latch**（清了等於宣告錄流已
		// 復原，但下一次 ensureAppServer 才會重建）；fail loud 讓呼叫端知道這輪
		// 沒有成功。不加這個守衛就會對 nil owner 取 o.Generation → panic。
		return errCodexNotRunning
	}
	a.clearWireLatch()
	return nil
}

// refuseIfCodexLive：受控 replacement／復原的拒絕條件（§3.4.7）——存在 live
// Codex host 或 in-flight turn 一律拒絕，只有 dormant session 不阻擋。
// in-flight turn 先判：它是兩者中較嚴重、訊息也較精確的那一個。
//
// **已知 TOCTOU 窗口（Task 13 review Important-3，帶進 Phase 5）**：本檢查讀的是
// hostsOf（a.mu 下的快照），但 startCodex 只在 ensureAppServer 期間持
// codexServerMu，`startCodexHost`（EnsureThread → publishCodexHost）整段跑在鎖外。
// 因此「正在建立中、尚未 publish 的 codex host」看不到：replacement 可能在該窗口
// 內放行並 terminate 舊 server，讓那筆 EnsureThread 對已死連線失敗。失敗是**可見
// 的**（session 起不來、回錯），不是靜默漏錄；且 latch 情境下新 session 已被
// codexWireGate 擋住，故 RecoverCodexRecording 大致不受影響，暴露面是 B1 的
// RestartCodexServerRecorded。相對於改動前（完全沒有拒絕條件、直接換掉 server）
// 仍是淨改善。收法留給 Phase 5：把 host 建立一併納入 codexServerMu。
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

// RecoverCodexRecording：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) RecoverCodexRecording() error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.recoverCodexRecording()
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
func (a *App) recoverCodexRecording() error {
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

// ---- §3.4.3 frame-level 歸屬：wire log 每一筆 frame 的 WSID ----
//
// SegmentSet（§3.4.4）回答「某 WSID 橫跨哪些 generation 的哪些 range」；並行
// session 共用同一條 codex.Conn，那些 range 必然互相涵蓋（closeWireSegment 的
// exclusive 限定詞就是在講這件事）。「重疊 range 裡的**這一個** frame 到底屬於誰」
// 只有 frame-level 歸屬答得出來，兩者不可互相替代。
//
// 判定發生在**寫入當下**（wirelog.WSIDResolver），結果寫進該 frame 自己那一行的
// wsid 欄位，因此 app 重啟後 RebuildFrameIndex 讀得回來——歸屬不是只活在記憶體裡的
// 註記。判不出來就留空、frame 照寫（§3.4.5）。
//
// 三條判定路徑（依 frame 形狀分流，見 resolveWireFrameWSID）：
//
//	response（有 id、無 method）  → 繼承對應 request 的歸屬（wireReqWSID）
//	request（有 id、有 method）   → identity 判定；判到就登記給未來的 response 繼承
//	notification（無 id）         → identity 判定
//
// identity 判定一律走 dispatcher 的同一支 codexWSIDFor（turnId → threadId →
// pending start）。**刻意共用而不另寫一份**：兩份查找表在路由規則變動時必然漂移，
// 而漂移的後果正是「事件送對 session、錄流證據卻歸錯」這種最難察覺的形狀。

// resolveWireFrameWSID 是掛進 wirelog.Generation 的 write-time 歸屬判定。
//
// 併發：由 codex.Conn.record 呼叫——c2s 在送訊息的 goroutine 內（Conn.send）、s2c
// 在 readLoop 內，兩者都不持有 a.mu（repo 既有慣例是「取 a.mu 讀出 conn → 放鎖 →
// 才 Call」，見 forcedShutdown 與 InterruptTurn），所以這裡取 a.mu 不會與送訊息端
// 死鎖。wirelog.Line 也刻意在自己的鎖之外呼叫（見 WSIDResolver doc）。
//
// s2c 的判定順序天然正確：Conn.readLoop 先 record 再 dispatch，所以 turn/completed
// 這一筆 frame 是在 unbindCodexTurn **之前**判定的；反過來，turn/start 的 response
// 是在 unbind 之後才抵達的，那筆靠 wireReqWSID 繼承，不靠 identity。
func (a *App) resolveWireFrameWSID(dir wirelog.Direction, raw []byte) string {
	var f struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "" // 解不開的 frame 照寫、WSID 留空（§3.4.5），不猜、不丟
	}
	id := strings.TrimSpace(string(f.ID))
	if id == "null" {
		id = ""
	}
	switch {
	case id != "" && f.Method == "":
		// response：id 是**對方**發的 request 的 id，歸屬繼承自那一筆。
		return a.takeWireRequestWSID(oppositeWireDir(dir), id)
	case id != "":
		// request：c2s 的 thread/turn 呼叫、s2c 的兩種 requestApproval。
		w := a.wireIdentityWSID(dir, f.Method, f.Params)
		if w != "" {
			a.rememberWireRequestWSID(dir, id, w)
		}
		return w
	default:
		// notification：沒有 request id，只有 thread／turn identity 能區分。
		return a.wireIdentityWSID(dir, f.Method, f.Params)
	}
}

// wireIdentityWSID：以 frame 的 threadId／turnId 判定歸屬。
//
// thread/start 的 c2s frame 是唯一「屬於某個 session、卻連 threadId 都還沒有」的
// 形狀：thread id 是這一筆請求的**回傳值**。thread/resume 帶 threadId、turn/* 帶
// threadId，都走得到 codexWSIDFor；只有它需要退回 pending start 登記。
//
// 這個退路刻意**只認 thread/start**，不是「c2s 沒有 identity 就歸給 pending」：
// initialize／initialized／account/* 也沒有 identity，它們是連線層的 frame，不屬於
// 任何 session，落在 pending 窗口內就被吞進某個 session 是假證據。
func (a *App) wireIdentityWSID(dir wirelog.Direction, method string, params []byte) string {
	threadID, turnID := codexFrameIdentity(params)
	if w, ok := a.codexWSIDFor(threadID, turnID); ok {
		return string(w)
	}
	if dir == wirelog.DirClientToServer && method == codex.MethodThreadStart {
		if w, ok := a.soleCodexPendingStart(); ok {
			return string(w)
		}
	}
	return ""
}

// soleCodexPendingStart：恰好一筆 pending start 時回傳它（理由與 codexWSIDFor 的
// 第三順位相同：codexStartMu 保證至多一筆，出現兩筆代表不變量已破，寧可不歸屬）。
func (a *App) soleCodexPendingStart() (appcore.WSID, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.codexPendingStarts) != 1 {
		return "", false
	}
	for _, w := range a.codexPendingStarts {
		return w, true
	}
	return "", false
}

// oppositeWireDir：response 的方向 → 它所回應的那筆 request 的方向。
func oppositeWireDir(dir wirelog.Direction) wirelog.Direction {
	if dir == wirelog.DirClientToServer {
		return wirelog.DirServerToClient
	}
	return wirelog.DirClientToServer
}

func wireReqKey(reqDir wirelog.Direction, id string) string { return string(reqDir) + ":" + id }

// rememberWireRequestWSID：登記某筆 request frame 的歸屬，供其 response 繼承。
func (a *App) rememberWireRequestWSID(reqDir wirelog.Direction, id, wsid string) {
	a.wireReqMu.Lock()
	defer a.wireReqMu.Unlock()
	if a.wireReqWSID == nil {
		a.wireReqWSID = map[string]string{}
	}
	a.wireReqWSID[wireReqKey(reqDir, id)] = wsid
}

// takeWireRequestWSID：取出並移除登記（一筆 request 至多一筆 response）。
func (a *App) takeWireRequestWSID(reqDir wirelog.Direction, id string) string {
	a.wireReqMu.Lock()
	defer a.wireReqMu.Unlock()
	k := wireReqKey(reqDir, id)
	w := a.wireReqWSID[k]
	delete(a.wireReqWSID, k)
	return w
}

// resetWireRequestWSID：換 generation 時清空（見 wireReqWSID 欄位 doc）。
func (a *App) resetWireRequestWSID() {
	a.wireReqMu.Lock()
	defer a.wireReqMu.Unlock()
	a.wireReqWSID = map[string]string{}
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
		a.manager.EmitApprovalRequest(w, id, threadID, method, params))
	a.emit("approval:request", map[string]any{
		"id": id, "wsid": string(w), "provider": "codex", "toolName": method, "inputJson": string(params)})
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
		a.removeApprOrderLocked(id)
		a.apprMu.Unlock()
		uiDecision = "timeout"
		a.audit("codex_approval_timeout", map[string]any{"id": id})
		a.emit("approval:dismiss", map[string]any{"id": id, "wsid": string(w), "cause": "timeout"})
	}
	a.noteWSEmitError("approval_decision", w,
		a.manager.EmitApprovalDecision(w, id, threadID, uiDecision, reason))
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
// progress」，整個 session 起不來。recordCase 因此降為 **label**：不控制 recorder
// attach，改掛在該 session 的 []WireSegmentRef view 上（`sessionHost.recordLabel`
// → `codex_wire_segments` 的 label 欄位，§3.4.4 末句的「轉為該 view 的 label」）。
// 起始那筆獨立 audit（codex_record_label）保留：它記的是「這個 session 起手時宣告
// 的 label」，與收尾時附在證據上的那份是不同時點的事實。W6 的「codex resume 以
// JSON-RPC 錄流佐證」改由 wire log 承載——**錨點也換了人守**：舊版是這裡的
// attach 鎖住「送 resume 之前就開錄」，新版由 RunOwnedHandshake 的
// gen_id → start → attach → handshake 順序負責（見 owner_test.go 與
// app_wirelog_latch_test.go 的順序測試）。
func (a *App) startCodexHost(w appcore.WSID, host codexHost, prompt, resume, recordCase, approvalPolicy string) (string, bool, error) {
	conn := host.Conn()
	if approvalPolicy == "" { // M0 驗證定位沿用：commandExecution 一律 requestApproval
		approvalPolicy = "untrusted"
	}
	runner := codex.NewThreadRunner(conn)
	// B1（owner 2026-08-21）：每一輪 turn/start 帶 workspace-write sandbox，否則
	// codex 預設 read-only、approval 的 accept 也放寬不了 sandbox → workspace 內
	// 寫入必然 EPERM（見 docs/spikes/codex-approval-eperm.md）。untrusted 下寫入
	// **仍會出 approval**；workspace 外與網路仍受 sandbox 邊界。thread/start 帶此
	// 欄無效（被靜默忽略），故設在 runner、由每輪 StartTurn 帶出。
	runner.SetTurnSandbox(codex.SandboxWorkspaceWrite)
	if recordCase != "" { // label-only：留下可觀測軌跡，不影響任何錄流行為
		a.audit("codex_record_label", map[string]any{"wsid": string(w), "label": recordCase})
	}

	// §3.4.4 錄流 segment：host 先建、起點先記，兩者都必須早於 thread/start 送出
	// （見 beginWireSegment 的 doc）。h 在 publishCodexHost 之前只有本 goroutine
	// 看得到，threadID 補上去仍符合「publish 前寫定」的規約。
	h := &sessionHost{wsid: w, provider: contract.ProviderCodex, sockIndex: -1,
		runner: runner, recordLabel: recordCase}
	a.beginWireSegment(h, conn) // generation 由本 session 的 conn 決定，不讀全域 a.wireGen

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
		// session 起不來，但 thread/start｜resume 那幾筆 frame 已經進了 wire log
		// ——證據照留（失敗的嘗試同樣要可稽核，比照失敗 generation 仍保留
		// wire_log_id 與收尾證據）。
		a.closeWireSegment(h)
		return "", false, err
	}

	h.threadID = threadID
	a.publishCodexHost(h) // 發布：首輪事件的 handler ownership（host ＋ threadId 路由）
	endPending()          // host 已可經 threadId 命中：pending 窗口到此為止

	_, alreadyEnded, err := runner.StartTurn(ctx, prompt)
	if err != nil {
		a.takeHost(h) // rollback：registry 不留半成品
		a.forgetCodexHostRouting(h)
		a.closeWireSegment(h) // 同上：已寫進 wire log 的 frame 仍要歸屬得到
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
// w 只服務 h == nil 的分支（那條路徑沒有 host 可問 WSID）；h != nil 時一律用
// h.wsid。兩者恆等——全部呼叫端傳的 w 都是取得該 host 的同一個 WSID（`hostFor(w)`
// 或 `h.wsid` 本身），host registry 也以 WSID 為 key，不可能取到別人的 host。
func (a *App) codexTeardown(w appcore.WSID, h *sessionHost) error {
	if h == nil {
		a.emitCodexSessionDone(w, "")
		return nil
	}
	a.takeHost(h)
	a.forgetCodexHostRouting(h)
	h.track.NoteEnded()
	// §3.4.4：session 級錄流證據在這裡落盤——收尾點正是 frame range 的尾界。
	// 排在 takeHost 之後：此後沒有新讀者能拿到這個 host，也就不會再有新的 frame
	// 被歸屬到它。
	a.closeWireSegment(h)
	// §3.4.4 之後 codex 已無 session-scoped 錄流可 finalize（lease 只由
	// startClaude 寫入），這裡因此沒有 recorder 錯誤可回報。簽名保留 error 是
	// EndSessionFlow 的 teardown 契約要求。
	a.emitCodexSessionDone(h.wsid, "")
	return nil
}

// emitCodexSessionDone：codex 收尾的 UI 事件（長駐 server 不隨 session 退出，
// 因此 processStillRunning 恆為 true、stderr 取 live snapshot）。
func (a *App) emitCodexSessionDone(w appcore.WSID, recorderErr string) {
	stderr := ""
	if srv, serr := a.currentAppServer(); serr == nil {
		stderr = srv.StderrSnapshot()
	}
	a.emit("session:done", map[string]any{"provider": "codex", "wsid": string(w),
		"processStillRunning": true, "stderrTail": stderr, "recorderError": recorderErr})
}

// RestartCodexServerRecorded：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) RestartCodexServerRecorded(recordCase string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.restartCodexServerRecorded(recordCase)
}

// RestartCodexServerRecorded：B1 受控重啟 probe。
//
// M3b §3.4 之後它與 RecoverCodexRecording 是同一件事的兩個入口，共用
// replaceCodexGeneration：錄流不再是 probe-scoped（原本成功前會 StopRecording ＋
// CloseWith），而是交棒給 connection-level 的 always-on wire log，錄到該 server
// 終止為止。recordCase 因此**只剩 label**（進 audit），不再控制 recorder attach
// （§3.4.4）；拒絕條件與復原入口相同（§3.4.7：live host／in-flight turn 一律拒絕）。
func (a *App) restartCodexServerRecorded(recordCase string) error {
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

// AuthStatus：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) AuthStatus(provider string) (string, error) {
	if err := a.beginTxn(); err != nil {
		return "", err
	}
	defer a.endTxn()
	return a.authStatus(provider)
}

func (a *App) authStatus(provider string) (string, error) {
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

// StartLogin：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) StartLogin(provider string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.startLogin(provider)
}

func (a *App) startLogin(provider string) error {
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

// CancelLogin：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) CancelLogin(provider string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.cancelLogin(provider)
}

// CancelLogin 取消進行中的 codex 官方登入（account/login/cancel，schema 必填 loginId）。
func (a *App) cancelLogin(provider string) error {
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

// Logout：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) Logout(provider string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.logout(provider)
}

func (a *App) logout(provider string) error {
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
	if a.wsReg == nil {
		return
	}
	// 已 tombstone／entry 不存在由 SetResume 在 store mutex 內拒絕（全序，
	// 不是 check-then-act）——見 noteRegistryWriteResult。
	a.noteRegistryWriteResult(host.wsid, contract.ProviderClaude, "set_resume",
		a.wsReg.SetResume(string(host.wsid), sessionID))
}

// RestoreViews：provider-keyed 的 view 重放來源（唯讀——不 spawn provider、
// 不回寫 audit）。
//
// **Task 26 之後前端不再呼叫它**：session 清單改由 ListSessions（registry 權威）
// 提供，transcript 改由 §3.8 的 LoadTurnsBefore 以 WSID 視窗化載入；`frontend/src`
// 對它零引用，連 makeBindings 都沒有轉發。
//
// 保留的理由僅有一個且要說準：它是 M1.5 恢復語意測試的既有唯讀出口（Go 端數處
// **測試**引用），刪掉會連帶重寫那一組。它**不是** resume 的讀取端——M3b per-WSID
// writer 之後 production 的 resume 讀取走 registryResume(wsid)（該 WSID 自己的
// registry entry），`a.restore.Get(...)` 與 providerResumeFallback 都已不在那條
// 路徑上（後者已移除）。restore.json 現在只剩 legacy 遷移與升級 backfill 兩個
// 消費者，本方法可在 M3b 收尾時直接刪除。
// RestoreViews：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
//
// **簽章加上 error**（reviewer 2026-08-20）：它讀的是 startup 才發布的
// a.restore，而 Wails 的 OnStartup 與 bindings 會並行——啟動期呼叫會 nil pointer
// panic。加了 error 之後，「還沒 ready」才有辦法據實回報；回一個空 map 假裝沒有
// 待還原的 view 是更糟的謊。
func (a *App) RestoreViews() (map[string]RestoredView, error) {
	if err := a.beginTxn(); err != nil {
		return nil, err
	}
	defer a.endTxn()
	return a.restoreViews()
}

func (a *App) restoreViews() (map[string]RestoredView, error) {
	out := map[string]RestoredView{}
	for _, p := range []string{"claude", "codex"} {
		e := a.restore.Get(p)
		out[p] = RestoredView{
			Envelopes:       replayViewWindow(a.eventsPath(), p, e.ViewStartEventID),
			ResumeSessionID: e.ResumeSessionID,
			TaskID:          e.TaskID,
		}
	}
	return out, nil
}

// NewSession：Wails binding 的固定形狀——開交易 → defer 收尾 → 呼叫實作。
// 形狀由 app_binding_surface_test.go 逐項驗證；不得在此插入任何其他敘述
// （含參數驗證與 provider 分支），那會讓一部分分支落在交易之外。
func (a *App) NewSession(wsid string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.newSession(wsid)
}

// NewSession：New 專用原子流程（plan D4）。收尾成功才重設恢復視窗；失敗回錯、
// UI 不重設；另一 provider 完全不受影響。resetting phase 涵蓋
// 「teardown → restore reset」整段（期間 Start 回 ErrResetInProgress）。
//
// review P1（fix/lifecycle-app-txn）：整段納入 app transaction（beginTxn／
// endTxn）——理由與 EndSession 上方 doc 完全相同（同一類 shutdown race，
// NewSession 也是「BeginEndSession 成功才會呼叫 teardown、失敗就直接回錯」，
// 沒有 fallback 兜底重跑，故同樣不需要共用 host.teardownFn）。
func (a *App) newSession(wsid string) error {
	w, pv, err := a.resolveWSID("new session", wsid)
	if err != nil {
		return err
	}
	// registry uncertain：在 teardown 之前早退。放行的話對話會被真的結束、UI
	// 重設，但 view boundary 前移寫不進去——重啟後那些「已經開新對話」的舊
	// turn 會整批復活，正是 §3.8 boundary 要防的事。
	if a.registryUncertain() {
		return errRegistryUncertain
	}
	provider := string(pv)
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
			tearErr = a.codexTeardown(w, host)
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
	// view boundary 前移 ＋ 清空續聊身分，一次交易寫進**該 WSID 自己的** entry
	// （owner 2026-08-17 D4 凍結）。以前這是 provider-keyed 的破壞性寫入，對 B 按
	// 「開新對話」會把 A 的 resume 一併清掉，所以只好在不明確時整段跳過——而跳過
	// 的代價是「開新對話」在跨重啟那一維完全沒有效果。per-WSID 之後兩難消失：
	// 無條件寫，且只影響自己。
	//
	// boundary 的消費端是 LoadTurnsBefore（§3.8 視窗化載入）；不得改用「刪掉
	// index 裡的舊 turn record」來實作——VerifyOrRebuild 會從 audit 全量重建，
	// 刪掉的下次啟動就長回來（§3.5.10：index 是快取，不是第二份事件格式）。
	var rerr error
	if a.wsReg != nil {
		hw, _, _ := auditHighWatermark(a.eventsPath())
		err := a.noteRegistryUncertainErr("reset_view", wsid,
			a.wsReg.ResetView(wsid, hw))
		switch {
		case err == nil:
		case errors.Is(err, wsregistry.ErrEntryNotFound), errors.Is(err, wsregistry.ErrTombstoned):
			// 良性：這個 WSID 已不該有 durable view（已移除／未接線的測試路徑）。
			// lifecycle 照樣收束回 idle、UI 照樣重設，只留稽核軌跡。
			a.audit("reset_view_skipped", map[string]any{
				"provider": provider, "wsid": string(w), "reason": err.Error()})
		default:
			rerr = err
		}
	}
	finErr := a.manager.FinishReset(w, rtok) // restore 失敗仍 FinishReset 回 idle
	if rerr != nil {
		return errors.Join(rerr, finErr) // 失敗回錯：UI 不重設
	}
	return finErr
}
