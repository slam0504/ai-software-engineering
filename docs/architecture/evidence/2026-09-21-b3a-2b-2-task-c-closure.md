# B3a-2b-2 Task C 結案紀錄

> 日期：2026-09-21（台灣時間）。以下時刻一律為原始 UTC 值，除非另外註明。
> 結論：**Task C——Codex `commandExecution-allow` 單一 browser 整合檢查點——技術驗收通過，帶明列限制**（codex-reviewer 裁定）。
> **這不等於**整張 B3a-2b aggregate、四案 matrix、browser resume、Claude fake、cold restart 或正式 CI 驗收完成——本文件措辭刻意避免讓人如此誤讀，未涵蓋項目見第 7 節。
> 本輪只做結案文件與 backlog 更新、本機 commit，**未改動任何已驗收的 source**。

## 1. 範圍與受測對象

| 項目 | 值 |
|---|---|
| Worktree | `/Users/eason_tseng/playground/project/ai-se-worktrees/b3a-2b-taskC` |
| 分支 | `b3a-2b-2/task-c-browser-checkpoint` |
| base | main `2eccac93e284cc4c4f73a2e38f94ebd3da751924` |
| 涵蓋流程 | **單一** scenario：`commandExecution-allow`（Codex approval 允許路徑） |
| 受測 source 範圍 | 19 檔（tracked 已修改 9 檔＋untracked 新檔 10 檔，見第 8 節清單） |

本票只驗一個流程、一個 scenario；**不是** B3a-2b 驗收條件裡「session recovery、approval 兩條流程」的完整覆蓋，approval 流程本身也只驗了 `commandExecution-allow`（允許）這一案，**不含**拒絕案、其他 tool 案、resume 案。

## 2. Task B（App 探針）與 Task C（browser）的證據層級差異

**兩者證據層級不同，不可混為一談：**

- **Task B**（bounded App 探索，已完成）：走 `codexHostOverride` 測試注入 seam（`app.go:490`「測試注入：fake wire 走 production StartSession 分支」），**繞過** `ensureAppServer()`／`replaceCodexGeneration()` 真正 spawn Codex CLI 的路徑。這是 App／adapter 層的探針，不含真實 Wails App 啟動，也不含瀏覽器 DOM 操作。
- **Task C**（本票）：`frontend/e2e/scenarios/commandExecution-allow.spec.ts` 檔頭明訂「不沿用 `codexHostOverride`——App 走 `a.ensureAppServer()` 真正 spawn `a.codexCLIPath()`」；`global-setup.scenario.ts` 啟動真的 `wails dev`，Playwright 操作真瀏覽器 DOM（`page.locator(...).click()`），approval 核可走真的 `window.go.main.App.ResolveApproval` binding，**不在 browser 注入 fake bindings**、**不直接呼叫 `ResolveApproval` 取代按鈕點擊**。

Task C 的證據強度高於 Task B（真啟動＋真瀏覽器操作 vs. App 層 seam 繞道），但兩者驗的是不同東西——Task B 完成**不能**當作 Task C 範圍已被涵蓋，反之亦然。

## 3. 三輪補正各修了什麼

以下依 repo 內程式碼註解與三份 evidence bundle 的 `COMMANDS-AND-ENV.txt`／`CORRECTIONS.md` 逐一核對，**只寫本 session 能在 repo 或 bundle 內實際找到出處的內容**。

### 3.1 round2（`.taskc-evidence-b3a2b2/`，56 檔，2026-09-21 14:30–14:31 產出）

修正 reviewer 提出的驗收缺口。本 session 於 19 檔 source 內 grep `B3a-2b-2 Task C 驗收缺口修正` 逐一核對，**找到 5 項有明確編號與程式碼位置對應**：

| 缺口 | 內容 | 對應位置 |
|---|---|---|
| 缺口 1 | WSID 三段串接核對：DOM（`data-test-wsid`）↔ App 端 `audit.jsonl`／`workspace-sessions.json` ↔ 原始 wire（`scenario-wire.log`）；approval DOM 額外核對 method 與 thread/turn/item；新內容泡泡限定在同一個 WSID 的 pane 內尋找 | `frontend/src/components/PaneView.vue:77`、`ApprovalDialog.vue:78`、`commandExecution-allow.spec.ts:20,99,127,142` |
| 缺口 2 | `CLIInfo` `info.workspace`／`info.toolsDir` 與 `env.workspaceDir`／`env.toolsDir` 的 canonical path（realpath）核對——不只信 `toolsSource`/`workspaceSource === 'env'` | `commandExecution-allow.spec.ts:25,63` |
| 缺口 3 | 完整 protocol 判定（新增 `scenarioProtocolJudge.ts`）＋manifest／scenario-config／run identity 交叉核對；移除 `String(id)` 寬鬆比對；manifest 輪詢只容忍「檔案尚未出現」，其餘錯誤直接失敗 | `scenarioProtocolJudge.ts:1`、`commandExecution-allow.spec.ts:27,248,290` |
| 缺口 4 | 執行模式判定改用明確的 `determineExecutionMode`（不是 `env.scenario` truthy）決定走哪一套 tripwire 判定；`scenario-broken`（identity 遺失／型別錯誤／來源不一致）一律判失敗，不回退 default | `global-teardown.ts:264`、`global-setup.ts:51` |
| 缺口 5 | `playwright.scenario.config.ts` 相關的 offline sandbox 邊界修正 | `global-setup.scenario.ts:89` |

**缺口 6 沒有程式碼對應是正確結果，不是遺漏**：reviewer 該輪裁定的第 6 項是**證據與失敗分類**——要求提供凍結 source snapshot、實際命令／env／stdout／stderr、各 run 對照表與 `SHA256SUMS`，並明確指出「tracked diff 不含新增檔全文，不可當總改動量」。這一項本質是證據管理要求，**不產生 source 變更**，因此在 19 檔 source 內 grep 不到編號標記。

其交付物即 round2 bundle 本身：`COMMANDS-AND-ENV.txt`（靜態驗證 typecheck／`selftest:all`／build／元件 sanity，缺口 4／5 的獨立負控制——offline-sandbox 拒絕、mode-detection 7/7，以及三個 real browser run：scenario／default／controls 皆 rc=0）、`RUN-COMPARISON-TABLE.txt`、`source-snapshot/` 與 56 行 `SHA256SUMS`。

### 3.2 round3（`.taskc-evidence-b3a2b2-round3/`，44 檔，「第三輪限縮補正」）

修正三項缺陷＋新增 wire 副本證據：

- **缺陷重現＋修後單元驗證**：`executionMode.selftest.ts`（8 passed，含 4 個 reviewer 反例 a–d＋2 正控制＋2 負控制）、`scenarioProtocolJudge.selftest.ts`（18 passed）。
- **缺陷 3 的 controller 反例重跑**：用真實 `.artifacts/20260921T063619Z-a5d98e/scenario-wire.log` 竄改重現——baseline 0 violations；seq2 混入 method → 1 violation；seq2 刪掉 result → 1 violation。
- **wire 副本**：真跑（scenario run `20260921T070909Z-c5cb73`）產出新的 `app-wire-log.jsonl`／`app-wire-log.meta.json`，複本存於 `run-artifacts/scenario-run-20260921T070909Z-c5cb73/`，與既有 `scenario-wire.log` 並列，作為 App 端寫回 wire 的獨立證據。
- 三次真跑（scenario／default／controls）皆 rc=0，執行模式判定分別為 `scenario`／`default`／`default`，符合缺口 4 的入口標記設計。
- 過程中發現一次 `selftest:all` 內 `selftest:scenario-fake-app-server` 的逾時（見第 5 節，**不在本節重複下結論**）。

### 3.3 addendum（`.taskc-evidence-b3a2b2-addendum/`，20 項，阻擋缺陷最小修正＋逾時調查）

四點對既有紀錄的補正／撤回，詳見 `CORRECTIONS.md`：

1. 早期 offline 負控制的原始 artifacts 已不存在（見第 6 節）。
2. round3「重跑 PASS ⇒ 確認是既有計時 flake、與本輪改動無關」的推論**撤回**（見第 5 節，措辭以本節為準）。
3. **阻擋缺陷本體（controller 先前兩次回報錯誤，已撤回）**：`global-teardown.ts` 修正前，`determineExecutionMode(...)` 呼叫（原 `:120`）**早於** `runtime.processTree.stop()`（原 `:134`）——一旦 `determineExecutionMode` 對壞掉的 identity（例如落地內容為 JSON `null`）拋出未捕捉例外，`globalTeardown()` 會在停止程序樹之前就整個中止，**已擁有的程序反而不會被停**。本輪修正：將 `determineExecutionMode` 呼叫移到「停止取樣／停止 wails dev 程序樹」與 pointer 清理都做完之後、緊接在其唯一使用點（tripwire 判定）之前（現況見 `global-teardown.ts:255` 起），同步修正相鄰註解、對 `determineExecutionMode` 內部兩處 `JSON.parse` 結果補上 runtime object validation（拒絕 `null`／array／primitive）。
4. `source-snapshot/` 收錄**本次任務結束當下**19 檔的完整內容（不是 diff），取代 round2／round3 舊快照（僅 16 檔、不含 untracked 新檔）作為「目前原始碼」的權威來源；round2／round3 的 manifest 與 `SHA256SUMS` 維持原樣不動、不追溯覆寫。

## 4. 誰驗了什麼（分開陳述，不得把他人執行結果寫成自己的）

- **reviewer（codex-reviewer）**：獨立裁定「技術驗收通過，帶明列限制」，並**自行執行**了模式判定 selftest 12/12、收尾 selftest 15/15（含真 `globalTeardown` ＋ stop stub 的 null marker regression）與 `tsc --noEmit -p tsconfig.e2e.json`，**三條命令 rc 皆為 0**。原始紀錄位於本機路徑 `/tmp/b3a2b-review236-o57hpcjy/`（`results.json` 與 `0.stdout`／`0.stderr`／`1.*`／`2.*`），`results.json` 的 sha256 ＝ `0e7c3b1f92a5ff2237462cb8aba247e5f7a07d14efbd190b17447283629268f5`。
  - **誰讀過**：文件編寫者於 reviewer 指出路徑後**直接讀取並核對過** `results.json`（sha256 與 reviewer 所述相符、三條命令 rc 皆 0）。但**執行者是 reviewer，不是文件編寫者**——此處只是確認紀錄存在與內容一致，**不等於文件編寫者獨立重跑過那三條命令**。
  - **先前錯誤（撤回）**：本文件初版寫「本 session 搜尋未找到 review236 的本地副本」，並據此推論其原始輸出不在本機。**該推論不成立**——搜尋不到只代表當時沒找到，不能推論檔案不存在；實際上該路徑存在且可讀。
  - 該路徑是**本機路徑，非永久、非跨機器可取得的證據**，僅供同一台機器上核對。
- **controller**（彙整證據、撰寫 `COMMANDS-AND-ENV.txt`／`CORRECTIONS.md`、對 reviewer 回報者）：彙整三份 evidence bundle、產生 `SHA256SUMS`、執行第 3 節所列各輪 log 對應的指令並自行記錄 rc／輸出路徑；addendum 中**撤回自己**兩項先前對 reviewer 的錯誤回報（第 3.3 節第 2、3 點）。
- **施工方**（撰寫 19 檔 source 改動者）：撰寫本票範圍內的程式碼修正（含 `scenarioProtocolJudge.ts`、`executionMode.ts`、`global-teardown.ts` 的呼叫順序修正等），round2／round3 的 `COMMANDS-AND-ENV.txt` 內各輪指令由撰寫者本人於本機執行並記錄（例如 round2 log 09 明確標註「不在票面要求清單內，是我自己加的 sanity check」）。
- 本 session（Task C 結案文件＋backlog＋commit 執行者）**未重跑**任何 round2／round3／addendum 已記錄的功能測試，只逐檔核對三份 `SHA256SUMS` 本身（`shasum -a 256 -c`，三份皆全數 `OK`，56／44／20 檔數與 manifest 宣稱相符）與程式碼內「缺口 N」註解的實際位置。

## 5. 未解逾時（已知測試可靠性問題，未解）

round3 執行 `selftest:all` 時，`selftest:scenario-fake-app-server` 出現一次逾時：「bad-config-method：threadMode 缺漏或型別錯誤時 exit 17（R4）」案，錯誤為 `waitForChildExit` **3000ms 逾時**。

- 本輪**完全未修改** `fakeAppServer.ts`／`fakeAppServer.selftest.ts`（五支凍結檔之二，不在本票 19 檔變動列表內）。
- round3 原文曾寫「⇒ 確認 05 的失敗是間歇性計時 flake，非本輪改動造成的紅燈」——**此推論在 addendum 中已撤回**，不成立。
- **本文件採用 addendum 校正後的措辭**：已觀察到一次約 3 秒的 `waitForChildExit` 逾時，其後重跑（隔離重跑 #1／#2，各 24 passed）通過。**根因、以及是否與本輪改動有因果關係，皆未確認**——「本輪未改凍結檔」只排除「本輪程式改動直接造成」這一種可能成因，**不排除**環境負載、並行行程數、macOS fork/exec 延遲、或與同輪 `selftest:all` 內其他測試的組合效應。
- addendum 輪用**有上限的 3 次固定診斷 probe**（旁路重現，不碰凍結檔）重跑同一段 spawn 邏輯，3 次全部在 150–190ms 內乾淨結束、無逾時，**未能重現**，因此**無法確認根因**。
- **最終結論：未解間歇失敗**。**不得**寫成「根因已解」或「沒有 flaky test」，**不得**把某次 FAIL 因為後來 PASS 就改列成 PASS——原始的一次 FAIL 與後續的 PASS 是兩筆各自獨立的觀測記錄，兩者並存。
- **暫存目錄措辭校正（reviewer 指定）**：不得寫「暫存目錄被 OS 回收」，因為沒有 OS 回收的直接證據。正確措辭：**原失敗對應的目錄／程序資料目前無法唯一定位或已不在，無法確認消失原因。**

## 6. early offline 負控制的 raw artifacts 不可再驗

`.taskc-evidence-b3a2b2/RUN-COMPARISON-TABLE.txt` 記錄的「早期 offline 負控制」原始 artifacts 目錄，目前已不存在——只剩轉錄後的 log 文字（`04-negctrl-offline-sandbox.log`），**原始 artifacts 不可再重新雜湊、不可再核對轉錄是否忠實**。舊表格「純負控制、無須保留」一句**不是目前接受的規則**：保留與否是證據管理政策問題，addendum 只如實記錄現狀（原始檔案已不在），不代表追認當初刪除的正當性，也不代表現在補得回來。

## 7. 未涵蓋項目

本票**不涵蓋**：

- 四案 matrix（`commandExecution-allow` 以外的其他 scenario／tool／deny 案）
- browser resume（session recovery 流程的瀏覽器整合）
- Claude fake（Claude provider 的等效 browser 檢查點）
- cold restart
- 正式 CI 整合（B3a-CI 另票，僅完成單次可行性驗收）
- B3a-2a（Gate 1／Gate 2／STALE，不經 provider）
- A5
- B3a-2b aggregate 整體驗收（session recovery ＋ approval 兩條流程的完整覆蓋）

## 8. 已驗收的 19 檔 source

```
 M frontend/e2e/global-setup.ts
 M frontend/e2e/global-teardown.ts
 M frontend/e2e/playwright.config.ts
 M frontend/e2e/support/env.ts
 M frontend/e2e/support/globalTeardownStop.selftest.ts
 M frontend/package.json
 M frontend/package.json.md5
 M frontend/src/components/ApprovalDialog.vue
 M frontend/src/components/PaneView.vue
?? frontend/e2e/global-setup.scenario.ts
?? frontend/e2e/playwright.scenario.config.ts
?? frontend/e2e/scenarios/commandExecution-allow.spec.ts
?? frontend/e2e/support/executionMode.selftest.ts
?? frontend/e2e/support/executionMode.ts
?? frontend/e2e/support/scenario/scenarioCli.ts
?? frontend/e2e/support/scenario/scenarioProtocolJudge.selftest.ts
?? frontend/e2e/support/scenario/scenarioProtocolJudge.ts
?? frontend/e2e/support/scenario/scenarioTripwire.ts
?? frontend/e2e/support/scenario/scenarios.ts
```

## 9. 三份 evidence bundle：路徑、檔數與 digest

**以下本機路徑不是跨機器可取得的證據**——三份 bundle 都留在 worktree 內、**不進 git**，僅記錄路徑與 digest 供日後在同一台機器或以其他方式取得原始檔案時核對；不得把這些路徑當成永久下載連結。

| bundle | 路徑（worktree 相對） | 檔數（`SHA256SUMS` 行數） | 自驗結果 | `SHA256SUMS` 本身的 sha256 指紋 |
|---|---|---|---|---|
| round2 | `.taskc-evidence-b3a2b2/` | 56 | `shasum -a 256 -c SHA256SUMS` 全數 `OK`（本次結案文件撰寫前實際執行過） | `a8d2eec08d0aeae6b6f3887050b8c02c3984d9ab42e9a2304751809b51e71165` |
| round3 | `.taskc-evidence-b3a2b2-round3/` | 44 | 同上，全數 `OK` | `bc80961af33c4169aeeee2b6f60c3ef2bd2888dba42c7bb379943c8c34364a7a` |
| addendum | `.taskc-evidence-b3a2b2-addendum/` | 20 | 同上，全數 `OK` | `8ce47bce7c112f4a13480dc9d180b6b1b1a9d899a288ba5428b04cf7508318a9` |

三份 bundle 皆 additive、互不覆寫。**review236**（reviewer 本輪自行執行三條命令、rc 皆 0 的紀錄）位於本機 `/tmp/b3a2b-review236-o57hpcjy/`，`results.json` sha256 ＝ `0e7c3b1f92a5ff2237462cb8aba247e5f7a07d14efbd190b17447283629268f5`；**執行者為 reviewer**，文件編寫者僅讀取核對、未重跑，詳見第 4 節。**此路徑同樣是本機路徑，非永久／跨機器可取得證據。**

**原始大 bundle 不放入 git**——本次 commit（見交付回報）只納入已驗收的 19 檔 source 與本文件、backlog 更新，三份 bundle 與其內容持續留在 worktree 的 untracked 狀態。
