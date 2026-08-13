// staleNav（M3a.1 Task 11，spec §3.5）：STALE 重核引導的共用純函式——GateConsole
// 已經有結構化的 gate/subject（GateEntry 欄位），可直接用 resolveResubmitTarget；
// EscalationInbox 只有 condition_key 字串，必須先經 parseStaleTarget 切出
// gate/subject 才能餵給 resolveResubmitTarget。兩處導航必須共用同一份驗證，
// 避免「顯示按鈕」跟「按下去是否真能導航」判斷分岔。

// parseStaleTarget：condition_key 格式 "stale:<known-gate>:<subject remainder>"。
// 以已知 gate 名（gate1｜gate2｜test_contract_approval）前綴匹配切分，不做
// 全字串 split——subject 自身可能含 ":"（例如 gate2 的 "plan:P1"），全字串
// split 會切錯。未知 gate／空 subject／格式不符（不含 "stale:" 前綴、缺冒號
// 分段）一律回 null——呼叫端 fail loud，不臆測。
const KNOWN_GATES = ['gate1', 'gate2', 'test_contract_approval'] as const

export function parseStaleTarget(conditionKey: string): { gate: string; subject: string } | null {
  for (const gate of KNOWN_GATES) {
    const prefix = `stale:${gate}:`
    if (conditionKey.startsWith(prefix)) {
      const subject = conditionKey.slice(prefix.length)
      return subject ? { gate, subject } : null
    }
  }
  return null
}

// ResubmitTarget：二次解析後的具體導航目標——gate2 的 subject 必須是
// "plan:<id>"（非空 id），test_contract_approval 的 subject 必須是
// "task:<plan>/<task>"（plan／task 皆非空）。gate1 沒有 subject 內容要解析，
// 直接對應規格工作區。
export type ResubmitTarget =
  | { kind: 'gate1' }
  | { kind: 'gate2'; planId: string }
  | { kind: 'tca'; planId: string; taskId: string }

// resolveResubmitTarget：把 (gate, subject) 解析成具體導航目標；gate 未知或
// subject 形狀不符（缺字首、id/plan/task 任一段為空）一律回 null——呼叫端顯示
// 資料完整性錯誤，禁止導航，不得靜默隱藏或猜一個目標出來。
export function resolveResubmitTarget(gate: string, subject: string): ResubmitTarget | null {
  if (gate === 'gate1') return { kind: 'gate1' }
  if (gate === 'gate2') {
    if (!subject.startsWith('plan:')) return null
    const planId = subject.slice('plan:'.length)
    return planId ? { kind: 'gate2', planId } : null
  }
  if (gate === 'test_contract_approval') {
    if (!subject.startsWith('task:')) return null
    const rest = subject.slice('task:'.length)
    const slash = rest.indexOf('/')
    if (slash <= 0 || slash === rest.length - 1) return null
    return { kind: 'tca', planId: rest.slice(0, slash), taskId: rest.slice(slash + 1) }
  }
  return null
}
