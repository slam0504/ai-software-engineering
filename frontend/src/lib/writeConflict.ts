// A1a-1：SpecWrite／PlanWrite 樂觀鎖衝突判定——後端錯誤字串含
// WRITE_CONFLICT_MARKER 時視為版本衝突（供 SpecWorkspace／PlanWorkspace 的
// [data-test=save-error][data-conflict] 呈現使用），其餘錯誤原文顯示、無此標記。
// 未匯出：唯一使用者是同檔的 isWriteConflict（匯出但零 production 呼叫端＝未接線）。
const WRITE_CONFLICT_MARKER = 'write conflict: expected_digest'

export function isWriteConflict(e: unknown): boolean {
  return String(e).includes(WRITE_CONFLICT_MARKER)
}
