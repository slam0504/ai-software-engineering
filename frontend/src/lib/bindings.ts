import {
  StartSession, SendMessage,
  RegisterMutation, RunEvidence, EvidenceGet, SubmitTestContract, ValidateTestCommit, EvidenceCommitCandidates,
  EscalationList, EscalationCreate, EscalationAck, EscalationResolve,
} from '../../wailsjs/go/main/App'
import type { Bindings, EvidenceBindings, EscalationBindings } from '../types'

// production bindings adapter（M1.5 第三輪 review P1-1：SendMessage 必須
// 逐參數轉發——單參數 adapter 會把 provider 名當成訊息內容送出）。Task 22 加入
// TCA workspace 六個綁定、Task 25 加入 escalation 收件匣四個綁定，同一教訓
// 套用：多參數呼叫每個都逐參數轉發、順序與 Go 簽章一致（見 bindings.test.ts）。
// 回傳型別交集 Bindings & EvidenceBindings & EscalationBindings：session
// store 只取用 Bindings 那兩個欄位，App.vue 把其餘綁定個別當 prop 傳給
// TcaWorkspace／EscalationInbox 等元件。
export function makeBindings(): Bindings & EvidenceBindings & EscalationBindings {
  return {
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (p, t) => SendMessage(p, t),
    RegisterMutation: (taskRef, patch) => RegisterMutation(taskRef, patch),
    RunEvidence: (expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID) =>
      RunEvidence(expectedGate2ApprovalID, planID, taskID, testCommit, kind, mutationID),
    EvidenceGet: (evidenceID) => EvidenceGet(evidenceID),
    SubmitTestContract: (planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID) =>
      SubmitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID),
    ValidateTestCommit: (planID, taskID, testCommit) => ValidateTestCommit(planID, taskID, testCommit),
    EvidenceCommitCandidates: (planID) => EvidenceCommitCandidates(planID),
    EscalationList: () => EscalationList(),
    EscalationCreate: (sourceRef, blockScope, summary) => EscalationCreate(sourceRef, blockScope, summary),
    EscalationAck: (id) => EscalationAck(id),
    EscalationResolve: (id, resolution, reason) => EscalationResolve(id, resolution, reason),
  }
}
