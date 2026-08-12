import { defineStore } from 'pinia'
import type { Envelope, GateEntry } from '../types'

interface State {
  entries: Record<string, GateEntry>
}

function approvalIdOf(env: Envelope): string | undefined {
  const p = env.payload as Record<string, unknown> | undefined
  const id = p?.approval_id
  return typeof id === 'string' ? id : undefined
}

// gate store：workspace-scope 事件（gate_request/approval_decision/binding_stale）的
// projection，供 Task 14 的 gate console 消費。不碰 session store。
export const useGate = defineStore('gate', {
  state: (): State => ({ entries: {} }),

  getters: {
    list: (s): GateEntry[] => Object.values(s.entries),
  },

  actions: {
    applyGateEvent(env: Envelope) {
      const id = approvalIdOf(env)
      if (!id) return
      const p = (env.payload as Record<string, unknown>) ?? {}

      switch (env.kind) {
        case 'gate_request':
          this.entries[id] = {
            approval_id: id,
            state: 'pending',
            gate: typeof p.gate === 'string' ? p.gate : undefined,
            bindings: env.bindings,
            base_commit: typeof p.base_commit === 'string' ? p.base_commit : undefined,
          }
          break
        case 'approval_decision': {
          const existing = this.entries[id]
          const decision = typeof p.decision === 'string' ? p.decision : undefined
          this.entries[id] = {
            ...(existing ?? { approval_id: id }),
            state: decision === 'approved' ? 'active' : (decision ?? 'decided'),
          }
          break
        }
        case 'binding_stale': {
          const existing = this.entries[id]
          this.entries[id] = { ...(existing ?? { approval_id: id }), state: 'stale' }
          break
        }
      }
    },
  },
})
