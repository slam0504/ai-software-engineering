# Replay reliability：readEnvelopeRange 讀取錯誤 fail loud 設計

Owner 開票（2026-08-24）：「readEnvelopeRange 吞讀取錯誤是既有的通用可靠性問題，
範圍與 legacy hydrate 收尾不同，獨立一張。」本文件為該票的設計 spec，rev1。

## 1. 問題

`readEnvelopeRange`（rebuild_orchestrator.go:417-441）是 `loadTurnsBefore` 讀取
turn envelope 的唯一 I/O 出口（兩個呼叫點：per-record range :330、open-turn tail
:341）。它的讀取迴圈對 `ReadBytes` 的任何錯誤一律 `break`：

```go
if rerr != nil {
    break // EOF 或讀取錯誤都代表這段已到終點
}
```

EOF 是正常終點；但非 EOF 的讀取錯誤（EIO、EISDIR、裝置消失、權限在開檔後被撤）
被當成同一件事——**靜默截頁**：UI 顯示殘缺的 transcript、沒有任何錯誤，與
repo 的 fail loud 契約（暫時性 I/O 錯誤不得偽裝成資料不存在）直接矛盾。同一份
檔案的 legacy 掃描（`scanLegacyWindow`）在 closeout 票已改為 fail loud，同一個
函式（`loadTurnsBefore`）內兩種 error 姿態並存。

## 2. 凍結語意

`readEnvelopeRange` 讀取迴圈：

| 情況 | 處置 |
|---|---|
| `rerr == io.EOF` | 正常終點——**先處理同批回傳的殘行**（`ReadBytes` 可能同時回資料與 EOF：最後一行無換行），再 break。行為與現狀一致 |
| `rerr != nil` 且非 EOF | **回錯**（wrapped，含 offset 與 wsid 脈絡），`loadTurnsBefore` 傳播給呼叫端——不靜默截頁 |
| malformed 單行 | 跳過不中斷（既有慣例不變：UI 視窗不因單一壞行整段消失） |

**EOF 早於 `end`（index 宣稱的 range 超出檔尾）維持現狀寬容**：這是「events.jsonl
尾端 truncate＋stale index」的形狀，屬 replayindex 損壞分級（尾端 truncate →
靜默補掃）既有機制的職責域，本票不重疊處理；在測試中以迴歸鎖固定此行為。

## 3. 影響面

- 呼叫端只有 `loadTurnsBefore` 兩處，error 傳播路徑既有（`return nil, err`）。
- 前端：`LoadTurnsBefore` 回錯目前會被 `pin()` 吞掉——那是已開的 UI P1 票
  （memory 1b）的範圍，本票不動 frontend；本票完成後，UI P1 票的錯誤顯示自動
  涵蓋本路徑。
- 既有測試：`TestLoadTurnsBeforeScanErrorPropagates` 與
  `TestLoadTurnsBeforeScanErrorDoesNotClearFlag`（目錄注入）目前實際在
  `readEnvelopeRange` 先回 nil、由 legacy 掃描回錯（closeout final review Minor 1）
  ——本票之後同一注入會改由 turn-read 路徑先回錯，兩測試的斷言（回錯、旗標不清）
  **仍成立**，但錯誤來源改變；測試註解同步更正，不改斷言。

## 4. 測試策略

- **unit（同 package main，直呼 readEnvelopeRange）**：
  - 目錄 FD 注入（darwin `os.Open(dir)` 成功、read 回 EISDIR）→ 回錯、非空 out
    不得部分回傳成功樣。
  - 正常檔案含 malformed 行 → 跳過、其餘照回（既有慣例迴歸鎖）。
  - 檔尾無換行的最後一行 → 必須被收進 out（EOF 同批殘行處理；mutation：把殘行
    處理放到 break 之後會紅）。
  - `end` 超出檔尾 → 讀到 EOF 為止、回部分結果、**不回錯**（§2 寬容格迴歸鎖）。
- **app 層**：index 建好後把 events.jsonl 換成同名目錄 → `LoadTurnsBefore` 回錯
  （更正既有兩測試的來源註解即涵蓋，另加一條斷言錯誤訊息含 offset 脈絡）。

## 5. 相鄰缺口（owner 已裁決，本票非目標）

- **`auditHighWatermark`（restore.go:137-155）open 失敗靜默回 `""`，且被
  `ResetView`（app.go:9075）直接消費**：open 失敗當下按「開新對話」會把 boundary
  寫成空字串＋清 legacy 旗標。下游已由空 boundary guard 擋住（hydrate 不前綴、
  backfill 跳過），方向安全，但「新對話的 view 從空 boundary 開始」等於下次重啟
  重放整段歷史。它同時忽略 `Scanner.Err()`（截讀時高水位偏舊——方向安全，多重放
  不漏放）。
  **Owner 裁決（2026-08-24）：另開 P1，不併入本票**——它涉及 ResetView 的持久化
  boundary 與使用者可見歷史，不只是 reader 可靠性；該票需先盤點 auditHighWatermark
  全部 caller，再凍結哪些讀取路徑可寬容、哪些**寫入路徑必須 fail loud 且不得改
  boundary**。
- **`replayViewWindow`（restore.go:167）**：維持凍結不動（僅 RestoreViews 消費，
  hydrate 主線 spec 已明文保留）。

## 6. 驗證

unit＋app 層測試如 §4；`go build ./... && go vet ./...`；`go test . -count=1`
全套（牆鐘不穩定名單規則照舊）。
