import { defineStore } from 'pinia'
import type { Envelope } from '../types'

export interface AssistDraft { text: string; thinking: string }
export interface PlanFileMeta { name: string; path: string }

interface ErrorEntry { kind: string; message: string }

interface State {
  files: PlanFileMeta[]
  currentPath: string
  currentContent: string
  currentDigest: string
  drafts: Record<string, AssistDraft>
  errorEntries: ErrorEntry[]
}

// plan store（Task 13）：PlanWorkspace 消費——檔案清單、目前檔（rel/content/
// digest）、AI 草稿（purpose=plan_draft 事件，by corr_id 累積，沿 assist.ts
// 慣例）、驗證（PlanWrite 樂觀鎖／commit staleness）與送核（SubmitPlanForApproval，
// 含其內部 GateList fail closed）錯誤——後端錯誤原樣 push，不吞（§9）。不碰
// session store：plan_draft 事件經 gateRouting 直接分流到這裡，不會污染 Chat／
// totals（本 task 硬性驗收缺口）。
export const usePlan = defineStore('plan', {
  state: (): State => ({
    files: [],
    currentPath: '',
    currentContent: '',
    currentDigest: '',
    drafts: {},
    errorEntries: [],
  }),

  getters: {
    draftOf: (s) => (correlationId: string): AssistDraft => s.drafts[correlationId] ?? { text: '', thinking: '' },
    // errors：對外仍是純 string[]（沿用既有呼叫端／測試慣例），kind 只是內部
    // 用來做「同類 transient error」的 scoped clear（A2：error lifecycle 三
    // 原則——見下方 pushError／clearErrors）。
    errors: (s): string[] => s.errorEntries.map(e => e.message),
  },

  actions: {
    applyAssistEvent(env: Envelope) {
      const id = env.correlation_id
      if (!id) return
      const d = this.drafts[id] ?? (this.drafts[id] = { text: '', thinking: '' })
      d.text += env.text ?? ''
      d.thinking += env.thinking ?? ''
    },
    setFiles(files: PlanFileMeta[]) { this.files = files },
    setCurrentFile(path: string, content: string, digest: string) {
      this.currentPath = path
      this.currentContent = content
      this.currentDigest = digest
    },
    // pushError／clearErrors 的 kind 參數（A2）：kind 讓呼叫端（PlanWorkspace 的
    // 各個寫入操作——save／submit／previewCommit／confirmCommit／createNewFile）
    // 能只清自己那一類的既有錯誤，不誤清其他操作留下的錯誤訊息。不帶 kind 時
    // 沿用舊行為（單一 'generic' 分類、clearErrors() 清全部）——維持既有呼叫端
    // 與測試相容。
    pushError(msg: string, kind = 'generic') { this.errorEntries.push({ kind, message: msg }) },
    clearErrors(kind?: string) {
      this.errorEntries = kind === undefined ? [] : this.errorEntries.filter(e => e.kind !== kind)
    },
  },
})
