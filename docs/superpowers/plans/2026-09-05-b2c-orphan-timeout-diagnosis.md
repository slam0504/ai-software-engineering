# B2c `TestOrphanDoesNotHangNormalExit` CI-only 逾時診斷 Implementation Plan（diagnosis-only）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev12（2026-09-06，收尾措辭統一：已關票、rev11＝`4688afc`、current main＝`5fc3373`、程式碼／分支基準 `81af8f2`；前版：rev11（register v4／backlog rev17 docs-only 已推送 main `5fc3373`；Task 3 對應項勾選；rev10 已提交 `d5b0967`；前版：rev10（rev9 短複審 CHANGES_REQUIRED 後修訂：Task 3 結論改為「自製 proc 探針路徑 0/400 未重現；對真正 `claude.Session`＋`fake-claude.sh` 路徑尚未排除任何候選機制」；macOS「存活到 `sleep 30`」明標為假設；D5 裁定拆 **B2c-2**（0.3 pt，另開 plan，核准 build-tag 隔離的 `internal/claude` 暫時性診斷檔）；D6 register #7 措辭收斂；已完成 Gate 項勾選、round 2 另設 Gate；「rev9 待 commit」改已提交 `cbd7d3b`；前版：rev9（Task 2 Step 2 一次推送與四 job 結果回填：layer 1 高重現（macOS 36／45 次 5 秒 guard；ubuntu 98／99 次紅在另一條斷言 `orphan must be reaped`）、layer 2 四 job 400 輪全部未重現；探針未捕捉到真實測試的機制，提出 round 2 設計待裁定；前版：rev8（第三輪 owner 對探針的 P1 修正後回填：`diagWaitConverge` 觀察後設 nil channel＋單一絕對 deadline timer 並加 Done-before-EOF helper control；背景彙報同時等 EOF 與 Done、稍後 scanner error 計入；正常與逾時路徑共用 `finalizeIter`；stdin close 與父 PID 檔缺失計入 `invalidEvidence`（child PID 為 optional）；控制與 100 輪基準重做；前版：rev7（第二輪 owner 對探針的 P1 修正後回填：有界 exitedCh／guard 2 單一絕對 deadline 迴圈、`onSignal` 於 `p.mu` 內寫入、兩階段身分（父 token＋`DIAG_CHILDPIDFILE`）、rescue 只對已驗明目標、證據完整性計數以非零結束；新增 hang／escape／force-eof 強制控制與 RO 目錄控制證據；重做 100 輪基準；契約未動；前版：rev6（design gate APPROVED 後執行 Task 1（本機）與 Task 2 Step 1 並回填證據；契約未動；前版：rev5（rev4 短複審 CHANGES_REQUIRED 後修訂：P1 rescue 不再無條件對舊 PGID 發 SIGKILL——rescue 前以當下 token／PID 快照確認目標仍屬 `p.PGID()`，無佐證記 `rescue=skipped-no-target`，(b2) 只對已驗明 PID 做窗口後清理，`ps@5s` anomaly 路徑同；P1 新 fixture 明定 mode 100755，推送前 `test -x`；P2 JSONL 改為 iteration record 於彙報完成後每輪只序列化一次，`finalConverged|pending` 於彙報階段填入；前版：rev4（rev3 短複審 CHANGES_REQUIRED 後修訂：P1 有界收斂——guard 1 以 `tExited` 為絕對 deadline、快照與 EOF 等待並行、所有 `Done()` 等待有界、逾時即 rescue 再進 guard 2、背景 `p.Wait()` 彙報設期限逾時記 pending；P1 Goal／Gate 改為只可直接歸因 (b1)／(b2)／(c)，(a′) 只是代理證據、命中時維持未定位並要求下一票取得獨立退出時點；P2 刪除「Start 之前設定 onSignal」矛盾句；P2 已知缺口改寫為「不同 fixture、相同 orphan 語意」；前版：rev3（rev2 短複審 CHANGES_REQUIRED 後修訂：P1 EOF 逾時後改為「立即 rescue → 第二段有界 guard 等 EOF／Done → 最後 `p.Wait()`，仍未收斂即記錄並停止該 iteration」（`doneCh` 要等 stderr EOF，同一孫程序可同時卡住 EOF 與 Done）；P1 孫程序身分改以暫時性 fixture 寫出 `$!` PID 與每輪唯一 token，否則「不在群組」只能記未定位；Gate A 歸因邊界：不在預期 PGID 只支持 fixture／PGID 契約問題，只有「cleanup KILL 已成功送往正確群組但程序仍未及時消失」才支持 production 收尾缺陷；P2 分支基準改 `81af8f2`；P2 `tExited − tLastLine` 改稱「最後輸出至 supervisor 記錄退出的延遲」；前版：rev2（第一輪 owner CHANGES_REQUIRED 後修訂：探針改採 **D1(c′) 不合併的白箱診斷測試檔**（公開 API 量不到 supervisor 收尾窗口）；時序點改為 `tExited`／`tCleanupKill`／`tEOF`／`tDone`，`p.Wait()` 只作最終收斂；單一 stdout reader；第二次 SIGKILL 只作窗口後 rescue 並分開記錄；cleanup event 缺席時只能標未定位；workflow 改為 `push` 限定 `b2c/diag`＋matrix runner × replica 四個獨立 job、`fail-fast: false`、檔案齊備後一次推送；新增殘留程序前後快照與受限清理；D2 ubuntu 對照、D3 每 runner 兩 replica 每層 100 次、D4 維持 0.4 pt 皆已裁定；前版：rev1）
> 狀態：**Task 2 Step 2 已完成（owner 授權推送 `d12b13b`，run `33968040444` 四 job 結束）；Task 2 Step 3 解析完成；Task 3 結論已依 owner 裁定收斂；round 2 拆為 B2c-2（另開 plan），本 plan 於 register #7 回寫與 backlog 落地後關票**。未 rerun、未 dispatch、GitHub 設定面零變更
> 票源：Pre-M4 Readiness Backlog **B2c**（rev16 新增，**0.4 pt**，owner 於 plan gate 維持）。承接 `wall-clock-test-register.md` v3 Go 表 **#7** 候選
> 基準 commit：**程式碼／診斷分支基準 `81af8f287ee19d59e916f84f7b416b17eb21260d`**（`b2c/diag` 的 merge-base；`internal/proc`、`internal/claude`、`testdata/fake-claude.sh` 自 `05069e2` 後未變）；**current main `5fc3373`**（register v4／backlog rev17）
> 上游阻擋關係：B2a Gate A 因本票標的停止；本票結論經 owner 裁定後才解除 B2a 的合併阻擋。**B2a 分支的 `bd6a102` 依裁定留在本機**（推進 PR head 會觸發新的 pull_request run，等同重跑已停止的 Gate A），待本票結論後與正式恢復 run 一起推送

**Goal:** 對 `internal/claude/TestOrphanDoesNotHangNormalExit` 在 `macos-15-intel` 上（現行 HEAD `ffcd161` 2/2 紅、`109b407` 一次綠；本機 0.02s 級）的逾時，以 CI artifact 與可重現指令取證。**可直接歸因的機制只有 (b1)／(b2)／(c)**；(a) 只能取得代理證據 (a′)，**不能被本票確認或排除**——(a′) 命中時維持「未定位」並要求下一票取得獨立的 leader 退出時點。三種候選機制：
- **(a)** `cmd.Wait()` 延遲返回（leader 已退出但 supervisor 未及時得知）；
- **(b)** supervisor 於 `cmd.Wait()` 返回後立即發出的 group SIGKILL（`internal/proc/proc.go:191`）**未及時使持有 stdout 的孫程序退出**（kill 回錯、孫程序不在該 pgid、或 kill 成功但程序未即時消失）；
- **(c)** 孫程序已死但 stdout **EOF 傳遞延遲**（pipe／scanner 層）。

**Architecture:** 本票**不修改既有 production／測試檔、不放寬 5 秒 guard、不加 retry**。兩層取證都在**不合併的診斷分支 `b2c/diag`**（自 `origin/main`）：(1) **測試層高重複觀測**——既有測試 `-count=100 -race -v -json`，量化重現率與耗時分布；(2) **白箱診斷測試檔 `internal/proc/orphan_diag_test.go`**（暫時性、結案刪除），以既有未匯出 seam 直接對 supervisor 收尾窗口打點：`p.exited`（`cmd.Wait()` 完成的事實）、`p.onSignal` 收到 `sigEventSupervisorCleanupKill`（清孫程序的 KILL 送出成功）、單一 stdout reader 讀到 EOF、`p.Done()`（Exit 快取完成）。**`p.Wait()` 只作最終收斂，不作時序探針**（它本身先等 `doneCh`，量不到窗口）。診斷 workflow 以 **`push` 限定 `b2c/diag`** 觸發（`workflow_dispatch` 對非 default branch 不會觸發），matrix 展開 runner × replica 四個獨立 job，所有輸出以 artifact 保存，結論回寫 register #7。

**Tech Stack:** GitHub Actions（`push: branches: [b2c/diag]`、`strategy.matrix`、`fail-fast: false`）、`macos-15-intel`＋`ubuntu-latest`（D2）、Go 1.26.5（`-race`）、`ps -eo pgid,pid,ppid,stat,etime,command`、`python3` 解析 `-json`／`.jsonl`。

**參考文件：** backlog rev16 `### B2c`／`### B2`；register v3 #7；B2a plan（Gate A 停止證據段、run 33953144191 artifact）；`internal/proc/proc.go`（`exited` 欄位 `:50`、`onSignal` seam `:66`、`signalEvent` `:75-85`、supervisor `:185-200`）、`internal/proc/proc_test.go:519／591／626`（既有白箱測試注入 `p.onSignal` 的寫法）、`internal/claude/session_test.go:200-212`、`testdata/fake-claude.sh:16`。

---

## 已知事實（2026-09-05，唯讀）

- **失敗形狀**：`select { case <-doneCh: case <-time.After(5 * time.Second): t.Fatal("drain/Wait hung on orphan-held pipes") }`；`doneCh` 由 `drain(s); s.Wait(); close(doneCh)` 關閉；`drain` 消費 `s.Events()` 直到關閉，`Events()` 於 stdout scanner EOF 後關閉。紅燈＝**leader 正常退出後 5 秒內 stdout 沒有 EOF**。
- **收尾機制（`proc.go:185-200`）**：supervisor goroutine `werr := cmd.Wait()` → `p.exited = true`（鎖內）→ `close(p.exitedCh)` → `cleanupErr := p.SignalGroup(syscall.SIGKILL)` → **只在 `cleanupErr == nil` 時** `seamOnSignal()(sigEventSupervisorCleanupKill)`（失敗**不記錄**）→ `wg.Wait()`（stderr EOF）→ 快取 Exit → `close(doneCh)`。stdout／stderr 皆 `os.Pipe` write end（`:152`），無 copy goroutine。
- **既有 seam**：`p.onSignal signalObserverFunc`（`:66`，nil 時 no-op）、`p.exited bool`（`:50`，`p.mu` 保護）、`p.Done()`（Exit 已快取）；`proc_test.go:519` 等既有白箱測試以 `p.onSignal = func(ev signalEvent) { events <- ev }` 注入。
- **fixture**：`fake-claude.sh:16` `bash -c 'trap "" TERM; sleep 30' &`（繼承 stdout／stderr、忽略 TERM、30 秒後自結束）；leader 印 init／delta／result 後 `exit 0`。
- **CI 證據**：run `33953144191` attempt 1／2 皆 5.01s 紅（同套件其餘 34 條 0–0.06s；無 `-timeout` panic、無 DATA RACE）；三次 `go` job 同 image macos-15 `20260824.0482.1`；artifact `go-test-json` id 9965561717／9965826248 已本機保存（hash `46d5466e…`／`1c0ff62e…`）。
- **本機反證**：focused `-race -count=30` 全過；三份 `./internal/...` `-race` 併發下 focused ×20 最慢 0.02s。
- **公開 API 為何量不到**（rev1 缺陷，owner 指出）：`p.Wait()` 先等 `doneCh`，返回時 supervisor 早已做完 KILL 與 stderr EOF；`<-p.Done()` 不產生獨立時間點；在 `p.Wait()` 後才 `ps`／KILL 已錯過窗口。故必須用套件內 seam。

---

## owner 裁定紀錄（design gate 第一輪）

- **D1**：採 **(c′)**——在 `b2c/diag` 新增暫時性 `internal/proc/orphan_diag_test.go`（白箱，使用既有 `p.onSignal`／`p.exited`／`p.Done()`），**分支不得合併、結案後刪除**；不修改既有 production／測試檔。backlog rev16 票面已同步（`81af8f2`）。
- **D2**：採 `ubuntu-latest` 對照。
- **D3**：每個 runner **兩份獨立 replica**、每層 **100 次**。
- **D4**：估點維持 **0.4 pt**。
- **workflow 觸發**：`push` 限定 `b2c/diag`；`strategy.matrix: runner × replica` 四個獨立 job；`fail-fast: false`；所有檔案準備完成後**一次推送**，避免不完整 workflow 先跑。
- **殘留程序**：每輪保存前後快照，先被動取證；最後只清理**本輪建立且 PGID 已驗明**的程序，並保存清理前後證據。

---

## Global Constraints

- **既有檔案零變更**：不改 `internal/**` 既有檔、`testdata/**`、`.github/workflows/ci.yml`、`go.mod`。診斷分支只新增四個檔：`internal/proc/orphan_diag_test.go`、`internal/proc/testdata/diag-orphan.sh`（暫時性 fixture，孫程序語意與 `testdata/fake-claude.sh:16` 相同；另寫出孫程序 bash 的 PID（`DIAG_PIDFILE`）、其 `sleep` 子程序的確切 PID（`DIAG_CHILDPIDFILE`，由孫程序以 `sleep 30 & echo $! > …; wait` 寫出）與每輪 token；`DIAG_MODE=hang|escape` 只供本機強制控制，CI 不設）、`.github/workflows/diag-orphan.yml`、本 plan（rev 更新）。分支結案時刪除遠端分支；`git diff --name-only origin/main..b2c/diag` 只含這四個檔。
- **GitHub 外部寫入逐步授權**：建立／推送 `b2c/diag`、任何後續 push、刪分支，皆先取得 owner 授權；GitHub 設定面零變更；**不 dispatch、不 rerun**。
- **wrapper 契約**（沿 B2a）：`set +e` → pipeline `| tee` → `rc=${PIPESTATUS[0]}` → `set -e` → 寫 `.rc` → `exit "$rc"`；`upload-artifact` `if: always()`，artifact 名含 `${{ matrix.runner }}-r${{ matrix.replica }}`。
- **所有等待皆有界**（owner 第二輪 P1）：`exitedCh` 等待 `DIAG_EXITED_TIMEOUT`（預設 10s），逾時記 `exitedTimeout`＝(a′) 代理命中、取快照、身分驗證後 cleanup、有界等收斂；guard 2 為**單一絕對 deadline 迴圈**同時追蹤 EOF 與 `Done()`（每次以剩餘時間建新 timer，不共用已耗盡的 `time.After`）；背景 `p.Wait()` 彙報整體 ≤30s。
- **`onSignal` 寫入契約**：依既有白箱測試（`proc_test.go:519`）於 `p.mu.Lock()` 內賦值，production 以 `seamOnSignal()` 鎖內讀取。
- **兩階段身分**：先以 `DIAG_PIDFILE` 的 PID＋命令列含 token 驗明父 bash；再接受**同一份快照**中 `PID == DIAG_CHILDPIDFILE` 且 `ppid == 父 PID` 的 `sleep`。rescue 目標清單只含已驗明者；`exitedTimeout` 路徑另以 `pid == p.PGID()` 且命令列含 `diag-orphan.sh` 驗明 leader。
- **證據完整性**：Start／stdin write／**stdin close**／scanner error（含彙報階段才出現的）／每個 `ps` 寫檔／partial／JSONL encode／close／summary／**父 PID 檔缺失或格式錯誤**任一都累積 `invalidEvidence`，保留可用 artifact 後以 `t.Fatalf` 非零結束（layer 2 rc≠0 ＝ 證據不完整，不是重現）。child PID 檔為 optional（normal 模式下孫程序常在寫出前被清掉）。正常與 `exitedTimeout` 路徑共用 `finalizeIter`（scanner error、逐輪 after 快照、partial）。
- **收斂等待實作契約**：`diagWaitConverge` 使用**單一絕對 deadline timer**，已觀察到的 channel 設為 nil（已關閉的 channel 永遠 ready，不設 nil 會空轉）；helper control `TestDiagWaitConvergeDoneBeforeEOF`（Done 立即關閉、EOF 永不到達）須準時回傳 `(eof=false, done=true)` 且 select 次數 ≤2。背景彙報同以此函式等 **EOF 與 Done 兩者**，30s 絕對期限，兩者皆到才 `finalConverged`，否則 `pending` 並記錄哪一個未到。
- **時序點權威定義**（診斷測試內，全部 `time.Now()` 單調時鐘）：
  - `t0`：`Start` 返回。
  - `tLastLine`：**單一 stdout reader** 讀到 result 行（leader 即將 `exit 0`）。
  - `tExited`：**輪詢 `p.exited`**（鎖內讀）首次為 true 的時刻——即 supervisor 已記錄 `cmd.Wait()` 完成（等價地可用 `<-p.exitedCh`；`exitedCh` 在 `p.exited = true` 之後立即 close，兩者皆可，plan 採 `exitedCh` 免輪詢）。
  - `tCleanupKill`：`p.onSignal` 收到 `sigEventSupervisorCleanupKill` 的時刻（**只有 `SignalGroup(SIGKILL)` 成功才會有**）。
  - `tEOF`：同一個 stdout reader 讀到 `io.EOF`。
  - `tDone`：`<-p.Done()`。
  - `p.Wait()` 在 `tDone` 之後呼叫，只作最終收斂與取 Exit，**不記為時序點**。
- **觀察窗口內不得干預**：`[tExited, tEOF]` 期間**不得**由測試再送任何訊號；`ps` 快照為唯讀。**第二次 `SignalGroup(SIGKILL)` 只允許在窗口結束後**（`tEOF` 到達，或 guard 逾時 10s）作 rescue cleanup，並**分開記錄**其時刻與回傳錯誤，不併入歸因。
- **cleanup event 缺席的解讀**：若 `tCleanupKill` 未出現，只能記「supervisor 的 KILL 未成功送出或事件未觀察到」；**不得**宣稱已知道原始 `cleanupErr` 值（production 不記錄失敗），除非另有**以本輪 token／PID 佐證**的直接證據（例如 `ps` 顯示本輪孫程序 pgid ≠ `p.PGID()`，即 (b2)）。
- **stdout 只能有一個 reader**：診斷測試自行 `bufio.Scanner` 讀 `p.Stdout`，同一 goroutine 記 `tLastLine` 與 `tEOF`；不並行開第二個 reader。
- **不得單獨重跑吸收**：診斷 job 的紅燈是資料；不重跑。任何一輪「未重現」只寫「本輪未重現」，register #7 狀態只由 owner 依結論更改。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭；`gh` 輸出逐字抄錄。

---

## Task 1: 白箱診斷測試檔（本機撰寫與基準）

- [x] **Step 0**：本機建分支 `b2c/diag`（自 `origin/main` **`81af8f2`**），**不推送**。
- [x] **Step 1 `internal/proc/orphan_diag_test.go`**（`package proc`；檔頭註解明寫「B2c 暫時性診斷、不得合併、結案刪除」；以 build tag `//go:build diag_orphan` 隔離，避免被一般 `go test ./...` 撿到）。測試函式 `TestDiagOrphanTimeline`，迭代 `N`（環境變數 `DIAG_ITER`，預設 100），每次迭代：
  1. **暫時性 fixture `internal/proc/testdata/diag-orphan.sh`**（新增檔，**mode 100755**，與 `testdata/fake-claude.sh` 相同——它會被當作 `Config.Binary` 直接執行；不改 `testdata/fake-claude.sh`）：讀一行 prompt；`bash -c "trap '' TERM; : DIAG_TOKEN=$DIAG_TOKEN; sleep 30" &`（孫程序語意與 fake-claude 相同，但命令列內含本輪 token）；`echo $! > "$DIAG_PIDFILE"`（父 shell 寫出孫程序 `bash` 的 PID）；印 init／delta／result 三行；`exit 0`。診斷測試每輪產生唯一 `DIAG_TOKEN`（`iter-<n>-<random>`）與 `DIAG_PIDFILE` 路徑，經 `Env` 傳入：`p, err := Start(ctx, Config{Binary: <diag-orphan.sh 絕對路徑>, Env: []string{"DIAG_TOKEN=" + token, "DIAG_PIDFILE=" + pidfile}, TermGrace: 200 * time.Millisecond})`（grace 與 `internal/claude` 測試相同）；supervisor 的 cleanup KILL 發生在 leader 退出後（leader 至少要先讀 prompt），因此 `Start` 返回後、送 prompt 前**立即**設 `p.onSignal = func(ev signalEvent){ if ev == sigEventSupervisorCleanupKill { select { case killCh <- time.Now(): default: } } }`，再寫 prompt 到 `p.Stdin` 並關閉——順序保證事件不會漏（leader 在收到 prompt 前不會退出）。
  2. 單一 goroutine：`sc := bufio.NewScanner(p.Stdout)`；逐行記錄，讀到含 `"type":"result"` 的行記 `tLastLine`；`Scan()` 回 false 時記 `tEOF`。
  3. 主 goroutine：`<-p.exitedCh` → `tExited`，**當下建立絕對 deadline `D1 = tExited + 10s`**。同時啟動一個**獨立快照 goroutine**，在 `tExited`＋0／50ms／200ms／1s／5s 各取一次 `ps -eo pgid,pid,ppid,stat,etime,command`（獨立 `exec.Command`，全文存 `iter-<n>-ps-<offset>.txt`），**與 EOF 等待並行、不阻塞主流程**；摘要以兩種身分統計：(i) `pgid == p.PGID()` 的成員數；(ii) **本輪孫程序**＝`DIAG_PIDFILE` 內的 PID（命令列含本輪 token）與 `ppid == 該 PID` 的 `sleep`——存在與否、`pgid` 是否等於 `p.PGID()`、是否 zombie。沒有 token／PID 佐證的 `bash`／`sleep 30` 不得計為本輪孫程序；原始 `ps` 全文保留。
  4. **有界收斂（所有等待都有 deadline）**：
     - **guard 1**：`select { case <-eofCh: tEOF; case <-time.After(until(D1)): eofTimeout=true }`。
     - **EOF 已到**：接著 `select { case <-p.Done(): tDone; case <-time.After(5s): doneTimeout=true }`（production 的 `doneCh` 要等 stderr EOF 才關閉，孫程序持有 stderr 時 stdout EOF 到了 `Done()` 仍可能不到，故此等待**必須有界**）。
     - **任一逾時（`eofTimeout` 或 `doneTimeout`）→ 立即進入 rescue 判定**：先取一次**當下** `ps`（含 token／PID 身分摘要），只有在**本輪孫程序（`DIAG_PIDFILE` 的 PID，命令列含本輪 token）或其 `sleep` 子程序仍存在、且其 `pgid == p.PGID()`** 時，才 `tRescue = now()`、`rescueErr := p.SignalGroup(syscall.SIGKILL)`（`doneTimeout` 時 stdout 已 EOF，原群組可能已消失並被重用，**不得**無佐證對舊 PGID 發訊號）；若本輪孫程序存在但 `pgid != p.PGID()`（即 (b2)），**只對已驗明的 PID 及其 `sleep` 子程序個別 `kill -KILL <pid>`**，記 `rescue=targeted-pid`，不殺舊群組；若快照中找不到本輪孫程序，記 `rescue=skipped-no-target`，不送任何訊號。rescue（若執行）後再取一次含身分摘要的 `ps`；然後 **guard 2（5s）**：並行等 `eofCh`（若尚未到）與 `p.Done()`；收斂者記 `tEOF`／`tDone` 並標 `afterRescue=true`；仍未收斂記 `converged=false`、**停止該 iteration**（不呼叫 `p.Wait()`），把 `p` 交給背景 goroutine 執行 `p.Wait()` 並記錄完成時刻。
     - 快照 goroutine 由 `D1`／rescue 之後統一 `wait`，不因主流程提前結束而成為孤兒 goroutine。
  5. 收斂的 iteration 最後 `ex := p.Wait()`（此時 `Done()` 已觀察到，`Wait()` 立即返回；只作取 Exit）。**測試結束前的背景彙報**：對所有 `converged=false` 的 iteration，等待其背景 `p.Wait()` **至多 30 秒（整體）**；期限內完成者記 `finalConverged=true` 與時刻，未完成者記 `pending`，**不得為了彙報而阻塞測試結束**。若 `ps@5s` 仍見本輪孫程序但 `tEOF` 已到，標 `anomaly=true` 並走**同一套 rescue 判定**（先當下快照確認身分與 `pgid`，符合才對群組或已驗明 PID 發訊號，否則 `skipped-no-target`）。
  6. 每次迭代先在記憶體保留一筆 record（欄位如下）；**待所有 iteration 結束、30 秒彙報階段完成後，每輪只序列化一次**寫入 `iter-records.jsonl`（此時 `finalConverged|pending` 才確定）。另於每輪結束時立即寫一份不含 final 欄位的 `iter-<n>-partial.json`（保底：測試若被外力中止仍有逐輪資料）。record 欄位：`{iter, runner, token, orphanPid, lastLineToExited, exitedToCleanupKill|null, exitedToEOF|null, exitedToDone|null, eofTimeout, doneTimeout, afterRescue, converged, finalConverged|pending（彙報後填入）, psAt0/50ms/200ms/1s/5s:{groupMembers, orphanPresent, orphanPgidMatches, orphanZombie, sleepChildPresent}, rescue:{mode: group|targeted-pid|skipped-no-target, err, at, psBefore, psAfter}}`。
  7. 每次迭代**前後**各存一次全域 `ps` 快照（`iter-<n>-before.txt`／`-after.txt`）；迭代後被動比對新增 PID（不清理）。
  - **歸因規則**（寫死於檔頭註解與本 plan）：
    - `lastLineToExited`（**最後輸出至 supervisor 記錄退出的延遲**——`tLastLine` 只是最後輸出時間，不是 leader 實際退出時間；本票沒有獨立的 leader 退出時點，此指標是 (a) 的**代理**，不精確稱為 `cmd.Wait` 延遲）≫ 本機基準（<10ms）且 `exitedToEOF` 正常 → **(a′) 退出記錄延遲（代理證據）**——**不能區分**「leader 最後輸出後尚未退出」與「`cmd.Wait()` 回傳延遲」，因此**不確認也不排除 (a)**；命中時該 iteration 記「未定位（a′ 代理命中）」，並於結論要求下一票取得獨立的 leader 退出時點。
    - `tCleanupKill` 存在、**本輪孫程序（token／PID 佐證）`orphanPgidMatches=true`**、且 `exitedToEOF` ≥ 1s、`ps@200ms`／`ps@1s` 仍見該孫程序或其 `sleep` 子程序為非 zombie → **(b1) cleanup KILL 已成功送往正確群組但程序未及時消失**——**唯一支持 production 收尾缺陷的樣本型**。
    - 本輪孫程序存在且 **`orphanPgidMatches=false`**（token／PID 佐證）→ **(b2) 孫程序不在預期 PGID**——支持的是 **fixture／PGID 契約問題**，**不得**據此宣稱 production defect。
    - `tCleanupKill` **缺席**且無法以 token／PID 證明孫程序去向 → **未定位**（不得推斷 errno，不得歸 (b1)／(b2)）。
    - `ps@50ms` 起本輪孫程序與其 `sleep` 子程序皆已消失（token／PID 佐證）、但 `exitedToEOF` ≥ 1s → **(c) EOF 延遲**。
    - 其餘（例如 `exitedToEOF` 在 0.1–1s）記「輕度延遲、未達歸因門檻」並保留原始資料。`converged=false` 的 iteration 另列「未收斂」。
- [x] **Step 2 本機基準**：`go test -tags diag_orphan -race -run '^TestDiagOrphanTimeline$' ./internal/proc -count=1 -v` 以 `DIAG_ITER=100` 跑一次；預期 `exitedToEOF` <10ms、`tCleanupKill` 每次出現、`orphanPgidMatches=true`、rescue 從未觸發；記錄分布作對照。**不得為了讓數字好看調整探針。**
- [x] **Step 3 殘留檢查（本機）**：迭代結束後彙整所有 `before`／`after` 差集；只清理「本輪由診斷測試建立（pgid ∈ 本輪記錄的 `p.PGID()` 集合）且仍存活」的程序，清理前後各存快照；其他新增 PID 只記錄。

## Task 2: 診斷 workflow 與測試層高重複

- [x] **Step 1 `.github/workflows/diag-orphan.yml`**：

  ```yaml
  name: diag-orphan
  on:
    push:
      branches: [b2c/diag]
  concurrency:
    group: diag-orphan-${{ github.ref }}
    cancel-in-progress: false
  jobs:
    diag:
      name: diag-${{ matrix.runner }}-r${{ matrix.replica }}
      strategy:
        fail-fast: false
        matrix:
          runner: [macos-15-intel, ubuntu-latest]
          replica: [1, 2]
      runs-on: ${{ matrix.runner }}
      timeout-minutes: 60
      steps:
        - uses: actions/checkout@<SHA>            # 與 ci.yml 同一 pin（v7.0.1）
        - uses: actions/setup-go@<SHA>            # v7.0.0，go-version 1.26.5
          with: { go-version: '1.26.5', check-latest: false }
        - name: env
          shell: bash
          run: |
            (uname -a; sw_vers 2>/dev/null || cat /etc/os-release; sysctl -n hw.ncpu 2>/dev/null || nproc; go version) | tee env.txt
            ps -eo pgid,pid,ppid,stat,etime,command > ps-job-start.txt
        - name: layer 1 — existing test x100
          shell: bash
          run: |
            set +e
            go test -race -run '^TestOrphanDoesNotHangNormalExit$' ./internal/claude -count=100 -timeout 30m -v -json 2>&1 | tee layer1-test.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > layer1-test.rc
            exit "$rc"
        - name: layer 2 — whitebox diag x100
          if: always()
          shell: bash
          env: { DIAG_ITER: '100', DIAG_OUT: '${{ github.workspace }}/diag-out' }
          run: |
            set +e
            mkdir -p "$DIAG_OUT"
            go test -tags diag_orphan -race -run '^TestDiagOrphanTimeline$' ./internal/proc -count=1 -timeout 40m -v -json 2>&1 | tee layer2-diag.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > layer2-diag.rc
            exit "$rc"
        - name: residue snapshot (passive)
          if: always()
          run: ps -eo pgid,pid,ppid,stat,etime,command > ps-job-end.txt
        - uses: actions/upload-artifact@<SHA>     # v7.0.1
          if: always()
          with:
            name: diag-${{ matrix.runner }}-r${{ matrix.replica }}
            if-no-files-found: error
            path: |
              env.txt
              ps-job-start.txt
              ps-job-end.txt
              layer1-test.json
              layer1-test.rc
              layer2-diag.json
              layer2-diag.rc
              diag-out/**
  ```

  - **不含 `workflow_dispatch`**（非 default branch 不會觸發）；不含 `pull_request`（本分支不開 PR）。
  - 兩層各自失敗不互相遮蔽（layer 2 `if: always()`）；job 成敗由兩層 rc 決定，upload 不改變。
  - CI 上的殘留只做**被動快照**（job 結束 VM 即銷毀），不清理。
- [x] **Step 2 一次推送**：四個檔（診斷測試、暫時性 fixture、workflow、本 plan rev 更新）在本機 commit 完成、`go vet -tags diag_orphan ./internal/proc`、`bash -n internal/proc/testdata/diag-orphan.sh`、**`test -x internal/proc/testdata/diag-orphan.sh`**（`bash -n` 只驗語法不驗執行權限）、`git ls-files -s internal/proc/testdata/diag-orphan.sh` 顯示 mode `100755`，與 YAML 解析通過後，owner 授權 **一次** `git push -u origin b2c/diag`；push 觸發 4 個 job（macOS ×2、ubuntu ×2）。記錄 run ID、各 job ID／runner image／耗時。**不 rerun。**
- [x] **Step 3 解析**（本機，唯讀）：下載 4 個 artifact；layer 1：每 job 的 pass／fail 次數與 Elapsed 分布（含 0.5–4.9s「險過」樣本數）；layer 2：逐迭代套用歸因規則，統計 (a′)／(b1)／(b2)／(未定位)／(c)／(輕度延遲)／(未收斂)／(未重現)，並比較 macOS 與 ubuntu；`tCleanupKill` 出現率；rescue 觸發次數與 rescue 後收斂率。

## Task 3: 結論、register 回寫、下一步

- [x] 結論（owner 裁定措辭）：自製 proc 探針路徑 0/400 未重現；對真正 `claude.Session`＋`fake-claude.sh` 路徑尚未排除任何候選機制（見 Task 2 Step 3 解析）。
- [x] register 回寫：**v4**（`5fc3373`，已推送 main）——#7 維持 candidate，補 D6 逐字附註與本輪 CI 資料（run／job／artifact hash）。
- [x] 若需修改：另開票——已裁定拆 **B2c-2**（診斷 round 2，0.3 pt）；本票不修任何碼。
- [x] backlog **rev17**（`5fc3373`，已推送 main）：B2c 關票（結論如上）、新增 B2c-2 0.3 pt、B2a 維持 blocked；`b2c/diag` 遠端分支**保留給 B2c-2 使用**（round 2 檔案備齊後一次推送），B2c-2 結案時再刪。

---

## Task 1／Task 2 Step 1 證據（2026-09-05，本機，分支 `b2c/diag` 自 `81af8f2`）

- **分支與檔案**：`a48018c` plan rev5；`a103d57` 三個診斷檔——`internal/proc/orphan_diag_test.go`（`//go:build diag_orphan`）、`internal/proc/testdata/diag-orphan.sh`（index mode **100755**，`bash -n`／`test -x` OK）、`.github/workflows/diag-orphan.yml`（YAML 解析：trigger 只有 `push`、matrix `runner=[macos-15-intel, ubuntu-latest] × replica=[1,2]`、`fail-fast: false`、7 steps）。`git diff --name-only origin/main..HEAD` 只含四個檔（含本 plan）；main 零變更。
- **測試隔離**：`go test -list Diag ./internal/proc` 無 tag 時 0 筆；`go vet -tags diag_orphan ./internal/proc` 與無 tag `go vet` 皆通過；`gofmt -l` 空。
- **實作偏差（已修）**：初版 reader goroutine 寫 `tLastLine` 與主 goroutine 讀之間無 happens-before，`-race` 報 DATA RACE；改以 mutex 保護 `tLastLine`／`tEOF` 並經 getter 讀取。`exitedToCleanupKillMs` 可為極小負值（`tExited` 是主 goroutine 自 `exitedCh` 醒來的觀察時刻，supervisor 的事件可早數微秒），已註解。
- **smoke**（`DIAG_ITER=2`，`-race`）：PASS、0 DATA RACE、converged 2/2、cleanup KILL 2/2。
- **本機基準（Step 2，`DIAG_ITER=100 -race`，8 核 x86_64，證據目錄 `/tmp/b2c-baseline.XRlhQZ`，907 檔、148 MB）**：`--- PASS (535.81s)`，0 DATA RACE。歸因（`/tmp/b2c-attrib.py` 依本 plan 規則）：**未重現 100/100**；`cleanupKillObserved` **100/100**；rescue 全為 `none`；converged 100、pending 0。分布（ms）：`startToExited` min 6.6／p50 8.2／p95 13.8／max 32.9；`lastLineToExited` 0.08／0.28／0.40／0.71；`exitedToCleanupKill` −0.04／0.00／0.02／0.10；`exitedToEOF` 0.13／0.74／1.92／**13.87**；`exitedToDone` 0.22／1.44／3.88／14.36。`ps@0s` 起本輪孫程序與其 `sleep` 從未被觀察到（100 輪 `orphanPresent@0s`=0、`groupMembers@0s`=0）——本機的 group KILL 在第一個 `ps`（exec 約 10–30ms）完成前就已清空群組。基準門檻確認：`lastLineToExited` <1ms（plan 假設 <10ms 成立）、`exitedToEOF` 全部 <100ms。關鍵 artifact hash：`iter-records.jsonl`、`summary.json`、`run.log`、`ps-run-start.txt`、`ps-run-end.txt` 於證據目錄，見執行紀錄。
- **殘留檢查（Step 3）**：100 個 `iter-*-orphan.pid` 於結束時**皆不存活**（0 alive）；run 起訖全域快照 PID 差集 9 筆，**皆不含 `DIAG_TOKEN`／`sleep 30`**（與矩陣無關的系統／工具程序，列於 `ps-new-pids.txt`）；未執行任何清理（無目標）。
- **第二輪 P1 修正（`380afe1`）**：見 Global Constraints 新增四條；另 fixture 新增 `DIAG_CHILDPIDFILE` 與 `DIAG_MODE`。escape 模式初版曾因 leader 退出後的 group KILL 在 perl `setsid` 完成前殺掉孫程序而失效（快照從未見孫程序、child pid 檔未寫出）；已改為 leader 在 escape 模式下等 child pid 檔出現（≤2s）再退出，normal 模式不變。
- **強制控制（本機，`-race`，皆 0 DATA RACE、`invalidEvidence=0`）**：
  - `hang`（`DIAG_MODE=hang DIAG_EXITED_TIMEOUT=3s` ×2）：`exitedTimeout` 2/2；rescue-before 快照父／子皆在且 `orphanPgidMatches=true` → **`group`** 2/2；guard 2 內 EOF＋Done 收斂 2/2。
  - `escape`（`DIAG_MODE=escape` ×2）：`eofTimeout` 2/2（D1=10s，`exitedToEOF` 10131／10171ms）；**cleanup KILL 事件缺席 2/2**（群組已空、`SignalGroup` 回錯不發事件——與 production 一致）；快照五個時點父／子皆在、`orphanPgid`=孫程序自身 PID≠`p.PGID()` → rescue-before 判定 **`targeted-pid`**，perPid 兩個目標皆 `ok`，rescue-after 孫程序消失；guard 2 內 EOF 到達、`afterRescue=true`、收斂 2/2。這正是 (b2) 的證據形狀。
  - `force-eof`（`DIAG_FORCE_EOF_TIMEOUT=1` ×2）：`eofTimeout` 2/2；rescue-before 找不到本輪孫程序（早已被 supervisor 清掉）→ **`skipped-no-target`** 2/2，未送任何訊號；guard 2 立即收斂。
  - `normal`（×2，fixture 改動後）：converged 2/2、cleanup KILL 2/2。
  - **RO 目錄**（`DIAG_OUT` 為 0555）：測試 rc **1**，`create iter-records.jsonl: permission denied`——證據不完整以非零結束。
  - 控制結束後 `ps` 無任何 `DIAG_TOKEN`／`diag-orphan.sh` 殘留。artifact（`iter-records.jsonl` SHA-256 前 16）：hang `f67866635482be76`、escape `7bc3e530c0f6a6ee`、force-eof `08d6f2d5b7763cb8`、normal `59c66816b5a10660`；RO 控制 log `5723845da60efb2e`。
- **第三輪 P1 修正（見版本行）與控制重跑**：helper control ×3 皆 0.30s 準時返回、不空轉；normal／hang／escape／force-eof 各 ×2 與 RO 目錄控制結果與前輪相同（hang→`exitedTimeout`＋`group`、escape→`eofTimeout`＋`targeted-pid`＋guard 2 收斂＋cleanup 事件缺席、force-eof→`skipped-no-target`、RO→rc 1），0 DATA RACE、`invalidEvidence` 0、`errors` 皆空、殘留 0。artifact：hang `71f540efb8c5e051`、escape `868fd65915b3a34a`、force-eof `e6f144f6b3cf6046`、normal `48f69de123446120`、RO log `e37328eebbb70b29`。
- **100 輪基準重做（第三輪修正後，`d207788`，`-race`，8 核 x86_64，證據目錄 `/tmp/b2c-baseline3.eyEwTc`，907 檔）**：PASS（534.73s）、0 DATA RACE、`invalidEvidence` 0、`errors` 0；歸因**未重現 100/100**；cleanup KILL 100/100；`exitedTimeout` 0；rescue 全 `none`；final 全 `converged`。分布（ms，min／p50／p95／max）：`startToExited` 6.8／8.9／16.3／27.7；`lastLineToExited` 0.18／0.31／0.48／0.67；`exitedToCleanupKill` −0.03／−0.01／0.02／0.06；`exitedToEOF` 0.06／0.73／1.12／**6.96**；`exitedToDone` 0.66／1.33／3.18／11.31。父 PID 檔 100/100 存在；child PID 檔 0/100（normal 模式預期）；`orphanPresent@0s` 0/100。殘留：run 內 PID 結束時 0 存活；起訖新增 PID 無 `DIAG_TOKEN`／`sleep 30`。hash：`iter-records.jsonl` `a3e2335bba0dadc1`、`summary.json` `9aaaa6b5e7602f9d`、`run.log` `18ade467a692a196`。
- **100 輪基準重做（`380afe1`，`-race`，8 核 x86_64，證據目錄 `/tmp/b2c-baseline2.AND6CM`，908 檔）**：PASS、0 DATA RACE、`invalidEvidence` 0；歸因**未重現 100/100**；cleanup KILL 100/100；`exitedTimeout` 0；rescue 全 `none`。分布（ms）：`startToExited` 6.2／8.9／19.5／34.0（min／p50／p95／max）；`lastLineToExited` 0.22／0.31／0.46／0.59；`exitedToCleanupKill` −0.04／−0.01／0.03／0.05；`exitedToEOF` 0.04／0.83／1.52／**5.57**；`exitedToDone` 0.66／1.46／2.87／5.69。`orphanPresent@0s` 0/100。**`childPid` 只有 1/100 被記錄**：normal 模式下孫程序在寫出 `sleep` PID 前就被 supervisor 的 group KILL 清掉，屬預期；若 CI 上 KILL 未及時生效，孫程序會活得夠久寫出 child pid 檔，兩階段身分即可用；child pid 缺席時只能驗父（token），不影響 (b1)／(b2) 的父級判定。殘留：run 內 orphan／child PID 結束時 0 存活；run 起訖新增 20 PID 皆無 `DIAG_TOKEN`／`sleep 30`。hash：`iter-records.jsonl` `f1295f027898e0df`、`summary.json` `c1810da5315f1c81`、`run.log` `cca3d73a82ebd57d`。
- **CI 執行（Task 2 Step 2，owner 授權後 `git push -u origin b2c/diag`，HEAD `d12b13b`）**：run **`33968040444`**（push，2026-09-05 13:08Z），四個 matrix job 皆 `failure`（皆因 layer 1 rc=1；**layer 2 rc 全為 0**）。artifact 本機下載至 `/tmp/b2c-ci.XbqHOZ`（每 job 8 檔＋`diag-out/` 902 檔，hash 見 `HASHES.txt`）。runner：macos-15-intel（Darwin 24.6.0 x86_64，go1.26.5 darwin/amd64）×2、ubuntu-latest（Linux 6.17 azure x86_64，go1.26.5 linux/amd64）×2；fixture executable 皆 yes。

## Task 2 Step 3 解析（2026-09-06）

**Layer 1（既有測試 `TestOrphanDoesNotHangNormalExit` ×100，`internal/claude`，`-race`）**：

| job | fail／100 | 失敗斷言（逐字） | 失敗 elapsed | pass elapsed |
|---|---|---|---|---|
| macos-15-intel r1 | **36** | `session_test.go:207: drain/Wait hung on orphan-held pipes` | 5.00–5.01s | 0.01–0.04s |
| macos-15-intel r2 | **45** | 同上 | 5.01–5.02s | 0.01–0.04s |
| ubuntu-latest r1 | **98** | **`session_test.go:210: orphan must be reaped by supervisor on parent exit`** | 0.00s（97）／0.01s（1） | 0.00s |
| ubuntu-latest r2 | **99** | 同上 | 0.00–0.01s | 0.00s |

macOS 為**二值分布**：要嘛 ≤0.04s 過，要嘛卡滿 5s（沒有 0.05–4.9s 的中間樣本）。ubuntu 幾乎每次都在 `drain`／`Wait` **立即返回後**、`groupDead(s.PGID())` 為 false 時失敗——即 stdout／stderr EOF 都已到、supervisor 已收尾，但 `kill(-pgid, 0)` 仍成功。

**Layer 2（白箱探針 ×100，`internal/proc` 直呼 `proc.Start`＋暫時性 fixture）**：四 job **未重現 100/100**，`invalidEvidence` 0、`errors` 0；cleanup KILL 事件 100/100；`exitedToEOF` p50 0.12–0.37ms、max 0.35–2.92ms；`exitedToDone` max 10.26ms（**毫秒**，非離群）；`exitedTimeout`／`eofTimeout`／`doneTimeout`／anomaly 皆 0；rescue 全 `none`；`ps@0s` 起群組成員（含 zombie）0。

**分歧解讀（事實與假設分開）**：
- **事實**：同一批 runner、同一時段、同一 `proc` 收尾路徑，探針 0/400，既有測試 278/400 紅。因此**機制不在探針所覆蓋的路徑**（proc 直呼＋本 fixture）；差異只剩兩處——(i) 真實測試走 `claude.Session`（`fmt.Fprintf(p.Stdin, JSON 行)` 後 `Close`、`bufio.Scanner` pump 把事件送進容量 64 的 channel、`drain` 消費、`s.Wait()`；探針則直接讀 `p.Stdout` 並寫 `"x\n"`），(ii) 真實 fixture `fake-claude.sh` 的 orphan 為 `bash -c 'trap "" TERM; sleep 30' &` 且 leader **立即**印三行退出；探針 fixture 的 orphan 為 `bash -c "…; sleep 30 & echo $! > file; wait"`（多一次 fork／寫檔，時序不同）。
- **假設 H-mac（未驗證；「存活到 `sleep 30`」僅由 5 秒 guard 推論，未觀察到任何存活程序）**：macOS 的二值分布符合「某個持有 stdout／stderr 的程序在 group KILL 時漏殺（例如 orphan bash 正在 fork `sleep` 的窗口）」——若成立，存活者持有 pipe 直到 `sleep 30` 結束，EOF 不在 5 秒內到達；與 (c) EOF 延遲的連續分布不符，與 (b1) 的「KILL 已送出但程序未消失」形狀相符但探針未觀察到。leader 的極短存活（<1ms）使窗口機率高；探針 fixture 的額外操作可能改變了時序而避開。
- **假設 H-ubuntu（未驗證）**：ubuntu 的 0 秒失敗符合「orphan 已被 KILL 但仍為 zombie、尚未被 subreaper／init 回收，`kill(-pgid, 0)` 對含 zombie 的群組仍回 0」——即測試 oracle `groupDead` 把 zombie 算成存活；這是**測試 oracle 問題，不是 production 收尾缺陷**。探針的 `ps@0s`（約 10–30ms 後）已看不到 zombie，無法在微秒級驗證。
- **探針路徑觀察**：在探針覆蓋的路徑上，(b2) 0/400；(c) 0/400；(a′) 代理 0/400。**這不構成對真實測試路徑任何候選機制的排除**。

**Task 3 結論（owner 裁定後措辭）**：**自製 proc 探針路徑（`proc.Start` 直呼＋暫時性 fixture）0/400 未重現；對真正的 `claude.Session`＋`fake-claude.sh` 路徑，本輪尚未排除任何候選機制**（(a)／(b1)／(b2)／(c) 皆未定）。真實測試在 CI 上高重現且有兩種依 OS 不同的失敗形狀。register #7 維持 candidate 並補本輪資料；**不宣稱 production defect**。

## owner 裁定（rev9 提出、rev10 回寫）

**D5 round 2 探針設計（需另一次推送授權）**——把探針拉到與真實測試同一條路徑，兩層都保留：
1. **layer 2b**：探針改用**真實 fixture** `testdata/fake-claude.sh`（`FAKE_ORPHAN=1`，唯讀使用，不改檔）＋與 `claude.Start` 相同的 stdin 寫法（JSON 行後 `Close`），其餘時序點與 rescue 契約不變；另在 `Done()` 後**立即**執行與測試 oracle 相同的 `syscall.Kill(-pgid, 0)` 並記錄結果與時刻（微秒級驗證 H-ubuntu）；再於 +1ms／+10ms／+100ms 重試記錄 zombie 回收時間。真實 fixture 沒有 token／PID 檔，身分改以「命令列含 `trap "" TERM; sleep 30` 且 pgid==p.PGID() 或 ppid 鏈可追」判定，rescue 仍只對已驗明目標。
2. **layer 3（需 owner 明示例外）**：在 `internal/claude` 新增暫時性 `orphan_diag_test.go`（同樣不合併、結案刪除），以 `claude.Start`＋`drain`＋`Wait` 完整重演既有測試，並加上同樣的立即 `kill(-pgid,0)`／`ps` 取證——這是唯一能直接對真實路徑打點的方法。
3. 若 owner 不同意 layer 3，僅做 layer 2b；若 2b 仍 0 重現，結論改為「機制位於 `claude.Session` 層，需 layer 3」。
4. **owner 裁定**：round 2 **拆為 B2c-2，0.3 pt（3 hr）**，另開 plan、獨立驗收；**明示核准**暫時性、build-tag 隔離的 `internal/claude/orphan_diag_test.go`（不合併、結案刪除）；不修改 production、既有測試或 timeout；B2a 維持 blocked。round 2 契約改為：(i) **fixture-only differential**——維持 `proc.Start`／直接 stdout reader／原單行輸入，只把 fixture 換成真正的 `fake-claude.sh`；JSON stdin 若要驗證，列為另一個 control，不與 fixture 同時改；(ii) **full-path layer 3**——完整走 `claude.Start → drain → Wait`，**分開記錄 `Events()` 關閉與 `Wait()` 返回**；(iii) 真實 fixture 無 token／PID，無可靠身分者只觀察並記 `skipped-no-target`，需要 PID／state 證據時另設明確標成「instrumented fixture」的 control；(iv) `kill(-pgid, 0)` 立即／+1／+10／+100ms 只能證明「群組當下仍存在」（Linux `kill(2)`：signal 0 僅做存在性與權限檢查），zombie 判定必須另取 PID 與 `Z` state，否則維持假設。

**D6（owner 裁定：核准更新 register #7，逐字如下）**：「ubuntu-latest 上 197/200 次於 drain／Wait 返回後立即失敗，`kill(-pgid, 0)` 顯示該 process group 當下仍存在。原因未確認；zombie／oracle race 為待驗假設，不判定為 production defect。」

## Gate A（診斷完成條件）
- [x] 本機基準（Task 1 Step 2）分布已記錄；本機殘留檢查前後快照齊全、無需清理（0 alive）。
- [x] 強制控制覆蓋 `exitedTimeout`、guard 2 deadline、`group`／`targeted-pid`／`skipped-no-target` 三種 rescue 與證據不完整非零結束。
- [x] `b2c/diag` 一次推送觸發 4 個 job，artifact 4 份齊全（`env.txt`、兩層 `.json`／`.rc`、`diag-out/**`、`ps-job-*`）；run ID／job ID／image 抄錄。
- [x] layer 1 與 layer 2 統計表；歸因統計含「未定位」欄（含 a′ 代理命中），`tCleanupKill` 缺席未被推斷為任何 errno；**(a) 未被寫成已確認或已排除**。
- [x] 結論明寫三選一，且**只有 layer 2 出現 (b1)「cleanup KILL 已成功送往正確群組但程序仍未及時消失」的 token／PID 佐證樣本，才可支持 production 收尾缺陷**；(b2) 不在預期 PGID 只支持 fixture／PGID 契約問題；無身分佐證者一律「未定位」。
- [x] `git diff --name-only origin/main..b2c/diag` 只含四個檔；main 零變更；GitHub 設定面零變更。
- [x] 每個 iteration 的等待皆有界：guard 1 以 `tExited` 為絕對 deadline、`Done()` 等待有界、任一逾時即進入 rescue 判定再 guard 2；未收斂者已列表（含 `finalConverged`／`pending`），測試總耗時未被 pending 阻塞。
- [x] 每一次 rescue 都有 `psBefore` 佐證目標屬本輪且 `pgid == p.PGID()`（`mode=group`）或為已驗明 PID（`mode=targeted-pid`）；無佐證者為 `skipped-no-target` 且未送訊號。
- [x] `diag-orphan.sh` 於分支內 mode 100755（`git ls-files -s`），CI job 內 `Start` 成功執行（非 `permission denied`）。
- [x] `iter-records.jsonl` 每輪恰一筆且 `finalConverged|pending` 已填；`iter-<n>-partial.json` 逐輪存在。

## 已知缺口
1. 白箱測試與 `internal/claude` 的測試不是同一個程序，也**不是同一個 fixture**（暫時性 `diag-orphan.sh` 與 `fake-claude.sh` 的 orphan 語意相同：`bash -c 'trap "" TERM; sleep 30' &`＋leader 正常 `exit 0`，差別只在 token 與 PID 檔），但走同一套 `proc.Start`／supervisor 路徑；歸因以統計為準。
2. `ps` 的 exec 成本（macOS 約 10–30ms）使 `ps@0` 分辨不了 <30ms 的事件。
3. `tCleanupKill` 只在 KILL 成功時可觀察；失敗路徑 production 不記錄，本票只能標未定位。
4. 若四 job 皆未重現，只能維持 candidate；B2a 是否可帶著 candidate 合併由 owner 另裁。

## 尚未完成
- **B2c 已關票**（2026-09-06）：Task 1–3 完成；結論依 owner 裁定收斂；register v4／backlog rev17 已推送 main（current main `5fc3373`）。plan rev5–rev11 已 commit（rev9 `cbd7d3b`、rev10 `d5b0967`、rev11 `4688afc`），rev12 待與 B2c-2 檔案一起推送。round 2 由 B2c-2 承接（plan 另立）。`b2c/diag` 遠端 head `d12b13b`；本機領先的 rev9／rev10 **不單獨推送**（會無效觸發四 job），待 B2c-2 檔案備齊後一次推送。

## 修訂記錄
- rev12（2026-09-06）：收尾措辭統一——已關票、rev11＝`4688afc`、current main＝`5fc3373`、基準改稱程式碼／診斷分支基準 `81af8f2`（不再稱 `origin/main`）。
- rev11（2026-09-06）：register v4／backlog rev17 以 docs-only 推送 main `5fc3373`（owner 授權，`81af8f2..5fc3373`，`ls-remote` 相符）；Task 3 兩項勾選；rev10 已提交 `d5b0967`。B2c 關票，round 2 由 B2c-2 承接。
- rev10（2026-09-06，rev9 短複審 CHANGES_REQUIRED）：Task 3 結論改為「自製 proc 探針路徑 0/400 未重現；對真正 `claude.Session`＋`fake-claude.sh` 路徑尚未排除任何候選機制」；H-mac 的「存活到 sleep 30」明標為假設；D5 裁定拆 B2c-2（0.3 pt，核准 `internal/claude` 暫時性診斷檔，round 2 契約：fixture-only differential、full-path layer 3 分開記錄 `Events()` 關閉與 `Wait()` 返回、無身分者只觀察、`kill(-pgid,0)` 只證存在性）；D6 register #7 措辭逐字收斂；已完成 Gate 項勾選；rev9 已提交 `cbd7d3b`。未修改原始碼、未推送。
- rev9（2026-09-06，CI 結果回填）：run `33968040444` 四 job——layer 1 macOS 36／45 次紅在 5 秒 guard（二值分布）、ubuntu 98／99 次紅在 `orphan must be reaped`（0 秒）；layer 2 四 job 400 輪未重現、cleanup KILL 100%、EOF 亞毫秒。結論：探針路徑上排除 (b2)／(c)／(a′)，但真實測試路徑機制未定位；提出 H-mac（fork 窗口漏殺）與 H-ubuntu（oracle 計入 zombie）兩個未驗證假設；新增 D5 round 2 設計（真實 fixture＋立即 `kill(-pgid,0)` 取證；layer 3 需 owner 例外）與 D6（Linux 形狀登記）。未 rerun、未修改原始碼、未 commit 前之 GitHub 寫入。
- rev8（2026-09-06，探針第三輪 P1 修正回填）：`d207788`——`diagWaitConverge` 觀察後設 nil channel＋單一絕對 deadline timer，新增 helper control `TestDiagWaitConvergeDoneBeforeEOF`；背景彙報同時等 EOF 與 Done、稍後 scanner error 計入；`finalizeIter` 共用；stdin close／父 PID 檔缺失計入 `invalidEvidence`。helper ×3、四種控制、RO 控制皆通過；100 輪基準重做全部收斂。「尚未完成」補 rev7 `438e80e`。契約未動。
- rev7（2026-09-06，探針第二輪 P1 修正回填）：`380afe1`——有界 `exitedCh`（`DIAG_EXITED_TIMEOUT`）、guard 2 單一絕對 deadline 迴圈、`onSignal` 於 `p.mu` 內寫入、兩階段身分（父 token＋`DIAG_CHILDPIDFILE`）、rescue 只對已驗明目標（group／targeted-pid／skipped-no-target）、`invalidEvidence` 非零結束；fixture 新增 child pid 檔與 hang／escape 控制模式（escape 需 leader 等 child pid 檔以避開 group KILL 競態）。強制控制四種分支與 RO 目錄皆實證；100 輪基準重做全部收斂。Gate A 本機項改 `[x]`。契約未動。
- rev6（2026-09-05，Task 1 證據回填）：本機分支 `b2c/diag`（`81af8f2`）新增三個診斷檔並提交 `a103d57`；`-race` smoke 與 100 輪基準全部收斂、cleanup KILL 100/100、`exitedToEOF` max 13.87ms、殘留 0；修正一處 reader／主 goroutine 資料競態（mutex）。契約未動。
- rev5（2026-09-05，rev4 短複審 CHANGES_REQUIRED）：P1 rescue 判定改為先取當下 token／PID 快照——本輪孫程序仍在且 `pgid == p.PGID()` 才對群組 SIGKILL（`mode=group`）；(b2) 只對已驗明 PID 及其 `sleep` 個別 kill（`targeted-pid`）；無佐證 `skipped-no-target` 不送訊號；`ps@5s` anomaly 同一套判定。P1 fixture 明定 mode 100755，推送前 `test -x` 與 `git ls-files -s` 核對。P2 JSONL 改為記憶體 record、彙報後每輪序列化一次到 `iter-records.jsonl`，逐輪另存不含 final 欄位的 partial 檔。Gate A 新增三項。未新增檔案、未 commit、未動 GitHub。
- rev4（2026-09-05，rev3 短複審 CHANGES_REQUIRED）：P1 有界收斂重寫 Step 3–5——`tExited` 當下建立絕對 deadline `D1=tExited+10s`，快照 goroutine 與 EOF 等待並行；EOF 到達後的 `Done()` 等待有界（5s）；`eofTimeout` 或 `doneTimeout` 任一即刻 rescue，再 guard 2（5s）；未收斂停止該 iteration、背景 `p.Wait()` 彙報整體至多 30s、逾時記 pending。P1 Goal／Task 3／Gate A 改為只可直接歸因 (b1)／(b2)／(c)，(a′) 為代理證據、不確認不排除 (a)、命中記未定位並要求下一票取得獨立退出時點。P2 刪除「Start 之前設定 onSignal」矛盾句；P2 已知缺口改為不同 fixture、相同 orphan 語意。未新增檔案、未 commit、未動 GitHub。
- rev3（2026-09-05，rev2 短複審 CHANGES_REQUIRED）：P1 Step 4–5 改為 EOF 逾時 → 立即 rescue → guard 2（5s）等 EOF／Done → 收斂者才 `p.Wait()`，未收斂記錄並停止該 iteration；P1 新增暫時性 fixture `internal/proc/testdata/diag-orphan.sh`（孫程序語意同 fake-claude，命令列含每輪 token、父 shell 寫出 `$!`），歸因改為 (b1) 已送往正確群組未消失／(b2) 不在預期 PGID／未定位，Gate A 只有 (b1) 支持 production 缺陷；P2 基準改 `81af8f2`；P2 `lastLineToExited` 改稱「最後輸出至 supervisor 記錄退出的延遲」並標為 (a) 代理。檔案清單 3→4。未新增檔案、未 commit、未動 GitHub。
- rev2（2026-09-05，第一輪 owner CHANGES_REQUIRED）：D1 改採 (c′) 白箱診斷測試檔（`orphan_diag_test.go`，build tag `diag_orphan`，不合併、結案刪除），時序點改為 `tExited`（`exitedCh`）／`tCleanupKill`（`onSignal` 的 `sigEventSupervisorCleanupKill`）／`tEOF`（單一 reader）／`tDone`，`p.Wait()` 只作收斂；cleanup event 缺席只標未定位；第二次 SIGKILL 移至窗口後 rescue 並分開記錄；workflow 改 `push` 限定 `b2c/diag`、matrix runner × replica 四 job、`fail-fast: false`、檔案齊備後一次推送、不 dispatch 不 rerun；新增殘留前後快照與受限清理；D2 ubuntu、D3 100×2、D4 0.4 pt 回寫。B2a `bd6a102` 依裁定留本機。未新增檔案、未 commit、未動 GitHub。
- rev1（2026-09-05）：初稿。
