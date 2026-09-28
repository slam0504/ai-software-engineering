# B3a-CI CI-1／CI-2 限定技術驗收紀錄

> 2026-09-28：CI-1／CI-2 於 review538 限定範圍內技術驗收完成。review541 獨立核對 PR26 合併、main tree 與 main push CI，並修訂本紀錄；文件交付的 PR／CI／合併狀態另行核對。

## 0. 範圍界定

本文件只記錄 CI-1（workflow／wrapper／evaluator 撰寫與離線驗證）與 CI-2（受控遠端執行：13 個正常案例＋兩類負例診斷）在
review538 核定範圍內的技術驗收結果，以及 PR26 併入 main 的合併證據。**不含**：CI-3(a) 日常 `pull_request` 觸發、CI-3(b)
required 化、B3a aggregate 最終狀態判定——這些依契約另案處理，本文件不代為裁定。

## 1. Source Lineage

| 階段 | head | 對應 run/PR | 結果 |
|---|---|---|---|
| 第一次遠端 smoke | `ca6795528e02a3ed4201a70822fa6f88576f1c41`（ca679552） | run `36347781855` | **failure**（收尾無法確認乾淨＋隱藏檔上傳缺口） |
| 第二次遠端 smoke | `c92a86371c5a820e27ccf3a2c20e7b0c7871ffeb`（c92a8637） | run `36354527110` | **failure**（pid 身分比對不符，判為 abandoned） |
| 修正後 source | `91226366d70b8e2f7e949d010b597b269db43183`（91226366） | PR26（`slam0504/ai-software-engineering#26`），head ref `ci/b3a-e2e-integration` | 13 個正常案例全部 success（見第 2 節） |
| main（rebase 合併後） | `a3c3e39a06cb87d92324e70f0c47ce8c7cd1a449` | 合併時間 `2026-09-28T05:18:17Z` | main push CI `36381327268` attempt 1，四個 job 皆 success |

PR26 以 `gh pr merge 26 --repo slam0504/ai-software-engineering --rebase --match-head-commit 91226366d70b8e2f7e949d010b597b269db43183`
（rc=0）完成 rebase 合併；base 為 `f0b1a38943110604ea1d97d20e665bb5e2e2dff9`。相對 base 新增 4 個 commit（GitHub compare API ahead=4
behind=0）：

1. `ca679552` → `4e9307e0`：CI-1 workflow／wrapper／evaluator。
2. `8169b460` → `2198f231`：隱藏檔上傳與執行／封裝 verdict 分列。
3. `c92a8637` → `511a16ac`：C 的有限唯讀後續觀測。
4. `91226366` → `a3c3e39a`：watcher 直接以 Node 啟動 Vite。

main 合併後 tree 為 `331b86aae5ef0a65069b2223316abf7f7fe27feb`，parent `511a16ace66a053c8e7691a75056c4b083a3cc6c`；tree 值與
PR26 head/test-merge tree 一致，main tree 內沒有 PR27 新增的診斷檔（PR27 的 9 個新增檔案未進入 main）。main 的 push CI run
`36381327268`（event=push, headSha=`a3c3e39a…`, attempt=1）四個 job：`checksums`（05:18:23Z→05:18:31Z success）、`frontend`
（05:18:42Z→05:19:41Z success）、`go`（05:19:46Z→05:25:50Z success）、`wails-build`（05:19:48Z→05:27:00Z success），conclusion
success。

PR27（診斷用，head `d5d11efdf3fd799ae6879fa3f40c9545fe20cfba`，branch `ci/b3a-ci2-platform-negative`）**已關閉、未合併**（`closed_at
2026-09-28T05:27:26Z`，`merged=false`）；分支、worktree、label（`b3a-ci2-negative-artifact`、`b3a-ci2-negative-platform`）、
run 與 artifact 全部保留，未刪除。

## 2. 13 個正常案例：run／artifact／digest／expires 對照

三次獨立 run，皆為 source `91226366`、test-merge/checkout `5216e9b2`、tree `331b86aa`、attempt 1、conclusion **success**、
0 retries：

| 批次 | run id | 案例數 | artifact id | artifact digest（sha256） | tar/zip hash 核對 | expires_at |
|---|---|---|---|---|---|---|
| smoke（default＋controls） | `36369137680` | 2 | `10948207802` | `7489132f621c7a2a7c9f76d3f614e46cad61336157e181cdf97cce23b2746f15` | 下載 zip SHA256 與平台 digest 一致；138 個檔案，default tar 27 檔／controls tar 29 檔，tar／manifest／package／readback 各證據集合相符，三個隱藏檔均在 | `2026-12-27T02:15:37Z` |
| gates（gate1／gate2／stale） | `36370124581` | 3 | `10949321331` | `20b11485ec63772da729c4076ade618f8ed671bf1ea5ea0e39a8c8acde99d3eb` | 同上模式，206 個檔案，三案各自 tar 28 檔，各證據集合相符 | `2026-12-27T02:31:07Z` |
| scenarios（8 案） | `36371648990` | 8 | `10950120673` | `1aa85bf884f7e6473c13be1cc3d3516d51f1eda0b0170b9652fcfec567932e75` | 812 個檔案，各證據集合相符；review523 獨立核對 wire／audit／registry 與 broker／UI／MCP／兩輪 resume 的關聯，通過 | `2026-12-27T02:55:06Z` |

controls 的 A 以預期失敗通過，唯一錯誤為指定的存檔斷言；B passed，reverse-check 依設定 skipped。此處「13 案通過」指 entry 判定符合預期，並非每個 Playwright test 都是 passed。

合計 2＋3＋8＝13 個獨立 entry，各自獨立 run-id／輸出目錄，皆 cleanupClean=true、portsReleased=true、
residual/abandoned/unconfirmed 皆空、artifactViolations=0、overallFailed=false，watcher 登記 command 為
`node node_modules/vite/bin/vite.js`（直接 Node 啟動，非 npm 包裝）。C 的 pending 持續觀測路徑在這 13 案中**全數未被自然觸發**
（unconfirmed 事件均為 0），仍未取得自然 ps／kernel 層級的證據——這部分改由第 4 節的負例 Run B 用合成輸入補上受控分支證據。

### 各 entry 的 tar SHA-256

下表來自 review517／519／523 的獨立 tar／manifest／package／readback 核對紀錄；artifact ZIP digest 與內層 tar hash 分開記錄。

| entry | run-id | tar 檔案數 | tar SHA-256 |
|---|---|---:|---|
| `default` | `20260928T021837Z-64df17` | 27 | `545b7c3401b6d1b7ee495593d76033a7b1baad29e63a152cad948cbc11933a33` |
| `controls` | `20260928T022255Z-a75f7b` | 29 | `ebc34de788c044ab5eb8ac6adf3bed9224646572ecf187226e9faafc1cfa393e` |
| `gate1` | `20260928T023528Z-fbf649` | 28 | `cabf8828804885828c9f5bf47dbb5d98140ac99f1e970128b19a97e6cb9305c4` |
| `gate2` | `20260928T024027Z-b27ccf` | 28 | `8fe8c838b1c1474ef73459989bbc6af69a25300f6174f1100b6851a92e5ca632` |
| `stale` | `20260928T024243Z-1ec6bc` | 28 | `dcd71692c1389b51d00d0c3a0fda42326529b791667b97937197f99a1c207d8c` |
| `commandExecution-allow` | `20260928T025640Z-2ec6c8` | 36 | `f7495b1e805538df409fcd37fb67d20c7d2042ffe6617a0686d8589b35223769` |
| `commandExecution-deny` | `20260928T025841Z-361b9a` | 36 | `51e0f81ac06ff11b90fda6cc7ea2d746bcc4b0b39f755531db6e5b53ecc76657` |
| `fileChange-allow` | `20260928T025919Z-e0ccc3` | 36 | `55778ba7b303bdf8e18b7df8f58e03b9f0f937f5509dd6fdb8dbd5271e4d2544` |
| `fileChange-deny` | `20260928T025956Z-ff8a22` | 36 | `cdac529e49b91f498c5aecfe0da6687dea5dc3942c6bb4a5a98772a6f4f0ad4a` |
| `commandExecution-recovery` | `20260928T030034Z-5c8cbf` | 39 | `3e707857b4741de9051d167572980f5f3ed3c14046e09249dd22fbac93ded2e6` |
| `claude-approval-allow` | `20260928T030114Z-59aa89` | 49 | `64ed51e91e8c1563491443e6c3b2565dbcdaed676ce8d946b06916da37f07375` |
| `claude-approval-deny` | `20260928T030152Z-f6616b` | 51 | `cadc060a25863da262594e4de07fc8a3e8481d23159999b088e87fb51eb7c55b` |
| `claude-approval-recovery` | `20260928T030232Z-60d9fd` | 74 | `11707dc5c13be60c0dd84330d5b79d48223db8399df1530b6862ea738d9f8fec` |

Gate／scenario 證據界線：STALE 的 Gate1 stale badge 與 SN1 後 Gate2 active 有 UI 斷言；Gate2 stale 由 durable journal 證明，沒有另一張 UI 截圖。Claude recovery 的 round1 預期 resume 為 null 且 argv 無 `--resume`，round2 才帶同一 session；兩輪 `appBoundResume` 都是 session 值，記錄的是各輪結束後的 registry，兩者語意不同。

## 3. 兩次舊 failure：原因與 erratum 結論

| run | head | artifact digest | expires_at | 原始判定原因 | erratum 修正後結論 |
|---|---|---|---|---|---|
| `36347781855`（attempt-001） | ca679552 | `99a3c17a3b3a7e0359258a71cf8b6e4f3ba0331b692841e13549b232152f7ef0` | `2026-12-26T20:22:23Z` | `globalTeardown` 3 個 pid 身分未確認（46603 stat=`?<`，46629／47768 stat=`?<E`；不是三個都有 E），`cleanupClean=false`、`overallFailed=true`、`wrapperRc=1`；另發現 `upload-artifact` 的 `include-hidden-files=false` 導致 package／readback 各少 3 個隱藏路徑檔（tar 本身完整） | review497 已指出三個 pid 並非全帶 E，且缺少後續死亡／消失證據。main 上 `2198f231` 修正隱藏檔上傳與 verdict 語意；`511a16ac` 新增未確認目標的有限唯讀觀測。原 failure 保留，不能宣稱前者已修復收尾問題，也不能由後續正常樣本推論原三個 pid 已消失 |
| `36354527110`（attempt-002） | c92a8637 | `8a62ffcd8c74d6f22c20aa64a29576d7e0936aa38a595433cad44da91894503b` | `2026-12-26T22:13:28Z` | pid 17224（npm，pgid 17224）登記時 command 為 `npm`，送信號前比對看到 `npm run dev`，pgid／startedAt 相同、僅 command 不同 → 判為身分不符 → abandoned，未送信號，`clean=false` | **erratum（依 review507）**：「登記身分時的時序競態」只是**有原始碼依據的假說**（npm 11.19.1 entry.js 先設 title 為 `npm`、npm.js 再依參數改寫），**不能升格為唯一確定根因**——tracker 分開取樣（先讀 pid/ppid/pgid，之後才查 command/lstart，不再更新已知 PID）、lstart 只有秒精度、沒有同一程序連續持有改名的直接觀測、首跑的 pid 27311 是另一個程序。「不是 C 造成的退步」也**只能縮小到**：用同一組假 snapshot 重播 ca679552 與 c92a8637 兩版皆判 `clean=false`/abandoned，證明這條「command 不符即不送信號」的分類路徑**早於 C 就存在**；**不能**宣稱 C 對整輪時序完全沒有影響。pid 17224 後續是否退出**沒有任何觀測證據**，原報告「應該會自行退出」是推測，不作為結論依據 |

隱藏檔上傳缺口修正已在 attempt-002 上**得到遠端證實**（upload log 顯示 `include-hidden-files: true`，三個隱藏檔在 tar／
package／readback 三份中都在，無 AppleDouble 項目）；attempt-002 的核心失敗原因（pid 身分不符）不是隱藏檔問題，是獨立的
teardown 分類路徑，經 erratum 修正後才定案如上表。

## 4. 負例：Run A 與 Run B（PR27 診斷，未合併進 main）

兩次 run 皆在 head `d5d11efd`（91226366 的子節點）上跑，僅涵蓋 review538 核定的受控範圍。

### Run B（`diag-run-b-001`，run `36379253104`，checkout `9e90bf0d`，conclusion success）

- driver receipt：`overall=passed`，`notRun=[]`，`missingEvidence=[]`，`goldenSourceCheck` 14 筆 `allOk=true`（固定原始 91226366
  的 14 個模組雜湊）。
- **C-persistent-to-deadline**（合成輸入）：`clean=false`、`unconfirmed=[990001]`，只送一次假 TERM、沒有 KILL。
  **C-unconfirmed-then-gone**（合成輸入）：`clean=true`、`unconfirmed=[]`。**這只是遠端 runner 上的受控分支證據，不是自然
  ps／kernel 層級的保證**；13 案正常矩陣中 C 的 pending 路徑仍是 0 次自然觸發。
- wrapper 三情境：obedient（rc124／timeout／`childConfirmedGone=true`）；stubborn（rc124／`timeout-no-clean-exit`／
  `childConfirmedGone=false`，原值保留）；watchdog（rc125／`watchdog-forced-exit`，後續對直接 child 與孫 child 的 `ps -p`
  查詢皆 absent）。**erratum（依 review535）**：孫 child 的 `grandchildEvidence` 是孫 child 自己 exit 前寫的 deadline 紀錄，不是
  ps 查詢結果，真正的 ps 查詢只有 `grandchildProcCheck` 一筆（absent），不是「查了兩次」；三個 wrapper 秒數是 deadline／grace／
  **計算後**得到的 watchdog 秒數，watchdog buffer 實際是 **1**、不是 4（watchdog 情境為 deadline 1、grace 1、watchdog 3）。
- NO-RUN：內層 tar 6 檔，SHA-256 `c7ce8116f40cf543883aeb5253acd9f80a5fe404bd2b168d27e16bced3725ef3`；缺少 candidate 代表缺少可封裝證據，不能推論 App 從未啟動。package rc=1，verdict `no-run/failed/no-run/overall=failed`，`readbackOk=true`，`NO-RUN.txt` 存在。
- artifact oracle：`upload-control` outcome/conclusion 皆 success；`upload-empty` **outcome=failure**、conclusion=success（
  由 `continue-on-error` 造成，erratum 特別指出**不等於** outcome=success，以 `artifact-oracle-observation.txt` 與 oracle
  step log 為準），沒有產生 empty artifact。
- artifact：control `10951094343`（digest `879c762a200c437618b80c9812460be65d92ca28d3e08c7d5b691e6925746d87`，1 檔，
  expires `2026-12-27T04:48:32Z`）；final `10952201377`（digest `7fda798d9285ff17612f847f0db0848b35aed891f1fa7cb021403d836767cf9f`，
  68 檔，expires `2026-12-27T04:48:32Z`）。final artifact 的 evidence-manifest 64 筆 raw 檔案存在/bytes/hash 全部相符，
  receipt 記錄的 manifest hash（`0407d9ec…`）等於實際值。

### Run A（`diag-run-a-001`，run `36380011125`，conclusion **cancelled**，此為有效 cancel 的預期結果）

- **step-timeout**：`step-timeout-and-always` job（05:00:07Z–05:01:33Z，success）中，"intentionally-exceed-step-timeout" 於
  05:01:27Z 因平台 1 分鐘 step 限制逾時（`outcome=failure`／`conclusion=success`）；`always`-observation 全部 success。
  artifact `10951309865`，digest `13001a7dc62bb77f78e336b90515961cfb688179ad141bb716169736641251af`，909 bytes，
  expires `2026-12-27T05:00:03Z`。
- **cancel-target**：ready-marker/upload-ready success，artifact `10952515486`，digest
  `95c94da1ffc63f8640259a083f308b2ebe752904a17477081d2195c8729ff684`，337 bytes，expires 同上。等待步驟 05:01:44Z 開始。
- controller：05:01:51Z 最後核對時等待步驟仍 in_progress（elapsed 7 秒，窗口 120 秒）；只送一次
  `gh api -X POST … actions/runs/36380011125/cancel`，回應 **HTTP 202**，rc=0，無重送、無 force。
- 等待步驟於 05:03:03Z 變成 cancelled（實際 error 為 `operation was canceled`，早於 sleep 180 秒的自然到期 05:04:44Z），
  距送出請求約 **72 秒**；run 於 05:03:08Z 以 cancelled 結束。
- 取消後：step 層級 `always` tail（`observe-tail`）**有執行、success**，`tail-observation` 記錄 `job.status=cancelled`；
  最終上傳**有執行、success**，artifact `10952880394`，digest
  `ff2647e78e51bcffa85051be407a81f9f11c725a12721cd9ae5b769a4f1de299`，684 bytes，expires 同上；內含與取消前逐位元組相同的
  ready-marker。
- **72 秒是這次 API 送出到等待步驟 terminal 的觀察差，不是 signal 送達時間、不是 SIGINT/SIGTERM 的證明，也不是未來的保證
  上限**——這只是一次普通 cancel 且前置證據完整的觀察樣本。

## 5. 保留限制（review538 原文範圍，本文件不放寬也不加強）

1. **job timeout（job 上限自然到期）、force-cancel、平台網路／權限／儲存服務故障、真實（live）
   provider、App／browser 活躍時的完整取消流程**——以上全部**未驗證**。
2. C 的合成分支證據（Run B 的 `C-persistent-to-deadline`／`C-unconfirmed-then-gone`）只是**在遠端 runner 上對合成 pending
   輸入的受控分支證明**，不是自然 ps／kernel 層級的死亡確認；13 案正常矩陣中 C 的 pending 路徑仍是 0 次自然觸發。
3. 13 個正常案例**只跑了一輪**（0 retries），不構成多次穩定性樣本或 cold-start 保證。
4. Run A 觀察到的 **72 秒 cancel 延遲不能當成上限**——這只是一次普通 cancel 的觀察差，不是 SIGINT/SIGTERM 送達時間的證明。
5. Run A 取消後 `always` tail 與最終上傳**這次都有執行、成功**，但**不能推論所有 job timeout／force-cancel／平台故障情境
   下都能保存**；這是單一觀察樣本。
6. 以上未涵蓋項為限制，不因本次技術驗收自動推論成立；日常化／required 等較廣政策不得引用本裁定自動啟用。

## 6. Artifact 保存期限與同機副本聲明

各 run 的 artifact `expires_at`（GitHub 平台觀測值，非本文件承諾）：

- smoke `10948207802`：`2026-12-27T02:15:37Z`
- gates `10949321331`：`2026-12-27T02:31:07Z`
- scenarios `10950120673`：`2026-12-27T02:55:06Z`
- Run B control `10951094343`／final `10952201377`：`2026-12-27T04:48:32Z`
- Run A step-timeout `10951309865`／cancel-ready `10952515486`／cancel-final `10952880394`：`2026-12-27T05:00:03Z`
- attempt-001（舊 failure）`10941635782`：`2026-12-26T20:22:23Z`
- attempt-002（舊 failure）`10943134348`：`2026-12-26T22:13:28Z`

本機 `/Users/eason_tseng/b3a-evidence/ci-2/` 下保存的下載副本，**本次只確認同機持久副本；是否另有異地備份未驗證**；GitHub 平台
artifact 到期後，若本機副本遺失（例如先前 2026-09-27 曾發生 `/tmp` 重開機清空的案例），將無法從平台重新取得。本次未檢查備份基礎設施。

補充證據完整性限制：離線修正期間曾有 cleanup-observation-002 的三份 JSON 原件被後次測試覆寫，原 hash 留存但原 bytes 未恢復；reviewer 的新目錄複驗只證明後來狀態，不能回補原件。另有早期 TypeScript 編譯失敗 log 遭覆寫、一次未依約執行的 fetch，以及超出限定 PID 範圍的唯讀程序掃描，均保留於各輪裁定，不宣稱整段施工毫無流程偏離。

## 7. Watcher 變更：npm hooks／env／PATH 差異

main 上的 `a3c3e39a` 把 `wails.json` 的 `frontend:dev:watcher` 改為 `node node_modules/vite/bin/vite.js`，取代原本經由
`npm run dev` 啟動的路徑。已核對的差異（依 `identity-design-001/main-review/ERRATUM-final.md`）：

- `npm run dev` 本身**未改**；但 `wails dev` 呼叫的 watcher 不再經過 npm，因此不再由 npm 執行 lifecycle hooks（predev/postdev 等），
  也不再由這層 npm 注入 `npm_*` 環境變數與 `node_modules/.bin` PATH；仍可能繼承呼叫端已有的環境。
- 目前的 dev script 只是 `vite`，沒有 predev/postdev，所查的 Vite config 也沒有依賴 npm 環境變數；因此「env／lifecycle
  完全相同」**不成立**，只能說「在目前設定下沒有看到依賴」。之後若新增 hook 或修改 dev script，需要重新檢視這一行設定。
- 這個變更只移除**已知的** npm 與 shebang 包裝層，不保證任何程序的 command 永遠穩定；13 案正式 run 觀察到 watcher 登記為
  `node node_modules/vite/bin/vite.js`、收到 TERM 後完成清理，但這仍是有限次數的觀察樣本。

## 8. CI-3 與 B3a aggregate

- **CI-3(a)（日常 `pull_request` 觸發，仍 non-required）與 CI-3(b)（required 化）本次均未授權、未啟用**；PR26／main 合併後
  workflow 仍維持精確 label 觸發、限定分支、non-required，未改動一般 PR 自動 E2E、排程或 merge queue。
- **B3a aggregate 的最終狀態不在本文件範圍內**，需依原契約另行判定受控整合與日常啟用的分野，不因本次 CI-1／CI-2 技術驗收
  完成就宣稱 required 已完成或 B3a 全案已關票。

## 9. 估算與工時

decision470 已採 CI-1 的 10–18 hr（中位 14 hr／1.4 pt）作施工規劃基準；CI-2 的 8–16 hr（中位 12 hr／1.2 pt）仍屬 proposal。兩者都不是已投入工時，本次不回填 backlog 點數或合計。active 工時未獨立量測；GitHub job／session 的牆鐘時間包含等待，不能直接換算工程量。

## 出處清單

repo 外證據路徑均相對本次保存根目錄 `/Users/eason_tseng/b3a-evidence/`；它們是本機索引，不是可公開存取的連結。

| 內容 | 檔案 | 欄位 |
|---|---|---|
| review541 合併及文件複核 | `2026-09-28-review541/` 下 `main.json`、`main-commit.json`、`main-ci.json`、`main-jobs.json`、`pr26.json`、`pr27.json` | GitHub API 當輪取得值 |
| normal tar 與 domain 複核 | `2026-09-28-review517/artifact-verification.json`、`2026-09-28-review519/artifact-domain-verification.json`、`2026-09-28-review523/verification.json` | tar hashes／run-id／domain 關聯與證據界線 |
| failure 與離線證據事故 | `2026-09-28-review497/decision.md`、`2026-09-28-review499/decision.md`、`2026-09-28-review503/decision.md` | 原 failure 分類、證據覆寫及流程偏離的保留紀錄 |
| decision538 裁定全文、Run A 核對、PR26 合併條件、文件收尾授權 | `2026-09-28-review538/decision.md` | 全文 |
| PR26 merge 結果、main tree／CI、PR27 close 狀態 | `ci-2/merge-pr26-001/pr26-after-merge.json`、`pr27-after.json`、`main-after.txt`、`main-compare.txt`、`main-ci.json`、`merge-cmd.txt`、`merge.rc`、`pr27-close.log` | `merged_at`／`merge_commit_sha`／`head.sha`／`base.sha`；main tree／parents；ahead/behind commits；push CI `id`/`attempt`/`conclusion`/`jobs` |
| PR26／PR27 body 內容與 hash 核對 | `2026-09-28-review538/pr26-body.md`（SHA256 `960f349e…`）、`pr27-body.md`（SHA256 `e68f0fe0…`） | 全文；hash 經 `shasum -a 256` 核對與 decision538 §5 相符 |
| smoke 13 案中的 2 案 | `ci-2/attempt-003/SUMMARY.md`、`ci-2/attempt-003/remote/artifacts.json` | run id／head／tree／verdict／artifact digest／expires_at |
| gates 3 案 | `ci-2/gates-attempt-001/SUMMARY.md`、`ci-2/gates-attempt-001/remote/artifacts.json` | 同上，另含 journal 行數／Gate1/Gate2/STALE 判定 |
| scenarios 8 案 | `ci-2/scenarios-attempt-001/SUMMARY.md`、`ci-2/scenarios-attempt-001/SUMMARY.ERRATUM.md`、`remote/artifacts.json` | 同上，另含 resume/approval 欄位語意澄清 |
| 第一次舊 failure | `ci-2/attempt-001/SUMMARY.md`、`ci-2/attempt-001/remote/artifacts.json` | run id／teardown 原因／artifact digest／expires_at |
| 第二次舊 failure＋erratum | `ci-2/attempt-002/SUMMARY.md`、`ci-2/attempt-002/SUMMARY.ERRATUM.md`、`ci-2/attempt-002/remote/artifacts.json` | 同上，erratum 三點修正 |
| Run A 負例 | `ci-2/diag-run-a-001/SUMMARY.md`、`controller-run/artifacts-1.json`、`artifacts-2.json`、`remote/artifacts.json` | run/job 時間戳、controller cancel 呼叫、artifact digest／expires_at |
| Run B 負例＋erratum | `ci-2/diag-run-b-001/SUMMARY.md`、`SUMMARY.ERRATUM.md`、`remote/artifacts.json` | driver receipt、C 合成分支、wrapper 三情境、NO-RUN、artifact oracle、erratum 三點修正 |
| watcher npm 差異 | `ci-2/identity-design-001/main-review/ERRATUM-final.md` | 全文（2. npm 環境差異段） |
| CI-1 規劃基準／CI-2 proposal（均未改 backlog 點數） | `2026-09-27-recovery/contracts/b3a-ci-design-draft-v2.md` §6、`2026-09-27-recovery/contracts/decision470.md` | decision470 核定 CI-1 10–18 hr／中位 14 hr 為規劃基準；CI-2 8–16 hr 仍為 proposal；均非實際工時，未改 backlog 合計 |
| repo 現況參照格式 | `docs/architecture/evidence/2026-09-25-b3a-2a-gates-closure.md`（經 `git show 91226366:…` 讀取） | 章節結構、保留限制寫法沿用 |
| backlog 既有 B3a-CI 票列 | `docs/architecture/pre-m4-readiness-backlog.md`（經 `git -C ai-se-worktrees/b3a-ci-1 show 91226366:docs/architecture/pre-m4-readiness-backlog.md` 讀取，line 415） | 現行 0.4 pt 列與狀態文字 |

**未驗證（缺證據，本文件不代為判定）**：job timeout 自然到期路徑、force-cancel、平台網路／權限／儲存故障、真實 provider、
App/browser 活躍時完整取消流程、13 案在多輪重跑下的穩定性、cancel 延遲的真正上界、`always()` 保證上傳的通用性。
