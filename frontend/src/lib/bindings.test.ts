import { describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({
  StartSession: vi.fn(async () => {}),
  SendMessage: vi.fn(async () => {}),
}))
vi.mock('../../wailsjs/go/main/App', () => h)

import { makeBindings } from './bindings'

// P1 迴歸：adapter 必須逐參數轉發——單參數版本會把 provider 名送成訊息內容
describe('production bindings adapter', () => {
  it('forwards both SendMessage arguments positionally', async () => {
    const b = makeBindings()
    await b.SendMessage('claude', 'the real message')
    expect(h.SendMessage).toHaveBeenCalledWith('claude', 'the real message')
    await b.SendMessage('codex', 'second round')
    expect(h.SendMessage).toHaveBeenCalledWith('codex', 'second round')
  })

  it('forwards all six StartSession arguments', async () => {
    const b = makeBindings()
    await b.StartSession('codex', 'prompt', 'resume-id', 'rec', 'task', 'untrusted')
    expect(h.StartSession).toHaveBeenCalledWith('codex', 'prompt', 'resume-id', 'rec', 'task', 'untrusted')
  })
})
