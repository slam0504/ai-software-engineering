# B1a-4 整合驗收：五套件負載矩陣、§7 追加、living 有效名單、aggregate closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev7（2026-09-04，Gate A 證據複審 CHANGES_REQUIRED 後修訂：JSON 計數 16→18、manifest 結構改為不自我雜湊的實際結構、go 版本標為執行者現場紀錄、PGID 22592 依 owner 授權清理並補證據、#2／#6 餘裕觀察與 B2 承接範圍改 #2／#3／#6；矩陣結果未動、未重跑；前版：rev6（rev5 APPROVED 後執行 Gate A 並回填證據；前版：rev5（第四輪 owner CHANGES_REQUIRED 後修訂：一項 P1——M4 採計改為 PID 存活與時間區間必須同時成立、任一缺失或矛盾即整批無效，時間戳改 `time.monotonic_ns()`；前版：rev4（第三輪 CHANGES_REQUIRED 後修訂：三項命令層 P1——`$B1A4` 範例路徑前綴重複、M4 秒級時間戳改 `time.time_ns()` 且以 PID 存活為權威、baseline build 保存 rc 並 fail loud——與兩項 P2——D 段改已裁定語氣、M1 檔名統一；前版：rev3（第二輪 CHANGES_REQUIRED 後修訂：五項 P1——跨工具呼叫的 cwd／`$B1A4` 重設與 fail loud、M4 每條 focused 前後的背景負載檢查、panic／`-timeout` 依 goroutine dump 歸因、可稽核的有效指定執行帳表與「格」粒度、殘留程序完成條件與 kill 安全性——與四項 P2；前版：rev2（第一輪 CHANGES_REQUIRED 後修訂：D1–D5 方向全數裁定接受；修四項 P1——矩陣分母與降級規則、紅燈分類、暫存證據與殘留檢查、D2／D4 回寫權威票面——與一項 P2——`-p=8`、`UPDATE_CORPUS` 前置確認、JSON 計數與 stderr／exit code 保存指令；前版：rev1）
> 狀態：**Gate A 已完成（2026-09-04，整合 HEAD `583387d`，隔離 worktree）——M1／M2 ×3／M3 三份／M4 六條 ×20 全部有效，全部 artifact 頂層 FAIL 事件 0，六條每條計入 PASS 數 27／27，帳表見「Gate A 證據」。owner 已獨立重算確認矩陣結果實質通過；rev7 修正文件層問題並完成一次性環境清理，待短複審後才可進 Task 2–4；本 plan 尚未 commit、未修改任何其他文件**
> 票源：Pre-M4 Readiness Backlog **B1a-4**（`docs/architecture/pre-m4-readiness-backlog.md` rev13；估點 **0.95 pt**，範圍 0.75–1.15；組成：整合負載跑批 4.0–6.0 hr／§7 追加 1.0–1.5 hr／living 文件 1.5–2.5 hr／closure review 1.0–1.5 hr。整合負載工時為**推估值，非計時結果**）
> 基準 commit（整合 HEAD）：**`583387ddefa0feeb93b198b45e438c1ea1497971`**（backlog rev13，已推送、`git ls-remote` 核實相符）。三張施工票的 implementation 全數在此 HEAD 之內：`82caf8b`／`f7ad1ed`（B1a-1）、`7b1bb0c`／`05069e2`／`b0a8404`（B1a-2）、`39aa732`（B1a-3）

**Goal:** 收掉 B1a aggregate。**production 與測試碼零變更**：本票只做四件事——(1) 在整合 HEAD 對五套件（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）跑一次**併行＋`-race`＋負載**矩陣，取得三張施工票都明文推遲到本票的整合負載證據；(2) 於 `docs/spikes/m3b-results.md` §7 **追加**六條的處置結果，不刪改 §7 原文；(3) 建立牆鐘測試 living 有效名單文件；(4) backlog rev14 關閉 B1a aggregate，並提供 closure review checklist。

**Architecture:** 本票是**驗收票**，沒有 seam、沒有 mutation。§6.7 的 mutation acceptance table 規則**不適用**（沒有本票新增或修改的 production code 可植入），本 plan 不得為求格式一致而套一張空表。取而代之的完成 gate 是：矩陣每一格都有**逐字抄錄的 `go test` 輸出**、每一次 FAIL 都有**失效形狀分類**、每一份文件變更都有 **range diff 機械確認零 `.go` 異動**。矩陣的紅燈語意（D1）是本票最重要的設計決定：B1a 三張施工票的目的就是讓六條**不再是牆鐘測試**，因此本票的矩陣若在六條之一紅燈，那是 **aggregate 不能關的發現**，不是「先單獨重跑再判定」的舊規則能吸收的雜訊。

**Tech Stack:** Go（stdlib only，無新依賴）；`go test -race -count=N -json`；`shasum -a 256`；隔離 worktree（沿用 B1a-1／B1a-2／B1a-3）。

**參考文件：**
- `docs/architecture/pre-m4-readiness-backlog.md`（rev13：B1a-4 票面、B1 驗收條件 (1)–(4)、附錄 C B1a-4 組成、rev8 preflight 加壓缺口）
- `docs/spikes/m3b-results.md` §7（牆鐘名單權威來源與「紅了先單獨重跑」舊規則；本票追加、不刪改）、§10–§12（追加段落的標題與日期／commit 標註格式）
- `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7（本票**不適用**，見上）、§6.8（錨點與行號慣例：文件引用測試時用符號名＋「現 line N」）
- `docs/superpowers/plans/2026-09-03-b1a-1-proc-terminate-seam.md`、`2026-09-03-b1a-2-wallclock-determinization.md`、`2026-09-04-b1a-3-proc-lifecycle-wallclock.md`（三張施工票的「已知缺口」與「留給 B1a-4」段，本票逐條承接，見下表）
- `.superpowers/sdd/2026-08-14-m3b-multi-session/task-30-report.md` §4③（§7 提到的「Task 30 §4③ 已登記」原始位置，僅供追溯）

---

## 本票承接的輸入（六條處置狀態，全部已在整合 HEAD 內）

| # | 測試（套件） | 施工票 | 處置 | implementation commit | 施工票 focused 耗時 |
|---|---|---|---|---|---|
| 1 | `TestAppServerTerminateKillsGroup`（`internal/codex`） | B1a-1 | `Proc` 未匯出 timer／signal-event seam＋codex 端契約改寫（不再宣稱驗到 escalation）＋`internal/proc` 三條白箱測試 | `82caf8b`、`f7ad1ed` | `internal/codex` 全包約 2.9s |
| 2 | `TestClaudeAssistFailsLoudOnOversizedLine`（`internal/assist`） | B1a-2 | 移除 fixture 的 `tr` 轉換、保留 15 秒 context（餘裕約 9 倍） | `7b1bb0c` | 0.61s |
| 3 | `TestMultiTurnSendAndTurnBoundaries`（`internal/claude`） | B1a-2 | `waitResult` 局部 deadline 5s→15s（卡死保險絲，非成功判準） | `05069e2` | 0.37s |
| 4 | `TestInFlightTurnDoesNotBlockNewSession`（root） | B1a-2 | `afterFn` 接上既有 `newFakeAfter()`，quiesce 逾時不再由真實時鐘決定 | `b0a8404` | 0.32s |
| 5 | `TestAppServerMidStreamDeath`（`internal/codex`） | B1a-3 | **no-change disposition**——preflight 未證實存在可修的牆鐘缺陷（非宣稱它完全不含牆鐘） | **無**（不得虛構） | owner focused `-count=20` 20/20 |
| 6 | `TestOutputCancellationKillsGrandchildren`（`internal/proc`） | B1a-3 | 第二段輪詢 `deadline` 重設＋fixture 改由父 shell 寫 `$!`＋`pid != pgid` oracle 斷言 | `39aa732` | 0.64s（`-count=5` 5/5） |

**三張施工票明文推遲到本票的事項**（逐條，本票必須逐條交代，不得漏）：

- (a) 五套件整合負載矩陣——三張票都寫「本票不做、也不得宣稱完成；B1a-4 須於整合 HEAD 重跑，不得以任一施工票的 focused 結果取代」。
- (b) CI runner 冷啟動分布未取得——B1a-2（#3 的 383ms 為本機唯一樣本）與 B1a-3（#6 的 `<-done` 在 CI 上是否超過 20 秒無資料）都寫「缺口留給 B1a-4」。**本票無法在本機取得 CI 資料**（B2 最小 CI 尚未建立），見 D4。
- (c) #6 的自然誤紅未重現——B1a-3 只有人工延遲證明機制（6a 的 21 秒 sleep），本票的負載矩陣是第一次把 `internal/proc` 排進加壓範圍（rev8 明載它「根本沒排進加壓範圍」）。
- (d) rev8 preflight 加壓的兩處未涵蓋：root package（含 #4）「未跑完，高負載下編譯搶不到 CPU 而依上限中止」、`internal/proc`（含 #6）「根本沒排進」。本票矩陣必須把這兩包**跑完**，並記錄實際耗時。
- (e) B1 驗收條件 (2)「併行＋`-race`＋負載下重複執行不偽陽（**次數於估點時定**）」——backlog 全文**從未定出這個次數**。本票在 D1 提出具體數字，由 owner 裁定後成為驗收數字。

**不屬本票、本票不碰**：`internal/evidence` 三份併發時固定 ULID 臨時路徑碰撞（B1a-3 preflight 觀察、owner 裁定範圍外）——該套件**不在**五套件矩陣內，本票矩陣不會執行它，也不追查。前端兩條候選的重現與處置屬 B1b。

---

## Production 與測試碼零變更聲明

本票**不修改任何 `.go` 檔**，包括測試。整合 HEAD 上的六條測試就是驗收對象；若矩陣結果顯示需要改測試，那是**新的發現**，本票只記錄並停下，不在本票內修（見 D1 的紅燈語意）。Gate B 以 `git diff --name-only 583387d..HEAD` 機械確認：只含本 plan、`docs/spikes/m3b-results.md`、living 文件、backlog 四份 Markdown。

**§6.7 不適用聲明**：本票沒有本票新增或修改的 production code，mutation acceptance table 的植入對象不存在。B1a-1 的 5/5、B1a-2 的 3/3、B1a-3 的 2/2 已各自在施工票關票時完成並經 golden hash 轉移，本票不重跑、不重複計入、也不宣稱為本票證據。

---

## Global Constraints

- Module：`github.com/slam0504/sdlc-workbench`。本機環境：macOS、8 core、16 GB RAM、Go 1.25 系列（以 `go version` 現場記錄為準）。
- **矩陣一律在隔離 worktree 執行**：`git worktree add --detach /Users/eason_tseng/scratch-worktrees/b1a4-1 583387d`，第一步 `cp -R <主 repo>/frontend/dist <worktree>/frontend/`（root `main.go` 有 `//go:embed all:frontend/dist`，未複製則 root package 編譯失敗）。複製來源與其後 `go build ./...` exit code 記入證據。主工作區在 Task 1 期間**零異動**。
- **證據目錄以 `mktemp -d /tmp/b1a4.XXXXXX` 建立一次**，實際絕對路徑記入證據段（下文以 `$B1A4` 代稱）；**不得重用固定路徑**，避免中斷或前輪檔案混入 hash 清單。
- **每個工具呼叫都是新的 shell：cwd 與變數不會跨呼叫保留。** 因此 Step 2 起的**每一個**工具呼叫都必須以下列前置行開頭（`<實際絕對路徑>` 為 Step 1 `mktemp` 印出的完整路徑，例如 `/tmp/b1a4.ABC123`，整段逐字貼入、不再加任何前綴、不用變數展開），任一檢查失敗即 fail loud、該呼叫不得繼續：

  ```sh
  cd /Users/eason_tseng/scratch-worktrees/b1a4-1 || exit 1
  B1A4=<實際絕對路徑>; test -n "$B1A4" && test -d "$B1A4" || { echo "B1A4 missing: $B1A4"; exit 1; }   # 例：B1A4=/tmp/b1a4.ABC123
  test "$(git rev-parse HEAD)" = 583387ddefa0feeb93b198b45e438c1ea1497971 || { echo "wrong HEAD"; exit 1; }
  ```

  本 plan 下文所有指令區塊都省略這三行，但**執行時每個呼叫都要帶**；證據段抄錄指令時保留前置行。
- **每一次 `go test` 都同時保存三樣東西**：`-json` stdout（`$B1A4/<格>.json`）、stderr（`$B1A4/<格>.stderr`）、exit code（`$B1A4/<格>.exit`）。固定寫法：`go test … -json > "$B1A4/<格>.json" 2> "$B1A4/<格>.stderr"; echo $? > "$B1A4/<格>.exit"`。plan 內只放摘要表，每個檔案記錄**行數與 SHA-256**。這些檔案不進 repo。
- **頂層測試計數的可重現指令**（只計無 `/` 的頂層 `Test` 名，排除 subtest 與套件層事件）：

  ```sh
  python3 - "$B1A4/<格>.json" <<'EOF'
  import json, sys, collections
  c = collections.Counter(); six = {}
  SIX = {"TestAppServerTerminateKillsGroup","TestClaudeAssistFailsLoudOnOversizedLine","TestMultiTurnSendAndTurnBoundaries","TestInFlightTurnDoesNotBlockNewSession","TestAppServerMidStreamDeath","TestOutputCancellationKillsGrandchildren"}
  for line in open(sys.argv[1]):
      try: e = json.loads(line)
      except ValueError: continue
      t = e.get("Test"); a = e.get("Action")
      if t and "/" not in t and a in ("pass","fail","skip"):
          c[(e.get("Package"), a)] += 1
          if t in SIX: six.setdefault(t, []).append((a, e.get("Elapsed")))
  for k in sorted(c): print(k, c[k])
  for t in sorted(six): print(t, six[t])
  EOF
  ```

- **顯式 `-p=8`**：M1–M4 全部指定 `-p=8`。它固定的是 go command 的**建置與套件測試併行上限**，讓矩陣的併行度定義可重現；它**不控制** `GOMAXPROCS`、測試內 goroutine 排程或 OS 排程，這些仍隨環境浮動，證據段只記錄、不宣稱已控制。環境前置以 `sysctl -n hw.ncpu` 記錄核心數（預期 8）。
- **`UPDATE_CORPUS` 必須未設定**：環境前置以 `if [ -n "${UPDATE_CORPUS+x}" ]; then echo "UPDATE_CORPUS is set"; exit 1; fi` 明確 fail loud（不依賴 `grep -c` 零命中時的 exit code），root 的 422 PASS＋1 SKIP＝423 前提才成立。
- **顯式 `-timeout 30m`**：root package 單跑 `-race` 已約 197s，三份併發下可能超過 `go test` 預設 10 分鐘。撞 `-timeout` **不自動等於環境失效**——`go test` 逾時會印出 `running tests:` 清單與 goroutine dump，必須依此歸因（見 D1 紅燈語意與 Step 6）：卡住的是六條之一或其契約路徑即為回歸並阻擋；只有能證明卡在其他測試、或由資源失效造成，才算該格無效並調整後重跑。
- **紅燈不重跑到綠**：任何 FAIL 先保存完整 `-json`、依「失效形狀分類」歸類，再決定下一步（見 D1）。「單獨重跑一次」只用於**六條以外**的測試。
- **時間常數不得成為成功判準**：矩陣記錄耗時是為了證明「跑完了」與「負載確實存在」（對照施工票的 focused 耗時），不設任何「跑得夠快才算過」的門檻。
- 無新外部依賴；不新增任何腳本進 repo（矩陣指令逐字寫在本 plan，可重現即可）。
- 文件引用測試時遵守 §6.8：符號名為錨點，行號只作「現 line N」附註。

---

## owner 已裁定事項（第一輪 design review，D1–D5 全數接受；rev2 起為已定契約）

**D1 矩陣次數與紅燈語意**（改變 Task 1 實作與 B1 驗收條件 (2) 的數字）

**owner 裁定（第一輪）**：接受四層矩陣，分母改為每條**至少 27 次「有效、指定執行」**——只有前景指定該測試或該套件、且該格未被判定無效的執行才計入；背景負載中的執行與失效嘗試一律不計。

四層矩陣，全部在整合 HEAD、`-race`、顯式 `-p=8`：

| 層 | 內容 | 輪數 | 六條各自計入分母的有效執行次數 |
|---|---|---|---|
| M1 | 五套件**逐包單獨**跑（`go test -race -p=8 -count=1 -timeout 30m <pkg>`，五個獨立指令），建立整合 HEAD 的逐包基準耗時 | 1 | 1 |
| M2 | 五套件**同一指令併行**（`go test -race -p=8 -count=1 -timeout 30m . ./internal/codex ./internal/assist ./internal/claude ./internal/proc`） | 3 | 3 |
| M3 | **三份 M2 同時啟動**（各自獨立 `-json` 檔），對齊 rev8 preflight 的「三份併發」形狀，但這次**包含 root 與 `internal/proc`**。**降級規則**：若三份併發因環境失效（撞 `-timeout`、OOM、fork 失敗），改為**兩份併發 ×2 輪**，取得至少四次有效執行；失效或未完成的嘗試**不計分母**，但其證據仍保存並揭露 | 1（降級時 2） | ≥3（降級時 ≥4） |
| M4 | 以一份 M2 作背景負載，前景**逐條** focused `go test -race -p=8 -run '^<Test>$' -count=20 <pkg>`（六條依序）。**背景 M2 內六條的執行不計入分母**，但其任何 FAIL 仍須保存並分類 | 1 | 20 |
| | | | **每條至少 27 次有效指定執行**（1＋3＋≥3＋20） |

紅燈語意（owner 第一輪 P1 修正：不得只看是否 `t.Fatal`——fixture setup、fork／exec、OOM、磁碟錯誤同樣以 `t.Fatal(err)` 呈現，不是契約回歸）：

- **六條之一出現任何 FAIL**：先**停止該格**，保存完整 `-json`、stderr 與 exit code，再依失敗訊息命中的斷言分類。
  - **命中該測試的契約／oracle 斷言**（六條各自的正題斷言清單見 Task 1 Step 6 的對照表）：**真正回歸，B1a aggregate 阻擋**。不得單獨重跑吸收——那是 §7 的舊規則，B1a 的目的正是讓它失效。回報 owner；修法屬新票或重開對應施工票，本票不修。
  - **命中 setup／前提校驗**（「測試前提不成立」類訊息、`os.WriteFile`／`exec` 錯誤）或**可證明的資源失效**（OOM、fork／exec 失敗、磁碟）：**該格對該測試無效**——不算紅也不算綠、不計分母。保留證據、調整負載（降低併發份數或錯開時間）後重跑該格，並在證據段揭露原因與調整。
  - **panic 或撞 `-timeout`：依 goroutine dump／panic stack 歸因，不得一律歸環境**。`go test` 逾時輸出的 `running tests:` 清單若含六條之一，或 dump 中卡住的 goroutine 落在六條的契約路徑（例如 #4 的 pump／`CloseSequence`、#3 的 `waitResult`、#6 的 `Output`／`io.ReadAll`、#1／#5 的 `Terminate`／`Wait`），即為**卡死回歸、阻擋**——#4 的測試碼已明載真實 pump 卡死會一路落到 `go test -timeout`（`app_invariants_test.go` 的 `TestInFlightTurnDoesNotBlockNewSession`，現 line 317–319），這正是本規則要接住的形狀。只有 dump 證明卡在**其他**測試、panic 源自其他測試而六條只是被中斷未執行、或伴隨可證明的資源失效，才算該格無效。
- **五套件內六條以外的測試 FAIL**：依既定分流——先看失敗訊息：環境類（同上定義）→ 該格對該測試無效、揭露後重跑；非環境類 → 沿用 §7 舊規則單獨重跑一次判定，**無論結果都要揭露**，並登記為 living 文件的「候選」（失效形狀是逾時／牆鐘）或「回歸」（其他）；「回歸」阻擋 aggregate 關閉，「候選」不阻擋但須開續票。
- **root 唯一的 env-gated SKIP**（`TestRegenerateCorpusVerdicts`，`UPDATE_CORPUS=1` 才跑）維持 SKIP，計為 422 PASS＋1 SKIP＝423。

若 owner 認為 27 次過多或過少、或 M3 的三份併發在 16 GB 下風險過高，改數字即可，矩陣結構不變。

**D2 living 文件的位置與 B1b 分工**（改變 Task 3 交付物）

**owner 裁定**：接受建立 `docs/architecture/wall-clock-test-register.md`，B1b 後續更新同一份；B1b 估點暫不調整，到 B1b plan gate 再核對。票面回寫見 Task 4。

backlog 的 B1a-4 與 B1b 票面**都**寫「living 有效名單文件」，且沒有任何一處切分。裁定後的契約：本票**建立** `docs/architecture/wall-clock-test-register.md`（living，含版本與更新日期；三段：Go 六條的處置狀態與證據指標、前端兩條候選標「待重現（B1b）」、規則段），B1b 之後**更新**同一份文件而不是另建。B1b 估點 0.3 pt 暫不調整，於 B1b plan gate 再核對；本 plan 不重估 B1b。

**D3 §7 追加的形狀**

**owner 裁定**：接受於 §7 追加 7.1，原文零刪改。

裁定後的契約：在 §7 具名清單之後、`---` 之前新增小節 `### 7.1 處置結果（2026-09-XX，B1a 收尾，整合 HEAD 583387d）`，內含六條處置表（disposition／commit／修正方式／本票矩陣重驗摘要），並明寫「§7 上文為 2026-08-21 的歷史觀察，原文不動；『紅了先單獨重跑再判定、綠燈不作通過證據』的規則自本節起**對這六條不再適用**，有效規則改由 living 文件承載」。不另開頂層 §13，因為 backlog 驗收條件 (3) 的原話是「於 §7 追加」。

**D4 CI runner 冷啟動缺口的歸屬**

**owner 裁定**：接受移交 B2，不阻擋 B1a aggregate 關閉；B2 驗收條件須新增對應項（Task 4）。

本票無法取得 CI 資料（B2 最小 CI 尚未建立）。裁定後的契約：本票在 §7.1、living 文件與 backlog rev14 **明寫此缺口未消除**，並把它移交給 **B2**（CI 建立後首批 required check 跑批時補量，B2 驗收條件新增對應項，見 Task 4）。B1a aggregate 關閉**不以此缺口消除為條件**。

**D5 B1a aggregate 關閉的語意**

**owner 裁定**：接受本票只關 B1a aggregate，B1 仍等待 B1b。

B1 = B1a（Go 六條）＋ B1b（前端兩條候選）。本票關閉的是 **B1a aggregate**；B1 票整體仍開啟直到 B1b 完成。backlog rev14 的狀態行照此措辭。

---

## Task 1: 五套件整合負載矩陣（隔離 worktree，不 commit）

**前置：D1 已裁定（每條至少 27 次有效指定執行；M3 三份、降級時兩份 ×2 輪）。** 每個工具呼叫都帶 Global Constraints 的三行前置。

- [ ] **Step 1: 建立隔離 worktree 與環境記錄**

```sh
git worktree add --detach /Users/eason_tseng/scratch-worktrees/b1a4-1 583387d
cp -R /Users/eason_tseng/playground/project/ai-software-engineering/frontend/dist /Users/eason_tseng/scratch-worktrees/b1a4-1/frontend/
cd /Users/eason_tseng/scratch-worktrees/b1a4-1 && git rev-parse HEAD && go version && sysctl -n hw.ncpu hw.memsize
if [ -n "${UPDATE_CORPUS+x}" ]; then echo "UPDATE_CORPUS is set"; exit 1; fi
B1A4=$(mktemp -d /tmp/b1a4.XXXXXX); echo "$B1A4"   # 印出的絕對路徑逐字記入證據段，之後每個呼叫的前置行都貼這個值
go build ./... 2> "$B1A4/build.stderr"; rc=$?; echo "$rc" > "$B1A4/build.rc"; [ "$rc" -eq 0 ] || { echo "baseline build failed rc=$rc"; exit 1; }
```

`go build ./...` 非零即**停止整個 Task 1**，不得進入 M1；`build.rc` 與 `build.stderr` 列入 Step 7 的 artifact 清單。

記錄：worktree HEAD、`go version`、核心數與記憶體、`UPDATE_CORPUS` 未設定、`build.rc`＝0、`$B1A4` 實際絕對路徑、主工作區 `git status --porcelain` 為空。

**執行前程序快照（獨立工具呼叫，帶前置行）**：`ps -eo pgid,pid,ppid,etime,command > "$B1A4/ps-before.txt"; wc -l "$B1A4/ps-before.txt"`。

- [ ] **Step 2: M1 逐包基準（五個獨立指令，依序）**

```sh
m1() { go test -race -p=8 -count=1 -timeout 30m -json "$2" > "$B1A4/m1-$1.json" 2> "$B1A4/m1-$1.stderr"; echo $? > "$B1A4/m1-$1.exit"; }
m1 root   .
m1 codex  ./internal/codex
m1 assist ./internal/assist
m1 claude ./internal/claude
m1 proc   ./internal/proc
cat "$B1A4"/m1-*.exit
```

從每個 `-json` 萃取：頂層 PASS／FAIL／SKIP 數（預期 423=422+1／47／20／22／18）、套件 `elapsed`、六條各自的 `Elapsed`。

- [ ] **Step 3: M2 五套件併行 ×3**

```sh
for r in 1 2 3; do
  go test -race -p=8 -count=1 -timeout 30m -json . ./internal/codex ./internal/assist ./internal/claude ./internal/proc > "$B1A4/m2-r${r}.json" 2> "$B1A4/m2-r${r}.stderr"; echo $? > "$B1A4/m2-r${r}.exit"
done; cat "$B1A4"/m2-r*.exit
```

每輪記錄：總 wall time、五包各自 `elapsed`、六條各自 `Elapsed`、任何 FAIL。

- [ ] **Step 4: M3 三份併發 ×1**

```sh
python3 -c "import time; print(time.monotonic_ns())" > "$B1A4/m3-start.txt"
for c in 1 2 3; do
  ( go test -race -p=8 -count=1 -timeout 30m -json . ./internal/codex ./internal/assist ./internal/claude ./internal/proc > "$B1A4/m3-c${c}.json" 2> "$B1A4/m3-c${c}.stderr"; echo $? > "$B1A4/m3-c${c}.exit" ) &
done; wait; python3 -c "import time; print(time.monotonic_ns())" > "$B1A4/m3-end.txt"; cat "$B1A4"/m3-c*.exit
```

**降級規則（owner 裁定）**：若任一份因環境失效（撞 `-timeout`、OOM、fork 失敗，或 M3 wall time 超過 M2 單輪的 4 倍且伴隨上述任一），該輪三份**全部不計分母**、證據保留；改為**兩份併發 ×2 輪**（檔名 `m3b-r<輪>-c<份>`），取得至少四次有效執行。失效嘗試在證據段逐一揭露。

- [ ] **Step 5: M4 背景負載下六條 focused ×20（每條一個工具呼叫，前後各查一次背景）**

**背景負載的啟動方式**：用工具的背景執行模式（Claude Code Bash `run_in_background`）**單獨一個呼叫**啟動，腳本內把 `go test` 的 PID 寫入 pid 檔並 `wait`，這樣它才能跨工具呼叫存活、也才能被後續呼叫以 `kill -0` 檢查：

```sh
n=1   # 第二份起遞增，檔名 m4-bg2、m4-bg3…
go test -race -p=8 -count=1 -timeout 30m -json . ./internal/codex ./internal/assist ./internal/claude ./internal/proc > "$B1A4/m4-bg$n.json" 2> "$B1A4/m4-bg$n.stderr" &
echo $! > "$B1A4/m4-bg.pid"; python3 -c "import time; print(time.monotonic_ns())" > "$B1A4/m4-bg$n.start"
wait; echo $? > "$B1A4/m4-bg$n.exit"; python3 -c "import time; print(time.monotonic_ns())" > "$B1A4/m4-bg$n.end"
```

**每條 focused 一個前景呼叫**（`k`＝t1…t6、`T`＝測試名、`P`＝套件；六條依 t1→t6 順序）：

```sh
k=t1; T=TestAppServerTerminateKillsGroup; P=./internal/codex; r=1   # 重跑時 r 遞增
bg=$(cat "$B1A4/m4-bg.pid"); kill -0 "$bg" 2>/dev/null && a=alive || a=dead
echo "$k r$r before bg=$bg $a $(python3 -c "import time; print(time.monotonic_ns())")" >> "$B1A4/m4-ledger.txt"
[ "$a" = alive ] || { echo "no background load; start a new one first"; exit 1; }
go test -race -p=8 -run "^$T\$" -count=20 -timeout 30m -json "$P" > "$B1A4/m4-$k-r$r.json" 2> "$B1A4/m4-$k-r$r.stderr"; echo $? > "$B1A4/m4-$k-r$r.exit"
kill -0 "$bg" 2>/dev/null && a=alive || a=dead
echo "$k r$r after  bg=$bg $a $(python3 -c "import time; print(time.monotonic_ns())")" >> "$B1A4/m4-ledger.txt"
```

**計入分母的條件（兩項證據必須同時成立，缺一即該批 20 次全部無效）**：(1) **PID 存活**——同一條的 `before` 與 `after` 兩行記錄的背景 PID 相同、兩次都 `alive`；(2) **時間區間**——以 `time.monotonic_ns()` 整數比較（單調時鐘，不受同秒、跨午夜或 NTP 校時影響），該背景的 `.start` < `before` 且 `.end` > `after`。兩項缺一不可：PID 存活單獨不足以排除 PID 重用，時間區間單獨不足以證明背景真的在跑。**任一證據缺失（pid 檔、start／end 檔或 ledger 行不存在或不可解析）、或兩者矛盾（含時鐘異常造成的矛盾），一律採保守判定：該批無效、artifact 保留並列入帳表、在新背景下整批重跑**；不得以其中一項為權威覆蓋另一項。不符者該批**整批不計分母**、檔案保留並在帳表標「無效：背景未涵蓋完整區間」，然後啟動新背景（`n` 遞增）、以 `r` 遞增**整批重跑**該條。背景 `m4-bg*.json` 內六條的執行**不計入分母**，但其任何 FAIL 仍須依 Step 6 保存並分類。六條全部跑完後，等待背景自然結束（不得 kill），確認 `.end` 與 `.exit` 已寫出。

- [ ] **Step 6: 失效形狀分類與程序殘留檢查**

對每一個 FAIL（若有，含背景 `m4-bg*`）：抄錄 `-json` 中該測試的 `Output` 全文與對應 `.stderr`，判定失敗訊息命中哪一條斷言，依 D1 的紅燈語意歸類為 (i) 六條契約／oracle 斷言→回歸、阻擋；(ii) 六條 setup／前提校驗→該格對該測試無效；(iii) 可證明的資源失效（OOM、fork／exec、磁碟）→該格無效；(iv) panic→**依 stack 歸因**：源自六條或其契約路徑→(i)；源自其他測試且六條只是被中斷→該格無效；(v) 撞 `-timeout`→**依 `running tests:` 與 goroutine dump 歸因**：卡住的是六條之一或其契約路徑→(i)；卡在其他測試→該格無效；(vi) 六條以外測試→依既定分流。**每一筆 (iv)／(v) 的歸因都要在證據段抄錄 dump 中的關鍵 frame**（測試函式名與卡住的呼叫），不得只寫「timeout」。

**六條的契約／oracle 斷言對照表**（分類 (i) 的依據；行號為現況附註、符號為錨點）：

| # | 測試 | 契約／oracle 斷言（命中即回歸） | setup／前提斷言（命中即該格無效） |
|---|---|---|---|
| 1 | `TestAppServerTerminateKillsGroup` | 「terminated server must not exit 0」、「leader 必須死於訊號（*exec.ExitError）」、「leader 必須死於訊號終止」、「leader 死因必須是 SIGTERM（未進入 escalation 分支）」、「process group must be fully dead」 | 啟動與 handshake 的裸 `t.Fatal(err)`、「Terminate: %v」 |
| 2 | `TestClaudeAssistFailsLoudOnOversizedLine` | 「oversized line must surface a stream_error (fail loud)」 | fixture 建立的裸 `t.Fatal(err)` |
| 3 | `TestMultiTurnSendAndTurnBoundaries` | 「stream closed before result」、「no result within 15s」（`waitResult` 保險絲——命中時須另判：若同時有環境訊號則歸 (iii)）、「results = %d」、「exit = %+v」 | 啟動與 send 的裸 `t.Fatal(err)` |
| 4 | `TestInFlightTurnDoesNotBlockNewSession` | 「卡住的 turn 不得擋住「開新對話」」、「afterFn seam 未被接上」、「開新對話之後 slot 必須回到 idle」、「開新對話之後必須可再啟動」、「重新啟動之後 slot 必須是 active」 | 「StartSession: %v」、「precondition：turn 必須卡在進行中」 |
| 5 | `TestAppServerMidStreamDeath` | 「exit = %d, want 7」、「Done must be closed after death」、「Call after death must error」 | 啟動的裸 `t.Fatal(err)`、「server must be alive right after start」、「handshake: %v」 |
| 6 | `TestOutputCancellationKillsGrandchildren` | 「取消之後 Output 沒有返回」、「忽略 TERM 的孫行程仍存活」 | 「測試前提不成立：孫行程沒有起來」、「測試前提不成立：pid 檔記錄到 group leader」、「取不到 pid 的 PGID」 |

上表於 rev2 以整合 HEAD `583387d` 的測試碼逐條 grep 核對（#6 取自 B1a-3 plan 模板）。執行 Task 1 時再核對一次，發現不符即先修表再分類。#3 的「no result within 15s」是 B1a-2 放寬後的卡死保險絲：單獨命中且無環境訊號時視為回歸，伴隨 OOM／fork 失敗等環境訊號時歸該格無效並揭露。

**程序殘留檢查（三個獨立工具呼叫，不得把 `ps` 與含搜尋字串的 `awk` 寫在同一命令列）**：

1. `ps -eo pgid,pid,ppid,etime,command > "$B1A4/ps-after.txt"`
2. `wc -l "$B1A4/ps-before.txt" "$B1A4/ps-after.txt"`
3. 另一個呼叫比對：`awk 'NR==FNR{seen[$2]=1; next} !($2 in seen)' "$B1A4/ps-before.txt" "$B1A4/ps-after.txt" > "$B1A4/ps-new-pids.txt"; wc -l "$B1A4/ps-new-pids.txt"`，再對新增 PID 的**完整 `$0`** 人工逐行檢視。差集**可能包含與矩陣無關的新程序**（工具本身、系統服務），完成條件是「**沒有可歸因於矩陣的殘留程序**」——即沒有 `spawn.sh`、`fake-codex-appserver.sh`、`fake-claude.sh`、其子 `sleep`，也沒有 `go test` 產生的 `*.test` 二進位仍在執行。每一個被判定為無關的新 PID 都要在證據段列出命令列與判定理由。

**若有可歸因的殘留，清理前的安全檢查（每步獨立呼叫）**：(a) `ps -eo pgid,pid,ppid,command > "$B1A4/ps-pre-kill.txt"` 重新取快照，不用舊快照；(b) 另一呼叫 `awk -v g=<pgid> '$1 == g' "$B1A4/ps-pre-kill.txt"` 列出該 PGID **目前**的完整成員；(c) 確認 `<pgid>` 是正整數、不等於目前 shell 的 PGID（`ps -o pgid= -p $$`）、也不等於 1，且成員**只含**本次 fixture 程序（`spawn.sh`／fake CLI／其 `sleep`）；三者缺一不得殺；(d) 才執行 `kill -KILL -- -<pgid>`，再取一次快照確認消失。全部揭露於證據段。

- [ ] **Step 7: 證據落地與 worktree 收尾**

對 `$B1A4/` 下全部檔案逐檔記錄行數與 SHA-256（兩個指令都要：`wc -l "$B1A4"/*` 與 `shasum -a 256 "$B1A4"/*`）；**產出「有效指定執行帳表」**（格式見下）；把 M1–M4 摘要表與 FAIL 分類（或「0 FAIL」）寫入本 plan「Gate A 證據」段；`git worktree remove --force /Users/eason_tseng/scratch-worktrees/b1a4-1`，`git worktree list` 只剩主 repo；主工作區 `git status --porcelain` 只含本 plan。

**有效指定執行帳表（Gate A 必備，可稽核）**——每一列是「一個 artifact × 一條測試」：

| artifact | 層 | 指定／背景 | 該測試執行次數 | 有效／無效 | 排除理由 | 計入 PASS 數 |
|---|---|---|---|---|---|---|
| `m1-codex.json` | M1 | 指定 | 1 | 有效 | — | 1 |
| `m3-c2.json` | M3 | 指定 | 1 | 無效 | 撞 `-timeout`，`running tests:` 為 `TestXxx`（非六條）→該格無效 | 0 |
| `m4-bg1.json` | M4 | 背景 | 1 | 不計 | 背景負載，依 D1 不入分母 | 0 |
| `m4-t4-r1.json` | M4 | 指定 | 20 | 無效 | 背景未涵蓋完整區間（after=dead） | 0 |
| `m4-t4-r2.json` | M4 | 指定 | 20 | 有效 | — | 20 |

六條各一張，末列為「合計計入 PASS 數」，須 ≥27；帳表由 Global Constraints 的 python 計數指令逐檔產出原始數字，再由執行者依 Step 5／6 規則標註有效性——**兩者都要抄進證據段**，reviewer 可從 artifact hash 重算。

**「格」的粒度與重跑計數**：M1 的格＝一個套件一次執行（一個 artifact）；M2 的格＝一輪（一個 artifact，五包同檔）；M3 的格＝一份（一個 artifact）；M4 的格＝一條 focused 的一批 20 次（一個 artifact）。**格級無效**（資源失效、timeout 歸因於他處、背景未涵蓋）使該 artifact 內六條的執行全部不計；**測試級無效**（六條之一命中 setup 斷言）只使該測試在該 artifact 的執行不計，同檔其他測試照常計入。重跑一律產生**新 artifact**（檔名加 `-r<N>`），原無效 artifact 保留並列入帳表；同一格重跑不得超過 2 次，超過即停止並回報 owner（那已是矩陣設計或環境問題）。

**本 task 不 commit。** 結果回報 owner；**只有帳表顯示六條每條計入 PASS 數 ≥27 且分類 (i) 為 0（或 owner 對發現另有裁定）之後才進 Task 2–4**。

---

## Task 2: `docs/spikes/m3b-results.md` §7 追加 7.1（D3 裁定後）

- [ ] **Step 1:** 在 §7 具名清單第 5 項之後、`---` 之前插入 `### 7.1 處置結果（<日期>，B1a 收尾，整合 HEAD 583387d）`。內容：六條處置表（#、測試、disposition、commit、修正方式一句、本票矩陣重驗摘要：M1–M4 各層有效指定執行次數與 PASS 數，合計至少 27 次）、規則變更聲明（見 D3）、仍未消除的缺口（CI 冷啟動→B2；#6 自然誤紅未在本機重現，只有人工延遲機制與本票負載下的有效指定執行全綠）、**#2／#6 在本機 8 核 M3 三份併發下餘裕縮至約 1.9／2.2 倍的觀察（明標不外推至 CI，移交 B2）**。
- [ ] **Step 2:** `git diff docs/spikes/m3b-results.md` 確認 §7 原文（第 451 行起至具名清單）**零刪改**，只有新增行。
- [ ] **Step 3:** 不 commit，與 Task 3 一起進 Gate B。

---

## Task 3: living 有效名單文件（D2 裁定後）

- [ ] **Step 1:** 建立 `docs/architecture/wall-clock-test-register.md`（D2 已裁定）。頂部：版本 v1、更新日期、權威來源說明（取代 §7 具名清單作為「目前有效名單」，§7 保留為歷史）。
- [ ] **Step 2:** 三段：(A) Go 六條——每條：測試名（符號）、套件、狀態（resolved／no-change）、commit、修正方式、最後一次負載重驗（本票矩陣日期、有效指定執行次數與 PASS 數）；(B) 前端兩條候選——狀態「待重現（B1b）」，附 owner 凍結的前端規則（相同 timeout 可單獨重跑一次判定但仍須揭露；失敗形狀改變視為真正失敗）；(C) 規則——六條紅燈即回歸、不得單獨重跑吸收；新候選登記條件（須以現行 HEAD 重現並附證據）；CI 冷啟動缺口歸屬 B2；**#2／#6 餘裕觀察（本機 8 核、M3 三份併發，不外推至 CI）與 B2 承接 #2／#3／#6 的量測**。
- [ ] **Step 3:** 不新增任何 `.go` 或腳本。不 commit，進 Gate B。

---

## Task 4: backlog rev14 與 closure review checklist

- [ ] **Step 1:** backlog rev14：標題與版本行；狀態行改為「B1a-1／B1a-2／B1a-3／B1a-4 皆已完成，**B1a aggregate 已關閉**；B1 票仍開啟待 B1b」；估點表 B1a-4 列改「已完成」並引用 plan／implementation commit；「B1a 進度」段改 rev14；B1 驗收條件 (3) 補 living 文件實際路徑；修訂記錄新增 rev14（純狀態更新，估點不變；矩陣摘要；缺口移交 B2；D1–D5 裁定結果；**「發現既有診斷 fixture PGID 22592、已安全清理、非本矩陣殘留」一筆**）。**另兩處權威票面回寫（owner 第一輪 P1）**：(a) **B1b 票面**由「前端兩條候選重現與處置＋living 有效名單文件」改為「……＋**更新既有** living 有效名單文件（`docs/architecture/wall-clock-test-register.md`，B1a-4 建立）」，估點 0.3 pt 暫不調整、於 B1b plan gate 再核對；(b) **B2 驗收條件**新增一項：CI 建立後首批 required check 跑批時，量測並記錄 **#2 `TestClaudeAssistFailsLoudOnOversizedLine`、#3 `TestMultiTurnSendAndTurnBoundaries`、#6 `TestOutputCancellationKillsGrandchildren`** 的 CI 冷啟動與 required-check 耗時分布，回寫 living 文件（B2 估點暫不調整，留到 B2 plan gate 核對）——只在修訂記錄寫「移交 B2」不構成可執行契約。
- [ ] **Step 2:** closure review checklist（供 owner 於關票 review 逐項核對）：
  - [ ] 六條每條至少 27 次**有效指定執行**全 PASS（背景負載與失效嘗試不計），每格有 `.json`／`.stderr`／`.exit` 行數與 hash。
  - [ ] root 與 `internal/proc` 兩包在負載下**跑完**（rev8 兩處未涵蓋已補）。
  - [ ] 每一個 FAIL（含六條以外）都有形狀分類與處置。
  - [ ] §7 原文零刪改，7.1 只新增。
  - [ ] living 文件存在、六條與兩條候選齊全、規則段含紅燈語意。
  - [ ] `git diff --name-only 583387d..HEAD` 只含四份 Markdown，零 `.go`。
  - [ ] CI 冷啟動缺口在三份文件中一致標「未消除、移交 B2」，且 **B2 驗收條件已新增對應項**。
  - [ ] **B1b 票面已改為「更新既有 living 文件」**，建立／更新歧義消除。
  - [ ] B1 票仍開啟、B1b 未被宣稱完成。

---

## Gate A：矩陣完成條件（隔離 worktree，不 commit）

- [ ] 每個工具呼叫都帶三行前置（worktree cwd、`$B1A4` 絕對路徑存在、HEAD＝`583387d`），證據段抄錄的指令含前置行。
- [ ] Step 1 的 `build.rc`＝0（baseline `go build ./...` 成功），非零時 Task 1 已停止、未進入 M1。
- [ ] M1 五包逐包全綠（423=422+1 SKIP／47／20／22／18），artifact 為 `m1-root`／`m1-codex`／`m1-assist`／`m1-claude`／`m1-proc`，耗時記錄。
- [ ] M2 三輪、M3 **三份（或降級為兩份 ×2 輪）**、M4 六條各一批 ×20 全部執行完畢；每一個無效格都有歸因（timeout／panic 附 dump 關鍵 frame）、已重跑並在帳表保留原 artifact。
- [ ] M4 每條計入分母的批次**同時**滿足：ledger 的 before／after 同一背景 PID 且皆 alive，**且** `monotonic_ns` 區間 `.start` < before、`.end` > after；任一缺失或矛盾的批次已標無效、保留 artifact 並重跑。
- [ ] 有效指定執行帳表完整：六條每條計入 PASS 數 ≥27 且分類 (i) 為 0；或有分類 (i) 且已回報 owner、本票停在此處。
- [ ] 程序殘留檢查：**沒有可歸因於矩陣的殘留程序**；無關新 PID 逐一列出理由；若有清理，安全檢查四步齊備。
- [ ] 主要 artifact 的行數存 `WC.txt`、SHA-256 存 `SHA256SUMS.txt`（後者含 `WC.txt`、不含自身），兩份 manifest 自身的 hash 記於 plan；manifest 之後新增的檔案（如清理快照）hash 個別記於 plan；worktree 已移除；主工作區只含本 plan。

## Gate B：文件落地完成 gate（主 repo）

- [ ] 本 plan 先單獨 commit（含 Gate A 證據）；Task 2／3 一個 commit；Task 4 一個 commit（backlog rev 慣例上獨立提交）。
- [ ] `git diff --name-only 583387d..HEAD` 只含：本 plan、`docs/spikes/m3b-results.md`、living 文件、`docs/architecture/pre-m4-readiness-backlog.md`。**零 `.go`**。
- [ ] `git diff --check` 乾淨。
- [ ] `go build ./...`／`go vet ./...`／`gofmt -l .` 於主 repo 各跑一次仍乾淨（文件票也要證明沒碰到程式）。
- [ ] Task 4 closure review checklist 由 owner 逐項勾選；本票不推送，停在關票 review。

---

## 已知缺口（誠實標註，本票不得宣稱消除）

1. **CI runner 冷啟動分布**：本票全部量測來自本機 8 核。移交 B2（D4）。
2. **#6 自然誤紅從未在本機重現**：本票矩陣是第一次把 `internal/proc` 排進負載，即使每條至少 27 次有效指定執行全綠，結論也只是「本機負載下未重現」，不是「不可能發生」。
3. **負載矩陣是本機單一硬體配置**：M3 三份併發的實際 CPU／記憶體壓力與 CI runner 不同構，數字不可跨環境外推。
4. **`internal/evidence` ULID 碰撞**：範圍外、不在矩陣內，維持未驗證。

---

## Gate A 證據（2026-09-04，隔離 worktree `/Users/eason_tseng/scratch-worktrees/b1a4-1`，整合 HEAD `583387d`）

**環境**：worktree HEAD 核對為 `583387ddefa0feeb93b198b45e438c1ea1497971`；`go version` = **go1.27.0 darwin/amd64**——**此為執行者的現場紀錄（stdout 抄錄），沒有獨立的 go-version artifact，亦未由本輪 reviewer 獨立重現**（reviewer shell 為 go1.25.0）；依 owner 裁定不補造事後 artifact，證據層級照實標註；`hw.ncpu`=8、`hw.memsize`=17179869184；`UPDATE_CORPUS` 未設定（`if [ -n "${UPDATE_CORPUS+x}" ]` 未觸發）；`frontend/dist` 自主 repo 複製後 `go build ./...` **`build.rc`=0**、`build.stderr` 0 行。證據目錄 `mktemp -d` 實際路徑 **`/tmp/b1a4.EOr8O9`**；Step 2 起每個工具呼叫都以三行前置（`cd` 絕對路徑、`B1A4=/tmp/b1a4.EOr8O9` 並 `test -d`、HEAD 核對）開頭。主工作區全程零異動（`git status --porcelain` 僅本 plan）。

**M1 逐包基準（五個獨立指令，`-race -p=8 -count=1 -timeout 30m -json`）**：

| artifact | 頂層結果 | 套件 elapsed | 六條 elapsed |
|---|---|---|---|
| `m1-root.json` | **422 PASS＋1 SKIP**＝423、0 FAIL | 234.2s | #4 0.39s |
| `m1-codex.json` | 47 PASS | 2.2s | #1 0.04s、#5 0.31s |
| `m1-assist.json` | 20 PASS | 4.5s | #2 0.51s |
| `m1-claude.json` | 22 PASS | 1.9s | #3 0.31s |
| `m1-proc.json` | 18 PASS | 12.2s | #6 0.64s |

五個 `.exit` 皆 0、五個 `.stderr` 皆 0 行。

**M2 五套件併行 ×3（同一指令、`-p=8`）**：三輪皆 exit 0、stderr 0 行，頂層計數三輪均為 423（422+1 SKIP）／47／20／22／18，FAIL 0。wall time（`monotonic_ns` 差）**240s／282s／313s**。六條 elapsed（r1／r2／r3）：#1 0.02／0.04／0.04；#2 1.28／1.39／**4.14**；#3 0.02／0.03／0.03；#4 0.39／0.45／0.54；#5 0.03／0.04／0.05；#6 2.04／1.66／2.48。

**M3 三份併發 ×1**：三份同時啟動，`m3-c1`／`c2`／`c3` exit 皆 0、stderr 0 行，頂層計數三份均為 423／47／20／22／18，FAIL 0。wall **363s**（低於 M2 單輪 240s 的 4 倍門檻，未觸發降級，三份全部有效）。六條 elapsed（c1／c2／c3）：#1 0.04／0.05／0.06；#2 **6.95／7.89／8.02**（15s 預算，餘裕約 1.9 倍）；#3 0.02／0.04／0.03；#4 0.98／0.93／0.99；#5 0.04／0.05／0.05；#6 4.06／**9.3**／7.69（20s deadline，餘裕約 2.2 倍）。root 套件 elapsed 345–358s、`internal/assist` 47.6–49.6s、`internal/proc` 38.0–41.8s。

**M4 背景負載下六條 focused ×20**：背景 `m4-bg1` 以工具背景模式獨立啟動（PID 41879），`monotonic_ns` start=196822945340885、end=197095461183757（wall 272.5s），exit 0、stderr 0 行，其五包頂層計數 423／47／20／22／18、FAIL 0（背景結果不入分母）。六條各一個前景呼叫，ledger（`m4-ledger.txt`，12 行）逐條 before／after：

| 批 | before（bg、狀態、ns） | after（bg、狀態、ns） | PID 同且皆 alive | start<before | end>after | 判定 | 20 次結果 |
|---|---|---|---|---|---|---|---|
| t1 `TestAppServerTerminateKillsGroup` | 41879 alive 196834045990596 | 41879 alive 196838443842907 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.03–0.23s |
| t2 `TestClaudeAssistFailsLoudOnOversizedLine` | 41879 alive 196849120401502 | 41879 alive 196870174675403 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.67–1.07s |
| t3 `TestMultiTurnSendAndTurnBoundaries` | 41879 alive 196879811180678 | 41879 alive 196882771496908 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.02–0.04s |
| t4 `TestInFlightTurnDoesNotBlockNewSession` | 41879 alive 196895646174315 | 41879 alive 196914792490571 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.44–0.79s |
| t5 `TestAppServerMidStreamDeath` | 41879 alive 196925941478612 | 41879 alive 196930106766852 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.03–0.06s |
| t6 `TestOutputCancellationKillsGrandchildren` | 41879 alive 196940221166584 | 41879 alive 196961994628276 | ✅ | ✅ | ✅ | 有效 | 20 PASS，0.76–1.87s |

單一背景涵蓋全部六批（t6 after 196961994628276 < bg end 197095461183757），未啟動第二份背景，無重跑。六個 `m4-t*-r1.exit` 皆 0、stderr 皆 0 行。

**FAIL 分類**：以 `count.py` 掃描全部 **18** 個 `.json` artifact（M1 ×5＋M2 ×3＋M3 ×3＋M4 背景 ×1＋M4 focused ×6＝18），頂層 `fail` 事件 **0**；六條以外亦無任何 FAIL。分類 (i)–(vi) 全部為 0，無需 goroutine dump 歸因。

**有效指定執行帳表**（完整版存 `ledger.md`，93 行；六條結構相同，此處合併呈現）：

| artifact | 層 | 指定／背景 | 每條執行次數 | 有效／無效 | 排除理由 | 每條計入 PASS 數 |
|---|---|---|---|---|---|---|
| `m1-<pkg>.json`（#1／#5 `codex`、#2 `assist`、#3 `claude`、#4 `root`、#6 `proc`） | M1 | 指定 | 1 | 有效 | — | 1 |
| `m2-r1.json`、`m2-r2.json`、`m2-r3.json` | M2 | 指定 | 1 ×3 | 有效 | — | 3 |
| `m3-c1.json`、`m3-c2.json`、`m3-c3.json` | M3 | 指定 | 1 ×3 | 有效 | — | 3 |
| `m4-bg1.json` | M4 | 背景 | 1 | 不計 | 背景負載，依 D1 不入分母 | 0 |
| `m4-t<k>-r1.json` | M4 | 指定 | 20 | 有效 | — | 20 |
| **合計（六條每條）** | | | | | | **27**（≥27 ✅） |

**程序殘留檢查（三個獨立呼叫）**：`ps-before.txt` 881 行、`ps-after.txt` 881 行；PID 差集 `ps-new-pids.txt` **33 行**，逐行檢視全部為與矩陣無關的新程序：Chrome renderer ×5、`mdworker_shared` ×6、`ManagedClient`／`mdmclient`、本 Claude session 及其 MCP server ×9、Codex app-server ×2、remember plugin hook ×4、取快照的 zsh／`ps` 自身 ×2，以及 **2 個瞬時 `(sleep)`**（PGID 22592）。後者經另一呼叫在 `ps-after.txt` 內回查，其父程序為 `/bin/sh …/TestOutputCancellationKillsGrandchildren2560605473/001/spawn.sh`（PID 22592 leader、22596 subshell），**但這兩個程序在 `ps-before.txt` 內已存在（etime 6:29:39，即約 10:02 CST 啟動）**，早於本矩陣約 6.5 小時——它是 B1a-3 preflight（今日 10:05 前後的診斷 mutation／負載輪）留下的孤兒 fixture 群組，**不可歸因於本矩陣**。`ps-after.txt` 內 `spawn.sh`／`fake-codex-appserver`／`fake-claude`／`.test` 樣式命中僅此 2 行（即該舊群組），無 `go test`／`*.test` 二進位殘留。**結論：沒有可歸因於本矩陣的殘留程序**。

**PGID 22592 一次性環境清理（owner 於 Gate A 複審時授權；非本矩陣殘留、不影響矩陣有效性）**——Step 6 四步各自獨立呼叫：(a) 重新取快照 `ps-pre-kill.txt`（871 行，SHA-256 `a25bd156378fd04640d83841ec4921039a4008d1144a8cff596e44091b9afc2a`），不用舊快照；(b) 另一呼叫 `awk -v g=22592 '$1 == g'` 列出**當下**成員共 4 個：22592 `/bin/sh …/spawn.sh`（leader，PPID 1）、22596 `/bin/sh …/spawn.sh`（subshell）、95574／95575 兩個瞬時 `(sleep)`——**全部為 fixture 程序，無任何非 fixture 成員**；(c) 另一呼叫確認 22592 為正整數、不等於 1、不等於執行 shell 的 PGID（當下為 96393）；(d) 三項皆過後才執行 `kill -KILL -- -22592`，rc=0（`kill-22592.txt`，2 行，`2ee25f76164d7f3cc281f6a3fba6616b6ca9f2af387f6a41a0e9b6a717389953`）；(e) 事後獨立呼叫取 `ps-post-kill.txt`（893 行，`b362ac77b70456556d83a468d42ca61cd5954574b4af7ded6f00a77c6283a9a7`），PGID 22592 成員 **0**、`spawn.sh` 命中 **0**。這三個清理 artifact 於 `WC.txt`／`SHA256SUMS.txt` 產出**之後**建立，故不在兩份 manifest 內，hash 只記錄於本 plan；證據目錄現為 75 個檔案。依 owner 裁定：不寫入 living 文件、不開續票，只在本段與 backlog rev14 修訂記錄留「發現既有診斷 fixture、已安全清理、非本矩陣殘留」一筆。

**artifact 清單與 hash（實際結構，不自我雜湊）**：Gate A 結束時證據目錄共 **72 個檔案**＝70 個主要 artifact＋`WC.txt`＋`SHA256SUMS.txt`。`WC.txt` 記 70 個主要 artifact 的行數，第 71 行為 `total`（共 71 行，SHA-256 `3e9f33eaa36d39b6ef4317444974b199b6010f8eeda783c898b7e1bcac5b653b`）。`SHA256SUMS.txt` 含 70 個主要 artifact＋`WC.txt` 共 **71 筆**，**不含它自己**（其 SHA-256 `45b06705ac8da5f64d0984bbb606fe6f6cba52accfec7bbb59c5546f52b5b0e2` 只記錄於本 plan）。owner 複審時獨立重算 71/71 相符。關鍵 artifact：`m1-root.json` 1803 行 `3f805e83…d691e10`；`m2-r1`／`r2`／`r3.json` 各 2411 行 `8843c48e…`／`d29056d7…`／`4bcdd4c1…`；`m3-c1`／`c2`／`c3.json` 各 2411 行 `c2c48f00…`／`7cc6cae6…`／`16fe6f3a…`；`m4-bg1.json` 2411 行 `3b085d11…`；`m4-t1`…`t6-r1.json` 各 84 行；`m4-ledger.txt` 12 行 `9dec33cc…`；`ledger.md` 93 行 `2f6f506c…`；`ps-before.txt` `fa1ef295…`、`ps-after.txt` `2dabe585…`、`ps-new-pids.txt` `7d9425f5…`。完整 64 位 hash 見 `SHA256SUMS.txt`。這些檔案不進 repo。

**收尾**：`git worktree remove --force` 後 `git worktree list` 只剩主 repo；主工作區 `git status --porcelain` 僅本 plan；HEAD 與 `origin/main` 皆為 `583387d`。

**rev6 提出、owner 已裁定的兩項**：(1) PGID 22592 授權清理，已依上段完成；其存在期間涵蓋整個矩陣，對負載數字的影響方向是「更保守」，不影響有效性。(2) 不寫入 living 文件、不開續票；只在本 plan 與 backlog rev14 修訂記錄留一筆。

**觀察（不改變判定）**：#2 在 M3 三份併發下最長 8.02s（預算 15s），#6 最長 9.3s（deadline 20s），餘裕分別約 1.9 倍與 2.2 倍，明顯低於單跑時的 29 倍與 31 倍；CI runner 若比本機 8 核更弱，這兩條是最先逼近預算的。**owner 裁定**：此觀察寫入 §7.1 與 living 文件，**明標為本機 8 核、M3 三份併發下的觀察，不外推至 CI**；B2 驗收條件的承接範圍由 #3／#6 擴為 **#2／#3／#6** 的 CI 冷啟動與 required-check 耗時分布，B2 估點暫不調整、留到 B2 plan gate 核對（Task 2／3／4 同步）。

---

## 尚未完成

- **Gate A 矩陣結果 owner 已確認實質通過**；rev7 文件修正待短複審。
- Task 2–4 全部未執行（NO-GO 維持到 owner 裁定）。
- 本 plan 未 commit。

---

## 修訂記錄

- rev7（2026-09-04，Gate A 證據複審 CHANGES_REQUIRED）：P1-1 JSON artifact 數 16→**18**（5＋3＋3＋1＋6）。P1-2 manifest 結構改為實際結構：72 個檔案＝70 主要 artifact＋`WC.txt`＋`SHA256SUMS.txt`；`WC.txt` 71 行含 total；`SHA256SUMS.txt` 71 筆含 `WC.txt`、不含自身；Gate A checklist 同步。補證據層級：go1.27.0 為執行者現場紀錄、無獨立 artifact、reviewer 未重現。依 owner 授權完成 PGID 22592 一次性清理（四步獨立呼叫、成員 4 個皆 fixture、`kill -KILL -- -22592` rc=0、事後成員 0），三個清理 artifact hash 記於 plan；不寫 living、不開續票，backlog rev14 留一筆。#2／#6 餘裕觀察寫入 §7.1 與 living（明標本機 8 核 M3 三份併發、不外推 CI）；B2 承接範圍改 #2／#3／#6，估點留 B2 plan gate。矩陣結果與 plan 契約未動，未重跑任何測試。
- rev6（2026-09-04，Gate A 執行與證據回填）：rev5 APPROVED 後於隔離 worktree 對整合 HEAD `583387d` 執行 M1／M2 ×3／M3 三份／M4 六條 ×20，全部 artifact 頂層 FAIL 0，六條每條計入 PASS 數 27，帳表與 hash 清單回填；plan 契約（矩陣定義、紅燈語意、帳表規則）未動。新增兩項待裁定：舊孤兒 fixture 群組 PGID 22592 的清理歸屬、診斷輪 fixture 清理缺口是否登記。記錄 #2／#6 在三份併發下餘裕縮至約 2 倍的觀察。
- rev5（2026-09-04，第四輪 owner CHANGES_REQUIRED）：P1 M4 採計條件改為 PID 存活與時間區間**必須同時成立**，任一證據缺失或兩者矛盾（含時鐘異常）一律該批無效、保留 artifact 並在新背景下整批重跑，移除「以 PID 判準為權威」的覆蓋規則（避免 PID 重用的錯誤採計面）；全部時間戳由 `time.time_ns()` 改為 `time.monotonic_ns()`；Gate A checklist 同步。估點維持 0.95 pt。本輪未執行任何 Go 測試。
- rev4（2026-09-04，第三輪 owner CHANGES_REQUIRED）：P1-1 前置行改 `B1A4=<實際絕對路徑>` 並附例 `/tmp/b1a4.ABC123`，不再重複前綴。P1-2 M4 的 ledger 與背景 `.start`／`.end` 改用 `time.time_ns()` 整數；權威判準定為控制流程＋前後 PID 存活，時間戳只作交叉核對。P1-3 Step 1 先 `mktemp` 再 build，`go build ./...` 保存 `build.rc`／`build.stderr`，非零立即停止 Task 1；Gate A 新增 `build.rc`＝0 勾選項。M3 的 wall time 標記同步改 `time.time_ns()`。P2：D 段標題改「owner 已裁定事項」，D2–D4 移除「提案」「owner 若不同意」改為已定契約；M1 改為顯式 `m1-root`／`m1-codex`／`m1-assist`／`m1-claude`／`m1-proc` 檔名，帳表示例與 Gate A 同步。估點維持 0.95 pt。本輪未執行任何 Go 測試。
- rev3（2026-09-04，第二輪 owner CHANGES_REQUIRED）：P1-1 每個工具呼叫加三行前置（`cd` worktree 絕對路徑、`$B1A4` 以實際絕對路徑重設並 `test -d`、HEAD 核對），fail loud。P1-2 M4 改為每條 focused 一個呼叫，前後各以 `kill -0` 檢查背景 PID 並寫入 ledger，背景以工具背景模式獨立啟動並寫 pid／start／end；背景未涵蓋完整區間即整批無效、在新背景下整批重跑。P1-3 panic／`-timeout` 改依 `running tests:` 與 goroutine dump 歸因：落在六條或其契約路徑即回歸阻擋（明引 #4 pump 卡死會落到 `-timeout` 的測試碼註解），只有可證明卡在他處或資源失效才算該格無效；Global Constraints 與 D1 同步修正。P1-4 新增有效指定執行帳表（artifact × 測試，含指定／背景、有效／無效、排除理由、計入 PASS 數）與「格」粒度、格級／測試級無效、重跑新 artifact 與重跑上限 2 次。P1-5 殘留完成條件改為「沒有可歸因於矩陣的殘留程序」，無關新 PID 逐一列理由；kill 前重新取快照、確認 PGID 正整數且非本 shell PGID、成員只含 fixture，四步齊備才殺。P2：`-p=8` 宣稱改為只固定 go command 併行上限；`UPDATE_CORPUS` 改 `if [ -n "${UPDATE_CORPUS+x}" ]` fail loud；Step 7 補 `wc -l`；Gate A 寫明 M3 三份或降級兩份 ×2 輪，清除「提案值」「或 owner 指定路徑」。估點維持 0.95 pt。本輪未執行任何 Go 測試。
- rev2（2026-09-04，第一輪 owner CHANGES_REQUIRED）：D1–D5 方向全數接受並回寫各項。P1-1 矩陣分母改為「每條至少 27 次有效指定執行」：M4 背景 M2 內六條不計分母但 FAIL 仍保存分類；M3 環境失效降為兩份併發 ×2 輪取至少四次有效執行，失效嘗試不計分母；Gate、§7.1、living 文件措辭全部改為「至少 27 次有效指定執行」，不再固定寫 27/27。P1-2 紅燈分類改為：六條任何 FAIL 先停該格，命中契約／oracle 斷言才是回歸並阻擋，setup／環境／panic／timeout 為該格無效（不紅不綠、調整後重跑），並新增六條契約 vs. setup 斷言對照表。P1-3 證據目錄改 `mktemp -d /tmp/b1a4.XXXXXX`；殘留檢查改為執行前後各一次獨立 `ps` 快照、第三個呼叫比對新增 PID 的完整 `$0`（原 `$5` 比對命中不了 `sleep 0.05`）。P1-4 Task 4 新增 B1b 票面改「更新既有 living 文件」與 B2 驗收條件承接 #3／#6 冷啟動量測。P2 M1–M4 顯式 `-p=8`、前置確認 `UPDATE_CORPUS` 未設定、JSON 頂層計數 python 指令、stderr／exit code 固定保存寫法。估點維持 0.95 pt。本輪未執行任何 Go 測試。
- rev1（2026-09-04）：初稿。承接三張施工票的「留給 B1a-4」事項（五套件矩陣、CI 冷啟動、#6 自然誤紅、rev8 兩處未涵蓋、B1 驗收條件 (2) 未定次數）；提出四層矩陣（每條 27 次）與紅燈語意、living 文件位置與 B1b 分工、§7.1 追加形狀、CI 缺口移交 B2、B1a aggregate 關閉語意五項待裁定。明列 §6.7 不適用與零 `.go` 變更。本輪未執行任何 Go 測試。
