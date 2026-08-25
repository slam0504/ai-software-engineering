import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { useSession } from './session'
import type { Envelope } from '../types'
import zhTW from '../i18n/locales/zh-TW'
import en from '../i18n/locales/en'

// F2（spec §3／§4 前端清單）：pin()／loadOlder() 的載入錯誤攔截——proxy 身分
// 比對防止 A→B→A 時序汙染新實例、重試前置（catch 會把壞掉的 view 刪掉，讓
// 下一次 pin 重新走 isNew 分支）、notice 不動 busy。
//
// hist：兩則歷史事件組成一個完整 turn，與 PaneView.test.ts 的 hist() 同一慣例
// ——尾端視窗 mock 的預設回傳，讓 pin() 之後 timeline 非空、有游標可分頁。
const hist = (wsid: string): Envelope[] => [
  { event_id: 'h1', ts: 't1', provider: 'claude', kind: 'message', role: 'user', text: 'hist user', workspace_session_id: wsid },
  { event_id: 'h2', ts: 't2', provider: 'claude', kind: 'message', role: 'assistant', text: 'hist reply', workspace_session_id: wsid },
]

// bindings：binding stub 統一採 registryUncertain.test.ts:43 的
// `LoadTurnsBefore: vi.fn()` 形——但 Bindings.StartSession／SendMessage 是必填
// 欄位（session.test.ts 的 okBindings() 同一慣例），這裡補成 no-op。
function bindings(load: (wsid: string, beforeEventID: string, n: number) => Promise<Envelope[]>) {
  return {
    StartSession: vi.fn(async () => {}),
    SendMessage: vi.fn(async () => {}),
    LoadTurnsBefore: load,
  }
}

// REGISTRY_UNCERTAIN_MARK 的實際片語（session.ts 內部常數，未匯出）——逐字對照
// registryUncertain.test.ts 的 GO_ERROR 慣例，不猜測、不簡化。
const MARK = 'session registry 上一次寫入的結果不確定'

describe('pin()：載入失敗攔截（proxy 身分比對、重試前置、notice 不動 busy）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // (1) pin reject → view 不存在、notice 有錯誤、busy（前置 true）不受影響、
  // errorSeq 增；重試＝下一次 pin 同一個 wsid 再呼叫一次 binding（isNew 靠
  // views[wsid] 是否存在判斷，catch 刪掉壞掉的 view 正是讓下一次 pin 重新
  // 走一次 lazy load 的機制）。
  it('reject 時不留下壞掉的 view、寫 notice、不動 busy，且可重試', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.sessions['w1'].busy = true // 前置：驗證 pin 的 catch 不動它
    const load = vi.fn().mockRejectedValue(new Error('load boom'))
    s.setBindings(bindings(load))
    const before = s.errorSeq

    await s.pin(0, 'w1')

    expect(s.views['w1']).toBeUndefined()
    expect(s.notices.at(-1)!.env.error).toContain('w1')
    expect(s.notices.at(-1)!.env.error).toContain('load boom')
    expect(s.sessions['w1'].busy).toBe(true)
    expect(s.errorSeq).toBe(before + 1)

    await s.pin(0, 'w1') // 重試：views['w1'] 已被刪，isNew 重新成立
    expect(load).toHaveBeenCalledTimes(2)
  })

  // (2) latch 片語走同一條路徑：pushNotice → applyNotice → bumpError，含
  // REGISTRY_UNCERTAIN_MARK 時 latchSeq 另計。
  it('reject 且錯誤含 REGISTRY_UNCERTAIN_MARK → latchSeq 增', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    const load = vi.fn().mockRejectedValue(new Error(`wsregistry: ${MARK}（檔案已 rename 但 directory sync 失敗）`))
    s.setBindings(bindings(load))
    const before = s.latchSeq

    await s.pin(0, 'w1')

    expect(s.latchSeq).toBe(before + 1)
  })

  // (3a) A→B→A（reject 變體）：同一個 pane 從 A 切到 B 再切回 A——不需要
  // unpin() UI（目前零呼叫端）即可達：pin() 自己在 prev!==wsid 時就會
  // releaseView(prev)。第一次 pin('A') 的載入還沒回來時，pane 已經又切回
  // 'A'（建出第二實例）；這時第一次的 load 才 reject，不得刪掉第二實例。
  it('A→B→A（reject）：舊實例的 reject 不刪除新實例的 view', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'A', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'B', provider: 'claude', taskLabel: '' })
    let rejectA1!: (e: Error) => void
    const loadA1 = new Promise<Envelope[]>((_, rej) => { rejectA1 = rej })
    const load = vi.fn()
      .mockImplementationOnce(() => loadA1) // pin(0,'A') 第一次：卡住
      .mockImplementationOnce(async () => hist('B')) // pin(0,'B')
      .mockImplementationOnce(async () => []) // pin(0,'A') 第二次
    s.setBindings(bindings(load))

    const pinA1 = s.pin(0, 'A') // 不 await：卡在 LoadTurnsBefore
    await s.pin(0, 'B') // releaseView('A') 刪掉第一實例
    await s.pin(0, 'A') // 建第二實例
    const secondInstance = s.views['A']
    expect(secondInstance).toBeDefined()

    rejectA1(new Error('stale reject'))
    await pinA1 // pin() 內部自己 catch，這裡不會拋出

    expect(s.views['A']).toBe(secondInstance) // 沒被第一實例的 reject 刪掉
  })

  // (3b) A→B→A（resolve 變體）：第一實例的 load 遲來的 resolve 也不得把舊
  // envelope 套到第二實例——同一段 identity 比對同時擋 reject 與 resolve
  // 兩條路。
  it('A→B→A（resolve）：舊實例遲來的 envelope 不套用到新實例', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'A', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'B', provider: 'claude', taskLabel: '' })
    let resolveA1!: (envs: Envelope[]) => void
    const loadA1 = new Promise<Envelope[]>(res => { resolveA1 = res })
    const load = vi.fn()
      .mockImplementationOnce(() => loadA1)
      .mockImplementationOnce(async () => hist('B'))
      .mockImplementationOnce(async () => [])
    s.setBindings(bindings(load))

    const pinA1 = s.pin(0, 'A')
    await s.pin(0, 'B')
    await s.pin(0, 'A')
    const secondInstance = s.views['A']

    resolveA1(hist('A')) // 第一實例的尾端視窗這時才回來
    await pinA1

    expect(s.views['A']).toBe(secondInstance)
    expect(s.views['A'].timeline).toHaveLength(0) // 沒被舊 envelope 汙染
  })

  // (4) pin 不向外 reject：restoreLayout 逐格 await this.pin()，pane 0 的
  // load reject 必須被 pin() 自己吞掉，否則 pane 1 永遠不會被還原。
  it('restoreLayout：pane 0 load reject 不擋 pane 1 還原（pin 不向外 reject）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'A', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'B', provider: 'claude', taskLabel: '' })
    const load = vi.fn()
      .mockImplementationOnce(async () => { throw new Error('pane0 boom') })
      .mockImplementationOnce(async () => hist('B'))
    s.setBindings(bindings(load))

    await expect(s.restoreLayout({ pins: ['A', 'B'], focused: 'B' })).resolves.toBeUndefined()

    expect(s.views['A']).toBeUndefined()
    expect(s.views['B']).toBeDefined()
    expect(s.views['B'].chat.map(c => c.text)).toEqual(['hist user', 'hist reply'])
  })

  // (5) 反向：persistentPins 不因載入失敗回退——這是既有行為（pin() 一開始
  // 就同步寫 persistentPins，早於 `await load(...)`），實作前就該是綠的。
  //
  // review 教訓：斷言必須等 promise（含 catch continuation）收斂之後再下，
  // 不能緊跟 `s.pin(...)` 同步斷言——那只重驗了呼叫當下的同步賦值，對「catch
  // 裡誤清 persistentPins」這種 mutation 零鑑別力（reviewer 實測在 catch 塞一行
  // `this.persistentPins[idx] = null`，同步斷言版本 11/11 仍綠）。先
  // `await p.catch(() => {})` 讓 catch 真的跑完，再斷言才測得到 catch 沒有
  // 動 persistentPins。
  it('pin 失敗後 persistentPins 不回退', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.setBindings(bindings(vi.fn(async () => { throw new Error('boom') })))

    const p = s.pin(0, 'w1')
    await p.catch(() => {}) // 消化 rejection，避免 unhandled；也讓 catch continuation 跑完
    expect(s.persistentPins[0]).toBe('w1')
  })
})

describe('loadOlder()：分頁失敗攔截（notice、不動 view／busy，可重試）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // (6) wsid 釘進 focused pane、busy 前置 true：分頁失敗只 pushNotice，
  // timeline／busy 都不受影響；再呼叫一次要能再打 binding（沒有任何 mutation
  // 守門擋住重試）。
  it('reject → timeline 不變、busy 仍 true、notice 有錯誤，可再重試', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    const load = vi.fn()
      .mockImplementationOnce(async () => hist('w1')) // pin 的尾端視窗
      .mockImplementationOnce(async () => { throw new Error('older boom') }) // loadOlder 失敗
      .mockImplementationOnce(async () => []) // 重試
    s.setBindings(bindings(load))
    await s.pin(0, 'w1')
    s.setFocus(0)
    s.sessions['w1'].busy = true // 前置
    const before = s.views['w1'].timeline.map(i => i.env.event_id)

    await s.loadOlder('w1')

    expect(s.views['w1'].timeline.map(i => i.env.event_id)).toEqual(before)
    expect(s.sessions['w1'].busy).toBe(true)
    expect(s.notices.at(-1)!.env.error).toContain('older boom')

    await s.loadOlder('w1') // 重試
    expect(load).toHaveBeenCalledTimes(3)
  })

  // (7) 同一條路徑的 latch 變體。
  it('reject 且錯誤含 REGISTRY_UNCERTAIN_MARK → latchSeq 增', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    const load = vi.fn()
      .mockImplementationOnce(async () => hist('w1'))
      .mockImplementationOnce(async () => { throw new Error(`wsregistry: ${MARK}`) })
    s.setBindings(bindings(load))
    await s.pin(0, 'w1')
    const before = s.latchSeq

    await s.loadOlder('w1')

    expect(s.latchSeq).toBe(before + 1)
  })

  // (8) 反向：成功路徑不受影響——既有 PaneView.test.ts 的 pin／loadOlder 行為
  // 不得被本次攔截邏輯破壞。這條在實作前就該是綠的。
  it('成功路徑無 notice，既有行為不變', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.setBindings(bindings(vi.fn(async () => hist('w1'))))

    await s.pin(0, 'w1')

    expect(s.notices).toHaveLength(0)
    expect(s.views['w1'].timeline.length).toBeGreaterThan(0)
  })
})

// (9) i18n placeholder 守門（gate P2）：locales.parity 只比 key 路徑、不查
// placeholder——zh-TW 或 en 任一邊漏掉 {wsid}／{error} 都不會被那條測試攔到。
// 這裡各自用該語系建一個獨立 i18n 實例直接 t()，不透過 store 的全域 `t`
// （固定 zh-TW），確保兩語系都被驗到。
describe('i18n placeholder 守門：store.turnsLoadFailed／store.olderTurnsLoadFailed', () => {
  it.each([
    ['zh-TW', zhTW],
    ['en', en],
  ] as const)('%s：兩個 key 的產出都同時含 wsid 與 error', (locale, messages) => {
    const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages } })
    const t = i18n.global.t

    const a = t('store.turnsLoadFailed', { wsid: 'w1', error: 'x' })
    expect(a).toContain('w1')
    expect(a).toContain('x')

    const b = t('store.olderTurnsLoadFailed', { wsid: 'w1', error: 'x' })
    expect(b).toContain('w1')
    expect(b).toContain('x')
  })
})
