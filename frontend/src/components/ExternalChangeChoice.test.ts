import { describe, it, expect } from 'vitest'
import ExternalChangeChoice from './ExternalChangeChoice.vue'
import { mountWithI18n } from '../test/i18n'

describe('ExternalChangeChoice', () => {
  it('renders three buttons with i18n text', () => {
    const w = mountWithI18n(ExternalChangeChoice)
    expect(w.find('[data-test=external-choice-reload]').text()).toBe('重新載入')
    expect(w.find('[data-test=external-choice-compare]').text()).toBe('比較')
    expect(w.find('[data-test=external-choice-keep]').text()).toBe('保留本地')
  })

  it('clicking reload emits only reload', async () => {
    const w = mountWithI18n(ExternalChangeChoice)
    await w.find('[data-test=external-choice-reload]').trigger('click')
    expect(w.emitted('reload')?.length).toBe(1)
    expect(w.emitted('compare')).toBeUndefined()
    expect(w.emitted('keep')).toBeUndefined()
  })

  it('clicking compare emits only compare', async () => {
    const w = mountWithI18n(ExternalChangeChoice)
    await w.find('[data-test=external-choice-compare]').trigger('click')
    expect(w.emitted('compare')?.length).toBe(1)
    expect(w.emitted('reload')).toBeUndefined()
    expect(w.emitted('keep')).toBeUndefined()
  })

  it('clicking keep emits only keep', async () => {
    const w = mountWithI18n(ExternalChangeChoice)
    await w.find('[data-test=external-choice-keep]').trigger('click')
    expect(w.emitted('keep')?.length).toBe(1)
    expect(w.emitted('reload')).toBeUndefined()
    expect(w.emitted('compare')).toBeUndefined()
  })

  it('disabled=true disables all three buttons and clicks emit nothing', async () => {
    const w = mountWithI18n(ExternalChangeChoice, { props: { disabled: true } })
    const reload = w.find('[data-test=external-choice-reload]')
    const compare = w.find('[data-test=external-choice-compare]')
    const keep = w.find('[data-test=external-choice-keep]')
    expect(reload.attributes('disabled')).toBeDefined()
    expect(compare.attributes('disabled')).toBeDefined()
    expect(keep.attributes('disabled')).toBeDefined()
    await reload.trigger('click')
    await compare.trigger('click')
    await keep.trigger('click')
    expect(w.emitted('reload')).toBeUndefined()
    expect(w.emitted('compare')).toBeUndefined()
    expect(w.emitted('keep')).toBeUndefined()
  })
})
