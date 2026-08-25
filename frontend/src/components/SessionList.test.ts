import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

// wailsjs 綁定全部 mock：清單渲染與計數不得觸發任何 backend 呼叫
const mocks = vi.hoisted(() => ({
  CreateSession: vi.fn(async () => 'w-new'),
  RemoveSession: vi.fn(async () => undefined),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

import SessionList from './SessionList.vue'
import { useSession } from '../stores/session'
import { mountWithI18n } from '../test/i18n'

describe('SessionList（Task 27，spec §4）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockClear()
  })

  it('只顯示既有 session，不畫固定 8 張空卡', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(1)
  })

  it('沒有任何 session 時顯示空狀態說明，而非畫出隱藏的固定卡片', () => {
    useSession()
    const w = mountWithI18n(SessionList)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(0)
    expect(w.find('[data-test=empty]').exists()).toBe(true)
  })

  it('顯示 per-provider n / 4 計數', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    const w = mountWithI18n(SessionList)
    expect(w.find('[data-test=count-claude]').text()).toBe('2 / 4')
    expect(w.find('[data-test=count-codex]').text()).toBe('0 / 4')
  })

  it('達上限時建立按鈕停用，未達上限的 provider 不受影響', async () => {
    const s = useSession()
    for (let n = 1; n <= 4; n++) s.registerSession({ wsid: 'w' + n, provider: 'codex', taskLabel: '' })
    const w = mountWithI18n(SessionList)
    expect(w.find('[data-test=create-codex]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=create-claude]').attributes('disabled')).toBeUndefined()
  })

  it('卡片顯示 unread 與待核可標記', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.sessions['w1'].unread = 3
    s.sessions['w1'].awaitingApproval = true
    const w = mountWithI18n(SessionList)
    await w.vm.$nextTick()
    expect(w.find('[data-test=unread-w1]').text()).toBe('3')
    expect(w.find('[data-test=awaiting-w1]').exists()).toBe(true)
  })

  it('unread 為 0、未待核可時不畫出對應標記（不是隱藏的空殼）', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    expect(w.find('[data-test=unread-w1]').exists()).toBe(false)
    expect(w.find('[data-test=awaiting-w1]').exists()).toBe(false)
  })

  it('busy 時顯示忙碌標記，收尾後消失', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.sessions['w1'].busy = true
    const w = mountWithI18n(SessionList)
    await w.vm.$nextTick()
    expect(w.find('[data-test=busy-w1]').exists()).toBe(true)
    s.sessions['w1'].busy = false
    await w.vm.$nextTick()
    expect(w.find('[data-test=busy-w1]').exists()).toBe(false)
  })

  it('registry 有、Manager 無 slot 的 session 據實呈現為不可操作（沿用 Task 26 慣例）', async () => {
    const s = useSession()
    s.hydrateSessions([
      { wsid: 'wOrphan', provider: 'codex', task_label: 'b', resume_session_id: '', created_at: '', available: false, state: '' },
    ])
    const w = mountWithI18n(SessionList)
    await w.vm.$nextTick()
    const card = w.find('[data-test=session-card]')
    expect(card.classes()).toContain('unavailable')
    expect(w.find('[data-test=unavailable-wOrphan]').text()).toContain('wOrphan')
  })

  it('可用的 session 卡片不帶 unavailable class／說明', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    const card = w.find('[data-test=session-card]')
    expect(card.classes()).not.toContain('unavailable')
    expect(w.find('[data-test=unavailable-w1]').exists()).toBe(false)
  })

  it('點擊釘選把該 session 帶入目前 focused pane', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=pin-w1]').trigger('click')
    expect(s.pins[0]).toBe('w1')
  })

  it('已釘選的 session 再次點擊只切 focus，不重新釘一次（transcript 不被重置）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    s.setFocus(0)
    const w = mountWithI18n(SessionList)
    const view1 = s.views['w1']
    await w.find('[data-test=pin-w2]').trigger('click')
    expect(s.focused).toBe(1)
    expect(s.pins).toEqual(['w1', 'w2']) // 沒有被踢掉重灌
    expect(s.views['w1']).toBe(view1) // w1 的 transcript 容器沒被換掉
  })

  // F3（本票，spec §4）：pin() 的「已釘選」分支目前只 setFocus，即使 view
  // 因首載失敗被 F2 清掉也不重新載入——使用者除了重整整個 App 沒有其他方法
  // 重試。這裡鎖住真實 UI 重試路徑：已釘選但 view 缺失時再點同一顆釘選鈕，
  // 必須重新呼叫 binding（不是只切 focus）。
  it('已釘選但首載失敗清空 view 後再點——binding 被重試呼叫（真實 UI 重試）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const load = vi.fn()
      .mockRejectedValueOnce(new Error('load boom'))
      .mockResolvedValueOnce([])
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), LoadTurnsBefore: load })
    const w = mountWithI18n(SessionList)

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()
    expect(s.views['w1']).toBeUndefined() // F2：reject 後 catch 清掉壞掉的 view
    expect(s.persistentPins[0]).toBe('w1') // 已釘選——不是「未釘選」分支

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()

    expect(load).toHaveBeenCalledTimes(2)
  })

  // 反向（owner review rev2 P1 定案——action spy）：view 存在時再點同一顆釘選
  // 鈕，必須維持既有語意（只切 focus，不重新釘一次），不得因本票的修正誤用
  // 成 `pin(at, wsid)`。行為面快照（pins／focused／view 實例）在無 transient
  // 的 fixture 下兩條路徑結果相同、無法區辨這兩者，所以直接對 store action
  // 加 spy：`setFocus(at)` 被呼叫且 `pin` 未被呼叫。
  it('反向守門（action spy）：view 存在時再點只切 focus，不誤用 pin', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    s.setFocus(0)
    const w = mountWithI18n(SessionList)
    const pinSpy = vi.spyOn(s, 'pin')
    const focusSpy = vi.spyOn(s, 'setFocus')

    await w.find('[data-test=pin-w2]').trigger('click')
    await flushPromises()

    expect(focusSpy).toHaveBeenCalledWith(1)
    expect(pinSpy).not.toHaveBeenCalled()
  })

  it('建立按鈕呼叫 CreateSession 並登記＋釘選新 session', async () => {
    useSession()
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=create-claude]').trigger('click')
    await Promise.resolve()
    expect(mocks.CreateSession).toHaveBeenCalledWith('claude', '')
    const s = useSession()
    expect(s.sessions['w-new']).toMatchObject({ provider: 'claude' })
    expect(s.pins[0]).toBe('w-new')
  })

  it('移除需確認並說明稽核保留', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=remove-w1]').trigger('click')
    expect(w.find('[data-test=remove-confirm]').text()).toContain('稽核事件與錄流會永久保留')
  })

  it('取消移除不呼叫 RemoveSession、關閉確認面板', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-cancel]').trigger('click')
    expect(w.find('[data-test=remove-confirm]').exists()).toBe(false)
    expect(mocks.RemoveSession).not.toHaveBeenCalled()
    expect(s.sessions['w1'].removed).toBe(false)
  })

  it('確認移除成功後卡片從清單消失（markRemoved）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await Promise.resolve()
    await w.vm.$nextTick()
    expect(mocks.RemoveSession).toHaveBeenCalledWith('w1')
    expect(s.sessions['w1'].removed).toBe(true)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(0)
  })

  // 帶入事項 1：RemoveSession 可能在 tombstone 已落盤後，因 Manager 釋放名額
  // 失敗而回錯（殘留 TOCTOU 窗口，見 app.go RemoveSession doc／Task 26
  // review round-2）。這裡鎖住失敗時的據實呈現：卡片繼續存在（不靜默視為已
  // 移除，使用者不會誤以為刪成功），且 store 收到錯誤訊息原文（fail loud）。
  it('RemoveSession 失敗時據實呈現——卡片不消失、錯誤原文可見，不誤判成功', async () => {
    mocks.RemoveSession.mockRejectedValueOnce(new Error('app: remove session w1: 已 tombstone 但釋放名額失敗：not idle'))
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await Promise.resolve()
    await Promise.resolve()
    await w.vm.$nextTick()
    expect(s.sessions['w1'].removed).toBe(false)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(1)
    const noticeText = s.notices.map(n => n.env.error ?? n.env.text ?? '').join(' ')
    expect(noticeText).toContain('已 tombstone 但釋放名額失敗')
  })

  // review fix（Important）：對一個 streaming／忙碌中的 session 按移除，
  // Removable() 會在最前面直接擋下（ErrSubmitActive／ErrStartInProgress），
  // 完全不碰後端狀態——但這裡從沒把 busy 設成 true，pushError 預設的清 busy
  // 副作用不該套用在這條路徑。守住：失敗後 busy 維持原值，busy-dot 仍在。
  it('對忙碌中的 session 移除失敗——busy 不被誤清，busy-dot 仍在', async () => {
    mocks.RemoveSession.mockRejectedValueOnce(new Error('app: remove session w1: session not idle'))
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.sessions['w1'].busy = true
    const w = mountWithI18n(SessionList)
    await w.vm.$nextTick()
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await Promise.resolve()
    await Promise.resolve()
    await w.vm.$nextTick()
    expect(s.sessions['w1'].busy).toBe(true)
    expect(w.find('[data-test=busy-w1]').exists()).toBe(true)
  })
})
