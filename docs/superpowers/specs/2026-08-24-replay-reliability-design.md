# Replay reliability：readEnvelopeRange 讀取錯誤 fail loud 設計

Owner 開票（2026-08-24）：「readEnvelopeRange 吞讀取錯誤是既有的通用可靠性問題，
範圍與 legacy hydrate 收尾不同，獨立一張。」本文件為該票的設計 spec。

**修訂記錄**：
- rev3（2026-08-24，owner review 3 P2）：包裝 EOF fixture（`errors.Is` vs `==` 的可
  區辨測試——真實檔案只產生裸 EOF）；EIO 測試錯誤脈絡雙斷言（offset＋wsid 缺一必
  紅）；§3 過渡期措辭校正（backend 不再靜默成功≠完整使用者層 fail loud，後者等
  UI P1）。
- rev2（2026-08-24，spec gate 2 P1／4 P2）：EOF 寬容的兜底歸屬改正（`checkpointTrustedLocked`
  啟動期兜底，非 corrupt.go 損壞分級；b/c/e 三格標已知殘餘風險）；新增 legacy scan
  error 分支守門義務（本票會讓既有兩個目錄注入測試短路、原守門失效——index 零 turn
  fixture 補上）；簽章放寬 `io.ReadSeeker` 使「讀到一半才錯」可注入；app 層 offset
  脈絡斷言升格為必要；EOF 判定統一 `errors.Is`；過渡期 UI 行為明文；§5 引用校正。
- rev1.1：auditHighWatermark 相鄰缺口載入 owner 另開 P1 裁決。
- rev1：初版。

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

`readEnvelopeRange` 讀取迴圈（EOF 判定一律 `errors.Is(rerr, io.EOF)`——§4 簽章
放寬成 interface 後，`==` 對包裝過的 EOF 會誤判成錯誤）：

| 情況 | 處置 |
|---|---|
| `errors.Is(rerr, io.EOF)` | 正常終點——**先處理同批回傳的殘行**（`ReadBytes` 可能同時回資料與 EOF：最後一行無換行），再 break。行為與現狀一致（現行迴圈已是「處理殘行→break」順序，rebuild_orchestrator.go:424-439，gate 實測確認） |
| `rerr != nil` 且非 EOF | **回錯**（wrapped，含 offset 與 wsid 脈絡），`loadTurnsBefore` 傳播給呼叫端——不靜默截頁 |
| malformed 單行 | 跳過不中斷（既有慣例不變：UI 視窗不因單一壞行整段消失） |

**EOF 早於 `end`（index 宣稱的 range 超出檔尾）維持現狀寬容**——兜底歸屬與殘餘
風險（spec gate 2026-08-24 校正，原 rev1 指錯機制）：

| 格 | 情境 | 兜底 |
|---|---|---|
| a | 重啟後 checkpointOffset > 檔尾（尾端截檔跨重啟） | **有**：`checkpointTrustedLocked`（internal/replayindex/rebuild.go:216-229）判不可信 → quarantine＋全量重建（rebuild.go:151-161）。僅啟動期跑（`VerifyOrRebuild` 唯一呼叫點 app.go:1922） |
| b | checkpointOffset 合法但單筆 turn record 的 EndOffset > 檔尾（crash 窗口＋截檔的雙重故障） | **無**——已知殘餘風險 |
| c | 執行期截檔（啟動驗證之後 events.jsonl 被縮短） | **無**——runtime 窗口無人兜，靜默部分頁直到下次重啟；已知殘餘風險（外部行為者） |
| e | open-turn tail 的 start 超出檔尾 | **無專屬兜底**；實務上 start ≤ checkpointOffset，風險低 |

b／c／e 保留寬容（皆為雙重故障或外部行為者，為此把 UI 打成 fail loud 不成比例）；
**不**在本票加 runtime 截檔偵測訊號——那屬 replayindex runtime 驗證域，要補是獨立票。
在測試中以迴歸鎖固定 a 格之外的寬容行為（`end` 超出檔尾讀到 EOF 為止、不回錯）。

## 3. 影響面

- 呼叫端只有 `loadTurnsBefore` 兩處，error 傳播路徑既有（`return nil, err`）。
- 前端：`LoadTurnsBefore` 的錯誤目前不會被 `pin()` 轉換為使用者可見的錯誤狀態
  （unhandled rejection）——那是已開的 UI P1 票
  （memory 1b）的範圍，本票不動 frontend；本票完成後，UI P1 票的錯誤顯示自動
  涵蓋本路徑。
- 既有測試：`TestLoadTurnsBeforeScanErrorPropagates` 與
  `TestLoadTurnsBeforeScanErrorDoesNotClearFlag`（目錄注入）目前實際在
  `readEnvelopeRange` 先回 nil、由 legacy 掃描回錯（closeout final review Minor 1）
  ——本票之後同一注入會改由 turn-read 路徑先回錯，兩測試的斷言（回錯、旗標不清）
  仍綠，**但原本守的 legacy scan error 分支從此無人守**（spec gate P1 實測：套
  patch 後把 legacy 分支的 `lerr` 吞掉，全套測試全綠）。因此本票**必須**同時交付
  legacy 分支的新守門測試：fixture 用 index 零 turn record（events.jsonl 只有
  legacy 無 WSID 事件、無 open turn）→ `readEnvelopeRange` 根本不被呼叫 → 目錄
  注入時錯誤只可能來自 `scanLegacyWindow`——mutation（吞掉 lerr）必紅。此 fixture
  同時解掉 closeout final review Minor 1（scan error 測試未真驅動 §6a 分支）。
- **過渡期使用者可見行為（本票落地、UI P1 票未落地期間；owner review 措辭校正）**：
  本票讓 **backend 不再靜默成功**（讀取錯誤如實回錯），但 **frontend 仍未向使用者
  顯示錯誤**——`pin()` 的 load 無 try/catch，錯誤成 unhandled rejection
  （session.ts:409-411），該 pane 呈現「完全空白、無訊息」。這不是完整的 fail
  loud：**完整的使用者層 fail-loud 契約須等 UI P1 票完成**。過渡態（空白優於殘缺
  誤導）為刻意接受；錯誤顯示與軌跡統一由 UI P1 票處理，本票不在 `loadTurnsBefore`
  呼叫點另加 audit——避免兩票各做半套。

## 4. 測試策略

**簽章放寬（spec gate P2 裁決，三選一取 (a)）**：`readEnvelopeRange` 第一參數由
`*os.File` 放寬為 `io.ReadSeeker`——兩個呼叫點傳 `*os.File` 型別相容、零改動，
放寬後才能以 stub reader 造出「回 N 行後回 EIO」的**讀到一半才錯**形狀（darwin
目錄注入的 EISDIR 只出現在首讀，蓋不到這格）。

- **unit（同 package main，直呼 readEnvelopeRange）**：
  - stub reader 注入「先回 3 行合法 envelope、再回 EIO」→ 回錯且**不得**把已讀的
    3 行當成功結果回傳（部分成功樣即靜默截頁）。**錯誤脈絡雙斷言**（owner review
    P2）：錯誤訊息必須同時含預期 offset 與 wsid——移除任一脈絡都必須使測試失敗
    （凍結語意「wrapped，含 offset 與 wsid 脈絡」不能只驗到一半）。核心 mutation：
    把非 EOF 分支改回 `break` → 本測試紅。
  - **包裝 EOF fixture**（owner review P2——`errors.Is` 的可區辨測試）：stub reader
    回傳一行合法、WSID 相符且**無換行**的最後一行，同批回
    `fmt.Errorf("wrapped: %w", io.EOF)` → 斷言該行被收錄**且不回錯**。mutation：
    把 `errors.Is(rerr, io.EOF)` 改回 `rerr == io.EOF` → 包裝 EOF 被當成讀取錯誤、
    本測試紅（真實檔案只會產生裸 io.EOF，沒有這條 fixture 該 mutation 恆綠）。
  - 目錄 FD 注入（首讀 EISDIR）→ 回錯（開檔即壞的形狀）。
  - 正常檔案含 malformed 行 → 跳過、其餘照回（既有慣例迴歸鎖）。
  - 檔尾無換行的最後一行 → 必須被收進 out。fixture 條件寫死（gate A(3)）：該行須
    為**合法 JSON＋WSID 相符＋無換行**——半截 JSON 會被 malformed 跳過使 mutation
    恆綠。mutation：把殘行處理移到 break 之後 → 紅。
  - `end` 超出檔尾 → 讀到 EOF 為止、回部分結果、**不回錯**（§2 寬容格迴歸鎖）。
- **app 層**：
  - index 建好後把 events.jsonl 換成同名目錄 → `LoadTurnsBefore` 回錯，且**必要
    斷言**錯誤訊息含 offset 脈絡（`read events.jsonl at`）——沒有這條斷言，既有兩
    測試修前修後都綠、對本票 mutation 零鑑別力（gate D 實測）。
  - legacy 分支守門（§3 義務）：index 零 turn fixture＋目錄注入 → 錯誤來自
    `scanLegacyWindow`；吞 lerr 的 mutation 必紅。

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
  boundary**。（引用校正＋盤點起點，省該票成本：ResetView 消費點在 **app.go:9084**
  （9075 是註解行）；production caller 共兩處——app.go:2242（`openRestoreStore`
  初始化快照）與 app.go:9084（ResetView 高水位）。）
- **`replayViewWindow`（restore.go:167）**：維持凍結不動（僅 RestoreViews 消費，
  hydrate 主線 spec 已明文保留）。

## 6. 驗證

unit＋app 層測試如 §4；`go build ./... && go vet ./...`；`go test . -count=1`
全套（牆鐘不穩定名單規則照舊）。
