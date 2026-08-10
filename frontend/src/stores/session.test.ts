import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSession } from './session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta', ...over,
})

const okBindings = () => ({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) })

describe('session store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('adds user bubble only from host envelope', () => {
    const s = useSession()
    expect(s.chat.length).toBe(0)
    s.apply(env({ kind: 'message', role: 'user', text: 'hi' }))
    expect(s.chat[0]).toMatchObject({ role: 'user', text: 'hi' })
  })

  it('routes tool-role echo to timeline, not chat', () => {
    const s = useSession()
    s.apply(env({ kind: 'message', role: 'tool', text: 'tool result echo' }))
    expect(s.chat.length).toBe(0)
    expect(s.timeline.at(-1)!.env.text).toBe('tool result echo')
  })

  it('accumulates deltas then finalizes on assistant message', () => {
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'a', thinking: 'th1-' }))
    s.apply(env({ kind: 'delta', text: 'b', thinking: 'th2' }))
    expect(s.chat.at(-1)).toMatchObject({ role: 'assistant', text: 'ab', thinking: 'th1-th2', streaming: true })
    s.apply(env({ kind: 'message', role: 'assistant', text: 'ab!' }))
    expect(s.chat.at(-1)).toMatchObject({ text: 'ab!', thinking: 'th1-th2', streaming: false })
  })

  it('result finalizes trailing streaming bubble', () => {
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'hi' }))
    s.apply(env({ kind: 'message', role: 'assistant', text: 'hi' }))
    s.apply(env({ kind: 'delta', text: '' }))
    s.apply(env({ kind: 'result' }))
    expect(s.chat.length).toBe(1)
    expect(s.chat.at(-1)).toMatchObject({ text: 'hi', streaming: false })
    s.apply(env({ kind: 'delta', text: 'partial tail' }))
    s.apply(env({ kind: 'result' }))
    expect(s.chat.at(-1)).toMatchObject({ text: 'partial tail', streaming: false })
  })

  it('overwrites usage snapshot instead of adding', () => {
    const s = useSession()
    s.apply(env({ kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 } }))
    s.apply(env({ kind: 'usage', usage: { input_tokens: 15, output_tokens: 3 } }))
    expect(s.totals.input).toBe(15)
    s.apply(env({ kind: 'result', cost_usd: 0.25, usage: { input_tokens: 20, output_tokens: 5 } }))
    expect(s.totals.input).toBe(20)
    expect(s.totals.cost).toBeCloseTo(0.25)
  })

  it('shows — when provider reports no cost', () => {
    const s = useSession()
    expect(s.costDisplay).toBe('—')
    s.apply(env({ kind: 'result', cost_usd: 0.5 }))
    expect(s.costDisplay).toBe('$0.5000')
  })

  it('tracks usage semantics for provider_latest marker', () => {
    const s = useSession()
    s.apply(env({ kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 }, usage_semantics: 'provider_latest' }))
    expect(s.usageSemantics).toBe('provider_latest')
    s.apply(env({ kind: 'result', usage: { input_tokens: 5, output_tokens: 2 }, usage_semantics: 'session_total' }))
    expect(s.usageSemantics).toBe('session_total')
  })

  it('tracks identity and state; result unlocks busy', () => {
    const s = useSession()
    s.setBindings(okBindings())
    void s.submit('hello')
    expect(s.busy).toBe(true)
    s.apply(env({ kind: 'init', session_id: 's1', task_id: 'task-1' }))
    s.apply(env({ kind: 'state_change', state: 'waiting' }))
    s.apply(env({ kind: 'result' }))
    expect(s.busy).toBe(false)
    expect(s.sessionId).toBe('s1')
    expect(s.taskId).toBe('task-1')
    expect(s.state).toBe('waiting')
  })

  it('submit routes first to StartSession then to SendMessage', async () => {
    const s = useSession()
    const start = vi.fn(async () => {})
    const send = vi.fn(async () => {})
    s.setBindings({ StartSession: start, SendMessage: send })
    await s.submit('one')
    expect(start).toHaveBeenCalledOnce()
    s.apply(env({ kind: 'result' }))
    await s.submit('two')
    expect(send).toHaveBeenCalledWith('claude', 'two')
  })

  it('submit failure pushes error and unlocks without user bubble', async () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => { throw new Error('busy') }), SendMessage: vi.fn(async () => {}) })
    await s.submit('x')
    expect(s.busy).toBe(false)
    expect(s.chat.length).toBe(0)
    expect(s.timeline.at(-1)!.env.error).toContain('busy')
  })

  it('note enters timeline as info item', () => {
    const s = useSession()
    s.note('auth ok')
    expect(s.timeline.at(-1)!.env).toMatchObject({ kind: 'note', text: 'auth ok' })
  })

  it('remembers inputs per view across switches', () => {
    const s = useSession()
    s.setResumeInput('claude-id')
    s.setTaskLabel('label-c')
    s.setActiveProvider('codex')
    expect(s.resumeInput).toBe('')
    s.setResumeInput('codex-id')
    expect(s.resumeInput).toBe('codex-id')
    s.setActiveProvider('claude')
    expect(s.resumeInput).toBe('claude-id')
    expect(s.taskLabel).toBe('label-c')
  })

  it('groups consecutive system noise in timeline', () => {
    const s = useSession()
    s.apply(env({ kind: 'system_other' }))
    s.apply(env({ kind: 'system_other' }))
    s.apply(env({ kind: 'unknown' }))
    s.apply(env({ kind: 'tool_use', text: 'Bash' }))
    const groups = new Set(s.timeline.map(i => i.group).filter(g => g !== undefined))
    expect(groups.size).toBe(1)
    expect(s.timeline.at(-1)!.group).toBeUndefined()
  })

  // ---- M1.5：per-provider views ----

  it('routes events to per-provider views', () => {
    const s = useSession()
    s.apply(env({ provider: 'claude', kind: 'delta', text: 'c1' }))
    s.apply(env({ provider: 'codex', kind: 'delta', text: 'x1' }))
    expect(s.views.claude.chat.at(-1)!.text).toBe('c1')
    expect(s.views.codex.chat.at(-1)!.text).toBe('x1')
    s.apply(env({ provider: 'codex', kind: 'usage', usage: { input_tokens: 9, output_tokens: 1 } }))
    expect(s.views.codex.totals.input).toBe(9)
    expect(s.views.claude.totals.input).toBe(0) // 隔離
  })

  it('switching provider preserves both views', () => {
    const s = useSession()
    s.apply(env({ provider: 'claude', kind: 'message', role: 'user', text: 'hi-c' }))
    s.setActiveProvider('codex')
    s.apply(env({ provider: 'codex', kind: 'message', role: 'user', text: 'hi-x' }))
    expect(s.chat.at(-1)!.text).toBe('hi-x')
    s.setActiveProvider('claude')
    expect(s.chat.at(-1)!.text).toBe('hi-c') // 切回零丟失
    expect(s.views.codex.chat.at(-1)!.text).toBe('hi-x')
  })

  it('background view counts unread per completed turn', () => {
    const s = useSession() // active = claude
    s.apply(env({ provider: 'codex', kind: 'delta', text: 'streaming…' })) // 非 turn 完成：不計
    expect(s.unreadOf('codex')).toBe(0)
    s.apply(env({ provider: 'codex', kind: 'message', role: 'assistant', text: 'done' })) // 訊息也不計
    expect(s.unreadOf('codex')).toBe(0)
    s.apply(env({ provider: 'codex', kind: 'result' })) // 每完成 turn +1
    expect(s.unreadOf('codex')).toBe(1)
    s.apply(env({ provider: 'codex', kind: 'result' }))
    expect(s.unreadOf('codex')).toBe(2)
    s.apply(env({ provider: 'claude', kind: 'result' })) // active view 不計 unread
    expect(s.unreadOf('claude')).toBe(0)
  })

  it('unread clears on switch', () => {
    const s = useSession()
    s.apply(env({ provider: 'codex', kind: 'result' }))
    expect(s.unreadOf('codex')).toBe(1)
    s.setActiveProvider('codex')
    expect(s.unreadOf('codex')).toBe(0)
  })

  it('per-view busy: A busy does not block B submit', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    await s.submit('claude go') // claude busy=true（等 result）
    expect(s.views.claude.busy).toBe(true)
    s.setActiveProvider('codex')
    expect(s.busy).toBe(false) // codex view 不受影響
    await s.submit('codex go')
    expect(b.StartSession).toHaveBeenCalledTimes(2)
    expect(s.views.codex.busy).toBe(true)
  })

  it('applyDone routes by payload provider', () => {
    const s = useSession() // active = claude
    s.views.codex.active = true
    s.views.codex.busy = true
    s.applyDone({ provider: 'codex', processStillRunning: true })
    expect(s.views.codex.busy).toBe(false)
    expect(s.views.codex.active).toBe(false)
    expect(s.views.codex.timeline.at(-1)!.env.kind).toBe('session_done')
    expect(s.views.claude.timeline.length).toBe(0) // 不進 active view
  })

  it('awaiting_approval flags the owning view', () => {
    const s = useSession() // active = claude
    s.apply(env({ provider: 'codex', kind: 'state_change', state: 'awaiting_approval' }))
    expect(s.awaitingOf('codex')).toBe(true)
    expect(s.awaitingOf('claude')).toBe(false)
  })

  it('taskLabel is isolated per view', async () => {
    const s = useSession()
    const b = okBindings()
    s.setBindings(b)
    s.setTaskLabel('task-c')
    s.setActiveProvider('codex')
    s.setTaskLabel('task-x')
    await s.submit('go x') // codex submit 帶自己的標籤
    expect(b.StartSession).toHaveBeenCalledWith('codex', 'go x', '', '', 'task-x', 'untrusted')
    s.setActiveProvider('claude')
    expect(s.taskLabel).toBe('task-c') // A 設標籤不影響 B、各帶各的
    await s.submit('go c')
    expect(b.StartSession).toHaveBeenCalledWith('claude', 'go c', '', '', 'task-c', 'untrusted')
  })

  it('recordCase is isolated per view', () => {
    const s = useSession()
    s.setRecordCase('claude-rec')
    s.setActiveProvider('codex')
    expect(s.recordCase).toBe('')
    s.setRecordCase('codex-rec')
    s.setActiveProvider('claude')
    expect(s.recordCase).toBe('claude-rec')
    expect(s.views.codex.recordCase).toBe('codex-rec')
  })

  it('reset only resets the active view', () => {
    const s = useSession()
    s.apply(env({ provider: 'claude', kind: 'message', role: 'user', text: 'c' }))
    s.apply(env({ provider: 'codex', kind: 'message', role: 'user', text: 'x' }))
    s.setTaskLabel('keep-me')
    s.reset()
    expect(s.chat.length).toBe(0)
    expect(s.taskLabel).toBe('keep-me') // 輸入欄位保留
    expect(s.views.codex.chat.length).toBe(1) // 另一 view 不動
  })
})
