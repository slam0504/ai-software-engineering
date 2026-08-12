import { flushPromises, mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SpecWorkspace from './SpecWorkspace.vue'
import { useAssist } from '../stores/assist'

// wailsjs 綁定 mock：控制 SpecAssist 的 resolve 時機，模擬「assist 進行中操作者
// 切檔」的競態（fix round 2）。SpecList/SpecRead 給穩定預設值，避免 onMounted
// 的載入路徑在測試裡撞到未定義行為。
const mocks = vi.hoisted(() => ({
  SpecAssist: vi.fn(),
  SpecList: vi.fn(),
  SpecRead: vi.fn(),
  SpecWrite: vi.fn(),
  SubmitForApproval: vi.fn(),
  PreviewSpecCommit: vi.fn(),
  ConfirmSpecCommit: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

describe('SpecWorkspace draft accept', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
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

  // fix round 2：SpecAssist 在 in-flight 時操作者切到另一個檔案，assist 完成後
  // 不得把舊檔的 correlation_id 綁到現在選中的新檔——否則 accept 會用新檔的合法
  // digest 把舊檔的草稿寫進新檔（同 fix round 1 的跨檔污染，經由競態觸發）。
  //
  // SpecAssist 現在直接回傳 correlation_id（見 app.go），mock 需回傳同一個 id
  // 才能還原「id 到達時 effectivePath 已經變了」這個競態視窗。
  it('discards spec-assist result if the file switches during the call', async () => {
    let resolveAssist: (id: string) => void = () => {}
    mocks.SpecAssist.mockImplementation(() => new Promise<string>(r => { resolveAssist = r }))

    const w = mount(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()

    await w.find('[data-test=assist-draft]').trigger('click') // 對 A 發起 assist，尚未 resolve
    expect(mocks.SpecAssist).toHaveBeenCalledTimes(1)

    // 模擬串流事件在 await 期間送達（scope=session/purpose=spec_assist 的 envelope）
    const assist = useAssist()
    assist.applyAssistEvent({
      event_id: 'e1', ts: 't', provider: 'claude', kind: 'delta',
      correlation_id: 'corr-a', text: 'draft for A',
    })

    await w.setProps({ path: 'spec/b.feature' }) // 操作者切到 B（resetDraft 已清空 currentCorrelationId）
    await flushPromises()

    resolveAssist('corr-a') // A 的 SpecAssist 呼叫現在才 resolve，回傳 A 的 correlation_id
    await flushPromises()

    expect(w.find('[data-test=draft-text]').text()).toBe('') // 不得綁定成 B 的目前草稿
    expect(w.find('[data-test=accept-draft]').attributes('disabled')).toBeDefined()
  })

  it('binds the draft via the correlation_id returned by SpecAssist', async () => {
    mocks.SpecAssist.mockResolvedValue('corr-a')

    const w = mount(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()

    const assist = useAssist()
    assist.applyAssistEvent({
      event_id: 'e1', ts: 't', provider: 'claude', kind: 'delta',
      correlation_id: 'corr-a', text: 'draft for A',
    })

    await w.find('[data-test=assist-draft]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=draft-text]').text()).toBe('draft for A')
    expect(w.find('[data-test=assist-busy]').exists()).toBe(false)
  })
})
