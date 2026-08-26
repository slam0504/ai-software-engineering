# DomainSpec kernel 限時 spike——Gate 2 shadow evaluator 設計

Owner 2026-08-21 仲裁凍結的方向（見 auto memory `domainspec-spike-direction`）：**獨立、
限時**的 spike，切點是 Gate 2 decision eligibility＋risk policy 的 **shadow evaluator**
——immutable facts snapshot 評估、現行 Go 實作保持權威、新 kernel 只比對不接管；不重塑
M4。第二階段 STALE facts、第三階段 TCA consistency 不在本 spike。本文件 rev2。

## 1. 範圍：要 shadow 的規則面

規則面已逐條盤點（2026-08-26，對照 `internal/gatepolicy/gate2.go`／`internal/gate/
service.go`／`internal/escalation/project.go`／app.go `gateDecide`，R1–R34 完整清單見
本檔附錄 A）。Spike 只 shadow **純 facts→verdict** 的子集：

- **Eligibility**：decision enum 與 reject-reason（R1–R2）、pending 狀態（R4）、gate 種類
  與 subject 形狀（R5–R6）、binding 集合完整性與 digest 格式（R7–R9）、stale 比對
  （R11——bound digest 對 snapshot 內的現值 digest 做相等比較，**僅 approved 分支**，
  沿 R10 guard）、base_commit 存在性（R12，**僅 approved**；host 取值成功才有此 fact）、
  blocking escalation（R15 scope 導出——**CEL 由 gate＋subject 獨立重算 scope**，不抄
  production `scopeForSubject` 的輸出，否則 mapping mutation 測不到——＋R16 覆蓋判定）。
- **Risk 決議**：rejected 不得帶 selections（R21）、selection 對 plan tasks 的雙向完整性
  （R24–R26）、minimum 重算（R27，含 risk policy match 規則）、tier 合法值（R28）、
  planner／selected 底線（R29–R30）、override_reason 強制（R31）、輸出排序決定性（R32）。

**不進 CEL**（owner 凍結＋盤點確認）：git 子行程（rev-parse／show／merge-base）、檔案
digest 重算、journal 原子 append 與 supersession 合寫、escalation 寫入、`workflowMu`／
`s.mu` 鎖語意、`ensureGate` 惰性初始化、EmitGateEvent。這些留在 host；**所有 I/O 結果
在評估前解析成 facts**（例如 rev-parse 的 ok／exit1／fatal 三態）。R13／R17 的
fail-closed（讀取錯誤不得當 stale／無 blocker）由 host 層維持，snapshot 產生失敗即不
評估——error 不併入 unknown。

兩個對齊警訊（盤點實證，shadow 必須複製同語意，並各配一條 corpus 案例）：
- `escalation.BlockingFor` **忽略 `hard` 旗標**——hard 只影響 UI 可否手動 resolve，
  不影響擋不擋（project.go:81-96）。
- `bindingDigest` 只匹配 `Role == ""` 的 binding（gate2.go:261-268）。

## 2. Facts schema（typed、canonical、unknown 拒絕）

新 package `internal/domainspec`（純 domain，無 I/O——依 plan package 的先例）。
`FactsSnapshot` 為 typed struct（非 map）：

- `decision`／`reason`／`approver{name,email}`
- `entry{exists, has_request, has_record}`＋`request{gate, subject, bindings[]{kind,role,ref,digest}}`
- `registry_gates[]`、`tier_order` 為 bundle 常數，不入 snapshot
- `current{spec_manifest, plan_manifest, risk_policy, permission_manifest}`（digest 字串）
- `base_commit_state ∈ {ok, missing}`
- `plan{tasks[]{id, minimum_risk_tier, planner_risk_tier, impact{contexts,modules}}}`
- `risk_policy{default_tier, rules[]{match{contexts,modules}, tier}}`
- `risk_selections[]{task_id, selected_risk_tier, override_reason}`
- `escalations[]{state, block_scope}`（scope **不入** snapshot——CEL 自行由 gate＋subject
  導出，見 §1 R15）

**Phase-aware facts acquisition（rev2）**：每個 fact 帶三態 presence——`known`／
`not_applicable`／`missing`。rejected 分支的 `current.*`／`base_commit_state`／`plan`／
`risk_policy` 一律 `not_applicable`（production rejected 路徑不跑 ReconcileBindings、不
`LoadAt`——不得因 shadow 取 facts 而改變 rejected 可成立的語意）；host 取值失敗
（digest 重算錯、rev-parse fatal、LoadAt 錯）發生在 **evaluator 之外**：該案例標記
`acquisition_failed` 並記錄原因，不產 snapshot、不評估、不計入一致性統計（但計入
coverage 報告的豁免欄）。`missing` 只用於「本應 known 而缺」，評估時進 `unknown_leaves`。

**Canonical 形式（rev2）**：snapshot 帶 `schema_version`；presence 以 pointer／wrapper
表達（nil＝missing，與合法零值區分）；集合排序固定——`bindings` 依 (kind,role)、
`risk_selections` 依 task_id、`escalations` 依 escalation_id、`plan.tasks` 依 id；
canonical 序列化時 nil 集合與空集合一律輸出 `[]`。canonical JSON 取 sha256 digest 可
重放；decode 拒絕未知欄位（fail loud）。

## 3. Rule bundle 與 evaluator 輸出

- **Bundle**：YAML 檔（spike 期間放 `internal/domainspec/testdata/gate2-bundle.yaml`，
  不動 `spec/` scope——manifest ScopePatterns 不含 `spec/rules/**`，擴 scope 是 M4.5 的
  事）。**最小規則代數（rev2）**——每條規則：`id`（穩定，沿用附錄 A 的 R 編號）、
  `when`（CEL 布林式）、`effect ∈ {deny, allow}`（spike 的 Go 對齊面全部是 deny；allow
  保留給 conflict fixture 與代數完整性）、`target`（效果作用的判定標的，如
  `decision.eligibility`／`risk.T<id>`）、`depends_on[]`（引用其他 rule id，SCC 在此圖
  上驗無環）、`priority`（同 target 衝突時的 tie-break；無法 tie-break 即 conflict）、
  `verdict` 訊息模板、`refs`（production file:line）。**全域 truth 映射**：任一 `deny`
  命中 → `false`；零命中且無 missing leaf → `true`；被評估的 leaf 有 missing →
  `unknown`；同一 target 上 `allow` 與 `deny` 同時命中且 priority 無法裁決 →
  `conflict`。bundle 全檔 canonical digest 可重放。
- **CEL 限制**：compile＋type-check 於載入時完成；cost 採**雙層**——載入時 static
  estimate 超限拒收、執行時 runtime evaluation limit 超限記 `evaluation_error`；host
  extension 只允許純函式（tier 比較）。「無環」不寫成「無歧義」——歧義由 target＋
  priority 的 conflict 判定承擔。violation 聚合為列表、不短路（僅供 explain，見 §4）。
- **輸出**（四值拆開，owner 凍結）：`truth ∈ {true,false,unknown,conflict}`＋
  `status ∈ {ok, evaluation_error}`＋`unknown_leaves[]`／`matched_rule_ids[]`／
  `conflicting_rule_ids[]`；error 不併入 unknown。**Reason graph**：每條規則的命中／未
  命中、false 的 leaf facts、缺的 facts、衝突規則——deterministic，不承諾最小反證。

## 4. Shadow 比對（不接管）

- **比較契約（rev2，取代 rev1 的「violation 集合完全一致」——production 首錯即回、
  error 無 Rule ID，集合等價會迫使再造一套 Go 判定、自我指涉）**：Go oracle 只取
  **實際可觀測結果**——(a) accept／reject；(b) reject 時依既有 precedence（gateDecide
  判定順序＋PrepareDecision／BuildDecision 的首錯即回）產生的 **primary error**（以訊息
  形狀對映到單一 rule id）；(c) accept 時的 `RiskDecisions` 決定性輸出。CEL 端由完整
  violation 列表依同一 precedence 選出 primary 再比對；完整列表僅作 **explain**，不作
  等價條件。
- **Corpus manifest（rev2）**：每筆案例＝`{facts_digest, bundle_digest, oracle_source,
  go_verdict}`，來源明列——(a) `internal/gatepolicy` 測試表（R6–R13、R21–R32）；
  (b) gate service 測試面（R1–R2、R4–R5、R19–R20）；(c) escalation projection 測試面
  （R15–R16）；(d) A9 驗收 workspace 三個 git commit＋gate journal＋escalation journal
  合成的真實案例（stale→override→supersede）；(e) **獨立 dirty-tree fixture**（journal
  無此紀錄——送核在寫 journal 前失敗，不能只靠 journal 重放）。journal 不保存各時點
  current digest，(d) 的 current 值由 manifest 內記錄的 commit OID 重算補齊。
- **Coverage**：每條 in-scope 規則以**隔離案例**（單一違規）證明 Rule ID 覆蓋；另備
  多重違規案例驗 primary-rule precedence。未覆蓋列表必須為空或明列豁免理由。
- **不接管**：spike 全程不掛 production `gateDecide` 路徑、不加 runtime hook；比對只在
  測試／CLI harness 內發生。

## 5. 出口條件（驗證計畫，逐項對應 owner 凍結清單）

1. typed facts schema＋未知欄位拒絕——unit test（多餘欄位 decode 必須紅）。
2. CEL compile／type-check／cost 上限——載入壞 bundle（型別錯、cost 超）必須拒。
3. truth／status 三分不混用——unknown（缺 fact）、conflict（規則衝突 fixture）、
   evaluation_error 各一條 unit test，互不吞併。
4. reason graph determinism——同 facts 兩次評估輸出逐字節相等。
5. corpus 重放：Go 可觀測結果與 CEL primary verdict 逐案例一致（§4 比較契約）＋
   Rule ID coverage 報告（隔離案例）＋primary precedence 多重違規案例；另以
   baseline／candidate 兩個 bundle digest 對同一 corpus 重放，diff 報告輸出
   outcome／unknown／error 三欄的翻轉表。
6. mutation 鑑別力——(a) 改一條 bundle 規則（如 R31 拿掉 override 檢查）→ diff 必須抓
   到翻轉；(b) 移除比對 harness 的 guard → 對應測試紅。
7. facts snapshot 與 bundle 皆有 canonical digest，任一筆 corpus 案例可獨立重放。
8. spike 不接管 production——`gateDecide` 零改動（git diff 佐證）。

## 6. 非目標與時間盒

- 不做 STALE facts（二階段）、TCA consistency（三階段）、Rego／Datalog／Z3、
  `spec/rules/**` scope 擴充、production 掛載。
- 已知缺口不在本 spike 修：`Plan.SpecManifest` 與 active Gate 1 digest 的相等驗證
  （owner 標記待查，仍留待查清單）。
- 時間盒：spec 核准後以工作階段計 **3 個 session** 為上限；GO／NO-GO 以 §5 八項出口
  條件為判準，屆期未齊即帶著 diff 報告收斂為 NO-GO 記錄，不延展。
- **強制停止條件（rev2，自非目標升格）**：一旦發現需要 production schema migration、
  或需要擴 `spec/` scope（如 `spec/rules/**`）才能繼續，**立即 NO-GO**、當場收斂記錄
  ——不因時間盒未滿而續作。

## 附錄 A：規則盤點（R1–R34）

盤點結論以 2026-08-26 的 code 為準（gateDecide 十一步判定順序、R1–R20 eligibility、
R21–R34 risk 決議與送核路徑豁免項），完整表格與 file:line 見同目錄
[`2026-08-26-domainspec-rule-inventory.md`](2026-08-26-domainspec-rule-inventory.md)；
本檔 §1 的取捨以該盤點為依據，實作時引用前先驗 file:line 仍成立。

## 修訂記錄

- rev2（2026-08-26，design gate CHANGES REQUIRED 收斂，4 P1＋3 P2）：
  - P1：比較契約改為 Go 可觀測結果（accept／reject＋primary error＋RiskDecisions），
    CEL 完整 violation 列表降為 explain；coverage 改隔離案例＋precedence 案例。
  - P1：facts 取得改 phase-aware（known／not_applicable／missing），rejected 分支不取
    current／plan facts；acquisition error 移到 evaluator 之外；R11／R12 補「僅
    approved」guard；R15 改由 CEL 獨立導出 scope。
  - P1：補最小規則代數（effect／target／depends_on／priority）與全域 truth 映射，
    conflict 與 SCC 有了可執行模型。
  - P1：corpus manifest 明列五類來源與各規則的 oracle 出處；dirty-tree 用獨立
    fixture（journal 無此紀錄）。
  - P2：snapshot 補 schema_version、presence pointer 規則、集合 canonical ordering、
    nil／空集合等價。
  - P2：盤點附件 R14 改分支敘述（approved 會重跑 planIDFromSubject）、判定順序標題
    更正為 wrapper＋內部十一步。
  - P2：cost 拆 static estimate＋runtime limit；補 baseline／candidate bundle diff；
    owner 停止條件升格為強制 NO-GO。
- rev1（2026-08-26）：初版。
