import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
import PlanWorkspace from './PlanWorkspace.vue'
import { usePlan } from '../stores/plan'
import { mountWithI18n } from '../test/i18n'

// wailsjs 綁定 mock：控制 PlanAssist 的 resolve 時機（同 SpecWorkspace.test.ts 慣例）。
// PlanList/PlanRead 給穩定預設值，避免 onMounted 的載入路徑撞到未定義行為。
const mocks = vi.hoisted(() => ({
  PlanAssist: vi.fn(),
  PlanList: vi.fn(),
  PlanRead: vi.fn(),
  PlanWrite: vi.fn(),
  PreviewPlanCommit: vi.fn(),
  ConfirmPlanCommit: vi.fn(),
  SubmitPlanForApproval: vi.fn(),
  PreviewAnalysisBaseBump: vi.fn(),
  ConfirmAnalysisBaseBump: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

describe('PlanWorkspace', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
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

  it('PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積', async () => {
    let resolveAssist: (id: string) => void = () => {}
    mocks.PlanAssist.mockImplementation(() => new Promise<string>(r => { resolveAssist = r }))

    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    await w.find('[data-test=generate-draft]').trigger('click')
    expect(mocks.PlanAssist).toHaveBeenCalledTimes(1)
    expect(w.find('[data-test=assist-busy]').exists()).toBe(true) // loading

    const plan = usePlan()
    plan.applyAssistEvent({
      event_id: 'e1', ts: 't', provider: 'claude', kind: 'delta',
      correlation_id: 'corr-a', text: 'plan draft chunk 1',
    })
    resolveAssist('corr-a')
    await flushPromises()

    plan.applyAssistEvent({
      event_id: 'e2', ts: 't', provider: 'claude', kind: 'delta',
      correlation_id: 'corr-a', text: ' chunk 2',
    })
    await flushPromises()

    expect(w.find('[data-test=draft-text]').text()).toBe('plan draft chunk 1 chunk 2') // 輸出累積
    expect(w.find('[data-test=assist-busy]').exists()).toBe(false)
  })

  it('套用草稿只更新編輯器 buffer；儲存才呼叫 PlanWrite', async () => {
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, { props: {
      path: 'plan/a.yaml', draft: 'AI plan draft content', write,
    }})
    await flushPromises()

    await w.find('[data-test=apply-draft]').trigger('click')
    expect(write).not.toHaveBeenCalled() // 套用草稿不落地
    expect(usePlan().currentContent).toBe('AI plan draft content') // buffer 已更新

    await w.find('[data-test=save]').trigger('click')
    expect(write).toHaveBeenCalledWith('plan/a.yaml', 'AI plan draft content', 'sha256:stub')
  })

  it('驗證錯誤（PlanWrite 樂觀鎖衝突）inline 顯示，原樣不吞', async () => {
    const write = vi.fn().mockRejectedValue(new Error('plan write conflict: expected_digest does not match current file'))
    const w = mountWithI18n(PlanWorkspace, { props: {
      path: 'plan/a.yaml', draft: 'AI plan draft content', write,
    }})
    await flushPromises()

    await w.find('[data-test=apply-draft]').trigger('click')
    await w.find('[data-test=save]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=plan-errors]').text()).toContain('plan write conflict: expected_digest does not match current file')
  })

  it('送核 Gate 2 失敗（含 GateList fail closed）錯誤原樣顯示', async () => {
    mocks.SubmitPlanForApproval.mockRejectedValue(new Error('assist: 無生效規格核可——先完成 Gate 1'))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/my-plan.yaml' } })
    await flushPromises()

    expect((w.find('[data-test=plan-id]').element as HTMLInputElement).value).toBe('my-plan') // 從檔名推導

    await w.find('[data-test=submit-gate2]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=plan-errors]').text()).toContain('assist: 無生效規格核可——先完成 Gate 1')
  })

  // A2（Pre-M4 Readiness Backlog）回歸：Gate 2 dirty-tree 送核失敗後，後續
  // 修正＋再送核成功，舊錯誤不得留在畫面（A9 驗收 sec-12 記錄的真實缺陷）。
  it('送核失敗→修正→再送核成功後，舊的送核錯誤要清空', async () => {
    mocks.SubmitPlanForApproval
      .mockRejectedValueOnce(new Error('assist: 無生效規格核可——先完成 Gate 1'))
      .mockResolvedValueOnce('approval-123')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/my-plan.yaml' } })
    await flushPromises()

    await w.find('[data-test=submit-gate2]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=plan-errors]').text()).toContain('assist: 無生效規格核可——先完成 Gate 1')

    // 模擬「修正」：再次送核（無需真的改內容，重現的是送核 lifecycle，不是修正本身）
    await w.find('[data-test=submit-gate2]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=plan-errors]').exists()).toBe(false)
  })

  // review fix（spec §3.8 回填）：「建立升級項目」帶目前 plan 檔 rel path 當
  // sourceRef，blockScope 留空（不預設阻擋哪個 gate scope）。
  it('點擊「建立升級項目」emit escalate，sourceRef=目前 plan 檔 rel path', async () => {
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/my-plan.yaml' } })
    await flushPromises()

    await w.find('[data-test=escalate]').trigger('click')
    expect(w.emitted('escalate')).toEqual([[{ sourceRef: 'plan/my-plan.yaml', blockScope: '' }]])
  })
})

// 新增檔案 inline 列（M3a.1 Task 4，spec §3.1 SC4 缺口 1）：路徑輸入＋即時 scope
// 預驗（plan/**）＋單一 plan 擋＋送出 PlanWrite(path, templateFor(path), '')，
// 成功後重載清單並選取新檔，失敗經 plan.pushError 原樣顯示、清單不動。
describe('PlanWorkspace 新增檔案', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('scope 外路徑即時提示，送出 disabled', async () => {
    const w = mountWithI18n(PlanWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('spec/features/x.feature')
    await flushPromises()

    expect(w.find('[data-test=new-file-scope-hint]').exists()).toBe(true)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeDefined()
    expect(mocks.PlanWrite).not.toHaveBeenCalled()
  })

  it('scope 內路徑送出成功後重載清單並選取新檔', async () => {
    mocks.PlanWrite.mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('plan/risk-policy.yaml')
    await flushPromises()
    expect(w.find('[data-test=new-file-scope-hint]').exists()).toBe(false)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=new-file-submit]').trigger('click')
    await flushPromises()

    expect(mocks.PlanWrite).toHaveBeenCalledWith('plan/risk-policy.yaml', expect.stringContaining('default_tier: medium'), '')
    expect(mocks.PlanList).toHaveBeenCalledTimes(2) // mount 一次＋成功後重載一次
    expect(mocks.PlanRead).toHaveBeenCalledWith('plan/risk-policy.yaml') // 選取新檔＋載入內容
  })

  it('失敗（PlanWrite 樂觀鎖衝突）錯誤原文顯示，清單不動', async () => {
    mocks.PlanWrite.mockRejectedValue(new Error('plan write conflict: expected_digest does not match current file'))
    const w = mountWithI18n(PlanWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('plan/dup-plan.yaml')
    await w.find('[data-test=new-file-submit]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=plan-errors]').text()).toContain('plan write conflict: expected_digest does not match current file')
    expect(mocks.PlanList).toHaveBeenCalledTimes(1) // 只有 mount 那次，失敗後不重載
  })

  it('單一 plan 擋：清單已有主要 plan 時，再輸入另一個主要 plan 路徑送出 disabled＋提示', async () => {
    mocks.PlanList.mockResolvedValue([{ name: 'existing.yaml', path: 'plan/existing.yaml' }])
    const w = mountWithI18n(PlanWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('plan/another.yaml')
    await flushPromises()

    expect(w.find('[data-test=new-file-single-plan-hint]').exists()).toBe(true)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeDefined()
    expect(mocks.PlanWrite).not.toHaveBeenCalled()
  })

  it('單一 plan 擋不影響 oracle-surface／risk-policy／permissions 路徑', async () => {
    mocks.PlanList.mockResolvedValue([{ name: 'existing.yaml', path: 'plan/existing.yaml' }])
    mocks.PlanWrite.mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, {})
    await flushPromises()

    await w.find('[data-test=new-file-path]').setValue('plan/oracle-surface.yaml')
    await flushPromises()

    expect(w.find('[data-test=new-file-single-plan-hint]').exists()).toBe(false)
    expect(w.find('[data-test=new-file-submit]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=new-file-submit]').trigger('click')
    await flushPromises()
    expect(mocks.PlanWrite).toHaveBeenCalledWith('plan/oracle-surface.yaml', expect.stringContaining('patterns:'), '')
  })

  // M3a.1 Task 11（spec §3.5）回歸：path prop 只當「seed」用一次——STALE 重核
  // 引導會從 App.vue 傳入 path 導航到指定 plan 檔，但那之後操作者在檔案清單裡
  // 點別的檔仍要能正常換檔，不能被殘留的 prop 值永久鎖死（否則導航一次之後，
  // Plan 工作區的手動檔案瀏覽功能就整個壞掉）。
  it('path prop 只 seed 一次，seed 後點檔案清單仍可正常換檔', async () => {
    mocks.PlanList.mockResolvedValue([
      { name: 'a.yaml', path: 'plan/a.yaml' },
      { name: 'b.yaml', path: 'plan/b.yaml' },
    ])
    mocks.PlanRead.mockImplementation((path: string) =>
      Promise.resolve({ content: path === 'plan/a.yaml' ? 'content-a' : 'content-b', digest: 'sha256:' + path }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(mocks.PlanRead).toHaveBeenCalledWith('plan/a.yaml')

    const bButton = w.findAll('button').find(b => b.text() === 'b.yaml')
    expect(bButton).toBeTruthy()
    await bButton!.trigger('click')
    await flushPromises()
    expect(mocks.PlanRead).toHaveBeenCalledWith('plan/b.yaml') // 點清單裡的另一個檔仍能正常換檔，未被 path prop 鎖死
  })
})

// analysis_base bump 引導 UI（M3a.1 Task 6，spec §3.2）：觸發時機（檔案載入／
// 儲存成功／視窗聚焦，非逐鍵擊）、bump 提示條＋面板內容、確認→buffer 取代
// （未儲存）、Confirm 錯誤→原文顯示＋重新預覽、no_bump_needed 顯示、非主要
// plan 文件不查、Preview 失敗（分析基準尚未填等正常過渡狀態）靜默不報錯。
describe('PlanWorkspace analysis_base bump 引導 UI', () => {
  const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
  const bumpPreview = {
    token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
    old: 'old000',
    head: 'head111',
    commits: [
      { oid: 'c1111111111111111111111111111111111111', subject: 'fix: something' },
      { oid: 'c2222222222222222222222222222222222222', subject: 'feat: other' },
    ],
    touched_files: ['src/foo.go', 'src/bar.go'],
    no_bump_needed: false,
  }

  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
  })

  it('bump 非 NoBumpNeeded 時顯示提示條與檢視差異入口（面板尚未展開）', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledWith('plan/a.yaml', bufferText) // 檔案載入觸發
    expect(w.find('[data-test=bump-banner]').exists()).toBe(true)
    expect(w.find('[data-test=bump-panel]').exists()).toBe(false)
  })

  it('點開面板顯示 old／head／commits／touched files＋警語＋重新執行 PlannerAssist 按鈕', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    await w.find('[data-test=bump-toggle]').trigger('click')
    const panel = w.find('[data-test=bump-panel]')
    expect(panel.exists()).toBe(true)
    expect(panel.find('[data-test=bump-old]').attributes('title')).toBe('old000')
    expect(panel.find('[data-test=bump-head]').attributes('title')).toBe('head111')
    const commitsText = panel.find('[data-test=bump-commits]').text()
    expect(commitsText).toContain('fix: something')
    expect(commitsText).toContain('feat: other')
    const touchedText = panel.find('[data-test=bump-touched-files]').text()
    expect(touchedText).toContain('src/foo.go')
    expect(touchedText).toContain('src/bar.go')
    expect(panel.find('[data-test=bump-warning]').text()).toBe('更新代表你已檢視這段 code 變更，並確認現有計畫仍適用') // 警語措辭凍結
    expect(panel.find('[data-test=bump-rerun-assist]').exists()).toBe(true)
  })

  it('確認更新：Confirm 成功後 editor buffer 被 updatedBuffer 取代，標記未儲存', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    const updated = 'plan_id: a\nanalysis_base_commit: "head111"\n'
    mocks.ConfirmAnalysisBaseBump.mockResolvedValue(updated)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click')
    await flushPromises()

    expect(mocks.ConfirmAnalysisBaseBump).toHaveBeenCalledWith(bumpPreview.token, 'plan/a.yaml', bufferText)
    expect(usePlan().currentContent).toBe(updated) // buffer 被取代
    expect(w.find('[data-test=save]').attributes('disabled')).toBeUndefined() // 未儲存狀態：save 按鈕可按（bufferDirty）
    expect(w.find('[data-test=bump-panel]').exists()).toBe(false) // 確認成功後面板收合
  })

  it('Confirm 失敗（token 過期／值不符）顯示錯誤原文，並重新預覽刷新面板', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValueOnce(bumpPreview)
    mocks.ConfirmAnalysisBaseBump.mockRejectedValue(new Error('plan: bump: buffer changed since preview — re-run preview'))
    mocks.PreviewAnalysisBaseBump.mockResolvedValueOnce({ ...bumpPreview, head: 'head222' })
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click')
    await flushPromises()

    expect(w.find('[data-test=bump-confirm-error]').text()).toContain('plan: bump: buffer changed since preview — re-run preview') // 原文顯示
    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledTimes(2) // 初次載入＋Confirm 失敗後重新預覽
    expect(usePlan().currentContent).toBe(bufferText) // buffer 未被取代
  })

  it('no_bump_needed 顯示「不需要更新」', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValue({
      token: { plan_rel: '', old: '', head: '', buffer_digest: '' },
      old: 'same', head: 'same', commits: [], touched_files: [], no_bump_needed: true,
    })
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    expect(w.find('[data-test=bump-no-bump-needed]').text()).toBe('不需要更新')
    expect(w.find('[data-test=bump-banner]').exists()).toBe(false)
  })

  it('儲存成功後重新查一次 bump（觸發時機：儲存成功）', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValueOnce({ ...bumpPreview, no_bump_needed: true })
    mocks.PreviewAnalysisBaseBump.mockResolvedValueOnce(bumpPreview)
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'draft content', write } })
    await flushPromises()
    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledTimes(1)

    await w.find('[data-test=apply-draft]').trigger('click')
    await w.find('[data-test=save]').trigger('click')
    await flushPromises()

    expect(write).toHaveBeenCalled()
    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledTimes(2)
    expect(w.find('[data-test=bump-banner]').exists()).toBe(true) // 第二次查詢結果反映在畫面上
  })

  // 直接呼叫元件掛上的 focus handler（而非 window.dispatchEvent 全域廣播）——
  // 其他測試（本檔案內、未 unmount）掛載時同樣會註冊 window focus 監聽，全域
  // dispatch 會連帶觸發那些殘留監聽，讓呼叫次數不可預期；spy
  // addEventListener 抓出「這個」元件實際註冊的 handler 才是穩定斷言。
  it('視窗聚焦時重新查一次 bump（觸發時機：視窗聚焦）', async () => {
    mocks.PreviewAnalysisBaseBump.mockResolvedValue({ ...bumpPreview, no_bump_needed: true })
    const addSpy = vi.spyOn(window, 'addEventListener')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledTimes(1)

    const focusHandler = addSpy.mock.calls.find(([type]) => type === 'focus')?.[1] as (() => void) | undefined
    expect(focusHandler).toBeTypeOf('function')
    focusHandler?.()
    await flushPromises()
    expect(mocks.PreviewAnalysisBaseBump).toHaveBeenCalledTimes(2)

    w.unmount()
    addSpy.mockRestore()
  })

  it('非主要 plan 文件（risk-policy.yaml）不呼叫 bump 檢查', async () => {
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/risk-policy.yaml' } })
    await flushPromises()

    expect(mocks.PreviewAnalysisBaseBump).not.toHaveBeenCalled()
    expect(w.find('[data-test=bump-banner]').exists()).toBe(false)
  })

  it('Preview 失敗（例如 analysis_base_commit 尚未填）靜默視為無 bump 待處理，不推進 plan.errors', async () => {
    mocks.PreviewAnalysisBaseBump.mockRejectedValue(
      new Error('plan: bump: analysis_base_commit "" is not a full commit id — re-run PlannerAssist'),
    )
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()

    expect(w.find('[data-test=bump-banner]').exists()).toBe(false)
    expect(w.find('[data-test=bump-no-bump-needed]').exists()).toBe(false)
    expect(usePlan().errors).toEqual([])
  })
})

// A1a-1 expected-red 階段：非同步儲存契約（buffer／saved／digest／dirty 依內容比較、
// data-busy、載入世代、confirmBump 四條結束路徑、樂觀鎖衝突）尚未實作，本 describe
// 內的測試針對「將來會提供」的可觀察介面斷言——多數預期失敗（R），這是 TDD 紅燈階段
// 的正常狀態。Plan 已有 [data-test=save] 按鈕與 bufferDirty 手動旗標，但 dirty 目前
// 不是內容比較（見 PlanWorkspace.vue applyDraft／confirmBump／saveFile），部分測試
// 因此仍可能通過（例如未涉及續打／世代競態的存後重載）——結果如實回報，不強行製造
// 失敗。
//
// CM6 view 取得方式：同 SpecWorkspace.test.ts——用 @codemirror/view 匯出的
// `EditorView.findFromDOM(dom)`（官方靜態方法）從 [data-test=editor-host] 反查目前
// 掛載的 view instance，再用 `view.dispatch({changes})` 直接改動 CM6 文件（已實測
// beforeinput／input 在 jsdom 下不會改變 CM6 文件）。
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
      return Promise.reject(new Error('plan write conflict: expected_digest does not match current file'))
    }
    seq += 1
    const digest = `sha256:seq${seq}`
    store.set(path, { content, digest })
    return Promise.resolve(digest)
  })
  return { read, write }
}

describe('PlanWorkspace 非同步儲存契約（A1a-1，expected-red）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  it('T2-P：編輯器文件改變→buffer 等於編輯器內容、dirty 為真', async () => {
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited content')
    await flushPromises()

    expect(w.find('[data-test=save]').attributes('disabled')).toBeUndefined() // dirty 為真
    await w.find('[data-test=save]').trigger('click')
    await flushPromises()
    expect(write).toHaveBeenCalledWith('plan/a.yaml', 'edited content', 'sha256:stub') // buffer 即送出內容
  })

  it('T3-P：存後重載——重載前 saved／digest／dirty 斷言', async () => {
    const store = makeFileStore({
      'plan/a.yaml': { content: 'plan a original content', digest: 'sha256:stub' },
      'plan/b.yaml': { content: 'plan b content', digest: 'sha256:b0' },
    })
    mocks.PlanRead.mockImplementation(store.read)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write: store.write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'plan a edited via real input') // 真實編輯器輸入，不是套用草稿
    await flushPromises()

    await w.find('[data-test=save]').trigger('click')
    await flushPromises()

    expect(store.write).toHaveBeenCalledWith('plan/a.yaml', 'plan a edited via real input', 'sha256:stub')
    const newDigest = await store.write.mock.results[0].value // 替身實際回傳的新 digest
    expect(usePlan().currentDigest).toBe(newDigest)
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined() // 重載前：saved==buffer→dirty 假

    await w.setProps({ path: 'plan/b.yaml' }) // 切到別的檔，走替身 read
    await flushEditor()
    await w.setProps({ path: 'plan/a.yaml' }) // 切回來，走替身 read——讀到的必須是剛才寫入的內容
    await flushEditor()

    expect(getView(w).state.doc.toString()).toBe('plan a edited via real input') // 重載後文件等於已儲存內容
  })

  it('T5-P：編輯回原內容→dirty 為假', async () => {
    const write = vi.fn().mockResolvedValue('sha256:new')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'edited')
    await flushPromises()
    expect(w.find('[data-test=save]').attributes('disabled')).toBeUndefined() // 編輯後 dirty 真

    typeText(view, '') // 改回原本 saved 內容（初始 content 為空字串）
    await flushPromises()
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined() // 回到與 saved 相同→dirty 假
  })

  it('T6-P：儲存中續打——成功後 saved 為送出時內容，續打後仍 dirty', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'v1', write } })
    await flushEditor()
    await w.find('[data-test=apply-draft]').trigger('click') // buffer=v1，dirty 真
    await w.find('[data-test=save]').trigger('click') // 送出 v1，尚未 resolve
    expect(write).toHaveBeenCalledWith('plan/a.yaml', 'v1', 'sha256:stub')

    const view = getView(w)
    typeText(view, 'v2') // 儲存中續打
    await flushPromises()
    resolveWrite('sha256:v1')
    await flushPromises()

    // saved 應為送出時的內容 v1；目前 buffer 是 v2 → dirty 應為真
    expect(w.find('[data-test=save]').attributes('disabled')).toBeUndefined()
  })

  it('T7-P：儲存中封鎖——再次儲存／切檔皆不執行，data-busy 反映', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'v1', write } })
    await flushEditor()
    await w.find('[data-test=apply-draft]').trigger('click')
    await w.find('[data-test=save]').trigger('click')
    expect(write).toHaveBeenCalledTimes(1)
    expect(w.attributes('data-busy')).toBe('save')

    await w.find('[data-test=save]').trigger('click') // 再次儲存
    const contentDuringSave = usePlan().currentContent
    // 缺口 4：儲存中實際點擊套用草稿。草稿必須與目前 buffer **不同**，否則即使
    // 兩層防護（函式 guard＋按鈕 disabled）都被拿掉，套用同樣內容也觀察不到差異，
    // 這條斷言就沒有鑑別力（MU-draft-busy-P 首輪 NOT_RED 的原因）。
    await w.setProps({ draft: 'v2 different from buffer' })
    await w.find('[data-test=apply-draft]').trigger('click')
    expect(usePlan().currentContent).toBe(contentDuringSave) // 內容未被替換，不只是斷言按鈕 disabled
    await w.setProps({ path: 'plan/b.yaml' }) // 切檔
    await flushPromises()

    expect(write).toHaveBeenCalledTimes(1) // 未再被呼叫
    expect(mocks.PlanRead).toHaveBeenCalledTimes(1) // 只有 mount 那次，切檔未觸發重載

    resolveWrite('sha256:new')
    await flushPromises()
    expect(w.attributes('data-busy')).toBe('')
  })

  it('T8-P：過期世代丟棄——切檔後先前的延遲回應到達時整筆丟棄', async () => {
    // 先讓第一次載入正常完成，CM6 才會初始化（onMounted 內 initEditor 排在
    // loadFile 之後，見 PlanWorkspace.vue）。
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    const view = getView(w)

    let resolveDelayed: (v: { content: string; digest: string }) => void = () => {}
    mocks.PlanRead.mockImplementationOnce(() => new Promise(r => { resolveDelayed = r }))
    await w.setProps({ path: 'plan/delayed.yaml' }) // 觸發一次延遲載入
    await flushPromises()

    mocks.PlanRead.mockResolvedValueOnce({ content: 'B content', digest: 'sha256:b' })
    await w.setProps({ path: 'plan/b.yaml' }) // 切到 B（世代較新），B 立即 resolve
    await flushEditor()
    expect(view.state.doc.toString()).toBe('B content')

    resolveDelayed({ content: 'STALE delayed content', digest: 'sha256:stale' }) // 過期回應現在才到
    await flushPromises()
    expect(view.state.doc.toString()).toBe('B content') // 過期回應被整筆丟棄，不覆蓋 B
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined() // 未被寫入→仍等於 saved(B)→dirty 假
  })

  it('T8b-P：A→B→A——先前 A 回應延遲，經過 B 後回到 A，該延遲回應仍須丟棄', async () => {
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/seed.yaml' } })
    await flushEditor()
    const view = getView(w)

    const resolvers: Record<string, (v: { content: string; digest: string }) => void> = {}
    mocks.PlanRead.mockImplementation((path: string) => new Promise(r => { resolvers[path] = r }))

    await w.setProps({ path: 'plan/a.yaml' }) // 第一次選 A（延遲）
    await flushPromises()
    const firstAResolve = resolvers['plan/a.yaml']

    await w.setProps({ path: 'plan/b.yaml' }) // 切到 B（延遲）
    await flushPromises()
    resolvers['plan/b.yaml']({ content: 'B content', digest: 'sha256:b' })
    await flushEditor()
    expect(view.state.doc.toString()).toBe('B content')

    await w.setProps({ path: 'plan/a.yaml' }) // 再切回 A（新世代，延遲）
    await flushPromises()
    resolvers['plan/a.yaml']({ content: 'A content (second load)', digest: 'sha256:a2' }) // 第二次 A 的回應先到
    await flushEditor()
    expect(view.state.doc.toString()).toBe('A content (second load)')

    firstAResolve({ content: 'STALE first A', digest: 'sha256:a-stale' }) // 第一次 A 的延遲回應現在才到
    await flushPromises()
    expect(view.state.doc.toString()).toBe('A content (second load)') // 不被第一次的過期回應覆蓋
  })

  it('T8c-P：載入中編輯器真正暫停——裝飾性標記＋CM6 實際 contenteditable＋load 期間 dispatch 不回寫 buffer', async () => {
    // 先讓第一次載入正常完成，CM6 才會初始化（同 T8-P），才能拿到可觀察的 view
    // 來斷言載入中的 contenteditable。
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    const view = getView(w)
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('true') // 初始載入完成後可編輯

    let resolveRead: (v: { content: string; digest: string }) => void = () => {}
    mocks.PlanRead.mockImplementationOnce(() => new Promise(r => { resolveRead = r }))
    await w.setProps({ path: 'plan/b.yaml' }) // 觸發第二次（延遲）載入
    await flushPromises()

    expect(w.find('[data-test=editor-host]').attributes('data-editing-suspended')).toBe('true') // 既有裝飾性標記
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('false') // CM6 實際可編輯狀態，不是我們自己掛的標記

    // 載入期間程式化 dispatch 改文件：EditorView.editable 只擋使用者輸入，仍要
    // 靠 updateListener 的 busyReason 檢查擋住回寫，buffer（plan.currentContent）
    // 才不會被污染。
    const bufferBeforeDispatch = usePlan().currentContent
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: 'injected during load' } })
    await flushPromises()
    expect(usePlan().currentContent).toBe(bufferBeforeDispatch) // buffer 未變

    resolveRead({ content: 'B content', digest: 'sha256:b' })
    await flushEditor()

    expect(w.find('[data-test=editor-host]').attributes('data-editing-suspended')).toBeUndefined()
    expect(view.contentDOM.getAttribute('contenteditable')).toBe('true') // 載入完成後回到可編輯

    // 載入完成後再 dispatch 一次：buffer 應正常更新
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: 'edited after load' } })
    await flushPromises()
    expect(usePlan().currentContent).toBe('edited after load')
  })

  it('T9-P：衝突——[data-test=save-error][data-conflict=true]，三者不變，不自動重載，dirty 依內容比較', async () => {
    const write = vi.fn().mockRejectedValue(new Error('write conflict: expected_digest does not match current file'))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'v1', write } })
    await flushEditor()
    await w.find('[data-test=apply-draft]').trigger('click')
    await w.find('[data-test=save]').trigger('click')
    await flushPromises()

    const err = w.find('[data-test=save-error]')
    expect(err.exists()).toBe(true) // 目前錯誤只進 plan-errors，沒有專屬 save-error
    expect(err.text()).toContain('write conflict: expected_digest does not match current file')
    expect(err.attributes('data-conflict')).toBe('true')
    expect(mocks.PlanRead).toHaveBeenCalledTimes(1) // 未自動重新載入

    const view = getView(w)
    typeText(view, '') // 改回 saved 原內容（初始 content 為空字串）
    await flushPromises()
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined() // dirty 依內容比較→假
    expect(usePlan().currentDigest).toBe('sha256:stub') // 持有的 digest 不得被衝突分支動到
  })

  it('T10-P：非衝突錯誤——原文顯示且無 data-conflict，三者不變，dirty 依內容比較', async () => {
    const write = vi.fn().mockRejectedValue(new Error('boom: disk full'))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'v1', write } })
    await flushEditor()
    await w.find('[data-test=apply-draft]').trigger('click')
    await w.find('[data-test=save]').trigger('click')
    await flushPromises()

    const err = w.find('[data-test=save-error]')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('boom: disk full')
    expect(err.attributes('data-conflict')).toBeUndefined()

    const view = getView(w)
    typeText(view, '')
    await flushPromises()
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined()
  })

  it('T14a：confirmBump 只更新 buffer 與編輯器，不呼叫 PlanWrite，saved／digest 不變', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    const updated = 'plan_id: a\nanalysis_base_commit: "head111"\n'
    mocks.ConfirmAnalysisBaseBump.mockResolvedValue(updated)
    const write = vi.fn()
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write } })
    await flushEditor()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click')
    await flushPromises()

    expect(write).not.toHaveBeenCalled()
    expect(usePlan().currentDigest).toBe('sha256:stub') // digest 未變
    const view = getView(w)
    expect(view.state.doc.toString()).toBe(updated) // 編輯器也要反映（syncEditorDoc 已呼叫）
    expect(w.find('[data-test=save]').attributes('disabled')).toBeUndefined() // saved 未動→內容已異動→dirty 真
  })

  it('T14b：applyDraft 只更新 buffer——不同於 saved→dirty 真；恰等於 saved→dirty 假', async () => {
    // 前半段（不同於 saved）同時證明 applyDraft 不得一併更新 saved：若它更新了，
    // dirty 會變成假，這條就會紅。後半段是「結果恰等於 saved 不得誤報未儲存」。
    mocks.PlanRead.mockResolvedValue({ content: 'same content', digest: 'sha256:stub' })
    const wDiff = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'different content' } })
    await flushEditor()
    await wDiff.find('[data-test=apply-draft]').trigger('click')
    await flushPromises()
    expect(usePlan().currentContent).toBe('different content')
    expect(usePlan().savedContent).toBe('same content') // saved 不動
    expect(wDiff.find('[data-test=save]').attributes('disabled')).toBeUndefined() // dirty 真

    setActivePinia(createPinia())
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'same content' } })
    await flushEditor()
    await w.find('[data-test=apply-draft]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=save]').attributes('disabled')).toBeDefined() // 套用結果等於 saved→dirty 假
  })

  it('T14f：confirmBump 等待期間切檔被阻止，未續打且版本相符→套用結果並解除封鎖', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    let resolveConfirm: (v: string) => void = () => {}
    mocks.ConfirmAnalysisBaseBump.mockImplementation(() => new Promise<string>(r => { resolveConfirm = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', draft: 'draft during bump wait' } })
    await flushEditor()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click') // 等待中，尚未 resolve

    // 缺口 4：bump 等待期間實際點擊套用草稿——內容不得被替換
    await w.find('[data-test=apply-draft]').trigger('click')
    expect(usePlan().currentContent).toBe(bufferText)

    await w.setProps({ path: 'plan/b.yaml' }) // 切檔理應被阻止
    await flushPromises()
    expect(mocks.PlanRead).toHaveBeenCalledTimes(1) // 未因切檔重新載入

    resolveConfirm('plan_id: a\nanalysis_base_commit: "head111"\n')
    await flushPromises()
    expect(usePlan().currentContent).toBe('plan_id: a\nanalysis_base_commit: "head111"\n') // 版本相符→套用結果
    expect(w.attributes('data-busy')).toBe('') // 解除封鎖
  })

  it('T14g(a)：confirmBump 等待期間續打使 buffer 版本過期→回應不套用、續打內容保留、data-stale=true', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    let resolveConfirm: (v: string) => void = () => {}
    mocks.ConfirmAnalysisBaseBump.mockImplementation(() => new Promise<string>(r => { resolveConfirm = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click') // 等待中

    const view = getView(w)
    typeText(view, 'plan_id: a\nanalysis_base_commit: "old000"\nextra: line\n') // 續打使 buffer 版本過期
    await flushPromises()

    resolveConfirm('plan_id: a\nanalysis_base_commit: "head111"\n')
    await flushPromises()

    const err = w.find('[data-test=bump-error]')
    expect(err.exists()).toBe(true) // 目前錯誤走 bump-confirm-error，非契約要求的 bump-error
    expect(err.attributes('data-stale')).toBe('true')
    expect(view.state.doc.toString()).toContain('extra: line') // 續打內容保留，回應不套用
  })

  it('T14g(b)：confirmBump 等待期間文件識別被替換→同版本過期處置', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockImplementation((path: string) =>
      Promise.resolve(path === 'plan/a.yaml'
        ? { content: bufferText, digest: 'sha256:stub' }
        : { content: 'other file content', digest: 'sha256:other' }))
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    let resolveConfirm: (v: string) => void = () => {}
    mocks.ConfirmAnalysisBaseBump.mockImplementation(() => new Promise<string>(r => { resolveConfirm = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click') // 針對 a 檔發起 confirmBump，尚未 resolve

    // 注入：等待期間文件識別被替換。正常導覽（setProps／selectFile）在 bump 等待
    // 期間已被封鎖（契約第 7 條），因此這條防禦性驗證直接改 store 的文件識別與
    // buffer，不經被封鎖的導覽路徑。
    usePlan().currentPath = 'plan/other.yaml'
    usePlan().currentContent = 'other file content'
    await flushPromises()

    resolveConfirm('plan_id: a\nanalysis_base_commit: "head111"\n') // a 檔的回應現在才到
    await flushPromises()

    expect(usePlan().currentPath).toBe('plan/other.yaml') // 識別已替換
    const err = w.find('[data-test=bump-error]')
    expect(err.exists()).toBe(true)
    expect(err.attributes('data-stale')).toBe('true')
    expect(usePlan().currentContent).toBe('other file content') // 回應不得覆蓋目前（other）檔的 buffer
  })

  it('T14h：confirmBump 後端真正錯誤——原訊息保留在 bump-error 且無 data-stale，解除封鎖', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    mocks.ConfirmAnalysisBaseBump.mockRejectedValue(new Error('plan: bump: token expired'))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()

    await w.find('[data-test=bump-toggle]').trigger('click')
    await w.find('[data-test=bump-confirm]').trigger('click')
    await flushPromises()

    const err = w.find('[data-test=bump-error]')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('plan: bump: token expired')
    expect(err.attributes('data-stale')).toBeUndefined()
    expect(w.attributes('data-busy')).toBe('') // 解除封鎖
  })
})

// A1a-2 expected-red 階段：切檔守衛尚未實作於 PlanWorkspace——selectFile() 目前
// 沒有任何 dirty 檢查（見 PlanWorkspace.vue 第 307-313 行），外部（非寫入中）
// dirty 時點清單切檔會直接切換。斷言方式與 SpecWorkspace.test.ts 同一組
// G1/G2/G3/G6 契約，各自獨立維護一份（同本檔既有 flushEditor／getView／
// typeText／makeFileStore 皆不跨檔匯入的慣例），不是測試寫錯。G6-P 例外：驗證
// A1a-1 的儲存中互斥仍優先於切檔守衛——這是現有程式碼已有的行為，本來就該綠。
describe('PlanWorkspace 切檔守衛（A1a-2，expected-red）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.PlanList.mockResolvedValue([
      { name: 'a.yaml', path: 'plan/a.yaml' },
      { name: 'b.yaml', path: 'plan/b.yaml' },
    ])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
  })

  function mustFind(w: VueWrapper<any>, selector: string) {
    const el = w.find(selector)
    expect(el.exists(), `找不到 ${selector}——尚未實作（expected-red）`).toBe(true)
    return el
  }

  // findFileButton：同 SpecWorkspace.test.ts 的版本，本檔獨立維護一份。
  function findFileButton(w: VueWrapper<any>, name: string) {
    const btn = w.findAll('.files button').find(b => b.text() === name)
    expect(btn, `找不到檔案清單按鈕 ${name}`).toBeTruthy()
    return btn!
  }

  it('G1-P：dirty 時點擊清單切檔→[data-test=unsaved-guard] 出現，新檔的 read 尚未被呼叫', async () => {
    const store = makeFileStore({
      'plan/a.yaml': { content: 'plan a original', digest: 'sha256:a0' },
      'plan/b.yaml': { content: 'plan b original', digest: 'sha256:b0' },
    })
    mocks.PlanRead.mockImplementation(store.read)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'plan a edited') // dirty：plan.currentContent ≠ plan.savedContent
    await flushPromises()

    await findFileButton(w, 'b.yaml').trigger('click')
    await flushPromises()

    mustFind(w, '[data-test=unsaved-guard]')
    expect(store.read).not.toHaveBeenCalledWith('plan/b.yaml') // 守衛出現前不得先載入新檔
    expect(view.state.doc.toString()).toBe('plan a edited') // 編輯器仍停在原檔未儲存內容
  })

  it('G2-P：G1 情境下點擊 discard→新檔正式載入，read 被呼叫且編輯器內容變成新檔內容', async () => {
    const store = makeFileStore({
      'plan/a.yaml': { content: 'plan a original', digest: 'sha256:a0' },
      'plan/b.yaml': { content: 'plan b original', digest: 'sha256:b0' },
    })
    mocks.PlanRead.mockImplementation(store.read)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    typeText(getView(w), 'plan a edited')
    await flushPromises()
    await findFileButton(w, 'b.yaml').trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-discard]').trigger('click')
    await flushEditor()

    expect(store.read).toHaveBeenCalledWith('plan/b.yaml')
    expect(getView(w).state.doc.toString()).toBe('plan b original')
  })

  it('G3-P：G1 情境下點擊 keep→維持原檔，read 未被呼叫，編輯器仍是未儲存內容', async () => {
    const store = makeFileStore({
      'plan/a.yaml': { content: 'plan a original', digest: 'sha256:a0' },
      'plan/b.yaml': { content: 'plan b original', digest: 'sha256:b0' },
    })
    mocks.PlanRead.mockImplementation(store.read)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    typeText(getView(w), 'plan a edited')
    await flushPromises()
    await findFileButton(w, 'b.yaml').trigger('click')
    await flushPromises()

    await mustFind(w, '[data-test=unsaved-keep]').trigger('click')
    await flushPromises()

    expect(store.read).not.toHaveBeenCalledWith('plan/b.yaml')
    expect(getView(w).state.doc.toString()).toBe('plan a edited') // 停留在原檔的未儲存內容
  })

  // G6-P-save-selectfile／G6-P-bump-selectfile：owner 裁定（round 2）——G6 要
  // 涵蓋 PlanWorkspace 每一種寫入等待狀態（busyReason 'save'／'bump'），不只
  // save。兩條都預期綠：不是「守衛缺元素」的紅燈缺口，而是
  // 「busyReason!=='' 時 selectFile() 直接 return（見 PlanWorkspace.vue
  // 307-313 行）」已經先擋下了，dirty-guard 邏輯根本沒機會執行——所以現在就是
  // 綠，且綠得有意義（precedence 驗證，不是誤判）。
  it('G6-P-save-selectfile：A1a-1 優先——save 進行中同時 dirty，切檔被 A1a-1 直接拒絕，不進切檔守衛選擇（precedence 已實作，本條預期綠）', async () => {
    let resolveWrite: (d: string) => void = () => {}
    const write = vi.fn().mockImplementation(() => new Promise<string>(r => { resolveWrite = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write } })
    await flushEditor()
    const view = getView(w)
    typeText(view, 'plan a edited')
    await flushPromises()
    await w.find('[data-test=save]').trigger('click') // 觸發 save，尚未 resolve → busyReason='save'
    expect(w.attributes('data-busy')).toBe('save')

    await findFileButton(w, 'b.yaml').trigger('click') // 儲存中嘗試切檔（同時 dirty）
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false) // A1a-1 儲存互斥直接擋下，不出現守衛選擇
    expect(w.attributes('data-busy')).toBe('save') // busy 未變
    expect(mocks.PlanRead).toHaveBeenCalledTimes(1) // 只有初次載入，切檔未觸發任何讀取
    expect(view.state.doc.toString()).toBe('plan a edited') // 內容未變

    resolveWrite('sha256:new')
    await flushPromises()
  })

  it('G6-P-bump-selectfile：A1a-1 優先——confirmBump 進行中同時 dirty，切檔被 A1a-1 直接拒絕，不進切檔守衛選擇（precedence 已實作，本條預期綠）', async () => {
    const bufferText = 'plan_id: a\nanalysis_base_commit: "old000"\n'
    const bumpPreview = {
      token: { plan_rel: 'plan/a.yaml', old: 'old000', head: 'head111', buffer_digest: 'digest1' },
      old: 'old000', head: 'head111', commits: [], touched_files: [], no_bump_needed: false,
    }
    mocks.PlanRead.mockResolvedValue({ content: bufferText, digest: 'sha256:stub' })
    mocks.PreviewAnalysisBaseBump.mockResolvedValue(bumpPreview)
    let resolveConfirm: (v: string) => void = () => {}
    mocks.ConfirmAnalysisBaseBump.mockImplementation(() => new Promise<string>(r => { resolveConfirm = r }))
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushEditor()
    const view = getView(w)

    await w.find('[data-test=bump-toggle]').trigger('click')
    // 觸發 confirmBump 前先編輯，讓凍結快照（confirmBump 內的 frozen.buf）帶著
    // 未儲存內容，證明「同時 dirty」而不只是「剛好沒改過」。
    typeText(view, bufferText + 'edited before bump confirm\n')
    await flushPromises()
    await w.find('[data-test=bump-confirm]').trigger('click') // 觸發 confirmBump，尚未 resolve → busyReason='bump'
    expect(w.attributes('data-busy')).toBe('bump')

    await findFileButton(w, 'b.yaml').trigger('click') // bump 進行中嘗試切檔（同時 dirty）
    await flushPromises()

    expect(w.find('[data-test=unsaved-guard]').exists()).toBe(false) // A1a-1 的 bump 互斥直接擋下，不出現守衛選擇
    expect(w.attributes('data-busy')).toBe('bump') // busy 未變
    expect(mocks.PlanRead).toHaveBeenCalledTimes(1) // 只有初次載入，切檔未觸發任何讀取
    expect(view.state.doc.toString()).toBe(bufferText + 'edited before bump confirm\n') // 內容未變

    resolveConfirm('plan_id: a\nanalysis_base_commit: "head111"\n')
    await flushPromises()
  })
  it('G7-P：dirty 發送端——真實輸入→true、改回 saved→false、儲存成功→false（不是只驗接收端）', async () => {
    const store = makeFileStore({ 'plan/a.yaml': { content: 'a original', digest: 'sha256:a0' } })
    mocks.PlanRead.mockImplementation(store.read)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml', write: store.write } })
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
    await w.find('[data-test=save]').trigger('click') // 儲存成功
    await flushPromises()
    expect(seen().at(-1)).toBe(false)
  })

})
