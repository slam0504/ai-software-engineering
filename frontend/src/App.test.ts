import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'

// App.vue smoke test（Task 28 review round 1）——reviewer 抓到的真缺口：
// SessionList（Task 27）與 DualPane（Task 28）是否真的掛進 App.vue 沒有任何
// 測試守門，之前完全靠人工走查。這裡只做 shallow 層級：兩個 wailsjs module 全
// mock（App.vue 的 onMounted 會直接呼叫 EventsOn／ListSessions／CLIInfo／
// GateList／SpecList，未 mock 會在 jsdom 撞 `window.go`/`window.runtime`
// undefined），子元件一律 shallow stub，只斷言「掛沒掛上去」，不驗內部行為
// （那是各元件自己的測試檔的事）。
const wailsAppMocks = vi.hoisted(() => ({
  CLIInfo: vi.fn(async () => ({})),
  GateDecide: vi.fn(), GateDecisionContext: vi.fn(),
  GateList: vi.fn(async () => []),
  ListSessions: vi.fn(async () => []),
  SpecList: vi.fn(async () => []),
  StartSession: vi.fn(), SendMessage: vi.fn(), EndSession: vi.fn(), NewSession: vi.fn(),
  TerminateSession: vi.fn(), CreateSession: vi.fn(), RemoveSession: vi.fn(),
  RecoverCodexRecording: vi.fn(), LoadTurnsBefore: vi.fn(),
  // PaneLayout／SetPaneLayout：pane pins 持久化（owner review 修正 3）。
  // **少了它們這個檔仍會綠，但綠得沒有意義**——App.vue 的 onMounted 會撞
  // 「No "PaneLayout" export is defined on the mock」，被它自己的 catch 接住、
  // 塞一筆 notice，還原路徑在這個檔從沒被執行過。本檔沒有錯誤計數斷言，所以
  // 那條失敗分支是靜默的（失效形狀 (E)：測試環境本身量不出那個效果）。
  //
  // 通則：**任何 mock 這個 module 的測試檔，都有跟著 production binding 一起
  // 補的義務**；目前靠人工，沒有機制守（見 pane-pins-report.md §6 的流程缺口）。
  PaneLayout: vi.fn(async () => ({ pins: ['', ''], focused: '' })),
  SetPaneLayout: vi.fn(async () => undefined),
  RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
  SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
  EscalationList: vi.fn(async () => []), EscalationCreate: vi.fn(), EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => wailsAppMocks)
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn() }))

import App from './App.vue'
import DualPane from './components/DualPane.vue'
import SessionList from './components/SessionList.vue'
import SpecWorkspace from './components/SpecWorkspace.vue'
import PlanWorkspace from './components/PlanWorkspace.vue'
import GateConsole from './components/GateConsole.vue'
import PreviewPane from './components/PreviewPane.vue'
import FileTree from './components/FileTree.vue'
import { makeI18n } from './test/i18n'

describe('App shell 接線（Task 28 review round 1：SessionList／DualPane 真的掛上去了嗎）', () => {
  it('左欄 .side-sessions 內有 SessionList，chat tab（預設）下有 DualPane', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const i18n = makeI18n()
    const w = shallowMount(App, { global: { plugins: [pinia, i18n] } })
    await flushPromises()
    expect(w.find('.side-sessions').findComponent(SessionList).exists()).toBe(true)
    expect(w.findComponent(DualPane).exists()).toBe(true)
  })

  // Task 2a rev2（矩陣 A 的啟動路徑那幾列）：registry 的 Open／Migrate／
  // BackfillResume 落盤失敗（含 dir-sync 失敗的 uncertain latch）發生在 UI 還沒
  // 開之前，**沒有任何 binding 呼叫端可以回錯**——唯一的使用者出口是
  // CLIInfo().startupError 渲染成的啟動列紅字。這條守它真的渲染得出來，不是
  // 只有 backend 把字串塞進 a.startupErr（失效形狀 (C)）。
  //
  // mutation：拿掉 App.vue `.meta` 裡的 `<span v-if="cliInfo.startupError">`
  // → 紅在「啟動失敗必須渲染」。
  it('startupError 渲染成啟動列紅字（registry 落盤失敗在啟動路徑的唯一出口）', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    wailsAppMocks.CLIInfo.mockResolvedValueOnce({
      startupError: 'session registry load failed: 檔案：/x/workspace-sessions.json 請先備份該檔',
    })
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const err = w.find('.meta .err')
    expect(err.exists(), '啟動失敗必須渲染').toBe(true)
    expect(err.text()).toContain('session registry load failed')
    expect(err.text()).toContain('/x/workspace-sessions.json')
  })
})

// A1a-1 缺口 2 修正：onGoResubmit／FileTree select handler 原本直接改 tab／
// planFocusPath／selectedFile，沒有經過 workspaceBusy 檢查——SpecWorkspace／
// PlanWorkspace 儲存中若被切走，會把進行中的寫入連同元件一起卸載（v-if）。
// 這裡在 App.vue 層級證明：busy 時兩個入口都被攔截、且不留下部分狀態變更；
// 非 busy 時兩者仍正常運作（避免只證明「永遠拒絕」）。
describe('App 寫入安全攔截（A1a-1 缺口 2）', () => {
  it('workspaceBusy 時 go-resubmit 不改變 tab／planFocusPath；解除後正常導航', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()

    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    expect(planTabBtn, '找不到 plan tab 按鈕').toBeTruthy()
    await planTabBtn!.trigger('click') // 切到 plan tab（此時非 busy，允許）
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).props('path')).toBeUndefined() // planFocusPath 初始未設

    w.findComponent(PlanWorkspace).vm.$emit('busy', true) // 工作區回報 busy（例如儲存中）
    await flushPromises()

    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate2', subject: 'plan:P9' })
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).props('path')).toBeUndefined() // planFocusPath 未被改動
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false) // tab 未被切走

    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate1', subject: '' })
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false) // busy 時 gate1 目標也不導航到 spec

    w.findComponent(PlanWorkspace).vm.$emit('busy', false) // 解除 busy
    await flushPromises()
    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate2', subject: 'plan:P9' })
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).props('path')).toBe('plan/P9.yaml') // 非 busy 時正常導航
  })

  it('workspaceBusy 時 FileTree select 不改變 selectedFile／tab；解除後正常運作', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()

    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click') // 切到 plan tab（非 busy，允許）
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(PreviewPane).props('path')).toBe('') // selectedFile 初始為空字串

    w.findComponent(PlanWorkspace).vm.$emit('busy', true) // 工作區回報 busy
    await flushPromises()

    w.findComponent(FileTree).vm.$emit('select', 'spec/a.feature')
    await flushPromises()
    expect(w.findComponent(PreviewPane).props('path')).toBe('') // selectedFile 未被改動
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true) // tab 未被切到 preview（PlanWorkspace 仍掛著）

    w.findComponent(PlanWorkspace).vm.$emit('busy', false) // 解除 busy
    await flushPromises()
    w.findComponent(FileTree).vm.$emit('select', 'spec/a.feature')
    await flushPromises()
    expect(w.findComponent(PreviewPane).props('path')).toBe('spec/a.feature') // 非 busy 時正常選檔
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false) // tab 已切到 preview
  })
})

// A1a-2 expected-red 階段（App 層級切檔守衛）：SpecWorkspace／PlanWorkspace 目前
// 只把 busy（寫入中）往上 emit（見上面 @busy="workspaceBusy = $event"），沒有
// 「dirty（未儲存變更）」訊號；App.vue 的三個導航入口（tab 按鈕 switchTab／
// FileTree @select selectPreviewFile／GateConsole·EscalationInbox @go-resubmit
// onGoResubmit）也都還沒有任何 dirty 感知——外部 dirty 時導航會直接生效，不會
// 出現 keep／discard 選擇。
//
// 假設接口（不是已確認的 production 設計，只是「測試怎麼寫」的假設，供未來
// A1a-2 實作者參考／推翻）：假設 SpecWorkspace／PlanWorkspace 會新增一個
// `@dirty="workspaceDirty = $event"` emit（對稱於既有的 `@busy`），且 App.vue
// 會在自己的模板內（不是被 shallow stub 掉的子元件內）渲染
// `[data-test=unsaved-guard]`／`[data-test=unsaved-keep]`／
// `[data-test=unsaved-discard]`。下面用 `.vm.$emit('dirty', true)` 模擬這個
// 尚未存在的訊號，真正的 A1a-2 實作出來後，這裡的模擬方式與 DOM 斷言都可能
// 需要跟著調整——不是紅燈本身的問題。
describe('App 切檔守衛（A1a-2，expected-red，App 層級）', () => {
  function mustFind(w: VueWrapper<any>, selector: string) {
    const el = w.find(selector)
    expect(el.exists(), `找不到 ${selector}——尚未實作（expected-red）`).toBe(true)
    return el
  }

  async function mountOnSpecDirty() {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    expect(specTabBtn, '找不到規格 tab 按鈕').toBeTruthy()
    await specTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    w.findComponent(SpecWorkspace).vm.$emit('dirty', true) // 假設接口，見上方 describe 註解
    await flushPromises()
    return w
  }

  async function mountOnPlanDirty() {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    expect(planTabBtn, '找不到計畫 tab 按鈕').toBeTruthy()
    await planTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    w.findComponent(PlanWorkspace).vm.$emit('dirty', true) // 假設接口，見上方 describe 註解
    await flushPromises()
    return w
  }

  it('G4-S-tab：Spec dirty 時點另一個 tab 按鈕→守衛出現、Spec 未被卸載、tab 未切走；keep 後維持原狀', async () => {
    const w = await mountOnSpecDirty()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click') // 嘗試切到 plan tab
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true) // 未被 v-if 卸載（未儲存狀態不能隨 unmount 消失）
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false) // tab 未切走

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G4-S-filetree：Spec dirty 時 FileTree 選檔→守衛出現、selectedFile／tab 未變；keep 後維持原狀', async () => {
    const w = await mountOnSpecDirty()
    w.findComponent(FileTree).vm.$emit('select', 'spec/other.feature')
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(PreviewPane).props('path')).toBe('') // selectedFile 未被改動
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true) // tab 未被切到 preview

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(PreviewPane).props('path')).toBe('')
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
  })

  it('G4-S-goresubmit：Spec dirty 時 go-resubmit(gate2)→守衛出現、tab 未變（未切到 plan）；keep 後維持原狀', async () => {
    const w = await mountOnSpecDirty()
    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate2', subject: 'plan:P9' })
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true) // tab 仍在 spec
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G4-P-tab：Plan dirty 時點另一個 tab 按鈕→守衛出現、Plan 未被卸載、tab 未切走；keep 後維持原狀', async () => {
    const w = await mountOnPlanDirty()
    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click') // 嘗試切到 spec tab
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
  })

  it('G4-P-filetree：Plan dirty 時 FileTree 選檔→守衛出現、selectedFile／tab 未變；keep 後維持原狀', async () => {
    const w = await mountOnPlanDirty()
    w.findComponent(FileTree).vm.$emit('select', 'spec/other.feature')
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(PreviewPane).props('path')).toBe('')
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(PreviewPane).props('path')).toBe('')
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
  })

  it('G4-P-goresubmit：Plan dirty 時 go-resubmit(gate1)→守衛出現、tab 未變（未切到 spec）；keep 後維持原狀', async () => {
    const w = await mountOnPlanDirty()
    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate1', subject: '' })
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true) // tab 仍在 plan
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
  })

  it('G5-S-tab：G4-S-tab 情境下點 discard→導航真正完成，切到 plan tab', async () => {
    const w = await mountOnSpecDirty()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(PlanWorkspace).exists()).toBe(true) // 導航完成：切到 plan tab
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
  })

  it('G5-S-filetree：G4-S-filetree 情境下點 discard→導航真正完成，selectedFile 設定且切到 preview', async () => {
    const w = await mountOnSpecDirty()
    w.findComponent(FileTree).vm.$emit('select', 'spec/other.feature')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(PreviewPane).props('path')).toBe('spec/other.feature')
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false) // tab 已切到 preview
  })

  it('G5-S-goresubmit：G4-S-goresubmit 情境下點 discard→導航真正完成，切到 plan tab 且 planFocusPath 設定', async () => {
    const w = await mountOnSpecDirty()
    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate2', subject: 'plan:P9' })
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).props('path')).toBe('plan/P9.yaml') // planFocusPath 已設定
  })

  it('G5-P-tab：G4-P-tab 情境下點 discard→導航真正完成，切到 spec tab', async () => {
    const w = await mountOnPlanDirty()
    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G5-P-filetree：G4-P-filetree 情境下點 discard→導航真正完成，selectedFile 設定且切到 preview', async () => {
    const w = await mountOnPlanDirty()
    w.findComponent(FileTree).vm.$emit('select', 'spec/other.feature')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(PreviewPane).props('path')).toBe('spec/other.feature')
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G5-P-goresubmit：G4-P-goresubmit 情境下點 discard→導航真正完成，切到 spec tab', async () => {
    const w = await mountOnPlanDirty()
    w.findComponent(GateConsole).vm.$emit('go-resubmit', { gate: 'gate1', subject: '' })
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()

    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  // G6-*-switchtab：owner 裁定（round 2）——G6 在 App 層也要驗「切分頁」入口，
  // 確認 A1a-1 既有的 workspaceBusy 攔截（switchTab 內 `if (workspaceBusy.value)
  // return`）不會被新的 dirty-guard 流程繞過。沿用既有的
  // `.vm.$emit('busy', true)` 模擬手法（A1a-1 既有機制，不是為本次新發明的
  // 介面）——不模擬 dirty，因為 G6 要證明的正是「busy 直接擋下，dirty-guard
  // 流程完全不介入」。App 層的 workspaceBusy 是單一 boolean，不分辨底層
  // busyReason 是 save／accept／bump（那個區分只存在於各工作區元件內部）；
  // save／accept 兩條、save／bump 兩條在 App 層機制完全相同，這裡仍依 owner
  // 要求各自命名一條，對應到元件層測過的三種底層狀態。四條都預期綠：不是
  // 「守衛缺元素」的紅燈缺口，而是攔截已經先擋下，dirty-guard 邏輯根本沒機會
  // 執行——綠得有意義，不是誤判。
  it('G6-S-save-switchtab：App 層——SpecWorkspace 回報 busy（對應 save 等待）時嘗試切分頁，被 A1a-1 既有機制直接擋下（busy＋dirty 同時成立；precedence 已實作，本條預期綠）', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)

    // busy 與 dirty **同時**成立才能證明優先順序：若實作先看 dirty 再看 busy，
    // 沒有 dirty 訊號的版本一樣會通過，這條就證明不了 A1a-1 優先（owner 2026-09-07）。
    w.findComponent(SpecWorkspace).vm.$emit('dirty', true)
    w.findComponent(SpecWorkspace).vm.$emit('busy', true) // 對應 SpecWorkspace busyReason='save'
    await flushPromises()

    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click') // busy 時嘗試切分頁離開 spec
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false) // A1a-1 既有 workspaceBusy 攔截直接擋下
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true) // tab 未被切走
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G6-S-accept-switchtab：App 層——SpecWorkspace 回報 busy（對應 accept 等待）時嘗試切分頁，被 A1a-1 既有機制直接擋下（busy＋dirty 同時成立；precedence 已實作，本條預期綠）', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)

    w.findComponent(SpecWorkspace).vm.$emit('dirty', true) // busy＋dirty 同時成立，才驗得出 A1a-1 優先於 A1a-2 守衛
    w.findComponent(SpecWorkspace).vm.$emit('busy', true) // 對應 SpecWorkspace busyReason='accept'（App 層機制與 save 案例相同，見上方 describe 註解）
    await flushPromises()

    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click')
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false)
    expect(w.findComponent(SpecWorkspace).exists()).toBe(true)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
  })

  it('G6-P-save-switchtab：App 層——PlanWorkspace 回報 busy（對應 save 等待）時嘗試切分頁，被 A1a-1 既有機制直接擋下（busy＋dirty 同時成立；precedence 已實作，本條預期綠）', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)

    w.findComponent(PlanWorkspace).vm.$emit('dirty', true) // busy＋dirty 同時成立，才驗得出 A1a-1 優先於 A1a-2 守衛
    w.findComponent(PlanWorkspace).vm.$emit('busy', true) // 對應 PlanWorkspace busyReason='save'
    await flushPromises()

    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click') // busy 時嘗試切分頁離開 plan
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true) // tab 未被切走
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
  })

  it('G6-P-bump-switchtab：App 層——PlanWorkspace 回報 busy（對應 bump 等待）時嘗試切分頁，被 A1a-1 既有機制直接擋下（busy＋dirty 同時成立；precedence 已實作，本條預期綠）', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)

    w.findComponent(PlanWorkspace).vm.$emit('dirty', true) // busy＋dirty 同時成立，才驗得出 A1a-1 優先於 A1a-2 守衛
    w.findComponent(PlanWorkspace).vm.$emit('busy', true) // 對應 PlanWorkspace busyReason='bump'（App 層機制與 save 案例相同，見上方 describe 註解）
    await flushPromises()

    const specTabBtn = w.findAll('nav button').find(b => b.text() === '規格')
    await specTabBtn!.trigger('click')
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false)
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true)
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
  })
  it('G8-S：同一次導覽只出現一次確認——切分頁時守衛只有一個，捨棄後完成導覽且不再跳第二次', async () => {
    const w = await mountOnSpecDirty()
    const planTabBtn = w.findAll('nav button').find(b => b.text() === '計畫')
    await planTabBtn!.trigger('click')
    await flushPromises()
    expect(w.findAll('[data-test=unsaved-guard]').length).toBe(1) // App 層與元件層不得各跳一次

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(true) // 導覽完成
    expect(w.findAll('[data-test=unsaved-guard]').length).toBe(0) // 不再出現第二次確認
  })

})
