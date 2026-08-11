import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SpecWorkspace from './SpecWorkspace.vue'

describe('SpecWorkspace draft accept', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('accept writes draft via SpecWrite, not before', async () => {
    const write = vi.fn().mockResolvedValue('sha256:x')
    const w = mount(SpecWorkspace, { props: {
      path: 'spec/glossary.md', draft: 'AI draft content', write,
    }})
    expect(write).not.toHaveBeenCalled() // 草稿不自動寫檔
    await w.find('[data-test=accept-draft]').trigger('click')
    expect(write).toHaveBeenCalledWith('spec/glossary.md', 'AI draft content', expect.any(String))
  })
})
