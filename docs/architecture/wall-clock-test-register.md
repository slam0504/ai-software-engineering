# Wall-clock 測試有效名單（living）

> 版本：v3（2026-09-05，B2a 登記 Go 表 #7 候選 `TestOrphanDoesNotHangNormalExit`；前版 v2 2026-09-05 B1b 更新、v1 2026-09-04 B1a-4 建立）
> 性質：**living 文件**——「目前有效名單」與規則以本文件為準。`docs/spikes/m3b-results.md` §7 保留為 2026-08-21 的歷史觀察與具名來源，§7.1 為 B1a 收尾時的處置結果快照；兩者原文不再更新。
> 更新責任：B1b 已於 v2 更新前端兩條候選；B2a 於 v3 登記 #7 候選；**B2c** 承接 #7 的診斷並回寫；B2b 於 CI ruleset 啟用後回寫 #2／#3／#6 的 CI 量測（版本順延為 **v4**）。任何新候選的登記與除名都在本文件的修訂記錄留痕。

---

## A. Go 測試（#1–#6 B1a 已處置；#7 為 B2a 登記之候選）

| # | 測試 | 套件 | 狀態 | commit | 修正方式 | 最後一次負載重驗 |
|---|---|---|---|---|---|---|
| 1 | `TestAppServerTerminateKillsGroup` | `internal/codex` | **resolved**（B1a-1，2026-09-03） | `82caf8b`、`f7ad1ed` | `Proc` 未匯出 timer／signal-event seam＋三條白箱測試；codex 端改驗 supervisor 收尾 | 2026-09-04 B1a-4 矩陣，27 次有效指定執行全 PASS |
| 2 | `TestClaudeAssistFailsLoudOnOversizedLine` | `internal/assist` | **resolved**（B1a-2，2026-09-04） | `7b1bb0c` | 移除 fixture `tr` 轉換、保留 15 秒 context | 同上，27/27；三份併發下最長 8.02s（見 C.4） |
| 3 | `TestMultiTurnSendAndTurnBoundaries` | `internal/claude` | **resolved**（B1a-2，2026-09-04） | `05069e2` | `waitResult` 局部 deadline 5s→15s（卡死保險絲） | 同上，27/27 |
| 4 | `TestInFlightTurnDoesNotBlockNewSession` | root | **resolved**（B1a-2，2026-09-04） | `b0a8404` | `afterFn` 接上 `newFakeAfter()`，quiesce 逾時不由真實時鐘決定 | 同上，27/27 |
| 5 | `TestAppServerMidStreamDeath` | `internal/codex` | **no-change disposition**（B1a-3，2026-09-04） | 無（不存在 implementation／resolved commit） | preflight 未證實存在可修的牆鐘缺陷（非宣稱它完全不含牆鐘） | 同上，27/27 |
| 6 | `TestOutputCancellationKillsGrandchildren` | `internal/proc` | **resolved**（B1a-3，2026-09-04） | `39aa732` | 第二段輪詢 `deadline` 重設＋fixture 父 shell 寫 `$!`＋`pid != pgid` oracle 斷言 | 同上，27/27；三份併發下最長 9.3s（見 C.4） |
| 7 | `TestOrphanDoesNotHangNormalExit` | `internal/claude` | **candidate**——CI-only；**現行 HEAD `ffcd161` 上 2/2 重現**；本機 bounded stress 未重現；根因未確認（B2a 登記，2026-09-05；B2c 承接診斷） | 無 | 無（不得以放寬 5 秒 guard 或加 retry 作修法；B2c 診斷後另決） | **CI 證據**：PR #1 run `33953144191` attempt 1（07:39Z）與 attempt 2（08:00Z，owner 一次性診斷例外、只重跑 `go`）皆紅，HEAD `ffcd16140e13399451b69833fa106f0c7fa5980b`，runner `macos-15-intel`（image macos-15 `20260824.0482.1`），Go 1.26.5，指令 `go test -race ./... -count=1 -timeout 30m -json`，artifact `go-test-json` id 9965561717／9965826248（`go-test.rc`=1），逐字失敗 `session_test.go:207: drain/Wait hung on orphan-held pipes`、`--- FAIL: TestOrphanDoesNotHangNormalExit (5.01s)`；同套件其餘 34 條 0–0.06s。**跨 SHA 對照**：`109b407`（`internal/claude` 與 workflow 相關 bytes 相同）run `33952217785` 一次綠（0.04s）。**本機反證**（2026-09-05，8 核 x86_64）：focused `-race -count=30` 全過 2.02s；三份 `./internal/...` `-race` 併發下 focused `-count=20` 最慢 0.02s；B1a-4 各層 0.01–0.03s |

負載重驗定義：B1a-4 plan（`docs/superpowers/plans/2026-09-04-b1a-4-integration-acceptance.md`）Gate A 的四層矩陣——M1 逐包單跑、M2 五套件併行 ×3、M3 三份併發 ×1、M4 背景負載下 focused ×20，全部 `-race -p=8`，整合 HEAD `583387d`，本機 8 核 16 GB。每條 27 次「有效、指定執行」（背景負載中的執行不計）全 PASS；18 個 `-json` artifact 頂層 FAIL 為 0。

## B. 前端兩條（B1b 已重現並處置）

| # | 測試（檔案） | 狀態 | commit | 根因（已確認） | 修法 | 重驗 |
|---|---|---|---|---|---|---|
| F1 | `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積`（`frontend/src/components/PlanWorkspace.test.ts`） | **resolved**（B1b，2026-09-05） | `8aee222` | CodeMirror 模組動態載入＋jsdom 首次建構 `EditorView` 的一次性成本，落在該檔當下執行的測試；三份併發全套下超過 vitest 預設 5000ms（preflight 三份 3/3 重現，6.3–7.7s） | 測試檔 `beforeAll` 內預先 `import('codemirror')`／`import('@codemirror/state')` 並建構一次即銷毀的 `EditorView`；production 與 `vitest.config.ts` 零變更 | B1b Gate A：三份併發 P 兩輪六份 397 PASS（F1 1.5–2.5s）；`v4-N1-file-confirm` 2/3 |
| F2 | `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call`（`frontend/src/components/SpecWorkspace.test.ts`） | **resolved**（B1b，2026-09-05） | `8aee222` | 同上（同一機制，成本落點依執行順序不同） | 同上 | 同上（F2 1.3–2.1s）；`v4-N2-file-confirm` 3/3 |

來源：2026-08-21 session 首錄、2026-08-25 更新；B1b（plan `docs/superpowers/plans/2026-09-04-b1b-frontend-wallclock-candidates.md`）於 HEAD `92719fb` 重現後併入處置。**處置紀錄註記**：本次 pre-merge negative control 採**檔案層級**判準（移除預熱後，一次性成本落在該檔任一條測試，非固定為候選；owner 於 plan rev8 裁定），此判準只用於 B1b 的 pre-merge 驗證，**與規則 7 的具名 FAIL 分類無關、不放寬之**。

**前端一般規則（owner 凍結，適用於 F1／F2 以外的前端測試）**：完整套件碰到**相同 timeout** 可單獨重跑一次判定，但**仍須揭露**；單獨重跑失敗、或失敗形狀改變（非 timeout），視為真正失敗。新候選須以現行 HEAD 重現並附證據才可補入本文件；不成立者除名並在修訂記錄留痕。


## C. 規則

1. **A 段六條的 FAIL 先分類，契約回歸不得重跑吸收**。在 `-race`、套件併行或負載下任一條 FAIL，先依 B1a-4 plan D1 分類：**命中該測試的契約／oracle 斷言、或 goroutine dump 可歸因於其契約路徑的卡死（panic／`-timeout`）→ 契約回歸**，不得以「先單獨重跑再判定」吸收，§7 的舊規則對這六條自 2026-09-04 起失效；**命中 setup／前提校驗、可證明的資源失效、或可歸因於其他測試的 panic／`-timeout` → 該次無效**，揭露後可在調整負載後重跑，不算紅也不算綠。
2. **綠燈仍不是修正的通過證據**。六條全綠只證明「未重現」，任何修正的通過證據須來自其對應施工票的 mutation／negative control。
3. **新候選登記條件**：須以現行 HEAD 重現（含失敗輸出、重現指令、HEAD SHA），登記時標「候選」，不得直接視為名單成員；處置完成後改「resolved」或「no-change disposition」並附 commit（no-change 不得虛構 commit）。
4. **本機餘裕觀察（不外推至 CI）**：B1a-4 矩陣 M3 三份併發下，#2 最長 8.02s／預算 15s（約 1.9 倍）、#6 最長 9.3s／deadline 20s（約 2.2 倍），遠低於單跑時的約 30 倍。此為本機 8 核觀察，CI runner 若較弱，這兩條最先逼近預算。
5. **CI 冷啟動缺口歸屬 B2**：B1a 全部量測來自本機；CI runner 的冷啟動與 required-check 耗時分布由 B2 於 CI 建立後首批 required check 跑批時量測 **#2／#3／#6** 並回寫本文件 A 段「最後一次負載重驗」欄。
6. **#6 的自然誤紅從未在本機重現**：現有證據是 B1a-3 的人工延遲證明機制與 B1a-4 負載下 27/27 全綠，結論僅為「本機負載下未重現」。
7. **F1 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` 與 F2 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` 自 B1b 處置後，「相同 timeout 可單獨重跑一次判定」的前端規則對這兩條失效**，任何 FAIL 先分類：(i) 命中該測試的契約斷言（F1：`assist-busy` 顯示／`draft-text` 累積／busy 解除；F2：`draft-text` 為空／`accept-draft` disabled）、或可歸因於其契約路徑的卡死（含再次 `Test timed out in 5000ms` 且無環境訊號）→ **回歸，required check 阻擋，不得重跑吸收**；(ii) setup 失敗（掛載／mock 建立）、可證明的資源失效（OOM、worker 啟動失敗）、或其他測試造成的中斷 → **該次無效**，揭露後重跑，不算紅也不算綠。另：前端測試不得把模組動態載入或首次建構重型元件的成本留在測試本體。**此規則不受 B1b pre-merge 檔案層級 control 例外影響。**

## 修訂記錄

- v3（2026-09-05）：B2a 登記 Go 表 **#7** `TestOrphanDoesNotHangNormalExit` 為 candidate（CI-only、現行 HEAD `ffcd161` 2/2 重現、本機 bounded stress 未重現、根因未確認），附 run／attempt／HEAD／指令／artifact／逐字訊息／跨 SHA 對照／本機反證；候選不因後續 attempt 轉綠而除名或改寫為誤紅。#7 為 owner 明示的一次性診斷例外下取得的 attempt 2 證據，**不構成**「非八條 Go 測試可重跑」通則；處置由 B2c 承接。B2b 回填版本順延 v4。
- v2（2026-09-05）：B1b 更新。B 段兩條由「待重現（B1b）」改為 resolved（`8aee222`），附已確認根因、修法與 Gate A 重驗摘要；註明 pre-merge control 採檔案層級判準且與規則 7 無關；前端一般規則改為適用於 F1／F2 以外；新增規則 7（具名 F1／F2 的 FAIL 分類契約）。
- v1（2026-09-04）：B1a-4 建立。A 段六條處置狀態自 B1a-1／B1a-2／B1a-3 關票紀錄與 B1a-4 Gate A 帳表轉錄；B 段兩條前端候選自 backlog B1 驗收條件 (4) 轉錄，標「待重現（B1b）」；C 段規則 1–6 依 B1a-4 plan D1／D4 與 owner 裁定寫入；規則 1 於 closure review 依 owner 要求改為「先分類、僅契約回歸不得重跑吸收」，與 §7.1 及 plan D1 一致。
