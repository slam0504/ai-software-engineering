import { flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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
