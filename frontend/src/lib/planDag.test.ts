import { describe, it, expect } from 'vitest'
import { parsePlanDoc, planToMermaid, type PlanDoc } from './planDag'

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

  it('depends_on 為 block list 形式（比 key 多縮 2 space）也能解析', () => {
    const yaml = planYaml({ dependsOn2: '' }).replace('    depends_on: \n', '    depends_on:\n      - T1\n')
    const doc = parsePlanDoc(yaml)
    expect(doc?.tasks[1].dependsOn).toEqual(['T1'])
  })

  // review finding 1：depends_on 的 block list 項目若跟 depends_on: 同縮排
  // （go-yaml v3 預設 marshal 風格、後端實際會寫出），舊版解析器會把它靜默
  // 當成無法辨識的欄位跳過，dependsOn 停留在初始值 []——產出「看起來成功、
  // 實際少了依賴邊」的錯圖，比直接回 null 更糟。這裡驗證同縮排也要正確解析
  // 出依賴，不能是空陣列。
  it('depends_on 為 block list 形式（與 key 同縮排）也能正確解析，不是空陣列', () => {
    const yaml = planYaml({ dependsOn2: '' }).replace('    depends_on: \n', '    depends_on:\n    - T1\n')
    const doc = parsePlanDoc(yaml)
    expect(doc).not.toBeNull()
    expect(doc?.tasks[1].dependsOn).toEqual(['T1'])
  })

  // 同一個 finding 的反面：depends_on 後面接了看起來像 list 項目、但縮排既非
  // 同縮排（4-space）也非多縮 2 space（6-space）的可疑行——寧可 fail-null，
  // 也不能把它當成別的欄位跳過而讓 dependsOn 悄悄停在空陣列。
  it('depends_on block list 縮排既非同縮排也非多縮 2 space → 回傳 null（不是空依賴的成功解析）', () => {
    const yaml = planYaml({ dependsOn2: '' }).replace('    depends_on: \n', '    depends_on:\n  - T1\n')
    expect(parsePlanDoc(yaml)).toBeNull()
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

  // review finding 2：後端對 task id 只驗非空＋唯一，沒有字元限制。"a b" 與
  // "a_b" 是兩個不同、合法的 task id，但 sanitizeNodeId 都會變成 "a_b"——若
  // 直接拿來當節點 id，會被 mermaid 疊成同一個節點，邊接錯、DagPane 點選也
  // 會對回錯的 taskId。驗證兩者仍各自產生獨立節點、依賴邊接到正確的節點。
  it('sanitize 後碰撞的 task id 仍各自產生獨立節點與正確的邊', () => {
    const doc: PlanDoc = {
      planId: 'P1',
      tasks: [
        { id: 'a b', title: 'First', dependsOn: [], minimumRiskTier: 'low', plannerRiskTier: 'low' },
        { id: 'a_b', title: 'Second', dependsOn: ['a b'], minimumRiskTier: 'low', plannerRiskTier: 'low' },
      ],
    }
    const out = planToMermaid(doc)
    const nodeLines = out.split('\n').filter(l => l.includes('["'))
    expect(nodeLines).toHaveLength(2) // 沒有被疊成同一個節點
    expect(out).toContain('a_b["a b · First · low"]')
    expect(out).toContain('a_b_2["a_b · Second · low"]')
    expect(out).toContain('a_b --> a_b_2') // 依賴邊接到正確（各自獨立）的節點 id
  })
})
