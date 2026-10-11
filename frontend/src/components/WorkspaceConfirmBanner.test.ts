import { setActivePinia, createPinia } from 'pinia'
import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WorkspaceConfirmBanner from './WorkspaceConfirmBanner.vue'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { mountWithI18n } from '../test/i18n'
import zhTW from '../i18n/locales/zh-TW'

// ---- B4 R5／§6.2：高風險警示與確認流程 ----

const PENDING = {
  required: true, confirmed: false, workspace: '/Users/me', source: 'home',
  reasons: ['source_home', 'is_home'],
}

function mountBanner(over: { confirm?: (p: string) => Promise<void>; refresh?: () => Promise<void> } = {}, locale: 'zh-TW' | 'en' = 'zh-TW') {
  const confirm = over.confirm ?? vi.fn(async () => {})
  const refresh = over.refresh ?? vi.fn(async () => {})
  const w = mountWithI18n(WorkspaceConfirmBanner, { props: { confirm, refresh } }, locale)
  return { w, confirm, refresh }
}

describe('WorkspaceConfirmBanner', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('required && !confirmed：role="alert"、--err 樣式，顯示實際目錄、來源與原因', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w } = mountBanner()
    const alert = w.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.attributes('data-test')).toBe('ws-alert')
    expect(w.find('[data-test=ws-path]').text()).toBe('/Users/me')
    expect(w.find('[data-test=ws-source]').text()).toBe('home')
    const reasons = w.findAll('[data-test=ws-reason]').map(r => r.text())
    expect(reasons).toEqual([zhTW.workspaceConfirm.reason.sourceHome, zhTW.workspaceConfirm.reason.isHome])
    expect(w.find('[data-test=ws-open-confirm]').text()).toBe('確認在此目錄進行 AI 工作')
    // 樣式必須使用 --err（高風險警示，不是普通狀態文字）
    expect(w.html()).toContain('ws-alert')
  })

  it('en locale 顯示英文，未知的原因代碼原樣顯示（不洩漏缺漏 key）', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, reasons: ['is_home', 'brand_new_reason'] }))
    const { w } = mountBanner({}, 'en')
    expect(w.text()).toContain('High-risk workspace')
    const reasons = w.findAll('[data-test=ws-reason]').map(r => r.text())
    expect(reasons[0]).toContain('home directory')
    expect(reasons[1]).toBe('brand_new_reason')
  })

  it('不需確認或已確認：不渲染警示', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, confirmed: true }))
    const { w } = mountBanner()
    expect(w.find('[role="alert"]').exists()).toBe(false)
    expect(w.find('[role="status"]').exists()).toBe(false)
    await wsc.refresh(async () => ({ ...PENDING, required: false }))
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('尚未取得 status：顯示 loading（role="status"），不是警示', () => {
    const { w } = mountBanner()
    expect(w.find('[data-test=ws-loading]').attributes('role')).toBe('status')
    expect(w.find('[role="alert"]').exists()).toBe(false)
  })

  it('查詢失敗：role="alert" 顯示原因，並可重新查詢', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => { throw new Error('app 尚未完成啟動') })
    const { w, refresh } = mountBanner()
    const failed = w.find('[data-test=ws-query-failed]')
    expect(failed.attributes('role')).toBe('alert')
    expect(failed.text()).toContain('app 尚未完成啟動')
    await w.find('[data-test=ws-retry]').trigger('click')
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('確認成功：點擊 → 對話 → 確認 → 以 status.workspace 原樣呼叫 ConfirmWorkspace，並重新查詢', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const confirm = vi.fn(async (_p: string) => {})
    const refresh = vi.fn(async () => { await wsc.refresh(async () => ({ ...PENDING, confirmed: true })) })
    const { w } = mountBanner({ confirm, refresh })
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(false) // 點擊前沒有對話
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    expect(confirm).not.toHaveBeenCalled() // 只是打開確認對話，還沒確認
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith('/Users/me')
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(wsc.blocked).toBe(false)
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('確認失敗：保留警示、顯示錯誤、狀態不變', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w } = mountBanner({ confirm: async () => { throw new Error('workspace_confirmation_path_mismatch') } })
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=ws-confirm-error]').text()).toContain('path_mismatch')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(true)
    expect(wsc.blockKind).toBe('pending')
  })

  it('取消：只關閉這次確認操作——不呼叫 binding、不改變狀態', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w, confirm, refresh } = mountBanner()
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(true)
    await w.find('[data-test=ws-cancel]').trigger('click')
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(false)
    expect(confirm).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()
    expect(wsc.blockKind).toBe('pending')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(true)
  })

  it('元件重新掛載不會自動確認', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const first = mountBanner()
    first.w.unmount()
    const second = mountBanner()
    await flushPromises()
    expect(first.confirm).not.toHaveBeenCalled()
    expect(second.confirm).not.toHaveBeenCalled()
    expect(second.w.find('[data-test=ws-alert]').exists()).toBe(true)
  })
})
