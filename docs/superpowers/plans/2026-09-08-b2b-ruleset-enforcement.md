# B2b main ruleset、enforcement 實證、`ci-merge-policy.md`、CI 冷啟動量測 Implementation Plan

> **For agentic workers:** 本票的外部寫入（ruleset 建立／修改／刪除、push、開／關 PR、merge、刪分支）**每一步逐次由 owner 授權**，並抄錄前後狀態（ruleset 以 JSON）。Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev1（2026-09-08，design gate 第一輪；待 owner 裁定 D1–D8）
> 狀態：**待 owner design gate**。尚未變更任何 GitHub 設定、未開 PR、未 push。
> 票源：Pre-M4 Readiness Backlog **B2b**（rev16 拆自 B2，**0.7 pt**＝6.0–8.5 hr）：驗收條件 (2) main ruleset、(5a)–(5e) enforcement 實證、`ci-merge-policy.md`、(6) CI 冷啟動量測 n=5；承接 B2a plan rev7「B2b 承接事項」的已裁定輸入（required contexts 權威、probe／closure 兩條分支路徑、外部寫入授權清單、D6 樣本定義）。
> 基準：`main`＝`origin/main`＝`1bbb47a`（B2a 關票，含 `ci.yml`）。分支 **`b2b/ruleset`**（本機，自 `1bbb47a`）放本 plan；enforcement 驗證用 **`ci-probe/2026-09-08-<目的>`**（驗完關 PR、刪分支、不合併）；政策文件與回寫用 **`b2b/closure`** PR（required checks 全綠後 rebase and merge）。
> 唯讀前置（2026-09-08）：`rulesets` 為 `[]`、`main` branch protection 404、repo public、`allow_merge_commit`／`squash`／`rebase` 皆 true、`allow_auto_merge` false、`delete_branch_on_merge` false；Actions `enabled`、`allowed_actions: all`、`sha_pinning_required: false`；main head `1bbb47a` 的 check-runs 四個 context（`go`／`wails-build`／`frontend`／`checksums`，app `github-actions`／15368）；`ci.yml` 觸發為 `pull_request`、`push: main`、`workflow_dispatch`；無 open PR。

**Goal:** 讓「required checks 全綠才能合併、禁止 direct push、線性歷史」在 GitHub 上實際生效並被實證：(2) 以 ruleset 保護 `main`；(5a) 紅燈 PR 不可合併、(5a′) required context 缺席時永遠 pending（fail loud）、(5b) 修正後 `CLEAN` 可合併；(5c)(5e) 政策文件明文化（direct push、admin bypass、緊急例外、required-check 改名程序）並由 automation plan §12 指向；(6) 以 n=5 clean PR run attempts 量測 #2／#3／#6／F1／F2 的 CI 耗時回寫 register。

**Architecture:** ruleset 一次建立、以 JSON 前後快照留痕；probe 分支只做負向／正向 enforcement 驗證，證據為 `gh pr view --json mergeStateStatus,statusCheckRollup` 與 `gh api .../rules/branches/main`；closure PR 提交 `ci-merge-policy.md`、automation plan §12 指標、register v8、backlog rev27 與本 plan，其自身的 5 次 clean run attempts 即 D6 樣本；量測直接從 artifact（`go-test.json` 的 `Elapsed`、`vitest.out` 的 verbose reporter 毫秒）取值。**ruleset 生效後，repo 既有的「docs-only 直接 push main」慣例終止**，所有變更走 PR＋required checks（每次 PR run 約 6–8 分鐘）——見 D1。

---

## 待 owner 裁定（D1–D8）

- **D1（ruleset 內容與對慣例的影響）**：`POST /repos/slam0504/ai-software-engineering/rulesets`，`target: branch`、`enforcement: active`、`conditions.ref_name.include: ["refs/heads/main"]`、`bypass_actors: []`（不設常駐 bypass），rules：`pull_request`（`required_approving_review_count: 0`——repo 只有 owner 一人，無法要求他人 approve；`dismiss_stale_reviews_on_push: false`、`require_code_owner_review: false`、`require_last_push_approval: false`、`required_review_thread_resolution: false`）、`required_status_checks`（`strict_required_status_checks_policy: true`；`required_status_checks: [{context: "go", integration_id: 15368}, {context: "frontend", integration_id: 15368}, {context: "wails-build", integration_id: 15368}, {context: "checksums", integration_id: 15368}]`）、`required_linear_history`、`deletion`、`non_fast_forward`。**影響**：owner 亦不可 direct push main（含 docs-only）、不可 force-push、不可刪 main；PR 需 up-to-date 才可合併；merge 方式由 D2 決定。是否核准此內容與慣例變更。
- **D2（merge 方式）**：repo 目前允許 merge／squash／rebase 三種；ruleset 的 `required_linear_history` 會拒絕 merge commit。建議同時把 repo 設定改為只允許 **rebase merge**（`allow_merge_commit: false`、`allow_squash_merge: false`、`allow_rebase_merge: true`）以免誤選；`delete_branch_on_merge` 維持 false（刪分支仍逐次授權）。是否核准（屬 GitHub 設定變更，另列授權清單）。
- **D3（probe 設計）**：分支 `ci-probe/2026-09-08-enforcement`，自 `1bbb47a`：commit A（5a）改 `docs/architecture/SHA256SUMS` 任一筆 hash 的最後一碼 → `checksums` 紅 → 預期 `mergeStateStatus: BLOCKED`、`statusCheckRollup` 含 `checksums: FAILURE`；commit B（5a′，在 A 之上）把 `ci.yml` 的 `checksums` job `name:` 改為 `checksums-renamed` → 原 context 缺席 → 預期 `BLOCKED` 且 required check 顯示 expected／pending（fail loud，不得靜默通過）；commit C（5b）revert A、B → 四 job 全綠 → 預期 `mergeStateStatus: CLEAN`（`mergeable: MERGEABLE`）。每一步的 PR 狀態以 `gh pr view` JSON 逐字保存；**probe PR 不合併**，驗完關閉 PR 並刪分支（各自授權）。是否核准三個 commit 的內容與順序。
- **D4（`ci-merge-policy.md` 內容骨架）**：(i) 保護對象與 ruleset id／JSON 快照路徑；(ii) required contexts 權威清單與來源（B2a plan rev7 main SHA `ee30055`／`19422bc` check-runs）；(iii) direct push：禁止（含 admin），一律 PR；(iv) admin bypass：不設 bypass actor；(v) 緊急例外：唯一途徑是 owner 以 API 暫時停用 ruleset（`enforcement: disabled`）→ 執行 → 立即恢復，前後 JSON 與理由以 docs commit 留痕（走 PR），並限定用於「CI 基礎設施本身故障」；(vi) required-check 改名程序：先在 PR 內同時改 workflow 與 ruleset 草案、驗證 fail-loud 後才更新 ruleset，避免靜默降級（(5d)）；(vii) 紅燈處置指向 register 規則 1／7／8（不得重跑吸收）；(viii) 量測資料出處。是否核准骨架。
- **D5（D6 樣本來源）**：n=5 clean PR run attempts 取自 `b2b/closure` PR（ruleset 啟用後、workflow 未修改、四個 required contexts 皆出現）：closure PR 的每次 docs 修訂 push 各產生一次 pull_request run；不足 5 次時以「plan 修訂 commit」補齊（每次為真實文件修訂，不製造空 commit）。probe PR 的 run 因 workflow 被修改（5a′）不入樣本；main push run 與 `workflow_dispatch` 只作補充。是否核准。
- **D6（量測欄位與回寫）**：每 attempt 記 run ID、attempt、runner image（`macos-15`／`ubuntu-24.04` 版本字串）、npm cache hit、四 job 起訖與 elapsed；#2／#3／#6 取 `go-test.json` 的 `Elapsed`、F1／F2 取 `vitest.out` verbose reporter 的毫秒；回寫 register **v8**（A 段 #2／#3／#6 與 B 段 F1／F2 的「最後一次負載重驗」／「重驗」欄補 CI 量測，註明「n=5 clean PR attempts，非負載矩陣」；規則 5 標為已完成）、backlog rev27（B2b 關票）。是否核准。
- **D7（外部寫入授權清單，逐步）**：(1) `POST rulesets`（前：`[]`；後：JSON）；(2) repo merge 設定 PATCH（D2 通過時；前後 JSON）；(3) push `ci-probe/...` 三次（A、B、C 各一次，或 A＋B 合併為兩次）；(4) 開 probe PR；(5) 關 probe PR；(6) 刪 probe 分支；(7) push `b2b/closure`（每次修訂各一次）；(8) 開 closure PR；(9) rebase merge closure PR；(10) 刪 `b2b/closure` 分支；(11) 刪 `b2a/ci-workflows`（若尚未刪）。每步前後狀態抄錄。是否核准清單與順序（允許 (3) 內的三次 push 以單次授權涵蓋？——建議每次仍分開）。
- **D8（估點與範圍）**：0.7 pt（6.0–8.5 hr，中位 7.0）：ruleset＋設定 1.0 hr、probe 三步與證據 1.5 hr、政策文件 1.5 hr、closure PR 與 5 次 attempts 量測 1.5 hr、register v8／backlog rev27／plan 回寫 1.0 hr、CI 等候不計。範圍：不改 production、測試、fixture、`ci.yml`（probe 分支上的暫時修改除外且不合併）。是否核准。

---

## Global Constraints

- **零 production／測試變更**；`ci.yml` 於 main 不變（probe 分支的 5a′ 修改不合併）。
- **外部寫入逐次授權**（D7 清單）；每步以 `gh api`／`gh pr view --json` 抄錄前後狀態進證據目錄 `/tmp/b2b-*`，關鍵 JSON 同步進 `ci-merge-policy.md` 附錄或 plan。
- **不 rerun、不 dispatch**（D6 補充資料除外，且須另案授權）；紅燈依 register 規則分類，不得重跑吸收。
- **fail loud 優先**：(5a′) 必須實證 required context 缺席時 PR 永遠不可合併；若 GitHub 行為與預期不同（例如靜默忽略缺席 context），列為 finding 並停在 D 決策，不得繞過。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭；`gh` 輸出逐字抄錄。

---

## Task 1：ruleset 與 repo 設定（D1／D2；授權 (1)(2)）

- [ ] Step 0 唯讀快照：`gh api repos/.../rulesets`、`repos/...`（merge 旗標）、`rules/branches/main`、main head check-runs → `/tmp/b2b-t1/before.json`。
- [ ] Step 1（授權後）`POST rulesets`（D1 JSON）→ 抄錄回應（id、created_at）；`GET rules/branches/main` 確認四個 required contexts 與 rules 生效。
- [ ] Step 2（D2 通過並授權後）`PATCH repos/...`（只允許 rebase merge）→ 前後 JSON。
- [ ] Step 3 本機唯讀驗證：`git push --dry-run origin main`（預期被拒：dry-run 不會真的 push，但 ruleset 拒絕在伺服器端；若 dry-run 不觸發，改以 (5a) probe PR 的 direct-push 嘗試**不做**——direct push 拒絕以 API `rules/branches/main` 的 `non_fast_forward`／`pull_request` 規則存在為證，不實際嘗試推 main）。

## Task 2：enforcement 實證（D3；授權 (3)–(6)）

- [ ] Step 1 建 `ci-probe/2026-09-08-enforcement`，commit A（SHA256SUMS 一碼）→ push → 開 PR → 等 run → `gh pr view --json mergeStateStatus,mergeable,statusCheckRollup` 逐字保存（預期 `BLOCKED`、`checksums` FAILURE、其餘三 job 綠）。
- [ ] Step 2 commit B（`ci.yml` `checksums` job 改名）→ push → 等 run → 預期 `BLOCKED`，rollup 顯示 `checksums` 為 expected／pending、`checksums-renamed` 為非必要 success；記錄 GitHub 實際呈現。
- [ ] Step 3 commit C（revert B、A）→ push → 等 run → 預期四 job 綠、`mergeStateStatus: CLEAN`；**不合併**。
- [ ] Step 4（授權）關閉 PR、刪分支；記錄 PR 編號與三次 run ID。

## Task 3：政策文件與 automation plan 指標（D4）

- [ ] 新增 `docs/architecture/ci-merge-policy.md`（D4 骨架；附錄放 ruleset JSON 前後快照與 probe 證據摘要）。
- [ ] `docs/architecture/sdlc-ai-agent-automation-plan.md` §12 第 1 點補一句指向 `ci-merge-policy.md`（B2 (5e)）；README「測試」段補一句「合併規則見 `docs/architecture/ci-merge-policy.md`」。
- [ ] 本機：`gofmt`／`go build` 不受影響（純文件）；用語掃描；`git diff --check`。

## Task 4：closure PR、D6 量測、回寫（D5／D6；授權 (7)–(11)）

- [ ] 建 `b2b/closure`（自當時 main），commit：`ci-merge-policy.md`、automation plan、README、本 plan rev N；push → 開 PR → attempt 1。
- [ ] 後續每次修訂（register v8 草稿、backlog rev27、plan 回填）各一次 push → attempts 2–5；每次記錄 D6 欄位；不足 5 次以真實修訂補齊。
- [ ] 量測表：五次 attempts × {#2, #3, #6 `Elapsed`；F1、F2 ms；四 job elapsed；runner image；npm cache hit}；紅燈依 register 分類（若出現，先分類再決定是否計入）。
- [ ] register **v8**：A 段 #2／#3／#6「最後一次負載重驗」欄與 B 段 F1／F2「重驗」欄補 CI n=5 量測（min／median／max）；規則 5 標完成；修訂記錄 v8。backlog **rev27**：B2b 關票、B2 全部完成、估點不變。
- [ ] closure PR required checks 全綠 → （授權）rebase merge → main run（自動）→ 記錄；（授權）刪 `b2b/closure`；`b2a/ci-workflows` 若仍在則一併申請刪除。

## 驗證策略

- enforcement：三個 probe 狀態的 `gh pr view` JSON 為證（BLOCKED／BLOCKED＋expected／CLEAN）；ruleset 生效以 `rules/branches/main` 為證。
- 量測：artifact 原始 `go-test.json`／`vitest.out` 取值，五次 attempts 全部保存。
- 無法本機驗證：GitHub 對缺席 required context 的實際呈現（(5a′)），只能在 CI 上觀察；若不符預期即停。

## 估點核對

合計約 7.0 hr（D8），在 0.7 pt 中位；CI 等候不計。

## Gate B（B2b 完成條件）

- [ ] ruleset 存在且 `rules/branches/main` 顯示四個 required contexts、線性歷史、禁 force push／刪除、無 bypass actor；前後 JSON 留痕。
- [ ] (5a) BLOCKED、(5a′) BLOCKED＋required context expected、(5b) CLEAN 三份 JSON 證據；probe PR 已關閉、分支已刪、未合併。
- [ ] `ci-merge-policy.md` 落地並由 automation plan §12 與 README 指向；closure PR 以 rebase merge 落地 main。
- [ ] D6 n=5 量測表與 register v8、backlog rev27 落地；每步外部寫入皆有授權與前後狀態紀錄。

## 修訂記錄

- rev1（2026-09-08）：建立；唯讀前置；D1–D8；四 Task；Gate B。
