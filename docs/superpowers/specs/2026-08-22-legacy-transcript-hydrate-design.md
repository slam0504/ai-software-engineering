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
- legacy 舊歷史不做 turn-level 分頁（它無 turn 邊界）；一次性升級資料，抵達最舊
  WSID turn 頁時一次全給。

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

`Migrate`（migrate.go）：**只依 App 掃描得到的 `HasLegacyTranscript` 設 flag**，
不是「非空 entry 一律設 true」（reviewer 2026-08-22 P2）。`LegacyEntry` 增加
`HasLegacyTranscript bool` 欄位，由 App 的 `legacyEntries()` 填入。`Migrate` 對
每個遷移的 entry 設 `LegacyTranscript = le.HasLegacyTranscript`。

**`legacyEntries()` 改用 error-returning `scanLegacyWindow`**（reviewer 2026-08-22
P1）：現行它以會吞 I/O 錯誤的 `replayViewWindow` 判斷 window（app.go:1282）。
**transcript-only 使用者**（只有舊對話、無 resume／task）最脆弱——掃描暫時失敗會
讓該 provider 被判成「window 空、三者皆空」而**跳過遷移**，接著 `Migrate` 寫
`migrated=true` ＋空 entries，下次啟動不再 migration、也沒有 entry 可 backfill，
舊歷史**永久遺失**。修正：`legacyEntries()` 用 `scanLegacyWindow`，掃描失敗**回 error**；
呼叫端（`loadSessionRegistry`）據此**不呼叫 Migrate、marker 不落盤**，下次啟動可重試。
`HasLegacyTranscript = len(scanLegacyWindow(...)) > 0`（掃描成功時）。

**理由**：`LegacyTranscript=true` 的語意是「此 entry 確實擁有可顯示的舊 transcript」。
migration 允許只有 resume identity／task label、**沒有 legacy window** 的 entry
（migrate.go:67 只排除三者皆空）——那種 entry 不該設 flag，否則首載會對空 window
觸發合併邏輯、語意不符。reverse 測試：resume-only／task-only entry 的
`LegacyTranscript==false`。

**語意例外（closeout 2026-08-23 對齊實作）**：`ViewStartEventID==""` 的 entry
其 flag 可能為 true 但**不可顯示**——hydrate 端依 §5 guard 對空 boundary 一律
不前綴（無可信 boundary＝無可信比對證據）。此時 flag 是惰性的，不觸發任何載入
行為，也**不**因此被清除（§6a：清除前提是「實際掃描成功且零筆」，空 boundary
根本沒有掃描）。

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

**guard（integration review 2026-08-22 Critical）**：restore entry 的
`ViewStartEventID` 為空字串時，該 provider **直接略過**條件 3 的比對（等同零
候選，不進入掃描與判定）——空字串在這個系統代表「沒有 boundary」，不是一個
可信的比對值：`CreateSession` 建 entry 時不設 `ViewStartEventID`（唯一寫入者
是 `ResetView`），而首次啟動時若 `events.jsonl` 為空，restore.json 快照會被
`freshEntries(auditHighWatermark)` 初始化成 `""`（`restore.go:56、137-141`）。
放行空字串比對，會把「該 provider 目前沒有可信 boundary」誤判成「找到了」：
同快照為 `""` 的多個 entry 會被誤判成多候選，導致 marker 永遠卡在未落盤、每
次啟動都重新 fail loud（不會自癒）；若當下恰好只剩一筆 `ViewStartEventID=""`
的 entry，則會把整段 pre-migration 歷史誤標給它——owner 已否決的失效模式。
略過＝零候選是本節已定義的降級語意：無可信比對證據就不猜，該 provider 這次
拿不到 legacy 標記，其他 provider 與 marker 落盤不受影響。

處置：
- **恰一候選** → 標記該 entry `LegacyTranscript=true`。
- **零候選** → 該 provider 無待補（已 NewSession 前移 boundary／已移除／本就無
  legacy transcript），安全略過。
- **多候選** → **不猜、fail loud、marker 不落盤**（回錯，啟動期記 startup blocker
  ——僅影響訊息排序優先度、**不阻擋啟動其餘部分**（closeout 2026-08-23 措辭對齊
  實作：沿用 `backfillResumeFromLegacy` 失敗的 `noteStartupBlocker` 前例）；
  下次啟動可重試）。

**失敗軌跡（closeout 2026-08-23 新增；2026-08-24 owner review 補統一稽核）**：
backfill 失敗（多候選／scan error／persist error）的錯誤分支依序：
1. 先經 `a.noteRegistryUncertainErr("legacy_transcript_backfill", "", err)`——既有
   契約（app.go:734）要求任何 registry 寫入回 `ErrRegistryUncertain` 都走統一稽核，
   `BackfillLegacyTranscript` 是 registry 寫入、不得例外；非 uncertain 錯誤原樣通過。
2. 再寫具名稽核事件 `legacy_transcript_backfill_failed`（`{"error": ...}`，對齊同檔
   `resume_backfill_failed` 慣例）——startup 訊息是一次性的，audit.jsonl 才是事後
   診斷「為什麼這次啟動沒補標記」的持久軌跡。
3. 最後記 startup blocker。

成功路徑**不**另發 audit：結果已可由 registry 的 marker 與 entry flag 直接觀測。
uncertain 覆蓋測試的「呼叫點數」說明與「具體 Store、尚無跨 package 注入」的已知
覆蓋缺口註記一併更新。

**I/O 錯誤不得誤判成零候選**（reviewer 2026-08-22 P1）：條件 4 的 legacy window
掃描**不得**用 `replayViewWindow`——它開檔失敗回 nil、且不檢查 `Scanner.Err()`
（restore.go:167），暫時性讀取錯誤會被固化成「零候選 → 落 marker → 永不重試」。
凍結一個 **error-returning legacy scanner**（見 §5a）：開檔失敗／scan 錯誤 → 回
error、**不改任何 entry、不落 marker、fail loud**；只有掃描成功、確定零候選才
落 marker。

所有 entry 標記 + file-level `LegacyTranscriptBackfilled=true` **同一次持久化交易**
寫入（沿用 `MarkResumeBackfilled` 的原子＋回滾模式）；persist 失敗則全部回滾、
marker 不落盤。

## 5. 合併載入（`loadTurnsBefore`，rebuild_orchestrator.go）

**legacy window 只在「最舊 WSID turn 所在的那一頁」前綴**（reviewer 2026-08-22
P1）——不論那頁是首載還是向上分頁。原設計「首載就前綴」有致命 bug：前端以
timeline 第一筆（會是 legacy event）當下一頁 cursor（session.ts:450），replay index
查不到 legacy cursor 會回空（index.go 的 `cut<=0`），導致比 20 更舊的 post-migration
turn 永遠載不到。

規則（`entry.LegacyTranscript==true` 時）：

```
recs, hasOlder := replayIndex.TurnsBefore(wsid, beforeEventID, n)   // hasOlder：此頁之後還有更舊 WSID turn 嗎
if beforeEventID != "" && len(recs) == 0 {
    return nil, nil          // cursor 找不到（含 legacy id）或有效但已最舊——回空、不前綴
}                            // legacy 只在最舊 turn 頁前綴一次，此處回空讓前端停
out := envelopes(recs) ++ (若首載) open-turn tail
if !hasOlder && entry.ViewStartEventID != "" {   // 這一頁是最舊 WSID turn 頁，且有可信 boundary → 前綴 legacy
    legacy, scanned, err := scanLegacyWindow(eventsPath, provider, entry.ViewStartEventID)  // §5a，只取無 WSID
    if err != nil { return nil, err }         // I/O 錯誤 fail loud，不靜默少給
    if len(legacy) > 0 {
        out = legacy ++ out
    } else if scanned {                       // §6a：成功掃描確定零筆 → 清旗標（下次首載不再掃）
        err := registry.ClearLegacyTranscript(wsid)   // 哨兵（不存在／tombstone）良性跳過
        if err != nil && 非哨兵 { return nil, uncertainAudit(err) }  // persist 失敗依 §6a 三語意表，一律回錯
    }                                         // scanned==false（NotExist／TOCTOU）→ 不清、不前綴
}
```

`if beforeEventID != "" && len(recs) == 0 { return nil, nil }` 是正式演算法的一部分
（reviewer 2026-08-22 P2）：legacy cursor（`cut<=0`）與「有效但已是最舊 turn」的 cursor
都是 `recs==[]、hasOlder==false`，若不早退就會再掃描前綴整段 legacy、與「回空不前綴」
矛盾。正常序列中 legacy 已在**前一頁**（最舊 turn 頁）回過，故此處回空正確；由 §8
「第三頁 cursor=legacy id → 回空」測試守住。

- `beforeEventID==""` 且 `hasOlder==false`（WSID turn 不滿 n）→ 首載即前綴 legacy。
- `beforeEventID==""` 且 `hasOlder==true`（WSID turn 超過 n）→ 首載**不**前綴；正常
  分頁到最舊 turn 頁時（`hasOlder==false`）才前綴。
- `beforeEventID` 是 legacy event id → `TurnsBefore` `cut<=0` 回空、`hasOlder==false`
  但**該頁 recs 為空且 cursor 不是 WSID turn** → 回空、**不**再前綴（legacy 已於
  最舊 turn 頁給過）。判準：只有「cursor=="" 或 cursor 命中某 WSID turn」的頁才可能
  前綴 legacy；cursor 非 WSID turn（含 legacy id）一律回空、不前綴。

**空 ViewStart 不前綴（integration review 2026-08-23）**：`entry.ViewStartEventID==""`
時，即使 `LegacyTranscript==true` 也不得前綴 legacy。`Migrate` 可能建出這種
entry——首啟時 events.jsonl 為空、restore.json 快照的 `ViewStartEventID` 是空字串、
使用者從未 `ResetView`，但 `ResumeSessionID`／`TaskID` 非空放行 `Migrate` 判準。此時
`scanLegacyWindow(viewStart="")` 不做 boundary 過濾（§5a：`viewStart != "" && ...`
才過濾），會把該 provider **整個歷史**一次前綴進最舊頁——違反 m3b §3.2.5「不得把
該 provider 全部歷史丟入 legacy session」。guard 比照 §4 backfill 的空 boundary
前例（`backfillLegacyTranscript`：`re.ViewStartEventID == ""` 時直接跳過該
provider，不猜）：無可信 boundary 來源＝無可信比對證據，寧可少顯示也不前綴。

**replay index 的 hasOlder**：`TurnsBefore` 增加回傳「此頁之後是否還有更舊 turn」
（實作可由 `len(all[:cut]) > n` 或 `len(all) > n` 判定；plan 定確切簽章）。這是本
設計對 index 的唯一介面新增。

**資料正確性**：
- **去重**：legacy window 只取 `WorkspaceSessionID==""`（§5a）。post-migration 的
  同 provider WSID 事件已由 turn index 涵蓋，濾掉避免重複。
- **排序（正式保證，非假設）**：合併後**依 event_id 遞增排序**作為最終保證。legacy
  事件 event_id 皆早於任何 post-migration turn（migration 一次性、之後才寫 WSID
  事件），`legacy ++ out` 已維持順序；排序是防禦性最終保證，不純依賴此時序假設。
- **view boundary**：legacy window 用 `entry.ViewStartEventID` 過濾（boundary 之後），
  與 WSID turn 的既有 `viewStart` 過濾一致。
- **20-turn 契約**：20 指 WSID turn record 數（既有語意不變）；legacy 是最舊 turn 頁的
  一次性前綴，不佔 turn 額度、自身不做 turn-level 分頁。
- **前端停止條件**：最舊 turn 頁前綴 legacy 後，前端下次 loadOlder 以 legacy id 當
  cursor → 回空 → 停（session.ts loadOlder 對空結果 return）。

## 5a. error-returning legacy scanner（`scanLegacyWindow`）

新增 `scanLegacyWindow(eventsPath, provider, viewStart) ([]contract.Envelope, scanned bool, error)`
（closeout 2026-08-23 簽章擴充：原 `([]contract.Envelope, error)`），
供**三條路徑共用**——§3 migration 的 `legacyEntries()`、§4 backfill、§5 合併載入
（三處都必須正確處理 I/O 錯誤；reviewer 2026-08-22 P1）：

- 語意同 `replayViewWindow`（provider 相符、event_id > viewStart、排除 workspace／
  spec_assist scope），**額外只保留 `WorkspaceSessionID==""`**（pre-migration）。
- 開檔失敗（非 `os.IsNotExist`）→ 回 error。檔案不存在 → 回 `(nil, false, nil)`（全新
  workspace，非錯誤）。
- **`scanned` 語意（closeout 2026-08-23；2026-08-24 owner 解凍擴大消費）**：`true`＝
  檔案存在、開檔成功、完整掃描到 EOF 且 `Scanner.Err()==nil`——「零筆」與「檔案
  不存在」自此可區分。三個呼叫端全數消費 `scanned`（原「NotExist 行為不變」的凍結
  已由 owner 2026-08-24 解凍——degraded startup（events sink 開檔失敗、events.jsonl
  不存在）下原行為會把兩個一次性 marker 燒掉，影響未遷移與已遷移使用者）：
  - `legacyEntries`：`scanned==false` → **回錯、不呼叫 Migrate**（migrated marker 與
    entries 皆不落盤，下次啟動可重試）。正常啟動不受影響：sink 先於
    loadSessionRegistry 建立 events.jsonl，全新 workspace 掃到的是存在的空檔、
    `scanned==true`。
  - `backfillLegacyTranscript`：boundary 可信（非空）但 `scanned==false` → **回錯、
    不落 marker**（空 boundary 仍先被 §4 guard 以零候選語意跳過，順序不變）。
  - `loadTurnsBefore`（§6a 清旗標）：NotExist 不得視為「成功掃描零筆」而清旗標——
    缺檔可能是暫時狀態，清了就不可逆。
- 掃描結束檢查 `Scanner.Err()`，非 nil → 回 error（修 restore.go:167 既有缺口的
  同類問題；`replayViewWindow` 本身保留不動，僅 RestoreViews 用）。
- malformed 單行跳過不中斷（同既有語意）。

## 6. 標記清除（前移 boundary 的同一交易）

`NewSession`／`ResetView` 前移 view boundary（`ResetView(wsid, 高水位)`）時，同一次
持久化交易清除 `entry.LegacyTranscript=false`——開新對話＝建立新 view 世代，舊
legacy 歷史不再屬於目前 view。實作：`ResetView` 一併清標記（單一原子寫入），
避免「boundary 前移了但標記還在」導致 legacy 歷史在新世代復活。

## 6a. 空 window 首載清旗標（closeout 2026-08-23，owner 裁決語意）

**問題**：`LegacyTranscript=true` 但 boundary 後已無無 WSID 事件（例如使用者手動清過
events.jsonl）時，每次首載（`hasOlder==false` 的頁）都會對 events.jsonl 做一次完整
掃描才發現零筆——這是系統中唯一會**重複發生**的全檔掃描（integration review
2026-08-23 M2）。

**清除條件（四分支，owner 凍結）**——只在 §5 合併分支、`scanLegacyWindow` 回傳後判定：

| 情況 | 處置 |
|---|---|
| `scanned==true` 且零筆（檔案開啟成功、完整掃描、`Scanner.Err()==nil`） | 清除 `LegacyTranscript`（單向、僅清旗標、**不動 boundary**），下次首載不再掃描 |
| `ViewStartEventID==""`（§5 guard 擋下、未實際掃描） | **不清除**——flag 維持惰性（§3 語意例外） |
| NotExist（`scanned==false`） | **不清除**——缺檔不等於成功掃描零筆，可能是暫時狀態。（實作註記：`loadTurnsBefore` 開頭自己的 `os.Open` 對 NotExist 已早退、通常到不了合併分支——此格的主路徑由早退覆蓋；分支內的 `scanned` 條件擋的是「早退 open 與 legacy 掃描之間檔案被移除」的 TOCTOU 窗口，無確定性測試可打紅，凍結保留） |
| open／scan error | **不清除**、`loadTurnsBefore` 回錯（既有 §5 語意），下次可重試 |

**寫入口**：新增 `Store.ClearLegacyTranscript(wsid) error`（sessionRegistry interface
一併擴充）——mutate closure 內僅設 `LegacyTranscript=false`、沿用 per-WSID durable
writer 慣例（`ErrEntryNotFound`／`ErrTombstoned` 哨兵，呼叫端視為良性跳過）；
flag 已為 false 時冪等跳過、不落盤（沿用 `SetLayout` 冪等比對前例）。
新 mutator 依慣例登記進 uncertain latch 的寫入拒絕枚舉表（fsync_test.go）。

**persist 失敗——三種語意（owner review 2026-08-24 校正，對齊 `persistOrRollback`
既有契約 store.go:262-275，不得籠統宣稱「旗標維持 true」）**：

| 失敗點 | 語意 |
|---|---|
| 步驟 1-3（write／file-sync／rename 前）失敗 | 回滾——記憶體與磁碟旗標皆維持 true，回錯，process 內可重試（下次首載再清） |
| store 已 latched（先前寫入已 uncertain） | 本次變更回滾（未觸碰檔案系統），回 `ErrRegistryUncertain`，需重啟 |
| 本次 Clear 的步驟 4（directory-sync）失敗 | **不回滾**——rename 已成功，記憶體停在 false、磁碟很可能也是 false 但 process 內無法確認；latch、回 `ErrRegistryUncertain`、需重啟。此情形**不可宣稱旗標仍為 true** |

三種情形 `loadTurnsBefore` 一律**回錯**（owner 裁決 fail loud）。取捨：這把一個
「純優化」的寫入失敗升級成該頁載入失敗——接受，因為 registry 寫入失敗（尤其
uncertain latch）代表整個 registry 已不可寫，掩蓋它只會讓使用者更晚發現。

**併發**：清除發生在讀路徑（`loadTurnsBefore`）上，是刻意的窄寫入——Store mutex
序列化所有 registry 寫入；`ResetView` 與本清除都只把 flag 單向清成 false，
無競態方向風險。

## 7. 元件邊界與資料流

```
startup:
  loadSessionRegistry → legacyEntries()（scanLegacyWindow，掃描失敗回錯→不 Migrate、marker 不落盤）
                      → Migrate（依 HasLegacyTranscript 設 LegacyTranscript）
                      → backfillResumeFromLegacy（既有）
                      → backfillLegacyTranscript（§4，新增；restore.json 快照精確比對）

hydrate（首次＋向上分頁）:
  frontend pin()/loadOlder → LoadTurnsBefore(wsid, cursor, 20)
                 → loadTurnsBefore：WSID turn index 事件（cursor 分頁）
                   ＋（若 entry.LegacyTranscript 且 ViewStartEventID!="" 且此頁為
                      最舊 turn 頁 hasOlder==false）
                     legacy window（無 WSID、boundary 後）前綴、依 event_id 排序

前移 boundary:
  NewSession/ResetView → ResetView(wsid, 高水位) 同交易清 LegacyTranscript
```

frontend 零改動（`pin()` 已呼叫 `LoadTurnsBefore("",20)`，自動受益）。

## 8. 測試策略（TDD）

**migration 掃描（app，in-process）**：
- transcript-only fixture（有 legacy window、無 resume／task）：正常遷移、
  `LegacyTranscript==true`。
- transcript-only ＋ scanLegacyWindow open error／scan error → `legacyEntries()`
  回錯、**不呼叫 Migrate、migrated marker 與 entries 皆不落盤**；重試成功後正常遷移。
  （守住「暫時性 I/O 錯誤不得把 transcript-only 使用者永久遷成空 entries」。）

**wsregistry（unit）**：
- `Migrate` 依 `LegacyEntry.HasLegacyTranscript` 設 flag：有 legacy window → true；
  **resume-only／task-only（無 window）→ false**（反向）；三者皆空不建。
- schema：舊檔（無 `legacy_transcript`／`legacy_transcript_backfilled`）載入預設
  false；round-trip 保留。
- `ResetView` 清除 `LegacyTranscript`（同一寫入）。

**backfill（app，in-process）**：
- 五條件精確比對：ViewStart 精確相等（差一字元不算候選）、boundary 後有無 WSID
  legacy window 才算、tombstone 不算、provider 對應。
- 零候選（掃描成功）→ marker 落盤、entry 不動。
- 多候選 → fail loud、marker **不**落盤、entry 不動、可重試。
- **scanner I/O 錯誤（open error／scan error）→ fail loud、marker 不落盤、entry
  不動、可重試**（不得誤判成零候選）。
- 冪等：`LegacyTranscriptBackfilled==true` 後不再跑。
- 同交易原子性：persist 失敗 → entry 標記與 marker 全回滾。

**loadTurnsBefore（app）**：
- legacy WSID 首載（WSID turn ≤ 20）：回「legacy window（無 WSID、boundary 後）＋
  WSID turn」，依 event_id 排序、無重複。
- **跨頁分頁（legacy + 25 turns，reviewer 2026-08-22 P1）**：首載回最新 20 turn
  （**不**含 legacy，因 hasOlder）；第二頁 cursor=turn6 → 回 turns 1–5 ＋ legacy
  前綴（最舊 turn 頁）；第三頁 cursor=legacy id → 回空、停。驗「turns 1–5 不會遺失」。
- 去重：post-migration 同 provider 的 WSID 事件不因 legacy window 重複出現。
- 非 legacy WSID（`LegacyTranscript==false`）：任何頁都不含無 WSID 事件（反向）。
- **空 ViewStart（integration review 2026-08-23 I1）**：`LegacyTranscript==true` 但
  `ViewStartEventID==""` 時，任何頁都不得前綴 legacy（無可信 boundary）。
- **首載即前綴（integration review 2026-08-23 I2，mutation 守門）**：legacy 使用者
  WSID turn 數 < 20（首載本身就是最舊 turn 頁）時，`beforeEventID==""` 的首載必須
  直接前綴 legacy——守住「合併分支條件誤加 `beforeEventID != ""`」這類 mutation。
- scanner I/O 錯誤 → loadTurnsBefore 回 error（不靜默少給 legacy）。
- view boundary：boundary 之後才回；前移 boundary（清標記）後不再含 legacy。
- 20-turn：legacy 不佔 turn 額度。

**§6a 空 window 清旗標（closeout 2026-08-23）**：
- 成功清除：flag=true、boundary 非空、window 空 → 首載成功回頁、flag 清為 false
  且持久化；**第二次首載不再掃描**——探針：清除後往 events.jsonl 追加一筆
  boundary 之後的無 WSID 事件，第二次首載不得含它（清除失效或分支忽略 flag 的
  mutation 會把它前綴出來，確定性打紅。刻意不用檔案破壞注入：NotExist 會被
  loadTurnsBefore 自己的早退擋掉、目錄／權限注入則依賴 readEnvelopeRange 現行
  吞錯行為，replay reliability 票修掉後會誤紅）。
- NotExist 不清：events.jsonl 不存在 → 首載成功（無前綴）、flag 仍 true（磁碟）。
- scan error 不清：回錯、flag 仍 true。
- persist error 不清：registry 目錄唯讀注入 → 首載回錯、flag 仍 true（磁碟）、
  修復後重試成功並清除。
- 空 ViewStart 不清：既有 I1 測試擴斷言——guard 擋下後 flag 仍 true。
- `scanLegacyWindow` 簽章：NotExist → `scanned==false`；空檔／過濾後零筆 →
  `scanned==true`（unit）。
- `ClearLegacyTranscript`（wsregistry unit）：清除持久化 round-trip、
  `ErrEntryNotFound`／`ErrTombstoned` 哨兵、uncertain latch 枚舉表拒絕；
  注入一律用既有 step hook（`recordSteps`／`failAt`），**不用 chmod**（root
  可繞過權限造成假綠）：冪等＝斷言零 persist step；一般 persist 失敗＝
  `failAt(stepWrite)` 斷言回錯＋回滾；directory-sync 失敗＝
  `failAt(stepDirSync)` 斷言**不回滾**（記憶體停在 false）＋latch＋後續寫入
  一律拒絕（§6a 三種語意各有測試）。

**§4 失敗軌跡（closeout 2026-08-23）**：
- backfill 多候選 fixture → audit.jsonl 出現 `legacy_transcript_backfill_failed`
  且帶 error 內容；成功路徑不發該事件（反向）。

**degraded startup 單向 marker 防護（owner 2026-08-24 解凍新增）**：
- 未遷移 fixture＋events.jsonl 不存在（模擬 sink 開檔失敗的降級啟動）→
  `restoreSessions` 回錯、`Migrated()==false`、entries 空（migrated marker 不被
  燒掉）；恢復 events.jsonl 後重試正常遷移。
- 已遷移 fixture（boundary 非空）＋events.jsonl 不存在 → backfill 回錯、
  `LegacyTranscriptBackfilled()==false`（marker 不被燒掉）；恢復後重試正常標記。
- 反向（既有語意不變）：檔案存在且成功掃描零筆 → 照常落 marker（既有零候選測試
  持續守住）。

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
  對話後的一段），非整個 provider 歷史；抵達最舊 WSID turn 頁時一次全給可接受。若某使用者 boundary
  極早導致 window 巨大，最舊 WSID turn 頁會偏重——列為已知邊界，不在本票處理分頁。
- （event 順序不再列為待驗證假設——§5 已把「合併後依 event_id 遞增排序」定為正式
  演算法保證，不依賴時序假設。）

## 10. 風險與相容性

- schema additive、不升版本：舊檔可讀、新欄位預設 false，向後相容。
- backfill 多候選 fail loud：寧可暫不顯示（可重試）也不誤接歷史——對齊 owner
  否決推導的原則與本 repo 的 fail-loud 慣例。
- 前移 boundary 清標記：避免舊世代 legacy 歷史在 NewSession 後復活。
