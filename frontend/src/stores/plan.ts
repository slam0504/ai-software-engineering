import { defineStore } from 'pinia'
import type { Envelope } from '../types'

export interface AssistDraft { text: string; thinking: string }
export interface PlanFileMeta { name: string; path: string }

interface State {
  files: PlanFileMeta[]
  currentPath: string
  currentContent: string
  currentDigest: string
  drafts: Record<string, AssistDraft>
  errors: string[]
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
    errors: [],
  }),

  getters: {
    draftOf: (s) => (correlationId: string): AssistDraft => s.drafts[correlationId] ?? { text: '', thinking: '' },
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
    pushError(msg: string) { this.errors.push(msg) },
    clearErrors() { this.errors = [] },
  },
})
