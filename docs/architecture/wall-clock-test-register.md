# Wall-clock 測試有效名單（living）

> 版本：v1（2026-09-04，B1a-4 建立；整合 HEAD `583387d`）
> 性質：**living 文件**——「目前有效名單」與規則以本文件為準。`docs/spikes/m3b-results.md` §7 保留為 2026-08-21 的歷史觀察與具名來源，§7.1 為 B1a 收尾時的處置結果快照；兩者原文不再更新。
> 更新責任：B1b 完成前端兩條候選的重現與處置後**更新本文件**（不另建）；B2 於 CI 建立後回寫 #2／#3／#6 的 CI 量測。任何新候選的登記與除名都在本文件的修訂記錄留痕。

---

## A. Go 六條（B1a 已處置）

| # | 測試 | 套件 | 狀態 | commit | 修正方式 | 最後一次負載重驗 |
|---|---|---|---|---|---|---|
| 1 | `TestAppServerTerminateKillsGroup` | `internal/codex` | **resolved**（B1a-1，2026-09-03） | `82caf8b`、`f7ad1ed` | `Proc` 未匯出 timer／signal-event seam＋三條白箱測試；codex 端改驗 supervisor 收尾 | 2026-09-04 B1a-4 矩陣，27 次有效指定執行全 PASS |
| 2 | `TestClaudeAssistFailsLoudOnOversizedLine` | `internal/assist` | **resolved**（B1a-2，2026-09-04） | `7b1bb0c` | 移除 fixture `tr` 轉換、保留 15 秒 context | 同上，27/27；三份併發下最長 8.02s（見 C.4） |
| 3 | `TestMultiTurnSendAndTurnBoundaries` | `internal/claude` | **resolved**（B1a-2，2026-09-04） | `05069e2` | `waitResult` 局部 deadline 5s→15s（卡死保險絲） | 同上，27/27 |
| 4 | `TestInFlightTurnDoesNotBlockNewSession` | root | **resolved**（B1a-2，2026-09-04） | `b0a8404` | `afterFn` 接上 `newFakeAfter()`，quiesce 逾時不由真實時鐘決定 | 同上，27/27 |
| 5 | `TestAppServerMidStreamDeath` | `internal/codex` | **no-change disposition**（B1a-3，2026-09-04） | 無（不存在 implementation／resolved commit） | preflight 未證實存在可修的牆鐘缺陷（非宣稱它完全不含牆鐘） | 同上，27/27 |
| 6 | `TestOutputCancellationKillsGrandchildren` | `internal/proc` | **resolved**（B1a-3，2026-09-04） | `39aa732` | 第二段輪詢 `deadline` 重設＋fixture 父 shell 寫 `$!`＋`pid != pgid` oracle 斷言 | 同上，27/27；三份併發下最長 9.3s（見 C.4） |

負載重驗定義：B1a-4 plan（`docs/superpowers/plans/2026-09-04-b1a-4-integration-acceptance.md`）Gate A 的四層矩陣——M1 逐包單跑、M2 五套件併行 ×3、M3 三份併發 ×1、M4 背景負載下 focused ×20，全部 `-race -p=8`，整合 HEAD `583387d`，本機 8 核 16 GB。每條 27 次「有效、指定執行」（背景負載中的執行不計）全 PASS；18 個 `-json` artifact 頂層 FAIL 為 0。

## B. 前端候選（待重現，B1b 承接）

| 候選 | 狀態 | 來源 |
|---|---|---|
| `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` | **待重現（B1b）** | 2026-08-21 session 首錄、2026-08-25 更新；未落正式文件 |
| `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` | **待重現（B1b）** | 同上 |

前端規則（owner 凍結）：完整套件碰到**相同 timeout** 可單獨重跑一次判定，但**仍須揭露**；單獨重跑失敗、或失敗形狀改變（非 timeout），視為真正失敗。候選須以現行 HEAD 重現並附證據才可補入 A 段；不成立者除名並在修訂記錄留痕。

## C. 規則

1. **A 段六條的 FAIL 先分類，契約回歸不得重跑吸收**。在 `-race`、套件併行或負載下任一條 FAIL，先依 B1a-4 plan D1 分類：**命中該測試的契約／oracle 斷言、或 goroutine dump 可歸因於其契約路徑的卡死（panic／`-timeout`）→ 契約回歸**，不得以「先單獨重跑再判定」吸收，§7 的舊規則對這六條自 2026-09-04 起失效；**命中 setup／前提校驗、可證明的資源失效、或可歸因於其他測試的 panic／`-timeout` → 該次無效**，揭露後可在調整負載後重跑，不算紅也不算綠。
2. **綠燈仍不是修正的通過證據**。六條全綠只證明「未重現」，任何修正的通過證據須來自其對應施工票的 mutation／negative control。
3. **新候選登記條件**：須以現行 HEAD 重現（含失敗輸出、重現指令、HEAD SHA），登記時標「候選」，不得直接視為名單成員；處置完成後改「resolved」或「no-change disposition」並附 commit（no-change 不得虛構 commit）。
4. **本機餘裕觀察（不外推至 CI）**：B1a-4 矩陣 M3 三份併發下，#2 最長 8.02s／預算 15s（約 1.9 倍）、#6 最長 9.3s／deadline 20s（約 2.2 倍），遠低於單跑時的約 30 倍。此為本機 8 核觀察，CI runner 若較弱，這兩條最先逼近預算。
5. **CI 冷啟動缺口歸屬 B2**：B1a 全部量測來自本機；CI runner 的冷啟動與 required-check 耗時分布由 B2 於 CI 建立後首批 required check 跑批時量測 **#2／#3／#6** 並回寫本文件 A 段「最後一次負載重驗」欄。
6. **#6 的自然誤紅從未在本機重現**：現有證據是 B1a-3 的人工延遲證明機制與 B1a-4 負載下 27/27 全綠，結論僅為「本機負載下未重現」。

## 修訂記錄

- v1（2026-09-04）：B1a-4 建立。A 段六條處置狀態自 B1a-1／B1a-2／B1a-3 關票紀錄與 B1a-4 Gate A 帳表轉錄；B 段兩條前端候選自 backlog B1 驗收條件 (4) 轉錄，標「待重現（B1b）」；C 段規則 1–6 依 B1a-4 plan D1／D4 與 owner 裁定寫入；規則 1 於 closure review 依 owner 要求改為「先分類、僅契約回歸不得重跑吸收」，與 §7.1 及 plan D1 一致。
