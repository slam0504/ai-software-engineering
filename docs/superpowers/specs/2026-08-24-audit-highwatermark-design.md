# auditHighWatermark 空高水位污染寫入路徑 設計

Owner 開票（2026-08-24，P1）：`auditHighWatermark` 讀取失敗時靜默回 `""`，且被
寫入路徑直接消費——空 boundary 被寫入 durable registry。Owner 凍結方向：**兩個
caller 契約分開**——ResetView 寫入路徑遇 open／scan error 必須停止且不得改
boundary；startup snapshot 路徑是否允許降級獨立裁決。本文件 rev1。

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
| 檔案不存在（NotExist） | `("", false, nil)`——全新 workspace 的合法狀態，不是錯誤 |
| open 失敗（非 NotExist） | `("", false, err)` |
| `Scanner.Err()` 非 nil | `("", false, err)`——**不再忽略**（截讀的偏舊值對寫入路徑是持久化污染，見 §3；不回部分值，避免呼叫端誤用） |
| 完整掃描 | `(last, true, nil)`（空檔回 `("", true, nil)`——存在且確定為空，`""` 是正確高水位） |

malformed 行跳過不變。

## 3. ResetView 寫入路徑契約（owner 已凍結）

在呼叫 `a.wsReg.ResetView` **之前**判定：

- `err != nil`（open／scan error）→ **停止：不呼叫 ResetView、boundary 與
  LegacyTranscript 旗標皆不得變**；錯誤掛上既有 `rerr` 路徑（該區塊已有錯誤協定
  ——哨兵良性跳過＋`rerr` 傳播，app.go:9081-9094），「開新對話」該次失敗、可重試。
  失敗前發具名 audit（`reset_view_watermark_failed`，含 error 內容——對齊
  `reset_view_skipped` 慣例）。
- `scanned == false && err == nil`（NotExist）→ **同樣停止、fail loud**：ResetView
  時 events.jsonl 必然存在（sink `O_CREATE` 先於啟動完成，closeout final review
  已獨立核實），缺檔＝異常（外部刪除），不是合法的「空 workspace」。
- `scanned == true` → 照常 `ResetView(wsid, last)`（含空檔 `""` 的情況——存在且
  確定為空時 `""` 是正確高水位，此格與現行為一致；區別在「確定為空」與「讀不到」
  自此可分）。

lifecycle 影響：停止發生在 registry 寫入之前，session 的 reset 流程（manager
BeginReset 等）已進行——`rerr` 的既有語意就是「lifecycle 已收束但 durable 寫入
失敗」，本票不改變該處置，只是讓 watermark 失敗也走同一條路。

## 4. startup snapshot 路徑（獨立裁決——兩案並列，owner 決定）

`openRestoreStore` 的高水位只用於「首次建立 restore.json 快照」與「缺 provider
條目補齊」（restore.go:53-76）：

- **(a) 維持降級（傾向，與既有 startup 慣例一致）**：open／scan error 時以 `""`
  初始化快照＋記 startupErr（現行 sink 失敗等 startup 異常皆降級不阻擋，app.go:2199
  一帶）。風險：該 workspace 的 RestoreViews 重放整段歷史（一次性、可見但不毀
  資料）；空 boundary 的後續效應（hydrate 前綴、backfill 比對）已由 closeout 票的
  guard 全面擋住。差異化：至少把降級**留下軌跡**（audit `restore_snapshot_degraded`
  ＋startup warning），不再全然靜默。
- **(b) fail loud 阻擋啟動**：一致性最強，但暫時性 I/O 錯誤會讓 app 起不來——
  與「startup 降級不阻擋」的既有慣例衝突，且 snapshot 是便利性（重放範圍），非
  資料完整性。

NotExist 兩案皆回 `""` 合法（全新 workspace）。

## 5. 測試策略

- **unit（restore.go 同 package）**：NotExist → `("", false, nil)`；open error
  （chmod 0o000＋root skip，或目錄注入視實際錯誤點）→ err；Scanner.Err()（>16MiB
  單行）→ err、不回部分值；完整掃描／空檔 → `(last|"", true, nil)`。mutation：
  忽略 `Scanner.Err()` 改回現狀 → 截讀測試紅。
- **ResetView 路徑（app 層）**：watermark 失敗注入（events.jsonl 換目錄）→
  「開新對話」回錯、registryOnDisk 斷言 boundary 與 LegacyTranscript **皆未變**、
  audit 含 `reset_view_watermark_failed`；修復後重試成功且 boundary 為正確高水位。
  NotExist（刪 events.jsonl）→ 同樣停止不寫。
- **snapshot 路徑**：依 §4 裁決結果定測試（(a)：降級軌跡 audit＋快照 `""`；
  (b)：啟動阻擋）。
- 既有四處測試 caller 簽章適配；迴歸：RestoreViews／reset 流程既有測試不破。

## 6. 非目標

- `replayViewWindow` 維持凍結（RestoreViews 專用）。
- LoadTurnsBefore 前端錯誤顯示（UI P1 票）。
- malformed 行語意。
