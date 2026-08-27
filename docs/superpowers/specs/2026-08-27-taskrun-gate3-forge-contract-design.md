# TaskRun／Gate 3／Forge 契約設計（B5）

> 版本：rev1（2026-08-27）
> 狀態：**待 design gate**
> 來源：Pre-M4 Readiness Backlog B5（rev5 估點版）；owner 裁決 #3（session 自動綁定不可變 snapshot）、#4（GitHub-first）、#5（Gate 3 六件綁定）、#6（DomainSpec 僅 shadow／explain）
> 範圍：**spec 級**——定義物件、生命週期與契約，不含實作；為 C1a／C1b／C1c 垂直切片與 B6 application seams 的設計依據。production 錨點以 2026-08-27 HEAD `f63d975` 盤讀為準，實作時引用前先驗 file:line 仍成立。

## 1. 目的

M4 的治理迴路要能回答並強制：「這一段實作是在**什麼核准上下文**中完成的，且該上下文在 Gate 3 決議當下**仍然成立**」。本 spec 定義三個物件承載它：**TaskRun snapshot**（不可變核准上下文）、**implementation session 綁定**（自動、不可繞過）、**Gate 3 決議契約**（六件綁定＋決議時重驗）。平台強制力（PR／required checks／ruleset）屬 forge 與 B2 範圍；Workbench 負責建立、注入、顯示、記錄與編排。

## 2. TaskRun snapshot（不可變物件）

### 2.1 欄位

TaskRun 於「選定 task、啟動 implementation」時建立，建立後**不可變**（append-only journal，沿 `internal/gate`／`internal/evidence` journal 先例；重大狀態變化以 transition 記錄追加，不改寫原 snapshot）：

| 欄位 | 內容 | 來源錨點 |
|---|---|---|
| `task_run_id` | ULID | 新發 |
| `plan_id`／`task_id` | 對應 plan 與 task | Gate 2 subject `plan:<id>`（gate2.go:253-259）＋plan.tasks |
| `gate1_approval` | `{approval_id, record_digest}` | `gate.RecordDigest`（record_digest.go:12-19）——**以 digest 錨定紀錄而非只記 ID**，沿 TCA 對 gate2_approval 的既有先例（tca.go:238-240） |
| `gate2_approval` | `{approval_id, record_digest}` | 同上；record 傳遞性涵蓋 spec_manifest／plan／base_commit／risk_policy／permission_manifest 五 binding digest |
| `selected_risk_tier` | 該 task 的核可風險層 | `ApprovalRecord.Metadata.RiskDecisions`——**production 現況為隻寫不讀**（GateEntryDTO 不含 Metadata；gate2.go:193 寫入後無讀取點），本 spec 明定讀取路徑＝`gate.Service.Lookup(approval_id)`（service.go:198-211）直取 record 後依 task_id 檢索 |
| `permission_manifest_digest` | 自 gate2 record 的 `permission_manifest` binding 抄出 | app.go:4988（binding 組裝）、app.go:4295-4307（digest 計算） |
| `tca_approval` | `{approval_id, record_digest}` | TCA record（subject `task:<plan_id>/<task_id>`，tca.go:394-404） |
| `expected_red_evidence`／`negative_control_evidence` | 各 `{evidence_id, evidence_run_digest}` | 自 TCA record 的兩個 role-scoped `evidence_run` binding 抄出（tca.go:78-85）；digest 公式 `EvidenceRunDigest`（runner.go:274-281） |
| `oracle_surface_digest` | 自 TCA record 的 `oracle_surface` binding 抄出 | tca.go:78-85 |
| `implementation_base_commit` | TaskRun 建立當下 workspace HEAD 的 git OID | 建立時 rev-parse；**必須是 gate2 `base_commit`（plan_commit）的 descendant**（ancestry 檢查，沿 §3.0 lineage 慣例） |
| `created_at`／`snapshot_digest` | 建立時間；canonical JSON 之 `sha256:<hex>` | digest 公式沿 manifest／RecordDigest 慣例（manifest.go:22-36） |

抄錄欄位（selected_risk_tier、permission_manifest_digest、evidence digests、oracle_surface_digest）的**權威仍是上游 record**；抄錄目的為 Gate 3 讀取便利與獨立 staleness 檢查，建立時必須與上游 record 逐欄一致，不一致即建立失敗（fail loud，不得靜默取上游現值）。

### 2.2 建立前置：currentness（backlog 6a）

TaskRun 建立當下，透過 `gate.Service.List()`（Reconcile-before-Project，service.go:191-196——「projection 永不信任快取的 active 狀態」）驗證：

1. gate1、gate2、TCA 三者對應 subject 的核可皆為 `Active`（非 Pending／Stale／Superseded／Rejected）。
2. TCA 的 `gate2_approval` binding 所指 approval_id 與本 TaskRun 綁定的 gate2 approval **同一筆**（防止 TCA 綁舊 gate2、TaskRun 綁新 gate2 的錯位）。
3. `implementation_base_commit` 為 gate2 `base_commit` 的 descendant。
4. worktree 乾淨（沿 Gate 2 submit host boundary 慣例）。

任一不成立→拒絕建立，錯誤具名（哪一件、哪種原因）。

## 3. Implementation session 自動綁定（owner 裁決 #3）

### 3.1 附著點與不可變性

- 綁定附著於 `sessionHost`（session_host.go:49-105）——該結構已有「publish 前寫定、publish 後不可變」契約（session_host.go:30-42），TaskRun 引用（`task_run_id`＋`snapshot_digest`）作為其新欄位，同受此契約約束。
- 建構位置在 `startClaude` 的 host 組裝與 `claude.Start` 之間（app.go:7307-7375）；pilot provider＝Claude（backlog C1 已裁決）。
- **「僅複製上下文」不接受**（#3）：session 啟動參數（prompt 前置、MCP config、env）由 TaskRun 內容**生成**，不提供讓使用者自行貼上的路徑；無 TaskRun 的一般 session 仍可啟動，但不得標記為 implementation session、其產出不得進 Gate 3（區分旗標入 `wsregistry` entry）。

### 3.2 注入內容（C1a 實作面，spec 凍結語意）

1. **核准上下文前置**：spec／plan／task／risk tier／TCA 摘要與 digest 清單，作為 session 首則 user message 的前置段（production 現況無 system-prompt 注入面——`Config.args()` 不帶 `--system-prompt`，session.go:35-49；沿現況用 Prompt 前置，不新增 CLI 面）。
2. **Permission manifest 落地**：Claude 的 `SettingsJSON` 現為硬編碼常數（app.go:7379），且 `approvalPolicy` 參數只接到 Codex（app.go:6905；`startClaude` 無此參數）——本 spec 要求 implementation session 的 SettingsJSON 由 TaskRun 的 permission manifest 內容**生成**；生成規則於 C1a 實作時定義，spec 凍結的是「來源必須是 snapshot 所綁 digest 對應的 manifest 內容，不得另讀 worktree 現值」。
3. **TaskRun 標記**：`task_run_id` 入 session 的 env／MCP config（mcp-<wsid>.json 既有機制，app.go:7309），供 evidence 收集與 audit 歸屬。

### 3.3 Resume（backlog 6d——【owner 決策】）

選項（design gate 決）：
- **(a) resume 僅能回到原 TaskRun**：resume 時驗 `task_run_id` 一致，TaskRun 已 STALE 則依 §4 處置。較嚴格、審計最乾淨。
- **(b) resume 允許但降級標記**：TaskRun STALE 後 resume 的 session 產出標記為「stale context」，不得進 Gate 3。
建議 (a)——與 #3 的自動綁定精神一致；`internal/claude.Registry` 的 resume 驗證面（registry.go:57-99）是現成掛點。

## 4. STALE／重驗生命週期

### 4.1 觸發形狀（backlog 6b——三形狀必須區分）

| 形狀 | 判定 | 效果 |
|---|---|---|
| **上游核可失效** | gate1／gate2／TCA record 轉 Stale／Superseded（Reconcile 既有機制），或 record_digest 對不上 | TaskRun → STALE |
| **binding digest 改變** | permission manifest 等現值重算與 snapshot 抄錄不一致（沿 §3.9 持續重算，gate2.go:207-232） | TaskRun → STALE |
| **commit missing** | `implementation_base_commit` 或上游 base_commit 於 repo 消失（rev-parse exit1） | TaskRun → STALE（fail closed：讀取錯誤≠missing，沿 R13 慣例） |
| **單純 HEAD 前移** | workspace／main HEAD 正常前進，上述皆未觸發 | **不得 STALE**（沿 TCA「HEAD 正常前移不誤判 STALE」既有錨點契約） |

### 4.2 STALE 處置（backlog 6e——【owner 決策】）

選項（design gate 決）：
- **(a) 中止 session**：STALE 即終止 implementation session。最嚴格；可能浪費進行中的合理工作。
- **(b) 保留 evidence、禁止 Gate 3**：session 可續跑收尾，產出與 evidence 保留並標記 stale context；Gate 3 拒收，需建立新 TaskRun 重驗。
- **(c) 強制新 TaskRun**：STALE 當下引導重建 snapshot（上游重核後），session 遷移綁定。
建議 (b)——與 escalation 收件匣「durable blocker」慣例一致，且不銷毀已花費的工作；(c) 的「遷移綁定」與不可變契約衝突，不建議。

### 4.3 Gate 3 即時失效（backlog 6c——【owner 決策】）

promotion head 前移、main base 前移、required-check 結果變動時，已開啟但未決議的 Gate 3 是否立即失效：
- **(a) 立即失效**：三者任一變動即要求重新發起決議。
- **(b) 決議時重驗**：開啟狀態不追蹤變動，僅在按下決議當下依 §5.3 清單重驗，不符即拒。
建議 (b)——與 `PrepareDecision` 的 current-binding validation 模式一致（service.go:77-116：決議當下重驗、fail closed），避免對 forge 做輪詢追蹤。

## 5. Gate 3 決議契約（owner 裁決 #5）

### 5.1 六件綁定

Gate 3 approval request 的 bindings（沿 `gate.Binding{Kind, Role, Ref, Digest}` 形狀，types.go:15-20）：

1. `task_run`——ref `taskrun:<ULID>`、digest＝`snapshot_digest`。
2. `promotion_head`——PR head commit OID（git digest 格式沿 reGitOID，gate2.go:28-31）。
3. `main_base`——PR base（main）commit OID。
4. `oracle_surface`——digest 必須等於 TaskRun 抄錄值（傳遞性錨定 TCA）。
5. `required_check_run`——forge check run 的 `{run id, head SHA, conclusion}` 摘要 digest；head SHA 必須等於 `promotion_head`。
6. `review_evidence_provenance`——實作期 evidence（diff digest、測試證據）與人工 review 記錄的 manifest digest。

**merge-group checks 定位**：Gate 3 之後、由平台獨立執行的驗證（B2 ruleset 範圍），不進 Gate 3 綁定，Gate 3 通過不豁免之。

### 5.2 Gate 3 policy 歸屬

`internal/gatepolicy` 新 policy（沿 gate2／tca 先例：`internal/gate` 保持零 domain import，tca.go:1-12 架構凍結註記），註冊名 `"gate3_promotion"`（暫名，實作時定）；subject 形狀 `taskrun:<ULID>`。

### 5.3 決議時重驗清單（backlog 6f）

Gate 3 `PrepareDecision`（approved 分支）必須重驗，任一不符 fail closed：

1. TaskRun 非 STALE，且其上游三件核可（gate1／gate2／TCA）此刻仍 Active、record_digest 不變。
2. `promotion_head` == forge 現時 PR head（防決議與 push 競態）。
3. `required_check_run` 的 conclusion 為 success 且 head SHA == promotion_head（check 對到的是這份 code）。
4. `main_base` 為 forge 現時 PR base，且 promotion_head 為 main_base 的 descendant。
5. `review_evidence_provenance` manifest 之 CAS 內容可重讀且 digest 相符（沿 EvidenceStore 重讀重驗契約，tca.go:27-38）。

rejected 分支沿 gate 慣例：僅需 reason，跳過重驗（在過期上下文上駁回必須成立）。

## 6. Forge interface（owner 裁決 #4）

GitHub-first 的最小 port（B6 實作為 application service 的下游 adapter；GitLab 為未來第二實作，本 spec 僅保留擴充性、不設計其細節）：

```go
type Forge interface {
    CreatePullRequest(ctx, {head, base, title, body}) (PRRef, error)
    GetPullRequest(ctx, PRRef) ({head OID, base OID, state}, error)
    GetRequiredCheckRuns(ctx, PRRef, headOID) ([]{name, runID, conclusion, headSHA}, error)
}
```

- 全部唯讀＋建立 PR；**不含 merge**——合併由平台（ruleset／merge queue）與人工執行，Workbench 不代按。
- 錯誤語意 fail closed：forge 讀取失敗＝無法決議，不得當作 checks 未設定。
- 認證與 rate limit 處理屬 C1b 實作面。

## 7. DomainSpec 定位（owner 裁決 #6）

Gate 3 決議面可掛 DomainSpec shadow evaluator 之 explain 輸出（reason graph）作為**顯示層輔助**；決議權威為本 spec §5 的 Go 判定路徑。不接管、不阻擋、不計入決議條件。正式採用評估屬 M4.5（backlog D1）。

## 8. Owner 決議事項（design gate 中裁定）

| 項 | 議題 | 選項與建議 |
|---|---|---|
| 6d | session resume 綁定 | §3.3——建議 (a) 僅回原 TaskRun |
| 6e | STALE 處置 | §4.2——建議 (b) 保留 evidence、禁止 Gate 3 |
| 6c | Gate 3 即時失效 | §4.3——建議 (b) 決議時重驗 |

## 9. 出口條件（本 spec 的 design gate 通過標準）

1. TaskRun 欄位與抄錄一致性規則無歧義（§2.1）。
2. currentness 前置四項可逐條轉為測試（§2.2）。
3. 綁定不可繞過的機制邊界明確（§3.1——無 TaskRun 即非 implementation session）。
4. STALE 三形狀與 HEAD 前移不誤判的判定可逐條轉為測試（§4.1）。
5. 三項 owner 決議完成並回填 §3.3／§4.2／§4.3。
6. Gate 3 六件綁定與決議時重驗清單完整、fail-closed 語意無缺口（§5）。
7. Forge port 最小面凍結（§6）。

## 10. 非目標

- merge queue／merge 代按、GitLab 實作、多 provider 抽象（pilot=Claude）。
- CI workflow 與 ruleset 內容（B2）、E2E（B3a/B3b）、app.go seams 實作（B6）、垂直切片實作（C1）。
- STALE facts 第二階段與 DomainSpec 正式採用（M4.5）。

## 修訂記錄

- rev1（2026-08-27）：初版——production 錨點盤讀（TCA binding 先例、sessionHost 不可變契約、RiskDecisions 隻寫不讀、permission manifest 無獨立型別、Claude approvalPolicy 無注入點）納入設計依據。
