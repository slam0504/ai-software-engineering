<script setup lang="ts">
import { reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GateEntry, GateDecisionTask, RiskSelection } from '../types'
import type { evidence } from '../../wailsjs/go/models'
import { resolveState, gateStateKeys, evidenceResultKeys, riskTierKeys } from '../i18n/stateKeys'

const { t } = useI18n()

// Gate 主控台（spec §5.3／§3.3）：entries／decide／loadDecisionContext 走 props 注入
// （測試以 props 驅動，不依賴 live Wails binding）；真實 wiring 在 App.vue
// （GateDecide＋GateDecisionContext＋GateList refresh）。getEvidence（Task 22）
// 同一慣例：tca 卡片的兩筆 evidence 摘要走 props 注入，不依賴真實 Wails binding。
const props = defineProps<{
  entries: GateEntry[]
  decide: (id: string, decision: string, reason: string, riskSelections: RiskSelection[]) => void
  loadDecisionContext?: (approvalId: string) => Promise<{ tasks: GateDecisionTask[] }>
  getEvidence?: (evidenceId: string) => Promise<evidence.EvidenceRun>
  degraded?: boolean
  highlightId?: string
}>()
const emit = defineEmits<{
  (e: 'open-evidence', evidenceId: string): void
  (e: 'escalate', payload: { sourceRef: string; blockScope: string }): void
}>()

// scopeForEntry：review fix（spec §3.8 回填）——鏡射後端 app.go 的
// scopeForSubject（gate1／未知 gate→workspace、gate2 "plan:<id>"→
// "gate2:<id>"、tca "task:<p>/<t>"→"tca:<p>/<t>"），讓「建立升級項目」按鈕
// 預填的 blockScope 與後端阻擋語意一致，不是憑空編一個字串。
function scopeForEntry(e: GateEntry): string {
  if (e.gate === 'gate2') {
    const id = e.subject?.startsWith('plan:') ? e.subject.slice('plan:'.length) : (e.subject ?? '')
    return 'gate2:' + id
  }
  if (e.gate === 'test_contract_approval') {
    const rest = e.subject?.startsWith('task:') ? e.subject.slice('task:'.length) : (e.subject ?? '')
    return 'tca:' + rest
  }
  return 'workspace'
}
function onEscalate(e: GateEntry) {
  emit('escalate', { sourceRef: 'approval:' + e.approval_id, blockScope: scopeForEntry(e) })
}

const reasons = reactive<Record<string, string>>({}) // 理由欄：per approval_id 獨立輸入
const hints = reactive<Record<string, boolean>>({}) // reject 無理由時的提示旗標

// gate2 pending 卡片的 risk decision 區狀態：per approval_id 一份 committed plan
// task context（GateDecisionContext）／per-task 選擇（selected tier／override reason）。
const riskTasks = reactive<Record<string, GateDecisionTask[]>>({})
const riskErrors = reactive<Record<string, string>>({})
const selectedTier = reactive<Record<string, Record<string, string>>>({})
const overrideReason = reactive<Record<string, Record<string, string>>>({})

const tierOrder: Record<string, number> = { low: 1, medium: 2, high: 3 }
const allTiers = ['low', 'medium', 'high']

function isGate2Pending(e: GateEntry): boolean {
  return e.state === 'pending' && e.gate === 'gate2'
}

// 下拉只列 >= minimum 的 tier（binding constraint，spec 明文）：未知 minimum 時不過濾，
// 讓後端錯誤訊息浮現而不是前端默默清空選項。
function tierOptions(task: GateDecisionTask): string[] {
  const minRank = tierOrder[task.minimum_risk_tier]
  if (!minRank) return allTiers
  return allTiers.filter(tier => tierOrder[tier] >= minRank)
}

async function ensureRiskContext(id: string) {
  if (!props.loadDecisionContext) return
  if (riskTasks[id] || riskErrors[id]) return // 已載入或已知失敗，不重複打
  try {
    const ctx = await props.loadDecisionContext(id)
    const tasks = ctx.tasks ?? []
    riskTasks[id] = tasks
    const tierByTask: Record<string, string> = {}
    const reasonByTask: Record<string, string> = {}
    for (const task of tasks) {
      tierByTask[task.task_id] = task.planner_risk_tier // 預設＝planner
      reasonByTask[task.task_id] = ''
    }
    selectedTier[id] = tierByTask
    overrideReason[id] = reasonByTask
  } catch (e) {
    riskErrors[id] = String(e) // 錯誤原文顯示，不吞
  }
}

// tca 卡片（Task 22，§3.4／§6）：test_contract_approval gate 的兩筆 evidence_run
// binding 只帶 ref（evidence_id）／digest——role／result／test_commit 要顯示
// 短格式必須另外呼叫 EvidenceGet(ref) 取得完整 record，鏡射 ensureRiskContext
// 的載入＋快取慣例（載入中／已知失敗都不重複打）。
function isTca(e: GateEntry): boolean {
  return e.gate === 'test_contract_approval'
}
function evidenceBindingsOf(e: GateEntry) {
  return (e.bindings ?? []).filter(b => b.kind === 'evidence_run')
}
function mutationBindingOf(e: GateEntry) {
  return (e.bindings ?? []).find(b => b.kind === 'mutation')
}
function gate2ApprovalIdOf(e: GateEntry): string {
  const b = (e.bindings ?? []).find(b => b.kind === 'gate2_approval')
  if (!b) return ''
  return b.ref.startsWith('approval:') ? b.ref.slice('approval:'.length) : b.ref
}
const evidenceCache = reactive<Record<string, evidence.EvidenceRun>>({})
const evidenceErrors = reactive<Record<string, string>>({})
async function ensureEvidence(evidenceId: string) {
  if (!props.getEvidence) return
  if (evidenceCache[evidenceId] || evidenceErrors[evidenceId]) return
  try {
    evidenceCache[evidenceId] = await props.getEvidence(evidenceId)
  } catch (e) {
    evidenceErrors[evidenceId] = String(e) // 錯誤原文顯示，不吞
  }
}

// scrollToApproval：gate2_approval 連結點擊後捲到對應卡片（同 DagPane
// select-task → highlightId 的「導航」語意，這裡改用 scrollIntoView 直接定位，
// 不像 highlightId 需要跨元件狀態）。jsdom 測試環境不一定實作
// scrollIntoView，optional chaining 讓測試安全跳過。
function scrollToApproval(id: string) {
  const el = document.querySelector(`[data-test="entry-${id}"]`) as (HTMLElement & { scrollIntoView?: (opts?: unknown) => void }) | null
  el?.scrollIntoView?.({ behavior: 'smooth', block: 'center' })
}

// shortOID：test_commit 短格式（git OID，非 digest——沒有 "sha256:" 前綴，
// shortDigest 的 12 字元切法一樣適用，各自獨立避免耦合兩種不同語意的字串）。
function shortOID(oid: string): string {
  return oid.length > 10 ? oid.slice(0, 10) : oid
}

watch(() => props.entries, (entries) => {
  for (const e of entries) {
    if (isGate2Pending(e)) void ensureRiskContext(e.approval_id)
    if (isTca(e)) for (const b of evidenceBindingsOf(e)) void ensureEvidence(b.ref)
  }
}, { immediate: true })

function needsOverride(id: string, task: GateDecisionTask): boolean {
  const sel = selectedTier[id]?.[task.task_id]
  if (!sel) return false
  return tierOrder[sel] < tierOrder[task.planner_risk_tier]
}

// selected < minimum 防禦縱深：下拉已依 minimum 過濾，這裡再擋一次送出，避免下拉
// 過濾邏輯未來被改動或繞過時，前端仍判 valid 而讓送出後才被後端拒（gatepolicy/gate2.go
// 的 selected_risk_tier below minimum_risk_tier 檢查）。
function belowMinimum(id: string, task: GateDecisionTask): boolean {
  const sel = selectedTier[id]?.[task.task_id]
  if (!sel) return false
  return tierOrder[sel] < tierOrder[task.minimum_risk_tier]
}

function riskValid(id: string): boolean {
  const tasks = riskTasks[id]
  if (!tasks) return false // context 尚未載入完成（或失敗）前不可核可
  return tasks.every(t =>
    !belowMinimum(id, t) && (!needsOverride(id, t) || !!(overrideReason[id]?.[t.task_id] ?? '').trim()))
}

function approveDisabled(e: GateEntry): boolean {
  if (props.degraded) return true
  if (isGate2Pending(e)) return !riskValid(e.approval_id)
  return false
}

function buildRiskSelections(id: string): RiskSelection[] {
  const tasks = riskTasks[id] ?? []
  return tasks.map(t => ({
    TaskID: t.task_id,
    SelectedRiskTier: selectedTier[id]?.[t.task_id] ?? t.planner_risk_tier,
    OverrideReason: overrideReason[id]?.[t.task_id] ?? '',
  }))
}

function onApprove(e: GateEntry) {
  const id = e.approval_id
  const riskSelections = isGate2Pending(e) ? buildRiskSelections(id) : []
  props.decide(id, 'approved', reasons[id] ?? '', riskSelections)
}

function onReject(e: GateEntry) {
  const id = e.approval_id
  const reason = reasons[id] ?? ''
  if (!reason) { // reject 只需 reason（無 risk 輸入），空理由不送（spec §5.3）
    hints[id] = true
    return
  }
  hints[id] = false
  props.decide(id, 'rejected', reason, [])
}

// bindings 短格式：前 12 字元＋… ，全文交給 title tooltip
function shortDigest(d: string): string {
  return d.length > 12 ? d.slice(0, 12) + '…' : d
}
</script>

<template>
  <div class="gate-console">
    <p v-if="degraded" class="degraded-notice">{{ t('gate.degradedNotice') }}</p>
    <p v-if="entries.length === 0" class="empty">{{ t('gate.empty') }}</p>
    <div
      v-for="e in entries" :key="e.approval_id" class="entry"
      :class="{ highlighted: !!highlightId && e.approval_id === highlightId }"
      :data-test="'entry-' + e.approval_id"
    >
      <div class="head">
        <span class="id">{{ e.approval_id }}</span>
        <span v-if="e.gate" class="gate">{{ e.gate }}</span>
        <span v-if="e.subject" class="subject">{{ e.subject }}</span>
        <span :class="['badge', 'badge-' + e.state]" :data-test="'badge-' + e.approval_id">{{ resolveState(gateStateKeys, e.state, t) }}</span>
        <button type="button" :data-test="'escalate-' + e.approval_id" @click="onEscalate(e)">{{ t('escalation.create.buttonFrom') }}</button>
      </div>
      <ul v-if="e.bindings && e.bindings.length" class="bindings">
        <li v-for="b in e.bindings" :key="b.kind + (b.role ?? '') + b.ref" :title="b.digest">
          {{ b.kind }}<template v-if="b.role">（{{ b.role }}）</template>: {{ shortDigest(b.digest) }}
        </li>
      </ul>
      <p v-else-if="e.base_commit" class="bindings">{{ t('gate.label.baseCommit') }}: {{ e.base_commit }}</p>

      <div v-if="isGate2Pending(e)" class="risk-section" data-test="risk-section">
        <p v-if="riskErrors[e.approval_id]" class="err" data-test="risk-error">{{ riskErrors[e.approval_id] }}</p>
        <div v-else-if="riskTasks[e.approval_id]" class="risk-rows">
          <div
            v-for="task in riskTasks[e.approval_id]" :key="task.task_id" class="risk-row"
            :data-test="'risk-row-' + task.task_id"
          >
            <span class="task-id">{{ task.task_id }}</span>
            <span class="task-title">{{ task.title }}</span>
            <span class="tier-ro" data-test="minimum">{{ t('gate.risk.minimum') }}: {{ resolveState(riskTierKeys, task.minimum_risk_tier, t) }}</span>
            <span class="tier-ro" data-test="planner">{{ t('gate.risk.planner') }}: {{ resolveState(riskTierKeys, task.planner_risk_tier, t) }}</span>
            <select
              v-model="selectedTier[e.approval_id][task.task_id]"
              :data-test="'selected-' + task.task_id"
              :disabled="degraded"
            >
              <option v-for="tier in tierOptions(task)" :key="tier" :value="tier">{{ resolveState(riskTierKeys, tier, t) }}</option>
            </select>
            <textarea
              v-if="needsOverride(e.approval_id, task)"
              v-model="overrideReason[e.approval_id][task.task_id]"
              :data-test="'override-reason-' + task.task_id"
              :placeholder="t('gate.risk.overrideReasonPlaceholder')"
              :disabled="degraded"
            />
          </div>
        </div>
      </div>

      <div v-if="isTca(e)" class="tca-section" data-test="tca-section">
        <p v-if="gate2ApprovalIdOf(e)" class="tca-link">
          <button type="button" data-test="tca-gate2-link" @click="scrollToApproval(gate2ApprovalIdOf(e))">
            {{ t('gate.tca.gate2Link', { id: gate2ApprovalIdOf(e) }) }}
          </button>
        </p>
        <div v-for="b in evidenceBindingsOf(e)" :key="b.role + b.ref" class="tca-evidence" :data-test="'tca-evidence-' + b.role">
          <span class="role">{{ b.role }}</span>
          <span v-if="evidenceErrors[b.ref]" class="err" :data-test="'tca-evidence-error-' + b.role">{{ evidenceErrors[b.ref] }}</span>
          <template v-else-if="evidenceCache[b.ref]">
            <span
              :class="['result', 'result-' + evidenceCache[b.ref].result]"
              :data-test="'tca-evidence-result-' + b.role"
            >{{ resolveState(evidenceResultKeys, evidenceCache[b.ref].result, t) }}</span>
            <span class="test-commit" :title="evidenceCache[b.ref].test_commit">{{ shortOID(evidenceCache[b.ref].test_commit) }}</span>
            <span v-if="evidenceCache[b.ref].result === 'error'" class="err" :data-test="'tca-evidence-observed-' + b.role">
              {{ evidenceCache[b.ref].observed_failure }}
            </span>
            <button type="button" :data-test="'tca-evidence-open-' + b.role" @click="emit('open-evidence', b.ref)">
              {{ t('gate.tca.viewEvidence') }}
            </button>
          </template>
        </div>
        <p v-if="mutationBindingOf(e)" class="tca-mutation" data-test="tca-mutation">
          {{ t('gate.tca.mutationDigest') }}: {{ shortDigest(mutationBindingOf(e)!.digest) }}
        </p>
      </div>

      <div v-if="e.state === 'pending'" class="actions">
        <input
          v-model="reasons[e.approval_id]"
          data-test="reason"
          :placeholder="t('gate.reason.placeholder')"
          :disabled="degraded"
        />
        <button data-test="approve" :disabled="approveDisabled(e)" @click="onApprove(e)">{{ t('gate.action.approve') }}</button>
        <button data-test="reject" :disabled="degraded" @click="onReject(e)">{{ t('gate.action.reject') }}</button>
        <span v-if="hints[e.approval_id]" class="hint">{{ t('gate.reasonHint') }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.gate-console { text-align: left; padding: 8px; overflow-y: auto; }
.degraded-notice { color: var(--err); font-size: var(--fs-s); }
.empty { color: var(--text-faint); font-size: var(--fs-s); }
.entry { border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; margin-bottom: 8px; }
.entry.highlighted { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
.head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.id { font-weight: 600; overflow-wrap: anywhere; word-break: break-all; }
.gate { color: var(--text-muted); font-size: var(--fs-s); }
.subject { color: var(--text-faint); font-size: var(--fs-s); overflow-wrap: anywhere; word-break: break-all; }
.badge { margin-left: auto; font-size: var(--fs-s); padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; }
.badge-active { background: var(--ok); color: #10201e; }
.badge-stale { background: var(--err); color: #2a0d0b; }
.badge-pending { background: var(--warn); color: #2a2410; }
.badge-superseded { background: var(--text-faint, #6b7280); color: #f0f0f0; }
.badge-rejected { background: var(--rejected); color: #2a1708; }
.bindings { list-style: none; margin: 4px 0 0; padding: 0; color: var(--text-muted); font-size: var(--fs-s); overflow-wrap: anywhere; word-break: break-all; }
.bindings li { overflow-wrap: anywhere; word-break: break-all; }
.risk-section { margin-top: 6px; border-top: 1px solid var(--border); padding-top: 6px; }
.risk-rows { display: flex; flex-direction: column; gap: 6px; }
.risk-row { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: var(--fs-s); }
.risk-row .task-id { font-weight: 600; }
.risk-row .task-title { color: var(--text-muted); }
.risk-row .tier-ro { color: var(--text-faint); }
.risk-row textarea { flex: 1 1 100%; min-width: 160px; min-height: 40px; padding: 4px 6px; }
.actions { display: flex; align-items: center; gap: 6px; margin-top: 6px; flex-wrap: wrap; }
.actions input { flex: 1; min-width: 120px; padding: 4px 6px; }
.hint { color: var(--err); font-size: var(--fs-s); }
.tca-section { margin-top: 6px; border-top: 1px solid var(--border); padding-top: 6px; font-size: var(--fs-s); }
.tca-link button { background: none; border: none; color: var(--accent); cursor: pointer; padding: 0; text-decoration: underline; }
.tca-evidence { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-top: 4px; }
.tca-evidence .role { font-weight: 600; }
.tca-evidence .result { padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; }
.tca-evidence .result-passed { background: var(--ok); color: #10201e; }
.tca-evidence .result-failed, .tca-evidence .result-error { background: var(--err); color: #2a0d0b; }
.tca-evidence .test-commit { color: var(--text-faint); }
.tca-mutation { color: var(--text-muted); margin-top: 4px; }
</style>
