import { mount } from '@vue/test-utils'
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

const req = (id: string, provider: string) => ({ id, provider, toolName: 'Bash', inputJson: '{}' })

describe('ApprovalDialog FIFO queue (M1.5 D7)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    h.handlers = {}
    h.ResolveApproval.mockClear()
  })

  it('does not overwrite the displayed request; queues the second', async () => {
    const w = mount(ApprovalDialog)
    const s = useSession()
    h.handlers['approval:request'](req('a1', 'codex'))
    h.handlers['approval:request'](req('a2', 'claude'))
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('codex') // 顯示中不被覆蓋
    expect(w.find('.pending').text()).toContain('1') // 第二筆入列
    expect(s.activeProvider).toBe('codex') // 彈出時自動切 tab（僅第一筆）
  })

  it('promotes the next request after resolve and switches tab then', async () => {
    const w = mount(ApprovalDialog)
    const s = useSession()
    h.handlers['approval:request'](req('a1', 'codex'))
    h.handlers['approval:request'](req('a2', 'claude'))
    await w.vm.$nextTick()
    await w.find('.allow').trigger('click')
    expect(h.ResolveApproval).toHaveBeenCalledWith('a1', true, '')
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('claude') // promotion
    expect(s.activeProvider).toBe('claude') // promotion 才切 tab
  })

  it('removes a queued item by id on timeout without touching the displayed one', async () => {
    const w = mount(ApprovalDialog)
    h.handlers['approval:request'](req('a1', 'codex'))
    h.handlers['approval:request'](req('a2', 'claude'))
    await w.vm.$nextTick()
    h.handlers['approval:dismiss']({ id: 'a2' }) // 佇列項 timeout
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('codex') // 顯示中不受影響
    expect(w.find('.pending').exists()).toBe(false)
  })

  it('Esc denies only the displayed request with reason esc', async () => {
    const w = mount(ApprovalDialog)
    h.handlers['approval:request'](req('a1', 'codex'))
    h.handlers['approval:request'](req('a2', 'claude'))
    await w.vm.$nextTick()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await w.vm.$nextTick()
    expect(h.ResolveApproval).toHaveBeenCalledWith('a1', false, 'esc')
    await w.vm.$nextTick()
    expect(w.find('h3').text()).toContain('claude') // 佇列中的 a2 promotion、不受 Esc 影響
  })
})
