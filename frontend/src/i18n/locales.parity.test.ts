import { describe, it, expect } from 'vitest'
import zhTW from './locales/zh-TW'
import en from './locales/en'

function leaves(obj: any, prefix = ''): Record<string, 'string' | 'object'> {
  const out: Record<string, 'string' | 'object'> = {}
  for (const [k, v] of Object.entries(obj)) {
    const p = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') { out[p] = 'object'; Object.assign(out, leaves(v, p)) }
    else out[p] = 'string'
  }
  return out
}
describe('locale parity', () => {
  it('zh-TW and en have identical leaf paths and types', () => {
    const a = leaves(zhTW), b = leaves(en)
    expect(Object.keys(a).sort()).toEqual(Object.keys(b).sort())
    for (const k of Object.keys(a)) expect(b[k]).toBe(a[k]) // 型別一致
  })
})
