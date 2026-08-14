import { flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import GateConsole from './GateConsole.vue'
import { mountWithI18n } from '../test/i18n'

// minimum_risk_tier 預設用 medium（非 low）：low 是 tierOrder 的最小值，若預設一路用
// low 測試永遠不會踩到「selected 被下拉過濾／低於 minimum 送出擋」這條路徑（review
// round 1 finding），故意選一個 minimum > low 的預設值消除這個測試盲點。
const gate2Task = (over: Partial<{ task_id: string; title: string; minimum_risk_tier: string; planner_risk_tier: string }> = {}) => ({
  task_id: 'T1', title: 'do the thing', minimum_risk_tier: 'medium', planner_risk_tier: 'high', ...over,
})

describe('GateConsole', () => {
  it('reject requires reason', async () => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'pending' }], decide } })
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).not.toHaveBeenCalled() // 無理由不送
    await w.find('[data-test=reason]').setValue('bad')
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'rejected', 'bad', []) // rejected 只需 reason，riskSelections 恆空
  })
  it('shows stale badge', () => {
    const w = mountWithI18n(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'stale' }], decide: vi.fn() } })
    expect(w.find('[data-test=badge-A]').text()).toContain('已失效') // zh-TW 預設 locale：gate.state.stale
  })
  it('shows rejected badge', () => {
    const w = mountWithI18n(GateConsole, { props: { entries: [{ approval_id: 'A', state: 'rejected' }], decide: vi.fn() } })
    expect(w.find('[data-test=badge-A]').text()).toContain('已退回') // zh-TW 預設 locale：gate.state.rejected
  })
  it('gate2 卡片 risk tier 下拉與唯讀欄位皆走 riskTierKeys 翻譯（zh-TW：低／中／高）', async () => {
    const loadDecisionContext = vi.fn().mockResolvedValue({
      tasks: [gate2Task({ minimum_risk_tier: 'low', planner_risk_tier: 'low' })],
    })
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide: vi.fn(), loadDecisionContext },
    })
    await flushPromises()
    expect(w.find('[data-test=minimum]').text()).toContain('低')
    expect(w.find('[data-test=planner]').text()).toContain('低')
    const options = w.find('[data-test=selected-T1]').findAll('option')
    expect(options.map(o => o.attributes('value'))).toEqual(['low', 'medium', 'high']) // value 維持 raw tier
    expect(options[0].text()).toBe('低') // 顯示文字走翻譯
  })

  it('gate1 卡片不顯示 risk 列（回歸）', async () => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate1' }], decide },
    })
    await flushPromises()
    expect(w.find('[data-test=risk-section]').exists()).toBe(false)
    // gate1 approve 不受 risk 邏輯影響，只受 degraded 控制（既有行為）
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()
    await w.find('[data-test=reason]').setValue('ok')
    await w.find('[data-test=approve]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'approved', 'ok', [])
  })

  it('gate2 卡片：selected<planner 未填理由時核可 disabled，填理由後 enabled 且送出 payload 含 override_reason', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({ tasks: [gate2Task()] })
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2', subject: 'plan:P1' }],
        decide, loadDecisionContext,
      },
    })
    await flushPromises()
    expect(loadDecisionContext).toHaveBeenCalledWith('A')
    expect(w.find('[data-test=risk-row-T1]').exists()).toBe(true)
    expect(w.find('[data-test=minimum]').text()).toContain('中') // riskTierKeys：medium → zh-TW 中
    expect(w.find('[data-test=planner]').text()).toContain('高') // riskTierKeys：high → zh-TW 高

    // 預設 selected=planner，不需要 override reason，核可可用
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()

    // 調到 medium（= minimum，< planner=high）：override reason 欄位出現，未填時核可 disabled
    await w.find('[data-test=selected-T1]').setValue('medium')
    expect(w.find('[data-test=override-reason-T1]').exists()).toBe(true)
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeDefined()

    await w.find('[data-test=override-reason-T1]').setValue('policy exception: read-only preview')
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeUndefined()

    await w.find('[data-test=approve]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'approved', '', [
      { TaskID: 'T1', SelectedRiskTier: 'medium', OverrideReason: 'policy exception: read-only preview' },
    ])
  })

  it('gate2 卡片：下拉依 minimum 過濾，不會列出低於 minimum 的選項（selected<minimum binding constraint）', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({
      tasks: [gate2Task({ minimum_risk_tier: 'medium', planner_risk_tier: 'high' })],
    })
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    const options = w.find('[data-test=selected-T1]').findAll('option').map(o => o.attributes('value'))
    expect(options).toEqual(['medium', 'high']) // low（< minimum）不在選項內
  })

  it('gate2 卡片：rejected 只需 reason，不受 risk 選擇影響', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockResolvedValue({ tasks: [gate2Task()] })
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    await w.find('[data-test=reject]').trigger('click')
    expect(decide).not.toHaveBeenCalled() // 無理由不送

    await w.find('[data-test=reason]').setValue('not ready')
    await w.find('[data-test=reject]').trigger('click')
    expect(decide).toHaveBeenCalledWith('A', 'rejected', 'not ready', [])
  })

  it('GateDecisionContext 失敗時顯示錯誤原文，且核可 disabled', async () => {
    const decide = vi.fn()
    const loadDecisionContext = vi.fn().mockRejectedValue(new Error('gate: approval id "A" not found'))
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate2' }], decide, loadDecisionContext },
    })
    await flushPromises()

    expect(w.find('[data-test=risk-error]').text()).toContain('gate: approval id "A" not found')
    expect(w.find('[data-test=approve]').attributes('disabled')).toBeDefined()
  })

  // Task 22：tca 卡片——subject（既有 .subject 泛用渲染）、gate2_approval 連結、
  // 兩筆 evidence 摘要（role/result/test_commit 短 SHA）、mutation digest。
  const tcaBindings = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    { kind: 'evidence_run', role: 'expected_red', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'evidence_run', role: 'negative_control', ref: 'ev-neg', digest: 'sha256:' + 'e'.repeat(64) },
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]
  const evidenceRunFixture = (over: Partial<{ evidence_id: string; result: string; test_commit: string; observed_failure: string }> = {}) => ({
    evidence_id: 'ev-red', kind: 'expected_red', result: 'passed', test_commit: 'a'.repeat(40),
    observed_failure: '', ...over,
  })

  it('tca 卡片渲染 gate2_approval 連結、兩筆 evidence role／result／test_commit 短 SHA、mutation digest', async () => {
    const decide = vi.fn()
    const getEvidence = vi.fn()
      .mockImplementation((id: string) => Promise.resolve(
        id === 'ev-red' ? evidenceRunFixture({ result: 'passed' }) : evidenceRunFixture({ evidence_id: 'ev-neg', result: 'passed' }),
      ))
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'pending', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: tcaBindings() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    expect(getEvidence).toHaveBeenCalledWith('ev-red')
    expect(getEvidence).toHaveBeenCalledWith('ev-neg')
    expect(w.find('[data-test=tca-section]').exists()).toBe(true)
    expect(w.find('[data-test=tca-gate2-link]').text()).toContain('G2-1')
    expect(w.find('[data-test=tca-evidence-expected_red]').exists()).toBe(true)
    expect(w.find('[data-test=tca-evidence-result-expected_red]').text()).toContain('通過')
    expect(w.find('[data-test=tca-evidence-expected_red]').text()).toContain('aaaaaaaaaa') // test_commit 短 SHA
    expect(w.find('[data-test=tca-evidence-negative_control]').exists()).toBe(true)
    expect(w.find('[data-test=tca-mutation]').text()).toContain('sha256:fffff…')
  })

  it('tca 卡片：result=error 顯示錯誤標示', async () => {
    const decide = vi.fn()
    const getEvidence = vi.fn().mockImplementation((id: string) => Promise.resolve(
      id === 'ev-red'
        ? evidenceRunFixture({ result: 'error', observed_failure: 'timeout exceeded after 10m0s' })
        : evidenceRunFixture({ evidence_id: 'ev-neg', result: 'passed' }),
    ))
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'active', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: tcaBindings() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    expect(w.find('[data-test=tca-evidence-result-expected_red]').text()).toContain('錯誤')
    expect(w.find('[data-test=tca-evidence-observed-expected_red]').text()).toContain('timeout exceeded after 10m0s')
  })

  it('tca 卡片：點擊「查看證據」emit open-evidence 帶 evidence_id', async () => {
    const decide = vi.fn()
    const getEvidence = vi.fn().mockResolvedValue(evidenceRunFixture())
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'active', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: tcaBindings() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    await w.find('[data-test=tca-evidence-open-expected_red]').trigger('click')
    expect(w.emitted('open-evidence')).toEqual([['ev-red']])
  })

  it('tca 卡片：EvidenceGet 失敗顯示錯誤原文', async () => {
    const decide = vi.fn()
    const getEvidence = vi.fn().mockRejectedValue(new Error('evidence: not found: evidence_id ev-red'))
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'active', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: tcaBindings() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    expect(w.find('[data-test=tca-evidence-error-expected_red]').text()).toContain('evidence: not found: evidence_id ev-red')
  })

  // Task 10（§3.3.3）：role 完整性 fail loud——根因是 GateConsole 過去無條件信任
  // binding.role、直接把它接進 data-test／DOM；bindings[].role 缺漏／未知／重複時
  // 一律不得靜默組出 "...-undefined"，改顯示資料完整性錯誤、零呼叫 EvidenceGet、
  // 不渲染「查看證據」控制項，raw bindings 清單仍保留診斷。
  const tcaBindingsMissingRole = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    // role 欄位完全缺漏（重現 M3a review 記錄的 data-test="...-undefined"：
    // b.role 是 JS undefined，字串樣板把它 toString 成字面 "undefined"）。
    { kind: 'evidence_run', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'evidence_run', role: 'negative_control', ref: 'ev-neg', digest: 'sha256:' + 'e'.repeat(64) },
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]
  const tcaBindingsMissingNegativeControl = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    { kind: 'evidence_run', role: 'expected_red', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]
  const tcaBindingsUnknownRole = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    { kind: 'evidence_run', role: 'bogus_role', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'evidence_run', role: 'negative_control', ref: 'ev-neg', digest: 'sha256:' + 'e'.repeat(64) },
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]
  const tcaBindingsDuplicateRole = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    { kind: 'evidence_run', role: 'expected_red', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'evidence_run', role: 'expected_red', ref: 'ev-red-2', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]
  // review 修正：validateTCABindings（internal/gatepolicy/tca.go）只鎖必要
  // (kind,role) 存在＋不重複，不拒絕清單外的額外 binding——兩個必要 role 齊全
  // 之外再帶一筆 role 省略（omitempty 序列化後前端拿到 undefined）的第三筆
  // evidence_run，是後端目前這道缺口在正常請求路徑下就能產生的真實資料形狀，
  // 不是繞過驗證才會出現，integrity 檢查的 unknown 分支必須擋下它。
  const tcaBindingsExtraUnknownRoleBinding = () => [
    { kind: 'gate2_approval', ref: 'approval:G2-1', digest: 'sha256:' + 'a'.repeat(64) },
    { kind: 'base_commit', ref: 'plan_commit', digest: 'git:sha1:' + 'b'.repeat(40) },
    { kind: 'oracle_surface', ref: 'c'.repeat(40), digest: 'sha256:' + 'c'.repeat(64) },
    { kind: 'evidence_run', role: 'expected_red', ref: 'ev-red', digest: 'sha256:' + 'd'.repeat(64) },
    { kind: 'evidence_run', role: 'negative_control', ref: 'ev-neg', digest: 'sha256:' + 'e'.repeat(64) },
    { kind: 'evidence_run', ref: 'ev-extra', digest: 'sha256:' + 'd'.repeat(64) }, // role 省略（後端 omitempty 序列化後的真實形狀）
    { kind: 'mutation', ref: 'mut-1', digest: 'sha256:' + 'f'.repeat(64) },
  ]

  it.each([
    ['role 完全缺漏（-undefined 根因重現）', tcaBindingsMissingRole],
    ['缺 negative_control', tcaBindingsMissingNegativeControl],
    ['未知 role', tcaBindingsUnknownRole],
    ['雙份同 role', tcaBindingsDuplicateRole],
    ['兩必要 role 齊全＋額外一筆 role 省略的第三筆 evidence_run（後端白名單缺口的真實資料形狀）', tcaBindingsExtraUnknownRoleBinding],
  ])('tca 卡片：evidence bindings 不完整（%s）→ 資料完整性錯誤、零呼叫 EvidenceGet、不渲染查看證據', async (_label, bindingsFactory) => {
    const decide = vi.fn()
    const getEvidence = vi.fn().mockResolvedValue(evidenceRunFixture())
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'pending', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: bindingsFactory() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    expect(getEvidence).not.toHaveBeenCalled() // EvidenceGet 零呼叫
    expect(w.find('[data-test=tca-evidence-integrity-error]').exists()).toBe(true)
    expect(w.find('[data-test=tca-evidence-integrity-error]').text()).not.toContain('undefined') // 不得靜默組出 "-undefined"
    expect(w.find('button[data-test^="tca-evidence-open-"]').exists()).toBe(false) // 不渲染「查看證據」控制項
    expect(w.html()).not.toContain('-undefined') // data-test 屬性本身也不得出現 undefined
    // raw bindings 清單仍保留供診斷
    expect(w.find('.bindings').exists()).toBe(true)
    expect(w.find('.bindings').text()).toContain('evidence_run')
  })

  it('tca 卡片：evidence bindings 完整（雙 role 各恰一）→ 查看證據控制項正常渲染（回歸）', async () => {
    const decide = vi.fn()
    const getEvidence = vi.fn().mockResolvedValue(evidenceRunFixture())
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'pending', gate: 'test_contract_approval', subject: 'task:P1/T1', bindings: tcaBindings() }],
        decide, getEvidence,
      },
    })
    await flushPromises()

    expect(w.find('[data-test=tca-evidence-integrity-error]').exists()).toBe(false)
    expect(getEvidence).toHaveBeenCalledWith('ev-red')
    expect(getEvidence).toHaveBeenCalledWith('ev-neg')
    expect(w.find('[data-test=tca-evidence-open-expected_red]').exists()).toBe(true)
  })

  it('gate2／gate1 卡片不渲染 tca-section（回歸）', async () => {
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'active', gate: 'gate2', subject: 'plan:P1' }], decide: vi.fn() },
    })
    await flushPromises()
    expect(w.find('[data-test=tca-section]').exists()).toBe(false)
  })

  // review fix（spec §3.8 回填）：每張卡片的「建立升級項目」按鈕帶
  // sourceRef=approval:<id>，blockScope 依卡片的 gate/subject 換算——鏡射
  // 後端 app.go 的 scopeForSubject，讓預填值與實際阻擋語意一致。
  it('gate2 卡片點擊「建立升級項目」emit escalate，blockScope=gate2:<planID>', async () => {
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'G2-1', state: 'pending', gate: 'gate2', subject: 'plan:P1' }], decide: vi.fn() },
    })
    await w.find('[data-test=escalate-G2-1]').trigger('click')
    expect(w.emitted('escalate')).toEqual([[{ sourceRef: 'approval:G2-1', blockScope: 'gate2:P1' }]])
  })

  it('tca 卡片點擊「建立升級項目」emit escalate，blockScope=tca:<planID>/<taskID>', async () => {
    const w = mountWithI18n(GateConsole, {
      props: {
        entries: [{ approval_id: 'TCA-1', state: 'active', gate: 'test_contract_approval', subject: 'task:P1/T1' }],
        decide: vi.fn(),
      },
    })
    await w.find('[data-test=escalate-TCA-1]').trigger('click')
    expect(w.emitted('escalate')).toEqual([[{ sourceRef: 'approval:TCA-1', blockScope: 'tca:P1/T1' }]])
  })

  it('gate1 卡片點擊「建立升級項目」emit escalate，blockScope=workspace', async () => {
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate1' }], decide: vi.fn() },
    })
    await w.find('[data-test=escalate-A]').trigger('click')
    expect(w.emitted('escalate')).toEqual([[{ sourceRef: 'approval:A', blockScope: 'workspace' }]])
  })

  // M3a.1 Task 11（spec §3.5）：stale 卡片「前往重新送核」——三種 gate 各 emit
  // 正確的 (gate, subject)；畸形 subject 顯示資料完整性錯誤、不渲染按鈕、不
  // emit；導航純 view 操作，不呼叫任何寫入 binding（decide 全程零呼叫）。
  it.each([
    ['gate1', { gate: 'gate1', subject: 'workspace' }],
    ['gate2', { gate: 'gate2', subject: 'plan:P1' }],
    ['test_contract_approval', { gate: 'test_contract_approval', subject: 'task:P1/T1' }],
  ])('stale 卡片（%s）點擊「前往重新送核」emit go-resubmit 帶正確 gate/subject，且不呼叫 decide', async (_label, entry) => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'stale', ...entry }], decide },
    })
    expect(w.find('[data-test=stale-nav-error-A]').exists()).toBe(false)
    await w.find('[data-test=go-resubmit-A]').trigger('click')
    expect(w.emitted('go-resubmit')).toEqual([[entry]])
    expect(decide).not.toHaveBeenCalled()
  })

  it('stale 卡片：畸形 subject（gate2 缺 plan id）顯示資料完整性錯誤、不渲染導航按鈕、不 emit', async () => {
    const decide = vi.fn()
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'stale', gate: 'gate2', subject: 'plan:' }], decide },
    })
    expect(w.find('[data-test=stale-nav-error-A]').exists()).toBe(true)
    expect(w.find('[data-test=go-resubmit-A]').exists()).toBe(false)
    expect(decide).not.toHaveBeenCalled()
  })

  it('stale 卡片：缺 gate/subject（未知形狀）顯示資料完整性錯誤、不渲染導航按鈕', () => {
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'stale' }], decide: vi.fn() },
    })
    expect(w.find('[data-test=stale-nav-error-A]').exists()).toBe(true)
    expect(w.find('[data-test=go-resubmit-A]').exists()).toBe(false)
  })

  it('非 stale 卡片不渲染 stale-section（正常，無導航）', () => {
    const w = mountWithI18n(GateConsole, {
      props: { entries: [{ approval_id: 'A', state: 'pending', gate: 'gate1' }], decide: vi.fn() },
    })
    expect(w.find('[data-test=stale-section]').exists()).toBe(false)
  })
})
