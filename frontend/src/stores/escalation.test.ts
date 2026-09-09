import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useEscalation } from './escalation'
import { escalation } from '../../wailsjs/go/models'

// 用真實 wailsjs escalation.Entry class（非 plain object）建構 fixture——
// store.load() 的簽章直接鎖 escalation.Entry[]（同 production EscalationList
// 的回傳型別），plain object 缺 convertValues 方法過不了 vue-tsc；
// createFrom 是 wailsjs 產生的標準建構入口。
const entry = (id: string, state: string) => escalation.Entry.createFrom({
  Item: {
    _type: 'escalation_item', escalation_id: id, condition_key: '', occurrence: 1,
    source: 'manual', source_ref: 'P1/T1', block_scope: 'workspace', hard: false,
    summary: 's', created_at: '2026-01-01T00:00:00Z',
  },
  State: state,
})

describe('escalation store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('load() 成功時落地 entries 且清空 unavailable', async () => {
    const s = useEscalation()
    s.unavailable = 'stale error' // 前一次失敗殘留
    await s.load(async () => [entry('E1', 'open')])
    expect(s.entries).toHaveLength(1)
    expect(s.unavailable).toBe('')
  })

  it('load() 失敗時 unavailable＝錯誤原文，絕不裝空（§3.8）', async () => {
    const s = useEscalation()
    await s.load(async () => { throw new Error('escalation: journal degraded') })
    expect(s.unavailable).toBe('Error: escalation: journal degraded')
  })

  it('open／acknowledged／resolved getters 依 State 分區', async () => {
    const s = useEscalation()
    await s.load(async () => [entry('E1', 'open'), entry('E2', 'acknowledged'), entry('E3', 'resolved')])
    expect(s.open.map(e => e.Item.escalation_id)).toEqual(['E1'])
    expect(s.acknowledged).toHaveLength(1)
    expect(s.resolved).toHaveLength(1)
  })

  it('unresolvedCount 只算未 resolved 的項目', async () => {
    const s = useEscalation()
    await s.load(async () => [entry('E1', 'open'), entry('E2', 'acknowledged'), entry('E3', 'resolved')])
    expect(s.unresolvedCount).toBe(2)
  })

  it('連續 load()：先發後到的舊成功回應不得覆蓋新狀態（A2-1）', async () => {
    const s = useEscalation()
    let resolveOld!: (v: escalation.Entry[]) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>(r => { resolveOld = r }))
    await s.load(async () => [entry('E-new', 'open')])
    resolveOld([entry('E-old', 'open'), entry('E-old2', 'open')])
    await p1
    expect(s.entries.map(e => e.Item.escalation_id)).toEqual(['E-new'])
    expect(s.unresolvedCount).toBe(1)
  })

  it('連續 load()：舊呼叫的失敗不得把新成功狀態改成 unavailable（A2-1）', async () => {
    const s = useEscalation()
    let rejectOld!: (e: unknown) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>((_, rej) => { rejectOld = rej }))
    await s.load(async () => [entry('E-new', 'open')])
    rejectOld(new Error('stale failure'))
    await p1
    expect(s.unavailable).toBe('')
    expect(s.entries).toHaveLength(1)
  })

  it('連續 load()：較新請求失敗後，較舊成功回應不得清除 unavailable（A2-1）', async () => {
    const s = useEscalation()
    let resolveOld!: (v: escalation.Entry[]) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>(r => { resolveOld = r }))
    await s.load(async () => { throw new Error('journal degraded') })
    expect(s.unavailable).toBe('Error: journal degraded')
    resolveOld([entry('E-old', 'open')])
    await p1
    expect(s.unavailable).toBe('Error: journal degraded')
    expect(s.entries).toHaveLength(0)
  })

})
