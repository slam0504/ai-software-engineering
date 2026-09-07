# A1a Spec／Plan 編輯器同步閉環 Implementation Plan

> **For agentic workers:** 本票為前端佈線與測試，無外部寫入。實作前須通過 owner design gate；push、開 PR、CI 另案授權。Steps use checkbox (`- [ ]`) syntax for tracking.
> 版本：rev2（2026-09-07，**design gate 第一輪 CHANGES_REQUIRED 後修訂**：D1–D8 裁定回寫；新增第三節「非同步儲存與載入契約」（送出快照、等待期間輸入、封鎖重疊、延遲載入回應、草稿／bump 同步、衝突後不自動重載）；場景表改寫並更正條數（12 條 Scenario＋1 條 Scenario Outline／6 例＝18 個可執行案例）；切檔的保留／捨棄拆為兩條獨立情境；新增切分頁保護（Spec／Plan × 三個入口）；T3／T4 改用有狀態讀寫替身並斷言重載後的 `EditorView` 文件；mutation 由 9 項增為 15 項；Gate A 納入 Spec／Plan 各一次實際 Wails 人工驗收；速記展開為正式說明；重估後 **21.6 hr／2.16 pt 超過拆票門檻，提出 A1a-1／A1a-2 拆分（D9）**。**待 owner 複核**）；前版：rev1（2026-09-07，建立）
> 狀態：**待 owner 複核 rev2**。尚未撰寫任何 production 或測試程式碼；未 push、未開 PR。
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
5. **不重構分頁架構**：D3 允許為了保護未儲存內容而修改 `App.vue`，但限於加入攔截點，不改寫既有 `v-if` 分頁結構的整體設計。
6. mutation acceptance table 依 §6.7 **N/N 全跑**，不抽樣；變異只植入本票新增或修改的 production code。
7. 前端全套 `vitest` 與 `npm run build` 必須綠；Go 端訊息契約測試所屬套件須綠。

## 三、非同步儲存與載入契約（rev2 新增，D1–D8 裁定後的強制契約）

以下六條為驗收契約的一部分，實作與測試都必須落到：

1. **送出快照**：按下儲存時凍結該次的 `{path, content, digest}` 為送出快照 P，之後的輸入不改變 P。寫入成功後，**已儲存快照 `saved` 更新為 `P.content`**，而不是回應到達當下的 buffer。持有的 digest 更新為回應回傳的 `newDigest`。
2. **等待期間允許輸入**：寫入進行中使用者可繼續編輯；回應後 `dirty` 依 `buffer ≠ saved` 重新計算，因此等待期間輸入的新內容仍為未儲存。
3. **同一文件寫入不可重疊**：寫入進行中，再次儲存、切檔、切分頁，以及套用草稿／確認 bump 等會替換編輯器內容的操作一律暫停，回應後恢復。
4. **延遲載入回應不得覆蓋後來選取的檔案**：每次載入帶請求識別（或比對目前選取路徑），回應到達時若已不屬於目前選取的檔案，**整筆丟棄**——不寫 buffer、不寫 `saved`、不寫 digest、不動編輯器。
5. **草稿套用與 bump**：`applyDraft`／`acceptDraft`／`confirmBump` 更新編輯器內容與受控 buffer，並使該檔為未儲存；**不更新 `saved` 與持有的 digest**（只有寫入成功才更新）。三者的業務語意（萃取規則、bump 預覽與確認流程）一律不變。
6. **衝突後保留現場**：辨識為 digest 衝突時，buffer、`saved`、持有的 digest 三者皆不變，且**不自動重新載入**。重新載入只在使用者明確要求、或在切檔守衛中選擇捨棄後才發生。

圖已依此契約更新（狀態機的 `saving`／`settled`／`conflict`、循序圖的送出快照 P 與封鎖區間）。

## 四、owner 裁定（design gate 第一輪，rev2 回寫）

- **D1 通過**：新增 Spec「儲存目前內容」action、按鈕與翻譯文字，屬本票必要範圍。
- **D2 採 (b)**：使用明確的 sentinel 文字契約，配 Go 訊息契約測試與前端衝突／非衝突測試；未知錯誤保留原訊息。不改 backend API。
- **D3 納入**：切檔與切分頁都須保護未儲存內容；必要的 `App.vue` 修改屬本票範圍，但不重構整個分頁架構。
- **D4 採內容比較**：`dirty = buffer !== saved`；`saved` 的更新時機依第三節第 1 條。
- **D5 有條件接受**：`view.dispatch` 退路可用於證明「編輯器文件變更會回寫 buffer」，**不得**當成真實鍵盤與 Wails 儲存流程的完整證據（故 Gate A 另列人工驗收）。
- **D6 接受 N/N 全跑原則**，項數暫不核定：先補齊本 rev 的測試，再同步 mutation table（本 rev 已由 9 項擴為 15 項，待複核）。
- **D7 通過**：保留 Spec local ref／Plan store，不做對稱化重構。
- **D8 暫留為提案**：算式正確；補入新增驗證工作後重估（見第七節），不要求維持原數字。
- **D9（rev2 新增，待裁定）**：重估後合計 **21.6 hr／2.16 pt**，超過「>2.0 pt 必拆」門檻且範圍性質混合（佈線閉環 vs 導覽保護）。建議拆為 **A1a-1 編輯儲存閉環**（15.7 hr／1.57 pt）與 **A1a-2 未儲存內容導覽保護**（5.9 hr／0.59 pt；兩票相加 21.6 hr，與合計相符），兩票各自 Gate。若裁定不拆，本票即為 2.16 pt 的單票，需明示接受超門檻。

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

場景見 `docs/architecture/features/spec-plan-editing.feature`：**12 條 Scenario ＋ 1 條 Scenario Outline（6 例）＝ 18 個可執行案例**。涵蓋真實輸入回寫、Spec／Plan 各自的存後重載、改回原樣不誤報、儲存中繼續輸入、儲存中封鎖重疊與切換、延遲載入回應丟棄、衝突、非衝突錯誤、切檔捨棄、切檔保留（兩條獨立）、切分頁保護（Spec／Plan × 分頁按鈕／檔案樹選取／重新送核導向）、草稿與 bump 後三者同步。**「真實輸入」一律定義為使編輯器文件改變；注入 props 或直接呼叫 store action 不算。**

## 六、Phase 2｜DDD（責任劃分）

| 責任 | 承擔者 | 不得承擔 |
|---|---|---|
| 使用者輸入的即時狀態 | editor document（CM6） | 不作為儲存來源直接讀取；一律經 updateListener 落到受控 buffer |
| 儲存送出的內容 | 送出快照 P（按下儲存當下凍結） | 不在回應到達時重新取 buffer |
| dirty 判定 | 受控 buffer 與 `saved` 的內容比較 | 不用「有無輸入事件」判定 |
| 樂觀鎖 | 持有的 digest（讀檔取得、寫成功更新、衝突與其他錯誤時不動） | 前端不自行計算 digest；`specDigestOf` 是 Go 端權威 |
| 寫入原子性與衝突判定 | `app.go` 的 `specWrite`／`planWrite`（暫存檔＋rename、sentinel） | 前端不重試、不覆寫、不吞錯 |
| 衝突的辨識與呈現 | 元件（sentinel 文字契約，D2） | 不在此票做 reload／compare（A1b）；不自動覆蓋未儲存內容 |
| 載入回應的歸屬判定 | 元件（比對目前選取路徑／請求識別） | 不假設回應順序與請求順序一致 |
| 未儲存內容的導覽保護 | 工作區元件＋`App.vue` 的攔截點 | 不重構分頁架構；選擇前不得卸載工作區元件 |

圖：`a1a-editor-buffer-state.mmd`（狀態機）、`a1a-seq-save.mmd`（循序圖，含等待期間輸入與衝突分支）。**圖與實作偏差須同 PR 修圖。**

## 七、Phase 3｜TDD（測試與 mutation acceptance table）

**新增測試**（前端 vitest 為主；`*` 表示需使編輯器文件改變，依 D5 以 `view.dispatch` 為退路）

| # | 測試 | 對應場景 |
|---|---|---|
| T1* | Spec：編輯器文件改變後 `fileContent` 等於編輯器內容、dirty 為真 | 真實輸入回寫 |
| T2* | Plan：同上，對 `plan.currentContent` | 真實輸入回寫 |
| T3* | Plan：**有狀態讀寫替身**（in-memory 檔案＋digest）——輸入後儲存，再重新載入，斷言 `EditorView` 文件等於儲存的內容 | Plan 存後重載 |
| T4* | Spec：同 T3，並斷言送出的不是 `extractGherkin(draft)` | Spec 存後重載 |
| T5* | 改回等於 `saved` → dirty 為假 | 改回原樣不誤報 |
| T6* | 儲存中繼續輸入為 B，回應成功後 `saved`＝A、dirty 仍為真、digest 為新值 | 儲存中繼續輸入 |
| T7* | 儲存中：再次儲存、切檔、切分頁、套用草稿皆被暫停；回應後恢復 | 儲存中封鎖 |
| T8 | 兩次載入回應亂序到達，屬於已被取代檔案的那筆整筆丟棄 | 延遲載入回應 |
| T9 | 替身丟出衝突 sentinel 文字 → 判為衝突；buffer／`saved`／digest 不變；未觸發重新載入 | digest 衝突 |
| T10 | 替身丟出其他錯誤 → 原訊息呈現、不判為衝突、三者不變 | 非衝突錯誤 |
| T11* | 切檔且 dirty → 出現選擇；選捨棄才載入新檔 | 切檔捨棄 |
| T12* | 切檔且 dirty → 選保留：停留原檔、新檔未載入（斷言替身未被呼叫） | 切檔保留 |
| T13* | 切分頁保護：Spec／Plan × 分頁按鈕／檔案樹選取／重新送核導向 6 例；**並斷言選擇前工作區元件未被切走或卸載** | 切分頁保護 |
| T14* | 套用草稿／確認 bump 後：編輯器與 buffer 更新、dirty 為真、`saved` 與 digest 未變 | 草稿與 bump 同步 |
| T15 | **Go 訊息契約測試**：`ErrSpecWriteConflict`／`ErrPlanWriteConflict` 的訊息含前端判別片語 | 跨語言 sentinel 契約 |

**Mutation acceptance table（§6.7，N/N 全跑，不抽樣；15 項）**

| # | 變異植入處 | 預期轉紅 |
|---|---|---|
| M1 | 移除 Spec 的 updateListener 回寫 | T1 |
| M2 | 移除 Plan 的 updateListener 回寫 | T2 |
| M3 | Plan 儲存改送 `saved` 而非 buffer | T3 |
| M4 | Spec 儲存改送 `extractGherkin(draftText)` 而非 buffer | T4 |
| M5 | dirty 判定改為「有輸入即 true」（不比 `saved`） | T5 |
| M6 | 儲存成功後不更新 `saved` | T3 的 dirty 斷言 |
| M7 | 衝突判別改為 catch-all（任何錯誤都當衝突） | T10 |
| M8 | 衝突分支仍更新持有的 digest | T9 |
| M9 | 切檔守衛不檢查 dirty | T11 |
| M10 | 儲存成功後 `saved` 取回應當下的 buffer（而非送出快照） | T6 |
| M11 | 儲存期間不封鎖切換與再次儲存 | T7 |
| M12 | 載入回應不比對歸屬即寫入 | T8 |
| M13 | 分頁守衛只實作在 Plan（Spec 缺） | T13 的 spec 三例 |
| M14 | 守衛在使用者選擇前就卸載工作區元件 | T13 的未卸載斷言 |
| M15 | 套用草稿後一併更新 `saved` | T14 |

每項須留四格證據：**套用**（`git diff` 顯示變更且檔案 hash 改變）、**紅在正題**（表中指定的測試因其自身的斷言訊息而失敗，不是撞到其他前置檢查、也不是由其他測試連帶失敗）、**還原**（還原後與變異前 byte-identical）、**回綠**（該 task 基準指令回綠）。實作前先做 expected-red（新測試在未修的 production code 上為紅）。

## 八、Task 分解與估點（bottom-up，hr 為權威單位）

| Task | 內容 | hr（低–高） | 中位 |
|---|---|---|---|
| T-1 | Spec 佈線：updateListener → `fileContent`、`saved`、dirty、儲存 action＋按鈕＋i18n key | 2.0–3.0 | 2.5 |
| T-2 | Plan 佈線：updateListener → `plan.currentContent`、`saved`、dirty 正確化 | 1.0–1.5 | 1.25 |
| T-3 | 衝突辨識契約（sentinel 文字常數＋Go 訊息契約測試） | 1.0–1.5 | 1.25 |
| T-4 | 非同步契約：送出快照、封鎖重疊、延遲載入丟棄、草稿／bump 同步 | 1.5–2.5 | 2.0 |
| T-5 | 切檔／切分頁守衛（含 `App.vue` 最小攔截點、三個入口、選擇前不卸載） | 2.0–3.0 | 2.5 |
| T-6 | 真實輸入測試 T1–T14（含 D5 可行性確認 0.5–1.0、有狀態讀寫替身） | 3.5–5.0 | 4.25 |
| T-7 | mutation table N/N（15 項＋expected-red） | 3.75–6.25 | 5.0 |
| T-8 | 人工 Wails 驗收：build＋實機 Spec／Plan 各一次輸入→儲存→重新開啟 | 1.0–1.5 | 1.25 |
| T-9 | design gate 往返、圖與 feature 對齊、closure review | 1.25–2.0 | 1.6 |

**合計 21.6 hr → 2.16 pt**（rev1 為 14.5 hr／1.45 pt；增量來自非同步契約、切分頁六例、有狀態替身重載斷言、mutation 9→15、人工驗收）。

**拆票建議（D9）**：

| 票 | 範圍 | hr | pt |
|---|---|---|---|
| A1a-1 編輯儲存閉環 | T-1（2.5）、T-2（1.25）、T-3（1.25）、T-4（2.0）、T-8（1.25）；T-6 扣除守衛測試後 2.75；T-7 的 11 項 3.67；T-9 分攤 1.0；測試 T1–T10、T14、T15；mutation M1–M8、M10、M12、M15 | 15.7 | 1.57 |
| A1a-2 未儲存內容導覽保護 | T-5（2.5）；T-6 的守衛測試 1.5；T-7 的 4 項 1.33；T-9 分攤 0.6；測試 T11–T13；mutation M9、M11、M13、M14 | 5.9 | 0.59 |

等候（design gate 往返、CI）不計工時。

## 九、驗證策略

- **自動化**：前端全套 `npx vitest run`（現況 40 檔／397 條）須全綠；`npm run build` exit 0；Go 訊息契約測試所屬套件綠。
- **辨識力**：mutation table N/N（第七節），另對需要真實輸入的測試先做 expected-red。
- **人工驗收（不可省略）**：實際建置並執行 Wails app，Spec 與 Plan 各做一次「輸入 → 儲存 → 重新開啟檔案確認內容」。可人工執行、不建整套 E2E 基建；**若無法執行則該項保留為未完成，不得以 jsdom 綠燈替代**。
- **未接線掃描**：對本票新增的每個匯出符號與新動作，機械掃描其 production 呼叫端；零命中即視為未接線，不得標記完成（這是 grep，不是判斷）。
- **邊界揭露**：jsdom＋`view.dispatch` 只能證明「編輯器文件變更會回寫 buffer 並被儲存送出」，不涵蓋真實鍵盤事件與 Wails runtime 的儲存流程；報告一律標明此邊界。

## 十、Gate A（A1a 完成條件）

- [ ] D9 裁定（拆票與否）並回寫；若拆票，本 Gate 依拆分後各票分列。
- [ ] 18 個可執行案例各有對應測試並全綠；需要真實輸入者的 expected-red 證據留存。
- [ ] mutation table N/N 四格證據齊全（套用／紅在正題／還原 byte-identical／回綠）。
- [ ] 前端全套 vitest 與 `npm run build` 綠；Go 訊息契約測試綠。
- [ ] **人工 Wails 驗收**：Spec 與 Plan 各一次「輸入 → 儲存 → 重新開啟」通過並留下操作紀錄；未執行即為未完成。
- [ ] 第三節六條非同步契約各有對應測試（T6–T10、T14）與 mutation（M10–M12、M15）。
- [ ] 切分頁保護在 Spec 與 Plan 兩邊各自成立，且三個入口皆不可繞過（T13 六例全綠）。
- [ ] 未接線掃描通過；圖（兩張 `.mmd`）與 feature 檔與最終實作一致，偏差已同 PR 修正。

## 修訂記錄

- rev2（2026-09-07）：design gate 第一輪 CHANGES_REQUIRED 後修訂——D1–D8 裁定回寫並新增 D9（拆票）；新增第三節非同步儲存與載入契約六條；場景改寫（12 Scenario＋1 Outline／6 例＝18 案例；切檔保留／捨棄拆為獨立情境；新增儲存中繼續輸入、儲存中封鎖、延遲載入回應、切分頁保護 Spec／Plan × 三入口、草稿與 bump 同步）；T3／T4 改用有狀態讀寫替身並斷言重載後的 `EditorView` 文件；mutation 9 → 15 項；Gate A 納入人工 Wails 驗收與「未執行即未完成」；速記（未接線掃描、紅在正題）展開為正式說明；兩張圖依契約更新；重估 14.5 → 21.6 hr（2.16 pt）並提出拆票。事實核對第 9 點更正為「五個寫入操作」並補記 commit `7789f40` 已載明的 Spec 同型檢視與 escalation 確認。
- rev1（2026-09-07）：建立。事實於 HEAD `3ea31ea` 重新核對；BDD 場景；DDD 責任表與兩張 mermaid 圖；TDD 測試與 mutation table；Task 分解與 bottom-up 估點；D1–D8 待裁定。（rev1 的場景條數敘述有誤——實際為 8 條，rev2 已改寫並更正。）
