import type { Envelope } from '../types'

// routeEnvelope：依 scope/purpose/kind 分流 host envelope（§3.4c）。
// workspace + kind=evidence_run → evidence（Task 22：RunEvidence 的
// started/finished 進度事件，走 workspace scope 但不是 gate_request/
// approval_decision/binding_stale，需要跟其餘 workspace 事件分開，不進 gate
// store）；workspace 其餘 kind → gate；session（含缺省）+ purpose=spec_assist
// → assist；session（含缺省）+ purpose=plan_draft → plan（Task 13：
// PlanWorkspace 草稿區，不得 fallback 進 session，否則會污染 Chat 與
// totals）；否則 session。
export function routeEnvelope(
  env: Pick<Envelope, 'scope' | 'purpose' | 'kind'>,
): 'session' | 'gate' | 'assist' | 'plan' | 'evidence' {
  if (env.scope === 'workspace') return env.kind === 'evidence_run' ? 'evidence' : 'gate'
  if (env.scope === 'session' || env.scope === undefined) {
    if (env.purpose === 'spec_assist') return 'assist'
    if (env.purpose === 'plan_draft') return 'plan'
  }
  return 'session'
}
