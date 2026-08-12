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
  mutations: Record<string, string> // mutation_id -> task_ref（RegisterMutation 登記記錄）
}

function runKey(planId: string, taskId: string, kind: string): string {
  return `${planId}/${taskId}/${kind}`
}

// evidence store（Task 22；review fix：pendingKey FIFO 配對在真實可達的併發
// 下會錯位——同一 task 的 expected_red／negative_control 兩顆按鈕互不
// disable 時，先後點兩顆會讓兩筆 started 依序抵達、第二筆覆寫 pendingKey，
// 先完成的 finished 就被誤寫進另一格。修法：started/finished payload 都帶
// plan_id/task_id/kind（app.go 的 EmitWorkspace 呼叫已補齊，finished 原本
// 只帶 evidence_id／result／error），事件直接用這三個欄位定位格子，不再猜
// 「最近一筆 started」）：
//
// 1. RunEvidence 呼叫本身：元件直接 await，成功後另呼叫 EvidenceGet(evidence_id)
//    取得完整 record（含 result），是狀態的權威來源——見 setResult／setError，
//    元件對 await 的成敗自行呼叫。
// 2. workspace lane 的 evidence_run started/finished 事件（經 gateRouting 依
//    kind 分流到這裡，不進 session／gate store）：提供「執行中」的即時進度
//    顯示。最終 passed/failed/error 仍以來源 1 的直接 EvidenceGet 為準，兩者
//    寫入的值本就來自同一份 journal record，不會互相矛盾——事件路徑純粹是
//    過渡態的進度指示。
export const useEvidence = defineStore('evidence', {
  state: (): State => ({ runs: {}, mutations: {} }),

  getters: {
    runOf: (s) => (planId: string, taskId: string, kind: string): EvidenceRunState | undefined =>
      s.runs[runKey(planId, taskId, kind)],
    // taskHasRunInFlight：per-task 互斥用——RunEvidence 是同步長呼叫，同一
    // task 的任一 kind 執行中時，兩顆 run 按鈕都該 disabled（見 TcaWorkspace
    // 的 isTaskBusy），不只 disable 自己那顆。
    taskHasRunInFlight: (s) => (planId: string, taskId: string): boolean =>
      Object.entries(s.runs).some(([key, run]) =>
        key.startsWith(`${planId}/${taskId}/`) && run.status === 'running'),
  },

  actions: {
    applyEvidenceEvent(env: Envelope) {
      const p = (env.payload as Record<string, unknown>) ?? {}
      const phase = typeof p.phase === 'string' ? p.phase : undefined
      const planId = typeof p.plan_id === 'string' ? p.plan_id : ''
      const taskId = typeof p.task_id === 'string' ? p.task_id : ''
      const kind = typeof p.kind === 'string' ? p.kind : ''
      if (!planId || !taskId || !kind) return // 缺任一識別欄位就無法定位格子，不猜、不落地

      if (phase === 'started') {
        this.runs[runKey(planId, taskId, kind)] = {
          status: 'running', kind, testCommit: typeof p.test_commit === 'string' ? p.test_commit : undefined,
        }
      } else if (phase === 'finished') {
        const errMsg = typeof p.error === 'string' ? p.error : undefined
        const evidenceId = typeof p.evidence_id === 'string' ? p.evidence_id : undefined
        const result = typeof p.result === 'string' ? p.result : undefined
        this.runs[runKey(planId, taskId, kind)] = {
          status: errMsg ? 'error' : (result === 'passed' ? 'passed' : result === 'failed' ? 'failed' : 'error'),
          kind, evidenceId, error: errMsg,
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
