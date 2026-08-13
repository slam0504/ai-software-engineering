<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
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
  runEvidence: (planId: string, taskId: string, testCommit: string, kind: string, mutationId: string) => Promise<string>
  getEvidence: (evidenceId: string) => Promise<{ result: string }>
  submitTestContract: (planId: string, taskId: string, testCommit: string,
    expectedRedId: string, negativeControlId: string, mutationId: string) => Promise<string>
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

const tasks = ref<GateDecisionTask[]>([])
const loadError = ref('')
async function loadTasks() {
  loadError.value = ''
  tasks.value = []
  const approvalId = activeGate2.value?.approval_id
  if (!approvalId) return
  try {
    const ctx = await props.loadDecisionContext(approvalId)
    tasks.value = ctx.tasks ?? []
  } catch (e) {
    loadError.value = String(e)
  }
}
watch(() => activeGate2.value?.approval_id, () => { void loadTasks() }, { immediate: true })

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
watch(planId, () => { void loadCandidates() }, { immediate: true })

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

function taskRefOf(taskId: string): string {
  return `${planId.value}/${taskId}`
}

async function runPrecheck(taskId: string) {
  precheckError[taskId] = ''
  precheckOk[taskId] = false
  precheckBusy[taskId] = true
  try {
    await props.validateTestCommit(planId.value, taskId, testCommitInput[taskId] ?? '')
    precheckOk[taskId] = true
  } catch (e) {
    precheckError[taskId] = String(e)
  } finally {
    precheckBusy[taskId] = false
  }
}

async function registerMutationFor(taskId: string) {
  registerError[taskId] = ''
  registerBusy[taskId] = true
  try {
    const id = await props.registerMutation(taskRefOf(taskId), patchInput[taskId] ?? '')
    mutationIds[taskId] = id
    evidence.registerMutation(id, taskRefOf(taskId))
  } catch (e) {
    registerError[taskId] = String(e)
  } finally {
    registerBusy[taskId] = false
  }
}

// isRunBusy：這個 kind 本身正在跑（顯示「執行中…」進度指示）。
function isRunBusy(taskId: string, kind: string): boolean {
  return evidence.runOf(planId.value, taskId, kind)?.status === 'running'
}
// isTaskBusy：review fix（Medium correctness finding）——RunEvidence 是同步
// 長呼叫，同一 task 的 expected_red／negative_control 原本互不 disable：先按
// red 再按 negative_control，兩筆 started 依序抵達會讓只認「最近一筆
// started」的配對邏輯錯位（見 stores/evidence.ts 的修法說明）。per-task 互斥
// 更貼近實際：同一 task 任一 kind 執行中時，兩顆 run 按鈕都 disabled，不能
// 同時觸發第二個 RunEvidence 呼叫。
function isTaskBusy(taskId: string): boolean {
  return evidence.taskHasRunInFlight(planId.value, taskId)
}

// run：RunEvidence 只回傳 evidence_id，不帶 result（RunEvidence 的返回值刻意
// 精簡）——成功後另呼叫 EvidenceGet 取得權威的 result，落地進 evidence store
// （見 stores/evidence.ts 的 setResult 文件）。呼叫本身失敗（含 EmitWorkspace
// 「started」之前就拒絕的早期驗證錯誤，例如無 active gate2）用 setError 落地，
// 錯誤原文顯示，不吞。
async function run(taskId: string, kind: string) {
  const testCommit = testCommitInput[taskId] ?? ''
  const mutationId = kind === 'negative_control' ? (mutationIds[taskId] ?? '') : ''
  evidence.setRunning(planId.value, taskId, kind)
  try {
    const evidenceId = await props.runEvidence(planId.value, taskId, testCommit, kind, mutationId)
    const rec = await props.getEvidence(evidenceId)
    evidence.setResult(planId.value, taskId, kind, evidenceId, rec.result)
  } catch (e) {
    evidence.setError(planId.value, taskId, kind, String(e))
  }
}

function bothPassed(taskId: string): boolean {
  return evidence.runOf(planId.value, taskId, 'expected_red')?.status === 'passed'
    && evidence.runOf(planId.value, taskId, 'negative_control')?.status === 'passed'
}

async function submit(taskId: string) {
  submitError[taskId] = ''
  submitBusy[taskId] = true
  try {
    const redId = evidence.runOf(planId.value, taskId, 'expected_red')?.evidenceId ?? ''
    const negId = evidence.runOf(planId.value, taskId, 'negative_control')?.evidenceId ?? ''
    const mutationId = mutationIds[taskId] ?? ''
    const testCommit = testCommitInput[taskId] ?? ''
    submitResult[taskId] = await props.submitTestContract(planId.value, taskId, testCommit, redId, negId, mutationId)
  } catch (e) {
    submitError[taskId] = String(e)
  } finally {
    submitBusy[taskId] = false
  }
}
</script>

<template>
  <div class="tca-workspace">
    <p v-if="!planId" class="empty" data-test="tca-empty">{{ t('tcaWorkspace.empty') }}</p>
    <template v-else>
      <p v-if="loadError" class="err" data-test="tca-load-error">{{ loadError }}</p>
      <p v-if="candidatesError" class="err" data-test="tca-candidates-error">{{ candidatesError }}</p>

      <div v-for="task in tasks" :key="task.task_id" class="task-row" :data-test="'tca-row-' + task.task_id">
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
            <template v-else-if="evidence.runOf(planId, task.task_id, kind)">
              <span
                :class="['badge', 'badge-' + evidence.runOf(planId, task.task_id, kind)!.status]"
                :data-test="'run-result-' + kind + '-' + task.task_id"
              >{{ resolveState(evidenceResultKeys, evidence.runOf(planId, task.task_id, kind)!.status, t) }}</span>
              <span v-if="evidence.runOf(planId, task.task_id, kind)!.evidenceId" class="evidence-id" :data-test="'evidence-id-' + kind + '-' + task.task_id">
                {{ evidence.runOf(planId, task.task_id, kind)!.evidenceId }}
              </span>
              <template v-if="evidence.runOf(planId, task.task_id, kind)!.status === 'error'">
                <span class="err" :data-test="'run-error-' + kind + '-' + task.task_id">{{ evidence.runOf(planId, task.task_id, kind)!.error }}</span>
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
.ok { color: var(--text-muted); font-size: var(--fs-s); }
</style>
