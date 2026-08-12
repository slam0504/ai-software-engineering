// enRender.test.ts — 原本硬編中文的畫面在 locale=en 下轉英文
import { describe, it, expect } from 'vitest'
import { mountWithI18n } from '../test/i18n'
import GateConsole from '../components/GateConsole.vue'

describe('en locale renders English', () => {
  it('GateConsole degraded notice + empty are English under en', () => {
    const w = mountWithI18n(GateConsole, { props: { entries: [], decide: () => {}, degraded: true } }, 'en')
    expect(w.text()).toContain('degraded') // 英文，而非「核可記錄異常」
    expect(w.text()).not.toContain('核可記錄異常')
  })
})
