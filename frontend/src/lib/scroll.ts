// follow-tail 判定（normative）：距底 slack px 內視為在底部、維持自動跟隨。
export function isAtBottom(scrollTop: number, scrollHeight: number,
  clientHeight: number, slack = 24): boolean {
  return scrollTop + clientHeight >= scrollHeight - slack
}

// 捲到頂判定（§3.8 向上分頁的觸發條件）：鏡射 isAtBottom 同一個 slack 語意，
// 距頂 slack px 內視為已到頂。
export function isAtTop(scrollTop: number, slack = 24): boolean {
  return scrollTop <= slack
}
