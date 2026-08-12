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
    e.applyEvidenceEvent(env({ phase: 'started', plan_id: 'P1', task_id: 'T1', kind: 'expected_red', test_commit: 'deadbeef' }))
    expect(e.runOf('P1', 'T1', 'expected_red')).toMatchObject({ status: 'running', testCommit: 'deadbeef' })
  })

  it('started → finished(passed) transitions running→passed and records evidence_id', () => {
    const e = useEvidence()
    e.applyEvidenceEvent(env({ phase: 'started', plan_id: 'P1', task_id: 'T1', kind: 'expected_red' }))
    e.applyEvidenceEvent(env({ phase: 'finished', evidence_id: 'ev-1', result: 'passed' }))
    expect(e.runOf('P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-1' })
  })

  it('finished with an error payload lands as status=error and keeps the raw message', () => {
    const e = useEvidence()
    e.applyEvidenceEvent(env({ phase: 'started', plan_id: 'P1', task_id: 'T1', kind: 'negative_control' }))
    e.applyEvidenceEvent(env({ phase: 'finished', evidence_id: 'ev-2', error: 'evidence: run canceled: context canceled' }))
    expect(e.runOf('P1', 'T1', 'negative_control')).toMatchObject({
      status: 'error', evidenceId: 'ev-2', error: 'evidence: run canceled: context canceled',
    })
  })

  it('a finished event with no prior pending started is a no-op (no spurious entry)', () => {
    const e = useEvidence()
    e.applyEvidenceEvent(env({ phase: 'finished', evidence_id: 'ev-3', result: 'passed' }))
    expect(e.runOf('P1', 'T1', 'expected_red')).toBeUndefined()
  })

  it('setResult/setError are the authoritative direct-call path, independent of the event stream', () => {
    const e = useEvidence()
    e.setRunning('P1', 'T1', 'expected_red')
    expect(e.runOf('P1', 'T1', 'expected_red')?.status).toBe('running')
    e.setResult('P1', 'T1', 'expected_red', 'ev-4', 'passed')
    expect(e.runOf('P1', 'T1', 'expected_red')).toMatchObject({ status: 'passed', evidenceId: 'ev-4' })

    e.setError('P1', 'T1', 'negative_control', 'evidence: no active Gate 2 approval for plan "P1"')
    expect(e.runOf('P1', 'T1', 'negative_control')).toMatchObject({
      status: 'error', error: 'evidence: no active Gate 2 approval for plan "P1"',
    })
  })

  it('registerMutation records mutation_id -> task_ref', () => {
    const e = useEvidence()
    e.registerMutation('mut-1', 'P1/T1')
    expect(e.mutations['mut-1']).toBe('P1/T1')
  })
})
