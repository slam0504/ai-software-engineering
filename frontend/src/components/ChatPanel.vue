<script setup lang="ts">
import { ref, nextTick, watch } from 'vue'
import { useSession } from '../stores/session'
import { isAtBottom } from '../lib/scroll'

const s = useSession()
const draft = ref('')
const listEl = ref<HTMLElement | null>(null)
const follow = ref(true) // BAT 慣例（normative）：使用者上捲後停止自動跟隨

function onScroll() {
  const el = listEl.value
  if (el) follow.value = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight)
}

async function send() {
  const text = draft.value.trim()
  if (!text || s.busy) return
  draft.value = ''
  follow.value = true // 送出視為回到追蹤
  await s.submit(text)
}

watch(() => s.chat.length + (s.chat.at(-1)?.text.length ?? 0), () =>
  nextTick(() => {
    if (follow.value) listEl.value?.scrollTo({ top: listEl.value.scrollHeight })
  }))
</script>

<template>
  <div class="chat">
    <div ref="listEl" class="msgs" @scroll.passive="onScroll">
      <div v-for="(m, i) in s.chat" :key="i" :class="['bubble', m.role]">
        <details v-if="m.thinking" class="thinking"><summary>thinking</summary>
          <pre>{{ m.thinking }}</pre>
        </details>
        <div class="text">{{ m.text }}<span v-if="m.streaming" class="cursor">▌</span></div>
      </div>
    </div>
    <div class="composer">
      <textarea v-model="draft" rows="2" :disabled="s.busy"
        placeholder="輸入訊息，Enter 送出（Shift+Enter 換行）"
        @keydown.enter.exact.prevent="send" />
      <button :disabled="s.busy || !draft.trim()" @click="send">送出</button>
    </div>
  </div>
</template>

<style scoped>
.chat { display: flex; flex-direction: column; height: 100%; }
.msgs { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.bubble { max-width: 76%; padding: 8px 12px; border-radius: 10px; text-align: left; white-space: pre-wrap; }
.bubble.user { align-self: flex-end; background: var(--bg-bubble-user); }
.bubble.assistant { align-self: flex-start; background: var(--bg-bubble-assistant); }
.thinking pre { font-size: 12px; color: var(--text-muted); white-space: pre-wrap; }
.cursor { animation: blink 1s step-start infinite; }
@keyframes blink { 50% { opacity: 0; } }
.composer { display: flex; gap: 8px; padding: 8px; border-top: 1px solid var(--border); }
.composer textarea { flex: 1; resize: none; padding: 8px; }
</style>
