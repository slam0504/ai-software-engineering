// escalationBadge：App.vue 右側 side-nav「Escalation」tab 的徽章邏輯，抽成
// 純函式（同 gateRouting.ts／persist.ts 等既有 lib/ 慣例）——App.vue 本身沒有
// 測試 harness，抽出來才能直接單元測試這條 §3.8 紅線行為：EscalationList
// 失敗（unavailable 非空）時必須顯示警示，絕不能因為 entries 是舊資料／空而
// 讓 badge 視覺上等同「沒有項目」。unavailable 優先於未 resolved 計數。
export type EscalationBadge = { kind: 'warn' } | { kind: 'count'; n: number } | null

export function escalationBadge(unavailable: string, unresolvedCount: number): EscalationBadge {
  if (unavailable) return { kind: 'warn' }
  if (unresolvedCount > 0) return { kind: 'count', n: unresolvedCount }
  return null
}
