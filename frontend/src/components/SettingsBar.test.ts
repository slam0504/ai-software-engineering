import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// wailsjs 綁定全部 mock：tab 切換不得觸發任何 backend 呼叫
const mocks = vi.hoisted(() => ({
  TerminateSession: vi.fn(), EndSession: vi.fn(), AuthStatus: vi.fn(),
  StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
  RestartCodexServerRecorded: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

import SettingsBar from './SettingsBar.vue'
import { useSession } from '../stores/session'

describe('SettingsBar provider tabs', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockClear()
  })

  it('tab click switches active view without backend calls', async () => {
    const wrapper = mount(SettingsBar)
    const s = useSession()
    const tabs = wrapper.findAll('.tab')
    expect(tabs.length).toBe(2)
    await tabs[1].trigger('click')
    expect(s.activeProvider).toBe('codex')
    await tabs[0].trigger('click')
    expect(s.activeProvider).toBe('claude')
    for (const [name, fn] of Object.entries(mocks)) {
      expect(fn, name).not.toHaveBeenCalled()
    }
  })

  it('shows unread badge and approval flag per tab', async () => {
    const wrapper = mount(SettingsBar)
    const s = useSession()
    s.apply({ event_id: 'e1', ts: 't', provider: 'codex', kind: 'result' })
    s.apply({ event_id: 'e2', ts: 't', provider: 'codex', kind: 'state_change', state: 'awaiting_approval' })
    await wrapper.vm.$nextTick()
    const codexTab = wrapper.findAll('.tab')[1]
    expect(codexTab.find('.badge').text()).toBe('1')
    expect(codexTab.find('.await').exists()).toBe(true)
    await codexTab.trigger('click') // 切入歸零
    await wrapper.vm.$nextTick()
    expect(codexTab.find('.badge').exists()).toBe(false)
  })
})
