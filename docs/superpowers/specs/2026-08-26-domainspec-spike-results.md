# DomainSpec kernel 限時 spike——收斂報告（GO／NO-GO）

Spec：[`2026-08-26-domainspec-spike-design.md`](2026-08-26-domainspec-spike-design.md)（e09c3fe APPROVED rev6）
Plan：`docs/superpowers/plans/2026-08-26-domainspec-spike.md`（rev8，8bf3bb3 plan gate APPROVED 第八輪）
Ledger：`.superpowers/sdd/2026-08-26-domainspec-spike/progress.md`

驗證範圍：8bf3bb3（plan 核准點）..3037568（Task 8 完成點）＋本任務（Task 9）新增的
`internal/domainspec/mutation_test.go`。本報告的每一項證據皆為本次任務執行過的指令與
實測輸出（時間戳記 2026-08-27），未實際跑過的一律明標「未驗證」。

---

## 一、八項出口條件逐項證據

以下編號對齊 spec §5。

### 出口 1：typed facts schema＋未知欄位拒絕

- 測試：`TestDecodeFactsSnapshotRejectsUnknownField`（`internal/domainspec/facts_test.go:120`）——逐欄位注入多餘 key，斷言 decode 必須紅。
- 實測：

  ```
  $ go test ./internal/domainspec/... -run TestDecodeFactsSnapshotRejectsUnknownField -v
  --- PASS: TestDecodeFactsSnapshotRejectsUnknownField
  ```

**判定：滿足。**

### 出口 2：CEL compile／type-check／cost 上限＋壞 bundle 拒收

- 測試：`TestLoadBundleRejectsTypeError`、`TestLoadBundleRejectsStaticCostOverLimit`、
  `TestLoadBundleRejectsCrossPhaseDependsOn`（rev5 P1 項——跨 phase `depends_on` 載入時拒收）、
  `TestLoadBundleRejectsCycle`（`internal/domainspec/bundle_test.go`）。
- 實測：

  ```
  $ go test ./internal/domainspec/... -run \
    'TestLoadBundleRejectsTypeError|TestLoadBundleRejectsStaticCostOverLimit|TestLoadBundleRejectsCrossPhaseDependsOn|TestLoadBundleRejectsCycle' -v
  --- PASS: TestLoadBundleRejectsTypeError
  --- PASS: TestLoadBundleRejectsStaticCostOverLimit
  --- PASS: TestLoadBundleRejectsCrossPhaseDependsOn
  --- PASS: TestLoadBundleRejectsCycle
  ```

**判定：滿足。**

### 出口 3：truth／status 三分不混用＋三層 DAG 傳遞

- 測試：
  - `TestEvaluateMissingLeafYieldsUnknown`（missing fact → unknown）
  - `TestEvaluateConflict`（同 priority 反效果 → conflict）
  - `TestEvaluateRuntimeCostLimitIsError`（runtime cost 超限 → status=evaluation_error，不折入 Truth／UnknownLeaves）
  - `TestEvaluateNotEligibleTransitive`（三層 DAG A→B→C：A when 為 false ⇒ B not_eligible（cause=A）⇒ C not_eligible（cause=B，transitive），逐層成因記入 ReasonGraph）
- 實測：`go test ./internal/domainspec/... -v` 全數 60 個測試 PASS、0 FAIL（見下方 §三 全量測試結果）。

**判定：滿足。**

### 出口 4：reason graph determinism

- 測試：`TestEvaluateDeterministicBytes`（`eval_test.go`）——同 facts 兩次評估輸出逐位元組相等。
- 實測：`go test ./internal/domainspec/... -run TestEvaluateDeterministicBytes -v` → PASS。

**判定：滿足。**

### 出口 5：corpus 重放一致性＋coverage＋precedence＋bundle diff

- **Go／CEL 逐案例一致性**：`TestReplayCorpusAllConsistent`（`corpus_test.go:630`）；本次任務額外
  跑根層 `TestOracleFreshnessAllFresh`（`domainspec_oracle_freshness_test.go:681`）——42 筆 corpus
  中 39 筆 evaluated 案例逐一透過**真實 production seam**（`gate_service_submit`／
  `gatepolicy_validate`／`gate_service_prepare`／`gatepolicy_reconcile`／`gatepolicy_build`／
  `escalation`／`app_gatedecide`）重新計算 verdict，與固化值比對零漂移：

  ```
  $ go test . -run TestOracleFreshnessAllFresh -v
  --- PASS: TestOracleFreshnessAllFresh (5.44s)
  ```

- **Coverage 報告**（本次任務以 `ReplayCorpus` 實測，throwaway probe 已刪除，結果如下）：

  | 項目 | 數值 |
  |---|---|
  | Consistent | 39 |
  | Inconsistent | 0 |
  | Exempt（acquisition_failed） | 3 |
  | OutputEvidence（R32） | 1 |
  | Bundle 總規則數 | 25 |
  | UncoveredRules | 空 |

  逐 phase-specific rule id 命中次數（CoveredRules，isolated／precedence 案例合計）：

  | Rule ID | 命中次數 | 備註 |
  |---|---|---|
  | R1 | 1 | |
  | R2 | 1 | |
  | R3 | 2 | isolated-R3 ＋ precedence-R3-vs-R24 |
  | R4 | 1 | |
  | R5.submit | 1 | |
  | R5.decide | 1 | |
  | R6.submit | 1 | |
  | R6.decide | 1 | |
  | R7 | 1 | |
  | R8 | 2 | isolated-R8（source_index=1／kind=plan）＋ precedence-R9-vs-R8-kind-occurrence |
  | R9 | 2 | isolated-R9（source_index=0／kind=spec_manifest）＋ precedence-R9-vs-R8-kind-occurrence |
  | R11 | 2 | isolated-R11 ＋ a9-2-stale-blocked-r11（真實案例） |
  | R12 | 1 | |
  | R16 | 2 | isolated-R16 ＋ precedence-R30-vs-R16 |
  | R21 | 1 | |
  | R24 | 3 | |
  | R25 | 2 | |
  | R26 | 2 | |
  | R27 | 1 | |
  | R28.minimum | 1 | |
  | R28.planner | 1 | |
  | R28.selected | 1 | |
  | R29 | 1 | |
  | R30 | 4 | |
  | R31 | 2 | isolated-R31 ＋ precedence-R31-vs-R26 |

  R8／R9 per-kind 佐證（實測 `Evaluate` 輸出的 `Violation.SourceIndex`，對照
  `required_kinds` 順序 `[spec_manifest, plan, base_commit, risk_policy, permission_manifest]`）：
  `isolated-R9` 命中 `sourceIndex=0`（spec_manifest digest 格式錯）、`isolated-R8` 命中
  `sourceIndex=1`（plan binding 缺失）；`precedence-R9-vs-R8-kind-occurrence` 同時命中兩者，
  驗證「較早 kind（spec_manifest）digest 錯」與「較晚 kind（plan）missing」的 occurrence-rank
  precedence。coverage 計數只認 rule id（R8／R9 各一個 CEL 規則、內部依 per_kind 迴圈展開），
  per-kind 區分只在 SourceIndex／Target 層級，未各自成獨立 rule id——與 bundle 代數設計一致，
  不是遺漏。

- **Primary precedence 多重違規**（spec §4 要求至少三筆，各驗一層邊界）：
  `precedence-R3-vs-R24`（gate step：R3 先於 R24）、`precedence-R30-at-T1-vs-R25-at-T2`
  （source_index：T1 的 R30 先於 T2 的 R25）、`precedence-R31-vs-R26`（build stage：
  task-loop 的 R31 先於 post-loop 的 R26）——另有 `precedence-R24-vs-R30`、
  `precedence-R30-vs-R16`、`precedence-R9-vs-R8-kind-occurrence` 三筆補強，共 6 筆
  precedence 案例，逐筆由 `TestReplayCorpusAllConsistent` 驗證 `PrimaryViolation` 命中預期
  規則。

- **Bundle diff**（baseline／candidate 翻轉表）：`TestDiffBundlesDetectsFlip`
  （`bundlediff_test.go`）——合成 corpus 驗 R31 override 檢查移除後 pass→blocked 翻轉，
  且 baseline==candidate 產生空表。實測：

  ```
  $ go test ./internal/domainspec/... -run TestDiffBundlesDetectsFlip -v
  --- PASS: TestDiffBundlesDetectsFlip
  ```

**判定：滿足。**

### 出口 6：mutation 鑑別力

**(a) bundle 規則翻轉**——本次任務新增 `internal/domainspec/mutation_test.go` 的
`TestMutationBundleRuleFlipsCaught`：對 `testdata/gate2-bundle.yaml` 的 R31 `when` 做
`sel.override_reason == ''` → `false` 替換（讓 R31 整條規則恆假、等同刪除該規則），逐項
檢查 `os.ReadFile`／`LoadBundle`／`DiffBundles` 的 error、斷言 mutated bytes 與 candidate
digest 確實與 baseline 不同，再對 `loadCorpus(t)` 全部 42 筆 corpus 案例跑
`DiffBundles`：翻轉列表恰好只有 `isolated-R31`（`BaselineOutcome=false`(blocked) →
`CandidateOutcome=true`(pass)），任何 `covers_rules` 不含 R31 的案例若翻轉即 fail。

實測（探測階段先以 throwaway probe 確認 mutation 落點，probe 已刪除不落地）：

```
$ go test ./internal/domainspec/... -run TestMutationBundleRuleFlipsCaught -v
--- PASS: TestMutationBundleRuleFlipsCaught (0.07s)
```

本 mutation 與 Task 8 的 `TestDiffBundlesDetectsFlip`（移除 `&& sel.override_reason == ''`
子句、讓 override 檢查失效但其餘四個 AND 分量仍生效）互補：Task 8 驗「override 檢查被
繞過但規則仍存在」；本測試驗「規則整條消失」。兩者對真實 `testdata/corpus/isolated-R31.json`
fixture 的行為不同——Task 8 的 mutation 不會讓該 fixture 翻轉（因為它在 baseline 就已經
觸發 R31，A&&B→A 只是條件變寬，truth 不變），本測試的 `&&false` mutation 才會讓
`isolated-R31.json` 翻轉，這點已於探測階段實測確認、非猜測。

**(b) harness guard 鑑別力**（全程式化，無手動實驗，直接引用既有測試作為證據，未新增
重複實作）：

- `TestOracleFreshnessDetectsCorruption`（`domainspec_oracle_freshness_test.go:692`）——把
  固化 corpus 的某筆 verdict 人工腐化，`VerifyOracleFreshness` 必須點名該筆案例；證明
  freshness guard 沒有被「複製 production 輸出再回填」架空。實測：

  ```
  $ go test . -run TestOracleFreshnessDetectsCorruption -v
  --- PASS: TestOracleFreshnessDetectsCorruption (3.82s)
  ```

- `TestCompareCaseContract`（`internal/domainspec/compare_test.go:78`）——`CompareCase` 的
  mismatch／error 分支覆蓋；比對邏輯若被弱化（例如漏比某個 `GoVerdict` 欄位），對應
  fixture 必須由 match 變 mismatch。實測：

  ```
  $ go test ./internal/domainspec/... -run TestCompareCaseContract -v
  --- PASS: TestCompareCaseContract
  ```

**判定：滿足。**

### 出口 7：canonical digest 重放與排序無關性

- 測試：`TestCanonicalReorderAndDupSameDigest`（`canonical_test.go:8`，contexts／modules
  重排＋含重複值仍得相同 digest）、`TestCorpusBundleDigestMatchesLiveBundle`
  （每筆 corpus 案例的 `bundle_digest` 對得上目前 bundle）、`TestCorpusDigestDriftFailsLoud`
  （facts_digest 或 bundle_digest 漂移必須 fail loud，不是靜默略過）。
- 實測：`go test ./internal/domainspec/... -v` 全數 PASS（見全量測試結果）。

**判定：滿足。**

### 出口 8：spike 不接管 production（`gateDecide` 零改動）

- 指令：

  ```
  $ git diff 8bf3bb3..HEAD --stat -- . \
    ':(exclude)internal/domainspec' ':(exclude)docs' \
    ':(exclude)go.mod' ':(exclude)go.sum' \
    ':(exclude)domainspec_oracle_freshness_test.go'
  ```

  輸出：**空**（實測確認，2026-08-27）。即排除 domainspec 套件、文件、依賴清單、
  root 測試 harness 之外，**沒有任何 production 檔案被改動**——`app.go` 的
  `gateDecide`、`internal/gatepolicy`、`internal/gate` 等所有既有 production 路徑零改動。

- root 層唯一新增檔為 `domainspec_oracle_freshness_test.go`（805 行，`package main`，
  全部 `Test*` 函式，非 production 程式碼——實測 `git diff 8bf3bb3..HEAD --stat
  --diff-filter=A -- . ':(exclude)internal/domainspec' ':(exclude)docs'` 只列出此一檔）。

- go.mod／go.sum 增項僅 cel-go 依賴鏈（無其他依賴變動）：

  ```diff
  --- a/go.mod
  +++ b/go.mod
  @@ -3,6 +3,7 @@ module github.com/slam0504/sdlc-workbench
   go 1.25.0

   require (
  +	cel.dev/cel-go v0.32.0
   	github.com/fsnotify/fsnotify v1.10.1
   	...
   require (
  +	cel.dev/expr v0.25.1 // indirect
   	git.sr.ht/~jackmordaunt/go-toast/v2 v2.0.3 // indirect
  +	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
   	...
  +	go.yaml.in/yaml/v3 v3.0.4 // indirect
   	golang.org/x/crypto v0.51.0 // indirect
  +	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
   	...
  +	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
  +	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
  +	google.golang.org/protobuf v1.36.10 // indirect
   )
  ```

  （go.sum 對應加了同一批套件的 hash，皆為 cel-go v0.32.0 的遞移依賴：
  `cel.dev/expr`、`antlr4-go/antlr`、`go.yaml.in/yaml`、`golang.org/x/exp`、
  `google.golang.org/genproto/*`、`google.golang.org/protobuf`。）

**判定：滿足。**

---

## 二、豁免表（Exemption Table）

| 規則／項目 | 豁免理由 | 佐證 |
|---|---|---|
| R15（scope 導出公式） | 未獨立成 CEL 規則，**完整內嵌**在 R16 的 `when` 內重算（`request.gate`＋`request.subject` 獨立推導 scope，不引用任何 precomputed 值）——scope-sensitive 案例（`isolated-R16`、`precedence-R30-vs-R16`）即為 R15 的可測證據 | `gate2-bundle.yaml` R16 規則區塊註解；rule-inventory §2 R15 |
| R13 | current* 讀取錯誤一律回錯、不當 stale——I/O 錯誤處理，host 層職責，非「facts→verdict」判定 | rule-inventory `R13 current* 讀取錯誤一律回錯，不當 stale — gate2.go:224-228、241-245` |
| R17 | escalation 讀取失敗一律回錯，不當無 blocker——同上，I/O fail-closed 語意 | rule-inventory `R17 escalation 讀取失敗一律回錯 — app.go:5949-5958` |
| R19 | `CommitDecision` 重跑 `Project` 再驗 pending——跨鎖區間的一致性重驗（`s.mu` 在 Prepare／Commit 之間放鎖），屬生命週期／鎖語意，非純函式判定 | rule-inventory `R19 CommitDecision 重跑 Project 再驗 pending — service.go:139-146` |
| R20 | supersession transition——journal 原子 append 與 supersession 合寫，屬 I/O／生命週期 | rule-inventory `R20 supersession — service.go:152-167` |
| R14（lineage） | 分支敘述，非獨立判定規則——描述 R7–R9／R6.decide 在哪個呼叫路徑執行，已由對應 phase-specific 規則（R6.decide 等）承載 | rule-inventory `R14（分支敘述，rev2 更正）` |
| R10 | pending bindings 對 pseudo-record 跑 `ReconcileBindings`（PrepareDecision 內部 dry-run），與 R11 同一套 stale 判定邏輯但作用在不同呼叫點——CEL 端以 R11 的 CEL 重算涵蓋同一語意，R10 本身未獨立進 25 條 CEL 規則清單（**注意**：design §4 corpus manifest 來源列表把 R10 的 oracle 列在 gate service 測試面，但這只是「哪個 Go 測試檔涵蓋」的來源標註，不代表 R10 有獨立 CEL 規則；本報告未對此做進一步交叉驗證，屬**未驗證**的細節，建議 M4.5 若擴大規則面時明確排查） | rule-inventory §2 R10；design §4（b） |
| R18／R22／R23／R33／R34 | 未在 spec §1 的 in-scope 清單內（R18＝寫入失敗即拒屬 I/O；R22 為 R1 在 gate2.go 的重複防線；R23＝git blob 讀取；R33／R34 屬送核路徑 plan schema 驗證，非 decide 路徑） | rule-inventory §3；design §1 in-scope 清單未列 |
| acquisition_failed（3 筆：dirty-worktree／loadat-error／revparse-fatal） | host 邊界——facts 無法組成，不入 CEL 一致性統計，計入 `Exempt` | `ReplayCorpus` 實測 `Exempt=3`；對應 3 個 `testdata/corpus/acquisition-failed-*.json` |
| **R3（不豁免）** | approver identity 檢查已用 App seam（`app_gatedecide`，`recomputeAppGateDecide`）跑真正的 `a.GateDecide`，經 git identity override 驅動 production 路徑，**無 exemption fallback**——若接線不成立即視為 NO-GO（ledger 明載）。實測 `isolated-R3`／`precedence-R3-vs-R24` 皆透過此 seam 重放一致 | `domainspec_oracle_freshness_test.go` `recomputeAppGateDecide`；`TestOracleFreshnessAllFresh` PASS |
| **R32（不豁免）** | Metadata.RiskDecisions 排序輸出——以 `OutputEvidence` 計數，要求 ≥2 task、plan 來源順序非 task_id 序、雙方 RiskDecisions 逐欄比對相等，方計入證據。實測 `clean-decide-approved` 案例貢獻 `OutputEvidence=1`，`CompareCase` 的 pass 分支逐欄比對（非「集合相等」的弱檢查） | `ReplayCorpus` 實測 `OutputEvidence=1`；`compare.go` pass 側比對邏輯 |

---

## 三、全量測試結果

```
$ go test ./... -count=1
ok  	github.com/slam0504/sdlc-workbench	233.170s
ok  	github.com/slam0504/sdlc-workbench/cmd/probe-codex-parallel	2.206s
ok  	github.com/slam0504/sdlc-workbench/internal/appcore	5.690s
ok  	github.com/slam0504/sdlc-workbench/internal/approval	3.166s
ok  	github.com/slam0504/sdlc-workbench/internal/assist	29.879s
ok  	github.com/slam0504/sdlc-workbench/internal/claude	3.921s
ok  	github.com/slam0504/sdlc-workbench/internal/codex	2.121s
ok  	github.com/slam0504/sdlc-workbench/internal/contract	4.248s
ok  	github.com/slam0504/sdlc-workbench/internal/domainspec	5.176s
ok  	github.com/slam0504/sdlc-workbench/internal/escalation	5.028s
ok  	github.com/slam0504/sdlc-workbench/internal/evidence	37.385s
ok  	github.com/slam0504/sdlc-workbench/internal/gate	6.403s
ok  	github.com/slam0504/sdlc-workbench/internal/gatepolicy	10.005s
ok  	github.com/slam0504/sdlc-workbench/internal/journal	6.105s
ok  	github.com/slam0504/sdlc-workbench/internal/plan	7.619s
ok  	github.com/slam0504/sdlc-workbench/internal/proc	21.183s
ok  	github.com/slam0504/sdlc-workbench/internal/recorder	5.112s
ok  	github.com/slam0504/sdlc-workbench/internal/replayindex	7.738s
ok  	github.com/slam0504/sdlc-workbench/internal/singleinstance	2.630s
ok  	github.com/slam0504/sdlc-workbench/internal/spec	23.762s
ok  	github.com/slam0504/sdlc-workbench/internal/wirelog	19.803s
ok  	github.com/slam0504/sdlc-workbench/internal/wsregistry	22.322s
[exited with code 0]
```

`internal/domainspec` 套件單獨以 `-v` 執行：60 個測試全數 PASS、0 FAIL（實測，2026-08-27）。

`gofmt -l internal/domainspec/ .` 對本任務新增的 `mutation_test.go` 與既有
`internal/domainspec/` 目錄結果為空（clean）。**注意**：repo 根目錄 `gofmt -l .`
對整個專案跑會列出 5 個既有檔案（`cmd/probe-codex-parallel/main.go`、
`internal/appcore/manager_test.go`、`internal/appcore/manager_wsid_test.go`、
`internal/codex/session.go`、`internal/replayindex/corrupt_test.go`）——這些皆為
**本 spike 之前既有的 repo 債務**（`git status --short` 確認本任務只新增
`internal/domainspec/mutation_test.go`，未觸碰上述 5 檔），不在本 spike 的
constraint 範圍內，本報告如實揭露、不隱藏。

`go vet ./internal/domainspec/...`：clean（無輸出）。

---

## 四、契約變更裁決摘要（Ruling Summary，摘自 ledger）

以下五項為 plan 執行過程中對凍結契約的裁決，均已記錄於 ledger 並經 review round 確認：

1. **sel → `cel.DynType`**（Task 4）：plan 原定 `sel: map(string,dyn)` 與「sel 可為
   null」自我矛盾（cel-go v0.32.0 拒絕 `map==null`）；改為 `DynType` 保留 nullable
   語意，R25/R28.selected/R30/R31 的 `sel != null` 守門不受影響。
2. **target enum 擴充 `binding.kind`**（Task 3）：per_kind 規則（R8/R9）專用 target，
   與既有 `risk.task`（per_task）對稱；雙向一致性已驗（`per_kind==true ⇔
   target=="binding.kind"`）。
3. **Violations 限 deny-only**（Task 4）：spec「完整 violation 列表」界定為全部
   **deny** 命中，不含 allow（allow 資訊留在 `matched_rule_ids`／reason graph）——
   explain 完整性不受影響，Task 6 primary 選擇邏輯無須 allow 資訊。
4. **混合 cardinality `depends_on` 拒收**（Task 4）：per_task／per_kind／scalar 之間
   跨基數依賴於 `LoadBundle` 直接拒收，消除 `lookupDependency` 的 index 碰撞風險；
   gate2 bundle 本身不用 `depends_on`，零成本。
5. **phaseRank 作第 0 層 tiebreak**（Task 6）：step_rank 表 submit／decide 數值有重疊
   （如 `R7=2` 與 `R1=2`），`PrimaryViolation` 明確以 phase rank（submit=0 <
   decide=1）為最高優先層，不依賴 bundle 宣告順序的巧合排序。

另有兩項語意必要但 brief 未列出的補強守門（Task 5，已加註明確理由）：
`R6.decide` 補 `request.gate=='gate2'` 守門（否則 gate1／TCA 誤觸）；R16 補齊
`test_contract_approval` 分支與 TCA fallback（首版漏 tca 分支，已對照 production 修正）。

---

## 五、剩餘風險（Residual Risks）

1. **`step_rank`／`check_rank` 人工凍結表**：R1–R31 的 phase／build-stage／
   source_index／check-rank 四層 precedence，是 controller 逐行對照
   `internal/gatepolicy/gate2.go`／`internal/gate/service.go` 的判定序**人工**轉譯
   進 bundle YAML 的常數欄位（`step_rank`／`stage`／`check_rank`），並在 Task 5／6
   review round 手動 trace 確認（ledger 標「已對照 production」）。**沒有自動化
   linter 或 CI 檢查**在 production 判定序變動時同步提醒 bundle 需要更新——若未來
   `gateDecide`／`BuildDecision` 的檢查序重排，bundle 的 precedence 表可能靜默過期。
   建議 M4.5 若採用此 kernel，補一道「production 判定序 vs bundle step_rank」的
   自動化交叉檢查（例如 AST 掃描呼叫序或至少加 code owner review checklist 項）。
2. **`R10` 是否需要獨立 CEL 規則**：design §4 corpus manifest 來源列表把 R10 的
   oracle 列在 gate service 測試面，但 25 條 CEL 規則清單內沒有獨立的 R10——本報告
   未進一步交叉驗證這是「R11 已涵蓋同一語意」還是遺漏（見 §二豁免表 R10 列）。
   **未驗證**，建議 M4.5 擴大規則面前先排查。
3. **Deferred minors（風格／防禦性/未測分支，均已由 reviewer 認可延後、不影響
   correctness）**，摘自 ledger（完整清單見 `.superpowers/sdd/2026-08-26-domainspec-
   spike/progress.md`）：
   - `facts.go`：`validateSubmitMatrix` 十段 if 可表驅動（風格）；decide phase
     `Decision.Presence==Missing` 分支無測試（邏輯已手動 trace 認可）；
     `ValidateFactsSnapshot(nil)` 防禦分支超出 brief。
   - `canonical.go`：`sortDedupeStrings` 對已 deep-copy 的 slice 再複製一次
     （冗餘配置）；非 nil slice 的 no-op 回寫。
   - `bundle.go`：`LoadBundle` doc comment 順序與實際執行序不符；static cost 用
     `CostEstimate.Max`（保守選擇，brief 未指定——**建議 owner 在 M4.5 若採用時
     重新確認此選擇**）。
   - `eval.go`：Violation doc comment 用字與 deny-only 行為不完全對齊（Task 6
     已載明正確語意，需補一行修正 comment）；`checkOwnPresence` missing 優先於
     not_applicable 的取捨未測；`factGroupOrder` 與 `celTopLevelVars` 無同步檢查。
   - `gate2_bundle_test.go`：註解引用不存在的 `findBinding` helper；無
     decide/rejected clean-pass 案例（R21 唯一 rejected-gated 規則）；R16 隔離案例
     只測 `state=open`，`acknowledged` 未測（表達式已正確涵蓋，未逐分支測）；R16
     gate1/unknown-gate 分支無 positive-hit fixture。
   - `compare.go`：`phaseRank` 未知分支用位運算取 MaxInt（可讀性）；
     `BuildShadowRiskDecisions` 無配對 selection 時補空欄位的選擇未測（R25 保證
     合法案例不可達，已記文件）。
   - `corpus_test.go`（Task 7）：output 案例 `go_verdict` 由
     `BuildShadowRiskDecisions` 產生（與 `CompareCase` want 同源，排序性質靠
     `compare_test.go` 獨立單元測試支撐）；`TestCoverageComplete` 豁免表分支目前
     dead code（豁免規則不在 bundle，`UncoveredRules` 空集合即過，已誠實揭露）。
   - Root 側（Task 7b）：`writeCorpusBatch` 寫檔無 rollback（手動觸發、風險低，
     建議 temp-then-rename）；`stubGate2Policy` 為不可達防禦碼（四案例皆早退，
     註解已自我揭露）。
   - `bundlediff.go`（Task 8）：`DiffBundles` 的 row order 依 caller slice 序
     （與 `ReplayCorpus` 慣例一致，非缺陷）。
4. **A9 真實案例的建模假設**：3 筆 A9 案例（`a9-1-initial-approved`／
   `a9-2-stale-blocked-r11`／`a9-3-corrected-approved-override`）合成自驗收
   workspace 的 3 個 git commit＋gate journal＋escalation journal（stale→
   override→supersede 流程），journal 本身不保存各時點的 `current` digest，是由
   manifest 記錄的 commit OID **重算**補齊——這個重算路徑假設對應 commit 仍可由
   `git show` 存取（worktree 未被 GC／rebase 掉）。若未來該驗收 workspace 被清理，
   這 3 筆案例將無法重新生成（`UPDATE_CORPUS=1` regen 會失敗），需要重新從新的
   驗收案例合成或改用完全 synthetic fixture。
5. **`escalations`／entry 建模的邊界案例密度**：alignment 案例只有兩筆
   （`alignment-R11-role-not-participating`、`alignment-R16-hard-ignored`，對齊
   spec §1 的兩個「對齊警訊」），涵蓋的是已知的兩個 production 行為細節，不是
   escalation／entry 欄位全狀態空間的窮舉——若 M4.5 擴大規則面，這塊需要視新規則
   需求擴充。
6. **`sel`／`risk_selections` presence 缺口（final review 發現，已於本 wave
   修復）**：per_task 規則的 own-presence gate（`checkOwnPresence`／
   `refFactGroups`）原本只用 CEL 頂層變數名比對 `factGroupOrder`，而 `sel` 是
   `findSelection` 由 `risk_selections` 衍生出的變數、本身不在 `factGroupOrder`
   裡，導致 `RiskSelections.Presence==Missing`（decide presence matrix 合法容許）
   時，引用 `sel` 的規則（如 R25）把 `findSelection` 回傳的 `nil` 誤判成「已知但
   無選擇」而 deny，而非 spec §2 要求的 unknown。已在 `refFactGroups` 把 RefVars
   含 `sel` 映射到 `risk_selections` 群組修復，回歸測試
   `TestEvaluatePerTaskSelMissingYieldsUnknown`（`internal/domainspec/eval_test.go`）
   涵蓋，並以 revert-check 確認該測試在修復前會失敗。

---

## 六、GO／NO-GO 判定

**GO。**

八項出口條件（spec §5）逐項以本次任務實際執行的指令與輸出驗證，全數滿足：
`internal/domainspec` 套件 60 個測試、root 層新增 harness 全數 PASS，`go test ./...
-count=1` 全 repo 綠燈（exit code 0），`gofmt -l`／`go vet` 對本任務改動範圍 clean，
`git diff` 佐證 production 路徑（含 `app.go` 的 `gateDecide`）零改動、依賴變更僅
cel-go 依賴鏈。R3／R32 兩條「不得豁免」的規則皆有實際證據（真實 App seam 重放、
逐欄輸出比對），42 筆 corpus（39 evaluated＋3 acquisition_failed）零 shadow
misalignment。

### M4.5 建議

1. **規則面擴充前先處理 §五殘餘風險第 1、2 項**：`step_rank` 精確凍結表與
   production 判定序目前靠人工 review 對齊，擴大規則面（例如涵蓋 STALE facts／
   TCA consistency）前，應先補自動化交叉檢查機制，避免判定序漂移時 bundle 靜默
   過期；同時排查 R10 是否需要獨立 CEL 規則。
2. **`spec/` scope 擴充需另立提案**：本 spike 全程未動 `spec/rules/**`
   manifest scope（design §6 非目標），M4.5 若要讓 bundle 檔案可經 spec 審核流程
   管理，需要另外評估 scope 擴充的影響面，屬於獨立決策，不應隨此 spike 收斂
   自動放行。
3. **A9 真實案例的可重現性**（§五第 4 項）：若 M4.5 決定保留 A9 案例作為長期
   回歸測試，建議把驗收 workspace 的 3 個 commit 做成穩定 fixture repo（而非依賴
   現存、可能被清理的 workspace），避免未來重放失敗。
4. **`CostEstimate.Max` 的靜態成本估計選擇**（§五第 3 項 `bundle.go` deferred
   minor）：目前是實作者在 brief 未明確指定下的保守選擇，M4.5 正式採用前建議
   owner 明確 sign-off 這個選擇（或改用更精確的估計策略）。`gate2-bundle.yaml`
   實際採用的 static cost limit（50,000,000）刻意調得寬鬆，是為了容納 R27
   雙層 `exists` 重算在 `fixedSizeCostEstimator`（固定 size hint 64）下數千萬量級
   的靜態估計值，機制本身仍由 `TestLoadBundleRejectsStaticCostOverLimit` 驗證。
5. **豁免表 R13／R17／R19／R20 等 host 層規則**：這些規則的正確性目前完全依賴
   既有 production 測試（`internal/gate`／`internal/gatepolicy` 套件既有測試），
   不在本 spike 的 shadow 對比範圍內——M4.5 若要把這些規則也納入 CEL shadow，
   需要額外設計「I/O 結果先解析成 facts」的路徑（design §1 已預留此原則，但尚未
   實作）。
