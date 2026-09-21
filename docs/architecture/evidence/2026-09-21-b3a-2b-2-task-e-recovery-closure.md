# B3a-2b-2 Task E：Codex session recovery（真 UI End→Start）結案紀錄

> **最終狀態（codex-reviewer 裁定，mailroom #333）：Task E（E1 ＋ E2）技術驗收完成。**
> **這不是** B3a-2b aggregate 完成——aggregate 仍未完成。兩者狀態分開。

## 1. 原問題

B3a-2b 的 browser resume 一直未驗。最小候選命題是：

> **同一個 App 執行期間、同一個 WSID、同一條存活的 Codex app-server 連線
> （同一 generation），使用者在真 UI 按下 End 之後再送出訊息，應該重接既有的
> provider thread（`thread/resume`），而不是開新 thread。**

讀碼顯示 production 已支援（`ensureAppServer` 重用既有 server／conn、
`registryResume` 供上一輪的 `ResumeSessionID`、`EnsureThread` 在 resume 非空時
送 `thread/resume`），**但當時無法以 browser E2E 證明**：凍結的
`fakeAppServer.ts` 是一次性狀態機，`turn/completed` 後即 `process.exit`，
結構上不可能在同一程序內接收第二次 `thread/resume`。

## 2. 最終實作與適用範圍

分兩段交付：

**E1（協定元件＋共用判定，已於 #312 驗收）**
- `protocol.ts` 新增**選填** `secondTurn`；缺省時單輪行為不變。
- `fakeAppServer.ts` 固定兩輪（非任意輪數框架）：第一輪 `threadMode='start'`，
  第一輪 `turn/completed` 後**保持存活**等待第二次 `thread/resume`，
  `params.threadId` 必須精確等於同一個配置 threadId；第二輪完成才寫最終 manifest
  並 exit 0。**fake 不得自行替 client 發 resume。**
- `recoveryJudge.ts`（新增、匯出）：`judgeRecoverySequence` 核對完整有序序列
  `initialize → thread/start → turn1/approval1/completed → thread/resume →
  turn2/approval2/completed`，含方向、frame 形狀、嚴格 ID 型別與值；
  `judgeRecoveryManifest` 以 `judgeApproval`（**只 import，`verify.ts` 未改**）
  核對成功收尾，再加 `secondTurn` 的 identity／`resumeAccepted`。

**E2（真 browser 接線）**
- `codexSessionRecovery.spec.ts`：真 UI 建立 session → 送出 → 第一輪 allow →
  內容可見 → **點真的 `end-session`** → 等同一 WSID 的 `data-test-active="false"`
  → **同一 pane 再送出** → 第二輪 allow → 第二輪不同內容可見。
- `specRouting.ts`：以 `resolveScenario(name).kind` 決定 spec 檔名，
  **每次 invocation 只收一支 spec**（原 `testMatch: *.spec.ts` 會讓兩案同跑）。
- `recoveryAppEvidence.ts`：App 原始 wire 的型別／原始順序核對、
  **依握手位置（前三筆）判定 WSID 歸屬**（其後每筆必須非空且等於 `domWsid`）、
  generation **唯一性**（候選 != 1 即失敗並保留歧義診斷，不依 mtime 猜最新）、
  以及**落地 `scenario-config.json` 對上受版控 builder**（含 `secondTurn`）。
- `PaneView.vue` 只新增 `data-test-active`（反映既有 `meta.active`），
  **無新 binding、無 production 行為變更**。

**適用範圍**：**一個** `commandExecution`／**兩輪都 accept** 的受控 fake
browser recovery checkpoint。

## 3. 基準與最終 run

- **base SHA**：`19aa080f0710f4a6bc231fd6fc1b615f049656b8`
- **最終 run-id**：`20260921T121604Z-bb739c`（rc=0、PASSED、`artifactViolations=0`）
- **WSID**：`01M31YCP470007X6N2Y9YRK0TV`
- **fake PID**：`83330`（單一 process 涵蓋兩輪，無重啟／換代）
- **generation**：`codex-wire-20260921T121728-9995a864-001`
- App wire 21 frames；audit 第一段 close 落在第一輪 decision 與第二輪 request 之間；
  兩輪同 WSID、同 thread、**不同 turn／item／approval**。

## 4. 各層驗證與執行者（不得混寫）

| 層級 | 內容 | 執行者 |
|---|---|---|
| source-only `selftest:all` 228 項 | 不含 `.artifacts`／`.task-e-notes` 的可攜性驗證 | **Claude controller 實跑** |
| `recoveryAppEvidence` selftest 必要 16 項＋`typecheck:e2e` | 獨立複核 | **codex-reviewer 獨立執行** |
| 最終 run 的 raw replay（四組 judge 以 builder+runId 獨立產生期望） | fake sequence／manifest／App sequence+WSID／disk config 全 `[]` | **codex-reviewer 獨立執行** |
| B 歧義分支四反例（two-readable／missing-meta／copy-failure／zero-candidates） | 由 spec 實際分支原樣抽取的探針 | **codex-reviewer 執行；非 repo 常駐 regression test** |
| 最終 browser run 與預檢 | 見第 3 節 | **Claude controller 執行** |

## 5. 四次 browser run（授權狀態／結果／限制）

| run-id | 授權 | 結果 | 限制 |
|---|---|---|---|
| `20260921T113808Z-c12056` | 是 | PASSED | **判定強度不足**：當時 spec 的 App wire 檢查只 `find` 兩輪 approval frame、未讀 `wsid`、未驗完整序列；也未讀 `scenarioConfigPath` |
| `20260921T115751Z-b1516a` | 是 | **FAILED** | `package.json.md5` 於 run 期間 content hash 由 `c5ee6829…` 變成 `0e61e68e…` → `artifactViolations=1`。**harness 正確攔下**；**寫入者未知**（見第 6 節） |
| `20260921T120105Z-7c9873` | **否（未授權追加）** | PASSED | **僅作額外觀察資料，不取代 b1516a 的失敗、不追認為符合單次授權** |
| `20260921T121604Z-bb739c` | 是（最終） | **PASSED** | 本結案的驗收依據 |

**舊失敗與未授權 run 不因本次通過而被消除。**

## 6. `package.json.md5` 變動的歸因界線

Wails v2.13.0 的 `NpmInstallUsingCommand`
（`pkg/commands/build/base.go:460-478`）在 stored checksum 與實際 MD5 不符時，
**確實會 `fs.MustWriteString(packageChecksumFile, packageJSONMD5)`**——
機制由**一手 source 證實存在**。

但**該次寫入的行為者仍未知**：機制存在不等於該次即由它觸發，亦無法證明是
執行者手動寫入。**兩項先前的歸因（「執行者手改」與「repo 找不到字樣故不支持
Wails 覆寫說」）都已撤回**；後者的推理瑕疵在**搜尋範圍**（只搜 repo、未搜
GOPATH 的 Wails module）。

現況：`package.json` 真正 MD5 與 `.md5` 精確一致
（`44e0171dc301e0e71bbc5e8e70f668bb`，恰 32 bytes、無多餘換行），
同一不符條件已不成立。**未因此修改 artifact-integrity 或放寬任何判定。**

## 7. 交付範圍與證據

**本次 commit 的 20 檔 source**：`frontend/e2e/` 下的 scenario 入口與 routing、
fake／protocol／judge、recovery evidence helper 與其 selftest、**最小 fixtures 與
`PROVENANCE.json`**、`package.json`／`.md5`、以及 `PaneView.vue`。

⚠️ **兩種 manifest 不可混稱**：

| manifest | 路徑（本機） | 涵蓋 | 該 manifest 檔本身的 SHA256 |
|---|---|---|---|
| **source manifest**（20 檔驗收快照） | `.task-e-notes/e2-delivery-source/SHA256SUMS` | 本次 commit 的 20 檔 source | `c4ddab1497d273a18461461e71e90b3c35a739491b84dac8b463068ccdbde58f` |
| **runtime evidence manifest** | `.task-e-notes/e2-final-run-bundle/SHA256SUMS` | 最終 run 的 artifacts 副本、預檢紀錄、原始 stdout、README（**33 項**） | `afa48ecbbe443c67dc9f54c49c1e8b7ebcccf7dd5c0a95359a01c07d48e9a8a4` |

source manifest 是 reviewer 於驗收時保存的清單**原樣複製**（20 行，自驗 20/20 OK）；
**runtime bundle 的 33 項不含 20 檔 source**，兩者用途不同。
⚠️ **該 33 檔不含最終 run 目錄的全部原始項目**——bundle **排除了 2 支可由受版控
`scenarioCli.ts` 重建的 per-run fake CLI wrapper**。**原始 run 目錄
`frontend/e2e/.artifacts/20260921T121604Z-bb739c/` 仍完整保留在本機可取得。**

`.artifacts/`、`.task-e-notes/`、wrapper 與暫存探針**都不進 git**。

## 8. 明列限制

- **不是** B3a-2b aggregate、provider 真實行為、cold restart、server replacement、
  Claude fake 或正式 CI 完成。
- **未證明** backend 空 resume 的 registry fallback——UI 在 `session.ts:551`
  收到 init 事件時就帶上 `m.resume`，本案未走到該 fallback。
- **既知 `waitForChildExit` timeout 仍未解**（近期未重現，不代表已解）。
- **bounded wait 的真實環境有效性未經實跑觸發**（歷次皆「等待 0ms／1 次嘗試」，
  僅單元層覆蓋）。
- **B 歧義分支**的四個反例是 reviewer 的離線抽取探針，**不是 repo 常駐測試**。
- **工時紀錄**：部分回合的 active 工時**未量測或未知**，**照實保留為未知**，
  未以估計回填成精確數字。過往亦有數次超出當時授權上限的程序偏差，
  已記錄於各回合，**不因技術驗收通過而消除**。
