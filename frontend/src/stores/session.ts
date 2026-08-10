import { defineStore } from 'pinia'
import type { Bindings, ChatItem, Envelope, TimelineItem } from '../types'

const NOISE_KINDS = new Set(['system_other', 'unknown'])

interface State {
  provider: string
  taskLabel: string
  approvalPolicy: string
  recordCase: string
  resume: Record<string, string> // per-provider 記憶
  sessionId: string
  taskId: string
  state: string
  chat: ChatItem[]
  timeline: TimelineItem[]
  totals: { cost: number; input: number; output: number }
  usageSemantics: string
  busy: boolean
  active: boolean // session 已啟動（submit 路由 StartSession/SendMessage）
  bindings: Bindings | null
  noiseGroup: number // 目前 noise run 的 group id；-1 = 無進行中 run
  nextGroup: number
}

export const useSession = defineStore('session', {
  state: (): State => ({
    provider: 'claude',
    taskLabel: '',
    approvalPolicy: 'untrusted',
    recordCase: '',
    resume: {},
    sessionId: '',
    taskId: '',
    state: 'idle',
    chat: [],
    timeline: [],
    totals: { cost: 0, input: 0, output: 0 },
    usageSemantics: '',
    busy: false,
    active: false,
    bindings: null,
    noiseGroup: -1,
    nextGroup: 0,
  }),

  getters: {
    costDisplay: (s): string => (s.totals.cost > 0 ? '$' + s.totals.cost.toFixed(4) : '—'),
    // per-provider resume 記憶：切 provider 顯示各自的值（寫入走 setResumeInput）
    resumeInput: (s): string => s.resume[s.provider] ?? '',
  },

  actions: {
    setBindings(b: Bindings) {
      this.bindings = b
    },

    // apply 是 host envelope 的唯一入口（EventsOn('workbench:event', s.apply)）。
    apply(env: Envelope) {
      // usage snapshot 覆寫（host 端 totals 已是累計值，不在前端相加）
      if (env.usage) {
        this.totals.input = env.usage.input_tokens
        this.totals.output = env.usage.output_tokens
      }
      if (env.usage_semantics) this.usageSemantics = env.usage_semantics

      switch (env.kind) {
        case 'init':
          if (env.session_id) this.sessionId = env.session_id
          if (env.task_id) this.taskId = env.task_id
          break
        case 'state_change':
          if (env.state) this.state = env.state
          break
        case 'result':
          this.totals.cost += env.cost_usd ?? 0
          this.busy = false
          break
      }

      // chat 路由
      if (env.kind === 'delta') {
        const last = this.chat.at(-1)
        if (last && last.role === 'assistant' && last.streaming) {
          last.text += env.text ?? ''
          last.thinking += env.thinking ?? ''
        } else {
          this.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: true })
        }
        return // delta 不進 timeline
      }
      if (env.kind === 'message' && env.role === 'user') {
        this.chat.push({ role: 'user', text: env.text ?? '', thinking: '', streaming: false })
      } else if (env.kind === 'message' && env.role === 'assistant') {
        const last = this.chat.at(-1)
        if (last && last.role === 'assistant' && last.streaming) {
          last.text = env.text ?? last.text
          if (env.thinking) last.thinking = env.thinking
          last.streaming = false
        } else {
          this.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: false })
        }
      }
      // role==='tool' 的 message 只進 timeline（不進 chat）

      // timeline：連續 system_other/unknown 併 group
      if (NOISE_KINDS.has(env.kind)) {
        if (this.noiseGroup === -1) this.noiseGroup = this.nextGroup++
        this.timeline.push({ env, group: this.noiseGroup })
      } else {
        this.noiseGroup = -1
        this.timeline.push({ env })
      }
    },

    async submit(text: string) {
      if (this.busy) return
      if (!this.bindings) {
        this.pushError('bindings not ready')
        return
      }
      this.busy = true
      try {
        if (!this.active) {
          await this.bindings.StartSession(this.provider, text, this.resume[this.provider] ?? '',
            this.recordCase, this.taskLabel, this.approvalPolicy)
          this.active = true
        } else {
          await this.bindings.SendMessage(text)
        }
        // 不本地新增 user 氣泡：等 host 的 canonical user envelope
      } catch (e) {
        this.pushError(String((e as Error)?.message ?? e))
      }
    },

    setResumeInput(v: string) {
      this.resume[this.provider] = v
    },

    note(msg: string) {
      this.timeline.push({
        env: { event_id: 'ui-note-' + this.timeline.length, ts: new Date().toISOString(),
          provider: this.provider, kind: 'note', text: msg },
      })
      this.noiseGroup = -1
    },

    // session:done（exit/stderr/recorderError）入 timeline；session 已結束 →
    // busy 解鎖、active 清除（下一次 submit 走 StartSession）。
    applyDone(d: Record<string, unknown>) {
      this.timeline.push({
        env: { event_id: 'ui-done-' + this.timeline.length, ts: new Date().toISOString(),
          provider: String(d?.provider ?? this.provider), kind: 'session_done',
          text: JSON.stringify(d) },
      })
      this.noiseGroup = -1
      this.busy = false
      this.active = false
    },

    pushError(msg: string) {
      this.timeline.push({
        env: { event_id: 'ui-err-' + this.timeline.length, ts: new Date().toISOString(),
          provider: this.provider, kind: 'stream_error', error: msg },
      })
      this.noiseGroup = -1
      this.busy = false
    },

    reset() {
      const b = this.bindings
      const provider = this.provider
      const taskLabel = this.taskLabel
      const approvalPolicy = this.approvalPolicy
      const recordCase = this.recordCase
      const resume = this.resume
      this.$reset()
      this.bindings = b
      this.provider = provider
      this.taskLabel = taskLabel
      this.approvalPolicy = approvalPolicy
      this.recordCase = recordCase
      this.resume = resume
    },
  },
})
