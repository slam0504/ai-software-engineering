<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { resolveState, workspaceReasonKeys } from '../i18n/stateKeys'

// B4 高風險 workspace 警示（spec §5.6、R5）。三種狀態：
//   loading → 尚未取得 status：一行說明（role="status"），AI 工作已停用；
//   failed  → 查詢失敗：role="alert"＋重新查詢，AI 工作已停用；
//   pending → required && !confirmed：高風險警示（role="alert"、--err 樣式），顯示實際目錄、
//             來源與原因，並提供「確認在此目錄進行 AI 工作」→ 確認／取消 兩步流程。
// ok 時不渲染任何東西。元件重新掛載不會自動確認：確認只由使用者按下「確認」觸發。
const props = defineProps<{
  confirm: (expectedPath: string) => Promise<void> // ConfirmWorkspace 綁定（由 App.vue 注入）
  refresh: () => Promise<void> // 重新查詢 WorkspaceConfirmationStatus
}>()
const { t } = useI18n()
const wsc = useWorkspaceConfirmation()
const dialogOpen = ref(false)
const reasons = computed(() => (wsc.status?.reasons ?? []).map(r => resolveState(workspaceReasonKeys, r, t)))

async function doConfirm() {
  const ok = await wsc.confirm(props.confirm, props.refresh)
  if (ok) dialogOpen.value = false
}
// 取消只關閉這次確認操作：不呼叫任何 binding、不改變確認狀態。
function cancel() {
  dialogOpen.value = false
  wsc.confirmError = ''
}
</script>

<template>
  <div v-if="wsc.blockKind === 'loading'" class="ws-status" role="status" data-test="ws-loading">
    {{ t('workspaceConfirm.loading') }}
  </div>
  <div v-else-if="wsc.blockKind === 'failed'" class="ws-alert" role="alert" data-test="ws-query-failed">
    <span>{{ t('workspaceConfirm.queryFailed', { error: wsc.queryError }) }}</span>
    <button type="button" data-test="ws-retry" @click="props.refresh()">{{ t('workspaceConfirm.action.retry') }}</button>
  </div>
  <div v-else-if="wsc.blockKind === 'pending' && wsc.status" class="ws-alert" role="alert" data-test="ws-alert">
    <strong>{{ t('workspaceConfirm.title') }}</strong>
    <p>{{ t('workspaceConfirm.intro') }}</p>
    <dl>
      <dt>{{ t('workspaceConfirm.field.path') }}</dt>
      <dd data-test="ws-path">{{ wsc.status.workspace }}</dd>
      <dt>{{ t('workspaceConfirm.field.source') }}</dt>
      <dd data-test="ws-source">{{ wsc.status.source }}</dd>
      <dt>{{ t('workspaceConfirm.field.reasons') }}</dt>
      <dd>
        <ul>
          <li v-for="(r, i) in reasons" :key="i" data-test="ws-reason">{{ r }}</li>
        </ul>
      </dd>
    </dl>
    <button v-if="!dialogOpen" type="button" data-test="ws-open-confirm" @click="dialogOpen = true">
      {{ t('workspaceConfirm.action.open') }}
    </button>
    <div v-else class="ws-dialog" data-test="ws-confirm-dialog">
      <p>{{ t('workspaceConfirm.dialog.prompt', { path: wsc.status.workspace }) }}</p>
      <button type="button" :disabled="wsc.confirming" data-test="ws-confirm" @click="doConfirm">
        {{ wsc.confirming ? t('workspaceConfirm.confirming') : t('workspaceConfirm.action.confirm') }}
      </button>
      <button type="button" :disabled="wsc.confirming" data-test="ws-cancel" @click="cancel">
        {{ t('workspaceConfirm.action.cancel') }}
      </button>
    </div>
    <p v-if="wsc.confirmError" class="ws-error" data-test="ws-confirm-error">
      {{ t('workspaceConfirm.confirmFailed', { error: wsc.confirmError }) }}
    </p>
  </div>
</template>

<style scoped>
.ws-status { font-size: 11px; color: var(--text-faint); padding: 0 10px 4px; }
.ws-alert { margin: 0 10px 6px; padding: 6px 10px; border: 1px solid var(--err); border-left-width: 4px; border-radius: 4px; color: var(--err); font-size: 12px; text-align: left; }
.ws-alert p { margin: 2px 0; }
.ws-alert dl { display: grid; grid-template-columns: max-content 1fr; gap: 0 8px; margin: 4px 0; }
.ws-alert dt { font-weight: 600; }
.ws-alert dd { margin: 0; word-break: break-all; }
.ws-alert ul { margin: 0; padding-left: 16px; }
.ws-dialog { margin-top: 4px; }
.ws-error { font-weight: 600; }
</style>
