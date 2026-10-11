import { defineStore } from 'pinia'

// B4 workspace 確認狀態（spec §5.6）：App 頂部列警示、composer、Spec／Plan Assist 共用的
// 單一來源。沿用 escalation store 的慣例——不在 store 內 import wailsjs，真正的 binding 由
// App.vue 注入（測試因此不必 mock 整個 App module）。
//
// fail closed：只有「已取得 status，且不是 required && !confirmed」才放行。尚未取得、查詢失敗、
// 回覆形狀不對，一律視為封鎖。

export interface WorkspaceStatus {
  required: boolean
  confirmed: boolean
  workspace: string
  source: string
  reasons: string[]
}

export type BlockKind = 'ok' | 'loading' | 'failed' | 'pending'

interface State {
  status: WorkspaceStatus | null // null＝尚未取得或上一次查詢失敗
  queryError: string // 查詢失敗的原文；空字串＝沒有失敗
  seq: number // request sequencing：回覆到達時序號已變＝舊回應，整筆丟棄
  confirming: boolean // 確認進行中（擋重複點擊）
  confirmError: string // 最近一次確認失敗的原文
}

// parseStatus：驗證完整 DTO 形狀，不補空值、不用 String() 把錯誤資料轉成合法資料。
// Go 端保證 required／confirmed 是 boolean、workspace／source 是 string、reasons 是**非 nil** 的
// string 陣列（見 WorkspaceConfirmationInfo.info()）——所以任何缺少、null、型別不符的欄位都是
// 壞回覆，一律丟錯，由 refresh 轉成封鎖（failed）。缺 required 若被當成 false、reasons 缺少
// 若被補成 []，一個壞掉的回覆就會解除封鎖。
export function parseStatus(raw: unknown): WorkspaceStatus {
  const r = raw as Partial<Record<keyof WorkspaceStatus, unknown>> | null | undefined
  if (
    !r || typeof r !== 'object' ||
    typeof r.required !== 'boolean' || typeof r.confirmed !== 'boolean' ||
    typeof r.workspace !== 'string' || typeof r.source !== 'string' ||
    !Array.isArray(r.reasons) || !r.reasons.every((x): x is string => typeof x === 'string')
  ) {
    throw new Error('WorkspaceConfirmationStatus: malformed response')
  }
  return {
    required: r.required,
    confirmed: r.confirmed,
    workspace: r.workspace,
    source: r.source,
    reasons: [...r.reasons],
  }
}

export const useWorkspaceConfirmation = defineStore('workspaceConfirmation', {
  state: (): State => ({ status: null, queryError: '', seq: 0, confirming: false, confirmError: '' }),

  getters: {
    blockKind: (s): BlockKind => {
      if (s.status === null) return s.queryError === '' ? 'loading' : 'failed'
      return s.status.required && !s.status.confirmed ? 'pending' : 'ok'
    },
    blocked(): boolean { return this.blockKind !== 'ok' },
  },

  actions: {
    async refresh(fetchStatus: () => Promise<unknown>) {
      const token = ++this.seq
      try {
        const st = parseStatus(await fetchStatus())
        if (token !== this.seq) return // 較新的 refresh 已發出：這筆是舊回應，丟棄
        this.status = st
        this.queryError = ''
      } catch (e) {
        if (token !== this.seq) return // 舊呼叫的失敗不得覆蓋新狀態
        this.status = null // 查詢失敗＝封鎖，不沿用舊的 confirmed
        this.queryError = String((e as Error)?.message ?? e)
      }
    },

    // confirm：把後端回報的 workspace **原始字串**原樣傳回（不縮短、不改寫成 ~、不 trim）。
    // 成功後一律重新查詢 status——前端不自己把 confirmed 設成 true，放行與否只看後端回覆。
    async confirm(confirmFn: (expectedPath: string) => Promise<void>, refreshFn: () => Promise<void>): Promise<boolean> {
      if (this.confirming || this.status === null) return false
      this.confirming = true
      this.confirmError = ''
      try {
        try {
          await confirmFn(this.status.workspace)
        } catch (e) {
          this.confirmError = String((e as Error)?.message ?? e)
          return false
        }
        await refreshFn()
        return true
      } finally {
        this.confirming = false
      }
    },
  },
})
