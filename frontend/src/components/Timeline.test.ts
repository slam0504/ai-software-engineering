import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import Timeline from './Timeline.vue'
import { useSession } from '../stores/session'
import type { Envelope } from '../types'
import { mountWithI18n } from '../test/i18n'

// 以下三個 fixture 是 **production 實際發出的形狀**，逐欄對照 Go 端：
//  - onIndexDegraded（rebuild_orchestrator.go）：直接組 Envelope，頂層 Error 有填
//  - failLoudCodexDispatch／latchWireRecorder（app.go）：走 Manager.EmitWorkspace，
//    訊息只在 Payload，**頂層 Error 是 omitempty 且從未被填**
//  - emitCodexBroadcast：payload 帶 method／params（或 raw）
//
// 用「頂層 error」當 fixture 會讓這組測試恆綠而放過真正的落差（Task 26 review
// 抓到的 Important：路由修好了，訊息內容仍到不了使用者）。
const indexDegraded: Envelope = {
  event_id: 'wn1', ts: 't', provider: '', scope: 'workspace',
  kind: 'stream_error', error: 'replay index degraded: checkpoint ahead of audit',
}
const codexDispatchFailure: Envelope = {
  event_id: 'wn2', ts: 't', provider: '', scope: 'workspace', kind: 'stream_error',
  payload: { error: 'codex: 無法歸屬的 approval 請求 tool/approve → decline' },
}
const wireLogDegraded: Envelope = {
  event_id: 'wn3', ts: 't', provider: '', scope: 'workspace', kind: 'stream_error',
  payload: { component: 'codex_wire_log', wireLogId: 'wl-1', error: 'disk full' },
}
const broadcast: Envelope = {
  event_id: 'wn4', ts: 't', provider: 'codex', scope: 'workspace', kind: 'codex_broadcast',
  payload: { provider: 'codex', method: 'account/rateLimits/updated', params: {} },
}

describe('Timeline：workspace 通知的內容必須看得到（不只看得到 kind）', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('replay index degraded 的訊息渲染出來', () => {
    const s = useSession()
    s.applyNotice(indexDegraded)
    const w = mountWithI18n(Timeline)
    expect(w.find('.sum').text()).toContain('replay index degraded')
  })

  it('EmitWorkspace 形狀（訊息只在 payload）同樣渲染得出來', () => {
    const s = useSession()
    s.applyNotice(codexDispatchFailure)
    const w = mountWithI18n(Timeline)
    const sum = w.find('.sum').text()
    expect(sum).toContain('無法歸屬的 approval 請求')
    expect(sum).not.toBe('stream_error') // 迴歸：落到 `return e.kind` 就是這個值
  })

  it('帶 component 的 payload 一併顯示來源元件', () => {
    const s = useSession()
    s.applyNotice(wireLogDegraded)
    const w = mountWithI18n(Timeline)
    expect(w.find('.sum').text()).toContain('codex_wire_log')
    expect(w.find('.sum').text()).toContain('disk full')
  })

  it('codex_broadcast 顯示 method 而非只有 kind', () => {
    const s = useSession()
    s.applyNotice(broadcast)
    const w = mountWithI18n(Timeline)
    const sum = w.find('.sum').text()
    expect(sum).toContain('account/rateLimits/updated')
    expect(sum).not.toBe('codex_broadcast')
  })

  it('session lane 的既有 summary 行為不變', () => {
    const s = useSession()
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.pin(0, 'w1')
    s.setFocus(0)
    s.apply({ event_id: 'e1', ts: 't', provider: 'claude', kind: 'tool_use',
      text: 'Bash(ls)', workspace_session_id: 'w1' })
    const w = mountWithI18n(Timeline)
    expect(w.find('.sum').text()).toContain('Bash(ls)')
  })
})
