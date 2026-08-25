import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

// ---- CLIInfo ready 契約的 frontend 格（spec §3／§4）----
// docs/superpowers/specs/2026-08-25-cliinfo-late-connect-design.md
//
// mount 真的 App.vue、抓它自己註冊的 EventsOn('workbench:cli-ready') handler 來
// 觸發 refresh——不重寫 production 邏輯（registryUncertain.test.ts C1 的教訓：
// 自己重現分流會守不到 production 那一行）。
//
// 契約兩格：早連線（首查早於 backend 定案，靠事件補讀）與晚連線（mount 時已
// ready，首查直接拿定案值、不依賴事件）；並行回覆以 request sequencing 收斂
// （最新 request 優先——ready=true 的回覆之間也可能不同，見 spec §2）。

// callOrder：跨兩個 mock module 的呼叫順序（訂閱必須先於首查）。
const callOrder = vi.hoisted(() => [] as string[])

const runtimeMocks = vi.hoisted(() => {
  const handlers: Record<string, (d?: any) => void> = {}
  const disposers: Record<string, ReturnType<typeof vi.fn>> = {}
  return {
    handlers,
    disposers,
    EventsOn: (name: string, cb: (d?: any) => void) => {
      handlers[name] = cb
      callOrder.push(`EventsOn:${name}`)
      const dispose = vi.fn()
      disposers[name] = dispose
      return dispose
    },
  }
})
vi.mock('../wailsjs/runtime/runtime', () => runtimeMocks)

const appMocks = vi.hoisted(() => ({
  CLIInfo: vi.fn(async () => ({}) as Record<string, string>),
  GateDecide: vi.fn(), GateDecisionContext: vi.fn(),
  GateList: vi.fn(async () => [] as any[]),
  ListSessions: vi.fn(async () => [] as any[]),
  SpecList: vi.fn(async () => [] as any[]),
  StartSession: vi.fn(), SendMessage: vi.fn(), EndSession: vi.fn(), NewSession: vi.fn(),
  TerminateSession: vi.fn(), CreateSession: vi.fn(), RemoveSession: vi.fn(),
  RecoverCodexRecording: vi.fn(), LoadTurnsBefore: vi.fn(),
  PaneLayout: vi.fn(async () => ({ pins: ['', ''], focused: '' })),
  SetPaneLayout: vi.fn(async () => undefined),
  RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
  SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
  EscalationList: vi.fn(async () => [] as any[]), EscalationCreate: vi.fn(),
  EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => appMocks)

import App from './App.vue'
import { makeI18n } from './test/i18n'

// FINAL：backend 定案後的快照。node 值取一個不會出現在空快照裡的識別字串，
// meta 列渲染與否可以直接用它斷言。
const FINAL: Record<string, string> = {
  ready: 'true', toolsDir: '/t', toolsSource: 'bundled',
  node: 'v99-final', workspace: '/w', workspaceSource: 'flag', startupError: '',
}

function deferred() {
  let resolve!: (v: Record<string, string>) => void
  const promise = new Promise<Record<string, string>>((r) => { resolve = r })
  return { promise, resolve }
}

// cliQueue：CLIInfo 逐次回覆的 thunk 佇列；空了就回空物件（其餘 binding 的
// 呼叫不在本檔視野）。
let cliQueue: Array<() => Promise<Record<string, string>>> = []

function mountApp() {
  const pinia = createPinia()
  setActivePinia(pinia)
  return shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
}

beforeEach(() => {
  callOrder.length = 0
  for (const k of Object.keys(runtimeMocks.handlers)) delete runtimeMocks.handlers[k]
  for (const k of Object.keys(runtimeMocks.disposers)) delete runtimeMocks.disposers[k]
  for (const fn of Object.values(appMocks)) (fn as any).mockClear?.()
  cliQueue = []
  appMocks.CLIInfo.mockImplementation(() => {
    callOrder.push('CLIInfo')
    const next = cliQueue.shift()
    return next ? next() : Promise.resolve({})
  })
})

describe('CLIInfo ready 契約（頂列 meta 晚連線空白）', () => {
  // mutation：拿掉訂閱、或訂閱排在查詢之後 → 事件到不了 refresh，此測紅。
  it('早連線格：首查 ready=false 空值 → cli-ready 事件 → refresh 拿到定案值', async () => {
    cliQueue.push(async () => ({ ready: 'false' }))
    const w = mountApp()
    await flushPromises()
    expect(w.find('.meta').text()).not.toContain('v99-final')

    const h = runtimeMocks.handlers['workbench:cli-ready']
    expect(h, 'App.vue 必須訂閱 workbench:cli-ready').toBeTruthy()
    cliQueue.push(async () => FINAL)
    h!()
    await flushPromises()
    expect(w.find('.meta').text()).toContain('v99-final')
  })

  it('晚連線格：首查即 ready=true 定案值，顯示正確且不需事件', async () => {
    cliQueue.push(async () => FINAL)
    const w = mountApp()
    await flushPromises()
    expect(w.find('.meta').text()).toContain('v99-final')
  })

  it('訂閱先於首查（spec §3 順序：事件不可能落在「查完之後、訂閱之前」）', async () => {
    mountApp()
    await flushPromises()
    const sub = callOrder.indexOf('EventsOn:workbench:cli-ready')
    const firstQuery = callOrder.indexOf('CLIInfo')
    expect(sub, '必須訂閱 workbench:cli-ready').toBeGreaterThanOrEqual(0)
    expect(firstQuery, '必須有首查').toBeGreaterThanOrEqual(0)
    expect(sub).toBeLessThan(firstQuery)
  })

  // mutation：拿掉 request sequencing → 較慢的首查覆蓋定案值，此測紅。
  it('逆序完成格（false→true）：較慢的首查不得覆蓋事件查詢的定案值', async () => {
    const first = deferred()
    cliQueue.push(() => first.promise)
    const w = mountApp()
    await flushPromises()
    const h = runtimeMocks.handlers['workbench:cli-ready']
    expect(h, 'App.vue 必須訂閱 workbench:cli-ready').toBeTruthy()

    const second = deferred()
    cliQueue.push(() => second.promise)
    h!()
    second.resolve(FINAL)
    await flushPromises()
    expect(w.find('.meta').text()).toContain('v99-final')

    first.resolve({ ready: 'false', node: 'stale-empty' })
    await flushPromises()
    expect(w.find('.meta').text()).toContain('v99-final')
    expect(w.find('.meta').text()).not.toContain('stale-empty')
  })

  // mutation：sequencing 退化成「只丟棄 ready!=true」的單調規則 → 較舊的
  // ready=true 回覆照樣覆蓋，此測紅——這一格是 spec rev2 規則的反例。
  it('逆序完成格（true→true）：兩個 ready=true 回覆不同時，最新 request 優先', async () => {
    const first = deferred()
    cliQueue.push(() => first.promise)
    const w = mountApp()
    await flushPromises()
    const h = runtimeMocks.handlers['workbench:cli-ready']
    expect(h, 'App.vue 必須訂閱 workbench:cli-ready').toBeTruthy()

    const second = deferred()
    cliQueue.push(() => second.promise)
    h!()
    // 事件查詢先完成：帶著 ready 後才追加的 startupError（spec §2 的兩條
    // fail-loud 路徑之一）。
    second.resolve({ ...FINAL, startupError: 'audit invariant broken' })
    await flushPromises()
    expect(w.find('.meta').text()).toContain('audit invariant broken')

    // 較舊的首查較晚完成：同樣 ready=true，但沒有那則橫幅。不得覆蓋。
    first.resolve({ ...FINAL })
    await flushPromises()
    expect(w.find('.meta').text()).toContain('audit invariant broken')
  })

  // spec §3 當時把既有 listener 標為待清理、另票處理；該票已執行——App.vue 的
  // 三個 Wails listener 在 unmount 時都必須清（不清的 handler 會在 HMR／重掛
  // 後對著舊的 store instance 繼續收事件）。
  it('清理格：unmount 後三個 Wails listener 的 disposer 都被呼叫', async () => {
    const w = mountApp()
    await flushPromises()
    const names = ['workbench:event', 'session:done', 'workbench:cli-ready']
    for (const name of names) {
      expect(runtimeMocks.disposers[name], `App.vue 必須訂閱 ${name}`).toBeTruthy()
      expect(runtimeMocks.disposers[name]!).not.toHaveBeenCalled()
    }
    w.unmount()
    for (const name of names) {
      expect(runtimeMocks.disposers[name]!, `${name} 的 disposer 必須在 unmount 時被呼叫`)
        .toHaveBeenCalledTimes(1)
    }
  })
})
