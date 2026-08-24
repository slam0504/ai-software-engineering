# auditHighWatermark 空高水位污染寫入路徑 設計

Owner 開票（2026-08-24，P1）：`auditHighWatermark` 讀取失敗時靜默回 `""`，且被
寫入路徑直接消費——空 boundary 被寫入 durable registry。Owner 凍結方向：**兩個
caller 契約分開**——ResetView 寫入路徑遇 open／scan error 必須停止且不得改
boundary；startup snapshot 路徑是否允許降級獨立裁決。

**修訂記錄**：
- rev4（2026-08-24，owner review 2 P2）：§1 表格同步 rev3 校正（RestoreViews 改標
  「無 production caller、僅直接呼叫 binding 時的潛在行為」）；§4 拆開「watermark
  不可用」與「snapshot 被降級寫入」——audit 改名 `restore_watermark_unavailable`
  （陳述確定事實）、fallback 消費三條件凍結（不存在／malformed 持久化、缺 provider
  僅記憶體、完整存在不消費）、§5 補完整 restore.json 反向案例＋持久降級 fixture
  前提明訂。
- rev3（2026-08-24，spec gate 3 P1／3 P2）：修復路徑更正——ResetView 寫 wsregistry
  非 restore.json（gate 實測），唯一修復＝手動刪除／編輯 restore.json；app 層注入
  改「events.jsonl 建成目錄」打 scan error 分支（ENOTDIR 僅 unit 層）；「不阻擋
  啟動」限定 migration marker 已設穩態＋複合失效形狀明文；RestoreViews 無 production
  caller、影響改寫為遷移視窗放寬；§3 補「session 已收尾、顯示暫時不一致」如實語意
  ＋watermark 錯誤不經 noteRegistryUncertainErr；補齊路徑不落盤的語意區分。
- rev2（2026-08-24，owner review 2 P1／2 P2＋§4 裁決）：§4 採 (a) 降級＋完整軌跡
  凍結；startup NotExist 改判異常（sink O_CREATE 先行、全新 workspace 是 scanned=true
  空檔）、降級但不得靜默；「一次性」風險描述撤回——freshEntries("") 立即持久化、
  空 boundary 持續至明確修復，如實記載影響（hydrate/backfill 有 guard、RestoreViews
  仍重放全歷史）；ResetView audit payload 凍結四欄位＋NotExist 合成錯誤內容＋
  FinishReset 收束語意；open error 測試改 ENOTDIR 確定性注入（父路徑為普通檔案）。
- rev1：初版。

## 1. 問題與現狀

`auditHighWatermark(eventsPath) string`（restore.go:137-155）：

- open 失敗（**含 NotExist 與其他錯誤不分**）→ 回 `""`。
- `Scanner.Err()` 被忽略 → 截讀時回「讀到一半為止的最後一筆」（偏舊）。
- malformed 行跳過（既有慣例，不改）。

Production caller 恰兩處（replay reliability spec §5 盤點、本票複核）：

| Caller | 消費方式 | 失敗時的實際後果 |
|---|---|---|
| `openRestoreStore`（app.go:2242，啟動快照） | `freshEntries(hw)`／缺 provider 補齊 | restore.json 不存在／malformed 時快照 view_start 被持久化為 `""`（缺 provider 補齊僅改記憶體）；殘餘風險＝未遷移狀態的 `legacyEntries` 掃描範圍放寬（§4）；空 boundary 的 hydrate／backfill 效應已由既有 guard 擋住（closeout 票）。`RestoreViews` 為無 production caller 的 legacy binding，僅直接呼叫該 binding 時才有重放全歷史的潛在行為 |
| `ResetView`（app.go:9084，「開新對話」高水位） | `a.wsReg.ResetView(wsid, hw)` **durable 寫入** | boundary 被寫成 `""`（open 失敗）或偏舊值（截讀）——「開新對話」沒有切乾淨，且是**持久化**的使用者可見歷史錯誤 |

測試 caller 四處（app_test.go:206／:1214、app_restore_dormant_test.go:59、
app_legacy_transcript_test.go:389）——簽章變更需一併適配。

## 2. 簽章擴充（沿用 scanLegacyWindow 的 scanned 慣例）

`auditHighWatermark(eventsPath) (last string, scanned bool, err error)`：

| 情況 | 回傳 |
|---|---|
| 檔案不存在（NotExist） | `("", false, nil)`——函式層以 `scanned=false` 回報「未能確認」。**兩個 production caller 都不得把它當正常**（owner review rev1 P1）：sink 以 `O_CREATE` 先於 watermark 建立 events.jsonl（app.go:2207 → :2242），全新 workspace 掃到的是**存在的空檔**（`scanned=true`），NotExist 只可能是 sink 建立失敗或外部刪除 |
| open 失敗（非 NotExist） | `("", false, err)` |
| `Scanner.Err()` 非 nil | `("", false, err)`——**不再忽略**（截讀的偏舊值對寫入路徑是持久化污染，見 §3；不回部分值，避免呼叫端誤用） |
| 完整掃描 | `(last, true, nil)`（空檔回 `("", true, nil)`——存在且確定為空，`""` 是正確高水位） |

malformed 行跳過不變。

## 3. ResetView 寫入路徑契約（owner 已凍結）

在呼叫 `a.wsReg.ResetView` **之前**判定：

- `err != nil`（open／scan error）→ **停止：不呼叫 ResetView、boundary 與
  LegacyTranscript 旗標皆不得變**；錯誤掛上既有 `rerr` 路徑（該區塊已有錯誤協定
  ——哨兵良性跳過＋`rerr` 傳播，app.go:9081-9094），「開新對話」該次失敗、可重試。
- `scanned == false && err == nil`（NotExist）→ **同樣停止、fail loud**：ResetView
  時 events.jsonl 必然存在（sink `O_CREATE` 先於啟動完成，closeout final review
  已獨立核實），缺檔＝異常（外部刪除）。
- `scanned == true` → 照常 `ResetView(wsid, last)`（含空檔 `""` 的情況——存在且
  確定為空時 `""` 是正確高水位，此格與現行為一致；區別在「確定為空」與「讀不到」
  自此可分）。

**具名 audit payload 凍結（owner review rev1 P2）**：兩種停止情況都發
`reset_view_watermark_failed`，同一筆至少含 `provider`、`wsid`、`path`、`error`
四欄位（對齊 `reset_view_skipped` 慣例但欄位更完整——只有事件名稱無法追查對象）；
NotExist 情況的 `error` 合成可操作內容（例如「events.jsonl 不存在——sink 建立失敗
或遭外部刪除，請確認 workspace 狀態後重試」），不得為空。

**lifecycle 收束（owner 裁決；spec gate rev2 P2 措辭如實化）**：兩種停止情況回錯、
不寫 registry，但**最後仍須 FinishReset 讓 lifecycle 回到 idle**（gate 核實：
app.go:9096 的 FinishReset 在 rerr 判斷前無條件執行，結構既有）。如實語意：
**停止的只有 durable boundary 寫入；session 本身已收尾**（teardown 與
FinishEndSessionIntoReset 在 watermark 判定點之前已完成）——UI 因 SettingsBar
失敗不 reset 而仍顯示舊 transcript，**顯示與實際狀態暫時不一致**；重試「開新對話」
會補上 boundary。這不是無副作用的原子失敗，實作不得試圖回滾 session 收尾。

**錯誤不得經 `noteRegistryUncertainErr`**（gate 建議凍結）：watermark 失敗是讀檔
錯誤、不是 registry 寫入結果不確定，誤掛會誤標語意；直接以具名 audit＋`rerr` 處理。

## 4. startup snapshot 路徑（owner 裁決 2026-08-24：採 (a) 維持啟動降級＋完整診斷軌跡）

`openRestoreStore` 的高水位只用於「首次建立 restore.json 快照」與「缺 provider
條目補齊」（restore.go:53-76）。凍結語意：

- **`err != nil` 或 `scanned == false`（含 NotExist——見 §2 表註：此路徑的 NotExist
  代表 sink 建立失敗或外部刪除，不是全新 workspace）**：發具名 audit
  **`restore_watermark_unavailable`**（含 path 與 error；NotExist 合成可操作說明）
  ＋startup warning，將 `""` 作為 fallback **傳入** `openRestoreStore`，**繼續啟動**。
  不列為 blocker、不停止 app。
  **命名理由（owner review rev3 P2）**：audit 陳述的是「watermark 不可用」這個
  確定發生的事實，不是「snapshot 被降級寫入」——fallback 是否實際被消費由
  restore.json 的狀態決定（下段），事件名稱不得宣稱可能不存在的 snapshot 變更。
- `scanned == true`：以實際高水位（含空檔的 `""`——這才是全新 workspace 的正常格）
  傳入，無軌跡。

**fallback 的消費條件（凍結——watermark 不可用≠snapshot 被改）**：
`openRestoreStore` 只在三種情況消費 highWatermark 參數——restore.json **不存在**
（freshEntries **持久化**）、**malformed**（重建並**持久化**）、**缺 provider 條目**
（補齊**僅改記憶體**，等後續 CommitResume／ClearResume 順帶落盤）。restore.json
已完整存在時 fallback **不被消費、既有 boundary 不變**——watermark 不可用的那次
啟動只留軌跡、不改 snapshot。

**持久降級——如實描述（owner review rev1 P1；spec gate rev2 P1 修正修復路徑）**：
restore.json 不存在或 malformed 時，`freshEntries("")` 會**立即持久化**
（restore.go:47、:56；「缺 provider 條目補齊」:63-67 則只改記憶體、不落盤——
兩種路徑的持久化語意不同，spec gate P2）；之後成功重啟**不會**自動修復這個
boundary。**唯一的明確修復路徑是手動刪除／編輯 restore.json**（刪除後下次啟動
以當時高水位重建）——`ResetView` 寫的是 workspace-sessions.json 的 per-WSID
boundary（wsregistry），**不會**改到 restore.json（restore.json 的
`ViewStartEventID` 寫入點僅存在於 `openRestoreStore` 內，restore.go:65/:73/:74；
spec gate 實測「開新對話」後檔案仍為 `""`）。owner 裁決接受此 legacy snapshot 的
持久降級，以避免一個相容性快照阻擋整個 app。降級期間的實際影響：

- per-WSID 的 hydrate 前綴與 backfill 比對**不會誤接**——空 boundary 的 guard
  （closeout 票 §4/§5）全面擋住。
- **真正的殘留風險是遷移視窗被放寬**（spec gate P2 校正——`RestoreViews` 自
  Task 26 起已無 production caller，僅為 Go 測試出口，「使用者可見重放」不成立）：
  未遷移 workspace 的 `legacyEntries` 以 `viewStart=""` 掃描等於該 provider 全部
  歷史，只靠「三者皆空即跳過」擋住——此為 app.go:1264-1274 已知分歧的既有形狀，
  本票不擴大處理。

**「不阻擋啟動」的適用範圍（spec gate rev2 P1 限定）**：只在 **migration marker
已設的穩態**成立。未遷移 workspace 遇到同一個 I/O 條件時，`legacyEntries` 會先
因 `scanned==false` 回錯（closeout C4a 的 degraded startup 防護）→
`loadSessionRegistry` 失敗 → registry 未接線、CreateSession 早退＋startup
blocker——這是**刻意的複合失效形狀**（marker 防護優先於 snapshot 便利性），
§4 的「不阻擋」裁決不覆蓋它。

## 5. 測試策略

- **unit（restore.go 同 package）**：
  - NotExist → `("", false, nil)`。
  - **open error 確定性注入（owner review rev1 P2——chmod 需 root skip、目錄注入
    實際命中 Scanner.Err() 而非 os.Open）**：把父路徑建立成普通檔案、再讀其子路徑
    （如 `<tmp>/file/events.jsonl`）→ 確定得到 ENOTDIR 的 open error → err。
  - Scanner.Err()（>16MiB 單行）→ err、**不回部分值**（獨立於 open error 的測試）。
  - 完整掃描／空檔 → `(last|"", true, nil)`。
  - mutation：忽略 `Scanner.Err()` 改回現狀 → 截讀測試紅。
- **ResetView 路徑（app 層）**：watermark 失敗注入——**app 層打 scan error 分支**
  （app 已啟動後把 events.jsonl 換成同名目錄：`os.Open` 成功、`Scanner.Err()` 回
  is a directory；spec gate 實測 ENOTDIR 父路徑手法在 app 層不可佈——stateDir
  必須是真目錄，open error 分支只在 unit 層以 ENOTDIR 覆蓋）→「開新對話」回錯、
  registryOnDisk 斷言 boundary 與 LegacyTranscript **皆未變**、**lifecycle 回到
  idle（FinishReset 已執行）且可重試**；audit **逐行定位**
  `reset_view_watermark_failed` 並斷言同一筆含 `provider`／`wsid`／`path`／`error`
  四欄位（逐行手法沿用 `auditHasOp` 既有 pattern，app_registry_uncertain_test.go:364）；
  修復後重試成功且 boundary 為正確高水位。NotExist（刪 events.jsonl）→ 同樣停止
  不寫、error 欄位為合成的可操作內容。
- **snapshot 路徑（§4 裁決 (a)；fixture 前提：migration marker 已設**——未遷移
  workspace 會先被 C4a 防護擋在 legacyEntries，見 §4 複合失效形狀）：
  - 降級啟動（events.jsonl 預建成目錄：sink `OpenFile` 回 EISDIR 順帶覆蓋 sink
    失敗路徑、watermark 命中 scan error；或刪檔打 NotExist）＋**fixture 明訂
    restore.json 不存在或 malformed**（fallback 才會被消費並持久化）→ app 繼續
    啟動、audit 含 `restore_watermark_unavailable`＋startup warning、restore.json
    快照 view_start 為 `""`。
  - **完整 restore.json 的反向案例**：restore.json 已完整存在＋watermark 不可用
    → audit 仍發（watermark 不可用是事實）、**既有 boundary 不變**（fallback 未
    被消費）——鎖住「不可用≠被改」的區分。
  - **持久降級的重啟形狀**：降級啟動後修復 events.jsonl → 再次啟動 → 快照 boundary
    仍為 `""`（不自動修復）；**刪除 restore.json → 再啟動 → boundary 為當時正確
    高水位**（§4 唯一修復路徑的測試，spec gate P1 校正——不再宣稱 ResetView 可修）。
- 既有四處測試 caller 簽章適配；迴歸：RestoreViews／reset 流程既有測試不破。

## 6. 非目標

- `replayViewWindow` 維持凍結（RestoreViews 專用）。
- LoadTurnsBefore 前端錯誤顯示（UI P1 票）。
- malformed 行語意。
