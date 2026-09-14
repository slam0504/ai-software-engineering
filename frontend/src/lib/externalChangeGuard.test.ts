import { describe, expect, it } from 'vitest'
import {
  createExternalChangeGuard, shouldApplyBackground, type GuardSnapshot,
} from './externalChangeGuard'

// 基準戳記：四欄皆有效，各案只改其中一欄，藉此證明 B1–B4 是**各自獨立**的必要
// 條件——owner 要求「不能單憑 C3 通過就宣稱 B1 與 B4 各自有效」。
const base: GuardSnapshot = { gen: 5, path: 'spec/a.feature', instanceId: 1, baselineDigest: 'sha256:aaa' }

describe('shouldApplyBackground（B1–B4 四項核對，各自為必要條件）', () => {
  it('四欄全等才套用', () => {
    expect(shouldApplyBackground(base, { ...base })).toBe(true)
  })

  it('B1：唯獨世代不同即拒絕（其他三項有效）', () => {
    expect(shouldApplyBackground(base, { ...base, gen: 6 })).toBe(false)
  })

  it('B2：唯獨目前檔案不同即拒絕（其他三項有效）', () => {
    expect(shouldApplyBackground(base, { ...base, path: 'spec/b.feature' })).toBe(false)
  })

  it('B3：唯獨元件實例不同即拒絕（其他三項有效）', () => {
    expect(shouldApplyBackground(base, { ...base, instanceId: 2 })).toBe(false)
  })

  // ★ owner 指定的獨立證明：C3 同時被 B1 與 B4 擋住，故 C3 通過不足以證明 B4 有效。
  // 這裡讓世代、檔案、實例三項**都保持有效**，只把寫入基準換成寫入成功後的新值
  // ——正是「期間寫入成功使基準更新」的形狀——B4 仍須拒絕。
  it('B4：唯獨寫入基準改變即拒絕（世代／檔案／實例皆有效）', () => {
    const current = { ...base, baselineDigest: 'sha256:bbb' }
    expect(current.gen).toBe(base.gen)
    expect(current.path).toBe(base.path)
    expect(current.instanceId).toBe(base.instanceId)
    expect(shouldApplyBackground(base, current)).toBe(false)
  })
})

describe('createExternalChangeGuard（前景所有權與背景世代）', () => {
  it('取得所有權時即遞增世代，使此前在途的背景讀取立即失效（C3：不等回應、不看 busy）', () => {
    const g = createExternalChangeGuard()
    const stamp = g.snapshot('spec/a.feature', 'sha256:aaa', true) // 背景讀取 A 發出

    const token = g.acquire() // 使用者按下儲存
    // 失效在此刻定案——此時前景尚未完成，busy 仍在，但 A 已經不可套用
    expect(shouldApplyBackground(stamp, g.snapshot('spec/a.feature', 'sha256:aaa'))).toBe(false)

    g.release(token) // 寫入完成、busy 清除
    // 釋放所有權不會讓先前失效的背景讀取復活
    expect(shouldApplyBackground(stamp, g.snapshot('spec/a.feature', 'sha256:aaa'))).toBe(false)
  })

  it('所有權期間不得發出背景讀取，且比對用取樣不遞增世代（C1：不得淘汰進行中的寫入）', () => {
    const g = createExternalChangeGuard()
    const token = g.acquire()
    expect(g.canStartBackgroundRead()).toBe(false)
    expect(g.hasOwner()).toBe(true)

    // 前景自己的多次取樣不得互相淘汰
    const a = g.snapshot('spec/a.feature', 'sha256:aaa')
    const b = g.snapshot('spec/a.feature', 'sha256:aaa')
    expect(a.gen).toBe(b.gen)

    // 前景回應有效性只由所有權決定
    expect(g.isOwner(token)).toBe(true)
    g.release(token)
    expect(g.canStartBackgroundRead()).toBe(true)
  })

  it('較新的背景讀取淘汰較舊的（B1 在背景之間仍有效）', () => {
    const g = createExternalChangeGuard()
    const older = g.snapshot('spec/a.feature', 'sha256:aaa', true)
    const newer = g.snapshot('spec/a.feature', 'sha256:aaa', true)
    const now = g.snapshot('spec/a.feature', 'sha256:aaa')
    expect(shouldApplyBackground(newer, now)).toBe(true)
    expect(shouldApplyBackground(older, now)).toBe(false)
  })

  it('release 只認目前擁有者，遲到的釋放不會放掉新操作的所有權', () => {
    const g = createExternalChangeGuard()
    const first = g.acquire()
    g.release(first)
    const second = g.acquire()
    g.release(first) // 遲到的 finally
    expect(g.isOwner(second)).toBe(true)
    expect(g.hasOwner()).toBe(true)
  })

  it('dispose 後任何回應都不得套用（B3：元件生命週期）', () => {
    const g = createExternalChangeGuard()
    const token = g.acquire()
    g.dispose()
    expect(g.isActive()).toBe(false)
    expect(g.isOwner(token)).toBe(false)
    expect(g.canStartBackgroundRead()).toBe(false)
  })

  it('每個實例有各自的 instanceId（跨掛載的回應不會互相套用）', () => {
    const a = createExternalChangeGuard()
    const b = createExternalChangeGuard()
    expect(a.instanceId).not.toBe(b.instanceId)
  })
})

describe('invalidateBackground（不取得所有權、但使在途背景讀取失效）', () => {
  it('load／bump 這類既有 busy 開始時呼叫，即可使此前在途的背景讀取失效', () => {
    const g = createExternalChangeGuard()
    const stamp = g.snapshot('spec/a.feature', 'sha256:aaa', true) // 背景讀取在途

    g.invalidateBackground() // loadFile／confirmBump 開始
    expect(shouldApplyBackground(stamp, g.snapshot('spec/a.feature', 'sha256:aaa'))).toBe(false)
  })

  it('不取得所有權——呼叫後背景檢查仍可重新發出', () => {
    const g = createExternalChangeGuard()
    g.invalidateBackground()
    expect(g.hasOwner()).toBe(false)
    expect(g.canStartBackgroundRead()).toBe(true)
  })

  it('基準不變時 B4 擋不住，仍須靠本方法失效（confirmBump 只改 buffer 不改基準的形狀）', () => {
    const g = createExternalChangeGuard()
    const stamp = g.snapshot('plan/P1.yaml', 'sha256:same', true)
    g.invalidateBackground()
    // 路徑、實例、基準三項都沒變——只有世代變了
    const now = g.snapshot('plan/P1.yaml', 'sha256:same')
    expect(now.path).toBe(stamp.path)
    expect(now.instanceId).toBe(stamp.instanceId)
    expect(now.baselineDigest).toBe(stamp.baselineDigest)
    expect(shouldApplyBackground(stamp, now)).toBe(false)
  })
})
