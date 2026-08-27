import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { usePlan } from './plan'
import { useSession } from './session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta',
  scope: 'session', purpose: 'plan_draft', ...over,
})

describe('plan store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('accumulates draft text/thinking by correlation_id, same as assist.ts convention', () => {
    const p = usePlan()
    p.applyAssistEvent(env({ correlation_id: 'corr-1', text: 'foo ', thinking: 'think ' }))
    p.applyAssistEvent(env({ correlation_id: 'corr-1', text: 'bar', thinking: 'more' }))
    expect(p.draftOf('corr-1')).toEqual({ text: 'foo bar', thinking: 'think more' })
  })

  it('ignores events without a correlation_id', () => {
    const p = usePlan()
    p.applyAssistEvent(env({ text: 'stray' }))
    expect(p.draftOf('')).toEqual({ text: '', thinking: '' })
  })

  it('draftOf returns an empty draft for unknown correlation ids', () => {
    const p = usePlan()
    expect(p.draftOf('missing')).toEqual({ text: '', thinking: '' })
  })

  it('setFiles/setCurrentFile track the file list and the currently open file', () => {
    const p = usePlan()
    p.setFiles([{ name: 'a.yaml', path: 'plan/a.yaml' }])
    expect(p.files).toEqual([{ name: 'a.yaml', path: 'plan/a.yaml' }])

    p.setCurrentFile('plan/a.yaml', 'content', 'sha256:d1')
    expect(p.currentPath).toBe('plan/a.yaml')
    expect(p.currentContent).toBe('content')
    expect(p.currentDigest).toBe('sha256:d1')
  })

  it('pushError/clearErrors accumulate and reset validation/submission errors verbatim', () => {
    const p = usePlan()
    p.pushError('plan write conflict: expected_digest does not match current file')
    p.pushError('assist: 無生效規格核可——先完成 Gate 1')
    expect(p.errors).toEqual([
      'plan write conflict: expected_digest does not match current file',
      'assist: 無生效規格核可——先完成 Gate 1',
    ])
    p.clearErrors()
    expect(p.errors).toEqual([])
  })

  it('clearErrors(kind) 只清同類 error，不動其他 kind（A2：submit 重試不誤清 save 的錯誤）', () => {
    const p = usePlan()
    p.pushError('save failed', 'save')
    p.pushError('submit failed', 'submit')
    p.clearErrors('submit')
    expect(p.errors).toEqual(['save failed'])
  })

  it('does not mutate session store state', () => {
    const p = usePlan()
    const s = useSession()
    const chatLenBefore = s.chat.length
    const timelineLenBefore = s.timeline.length

    p.applyAssistEvent(env({ correlation_id: 'corr-1', text: 'foo' }))
    p.pushError('boom')

    expect(s.chat).toHaveLength(chatLenBefore)
    expect(s.timeline).toHaveLength(timelineLenBefore)
  })
})
