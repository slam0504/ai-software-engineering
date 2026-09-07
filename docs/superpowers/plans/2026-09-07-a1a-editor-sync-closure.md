# A1a Spec／Plan 編輯器同步閉環 Implementation Plan

> **For agentic workers:** 本票為前端佈線與測試，無外部寫入。實作前須通過 owner design gate；push、開 PR、CI 另案授權。Steps use checkbox (`- [ ]`) syntax for tracking.
> 版本：rev1（2026-09-07，建立：事實重新核對、BDD 場景、DDD 責任劃分與兩張圖、TDD 測試與 mutation table、Task 分解、bottom-up 重估。**待 owner design gate**）
> 狀態：**待 owner design gate**（D1–D8）。尚未撰寫任何 production 或測試程式碼；未 push、未開 PR。
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
8. **既有測試全部是注入 props 或直接呼叫 store action**（`SpecWorkspace.test.ts:49`、`PlanWorkspace.test.ts:79` 等），沒有任何一條對編輯器做真實輸入。jsdom 下建構 `EditorView` 本身可行（兩個測試檔的 `beforeAll` 預熱已證明，B1b 遺留）。
9. **與 A1a 相鄰但不屬本票**：A2 的 error kind 機制已落地（`plan.ts:62` 的 `clearErrors(kind)`＋`PlanWorkspace.vue` 四處操作各自清同類，commit `7789f40`），本票沿用、不改其語意。

## 二、Global Constraints

1. **不新增 backend API**：只用既有 `SpecRead`／`SpecWrite`／`PlanRead`／`PlanWrite`。若 D2 選了需要改後端回傳形狀的方案，該假設即破，須另過 gate。
2. **A1b 界線**：外部檔案變更偵測、reload／compare／保留本地的選擇流程一律不做。本票只保證衝突「可被辨識並如實揭露」。
3. **不改 A2 已落地的 error lifecycle 語意**，只新增本票需要的 kind 與判別。
4. **不動 `app.go` 的寫入路徑與 sentinel 文字**（若 D2 採判別片語方案，改為「新增釘字測試保護該文字」，仍不改文字本身）。
5. mutation acceptance table 依 §6.7 **N/N 全跑**，不抽樣；變異只植入本票新增或修改的 production code。
6. 前端全套 `vitest` 與 `npm run build` 必須綠；Go 端若新增釘字測試，`go test` 對應套件須綠。

## 三、待 owner 裁定（D1–D8）

- **D1｜Spec 儲存動作**：Spec 需新增「儲存目前內容」的 action 與按鈕（現行不存在）。確認此項屬 A1a 範圍（對應驗收條件 (3)），且按鈕與 `accept-draft` 並列、i18n key 沿用 `spec.action.*` 命名。
- **D2｜衝突辨識方式**（三選一）：
  (a) 維持現況純字串顯示，不特別辨識衝突——最省，但無法滿足「衝突不得呈現為泛用錯誤」。
  (b) **建議**：前端定義判別片語常數（比對 sentinel 文字 `write conflict: expected_digest`），並在 Go 端新增釘字測試保護該文字，形成跨語言契約。不新增 backend API，代價是耦合訊息文字。
  (c) 改 `SpecWrite`／`PlanWrite` 回傳形狀帶 error code——最穩固，但**違反「不新增 backend API」假設**，需重新估點並可能拆票。
- **D3｜切檔／切分頁未儲存保護是否納入本票**：現況靜默覆蓋。納入則需動 `App.vue` 的 `v-if` 分頁結構（切分頁會卸載元件），屬本票以外的檔案；不納入則 A1a 完成後「打字→切檔→內容消失」仍會發生。估點差 **2.0 hr**（見第七節）。若不納入，須明確立為後續票，不得默認消失。
- **D4｜dirty 判定**：採「buffer ≠ 載入時 baseline」（內容比較，改回原樣即不 dirty），或「有輸入即 dirty」（較省但會誤報）。建議前者，場景表已依前者撰寫。
- **D5｜「真實輸入」的測試手段**：jsdom 下對 CM6 的可行手段依序為 (i) 對 `contenteditable` 派發 `beforeinput`／`input` 事件、(ii) `view.dispatch({changes})` 模擬 document 變更。(ii) 一定可行但較接近「呼叫 API」，(i) 較接近真實輸入但 jsdom 支援度未實測。**本票排入一段 0.5–1.0 hr 的可行性確認**，結果寫回 plan；若 (i) 不可行則以 (ii) 為準並明確標註其邊界（不宣稱涵蓋鍵盤事件層）。請裁示是否接受此退路。
- **D6｜mutation table 範圍**：第六節列 9 項（D3 不納入時為 8 項），依 §6.7 N/N 全跑、逐項留套用／紅在正題／還原 byte-identical／回綠四格證據。請確認項數與目標。
- **D7｜Spec 與 Plan 的 buffer 存放位置**：Plan 的 buffer 在 Pinia store（`plan.currentContent`），Spec 在元件 local ref（`fileContent`）。本票**不做對稱化重構**，各自原地接線；若要求對稱（Spec 也進 store）須另估。
- **D8｜估點**：bottom-up 重估中位 **14.5 hr → 1.45 pt**（含 D3）或 **12.5 hr → 1.25 pt**（不含 D3）。原始估計 1.4 pt 未經 gate，僅作對照。

## 四、Phase 1｜BDD（詞彙與場景）

**詞彙表**（本票統一用語）：

| 詞 | 定義 |
|---|---|
| editor document | CodeMirror `EditorView` 內部的文字狀態，使用者鍵入直接改變它 |
| 受控 buffer | 元件／store 內、**儲存動作實際送出**的字串（Spec `fileContent`、Plan `plan.currentContent`） |
| baseline | 載入或儲存成功當下的內容快照，dirty 判定的比較對象 |
| 持有的 digest | 讀檔時取得、寫檔時作為 `expectedDigest` 送出的值 |
| 磁碟內容 | 檔案在磁碟上的實際內容，只能經 `*Read`／`*Write` 存取 |

場景清單見 `docs/architecture/features/spec-plan-editing.feature`（9 條）：真實輸入回寫、Plan 存後重載、Spec 存後重載、改回原樣不誤報、digest 衝突、其他錯誤原樣揭露、切檔保護、切分頁保護。**「真實輸入」一律定義為對編輯器輸入，注入 props 或直接呼叫 store action 不算。**

## 五、Phase 2｜DDD（責任劃分）

| 責任 | 承擔者 | 不得承擔 |
|---|---|---|
| 使用者輸入的即時狀態 | editor document（CM6） | 不作為儲存來源直接讀取；一律經 updateListener 落到受控 buffer |
| 儲存送出的內容、dirty 判定 | 受控 buffer＋baseline | 不由 editor document 於儲存當下臨時取值（避免兩條真相來源） |
| 樂觀鎖 | 持有的 digest（讀檔取得、寫成功更新、衝突時不動） | 前端不自行計算 digest；`specDigestOf` 是 Go 端權威 |
| 寫入原子性與衝突判定 | `app.go` 的 `specWrite`／`planWrite`（暫存檔＋rename、sentinel） | 前端不重試、不覆寫、不吞錯 |
| 衝突的呈現 | 元件（依 D2 的辨識方式） | 不在此票做 reload／compare（A1b） |

圖：`a1a-editor-buffer-state.mmd`（狀態機：clean／dirty／saving／conflict／guard）、`a1a-seq-save.mmd`（真實輸入→儲存→重載，含衝突分支）。**圖與實作偏差須同 PR 修圖。**

## 六、Phase 3｜TDD（測試與 mutation acceptance table）

**新增測試（依場景綁定，皆為前端 vitest；`*` 表示需要真實輸入手段，依 D5 決定）**

| # | 測試 | 對應場景 |
|---|---|---|
| T1* | Spec：真實輸入後 `fileContent` 等於編輯器內容、dirty 為真 | 真實輸入回寫 |
| T2* | Plan：同上，對 `plan.currentContent` | 真實輸入回寫 |
| T3* | Plan：輸入後儲存，斷言 `write` mock 收到的第二參數＝鍵入後內容（非載入內容）；成功後 dirty 清除、digest 更新 | Plan 存後重載 |
| T4* | Spec：同 T3，斷言送出的不是 `extractGherkin(draft)` | Spec 存後重載 |
| T5* | 改回原樣 → dirty 為假 | 改回原樣不誤報 |
| T6 | `write` mock 丟出衝突 sentinel 文字 → 呈現為衝突、buffer 與 digest 不變、dirty 維持 | digest 衝突 |
| T7 | `write` mock 丟出其他錯誤 → 原樣呈現、不誤判為衝突 | 其他錯誤 |
| T8* | 切檔時 dirty → 出現保留／捨棄選擇；選捨棄才載入新檔（**D3 納入時才有**） | 切換檔案保護 |
| T9 | Go 釘字測試：`ErrSpecWriteConflict`／`ErrPlanWriteConflict` 訊息含前端判別片語（**D2 選 (b) 時才有**） | 跨語言契約 |

**Mutation acceptance table（§6.7，N/N 全跑，不抽樣）**

| # | 變異植入處 | 預期轉紅 |
|---|---|---|
| M1 | 移除 Spec 的 updateListener 回寫 | T1 |
| M2 | 移除 Plan 的 updateListener 回寫 | T2 |
| M3 | Plan 儲存改送 baseline 而非 buffer | T3 |
| M4 | Spec 儲存改送 `extractGherkin(draftText)` 而非 buffer | T4 |
| M5 | dirty 判定改為「有輸入即 true」（不比 baseline） | T5 |
| M6 | 儲存成功後不更新 baseline | T5（第二次比較）／T3 的 dirty 斷言 |
| M7 | 衝突判別改為 catch-all（任何錯誤都當衝突） | T7 |
| M8 | 衝突分支仍更新持有的 digest | T6 |
| M9 | 切檔 guard 不檢查 dirty（**D3 納入時才有**） | T8 |

每項須留：套用（`git diff`＋hash 改變）、紅在正題（指定測試自己的斷言訊息）、還原 byte-identical、該 task 基準指令回綠。**先做 expected-red**（實作前確認新測試在未修的 production code 上為紅）。

## 七、Task 分解與估點（bottom-up，hr 為權威單位）

| Task | 內容 | hr（低–高） | 中位 |
|---|---|---|---|
| T-1 | Spec 佈線：updateListener → `fileContent`、baseline、dirty、儲存 action＋按鈕＋i18n key | 2.0–3.0 | 2.5 |
| T-2 | Plan 佈線：updateListener → `plan.currentContent`、baseline、dirty 正確化（沿用既有 save 按鈕） | 1.0–1.5 | 1.25 |
| T-3 | 衝突辨識契約（依 D2；(b) 含 Go 釘字測試） | 1.0–1.5 | 1.25 |
| T-4 | 切檔／切分頁 guard（**依 D3；含 `App.vue` `v-if` 影響評估**） | 1.5–2.5 | 2.0 |
| T-5 | 真實輸入測試 T1–T8（含 D5 可行性確認 0.5–1.0） | 2.5–4.0 | 3.25 |
| T-6 | mutation table N/N 執行（9 項×套用／跑／還原／回綠＋expected-red） | 2.25–3.75 | 3.0 |
| T-7 | design gate 往返修訂、圖與 feature 對齊、closure review | 1.0–1.5 | 1.25 |

**合計（含 T-4）14.5 hr → 1.45 pt；不含 T-4 為 12.5 hr → 1.25 pt。** 原始估計 1.4 pt 未經 gate，本表為獨立 bottom-up，非回推。等候（design gate 往返、CI）不計工時。

## 八、驗證策略

- **自動化**：前端全套 `npx vitest run`（現況 40 檔／397 條）須全綠；`npm run build` exit 0；若 T9 成立，`go test ./...` 對應套件綠。
- **辨識力**：mutation table N/N（第六節），另對 T1–T5 先做 expected-red。
- **無法在本機驗證者**：真實鍵盤輸入在實際 Wails runtime 下的行為（jsdom 只能近似）。替代檢查＝D5 的手段選擇與邊界標註；**不得以 jsdom 綠燈宣稱「真實 app 內編輯已驗收」**，人工驗收另行安排。
- **零接線掃描**：本票新增的每個 exported 符號／新 action 須有 production 呼叫端，零命中即未完成。

## 九、Gate A（A1a 完成條件）

- [ ] D1–D8 全部裁定並回寫本 plan。
- [ ] 場景表 9 條各有對應測試並全綠；T1–T5 的 expected-red 證據留存。
- [ ] mutation table N/N 四格證據齊全（套用／紅在正題／還原 byte-identical／回綠）。
- [ ] 前端全套 vitest 與 `npm run build` 綠；Go 端（若有 T9）綠。
- [ ] 「打字→儲存→重載」在 Spec 與 Plan 兩邊均可由測試獨立證明；零接線掃描通過。
- [ ] 圖（兩張 `.mmd`）與 feature 檔與最終實作一致，偏差已同 PR 修正。
- [ ] D3 若判定不納入，後續票已明確立項，不默認消失。

## 修訂記錄

- rev1（2026-09-07）：建立。事實於 HEAD `3ea31ea` 重新核對（九項，含舊診斷未涵蓋的「Spec 無儲存入口」與「衝突無結構化辨識」）；BDD 9 條場景；DDD 責任表與兩張 mermaid 圖；TDD 測試 T1–T9 與 mutation M1–M9；Task 分解與 bottom-up 估點（1.45／1.25 pt）；D1–D8 待裁定。
