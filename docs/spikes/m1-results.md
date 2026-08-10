# M1 驗收結果

- 執行依據：`docs/architecture/sdlc-workbench-m1-plan.md`（v13 APPROVED，SHA256 `c2c1f237464e7b415e86ce3df4513ef1a0e53bda41955d1f62f31c30b6f142ac`）
- 基線：main @ `05415b9`；branch `m1-mvp`（Task 1–12 全數 commit）
- 狀態：自動 gate 全 PASS；**V0–V6 live 驗收 2026-08-10 執行**（`wails dev` + Playwright 驅動 browser 前端，與原生視窗共用同一 backend；證據見下）

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
| `npm --prefix frontend run test` | PASS | 23/23（store 14 + scroll 3 + sanitizer 4 + ChatPanel thinking 2；歷次修正後最終數） |
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

## V0–V6 live 驗收（2026-08-10 執行）

| 項目 | 結果 | 證據 |
|---|---|---|
| V0.1 claude allow | PASS | touch probe → 彈窗「[claude] 工具權限請求：Bash」→ Allow → `.workbench/probe/m1-v0-allow.txt` 存在；audit request（含 command input）+ decision |
| V0.2 claude deny | PASS | Deny+理由 → `m1-v0-deny.txt` 不存在、turn 正常收尾（done） |
| V0.3 claude 逾時 | PASS | `WORKBENCH_APPROVAL_TIMEOUT=5s` 重啟後 touch prompt 不操作：彈窗 5s 自動消失、`v03-timeout.txt` 不存在、`approval_decision: timeout` envelope 落檔、turn 正常收尾 |
| V0.4 claude resume | PASS | backend 重啟後 resume `9308172d…`：正確回答前 session 被 deny 的檔名 `m1-v0-deny.txt`；未綁定 id resume → `resume refused: … bound to ""` |
| V0.5 claude Terminate | PASS | streaming 中 Terminate → session:done `exitCode:143`、秒級收尾 |
| V0.6 Recorder 失敗可見 | PASS | chmod 500 recordings → StartSession 回 `permission denied`（可見失敗、不無聲降級）；chmod 755 後 V5 錄流正常 |
| V0.7 codex approval + resume | PASS | untrusted：`touch` 彈核可（`echo hi` 被 codex 判 trusted 自動放行，如實記錄）→ Allow 檔案存在／Deny 不存在＋audit accept/decline；End 後 thread/resume `019fea62…` 正確回答前 session 第一題答案 2 |
| V0.8 codex auth | PASS（AuthStatus only） | claude `loggedIn:true`、codex `chatgpt/plus`；**logout/login 循環未授權，記 waiver（沿 M0）** |
| V0.9 replay 迴歸 | PASS | `TestContractReplay` + `TestReplay` ok |
| V1 多輪 | PASS | claude 3 輪（42→52→152，第 2、3 輪引用前文、session `8e2fbcfe…` 恆定、自然 End exit 0）；codex 3 輪（42→21→63、同 thread `019fea62…`、server 不重啟）；`codex-m1-chat.jsonl` 單檔 3× turn/start + 3× turn/completed、meta 恰一份（ProcessStillRunning、ExitCode nil） |
| V2 SC2 四問 | PASS | 三輪當下 StatusBar 文字記錄：claude `任務：m1-v1-claude／完成／session 8e2fbcfe…／tokens 6/9（無 *）／$1.3014`；codex `任務：m1-v2-codex／等待核可（approval 時刻）／session 019fea62…／tokens 66470/101*（tooltip「provider 最新回報值」）／—`。存檔截圖 `docs/spikes/evidence/v2-{claude,codex}-statusbar.png`（驗收後補截的單輪 session，同欄位形狀；原三輪截圖誤刪，見偏差 6） |
| V3 normative UI | PASS | streaming 逐 token＋游標；follow-tail：長回覆中 scrollTop 置 0 後內容持續增加不跳底；tool 卡片：claude approval 彈窗含 `Bash` + command input、codex `tool_use /bin/zsh -lc 'echo hi'（inProgress→completed）` per-type＋status；Timeline toggle 收合（181px→0）。thinking 摺疊：live 未出現 thinking 內容（claude -p 無 extended thinking），改以 ChatPanel component test 驗收（`ChatPanel.test.ts`：thinking 累積渲染於**預設收合**的 `<details>`、無 thinking 時不渲染；store 測試斷言 thinking 逐 delta 累積） |
| V4 檔案樹＋預覽 | PASS | 懶載入樹瀏覽 repo；`v4-preview-test.md`：h1/strong 渲染、mermaid 區塊→SVG、`<script>` 被 DOMPurify 消毒；`sample.mmd` 編輯存檔 1 秒內重渲染（"Rerendered" 顯示）；symlink 指 /etc → `path escapes workspace` 錯誤 |
| V5 approvalPolicy | PASS | untrusted：touch 彈核可（V0.7）；never：同 prompt 不彈、直接執行（`v5-never.txt` 建立、該 session approval envelope 0 筆）。**風險如實記錄：never 下寫入指令無人審即執行** |
| V6 稽核 JSONL | PASS | `events.jsonl` 621 envelopes；event_id 嚴格單調；15 個 `role=user` message **全部**緊跟 `state_change(waiting)`（coordinator user-first live 證據）；抽 3 筆對照 UI（user text＝氣泡、approval raw＝彈窗內容、result cost 0.412835＝StatusBar $0.4128）；kinds 涵蓋 approval/approval_decision/usage/tool_use；無 stream_error（AuditErr nil） |
| 封裝 smoke | PASS（owner 2026-08-10 執行） | bundled `.app`（Finder 啟動，workspace fallback `home`、tools `bundle`、startup_error 空）：claude 2 輪 session（result ok、cost 累計、其中一輪 approval E2E Allow）＋codex 1 輪（init thread id、usage `provider_latest` 21858/72、result ok）；`~/.workbench/events.jsonl` event_id 單調。過程附帶確認：active session 期間切 provider 下拉不影響進行中 session（訊息仍送原 provider），New 之後才以新 provider 開 session——符合設計 |

### Live 驗收中發現並修正（dev 過程 fix，均有測試或迴歸證據）

1. **main.ts 未註冊 Pinia**（App mount 即炸；vitest 自建 pinia 故未攔到）→ `createApp(App).use(createPinia())`。
2. **codex 線缺 init envelope**（M0 行為在 T6 重寫時遺漏；StatusBar 任務/session 顯示 `—`）→ `startCodexHost` 成功後 Emit `KindInit`（queue 至 Accept 後 flush，順序 user→waiting→init）；`TestCodexFirstTurnCompletedBeforeResponse` 增 init envelope 斷言。
3. **result 後殘留 streaming 游標氣泡**（message 落定後的殘餘 delta 產生空氣泡）→ store `result` 分支落定／移除尾端 streaming 氣泡；vitest 新增 1 測試（21/21）。
4. **多視窗殘留 approval 彈窗**（dev 模式原生視窗＋browser 前端共用 backend；Resolve 只關掉按鈕所在前端）→ `ResolveApproval` 成功後廣播 `approval:dismiss(cause:resolved)`。

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

## 報告審查修正（merge 前審查，CHANGES_REQUIRED → 已修）

2026-08-10 報告審查 2 P1 + 1 P2（多項），全數修正：

1. **P1：init 把 waiting／done 重設成 idle（違反 SC2）**——reducer 對 `KindInit` 無條件切 idle；封裝 codex 錄流（user→waiting→init→idle）與 claude 多輪錄流均重現，等待回覆期間 StatusBar 錯顯 idle。修正：`KindInit` 改為**狀態中性**（新 session 的 idle 本由 `Reset()` 建立）。新測試：`TestReducerInitIsNeutral`（waiting→init 仍 waiting、done→重複 init 仍 done）、`TestInitDoesNotRegressState`（coordinator flush user→waiting→init 恰一個 state_change=waiting、不追發 idle）。修後 live 抽驗事件序列見下。
2. **P1：V3 thinking 摺疊未驗收**——live 未觸發 thinking，原報告仍計入「全數完成」過強。補 `ChatPanel.test.ts` component 測試（thinking 累積渲染於預設收合 `<details>`、無 thinking 不渲染）＋ store thinking 累積斷言；V3 證據列同步改寫。
3. **P2：報告 stale／契約差異**——vitest 計數更正（23/23）；殘餘風險「封裝 smoke 未執行」標記 stale；V0.6 失敗面向差異列為偏差 9。

### 修後 live 事件序列抽驗

2026-08-10 dev 模式抽驗（events.jsonl 逐 envelope 驗證）：

- claude 兩輪（`m1-fix-verify-claude`，48 envelopes）：兩輪皆為 `message(user) → state:waiting → init → state:streaming → message → result → state:done`——init 之後**零** idle state_change。
- codex 一輪（`m1-fix-verify-codex`，22 envelopes）：`message(user) → state:waiting → init → … → result → state:done`——同樣無 idle 追發。
- bundled codex 一輪（owner 2026-08-10 以重建後 `.app` 執行）：`~/.workbench/events.jsonl` 第 205–238 筆共 34 envelopes、task `m1-final-bundled-codex`、session `019feab4-6199-7032-b941-11816da7f116`；序列 `message(user) → waiting → init → streaming → message(assistant) → usage → result → done`，**init 後 idle state_change 0 筆**；回覆精確為 `M1-CODEX-INIT-NEUTRAL-PASS`、usage `provider_latest` 21870/17、34 個 event_id 嚴格遞增；證據片段 SHA-256 `87556dbe9a8d8b63e8b2866644a0d205948e9d1228d0cf461e6975f9ac5f797f`。End 後 Workbench 與長駐 codex app-server 均於 2 秒內退出（AppleScript 關閉回報 -128，程序檢查確認非殘留）。

## 偏差記錄

1. **MermaidPane.vue 刪除時點**：plan 排在 Task 10 commit；實際移至 Task 11 commit（App.vue 重寫同時），避免中間 commit 引用已刪檔案。內容無偏差。
2. **SettingsBar resume 輸入**：plan 樣張為 `v-model="s.resumeInput"`；Pinia options-store getter 唯讀，實作為元件內 writable computed 委派 store `resumeInput` getter ＋ `setResumeInput` action。行為等價（per-provider 記憶），有 vitest 覆蓋。
3. **FileTree 實作形狀**：plan 描述「懶載入樹（約 60 行）」未附完整碼；實作採 Vue 3 檔名自遞迴 SFC，每層展開時 mount 子層才呼叫 `ListWorkspace`（懶載入語意一致）。
4. **`ListWorkspace("..")` 拒絕**：`filepath.Clean("/"+rel)` 會把 `..` 中和成 root（不逸出但也不報錯），與 plan 測試「必須拒絕」不符；實作補 `..` 成分顯式檢查（拒絕、不無聲重導）。
5. **PreviewPane mermaid 錯誤顯示**：錯誤文字以 `textContent` 寫入（不進 HTML sink），較 plan 樣張更嚴格（XSS 防護）。
6. **V2 原始截圖誤刪**：三輪驗收當下的 StatusBar 截圖在清理測試產物時誤刪（無備份）；當下欄位值已有文字記錄（見 V2 列），另以單輪 session 補截同形狀截圖存 `docs/spikes/evidence/`。後續證據截圖一律直接存 evidence 目錄。
7. **V0.7 `echo hi` 未觸發核可**：codex 0.146.1 的 untrusted policy 將 `echo` 判定為 trusted 指令自動放行；改以寫入指令（`touch`）驗證核可流。如實記錄，非 app 缺陷。
8. **claude 每輪皆發 init 事件**：claude 2.1.223 多輪模式每輪回一個 `system/init`（session_id 恆定）。此行為曾使 reducer 把 waiting 打回 idle（報告審查第一輪 P1，已修——init 改為狀態中性，見「報告審查修正」節）。
9. **V0.6 失敗面向與 plan 敘述不同**：plan 寫「session 帶 recorderError」；實際 M1 行為是 recorder 初始化失敗時 `StartSession` 直接回 error（fail-loud 更早、session 不啟動）。行為更嚴格，但非完全相同——如實列為偏差。

## 殘餘風險

1. ~~封裝 smoke 未執行~~（stale，第一輪報告審查指正）：封裝 smoke 已由 owner 執行 PASS（見 V 表）。
2. claude 自然結束（V1 End exit 0）與 Terminate（V0.5 exit 143）均 live 驗證；快速退出／abort 路徑由 production-path barrier 測試覆蓋（未 live 重現）。
3. 測試穩定性：appcore barrier 測試依 channel 同步（deterministic）、三個 app-level barrier 測試 `-race -count=30` 通過；M0 race-test 穩定性註記沿用。
4. bundle 873M 未瘦身（plan 明列 M1 不處理）。
5. thinking 摺疊 UI 未經 live thinking 內容觸發（claude -p 本輪無 extended thinking）；元件與 store 邏輯有測試覆蓋。
6. dev 模式雙前端（原生視窗＋browser devserver）共用 backend 時，approval 彈窗殘留已以 dismiss 廣播修正；bundled 單視窗不受影響。

## SC2 達成聲明

V2 live 驗收確認：單一 StatusBar 同時回答 SC2 四問——目前任務（taskLabel）、agent 狀態（含「等待核可」時刻的黃色高亮）、session／thread id、資源消耗（tokens 累計或 provider 最新值＋cost；語意以 `*`＋tooltip 明示，不把最新回報值謊稱累計）。**SC2 達成**。

## 錄流 digest（SHA-256）

```
d4d62ace79c0805dec2491f2c4e2880bc18850e61027c046510fc8993d3f5aa3  claude-m1-chat.ndjson
eca302e729b0d42a2f7a38bc4790f21a37064405792ec8fc463c7dc23f957d60  claude-m1-chat.meta.json
5fd7a03189834c115189fb37f562362171003aae967e599dc7c08e3a909d590c  codex-m1-chat.jsonl
6d01e5489a1b2668c074f51a43a28529e2c14afe31863dbeef71eda2dc0c82e0  codex-m1-chat.meta.json
```
