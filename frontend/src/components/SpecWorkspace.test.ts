import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
import SpecWorkspace from './SpecWorkspace.vue'
import { useAssist } from '../stores/assist'
import { mountWithI18n } from '../test/i18n'
import zhTW from '../i18n/locales/zh-TW'

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
    // A1b-1：onMounted 的初次載入要先完成（fileDigest 才有值），否則會和新增的
    // 寫入前預檢競態——原本 write() 在 acceptDraft 內同步呼叫，沒有這個風險；
    // 現在 acceptDraft 多了一次 await SpecRead 才呼叫 writer，需等載入穩定。
    await flushEditor()
    expect(write).not.toHaveBeenCalled() // 草稿不自動寫檔
    await w.find('[data-test=accept-draft]').trigger('click')
    await flushPromises() // A1b-1：accept 現在會先做一次寫入前預檢 SpecRead，等它 resolve 才輪到 writer
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

// A2-1：submit prop 注入——SpecWorkspace 送核改由 App 注入包裝版本（沿 write prop
// 慣例），未注入時才回退直呼 SubmitForApproval。specAssist 不注入（D7：無 blocker 路徑）。
describe('SpecWorkspace submit prop 注入（A2-1）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('注入 submit prop 時送核走 prop，不走 wailsjs 直呼（A2-1：重載責任在 App）', async () => {
    const submit = vi.fn(async () => 'approval-9')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', submit } })
    await flushPromises()
    await w.find('[data-test=submit-for-approval]').trigger('click')
    await flushPromises()
    expect(submit).toHaveBeenCalledTimes(1)
    expect(mocks.SubmitForApproval).not.toHaveBeenCalled()
  })

  // D3：Spec 專門回歸——失敗後重試成功，同類錯誤清除、其他操作的錯誤保留。
  // 「其他操作」用 preview-commit（SpecWorkspace.vue:439，只在 commitBusy 時 disabled，
  // 不需先弄 dirty）；save 按鈕在非 dirty 時 disabled（:414），不適合當第二操作。
  it('送核失敗→預覽 commit 失敗→再送核成功：送核錯誤清空、commit 錯誤保留（A2 原則 1／2，Spec 同型）', async () => {
    const submit = vi.fn()
      .mockRejectedValueOnce(new Error('spec: dirty tree'))
      .mockResolvedValueOnce('approval-10')
    mocks.PreviewSpecCommit.mockRejectedValueOnce(new Error('commit: nothing to commit'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', submit } })
    await flushPromises()
    await w.find('[data-test=submit-for-approval]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('spec: dirty tree')
    await w.find('[data-test=preview-commit]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('commit: nothing to commit')
    await w.find('[data-test=submit-for-approval]').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain('spec: dirty tree')
    expect(w.text()).toContain('commit: nothing to commit')
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
    await flushPromises() // A1b-1：save 先做一次寫入前預檢 SpecRead，等它 resolve 才輪到 writer
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
    await flushPromises() // A1b-1：save 先做一次寫入前預檢 SpecRead，等它 resolve 才輪到 writer（見下方 readCallsAfterPrecheck）
    expect(write).toHaveBeenCalledTimes(1)
    expect(w.attributes('data-busy')).toBe('save')
    const readCallsAfterPrecheck = mocks.SpecRead.mock.calls.length // mount 一次＋此次 save 的預檢一次

    await mustFind(w, '[data-test=save]').trigger('click') // 再次儲存
    await mustFind(w, '[data-test=accept-draft]').trigger('click') // 接受草稿
    await w.setProps({ path: 'spec/b.feature' }) // 切檔
    await flushPromises()

    expect(write).toHaveBeenCalledTimes(1) // 未再被呼叫
    expect(mocks.SpecRead).toHaveBeenCalledTimes(readCallsAfterPrecheck) // 切檔未觸發任何額外讀取（重載或預檢）

    resolveWrite('sha256:new')
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('')

    // 歸屬斷言（缺口 3 修正）：儲存中把 props.path 換成 B 被拒絕，effectivePath
    // 解封後仍須維持 A（不得被切檔期間的 props.path 污染）——再次編輯並儲存，
    // write 收到的 path／content／digest 都必須屬於 A。
    // A1b-1：這次 save 也會先做一次寫入前預檢——beforeEach 的預設 SpecRead mock
    // 固定回傳 sha256:stub，不會跟著第一次寫入更新，須另外排一次符合目前寫入
    // 基準（sha256:new）的回應，預檢才會通過。
    mocks.SpecRead.mockResolvedValueOnce({ content: 'v1', digest: 'sha256:new' })
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

  // A1b-1／裁定 18：新增的寫入前預檢與「SpecRead 總次數==1」不相容（見
  // docs/superpowers/plans/2026-09-09-a1b-external-change-reload-compare.md
  // §2.11）——本條改造為「情境 A：預檢成功後才發生的後端衝突」，計次基準改為
  // 「mount 一次＋本次 save 的預檢一次」，行為斷言（三者不變、不自動重載、
  // dirty 依內容比較、data-conflict）維持不變。
  it('T9-S(情境A)：預檢成功後端衝突——[data-test=save-error][data-conflict=true]，三者不變，不自動重載，dirty 依內容比較', async () => {
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
    // 情境 A：預檢本身成功（磁碟 digest 與快照相符，預設 mock 皆回傳 sha256:stub）
    // 才進到 writer 並在後端遇到衝突——不觸發任何「額外」重新載入：
    // mount 一次＋本次 save 的預檢一次＝2，不因後端衝突多讀一次。
    expect(mocks.SpecRead).toHaveBeenCalledTimes(2)

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
    await flushPromises() // A1b-1：accept 先做一次寫入前預檢 SpecRead，等它 resolve 才輪到 writer
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

  // 情境 B（T9 相容，裁定 18）：與情境 A（見上方 T9-S）互補——這裡是寫入前預檢
  // 本身就發現外部變更（磁碟 digest 與按下時固定的快照不符），writer 根本不被
  // 呼叫；同時滿足「兩入口」要求的 saveFile 一案。
  it('T9-S(情境B)／兩入口-save：saveFile 寫入前預檢發現外部變更——中止，writer 呼叫次數 0，三者不變，顯示中止原因', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: '', digest: 'sha256:stub' }) // mount 載入
      .mockResolvedValueOnce({ content: 'changed on disk', digest: 'sha256:EXTERNAL' }) // save 的寫入前預檢：磁碟已變
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()

    expect(write).not.toHaveBeenCalled() // 不看當下 dirty，直接中止，writer 根本不被呼叫
    expect(view.state.doc.toString()).toBe('edited') // fileContent 未被中止邏輯改動
    // 修正輪修正 2：預檢中止改用獨立呈現點 external-abort，save-error 只留給
    // writer 真的被呼叫後才失敗的情況——這裡 writer 從未被呼叫，不得出現 save-error。
    expect(mustFind(w, '[data-test=external-abort]').text()).toBe(zhTW.externalChange.writeAborted)
    expect(w.find('[data-test=save-error]').exists()).toBe(false)

    // 三者不變的行為驗證（Spec 沒有可直接讀的 store）：再次儲存，磁碟這次核對通過
    // （預設 mock 回傳 sha256:stub，與寫入基準相符——證明 fileDigest 仍是中止前
    // 持有的原值，沒有被中止分支動到），writer 應被正常呼叫。
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenCalledWith('spec/a.feature', 'edited', 'sha256:stub')
  })

  // 兩入口-accept：acceptDraft 的寫入前預檢版本，證明中止同樣擋住這個寫入入口
  // （spec 側最容易漏的一個——acceptDraft 也會實際寫檔，不是只改 buffer）。
  it('兩入口-accept：acceptDraft 寫入前預檢發現外部變更——中止，writer 呼叫次數 0，顯示中止原因', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: '', digest: 'sha256:stub' }) // mount 載入
      .mockResolvedValueOnce({ content: 'changed on disk', digest: 'sha256:EXTERNAL' }) // accept 的寫入前預檢：磁碟已變
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    await mustFind(w, '[data-test=accept-draft]').trigger('click')
    await flushPromises()

    expect(write).not.toHaveBeenCalled()
    // fileContent 在 acceptDraft 一開始就同步換成草稿萃取結果（既有 T14e 行為，
    // 不因中止回捲）；savedContent／fileDigest 未變則透過 write 未被呼叫佐證。
    expect(getView(w).state.doc.toString()).toBe('AI draft')
    // 修正輪修正 2：預檢中止改用獨立呈現點 external-abort，accept-error 只留給
    // writer 真的被呼叫後才失敗的情況。
    expect(mustFind(w, '[data-test=external-abort]').text()).toBe(zhTW.externalChange.writeAborted)
    expect(w.find('[data-test=accept-error]').exists()).toBe(false)
  })

  // 條款 12：預檢等待期間使用者把 buffer 改回 savedContent（dirty→false），
  // 磁碟這次核對仍不符——仍須中止，不得因為「當下已經不 dirty」就轉為自動重載。
  it('條款12：預檢等待期間改回原內容（dirty→false）——磁碟不符仍中止，writer 呼叫次數 0，不自動重載', async () => {
    let resolvePrecheck: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:stub' }) // mount 載入
      .mockImplementationOnce(() => new Promise(res => { resolvePrecheck = res })) // save 的寫入前預檢：延遲
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click') // 取得所有權，預檢尚未 resolve
    await flushPromises()

    typeText(view, 'orig') // 等待期間把內容改回 savedContent——此刻 dirty 應為假
    await flushPromises()
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // 佐證：當下 dirty 確實已變假

    resolvePrecheck({ content: 'changed on disk', digest: 'sha256:EXTERNAL' }) // 磁碟仍與固定快照的 digest 不符
    await flushPromises()

    expect(write).not.toHaveBeenCalled() // 不因當下 dirty 變假就轉為自動重載，仍中止
    expect(view.state.doc.toString()).toBe('orig') // buffer 保留使用者操作結果，未被中止邏輯覆寫
    // 修正輪修正 2：預檢中止改用獨立呈現點 external-abort。
    expect(mustFind(w, '[data-test=external-abort]').text()).toBe(zhTW.externalChange.writeAborted)
    expect(w.find('[data-test=save-error]').exists()).toBe(false)
  })

  // C1（反例）：寫入已送出後、期間視窗取得焦點——前景所有權存在時背景檢查一律
  // 略過（不發出、不推進世代），寫入成功回應仍正確套用（savedContent／寫入基準
  // 確實更新，不被背景讀取淘汰）。
  it('C1：寫入已送出→期間觸發 window focus→寫入成功回應仍被套用（savedContent／寫入基準確實更新）', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    // 直接呼叫元件掛上的 focus handler（而非 window.dispatchEvent 全域廣播）——
    // 本檔內其他測試掛載的元件未必 unmount，全域 dispatch 會連帶觸發那些殘留
    // 監聽、讓呼叫次數不可預期；spy addEventListener 抓「這個」元件實際註冊的
    // handler 才是穩定斷言（同 PlanWorkspace.test.ts 既有慣例）。
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined
    expect(focusHandler).toBeTypeOf('function')

    typeText(view, 'v1')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click') // 取得前景所有權，寫入尚未 resolve
    await flushPromises()
    expect(write).toHaveBeenCalledTimes(1)

    focusHandler?.() // 儲存等待期間視窗取得焦點
    await flushPromises()
    // mount(1)＋save 自己的預檢(1)——focus 觸發的背景檢查被所有權擋下，未發出任何讀取
    expect(mocks.SpecRead).toHaveBeenCalledTimes(2)

    resolveWrite('sha256:new')
    await flushPromises()

    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // savedContent 已更新為 v1，dirty 為假
    // 再次編輯／儲存，驗證寫入基準確實是 sha256:new（送出的 expectedDigest 是新值）。
    // beforeEach 的預設 SpecRead mock 固定回傳 sha256:stub，不會跟著上次寫入更新，
    // 這次 save 的預檢須另外排一次符合目前寫入基準的回應才能通過。
    mocks.SpecRead.mockResolvedValueOnce({ content: 'v1', digest: 'sha256:new' })
    typeText(view, 'v2')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenNthCalledWith(2, 'spec/a.feature', 'v2', 'sha256:new')

    w.unmount()
    addSpy.mockRestore()
  })

  // C3：前景開始「前」已在途的背景讀取——儲存完成、寫入基準已更新、busy 已清
  // 之後，舊背景讀取才返回：不能只看 busy（已清）判過期，須靠 B1（世代，前景
  // acquire 當下即失效）與 B4（基準版本，發出時基準已與目前不同）擋下。
  // 成功與失敗各一案（C3-a／C3-b）。
  it('C3-a：舊背景讀取在儲存完成後才成功返回——完全不改 buffer／savedContent／寫入基準／錯誤', async () => {
    let resolveBg: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise(res => { resolveBg = res })) // 背景檢查（focus 觸發，延遲）
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // save 自己的預檢：磁碟仍與快照相符
    const write = vi.fn().mockResolvedValue('sha256:d1')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.() // 觸發背景檢查（尚未 resolve）
    await flushPromises()

    typeText(view, 'edited before save')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click') // 取得所有權→背景世代立即失效（B1）
    await flushPromises() // 讓 save 自己的預檢＋write 完成：savedContent／寫入基準已更新，busy 已清

    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // 已存檔，dirty 為假

    resolveBg({ content: 'STALE background content', digest: 'sha256:STALE' }) // 舊背景讀取現在才成功返回
    await flushPromises()

    expect(view.state.doc.toString()).toBe('edited before save') // buffer 未被覆蓋
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // savedContent／寫入基準未被動到，仍是「已存檔」狀態
    expect(w.find('[data-test=save-error]').exists()).toBe(false) // 沒有任何錯誤跑出來
    expect(w.find('[data-test=external-change]').exists()).toBe(false) // 也沒有背景通知

    w.unmount()
    addSpy.mockRestore()
  })

  it('C3-b：舊背景讀取在儲存完成後才失敗返回——完全不改 buffer／savedContent／寫入基準／錯誤', async () => {
    let rejectBg: (e: unknown) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectBg = rej })) // 背景檢查（focus 觸發，延遲）
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // save 自己的預檢：磁碟仍與快照相符
    const write = vi.fn().mockResolvedValue('sha256:d1')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    typeText(view, 'edited before save')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()

    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined()

    rejectBg(new Error('stale background read boom')) // 舊背景讀取現在才失敗返回
    await flushPromises()

    expect(view.state.doc.toString()).toBe('edited before save')
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined()
    expect(w.text()).not.toContain('stale background read boom') // 舊失敗不得顯示
    expect(w.find('[data-test=save-error]').exists()).toBe(false)
    expect(w.find('[data-test=external-change]').exists()).toBe(false)

    w.unmount()
    addSpy.mockRestore()
  })

  // C2（反例）：新的背景檢查已成功套用（本案為「已重載」，比「靜默已同步」更
  // 具觀察性）之後，較舊一次背景檢查的失敗回應才到——完全 no-op，不得顯示該
  // 錯誤，也不得改動已經套用的結果。
  it('C2：新背景檢查已套用成功結果後，舊背景讀取失敗回應才到——狀態不變、不顯示該錯誤', async () => {
    let rejectOld: (e: unknown) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectOld = rej })) // 第一次背景檢查（延遲、稍後失敗）
      .mockResolvedValueOnce({ content: 'new on disk', digest: 'sha256:d1' }) // 第二次背景檢查（立即成功，無未儲存內容→自動重載）
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.() // 第一次背景檢查（尚未 resolve）
    await flushPromises()
    focusHandler?.() // 第二次背景檢查（世代較新，立即成功）
    await flushPromises()

    expect(view.state.doc.toString()).toBe('new on disk') // 已依第二次的結果自動重載
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.notice)

    rejectOld(new Error('stale background read boom')) // 第一次的延遲失敗現在才到
    await flushPromises()

    expect(view.state.doc.toString()).toBe('new on disk') // 未被舊失敗回應改動
    expect(w.text()).not.toContain('stale background read boom') // 不顯示該錯誤
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.notice) // 通知內容未變

    w.unmount()
    addSpy.mockRestore()
  })

  // 無訂閱仍有效（修正輪修正 3：EventsOn('spec:changed', …) 訂閱與只寫不讀的
  // externalChangeSignal 旗標已整段移除——沒有消費端就不該留）：SpecWorkspace
  // 現在完全不訂閱任何事件，三個檢查點（回到工作區／視窗取得焦點／寫入前）仍
  // 無條件重讀並比對 digest，證明檢查從未依賴過任何事件標記。
  it('無訂閱仍有效：SpecWorkspace 已無任何事件訂閱，focus 檢查點仍能偵測到外部變更', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' })
      .mockResolvedValueOnce({ content: 'disk updated', digest: 'sha256:d1' })
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    expect(view.state.doc.toString()).toBe('disk updated')
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.notice)

    w.unmount()
    addSpy.mockRestore()
  })

  // 背景自動重載：無未儲存內容且核對通過（磁碟 digest 與目前寫入基準不符）
  // → 載入磁碟新內容並顯示 notice；寫入基準同步更新（重載後不再 dirty）。
  it('背景自動重載：無未儲存內容且核對通過——載入磁碟新內容＋顯示 notice', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' })
      .mockResolvedValueOnce({ content: 'disk updated', digest: 'sha256:d1' })
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    expect(view.state.doc.toString()).toBe('disk updated')
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.notice)
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // 重載後 saved===buffer→非 dirty

    w.unmount()
    addSpy.mockRestore()
  })

  // 背景檢查：有未儲存內容時不得覆寫，只提示 detected（三選一屬 A1b-2，本輪不做）。
  it('背景檢查發現變更但有未儲存內容——不覆寫，顯示 detected', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' })
      .mockResolvedValueOnce({ content: 'disk updated', digest: 'sha256:d1' })
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    typeText(view, 'unsaved local edit')
    await flushPromises()
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    expect(view.state.doc.toString()).toBe('unsaved local edit') // 不覆寫
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.detected)

    w.unmount()
    addSpy.mockRestore()
  })

  // 讀取失敗／已刪除（限當前有效檢查）：三者保留、顯示原始訊息、不判為已同步。
  it('背景檢查讀取失敗——三者保留、顯示原始訊息、不判為已同步', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' })
      .mockRejectedValueOnce(new Error('disk unreachable boom'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    expect(view.state.doc.toString()).toBe('orig') // fileContent 不變
    expect(mustFind(w, '[data-test=save]').attributes('disabled')).toBeDefined() // savedContent／寫入基準未變→非 dirty
    expect(mustFind(w, '[data-test=external-change]').text()).toContain('disk unreachable boom') // 原始訊息

    w.unmount()
    addSpy.mockRestore()
  })

  it('背景檢查判定檔案已刪除——顯示刪除訊息、不清空內容', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' })
      .mockRejectedValueOnce(new Error('open spec/a.feature: no such file or directory'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    expect(view.state.doc.toString()).toBe('orig') // 不清空編輯內容
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.deleted)

    w.unmount()
    addSpy.mockRestore()
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
    // A1b-1：mount(1)＋save 自己的寫入前預檢(1)——切檔本身未觸發任何額外讀取
    expect(mocks.SpecRead).toHaveBeenCalledTimes(2)
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
    // A1b-1：mount(1)＋accept 自己的寫入前預檢(1)——切檔本身未觸發任何額外讀取
    expect(mocks.SpecRead).toHaveBeenCalledTimes(2)
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

  it('H1-S：確認框開啟後才開始寫入——此時點捨棄不得切檔；寫入結束後才可切', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: 'a original', digest: 'sha256:a0' },
      'spec/b.feature': { content: 'b original', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    typeText(getView(w), 'a edited')
    await flushPromises()

    await findFileButton(w, 'b.feature').trigger('click')
    await flushPromises()
    mustFind(w, '[data-test=unsaved-guard]')

    await mustFind(w, '[data-test=save]').trigger('click') // 確認框開著時按儲存
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('save')
    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()
    expect(store.read).not.toHaveBeenCalledWith('spec/b.feature') // 寫入中不得切檔
    mustFind(w, '[data-test=unsaved-guard]') // 確認框保留

    resolveWrite('sha256:a1')
    await flushPromises()
    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()
    expect(store.read).toHaveBeenCalledWith('spec/b.feature') // 寫入結束後才切
  })

  it('H1-S-accept：確認框開啟後才開始接受草稿——此時點捨棄不得切檔；寫入回應後才可切', async () => {
    const store = makeFileStore({
      'spec/a.feature': { content: 'a original', digest: 'sha256:a0' },
      'spec/b.feature': { content: 'b original', digest: 'sha256:b0' },
    })
    mocks.SpecRead.mockImplementation(store.read)
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', draft: 'AI draft', write } })
    await flushEditor()
    typeText(getView(w), 'a edited')
    await flushPromises()

    await findFileButton(w, 'b.feature').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(true)

    await mustFind(w, '[data-test=accept-draft]').trigger('click') // 確認框開著時接受草稿
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('accept')

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()
    expect(store.read).not.toHaveBeenCalledWith('spec/b.feature') // accept 等待中不得切檔
    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(true)

    resolveWrite('sha256:a1')
    await flushPromises()
    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushPromises()
    expect(store.read).toHaveBeenCalledWith('spec/b.feature') // accept 結束後才切
  })

})

// A1b-1 整合複核補測（controller 審查時補，非 agent 產出）：這兩條各自對應一個
// 在 code review 才發現、Plan 側沒有而 Spec 側有的缺陷。兩條都必須在修正前為紅。
describe('SpecWorkspace 外部檔案變更——B2 與換檔殘留（整合複核補測）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })
  // 測試失敗時，it 內最後面的 mockRestore 不會執行，spy 會殘留到下一條並讓它紅在
  // 前置條件（找不到 focus handler）而不是正題。負控制要能一次只證一個命題，清理
  // 必須放在 afterEach。
  afterEach(() => { vi.restoreAllMocks() })

  // 切檔的整體保護：背景讀取對 A 發出後，操作者切到 B，A 的回應才返回——不得被
  // 套用到 B 上。
  //
  // ★ 鑑別性範圍（勿再誇大）：本案**不是** B2 的隔離證明。目前正式切檔路徑
  // （selectFile／watch(props.path)／unsavedDiscard）**全部經過 loadFile()**，
  // 而 loadFile() 開頭就呼叫 guard.invalidateBackground() 同步推進 B1——
  // 因此**單獨移除 B2 不會使本測試失敗**（實測：把比對快照改回發出時捕捉的
  // path 後本檔仍 47 條全過）。B2 比較規則本身由模組測試
  // externalChangeGuard.test.ts 獨立驗證；呼叫端是否傳入即時路徑由程式碼審查確認。
  // 本案驗的是「切檔後過期背景回應不得套用」這個整合行為。
  it('切檔後過期背景回應不得套用——A 的回應在切到 B 之後才返回', async () => {
    let resolveBg: (v: { content: string; digest: string }) => void = () => {}
    let bgIssued = false
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'same', digest: 'sha256:same' }) // mount 載入 a
      .mockImplementationOnce(() => new Promise(res => { bgIssued = true; resolveBg = res })) // 背景檢查（延遲）
      .mockResolvedValueOnce({ content: 'same', digest: 'sha256:same' }) // 切檔後載入 b
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    // ★ 前置必須成立才算數：handler 存在且背景讀取「確實已發出」。
    // 用 focusHandler?.() 而不斷言，會讓 handler 為 undefined 時整個測試空轉仍通過。
    expect(focusHandler, 'focus handler 必須已註冊，否則本測試等於沒跑').toBeTypeOf('function')
    focusHandler!()
    await flushPromises()
    expect(bgIssued, '背景讀取必須確實已發出').toBe(true)
    const readsBeforeSwitch = mocks.SpecRead.mock.calls.length

    await w.setProps({ path: 'spec/b.feature' }) // 期間切檔
    await flushPromises()
    expect(view.state.doc.toString()).toBe('same')
    // ★ 回應確實延遲到切檔完成之後才返回：切檔已觸發自己的讀取，但 A 的背景讀取仍未 resolve
    expect(mocks.SpecRead.mock.calls.length).toBeGreaterThan(readsBeforeSwitch)

    resolveBg({ content: 'DISK CONTENT OF FILE A', digest: 'sha256:changed' }) // a 的舊回應現在才到
    await flushPromises()

    expect(view.state.doc.toString()).toBe('same') // 不得被套用到 b
    expect(w.find('[data-test=external-change]').exists()).toBe(false) // 也不得對 b 發出變更提示

    w.unmount()
    addSpy.mockRestore()
  })

  // 換檔後仍留著上一個檔的外部變更訊息，會被讀成「新檔被外部改動」——訊息歸屬錯誤。
  it('換檔：上一個檔的外部變更提示不得留在新檔畫面上', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入 a
      .mockResolvedValueOnce({ content: 'disk updated', digest: 'sha256:d1' }) // 背景檢查：a 被外部改動
      .mockResolvedValueOnce({ content: 'file b', digest: 'sha256:d2' }) // 切檔後載入 b
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()
    expect(mustFind(w, '[data-test=external-change]').text()).toBe(zhTW.externalChange.notice) // a 的提示已出現

    await w.setProps({ path: 'spec/b.feature' })
    await flushPromises()

    expect(getView(w).state.doc.toString()).toBe('file b')
    expect(w.find('[data-test=external-change]').exists()).toBe(false) // a 的提示不得殘留在 b 上

    w.unmount()
    addSpy.mockRestore()
  })
})

// A1b-1 Spec 側修正輪（owner code review 後）：修正 1（B3 卸載保護）／修正 2
// （中止訊息獨立呈現點＋清除時機）／修正 4（背景檢查須處理既有 load busy）
// 的補測。修正 3（移除 EventsOn 訂閱與 externalChangeSignal）由上方「無訂閱
// 仍有效」測試覆蓋，不在此重複。
describe('SpecWorkspace 外部檔案變更修正輪（owner code review 後）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([
      { name: 'a.feature', path: 'spec/a.feature', isDir: false },
    ])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })
  afterEach(() => { vi.restoreAllMocks() })

  // 修正 1：dispose() 不改變 instanceId 與世代，四欄比對在卸載後仍可能全等——
  // 必須另外查 guard.isActive()。用「無未儲存內容→會自動重載」這條分支最具
  // 觀察性：若 B3 沒擋住，syncEditorDoc() 會呼叫 cmView.dispatch()，即使
  // cmView 已 destroy()，CM6 的 update() 仍會把新內容寫進 view.state（見
  // @codemirror/view 原始碼 update()：`if (this.destroyed) { this.viewState.state
  // = state; return }`）——因此 view.state.doc 是否被改動，是判定「有沒有套用」
  // 最直接可觀察的證據，不靠讀取元件內部（無法讀，卸載後 script setup 的
  // local ref 不對外曝露）。
  it('卸載-成功：背景讀取在途時卸載→讀取以成功返回→不得對已卸載元件套用（CM6 buffer 不變）', async () => {
    let resolveBg: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise(res => { resolveBg = res })) // 背景檢查（延遲）
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w) // 卸載前先取得 view 參考——物件本身不隨卸載被回收，之後仍可讀 view.state
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.() // 發出背景讀取，尚未 resolve
    await flushPromises()

    w.unmount() // 卸載：guard.dispose() 執行，isActive() 之後回 false
    resolveBg({ content: 'disk updated after unmount', digest: 'sha256:after' }) // 讀取現在才成功返回
    await flushPromises() // 不應拋出任何例外

    expect(view.state.doc.toString()).toBe('orig') // 未被套用——B3 擋下卸載後才到的成功回應

    addSpy.mockRestore()
  })

  // 修正 1（失敗路徑）。★ 鑑別性範圍（owner 2026-09-14 裁定的措辭，勿擴大）：
  // **本案驗證不拋例外與 CM6 內容不變，未獨立驗證卸載後沒有內部狀態寫入；
  // 失敗路徑另有共用檢查點的程式碼審查支持。**
  //
  // 前一版註解寫「讀取失敗分支原本就只寫一個未被消費的內部旗標」——**該敘述不成立**，
  // 已更正：失敗分支同時寫 `syncState` 與 `externalChangeNotice`（SpecWorkspace.vue:162-167），
  // 而後者是**會被模板消費**的 reactive ref（同檔 :545 的 [data-test=external-change]）。
  // 共用的檢查點是 `if (!guard.isActive() || !shouldApplyBackground(...)) return`
  // （SpecWorkspace.vue:160），成功／失敗兩條路徑都在**狀態更新之前**被它擋下。
  // 條款 10 因此記為「部分驗證」，owner 已同意以**明列驗證例外**接受剩餘風險，
  // 不為此新增產品診斷介面或重構。
  it('卸載-失敗：背景讀取在途時卸載→讀取以失敗返回→不得拋出例外、CM6 buffer 不變', async () => {
    let rejectBg: (e: unknown) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectBg = rej })) // 背景檢查（延遲）
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.()
    await flushPromises()

    w.unmount()
    rejectBg(new Error('stale background read after unmount boom'))
    await flushPromises() // 不應拋出任何例外／不應有 unhandled rejection

    expect(view.state.doc.toString()).toBe('orig') // buffer 未被觸碰

    addSpy.mockRestore()
  })

  // 修正 4：canStartBackgroundRead() 只認得 guard 前景所有權，loadFile 的
  // busy='load' 未呼叫 guard.acquire()，光靠所有權擋不住「發出時基準 X、reload
  // 後又是同一個 X」這種情況——B2（路徑）與 B4（基準 digest）都不變，唯一能擋
  // 下來的只有 B1（世代），必須靠 loadFile() 開頭的 guard.invalidateBackground()
  // 才會被推進。刻意讓 reload 後內容／digest 與 reload 前完全相同，關掉 B2／B4
  // 這兩條防線，只留 B1 單獨受測——這正是「loadFile 已結束、busy 已清之後回應
  // 才到」這個反例最刁鑽的形狀。
  it('load 交錯：背景讀取在途時觸發同檔重載→重載完成（busy 已清）後背景讀取才返回→不得套用、不得出現外部變更提示', async () => {
    let resolveBg: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // mount 載入
      .mockImplementationOnce(() => new Promise(res => { resolveBg = res })) // 背景檢查（延遲）
      .mockResolvedValueOnce({ content: 'orig', digest: 'sha256:d0' }) // 同檔重載：內容／digest 與重載前完全相同
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    const addSpy = vi.spyOn(window, 'addEventListener')
    await flushEditor()
    const view = getView(w)
    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined

    focusHandler?.() // 對 a 發出背景讀取，尚未返回
    await flushPromises()

    await findFileButton(w, 'a.feature').trigger('click') // 重新選同一個檔→觸發 loadFile() 重載
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('') // 重載已完成，busy 已清

    resolveBg({ content: 'STALE background content', digest: 'sha256:STALE' }) // 背景讀取現在才返回
    await flushPromises()

    expect(view.state.doc.toString()).toBe('orig') // 未被套用
    expect(w.find('[data-test=external-change]').exists()).toBe(false) // 也不得出現變更提示

    w.unmount()
    addSpy.mockRestore()
  })

  // 修正 2（清除時機 a）：新操作一開始（saveFile 開頭）就清掉上次的中止提示，
  // 不必等這次的預檢／寫入結果——用延遲的第二次預檢證明清除發生在「按下當下」
  // 而不是「這次操作也成功之後」。
  it('中止訊息清除(a)：再次發起寫入時，abort 訊息在操作開始當下就先被清掉', async () => {
    let resolveSecondPrecheck: (v: { content: string; digest: string }) => void = () => {}
    mocks.SpecRead
      .mockResolvedValueOnce({ content: '', digest: 'sha256:stub' }) // mount 載入
      .mockResolvedValueOnce({ content: 'changed on disk', digest: 'sha256:EXTERNAL' }) // 第一次 save 預檢：磁碟已變→中止
      .mockImplementationOnce(() => new Promise(res => { resolveSecondPrecheck = res })) // 第二次 save 預檢：延遲
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(mustFind(w, '[data-test=external-abort]').text()).toBe(zhTW.externalChange.writeAborted) // 第一次中止已顯示

    await mustFind(w, '[data-test=save]').trigger('click') // 再次發起寫入，這次預檢尚未 resolve
    await flushPromises()
    expect(w.find('[data-test=external-abort]').exists()).toBe(false) // 操作一開始就清掉，不等預檢結果
    expect(write).not.toHaveBeenCalled() // 佐證：此刻預檢仍未完成，尚未走到 writer

    resolveSecondPrecheck({ content: 'edited', digest: 'sha256:stub' }) // 這次磁碟核對通過
    await flushPromises()
    expect(write).toHaveBeenCalledWith('spec/a.feature', 'edited', 'sha256:stub')
  })

  // 修正 2（清除時機 b）：切檔（loadFile()）與既有 externalChangeNotice 清除同
  // 處清掉 externalAbortMessage，理由同「換檔：上一個檔的外部變更提示不得留在
  // 新檔畫面上」——上一個檔的中止原因不得被誤讀成新檔的狀態。
  it('中止訊息清除(b)：切檔後上一個檔的 abort 訊息不得殘留在新檔畫面上', async () => {
    mocks.SpecRead
      .mockResolvedValueOnce({ content: '', digest: 'sha256:stub' }) // mount 載入 a
      .mockResolvedValueOnce({ content: 'changed on disk', digest: 'sha256:EXTERNAL' }) // save 預檢：磁碟已變→中止
      .mockResolvedValueOnce({ content: 'file b', digest: 'sha256:b0' }) // 切檔後載入 b
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    await mustFind(w, '[data-test=save]').trigger('click')
    await flushPromises()
    expect(mustFind(w, '[data-test=external-abort]').text()).toBe(zhTW.externalChange.writeAborted)

    await w.setProps({ path: 'spec/b.feature' })
    await flushEditor()

    expect(getView(w).state.doc.toString()).toBe('file b')
    expect(w.find('[data-test=external-abort]').exists()).toBe(false) // a 的中止原因不得殘留在 b 上
  })
})
