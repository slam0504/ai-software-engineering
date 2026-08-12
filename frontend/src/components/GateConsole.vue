<script setup lang="ts">
import { reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GateEntry } from '../types'
import { resolveState, gateStateKeys } from '../i18n/stateKeys'

const { t } = useI18n()

// Gate 1 主控台（spec §5.3）：entries／decide 走 props 注入（測試以 props 驅動，
// 不依賴 live Wails binding）；真實 wiring 在 App.vue（GateDecide＋GateList refresh）。
const props = defineProps<{
  entries: GateEntry[]
  decide: (id: string, decision: string, reason: string) => void
  degraded?: boolean
}>()

const reasons = reactive<Record<string, string>>({}) // 理由欄：per approval_id 獨立輸入
const hints = reactive<Record<string, boolean>>({}) // reject 無理由時的提示旗標

function onApprove(id: string) {
  props.decide(id, 'approved', reasons[id] ?? '')
}

function onReject(id: string) {
  const reason = reasons[id] ?? ''
  if (!reason) { // reject 必填理由，空理由不送（spec §5.3）
    hints[id] = true
    return
  }
  hints[id] = false
  props.decide(id, 'rejected', reason)
}
</script>

<template>
  <div class="gate-console">
    <p v-if="degraded" class="degraded-notice">{{ t('gate.degradedNotice') }}</p>
    <p v-if="entries.length === 0" class="empty">{{ t('gate.empty') }}</p>
    <div v-for="e in entries" :key="e.approval_id" class="entry">
      <div class="head">
        <span class="id">{{ e.approval_id }}</span>
        <span v-if="e.gate" class="gate">{{ e.gate }}</span>
        <span :class="['badge', 'badge-' + e.state]" :data-test="'badge-' + e.approval_id">{{ resolveState(gateStateKeys, e.state, t) }}</span>
      </div>
      <ul v-if="e.bindings && e.bindings.length" class="bindings">
        <li v-for="b in e.bindings" :key="b.kind + b.ref">{{ b.kind }}: {{ b.digest }}</li>
      </ul>
      <p v-else-if="e.base_commit" class="bindings">base_commit: {{ e.base_commit }}</p>
      <div v-if="e.state === 'pending'" class="actions">
        <input
          v-model="reasons[e.approval_id]"
          data-test="reason"
          :placeholder="t('gate.reason.placeholder')"
          :disabled="degraded"
        />
        <button data-test="approve" :disabled="degraded" @click="onApprove(e.approval_id)">{{ t('gate.action.approve') }}</button>
        <button data-test="reject" :disabled="degraded" @click="onReject(e.approval_id)">{{ t('gate.action.reject') }}</button>
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
.head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.id { font-weight: 600; overflow-wrap: anywhere; word-break: break-all; }
.gate { color: var(--text-muted); font-size: var(--fs-s); }
.badge { margin-left: auto; font-size: var(--fs-s); padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; }
.badge-active { background: var(--ok); color: #10201e; }
.badge-stale { background: var(--err); color: #2a0d0b; }
.badge-pending { background: var(--warn); color: #2a2410; }
.badge-superseded { background: var(--text-faint, #6b7280); color: #f0f0f0; }
.bindings { list-style: none; margin: 4px 0 0; padding: 0; color: var(--text-muted); font-size: var(--fs-s); overflow-wrap: anywhere; word-break: break-all; }
.bindings li { overflow-wrap: anywhere; word-break: break-all; }
.actions { display: flex; align-items: center; gap: 6px; margin-top: 6px; flex-wrap: wrap; }
.actions input { flex: 1; min-width: 120px; padding: 4px 6px; }
.hint { color: var(--err); font-size: var(--fs-s); }
</style>
