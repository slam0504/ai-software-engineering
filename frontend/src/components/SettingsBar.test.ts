import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

// wailsjs 綁定全部 mock：分頁切換不得觸發任何 backend 呼叫
const mocks = vi.hoisted(() => ({
  TerminateSession: vi.fn(), NewSession: vi.fn(),
  CreateSession: vi.fn(async () => 'w-new'), AuthStatus: vi.fn(),
  StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
  RestartCodexServerRecorded: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

import SettingsBar from './SettingsBar.vue'
import { useSession } from '../stores/session'
import { mountWithI18n } from '../test/i18n'

// two：兩個已註冊 session，w1 釘在 pane 0（focused）、w2 尚未釘選。
// review round 1（Task 28）：End 改走 s.bindings.EndSession（不再是 wailsjs
// 原始 import），這裡改用 s.setBindings() 注入同一份 mock，斷言也從
// mocks.EndSession 改成 s.bindings.EndSession。
function two() {
  const s = useSession()
  s.setBindings({
    StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), EndSession: vi.fn(async () => {}),
  })
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

  // F3（本票，spec §4）：selectSession() 的「已釘選」分支目前只 setFocus，
  // 即使 view 因首載失敗被 F2 清掉也不重新載入。這裡鎖住真實 UI 重試路徑：
  // 已釘選但 view 缺失時再點同一個分頁，必須重新呼叫 binding（不是只切
  // focus）。
  it('已釘選但首載失敗清空 view 後再點——binding 被重試呼叫（真實 UI 重試）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const load = vi.fn()
      .mockRejectedValueOnce(new Error('load boom'))
      .mockResolvedValueOnce([])
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), LoadTurnsBefore: load })
    const w = mountWithI18n(SettingsBar)

    await w.find('[data-test=session-tab-w1]').trigger('click')
    await flushPromises()
    expect(s.views['w1']).toBeUndefined() // F2：reject 後 catch 清掉壞掉的 view
    expect(s.pins[0]).toBe('w1') // 已釘選——不是「未釘選」分支

    await w.find('[data-test=session-tab-w1]').trigger('click')
    await flushPromises()

    expect(load).toHaveBeenCalledTimes(2)
  })

  // 反向（owner review rev2 P1 定案——action spy）：view 存在時再點同一個分頁，
  // 必須維持既有語意（只切 focus，不重新釘一次），不得因本票的修正誤用成
  // `pin(at, w)`。行為面快照在無 transient 的 fixture 下無法區辨這兩者，改對
  // store action 加 spy：`setFocus(at)` 被呼叫且 `pin` 未被呼叫。
  it('反向守門（action spy）：view 存在時再點只切 focus，不誤用 pin', async () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), EndSession: vi.fn(async () => {}) })
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    s.setFocus(0)
    const w = mountWithI18n(SettingsBar)
    const pinSpy = vi.spyOn(s, 'pin')
    const focusSpy = vi.spyOn(s, 'setFocus')

    await w.find('[data-test=session-tab-w2]').trigger('click')
    await flushPromises()

    expect(focusSpy).toHaveBeenCalledWith(1)
    expect(pinSpy).not.toHaveBeenCalled()
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
    expect(s.bindings?.EndSession).toHaveBeenCalledWith('w1')
    await w.find('[data-test=session-tab-w2]').trigger('click')
    await w.find('[data-test=end-session]').trigger('click')
    expect(s.bindings?.EndSession).toHaveBeenLastCalledWith('w2')
    expect(s.bindings?.EndSession).not.toHaveBeenCalledWith('claude')
    expect(s.bindings?.EndSession).not.toHaveBeenCalledWith('codex')
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

  it('registry 有、Manager 無 slot 的 session 在清單上明確標示不可操作', async () => {
    const s = useSession()
    s.hydrateSessions([
      { wsid: 'w1', provider: 'claude', task_label: 'a', resume_session_id: '', created_at: '', available: true, state: 'idle' },
      { wsid: 'wOrphan', provider: 'codex', task_label: 'b', resume_session_id: '', created_at: '', available: false, state: '' },
    ])
    const w = mountWithI18n(SettingsBar)
    await w.vm.$nextTick()
    const orphan = w.find('[data-test=session-tab-wOrphan]')
    expect(orphan.classes()).toContain('unavailable') // 不隱藏、據實呈現
    expect(orphan.attributes('title')).toContain('wOrphan') // tooltip 說明為什麼
    expect(w.find('[data-test=session-tab-w1]').classes()).not.toContain('unavailable')
    void s
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
