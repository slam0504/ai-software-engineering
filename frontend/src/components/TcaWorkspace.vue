<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GateEntry, GateDecisionTask, CommitCandidate } from '../types'
import { useEvidence } from '../stores/evidence'
import { resolveState, evidenceResultKeys } from '../i18n/stateKeys'

const { t } = useI18n()
const evidence = useEvidence()

// TcaWorkspace（Task 22，spec §6／A4）：Stage C 全程可從 app 完成的操作面——
// active Gate 2 送核的 committed plan，per task 一列：test_commit 輸入（下拉
// 近期 commits＋手動輸入）、預檢（ValidateTestCommit）、mutation 登記
// （RegisterMutation）、兩筆 evidence run（RunEvidence，執行中 disabled＋
// 進度指示，完成顯示 result 徽章＋evidence_id）、雙 passed 後送核
// （SubmitTestContract）。所有後端呼叫皆走 props 注入（鏡射 GateConsole 的
// decide／loadDecisionContext、PlanWorkspace 的 write 慣例）——測試以 props
// 驅動，不依賴真實 Wails binding；真實 wiring 在 App.vue。
const props = defineProps<{
  entries: GateEntry[]
  loadDecisionContext: (approvalId: string) => Promise<{ tasks: GateDecisionTask[] }>
  listCandidates: (planId: string) => Promise<CommitCandidate[]>
  validateTestCommit: (planId: string, taskId: string, testCommit: string) => Promise<void>
  registerMutation: (taskRef: string, patch: string) => Promise<string>
  runEvidence: (expectedGate2ApprovalId: string, planId: string, taskId: string,
    testCommit: string, kind: string, mutationId: string) => Promise<string>
  getEvidence: (evidenceId: string) => Promise<{ result: string }>
  submitTestContract: (planId: string, taskId: string, testCommit: string,
    expectedRedId: string, negativeControlId: string, mutationId: string) => Promise<string>
  // focusTaskId（M3a.1 Task 11，spec §3.5）：STALE 重核引導從 App.vue 導航進來
  // 時要聚焦的 task 列——scroll＋高亮，同 GateConsole highlightId 的既有慣例。
  focusTaskId?: string
}>()

// active gate2：subject="plan:"+planID——同 app.go activeGate2PlanCommit／
// activeGate2ApprovalID 唯一信任的來源（committed context 閉環），這裡用
// entries 的 state==='active' 鏡射同一條件。
const activeGate2 = computed(() => props.entries.find(e => e.gate === 'gate2' && e.state === 'active'))
const planId = computed(() => {
  const subject = activeGate2.value?.subject
  if (!subject || !subject.startsWith('plan:')) return ''
  return subject.slice('plan:'.length)
})
// approvalId：目前這個「generation」的識別（Task 9，§3.3.1-2）——換版即
// approval_id 改變。傳給 evidence store 當 run key 的一段，也是所有 async
// 呼叫換版丟棄（generation guard）的快照比對基準。
const approvalId = computed(() => activeGate2.value?.approval_id ?? '')

const tasks = ref<GateDecisionTask[]>([])
const loadError = ref('')
async function loadTasks() {
  loadError.value = ''
  tasks.value = []
  if (!approvalId.value) return
  try {
    const ctx = await props.loadDecisionContext(approvalId.value)
    tasks.value = ctx.tasks ?? []
  } catch (e) {
    loadError.value = String(e)
  }
}

const candidates = ref<CommitCandidate[]>([])
const candidatesError = ref('')
async function loadCandidates() {
  candidatesError.value = ''
  candidates.value = []
  if (!planId.value) return
  try {
    candidates.value = await props.listCandidates(planId.value)
  } catch (e) {
    candidatesError.value = String(e)
  }
}

const testCommitInput = reactive<Record<string, string>>({})
const precheckBusy = reactive<Record<string, boolean>>({})
const precheckError = reactive<Record<string, string>>({})
const precheckOk = reactive<Record<string, boolean>>({})

const patchInput = reactive<Record<string, string>>({})
const registerBusy = reactive<Record<string, boolean>>({})
const registerError = reactive<Record<string, string>>({})
const mutationIds = reactive<Record<string, string>>({})

const submitBusy = reactive<Record<string, boolean>>({})
const submitResult = reactive<Record<string, string>>({})
const submitError = reactive<Record<string, string>>({})

// clearTaskState：換版（approval_id 改變）時全清空 per-task 的暫存 UI 狀態
// （Task 9 §3.3.1-2 step 1(c)）——不能只清「顯眼」的欄位：task_id 在 replan
// 前後可能沿用同一值，殘留的舊 generation 草稿／忙碌旗標／錯誤訊息會在新畫面
// 的同一格重新冒出來，不是真的乾淨換版。
function clearTaskState() {
  for (const dict of [testCommitInput, precheckBusy, precheckError, precheckOk,
    patchInput, registerBusy, registerError, mutationIds, submitBusy, submitResult, submitError]) {
    for (const k of Object.keys(dict)) delete dict[k]
  }
}

// 換版守門：approval_id 一變就清空全部暫存狀態＋更新 store 的 generation
// 標記＋重載 task context 與 commit 候選（Task 9 §3.3.1-2 step 1(c)）。原本
// loadTasks／loadCandidates 分別掛在 approval_id／planId 兩個 watch 上——但
// 同一 plan 換版（approval_id 變、planId 不變）時 planId watch 不會觸發，候
// 選清單就不會重載，所以兩者都併進這個 watcher，以 approval_id 為唯一觸發源
// （沿既有 watch planId 模式擴充）。
watch(approvalId, (id) => {
  clearTaskState()
  evidence.setCurrentGeneration(id)
  void loadTasks()
  void loadCandidates()
}, { immediate: true })

function taskRefOf(taskId: string): string {
  return `${planId.value}/${taskId}`
}

// generation guard（Task 9 §3.3.1-2 step 1(d)）：呼叫當下記錄 approvalId 快
// 照，Promise resolve/reject 時若目前的 approvalId 已不同——換版已經發生、
// clearTaskState 已經清過場——這筆晚到的回應就丟棄，不寫入任何狀態，避免舊
// generation 的結果覆蓋新畫面。
async function runPrecheck(taskId: string) {
  const expectedApprovalId = approvalId.value
  precheckError[taskId] = ''
  precheckOk[taskId] = false
  precheckBusy[taskId] = true
  try {
    await props.validateTestCommit(planId.value, taskId, testCommitInput[taskId] ?? '')
    if (approvalId.value !== expectedApprovalId) return
    precheckOk[taskId] = true
  } catch (e) {
    if (approvalId.value !== expectedApprovalId) return
    precheckError[taskId] = String(e)
  } finally {
    if (approvalId.value === expectedApprovalId) precheckBusy[taskId] = false
  }
}

async function registerMutationFor(taskId: string) {
  const expectedApprovalId = approvalId.value
  registerError[taskId] = ''
  registerBusy[taskId] = true
  try {
    const id = await props.registerMutation(taskRefOf(taskId), patchInput[taskId] ?? '')
    if (approvalId.value !== expectedApprovalId) return
    mutationIds[taskId] = id
    evidence.registerMutation(id, taskRefOf(taskId))
  } catch (e) {
    if (approvalId.value !== expectedApprovalId) return
    registerError[taskId] = String(e)
  } finally {
    if (approvalId.value === expectedApprovalId) registerBusy[taskId] = false
  }
}

// isRunBusy：這個 kind 本身正在跑（顯示「執行中…」進度指示）。
function isRunBusy(taskId: string, kind: string): boolean {
  return evidence.runOf(approvalId.value, planId.value, taskId, kind)?.status === 'running'
}
// isTaskBusy：review fix（Medium correctness finding）——RunEvidence 是同步
// 長呼叫，同一 task 的 expected_red／negative_control 原本互不 disable：先按
// red 再按 negative_control，兩筆 started 依序抵達會讓只認「最近一筆
// started」的配對邏輯錯位（見 stores/evidence.ts 的修法說明）。per-task 互斥
// 更貼近實際：同一 task 任一 kind 執行中時，兩顆 run 按鈕都 disabled，不能
// 同時觸發第二個 RunEvidence 呼叫。
function isTaskBusy(taskId: string): boolean {
  return evidence.taskHasRunInFlight(approvalId.value, planId.value, taskId)
}

// STALE_GENERATION_MESSAGE：跟 app.go 的 ErrStaleGeneration 原文
// （"evidence: gate2 approval changed since view was loaded"）比對，命中時額
// 外顯示引導文案（Task 9 §3.3.1-2 step 1(e)）——這是「client 這次呼叫當下就
// 已經換版」以外的另一種情況：後端在 workflowMu 下重讀到的權威值先變了，前端
// 畫面（entries prop）還沒收到新事件，所以上面的 generation guard 不會攔到，
// 錯誤會正常顯示，這裡只是加一句引導。
const STALE_GENERATION_MESSAGE = 'evidence: gate2 approval changed since view was loaded'
function isStaleGenerationError(message: string | undefined): boolean {
  return !!message && message.includes(STALE_GENERATION_MESSAGE)
}

// run：RunEvidence 只回傳 evidence_id，不帶 result（RunEvidence 的返回值刻意
// 精簡）——成功後另呼叫 EvidenceGet 取得權威的 result，落地進 evidence store
// （見 stores/evidence.ts 的 setResult 文件）。呼叫本身失敗（含 EmitWorkspace
// 「started」之前就拒絕的早期驗證錯誤，例如無 active gate2 或 CAS 換版
// ErrStaleGeneration）用 setError 落地，錯誤原文顯示，不吞。
//
// M3a.1 T8（§3.3.2）：第一參數傳目前畫面讀到的 active gate2 approval_id
// （approvalId，跟 loadTasks 用的是同一個計算屬性）——後端以此跟它自己在
// workflowMu 下重讀的權威值做 CAS 比對，換版即 ErrStaleGeneration。
async function run(taskId: string, kind: string) {
  const testCommit = testCommitInput[taskId] ?? ''
  const mutationId = kind === 'negative_control' ? (mutationIds[taskId] ?? '') : ''
  const expectedApprovalId = approvalId.value
  evidence.setRunning(expectedApprovalId, planId.value, taskId, kind)
  try {
    const evidenceId = await props.runEvidence(expectedApprovalId, planId.value, taskId, testCommit, kind, mutationId)
    if (approvalId.value !== expectedApprovalId) return
    const rec = await props.getEvidence(evidenceId)
    if (approvalId.value !== expectedApprovalId) return
    evidence.setResult(expectedApprovalId, planId.value, taskId, kind, evidenceId, rec.result)
  } catch (e) {
    if (approvalId.value !== expectedApprovalId) return
    evidence.setError(expectedApprovalId, planId.value, taskId, kind, String(e))
  }
}

// scrollToTask／focusTaskId watch：鏡射 GateConsole scrollToApproval 的既有
// pattern——data-test 選 DOM 節點＋optional chaining（jsdom 測試環境不一定實作
// scrollIntoView）。nextTick 等 tasks 完成渲染（loadTasks 是 async），避免目標
// task 列還沒掛上 DOM 就 query 落空。
function scrollToTask(id: string) {
  const el = document.querySelector(`[data-test="tca-row-${id}"]`) as (HTMLElement & { scrollIntoView?: (opts?: unknown) => void }) | null
  el?.scrollIntoView?.({ behavior: 'smooth', block: 'center' })
}
watch(() => props.focusTaskId, (id) => {
  if (id) void nextTick(() => scrollToTask(id))
})

function bothPassed(taskId: string): boolean {
  return evidence.runOf(approvalId.value, planId.value, taskId, 'expected_red')?.status === 'passed'
    && evidence.runOf(approvalId.value, planId.value, taskId, 'negative_control')?.status === 'passed'
}

async function submit(taskId: string) {
  const expectedApprovalId = approvalId.value
  submitError[taskId] = ''
  submitBusy[taskId] = true
  try {
    const redId = evidence.runOf(expectedApprovalId, planId.value, taskId, 'expected_red')?.evidenceId ?? ''
    const negId = evidence.runOf(expectedApprovalId, planId.value, taskId, 'negative_control')?.evidenceId ?? ''
    const mutationId = mutationIds[taskId] ?? ''
    const testCommit = testCommitInput[taskId] ?? ''
    const result = await props.submitTestContract(planId.value, taskId, testCommit, redId, negId, mutationId)
    if (approvalId.value !== expectedApprovalId) return
    submitResult[taskId] = result
  } catch (e) {
    if (approvalId.value !== expectedApprovalId) return
    submitError[taskId] = String(e)
  } finally {
    if (approvalId.value === expectedApprovalId) submitBusy[taskId] = false
  }
}
</script>

<template>
  <div class="tca-workspace">
    <p v-if="!planId" class="empty" data-test="tca-empty">{{ t('tcaWorkspace.empty') }}</p>
    <template v-else>
      <p v-if="loadError" class="err" data-test="tca-load-error">{{ loadError }}</p>
      <p v-if="candidatesError" class="err" data-test="tca-candidates-error">{{ candidatesError }}</p>

      <div
        v-for="task in tasks" :key="task.task_id" class="task-row"
        :class="{ highlighted: !!focusTaskId && task.task_id === focusTaskId }"
        :data-test="'tca-row-' + task.task_id"
      >
        <div class="head">
          <span class="task-id">{{ task.task_id }}</span>
          <span class="task-title">{{ task.title }}</span>
        </div>

        <div class="test-commit">
          <select v-model="testCommitInput[task.task_id]" :data-test="'test-commit-select-' + task.task_id">
            <option value="">{{ t('tcaWorkspace.testCommit.pick') }}</option>
            <option v-for="c in candidates" :key="c.oid" :value="c.oid">{{ c.oid.slice(0, 10) }} — {{ c.subject }}</option>
          </select>
          <input
            v-model="testCommitInput[task.task_id]" :data-test="'test-commit-input-' + task.task_id"
            :placeholder="t('tcaWorkspace.testCommit.placeholder')"
          />
          <button type="button" :data-test="'precheck-' + task.task_id" :disabled="precheckBusy[task.task_id]" @click="runPrecheck(task.task_id)">
            {{ t('tcaWorkspace.action.precheck') }}
          </button>
          <span v-if="precheckError[task.task_id]" class="err" :data-test="'precheck-error-' + task.task_id">{{ precheckError[task.task_id] }}</span>
          <span v-else-if="precheckOk[task.task_id]" class="ok" :data-test="'precheck-ok-' + task.task_id">{{ t('tcaWorkspace.testCommit.precheckOk') }}</span>
        </div>

        <div class="mutation">
          <textarea
            v-model="patchInput[task.task_id]" :data-test="'mutation-patch-' + task.task_id"
            :placeholder="t('tcaWorkspace.mutationPatch.placeholder')"
          />
          <button
            type="button" :data-test="'register-mutation-' + task.task_id" :disabled="registerBusy[task.task_id]"
            @click="registerMutationFor(task.task_id)"
          >{{ t('tcaWorkspace.action.registerMutation') }}</button>
          <span v-if="mutationIds[task.task_id]" class="ok" :data-test="'mutation-id-' + task.task_id">
            {{ t('tcaWorkspace.mutationId', { id: mutationIds[task.task_id] }) }}
          </span>
          <span v-if="registerError[task.task_id]" class="err" :data-test="'register-error-' + task.task_id">{{ registerError[task.task_id] }}</span>
        </div>

        <div class="runs">
          <div v-for="kind in ['expected_red', 'negative_control']" :key="kind" class="run-row">
            <button
              type="button" :data-test="'run-' + kind + '-' + task.task_id"
              :disabled="isTaskBusy(task.task_id) || (kind === 'negative_control' && !mutationIds[task.task_id])"
              @click="run(task.task_id, kind)"
            >{{ kind === 'expected_red' ? t('tcaWorkspace.action.runExpectedRed') : t('tcaWorkspace.action.runNegativeControl') }}</button>
            <span v-if="isRunBusy(task.task_id, kind)" class="busy" :data-test="'run-busy-' + kind + '-' + task.task_id">
              {{ t('evidence.run.running') }}
            </span>
            <template v-else-if="evidence.runOf(approvalId, planId, task.task_id, kind)">
              <span
                :class="['badge', 'badge-' + evidence.runOf(approvalId, planId, task.task_id, kind)!.status]"
                :data-test="'run-result-' + kind + '-' + task.task_id"
              >{{ resolveState(evidenceResultKeys, evidence.runOf(approvalId, planId, task.task_id, kind)!.status, t) }}</span>
              <span v-if="evidence.runOf(approvalId, planId, task.task_id, kind)!.evidenceId" class="evidence-id" :data-test="'evidence-id-' + kind + '-' + task.task_id">
                {{ evidence.runOf(approvalId, planId, task.task_id, kind)!.evidenceId }}
              </span>
              <template v-if="evidence.runOf(approvalId, planId, task.task_id, kind)!.status === 'error'">
                <span class="err" :data-test="'run-error-' + kind + '-' + task.task_id">{{ evidence.runOf(approvalId, planId, task.task_id, kind)!.error }}</span>
                <span
                  v-if="isStaleGenerationError(evidence.runOf(approvalId, planId, task.task_id, kind)!.error)"
                  class="hint" :data-test="'run-stale-hint-' + kind + '-' + task.task_id"
                >{{ t('tcaWorkspace.staleGeneration.hint') }}</span>
                <button
                  type="button" :data-test="'retry-' + kind + '-' + task.task_id" :disabled="isTaskBusy(task.task_id)"
                  @click="run(task.task_id, kind)"
                >{{ t('tcaWorkspace.action.retry') }}</button>
              </template>
            </template>
          </div>
        </div>

        <div class="submit">
          <button
            type="button" :data-test="'submit-tca-' + task.task_id" :disabled="submitBusy[task.task_id] || !bothPassed(task.task_id)"
            @click="submit(task.task_id)"
          >{{ t('tcaWorkspace.action.submit') }}</button>
          <span v-if="submitResult[task.task_id]" class="ok" :data-test="'submit-result-' + task.task_id">
            {{ t('tcaWorkspace.submittedApprovalId', { id: submitResult[task.task_id] }) }}
          </span>
          <span v-if="submitError[task.task_id]" class="err" :data-test="'submit-error-' + task.task_id">{{ submitError[task.task_id] }}</span>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.tca-workspace { display: flex; flex-direction: column; gap: 10px; padding: 8px; text-align: left; height: 100%; overflow-y: auto; }
.empty { color: var(--text-faint); font-size: var(--fs-s); }
.task-row { border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; display: flex; flex-direction: column; gap: 6px; }
.task-row.highlighted { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
.head { display: flex; gap: 8px; align-items: baseline; }
.head .task-id { font-weight: 600; }
.head .task-title { color: var(--text-muted); font-size: var(--fs-s); }
.test-commit, .mutation, .submit { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.mutation textarea { flex: 1 1 100%; min-height: 60px; padding: 4px 6px; }
.runs { display: flex; flex-direction: column; gap: 4px; }
.run-row { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: var(--fs-s); }
.badge { padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; }
.badge-passed { background: var(--ok); color: #10201e; }
.badge-failed, .badge-error { background: var(--err); color: #2a0d0b; }
.evidence-id { color: var(--text-faint); font-size: var(--fs-s); overflow-wrap: anywhere; }
.busy { color: var(--text-muted); font-size: var(--fs-s); }
.err { color: var(--err); font-size: var(--fs-s); }
.hint { color: var(--text-muted); font-size: var(--fs-s); font-style: italic; }
.ok { color: var(--text-muted); font-size: var(--fs-s); }
</style>
