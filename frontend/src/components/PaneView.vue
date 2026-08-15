<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSession } from '../stores/session'
import { resolveState, sessionStateKeys } from '../i18n/stateKeys'
import { isAtBottom } from '../lib/scroll'

// PaneView（Task 28，spec §3.7）：單一 pane，綁定 DualPane 分配給它的 WSID
// （`s.pins[idx]`）。不管是否 focused，pane 都持續接收該 WSID 的串流／狀態／
// unread（store 的 apply() 只要該 WSID 有 view 就會累積 transcript，跟 focus
// 無關）——composer 是唯一「只有 focused pane 才有」的東西（§3.7：Enter/
// Shift+Enter／SettingsBar 的 End/Terminate/New 只作用於 focused pane）。
//
// review round 1：End／Terminate／New 一律留在 SettingsBar 頂欄（§3.7 原文
// 只點名 SettingsBar，§4 對 PaneView 的描述只到「綁 WSID；focus 樣式明確」）。
// PaneView 原本自帶一份等價 End 按鈕，會在預設畫面（tab='chat'、SettingsBar
// 恆掛頂端、DualPane 用 v-show）同時出現兩顆「結束」，裁決收斂回 SettingsBar，
// 這裡不再重複。
const props = defineProps<{ idx: 0 | 1 }>()
const { t } = useI18n()
const s = useSession()

const wsid = computed(() => s.pins[props.idx] ?? '')
const meta = computed(() => (wsid.value ? s.sessions[wsid.value] : undefined))
const view = computed(() => (wsid.value ? s.views[wsid.value] : undefined))
const focused = computed(() => s.focused === props.idx)

const draft = ref('')
const listEl = ref<HTMLElement | null>(null)
const follow = ref(true) // BAT 慣例：使用者上捲後停止自動跟隨

function onScroll() {
  const el = listEl.value
  if (el) follow.value = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight)
}

async function send() {
  const text = draft.value.trim()
  if (!text || !meta.value || meta.value.busy) return
  draft.value = ''
  follow.value = true
  await s.submit(text) // submit() 作用於 focused pane，composer 只在 focused 時渲染，故一致
}

watch(() => (view.value?.chat.length ?? 0) + (view.value?.chat.at(-1)?.text.length ?? 0), () =>
  nextTick(() => {
    if (follow.value) listEl.value?.scrollTo({ top: listEl.value.scrollHeight })
  }))
</script>

<template>
  <div class="pane" :class="{ focused }" :data-test="'pane-' + idx" @click="s.setFocus(idx)">
    <div v-if="!wsid" class="empty">{{ t('dualPane.empty') }}</div>
    <template v-else>
      <div class="head">
        <span class="provider">{{ meta?.provider }}</span>
        <span v-if="meta?.taskLabel" class="label">{{ meta.taskLabel }}</span>
        <span v-if="meta" :class="['state', meta.state]">{{ resolveState(sessionStateKeys, meta.state, t) }}</span>
      </div>
      <div ref="listEl" class="msgs" @scroll.passive="onScroll">
        <div v-for="(m, i) in view?.chat ?? []" :key="i" :class="['bubble', m.role]">
          <details v-if="m.thinking" class="thinking"><summary>{{ t('chat.thinking') }}</summary>
            <pre>{{ m.thinking }}</pre>
          </details>
          <div class="text">{{ m.text }}<span v-if="m.streaming" class="cursor">▌</span></div>
        </div>
      </div>
      <div v-if="focused" class="composer" data-test="composer">
        <textarea v-model="draft" rows="2" :disabled="meta?.busy"
          :placeholder="t('chat.input.placeholder')"
          @keydown.enter.exact.prevent="send" @click.stop />
        <button :disabled="meta?.busy || !draft.trim()" @click.stop="send">{{ t('chat.action.send') }}</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.pane { display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0; cursor: pointer; }
.pane.focused { box-shadow: inset 0 0 0 2px var(--accent); }
.empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: var(--fs-s); padding: 12px; text-align: center; }
.head { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-bottom: 1px solid var(--border); flex-shrink: 0; cursor: default; }
.head .provider { font-weight: 600; }
.head .label { color: var(--text-muted); font-size: var(--fs-s); }
.head .state { margin-left: auto; font-size: var(--fs-s); color: var(--text-faint); }
.head .state.awaiting_approval { color: var(--warn); }
.head .state.failed { color: var(--err); }
.msgs { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; cursor: default; }
.bubble { max-width: 76%; padding: 8px 12px; border-radius: var(--radius-l); text-align: left; white-space: pre-wrap; box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25); line-height: 1.45; }
.bubble.user { align-self: flex-end; background: var(--bg-bubble-user); border-bottom-right-radius: var(--radius-s); }
.bubble.assistant { align-self: flex-start; background: var(--bg-bubble-assistant); border-bottom-left-radius: var(--radius-s); }
.thinking pre { font-size: 12px; color: var(--text-muted); white-space: pre-wrap; }
.cursor { animation: blink 1s step-start infinite; }
@keyframes blink { 50% { opacity: 0; } }
.composer { display: flex; gap: 8px; padding: 8px; border-top: 1px solid var(--border); cursor: default; }
.composer textarea { flex: 1; resize: none; padding: 8px; }
</style>
