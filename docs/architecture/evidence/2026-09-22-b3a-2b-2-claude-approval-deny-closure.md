# B3a-2b-2 Claude approval deny 限定檢查點結案紀錄

> 建立：2026-09-22（台北時間）。範圍：**受控 fake Claude ＋ 真 App／MCP／broker／UI，使用者在既有 reason 輸入框填入本次專屬理由後實際按下「拒絕」，該決策逐層正確傳遞**。
> **這不是 Task F 整體完成，也不是 B3a-2b／B3a aggregate 完成。** 真實 Claude provider、逾時的自動 deny 實跑、空理由 deny 的 browser 案、多 session、cold restart、正式 browser CI 皆未驗。

## 0. 一句話範圍

證明的是：真 App `StartSession(provider=Claude)` → 受控假 Claude CLI → 真 App 產生的 MCP config → 真 `mcp-approval` 子程序 → 真 `approval.Broker`／`pumpApprovals` → 瀏覽器看見 approval → **在既有 reason 輸入框填入 run 專屬理由 → 實際點 `[data-test="approval-deny"]`** → 真 `ResolveApproval(id,false,reason)` → MCP 回覆 `behavior=deny` → 假 CLI 送該案專屬的「被拒絕」完成內容。**一次成功、單一樣本。**

**provider 是受控的假 Claude CLI。真實的是 App、MCP、broker 與 UI。** 本次**不證明**真實 Claude 收到 deny 之後不會執行該命令——那是 provider 行為。假 CLI 全程**未執行** `input.command`。

## 1. 為什麼 deny 要單獨驗（不是 allow 的鏡像）

一手讀碼（2026-09-22 覆核）：

| 事實 | 位置 |
|---|---|
| `reason` 預設**空字串**，由既有 `<input v-model="reason">` 綁定 | `ApprovalDialog.vue:16,90` |
| `decide(false)` 把 reason 原樣送進 `ResolveApproval(id,false,reason)` | `ApprovalDialog.vue:55-62` |
| `allow=false` → `behavior/decision="deny"`，reason 成為 `Decision.Message` | `app.go:6908-6916` |
| `Message`／`UpdatedInput` 皆 `omitempty`；**只有 allow 才補 `UpdatedInput`** | `broker.go:20-25,118-120` |
| MCP 回覆 `{behavior,message?,updatedInput?}` | `mcpserver.go:70-76` |

→ 本案指定非空理由的 deny payload 與 allow 不同（有 `message`、**無 `updatedInput`**），既有 F1a 判定當時是**寫死 allow** 的，判不了 deny 案。

**同時必須寫清楚**：`deny` 一定帶非空 message **不是** production 契約——空理由的 deny 完全合法。本 scenario 只是**自己固定一個 run 專屬理由**，好讓判定逐字認出「使用者這次按下的 deny」，並與 fail-closed 的自動 deny（`approval timeout (fail closed)`／`approval broker unavailable (fail closed)`）分開。本檔與測試中「空／錯 reason 必須被擋」的反例**只對這個非空理由的案例成立**。未為了測試改動 production。

## 2. 交付（8 檔，**全部在 `frontend/e2e/`**，未修改 production Go／Vue／store）

| 檔 | 要點 |
|---|---|
| `scenario/claudeApprovalProtocol.ts` | `ApprovalDecision`、`protocolDecisionFor()`（accept→allow／decline→deny，未知值 throw）、`buildClaudeDenyExpectation()`；`decision` 為**必填**欄位，既有兩個 builder 明確填 `'allow'`；`denyReason` 只在 deny 案存在 |
| `scenario/claudeApprovalJudge.ts` | `validateExpectationDecision()`（缺值／未知值／allow 帶 `denyReason` 一律拒絕，**無預設**）；`judgeMcpToolCallResult`／`judgeBrokerAudit` 依核定 decision 分流，**既有 allow 判定條件保留**（fail-closed 錯誤訊息的後綴有調整） |
| `scenario/scenarios.ts` | 登記 `claude-approval-deny`（provider claude／kind approval／decision decline／`build()` throw） |
| `global-setup.scenario.ts` | 依登記表的 decision 經 `protocolDecisionFor()` 選 builder；recovery 案若非 allow 在組 fixture 前 fail loud |
| `scenarios/claudeApproval.spec.ts` | 同一支 spec 服務 allow 與 deny；reason 輸入框用**既有 locator**（`dialog.locator('input')`），未新增 production selector |
| 三支 selftest | 新增 38 項檢查（**含正控制、負控制與 routing／builder 檢查**，不全是負控制） |

期望來源：spec 先由**已驗證的 scenario identity** 取得 decision，再與已保存的 fixture 雙向核對、並逐欄比對 fixture 等於受版控 builder 的輸出；**不從待驗的 MCP／audit／DOM 回填**。

## 3. 本次 browser run

| 項目 | 值 |
|---|---|
| 基底 commit | `46df1d888ed7146098de53299f22c0e2f0c7a8f3`（PR #20 已 merge，見 §6） |
| run-id | **`20260922T000918Z-c85e82`**（**PASSED，第一次嘗試即通過**，rc=0） |
| 牆鐘 | 2026-09-22T00:09:17Z → 00:11:29Z（`1 passed (2.2m)`，test 本體 11.2s） |
| 入口 | `E2E_SCENARIO=claude-approval-deny E2E_KEEP_ARTIFACTS=1 npm run test:e2e:scenario`，1 worker／0 retries，恰一個 test |
| App binary | `build/bin/sdlc-workbench.app/Contents/MacOS/sdlc-workbench`，pid 69896，sha256 `00efb8fa…`（**當次留存值**；複核時該 binary 已不存在、未重新計算，刪除原因未獨立驗證） |
| CLIInfo | `claudeVersion`／`codexVersion` 皆等於本次 run 專屬字串；`toolsSource=env`、`workspaceSource=env`、`startupError=""` |

### 3.1 拒絕鏈的實際證據

- **UI（點擊前）**：`ui-claude-deny-ready.png` 顯示 raw params 為本次 marker、reason 輸入框已填入 `b3a2b2-claude-deny-reason-20260922T000918Z-c85e82`、且「允許」與「拒絕」兩顆按鈕**同時在場**。
- **UI 操作紀錄**：`claude-ui-action.json` 是 **`clickResolved=true` 的最終紀錄**（不是前後兩份 JSON；點擊前的證據是上面那張 ready 截圖），內含 `domWsid=01M33773M60006S7DPP56DEA8P`、`domApprovalId=bb5fa05aa130516ae1b4cd3492acc6cf`、`observedReason`＝核定理由、`button=approval-deny`。`clickResolved` 只代表 Playwright 的點擊操作完成，**決策仍由 MCP／audit 證明**。
- **MCP（真 `mcp-approval` 的原始行）**：`{"behavior":"deny","message":"b3a2b2-claude-deny-reason-20260922T000918Z-c85e82"}`——payload 鍵**恰為 `["behavior","message"]`**，`updatedInput` 連欄位都不存在。
- **broker audit（完整原檔 3 行）**：`startup` / `request` / `decision`，**無 `timeout` 列**；decision 為 `{"id":"bb5fa05a…","behavior":"deny","message":"<核定理由>"}`，無 `updatedInput`、不含 `fail closed`。
- **一致性（措辭精確）**：**DOM 與 broker 的 approval id 一致**（`bb5fa05a…`）；**MCP 與 broker 的 request 內容、decision 與 reason 一致**；**config 檔名的 WSID 與 DOM 的 `data-test-wsid` 一致**。MCP response 的 JSON-RPC `id` 是 `2`，且 payload 依契約**不含** broker id，因此**不存在「三方 id 一致」這回事**。`claude-ui-judgement.json` → `violations: []`。
- **完成內容**：`ui-claude-completion.png` 顯示該 WSID 的 pane 內 assistant 氣泡為 `b3a2b2-claude-denied-content-20260922T000918Z-c85e82`（deny 專屬內容），pane 狀態「完成」；左下 timeline 另有 `approval_decision 核可決定：deny`。
- **無 allow 代替、無自動 deny**：兩層 behavior 都是 `deny`，message 逐字等於核定理由，audit 成對且無 timeout 列。
- **假 CLI 與子程序**：argv 14 token 不含 `--resume`；CLI 自判 `{"problems":[]}`；MCP 子程序 `ppid` 等於 CLI 自身 pid、psDuring=present、psAfter=**absent**、exit=0、無訊號、雙串流 drained、`unreaped=false`。
- **tripwire／network／收尾**：tripwire 違規 0（conversation 恰 1/1、核定 resume=null）；`browser-network-violations.log` 0 bytes、`network-samples.log` 287 行——**這是取樣結果，不是封包層全程證明**；收尾 `clean=true`、`portsReleased=true`、`escalatedToKill=false`、`overallFailed=false`。
- run 前後 source manifest 皆 8/8 相符——本次 run 未改動任何交付檔。

## 4. 驗證的時間與對象界線（**必須分清**）

| 證據 | 對象 | 時間點 |
|---|---|---|
| 隔離 source-only `selftest:all` rc=0、**22 套 581 項檢查 0 失敗**；`typecheck:e2e` rc=0 | 當時的 8 檔 | **reviewer 最後一次 spec 取證補丁之前** |
| `typecheck:e2e` rc=0、`selftest:claude-approval-protocol` **91 passed**、`selftest:claude-app-evidence` **20/20**（reviewer 親跑） | 最終 source 快照；typecheck 涵蓋 spec，兩套 selftest 驗證協定與證據判定 | 補丁之後 |
| browser run `20260922T000918Z-c85e82` **PASSED** | 含補丁的最終 8 檔 | 補丁之後 |

→ **581 項全套結果不涵蓋最終 spec**。另：施工期間出現過一次 `TS2741` 編譯失敗（`decision` 改必填後，F1a selftest 的字面值 fixture 未同步）。那是**介面更新後 fixture 未同步的編譯失敗**，保留為事實；**不得當作「runtime 會拒絕未知 decision」的證據**——runtime 的拒絕由 `validateExpectationDecision` 的反例負責。

## 5. Manifest 與證據位置（本機路徑，非跨機器可取得）

| 項目 | 值 |
|---|---|
| source manifest（8 檔，reviewer 建立） | `/var/folders/.../T/review404-f59u2d85/SOURCE-SHA256SUMS`，SHA256 `486de804e54b2730a7273e5ce6b6f6238528742d35533e579376d30d620de229` |
| 最終 spec SHA256 | `b6b3b3e65c8b90675a96042021c70ca0e7bdc02b98b89286d89c16178d441c08` |
| runtime manifest（45 筆，含 run.log／run-env／期望 fixture／UI action／三張截圖） | `/tmp/denyrunB/RUNTIME-SHA256SUMS`，SHA256 `9c826a3d201f545b513250e07bbb0dcbedc1d6f2276ccfa64d00b66f4c3a8b1d` |
| reviewer 複核證據 | `/var/folders/.../T/review406-fq38qnij/`（`rejudge.ts`／`rejudge.log`、`live-cleanup.json`、`REVIEW-SHA256SUMS`） |
| 階段 A 隔離 source-only 副本 | `/tmp/denysrc`（git baseline `7799847370902da4f4c8eb9d5ac1b770a4d1e0fc`） |
| browser run artifacts | `frontend/e2e/.artifacts/20260922T000918Z-c85e82`（`E2E_KEEP_ARTIFACTS=1` 保留）；原始 stdout `/tmp/denyrunB/run.log` |

本次 PASSED，config 為 `retain-on-failure`，因此**沒有 trace**；證據是三張截圖與上述檔案。

## 6. 上一個檢查點的交付狀態

**PR #20 已 merge**：mergeCommit **`46df1d888ed7146098de53299f22c0e2f0c7a8f3`**（rebase，tree `0ba776bf…` 與 rebase 前相同，parent `c86b354f…`），mergedAt `2026-09-21T23:49:39Z`，20 檔。其範圍限於 **Claude same-App recovery 限定檢查點**（結案見 [`2026-09-22-b3a-2b-2-claude-recovery-checkpoint-closure.md`](2026-09-22-b3a-2b-2-claude-recovery-checkpoint-closure.md)），**不含**本次 deny 案。

## 7. Task F／B3a-2b 的原契約對照

**原契約可追溯的文字**（backlog）：
- B3a aggregate 驗收條件 (1)：browser E2E suite 覆蓋 **Gate 1／Gate 2／STALE／session recovery／approval** 核心流；(2) required CI 路徑一律使用 deterministic fake／replay provider，**live Claude／Codex 驗收另列獨立項目、不作 required check**；(3) 納入 B2 的 CI。
- **B3a-2b 票面**：「**session recovery、approval 兩條流程，以 replay provider 驅動**」。票面未指名 provider。

**目前覆蓋**：

| 流程 × provider | 狀態 | 依據 |
|---|---|---|
| approval／Codex | allow＋deny（2 methods × 2 decisions 四案） | Task C、Task D |
| session recovery／Codex | 已驗（單一 `commandExecution`、兩輪 accept） | Task E |
| approval／Claude — allow | 已驗 | Task F1／F2 |
| approval／Claude — **deny** | **本次已驗** | 本文件 |
| session recovery／Claude | 已驗（fresh → UI End → `--resume S`，兩輪 allow） | recovery closure（PR #20） |

**reviewer 複核結果（2026-09-22）**：兩條流程對兩個 provider 皆已有上述限定檢查點，但尚不足以裁定 Task F／B3a-2b 整體關票。repo 外仍保存 `/tmp/b3a2s-2b-design-v2.md`（SHA256 `17d89ee55e9f17753d8590dd3d62dd644860710d0459cc9815ccdf6a26fda178`）；其 §6 曾提出 App 真正重啟後讀盤的驗證，§7–8 曾列最終 smoke／controls 實跑。設計留在 repo 外，不能據此推論只有票面摘要的條件。

該檔自述是**設計草稿**，不能直接當成已核定施工範圍。關票前須對照後續核定設計／裁定，列明這兩項是已驗收、由後續決策排除／拆票，或仍待完成。Task C 結案已有當時 default／controls 實跑紀錄，但本次尚未確定其是否滿足最終回歸條件。這是既有範圍的追溯工作，**本次不因此授權重跑 browser 或新增 cold-restart 實作**。

**本次裁定**：deny 限定檢查點可獨立提交與開 PR；Task F／B3a-2b 整體關票暫緩，待上述範圍對照完成。真實 provider、多 session、三輪以上等未驗能力仍按各 closure 保留，不自動升格為新增驗收條件。**B3a aggregate 仍未完成**：Gate 1／Gate 2／STALE 屬 **B3a-2a**，正式 CI 整合另票，均不由這次 deny 交付完成。

## 8. 保留限制

1. **單一樣本**，不宣稱重複穩定性。
2. provider 是受控 fake；**不證明**真實 Claude 收到 deny 後不執行命令。
3. 逾時／socket 不可達的自動 deny **只有離線反例**，未在 browser 實跑。
4. **空理由 deny 的 browser 案未驗**；本案固定非空理由，相關反例不推廣成「空理由 deny 非法」。
5. binary 於複核時已不存在，未重新 hash；刪除原因未獨立驗證。
6. network 證據是**取樣**（287 行）＋違規檔 0 bytes，不是封包層全程證明。
7. `fakeAppServer.selftest.ts` 的 `waitForChildExit` 間歇逾時仍未解，未混入本次修正。
8. `/tmp` 與 worktree 路徑非跨機器可取得證據。
9. **active effort 未量測**（無可靠量測工具），不以牆鐘回填；未核定任何新點數。

## 9. 交付狀態

8 檔 source 已驗收凍結；**截至本文件寫成時尚未 commit／push／PR**。主工作區未動，全部歷史樣本與 manifest 保留。
