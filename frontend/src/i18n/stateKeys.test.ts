import { describe, it, expect } from 'vitest'
import { sessionStateKeys, gateStateKeys, codexToolStatusKeys, riskTierKeys, resolveState } from './stateKeys'

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
  it('gateStateKeys covers rejected（backend internal/gate.Rejected 終態）', () => {
    expect(gateStateKeys.rejected).toBe('gate.state.rejected')
    expect(resolveState(gateStateKeys, 'rejected', fakeT)).toBe('T(gate.state.rejected)')
  })
  it('riskTierKeys covers low/medium/high and passes through unknown tier', () => {
    expect(riskTierKeys.low).toBe('risk.tier.low')
    expect(riskTierKeys.medium).toBe('risk.tier.medium')
    expect(riskTierKeys.high).toBe('risk.tier.high')
    expect(resolveState(riskTierKeys, 'low', fakeT)).toBe('T(risk.tier.low)')
    expect(resolveState(riskTierKeys, 'medium', fakeT)).toBe('T(risk.tier.medium)')
    expect(resolveState(riskTierKeys, 'high', fakeT)).toBe('T(risk.tier.high)')
    expect(resolveState(riskTierKeys, 'critical', fakeT)).toBe('critical') // unknown tier passthrough
  })
})
