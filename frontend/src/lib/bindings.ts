import {
  StartSession, SendMessage, EndSession, NewSession, TerminateSession,
  CreateSession, RemoveSession, ListSessions, RecoverCodexRecording, LoadTurnsBefore,
  RegisterMutation, RunEvidence, EvidenceGet, SubmitTestContract, ValidateTestCommit, EvidenceCommitCandidates,
  EscalationList, EscalationCreate, EscalationAck, EscalationResolve,
} from '../../wailsjs/go/main/App'
import type {
  Bindings, SessionLifecycleBindings, WorkspaceSessionBindings, CodexRecordingBindings,
  TurnWindowBindings, EvidenceBindings, EscalationBindings,
} from '../types'

// production bindings adapter（M1.5 第三輪 review P1-1：SendMessage 必須
// 逐參數轉發——單參數 adapter 會把 provider 名當成訊息內容送出）。Task 22 加入
// TCA workspace 六個綁定、Task 25 加入 escalation 收件匣四個綁定，同一教訓
// 套用：多參數呼叫每個都逐參數轉發、順序與 Go 簽章一致（見 bindings.test.ts）。
// M3b Task 4 加入 CreateSession（純新增，多參數）——同一教訓再套一次；Task 22
// 加入 RemoveSession（純新增，單參數）；
// Task 13 加入無參數的 RecoverCodexRecording（§3.4.6 錄流 latch 的復原入口）；
// Task 20 加入 LoadTurnsBefore（§3.8 視窗化載入／向上分頁，三個參數）。
// **Task 26 原子切換**：StartSession／SendMessage／EndSession／NewSession／
// TerminateSession 的第一參數由 provider 改為 WSID，並加入 ListSessions
// （session 清單來源）。轉發順序與「第一參數不得是 provider 名」由
// bindings.test.ts 鎖住。
// 回傳型別交集 Bindings & WorkspaceSessionBindings & EvidenceBindings &
// EscalationBindings：session store 只取用 Bindings 那兩個欄位，App.vue 把其餘
// 綁定個別當 prop 傳給 TcaWorkspace／EscalationInbox 等元件。
export function makeBindings(): Bindings & SessionLifecycleBindings & WorkspaceSessionBindings
  & CodexRecordingBindings & TurnWindowBindings & EvidenceBindings & EscalationBindings {
  return {
    StartSession: (wsid, prompt, resume, rc, task, policy) => StartSession(wsid, prompt, resume, rc, task, policy),
    SendMessage: (wsid, text) => SendMessage(wsid, text),
    EndSession: (wsid) => EndSession(wsid),
    NewSession: (wsid) => NewSession(wsid),
    TerminateSession: (wsid) => TerminateSession(wsid),
    CreateSession: (p, taskLabel) => CreateSession(p, taskLabel),
    RemoveSession: (wsid) => RemoveSession(wsid),
    ListSessions: () => ListSessions(),
    RecoverCodexRecording: () => RecoverCodexRecording(),
    LoadTurnsBefore: (wsid, beforeEventID, n) => LoadTurnsBefore(wsid, beforeEventID, n),
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
