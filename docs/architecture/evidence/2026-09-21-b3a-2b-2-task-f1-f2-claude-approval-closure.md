# B3a-2b-2 Task F1／F2 限定結案紀錄——Claude 單一 fresh-start approval allow

> 建立：2026-09-21。範圍：**F1a／F1b（離線協定與真 mcp-approval 子程序）＋ F2（真 App／UI 整合檢查點）**。
> **這不是 Task F 整體完成，也不是 B3a-2b aggregate 完成。** Task F 若仍含 Claude recovery，該部分未做、未驗。

## 0. 一句話範圍

證明的是：**真 App → 受控假 Claude CLI → 真 App 產生的 MCP config → 真 `mcp-approval` 子程序 → 真 `approval.Broker`／`pumpApprovals` → 瀏覽器看見 approval → 真的點 allow → 真 `ResolveApproval` → MCP allow → 預定完成內容顯示** 這一條鏈，**單一 fresh-start allow 案、一次成功**。

**provider 是受控的假 Claude CLI。真實的是 App、MCP、broker 與 UI。本次沒有驗證任何真實 Claude provider 行為。**

## 1. Base 與最終 run

| 項目 | 值 |
|---|---|
| base commit | `ddf15dc5f2aaaf4d19435d415bd59437b422f91c` |
| base tree | `1588fea697d67ca06cbd3657d66b302d95db6d12` |
| 最終 browser run | `20260921T145922Z-e50ef9`（PASSED，rc=0，測試 9.7s） |
| 核定 App binary | `<repoRoot>/build/bin/sdlc-workbench.app/Contents/MacOS/sdlc-workbench` |
| 該 binary sha256 | `811e0c566298d08e42eabd33667f9406b857f2e6bd2763c09cd973b08b287ecc` |
| 工具鏈 | Node v26.8.1；Go1.27.1（`/usr/local/opt/go/libexec/bin`）；`GOTOOLCHAIN=local`、`GOPROXY=off`、`GOFLAGS=-mod=readonly`、npm offline |
| 入口 | `npm run test:e2e:scenario`，`E2E_SCENARIO=claude-approval-allow`，1 worker／0 retry |

## 2. 四個樣本的區別（**全部保留，不因後續成功而改寫**）

| 樣本 | 結果 | 說明 |
|---|---|---|
| run1 `20260921T144101Z-4de19e` | **FAILED** | App 已 ready（HTTP 可達、vite 5173），但 setup 在**核定 binary 路徑**這步失敗——我原本假設 `wails dev` 產出 `build/bin/<outputfilename>`，macOS 實際是 `go build -o build/bin/<name>-dev-<arch>` 之後執行 `.app/Contents/MacOS/<name>`。**approval 整合鏈從未進入，當時狀態未知**（我最初寫「不是整合鏈的問題」過強，已更正）。 |
| run2 `20260921T144946Z-8d973c` | **PASSED，但少兩項檢查** | 全鏈走通。但當時 spec **只斷言 claudeVersion、未斷言 codexVersion**，且 `appBinaryIdentity` 的 canonical 邊界仍有 symlink 放行缺口（reviewer 已重現）。因此它是有效成功樣本，但**不足以宣稱 helper 已完整防止錯工作樹、也不足以宣稱兩個 CLIInfo 版本都驗過**。 |
| 自測字串失敗 | **FAILED（保留）** | 收緊 `appBinaryIdentity` 後，一條負控制的**診斷字串**斷言過期（`command 已變動` → `command 與快照不一致`），19/20。行為斷言 `identity===null` 當時已通過。經 reviewer 授權只同步文字、保留該負控制、不放寬行為斷言後 20/20。原始 log 保留。 |
| run3 `20260921T145922Z-e50ef9` | **PASSED（最終）** | 收緊後的 identity 仍正確核定；**兩個 CLIInfo 版本都斷言且存證**。 |

另有一次**整合入口失敗**（`selftest:all` rc=1）：`selftest:execution-mode` 的 npm 入口沒有 loader，而 F2 讓 `executionMode.ts` 改 import `scenarios.ts`（後者用 `./protocol.js`）。**我先前逐檔手動加 loader 執行，把這個問題遮住了**——該套件的 12/12 從未經過它真正的 npm 入口。reviewer 裁定採「只替該 npm script 加同一個 loader 旗標」。原始失敗 log 保留。

## 3. 最終 run 的實際鏈路證據

- **核定 binary identity**（ready 後、spec 前）：pid 78184、`startedAt=Mon Sep 21 23:00:18 2026`、canonicalPath 與 expectedPath 相等、command 與 canonicalPath **完全相等**。身分來源是**當次受控程序樹 ＋ 新鮮 `ps` 觀測**，不看歷史 run-state、不挑「command 含關鍵字的第一個」、**不從待驗 MCP config 反推**。
- **真 App 寫出的 MCP config**：`<fixture>/.workbench/mcp-01M327QKKB000EAGNZACTT6XZS.json`，其 `command` 與上述核定 binary 相同，`args=["mcp-approval","--socket","<stateDir>/approval-0.sock"]`。假 CLI 的 config 路徑**只從 argv 的 `--mcp-config` 取得**。
- **真 mcp-approval 五筆往返**：`initialize` / `result{capabilities,protocolVersion,serverInfo}` / `notifications/initialized` / `tools/call` / `result{content}`，最終 payload `{"behavior":"allow","updatedInput":{...}}`（`updatedInput` 由真 broker 依 `broker.go:119-120` 補上）。
- **子程序**：pid 79527，`ps` 回報 **ppid = CLI selfPid**，exit=0、退出後 `ps` **absent**、stdout/stderr 皆 drained。
- **真 App broker audit**（`<stateDir>/audit.jsonl`）：`request` → `decision`，同一個 id `6b5867600762c60a411e67328ddcb272`。
- **三方一致**：transcript ↔ App audit ↔ DOM `data-test-approval-id`，`violations=[]`；config 檔名的 WSID 與 DOM `data-test-wsid` 相同。
- **UI**：`ui-claude-approval-visible.png`、`ui-claude-completion.png`。決策來自 `[data-test="approval-allow"]` 的**實際點擊**——沒有 Go helper、沒有直接呼叫 `ResolveApproval`、沒有 mock UI。
- **CLIInfo 隔離**：`claudeVersion` 與 `codexVersion` **兩者**都等於本次 run-env 值；workspace／toolsDir canonical path 相符；`startupError` 為空。證據 `cliinfo.json`。
- **tripwire**：claude `--version` 3、conversation 1、codex `--version` 3、wrapper 行 4、**違規 0**。判定來源是**結構化 argv 紀錄**（原生陣列），不對 `printf %q` 拆字。
- **收尾**：`clean=true`、`portsReleased=true`、TERM 1298ms 未升級 KILL、`overallFailed=false`；事後掃描無殘留、34115／5173 已釋放。

## 4. 驗證分工（worker 與 reviewer 各自）

- **worker（claude-worker）**：實作、逐批自測、mutation、三次 browser run 的執行、自測失敗紀錄與證據整理。
- **reviewer（codex-reviewer）**：獨立重跑 selftest 與 typecheck、獨立重算保存證據的 judge、逐檔核對 manifest 雜湊、親讀 MCP 五筆／broker audit／子程序身分／截圖、透過讀碼、執行紀錄及獨立受控反例確認缺口（binary 路徑、socket traversal、leaf symlink、`commandIsBinary` 前綴、CLIInfo 漏 codexVersion、預檢紀錄不同步、argv TypeError、Claude identity 誤判）。
- **本結案的多項阻擋是 reviewer 先以獨立探針重現、worker 才修正的**；worker 的 mutation 與 typecheck **不計為 reviewer 獨立驗證**。

## 5. 交付整合檢查（隔離 source-only 副本）

為同時證明「正式 npm 入口可重跑」與「不依賴歷史 artifact」，在 `/tmp/f2src/repo` 由**明確待交付來源清單**重建副本並執行：

- 來源＝`git ls-files`（923，工作樹內容）＋ 新增未追蹤交付檔（14）＝ **937 檔**
- **排除**：`node_modules`、`frontend/dist`、`build/bin`、舊 `.artifacts` 內容（只帶 tracked 的 `.gitkeep`）、`/tmp/f1b`、`/tmp/f2run*`
- 必要本機依賴以 `node_modules` symlink 接入（**不列為交付檔**）
- `selftest:artifact-integrity` 需要 HEAD，因此在副本內建**隔離 git 基準**（`git init` + 單一 commit），**不借用主工作樹的舊 artifact**
- 結果：`selftest:all` **rc=0**，**21 個套件、463 項檢查、0 失敗**；`typecheck:e2e` **rc=0**
- 交付 source 內**無** `/tmp/f1b`、`/tmp/f2run*` 等硬編路徑

## 6. 保留的未驗項與限制

1. **Claude recovery／deny matrix、多輪、多 session、正式 CI**：完全未做、未驗。
2. **真實 Claude provider**：未驗；provider 一律是受控的假 CLI。
3. **F1b 歷史限定驗收的未驗項全部保留**：假 CLI 遭 SIGKILL 的清理、`closeAndReapChild` 的 default grace（10s/10s/5s）實際等待時長、`FINALIZE_DEADLINE` 逾時分支——三者皆有程式碼但無測試涵蓋。
4. **F1b 的暫存 driver（driver.ts／driver2.ts／driver3.ts）與 Go probe helper 不在交付範圍**，也**不改寫**；它們是 r5／r6／r7 的歷史執行來源。介面已改為 `PathPolicy` 互斥聯集，**舊 driver 不能直接套現版 policy**（對應 SHA 見 `/tmp/f1b/DRIVER-VERSION-PINNING.txt`）。
5. **`appBinaryIdentity` 只支援 darwin**；其他平台是明確拒絕，未驗證。
6. **symlink 跨工作樹的防護只由受控負控制證明**；真實環境沒有該佈局，在 browser run 中屬未觸發路徑。
7. **`fakeAppServer.selftest.ts` 的 `waitForChildExit` 間歇逾時仍是既有未解 issue**；本次隔離副本 36/36 通過**不代表根因已解**。
8. **`frontend/dist` 曾被 F1b 的離線 build 佔位檔覆蓋**，F2 前已移除並跑真 `npm run build`；佔位檔副本保留於 `/tmp/f2run1/placeholder-index.html.removed`。
9. **`/tmp/f1b/attempt2/workbench-f1b` 是帶佔位前端的 binary，不得用於任何 UI 驗收**；本次 browser run 未使用它。

## 7. 原始證據位置與跨機器限制

| 內容 | 位置 |
|---|---|
| run1（FAILED） | `/tmp/f2run1/`（含 `manifest.json`、`manifest-rev2.json`、`artifacts/`） |
| run2（PASSED，少兩項檢查） | `/tmp/f2run2/`（`manifest.json` 51 項） |
| 自測字串失敗 | `/tmp/f2run3/logs/appBinaryIdentity-FAILED.log` |
| run3（最終 PASSED） | `/tmp/f2run4/`（`manifest.json` 48 項）；harness 原地證據 `frontend/e2e/.artifacts/20260921T145922Z-e50ef9` |
| 整合入口失敗 | `/tmp/f2pkg/logs/selftest-all-FAILED.log` |
| 隔離 source-only 整合檢查 | `/tmp/f2src/`（`logs/selftest-all-isolated.log`、`logs/typecheck-isolated.log`、`list-all.txt`、`delivery-source-manifest.txt`） |
| F1b driver 版本對應 | `/tmp/f1b/DRIVER-VERSION-PINNING.txt` |

**限制：上列 `/tmp` 與 worktree 路徑都是本機證據，不是跨機器可取得的成果。** 跨機器重跑以提交後的 source 為準；本機保存的來源與執行證據雜湊清單不會隨 Git 提交交付。

最終 runtime 證據雜湊清單：`/var/folders/qc/sl4x_xc16c919shkwhz_tg08xl6cln/T/review382-30ag97bn/RUNTIME-SHA256SUMS`，收錄最終 run 全部證據檔、原始 manifest、隔離檢查 log 與歷史失敗紀錄；`/tmp/f2src/RUNTIME-EVIDENCE-MANIFEST.txt` 僅為位置索引。

## 8. 擬提交清單

**修改的既有檔案（10）**：`frontend/e2e/global-setup.scenario.ts`、`global-teardown.ts`、`support/env.ts`、`support/executionMode.ts`、`support/fakeCli.ts`、`support/scenario/scenarioTripwire.ts`、`support/scenario/scenarios.ts`、`support/scenario/specRouting.ts`、`frontend/package.json`、`frontend/package.json.md5`
**新增檔案（14）**：`frontend/e2e/scenarios/claudeApproval.spec.ts`、`support/scenario/` 下的 `appBinaryIdentity.ts`／`.selftest.ts`、`claudeAppEvidence.ts`／`.selftest.ts`、`claudeApprovalJudge.ts`、`claudeApprovalProtocol.ts`／`.selftest.ts`、`claudeScenarioCli.ts`、`claudeSetupFailure.ts`、`f2Routing.selftest.ts`、`f2RunEnv.selftest.ts`、`fakeClaudeCli.ts`／`.selftest.ts`
**加上本結案文件與 backlog 版本行。** 最終 26 檔（含兩份文件）的逐檔 SHA-256 見 `/var/folders/qc/sl4x_xc16c919shkwhz_tg08xl6cln/T/review382-30ag97bn/SOURCE-SHA256SUMS`；該清單本身留在本機。

**不納入提交**：`frontend/node_modules`（原工作樹為本機依賴實體目錄；隔離副本以 symlink 接入）、`build/bin/f1b-probe/`（gitignore 內的暫存 Go probe 與 driver）、`frontend/dist/`、`build/bin/sdlc-workbench.app/`（建置產物）。

## 9. 本次未做

未 commit／push／開 PR；未改 production；未新增 recovery／deny／CI；未更動任何估點；**未把 Task F、B3a-2b aggregate、CI、recovery、deny 標記為完成**。
