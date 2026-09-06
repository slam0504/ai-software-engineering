# B2c-6 測試 oracle 有界化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev1（2026-09-07，design gate 第一輪；待 owner 裁定 D1–D4）
> 狀態：**待 owner design gate**。尚未修改任何 code。
> 票源：Pre-M4 Readiness Backlog **B2c-6**（rev20 新增，**0.3 pt**＝2.5–3.5 hr，owner 2026-09-07 採用）。依 **B2c-4 裁定記錄** rev3 D4：oracle 有界輪詢，**只有 `ESRCH` 算群組消失**；`EPERM` 繼續輪詢並記錄；逾 2 s 仍為 `nil`／`EPERM` 皆失敗並附快照，不把 `EPERM` 偽裝成 gone。
> 基準：`main`＝`origin/main`＝`61c2201`（B2c-5 已落地）。分支 **`b2c6/oracle-bounded`**（本機，自 `61c2201`）。
> 授權邊界：只改四個測試檔的 oracle 與其呼叫點；**不改 production、不改 fixture／`testdata/**`、不放寬 5 秒 combined-completion guard、不在被測路徑加 retry**（register 規則 8）；有界輪詢只作用於 `Wait()` 返回之後的 oracle 取樣。合併 main 需 owner 授權（fast-forward push）。

**Goal:** 把四個一次性 `syscall.Kill(-pgid, 0) != nil` oracle（`internal/proc/proc_test.go:37 groupGone`、`internal/claude/session_test.go:182 groupDead`、`internal/codex/session_test.go:84 srvGroupDead`、`internal/evidence/runner_test.go:106 groupGone`）改為兩平台語意一致的有界輪詢，消除 B2c round 1 的 ubuntu 形狀（`Wait()` 返回瞬間 zombie 殘留使 `kill(-pgid, 0)` 回 0）並停止把 macOS 的 `EPERM`（可能是建立中成員）誤判為已消失。

**Architecture:** 每套件一份相同的 helper（不共用檔、不跨套件 import；註解指向 B2c-4 裁定記錄）：可注入的核心 `pollGroupGone(probe func() error, deadline time.Time, sleep func(time.Duration)) (gone bool, epermCount int, last error, seq []string)`——退避 1／2／4／…／64 ms 後固定 64 ms 直到 deadline；`ESRCH` → gone；`nil`／`EPERM`／其他 errno → 記錄並繼續。外層 `requireGroupGone(t, pgid)` 以 2 s deadline 呼叫核心，失敗時 `t.Fatalf` 附 errno 序列（含 `EPERM` 次數）與最後一次 `ps -o pid,pgid,stat,command -g <pgid>` 快照。既有測試的斷言形狀不變，只把 `if !groupGone(pgid) { t.Fatal(...) }` 改為 `requireGroupGone(t, pgid)`（保留原訊息作為 context）。

**Tech Stack／參考文件：** 呼叫點——proc 5 處（`proc_test.go:58,105,128,574,675`）、claude 2 處（`session_test.go:197,211`）、codex 1 處（`session_test.go:140`）、evidence 2 處（`runner_test.go:259,262`，已有 20 ms 輪詢＋2 s deadline，改為 ESRCH-only 語意＋EPERM 記錄）；B2c-4 §3 O1 與 D4；diagnosis-record v2 §7.5 (4)（EPERM 語意勘誤）。

---

## 待 owner 裁定（D1–D4）

- **D1（helper 形狀）**：如 Architecture；核心函式與外層各一，四套件各一份（名稱沿用各套件既有：`groupGone`／`groupDead`／`srvGroupDead` 改為回 `(bool, []string)` 或直接以 `requireGroupGone(t, pgid)` 取代），退避序列 1→64 ms 倍增後固定 64 ms、deadline 2 s。是否核准。
- **D2（失敗輸出）**：`t.Fatalf("%s: process group %d not gone within 2s: errno sequence=%v (EPERM=%d, last=%v); ps:\n%s", context, pgid, seq, epermCount, last, psSnapshot)`；`ps` 失敗時附其錯誤而非省略。是否核准。
- **D3（evidence `runner_test.go`）**：既有迴圈保留結構、改用同一核心（ESRCH-only、EPERM 記錄、失敗附快照），訊息維持「zero-residue guarantee violated」語意。是否核准。
- **D4（backlog 回寫併入）**：backlog rev21（B2c-5 關票、B2c-6 依賴成立）已在本分支以獨立 docs commit 備妥，與本票一起於 B2c-6 落地時推送；B2c-6 關票再出 rev22。是否核准。

---

## Global Constraints

- 範圍：`internal/proc/proc_test.go`（或新增 `proc_group_oracle_test.go` 放 helper＋單測）、`internal/claude/session_test.go`、`internal/codex/session_test.go`、`internal/evidence/runner_test.go`；`docs/architecture/pre-m4-readiness-backlog.md`（rev21）；本 plan。不改任何非測試檔。
- helper 單測：每套件一個 table test（synthetic errno 序列）：(a) `[EPERM, EPERM, ESRCH]` → gone、`epermCount=2`；(b) `[nil…]` 到 deadline → not gone；(c) `[EPERM…]` 到 deadline → **not gone**（EPERM 不得視為 gone）；(d) `[ESRCH]` → gone、0 次 sleep。`sleep` 注入為記錄用，不真的睡。
- mutation（proc 套件，各一次並還原）：把 `EPERM` 視為 gone → (c) 紅；把 deadline 改 0 → 真實程序案例在本機仍多半 PASS（macOS 群組通常 <1 ms 消失），僅記錄結果，Linux 重現交 B2c-7（可選）。
- 證據到 `/tmp/b2c6-*`；主 agent 讀碼審查與獨立重跑。每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭。

---

## Task 1: proc 套件（Sonnet 實作；主 agent 審查）

- [ ] **Step 1**：新增 `internal/proc/proc_group_oracle_test.go`：`pollGroupGone` 核心、`requireGroupGone(t, pgid, context string)` 外層（含 `ps` 快照）、table test 四案；把 `proc_test.go:37 groupGone` 改為呼叫核心（保留名稱供 `proc_cleanup_test.go` 的 test-local 輪詢對齊——若該檔已有自己的 bounded poll，改為共用本 helper，刪除重複）；五個呼叫點改 `requireGroupGone`。
- [ ] **Step 2 本機控制**：`go test -race ./internal/proc -count=3` PASS；mutation「EPERM 視為 gone」→ (c) 紅（逐字）；`gofmt`／`go vet`。

## Task 2: claude／codex／evidence 套件（Sonnet 實作；主 agent 審查）

- [ ] **Step 1**：三套件各放一份相同核心＋外層（註解指向 B2c-4 D4）與 table test；呼叫點：claude `session_test.go:197,211`（`TestTerminateKillsProcessGroup`、`TestOrphanDoesNotHangNormalExit`——**5 秒 combined-completion guard 與 `t.Fatal("drain/Wait hung on orphan-held pipes")` 形狀不變**）、codex `session_test.go:140`、evidence `runner_test.go:259-263`。
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

- rev1（2026-09-07）：建立；D1–D4；三 Task；估點核對 3.0 hr。
