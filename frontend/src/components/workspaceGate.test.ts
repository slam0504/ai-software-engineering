import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import PaneView from './PaneView.vue'
import SpecWorkspace from './SpecWorkspace.vue'
import PlanWorkspace from './PlanWorkspace.vue'
import { useSession } from '../stores/session'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { mountWithI18n } from '../test/i18n'
import type { Bindings } from '../types'

// ---- B4 R5／§5.6：送出訊息與 Spec／Plan Assist 的停用條件 ----
// 三種封鎖都必須停用：required && !confirmed（pending）、尚未取得 status（loading）、
// 查詢失敗（failed）。放行：不需確認、或已確認。

const mocks = vi.hoisted(() => ({
  SpecAssist: vi.fn(), SpecList: vi.fn(), SpecRead: vi.fn(), SpecWrite: vi.fn(),
  SubmitForApproval: vi.fn(), PreviewSpecCommit: vi.fn(), ConfirmSpecCommit: vi.fn(),
  PlanAssist: vi.fn(), PlanList: vi.fn(), PlanRead: vi.fn(), PlanWrite: vi.fn(),
  PreviewPlanCommit: vi.fn(), ConfirmPlanCommit: vi.fn(), SubmitPlanForApproval: vi.fn(),
  PreviewAnalysisBaseBump: vi.fn(), ConfirmAnalysisBaseBump: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

type Mode = 'pending' | 'loading' | 'failed' | 'confirmed' | 'notRequired'

async function putIntoMode(mode: Mode) {
  const wsc = useWorkspaceConfirmation()
  const base = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home'] }
  if (mode === 'pending') await wsc.refresh(async () => base)
  if (mode === 'failed') await wsc.refresh(async () => { throw new Error('down') })
  if (mode === 'confirmed') await wsc.refresh(async () => ({ ...base, confirmed: true }))
  if (mode === 'notRequired') await wsc.refresh(async () => ({ ...base, required: false }))
  // loading：什麼都不做（status 為 null、沒有錯誤）
  return wsc
}

const BLOCKED: Mode[] = ['pending', 'loading', 'failed']
const OPEN: Mode[] = ['confirmed', 'notRequired']

beforeAll(() => { Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {}) })

describe('composer（PaneView）與 session.submit', () => {
  let bindings: Bindings
  beforeEach(() => {
    setActivePinia(createPinia())
    bindings = { StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) }
    const s = useSession()
    s.setBindings(bindings)
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.pin(0, 'w1')
    s.setFocus(0)
  })

  it.each(BLOCKED)('%s：composer 的輸入框與送出鈕停用，並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    expect(w.find('[data-test=composer-textarea]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=composer-send]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=composer-blocked]').exists()).toBe(true)
  })

  it.each(OPEN)('%s：composer 不停用、沒有封鎖提示', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await w.find('[data-test=composer-textarea]').setValue('hi')
    expect(w.find('[data-test=composer-textarea]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=composer-send]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=composer-blocked]').exists()).toBe(false)
  })

  it.each(BLOCKED)('%s：s.submit 不呼叫 StartSession／SendMessage，推一則 notice', async (mode) => {
    await putIntoMode(mode)
    const s = useSession()
    const errorsBefore = s.errorSeq
    await s.submit('go')
    expect(bindings.StartSession).not.toHaveBeenCalled()
    expect(bindings.SendMessage).not.toHaveBeenCalled()
    expect(s.errorSeq).toBe(errorsBefore + 1)
    expect(s.sessions['w1'].busy).toBe(false)
  })

  // disabled 的 textarea／button 收不到使用者輸入與事件，所以：先在放行狀態下輸入草稿，再把 store
  // 翻成封鎖，然後直接呼叫元件的 send()（Enter 與送出鈕共用的函式）。「被擋下的草稿要留在
  // 輸入框」靠的就是 send() 在清空 draft 之前先檢查封鎖。
  it('輸入草稿後才被封鎖：send() 不會清掉草稿，也不會送出', async () => {
    const wsc = await putIntoMode('confirmed')
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    const ta = w.find('[data-test=composer-textarea]')
    await ta.setValue('keep me')
    await wsc.refresh(async () => { throw new Error('status query down') }) // 之後查詢失敗 → 封鎖
    expect(wsc.blocked).toBe(true)
    await (w.vm as unknown as { send: () => Promise<void> }).send()
    await flushPromises()
    expect((ta.element as HTMLTextAreaElement).value).toBe('keep me')
    expect(bindings.StartSession).not.toHaveBeenCalled()
  })

  it('解除封鎖後可正常送出（第一輪走 StartSession）', async () => {
    await putIntoMode('confirmed')
    await useSession().submit('go')
    expect(bindings.StartSession).toHaveBeenCalledTimes(1)
  })
})

// CodeMirror 首次建構的一次性成本（沿用 SpecWorkspace.test.ts／PlanWorkspace.test.ts 的預熱）。
beforeAll(async () => {
  const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
    import('codemirror'), import('@codemirror/state'),
  ])
  const host = document.createElement('div')
  document.body.appendChild(host)
  new EditorView({ state: EditorState.create({ doc: '', extensions: [basicSetup] }), parent: host }).destroy()
  host.remove()
}, 30_000)

describe('Spec／Plan Assist 觸發點', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
    mocks.SpecAssist.mockResolvedValue('corr-1')
    mocks.PlanAssist.mockResolvedValue('corr-1')
  })

  const SPEC_BUTTONS = ['assist-draft', 'assist-ambiguity', 'assist-oracle']

  it.each(BLOCKED)('SpecWorkspace %s：三個 AI 輔助按鈕都停用並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    for (const id of SPEC_BUTTONS) expect(w.find(`[data-test=${id}]`).attributes('disabled'), id).toBeDefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(true)
    // disabled 的按鈕收不到 click；runAssist 本身的守門（深度防禦）要直接呼叫才量得到。
    await (w.vm as unknown as { runAssist: (p: string) => Promise<void> }).runAssist('prompt')
    await flushPromises()
    expect(mocks.SpecAssist).not.toHaveBeenCalled()
    expect(w.find('.err').exists()).toBe(true)
  })

  it.each(OPEN)('SpecWorkspace %s：按鈕可用且會呼叫 SpecAssist', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(false)
    await w.find('[data-test=assist-draft]').trigger('click')
    await flushPromises()
    expect(mocks.SpecAssist).toHaveBeenCalledTimes(1)
  })

  it.each(BLOCKED)('PlanWorkspace %s：生成草稿按鈕停用並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(w.find('[data-test=generate-draft]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(true)
    await (w.vm as unknown as { runAssist: () => Promise<void> }).runAssist() // bump 的重新執行也走這裡
    await flushPromises()
    expect(mocks.PlanAssist).not.toHaveBeenCalled()
    expect(w.find('.err').exists()).toBe(true)
  })

  it.each(OPEN)('PlanWorkspace %s：生成草稿會呼叫 PlanAssist', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(w.find('[data-test=generate-draft]').attributes('disabled')).toBeUndefined()
    await w.find('[data-test=generate-draft]').trigger('click')
    await flushPromises()
    expect(mocks.PlanAssist).toHaveBeenCalledTimes(1)
  })

  it('解除封鎖後同一個元件的按鈕立即恢復（響應式，不需重新掛載）', async () => {
    const wsc = await putIntoMode('pending')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeDefined()
    await wsc.refresh(async () => ({ required: true, confirmed: true, workspace: '/Users/me', source: 'home', reasons: [] }))
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeUndefined()
  })
})
