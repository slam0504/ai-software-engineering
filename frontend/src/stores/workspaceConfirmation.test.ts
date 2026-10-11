import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { parseStatus, useWorkspaceConfirmation } from './workspaceConfirmation'

// ---- B4 R5：workspace 確認 store ----
// fail closed：只有「已取得 status，且不是 required && !confirmed」才放行。

const PENDING = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home', 'is_home'] }
const CONFIRMED = { ...PENDING, confirmed: true }
const NOT_REQUIRED = { required: false, confirmed: false, workspace: '/work/repo', source: 'env', reasons: [] }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('workspace confirmation store：封鎖判定', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('尚未取得 status：封鎖（loading），不能只判 required && !confirmed', () => {
    const wsc = useWorkspaceConfirmation()
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('loading')
    expect(wsc.blocked).toBe(true)
  })

  it('查詢失敗：封鎖（failed）並保留錯誤原文', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => { throw new Error('app 尚未完成啟動') })
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
    expect(wsc.queryError).toContain('app 尚未完成啟動')
  })

  it('required && !confirmed：封鎖（pending）；已確認或不需確認：放行', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    expect(wsc.blockKind).toBe('pending')
    expect(wsc.blocked).toBe(true)
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => NOT_REQUIRED)
    expect(wsc.blocked).toBe(false)
  })

  // Review Focus 4：壞掉的回覆不得解除封鎖，也不得被補成合法資料。
  const BAD: Array<[string, unknown]> = [
    ['空物件', {}],
    ['缺 required', { confirmed: false, workspace: '/w', source: 'env', reasons: [] }],
    ['required 不是 boolean', { required: 'false', confirmed: false, workspace: '/w', source: 'env', reasons: [] }],
    ['缺 confirmed', { required: false, workspace: '/w', source: 'env', reasons: [] }],
    ['缺 workspace', { required: false, confirmed: false, source: 'env', reasons: [] }],
    ['缺 source', { required: false, confirmed: false, workspace: '/w', reasons: [] }],
    ['source 不是字串', { required: false, confirmed: false, workspace: '/w', source: 1, reasons: [] }],
    ['缺 reasons', { required: false, confirmed: false, workspace: '/w', source: 'env' }],
    ['reasons 是 null', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: null }],
    ['reasons 不是陣列', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: 'is_home' }],
    ['reasons 含數字', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: ['is_home', 1] }],
    ['reasons 含 null', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [null] }],
    ['null', null],
    ['undefined', undefined],
    ['字串', 'ok'],
  ]

  it.each(BAD)('parseStatus 拒絕形狀不對的回覆（%s）：丟錯，不補空值', (_name, raw) => {
    expect(() => parseStatus(raw)).toThrow('malformed')
  })

  it('parseStatus 接受完整且正確的 DTO，並回傳 reasons 的複本', () => {
    const reasons = ['source_home', 'is_home']
    const st = parseStatus({ required: true, confirmed: false, workspace: '/w', source: 'home', reasons })
    expect(st).toEqual({ required: true, confirmed: false, workspace: '/w', source: 'home', reasons })
    expect(st.reasons).not.toBe(reasons)
  })

  it.each(BAD)('refresh：尚未取得時收到壞回覆（%s）→ failed，blocked=true', async (_name, raw) => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => raw)
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
  })

  // 最重要的一格：已經放行（confirmed／notRequired）之後收到壞回覆，必須回到 failed 並重新封鎖，
  // 不能沿用舊的放行狀態，也不能因為壞回覆而「看起來沒事」。
  it.each(BAD)('refresh：已放行後收到壞回覆（%s）→ 回到 failed，blocked=true', async (_name, raw) => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => raw)
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
    await wsc.refresh(async () => NOT_REQUIRED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => raw)
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
  })

  // 先前已確認、之後查詢失敗要重新封鎖（fail closed），不沿用舊的 confirmed。
  it('先前 confirmed、之後查詢失敗 → 重新封鎖', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => { throw new Error('boom') })
    expect(wsc.blocked).toBe(true)
    expect(wsc.status).toBeNull()
  })
})

describe('workspace confirmation store：晚到的舊回應', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('較舊的 request 較晚完成：不得覆蓋較新的狀態（成功回覆）', async () => {
    const wsc = useWorkspaceConfirmation()
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    const p1 = wsc.refresh(() => first.promise)
    const p2 = wsc.refresh(() => second.promise)
    second.resolve(CONFIRMED)
    await p2
    expect(wsc.blockKind).toBe('ok')
    first.resolve(PENDING) // 舊回應（確認之前查到的）較晚抵達
    await p1
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.status?.confirmed).toBe(true)
  })

  it('較舊的 request 較晚失敗：也不得覆蓋較新的狀態', async () => {
    const wsc = useWorkspaceConfirmation()
    const first = deferred<unknown>()
    const p1 = wsc.refresh(() => first.promise)
    const p2 = wsc.refresh(async () => CONFIRMED)
    await p2
    first.reject(new Error('stale failure'))
    await p1
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.queryError).toBe('')
  })
})

describe('workspace confirmation store：確認流程', () => {
  beforeEach(() => setActivePinia(createPinia()))

  async function pendingStore(workspace = PENDING.workspace) {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, workspace }))
    return wsc
  }

  // R3：前端必須傳 status.workspace 的原始字串——不縮短、不改寫成 ~、不 trim。
  it('ConfirmWorkspace 收到的是 status.workspace 原樣（含空白、中文、NFD 組合字元）', async () => {
    const raw = '/Users/測試 user/café 專案 '
    const wsc = await pendingStore(raw)
    const confirmFn = vi.fn(async (_p: string) => {})
    await wsc.confirm(confirmFn, async () => { await wsc.refresh(async () => ({ ...CONFIRMED, workspace: raw })) })
    expect(confirmFn).toHaveBeenCalledTimes(1)
    expect(confirmFn.mock.calls[0][0]).toBe(raw)
  })

  it('成功後重新查詢；放行只看後端回覆，store 不自己把 confirmed 設成 true', async () => {
    const wsc = await pendingStore()
    const refreshFn = vi.fn(async () => {}) // 後端回覆沒變（假設仍是 Pending）
    const ok = await wsc.confirm(async () => {}, refreshFn)
    expect(ok).toBe(true)
    expect(refreshFn).toHaveBeenCalledTimes(1)
    expect(wsc.blockKind).toBe('pending') // 沒有後端的 confirmed 回覆就不放行
  })

  it('確認失敗：狀態不變、保留錯誤、不重新查詢', async () => {
    const wsc = await pendingStore()
    const refreshFn = vi.fn(async () => {})
    const ok = await wsc.confirm(async () => { throw new Error('workspace_confirmation_path_mismatch') }, refreshFn)
    expect(ok).toBe(false)
    expect(wsc.confirmError).toContain('path_mismatch')
    expect(wsc.blockKind).toBe('pending')
    expect(refreshFn).not.toHaveBeenCalled()
  })

  // Review Focus 4：確認成功，但之後的重新查詢失敗 → 仍封鎖（不因為「確認成功」就放行）。
  it('確認成功但重新查詢失敗 → 封鎖', async () => {
    const wsc = await pendingStore()
    await wsc.confirm(async () => {}, async () => { await wsc.refresh(async () => { throw new Error('query down') }) })
    expect(wsc.blocked).toBe(true)
    expect(wsc.blockKind).toBe('failed')
  })

  // Review Focus 4：連點兩次只送一次 ConfirmWorkspace。
  it('確認進行中再次呼叫 confirm 會被忽略（防連點）', async () => {
    const wsc = await pendingStore()
    const gate = deferred<void>()
    const confirmFn = vi.fn(() => gate.promise)
    const p1 = wsc.confirm(confirmFn, async () => {})
    expect(wsc.confirming).toBe(true)
    const second = await wsc.confirm(confirmFn, async () => {})
    expect(second).toBe(false)
    gate.resolve()
    await p1
    expect(confirmFn).toHaveBeenCalledTimes(1)
    expect(wsc.confirming).toBe(false)
  })

  it('沒有 status（尚未取得／查詢失敗）時不送出確認', async () => {
    const wsc = useWorkspaceConfirmation()
    const confirmFn = vi.fn(async () => {})
    expect(await wsc.confirm(confirmFn, async () => {})).toBe(false)
    expect(confirmFn).not.toHaveBeenCalled()
  })
})
