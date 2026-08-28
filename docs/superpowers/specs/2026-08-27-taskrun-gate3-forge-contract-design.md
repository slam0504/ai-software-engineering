# TaskRun／Gate 3／Forge 契約設計（B5）

> 版本：rev3（2026-08-28，design gate 第二輪 4 P1＋1 P2 收斂）
> 狀態：**待 design gate**
> 來源：Pre-M4 Readiness Backlog B5（rev5 估點版）；owner 裁決 #3（session 自動綁定不可變 snapshot）、#4（GitHub-first）、#5（Gate 3 六件綁定）、#6（DomainSpec 僅 shadow／explain）
> 範圍：**spec 級**——定義物件、生命週期與契約，不含實作；為 C1a／C1b／C1c 垂直切片與 B6 application seams 的設計依據。production 錨點以 2026-08-27 盤讀為準（rev3 新增錨點為 2026-08-28 盤讀），實作時引用前先驗 file:line 仍成立。

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
   - (iv) 鎖外：`claude.Start` → publish host。wsregistry Entry 的 `{task_run_id, snapshot_digest}` 隨既有 `registry.Bind`／`commitClaudeResume` 的 Entry 落盤寫入（init event 觸發，app.go:7404-7416；四步原子落盤契約 store.go:182-218），**不新增獨立寫入點**。
   - 殘餘窗口：git repo 外部狀態（commit 遭 GC／branch 刪除）無法納入臨界區；由 §4.1 staleness 形狀與 §5.3 決議時重驗承接。
2. **權威階層（rev3 凍結）**：TaskRun journal 是綁定的**唯一權威**；wsregistry Entry 的 `{task_run_id, snapshot_digest}` 是 **derived cache**（供 resume 快速定位），可自 journal 重建。兩者不一致一律以 journal 為準修復 Entry；不得反向以 Entry 補寫 journal。
3. **Repair matrix（startup repair 逐分割點；rev3 補）**：

   | 分割點 | 觀測形狀 | 修復 |
   |---|---|---|
   | created 後、bound 前 crash | journal：created 非終態、無 bound | append `taskrun_aborted`（終態；snapshot 保留供審計，不可再綁定） |
   | bound 後、Entry 落盤前 crash（或 Entry 寫入失敗） | journal：bound；Entry 無綁定欄位或不存在 | 「bound 無活 session」→ §3.5 resume；session 重建時依 journal 回填 Entry |
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

### 3.4 TaskRun 狀態機（rev2 補）

`created →（bound | aborted）`；`bound →（stale | completed）`；終態＝`aborted`／`stale`／`completed`。全部以 journal transition op 表達；startup repair 負責 crash 殘留的收斂（§3.2(3)）。orphan 定義：`created` 未達 `bound`＝aborted；`bound` 無活 session＝可 resume，非 orphan。**rev3 澄清**：`aborted` 唯一入口為 `created`；`claude.Start` 失敗不進 aborted（§3.2(3) 統一走 resume-to-original），狀態機不擴寬。

### 3.5 Resume（owner 裁決 6d，已定）

Resume **永遠只能回到原 TaskRun**：resume 時驗 `wsregistry.Entry` 的 `{task_run_id, snapshot_digest}` 與 journal 內 snapshot 一致；**不允許重新綁定**至其他 TaskRun。TaskRun 已 STALE → 依 §4.2 凍結語意（session 不可再產生新 turn）。`internal/claude.Registry` 的 resume 驗證面（registry.go:57-99）為現成掛點。**rev3 補（journal 權威，§3.2(2)）**：Entry 遺失或缺綁定欄位時，以 journal 的 bound 紀錄為準重建 Entry，不阻擋 resume；bound 且**從未**成功建立 session 者（Start 失敗、bound 後立即 crash），resume＝為原綁定重新建立 session——無 transcript 可續屬合法形狀，非錯誤。

## 4. STALE／重驗生命週期

### 4.1 觸發形狀（backlog 6b——三形狀必須區分）

| 形狀 | 判定 | 效果 |
|---|---|---|
| **上游核可失效** | gate1／gate2／TCA record 轉 Stale／Superseded（Reconcile 既有機制），或 record_digest 對不上 | TaskRun → STALE |
| **binding digest 改變** | permission manifest 等現值重算與 snapshot 抄錄不一致（沿 §3.9 持續重算，gate2.go:207-232） | TaskRun → STALE |
| **commit missing** | `implementation_base_commit`／`test_commit`／上游 base_commit 於 repo 消失（rev-parse exit1） | TaskRun → STALE（fail closed：讀取錯誤≠missing，沿 R13） |
| **單純 HEAD 前移** | workspace／main HEAD 正常前進，上述皆未觸發 | **不得 STALE**（沿 TCA 錨點契約） |

### 4.2 STALE 處置（owner 裁決 6e，已定）

TaskRun STALE 後**立即凍結 implementation session**：

1. 拒絕新 turn 與新 tool approval（approval broker 對該 session fail closed）。
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
5. `required_check_manifest`（rev2 改——單筆 check run 無法證明 required set 完整；rev3 補確定性收斂）——canonical manifest 的 digest，manifest 內容：
   - `manifest_schema`：固定 1。
   - `required_names[]`：**權威集合**＝forge ruleset／branch protection 定義的 required check 名稱清單（決議時自 forge 讀取），**字典序排序**；forge 回傳重複名稱→fail loud（不靜默去重）。
   - `runs[]`：依 `name` 字典序排序、每 name **恰一筆** `{name, run_id, head_sha, status, conclusion}`（巢狀 key 依此字面序）。**同名多次執行的收斂規則（current-effective run）**：取 `started_at` 最新者，tie-break `run_id` 較大者；驗證條件施加於收斂後的那一筆。
   - 驗證條件：names 與 runs **集合相等**（無缺漏、無重複）、全部 `conclusion == success`、全部 `head_sha == promotion_head`。
6. `review_evidence_provenance`（rev2 定權威；rev3 補 current-effective 規則與 implementation evidence schema）——canonical manifest 的 digest，兩節、各有具名權威：
   - **Review 節（權威＝GitHub PR review）**：列表＝**每 reviewer 至多一筆 current-effective review**，依 `reviewer_login` 字典序排序（unique key）；每筆 `{reviewer_login, review_id, state, reviewed_head_sha, submitted_at}`（巢狀 key 依此字面序）。**Current-effective 規則**：該 reviewer 全部 review 中 `state ∈ {APPROVED, CHANGES_REQUESTED, DISMISSED}` 且 `submitted_at` 最新者（tie-break `review_id` 較大者）；`COMMENTED`／`PENDING` 不改變有效狀態、不入 manifest。**驗證條件（rev3 收斂——修「較早 approval 在後續非核准 review 下仍通過」）**：至少一 reviewer 的 current-effective `state == APPROVED` 且 `reviewed_head_sha == promotion_head`；**且不存在任何 current-effective `state == CHANGES_REQUESTED`**（任一存在即不符）；`DISMISSED`＝該 reviewer 無有效核可（不計入亦不阻擋）。〔rev3 選擇註記：零 CHANGES_REQUESTED 為 fail-closed 預設，較 GitHub 預設 branch protection 嚴；owner 可於 gate 裁決放寬。〕任意呼叫者自產 manifest 無效——決議時自 forge **重讀**比對（§5.3(5)）。
   - **Evidence 節（權威＝Workbench evidence journal；rev3 凍結 implementation evidence schema——修「任意 {evidence_id, digest} 列表、空集合或他人紀錄可通過」）**：
     - 新 journal record type **`implementation_evidence`**（schema v1）：`{schema_version: 1, evidence_id（ULID、exactly-once，journal.go:30-32）, task_run_id, kind, base_commit, head_commit, command, cwd, exit_code, stdout_digest, stderr_digest, started_at, finished_at, result}`。為 evidence journal record type 白名單（現況僅 `evidence_run`／`mutation`，journal.go:68-92）的 additive 第三種；現有 `EvidenceRun`（runner.go:70-90）承載 TCA 語意（expected_red／negative_control），**不重用、不改欄位**。
     - **必要種類與完整性**：`kind` v1 僅凍結 `test_run`。Gate 3 要求至少一筆 `test_run` 同時滿足：`task_run_id`＝＝本 TaskRun、`base_commit`＝＝snapshot `implementation_base_commit`、`head_commit`＝＝`promotion_head`、`result`＝＝green（exit 0）。空集合或無任一滿足者→不符。commit range 以 OID 對錨定（兩 OID 已完整內容定址該 diff）；**不採 diff bytes digest**——diff 輸出跨 git 版本非確定。
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
5. `review_evidence_provenance` 重建：review 節自 forge 重讀、依 §5.1(6) current-effective 規則重建比對（含零 CHANGES_REQUESTED 條件）；evidence 節依 §5.1(6) 以 `task_run_id` 自 journal 重建權威集合（集合相等＋required kind 滿足）＋CAS 重讀重驗。
6. rejected 分支沿 gate 慣例：僅需 reason，跳過重驗（在過期上下文上駁回必須成立）。

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
    // CheckRun 必含 {Name, RunID, HeadOID, Status, Conclusion, StartedAt}——StartedAt 供 §5.1(5) 同名收斂（rev3 補）。
    GetRequiredChecks(ctx, repo RepoID, pr PRRef, head OID) (RequiredChecks{RequiredNames []string, Runs []CheckRun}, error)
    // PR review 讀取面（rev2 補——§5.1(6) review 節的權威來源）；SubmittedAt 供 current-effective 收斂（rev3 補）。
    GetReviews(ctx, repo RepoID, pr PRRef) ([]Review{ReviewID, ReviewerLogin, State, ReviewedHeadOID, SubmittedAt}, error)
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
3. 綁定不可繞過且**可跨重啟恢復**（§3.1 持久化＋基數）；gate journal 單一寫入者不變式、權威階層與逐分割點 repair matrix 完整、無未定義 crash split（§3.2／§3.4）。
4. Permission schema v1、strict decode 與 deterministic mapping 凍結，讀取來源為 commit tree 非 worktree（§3.3(2)）。
5. STALE 三形狀與 HEAD 前移不誤判可逐條轉為測試（§4.1）；6d／6e／6c 裁決語意落地（§3.5／§4.2／§4.3）。
6. Gate 3 綁定 manifest（required 集合完整性、review／evidence 雙權威）確定性收斂——canonical 排序鍵、同名 check／同 reviewer current-effective 規則、implementation evidence schema 與 Gate 3 重建權威集合——與決議時重驗清單 fail-closed 無缺口（§5）。
7. Forge port 型別區分與 EnsurePullRequest idempotency 凍結（§6）。

## 9. 非目標

- merge queue／merge 代按、GitLab 實作、多 provider 抽象（pilot=Claude）。
- CI workflow 與 ruleset 內容（B2）、E2E（B3a/B3b）、app.go seams 實作（B6）、垂直切片實作（C1）。
- STALE facts 第二階段與 DomainSpec 正式採用（M4.5）。

## 修訂記錄

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
