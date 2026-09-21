# B3a-2b-2 Claude same-App recovery 限定檢查點結案紀錄

> 建立：2026-09-22（台北時間）。**日期說明**：本次 browser run 的 run-id 前綴是 UTC（`20260921T232407Z`），對應台北時間 2026-09-22 07:24；本檔日期採台北時間，內文時間一律標註時區。
>
> 範圍：**同一 App 執行期、同一 WSID／canonical cwd 的「第一輪 fresh → 第二輪 `--resume S`」兩輪 approval allow**。
> **這不是 Task F 整體完成，也不是 B3a-2b aggregate 完成。** deny matrix、真實 provider、cold restart、多 session、正式 browser CI 與重複穩定性都未做、未驗。

## 0. 一句話範圍

證明的是：**真 App `StartSession(provider=Claude)` → 受控假 Claude CLI（fresh）→ 真 App 產生的 MCP config → 真 `mcp-approval` 子程序 → 真 `approval.Broker`／`pumpApprovals` → 瀏覽器看見 approval → 真的點 allow → 第一輪完成內容 → 第一輪 CLI／MCP 自然退出（OS 觀測確認）→ 真的點 end-session 並等到這次點擊自己的操作結果 → 同一個 pane 再次送出 → 新的假 CLI 帶 `--resume S` → 第二輪 allow → 第二輪（不同）內容顯示** 這一條鏈，**一次成功、單一樣本**。

**provider 是受控的假 Claude CLI。真實的是 App、MCP、broker 與 UI。本次沒有驗證任何真實 Claude provider 行為。**

`--resume` 的位置是一手讀碼：`internal/claude/session.go:45-47` 的 `if c.Resume != ""` 分支在 `return` 之前，因此 resume 恰好接在既有序列尾端。

## 1. Base、最終 run 與工具鏈

| 項目 | 值 |
|---|---|
| base commit | `c86b354f0290e1a2ff6244bf2c4a53a7323c84ee`（已合併的 F1／F2 交付） |
| 工作樹／branch | `ai-se-worktrees/b3a-2b-e1claude`／`b3a-2b-2/task-e1-claude-recovery`（文件收尾審查時尚未 commit） |
| 唯一 browser run | `20260921T232407Z-25d858`（**PASSED，第一次嘗試即通過**，rc=0） |
| 牆鐘 | 2026-09-21 23:24:06Z → 23:26:09Z（`1 passed (2.0m)`，test 本體 14.2s） |
| 入口 | `npm run test:e2e:scenario`，`E2E_SCENARIO=claude-approval-recovery`、`E2E_KEEP_ARTIFACTS=1`，1 worker／0 retries，恰一個 test |
| 核定 App binary | `<repoRoot>/build/bin/sdlc-workbench.app/Contents/MacOS/sdlc-workbench`，pid 15472 |
| 該 binary sha256 | `52d97aa9a1f8fee34c347aeefb140f0aab44a09eea2fb50bbc9dd35a52ce67f9`（**當次留存值**；reviewer 複核時該 binary 已不存在，刪除原因未獨立驗證，**未重新計算**） |
| 工具鏈 | Node v26.8.1；Go1.27.1（`/usr/local/Cellar/go/1.27.1`，由 run-state 的 link 指令路徑佐證） |

## 2. 交付內容（18 檔，**未修改 production 程式碼**）

全部落在 `frontend/e2e/` 與 `frontend/package.json*`；Go／Vue／store 一行未改（以 `git status --porcelain` 濾掉這兩個前綴後為空反向確認）。

**新增 3 檔**
- `e2e/support/scenario/claudeRecoveryJudge.ts`（390 行）——跨輪判定
- `e2e/support/scenario/claudeRecoveryJudge.selftest.ts`（378 行）——59 條正負控制
- `e2e/scenarios/claudeSessionRecovery.spec.ts`（317 行）——browser 檢查點

**修改 15 檔**（tracked diff 合計 886 insert／89 delete；**此數字不含上面三個新檔的行數，兩者統計範圍不同，須分開標示**）：`global-setup.scenario.ts`、`global-teardown.ts`、`support/env.ts`、`support/executionMode.ts`、`scenario/claudeApprovalProtocol.ts`、`scenario/claudeScenarioCli.ts`、`scenario/fakeClaudeCli.ts`、`scenario/scenarioTripwire.ts`、`scenario/scenarios.ts`、`scenario/specRouting.ts`、`scenario/f2Routing.selftest.ts`、`scenario/f2RunEnv.selftest.ts`、`scenario/fakeClaudeCli.selftest.ts`、`package.json`、`package.json.md5`。

### 2.1 四個關鍵設計決定（皆由 reviewer 事前指定或事後擋下後定案）

1. **輪次絕不由待驗 argv 推導。** 改用 run 專屬目錄（wrapper 烤入 `FAKE_CLAUDE_ROUND_DIR=<toolsDir>/claude-rounds`）內的 `mkdir`（非 recursive，POSIX 原子）作為**有序排他 claim**。`--version` 探測在對話分支之前 return，**不佔輪次**；第 n 輪必須看到 `round-<n-1>/done.json` 才准啟動；超出核定輪數仍取得號碼（以便留證）但判違規；缺 `FAKE_CLAUDE_ROUND_DIR` 一律 fail closed、**不自行補建**。
2. **每輪證據獨立且禁止覆寫。** 兩輪案落檔在排他新建的 `<evidenceDir>/round-<n>/`；已存在即代表有人拿別輪證據冒充，當場失敗且不覆寫。**共用的 config／socket 路徑要求「相同」而非「不同」**——本次實測兩輪的 socket 都是 `approval-0.sock`，坐實了「socket index 會被回收重配、不能拿來判輪次」。
3. **S 的三處交叉核對。** 期望 S 只來自固定 builder `buildClaudeRecoveryExpectation(runId)`；核對 (a) 第一輪 CLI 實際宣告的 init **原始事件 line**、(b) App 兩份 registry 的綁定、(c) 第二輪 argv。**不從第二輪待驗輸出反填期望。**
4. **tripwire 的預期輪數來自已驗證 scenario identity。** `determineExecutionMode` 增加 `scenarioKind`，teardown 據此傳 `expectedConversationCalls`／`expectedResume`；未知或跨來源不一致仍 fail closed。legacy 單輪契約未放寬（預設恰一次、預設禁止 resume，且現在由假 CLI 自己守，不只靠 teardown）。

## 3. 本次 run 的兩輪身分鏈

**相同**（同一 session／WSID／cwd／config）

| 項目 | 值 |
|---|---|
| S | `b3a2b2-claude-session-20260921T232407Z-25d858` |
| 兩輪 init 原始 line | `{"type":"system","subtype":"init","session_id":"<S>"}` |
| WSID | `01M334M3Q80006S9MSYD3TY6QF`（兩輪 DOM `data-test-wsid` 相同） |
| canonical cwd | `/private/var/folders/.../T/b3a1-e2e-psaDB4` |
| MCP config | `<stateDir>/mcp-01M334M3Q80006S9MSYD3TY6QF.json`（兩輪逐字相同） |
| socket | `<stateDir>/approval-0.sock`（**兩輪相同**，見 2.1 第 2 點） |

**不同**（第二輪確實是新啟動的程序與新的 approval）

| 項目 | round 1 | round 2 |
|---|---|---|
| CLI pid | 16812 | 16981 |
| MCP 子程序 pid | 16883 | 17039 |
| `ppid` 核對 | = 16812 | = 16981 |
| approval id | `3ba1cb690e801b43c947e574cb78a223` | `86390f65abdfa7631b10cb9cda4f0e8e` |
| argv | 14 token，**無** `--resume` | 16 token，尾端 `--resume <S>` |
| prompt／marker／完成內容 | `…-r1-…` | `…-r2-…` |

**第二輪的 resume 在三份紀錄中一致**：假 CLI 自存的 `round-2/argv.json`、結構化 `fake-tools/claude-argv.jsonl`，以及 **`run-state.json` 的 OS 程序表**——前兩份由同一個假 CLI 寫出，不是獨立來源；OS 程序表則由 harness 透過 `ps` 另行觀測，記錄真 App（pid 15472）子程序的 command 字串，並非 spawn 當下的事件紀錄。

**MCP 鏈**：兩輪各 5 frame（`initialize` / `result#1` / `notifications/initialized` / `tools/call` / `result#2`）。round-2 的 `mcp-stdout-raw.txt` 原始行由真 `mcp-approval` 回，`serverInfo.name="workbench"`、`protocolVersion="2025-06-18"`，result text 為 `{"behavior":"allow","updatedInput":{...r2 marker...}}`。兩輪 CLI 自判 `judgement.json` 皆 `{"problems":[]}`；跨輪 judge `violations=[]`。

**UI**：全程真點擊 `[data-test="create-claude"]`／`composer-send`／`approval-allow`／`end-session`。**未**直接呼叫 `StartSession`／`EndSession`／`ResolveApproval`，**未**改 store／registry，**未**用 Go helper 代替 UI 決議。完成內容以 `[data-test-wsid=<WSID>] .bubble.assistant` 逐輪 `toHaveCount(1)` 核對（不是整頁 `body` contains），且第二輪開始前先斷言第二輪內容 `toHaveCount(0)`。

## 4. 取證時點（本次最關鍵的修正）

第二輪的 init 會**重新** `registry.Bind` 與 `commitClaudeResume → SetResume` 同一個 S，因此「最終值等於 S」證明不了第一輪已寫入。三階段各自保存 raw JSON：

| 階段 | audit 行數 | 含 approval#1 | 含 approval#2 | `sessions.json` 的 `created_at` | `resume_session_id` |
|---|---|---|---|---|---|
| `round1/`（**第二輪送出前**） | 3 | 2 | **0** | `2026-09-21T23:25:38Z` | S |
| `round2/`（第二輪完成後） | 5 | 2 | 2 | `2026-09-21T23:25:42Z` | S |
| `final/`（afterEach，盡力而為） | 5 | 2 | 2 | 同 round2 | S |

→ round1 快照內**沒有**第二輪的 approval，而綁定已存在，這才是「第一輪的綁定先寫入」的證據。

**退出觀測**：`exit-observations.jsonl` 4 筆，皆 `state=absent`、`status=1`、`error=null`——round1 的 cli／mcp 在 23:25:40.79／40.80（**第二輪 23:25:42 啟動之前**），round2 的在 23:25:44.25／44.26。**`done.json` 只是協定處理完成標記**（CLI 在送 result／退出之前寫），不是 OS 層退出證據；`probeChildOs` 的 `error` 一律不當成 `absent`。

**End 操作結果**：按下 `end-session` 後 `.timeline .row.note` 由 0 增為 1、`結束對話失敗` 維持 0，pane 仍 `data-test-active="false"`；截圖 `ui-claude-after-end-session.png` 可見新增的 `note` 列。`internal/appcore/pump.go:80-84` 的 `EndSessionFlow` 對 `ErrNoSession` 冪等回 `nil`，與觀測一致。

**DOM**：`dom-observations.json` 兩輪各存 wsid／approval id／raw params／dialog 全文，且在每輪取得後**立即**落檔（不等第二輪成功）。

## 5. 被擋下的缺陷與修正（**全部保留，不因最終通過而改寫**）

### 5.1 reviewer #391 事前指定的五項修正
提案（#390）原稿未過，reviewer 逐項要求：輪次不得由 argv 推導、每輪證據獨立且禁止覆寫、流程改為「自然結束→確認退出→真 End→重送」且不得宣稱 End 終止活程序、完成內容須核對 pane/assistant 氣泡、補齊 setup／teardown／env／executionMode／routing 接線與對應 selftest。全部完成後才進入階段 A 複核。

### 5.2 reviewer #393 的四個 P1（**已由我自己的獨立探針逐項重現後才修**）

| 編號 | 缺陷 | 我的獨立重現 | 修法 |
|---|---|---|---|
| P1-1 | 跨輪 judge 是**另寫的、比 CLI 既有判定弱的第二份** | 8 種壞證據全部 `violations=[]`：`psDuring=null`、`psDuring.state=error`、`exitCode=17`、`stdoutDrained=false`、`roundRecord.pid` 不符、init 原始 line 的 `session_id=WRONG` 而便利欄位不變（reviewer 列的六種），**外加我自己找到的 `exitSignal='SIGKILL'`、`unreaped=true`** | 把 `judgeRoundTripSuccess` 的子程序判定原封不動抽成 `judgeChildObservation()` 兩邊共用（抽取後 CLI selftest 89/89 不變，證明行為保存）；對已保存的 JSON 先做嚴格形狀驗證；init 改為解析原始 line 並與便利欄位交叉核對；加 `round.json.pid === mcp-child.selfPid` 同源核對。修後同一探針 **14 種注入全部被擋、正控制乾淨** |
| P1-2 | **失敗路徑會把既有證據改成「全過」** | 受控 CLI 探針：round-2 目錄 EEXIST 時 rc 雖為 19，但根目錄 `judgement.json` 由 `{"problems":["PREEXISTING-FAILURE"]}` 被改寫成 **`{"problems":[]}`** | `outDir` 改成「排他取得後才有值」；未取得時診斷一律寫進 `mkdtemp` 排他新建的 `unowned-*/`；`safeWrite` 對空目的地記問題並 return，**絕不回退寫既有目錄**。修後三個檔案 hash 全部不變、診斷另存 |
| P1-3 | **End 的斷言等於沒等待** | 讀碼確認：先 `click`，再比對一個**點擊前就已經是 `false`** 的屬性 | 改為等待「這次點擊自己」的操作結果（note 增加 或 出現失敗訊息），失敗即判定失敗 |
| P1-4 | 取證時點錯 | 讀碼確認第二輪 init 會重新綁定同一個 S；`global-teardown.ts:364-366` 會刪整個 fixture（含 `.workbench`） | 三階段保存＋逐階段核對；`done.json` 語意更正；退出改以 `probeChildOs` 觀測；afterEach 盡力保存且不妨礙既有 bounded teardown |

### 5.3 reviewer 在 #395 直接補的取證缺口
reviewer 直接修改我工作樹內的 `claudeSessionRecovery.spec.ts`（最終 SHA `6a9c961f…`）：每次退出觀測**先寫** `exit-observations.jsonl`（含時間／round／role／pid／完整三態）再斷言、每輪 DOM 取得後立即落檔、probe 前驗 pid 為正整數。**未改 production 與判定目標。**

### 5.4 我自己回報的紀錄錯誤
- #392 把 `744 insert / 76 delete` 標成「18 檔合計」是錯的——那只是 15 個 tracked 檔的 `git diff`，不含 3 個新檔的全檔行數。本檔 §2 已分開列。
- #390 內文的「約 9 分鐘」與 15:46Z 結束時間無法由原始紀錄核實（server `created_at` 為 15:38:51Z），**已改標為未量測**，不得當工時。
- 本系列的 active work **一律未量測**；只有牆鐘可由 mailroom 時戳核實。

### 5.5 施工期間同步過的既有測試期望（行為判定未放寬）
每一批都先獨立確認「行為仍然被擋」才同步訊息字串：
- `f2Routing.selftest.ts` 三條 tripwire 訊息字串（先列印 violations 陣列確認三種情況仍各自非空）。
- `f2RunEnv.selftest.ts` 因新增必填欄位 `claudeRoundDir` 的 fixture 與欄位清單更新。
- `claudeRecoveryJudge.selftest.ts` 一條斷言字串（`init.json 缺少 sessionId` → `init.json 應為物件`），另兩條因 fixture 改為完整形狀而調整比對字串。
- 另在 `judgeClaudeConversationArgvStrict` 補了 resume 有無的明確訊息（診斷可讀性，不改判定強弱）。

## 6. 驗證分工與**時間／對象界線**

- **worker（claude-worker）**：實作、四個 P1 的獨立重現與修正、離線 selftest／typecheck、隔離 source-only 整合、唯一一次 browser run 的執行與證據整理。
- **reviewer（codex-reviewer）**：#391 事前設計裁定、#393 以獨立離線探針、受控 CLI 探針及讀碼確認四個 P1、#395 逐檔核對 manifest 並親跑 `selftest:claude-recovery-judge`（59/59）與 `typecheck:e2e`、#397 以**實際 raw artifacts** 重跑 `judgeClaudeRecovery` 與 scenario tripwire（皆 `violations=[]`）、逐輪核對 registry 的 S／cwd／WSID、查看截圖、核對四筆退出觀測與先退出再啟動的時間順序、另做 live `ps`／`lsof`（所列 PID 均不存在、34115／5173 無 listener）。**reviewer 未重跑 browser。**

**必須分清的三個時間點與對象**（避免把不同階段的數字混為一談）：

| 證據 | 對象 | 時間點 |
|---|---|---|
| 隔離 source-only `selftest:all` **rc=0、22 套 543 項檢查 0 失敗**；`typecheck:e2e` rc=0（`/tmp/e1src-rev2`，git baseline `e048cb58…`） | 四個 P1 修正後的 18 檔 | **reviewer #395 的 spec 取證補丁之前** |
| `typecheck:e2e` rc=0、`selftest:claude-recovery-judge` **59/59**（reviewer 親跑） | 含補丁的最終 spec | 補丁之後 |
| browser run `20260921T232407Z-25d858` **PASSED** | 含補丁的最終 18 檔 | 補丁之後 |

→ **543 項全套結果不涵蓋最終 spec**；最終 spec 只有上表第二、三列的證據。另有一份較早的隔離副本 `/tmp/e1src`（514 項，四個 P1 修正**之前**），兩份都保留、互不覆寫。

## 7. 保留的限制與未驗項

以下列出本次證據未涵蓋的能力；是否為 Task F 或 aggregate 的必要驗收條件，仍須依原驗收契約逐項核對，不因列入本節而新增需求。

1. **deny 決策、逾時、三輪以上、多 session、cold restart／跨 App 重啟、真實 Claude provider、正式 browser CI**：完全未做、未驗。
2. **單一樣本**：本次只跑一次，**不宣稱時序穩定性或可重複性**。
3. **第二輪 screenshot 的狀態**：`ui-claude-r2-completion.png` 四個氣泡（兩輪 prompt 與內容）都已顯示，但畫面右上狀態仍為**「回覆中」**——該圖證明內容已顯示，**不得當作最終 UI 狀態已完成的證據**。
4. **End 的證明強度**：只證明「程序自然退出之後仍可完成 End 操作」，**不證明 End 能終止存活中的 CLI**。
5. **backend 空值 resume fallback** 未涵蓋（依 reviewer 裁定，措辭限於「本案正常 UI 流程不走 fallback」，不泛稱所有 UI 情境不可達）；durable identity 另有 `commitClaudeResume` 的 late-init 寫入路徑（`app.go:7429`／`9076`），不只 AcceptSubmit 後的 `commitSessionIdentity`。
6. **錯 WSID／cwd 的 production 拒絕路徑**未新增 browser scenario；`app_per_wsid_writer_test.go` 既有的 Go 層覆蓋（`TestClaudeResumeRefusedForForeignWSID`／`TestClaudeResumeWSIDGuardSurvivesRestart`）**本系列只讀碼、未重跑**，不列為本輪驗證。
7. **App binary hash 未於複核時重新計算**：複核時該 binary 已不存在，刪除原因未獨立驗證；身分結論引用當次留存的 identity／CLIInfo／config 與原始 run 證據。
8. **`probeChildOs` 以 pid 觀測**，pid 重用的理論可能性仍在，只當「本次持有期間」的佐證。
9. **CLI selftest 的 MCP 子程序是本檔控制的 synthetic mock**，不是真 workbench binary；該層只證 CLI 端行為。
10. **F1b 歷史未驗項全部保留**：假 CLI 遭 SIGKILL 的清理、`closeAndReapChild` default grace（10s/10s/5s）實際等待時長、`FINALIZE_DEADLINE` 逾時分支——有碼無測試。`appBinaryIdentity` 仍只支援 darwin。
11. **`fakeAppServer.selftest.ts` 的 `waitForChildExit` 間歇逾時仍未解**（本輪未重現，36 passed／0 failed，不宣稱已修好）。
12. **`/tmp` 與 worktree 路徑非跨機器可取得證據**。

## 8. Manifest 與證據位置（本機路徑，非跨機器可取得）

| 項目 | 位置／值 |
|---|---|
| 最終 18 檔 source manifest（reviewer 建立） | `/var/folders/.../T/review394-6fch3gyd/SOURCE-SHA256SUMS`，SHA256 `3da2277f15b28cf89a4197ba401ea2be48ac1ef7206d4af2fe319a5224e14c9c` |
| 最終 spec SHA256 | `6a9c961f0ba0f4fb9293375362a2b7d361db01094e834bf00ded11d86e846341` |
| run 證據 manifest（worker，42 檔） | `/tmp/e1runB/RUN-EVIDENCE-SHA256SUMS`，SHA256 `09688af428b0aa42b971b7e5ab9e8770aa583fa11d75758ece5e5fcda686820a` |
| 完整 runtime manifest（reviewer，68 檔） | `/var/folders/.../T/review396-dz_d3nm6/FULL-RUNTIME-SHA256SUMS`，SHA256 `daed6d3e15fcd4c0a5b9e17ac1bf029171d35fcd6631d3807a9d25e0d0d2b004` |
| browser run artifacts | `frontend/e2e/.artifacts/20260921T232407Z-25d858/`（`E2E_KEEP_ARTIFACTS=1` 保留） |
| run 原始 stdout | `/tmp/e1runB/run.log`（rc=0） |
| 隔離 source-only 副本 | `/tmp/e1src`（514 項，P1 修正前）、`/tmp/e1src-rev2`（543 項，P1 修正後、spec 補丁前） |
| reviewer 探針與 log | `/var/folders/.../T/review392-l0cu6rvt/`、`review394-6fch3gyd/`、`review396-dz_d3nm6/` |

run 前後各做一次 `shasum -a 256 -c SOURCE-SHA256SUMS`，**皆 18/18 OK**——本次 run 未改動任何交付檔。`git diff --check` rc=0。`package.json.md5` = `02193ddd1e7c1b4d408a9035a90fe5b0`（恰 32 bytes、無換行）。

## 9. 交付狀態

- **截至 mailroom #398 文件收尾審查時，尚未 commit／push／PR**。18 檔 source 已驗收凍結，本輪僅做文件收尾。
- **未改動 backlog 的任何點數**，未核定新總量 pt。
- 主工作區 README 未動；全部歷史樣本與新舊 source-only 副本／manifest 保留。
