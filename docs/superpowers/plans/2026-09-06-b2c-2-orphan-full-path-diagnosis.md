# B2c-2 `TestOrphanDoesNotHangNormalExit` round 2：fixture-only differential ＋ full-path 診斷 Implementation Plan（diagnosis-only）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev5（2026-09-06，rev4 短複審 CHANGES_REQUIRED 後修訂：P1 (b1) 補 KILL 與 `ps` 先後關係——記錄 cleanup callback 絕對時刻 `tCleanupKill`，只有 `ps.start >= tCleanupKill` 且正確 PGID 仍有 live member 才支持 (b1)，早於或橫跨者標未定位；P2 D1 措辭改「不送診斷額外 rescue 訊號」、一次推送清單補 B2c rev12／rev13、`kill0` 語意條「同一時點」改為取樣窗口、外層輪詢 JSONL／`pgid` 缺失即 fail loud 且只記「該 PGID 群組消失／逾時」；前版：rev4（rev3 短複審 CHANGES_REQUIRED 後修訂：P1 checkpoint 讀走 cap-1 channel 後彙報誤判 pending——主 goroutine 保存 `eventsSeen`／`waitSeen` 與不可變結果，aggregator 接受既有狀態，`reportDeadline` 於彙報開始只建立一次全批共用；P1 pending 控制 Env 接線——具名診斷入口 `DIAG_FAKE_HANG=1 → FAKE_HANG=1`（正式 CI 不設定），`t.Fatalf` 後的 ≤40s 自然消失改由外層 shell 依 artifact 記錄的 PGID 有界唯讀輪詢、最後仍 `exit "$test_rc"`；P1 (b1) 證據不得跨 layer 拼「同一輪」——只有 layer 2b 單一樣本同時具備 cleanup event 成功＋自身鄰近 `ps` 顯示正確 PGID 仍有 live member 才支持 (b1)；文字：「同時點 ps」改「有起訖時間的鄰近／重疊 ps 取樣」；前版：rev3（rev2 短複審 CHANGES_REQUIRED 後修訂：P1 `pending` 與 `invalidEvidence` 分開計數（皆令 rc 非零，Gate 同時要求兩者為 0）；P1 layer 3 record 只由主 goroutine 持有、worker 以 buffered channel 傳不可變結果；P1 fail-loud 控制改為確定性（`DIAG_ITER=1`＋`DIAG_REPORT_DEADLINE=1s`＋真實 fixture `FAKE_HANG=1` observe-only）並抽出 report aggregator 以 synthetic channel 單測；P1 kill0 各偏移以同一 monotonic base 獨立排程、`Z` 只寫「支持性證據」、layer 3 的 S／R 只證「Session 完成後仍有 live member」、(b1) 需 layer 2b cleanup-event 佐證；P1 workflow 三 step 逐字契約（`DIAG_ITER`／`DIAG_OUT`／`DIAG_RUNNER`、不重疊目錄、各自 `go test -timeout`、wrapper 與 upload 路徑）、40 分鐘標為估算；文字：「零診斷額外 rescue 訊號」、狀態列補本機領先 rev9／rev10／rev11；前版：rev2（第一輪 owner CHANGES_REQUIRED 後修訂：D1 observe-only＋整批 60s 絕對期限彙報、逾時記 pending 並 fail loud；D2 layer 3 維持既有測試順序（同一 worker 先 drain 至 `Events()` 關閉再 `s.Wait()`）、保留 5 秒 combined-completion checkpoint 只取證不 `t.Fatal`、`kill(-pgid,0)` 保存原始 errno；D3 移除 JSON stdin control；D4 2×2 matrix、2b／3 各 100 次、原 layer 2 parity 20 次、最壞時間公式；D5 維持 0.3 pt；scope 補既存 fixture 共六檔、基準拆為程式碼 `81af8f2`／governance `5fc3373`、scope check 改三點；前版：rev1）
> 狀態：**rev5 APPROVED（owner，2026-09-06）；Task 1 進行中——本 plan 首次 commit，程式尚未新增、未推送；`b2c/diag` 遠端 head 仍為 `d12b13b`（本機領先 B2c rev9–rev13＝`cbd7d3b`／`d5b0967`／`4688afc`／`a8b68e8`／`a0da2b4`，不單獨推送）**
> 票源：Pre-M4 Readiness Backlog **B2c-2**（owner 於 B2c rev9 複審裁定拆出，**0.3 pt**＝3 hr；backlog rev17 落地）。承接 B2c plan rev10 的結論與 D5 契約
> 基準 commit：**程式碼／診斷分支基準 `81af8f2`**（`b2c/diag` 的 merge-base；`internal/proc`、`internal/claude`、`testdata` 自 `05069e2` 後未變）；**governance main `5fc3373`**（register v4／backlog rev17，已推送）。scope check 一律用三點 `git diff --name-only origin/main...HEAD`（自 merge-base 看 branch-only 變更）
> owner 明示核准：暫時性、build-tag 隔離的 `internal/claude/orphan_diag_test.go`（不合併、結案刪除）。**不修改 production、既有測試或任何 timeout。B2a 維持 blocked。**

**Goal:** 針對 B2c 的分歧——自製 proc 探針 0/400 未重現、真實測試（`claude.Session`＋`fake-claude.sh`）在 CI 上 macOS 81/200（5 秒 guard）、ubuntu 197/200（`orphan must be reaped`，0 秒）重現——用**兩個各自只改一個變因**的實驗把差異切開（另保留原 layer 2 的小型 parity control）：
- **layer 2b（fixture-only differential）**：探針其他一切不變（`proc.Start` 直呼、直接 stdout reader、單行 `"x\n"` 輸入、時序點與 rescue 契約），**只把 fixture 換成真正的 `testdata/fake-claude.sh`（`FAKE_ORPHAN=1`）**。若重現 → 差異在 fixture（orphan 結構／leader 時序）；若仍 0 → **差異位於 `claude.Session` 所增加的路徑集合（stdin JSON 寫法、pump／Decode／channel、`Wait` 包裝），子機制未定位**。
- **layer 3（full-path）**：在 `internal/claude` 以**既有測試的實際順序**重演——同一個 worker goroutine 先 `drain` 至 `Events()` 關閉、再呼叫 `s.Wait()`、再 `close(doneCh)`；外層保留原本的 **5 秒 combined-completion checkpoint**（5 秒到時只取證、不 `t.Fatal`，worker 繼續自然收斂）；**分開記錄 `tEventsClosed` 與 `tWaitReturn`**；checkpoint 與收斂時各做與既有 oracle 相同的 `syscall.Kill(-pgid, 0)`（保存原始 errno）與 `ps` 快照（只觀察）。

**Architecture:** 承 B2c 的 CI 取證骨架與所有有界性／證據完整性契約；本輪 job 內三個 step：layer 2b（100）、layer 3（100）、原 layer 2 parity（20，只確認參數化未改壞 baseline，不入主要分母），各自獨立 artifact、`if: always()`。真實 fixture 沒有 token／PID 檔，**真實 fixture 路徑零診斷額外 rescue 訊號**（observe-only；記 `skipped-no-target`；既有 supervisor 仍會在 `proc.go:191` 發 cleanup KILL）。逾時輪**記錄後立即繼續下一輪**，未收斂的 `Proc`／`Session` 交背景，最後以**整批共用的 60 秒絕對期限**彙報（EOF／`Events()` 關閉與 `Done`／`Wait` 兩者皆到才 finalConverged），超過仍未收斂記 **`pending`**。**`pending` 是有效診斷結果，與 `invalidEvidence`（artifact／Start／stdin／scanner／編碼等取證失敗）分開計數**；兩者任一 >0 都令測試 rc 非零，summary 分別列出。`kill(-pgid, 0)` 保存原始 error：`nil`＝群組存在、`ESRCH`＝不存在、其他＝unknown；zombie 只能由有起訖時間的鄰近／重疊 `ps` 取樣的 `Z` state 判定。

**Tech Stack／參考文件：** 同 B2c plan（`macos-15-intel`＋`ubuntu-latest` 各兩 replica、Go 1.26.5、`-race`、push 觸發 matrix）；`internal/claude/session.go:68-131`（`Start`／`Events`／`Wait`）、`internal/claude/session_test.go:15-26,180,200-212`（`fakeCfg`／`drain`／`groupDead`／被診斷的測試）、`testdata/fake-claude.sh:14-23`、B2c plan rev10 Task 2 Step 3 解析與 D5／D6。

---

## 已知事實（承 B2c，2026-09-06）

- **真實測試**：`s, _ := Start(ctx, fakeCfg(t, "FAKE_ORPHAN=1"))`（`TermGrace: 200ms`、`CWD: t.TempDir()`、`Prompt: "x"`）；`go func(){ drain(s); s.Wait(); close(doneCh) }()`；`select { case <-doneCh: case <-time.After(5s): t.Fatal(207) }`；`if !groupDead(s.PGID()) { t.Fatal(210) }`，其中 `groupDead(pgid) = syscall.Kill(-pgid, 0) != nil`。
- **`claude.Start`**：`proc.Start(... Args: cfg.args(), Dir: cwd, Env: cfg.Env, TermGrace ...)` → `fmt.Fprintf(p.Stdin, "%s\n", JSON)` → 單回合 `p.Stdin.Close()` → pump goroutine：`bufio.Scanner(p.Stdout)` 每行 `Decode` 送進 `events`（cap 64），EOF 後 `close(s.events)`；`Wait()` 回 `p.Wait()`。
- **真實 fixture**（`fake-claude.sh`）：`read -r _prompt || true` → `[FAKE_ORPHAN] bash -c 'trap "" TERM; sleep 30' &` → 印 init／delta／（無 FAKE_HANG）result → `exit 0`。leader 從起 orphan 到退出 <1ms；orphan 為 bash（macOS bash 3.2 會 fork `sleep`；Linux bash 5 是否 exec 最後一個命令未查證）。
- **B2c CI 結果**（run `33968040444`）：layer 1 macOS 36／45 紅（皆 5.00–5.02s，pass 皆 ≤0.04s，二值）、ubuntu 98／99 紅（皆 0.00–0.01s，`orphan must be reaped`）；layer 2（自製 fixture）0/400，cleanup KILL 100%，EOF 亞毫秒。
- **假設（皆未驗證）**：H-mac＝group KILL 漏殺持有 pipe 的程序（fork 窗口）；H-ubuntu＝orphan 已 KILL 但仍為 zombie、`kill(-pgid,0)` 對含 zombie 的群組回 0（oracle race）。

---

## owner 裁定（design gate 第一輪，rev2 回寫）

- **D1（核准 observe-only）**：真實 fixture 路徑不送診斷額外 rescue 訊號（既有 supervisor 的 cleanup KILL 照常）。逾時輪記錄後**繼續下一輪**（不串行等 30–60s），未收斂者交背景；整批以 **60 秒絕對期限**彙報；超過仍未收斂 → `pending`（有效結果、獨立計數，rc 非零）；`invalidEvidence` 只計取證失敗。instrumented control 不重跑（沿用 B2c 資料），只保留 20 次 parity（D4）。
- **D2（方向核准，兩項 P1 已納入）**：(i) layer 3 維持既有測試順序——同一 worker 先 `drain` 至 `Events()` 關閉再 `s.Wait()`，外層保留 **5 秒 combined-completion checkpoint**，5 秒到只取證（`kill(-pgid,0)` errno、`ps`、`tEventsClosed`／`tWaitReturn` 是否已到）不 `t.Fatal`，worker 繼續自然收斂；**不得**以 10／12 秒新期限取代原失敗形狀。(ii) 每次 `kill(-pgid, 0)` 保存**原始 error／errno**（`nil`／`ESRCH`／其他），不只存布林；**各偏移（0／1ms／10ms／100ms／1s）以同一 monotonic base 的絕對期限獨立排程**（`time.Sleep(time.Until(base+off))` 後立刻 kill0，`ps` 在另一 goroutine 依自己的偏移排程，不得讓 `ps` 的 10–30ms 執行拖延 kill0）；`ps` 另記實際起訖時刻；看到 `Z` 只能寫「與 zombie／oracle 假設相容、提供支持性證據」，不得稱直接證明該 zombie 導致稍早的 `kill0=nil`。layer 3 看不到 `proc.onSignal`，看到 `S`／`R` 只能記「Session 完成後仍有 live group member」，**不歸為 (b1)**，也**不得與另一層的 cleanup event 合併歸因**（layer 2b 與 layer 3 是不同程序、不同 iteration，沒有一對一同輪關係）。**(b1) 只能由 layer 2b 單一樣本自身**同時具備：(a) cleanup event 成功並記錄 callback 的絕對時刻 **`tCleanupKill`**（`proc.go:191` 的 event 是 `SignalGroup(SIGKILL)` 成功返回後才發出）；(b) 該樣本自己的某次 `ps` 滿足 **`ps.start >= tCleanupKill`** 且顯示正確 PGID 仍有 live member（`S`／`R`）。取樣 `ps.start` 早於 `tCleanupKill`、或 `ps.start < tCleanupKill <= ps.end`（橫跨）者，只能標「未定位」；KILL 前看到 live member 是正常狀態，不支持「KILL 未及時生效」。
- **D3（不納入）**：JSON stdin control 移除——fixture 只讀一行不解析 JSON，layer 3 已涵蓋真正 stdin 寫法。
- **D4（2×2 matrix）**：layer 2b 100 次、layer 3 100 次、原 layer 2 parity 20 次；不重跑 layer 1；`timeout-minutes: 90`。**最壞時間公式**（每 job）：layer 2b ≤ 100 ×（Start＋5s 快照）＋ 逾時輪的 D1 10s（不串行等收斂）≈ 100×5.5s ＋ 100×10s 上界 ≈ 26 min；layer 3 ≤ 100 ×（5s checkpoint 上界＋取樣約 0.5s）≈ 9 min（收斂輪 <1s）；parity 20 × 5.5s ≈ 2 min；彙報 ≤ 60s ×3 層；合計**估算**約 40 min；**硬上限由各 step 的 `go test -timeout` 與 job 的 90 分鐘共同保證**（見 Task 3 逐字契約）。
- **D5（維持 0.3 pt）**：移除 JSON control 後可接受；backlog 算式不變。

---

## Global Constraints

- **零變更**：不改 `internal/**` 既有檔、`testdata/**`、`ci.yml`、`go.mod`；不放寬 timeout、不加 retry。診斷分支 branch-only 範圍為**六個檔**：`internal/proc/orphan_diag_test.go`（B2c 檔，本輪參數化 fixture）、`internal/proc/testdata/diag-orphan.sh`（B2c 既存 fixture，本輪不改）、`internal/claude/orphan_diag_test.go`（新，`//go:build diag_orphan`）、`.github/workflows/diag-orphan.yml`（改為 2b／3／parity 三 step）、B2c plan、本 plan。scope check：**`git diff --name-only origin/main...HEAD`（三點）** 只含這六個。
- **GitHub 外部寫入逐步授權**：本輪只有**一次** `git push origin b2c/diag`（含 B2c rev9–rev13、本 plan、程式）；不 dispatch、不 rerun、不動設定面。
- **身分與訊號**：真實 fixture 路徑（2b、3）**零診斷額外 rescue 訊號**（既有 supervisor 的 cleanup KILL 照常）；只有 parity control（`diag-orphan.sh`，有身分）沿 B2c 的兩階段身分才可 rescue。
- **`pending` vs `invalidEvidence`**：分開計數與列示；`pending`＝60s 彙報期限內未收斂（有效診斷結果）；`invalidEvidence`＝取證失敗。任一 >0 → rc 非零；Gate A 同時要求兩者為 0。
- **layer 3 record 所有權**：record 只由主 goroutine 持有與序列化；worker 只透過 **buffered channel（cap 1）** 傳回不可變結果 `{tFirstEvent, tLastResult, tEventsClosed, tWaitReturn, exit}`（`Events()` 關閉後一筆、`Wait()` 返回後一筆，或合併為兩個 channel），主 goroutine 在 checkpoint／收斂／彙報時**只讀 channel**，且每個 channel 的那一筆只會被讀走一次：讀到即設 `eventsSeen`／`waitSeen` 並保存不可變結果於 record；aggregator **接受既有狀態**（已 seen 的不再等待，只對未 seen 的 channel 等待）；`reportDeadline` 在彙報開始時**只建立一次**、所有 iteration 共用；序列化後不再收 channel。
- **`kill(-pgid, 0)` 語意**：保存原始 error（`nil`＝存在、`ESRCH`＝不存在、其他＝unknown）與時刻；任何「zombie」結論必須有起訖時間與 `kill0` 時刻鄰近／重疊的 `ps` 取樣窗口內的 `Z` state 佐證，否則寫「群組當下仍存在、狀態未知」。`ps` 記錄實際起訖時刻（exec 前後），與 `tCleanupKill`（proc 層）一起寫入 record 以判先後。
- 有界性、證據完整性（`invalidEvidence` → 非零）、`onSignal` 鎖內寫入（僅 proc 層）、單一 reader、record 於彙報後序列化、wrapper 契約：**全部沿 B2c plan rev8 Global Constraints**；彙報期限本輪為 **60s 整批絕對期限**，超過即 `pending`（獨立計數，rc 非零）。**report aggregator 抽成可單測的函式**（輸入：每輪的 `eofCh`／`doneCh`（或 layer 3 的兩個結果 channel）、絕對 deadline；輸出：finalConverged／pending 與時刻），以永不關閉的 synthetic channel 單測 deadline、`pending` 判定與非零 rc，並單測「一筆已 seen、另一筆永不到」只等未 seen 者。
- **診斷專用 Env 入口**：正式 fake 路徑（2b、3）Env 只有 `FAKE_ORPHAN=1`。控制用的 `FAKE_HANG=1` 只能經具名診斷入口 **`DIAG_FAKE_HANG=1`**（測試讀到後才加 `FAKE_HANG=1` 進 fixture Env）；正式 CI 三個 step **皆不設定** `DIAG_FAKE_HANG`。record 一律寫入 `pgid`，供外層 shell 唯讀輪詢。layer 3 的 `Events()` 只能由診斷測試的 worker 消費（`claude.Session` 內部已是單一 stdout reader）。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭；`gh` 輸出逐字抄錄。

---

## Task 1: layer 2b——proc 探針參數化（本機）

- [ ] **Step 1**：`internal/proc/orphan_diag_test.go` 新增環境變數 `DIAG_FIXTURE=diag|fake-claude`（預設 `diag`，行為與 B2c 完全相同）。`fake-claude` 時：Binary 改 `../../testdata/fake-claude.sh`，Env 只給 `FAKE_ORPHAN=1`（不給 token／PID 檔）；身分欄位全為「不可用」，`rescueDecide` 在無身分時**直接** `skipped-no-target`（不讀任何 PID 檔、不送訊號）；逾時輪不等 `sleep 30`，記錄後交背景、繼續下一輪；`ps` 摘要新增 `groupStatZ`（群組成員中 `stat` 以 `Z` 開頭的數量）與 `orphanLikeCount`（命令列含 `trap "" TERM; sleep 30` 或 `sleep 30` 且 `pgid == p.PGID()` 的數量，**僅觀察**）；每次 `ps` 記 exec 前後時刻；record 保存 `onSignal` cleanup callback 的絕對時刻 `tCleanupKill`（B2c 已有 event，本輪補絕對時刻欄位），並對每次 `ps` 標 `afterCleanupKill`（`ps.start >= tCleanupKill`）／`straddle`／`before`。彙報階段改為整批 60s 絕對期限（`DIAG_REPORT_DEADLINE` 可覆寫，僅本機控制用），超過記 `pending`（獨立計數）；summary 分列 `pending` 與 `invalidEvidence`，任一 >0 → `t.Fatalf`。**不新增 JSON stdin 選項（D3）。**
- [ ] **Step 2 本機控制**：`DIAG_FIXTURE=fake-claude DIAG_ITER=10` 與 `DIAG_FIXTURE=diag DIAG_ITER=3`（回歸）各跑一次 `-race`，確認 0 DATA RACE、`invalidEvidence` 0、真實 fixture 路徑 rescue 全為 `none`／`skipped-no-target`、無殘留；另以 `DIAG_FIXTURE=fake-claude DIAG_MODE=escape`（fake-claude 不支援 escape，應被忽略）確認參數不外洩。**確定性 fail-loud 控制（pending）**：`DIAG_FIXTURE=fake-claude DIAG_ITER=1 DIAG_REPORT_DEADLINE=1s DIAG_FAKE_HANG=1`（→ fixture Env 加 `FAKE_HANG=1`：leader 收尾前 `sleep 30`，observe-only 不會被 rescue 收斂）→ 該輪必 `exitedTimeout`（10s）→ 進彙報時仍未收斂 → `pending=1`、`invalidEvidence=0`、`t.Fatalf`、rc 非零。**測試內不再等待**；由外層 shell 在保存 `test_rc` 後，讀 artifact（JSONL record）的 `pgid`：**JSONL 不存在、無法解析、`pgid` 缺失或非正整數 → 立即 fail loud（印出原因、`exit 1`）**；否則以 `kill -0 -- -$pgid`／`ps -o pid,pgid,stat -g $pgid`（唯讀）每 1s 輪詢、上限 40s，只記「**該 PGID 群組消失時刻**」或「**逾時仍存在**」（真實 fixture 無身分，不斷言是本輪程序自然消失），最後仍 `exit "$test_rc"`（非零）。**aggregator 單測**：`TestDiagReportAggregator`（build tag 內）以永不關閉的 synthetic channel 驗證 300ms deadline 準時回 pending、select 不空轉；再驗「eof 已 seen、done 永不到」只等 done、以及兩者皆 seen 時立即返回不看 deadline。
- [ ] **Step 3**：既有 B2c 強制控制（hang／escape／force-eof／helper／RO）重跑一次確認未回歸。

## Task 2: layer 3——`internal/claude/orphan_diag_test.go`（本機）

- [ ] **Step 1**：新檔（`//go:build diag_orphan`，`package claude`，檔頭註解「B2c-2 暫時性、不合併、結案刪除」）。每輪**依既有測試的實際順序**：
  1. `s, err := Start(ctx, fakeCfg(t, "FAKE_ORPHAN=1"))`（直接呼叫既有 `fakeCfg`，不複製）；`t0`；`pgid := s.PGID()`。
  2. **worker goroutine**（與既有測試相同的一條）：`for ev := range s.Events()`（記 `tFirstEvent`、遇 `KindResult` 記 `tLastResult`、range 結束記 **`tEventsClosed`**）→ 送一筆不可變結果到 `eventsCh`（cap 1）→ `s.Wait()` 返回記 **`tWaitReturn`** 與 Exit → 送一筆到 `waitCh`（cap 1）→ `close(doneCh)`。worker **不持有、不修改 record**；worker 內**不設**任何新期限。
  3. 主 goroutine：`select { case <-doneCh: converged; case <-time.After(5 * time.Second): checkpoint }`——**5 秒 combined-completion checkpoint 與既有測試相同**；到 checkpoint 時**只取證、不 `t.Fatal`**：記 `checkpointHit=true`、以非阻塞 `select` 讀 `eventsCh`／`waitCh`，讀到即設 `eventsSeen`／`waitSeen` 並把不可變結果存入 record（該筆之後不會再出現在 channel）、`kill(-pgid,0)` 的原始 error、`ps` 快照（含 `stat`、起訖時刻）。record 只在主 goroutine 更新。
  4. **oracle 取樣**：`base`＝`doneCh` 關閉時刻（收斂輪）或 checkpoint 時刻（逾時輪）。**kill0 goroutine**：對偏移 0／1ms／10ms／100ms／1s 各 `time.Sleep(time.Until(base+off))` 後立刻 `syscall.Kill(-pgid, 0)`，記 `{off, at, err}`（`nil`／`ESRCH`／其他）；**ps goroutine**（獨立）：對偏移 0／10ms／100ms／1s 各排程一次 `ps`，記 exec 起訖與 `pgid` 成員的 `pid`／`ppid`／`stat`。兩個 goroutine 互不等待；結果經 channel 回主 goroutine。`Z` 只在 `ps` 內判定並只作支持性證據。
  5. 逾時輪：記錄後**立即進入下一輪**，該輪的 `doneCh`／worker 交背景；不送訊號。
  6. 彙報：整批 60s 絕對期限（`DIAG_REPORT_DEADLINE` 可覆寫），`reportDeadline` 只建立一次；逐輪把 `eventsSeen`／`waitSeen` 既有狀態交給 aggregator，只對未 seen 的 channel 等待（即補齊 `Events()` 關閉與 `Wait()` 返回）；超過 → `pending`（獨立計數，已 seen 的部分照常寫入）。序列化後不再讀任何 channel。record／partial／JSONL／summary 契約同 B2c；summary 分列 `pending`／`invalidEvidence`。
- [ ] **Step 2 本機**：`DIAG_ITER=30 -race` 一次；預期本機全收斂、`checkpointHit` 0、`groupExists@0` 多為 `ESRCH`（既有測試本機 0/50 紅）；記錄分布作對照。確定性 fail-loud 控制：`DIAG_ITER=1 DIAG_REPORT_DEADLINE=1s DIAG_FAKE_HANG=1`（測試讀到後以 `fakeCfg(t, "FAKE_ORPHAN=1", "FAKE_HANG=1")` 啟動；leader 收尾前 `sleep 30`，`drain` 在 5 秒 checkpoint 前不會結束）→ `checkpointHit=1`、進彙報時未收斂 → `pending=1`、`invalidEvidence=0`、`t.Fatalf`、rc 非零；群組消失由外層 shell 依 record `pgid` 唯讀輪詢 ≤40s（同 Task 1 Step 2：JSONL／`pgid` 缺失即 fail loud，只記該 PGID 群組消失／逾時），最後 `exit "$test_rc"`。aggregator 單測與 proc 層共用同一函式或各自一份（build tag 內）。

## Task 3: workflow 與一次推送

- [ ] `diag-orphan.yml` 改為三個 step（皆 `if: always()`，各自輸出目錄不重疊，各自 `go test -timeout`，wrapper 契約保留 rc，upload 路徑逐字如下；移除 layer 1）：

  ```yaml
        - name: layer 2b — fixture-only differential (fake-claude.sh) x100
          if: always()
          shell: bash
          env:
            DIAG_FIXTURE: fake-claude
            DIAG_ITER: '100'
            DIAG_OUT: ${{ github.workspace }}/diag-out-2b
            DIAG_RUNNER: ${{ matrix.runner }}-r${{ matrix.replica }}
          run: |
            set +e
            mkdir -p "$DIAG_OUT"
            go test -tags diag_orphan -race -run '^TestDiagOrphanTimeline$' ./internal/proc -count=1 -timeout 35m -v -json 2>&1 | tee layer2b.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > layer2b.rc
            exit "$rc"
        - name: layer 3 — full path (claude.Start → drain → Wait) x100
          if: always()
          shell: bash
          env:
            DIAG_ITER: '100'
            DIAG_OUT: ${{ github.workspace }}/diag-out-3
            DIAG_RUNNER: ${{ matrix.runner }}-r${{ matrix.replica }}
          run: |
            set +e
            mkdir -p "$DIAG_OUT"
            go test -tags diag_orphan -race -run '^TestDiagOrphanFullPath$' ./internal/claude -count=1 -timeout 20m -v -json 2>&1 | tee layer3.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > layer3.rc
            exit "$rc"
        - name: layer 2 parity — instrumented fixture x20 (not in denominator)
          if: always()
          shell: bash
          env:
            DIAG_FIXTURE: diag
            DIAG_ITER: '20'
            DIAG_OUT: ${{ github.workspace }}/diag-out-parity
            DIAG_RUNNER: ${{ matrix.runner }}-r${{ matrix.replica }}
          run: |
            set +e
            mkdir -p "$DIAG_OUT"
            go test -tags diag_orphan -race -run '^TestDiagOrphanTimeline$' ./internal/proc -count=1 -timeout 10m -v -json 2>&1 | tee parity.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > parity.rc
            exit "$rc"
        - uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
          if: always()
          with:
            name: diag2-${{ matrix.runner }}-r${{ matrix.replica }}
            if-no-files-found: error
            path: |
              env.txt
              ps-job-start.txt
              ps-job-end.txt
              layer2b.json
              layer2b.rc
              layer3.json
              layer3.rc
              parity.json
              parity.rc
              diag-out-2b/**
              diag-out-3/**
              diag-out-parity/**
  ```

  `timeout-minutes: 90`（job 硬上限）；各 step 的 `go test -timeout`（35m／20m／10m）為 step 硬上限；D4 的 40 分鐘只是估算。
- [ ] 本機：`go vet -tags diag_orphan ./internal/proc ./internal/claude`、無 tag 隔離（`go test -list Diag ./internal/...` 為空）、`gofmt -l`、YAML 解析；scope：`git diff --name-only origin/main...HEAD` 只含六個檔。
- [ ] owner 授權後**一次** `git push origin b2c/diag`（含 B2c rev9–rev13、本 plan、程式）；四 job 各含 2b／3／parity。不 rerun。

## Task 4: 解析、結論、回寫

- [ ] 依 layer 對照表判讀：
  - 2b 重現 → **fixture 差異**為主因（orphan 結構／leader 時序）；2b 不重現而 3 重現 → **差異位於 `claude.Session` 所增加的路徑集合（stdin JSON 寫法、pump／Decode／channel、`Wait` 包裝），子機制未定位**；parity 20 次須與 B2c 的 layer 2 一致（0 重現、cleanup KILL 100%），否則參數化有誤、本輪 2b 結果無效。
  - layer 3 的 `kill0@0.err == nil` 且鄰近 `ps` 有 `Z` → **與 zombie／oracle 假設相容、提供支持性證據**（不稱直接證明）；`kill0@0.err == nil` 但成員 `S`／`R` → 只記 **「Session 完成後仍有 live group member」**（不歸 (b1)、不與 layer 2b 合併歸因）；(b1) 只能由 layer 2b **單一樣本自身**「cleanup event 成功（`tCleanupKill`）＋自身 **`ps.start >= tCleanupKill`** 的取樣顯示正確 PGID 仍有 live member」支持，早於或橫跨 `tCleanupKill` 的取樣標未定位；`ESRCH` → 群組已不存在（與既有測試的失敗矛盾，需查 checkpoint 時序）；`tEventsClosed` 與 `tWaitReturn` 何者未在 5 秒 checkpoint 前到達，說明 macOS 5 秒是卡在 EOF 還是 `Wait`。
- [ ] register 回寫（版本＝當時最新＋1）：#7 依結論改為 resolved／no-change／需拆 implementation 票，或維持 candidate；backlog rev18：B2c-2 關票、B2a 是否解除 blocked 由 owner 裁定；owner 授權後刪 `b2c/diag`（探針原始碼以 plan 附錄保存）。

---

## Gate A（B2c-2 完成條件）
- [ ] 本機控制（Task 1 Step 2–3、Task 2 Step 2）全部通過並記錄。
- [ ] 一次推送觸發四 job；每 job 2b／3／parity 三層 artifact 齊全；**`invalidEvidence` 0 且 `pending` 0**（分開列示）；parity 與 B2c layer 2 一致。
- [ ] 對照表判讀寫明；`kill(-pgid,0)` 以原始 errno 記錄且各偏移獨立排程；`Z` 只寫支持性證據；layer 3 的 S／R 不歸 (b1)、不跨 layer 合併歸因；(b1) 判定附 `tCleanupKill` 與 `ps.start` 先後；`pgid`／`tCleanupKill` 寫入 record；真實 fixture 路徑零診斷額外 rescue 訊號；layer 3 的 5 秒 checkpoint 形狀與既有測試相同；record 只由主 goroutine 持有。
- [ ] 本機控制：確定性 pending 控制（`DIAG_FAKE_HANG=1`；rc 非零、`pending=1`、`invalidEvidence=0`；外層 shell 唯讀輪詢記錄該 PGID 群組消失／逾時，JSONL／`pgid` 缺失即 fail loud）與 aggregator 單測（含既有 seen 狀態）皆通過；正式 CI YAML 無 `DIAG_FAKE_HANG`。
- [ ] `git diff --name-only origin/main...HEAD` 只含六個檔；main 零變更；GitHub 設定面零變更。

## 已知缺口
1. 真實 fixture 無身分：若 layer 2b／3 出現存活程序，只能觀察、不能安全清理；`sleep 30` 自然結束後收斂，於整批 60s 彙報期內回收（不串行等待）。
2. `ps` 取樣最早 10–30ms；`groupExists@0` 是唯一微秒級訊號，但只證存在。
3. Linux bash 是否對 `bash -c 'trap "" TERM; sleep 30'` 做 exec 最佳化未查證；layer 3 的 `ps` 會直接顯示 orphan 是 `bash` 還是 `sleep`。

## 尚未完成
- rev5 APPROVED；Task 1 開始。Task 1–4 未完成。本 plan 首次 commit（本 commit）。

## 修訂記錄
- rev5（2026-09-06，rev4 短複審 CHANGES_REQUIRED）：(b1) 補 KILL 與 `ps` 先後——record 記 `tCleanupKill`，每次 `ps` 標 `afterCleanupKill`／`straddle`／`before`，只有 `ps.start >= tCleanupKill` 且正確 PGID 仍有 live member 才支持 (b1)；D1 措辭改「不送診斷額外 rescue 訊號」；一次推送清單補 B2c rev12／rev13；`kill0` 語意條改為取樣窗口；外層輪詢 JSONL／`pgid` 缺失即 fail loud，只記該 PGID 群組消失／逾時。
- rev4（2026-09-06，rev3 短複審 CHANGES_REQUIRED）：checkpoint 讀走 cap-1 channel 後由主 goroutine 保存 `eventsSeen`／`waitSeen` 與不可變結果、aggregator 接受既有狀態、`reportDeadline` 只建立一次；pending 控制改經具名診斷入口 `DIAG_FAKE_HANG=1`、`t.Fatalf` 後自然消失改由外層 shell 依 record `pgid` 唯讀輪詢並 `exit "$test_rc"`；(b1) 只由 layer 2b 單一樣本自身支持、layer 3 S／R 不跨 layer 合併歸因；「同時點 ps」改「有起訖時間的鄰近／重疊 ps 取樣」；狀態列補 rev12。
- rev3（2026-09-06，rev2 短複審 CHANGES_REQUIRED）：`pending` 與 `invalidEvidence` 分開計數（Gate 同時要求為 0）；layer 3 record 只由主 goroutine 持有、worker 以 cap-1 channel 傳不可變結果；確定性 pending 控制（`DIAG_ITER=1`＋`DIAG_REPORT_DEADLINE=1s`＋真實 fixture `FAKE_HANG=1`）與 aggregator 單測；kill0 各偏移獨立排程、`Z` 只作支持性證據、S／R 不單獨歸 (b1)；workflow 三 step 逐字契約（環境變數、不重疊目錄、`-timeout` 35m／20m／10m、wrapper、upload 路徑），40 分鐘標為估算；「零訊號」改「零診斷額外 rescue 訊號」；狀態列補本機領先 rev9／rev10／rev11。
- rev2（2026-09-06，第一輪 owner CHANGES_REQUIRED）：D1 observe-only、逾時輪不串行等待、整批 60s 絕對期限、超時 `pending`＋fail loud；D2 layer 3 改為既有測試順序（worker 先 drain 至 `Events()` 關閉再 `Wait()`），保留 5 秒 combined-completion checkpoint 只取證不 `t.Fatal`，`kill(-pgid,0)` 保存原始 errno、`ps` 記起訖；D3 移除 JSON stdin control；D4 2×2、2b／3 各 100、parity 20、最壞時間公式；D5 0.3 pt；scope 補既存 fixture 共六檔、三點 diff；基準拆程式碼 `81af8f2`／governance `5fc3373`；「差異在 Session」改為「Session 所增加的路徑集合，子機制未定位」。
- rev1（2026-09-06）：初稿。承 B2c rev10 結論與 D5 契約：layer 2b fixture-only differential（不送訊號）、2b-json 獨立 control、layer 3 full-path 分開記錄 `Events()` 關閉與 `Wait()` 返回並做 `kill(-pgid,0)`＋`ps` `Z` 取樣、instrumented control 分離、一次推送。未新增檔案、未 commit。
