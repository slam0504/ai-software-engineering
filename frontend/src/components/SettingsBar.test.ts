import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// wailsjs 綁定全部 mock：分頁切換不得觸發任何 backend 呼叫
const mocks = vi.hoisted(() => ({
  TerminateSession: vi.fn(), EndSession: vi.fn(), NewSession: vi.fn(),
  CreateSession: vi.fn(async () => 'w-new'), AuthStatus: vi.fn(),
  StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
  RestartCodexServerRecorded: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

import SettingsBar from './SettingsBar.vue'
import { useSession } from '../stores/session'
import { mountWithI18n } from '../test/i18n'

// two：兩個已註冊 session，w1 釘在 pane 0（focused）、w2 尚未釘選。
function two() {
  const s = useSession()
  s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
  s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
  s.pin(0, 'w1')
  s.setFocus(0)
  return s
}

describe('SettingsBar session 分頁（Task 26：WSID 定址）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockClear()
  })

  it('分頁點擊切換 focused pane 的 session，不觸發任何 backend 呼叫', async () => {
    const s = two()
    const w = mountWithI18n(SettingsBar)
    await w.find('[data-test=session-tab-w2]').trigger('click')
    expect(s.focusedWsid).toBe('w2')
    await w.find('[data-test=session-tab-w1]').trigger('click')
    expect(s.focusedWsid).toBe('w1')
    for (const [name, fn] of Object.entries(mocks)) {
      expect(fn, name).not.toHaveBeenCalled()
    }
  })

  it('per-session unread 徽章與待核可標記', async () => {
    const s = two()
    const wrapper = mountWithI18n(SettingsBar)
    s.apply({ event_id: 'e1', ts: 't', provider: 'codex', kind: 'result', workspace_session_id: 'w2' })
    s.apply({ event_id: 'e2', ts: 't', provider: 'codex', kind: 'state_change',
      state: 'awaiting_approval', workspace_session_id: 'w2' })
    await wrapper.vm.$nextTick()
    const tab = wrapper.find('[data-test=session-tab-w2]')
    expect(tab.find('.badge').text()).toBe('1')
    expect(tab.find('.await').exists()).toBe(true)
    await tab.trigger('click') // 切入歸零
    await wrapper.vm.$nextTick()
    expect(tab.find('.badge').exists()).toBe(false)
  })

  it('lifecycle 操作一律帶 focused pane 的 WSID，不是 provider 名', async () => {
    const s = two()
    const w = mountWithI18n(SettingsBar)
    s.setFocus(0)
    await w.find('[data-test=end-session]').trigger('click')
    expect(mocks.EndSession).toHaveBeenCalledWith('w1')
    await w.find('[data-test=session-tab-w2]').trigger('click')
    await w.find('[data-test=end-session]').trigger('click')
    expect(mocks.EndSession).toHaveBeenLastCalledWith('w2')
    expect(mocks.EndSession).not.toHaveBeenCalledWith('claude')
    expect(mocks.EndSession).not.toHaveBeenCalledWith('codex')
  })

  it('建立按鈕登記新 session 並釘進目前 pane', async () => {
    const s = useSession()
    const w = mountWithI18n(SettingsBar)
    await w.find('[data-test=create-claude]').trigger('click')
    await Promise.resolve()
    expect(mocks.CreateSession).toHaveBeenCalledWith('claude', '')
    expect(s.sessions['w-new']).toMatchObject({ provider: 'claude' })
    expect(s.pins[0]).toBe('w-new')
  })

  it('沒有 focused session 時 lifecycle 按鈕停用（避免打空 WSID）', async () => {
    useSession()
    const w = mountWithI18n(SettingsBar)
    expect(w.find('[data-test=end-session]').attributes('disabled')).toBeDefined()
  })

  it('codex session 的 recordCase 欄位停用並說明只剩 label（§3.4.4）', async () => {
    two()
    const w = mountWithI18n(SettingsBar)
    await w.find('[data-test=session-tab-w2]').trigger('click') // codex
    await w.vm.$nextTick()
    const rec = w.findAll('input').find(i => i.attributes('title')?.includes('§3.4.4'))
    expect(rec, 'codex recordCase 欄位必須帶說明').toBeTruthy()
    expect(rec!.attributes('disabled')).toBeDefined()
    await w.find('[data-test=session-tab-w1]').trigger('click') // 切回 claude
    await w.vm.$nextTick()
    expect(w.findAll('input').some(i => i.attributes('disabled') !== undefined)).toBe(false)
  })
})
