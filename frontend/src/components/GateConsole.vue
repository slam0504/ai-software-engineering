<script setup lang="ts">
import { reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GateEntry, GateDecisionTask, RiskSelection } from '../types'
import { resolveState, gateStateKeys } from '../i18n/stateKeys'

const { t } = useI18n()

// Gate 主控台（spec §5.3／§3.3）：entries／decide／loadDecisionContext 走 props 注入
// （測試以 props 驅動，不依賴 live Wails binding）；真實 wiring 在 App.vue
// （GateDecide＋GateDecisionContext＋GateList refresh）。
const props = defineProps<{
  entries: GateEntry[]
  decide: (id: string, decision: string, reason: string, riskSelections: RiskSelection[]) => void
  loadDecisionContext?: (approvalId: string) => Promise<{ tasks: GateDecisionTask[] }>
  degraded?: boolean
  highlightId?: string
}>()

const reasons = reactive<Record<string, string>>({}) // 理由欄：per approval_id 獨立輸入
const hints = reactive<Record<string, boolean>>({}) // reject 無理由時的提示旗標

// gate2 pending 卡片的 risk decision 區狀態：per approval_id 一份 committed plan
// task context（GateDecisionContext）／per-task 選擇（selected tier／override reason）。
const riskTasks = reactive<Record<string, GateDecisionTask[]>>({})
const riskErrors = reactive<Record<string, string>>({})
const selectedTier = reactive<Record<string, Record<string, string>>>({})
const overrideReason = reactive<Record<string, Record<string, string>>>({})

const tierOrder: Record<string, number> = { low: 1, medium: 2, high: 3 }

function isGate2Pending(e: GateEntry): boolean {
  return e.state === 'pending' && e.gate === 'gate2'
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

watch(() => props.entries, (entries) => {
  for (const e of entries) if (isGate2Pending(e)) void ensureRiskContext(e.approval_id)
}, { immediate: true })

function needsOverride(id: string, task: GateDecisionTask): boolean {
  const sel = selectedTier[id]?.[task.task_id]
  if (!sel) return false
  return tierOrder[sel] < tierOrder[task.planner_risk_tier]
}

function riskValid(id: string): boolean {
  const tasks = riskTasks[id]
  if (!tasks) return false // context 尚未載入完成（或失敗）前不可核可
  return tasks.every(t => !needsOverride(id, t) || !!(overrideReason[id]?.[t.task_id] ?? '').trim())
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
            <span class="tier-ro" data-test="minimum">{{ t('gate.risk.minimum') }}: {{ task.minimum_risk_tier }}</span>
            <span class="tier-ro" data-test="planner">{{ t('gate.risk.planner') }}: {{ task.planner_risk_tier }}</span>
            <select
              v-model="selectedTier[e.approval_id][task.task_id]"
              :data-test="'selected-' + task.task_id"
              :disabled="degraded"
            >
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
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
</style>
