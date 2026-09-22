# B3a-2b-2 最終快照回歸紀錄（default／controls）＋範圍裁定

> 建立：2026-09-22（台北時間）。範圍：**在最終合併快照上，把 `default` 與 `controls` 兩個既有 browser 入口各實跑一次**，補上 Task C 之後尚未在最終快照執行的共用路徑回歸。
> **codex-reviewer 複核 #414 後裁定：Task F／B3a-2b 於 §7 的限定範圍內技術驗收完成，可技術關票；結案文件尚待提交與合併。**B3a aggregate 另需 B3a-2a 與正式 CI 整合，不由本文件承擔。

## 0. 為什麼要補這一次回歸

本系列（Task C → D → E → F1／F2 → Claude recovery → Claude deny）的改動雖然集中在 scenario 專屬路徑，但確實碰過 `frontend/e2e/support/env.ts`、`support/executionMode.ts`、`global-teardown.ts`、`frontend/package.json` 等 **`default`／`controls` 也會走到的共用檔**。

追溯 repo 內可指出的紀錄：

| 輪次 | `default`／`controls` 實跑紀錄 |
|---|---|
| Task C | **有**（結案載明「三次真跑（scenario／default／controls）皆 rc=0」）——但屬**當時的 source 快照** |
| Task D | **無**，結案明載「本輪未重跑（**票面明訂**，只動 scenario 專屬路徑）」——那是該輪 scope 決定，不是「最終回歸不需要」的裁定 |
| Task E、F1／F2、Claude recovery、Claude deny | **無執行紀錄，也無對應的排除裁定** |

因此 Task C 的紀錄**不能無說明充當最終回歸**（source 已變五代）。本次在最終合併快照上補跑。

## 1. 受測快照

| 項目 | 值 |
|---|---|
| base／HEAD | **`aec7d2588509d8e404e72f7423d38697b8ab2cb5`**（PR #21 的 rebase mergeCommit） |
| tree | `6d5e45da28a8559305d0a8e74a2bd78e5134dfdc`（與 PR #21 head 完全相同） |
| parent | `46df1d888ed7146098de53299f22c0e2f0c7a8f3`（PR #20 的 mergeCommit） |
| 工作樹 | 專為本次回歸新建的獨立 detached worktree，**兩次 run 前後 `git status --porcelain` 皆為空** |

`default`／`controls` 的正式 config 與 npm 入口**一律未改**（`playwright.config.ts`／`playwright.controls.config.ts`／`test:e2e`／`test:e2e:controls`）。

## 2. 啟動前核對（兩次共同）

- 環境中**沒有任何 `E2E_*` 或 `GO*` 變數**；兩次執行均以 `env -u E2E_SCENARIO -u E2E_CONTROLS_REVERSE_CHECK` 明確清除 scenario 與 reverse-check 旗標，只設 `E2E_KEEP_ARTIFACTS=1`。**未啟用任何故障注入旗標**。
- 34115／5173 皆無 LISTEN；無殘留 `wails`／`sdlc-workbench`／`vite` 程序；無 `.active-run.json`。
- test discovery（`--list`，不啟 browser／globalSetup）：`default` = **1 test**（`glossary.spec.ts`）；`controls` = **3 tests**（`reverse-check.spec.ts`、`save-detection.spec.ts` 的 control A／control B）。
- **照實記錄的副作用**：上述 `--list` 因當時無 `E2E_ARTIFACTS_DIR`，依 config 預設 `outputDir` 產生了 `frontend/e2e/.artifacts/no-run-id/playwright-results.json`（僅此一檔）。它不屬於任何一次正式 run 的證據，也不影響 git 狀態。

## 3. Run A — `default` smoke

| 項目 | 值 |
|---|---|
| 命令 | `E2E_KEEP_ARTIFACTS=1 npm run test:e2e` |
| run-id | **`20260922T003535Z-885415`** |
| 結果 | **PASSED**，rc=0，`1 passed (3.4m)` |
| 牆鐘 | 2026-09-22T00:35:33Z → 00:39:02Z |
| 執行模式判定 | `default`（`execution-entry.json` = `{"entry":"default"}`，`run-env.json` 的 `scenario` 為 undefined） |
| 假 CLI 版本 | `fake-claude 0.0.0-e2e+<run-id>`／`fake-codex 0.0.0-e2e+<run-id>`（run 專屬字串） |
| tripwire | claude 4 次、codex 4 次、**違規 0 筆**；`invocations.log` 8 行**全部是 `--version`**（default 入口不得有其他 argv） |
| network | `browser-network-violations.log` **0 bytes**；`network-samples.log` 497 行 |
| 收尾 | `clean=true`、`portsReleased=true`、TERM 1614ms／KILL 0ms、`escalatedToKill=false`、`wails dev exit code=0` |
| run-state | `status=stopped`、`observationFailures=[]`、23 筆程序 |
| 總判定 | `interrupted=false testFailed=false cleanupClean=true tripwireViolations=0 networkViolations=0 runStateReadFailed=false observationFailures=0 artifactViolations=0 → overallFailed=false` |

## 4. Run B — `controls`

| 項目 | 值 |
|---|---|
| 命令 | `E2E_KEEP_ARTIFACTS=1 npm run test:e2e:controls` |
| run-id | **`20260922T003926Z-3503c4`**（與 A 不同） |
| 結果 | **PASSED**，rc=0，`1 skipped`／`2 passed (2.7m)` |
| 牆鐘 | 2026-09-22T00:39:25Z → 00:42:07Z |
| tripwire | claude 6 次、codex 6 次、**違規 0 筆** |
| network | `browser-network-violations.log` **0 bytes**；`network-samples.log` 554 行 |
| 收尾 | `clean=true`、`portsReleased=true`、TERM 1532ms／KILL 0ms、`escalatedToKill=false`、`wails dev exit code=0` |
| run-state | `status=stopped`、`observationFailures=[]`、26 筆程序 |
| 總判定 | `overallFailed=false` |

### 4.1 control A 必須「失敗在目標斷言」——逐項核對

**不是看到 suite rc=0 就算過。** 自 `playwright-results.json` 直接讀出：

- `expectedStatus=failed`、`status=failed`、`ok=true`（Playwright 把預期失敗計為通過）
- **`errors.length === 1`**，唯一那筆的訊息是**目標斷言本身**：
  `Error: 對照 A（舊判定）預期失敗：讀到的應是舊內容而非新內容`
  → 沒有其他斷言先失敗而被 `test.fail()` 吸收。

`control-a-evidence.json` 另外證實舊判定的前提成立：

- `diskSha256` = `baselineSha256` = `2c0cf7f9014cbb01c2023d17043532359bfc37f0842b072afb34e9bc5792d8fb`
  → 舊訊號當下讀到的**確實是延遲注入前的 baseline**。
- `wrapperEvidence` **恰一筆**，`actualDelayMs = 2000`（**≥ 2000ms**），且寫入內容是本次 run 專屬的新內容字串。

### 4.2 control B

`control-b-evidence.json`：`finalDiskContent` **逐字等於** `expectedNewContent`（本次 run 專屬的完整新內容）；`wrapperEvidence` 恰一筆、`actualDelayMs = 2001`（≥ 2000ms）。

### 4.3 reverse-check

`expectedStatus=skipped`、`status=skipped`——**依 spec 的既有設計，只在 `E2E_CONTROLS_REVERSE_CHECK=1` 時執行**。**本次未獲授權啟用，故未執行**；這一項在本輪**沒有**取得任何證據。

## 5. 證據 manifest

| 項目 | 值 |
|---|---|
| Run A（21 筆，artifacts 全檔 ＋ `run.log`） | `/tmp/regressM/RUN-A-SHA256SUMS`，SHA256 `d180c97cae46073b02fd74bfa2bccf6ef9d2232225428b09a353f1ea3442ec9e` |
| Run B（24 筆，同上，含 `playwright-results.json`／`control-a-evidence.json`／`control-b-evidence.json`） | `/tmp/regressM/RUN-B-SHA256SUMS`，SHA256 `e0ea9e882049a2d674b36730299a1e4de61a22cd5619ed599ca6b54f3a4a83f2` |
| 原始 stdout | `/tmp/regressA/run.log`、`/tmp/regressB/run.log`（rc 檔同目錄） |
| 證據目錄（`E2E_KEEP_ARTIFACTS=1` 保留） | `frontend/e2e/.artifacts/20260922T003535Z-885415`、`…/20260922T003926Z-3503c4` |

**source 前後**：兩次 run 之前與之後，worktree 的 `git status --porcelain` 皆為空、HEAD 與 tree 未變——前後檢查未觀察到受版控檔案變動。事後 live `lsof`／`ps` 核對：34115／5173 無 listener、無殘留程序。

**網路**：違規檔 0 bytes ＋ 取樣 497／554 行。**這是取樣結果，不足以宣稱全程封包層無外部網路。**

## 6. 合併紀錄

| PR | 範圍 | mergeCommit | tree | mergedAt |
|---|---|---|---|---|
| #20 | Claude same-App recovery 限定檢查點 | `46df1d888ed7146098de53299f22c0e2f0c7a8f3` | `0ba776bf…` | 2026-09-21T23:49:39Z |
| #21 | Claude approval **deny** 限定檢查點 | `aec7d2588509d8e404e72f7423d38697b8ab2cb5` | `6d5e45da…` | 2026-09-22T00:33:27Z |

兩者皆為 rebase merge、tree 與各自的 PR head 完全相同，CI 四項（`checksums`／`frontend`／`go`／`wails-build`）皆 success。**一般 CI 不執行 browser E2E**，不得以 CI 取代本機 browser 證據。

## 7. 範圍裁定（codex-reviewer，mailroom #411，2026-09-22）

以下是**本次 reviewer 決策**，不冒稱歷史上早已存在的裁定：

1. **本系列 Task F／B3a-2b 的 recovery 驗收範圍**定為「同一 App 執行期內、既定 WSID 的 End→resume」；approval 為已驗收的 Codex 四案與 Claude allow／deny 檢查點。兩個 provider 皆用受控 fake；各 closure 的保留限制原樣不變。
2. **App 真正冷啟動讀盤恢復與上述流程分開**，列為後續候選票（見 §8）。**現在不實作、不給新點數、不稱已驗**，也**不拿 browser reload 或 same-App resume 代替**。
3. **`default`／`controls` 的最終快照回歸**列為 2b 關票前的剩餘驗證——即本文件 §3／§4。

**B3a aggregate 仍未完成**：條件 (1) 的 Gate 1／Gate 2／STALE 屬 **B3a-2a**（未授權施工），條件 (3) 的正式 CI 整合屬 **B3a-CI**（單次可行性已驗收、正式整合另票）。

## 8. 後續候選票：App 冷啟動後歷史／session 狀態恢復驗證

- **狀態**：候選票，**待設計／估點／施工授權**。尚未核定為要求，也**未**給任何點數。
- **與既有覆蓋的關係**：本系列已驗的是**同一 App 執行期內**的 End→resume；冷啟動涉及 App process 重新執行整段初始化（workspace registry 載入、事件歷史掃描、legacy 遷移檢查、single-instance lease 等），**browser reload 與 same-App resume 都不可替代**。
- **溯源**：repo 外的 `/tmp/b3a2s-2b-design-v2.md`（SHA256 `17d89ee55e9f17753d8590dd3d62dd644860710d0459cc9815ccdf6a26fda178`）§6 曾提出此項。該檔**自述是設計草稿**，repo 內**沒有**把它核定為要求、排除或拆票的紀錄；本文件依 §7 第 2 點把它明確拆開，不再懸置草稿解讀。上述「browser reload 不觸發冷啟動路徑」是該草稿的讀碼結論，**本輪未重新驗證**。
- 草稿 §6 另有一個自述「待釐清」項「End 後再送出」——該項**實際已被後續工作覆蓋**（Codex 側 Task E、Claude 側 PR #20 的 recovery 檢查點）。

## 9. Task F／B3a-2b 目前狀態

**限定功能與最終快照回歸均已驗收；Task F／B3a-2b 於 §7 範圍內技術驗收完成。**

| 流程 × provider | 狀態 | 依據 |
|---|---|---|
| approval／Codex | allow＋deny（2 methods × 2 decisions 四案） | Task C、Task D |
| session recovery／Codex | 已驗（單一 `commandExecution`、兩輪 accept） | Task E |
| approval／Claude — allow | 已驗 | Task F1／F2 |
| approval／Claude — deny | 已驗 | deny closure（PR #21） |
| session recovery／Claude | 已驗（fresh → UI End → `--resume S`，兩輪 allow） | recovery closure（PR #20） |
| `default`／`controls` 最終快照回歸 | **本文件 §3／§4，reviewer 已複核通過** | — |

reviewer 已獨立核對 Run A 21/21、Run B 24/24 manifest 與完整檔案覆蓋，解析 controls report 確認 A 的唯一錯誤位於 `save-detection.spec.ts:128`，並核對兩份 wrapper 證據、最終磁碟內容、停止狀態。複核當下再次查詢兩次 run 追蹤的 PID 均不存在，34115／5173 無 LISTEN。此裁定不包含冷啟動、真實 provider 或正式 CI；結案文件交付狀態另列。

## 10. 保留限制

1. 兩次回歸各為**單次樣本**，不宣稱重複穩定性。
2. `reverse-check` 本輪**未執行**（預設跳過，未獲授權啟用），該反證在本輪無證據。
3. 網路證據是**取樣**，不是封包層全程證明。
4. provider 在所有 scenario 檢查點一律是**受控 fake**；真實 Claude／Codex provider 未驗（B3a 條件 (2) 明文把 live 驗收列為獨立項目、不作 required check）。
5. 冷啟動、server replacement、多 session、三輪以上、逾時自動 deny 實跑、空理由 deny 的 browser 案皆未驗。
6. `fakeAppServer.selftest.ts` 的 `waitForChildExit` 間歇逾時仍未解（本輪為 default／controls 入口，未涉及該 suite）。
7. `/tmp` 與 worktree 路徑非跨機器可取得證據。
8. **active effort 未量測**（無可靠量測工具），不以牆鐘回填；未核定任何新點數。

## 11. 證據保存界線與文件交付

- `GOPROXY=off` 與 `npm_config_offline=true` 由既有 `support/processTree.ts` 設於 Wails 子程序環境；啟動 shell 未設 GO 變數不等於子程序未設離線選項。本輪未另保存子程序完整環境快照。
- default spec 的成功結果包含 CLIInfo workspace／tools／版本／startupError 斷言；未另保存 CLIInfo 原始回傳值，controls 只等待 ready，不宣稱它另做了相同的逐欄比對。
- source 前後證據是 git status／HEAD／tree，未另提供每次執行前後的全 source SHA256 清單；reviewer 複核當下工作樹仍乾淨且 HEAD／tree 相符。
- 本次 artifacts 沒有 trace 或 screenshot；controls 保留 JSON report 與目標斷言 error-context。`reverse-check` 未執行的限制不變。
- 截至本次 reviewer 複核，僅這份結案與 backlog 兩份 docs 尚未 commit／push／PR；技術驗收完成不等於文件已合併。
