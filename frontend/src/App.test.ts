import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

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
  RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
  SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
  EscalationList: vi.fn(async () => []), EscalationCreate: vi.fn(), EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => wailsAppMocks)
vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn() }))

import App from './App.vue'
import DualPane from './components/DualPane.vue'
import SessionList from './components/SessionList.vue'
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
})
