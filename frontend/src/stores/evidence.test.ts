import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useEvidence } from './evidence'
import type { Envelope } from '../types'

const env = (payload: Record<string, unknown>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'evidence_run', scope: 'workspace', payload,
})

describe('evidence store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('started event marks the run as running', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({
      phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', test_commit: 'deadbeef',
    }))
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'running', testCommit: 'deadbeef' })
  })

  it('started → finished(passed) transitions running→passed and records evidence_id', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' }))
    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', evidence_id: 'ev-1', result: 'passed',
    }))
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-1' })
  })

  it('finished with an error payload lands as status=error and keeps the raw message', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'negative_control' }))
    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'negative_control',
      evidence_id: 'ev-2', error: 'evidence: run canceled: context canceled',
    }))
    expect(e.runOf('G1', 'P1', 'T1', 'negative_control')).toMatchObject({
      status: 'error', evidenceId: 'ev-2', error: 'evidence: run canceled: context canceled',
    })
  })

  it('a finished event with no matching run is a no-op elsewhere but still lands on its own (approval,plan,task,kind) cell', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', evidence_id: 'ev-3', result: 'passed',
    }))
    // finished 不再需要事先有 started（review fix：直接用 payload 的三個識別
    // 欄位定位，不靠 pendingKey FIFO），所以會直接落地在自己的格子。
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-3' })
    // 其他格子不受影響。
    expect(e.runOf('G1', 'P1', 'T1', 'negative_control')).toBeUndefined()
  })

  it('缺 plan_id/task_id/kind 任一識別欄位的事件不落地（無法定位格子就不猜）', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1' })) // 缺 kind
    expect(Object.keys((e as any).runs)).toHaveLength(0)
  })

  // review finding（Medium correctness）：同一 task 的 expected_red／
  // negative_control 兩顆按鈕互不 disable 時，先按 red 再按 negative_control
  // 會讓兩筆 started 依序抵達；若靠「最近一筆 started」的 pendingKey FIFO 配
  // 對，red 先完成時 finished 會被誤寫進 negative_control 那格。改用 payload
  // 帶的 (plan_id,task_id,kind) 直接定位後，交錯順序不再影響結果。
  it('兩筆 run 交錯 started/finished 各自落在正確格子，不因交錯順序錯位', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' }))
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'negative_control' }))
    // expected_red 先完成——若走舊的 pendingKey FIFO，這筆 finished 會被誤寫
    // 進「最近一筆 started」也就是 negative_control 那格。
    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', evidence_id: 'ev-red', result: 'passed',
    }))

    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-red' })
    // negative_control 必須仍在執行中，不能被 red 的 finished 誤標成 passed。
    expect(e.runOf('G1', 'P1', 'T1', 'negative_control')).toMatchObject({ status: 'running' })
    expect(e.taskHasRunInFlight('G1', 'P1', 'T1')).toBe(true)

    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'negative_control', evidence_id: 'ev-neg', result: 'failed',
    }))
    expect(e.runOf('G1', 'P1', 'T1', 'negative_control')).toMatchObject({ status: 'failed', evidenceId: 'ev-neg' })
    expect(e.taskHasRunInFlight('G1', 'P1', 'T1')).toBe(false)
  })

  it('setResult/setError are the authoritative direct-call path, independent of the event stream', () => {
    const e = useEvidence()
    e.setRunning('G1', 'P1', 'T1', 'expected_red')
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')?.status).toBe('running')
    e.setResult('G1', 'P1', 'T1', 'expected_red', 'ev-4', 'passed')
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-4' })

    e.setError('G1', 'P1', 'T1', 'negative_control', 'evidence: no active Gate 2 approval for plan "P1"')
    expect(e.runOf('G1', 'P1', 'T1', 'negative_control')).toMatchObject({
      status: 'error', error: 'evidence: no active Gate 2 approval for plan "P1"',
    })
  })

  it('registerMutation records mutation_id -> task_ref', () => {
    const e = useEvidence()
    e.registerMutation('mut-1', 'P1/T1')
    expect(e.mutations['mut-1']).toBe('P1/T1')
  })

  // Task 9（§3.3.1-2）：run key 擴為 (gate2_approval_id, plan_id, task_id, kind)
  // ——同一 plan/task/kind 若因換版重跑（新的 Gate 2 approval），新舊兩次
  // 執行要落在各自獨立的格子，不能互相覆蓋。
  it('不同 approval 的同一 (plan,task,kind) 各自獨立，互不覆蓋', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')
    e.applyEvidenceEvent(env({
      phase: 'finished', gate2_approval_id: 'G1', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', evidence_id: 'ev-g1', result: 'passed',
    }))
    e.setCurrentGeneration('G2')
    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G2', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' }))

    // G1 的舊結果不受 G2 新一輪 started 影響。
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-g1' })
    expect(e.runOf('G2', 'P1', 'T1', 'expected_red')).toMatchObject({ status: 'running' })
  })

  // Task 9（§3.3.1-2）：事件缺 gate2_approval_id，或跟目前 generation
  // （currentGenerationApprovalId）不符，一律丟棄，不落地到任何格子。
  it('事件缺 gate2_approval_id 或跟目前 generation 不符時丟棄', () => {
    const e = useEvidence()
    e.setCurrentGeneration('G1')

    e.applyEvidenceEvent(env({ phase: 'started', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' })) // 缺 gate2_approval_id
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toBeUndefined()

    e.applyEvidenceEvent(env({ phase: 'started', gate2_approval_id: 'G0-stale', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' }))
    expect(e.runOf('G0-stale', 'P1', 'T1', 'expected_red')).toBeUndefined()
    expect(e.runOf('G1', 'P1', 'T1', 'expected_red')).toBeUndefined()
  })
})
