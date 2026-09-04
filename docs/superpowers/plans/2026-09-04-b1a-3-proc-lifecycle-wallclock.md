# B1a-3 process lifecycle 牆鐘名單處置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev5（2026-09-04，rev4 唯讀 design review APPROVED 後執行**新 Gate A**並回填證據；模板、negative control 與執行範圍未動；前版：rev4、rev3、rev2、rev1）
> 狀態：**新 Gate A 已完成（2026-09-04，綁定已推送的 `bb981c4`）——build／vet／gofmt／`internal/proc` 18 PASS／negative control 2/2（6a、6c）／positive control 6b／兩次 PGID 殘留檢查／三個 hash 時點全數命中 golden。待 owner 裁定後才可進 Gate B implementation**
> 票源：Pre-M4 Readiness Backlog **B1a-3**（`docs/architecture/pre-m4-readiness-backlog.md`，wall-clock 名單 #5／#6；Appendix C **rev12 再重估 0.77 pt**，範圍 0.58–0.96；rev11 的 0.48 pt 已失效）
> 基準 commit：**`bb981c4c214255e45119af7e585a5223e36f3e1a`**（backlog **rev12**，**已推送至 `origin/main`、`git ls-remote` 核實相符**，為固定不變的基準；Gate B 的 range diff 亦以此 SHA 為準）。rev1–rev3 的基準 `a262a60`（rev11）僅適用於已作廢的 rev3 Gate A 證據

**Goal:** 收掉 wall-clock 名單最後兩條 process lifecycle 相關項目，**production 零變更**：**#5** 依 rev11 裁定**不改任何程式碼**，只固化 no-change disposition 與其 preflight 證據；**#6** 修掉**兩個**測試自身的缺陷——**(i)** `deadline` 建立於第一段輪詢之前卻被第二段原封不動重用，導致第二段可能**零次探測**就宣告失敗；**(ii)** pid 檔記錄的是 leader `spawn.sh` 的 PID 而非孫行程，使**第二條明示的孫行程存活 oracle 探測錯對象**，原註解承諾的「兩條分得開」契約失效。

**Architecture:** 兩條的處置**性質完全不同**，不共用工作流。#5 是**調查結論**，交付物是證據與措辭，不是 diff；#6 是**測試自身的邏輯缺陷**，交付物是 `deadline` 重設 ＋ fixture／oracle 改寫，與兩項 negative control ＋ 一項 positive control。兩者唯一的共同點是 production 零變更。**§6.7 與其 B1a-3 專屬例外只治理 #6**——#5 依裁定**不建立 acceptance table**，沒有變異可談，因此 §6.7 原文與例外**兩者都不適用**，不得為求格式一致而替它套一個空例外。

**Tech Stack:** Go（stdlib only，無新依賴）；驗證慣例 `go build`／`go vet`／`gofmt -l`／`go test -race -count=1`，沿用本 repo 既有慣例。

**參考文件：**
- `docs/architecture/pre-m4-readiness-backlog.md`（**rev12：B1a-3 目前的票面範圍、驗收表與估點權威來源，附錄 C 重估、B1a-4 承擔的邊界**；rev11 僅保留 #5 no-change disposition 等歷史裁定，估點與 #6 範圍已被 rev12 取代）
- `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7（mutation 鑑別表 N/N 執行規則、四步完成定義、panic／編譯失敗／`go test -timeout` 不算有效紅燈）、§6.8（錨點與行號慣例）
- `docs/superpowers/plans/2026-09-03-b1a-2-wallclock-determinization.md`（同系列前一票；本 plan 沿用其 header／Global Constraints／negative control／證據轉移契約格式）
- `docs/superpowers/plans/2026-09-03-b1a-1-proc-terminate-seam.md`（同系列首票；golden-hash 轉移規則同源）
- `docs/spikes/m3b-results.md` §7（牆鐘相依測試權威名單；**本票不修改 §7**，見下方邊界）

---

## 兩條各自的處置（preflight 已實測，本票不重新論證）

以下為綁定 `d03922e` 的 preflight 取得的事實。該 preflight **以 read-only 為原則**：不修改主工作區任何檔案；**diagnostic mutation 僅在隔離 worktree 內執行並全數還原**，主工作區零 `.go` 異動。

### #5 `internal/codex/TestAppServerMidStreamDeath`（`internal/codex/session_test.go:25`）——no-change disposition

**裁定（backlog rev11）：不改測試碼、不列 mutation，只記錄 no-change disposition 與本輪查證結果。**

**措辭上限**：本輪的結論是「找不到可修的牆鐘缺陷」，**不是**「§7 把它列進名單是錯的」。後者是更強的主張，本輪證據不足以支撐，任何回報都不得升級到那個強度。

措辭必須精確：preflight **未證實存在可修的牆鐘缺陷**——這**不是**宣稱該測試完全不含牆鐘，而是指本輪找不到一個「改了會讓它更確定性、且不破壞其鑑別力」的修法。rev8 曾記載本條屬「同檔三條」的共用範圍，**該記載已由 rev11 更正**。

兩個候選 mutation 植入點**皆遭否決，不得進 acceptance table**：

| 植入點 | 實測連帶 | 否決理由 |
|---|---|---|
| `Conn.Handshake`（`internal/codex/rpc.go:198`） | **19 條轉紅** | 連帶面過廣，打的是整個套件的共用前置而非本條契約。19 條的組成已由 grep 對齊實測：`rpc_test.go` 9 條（經 `doHandshake` helper）＋ `turns_test.go` 7 條（同一 helper）＋ `session_test.go` 3 條（`Server.Handshake` 委派下來）。`owner_test.go` 走 `stubServer.Handshake` 測試替身，不受影響 |
| `Server.Handshake`（`internal/codex/session.go:46`） | **3 條轉紅**（`TestAppServerMidStreamDeath`／`TestAppServerStderrCaptured`／`TestAppServerTerminateKillsGroup`） | 連帶面雖窄，但只讓測試**在 handshake 前置就失敗**，完全沒有鑑別到本條真正承載的契約：mid-stream death、exit code、`Done()` 關閉、death-after-call |

**兩者共同的硬性理由**：`rpc.go` 與 `session.go` 都是**本票未修改的既有 production code**，對其植入變異違反 §6.7 的 mutation target 規則（變異必須植入本票修改且承載契約的 production code）。

**19／3 清單的定位**：作為 **diagnostic evidence** 留在本 plan，**不進 mutation 分母**，也不得在任何回報中被描述為「本票的 mutation 覆蓋」。

**preflight 過程中的自我更正（誠實記錄）**：初次 subagent 回報「Handshake 連帶 3 條」，實際上它把變異植入 `Conn.Handshake` 卻只跑 `TestAppServer*`，漏掉 9 條走 `doHandshake` 的測試；重跑兩個植入點後得到 19／3 的正確數字。另一次自行構造的 `Server.Handshake` 變異**編譯失敗**（用了 `errors.New` 但 `session.go` 未 import `errors`）——**編譯失敗不是有效紅燈**，已改用 `context.DeadlineExceeded` 重做。由此得出一條本 plan 的硬性要求：**任何變異寫法必須 import-safe**，套用後第一件事是確認能編譯。

### #6 `internal/proc/TestOutputCancellationKillsGrandchildren`（`internal/proc/proc_test.go:143`）——`deadline` 重設 ＋ fixture／oracle 改寫

本條有**兩個**獨立缺陷，rev1–rev3 只涵蓋第一個，第二個是 rev3 Gate A 期間實測發現、經 owner 裁定併入（backlog rev12）。

#### 缺陷一：第二段輪詢重用過期的 `deadline`

**這是一個明確的測試 bug，不是「牆鐘偏好」問題。** 失效時序如下（行號為基準 `a262a60`／`bb981c4` 的現況——兩者 `.go` 內容相同，僅供定位）：

1. `proc_test.go:163` 建立 `deadline := time.Now().Add(20 * time.Second)`。
2. `:165` 起第一段輪詢用它等孫行程的 pid 檔出現。
3. `:175` `cancel()`。
4. `:176-180` 一個 `select` 等 `<-done`，**逾時上限 30 秒**。
5. `:182` 第二段輪詢**原封不動重用同一個 `deadline`**，探測孫行程是否已被 group KILL 收掉。

**根因**：第 4 步的逾時（30s）**大於** deadline 的總預算（20s）。只要 `<-done` 耗時超過 20 秒，第二段輪詢的 `time.Now().Before(deadline)` 首次求值即為 false——**一次都不探測**就落到 `:189` 的 `t.Fatal("忽略 TERM 的孫行程仍存活……")`，宣稱一件它根本沒有量過的事。

**修法**：在第 4 步的 `select` 結束後、第二段輪詢開始前，加入**一行** `deadline = time.Now().Add(20 * time.Second)`。這不放寬任何成功判準——成功判準仍是「孫行程已不存在」（`syscall.Kill(pid, 0)` 回錯）——只是讓第二段拿到屬於自己的預算。

**owner 已實測**：focused `-race -count=5` 五次全綠、整批 5.286s。

#### 缺陷二：pid 檔記錄的是 leader，不是孫行程（rev4 新增，rev1–rev3 未預見）

fixture 為 `( trap '' TERM HUP; echo $$ > <pidFile>; while true; do sleep 0.05; done ) &`。**POSIX `sh` 的 `$$` 在 subshell 內展開為父 shell 的 PID**，不是 subshell 自己的——因此 pid 檔寫入的是 leader `spawn.sh` 的 PID。

**兩項獨立證據（rev3 Gate A 期間取得）**：

1. 臨時診斷輸出 `B1A3-PGID=54018 GC-PID=54018`——**`PID == PGID`**。`internal/proc` 以 `Setpgid` 讓 leader 成為 group leader（其 PGID 等於自身 PID），孫行程雖同組但 PID 必然不同；相等只可能代表該 pid 就是 leader。
2. 獨立小實驗：`( echo $$ ) &` 內的 `$$` 實測輸出與父 shell 的 `$$` 相同（`leader-pid=64208`／`subshell-reports=64208`）。

**影響（rev4 第二輪收斂，不得放大）**：該測試的函式註解明寫「正題斷言（**兩條，分得開**）」——(1) 取消之後 `Output` **會返回**；(2) 那個忽略 TERM 的**孫行程已經不在**。**失效的是第 (2) 條**：它探測的是 leader（leader 同樣 `trap '' TERM HUP`，所以看起來一直是綠的），無法獨立證明孫行程被 group KILL 收掉——兩條因此**不再分得開**。

**不得宣稱整條測試會假綠**（本輪明確更正 rev3 的過強判定）。孫行程繼承 stdout，而：

- `Output` 先 `io.ReadAll(p.Stdout)`、之後才 `p.Wait()`（`internal/proc/proc.go`，符號 `Output`）；
- stdout 是父程序**自建的 `os.Pipe()`**，須**所有 write end 關閉**才會取得 EOF（`Start` 內建立 pipe 處的既有註解已寫明此設計意圖）。

因此孫行程若存活，`Output` 會卡在讀取、`<-done` 不會觸發，測試仍會在 30 秒後紅在第 (1) 條「取消之後 Output 沒有返回——孫行程持有的 pipe 把它卡住了」。**漏殺目前仍由第 (1) 條間接偵測**；本票要恢復的是第 (2) 條的獨立鑑別力，不是補上一個完全不存在的偵測。

**owner 裁定（backlog rev12）：併入本票**。理由——這不是鄰近改善，而是 #6 明文承諾的「兩條分得開」契約失效；只修 `deadline` 會讓第 (2) 條 oracle **更穩定地驗錯對象**，無法誠實關票。不放進 B1a-4（該票承接整合負載／§7／living／closure review，不承接測試實作）；不只記錄缺口（會讓名稱、註解與實際 oracle 長期矛盾）；不另開票（同一條 #6、同一測試檔、純測試修改，無 owner 或性質混合，拆票無收益）。

---

## Production 零變更聲明

本票**不新增、不修改任何 production 程式碼**，也**不修改 `internal/codex` 的任何檔案**（#5 是 no-change disposition）。

| 檔案 | 性質 | 本票動作 |
|---|---|---|
| `internal/proc/proc_test.go` | 測試 | 修改（Task 2 Step 1–3：`deadline` 重設 ＋ fixture `$!` ＋ oracle 斷言）；Task 3 的 6a／6b／6c 與臨時 `t.Logf` 期間暫時再改，事後 byte-identical 還原 |
| `internal/codex/session_test.go`、`internal/codex/rpc.go`、`internal/codex/session.go` | 測試／production | **零變更**（#5 的裁定就是不改） |
| `internal/proc/proc.go` | production | **零變更** |

**每個 implementation commit 前必須以 `git diff --name-only HEAD` 核對工作區**，出現任何非 `internal/proc/proc_test.go` 的檔案即停工回報，不得自行新增 production seam，也不得順手「補」#5。這是 per-commit 的工作區檢查，與 Gate B 的 range diff 是不同的兩層，兩層都要做。

---

## Global Constraints

- Module：`github.com/slam0504/sdlc-workbench`。
- **受影響 package 只有一個：`internal/proc`。**
  - Task 2 是 package-scoped：`go build ./internal/proc/...`、`go vet ./internal/proc/...`、`gofmt -l internal/proc`、`go test -race ./internal/proc/... -count=1 -v`。
  - **完整 `go build ./...`／`go vet ./...`／`gofmt -l .` 由 Gate A 統一執行一次**，不由各 task 重複。
  - **所有 Gate A 驗證一律在隔離 worktree 內執行**，主工作區全程零異動。
- **Task 2 結尾的「然後 commit」屬於 Gate B**（主 repo 的 implementation 階段；**Task 1 不產生 implementation commit**，其交付物是本 plan 內的證據文字）。Gate A 期間只在隔離 worktree 內套用模板與跑驗證，**不 commit**。
- **落地驗證環境：隔離 worktree ＋ 從主 repo 複製 `frontend/dist`**（沿用 B1a-2 定案）。理由：root `main.go` 有 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore` 忽略、零追蹤檔，`git worktree add` 不會帶過去，Gate A 的 `go build ./...` 會失敗於 `pattern all:frontend/dist: no matching files found`。**本票的受影響 package 是 `internal/proc`，不受此限**——複製僅為了讓 Gate A 的全量 `go build ./...`／`go vet ./...` 能跑完。複製來源與複製後的 `go build ./...` exit code 須記入證據段落。
- **本票不修改 `docs/spikes/m3b-results.md` §7、不建立 living 有效名單文件。** #5 的 no-change disposition 與 #6 的 resolved 證據由本 plan 的證據段落承載，**§7 追加與 living 文件一律由 B1a-4 執行**（backlog rev11 明定）。
- **本票只做 `internal/proc` 的 focused 與全包回歸；B1a-4 的五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）本票不做、也不得宣稱完成。** 注意該矩陣**包含 `internal/proc`**——B1a-4 會在整合 HEAD 再跑一次，**不以本票的結果取代**。
- 無新外部依賴；一律 stdlib。
- **時間常數不得成為成功判準**：#6 修改後，20 秒仍只扮演「卡死時終於失敗」的角色。任何把「跑得夠快」寫成通過條件的斷言一律不加。
- **`go test -timeout` 撞牆不是有效紅燈**：Task 3 的 negative control 必須紅在測試自身的 `t.Fatal`／`t.Fatalf`，逐字抄錄失敗訊息為證。
- **變異寫法必須 import-safe**：套用後第一步確認能編譯；**編譯失敗不算有效紅燈**（preflight 已踩過一次，見 #5 段落）。

---

## Task 1: #5 — 固化 no-change disposition（零程式碼變更）

**本 task 不建立 acceptance table**：#5 沒有變異、沒有分母，§6.7 原文與 Task 3 的 B1a-3 專屬例外**對它都不適用**。

- [ ] **Step 1: 確認 `internal/codex` 工作區零異動**

`git status --porcelain internal/codex` 必須為空。本 task **不產生任何 `.go` diff**。

- [ ] **Step 2: 把下列三項寫入本 plan 的「Design-gate 證據」段落**

1. **裁定措辭**（逐字，不得改寫）：「preflight **未證實存在可修的牆鐘缺陷**（非宣稱它完全不含牆鐘）」。
2. **兩個植入點的否決理由**與 19／3 連帶數字，明標為 **diagnostic evidence、不進 mutation 分母**。
3. **rev8「同檔三條」記載已由 rev11 更正**的事實。

- [ ] **Step 3: 交接註記**

明寫「#5 **不存在** implementation commit 與 resolved commit，不得為湊格式而虛構」，並註明 §7 的 no-change disposition 追加由 **B1a-4** 執行。

**本 task 不 commit**——它的交付物是 plan 內的證據文字，隨 plan commit 一起落地。

---

## Task 2: `internal/proc` — #6 `deadline` 重設 ＋ fixture／oracle 改寫

- [ ] **Step 1: 插入 `deadline` 重設 ＋ 一段說明註解**

在 `TestOutputCancellationKillsGrandchildren` 內，等待 `Output` 返回的 `select` 區塊**結束之後**、既有註解 `// 孫行程必須已經被 group KILL 收掉（signal 0 ＝ 存活探測）。` **之前**插入：

```go
	// 第二段輪詢必須拿到屬於自己的預算。上面的 deadline 建立於第一段輪詢之前，
	// 而中間那個 select 的逾時上限（30s）大於 deadline 的總預算（20s）——若
	// <-done 耗時超過 20s，下面的迴圈條件首次求值就是 false，一次都不探測便落到
	// t.Fatal，宣稱一件它沒有量過的事。重設後成功判準不變（孫行程已不存在）。
	deadline = time.Now().Add(20 * time.Second)
```

**不改 `deadline` 的型別或初值**（維持 `:=` 建立、此處 `=` 重設）。

- [ ] **Step 2: fixture 改由父 shell 寫入 `$!`**

把 `script` 改成由**父 shell** 記錄背景 subshell 的 PID：

```go
	// child：spawn 一個忽略 TERM/HUP 的孫行程（繼承 stdout），自己也不退出。
	//
	// pid 必須由父 shell 以 $! 寫入：POSIX sh 的 $$ 在 subshell 內展開為
	// 父 shell 的 PID，先前寫法記錄到的是 leader 自己，測試因此反覆探測
	// 錯誤對象（leader 同樣 trap TERM/HUP，所以看起來一直是綠的）。
	script := "#!/bin/sh\n" +
		"( trap '' TERM HUP; while true; do sleep 0.05; done ) &\n" +
		"echo $! > " + pidFile + "\n" +
		"trap '' TERM HUP\n" +
		"while true; do sleep 0.05; done\n"
```

- [ ] **Step 3: 新增 oracle 斷言——pid 必須不是 group leader**

在既有的 `if pid == 0 { … }` 區塊**之後**，趁孫行程仍存活時取 PGID，但**不在此處失敗**：

```go
	// 趁孫行程還活著先取 PGID；斷言延後到 Output 收斂之後才發（見下），
	// 否則 fixture 契約一旦失敗就會在取消之前 t.Fatal，留下整組程序。
	pgid, pgidErr := syscall.Getpgid(pid)
```

在等待 `Output` 返回的 `select` 之後、`deadline` 重設**之前**發出斷言：

```go
	// fixture 契約：pid 檔必須記錄孫行程，不是 group leader。leader 由
	// Setpgid 成為 group leader（PGID 等於自身 PID），孫行程同組但 PID 必不同，
	// 因此 pid == pgid 即代表記錄錯了對象——第二段輪詢會探測 leader 而非孫行程。
	if pgidErr != nil {
		t.Fatalf("測試前提不成立：取不到 pid %d 的 PGID：%v", pid, pgidErr)
	}
	if pid == pgid {
		t.Fatalf("測試前提不成立：pid 檔記錄到 group leader、不是孫行程（pid=%d == pgid=%d）", pid, pgid)
	}
```

**為什麼延後**：此時 `cancel()` 已執行、`<-done` 已收斂，`Output` 內部的 group 收尾已完成，契約失敗時不會留下程序。這正是 owner 指定的順序。

**其餘一律不動**：兩段輪詢的 20ms 間隔不動、`select` 的 30 秒不動、`TermGrace: 300 * time.Millisecond` 不動、第二段輪詢的既有 `t.Fatal` 訊息與清理 `syscall.Kill(pid, syscall.SIGKILL)` 不動。

- [ ] **Step 4: 驗證（package-scoped）**

`go build ./internal/proc/...`、`go vet ./internal/proc/...`、`gofmt -l internal/proc`、`go test -race ./internal/proc/... -count=1 -v`。

基準：`internal/proc` 現有 **18 條頂層測試**（以 `go test -list '^Test' ./internal/proc` 實際編譯列出為準），本票不增減條數，預期 18 條全 PASS。另跑 focused `go test -race -run '^TestOutputCancellationKillsGrandchildren$' ./internal/proc -count=5 -v` 並記錄耗時（owner 已實測整批 5.286s，可作對照）。

- [ ] **Step 5: `git diff --name-only` 確認只有 `internal/proc/proc_test.go`**，然後 commit（Gate B 階段）。

---

## Task 3: Negative／positive control ＋ 回歸 ＋ 兩道 gate（**只治理 #6**）

### §6.7 的 B1a-3 專屬例外（owner 核准，必讀）——**適用範圍僅 #6**

**先釐清適用範圍**：本節與其例外**只治理 #6**。**#5 不在本節範圍內**——它依裁定不建立 acceptance table，沒有變異、沒有分母，§6.7 原文與本例外對它**都不適用**。

`docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7 原文要求變異必須植入**本票修改且承載契約的 production code**。**本票無法符合這條原文**——B1a-3 的既定邊界就是 production 零變更，沒有任何 production code 可植入；而既有 production code（`rpc.go`／`session.go`／`proc.go`）本票並未修改，對其植入正是 §6.7 明文禁止的。

owner 因此核准一個**僅限 B1a-3** 的例外：以**測試側 negative control** 代替 production mutation，但 **N/N 全跑、hash 前後比對、byte-identical 還原、完整回歸強度**四項要求一併保留，不打折。

**措辭紀律**：本票的驗收證據**不得**寫成「符合 §6.7 的 production-target 規則」，只能寫成「依 owner 核准的 B1a-3 專屬例外，以測試側 negative control 執行，強度要求未降低」。後續 rev、commit message、backlog 狀態更新一律照此措辭。

### 分母：2/2（6a、6c）

**6a 與 6c 都必須由各自指定的斷言轉為失敗**，缺一即本票未通過。**6b 不在分母內**——它是 6a 的 **positive control**。**#5 的兩個 Handshake 植入點一律不進本表**（見上方裁定）。

**兩項各打一個缺陷、不可互相替代**：6a 打的是 `deadline` 重設（缺陷一），6c 打的是 fixture／oracle（缺陷二）。**單獨跑 6a 無法證明 oracle 改寫有意義，單獨跑 6c 也無法證明重設有意義。**

每一項變異都必須完成以下四步並留下證據，缺任一步即該項未通過：

1. **套用**：被改檔案的 `shasum -a 256` 在套用前後不同，且**套用後能編譯**（`go vet ./internal/proc/...` 通過）。
2. **轉紅**：指定測試確實 FAIL，且**紅在正題**——失敗訊息是該測試自己的 `t.Fatal`，不是撞到 `go test -timeout`、panic 或編譯失敗。
3. **還原**：還原後檔案 hash 與變異前 **byte-identical**，且該值**等於 Gate A checklist 先行建立的 golden SHA-256**（見 Gate A 的 ★ 項；golden 必須在任何變異之前就已固定）。
4. **轉綠**：還原後，focused ＋ `internal/proc` 全包回綠。

- [ ] **Step 1: negative control 6a（分母內，必跑）**

| # | 變異（植入處） | 必須轉紅的斷言（測試自身的 `t.Fatal`） | 對應測試 | 連帶範圍（須實跑記錄） |
|---|---|---|---|---|
| 6a | `internal/proc/proc_test.go`：**移除 Task 2 Step 1 插入的 `deadline = time.Now().Add(20 * time.Second)` 那一行**，並在**原位置之前**加入 `time.Sleep(21 * time.Second)` | `忽略 TERM 的孫行程仍存活——取消必須走 process group（TERM → bounded KILL）` | `TestOutputCancellationKillsGrandchildren` | `internal/proc` |
| 6c | `internal/proc/proc_test.go`：把 Task 2 Step 2 的 fixture **退回舊寫法**——刪掉父 shell 的 `echo $! > <pidFile>`，改回在 subshell 內 `echo $$ > <pidFile>`（其餘不動） | `測試前提不成立：pid 檔記錄到 group leader、不是孫行程（pid=%d == pgid=%d）`（Task 2 Step 3 新增的**專用斷言**） | `TestOutputCancellationKillsGrandchildren` | `internal/proc` |

**6a 證明的是什麼**：它把「第二段輪詢零次探測」這個原本只在慢環境下偶發的失效，變成**確定性可重現**。睡滿 21 秒後 `time.Now().Before(deadline)` 首次求值即為 false，迴圈一次都不跑，直接落到既有的 `t.Fatal`——這正是修正前的 bug 形狀。預期耗時約 21–22 秒。

**為什麼是 21 秒而不是 19.9 秒**（owner 裁定）：21 秒**刻意大於**舊 deadline 的 20 秒總預算，避免落在邊界上、避免結果隨機器速度擺盪。19.9 秒那類貼邊寫法會讓 negative control 自己變成一條牆鐘測試。

**6c 證明的是什麼**：它證明 Task 2 的 fixture 改寫**確實改變了測試探測的對象**。退回 `$$` 後 pid 檔又記錄到 leader，`pid == pgid` 成立，新增的專用斷言如實轉紅。**沒有 6c，「改成 `$!`」就只是一段沒有 oracle 保護的字串修改**——日後有人改回去也不會有任何測試失敗。

**6c 必須紅在專用斷言、不得紅在第二段輪詢**：若 6c 紅在 `忽略 TERM 的孫行程仍存活…`，代表新增的斷言沒有生效或位置不對，該項未通過。逐字抄錄失敗訊息以區分兩者。

**6c 的失敗時點**：新增的斷言位於 `cancel()` 與 `<-done` 收斂**之後**，因此 6c 的紅燈路徑**結構上不留下程序**（見 Step 3 的殘留檢查）。

**明確排除、不列入本表**：`Conn.Handshake` 與 `Server.Handshake` 的兩個植入點（理由見 #5 段落）。**不得**以「連帶只有 3 條、範圍夠窄」為由把 `Server.Handshake` 補回分母。

- [ ] **Step 2: positive control 6b（必跑，不計入分母）**

| 項目 | 內容 |
|---|---|
| 設定 | **保留 6a 的 `time.Sleep(21 * time.Second)`**，同時**恢復** Task 2 的 `deadline = time.Now().Add(20 * time.Second)` 那一行 |
| 預期 | **PASS**（重設後第二段拿到新的 20 秒預算；孫行程此時早已被 group KILL 收掉，首次探測即返回）。耗時約 21–22 秒 |
| 證明什麼 | 6a 的紅燈確實來自**缺少 deadline 重設**，而不是那段 21 秒延遲本身把測試弄壞。**單獨跑 6a 證不出這一行有意義**，必須成對 |
| 記錄 | 實際耗時、PASS 結果，與 6a 的失敗訊息並列 |

`go test -timeout` 撞牆在 6a／6b／6c 任何一項都**不算**有效證據；6b 若因 package timeout 失敗，代表環境異常，須重跑而非記為證據。

- [ ] **Step 3: 6a／6c 的程序殘留檢查——以 PGID 精確比對（失敗路徑專屬）**

6a 與 6c 是本票僅有的兩項刻意走測試失敗路徑的項目，各自都必須證明沒有殘留 fake 程序（該測試刻意 spawn 一個 `trap '' TERM HUP` 的孫行程）。

**為什麼不用字串計數**：先落檔再 grep 只解決了「grep 程序尚未啟動」這一半；外層 `zsh -lc` 之類的 wrapper 命令列仍可能帶著待比對字串而被快照收進去，全機器搜尋 `sleep 0.05` 更會比中無關工作。**全機器的字串計數一律不作為主要證據**（B1a-1 已實際踩過自我比中誤報 1 的坑）。

**指定方式——以本次測試的 process group 精確比對**：

1. **取得 PGID**。Task 2 Step 3 之後，`pgid` 已是測試本體的變數，不再需要 rev3 那個帶錯誤處理的臨時診斷區塊（fail-loud 已內建於 Task 2 的 `pgidErr` 斷言）：

   - **6c**：**不需要任何臨時碼**——它的失敗訊息本身就印出 `pid` 與 `pgid`（`… pid=%d == pgid=%d`），直接取用。
   - **6a**：於 Task 2 Step 3 的兩個斷言**之後**暫時加入**一行**：

     ```go
     t.Logf("B1A3-PGID=%d GC-PID=%d", pgid, pid)
     ```

     跑 6a 時用 `-v` 取得該值（`t.Logf` 在 FAIL 的測試一定會輸出）。**縮排注意**：貼入 `proc_test.go` 時改回該檔的 tab 縮排；本 plan 內以空白呈現，是為了讓巢狀 fenced block 通過 `git diff --check` 的 `space before tab in indent`。

2. **另一次獨立的工具呼叫**產生含 PGID 欄的程序快照（與步驟 3 分開，不寫在同一條命令列）：

   ```sh
   ps -eo pgid=,pid=,ppid=,args= > /tmp/b1a3-ps-after-<6a|6c>.txt
   ```

3. 以**第一欄 PGID 精確比對**該數值，不做任何字串比對：

   ```sh
   awk -v g=<步驟 1 取得的 PGID> '$1 == g' /tmp/b1a3-ps-after-<6a|6c>.txt
   ```

   **預期輸出為空**。比對用的 `awk` 自身屬於當前 shell 的 process group，與待測 PGID 不同，結構上不可能自我比中。

4. 移除 6a 的臨時 `t.Logf`，並確認 `internal/proc/proc_test.go` 的 `shasum -a 256` **回到 Gate A 記錄的 golden hash**——臨時記錄與 6a／6c 的變異全部都必須完全還原。

**兩份**快照檔、**兩次** `awk` 的空輸出、還原後的 hash 一併留為證據。

- [ ] **Step 4: 既有回歸清單——零回歸**

| package | 頂層測試條數（基準 `bb981c4`／`a262a60`，兩者 `.go` 內容相同） | 本票預期 |
|---|---|---|
| `internal/proc` | 18 | 18 PASS，條數不變 |

條數以 `go test -list '^Test' ./internal/proc` 實際編譯列出為準。其中須特別確認未被波及的同檔鄰居：`TestOutputCancellationKillsGrandchildren` 之外所有使用 `cancel()`／`SignalGroup`／`SIGKILL` 的測試（`proc_test.go` 內另有多處，含死因仲裁表格測試）——它們與本票的一行修改無共享狀態，但仍須以實跑結果確認，不得只靠推理。

**`internal/codex` 不列入回歸清單**：#5 零變更，本票未觸及該套件；若要宣稱其綠燈須另跑並標明來源，**不得**把 B1a-1 或 preflight 的舊結果當成本票證據。

- [ ] **Step 5: 記錄 6a 與 6c 的實際連帶清單**（各自實跑取得，不得只靠 grep 推測），寫入下方「Design-gate 證據」段落。

---

### Gate A：design-gate 落地驗證完成條件（在隔離 worktree 執行）

全部在 `~/scratch-worktrees/b1a3-<n>` 內完成，主工作區零異動：

- [ ] worktree 自基準 **`bb981c4`（rev12）** 建立後第一步：`cp -R <主 repo>/frontend/dist <worktree>/frontend/`，並記錄 `go build ./...` exit code。
- [ ] `internal/proc/proc_test.go` 按 Task 2 的 **Step 1–3 三段模板**逐字套用（`deadline` 重設 ＋ fixture `$!` ＋ oracle 斷言），`gofmt -l` 乾淨。
- [ ] **統一執行一次**：`go build ./...`、`go vet ./...`、`gofmt -l .` 乾淨；`go test -race ./internal/proc/... -count=1 -v` 全綠，條數與上表相符。
- [ ] focused `-race -count=5` 全綠，耗時已記錄。
- [ ] **★ golden SHA-256 於此時建立**（`internal/proc/proc_test.go`）——**時點硬性規定：Task 2 三段模板全部套用 ＋ `gofmt` ＋ baseline 全綠之後、任何 6a／6b／6c／臨時 `t.Logf` 套用之前**。此刻檔案內容就是本票要交付的最終形態，不含任何變異或臨時記錄。hash 立即寫入證據段落。**不得**在 6a／6b 還原之後才「補記」golden——那是循環定義，會讓混入的臨時內容被追認為 golden（B1a-2 曾需由 owner 從基準獨立重建才排除此風險）。
- [ ] negative control **2/2（6a、6c）** 各自四步齊備，逐字抄錄失敗訊息與耗時；**確認 6c 紅在專用斷言而非第二段輪詢**。
- [ ] positive control **6b** 已跑且 PASS，耗時已記錄。
- [ ] 6a **與 6c** 的程序殘留檢查：兩次 `awk` 的 PGID 精確比對輸出均為空，兩份快照檔留存。
- [ ] **三個時點的 hash 全部命中同一個 golden 值**：(i) 第一項變異套用**前**、(ii) 6a／6b／6c 與臨時 `t.Logf` **全部還原後**、(iii) Gate A 結束時的**最終**檔案。任一不符即 Gate A 未通過，須自基準重建後整套重跑。
- [ ] Task 1 的三項 #5 證據文字已寫入證據段落。
- [ ] worktree 移除、`git worktree list` 只剩主 repo。

### 證據轉移契約（Gate A → Gate B）

Gate A 在隔離 worktree 執行，正式 implementation 稍後才落到 main。**沒有 byte-level 綁定就不能證明兩邊是同一套測試與變異定義**，因此 Gate A 的證據只有在以下**四項全部成立**時才可轉移到 main 的 implementation：

1. **測試檔 hash 相符**：main 上 implementation commit 後的 `internal/proc/proc_test.go` SHA-256，與 Gate A 記錄的 golden hash 相同。
2. **變異內容與預期失敗訊息不變**：本 plan 的 negative control 表格（**6a 與 6c** 的植入內容與必須轉紅的斷言字串）自 Gate A 後未被修改。
3. **驗收斷言模板不變**：Task 2 的 **Step 1–3 三段**程式模板（含註解文字與斷言訊息字串）自 Gate A 後未被修改。
4. **執行套件與測試範圍沒有擴張**：仍為 `internal/proc` 單一套件，未增減。

**任一項不符即整套重跑，不做部分轉移。** 此契約與 B1a-1／B1a-2 採用的 golden-hash 轉移規則同源；本票的比對對象是**單一測試檔**。

### Gate B：implementation 完成 gate（在主 repo 執行）

- [ ] **一個 implementation commit** 已落在主 repo（plan commit 不計入）。
- [ ] `internal/proc/proc_test.go` hash 與**本輪重跑的** Gate A golden hash 相符（轉移契約第 1 項）；契約第 2–4 項逐項確認並記錄。**rev3 的 golden `9c31e9fe…` 已作廢，不得用作比對基準。**
- [ ] `go build ./...`、`go vet ./...`、`go test -race ./internal/proc/... -count=1` 全綠。
- [ ] `gofmt -l` 與 `git diff --check` 乾淨。
- [ ] **範圍檢查拆成兩個可重現的 range diff**（plan 會先 commit、implementation commit 在後，因此單一「對 `bb981c4` 只含一個測試檔」必然失敗）：
  - `git diff --name-only <plan-commit>..HEAD` → **只含 `internal/proc/proc_test.go`**。
  - `git diff --name-only bb981c4..HEAD` → **只含本 plan 文件 ＋ `internal/proc/proc_test.go`**（`bb981c4` ＝ backlog rev12，已推送且固定，不再改綁）。
  - 兩條指令的完整輸出都要留為證據。
  - **`<plan-commit>` 於 Gate B 執行時現場取得，不回寫進已提交的 plan**（回填會讓 `bb981c4..HEAD` 的 range diff 多出一次 plan 異動）。取得方式：

    ```sh
    git log --reverse --format=%H bb981c4..HEAD -- \
      docs/superpowers/plans/2026-09-04-b1a-3-proc-lifecycle-wallclock.md |
      head -1
    ```

- [ ] **`internal/codex` 零異動**已由上述 range diff 機械確認（#5 的 no-change disposition 成立的形式證據）。
- [ ] **五套件整合負載矩陣、§7 追加、living 有效名單明確不在本票範圍，屬 B1a-4，不得宣稱完成。**

---

## Design-gate 證據

### ⚠️ rev3 Gate A 執行結果——**已作廢，僅保留為診斷歷史，證據不得轉移**

**作廢理由（owner 裁定，backlog rev12）**：本輪 Gate A 是在「#6 只需一行 `deadline` 重設」的前提下執行的。Gate A 期間發現 #6 的核心 oracle 失效並經 owner 裁定併入本票後，**測試模板、golden hash、oracle 與 mutation 定義全部改變**——rev4 的 Task 2 由一段模板擴為三段，negative control 由 1/1 擴為 2/2。因此 **golden `9c31e9fe…` 已失效**，下列所有結果**不得**依證據轉移契約帶入新的 Gate A，**必須完整重跑**，也不做部分轉移。

保留本段的唯一用途：記錄 oracle 失效是**如何被發現的**，以及當時的量測數字（供 rev12 重估追溯）。

#### （診斷歷史）2026-09-04，隔離 worktree `~/scratch-worktrees/b1a3-1`，基準 `a262a60`

**環境**：`git worktree add --detach ~/scratch-worktrees/b1a3-1 a262a60`，worktree HEAD 核對為 `a262a606e9ee4f88ee261e22d4123824d413ef96`。第一步 `cp -R <主 repo>/frontend/dist <worktree>/frontend/`，其後 `go build ./...` **exit 0**。主工作區全程零 `.go` 異動（僅本 plan 一個 untracked 檔）。

**模板套用**：Task 2 的五行（四行註解 ＋ 一行 `deadline = time.Now().Add(20 * time.Second)`）逐字套用，插在 `select` 區塊之後、`// 孫行程必須已經被 group KILL 收掉` 註解之前。`gofmt -l internal/proc` 空輸出。`git diff --stat` = `1 file changed, 5 insertions(+)`。

**統一驗證（各執行一次）**：

| 項目 | 結果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0，輸出 0 行 |
| `gofmt -l .` | 空輸出 |
| `go test -list '^Test' ./internal/proc` | **18** 條，與基準相符 |
| `go test -race ./internal/proc/... -count=1 -v` | **18 PASS／0 FAIL／0 SKIP**，`ok … 13.665s` |
| focused `-race -count=5` | 5/5 PASS（0.92／0.92／0.88／0.88／0.87s），`ok … 6.170s` |

**★ golden SHA-256（於此時點建立——模板套用＋`gofmt`＋baseline 全綠之後、任何 6a／6b／臨時診斷區塊之前）**：

```
internal/proc/proc_test.go   9c31e9fe75bbdc3cad359cc7dfbd87e22d2246b65f490323709ac21d77dc2277
```

**negative control 6a（分母 1/1）**——四步齊備：

| 步驟 | 證據 |
|---|---|
| 套用 | 套用前 hash `9c31e9fe…`（＝golden）→ 套用後 `28298a51e52cc905ba5c9554d8f54f6427753eb40f551361cdaf34b40ce20f4e`，兩者不同 |
| 編譯 | `go vet ./internal/proc/...` **exit 0**（import-safe，非編譯失敗紅燈） |
| 轉紅 | `--- FAIL: TestOutputCancellationKillsGrandchildren (21.76s)`，失敗訊息逐字為 `proc_test.go:206: 忽略 TERM 的孫行程仍存活——取消必須走 process group（TERM → bounded KILL）`。**紅在測試自身的 `t.Fatal`**，非 `go test -timeout`、非 panic、非編譯失敗 |
| 還原／轉綠 | 見下方「還原」列 |

**位置註記**：6a 的 `time.Sleep(21 * time.Second)` 取代了 Task 2 重設行的**同一個位置**（重設行已移除），與 owner 裁定的「移除重設行、在原位置前加入 sleep」語意等價——兩者都讓 21 秒延遲落在 `select` 之後、第二段輪詢之前。

**positive control 6b（不計入分母）**：配置為 golden ＋ 在重設行**之前**插入 `time.Sleep(21 * time.Second)`（hash `7203f161f54c7b315f1e325913ac300f610188fa405b60e68f657da6cd2fdea7`，`gofmt` 乾淨）。結果 `--- PASS: TestOutputCancellationKillsGrandchildren (22.11s)`，`ok … 23.884s`。**與 6a 成對成立**：同樣的 21 秒延遲下，有重設行即 PASS、無重設行即 FAIL——紅燈確實來自缺少重設，而非延遲本身把測試弄壞。

**6a 的程序殘留檢查（PGID 精確比對）**：臨時診斷區塊取得 `proc_test.go:185: B1A3-PGID=54018 GC-PID=54018`（FAIL 那一輪的 `-v` 輸出）→ **另一次獨立工具呼叫**產生 `ps -eo pgid=,pid=,ppid=,args= > /tmp/b1a3-ps-after-6a.txt`（830 行）→ **再一次獨立呼叫** `awk -v g=54018 '$1 == g' /tmp/b1a3-ps-after-6a.txt` → **空輸出（matched lines: 0）**。該 process group 已無存活程序。

**還原與三個 hash 時點**：6a／6b 與臨時診斷區塊全部移除後，檔案自基準 `a262a60` 重新套用 Task 2 模板產生，`shasum -a 256` = **`9c31e9fe…`**，與 golden 逐字相符。

| 時點 | hash | 是否命中 golden |
|---|---|---|
| (i) 6a 套用前 | `9c31e9fe…` | ✅ |
| (ii) 6a／6b 與臨時區塊全部還原後 | `9c31e9fe…` | ✅ |
| (iii) Gate A 結束時的最終檔案 | `9c31e9fe…` | ✅ |

還原後 `gofmt -l .` 乾淨、focused `ok … 2.262s`、`internal/proc` 全包 **18 PASS**（`ok … 11.351s`）。

**6a 的實際連帶清單**：僅 `TestOutputCancellationKillsGrandchildren` 一條轉紅；同包其餘 17 條在 6a 期間未受影響（還原後全包 18 PASS 已覆核）。

**收尾**：`git worktree remove --force` 後 `git worktree list` 只剩主 repo；主工作區 `git status --porcelain` 僅本 plan 一個 untracked 檔，`HEAD` 仍為 `a262a60`。

### 新 Gate A 執行結果（2026-09-04，隔離 worktree `~/scratch-worktrees/b1a3-2`，基準 `bb981c4`）——已完成

**環境**：`git worktree add --detach ~/scratch-worktrees/b1a3-2 bb981c4`，worktree HEAD 核對為 `bb981c4c214255e45119af7e585a5223e36f3e1a`。第一步 `cp -R <主 repo>/frontend/dist <worktree>/frontend/`，其後 `go build ./...` **exit 0**。主工作區全程零 `.go` 異動（僅本 plan 一個 untracked 檔）。

**模板套用**：Task 2 Step 1–3 三段逐字套用（`deadline` 重設 ＋ fixture `$!` ＋ oracle 斷言），`gofmt -l internal/proc` 空輸出，`git diff --stat` = `1 file changed, 23 insertions(+), 1 deletion(-)`。

**統一驗證（各執行一次）**：

| 項目 | 結果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0，輸出 0 行 |
| `gofmt -l .` | 空輸出 |
| `go test -list '^Test' ./internal/proc` | **18** 條，與基準相符 |
| `go test -race ./internal/proc/... -count=1 -v` | **18 PASS／0 FAIL／0 SKIP**，`ok … 12.104s` |
| focused `-race -count=5` | 5/5 PASS（0.82／0.87／0.89／0.80／0.86s），`ok … 5.861s` |

**★ golden SHA-256（於指定時點建立——三段模板套用＋`gofmt`＋baseline 全綠之後、任何 6a／6b／6c／臨時 `t.Logf` 之前）**：

```
internal/proc/proc_test.go   7cf94870204d8789ba3c7fe687678f00ff35a2147a9fb618caa0c12deeff1afa
```

（rev3 的 `9c31e9fe…` 已作廢，兩者不得混用。）

**negative control 2/2**——各自四步齊備：

| # | 套用前／後 hash | 編譯 | 轉紅證據（逐字） | 耗時 |
|---|---|---|---|---|
| 6a | `7cf94870…` → `ecdbebbb646269d9f9a29b5ea4f6961971d1229e0c7f171697308661db539ae3` | `go vet ./internal/proc/...` exit 0 | `proc_test.go:212: 忽略 TERM 的孫行程仍存活——取消必須走 process group（TERM → bounded KILL）` | FAIL 21.80s |
| 6c | `7cf94870…` → `8ea96df04baec4d115915047c916ac11124abcfd29ed0334d14487f20c20296f` | `go vet ./internal/proc/...` exit 0 | `proc_test.go:195: 測試前提不成立：pid 檔記錄到 group leader、不是孫行程（pid=43580 == pgid=43580）` | FAIL 0.79s |

**6c 確實紅在專用斷言、不是第二段輪詢**：失敗訊息為新增的 `測試前提不成立：pid 檔記錄到 group leader…`，且耗時 0.79s（遠低於任何輪詢預算），證明它在 `Output` 收斂後立即失敗，未進入第二段輪詢。

**兩項各打一個缺陷、確認不可互相替代**：6a 保留 `$!` fixture、只移除 `deadline` 重設 → 紅在第二段輪詢；6c 保留 `deadline` 重設、只把 fixture 退回 `$$` → 紅在專用斷言。兩者失敗訊息與耗時皆不同，互不覆蓋。

**positive control 6b（不計入分母）**：配置為 golden ＋ 在重設行**之前**插入 `time.Sleep(21 * time.Second)`（hash `d8b1ef08a5acb48d5ce28c7fbeb9b3fd14b3b44bd15425d251548476717b4efb`，`gofmt` 乾淨）。結果 `--- PASS … (21.76s)`，`ok … 23.255s`。**與 6a 成對成立**：同樣的 21 秒延遲下，有重設行即 PASS、無重設行即 FAIL。

**oracle 改寫確實生效的直接證據**：baseline（`$!` 版本）的 6a 診斷輸出為 `proc_test.go:198: B1A3-PGID=41087 GC-PID=41102`——**PGID 與 pid 不同**，pid 檔記錄的是孫行程；而 6c（退回 `$$`）測得 `pid=43580 == pgid=43580`。同一測試在兩種 fixture 下的 `pid` 與 `pgid` 關係相反，**機械證明 `$!` 改寫改變了測試探測的對象**。

**程序殘留檢查（兩輪，均以 PGID 精確比對）**：

| 變異 | PGID 來源 | 快照 | `awk` 精確比對 |
|---|---|---|---|
| 6a | 臨時 `t.Logf` → `B1A3-PGID=41087` | `/tmp/b1a3-ps-after-6a.txt`（858 行） | `awk -v g=41087 '$1 == g'` → **matched 0（空輸出）** |
| 6c | 失敗訊息自帶 `pgid=43580`（無需臨時碼） | `/tmp/b1a3-ps-after-6c.txt`（857 行） | `awk -v g=43580 '$1 == g'` → **matched 0（空輸出）** |

兩次 `ps` 快照與兩次 `awk` 比對均為**各自獨立的工具呼叫**，未與其他命令寫在同一條命令列。

**還原與三個 hash 時點**：6a／6b／6c 與臨時 `t.Logf` 全部移除後，`shasum -a 256` = **`7cf94870…`**，與 golden 逐字相符。

| 時點 | hash | 命中 golden |
|---|---|---|
| (i) 第一項變異（6a）套用前 | `7cf94870…` | ✅ |
| (ii) 6a／6b／6c 與臨時 `t.Logf` 全部還原後 | `7cf94870…` | ✅ |
| (iii) Gate A 結束時的最終檔案 | `7cf94870…` | ✅ |

還原後 `gofmt -l .` 乾淨、focused `ok … 2.983s`、`internal/proc` 全包 **18 PASS**（`ok … 11.066s`）。

**6a／6c 的實際連帶清單**：兩者各自僅 `TestOutputCancellationKillsGrandchildren` 一條轉紅；同包其餘 17 條未受影響（還原後全包 18 PASS 已覆核）。

**收尾**：`git worktree remove --force` 後 `git worktree list` 只剩主 repo；主工作區 `git status --porcelain` 僅本 plan 一個 untracked 檔，`HEAD` 仍為 `bb981c4`、與 `origin/main` 同步。

### Task 1（#5）證據——no-change disposition

1. **裁定措辭（逐字）**：preflight **未證實存在可修的牆鐘缺陷**（非宣稱它完全不含牆鐘）。
2. **兩個植入點的否決理由與連帶數字**：`Conn.Handshake` 19 條、`Server.Handshake` 3 條——**diagnostic evidence，不進 mutation 分母**。19 的組成已於本輪以呼叫點核對：`rpc_test.go` 9 ＋ `turns_test.go` 7（同一 `doHandshake` helper）＋ `session_test.go` 3（`Server.Handshake` 委派），`owner_test.go` 走 `stubServer` 測試替身故不受影響。
3. **rev8 的「同檔三條」記載已由 backlog rev11 更正。**
4. **#5 不存在 implementation commit 與 resolved commit**，不得為湊格式而虛構；§7 的 no-change disposition 追加由 **B1a-4** 執行。
5. Gate A 期間 `internal/codex` 零異動（worktree 內只動 `internal/proc/proc_test.go`，`git diff --stat` 已證）。

### Gate A 期間發現的事實——**owner 已裁定 (a) 併入本票（backlog rev12）**

`TestOutputCancellationKillsGrandchildren` 的 pid 檔記錄的**不是孫行程的 PID，而是 leader `spawn.sh` 自己的 PID**。

**證據（兩項獨立）**：

1. 6a 的診斷輸出為 `B1A3-PGID=54018 GC-PID=54018`——**PGID 與該 pid 相等**。`Proc` 以 `Setpgid` 讓 leader 成為 group leader（其 PGID＝自身 PID），孫行程雖同組但 PID 必然不同；因此 `pid == pgid` 只可能代表該 pid 就是 leader。
2. 獨立小實驗確認 POSIX `sh` 的 `$$` 語意：`( echo $$ ) &` 內的 `$$` 展開為**父 shell 的 PID**，不是 subshell 的。實測 `leader-pid=64208`／`subshell-reports=64208` 相同。測試 fixture 正是 `( trap '' TERM HUP; echo $$ > <pidfile>; … ) &`。

**影響（誠實界定；rev4 第二輪已收斂，rev3 原本的「整體假綠」判定過強、在此更正）**：失效的是**第 (2) 條明示的孫行程存活 oracle**——它探測的是 leader（同樣 `trap '' TERM HUP`），無法獨立證明孫行程被收掉，測試註解承諾的「兩條分得開」因此不成立。**整條測試不會因為漏殺孫行程而假綠**：孫行程繼承 stdout，`Output` 先 `io.ReadAll(p.Stdout)` 才 `Wait()`，而 stdout 是自建 `os.Pipe`、須所有 write end 關閉才 EOF，孫行程存活時 `Output` 不會返回，測試會紅在第 (1) 條。

**owner 裁定（2026-09-04）：選 (a)，併入 B1a-3。** 理由——這是 #6 的核心測試契約失效，不是鄰近改善；只修 `deadline` 會讓測試更穩定地驗錯對象，無法誠實關票。不放進 B1a-4（該票承接整合負載／§7／living／closure review，不承接測試實作）；不只記錄缺口（名稱、註解與實際 oracle 會長期矛盾，第 (2) 條斷言會長期失去獨立鑑別力）；不另開票（同一條 #6、同一測試檔、純測試修改，無 owner 或性質混合，拆票無收益）。

**連帶處置**：票面範圍由「一行 `deadline` 重設」擴為「`deadline` 重設 ＋ fixture／oracle 改寫」（Task 2 Step 1–3）；negative control 分母 1/1 → **2/2（6a、6c）**；估點 0.48 → **0.77 pt**（0.58–0.96），連帶 B1a 3.86 pt、B 軌 11.61 pt、合計 18.11 pt——均已寫入 backlog rev12。**rev3 的 Gate A 證據不可轉移，須完整重跑。**

### 已知缺口（誠實標註，不得宣稱消除）

以下四項**必須分開陳述**，不得混為一談（backlog rev11 明令）：

1. **#6 的自然誤紅本輪未重現**。本機 8 核下 `./internal/...` 全跑 1.49–2.60s、三份併發 7.36–10.41s，皆遠低於 20 秒 deadline。**存在的是人工延遲證明機制**（6a 的 21 秒 sleep 可 100% 重現該失效形狀）——這與「已在自然負載下重現」是兩件事，回報時不得互換。
2. **CI runner 冷啟動分布未取得**。本票的所有量測都來自本機；CI 上 `<-done` 是否會超過 20 秒，本票**無資料**。缺口留給 B1a-4 的整合負載矩陣。
3. **五套件整合負載矩陣屬 B1a-4**，本票不做。
4. **`internal/evidence` 的並行碰撞維持未驗證、範圍外**。preflight 曾觀察到三份併發時該套件兩條無關測試因固定 ULID 臨時路徑衝突而 FAIL；owner 裁定**不併入本票**，本 plan 僅作記錄，不追查、不修。

---

## 尚未完成（Gate B）

- **新 Gate A 已完成**（見上方「新 Gate A 執行結果」）。
- implementation commit 尚未產生（本 plan 亦尚未提交）。
- Gate B checklist 全部未執行。
- 五套件整合負載矩陣、§7 追加、living 有效名單仍屬 **B1a-4**。

---

## Erratum

### 新 Gate A 執行完成（2026-09-04，rev4 → rev5）

owner 於唯讀 design review APPROVED rev12 ＋ plan rev4，先 fast-forward 推送 `bb981c4`（`git ls-remote` 核實遠端逐字相符），再以該 SHA 執行新 Gate A。全部 checklist 於隔離 worktree `~/scratch-worktrees/b1a3-2` 完成：新 golden `7cf94870…` 於指定時點建立、三個 hash 時點全數命中、negative control **2/2**（6a 21.80s 紅在第二段輪詢、6c 0.79s 紅在專用斷言）、positive control 6b 21.76s PASS、兩輪 PGID 殘留檢查均為空輸出、還原後 `internal/proc` 18 PASS。**模板、negative control 定義與執行範圍自 rev4 起未變動**（證據轉移契約第 2–4 項成立）。主工作區全程零 `.go` 異動，worktree 已移除。

**本輪未新增任何待裁定事項**；rev3 那一輪的 golden `9c31e9fe…` 維持作廢，未用於任何比對。

### owner 裁定 #6 oracle 失效併入本票（2026-09-04，rev3 → rev4）

owner 就 rev3 回報的發現裁定**選 (a)：併入 B1a-3**，本輪為 CHANGES_REQUIRED，Gate B 維持 NO-GO。

- **票面範圍擴大**：Task 2 由「一行 `deadline` 重設」擴為三段——Step 1 `deadline` 重設、Step 2 fixture 改由父 shell 寫 `$!`、Step 3 新增「pid 不得是 group leader」的專用斷言（`Getpgid` 趁存活時取得，`t.Fatal` 延後到 `cancel()` 與 `<-done` 收斂之後才發，避免契約失敗時留下程序）。
- **驗收表擴大**：新增 **6c**（把 `$!` 退回 `$$`，須紅在專用斷言而非第二段輪詢），**negative control 分母 1/1 → 2/2（6a、6c）**；6b 維持 positive control、不計分母。殘留檢查同時涵蓋 6a 與 6c。
- **臨時診斷碼簡化**：`pgid` 已成為測試本體的變數且 fail-loud 內建於 Task 2 Step 3，rev3 那個帶 `cancel()`／`<-done` 錯誤處理的臨時區塊不再需要——6a 只需一行 `t.Logf`，6c 完全不需要（失敗訊息本身就印出 `pid` 與 `pgid`）。
- **rev3 Gate A 證據作廢**：模板、golden hash、oracle 與 mutation 定義都改變，golden `9c31e9fe…` 失效，**必須完整重跑 Gate A、不做部分轉移**。該段保留為診斷歷史。
- **判定範圍收斂（第二輪 owner CHANGES_REQUIRED，P1-1）**：rev3／rev4 初稿寫的「已確認整體假綠／漏掉孫行程仍會通過」**超過證據強度，已全數移除**。正確判定為：**只有第 (2) 條孫行程 oracle 失效**（探測 leader、無法獨立證明），漏殺仍由第 (1) 條經 stdout EOF／`Output` 收斂路徑間接偵測。併入本票的理由改為**恢復原註解承諾的「兩條分得開」契約**。此更正**不改變估點**（0.77 pt 維持）。
- **文件索引與捨入推導同步修正**（同輪 P1-2／P1-3、P2）：backlog 主標題改 `rev12·估點版`；本 plan 參考文件改以 rev12 為範圍與估點權威；捨入規則段改以未捨入 hr 推導（`(92.5 − 15 ＋ 38.55) hr = 116.05 hr → 11.61 pt`、`(157.5 − 15 ＋ 38.55) hr = 181.05 hr → 18.11 pt`），不再用已捨入的 3.86 pt 入算；Global Constraints 的「然後 commit」限定於 Task 2；backlog 修訂記錄改為 rev12 → rev11 → rev10 遞減；附錄 C 標題註明 rev12 只引用已作廢 Gate A 中的診斷事實、非可轉移驗收證據。
- **重估**：0.48 → **0.77 pt**（0.58–0.96），逐項相加未用平均係數；連帶 B1a 38.55 hr／3.86 pt、B 軌 116.05 hr／11.61 pt、合計 181.05 hr／18.11 pt。已寫入 backlog **rev12**（`bb981c4`，本機已提交、未推送），本 plan 基準同步改綁該 SHA。
- **本輪未執行**：新的 Gate A、任何 Go 測試、任何 `.go` 修改。

### Gate A 執行完成（2026-09-04，rev2 → rev3）

owner 核准 rev2 並授權執行 Gate A。全部 checklist 於隔離 worktree `~/scratch-worktrees/b1a3-1`（基準 `a262a60`）完成，證據回填至「Design-gate 證據」段落：golden `9c31e9fe…` 於指定時點建立、三個 hash 時點全數命中、6a 於 21.76s 紅在測試自身 `t.Fatal`、6b 於 22.11s PASS、PGID 精確比對空輸出、還原後 `internal/proc` 18 PASS。主工作區全程零 `.go` 異動，worktree 已移除。

**同時新增一項 Gate A 期間發現、rev1／rev2 未預見的事實**：該測試 pid 檔記錄的是 leader `spawn.sh` 的 PID 而非孫行程的（由 `PGID == pid` 與 POSIX `sh` 的 `$$` 語意兩項獨立證據確認）。**已超出 rev11 裁定的「一行 `deadline` 重設」範圍，本票未處置，待 owner 裁定**（併入／開續票／僅記錄）。（rev3 當時把影響寫成「整體假綠」，**該判定過強，已於 rev4 第二輪更正**——見上方 rev3 → rev4 條目。）

### 第一輪 owner 裁定 CHANGES_REQUIRED（2026-09-04，rev1 → rev2）

- **P1-1 #5 被錯誤納入 §6.7 專屬例外，且措辭超過證據強度**：rev1 的 Architecture 寫成 #5、#6 都須走專屬例外，但 #5 依裁定不建立 acceptance table，沒有變異可談，§6.7 原文與例外**兩者都不適用**；同時「只記錄**誤列事實**」等同宣告 §7 歷史名單本身有錯，強度高於本輪證據支持的「未證實存在可修的牆鐘缺陷」。**已修**：Architecture 改寫並明示例外只治理 #6；「誤列事實」改為「no-change disposition 與本輪查證結果」，另補一段**措辭上限**釘死不得升級；Task 1 開頭與 Task 3 標題／開頭各補一句適用範圍聲明。
- **P1-2 golden hash 的建立時點循環定義**：rev1 的 Gate A checklist 先要求「hash 回到 golden」、下一項才「記錄 golden」，順序倒置——若臨時 `t.Logf` 或其他多餘內容一開始就混入，最後仍可能被追認為 golden（B1a-2 正是靠 owner 從基準獨立重建才排除此風險）。**已修**：golden 的記錄移到 baseline 全綠之後、任何 6a／6b／臨時診斷區塊之前，標為 ★ 並寫明時點為硬性規定；新增「三個時點 hash 全部命中同一 golden」的驗收項（6a 套用前／全部還原後／Gate A 最終）；四步定義的第 3 步同步釘住該基準。
- **P1-3 未追蹤檔未通過 whitespace check**：`git diff --check` 不檢查 untracked 檔，rev1 回報的「乾淨」是假陰性；owner 以 `git diff --no-index --check /dev/null <plan>` 檢出 exit 3，三行 `space before tab in indent`（巢狀 fenced Go block 的「三空白＋tab」縮排）。**已修**：該 block 改為純空白縮排（與 B1a-2 plan 同一寫法），並在 block 下方註明貼入 `proc_test.go` 時要改回 tab；本輪起 plan 的 whitespace 驗證改用 `git diff --no-index --check /dev/null <plan>`（未追蹤時）或 `git diff --cached --check`（已暫存時）。
- **P2-1 preflight 措辭**：rev1 稱「read-only preflight」卻同段承認執行過 diagnostic mutation。**已修**：改為「以 read-only 為原則：不修改主工作區任何檔案；diagnostic mutation 僅在隔離 worktree 內執行並全數還原」。
- **P2-2 診斷碼靜默吞錯**：rev1 的 `if pg, err := syscall.Getpgid(pid); err == nil` 會在失敗時無聲跳過殘留檢查。**已修**：改為 fail loud，且失敗路徑**先 `cancel()`、以 30 秒上限等 `<-done` 收斂後才 `t.Fatalf`**，避免診斷碼自己留下程序。
