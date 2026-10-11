import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { workspaceBlockKeys, workspaceReasonKeys } from './stateKeys'

// B4 drift guard：Go 端 workspace_confirmation.go 的每一個 wsReason* 代碼都必須有對應的 i18n key。
// 少了的話，UI 會把原始代碼（例如 "is_home"）直接顯示給使用者。

describe('workspace 確認的原因代碼', () => {
  it('Go 的每個 wsReason* 常數都在 workspaceReasonKeys 裡', () => {
    const src = readFileSync(join(__dirname, '../../../workspace_confirmation.go'), 'utf8')
    const codes = [...src.matchAll(/wsReason\w+\s*=\s*"([a-z_]+)"/g)].map(m => m[1])
    expect(codes.length).toBeGreaterThanOrEqual(8) // 守門本身有效：抓得到常數
    expect(codes.filter(c => !(c in workspaceReasonKeys))).toEqual([])
  })

  it('封鎖種類 loading／failed／pending 都有 key（ok 不需要）', () => {
    expect(Object.keys(workspaceBlockKeys).sort()).toEqual(['failed', 'loading', 'pending'])
  })
})
