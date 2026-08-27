# Pre-M4 Readiness Backlog（rev1）

> 版本：rev1（2026-08-27）
> 狀態：**待 owner review**——通過後才逐票估點（0.1 pt＝1 hr；混合性質或超過 2.0 pt 必拆）
> 基準：HEAD `9be0f4d`（main，與 origin 同步）
> 來源：外部審核（網頁版 ChatGPT，基準 `4cb19b2`）經逐項核實後，與既有待辦合併；分軌依 owner 2026-08-27 裁決，不使用「M3b Closure」命名（多數項目源自 M2／M3a 缺口、M4 基礎建設與長期治理，非 M3b 失敗）

## 已裁決事項（owner 2026-08-27，各票引用時以編號標注）

1. 個人自用、macOS-first 定位維持。
2. Spec／Plan 手動編輯為正式第一級路徑，列 closure P1。
3. Implementation session 必須自動綁定**不可變 approvals snapshot**；「僅提供複製上下文」不接受。
4. M4 pilot 採 **GitHub-first**；Forge interface 保留 GitLab 擴充能力。
5. Gate 3 綁定：TaskRun、promotion head、main base、oracle-surface digest、required-check run、review／evidence provenance；merge-group checks 為 Gate 3 之後的獨立平台驗證。
6. DomainSpec 在 M4 僅作 shadow／explain 層，不接管 production；正式採用留待 M4.5。
7. TCA runner 近期信任邊界：本人、本機、自有 repo；外部不受信任程式另立 sandbox scope。
8. `app.go` 不做全面重構；僅做 M4 所需 application seam 的局部抽取。
9. Wall-clock 權威名單為 `docs/spikes/m3b-results.md` §7 的五條；其他候選須以現行 HEAD 重現並補入正式文件後才能加入。
10. 本 backlog rev1 通過 review 後才估點。

**不在本 backlog**：legacy hydrate 兩張既核定票（audit＋空 window 重複掃描、readEnvelopeRange 吞錯——另行追蹤）；DomainSpec spike 遞延 minors（`docs/superpowers/specs/2026-08-26-domainspec-spike-results.md` §五為權威清單）。

---

## 軌道 A：既有里程碑 closure

### A1（P1）Spec／Plan 手動編輯閉環

- **背景**：README 明確承諾可在 app 內編輯規格與計畫，但目前 editor 的手動輸入未形成完整持久化資料流；實際寫檔路徑只有模板建立與 AI 草稿套用。缺口源自 M2／M3a，非 M3b 失敗。
- **HEAD 證據**：`frontend/src` 全域 grep 無任何 `updateListener`（CodeMirror 輸入不回寫受控 buffer）；`PlanWorkspace.vue` 儲存寫入的是 Pinia `plan.currentContent`，僅在套用 AI 草稿時更新——直接鍵入後按儲存可能寫入舊 buffer；A9 驗收記錄 SpecWorkspace 手動編輯不落盤（以 shell 種資料繞過）。
- **驗收條件**：(1) CodeMirror `updateListener` 將 document 同步到受控 buffer；(2) Spec／Plan 均有明確 dirty state；(3) Spec 增加手動儲存動作；(4) 儲存沿用 digest optimistic locking；(5) 外部檔案變更時提供 reload／compare／保留本地的明確選擇；(6)「真實 editor 輸入 → 儲存 → 重載」integration test（不得只測注入 props）。
- **建議模組**：`frontend/src/components/SpecWorkspace.vue`／`PlanWorkspace.vue`、`frontend/src/stores/`、既有 `SpecWrite` 類 bindings。
- **依賴／裁決**：無依賴；裁決已定（#2）。

### A2 UI error lifecycle

- **背景**：Gate 2 dirty-tree 送核失敗後，後續 commit＋送核＋核可成功，舊錯誤仍留在畫面（A9 驗收 sec-12 記錄）。
- **HEAD 證據**：`frontend/src/stores/plan.ts:51` 定義 `clearErrors()`，production 程式碼零引用（僅測試引用）；`submitForApproval()` 成功路徑未呼叫。
- **驗收條件**：error lifecycle 三原則落地——新操作開始清除同類 transient error、操作成功清除該操作既有錯誤、durable blocker 歸 escalation 收件匣而非一般紅字；含回歸測試（成功後斷言錯誤清空）。
- **建議模組**：`frontend/src/stores/plan.ts`（必要時 spec store 同型檢視）、相關元件。
- **依賴／裁決**：無；無需裁決。

### A3 文件版本標記同步

- **背景**：核可物件的版本權威性不清。
- **HEAD 證據**：`docs/architecture/sdlc-workbench-app-plan.md:3` header 標 v1.11（2026-08-06），同檔 `:200`／`:347` 修訂記錄已至 v1.13（2026-08-18）。
- **驗收條件**：header 版本與最新 revision 同步；文件明確標注 frozen（核可綁定 digest）／living 的區分原則；同型檢查掃過 `docs/architecture/` 其他規劃文件。
- **建議模組**：`docs/architecture/*.md`。
- **依賴／裁決**：無；無需裁決。

### A4 Go minimum／validated toolchain 語意

- **背景**：語言最低版本與驗證工具鏈的語意未說清。
- **HEAD 證據**：`go.mod` 為 `go 1.25.0`；`README.md:95` 要求「Go 1.26+」、`:330` 標 Go 1.26；M3b 驗收使用 Go 1.26.5。
- **驗收條件**：二選一落地——(a) `go 1.25` 維持、README 改寫「1.25 minimum、1.26.5 validated」；或 (b) `go.mod` 加 `toolchain go1.26.5`。
- **建議模組**：`go.mod`、`README.md`。
- **依賴／裁決**：**需 owner 二選一**（未在既裁清單內）。

---

## 軌道 B：M4 entry blockers

### B1 五條 wall-clock 測試確定性化

- **背景**：具名五條測試量的是排程與牆鐘而非同步契約，紅了須人工單獨重跑判定——與 fail-closed 的 required check 語意衝突，是 CI（B2）升 required 的硬前置。
- **HEAD 證據**：`docs/spikes/m3b-results.md` §7 權威具名五條——`internal/codex/TestAppServerTerminateKillsGroup`、`internal/assist/TestClaudeAssistFailsLoudOnOversizedLine`、`internal/claude/TestMultiTurnSendAndTurnBoundaries`、root `TestInFlightTurnDoesNotBlockNewSession`、`internal/codex/TestAppServerMidStreamDeath`；§7 並記錄負載下 150 倍延遲的實測。
- **驗收條件**：(1) 五條改為 channel／barrier／process-exit event 等確定性同步或 fake clock＋可注入 timeout policy；(2) 併行＋`-race`＋負載下重複執行不偽陽（次數於估點時定）；(3) 名單自 §7 移除；(4) 三條候選（`internal/proc/TestOutputCancellationKillsGrandchildren`＋前端兩條，出處 2026-08-25 session 登記、未落文件）以現行 HEAD 重現——成立者補入文件後併入本票或開續票，不成立者除名（#9）。
- **建議模組**：`internal/codex`、`internal/assist`、`internal/claude`、root tests、`internal/proc`（候選）、frontend vitest（候選）。
- **依賴／裁決**：無依賴；裁決已定（#9）。

### B2 最小 CI＋ruleset

- **背景**：專案倡議「測試全綠＋review＋CI 過才合併」，自身尚無任何平台驗證——dogfooding gap 確認成立；required checks 是 M4 forge enforcement 的核心依賴。
- **HEAD 證據**：repo 無 `.github/workflows`；owner 以 GitHub API 唯讀確認 ruleset 空、main 無 branch protection、無 PR 歷史。
- **驗收條件**：(1) workflow 覆蓋 Go build／`go vet`／test、frontend Vitest＋typecheck＋build、macOS Wails build、schema／checksum 檢查；(2) main ruleset 以上述為 required checks；(3) race＋全套測試升 required 前置 B1 完成；(4) 任意 reviewer checkout 同一 SHA 可由 CI 獨立產生同等證據。
- **建議模組**：`.github/workflows/`、GitHub repo settings（ruleset）。
- **依賴／裁決**：required 完整化依賴 B1；GitHub-first 已裁（#4）。

### B3 Browser E2E 資產化

- **背景**：M3b 的 UI 驗收（wails dev＋Playwright）有價值但屬一次性操作，非 repo 可重跑資產；native window 渲染未逐項驗證的邊界已在驗收報告誠實揭露。
- **HEAD 證據**：`frontend/package.json` 無 Playwright／Cypress 依賴（lockfile 僅 Vitest optional transitive）。
- **驗收條件**：(1) repo 內可執行的 browser E2E suite，覆蓋 Gate 1、Gate 2、STALE、session recovery、approval 核心流；(2) 最小 native smoke（app 啟動、binding ready、主要 pane 渲染、bundle CLI 可尋得）；(3) E2E 納入 B2 的 CI（可為 non-required 起步）。
- **建議模組**：`frontend/`（新 e2e 目錄）、`.github/workflows`。
- **依賴／裁決**：與 B2 互相配合；無需裁決。

### B4 Workspace 明確確認

- **背景**：AI 可執行工具與改檔，workspace 誤落家目錄的風險只靠使用者自行檢查頂部文字，不符 fail-closed。
- **HEAD 證據**：`README.md:118-124`（Finder 開啟通常退回家目錄、僅提示檢查 `ws:` 顯示）、`:344`（`WORKBENCH_WORKSPACE` fallback 說明）。
- **驗收條件**：(1) 首次工具寫入前強制確認 workspace 或啟動時 workspace picker；(2) `$HOME` fallback 顯示高風險警示（非普通狀態文字）；(3) 測試覆蓋 fallback 路徑的確認閘。
- **建議模組**：startup／workspace 決議路徑（`main.go`／`app.go` 相關區）、frontend 頂部列。
- **依賴／裁決**：無；無需裁決（macOS-first 定位 #1 不變）。

### B5 TaskRun／Gate 3／forge 契約設計（spec 級）

- **背景**：M4 若未先定義「核准內容如何不可變地綁定 implementation session」，只會多一套 Gate 3 UI 而未關閉治理迴路。本票產出 spec 文件（走 design gate），不含實作。
- **HEAD 證據**：README 明載一般 session 不受 active spec／plan／permissions 約束（規劃中屬 M4）；DomainSpec spike 收斂報告（`2026-08-26-domainspec-spike-results.md`）提供 shadow evaluator 能力邊界供 Gate 3 explain 層設計參考。
- **驗收條件**：spec 定義——(1) **TaskRun snapshot**（不可變）：Gate 1／Gate 2 approval ID＋digest、task ID、selected risk tier、permission manifest digest、TCA approval ID、expected-red／negative-control evidence digest、implementation base commit；(2) implementation session 自動綁定該 snapshot（#3，「複製上下文」不接受）；(3) Gate 3 通過條件綁定六件：TaskRun、promotion head、main base、oracle-surface digest、required-check run、review／evidence provenance（#5）；merge-group checks 明確定位為 Gate 3 後獨立平台驗證；(4) Forge interface：GitHub-first、保留 GitLab 擴充（#4）；(5) DomainSpec 定位為 shadow／explain（#6）。spec 通過 design gate 即為本票完成。
- **建議模組**：`docs/superpowers/specs/`（新 spec）；牽動 `internal/`（設計對象：TaskRun／Forge／Gate 3 application service）。
- **依賴／裁決**：無依賴（可先行）；核心裁決已定（#3/#4/#5/#6）。

### B6 `app.go` M4 application seams（局部抽取）

- **背景**：`app.go` 已達 423,700 bytes，application orchestration 高度集中，與內部文件「輕量接線層」宣稱落差確認；但全面重構會混入大量既有 concurrency 風險——僅抽 M4 會觸碰的部分。
- **HEAD 證據**：`ls -la app.go` = 423,700 bytes；App 結構含 lifecycle／startup／registry／replay index／lease／mutex／ownership／shutdown／latch／測試 hook 等多責任。
- **驗收條件**：(1) 新增 TaskRun／Forge／Gate 3 對應的 application service（依 B5 spec）；(2) Wails binding 留在 `app.go`；(3) 只抽出 M4 觸及的既有 orchestration；(4) 驗收準則為「M4 新功能不再往 App aggregate 增加狀態欄位與測試 hook；新測試注入點走 interface／fake adapter」——**不以行數或物理切檔為驗收條件**；(5) 抽取部分的既有測試全綠（含 race）。
- **建議模組**：`app.go`、`internal/appcore`（或新 application package）。
- **依賴／裁決**：依賴 B5 spec；裁決已定（#8）。

---

## 軌道 C：M4 最小垂直切片

### C1 垂直切片：approved context → PR → Gate 3

- **背景**：先證明治理迴路端到端閉合，再抽象多 provider／多 forge。
- **HEAD 證據**：B5 所列（enforcement 缺口為規劃中的 M4 範圍，非現況缺陷）。
- **驗收條件**：以**一個 provider＋GitHub＋一個 task＋一套 required checks**完整跑通——選定 task → 建立不可變 TaskRun snapshot → 注入核准 spec／plan／risk／permissions／TCA → 啟動 implementation session → 收集 diff 與測試證據 → 建立 PR → 讀取 required checks → Gate 3 人工決議；全程不離開 app；TaskRun 能回答「這段實作在什麼核准上下文中完成」。
- **建議模組**：依 B5／B6 產出的 application service、forge adapter（GitHub）、frontend Gate 3 檢視。
- **依賴／裁決**：依賴 B1–B6 全部；裁決已定（#3/#4/#5）。

---

## 軌道 D：M4.5／長期治理

### D1 DomainSpec production 採用評估（M4.5）

- **背景**：spike GO 證明的是限定規則面 shadow 零 misalignment，非 Gate 3 權威決策能力；M4 僅 shadow／explain（#6）。
- **HEAD 證據**：`docs/superpowers/specs/2026-08-26-domainspec-spike-results.md` §六——step_rank 自動交叉檢查（擴規則面前必做）、`spec/rules/**` scope 擴充另立提案、A9 案例穩定 fixture repo、`CostEstimate.Max`＋static cost limit sign-off、host 層規則 I/O→facts 路徑設計。
- **驗收條件**：M4.5 立項時逐項處理上列五項；正式採用與否為獨立 GO／NO-GO。
- **依賴／裁決**：scope 擴充與 cost 策略需 owner 裁決；其餘工程項。

### D2 Audit tamper-evident 與保存政策

- **背景**：append-only 是應用程式寫入規約，app 外仍可編輯 JSONL——現況可稱「可追溯、可重建」，不可稱「不可竄改」；quarantine 檔無 retention policy（架構文件自承）。
- **驗收條件**：評估並落地 event hash chain／checkpoint signing／evidence manifest 其中適合個人定位的最小集；定義 audit／wire log／prompt 的保存年限、遮蔽、匯出與刪除政策。
- **依賴／裁決**：政策內容需 owner 裁決（保存多久、是否遮蔽）。

### D3 TCA runner sandbox scope

- **背景**：近期信任邊界已裁定為本人、本機、自有 repo（#7）；外部不受信任程式需真正的 container／VM 隔離與網路、credential 隔離。
- **驗收條件**：另立 sandbox scope 提案（範圍、技術選型、退出條件）；在此之前 TCA runner 明文拒絕信任邊界外的目標。
- **依賴／裁決**：提案本身需 owner 立項。

### D4 資源 admission control

- **背景**：單一 idle Claude 子行程實測約 385 MB RSS；「長對話、多工具、高頻輸出」尖峰未量測；第三 provider／ACP 前需有上限與警示。
- **驗收條件**：定義並落地——每 provider session 上限、idle parking／suspend、CPU／RSS 警示、context 長度與 log 體積上限、evidence runner 併行上限。
- **依賴／裁決**：上限數值需 owner 裁決；機制為工程項。

### D5 BDD scenario 與測試的 metadata 自動追蹤

- **背景**：目前條款→測試對應靠人工矩陣，測試名稱漂移無機制偵測；不必導入 Cucumber runner。
- **驗收條件**：(1) active scenario 具穩定 ID；(2) 測試以 metadata 宣告 scenario ID；(3) CI 驗證每個 active scenario 至少一個對應測試（掛 B2）；(4) 驗收報告可從 metadata 自動產生。
- **依賴／裁決**：CI 部分依賴 B2；無需裁決。

---

## 附註：外部審核主張的核實記錄（2026-08-27，HEAD `9be0f4d`）

| 主張 | 核實 |
|---|---|
| Spec／Plan editor 無 updateListener、儲存寫舊 buffer | 屬實（frontend/src 全域 grep） |
| `app.go` ≈423KB | 屬實（423,700 bytes） |
| 無 `.github/workflows`、無 ruleset／protection／PR 史 | 屬實（owner API 唯讀確認） |
| frontend 無 E2E 依賴 | 屬實 |
| `clearErrors` production 零引用 | 屬實（plan.ts:51） |
| go.mod 1.25.0 vs README 1.26+ | 屬實 |
| 文件版本漂移 v1.11／v1.13 | 屬實（app-plan.md:3 vs :347） |
| wall-clock 名單「五條」 | 屬實（m3b-results.md §7 為權威；另 3 條候選僅 session 登記，處理見 B1） |
| 審核基準 `4cb19b2` 未含 DomainSpec 系列 | 屬實；spike 未改 production／UI 路徑，上述事實不受影響 |
