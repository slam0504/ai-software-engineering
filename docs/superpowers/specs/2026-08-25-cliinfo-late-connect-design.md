# 頂列 meta 晚連線空白（CLIInfo ready 契約）設計

M3b 開票記錄第 3 張（P2，m3b-results.md §11）：瀏覽器端晚於 startup 連線時頂列
meta 初始為空，重新載入後才正常；native Wails 同樣可能發生（OnStartup 與 binding
本就並行）。Owner 凍結方向（2026-08-24）：**修法應提供可判斷的 ready 狀態並重新
讀取 CLIInfo；只靠一次性事件仍會漏掉晚連線者；ready 契約必須同時涵蓋「先查後
訂閱」與「晚連線」**。本文件 rev1。

## 1. 問題與現狀

- Frontend：`App.vue:273` 在 `onMounted` 呼叫一次 `await CLIInfo()`（catch 忽略），
  無任何重讀機制。早連線（webview mount 早於 startup 發布欄位）拿到空欄位後永遠
  空白；晚連線（dev server 重整）通常正常——但兩種時序都必須被契約涵蓋。
- Go：`CLIInfo()`（app.go:3466 一帶）讀 `startupSnapshot()` 快照（併發安全，owner
  2026-08-19 已修資料競爭），但回傳無 ready 語意——呼叫端無從分辨「欄位還沒
  發布」與「發布了但就是空」。

## 2. Go 端：ready 旗標＋發布事件

- **ready 旗標**：`CLIInfo()` 回傳 map 增加 `"ready": "true"|"false"`。判準＝
  startup 已把五個欄位發布完成的既有狀態點（實作時從 `startupSnapshot` 的欄位
  發布時序找既有訊號——例如 startup 序列對 startupData 的最後一次寫入處立
  flag；**不得**用「欄位非空」推導：空 toolsDir 在某些失敗形狀是合法終值）。
  additive 欄位，既有 key 不動。
- **發布事件**：startup 發布欄位完成的那一刻 `emitUI("workbench:cli-ready", nil)`
  （一次性；沿用既有 emitUI 出口）。事件**不帶資料**——資料一律由 `CLIInfo()`
  查詢取得（單一來源，避免事件 payload 與 binding 回傳漂移）。
- 失敗形狀不阻塞 ready：startup 失敗（sink 壞、CLI 缺）也會走到欄位發布點——
  ready 表示「值已定案」，不是「一切健康」；空值＋ready 是合法組合（UI 顯示
  現況即可）。

## 3. Frontend：先訂閱後查詢

`App.vue` 的 onMounted 改為：

1. **先** `EventsOn('workbench:cli-ready', refresh)`（`refresh` ＝ 重新
   `await CLIInfo()` 並覆寫 `cliInfo.value`）。
2. **後** 首次 `refresh()`。

時序覆蓋（契約核心，兩格都要測）：
- **早連線**：首查拿到 `ready=false`（或空）→ startup 完成時事件到 → refresh 拿
  到定案值。訂閱在查詢之前，事件不可能落在「查完之後、訂閱之前」的縫裡。
- **晚連線**：mount 時 startup 已 ready、事件早已發過（收不到）→ 首查直接拿到
  `ready=true` 的定案值——不依賴事件。
- 首查 `ready=false` 且事件因任何原因遺失：**不加輪詢**（keep minimal——事件＋
  首查已涵蓋兩格；Wails EventsOn 在同 process 內不丟事件，dev server 晚連線屬
  「晚連線」格。若實測發現第三格再議，spec 不預先複雜化）。
- `onUnmounted` 對應 `EventsOff`（沿用 App.vue 既有事件清理慣例）。

## 4. 測試策略

- **Go**：`CLIInfo()` 在 startup 發布點前後的 ready 值（早於發布＝false、發布後
  ＝true）；事件恰在發布點發出一次（audit 或既有 emitUI 測試攔截手法——依
  repo 既有 emitUI 測試慣例，動手前先查）。
- **Frontend（vitest）**：mock CLIInfo 與 EventsOn——
  - 早連線格：首查回 `ready=false` 空值 → 觸發 cli-ready handler → 第二次查回
    定案值 → 頂列顯示更新（mutation：拿掉訂閱或訂閱在查詢之後，此測紅）。
  - 晚連線格：首查即回 `ready=true` 定案值 → 顯示正確且**不需**事件。
  - 訂閱先於查詢的順序斷言（spy 呼叫順序）。
- 迴歸：既有 App.vue 測試（registryUncertain.test.ts 等 mount 全 App 的測試）
  不破——CLIInfo mock 需補 ready 欄位。

## 5. 非目標

- 不改 startup 時序本身、不加輪詢、不把 CLIInfo 資料塞進事件 payload。
- 不處理「startup 中途欄位變動」（欄位一次定案，無中途更新需求）。
