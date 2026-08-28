# TaskRun／Gate 3／Forge 契約設計（B5）

> 版本：rev5（2026-08-28，design gate 第四輪 3 P1 收斂——required checks 一對一 coverage、latch 鎖序與線性化、runtime 部分失敗 fail-closed）
> 狀態：**待 design gate**
> 來源：Pre-M4 Readiness Backlog B5（rev5 估點版）；owner 裁決 #3（session 自動綁定不可變 snapshot）、#4（GitHub-first）、#5（Gate 3 六件綁定）、#6（DomainSpec 僅 shadow／explain）
> 範圍：**spec 級**——定義物件、生命週期與契約，不含實作；為 C1a／C1b／C1c 垂直切片與 B6 application seams 的設計依據。production 錨點以 2026-08-27 盤讀為準（rev3–rev5 新增錨點為 2026-08-28 盤讀），實作時引用前先驗 file:line 仍成立。

## 1. 目的

M4 的治理迴路要能回答並強制：「這一段實作是在**什麼核准上下文**中完成的，且該上下文在 Gate 3 決議當下**仍然成立**」。本 spec 定義三個物件承載它：**TaskRun snapshot**（不可變核准上下文）、**implementation session 綁定**（自動、不可繞過、可跨重啟恢復）、**Gate 3 決議契約**（綁定 manifest＋決議時重驗）。平台強制力（PR／required checks／ruleset）屬 forge 與 B2 範圍；Workbench 負責建立、注入、顯示、記錄與編排。

## 2. TaskRun snapshot（不可變物件）

### 2.1 欄位

TaskRun 於「選定 task、啟動 implementation」時建立，建立後**不可變**（append-only journal，沿 `internal/gate`／`internal/evidence` journal 先例；狀態變化以 transition op 追加——見 §3.4 狀態機——不改寫原 snapshot）：

| 欄位 | 內容 | 來源錨點 |
|---|---|---|
| `schema_version` | 固定 1（rev2 補——digest preimage 與 strict decode 的版本錨） | — |
| `task_run_id` | ULID | 新發 |
| `plan_id`／`task_id` | 對應 plan 與 task | Gate 2 subject `plan:<id>`（gate2.go:253-259）＋plan.tasks |
| `gate1_approval` | `{approval_id, record_digest}` | `gate.RecordDigest`（record_digest.go:12-19）——以 digest 錨定紀錄而非只記 ID，沿 TCA 先例（tca.go:238-240） |
| `gate2_approval` | `{approval_id, record_digest}` | 同上；record 傳遞性涵蓋五 binding digest |
| `selected_risk_tier` | 該 task 的核可風險層 | `ApprovalRecord.Metadata.RiskDecisions`——production 現況隻寫不讀，本 spec 明定讀取路徑＝`gate.Service.Lookup(approval_id)`（service.go:198-211）直取 record 後依 task_id 檢索 |
| `permission_manifest_digest` | 自 gate2 record 的 `permission_manifest` binding 抄出 | app.go:4988、4295-4307 |
| `tca_approval` | `{approval_id, record_digest}` | TCA record（subject `task:<plan_id>/<task_id>`，tca.go:394-404） |
| `expected_red_evidence`／`negative_control_evidence` | 各 `{evidence_id, evidence_run_digest}` | TCA record 的兩個 role-scoped `evidence_run` binding（tca.go:78-85）；digest 公式 `EvidenceRunDigest`（runner.go:274-281） |
| `oracle_surface_digest` | 自 TCA record 的 `oracle_surface` binding 抄出 | tca.go:78-85 |
| `test_commit` | TCA 兩筆 evidence 共同的 test_commit（rev2 補） | TCA 已驗證兩筆 evidence 的 test_commit 相同且 oracle_surface.ref==test_commit（tca.go:228-229、243-244）——自 record 抄出 |
| `implementation_base_commit` | TaskRun 建立當下 workspace HEAD 的 git OID | 建立時 rev-parse；錨定規則見 §2.2(3) |
| `created_at` | 建立時間 | — |
| `snapshot_digest` | 見 §2.3 | — |

抄錄欄位（selected_risk_tier、permission_manifest_digest、evidence digests、oracle_surface_digest、test_commit）的**權威仍是上游 record**；抄錄目的為 Gate 3 讀取便利與獨立 staleness 檢查，建立時必須與上游 record 逐欄一致，不一致即建立失敗（fail loud，不得靜默取上游現值）。

### 2.2 建立前置：currentness（backlog 6a）

TaskRun 建立當下，透過 `gate.Service.List()`（Reconcile-before-Project，service.go:191-196；於 §3.2 臨界區 A 內執行，其 Reconcile durable 寫入屬持鎖寫入路徑，符合 §3.2(0) 單一寫入者不變式）驗證：

1. gate1、gate2、TCA 三者對應 subject 的核可皆為 `Active`。
2. TCA 的 `gate2_approval` binding 所指 approval_id 與本 TaskRun 綁定的 gate2 approval **同一筆**（防錯位）。
3. **基準錨定（rev2 強化——防 TCA 後、TaskRun 前混入未受驗 commit）**：`test_commit` 必須為 `implementation_base_commit` 的**祖先**，且 `git diff test_commit..implementation_base_commit` 的 tree 差異**必須為空**（允許空 diff 的 fast-forward／merge commit 形狀；任何內容差異即拒絕建立）。放寬規則（如允許特定路徑）屬未來 spec 修訂，rev2 不預留。
4. worktree 乾淨（沿 Gate 2 submit host boundary 慣例）。

任一不成立→拒絕建立，錯誤具名（哪一件、哪種原因）。git 讀取錯誤（rev-parse fatal 等）≠不成立——fail closed 為「無法建立」，不得誤報為條件不符（沿 R13 慣例）。

### 2.3 snapshot_digest（rev3 凍結 TaskRunPayloadV1 具體欄位）

`snapshot_digest = "sha256:" + hex(sha256(canonical_json(TaskRunPayloadV1)))`。**TaskRunPayloadV1 欄位與序（rev3 凍結——rev2「欄位皆單值」不成立，approval／evidence 欄位為巢狀物件，改為逐欄含巢狀 key 序凍結）**：

```
schema_version, task_run_id, plan_id, task_id,
gate1_approval{approval_id, record_digest},
gate2_approval{approval_id, record_digest},
selected_risk_tier, permission_manifest_digest,
tca_approval{approval_id, record_digest},
expected_red_evidence{evidence_id, evidence_run_digest},
negative_control_evidence{evidence_id, evidence_run_digest},
oracle_surface_digest, test_commit, implementation_base_commit, created_at
```

頂層與巢狀 key 一律依上列字面序輸出；payload 不含 `snapshot_digest` 自身；本 schema 無陣列欄位。strict decode——未知欄位（含巢狀物件內）拒絕、`schema_version != 1` 拒絕（沿 `internal/domainspec` canonical／strict decode 先例）。

## 3. Implementation session 自動綁定（owner 裁決 #3）

### 3.1 附著點、持久化與基數（rev2 強化）

- Runtime 附著於 `sessionHost`（session_host.go:49-105，write-once-before-publish 契約）。
- **持久化（rev2 補——sessionHost 為 runtime-only，重啟即失）**：`wsregistry.Entry`（store.go:67-77）新增 `task_run_id`＋`snapshot_digest` 兩欄位，與 entry 一同落盤；重啟後 resume 依此恢復綁定。
- **基數凍結**：一個 TaskRun 同時至多綁定一個 WSID；一個 WSID 至多承載一個 TaskRun；**不允許重新綁定**（owner 裁決 6d）——TaskRun 與 session 的關係只有「建立時綁定」與「resume 回到原綁定」兩種。
- 無 TaskRun 的一般 session 仍可啟動，但不得標記為 implementation session、其產出不得進 Gate 3（以 Entry 的 task_run_id 空／非空區分）。

### 3.2 建立交易與 crash boundary（rev3 重寫——單一寫入者不變式＋權威階層＋repair matrix）

TaskRun 建立到 session publish 之間的競態（List → Lookup → git 讀 → 寫 TaskRun → 啟動 Claude → publish host），以下列交易語意關閉：

0. **Gate journal 單一寫入者不變式（rev3 凍結）**：任何會 append gate journal 的路徑——含 `Service.Reconcile()` 產生的 stale transition（service.go:244-261）——**必須持有 `workflowMu`**。純讀取入口改用 **detect-only 投影**：以與 Reconcile 相同的 policy 檢查計算有效狀態並疊加於 `Project()` 結果（呈現結果與 reconcile 後相同），但不 append journal；durable stale transition 延後至下一個持鎖寫入路徑落盤。現況違反此不變式、實作時須逐一遷移的入口（2026-08-28 盤讀）：`GateList`（app.go:5716-5730，僅 beginTxn/shutMu）、`SubmitPlanForApproval`（app.go:5019）、`ValidateTestCommit`（app.go:5433）、`EvidenceCommitCandidates`（app.go:5480）、`SubmitTestContract`（app.go:5564）、`GateDecisionContext`（app.go:5660）、`PlannerAssist`（app.go:6475，取鎖前即呼叫）。已持鎖對照組：`gateDecide`（app.go:5823-5825）、`reconcileGate1NotifyOnly`（app.go:2646-2657）、`RunEvidence`（app.go:5235-5251）。捨棄之替代案：(a) 七個讀取入口全取 `workflowMu`——UI 讀取被序列化在寫入臨界區之後；(b) generation CAS——gate journal 與 TaskRun journal 為兩個獨立 durable store，無單一交易可提交。
1. **Durable ordering（rev3 調整——`claude.Start` 移出臨界區）**：
   - (i) **臨界區 A**（`workflowMu`，沿 gateDecide 先例 app.go:5823-5824）：完成 §2.2 全部前置驗證後 append `taskrun_created`（snapshot 本體，durable）。
   - (ii) 鎖外：組裝 session 資源（broker／socket／MCP config；沿 startClaude committed-rollback 慣例，app.go:7328-7335）。
   - (iii) **臨界區 B**（`workflowMu`）：currentness 重驗（重跑 §2.2 第 1-2 項）→ 通過即 append `taskrun_bound{wsid}`。在 (0) 不變式下，重驗與 bound 提交之間**不可能**有任何 gate journal 寫入介入——此即原子性保證。
   - (iv) 鎖外：`claude.Start` → publish host。wsregistry Entry 的 `{task_run_id, snapshot_digest}` 於 **`commitSessionIdentity`**（app.go:1181-1188——StartSession Accept 成功後、claude／codex 兩 provider 共同掛點，app.go:6901／6921）寫入，沿 store 私有 `mutate()` 慣例（store.go:359-373）新增寫入方法；四步原子落盤契約 store.go:182-218。**rev4 錨點更正（修 rev3 P2）**：claude `registry.Bind`（registry.go:57-70）寫的是獨立的 sessionID→{cwd, wsid} 反查表 JSON，與 wsregistry 是不同 store，不得作為本欄位掛點；`SetResume`（app.go:8996）為 Claude init event 專屬、僅覆寫 ResumeSessionID，亦非本欄位入口。
   - 殘餘窗口：git repo 外部狀態（commit 遭 GC／branch 刪除）無法納入臨界區；由 §4.1 staleness 形狀與 §5.3 決議時重驗承接。
2. **權威階層（rev3 凍結）**：TaskRun journal 是綁定的**唯一權威**；wsregistry Entry 的 `{task_run_id, snapshot_digest}` 是 **derived cache**（供 resume 快速定位），可自 journal 重建。兩者不一致一律以 journal 為準修復 Entry；不得反向以 Entry 補寫 journal。
3. **Repair matrix（startup repair 逐分割點；rev3 補）**：

   | 分割點 | 觀測形狀 | 修復 |
   |---|---|---|
   | created 後、bound 前 crash | journal：created 非終態、無 bound | append `taskrun_aborted`（終態；snapshot 保留供審計，不可再綁定） |
   | bound 後、Entry 落盤前 crash（或 Entry 寫入失敗） | journal：bound；Entry 無綁定欄位或不存在，**且 Entry 非 tombstoned**（RemovedAt 空） | 「bound 無活 session」→ §3.5 resume；session 重建時依 journal 回填 Entry |
   | Entry tombstoned、journal 為 bound 且無 `taskrun_abandoned` | §3.6 journal-先行序被違反或 legacy 殘留（tombstone＝RemovedAt 非空，store.go:310-322，**無復活路徑**：Put／mutate 一律拒絕，store.go:291-306／359-373） | **不得依「Entry 遺失」列重建、不得 resume**：append `taskrun_abandoned` 收斂＋corruption audit event |
   | `taskrun_abandoned` 已 append、移除六步未完成即 crash | journal：abandoned；Entry 未 tombstoned 或中間步驟殘留 | startup repair 重新驅動移除流程至 tombstone_persist 完成（六步凍結順序 app.go:7112） |
   | Gate 3 approved 落盤後、`taskrun_completed` append 前 crash 或 append 失敗 | gate journal：gate3 approved record（subject `taskrun:<ULID>`）；TaskRun journal：仍 bound | repair（startup＋runtime 觸發點，§4.2(4)）依 gate3 record 確定性補 append `taskrun_completed`；程序未重啟期間由 pending-completed 旗標阻擋衝突 transition（dominance 規則見 §3.4） |
   | Entry 有綁定欄位、journal 無對應 bound | 逆向分裂（正常排序下不應出現） | fail loud：落 corruption audit event、該 session 不得視為 implementation session；修復＝清除 Entry 綁定欄位（journal 權威），不得偽造 bound |
   | Entry 與 journal 的 snapshot_digest 不一致 | 資料毀損或錯繫 | fail loud，拒絕 resume（沿 §3.5） |
   | `claude.Start` 失敗（bound 已 append、未 crash；現況此時尚無 Entry，app.go:7379-7383） | bound、無活 session、session 資源已 rollback | **統一走 §3.5 resume-to-original**：bound 且從未成功建立 session 者，「resume」＝為原綁定重新建立 session（前置：非 STALE）。不轉 aborted、不建新 TaskRun——消除 rev2「Start 失敗轉 aborted」與狀態機 `bound →（stale｜completed）` 的矛盾；`aborted` 唯一來源為 created 未達 bound |

### 3.3 注入內容（C1a 實作面，spec 凍結語意）

1. **核准上下文前置**：內容**一律自 gate2 record 的 `base_commit` binding 所指 commit tree 讀取**（rev2 凍結——不得讀 worktree 現值）：plan 經 `PlanLoader.LoadAt(plan_commit, plan_id)` 先例；spec／risk_policy／permission 檔自同一 commit tree 讀出後**重算 manifest digest**，與 gate2 record 對應 binding digest 逐一比對；任何 mismatch、commit missing、讀取錯誤→session 啟動失敗（fail closed），不建立部分上下文。
2. **Permission enforcement（rev2 凍結——安全面不得留給實作臨場決定）**：
   - Permission 檔案（`plan/permissions/**`，task 經 `permissions_ref` 引用，plan/types.go:41）現況為 opaque artifact——rev2 凍結**版本化 schema v1**：
     ```yaml
     permission_schema: 1
     allow: []   # Claude permission rule 字串列表，例 "Bash(go test ./...)"
     ask: []
     deny: []
     ```
   - **Strict decode**：未知欄位、缺 `permission_schema`、版本≠1 一律拒絕（session 啟動失敗）。
   - **Deterministic mapping**：SettingsJSON `permissions` 由 schema 欄位直接映射——`defaultMode: "default"`＋`allow`／`ask`／`deny` 三列表逐字帶入，列表內排序去重（字典序）後輸出；不做任何推導、合併或萬用字元展開。同一 manifest 內容必然產生 byte-identical SettingsJSON。
   - 讀取來源同 (1)：gate2 `base_commit` binding 所指 commit tree；重算 permission manifest digest（app.go:4295-4307 公式）必須等於 snapshot 的 `permission_manifest_digest`。
   - 既有 schema 前置：現行 repo 的 permission 檔需先遷移為 schema v1（C1a 範圍內的一次性遷移；遷移後 Gate 2 重送核使 digest 更新）。
3. **TaskRun 標記**：`task_run_id` 入 session env／MCP config（app.go:7309 既有機制），供 evidence 收集與 audit 歸屬。

### 3.4 TaskRun 狀態機（rev2 補；rev4 增 abandoned 與 completed 觸發時點）

`created →（bound | aborted）`；`bound →（stale | completed | abandoned）`；終態＝`aborted`／`stale`／`completed`／`abandoned`。全部以 journal transition op 表達；startup repair 負責 crash 殘留的收斂（§3.2(3)）。orphan 定義：`created` 未達 `bound`＝aborted；`bound` 無活 session＝可 resume，非 orphan。**rev3 澄清（owner 裁決已接受）**：`aborted` 唯一入口為 `created`；`claude.Start` 失敗不進 aborted（§3.2(3) 統一走 resume-to-original）。**rev4 補**：

- **`abandoned`（新終態）**：使用者移除 session 之明示放棄（§3.6），僅可自 `bound` 進入；不可 resume、不可再綁定、產出禁入 Gate 3；transcript／evidence 保留（同 §4.2(3) 審計原則）。
- **`completed` 觸發時點（rev4 凍結）**：Gate 3 approved 決議當下——`gateDecide` 的 `workflowMu` 臨界區內 `svc.CommitDecision(prepared)`（app.go:5854，臨界區形狀 app.go:5823-5854）成功後、同一臨界區內 append `taskrun_completed`。gate journal 與 TaskRun journal 間的 crash split 由 §3.2(3) repair matrix 依 gate3 record 確定性收斂。completed 後 session 之後續產出不再屬於該 TaskRun（該 TaskRun 不可再送 Gate 3）。
- **Completed dominance 與 pending-completed（rev5 補——關閉「decision 已落、completed append 失敗、程序未重啟」窗口）**：
  - `CommitDecision` 成功之瞬間，該 TaskRun 進入 runtime **pending-completed** 旗標（與 freeze latch 同為持 `workflowMu` 寫入者所有之 per-TaskRun 狀態；monotonic，至 `taskrun_completed` 落盤成功才視為收斂）。旗標存續期間，凍結序列（§4.2）與移除流程（§3.6）**不得**對該 TaskRun append `taskrun_stale`／`taskrun_abandoned`（兩路徑皆持 `workflowMu`，進入時檢查旗標即互斥）。
  - `taskrun_completed` append 失敗或**結果不確定**（crash／`ErrRegistryUncertain` 類形狀）→ fail closed：一律視為「可能已提交」，旗標維持、阻擋持續；收斂由 §4.2(4) repair 觸發點（startup＋runtime 兩種）補 append。
  - **Dominance 規則**：gate3 approved record（subject `taskrun:<ULID>`）存在 ⇒ 該 TaskRun 的權威終態＝`completed`。若 journal 仍殘留其後的 `taskrun_stale`／`taskrun_abandoned` op（違反阻擋規則的 crash 殘留），projection 層面忽略之＋corruption audit event——以 projection 規則收斂，不需「自終態轉移」的非法 transition。

### 3.5 Resume（owner 裁決 6d，已定）

Resume **永遠只能回到原 TaskRun**：resume 時驗 `wsregistry.Entry` 的 `{task_run_id, snapshot_digest}` 與 journal 內 snapshot 一致；**不允許重新綁定**至其他 TaskRun。TaskRun 已 STALE → 依 §4.2 凍結語意（session 不可再產生新 turn）。`internal/claude.Registry` 的 resume 驗證面（registry.go:57-99）為現成掛點。**rev3 補（journal 權威，§3.2(2)）**：Entry 遺失或缺綁定欄位時，以 journal 的 bound 紀錄為準重建 Entry，不阻擋 resume；bound 且**從未**成功建立 session 者（Start 失敗、bound 後立即 crash），resume＝為原綁定重新建立 session——無 transcript 可續屬合法形狀，非錯誤。**§3.5 全部 resume 規則的前置**：Entry 未 tombstoned（tombstoned＝§3.6 abandoned 路徑，永不 resume）。

### 3.6 Session 移除與 TaskRun lifecycle（rev4 補——tombstone 契約）

現行 `RemoveSession` 為六步凍結順序（deny_approvals → teardown → lease_finalize → cleanup_files → … → tombstone_persist，app.go:7100-7209），tombstone（`Entry.RemovedAt`，store.go:310-322）**無任何復活路徑**（`Put`／`mutate` 一律拒絕，store.go:291-306／359-373）；`Removable()`（manager.go:328-347）不要求 session idle，活的 session 一樣可移除。tombstone 與 §3.2(3)「Entry 遺失依 journal 重建」是**不同形狀，不得混用**。rev4 凍結：

1. **允許移除 bound TaskRun 的 session，語意＝明示放棄**：移除流程於 deny_approvals **之前**，先在 `workflowMu` 臨界區內 append `taskrun_abandoned`（journal 先行、tombstone 在後——沿 §3.2(2) journal 權威排序原則）；之後接續既有六步。
2. **UI 前置**：前端既有兩段式 confirm（SessionList.vue:105-111）必須呈現「將放棄 TaskRun、不可恢復」語意；不得靜默放棄。
3. TaskRun 已為終態（stale／completed／aborted）或無 TaskRun 之一般 session：移除不追加 transition，逕走既有六步。**rev5 補**：TaskRun 處於 pending-completed（§3.4）→ 不得追加 `taskrun_abandoned`；移除須待 completed 收斂後依本項終態形狀處理。
4. Crash split 收斂見 §3.2(3)（tombstone-無-abandoned 收斂列、abandoned-移除未完成列）。

## 4. STALE／重驗生命週期

### 4.1 觸發形狀（backlog 6b——三形狀必須區分）

| 形狀 | 判定 | 效果 |
|---|---|---|
| **上游核可失效** | gate1／gate2／TCA record 轉 Stale／Superseded（Reconcile 既有機制），或 record_digest 對不上 | TaskRun → STALE |
| **binding digest 改變** | permission manifest 等現值重算與 snapshot 抄錄不一致（沿 §3.9 持續重算，gate2.go:207-232） | TaskRun → STALE |
| **commit missing** | `implementation_base_commit`／`test_commit`／上游 base_commit 於 repo 消失（rev-parse exit1） | TaskRun → STALE（fail closed：讀取錯誤≠missing，沿 R13） |
| **單純 HEAD 前移** | workspace／main HEAD 正常前進，上述皆未觸發 | **不得 STALE**（沿 TCA 錨點契約） |

### 4.2 STALE 處置（owner 裁決 6e，已定；rev4 補 transition protocol）

**Transition protocol（rev4 凍結——上游 gate stale、TaskRun journal stale、runtime freeze 三狀態的寫入順序與競態關閉）**：

1. **偵測與 ordering（rev5 調整——凍結方向 runtime 先行）**：durable stale 只產生於持 `workflowMu` 的寫入路徑（§3.2(0) 單一寫入者不變式下的全部寫入面：watcher `reconcileGate1NotifyOnly`、`gateDecide`、`RunEvidence`、TaskRun 建立臨界區）。同一臨界區內依序：(a) gate journal stale transition（既有 Reconcile）→ (b) **設 runtime freeze latch＋逐筆 deny 該 WSID 全部 pending approvals**（沿 `denyApprovalsForRemove` 先例，app.go:7076-7080；純 runtime 動作，不可失敗）→ (c) append `taskrun_stale`。**原則凍結：凍結方向的 runtime 效果不得以 journal append 成功為前置；放行方向（resume、Gate 3 決議、新 TaskRun 建立）必須以 durable 紀錄與重驗為前置。**步驟 (c) 失敗——含 journal degraded（write／sync 錯誤後拒絕後續 append 之既有語意，gate journal.go:11／56、evidence journal.go:23／125）——時 latch 已設、session 已凍結，fail closed 已達成；journal 收斂延後至 (4) repair。detect-only 讀取（§3.2(0)）不觸發本序列；偵測→凍結的收斂由 watcher 寫入路徑保證有界延遲。
2. **Freeze latch（monotonic；rev5 補唯一 owner 與設定端鎖序）**：
   - **唯一 owner**：App 層 per-WSID freeze state，寫入僅由持 `workflowMu` 的 freeze 序列執行（單一寫入者）；per-WSID 狀態掛點沿 `appcore.Manager` phase 狀態機先例（manager.go:328-347）。set-once，於該 TaskRun 生命週期內永不清除。
   - **設定端固定鎖序**：`workflowMu`（已持）→ 取 manager 鎖、設 turn-admission 旗標、釋放 → 取 `apprMu`、設 approval 旗標＋執行 (1)(b) 之逐筆 deny、釋放。latch 概念上一個、實作為**兩個同源 monotonic 旗標**（各在其鎖域，rev4「同一把鎖」之精確化——admission 與 approval 本就分屬 manager 鎖與 `apprMu` 兩鎖域，無法共用單鎖）；兩旗標設定間隙的唯一後果是「已 admit 的 turn 之後續 approval」——仍被 approval 旗標擋下，fail closed 不破。**除 freeze 設定端外，禁止任何路徑同時持有 manager 鎖與 `apprMu`**（現況兩鎖無交疊持有：`SendMessage` 僅 manager 鎖 app.go:6930-6976、`resolveApproval` 僅 `apprMu` app.go:6783-6810——此不變式凍結；設定端依固定順序取鎖且逐把釋放，無死鎖形狀）。
   - latch 為 runtime-only——重啟後於 startup repair 階段自 TaskRun journal＋detect-only currentness 重建（終態 `stale` ⇒ latch set），先於任何 resume 可能發生前完成。
3. **線性化點與 system-deny 豁免（rev5 精確化）**：新 turn 的線性化點＝`Manager.BeginSubmit` 成功返回（manager 鎖內完成旗標檢查後才 admit）。approval resolution 的線性化點＝`apprMu` 臨界區內「檢查旗標＋自 pending 取出」完成之瞬間——旗標檢查必在 `apprMu` 內、`p.resolve` 呼叫前完成（現況 resolveApproval 先解鎖再 `p.resolve` 的形狀維持：凡於旗標設定後進入 `apprMu` 者必見旗標；已於鎖內 admit 者，其鎖外 `p.resolve` 不再受旗標影響）。**旗標僅阻擋 `allow=true` 之核可與新 turn admission；`allow=false`（deny）永遠合法、不受旗標阻擋**——(1)(b) 的系統 deny 與使用者 deny 皆得執行（拒絕方向與 fail closed 一致，freeze 序列不會被自身 latch 擋住）。latch 設定前已 admit 的進行中 turn 屬下列凍結效果 (2) 受控收束；其後續 tool approval 仍被旗標擋下。
4. **Repair 觸發點（rev5——不限 startup）**：
   - (i) **startup repair**：gate journal stale 已落、`taskrun_stale` 未落 → 掃全部 bound TaskRun 以 detect-only 重算 currentness，不成立者補 append `taskrun_stale`＋set latch；`taskrun_stale` 已落、latch 未設 → latch 依 (2) 自 journal 重建。
   - (ii) **runtime repair（程序未重啟）**：每個持 `workflowMu` 寫入路徑進入時做收斂檢查——latch 已設而 `taskrun_stale` 未落（或 §3.4 pending-completed 殘留）且 journal 可寫 → 補 append；journal degraded 期間，所有依賴 append 的 workflow 操作（TaskRun 建立、gate decide、resume 放行）因既有拒絕語意自然 fail closed，而 latch 只增不減——runtime 凍結不因 journal 失敗而漏。
   - 兩向皆收斂，無需跨 store 交易。

**凍結效果**（TaskRun STALE 後**立即凍結 implementation session**）：

1. 拒絕新 turn 與新 tool approval（freeze latch fail closed，機制見上）。
2. 進行中 turn 取消或受控收束（不強殺進行中的工具呼叫、但不再核可新的）。
3. transcript 與 evidence **全數保留**（審計資產，不銷毀）。
4. 該 TaskRun 的產出**禁止進 Gate 3**；繼續實作必須建立**新 TaskRun**（上游重核後重新走 §2.2）。

### 4.3 Gate 3 pending request 的失效（owner 裁決 6c，已定）

採**決議時重驗**（§5.3），不做 forge 輪詢。但決議時重驗 mismatch 的 pending Gate 3 request **不得永久留 pending**——必須轉入明確的 `expired`／`stale` 終態（journal transition），需要重新送核產生新 request；UI 呈現失效原因。

## 5. Gate 3 決議契約（owner 裁決 #5）

### 5.1 綁定

Gate 3 approval request 的 bindings（沿 `gate.Binding{Kind, Role, Ref, Digest}`，types.go:15-20）：

1. `task_run`——ref `taskrun:<ULID>`、digest＝`snapshot_digest`。
2. `promotion_head`——PR head commit OID（格式沿 reGitOID，gate2.go:28-31）。
3. `main_base`——PR base（main）commit OID。
4. `oracle_surface`——digest 必須等於 TaskRun 抄錄值。
5. `required_check_manifest`（rev2 改——單筆 check run 無法證明 required set 完整；rev3 補確定性收斂；rev4 補 provider identity——名稱非權威 key）——canonical manifest 的 digest，manifest 內容：
   - `manifest_schema`：固定 1。
   - `required_checks[]`：**權威集合**＝forge ruleset／branch protection 定義的 required checks（決議時自 forge 讀取），每筆 `{context, app_id}`（巢狀 key 依此字面序；`app_id` 可空＝不限來源——對齊 branch protection API `required_status_checks.checks[]` 的 `{context, app_id}` 形狀），依 `(context, app_id)` 排序；forge 回傳重複 `(context, app_id)` →fail loud（不靜默去重）。
   - `runs[]`：依 `(context, required_app_id)` 排序（與 required_checks 同 key 序）、每 required check **恰一筆** `{context, required_app_id, run_name, run_app_id, run_id, head_sha, status, conclusion}`——**同時記錄 required 端 key 與實際 run 的 app identity**（rev5 修 null-key 語意：required_app_id 可空、run_app_id 為實際值，兩者可不同）。
   - **歸屬（attribution）規則**：run 可歸屬於 required check 當 `run_name == context` 且（`required_app_id` 為空，或 `run_app_id == required_app_id`）；`required_app_id` 為空且候選 run 來自多個不同 `run_app_id` →歸屬歧義，fail loud。**同 key 多次執行的收斂規則（current-effective run）**：取 `started_at` 最新者，tie-break `run_id` 較大者；驗證條件施加於收斂後的那一筆。
   - **驗證條件（rev5 改「集合相等」為一對一 coverage——(context, app_id=null) 與 (name, app_id=實際值) 依 key 比較必然不等，集合相等不可滿足）**：required_checks 與 runs **一對一對應（bijection）**——每一 required check 恰有一筆歸屬 run（無缺漏），runs 僅含歸屬於某 required check 者（無多餘），一筆 run 至多歸屬一個 required check（多重歸屬→fail loud）；全部 `conclusion == success`；全部 `head_sha == promotion_head`。
6. `review_evidence_provenance`（rev2 定權威；rev3 補 current-effective 規則與 implementation evidence schema）——canonical manifest 的 digest，兩節、各有具名權威：
   - **Review 節（權威＝GitHub PR review；rev4 補 reviewer eligibility——公開 repo 任何人可 review，僅具權限者影響合併）**：列表＝**每具效力 reviewer 至多一筆 current-effective review**，依 `reviewer_login` 字典序排序（unique key）；每筆 `{reviewer_login, permission, review_id, state, reviewed_head_sha, submitted_at}`（巢狀 key 依此字面序）。**Eligibility（rev4 凍結）**：決議時經 forge 逐 reviewer 查 collaborator permission，`permission ∈ {write, maintain, admin}` 才入 manifest；不具效力者**完全不入 manifest**——其 approval 不放行、其 CHANGES_REQUESTED 亦不阻擋（雙向修正）。CODEOWNERS／required approving review count 等深度 review policy 屬平台強制（B2 ruleset），Gate 3 不重演（merge-group 定位段同理）。**Current-effective 規則**：該 reviewer 全部 review 中 `state ∈ {APPROVED, CHANGES_REQUESTED, DISMISSED}` 且 `submitted_at` 最新者（tie-break `review_id` 較大者）；`COMMENTED`／`PENDING` 不改變有效狀態、不入 manifest。**驗證條件**：至少一具效力 reviewer 的 current-effective `state == APPROVED` 且 `reviewed_head_sha == promotion_head`；**且不存在任何具效力 reviewer 的 current-effective `state == CHANGES_REQUESTED`**；`DISMISSED`＝該 reviewer 無有效核可（不計入亦不阻擋）。〔owner 裁決（第三輪）：零 current-effective CHANGES_REQUESTED 保留為 fail-closed 預設，僅施加於具效力 reviewer。〕任意呼叫者自產 manifest 無效——決議時自 forge **重讀**比對（§5.3(5)）。
   - **Evidence 節（權威＝Workbench evidence journal；rev3 凍結 implementation evidence schema——修「任意 {evidence_id, digest} 列表、空集合或他人紀錄可通過」）**：
     - 新 journal record type **`implementation_evidence`**（schema v1）：`{schema_version: 1, evidence_id（ULID、exactly-once，journal.go:30-32）, task_run_id, kind, plan_commit, base_commit, head_commit, command, cwd, exit_code, stdout_digest, stderr_digest, started_at, finished_at, result}`。為 evidence journal record type 白名單（現況僅 `evidence_run`／`mutation`，journal.go:68-92）的 additive 第三種；現有 `EvidenceRun`（runner.go:70-90）承載 TCA 語意（expected_red／negative_control），**不重用、不改欄位**。
     - **產生契約（rev4 凍結——修「任意成功命令可滿足」）**：`test_run` 必須由 workbench evidence runner 產生，沿 TCA runner 防呆契約（package doc runner.go:6-10）並在兩處收緊：
       - (a) **Command 唯一來源＝committed plan**：runner 以 `LoadAt(plan_commit, plan_id)` → `task.TestContract.Command` 載入（runner.go:155-162、213 先例），caller **不得傳入 command**；`plan_commit`＝TaskRun snapshot 之 gate2 record `base_commit` binding 所指 commit（權威值），record 的 `command` 欄位為 runner 抄錄、供 Gate 3 重載比對。
       - (b) **執行環境**：於 `head_commit` 的 detached worktree 執行（`NewWorktree(repoRoot, commit, ...)` 先例，worktree.go:76-106；crash 清理沿 wt journal，worktree.go:274-329），不得於使用者 worktree。
       - (c) **OID 由 runner 解析（不採 TCA 現況 caller 傳入形狀**，TcaWorkspace.vue:191-196 使用者輸入、runner.go:250-251 直抄**）**：`head_commit` 由 runner rev-parse 為完整 OID；`base_commit` 自 snapshot 抄錄並驗其為 `head_commit` 祖先。
       - (d) stdout／stderr 經 CAS digest（runner.go:219-226 先例）。
     - **必要種類與完整性**：`kind` v1 僅凍結 `test_run`。Gate 3 要求至少一筆 `test_run` 同時滿足：`task_run_id`＝＝本 TaskRun、`plan_commit`＝＝snapshot 之 gate2 base_commit、`base_commit`＝＝snapshot `implementation_base_commit`、`head_commit`＝＝`promotion_head`、`result`＝＝green（exit 0）、且 **record `command` ＝＝ Gate 3 自 `plan_commit` 重載之 `task.TestContract.Command`**（§5.3(5)）。空集合或無任一滿足者→不符。commit range 以 OID 對錨定（兩 OID 已完整內容定址該 diff）；**不採 diff bytes digest**——diff 輸出跨 git 版本非確定。
     - **權威集合由 Gate 3 重建**：`PrepareDecision` 以 `task_run_id` 掃 evidence journal 重建完整 `{evidence_id, record_digest}` 集合（依 `evidence_id` 字典序排序），重算 manifest digest 必須等於 binding digest——**集合相等，非子集**；request 後新增 evidence 造成 mismatch→依 §4.3 轉終態、重新送核。任意呼叫者自產列表無效；CAS 重讀重驗沿 tca.go:27-38 契約。

**Canonical manifest 共同規則（rev3 凍結）**：§5.1(5)(6) 兩 manifest 皆含 `manifest_schema: 1`；digest＝`"sha256:" + hex(sha256(canonical_json(manifest)))`，canonicalization 同 §2.3（key 依本 spec 表列字面序、strict decode、未知欄位拒絕）；陣列一律依上列具名排序鍵排序後輸出，**forge／journal 回傳順序不得影響 digest**；同鍵多筆一律以具名 current-effective 規則收斂為至多一筆後才進 manifest。

**merge-group checks 定位**：Gate 3 之後、由平台獨立執行的驗證（B2 ruleset 範圍），不進 Gate 3 綁定，Gate 3 通過不豁免之。

### 5.2 Gate 3 policy 歸屬

`internal/gatepolicy` 新 policy（`internal/gate` 保持零 domain import，tca.go:1-12 架構凍結），註冊名 `"gate3_promotion"`（暫名）；subject 形狀 `taskrun:<ULID>`。

### 5.3 決議時重驗清單（backlog 6f）

Gate 3 `PrepareDecision`（approved 分支）必須重驗，任一不符→依 §4.3 轉終態並 fail closed：

1. TaskRun 非 STALE，且其上游三件核可此刻仍 Active、record_digest 不變。
2. `promotion_head` == forge 現時 PR head（防決議與 push 競態）。
3. `required_check_manifest` 重建：自 forge 重讀 required 集合與 runs，重算 manifest 必須與 binding digest 一致，且滿足 §5.1(5) 全部驗證條件——**missing／pending／failed 三態皆為不符**，不得以「目前存在的 runs 剛好都綠」替代集合完整性。
4. `main_base` 為 forge 現時 PR base，且 promotion_head 為 main_base 的 descendant。
5. `review_evidence_provenance` 重建：review 節自 forge 重讀（含**逐 reviewer eligibility 重查**，§5.1(6)）、依 current-effective 規則重建比對（零 CHANGES_REQUESTED 條件限具效力 reviewer）；evidence 節依 §5.1(6) 以 `task_run_id` 自 journal 重建權威集合（集合相等＋required kind 滿足＋record `command` 對 `plan_commit` 重載之 `TestContract.Command` 比對）＋CAS 重讀重驗。
6. rejected 分支沿 gate 慣例：僅需 reason，跳過重驗（在過期上下文上駁回必須成立）。

approved 重驗全數通過 → `CommitDecision` 落盤後、同一 `workflowMu` 臨界區內 append `taskrun_completed`（§3.4；臨界區既有形狀 app.go:5823-5854；crash split 收斂見 §3.2(3)）。

## 6. Forge interface（owner 裁決 #4；rev2 補 idempotency 與型別區分）

GitHub-first 的最小 port（B6 實作為 application service 的下游 adapter；GitLab 為未來第二實作，本 spec 僅保留擴充性）。型別明確區分 **repo identity**（`{owner, repo}`）、**branch ref**（`refs/heads/...`）、**commit OID**（git digest）：

```go
type Forge interface {
    // EnsurePullRequest（rev2——CreatePullRequest 為外部寫入，必須 crash/retry idempotent）：
    // 以 (repo, headRef, baseRef, taskRunID marker) 確定性收斂——
    //   既有 open PR 同 head/base 且 marker 相符 → 回傳之（不重建）；
    //   marker 不符或同 head/base 多筆 → fail loud（不自動收編、不另建）；
    //   不存在 → 建立（body/label 帶 taskrun:<ULID> marker）。
    EnsurePullRequest(ctx, repo RepoID, headRef, baseRef BranchRef, taskRunID string, meta PRMeta) (PRRef, error)
    GetPullRequest(ctx, repo RepoID, pr PRRef) (PRState{HeadOID, BaseOID, State}, error)
    // required 權威集合＋逐 check 狀態；三態（missing／pending／completed{conclusion}）必須可區分。
    // rev4：required key＝{Context, AppID}（AppID 可空＝不限來源，對齊 branch protection API）；
    // CheckRun 必含 {Name, AppID, RunID, HeadOID, Status, Conclusion, StartedAt}——StartedAt 供 §5.1(5) 收斂。
    GetRequiredChecks(ctx, repo RepoID, pr PRRef, head OID) (RequiredChecks{Required []RequiredCheckRef, Runs []CheckRun}, error)
    // PR review 讀取面（rev2 補——§5.1(6) review 節的權威來源）；SubmittedAt 供 current-effective 收斂（rev3 補）。
    GetReviews(ctx, repo RepoID, pr PRRef) ([]Review{ReviewID, ReviewerLogin, State, ReviewedHeadOID, SubmittedAt}, error)
    // reviewer eligibility（rev4 補——§5.1(6) 具效力 reviewer 判定；決議時逐 reviewer 查詢）。
    GetCollaboratorPermission(ctx, repo RepoID, login string) (Permission, error)
}
```

- 全部唯讀＋EnsurePullRequest；**不含 merge**——合併由平台與人工執行。
- 錯誤語意 fail closed：forge 讀取失敗＝無法決議，不得當作 checks 未設定或 review 不存在。
- 認證與 rate limit 處理屬 C1b 實作面。

## 7. DomainSpec 定位（owner 裁決 #6）

Gate 3 決議面可掛 DomainSpec shadow evaluator 之 explain 輸出作為**顯示層輔助**；決議權威為本 spec §5 的 Go 判定路徑。不接管、不阻擋、不計入決議條件。正式採用評估屬 M4.5（backlog D1）。

## 8. 出口條件（本 spec 的 design gate 通過標準）

1. TaskRun 欄位、抄錄一致性與 digest preimage（TaskRunPayloadV1 逐欄含巢狀 key 序、省略自身、canonicalization）無歧義（§2.1／§2.3）。
2. currentness 前置（含 test_commit 錨定與空 diff 規則）可逐條轉為測試（§2.2）。
3. 綁定不可繞過且**可跨重啟恢復**（§3.1 持久化＋基數）；gate journal 單一寫入者不變式、權威階層與逐分割點 repair matrix 完整、無未定義 crash split；session 移除 tombstone 契約（abandoned 終態）與 completed 觸發時點凍結（§3.2／§3.4／§3.6）。
4. Permission schema v1、strict decode 與 deterministic mapping 凍結，讀取來源為 commit tree 非 worktree（§3.3(2)）。
5. STALE 三形狀與 HEAD 前移不誤判可逐條轉為測試（§4.1）；STALE transition protocol（凍結方向 runtime 先行的 ordering、monotonic latch 唯一 owner 與設定端鎖序、線性化點與 system-deny 豁免、startup＋runtime repair 觸發點）可逐條轉為測試；completed dominance 與 pending-completed 阻擋規則無缺口（§3.4）；6d／6e／6c 裁決語意落地（§3.5／§4.2／§4.3）。
6. Gate 3 綁定 manifest（required 集合完整性、review／evidence 雙權威）確定性收斂——canonical 排序鍵、`(context, app_id)` 權威 key 與**一對一 coverage（bijection）**規則、reviewer eligibility、同 key current-effective 規則、implementation evidence schema 與 runner 產生契約、Gate 3 重建權威集合——與決議時重驗清單 fail-closed 無缺口（§5）。
7. Forge port 型別區分與 EnsurePullRequest idempotency 凍結（§6）。

## 9. 非目標

- merge queue／merge 代按、GitLab 實作、多 provider 抽象（pilot=Claude）。
- CI workflow 與 ruleset 內容（B2）、E2E（B3a/B3b）、app.go seams 實作（B6）、垂直切片實作（C1）。
- STALE facts 第二階段與 DomainSpec 正式採用（M4.5）。

## 修訂記錄

- rev5（2026-08-28，design gate 第四輪 3 P1 收斂）：
  - P1（app_id null 時集合相等不可滿足）：`runs[]` 改同時記錄 `{context, required_app_id}` 與實際 `{run_name, run_app_id}`；驗證條件由「集合相等」改為**一對一 coverage（bijection）**——每 required check 恰一筆歸屬 run、runs 無多餘、一 run 至多歸屬一 required check（多重歸屬 fail loud）（§5.1(5)）。
  - P1（latch 無單一線性化與鎖序）：精確化 rev4「同一把鎖」——latch 唯一 owner＝持 workflowMu 之 freeze 序列；實作為 manager 鎖域＋apprMu 鎖域兩個同源 monotonic 旗標，設定端固定鎖序（workflowMu→manager 鎖→apprMu，逐把釋放）＋「除設定端外禁止同時持兩鎖」不變式；線性化點凍結（新 turn＝BeginSubmit 成功返回；approval＝apprMu 內檢查＋pending 取出，p.resolve 鎖外形狀維持）；**allow=false 之 deny 永遠合法**——freeze 序列的系統 deny 不被自身 latch 阻擋（§4.2(2)(3)）。
  - P1（runtime 部分成功不 fail closed）：凍結方向 runtime 先行原則——latch＋deny 先於 `taskrun_stale` append 且不以 append 成功為前置（journal degraded 之既有拒絕語意＝gate journal.go:11/56、evidence journal.go:23/125，本輪盤讀確認）；repair 觸發點擴為 startup＋**每個持 workflowMu 寫入路徑進入時**；completed 側補 **pending-completed 旗標**（CommitDecision 成功即設，阻擋 stale/abandoned 追加，append 失敗或結果不確定一律視為可能已提交）＋ **completed dominance**（gate3 approved record 存在⇒權威終態 completed，殘留衝突 op 由 projection 忽略＋audit）（§4.2(1)(4)／§3.4／§3.6(3)／§3.2(3)）。
- rev4（2026-08-28，design gate 第三輪 4 P1＋1 P2 收斂；驗證來源＝2026-08-28 第二次盤讀 app.go:7100-7209／6930-6976／6783-6810／1181-1188、store.go:291-373、runner.go:155-262、worktree.go:76-106、registry.go:19-70）：
  - P1（RemoveSession tombstone 衝突）：新 §3.6——允許移除 bound TaskRun 之 session 但語意＝明示放棄，`taskrun_abandoned`（新終態，僅自 bound 進入）journal 先行、tombstone 在後；tombstone 無復活路徑，repair matrix 明確排除「當一般 Entry 遺失重建」並補 tombstone-無-abandoned／abandoned-移除未完成兩列；UI 兩段式 confirm 呈現放棄語意。`completed` 觸發時點凍結＝Gate 3 approved 決議同一 workflowMu 臨界區（CommitDecision 後 append），跨 journal crash split 依 gate3 record 確定性收斂（§3.2(3)／§3.4／§3.6）。
  - P1（STALE 凍結無 transition protocol）：§4.2 補 protocol——durable ordering（gate journal stale → taskrun_stale → runtime latch → deny pending approvals，同一 workflowMu 臨界區）；per-WSID monotonic freeze latch（set-once，startup 自 journal 重建）；SendMessage／resolveApproval 維持不取 workflowMu，改在各自既有鎖內做 latch 原子 check（與 set 同鎖互斥）；雙向 crash split repair（§4.2）。
  - P1（evidence 未證明核准測試於 promotion head 執行）：§5.1(6) 補 runner 產生契約——command 唯一來源＝committed plan（`LoadAt(plan_commit)` → `TestContract.Command`，caller 不得傳入；record 增 `plan_commit` 欄位）；於 head_commit 之 detached worktree 執行（TCA worktree 先例）；OID 由 runner rev-parse（不採 TCA caller-傳入現況）；Gate 3 重載 command 比對入必要條件與 §5.3(5)。
  - P1（forge manifest 缺 GitHub 權威身分條件）：required check 權威 key 改 `(context, app_id)`（對齊 branch protection API），run 歸屬規則＋歧義 fail loud；review 節補 reviewer eligibility（決議時查 collaborator permission ∈ write/maintain/admin，不具效力者雙向不計）；CODEOWNERS 等深度 policy 維持平台強制（B2），Gate 3 不重演；Forge port 補 `RequiredCheckRef`／`CheckRun.AppID`／`GetCollaboratorPermission`（§5.1(5)(6)／§6）。
  - P2（wsregistry 落盤錨點錯誤）：Entry `{task_run_id, snapshot_digest}` 掛點更正為 `commitSessionIdentity`（StartSession Accept 後、雙 provider 共用，app.go:1181-1188），沿 store `mutate()` 慣例新增寫入方法；`registry.Bind` 為獨立 claude session 反查表非 wsregistry（§3.2(1)(iv)）。
  - Owner 裁決回填：啟動失敗 resume-to-original 接受（§3.4 rev3 澄清維持）；零 current-effective CHANGES_REQUESTED 保留、限具效力 reviewer（§5.1(6)）。
- rev3（2026-08-28，design gate 第二輪 4 P1＋1 P2 收斂）：
  - P1（currentness 重驗非原子）：凍結 gate journal **單一寫入者不變式**——所有 append（含 Reconcile stale transition）必須持 `workflowMu`；讀取入口改 detect-only 投影（不落盤）；列舉現況 7 個違反入口為實作遷移前置（§3.2(0)）。捨棄替代案（讀取全取鎖／generation CAS）附因。驗證來源：2026-08-28 盤讀 app.go:5716-5730、service.go:191-261 等。
  - P1（TaskRun journal／wsregistry crash split）：凍結**權威階層**（journal 唯一權威、Entry 為 derived cache 可重建）；durable ordering 改兩段短臨界區、`claude.Start` 移出鎖外、Entry 隨既有 init-event Bind 落盤不新增寫入點（對齊 production 實際時機 app.go:7404-7416）；補逐分割點 **repair matrix**；`claude.Start` 失敗統一走 resume-to-original，`aborted` 唯一入口為 created，消除 rev2 與狀態機的矛盾（§3.2(1)-(3)／§3.4／§3.5）。
  - P1（implementation evidence 未綁 TaskRun）：凍結新 record type `implementation_evidence` schema v1（task_run_id＋OID 對 range＋執行結果；不重用 TCA 語意的 EvidenceRun）；required kind＝green `test_run`@`implementation_base_commit..promotion_head`；**Gate 3 以 task_run_id 重建權威集合、集合相等**，空集合／他 TaskRun 紀錄不可能通過（§5.1(6)）。
  - P1（canonical manifest 無確定性收斂）：兩 manifest 補 `manifest_schema: 1`＋具名排序鍵＋共同 canonicalization 規則；同名 check 取 started_at 最新（tie run_id）；review 依 reviewer 收斂 current-effective（COMMENTED/PENDING 不改狀態），通過條件改「≥1 current-effective APPROVED@head **且零 current-effective CHANGES_REQUESTED**」，附 rev3 選擇註記供 owner 裁決放寬（§5.1(5)(6)＋共同規則）。
  - P2（preimage 欄位非皆單值）：凍結 TaskRunPayloadV1 逐欄清單含巢狀 key 字面序，改正 rev2「欄位皆單值」的錯誤宣稱（§2.3）。
- rev2（2026-08-27，design gate 第一輪 8 P1＋forge 補強收斂）：
  - P1：`test_commit` 入 snapshot＋基準錨定規則（test_commit 為祖先且至 implementation_base_commit 空 diff；TCA 共同 test_commit 驗證為來源，tca.go:228-229）。
  - P1：permission enforcement 凍結——schema v1／strict decode／deterministic mapping／自 gate2 base_commit tree 讀取重算，不讀 worktree；含既有檔遷移前置。
  - P1：核准上下文取得凍結——一律自 gate2 base_commit tree 讀取＋重算 manifest 比對，mismatch／missing／讀取錯誤 fail closed。
  - P1：TOCTOU 關閉——workflowMu 臨界區＋兩階段 durable ordering（created→bound）＋publish 前最後重驗＋crash boundary 語意（§3.2）。
  - P1：綁定持久化——wsregistry.Entry 增 {task_run_id, snapshot_digest}；基數 1:1、不允許重綁；TaskRun 狀態機（§3.4）。
  - P1：snapshot_digest preimage 定義——schema_version＋省略自身＋canonicalization／strict decode 凍結（§2.3）。
  - P1：required_check_run 改 required_check_manifest——權威 required 集合＋逐 check 驗證（無缺漏／無重複／全 success／全對 head）；forge port 三態可區分。
  - P1：review_evidence_provenance 定權威——review 節=GitHub PR review（重讀比對）、evidence 節=Workbench evidence journal（exactly-once＋CAS 重驗）；forge port 補 GetReviews。
  - Forge 補強：CreatePullRequest 改 EnsurePullRequest（TaskRun marker 確定性收斂、多筆／不符 fail loud）；型別區分 repo identity／branch ref／commit OID。
  - Owner 裁決回填：6d resume 僅回原 TaskRun ID＋digest 不允許重綁（§3.5）；6e STALE 立即凍結 session（拒新 turn／新核可、收束進行中 turn、保留 transcript／evidence、禁 Gate 3、需新 TaskRun）（§4.2）；6c 決議時重驗＋mismatch 轉 expired／stale 終態不留 pending（§4.3）。
- rev1（2026-08-27）：初版。
