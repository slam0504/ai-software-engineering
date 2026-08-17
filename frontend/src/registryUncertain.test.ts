import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

// ---- M3b Task 2a rev2：registry uncertain latch 的**使用者可見出口**（矩陣 A）----
//
// 這個檔案守的是 owner 追問的那一格：latch 的訊號**真的到得了使用者嗎**。
//
// **rev2 review C1 的教訓**：第一版自己寫了一份 `dispatch()` helper 重現
// App.vue 的分流，結果守到了 routeEnvelope 與 store，**就是沒守到 production 的
// EventsOn handler**——把 App.vue 那一行 `else if (dst === 'notice')` 清空，全套
// 測試照樣綠（形狀 (C)+(D) 合體）。而 A7（late claude init 的背景寫入）沒有任何
// 同步呼叫端可以回錯，那一行斷掉就等於缺口整個回來。
//
// 所以這裡**不重寫任何 production 邏輯**：mount 真的 App.vue、抓它自己註冊的
// `EventsOn('workbench:event')` handler 來送事件，斷言 App 自己 DOM 樹裡的
// Timeline 內容。整條鏈（EventsOn handler → routeEnvelope → store → notices →
// timeline getter → Timeline.vue → v-show 掛載條件）全部是 production 那一份。

// runtimeMocks：EventsOn 收下 handler，測試再回頭呼叫它。ApprovalDialog.test.ts
// 已有同樣的 pattern。
const runtimeMocks = vi.hoisted(() => {
  const handlers: Record<string, (d: any) => void> = {}
  return {
    handlers,
    EventsOn: (name: string, cb: (d: any) => void) => { handlers[name] = cb },
  }
})
vi.mock('../wailsjs/runtime/runtime', () => runtimeMocks)

// App.vue 與其子元件（SessionList／SettingsBar）的 wailsjs 綁定。
const appMocks = vi.hoisted(() => ({
  CLIInfo: vi.fn(async () => ({}) as Record<string, string>),
  GateDecide: vi.fn(), GateDecisionContext: vi.fn(),
  GateList: vi.fn(async () => [] as any[]),
  ListSessions: vi.fn(async () => [] as any[]),
  SpecList: vi.fn(async () => [] as any[]),
  StartSession: vi.fn(), SendMessage: vi.fn(), EndSession: vi.fn(),
  NewSession: vi.fn(async () => undefined),
  TerminateSession: vi.fn(),
  CreateSession: vi.fn(async () => 'w-new'),
  RemoveSession: vi.fn(async () => undefined),
  RecoverCodexRecording: vi.fn(), LoadTurnsBefore: vi.fn(),
  RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
  SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
  AuthStatus: vi.fn(), StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
  RestartCodexServerRecorded: vi.fn(),
  EscalationList: vi.fn(async () => [] as any[]), EscalationAck: vi.fn(),
  EscalationCreate: vi.fn(), EscalationResolve: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => appMocks)

import App from './App.vue'
import { useSession } from './stores/session'
import type { Envelope } from './types'
import { makeI18n } from './test/i18n'

// memStorage：這個 jsdom 環境沒有 localStorage（vitest 啟動時就印
// 「localStorage is not available」），而 lib/persist 的 load/save 對缺席是靜默
// fallback。收合狀態「跨重啟保持」正是 C2 的前提，所以這裡補一個最小實作，
// 讓 timelineOpen 真的走 load('wb.tl.open') 那條路，而不是永遠拿 default。
const memStorage = {
  data: new Map<string, string>(),
  getItem(k: string) { return this.data.has(k) ? this.data.get(k)! : null },
  setItem(k: string, v: string) { this.data.set(k, v) },
  removeItem(k: string) { this.data.delete(k) },
  clear() { this.data.clear() },
}
vi.stubGlobal('localStorage', memStorage)

// jsdom 沒有實作 Element.scrollTo，而真實的 PaneView（DualPane 底下）在 chat
// 更新時會呼叫它。mount 真元件的代價之一；補一個 no-op，不改任何受測邏輯。
if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {}

function resetEnv() {
  memStorage.clear()
  for (const k of Object.keys(runtimeMocks.handlers)) delete runtimeMocks.handlers[k]
  for (const fn of Object.values(appMocks)) (fn as any).mockClear?.()
}

// GO_ERROR：app.go errRegistryUncertain 的實際字串（逐字對照，不簡化）。用真的
// 那一串是因為這組測試要證明的正是「使用者看得懂下一步」，而下一步就寫在裡面。
const GO_ERROR =
  'app: session registry 上一次寫入的結果不確定，建立／移除／開始對話／開新對話已停用；' +
  '請重啟 app（重啟後 registry 以磁碟上的 workspace-sessions.json 為準重新載入）：' +
  'wsregistry: registry 上一次寫入的 commit 結果不確定（檔案已 rename 但 directory sync 失敗）'

// backgroundNotice：Manager.EmitWorkspace 實際送出的形狀——scope='workspace'、
// 訊息只在 payload、頂層 error 是 omitempty 且**從未被填**。
const backgroundNotice: Envelope = {
  event_id: 'ru1', ts: 't', provider: '', scope: 'workspace', kind: 'stream_error',
  payload: { component: 'session-registry', wsid: 'w1', op: 'set_resume', error: GO_ERROR },
}

// legacyUnroutable：rev2 之前的形狀——session lane、**不帶 workspace_session_id**。
const legacyUnroutable: Envelope = {
  event_id: 'ru0', ts: 't', provider: 'claude', kind: 'stream_error', error: GO_ERROR,
}

// 只 stub 與本檔無關的重面板（mermaid／cytoscape 在 jsdom 昂貴且無關）。
// **Timeline／SessionList／SettingsBar／DualPane 一律保持真實**：它們正是證據鏈
// 上要驗的那幾格。
const STUBS = {
  GateConsole: true, EscalationInbox: true, SpecWorkspace: true, PlanWorkspace: true,
  TcaWorkspace: true, EvidenceDetail: true, DiagramPane: true, DagPane: true,
  PreviewPane: true, FileTree: true, ApprovalDialog: true, StatusBar: true,
}

async function mountApp() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(App, { global: { plugins: [pinia, makeI18n()], stubs: STUBS }, attachTo: document.body })
  await flushPromises()
  return w
}

// fire：經 **production 的** EventsOn handler 送事件。測試不自己分流。
function fire(env: Envelope) {
  const h = runtimeMocks.handlers['workbench:event']
  if (!h) throw new Error('App.vue 沒有註冊 workbench:event handler')
  h(env)
}

// timelineText：App 自己 DOM 樹裡 Timeline 的摘要文字。
function timelineText(w: any) {
  return w.findAll('.tl .sum').map((n: any) => n.text()).join('\n')
}

// flush2：元件內 async handler（await binding → catch → store）需要兩輪
// microtask。刻意不用 setTimeout／sleep（barrier 測試不得依賴時間）。
async function flush2(w: any) {
  await Promise.resolve()
  await Promise.resolve()
  await w.vm.$nextTick()
}

describe('registry uncertain latch：背景失敗必須有使用者可見出口（production 分流）', () => {
  beforeEach(resetEnv)

  // C1 的守門：mutation 目標是 App.vue 的
  // `else if (dst === 'notice') s.applyNotice(e)`——清成空 block 必須紅。
  // 其他 mutation：routeEnvelope 把 workspace 非 gate 事件送回 gate；
  // Timeline.summary() 不讀 payload；app.go 換回 session-scope 無 WSID 的形狀。
  it('workspace lane 的 latch 通知經 App.vue 自己的 handler 渲染出來', async () => {
    const w = await mountApp()
    fire(backgroundNotice)
    await w.vm.$nextTick()

    const txt = timelineText(w)
    expect(txt).toContain('session-registry')
    expect(txt).toContain('請重啟 app')
    expect(txt).toContain('已停用')
    expect(txt).not.toBe('stream_error') // 落到 `return e.kind` 就是這個值
  })

  // 「正確的 pane」：latch 是 registry **全域**的事實，正確答案是「不論在看哪個
  // pane 都看得到」。notices 被合併進任何 focused pane 的 timeline。
  it('不論 focus 在哪一個 pane 都看得到（latch 是 registry 全域事實）', async () => {
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    fire(backgroundNotice)

    s.setFocus(0)
    await w.vm.$nextTick()
    expect(timelineText(w)).toContain('請重啟 app')
    s.setFocus(1)
    await w.vm.$nextTick()
    expect(timelineText(w)).toContain('請重啟 app')
  })

  // 迴歸守門：舊形狀到不了使用者。這條**必須維持綠**——它斷言的正是為什麼
  // rev2 非改 app.go 不可。
  it('迴歸：session lane ＋空 WSID 的舊形狀確實到不了使用者（unrouted 無渲染端）', async () => {
    const w = await mountApp()
    const s = useSession()
    fire(legacyUnroutable)
    await w.vm.$nextTick()

    expect(s.unrouted).toBe(1)
    expect(s.notices).toHaveLength(0)
    expect(timelineText(w)).not.toContain('請重啟 app')
  })
})

describe('registry uncertain latch：Timeline 收合時仍必須看得見（rev2 review C2）', () => {
  beforeEach(resetEnv)

  // Timeline 掛在 `v-show="timelineOpen"` 之下，而 timelineOpen 會寫進
  // localStorage——使用者收合過一次就跨重啟保持收合。這條先證明「收合是真的
  // 會發生、而且真的看不到內容」，再證明 toggle 上的未讀 badge 補上了可見度。
  //
  // mutation：拿掉 `.tl-badge`（或不再累計 tlUnread）→ 紅在「收合時必須有未讀
  // 標記」。直接 mount Timeline 的測試繞過整個掛載條件，量不到這一維。
  it('收合狀態（localStorage 記憶）下 latch 通知不可見，但 toggle 有未讀 badge', async () => {
    memStorage.setItem('wb.tl.open', 'false') // 上一輪使用者收合過
    const w = await mountApp()
    expect(w.find('.tl').isVisible(), '前提：收合狀態下 Timeline 內容不可見').toBe(false)

    fire(backgroundNotice)
    await w.vm.$nextTick()

    const badge = w.find('[data-test=tl-unread]')
    expect(badge.exists(), '收合時必須有未讀標記，否則這條出口在收合狀態下可見度是零').toBe(true)
    expect(badge.text()).toBe('1')
  })

  // 展開後：內容看得到、badge 歸零。
  it('展開後訊息可見且未讀歸零', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    fire(backgroundNotice)
    await w.vm.$nextTick()
    await w.find('.tl-toggle').trigger('click')
    await w.vm.$nextTick()

    expect(w.find('.tl').isVisible()).toBe(true)
    expect(timelineText(w)).toContain('請重啟 app')
    expect(w.find('[data-test=tl-unread]').exists()).toBe(false)
  })

  // 展開狀態下不累計——badge 是「沒看到的那些」，不是錯誤總數。
  it('展開狀態下不累計未讀', async () => {
    const w = await mountApp() // 預設展開
    fire(backgroundNotice)
    await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').exists()).toBe(false)
  })
})

describe('registry uncertain latch：同步拒絕的使用者可見出口（矩陣 A）', () => {
  beforeEach(resetEnv)

  // CreateSession：SessionList 的建立按鈕（App 樹裡的真實子元件）。
  // mutation：拿掉 SessionList.createSession 的 catch → 紅在「建立失敗必須渲染」。
  it('CreateSession 被拒絕 → 清單不新增，錯誤原文與下一步渲染出來', async () => {
    appMocks.CreateSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    // data-test=create-claude 在 SettingsBar 與 SessionList 都有一顆（兩個都是
    // production 入口）；這裡限定左欄清單那顆，與註解說的入口一致。
    await w.find('.session-list [data-test=create-claude]').trigger('click')
    await flush2(w)

    expect(w.findAll('[data-test=session-card]')).toHaveLength(0)
    const txt = timelineText(w)
    expect(txt).toContain('建立 claude session 失敗')
    expect(txt).toContain('請重啟 app')
  })

  // RemoveSession：SessionList 的二段式移除。
  // mutation：失敗時仍呼叫 markRemoved → 紅在「卡片不得消失」。
  it('RemoveSession 被拒絕 → 卡片不消失，錯誤原文與下一步渲染出來', async () => {
    appMocks.RemoveSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    await w.vm.$nextTick()
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flush2(w)

    expect(s.sessions['w1'].removed).toBe(false)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(1)
    expect(timelineText(w)).toContain('請重啟 app')
  })

  // NewSession：SettingsBar 的「開新對話」。call() 失敗時**不**呼叫 s.reset()，
  // 所以 transcript 不得被清掉——否則畫面看起來像已經開了新對話。
  // mutation：call() 的 catch 靜默 → 紅在「必須渲染」；s.reset() 移到 try 之外
  // → 紅在「transcript 不得被清」。
  it('NewSession 被拒絕 → transcript 不被重設，錯誤原文與下一步渲染出來', async () => {
    appMocks.NewSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.pin(0, 'w1')
    s.setFocus(0)
    s.apply({ event_id: 'e1', ts: 't', provider: 'claude', kind: 'message',
      role: 'assistant', text: '之前的回覆', workspace_session_id: 'w1' })
    await w.vm.$nextTick()

    const btn = w.findAll('button').find((b: any) => b.text() === '開新對話')
    expect(btn, '找不到「開新對話」按鈕').toBeTruthy()
    await btn!.trigger('click')
    await flush2(w)

    expect(s.chat.map(c => c.text)).toContain('之前的回覆')
    const txt = timelineText(w)
    expect(txt).toContain('開新對話失敗')
    expect(txt).toContain('請重啟 app')
  })

  // StartSession：composer 送出（store.submit 的第一輪）。錯誤進**該 WSID 自己
  // 的** timeline，而不是 workspace notices——使用者知道是哪個 session 送不出去。
  // mutation：submit 的 catch 靜默 → 紅在 busy 與渲染兩行。
  it('StartSession 被拒絕 → 該 session 的 timeline 顯示錯誤，busy 解除', async () => {
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.pin(0, 'w1')
    s.setFocus(0)
    s.setBindings({ StartSession: vi.fn(async () => { throw new Error(GO_ERROR) }) } as any)

    await s.submit('go')
    await w.vm.$nextTick()

    expect(s.busy).toBe(false)
    const txt = timelineText(w)
    expect(txt).toContain('請重啟 app')
    expect(txt).toContain('已停用')
  })
})
