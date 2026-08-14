import { describe, it, expect } from 'vitest'
import { templateFor, inScope, SPEC_SCOPE_PATTERNS, PLAN_SCOPE_PATTERNS } from './planTemplates'

describe('templateFor', () => {
  it('plan/risk-policy.yaml -> version + default_tier 骨架', () => {
    const t = templateFor('plan/risk-policy.yaml')
    expect(t).toContain('version: 1')
    expect(t).toContain('default_tier: medium')
  })

  it('plan/oracle-surface.yaml -> version + patterns 骨架，佔位樣式標明不改會在 TCA 預檢被拒（walkthrough 現場發現缺陷 1）', () => {
    const t = templateFor('plan/oracle-surface.yaml')
    expect(t).toContain('version: 1')
    expect(t).toContain('patterns:')
    expect(t).toContain('path outside allowed scope')
  })

  it('plan/permissions/<id>.yaml -> comment-only（純註解）', () => {
    const t = templateFor('plan/permissions/T1.yaml')
    expect(t.trim().length).toBeGreaterThan(0)
    for (const line of t.split('\n')) {
      if (line.trim() === '') continue
      expect(line.trimStart().startsWith('#')).toBe(true)
    }
    expect(t).toContain('opaque artifact')
  })

  it('plan/<id>.yaml（排除 risk-policy／oracle-surface）-> plan 骨架，plan_id 由檔名帶入', () => {
    const t = templateFor('plan/my-plan.yaml')
    expect(t).toContain('plan_id: my-plan')
    expect(t).toContain('analysis_base_commit: ""')
    expect(t).toContain('test_contract:')
    expect(t).toContain('tasks:')
  })

  // walkthrough 現場發現缺陷 2（docs/spikes/m3a1-results.md）：permissions_ref
  // 預設值原本帶 "plan/" 前綴，但 app.go 的 permissionRefEntries 讀檔時會自行
  // 補上 plan/ 前綴，雙重前綴送核 Gate 2 時 git show 找不到檔案（exit 128）。
  it('plan 骨架的 permissions_ref 相對 plan/，不帶重複的 plan/ 前綴', () => {
    const t = templateFor('plan/my-plan.yaml')
    expect(t).toContain('permissions_ref: permissions/T1.yaml')
    expect(t).not.toContain('permissions_ref: plan/permissions/T1.yaml')
  })

  // walkthrough 現場發現缺陷 3：risk-policy.yaml 骨架的 default_tier: medium
  // 與 plan 骨架 task 的 risk tier 若不一致（原為 low），原樣不改直接送核會被
  // plan.Validate() 判定 risk 分類不符而拒絕。
  it('plan 骨架的 task risk tier 對齊 risk-policy.yaml 骨架的 default_tier: medium', () => {
    const t = templateFor('plan/my-plan.yaml')
    expect(t).toContain('minimum_risk_tier: medium')
    expect(t).toContain('planner_risk_tier: medium')
  })

  it('其他路徑（含 spec/ 路徑）-> 空字串', () => {
    expect(templateFor('spec/features/foo.feature')).toBe('')
    expect(templateFor('README.md')).toBe('')
  })
})

describe('inScope', () => {
  it('spec 四 pattern：dir/** 前綴與精確路徑', () => {
    expect(inScope('spec/features/foo.feature', SPEC_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('spec/nfr/bar.md', SPEC_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('spec/glossary.md', SPEC_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('spec/context-map/baz.md', SPEC_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('spec/other/x.feature', SPEC_SCOPE_PATTERNS)).toBe(false)
    expect(inScope('README.md', SPEC_SCOPE_PATTERNS)).toBe(false)
  })

  it('plan/**：任何 plan/ 底下路徑都在範圍內', () => {
    expect(inScope('plan/my-plan.yaml', PLAN_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('plan/permissions/T1.yaml', PLAN_SCOPE_PATTERNS)).toBe(true)
    expect(inScope('spec/features/foo.feature', PLAN_SCOPE_PATTERNS)).toBe(false)
  })
})
