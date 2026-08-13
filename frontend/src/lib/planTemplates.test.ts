import { describe, it, expect } from 'vitest'
import { templateFor, inScope, SPEC_SCOPE_PATTERNS, PLAN_SCOPE_PATTERNS } from './planTemplates'

describe('templateFor', () => {
  it('plan/risk-policy.yaml -> version + default_tier 骨架', () => {
    const t = templateFor('plan/risk-policy.yaml')
    expect(t).toContain('version: 1')
    expect(t).toContain('default_tier: medium')
  })

  it('plan/oracle-surface.yaml -> version + patterns 骨架', () => {
    const t = templateFor('plan/oracle-surface.yaml')
    expect(t).toContain('version: 1')
    expect(t).toContain('patterns:')
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
