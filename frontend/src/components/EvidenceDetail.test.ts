import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import EvidenceDetail from './EvidenceDetail.vue'
import { mountWithI18n } from '../test/i18n'

const record = {
  evidence_id: 'ev-1', kind: 'expected_red', source: 'local_app',
  base_commit: 'a'.repeat(40), test_commit: 'a'.repeat(40),
  oracle_surface_digest: 'sha256:' + 'b'.repeat(64), mutation_digest: '',
  command: { executable: 'sh', argv: ['run_test.sh'] },
  cwd: 'worktree:ev-1', started_at: 't1', finished_at: 't2', exit_code: 1,
  expected_failure: { test_ids: ['TestX'], matcher: 'FAIL' }, observed_failure: 'FAIL: TestX',
  stdout_digest: 'sha256:' + 'c'.repeat(64), stderr_digest: 'sha256:' + 'd'.repeat(64),
  recording_ref: '/tmp/cas', runner_version: 'm3a-1', result: 'passed',
}

describe('EvidenceDetail', () => {
  it('empty evidenceId → not rendered, get not called', () => {
    const get = vi.fn()
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: '', get } })
    expect(w.find('[data-test=evidence-detail]').exists()).toBe(false)
    expect(get).not.toHaveBeenCalled()
  })

  it('renders the full record on success, digests shown short with full value in title', async () => {
    const get = vi.fn().mockResolvedValue(record)
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: 'ev-1', get } })
    await flushPromises()
    expect(get).toHaveBeenCalledWith('ev-1')
    expect(w.find('[data-test=evidence-fields]').exists()).toBe(true)
    expect(w.find('[data-test=evidence-result]').text()).toContain('通過')
    const oracleDigestCell = w.find('[data-test=evidence-fields] dd[title^="sha256:bbbb"]')
    expect(oracleDigestCell.exists()).toBe(true)
    expect(oracleDigestCell.text().length).toBeLessThan(record.oracle_surface_digest.length)
  })

  it('get() failure shows the raw error text, not the fields', async () => {
    const get = vi.fn().mockRejectedValue(new Error('evidence: not found: evidence_id ev-x'))
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: 'ev-x', get } })
    await flushPromises()
    expect(w.find('[data-test=evidence-error]').text()).toContain('evidence: not found: evidence_id ev-x')
    expect(w.find('[data-test=evidence-fields]').exists()).toBe(false)
  })

  it('close button emits close', async () => {
    const get = vi.fn().mockResolvedValue(record)
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: 'ev-1', get } })
    await flushPromises()
    await w.find('[data-test=close]').trigger('click')
    expect(w.emitted('close')).toBeTruthy()
  })

  it('re-fetches when evidenceId prop changes', async () => {
    const get = vi.fn().mockResolvedValue(record)
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: 'ev-1', get } })
    await flushPromises()
    await w.setProps({ evidenceId: 'ev-2' })
    await flushPromises()
    expect(get).toHaveBeenCalledWith('ev-2')
    expect(get).toHaveBeenCalledTimes(2)
  })

  // review fix（spec §3.8 回填）：「建立升級項目」帶 sourceRef=evidence:<id>、
  // blockScope=evidence:<id>（同一份 id 兩用——收件匣沒有另外的 evidence
  // scope 下拉，直接用完整字串當 block scope）。
  it('點擊「建立升級項目」emit escalate，sourceRef／blockScope 都是 evidence:<id>', async () => {
    const get = vi.fn().mockResolvedValue(record)
    const w = mountWithI18n(EvidenceDetail, { props: { evidenceId: 'ev-1', get } })
    await flushPromises()

    await w.find('[data-test=escalate]').trigger('click')
    expect(w.emitted('escalate')).toEqual([[{ sourceRef: 'evidence:ev-1', blockScope: 'evidence:ev-1' }]])
  })
})
