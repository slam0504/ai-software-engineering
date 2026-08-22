# Legacy transcript 首次 hydrate — 設計

- 日期：2026-08-22
- 票源：m3b-results.md §11 開票記錄第 2 項（P1，owner 裁決）
- 背景：A8 實機驗收發現——legacy（M3a→M3b migration 建立）的 WSID 首次 hydrate
  時，pre-migration 的舊對話**看不到**。`LoadTurnsBefore` 走 turn index，而 index
  依 §3.5.9 只索引有 WSID 的事件（`applyTurnState` 對 `wsid==""` 直接略過），且
  `readEnvelopeRange` 逐筆濾 WSID——legacy 事件（無 WSID）兩道都被排除。
  `RestoreViews`（provider-keyed）能重放 legacy window，但 frontend 零呼叫端。
- 既有測試契約（`app_invariants_test.go` `TestLegacyJournalWithoutWSIDAttributes`）
  已把「claude 的歷史對話升級後整段消失」列為錯誤——本設計補上實際顯示路徑。

## 1. 目標與非目標

**目標**：legacy-migrated WSID 首次 hydrate（釘選 pane、`LoadTurnsBefore` 尾端
載入）時，顯示 pre-migration 的舊 transcript（view boundary 之後、無 WSID 的
provider 歷史），與 post-migration 的新 turn 正確合併、排序、去重。

**非目標**：
- 不改 `RestoreViews`（保留為既有 Go 測試出口）、不新增 frontend 對它的呼叫。
- 不改 turn index 的 §3.5.9 語意（無 WSID 事件不入 index，維持凍結）。
- legacy 舊歷史不做 turn-level 分頁（它無 turn 邊界）；一次性升級資料，首載全給。

## 2. owner 裁決（兩輪，凍結為設計約束）

1. **接入層：backend `LoadTurnsBefore` 內建**，不在 frontend 合併 RestoreViews。
   理由：backend 已持有 registry／provider／view boundary／replay index，最適合
   統一處理排序、去重、20-turn、view boundary、向上分頁；frontend 合併會產生
   第二套 transcript 資料來源。
2. **識別用明確的持久化標記**，不得用「事件無 WSID 就歸給目前 WSID」推導——同
   provider 多 session 後會把舊歷史接到錯誤的 session。
3. **相容性：一次性 backfill**（不只對新 migration 生效——這張 P1 要修的正是
   「已完成 migration 的使用者看不到舊歷史」）。backfill 採精確比對，見 §4。
4. **NewSession／ResetView 前移 boundary 的同一交易清除 LegacyTranscript 標記**。

## 3. 資料模型變更（wsregistry，schema v2 additive，不升版本）

沿用 `ResumeBackfilled`／`Migrated` 的既有模式（`store.go`：additive bool 欄位、
舊檔無此欄即 false、原子寫入含失敗回滾）。

- `Entry.LegacyTranscript bool`（json `legacy_transcript`）：此 entry 擁有
  pre-migration 的舊 transcript，首次 hydrate 應合併顯示。
- file-level `LegacyTranscriptBackfilled bool`（json `legacy_transcript_backfilled`）：
  §4 的一次性 backfill 是否已完成（與 `ResumeBackfilled` 並列）。

`Migrate`（migrate.go）：產生 legacy entry 時設 `LegacyTranscript=true`（三者皆空
不遷移的既有語意不變）。

## 4. 一次性 backfill（app startup，沿用 `backfillResumeFromLegacy` 附近）

對「本修正之前已遷移」的既有使用者（`Migrated()==true` 但無任何 `LegacyTranscript`
標記）補標記。**以保留的 restore.json 作為 migration 來源快照**（restore.json 仍是
legacy 遷移與升級 backfill 的消費者，見 RestoreViews doc）。

觸發：`!LegacyTranscriptBackfilled`。對每個 provider（claude／codex）的 restore
entry，尋找候選 WSID entry，**五條件同時成立**才算候選：

1. live（未 tombstone）；
2. provider 相同；
3. `ViewStartEventID` **精確等於** restore entry 的 `ViewStartEventID`；
4. 該 boundary 之後**確實存在 legacy window**（provider 相符、event_id > boundary、
   **WorkspaceSessionID==""** 的事件至少一筆）；
5. 每個 provider **恰好一個候選**。

處置：
- **恰一候選** → 標記該 entry `LegacyTranscript=true`。
- **零候選** → 該 provider 無待補（已 NewSession 前移 boundary／已移除／本就無
  legacy transcript），安全略過。
- **多候選** → **不猜、fail loud、marker 不落盤**（回錯，啟動期記 startup warning，
  不阻擋啟動其餘部分；下次啟動可重試）。

所有 entry 標記 + file-level `LegacyTranscriptBackfilled=true` **同一次持久化交易**
寫入（沿用 `MarkResumeBackfilled` 的原子＋回滾模式）；persist 失敗則全部回滾、
marker 不落盤。

## 5. 合併載入（`loadTurnsBefore`，rebuild_orchestrator.go）

僅在**首次 hydrate**（`beforeEventID==""`，尾端視窗）合併 legacy window：

```
if entry.LegacyTranscript && beforeEventID == "" {
    legacy := replayViewWindow(eventsPath, provider, entry.ViewStartEventID)
             |> filter WorkspaceSessionID == ""   // 只取 pre-migration 無 WSID 事件
    out = legacy ++ out    // legacy 較舊、排在 WSID turn 事件之前
}
```

**資料正確性**：
- **去重**：legacy window 只取 `WorkspaceSessionID==""`。`replayViewWindow` 按
  provider 過濾、不看 WSID——若不濾，post-migration 的同 provider WSID 事件會被
  抓到、與 turn index 結果重複。濾掉有 WSID 的即可，因為那些已由 turn index 涵蓋。
- **排序**：legacy 事件 event_id 遞增、且全部早於任何 post-migration turn（migration
  之後才產生帶 WSID 的事件），故 `legacy ++ out` 維持全域 event 順序。
- **view boundary**：legacy window 用 `entry.ViewStartEventID` 過濾（boundary 之後）；
  與 WSID turn 的 boundary 過濾（`loadTurnsBefore` 既有 `viewStart` 邏輯）一致。
- **20-turn 契約**：20 指 WSID turn record 數（既有語意不變）；legacy 是額外的一次性
  前綴，不佔 20 個 turn 額度、不分頁。
- **cursor 分頁**：`beforeEventID != ""` 時**不回** legacy（legacy 只在首載出現一次）；
  cursor 到 WSID 最舊 turn 後回空，不跨進 legacy。前端 `loadOlder` 的 event_id 去重
  仍是第二道防線。

## 6. 標記清除（前移 boundary 的同一交易）

`NewSession`／`ResetView` 前移 view boundary（`ResetView(wsid, 高水位)`）時，同一次
持久化交易清除 `entry.LegacyTranscript=false`——開新對話＝建立新 view 世代，舊
legacy 歷史不再屬於目前 view。實作：`ResetView` 一併清標記（單一原子寫入），
避免「boundary 前移了但標記還在」導致 legacy 歷史在新世代復活。

## 7. 元件邊界與資料流

```
startup:
  loadSessionRegistry → Migrate（新遷移設 LegacyTranscript）
                      → backfillResumeFromLegacy（既有）
                      → backfillLegacyTranscript（§4，新增；restore.json 快照精確比對）

hydrate（首次）:
  frontend pin() → LoadTurnsBefore(wsid,"",20)
                 → loadTurnsBefore：WSID turn index 事件
                   ＋（若 entry.LegacyTranscript）legacy window（無 WSID、boundary 後）前綴

前移 boundary:
  NewSession/ResetView → ResetView(wsid, 高水位) 同交易清 LegacyTranscript
```

frontend 零改動（`pin()` 已呼叫 `LoadTurnsBefore("",20)`，自動受益）。

## 8. 測試策略（TDD）

**wsregistry（unit）**：
- `Migrate` 對 legacy entry 設 `LegacyTranscript=true`；三者皆空的 provider 不建、
  不設標記。
- schema：舊檔（無 `legacy_transcript`／`legacy_transcript_backfilled`）載入預設
  false；round-trip 保留。
- `ResetView` 清除 `LegacyTranscript`（同一寫入）。

**backfill（app，in-process）**：
- 五條件精確比對：ViewStart 精確相等（差一字元不算候選）、boundary 後有無 WSID
  legacy window 才算、tombstone 不算、provider 對應。
- 零候選 → marker 落盤、entry 不動。
- 多候選 → fail loud、marker **不**落盤、entry 不動、可重試。
- 冪等：`LegacyTranscriptBackfilled==true` 後不再跑。
- 同交易原子性：persist 失敗 → entry 標記與 marker 全回滾。

**loadTurnsBefore（app）**：
- legacy WSID 首載：回「legacy window（無 WSID、boundary 後）＋ WSID turn」，順序
  正確、無重複。
- 去重：post-migration 同 provider 的 WSID 事件不因 legacy window 重複出現。
- 非 legacy WSID（`LegacyTranscript==false`）：首載不含任何無 WSID 事件（反向）。
- cursor 分頁（`beforeEventID!=""`）不回 legacy。
- view boundary：boundary 之後才回；前移 boundary（清標記）後首載不再含 legacy。
- 20-turn：legacy 不佔 turn 額度。

**既有契約不破**：
- `TestLegacyJournalWithoutWSIDAttributes`（RestoreViews provider-keyed）維持綠。
- turn index §3.5.9（無 WSID 不入 index）不變。

**端對端（A8 補跑，實機或 fixture）**：legacy fixture 首次 hydrate 舊 transcript
可見；同 provider 多 session 不誤接（owner 否決推導的核心風險）。

## 9. 待驗證假設

- **restore.json 在既有升級使用者仍存在且未被清空**：backfill 以它為來源快照。
  RestoreViews doc 明載 restore.json 現有兩個消費者（legacy 遷移、升級 backfill），
  應仍在；實作前以 fixture 確認「已 migrated 的 workspace 其 restore.json 仍保有
  provider entry」。若某些升級路徑已清空 restore.json，該使用者落入「零候選」→
  marker 落盤、舊歷史仍不可見（可接受的降級，非資料損毀）。
- **legacy window 的量**：受 `ViewStartEventID` boundary 限制（通常是最後一次開新
  對話後的一段），非整個 provider 歷史；首載一次全給可接受。若某使用者 boundary
  極早導致 window 巨大，首載會偏重——列為已知邊界，不在本票處理分頁。
- **event 順序假設**：所有 legacy（無 WSID）事件 event_id 皆早於任何 post-migration
  （有 WSID）事件。migration 是一次性、在所有 legacy 事件之後才開始寫 WSID 事件，
  故成立；實作以 event_id 遞增排序為最終保證，不純依賴此假設。

## 10. 風險與相容性

- schema additive、不升版本：舊檔可讀、新欄位預設 false，向後相容。
- backfill 多候選 fail loud：寧可暫不顯示（可重試）也不誤接歷史——對齊 owner
  否決推導的原則與本 repo 的 fail-loud 慣例。
- 前移 boundary 清標記：避免舊世代 legacy 歷史在 NewSession 後復活。
