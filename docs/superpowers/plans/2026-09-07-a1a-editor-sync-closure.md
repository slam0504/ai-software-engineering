# A1a Spec／Plan 編輯器同步閉環 Implementation Plan

> **For agentic workers:** 本票為前端佈線與測試，無外部寫入。實作前須通過 owner design gate；push、開 PR、CI 另案授權。Steps use checkbox (`- [ ]`) syntax for tracking.
> 版本：rev13（2026-09-08，**證據封存遺失紀錄**：新增第十三節——`/tmp/a1a-1`（120 檔）與 `/tmp/a1a-2`（143 檔）於 2026-09-08 發現整個遺失、清除時點與原因未知；保留歷史 Gate 裁定、manifest hash 與**當時**的驗證結果，另記發現時間、影響範圍與**部分回收**狀態；往後證據改存 `~/.local/share/sdlc-evidence/`，`/tmp` 僅作暫存。**不重跑、不重建、不覆寫歷史裁定**）；前版：版本：rev12（2026-09-08，**A1a-2 Gate 通過回填**：owner 裁定綁定 `85d6418`，最後一格「Wails GUI 證據與涵蓋限制裁定」勾選並保留操作歸屬（agent 執行、非人工／非 owner 確認）與兩項 GUI 未涵蓋；A1a 原票關票條件勾選為 **aggregate 本機驗收完成**。測試與 GUI 結論不變）；前版：版本：rev11（2026-09-08，**文件補正（不重跑測試或 GUI）**：狀態圖 rev5 補上 H5-P 的例外路徑（同工作區換檔且載入失敗時 buffer／saved／dirty 全部保留）與捨棄時的 busy 再檢查、無效導覽不進 guard；Gate 最後一格改稱「Wails GUI 證據與涵蓋限制裁定」；恢復「未接線掃描」一格；§12 的 `f719d43` 正名為**受測原始碼 HEAD**）；前版：版本：rev10（2026-09-08，**A1a-2 執行紀錄回填**：新增第十二節（expected-red 26 條 R18／G8、owner 複核發現的兩條繞過路徑與修法、mutation 24 項 N/N 與 harness v2 的備份還原、agent 執行的 Wails GUI 驗證與其涵蓋限制、證據封存 143 檔）；Gate A1a-2 逐項回填，**人工驗收一格維持未完成**、Gate 未判定通過）；前版：版本：rev9（2026-09-07，**紀錄修正與證據封存**：`getClientRects` 雜訊計數 35 → **36**（附計數方法；不含的兩份是不掛載 CM6 的 App 測試）；§11.5 措辭改為「這些批次未重現」並改記「已登記 register v8 候選 C1」；新增 §11.4b A1a-1 證據封存（manifest 120 檔、120/120 OK）。A1a-2 開工前的最終狀態）；前版：版本：rev8（2026-09-07，**複核修正與證據歸屬補齊**：更正「紅燈日誌零環境失敗訊息」的措辭為「沒有任何目標失敗是環境造成的」並揭露 35/38 份仍含 jsdom 量測雜訊、摘要擷取已離線修正（原始日誌保留、不重跑）；補齊人工 Wails 驗收的實際操作者、原始確認紀錄與受測版本；記錄 register v8 候選登記與 backlog A5 bindings 同步小票的處置）；前版：版本：rev7（2026-09-07，**A1a-1 執行完成回填**：expected-red 結果（R=27／G=3）、mutation **38 項 N/N 全部紅在正題並回綠**（含框架修正與並行汙染的揭露）、人工 Wails 驗收通過（owner 實機確認、隔離工作區）、Gate A1a-1 全部勾選、新增「A1a-1 剩餘揭露」。實作見 `ddbbd2d`／`da20294`／`70dbe04`／`af0cae2`。A1a-2 未授權）；前版：版本：rev6（2026-09-07，**實作啟動回填（設計不變）**：狀態改為 design gate APPROVED（綁定 `7860d75`）、A1a-1 實作進行中；新增第十一節「A1a-1 執行 checklist」（30 個測試識別項目、31 項變異、expected-red 分類規則、人工驗收資料隔離、失敗分類規則）；**測試計數更正 33 → 30**（明列 13 Spec ＋ 16 Plan ＋ 1 Go；不為湊數新增測試）；**前端基準更正 397 → 399 條**（本機實測 40 檔／399 全綠）；**D5 可行性已實測**：jsdom 下 `beforeinput`／`input` 不改變 CM6 文件（doc 未變），採 `view.dispatch` 退路並標明邊界。設計、估點、拆票不變）；前版：版本：rev5（2026-09-07，**窄複核修正（僅一處契約與三處引用；設計、估點、拆票不重開）**：`confirmBump` 改為四條結束路徑——等待期間被阻止的是切檔／切分頁而**不是打字**；**(a) 正常續打造成 buffer 版本過期改以實際編輯器輸入驗證**（不再誤稱只能注入），(b) 文件識別被替換才用測試注入，(c) 未續打且版本相符則套用，(d) 真正的後端錯誤保留原訊息，**四條路徑結束時一律解除封鎖**；feature 與 T14f／T14g／T14h 同步（未新增測試編號、未改架構、未重估）；同批同步引用：A1a-1 分攤表改為 T14a–T14h、Gate A1a-1 的版本檢查改指 T14g 並納入 T14h、backlog 的 plan 引用改為 rev5。場景 33 → 34 個可執行案例。**待 owner 最終窄複核**）；前版：版本：rev4（2026-09-07，**窄幅一致性修正（設計選項不重開）**：(1) `confirmBump` 等待期間的限制與測試情境分為兩層——正常操作允許繼續輸入但阻止切檔／切分頁與衝突操作，「送出版本已過期」改以**測試注入**的防禦性驗證表達，不再描述成 UI 可繞過；本機判定過期時顯示「內容已變更，請重新預覽」，**真正的後端錯誤仍保留原訊息**；(2) 兩張圖改正——載入循序圖以 `alt` 區分過期丟棄與最新才更新，狀態圖補「Plan 套用結果等於 `saved` 留在 clean」「Spec 自 clean 接受草稿進入寫入流程」，並把衝突／失敗改為經 `recompute` 依內容比較決定 dirty，不無條件轉 dirty；(3) D6 撤除「15 項」舊數字改指逐元件展開的實際清單；**核定估點回填**（A1a-1 19.1 hr／1.91 pt、A1a-2 6.5 hr／0.65 pt）並同批更新 backlog 小計。**待 owner 窄複核**）；前版：版本：rev3（2026-09-07，**design gate 第二輪 CHANGES_REQUIRED 後修訂**：(1) 草稿語意分流——Plan `applyDraft`／`confirmBump` 只更新 buffer，**Spec `acceptDraft` 保留立即寫入語意**並納入送出快照與寫入互斥，feature／測試／變異同步改寫；(2) 載入回應歸屬改為**請求世代**（補 A→B→A 亂序案例、載入進行中暫停編輯）、`confirmBump` 回應以「文件＋buffer 版本」檢查，不新增通用並行框架；(3) 測試補 M6 的**重載前**斷言、切分頁補捨棄後成功導覽、所有測試與變異標明 Spec／Plan 實際目標且**不預先固定項數**；(4) D9 拆票通過並修正分攤——寫入期間封鎖（含其 `App.vue` 攔截點）留在 A1a-1，A1a-2 只負責非儲存期間的保留／捨棄導覽；重估 25.6 hr（兩票 19.1／6.5）。**待 owner 複核**）；前版：版本：rev2（2026-09-07，**design gate 第一輪 CHANGES_REQUIRED 後修訂**：D1–D8 裁定回寫；新增第三節「非同步儲存與載入契約」（送出快照、等待期間輸入、封鎖重疊、延遲載入回應、草稿／bump 同步、衝突後不自動重載）；場景表改寫並更正條數（12 條 Scenario＋1 條 Scenario Outline／6 例＝18 個可執行案例）；切檔的保留／捨棄拆為兩條獨立情境；新增切分頁保護（Spec／Plan × 三個入口）；T3／T4 改用有狀態讀寫替身並斷言重載後的 `EditorView` 文件；mutation 由 9 項增為 15 項；Gate A 納入 Spec／Plan 各一次實際 Wails 人工驗收；速記展開為正式說明；重估後 **21.6 hr／2.16 pt 超過拆票門檻，提出 A1a-1／A1a-2 拆分（D9）**。**待 owner 複核**）；前版：rev1（2026-09-07，建立）
> 狀態：**A1a-1 Gate 通過（2026-09-07）、A1a-2 Gate 通過（2026-09-08，綁定 `85d6418`）→ A1a aggregate 本機驗收完成**。證據：vitest 466/466、`npm run build` exit 0、`go test ./...` 全綠、A1a-1 mutation 38/38 與 A1a-2 mutation 24/24 四格齊全、A1a-1 人工 Wails 驗收（owner 實機確認）、A1a-2 **agent 執行的 Wails GUI 驗證**（兩項 GUI 未涵蓋已明列並由自動化證據承接）。**push、開 PR、遠端 CI、設定變更未授權**；正式關票與推送待 owner 裁定。
> 票源：Pre-M4 Readiness Backlog **A1a**（P1，原始估計 **1.4 pt**，未經 gate 核准）：A1 驗收條件 (1)(2)(3)(4)(6)。**(5) 外部檔案變更 reload／compare／保留本地屬 A1b，不在本票。**
> 基準：`main`＝`origin/main`＝`3ea31ea`（B2b 關票）。分支 **`a1a/editor-sync`**（本機，自 `3ea31ea`）。
> 相關產出：`docs/architecture/features/spec-plan-editing.feature`、`docs/architecture/diagrams/a1a-editor-buffer-state.mmd`、`docs/architecture/diagrams/a1a-seq-save.mmd`。

## 一、事實重新核對（2026-09-07，HEAD `3ea31ea`；不沿用舊診斷）

由主 agent 指派的獨立讀碼與主 agent 抽驗共同確認，逐項可追溯：

1. **兩個工作區的 CodeMirror 都只掛 `basicSetup`**：`SpecWorkspace.vue:95`、`PlanWorkspace.vue:189` 的 `EditorState.create({ doc, extensions: [basicSetup] })`。`frontend/src`／`frontend/wailsjs` 全域 grep `updateListener` **0 命中**。`syncEditorDoc()`（`SpecWorkspace.vue:82`、`PlanWorkspace.vue:176`）方向永遠是 buffer → editor，無反向路徑。
2. **鍵入的內容不會進入任何會被儲存讀取的變數**。Plan 的 `saveFile()`（`PlanWorkspace.vue:313`）送出 `plan.currentContent`，而該欄位只由 `loadFile()`／`applyDraft()`／`confirmBump()` 設值。**結論：直接鍵入後按儲存，送出的是上次載入或上次套用草稿的內容。**
3. **Spec 連「儲存目前內容」的入口都不存在**。`SpecWorkspace.vue` 只有兩處呼叫 `SpecWrite`：`createNewFile()` 送樣板（`:147`）、`acceptDraft()` 送 `extractGherkin(draftText)`（`:199-202`）。這比「輸入沒被捕捉」更根本——舊診斷未涵蓋此點。
4. **dirty 不對稱且對「鍵入」麻木**。Plan 有 `bufferDirty`（`PlanWorkspace.vue:61`），只在 `applyDraft`（`:307`）、`confirmBump`（`:157`）設 true，`loadFile`（`:113`）、`saveFile` 成功（`:320`）設 false，並綁定 save 按鈕 `:disabled`（`:418`）。Spec 檔內 `dirty` **0 命中**。
5. **後端寫入面已足夠、且是 atomic rename**：`SpecWrite`（`app.go:3731`）／`PlanWrite`（`app.go:4451`）簽章皆為 `(rel, content, expectedDigest string) (newDigest string, err error)`，內部走暫存檔＋`os.Rename`；衝突回傳 sentinel `ErrSpecWriteConflict`（`app.go:3624`）／`ErrPlanWriteConflict`（`app.go:4446`）。
6. **衝突在前端沒有結構化辨識**：sentinel 過 Wails 邊界後只剩訊息字串，元件一律 `String(e)` 塞進 `plan.errors`／`acceptError`（`PlanWorkspace.vue:324`、`SpecWorkspace.vue:208`），既有測試也只斷言字串包含關係。
7. **切檔與切分頁一律靜默覆蓋**：`selectFile()`／`watch(props.path)` 兩邊都無條件 `loadFile()`（Spec `:111`／`:123`，Plan `:223`／`:234`），Plan 即使 `bufferDirty === true` 也照切；`App.vue:326-327` 以 `v-if` 切分頁會卸載元件、切回重新 `onMounted` 載入。全域 grep `unsaved`／`beforeunload`／`discard` **0 命中**。
8. **既有測試全部是注入 props 或直接呼叫 store action**（`SpecWorkspace.test.ts:49`、`PlanWorkspace.test.ts:79` 等），沒有任何一條使編輯器文件改變。jsdom 下建構 `EditorView` 本身可行（兩個測試檔的 `beforeAll` 預熱已證明，B1b 遺留）。
9. **與 A1a 相鄰但不屬本票**：A2 的 error kind 機制已落地（`plan.ts:62` 的 `clearErrors(kind)`，`PlanWorkspace.vue` **五個寫入操作**——`createNewFile`／`saveFile`／`submitForApproval`／`previewCommit`／`confirmCommit`——各自在操作開始與成功時清同類；commit `7789f40` 並已載明 Spec 側同型檢視結論與 escalation 未進入 `plan.errors` 的確認）。本票沿用、不改其語意。

## 二、Global Constraints

1. **不新增 backend API**：只用既有 `SpecRead`／`SpecWrite`／`PlanRead`／`PlanWrite`（D2 已裁定採 sentinel 文字契約，此假設成立）。
2. **A1b 界線**：外部檔案變更偵測、reload／compare／保留本地的選擇流程一律不做。本票只保證衝突可被辨識、如實揭露，且**不自動覆蓋未儲存內容**。
3. **不改 A2 已落地的 error lifecycle 語意**，只新增本票需要的 kind 與判別。
4. **不改 `app.go` 的寫入路徑與 sentinel 文字**；D2 的作法是**新增 Go 訊息契約測試保護該文字**，不修改它。
5. **不改既有草稿與 bump 的業務語意**：Spec `acceptDraft` 的「按接受才寫檔」、Plan 套用後仍要走儲存的兩步慣例、bump 預覽／確認流程與萃取規則一律不變；本票只把它們納入送出快照、寫入互斥與回應歸屬。
6. **不重構分頁架構**：D3 允許為了保護未儲存內容而修改 `App.vue`，但限於加入攔截點，不改寫既有 `v-if` 分頁結構的整體設計。
7. mutation acceptance table 依 §6.7 **N/N 全跑**，不抽樣；變異只植入本票新增或修改的 production code。
8. 前端全套 `vitest` 與 `npm run build` 必須綠；Go 端訊息契約測試所屬套件須綠。

## 三、非同步儲存與載入契約（rev3 改寫）

以下八條為驗收契約的一部分，實作與測試都必須落到。**三種寫入路徑（Plan 儲存、Spec 儲存、Spec 接受草稿）共用第 1–3 條**；草稿與 bump 的語意依操作分開定義（第 5、6 條）。

1. **送出快照**：任何寫入（`PlanWrite`／`SpecWrite`，含 Spec 接受草稿）在觸發當下凍結該次的 `{path, content, digest}` 為送出快照 P，之後的輸入不改變 P。寫入成功後 **`saved` ← `P.content`**（不是回應到達當下的 buffer），持有的 digest ← 回應回傳的 `newDigest`。
2. **等待期間允許輸入**：寫入進行中使用者可繼續編輯；回應後 `dirty` 依 `buffer ≠ saved` 重新計算。
3. **寫入互斥**：同一文件的寫入不可重疊。寫入進行中，再次儲存、Spec 接受草稿、切檔、切分頁，以及其他會替換編輯器內容的操作一律暫停，回應後恢復。**此封鎖屬寫入安全，責任在 A1a-1**（含為此所需的 `App.vue` 攔截點）。
4. **載入回應歸屬＝請求世代**：每次載入配一個遞增的請求識別，回應到達時**只有世代等於最新一次請求才套用**。只比對路徑不足——使用者依序選 A→B→A 時，第一次 A 的舊回應路徑相同卻已過期，必須丟棄。丟棄＝不寫 buffer／`saved`／digest／編輯器。載入進行中**暫停編輯**，避免回應覆蓋新輸入。
5. **Plan `applyDraft`／`confirmBump`：只更新 buffer**。更新編輯器內容與受控 buffer，**不寫入磁碟、不動 `saved` 與持有的 digest**；`dirty` 依內容比較決定——若操作結果恰等於 `saved`，**不得無條件標為未儲存**。既有兩步慣例（套用後仍要走儲存）不變。
6. **Spec `acceptDraft`：保留立即寫入語意**（既有行為與測試 `SpecWorkspace.test.ts:47`「accept writes draft via SpecWrite, not before」不得更動）。接受時**先把受控 buffer 替換為草稿萃取結果**，該內容即為此次的送出快照 P；該次寫入納入第 1–3 條（送出快照與寫入互斥）。等待期間使用者仍可編輯，**新輸入保留在 buffer 上、不被回應覆蓋**；成功後 `saved` ← `P.content`（＝草稿內容）、digest ← `newDigest`，因此若使用者續打則仍為未儲存。失敗時 `saved` 與 digest 皆不變，buffer 保留接受後的內容。
7. **`confirmBump` 的四條結束路徑**（送出時凍結「文件識別＋buffer 版本」）：
   - **等待期間的操作限制**：允許繼續輸入；**被阻止的是切檔、切分頁與其他會替換編輯器內容的操作，不是打字**。
   - **(a) 正常續打造成 buffer 版本過期**：這是正常 UI 就會到達的狀態，**以實際編輯器輸入驗證**（不是注入）。回應不套用、使用者續打的新內容保留、顯示「內容已變更，請重新預覽」。
   - **(b) 文件識別被替換**：正常導覽已被上一條的封鎖擋住，**這部分才以測試注入驗證**。處置與 (a) 相同。
   - **(c) 未續打且版本相符**：套用 bump 結果，更新編輯器與受控 buffer（依第 5 條，不動 `saved` 與 digest）。
   - **(d) 真正的後端錯誤**（例如 token 過期）：**原訊息原樣保留**，不得換成版本已變更的訊息。
   - 四條路徑**結束時一律解除操作封鎖**，並依第 8 條重算 `dirty`。(a)(b)(d) 都走既有的「要求重新預覽」流程。只做上述兩項版本檢查，**不新增通用並行框架**。

8. **衝突與失敗後保留現場、dirty 依內容比較**：辨識為 digest 衝突時，buffer、`saved`、持有的 digest 三者皆不變，且**不自動重新載入**。衝突與非衝突錯誤都**不得無條件把狀態轉為未儲存**——使用者可能在等待期間把內容改回等於 `saved`，此時錯誤照常顯示但 dirty 為假；任何路徑結束後一律重算 `dirty = buffer !== saved`。重新載入只在使用者明確要求、或在導覽守衛中選擇捨棄後才發生。

圖已依此契約更新（狀態機的 `saving`／`settled`／`conflict`、循序圖的送出快照 P 與封鎖區間）。

## 四、owner 裁定（第一輪 rev2 回寫、第二輪 rev3 回寫）

- **D1 通過**：新增 Spec「儲存目前內容」action、按鈕與翻譯文字，屬本票必要範圍。
- **D2 採 (b)**：使用明確的 sentinel 文字契約，配 Go 訊息契約測試與前端衝突／非衝突測試；未知錯誤保留原訊息。不改 backend API。
- **D3 納入**：切檔與切分頁都須保護未儲存內容；必要的 `App.vue` 修改屬本票範圍，但不重構整個分頁架構。
- **D4 採內容比較**：`dirty = buffer !== saved`；`saved` 的更新時機依第三節第 1 條。
- **D5 有條件接受**：`view.dispatch` 退路可用於證明「編輯器文件變更會回寫 buffer」，**不得**當成真實鍵盤與 Wails 儲存流程的完整證據（故 Gate A 另列人工驗收）。**rev6 實測結果（`/tmp/a1a-1/evidence/d5-spike.txt`）**：jsdom 下 `contenteditable` 節點存在，但派發 `beforeinput`＋`input`（`inputType: insertText`）後 `view.state.doc` **未改變**（仍為 `hello`）——CM6 依賴 DOM mutation 觀測，jsdom 的合成事件不會驅動它；`view.dispatch({changes})` 則正常生效。**故一律採 `view.dispatch` 退路**，並在報告標明不涵蓋真實鍵盤事件層。
- **D6 接受 N/N 全跑原則**，項數不預先固定（rev3 起）：第七節列的是**變異目標**，實際執行清單依 Spec／Plan 逐元件展開，於各子票實作前在該票的執行 checklist 中編號列出。（rev2 曾寫「15 項」，rev3 已撤回；歷史見修訂記錄。）
- **D7 通過**：保留 Spec local ref／Plan store，不做對稱化重構。
- **D8 暫留為提案**：算式正確；補入新增驗證工作後重估（見第七節），不要求維持原數字。
- **D9（rev2 提出，第二輪裁定：拆票通過，不接受超門檻單票例外）**：拆為 **A1a-1 編輯儲存完整流程** 與 **A1a-2 未儲存內容導覽保護**，後者依賴前者。**分攤修正**：寫入期間禁止再次儲存與切換屬寫入安全要求，其測試、變異與所需的 `App.vue` 攔截點一律留在 **A1a-1**；A1a-2 只負責非儲存期間的保留／捨棄導覽流程。A1a 為 aggregate，兩張子票都完成才關票；拆票不要求製造兩個 PR。工時見第八節，**本輪只核准拆分方向，未核定最終工時**。
- **D10（rev3 回寫，第二輪裁定）**：草稿語意不得統一——Plan 的 `applyDraft`／`confirmBump` 只更新 buffer；**Spec 的 `acceptDraft` 保留立即寫入**（既有測試明確要求「按接受才寫檔」），納入同一套送出快照與寫入互斥。操作結果恰等於 `saved` 時不得無條件標為未儲存。見第三節第 5、6 條。
- **D11（rev3 回寫，第二輪裁定）**：載入回應歸屬須用**請求世代**，只比路徑會讓 A→B→A 的舊回應通過；載入進行中暫停編輯。`confirmBump` 的等待期間同樣要處理反方向覆蓋，以「文件＋buffer 版本」檢查解決，不新增通用並行框架。見第三節第 4、7 條。

## 五、Phase 1｜BDD（詞彙與場景）

**詞彙表**（本票統一用語）：

| 詞 | 定義 |
|---|---|
| editor document | CodeMirror `EditorView` 內部的文字狀態，使用者輸入直接改變它 |
| 受控 buffer | 元件／store 內、**儲存動作實際送出**的字串（Spec `fileContent`、Plan `plan.currentContent`） |
| 已儲存快照 `saved` | 最近一次成功寫入所**送出**的內容；載入成功時等於磁碟內容。dirty 判定的比較對象 |
| 送出快照 P | 按下儲存當下凍結的 `{path, content, digest}`，寫入期間不變 |
| 持有的 digest | 讀檔時取得、寫檔時作為 `expectedDigest` 送出；只在寫入成功時更新為 `newDigest` |
| 磁碟內容 | 檔案在磁碟上的實際內容，只能經 `*Read`／`*Write` 存取 |

場景見 `docs/architecture/features/spec-plan-editing.feature`：**22 條 Scenario ＋ 2 條 Scenario Outline（各 6 例）＝ 34 個可執行案例**。涵蓋真實輸入回寫、Spec／Plan 各自的存後重載、改回原樣不誤報、儲存中繼續輸入、儲存中封鎖重疊與切換、延遲載入回應丟棄、**同檔案舊世代回應丟棄（A→B→A）**、**載入進行中暫停編輯**、衝突、非衝突錯誤、切檔捨棄、切檔保留（兩條獨立）、切分頁保護與**捨棄後導覽到正確目標**（各 Spec／Plan × 分頁按鈕／檔案樹選取／重新送核導向）、**Plan 草稿與 bump 只更新 buffer**、**Plan 結果等於 `saved` 時不算未儲存**、**Spec 接受草稿立即寫入／等待期間新輸入保留／失敗不更新快照**、**bump 的四條結束路徑（等待期間限制／續打使版本過期／文件識別被替換的防禦性驗證／後端錯誤保留原訊息）**。**「真實輸入」一律定義為使編輯器文件改變；注入 props 或直接呼叫 store action 不算。**

## 六、Phase 2｜DDD（責任劃分）

| 責任 | 承擔者 | 不得承擔 |
|---|---|---|
| 使用者輸入的即時狀態 | editor document（CM6） | 不作為儲存來源直接讀取；一律經 updateListener 落到受控 buffer |
| 儲存送出的內容 | 送出快照 P（按下儲存當下凍結） | 不在回應到達時重新取 buffer |
| dirty 判定 | 受控 buffer 與 `saved` 的內容比較 | 不用「有無輸入事件」判定 |
| 樂觀鎖 | 持有的 digest（讀檔取得、寫成功更新、衝突與其他錯誤時不動） | 前端不自行計算 digest；`specDigestOf` 是 Go 端權威 |
| 寫入原子性與衝突判定 | `app.go` 的 `specWrite`／`planWrite`（暫存檔＋rename、sentinel） | 前端不重試、不覆寫、不吞錯 |
| 衝突的辨識與呈現 | 元件（sentinel 文字契約，D2） | 不在此票做 reload／compare（A1b）；不自動覆蓋未儲存內容 |
| 載入回應的歸屬判定 | 元件持有的**請求世代**（遞增識別，只套用最新一次） | 不以路徑代替世代；不假設回應順序與請求順序一致 |
| 草稿與 bump 的落盤語意 | Plan：只更新 buffer（兩步慣例）；Spec：`acceptDraft` 立即寫入並納入送出快照 | 不把兩者統一為同一種語意 |
| `confirmBump` 回應是否可套用 | 元件（檢查文件與送出時凍結的 buffer 版本） | 不引入通用並行框架 |
| 未儲存內容的導覽保護 | 工作區元件＋`App.vue` 的攔截點 | 不重構分頁架構；選擇前不得卸載工作區元件 |

圖：`a1a-editor-buffer-state.mmd`（狀態機）、`a1a-seq-save.mmd`（循序圖，含等待期間輸入與衝突分支）。**圖與實作偏差須同 PR 修圖。**

## 七、Phase 3｜TDD（測試與 mutation acceptance table）

**逐元件原則（rev3）**：Spec 與 Plan 是兩份各自實作的元件，任何「通用」敘述都不足以證明兩邊都成立。下表的「元件」欄標明實際執行目標；`Spec／Plan` 表示兩邊各執行一次（可用參數化測試，但**驗收表列出的是實際執行項目**）。`*` 表示需使編輯器文件改變（依 D5 以 `view.dispatch` 為退路）。

| # | 元件 | 測試 | 對應場景 |
|---|---|---|---|
| T1* | Spec | 編輯器文件改變後 `fileContent` 等於編輯器內容、dirty 為真 | 真實輸入回寫 |
| T2* | Plan | 同上，對 `plan.currentContent` | 真實輸入回寫 |
| T3* | Plan | **有狀態讀寫替身**：輸入後儲存 →（**重載前**）斷言 `saved`＝送出內容、digest＝新值、dirty 為假 → 再重新載入並斷言 `EditorView` 文件等於儲存內容 | Plan 存後重載 |
| T4* | Spec | 同 T3（含重載前斷言），並斷言送出的不是 `extractGherkin(draft)` | Spec 存後重載 |
| T5* | Spec／Plan | 改回等於 `saved` → dirty 為假 | 改回原樣不誤報 |
| T6* | Spec／Plan | 儲存中續打為 B，回應成功後 `saved`＝A、dirty 仍為真、digest 為新值 | 儲存中繼續輸入 |
| T7* | Spec／Plan | 儲存中：再次儲存、Spec 接受草稿、切檔、切分頁皆被暫停；回應後恢復 | 儲存中封鎖 |
| T8 | Spec／Plan | 兩次載入回應亂序到達，過期世代整筆丟棄 | 延遲載入回應 |
| T8b | Spec／Plan | **A→B→A**：第一次 A 的延遲回應路徑相同但世代過期，仍須丟棄 | 同檔案舊世代 |
| T8c | Spec／Plan | 載入進行中編輯為暫停；回應後恢復且內容為該次載入結果 | 載入中暫停編輯 |
| T9 | Spec／Plan | 替身丟出衝突 sentinel 文字 → 判為衝突；buffer／`saved`／digest 不變；未觸發重新載入；**等待期間若把內容改回等於 `saved`，錯誤仍顯示但 dirty 為假** | digest 衝突 |
| T10 | Spec／Plan | 替身丟出其他錯誤 → 原訊息呈現、不判為衝突、三者不變；**dirty 依內容比較，不無條件為真** | 非衝突錯誤 |
| T11* | Spec／Plan | 切檔且 dirty → 出現選擇；選捨棄才載入新檔 | 切檔捨棄 |
| T12* | Spec／Plan | 切檔且 dirty → 選保留：停留原檔、新檔未載入（斷言替身未被呼叫） | 切檔保留 |
| T13a* | Spec／Plan × 3 入口 | 切分頁保護：選保留時停留原處，且**選擇前元件未被切走或卸載** | 切分頁保護（6 例） |
| T13b* | Spec／Plan × 3 入口 | **選捨棄後導覽成功到達該入口指定的目標**（防止「永遠拒絕離開」的實作假綠） | 捨棄後導覽（6 例） |
| T14a* | Plan | `applyDraft`／`confirmBump` 只更新 buffer：不呼叫寫入替身、`saved` 與 digest 不變、dirty 依內容比較 | Plan 草稿／bump |
| T14b* | Plan | 套用結果恰等於 `saved` → dirty 為假 | 結果等於 saved |
| T14c* | Spec | `acceptDraft` 仍立即呼叫寫入替身（接受前不呼叫）；成功後 `saved`＝草稿內容、digest 更新 | Spec 接受草稿 |
| T14d* | Spec | `acceptDraft` 等待期間續打 → 回應後 buffer 為續打內容、`saved` 為草稿內容、dirty 為真 | 等待期間新輸入 |
| T14e* | Spec | `acceptDraft` 失敗 → `saved` 與 digest 不變、buffer 保留接受後內容 | 接受失敗 |
| T14f* | Plan | `confirmBump` 等待期間：**可繼續打字**，但切檔／切分頁／其他替換內容的操作被阻止；未續打且版本相符時套用結果並解除封鎖 | bump 等待期間限制／路徑 (c) |
| T14g* | Plan | 版本過期兩子案：**(a) 以實際編輯器輸入**續打使 buffer 版本過期；**(b) 以測試注入**替換文件識別。兩者皆斷言回應不套用、續打的新內容保留、顯示「內容已變更，請重新預覽」而非後端原文、封鎖解除 | bump 路徑 (a)(b) |
| T14h | Plan | 後端回傳真正的錯誤（例如 token 過期）→ 原訊息原樣呈現，不被換成版本已變更的訊息；封鎖解除 | bump 路徑 (d) |
| T15 | Go | 訊息契約測試：`ErrSpecWriteConflict`／`ErrPlanWriteConflict` 的訊息含前端判別片語 | 跨語言 sentinel 契約 |

**Mutation acceptance table（§6.7，N/N 全跑，不抽樣）**

下表列的是**變異目標**；`Spec／Plan` 的項目在執行時各算一項獨立變異。**實際執行清單（逐項編號、含元件）於各子票實作前在該票的執行 checklist 中展開列出，本 plan 不預先固定總項數**（rev2 曾寫「15 項」，rev3 撤回）。

| 目標 | 元件 | 變異植入處 | 預期轉紅 |
|---|---|---|---|
| MU-input | Spec／Plan | 移除 updateListener 回寫 | T1／T2 |
| MU-send | Spec／Plan | 儲存改送 `saved`（Spec 版改送 `extractGherkin(draftText)`）而非 buffer | T3／T4 |
| MU-dirty | Spec／Plan | dirty 判定改為「有輸入即 true」 | T5 |
| MU-saved-miss | Spec／Plan | 儲存成功後不更新 `saved` | T3／T4 的重載前斷言 |
| MU-saved-late | Spec／Plan | 儲存成功後 `saved` 取回應當下的 buffer（而非送出快照） | T6 |
| MU-lock | Spec／Plan | 儲存期間不封鎖再次儲存與切換 | T7 |
| MU-gen-path | Spec／Plan | 載入回應只比路徑不比世代 | T8b |
| MU-gen-none | Spec／Plan | 載入回應完全不比對即寫入 | T8 |
| MU-load-edit | Spec／Plan | 載入進行中不暫停編輯 | T8c |
| MU-conflict-all | Spec／Plan | 衝突判別改為 catch-all | T10 |
| MU-conflict-digest | Spec／Plan | 衝突分支仍更新持有的 digest | T9 |
| MU-guard-file | Spec／Plan | 切檔守衛不檢查 dirty | T11 |
| MU-guard-tab | Spec 或 Plan（單邊植入） | 分頁守衛只實作在其中一邊 | T13a 缺的那三例 |
| MU-guard-unmount | Spec／Plan | 守衛在使用者選擇前就卸載工作區元件 | T13a 的未卸載斷言 |
| MU-guard-block | Spec／Plan | 選擇捨棄後不執行導覽（永遠停留） | T13b |
| MU-draft-plan | Plan | `applyDraft` 一併更新 `saved` | T14a |
| MU-draft-dirty | Plan | 套用後無條件設 dirty | T14b |
| MU-draft-spec | Spec | `acceptDraft` 改為只更新 buffer、不寫檔 | T14c |
| MU-draft-spec-late | Spec | `acceptDraft` 成功後 `saved` 取回應當下 buffer | T14d |
| MU-bump-ver | Plan | `confirmBump` 回應不檢查文件與 buffer 版本即覆蓋 | T14g |
| MU-bump-lock | Plan | `confirmBump` 等待期間不封鎖切檔／切分頁 | T14f |
| MU-bump-msg | Plan | 本機判定過期時顯示後端原文（或空訊息），而非「內容已變更，請重新預覽」 | T14g |
| MU-fail-dirty | Spec／Plan | 衝突或錯誤路徑無條件把狀態設為未儲存 | T9／T10 的 dirty 斷言 |

每項須留四格證據：**套用**（`git diff` 顯示變更且檔案 hash 改變）、**紅在正題**（表中指定的測試因其自身的斷言訊息而失敗，不是撞到其他前置檢查、也不是由其他測試連帶失敗）、**還原**（還原後與變異前 byte-identical）、**回綠**（該 task 基準指令回綠）。實作前先做 expected-red（新測試在未修的 production code 上為紅）。

## 八、Task 分解與估點（bottom-up，hr 為權威單位）

| Task | 內容 | hr（低–高） | 中位 | 歸屬 |
|---|---|---|---|---|
| T-1 | Spec 佈線：updateListener → `fileContent`、`saved`、dirty、儲存 action＋按鈕＋i18n key | 2.0–3.0 | 2.5 | A1a-1 |
| T-2 | Plan 佈線：updateListener → `plan.currentContent`、`saved`、dirty 正確化 | 1.0–1.5 | 1.25 | A1a-1 |
| T-3 | 衝突辨識契約（sentinel 文字常數＋Go 訊息契約測試） | 1.0–1.5 | 1.25 | A1a-1 |
| T-4 | 非同步契約：送出快照、寫入互斥（含 Spec `acceptDraft`）與其 `App.vue` 攔截點、載入請求世代與載入中暫停編輯、`confirmBump` 版本檢查、草稿語意分流 | 2.5–4.0 | 3.25 | A1a-1 |
| T-5 | 導覽守衛：非儲存期間的保留／捨棄流程、三個入口、選擇前不卸載、捨棄後導覽到正確目標（`App.vue` 攔截點的導覽部分） | 2.0–3.0 | 2.5 | A1a-2 |
| T-6 | 測試：T1–T15 逐元件展開（含 D5 可行性確認 0.5–1.0、有狀態讀寫替身）；其中導覽測試 T11–T13b 約 1.8 | 4.5–6.5 | 5.5 | 分攤 |
| T-7 | mutation N/N 執行（逐元件展開後約 30 項；其中導覽相關 4 個目標約 1.6） | 5.0–8.0 | 6.5 | 分攤 |
| T-8 | 人工 Wails 驗收：build＋實機 Spec／Plan 各一次輸入→儲存→重新開啟 | 1.0–1.5 | 1.25 | A1a-1 |
| T-9 | design gate 往返、圖與 feature 對齊、closure review | 1.25–2.0 | 1.6 | 分攤 |

**合計 25.6 hr → 2.56 pt**（rev1 14.5、rev2 21.6；rev3 增量來自草稿語意分流、請求世代與載入中暫停、`confirmBump` 版本檢查、捨棄後導覽 6 例、逐元件展開的測試與變異）。

**拆票（D9 通過，分攤依第二輪裁定修正；工時於 2026-09-07 第三輪 **核定**）**

| 票 | 範圍 | hr（核定） | pt（核定） |
|---|---|---|---|
| **A1a-1 編輯儲存完整流程** | T-1（2.5）、T-2（1.25）、T-3（1.25）、T-4（3.25）、T-8（1.25）；T-6 扣除導覽測試後 3.7；T-7 扣除導覽變異後 4.9；T-9 分攤 1.0。測試 T1–T10、T14a–T14h、T15；變異除 MU-guard-* 外全部（**含 MU-lock**） | 19.1 | 1.91 |
| **A1a-2 未儲存內容導覽保護** | T-5（2.5）；T-6 的導覽測試 1.8；T-7 的導覽變異 1.6；T-9 分攤 0.6。測試 T11、T12、T13a、T13b；變異 MU-guard-file／MU-guard-tab／MU-guard-unmount／MU-guard-block | 6.5 | 0.65 |

兩票相加 25.6 hr，與合計相符。**A1a-2 依賴 A1a-1**（守衛需要 dirty 與寫入互斥先成立）。A1a-1 的 1.91 pt 已接近 2.0 門檻，實作期間若範圍或估計再擴須立即回報並重新考慮拆分。等候（design gate 往返、CI）不計工時。**工時已於 2026-09-07 核定（A1a-1 19.1 hr／1.91 pt、A1a-2 6.5 hr／0.65 pt），backlog rev31 同批回填；估點是目前範圍的工作量預估，不是要求壓在數字內。** rev4 新增的 T14g／T14h 與三個變異目標屬既有 bump 契約與失敗路徑的細化，於 T-4／T-6／T-7 既有工時內吸收，核定值不變。

## 九、驗證策略

- **自動化**：前端全套 `npx vitest run`（本機基準 **40 檔／399 條**全綠，2026-09-07 實測；plan 舊文寫的 397 為 B2b 取樣期舊值）須全綠；`npm run build` exit 0；Go 訊息契約測試所屬套件綠。
- **辨識力**：mutation table N/N（第七節），另對需要真實輸入的測試先做 expected-red。**驗收表列出逐元件展開後的實際執行項目**，不以「通用」敘述或單邊執行代替兩個元件。
- **人工驗收（不可省略）**：實際建置並執行 Wails app，Spec 與 Plan 各做一次「輸入 → 儲存 → 重新開啟檔案確認內容」。可人工執行、不建整套 E2E 基建；**若無法執行則該項保留為未完成，不得以 jsdom 綠燈替代**。
- **未接線掃描**：對本票新增的每個匯出符號與新動作，機械掃描其 production 呼叫端；零命中即視為未接線，不得標記完成（這是 grep，不是判斷）。
- **邊界揭露**：jsdom＋`view.dispatch` 只能證明「編輯器文件變更會回寫 buffer 並被儲存送出」，不涵蓋真實鍵盤事件與 Wails runtime 的儲存流程；報告一律標明此邊界。

## 十、Gate（兩張子票各自驗收；A1a 原票須兩票皆完成才關）

### Gate A1a-1（編輯儲存完整流程）

- [x] 第三節八條契約各有對應測試：送出快照與 `saved` 更新（T3／T4 重載前斷言、T6）、寫入互斥（T7）、載入請求世代（T8、T8b）、載入中暫停編輯（T8c）、草稿語意分流（T14a–T14e）、`confirmBump` 等待期間限制與路徑 (c)（T14f）、版本過期兩子案（T14g）、後端錯誤原訊息（T14h）、衝突保留現場（T9）。
- [x] Spec 與 Plan **兩邊各自**的測試皆綠；驗收表列出實際執行項目（不以「通用」敘述代替）。
- [x] mutation：除 `MU-guard-*` 外的目標逐元件展開後 N/N 全跑，四格證據齊全；expected-red 先行。
- [x] 前端全套 `vitest` 與 `npm run build` 綠；Go 訊息契約測試綠。
- [x] **人工 Wails 驗收**：Spec 與 Plan 各一次「輸入 → 儲存 → 重新開啟」通過（owner 2026-09-07 實機確認；隔離工作區見 §11.4），未以 jsdom 綠燈替代。
- [x] Spec `acceptDraft` 的既有語意未被破壞：`SpecWorkspace.test.ts:47` 的「接受前不寫檔」仍綠。
- [x] 未接線掃描通過；圖與 feature 與實作一致。

### Gate A1a-2（未儲存內容導覽保護）

- [x] 切檔保留／捨棄兩條獨立情境在 Spec 與 Plan 各自成立（G1–G3 各元件；GUI 亦已驗證）。
- [x] 切分頁保護與**捨棄後導覽到正確目標**（G4／G5 各 6 例）全綠；三個入口皆不可繞過；選擇前元件未被切走或卸載。**GUI 僅驗證分頁按鈕與檔案樹兩個入口，重新送核入口 GUI 未涵蓋**（見 §12.4）。
- [x] mutation **24 項** N/N 全跑，四格證據齊全（原 8 項因守衛集中於 `App.vue` 合併為 6，另新增 18 項涵蓋 keep／precedence／dirty 發送端／重複確認／兩條繞過路徑的修法；見 §12.3）。
- [x] 前端全套 `vitest` **466/466** 與 `npm run build` exit 0；`go test ./...` 亦全綠。
- [x] **未接線掃描通過**：A1a-2 新增的導覽動作（`unsavedKeep`／`unsavedDiscard`／`guardedNav`／`isGoResubmitNoop`／`selectPreviewFile`）、`[data-test=unsaved-guard|unsaved-keep|unsaved-discard]` 按鈕與 `dirty` 事件，皆有正式 production 呼叫端（owner 2026-09-08 核對確認）。
- [x] 圖與 feature 與實作一致（狀態圖 rev5 已補 H5-P 的「同工作區換檔＋載入失敗保留 buffer／dirty」例外路徑與捨棄時的 busy 再檢查）。
- [x] **Wails GUI 證據與涵蓋限制裁定**（owner 2026-09-08 裁定，綁定 `85d6418939c11445827ea3014ceb2075b3aa972b`）：GUI 證據由 **agent 透過 osascript 操作**取得（**非人工操作、非 owner 確認**），逐案結果見 §12.4；**寫入進行中**與**重新送核導向**兩項 **GUI 未涵蓋**，由 G6／H1 系列與 G4／G5／H4-S／H5-P 承接；**H5-P 維持只有自動化證據**，不追加人工操作要求。

### A1a-1 剩餘揭露（不影響 Gate，但須留痕）

- **寫入期間的切檔請求是丟棄而非排隊重放**：契約寫「操作暫停、回應後恢復可用」，實作依此丟棄；重放會在解封後覆蓋剛套用成功的內容（T14f 會紅）。分頁按鈕、檔案樹與重新送核導向三個入口已由 A1a-1 的攔截點擋住，其餘導覽路徑歸 A1a-2 的守衛。
- **`view.dispatch` 退路的邊界**：jsdom 下 `beforeinput`／`input` 不會改變 CM6 文件（實測），故自動化測試證明的是「編輯器文件變更會回寫 buffer 並被儲存送出」，**不涵蓋真實鍵盤事件層**；該層由 §11.4 的人工驗收承接。
- **i18n key 用 `bump.staleVersion`** 而非 plan 原文的 `plan.bump.staleVersion`——repo 既有慣例是頂層 `bump:` namespace，依 conformance 優先。
- **順帶發現，未納入本票**：`wails build` 重新產生 bindings 時 `frontend/wailsjs/go/models.ts` 多出 `terminal_cause` 欄位，代表 committed 的產生檔相對 Go 端已過期（來源 `53c94f0`，B6 Task 6b，與 A1a-1 無關）。已還原、不夾帶。**owner 2026-09-07 裁定另立 bindings 同步小票（backlog A5）：先唯讀核對產生方式、完整差異與使用端影響，不直接授權更新產生檔。**
- **隔離模式 flaky 已登記**：`wall-clock-test-register.md` **v8** B-1 段登記為「CM6／jsdom 隔離執行候選」C1（`T8b-S`，現行 HEAD `2bd4890` 2/4 重現），明列不入可重跑名單、不併入 F1／F2、全檔通過只寫「未重現」、失敗原文待補。

### A1a 原票關票條件

- [x] Gate A1a-1（2026-09-07）與 Gate A1a-2（2026-09-08，綁定 `85d6418`）皆通過，兩票證據分別齊備 → **A1a aggregate 本機驗收完成**。push、開 PR、遠端 CI 仍未授權，正式關票待 owner 另行裁定。

## 十一、A1a-1 執行 checklist（rev6 新增；owner 2026-09-07 授權本機實作與驗證）

**起點**：`main`＝`3ea31ea`，分支 `a1a/editor-sync` HEAD `7860d75`，工作樹乾淨。**授權範圍**：前端修改、`App.vue` 的寫入／bump 等待期間攔截、Go 訊息契約測試、plan rev6、本機測試與 build、mutation 驗證、一次實機 Wails 人工驗收。**不含** A1a-2、push、開 PR、遠端 CI、任何設定變更。

### 11.1 expected-red 分類規則

每個測試在實作前先於未修改的 production code 上執行，並依實際結果如實歸類：

- **R（新增修正目標）**：必須**因該測試自己的目標斷言**而失敗。撞到其他 guard、setup 失敗或前置檢查失敗**不算**。
- **G（既有行為保全／契約釘字）**：本來就會通過是正常的，**如實記錄為綠，不得刻意製造失敗**。
- **M（混合）**：既有斷言綠、新增斷言紅；須指明哪一條斷言紅。
- **編譯失敗、模組解析失敗或測試環境未就緒一律不算行為證據**，須修正後重跑再記錄。

**執行結果（2026-09-07）**：**R=27、G=3、M=0、ENV=0**。G 三項為 T14c（Spec 接受草稿仍立即寫檔）、T14a（`confirmBump` 只更新 buffer）、T15（Go 訊息契約）——皆為既有行為保全，如實記綠。揭露：T3-P 初版為**假綠**（用套用草稿代替真實輸入、手餵 read 回傳值、未斷言重載後文件），複核後改用真實輸入＋有狀態讀寫替身＋重載後 `EditorView` 文件斷言，轉為誠實紅燈並計入 R。多數 R 屬「元件／屬性尚不存在」層級，鑑別力由第 11.3 節的變異證明，不以紅燈數量代替。

### 11.2 測試識別項目（30 項；13 Spec ＋ 16 Plan ＋ 1 Go）

若實作時發現某項需展開為多個子案例，**在本節分別列出後重算總數**，不為符合數字新增測試。

| # | 元件 | 測試 | 預期分類 |
|---|---|---|---|
| 1 | Spec | T1 輸入回寫 `fileContent`、dirty 為真 | R |
| 2 | Spec | T4 存後重載（含重載前 `saved`／digest／dirty 斷言、送出非草稿萃取） | R |
| 3 | Spec | T5-S 改回等於 `saved` → dirty 為假 | R |
| 4 | Spec | T6-S 儲存中續打，成功後 `saved`＝送出內容 | R |
| 5 | Spec | T7-S 儲存中封鎖再次儲存／接受草稿／切檔／切分頁 | R |
| 6 | Spec | T8-S 過期世代回應丟棄 | R |
| 7 | Spec | T8b-S A→B→A 舊世代丟棄 | R |
| 8 | Spec | T8c-S 載入中暫停編輯 | R |
| 9 | Spec | T9-S 衝突辨識、三者不變、不自動重載、dirty 依內容比較 | M |
| 10 | Spec | T10-S 非衝突錯誤原訊息、不判為衝突、dirty 依內容比較 | M |
| 11 | Spec | T14c `acceptDraft` 仍立即寫檔（接受前不寫） | **G** |
| 12 | Spec | T14d `acceptDraft` 等待期間續打→內容保留、`saved`＝草稿 | R |
| 13 | Spec | T14e `acceptDraft` 失敗→`saved`／digest 不變 | M |
| 14 | Plan | T2 輸入回寫 `plan.currentContent`、dirty 為真 | R |
| 15 | Plan | T3 存後重載（含重載前斷言） | R |
| 16 | Plan | T5-P 改回等於 `saved` → dirty 為假 | R |
| 17 | Plan | T6-P 儲存中續打，成功後 `saved`＝送出內容 | R |
| 18 | Plan | T7-P 儲存中封鎖 | R |
| 19 | Plan | T8-P 過期世代回應丟棄 | R |
| 20 | Plan | T8b-P A→B→A 舊世代丟棄 | R |
| 21 | Plan | T8c-P 載入中暫停編輯 | R |
| 22 | Plan | T9-P 衝突（同 #9） | M |
| 23 | Plan | T10-P 非衝突錯誤（同 #10） | M |
| 24 | Plan | T14a `applyDraft`／`confirmBump` 只更新 buffer、不寫檔 | M |
| 25 | Plan | T14b 套用結果等於 `saved` → dirty 為假 | R |
| 26 | Plan | T14f bump 等待期間可打字、阻止切換；版本相符則套用並解封 | R |
| 27 | Plan | T14g(a) 續打使 buffer 版本過期（**實際 `view.dispatch` 輸入**）→ 不套用、內容保留、專屬訊息、解封 | R |
| 28 | Plan | T14g(b) 文件識別被替換（**測試注入**）→ 同上處置 | R |
| 29 | Plan | T14h bump 後端真錯誤保留原訊息、解封 | M |
| 30 | Go | T15 `ErrSpecWriteConflict`／`ErrPlanWriteConflict` 訊息含前端判別片語 | **G** |

- [x] 30 項全部完成 expected-red 分類並留下輸出

### 11.3 Mutation 執行（**38 項，N/N 全跑完成**）

原列 31 項，依 owner 裁定如實展開為 **38**：`MU-load-edit` 拆為 `-editable`／`-listener` 兩層分別植入（＋2）；缺口修正後的新行為補上鑑別力——`MU-path-authority-S`、`MU-draft-busy-S`／`-P`、`MU-nav-resubmit`、`MU-nav-filetree`（＋5）。

**結果（2026-09-07，乾淨工作樹、無並行代理、單一批次）**：

- [x] 38/38 `RED_ON_TARGET`；套用後 sha256 皆改變；還原後皆 byte-identical；皆回綠；工作樹乾淨
- [x] 獨立複驗：**沒有任何目標失敗是環境造成的**——38 份紅燈日誌都含目標測試的失敗行與其斷言訊息，且無一是因 CM6 未掛載或逾時而中止。**更正（owner 2026-09-07 複核指出）**：這不等於「沒有環境錯誤訊息」——38 份中有 **36 份**仍含 `getClientRects is not a function` 的 jsdom 量測 stderr 雜訊（計數方法：對 `results.json` 列出的 38 個 id 逐一讀其 `<id>.red.log`；不含雜訊的兩份是 `MU-nav-resubmit`／`MU-nav-filetree`，它們測 `App.test.ts`、不掛載 CM6。先前回報的 35 是我用 `grep -lc` 組合誤計，已更正），該雜訊不是失敗原因；`results.json` 的 `msg` 欄位先前常誤取此雜訊而非目標斷言，**已離線修正摘要擷取邏輯（排除雜訊、優先取斷言行），原始日誌保留、mutation 未重跑**

**執行框架修正（過程揭露，影響先前結論）**：

1. **首輪 4 項 `NOT_RED` 皆為測試鑑別力不足或目標對不上**，非變異選錯：(a) T9-S／T9-P 未斷言「衝突後持有的 digest 不變」→ Plan 補 `usePlan().currentDigest` 直接斷言、Spec 無 store 改以「衝突後再儲存，送出的 `expectedDigest` 仍為原值」行為驗證；(b) `MU-draft-plan-P` 變異 `applyDraft` 卻指向測 `confirmBump` 的 T14a → 改指 T14b 並擴為「不同於 saved → dirty 真／等於 saved → dirty 假」兩段；(c) `MU-draft-busy-P`／`-S` 的函式 guard 與按鈕 `disabled` 是同一條要求的兩層縱深防禦，只移除一層行為不變 → 改為一次移除兩層；且 T7-P 原本在儲存中套用**內容相同**的草稿，無可觀察差異 → 改為套用不同內容。
2. **紅燈與回綠兩步一律跑整個測試檔**。`-t` 單條隔離在 jsdom 下對 CM6 掛載時序敏感（會出現「view 未掛載」或 5s 逾時），§6.7 的回綠本就該用該 task 的基準測試指令。
3. **分類器新增 `ENV_FAIL`**：目標測試雖失敗，但訊息為 CM6 未掛載或逾時者一律不計為紅在正題——測試在碰到與變異相關的斷言前就已中止，該紅是巧合。修正前曾有 9 項落入此情形被誤計，**先前「38 項全數紅在正題」的回報已收回並重跑**。
4. **並行汙染**：曾同時由主 agent 與子代理操作同一份變異表與工作樹，造成表被覆寫、兩次 `RESTORE_FAIL`（harness 安全機制正確擋下）。最終結果為終止子代理後、由主 agent 單獨執行的乾淨批次。

### 11.4 人工 Wails 驗收（資料隔離）——**通過**

- [x] 使用專用暫存工作區 `WORKBENCH_WORKSPACE=/tmp/a1a-1/wsfixture`（fixture：`spec/features/a1a-acceptance.feature`、`plan/a1a-acceptance.yaml`）。實際啟動確認 app 的全部狀態（`.workbench/` 下 `audit.jsonl`、`events.jsonl`、`sessions.json`、`instance.lock`、`recordings/`、`evidence/` 等）皆落在 fixture 目錄內；正式 `spec/`／`plan/`、核可紀錄與既有使用者資料未被觸及，正式 repo 工作樹保持乾淨。
- [x] `wails build` exit 0（6m12s）。
- [x] Spec 一次「輸入 → 儲存 → 重新開啟」通過
- [x] Plan 一次「輸入 → 儲存 → 重新開啟」通過
- **證據歸屬（owner 2026-09-07 要求補齊）**：
  - **實際操作者**：owner 本人（agent 無法執行 GUI 互動，僅備妥隔離環境、建置與啟動驗證）。
  - **原始確認紀錄**：2026-09-07 16:17 owner 回覆「已測試，有正常顯示」；agent 因該敘述可能只涵蓋畫面渲染而追問確認範圍，owner 於 16:2x 明確選擇「**兩邊完整流程都通過**」（Spec 與 Plan 各一次「打字 → 儲存 → 切走再切回 → 內容就是打的那份」）。
  - **受測版本**：`build/bin/sdlc-workbench.app`，2026-09-07 16:12 由 `wails build` 產生（耗時 6m12s），當時 HEAD＝`af0cae2`。**受測二進位＝`af0cae2` 原始碼 ＋ 建置時 wails 自動再生的 `frontend/wailsjs/go/models.ts`**（該再生只為 `GateEntryDTO` 增加 `terminal_cause` 欄位，與 A1a-1 無關；再生檔已於建置後還原，未進 commit）。其後的 `2bd4890` 僅修改本 plan 文件，不影響受測程式碼。
  - 此格以 owner 的明確確認為準，**未以 jsdom 結果替代**。

### 11.4b A1a-1 證據封存（rev9）

- 證據根目錄 `/tmp/a1a-1/`（`evidence/`：D5 spike、build 輸出、wails build、app 啟動、隔離模式重現紀錄；`mutations/`：`table.json`、`results.json`、38 組 `<id>.red.log`／`<id>.green.log`、harness `run.py`）。
- **manifest**：`/tmp/a1a-1/EVIDENCE.sha256`，**120 個檔案**，`shasum -a 256 -c` **120/120 OK**；manifest 自身 SHA-256 `00806709edd5205faa37a3de18266a11d7bed629f0679e4aa1568ad1078924f1`。
- 封存時點：A1a-2 開工前（HEAD `a086694` ＋ 本 rev）。A1a-2 的證據另行分開存放，不混入本 manifest。

### 11.5 失敗處置

測試失敗一律**先分類**再處置：本票新行為缺陷／既有行為破壞／環境問題／`wall-clock-test-register.md` 已登記的既有不穩定測試（依該文件規則處理）。**不得以反覆執行取得綠燈代替判定**；契約回歸不得重跑吸收。

**本票實測到的分類（2026-09-07）**：新增的 CM6 相關測試在 **`-t` 單條隔離模式**下對 jsdom 的 CM6 掛載時序敏感（`getView()` 會 fail loud 為「環境前置條件失敗，不是行為證據」，或 5s 逾時）；在**全檔／全套模式**下：實測 `SpecWorkspace`＋`PlanWorkspace` 全檔連跑 3 次皆 55/55、`PlanWorkspace` 單檔 3 次皆 37/37、全套 3 次皆 429/429——**這些批次未重現**（不得寫成「穩定」或「已證明不會發生」）。專案基準執行方式為全套，故未改測試。**已依 register 規則登記**：`wall-clock-test-register.md` v8 B-1 段候選 **C1**（`T8b-S`），失敗原文待補前維持「候選（待處置）」。

### 11.6 執行順序

- [x] (1) 本節 checklist 落地（rev6）
- [x] (2) expected-red：先寫測試並記錄分類（R=27／G=3）
- [x] (3) 分段實作 T-1 → T-2 → T-3 → T-4，每段跑全套
- [x] (4) mutation **38** 項 N/N
- [x] (5) 人工 Wails 驗收（owner 2026-09-07 確認）
- [x] (6) 回報 diff、測試／變異證據與人工驗收結果，等 owner 裁定是否進 A1a-2

**範圍或估計超出核定（19.1 hr／1.91 pt）時先回報，不自行擴張。**


## 十二、A1a-2 執行紀錄（rev10 新增）

**起點**：`6d27bab`（A1a-1 完成後）。**受測原始碼 HEAD**：`f719d43`（其後僅有文件變更）。owner 於 2026-09-07 核准本機實作與驗證，核定 6.5 hr／0.65 pt。

### 12.1 expected-red（26 條）

**R=18、G=8、M=0、ENV=0**。G 的 8 條全是 G6 系列——A1a-1 的 busy 直接拒絕先於 dirty 守衛生效，故在實作前本來就通過；依「不得以元素不存在充當紅燈」的判準如實記 G。owner 另指出 G6 的 App 層四條原本只設 `busy` 未設 `dirty`，無法證明先後順序，已補上 `dirty` 訊號（先送 dirty 再送 busy）。

**測試總數**：429（既有）＋26（G1–G6）＋11（G7-S／G7-P／G8-S 與 H 系列）＝ **466**。

### 12.2 owner 複核發現的兩條繞過路徑（已修，`fe0d4b0`）

1. **確認框開啟後才開始寫入，仍可按捨棄離開**：確認框不阻止使用者按儲存／接受草稿／確認 bump，`unsavedDiscard()`（App 與兩個元件）未重新檢查 busy。修法：執行前重新檢查，且在確定離開前不改路徑、不動 buffer、不清 dirty；`pendingNav`／`pendingPath` 保留，寫入結束後可再選一次。
2. **點目前分頁再選捨棄會錯誤清除 App 的 dirty**：`switchTab()` 未排除「目標即目前分頁」，而捨棄時假設必然卸載就把 `workspaceDirty` 設 false。修法：`guardedNav` 增加 `isNoop` 判定（同分頁／同預覽檔／重新送核目標即現況），並移除捨棄時的自行清除，改由「切到非 spec／plan 分頁」與工作區的 immediate emit 維持。

回歸測試：H1-S／H1-P（save）、**H1-S-accept**／**H1-P-bump**（另外兩種等待狀態，皆用實際延遲的 binding 回應）、H2-S（App 父層契約，以 emit 模擬 busy，**不宣稱涵蓋後端等待**）、H3-S、H4-S、**H5-P**（gate2 同分頁換檔且新檔載入失敗時 App 仍須保留保護）。

### 12.3 Mutation（24 項，N/N）

24 個唯一 ID 全部 **RED_ON_TARGET**、套用後 hash 改變、備份還原 byte-identical、回綠。**沒有任何目標失敗由環境前置條件造成**；24 份紅燈日誌中 **14 份**仍含 `getClientRects` 的 jsdom 量測 stderr 雜訊（非失敗原因）。

**harness v2 的還原機制**（owner 要求）：植入前保存原始位元組、從備份還原並比對，**不再用 `git checkout`**（那會覆寫未提交內容，`RESTORE_FAIL` 只是事後偵測）；開始前鎖定 HEAD 與乾淨工作樹，異常退出也還原並保留備份。舊版保留為 `run.v1.py`。批次來源見 `mutations/BATCHES.md`，合併索引 `results-merged.json` 中 23 項的 hash 標為**離線重建**（`results.json` 曾被單項重跑覆蓋，副本存 `results.rerun5-only.json`）。

### 12.4 agent 執行的 Wails GUI 驗證（**不是人工操作、不是 owner 確認**）

由 agent 以 `screencapture` 判讀畫面、`osascript`／System Events 送點擊與鍵盤完成，29 張截圖對應操作順序。**受測原始碼 HEAD**：`f719d43`；`wails build` 產物 SHA-256 `46e2adc5e9ee521928cc15fa54f4892495220ae47efef9378d10fa749e4e63a7`；建置期再生的 `models.ts`（兩行 `terminal_cause`）diff 已保存並於驗收後還原。隔離工作區 `WORKBENCH_WORKSPACE=/tmp/a1a-2/wsfixture`，app 內顯示 `ws: env @ /private/tmp/a1a-2/wsfixture`，正式 repo 驗收後工作樹乾淨、HEAD 不變。

**通過**：Spec 清單切檔的守衛出現／保留／**再次觸發後**捨棄；Spec 未儲存時切分頁的守衛與捨棄後完成導覽；**點目前分頁不設守衛且之後離開仍受保護**；Plan 清單切檔的守衛／保留／再次觸發／捨棄；**檔案樹入口**的守衛；儲存確實寫入 fixture 檔案。

**歸因限定（owner 2026-09-07 更正）**：上述「點目前分頁後仍保留保護」證明的是**同分頁操作不誤清 dirty** 這條路徑，**並未重現 H5-P 的「gate2 同分頁換檔後新檔載入失敗」情境**——H5-P 仍只有自動化證據。

**GUI 未涵蓋（owner 裁定不追加，由自動化證據承接）**：
- **寫入進行中的行為（案例 8／9）**：本機寫檔在毫秒內完成，無法確認點擊發生於 busy 期間，單張結果圖也無法證明；不為抓時序修改受測程式。由 G6 八條與 H1／H1-accept／H1-bump／H2 系列承接。
- **重新送核導向入口**：fixture 無核可紀錄，無法在隔離環境觸發；不另造核可流程。由 G4／G5 的 goresubmit 例、H4-S、H5-P 承接。
- 因此**不宣稱三個導覽入口均經 GUI 驗證**——分頁按鈕與檔案樹已驗，重新送核未驗。

### 12.5 證據封存

`/tmp/a1a-2/EVIDENCE.sha256`：**143 檔、143/143 OK**，SHA-256 `5b868a68b89a5ede19cd5c42e88cc22d54af7e69faf287a9b65dffc721984926`。涵蓋 harness v1／v2、變異表與合併索引、24 組紅綠日誌與 24 份備份、`vitest-final.txt`（466/466 原始輸出）、build 與 `go test` 輸出、29 張 GUI 截圖、執行檔 hash 與 `models.ts` 再生 diff。A1a-1 的封存（120 檔）另存，不混用。


## 十三、證據封存遺失紀錄（rev13 新增；**不覆寫歷史裁定**）

**發現時間**：2026-09-08 約 12:2x，執行 register C1「僅從既有紀錄補失敗原文」時，`ls /tmp/a1a-1` 與 `/tmp/a1a-2` 皆回 `No such file or directory`。

**遺失範圍**：`/tmp/a1a-1/`（120 檔）與 `/tmp/a1a-2/`（143 檔）**整個目錄消失**，含 mutation 紅綠日誌、24＋24 份原始位元組備份、29 張 GUI 截圖、build／`go test`／vitest 原始輸出、D5 spike、harness `run.py`／`run.v1.py`、變異表與合併索引、GUI fixture 工作區。已確認 `/private/tmp`、`~/Library/Caches` 與 repo 內皆無副本。**清除時點與原因未知**——目錄時間戳不能證明清除時間或成因。

**歷史事實保留（不重新驗證、不覆寫）**：
- A1a-1 manifest `EVIDENCE.sha256` 120 檔、當時 `shasum -c` **120/120 OK**，manifest 自身 SHA-256 `00806709edd5205faa37a3de18266a11d7bed629f0679e4aa1568ad1078924f1`。
- A1a-2 manifest 143 檔、當時 **143/143 OK**，manifest 自身 SHA-256 `5b868a68b89a5ede19cd5c42e88cc22d54af7e69faf287a9b65dffc721984926`。
- **上述完整性由當時的驗證紀錄支持；manifest hash 本身不能證明檔案曾存在或完整。**
- **Gate 裁定維持**：A1a-1 Gate 通過（2026-09-07）、A1a-2 Gate 通過（2026-09-08，綁定 `85d6418`）、A1a aggregate 本機驗收完成。裁定作成於證據可調閱時、owner 已逐項核對，事後遺失不改變當時的判定。

**現況**：**原封存已遺失，目前無法調閱複查**。任何人要複查只能重跑重建，且 **GUI 截圖與當時的執行環境無法重建**。

**回收狀態（部分回收，非原封存恢復）**：`~/.local/share/sdlc-evidence/a1a-1/2bd4890/recovered-from-session/` 與 `a1a-2/f719d43/recovered-from-session/`，共 6 檔——隔離模式重現量測（僅計數與指令，**無失敗原文**）、mutation 目標斷言表（斷言為節錄）、GUI 逐案結果（截圖無法重建）、各自的 `PROVENANCE.md`。來源是 session 轉錄的輸出片段，**不得作為原始輸出、不得用於重建 manifest、不得宣稱與原封存等價**。

**往後的保存規約（owner 2026-09-08 裁定）**：證據改存 `~/.local/share/sdlc-evidence/<ticket>/<受測SHA>/<批次>/` 並納入備份；`/tmp` **僅作暫存**；**複製到持久位置並重新驗證 manifest 之後**，才可在文件回填「封存完成」。


## 修訂記錄

- rev13（2026-09-08）：證據封存遺失紀錄——新增第十三節。`/tmp/a1a-1`／`/tmp/a1a-2` 兩份封存（120／143 檔）於 2026-09-08 約 12:2x 發現整個消失，`/private/tmp`、`~/Library/Caches` 與 repo 內皆無副本，**清除時點與原因未知**（目錄時間戳不能證明）。保留歷史裁定與 manifest hash，並明確記載「該完整性由**當時的驗證紀錄**支持，hash 本身不能證明檔案曾存在或完整」；現況為**原封存已遺失、目前無法調閱複查**。部分回收 6 檔至 `~/.local/share/sdlc-evidence/`（僅 session 轉錄片段，非原始輸出，含截斷說明）。往後保存規約改為持久路徑＋複製後重新驗證 manifest 才回填「封存完成」。Gate、mutation 與 GUI 結論一律不變、未重跑。
- rev12（2026-09-08）：A1a-2 Gate 通過回填——owner 裁定綁定 `85d6418939c11445827ea3014ceb2075b3aa972b`，勾選最後一格「Wails GUI 證據與涵蓋限制裁定」並保留操作歸屬（agent 透過 osascript 執行，非人工操作、非 owner 確認）與兩項 GUI 未涵蓋（寫入進行中、重新送核導向），H5-P 維持只有自動化證據；A1a 原票關票條件勾選為 **aggregate 本機驗收完成**，正式關票與推送待另行裁定。測試、mutation 與 GUI 結論不變，本 rev 未重跑任何驗證。
- rev11（2026-09-08）：owner 複核 `ab67304` 後的文件補正，測試與 GUI 結論不重開——(1) 狀態圖升 rev5：原本把捨棄畫成必然 `guard → empty`，未表達 H5-P 已推翻的假設，補上 `guard → loading`（同工作區換檔、元件不卸載）與 `loading → recompute`（載入**失敗**時 buffer／saved／digest 皆不變、dirty 依內容比較保留），另補 `guard → guard`（捨棄當下仍在寫入則拒絕離開、守衛保留）與「無效導覽不進 guard」；(2) Gate 最後一格改稱「**Wails GUI 證據與涵蓋限制裁定**」，不新增人工操作要求，保留 agent 操作歸屬與兩項已接受的 GUI 未涵蓋；(3) 恢復被刪的「**未接線掃描**」一格並記錄 owner 已核對新增導覽動作、按鈕與 dirty 事件皆有正式呼叫端；(4) §12 的 `f719d43` 正名為**受測原始碼 HEAD**。
- rev10（2026-09-08）：A1a-2 執行紀錄回填——新增第十二節：12.1 expected-red（26 條，R18／G8，含 G6 補 dirty 訊號的修正）；12.2 owner 複核發現的兩條繞過路徑（捨棄前未重檢 busy、無效導覽誤清 dirty）與修法及回歸測試（含 accept／bump 兩種等待狀態與 H5-P）；12.3 mutation 24 項 N/N 與 harness v2 的位元組備份還原、批次索引與離線重建 hash 的標示；12.4 **agent 執行的 Wails GUI 驗證**（非人工操作、非 owner 確認）逐案結果、歸因限定（案例 6 未重現 H5-P 情境）與兩項 GUI 未涵蓋；12.5 證據封存 143 檔。Gate A1a-2 逐項回填，人工驗收一格維持未完成，**Gate 未判定通過**。
- rev9（2026-09-07）：owner 複核後的紀錄修正與封存——(1) `getClientRects` 雜訊計數更正為 **36/38**（計數方法：對 `results.json` 的 38 個 id 逐一讀其 red.log；不含的兩份為測 `App.test.ts` 的 `MU-nav-resubmit`／`MU-nav-filetree`。先前的 35 是 `grep -lc` 組合誤計）；(2) §11.5 不再寫「穩定」與「待裁」，改為「這些批次未重現」＋「已登記 register v8 候選 C1」；(3) register C1 的「疑與機器負載相關」改為**原因未確認**（推測不得寫成已確認），並註明 B2b-2 回填採當時最新版本號、不覆蓋或除名 C1；(4) 新增 §11.4b 證據封存（`/tmp/a1a-1/EVIDENCE.sha256`，120 檔全 OK）。Gate 結論與估點不變。
- rev8（2026-09-07）：owner 複核後的修正與補齊——(1) §11.3 措辭更正：先前寫「38 份紅燈日誌零份殘留環境失敗訊息」不精確，正確陳述是「沒有任何目標失敗是環境造成的」，38 份中 35 份仍含 `getClientRects` 的 jsdom 量測 stderr 雜訊，`results.json` 的 `msg` 欄位曾誤取該雜訊；摘要擷取邏輯已離線修正，原始日誌保留、**mutation 未重跑**（owner 明示不必重跑）。(2) §11.4 補齊人工驗收的證據歸屬：實際操作者為 owner、原始確認紀錄（16:17 訊息＋追問後的明確確認）、受測版本（`af0cae2` ＋ 建置時自動再生的 `models.ts`，再生檔已還原未進 commit）。(3) 剩餘揭露補記 register v8 的候選登記與 backlog A5 bindings 同步小票。設計、估點與 Gate 結論不變。
- rev7（2026-09-07）：A1a-1 執行完成回填——§11.1 expected-red 結果（R=27／G=3／M=0／ENV=0，含 T3-P 假綠的揭露與修正）；§11.3 mutation 由 31 如實展開為 **38 項並全部 N/N 紅在正題、回綠、還原 byte-identical**，並揭露執行框架的三處修正（回綠與紅燈改全檔模式、新增 `ENV_FAIL` 使環境失敗不計為紅在正題、先前「38 項全數紅在正題」的回報已收回重跑）與並行代理汙染事故；§11.4 人工 Wails 驗收通過（owner 實機確認，隔離工作區證據）；§11.5 補實測的隔離模式 flaky 分類與 register 登記待裁；執行順序全部勾選；Gate A1a-1 全部成立；新增「A1a-1 剩餘揭露」四點。設計與估點不變。
- rev6（2026-09-07）：實作啟動回填，設計不變——狀態改為 design gate APPROVED（綁定 `7860d75`）／A1a-1 實作進行中；新增第十一節執行 checklist（expected-red 分類規則、30 個測試識別項目與預期分類、31 項變異、人工驗收資料隔離、失敗分類、執行順序）；**測試計數更正 33 → 30**（13 Spec ＋ 16 Plan ＋ 1 Go；不為湊數新增測試）；**前端基準更正 397 → 399 條**（本機實測 40 檔／399 全綠）；**D5 可行性實測回填**——jsdom 下派發 `beforeinput`＋`input` 後 CM6 `doc` 未改變，`view.dispatch` 正常，故採退路並標明不涵蓋真實鍵盤事件層（證據 `/tmp/a1a-1/evidence/d5-spike.txt`）。
- rev5（2026-09-07）：窄複核修正——契約第 7 條改為四條結束路徑。原文把「buffer 版本過期」與「文件識別被替換」一併歸為「正常 UI 已擋住、只以注入驗證」，但**等待期間允許打字，續打本身就會使 buffer 版本過期**，該狀態正常可達：(a) 續打造成的版本過期改以**實際編輯器輸入**驗證且續打內容須保留；(b) 只有文件識別被替換才用測試注入；(c) 未續打且版本相符則套用；(d) 真正的後端錯誤保留原訊息；四條路徑結束時一律解除操作封鎖並重算 dirty。feature 對應改為四條場景（33 → **34 個可執行案例**），沿用 T14f／T14g／T14h，未新增測試編號、未改架構、未重估。同批同步引用：A1a-1 分攤表 T14a–T14f → **T14a–T14h**、Gate A1a-1 的版本檢查由 T14f 改指 **T14g** 並納入 T14h、backlog 的 plan 引用改為 rev5。
- rev4（2026-09-07）：窄幅一致性修正，設計選項不重開——(1) `confirmBump` 分為正常操作（可續打、阻止切檔／切分頁與衝突操作）與防禦性驗證（送出版本已過期只以測試注入驗證，不描述成 UI 可繞過），並規定本機判定過期時顯示「內容已變更，請重新預覽」、真正的後端錯誤保留原訊息（T14f／T14g／T14h、MU-bump-lock／MU-bump-ver／MU-bump-msg）；(2) 兩張圖改正：載入循序圖以 `alt` 區分過期丟棄與最新才更新（丟棄後不再順序畫更新），狀態圖補「Plan 套用結果等於 `saved` 留在 clean」與「Spec 自 clean 接受草稿進入寫入流程」，衝突與失敗改經 `recompute` 依內容比較決定 dirty（新增 MU-fail-dirty 與 T9／T10 的 dirty 斷言）；(3) D6 撤除「15 項」舊數字改指逐元件展開的實際清單；核定估點回填（A1a-1 19.1 hr／1.91 pt、A1a-2 6.5 hr／0.65 pt），backlog rev31 同批更新 A 軌 41.6 hr／4.16 pt、合計 220.65 hr／22.07 pt。場景 31 → **33 個可執行案例**。
- rev3（2026-09-07）：design gate 第二輪 CHANGES_REQUIRED 後修訂——(1) 草稿語意分流：Plan `applyDraft`／`confirmBump` 只更新 buffer 且結果等於 `saved` 時不標未儲存，**Spec `acceptDraft` 保留立即寫入**並納入送出快照與寫入互斥，明定接受時的 buffer 替換與等待期間新輸入的保留（feature、T14a–T14e、MU-draft-* 同步）；(2) 載入回應歸屬改為請求世代並補 A→B→A 案例與載入中暫停編輯，`confirmBump` 以「文件＋buffer 版本」檢查（第三節第 4、7 條；T8b／T8c／T14f、MU-gen-*／MU-load-edit／MU-bump-ver）；(3) T3／T4 補**重載前**的 `saved`／digest／dirty 斷言（避免重載重設 `saved` 掩蓋 MU-saved-miss）、切分頁補捨棄後成功導覽（T13b、MU-guard-block）、測試與變異表加「元件」欄並撤回「15 項」的預先固定（實際清單於各票執行 checklist 展開）；(4) D9 拆票通過並修正分攤——寫入期間封鎖（含其 `App.vue` 攔截點）與 MU-lock 留在 A1a-1，A1a-2 只負責非儲存期間導覽；新增 D10／D11 記錄第二輪裁定；場景 18 → **31 個可執行案例**；重估 21.6 → **25.6 hr**（A1a-1 19.1／1.91、A1a-2 6.5／0.65）；Gate 改為兩張子票各自驗收。
- rev2（2026-09-07）：design gate 第一輪 CHANGES_REQUIRED 後修訂——D1–D8 裁定回寫並新增 D9（拆票）；新增第三節非同步儲存與載入契約六條；場景改寫（12 Scenario＋1 Outline／6 例＝18 案例；切檔保留／捨棄拆為獨立情境；新增儲存中繼續輸入、儲存中封鎖、延遲載入回應、切分頁保護 Spec／Plan × 三入口、草稿與 bump 同步）；T3／T4 改用有狀態讀寫替身並斷言重載後的 `EditorView` 文件；mutation 9 → 15 項；Gate A 納入人工 Wails 驗收與「未執行即未完成」；速記（未接線掃描、紅在正題）展開為正式說明；兩張圖依契約更新；重估 14.5 → 21.6 hr（2.16 pt）並提出拆票。事實核對第 9 點更正為「五個寫入操作」並補記 commit `7789f40` 已載明的 Spec 同型檢視與 escalation 確認。
- rev1（2026-09-07）：建立。事實於 HEAD `3ea31ea` 重新核對；BDD 場景；DDD 責任表與兩張 mermaid 圖；TDD 測試與 mutation table；Task 分解與 bottom-up 估點；D1–D8 待裁定。（rev1 的場景條數敘述有誤——實際為 8 條，rev2 已改寫並更正。）
