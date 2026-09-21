# B3a-2b-2 Task D 限縮補正（F1／F2）結案紀錄

> **最終狀態（codex-reviewer 裁定，mailroom #288）：Task D（Codex 2 methods ×
> allow／deny browser matrix，含 F1／F2）技術驗收通過，保留下方明列限制。
> B3a-2b aggregate 尚未完成**——兩者狀態分開，本文件不構成 aggregate 的驗收結論。
>
> 撰寫者：Claude controller 與其指派的執行者。本文件涵蓋 Task D 四案 matrix、
> F1（App wire `meta.json` 由 optional 升格為必要證據，含最後的共用保存 guard）
> 與 F2（resolver `Object.prototype` 漏洞修正）。**下方各節保留歷次回合的原始
> 結論與失敗事實，不因最終通過而改寫成 PASS。**

## 0. 最終驗收結論與驗證邊界（#288）

- **Task D 技術驗收通過**；**B3a-2b aggregate 未完成**。
- **F1 最後一項阻擋（共用保存 guard）**：`wireEvidence.ts` 的
  `assertWireEvidencePersisted` 是 `codexApproval.spec.ts:333` 與
  `wireEvidence.selftest.ts` **實際共用的同一個判定**——`wireLogCopied` 或
  `metaCopied` 任一為 false 即 throw；原 wait／validation 錯誤優先，先保存的
  流程不變。
- **測試證據**：`selftest:scenario-wire-evidence` **24 passed**（`ok` 行數實際
  數過為 24）、`typecheck:e2e` rc=0。**Mutation 驗證**：移除 `metaCopied` guard
  → rc=1 且 FAIL 的正是 meta-only 那條（`Missing expected exception`）；還原後
  24/24 rc=0。**該 mutation 由 Claude controller 執行，reviewer 讀原始 log 核對，
  非 reviewer 另跑一次。**
- **驗證層級邊界（不得混為一談）**：
  - **四案 browser 證據**來自較早 source 階段的 Claude controller 重跑；
  - **單案整合確認**（`20260921T095328Z-f57a3c`）亦為既有 run；
  - **最後的保存 guard 變更**只以 targeted selftest ＋ typecheck 驗證，
    **未宣稱最終每一行 source 都重跑過四案 browser**。
- **evidence bundle**（各自 manifest，互不覆寫）：
  `original-bundle-copy` 143/143、`fixed-bundle` 72/72、
  `addendum-full-artifacts` 121/121、`final-candidate` 40/40
  （`final-candidate` digest `726ef827d3078d9be0465e6bedd50c6df43d9c859ccd34a2d57a5ed36e441822`）、
  以及本次交付補齊的 `delivery-addendum`（見該目錄 `SHA256SUMS`）。

### 0.1 程序偏差（Claude 回報，非事前獲准）

以下為 **Claude 回報的程序偏差**，**不寫成事前獲得許可，也不因技術驗收通過而消除**：

| 回合 | 授權上限 | 實際 active | 說明 |
|---|---|---|---|
| F1／F2 補正 | 25 分 | 施工者約 30–35 分 | 施工者自報超時，未壓縮驗證步驟 |
| 同上（複核） | — | Claude controller 約 12 分 | 併計後超出當時上限 |
| 保存 guard 補正 | 15 分（合計） | 約 18 分 | Claude 自行判斷續做完更正說明與 bundle |

**裁定（#288）**：**已明確授權的時間上限不得自行延長**；到限應回報已完成與未完成項目。

## 1. 背景

codex-reviewer 對 Task D（四案 browser matrix：`commandExecution-allow`／
`commandExecution-deny`／`fileChange-allow`／`fileChange-deny`）的裁定：**四案
功能證據成立，但尚未技術驗收**，需先完成兩項限縮補正：

- **F1（阻擋驗收）**：`frontend/e2e/scenarios/codexApproval.spec.ts` 原本用
  `if (fs.existsSync(chosenMetaPath))` 把 app wire 的 `<id>.meta.json` 當成
  optional 證據——缺了不失敗、不留觀測失敗紀錄。實證：首輪四案中
  `commandExecution-deny`（run `20260921T084322Z-8037b2`）確實缺
  `app-wire-log.meta.json`，其餘三案有。時序風險（讀碼佐證，**非確認根因**）：
  `internal/wirelog/wirelog.go` 的 `Generation.Finalize` 先 close wire 檔再寫
  meta；`internal/codex/owner.go` 的 `WatchGeneration`／`FinalizeWith` 在 fake
  app-server 子程序**自然退出後才非同步觸發**收尾，測試碼讀取 wire-logs 目錄的
  時間點與這個非同步收尾之間存在競態。
- **F2（小型契約補正）**：`resolveScenario` 用 `SCENARIOS[name]` 存取一般
  object，`name` 為 `Object.prototype` 上的任何成員（`toString`／
  `constructor`／`__proto__`／`valueOf`／`hasOwnProperty` 等，Claude controller 實測
  確認不限 reviewer 原列的三個）都會從原型鏈取值、不 throw，不符合「未知一律
  在 resolver 拒絕」的契約。**這不代表曾經成功啟動錯誤案例**，只是不符合契約。

## 2. F1 修正內容

新增純函式模組 `frontend/e2e/support/scenario/wireEvidence.ts`（scenario 專屬
小型 helper，未動任何凍結檔）：

- `selectGenerationsByIdentity(wireLogsDir, identity)`：以「generation 內容含
  本案 approval request（s2c、method＋id 相符）」為準挑選候選 generation，
  **不是只挑 mtime 最新**；回傳依 mtime 新到舊排序的候選陣列。
- `waitForWireMeta(metaPath, { deadlineMs, pollIntervalMs })`：對「同一個本次
  generation」的 meta 做有上限的輪詢等待（預設 15 秒級上限，本輪四案重驗實測
  均在數十毫秒內就緒，見第 5 節）。不固定 sleep；meta 尚未出現或
  `JSON.parse` 失敗（非原子寫入造成的暫態不完整內容）都視為 transient、繼續
  輪詢；deadline 到仍未成功，丟出 `WireMetaWaitError`（帶 `metaPath`／
  `deadlineMs`／`attempts`／`lastError`），不吞、不合成假 meta。
- `validateWireMeta(meta, exp)`：核對 `provider=codex`、`exit_code`
  必須是數字且為 0（缺少／非數字一律違規，不當成 0）、`recorder_error`／
  `cleanup_incomplete` 不得存在、`finalize_cause` 保留在回傳的 meta
  裡但若含逾時等異常訊號（`/timed out/i`）仍判違規（不因 `exit_code=0`
  就放過）、`argv[0]` 的 canonical path 須等於本次 codex wrapper（
  `<toolsDir>/codex-cli/node_modules/.bin/codex` 的 realpath）、其餘 argv
  段落須等於 `['app-server']`。

`codexApproval.spec.ts` 的呼叫順序（見該檔 F1 段落註解）：

1. `selectGenerationsByIdentity` 挑生成的 generation；零候選直接 fail loud
   （`harness.log` 記錄 run-id／wireLogsDir／identity）。
2. `waitForWireMeta` 等待該 generation 的 meta；失敗時把 run-id／generation／
   deadline／最後一次錯誤寫進 `harness.log`，並保留能取得的 wire jsonl 原始檔
   （meta 本身不合成），然後 rethrow（不阻擋既有的 bounded teardown）。
3. `validateWireMeta` 核對通過（`harness.log` 記錄等待耗時／嘗試次數）才
   `fs.copyFileSync` 複製 wire jsonl＋meta 到證據目錄。
4. 後續的 wire 內容斷言（approval request／decision frame 核對）一律讀
   **複製後的副本**（`env.artifactsDir` 底下那份），不再讀來源路徑——避免
   「副本先取、判定卻讀另一個時點的來源」。

## 3. F2 修正內容

`frontend/e2e/support/scenario/scenarios.ts` 的 `resolveScenario`：把
`SCENARIOS[name]` 改為先 `Object.hasOwn(SCENARIOS, name)` 判定，只認真正登記
在 `SCENARIOS` 自身的 key，繞開整條原型鏈。

`frontend/e2e/support/scenario/scenarios.selftest.ts`：

- 檔頭執行說明改為真正可用的 `npm run selftest:scenarios-matrix`（原本寫
  `node frontend/e2e/support/scenario/scenarios.selftest.ts`，裸 `node` 會
  `ERR_MODULE_NOT_FOUND`，因為本檔用 `.ts` specifier import 其他 support
  模組，需要 `--experimental-loader=./e2e/support/selftestJsToTsLoader.mjs`）。
- 新增五個 `Object.prototype` 成員的負控制（`toString`／`constructor`／
  `__proto__`／`valueOf`／`hasOwnProperty`），逐一驗證 `resolveScenario` 必須
  throw「未知 scenario」。

## 4. 允許範圍內的其餘改動

- `frontend/package.json`：新增 `selftest:scenario-wire-evidence` script（
  執行 `wireEvidence.ts` 的正負控制 selftest），併入 `selftest:all` 鏈。
- `frontend/package.json.md5`：因 `package.json` 內容變動而重算（`md5 -q
  package.json`，32 bytes 小寫 hex、無換行）。
- 新增 `frontend/e2e/support/scenario/wireEvidence.selftest.ts`：15 條正負
  控制（見第 5 節）。

**未動**：五支 frozen fake/protocol 檔——**`protocol.ts`／`fakeAppServer.ts`／
`verify.ts`／`fakeAppServer.selftest.ts`／`verify.selftest.ts`**（PR #15 合併
的那批，`frontend/e2e/support/scenario/` 底下；SHA256 見附件
`frozen-files-sha256.txt`）。**清單修正**：上一版本文件誤列
`scenarioCli.ts`／`scenarioTripwire.ts` 為候選凍結檔（執行者當時依檔案職責
推斷、非票面明文）——經 Claude controller 核實，這兩支**不在**凍結清單內，本輪已
更正措辭與 SHA 清單，不再推斷。本輪（F1／F2）對這五支檔案**完全未修改**。
另外未動：production Go／UI、`default`／`controls` 相關檔、
`artifactIntegrity.ts` 的 `SCOPE_PATHS`、CI 設定。

## 5. 驗證證據

### 5.1 受影響 selftests＋typecheck（本輪重跑）

| 檢查 | 結果 | 備註 |
|---|---|---|
| `selftest:scenarios-matrix` | 14 passed, 0 failed | 含新增 5 條 `Object.prototype` 負控制 |
| `selftest:scenario-wire-evidence`（新增） | 15 passed, 0 failed | 正控制（延後 meta／短暫不完整內容重試）＋負控制（永遠缺檔／損毀 JSON／錯身分 argv／非零 exit／exit_code 缺失／recorder_error／cleanup_incomplete／finalize_cause 含逾時訊號／provider 不符） |
| `selftest:scenario-verify` | 22 passed, 0 failed | 未修改，確認未受影響 |
| `selftest:scenario-protocol-judge` | 18 passed, 0 failed | 未修改，確認未受影響 |
| `typecheck:e2e`（`tsc --noEmit -p tsconfig.e2e.json`） | rc=0，無輸出 | |

### 5.2 四案重驗

⚠️ **本節重驗不抹去首輪 `commandExecution-deny`（run
`20260921T084322Z-8037b2`）缺 `app-wire-log.meta.json` 的事實**——那是觸發
F1 補正的原始證據，繼續保留於 `.evidence/task-d-f1f2/original-bundle-copy/`
（原 `/tmp/b3a2b2-taskD-20260921T165222Z/` 的未修改副本，143/143 自驗
`shasum -a 256 -c` 通過），本輪**不回填、不覆寫**。

#### 5.2.1 執行者第一輪重跑（有缺陷，事實保留，不得抹去）

執行者第一輪跑四案時，驅動腳本**漏設 `E2E_KEEP_ARTIFACTS=1`**：

| case | run-id | 結果 |
|---|---|---|
| commandExecution-allow | `20260921T091451Z-13d3c7` | PASSED（3.1m），但**證據目錄被既有 teardown 依預設清除**，只有 stdout log（`logs/my-round-rerun/run1-commandExecution-allow.stdout.log`）與其中的 `harness.log` 摘錄可查 |
| commandExecution-deny | `20260921T091801Z-11330f` | 同上（2.4m），證據目錄被清除 |
| fileChange-allow | `20260921T092027Z-1de5eb` | 同上（2.1m），證據目錄被清除 |
| fileChange-deny | `20260921T092238Z-66f3c5` | **rc=130，被 Claude controller 的 `kill -TERM` 中斷**——09:23:47Z Claude controller 對 pid 28198（run4 的 playwright）與 process group `-29218`（run4 的 wails dev）送出 `SIGTERM`，這兩個目標**當時正是 run4 自己於 09:22:52.248Z spawn、進行中的程序，不是孤兒／殘留**，Claude controller 誤判為殘留並主動終止，直接造成中斷；harness 收到這個非預期 SIGTERM 後完全依契約收尾（確認持有的 ChildProcess 已結束不再送信號、對無法驗證身分的目標不送信號、`portsReleased=true`、最終正確標記 `INTERRUPTED`），非 F1／F2 程式缺陷、無任何 assertion 執行過 |

原始 stdout log 保留在 `logs/my-round-rerun/`；run4 的完整時序與撤回項目見
`NOTES-run4-interruption.md`。**此段失敗事實（driver 漏設
`E2E_KEEP_ARTIFACTS=1`）不因後續有乾淨的四案而抹去。**（**二次更正**：先前
兩版此處分別誤判「中斷後偵測到 Claude controller 平行執行造成衝突」與「留下孤兒
程序、harness clean=true 與實際不一致」——Claude controller 釘清楚時序後確認：
28198／29218 是 run4 進行中的程序，是 Claude controller 自己的 `kill -TERM` 直接
造成中斷，不是孤兒清理，也不是環境／pty 互動副作用；harness 的 bounded
teardown 對這次非預期中斷運作正確，`clean=true` 回報屬實。上述兩項誤判均
已撤回，詳見 `NOTES-run4-interruption.md`。）

#### 5.2.2 Claude controller 重跑（乾淨、`E2E_KEEP_ARTIFACTS=1`，本節據此收尾四案驗收）

Claude controller 親自執行、各自獨立 run-id、1 worker／0 retries、rc 皆 0：

| case | run-id | 耗時 | decision（manifest） | F1 harness.log 摘錄 |
|---|---|---|---|---|
| commandExecution-allow | `20260921T092415Z-947b16` | 2.1m | accept | `F1：finalize meta 已就緒（run-id=20260921T092415Z-947b16，generation=codex-wire-20260921T092548-4dtte6x2-001.jsonl，等待 0ms／共 1 次嘗試）` |
| commandExecution-deny | `20260921T092624Z-56c332` | 2.4m | decline | `F1：finalize meta 已就緒（run-id=20260921T092624Z-56c332，generation=codex-wire-20260921T092805-vhsz43vn-001.jsonl，等待 0ms／共 1 次嘗試）` |
| fileChange-allow | `20260921T092850Z-da57d0` | 2.1m | accept | `F1：finalize meta 已就緒（run-id=20260921T092850Z-da57d0，generation=codex-wire-20260921T093020-rgw7s8et-001.jsonl，等待 0ms／共 1 次嘗試）` |
| fileChange-deny | `20260921T093059Z-2b924a` | 1.9m | decline | `F1：finalize meta 已就緒（run-id=20260921T093059Z-2b924a，generation=codex-wire-20260921T093224-cvqtxp5h-001.jsonl，等待 0ms／共 1 次嘗試）` |

四案均設 `E2E_KEEP_ARTIFACTS=1`，證據目錄完整保留，已複製進
`.evidence/task-d-f1f2/fixed-bundle/artifacts/case{1..4}-*/`（含
`app-wire-log.jsonl`＋`app-wire-log.meta.json`、`scenario-wire.log`＋
`.manifest.json`、`app-audit.jsonl`、`app-workspace-sessions.json`、
`run-env.json`、`scenario-config.json`、`harness.log`、approval／新內容
screenshot）。

**meta 欄位核對方式**：Claude controller 直接讀取這四份保留下來的
`app-wire-log.meta.json` 原始位元組（**不是推自測試通過**）：四案皆
`provider=codex`、`exit_code=0`、`finalize_cause="codex: app-server
exited"`、無 `recorder_error`、無 `cleanup_incomplete`；四份
`scenario-wire.log.manifest.json` 的 `decisionReceived`／`exitCode=0` 與案例
期望一致（`accept`／`decline`／`accept`／`decline`，對應上表）。執行者本輪
（收尾時）已重新讀取這四份 meta／manifest 位元組核對，內容與 Claude controller
回報一致（見 `.evidence/task-d-f1f2/fixed-bundle/artifacts/`）。

**限制（Claude controller 實測指出，須據實標註）**：四案的 `F1：finalize meta 已就
緒` 摘錄**全部是「等待 0ms／共 1 次嘗試」**——代表這四次實跑當下 meta 檔本來
就已經存在，`waitForWireMeta` 的**有上限輪詢路徑在真實瀏覽器跑中並未被實際
觸發過**（一次都沒有等待或重試）。這條路徑目前只在
`wireEvidence.selftest.ts` 的「延後 meta 出現」與「短暫不完整 JSON」兩個案例
以隔離的假檔案覆蓋，**bounded wait 在真實 App／wails dev／fake app-server
組合下的有效性尚未經實跑驗證，僅有單元層覆蓋**，不得寫成「已在真跑中驗
證」。

### 5.3 Playwright `--list` collection 證據

四個 scenario 名稱各自 `npx playwright test --config
e2e/playwright.scenario.config.ts --list`，全部回報 `Total: 1 test in 1
file`（見 `logs/playwright-list-evidence.log`）——證明每次 invocation 只收集
到一個 test，resolver 的 9 個單元測試不能取代這項 collection 檢查。

## 6. 證據 bundle

- **原 bundle 複本**：`.evidence/task-d-f1f2/original-bundle-copy/`（複製自
  `/tmp/b3a2b2-taskD-20260921T165222Z/`，未修改、未回填）。`shasum -a 256 -c
  SHA256SUMS`：143/143 OK。
- **修正版 bundle（本輪新立）**：`.evidence/task-d-f1f2/fixed-bundle/`，內容：
  完整 source snapshot（改動的 7 個檔案）、五支 frozen 檔 SHA256（
  `frozen-files-sha256.txt`，`protocol.ts`／`fakeAppServer.ts`／`verify.ts`／
  `fakeAppServer.selftest.ts`／`verify.selftest.ts`）、tracked diff
  （`diff/tracked.diff`）、四項 selftest 原始輸出、`typecheck:e2e` 原始輸出、
  Playwright `--list` 原始輸出、manifest digest（`SHA256SUMS`）。四案證據分
  兩份分開陳列，不混為一談：
  - `logs/my-round-rerun/`＋`NOTES-run4-interruption.md`：執行者第一輪重跑的
    失敗事實（run1–3 因漏設 `E2E_KEEP_ARTIFACTS=1` 證據被清除、run4 被
    Claude controller 誤判為殘留並 `kill -TERM` 中斷），**原樣保留、不刪除**。
  - `artifacts/case{1..4}-*/`＋`logs/controller-rerun/`：Claude controller 親自重跑、
    `E2E_KEEP_ARTIFACTS=1` 保留的四案完整證據（本節據此收尾四案驗收，見
    第 5.2.2 節）。
  `shasum -a 256 -c SHA256SUMS` 自驗結果與檔數見交付回報。

## 7. 明確保留的限制（沿用既有裁定，未解決）

- `fakeAppServer.selftest.ts` 曾有一次 `waitForChildExit` 約 3 秒逾時，根因
  未確認，未解——本輪未重跑該 selftest（不在 F1／F2 影響範圍內），沿用既有
  「已知測試可靠性問題、未解」的裁定，不得寫成已解。
- deny 案（`commandExecution-deny`／`fileChange-deny`）在 decline 之後看到的
  「新內容顯示」是 fake app-server 的合成情境（它不看 decision 決定要不要
  繼續送 afterApproval 內容），不得宣稱真正的 codex provider 被拒絕後也會
  照樣送出後續內容。
- 本輪未驗 browser resume、Claude fake（等效 browser 檢查點）、cold restart、
  正式 CI 整合；`default`／`controls` smoke 本輪未重跑（票面明訂，只動
  scenario 專屬路徑）。
- Task D／B3a-2b aggregate 整體驗收狀態**不由本文件裁定**，等 codex-reviewer
  複核本輪交付。
- **執行者第一輪重跑有缺陷，事實保留**：驅動腳本漏設
  `E2E_KEEP_ARTIFACTS=1`，`commandExecution-allow`／`commandExecution-deny`／
  `fileChange-allow` 三案雖 rc=0，證據目錄被既有 teardown 依預設清除；
  `fileChange-deny`（run `20260921T092238Z-66f3c5`）於執行期間被 **Claude controller
  的 `kill -TERM`** 中斷（rc=130）——Claude controller 於 09:23:47Z 把 run4 進行中的
  playwright（pid 28198）與 wails dev process group（`-29218`）誤判為殘留並
  主動終止；harness 對這次非預期 SIGTERM 的 bounded teardown 運作正確
  （`clean=true`、`portsReleased=true` 屬實，非缺陷）。**這段失敗事實不因
  後續有 Claude controller 的乾淨四案而抹去**，見第 5.2.1 節與
  `logs/my-round-rerun/`、`NOTES-run4-interruption.md`。**二次更正**：此前
  兩版分別誤判「與 Claude controller 平行執行的埠衝突」與「留下孤兒程序、harness
  clean=true 與實際不一致」，兩項均已撤回——正確成因是 Claude controller 自己的
  `kill -TERM`，不是孤兒清理，也不是環境／pty 互動副作用。
- **四案功能證據採用 Claude controller 重跑；Task D 已於 #288 技術驗收通過，B3a-2b aggregate 仍未完成**——
  四案功能證據來自 **Claude controller 親自重跑**（`E2E_KEEP_ARTIFACTS=1`，
  四案皆 PASSED、wire＋meta 齊全，見第 5.2.2 節）；meta 欄位是 Claude
  controller 直接讀取保留下來的原始 `app-wire-log.meta.json` 位元組核對，
  **不是推自測試通過**；這只是四案功能證據的驗證方式，**不等於 Task D／
  B3a-2b aggregate 已獲技術驗收**，該裁定權在 codex-reviewer（Codex），見
  第 7 節。
- **bounded wait 路徑真實環境有效性未經實跑驗證**：Claude controller 四案的 `F1：
  finalize meta 已就緒` 摘錄皆為「等待 0ms／共 1 次嘗試」，代表這四次實跑中
  meta 檔在取證當下已經存在，`waitForWireMeta` 的有上限輪詢／重試邏輯**未被
  真實觸發過**；該路徑目前只有 `wireEvidence.selftest.ts` 用隔離假檔案覆蓋
  的單元測試證據，**不得寫成已在真實 App／browser 組合下驗證**。

## 8. 交付狀態

**本輪未 commit／push／開 PR**（票面明訂 Task D 補正完成前不授權）。所有改動
仍是本 worktree（`b3a-2b-2/task-d-browser-matrix`）內的未提交變更，base 為
main `a1e7b27f50e1b108f1d9156ad8af113e0bc972fc`（即已合併的 PR #16
`test(e2e): 新增 Codex approval browser scenario 檢查點`）。
