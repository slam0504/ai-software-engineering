import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { useWorkspaceConfirmation } from './stores/workspaceConfirmation'

// ---- B4：App.vue 的接線（spec §5.6 資料取得順序、R5 Invariant）----
// 抓 App.vue 自己註冊的 EventsOn handler 觸發 refresh（同 cliInfoReady.test.ts：不重寫
// production 邏輯）。

const runtimeMocks = vi.hoisted(() => {
  const handlers: Record<string, (d?: any) => void> = {}
  return {
    handlers,
    EventsOn: (name: string, cb: (d?: any) => void) => { handlers[name] = cb; return vi.fn() },
  }
})
vi.mock('../wailsjs/runtime/runtime', () => runtimeMocks)

const appMocks = vi.hoisted(() => ({
  CLIInfo: vi.fn(async () => ({}) as Record<string, string>),
  WorkspaceConfirmationStatus: vi.fn(),
  ConfirmWorkspace: vi.fn(async (_p: string) => {}),
  GateDecide: vi.fn(), GateDecisionContext: vi.fn(),
  SubmitForApproval: vi.fn(), SubmitPlanForApproval: vi.fn(), PlanAssist: vi.fn(),
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

const READY = { ready: 'true', workspace: '/Users/me', workspaceSource: 'home', startupError: '' }
const PENDING = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home', 'is_home'] }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function mountApp() {
  const pinia = createPinia()
  setActivePinia(pinia)
  // 警示元件保持真實（shallowMount 預設會 stub 掉子元件）。
  return shallowMount(App, { global: { plugins: [pinia, makeI18n()], stubs: { WorkspaceConfirmBanner: false } } })
}

beforeEach(() => {
  for (const k of Object.keys(runtimeMocks.handlers)) delete runtimeMocks.handlers[k]
  for (const fn of Object.values(appMocks)) (fn as any).mockClear?.()
  appMocks.CLIInfo.mockImplementation(async () => READY)
  appMocks.WorkspaceConfirmationStatus.mockReset()
  appMocks.WorkspaceConfirmationStatus.mockImplementation(async () => PENDING)
  appMocks.ConfirmWorkspace.mockReset()
  appMocks.ConfirmWorkspace.mockImplementation(async () => {})
})

describe('App：workspace 確認狀態的取得順序', () => {
  it('CLIInfo 尚未 ready 時不查詢（維持封鎖）；cli-ready 事件之後才查', async () => {
    appMocks.CLIInfo.mockImplementationOnce(async () => ({ ready: 'false' }))
    const w = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).not.toHaveBeenCalled()
    expect(useWorkspaceConfirmation().blockKind).toBe('loading')
    expect(w.find('[data-test=ws-loading]').exists()).toBe(true)

    appMocks.CLIInfo.mockImplementationOnce(async () => READY)
    runtimeMocks.handlers['workbench:cli-ready']!()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(1)
    expect(useWorkspaceConfirmation().blockKind).toBe('pending')
    expect(w.find('[role="alert"][data-test=ws-alert]').exists()).toBe(true)
  })

  it('晚連線（mount 時已 ready）：首查之後立刻查 status', async () => {
    const w = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(1)
    expect(w.find('[data-test=ws-path]').text()).toBe('/Users/me')
  })

  it('status 查詢失敗：維持封鎖並顯示警示（不放行）', async () => {
    appMocks.WorkspaceConfirmationStatus.mockImplementation(async () => { throw new Error('app 尚未完成啟動') })
    const w = mountApp()
    await flushPromises()
    expect(useWorkspaceConfirmation().blocked).toBe(true)
    expect(w.find('[data-test=ws-query-failed]').text()).toContain('app 尚未完成啟動')
  })
})

describe('App：晚到的舊回應、確認流程與重新掛載', () => {
  it('較舊的 status 回覆較晚抵達：不得覆蓋較新的（已確認）狀態', async () => {
    const first = deferred<unknown>()
    appMocks.WorkspaceConfirmationStatus.mockImplementationOnce(() => first.promise as Promise<any>)
    const w = mountApp()
    await flushPromises() // 首查（已 ready）→ 第一次 status 查詢卡住

    appMocks.WorkspaceConfirmationStatus.mockImplementationOnce(async () => ({ ...PENDING, confirmed: true }))
    runtimeMocks.handlers['workbench:cli-ready']!() // 第二次 refresh → 第二次 status 查詢
    await flushPromises()
    expect(useWorkspaceConfirmation().blockKind).toBe('ok')

    first.resolve(PENDING) // 舊回應（確認之前查到的）較晚抵達
    await flushPromises()
    expect(useWorkspaceConfirmation().blockKind).toBe('ok')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('按下確認：以 status.workspace 原樣呼叫 ConfirmWorkspace，成功後重新查詢並解除警示', async () => {
    appMocks.WorkspaceConfirmationStatus
      .mockImplementationOnce(async () => PENDING)
      .mockImplementationOnce(async () => ({ ...PENDING, confirmed: true }))
    const w = mountApp()
    await flushPromises()
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(appMocks.ConfirmWorkspace).toHaveBeenCalledTimes(1)
    expect(appMocks.ConfirmWorkspace).toHaveBeenCalledWith('/Users/me')
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(2)
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('元件重新掛載不會自動確認：每次掛載只查詢，ConfirmWorkspace 從未被呼叫', async () => {
    const first = mountApp()
    await flushPromises()
    first.unmount()
    const second = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(2)
    expect(appMocks.ConfirmWorkspace).not.toHaveBeenCalled()
    expect(second.find('[data-test=ws-alert]').exists()).toBe(true)
  })

  it('頂部列的 ws: 顯示維持不變', async () => {
    const w = mountApp()
    await flushPromises()
    expect(w.find('.meta').text()).toContain('ws: home @ /Users/me')
  })
})
