import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
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

  it('T14c：accept writes draft via SpecWrite, not before（既有測試，預期本來就綠）', async () => {
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

// A1a-1 expected-red 階段：非同步儲存契約（buffer／saved／digest／dirty／data-busy／
// 載入世代／樂觀鎖衝突）尚未實作，本 describe 內的測試針對「將來會提供」的可觀察介面
// 斷言——現在大多預期失敗（R），這是 TDD 紅燈階段的正常狀態，不是測試寫錯。
//
// CM6 view 取得方式：production 沒有把 view 掛在任何可從外部拿到的地方，且不得為了
// 測試新增這種介面（不得改 production code）。改用 @codemirror/view 匯出的
// `EditorView.findFromDOM(dom)`（CM6 官方 API，靜態方法，逐一在內部登記表用 WeakMap
// 記錄「DOM 元素 → view instance」，見其原始實作）從 [data-test=editor-host] 反查
// 目前掛載的 view instance，取得後即可用 `view.dispatch({changes})` 直接改動 CM6
// 文件（已實測 beforeinput／input 不會改變 CM6 文件，dispatch 是唯一可靠手段）。
//
// flushEditor：CM6 是動態 import()（`await Promise.all([import('codemirror'), ...])`），
// 光呼叫 flushPromises() 不足以讓它 resolve——已實測需要至少一次真正的 macrotask
// （setTimeout(0)）才會讓 dynamic import 完成，純 microtask flush 對它沒用。
async function flushEditor() {
  await flushPromises()
  await new Promise(r => setTimeout(r, 0))
  await flushPromises()
}

function getView(w: VueWrapper<any>): EditorView {
  const host = w.find('[data-test=editor-host]').element as HTMLElement
  const view = EditorView.findFromDOM(host)
  if (!view) throw new Error('CM6 view 未在 jsdom 下成功掛載——這是環境前置條件失敗，不是行為證據，應先修好再重跑')
  return view
}

function typeText(view: EditorView, text: string) {
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } })
}

// makeFileStore：有狀態讀寫替身（in-memory），取代「手動安排 read 回傳值」的自己
// 餵答案做法。write 用 digest 做樂觀鎖（不符即 reject 帶衝突訊息），成功時產生新
// digest 並寫入內容；read 永遠回傳目前實際內容。用來證明「重載讀到的就是剛才
// 寫進去的」，而不是測試自己安排好的答案。
function makeFileStore(initial: Record<string, { content: string; digest: string }>) {
  const store = new Map<string, { content: string; digest: string }>(Object.entries(initial))
  let seq = 0
  const read = vi.fn((path: string) => {
    const entry = store.get(path)
    return entry
      ? Promise.resolve({ ...entry })
      : Promise.reject(new Error(`makeFileStore: no such file ${path}`))
  })
  const write = vi.fn((path: string, content: string, expectedDigest: string) => {
    const entry = store.get(path)
    const currentDigest = entry?.digest ?? ''
    if (expectedDigest !== currentDigest) {
      return Promise.reject(new Error('path write conflict: expected_digest does not match current file'))
    }
    seq += 1
    const digest = `sha256:seq${seq}`
    store.set(path, { content, digest })
    return Promise.resolve(digest)
  })
  return { read, write }
}

// mustFind：Spec 目前沒有 [data-test=save] 等契約要求的元素，直接 .trigger() 在
// 找不到的 wrapper 上會丟出 VTU 內部錯誤、訊息不易讀。先用明確的 exists 斷言讓
// 「這個契約要求的介面還沒做」這件事本身就是最先失敗、訊息最清楚的那一條。
function mustFind(w: VueWrapper<any>, selector: string) {
  const el = w.find(selector)
  expect(el.exists(), `找不到 ${selector}——尚未實作（expected-red）`).toBe(true)
  return el
}

// findFileButton：A1a-2 切檔守衛測試共用——從 .files 清單裡用檔名文字找按鈕
// （同 PlanWorkspace.test.ts「path prop 只 seed 一次」測試已用過的 findAll+text
// 慣例），不存在時給明確訊息而不是讓 .trigger() 撞上 VTU 的 undefined 錯誤。
function findFileButton(w: VueWrapper<any>, name: string) {
  const btn = w.findAll('.files button').find(b => b.text() === name)
  expect(btn, `找不到檔案清單按鈕 ${name}`).toBeTruthy()
  return btn!
}

describe('SpecWorkspace 非同步儲存契約（A1a-1，expected-red）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('T1：編輯器文件改變→buffer 等於編輯器內容、dirty 為真', async () => {
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited content')
    await flushPromises()

    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined() // dirty 為真
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenCalledWith('spec/a.feature', 'edited content', 'sha256:stub') // buffer 即送出內容
  })

  it('T4：存後重載——重載前 saved／digest／dirty 斷言，送出內容非草稿萃取結果', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: '', digest: 'sha256:stub' },
      'spec/b.feature': { content: 'spec b content', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write: store.write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, '```gherkin\nFeature: raw edit\n```') // 直接編輯，非透過 acceptDraft
    await flushPromises()

    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()

    // 重載前斷言：送出的是編輯器原始內容（含 fence），不是 extractGherkin 萃取結果
    expect(store.write).toHaveBeenCalledWith('spec/a.feature', '```gherkin\nFeature: raw edit\n```', 'sha256:stub')
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // saved==buffer→dirty 假

    await w.setProps({ path: 'spec/b.feature' }) // 切到別的檔，走替身 read
    await flushEditor()
    await w.setProps({ path: 'spec/a.feature' }) // 切回來，走替身 read——讀到的必須是剛才寫入的內容
    await flushEditor()

    expect(getView(w).state.doc.toString()).toBe('```gherkin\nFeature: raw edit\n```') // 重載後文件等於先前儲存內容
  })

  it('T5-S：編輯回原內容→dirty 為假', async () => {
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined() // 編輯後 dirty 真

    typeText(view, '') // 改回原本 saved 內容（初始 content 為空字串）
    await flushPromises()
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // 回到與 saved 相同→dirty 假
  })

  it('T6-S：儲存中續打——成功後 saved 為送出時內容，續打後仍 dirty', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'v1')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click') // 送出 v1，尚未 resolve
    expect(write).toHaveBeenCalledWith('spec/a.feature', 'v1', 'sha256:stub')

    typeText(view, 'v2') // 儲存中續打
    await flushPromises()
    resolveWrite('sha256:v1')
    await flushPromises()

    // saved 應為送出時的內容 v1；目前 buffer 是 v2 → dirty 應為真
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined()
  })

  it('T7-S：儲存中封鎖——再次儲存／接受草稿／切檔皆不執行，data-busy 反映', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'v1')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    expect(write).toHaveBeenCalledTimes(1)
    expect(w.attributes('data-busy')).toBe('save')

    await mustFind(w, '[data-test=save]').trigger('click') // 再次儲存
    await mustFind(w, '[data-test=accept-draft]').trigger('click') // 接受草稿
    await w.setProps({ path: 'spec/b.feature' }) // 切檔
    await flushPromises()

    expect(write).toHaveBeenCalledTimes(1) // 未再被呼叫
    expect(mocks.SpecRead).toHaveBeenCalledTimes(1) // 只有 mount 那次，切檔未觸發重載

    resolveWrite('sha256:new')
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('')

    // 歸屬斷言（缺口 3 修正）：儲存中把 props.path 換成 B 被拒絕，effectivePath
    // 解封後仍須維持 A（不得被切檔期間的 props.path 污染）——再次編輯並儲存，
    // write 收到的 path／content／digest 都必須屬於 A。
    typeText(view, 'v1 continued after unblock')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenNthCalledWith(2, 'spec/a.feature', 'v1 continued after unblock', 'sha256:new')
  })

  it('T8-S：過期世代丟棄——切檔後先前的延遲回應到達時整筆丟棄', async () => {
    // 先讓第一次載入正常完成，CM6 才會初始化（onMounted 內 initEditor 排在
    // loadFile 之後，見 SpecWorkspace.vue）——用一個「已完成」的初始載入建立
    // 可觀察的 view，再另外製造一次延遲載入來測世代丟棄，避免把「CM6 尚未就緒」
    // 和「過期世代該不該套用」這兩件事混在一起斷言。
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushEditor()
    const view = getView(w)

    let resolveDelayed: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead.mockImplementationOnce(() => new Promise(r => { resolveDelayed = r }))
    await w.setProps({ path: 'spec/delayed.feature' }) // 觸發一次延遲載入
    await flushPromises()

    mocks.SpecRead.mockResolvedValueOnce({ content: 'B content', digest: 'sha256:b' })
    await w.setProps({ path: 'spec/b.feature' }) // 切到 B（世代較新），B 立即 resolve
    await flushEditor()
    expect(view.state.doc.toString()).toBe('B content')

    resolveDelayed({ content: 'STALE delayed content', digest: 'sha256:stale' }) // 過期回應現在才到
    await flushPromises()
    expect(view.state.doc.toString()).toBe('B content') // 過期回應被整筆丟棄，不覆蓋 B
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // 未被寫入→仍等於 saved(B)→dirty 假
  })

  it('T8b-S：A→B→A——先前 A 回應延遲，經過 B 後回到 A，該延遲回應仍須丟棄', async () => {
    // 同 T8-S：先用一次立即完成的初始載入讓 CM6 就緒，再開始 A→B→A 的世代競態。
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/seed.feature' } })
    await flushEditor()
    const view = getView(w)

    const resolvers: Record<string, (v: { content: string; digest: string }) => void> = {}
    mocks.SpecRead.mockImplementation((path: string) => new Promise(r => { resolvers[path] = r }))

    await w.setProps({ path: 'spec/a.feature' }) // 第一次選 A（延遲）
    await flushPromises()
    const firstAResolve = resolvers['spec/a.feature']

    await w.setProps({ path: 'spec/b.feature' }) // 切到 B（延遲）
    await flushPromises()
    resolvers['spec/b.feature']({ content: 'B content', digest: 'sha256:b' })
    await flushEditor()
    expect(view.state.doc.toString()).toBe('B content')

    await w.setProps({ path: 'spec/a.feature' }) // 再切回 A（新世代，延遲）
    await flushPromises()
    resolvers['spec/a.feature']({ content: 'A content (second load)', digest: 'sha256:a2' }) // 第二次 A 的回應先到
    await flushEditor()
    expect(view.state.doc.toString()).toBe('A content (second load)')

    firstAResolve({ content: 'STALE first A', digest: 'sha256:a-stale' }) // 第一次 A 的延遲回應現在才到
    await flushPromises()
    expect(view.state.doc.toString()).toBe('A content (second load)') // 不被第一次的過期回應覆蓋
  })

  it('T8c-S：載入中編輯器真正暫停——裝飾性標記＋CM6 實際 contenteditable＋load 期間 dispatch 不回寫 buffer', async () => {
    // 先讓第一次載入正常完成，CM6 才會初始化（同 T8-S：onMounted 內 initEditor
    // 排在 loadFile 之後），才能拿到可觀察的 view 來斷言載入中的 contenteditable。
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushEditor()
    const view = getView(w)
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('true') // 初始載入完成後可編輯

    // 這次載入刻意「失敗」收尾：Spec 沒有像 Plan 那樣可直接讀的 store buffer，
    // 而 [data-test=save] 的 disabled 條件是 `busyReason !== '' || !dirty`——載入
    // 進行中必然 disabled，拿它斷言 buffer 保全會被 busy 遮蔽。失敗路徑會清掉
    // busy 但**不覆蓋 buffer**，因此解除遮蔽後 disabled 才是純粹的 dirty 訊號；
    // 也不能等成功載入覆蓋 buffer 後才驗，那會掩蓋載入中途的污染。
    let rejectRead: (e: unknown) => void = () => {}
    mocks.SpecRead.mockImplementationOnce(() => new Promise((_, rej) => { rejectRead = rej }))
    await w.setProps({ path: 'spec/b.feature' }) // 觸發第二次（延遲）載入
    await flushPromises()

    expect(w.find('[data-test=editor-host]').attributes('data-editing-suspended')).toBe('true') // 既有裝飾性標記
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('false') // CM6 實際可編輯狀態，不是我們自己掛的標記

    // 載入期間程式化 dispatch 改文件：EditorView.editable 只擋使用者輸入，仍要
    // 靠 updateListener 的 busyReason 檢查擋住回寫，buffer 才不會被污染。
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: 'injected during load' } })
    await flushPromises()

    rejectRead(new Error('load failed'))
    await flushEditor()

    expect(w.find('[data-test=editor-host]').attributes('data-editing-suspended')).toBeUndefined()
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('true') // 載入完成後回到可編輯
    // busy 已清且 buffer 未被載入結果覆蓋——此時 disabled 純粹反映 dirty：
    // 若 updateListener 在載入期間漏擋，buffer 會是 'injected during load' ≠ saved(A)，
    // dirty 為真、按鈕會 enabled，這條就會紅。
    expect(w.attributes('data-busy')).toBe('')
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // buffer 未被污染→仍等於 saved(A)

    // 載入完成後再 dispatch 一次：buffer 應正常更新
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: 'edited after load' } })
    await flushPromises()
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined() // buffer 已更新→dirty 真
  })

  it('T9-S：衝突——[data-test=save-error][data-conflict=true]，三者不變，不自動重載，dirty 依內容比較', async () => {
    const write = vi.fn().mockRejectedValue(new Error('write conflict: expected_digest does not match current file'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()

    const err = mustFind(w, '[data-test=save-error]')
    expect(err.text()).toContain('write conflict: expected_digest does not match current file')
    expect(err.attributes('data-conflict')).toBe('true')
    expect(mocks.SpecRead).toHaveBeenCalledTimes(1) // 未自動重新載入

    typeText(view, '') // 等待期間（此時已回應）把內容改回 saved 原內容
    await flushPromises()
    expect(mustFind(w, '[data-test=save-error]').exists()).toBe(true) // 錯誤仍顯示
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // dirty 依內容比較→假

    // 持有的 digest 不得被衝突分支動到。Spec 沒有可直接讀的 store，改以行為驗證：
    // 再編輯一次並儲存，第二次送出的 expectedDigest 必須仍是衝突前持有的原值。
    typeText(view, 'after conflict edit')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenNthCalledWith(2, 'spec/a.feature', 'after conflict edit', 'sha256:stub')
  })

  it('T10-S：非衝突錯誤——原文顯示且無 data-conflict，三者不變，dirty 依內容比較', async () => {
    const write = vi.fn().mockRejectedValue(new Error('boom: disk full'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()

    const err = mustFind(w, '[data-test=save-error]')
    expect(err.text()).toContain('boom: disk full')
    expect(err.attributes('data-conflict')).toBeUndefined()

    typeText(view, '')
    await flushPromises()
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined()
  })

  it('T14d：acceptDraft 等待期間續打——回應後 buffer 是續打內容、saved 是草稿內容→dirty 為真', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    await mustFind(w, '[data-test=accept-draft]').trigger('click') // 接受草稿：buffer 先被替換為草稿萃取結果並送出
    expect(write).toHaveBeenCalledWith('spec/a.feature', 'AI draft', 'sha256:stub')

    const view = getView(w)
    typeText(view, 'continued typing after accept') // 等待期間續打
    await flushPromises()
    resolveWrite('sha256:accepted')
    await flushPromises()

    // 回應後：buffer 是續打內容，saved 是草稿內容（AI draft）→ 兩者不同 → dirty 為真
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined()
  })

  it('T14e：acceptDraft 失敗——saved／digest 不變、buffer 保留接受後內容', async () => {
    const write = vi.fn().mockRejectedValue(new Error('boom'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    await mustFind(w, '[data-test=accept-draft]').trigger('click')
    await flushPromises()

    const view = getView(w)
    expect(view.state.doc.toString()).toBe('AI draft') // buffer（編輯器內容）保留接受後內容，即使失敗
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeUndefined() // buffer≠saved(原內容'')→ dirty 真
  })
})

// A1a-2 expected-red 階段：切檔守衛（unsaved-changes navigation guard）尚未實作
// ——selectFile() 目前沒有任何 dirty 檢查（見 SpecWorkspace.vue 第 197-202 行），
// 外部（非寫入中）dirty 時點清單切檔會直接切換，不會出現 keep／discard 選擇。
// 本 describe 斷言「將來會提供」的 [data-test=unsaved-guard]／
// [data-test=unsaved-discard]／[data-test=unsaved-keep] 介面，現在預期失敗
// （R），這是 TDD 紅燈階段的正常狀態，不是測試寫錯。G6-S 例外：驗證 A1a-1 的
// 儲存中互斥（busyReason==='save'）仍然優先於切檔守衛而直接拒絕切檔——這條
// precedence 已經是現有程式碼的行為，本來就該綠，不是本次要打開的紅燈缺口。
describe('SpecWorkspace 切檔守衛（A1a-2，expected-red）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([
      { name: 'a.feature', path: 'spec/a.feature', isDir: false },
      { name: 'b.feature', path: 'spec/b.feature', isDir: false },
    ])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('G1-S：dirty 時點擊清單切檔→[data-test=unsaved-guard] 出現，新檔的 read 尚未被呼叫', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: 'a original', digest: 'sha256:a0' },
      'spec/b.feature': { content: 'b original', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'a edited') // dirty：fileContent ≠ savedContent
    await flushPromises()

    await findFileButton(w, 'b.feature').trigger('click')
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(store.read).not.toHaveBeenCalledWith('spec/b.feature') // 守衛出現前不得先載入新檔
    expect(view.state.doc.toString()).toBe('a edited') // 編輯器仍停在原檔未儲存內容
  })

  it('G2-S：G1 情境下點擊 discard→新檔正式載入，read 被呼叫且編輯器內容變成新檔內容', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: 'a original', digest: 'sha256:a0' },
      'spec/b.feature': { content: 'b original', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushEditor()
    typeText(getView(w), 'a edited')
    await flushPromises()
    await findFileButton(w, 'b.feature').trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushEditor()

    expect(store.read).toHaveBeenCalledWith('spec/b.feature')
    expect(getView(w).state.doc.toString()).toBe('b original')
  })

  it('G3-S：G1 情境下點擊 keep→維持原檔，read 未被呼叫，編輯器仍是未儲存內容', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: 'a original', digest: 'sha256:a0' },
      'spec/b.feature': { content: 'b original', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushEditor()
    typeText(getView(w), 'a edited')
    await flushPromises()
    await findFileButton(w, 'b.feature').trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()

    expect(store.read).not.toHaveBeenCalledWith('spec/b.feature')
    expect(getView(w).state.doc.toString()).toBe('a edited') // 停留在原檔的未儲存內容
  })

  // G6-S-save-selectfile／G6-S-accept-selectfile：owner 裁定（round 2）——G6 要
  // 涵蓋 SpecWorkspace 每一種寫入等待狀態（busyReason 'save'／'accept'），不只
  // save，理由是保護 A1a-1 既有的儲存互斥契約不被新的保留／捨棄流程繞過。兩條
  // 都預期綠：不是「守衛缺元素」的紅燈缺口，而是「busyReason!=='' 時 selectFile()
  // 直接 return（見 SpecWorkspace.vue 197-202 行）已經先擋下了，dirty-guard 邏輯
  // 根本沒機會執行——所以現在就是綠，且綠得有意義（precedence 驗證，不是誤判）。
  it('G6-S-save-selectfile：A1a-1 優先——save 進行中同時 dirty，切檔被 A1a-1 直接拒絕，不進切檔守衛選擇（precedence 已實作，本條預期綠）', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'a edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click') // 觸發 save，尚未 resolve → busyReason='save'
    expect(w.attributes('data-busy')).toBe('save')

    await findFileButton(w, 'b.feature').trigger('click') // 儲存中嘗試切檔（同時 dirty）
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false) // A1a-1 儲存互斥直接擋下，不出現守衛選擇
    expect(w.attributes('data-busy')).toBe('save') // busy 未變
    expect(mocks.SpecRead).toHaveBeenCalledTimes(1) // 只有初次載入，切檔未觸發任何讀取
    expect(view.state.doc.toString()).toBe('a edited') // 內容未變

    resolveWrite('sha256:new')
    await flushPromises()
  })

  it('G6-S-accept-selectfile：A1a-1 優先——accept 進行中同時 dirty，切檔被 A1a-1 直接拒絕，不進切檔守衛選擇（precedence 已實作，本條預期綠）', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    await mustFind(w, '[data-test=accept-draft]').trigger('click') // 觸發 acceptDraft，尚未 resolve → busyReason='accept'
    expect(w.attributes('data-busy')).toBe('accept')
    // dirty：acceptDraft 已把 buffer 換成草稿內容（'AI draft'），savedContent 仍是初始載入的
    // 空字串，write 尚未 resolve，兩者不同——與 save 案例一樣同時具備 busy 與 dirty。
    expect(getView(w).state.doc.toString()).toBe('AI draft')

    await findFileButton(w, 'b.feature').trigger('click') // accept 進行中嘗試切檔（同時 dirty）
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false) // A1a-1 的 accept 互斥直接擋下，不出現守衛選擇
    expect(w.attributes('data-busy')).toBe('accept') // busy 未變
    expect(mocks.SpecRead).toHaveBeenCalledTimes(1) // 只有初次載入，切檔未觸發任何讀取
    expect(getView(w).state.doc.toString()).toBe('AI draft') // 內容未變

    resolveWrite('sha256:new')
    await flushPromises()
  })
  it('G7-S：dirty 發送端——真實輸入→true、改回 saved→false、儲存成功→false（不是只驗接收端）', async () => {
    const store = makeFileStore({ 'spec/a.feature': { content: 'a original', digest: 'sha256:a0' } })
    mocks.SpecRead.mockImplementation(store.read)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write: store.write } })
    await flushEditor()
    const seen = () => (w.emitted('dirty') ?? []).map(e => (e as unknown[])[0])
    expect(seen().at(-1)).toBe(false) // 載入後乾淨

    const view = getView(w)
    typeText(view, 'a edited') // 真實輸入
    await flushPromises()
    expect(seen().at(-1)).toBe(true)

    typeText(view, 'a original') // 改回與 saved 相同
    await flushPromises()
    expect(seen().at(-1)).toBe(false)

    typeText(view, 'a edited again')
    await flushPromises()
    expect(seen().at(-1)).toBe(true)
    await mustFind(w, '[data-test=save]').trigger('click') // 儲存成功
    await flushPromises()
    expect(seen().at(-1)).toBe(false)
  })

})
