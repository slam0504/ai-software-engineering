import { flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
vi.mock('mermaid', () => ({
  default: {
    initialize: vi.fn(),
    render: vi.fn().mockResolvedValue({ svg: '<svg><g class="node" id="flowchart-T1-0"></g></svg>' }),
  },
}))
import DagPane from './DagPane.vue'
import { usePlan } from '../stores/plan'
import { mountWithI18n } from '../test/i18n'

function validYaml(id: string): string {
  return '' +
    'plan_id: P1\n' +
    'tasks:\n' +
    `  - id: ${id}\n` +
    '    title: t\n' +
    '    depends_on: []\n' +
    '    minimum_risk_tier: low\n' +
    '    planner_risk_tier: low\n'
}

describe('DagPane', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('plan store 尚無內容時顯示 hint，不呼叫 mermaid', async () => {
    const mermaid = (await import('mermaid')).default as unknown as { render: ReturnType<typeof vi.fn> }
    mermaid.render.mockClear()
    const w = mountWithI18n(DagPane)
    await flushPromises()
    expect(w.find('.hint').exists()).toBe(true)
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it('plan YAML 解析失敗時顯示錯誤訊息，不炸（無 render 例外拋出）', async () => {
    const plan = usePlan()
    const w = mountWithI18n(DagPane)
    plan.setCurrentFile('plan/bad.yaml', 'not_a_plan: true', 'sha256:x')
    await flushPromises()
    expect(w.find('.err').exists()).toBe(true)
    expect(w.find('.rendered').exists()).toBe(false)
  })

  it('plan store 內容更新自動重渲染', async () => {
    const mermaid = (await import('mermaid')).default as unknown as { render: ReturnType<typeof vi.fn> }
    mermaid.render.mockClear()
    const plan = usePlan()
    const w = mountWithI18n(DagPane)

    plan.setCurrentFile('plan/a.yaml', validYaml('T1'), 'sha256:x')
    await flushPromises()
    expect(mermaid.render).toHaveBeenCalledTimes(1)
    expect(w.find('.rendered').exists()).toBe(true)

    plan.setCurrentFile('plan/a.yaml', validYaml('T2'), 'sha256:y')
    await flushPromises()
    expect(mermaid.render).toHaveBeenCalledTimes(2)
  })

  it('節點點選 emit select-task，換回原始（未 sanitize 前的）task id', async () => {
    const plan = usePlan()
    const w = mountWithI18n(DagPane)
    plan.setCurrentFile('plan/a.yaml', validYaml('T1'), 'sha256:x')
    await flushPromises()

    await w.find('#flowchart-T1-0').trigger('click')
    expect(w.emitted('select-task')).toEqual([['T1']])
  })

  // review finding 2 回歸：兩個 task id（"a b"／"a_b"）sanitize 後都變成 "a_b"，
  // buildNodeIdMap 用遞增後綴分開（a_b／a_b_2）。驗證兩個節點各自點選都對回
  // 正確的原始 taskId，不會因為 Map key 撞在一起而後蓋前、選到錯的任務。
  it('sanitize 後碰撞的兩個 task id 仍各自點選出正確的 taskId', async () => {
    const mermaid = (await import('mermaid')).default as unknown as { render: ReturnType<typeof vi.fn> }
    mermaid.render.mockResolvedValueOnce({
      svg: '<svg>' +
        '<g class="node" id="flowchart-a_b-0"></g>' +
        '<g class="node" id="flowchart-a_b_2-1"></g>' +
        '</svg>',
    })
    const plan = usePlan()
    const w = mountWithI18n(DagPane)
    const yaml = '' +
      'plan_id: P1\n' +
      'tasks:\n' +
      '  - id: a b\n' +
      '    title: First\n' +
      '    depends_on: []\n' +
      '    minimum_risk_tier: low\n' +
      '    planner_risk_tier: low\n' +
      '  - id: a_b\n' +
      '    title: Second\n' +
      '    depends_on: []\n' +
      '    minimum_risk_tier: low\n' +
      '    planner_risk_tier: low\n'
    plan.setCurrentFile('plan/collide.yaml', yaml, 'sha256:x')
    await flushPromises()

    expect(w.findAll('.node')).toHaveLength(2) // 沒有被 v-html 疊掉

    await w.find('#flowchart-a_b-0').trigger('click')
    await w.find('#flowchart-a_b_2-1').trigger('click')
    expect(w.emitted('select-task')).toEqual([['a b'], ['a_b']])
  })
})
