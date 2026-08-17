import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

// ---- M3b owner review 修正 3：pane pins 持久化（spec §3.2.1 白名單、§3.8 啟動
// 只重建兩個釘選 pane）----
//
// 這個檔案守的是證據鏈上前端那三格：**production 入口 → 寫入**、**重啟後載入 →
// consumer**、以及 **失敗時不擋路**。
//
// 兩個刻意的做法，對應本里程碑實證的兩種失效形狀：
//
// (B) 「setup 繞過 production 走法」——**不直接呼叫 store action 當作接線證據**。
//     釘選一律點真的 SessionList 卡片上的按鈕，還原一律靠 mount 真的 App.vue 跑
//     它自己的 onMounted。registryUncertain.test.ts 已經示範過：自己寫一份分流
//     helper 會守到 store 卻守不到 App.vue 那一行。
//
// (F) 「跨重啟那一維沒守」——pins 的全部價值都在重啟後。所以有一條測試會
//     **unmount 舊 App、建立新的 pinia、mount 新的 App**，並且把第一個 App 實際
//     寫出去的那組參數餵回第二個 App 的讀取端。同一個實例假裝重啟守不到這件事。

const runtimeMocks = vi.hoisted(() => {
  const handlers: Record<string, (d: any) => void> = {}
  return {
    handlers,
    EventsOn: (name: string, cb: (d: any) => void) => { handlers[name] = cb },
  }
})
vi.mock('../wailsjs/runtime/runtime', () => runtimeMocks)

// paneLayoutState：PaneLayout() 這個**讀取端**回傳的內容。測試改它＝改「磁碟上
// 那一份」，App 重新 mount 時就會讀到——跨重啟那條測試靠的就是這個。
const appMocks = vi.hoisted(() => {
  const state = { layout: { pins: ['', ''] as string[], focused: '' } }
  return {
    state,
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
    RecoverCodexRecording: vi.fn(),
    LoadTurnsBefore: vi.fn(async () => [] as any[]),
    PaneLayout: vi.fn(async () => ({ ...state.layout })),
    SetPaneLayout: vi.fn(async (_pins: string[], _focused: string) => undefined),
    ResolveApproval: vi.fn(async () => undefined),
    RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
    SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
    AuthStatus: vi.fn(), StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
    RestartCodexServerRecorded: vi.fn(),
    EscalationList: vi.fn(async () => [] as any[]), EscalationAck: vi.fn(),
    EscalationCreate: vi.fn(), EscalationResolve: vi.fn(),
  }
})
vi.mock('../wailsjs/go/main/App', () => appMocks)

import App from './App.vue'
import { useSession } from './stores/session'
import type { Envelope } from './types'
import { makeI18n } from './test/i18n'

const memStorage = {
  data: new Map<string, string>(),
  getItem(k: string) { return this.data.has(k) ? this.data.get(k)! : null },
  setItem(k: string, v: string) { this.data.set(k, v) },
  removeItem(k: string) { this.data.delete(k) },
  clear() { this.data.clear() },
}
vi.stubGlobal('localStorage', memStorage)
if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {}

// GO_ERROR：app.go errRegistryUncertain 的實際字串（逐字對照）。latch 期間
// SetPaneLayout 回的就是它——訊息裡的片語同時是 store 撥 latchSeq 的依據。
const GO_ERROR =
  'app: session registry 上一次寫入的結果不確定，建立／移除／開始對話／開新對話已停用；' +
  '請重啟 app（重啟後 registry 以磁碟上的 workspace-sessions.json 為準重新載入）：' +
  'wsregistry: registry 上一次寫入的 commit 結果不確定（檔案已 rename 但 directory sync 失敗）'

function session(wsid: string, provider: string, label: string) {
  return {
    wsid, provider, task_label: label, resume_session_id: '',
    created_at: '2026-08-01T00:00:00Z', available: true, state: 'idle',
  }
}

function resetEnv() {
  memStorage.clear()
  for (const k of Object.keys(runtimeMocks.handlers)) delete runtimeMocks.handlers[k]
  for (const fn of Object.values(appMocks)) (fn as any).mockClear?.()
  appMocks.state.layout = { pins: ['', ''], focused: '' }
  appMocks.ListSessions.mockImplementation(async () => [])
  appMocks.SetPaneLayout.mockImplementation(async () => undefined)
  appMocks.PaneLayout.mockImplementation(async () => ({ ...appMocks.state.layout }))
}

// Timeline／SessionList／DualPane／ApprovalDialog 一律保持真實——它們正是證據鏈
// 上要驗的那幾格（釘選按鈕、pane 內容、transient 路由、失敗通知）。
const STUBS = {
  GateConsole: true, EscalationInbox: true, SpecWorkspace: true, PlanWorkspace: true,
  TcaWorkspace: true, EvidenceDetail: true, DiagramPane: true, DagPane: true,
  PreviewPane: true, FileTree: true, StatusBar: true,
}

async function mountApp() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(App, { global: { plugins: [pinia, makeI18n()], stubs: STUBS }, attachTo: document.body })
  await flushPromises()
  return w
}

function paneText(w: any, idx: 0 | 1) {
  return w.find(`[data-test=pane-${idx}]`).text()
}

// setLayoutCalls：SetPaneLayout 真的收到的參數（成對的 pins／focused）。
function setLayoutCalls() {
  return vi.mocked(appMocks.SetPaneLayout).mock.calls as unknown as [string[], string][]
}

// fire：經 **production 的** `workbench:event` handler 送事件（同
// registryUncertain.test.ts 的慣例）。測試不自己重寫一份分流。
function fire(env: Envelope) {
  const h = runtimeMocks.handlers['workbench:event']
  if (!h) throw new Error('App.vue 沒有註冊 workbench:event handler')
  h(env)
}

function timelineText(w: any) {
  return w.findAll('.tl .sum').map((n: any) => n.text()).join('\n')
}

describe('§3.8 啟動重建兩個釘選 pane：registry 的排列是唯一輸入', () => {
  beforeEach(resetEnv)

  // 這條守的是缺陷本身：App.vue 的 onMounted 過去只做 hydrateSessions，pins 恆為
  // [null, null]，重啟後兩個 pane 都是空的。
  //
  // mutation（實測結果，據實記錄）：
  //   - 拿掉 App.vue onMounted 的 restoreLayout 呼叫 → 紅在本條的 `paneText(w, 0)`
  //     內容斷言。這一刀**同時打紅本檔 7 條**（本條、跳過未知 WSID、切焦點寫入、
  //     移除清排列、跨重啟、transient、busy）——「還原有沒有接上」是整組讀取端
  //     測試的共同前提，不宣稱它只紅一條。
  //   - restoreLayout 不還原 focused → 紅在 `focused pane 必須跟著還原`
  //     （另紅跨重啟那條的 `焦點也要跨重啟`）。
  //   - restoreLayout 只設 pins、不走 pin() → **只**紅在 `LoadTurnsBefore` 那兩行，
  //     即 §3.8 的「各載入最近 20 個完整 turn」那一格。
  it('啟動時依 registry 的 pins 重建兩個 pane 並還原焦點與尾端視窗', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w2' }

    const w = await mountApp()

    expect(paneText(w, 0)).toContain('alpha')
    expect(paneText(w, 1)).toContain('beta')
    const s = useSession()
    expect(s.pins).toEqual(['w1', 'w2'])
    expect(s.persistentPins).toEqual(['w1', 'w2'])
    expect(s.focused, 'focused pane 必須跟著還原').toBe(1)
    // §3.8：釘選的 pane 要載入尾端視窗（各 20 個完整 turn）
    expect(appMocks.LoadTurnsBefore).toHaveBeenCalledWith('w1', '', 20)
    expect(appMocks.LoadTurnsBefore).toHaveBeenCalledWith('w2', '', 20)
  })

  // 還原是**讀取**，不該回頭寫。逐格 pin 若各寫一次，中途關掉 app 就把使用者
  // 的第二個釘選弄丟了（第一次寫出去的排列只有 pane 0）。
  // mutation：restoreLayout 改成呼叫 `this.pin(idx, w)`（預設 persist=true）→
  // 紅在本條的 `啟動還原期間不得寫入排列`（另紅「點另一個 pane 切焦點」那條的
  // `前提：還原本身不寫`）。
  it('還原不回寫 registry（啟動沒有新資訊要落盤）', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w2' }

    await mountApp()

    expect(setLayoutCalls(), '啟動還原期間不得寫入排列').toHaveLength(0)
  })

  // Go 端 PaneLayout() 已經濾掉 tombstone，這是第二道：沒有 metadata 的 WSID
  // 釘上去會生出一個綁不到任何 session 的 pane（provider／狀態全部讀不到）。
  // mutation：拿掉 restoreLayout 的 `!this.sessions[w]` 檢查 → **只**紅在本條
  // 的 `s.pins` 斷言（會變成 ['ghost', 'w2']，pane-0 渲染出一個沒有 provider 的頭列）。
  it('跳過沒有 metadata 的 WSID，不生出綁不到 session 的 pane', async () => {
    appMocks.ListSessions.mockImplementation(async () => [session('w2', 'codex', 'beta')])
    appMocks.state.layout = { pins: ['ghost', 'w2'], focused: 'ghost' }

    const w = await mountApp()

    const s = useSession()
    expect(s.pins).toEqual([null, 'w2'])
    expect(paneText(w, 0)).toContain('還沒有釘選任何 session')
    expect(paneText(w, 1)).toContain('beta')
    expect(s.focused, 'focused 指向被跳過的那一格時維持預設').toBe(0)
  })

  // 讀取失敗不得擋住啟動——降級是「兩個 pane 空白、使用者自己釘」，不是開不起來。
  // mutation：拿掉 App.vue 的 try/catch → **只**紅在本條（未捕捉的 rejection 讓
  // onMounted 後續的 refreshGate／refreshEscalation 不再執行，兩個斷言都倒）。
  it('PaneLayout 讀取失敗時 fail loud 但不擋啟動', async () => {
    appMocks.PaneLayout.mockImplementation(async () => { throw new Error('registry not loaded') })

    const w = await mountApp()

    expect(timelineText(w)).toContain('registry not loaded')
    expect(appMocks.GateList, '啟動序列後續步驟必須照常執行').toHaveBeenCalled()
  })
})

describe('pins 寫入：production 入口與跨重啟', () => {
  beforeEach(resetEnv)

  // 寫入端的 production 入口——真的點 SessionList 卡片上的釘選鈕。
  // mutation：拿掉 store pin() 的 `void this.persistLayout()` → 紅在本條的
  // `setLayoutCalls().at(-1)`（另紅「跨重啟」與「latch 不擋路」——三條都以
  // 「釘選會寫入」為前提）。
  it('點釘選鈕會把排列寫進 registry（帶 focused pane）', async () => {
    appMocks.ListSessions.mockImplementation(async () => [session('w1', 'claude', 'alpha')])
    const w = await mountApp()

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()

    expect(setLayoutCalls().at(-1)).toEqual([['w1', ''], 'w1'])
  })

  // 切焦點同樣是 §3.2.1 白名單的一部分（focused pane）。
  // mutation：拿掉 setFocus 的 persistLayout → 紅在本條的 `setLayoutCalls().at(-1)`
  //（另紅「transient 不得寫進 durable 排列」與「失敗訊息不綁 pane」——那兩條都用
  // 切焦點當寫入觸發器）。
  it('點另一個 pane 切焦點也會寫入', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()
    expect(setLayoutCalls(), '前提：還原本身不寫').toHaveLength(0)

    await w.find('[data-test=pane-1]').trigger('click')
    await flushPromises()

    expect(setLayoutCalls().at(-1)).toEqual([['w1', 'w2'], 'w2'])
  })

  // ---- (F) 跨重啟：**新的 App 實例讀上一個 App 實際寫出去的那一份** ----
  //
  // 這不是「同一個 store 再讀一次自己」：舊 wrapper unmount、新的 pinia、新的
  // App.vue mount，中間唯一傳遞資訊的管道就是 SetPaneLayout 寫出去、PaneLayout
  // 讀回來的那組參數（模擬 workspace-sessions.json）。
  //
  // mutation（各自只打紅一列）：
  //   - persistLayout 送 `this.pins` 而不是 `persistentPins` → 這條仍綠（沒有
  //     transient），由下面 §3.6.4 那條打紅；此處據實說明，不宣稱它守得到。
  //   - 拿掉 pin() 的 persistLayout → 紅在本條的 `paneText(second, 0)`（重啟後
  //     pane-0 是空的）。
  //   - 拿掉 App.vue 的 restoreLayout → 同上，紅在重啟後。
  it('重啟後仍是同一組釘選（新 App 實例讀上一輪寫出去的排列）', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    const first = await mountApp()
    await first.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()
    await first.find('[data-test=pane-1]').trigger('click') // 焦點切到第二格
    await first.find('[data-test=pin-w2]').trigger('click')
    await flushPromises()

    // 「落盤」：把最後一次寫入的參數放進讀取端，其餘一律清空重來。
    const [pins, focused] = setLayoutCalls().at(-1)!
    first.unmount()
    resetEnv()
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins, focused }

    const second = await mountApp()

    expect(paneText(second, 0)).toContain('alpha')
    expect(paneText(second, 1)).toContain('beta')
    expect(useSession().focused, '焦點也要跨重啟').toBe(1)
  })

  // 移除一個**已釘選**的 session 必須把那一格從 durable 排列裡清掉——否則
  // registry 會留著一個指向 tombstone 的 pin。Go 端 PaneLayout() 讀取時會濾掉它
  // （TestPaneLayoutDropsRemovedPin），但那是第二道防線，不是不寫的理由。
  // 走 production 路徑：SessionList 的兩段式移除（先問、再送）。
  //
  // mutation（各自只打紅一列）：
  //   - markRemoved 拿掉 `if (unpinned) void this.persistLayout()` → 紅在本條第一個
  //     `setLayoutCalls().at(-1)`（另紅 busy 那條——它也靠移除觸發寫入）。
  //   - 把該條件改成無條件寫入 → **只**紅在 `移除未釘選的 session 不改變排列`。
  it('移除已釘選的 session 會清掉 durable 排列；移除未釘選的則不寫', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', ''], focused: 'w1' }
    const w = await mountApp()

    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flushPromises()

    expect(setLayoutCalls().at(-1)).toEqual([['', ''], ''])
    const afterPinned = setLayoutCalls().length

    // w2 從來沒被釘選：移除它不改變排列，不該產生寫入。
    await w.find('[data-test=remove-w2]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flushPromises()

    expect(setLayoutCalls().length, '移除未釘選的 session 不改變排列').toBe(afterPinned)
  })

  // **restore 側的 durable focus**（rev3 review：這一行曾經零守門，刪掉它全套照樣綠）。
  //
  // `restoreLayout` 只設 `focused`、不設 `durableFocusPane` 時：重啟後畫面焦點在
  // pane 1，但 durable 仍是 0。接下來任何**不是 pane 選擇**的 layout 寫入
  //（`markRemoved()`／`unpin()`）都會送 `pins[0]`，**把使用者的焦點選擇靜默改成
  // pane 0**。這與 approval 側是同一種污染，只是來源換成 restore。
  //
  // 用「移除另一格的 session」當那個寫入（走 SessionList 兩段式移除，不碰 pane
  // 選擇）。失敗形態很清楚：正確版寫出 focused='w2'，漏設版寫出 focused=''。
  //
  // mutation：拿掉 restoreLayout 的 `this.durableFocusPane = at`
  //   → 紅在「durable focused 必須是還原時那一格」，且跨重啟那段也紅。
  it('restore 還原的焦點必須同時是 durable 的，不被後續非 pane 選擇的寫入洗掉', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w2' }
    const first = await mountApp()
    const s = useSession()
    expect(s.focused, '前提：還原到 pane 1').toBe(1)
    expect(setLayoutCalls(), '前提：還原本身不寫').toHaveLength(0)

    await first.find('[data-test=remove-w1]').trigger('click')
    await first.find('[data-test=remove-confirm-submit]').trigger('click')
    await flushPromises()

    const [pins, focused] = setLayoutCalls().at(-1)!
    expect(pins, '前提：pane 0 的釘選被清掉了').toEqual(['', 'w2'])
    expect(focused, 'durable focused 必須是還原時那一格，不得被洗成 pane 0').toBe('w2')

    first.unmount()
    resetEnv()
    appMocks.ListSessions.mockImplementation(async () => [session('w2', 'codex', 'beta')])
    appMocks.state.layout = { pins, focused }
    const second = await mountApp()
    expect(useSession().focused, '重啟後仍須停在使用者的那一格').toBe(1)
  })

  // §3.6.4：persistent pin 永不被 transient view 改寫——durable 層的落點。
  // 走 production 路徑：ApprovalDialog 自己註冊的 `approval:request` handler。
  //
  // mutation：persistLayout 改送 `this.pins`（而不是 persistentPins）→ **只**紅在
  // 本條最後的 `setLayoutCalls().at(-1)`（寫出去的第二格會是 w3）；上面那條跨重啟
  // 測試實測仍綠——這正是它
  // 需要獨立存在的理由。
  it('approval 的 transient 顯示不得寫進 durable 排列', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w3', 'codex', 'gamma'),
    ])
    appMocks.state.layout = { pins: ['w1', ''], focused: 'w1' }
    const w = await mountApp()

    const approval = runtimeMocks.handlers['approval:request']
    expect(approval, 'ApprovalDialog 必須註冊 approval:request handler').toBeTruthy()
    approval!({ id: 'a1', wsid: 'w3', provider: 'codex', toolName: 'bash', inputJson: '{}' })
    await flushPromises()

    const s = useSession()
    expect(s.pins[1], '前提：transient 顯示真的發生了').toBe('w3')
    expect(s.persistentPins[1]).toBeNull()

    // 觸發一次寫入（切焦點），看送出去的是哪一份
    await w.find('[data-test=pane-0]').trigger('click')
    await flushPromises()
    expect(setLayoutCalls().at(-1)).toEqual([['w1', ''], 'w1'])
  })
})

describe('寫入失敗：fail loud 但不擋路', () => {
  beforeEach(resetEnv)

  // 本票的裁決：pins 是 UI 偏好、不是 resume correctness。registry uncertain
  // latch 期間釘選仍然要生效，只是重啟後會遺失——不能讓「釘選失敗」變成「不能
  // 釘選」。
  //
  // mutation（各自只打紅一列）：
  //   - persistLayout 的 catch 改成清掉 pins（回滾）→ **只**紅在
  //     `寫入失敗不得回滾使用者剛做的釘選`。
  //   - 把 catch 換成靜默 `.catch(() => {})` → 紅在本條的 `重啟後會遺失`
  //     （另紅「失敗訊息不綁 pane」與 busy 那兩條的同一則通知斷言）。
  //   - catch 改用 pushError 而非 pushNotice → 本條實測**仍綠**（focused pane 剛好
  //     是 w1，看得到）。真正打紅它的是本 describe 最後那條 busy 測試，不是下一條
  //     ——見那兩條各自的說明（review #1 更正）。
  it('registry latch 拒絕寫入時，釘選照常生效並在 notices lane fail loud', async () => {
    appMocks.ListSessions.mockImplementation(async () => [session('w1', 'claude', 'alpha')])
    appMocks.SetPaneLayout.mockImplementation(async () => { throw new Error(GO_ERROR) })
    const w = await mountApp()

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()

    const s = useSession()
    expect(s.pins[0], '寫入失敗不得回滾使用者剛做的釘選').toBe('w1')
    expect(paneText(w, 0)).toContain('alpha')
    expect(timelineText(w)).toContain('重啟後會遺失')
    expect(s.latchSeq, 'latch 片語必須照常撥動 latchSeq（強制展開 timeline）').toBe(1)
  })

  // 這條守的是**可見性**：排列寫入失敗的訊息在任何 focus 狀態下都到得了使用者。
  //
  // **它守不到 `pushNotice` vs `pushError` 的選擇**（review #1 更正——原本的
  // mutation 宣稱是錯的）：`pushError(msg)` 不帶 wsid 時 `w = this.focusedWsid`，
  // 而 `setFocus` 是**先改 `focused`、才觸發 `persistLayout`**，所以 catch 執行時
  // 焦點已經在使用者剛點的那個 pane，訊息一樣落進 focused view、一樣讀得到。
  // 換成 pushError 這條實測**仍綠**。兩者真正的差異（`pushError` 會清掉該 session
  // 的 `busy`）由下一條守。
  //
  // mutation：`persistLayout` 的 catch 換成靜默 `.catch(() => {})` → 紅在本條的
  // `重啟後會遺失`。
  it('失敗訊息不綁 pane：焦點在另一個 session 也看得到', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()
    appMocks.SetPaneLayout.mockImplementation(async () => { throw new Error(GO_ERROR) })

    await w.find('[data-test=pane-1]').trigger('click') // 焦點切到 w2，同時觸發寫入
    await flushPromises()

    expect(useSession().focused).toBe(1)
    expect(timelineText(w)).toContain('重啟後會遺失')
  })

  // **`pushNotice` 而非 `pushError` 的真正理由**（review #1：上一條宣稱守到、
  // 實際沒守到，這條補上）。
  //
  // 兩者的差異不在可見性——在副作用：`pushError` 會無條件 `m.busy = false`。
  // 那個出口的本職是「送出前把 busy 設成 true 的呼叫端失敗要復原」，而
  // `persistLayout` 從頭到尾沒碰過任何 session 的 busy。走 pane-scoped 出口的話，
  // 一次排列寫入失敗會把**焦點所在那個 session 正在跑的 turn** 標成閒置：
  // busy-dot 消失、composer 解除 disabled，使用者以為可以送下一則訊息了。
  //
  // 情境全部走 production：w1／w2 各釘一格、焦點在 w2、w2 由真的 canonical user
  // message（經 App.vue 自己的 EventsOn handler）進入 busy，然後從 SessionList
  // 移除 w1 觸發 persistLayout 失敗。
  //
  // mutation：`persistLayout` 的 catch 改成 `this.pushError(...)` → **只**紅在本條
  // 的 busy 斷言（`expected false to be true`），全檔其餘 14 條照樣綠。
  it('寫入失敗不得清掉別的 session 正在跑的 turn（pushNotice 的真正理由）', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w2' }
    const w = await mountApp()
    const s = useSession()

    // canonical user message ＝ turn 起點（§3.5.9）——經 production handler 進 store
    fire({
      event_id: 'e1', ts: 't', provider: 'codex', kind: 'message', role: 'user',
      text: 'run it', workspace_session_id: 'w2',
    })
    await w.vm.$nextTick()
    expect(s.sessions.w2.busy, '前提：w2 必須真的在跑一個 turn').toBe(true)

    appMocks.SetPaneLayout.mockImplementation(async () => { throw new Error(GO_ERROR) })
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flushPromises()

    expect(timelineText(w), '前提：失敗確實有發出通知').toContain('重啟後會遺失')
    expect(
      s.sessions.w2.busy,
      '排列寫入失敗不得動到別的 session 的 busy——pushError 會清掉它',
    ).toBe(true)
  })

})

describe('釘選＝明確選定 session ＋ pane，approval 的暫時切換不算（owner 2026-08-17 裁決）', () => {
  beforeEach(resetEnv)

  // ---- 判準 1／2：釘進哪一格，focus 就跟到哪一格 ----
  //
  // production 的釘選鈕永遠釘進「畫面上作用的那一格」（`s.pin(s.focused, wsid)`），
  // 所以要讓「focus 真的移動」在 production 路徑上可觀測，就得製造出「畫面作用格 ≠
  // durable 格」的狀態——approval 的暫時切換正好是唯一會產生它的 production 情境。
  // 這兩條因此各自用 approval 把畫面帶到目標格，再由使用者按釘選。
  //
  // mutation：
  //   - pin() 完全不更新 durableFocusPane → 兩條都紅（同一行實作）。
  //   - pin() 寫死 durableFocusPane = 0 → **只**紅判準 2 那條。
  //   - pin() 寫死 durableFocusPane = 1 → **只**紅判準 1 那條。
  //   後兩刀證明兩格是各自獨立被守住的，不是一條測試兼差。
  it('判準 1：釘進第 1 格 → Pins[0] 是該 session 且 Focused 指向第 1 格', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'), session('w3', 'claude', 'gamma'),
    ])
    // durable 起點是第 2 格，approval 把畫面帶到第 1 格
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w2' }
    const w = await mountApp()
    const s = useSession()
    expect(s.durableFocusPane, '前提：durable 起點是第 2 格').toBe(1)
    runtimeMocks.handlers['approval:request']!({
      id: 'a1', wsid: 'w1', provider: 'claude', toolName: 'bash', inputJson: '{}',
    })
    await flushPromises()
    expect(s.focused, '前提：畫面被 approval 帶到第 1 格').toBe(0)

    await w.find('[data-test=pin-w3]').trigger('click')
    await flushPromises()

    const [pins, focused] = setLayoutCalls().at(-1)!
    expect(pins[0], 'Pins[0] 必須是剛釘進去的那個 session').toBe('w3')
    expect(focused, 'Focused 必須指向第 1 格').toBe(pins[0])
    expect(s.durableFocusPane).toBe(0)
  })

  it('判準 2：釘進第 2 格 → focus 同步移到第 2 格', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'), session('w3', 'claude', 'gamma'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()
    const s = useSession()
    expect(s.durableFocusPane, '前提：durable 起點是第 1 格').toBe(0)
    runtimeMocks.handlers['approval:request']!({
      id: 'a1', wsid: 'w2', provider: 'codex', toolName: 'bash', inputJson: '{}',
    })
    await flushPromises()
    expect(s.focused, '前提：畫面被 approval 帶到第 2 格').toBe(1)

    await w.find('[data-test=pin-w3]').trigger('click')
    await flushPromises()

    const [pins, focused] = setLayoutCalls().at(-1)!
    expect(pins[1]).toBe('w3')
    expect(focused, 'focus 必須同步移到第 2 格').toBe(pins[1])
    expect(s.durableFocusPane).toBe(1)
  })

  // ---- 判準 1 × 判準 4 的交互（coordinator 的解讀，owner 裁決的延伸）----
  //
  // **解讀**：判準 4 約束的是「**approval 的暫時焦點切換**本身」不得改動 durable
  // focus——approval 只是把使用者*帶到*那一格，沒有代替他選。使用者接著在那一格
  // 按釘選，那是他自己的明確動作，符合判準 1「同時明確選了 session 與 pane」。
  // 所以：**approval 單獨發生 → durable 不動；使用者動手 → durable 才動。**
  //
  // 實作前逐條查過四條路徑，沒有發現矛盾：
  //   (a) restoreLayout 走 persist=false，pin() 不碰 durable，焦點權威仍是
  //       registry 的 layout.focused；
  //   (b) resolveApprovalPresentation 還原成 durable(=1)，停在使用者剛選的那格，自洽；
  //   (c) 來源未釘選的 transient 分支根本不移動焦點，釘選落在使用者自己那格；
  //   (d) 要釘進正顯示 transient view 的那一格，得先點它（setFocus，本身就是明確選擇）。
  //
  // mutation：pin() 不更新 durableFocusPane → 紅在 durable 斷言與跨重啟那行。
  it('交互：approval 帶到第 2 格後使用者按釘選，durable focus 跟著移動並跨重啟', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'), session('w3', 'claude', 'gamma'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const first = await mountApp()
    runtimeMocks.handlers['approval:request']!({
      id: 'a1', wsid: 'w2', provider: 'codex', toolName: 'bash', inputJson: '{}',
    })
    await flushPromises()

    await first.find('[data-test=pin-w3]').trigger('click')
    await flushPromises()
    expect(useSession().durableFocusPane, '使用者的釘選動作才是明確選擇').toBe(1)

    const [pins, focused] = setLayoutCalls().at(-1)!
    first.unmount()
    resetEnv()
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w3', 'claude', 'gamma'),
    ])
    appMocks.state.layout = { pins, focused }
    const second = await mountApp()
    expect(useSession().focused, '重啟後停在使用者釘進去的那一格').toBe(1)
    expect(useSession().durableFocusPane).toBe(1)
  })

  // ---- 判準 4 的對照組：approval 單獨發生，durable 完全不動 ----
  //
  // mutation：
  //   - routeApproval 同時寫 durableFocusPane → 紅在「核可後焦點回到使用者那一格」。
  //   - resolveApprovalPresentation 拿掉焦點還原（或放到 transientPane 早退之後）
  //     → 紅在同一行。
  it('對照組：approval 期間使用者什麼都不做，核可後焦點回到原本那一格且全程零寫入', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()
    const s = useSession()

    runtimeMocks.handlers['approval:request']!({
      id: 'a1', wsid: 'w2', provider: 'codex', toolName: 'bash', inputJson: '{}',
    })
    await flushPromises()
    expect(s.focused, '前提：畫面焦點確實被帶走了').toBe(1)
    expect(s.durableFocusPane, 'approval 本身不得改動 durable focus').toBe(0)

    await w.find('.dialog .allow').trigger('click')
    await flushPromises()

    expect(s.focused, '核可後焦點回到使用者那一格').toBe(0)
    expect(s.durableFocusPane).toBe(0)
    expect(setLayoutCalls(), 'approval 全程不得產生任何 durable 寫入').toHaveLength(0)
  })

  // ---- 判準 4：approval 期間的**非 pane 選擇**寫入不得把 durable focus 帶走 ----
  //
  // 移除 session 是 session 清單的操作，不是 pane 選擇，所以它觸發的 persistLayout
  // 必須仍以使用者選定的那一格為準。這條是 `persistLayout` 送 durableFocusPane
  // 而非 focused 的唯一守門（釘選與切焦點兩條路徑下兩者相等，分辨不出來）。
  //
  // mutation：
  //   - persistLayout 送 this.focused → 紅（寫出 '' —— 使用者的焦點就這樣沒了）。
  //   - routeApproval 同時寫 durableFocusPane → 紅在同一行。
  it('判準 4：approval 期間移除 session（非 pane 選擇）不得把 durable focus 帶走', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()

    runtimeMocks.handlers['approval:request']!({
      id: 'a1', wsid: 'w2', provider: 'codex', toolName: 'bash', inputJson: '{}',
    })
    await flushPromises()
    expect(useSession().focused, '前提：畫面焦點在第 2 格').toBe(1)

    // 移除第 2 格那個 session（走 SessionList 兩段式移除，完全不碰 pane 選擇）
    await w.find('[data-test=remove-w2]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flushPromises()

    const [pins, focused] = setLayoutCalls().at(-1)!
    expect(pins).toEqual(['w1', ''])
    expect(focused, 'durable focus 必須仍是使用者選的第 1 格').toBe('w1')
  })

  // ---- 判準 3：已是相同 pins 與 focus → 送出的 layout 逐字相同，由 store 冪等守衛攔下 ----
  //
  // 分工說清楚：**「零落盤」是 store 層的事**（`wsregistry` 的
  // `TestSetLayoutSkipsPersistWhenUnchanged` 以步驟計數＋`os.SameFile`／mtime 驗證）。
  // 這條驗的是它的前提——前端在「釘一個已經在該格、且該格已 focused 的 session」時，
  // 送出的 tuple 與上一次**逐字相同**，守衛才有東西可攔。
  //
  // 對照組刻意放在同一條：切到另一格之後送出的 tuple **必須不同**（pins 相同但
  // focus 不同 → 那是該落盤的，不能被冪等守衛吃掉）。
  //
  // mutation（實測）：
  //   - `setFocus` 不更新 durableFocusPane → 紅在對照組（`focus 變了就必須送出不同
  //     的 layout`）。
  //   - persistLayout 永遠送 `pins[0]`（忽略 focus 維度）→ 紅在同一行。
  //   **前半（逐字相同）沒有對應的 mutation**，據實記錄：它是 store 冪等守衛的
  //   *前提檢查*（前端必須送得出一模一樣的 tuple，守衛才有東西可攔），不是行為
  //   分歧點——真正把「零落盤」釘死的是 wsregistry 那條。
  it('判準 3：重複釘選同一格送出逐字相同的 layout；切格則不同', async () => {
    appMocks.ListSessions.mockImplementation(async () => [
      session('w1', 'claude', 'alpha'), session('w2', 'codex', 'beta'),
    ])
    appMocks.state.layout = { pins: ['w1', 'w2'], focused: 'w1' }
    const w = await mountApp()

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()
    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()

    const calls = setLayoutCalls()
    expect(calls.length).toBeGreaterThanOrEqual(2)
    expect(calls.at(-1), '相同 pins ＋ 相同 focus 必須送出逐字相同的 layout').toEqual(calls.at(-2))
    expect(calls.at(-1)).toEqual([['w1', 'w2'], 'w1'])

    // 對照組：pins 相同、focus 不同 → tuple 必須改變（該落盤，不得被守衛吃掉）
    await w.find('[data-test=pane-1]').trigger('click')
    await flushPromises()
    expect(setLayoutCalls().at(-1), 'focus 變了就必須送出不同的 layout').toEqual([['w1', 'w2'], 'w2'])
  })
})
