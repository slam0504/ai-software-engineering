# B3a-CI 單次可行性試跑紀錄

> 日期：2026-09-21（台灣時間）。以下量測時刻一律為原始 UTC 值。
> 結論：**單次 macOS runner 可行性驗收完成；正式 CI 整合另立票。**
> **綠燈不等於 CI 能力驗收通過**——三項判定分列於下。

## 1. 受測對象與來源

| 項目 | 值 |
|---|---|
| 實驗 PR | [#12](https://github.com/slam0504/ai-software-engineering/pull/12)（draft、標示 DO NOT MERGE、**已關閉未合併**） |
| 分支 | `ci/b3a-e2e-feasibility`（保留） |
| head | `79819d8562847b3ff8ec1cdae4fdf3...`（PR head branch 當下 commit） |
| base | `b42025fa598f7ad58cec97554984c5233a67cea4`（main） |
| 實際 checkout | `564a7cf07235827266b645c945e1356f0ffd0faf`（`git rev-parse HEAD` 實測；`pull_request` 事件下為 merge commit，**不等於 head**） |
| run | [35526092898](https://github.com/slam0504/ai-software-engineering/actions/runs/35526092898)（`event=pull_request`，label `run-e2e-smoke` 僅貼一次、零重試） |

實驗只新增 4 個檔（1 支 workflow ＋ `.github/scripts/` 3 支腳本），**未修改** `ci.yml`、ruleset、production 或 harness。

## 2. 時程（UTC，取自 job step 時間戳與 `harness.log`／`e2e.timestamped.out`）

| 階段 | 時間 | 耗時 |
|---|---|---|
| 排隊（created → job started） | 17:30:42 → 17:30:46 | 約 4 s |
| job 全程 | 17:30:46 → 17:35:54 | **5 m 08 s** |
| setup-node / setup-go | 17:30:53 → 17:31:59 | 13 s / 53 s |
| `npm ci` → frontend build | 17:31:59 → 17:32:49 | 19 s / 31 s |
| `go mod download` / `go install wails` | 17:32:49 → 17:32:53 | 1 s / 3 s |
| **e2e smoke step** | 17:32:54 → 17:35:38 | **2 m 44 s** |
| 其中 `wails dev` 就緒 | 17:32:58 → 17:35:16 | 約 2 m 18 s |
| **`globalSetup 完成` → `Running 1 test`** | 17:35:16.264Z → 17:35:17.904Z | **1.640 s** |
| package evidence → artifact 上傳 | 17:35:39 → 17:35:41 | < 1 s / 2 s |

## 3. 環境

- runner：`macos-15-intel`；**Image `macos-15`、Version `20260824.0482.1`**
  （同一段 log 另有 `Version: 20260819.586`，那是 runner provisioner 版本，**不是 image 版本**）
- Node `v26.9.0`、Go `go1.26.5 darwin/amd64`、wails `v2.13.0`
- Chrome **151.0.7922.174**（runner image 內建；**與本機 153.0.8010.47 不同**，本輪未固定 Chrome 版本）

## 4. 結果與證據核對

- job conclusion `success`；Playwright **`1 passed`**（測試確實執行，非略過）
- wrapper：`status=completed`、`wrapperRc=0`、`childRc=0`、`producerErrors=[]`
- `run-state.json`：`runId=20260920T173254Z-83f749`、`status=stopped`、`failureStage=null`、`observationFailures=[]`，runId 與目錄名一致
- 最後 teardown 判定：`overallFailed=false`、`cleanupClean=true`、`artifactViolations=0`、`portsReleased=true`，`residualPids`／`residualPorts` 皆空
- artifact `e2e-smoke-output`：**23/23 檔 manifest sha256 全符**
  大小 **41 118 bytes（約 40.15 KiB）**，來源為 GitHub artifact API `size_in_bytes`
  （本機 `du` 顯示的 48 KB 是檔案系統配置空間，**非下載產物大小**，兩者來源不同）
- 由 reviewer（codex-reviewer）**獨立下載、獨立解包、獨立核對**，結論一致

## 5. 三項分開判定

| 面向 | 結論 |
|---|---|
| (a) 功能可跑 | **本次單一樣本成立**：相依可安裝、`wails dev` 可就緒、Chrome 可啟動、smoke 通過、收尾乾淨 |
| (b) 可診斷 | **本次成立，但僅限成功路徑**：證據齊全、manifest 可驗、wrapper 狀態與 rc 一致 |
| (c) 成本／耗時適不適合常態 CI | **未定**：單一樣本；**計費無帳務證據，列為未知**；與既有 `go`／`wails-build` 共用 runner pool 的競爭未評估 |

## 6. 未測與已知限制

1. **遠端失敗／逾時／取消路徑完全未測**：本次未觸發任何 timeout 或 cancel，平台實際如何送訊號仍未知。wrapper 的 timeout／observation-error／producer-error 路徑僅有本機隔離 fixture 驗證。
2. 成功路徑本就不保留 trace，trace 缺失依既有成功／失敗契約判斷。
3. **本機「globalSetup 完成 → 測試開始」曾有 11–12 分鐘成因未知的耗時，本次在 CI 未重現**（實測 1.640 s）。**僅為觀察，成因仍未知**，不得據此推論本機問題的原因。
4. Chrome 在 runner 上輸出多筆 `CVDisplayLinkCreateWithCGDisplay failed (CVReturn: -6670)`，測試仍通過。方向上與「runner 為模擬顯示」的假設一致，但**該假設未經驗證**（來源為社群討論，非官方文件）。
5. 單一樣本、單一 Chrome 版本、單一時段。
6. **實驗稿已知措辭欠缺**（正式整合前必須清除，本輪為保全已採證的 head 而**未修改、未重跑**）：workflow 第 183 行「一定會寫出」、第 244 行「wrapper 保證」、第 213 行仍寫秒精度；evaluator 第 337 行的 NO-RUN 仍宣稱「確定未開始」——實際上 **NO-RUN 只代表未取得 run 證據，不能證明從未啟動**。
7. 主機或檔案系統整體 I/O 卡死時，最後界線仍是平台的 step 30 分／job 45 分限制，資料可能不完整。

## 7. 正式整合（另票）需涵蓋

- 真實失敗情境的 trace 產出與上傳
- timeout 與 cancel 的實際行為
- Chrome／runner image 版本策略與觸發策略
- 多次執行的穩定性與成本觀察（含與既有 job 的 runner 競爭）
- 清除第 6 點所列的措辭欠缺

## 8. 工時

B3a-CI 維持原估算 **0.4 pt**（3–5 hr，中位 4 hr）。**實際 active 未知**（多輪未逐秒量測，含一次施工 agent 卡死未交出計時），**不重算合計**。CI 等待另列：本次 run 牆鐘 5 m 08 s。
