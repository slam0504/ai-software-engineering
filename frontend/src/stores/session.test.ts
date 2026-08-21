import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSession } from './session'
import type { Envelope } from '../types'

// env：所有 conversation lane 事件都帶 workspace_session_id（§3.1.5 之後為必填）。
const env = (wsid: string, over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta',
  workspace_session_id: wsid, ...over,
})

const okBindings = () => ({
  StartSession: vi.fn(async () => {}),
  SendMessage: vi.fn(async () => {}),
})

// 一個已註冊＋釘選在 pane 0 並取得焦點的 session（多數測試的起點）。
function focusedSession(wsid = 'w1', provider: 'claude' | 'codex' = 'claude') {
  const s = useSession()
  s.registerSession({ wsid, provider, taskLabel: 'a' })
  s.pin(0, wsid)
  s.setFocus(0)
  return s
}

describe('session store：per-WSID lane 路由（§3.7-8）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('apply 依 workspace_session_id 路由，不再依 provider', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2')
    s.apply(env('w2', { kind: 'delta', text: 'hello' }))
    expect(s.views['w2'].chat.at(-1)?.text).toBe('hello')
    expect(s.views['w1'].chat).toHaveLength(0)
  })

  it('busy／unread 只在 SessionMeta 上，views 不承載狀態', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'message', role: 'user' }))
    expect(s.sessions['w1'].busy).toBe(true)
    expect((s.views['w1'] as unknown as Record<string, unknown>).busy).toBeUndefined()
    expect((s.views['w1'] as unknown as Record<string, unknown>).unread).toBeUndefined()
    s.apply(env('w1', { kind: 'result' }))
    expect(s.sessions['w1'].busy).toBe(false)
  })

  it('非釘選 session 只更新 metadata 與 unread，不保留 transcript', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w3', provider: 'codex', taskLabel: 'c' })
    s.apply(env('w3', { provider: 'codex', kind: 'result' }))
    expect(s.sessions['w3'].unread).toBe(1)
    expect(s.views['w3']).toBeUndefined()
    s.apply(env('w3', { provider: 'codex', kind: 'state_change', state: 'awaiting_approval' }))
    expect(s.sessions['w3'].state).toBe('awaiting_approval')
    expect(s.sessions['w3'].awaitingApproval).toBe(true)
  })

  it('釘選中的 session 不計 unread（兩 pane 都看得見）', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    s.apply(env('w2', { kind: 'result' })) // 背景 pane 但仍可見
    expect(s.sessions['w2'].unread).toBe(0)
  })

  it('解除釘選釋放 transcript，保留 metadata、已讀與捲動錨點', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'delta', text: 'x' }))
    s.setScrollAnchor('w1', 'e1')
    s.unpin(0)
    expect(s.views['w1']).toBeUndefined()
    expect(s.sessions['w1'].unread).toBe(0)
    expect(s.sessions['w1'].taskLabel).toBe('a')
    expect(s.scrollAnchors['w1']).toBe('e1')
  })

  it('釘選時 unread 歸零', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w3', provider: 'codex', taskLabel: 'c' })
    s.apply(env('w3', { provider: 'codex', kind: 'result' }))
    expect(s.sessions['w3'].unread).toBe(1)
    s.pin(1, 'w3')
    expect(s.sessions['w3'].unread).toBe(0)
  })

  it('未知 WSID 的事件自動登記，不靜默丟棄', () => {
    const s = useSession()
    s.apply(env('w-surprise', { provider: 'codex', kind: 'result' }))
    expect(s.sessions['w-surprise']).toMatchObject({ provider: 'codex', unread: 1 })
  })

  it('沒有 WSID 的 conversation 事件計入 unrouted，不落到任何 lane', () => {
    const s = focusedSession()
    s.apply({ event_id: 'e0', ts: 't', provider: 'claude', kind: 'delta', text: 'orphan' })
    expect(s.unrouted).toBe(1)
    expect(s.views['w1'].chat).toHaveLength(0)
  })

  it('registerSession 不覆寫既有 runtime 狀態', () => {
    const s = useSession()
    s.apply(env('w9', { provider: 'codex', kind: 'result' })) // 先由事件建出 metadata
    s.registerSession({ wsid: 'w9', provider: 'codex', taskLabel: 'from-registry' })
    expect(s.sessions['w9'].unread).toBe(1) // registry hydrate 不得清掉 unread
    expect(s.sessions['w9'].taskLabel).toBe('from-registry')
  })
})

describe('session store：approval 路由（§3.6.4）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // 六種凍結觸發（allow／deny／timeout／dismiss／remove／shutdown）在 store 這一層
  // 行為完全相同，因此這裡驗的是「反覆進出 transient 都能回到原釘選」（冪等 ＋
  // 不累積殘留）。**六種觸發各自真的都會呼叫到這裡**，由 ApprovalDialog.test.ts
  // 的六條情境測試（按鈕 allow／deny ＋ dismiss 事件的四種 cause／reason）守住。
  it('未釘選來源以 transient 顯示，反覆進出都恢復原釘選', () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    for (let i = 0; i < 6; i++) {
      s.routeApproval('w3')
      expect(s.pins[1]).toBe('w3')
      expect(s.persistentPins[1]).toBe('w2')
      expect(s.focused).toBe(0) // transient 不搶走 composer 焦點
      s.resolveApprovalPresentation()
      expect(s.pins[1]).toBe('w2')
      expect(s.views['w3']).toBeUndefined() // transient transcript 一併釋放
    }
  })

  it('來源在另一 pane 時自動切 focus', () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    s.routeApproval('w2')
    expect(s.focused).toBe(1)
    expect(s.pins).toEqual(['w1', 'w2']) // 已釘選：不動 pin
  })

  it('來源就是 focused pane 時不動 pin 也不動 focus', () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(1)
    s.routeApproval('w2')
    expect(s.focused).toBe(1)
    expect(s.pins).toEqual(['w1', 'w2'])
  })

  it('transient 期間第二筆 approval 不覆蓋原釘選的備份', () => {
    const s = useSession()
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    s.routeApproval('w3')
    s.routeApproval('w4') // FIFO promotion：下一筆同樣未釘選
    expect(s.pins[1]).toBe('w4')
    expect(s.persistentPins[1]).toBe('w2') // 不得被 w3 覆蓋
    s.resolveApprovalPresentation()
    expect(s.pins[1]).toBe('w2')
  })
})

describe('session store：focused pane 操作（§3.7）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // pin() 的 action 契約（owner 2026-08-17 追加裁決）：「釘進第 N 格」＝同時明確
  // 選了 session 與 pane，所以第 N 格必須成為作用中的那一格。
  //
  // **為什麼要一條直接呼叫的單元測試**：`this.focused = idx` 對目前**所有**
  // production 呼叫端都是 no-op（SessionList／createSession 都是
  // `s.pin(s.focused, wsid)`，idx 恆等於 focused），拿掉它整套元件測試照樣綠。
  // 但 pin() 是 store 的公開 action，這個語意不能靠「目前呼叫端剛好都已經 focus
  // 在該格」這個外部巧合成立——日後任何「釘進另一格」的入口（拖放、右鍵選單、
  // 快捷鍵）一加就會踩到。這條刻意直接呼叫 action，就是要在沒有 UI 入口的情況下
  // 也守得住它。
  //
  // mutation：拿掉 pin() 的 `this.focused = idx` → 紅在這裡（元件測試全綠）。
  it('pin(idx) 讓第 idx 格成為作用中那一格（durable 與畫面焦點同時移動）', async () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    await s.pin(0, 'w1')
    expect(s.focused).toBe(0)

    await s.pin(1, 'w2') // 釘進**另一**格：production 目前沒有這種呼叫端
    expect(s.focused, '釘進第 2 格 → 第 2 格作用中').toBe(1)
    expect(s.durableFocusPane, 'durable 焦點同步').toBe(1)
    expect(s.focusedWsid).toBe('w2')
  })

  it('submit 作用於 focused pane 的 WSID', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(1)
    await s.submit('go')
    expect(b.StartSession).toHaveBeenCalledWith('w2', 'go', '', '', 'b', 'untrusted')
    expect(b.SendMessage).not.toHaveBeenCalled()
  })

  it('第二輪走 SendMessage 且仍帶 WSID', async () => {
    const s = focusedSession()
    const b = okBindings()
    s.setBindings(b)
    await s.submit('one')
    s.apply(env('w1', { kind: 'result' }))
    await s.submit('two')
    expect(b.SendMessage).toHaveBeenCalledWith('w1', 'two')
  })

  it('A busy 不擋 B 送出', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    await s.submit('claude go')
    expect(s.sessions['w1'].busy).toBe(true)
    s.setFocus(1)
    expect(s.busy).toBe(false)
    await s.submit('other go')
    expect(b.StartSession).toHaveBeenCalledTimes(2)
    expect(s.sessions['w2'].busy).toBe(true)
  })

  it('submit 失敗推錯誤並解除 busy，不本地新增 user 氣泡', async () => {
    const s = focusedSession()
    s.setBindings({
      StartSession: vi.fn(async () => { throw new Error('busy') }),
      SendMessage: vi.fn(async () => {}),
    })
    await s.submit('x')
    expect(s.sessions['w1'].busy).toBe(false)
    expect(s.chat).toHaveLength(0)
    expect(s.timeline.at(-1)!.env.error).toContain('busy')
  })

  it('沒有 focused session 時 submit 不呼叫綁定', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    await s.submit('nowhere')
    expect(b.StartSession).not.toHaveBeenCalled()
    expect(s.notices.at(-1)!.env.kind).toBe('stream_error')
  })

  it('輸入欄位 per session 隔離', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: '' })
    s.pin(0, 'w1'); s.pin(1, 'w2')
    s.setFocus(0); s.setTaskLabel('task-c'); s.setRecordCase('rec-c')
    s.setFocus(1); s.setTaskLabel('task-x')
    expect(s.recordCase).toBe('')
    await s.submit('go x')
    expect(b.StartSession).toHaveBeenCalledWith('w2', 'go x', '', '', 'task-x', 'untrusted')
    s.setFocus(0)
    expect(s.taskLabel).toBe('task-c')
    expect(s.recordCase).toBe('rec-c')
  })

  it('applyDone 依 payload 的 wsid 路由', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'a' })
    s.registerSession({ wsid: 'w2', provider: 'codex', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    s.sessions['w2'].busy = true
    s.sessions['w2'].active = true
    s.applyDone({ wsid: 'w2', provider: 'codex', processStillRunning: true })
    expect(s.sessions['w2'].busy).toBe(false)
    expect(s.sessions['w2'].active).toBe(false)
    expect(s.views['w2'].timeline.at(-1)!.env.kind).toBe('session_done')
    expect(s.views['w1'].timeline).toHaveLength(0)
  })

  it('reset 只清 focused pane 的 transcript，保留輸入欄位', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: 'keep-me' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: 'b' })
    s.pin(0, 'w1'); s.pin(1, 'w2'); s.setFocus(0)
    s.apply(env('w1', { kind: 'message', role: 'user', text: 'c' }))
    s.apply(env('w2', { kind: 'message', role: 'user', text: 'x' }))
    s.reset()
    expect(s.chat).toHaveLength(0)
    expect(s.taskLabel).toBe('keep-me')
    expect(s.sessions['w1'].active).toBe(false)
    expect(s.views['w2'].chat).toHaveLength(1)
  })

  it('markRemoved 釋放 pane 與 transcript，metadata 留下 tombstone 標記', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'delta', text: 'x' }))
    s.markRemoved('w1')
    expect(s.pins[0]).toBeNull()
    expect(s.views['w1']).toBeUndefined()
    expect(s.sessions['w1'].removed).toBe(true)
    expect(s.sessionList.map(m => m.wsid)).not.toContain('w1')
  })
})

describe('session store：reducer 行為（M1 契約，改 per-WSID 後不變）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('user 氣泡只由 host envelope 產生', () => {
    const s = focusedSession()
    expect(s.chat).toHaveLength(0)
    s.apply(env('w1', { kind: 'message', role: 'user', text: 'hi' }))
    expect(s.chat[0]).toMatchObject({ role: 'user', text: 'hi' })
  })

  it('tool 角色只進 timeline 不進 chat', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'message', role: 'tool', text: 'tool result echo' }))
    expect(s.chat).toHaveLength(0)
    expect(s.timeline.at(-1)!.env.text).toBe('tool result echo')
  })

  it('delta 累積後由 assistant message 收尾', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'delta', text: 'a', thinking: 'th1-' }))
    s.apply(env('w1', { kind: 'delta', text: 'b', thinking: 'th2' }))
    expect(s.chat.at(-1)).toMatchObject({ role: 'assistant', text: 'ab', thinking: 'th1-th2', streaming: true })
    s.apply(env('w1', { kind: 'message', role: 'assistant', text: 'ab!' }))
    expect(s.chat.at(-1)).toMatchObject({ text: 'ab!', thinking: 'th1-th2', streaming: false })
  })

  it('result 收掉尾端 streaming 氣泡', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'delta', text: 'hi' }))
    s.apply(env('w1', { kind: 'message', role: 'assistant', text: 'hi' }))
    s.apply(env('w1', { kind: 'delta', text: '' }))
    s.apply(env('w1', { kind: 'result' }))
    expect(s.chat).toHaveLength(1)
    expect(s.chat.at(-1)).toMatchObject({ text: 'hi', streaming: false })
  })

  it('usage 為 snapshot 覆寫、cost 累加', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 } }))
    s.apply(env('w1', { kind: 'usage', usage: { input_tokens: 15, output_tokens: 3 } }))
    expect(s.totals.input).toBe(15)
    s.apply(env('w1', { kind: 'result', cost_usd: 0.25, usage: { input_tokens: 20, output_tokens: 5 } }))
    expect(s.totals.input).toBe(20)
    expect(s.totals.cost).toBeCloseTo(0.25)
    expect(s.costDisplay).toBe('$0.2500')
  })

  it('identity／state 追蹤在 SessionMeta 上', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'init', session_id: 's1', task_id: 'task-1' }))
    s.apply(env('w1', { kind: 'state_change', state: 'waiting' }))
    expect(s.sessionId).toBe('s1')
    expect(s.taskId).toBe('task-1')
    expect(s.state).toBe('waiting')
    expect(s.sessions['w1'].resume).toBe('s1') // 續聊自動接續
  })

  it('連續系統雜訊在 timeline 併成一組', () => {
    const s = focusedSession()
    s.apply(env('w1', { kind: 'system_other' }))
    s.apply(env('w1', { kind: 'system_other' }))
    s.apply(env('w1', { kind: 'unknown' }))
    s.apply(env('w1', { kind: 'tool_use', text: 'Bash' }))
    const groups = new Set(s.timeline.map(i => i.group).filter(g => g !== undefined))
    expect(groups.size).toBe(1)
    expect(s.timeline.at(-1)!.group).toBeUndefined()
  })
})

describe('session store：workspace 通知（degraded 到得了使用者）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // fixture 用 production 實際的形狀：Manager.EmitWorkspace 只填 Payload，
  // 頂層 Envelope.Error 是 omitempty 且從未被它填過。用頂層 error 當 fixture 會
  // 遮住「訊息內容到不了使用者」那半（見 Timeline.test.ts）。
  const workspaceErr = (id: string, error: string) => ({
    event_id: id, ts: 't', provider: '', kind: 'stream_error',
    scope: 'workspace', payload: { error },
  })

  it('applyNotice 進 notices 並出現在 focused timeline', () => {
    const s = focusedSession()
    s.applyNotice(workspaceErr('wn1', 'replay index degraded'))
    expect(s.notices).toHaveLength(1)
    expect(s.timeline.map(i => i.env.event_id)).toContain('wn1')
  })

  it('workspace 通知不落進任何 session lane', () => {
    const s = focusedSession()
    // 頂層 provider 空字串＝EmitWorkspace 的實際輸出（provider 只在 payload 裡）
    s.applyNotice({ event_id: 'wn1', ts: 't', provider: '', kind: 'codex_broadcast',
      scope: 'workspace', payload: { provider: 'codex', method: 'account/updated' } })
    expect(s.views['w1'].timeline).toHaveLength(0)
    expect(s.views['w1'].chat).toHaveLength(0)
  })

  it('沒有釘選 pane 時通知仍看得見', () => {
    const s = useSession()
    s.applyNotice(workspaceErr('wn1', 'boom'))
    expect(s.timeline.map(i => i.env.event_id)).toContain('wn1')
  })
})

describe('session store：registry hydrate（registry × working slot）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('hydrateSessions 帶入 durable metadata 與可操作性', () => {
    const s = useSession()
    s.hydrateSessions([
      { wsid: 'w1', provider: 'claude', task_label: 'a', resume_session_id: 's1', created_at: '', available: true, state: 'idle' },
      { wsid: 'w2', provider: 'codex', task_label: 'b', resume_session_id: '', created_at: '', available: false, state: '' },
    ])
    expect(s.sessions['w1']).toMatchObject({ provider: 'claude', taskLabel: 'a', resume: 's1', available: true })
    expect(s.sessions['w2'].available).toBe(false)
    expect(s.sessionList.map(m => m.wsid)).toEqual(['w1', 'w2'])
    // state 必須帶進來：SessionList／StatusBar 顯示的就是它，丟掉會讓重啟後每個
    // session 都顯示成 newMeta 的預設值。值域是 reducer 的 SessionState
    // （idle／waiting／streaming／tool_running／awaiting_approval／…），與
    // conversation lane 的 state_change 同源——**不是** Manager 內部的 slot
    // phase（starting／active／ending，沒有對外出口，production 不會出現在這裡）。
    expect(s.sessions['w1'].state).toBe('idle')
    expect(s.sessions['w2'].state).toBe('') // 無 slot：不假造狀態
  })

  it('hydrateSessions 帶入非 idle 的 reducer 狀態', () => {
    const s = useSession()
    s.hydrateSessions([
      { wsid: 'w1', provider: 'claude', task_label: 'a', resume_session_id: '', created_at: '', available: true, state: 'awaiting_approval' },
    ])
    expect(s.sessions['w1'].state).toBe('awaiting_approval')
  })

  it('不可操作的 session 不得被 submit', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    s.hydrateSessions([
      { wsid: 'w2', provider: 'codex', task_label: 'b', resume_session_id: '', created_at: '', available: false, state: '' },
    ])
    s.pin(0, 'w2'); s.setFocus(0)
    await s.submit('go')
    expect(b.StartSession).not.toHaveBeenCalled()
    expect(s.notices.at(-1)!.env.error).toContain('w2')
  })

  it('countOf 依 provider 計數且不含已移除', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'w2', provider: 'claude', taskLabel: '' })
    s.registerSession({ wsid: 'w3', provider: 'codex', taskLabel: '' })
    s.markRemoved('w2')
    expect(s.countOf('claude')).toBe(1)
    expect(s.countOf('codex')).toBe(1)
  })
})
