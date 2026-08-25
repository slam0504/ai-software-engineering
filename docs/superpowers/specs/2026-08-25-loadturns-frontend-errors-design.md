# LoadTurnsBefore 前端錯誤處理 設計

Owner 開票（2026-08-24，P1；2026-08-25 補充要件：「不只接住 Promise error，
還必須讓同一 session 能實際重試載入」）。四要件（owner 凍結）：
1. `pin()` 與 `loadOlder()` 都要在 store 層統一攔截錯誤。
2. 錯誤必須顯示，但不得沿用會清除 session busy 的 `pushError`。
3. `ErrRegistryUncertain` 回到前端前須帶既有穩定判別片語。
4. 首次 hydrate 失敗後要有明確的同 process 重試路徑，不能因 view 已存在而
   永久 early return。

**修訂記錄**：
- rev4（2026-08-25，owner review 1 P2）：Go 測試注入改帶探針文字的 uncertain
  錯誤並斷言 `dir-sync-probe` 留存（守住「cerr 訊息不遺失」契約——裸哨兵注入
  抓不到拿掉 `%v` 的 mutation）；§2 用語校正——`%v` 保留診斷文字非 error
  chain，本票需求只到文字層。
- rev3（2026-08-25，spec gate 2 P1／6 P2）：createdView 凍結寫法改「先寫入再
  讀回 proxy」（原 snippet 在 Pinia reactive 下比對恆 false——gate 實測 7 紅
  ／修正版 376 綠；比對與 applyToView 都須用 proxy）；SettingsBar component
  test 補入＋兩呼叫端判定基準（persistentPins vs pins）與 `at`／不另 setFocus
  凍結；Go 測試改 stubRegistry 兩條新測試（原 chmod／dirSync 提案不可實作）；
  wrap 保留 cerr；§3/§5 不可達註解矛盾擇 §3；補反向與缺格測試（view 存在不
  重 pin、persistentPins 不回退、pin 不向外 reject＋pane 1 仍還原、loadOlder
  ＋片語→latchSeq、loadOlder 斷言的 focused／busy 前置）；雙 pane 不需測的
  理由明文。
- rev2（2026-08-25，owner review 2 P1／1 P2）：呼叫端「零改動」不成立——
  SessionList/SettingsBar 已釘選分支只 setFocus 不再 pin，加「已釘選但無 view
  時重新 pin」；pin 的 view 建立保留實例身分、catch/成功皆比對 `createdView`
  （A→B→A 競態不需 unpin UI 即可達、既有「不可達」註解一併更新）；busy 測試
  前置設 true（區辨 pushError 誤用）；補 A→B→A deferred 兩變體與 SessionList
  component test。
- rev1：初版。

## 1. 問題與現狀

- `pin()`（session.ts:404-427）：`isNew` 時先建 view 再 `await load(...)`——
  reject 成 unhandled rejection、pane 全空白無訊息；且 `views[wsid]` 已存在，
  再點同一 session 走 `if (!isNew) return` **永久早退**、無法重試（replay
  reliability spec §3 已明文的過渡態，本票關閉它）。
- `loadOlder()`（session.ts:456-473）：同樣無 try/catch，reject 成 unhandled
  rejection（PaneView.vue:54 的 `await s.loadOlder(...)` 也不接）。
- 錯誤出口現況：`pushError` 會 `m.busy = false` ＋寫進 pane timeline（owner
  明禁：載入失敗不是 session 錯誤、不得動 busy）；`pushNotice`（session.ts:663）
  走 app-wide notice lane、經 `applyNotice` 計數——**含 latch 片語時 `bumpError`
  會撥 `latchSeq`**（session.ts:576-583 doc），機制既有、直接可用。
- 判別片語：前端唯一判定手段是錯誤字串含 `REGISTRY_UNCERTAIN_MARK`
  （session.ts:177，「session registry 上一次寫入的結果不確定」＝app.go
  `errRegistryUncertain` 片語，Go 測試 `TestErrRegistryUncertainKeepsUIMarker`
  守著不漂移）。但 `loadTurnsBefore` 清旗標失敗（closeout C3）回的是
  **wsregistry 原始字串**（「registry 上一次寫入的 commit 結果不確定」）——
  **與前端片語不同**，latch 判別在此路徑失效。這是 integration review 8-9
  Minor 3／closeout final review Minor 2 的同根缺口，本票 backend 半邊收斂。

## 2. Backend：清旗標錯誤對齊判別片語

`loadTurnsBefore` 合併分支的非哨兵 `cerr`（rebuild_orchestrator.go，經
`noteRegistryUncertainErr` 稽核後回傳）目前原樣傳播。凍結：

- `errors.Is(cerr, wsregistry.ErrRegistryUncertain)` 時，回傳前 wrap 成含
  app 層片語的錯誤：`fmt.Errorf("%w（load turns wsid=%s：清 legacy 旗標時
  發現：%v）", errRegistryUncertain, wsid, cerr)`——沿用既有
  `errRegistryUncertain` 變數（app.go:727-729，前端片語的唯一來源），**不另
  造第二種片語**；**`cerr` 的錯誤訊息不得遺失**（gate P2；owner rev3 校正
  用語：`%v` 保留的是診斷**文字**進使用者可見錯誤，不是把 `cerr` 留在 Go
  error chain——本票需求只到文字層；哨兵可追溯性由 `errRegistryUncertain`
  自身的 `%w` 提供，不需第二個 `%w`）。wrap 後 `errors.Is(err,
  wsregistry.ErrRegistryUncertain)` 仍成立（errRegistryUncertain 本身以 `%w`
  包住同一哨兵——gate 核實，既有 uncertain 覆蓋表測試不紅；該 subtest 的
  「要回同一個錯誤」註解語意會變，plan 順手修）。
- 非 uncertain 的 cerr（一般 persist 失敗）原樣傳播（前端顯示原因即可，
  不需 latch 判別）。
- **不**在 `loadTurnsBefore` 入口加 `registryUncertain()` 早退：latch 擋的是
  寫入，讀路徑照常服務（§6a 清旗標只是讀路徑上的窄寫入，撞 latch 時本就由
  cerr 路徑回錯）——integration review 建議的「早退慣例」在讀路徑不適用，
  此為本 spec 的明確裁定。
- 稽核不變（`noteRegistryUncertainErr("legacy_flag_clear", ...)` 已在）。

## 3. Frontend：store 層攔截與重試路徑

### pin() 首載（要件 1／2／4）

`isNew` 段建立 view 時**保留實例身分**（owner review rev1 P1——A→B→A 時序不
需要 unpin UI 就可達，session.ts:416 的「不可達」註解不成立、一併更新）：

```ts
if (isNew) this.views[wsid] = newView()
const createdView = this.views[wsid]   // ← 先寫入、再從 state 讀回 proxy
...
try {
  const envs = await load(wsid, '', TURN_WINDOW_SIZE)
  if (this.views[wsid] !== createdView) return   // 舊請求 resolve：不套到新實例
  for (const e of envs ?? []) applyToView(createdView, e)
} catch (e) {
  if (this.views[wsid] === createdView) delete this.views[wsid]  // 僅刪自己建立的
  this.pushNotice(...)
}
```

**Proxy 陷阱（spec gate rev2 P1，實測凍結）**：身分比對與 `applyToView` 都
**必須用「寫入後從 state 讀回」的 proxy**，不得用 `newView()` 的原始回傳值
——Vue reactive 的 get trap 會把讀回物件包成 proxy，`this.views[wsid] !==
原始物件` **恆為 true**（gate 逐字套用原 snippet：比對恆早退、envelope 永不
套用、view 永不刪、既有 7 條測試紅；proxy 讀回版：全套 376 條綠）。對 raw
物件 push 也不觸發重繪，所以兩處都得用 proxy。

- **實例比對**：catch 僅在 `this.views[wsid] === createdView` 時刪除——
  pin(A) 載入中 → 同 pane 切 B（releaseView 刪 A 的 view）→ 切回 A（建第二個
  view）→ 第一個請求稍後 reject 時，無條件 delete 會誤刪**較新的** view。
  成功結果同樣只套用到同一實例（舊請求 resolve 不得疊到新 view——順帶關掉
  既有註解自承的「舊 envelope 不去重疊上去」缺口）。
- 刪除後回復 `isNew` 前置狀態——重新觸發 pin 載入就是完整重試。
- notice 照 §3 錯誤出口段。

### 呼叫端調整（owner review rev1 P1——「呼叫端零改動」不成立）

SessionList（SessionList.vue:39-43，判定基準 `persistentPins`）與 SettingsBar
（SettingsBar.vue:20-24，判定基準 `pins`——兩者基準本就不同、各自維持）對
**已釘選**的 session 只 `setFocus`、不再呼叫 `pin()`——view 被清掉後再點同
一張卡片，真實 UI 仍永久空白。凍結：兩個呼叫端的「已釘選」分支改為
`if (!s.views[wsid]) { void s.pin(at, wsid) } else { s.setFocus(at) }`——
**`idx` 用已釘選的那一格 `at`**（gate P2：用 `s.focused` 會把卡片搬格並
releaseView 掉該格原 session）；重新 pin 的分支**不另呼叫 `setFocus`**
（`pin()` 本身已含 focused／durableFocusPane／persistLayout／unread 歸零，
再呼叫會多送一次 SetLayout 落盤）。view 存在時行為不變（反向有測試守）。
重試路徑因此成立於真實 UI，不只 store 層。
- `pushNotice(t('store.turnsLoadFailed', { wsid, error }))`——app-wide lane、
  不動 busy、不掛錯 pane；錯誤字串含 backend 原文，latch 片語（§2 對齊後）
  經 `applyNotice`→`bumpError` 照常撥 `latchSeq`（要件 3 的前端半邊零新機制、
  沿用既有接線）。
- pins／persistentPins **不回退**：釘選是使用者的選擇且已持久化（pin() 的
  既有註解明文「持久化的是使用者的選擇，沒有理由等 transcript 載完」）；
  pane 顯示空 view 骨架＋notice 錯誤，重啟後 restoreLayout 會再試載入。

### loadOlder()（要件 1／2）

包 try/catch。失敗時：view 與既有內容**全部保留**（分頁失敗不得毀掉已載入
的 transcript）、`pushNotice(t('store.olderTurnsLoadFailed', {...}))`；重試
路徑天然存在——使用者再捲到頂就再觸發（cursor 仍在，無早退問題）。

### 其餘呼叫端

`PaneView` 的 `await s.loadOlder(...)` 零改動；SessionList／SettingsBar 除上段
「已釘選但無 view 時重新 pin」外亦零改動。攔截收斂在 store 層（要件 1），
呼叫端不再產生 unhandled rejection（`pin` 內 async 錯誤已被吃掉、不再向外
reject）。

### i18n

新增 `store.turnsLoadFailed`／`store.olderTurnsLoadFailed` 兩個 key（zh-TW
＋en 皆補，沿用既有 `store.*` 命名）。

## 4. 測試策略

- **Go（§2；gate P2 改寫——原提案不可實作：chmod 只能造 stepWrite、
  `ForceStepHookForTest` 跨 package 取不到、latch 單向與既有測試的「修復後
  重試成功」斷言互斥）**：用 `stubRegistry`（app_wsid_test.go:140 已實作
  `ClearLegacyTranscript`、`mutateErr` 驅動）比照 app_registry_uncertain_test.go:328
  的手法，**兩條新測試**：`mutateErr = fmt.Errorf("%w: dir-sync-probe",
  wsregistry.ErrRegistryUncertain)`（owner rev3 P2——注入裸哨兵時「拿掉格式
  中的 `%v`」的 mutation 不會被抓）→ `LoadTurnsBefore` 錯誤字串含前端片語
  （與 `TestErrRegistryUncertainKeepsUIMarker` 同字面）、**含 `dir-sync-probe`**
  （cerr 訊息不遺失的守門）、且 `errors.Is` 哨兵仍成立；
  `mutateErr = errors.New(...)` 一般錯誤 → 不含片語（不誤標）。既有 chmod
  測試不動。
- **Frontend（vitest，沿用既有 session store 測試手法——registryUncertain.test.ts
  的 binding stub 慣例）**：
  - pin 首載 reject → view 被清（`views[wsid]` 不存在）、notice lane 有錯誤、
    **`busy` 前置設為 `true` 且事後仍為 `true`**（owner review rev1 P2：預設
    false 時把 pushNotice 誤改成 pushError 仍可能過——view 已刪時 pushError 也
    走 notice lane＋errorSeq，唯 busy 副作用可區辨兩者）、`errorSeq` 增；再次
    `pin` 同 wsid → binding 被第二次呼叫（**重試真的發生**——mutation：拿掉
    `delete views[wsid]` 則第二次呼叫不發生、此斷言紅）。
  - **A→B→A 競態（deferred promise 手法，前例 PaneView.test.ts:146-164；兩
    變體）**：pin(A) 的 load 懸置 → 同 pane pin(B)（releaseView 刪 A view）→
    pin(A) 第二次（建新實例、第二個 load 懸置）→ 讓**第一個** load (a)
    reject：新實例**不得被刪**（`views[A]` 仍存在且為第二實例）；(b)
    resolve：舊 envelope **不得**套到新實例。**雙 pane 情境不測**（gate 核
    實：A 同時釘兩格時 releaseView 早退不刪、第二次 pin 走 `!isNew` 早退，
    第二實例根本不會產生——競態不存在，補測只會是恆綠案例）。
  - **pin 不向外 reject**：pane 0 的首載 reject 時，`restoreLayout` 的
    `await this.pin(...)` 迴圈不得中斷——**pane 1 仍還原**（mutation：catch
    內重拋 → 此測試紅；這是 catch 存在的結構性證明）。
  - **persistentPins 不回退**：pin 失敗後 `persistentPins[idx]` 仍為該 wsid
    （§3 凍結「釘選是使用者選擇」的反向鎖）。
  - **loadOlder＋latch 片語 → `latchSeq` 增**（§6a 清旗標在向上分頁的最舊頁
    也會觸發，不只首載）；loadOlder 測試的「既有內容不變」斷言**前置須把該
    wsid 釘進 focused pane**（gate：`pushError` 寫進 `views[focusedWsid]`，
    非 focused 時誤用 pushError 斷言仍假綠），busy 前置同樣設 true。
  - **Component tests（SessionList＋SettingsBar 各一）**：第一次 pin 失敗
    （view 已清）→ 再點同一張卡片／同一入口 → binding 被第二次呼叫（真實
    UI 重試——store 直呼 pin 蓋不到「已釘選」分支；兩個呼叫端判定基準不同
    （persistentPins vs pins），缺一則該入口的重試無守門）。**反向**：view
    存在時再點只 setFocus、binding **不得**被再次呼叫（gate 實測：拿掉守衛
    改無條件 pin，現行 49 條測試全綠——此反向是唯一的守門）。SessionList
    的 mount／stub 慣例沿用 SessionList.test.ts 既有手法（vi.mock wailsjs
    ＋ `s.setBindings`）。
  - pin 首載 reject 且錯誤含 `REGISTRY_UNCERTAIN_MARK` → `latchSeq` 增。
  - loadOlder reject → 既有 timeline 內容不變、notice 有錯誤、再呼叫 loadOlder
    → binding 再被呼叫（重試）。
  - 反向：load 成功路徑無 notice、行為與現狀一致（既有 pin/loadOlder 測試不破）。
- 迴歸：`npm --prefix frontend run test`＋`run build`（vue-tsc）；Go 側 targeted
  ＋全套。

## 5. 非目標

- 不加 per-pane 錯誤 UI／重試按鈕（notice lane＋重新點選即重試已滿足四要件；
  更豐富的錯誤 UX 另議）。
- 不動 `pushError` 語意。（session.ts:419-423 的「不可達」註解**要**更新——
  §3 已凍結：A→B→A 不需 unpin UI 即可達；rev2 此處與 §3 矛盾，rev3 擇 §3。）
- backend 其他錯誤路徑（scan error／turn-read）字串不變——它們不是 latch、
  顯示原因即可。
