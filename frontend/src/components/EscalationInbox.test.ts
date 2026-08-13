import { describe, it, expect, vi } from 'vitest'
import EscalationInbox from './EscalationInbox.vue'
import { mountWithI18n } from '../test/i18n'

// entry fixture：鏡射 wailsjs escalation.Entry（Item + State，見 models.ts）。
const entry = (over: Partial<{
  escalation_id: string; source: string; source_ref: string; block_scope: string
  hard: boolean; summary: string; occurrence: number; state: string; condition_key: string
}> = {}) => ({
  Item: {
    _type: 'escalation_item', escalation_id: over.escalation_id ?? 'E1', condition_key: over.condition_key ?? '',
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

  // review fix（spec §3.8 回填）：PlanWorkspace／GateConsole／EvidenceDetail 的
  // 「建立升級項目」按鈕透過 App.vue 轉發 prefill prop，表單要正確帶入。
  it('prefill prop 帶入建立表單：sourceRef 直填，blockScope 非空時落在自由輸入欄位', async () => {
    const props = baseProps()
    const w = mountWithI18n(EscalationInbox, { props })
    await w.setProps({ prefill: { sourceRef: 'approval:G2-1', blockScope: 'gate2:P1' } })
    await w.vm.$nextTick()

    expect((w.find('[data-test=create-source-ref]').element as HTMLInputElement).value).toBe('approval:G2-1')
    expect((w.find('[data-test=create-scope-select]').element as HTMLSelectElement).value).toBe('custom')
    expect((w.find('[data-test=create-scope-id]').element as HTMLInputElement).value).toBe('gate2:P1')
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeDefined() // summary 仍空

    await w.find('[data-test=create-summary]').setValue('needs review')
    await w.find('[data-test=create-submit]').trigger('click')
    expect(props.create).toHaveBeenCalledWith('approval:G2-1', 'gate2:P1', 'needs review')
  })

  it('blockScope 選 gate2/tca/custom 但未填 id 時送出 disabled 並顯示提示（不得靜默忽略）', async () => {
    const props = baseProps()
    const w = mountWithI18n(EscalationInbox, { props })
    await w.find('[data-test=create-source-ref]').setValue('P1/T1')
    await w.find('[data-test=create-summary]').setValue('manual escalation summary')
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeUndefined() // 未選範圍：可送出

    await w.find('[data-test=create-scope-select]').setValue('gate2')
    expect(w.find('[data-test=create-scope-warning]').exists()).toBe(true)
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=create-scope-id]').setValue('P1')
    expect(w.find('[data-test=create-scope-warning]').exists()).toBe(false)
    expect(w.find('[data-test=create-submit]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=create-submit]').trigger('click')
    expect(props.create).toHaveBeenCalledWith('P1/T1', 'gate2:P1', 'manual escalation summary')
  })

  // M3a.1 Task 11（spec §3.5.4，凍結規則）：手動項／condition_key 不以 "stale:"
  // 開頭 → 不顯示導航（正常）；source=system 且 "stale:" 開頭但解析失敗 → 顯示
  // 資料完整性錯誤、禁止導航（不得靜默隱藏）；可解析 → 顯示導航按鈕，emit
  // go-resubmit 帶正確 (gate, subject)，且不呼叫任何寫入 binding（ack／
  // resolve／create 全程零呼叫）。
  it('手動項（source=manual）不顯示導航，即使 condition_key 剛好長得像 stale key（正常）', () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1', source: 'manual', condition_key: 'stale:gate1:workspace' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=go-resubmit-E1]').exists()).toBe(false)
    expect(w.find('[data-test=stale-nav-error-E1]').exists()).toBe(false)
  })

  it('系統項但 condition_key 不以 "stale:" 開頭 → 不顯示導航（正常，非 stale 系統項）', () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1', source: 'system', condition_key: 'journal-degraded:gate' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=go-resubmit-E1]').exists()).toBe(false)
    expect(w.find('[data-test=stale-nav-error-E1]').exists()).toBe(false)
  })

  it('系統 stale 項但 parseStaleTarget 解析失敗（未知 gate）→ 顯示資料完整性錯誤、禁止導航', () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1', source: 'system', hard: true, condition_key: 'stale:gate3:workspace' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=stale-nav-error-E1]').exists()).toBe(true)
    expect(w.find('[data-test=go-resubmit-E1]').exists()).toBe(false)
  })

  it('系統 stale 項但 parseStaleTarget 解析失敗（空 subject）→ 顯示資料完整性錯誤、禁止導航', () => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1', source: 'system', hard: true, condition_key: 'stale:gate1:' })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=stale-nav-error-E1]').exists()).toBe(true)
    expect(w.find('[data-test=go-resubmit-E1]').exists()).toBe(false)
  })

  it.each([
    ['gate1', 'stale:gate1:workspace', { gate: 'gate1', subject: 'workspace' }],
    ['gate2（subject 含冒號）', 'stale:gate2:plan:P1', { gate: 'gate2', subject: 'plan:P1' }],
    ['test_contract_approval', 'stale:test_contract_approval:task:P1/T1', { gate: 'test_contract_approval', subject: 'task:P1/T1' }],
  ])('系統 stale 項（%s）可解析 → 顯示導航按鈕，emit go-resubmit 帶正確 gate/subject，不呼叫任何寫入 binding', async (_label, key, expected) => {
    const props = { ...baseProps(), entries: [entry({ escalation_id: 'E1', source: 'system', hard: true, condition_key: key })] }
    const w = mountWithI18n(EscalationInbox, { props })
    expect(w.find('[data-test=stale-nav-error-E1]').exists()).toBe(false)
    await w.find('[data-test=go-resubmit-E1]').trigger('click')
    expect(w.emitted('go-resubmit')).toEqual([[expected]])
    // 導航純 view 操作——ack／resolve／create 全程零呼叫（mock 全量斷言）
    expect(props.ack).not.toHaveBeenCalled()
    expect(props.resolve).not.toHaveBeenCalled()
    expect(props.create).not.toHaveBeenCalled()
  })
})
