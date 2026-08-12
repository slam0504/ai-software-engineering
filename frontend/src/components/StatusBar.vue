<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useSession } from '../stores/session'
import { resolveState, sessionStateKeys } from '../i18n/stateKeys'
const s = useSession()
const { t } = useI18n()
</script>

<template>
  <div class="status">
    <span class="task">{{ t('statusbar.task', { id: s.taskId || '—' }) }}</span>
    <span :class="['state', s.state]">{{ resolveState(sessionStateKeys, s.state, t) }}</span>
    <span class="sid">{{ t('statusbar.session', { id: s.sessionId || '—' }) }}</span>
    <span class="usage" :title="s.usageSemantics === 'provider_latest' ? t('statusbar.usage.providerLatest') : t('statusbar.usage.sessionTotal')">
      tokens {{ s.totals.input }}/{{ s.totals.output }}{{ s.usageSemantics === 'provider_latest' ? '*' : '' }}
    </span>
    <span class="cost">{{ s.costDisplay }}</span>
  </div>
</template>

<style scoped>
.status { display: flex; gap: var(--space-4); padding: var(--space-1) var(--space-3); font-size: var(--fs-s); border-top: 1px solid var(--border); color: var(--text-muted); background: var(--bg-panel); align-items: center; }
.status > span { white-space: nowrap; }
.sid { overflow: hidden; text-overflow: ellipsis; max-width: 320px; }
.state.awaiting_approval { color: var(--warn); }
.state.failed { color: var(--err); }
.state.waiting, .state.streaming, .state.tool_running { color: var(--ok); }
</style>
