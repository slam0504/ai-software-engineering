import { createI18n } from 'vue-i18n'
import zhTW from './locales/zh-TW'
import en from './locales/en'

export const i18n = createI18n({
  legacy: false,
  locale: 'zh-TW',
  fallbackLocale: 'en',
  messages: { 'zh-TW': zhTW, en },
})

// 非元件（store／一般 TS 模組）產生固定 UI 訊息用這支
export const t = i18n.global.t
