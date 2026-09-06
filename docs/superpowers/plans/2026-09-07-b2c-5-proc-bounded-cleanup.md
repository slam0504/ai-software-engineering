# B2c-5 proc supervisor 有界清理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev2（2026-09-07，design gate 第一輪 CHANGES_REQUIRED 後修訂：四項 blocking——(1) fail-loud 案例改用受控 descendant 持續持有 stdout／stderr write end＋最後一個注入 timer 先停在 barrier、測試收尾以真實訊號清除；(2) 補 B2c-4 凍結的兩層 fork 真實程序具名測試 `TestSupervisorCleanupTwoLayerForkOrphan`（proc 測試內 inline fixture，`testdata/**` 不變），納入 Task 1／Task 3／Gate A 供 B2c-7 指定；(3) `stdoutReader.Close()` 先設旗標再關底層、只把 `os.ErrClosed` 正規化為 nil、helper 測試涵蓋連續兩次 `Close()` 與 caller-first／supervisor-first；(4) errno 分支以 table-driven subcases 常駐（最終 probe `EPERM`／其他 errno → `CleanupIncomplete`；rekill 回 `ESRCH` 立即停止、`EPERM`／其他繼續，斷言後續 probe 次數、是否停止、rekill 事件數）；D1／D3／D4／D5 通過、D2 方向通過、D6 三處通過（`groupGone` 留 B2c-6、真實程序 rekill 0–10）；前版：rev1）
> 狀態：**待 owner 複核 rev2 四項修正**。尚未修改任何 code、未委派。
> 票源：Pre-M4 Readiness Backlog **B2c-5**（rev20 新增，**0.6 pt**＝5.0–7.0 hr，owner 2026-09-07 採用）。依 **B2c-4 裁定記錄** `docs/superpowers/plans/2026-09-07-b2c-4-supervisor-cleanup-contract-decision.md` rev3（owner APPROVED）§3 O1 凍結狀態機、D1–D10 實作；不重新開放任何已裁定事項。
> 基準：`main`＝`origin/main`＝`cadb065`（register v6 補記、backlog rev20、diagnosis-record v2 勘誤）。分支 **`b2c5/proc-bounded-cleanup`**（本機，自 `cadb065`）。`internal/proc/proc.go` 自 `82caf8b`（B1a-1）後未變。
> 授權邊界：本票修改 production（`internal/proc`、`internal/ports`、`internal/claude`、`internal/codex`、`internal/recorder`、`internal/assist`、`app.go` 揭露點）與既有測試；**`testdata/**` 不變（proc 測試內可新增 inline fixture，含 B2c-4 凍結的兩層 fork 形狀）、不放寬 5 秒 guard、不加 retry 於被測路徑、不改 `Terminate()` 介面與升級路徑、不改 `Done()` 以外的既有語意**。合併 main 需 owner 授權（fast-forward push）。B2a 維持 blocked。

**Goal:** 依 B2c-4 裁定實作 supervisor 有界清理：第一次 cleanup KILL 三路分流、固定絕對偏移 1–512 ms 探測與重送、1 s 不送訊號確認、預算耗盡 fail-loud（`Exit.CleanupIncomplete`、強制解除本端 stdout／stderr 等待、`ErrCleanupIncomplete` 薄包裝），並把 `CleanupIncomplete` 傳到 `ports.Exit`、codex meta、assist 與 app 層wire log（原始通訊紀錄）meta；以可注入 seam 做確定性白箱測試（六案＋mutation）。

**Architecture:** 所有時序與訊號經三個 nil-safe seam（`cleanupAfter`／`groupProbe`／`cleanupSignal`，沿 B1a-1 `seamAfter`／`seamOnSignal` 慣例：欄位在 `p.mu` 下讀寫、回傳值解鎖後使用、nil 退回真實實作），production 不暴露任何 `Config` 旋鈕（B2c-4 D2）。狀態機完整跑在既有 supervisor goroutine 內、於 `close(exitedCh)` 之後、`wg.Wait()` 之前；`Done()` 語意改述為「`Exit` 已快取（stderr EOF 或強制解除其一）」。事件模型只新增 `sigEventSupervisorCleanupRekill`（實際成功送出才發），不新增 gave-up 事件（D9）。跨套件揭露不引入任何 hook：codex 用既有 `stubServer`（`ProbeTarget` 介面）；claude／assist 把純映射與 pump 抽成套件內函式做單測（D5 本 plan）。

**Tech Stack／參考文件：** `internal/proc/proc.go`（`Start` 124–215、seam 60–105、`Terminate` 291–331、`Exit` 21–25）；`internal/proc/proc_test.go`（`bashProc` 19、`drainStdout` 29、`groupGone` 37、`killAndReap` 496、`TestTerminateEscalatesViaInjectedTimerInOrder` 506、`TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned` 603）；`internal/ports` `Exit` 15–19；`internal/claude/session.go` 68–131（pump 96–116、`Wait` 126–129）；`internal/codex/owner.go`（`ProbeTarget` 22、`FinalizeWith` 137–167）、`internal/codex/rpc.go:92` `NewConn(stdin io.Writer, stdout io.Reader)`；`internal/recorder/recorder.go` `Meta` 14–28；`internal/assist/oneshot.go` `Run` 128–175；`app.go` 7476–7482（claude wire log（原始通訊紀錄）meta）、7585–7592（結果 payload）。Go `os.Pipe` 為 poller 管理（`os/file_unix.go` `kindPipe` pollable），`Close()` 會讓阻塞中的 `Read` 以 `os.ErrClosed` 返回。

---

## 凍結事項（承 B2c-4，不再裁定）

- 狀態機：base＝第一次 cleanup KILL **返回**時刻（不限成功）；`ESRCH` 完成不排程；`nil` 發 `sigEventSupervisorCleanupKill`（恰一次）並排程；`EPERM`／其他 errno 不發事件但排程。排程於絕對偏移 1／2／4／8／16／32／64／128／256／512 ms 探測 `kill(-pgid, 0)`：`ESRCH` 完成；`nil` 立即 rekill（rekill 回傳 `nil` 才發 `sigEventSupervisorCleanupRekill`、`ESRCH` 完成、`EPERM`／其他繼續）；`EPERM`／其他不送、繼續。1 s 最終確認不送訊號：非 `ESRCH` 一律 `CleanupIncomplete`。
- fail-loud：`Exit.CleanupIncomplete=true`；關閉 proc 持有的 stderr／stdout read end；`p.Stdout` 薄包裝**只在** supervisor 設定強制關閉狀態後才把底層 closed error 映射為 `proc.ErrCleanupIncomplete`（`errors.Is`），呼叫端自行 `Close()` 不誤標；快取 Exit、`close(doneCh)`。`Exit.Err` 不混入。
- 揭露：`ports.Exit.CleanupIncomplete`、codex `recorder.Meta.cleanup_incomplete`、assist one-shot 回傳 wrap `ErrCleanupIncomplete` 的錯誤；claude 的 stream 讀到 `ErrCleanupIncomplete` 走既有 `KindStreamError` 路徑。
- 既有 `TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned` 改名為「第一次 cleanup signal 成功」語意，逐事件種類斷言精確次數；退避時間表用獨立 nil-safe seam；不新增 gave-up 事件。

---

## 待 owner 裁定（D1–D6）

- **D1（seam 形狀）**：三個 seam 皆為 `Proc` 未匯出欄位＋nil-safe 存取子：`cleanupAfter afterFunc`（與 `after` 分離，避免與 escalation timer 注入互相干擾）、`groupProbe func(pgid int) error`（nil → `syscall.Kill(-pgid, 0)`）、`cleanupSignal func(pgid int, sig syscall.Signal) error`（nil → `syscall.Kill(-pgid, sig)`；只用於 cleanup 路徑，`Terminate`／escalation 仍走 `SignalGroup`）。絕對偏移實作為 `<-cleanupAfter(time.Until(base.Add(off)))`（負值即立即），測試注入可回傳已關閉 channel 壓縮整個時間表並記錄每次 duration。是否核准。
- **D2（薄包裝與強制關閉的邊界）**：`p.Stdout` 改為 `*stdoutReader{f *os.File, p *Proc}`（仍為 `io.ReadCloser`，型別對外不變）。`Read`：呼叫 `f.Read`；若 `err != nil && errors.Is(err, os.ErrClosed)` 且 `p.forcedClosed && !p.callerClosedStdout`（兩者皆在 `p.mu` 下讀）→ 回傳 `fmt.Errorf("%w: %v", ErrCleanupIncomplete, err)`，否則原樣回傳。`Close`：**先**在 `p.mu` 下設 `callerClosedStdout=true`，**再**呼叫底層 `f.Close()`；Go 的 `os.File.Close` 重複呼叫會回錯，故只把 `errors.Is(err, os.ErrClosed)` 正規化為 `nil`，其他錯誤照常回傳（連續兩次 `Close()` 皆成功）。supervisor 的強制關閉同樣忽略 `os.ErrClosed`。強制關閉：supervisor 在 `p.mu` 下設 `forcedClosed=true`，解鎖後 `errR.Close()`＋`f.Close()`（stdout）。「呼叫端先 `Close()`、supervisor 後強制關閉」→ 不映射（呼叫端意圖優先）；「supervisor 先強制關閉、呼叫端後 `Close()`」→ 之後的 `Read` 不映射（呼叫端已表明結束）。stderr reader 遇到 closed error 時結束（不計入 `stderrTail`）。是否核准此邊界。
- **D3（`ErrCleanupIncomplete` 文字與位置）**：`var ErrCleanupIncomplete = errors.New("proc: cleanup incomplete: process group not confirmed dead within 1s budget; stdout/stderr force-closed")`，定義於 `proc.go`；assist 以 `fmt.Errorf("assist: %w", proc.ErrCleanupIncomplete)`（`ex.Err` 非 nil 時 `errors.Join(ex.Err, …)`）回傳。是否核准。
- **D4（app.go 揭露）**：B2c-4 裁定明列 `ports.Exit`／codex meta／assist；`app.go:7478-7482` 的 claude wire log（原始通訊紀錄）meta 與 `7590` 的結果 payload 也消費同一個 `ports.Exit`。建議一併寫入 `recorder.Meta.CleanupIncomplete` 與 payload `cleanupIncomplete: true`（僅在為 true 時加入，前端零變更、只多一個可選欄位）。是否納入本票（估點內可吸收）或另開票。
- **D5（跨套件揭露測試方式）**：不引入任何跨套件 hook。(a) codex：`stubServer.Wait()` 回 `proc.Exit{CleanupIncomplete: true}` → 斷言 `meta.cleanup_incomplete`；(b) claude：抽出 `toPortsExit(proc.Exit) ports.Exit` 純函式並單測；pump goroutine 抽成 `pump(r io.Reader, maxLine int, events chan<- contract.Event, onStreamErr func())`，以回傳 `ErrCleanupIncomplete` 的 fake reader 單測「發出 `KindStreamError` 且 `Raw` 含錯誤文字、之後 channel 關閉」；(c) assist：抽出 `finishRun(ex proc.Exit) error` 純函式並單測三態（`Err=nil`／`CleanupIncomplete`／兩者皆有 → `errors.Is` 兩者成立）。真實 fail-loud 機制只在 `internal/proc` 白箱測試驗（seam）。替代方案（不建議）：`//go:build proctest` 匯出 seam 供 claude／assist 真實流程測試，需在 B2a ci.yml 與 B2c-7 workflow 加 `-tags proctest`。是否核准 (a)(b)(c)。
- **D6（既有測試調整範圍）**：(i) `TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned` → `TestSupervisorFirstCleanupSignalEventFiresOnlyWhenSent`：`orphan_present` 案例斷言 `sigEventSupervisorCleanupKill` 恰 1、`sigEventSupervisorCleanupRekill` 0–10（真實程序下 Linux 可能對 zombie 重送一次，數量不確定）、其他種類 0；`no_orphan` 案例三種皆 0；(ii) `TestTerminateEscalatesViaInjectedTimerInOrder` 的事件迴圈容許 `sigEventSupervisorCleanupRekill`（與既有容許 cleanup 事件同理）；(iii) 既有以 `groupGone` 為 oracle 的測試**本票不改**（B2c-6 範圍），但 B2c-5 落地後本機 `-race` 全套仍須 PASS——若因新增 rekill 使 Linux／macOS 上 `groupGone` 更早成立，屬預期。是否核准。

---

## Global Constraints

- **範圍**：`internal/proc/proc.go`、`internal/proc/proc_test.go`（既有調整＋新白箱測試，可拆新檔 `proc_cleanup_test.go`）、`internal/ports/*.go`（`Exit` 加欄位）、`internal/claude/session.go`＋測試、`internal/codex/owner.go`＋測試、`internal/recorder/recorder.go`（`Meta` 加欄位）、`internal/assist/oneshot.go`＋測試、`app.go`（D4 通過時）。不改 `testdata/**`、`internal/proc/testdata/**`、workflow、`go.mod`。
- **不放寬任何 timeout／guard、不加 retry 於被測路徑**（register 規則 8）。
- **seam 規約**沿 B1a-1：欄位在 `p.mu` 下讀寫，回傳值解鎖後呼叫；observer 不持鎖呼叫、不參與 production 判定。
- **狀態機在 supervisor goroutine 內同步執行**；不新增 goroutine（除既有 stderr reader）；`kill(-pgid, …)` 為非阻塞 syscall；最壞情況多等 1 s 才 `close(doneCh)`。
- **`Done()` 語意**：「`Exit` 已快取」（EOF 或強制解除其一）；`Wait()` 仍為任意時點、任意次數可呼叫。
- **證據**：每個 Task 的本機控制以 `-race` 執行並保存到 `/tmp/b2c5-*`；mutation 逐條記錄「改了什麼、哪個案例紅、逐字失敗訊息」；主 agent 讀碼審查與獨立重跑（沿 review-no-delegation 規則）。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭。

---

## Task 1: proc 核心——狀態機、seam、fail-loud（Sonnet 實作；主 agent 讀碼審查）

- [ ] **Step 1 型別與 seam**：`Exit` 加 `CleanupIncomplete bool`；`ErrCleanupIncomplete`（D3）；`Proc` 加欄位 `cleanupAfter afterFunc`、`groupProbe func(int) error`、`cleanupSignal func(int, syscall.Signal) error`、`forcedClosed bool`、`callerClosedStdout bool`，與三個 nil-safe 存取子 `seamCleanupAfter`／`seamGroupProbe`／`seamCleanupSignal`（模式同 `seamAfter`）；`signalEvent` 加 `sigEventSupervisorCleanupRekill`（註解：只在 rekill 實際成功送出時發；`signalEvent` 契約不變）；`stdoutReader` 薄包裝（D2），`Start` 以它包 `outR` 指派給 `p.Stdout`。
- [ ] **Step 2 supervisor 狀態機**：把 `proc.go:191-195` 改為呼叫 `p.cleanupGroup()`（回傳 `incomplete bool`），內容依「凍結事項」；errno 分類 helper `classifyKillErr(err) (esrch, eperm, nilErr bool)` 用 `errors.Is(err, syscall.ESRCH)`／`syscall.EPERM`；事件在鎖外發；rekill 事件只在 `cleanupSignal` 回 `nil` 時發；`incomplete` 時在 `p.mu` 下設 `forcedClosed=true`，解鎖後 `errR.Close()`、`stdout.f.Close()`；之後 `wg.Wait()`（stderr reader 因 closed error 返回）；`Exit{..., CleanupIncomplete: incomplete}`。既有 `errR.Close()`（`proc.go:196`）改為冪等（忽略已關閉錯誤）。`proc.go:27-31` 契約註解改為 B2c-4 D1 措辭（有界清理、無法確認時強制解除本端 pipe 等待並揭露，不再宣稱單次 KILL 保證 EOF）。
- [ ] **Step 3 白箱測試**（新檔 `internal/proc/proc_cleanup_test.go`）：兩種真實 Proc 載體——**載體 A（無 orphan）**：`bashProc` 起 `echo ready; read -r _; exit 0`，leader 退出後 write end 自然關閉，用於 (i)(ii)(iii)(v)(vi) 與 errno subcases；**載體 B（受控 descendant 持有 pipe）**：`bash -c 'exec sleep 3600' & echo ready; read -r _; exit 0`（descendant 繼承 stdout／stderr write end、在同一 PGID、不會自行退出），用於 (iv) 與對照案例——因 `cleanupSignal` 已被 seam 取代、真實訊號不會送出，descendant 會一直持有 pipe，reader 不可能提前 EOF，`wg.Wait()` 也不可能自行完成；**測試收尾以真實 `syscall.Kill(-pgid, SIGKILL)` 清除 descendant**（`t.Cleanup`，不依賴 seam），再 `p.Wait()`。注入三個 seam 後送 stdin 讓 leader 退出，seam 以 channel 記錄每次呼叫（避免 `-race`）；六案：
  1. (i) `cleanupSignal`→`nil`、`groupProbe`→`ESRCH`：cleanup 事件 1、rekill 0、`cleanupAfter` 被呼叫 1 次（offset 1 ms）、`CleanupIncomplete=false`。
  2. (ii) `cleanupSignal`→`nil`、`groupProbe` 序列 `nil,nil,nil,ESRCH`、rekill 皆 `nil`：cleanup 1、rekill 3、`cleanupAfter` 4 次且 duration 對應 base 偏移 1／2／4／8 ms（容許 `time.Until` 的負值或略小）。
  3. (iii) `cleanupSignal`→`nil`、`groupProbe` 序列 `EPERM,EPERM,ESRCH`：cleanup 1、rekill 0、`CleanupIncomplete=false`、探測 3 次。
  4. (iv)（載體 B）`cleanupSignal`→`nil`、`groupProbe` 恆 `nil`、rekill 恆 `nil`：rekill 10；注入的 `cleanupAfter` 前 10 次立即回傳，**第 11 次（1 s 確認）先停在 barrier**：測試在 barrier 處以非阻塞 select 確認並行 stdout reader 尚未返回、`Done()` 尚未關閉，才釋放 barrier；之後最終 probe `nil` → `CleanupIncomplete=true`；`Wait()` 在釋放後 <1 s 返回（測試 guard 3 s）；`Done()` 已關；stdout reader 收到錯誤且 `errors.Is(err, ErrCleanupIncomplete)`；stderr tail 不含我方敘述；收尾以真實 KILL 清除 descendant 並確認群組消失（test-local bounded poll，只認 `ESRCH`）。**對照案例（載體 B）**：同序列但呼叫端在 barrier 釋放前自行 `p.Stdout.Close()` → reader 得到的錯誤**不**滿足 `errors.Is(ErrCleanupIncomplete)`，且 `CleanupIncomplete` 仍為 true（揭露不受呼叫端關閉影響）。
  5. (v) `cleanupSignal` 第一次→`EPERM`、`groupProbe`→`nil`、rekill→`nil`、下一次 probe→`ESRCH`：cleanup 事件 0、rekill 1、`CleanupIncomplete=false`。
  6. (vi) `cleanupSignal` 第一次→`ESRCH`：0 探測、0 事件、`CleanupIncomplete=false`。
  7. **errno table-driven subcases**（載體 A 或 B 視是否需要 fail-loud）：(a) 最終 1 s probe 回 `EPERM` → `CleanupIncomplete=true`（載體 B）；(b) 最終 probe 回其他 errno（`EINVAL`）→ `CleanupIncomplete=true`（載體 B）；(c) rekill 回 `ESRCH` → 立即停止：後續 probe 0 次、rekill 事件 0、`CleanupIncomplete=false`；(d) rekill 回 `EPERM` → 不發事件、繼續：後續 probe 至下一個偏移、rekill 事件 0；(e) rekill 回其他 errno → 同 (d)；(f) 第一次 cleanup KILL 回其他 errno（`EINVAL`）→ 不發事件、仍排程（等同 (v) 的 `EPERM` 路徑）。每個 subcase 明確斷言：後續 probe 次數、是否停止、rekill 事件數、`CleanupIncomplete`。
  另：`stdoutReader` 單測（`Read` 映射三態；`Close` 連續兩次皆回 nil；caller-first 與 supervisor-first 兩種順序的映射結果）；`classifyKillErr` 單測（`syscall.Errno` 與 wrap 皆可辨識）。
- [ ] **Step 3b 兩層 fork 真實程序測試** `TestSupervisorCleanupTwoLayerForkOrphan`（B2c-4 凍結；供 B2c-7 以名稱指定 ×400）：inline script `[ -n "x" ] && bash -c 'trap "" TERM; sleep 30' & echo out; echo err >&2; exit 5`（與 `testdata/fake-claude.sh:16` 同形的兩層 fork：子 shell 執行 AND-list 再 fork `bash -c`），不用 seam（真實時鐘、真實訊號）；斷言 `Wait()` 於 5 s 內返回（既有 guard 形狀）、`Exit.Code=5`、`CleanupIncomplete=false`、群組於 2 s 內消失（test-local bounded poll，只認 `ESRCH`；本票不改 `groupGone`）、stdout 含 `out`；本機預期穩定 PASS（重現率接近 0），CI 上的效果由 B2c-7 驗。
- [ ] **Step 4 既有測試調整**（D6）：改名與逐種類計數；escalation 測試容許 rekill；`killAndReap` 不變。
- [ ] **Step 5 本機控制**：`go vet ./internal/proc`、`gofmt -l`、`go test -race ./internal/proc -count=3`；mutation（各改一處、跑對應案例、還原）：移除 rekill → (ii) 紅；`EPERM` 當終止 → (iii) 紅（探測次數 <3 或 `CleanupIncomplete`）；移除強制關閉 → (iv) 紅（載體 B 的 descendant 持有 pipe，`Wait()` 必逾時，以測試內 3 s guard 判；確定性）；第一次 KILL 非 `nil` 就不排程 → (v) 紅；rekill 失敗也發事件 → 新增案例 (ii') `rekill→EPERM` 時 rekill 事件 0 紅。逐字記錄。
- [ ] **Step 6 主 agent 讀碼審查**：seam 鎖規約；事件只在成功送出時發且鎖外；狀態機路徑逐 errno 對照 B2c-4 §3；強制關閉順序（設旗標 → 解鎖 → close）；`Exit.Err` 未被觸碰；`Terminate`／escalation 未改；`Done()` 註解更新。

## Task 2: 揭露傳遞——ports／claude／codex／assist／app（Sonnet 實作）

- [ ] **Step 1 ports**：`ports.Exit` 加 `CleanupIncomplete bool`（註解引用 B2c-4）；claude `session.go` 抽 `toPortsExit(ex proc.Exit) ports.Exit` 並在 `Wait()` 使用；pump 抽成套件內函式（D5(b)），行為不變（`KindStreamError` 後 `Terminate`，`Terminate` 於已退出時為 no-op）。
- [ ] **Step 2 codex**：`recorder.Meta` 加 `CleanupIncomplete bool \`json:"cleanup_incomplete,omitempty"\``；`owner.go` `FinalizeWith` 在 `ex := o.Server.Wait()` 後 `meta.CleanupIncomplete = ex.CleanupIncomplete`；`FinalizeCause` 不動（cleanup 狀態不是收尾原因）。
- [ ] **Step 3 assist**：`oneshot.go` 抽 `finishRun(ex proc.Exit) error`（D3／D5(c)），EOF 路徑改用它；`ctx.Done()` 路徑改為：`p.Wait()` 後若 `CleanupIncomplete` 則回 `errors.Join(ctx.Err(), proc.ErrCleanupIncomplete)`（呼叫端以 `errors.Is` 兩者皆可判），否則維持回 `ctx.Err()`。
- [ ] **Step 4 app.go（D4 通過時）**：claude wire log（原始通訊紀錄）meta 加 `CleanupIncomplete: ex.CleanupIncomplete`；結果 payload 於 `ex.CleanupIncomplete` 時加 `cleanupIncomplete: true`。
- [ ] **Step 5 測試**：claude `toPortsExit` 三態、pump fake reader 案例；codex `FinalizeWith` 以 `stubServer` 回 `CleanupIncomplete=true` → meta 欄位為 true、`FinalizeCause` 不含 cleanup 字樣；assist `finishRun` 三態；app.go 若無既有單測覆蓋該 closure，記錄為「讀碼確認」（不新增 app 層測試）。
- [ ] **Step 6 本機控制**：`go build ./...`、`go vet ./...`、`go test -race ./internal/... .`（含 root 套件）全 PASS；`gofmt -l` 空；前端不變（`git diff --stat -- frontend` 為空）。

## Task 3: 整合驗證與交付

- [ ] 全套 `go test -race ./... -count=1`（本機 8 核）PASS；`TestOrphanDoesNotHangNormalExit` focused `-count=30` PASS（本機不重現形狀，只作回歸）；既有 orphan 案例（`TestNormalExitReapsOrphanAndCachesExit`、`TestCtxCancelKillsWholeGroup`、`TestTerminateEscalatesToGroupKill`）與新增 **`TestSupervisorCleanupTwoLayerForkOrphan`** `-count=20` PASS；B2c-7 的核心測試清單：`TestOrphanDoesNotHangNormalExit`、`TestSupervisorCleanupTwoLayerForkOrphan`；escalation 清單：`TestCtxCancelKillsWholeGroup`、`TestTerminateEscalatesToGroupKill`。
- [ ] 主 agent 獨立重跑 Task 1 Step 5 的六案與至少兩條 mutation。
- [ ] scope 三點 diff 只含 Global Constraints 範圍內檔案；commit 拆為：proc 核心（含測試）、揭露傳遞（含測試）、plan；申請 owner 授權 fast-forward push 到 `main`（docs＋code，一次）。
- [ ] 落地後回寫：backlog rev21（B2c-5 關票、B2c-6 依賴成立）；register #7 維持 unresolved（v7 留給 B2c-7）；本 plan 狀態更新（同一次推送或併入 B2c-6 的推送，由 owner 裁定）。

---

## 驗證策略

- 單元／白箱：Task 1 六案＋對照案例＋兩個 helper 單測；mutation 五條。
- 整合：全套 `-race`；focused 回歸；主 agent 獨立重跑。
- 無法在本機驗證：真實 fork 窗口下的收斂效果（本機 production 順序重現率接近 0）——交 **B2c-7** 於 CI ×400 驗證；escalation 路徑窗口（B2c-4 假設 A6）同上。
- 剩餘風險：強制關閉 stdout 造成呼叫端看到 `KindStreamError`／錯誤回傳，是刻意的使用者可見行為變更，僅在失敗形狀發生；`EPERM` 連續到 1 s 的群組會被判 `CleanupIncomplete`（B2c-4 假設 A5，由 B2c-7 觀察）。

## 估點核對

Task 1 約 3.0 hr（狀態機 1.0、seam 與薄包裝 0.7、六案與 mutation 1.3）、Task 2 約 1.5 hr、Task 3 約 1.0 hr、審查與回寫 0.5 hr → 約 6.0 hr，在 0.6 pt 中位。

## Gate A（B2c-5 完成條件）

- [ ] Task 1 六案＋對照＋errno table subcases (a)–(f)＋helper 單測 PASS；(iv) 的 barrier 確認 reader 未提前返回；五條 mutation 各自紅並逐字記錄；`TestSupervisorCleanupTwoLayerForkOrphan` 存在且 `-count=20` PASS；讀碼審查六項通過。
- [ ] Task 2 四個揭露點各有測試或讀碼確認；`FinalizeCause`／`Exit.Err` 語意未變。
- [ ] 全套 `-race` PASS；scope 只含範圍內檔案；`Terminate`／escalation／`testdata/**`／guard 零變更。
- [ ] owner 授權推送並落地；backlog rev21 回寫。

## 修訂記錄

- rev2（2026-09-07）：design gate 第一輪修正——(1) (iv)／對照案例改用載體 B（受控 descendant 持有 stdout／stderr write end）、第 11 次注入 timer 先停在 barrier 並確認 reader 未返回、收尾以真實 KILL 清除；(2) 新增 Step 3b `TestSupervisorCleanupTwoLayerForkOrphan`（inline 兩層 fork fixture），Task 3／Gate A 列入 B2c-7 核心清單，授權邊界改為「`testdata/**` 不變、proc 測試可加 inline fixture」；(3) `stdoutReader.Close()` 先設旗標再關底層、只正規化 `os.ErrClosed`、helper 測試涵蓋連續兩次與兩種順序；(4) errno table-driven subcases (a)–(f)。D1／D3／D4／D5 通過，D2 方向通過（實作說明已修），D6 通過。
- rev1（2026-09-07）：建立；凍結事項承 B2c-4 rev3；D1–D6；三 Task；驗證策略；估點核對 6.0 hr。
