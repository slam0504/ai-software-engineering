// keyRefs.test.ts — 掃 src 下所有 t('...') / i18n.global.t('...') literal，斷言存在於 leaf paths
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import zhTW from './locales/zh-TW'

function leafSet(obj: any, prefix = '', out = new Set<string>()): Set<string> {
  for (const [k, v] of Object.entries(obj)) {
    const p = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') leafSet(v, p, out); else out.add(p)
  }
  return out
}
function walk(dir: string, acc: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const f = join(dir, e)
    if (statSync(f).isDirectory()) walk(f, acc)
    else if (/\.(vue|ts)$/.test(f) && !f.endsWith('.test.ts')) acc.push(f)
  }
  return acc
}
import { sessionStateKeys, gateStateKeys, codexToolStatusKeys, evidenceResultKeys, riskTierKeys } from './stateKeys'
describe('t() key references exist', () => {
  it('every literal t(\'...\') key is a locale leaf', () => {
    const keys = leafSet(zhTW)
    const files = walk(join(__dirname, '..'))
    const re = /(?:\bt|i18n\.global\.t)\(\s*['"]([\w.]+)['"]/g
    const missing: string[] = []
    for (const f of files) {
      const src = readFileSync(f, 'utf8')
      for (const m of src.matchAll(re)) if (!keys.has(m[1])) missing.push(`${m[1]} @ ${f}`)
    }
    expect(missing).toEqual([])
  })
  it('every state-map value is a locale leaf (maps referenced dynamically, not by literal t())', () => {
    const keys = leafSet(zhTW)
    const mapVals = [
      ...Object.values(sessionStateKeys), ...Object.values(gateStateKeys), ...Object.values(codexToolStatusKeys),
      ...Object.values(evidenceResultKeys), ...Object.values(riskTierKeys),
    ]
    expect(mapVals.filter(v => !keys.has(v))).toEqual([])
  })
  it('every SettingsBar operationAction key resolves to a locale leaf', () => {
    const keys = leafSet(zhTW)
    const opKeys = ['new', 'terminate', 'end', 'authStatus', 'login', 'cancelLogin', 'logout', 'b1Probe']
    const missing = opKeys.map(k => `settings.operationAction.${k}`).filter(k => !keys.has(k))
    expect(missing).toEqual([])
  })
})
