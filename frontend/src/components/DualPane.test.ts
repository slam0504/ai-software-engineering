import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import DualPane from './DualPane.vue'
import { useSession } from '../stores/session'
import { makeI18n } from '../test/i18n'
import type { Bindings } from '../types'

// DualPane（Task 28，spec §3.7）：兩個 pane 都持續接收串流（不管是否
// focused），只有 focused pane 有 composer，切 pane 只換焦點不卸載，
// SettingsBar/EndSession 一類的 lifecycle 操作只作用於 focused pane 的 WSID。
//
// 兩個 session 一律先 active=true——這裡要驗證的是「第二輪走 SendMessage」
// 這條分支（focused pane 切換後仍是既有對話，不是重新 StartSession），跟
// session.test.ts 既有「A busy 不擋 B 送出」（兩個都是全新 session，走
// StartSession）覆蓋的是不同分支，非重複。
function mockBindings(): Bindings {
  return {
    StartSession: vi.fn(async () => {}),
    SendMessage: vi.fn(async () => {}),
    EndSession: vi.fn(async () => {}),
  }
}

describe('DualPane（雙 pane 並看＋單一 focused pane 操作語意，§3.7／§4）', () => {
  let pinia: ReturnType<typeof createPinia>
  const i18n = makeI18n()

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    // jsdom 未實作 scrollTo（follow-tail watch 會呼叫，同 ChatPanel.test.ts 慣例）
    Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {})
    const s = useSession()
    s.setBindings(mockBindings())
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: '' })
    s.sessions['w1'].active = true
    s.sessions['w2'].active = true
  })

  it('兩 pane 皆 live，背景 pane 持續更新', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: '' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
    s.apply({ event_id: 'e1', ts: 't', kind: 'delta', text: 'bg', provider: 'claude', workspace_session_id: 'w2' })
    await nextTick()
    expect(w.find('[data-test=pane-1]').text()).toContain('bg')
  })

  it('只有 focused pane 有 composer', async () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
    await nextTick()
    expect(w.find('[data-test=pane-0] [data-test=composer]').exists()).toBe(true)
    expect(w.find('[data-test=pane-1] [data-test=composer]').exists()).toBe(false)
  })

  it('點另一 pane 切焦點但不卸載', async () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
    await nextTick()
    const before = w.find('[data-test=pane-1]').element
    await w.find('[data-test=pane-1]').trigger('click')
    expect(s.focused).toBe(1)
    expect(w.find('[data-test=pane-1]').element).toBe(before)
  })

  it('A 執行中仍可切 B 送出', async () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2')
    s.sessions['w1'].busy = true
    s.setFocus(1)
    await s.submit('hello')
    expect(s.bindings?.SendMessage).toHaveBeenCalledWith('w2', 'hello')
  })

  it('SettingsBar 的 End 只作用於 focused pane', async () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(1)
    const w = mount(DualPane, { global: { plugins: [pinia, i18n] } })
    await nextTick()
    await w.find('[data-test=end-session]').trigger('click')
    expect(s.bindings?.EndSession).toHaveBeenCalledWith('w2')
  })
})
