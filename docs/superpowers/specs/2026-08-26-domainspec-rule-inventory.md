# Gate 2 決議規則面盤點（DomainSpec spike 附件，2026-08-26）

以當日 code 為準（HEAD 4cb19b2）。供 shadow evaluator 逐條對齊；引用時先驗 file:line
仍成立。

## 1. gateDecide 判定順序（十一步）

0. `beginTxn`/`endTxn`（binding wrapper）app.go:5786-5792
1. `ensureGate()`（惰性建 journal＋registry；`stateBlockedErr` 擋在 once 前）app.go:5806、3842-3899
2. git identity → approver（name/email 皆空即拒）app.go:5810-5821
3. `a.workflowMu.Lock()`（整段臨界區）app.go:5823-5824
4. `reconcileLocked`：List(Reconcile+Project) → journal-degraded 補建 → stale 補建 escalation app.go:5825、5978-6017
5. `svc.PrepareDecision`：enum → reject-reason → Project → pending → normalizeRequest → registry → (approved) ReconcileBindings 現值重算 → BuildDecision → 組 record（不 append）app.go:5828-5832；internal/gate/service.go:85-125
6. `scopeForSubject(gate, subject)` app.go:5833、5895-5912
7. 2b 修復解除（僅 approved）：`escResolveByKeyLocked("stale:<gate>:<subject>", "superseded-by:<id>")`，寫入失敗即拒 app.go:5834-5843
8. `escBlockingForLocked(scope)` app.go:5844-5847、5949-5959
9. blocker 非空 → 拒（`blocked by N escalation item(s)`）app.go:5848-5850
10. `decideBarrierHook`（測試 seam）app.go:5851-5853
11. `svc.CommitDecision`：重驗 pending → supersession → 單一 gate_op append → EmitGateEvent service.go:135-175

## 2. Eligibility 規則（R1–R20）

- R1 decision ∈ {approved,rejected}，否則 `unknown decision` — service.go:86-88
- R2 rejected 需 reason 非空（`ErrRejectNeedsReason`）— service.go:89-91
- R3 approver：git user.name 或 user.email 至少一非空 — app.go:5814-5820
- R4 projection entry 存在且 Request!=nil 且 Record==nil（pending；`ErrNotPending`）— service.go:98-101
- R5 req.Gate 必須在 registry（gate1/gate2/test_contract_approval）— service.go:103-106；registry app.go:3888-3894
- R6 gate2 subject 需 `plan:` 前綴且 id 非空 — gate2.go:124-127、253-259
- R7 bindings (kind,role) 不得重複 — gate2.go:284-291
- R8 必備 5 kind：spec_manifest/plan/base_commit/risk_policy/permission_manifest（role 皆 ""）— gate2.go:42-48、292-305
- R9 digest 格式：manifest 類 `^sha256:[0-9a-f]{64}$`；base_commit `^git:(sha1:40|sha256:64)$` — gate2.go:28-31、296-299
- R10（僅 approved）pending bindings 對 pseudo-record 跑 ReconcileBindings，causes 必須空 — service.go:107-116
- R11 spec_manifest/plan/risk_policy/permission_manifest：bound digest ≠ "" 時必須 == 現值重算（stale cause `<kind> changed`）— gate2.go:215-232
- R12 base_commit `rev-parse --verify --quiet ^{commit}`：exit1 = stale `base_commit missing`；其餘 fail closed — gate2.go:234-248
- R13 current* 讀取錯誤一律回錯，不當 stale — gate2.go:224-228、241-245
- R14 R6-R9＋lineage 只在 ValidateRequest（送核）跑，decide 不重跑 — gate2.go:91-108；service.go:52
- R15 scope 導出：gate1→workspace；gate2 `plan:<id>`→`gate2:<id>`；tca→`tca:<p>/<t>`；未知→workspace（最寬）— app.go:5895-5912
- R16 blocking：State != resolved（open/acknowledged 都擋）且 BlockScope!="" 且（=="workspace" 或 ==scope）— escalation/project.go:81-96
- R17 escalation 讀取失敗一律回錯，不得視為無 blocker — app.go:5949-5958
- R18 approved 前 stale 修復解除寫入失敗 → 拒核 — app.go:5839-5842
- R19 CommitDecision 重跑 Project 再驗 pending — service.go:139-146
- R20 supersession：Active 且 SupersessionKey==gate+"|"+subject → superseded transition 同一 gate_op — service.go:152-167；key gate2.go:197-199

## 3. Risk 決議規則（R21–R34）

- R21 rejected 不得帶 RiskSelections — gate2.go:114-118
- R22 decision 未知值拒 — gate2.go:120-122
- R23 plan/risk-policy 由 base_commit 去前綴 OID `LoadAt` — gate2.go:128-132、270-281
- R24 同 task_id selection 不得重複 — gate2.go:134-140
- R25 committed plan 每個 task 都要有 selection — gate2.go:143-147
- R26 不得有 plan 不存在的 task_id（排序列出）— gate2.go:183-190
- R27 `pol.ComputeMinimum(t) == t.MinimumRiskTier`（rules match contexts∪modules 取交集、取最高 tier；無 match → DefaultTier）— gate2.go:150-153；internal/plan/riskpolicy.go:23-41
- R28 minimum/planner/selected ∈ {low,medium,high} — gate2.go:154-168；tierOrder gate2.go:26
- R29 planner ≥ minimum — gate2.go:162-164
- R30 selected ≥ minimum — gate2.go:169-171
- R31 selected < planner 需 override_reason 非空（selected ≥ planner 時不檢查）— gate2.go:172-174
- R32 Metadata.RiskDecisions 依 task_id 昇冪，每筆帶四欄（決定性輸出）— gate2.go:175-193；internal/gate/types.go:21-30
- R33 plan.Validate 只在送核路徑跑；risk 類錯誤以 `ErrRiskUnclassifiable` 開 `risk-unclassifiable:<planID>` hard escalation（scope `gate2:<planID>`），decide 時以 R16 blocker 生效 — plan/validate.go:24-84；app.go:5048-5068
- R34 selected_risk_tier/override_reason 不在 plan schema，Parse 即拒 — plan/parse.go:11、validate.go:14-23

## 4. 不進 CEL（I/O／鎖／生命週期）

git 子行程（rev-parse／show／merge-base／config）；worktree digest 重算（risk policy、
permission manifest、spec manifest）；gate journal 原子 append＋supersession 合寫；
escalation 寫入；`workflowMu` 全程臨界區；`gate.Service.s.mu`（Prepare 與 Commit 各一次，
之間放鎖，故 Commit 重驗 pending）；`ensureGate`/`ensureEscalation` once＋stateBlockedErr；
`beginTxn`；`decideBarrierHook`；EmitGateEvent。

## 5. 對齊警訊

- `escalation.BlockingFor` 忽略 `hard`——hard 只影響 UI 可否手動 resolve，不影響擋不擋。
- `bindingDigest` 只匹配 `Role == ""` 的 binding（gate2.go:261-268）。
