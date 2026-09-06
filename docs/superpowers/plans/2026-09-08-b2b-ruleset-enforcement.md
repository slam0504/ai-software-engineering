# B2b main ruleset、enforcement 實證、`ci-merge-policy.md`、CI 冷啟動量測 Implementation Plan

> **For agentic workers:** 本票的外部寫入（ruleset 建立／修改／刪除、repo 設定 PATCH、push、開／關 PR、merge、刪分支）**每一步逐次由 owner 授權**，並抄錄前後狀態（ruleset 與 repo 設定以完整 JSON）。Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev2（2026-09-08，design gate 第一輪 CHANGES_REQUIRED 後修訂：D3 probe 改為三個獨立狀態（A 錯 checksum／B 只改 job 名且 checksum 已恢復／C 與 base 相同），缺席判準改為「required 清單 vs 該 HEAD 實際 check-runs」並保存 GitHub 實際回應；移除 Task 1 的 dry-run 拒絕說法，改為「有效規則已核對、未實測 direct-push 拒絕」，並要求完整 ruleset GET（enforcement／conditions／bypass_actors）而非只看 branch rules；D4 補 required-check 改名可執行順序、ruleset 誤刪重建、暫停後恢復失敗處置、「無 bypass 不等於 admin 無法改設定」；D5／D6 固定五個樣本的識別欄位、失敗與排除保留、彙整 commit 不入樣本、不足五次回報缺口、Elapsed 與 job 耗時分開、冷啟動結論邊界；D7 順序改為 push A→開 PR→A 證據→push B→B 證據→push C→C 證據…，刪除 B2a 清理項，closure 合併授權含自動 main run；D8 合計補足為 7.0 hr；前版：rev1）
> 狀態：**待 owner 複核 rev2**。尚未變更任何 GitHub 設定、未開 PR、未 push；D1／D2 方向已通過，但建立 ruleset 與 PATCH 設定尚未授權。
> 票源：Pre-M4 Readiness Backlog **B2b**（rev16 拆自 B2，**0.7 pt**＝6.0–8.5 hr）：驗收條件 (2) main ruleset、(5a)–(5e) enforcement 實證、`ci-merge-policy.md`、(6) CI 冷啟動量測 n=5；承接 B2a plan rev7「B2b 承接事項」。
> 基準：`main`＝`origin/main`＝`1bbb47a`（B2a 關票，含 `ci.yml`）。分支 **`b2b/ruleset`**（本機，自 `1bbb47a`）放本 plan；enforcement 驗證用 **`ci-probe/2026-09-08-enforcement`**（驗完關 PR、刪分支、不合併）；政策文件、樣本與回寫用 **`b2b/closure`** PR（required checks 全綠後 rebase and merge）。
> 唯讀前置（2026-09-08）：`rulesets` 為 `[]`、`main` branch protection 404、repo public、`allow_merge_commit`／`squash`／`rebase` 皆 true、`allow_auto_merge` false、`delete_branch_on_merge` false；Actions `enabled`、`allowed_actions: all`、`sha_pinning_required: false`；main head `1bbb47a` check-runs 四個 context（`go`／`wails-build`／`frontend`／`checksums`，app `github-actions`／15368）；`ci.yml` 觸發 `pull_request`、`push: main`、`workflow_dispatch`；無 open PR。

**Goal:** 讓「required checks 全綠才能合併、禁止 direct push、線性歷史」在 GitHub 上實際生效並被實證：(2) 以 ruleset 保護 `main`；(5a) 紅燈 PR 不可合併、(5a′) required context 缺席時 PR 仍不可合併（fail loud）、(5b) 與 base 相同時 `CLEAN` 可合併；(5c)(5e) 政策文件明文化並由 automation plan §12 與 README 指向；(6) 以 n=5 clean PR run attempts 量測 #2／#3／#6／F1／F2 的 CI 耗時回寫 register。

**Architecture:** ruleset 一次建立、完整 JSON 前後快照留痕（含 name、id、enforcement、conditions、bypass_actors、rules）；probe 分支只做三個**彼此獨立**的 enforcement 狀態驗證，證據為 `gh pr view --json mergeStateStatus,mergeable,statusCheckRollup`＋`gh api .../commits/<head>/check-runs`＋`gh api .../rules/branches/main` 的逐字輸出；closure PR 提交 `ci-merge-policy.md`、automation plan §12 指標、README 一句與本 plan，其**前五次有效**的 pull_request run 為 D6 樣本；五個樣本固定後再做**一次**彙整 commit（register v8、backlog rev27、plan 回填），該次 CI 只作合併驗證。**ruleset 生效後，repo 既有的「docs-only 直接 push main」慣例終止**，所有變更走 PR＋required checks（每次約 6–8 分鐘）。

---

## owner 裁定（design gate 第一輪，rev2 回寫）與待複核項

- **D1（方向通過）**：main、`strict`、四 contexts／app 15368、無 bypass、PR 必須、0 approvals、線性歷史、禁 force-push／刪除；接受啟用後 docs-only 也走 PR。**0 approvals 不代表系統強制人工審查，owner 裁定仍靠流程執行**；**無 bypass actor 不等於 admin 無法修改設定**——admin 仍可改／刪 ruleset，只能由政策文件與 JSON 留痕約束（D4）。完整建立 payload（提交建立申請時逐字附上）：

  ```json
  {"name":"main-required-checks","target":"branch","enforcement":"active",
   "conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},
   "bypass_actors":[],
   "rules":[
     {"type":"deletion"},
     {"type":"non_fast_forward"},
     {"type":"required_linear_history"},
     {"type":"pull_request","parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false}},
     {"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[
       {"context":"go","integration_id":15368},{"context":"frontend","integration_id":15368},
       {"context":"wails-build","integration_id":15368},{"context":"checksums","integration_id":15368}]}}
   ]}
  ```
  建立後以 `GET rulesets/<id>` 核對 name／enforcement／conditions／bypass_actors／rules 完整內容，再以 `GET rules/branches/main` 核對**有效規則**；兩者皆保存。
- **D2（方向通過）**：repo 設定改為只允許 rebase merge（`allow_merge_commit: false`、`allow_squash_merge: false`、`allow_rebase_merge: true`），`delete_branch_on_merge` 維持 false（刪分支仍逐次授權）；PATCH 前後完整 JSON 保存。**外部 PATCH 尚未授權。**
- **D3（rev2 修正，待複核）**：三個獨立狀態，各自 push 一次、各自取證，**不合併 A／B 的驗證**：
  - **A**：只改 `docs/architecture/SHA256SUMS` 一筆 hash 最後一碼 → 預期實際 checks：`checksums` failure、其餘三 job success；`mergeStateStatus: BLOCKED`。
  - **B**：**先恢復 checksum 為正確**（與 base 相同），再把 `ci.yml` 的 `checksums` job `name:` 改為 `checksums-renamed`（其餘不變）→ 預期實際 checks：`go`／`frontend`／`wails-build`／`checksums-renamed` 全 success，**required 的 `checksums` 缺席**；`mergeStateStatus: BLOCKED`。
  - **C**：恢復原 job 名稱；workflow、checksum 與 base 完全相同（`git diff origin/main...C` 為空）→ 四個 required checks success，`mergeStateStatus: CLEAN`、`mergeable: MERGEABLE`。
  - **缺席判準**：以 `GET rules/branches/main` 取得 required 清單，與 `GET commits/<HEAD>/check-runs` 的實際 check 名稱比對；**不假設** `statusCheckRollup` 必定產生 expected／pending 項目，GitHub 實際回應逐字保存（若 rollup 有 expected 項目也一併記錄）。
  - probe PR 不合併；C 取證後關閉 PR、刪分支（各自授權）。
- **D4（rev2 補齊，待複核）**：`ci-merge-policy.md` 骨架——(i) 保護對象、ruleset name／id、建立 payload 與 GET 快照路徑；(ii) required contexts 權威清單與來源（B2a plan rev7，main `ee30055`／`19422bc` check-runs，app 15368）；(iii) direct push：禁止（含 admin），一律 PR；(iv) bypass：不設 bypass actor；**admin 仍能修改／刪除 ruleset，屬政策而非技術阻擋**，任何修改須以前後 JSON 留痕並走 PR 記錄；(v) 緊急例外：唯一途徑是 owner 以 API 暫時 `enforcement: disabled` → 執行 → **立即恢復並 GET 核對**；恢復失敗（GET 不符或 API 錯誤）→ **停止所有後續外部寫入並回報**，直到人工修復；僅限「CI 基礎設施本身故障」；(vi) **required-check 改名程序（不會卡住）**：新舊 job 並存 → PR 合併並在 main run 驗證新 context 出現 → 更新 ruleset required 清單（新增新名，暫留舊名）→ 另一 PR 移除舊 job → 再更新 ruleset 移除舊名；每步 GET 核對；(vii) **ruleset 誤刪重建**：以本文件保存的建立 payload 重新 POST → 記錄新 id → `GET rules/branches/main` 核對有效規則與四 contexts → 更新政策文件的 id；(viii) 紅燈處置指向 register 規則 1／7／8（不得重跑吸收）；(ix) 量測資料出處。
- **D5（rev2 固定取樣與收尾規則，待複核）**：樣本＝`b2b/closure` PR 上、ruleset 啟用後、`ci.yml` 未修改（workflow hash 與 main 相同）、四個 required contexts 皆出現的 pull_request run，**前五次有效者**；每次 push 產生不同 run ID（通常 attempt 1）。**保留所有失敗與排除**（含理由），不能只挑五次綠燈；**五次有效樣本固定後**，另做**一次**彙整 commit 回填 register v8／backlog rev27／plan，其 CI 作合併驗證，**不再計入樣本、不循環回填自身**。真實修訂不足五次 → 回報缺口，**不為湊數製造修訂、不 rerun**。
- **D6（rev2 補欄位與邊界，待複核）**：每樣本記 run ID／attempt、PR head／base SHA、`ci.yml` SHA-256、runner image 版本字串（四 job）、npm cache 狀態（hit／miss／未確認）、artifact id、四 job 起訖與 elapsed、五條精確測試名稱的 `Elapsed`／ms（`TestClaudeAssistFailsLoudOnOversizedLine`、`TestMultiTurnSendAndTurnBoundaries`、`TestOutputCancellationKillsGrandchildren`、`PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積`、`SpecWorkspace draft accept > discards spec-assist result if the file switches during the call`）。**缺值記「缺」不記 0**；個別測試 Elapsed 與 job 耗時**分開報告**；**未確認 cache miss 的樣本不稱為冷啟動**，結論只寫「n=5 clean PR attempts 的分布」。register v8：A 段 #2／#3／#6、B 段 F1／F2 補 CI 量測欄（min／median／max、樣本 run ID）、規則 5 標完成；backlog rev27 關票。
- **D7（rev2 順序，待複核）**：(1) `POST rulesets`（前 `[]`／後完整 JSON）；(2) `PATCH repos/...` merge 設定（前後 JSON）；(3) push A → (4) 開 probe PR → A 證據 → (5) push B → B 證據 → (6) push C → C 證據 → (7) 關 probe PR → (8) 刪 probe 分支；(9) push `b2b/closure` 首次（政策文件等）→ (10) 開 closure PR → 樣本 1；(11)…每次真實修訂 push → 樣本 2–5；(12) 彙整 commit push（不入樣本）；(13) rebase merge closure PR（**授權含自動 main run**，其結果依有限次回填原則只保存於 GitHub run 與結案回報）；(14) 刪 `b2b/closure` 分支。每步分開授權、前後狀態抄錄。（B2a 分支清理已完成，不列。）
- **D8（暫維持 0.7 pt）**：工程量——ruleset＋設定＋雙重 GET 核對 1.0 hr、probe 三狀態與證據 1.5 hr、政策文件（含維護程序）1.5 hr、closure PR 五樣本量測 1.5 hr、register v8／backlog rev27／plan 回寫 1.0 hr、**證據逐字抄錄與 JSON 前後快照整理 0.5 hr**，合計 **7.0 hr**；CI 等候不計。

---

## Global Constraints

- **零 production／測試變更**；`ci.yml` 於 main 不變（probe 分支的 B 修改不合併）。
- **外部寫入逐次授權**（D7）；每步以 `gh api`／`gh pr view --json` 抄錄前後狀態到 `/tmp/b2b-*`，關鍵 JSON 進 `ci-merge-policy.md` 附錄。
- **不 rerun、不 dispatch**；紅燈依 register 規則分類，不得重跑吸收；probe 與樣本的失敗一律保留。
- **設定證據 vs 阻擋實證分開**：ruleset 存在與有效規則以 GET 為證；「不可合併」以 probe PR 的 `mergeStateStatus` 為證；**direct push 被拒未實測**（不對 main 做任何推送嘗試，dry-run 亦不能證明），標為已知未實測。
- **fail loud 優先**：B 狀態若 GitHub 允許合併（`CLEAN`）即為 finding，停在 D 決策，不得繞過。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭；`gh` 輸出逐字抄錄。

---

## Task 1：ruleset 與 repo 設定（D1／D2；授權 (1)(2)）

- [ ] Step 0 唯讀快照：`GET rulesets`、`GET repos/...`（merge 旗標）、`GET rules/branches/main`、main head check-runs → `/tmp/b2b-t1/before.*.json`。
- [ ] Step 1（授權後）`POST rulesets`（D1 payload 逐字）→ 保存回應（id、created_at）→ `GET rulesets/<id>` 核對 name／enforcement／conditions／bypass_actors／rules 與 payload 一致 → `GET rules/branches/main` 核對有效規則含四個 required contexts、線性歷史、禁刪除／force-push、PR 必須。
- [ ] Step 2（授權後）`PATCH repos/...`（只允許 rebase merge）→ 前後 JSON。
- [ ] Step 3 標註：**有效規則已核對；direct-push 拒絕未實測**（不對 main 推送、不 dry-run）。

## Task 2：enforcement 實證（D3；授權 (3)–(8)）

- [ ] Step A：建 `ci-probe/2026-09-08-enforcement`（自 main），commit A（SHA256SUMS 一碼）→（授權）push → （授權）開 PR → 等 run → 保存 `gh pr view --json mergeStateStatus,mergeable,statusCheckRollup`、`GET commits/<A>/check-runs`、`GET rules/branches/main`；預期 `checksums` failure、BLOCKED。
- [ ] Step B：commit B（**恢復 SHA256SUMS 與 base 相同**＋`checksums` job 改名 `checksums-renamed`）→（授權）push → 等 run → 同組證據；預期實際 checks 全 success 但 required `checksums` 缺席、BLOCKED；記錄 rollup 是否出現 expected 項目（不預設）。
- [ ] Step C：commit C（恢復 job 名；`git diff origin/main...HEAD` 為空）→（授權）push → 等 run → 同組證據；預期四 required success、CLEAN／MERGEABLE。
- [ ] Step D：（授權）關閉 PR、（授權）刪分支；記錄 PR 編號與三次 run ID／HEAD。

## Task 3：政策文件與指標（D4）

- [ ] 新增 `docs/architecture/ci-merge-policy.md`（D4 (i)–(ix)；附錄：ruleset 建立 payload、GET 快照、probe 三狀態證據摘要）。
- [ ] automation plan §12 第 1 點補一句指向 `ci-merge-policy.md`；README「測試」段補一句「合併規則見 `docs/architecture/ci-merge-policy.md`」。
- [ ] 本機：純文件；用語掃描；`git diff --check`。

## Task 4：closure PR、D6 五樣本、彙整回寫（D5／D6；授權 (9)–(14)）

- [ ] 建 `b2b/closure`（自當時 main），首次 commit：政策文件、automation plan、README、本 plan →（授權）push →（授權）開 PR → 樣本 1（記 D6 欄位）。
- [ ] 後續每次**真實**修訂（例如政策文件依 probe 證據補附錄、plan 回填 Task 2 證據）各一次 push → 樣本 2–5；每次記 D6 欄位；失敗與排除保留；不足五次即回報缺口。
- [ ] 五樣本固定 → **一次**彙整 commit：register v8、backlog rev27、plan 最終回填 → push（不入樣本）→ required checks 全綠。
- [ ] （授權，含自動 main run）rebase merge closure PR → main run 結果只保存於 GitHub run 與結案回報 → （授權）刪 `b2b/closure`。

## 驗證策略

- 設定：`GET rulesets/<id>`＋`GET rules/branches/main` 前後快照。
- enforcement：三個獨立狀態的 PR JSON＋check-runs 逐字證據（BLOCKED／BLOCKED＋required 缺席／CLEAN）。
- 量測：五樣本原始 artifact（`go-test.json`／`vitest.out`）取值，缺值記缺；彙整 commit 的 CI 不入樣本。
- 已知未實測：direct push 被拒；GitHub 對缺席 required context 的實際呈現只能在 B 狀態觀察。

## Gate B（B2b 完成條件）

- [ ] ruleset 完整 GET 與有效規則皆符合 D1；repo merge 設定符合 D2；前後 JSON 留痕。
- [ ] A／B／C 三份獨立證據（BLOCKED／BLOCKED＋required 缺席／CLEAN）；probe PR 已關、分支已刪、未合併。
- [ ] `ci-merge-policy.md`（含維護程序 (iv)–(vii)）落地並由 automation plan §12 與 README 指向；closure PR 以 rebase merge 落地。
- [ ] D6 五樣本表（含失敗／排除紀錄）與 register v8、backlog rev27 落地；每步外部寫入皆有授權與前後狀態紀錄。

## 修訂記錄

- rev2（2026-09-08）：design gate 第一輪修正——D3 三個獨立狀態與缺席判準；移除 dry-run、要求完整 ruleset GET；D4 改名／重建／恢復失敗處置與「無 bypass ≠ admin 無法改設定」；D5／D6 取樣識別、失敗保留、彙整不入樣本、缺口回報、Elapsed 與 job 耗時分開、冷啟動邊界；D7 順序與清單；D8 合計 7.0 hr。
- rev1（2026-09-08）：建立；唯讀前置；D1–D8；四 Task；Gate B。
