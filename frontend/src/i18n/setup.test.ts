import { describe, it, expect } from 'vitest'
import { i18n } from './index'

describe('i18n setup', () => {
  it('defaults to zh-TW and resolves a key', () => {
    expect(i18n.global.locale.value).toBe('zh-TW')
    expect(i18n.global.t('app.tab.chat')).toBe('對話')
  })
  it('falls back to en for a zh-only-missing key path via fallbackLocale', () => {
    expect(i18n.global.fallbackLocale.value).toBe('en')
  })
})
