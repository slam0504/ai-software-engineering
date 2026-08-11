import type { Envelope } from '../types'

// routeEnvelope：依 scope/purpose 分流 host envelope（§3.4c）。
// workspace → gate；session（含缺省）+ purpose=spec_assist → assist；否則 session。
export function routeEnvelope(env: Pick<Envelope, 'scope' | 'purpose'>): 'session' | 'gate' | 'assist' {
  if (env.scope === 'workspace') return 'gate'
  if ((env.scope === 'session' || env.scope === undefined) && env.purpose === 'spec_assist') return 'assist'
  return 'session'
}
