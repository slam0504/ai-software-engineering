import type { Envelope } from '../types'

// GATE_KINDS：gate store 真正會投影的 workspace kind（gate.ts 的 switch 就是這
// 三個）。其餘 workspace 事件送進 gate store 只會被 applyGateEvent 的
// `if (!id) return` 靜默丟掉——replay index degraded、codex dispatch fail loud、
// codex_broadcast 全部走這條，使用者因此永遠看不到。改以白名單分流。
const GATE_KINDS = new Set(['gate_request', 'approval_decision', 'binding_stale'])

// routeEnvelope：依 scope/purpose/kind 分流 host envelope（§3.4c）。
// workspace + kind=evidence_run → evidence（Task 22：RunEvidence 的
// started/finished 進度事件）；workspace ＋ gate kind → gate；workspace 其餘
// kind → notice（session store 的 workspace 通知 lane，見 applyNotice）；
// session（含缺省）+ purpose=spec_assist → assist；session（含缺省）+
// purpose=plan_draft → plan（Task 13：PlanWorkspace 草稿區，不得 fallback 進
// session，否則會污染 Chat 與 totals）；否則 session。
export function routeEnvelope(
  env: Pick<Envelope, 'scope' | 'purpose' | 'kind'>,
): 'session' | 'gate' | 'assist' | 'plan' | 'evidence' | 'notice' {
  if (env.scope === 'workspace') {
    if (env.kind === 'evidence_run') return 'evidence'
    return GATE_KINDS.has(env.kind) ? 'gate' : 'notice'
  }
  if (env.scope === 'session' || env.scope === undefined) {
    if (env.purpose === 'spec_assist') return 'assist'
    if (env.purpose === 'plan_draft') return 'plan'
  }
  return 'session'
}
