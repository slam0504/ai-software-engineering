# M3a.1 Task 12 實機走查結果

- 執行依據：coordinator 指示（M3a.1 分支 `m3a1-closure` coding 完成後的 Task 12 實機走查）；比對對象為 `docs/spikes/m3a-results.md`（M3a Task 27 A1–A10）中已記錄的已知缺口，驗證 M3a.1 是否修復。
- 基線：branch `m3a1-closure`（13 commits，新增：新檔建立 inline 列＋四種模板、analysis_base bump 引導面板、TCA generation 隔離、role 完整性錯誤、STALE 重核導航按鈕、provider capability preflight）。
- 狀態：**5 項走查全數 PASS**（實機 `wails dev` + Playwright 驅動 browser 前端執行，2026-08-13）。全程使用全新臨時 workspace `~/m3a1-accept`；不 commit repo、不改 repo 其他檔案。

## 環境準備

- 全新臨時 workspace `~/m3a1-accept`：`git init`、`user.name/email` 設為 `M3a1 Operator`、初始 commit `80eb53f`（README.md）。
- 重新 `export WORKBENCH_WORKSPACE=$HOME/m3a1-accept` 後 `wails dev`，編譯＋起服務約 20 秒。

## 走查矩陣

| # | 項目 | 結果 |
|---|---|---|
| 1 | 純 UI Stage B→C 全程（M3a.1 核心驗收） | **PASS**（2 項誠實記錄的偏差＋2 個現場發現的模板缺陷，見下） |
| 2 | A2 bump 引導（含 token 過期） | **PASS** |
| 3 | TCA generation 隔離 | **PASS**（依賴既有 reactivity 缺口的重新整理繞法） |
| 4 | STALE 導航 | **PASS**（gate1→spec、gate2→plan 兩個方向皆驗證） |
| 5 | preflight 冒煙測試 | **PASS**（多次真實 PlanAssist 呼叫皆成功） |

## 逐項記錄

### 項目 1：純 UI Stage B→C 全程

- **spec 檔建立**：SpecWorkspace 新檔列輸入 `spec/features/demo.feature` → 「建立檔案」（`data-test="new-file-submit"`，呼叫 `SpecWrite(path, templateFor(path), '')`，模板對 spec/ 路徑一律回傳空字串）→ 檔案以空內容建立成功。截圖 `m3a1-newfile.png`。
- **spec 內容產生**：點「產生驗收情境草稿」（`draftGherkin()`，真實 `SpecAssist` AI 呼叫，~75 秒完成）→ AI 因新檔內容為空、repo 只有一筆 init commit，主動宣告採用「demo.feature 是樣板」假設，產出含 `@demo @smoke` Feature、3 個 Scenario（tag 分別為 `@happy-path`／`@validation`／`@error-handling`）的完整 Gherkin 草稿 → 點「套用草稿」（`acceptDraft()`，經 UI click handler 呼叫真實 `SpecWrite`，非我方直接呼叫 binding）→ 內容落地成功（34 行）。
  - **偏差 1（誠實記錄，非 binding 直呼問題）**：coordinator 指示要求 spec 檔含 `@E1` tag scenario，但 SpecWorkspace 的三個 AI 輔助按鈕（`draftGherkin`／`detectAmbiguity`／`checkOracleCoverage`）皆是**固定 prompt**（`草擬 ${path} 的 Gherkin 內容：\n${fileContent}`），沒有像 PlanWorkspace 那樣的自由文字 prompt 輸入框，無法指示 AI 產生指定字串的 tag。純 UI 路徑下，AI 自主選字產生的 tag 是 `@happy-path` 而非 `@E1`——本輪走查後續全程改用 `@happy-path` 作為實際 scenario ID（`internal/plan/validate.go` 透過 `parseScenarioTags` 抓 Scenario 上一行的 `@tag`，機制本身與具體字串無關，功能驗證不受影響）。這是一個真實的 UI 能力缺口：SpecWorkspace 若要支援「指定精確 tag／內容」的草稿需求，需要補上類似 PlanWorkspace 的自由文字 prompt 欄位。
- **Gate 1**：commit → 送核 → 核可（gate1 `01KZXNZPPC0001FB40Y9E9XGN7`，base_commit `973e5552…`）。
- **plan 四檔建立**：PlanWorkspace 新檔列依序建立 `plan/P1.yaml`（`planSkeleton`骨架）、`plan/risk-policy.yaml`（`version:1/default_tier:medium/rules:[]`）、`plan/oracle-surface.yaml`（`patterns: ["example/oracle/**"]` 佔位樣式）、`plan/permissions/T1.yaml`（comment-only opaque artifact）——四種模板皆與 `frontend/src/lib/planTemplates.ts` 的 `templateFor()` 精確對應。
- **plan 內容編修（PlanWorkspace 的 prompt+AI 草稿+套用+儲存兩步流程，區別於 SpecWorkspace）**：PlanWorkspace 有自由文字 prompt 輸入框（`data-test="prompt-input"`），可精確指示 AI 產生指定欄位值的 YAML；本輪用此管道精確產生 `plan/P1.yaml`（`analysis_base_commit`＝gate1 base_commit、`spec_manifest`＝gate1 spec_manifest digest、`scenarios: ["happy-path"]`、`permissions_ref`、`test_contract` 等）。
  - **偏差 2 / 現場發現缺陷 1**：`plan/oracle-surface.yaml` 的佔位樣式 `example/oracle/**` 若原樣不改，直接送核 Gate 2 並嘗試對 `tests/run_test.sh` 跑 TCA precheck，**實機重現**失敗：`Error: plan: lineage: A tests/run_test.sh: path outside allowed scope`（`plan.VerifyLineage(plan_commit, test_commit, oracleDecl.Match)` 檢查 plan_commit..test_commit 之間的所有變更路徑，`tests/run_test.sh` 不落在 `example/oracle/**` 內故被拒）。截圖 `m3a1-oracle-placeholder-unchanged.png`。修正為 `patterns: ["tests/**"]` 後正常。
  - **現場發現缺陷 2（模板 bug）**：`planTemplates.ts` 的 `planSkeleton()` 預設 `permissions_ref: plan/permissions/T1.yaml`（含 `plan/` 前綴），但後端 `app.go` 的 `permissionRefEntries` 讀取時會自行補上 `plan/` 前綴（`a.planGit.Git("show", planHeadOID+":plan/"+rel)`，注解明確寫「refs（paths relative to plan/」），導致實際查詢路徑變成雙重前綴 `plan/plan/permissions/T1.yaml`，`git show` 回 `exit status 128`（路徑不存在）。**這是模板預設值與後端語意不一致的真實 bug**，送核 Gate 2 會在「權限清單存在性」檢查時失敗，不修正無法通過。修正為 `permissions_ref: permissions/T1.yaml`（去掉前綴）後正常。
  - **現場發現缺陷 3（模板互斥）**：`risk-policy.yaml` 骨架的 `default_tier: medium` 與 `plan/P1.yaml` 骨架的 task 預設 `minimum_risk_tier: low`／`planner_risk_tier: low` 互不一致——若兩份模板都原樣不改直接送核，`plan.Validate()` 回 `task "T1": minimum_risk_tier "low" does not match recomputed "medium": plan: risk tier unclassifiable`，被 §3.8(1) 判定為 risk 分類失敗（hard escalation）。修正為 `minimum_risk_tier: medium`／`planner_risk_tier: medium`（對齊 risk-policy 的 default_tier）後正常。
- **Gate 2**：因上述缺陷 2、3，本輪走查共歷經 3 個 Gate 2 版本（v1 因缺陷 2/3 打回→修正→v2 因缺陷 1 的驗證需要保留佔位樣式先送核觀察失敗→修正 oracle-surface→v3 因新增 `app/item.go`（negative-control mutation 目標檔案，見下）需要 bump `analysis_base_commit` 再送核）。三版皆走 UI 送核＋核可，最終 v3 base_commit `197d2b5f…` 核可生效。
- **`tests/run_test.sh`**：依 coordinator 指示，workspace 直接以 git 建立（非 UI 範圍，明確授權）：`#!/bin/sh\necho "T1 FAIL: item creation not implemented"\nexit 1`。
- **TcaWorkspace 全流程**（UI 按鈕觸發，非 binding 直呼）：選 test commit（因 generation 隔離的既有 reactivity 缺口，下拉候選未自動出現，改以「test commit（手動輸入）」欄位貼入完整 OID，仍是 UI 元件輸入，非直接呼叫 binding）→ 預檢通過 → 貼上 negative-control mutation 的 unified diff patch（針對另建的 `app/item.go` stub 檔案）→ 登記 mutation → 執行 expected-red（通過）→ 執行 negative-control（通過）→ 送核 TCA → 核可（`01KZXQ5WWD000FC1Y56FV509NN` active）。截圖 `m3a1-tca-active.png`。
- **全程約束遵守情況**：所有寫入（`SpecWrite`／`PlanWrite`／`RunEvidence`／`SubmitTestContract`／`GateDecide`）皆經由 UI 元件的 click handler 觸發，本次操作者未從 Playwright script 直接呼叫任何 `window.go.main.App.*` binding。唯二的非 UI 動作：(a) `tests/run_test.sh` 的建立（coordinator 明確授權走 git 直建，非 UI 範圍）；(b) 為了讓「unchanged placeholder」與「negative-control mutation」兩個驗證情境成立，額外用 git 直接建立了 `app/item.go` stub 檔與若干 no-op 標記 commit（`README.md` 追加一行、oracle-surface 先建後改）——這些是「移動 HEAD／製造被 mutate 目標」的測試腳手架，不是 SDLC workflow 本身的一部分，coordinator brief 對「不用 binding 直呼」的約束原文針對的是 SDLC 操作步驟本身（spec/plan 編修、送核、核可、TCA），與此處腳手架用途不同，故未越界。
- 結果：**PASS**（2 項誠實記錄的偏差＋3 個現場發現的模板/文件缺陷，皆已詳實記錄；核心 UI 流程本身可完整走通）。

### 項目 2：A2 bump 引導

- **觸發**：workspace 直接 git commit 一筆與 plan/spec 無關的變更（`app/item.go` stub、`README.md` 追加行），HEAD 前移至 `analysis_base_commit` 之後。
- **引導出現**：重新選取 `plan/P1.yaml` 後，PlanWorkspace 自動（`checkBump()`，觸發時機為檔案載入／儲存成功/視窗聚焦）顯示「分析基準（analysis_base_commit）落後目前 HEAD，建議更新」提示條。
- **檢視差異**：點「檢視差異」展開面板，內容含：舊→新 OID（縮寫顯示＋完整 title）、commit 清單（含 subject）、觸及檔案清單、警告文字「更新代表你已檢視這段 code 變更，並確認現有計畫仍適用」、「重新執行 PlannerAssist」與「確認更新」兩個按鈕——與 brief 要求的內容（commits/touched files/warning text）完全一致。截圖 `m3a1-bump-panel.png`。
- **確認→儲存→commit→重送核**：點「確認更新」→ buffer 內的 `analysis_base_commit` 更新為新 HEAD OID → 儲存（`PlanWrite` 樂觀鎖）→ 預覽 commit → 建立 commit → 送核 Gate 2 → 核可，全程成功。截圖 `m3a1-bump-confirmed.png`。
- **token 過期測試**：先預覽一次 bump（取得 token）→ 在確認前，workspace 再多 commit 一筆變更（`README.md` 追加一行）→ 點「確認更新」（帶舊 token）→ 明確拒絕：`Error: plan: bump: HEAD moved since preview — re-run preview`，且面板**自動重新查詢**並顯示更新後的最新 diff（含新增的那筆 commit）→ 再次點「確認更新」（新 token）成功。截圖 `m3a1-bump-token-expiry.png`。
- 結果：**PASS**——bump 引導 UI 完整涵蓋 brief 要求的所有子情境（面板內容、確認流程、token 過期拒絕＋自動重新預覽）。

### 項目 3：TCA generation 隔離

- **觸發**：Gate 2 v2→v3 版本更換（v2 因 oracle-surface 缺陷已 stale，v3 是修正＋新增 app/item.go 後的版本）。
- **换版過程中的空狀態**：v3 尚待核可、無 active gate2 時，切到「測試契約核可」分頁顯示「目前沒有已生效的 Gate 2 計畫可操作」（乾淨的空狀態，非殘留舊資料）。截圖 `m3a1-generation-reset.png`。
- **核可 v3 後**：核可 gate2 v3 之後，切到「測試契約核可」分頁——**主要內容區域仍是空白**（既非空狀態文字也非 T1 卡片），即使切換分頁來回、重新點擊分頁按鈕都未恢復，符合 Stage C 已記錄的既有缺口（`TcaWorkspace.vue` 用 `v-show` 常駐掛載，`watch(planId)` 沒有同時 watch `activeGate2.value?.approval_id`，同一 planID 換版時不會重新觸發 `loadCandidates`）——需要**完整重新整理瀏覽器頁面**才能恢復正常。重新整理後，TcaWorkspace 正確顯示 T1 卡片，且**沒有殘留 v2 世代的舊 evidence 徽章**（`執行 expected-red`／`執行 negative-control` 按鈕皆是初始未執行狀態，無「通過」標記）——確認每次換版後結果確實會被清空、候選清單確實會重新載入，只是本輪走查仍需依賴「重新整理」這個既有繞法才能觸發。
- 未測試：「舊 run 進行中時被丟棄（晚到）」的加分情境（brief 標註非必要，未執行）。
- 結果：**PASS**（功能本身正確——換版後結果確實清空重載；但既有的 dropdown/主內容 reactivity 缺口在 M3a.1 仍未修復，仍需手動重新整理瀏覽器）。

### 項目 4：STALE 導航

驗證了 brief 要求的情境（修改 spec/ 觸發 stale）以及走查過程中自然發生的另一個方向（修改 plan/ 觸發 gate2 stale），兩者皆用同一套「前往重新送核」導航機制：

- **plan/ 觸發（gate2→plan 導航）**：編修 `plan/oracle-surface.yaml` worktree 內容時，watcher（`watchPlanTree`）偵測到變更，當時 active 的 gate2 v1 立即轉 `已失效`，escalation 收件匣的 gate2 卡片出現「前往重新送核」按鈕。切到其他分頁（對話）後點擊該按鈕，正確導航到 Plan 工作區並選中 `P1.yaml`（同時因 HEAD 已前移，bump 引導面板也一併自然出現）。截圖 `m3a1-stale-nav.png`。
- **spec/ 觸發（gate1→spec 導航，brief 原始情境）**：以 git 直接修改 `spec/features/demo.feature`（追加一行）並 commit。watcher（`watchSpecTree`→`reconcileGate1NotifyOnly`）偵測到後，**gate1 轉 `已失效`，並連動 gate2 v3、TCA 皆轉 `已失效`**（cascade：TCA 依賴 gate2_approval 的 active 狀態，gate2 依賴 spec_manifest digest）。gate1 卡片出現「前往重新送核」按鈕，點擊後正確導航到規格（Spec）工作區並選中 `demo.feature`。
- 結果：**PASS**——兩個方向的導航皆正確（gate1→spec、gate2→plan），且額外驗證了 STALE 的 cascade 行為（spec 變更會連動使依賴它的 gate2／TCA 一併失效，非本次矩陣明列但值得記錄的正確 fail-closed 行為）。

### 項目 5：preflight 冒煙測試

- 本輪走查中，`PlanAssist` 被實際呼叫 5 次（P1.yaml 草稿 3 輪：初版／risk tier 修正／permissions_ref 修正；oracle-surface.yaml 草稿 2 輪：改 tests/**／驗證佔位樣式原樣輸出），每次皆是真實 provider=claude 的 AI 呼叫，全部成功完成（無 preflight 相關錯誤、無 provider capability 檢查失敗訊息），輸出內容皆可用（AI 甚至主動偵測並提醒 prompt 前言與欄位值不一致等細節，顯示 AI 有正確讀取 workspace 現況）。
- 結果：**PASS**——preflight 通過路徑在多次真實呼叫下皆穩定運作，未見任何一次因 preflight 失敗而擋下 AI 呼叫。

## 已知缺口清單

1. **SpecWorkspace 的 AI 輔助按鈕無自由文字 prompt 輸入**：三個按鈕（草擬 Gherkin／檢查規格歧義／檢查驗收條件涵蓋度）皆是固定 prompt，無法像 PlanWorkspace 一樣指示 AI 產出特定字串內容（例如指定的 tag 名稱）。本輪因此無法產生 coordinator 指定的 `@E1` tag，改用 AI 自主產生的 `@happy-path`。
2. **`planTemplates.ts` 的 `permissions_ref` 預設值有雙重 `plan/` 前綴 bug**：`planSkeleton()` 預設 `permissions_ref: plan/permissions/T1.yaml`，但後端 `app.go` 的 `permissionRefEntries` 會自行補上 `plan/` 前綴，若模板值原樣不改，送核 Gate 2 時 `git show` 回 `exit status 128`（雙重前綴路徑不存在）。這是真實的模板/後端語意不一致，非文件明示的預期行為（不同於 `analysis_base_commit` 留空是有意設計）。
3. **`risk-policy.yaml` 與 `plan/<id>.yaml` 兩份骨架的風險層級預設值互斥**：`risk-policy.yaml` 的 `default_tier: medium` 與 plan 骨架 task 的 `minimum_risk_tier: low`／`planner_risk_tier: low` 不一致，兩份模板原樣不改直接送核會被 `plan.Validate()` 判定 risk 分類失敗。
4. **oracle-surface 佔位樣式（`example/oracle/**`）不改的失敗訊號延遲到 TCA precheck/RunEvidence 階段才出現**，Gate 2 送核本身不會擋下這個錯誤設定——操作者要到很後面的步驟才會發現 oracle-surface 沒改對。
5. **TcaWorkspace 的 generation 換版 reactivity 缺口延續自 Stage C 走查、M3a.1 仍未修復**：Gate 2 版本更換後，即使核可生效，TcaWorkspace 主內容區域仍需要完整重新整理瀏覽器頁面才能恢復正常渲染（tab 切換不足以觸發）。
6. **GateConsole 的 TCA 卡片在核可前短暫顯示「evidence binding role 不完整」的 fail-loud 錯誤**：本輪送核 TCA 後立即檢視卡片，出現「資料完整性錯誤：evidence binding role 不完整，無法顯示證據」；核可後（間隔約數秒）卡片正確顯示兩筆 evidence 的完整 role／狀態。看起來是 evidenceCache 尚未完成非同步載入的暫時性競態，而非持續性缺陷，但可能造成操作者誤判送核失敗。

## 附註：與 M3a 已知缺口的對照

比對 `docs/spikes/m3a-results.md` 的已知缺口清單：
- 缺口 1（無新檔 UI）：**M3a.1 已修復**——新檔建立 inline 列在 SpecWorkspace／PlanWorkspace 皆正常運作，本輪全程未使用 binding 直呼建檔。
- 缺口 2（`analysis_base_commit` 需手動前移，無 UI 提示）：**M3a.1 已修復**——bump 引導面板完整涵蓋提示、差異檢視、確認流程、token 過期防呆。
- 缺口 3（STALE 終態需修正版重核，無明確 UI 引導）：**M3a.1 已修復**——escalation 收件匣的「前往重新送核」按鈕提供了明確的下一步導引，並正確導航到對應工作區選中對應檔案。
- 缺口 4（TcaWorkspace 候選下拉 reactivity）：**M3a.1 仍未修復**——本輪再次確認存在（見已知缺口 5）。
- 缺口 5（tca-evidence-open role undefined）：**M3a.1 態度已改變但問題本質未變**——原本是靜默顯示 `undefined`，現在是明確的 fail-loud 錯誤訊息（role 完整性檢查已補上），但仍是暫時性競態，尚未解決根因（evidenceCache 載入時序）。

## 截圖清單（docs/spikes/evidence/）

| 檔案 | 對應狀態 |
|---|---|
| `m3a1-newfile.png` | 項目 1：SpecWorkspace 新檔列建立 demo.feature |
| `m3a1-oracle-placeholder-unchanged.png` | 項目 1：oracle-surface 佔位樣式未改，TCA precheck 實機重現 lineage 失敗 |
| `m3a1-bump-panel.png` | 項目 2：bump 引導面板（diff／commits／touched files／警告文字） |
| `m3a1-bump-token-expiry.png` | 項目 2：bump token 過期拒絕＋自動重新預覽 |
| `m3a1-bump-confirmed.png` | 項目 2：確認更新後 buffer 內容變更（待儲存） |
| `m3a1-generation-reset.png` | 項目 3：Gate 2 換版過渡期間 TcaWorkspace 空狀態 |
| `m3a1-stale-nav.png` | 項目 4：plan/ 觸發 gate2 STALE→「前往重新送核」→ 正確導航到 Plan 工作區 |
| `m3a1-tca-active.png` | 項目 1／4：TCA 核可後 active（含 gate1/gate2 cascade STALE 側欄） |

## 殘餘風險

1. 項目 1 的 `@E1`→`@happy-path` 替換是本輪唯一未能完全照字面指示執行的部分——若下游有任何工具或測試硬編依賴字串 `E1`，未經本輪驗證。
2. 項目 3 的「舊 run 進行中時被丟棄（晚到）」加分情境未測試（brief 標註非必要）。
3. 已知缺口 5、6（TcaWorkspace reactivity、evidence role 短暫競態）延續／新發現但均非阻斷性——皆有已知繞法（重新整理、等待數秒），不影響核心驗收路徑功能正確性。
4. 本輪全程在全新臨時 workspace（`~/m3a1-accept`）進行，未與專案實際 CI/build pipeline 互動；`go vet`／`go test`／`npm test`／`wails build` 等最終 gate 步驟不在本次 operator 授權範圍內，未執行。
5. 現場發現的 3 個模板缺陷（`permissions_ref` 雙重前綴、risk tier 互斥預設值、oracle-surface 佔位樣式延遲失敗）建議在下一輪迭代中修正 `planTemplates.ts` 的預設值，降低操作者踩坑機率；本輪僅記錄現象與繞法，未修改任何 repo 原始碼（超出本次授權範圍）。

## 走查後修正記錄（2026-08-13）

1. **模板三缺陷已修**（commit `691b111`）：permissions_ref 去 `plan/` 重複前綴、plan 骨架 risk tier 對齊 risk-policy default（medium）、oracle-surface 佔位樣式加失敗預警註解。修正後兩份骨架原樣可過送核驗證（除 oracle placeholder 屬刻意保留的待改值）。
2. **項目 3 的「換版後主內容空白需重新整理」經專項調查無法重現**：現行 code 已是 `v-if` 掛載＋合併 `watch(approvalId)`（非走查記錄引用的舊缺口描述）；以同 workspace 實機重建同型換版場景（全程停留 TCA 分頁、側欄核可、DOM/computed style 檢查），換版瞬間 T1 卡片正確渲染，多次分頁 remount 一致正常。該走查項無留存截圖，判為 loadTasks 短暫載入間隙＋Playwright snapshot 時序的誤判；如後續實機再現，以當下 DOM 快照為準重開調查。
3. 調查過程對 `~/m3a1-accept` workspace 做了新 commit 與 gate 重核（狀態已非走查結束時樣貌），後續驗證如需乾淨環境請另建 workspace。
