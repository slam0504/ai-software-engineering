import { defineStore } from 'pinia'
import type { Envelope } from '../types'

export interface AssistDraft { text: string; thinking: string }

interface State {
  drafts: Record<string, AssistDraft>
}

// assist store：session-scope + purpose=spec_assist 事件的草稿累積，
// 供 Task 15 的 spec workspace draft area 消費。不碰 session store。
export const useAssist = defineStore('assist', {
  state: (): State => ({ drafts: {} }),

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
  },
})
