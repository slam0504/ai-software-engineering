import { describe, expect, it, vi } from 'vitest'
import { withEscalationReload } from './escalationReload'

describe('withEscalationReload（A2-1：重載由 App 持有，與呼叫元件是否仍掛載無關）', () => {
  it('成功：先 reload 再回傳原值', async () => {
    const order: string[] = []
    const reload = vi.fn(async () => { order.push('reload') })
    const fn = vi.fn(async (a: string) => { order.push('fn'); return 'id-' + a })
    const wrapped = withEscalationReload(fn, reload)
    await expect(wrapped('x')).resolves.toBe('id-x')
    expect(fn).toHaveBeenCalledWith('x')
    expect(order).toEqual(['fn', 'reload'])
  })
  it('失敗：先 reload 再重拋原錯誤（失敗也可能已建立 blocker）', async () => {
    const reload = vi.fn(async () => {})
    const err = new Error('缺必要 binding')
    const wrapped = withEscalationReload(async () => { throw err }, reload)
    await expect(wrapped()).rejects.toBe(err)
    expect(reload).toHaveBeenCalledTimes(1)
  })
  it('等待中的呼叫在 fn 完成後才 reload（呼叫端可能已卸載，reload 仍執行）', async () => {
    const reload = vi.fn(async () => {})
    let resolveFn!: (v: string) => void
    const wrapped = withEscalationReload(() => new Promise<string>(r => { resolveFn = r }), reload)
    const p = wrapped()
    expect(reload).not.toHaveBeenCalled()
    resolveFn('done')
    await expect(p).resolves.toBe('done')
    expect(reload).toHaveBeenCalledTimes(1)
  })
  it('reload 完成前 wrapped Promise 尚未結束（等待順序：fn → reload → 回傳）', async () => {
    let finishReload!: () => void
    const reload = vi.fn(() => new Promise<void>(r => { finishReload = r }))
    const wrapped = withEscalationReload(async () => 'v', reload)
    let settled = false
    const p = wrapped().then(v => { settled = true; return v })
    await Promise.resolve(); await Promise.resolve() // 讓 fn 與 reload 開始
    expect(reload).toHaveBeenCalledTimes(1)
    expect(settled).toBe(false)
    finishReload()
    await expect(p).resolves.toBe('v')
    expect(settled).toBe(true)
  })
})
