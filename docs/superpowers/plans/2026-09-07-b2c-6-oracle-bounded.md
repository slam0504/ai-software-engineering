# B2c-6 測試 oracle 有界化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev2（2026-09-07，design gate 第一輪 CHANGES_REQUIRED 後修訂：D1 核心加可注入時鐘 `now`，fake sleep 同步推進 fake clock，每輪先 probe 再判 deadline、sleep 取 `min(backoff, remaining)`，table test 斷言 1,2,4,…,64,64… 與最後截短值；D2 快照改 `ps -Ao pid=,pgid=,stat=,command=` 於 Go 內依第二欄篩選，命令失敗保留 combined output＋error、無相符列明寫 `<no rows for pgid N>`；D3 evidence 既有 20 ms 外層迴圈整段換成一次 `pollGroupGone`、失敗維持 `t.Errorf`，並明確排除 `proc_cleanup_test.go`；D4 通過；前版：rev1）
> 狀態：**待 owner 極窄複核 rev2 三項修正**。尚未修改任何 code、未委派。
> 票源：Pre-M4 Readiness Backlog **B2c-6**（rev20 新增，**0.3 pt**＝2.5–3.5 hr，owner 2026-09-07 採用）。依 **B2c-4 裁定記錄** rev3 D4：oracle 有界輪詢，**只有 `ESRCH` 算群組消失**；`EPERM` 繼續輪詢並記錄；逾 2 s 仍為 `nil`／`EPERM` 皆失敗並附快照，不把 `EPERM` 偽裝成 gone。
> 基準：`main`＝`origin/main`＝`61c2201`（B2c-5 已落地）。分支 **`b2c6/oracle-bounded`**（本機，自 `61c2201`）。
> 授權邊界：只改四個測試檔的 oracle 與其呼叫點；**不改 production、不改 fixture／`testdata/**`、不放寬 5 秒 combined-completion guard、不在被測路徑加 retry**（register 規則 8）；有界輪詢只作用於 `Wait()` 返回之後的 oracle 取樣。合併 main 需 owner 授權（fast-forward push）。

**Goal:** 把四個一次性 `syscall.Kill(-pgid, 0) != nil` oracle（`internal/proc/proc_test.go:37 groupGone`、`internal/claude/session_test.go:182 groupDead`、`internal/codex/session_test.go:84 srvGroupDead`、`internal/evidence/runner_test.go:106 groupGone`）改為兩平台語意一致的有界輪詢，消除 B2c round 1 的 ubuntu 形狀（`Wait()` 返回瞬間 zombie 殘留使 `kill(-pgid, 0)` 回 0）並停止把 macOS 的 `EPERM`（可能是建立中成員）誤判為已消失。

**Architecture:** 每套件一份相同的 helper（不共用檔、不跨套件 import；註解指向 B2c-4 裁定記錄）：可注入的核心 `pollGroupGone(probe func() error, deadline time.Time, now func() time.Time, sleep func(time.Duration)) (gone bool, epermCount int, last error, seq []string)`——**每輪先 `probe`**：`ESRCH` → gone 立即返回；`nil`／`EPERM`／其他 errno → 記錄；**再判 deadline**（`!now().Before(deadline)` → 返回 not gone）；否則 `sleep(min(backoff, deadline−now()))`，backoff 序列 1／2／4／8／16／32／64 ms 後固定 64 ms。production 以 `time.Now`／`time.Sleep` 注入；table test 以 fake clock 注入，**fake sleep 同步推進 fake clock**（不真的睡、不忙迴圈），並明確斷言 sleep 序列 `1,2,4,8,16,32,64,64,…` 與最後一次不足 64 ms 的截短值。外層 `requireGroupGone(t, pgid, context)` 以 2 s deadline 呼叫核心，失敗時 `t.Fatalf` 附 errno 序列（含 `EPERM` 次數）與快照：**執行 `ps -Ao pid=,pgid=,stat=,command=`，在 Go 內依第二欄 PGID 篩選**（`ps -g` 在 Linux procps 對純數字選的是 session、非 process group，不可用）；命令失敗時附 combined output 與 error；成功但無相符列時明寫 `<no rows for pgid N>`。既有測試的斷言形狀不變，只把 `if !groupGone(pgid) { t.Fatal(...) }` 改為 `requireGroupGone(t, pgid)`（保留原訊息作為 context）。

**Tech Stack／參考文件：** 呼叫點——proc 5 處（`proc_test.go:58,105,128,574,675`）、claude 2 處（`session_test.go:197,211`）、codex 1 處（`session_test.go:140`）、evidence 2 處（`runner_test.go:259,262`，已有 20 ms 輪詢＋2 s deadline，改為 ESRCH-only 語意＋EPERM 記錄）；B2c-4 §3 O1 與 D4；diagnosis-record v2 §7.5 (4)（EPERM 語意勘誤）。

---

## 待 owner 裁定（D1–D4）

- **D1（helper 形狀；rev2 修正）**：核心 `pollGroupGone(probe, deadline, now, sleep)`（可注入時鐘＋sleep；每輪先 probe、再判 deadline、`sleep(min(backoff, remaining))`）與外層 `requireGroupGone(t, pgid, context)` 各一，四套件各一份；既有 `groupGone`／`groupDead`／`srvGroupDead` 名稱移除或改為呼叫外層。table test 四案改為五案：(a) `[EPERM, EPERM, ESRCH]` → gone、`epermCount=2`、sleep 序列 `[1ms, 2ms]`；(b) `[nil…]` 至 deadline（fake clock 2 s）→ not gone，sleep 序列為 `1,2,4,8,16,32,64,64,…` 且**最後一次為截短值**（2000−(1+2+4+8+16+32+64×k) 的餘數）、probe 次數與序列長度一致、無忙迴圈（fake `now` 只由 fake sleep 推進）；(c) `[EPERM…]` 至 deadline → **not gone**、`epermCount` 等於 probe 次數；(d) `[ESRCH]` → gone、0 次 sleep；(e) `[nil, EPERM, EINVAL, ESRCH]` → gone、`seq` 逐字為四個 errno 名稱。是否核准。
- **D2（失敗輸出；rev2 修正）**：`t.Fatalf("%s: process group %d not gone within 2s: errno sequence=%v (EPERM=%d, last=%v); ps rows for pgid:\n%s", context, pgid, seq, epermCount, last, snapshot)`；`snapshot` 來自 `ps -Ao pid=,pgid=,stat=,command=` 的 Go 內篩選（第二欄＝pgid）；`ps` 執行失敗 → `snapshot` 為 `"<ps failed: %v>\n%s"`（error＋combined output）；成功但無相符列 → `"<no rows for pgid N>"`。errno／EPERM／last 格式沿 rev1。是否核准。
- **D3（範圍；rev2 修正）**：(i) evidence `runner_test.go:258-264` 的既有 20 ms 外層迴圈**整段換成一次** `pollGroupGone`（2 s deadline、真實時鐘），不在外層再包一層輪詢；失敗**維持 `t.Errorf`**（附同 D2 的快照），讓後續 `assertNoZombieWorktrees` 繼續執行。(ii) **明確排除 `internal/proc/proc_cleanup_test.go`**：該檔兩個既有迴圈已是 bounded、ESRCH-only，且 cleanup helper 使用 5 s 期限，改接 2 s 的 `requireGroupGone` 會擴大範圍並改變失敗清理契約——本票不動該檔。是否核准。
- **D4（backlog 回寫併入）**：backlog rev21（B2c-5 關票、B2c-6 依賴成立）已在本分支以獨立 docs commit 備妥，與本票一起於 B2c-6 落地時推送；B2c-6 關票再出 rev22。是否核准。

---

## Global Constraints

- 範圍：`internal/proc/proc_test.go`（或新增 `proc_group_oracle_test.go` 放 helper＋單測）、`internal/claude/session_test.go`、`internal/codex/session_test.go`、`internal/evidence/runner_test.go`；`docs/architecture/pre-m4-readiness-backlog.md`（rev21）；本 plan。不改任何非測試檔。
- helper 單測：每套件一個 table test（synthetic errno 序列＋fake clock，五案見 D1）；fake `sleep` 記錄 duration 並推進 fake `now`，不真的睡、不忙迴圈；(b)(c) 必須斷言 sleep 序列與最後截短值。
- 範圍排除：`internal/proc/proc_cleanup_test.go` 不動（D3 (ii)）。
- mutation（proc 套件，各一次並還原）：把 `EPERM` 視為 gone → (c) 紅；把 deadline 改 0 → 真實程序案例在本機仍多半 PASS（macOS 群組通常 <1 ms 消失），僅記錄結果，Linux 重現交 B2c-7（可選）。
- 證據到 `/tmp/b2c6-*`；主 agent 讀碼審查與獨立重跑。每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭。

---

## Task 1: proc 套件（Sonnet 實作；主 agent 審查）

- [ ] **Step 1**：新增 `internal/proc/proc_group_oracle_test.go`：`pollGroupGone(probe, deadline, now, sleep)` 核心、`requireGroupGone(t, pgid, context string)` 外層（含 `ps -Ao` 篩選快照）、table test 五案（D1）；`proc_test.go:37 groupGone` 移除或改為呼叫外層；`proc_test.go` 五個呼叫點改 `requireGroupGone`。**不動 `proc_cleanup_test.go`**（D3 (ii)）。
- [ ] **Step 2 本機控制**：`go test -race ./internal/proc -count=3` PASS；mutation「EPERM 視為 gone」→ (c) 紅（逐字）；`gofmt`／`go vet`。

## Task 2: claude／codex／evidence 套件（Sonnet 實作；主 agent 審查）

- [ ] **Step 1**：三套件各放一份相同核心＋外層（註解指向 B2c-4 D4）與 table test；呼叫點：claude `session_test.go:197,211`（`TestTerminateKillsProcessGroup`、`TestOrphanDoesNotHangNormalExit`——**5 秒 combined-completion guard 與 `t.Fatal("drain/Wait hung on orphan-held pipes")` 形狀不變**）、codex `session_test.go:140`、evidence `runner_test.go:258-264`（整段換成一次 `pollGroupGone`，失敗 `t.Errorf`＋快照，後續 `assertNoZombieWorktrees` 照跑；D3 (i)）。
- [ ] **Step 2 本機控制**：三套件 `-race -count=3` PASS；`TestOrphanDoesNotHangNormalExit` `-count=30` PASS；`gofmt`／`go vet`。

## Task 3: 整合與交付

- [ ] 全套 `go test -race ./... -count=1` PASS（rc 記錄）；主 agent 獨立重跑四個 table test 與 proc mutation；scope 三點 diff 只含四個測試檔＋新 helper 檔＋backlog＋本 plan；production 檔零變更（`git diff --stat origin/main...HEAD -- ':!*_test.go' ':!docs'` 為空）。
- [ ] backlog rev21 commit（B2c-5 關票：commits `b2efb1c`／`b9c74e8`、plan rev3、全套 `-race` rc 0、mutation；B2c-6 依賴成立）備妥；申請 owner 授權一次 fast-forward 推送（tests＋docs）。
- [ ] 落地後：backlog rev22（B2c-6 關票、B2c-7 依賴成立）併入 B2c-7 的 docs。

## 驗證策略

- 單元：四套件 table test；proc mutation。
- 整合：全套 `-race`；focused ×30。
- 無法本機驗證：ubuntu 形狀的消失（本機為 macOS）——交 B2c-7 ×400；EPERM 連續出現的真實情況同上。

## 估點核對

Task 1 約 1.2 hr、Task 2 約 1.0 hr、Task 3 約 0.8 hr → 約 3.0 hr，在 0.3 pt 中位。

## Gate A（B2c-6 完成條件）

- [ ] 四套件 table test PASS、proc mutation (c) 紅並逐字記錄；呼叫點全部改用有界 oracle，斷言形狀不變。
- [ ] 全套 `-race` PASS；production 檔零變更；scope 如上。
- [ ] owner 授權推送並落地。

## 修訂記錄

- rev2（2026-09-07）：design gate 第一輪三項 blocking 修正——D1 核心加 `now` 注入、每輪先 probe 再判 deadline、`sleep(min(backoff, remaining))`、table test 五案含 sleep 序列與截短值斷言；D2 快照改 `ps -Ao pid=,pgid=,stat=,command=` Go 內篩選（`ps -g` 於 Linux 為 session 語意）、失敗與無列的固定格式；D3 evidence 迴圈整段替換＋`t.Errorf`、明確排除 `proc_cleanup_test.go`。D4 通過。
- rev1（2026-09-07）：建立；D1–D4；三 Task；估點核對 3.0 hr。
