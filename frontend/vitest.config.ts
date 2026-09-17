import { defineConfig, configDefaults } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    // e2e/ 是 Playwright 專用的 harness＋spec（B3a-1），有自己的 globalSetup／
    // globalTeardown 與行程生命週期管理，不該被 vitest 當成單元測試撿起來跑。
    exclude: [...configDefaults.exclude, 'e2e/**'],
  },
})
