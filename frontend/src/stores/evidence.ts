import { defineStore } from 'pinia'
import type { Envelope } from '../types'

export interface EvidenceRunState {
  status: 'running' | 'passed' | 'failed' | 'error'
  kind: string
  testCommit?: string
  evidenceId?: string
  error?: string
}

interface State {
  runs: Record<string, EvidenceRunState> // key: runKey(planId, taskId, kind)
  pendingKey: string | null // 最近一筆 started 事件尚未收到 finished 配對的 key
  mutations: Record<string, string> // mutation_id -> task_ref（RegisterMutation 登記記錄）
}

function runKey(planId: string, taskId: string, kind: string): string {
  return `${planId}/${taskId}/${kind}`
}

// evidence store（Task 22）：TcaWorkspace 消費——per (plan,task,kind) 的
// evidence run 狀態，資料來源有二：
//
// 1. RunEvidence 呼叫本身：元件直接 await，成功後另呼叫 EvidenceGet(evidence_id)
//    取得完整 record（含 result），是狀態的權威來源——見 setResult／setError，
//    元件對 await 的成敗自行呼叫。
// 2. workspace lane 的 evidence_run started/finished 事件（經 gateRouting 依
//    kind 分流到這裡，不進 session／gate store）：finished payload 只帶
//    evidence_id／result／error（RunEvidence 的 EmitWorkspace 設計本就
//    additive、不重複帶 plan_id/task_id/kind），靠 pendingKey 記住「最近一筆
//    started 尚未配對」的 key 來歸位——M3a 假設單一 active plan／UI 按鈕執行中
//    disabled，同一時間至多一筆 RunEvidence 在飛行中，FIFO 配對在此前提下安全
//    （鏡射 app.go worktreePlanDoc 的「單一 active plan」既定假設）。這個事件
//    路徑主要提供「執行中」的即時進度顯示；最終 passed/failed/error 以來源 1
//    的直接 EvidenceGet 為準，兩者寫入的值本就來自同一份 journal record，不會
//    互相矛盾。
export const useEvidence = defineStore('evidence', {
  state: (): State => ({ runs: {}, pendingKey: null, mutations: {} }),

  getters: {
    runOf: (s) => (planId: string, taskId: string, kind: string): EvidenceRunState | undefined =>
      s.runs[runKey(planId, taskId, kind)],
  },

  actions: {
    applyEvidenceEvent(env: Envelope) {
      const p = (env.payload as Record<string, unknown>) ?? {}
      const phase = typeof p.phase === 'string' ? p.phase : undefined
      if (phase === 'started') {
        const planId = typeof p.plan_id === 'string' ? p.plan_id : ''
        const taskId = typeof p.task_id === 'string' ? p.task_id : ''
        const kind = typeof p.kind === 'string' ? p.kind : ''
        const key = runKey(planId, taskId, kind)
        this.runs[key] = { status: 'running', kind, testCommit: typeof p.test_commit === 'string' ? p.test_commit : undefined }
        this.pendingKey = key
      } else if (phase === 'finished') {
        if (!this.pendingKey) return
        const entry = this.runs[this.pendingKey]
        this.pendingKey = null
        if (!entry) return
        const errMsg = typeof p.error === 'string' ? p.error : undefined
        const evidenceId = typeof p.evidence_id === 'string' ? p.evidence_id : undefined
        if (evidenceId) entry.evidenceId = evidenceId
        if (errMsg) {
          entry.status = 'error'
          entry.error = errMsg
        } else {
          const result = typeof p.result === 'string' ? p.result : undefined
          entry.status = result === 'passed' ? 'passed' : result === 'failed' ? 'failed' : 'error'
        }
      }
    },

    // setRunning：元件在呼叫 RunEvidence 前先行落地，讓按鈕立即進入
    // 「執行中」狀態，不必等 started 事件送達（IPC 順序不保證先於本地呼叫）。
    setRunning(planId: string, taskId: string, kind: string) {
      this.runs[runKey(planId, taskId, kind)] = { status: 'running', kind }
    },
    // setResult：RunEvidence 呼叫成功後，元件另呼叫 EvidenceGet 取得的權威結果。
    setResult(planId: string, taskId: string, kind: string, evidenceId: string, result: string) {
      this.runs[runKey(planId, taskId, kind)] = {
        status: result === 'passed' ? 'passed' : result === 'failed' ? 'failed' : 'error',
        kind, evidenceId,
      }
    },
    // setError：RunEvidence（或後續 EvidenceGet）呼叫本身失敗——錯誤原文落地，
    // 不吞（§9）。
    setError(planId: string, taskId: string, kind: string, message: string) {
      this.runs[runKey(planId, taskId, kind)] = { status: 'error', kind, error: message }
    },
    clearRun(planId: string, taskId: string, kind: string) {
      delete this.runs[runKey(planId, taskId, kind)]
    },
    registerMutation(mutationId: string, taskRef: string) {
      this.mutations[mutationId] = taskRef
    },
  },
})
