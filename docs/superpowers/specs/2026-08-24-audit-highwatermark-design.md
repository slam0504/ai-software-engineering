# auditHighWatermark 空高水位污染寫入路徑 設計

Owner 開票（2026-08-24，P1）：`auditHighWatermark` 讀取失敗時靜默回 `""`，且被
寫入路徑直接消費——空 boundary 被寫入 durable registry。Owner 凍結方向：**兩個
caller 契約分開**——ResetView 寫入路徑遇 open／scan error 必須停止且不得改
boundary；startup snapshot 路徑是否允許降級獨立裁決。

**修訂記錄**：
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
| `openRestoreStore`（app.go:2242，啟動快照） | `freshEntries(hw)`／缺 provider 補齊 | 快照 view_start=`""` → RestoreViews 重放整段歷史；空 boundary 的 hydrate／backfill 效應已由既有 guard 擋住（closeout 票） |
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

**lifecycle 收束（owner 裁決）**：兩種停止情況回錯、不寫 registry，但**最後仍須
FinishReset 讓 lifecycle 回到 idle**——停止的是 durable 寫入，不是 reset 流程本身
（`rerr` 的既有語意就是「lifecycle 已收束但 durable 寫入失敗」）。既有 SettingsBar
會顯示錯誤且不清 transcript，使用者可修復後重試「開新對話」。

## 4. startup snapshot 路徑（owner 裁決 2026-08-24：採 (a) 維持啟動降級＋完整診斷軌跡）

`openRestoreStore` 的高水位只用於「首次建立 restore.json 快照」與「缺 provider
條目補齊」（restore.go:53-76）。凍結語意：

- **`err != nil` 或 `scanned == false`（含 NotExist——見 §2 表註：此路徑的 NotExist
  代表 sink 建立失敗或外部刪除，不是全新 workspace）**：發具名 audit
  `restore_snapshot_degraded`（含 path 與 error／NotExist 說明）＋startup warning，
  以 `""` 建立 legacy snapshot，**繼續啟動**。不列為 blocker、不停止 app。
- `scanned == true`：以實際高水位（含空檔的 `""`——這才是全新 workspace 的正常格）
  建立快照，無軌跡。

**持久降級——如實描述（owner review rev1 P1，不得宣稱「一次性」或自動復原）**：
restore.json 不存在或 malformed 時，`freshEntries("")` 會**立即持久化**
（restore.go:47、:56）；之後成功重啟**不會**自動修復這個 boundary——空 boundary
**持續存在直到明確修復**（例如使用者對該 provider 按「開新對話」讓 ResetView 寫入
新高水位，或手動處理 restore.json）。owner 裁決接受此 legacy snapshot 的持久降級，
以避免一個相容性快照阻擋整個 app。降級期間的實際影響：

- per-WSID 的 hydrate 前綴與 backfill 比對**不會誤接**——空 boundary 的 guard
  （closeout 票 §4/§5）全面擋住。
- `RestoreViews`（provider-keyed legacy 出口）**仍可能重放該 provider 全歷史**
  ——可見、不毀資料，屬明文接受的降級。

## 5. 測試策略

- **unit（restore.go 同 package）**：
  - NotExist → `("", false, nil)`。
  - **open error 確定性注入（owner review rev1 P2——chmod 需 root skip、目錄注入
    實際命中 Scanner.Err() 而非 os.Open）**：把父路徑建立成普通檔案、再讀其子路徑
    （如 `<tmp>/file/events.jsonl`）→ 確定得到 ENOTDIR 的 open error → err。
  - Scanner.Err()（>16MiB 單行）→ err、**不回部分值**（獨立於 open error 的測試）。
  - 完整掃描／空檔 → `(last|"", true, nil)`。
  - mutation：忽略 `Scanner.Err()` 改回現狀 → 截讀測試紅。
- **ResetView 路徑（app 層）**：watermark 失敗注入（ENOTDIR 手法同上）→
  「開新對話」回錯、registryOnDisk 斷言 boundary 與 LegacyTranscript **皆未變**、
  **lifecycle 回到 idle（FinishReset 已執行）且可重試**；audit **逐行定位**
  `reset_view_watermark_failed` 並斷言同一筆含 `provider`／`wsid`／`path`／`error`
  四欄位（只驗事件名稱不足——owner review rev1 P2）；修復後重試成功且 boundary
  為正確高水位。NotExist（刪 events.jsonl）→ 同樣停止不寫、error 欄位為合成的
  可操作內容。
- **snapshot 路徑（§4 裁決 (a)）**：
  - 降級啟動（ENOTDIR 或刪檔）→ app 繼續啟動、audit 含 `restore_snapshot_degraded`
    ＋startup warning、restore.json 快照 view_start 為 `""`。
  - **持久降級的重啟形狀**：降級啟動後修復 events.jsonl → 再次啟動 → 快照 boundary
    仍為 `""`（不自動修復——鎖住 §4 的如實語意）；對該 provider 執行 ResetView 後
    boundary 更新為正確高水位（明確修復路徑可用）。
- 既有四處測試 caller 簽章適配；迴歸：RestoreViews／reset 流程既有測試不破。

## 6. 非目標

- `replayViewWindow` 維持凍結（RestoreViews 專用）。
- LoadTurnsBefore 前端錯誤顯示（UI P1 票）。
- malformed 行語意。
