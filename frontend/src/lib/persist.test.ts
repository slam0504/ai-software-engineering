import { beforeEach, describe, expect, it } from 'vitest'
import { load, save } from './persist'

// 此 jsdom 版本未提供 localStorage——以 in-memory stub 取代（介面相同）
const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => { store.set(k, String(v)) },
    removeItem: (k: string) => { store.delete(k) },
    clear: () => { store.clear() },
  },
})

describe('persist', () => {
  beforeEach(() => localStorage.clear())

  it('round-trips values', () => {
    save('k', 240)
    expect(load('k', 0)).toBe(240)
    save('b', false)
    expect(load('b', true)).toBe(false)
  })

  it('falls back on missing or malformed values', () => {
    expect(load('missing', 180)).toBe(180)
    localStorage.setItem('bad', '{not json')
    expect(load('bad', 'fb')).toBe('fb')
  })
})
