<script setup lang="ts">
import { reactive } from 'vue'
import type { GateEntry } from '../types'

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
    <p v-if="degraded" class="degraded-notice">journal degraded：核可／駁回暫停，僅供讀取（spec §3.2）</p>
    <p v-if="entries.length === 0" class="empty">目前沒有 Gate 1 項目</p>
    <div v-for="e in entries" :key="e.approval_id" class="entry">
      <div class="head">
        <span class="id">{{ e.approval_id }}</span>
        <span v-if="e.gate" class="gate">{{ e.gate }}</span>
        <span class="badge" :data-test="'badge-' + e.approval_id">{{ e.state.toUpperCase() }}</span>
      </div>
      <ul v-if="e.bindings && e.bindings.length" class="bindings">
        <li v-for="b in e.bindings" :key="b.kind + b.ref">{{ b.kind }}: {{ b.digest }}</li>
      </ul>
      <p v-else-if="e.base_commit" class="bindings">base_commit: {{ e.base_commit }}</p>
      <div v-if="e.state === 'pending'" class="actions">
        <input
          v-model="reasons[e.approval_id]"
          data-test="reason"
          placeholder="理由（reject 必填）"
          :disabled="degraded"
        />
        <button data-test="approve" :disabled="degraded" @click="onApprove(e.approval_id)">Approve</button>
        <button data-test="reject" :disabled="degraded" @click="onReject(e.approval_id)">Reject</button>
        <span v-if="hints[e.approval_id]" class="hint">請先填理由再駁回</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.gate-console { text-align: left; padding: 8px; overflow-y: auto; }
.degraded-notice { color: var(--err); font-size: var(--fs-s); }
.empty { color: var(--text-faint); font-size: var(--fs-s); }
.entry { border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; margin-bottom: 8px; }
.head { display: flex; align-items: center; gap: 6px; }
.id { font-weight: 600; }
.gate { color: var(--text-muted); font-size: var(--fs-s); }
.badge { margin-left: auto; font-size: var(--fs-s); padding: 1px 6px; border-radius: var(--radius-s); background: var(--bg-inset); }
.bindings { list-style: none; margin: 4px 0 0; padding: 0; color: var(--text-muted); font-size: var(--fs-s); }
.actions { display: flex; align-items: center; gap: 6px; margin-top: 6px; flex-wrap: wrap; }
.actions input { flex: 1; min-width: 120px; padding: 4px 6px; }
.hint { color: var(--err); font-size: var(--fs-s); }
</style>
