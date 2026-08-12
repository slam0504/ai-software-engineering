import { flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TcaWorkspace from './TcaWorkspace.vue'
import { mountWithI18n } from '../test/i18n'
import type { GateEntry } from '../types'

const activeGate2: GateEntry = { approval_id: 'G2-1', state: 'active', gate: 'gate2', subject: 'plan:P1' }
const oneTask = { tasks: [{ task_id: 'T1', title: 'do the thing', minimum_risk_tier: 'medium', planner_risk_tier: 'medium' }] }

function baseProps(over: Partial<Record<string, unknown>> = {}) {
  return {
    entries: [activeGate2],
    loadDecisionContext: vi.fn().mockResolvedValue(oneTask),
    listCandidates: vi.fn().mockResolvedValue([{ oid: 'deadbeef00', subject: 'add oracle test' }]),
    validateTestCommit: vi.fn().mockResolvedValue(undefined),
    registerMutation: vi.fn().mockResolvedValue('mut-1'),
    runEvidence: vi.fn().mockResolvedValue('ev-1'),
    getEvidence: vi.fn().mockResolvedValue({ result: 'passed' }),
    submitTestContract: vi.fn().mockResolvedValue('tca-approval-1'),
    ...over,
  }
}

describe('TcaWorkspace', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('no active gate2 → empty state, no GateDecisionContext call', async () => {
    const props = baseProps({ entries: [] })
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()
    expect(w.find('[data-test=tca-empty]').exists()).toBe(true)
    expect(props.loadDecisionContext).not.toHaveBeenCalled()
  })

  it('active gate2 → loads tasks via GateDecisionContext(approval_id) and lists commit candidates via planID', async () => {
    const props = baseProps()
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()
    expect(props.loadDecisionContext).toHaveBeenCalledWith('G2-1')
    expect(props.listCandidates).toHaveBeenCalledWith('P1')
    expect(w.find('[data-test=tca-row-T1]').exists()).toBe(true)
  })

  it('送核 disabled until both expected_red and negative_control evidence runs pass', async () => {
    const props = baseProps()
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()

    expect(w.find('[data-test=submit-tca-T1]').attributes('disabled')).toBeDefined()

    // 跑 expected-red：通過
    await w.find('[data-test=test-commit-input-T1]').setValue('deadbeef00')
    await w.find('[data-test=run-expected_red-T1]').trigger('click')
    await flushPromises()
    expect(props.runEvidence).toHaveBeenCalledWith('P1', 'T1', 'deadbeef00', 'expected_red', '')
    expect(w.find('[data-test=run-result-expected_red-T1]').text()).toContain('通過')
    expect(w.find('[data-test=submit-tca-T1]').attributes('disabled')).toBeDefined() // 只有一筆 passed，仍 disabled

    // negative_control 按鈕在未登記 mutation 前 disabled
    expect(w.find('[data-test=run-negative_control-T1]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=mutation-patch-T1]').setValue('diff --git a/x b/x')
    await w.find('[data-test=register-mutation-T1]').trigger('click')
    await flushPromises()
    expect(props.registerMutation).toHaveBeenCalledWith('P1/T1', 'diff --git a/x b/x')
    expect(w.find('[data-test=mutation-id-T1]').text()).toContain('mut-1')
    expect(w.find('[data-test=run-negative_control-T1]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=run-negative_control-T1]').trigger('click')
    await flushPromises()
    expect(props.runEvidence).toHaveBeenCalledWith('P1', 'T1', 'deadbeef00', 'negative_control', 'mut-1')
    expect(w.find('[data-test=submit-tca-T1]').attributes('disabled')).toBeUndefined() // 雙 passed，可送核

    await w.find('[data-test=submit-tca-T1]').trigger('click')
    await flushPromises()
    expect(props.submitTestContract).toHaveBeenCalledWith('P1', 'T1', 'deadbeef00', 'ev-1', 'ev-1', 'mut-1')
    expect(w.find('[data-test=submit-result-T1]').text()).toContain('tca-approval-1')
  })

  it('執行中的 run 按鈕 disabled 並顯示進度指示', async () => {
    let resolveRun: (id: string) => void = () => {}
    const runEvidence = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveRun = r }))
    const props = baseProps({ runEvidence })
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()

    void w.find('[data-test=run-expected_red-T1]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=run-expected_red-T1]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=run-busy-expected_red-T1]').exists()).toBe(true)

    resolveRun('ev-1')
    await flushPromises()
    expect(w.find('[data-test=run-busy-expected_red-T1]').exists()).toBe(false)
    expect(w.find('[data-test=run-expected_red-T1]').attributes('disabled')).toBeUndefined()
  })

  it('result=error 顯示錯誤標示原文＋重跑按鈕，點擊重跑再次呼叫 RunEvidence', async () => {
    const runEvidence = vi.fn().mockRejectedValue(new Error('evidence: no active Gate 2 approval for plan "P1"'))
    const props = baseProps({ runEvidence })
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()

    await w.find('[data-test=run-expected_red-T1]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=run-result-expected_red-T1]').text()).toContain('錯誤')
    expect(w.find('[data-test=run-error-expected_red-T1]').text()).toContain('evidence: no active Gate 2 approval for plan "P1"')

    const retry = w.find('[data-test=retry-expected_red-T1]')
    expect(retry.exists()).toBe(true)
    await retry.trigger('click')
    await flushPromises()
    expect(runEvidence).toHaveBeenCalledTimes(2)
  })

  it('預檢失敗顯示錯誤原文', async () => {
    const validateTestCommit = vi.fn().mockRejectedValue(new Error('plan: lineage: deadbeef is not an ancestor of cafebabe'))
    const props = baseProps({ validateTestCommit })
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()

    await w.find('[data-test=test-commit-input-T1]').setValue('cafebabe')
    await w.find('[data-test=precheck-T1]').trigger('click')
    await flushPromises()
    expect(validateTestCommit).toHaveBeenCalledWith('P1', 'T1', 'cafebabe')
    expect(w.find('[data-test=precheck-error-T1]').text()).toContain('plan: lineage: deadbeef is not an ancestor of cafebabe')
  })

  it('GateDecisionContext 失敗時顯示錯誤原文', async () => {
    const loadDecisionContext = vi.fn().mockRejectedValue(new Error('gate: approval id "G2-1" not found'))
    const props = baseProps({ loadDecisionContext })
    const w = mountWithI18n(TcaWorkspace, { props })
    await flushPromises()
    expect(w.find('[data-test=tca-load-error]').text()).toContain('gate: approval id "G2-1" not found')
  })
})
