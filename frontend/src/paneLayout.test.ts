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

function timelineText(w: any) {
  return w.findAll('.tl .sum').map((n: any) => n.text()).join('\n')
}

describe('§3.8 啟動重建兩個釘選 pane：registry 的排列是唯一輸入', () => {
  beforeEach(resetEnv)

  // 這條守的是缺陷本身：App.vue 的 onMounted 過去只做 hydrateSessions，pins 恆為
  // [null, null]，重啟後兩個 pane 都是空的。
  //
  // mutation（實測結果，據實記錄）：
  //   - 拿掉 App.vue onMounted 的 restoreLayout 呼叫 → 紅在本條 150 行（pane 內容）。
  //     這一刀**同時打紅 5 條**（本條、跳過未知 WSID、切焦點寫入、跨重啟、transient）
  //     ——「還原有沒有接上」是整組讀取端測試的共同前提，不宣稱它只紅一條。
  //   - restoreLayout 不還原 focused → 紅在 155 行（另紅跨重啟那條的 274 行）。
  //   - restoreLayout 只設 pins、不走 pin() → **只**紅在 157 行（LoadTurnsBefore），
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
  // 紅在本條 173 行（另紅「點另一個 pane 切焦點」那條的 231 行前提斷言）。
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
  // 187 行（pins 變成 ['ghost', 'w2']，pane-0 渲染出一個沒有 provider 的頭列）。
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
  // mutation：拿掉 App.vue 的 try/catch → **只**紅在本條 201／202 行（未捕捉的
  // rejection 讓 onMounted 後續的 refreshGate／refreshEscalation 不再執行）。
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
  // mutation：拿掉 store pin() 的 `void this.persistLayout()` → 紅在本條 219 行
  //（另紅跨重啟 273、latch 不擋路 360——三條都以「釘選會寫入」為前提）。
  it('點釘選鈕會把排列寫進 registry（帶 focused pane）', async () => {
    appMocks.ListSessions.mockImplementation(async () => [session('w1', 'claude', 'alpha')])
    const w = await mountApp()

    await w.find('[data-test=pin-w1]').trigger('click')
    await flushPromises()

    expect(setLayoutCalls().at(-1)).toEqual([['w1', ''], 'w1'])
  })

  // 切焦點同樣是 §3.2.1 白名單的一部分（focused pane）。
  // mutation：拿掉 setFocus 的 persistLayout → 紅在本條 236 行（另紅 transient
  // 332、非 focus pane 可見性 382——那兩條都用切焦點當寫入觸發器）。
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
  //   - 拿掉 pin() 的 persistLayout → 紅在本條 273 行（重啟後 pane-0 是空的）。
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
  //   - markRemoved 拿掉 `if (unpinned) void this.persistLayout()` → 紅在寫入斷言。
  //   - 把該條件改成無條件寫入 → 紅在最後一行（移除未釘選的 session 不得寫）。
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

  // §3.6.4：persistent pin 永不被 transient view 改寫——durable 層的落點。
  // 走 production 路徑：ApprovalDialog 自己註冊的 `approval:request` handler。
  //
  // mutation：persistLayout 改送 `this.pins`（而不是 persistentPins）→ **只**紅在
  // 本條 332 行（寫出去的第二格會是 w3）；上面那條跨重啟測試實測仍綠——這正是它
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
  //   - persistLayout 的 catch 改成清掉 pins（回滾）→ **只**紅在本條 358 行。
  //   - 把 catch 換成靜默 `.catch(() => {})` → 紅在 360 行（通知不見了；另紅下一
  //     條的 382）。
  //   - catch 改用 pushError 而非 pushNotice → 本條實測**仍綠**（focused pane 剛好
  //     是 w1，看得到），由下一條「非 focus pane 也看得到」打紅。
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

  // pushNotice 而非 pushError 的理由：排列失敗不屬於任何單一 session。走
  // pane-scoped 出口的話，訊息會寫進 `views[focusedWsid]`，而使用者可能正在看
  // 另一個 pane。
  //
  // mutation：persistLayout 的 catch 改成 `this.pushError(...)` → **只**紅在本條
  // 382 行（訊息落進 w1 的 view，focus 在 w2 時 timeline getter 讀不到）。
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
})
