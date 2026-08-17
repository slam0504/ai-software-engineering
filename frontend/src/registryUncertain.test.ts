import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// wailsjs 綁定全部 mock。這裡同時掛 SessionList 與 SettingsBar 用到的 export：
// 兩個元件 import 的是同一個模組路徑，vitest 依**解析後的絕對路徑**攔截，所以
// 一份 factory 要涵蓋兩邊的具名 export，少一個會在 mount 時 undefined。
const mocks = vi.hoisted(() => ({
  CreateSession: vi.fn(async () => 'w-new'),
  RemoveSession: vi.fn(async () => undefined),
  NewSession: vi.fn(async () => undefined),
  TerminateSession: vi.fn(), AuthStatus: vi.fn(), StartLogin: vi.fn(),
  CancelLogin: vi.fn(), Logout: vi.fn(), RestartCodexServerRecorded: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => mocks)

import SessionList from './components/SessionList.vue'
import SettingsBar from './components/SettingsBar.vue'
import Timeline from './components/Timeline.vue'
import { routeEnvelope } from './lib/gateRouting'
import { useSession } from './stores/session'
import type { Envelope } from './types'
import { mountWithI18n } from './test/i18n'

// ---- M3b Task 2a rev2：registry uncertain latch 的**使用者可見出口**（矩陣 A）----
//
// 這個檔案守的是 owner rev2 追問的那一格：latch 的訊號**真的到得了使用者嗎**。
// 「backend 有呼叫 emit」不算數（失效形狀 (C)：保證只寫在註解裡）——每一條都
// 斷言 Timeline 實際渲染出來的 DOM 文字，且文字裡要有使用者看得懂的下一步。
//
// 兩類入口分開守：
//  1. **背景路徑**（沒有同步呼叫端可以回錯）：late claude init 的 SetResume →
//     noteRegistryWriteResult → Manager.EmitWorkspace。這是 rev2 之前真正的缺口。
//  2. **同步拒絕**：Create／Start／New／Remove 在 latch 期間回 errRegistryUncertain，
//     由各自的呼叫端 catch → store → Timeline。

// GO_ERROR：app.go errRegistryUncertain 的實際字串（逐字對照，不簡化）。
// 用真的那一串而不是 'boom'，因為這組測試要證明的正是「使用者看得懂下一步」，
// 而下一步（重啟 app）就寫在這串裡。
const GO_ERROR =
  'app: session registry 上一次寫入的結果不確定，建立／移除／開始對話／開新對話已停用；' +
  '請重啟 app（重啟後 registry 以磁碟上的 workspace-sessions.json 為準重新載入）：' +
  'wsregistry: registry 上一次寫入的 commit 結果不確定（檔案已 rename 但 directory sync 失敗）'

// backgroundNotice：Manager.EmitWorkspace 實際送出的形狀——scope='workspace'、
// 訊息只在 payload、頂層 error 是 omitempty 且**從未被填**（同 Timeline.test.ts
// 的既有 fixture 慣例）。
const backgroundNotice: Envelope = {
  event_id: 'ru1', ts: 't', provider: '', scope: 'workspace', kind: 'stream_error',
  payload: { component: 'session-registry', wsid: 'w1', op: 'set_resume', error: GO_ERROR },
}

// legacyUnroutable：rev2 之前的形狀——session lane、**不帶 workspace_session_id**。
// 保留成 fixture 是為了讓迴歸看得見：這個形狀進不了任何 lane。
const legacyUnroutable: Envelope = {
  event_id: 'ru0', ts: 't', provider: 'claude', kind: 'stream_error', error: GO_ERROR,
}

// dispatch：完整重現 App.vue onMounted 裡 EventsOn('workbench:event') 的分流，
// 不讓測試直接呼叫 applyNotice——直接呼叫等於跳過 routeEnvelope，正是缺口所在
// 的那一格（失效形狀 (D)：時序讓受測路徑沒被走到）。
function dispatch(env: Envelope) {
  const s = useSession()
  const dst = routeEnvelope(env)
  if (dst === 'notice') s.applyNotice(env)
  else if (dst === 'session') s.apply(env)
  else throw new Error(`本檔不預期路由到 ${dst}`)
}

function timelineText() {
  return mountWithI18n(Timeline).findAll('.sum').map(n => n.text()).join('\n')
}

// flush：元件內 async handler（await binding → catch → store）需要讓出兩輪
// microtask 才會走完。刻意不用 setTimeout／sleep（barrier 測試不得依賴時間）。
async function flush(w: any) {
  await Promise.resolve()
  await Promise.resolve()
  await w.vm.$nextTick()
}

describe('registry uncertain latch：背景失敗必須有使用者可見出口', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockClear()
  })

  // mutation：app.go 的 noteRegistryWriteResult 換回舊的 a.emit(session-scope、
  // 無 WSID) 形狀 → routeEnvelope 回 'session'、apply() 只 unrouted++ →
  // 紅在「latch 訊息必須渲染出來」。
  it('workspace lane 的 latch 通知渲染成使用者看得懂的訊息與下一步', () => {
    dispatch(backgroundNotice)
    const txt = timelineText()
    expect(txt).toContain('session-registry')
    expect(txt).toContain('請重啟 app')
    expect(txt).toContain('已停用')
    expect(txt).not.toBe('stream_error') // 落到 `return e.kind` 就是這個值
  })

  // 「正確的 pane」：latch 是 registry **全域**的事實，不屬於任何單一 session，
  // 所以正確答案是「不論使用者正在看哪一個 pane 都看得到」。notices 被合併進
  // 任何 focused pane 的 timeline，這條就是它的守門。
  //
  // mutation：把通知改成走某個 WSID 的 session lane（apply 帶 workspace_session_id）
  // → 只有那個 pane 看得到，紅在另一個 pane 的斷言。
  it('不論 focus 在哪一個 pane 都看得到（latch 是 registry 全域事實）', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1')
    s.pin(1, 'w2')
    dispatch(backgroundNotice)

    s.setFocus(0)
    expect(timelineText()).toContain('請重啟 app')
    s.setFocus(1)
    expect(timelineText()).toContain('請重啟 app')
  })

  // 迴歸守門：舊形狀到不了使用者。這條**必須維持綠**——它斷言的是「那個形狀
  // 確實會被丟掉」，也就是為什麼 rev2 非改不可。
  //
  // mutation：把 apply() 對空 WSID 的 unrouted++ 改成 applyNotice → 紅在
  // 「不得落進 notices」（那會讓所有無主事件都變成通知，不是這次要的修法）。
  it('迴歸：session lane ＋空 WSID 的舊形狀確實到不了使用者（unrouted 無渲染端）', () => {
    const s = useSession()
    dispatch(legacyUnroutable)
    expect(s.unrouted).toBe(1)
    expect(s.notices).toHaveLength(0)
    expect(timelineText()).not.toContain('請重啟 app')
  })
})

describe('registry uncertain latch：同步拒絕的使用者可見出口（矩陣 A）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockClear()
  })

  // CreateSession：SessionList 的建立按鈕。
  // mutation：拿掉 SessionList.createSession 的 catch → 未捕捉的 rejection，
  // 紅在「建立失敗必須渲染」。
  it('CreateSession 被拒絕 → 清單不新增，錯誤原文與下一步渲染出來', async () => {
    mocks.CreateSession.mockRejectedValueOnce(new Error(GO_ERROR))
    useSession()
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=create-claude]').trigger('click')
    await flush(w)

    expect(w.findAll('[data-test=session-card]')).toHaveLength(0)
    const txt = timelineText()
    expect(txt).toContain('建立 claude session 失敗')
    expect(txt).toContain('請重啟 app')
  })

  // RemoveSession：SessionList 的二段式移除。
  // mutation：失敗時仍呼叫 markRemoved → 卡片消失，紅在「卡片不得消失」。
  it('RemoveSession 被拒絕 → 卡片不消失，錯誤原文與下一步渲染出來', async () => {
    mocks.RemoveSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    const w = mountWithI18n(SessionList)
    await w.find('[data-test=remove-w1]').trigger('click')
    await w.find('[data-test=remove-confirm-submit]').trigger('click')
    await flush(w)

    expect(s.sessions['w1'].removed).toBe(false)
    expect(w.findAll('[data-test=session-card]')).toHaveLength(1)
    expect(timelineText()).toContain('請重啟 app')
  })

  // NewSession：SettingsBar 的「開新對話」。call() 失敗時**不**呼叫 s.reset()，
  // 所以 transcript 不得被清掉——否則畫面看起來像已經開了新對話。
  // mutation：把 call() 的 catch 改成靜默 return → 紅在「必須渲染」。
  // mutation：把 s.reset() 移到 try 之外（無條件執行）→ 紅在「transcript 不得被清」。
  it('NewSession 被拒絕 → transcript 不被重設，錯誤原文與下一步渲染出來', async () => {
    mocks.NewSession.mockRejectedValueOnce(new Error(GO_ERROR))
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.pin(0, 'w1')
    s.setFocus(0)
    s.apply({ event_id: 'e1', ts: 't', provider: 'claude', kind: 'message',
      role: 'assistant', text: '之前的回覆', workspace_session_id: 'w1' })

    const w = mountWithI18n(SettingsBar)
    const btn = w.findAll('button').find(b => b.text() === '開新對話')
    expect(btn, '找不到「開新對話」按鈕').toBeTruthy()
    await btn!.trigger('click')
    await flush(w)

    expect(s.chat.map(c => c.text)).toContain('之前的回覆')
    const txt = timelineText()
    expect(txt).toContain('開新對話失敗')
    expect(txt).toContain('請重啟 app')
  })

  // StartSession：composer 送出（store.submit 的第一輪）。錯誤進**該 WSID 自己的**
  // timeline，而不是 workspace notices——這一條是同步拒絕，使用者知道是哪個
  // session 送不出去。
  // mutation：submit 的 catch 改成靜默 → 紅在「必須渲染」。
  // mutation：pushError 改成不解除 busy → 紅在「busy 必須解除」（否則輸入框永遠鎖住）。
  it('StartSession 被拒絕 → 該 session 的 timeline 顯示錯誤，busy 解除', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.pin(0, 'w1')
    s.setFocus(0)
    s.setBindings({ StartSession: vi.fn(async () => { throw new Error(GO_ERROR) }) } as any)

    await s.submit('go')

    expect(s.busy).toBe(false)
    const txt = timelineText()
    expect(txt).toContain('請重啟 app')
    expect(txt).toContain('已停用')
  })
})
