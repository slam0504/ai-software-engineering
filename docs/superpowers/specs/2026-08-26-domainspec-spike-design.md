# DomainSpec kernel 限時 spike——Gate 2 shadow evaluator 設計

Owner 2026-08-21 仲裁凍結的方向（見 auto memory `domainspec-spike-direction`）：**獨立、
限時**的 spike，切點是 Gate 2 decision eligibility＋risk policy 的 **shadow evaluator**
——immutable facts snapshot 評估、現行 Go 實作保持權威、新 kernel 只比對不接管；不重塑
M4。第二階段 STALE facts、第三階段 TCA consistency 不在本 spike。本文件 rev1。

## 1. 範圍：要 shadow 的規則面

規則面已逐條盤點（2026-08-26，對照 `internal/gatepolicy/gate2.go`／`internal/gate/
service.go`／`internal/escalation/project.go`／app.go `gateDecide`，R1–R34 完整清單見
本檔附錄 A）。Spike 只 shadow **純 facts→verdict** 的子集：

- **Eligibility**：decision enum 與 reject-reason（R1–R2）、pending 狀態（R4）、gate 種類
  與 subject 形狀（R5–R6）、binding 集合完整性與 digest 格式（R7–R9）、stale 比對
  （R11——bound digest 對 snapshot 內的現值 digest 做相等比較）、base_commit 存在性
  （R12 的三態結果作為 fact 輸入）、blocking escalation（R15 scope 導出＋R16 覆蓋判定）。
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
- `current{spec_manifest, plan_manifest, risk_policy, permission_manifest}`（digest 字串；
  host 重算後填入）
- `base_commit_state ∈ {ok, missing, error}`（error ⇒ host 已 fail closed，不評估）
- `plan{tasks[]{id, minimum_risk_tier, planner_risk_tier, impact{contexts,modules}}}`
- `risk_policy{default_tier, rules[]{match{contexts,modules}, tier}}`
- `risk_selections[]{task_id, selected_risk_tier, override_reason}`
- `scope`（host 以 `scopeForSubject` 導出）＋`escalations[]{state, block_scope}`

Snapshot 以 canonical JSON 序列化並取 sha256 digest（欄位排序固定），可重放；decode 採
`DisallowUnknownFields` 語意——未知欄位拒絕（fail loud），缺欄位記入 `unknown_leaves`。

## 3. Rule bundle 與 evaluator 輸出

- **Bundle**：YAML 檔（spike 期間放 `internal/domainspec/testdata/gate2-bundle.yaml`，
  不動 `spec/` scope——manifest ScopePatterns 不含 `spec/rules/**`，擴 scope 是 M4.5 的
  事）。每條規則：`id`（穩定，沿用附錄 A 的 R 編號）、`when`（CEL，型別檢查過的布林
  式）、`verdict`（violation 訊息模板）、`refs`（對應 production file:line）。bundle 全
  檔 canonical digest 可重放。
- **CEL 限制**：compile＋type-check 於載入時完成；evaluation cost 上限；host extension
  只允許純函式（tier 比較）。相依圖 SCC 驗無環——「無環」不寫成「無歧義」，規則間
  precedence 明確：violation 聚合為列表（與 Go 端 collection-style 一致），不做短路。
- **輸出**（四值拆開，owner 凍結）：`truth ∈ {true,false,unknown,conflict}`＋
  `status ∈ {ok, evaluation_error}`＋`unknown_leaves[]`／`matched_rule_ids[]`／
  `conflicting_rule_ids[]`；error 不併入 unknown。**Reason graph**：每條規則的命中／未
  命中、false 的 leaf facts、缺的 facts、衝突規則——deterministic，不承諾最小反證。

## 4. Shadow 比對（不接管）

- **Corpus 產生**：(a) 把 `internal/gatepolicy` 既有測試表匯出成 facts snapshots；
  (b) A9 驗收 workspace（`~/playground/wb-accept-a9g2`）的 gate journal 重放——含
  dirty→stale→override→supersede 全循環的真實 facts。每筆 corpus 案例＝
  `{facts_digest, bundle_digest, go_verdict}`。
- **比對器**：測試 harness 逐筆跑 CEL evaluator，比 `go_verdict`（accept／每條 violation
  的 rule id 集合）——**完全一致**才過；diff 輸出 outcome／unknown／error 三欄＋
  Rule ID coverage（附錄 A 中每條 in-scope 規則至少被一筆 corpus 案例覆蓋，未覆蓋列表
  必須為空或明列豁免理由）。
- **不接管**：spike 全程不掛 production `gateDecide` 路徑、不加 runtime hook；比對只在
  測試／CLI harness 內發生。

## 5. 出口條件（驗證計畫，逐項對應 owner 凍結清單）

1. typed facts schema＋未知欄位拒絕——unit test（多餘欄位 decode 必須紅）。
2. CEL compile／type-check／cost 上限——載入壞 bundle（型別錯、cost 超）必須拒。
3. truth／status 三分不混用——unknown（缺 fact）、conflict（規則衝突 fixture）、
   evaluation_error 各一條 unit test，互不吞併。
4. reason graph determinism——同 facts 兩次評估輸出逐字節相等。
5. corpus 重放：Go 與 CEL 完全一致＋Rule ID coverage 報告。
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

## 附錄 A：規則盤點（R1–R34）

盤點結論以 2026-08-26 的 code 為準（gateDecide 十一步判定順序、R1–R20 eligibility、
R21–R34 risk 決議與送核路徑豁免項），完整表格與 file:line 見同目錄
[`2026-08-26-domainspec-rule-inventory.md`](2026-08-26-domainspec-rule-inventory.md)；
本檔 §1 的取捨以該盤點為依據，實作時引用前先驗 file:line 仍成立。

## 修訂記錄

- rev1（2026-08-26）：初版。
