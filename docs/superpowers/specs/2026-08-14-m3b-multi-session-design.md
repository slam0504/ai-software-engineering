# M3b — 多 session 工作區設計

- 日期：2026-08-14
- 狀態：rev4，待 closure review（rev1 六項設計 P1、rev2 六項生命週期 P1、rev3 四項 P1＋兩項 P2 已整合；rev4 修五項 P1：CommitCreate × rollback 雙失敗收斂為 create-degraded latch、recorder error latch 的 in-process 復原入口、Codex 受控復原的錄流 ownership 交棒（`wire_log_id` 前置配置、非 probe-scoped）、runtime replay index 重建與並行 append 的交接、鎖外收斂至凍結上限後才取鎖——同時消除 TOCTOU 與無界鎖內掃描）
- 上游依據：app plan §7（M3 列「多 session 並看」延後項）；M3a 範圍切分時的 M3b 定義（同 provider 多 session、資源上限、事件重放視窗化、session 導覽與生命週期契約）
- 前置：M3a ✅（`cfa5a20`）＋M3a.1 ✅（`4ac2d78`）
- 明確不含：閒置回收、pane 比例拖曳／N-pane、task 綁定（M4）、removed session 的 reopen、M3a.1 九張後續票（獨立 triage）、ACP（候選里程碑 §7.1）

---

## 1. 目標與成功條件

1. **同 provider 多 session**：每 provider 最多 **4 個 session slot**（共 8），同時保留並可並行執行（每 session 至多一個進行中 turn，不新增全域排程器）；超限 `ErrSessionLimit` fail loud、前端顯示 `n / 4`，不自動終止任何 session。
2. **雙 pane 並看**：固定 50/50 並排、兩邊皆 live（串流／Timeline／狀態／unread 持續更新）、**單一 focused pane** 可操作（composer、快捷鍵、SettingsBar 的 End／Terminate／New 只作用於它）；點擊切換焦點不卸載、不重設捲動。
3. **重啟成本與完整 audit 事件量脫鉤**：釘選 lazy replay（20 完整 turn 尾端視窗＋分頁）、非釘選僅 metadata、per-WSID byte-offset replay index；events.jsonl 唯一權威、完整 append-only 不裁切。
4. **M2／M3a／M3a.1 全功能零回歸**：Gate 1/2/TCA、SpecAssist/PlanAssist、收件匣、evidence runner 不受 session 模型改造影響。

### Slot 語意（凍結）

「session slot」包含：active、已 End 但保留 resume identity、重啟後恢復的 session。**只有使用者明確「關閉／移除工作區 session」才釋放名額**；稽核事件與 recordings 永久保留。上限為凍結常數（不可設定）。**無閒置回收**——閒置回收會擴張出全新生命週期契約（回收時機、approval 等待中處置、stdin/recorder/broker 收尾、自動 resume、失敗阻擋 shutdown），留待取得實際資源數據後另議。

## 2. 架構總覽

**Manager 改造採方案 A（裁決一）**：擴充現有單一 `appcore.Manager` 為 session registry——slots 從 per-provider 改 per-WSID＋per-provider 上限計數；**保留單一 emit mutex 與檔案級 event_id 嚴格遞增不變量**；submission coordinator 與 lifecycle 狀態機 per slot 複用。RecordingLease 歸屬 **sessionHost**（Claude 每 session 一份實體錄流 lease）；Codex 的 transport 錄流 lease 為 **connection-level**（見 §3.4），不隨 session slot 複製。不採 per-session Manager 實例（event_id 全域單調需在聚合層重造，風險高於收益）。

**Codex 錄流採 connection-wide wire log（裁決二，見 §3.4）**：不重做 `codex.Conn` 為多 sink。

## 3. 契約（實作前凍結）

### 3.1 WSID 與 session 建立交易

`workspace_session_id`（WSID，ULID）為 host-side 穩定 session identity；provider session_id／thread id 為 attachment 資訊。

1. **WSID 必須在任何 provider 啟動與 `BeginNewSessionSubmit` 之前產生並取得 slot reservation**。建立為三段交易（避免與既有 End lifecycle token 混淆，命名 `CreateToken`）：

```go
ReserveSession(provider string) (wsid string, tok CreateToken, err error)
    // 單一 mutex 內：檢查該 provider 4-slot 上限（超限回 ErrSessionLimit）
    // → 產 WSID → reservation。slot count 自 reservation 當下即含該筆，
    //   第 5 個併發建立不可穿透。
CommitCreate(tok CreateToken) error   // reservation → 正式 slot
AbortCreate(tok CreateToken) error    // 退回 reservation、釋放名額
```

App 編排凍結為：`beginAppTxn → Manager.ReserveSession → sessionRegistry.Put ＋ atomic persist → Manager.CommitCreate → endAppTxn`；registry persist 失敗則 `AbortCreate`（slot 退回，fail loud）。shutdown 以 `beginAppTxn` 為柵欄：shutdown 開始後拒絕新 app txn，故不會插入 reservation 與 persist 之間。

**CommitCreate 失敗補償（凍結）**：對有效且未過期的 reservation，`CommitCreate` 為單一 mutex 內的純 in-memory 狀態轉移，**在此窗口不得失敗**（app txn 柵欄已排除 shutdown 插入）。防禦性地，若仍回 error，App 必須**回滾 registry**（移除該 entry＋atomic persist）＋`AbortCreate`，以 `errors.Join` 合併回報——不得留下「registry 已持久化、Manager 無正式 slot、重啟卻復活」的 durable ghost。

**若 durable rollback 本身也失敗（凍結）**：此時 Manager 與 registry 已無法證明一致，**不得**再 `AbortCreate`（記憶體釋放名額、磁碟仍有 entry，同樣是 ghost），也**不得**逕自把該 entry 包裝成 dormant（Manager 側無可信狀態可據）。凍結為：**保留該 reservation 與名額** → 該 provider 進入 **`session-create-degraded` latch**，拒絕後續 session 建立、以 `errors.Join` fail loud；**既有 session 不受影響，可正常執行與收尾**。latch **僅由 app restart 解除**——重啟後以 durable registry 為權威還原成 dormant（§3.2.2），名額歸位。degraded latch 期間該 reservation **不因逾期自動釋放**。注入式 Commit 失敗、以及 Commit 失敗 × rollback persist 失敗，皆為必測項（§5.1）。

2. **已成功建立（committed）的 workspace session，後續 provider 首次啟動失敗時保留為 dormant**，允許重試或移除；不得自動刪除。
3. slot 的 `provider` 建立後 **immutable**。
4. 後續 API（BeginNewSessionSubmit／BeginSubmit／EndSession…）**只收 WSID**，provider 從 slot 取得；未知 WSID 回 `ErrSessionNotFound`——**廢除現行 `slotLocked()` 讀取時隱式建立 slot 的行為**（隱式建立＋上限＝查詢未知 WSID 吃名額，不可保留）。
5. **Conversation lane 的每個 Envelope 必填 `workspace_session_id`**（contract additive 欄位）——含 user、state_change、approval、result 與 `session:done`，自 `BeginSubmit` 起。
6. Workspace lane 的 Gate event、SpecAssist／PlanAssist 隔離 one-shot **不屬 session slot**：維持無 WSID、不計入上限。
7. Event 的 provider 與 slot provider 不符時 **fail loud**（拒 emit＋錯誤）。

### 3.2 Session registry、restore v2 與 legacy 遷移

新 session registry 持久化檔命名 **`workspace-sessions.json`**（`sessions.json` 現為 Claude resume registry，不得沿用）。

1. **Registry 只保存 durable metadata（凍結白名單）**：`schema_version`（=2）、WSID、provider、resume identity、task label、view boundary、created／removed（tombstone，見 §3.6）、pane pins／focused pane。**starting／active／ending／busy／approval pending 等 runtime state 一律不持久化**——app crash 後這些狀態已失去真實 owner，不可原樣恢復。
2. **啟動還原**：所有非 removed session 一律以 **dormant** 還原——無 host、無 pending approval。
3. **Stuck busy 解除**：replay 發現某 WSID 最後一個 turn 未完成時，append 一筆帶 WSID 的 `stream_error`（app restart interrupted turn），由 reducer 追發 `state_change=failed` 結束該 turn；下一次送訊息走 resume start。**修復必須冪等**：該 WSID 末筆已是 app-restart `stream_error`／failed 時不得重複 append（crash 於修復中途、重啟重跑不得疊加）。
4. **啟動修復順序（凍結）**：載入／遷移 registry → 還原 Manager dormant slots → 驗證或重建 replay index → 偵測 incomplete turns → 經 Manager emit `stream_error`（reducer 追發 failed state）→ **才開放 UI 與 provider 啟動**。crash 後重跑此序列須收斂到相同狀態（§5.3 必測）。
5. **Legacy 遷移**（自現行 provider-keyed `restore.json`）：只遷移既有 `viewStartEventID` 之後的 view window（不得把該 provider 全部歷史丟入 legacy session）；沿用該 entry 最新 `resumeSessionID` 與 `taskID`；**只有存在 resume identity、taskID 或 view window 事件時才建立** legacy session，空 entry 不建立、不佔 slot。
6. Legacy WSID **只產生一次並原子持久化**；registry 帶 `schema_version: 2` 與 **migration marker**——遷移完成後不得再由舊 restore entry 建出第二枚 WSID；重啟不得換新 WSID。migration 持久化失敗時 **fail loud，不啟動 provider**。
7. Registry 是 **UI／恢復 metadata，不是第二份事件歷史**；事件權威永遠是 events.jsonl。

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

- **Claude**：每個 WSID 獨立常駐子行程＋**獨立 unix socket 與 MCP config 檔**（現行共用 `approval.sock`／`mcp.json`，第二個 session 會覆寫第一個——路徑帶 WSID；session remove 時一併清除）；resume registry（sessions.json）擴為 per-WSID 記錄。
- **Codex**：**共用單一 Conn**；建立 `threadID／turnID → WSID` dispatcher；**pending start request 在 response 到達前也能歸屬 WSID**（保留既有 completed-before-response 保證）；`pendingApproval` 必須攜帶 WSID；notification、approval、timeout、decision 全部依 WSID Emit——**廢除 `currentRunner()` 路由**。
- Approval dialog 依 WSID 路由（§3.6.4）。

### 3.4 Codex 錄流形態（裁決二）

現行 `codex.Conn` 僅容許單一 recorder sink（第二次 BeginRecording 回 recording already in progress）——「單 app-server＋多 thread＋每 session lease」不可直接成立。**凍結為 connection-wide 錄流**，lifecycle 全凍結：

1. **每個 production app-server generation 一份 always-on wire log**（transport 層完整原文）：handshake 前即開始；**每次 app-server restart 開新 `wire_log_id`（新 generation）**。不採 recordCase reference count 啟停。
2. **Finalize 順序（凍結）**：wire log 錄到 server 生命終點——一律 **terminate → wait／`Conn.Done` → finalize**，關閉期間最後的 frame 不得漏錄；server 意外死亡同樣在 `Conn.Done` 後 finalize。shutdown 總序（§3.6.5）與 B1 同此順序。
3. 另建**可重建的 frame index**，key 至少含 **`wire_log_id`＋`direction`＋`requestID`**（direction 不可省——client request 與 server request 的 ID 可能相撞）＋threadID／turnID→WSID 歸屬。
4. Session 級錄流證據為**有序的 `[]WireSegmentRef`**（每段 `{wire_log_id, frame range}`）——同一 WSID 可在 B1、server restart 或 app restart 後跨 generation 延續，單一 wire_log_id 不足以涵蓋（跨兩 generation 且不混入他 session frame 為必測項，§5.2）；既有 recordCase 轉為該 view 的 **label**，不控制 recorder attach。
5. **無法歸屬的 frame 仍寫入 wire log、WSID 留空**——不得丟棄。
6. **Recorder 寫入失敗即時 fail loud**：error latch 後立刻發 workspace 通知並**拒絕新 Codex session**；既有 session 允許 bounded 收尾。latch **只擋一般 session 建立，不擋第 7 條的受控復原**——否則解除條件（新 generation）永遠不可達。**latch 只在新 generation 的 recorder 掛載、handshake 與發布全部成功後解除**——不因時間或重試次數自動解除；其中任一步失敗即 dispose 新 server 並**保留 latch**。不需為錄流故障重啟整個 app。
7. **B1 handshake probe／受控 app-server restart 採最小方案（凍結）**：存在 live Codex host 或 in-flight turn 時**拒絕執行**；只有 dormant session 不阻擋。順序凍結為 **收乾 live host → terminate → wait（`Conn.Done`）→ finalize 舊 generation wire log（§3.4.2）→ 配置新 `wire_log_id` → 起新 server → 掛 recorder → handshake → 發布**；全段在 `Single.WithExclusive` 的單一互斥交易內完成。此路徑同時是第 6 條 recorder error latch 的 **in-process 復原入口**。不提供臨時 server 選項。

   **錄流 ownership（凍結——M3b 需改動現行 probe，不得直接沿用）**：現行 `RunHandshakeProbe` 成功路徑在發布前 `StopRecording` ＋ `CloseWith(ProcessStillRunning)`（`internal/codex/probe.go`），屬 **probe-scoped 錄流**，與第 1 條的 production always-on wire log 不相容。M3b 凍結為：**`wire_log_id` 在掛 recorder 之前配置**（不是發布成功才建立）；handshake 與發布成功後 **recorder ownership 轉交 connection-level lease**——不 Stop、不 Close，**直到該 server 終止才依 §3.4.2（terminate → wait／`Conn.Done`）finalize**。probe 與 production 啟動共用同一段編排（實作可抽共用 orchestration 或參數化「發布後是否交棒」），**失敗路徑維持現行語意**：Terminate → Wait → dispose，不留未發布 server。**失敗的 generation 同樣保留其 `wire_log_id` 與收尾證據**（exit code／stderr tail 寫入該 generation 的 meta），不得因未發布而丟棄或回收 ID。
8. 不採「每 WSID 實體錄流檔」：需重做 Conn 為多 sink＋frame attribution，工程量與風險更大。Claude 維持每 session 實體錄流（每 session 本就獨立子行程）。

### 3.5 Replay index

**目錄形狀（凍結）**——不採單一交錯 JSONL（混合 8 session 時啟動仍需全掃，無法降低成本）：

```
.workbench/replay-index/
  checkpoint.json          # events.jsonl 已索引 byte offset＋最後 event ID
                           # ＋每個 WSID 的 open_turn_start_offset
  <wsid>.turns.jsonl       # 每 WSID：完整 turn 的 audit byte range＋首末 event ID
```

1. 每 WSID 的 index 只記**完整 turn** 的 audit byte range 與首末 event ID；從檔尾反向讀最近 20 筆；分頁 cursor 用該 index 的 byte offset。
2. **Offset 來源（凍結）**：audit sink 的 append 回傳 `AppendReceipt{startOffset, endOffset, eventID}`，index 一律以 receipt 為準，不得自行推算 offset。
3. **Crash consistency（凍結）**：正常路徑先 audit write、再更新 index；audit sink 不為快取強迫逐事件 fsync，故 **crash 後 index 可能落後也可能超前——兩者一律視為不可信快取並修復**（落後：掃 audit suffix 補索引；超前／offset 超界／event ID 不符：依第 6 條處置）。
4. **Index 失敗不得影響 audit**：不回滾 audit、不讓 provider turn 失敗；改 latch `replay-index degraded` 狀態、checkpoint 不前移，待重建。**防遞迴（凍結）**：先 latch、後通知，**每個 degraded generation 只發一次** workspace 通知；通知事件照常進 audit，但 latch 期間 index writer 已停止寫入（含該通知本身），故通知不會再觸發 index 失敗——解除 latch 以成功重建為準（重建與並行 append 的交接見第 7 條）。
5. checkpoint 以 **atomic rename** 寫入、clean shutdown 時 Sync；啟動時以 audit offset＋末筆 event ID 驗證。checkpoint 保存**每個 WSID 的 `open_turn_start_offset`**——否則 checkpoint 越過未完成 turn 後，result 到達時無法重建該 turn 起點。
6. **損壞處置分級（凍結）**：index 檔**尾端** corruption → truncate 至最後一筆 valid record 續用；**中段** corruption、offset 超界或 event ID 不符 → quarantine＋全量重建＋復原通知。不可遺失或猜測事件。
7. **Runtime 重建交接（凍結）**：第 4 條的 degraded latch 與第 6 條的中段 quarantine，其重建都發生在 **app 運行中**——provider turn 仍持續 append audit，故不得「邊掃邊解除 latch」（掃描高水位之後、writer 接回之前的事件會留下缺口）。序列凍結為：

   1. **bulk 重建**至初始高水位（不持鎖）。
   2. **鎖外反覆 catch-up** 至最新 audit end，直到**殘量**（最新 audit end − 已索引 offset）低於**收斂上限**——byte 與 record 雙上限、凍結常數（不可設定）。bulk 期間 audit 仍在增長，故一次補掃不保證收斂，必須迭代。**迭代本身有固定嘗試次數／時間界限（凍結常數）**：界限內仍未達標即**中止本輪重建**（見下方收斂分支），不得無限迭代或 busy-loop。
   3. 殘量達標**才**取得 emit／index mutex。
   4. 鎖內重讀 final audit end：**殘量若又超限，立即 unlock 回到第 2 步重試**——**不得在鎖內硬掃**。
   5. 殘量符合上限 → 鎖內完成最後一段補掃 → 前移 checkpoint → 接回 writer → 解除 latch → unlock。

   **最後一段補掃與接回必須在同一把鎖內**——若在鎖外補掃完才去搶鎖，補掃結束到取得鎖之間仍可 append，會留下缺口（TOCTOU）。**持續高速 append 下的兩種不收斂分支，一律以「保留 degraded latch ＋ 中止本輪 ＋ backoff 稍後重試」收束**：(a) 鎖外 catch-up 在嘗試界限內**始終未達標**（本條第 2 步，從未進入取鎖階段）；(b) 達標取鎖後**殘量又超限**、反覆 unlock 重試至嘗試界限（本條第 4 步）。重試從已索引位置續掃，不重跑 bulk。此收束安全的前提是 **index 是快取、audit 不受影響**（第 4 條）；**不得為求收斂在鎖內做無上限掃描，也不得在鎖外 busy-loop**。落在重建窗口內的事件必須**恰好索引一次**（以 `AppendReceipt` 的 event ID／offset 去重，不得產生重複 record），barrier 為必測項（§5.5）。啟動期重建走 §3.2.4 序列（開 UI 前完成），無並行 append，不適用本條。
8. Current incomplete turn 從 `open_turn_start_offset` 之後的 audit suffix 直接取得（不入 index）。
9. **Turn boundary 定義（凍結）**：turn 自 **canonical user message** 起；以該 result 導出的 **terminal `state_change=done|failed`** 止（含 failed turn）；`stream_error` 同樣導出 failed 並結束 incomplete turn；`session:done` 不單獨構成 turn；**沒有 canonical user message 的 init／unknown 事件屬 session metadata，不得猜成一個 turn**。未完成 turn 獨立載入、不得從 turn 中間截斷。
10. 稽核匯出、contract replay 等仍讀完整 events.jsonl，不受 UI 視窗限制。index 是快取，不是第二份事件格式。

### 3.6 移除、approval 與 shutdown 語意

1. **移除＝durable tombstone，不是刪除 record**：registry 保留該 WSID 並記 `removed_at`＋`remove_reason`——否則 replay index 重建看到 audit 中的 WSID 會把已移除 session 復活。removed 不計 slot、不顯示、不自動恢復；M3b 不提供 reopen。legacy session 移除後同樣以 tombstone＋migration marker 防止再次遷入。
2. **釋放名額時點（凍結）**：teardown、lease finalize 與 tombstone durable persist **全部完成後**才遞減 provider count；Remove × New 競態以同一 ownership token 序列化；任一步失敗**保留 slot**（fail loud）。
3. **待核可時移除**：處理該 WSID 的**全部**顯示中與排隊 approval——全部 best-effort 以 `reason=session_removed` fail-closed deny → `errors.Join` 收集錯誤 → bounded teardown；只有全部收尾＋tombstone persist 成功才釋放 slot。deny 部分失敗時**仍 terminate provider**，但保留 dormant slot 供重試移除；任一步失敗不得靜默移除。
4. **Approval 路由**：一律按 WSID；來源在另一 pane → 自動切 focus 至該 pane 再顯示對話框；**來源未釘選 → transient secondary presentation**——次要 pane 暫時顯示來源 session（persistent pin 永不被 transient view 改寫）。**恢復原釘選的觸發（凍結全列）**：allow、deny、timeout、dismiss、session remove、shutdown。多筆待核可 FIFO promotion 按 approval ID／WSID。
5. **Shutdown 總序（凍結）**：

```
shuttingDown=true（拒新 app txn）
→ 停 watcher／reclaim assists／evidence
→ snapshot 全部 sessionHost 與 pending approvals
→ 全部 approval fail-closed deny
→ 全部 session 先 interrupt／terminate
→ per-session teardown 並行（goroutine＋WaitGroup 收斂；不得逐 session 串行）
→ 全部 Codex session host 收完
→ terminate／wait 共用 app-server（`Conn.Done`）
→ finalize Codex wire log（§3.4.2：錄到 server 生命終點，關閉期間最後 frame 不漏）
→ Manager.Close（含 pending audit flush）
→ replay index flush／checkpoint／Close
→ session registry Sync
```

   Manager.Close 可能 flush pending conversation events，故 **replay index 不得在 Manager.Close 之前停止接收**。並行 teardown 泛化現行雙 provider 模式（4 個 Claude 卡死時最壞仍為單一 bounded window，不乘以 session 數）。

### 3.7 並看與焦點（前輪裁決收錄）

最多釘選 2 個 session 並排；其餘留 session 清單。兩 pane 持續接收串流／Timeline／狀態／unread。任一時刻恰一個 focused pane：composer、Enter/Shift+Enter、SettingsBar 的 End/Terminate/New 只作用於它；點另一 pane 即切焦點，不卸載、不重設捲動。A 送出的 turn 繼續執行、切 B 可再送另一 turn（backend 多 session 並行、UI 單組輸入控制）。初版固定 50/50。

### 3.8 視窗化語意（前輪裁決收錄）

啟動只重建兩個釘選 pane：各載入最近 **20 個完整 turn**（依 §3.5.9 定義）＋未結束的目前 turn。其餘 session 僅載 durable metadata＋由 runtime 事件推導的即時狀態（busy／待核可為 in-memory 推導值，不持久化，見 §3.2.1）；非釘選不保留 transcript 於記憶體、背景事件仍更新狀態與 unread。釘選／切入時 lazy load 尾端視窗；向上捲到頂以每次 20 turn 分頁（`before_event_id` cursor＋event_id 去重）。解除釘選釋放 transcript、保留 metadata＋已讀狀態＋捲動錨點。

## 4. 前端

- **SessionList**（左欄新 section）：**只顯示既有 session**＋`n / 4`（per provider）＋「建立」按鈕——不畫固定 8 張空卡。每卡：provider 徽章、task label、狀態、unread、busy、待核可標記；操作：釘選至 pane、開新、關閉／移除（確認對話，說明稽核保留）。
- **雙 pane**：PaneView 綁 WSID；focus 樣式明確；SC2 StatusBar 顯示 focused pane 的 session 資訊。
- session store 泛化為 per-WSID lane（現 per-provider views 擴展）；gateRouting 依 WSID 分流 conversation lane。

## 5. 測試（production-path barrier 必備清單）

1. 同 provider **5 個並行 ReserveSession，恰 4 個**取得 reservation（`-race`）；Reserve → registry persist failure → AbortCreate 退回名額；**注入式 CommitCreate 失敗 → registry 回滾＋AbortCreate、重啟無 durable ghost**；**CommitCreate 失敗 × rollback persist 失敗 → 不 AbortCreate、名額保留、`session-create-degraded` latch 拒絕新建（既有 session 不受影響），重啟後由 registry 還原為 dormant 且名額歸位**；Reserve × shutdown barrier（拒新 app txn）。
2. Codex 兩 thread 的 notification／approval／completed-before-response／錄流 frame 歸屬**不串線**；wire recorder error latch → 拒新 Codex session、**但不擋受控 restart**；**latch 下的完整復原路徑：收乾 live host → terminate → wait（`Conn.Done`）→ finalize 舊 generation（§3.4.2 順序）→ 配置新 `wire_log_id` → 新 server 掛 recorder＋handshake＋發布全成功才解除 latch；掛 recorder 或 handshake 失敗 → dispose 新 server＋latch 保留＋不留未發布 server**；**發布成功後 recorder 未被 Stop／Close，wire log 持續錄到 server 終止才 finalize（非 probe-scoped）**；**失敗的 generation 仍保留 `wire_log_id` 與收尾證據**；app-server generation restart 開新 wire_log_id；**同一 WSID 橫跨兩個 generation 的 `[]WireSegmentRef` 完整且不混入他 session frame**；B1 在 live host／in-flight turn 時拒絕。
3. Legacy migration 的 crash／restart 得到**相同 WSID**（原子持久化＋migration marker）；incomplete turn restart → `stream_error`＋failed，不殘留 busy／pending approval；**啟動修復序列 crash 後重跑冪等**（不重複 append stream_error、收斂到相同狀態）。
4. Remove × New 同 token 競態；removed tombstone 重啟與 index rebuild **不復活**；shutdown × Start barrier；8 session 含一個卡死 Claude → shutdown 總時間仍為單一 bounded window。
5. Replay index 三種 crash：落後（補掃）、超前（不可信快取修復）、checkpoint 越過 open turn（`open_turn_start_offset` 重建）；index 尾端 truncate 續用 vs 中段 quarantine 重建；**degraded latch 每 generation 只通知一次、通知事件不觸發遞迴**；**runtime 重建 × 並行 append barrier：事件固定插在「bulk 補掃已完成、emit／index mutex 尚未取得」的窗口 → 由鎖內最後一段補掃涵蓋，恰好索引一次、無缺口亦無重複 record**；**sustained-append barrier 兩條**：(a) **鎖外 catch-up 始終無法達標** → 嘗試界限內中止本輪、保留 degraded latch、backoff 重試；(b) **達標後取鎖時再次超限** → 立即解鎖重試、鎖內處理量不超過凍結收斂上限。兩條均須驗證**不 busy-loop、不做無界掃描**，且後續成功重建時事件**無缺漏無重複**。
6. 雙 pane 已滿時未釘選 session approval 的 transient routing（顯示 → allow/deny/timeout/dismiss/remove/shutdown 各觸發恢復原 pin）。
7. 另：event_id 檔案級單調在 8 session 並行 turn 下不變量測試；舊 journal（無 WSID）legacy 歸屬 fixture；每 session 單一 in-flight turn 拒絕第二筆。

## 6. 驗收

1. 實機：雙 provider × 多 session 並行（A 執行中切 B 送出）、雙 pane 並看與焦點切換、unread、approval 跨 pane／未釘選路由。
2. 實機：8/8 上限拒絕＋`n / 4` 顯示＋關閉釋放名額。
3. 實機：重啟——釘選 lazy 恢復（20 turn 視窗）、非釘選 metadata、向上分頁、index 落後補掃；index 損壞注入 → 重建＋通知；未完成 turn 重啟 → failed 解除 busy。
4. 舊 workspace（M3a 時代 events.jsonl）升級——legacy session 正確歸屬、resume 可用、重複啟動不產生第二枚 WSID。
5. M2／M3a／M3a.1 迴歸抽驗（Gate 流程、TCA、收件匣、assists）。
6. 收尾 gate：`go vet ./...`／`go test -race ./... -count=1`／vitest／frontend build／`wails build`。

## 7. 風險與待驗證假設

1. Claude 多常駐子行程的實際資源占用（4 個 session 的 RAM/CPU）——驗收時實測記錄，供未來 idle parking 評估。
2. Codex 單一 app-server 對多 thread 並行 turn 的實際行為（pin 0.146.1 wire 層併發語意）——早期 spike 驗證（implementation plan 列前置 task）。**失敗裁決（凍結）**：此為 M3b 並行能力的架構前提——probe 失敗即 **implementation NO-GO、退回 spec gate 由 owner 重裁**；實作端**不得**自行改成 provider-wide 串行或多 app-server 等替代架構。
3. Claude per-WSID socket／MCP config 的檔案數與清理（session remove 時一併清）。
4. 既有 M1.5 恢復測試基線大改（restore v2）——舊測試語意遷移不減。
5. Audit sink append 介面改為回傳 `AppendReceipt` 屬 M1 核心路徑小幅改動——以既有 audit 測試全綠＋event_id 單調不變量測試護欄。
