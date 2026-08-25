# 頂列 meta 晚連線空白（CLIInfo ready 契約）設計

M3b 開票記錄第 3 張（P2，m3b-results.md §11）：瀏覽器端晚於 startup 連線時頂列
meta 初始為空，重新載入後才正常；native Wails 同樣可能發生（OnStartup 與 binding
本就並行）。Owner 凍結方向（2026-08-24）：**修法應提供可判斷的 ready 狀態並重新
讀取 CLIInfo；只靠一次性事件仍會漏掉晚連線者；ready 契約必須同時涵蓋「早連線
（首次查詢早於 backend 定案）」與「晚連線」兩種時序**。本文件 rev4。

## 1. 問題與現狀

- Frontend：`App.vue:273` 在 `onMounted` 呼叫一次 `await CLIInfo()`（catch 忽略），
  無任何重讀機制。早連線（webview mount 早於 startup 發布欄位）拿到空欄位後永遠
  空白；晚連線（dev server 重整）通常正常——但兩種時序都必須被契約涵蓋。
- Go：`CLIInfo()`（app.go:3467 一帶）讀 `startupSnapshot()` 快照（併發安全，owner
  2026-08-19 已修資料競爭），但回傳無 ready 語意——呼叫端無從分辨「欄位還沒
  發布」與「發布了但就是空」。

## 2. Go 端：ready 旗標＋發布事件

- **ready 定義（rev3 凍結）**：ready ＝ **唯一 startup owner 對快照的寫入已全部
  完成（owner 函式主體已返回）**。兩個不得採用的判準：
  1. 不綁欄位發布點（rev1 的判準）——startup 有多條提前返回到不了
     `publishNodePath`（app.go:2288）：lease 取得失敗（app.go:2148）、
     `openStateWriters` 失敗（app.go:2150）、`migrateLegacyState` 失敗
     （app.go:2270）。晚連線者在這些形狀下會永遠等不到 ready；且發布點之後
     `startupEvidence()`（app.go:2297）仍以 `setStartupErrOnce` 更新
     startupError（app.go:2408–2422），發布點不是 owner 的最後寫入處。
  2. 不綁「owner lifecycle 已終止」（rev2 的判準）——單一 finalizer 內 ready 發布
     與事件必須在 `endStartupLifecycle()` **之前**（見下），發布當下
     `startupRunning` 仍為 true；把 ready 定義成 owner 終止會讓定義與凍結的執行
     順序自相矛盾。
- **ready 不承諾整份快照凍結（rev3 更正）**：ready 保證的是 **meta 欄位**
  （toolsDir／toolsSource／CLI 路徑／nodePath／workspace／workspaceSource）自此
  不再變動。寫入者（rev4 更正）：toolsDir／toolsSource／nodePath 由 startup
  owner 寫入；workspace／workspaceSource 由 `acquireStateLease()` 內的
  `publishWorkspace`（app.go:2083／2090）寫入——production 正常路徑是
  `runInstance` 的 preflight（main.go:72，開視窗前）先寫，owner 隨後在 startup
  內的冪等呼叫因 lease 已存在直接返回、不再發布。兩類寫入都在 ready 之前，
  已核對這些欄位皆無 post-ready 的 production writer。`startupError`
  **不在**保證範圍：
  ready 之後仍有兩條既有的 fail-loud 追加路徑——
  1. startup 完成後再次呼叫 `startup()`：ownership 被拒後仍寫入「啟動序列只執行
     一次」橫幅（app.go:2127，`TestStartupIsRefusedAfterItAlreadyCompleted`
     明確要求此行為）。
  2. 稽核 writer 在 ready 後異常消失：`noteAuditInvariantBrokenLocked` 直接
     `appendStartup`（app.go:3416，app_audit_lifecycle_test.go 已保護）。
  兩條都是 owner 核定（2026-08-18／08-19／08-20）且有測試守護的行為，本 spec
  不重塑；後果是**兩個 ready=true 的回覆可能在 startupError 上不同**，由
  frontend 的 request sequencing 收斂（§3）。
- **實作位置（rev3 凍結順序）**：`beginStartupLifecycle()` 成功後（現行
  app.go:2131 位置），以**單一 defer 閉包**取代現行 `defer
  a.endStartupLifecycle()`，閉包內固定順序：
  1. 同鎖落 ready 旗標（進 startupState）；
  2. `a.emit("workbench:cli-ready", nil)`；
  3. `a.endStartupLifecycle()`。
  不得拆成兩個 defer——Go defer LIFO 會依登記順序反轉執行，順序契約必須收在同
  一個閉包內，測試才有單一的斷言落點。end 放最後不是妥協而是必要：shutdown 等的是 startup
  lifecycle 收斂（app.go:2123），emit 先於收斂訊號，teardown 就不可能與 ready
  發布交錯。單一 defer 涵蓋正常完成、降級（sink／registry 失敗續走）與阻擋
  （lease／writer／migration 提前返回）等所有 ownership 內返回路徑。
  `beginStartupLifecycle` 被拒的返回（收尾已開始或已有另一個 owner，
  app.go:2129）在 defer 之前，不發布——ready 的發布權只屬於唯一 owner，因此
  **每個 process 至多發布一次**。
- **儲存形狀**：ready 放進 `startupInfo`／`startupState`（app.go:1335／1358），與
  其餘欄位在**同一把鎖**下完成最終發布；不得另設獨立旗標（否則 CLIInfo 快照與
  ready 之間出現第二種同步機制）。`CLIInfo()` 回傳 map 增加
  `"ready": "true"|"false"`，additive，既有 key 不動。
- **發布事件**：依上述凍結順序，ready 旗標先落、事件後發（事件觸發的 refresh
  必須讀得到 ready=true）。出口必須是 `a.emit`
  （app.go:2019）——`emitUI`（app.go:476）是測試注入欄位、production 預設 nil，
  直接呼叫會 panic；測試攔截仍沿用注入 `emitUI` 的既有手法。事件**不帶資料**——
  資料一律由 `CLIInfo()` 查詢取得（單一來源，避免事件 payload 與 binding 回傳
  漂移）。
- 失敗形狀不阻塞 ready：startup 失敗（lease 被占、sink 壞、CLI 缺）也會走到
  defer——ready 表示「值已定案」，不是「一切健康」；空值＋ready 是合法組合
  （UI 顯示現況即可）。

## 3. Frontend：先訂閱後查詢

`App.vue` 的 onMounted 改為：

1. **先** `EventsOn('workbench:cli-ready', refresh)`（`refresh` ＝ 重新
   `await CLIInfo()` 並依下述規則寫入 `cliInfo.value`），保存回傳的 disposer。
2. **後** 首次 `refresh()`。

**Request sequencing（rev3 凍結，取代 rev2 的「不可回退」單調規則）**：兩次
`refresh()` 可並行（首查與事件觸發），且 `CLIInfo()` 取快照後還會 exec CLI／
node 版本探測（app.go:3467），回覆延遲不定——較舊 request 的回覆可能較晚完成。
rev2 的單調規則只丟棄 `ready!=true`，前提是「ready=true 的回覆之間不互相矛盾」
——該前提不成立（§2：startupError 在 ready 後仍可能追加，兩個 ready=true 回覆
可以不同，且較舊者可能較晚完成並覆蓋較新者）。規則改為**最新 request 優先**：

- `refresh()` 每次發出時遞增同一個 setup 閉包內的計數器並記下自己的 token；
- 回覆完成時，token 不等於目前計數器（其後已有更新的 refresh 發出）就**整筆丟
  棄**，否則寫入 `cliInfo.value`。

這同時涵蓋 false→true 逆序（rev2 規則的原始動機）與 true→true 逆序，不需再疊
ready 判斷；ready 欄位保留給「早連線首查拿到空值」的顯示判斷與測試斷言。

時序覆蓋（契約核心，兩格都要測）：
- **早連線**：首查拿到 `ready=false`（或空）→ startup finalizer 發布 ready 時事
  件到 → refresh 拿到定案值。訂閱在查詢之前，事件不可能落在「查完之後、訂閱之前」的縫裡。
- **晚連線**：mount 時 startup 已 ready、事件早已發過（收不到）→ 首查直接拿到
  `ready=true` 的定案值——不依賴事件。
- 首查 `ready=false` 且事件因任何原因遺失：**不加輪詢**（keep minimal——事件＋
  首查已涵蓋兩格；Wails EventsOn 在同 process 內不丟事件，dev server 晚連線屬
  「晚連線」格。若實測發現第三格再議，spec 不預先複雜化）。
- **清理**：`EventsOn` 回傳 disposer（runtime.d.ts:41），`onUnmounted` 呼叫它。
  註：App.vue 現況只清 keydown（App.vue:244），既有兩個 Wails listener
  （`workbench:event`／`session:done`）並未清理——本次只保證**新增的** listener
  有清，既有未清理標為待清理項，不在本 scope 順手改。

## 4. 測試策略

- **Go**（ready 至少四格）：
  - startup 執行中（owner 未終止）：`ready=false`。
  - 正常完成：`ready=true`。
  - migration 提前失敗（`migrateLegacyState` 返回 false）：仍 `ready=true`。
  - writer 提前失敗（lease 或 `openStateWriters` 阻擋）：仍 `ready=true`。
  - 事件：每個 startup owner 恰發布一次（測試注入 `emitUI` 攔截計數）；
    `beginStartupLifecycle` 被拒的呼叫不發布。
  - **finalizer 執行順序**（rev3，事件攔截驗 lifecycle 狀態）：注入的 `emitUI`
    在被呼叫當下斷言（a）`CLIInfo()` 已回 `ready=true`（驗證順序①→②，
    mutation：旗標晚於 emit 落地，此測紅）；（b）startup lifecycle 仍在 running
    （驗證順序②→③，mutation：`endStartupLifecycle` 提前到 emit 之前，此測紅）。
  - post-ready 的 startupError 追加行為（被拒 startup 橫幅、audit invariant
    橫幅）已由既有測試保護（app_state_binding_lifecycle_test.go:380 一帶、
    app_audit_lifecycle_test.go:156 一帶），本 spec 不新增也不得刪改。
- **Frontend（vitest）**：mock CLIInfo 與 EventsOn——
  - 早連線格：首查回 `ready=false` 空值 → 觸發 cli-ready handler → 第二次查回
    定案值 → 頂列顯示更新（mutation：拿掉訂閱或訂閱在查詢之後，此測紅）。
  - 晚連線格：首查即回 `ready=true` 定案值 → 顯示正確且**不需**事件。
  - 訂閱先於查詢的順序斷言（spy 呼叫順序）。
  - **逆序完成格（false→true）**：兩個 deferred Promise——首查（ready=false）
    後 resolve、事件查詢（ready=true）先 resolve——最終顯示定案值（mutation：
    拿掉 request sequencing，此測紅）。
  - **逆序完成格（true→true，rev3）**：兩個回覆都是 `ready=true` 但
    `startupError` 不同（模擬 §2 的 post-ready 追加），較舊 request 較晚
    resolve——最終顯示**較新 request** 的回覆（mutation：sequencing 退化成只看
    ready 的單調規則，此測紅——這一格就是 rev2 規則的反例）。
  - 清理格：unmount 後 disposer 被呼叫（EventsOn mock 回傳 spy 清理函式）。
- 迴歸：既有 App.vue 測試（registryUncertain.test.ts 等 mount 全 App 的測試）
  不破——CLIInfo mock 需補 ready 欄位，EventsOn mock 需回傳清理函式。

## 5. 非目標

- 不改 startup 時序本身、不加輪詢、不把 CLIInfo 資料塞進事件 payload。
- 不處理「startup 中途欄位變動」（欄位一次定案，無中途更新需求）。
- 不順手清理既有未清的 Wails listener（標為待清理，另票處理）。

## 修訂記錄

- rev4（2026-08-25，gate 第二輪 P2 收斂）：
  - P2：更正 meta 欄位寫入者敘述——workspace／workspaceSource 由
    `acquireStateLease` 內的 `publishWorkspace` 寫入（production 正常路徑為
    `runInstance` preflight，main.go:72），非 startup owner；其餘欄位維持
    owner 寫入。寫入均在 ready 前，ready 契約不受影響。
- rev3（2026-08-25，implementation gate CHANGES_REQUIRED 收斂）：
  - P1：撤回「owner 終止後快照不再變動」的查核主張——ready 後仍有兩條
    startupError 追加路徑（被拒 startup 橫幅 app.go:2127、audit invariant 橫幅
    app.go:3416），且均有既有測試保護。ready 保證範圍縮為 meta 欄位；frontend
    規則由「ready 單調不可回退」改為 request sequencing（最新 request 優先），
    補 true→true 逆序完成測試格。
  - P1：rev2 的 defer 寫法與「ready＝owner 已終止」定義矛盾（`defer
    a.endStartupLifecycle()` 先登記，LIFO 使 ready 發布早於 owner 終止）。改為
    單一 defer 閉包凍結順序（ready 旗標 → emit → endStartupLifecycle），ready
    改義為「owner 對快照的寫入已全部完成」，補 emitUI 攔截的順序守門測試兩格。
- rev2（2026-08-25，review CHANGES_REQUIRED 收斂）：
  - P1：ready 判準從「欄位發布點」改為「唯一 startup owner 已終止」，defer 涵蓋
    所有 ownership 內返回路徑，ready 併入 startupState 同鎖發布；補四格 Go 測試。
  - P1：新增「定案值不可回退」單調規則與逆序完成測試格。
  - P1：事件出口更正為 `a.emit`（`emitUI` 是測試注入欄位，production 為 nil）。
  - P2：開頭「先查後訂閱」措辭更正為「早連線（首次查詢早於 backend 定案）」。
  - P2：更正 App.vue 清理慣例的錯誤描述；新 listener 以 disposer 清理，既有未
    清理 listener 標為待清理、不入本 scope。
- rev1（2026-08-25）：初版。
