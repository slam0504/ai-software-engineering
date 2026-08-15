import { describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({
  StartSession: vi.fn(async (..._a: string[]) => {}),
  SendMessage: vi.fn(async (..._a: string[]) => {}),
  CreateSession: vi.fn(async () => 'wsid-1'),
  RemoveSession: vi.fn(async () => {}),
  EndSession: vi.fn(async () => {}),
  NewSession: vi.fn(async () => {}),
  TerminateSession: vi.fn(async () => {}),
  ListSessions: vi.fn(async () => []),
  RecoverCodexRecording: vi.fn(async () => {}),
  LoadTurnsBefore: vi.fn(async () => []),
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

// P1 迴歸：adapter 必須逐參數轉發——單參數版本會把第一參數送成訊息內容
describe('production bindings adapter', () => {
  // M3b Task 26 原子切換：第一參數由 provider 改為 WSID。這條守的是「轉發的
  // 就是呼叫端給的那個 WSID」，並明確排除誤傳 provider 名稱。
  it('WSID 取代 provider 作為第一參數——不得誤傳 provider', async () => {
    const b = makeBindings()
    await b.SendMessage('01JWSIDABC', 'text')
    const [first] = vi.mocked(h.SendMessage).mock.calls.at(-1)!
    expect(first).toBe('01JWSIDABC')
    expect(['claude', 'codex']).not.toContain(first)
  })

  it('forwards both SendMessage arguments positionally', async () => {
    const b = makeBindings()
    await b.SendMessage('w1', 'the real message')
    expect(h.SendMessage).toHaveBeenCalledWith('w1', 'the real message')
    await b.SendMessage('w2', 'second round')
    expect(h.SendMessage).toHaveBeenCalledWith('w2', 'second round')
  })

  it('StartSession／EndSession 逐參數轉發且第一參數為 WSID', async () => {
    const b = makeBindings()
    await b.StartSession('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')
    expect(h.StartSession).toHaveBeenCalledWith('w1', 'prompt', 'resume-id', 'case', 'label', 'untrusted')
    await b.EndSession('w1')
    expect(h.EndSession).toHaveBeenCalledWith('w1')
  })

  it('NewSession／TerminateSession 以 WSID 定址', async () => {
    const b = makeBindings()
    await b.NewSession('w1')
    expect(h.NewSession).toHaveBeenCalledWith('w1')
    await b.TerminateSession('w2')
    expect(h.TerminateSession).toHaveBeenCalledWith('w2')
  })

  it('ListSessions 轉發至 Go 綁定（前端 session 清單的唯一來源）', async () => {
    const b = makeBindings()
    await b.ListSessions()
    expect(h.ListSessions).toHaveBeenCalledWith()
  })

  // M3b Task 4：CreateSession 是純新增的多參數綁定，同一教訓套用。
  it('CreateSession 逐參數轉發', async () => {
    const b = makeBindings()
    await b.CreateSession('claude', 'my-task')
    expect(h.CreateSession).toHaveBeenCalledWith('claude', 'my-task')
  })

  // M3b Task 22：RemoveSession 逐參數轉發——purely additive binding。
  it('RemoveSession 逐參數轉發', async () => {
    const b = makeBindings()
    await b.RemoveSession('w1')
    expect(h.RemoveSession).toHaveBeenCalledWith('w1')
  })

  // M3b Task 13：RecoverCodexRecording 無參數，仍要鎖「adapter 真的打到 Go 綁定」
  // ——§3.4.6 的 latch 只有這條路徑能解除，adapter 接錯就是永久 degraded。
  it('RecoverCodexRecording 轉發至 Go 綁定', async () => {
    const b = makeBindings()
    await b.RecoverCodexRecording()
    expect(h.RecoverCodexRecording).toHaveBeenCalledWith()
  })

  // M3b Task 20：LoadTurnsBefore 是 §3.8 的視窗化載入／向上分頁，三個參數且
  // 順序（wsid, beforeEventID, n）不能互換——把 cursor 與 wsid 對調會安靜地
  // 回一個空頁，UI 只會看起來「沒有更舊的訊息」。
  it('LoadTurnsBefore 逐參數轉發', async () => {
    const b = makeBindings()
    await b.LoadTurnsBefore('w1', 'e100', 20)
    expect(h.LoadTurnsBefore).toHaveBeenCalledWith('w1', 'e100', 20)
    await b.LoadTurnsBefore('w2', '', 20)
    expect(h.LoadTurnsBefore).toHaveBeenCalledWith('w2', '', 20)
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
