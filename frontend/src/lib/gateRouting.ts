import type { Envelope } from '../types'

// routeEnvelope：依 scope/purpose 分流 host envelope（§3.4c）。
// workspace → gate；session（含缺省）+ purpose=spec_assist → assist；
// session（含缺省）+ purpose=plan_draft → plan（Task 13：PlanWorkspace 草稿區，
// 不得 fallback 進 session，否則會污染 Chat 與 totals）；否則 session。
export function routeEnvelope(env: Pick<Envelope, 'scope' | 'purpose'>): 'session' | 'gate' | 'assist' | 'plan' {
  if (env.scope === 'workspace') return 'gate'
  if (env.scope === 'session' || env.scope === undefined) {
    if (env.purpose === 'spec_assist') return 'assist'
    if (env.purpose === 'plan_draft') return 'plan'
  }
  return 'session'
}
