import { describe, expect, it } from 'vitest'
import { isAtBottom } from './scroll'

describe('isAtBottom', () => {
  it('true at exact bottom', () => expect(isAtBottom(760, 1000, 240)).toBe(true))
  it('true within slack', () => expect(isAtBottom(740, 1000, 240)).toBe(true))
  it('false when scrolled up', () => expect(isAtBottom(300, 1000, 240)).toBe(false))
})
