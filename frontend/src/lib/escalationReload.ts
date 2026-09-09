// withEscalationReload：把「可能建立／解除 blocker 的綁定」包成「完成後先重載
// 收件匣再回傳」。契約前提：reload 自行處理查詢失敗（落 unavailable）、不拋；
// finally 內的 await 若 reject 會取代原結果，這裡刻意不吞錯以免掩蓋契約違反。重載責任在 App（呼叫端元件可能在等待期間已被 v-if 卸載，
// 已卸載元件的 emit 不會送達，所以不能靠元件自己通知）。成功與失敗都重載：
// 失敗也可能已建立阻擋項（missing-binding 建立後仍回傳錯誤，app.go:6120）。
export function withEscalationReload<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  reload: () => Promise<void>,
): (...args: A) => Promise<R> {
  return async (...args: A) => {
    try {
      return await fn(...args)
    } finally {
      await reload()
    }
  }
}
