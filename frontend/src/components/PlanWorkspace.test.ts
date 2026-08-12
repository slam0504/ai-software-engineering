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
})
