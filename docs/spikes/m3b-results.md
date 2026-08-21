# M3b 驗收結果 — 多 session 工作區

- 日期：2026-08-15
- 分支：`m3b-multi-session`（Task 0-31）
- Spec：`docs/superpowers/specs/2026-08-14-m3b-multi-session-design.md`（rev4，closure review APPROVED）
- 執行環境：macOS 24.6.0（Darwin amd64）、16 GB RAM / 8 core、Go 1.26.5、Node 26.7.0、Wails v2.13.0
- Pin 版本：claude `2.1.223`、codex `0.146.1`（`./scripts/check-cli.sh` OK）

---

## 0. 結論摘要

**四個收尾 gate 全綠**（含本里程碑首次執行的 `wails build`）。

**spec §5「production-path barrier 必備清單」43 條**：綠 42、**未覆蓋 1**。
（**2026-08-21 矩陣重跑補記**：那 1 條〔2.11〕已由 SegmentSet 接線票轉綠，§3 的三個
未覆蓋／待裁決項全數出貨——見 §10。）

> **條數由 35 變 36 的原因**：§5.2 的原 2.4 一條在 frame-attribution 票（2026-08-18）被拆成
> **2.4a**（一份 wire log 收得到兩個 session 的雙向 frame——不漏、不分裂成兩份檔）與
> **2.4b**（§3.4.3 frame-level：交錯的兩個 session 逐 frame 各歸各的）。拆的理由是原本那一條
> 掛的證據（`TestWireLogCapturesFramesOfEverySession`）只證得到前者，把後者一起記綠是高估。
> 2.4b 的實跑證據來自該票，不是本文件原本那次 session。
>
> **再一次（同票稍後）**：owner 裁決把歷史 frame 歸屬的展開改成非阻塞（sidecar ＋單一背景
> worker ＋ job journal），凍結了六條契約，逐條落成 **2.4c-h**（36 → 42）；後續 review 又抓到一個「延後 resolve 污染更早的 view」的回歸，修正後補成 **2.4i**（→ 43）。連帶
> **`codex_wire_segments` 的稽核形狀變了**：它的 `frames` 現在**只含 live 那一代**，
> 完整答案在另一筆 `codex_wire_segment_frames`（以 `viewId` join），`framesStatus`
> 是「要不要去找第二筆」的旗標。稽核消費者需要改讀兩筆；§3.4.4 沒有 UI／binding
> 消費者，故無 Wails binding 變更。

**spec §6 實機驗收 10 項**：**無法執行 8、部分執行 1、未實作 1（A5）**——本 session 是非互動
agent 環境，沒有 GUI 操作能力；Codex 帳號用量限制至 2026-08-20，涉及 Codex live turn 的項目
另有外部阻塞。**A5 與環境無關**：pane pins 沒有持久化路徑，就算有 GUI 也會失敗（§3.3）。
**另有 1 項 spec 前提未受保護**（single-instance guard），**待 owner 裁決**。

| 類別 | 綠 | 未覆蓋／未實作 | 部分執行 | 無法執行 |
|---|---|---|---|---|
| spec §5 條款（43） | 42 | 1 | – | – |
| spec §6 實機驗收（10） | – | 1（A5＝未實作，§3.3） | 1（A10） | 8 |
| spec §7 待驗證假設（5） | 3（§7.3／§7.4／§7.5，既有測試覆蓋，見 §2.8） | – | 1（§7.1＝A10） | 1（§7.2＝Task 0 probe 重跑） |
| **本票新發現**的未受保護前提（1） | – | 1 | – | – |
| 收尾 gate（4） | 4 | – | – | – |

> **spec §7 那列與 §6 那列有重疊**：§7.1 就是 A10、§7.2 就是 Task 0 的 live probe，兩處各記一次
> 是為了讓「spec 的五項待驗證假設逐項有交代」這件事看得見，不是兩份獨立成績。
> **最後一列（single-instance guard）不在 spec §7 的五項之內**——它是本票從
> `internal/appcore/sink.go` 的 doc 推出來的新發現，見 §3.2。

> **本文件的判定規則**：只有「本次實際跑過並看到輸出」才記綠；「有測試但本次未跑」不記綠；
> 「型別層有測試、production 沒有呼叫端」記**未覆蓋**，不記綠；「環境或外部限制導致無法執行」
> 記無法執行並寫出阻塞原因與後續建議；**「拿掉環境限制也仍會失敗（production 缺實作）」記
> 未實作，不得記成無法執行**——後者會讓一個實作缺口偽裝成環境問題（A5 原本就記錯，final
> review 已更正）。
>
> **「綠 42」的精確含義**：42 條有**實跑通過**的對應測試（2.4b 與 2.4c-i 的實跑在
> frame-attribution 票，其餘在本文件原本那次 session），**不等於**這 42 條的守門力
> 在本票被重新驗證過——後者需要把 Task 0-30 的 60+ 個 mutation 全部重跑（見 §4 末段的方法論說明）。
> 本票只對新增的那一條做了完整 mutation 交叉矩陣。

---

## 1. 收尾 gate

三個 gate **分開跑，未併跑**（併跑會讓牆鐘相依測試偽陽，見 §5）。

下表是**最終樹（含 §4 新增測試與 §8 修正）** 上的執行結果，四個 gate 依序各跑一次：

| Gate | 指令 | 結果 |
|---|---|---|
| Go | `go build ./... && go vet ./... && go test -race ./... -count=1` | ✅ 全綠（`go list ./...` 共 **22** 個 package，其中 `cmd/probe-multiturn`／`internal/ports` 無測試檔 → **20 行 `ok`**；package main 117.8s） |
| 前端測試 | `npm --prefix frontend run test` | ✅ 36 files / 326 tests passed（17.27s） |
| 前端 build | `npm --prefix frontend run build` | ✅ built in 9.54s |
| **`wails build`** | `wails build` | ✅ Built `build/bin/sdlc-workbench.app`（darwin/amd64，27.7s） |

過程中 Go gate 共跑四次：第 1 次紅（負載偽陽，§7）、第 2 次全綠、第 3 次紅（既有測試競態，§8）、
第 4 次（最終樹）全綠。**四次都據實記錄，沒有只留最後一次。**

**`wails build` 是本里程碑第一次執行**（Task 0-30 皆未跑，plan 把它列在本票）。附帶驗到一件事：
build 會重新產生 Wails binding，跑完 `git status` **仍為 clean**——`frontend/wailsjs/go/main/App.d.ts`
等 generated 檔案已進版控且與目前 Go 簽名一致（`CreateSession`／`ListSessions`／`RemoveSession`／
`LoadTurnsBefore`／`SendMessage(wsid, prompt)` 逐一存在），Wails binding 四步規則的「產生＋提交」
那兩步沒有漏。

---

## 2. spec §5 逐條驗收矩陣

**證據欄的測試名皆已用 `grep -c '^func <name>('` 核對存在且唯一**；全部包含在 §1 的 Go gate 綠色跑次內。
逐條的 mutation 驗證（「拿掉哪一行會讓哪一條紅」）由各 task 在實作當下完成，記錄在
`.superpowers/sdd/2026-08-14-m3b-multi-session/task-*-report.md`，本票**未重跑**那些 mutation
（唯一的例外見 §4：本票新增的那一條）。

### §5.1 建立交易（5 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 1.1 | 同 provider 5 個並行 `ReserveSession`，恰 4 個取得（`-race`） | 自動化 | ✅ 綠 | `internal/appcore/manager_wsid_test.go` `TestReserveSessionLimitIsAtomic` |
| 1.2 | Reserve → registry persist failure → `AbortCreate` 退回名額 | 自動化 | ✅ 綠 | `app_wsid_test.go` `TestCreateSessionRollsBackOnPersistFailure`；`internal/appcore` `TestAbortCreateReleasesSlot` |
| 1.3 | 注入式 `CommitCreate` 失敗 → registry 回滾＋`AbortCreate`、重啟無 durable ghost | 自動化 | ✅ 綠 | `app_wsid_test.go` `TestCommitFailureRollsBackRegistryWithoutTombstone` |
| 1.4 | Commit 失敗 × rollback persist 失敗 → 不 Abort、名額保留、`session-create-degraded` latch、既有 session 不受影響 | 自動化 | ✅ 綠 | `app_wsid_test.go` `TestCommitAndRollbackBothFailEnterDegraded` |
| 1.5 | **Reserve × shutdown barrier（拒新 app txn）** | 自動化（**本票新增**） | ✅ 綠 | `app_wsid_test.go` `TestCreateSessionRejectedByShutdownBarrier`（見 §4） |

### §5.2 Codex 多 thread × connection-wide 錄流（20 條；2.4 拆成 2.4a／2.4b，另新增 2.4c-i）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 2.1 | 兩 thread 的 notification 不串線 | 自動化 | ✅ 綠 | `app_codex_dispatch_test.go` `TestCodexTwoThreadsDoNotCrossWire` |
| 2.2 | approval 依 identity 歸屬、不串線 | 自動化 | ✅ 綠 | 同檔 `TestCodexCommandExecutionApprovalRoutesByIdentity`、`TestCodexMissingIdentityApprovalInPendingWindowFailsClosed` |
| 2.3 | completed-before-response 保證仍成立 | 自動化 | ✅ 綠 | 同檔 `TestCodexCompletedBeforeResponseOnProductionPath`、`TestCodexConcurrentStartsKeepPendingInvariant` |
| 2.4a | 一份 connection-wide wire log 收得到兩個 session 的雙向 frame（**不漏、不分裂成兩份檔**） | 自動化 | ✅ 綠 | `app_invariants_test.go` `TestWireLogCapturesFramesOfEverySession` |
| 2.4b | **錄流 frame 歸屬不串線**（§3.4.3 frame-level：交錯的兩個 session 逐 frame 各歸各的；六種 frame 形狀＋歸屬不到留空；歸屬跨 app 重啟可從磁碟重建） | 自動化 | ✅ 綠 | `app_wire_frames_test.go` `TestWireFramesAttributeEachInterleavedSessionSeparately`（c2s／s2c／notification／approval／pending-start／completed-before-response ＋廣播留空）、`TestWireFrameOwnersSurviveAppRestart`（跨 process 重建）、`TestWireFrameAttributionUsesSameRouterAsDispatch`、`TestWireFrameRequestIDMapIsScopedToGeneration`；`internal/wirelog/wirelog_test.go` `TestAttributionIsPerFrameNotPerKey`、`TestEmptyWSIDIsNotAQueryableSession` |
| 2.4c | **frame 歸屬的歷史展開不阻塞收尾**（`SegmentSet.Append` 與本代結算仍同步；歷史 generation 交單一背景 worker） | 自動化 | ✅ 綠 | `app_wire_frame_jobs_test.go` `TestEndSessionReturnsWhileHistoryRebuildIsBlocked` |
| 2.4d | **同一 generation 一個 app run 最多讀／重建一次**，多 session 共用（錯誤也快取） | 自動化 | ✅ 綠 | 同檔 `TestSharedGenerationIsExpandedOnce` |
| 2.4e | **已 finalize 的 generation 走 compact sidecar 快路徑**；缺 sidecar 才重讀錄流並**補建** | 自動化 | ✅ 綠 | 同檔 `TestFinalizedGenerationIsReadFromSidecarNotRebuilt`、`TestRebuiltGenerationIsBackfilledForNextRun`；`internal/wirelog/attribution_test.go` `TestFinalizeWritesSidecar`、`TestUnreadableSidecarFallsBackAndIsRepaired`、`TestMissingSidecarIsBackfilled`、`TestSidecarHitDoesNotReadWireLog`（數實際讀取次數，不拿受測對象自陳的 `FromSidecar` 當 oracle） |
| 2.4f | **`frames_status=pending` 的稽核必須先同步寫**，再由背景寫 resolved／failed | 自動化 | ✅ 綠 | 同檔 `TestPendingAuditIsWrittenSynchronously`（以 `runtime.Stack` 證明跑在 `closeWireSegment` 的 goroutine 上，不是靠排程競賽） |
| 2.4g | **讀不出來的 generation 記成 `failed`，不得偽裝成零 frame**；檔尾截斷仍 resolved 但要有稽核出口 | 自動化 | ✅ 綠 | 同檔 `TestCorruptGenerationYieldsFailedNotEmptyFrames`、`TestTruncatedGenerationIsAudited`（含經 sidecar 取得那次）；`internal/wirelog/attribution_test.go` `TestTruncatedTailSurvivesIntoResult` |
| 2.4i | **延後 resolve 期間新增的 generation 不得混進更早的 view**，也不得為還活著的 generation 寫 sidecar；`SegmentSet` 掉段時 fail loud 不得靜默算成「歷史只有這一代」 | 自動化 | ✅ 綠 | 同檔 `TestDelayedResolveDoesNotLeakNewerGenerations`、`TestMissingSegmentsFailLoudNotSilentResolve` |
| 2.4h | **shutdown bounded drain**：收尾時間不隨待辦／歷史 generation 數成長，未完成的下次啟動補完 | 自動化 | ✅ 綠 | 同檔 `TestShutdownDoesNotExpandPendingHistory`（量磁碟載入次數與殘留待辦數，不用牆鐘門檻）、`TestPendingFrameJobIsRecoveredAfterRestart`（開第二個 App 讀磁碟） |
| 2.5 | recorder error latch → 拒新 Codex session，**但不擋受控 restart** | 自動化 | ✅ 綠 | `app_wirelog_latch_test.go` `TestLatchBlocksNewSessionButNotRecovery`、`TestLatchBlocksProviderStart`、`TestLatchAllowsExistingSessionTeardown` |
| 2.6 | latch 下完整復原路徑（收乾 → terminate → wait → finalize 舊 generation → 配新 `wire_log_id` → 掛 recorder → handshake → 發布）全成功才解除 | 自動化 | ✅ 綠 | 同檔 `TestRecoveryOrderAndFailureKeepsLatch`、`TestRecoveryRewiresConnAndWireLog` |
| 2.7 | 掛 recorder／handshake 失敗 → dispose 新 server＋latch 保留＋不留未發布 server | 自動化 | ✅ 綠 | `internal/codex/owner_test.go` `TestThreeStageFailuresDisposeAndKeepEvidence`；`app_wirelog_latch_test.go` `TestRecoveryOrderAndFailureKeepsLatch` |
| 2.8 | 發布成功後 recorder **未被 Stop／Close**，錄到 server 終止才 finalize（非 probe-scoped） | 自動化 | ✅ 綠 | `internal/codex/owner_test.go` `TestHandoffKeepsRecorderOpenAndIDBeforeAttach`、`TestNewGenerationOwnerIsTheOnlyPathThatDetaches`、`TestFinalizeWithDetachesOnlyAfterServerExit` |
| 2.9 | 失敗的 generation 仍保留 `wire_log_id` 與收尾證據 | 自動化 | ✅ 綠 | `internal/codex/owner_test.go` `TestThreeStageFailuresDisposeAndKeepEvidence` |
| 2.10 | app-server generation restart 開新 `wire_log_id` | 自動化 | ✅ 綠 | `internal/codex/owner_test.go` `TestReplacementFinalizesOldGenerationBeforePublishingNew`、`TestServerDeathAutoFinalizesGeneration`；`app_wirelog_latch_test.go` `TestRecoveryRewiresConnAndWireLog` |
| **2.11** | **同一 WSID 橫跨兩個 generation 的 `[]WireSegmentRef` 完整且不混入他 session frame** | 自動化（**2026-08-21 矩陣重跑補記**；接線在 SegmentSet 票 1eaa08c／95504ee，原記未覆蓋見 §3.1） | ✅ 綠 | `app_wire_segments_test.go` `TestWireSegmentsSpanControlledRestart`、`TestWireSegmentsSurviveServerDeath`、`TestWireSegmentsSurviveAppRestart`、`TestWireSegmentNotRecordedForForeignConn`、`TestWireSegmentsConcurrentRangeIsNotExclusive`（§10 的 race 跑次包含） |
| 2.12 | B1 在 live host／in-flight turn 時拒絕執行 | 自動化 | ✅ 綠 | `app_wirelog_latch_test.go` `TestRecoveryRefusedWhileLiveHostOrInFlightTurn` |

### §5.3 遷移與啟動修復（3 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 3.1 | legacy migration 的 crash／restart 得到**相同 WSID**（原子持久化＋migration marker） | 自動化 | ✅ 綠 | `internal/wsregistry/migrate_test.go` `TestMigrateIsIdempotentAcrossRestart`、`TestMigrateRefusesWhenLiveEntriesExistWithoutMarker`；`app_restore_dormant_test.go` `TestLoadRegistryTriggersMigrationOnce`、`TestMigrationPersistFailureBlocksProviderStart` |
| 3.2 | incomplete turn restart → `stream_error`＋failed，不殘留 busy／pending approval | 自動化 | ✅ 綠 | `app_startup_repair_test.go` `TestStartupRepairEmitsStreamErrorThenFailed`、`TestRepairCoversEveryIncompleteWSID`、`TestCompleteTurnIsNotRepaired` |
| 3.3 | 啟動修復序列 crash 後重跑冪等、收斂到相同狀態 | 自動化 | ✅ 綠（**斷言維度是計數，見右**） | 同檔 `TestStartupRepairIsIdempotent`、`TestStartupRepairIsIdempotentAfterIndexLoss`、`TestStartupOrderIsFrozen`、`TestUIOpensOnlyAfterRepair`。**限制**：兩條冪等測試的斷言都是 `len(second) != len(first)`——抓得到「重複 append `stream_error`」這個主要失效形狀，但 spec 那句「**收斂到相同狀態**」的**內容面**（第二次跑出的事件是否逐筆等同）**沒有斷言**。`AfterIndexLoss` 變體補強的是「權威來源是 audit 而非 index」，斷言維度仍是計數。本票**不改測試**，只記錄這個邊界 |

### §5.4 移除與 shutdown（4 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 4.1 | Remove × New 同 ownership token 競態 | 自動化 | ✅ 綠 | `app_remove_test.go` `TestRemoveXNewShareOwnershipToken` |
| 4.2 | removed tombstone 重啟與 index rebuild **不復活** | 自動化 | ✅ 綠 | `app_remove_test.go` `TestRemovedTombstoneSurvivesRestartAndRebuild`；`app_restore_dormant_test.go` `TestRemovedTombstoneNotRestored`；`internal/wsregistry` `TestRemovedLegacyIsNotRemigrated` |
| 4.3 | shutdown × Start barrier | 自動化 | ✅ 綠 | `app_test.go` `TestShutdownGateBlocksLateCodexStart`、`TestShutdownGateBlocksLateEnsure`、`TestNewStartBarrier` |
| 4.4 | 8 session 含卡死 Claude → shutdown 總時間仍為單一 bounded window | 自動化 | ✅ 綠 | `app_shutdown_multi_test.go` `TestStuckClaudeSessionsShareSingleBoundedWindow`、`TestAllTeardownsRunConcurrently`、`TestCodexSharedServerTerminatedAfterAllHostsDrained`、`TestShutdownFollowsFrozenOrder` |

### §5.5 Replay index（6 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 5.1 | 三種 crash：落後補掃／超前修復／checkpoint 越過 open turn | 自動化 | ✅ 綠 | `internal/replayindex/rebuild_test.go` `TestIndexBehindIsCaughtUp`、`TestIndexAheadIsRepaired`、`TestCheckpointPastOpenTurnRebuilds`；`index_test.go` `TestOpenTurnStartSurvivesReopen` |
| 5.2 | index 尾端 truncate 續用 vs 中段 quarantine 重建 | 自動化 | ✅ 綠 | `internal/replayindex/corrupt_test.go` `TestTailCorruptionTruncatesAndContinues`、`TestTailCorruptionActuallyTruncatesDisk`、`TestMidCorruptionQuarantinesAndRebuilds`、`TestMidCorruptionDoesNotLoseEvents` |
| 5.3 | degraded latch 每 generation 只通知一次、通知事件不觸發遞迴 | 自動化 | ✅ 綠 | `internal/replayindex/degraded_test.go` `TestIndexFailureDoesNotBreakAuditAndNotifiesOnce`、`TestNotificationEventDoesNotRecurse`、`TestLatchBeforeNotify`、`TestClearDegradedOpensNextGeneration`；`app_replayindex_test.go` `TestIndexDegradedNotifyDoesNotDeadlockAndRecovers` |
| 5.4 | runtime 重建 × 並行 append barrier：窗口內事件恰好索引一次、無缺口無重複 | 自動化 | ✅ 綠 | `internal/replayindex/runtime_rebuild_test.go` `TestRebuildCoversPreLockWindow`、`TestCrashDuringRuntimeRebuildDoesNotDuplicate`、`TestRestartAfterSuccessfulAttachDoesNotDuplicate`；`app_replayindex_test.go` `TestAppendLandsMidRebuildScan` |
| 5.5 | sustained-append (a)：鎖外 catch-up 始終不達標 → 界限內中止、保留 latch、backoff 重試，不 busy-loop | 自動化 | ✅ 綠 | `internal/replayindex/runtime_rebuild_test.go` `TestRebuildNeverConvergesKeepsLatch`、`TestRebuildRecordLimitBindsIndependently`、`TestRebuildRetryResumesFromCursorWithoutRebulk`；`app_replayindex_test.go` `TestNotConvergedTriggersBackoffRetry` |
| 5.6 | sustained-append (b)：達標取鎖後又超限 → 立即解鎖重試、鎖內處理量不超過凍結上限 | 自動化 | ✅ 綠 | `internal/replayindex/runtime_rebuild_test.go` `TestRebuildOverLimitUnderLockUnlocksAndRetries`、`TestUnlockedScanReleasesIndexMutexBetweenSegments` |

### §5.6 未釘選 session 的 approval transient routing（2 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 6.1 | 雙 pane 已滿時，未釘選來源以 transient secondary presentation 顯示，persistent pin 不被改寫 | 自動化（vitest） | ✅ 綠 | `frontend/src/stores/session.test.ts`「未釘選來源以 transient 顯示…」「transient 期間第二筆 approval 不覆蓋原釘選的備份」；`frontend/src/components/ApprovalDialog.test.ts`「未釘選來源 transient 顯示於次要 pane，allow 後恢復原釘選」 |
| 6.2 | 六種凍結觸發（allow／deny／timeout／dismiss／remove／shutdown）各自恢復原 pin | 自動化（vitest） | ✅ 綠（**但前端只有 2 條分支，見右**） | `ApprovalDialog.test.ts` 的 `it.each`（timeout／dismiss／remove／shutdown 四個 case）＋allow／deny 兩條按鈕情境。**不要把 `it.each` 的四個 case 讀成四條獨立分支**：它們走的是**同一個** `approval:dismiss` handler，`timeout` 與 `resolved` 差在 `cause`，而 `remove`／`shutdown` **只差一個前端根本沒讀的 `reason` 欄位**（`ApprovalDialog.vue` 的 `reason` 是使用者輸入框，不是 payload 欄位）。**六種觸發在前端收斂為 dismiss／resolve 兩條路徑，reason 由後端區分**（`app.go:1676` shutdown、`app.go:5149` session_removed）。這不是假綠——恢復原 pin 的行為確實被驗到，但覆蓋維度是 2 不是 6 |

### §5.7 跨切面不變量（3 條）

| # | 條款 | 驗證方式 | 結果 | 證據 |
|---|---|---|---|---|
| 7.1 | event_id 檔案級單調在 8 session 並行 turn 下成立 | 自動化 | ✅ 綠 | `app_invariants_test.go` `TestEventIDMonotonicAcross8ParallelSessions`（另 `TestReducerCascadeIsAtomicAcrossSessions` 守 cascade 相鄰性） |
| 7.2 | 舊 journal（無 WSID）legacy 歸屬 fixture | 自動化 | ✅ 綠 | 同檔 `TestLegacyJournalWithoutWSIDAttributes` |
| 7.3 | 每 session 單一 in-flight turn，拒絕第二筆 | 自動化 | ✅ 綠 | 同檔 `TestSecondInFlightTurnRejected`、`TestInFlightTurnDoesNotBlockNewSession`（**production 行為變更，見 §6**） |

### §2.8 spec §7「風險與待驗證假設」逐項對照（5 項）

spec §7 列了五項待驗證假設。前兩項屬實測／live probe（與 §5 的 A10、§5.0 是同一件事，
在那裡有完整記錄）；後三項是**工程假設**，本次以既有測試的綠色跑次交代。

| # | spec §7 假設 | 結果 | 證據／說明 |
|---|---|---|---|
| 7.1 | Claude 多常駐子行程的實際資源占用（4 session RAM/CPU） | 🟡 部分執行 | 見 §5.1（idle 實測 385 MB／process；負載中實測待 owner 實機） |
| 7.2 | Codex 單一 app-server 多 thread 並行 turn 的實際行為 | 🚫 重跑無法執行 | 見 §5.0（2026-08-14 已判定 GATE GO；帳號用量限制至 2026-08-20） |
| 7.3 | Claude per-WSID socket／MCP config 的檔案數與清理 | ✅ 既有測試覆蓋 | `app_claude_multi_test.go` `TestTwoClaudeSessionsDoNotShareSocketOrMCP`（各自獨立 socket／mcp／broker／子行程）、`TestClaudeApprovalSocketFitsInLongStateDir`（104 byte sockaddr 上限）；`session_host_test.go` `TestSockIndexFreeList`、`TestSockIndexExhaustionFailsLoud`（檔案數上界與回收）；`app_remove_test.go` `TestRemoveCleansUpPerWSIDMCPConfig`、`TestRemoveDeniesAllPendingApprovalsAndCleansFiles`（remove 時一併清） |
| 7.4 | 既有 M1.5 恢復測試基線大改（restore v2）——舊測試語意遷移不減 | ✅ 既有測試覆蓋（**但見下方限制**） | `app_test.go` `TestRestoreViewWindowReplay`、`TestResumeCandidateStagedThenCommitted`、`TestNewSessionRestoreWriteFailureKeepsEntry`；`app_claude_multi_test.go` 的 resume 歸屬八條（`TestSecondSessionOfSameProviderDoesNotInheritResume`、`TestProviderRestoreUnambiguousUsesBothSources`、`TestSoleRegistrySessionStillResumes`、`TestRemovedSessionsResumeIsNotInheritedByNextSession` 等）；`app_restore_dormant_test.go` 全檔 |
| 7.5 | `AppendReceipt` 改動 M1 核心路徑——以既有 audit 測試全綠＋event_id 單調護欄 | ✅ 既有測試覆蓋 | `internal/appcore/sink_test.go` `TestJSONLSinkReceiptMatchesFileOffsets`、`TestSinkReopenContinuesOffsets`、`TestJSONLSinkShortWriteRecalibratesOffset`；`internal/appcore/manager_test.go` `TestFileLevelEventIDMonotonicAcrossProviders`、`TestAuditFailureIsLoud`；`internal/appcore/manager_index_test.go` `TestIndexNotFedWhenSinkWriteFails`；`app_invariants_test.go` `TestEventIDMonotonicAcross8ParallelSessions` |

**§7.4 的判定限制（誠實邊界）**：上表證明的是「restore v2 的語意有測試、且本次全綠」。
spec 那句的字面要求是「**舊測試語意遷移不減**」——嚴格驗證需要把 M1.5 當時的測試清單與現行清單
逐條比對「哪一條被哪一條取代」，**本票沒有做這件清單比對**。以「不減」的字面標準看，
這一列是「**未反證**」而不是「已逐條證實」。

---

## 3. 未覆蓋清單（3 項）

### 3.1 §5.2 第 11 條：`[]WireSegmentRef` 跨 generation —— **未接線，不可能綠**

> **2026-08-21 補記**：已由 SegmentSet 接線票（1eaa08c、95504ee）完成 production 接線，
> 2.11 改記綠（證據見該列與 §10）。以下保留當時的判定原文。

**事實**（本票獨立核對）：

```
$ grep -rn "SegmentSet" --include='*.go' . | grep -v _test.go | grep -v internal/wirelog/segments.go
（零命中）
$ grep -rn "WireSegmentRef" --include='*.go' .
app.go:6267:// 等 []WireSegmentRef 接線時再加，不預留用不到的欄位）。…
```

`internal/wirelog/segments.go` 的 `NewSegmentSet`／`OpenSegmentSet`／`Append`／`For`
**全 repo 零 production 呼叫端**，只有 `segments_test.go` 引用；`app.go:6267` 還留著一句
「等 `[]WireSegmentRef` 接線時再加」的註解。

因此 `internal/wirelog/segments_test.go` 的 `TestSegmentsSpanTwoGenerations`／
`TestForDoesNotMixOtherSessionFrames` 測的是**一個沒有任何呼叫端的型別**。這兩條是綠的，
但它們**不構成 spec §5.2 第 11 條的驗收**——spec 要求的是「同一 WSID 橫跨兩個 generation 的
session 級錄流證據完整且不混入他 session frame」，而 production 目前根本沒有產生 session 級
的 `[]WireSegmentRef`。**不得因為型別層單元測試綠而記為已覆蓋。**

**已具備的部分**：connection-wide wire log 本身完整——每個 generation 一份、雙向 frame 都錄、
無法歸屬的 frame 照寫不丟（§5.2 第 4 條，`TestWireLogCapturesFramesOfEverySession`
＋`internal/wirelog` `TestUnattributedFrameStillWritten`）。**缺的是 session 級的歸屬索引**。

**成因**：計畫階段的排程缺口——Task 10／11 建了型別，接線從未被排進任何 task 的 Files
（Task 30 報告 §4⑦ 已記錄）。

**後續建議**：獨立開票。需要在 generation 邊界（`ensureAppServer`／`RecoverCodexRecording`／
server 意外死亡）與 session start／end 算出 per-WSID frame range 並 `Append`，決定
`SegmentSet` ownership 放在 App 哪一層，並定義 crash 後與 `RebuildFrameIndex` 的對齊方式。
**這是一張獨立票的量，不是收尾補丁。**

### 3.2 無 single-instance guard —— **本票新發現的未受保護前提（不在 spec §7 五項之內），待 owner 裁決**

> **2026-08-21 補記**：owner 裁決後已出貨（a810a40：flock 早於任何 writer、撐到 shutdown
> 收完才放、拒絕 UX＋i18n）。證據：`main_singleinstance_test.go`
> （`TestBareEntryRaceExactlyOneEntersWriterInit`、`TestNoWriterOpensBeforeLeaseIsAcquired`、
> `TestLeaseHeldUntilWritersClosed` 等）＋`internal/singleinstance`，均含在 §10 的 race 跑次。

> **這一項不是 spec 列出的待驗證假設**（spec §7 的五項見 §2.8），而是驗收時從
> `internal/appcore/sink.go` 的 doc 讀出來的**新發現**：doc 寫了一個假設，但 repo 沒有任何機制保證它。

**事實**（本票獨立核對）：

```
$ grep -rnE "flock|O_EXCL|LOCK_EX|lockfile" --include='*.go' .
（零命中）
```

`internal/appcore/sink.go:55-56` 的 doc 明文假設「同一時間只有一個 process／handle 持有寫入權」，
但 repo 沒有任何機制保證它。

**風險界定（精確版，不要擴大也不要縮小）**：`events.jsonl` 以 `O_APPEND` 開啟，兩個 writer 的行
不會互相截斷 → **稽核資料本身安全**。壞掉的是 `JSONLSink.offset` 這個**本機累加值**——它偵測不到
其他 process 的寫入，於是 spec §3.5.2「index 一律以 `AppendReceipt` 為準」的唯一可信 offset 來源
前提破裂，replay index 的 checkpoint 會指到錯的 byte 位置。

**為什麼標成待裁決而非 backlog**：解法（`stateDir` 加 lock file ＋啟動時的使用者可見拒絕訊息＋i18n）
**會改變啟動行為並新增一條使用者可見的失敗路徑**（第二個視窗開不起來），落在「會改變使用者可見行為」
的決策邊界。**本票不自行實作。**

### 3.3 pane pins 持久化 —— **未接線，A5 不可能通過**（final review 更正，原記為「無法執行(G)」）

> **2026-08-21 補記**：已由 pane pins 票（cb477bb…bce1f20，六輪 review）完成持久化與啟動
> 重建。證據：`app_pane_layout_test.go`、`internal/wsregistry/layout_test.go`，均含在 §10 的
> race 跑次。A5 的**實機 GUI 操作**仍屬 §5 實機矩陣、需 owner 實機執行，該表不改。

**事實**（final review 獨立核對）：

```
$ grep -rn "SetLayout|\.Layout\(\)" --include='*.go' .
internal/wsregistry/store.go:213,218,231   （宣告本體）
internal/wsregistry/store_test.go:202,205,…（只有型別層測試）
→ production 呼叫端：零
$ grep -rni "layout" frontend/wailsjs/go/main/App.d.ts
（零命中 → 沒有任何 layout 相關 Wails binding）
```

`frontend/src` 唯一的持久化是 `lib/persist.ts` 的 localStorage 包裝，用途只有
timeline 摺疊／高度與 gate 面板寬度三個 key（`App.vue:151-158` 的 `wb.tl.open`／
`wb.tl.height`／`wb.gate.width`），**完全沒碰 pins**。`App.vue:224` 的 `onMounted`
只做 `hydrateSessions(await ListSessions())`——那條路徑只回填 session metadata 與狀態，
不還原釘選；`stores/session.ts:162` 的 `pins` 初始恆為 `[null, null]`。

**後果**：每次重啟兩個 pane 都是空的，使用者必須重新釘選。spec §3.8「啟動只重建兩個釘選 pane」
在 production **沒有輸入可用**——A5 因此不是被 GUI 環境擋住，是**沒有可驗收的實作**。
（釘選**之後**的視窗化載入本身是完整且綠的，見 A5 那列列出的三條測試。）

**與 spec 的關係**：spec §3.2.1 的 registry durable metadata 凍結白名單**明列**「pane pins／
focused pane」（design 文件 L62），因此這是凍結契約中的一項，不是可選增強。

**成因**：與 §3.1 同型的排程缺口——plan 第 244-245 行只列了
`func (s *Store) SetLayout(l Layout) error` 與 `func (s *Store) Layout() Layout` 兩個簽名，
**沒有任何後續 task 接它**：Task 2 定義了型別、Task 26-29 的前端從未取用，責任掉在兩者之間。

**後續建議**：獨立開票（binding + 前端 pin/unpin/focus 的寫入時機 + 啟動 hydrate 順序，
還要決定「釘選的 session 已被移除」時的還原語意）。**本輪不實作——那是新功能，待 owner 裁決。**

---

## 4. 本票新增的一條測試（§5.1 第 5 條）＋mutation 交叉矩陣

驗收時逐條對照 spec §5.1 發現：**「Reserve × shutdown barrier（拒新 app txn）」在 App 層沒有任何
測試釘住**。production 側的柵欄是存在的（`app.go:552` `CreateSession` 第一行就是 `beginAppTxn()`），
但其他入口的 shutdown gate 測試（`TestShutdownGateBlocksLateCodexStart`／`TestShutdownGateBlocksLateEnsure`）
守的是 `StartSession`／`ensureAppServer`，**守不到 `CreateSession` 這一格**。這屬於「保證只寫在
production code 裡、沒有守門」的形狀，一次無心的重構就會靜默失效。

新增 `app_wsid_test.go` `TestCreateSessionRejectedByShutdownBarrier`。

**窗口設計（避免結構性兜底）**：測試用 `hookShutdownStep` 把 shutdown 停在**第一步**
`reject_new_txn`——此時 `shuttingDown` 已翻 true、**Manager 尚未 Close**。若改成「shutdown 全部跑完
後才呼叫 `CreateSession`」，`Manager.ReserveSession` 的 `ErrClosed` 會結構性兜底，拿掉柵欄照樣綠。
測試內另放一道**反向守門**：先 `ReserveSession`＋`AbortCreate` 探一次，確認 Manager 真的還開著，
窗口不成立就直接 fail。

**Mutation 交叉矩陣**（brief 要求的固定產出形狀：一個 mutation 之下，**其餘每條測試**的紅／綠）：

| Mutation | 位置 | 受測條款 | 全 root package `go test . -race -count=1` 結果 |
|---|---|---|---|
| **M-S1**：刪掉 `CreateSession` 的 `beginAppTxn()`／`defer endAppTxn()` | `app.go:552-555` | §5.1 第 5 條 | **恰好 1 條紅**：`--- FAIL: TestCreateSessionRejectedByShutdownBarrier`，訊息 `shutdown 開始後不得再建立 session（§3.1 app txn 柵欄）：<nil>`。**其餘 root package 全部測試皆綠。** |

這個交叉矩陣同時證明三件事：(a) 這個缺口是**真的**——mutation 之下沒有任何既有測試變紅，
所以先前確實零覆蓋；(b) 新測試是**唯一**守門者；(c) 訊號是**斷言**打紅（`err == nil`），
不是編譯錯、不是 panic、不是逾時。

mutation 已還原：`grep -rn MUTATION --include='*.go' .` 為空，`git diff` 只剩測試檔新增。

> **方法論說明（誠實邊界）**：本票只對**新增的那一條**做了完整交叉矩陣。Task 0-30 各自的 mutation
> （Task 26 一張票 55 個、Task 30 十個）**未在本票重跑**，其紅／綠證據見各自的 task report。
> 「每個 mutation × 其餘每條測試」的全域矩陣沒有做——那是 60+ 個 mutation × 每次 ~110s 的
> root package 全跑，屬獨立的測試品質工程票。

---

## 5. spec §6 實機驗收矩陣（A1-A10）

**8 項無法執行、1 項部分執行、1 項未實作。** 阻塞原因分兩類，逐項標明：

- **(G) 無 GUI 操作能力**：本 session 是非互動 agent 環境，`wails build` 產出的 `.app` 可以建置
  但無法由 agent 進行點擊／切焦點／捲動／確認對話框等操作。這些驗收項的判定條件本身就是**人眼與滑鼠**。
- **(Q) Codex 帳號用量限制至 2026-08-20**：涉及 Codex live turn 的項目另有外部阻塞
  （同一阻塞已記錄於 Task 0 ledger，`cmd/probe-codex-parallel` 的 live 重跑也因此停擺）。

> **A5 不屬於上面兩類**（final review 更正）：它先前被記成「無法執行（G）」，但**就算給它 GUI
> 也會失敗**——pane pins 在 production 沒有持久化路徑，重啟後根本沒有釘選 pane 可恢復。
> 它不是被環境擋住，是**未實作**，因此改記 ❌ 未實作並列進 §3 缺口章節（§3.3）。

| # | 項目 | 判定 | 阻塞 | 已具備的自動化替代覆蓋（**不等於實機通過**） |
|---|---|---|---|---|
| A1 | 雙 provider × 多 session 並行（A 執行中切 B 送出） | 🚫 無法執行 | G＋Q | `TestEventIDMonotonicAcross8ParallelSessions`（8 session 並行 turn）、`TestCrossProviderSubmitDoesNotBlock`、`TestDualSessionsConcurrently`；Codex 單 app-server 多 thread 真並行已由 **Task 0 的 live probe 於 2026-08-14 判定 GO**（`docs/spikes/m3b-codex-parallel.md`） |
| A2 | 雙 pane 並看與焦點切換、unread | 🚫 無法執行 | G | `frontend/src/components/DualPane.test.ts`（並排與焦點切換）、`PaneView.test.ts`＋`stores/session.test.ts`（focused pane 操作語意、unread） |
| A3 | approval 跨 pane／未釘選 transient 路由 | 🚫 無法執行 | G＋Q | §5.6 兩條全綠（store＋ApprovalDialog）；後端路由 `TestApprovalCarriesWSIDAndFIFOPromotion` |
| A4 | 8/8 上限拒絕＋`n / 4` 顯示＋關閉釋放名額 | 🚫 無法執行 | G＋Q | `TestReserveSessionLimitIsAtomic`（恰 4）、`SessionList.test.ts`（`n / 4` 渲染與移除確認）、`TestRemoveReleasesSlotOnlyAfterAllStepsSucceed` |
| A5 | 重啟：釘選 lazy 恢復（20 turn）、非釘選 metadata、向上分頁 | ❌ **未實作** | 不是環境阻塞 | **就算有 GUI 這項也會失敗**：pane pins 沒有持久化，重啟後 `pins` 恆為 `[null, null]`，沒有「釘選 pane」可恢復。詳見 §3.3。已具備的是**釘選之後**的視窗化載入：`TestRestoreLoadsLast20TurnsPlusOpenTurn`、`TestPagingUsesBeforeEventIDCursor`、`TestWindowExcludesOtherSessions`；前端 `session.test.ts` 的 `loadOlder` 去重與捲動補償 |
| A6 | index 落後補掃／注入損壞 → 重建＋通知 | 🚫 無法執行 | G | §5.5 六條全綠（落後／超前／truncate／quarantine／degraded 通知／runtime 重建） |
| A7 | 未完成 turn 重啟 → failed 解除 busy | 🚫 無法執行 | G | §5.3 第 2／3 條全綠（`app_startup_repair_test.go` 七條） |
| A8 | 舊 workspace 升級：legacy 歸屬、resume 可用、重啟不產生第二枚 WSID | 🚫 無法執行 | G | §5.3 第 1 條＋`TestLegacyJournalWithoutWSIDAttributes`、`TestLegacyViewWindowWithEventsMigrated`、`TestLegacyEmptyViewWindowNotMigrated` |
| A9 | M2／M3a／M3a.1 迴歸抽驗（Gate 流程、TCA、收件匣、assists） | 🚫 無法執行（實機抽驗） | G | **自動化迴歸套件全綠**：`internal/gate`／`internal/gatepolicy`／`internal/evidence`／`internal/escalation`／`internal/assist`／`internal/plan`／`internal/spec` 全部 ok（§1），前端 `GateConsole`／`TcaWorkspace`／`EscalationInbox`／`PlanWorkspace` 測試綠 |
| A10 | Claude 4 session 常駐的 RAM／CPU 實測（§7.1） | 🟡 **部分執行** | G（負載中量測需實機） | 見 §5.1 的 idle 實測數據 |

### 5.0 Task 0 的 live `GATE GO` 重跑 —— 🚫 **無法執行**

`cmd/probe-codex-parallel` 是 M3b 的架構前提 gate（單一 `codex app-server` 能否承載多並行 thread）。

| 項目 | 狀態 |
|---|---|
| 原始判定 | ✅ **GATE GO**——2026-08-14 以 natural／forced 兩次真實執行，(a)(b)(c) 三項全成立（`docs/spikes/m3b-codex-parallel.md`） |
| 本次重跑 | 🚫 **無法執行**——Codex 帳號用量限制至 **2026-08-20**（外部阻塞，已記於 Task 0 ledger） |
| 影響 | GO 判定不因此撤回（證據已在 rev3 文件與兩份完整 wire log 中），但**driver 進 CI gate 前必須補跑一次 natural run**——這筆待辦仍開著 |

**不把這一項略過、也不記綠**：本次沒有跑，就是沒有跑。

### 5.1 A10 部分實測：4 個常駐 idle Claude 子行程

用 **production 的 argv**（`internal/claude/session.go:36` 的 `Config.args()`）與 **pin 版本的 binary**
（`tools/claude-cli/node_modules/.bin/claude`，2.1.223）起 4 個子行程、**stdin 保持開啟但不送任何 prompt**
（＝不消耗 API 額度），量測常駐足跡：

| 時點 | 四個子行程的 RSS | %CPU |
|---|---|---|
| t = 30s | 386.8 / 388.2 / 382.8 / 385.8 MB | 0.0-0.1% |
| t = 90s | 389.7 / 390.6 / 385.2 / 388.5 MB | 0.0-0.1% |

**判讀（含限制，不要過度延伸）**：

- 單一常駐 idle Claude 子行程約 **385 MB RSS**，90 秒內不成長；idle CPU 幾乎為 0。
- **RSS 相加會高估**：macOS 的 RSS 把 Node runtime 等共享頁在每個 process 各算一次，
  4 × 385 MB ≈ 1.5 GB 是**上界**而非增量成本。本次的 `vm_stat` 前後差值受同時進行的 build／test
  page cache 干擾，**不可用**，故不列。
- 這是 **idle 且無對話 context** 的下界形態：真實 session 另有 conversation context、MCP
  permission-prompt 子行程與錄流緩衝，**負載中的實際占用會更高**。
- 因此 **§7.1「4 個 session 的 RAM/CPU」尚未完整回答**；idle parking 的成本效益評估需要
  owner 在實機做一次「4 session 各跑過幾輪之後」的量測。

> **本次量測造成的副作用（Fail Loud）**：清理探針時用了
> `pkill -f "include-partial-messages"`，這個 pattern **同時命中了兩個先前已存在、與本次量測無關的
> `claude` 長駐行程**（PID 680／12797，各已存活約 2 天），它們被一併終止。這是我造成的環境副作用，
> 與 M3b 程式碼無關；後續若要重做量測，應改用「只殺自己記下的 PID」而不是 pattern kill。

---

## 6. PR 揭露事項

1. **`ErrTurnInFlight` 是 Task 30 引入的 production 行為變更**（補上 spec §1.1 在 Claude 路徑上
   原本不存在的保護）。正常 UI 路徑零影響（前端在 `busy` 時就 disable 送出，且清除條件比後端更嚴），
   但 **Codex 在 turn 進行中送第二筆的錯誤文案有可見變更**：
   `codex: turn already active` → `appcore: a turn is already in flight for this session`。
   兩者都是未 i18n 的原始 Go 字串、都走 `pushError`；兩 provider 文案統一是改善，但**確實是使用者可見變更**。
   殘留風險：provider 子行程靜默死亡且不重啟 app 時，該 session 送不出第二輪；兜底為
   (a) 啟動修復補 `stream_error → failed`、(b)「開新對話」可脫身（`TestInFlightTurnDoesNotBlockNewSession` 守住）。
2. **§5.2 第 11 條未接線**（§3.1）——merge 前需 owner 確認以獨立票追蹤。
3. **無 single-instance guard**（§3.2）——**待 owner 裁決**，會改啟動行為＋新增使用者可見失敗路徑。
4. **spec §6 實機驗收 9 項無法在本 session 執行**（§5）——需 owner 實機補跑；本文件保留矩陣格式供逐項填寫。
5. **Codex live probe 無法重跑**：Task 0 的 `cmd/probe-codex-parallel` 已於 2026-08-14 以 natural／forced
   兩次真實執行判定 **GATE GO**（`docs/spikes/m3b-codex-parallel.md`），但**帳號用量限制至 2026-08-20**，
   本次無法重跑確認。Task 0 ledger 另記一項待辦：**該 driver 進 CI gate 前必須補跑一次 natural run**。
6. **牆鐘相依測試（既有問題，非本票造成）**：見 §7。另有一條既有測試競態在驗收過程被打紅並**已修**
   （`TestClaudeApprovalCarriesWSID` 的等待條件沒有涵蓋 UI emit），root cause 與確定性重現見 §8。
7. **Task 0 的殘留風險仍未關閉**：Codex 兩 thread「架構上不串行化」已證實，但**吞吐並行不保證**
   ——新開 thread 首輪可能有數秒無回應空窗，UI 不得假設兩 session 同時串流。
8. Task 30 登記但刻意不做的四項（`noteStartupWarning` 只保留第一則、`handleServerRequest` 的
   approval goroutine 可能在 `Manager.Close()` 之後 emit、`internal/replayindex` 測試未持 `idx.mu`、
   `M6b` 守門邊界較窄）維持原判定，見 `task-30-report.md` §4。

---

## 7. 牆鐘相依測試的觀察記錄（重要，但不是本票造成的）

Go gate **第一次執行時紅了一條**：

```
--- FAIL: TestAppServerTerminateKillsGroup (30.04s)
    session_test.go:101: kill escalation too slow
FAIL	github.com/slam0504/sdlc-workbench/internal/codex	33.229s
```

**判定：負載造成的偽陽，非缺陷。** 證據：

| 執行方式 | `internal/codex` package | 該條測試 |
|---|---|---|
| `go test -race ./... -count=1`（第 1 次，`go build`／`go vet`／greps 同時進行） | 33.229s，FAIL | 30.04s |
| `go test -race ./internal/codex/ -count=1 -run TestAppServerTerminateKillsGroup`（單獨） | 1.375s，PASS | **0.02s** |
| `go test -race ./... -count=1`（第 2 次，機器閒置） | 3.878s，ok | 綠 |

該測試的預算是 `TermGrace = 200ms` ＋ SIGKILL 回收，斷言上限 5s；單獨跑量到 **0.02s**，
負載下量到 **30.04s**（150 倍）。這條測試量的是**牆鐘**而非同步點，所以在 package 併行
＋`-race`＋同時有其他建置活動時會偽陽。

**同類已知項**（Task 30 §4③ 已登記，本票再次確認未改）：`internal/assist` 的
`context.WithTimeout(15s)`、`internal/claude` 的 `time.After(5s)`。

**操作建議**：三個 gate **必須分開跑**（本次即如此）；`go test ./...` 本身的 package 併行
無法關閉時，若這三處紅了，先單獨重跑該 package 再判定。**根治要改測試的同步設計，不在 M3b 範圍。**

**具名清單（owner 2026-08-19 逐一確認，共 5 條；2026-08-21 文件整理落地）**——這幾條紅了
先單獨重跑再判定，不計入回歸；它們的綠燈也**不作為**任何修正的通過證據：

1. `internal/codex` `TestAppServerTerminateKillsGroup`
2. `internal/assist` `TestClaudeAssistFailsLoudOnOversizedLine`
3. `internal/claude` `TestMultiTurnSendAndTurnBoundaries`
4. root package `TestInFlightTurnDoesNotBlockNewSession`
5. `internal/codex` `TestAppServerMidStreamDeath`

---

## 8. 驗收過程打紅的第二條既有測試競態（已修，root cause 已定位）

加入 §4 的新測試後重跑 Go gate，**另一條既有測試紅了**：

```
--- FAIL: TestClaudeApprovalCarriesWSID (0.03s)
    app_claude_multi_test.go:203: 必須發出 approval:request UI 事件
```

**這不是「flaky，重跑就好」，root cause 已定位並用診斷式 mutation 確定性重現。**

**機制**：production 的 `pumpApprovals`（`app.go:4920-4941`）是三步——
`registerApproval()` → `EmitApprovalRequest()`（稽核）→ `emit("approval:request")`（UI）。
測試 helper `seedApproval`（`app_claude_multi_test.go:80`）等的條件是
`waitFor(… a.pendingByID(id) != nil)`，也就是**只等到第一步**。它一返回，測試就直接
`ui.find("approval:request")`——與 pump goroutine 賽跑。負載低時 goroutine 一路跑完三步所以恆綠；
負載高（package 併行＋`-race`）時被排程切走就偽陽。

**確定性重現（診斷式 mutation）**：在 `registerApproval` 與 UI `emit` 之間插入
`time.Sleep(50ms)` → `TestClaudeApprovalCarriesWSID` **必紅**，同一 mutation 之下
`TestApprovalCarriesWSIDAndFIFOPromotion`、`TestRemoveDeniesPendingApprovals` **仍綠**
（它們不在 seedApproval 之後立刻讀 UI 事件）——確認缺陷只落在這一條的等待條件上。

**修正**（測試側一行，production 一行未改）：改為等**真正的**條件，沿用 `app_test.go:367`
既有慣例 `waitFor(t, "approval:request dialog event", …)`。修正後把同一個 50ms 診斷 mutation
再套一次 → **綠**，證明窗口確實關上（不是把時間拉長矇混過去）。

**為什麼是我打紅的**：新增的測試改變了 package main 的執行時序與負載分佈。**缺陷是既有的**
（自 Task 26 該斷言加入時就存在），與 §7 那三處「量牆鐘」的既有問題是同一類形狀
（測試的同步點沒有對齊 production 的實際完成點），差別在這一條可以用一行等待條件修好，
§7 那三處要重新設計同步點。

---

## 9. 本票的 commit 內容

| 檔案 | 性質 |
|---|---|
| `docs/spikes/m3b-results.md` | 新增（本文件） |
| `README.md` | 更新：多 session 章節、`.workbench/` 檔案表、`internal/` 三個新 package、開發藍圖 M3b 列、gate 分開跑的提醒 |
| `app_wsid_test.go` | 新增一條測試（§4） |
| `app_claude_multi_test.go` | 一行等待條件修正（§8） |

**production code 一行未改**——本票只動測試與文件。

---

## 10. 矩陣重跑（2026-08-21，最終樹 d35c4ec）

**本節是 2026-08-17 六項 follow-up 清單的第 6 項**（per-WSID design、pane pins、SegmentSet、
single-instance guard、Remove/Start phase、matrix）。前五項各自出貨並經 review 核可
（第 5 項的 re-review 鏈於 2026-08-21 APPROVED）後解除凍結，在**最終樹**上重跑四個收尾 gate。

**最終樹相對 §1 基線（2026-08-15）的差異**：pane pins 持久化（cb477bb…bce1f20 六輪）、
SegmentSet production 接線（1eaa08c、95504ee → 2.11 轉綠）、frame-level 歸屬與非阻塞展開
（8965948、5aa008e → §5.2 拆出 2.4b-i）、single-instance guard（a810a40）、audit lifecycle
與 state lease 生命週期（8b81569…fcd0db2）、binding 面四分類＋固定形狀薄包裝
（34d207f…0a461cb）、proc 收尾與取消因果仲裁（8188014…d35c4ec）。

**四個 gate 依序分開跑（§7 的操作規則），全綠**：

| Gate | 指令 | 結果 |
|---|---|---|
| Go | `go build ./... && go vet ./... && go test -race ./... -count=1` | ✅ 全綠一次過（21 行 `ok`；package main 230.6s、`internal/proc` 33.3s；§7 具名 5 條本次皆綠，惟依規則不作為通過證據） |
| 前端測試 | `npm --prefix frontend run test` | ✅ 38 files / 373 tests passed（34.20s） |
| 前端 build | `npm --prefix frontend run build` | ✅ built in 13.30s |
| `wails build` | `wails build` | ✅ Built `build/bin/sdlc-workbench.app`（darwin/amd64，38.62s）；build 後 `git status` 仍 clean——Wails binding 與 Go 簽名同步 |

**證據名冊核對**：§2 各表引用的 **120 個不同 Go 測試名**（含 2.11 本次補記的五項；
共 124 次引用）逐一以 `grep -rc '^func <name>(' --include='*_test.go'` 核對，
**全數存在且唯一**——歷輪 hardening 沒有讓任何一列的證據名冊失效。

**方法論邊界（沿用 §4 末段）**：本節驗的是「最終樹上四個 gate 綠＋證據名冊完好」；
Task 0-30 的 60+ 個 mutation 全域交叉矩陣仍未重跑。基線之後各修正票的 mutation 驗證
記錄在各自的 commit 與 review 往返（含 2026-08-21 proc／binding 守門的 7 個探針），
不在本節重複主張。

**§5 實機驗收（A1-A10）維持原狀**：本 session 仍為非互動 agent 環境，該矩陣待 owner 實機。
