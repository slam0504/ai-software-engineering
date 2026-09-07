# B2b main ruleset、enforcement 實證、`ci-merge-policy.md`、CI 冷啟動量測 Implementation Plan

> **For agentic workers:** 本票的外部寫入（ruleset 建立／修改／刪除、repo 設定 PATCH、push、開／關 PR、merge、刪分支）**每一步逐次由 owner 授權**，並抄錄前後狀態（ruleset 與 repo 設定以完整 JSON）。Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev6（2026-09-07，**design gate 修訂提案（範圍拆分）**：PR #3 文件已複核通過、正常情況不會再有修訂，D5「closure PR 前五次 run」的取樣設計無法自然產生樣本 3–5——owner 2026-09-07 指出這是取樣設計問題並建議拆分。本 rev：B2b 本票限治理與政策文件（ruleset、enforcement 實證、`ci-merge-policy.md`、closure PR 合併）；(6) 量測拆為獨立追蹤票 **B2b-2**，保留樣本 1／2，之後自正常開發 PR 的合格 run 累積；D5／D6 改寫樣本來源與**可比性條件（受測版本指紋）**；D7 (11)–(14) 調整；D8 重估（B2b 0.5＋B2b-2 0.3）；Gate B 重寫；backlog rev27 同步；**owner 複核修正（同 rev6）**：指紋改為保守的完整相關目錄（`internal`／`testdata`／`go.mod`／`go.sum`／`frontend`／`ci.yml`）、固定選樣順序與分組結論（每組標各自 n、不足五筆不完成、刪除例外語）、Goal／Architecture／D6 尾段／政策 §9／backlog 計算式同步。**待 owner 複核**）；前版：rev5（2026-09-07，一次合併回填：D7 (9) push `b2b/closure`＝`5a83717`、D7 (10) closure PR **#3** 與樣本 1（run `34075935919`，合格）；D6 樣本表建立，**目前 1/5，尚缺 4 次**；證據補正——D7 (9) 前置鎖定未硬性停止（揭露）、鎖定腳本改為失敗即 `exit 2` 並本機模擬驗證、`before.ls-remote` 0 bytes 與逾時原文另存、採集工具 image／cache 解析修正（`jobs-summary.v2.tsv`）與涵蓋 artifact 的完整 manifest；前版：rev4（2026-09-07，執行回填：Task 1 完成——ruleset `22394412` 建立、repo PATCH 為 rebase-only；Task 2 完成——PR #2 三狀態 A `eda9f25`／B `1c41d81`／C `b699d0b`，run `34047792759`／`34065893254`／`34066286258`，BLOCKED／BLOCKED（required `checksums` 缺席）／CLEAN，PR 已關、分支已刪、未合併；Task 3 本機落地 `ci-merge-policy.md` v1→v1.1（複核修正）、automation plan v2.5 §12、README 一句；owner 釐清 probe 耗時只作補充不入 n=5。註：rev1–rev3 標題日期寫為 2026-09-08，依 GitHub 時間戳實際日曆日為 2026-09-07，前版文字不改）；前版：rev3（2026-09-08，rev2 複核修正：D4 (vi) required-check 改名順序改為「新舊並存合併並確認新 context → required 加新名 → 先移除舊 required 並確認新名仍 required → 再以另一 PR 移除舊 job」；前版：rev2（2026-09-08，design gate 第一輪 CHANGES_REQUIRED 後修訂：D3 probe 改為三個獨立狀態（A 錯 checksum／B 只改 job 名且 checksum 已恢復／C 與 base 相同），缺席判準改為「required 清單 vs 該 HEAD 實際 check-runs」並保存 GitHub 實際回應；移除 Task 1 的 dry-run 拒絕說法，改為「有效規則已核對、未實測 direct-push 拒絕」，並要求完整 ruleset GET（enforcement／conditions／bypass_actors）而非只看 branch rules；D4 補 required-check 改名可執行順序、ruleset 誤刪重建、暫停後恢復失敗處置、「無 bypass 不等於 admin 無法改設定」；D5／D6 固定五個樣本的識別欄位、失敗與排除保留、彙整 commit 不入樣本、不足五次回報缺口、Elapsed 與 job 耗時分開、冷啟動結論邊界；D7 順序改為 push A→開 PR→A 證據→push B→B 證據→push C→C 證據…，刪除 B2a 清理項，closure 合併授權含自動 main run；D8 合計補足為 7.0 hr；前版：rev1）
> 狀態：**待 owner design gate 複核 rev6（僅範圍拆分、D5／D6 取樣條件、D7 (11)–(14)、D8、Gate B；D1–D4 與已完成的 Task 1–3 不變）**。D7 (1)–(10) 已完成；closure PR #3 OPEN、CLEAN、未合併；量測有效樣本 2/5（保留並移交 B2b-2）。rev6 通過前不合併、不關票、不製造修訂、不 rerun、不 dispatch。ruleset 啟用後 main 的一切變更（含 docs-only）走 PR＋required checks。
> 票源：Pre-M4 Readiness Backlog **B2b**（rev16 拆自 B2，**0.7 pt**＝6.0–8.5 hr）：驗收條件 (2) main ruleset、(5a)–(5e) enforcement 實證、`ci-merge-policy.md`、(6) CI 冷啟動量測 n=5；承接 B2a plan rev7「B2b 承接事項」。
> 基準：`main`＝`origin/main`＝`1bbb47a`（B2a 關票，含 `ci.yml`）。分支 **`b2b/ruleset`**（本機，自 `1bbb47a`）放本 plan；enforcement 驗證用 **`ci-probe/2026-09-08-enforcement`**（驗完關 PR、刪分支、不合併）；政策文件、樣本與回寫用 **`b2b/closure`** PR（required checks 全綠後 rebase and merge）。
> 唯讀前置（2026-09-08）：`rulesets` 為 `[]`、`main` branch protection 404、repo public、`allow_merge_commit`／`squash`／`rebase` 皆 true、`allow_auto_merge` false、`delete_branch_on_merge` false；Actions `enabled`、`allowed_actions: all`、`sha_pinning_required: false`；main head `1bbb47a` check-runs 四個 context（`go`／`wails-build`／`frontend`／`checksums`，app `github-actions`／15368）；`ci.yml` 觸發 `pull_request`、`push: main`、`workflow_dispatch`；無 open PR。

**Goal:** 讓「required checks 全綠才能合併、禁止 direct push、線性歷史」在 GitHub 上實際生效並被實證：(2) 以 ruleset 保護 `main`；(5a) 紅燈 PR 不可合併、(5a′) required context 缺席時 PR 仍不可合併（fail loud）、(5b) 與 base 相同時 `CLEAN` 可合併；(5c)(5e) 政策文件明文化並由 automation plan §12 與 README 指向；(6) CI 耗時量測**自 rev6 起移交 B2b-2**（樣本規則見 D5／D6），本票不再以湊足五個樣本為完成或合併條件。

**Architecture:** ruleset 一次建立、完整 JSON 前後快照留痕（含 name、id、enforcement、conditions、bypass_actors、rules）；probe 分支只做三個**彼此獨立**的 enforcement 狀態驗證，證據為 `gh pr view --json mergeStateStatus,mergeable,statusCheckRollup`＋`gh api .../commits/<head>/check-runs`＋`gh api .../rules/branches/main` 的逐字輸出；closure PR #3 提交 `ci-merge-policy.md`、automation plan §12 指標、README 一句、本 plan 與 backlog rev27；其 pull_request run 若符合 D5 條件可計入 B2b-2 的樣本，但 **B2b 的合併不以湊足五筆為前置**；register v8 與 B2b-2 關票由 B2b-2 在五個合格樣本固定後以**一次**彙整 commit 負責（其 CI 不入樣本）。**ruleset 生效後，repo 既有的「docs-only 直接 push main」慣例終止**，所有變更走 PR＋required checks（每次約 6–8 分鐘）。

---

## rev6 範圍調整（design gate 修訂提案，待複核）

- **問題**：D5 把量測樣本綁在 `b2b/closure` PR 的「前五次真實修訂 run」，隱含假設 closure PR 會經過多輪複核修正。實際上 PR #3 兩次 push 後即複核通過（`57b2448`），正常情況不會再有修訂；繼續等待等於要求意外發生，製造修訂又違反 D5。這是取樣設計的落差，不是文件缺陷。
- **調整**：(a) **B2b 本票**＝驗收條件 (2) ruleset、(5a)–(5e) enforcement 實證與政策文件、closure PR #3 以 rebase merge 落地；(b) **(6) CI 耗時量測拆為獨立追蹤票 B2b-2**（backlog rev27 新增）：保留 PR #3 的樣本 1／2，之後從**正常開發 PR** 的合格 `pull_request` run 累積到五個，不為量測刻意改文件或開 PR；(c) 量測條件固定並加入可比性：每樣本記錄 `ci.yml` hash、**受測版本指紋**與 cache 狀態，指紋不同者不直接混算；cache hit 樣本不稱為冷啟動。
- **不變**：D1–D4 與 Task 1–3 的內容與證據；「2/5 不是完成」——B2b-2 的關票仍要五個合格樣本；不足五筆即不完成，未來若要例外仍須另案裁定。

## owner 裁定（design gate 第一輪，rev2 回寫；rev3 全部通過，rev4 起下列「待複核」與「尚未授權」字樣為歷史狀態，執行結果見 Task 1／Task 2）

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
- **D2（方向通過；rev4 註：PATCH 已於 2026-09-07 授權並完成，見 Task 1 Step 2）**：repo 設定改為只允許 rebase merge（`allow_merge_commit: false`、`allow_squash_merge: false`、`allow_rebase_merge: true`），`delete_branch_on_merge` 維持 false（刪分支仍逐次授權）；PATCH 前後完整 JSON 保存。**外部 PATCH 尚未授權（rev2 時的狀態，歷史）。**
- **D3（rev2 修正；rev3 通過，rev4 已執行）**：三個獨立狀態，各自 push 一次、各自取證，**不合併 A／B 的驗證**：
  - **A**：只改 `docs/architecture/SHA256SUMS` 一筆 hash 最後一碼 → 預期實際 checks：`checksums` failure、其餘三 job success；`mergeStateStatus: BLOCKED`。
  - **B**：**先恢復 checksum 為正確**（與 base 相同），再把 `ci.yml` 的 `checksums` job `name:` 改為 `checksums-renamed`（其餘不變）→ 預期實際 checks：`go`／`frontend`／`wails-build`／`checksums-renamed` 全 success，**required 的 `checksums` 缺席**；`mergeStateStatus: BLOCKED`。
  - **C**：恢復原 job 名稱；workflow、checksum 與 base 完全相同（`git diff origin/main...C` 為空）→ 四個 required checks success，`mergeStateStatus: CLEAN`、`mergeable: MERGEABLE`。
  - **缺席判準**：以 `GET rules/branches/main` 取得 required 清單，與 `GET commits/<HEAD>/check-runs` 的實際 check 名稱比對；**不假設** `statusCheckRollup` 必定產生 expected／pending 項目，GitHub 實際回應逐字保存（若 rollup 有 expected 項目也一併記錄）。
  - probe PR 不合併；C 取證後關閉 PR、刪分支（各自授權）。
- **D4（rev2 補齊；rev3 通過，rev4 已落地為 `ci-merge-policy.md` v1.1）**：`ci-merge-policy.md` 骨架——(i) 保護對象、ruleset name／id、建立 payload 與 GET 快照路徑；(ii) required contexts 權威清單與來源（B2a plan rev7，main `ee30055`／`19422bc` check-runs，app 15368）；(iii) direct push：禁止（含 admin），一律 PR；(iv) bypass：不設 bypass actor；**admin 仍能修改／刪除 ruleset，屬政策而非技術阻擋**，任何修改須以前後 JSON 留痕並走 PR 記錄；(v) 緊急例外：唯一途徑是 owner 以 API 暫時 `enforcement: disabled` → 執行 → **立即恢復並 GET 核對**；恢復失敗（GET 不符或 API 錯誤）→ **停止所有後續外部寫入並回報**，直到人工修復；僅限「CI 基礎設施本身故障」；(vi) **required-check 改名程序（不會卡住；rev3 修正順序）**：(1) 新舊 job 並存的 PR 合併後，確認 main 上新 context 成功；(2) ruleset required 清單加入新名（核對新舊名稱與 app 皆為 15368）；(3) **先從 required 清單移除舊名**，並確認新名仍為 required；(4) **再以另一 PR 移除舊 job**——此時該 PR 只需滿足新名，required checks 全綠後合併。順序不得顛倒：若先移除舊 job 再移除舊 required，該 PR 的新 SHA 缺少仍被要求的舊 check（main 上先前的成功紀錄不能滿足新 SHA）而無法合併。每次 ruleset 更新分別授權並保存前後 GET；(vii) **ruleset 誤刪重建**：以本文件保存的建立 payload 重新 POST → 記錄新 id → `GET rules/branches/main` 核對有效規則與四 contexts → 更新政策文件的 id；(viii) 紅燈處置指向 register 規則 1／7／8（不得重跑吸收）；(ix) 量測資料出處。
- **D5（rev6 改寫，待複核；rev2–rev5 版本為歷史）**：量測歸 **B2b-2**。樣本＝ruleset 啟用（2026-09-07T00:59+08:00）後，**任何 PR** 上自動觸發的 `pull_request` run，且 (a) attempt 1（rerun 不算；`workflow_dispatch`／main push 不算）；(b) 該 head 的 `.github/workflows/ci.yml` SHA-256 與當時 main 相同；(c) 該 head 的 check-runs 四個 required contexts（`go`／`frontend`／`wails-build`／`checksums`，app 15368）皆出現（結果可紅，紅燈依 register 分類並保留）；(d) 記錄 D6 的受測版本指紋。**選樣順序固定**：保留 PR #3 的 run `34075935919`／`34077383674` 為樣本 1／2；樣本 3–5 只從**本 rev6 核准之後**建立的正常開發 PR 的 run，**按 run 建立時間先後**依序選取前三個合格者；**不回溯**納入 probe PR #2 或核准前的任何 run，**不挑綠燈**（合格的紅燈 run 照樣入樣本並依 register 分類）。**不為湊數製造修訂、不 rerun、不 dispatch、不刻意開 PR**；失敗與排除一律保留並附理由。五樣本固定後由 B2b-2 做一次彙整（register v8、backlog 關票）；彙整 commit 的 CI 不入樣本。
- **D6（rev2 欄位與邊界；rev6 補「受測版本指紋」與分組規則，待複核）**：**受測版本指紋**＝該 head 下列六個路徑的 git tree／blob hash（以 `git rev-parse <head>:<path>` 取得；採**保守的完整相關目錄**，涵蓋受測程式、測試、fixture、依賴與測試設定，不做依賴分析）：`internal`（三條 Go 測試所在套件及其可能依賴的所有內部套件，含 `internal/contract`）、`testdata`（`fake-claude.sh` 等 fixture）、`go.mod`、`go.sum`、`frontend`（`src/components`、`src/stores`、`vitest.config.ts`、`tsconfig*.json`、`package.json`、`package-lock.json` 等全部追蹤檔）、`.github/workflows/ci.yml`。root 套件的 `.go` 檔不在內（`internal` 不會反向 import root）。**指紋完全相同的樣本才可混算 min／median／max**；指紋不同的樣本分組列出，**每組標各自的 n**（例如 3＋2 只能寫「群組 A n=3、群組 B n=2」，不能宣稱同版本 n=5）；「最大群組」的分布只描述該組，不代表全部五筆；受測程式、測試或依賴有變的 PR 不能只因 workflow 相同就混算。**不足五筆仍不完成**。樣本 1／2 的指紋與 main `1bbb47a` 相同（`e1d3e323`／`8cc2ff00`／`83f5ffb9`／`4ae87410`／`c98b36df`／`4efaf169`）。cache 欄位：npm cache hit 的樣本**不稱為冷啟動**；只有記錄到 cache miss 才可標「冷啟動候選」。其餘欄位不變：每樣本記 run ID／attempt、PR head／base SHA、`ci.yml` SHA-256、runner image 版本字串（四 job）、npm cache 狀態（hit／miss／未確認）、artifact id、四 job 起訖與 elapsed、五條精確測試名稱的 `Elapsed`／ms（`TestClaudeAssistFailsLoudOnOversizedLine`、`TestMultiTurnSendAndTurnBoundaries`、`TestOutputCancellationKillsGrandchildren`、`PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積`、`SpecWorkspace draft accept > discards spec-assist result if the file switches during the call`）。**缺值記「缺」不記 0**；個別測試 Elapsed 與 job 耗時**分開報告**；**未確認 cache miss 的樣本不稱為冷啟動**，結論只寫「n=5 clean PR attempts 的分布」。register v8（由 **B2b-2** 在五個合格樣本固定後回填）：A 段 #2／#3／#6、B 段 F1／F2 補 CI 量測欄（依指紋分組各標 n 的 min／median／max、樣本 run ID）、規則 5 標完成；B2b-2 關票寫入 backlog（版本＝當時最新＋1），**不是本票的 backlog rev27**。
- **D7（rev6 調整 (11)–(14)，待複核；(1)–(10) 已完成）**：(11) **改為** B2b-2 自正常開發 PR 採樣（不在本票、不為此 push）；(12) **改為** PR #3 最後一次 push：plan rev6 回填＋backlog rev27（範圍拆分與 B2b-2 立票），其 run 若合格可計入樣本 3 但**不是為了取樣而推**；(13)(14) 不變：rebase merge PR #3（授權含自動 main run）、刪 `b2b/closure`。原文：(1) `POST rulesets`（前 `[]`／後完整 JSON）；(2) `PATCH repos/...` merge 設定（前後 JSON）；(3) push A → (4) 開 probe PR → A 證據 → (5) push B → B 證據 → (6) push C → C 證據 → (7) 關 probe PR → (8) 刪 probe 分支；(9) push `b2b/closure` 首次（政策文件等）→ (10) 開 closure PR → 樣本 1；(11)…每次真實修訂 push → 樣本 2–5；(12) 彙整 commit push（不入樣本）；(13) rebase merge closure PR（**授權含自動 main run**，其結果依有限次回填原則只保存於 GitHub run 與結案回報）；(14) 刪 `b2b/closure` 分支。每步分開授權、前後狀態抄錄。（B2a 分支清理已完成，不列。）
- **D8（rev6 重估，待 owner 裁定）**：**B2b 本票 5.0 hr → 0.5 pt**（ruleset＋設定＋雙重 GET 1.0、probe 三狀態 1.5、政策文件 1.5、closure PR 與 plan／backlog 回寫 0.5、證據抄錄與快照 0.5）；**B2b-2 3.0 hr → 0.3 pt**（三個樣本採集與指紋核對 1.5、register v8＋backlog 關票 1.0、分組分析與缺口說明 0.5）。合計 8.0 hr，較 rev1 的 7.0 hr 多 1.0 hr，原因是取樣設計落差造成的拆票與跨 PR 追蹤成本。歷史：rev1–rev5 為 0.7 pt＝7.0 hr（ruleset 1.0、probe 1.5、政策 1.5、closure 五樣本 1.5、回寫 1.0、證據 0.5）。

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

- [x] Step 0 唯讀快照（`/tmp/b2b-t1/before.*`：`rulesets` `[]`、`rules/branches/main` `[]`、merge 旗標皆 true、main `1bbb47a` 四 context success app 15368）：`GET rulesets`、`GET repos/...`（merge 旗標）、`GET rules/branches/main`、main head check-runs → `/tmp/b2b-t1/before.*.json`。
- [x] Step 1（2026-09-07 授權，單次 POST；payload SHA-256 `9d81a5ca…` 與 D1 相同）→ id **22394412**、`created_at` 2026-09-07T00:59:05+08:00；`GET rulesets/22394412` name／enforcement／conditions／bypass_actors 相同、rules 五項 type 與 payload 欄位值相同（GitHub 另補預設欄位 `required_reviewers`／`require_extra_approval_for_unattributed_changes`／`allowed_merge_methods`／`do_not_enforce_on_create`）；`GET rules/branches/main` 五種規則生效、四 required contexts app 15368、strict。`POST rulesets`（D1 payload 逐字）→ 保存回應（id、created_at）→ `GET rulesets/<id>` 核對 name／enforcement／conditions／bypass_actors／rules 與 payload 一致 → `GET rules/branches/main` 核對有效規則含四個 required contexts、線性歷史、禁刪除／force-push、PR 必須。
- [x] Step 2（2026-09-07 授權，單次 PATCH 三欄位）→ `allow_merge_commit`／`allow_squash_merge` true→false、`allow_rebase_merge` true 不變；全欄位比對只有前兩者改變；`allow_auto_merge`／`delete_branch_on_merge` false 不變；ruleset GET 與有效規則逐位元組相同。owner 裁定不另改 ruleset `allowed_merge_methods`（repo 與 ruleset 限制共同適用）。前後 JSON 見 `ci-merge-policy.md` 附錄 B。
- [x] Step 3 標註（`ci-merge-policy.md` §1）：**有效規則已核對；direct-push 拒絕未實測**（不對 main 推送、不 dry-run）。

## Task 2：enforcement 實證（D3；授權 (3)–(8)）

- [x] Step A（owner 接受改用 `schemas/codex/SHA256SUMS`，第 1 筆 hash 首字元 `9`→`a`）：A `eda9f25a59e6a0061c471ccb957a084149f93b77`，push A（精確 SHA）→ PR **#2** → run `34047792759`（pull_request、attempt 1）failure：`checksums` failure（`./ApplyPatchApprovalParams.json: FAILED`）、其餘三 job success；required 四個皆出現、`checksums` 失敗；REST `blocked`／GraphQL `BLOCKED`。原文：建 `ci-probe/2026-09-08-enforcement`（自 main），commit A（SHA256SUMS 一碼）→（授權）push → （授權）開 PR → 等 run → 保存 `gh pr view --json mergeStateStatus,mergeable,statusCheckRollup`、`GET commits/<A>/check-runs`、`GET rules/branches/main`；預期 `checksums` failure、BLOCKED。
- [x] Step B（owner 接受同時改 job key 與 `name:`）：B `1c41d81757388c852561b1489c9af582fa9b2353`（A 的直接子 commit，fast-forward push）→ run `34065893254` success：`checksums-renamed`／`frontend`／`go`／`wails-build` 全 success；required `checksums` **缺席**；REST `blocked`／GraphQL `BLOCKED`；rollup state `SUCCESS`、`checksums-renamed` `isRequired: false`、**無 expected 佔位**、combined status `total_count` 0。原文：commit B（**恢復 SHA256SUMS 與 base 相同**＋`checksums` job 改名 `checksums-renamed`）→（授權）push → 等 run → 同組證據；預期實際 checks 全 success 但 required `checksums` 缺席、BLOCKED；記錄 rollup 是否出現 expected 項目（不預設）。
- [x] Step C：C `b699d0bc14acf19d39ea03c6950aacc56d5b2bd4`（tree `a7dbefd…` 與 base 相同）→ run `34066286258` success：四 required 皆出現且 success、app 15368；REST `clean`／GraphQL `CLEAN`、`MERGEABLE`；未合併。原文：commit C（恢復 job 名；`git diff origin/main...HEAD` 為空）→（授權）push → 等 run → 同組證據；預期四 required success、CLEAN／MERGEABLE。
- [x] Step D：PR #2 以留言關閉（`state: CLOSED`、`mergedAt: null`）；遠端分支以 `--force-with-lease=<ref>:b699d0b…` 刪除，本機切回後核對仍指向 C 再 `-D`；刪後遠端與本機無 `ci-probe/*`、main 仍 `1bbb47a`、ruleset 與 repo 設定未動。三次 run 耗時 397／357／389 s 只作補充（owner 釐清，不入 n=5）。

## Task 3：政策文件與指標（D4）

- [x] 新增 `docs/architecture/ci-merge-policy.md` v1.1（§1–§9 對應 D4 (i)–(ix)；附錄 A payload＋GET、附錄 B 有效規則＋PATCH 前後、附錄 C 三狀態證據）。
- [x] automation plan v2.5：§12 第 1 點補一句；README §測試程式碼區塊後補一句指向 `ci-merge-policy.md`。
- [x] 本機：純文件（無 `.go`／`.ts`／`.vue`／workflow 變更）；用語掃描；`git diff --check` 乾淨。

## Task 4：closure PR、D6 五樣本、彙整回寫（D5／D6；授權 (9)–(14)）

- [x] 建 `b2b/closure`（自 `b2b/ruleset` `3e34181`＝main `1bbb47a`＋plan rev1–3）：`417791a`（Task 3）＋`5a83717`（複核修正）→（授權）push `5a83717` 精確 SHA（2026-09-07 09:20）→（授權，10 項鎖定通過後單次 `gh pr create`）PR **#3**（head `5a83717`、base `1bbb47a`）→ **樣本 1 合格**（見下表）。原文：建 `b2b/closure`（自當時 main），首次 commit：政策文件、automation plan、README、本 plan →（授權）push →（授權）開 PR → 樣本 1（記 D6 欄位）。

**D6 樣本表（目前 1/5，尚缺 4 次；樣本只來自真實修訂，不足即回報缺口）**

| # | run／attempt／event | head／base | `ci.yml` SHA-256 | 合格 | run 總長 | frontend | checksums | wails-build | go | npm cache | #2／#3／#6 Elapsed | F1／F2 | artifact（go-test-json／vitest-output） |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `34075935919`／1／pull_request | `5a83717`／`1bbb47a` | `8966b703…ffff`（與 main 相同） | 是（四 required 皆出現、全 success、app 15368） | 378 s（02:19:59Z→02:26:17Z） | 51 s；ubuntu-24.04 image 20260831.293.1、runner 20260828.587 | 8 s；ubuntu-24.04 同上 | 152 s；macos-15 image 20260824.0482.1、runner 20260819.586；setup-go cache hit（補充） | 319 s；macos-15 同上；setup-go cache hit（補充） | **hit**（`node-cache-Linux-x64-npm-9dcd67fe…`） | 0.39 s／0.04 s／0.35 s（皆 pass） | 195 ms／149 ms（vitest verbose，40 files／397 tests，Duration 19.74 s） | `10002143591`／`10002042695` |

樣本 1 的 npm cache 為 hit，依 D6 不稱為冷啟動。個別測試 Elapsed 與 job 耗時分開列；job 起訖取自 GitHub jobs API。原始記錄 `/tmp/b2b-t1/samples/s1/`（run／jobs／artifacts JSON、四 job log、artifact 原檔、`record.md`），manifest 兩份：`s1.manifest.sha256`（12 檔，首次採集、不含 artifact，保留）與 `s1.manifest-full.sha256`（19 檔，含 `go-test.json`／`vitest.out`／`.rc`），皆 `shasum -c` 全 OK。

**D7 (9)(10) 證據補正（rev5 揭露）**：(a) D7 (9) 推送時 GitHub SSH／API 暫時逾時，推送腳本的鎖定檢查未取得結果，且鎖定只以 `&&` 鏈印出「LOCK OK」、沒有硬性退出，push 在鎖定未確認下照樣執行；push 前約 7 秒另一次獨立 `ls-remote` 顯示 main＝`1bbb47a`、`b2b/closure` 不存在，推送內容與授權命令逐字相同，結果符合預期且無需回退，但**不符合「鎖定值改變即停止」的要求**。(b) 補救：鎖定腳本 `/tmp/b2b-t1/gate.sh` 改為任一查詢失敗、空結果或不符即 `exit 2`、寫入步驟不執行，並以本機模擬（SSH 逾時 rc 128／空輸出／SHA 不符／第二項失敗）驗證後，D7 (10) 才以 10 項鎖定通過後單次執行。(c) `push-closure1.before.ls-remote` 為 0 bytes（`tee` 只截 stdout）；逾時原文存於 harness 背景工作紀錄，已複製為 `push-closure1.task-stderr-stdout.txt`（第 2／7／17 行為三次逾時訊息）。(d) 採集工具首版 `jobs-summary.tsv` 的 image／runner 欄位為空、cache 全記「缺」，但原始 log 有值；解析已修正（`d6-summarize.sh`，以既有 log 離線驗證：frontend npm cache hit、四 job image 與版本字串齊全）並輸出 `jobs-summary.v2.tsv`，首版檔案保留。
- [x] 樣本 2（rev5 推送 `57b2448`，run `34077383674`，合格；390 s；frontend 49 s／checksums 5 s／wails-build 268 s／go 335 s；npm cache hit；#2 0.35 s／#3 0.03 s／#6 0.35 s；F1 218 ms／F2 147 ms；artifact go-test-json `10002641420`／vitest-output `10002541534`；指紋同 main `1bbb47a`；記錄 `/tmp/b2b-t1/samples/s2/`，manifest 12／18 檔全 OK）。**樣本 3–5 移交 B2b-2**（rev6）；本票不再為取樣 push。歷史原文：後續每次**真實**修訂各一次 push → 樣本 2–5；不足五次即回報缺口。
- [ ] （rev6）PR #3 最後一次 push：plan rev6＋backlog rev27（範圍拆分、B2b-2 立票、B2b 狀態）→ required checks 全綠。register v8 與 B2b-2 關票由 B2b-2 負責。歷史原文：五樣本固定 → 一次彙整 commit（register v8、backlog rev27、plan 最終回填）。
- [ ] （授權，含自動 main run）rebase merge PR #3 → main run 結果只保存於 GitHub run 與結案回報 →（授權）刪 `b2b/closure`。B2b 關票的合併 SHA／main run 由 backlog 的下一次修訂（B2b-2 或其他票）補記，不另開自我參照的回填 PR。

## 驗證策略

- 設定：`GET rulesets/<id>`＋`GET rules/branches/main` 前後快照。
- enforcement：三個獨立狀態的 PR JSON＋check-runs 逐字證據（BLOCKED／BLOCKED＋required 缺席／CLEAN）。
- 量測：五樣本原始 artifact（`go-test.json`／`vitest.out`）取值，缺值記缺；彙整 commit 的 CI 不入樣本。
- 已知未實測：direct push 被拒；GitHub 對缺席 required context 的實際呈現只能在 B 狀態觀察。

## Gate B（B2b 完成條件）

- [x] ruleset 完整 GET 與有效規則皆符合 D1；repo merge 設定符合 D2；前後 JSON 留痕（`ci-merge-policy.md` 附錄 A／B）。
- [x] A／B／C 三份獨立證據（BLOCKED／BLOCKED＋required 缺席／CLEAN）；probe PR #2 已關、分支已刪、未合併（附錄 C）。
- [ ] `ci-merge-policy.md` v1.1（含維護程序 §4–§7）落地並由 automation plan §12 與 README 指向；PR #3 以 rebase merge 落地（含 plan rev6、backlog rev27）。
- [ ] （rev6 改寫）量測移交完成：樣本 1／2 記錄、採集工具（`d6.sh`／`d6-summarize.sh`／gate）、D5／D6 取樣與指紋規則已寫入本 plan 與 backlog B2b-2；**register v8 與 B2b-2 關票不在本 Gate**。每步外部寫入皆有授權與前後狀態紀錄。歷史原文：D6 五樣本表與 register v8、backlog rev27 落地。

## 修訂記錄

- rev6（2026-09-07）：design gate 修訂提案（含 owner 第一輪複核修正：指紋改保守完整目錄、選樣順序與分組結論、刪除例外語、Goal／Architecture／D6 尾段同步）——範圍拆分（B2b 限治理與政策文件；(6) 量測拆為 B2b-2，保留樣本 1／2，後續自正常開發 PR 累積）；D5 改寫樣本來源與條件；D6 補受測版本指紋與分組規則、cache hit 不稱冷啟動；D7 (11)–(14) 調整；D8 重估 B2b 0.5＋B2b-2 0.3；Task 4 與 Gate B 重寫；新增「rev6 範圍調整」段。D1–D4、Task 1–3 不變。
- rev5（2026-09-07）：一次合併回填——D7 (9) push `5a83717`、D7 (10) PR #3 與樣本 1（合格）、D6 樣本表（1/5）、D7 (9)(10) 證據補正（鎖定未硬停揭露與 gate 腳本修正、0 bytes 檔與逾時原文另存、採集工具解析修正與完整 manifest）；狀態行與 D7 標註更新。設計內容不變。
- rev4（2026-09-07）：執行回填（含 owner 對 417791a 的複核修正：政策 §5 恢復基準、§7 重建 payload、§1／§6 指向附錄 C、§9 register 待回填；plan D2–D7 的「待複核」「尚未授權」標為歷史）——Task 1／Task 2 全部勾選並附 id／SHA／run／merge state；Task 3 本機落地；Gate B 前兩項勾選；D5 依 owner 釐清補「probe 耗時只作補充，不入 n=5」；狀態行更新。設計內容（D1–D8）不變。
- rev3（2026-09-08）：rev2 複核修正——D4 (vi) required-check 改名順序：先從 required 清單移除舊名並確認新名仍 required，再以另一 PR 移除舊 job；其餘不變。
- rev2（2026-09-08）：design gate 第一輪修正——D3 三個獨立狀態與缺席判準；移除 dry-run、要求完整 ruleset GET；D4 改名／重建／恢復失敗處置與「無 bypass ≠ admin 無法改設定」；D5／D6 取樣識別、失敗保留、彙整不入樣本、缺口回報、Elapsed 與 job 耗時分開、冷啟動邊界；D7 順序與清單；D8 合計 7.0 hr。
- rev1（2026-09-08）：建立；唯讀前置；D1–D8；四 Task；Gate B。
