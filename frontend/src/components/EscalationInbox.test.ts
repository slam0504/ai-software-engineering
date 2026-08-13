import { describe, it, expect, vi } from 'vitest'
import EscalationInbox from './EscalationInbox.vue'
import { mountWithI18n } from '../test/i18n'

// entry fixture：鏡射 wailsjs escalation.Entry（Item + State，見 models.ts）。
const entry = (over: Partial<{
  escalation_id: string; source: string; source_ref: string; block_scope: string
  hard: boolean; summary: string; occurrence: number; state: string
}> = {}) => ({
  Item: {
    _type: 'escalation_item', escalation_id: over.escalation_id ?? 'E1', condition_key: '',
    occurrence: over.occurrence ?? 1, source: over.source ?? 'manual', source_ref: over.source_ref ?? 'P1/T1',
    block_scope: over.block_scope ?? 'workspace', hard: over.hard ?? false, summary: over.summary ?? 'summary text',
    created_at: '2026-01-01T00:00:00Z',
  },
  State: over.state ?? 'open',
})

const baseProps = () => ({
  entries: [] as ReturnType<typeof entry>[],
  unavailable: '',
  ack: vi.fn(async () => {}),
  resolve: vi.fn(async () => {}),
  create: vi.fn(async () => 'esc-new'),
  reload: vi.fn(async () => {}),
})

describe('EscalationInbox', () => {
  it('hard 項不渲染 resolve 控件，改顯示「僅系統可解除」提示', () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E-hard', hard: true })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=resolve-form-E-hard]').exists()).toBe(false)
    expect(w.find('[data-test=hard-notice-E-hard]').exists()).toBe(true)
  })

  it('resolve 未填 reason（或未選 resolution）時送出 disabled，兩者都填齊後 enabled 並呼叫 resolve＋reload', async () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=resolve-submit-E1]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=resolve-resolution-E1]').setValue('fixed')
    expect(w.find('[data-test=resolve-submit-E1]').attributes('disabled')).toBeDefined() // reason 仍空

    await w.find('[data-test=resolve-reason-E1]').setValue('root caused and patched')
    expect(w.find('[data-test=resolve-submit-E1]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=resolve-submit-E1]').trigger('click')
    expect(props.resolve).toHaveBeenCalledWith('E1', 'fixed', 'root caused and patched')
    expect(props.reload).toHaveBeenCalled()
  })

  it('unavailable 時顯示錯誤原文＋重試按鈕，不渲染任何分區（不裝空）', async () => {
    const props = { ...baseProps(), unavailable: 'escalation: journal degraded', entries: [entry()] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=escalation-unavailable]').text()).toContain('escalation: journal degraded')
    expect(w.find('[data-test=section-open]').exists()).toBe(false)
    expect(w.find('[data-test=create-form]').exists()).toBe(false)

    await w.find('[data-test=escalation-retry]').trigger('click')
    expect(props.reload).toHaveBeenCalled()
  })

  it('手動建立缺 sourceRef 或 summary 時送出 disabled，補齊後 enabled 並呼叫 create＋reload', async () => {
    const props = baseProps()
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=create-summary]').setValue('manual escalation summary')
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeDefined() // sourceRef 仍空

    await w.find('[data-test=create-source-ref]').setValue('P1/T1')
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=create-submit]').trigger('click')
    expect(props.create).toHaveBeenCalledWith('P1/T1', '', 'manual escalation summary')
    expect(props.reload).toHaveBeenCalled()
  })

  it('acknowledged 分區顯示該狀態項目，並可 ack open 項目', async () => {
    const props = {
      ...baseProps(),
      entries: [entry({ escalation_id: 'E-open', state: 'open' }), entry({ escalation_id: 'E-ack', state: 'acknowledged' })],
    }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=section-acknowledged]').find('[data-test=entry-E-ack]').exists()).toBe(true)
    expect(w.find('[data-test=section-open]').find('[data-test=entry-E-open]').exists()).toBe(true)
    // ack 按鈕只在 open 項目出現，acknowledged 項目不再重複 ack
    expect(w.find('[data-test=ack-E-open]').exists()).toBe(true)
    expect(w.find('[data-test=ack-E-ack]').exists()).toBe(false)

    await w.find('[data-test=ack-E-open]').trigger('click')
    expect(props.ack).toHaveBeenCalledWith('E-open')
    expect(props.reload).toHaveBeenCalled()
  })

  it('resolved 摺疊區預設收合，展開後顯示已解除項目', async () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E-res', state: 'resolved' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=entry-E-res]').exists()).toBe(false)
    await w.find('[data-test=toggle-resolved]').trigger('click')
    expect(w.find('[data-test=entry-E-res]').exists()).toBe(true)
  })
})
