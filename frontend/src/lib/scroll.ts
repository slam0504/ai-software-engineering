// follow-tail 判定（normative）：距底 slack px 內視為在底部、維持自動跟隨。
export function isAtBottom(scrollTop: number, scrollHeight: number,
  clientHeight: number, slack = 24): boolean {
  return scrollTop + clientHeight >= scrollHeight - slack
}
