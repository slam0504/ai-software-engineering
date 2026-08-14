# M3b — 多 session 工作區設計

- 日期：2026-08-14
- 狀態：rev1，待 closure review（設計審閱六項 P1 已整合：WSID／Manager API、restore v2＋legacy 遷移、App ownership registry 化、Codex 錄流形態裁決、replay index 目錄形狀、移除／approval／shutdown 語意）
- 上游依據：app plan §7（M3 列「多 session 並看」延後項）；M3a 範圍切分時的 M3b 定義（同 provider 多 session、資源上限、事件重放視窗化、session 導覽與生命週期契約）
- 前置：M3a ✅（`cfa5a20`）＋M3a.1 ✅（`4ac2d78`）
- 明確不含：閒置回收、pane 比例拖曳／N-pane、task 綁定（M4）、M3a.1 九張後續票（獨立 triage）、ACP（候選里程碑 §7.1）

---

## 1. 目標與成功條件

1. **同 provider 多 session**：每 provider 最多 **4 個 session slot**（共 8），同時保留並可並行執行（每 session 至多一個進行中 turn，不新增全域排程器）；超限 `ErrSessionLimit` fail loud、前端顯示 `n / 4`，不自動終止任何 session。
2. **雙 pane 並看**：固定 50/50 並排、兩邊皆 live（串流／Timeline／狀態／unread 持續更新）、**單一 focused pane** 可操作（composer、快捷鍵、SettingsBar 的 End／Terminate／New 只作用於它）；點擊切換焦點不卸載、不重設捲動。
3. **重啟成本與 session 數脫鉤**：釘選 lazy replay（20 完整 turn 尾端視窗＋分頁）、非釘選僅 metadata、per-WSID byte-offset replay index；events.jsonl 唯一權威、完整 append-only 不裁切。
4. **M2／M3a／M3a.1 全功能零回歸**：Gate 1/2/TCA、SpecAssist/PlanAssist、收件匣、evidence runner 不受 session 模型改造影響。

### Slot 語意（凍結）

「session slot」包含：active、已 End 但保留 resume identity、重啟後恢復的 session。**只有使用者明確「關閉／移除工作區 session」才釋放名額**；稽核事件與 recordings 永久保留。上限為凍結常數（不可設定）。**無閒置回收**——閒置回收會擴張出全新生命週期契約（回收時機、approval 等待中處置、stdin/recorder/broker 收尾、自動 resume、失敗阻擋 shutdown），留待取得實際資源數據後另議。

## 2. 架構總覽

**Manager 改造採方案 A（裁決一）**：擴充現有單一 `appcore.Manager` 為 session registry——slots 從 per-provider 改 per-WSID＋per-provider 上限計數；**保留單一 emit mutex 與檔案級 event_id 嚴格遞增不變量**；submission coordinator、lifecycle 狀態機、RecordingLease per slot 複用。不採 per-session Manager 實例（event_id 全域單調需在聚合層重造，風險高於收益）。

**Codex 錄流採 connection-wide wire log（裁決二，見 §3.4）**：不重做 `codex.Conn` 為多 sink。

## 3. 契約（實作前凍結）

### 3.1 WSID 與 Manager API

`workspace_session_id`（WSID，ULID）為 host-side 穩定 session identity；provider session_id／thread id 為 attachment 資訊。

1. **WSID 必須在任何 provider 啟動與 `BeginNewSessionSubmit` 之前產生並取得 slot reservation**：

```go
CreateSession(provider string) (wsid string, token SessionToken, err error)
// 單一 mutex 內：檢查該 provider 4-slot 上限（超限回 ErrSessionLimit）→ 產 WSID → reservation。
```

2. slot 的 `provider` 建立後 **immutable**。
3. 後續 API（BeginNewSessionSubmit／BeginSubmit／EndSession…）**只收 WSID**，provider 從 slot 取得；未知 WSID 回 `ErrSessionNotFound`——**廢除現行 `slotLocked()` 讀取時隱式建立 slot 的行為**（隱式建立＋上限＝查詢未知 WSID 吃名額，不可保留）。
4. **Conversation lane 的每個 Envelope 必填 `workspace_session_id`**（contract additive 欄位）——含 user、state_change、approval、result 與 `session:done`，自 `BeginSubmit` 起。
5. Workspace lane 的 Gate event、SpecAssist／PlanAssist 隔離 one-shot **不屬 session slot**：維持無 WSID、不計入上限。
6. Event 的 provider 與 slot provider 不符時 **fail loud**（拒 emit＋錯誤）。

### 3.2 Restore v2 與 legacy 遷移

現行 `restore.json` 為 provider-keyed＋`view_start_event_id` 限定現行 view。升級規則（凍結）：

1. **只遷移既有 `viewStartEventID` 之後的 view window**——不得把該 provider 全部歷史丟入 legacy session。
2. 沿用該 entry 的最新 `resumeSessionID` 與 `taskID`。
3. **只有存在 resume identity、taskID 或 view window 事件時才建立 legacy session**；空 entry 不建立、不佔 slot。
4. Legacy WSID **只產生一次並原子持久化**；重啟不得換新 WSID。
5. 遷移持久化失敗時 **fail loud，不啟動 provider**。
6. 新的 **session registry**（持久化檔）保存：WSID、provider、resume identity、task label、slot 狀態、兩個 pinned pane 與 focused pane——它是 **UI／恢復 metadata，不是第二份事件歷史**；事件權威永遠是 events.jsonl。

### 3.3 App ownership registry 化

單例改造不只 Manager——App 現持有單一 broker、Claude session、runner、track 與 lease，全部 registry 化：

```go
sessionHosts map[WSID]*sessionHost
type sessionHost struct {
    provider          string
    // process/runner、teardown（OnceValue）、pump、recording lease、
    // provider session/thread ID、turn tracker、approval broker（Claude）
}
```

- **Claude**：每個 WSID 獨立常駐子行程＋**獨立 unix socket 與 MCP config 檔**（現行共用 `approval.sock`／`mcp.json`，第二個 session 會覆寫第一個——路徑帶 WSID）；resume registry（sessions.json）擴為 per-WSID 記錄。
- **Codex**：**共用單一 Conn**；建立 `threadID／turnID → WSID` dispatcher；**pending start request 在 response 到達前也能歸屬 WSID**（保留既有 completed-before-response 保證）；`pendingApproval` 必須攜帶 WSID；notification、approval、timeout、decision 全部依 WSID Emit——**廢除 `currentRunner()` 路由**。
- Approval dialog 依 WSID 路由（§3.6.3）。

### 3.4 Codex 錄流形態（裁決二）

現行 `codex.Conn` 僅容許單一 recorder sink（第二次 BeginRecording 回 recording already in progress）——「單 app-server＋多 thread＋每 session lease」不可直接成立。**凍結為 connection-wide 錄流**：

1. Codex 錄流改為**一份 connection-wide append-only wire log**（transport 層完整原文）。
2. 另建**可重建的 frame index**：`requestID／threadID／turnID → WSID`。
3. Session 級錄流證據以 **WSID filter／view** 表達（讀 wire log＋index 投影）；不再是每 session 實體檔。
4. **無法歸屬的 frame 仍寫入 transport log、WSID 留空**——不得丟棄。
5. B1 handshake probe 在存在 active Codex session 時**拒絕執行**，或改用獨立臨時 server——**不得搶走 production recorder**。
6. 不採「每 WSID 實體錄流檔」：需重做 Conn 為多 sink＋frame attribution，工程量與風險更大。Claude 維持每 session 實體錄流（每 session 本就獨立子行程）。

### 3.5 Replay index

**目錄形狀（凍結）**——不採單一交錯 JSONL（混合 8 session 時啟動仍需全掃，無法降低成本）：

```
.workbench/replay-index/
  checkpoint.json          # events.jsonl 已索引 byte offset＋最後 event ID
  <wsid>.turns.jsonl       # 每 WSID：完整 turn 的 audit byte range＋首末 event ID
```

1. 每 WSID 的 index 只記**完整 turn** 的 audit byte range 與首末 event ID；從檔尾反向讀最近 20 筆；分頁 cursor 用該 index 的 byte offset。
2. **順序凍結：audit append 成功 → index append**；crash 只會讓 index 落後，不會超前。
3. 啟動時驗 checkpoint：落後只掃 audit suffix 補索引；**offset 超界、event ID 不符或 index 中段損壞 → quarantine＋全量重建＋復原通知**——不可遺失或猜測事件。
4. Current incomplete turn 從最後 completed boundary 之後的 audit suffix 直接取得（不入 index）。
5. **Turn boundary 定義（凍結）**：從 canonical user message 起、至 result／state done 止；init 前事件歸入同一 turn；未完成 turn 獨立載入、不得從 turn 中間截斷。
6. 稽核匯出、contract replay 等仍讀完整 events.jsonl，不受 UI 視窗限制。index 是快取，不是第二份事件格式。

### 3.6 移除、approval 與 shutdown 語意

1. **釋放名額時點（凍結）**：teardown、lease finalize 與 session-registry durable remove **全部完成後**才遞減 provider count；Remove × New 競態以同一 ownership token 序列化；任一步失敗**保留 slot**（fail loud）。
2. **待核可時移除**：先以 `reason=session_removed` fail-closed **deny 該 approval**，再 bounded teardown；任一步失敗不得靜默移除。
3. **Approval 路由**：一律按 WSID；來源在另一 pane → 自動切 focus 至該 pane 再顯示對話框；**來源未釘選 → transient secondary presentation**——次要 pane 暫時顯示來源 session（保留原 pin，決議後恢復原釘選）；多筆待核可 FIFO promotion 按 approval ID／WSID。
4. **Shutdown（凍結為並行收斂）**：snapshot 全部 sessionHost → 全部先 interrupt／terminate → goroutine **並行** CloseSequence → WaitGroup 全收斂 → Manager.Close——泛化現行雙 provider 並行模式，**不得逐 session 串行**（4 個 Claude 最壞 4 倍 timeout）。

### 3.7 並看與焦點（前輪裁決收錄）

最多釘選 2 個 session 並排；其餘留 session 清單。兩 pane 持續接收串流／Timeline／狀態／unread。任一時刻恰一個 focused pane：composer、Enter/Shift+Enter、SettingsBar 的 End/Terminate/New 只作用於它；點另一 pane 即切焦點，不卸載、不重設捲動。A 送出的 turn 繼續執行、切 B 可再送另一 turn（backend 多 session 並行、UI 單組輸入控制）。初版固定 50/50。

### 3.8 視窗化語意（前輪裁決收錄）

啟動只重建兩個釘選 pane：各載入最近 **20 個完整 turn**＋未結束的目前 turn。其餘 session 僅載 metadata（provider、標題、狀態、unread、最後活動時間、resume identity、busy／待核可）；非釘選不保留 transcript 於記憶體、背景事件仍更新狀態與 unread。釘選／切入時 lazy load 尾端視窗；向上捲到頂以每次 20 turn 分頁（`before_event_id` cursor＋event_id 去重）。解除釘選釋放 transcript、保留 metadata＋已讀狀態＋捲動錨點。

## 4. 前端

- **SessionList**（左欄新 section）：**只顯示既有 session**＋`n / 4`（per provider）＋「建立」按鈕——不畫固定 8 張空卡。每卡：provider 徽章、task label、狀態、unread、busy、待核可標記；操作：釘選至 pane、開新、關閉／移除（確認對話，說明稽核保留）。
- **雙 pane**：PaneView 綁 WSID；focus 樣式明確；SC2 StatusBar 顯示 focused pane 的 session 資訊。
- session store 泛化為 per-WSID lane（現 per-provider views 擴展）；gateRouting 依 WSID 分流 conversation lane。

## 5. 測試（production-path barrier 必備清單）

1. 同 provider **5 個並行 CreateSession，恰 4 個**在 spawn 前取得 ownership（`-race`）。
2. Codex 兩 thread 的 notification／approval／completed-before-response／錄流 frame 歸屬**不串線**。
3. Legacy migration 的 crash／restart 得到**相同 WSID**（原子持久化驗證）。
4. Remove × New 同 token 競態；shutdown × Start barrier。
5. 雙 pane 已滿時未釘選 session approval 的 transient routing（顯示→決議→恢復原 pin）。
6. 另：event_id 檔案級單調在 8 session 並行 turn 下不變量測試；replay index 落後補掃／中段損壞 quarantine 重建；舊 journal（無 WSID）legacy 歸屬 fixture；每 session 單一 in-flight turn 拒絕第二筆。

## 6. 驗收

1. 實機：雙 provider × 多 session 並行（A 執行中切 B 送出）、雙 pane 並看與焦點切換、unread、approval 跨 pane／未釘選路由。
2. 實機：8/8 上限拒絕＋`n / 4` 顯示＋關閉釋放名額。
3. 實機：重啟——釘選 lazy 恢復（20 turn 視窗）、非釘選 metadata、向上分頁、index 落後補掃；index 損壞注入→重建＋通知。
4. 舊 workspace（M3a 時代 events.jsonl）升級——legacy session 正確歸屬、resume 可用。
5. M2／M3a／M3a.1 迴歸抽驗（Gate 流程、TCA、收件匣、assists）。
6. 收尾 gate：`go vet ./...`／`go test -race ./... -count=1`／vitest／frontend build／`wails build`。

## 7. 風險與待驗證假設

1. Claude 多常駐子行程的實際資源占用（4 個 session 的 RAM/CPU）——驗收時實測記錄，供未來 idle parking 評估。
2. Codex 單一 app-server 對多 thread 並行 turn 的實際行為（pin 0.146.1 wire 層併發語意）——早期 spike 驗證（implementation plan 列前置 task）。
3. Claude per-WSID socket／MCP config 的檔案數與清理（session remove 時一併清）。
4. 既有 M1.5 恢復測試基線大改（restore v2）——舊測試語意遷移不減。
