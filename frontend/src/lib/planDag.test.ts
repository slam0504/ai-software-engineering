import { describe, it, expect } from 'vitest'
import { parsePlanDoc, planToMermaid } from './planDag'

function planYaml(opts: { title2?: string; dependsOn2?: string } = {}): string {
  const title2 = opts.title2 ?? 'Task 2'
  const dependsOn2 = opts.dependsOn2 ?? '[T1]'
  return '' +
    'plan_id: P1\n' +
    'analysis_base_commit: deadbeef\n' +
    'spec_manifest: sha256:aaa\n' +
    'risk_policy: sha256:bbb\n' +
    'tasks:\n' +
    '  - id: T1\n' +
    '    title: Task 1\n' +
    '    scenarios: []\n' +
    '    depends_on: []\n' +
    '    impact:\n' +
    '      contexts: []\n' +
    '      modules: []\n' +
    '    completion: []\n' +
    '    minimum_risk_tier: low\n' +
    '    planner_risk_tier: low\n' +
    '    permissions_ref: permissions/T1.yaml\n' +
    '    test_contract:\n' +
    '      command:\n' +
    '        executable: go\n' +
    '        argv: []\n' +
    '      expected_failure:\n' +
    '        test_ids: []\n' +
    '        matcher: FAIL\n' +
    '  - id: T2\n' +
    `    title: ${title2}\n` +
    '    scenarios: []\n' +
    `    depends_on: ${dependsOn2}\n` +
    '    impact:\n' +
    '      contexts: []\n' +
    '      modules: []\n' +
    '    completion: []\n' +
    '    minimum_risk_tier: medium\n' +
    '    planner_risk_tier: medium\n' +
    '    permissions_ref: permissions/T2.yaml\n' +
    '    test_contract:\n' +
    '      command:\n' +
    '        executable: go\n' +
    '        argv: []\n' +
    '      expected_failure:\n' +
    '        test_ids: []\n' +
    '        matcher: FAIL\n'
}

describe('parsePlanDoc', () => {
  it('解析 plan_id 與 tasks（id/title/depends_on/風險 tier）', () => {
    const doc = parsePlanDoc(planYaml())
    expect(doc?.planId).toBe('P1')
    expect(doc?.tasks).toHaveLength(2)
    expect(doc?.tasks[0]).toEqual({ id: 'T1', title: 'Task 1', dependsOn: [], minimumRiskTier: 'low', plannerRiskTier: 'low' })
    expect(doc?.tasks[1].dependsOn).toEqual(['T1'])
  })

  it('depends_on 為 block list 形式也能解析', () => {
    const yaml = planYaml({ dependsOn2: '' }).replace('    depends_on: \n', '    depends_on:\n      - T1\n')
    const doc = parsePlanDoc(yaml)
    expect(doc?.tasks[1].dependsOn).toEqual(['T1'])
  })

  it('非法輸入（缺 plan_id）回傳 null', () => {
    expect(parsePlanDoc('tasks:\n  - id: T1\n    title: x\n')).toBeNull()
  })

  it('非法輸入（缺 tasks）回傳 null', () => {
    expect(parsePlanDoc('plan_id: P1\n')).toBeNull()
  })

  it('非法輸入（task 缺 id）回傳 null', () => {
    expect(parsePlanDoc('plan_id: P1\ntasks:\n  - title: no id\n')).toBeNull()
  })

  it('空字串回傳 null', () => {
    expect(parsePlanDoc('')).toBeNull()
  })
})

describe('planToMermaid', () => {
  it('兩 task 一依賴 → 輸出含 T1 --> T2', () => {
    const doc = parsePlanDoc(planYaml())!
    const out = planToMermaid(doc)
    expect(out).toContain('flowchart TD')
    expect(out).toContain('T1 --> T2')
    expect(out).toContain('T1["T1 · Task 1 · low"]')
    expect(out).toContain('T2["T2 · Task 2 · medium"]')
  })

  it('標題含 " 時跳脫不破圖（輸出仍是單一有效節點宣告）', () => {
    const doc = parsePlanDoc(planYaml({ title2: 'Fix "quoted" bug' }))!
    const out = planToMermaid(doc)
    expect(out).toContain('#quot;')
    expect(out).not.toMatch(/T2\["[^"]*"[^"]*"\]/) // 沒有裸露、提早結束標籤的雙引號
    expect(out).toContain('T2["T2 · Fix #quot;quoted#quot; bug · medium"]')
  })

  it('沒有依賴的任務不產生入邊', () => {
    const doc = parsePlanDoc(planYaml({ dependsOn2: '[]' }))!
    const out = planToMermaid(doc)
    expect(out).not.toContain('-->')
  })
})
