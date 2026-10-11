export const sessionStateKeys: Record<string, string> = {
  idle: 'session.state.idle', waiting: 'session.state.waiting',
  streaming: 'session.state.streaming', tool_running: 'session.state.toolRunning',
  awaiting_approval: 'session.state.awaitingApproval', retrying: 'session.state.retrying',
  done: 'session.state.done', failed: 'session.state.failed',
}
export const gateStateKeys: Record<string, string> = {
  pending: 'gate.state.pending', active: 'gate.state.active',
  stale: 'gate.state.stale', superseded: 'gate.state.superseded',
  rejected: 'gate.state.rejected',
}
export const riskTierKeys: Record<string, string> = {
  low: 'risk.tier.low', medium: 'risk.tier.medium', high: 'risk.tier.high',
}
export const codexToolStatusKeys: Record<string, string> = {
  completed: 'timeline.toolStatus.completed', inProgress: 'timeline.toolStatus.inProgress',
  failed: 'timeline.toolStatus.failed',
}
export const evidenceResultKeys: Record<string, string> = {
  passed: 'evidence.result.passed', failed: 'evidence.result.failed', error: 'evidence.result.error',
}
// B4：workspace 確認的原因代碼（Go 端 workspacePolicy 的輸出）與封鎖種類。未知代碼原樣顯示
// （resolveState 的既有行為），新增或改名要和 workspace_confirmation.go 的 wsReason* 同步。
export const workspaceReasonKeys: Record<string, string> = {
  source_home: 'workspaceConfirm.reason.sourceHome',
  source_tmp: 'workspaceConfirm.reason.sourceTmp',
  env_rejected: 'workspaceConfirm.reason.envRejected',
  env_rejected_normalize_failed: 'workspaceConfirm.reason.envRejectedNormalize',
  env_rejected_mkdir_failed: 'workspaceConfirm.reason.envRejectedMkdir',
  is_home: 'workspaceConfirm.reason.isHome',
  home_identity_unknown: 'workspaceConfirm.reason.homeIdentityUnknown',
  workspace_unresolved: 'workspaceConfirm.reason.unresolved',
}
export const workspaceBlockKeys: Record<string, string> = {
  loading: 'workspaceConfirm.blocked.loading',
  failed: 'workspaceConfirm.blocked.failed',
  pending: 'workspaceConfirm.blocked.pending',
}
export function resolveState(map: Record<string, string>, raw: string, translate: (k: string) => string): string {
  const key = map[raw]
  return key ? translate(key) : raw // unknown → 原樣，不洩漏缺漏 key
}
