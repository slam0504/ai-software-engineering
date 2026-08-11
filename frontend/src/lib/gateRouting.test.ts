import { describe, it, expect } from 'vitest'
import { routeEnvelope } from './gateRouting'

describe('routeEnvelope', () => {
  it('workspace → gate', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'gate_request' } as any)).toBe('gate')
  })
  it('session + spec_assist → assist', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', purpose: 'spec_assist' } as any)).toBe('assist')
  })
  it('normal session → session', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', kind: 'message' } as any)).toBe('session')
  })
  it('legacy no-scope → session', () => {
    expect(routeEnvelope({ provider: 'codex', kind: 'message' } as any)).toBe('session')
  })
})
