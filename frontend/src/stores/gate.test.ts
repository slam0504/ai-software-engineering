import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useGate } from './gate'
import { useSession } from './session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'gate_request', scope: 'workspace', ...over,
})

describe('gate store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('gate_request creates a pending entry', () => {
    const g = useGate()
    g.applyGateEvent(env({ kind: 'gate_request', payload: { approval_id: 'a1', gate: 'commit_review' } }))
    expect(g.list).toHaveLength(1)
    expect(g.entries['a1']).toMatchObject({ approval_id: 'a1', state: 'pending', gate: 'commit_review' })
  })

  it('gate_request → approval_decision(approved) → binding_stale transitions pending→active→stale', () => {
    const g = useGate()
    g.applyGateEvent(env({ kind: 'gate_request', payload: { approval_id: 'a1', gate: 'commit_review' } }))
    expect(g.entries['a1'].state).toBe('pending')

    g.applyGateEvent(env({ kind: 'approval_decision', payload: { approval_id: 'a1', decision: 'approved' } }))
    expect(g.entries['a1'].state).toBe('active')

    g.applyGateEvent(env({ kind: 'binding_stale', payload: { approval_id: 'a1' } }))
    expect(g.entries['a1'].state).toBe('stale')
  })

  it('does not mutate session store state', () => {
    const g = useGate()
    const s = useSession()
    const chatLenBefore = s.chat.length
    const timelineLenBefore = s.timeline.length

    g.applyGateEvent(env({ kind: 'gate_request', payload: { approval_id: 'a1', gate: 'commit_review' } }))
    g.applyGateEvent(env({ kind: 'approval_decision', payload: { approval_id: 'a1', decision: 'approved' } }))
    g.applyGateEvent(env({ kind: 'binding_stale', payload: { approval_id: 'a1' } }))

    expect(s.chat.length).toBe(chatLenBefore)
    expect(s.timeline.length).toBe(timelineLenBefore)
    expect(s.sessionId).toBe('')
  })
})
