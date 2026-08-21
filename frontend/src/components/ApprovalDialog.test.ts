import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({
  handlers: {} as Record<string, (d: any) => void>,
  ResolveApproval: vi.fn(async () => {}),
}))
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: (name: string, cb: (d: any) => void) => { h.handlers[name] = cb },
}))
vi.mock('../../wailsjs/go/main/App', () => ({ ResolveApproval: h.ResolveApproval }))

import ApprovalDialog from './ApprovalDialog.vue'
import { useSession } from '../stores/session'
import { mountWithI18n } from '../test/i18n'

const req = (id: string, wsid: string, provider: string) =>
  ({ id, wsid, provider, toolName: 'Bash', inputJson: '{}' })

// panes：w1 釘在 pane 0（focused）、w2 釘在 pane 1；w3 未釘選。
function panes() {
  const s = useSession()
  for (const [wsid, provider] of [['w1', 'claude'], ['w2', 'codex'], ['w3', 'claude']] as const) {
    s.registerSession({ wsid, provider, taskLabel: '' })
  }
  s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
  return s
}

describe('ApprovalDialog FIFO queue (M1.5 D7)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    h.handlers = {}
    h.ResolveApproval.mockClear()
  })

  it('does not overwrite the displayed request; queues the second', async () => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    h.handlers['approval:request'](req('a1', 'w2', 'codex'))
    h.handlers['approval:request'](req('a2', 'w1', 'claude'))
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('codex') // 顯示中不被覆蓋
    expect(w.find('.pending').text()).toContain('1') // 第二筆入列
    expect(s.focused).toBe(1) // 彈出時自動切到來源 pane（僅第一筆）
  })

  it('promotes the next request after resolve and switches pane then', async () => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    h.handlers['approval:request'](req('a1', 'w2', 'codex'))
    h.handlers['approval:request'](req('a2', 'w1', 'claude'))
    await w.vm.$nextTick()
    await w.find('.allow').trigger('click')
    expect(h.ResolveApproval).toHaveBeenCalledWith('a1', true, '')
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('claude') // promotion
    expect(s.focused).toBe(0) // promotion 才切 pane（a2 的來源是 w1）
  })

  it('removes a queued item by id on timeout without touching the displayed one', async () => {
    const w = mountWithI18n(ApprovalDialog)
    panes()
    h.handlers['approval:request'](req('a1', 'w2', 'codex'))
    h.handlers['approval:request'](req('a2', 'w1', 'claude'))
    await w.vm.$nextTick()
    h.handlers['approval:dismiss']({ id: 'a2', cause: 'timeout' }) // 佇列項 timeout
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('codex') // 顯示中不受影響
    expect(w.find('.pending').exists()).toBe(false)
  })

  it('Esc denies only the displayed request with reason esc', async () => {
    const w = mountWithI18n(ApprovalDialog)
    panes()
    h.handlers['approval:request'](req('a1', 'w2', 'codex'))
    h.handlers['approval:request'](req('a2', 'w1', 'claude'))
    await w.vm.$nextTick()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await w.vm.$nextTick()
    expect(h.ResolveApproval).toHaveBeenCalledWith('a1', false, 'esc')
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('claude') // 佇列中的 a2 promotion、不受 Esc 影響
  })
})

// §3.6.4：來源未釘選 → 次要 pane transient 顯示；六種觸發都恢復原釘選。
describe('ApprovalDialog：未釘選來源的 transient presentation（§3.6.4）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    h.handlers = {}
    h.ResolveApproval.mockClear()
  })

  it('未釘選來源 transient 顯示於次要 pane，allow 後恢復原釘選', async () => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    h.handlers['approval:request'](req('a1', 'w3', 'claude'))
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w3')
    expect(s.persistentPins[1]).toBe('w2')
    expect(s.focused).toBe(0) // transient 不搶 composer
    await w.find('.allow').trigger('click')
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w2')
  })

  it('deny 同樣恢復原釘選', async () => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    h.handlers['approval:request'](req('a1', 'w3', 'claude'))
    await w.vm.$nextTick()
    await w.find('.deny').trigger('click')
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w2')
  })

  it.each([
    ['timeout', { cause: 'timeout' }],
    ['dismiss', { cause: 'resolved' }],
    ['remove', { cause: 'resolved', reason: 'session_removed' }],
    ['shutdown', { cause: 'resolved', reason: 'shutdown' }],
  ])('%s 觸發恢復原釘選', async (_name, payload) => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    h.handlers['approval:request'](req('a1', 'w3', 'claude'))
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w3')
    h.handlers['approval:dismiss']({ id: 'a1', ...payload })
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w2')
    expect(w.find('.dialog').exists()).toBe(false)
  })

  it('連續兩筆未釘選來源：promotion 後仍能恢復到原始釘選', async () => {
    const w = mountWithI18n(ApprovalDialog)
    const s = panes()
    s.registerSession({ wsid: 'w4', provider: 'codex', taskLabel: '' })
    h.handlers['approval:request'](req('a1', 'w3', 'claude'))
    h.handlers['approval:request'](req('a2', 'w4', 'codex'))
    await w.vm.$nextTick()
    await w.find('.allow').trigger('click')
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w4') // 第二筆接手 transient
    expect(s.persistentPins[1]).toBe('w2')
    h.handlers['approval:dismiss']({ id: 'a2', cause: 'resolved' })
    await w.vm.$nextTick()
    expect(s.pins[1]).toBe('w2')
  })
})
