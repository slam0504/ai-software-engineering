import { defineStore } from 'pinia'
import type { Bindings, ChatItem, Envelope, TimelineItem } from '../types'

const NOISE_KINDS = new Set(['system_other', 'unknown'])
export const PROVIDERS = ['claude', 'codex'] as const
export type ProviderKey = (typeof PROVIDERS)[number]

// SessionView：單一 provider 的對話視圖（M1.5 plan D3）。
export interface SessionView {
  sessionId: string
  taskId: string
  state: string
  chat: ChatItem[]
  timeline: TimelineItem[]
  totals: { cost: number; input: number; output: number }
  usageSemantics: string
  busy: boolean
  active: boolean
  unread: number // 每完成 turn +1（result 抵達且非 active view）；切入歸零
  noiseGroup: number
  nextGroup: number
  // 輸入欄位（per view；M1.5 第一輪 review P1-7）
  taskLabel: string
  recordCase: string
  resume: string
}

function newView(): SessionView {
  return {
    sessionId: '', taskId: '', state: 'idle',
    chat: [], timeline: [],
    totals: { cost: 0, input: 0, output: 0 },
    usageSemantics: '', busy: false, active: false, unread: 0,
    noiseGroup: -1, nextGroup: 0,
    taskLabel: '', recordCase: '', resume: '',
  }
}

interface State {
  activeProvider: ProviderKey
  approvalPolicy: string
  views: Record<ProviderKey, SessionView>
  bindings: Bindings | null
  restoring: boolean // 重放恢復中：不計 unread（M1.5 D6）
}

function providerOf(env: { provider?: string }): ProviderKey {
  return env.provider === 'codex' ? 'codex' : 'claude'
}

export const useSession = defineStore('session', {
  state: (): State => ({
    activeProvider: 'claude',
    approvalPolicy: 'untrusted',
    views: { claude: newView(), codex: newView() },
    bindings: null,
    restoring: false,
  }),

  getters: {
    view: (s): SessionView => s.views[s.activeProvider],
    // 轉發 getter：元件沿 M1 讀法（一律 active view）
    provider: (s): string => s.activeProvider,
    chat(): ChatItem[] { return this.view.chat },
    timeline(): TimelineItem[] { return this.view.timeline },
    totals(): { cost: number; input: number; output: number } { return this.view.totals },
    state(): string { return this.view.state },
    sessionId(): string { return this.view.sessionId },
    taskId(): string { return this.view.taskId },
    busy(): boolean { return this.view.busy },
    active(): boolean { return this.view.active },
    usageSemantics(): string { return this.view.usageSemantics },
    taskLabel(): string { return this.view.taskLabel },
    recordCase(): string { return this.view.recordCase },
    resumeInput(): string { return this.view.resume },
    costDisplay(): string {
      return this.view.totals.cost > 0 ? '$' + this.view.totals.cost.toFixed(4) : '—'
    },
    unreadOf: (s) => (p: ProviderKey): number => s.views[p].unread,
    awaitingOf: (s) => (p: ProviderKey): boolean => s.views[p].state === 'awaiting_approval',
  },

  actions: {
    setBindings(b: Bindings) {
      this.bindings = b
    },

    setActiveProvider(p: ProviderKey) {
      this.activeProvider = p
      this.views[p].unread = 0 // 切入歸零
    },

    setTaskLabel(v: string) { this.view.taskLabel = v },
    setRecordCase(v: string) { this.view.recordCase = v },
    setResumeInput(v: string) { this.view.resume = v },
    setApprovalPolicy(v: string) { this.approvalPolicy = v },

    // apply 是 host envelope 的唯一入口；依 env.provider 路由（與 activeProvider
    // 無關——背景 view 照常累積）。
    apply(env: Envelope) {
      const p = providerOf(env)
      const v = this.views[p]

      if (env.usage) {
        v.totals.input = env.usage.input_tokens
        v.totals.output = env.usage.output_tokens
      }
      if (env.usage_semantics) v.usageSemantics = env.usage_semantics

      switch (env.kind) {
        case 'init':
          if (env.session_id) v.sessionId = env.session_id
          if (env.task_id) v.taskId = env.task_id
          break
        case 'state_change':
          if (env.state) v.state = env.state
          break
        case 'result': {
          v.totals.cost += env.cost_usd ?? 0
          v.busy = false
          if (p !== this.activeProvider && !this.restoring) v.unread++ // 每完成 turn +1
          const tail = v.chat.at(-1)
          if (tail && tail.role === 'assistant' && tail.streaming) {
            if (tail.text === '' && tail.thinking === '') v.chat.pop()
            else tail.streaming = false
          }
          break
        }
      }

      if (env.kind === 'delta') {
        const last = v.chat.at(-1)
        if (last && last.role === 'assistant' && last.streaming) {
          last.text += env.text ?? ''
          last.thinking += env.thinking ?? ''
        } else {
          v.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: true })
        }
        return // delta 不進 timeline
      }
      if (env.kind === 'message' && env.role === 'user') {
        v.chat.push({ role: 'user', text: env.text ?? '', thinking: '', streaming: false })
      } else if (env.kind === 'message' && env.role === 'assistant') {
        const last = v.chat.at(-1)
        if (last && last.role === 'assistant' && last.streaming) {
          last.text = env.text ?? last.text
          if (env.thinking) last.thinking = env.thinking
          last.streaming = false
        } else {
          v.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: false })
        }
      }
      // role==='tool' 的 message 只進 timeline

      if (NOISE_KINDS.has(env.kind)) {
        if (v.noiseGroup === -1) v.noiseGroup = v.nextGroup++
        v.timeline.push({ env, group: v.noiseGroup })
      } else {
        v.noiseGroup = -1
        v.timeline.push({ env })
      }
    },

    // submit 作用於 active view；per-view busy（A busy 不擋 B）。
    async submit(text: string) {
      const p = this.activeProvider
      const v = this.views[p]
      if (v.busy) return
      if (!this.bindings) {
        this.pushError('bindings not ready')
        return
      }
      v.busy = true
      try {
        if (!v.active) {
          await this.bindings.StartSession(p, text, v.resume, v.recordCase, v.taskLabel, this.approvalPolicy)
          v.active = true
        } else {
          await this.bindings.SendMessage(p, text)
        }
        // 不本地新增 user 氣泡：等 host 的 canonical user envelope
      } catch (e) {
        this.pushError(String((e as Error)?.message ?? e), p)
      }
    },

    note(msg: string) {
      const v = this.view
      v.timeline.push({
        env: { event_id: 'ui-note-' + v.timeline.length, ts: new Date().toISOString(),
          provider: this.activeProvider, kind: 'note', text: msg },
      })
      v.noiseGroup = -1
    },

    // session:done 依 payload 的 provider 路由到對應 view。
    applyDone(d: Record<string, unknown>) {
      const p = providerOf({ provider: String(d?.provider ?? this.activeProvider) })
      const v = this.views[p]
      v.timeline.push({
        env: { event_id: 'ui-done-' + v.timeline.length, ts: new Date().toISOString(),
          provider: p, kind: 'session_done', text: JSON.stringify(d) },
      })
      v.noiseGroup = -1
      v.busy = false
      v.active = false
    },

    pushError(msg: string, provider?: ProviderKey) {
      const p = provider ?? this.activeProvider
      const v = this.views[p]
      v.timeline.push({
        env: { event_id: 'ui-err-' + v.timeline.length, ts: new Date().toISOString(),
          provider: p, kind: 'stream_error', error: msg },
      })
      v.noiseGroup = -1
      v.busy = false
    },

    // 啟動重放（M1.5 D6）：RestoreViews 的 audited envelopes 逐筆 apply 重建
    // 各 view；resume／taskLabel 預填；重放不計 unread、完畢歸零。
    restoreViews(data: Record<string, { envelopes: Envelope[] | null; resumeSessionId: string; taskId: string }>) {
      this.restoring = true
      try {
        for (const p of PROVIDERS) {
          const d = data[p]
          if (!d) continue
          for (const e of d.envelopes ?? []) this.apply(e)
          const v = this.views[p]
          v.resume = d.resumeSessionId ?? ''
          if (d.taskId) v.taskLabel = d.taskId
          v.busy = false
          v.active = false // 恢復不 spawn：下一次 submit 走 StartSession(resume)
          v.unread = 0
        }
      } finally {
        this.restoring = false
      }
    },

    // reset 只重置 active view（New 成功後呼叫）；bindings／approvalPolicy／
    // 另一個 view 不動。輸入欄位（taskLabel/recordCase）保留沿用。
    reset() {
      const v = this.view
      const keep = { taskLabel: v.taskLabel, recordCase: v.recordCase }
      this.views[this.activeProvider] = { ...newView(), ...keep }
    },
  },
})
