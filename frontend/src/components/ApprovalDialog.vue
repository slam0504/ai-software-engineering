<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ResolveApproval } from '../../wailsjs/go/main/App'
import { useSession } from '../stores/session'
import type { ApprovalRestoreTrigger } from '../stores/session'

// Req：後端 a.emit("approval:request", …) 的 payload。Task 26 加入 wsid——
// 多 session 之後 provider 不足以定位來源 session（§3.6.4 的 pane 路由）。
interface Req { id: string; wsid: string; provider: string; toolName: string; inputJson: string }

const { t } = useI18n()
const s = useSession()
// FIFO queue（M1.5 plan D7）：[0] 為顯示中；顯示中的請求不被後到覆蓋。
const queue = ref<Req[]>([])
const reason = ref('')
const error = ref('')

const current = computed<Req | null>(() => queue.value[0] ?? null)

// present：把來源 session 帶到使用者眼前（§3.6.4）——已釘選就切 focus，未釘選
// 則由 store 做 transient secondary presentation。
function present(r: Req | undefined) {
  if (!r?.wsid) return
  s.routeApproval(r.wsid)
}

// dismissCause → §3.6.4 凍結的六種恢復觸發。remove／shutdown 都是走
// denyApprovals → ResolveApproval 出去的（cause=resolved），只有 reason 分得出來。
function triggerOf(cause: string, why: string): ApprovalRestoreTrigger {
  if (cause === 'timeout') return 'timeout'
  if (why === 'session_removed') return 'remove'
  if (why === 'shutdown') return 'shutdown'
  return 'dismiss'
}

function removeById(id: string, trigger: ApprovalRestoreTrigger) {
  const idx = queue.value.findIndex(r => r.id === id)
  if (idx === -1) return
  const wasCurrent = idx === 0
  queue.value.splice(idx, 1)
  if (!wasCurrent) return
  reason.value = ''
  error.value = ''
  // 先恢復原釘選，再讓下一筆（若有）重新路由——順序反過來的話，第二筆的
  // transient 會被緊接著的恢復動作立刻撤掉。
  s.resolveApprovalPresentation(trigger)
  present(queue.value[0]) // promotion：輪到顯示才切 pane
}

EventsOn('approval:request', (r: Req) => {
  queue.value.push(r)
  if (queue.value.length === 1) { // 立即顯示的第一筆
    reason.value = ''
    error.value = ''
    present(r)
  }
})
// timeout／resolved：按 ID 移除正確項目（cause＋reason 決定恢復觸發）
EventsOn('approval:dismiss', (d: { id: string; cause?: string; reason?: string }) =>
  removeById(d.id, triggerOf(d.cause ?? '', d.reason ?? '')))

async function decide(allow: boolean, why?: string) {
  const r = current.value
  if (!r) return
  try {
    await ResolveApproval(r.id, allow, why ?? reason.value)
    removeById(r.id, allow ? 'allow' : 'deny')
  } catch (e: any) {
    error.value = String(e)
  }
}

// Esc＝對目前顯示的請求 Deny（reason=esc；D7／E5b 定案），佇列其餘不受影響。
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && current.value) {
    e.preventDefault()
    void decide(false, 'esc')
  }
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div v-if="current" class="overlay">
    <div class="dialog">
      <h3>[{{ current.provider }}] {{ t('approval.toolRequest', { tool: current.toolName }) }}</h3>
      <pre>{{ current.inputJson }}</pre>
      <input v-model="reason" :placeholder="t('approval.reason.placeholder')" />
      <div class="actions">
        <button class="allow" @click="decide(true)">{{ t('approval.action.allow') }}</button>
        <button class="deny" @click="decide(false)">{{ t('approval.action.deny') }}</button>
        <span v-if="queue.length > 1" class="pending">{{ t('approval.pendingCount', { n: queue.length - 1 }) }}</span>
      </div>
      <p v-if="error" class="error">{{ error }}</p>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.55);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.dialog {
  background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius-m);
  padding: 16px; max-width: 640px; width: 90%; text-align: left;
}
.dialog pre {
  background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s);
  max-height: 240px; overflow: auto; white-space: pre-wrap; word-break: break-all;
}
.dialog input { width: 100%; margin: 8px 0; padding: 6px; }
.actions { display: flex; gap: 8px; align-items: center; }
.allow { background: #2e7d32; color: #fff; }
.deny { background: #c62828; color: #fff; }
.pending { color: var(--text-faint); font-size: var(--fs-s); }
.error { color: var(--err); }
</style>
