<script setup lang="ts">
// spike quality: to be rebuilt in M1
import { ref } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ResolveApproval } from '../../wailsjs/go/main/App'

const req = ref<{ id: string; provider: string; toolName: string; inputJson: string } | null>(null)
const reason = ref('')
const error = ref('')

EventsOn('approval:request', (r: any) => { req.value = r; reason.value = ''; error.value = '' })

async function decide(allow: boolean) {
  if (!req.value) return
  try {
    await ResolveApproval(req.value.id, allow, reason.value)
    req.value = null
    reason.value = ''
  } catch (e: any) {
    error.value = String(e)
  }
}
</script>

<template>
  <div v-if="req" class="overlay">
    <div class="dialog">
      <h3>[{{ req.provider }}] 工具權限請求：{{ req.toolName }}</h3>
      <pre>{{ req.inputJson }}</pre>
      <input v-model="reason" placeholder="理由（deny 建議填）" />
      <div class="actions">
        <button class="allow" @click="decide(true)">Allow</button>
        <button class="deny" @click="decide(false)">Deny</button>
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
  background: #1e2a38; border: 1px solid #3a4a5a; border-radius: 8px;
  padding: 16px; max-width: 640px; width: 90%; text-align: left;
}
.dialog pre {
  background: #101820; padding: 8px; border-radius: 4px;
  max-height: 240px; overflow: auto; white-space: pre-wrap; word-break: break-all;
}
.dialog input { width: 100%; margin: 8px 0; padding: 6px; }
.actions { display: flex; gap: 8px; }
.allow { background: #2e7d32; color: #fff; }
.deny { background: #c62828; color: #fff; }
.error { color: #ff8a80; }
</style>
