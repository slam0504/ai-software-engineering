# M3a 驗收結果

- 執行依據：`.superpowers/sdd/2026-08-12-m3a-plan-test-contract/task-27-brief.md`（A1–A10 驗收矩陣）
- 基線：branch `m3a-plan-test-contract`（Task 0–26 全數 commit，含 escalation 收件匣 UI／Gate 卡片「建立升級項目」按鈕／TCA workspace 修正）
- 狀態：**A1–A10 全數 PASS**（實機 `wails dev` + Playwright 驅動 browser 前端執行，2026-08-13；證據見下）。本輪之前已完成 Stage B／C 個別走查（見 `phase2-gate-report.md`），本輪是完整 A1–A10 矩陣＋全新臨時 workspace 從零驗證。

## 環境準備

- 全新臨時 workspace `~/m3a-accept-final`：`git init`、`user.name/email` 設為 `M3a Operator`、初始 commit `b054d60`（README.md）。
- 重新 `export WORKBENCH_WORKSPACE=$HOME/m3a-accept-final` 後 `wails dev`（`~/go/bin/wails`），拿到本輪新增的 escalation 收件匣側欄 tab、GateConsole 每張卡片的「建立升級項目」按鈕、TcaWorkspace 既有修正。編譯＋起服務約 1 分半。
- A1 起走 UI 優先：SpecWorkspace／PlanWorkspace 目前仍**沒有新檔路徑輸入 UI**（同 Stage B／C 已記錄的已知缺口），新檔一律經 `window.go.main.App.SpecWrite`／`PlanWrite` binding 直寫，寫入後即可在檔案清單正常選取、編修、commit、送核——commit／送核／核可全程走真實 UI 操作。

## 驗收矩陣

| # | 情境 | 預期 | 結果 |
|---|---|---|---|
| A1 | Stage B 全程：PlannerAssist→編修→驗證→commit→送核→per-task risk 核可 | Gate 2 active，metadata 含排序 risk_decisions | **PASS** |
| A2 | Gate 2 核可後建 test commit（HEAD 前移） | Gate 2 **不** STALE（§3.9 歷史錨點） | **PASS** |
| A3 | 改 spec/ 檔案 | Gate 2 STALE＋收件匣 hard 項 | **PASS** |
| A4 | Stage C：test commit→mutation→expected_red＋negative_control→TCA 送核→核可 | TCA active；evidence 詳情可回溯 recording | **PASS** |
| A5 | 改 oracle-surface 檔案 | TCA STALE（oracle 重算）＋收件匣 | **PASS** |
| A6 | Gate 2 被新版 plan supersede | 所屬 TCA 連動 STALE | **PASS** |
| A7 | 故意讓測試編譯失敗跑 expected_red | result=error＋收件匣項；TCA 送核被拒 | **PASS** |
| A8 | 修復 A7→重跑 evidence→系統 resolve→重送核 | 閉環解除阻擋（§1 fail-closed 閉環） | **PASS** |
| A9 | 手動建立升級項目（Gate 2 卡片）→核可被擋→resolve→核可通過 | §3.10 barrier | **PASS** |
| A10 | App 重啟 | gate／evidence／escalation projection 完整重建；orphan worktree／temp 清理 | **PASS（部分：見偏差 6）** |

## 逐項記錄

### A1：Stage B 全程

- 操作：SpecWorkspace 建 `spec/features/demo.feature`（`@E1`＋`Scenario: 示範`）→ commit `1bcf84d` → 送核 → 核可（gate1 `01KZWJ92YJ0002CWWZ8VQK8M4Y`）。PlanWorkspace 觸發 PlannerAssist 一次（provider=claude，唯讀 one-shot，正確讀出 `.workbench/gate.jsonl` 的 gate1 binding digest，回覆完整 plan YAML 草稿＋限制說明）；依 schema（`internal/plan/types.go`）手動組出 `plan/P1.yaml`＋`plan/risk-policy.yaml`＋`plan/permissions/T1.yaml`＋`plan/oracle-surface.yaml`（T1 test_contract 直接用 `sh tests/run_test.sh`，避免二次送核）→ commit `697d646` → 送核 Gate 2（planID=P1）→ risk 列自動展開、T1 minimum/planner 皆 medium、下拉預設 medium 未改動 → 核可（gate2 `01KZWJBKBP000C5TXBGZWV2G4A`）。
- 預期：Gate 2 active，metadata 含排序 risk_decisions。
- 實際：`.workbench/gate.jsonl` 核可記錄 `metadata.risk_decisions: [{"task_id":"T1","minimum_risk_tier":"medium","planner_risk_tier":"medium","selected_risk_tier":"medium"}]`（單一 task，排序條件trivially 成立）。GateConsole 顯示 5 筆 bindings（spec_manifest／plan／base_commit／risk_policy／permission_manifest）。
- 結果：**PASS**。

### A2：Gate 2 核可後 HEAD 前移不觸發 STALE

- 操作：workspace 直接 git commit 一筆與 plan/spec 無關的 README 變更（`e3d8902`，`chore: A2 HEAD-move marker`）。
- 預期：Gate 2 base_commit 是歷史錨點，不因 HEAD 前移而 STALE。
- 實際：`GateList()` 回傳 gate2 `01KZWJBKBP000C5TXBGZWV2G4A` 仍 `state: "active"`（`Gate2Policy.ReconcileBindings` 只在 `rev-parse --verify` 找不到 base_commit 時才報 stale，HEAD 前移不影響）。
- 結果：**PASS**。

### A3：改 spec/ 檔案觸發 STALE＋收件匣 hard 項

- 操作：`SpecWrite` 直接修改 worktree 的 `spec/features/demo.feature`（追加一行，未 commit）。
- 預期：Gate 2 STALE＋收件匣 hard 項。
- 實際：worktree 變更觸發 `watchSpecTree` → `reconcileGate1NotifyOnly` → `reconcileLocked`，**gate1 與 gate2 皆轉 STALE**（gate1 自身 spec_manifest 綁定也對到 worktree 現況，同樣過期）；收件匣新增 2 筆 hard 系統項（`stale:gate1:workspace`、`stale:gate2:plan:P1`），UI 顯示「硬性項目：僅系統可解除」。截圖 `m3a-a3-stale-escalation.png`。
- 額外發現（偏差 1，見下）：把內容還原成與已核可 digest**完全相同**的位元組後，Gate 仍維持 STALE——`gate.Service.Reconcile()` 只把 STALE 當終態寫進 journal（append-only transition），revert 內容不會讓它自動變回 Active，必須走「修正版重核」（新 approval 走 `GateDecide` 的 2b 步驟才會 `ResolveByKey` 解除對應 hard 項）。
- 結果：**PASS**（含額外驗證的 fail-closed 語意）。

### A4：Stage C 全程

- 操作：workspace 建 `tests/run_test.sh`（`echo "FAIL: TestX"; exit 1`，恆紅腳本）→ commit `ec6b36b`（先修正 `analysis_base_commit` 越過 A2 marker → gate2 v2 `01KZWJK5T90003CF510X3BNHZG` 核可，見偏差 2）。TcaWorkspace：選 test commit → 預檢通過 → 貼 README.md 的 mutation patch → 登記 mutation → 跑 expected-red（通過）→ 跑 negative-control（通過）→ 送核 TCA → 核可（`01KZWJQA9Y0007SWT2P5C08AK1`）。
- 預期：TCA active；evidence 詳情可回溯 recording。
- 實際：GateConsole tca 卡片顯示 6 筆 bindings（gate2_approval／base_commit／oracle_surface／evidence_run×2／mutation），兩筆 evidence 皆「通過」；EvidenceDetail overlay 顯示完整欄位（command=`sh tests/run_test.sh`、exit_code=1、oracle_surface_digest、stdout/stderr digest、`recording_ref` 指向 `.workbench/evidence/cas`）。截圖 `m3a-a4-tca-workspace.png`（雙徽章）、`m3a-a4-tca-active.png`（核可後 active）。
- 結果：**PASS**。

### A5：改 oracle-surface 檔案（tests/ 內容）觸發 TCA STALE

- 操作：直接改 worktree `tests/run_test.sh`（追加一行註解，未 commit）——注意「oracle-surface 檔案」指的是**落在 oracle surface pattern（`tests/**`）內的檔案**，不是 `plan/oracle-surface.yaml` 宣告檔本身（讀過 `TCAPolicy.ReconcileBindings` 的 `currentOracleDigest` 確認：宣告檔內容凍結在 gate2 approval 的 plan_commit，「持續重算」的是宣告範圍內檔案的**目前**內容）。
- 預期：TCA STALE（oracle 重算）＋收件匣。
- 實際：`GateList()` 立即回報 TCA `state: "stale"`；隨後（因同批也寫了 plan/P1.yaml，觸發 `watchPlanTree`）收件匣新增 hard 項 `stale:test_contract_approval:task:P1/T1`。截圖 `m3a-a5-oracle-stale-escalation.png`。
- 結果：**PASS**。

### A6：Gate 2 被新版 plan supersede → 所屬 TCA 連動 STALE

- 操作：A5 的 TCA 一旦 STALE 即為終態（同 A3 發現），無法用它乾淨驗證「僅因 gate2 supersede 而 stale」（會混進 A5 的 oracle 起因）。改建第二輪乾淨 TCA：新 test commit `6695c4274e`（僅追加 tests/ 內容）→ gate2 v3 `01KZWJYQ8T000BSKSTK53BJATA` 核可 → 全新 TCA `01KZWK1VAH000EK105QNY7PXBC` 核可 active（未觸碰任何 oracle 檔案）。接著送核並核可 gate2 v4 `01KZWK41H60006D05616GSN4B9`（僅改 plan/P1.yaml 標題字串，未觸碰 tests/）。
- 預期：所屬 TCA 連動 STALE。
- 實際：gate2 v4 核可後，`GateList()` 顯示 TCA `01KZWK1VAH000EK105QNY7PXBC` 轉 `stale`；`.workbench/gate.jsonl` 的 transition 記錄精確為 `{"cause":"gate2_approval not active","evidence_ref":"stale"}`——乾淨排除 oracle_surface 起因，單一 cause 命中設計預期。截圖 `m3a-a6-gate2-supersede-tca-stale.png`。
- 結果：**PASS**。

### A7：測試編譯/執行失敗 → result=error＋收件匣＋TCA 送核被拒

- 操作：新 test commit `8f04dc7`（`tests/run_test.sh` 改成呼叫不存在指令），對 gate2 v4 跑 expected-red。
- 預期：result=error＋收件匣項；TCA 送核被拒。
- 實際：TcaWorkspace 顯示「錯誤」徽章；`EscalationList()` 新增非 hard 系統項 `evidence-error:P1/T1/expected_red`（`hard:false`）。TCA UI 送核按鈕因 `bothPassed()` 為 false 維持 disabled（UI 層擋）；額外直接呼叫 `SubmitTestContract`（帶兩筆 result=error 的 evidence id）驗證後端層——Submit 本身成功（`ValidateRequest` 只驗 binding 格式），但 `GateDecide('approved', …)` 明確拒絕，錯誤原文：`gatepolicy: tca both evidence runs must have result "passed" (expected_red="error", negative_control="error")`。截圖 `m3a-a7-expected-red-error.png`。
- 結果：**PASS**（UI 層與後端層雙重擋下皆驗證）。

### A8：修復→重跑→系統 resolve→重送核

- 操作：新 commit `9b4a00b` 修回正確恆紅腳本；重新 precheck／登記 mutation／跑 expected-red／negative-control（皆「通過」）。
- 預期：閉環解除阻擋。
- 實際：`evidence-error:P1/T1/expected_red`／`evidence-error:P1/T1/negative_control` 兩筆收件匣項**未經任何手動 resolve 呼叫**即自動轉 `resolved`（`wireEvidenceEscalation` 的 `passed` 分支自動 `ResolveByKey`）。清掉 A7 遺留的舊 pending TCA request（`退回`／`GateDecide rejected`）後，重新送核＋核可成功（TCA `01KZWKC1Y5000F1J0MWCG0N4W2` active）。截圖 `m3a-a8-evidence-error-resolved.png`（自動解除當下）、`m3a-a8-tca-active-after-fix.png`（重核成功）。
- 結果：**PASS**。

### A9：手動升級項目 barrier

- 操作：新 gate2 v5 `01KZWKFATP0007BXNE5WWQV82N` 送核（pending，未核可）→ 點該卡片「建立升級項目」（prefill sourceRef=`approval:01KZWKFATP0007BXNE5WWQV82N`、blockScope 自由輸入補成 `gate2:P1`）→ 填摘要 → 建立（手動、非 hard）→ 嘗試核可。
- 預期：核可被擋→resolve→核可通過。
- 實際：`GateDecide('approved', …)` 回錯，原文：`blocked by 1 escalation item(s): 01KZWKG9TT0005H9AED9RHDB83（A9 手動升級：…暫時封鎖 gate2:P1 決議）`。截圖 `m3a-a9-manual-escalation-blocks.png`。在 EscalationInbox 用「已修復（fixed）」＋理由解除該項後，重新 `GateDecide('approved', …)` 成功（gate2 v5 轉 active）。截圖 `m3a-a9-gate2-approved-after-resolve.png`。
- 結果：**PASS**。

### A10：App 重啟

- 操作：重啟前記錄基線（`GateList()` 11 筆、active=[gate1 `01KZWJG1X3000D3HJ3ADZ7W1FD`、gate2 `01KZWKFATP0007BXNE5WWQV82N`]；`EscalationList()` 12 筆、1 筆未解除）。嘗試誘發 orphan worktree：`RunEvidence` 呼叫故意不 await 後立刻 `kill -9` wails 行程群組（含子行程 vite／編譯後的 `.app` 二進位），模擬非正常關閉。重新 `wails dev` 後比對狀態。
- 預期：gate／evidence／escalation projection 完整重建；orphan worktree／temp 清理。
- 實際：
  - `CLIInfo().startupError` 為空字串——`startupEvidence()` 內 `evidence.CleanupOrphans`／`CleanOrphanTemps` 皆無錯誤回報。
  - `GateList()` 重啟後仍 11 筆，active 集合與重啟前**完全一致**（gate1／gate2 v5 approval_id 不變）。
  - `.workbench/evidence/worktrees.jsonl` 檢查：無殘留未配對的 `wt_intent`／`wt_active`（每筆都有對應 `wt_removed`）；`/var/folders/.../T/wb-evidence-*` 臨時目錄無殘留。
  - 截圖 `m3a-a10-restart-projection-rebuilt.png`。
- 偏差（見下偏差 6）：本次「誘發 orphan」的競態未命中——`tests/run_test.sh` 執行近乎瞬時，且該次呼叫的 `test_commit`（`9b4a00b`）落在當時 active gate2 v5 的 plan_commit（`0f0181f`）**之前**（lineage 方向不合法），`evidence.Run` 在建立 worktree**之前**的 lineage 檢查就直接失敗回傳——沒有機會產生真正的孤兒 worktree 可供本次驗收觀察。改為以程式碼審讀＋重啟後無殘留兩者交叉確認 cleanup 路徑存在且無害觸發，如實記錄為部分／間接驗證（非缺陷）。
- 結果：**PASS（部分：orphan 清理僅程式碼審讀＋間接驗證，未能實機捕捉到真正孤兒 worktree 案例）**。

## 已知缺口清單

1. **SpecWorkspace／PlanWorkspace 無新檔路徑輸入 UI**：本輪與 Stage B／C 一致，新檔一律走 binding 直寫繞道（`SpecWrite`/`PlanWrite`），寫入後才能在既有 UI 正常選取／編修／送核。
2. **`plan/<id>.yaml` 的 `analysis_base_commit` 需在每次有「非 plan/ 提交」插入 plan_commit 與 analysis_base_commit 之間時手動前移**（§3.0 lineage：`VerifyLineage` 要求該區間內每個變更路徑都落在 `plan/**`）。A2（HEAD 前移一筆 README commit）之後，A4／A6 的下一次 gate2 送核都因此先撞到 `plan: lineage: <status> <path>: path outside allowed scope`，需先修正 `analysis_base_commit` 才能重送。這是 fail-closed 的預期行為，但目前沒有 UI 提示「需要 bump analysis_base_commit」，操作者只能從錯誤原文推回去修。
3. **Gate STALE 是 append-only 終態，不會因 worktree 內容還原回已核可版本而自動變回 Active**（A3 額外驗證）：需要一筆新的「修正版」核可（`GateDecide` 的 2b 步驟）才會解除對應的 hard escalation。若操作者誤以為「把檔案改回去」就能解除阻擋，會卡在這裡——建議之後在 escalation 摘要文字或 UI 提示更明確標出「解法是重新送核，不是回復內容」。
4. **TcaWorkspace 的 test_commit 候選下拉在同一 `planID` 字串下、僅 active gate2 approval 換版時不會自動重新載入**（`TcaWorkspace.vue` 用 `v-show` 常駐掛載，`watch(planId)` 而非同時 watch `activeGate2.value?.approval_id`）——本輪 A4／A6 重建 TCA 前都靠瀏覽器重新整理繞過，與 Stage C 走查已記錄的缺口相同，尚未修正。
5. **GateConsole tca 卡片「查看證據」按鈕的 `data-test` 屬性偶發顯示 role 為 `undefined`**（Stage C 已記錄的次要缺陷，功能本身不受影響，未深入追根因）。
6. **A10 的 orphan worktree 清理僅完成程式碼審讀＋重啟後無殘留的間接驗證**：受限於 `tests/run_test.sh` 執行速度遠快於本次 operator 工具鏈的往返延遲，無法可靠在 `RunEvidence` 建立 worktree 的窗口內精準命中 `kill -9`；本次嘗試命中的反而是「lineage 檢查早期失敗」路徑（產生一筆非預期的 `evidence-error` escalation，見附註），未能實機重現「真正孤兒 worktree 被清除」的完整案例。`evidence.CleanupOrphans`／`CleanOrphanTemps` 在 `app.go` 的 `startupEvidence()` 內確認為每次啟動無條件呼叫（程式碼審讀），且重啟前後查無殘留 `wb-evidence-*` 臨時目錄，是間接但一致的證據。

## 附註：A10 過程中的意外發現

嘗試誘發 orphan 的 `RunEvidence` 呼叫（test_commit 早於 active gate2 的 plan_commit）驗證了另一條 fail-closed 路徑：`evidence.Run` 的 lineage 檢查在建立 worktree**之前**就先失敗，此時 `app.go` 的 runner-level error 分支（`RunEvidence` 第 2080 行附近）會另開一筆 `evidence-error:<plan>/<task>/<kind>` 系統項（非 hard），即使沒有產生任何 durable `EvidenceRun` 或 worktree 也不會無聲失敗。這與 A7/A8 驗證的「已產生 EvidenceRun 但 result=error」是兩條不同但同樣 fail-closed 的路徑，非本次矩陣明列項目，但作為額外交叉驗證證據記錄於此（收件匣項 `01KZWKJSDA000BCWHNAEMWRJ95`）。

## 截圖清單（docs/spikes/evidence/）

| 檔案 | 對應狀態 |
|---|---|
| `m3a-a3-stale-escalation.png` | A3：gate1/gate2 皆 STALE，收件匣 hard 項展開 |
| `m3a-a4-tca-workspace.png` | A4：TcaWorkspace 雙「通過」徽章 |
| `m3a-a4-tca-active.png` | A4：TCA 核可後 active |
| `m3a-a5-oracle-stale-escalation.png` | A5：oracle-surface 內容漂移，TCA STALE＋收件匣 |
| `m3a-a6-gate2-supersede-tca-stale.png` | A6：gate2 supersede 後 TCA STALE |
| `m3a-a7-expected-red-error.png` | A7：expected-red result=error，收件匣項 |
| `m3a-a8-evidence-error-resolved.png` | A8：evidence-error 項自動 resolved |
| `m3a-a8-tca-active-after-fix.png` | A8：修復後重送核成功 active |
| `m3a-a9-manual-escalation-blocks.png` | A9：手動升級項目擋下核可 |
| `m3a-a9-gate2-approved-after-resolve.png` | A9：resolve 後核可成功 |
| `m3a-a10-restart-projection-rebuilt.png` | A10：重啟後 gate/escalation projection 一致 |

（A1／A2 以 `GateList()`／`.workbench/gate.jsonl` 原文佐證，未額外截圖——狀態本身無需視覺呈現，JSON 記錄已是最直接的證據。）

## 殘餘風險

1. 已知缺口 2、3（`analysis_base_commit` 手動前移、STALE 終態需修正版重核）都是目前 UI 沒有明確引導的操作陷阱——功能行為正確（fail closed），但操作者體驗上容易卡關，建議之後補 UI 提示或文件。
2. 已知缺口 6：A10 的 orphan worktree 清理僅間接驗證（程式碼審讀＋重啟後無殘留），未能實機重現「真正孤兒被清除」的完整案例，屬本次驗收條件下的時間窗口限制，非功能缺陷；若要嚴謹補齊，需要一支刻意拉長執行時間的假測試腳本（例如 `sleep 5`）搭配精確計時的 kill，才能可靠命中 worktree 建立後、移除前的窗口。
3. 已知缺口 4、5（TcaWorkspace 候選下拉 reactivity、tca-evidence-open data-test role undefined）延續自 Stage C 走查記錄，本輪未修正、僅再次確認存在，不影響核心驗收路徑（皆有已知繞法）。
4. 本輪 Task 27 brief 的 Step 2（`go vet`／`go test -race`／`npm test`／`wails build` 等最終 gate）與 Step 3（commit）**不在本次 operator 授權範圍內**（僅授權寫入截圖＋本檔＋臨時 workspace），未執行、留待 owner 另行處理。

## 最終 gate（controller 執行，2026-08-13）

| 項目 | 結果 |
|---|---|
| `go vet ./...` | ✅ |
| `go test -race ./... -count=1` | ✅ 16 packages ok |
| `go test -race -count=30 -run 'TestGateDecideBarrier\|TestStaleBlockerReleased'` | ✅（213s，30 輪競態壓力） |
| `npm --prefix frontend run test` | ✅ 30 檔／179 案 |
| `npm --prefix frontend run build` | ✅（vue-tsc＋vite） |
| `wails build` | ✅ 產出 build/bin/sdlc-workbench.app |
| .app 冒煙 | 未獨立自動化——等效路徑已由 wails dev 下的 A1–A10 完整實機驗收覆蓋（誠實記錄） |

## Ledger deferred 清單（實作期間各 task review 的未阻斷項，供後續 triage）

- 待 owner 決策：`oracle_surface.ref` 格式——spec §3.4 凍結 `git:<algo>:<oid>` vs 實作全線裸 OID（內部一致）；建議保留裸 OID＋spec erratum
- SC4 完整度：SpecWorkspace／PlanWorkspace 無「新檔」UI（驗收以 binding 直呼建檔繞道）
- OracleDigestAt 對 scope 不匹配無 runtime guard（doc 已明示前提）
- PlannerAssist Claude 唯讀白名單 live probe 未做（人工步驟）
- (9) planner-enforcement escalation 觸發點缺（key＋函式已備）
- 其餘 minor 級（測試覆蓋補強、cosmetic、pre-existing）詳見 .superpowers/sdd ledger——共約 30 條，均不影響核可正確性路徑

## 最終裁決記錄（owner 2026-08-13）

`oracle_surface.ref` 格式衝突裁決：**採裸 OID＋spec erratum**——ref 屬 reference 非 digest；凍結為：reference 類欄位（`oracle_surface.ref`、`EvidenceRun.test_commit`）＝完整裸 Git OID（SHA-1 40 hex／SHA-256 64 hex）；commit 類 `Binding.digest` 維持 `git:<algo>:<full-oid>`；既有 journal 不遷移不回寫。已同步修訂 spec §3.4（erratum）與 plan Global Constraints。
