# M1 驗收結果（進行中）

- 執行依據：`docs/architecture/sdlc-workbench-m1-plan.md`（v13 APPROVED，SHA256 `c2c1f237464e7b415e86ce3df4513ef1a0e53bda41955d1f62f31c30b6f142ac`）
- 基線：main @ `05415b9`；branch `m1-mvp`（`9564727`…`91fba7e`，Task 1–11 全數 commit）
- 狀態：**自動 gate 全 PASS；V0–V6 live 驗收待 owner 操作**（見「待驗收」節）

## 版本基線（CLI pin 不變聲明）

| 項目 | 版本 | 備註 |
|---|---|---|
| claude CLI | 2.1.223 | 沿 M0 pin，M1 未變更 |
| codex CLI | 0.146.1 | 沿 M0 pin，M1 未變更 |
| Go | 1.26.5 darwin/amd64 | |
| BAT 參考 | commit `72dc4ba`（non-normative 參考） | 未檢出漂移 |

## Task 4 多輪判定與 usage VERDICT（已完成，commit `1c94a1a`）

- 多輪判定：**Plan A GO**——pinned claude 2.1.223 in-process 多輪成立（同 session_id、前後文保留、stdin 關閉後自然 exit 0）。Task 4b fallback 未觸發，`internal/claude/multiturn*.go` 不存在。
- usage VERDICT：**per-turn**（turn1 output=642、turn2 output=9 → o2 < o1/2）→ Manager `ClaudeUsageCumulative=false`（累加制，UI 標示 `session_total`、claude tokens 無 `*`）。
- 證據：`testdata/fixtures/claude-multiturn-shape.ndjson`（已消毒）、`cmd/probe-multiturn` 輸出（M1-T4 記錄）。

## 自動驗證（已執行，2026-08-10）

| Gate | 結果 | 證據 |
|---|---|---|
| `go vet ./...` | PASS | 無輸出 |
| `go test -race ./... -count=1` | PASS | appcore/approval/claude/codex/contract/proc/recorder/main 全 ok |
| V0.9 replay 迴歸 | PASS | `TestContractReplay`（claude）、`TestReplay`（codex）各 ok |
| `npm --prefix frontend run test` | PASS | 20/20（store 13 + scroll 3 + sanitizer 4） |
| `npm --prefix frontend run build` | PASS | vue-tsc + vite build 成功 |
| `wails build` | PASS | `build/bin/sdlc-workbench.app` 產出 |
| `scripts/bundle-clis.sh` | PASS | bundled: 873M |
| grep gate `ClaudeTurns` | PASS | internal/ cmd/ app.go 均 0 hit |
| workspace 安全 6 例 | PASS | `TestWorkspaceReadSecurity`（dot-dot／symlink 檔／symlink 目錄／>1MB／list escape／正常讀） |
| clean working tree | PASS（本檔 commit 後複驗） | |

單元測試涵蓋（依 plan 驗證策略總表）：
- appcore：RecordingLease barrier 恰一次／首錯保留／stop-err join；coordinator ownership barrier／stale-accept／approval 入列順序／reject；StartSession 原子交易；Submit×End lifecycle barrier（`TestSendThenEndBarrier`、`TestEndThenSendRejected`、`TestEndDuringStartingIsRejected`、`TestCancelEndSessionRestoresActive`、stale token）；EndSessionFlow busy／teardown-error／冪等；Pump quiesce／timeout；CloseSequence 三路徑（正常／逾時升級／卡死＋`Exited=false`）；Close×Emit barrier＋fail-loud ID 遞增；usage 語意雙分支＋semantics 欄位；M0 turn 追蹤遷入（turntrack）。
- codex：wire lifecycle、early-completed latch、空 turn id、barrier 單 wire request、resume、session-scoped 錄流雙輪。
- contract：ULID 單調、Wrap role precedence、raw fallback、reducer 全轉移。
- 前端：user-envelope 氣泡、tool-echo 路由、delta 累積、usage 覆寫、costDisplay、busy、submit 分流／失敗、雜訊分組、note／applyDone／resumeInput、sanitizer 4 例、isAtBottom 3 例。

## 待驗收（需 owner 操作 live app）

以下項目需啟動 `build/bin/sdlc-workbench.app`（或 `wails dev`）與已登入的雙 CLI：

- [ ] V0.1 claude approval allow（touch probe → 彈窗 → Allow → 檔案存在 + audit）
- [ ] V0.2 claude approval deny（檔案不存在、turn 正常收尾）
- [ ] V0.3 claude approval 逾時自動 deny（`WORKBENCH_APPROVAL_TIMEOUT=5s`）
- [ ] V0.4 claude resume（跨 app 重啟引用前文；異 cwd refused）
- [ ] V0.5 claude Terminate（5s 內收尾、exit 143）
- [ ] V0.6 Recorder 失敗可見（chmod 500 recordings → recorderError）
- [ ] V0.7 codex approval allow/deny（untrusted）＋ thread/resume
- [ ] V0.8 codex AuthStatus（logout/login 循環僅在 owner 事前明示同意時執行；未同意記「未執行（沿 M0 waiver）」）
- [ ] V1 多輪：claude 3 輪 + codex 3 輪（同 thread、server 不重啟；codex 三輪單一 `codex-m1-chat.jsonl`、meta 恰一份）
- [ ] V2 SC2 四問（StatusBar 截圖；claude tokens 無 `*`、codex 恆 `*`＋tooltip）
- [ ] V3 streaming／thinking 摺疊／follow-tail 上捲停跟隨／tool 卡片（claude `Bash(touch …)`、codex per-type＋status）／Timeline 摺疊 toggle
- [ ] V4 檔案樹＋預覽（`.md` 含 mermaid 區塊、`.mmd` 存檔 1 秒內重渲染、symlink 拒絕一例實測）
- [ ] V5 approvalPolicy（untrusted 彈核可；never 不彈——行為與風險如實記錄）
- [ ] V6 稽核 `events.jsonl`（event_id 單調、抽 3 筆對照 UI、user message 先於該輪首個 provider event、無 stream_error）
- [ ] 封裝 smoke（bundle 後雙 provider 各 1 輪）

## 實作審查修正（第一輪 code review，CHANGES_REQUIRED → 已修）

2026-08-10 外部審查 2 P1 + 2 P2，全數修正並補 production-path barrier 測試：

1. **P1：codex 首輪 turn/completed 遺失（永久 busy）**——notification handler 只查 `a.runner`，但 runner 於 `StartTurn` 成功後才發布；completed-before-response 或發布前抵達時 earlyEnded latch 收不到。修正：runner 於 `EnsureThread` 成功後、`StartTurn` 前發布（`startCodexHost`），後續 recorder／StartTurn 失敗一律原子 rollback（`a.runner` 清回 nil）；同一修正使首輪空窗中的 approval envelope 帶 thread ID。新測試 `TestCodexFirstTurnCompletedBeforeResponse`：fake wire（in-memory pipes 上的真 `codex.Conn`）固定 approval→completed→response 惡意順序，斷言 alreadyEnded 對消、不殘留 busy、第二輪可送、approval envelope `SessionID=="t1"`。
2. **P1：claude 快速退出被接受成 active 的死亡 session**——自然結束 goroutine 在 phase=starting 時打 `EndSessionFlow` 得 `ErrStartInProgress` 後放棄，隨後 `AcceptSubmit` 標成 active。修正：`startClaude` 回傳 commit callback，goroutine 於 pump 收乾後**先等 start 交易 commit/abort**：accepted → `EndSessionFlow`；aborted → 直接 `claudeTeardown` 清理＋session:done。新測試 `TestClaudeFastExitDoesNotLeaveDeadActiveSession`：fast-exit fake CLI＋`hookAfterProviderStart` barrier 保證 pump 先於 Accept 收乾，斷言 session:done 發出、manager 終態 idle、下一個 session 可建立。
3. **P2：claude 啟動失敗未回收 broker**——broker 發布後的 MCP config／recorder／`claude.Start` 失敗路徑以 `defer` rollback（未 commit ownership 即 `Close` 並清 `a.broker`）。
4. **P2：`// spike quality` 註解殘留**——`ApprovalDialog.vue` 註解移除；未使用的 M0 `Transcript.vue` 整檔刪除。全 repo `spike quality` grep 為 0。

配套重構：`wireCodexHandlers(srv)` → `wireCodexConn(conn)`；`startCodex` 拆出 `startCodexHost(codexHost, …)`（最小 host 介面：`Conn`/`Argv`/`StderrSnapshot`，fatal watch 改 `conn.Done()`）；UI 事件出口統一 `a.emit`（`emitUI` 測試注入 seam）。修正後全套 gate 重跑：`go vet`、`go test -race ./... -count=1`、vitest 20/20、frontend build、`wails build`、ClaudeTurns grep 0——全 PASS。

### 第二輪 review（CHANGES_REQUIRED，1 P1 + 1 P2 → 已修）

1. **P1：commit(false) 可能永久等不到 teardown**——reaper 原本先等 process EOF 再讀 commit 結果；abort 時 MultiTurn CLI 仍在等下一輪輸入（done 不會關），teardown 永不執行（shutdown 與 StartSession 交錯即可觸發：`Manager.Close()` → Accept `ErrClosed` → commit(false) 滯留）。修正：reaper **先讀 commit 結果**——false 立即 `claudeTeardown`（CloseSequence 關 stdin → 界限內收乾）；true 才等 done 走自然結束流程。新測試 `TestClaudeAbortedStartIsReclaimed`：long-running fake CLI（讀首則訊息後等 EOF），hook 於 Accept 前 `Manager.Close()`，斷言 `StartSession` 回 `ErrClosed`、session:done 界限內發出、`claudeSess`／`activeProv`／`broker` 全清除、lease finalized（meta `exit_code: 0` 落檔）。
2. **P2：codex 測試背景 goroutine 呼叫 `t.Fatalf`**——approval 解決移回主 goroutine（envelope 於 codexApproval 阻塞前已 Emit，斷言不依賴 resolve 時序），失敗一律由主 goroutine Fatal。

三個 barrier 測試以 `-race -count=30` 重複通過；全套 gate（vet／race suite／vitest 20/20／frontend build／`wails build`）重跑 PASS。

## 偏差記錄

1. **MermaidPane.vue 刪除時點**：plan 排在 Task 10 commit；實際移至 Task 11 commit（App.vue 重寫同時），避免中間 commit 引用已刪檔案。內容無偏差。
2. **SettingsBar resume 輸入**：plan 樣張為 `v-model="s.resumeInput"`；Pinia options-store getter 唯讀，實作為元件內 writable computed 委派 store `resumeInput` getter ＋ `setResumeInput` action。行為等價（per-provider 記憶），有 vitest 覆蓋。
3. **FileTree 實作形狀**：plan 描述「懶載入樹（約 60 行）」未附完整碼；實作採 Vue 3 檔名自遞迴 SFC，每層展開時 mount 子層才呼叫 `ListWorkspace`（懶載入語意一致）。
4. **`ListWorkspace("..")` 拒絕**：`filepath.Clean("/"+rel)` 會把 `..` 中和成 root（不逸出但也不報錯），與 plan 測試「必須拒絕」不符；實作補 `..` 成分顯式檢查（拒絕、不無聲重導）。
5. **PreviewPane mermaid 錯誤顯示**：錯誤文字以 `textContent` 寫入（不進 HTML sink），較 plan 樣張更嚴格（XSS 防護）。

## 殘餘風險

1. V0–V6 live 驗收未執行（上表）；coordinator 的「app.go 忘記呼叫」殘餘情境依 plan 由 V1／V6 live 把關。
2. claude 自然結束路徑（pump 收乾 → EndSessionFlow）在單元層以 CloseSequence 測試覆蓋，但與 live CLI 的整合行為待 V1／V0.5 驗證。
3. 測試穩定性：appcore barrier 測試依 channel 同步（deterministic），無 sleep 競態；M0 已知 race-test 穩定性註記沿用。
4. bundle 873M 未瘦身（plan 明列 M1 不處理）。

## SC2 達成聲明

待 V2 截圖存證後補記。

## 錄流 digest

待 V1／V0 錄流產出後補 SHA-256 清單。
