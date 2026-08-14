import { describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({
  StartSession: vi.fn(async () => {}),
  SendMessage: vi.fn(async () => {}),
  RegisterMutation: vi.fn(async () => 'mutation-id'),
  RunEvidence: vi.fn(async () => 'evidence-id'),
  EvidenceGet: vi.fn(async () => ({})),
  SubmitTestContract: vi.fn(async () => 'approval-id'),
  ValidateTestCommit: vi.fn(async () => {}),
  EvidenceCommitCandidates: vi.fn(async () => []),
  EscalationList: vi.fn(async () => []),
  EscalationCreate: vi.fn(async () => 'esc-id'),
  EscalationAck: vi.fn(async () => {}),
  EscalationResolve: vi.fn(async () => {}),
}))
vi.mock('../../wailsjs/go/main/App', () => h)

import { makeBindings } from './bindings'

// P1 迴歸：adapter 必須逐參數轉發——單參數版本會把 provider 名送成訊息內容
describe('production bindings adapter', () => {
  it('forwards both SendMessage arguments positionally', async () => {
    const b = makeBindings()
    await b.SendMessage('claude', 'the real message')
    expect(h.SendMessage).toHaveBeenCalledWith('claude', 'the real message')
    await b.SendMessage('codex', 'second round')
    expect(h.SendMessage).toHaveBeenCalledWith('codex', 'second round')
  })

  it('forwards all six StartSession arguments', async () => {
    const b = makeBindings()
    await b.StartSession('codex', 'prompt', 'resume-id', 'rec', 'task', 'untrusted')
    expect(h.StartSession).toHaveBeenCalledWith('codex', 'prompt', 'resume-id', 'rec', 'task', 'untrusted')
  })

  // Task 22：TCA workspace 六個新綁定，逐一鎖參數順序與名稱——Go 測試驗不到
  // TS adapter 的轉發正確性（app_evidence_test.go 只驗 Go 端本身）。
  it('forwards both RegisterMutation arguments positionally', async () => {
    const b = makeBindings()
    await b.RegisterMutation('P1/T1', 'diff --git a/x b/x')
    expect(h.RegisterMutation).toHaveBeenCalledWith('P1/T1', 'diff --git a/x b/x')
  })

  // M3a.1 T8（§3.3.2）：換版 CAS 加了第一個參數 expectedGate2ApprovalID——
  // 同一教訓，逐參數轉發、順序鎖死。
  it('forwards all six RunEvidence arguments positionally', async () => {
    const b = makeBindings()
    await b.RunEvidence('appr-1', 'P1', 'T1', 'deadbeef', 'expected_red', '')
    expect(h.RunEvidence).toHaveBeenCalledWith('appr-1', 'P1', 'T1', 'deadbeef', 'expected_red', '')
    await b.RunEvidence('appr-1', 'P1', 'T1', 'deadbeef', 'negative_control', 'mut-1')
    expect(h.RunEvidence).toHaveBeenCalledWith('appr-1', 'P1', 'T1', 'deadbeef', 'negative_control', 'mut-1')
  })

  it('forwards the single EvidenceGet argument', async () => {
    const b = makeBindings()
    await b.EvidenceGet('ev-1')
    expect(h.EvidenceGet).toHaveBeenCalledWith('ev-1')
  })

  it('forwards all six SubmitTestContract arguments positionally', async () => {
    const b = makeBindings()
    await b.SubmitTestContract('P1', 'T1', 'deadbeef', 'red-1', 'neg-1', 'mut-1')
    expect(h.SubmitTestContract).toHaveBeenCalledWith('P1', 'T1', 'deadbeef', 'red-1', 'neg-1', 'mut-1')
  })

  it('forwards all three ValidateTestCommit arguments positionally', async () => {
    const b = makeBindings()
    await b.ValidateTestCommit('P1', 'T1', 'deadbeef')
    expect(h.ValidateTestCommit).toHaveBeenCalledWith('P1', 'T1', 'deadbeef')
  })

  it('forwards the single EvidenceCommitCandidates argument', async () => {
    const b = makeBindings()
    await b.EvidenceCommitCandidates('P1')
    expect(h.EvidenceCommitCandidates).toHaveBeenCalledWith('P1')
  })

  // Task 25：escalation 收件匣四個新綁定，同一教訓套用。
  it('forwards zero EscalationList arguments', async () => {
    const b = makeBindings()
    await b.EscalationList()
    expect(h.EscalationList).toHaveBeenCalledWith()
  })

  it('forwards all three EscalationCreate arguments positionally', async () => {
    const b = makeBindings()
    await b.EscalationCreate('P1/T1', 'gate2:P1', 'summary text')
    expect(h.EscalationCreate).toHaveBeenCalledWith('P1/T1', 'gate2:P1', 'summary text')
  })

  it('forwards the single EscalationAck argument', async () => {
    const b = makeBindings()
    await b.EscalationAck('esc-1')
    expect(h.EscalationAck).toHaveBeenCalledWith('esc-1')
  })

  it('forwards all three EscalationResolve arguments positionally', async () => {
    const b = makeBindings()
    await b.EscalationResolve('esc-1', 'fixed', 'because reasons')
    expect(h.EscalationResolve).toHaveBeenCalledWith('esc-1', 'fixed', 'because reasons')
  })
})
