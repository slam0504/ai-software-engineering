<script setup lang="ts">
import { useSession } from '../stores/session'
const s = useSession()
const stateLabel: Record<string, string> = {
  idle: '待命', waiting: '等待回覆', streaming: '回覆中', tool_running: '工具執行中',
  awaiting_approval: '等待核可', retrying: '重試中', done: '完成', failed: '失敗',
}
</script>

<template>
  <div class="status">
    <span class="task">任務：{{ s.taskId || '—' }}</span>
    <span :class="['state', s.state]">{{ stateLabel[s.state] ?? s.state }}</span>
    <span class="sid">session：{{ s.sessionId || '—' }}</span>
    <span class="usage" :title="s.usageSemantics === 'provider_latest' ? 'provider 最新回報值' : '本 session 累計'">
      tokens {{ s.totals.input }}/{{ s.totals.output }}{{ s.usageSemantics === 'provider_latest' ? '*' : '' }}
    </span>
    <span class="cost">{{ s.costDisplay }}</span>
  </div>
</template>

<style scoped>
.status { display: flex; gap: 16px; padding: 4px 12px; font-size: 12px; border-top: 1px solid var(--border); color: var(--text-muted); }
.state.awaiting_approval { color: var(--warn); }
.state.failed { color: var(--err); }
.state.waiting, .state.streaming, .state.tool_running { color: var(--ok); }
</style>
