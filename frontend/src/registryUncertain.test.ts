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

// plainError：**非 latch** 的一般 workspace 錯誤。badge 的語意（累計、不誤報）
// 要用它驗——latch 會觸發 one-shot 強制展開，用 latch 驗 badge 等於驗不到。
const plainError: Envelope = {
  event_id: 'ru9', ts: 't', provider: '', scope: 'workspace', kind: 'stream_error',
  payload: { component: 'replay-index', error: 'replay index degraded: checkpoint ahead of audit' },
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

describe('Timeline 收合時的可見性（rev2 review C2／rev3 review 修正）', () => {
  beforeEach(resetEnv)

  // badge 的來源必須是 store 的**單調** errorSeq，不是 `s.timeline` 的投影。
  // mutation：拿掉 .tl-badge → 紅在「收合時必須有未讀標記」。
  it('收合狀態（localStorage 記憶）下錯誤不可見，但 toggle 有未讀 badge', async () => {
    memStorage.setItem('wb.tl.open', 'false') // 上一輪使用者收合過
    const w = await mountApp()
    expect(w.find('.tl').isVisible(), '前提：收合狀態下 Timeline 內容不可見').toBe(false)

    fire(plainError)
    await w.vm.$nextTick()

    const badge = w.find('[data-test=tl-unread]')
    expect(badge.exists(), '收合時必須有未讀標記，否則這條出口在收合狀態下可見度是零').toBe(true)
    expect(badge.text()).toBe('1')
  })

  // rev3 review：累計性本身**零守門**——把 `tlUnread += now - prev` 改成 `= 1`
  // 舊測試照樣全綠。這條補上。
  // mutation：改成 `tlUnread.value = 1` → 紅在「兩則錯誤必須累計成 2」。
  it('未讀單調累計：兩則錯誤顯示 2', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    fire(plainError)
    fire({ ...plainError, event_id: 'ru9b' })
    await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').text()).toBe('2')
  })

  // rev3 review：badge 從 focus-dependent 的投影推導時會**誤報**——收合狀態下
  // 切 pane 再切回（沒有新錯誤）會把已讀的重新算成未讀。
  // mutation：badge 改回 `s.timeline.filter(...).length` 的投影 → 紅在這裡。
  it('切換 pane 不重算未讀（單調來源，不是 focus 投影）', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    // 錯誤放在**目前 focus 的** w1 自己的 lane：投影版本在「切走再切回」時會看到
    // 0→1 的變化而再加一次，這正是 reviewer 說的誤報。用 workspace notice 驗不
    // 出來（notices 對兩個 pane 都可見，投影值不會變）。
    s.apply({ event_id: 'e-w1', ts: 't', provider: 'claude', kind: 'stream_error',
      error: 'claude 那邊壞了', workspace_session_id: 'w1' })
    await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').text()).toBe('1')

    s.setFocus(1); await w.vm.$nextTick()
    s.setFocus(0); await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').text(), '沒有新錯誤，未讀不得增加').toBe('1')
  })

  // rev3 review：**漏報**的另一半——非 focus pane 的 session lane 錯誤也必須
  // 算進未讀（投影版本完全看不到它）。
  it('非 focus pane 的錯誤也算進未讀', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)

    s.apply({ event_id: 'e-w2', ts: 't', provider: 'codex', kind: 'stream_error',
      error: 'codex 那邊壞了', workspace_session_id: 'w2' })
    await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').exists(), '非 focus pane 的錯誤不得漏報').toBe(true)
  })

  it('展開後訊息可見且未讀歸零', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    fire(plainError)
    await w.vm.$nextTick()
    await w.find('.tl-toggle').trigger('click')
    await w.vm.$nextTick()

    expect(w.find('.tl').isVisible()).toBe(true)
    expect(timelineText(w)).toContain('replay index degraded')
    expect(w.find('[data-test=tl-unread]').exists()).toBe(false)
  })

  it('展開狀態下不累計未讀', async () => {
    const w = await mountApp() // 預設展開
    fire(plainError)
    await w.vm.$nextTick()
    expect(w.find('[data-test=tl-unread]').exists()).toBe(false)
  })
})

describe('latch 抵達時強制展開一次（rev3 review Important）', () => {
  beforeEach(resetEnv)

  // latch 的成本不對稱：必須重啟、之後每個 lifecycle 操作都會被拒。badge 需要
  // 使用者主動點一下，對這個等級的狀態不夠。出口仍是 Timeline 那一條，只是
  // 保證它在那一刻是開的。
  //
  // mutation：拿掉 App.vue 的 latchSeq watcher → 紅在「latch 必須強制展開」。
  it('收合狀態下 latch 抵達 → 自動展開且訊息立刻可見', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    const w = await mountApp()
    expect(w.find('.tl').isVisible(), '前提：起始為收合').toBe(false)

    fire(backgroundNotice)
    await w.vm.$nextTick()

    expect(w.find('.tl').isVisible(), 'latch 必須強制展開（不能只留一個要點的 badge）').toBe(true)
    expect(timelineText(w)).toContain('請重啟 app')
  })

  // one-shot：使用者自己收合之後，第二則 latch 不再搶畫面。
  // mutation：把 latchForcedOpen 的 one-shot 判斷拿掉 → 紅在「不得反覆搶畫面」。
  it('one-shot：使用者收合後第二則 latch 不再強制展開', async () => {
    const w = await mountApp()
    fire(backgroundNotice)
    await w.vm.$nextTick()
    await w.find('.tl-toggle').trigger('click') // 使用者讀完，自己收合
    await w.vm.$nextTick()
    expect(w.find('.tl').isVisible()).toBe(false)

    fire({ ...backgroundNotice, event_id: 'ru1b' })
    await w.vm.$nextTick()
    expect(w.find('.tl').isVisible(), 'one-shot：不得反覆搶畫面').toBe(false)
    expect(w.find('[data-test=tl-unread]').exists(), '但仍要留下未讀標記').toBe(true)
  })

  // 同步拒絕路徑也是 latch：錯誤只有字串，靠 REGISTRY_UNCERTAIN_MARK 辨識。
  // mutation：改掉 Go 或前端任一邊的片語 → Go 的
  // TestErrRegistryUncertainKeepsUIMarker 紅；這裡則紅在「同步拒絕也要強制展開」。
  it('同步拒絕（Create）帶的 latch 訊息同樣強制展開', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    appMocks.CreateSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    await w.find('.session-list [data-test=create-claude]').trigger('click')
    await flush2(w)

    expect(w.find('.tl').isVisible(), '同步拒絕也要強制展開').toBe(true)
    expect(timelineText(w)).toContain('請重啟 app')
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

  // RemoveSession（矩陣 A9）：**rev3 review 抓到的 Critical 就在這一條的 setup**。
  //
  // 舊版只 `registerSession` 沒 `pin`，所以 `views['w1']` 不存在，`pushError`
  // 落到 `applyNotice` → notices（全域可見）→ 測試綠。但 production 上使用者
  // 通常是對**已經釘住**的 session 按移除，那時 `views[wsid]` 存在，pushError
  // 會寫進那個 view 的 timeline——而 `timeline` getter **只讀 focused view**。
  // 對非 focus 的 w2 按移除 ⇒ 訊息寫進看不到的 view：收合時沒 badge、展開後
  // focused timeline 也是空的，使用者只看到「卡片沒消失」。形狀 (B)：setup 的
  // 一條捷徑同時滿足了多個前提，把真正的路徑繞過去。
  //
  // 現在的 setup 就是 production 最常見的樣子：**兩個 pane 都釘著、對非 focus
  // 的那個按移除**，而且全程走真實點擊。
  //
  // mutation：SessionList 的 catch 改回 `pushError(msg, wsid, true)` →
  // 紅在「非 focus pane 的移除失敗也必須看得到」。
  it('RemoveSession 被拒絕（對非 focus pane 的 session）→ 卡片不消失且訊息看得到', async () => {
    appMocks.RemoveSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    s.setFocus(0) // focus 在 w1，要移除的是 w2
    await w.vm.$nextTick()

    await w.find('[data-test=remove-w2]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flush2(w)

    expect(appMocks.RemoveSession).toHaveBeenCalledWith('w2')
    expect(s.sessions['w2'].removed).toBe(false)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(2)
    const txt = timelineText(w)
    expect(txt, '非 focus pane 的移除失敗也必須看得到').toContain('移除 session w2 失敗')
    expect(txt).toContain('請重啟 app')
  })

  // 同一條路徑在 timeline 收合時的樣子：latch 會強制展開，所以訊息照樣到得了。
  // mutation：SessionList 的 catch 改回 pane-scoped pushError → latchSeq 仍會
  // 增加（訊息含 marker）所以會展開，但 focused timeline 是空的 → 紅在文字斷言。
  it('RemoveSession 被拒絕（收合＋非 focus）→ 強制展開且訊息看得到', async () => {
    memStorage.setItem('wb.tl.open', 'false')
    appMocks.RemoveSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const w = await mountApp()
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    s.setFocus(0)
    await w.vm.$nextTick()
    expect(w.find('.tl').isVisible(), '前提：起始為收合').toBe(false)

    await w.find('[data-test=remove-w2]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flush2(w)

    expect(w.find('.tl').isVisible()).toBe(true)
    expect(timelineText(w)).toContain('移除 session w2 失敗')
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
