import { describe, expect, it } from 'vitest'
import { escalationBadge } from './escalationBadge'

describe('escalationBadge', () => {
  it('unavailable 非空時一律回 warn，即使 unresolvedCount=0（不得視覺等同「無項目」，§3.8）', () => {
    expect(escalationBadge('escalation: journal degraded', 0)).toEqual({ kind: 'warn' })
  })

  it('unavailable 非空時 warn 優先於未 resolved 計數（不會同時顯示數字）', () => {
    expect(escalationBadge('escalation: journal degraded', 3)).toEqual({ kind: 'warn' })
  })

  it('unavailable 空且 unresolvedCount=0 時不顯示徽章', () => {
    expect(escalationBadge('', 0)).toBeNull()
  })

  it('unavailable 空且 unresolvedCount>0 時顯示計數', () => {
    expect(escalationBadge('', 5)).toEqual({ kind: 'count', n: 5 })
  })
})
