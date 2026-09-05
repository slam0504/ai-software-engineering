import { flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import SpecWorkspace from './SpecWorkspace.vue'
import { useAssist } from '../stores/assist'
import { mountWithI18n } from '../test/i18n'

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

  // CodeMirror 由元件 onMounted 內動態 import 並建構 EditorView。B1b 觀察：本檔一條測試在
  // 全套並行下約 2s、三份併發下約 7s > 5s 預設 timeout，其餘測試僅數十 ms；只預熱 import
  // 時成本會移到同檔另一條測試而不消失。B1b Gate A（2026-09-05）以 differential control
  // 2/2 確認：剩餘為 jsdom 首次建構 EditorView 的一次性成本。先在 hook 內 import 並建構一次
  // 即銷毀，讓測試本體只量契約。不 catch：建構或清理失敗就讓 hook 失敗。30s 是卡死保險絲，不是成功判準。
  beforeAll(async () => {
    const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
      import('codemirror'),
      import('@codemirror/state'),
    ])
    const host = document.createElement('div')
    document.body.appendChild(host)
    const view = new EditorView({ state: EditorState.create({ doc: '', extensions: [basicSetup] }), parent: host })
    view.destroy()
    host.remove()
  }, 30_000)

  it('accept writes draft via SpecWrite, not before', async () => {
    const write = vi.fn().mockResolvedValue('sha256:x')
    const w = mountWithI18n(SpecWorkspace, { props: {
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

    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
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

    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
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

// 新增檔案 inline 列（M3a.1 Task 4，spec §3.1 SC4 缺口 1）：路徑輸入＋即時 scope
// 預驗（spec 四 pattern）＋送出 SpecWrite(path, templateFor(path), '')，成功後
// 重載清單並選取新檔，失敗顯示錯誤原文、清單不動。
describe('SpecWorkspace 新增檔案', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('scope 外路徑即時提示，送出 disabled', async () => {
    const w = mountWithI18n(SpecWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('spec/other/x.feature')
    await flushPromises()

    expect(w.find('[data-test=new-file-scope-hint]').exists()).toBe(true)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeDefined()
    expect(mocks.SpecWrite).not.toHaveBeenCalled()
  })

  it('scope 內路徑送出成功後重載清單並選取新檔', async () => {
    mocks.SpecWrite.mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('spec/features/new.feature')
    await flushPromises()
    expect(w.find('[data-test=new-file-scope-hint]').exists()).toBe(false)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=new-file-submit]').trigger('click')
    await flushPromises()

    expect(mocks.SpecWrite).toHaveBeenCalledWith('spec/features/new.feature', '', '') // spec 路徑 templateFor 回空字串
    expect(mocks.SpecList).toHaveBeenCalledTimes(2) // mount 一次＋成功後重載一次
    expect(mocks.SpecRead).toHaveBeenCalledWith('spec/features/new.feature') // 選取新檔＋載入內容
  })

  it('失敗顯示錯誤原文，清單不動', async () => {
    mocks.SpecWrite.mockRejectedValue(new Error('path "spec/features/dup.feature" write conflict: expected_digest does not match current file'))
    const w = mountWithI18n(SpecWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('spec/features/dup.feature')
    await w.find('[data-test=new-file-submit]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=new-file-error]').text()).toContain('write conflict: expected_digest does not match current file')
    expect(mocks.SpecList).toHaveBeenCalledTimes(1) // 只有 mount 那次，失敗後不重載
  })
})
