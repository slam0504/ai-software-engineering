import { StartSession, SendMessage } from '../../wailsjs/go/main/App'
import type { Bindings } from '../types'

// production bindings adapter（M1.5 第三輪 review P1-1：SendMessage 必須
// 逐參數轉發——單參數 adapter 會把 provider 名當成訊息內容送出）。
export function makeBindings(): Bindings {
  return {
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (p, t) => SendMessage(p, t),
  }
}
