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
})
