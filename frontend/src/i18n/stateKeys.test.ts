import { describe, it, expect } from 'vitest'
import { sessionStateKeys, gateStateKeys, codexToolStatusKeys, resolveState } from './stateKeys'

const fakeT = (k: string) => `T(${k})`

describe('stateKeys', () => {
  it('three maps are independent and cover known raws', () => {
    expect(sessionStateKeys.idle).toBe('session.state.idle')
    expect(gateStateKeys.stale).toBe('gate.state.stale')
    expect(codexToolStatusKeys.inProgress).toBe('timeline.toolStatus.inProgress')
  })
  it('resolveState translates known raw', () => {
    expect(resolveState(sessionStateKeys, 'idle', fakeT)).toBe('T(session.state.idle)')
  })
  it('resolveState passes through unknown raw (no missing key leak)', () => {
    expect(resolveState(sessionStateKeys, 'weird_state', fakeT)).toBe('weird_state')
    expect(resolveState(gateStateKeys, 'bogus', fakeT)).toBe('bogus')
  })
})
