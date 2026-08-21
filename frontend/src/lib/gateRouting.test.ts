import { describe, it, expect } from 'vitest'
import { routeEnvelope } from './gateRouting'

describe('routeEnvelope', () => {
  it('workspace → gate', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'gate_request' } as any)).toBe('gate')
  })
  it('session + spec_assist → assist', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', purpose: 'spec_assist' } as any)).toBe('assist')
  })
  it('session + plan_draft → plan', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', purpose: 'plan_draft' } as any)).toBe('plan')
  })
  it('no-scope + plan_draft → plan', () => {
    expect(routeEnvelope({ provider: 'codex', purpose: 'plan_draft' } as any)).toBe('plan')
  })
  it('workspace + plan_draft → notice (scope takes precedence, non-gate kind)', () => {
    expect(routeEnvelope({ scope: 'workspace', purpose: 'plan_draft' } as any)).toBe('notice')
  })
  it('workspace + kind=evidence_run → evidence', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'evidence_run' } as any)).toBe('evidence')
  })
  it('workspace + gate kind → gate', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'approval_decision' } as any)).toBe('gate')
    expect(routeEnvelope({ scope: 'workspace', kind: 'binding_stale' } as any)).toBe('gate')
  })
  // 迴歸：這三種 workspace 事件此前一律被丟給 gate store，而 applyGateEvent 的
  // 第一行 `if (!id) return` 會靜默丟棄它們——degraded 通知因此到不了使用者。
  it('workspace + 非 gate kind → notice（degraded／broadcast 不得被靜默丟棄）', () => {
    expect(routeEnvelope({ scope: 'workspace', kind: 'stream_error' } as any)).toBe('notice')
    expect(routeEnvelope({ scope: 'workspace', kind: 'codex_broadcast' } as any)).toBe('notice')
    expect(routeEnvelope({ scope: 'workspace', kind: 'unknown' } as any)).toBe('notice')
  })
  it('normal session → session', () => {
    expect(routeEnvelope({ scope: 'session', provider: 'claude', kind: 'message' } as any)).toBe('session')
  })
  it('legacy no-scope → session', () => {
    expect(routeEnvelope({ provider: 'codex', kind: 'message' } as any)).toBe('session')
  })
})
