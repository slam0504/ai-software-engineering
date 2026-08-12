export const sessionStateKeys: Record<string, string> = {
  idle: 'session.state.idle', waiting: 'session.state.waiting',
  streaming: 'session.state.streaming', tool_running: 'session.state.toolRunning',
  awaiting_approval: 'session.state.awaitingApproval', retrying: 'session.state.retrying',
  done: 'session.state.done', failed: 'session.state.failed',
}
export const gateStateKeys: Record<string, string> = {
  pending: 'gate.state.pending', active: 'gate.state.active',
  stale: 'gate.state.stale', superseded: 'gate.state.superseded',
}
export const codexToolStatusKeys: Record<string, string> = {
  completed: 'timeline.toolStatus.completed', inProgress: 'timeline.toolStatus.inProgress',
  failed: 'timeline.toolStatus.failed',
}
export function resolveState(map: Record<string, string>, raw: string, translate: (k: string) => string): string {
  const key = map[raw]
  return key ? translate(key) : raw // unknown → 原樣，不洩漏缺漏 key
}
