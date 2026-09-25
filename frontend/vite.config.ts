import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    watch: {
      // Playwright/harness 把 trace 資源 HTML 寫進這裡，會被 chokidar 的
      // add/change 事件觸發 vite full-reload（見 wails-dev.log:31,36）。
      // 排除掉，其餘 e2e/**、src/**、index.html 等原始碼不受影響。
      ignored: ['**/e2e/.artifacts/**'],
    },
  },
})
