# Pre-M4 Readiness Backlog（rev10·估點版）

> 版本：rev10（2026-09-04，**B1a-2 實作完成並關票**——狀態更新，估點與票面範圍不變）
> 前版：rev9（2026-09-03，B1a-1 實作完成並關票——狀態更新，估點與票面範圍不變）；rev8（2026-09-03，B1a preflight 實測落地——`TestAppServerTerminateKillsGroup` 未進入 escalation 分支之事實修正、#1 判類 2 並釘死邊界、B1a 逐條 bottom-up 重估後拆為三張施工票＋一張整合驗收票）；rev7（2026-08-31，C1 驗收條件補「Forge 回傳 eligible CR 不得經篩除後假綠」案例）；rev6（2026-08-28，B6 依 owner 於 B6 plan gate 第三輪裁決拆為 B6a/B6b——1.45／0.6 pt；C1 相依同步改 B6a＋B6b；其餘票面不變）
> 狀態：**B1a-1 已完成（2026-09-03，Gate 3 APPROVED，`82caf8b`／`f7ad1ed`）；B1a-2 已完成（2026-09-04，Gate B APPROVED，`7b1bb0c`／`05069e2`／`b0a8404`）；B1a-3／B1a-4 仍未完成，B1a aggregate 尚未關閉**——B1a 四票估點已於 rev8 通過 owner review（2026-09-03）；**A 軌五票（A1a／A1b／A2／A3／A4）、B 軌未完成之其餘票（B1b／B2／B3a-1／B3a-2／B3b／B4）、C 軌四票（C1a／C1b／C1c／C2）之估點仍待 owner review**（B5／B6a／B6b 已完成，不列入待估 review）——D 軌待立項後估（rev4 凍結）
> 盤點基準：`9be0f4d`（rev1 起始時之 main＝origin/main；後續 rev 修訂 commit 不改變盤點內容之基準）
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
- **驗收條件**（rev3 改版本歸因矩陣；rev4 落 owner 裁決）：(1) **同一 commit** 分別以 Go 1.25 與 1.26.5 跑完整 Go gate（test＋race＋vet）與 Wails build，做比較矩陣；**1.25 腳必須記錄實際 `go version` 輸出，並以 `GOTOOLCHAIN=local` 執行**——自動切換風險發生在**加入 `toolchain go1.26.5` 的受測 commit** 上（現行 go.mod 尚無 toolchain directive，`GOTOOLCHAIN=auto` 下實際仍為 1.25.0）；受測 commit 含 toolchain 後不設 `GOTOOLCHAIN=local` 即 1.25 證據無效（owner 2026-08-27 裁決附帶條件，措辭依估點 gate 校正）；(2) 只有**能歸因於 Go 版本不相容**的失敗（1.25 紅、1.26.5 綠、且排除環境／工具缺失）才提高 minimum；已知 wall-clock 名單（B1）與環境性失敗**不參與版本裁決**；(3) 依矩陣結果同步 README 語意（「X minimum、1.26.5 validated」）；(4) **已裁決（owner 2026-08-27）：採用 `toolchain go1.26.5`**——與 minimum 判定並存。
- **建議模組**：`go.mod`、`README.md`。
- **依賴／裁決**：建議 B1 完成後執行（或依 (2) 明文排除 wall-clock 偽陽）；裁決已定，無待決項。

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
- **preflight 實測修正與 #1 裁定（rev8，owner 2026-09-03 裁示；證據綁 `aa55413`）**：
  - **現有 `TestAppServerTerminateKillsGroup` 根本沒有進入 escalation 分支**（已實測，非推論）——`testdata/fake-codex-appserver.sh` 的 `FAKE_ORPHAN` 只讓孫程序 `trap "" TERM`、leader 自身不 trap，收到 group SIGTERM 即退出，`Proc.Terminate()` 內的 `select` 永遠走 `<-p.exitedCh`，`time.After(p.grace)` 升級分支不執行。preflight 先依原假設對 grace timer 套 mutation，測試維持綠燈；改標的到 supervisor 收尾管線（`cmd.Wait()` 返回後）才紅在自身斷言 `kill escalation too slow`。**後續 plan 以此事實重寫，不得保留「驗得不夠確定」的原前提**——現況是該條測試從未驗過 escalation。
  - **#1 判為類 2（局部注入），不拆票，但邊界釘死**：不新增或修改 exported `Config` 與公開簽章；只在 `Proc` 加未匯出的 timer 與 signal-event seam，由同套件 white-box 測試注入；事件必須**區分 Terminate 升級與 supervisor 收尾**，避免把收尾管線送出的 SIGKILL 誤認成 escalation。**若實作需要改動七個呼叫端、公開 API 或死因仲裁，立即升為類 3、停止施工並拆票重估。**
  - **#1 拆成兩條不同契約的測試**：(a) codex 端改為驗「leader 收到 TERM 後退出，supervisor 清除仍存活的 process group」，**移除或改寫誤導的 `kill escalation too slow`**，不再宣稱驗到 escalation；(b) `internal/proc` 新增 deterministic white-box 測試——leader 本身也忽略 TERM，明確驗證 TERM → 注入的 grace timer 到期 → escalation 分支送出 KILL → process group 消失。**5s 效能斷言移除**；真實時間 timeout 僅保留作卡死保險，不作功能驗收。
  - **#1／#5 的連帶失敗不得只靠讀碼寫成既定證據**：acceptance table 先標「**潛在連帶**」；design gate 須在 mutation 套用期間**實跑受影響套件**取得實際連帶清單；若 mutation 仍打在本票未修改的共用基礎設施，須重新設計到本票新增的 seam，不得靠廣泛連帶失敗算通過（現行 v2.4 §6.7；變異目標規則於 v2.3 引入）。
  - **#4 裁定：只接線、不改語意**——qErr 跨代影響面已於第三輪 preflight 查證：quiesce 逾時但 kill 相位成功收斂後，舊 host 已移除（`hostFor(w)` 轉 nil）、`IsActive=false`，同一 WSID 新一代 `StartSession`／`SendMessage` 全部成功、host 身分乾淨換代，**不是永久控制狀態污染**。本票只需把 fixture 接上 `afterFn`／`newFakeAfter()`，**保留既有錯誤契約**。該契約已由既有測試釘死——`internal/appcore/manager_test.go:1052-1067` `TestCloseSequenceOrderTimeoutAndStuck` 明確斷言「kill 相位成功、`finalize` 正常收尾時，整體回傳仍須非 nil 且含 `quiesce timeout`」，與「kill 成功即算整體成功」相反；任何吞掉 qErr 的語意變更都會直接打破這條測試，屬 B1a 範圍外。
  - **新未匯出 seam 必須 nil-safe（rev8 證據邊界修正）**：`internal/proc/proc_test.go:380` 並非裸 `&Proc{}`，它提供了 `exitedCh`；且該測試的 `SignalGroup` 必然回 ESRCH，`Terminate()` 在回錯分支即返回、**根本不會進入 escalation goroutine**——因此它**證明不了**新 seam 的 nil 安全性。另一處 `proc_test.go:297` 為 `&Proc{canceled:, exitReady:, exit:}`，連 `exitedCh` 都未提供。部分欄位建構是本檔既有慣例，故 plan 必須硬性要求：**新增的未匯出 timer／signal-event seam 在 nil 時退回 real timer 與 no-op observer，不得直接呼叫 nil function（panic），亦不得向 nil channel 送值（永久阻塞、更難診斷）**。#1 仍維持類 2。
  - **preflight 的證據缺口（rev8，明確標為未驗證，不得引用為已完成的負載驗證）**：三輪 preflight 的人工加壓依 owner 裁定「只做一輪、有上限」執行，該輪有兩處未涵蓋——(i) **root package（含 #4）未跑完**，高負載下編譯搶不到 CPU 而依上限中止，**這是「未取得結果」而非「未重現」**；(ii) **`internal/proc`（含 #6）根本沒排進加壓範圍**，同樣是未涵蓋而非未重現。因此六條在真實負載下是否偽陽，目前**沒有完整實測基礎**；B1a-4 的整合負載跑批工時（4.0-6.0 hr）係依套件規模與既有測試數量推算，**非計時結果**。另：六條診斷 mutation 的連帶清單**全部為 grep＋讀碼推論**，無一實跑全套件驗證，#1／#5 已知鑑別力不唯一——依 owner 裁示，acceptance table 一律先標「潛在連帶」，實際清單須於各施工票的 design gate 於 mutation 套用期間取得（還原後無法補證，故不得移入 B1a-4）。
  - **`internal/proc` 候選（#6）已實測確認為測試側 bug**：`proc_test.go` 的 `deadline` 變數在等 pid 檔案的迴圈設定後，被第二段孫程序存活探測迴圈重用而未重設，中間隔著上限 30s 的 `<-done` 等待，stale deadline 使該迴圈可能執行 0 次；mutation 紅燈後 `ps` 確認孫程序實際已被殺掉、無殘留，證實非 production 缺陷。
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
- **驗收條件**：(1) repo 內可執行的 browser E2E suite，覆蓋 Gate 1、Gate 2、STALE、session recovery、approval 核心流；(2) **CI provider 邊界（rev4 凍結）**：required CI 路徑一律使用 **deterministic fake／replay provider**——不需訂閱帳號、不需外部網路，任意第三方 checkout 可重跑；live Claude／Codex 的驗收另列為獨立項目，**不作 required check**；(3) 納入 B2 的 CI（可為 non-required 起步）。
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

### B6a gate 單一寫入者＋Gate 3 policy／manifest seams（rev6 拆自 B6）

- **背景**（原 B6）：`app.go` 已達 423,700 bytes，application orchestration 高度集中，與內部文件「輕量接線層」宣稱落差確認；但全面重構會混入大量既有 concurrency 風險——僅抽 M4 會觸碰的部分。拆票依「>2.0 pt 必拆」規則（B6 plan rev3 重算 2.05 pt），owner 於 B6 plan gate 第三輪核准。
- **HEAD 證據**：同原 B6；plan＝`docs/superpowers/plans/2026-08-28-b6-m4-application-seams.md`（Task 1-6b＋10a）。
- **驗收條件**：(1) Forge port（型別＋interface＋fake）；(2) gate journal 單一寫入者——讀面七入口 detect-only 遷移＋寫面唯一 Submit 呼叫點入 workflowMu；(3) `gate3_promotion` policy 骨架＋manifest 收斂＋pending 終態封閉（Expired／TerminalCause）；(4) Wails binding 留在 `app.go`、不新增 M4 domain mutable state 或 ad-hoc test hook 於 App aggregate、新測試注入走 interface／fake；(5) 既有測試全綠含 race。**獨立結案**——不待 B6b；若本票為兩票中後完成者，收尾時確認原 B6 aggregate 關閉。
- **建議模組**：`app.go`、`internal/forge`（新）、`internal/gate`、`internal/gatepolicy`。
- **依賴／裁決**：僅依賴 B5 spec；裁決已定（#8＋拆票裁決 2026-08-28）。

### B6b 綁定持久化＋freeze latch seams（rev6 拆自 B6）

- **背景**：同 B6a 拆票脈絡；承載 B5 §3.1／§4.2(2)(3) 的既有 store 與 admission orchestration seams。
- **HEAD 證據**：同原 B6；plan 同上（Task 7-9＋10b）。
- **驗收條件**：(1) `wsregistry.Entry` TaskRun 綁定欄位＋雙向 1:1 write-once＋persistOrRollback failure matrix；(2) freeze latch——`FreezeTurns`（兩種 turn admission 檢查）＋approval 旗標＋雙鎖同持設定端＋鎖不洩漏；(3) 同 B6a 驗收 (4)(5)。**獨立結案**；原 B6 aggregate 於兩票皆完成時關閉、由後完成之票確認（不固定綁在本票）。
- **建議模組**：`app.go`、`internal/wsregistry`、`internal/appcore`。
- **依賴／裁決**：僅依賴 B5 spec（B6a→B6b 為建議執行順序、**非技術相依**）；裁決同上。

---

## 軌道 C：M4 最小垂直切片

### C1 Implementation-to-Gate-3 垂直切片

- **背景**：先證明 M4 **新增**的治理迴路（核准上下文綁定 → PR → Gate 3）端到端閉合，再抽象多 provider／多 forge。**範圍限縮聲明（rev2）**：本票起點是「已選定 task」，不含 spec／plan authoring 與 Gate 1／Gate 2／TCA 流程（該些為 M2／M3 已驗收資產）；因此**本票完成不得宣稱滿足完整 SC4**——「app 內從 authoring 到 Gate 3 全程不離開」的完整驗收，須待 A1 完成後另立驗收項（見依賴欄）。選擇限縮而非前移起點的理由：垂直切片的價值在最小範圍證明新迴路，authoring 閉環是獨立缺口（A1），混入會讓失敗訊號無法歸因。
- **HEAD 證據**：B5 所列（enforcement 缺口為規劃中的 M4 範圍，非現況缺陷）。
- **驗收條件**：以**一個 provider＋GitHub＋一個 task＋一套 required checks**跑通——選定 task → 建立不可變 TaskRun snapshot → 注入核准 spec／plan／risk／permissions／TCA → 啟動 implementation session → 收集 diff 與測試證據 → 建立 PR → 讀取 required checks → Gate 3 人工決議；此段全程不離開 app；TaskRun 能回答「這段實作在什麼核准上下文中完成」。**Forge 回傳 eligible CR 不得經篩除後假綠（rev7 新增，B6 Task 5 施工事實核對對應）**：C1 決議時必須自 Forge 重讀完整 review 集合並查齊 permissions；針對「Forge 實際回傳了某具效力 reviewer 的 `CHANGES_REQUESTED`」的情形，驗收須證明該筆不會被篩除而得到假綠——`VerifyReviewSection`（B6 Task 5）依設計無法偵測 caller 整筆刪除某 reviewer 的 review（見該函式 doc-comment 之範圍聲明），完整性責任在 C1 的重讀重建路徑，非 Task 5 的 section-level 驗證。
- **建議模組**：依 B5／B6 產出的 application service、forge adapter（GitHub）、frontend Gate 3 檢視。
- **依賴／裁決**：依賴 **B1、B2、B3a、B3b、B4、B5、B6a、B6b**（rev3 明列；rev6 依拆票改 B6→B6a＋B6b）；完整 SC4 驗收由 **C2** 承載（rev3 新增，不再是「完成後另開」）；forge 裁決已定（#4）；**pilot provider 已裁決（owner 2026-08-27）：Claude**——現有實機證據已跑過 Bash 核可並實際寫檔，per-WSID 子行程是較自然的 TaskRun ownership 邊界；Codex 的共用 app-server generation 會為第一條垂直切片多帶一層共享生命週期複雜度。

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

## 附錄 A：外部審核主張的核實記錄（2026-08-27，盤點基準 `9be0f4d`）

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

## 估點（rev5 制定、rev6 更新 B6 拆票、rev8 更新 B1a 拆四票重估；A／B／C 軌；0.1 pt＝1 hr 實際工作，不含等候）

拆票依規則執行（>2.0 pt 或混合性質必拆）；「假設」欄記載估點成立前提，前提破壞時該票重估。等候時間（design gate 往返、CI runner 排隊）不計工時、拉長日曆時間，已標注於備註。

| 票 | 拆分範圍 | pt | 假設／備註 |
|---|---|---|---|
| A1a | Editor 同步閉環：updateListener→受控 buffer、dirty state、Spec 手動儲存、digest optimistic locking、真實輸入 integration test（A1 驗收 1-4＋6） | 1.4 | 沿用既有 SpecWrite 類 bindings 的 digest 機制，不新增 backend surface |
| A1b | 外部檔案變更 reload／compare／保留流程（A1 驗收 5）＋對應測試 | 0.6 | 以 on-focus／操作前檢查實作，不引入 file watcher；compare 以並列顯示起步 |
| A2 | Error lifecycle 三原則＋回歸測試 | 0.4 | 範圍限 plan store＋同型檢視 spec store，不動 escalation 機制 |
| A3 | 文件版本同步＋frozen／living 原則＋同型掃描 | 0.2 | — |
| A4 | 1.25／1.26.5 比較矩陣＋go.mod toolchain＋README | 0.4 | 兩腳全套執行屬監督型等候（約 1 hr 內），計入；建議 B1a 後執行 |
| B1a-1 | #1 `TestAppServerTerminateKillsGroup`——`Proc` 未匯出 timer／signal-event seam＋(a) codex 端契約改寫＋(b) `internal/proc` 新增 deterministic white-box 測試 | **1.13**（0.90-1.35） | **已完成（2026-09-03，Gate 3 APPROVED；plan `efdc82c`、implementation `82caf8b`＋`f7ad1ed`）**。原估點維持不變作為歷史記錄。理解 1.0-1.5／production seam 1.5-2.5／測試改寫 2.5-3.5／mutation 2.5-3.5／獨立 design gate＋implementation review 1.5-2.5。**(a)(b) 必須同票**——兩者共同界定同一個 `Proc.Terminate()` seam，拆開會使 production 修改與契約驗證失去原子性。負載驗證移入 B1a-4 |
| B1a-2 | #2 `TestClaudeAssistFailsLoudOnOversizedLine`、#3 `TestMultiTurnSendAndTurnBoundaries`、#4 `TestInFlightTurnDoesNotBlockNewSession`——非 process 類純測試確定性化 | **1.01**（0.81-1.21） | **已完成**（2026-09-04 Gate B APPROVED；plan `6dd8edf`、implementation `7b1bb0c`＋`05069e2`＋`b0a8404`）。production 零改動已由 range diff 機械確認。負載驗證仍在 B1a-4 |
| B1a-3 | #5 `TestAppServerMidStreamDeath`、#6 `TestOutputCancellationKillsGrandchildren`——process lifecycle 純測試修正 | **0.66**（0.51-0.80） | **未完成**。production 零改動。#5 鑑別力不唯一（`Server.Handshake` 被同檔三條測試共用），mutation 連帶清單須實跑取得；#6 為一行 `deadline` 重設，工時集中在 mutation 驗收而非修改本身。負載驗證移入 B1a-4 |
| B1a-4 | B1a 整合驗收——三張施工票完成後的整合負載跑批、§7 追加 resolved 證據、living 有效名單文件、aggregate closure review | **0.95**（0.75-1.15） | **未完成**。整合負載跑批 4.0-6.0（對整合 HEAD 一次跑完 root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc` 矩陣；六條原分列合計 9.0-13.5 hr，**此為唯一保留的共用**，省約 5.0-7.5 hr——**推估值，非實測**，見下方證據缺口）／§7 追加證據 1.0-1.5／living 文件 1.5-2.5／closure review 1.0-1.5。§7 與 living 文件集中於本票，避免三張施工票同時改同一份文件 |
| B1b | 前端兩條候選重現與處置＋living 有效名單文件 | 0.3 | — |
| B2 | 最小 CI workflows＋ruleset＋enforcement 實證五項＋政策文件 | 1.2 | macOS runner 可用；CI 迭代的 runner 等候不計工時（日曆另計）；測試 PR 驗畢即清 |
| B3a-1 | E2E 基建：Playwright＋deterministic fake／replay provider harness＋1 條 smoke flow | 1.0 | 混合性質拆分（基建屬 spike 性質）；假設可沿用既有 Go 測試 double 思路造 fake provider CLI |
| B3a-2 | 其餘核心流覆蓋（Gate 1／Gate 2／STALE／recovery／approval） | 0.9 | 依 B3a-1 harness；每流平均 1.5-2 hr |
| B3b | 最小 native smoke 自動化 | 0.5 | 依 B2 macOS build job |
| B4 | Workspace 確認閘＋高風險警示＋fallback 測試 | 0.6 | — |
| B5 | TaskRun／Gate 3／forge 契約 spec（authoring＋gate 修訂輪工作量） | 1.2 | design gate 往返等候不計工時；(6c)(6d)(6e) owner 決議於 gate 中取得 |
| B6a | gate 單一寫入者＋Gate 3 policy／manifest seams（rev6 拆） | 1.45 | B6 plan rev3 bottom-up 重估（原 1.4 pt 前提破壞作廢）；plan Task 1-6b＋10a |
| B6b | 綁定持久化＋freeze latch seams（rev6 拆） | 0.6 | 同上；plan Task 7-9＋10b；B6a→B6b 為建議順序非相依 |
| C1a | TaskRun snapshot（domain＋持久化）＋Claude session 自動綁定注入 | 1.2 | pilot=Claude（已裁決）；沿 per-WSID ownership 既有機制 |
| C1b | GitHub forge adapter：PR 建立＋required-check 讀取 | 0.8 | 唯讀＋建立 PR 的最小 API 面；不含 merge queue |
| C1c | Gate 3 決議面（UI＋決議＋evidence 顯示）＋切片串接走查 | 1.2 | 沿既有 gate UI 慣例 |
| C2 | SC4 authoring-to-Gate-3 端到端驗收走查 | 0.3 | 驗收性質；依賴 A1a／A1b／C1a-c 全部完成 |

**小計（rev8 更新——B1a 拆四票重估）**：A 軌 3.0 pt（30 hr）｜B 軌 **11.49 pt（114.9 hr）**｜C 軌 3.5 pt（35 hr）｜**合計 17.99 pt（約 179.9 hr 實際工作）**。排程換算（團隊 throughput 約 0.6 pt／day）為另一維度，於排程時另算，不與工程量混用。

**B1a 進度（rev10）**：**B1a-1 與 B1a-2 皆已完成並關票**——B1a-1（2026-09-03 Gate 3 APPROVED；plan `efdc82c`、implementation `82caf8b`＋`f7ad1ed`）、B1a-2（2026-09-04 Gate B APPROVED；plan `6dd8edf`、implementation `7b1bb0c`＋`05069e2`＋`b0a8404`）。**B1a-3、B1a-4 仍未完成，B1a aggregate 尚未關閉。** 下列項目一律仍由 **B1a-4** 承擔，B1a-1／B1a-2 的完成**不代表**其中任何一項已達成：(i) 對整合 HEAD 一次跑完的五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）——**注意該矩陣包含 B1a-1 的 `internal/codex`／`internal/proc` 與 B1a-2 的 root／`internal/assist`／`internal/claude`，B1a-4 須於整合 HEAD 重跑，不得以任一施工票的 focused 結果取代**；(ii) §7 追加 resolved 證據；(iii) living 有效名單文件；(iv) aggregate closure review。本次狀態更新**不提前修改 §7 或 living 文件**。

**單位與捨入規則（rev8 新增，避免兩組答案）**：**hr 為權威單位，pt 一律由 hr ÷ 10 衍生**。估點表各票的 pt 為顯示用值、四捨五入至小數點後兩位；**合計一律以未捨入的 hr 總和換算，不得由各票顯示值相加**。B1a 四票中位 hr 為 11.25／10.10／6.55／9.50，**總和 37.40 hr → 3.74 pt**；若改以四個顯示值（1.13＋1.01＋0.66＋0.95）相加則得 3.75 pt，該 0.01 pt 差額純為捨入誤差、**非另一個估點**。B 軌與合計均採前者（9.25 − 1.5 ＋ 3.74 = 11.49；15.75 − 1.5 ＋ 3.74 = 17.99）。逐條原始數字見附錄 C。

**B1a 估點膨脹的歸因（rev8）**：1.5 pt→3.74 pt 的差額主要**不是**範圍蔓延，而是治理要求演進。原估訂於 rev5（2026-08-27），而 §6.7 的 mutation acceptance N/N 全跑門檻於治理文件 v2.2（`09751d9`，2026-09-01）才寫入、變異目標規則 v2.3（2026-09-02）；owner 另於 2026-09-03 裁示「design gate 須在 mutation 套用期間實跑受影響套件取得實際連帶清單」。重估表中 mutation 與負載驗證兩個維度即佔總數一半以上。**A 軌與 B 軌其他票的估點同樣訂於 rev5、同樣早於 §6.7**，若其驗收將列有限編號的 mutation table，會有同型低估——**本輪不重估它們**，依 owner 裁示於各票進入 plan gate、且實際建立 mutation table 時，再依當時的 §6.7 重新檢查原估點前提。

## 附錄 C：B1a 逐條 bottom-up 重估原始資料（rev8，證據綁 `aa55413`）

保留三輪 preflight 後的完整重估輸入，供未來 session 由 commit 重建估點。單位 hr（低-高）；pt 由 hr ÷ 10 衍生，捨入規則見估點段。

**六條 × 五維度**（`production seam` 為 0 者即純測試修正）：

| 條目 | 理解 | production seam | 測試改寫 | mutation | 負載驗證 | 小計 | 中位 |
|---|---|---|---|---|---|---|---|
| #1 `TestAppServerTerminateKillsGroup` | 1.0-1.5 | 1.5-2.5 | 2.5-3.5 | 2.5-3.5 | 2.0-3.0 | 9.5-14.0 | 11.75 |
| #2 `TestClaudeAssistFailsLoudOnOversizedLine` | 0.3-0.5 | 0 | 0.3-0.5 | 1.0-1.5 | 1.0-1.5 | 2.6-4.0 | 3.30 |
| #3 `TestMultiTurnSendAndTurnBoundaries` | 0.3-0.5 | 0 | 0.2-0.3 | 1.0-1.5 | 1.0-1.5 | 2.5-3.8 | 3.15 |
| #4 `TestInFlightTurnDoesNotBlockNewSession` | 0.5-0.8 | 0 | 1.5-2.0 | 1.5-2.0 | 2.0-3.0 | 5.5-7.8 | 6.65 |
| #5 `TestAppServerMidStreamDeath` | 0.3-0.5 | 0 | 0.3-0.5 | 1.5-2.5 | 1.0-1.5 | 3.1-5.0 | 4.05 |
| #6 `TestOutputCancellationKillsGrandchildren` | 0.3 | 0 | 0.2 | 1.0-1.5 | 2.0-3.0 | 3.5-5.0 | 4.25 |
| **六條合計** | | | | | | **26.7-39.6** | **33.15** |

**逐條依據錨點**（施工時據此重建判斷，不必重跑 preflight）：

- **#1**：`proc.go:243-276`（`Terminate` 逾時升級）、`fake-codex-appserver.sh:6`（leader 不 trap TERM）、`proc_test.go:143-190`（可重用的 pidfile-script pattern）、`proc_test.go:297`／`:380`（部分欄位建構 `&Proc{}`，新 seam 須 nil-safe）。mutation 偏高係因鑑別力不唯一、須實跑 `internal/codex`＋`internal/proc` 取得連帶清單。
- **#2**：`oneshot.go:128-174` 已為 select-on-channel，僅測試側 15s 邊際問題；鑑別力唯一（全 repo 僅此測試建構 `assist.claudeAssist`）。
- **#3**：`session.go:99-117` pump goroutine 已為事件驅動；修法直接沿用 `app_test.go:76-88` 既有先例（5s→15s＋註解），測試改寫為六條最低。
- **#4**：`app.go:9144`（`newSession`）→ `app.go:7560-7594`（`claudeTeardown`）→ `appcore.CloseSequence(..., a.after())`；`newTestApp`（`app_test.go:130-149`）未設 `afterFn`。測試改寫較 #2／#3 高，因需非同步化 `NewSession` 呼叫並以 `newFakeAfter()`／`waitForOutstanding`／`fireAll`（`app_shutdown_multi_test.go:141-176`，同屬 `package main` 可直接重用）驅動兩段 timer。mutation 偏高係因 root package 大、連帶收斂費時。
- **#5**：`Server.Handshake` 為同檔三條測試共用（`:36`／`:58`／`:91`），mutation 連帶清單須實跑取得。
- **#6**：`proc_test.go:163` 設定的 `deadline` 被 `:182` 第二段迴圈重用而未重設，中間隔著 `:176-180` 上限 30s 的 `<-done`。修改本身為一行；負載驗證偏高係因該套件含高重複次數測試（`TestNaturalSignalDeathDuringCancelIsNotCancellation` 200 次、`TestLateCancelAfterNaturalExitNeverMarksCanceled` 2000 次），且從未納入任何加壓輪。

**共通工時原始拆解**：§7 追加 resolved 證據 1.0-1.5／living 有效名單文件 1.5-2.5（repo 內 grep 無既有 wall-clock living 文件，須從零建立）／整票 review 往返 2.0-4.0。合計 4.5-8.0（中位 6.25）。

**拆票分攤**（owner 2026-09-03 裁定三條共用規則）：

1. **共同 PR／共同 review 折扣作廢**——每張施工票須有獨立 design gate、mutation N/N 與 CR，各計 1.5-2.5 hr；原共通的「整票 review 往返 2.0-4.0」由此取代。
2. **負載驗證保留共用**——六條原分列合計 9.0-13.5 hr，收攏為 B1a-4 對整合 HEAD 一次跑完 root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc` 矩陣，計 4.0-6.0 hr（**推估值，非計時結果**）。
3. **mutation 連帶清單不得移入 B1a-4**——還原後無法補證當時的實際連帶，故留在各施工票。§7 與 living 文件則集中於 B1a-4，避免三張施工票同時改同一份文件。

| 票 | 組成 | 低 | 中位 | 高 | pt 區間 |
|---|---|---|---|---|---|
| B1a-1 | #1 四維度（扣負載）7.5-11.0 ＋ gate／review 1.5-2.5 | 9.00 | 11.25 | 13.50 | 0.90-1.35 |
| B1a-2 | #2＋#3＋#4 四維度（扣負載）6.6-9.6 ＋ gate／review 1.5-2.5 | 8.10 | 10.10 | 12.10 | 0.81-1.21 |
| B1a-3 | #5＋#6 四維度（扣負載）3.6-5.5 ＋ gate／review 1.5-2.5 | 5.10 | 6.55 | 8.00 | 0.51-0.80 |
| B1a-4 | 整合負載 4.0-6.0 ＋ §7 1.0-1.5 ＋ living 1.5-2.5 ＋ closure review 1.0-1.5 | 7.50 | 9.50 | 11.50 | 0.75-1.15 |
| **合計** | | **29.70** | **37.40** | **45.10** | **2.97-4.51** |

**四票高估端均低於 2.0 pt**，故三張施工票＋一張整合票的切分成立，不需再拆。拆票代價（失去跨票共用跑批與多出兩輪 gate／review）已含在上表，未另行折扣。

---

## 修訂記錄

- rev10（2026-09-04，B1a-2 實作完成並關票）：純狀態更新，**估點與票面範圍不變**。B1a-2（#2 `TestClaudeAssistFailsLoudOnOversizedLine`、#3 `TestMultiTurnSendAndTurnBoundaries`、#4 `TestInFlightTurnDoesNotBlockNewSession`）經 design gate 三輪 owner CHANGES_REQUIRED 後 APPROVED，plan 提交為 `6dd8edf`（rev5）；implementation 為 `7b1bb0c`（`internal/assist` 移除 oversized-line fixture 的 `tr` 轉換）、`05069e2`（`internal/claude` `waitResult` 局部 deadline 5s→15s）、`b0a8404`（root package #4 接上 `afterFn`／`newFakeAfter()` 並加 `totalCreated()` 接線鑑別斷言）。**production 零變更**已由 range diff 機械確認（`a5a3cab..b0a8404` 只含 plan 文件＋三個測試檔；`internal/assist/oneshot.go`／`internal/claude/session.go`／`internal/appcore/pump.go`／`app.go`／`testdata/fake-claude.sh` 零異動）。**§6.7 的處置須註明**：本票 production 零變更，無法套用 §6.7 的 production-target mutation 原文，owner 核准**僅限 B1a-2** 的例外——以測試側 negative control（3/3：2a／3a／4a，三項全紅在測試自身斷言）代替，並保留 N/N 全跑、hash 前後比對、byte-identical 還原與完整回歸強度；另跑 3a 的 positive control 3b（同一 6 秒 fake CLI 延遲下 15 秒版本 PASS 18.05s），證明常數變更確實改變鑑別力。關票證據：三個測試檔的 golden SHA-256 由 owner 從基準 `a5a3cab` 建立全新 worktree 逐字重建後精確命中，Gate A→Gate B 的四項轉移條件全數成立；驗證分層（避免虛增證據強度）：**(i) Gate B 於主 repo** 跑完整三包 `-race`（`internal/assist` 20／`internal/claude` 22／root 422 PASS＋1 既有 env-gated SKIP＝423，0 FAIL；**executor 與 reviewer 各跑過一次、結果一致**——此重跑次數僅適用於三包 `-race`）；另於 Gate B 確認 `go build ./...`／`go vet ./...`／`gofmt -l`／`git diff --check` 全數乾淨；**(ii) owner 的關票 review 獨立重跑的是三條受影響的 focused `-race`**（非完整三包），另核對三個 golden hash、兩條 range diff 與 production 零異動；**(iii) Gate A 的完整三包結果屬執行者證據**，不得改稱 reviewer 的獨立複驗。**已知缺口（不得宣稱消除）**：#3 的 CI runner 冷啟動分布未取得（383ms 為本機唯一冷啟動樣本，缺口留給 B1a-4）；#2 本輪未重現過誤紅，只量得約 9 倍餘裕；#4 注入 fake timer 後真正的 pump 卡死會落到 `go test -timeout`，失去原本 5 秒的局部快速失敗。**B1a-3／B1a-4 仍未完成，B1a aggregate 未關閉**；五套件整合負載矩陣、§7 resolved 證據、living 有效名單與 aggregate closure review 仍全數由 B1a-4 承擔，本 rev 不提前修改 §7 或 living 文件。
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
- rev4（2026-08-27，backlog review 第三輪 2 P1＋1 P2 收斂＋owner 兩項裁決落地）：
  - P1：B3a 凍結 CI provider 邊界——required CI 路徑一律 deterministic fake／replay
    （免訂閱、免外網、第三方可重跑）；live provider 驗收另列、不作 required check。
  - P1：估點範圍凍結——本輪只估 A／B／C 軌；D 軌各票受 owner scope／宣稱／政策／
    立項／上限決策影響，待立項後估。
  - P2：`9be0f4d` 改稱「盤點基準」（rev1 起始時之 main），不再稱 HEAD——修訂
    commit 不改變盤點內容之基準，避免稽核事實失真。
  - 裁決落地：A4 採 `toolchain go1.26.5`，1.25 比較腳強制記錄 `go version`＋
    `GOTOOLCHAIN=local`（現環境 auto 會自動切換，不設即證據無效）；C1 pilot
    provider 選 **Claude**（實機證據既有、per-WSID ownership 邊界自然；Codex 共用
    app-server generation 增加共享生命週期複雜度）。
- rev5（2026-08-27，估點版——rev4 通過 review 後依裁決進入估點）：
  - 加入 A／B／C 軌逐票估點表（合計 15.1 pt）；依「>2.0 pt 或混合必拆」拆
    A1→A1a/A1b、B1→B1a/B1b、B3a→B3a-1/B3a-2、C1→C1a/C1b/C1c；假設欄記載
    前提，破壞即重估。
  - A4 措辭校正（owner 核可於估點 commit 順手修）：GOTOOLCHAIN 自動切換風險
    發生在加入 toolchain directive 的受測 commit，非現行 go.mod。
- rev6（2026-08-28，B6 拆票）：B6 plan gate 第三輪 owner 裁決落地——B6 拆 **B6a**（gate 單一寫入者＋Gate 3 policy／manifest，1.45 pt）／**B6b**（綁定持久化＋freeze latch，0.6 pt）；拆票理由＝plan rev3 bottom-up 重估 2.05 pt 逾 2.0 拆票線（原 1.4 pt「僅 service 骨架」前提破壞）。兩票皆僅依賴 B5、**各自獨立結案**；B6a→B6b 為建議順序非技術相依；**原 B6 aggregate 於兩票皆完成時關閉、由後完成之票確認**（不固定綁在 B6b）。C1 相依 B6→B6a＋B6b；B 軌小計 8.6→9.25、合計 15.1→15.75 pt。
- rev7（2026-08-31，C1 驗收條件補案例）：B6 Task 5（review section 收斂）尚未實作前的施工事實核對發現 `VerifyReviewSection` 依設計無法偵測 caller 整筆刪除某具效力 reviewer 的 review（完整性責任在 C1），owner 裁示於 **C1 Implementation-to-Gate-3 垂直切片**的驗收條件補「Forge 回傳 eligible CR 不得經篩除後假綠」案例——C1 決議時須自 Forge 重讀完整 review 集合並查齊 permissions，驗收須證明具效力 CR 不會被篩除得到假綠。僅動 C1 該列驗收條件敘述，其餘票面／估點不變。
- rev9（2026-09-03，B1a-1 實作完成並關票）：純狀態更新，**估點與票面範圍不變**。B1a-1（#1 `TestAppServerTerminateKillsGroup` 根因修復）經 design gate 四輪（rev1 初稿 → 審查者修正三處 → owner CHANGES_REQUIRED 五 P1＋一 P2 → owner 第二輪 CHANGES_REQUIRED 兩項文件契約）後 APPROVED，plan 提交為 `efdc82c`；implementation 為 `82caf8b`（`internal/proc` timer／signal-event seam＋`killAndReap` helper＋三條白箱測試）與 `f7ad1ed`（`internal/codex` 測試改寫）。關票證據：`internal/proc/proc.go` 的 SHA-256 與 design gate 第二輪落地驗證的 golden hash 精確相同，故該輪 mutation 鑑別表 5/5（每項四步、每步實跑 `internal/proc`＋`internal/codex` 兩包）之證據可轉移，無須重做植入；owner 獨立複驗兩包 `-race`（頂層 18／47 全數 PASS）、五條點名回歸測試、`go vet`／`go build ./...`／`gofmt -l`／`git diff --check` 全數乾淨、程序殘留精確檢查為零。rev8 記載的三項邊界於實作中全部落實——新 seam 維持未匯出且 nil 時退回 real timer 與 no-op observer、七個呼叫端與 `Config` 匯出面零變更、`termSent`／`killSent`／`fatalSig` 死因仲裁語意未動。**B1a-2／B1a-3／B1a-4 仍未完成，B1a aggregate 未關閉**；五套件整合負載矩陣、§7 resolved 證據、living 有效名單與 aggregate closure review 仍全數由 B1a-4 承擔，本 rev 不提前修改 §7 或 living 文件。
- rev8（2026-09-03，B1a preflight 實測落地＋拆四票重估）：**三輪** preflight（第一輪 read-only 重現與讀碼分析，第二輪於隔離 worktree 逐條套用診斷 mutation 取得根因證據，第三輪於隔離 worktree 診斷 qErr 跨代影響面；主工作區全程零異動，證據全數綁 `aa55413`）產出票面修正，owner 逐項裁示——(1) **事實修正**：`TestAppServerTerminateKillsGroup` **未進入 escalation 分支**（`FAKE_ORPHAN` 只讓孫程序 trap TERM、leader 自身不 trap，`Terminate()` 的 `select` 永遠走 `<-p.exitedCh`），依原假設對 grace timer 套 mutation 實測維持綠燈，改標的到 supervisor 收尾管線才紅在自身斷言；後續 plan 不得保留「驗得不夠確定」的前提。(2) **#1 判類 2 不拆票但邊界釘死**（未匯出 seam、white-box 注入、事件須區分 Terminate 升級與 supervisor 收尾；觸及公開 API／七個呼叫端／死因仲裁即升類 3 停工拆票），拆成 codex 端與 `internal/proc` 端兩條不同契約的測試、移除 5s 效能斷言；另修正證據邊界——`proc_test.go:380` 非裸 `&Proc{}` 且不會進入 escalation goroutine，證明不了 nil 安全性，plan 須硬性要求新 seam 在 nil 時退回 real timer 與 no-op observer。(3) **#4 只接線不改語意**，qErr 經實測非跨代污染，既有錯誤契約由 `internal/appcore/manager_test.go:1052-1067` 釘死。(4) **#6 確認為測試側 stale deadline bug**，非 production 缺陷。(5) **估點逐條 bottom-up 重估**（禁用平均係數，五維度分列、逐條給區間、最後由 hr 換算 pt），中位 3.74 pt 逾 2.0 門檻，**拆為 B1a-1／B1a-2／B1a-3 三張施工票＋B1a-4 整合驗收票**，四票高估端均低於 2.0 pt。拆票時共同 PR／共同 review 的省時折扣作廢（每張施工票須有獨立 design gate、mutation N/N 與 CR），**mutation 連帶清單不得移入 B1a-4**（還原後無法補證當時的實際連帶），僅整合負載跑批與文件收斂保留共用。#1／#5 連帶仍僅為「潛在連帶」，須於各施工票的 design gate 實跑取得實際清單。B 軌小計 9.25→11.49、合計 15.75→17.99 pt。
- rev1（2026-08-27）：初版。
