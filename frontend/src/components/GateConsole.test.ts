import { flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import GateConsole from './GateConsole.vue'
import { mountWithI18n } from '../test/i18n'

// minimum_risk_tier 預設用 medium（非 low）：low 是 tierOrder 的最小值，若預設一路用
// low 測試永遠不會踩到「selected 被下拉過濾／低於 minimum 送出擋」這條路徑（review
// round 1 finding），故意選一個 minimum > low 的預設值消除這個測試盲點。
const gate2Task = (over: Partial<{ task_id: string; title: string; minimum_risk_tier: string; planner_risk_tier: string }> = {}) => ({
  task_id: 'T1', title: 'do the thing', minimum_risk_tier: 'medium', planner_risk_tier: 'high', ...over,
})

describe('GateConsole', () => {
  it('reject requires reason', async () => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'pending' }], decide } })
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).not.toHaveBeenCalled() // 無理由不送
    await w.find('[data-test=reason]').setValue('bad')
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'rejected', 'bad', []) // rejected 只需 reason，riskSelections 恆空
  })
  it('shows stale badge', () => {
    const w = mountWithI18n(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'stale' }], decide: vi.fn() } })
    expect(w.find('[data-test=badge-A]').text()).toContain('已失效') // zh-TW 預設 locale：gate.state.stale
  })

  it('gate1 卡片不顯示 risk 列（回歸）', async () => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate1' }], decide },
    })
    await flushPromises()
    expect(w.find('[data-test=risk-section]').exists()).toBe(false)
    // gate1 approve 不受 risk 邏輯影響，只受 degraded 控制（既有行為）
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()
    await w.find('[data-test=reason]').setValue('ok')
    await w.find('[data-test=approve]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'approved', 'ok', [])
  })

  it('gate2 卡片：selected<planner 未填理由時核可 disabled，填理由後 enabled 且送出 payload 含 override_reason', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({ tasks: [gate2Task()] })
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2', subject: 'plan:P1' }],
        decide, loadDecisionContext,
      },
    })
    await flushPromises()
    expect(loadDecisionContext).toHaveBeenCalledWith('A')
    expect(w.find('[data-test=risk-row-T1]').exists()).toBe(true)
    expect(w.find('[data-test=minimum]').text()).toContain('medium')
    expect(w.find('[data-test=planner]').text()).toContain('high')

    // 預設 selected=planner，不需要 override reason，核可可用
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()

    // 調到 medium（= minimum，< planner=high）：override reason 欄位出現，未填時核可 disabled
    await w.find('[data-test=selected-T1]').setValue('medium')
    expect(w.find('[data-test=override-reason-T1]').exists()).toBe(true)
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=override-reason-T1]').setValue('policy exception: read-only preview')
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=approve]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'approved', '', [
      { TaskID: 'T1', SelectedRiskTier: 'medium', OverrideReason: 'policy exception: read-only preview' },
    ])
  })

  it('gate2 卡片：下拉依 minimum 過濾，不會列出低於 minimum 的選項（selected<minimum binding constraint）', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({
      tasks: [gate2Task({ minimum_risk_tier: 'medium', planner_risk_tier: 'high' })],
    })
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    const options = w.find('[data-test=selected-T1]').findAll('option').map(o => o.attributes('value'))
    expect(options).toEqual(['medium', 'high']) // low（< minimum）不在選項內
  })

  it('gate2 卡片：rejected 只需 reason，不受 risk 選擇影響', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({ tasks: [gate2Task()] })
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    await w.find('[data-test=reject]').trigger('click')
    expect(decide).not.toHaveBeenCalled() // 無理由不送

    await w.find('[data-test=reason]').setValue('not ready')
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'rejected', 'not ready', [])
  })

  it('GateDecisionContext 失敗時顯示錯誤原文，且核可 disabled', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockRejectedValue(new Error('gate: approval id "A" not found'))
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    expect(w.find('[data-test=risk-error]').text()).toContain('gate: approval id "A" not found')
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeDefined()
  })
})
