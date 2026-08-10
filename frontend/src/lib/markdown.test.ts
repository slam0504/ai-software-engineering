import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown'

describe('renderMarkdown sanitization', () => {
  it('strips script tags', () => {
    expect(renderMarkdown('hello <script>alert(1)</script>')).not.toContain('<script')
  })
  it('strips event handlers', () => {
    expect(renderMarkdown('<img src=x onerror="alert(1)">')).not.toContain('onerror')
  })
  it('strips javascript: URLs', () => {
    expect(renderMarkdown('[x](javascript:alert(1))')).not.toContain('javascript:')
  })
  it('keeps normal markdown', () => {
    const html = renderMarkdown('# Title\n\n**bold**')
    expect(html).toContain('<h1')
    expect(html).toContain('<strong>')
  })
})
