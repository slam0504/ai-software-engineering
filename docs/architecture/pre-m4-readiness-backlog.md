# Pre-M4 Readiness Backlog（rev3）

> 版本：rev3（2026-08-27，backlog review 第二輪 5 P1＋3 P2 收斂）
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
10. 本 backlog 通過 review 後才估點。

**不在本 backlog**：legacy hydrate 系列——rev3 更正：rev1／rev2 誤列為「待辦兩票」，實際**均已於外部審核基準之前完成**（commit 證據見附錄 B），不存在待追蹤工作；DomainSpec spike 遞延 minors（`docs/superpowers/specs/2026-08-26-domainspec-spike-results.md` §五為權威清單，該檔已入 repo，追蹤入口成立）。

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

- **背景**：語言最低版本與驗證工具鏈的語意未說清。rev2 更正：`go` directive（最低語言／模組版本）與 `toolchain` directive（建議工具鏈）**不是二選一、可並存**——rev1 誤寫成互斥選項。
- **HEAD 證據**：`go.mod` 為 `go 1.25.0`；`README.md:95` 要求「Go 1.26+」、`:330` 標 Go 1.26；M3b 驗收使用 Go 1.26.5；reviewer 以 Go 1.25.0 實跑 `go build ./...` 通過（但 build 通過**不足以**宣稱 1.25 完整支援）。
- **驗收條件**（rev3 改版本歸因矩陣——「未全綠即提高 minimum」會把非版本因素誤判成版本不足）：(1) **同一 commit** 分別以 Go 1.25 與 1.26.5 跑完整 Go gate（test＋race＋vet）與 Wails build，做比較矩陣；(2) 只有**能歸因於 Go 版本不相容**的失敗（1.25 紅、1.26.5 綠、且排除環境／工具缺失）才提高 minimum；已知 wall-clock 名單（B1）與環境性失敗**不參與版本裁決**；(3) 依矩陣結果同步 README 語意（「X minimum、1.26.5 validated」）；(4) **獨立決策**：是否加 `toolchain go1.26.5` 提升 reproducibility（與 minimum 判定無關，可並存）。
- **建議模組**：`go.mod`、`README.md`。
- **依賴／裁決**：建議 B1 完成後執行（或依 (2) 明文排除 wall-clock 偽陽）；(4) 需 owner 裁決。

---

## 軌道 B：M4 entry blockers

### B1 五條 wall-clock 測試確定性化

- **背景**：具名五條測試量的是排程與牆鐘而非同步契約，紅了須人工單獨重跑判定——與 fail-closed 的 required check 語意衝突，是 CI（B2）升 required 的硬前置。
- **HEAD 證據**：`docs/spikes/m3b-results.md` §7 權威具名五條——`internal/codex/TestAppServerTerminateKillsGroup`、`internal/assist/TestClaudeAssistFailsLoudOnOversizedLine`、`internal/claude/TestMultiTurnSendAndTurnBoundaries`、root `TestInFlightTurnDoesNotBlockNewSession`、`internal/codex/TestAppServerMidStreamDeath`；§7 並記錄負載下 150 倍延遲的實測。
- **驗收條件**：(1) 五條改為 channel／barrier／process-exit event 等確定性同步或 fake clock＋可注入 timeout policy；(2) 併行＋`-race`＋負載下重複執行不偽陽（次數於估點時定）；(3)（rev3 更正——**不得刪改 m3b-results.md §7 的歷史驗收紀錄**）§7 原始觀察保留，於各條**追加** resolved commit、修正方式與重驗證據；「目前有效名單」另立 living 狀態文件（或本 backlog 附錄）承載；(4) 三條候選以現行 HEAD 重現——成立者補入 living 文件後併入本票或開續票，不成立者除名（#9）。候選具名（rev2，原始紀錄 2026-08-21 session 首錄、2026-08-25 更新，未落正式文件）：
  - `internal/proc/TestOutputCancellationKillsGrandchildren`
  - 前端 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積`
  - 前端 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call`
- **建議模組**：`internal/codex`、`internal/assist`、`internal/claude`、root tests、`internal/proc`（候選）、frontend vitest（候選）。
- **依賴／裁決**：無依賴；裁決已定（#9）。

### B2 最小 CI＋ruleset

- **背景**：專案倡議「測試全綠＋review＋CI 過才合併」，自身尚無任何平台驗證——dogfooding gap 確認成立；required checks 是 M4 forge enforcement 的核心依賴。
- **HEAD 證據**：repo 無 `.github/workflows`；2026-08-27 reviewer 以 GitHub API 唯讀核實 ruleset 空、main 無 branch protection、無 PR 歷史。
- **驗收條件**：(1) workflow 覆蓋 Go build／`go vet`／test、frontend Vitest＋typecheck＋build、macOS Wails build、schema／checksum 檢查；(2) main ruleset 以上述為 required checks；(3) race＋全套測試升 required 前置 B1 完成；(4) 任意 reviewer checkout 同一 SHA 可由 CI 獨立產生同等證據；(5) **enforcement 實證（rev2 補——只設定不驗證，dogfooding 仍不成立）**：(5a) failing／missing required check 的 PR 實測不可合併（負向驗證）；(5b) checks 全綠後實測可合併；(5c) direct push、admin bypass 與緊急例外政策明文化；(5d) required-check 名稱變更時 fail loud（不得靜默降級為非必要）；(5e) ruleset 誤刪的回復方式，與外部（GitHub 設定面）變更需 owner 授權的規約。**驗證方式邊界（rev3——本項實證會改變 GitHub 外部狀態）**：負向驗證一律使用測試 PR＋可回復 branch，驗證完即關閉／刪除；**不得**為了驗證把已知失敗內容合併進 main。
- **建議模組**：`.github/workflows/`、GitHub repo settings（ruleset）。
- **依賴／裁決**：required 完整化依賴 B1；GitHub-first 已裁（#4）。

### B3a Browser E2E 資產化

- **背景**：M3b 的 UI 驗收（wails dev＋Playwright）有價值但屬一次性操作，非 repo 可重跑資產。（rev2：原 B3 依「混合性質必拆」自我規則拆為 B3a／B3b。）
- **HEAD 證據**：`frontend/package.json` 無直接宣告的 E2E dependency、script、config 與 suite（lockfile 有 Vitest 的 optional Playwright transitive entry）。
- **驗收條件**：(1) repo 內可執行的 browser E2E suite，覆蓋 Gate 1、Gate 2、STALE、session recovery、approval 核心流；(2) 納入 B2 的 CI（可為 non-required 起步）。
- **建議模組**：`frontend/`（新 e2e 目錄）、`.github/workflows`。
- **依賴／裁決**：與 B2 互相配合；無需裁決。

### B3b 最小 native smoke

- **背景**：browser E2E 與 native window 共用 Go backend 與 bindings，但 native 渲染未逐項驗證（M3b 驗收報告已誠實揭露此邊界）。
- **HEAD 證據**：同 B3a；repo 無 native smoke 資產。
- **驗收條件**：app 能啟動、binding ready、主要 pane 渲染、bundle CLI 可尋得——可自動化執行並產生證據。
- **建議模組**：Wails build 產物驗證腳本、`.github/workflows`（macOS runner）。
- **依賴／裁決**：依賴 B2 的 macOS build job；無需裁決。

### B4 Workspace 明確確認

- **背景**：AI 可執行工具與改檔，workspace 誤落家目錄的風險只靠使用者自行檢查頂部文字，不符 fail-closed。
- **HEAD 證據**：`README.md:118-124`（Finder 開啟通常退回家目錄、僅提示檢查 `ws:` 顯示）、`:344`（`WORKBENCH_WORKSPACE` fallback 說明）。
- **驗收條件**：(1) 首次工具寫入前強制確認 workspace 或啟動時 workspace picker；(2) `$HOME` fallback 顯示高風險警示（非普通狀態文字）；(3) 測試覆蓋 fallback 路徑的確認閘。
- **建議模組**：startup／workspace 決議路徑（`main.go`／`app.go` 相關區）、frontend 頂部列。
- **依賴／裁決**：無；無需裁決（macOS-first 定位 #1 不變）。

### B5 TaskRun／Gate 3／forge 契約設計（spec 級）

- **背景**：M4 若未先定義「核准內容如何不可變地綁定 implementation session」，只會多一套 Gate 3 UI 而未關閉治理迴路。本票產出 spec 文件（走 design gate），不含實作。
- **HEAD 證據**：README 明載一般 session 不受 active spec／plan／permissions 約束（規劃中屬 M4）；DomainSpec spike 收斂報告（`2026-08-26-domainspec-spike-results.md`）提供 shadow evaluator 能力邊界供 Gate 3 explain 層設計參考。
- **驗收條件**：spec 定義——(1) **TaskRun snapshot**（不可變）：Gate 1／Gate 2 approval ID＋digest、task ID、selected risk tier、permission manifest digest、TCA approval ID、expected-red／negative-control evidence digest、implementation base commit；(2) implementation session 自動綁定該 snapshot（#3，「複製上下文」不接受）；(3) Gate 3 通過條件綁定六件：TaskRun、promotion head、main base、oracle-surface digest、required-check run、review／evidence provenance（#5）；merge-group checks 明確定位為 Gate 3 後獨立平台驗證；(4) Forge interface：GitHub-first、保留 GitLab 擴充（#4）；(5) DomainSpec 定位為 shadow／explain（#6）；(6) **STALE／重驗生命週期（rev2 補；rev3 標注決策歸屬——缺此則「不可變 snapshot」只能回答歷史上下文，擋不住過期核可進 Gate 3）**：(6a) TaskRun 建立時 active approvals 的 currentness 前置條件；(6b) 上游變動後既有 TaskRun 的 STALE 轉換規則——**必須區分三種形狀**：commit missing、binding digest 改變、單純 HEAD 前移；**HEAD 正常前移不得觸發 STALE**（沿既有 TCA 歷史錨點契約，不得破壞）；(6c)【**owner 決策**】main base 變動是否立即使 Gate 3 失效（promotion head／required-check 結果變動同組討論）；(6d)【**owner 決策**】implementation session resume 是否僅能回到原 TaskRun；(6e)【**owner 決策**】STALE 後的處置語意——中止 session、保留 evidence 但禁止 Gate 3、或強制建立新 TaskRun；(6f) Gate 3 決議當下必須重新驗證的 bindings 清單。(6c)(6d)(6e) 為商業流程選擇，於 B5 design gate 中列 owner 決議事項，非工程自行取捨。spec 通過 design gate 即為本票完成。
- **建議模組**：`docs/superpowers/specs/`（新 spec）；牽動 `internal/`（設計對象：TaskRun／Forge／Gate 3 application service）。
- **依賴／裁決**：無依賴（可先行）；核心裁決已定（#3/#4/#5/#6）。

### B6 `app.go` M4 application seams（局部抽取）

- **背景**：`app.go` 已達 423,700 bytes，application orchestration 高度集中，與內部文件「輕量接線層」宣稱落差確認；但全面重構會混入大量既有 concurrency 風險——僅抽 M4 會觸碰的部分。
- **HEAD 證據**：`ls -la app.go` = 423,700 bytes；App 結構含 lifecycle／startup／registry／replay index／lease／mutex／ownership／shutdown／latch／測試 hook 等多責任。
- **驗收條件**：(1) 新增 TaskRun／Forge／Gate 3 對應的 application service（依 B5 spec）；(2) Wails binding 留在 `app.go`；(3) 只抽出 M4 觸及的既有 orchestration；(4) 驗收準則（rev2 精確化）：**允許** App 注入 application service／port reference；**不得**新增 M4 domain mutable state 或 ad-hoc test hook 於 App aggregate；新測試注入點走 interface／fake adapter——不以行數或物理切檔為驗收條件；(5) 抽取部分的既有測試全綠（含 race）。
- **建議模組**：`app.go`、`internal/appcore`（或新 application package）。
- **依賴／裁決**：依賴 B5 spec；裁決已定（#8）。

---

## 軌道 C：M4 最小垂直切片

### C1 Implementation-to-Gate-3 垂直切片

- **背景**：先證明 M4 **新增**的治理迴路（核准上下文綁定 → PR → Gate 3）端到端閉合，再抽象多 provider／多 forge。**範圍限縮聲明（rev2）**：本票起點是「已選定 task」，不含 spec／plan authoring 與 Gate 1／Gate 2／TCA 流程（該些為 M2／M3 已驗收資產）；因此**本票完成不得宣稱滿足完整 SC4**——「app 內從 authoring 到 Gate 3 全程不離開」的完整驗收，須待 A1 完成後另立驗收項（見依賴欄）。選擇限縮而非前移起點的理由：垂直切片的價值在最小範圍證明新迴路，authoring 閉環是獨立缺口（A1），混入會讓失敗訊號無法歸因。
- **HEAD 證據**：B5 所列（enforcement 缺口為規劃中的 M4 範圍，非現況缺陷）。
- **驗收條件**：以**一個 provider＋GitHub＋一個 task＋一套 required checks**跑通——選定 task → 建立不可變 TaskRun snapshot → 注入核准 spec／plan／risk／permissions／TCA → 啟動 implementation session → 收集 diff 與測試證據 → 建立 PR → 讀取 required checks → Gate 3 人工決議；此段全程不離開 app；TaskRun 能回答「這段實作在什麼核准上下文中完成」。
- **建議模組**：依 B5／B6 產出的 application service、forge adapter（GitHub）、frontend Gate 3 檢視。
- **依賴／裁決**：依賴 **B1、B2、B3a、B3b、B4、B5、B6**（rev3 明列）；完整 SC4 驗收由 **C2** 承載（rev3 新增，不再是「完成後另開」）；forge 裁決已定（#4）；【**owner 決策，估點前置**】pilot provider 單選——Claude 與 Codex 的 process／resume／session ownership 路徑不同，直接影響本票範圍與估點，估點前必須選定。

### C2 SC4 authoring-to-Gate-3 端到端驗收

- **背景**：C1 明文不宣稱完整 SC4；本票（rev3 新增）為完整承諾的 durable 追蹤入口——「app 內從 spec authoring、Gate 1、plan authoring、Gate 2、TCA 到 implementation、PR、Gate 3 全程不離開」。
- **HEAD 證據**：同 A1（authoring 閉環缺口）＋B5（enforcement 缺口屬 M4 規劃）。
- **驗收條件**：在 app 內建立／手動編輯 spec → Gate 1 核可 → plan 編輯 → Gate 2 核可（含 risk 決議）→ TCA → C1 的 implementation-to-Gate-3 全鏈——單一連續走查完成、全程不離開 app，各步驟以既有 gate 證據慣例留痕。
- **建議模組**：無新模組（驗收性質票，串接 A1＋C1 產出）。
- **依賴／裁決**：依賴 **A1＋C1**；無新裁決。

---

## 軌道 D：M4.5／長期治理

### D1 DomainSpec production 採用評估（M4.5）

- **背景**：spike GO 證明的是限定規則面 shadow 零 misalignment，非 Gate 3 權威決策能力；M4 僅 shadow／explain（#6）。
- **HEAD 證據**：`docs/superpowers/specs/2026-08-26-domainspec-spike-results.md` §六——step_rank 自動交叉檢查（擴規則面前必做）、`spec/rules/**` scope 擴充另立提案、A9 案例穩定 fixture repo、`CostEstimate.Max`＋static cost limit sign-off、host 層規則 I/O→facts 路徑設計。
- **驗收條件**：M4.5 立項時逐項處理上列五項；正式採用與否為獨立 GO／NO-GO。
- **依賴／裁決**：scope 擴充與 cost 策略需 owner 裁決；其餘工程項。

### D2a Audit tamper-evident 機制

- **背景**：append-only 是應用程式寫入規約，app 外仍可編輯 JSONL——現況可稱「可追溯、可重建」，不可稱「不可竄改」。（rev2：原 D2 依「混合性質必拆」拆為 D2a 技術機制／D2b 資料政策。）
- **驗收條件**：評估並落地 event hash chain／checkpoint signing／evidence manifest 中適合個人定位（#1）的最小集；明文記錄「不可竄改」宣稱的邊界。
- **依賴／裁決**：技術選型可工程判斷；宣稱邊界需 owner 確認。

### D2b Audit／wire log／prompt 保存與刪除政策

- **背景**：保存年限、可能含 token／路徑／商業資料的遮蔽、匯出與刪除目前皆無政策；quarantine 檔無 retention policy（架構文件自承）。
- **驗收條件**：定義並落地保存年限、遮蔽規則、匯出與刪除流程、quarantine retention。
- **依賴／裁決**：政策內容需 owner 裁決。

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

## 附錄 A：外部審核主張的核實記錄（2026-08-27，HEAD `9be0f4d`）

| 主張 | 核實 |
|---|---|
| Spec／Plan editor 無 updateListener、儲存寫舊 buffer | 屬實（frontend/src 全域 grep） |
| `app.go` ≈423KB | 屬實（423,700 bytes） |
| 無 `.github/workflows`、無 ruleset／protection／PR 史 | 屬實（2026-08-27 reviewer 以 GitHub API 唯讀核實） |
| frontend 無 E2E 資產 | 屬實——無直接宣告的 E2E dependency、script、config 與 suite；lockfile 有 Vitest 的 optional Playwright transitive entry |
| `clearErrors` production 零引用 | 屬實（plan.ts:51） |
| go.mod 1.25.0 vs README 1.26+ | 屬實 |
| 文件版本漂移 v1.11／v1.13 | 屬實（app-plan.md:3 vs :347） |
| wall-clock 名單「五條」 | 屬實（m3b-results.md §7 為權威；另 3 條候選僅 session 登記，具名與處理見 B1） |
| 審核基準 `4cb19b2` 未含 DomainSpec 系列 | 屬實；spike 未改 production／UI 路徑，上述事實不受影響 |

## 附錄 B：legacy hydrate follow-ups——已完成記錄（rev3 更正，不納入 backlog）

rev1／rev2 誤將此系列列為 pending——實際工作**均已於外部審核基準 `4cb19b2` 之前完成**，commit 證據（rev3 已逐一以 `git log`＋ancestry 核實）：

| 原誤列項 | 實際狀態 | Commit 證據 |
|---|---|---|
| 「空 window 重複掃描」 | 已完成——loadTurnsBefore 空 window 清旗標（§6a 四分支、persist 失敗 fail loud） | `0de1f78` |
| 「audit 佐證」 | 已完成——backfill 失敗具名 audit（`legacy_transcript_backfill_failed`） | `fd88185` |
| 「readEnvelopeRange 吞錯」 | 已完成——非 EOF 讀取錯誤 fail loud，不再當 EOF 靜默截頁 | `c8e1e52` |

對應 spec、plan、production 與測試均在 repo（`docs/superpowers/specs/2026-08-24-replay-reliability-design.md`、`rebuild_orchestrator.go`）。錯誤盤點根因：auto-memory 索引行過期（本文已正確標「勿再列為待辦」）——索引已同步更正。

## 修訂記錄

- rev2（2026-08-27，backlog review 第一輪 5 P1＋4 P2 收斂）：
  - P1：A4 更正 go／toolchain 非二選一——先以 1.25 跑完整 gate 決定 minimum，toolchain 為獨立 reproducibility 決策。
  - P1：B5 補 STALE／重驗生命週期六項（currentness 前置、STALE 轉換、Gate 3 失效、resume 綁定、STALE 處置、決議時重驗清單）。
  - P1：C1 改名「Implementation-to-Gate-3 垂直切片」並明文不得宣稱完整 SC4；完整 SC4 驗收依賴 A1、另立續票。
  - P1：排除項與 wall-clock 候選補 durable locator——附錄 B 收 legacy hydrate 兩票、B1 具名三條候選（出處更正為 2026-08-21 首錄／08-25 更新）。
  - P1：B2 補 enforcement 實證五項（負向合併驗證、bypass 政策、check 更名 fail loud、ruleset 回復與外部變更授權）。
  - P2：B6 準則精確化（允許 service／port 注入；禁 M4 domain mutable state 與 ad-hoc hook）；B3 拆 B3a／B3b、D2 拆 D2a／D2b（依自身「混合必拆」規則）；核實表 E2E 措辭補 lockfile transitive 事實；GitHub API 核實者更正為 reviewer。
- rev3（2026-08-27，backlog review 第二輪 5 P1＋3 P2 收斂）：
  - P1：附錄 B 更正為**已完成記錄**——LH 系列均於審核基準前完成（0de1f78／fd88185／c8e1e52，ancestry 已核實），rev1／rev2 誤列 pending 的根因（auto-memory 索引過期）已同步修正；排除項敘述同步。
  - P1：A4 改同 commit 1.25／1.26.5 比較矩陣——只有可歸因版本不相容的失敗才提高 minimum；wall-clock（B1）與環境失敗不參與版本裁決。
  - P1：B1 不再要求刪除 m3b-results.md §7 歷史紀錄——原始觀察保留＋追加 resolved 證據，「目前有效名單」另立 living 文件。
  - P1：B5 (6c)(6d)(6e) 標注為 owner 決策（商業流程選擇）；(6b) 明定區分 commit missing／binding digest 改變／HEAD 前移，HEAD 正常前移不得 STALE（沿既有 TCA 錨點契約）。
  - P1：C1 增 pilot provider 單選為估點前置 owner 決策；新增 **C2 SC4 authoring-to-Gate-3 端到端驗收**（依賴 A1＋C1），完整 SC4 自此有 durable 追蹤入口。
  - P2：C1 依賴明列 B1／B2／B3a／B3b／B4／B5／B6；裁決事項第 10 項改為不綁版本措辭；B2 enforcement 實證補「測試 PR＋可回復 branch、不得為驗證合併已知失敗內容」邊界。
- rev1（2026-08-27）：初版。
