import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import zhTW from '../i18n/locales/zh-TW'
import en from '../i18n/locales/en'

export function makeI18n(locale: 'zh-TW' | 'en' = 'zh-TW') {
  return createI18n({ legacy: false, locale, fallbackLocale: 'en', messages: { 'zh-TW': zhTW, en } })
}
export function mountWithI18n(component: any, options: any = {}, locale: 'zh-TW' | 'en' = 'zh-TW') {
  const i18n = makeI18n(locale)
  return mount(component, {
    ...options,
    global: { ...(options.global ?? {}), plugins: [i18n, ...((options.global?.plugins) ?? [])] },
  })
}
