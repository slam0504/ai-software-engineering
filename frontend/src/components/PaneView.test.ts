import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import PaneView from './PaneView.vue'
import { useSession } from '../stores/session'
import { mountWithI18n } from '../test/i18n'
import type { Envelope } from '../types'

// PaneView（Task 29，spec §3.8）：釘選觸發尾端視窗 lazy load、捲到頂觸發向上
// 分頁。空 cursor＝尾端視窗（含未結束的目前 turn）；帶 cursor（此前已載入的
// 最舊 event_id）＝該 turn 之前的一頁，回傳依 event id 遞增排列、與該頁不重疊
// （§3.8 契約；rebuild_orchestrator.go LoadTurnsBefore doc）。
//
// 這是修掉 Task 26→28 之間刻意接受的暫時降級的票：重啟後 registry 仍記得
// session、使用者重新把它釘進 pane，但 pin() 過去只建空殼 view，從不回填歷史
// ——metadata／unread／狀態正常，畫面卻像全新對話。fixture 用 production
// LoadTurnsBefore 實際會回的形狀（contract.Envelope 陣列，帶
// workspace_session_id），不是隨意欄位。

// hist：兩則歷史事件組成一個完整 turn（user＋assistant），是尾端視窗 mock 的
// 預設回傳——讓 pin() 之後 timeline 非空，後續分頁測試才有游標可用。
const hist = (wsid: string): Envelope[] => [
  { event_id: 'h1', ts: 't1', provider: 'claude', kind: 'message', role: 'user', text: 'hist user', workspace_session_id: wsid },
  { event_id: 'h2', ts: 't2', provider: 'claude', kind: 'message', role: 'assistant', text: 'hist reply', workspace_session_id: wsid },
]

function bindingsWithHistory(load = vi.fn(async (wsid: string) => hist(wsid))) {
  return {
    StartSession: vi.fn(async () => {}),
    SendMessage: vi.fn(async () => {}),
    LoadTurnsBefore: load,
  }
}

describe('session store：釘選觸發尾端視窗 lazy load（§3.8，Task 29）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('釘選時 lazy load 尾端視窗', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    expect(s.views['w9']).toBeUndefined()
    await s.pin(0, 'w9')
    expect(s.bindings!.LoadTurnsBefore).toHaveBeenCalledWith('w9', '', 20)
    expect(s.views['w9']).toBeDefined()
  })

  it('捲到頂以每次 20 turn 分頁並以 event_id 去重，插在較舊的一端', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await s.pin(0, 'w9')
    const oldest = s.views['w9'].timeline[0].env.event_id
    vi.mocked(s.bindings!.LoadTurnsBefore!).mockResolvedValueOnce([
      { event_id: oldest, kind: 'message' } as Envelope,
      { event_id: 'older-1', kind: 'message' } as Envelope,
    ])
    await s.loadOlder('w9')
    expect(s.bindings!.LoadTurnsBefore).toHaveBeenLastCalledWith('w9', oldest, 20)
    const ids = s.views['w9'].timeline.map(i => i.env.event_id)
    expect(new Set(ids).size).toBe(ids.length) // 去重：重複的 oldest 不得再出現一次
    // 位置也要驗——只驗 toContain 抓不到「插到尾端」這種 push 而非 unshift
    // 的錯誤（結果集合一樣、順序全反）。
    expect(ids[0]).toBe('older-1')
    expect(ids[1]).toBe(oldest)
  })

  it('分頁不重設捲動錨點', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await s.pin(0, 'w9')
    s.setScrollAnchor('w9', 'keep-me')
    await s.loadOlder('w9')
    expect(s.scrollAnchors['w9']).toBe('keep-me')
  })

  // review 教訓 (A)：上面三條各自只驗一條路徑本身聲稱的行為，這裡另外補
  // 「不該發生的事沒發生」——分開驗證，避免同一條 setup 路徑掩蓋單一機制。

  it('已有 view（同一 wsid 釘在另一 pane）時不重新載入，不呼叫第二次', async () => {
    const s = useSession()
    const b = bindingsWithHistory()
    s.setBindings(b)
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await s.pin(0, 'w9')
    await s.pin(1, 'w9') // 同一個 wsid 釘進第二個 pane
    expect(b.LoadTurnsBefore).toHaveBeenCalledTimes(1)
  })

  it('沒有 bindings.LoadTurnsBefore 時退回空殼 view，不拋錯（dev／舊 mock 相容）', async () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) })
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await expect(s.pin(0, 'w9')).resolves.toBeUndefined()
    expect(s.views['w9']).toBeDefined()
    expect(s.views['w9'].timeline).toHaveLength(0)
  })

  it('view 存在但 timeline 是空的（尾端視窗回空陣列）時 loadOlder 不多呼叫一次——沒有游標可分頁', async () => {
    // 特意跟上一條（沒有 binding）分開驗：這裡 binding 是有 LoadTurnsBefore
    // 的，用來單獨鎖住 loadOlder 自己的「空 cursor → 不呼叫」判斷，避免跟
    // 「bindings 缺 LoadTurnsBefore」那條分支疊在一起、測不出兩者是各自獨立
    // 的守門（review 教訓 A）。
    const s = useSession()
    const load = vi.fn(async () => [] as Envelope[]) // 尾端視窗本身就回空陣列
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), LoadTurnsBefore: load })
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await s.pin(0, 'w9')
    expect(load).toHaveBeenCalledTimes(1) // 尾端視窗那一次
    await s.loadOlder('w9')
    expect(load).toHaveBeenCalledTimes(1) // 沒有游標：loadOlder 不該再呼叫一次
  })

  it('已到最舊（backend 回空陣列）時 timeline 不變、不拋錯', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    await s.pin(0, 'w9')
    const before = s.views['w9'].timeline.map(i => i.env.event_id)
    vi.mocked(s.bindings!.LoadTurnsBefore!).mockResolvedValueOnce([])
    await s.loadOlder('w9')
    expect(s.views['w9'].timeline.map(i => i.env.event_id)).toEqual(before)
  })

  // 「重啟後重新釘選 transcript 空白」的既定降級——這裡用 hydrateSessions
  // （ListSessions() 的真實路徑）模擬重啟後的 registry hydrate，而非
  // registerSession，貼近 App.vue onMounted 的實際呼叫順序。
  it('重啟情境：hydrateSessions 之後重新釘選，歷史對話回填（修掉 Task 26→28 的暫時降級）', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.hydrateSessions([
      { wsid: 'w9', provider: 'claude', task_label: 'resumed', resume_session_id: 's-old', created_at: '', available: true, state: 'idle' },
    ])
    expect(s.views['w9']).toBeUndefined() // hydrate 只帶 metadata，尚未釘選
    await s.pin(0, 'w9')
    expect(s.views['w9'].chat.map(c => c.text)).toEqual(['hist user', 'hist reply'])
    expect(s.sessions['w9'].taskLabel).toBe('resumed') // metadata 這半本來就正常，順帶確認沒被歷史回放蓋掉
  })

  // review Important：pin() 的 await-window 防護只做存在性檢查，不是身分
  // 檢查。真正可達的分支是「pin(A) 載入中，同一 pane 被切去 pin(B)」——
  // unpin() 目前零 UI 呼叫端，所以「同一 wsid 刪除後重建」那條不可達，不在
  // 這裡測（見 pin() 內的文件）。
  it('pin(A) 載入中切去 pin(B)：A 稍後才回來的歷史事件不得汙染 B 的 view', async () => {
    const s = useSession()
    let resolveA!: (envs: Envelope[]) => void
    const loadA = new Promise<Envelope[]>(r => { resolveA = r })
    const load = vi.fn()
      .mockImplementationOnce(() => loadA) // pin(0,'A') 卡住，模擬慢請求
      .mockImplementationOnce(async () => hist('B'))
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}), LoadTurnsBefore: load })
    s.registerSession({ wsid: 'A', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'B', provider: 'claude', taskLabel: '' })

    const pinA = s.pin(0, 'A') // 不 await：卡在 LoadTurnsBefore 那個 await
    await s.pin(0, 'B') // 使用者在 A 還沒回來前把同一個 pane 切去 B
    resolveA(hist('A')) // A 的慢請求這時才回來
    await pinA

    expect(s.views['A']).toBeUndefined() // 已被 releaseView 刪除，不該被稍後回來的資料復活
    expect(s.views['B'].chat.map(c => c.text)).toEqual(['hist user', 'hist reply']) // B 沒被汙染
  })
})

describe('PaneView：歷史真的渲染成 DOM（不只是 mock 被呼叫，見 review 教訓 D）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {})
  })

  it('釘選後歷史氣泡出現在畫面上', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await s.pin(0, 'w9')
    await nextTick()
    const text = w.find('[data-test=pane-0]').text()
    expect(text).toContain('hist user')
    expect(text).toContain('hist reply')
  })

  it('捲到頂載入更舊 turn 後，舊訊息渲染在既有氣泡之上（不是接在尾端）', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await s.pin(0, 'w9')
    vi.mocked(s.bindings!.LoadTurnsBefore!).mockResolvedValueOnce([
      { event_id: 'older-1', ts: 't0', provider: 'claude', kind: 'message', role: 'user', text: 'older turn text', workspace_session_id: 'w9' },
    ])
    await s.loadOlder('w9')
    await nextTick()
    const bubbles = w.findAll('.bubble')
    expect(bubbles[0].text()).toContain('older turn text') // 較舊的插到最前面，不是 push 到最後
    expect(bubbles.at(-1)!.text()).toContain('hist reply')
  })
})

// review Critical：loadOlder()／setScrollAnchor() 過去零 UI 呼叫端——store 邏輯
// 對、mutation 也守得住，但 onScroll() 從沒偵測「捲到頂」也沒呼叫它們，功能在
// 真實 app 裡使用者碰不到（跟 Task 27 SessionList 沒掛上 App.vue 同一個形狀）。
// 這裡直接模擬 DOM 'scroll' 事件（不是像上面幾條測試直接呼叫 s.loadOlder()），
// 證明 onScroll() 真的接上了。
describe('PaneView：捲到頂觸發向上分頁（模擬捲動事件，不是直接呼叫 store action）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {})
  })

  // jsdom 不做真實版面計算，scrollTop／scrollHeight／clientHeight 這幾個唯讀
  // getter 要手動覆寫成可控值，才能模擬「捲到頂」這個幾何狀態。
  function stubGeometry(el: HTMLElement, scrollTop: number, scrollHeight: number, clientHeight: number) {
    Object.defineProperty(el, 'scrollTop', { value: scrollTop, configurable: true, writable: true })
    Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
    Object.defineProperty(el, 'clientHeight', { value: clientHeight, configurable: true })
  }

  it('捲到頂（scrollTop 在 slack 內）觸發 loadOlder，游標帶目前最舊 event_id，並記錄捲動錨點', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await s.pin(0, 'w9')
    await nextTick()
    const load = vi.mocked(s.bindings!.LoadTurnsBefore!)
    load.mockClear()
    load.mockResolvedValueOnce([
      { event_id: 'older-1', ts: 't0', provider: 'claude', kind: 'message', role: 'user', text: 'older turn text', workspace_session_id: 'w9' },
    ])
    const el = w.find('.msgs').element as HTMLElement
    stubGeometry(el, 0, 400, 200) // scrollTop=0：在頂端
    await w.find('.msgs').trigger('scroll')
    await nextTick()
    expect(load).toHaveBeenCalledWith('w9', 'h1', 20) // 'h1'＝pin() 尾端視窗載入的最舊事件
    expect(s.scrollAnchors['w9']).toBe('h1')
    expect(w.find('[data-test=pane-0]').text()).toContain('older turn text')
  })

  it('不在頂端（scrollTop 遠大於 slack）捲動不觸發 loadOlder', async () => {
    const s = useSession()
    s.setBindings(bindingsWithHistory())
    s.registerSession({ wsid: 'w9', provider: 'claude', taskLabel: '' })
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await s.pin(0, 'w9')
    await nextTick()
    const load = vi.mocked(s.bindings!.LoadTurnsBefore!)
    load.mockClear()
    const el = w.find('.msgs').element as HTMLElement
    stubGeometry(el, 500, 1000, 200) // 遠離頂端也遠離底部
    await w.find('.msgs').trigger('scroll')
    await nextTick()
    expect(load).not.toHaveBeenCalled()
  })
})
