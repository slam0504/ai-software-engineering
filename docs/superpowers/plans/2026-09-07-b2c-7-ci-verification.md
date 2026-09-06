# B2c-7 CI 驗證與 register #7 處置 Implementation Plan

> **For agentic workers:** workflow 與文件回寫須依本 plan 分階段執行；驗證分支只推送一次，不 rerun、不 dispatch。

> 版本：rev2（2026-09-07，D1–D5 通過；Task 1 workflow `54b596c` 完成並經結構化 parser、三條本機 focused race 命令與 scope 檢查；本機未安裝 `actionlint`，已明列驗證邊界；前版：rev1）
> 狀態：**Task 1 完成；待 owner 對精確 HEAD 授權一次推送 `b2c7/verify`。尚未推送驗證分支。**
> 票源：Pre-M4 Readiness Backlog **B2c-7**（0.2 pt＝1.5–2.5 hr，owner 2026-09-07 採用）。
> 基準：`main`＝`origin/main`＝`852c28730139041732d02f639f55a18196c77775`；B2c-5／B2c-6 已落地。驗證分支 `b2c7/verify` 自該 SHA 建立。
> 授權邊界：一次性 workflow 只存在於驗證分支，不進 `main` 歷史；驗證分支只推送一次，不 rerun、不 `workflow_dispatch`；CI 結果出來前不改 register #7 狀態。

**Goal:** 在與既有 CI 證據相同的兩平台、兩 replica runner 上，確認 B2c-5 supervisor 有界清理與 B2c-6 oracle 有界化後，兩條核心測試與兩條 escalation 測試各 400/400 通過，且沒有 invalid、setup、race 或 timeout；全部條件成立後才把 register #7 改為 resolved。

**Architecture:** 新增 branch-only `.github/workflows/verify-b2c7.yml`，只接受 `b2c7/verify` 的 `push`。matrix 為 `macos-15-intel`／`ubuntu-latest` × replica 1／2，共四個 runner job，`fail-fast: false`。每個 job 依序執行三個獨立 `go test -race -count=100 -json` step：Claude 核心、proc 兩層 fork 核心、proc 兩條 escalation；前一步失敗時後續 step 仍以 `if: always()` 執行並各自保存原始 JSON 與 rc。環境、起訖程序快照、驗證基準 SHA、workflow HEAD、三份測試輸出與 rc 均進 artifact；每個 job 產生自己的 SHA-256 manifest。run 完成後下載四份 artifact，另建立總 manifest 並逐檔驗證，再按每輪 JSON terminal event 分類。文件落地走另一條自 `main` 建立的 docs 分支，只帶 backlog rev22、plan 與 run 後的 register／diagnosis／backlog 回寫；workflow commit 不得成為 `main` 祖先。

**固定測試集合：**

- Claude 核心：`go test -race ./internal/claude -run '^TestOrphanDoesNotHangNormalExit$' -count=100 -timeout 20m -json`
- proc 核心：`go test -race ./internal/proc -run '^TestSupervisorCleanupTwoLayerForkOrphan$' -count=100 -timeout 20m -json`
- proc escalation：`go test -race ./internal/proc -run '^(TestCtxCancelKillsWholeGroup|TestTerminateEscalatesToGroupKill)$' -count=100 -timeout 30m -json`

每個 runner 上，前兩個命令各產生單一測試的 100 次 terminal event；第三個命令須對兩個具名測試各產生 100 次 terminal event。四個 runner 合計每條測試各 400 次。

---

## Design gate 裁定（D1–D5）

- **D1（通過；workflow 與工具鏈）**：採單一 branch-only push trigger、四個 matrix job、Go `1.26.5`，沿用先前診斷 workflow 已鎖定的 action commit SHA；checkout 設 `fetch-depth: 0`，讓 runner 能以本機 object 驗證 `852c287` 為 workflow HEAD 祖先；三個測試 step 分開保存 rc，後續 step 與 artifact upload 均 `if: always()`。
- **D2（通過；artifact 完整性）**：每個 job 固定保存 `env.txt`、`ps-job-start.txt`、`ps-job-end.txt`、`implementation-base.sha`（固定為 `852c287…` 並驗證為 workflow HEAD 祖先）、`workflow-head.sha`、三份 `*.json`、三份 `*.rc`、job-local `SHA256SUMS.txt`；下載後另產生涵蓋四份 artifact 的 `HASHES.txt`，以 `shasum -c` 全 OK 為完整性門檻。
- **D3（通過；逐輪分類）**：以 JSON 中四個精確 test name 的 terminal `pass`／`fail`／`skip` event 計數；每個 runner 每條須恰 100 個 terminal event 且全為 pass。terminal fail、package-level FAIL、沒有對應 terminal event、非零 rc、`WARNING: DATA RACE`、`panic`、`timed out`、setup／runner 中斷均依 register 規則 8 檢視原始輸出，分為契約回歸或 setup／資源失效；另統計 invalid/setup/race/timeout，任何一項皆不得吸收到 #7 已知形狀，也不得 rerun。任一門檻不成立即維持 unresolved，依 backlog 回到 B2c-5。
- **D4（通過；分支與落地隔離）**：`b2c7/verify` 可含 backlog rev22、plan 與一次性 workflow，完成 design gate 後只推送一次；run 後以新的 docs 分支自 `main=852c287` 建立，僅 cherry-pick／重建文件 commit，確保 workflow commit 不進 `main` 歷史。成功時回寫 register v7、diagnosis-record v3、backlog rev23；失敗時三份文件保留 unresolved 與逐輪分類。
- **D5（通過；backlog rev22）**：本分支先以獨立 docs commit 關閉 B2c-6（main `852c287`、兩個實作 commit、全套 race、三條有效 mutation、7 檔範圍），並把 B2c-7 改為進行中；估點、小計與 register #7 狀態不變。

## Global Constraints

- 不改 production、既有測試、fixture、timeout 或 oracle；只新增一次性 workflow 與文件。
- workflow 不提供 `workflow_dispatch`、schedule、pull_request 或 main push trigger；不設定 rerun 路徑。
- 不把四個 runner 的綠燈只寫成 aggregate success；每個具名測試須逐 runner 證明 100/100。
- `TestOrphanDoesNotHangNormalExit` 的 5 秒 guard 與所有既有測試內容 byte-identical。
- CI run 等候時間不算工程工時；若 GitHub runner 無法啟動，列 setup/runner invalid，不以補跑替代。
- 證據下載至 `/tmp/b2c7-*`；保留 run id、artifact id、每份 manifest 驗證輸出與分類表。

## Task 1：一次性 workflow

- [x] 新增 `.github/workflows/verify-b2c7.yml`（`54b596c`）；鎖定 branch、matrix、action SHA、Go 版本、三個命令、timeout 與 `if: always()`。
- [x] 本機 YAML parser／結構檢查確認 push-only trigger、2×2 matrix、三個 `-count=100` 命令、四個精確測試名稱、三個 action pin、`fetch-depth: 0` 與 artifact 路徑；三條對應命令各以 `-race -count=1` PASS；manifest 的 `shasum -c` 形狀本機 PASS；`git diff --check` 乾淨。`actionlint` 未安裝，未執行。
- [x] 主 agent 讀碼審查：四條測試名稱在 exact base `852c287` 存在；workflow 沒有 mutation、retry、dispatch、schedule、pull_request 或 main trigger；既有 test／fixture 沒有任何變更。

## Task 2：一次推送與 CI 證據

- [ ] 推送前鎖定 `origin/main=852c287`、驗證分支 HEAD、工作樹乾淨、merge-base；取得 owner 對精確 SHA 的一次推送授權。
- [ ] 只執行一次 `git push origin <verify-head>:refs/heads/b2c7/verify`；以 `ls-remote` 與 GitHub run 的 head SHA 雙重確認。
- [ ] 等四個 runner job 結束；不 rerun。下載 artifact，驗證四份 job-local manifest 與總 manifest，產出逐 runner／逐 test 的 100 次分類表。

## Task 3：register #7 與文件落地

- [ ] 只有四條測試各 400/400 PASS、invalid/setup/race/timeout 計數皆為 0、manifest 全 OK 時，register v7 才將 #7 改 resolved；commit 欄填 B2c-5／B2c-6 已落地 commit，負載重驗欄填本次 run id。任一條件不成立則維持 unresolved。
- [ ] diagnosis-record v3 記錄 exact implementation base、workflow head、run／artifact、逐輪分類與證據限制；backlog rev23 關閉或續開 B2c-7，並依結果更新 B2a 依賴。
- [ ] 從 `main=852c287` 建立 docs 分支，只帶文件，不帶 workflow commit；完成文件 design gate 後另申請 fast-forward 推送 main。驗證分支待 docs 落地並核對後再申請刪除。

## 驗證策略

- 本機：workflow 結構、固定測試名稱、action pin、branch trigger、scope 與 diff check。
- CI：四 runner × 四具名測試 ×100；每條 400/400；raw JSON、rc、環境、程序快照與 SHA manifest。
- Negative control：本票不修改 code／test，不另外植入 mutation；鑑別力沿用 B2c-5／B2c-6 已落地 mutation，B2c-7 只驗跨平台整合結果。
- 無法在本機替代：`macos-15-intel` 與 `ubuntu-latest` runner 的 fork／reap 排程。CI 未完成前不得宣稱 #7 resolved。

## 估點核對

Task 1 約 0.5 hr、Task 2 約 0.8 hr、Task 3 約 0.7 hr，合計約 2.0 hr，在 0.2 pt 中位；CI 排隊與執行等待不計工時。

## Gate A（B2c-7 完成條件）

- [ ] workflow 只推送一次，run head 與核准 SHA 相同，四份 artifact 與 manifest 完整。
- [ ] 四條測試各 400/400 PASS，invalid/setup/race/timeout 計數皆為 0；沒有 rerun。
- [ ] register v7、diagnosis-record v3、backlog rev23 已在不含 workflow commit 的 docs 分支落地；#7 與 B2a 狀態符合實際結果。

## 修訂記錄

- rev2（2026-09-07）：D1–D5 通過；Task 1 workflow `54b596c` 完成；記錄 parser、focused race、manifest、scope 與 `actionlint` 未安裝的驗證邊界；待精確 HEAD 一次推送授權。
- rev1（2026-09-07）：建立；D1 workflow／工具鏈、D2 artifact、D3 分類、D4 分支隔離、D5 backlog rev22；三 Task；估點核對 2.0 hr。
