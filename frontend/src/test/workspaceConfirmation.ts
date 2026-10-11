import { useWorkspaceConfirmation, type WorkspaceStatus } from '../stores/workspaceConfirmation'

// 測試輔助：把 workspace 確認 store 放進指定狀態（預設＝不需確認，與 E2E／CI 的 env 注入同形）。
// 必須在 setActivePinia() 之後呼叫；不需要確認的既有元件測試在 beforeEach 呼叫它，讓 composer、
// Spec／Plan Assist 的行為維持原樣。
export function seedWorkspaceConfirmation(over: Partial<WorkspaceStatus> = {}) {
  const wsc = useWorkspaceConfirmation()
  wsc.$patch({
    status: { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [], ...over },
    queryError: '',
  })
  return wsc
}
