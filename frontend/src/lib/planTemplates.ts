// planTemplates.ts（M3a.1 Task 4，spec §3.1 SC4 缺口 1）：SpecWorkspace／
// PlanWorkspace「新增檔案」inline 列共用的兩件事——
//
// 1. templateFor(path)：精確路徑 → 初始內容映射（brief §3.1.4，五種情形）：
//    - plan/risk-policy.yaml → version＋default_tier 骨架
//    - plan/oracle-surface.yaml → version＋patterns 骨架
//    - plan/permissions/<id>.yaml → comment-only（純註解，permissions 是
//      opaque artifact，見 app.go permissionManifestScope／taskPermissionRefs）
//    - plan/<id>.yaml（排除前兩者）→ plan 骨架，plan_id 由檔名 stem 帶入
//    - 其他（含所有 spec/ 路徑）→ 空字串
//    plan 骨架的欄位名稱／巢狀結構對齊 internal/plan/types.go 的 yaml tag，能
//    被 internal/plan.Parse 成功解碼（KnownFields(true)，欄位名打錯會直接
//    拒絕解碼）。analysis_base_commit 刻意留空——這是 Planner 分析基準的
//    commit OID，骨架本身生不出這個值，之後 lineage／送核驗證（VerifyLineage）
//    會因此擋下：這是預期行為，操作者需自行補上才能通過驗證，不是 bug。
//
// 2. inScope／SPEC_SCOPE_PATTERNS／PLAN_SCOPE_PATTERNS：「新增檔案」inline 列
//    輸入路徑當下的即時 scope 預驗，dir/** 前綴／精確路徑比對語意鏡射
//    internal/spec.specInScope（spec 四 pattern）與 spec.PlanScope.Match
//    （plan/**）。這裡只做 UI 提示用——真正權威的邊界驗證仍在後端
//    SpecWrite/PlanWrite（spec.InScope／spec.PlanScope.Match），送出後端仍會
//    重新檢查一次。

export const SPEC_SCOPE_PATTERNS = ['spec/features/**', 'spec/nfr/**', 'spec/glossary.md', 'spec/context-map/**']
export const PLAN_SCOPE_PATTERNS = ['plan/**']

export function inScope(path: string, patterns: string[]): boolean {
  const rel = path.replace(/^\.\//, '')
  return patterns.some(p => {
    if (p.endsWith('/**')) {
      const dir = p.slice(0, -3) // "plan/**" -> "plan"
      return rel === dir || rel.startsWith(dir + '/')
    }
    return rel === p
  })
}

function riskPolicySkeleton(): string {
  return `version: 1
default_tier: medium
rules: []
`
}

function oracleSurfaceSkeleton(): string {
  return `version: 1
patterns:
  - "example/oracle/**"  # 改成需要 lineage 保護的 test/oracle 檔案或目錄，dir/** 或精確路徑——
    # 不改此值送核後，Gate 2 本身不會擋下；等到 TCA 預檢（ValidateTestCommit／RunEvidence）
    # 對實際 test_contract.command.argv 指到的檔案做 lineage 檢查時，才會因該檔案不在
    # example/oracle/** 範圍內，拒絕並回「path outside allowed scope」（實機重現見
    # docs/spikes/m3a1-results.md 現場發現缺陷 1）
`
}

function permissionsCommentOnly(): string {
  return `# 權限清單（opaque artifact）——本檔內容不由前端或 plan schema 驗證格式，
# task.permissions_ref 只要求指向此檔的 canonical manifest digest 存在即可
# （見 app.go permissionManifestScope／taskPermissionRefs）。請依實際權限需求
# 填寫，格式由使用方（gate policy 或外部授權系統）自行約定。
`
}

function planSkeleton(planId: string): string {
  return `plan_id: ${planId}
analysis_base_commit: ""  # 填入 Planner 分析基準的 commit OID——留空會在 lineage／送核驗證階段被擋下（預期行為，需操作者補上）
spec_manifest: ""
risk_policy: plan/risk-policy.yaml
tasks:
  - id: T1
    title: ""
    scenarios: []
    depends_on: []
    impact:
      contexts: []
      modules: []
    completion: []
    minimum_risk_tier: medium  # 對齊 risk-policy.yaml 骨架的 default_tier: medium——兩份骨架都
    planner_risk_tier: medium  # 原樣不改直接送核時，plan.Validate() 才不會判定 risk 分類不符
    permissions_ref: permissions/T1.yaml  # 相對 plan/（非 "plan/permissions/T1.yaml"）——
    # app.go 的 permissionRefEntries 讀檔時會自行補上 plan/ 前綴，帶前綴會變成雙重路徑
    # plan/plan/permissions/T1.yaml，git show 找不到檔案（實機重現見
    # docs/spikes/m3a1-results.md 現場發現缺陷 2）
    test_contract:
      command:
        executable: ""
        argv: []
      expected_failure:
        test_ids: []
        matcher: ""
`
}

export function templateFor(path: string): string {
  if (path === 'plan/risk-policy.yaml') return riskPolicySkeleton()
  if (path === 'plan/oracle-surface.yaml') return oracleSurfaceSkeleton()
  if (/^plan\/permissions\/[^/]+\.yaml$/.test(path)) return permissionsCommentOnly()
  const m = /^plan\/([^/]+)\.yaml$/.exec(path)
  if (m) return planSkeleton(m[1])
  return ''
}
