import { flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
vi.mock('mermaid', () => ({ default: { initialize: vi.fn(), render: vi.fn().mockResolvedValue({ svg: '<svg/>' }) } }))
import DiagramPane from './DiagramPane.vue'
import { mountWithI18n } from '../test/i18n'

describe('DiagramPane', () => {
  it('renders context-map mmd on load', async () => {
    // SpecRead(rel) 回傳 SpecFile{content,digest}（Task 15 起的簽章，非舊的 [content,digest] tuple）
    const read = vi.fn().mockResolvedValue({ content: 'graph TD; A-->B', digest: 'sha256:x' })
    const w = mountWithI18n(DiagramPane, { props: { path: 'spec/context-map/c4.mmd', read } })
    await flushPromises()
    expect(w.html()).toContain('svg')
    expect(read).toHaveBeenCalledWith('spec/context-map/c4.mmd')
  })
})
