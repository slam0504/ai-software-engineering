import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSession } from './session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta', ...over,
})

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

  it('tracks usage semantics for provider_latest marker', () => { // 第四輪 P2-1
    const s = useSession()
    s.apply(env({ kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 }, usage_semantics: 'provider_latest' }))
    expect(s.usageSemantics).toBe('provider_latest')
    s.apply(env({ kind: 'result', usage: { input_tokens: 5, output_tokens: 2 }, usage_semantics: 'session_total' }))
    expect(s.usageSemantics).toBe('session_total')
  })

  it('tracks identity and state; result unlocks busy', () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) })
    void s.submit('hello')
    expect(s.busy).toBe(true)
    s.apply(env({ kind: 'init', session_id: 's1', task_id: 'task-1' }))
    s.apply(env({ kind: 'state_change', state: 'waiting' }))
    s.apply(env({ kind: 'result' }))
    expect(s.busy).toBe(false)
    expect(s.sessionId).toBe('s1')
    expect(s.taskId).toBe('task-1')
    expect(s.state).toBe('waiting') // state 只由 state_change 驅動（result 的 done 事件另行到達）
  })

  it('submit routes first to StartSession then to SendMessage', async () => {
    const s = useSession()
    const start = vi.fn(async () => {})
    const send = vi.fn(async () => {})
    s.setBindings({ StartSession: start, SendMessage: send })
    await s.submit('one')
    expect(start).toHaveBeenCalledOnce()
    s.apply(env({ kind: 'result' })) // busy 解鎖
    await s.submit('two')
    expect(send).toHaveBeenCalledWith('two')
  })

  it('submit failure pushes error and unlocks without user bubble', async () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => { throw new Error('busy') }), SendMessage: vi.fn(async () => {}) })
    await s.submit('x')
    expect(s.busy).toBe(false)
    expect(s.chat.length).toBe(0)
    expect(s.timeline.at(-1)!.env.error).toContain('busy')
  })

  it('result finalizes trailing streaming bubble', () => {
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'hi' }))
    s.apply(env({ kind: 'message', role: 'assistant', text: 'hi' }))
    s.apply(env({ kind: 'delta', text: '' })) // message 落定後的殘餘 delta（空氣泡）
    s.apply(env({ kind: 'result' }))
    expect(s.chat.length).toBe(1) // 空殘餘氣泡被移除
    expect(s.chat.at(-1)).toMatchObject({ text: 'hi', streaming: false })
    s.apply(env({ kind: 'delta', text: 'partial tail' })) // 有內容的殘餘則落定保留
    s.apply(env({ kind: 'result' }))
    expect(s.chat.at(-1)).toMatchObject({ text: 'partial tail', streaming: false })
  })

  it('note enters timeline as info item', () => {
    const s = useSession()
    s.note('auth ok')
    expect(s.timeline.at(-1)!.env).toMatchObject({ kind: 'note', text: 'auth ok' })
  })

  it('applyDone records session end and clears active/busy', () => {
    const s = useSession()
    s.active = true
    s.busy = true
    s.applyDone({ provider: 'claude', exitCode: 0, recorderError: '' })
    expect(s.timeline.at(-1)!.env.kind).toBe('session_done')
    expect(s.timeline.at(-1)!.env.text).toContain('exitCode')
    expect(s.busy).toBe(false)
    expect(s.active).toBe(false)
  })

  it('remembers resume per provider', () => {
    const s = useSession()
    s.setResumeInput('claude-id')
    s.provider = 'codex'
    expect(s.resumeInput).toBe('')
    s.setResumeInput('codex-id')
    expect(s.resumeInput).toBe('codex-id')
    s.provider = 'claude'
    expect(s.resumeInput).toBe('claude-id')
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
})
