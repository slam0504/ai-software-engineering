<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ResolveApproval } from '../../wailsjs/go/main/App'
import { useSession } from '../stores/session'

interface Req { id: string; provider: string; toolName: string; inputJson: string }

const { t } = useI18n()
const s = useSession()
// FIFO queue（M1.5 plan D7）：[0] 為顯示中；顯示中的請求不被後到覆蓋。
const queue = ref<Req[]>([])
const reason = ref('')
const error = ref('')

const current = computed<Req | null>(() => queue.value[0] ?? null)

function focusProvider(r: Req | undefined) {
  if (!r) return
  s.setActiveProvider(r.provider === 'codex' ? 'codex' : 'claude') // 彈出／promotion 時自動切 tab
}

function removeById(id: string) {
  const idx = queue.value.findIndex(r => r.id === id)
  if (idx === -1) return
  const wasCurrent = idx === 0
  queue.value.splice(idx, 1)
  if (wasCurrent) {
    reason.value = ''
    error.value = ''
    focusProvider(queue.value[0]) // promotion：輪到顯示才切 tab
  }
}

EventsOn('approval:request', (r: Req) => {
  queue.value.push(r)
  if (queue.value.length === 1) { // 立即顯示的第一筆
    reason.value = ''
    error.value = ''
    focusProvider(r)
  }
})
EventsOn('approval:dismiss', (d: { id: string }) => removeById(d.id)) // timeout／resolved：按 ID 移除正確項目

async function decide(allow: boolean, why?: string) {
  const r = current.value
  if (!r) return
  try {
    await ResolveApproval(r.id, allow, why ?? reason.value)
    removeById(r.id)
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
      <h3>[{{ current.provider }}] 工具權限請求：{{ current.toolName }}</h3>
      <pre>{{ current.inputJson }}</pre>
      <input v-model="reason" :placeholder="t('approval.reason.placeholder')" />
      <div class="actions">
        <button class="allow" @click="decide(true)">{{ t('approval.action.allow') }}</button>
        <button class="deny" @click="decide(false)">{{ t('approval.action.deny') }}</button>
        <span v-if="queue.length > 1" class="pending">＋{{ queue.length - 1 }} 筆等待中</span>
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
