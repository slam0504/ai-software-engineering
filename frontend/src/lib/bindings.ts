import {
  StartSession, SendMessage,
  RegisterMutation, RunEvidence, EvidenceGet, SubmitTestContract, ValidateTestCommit, EvidenceCommitCandidates,
} from '../../wailsjs/go/main/App'
import type { Bindings, EvidenceBindings } from '../types'

// production bindings adapter（M1.5 第三輪 review P1-1：SendMessage 必須
// 逐參數轉發——單參數 adapter 會把 provider 名當成訊息內容送出）。Task 22 加入
// TCA workspace 六個綁定，同一教訓套用：RunEvidence／SubmitTestContract 這類
// 多參數呼叫，每個都逐參數轉發、順序與 Go 簽章一致（見 bindings.test.ts）。回傳
// 型別交集 Bindings & EvidenceBindings：session store 只取用 Bindings 那兩個
// 欄位，App.vue 把 EvidenceBindings 那六個個別當 prop 傳給 TcaWorkspace 等元件。
export function makeBindings(): Bindings & EvidenceBindings {
  return {
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (p, t) => SendMessage(p, t),
    RegisterMutation: (taskRef, patch) => RegisterMutation(taskRef, patch),
    RunEvidence: (planID, taskID, testCommit, kind, mutationID) =>
      RunEvidence(planID, taskID, testCommit, kind, mutationID),
    EvidenceGet: (evidenceID) => EvidenceGet(evidenceID),
    SubmitTestContract: (planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID) =>
      SubmitTestContract(planID, taskID, testCommit, expectedRedID, negativeControlID, mutationID),
    ValidateTestCommit: (planID, taskID, testCommit) => ValidateTestCommit(planID, taskID, testCommit),
    EvidenceCommitCandidates: (planID) => EvidenceCommitCandidates(planID),
  }
}
