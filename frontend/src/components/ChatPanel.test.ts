import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import ChatPanel from './ChatPanel.vue'
import { useSession } from '../stores/session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta', ...over,
})

// V3 normative：thinking 摺疊——thinking 內容渲染於預設收合的 <details>
describe('ChatPanel thinking fold', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    // jsdom 未實作 scrollTo（follow-tail watch 會呼叫）
    Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {})
  })

  it('renders accumulated thinking inside a collapsed details block', async () => {
    const wrapper = mount(ChatPanel)
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'ans-', thinking: 'step one; ' }))
    s.apply(env({ kind: 'delta', text: 'wer', thinking: 'step two' }))
    await wrapper.vm.$nextTick()

    const details = wrapper.find('details.thinking')
    expect(details.exists()).toBe(true)
    expect(details.attributes('open')).toBeUndefined() // 預設收合
    expect(details.text()).toContain('step one; step two') // thinking 累積
    expect(wrapper.find('.bubble .text').text()).toContain('ans-wer') // 主文不含 thinking
  })

  it('omits details when message has no thinking', async () => {
    const wrapper = mount(ChatPanel)
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'plain' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('details.thinking').exists()).toBe(false)
  })
})
