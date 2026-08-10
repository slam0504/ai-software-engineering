<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { StartSession, SendMessage, CLIInfo, RestoreViews } from '../wailsjs/go/main/App'
import { useSession } from './stores/session'
import { load, save } from './lib/persist'
import SettingsBar from './components/SettingsBar.vue'
import ChatPanel from './components/ChatPanel.vue'
import Timeline from './components/Timeline.vue'
import StatusBar from './components/StatusBar.vue'
import FileTree from './components/FileTree.vue'
import PreviewPane from './components/PreviewPane.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'

const s = useSession()
const tab = ref<'chat' | 'preview'>('chat')
const timelineOpen = ref(load('wb.tl.open', true)) // VS Code panel 慣例：可摺疊＋記憶
const timelineHeight = ref(load('wb.tl.height', 180)) // 拖高＋記憶（M1.5 T5）
const selectedFile = ref('')
const cliInfo = ref<Record<string, string>>({})
watch(timelineOpen, v => save('wb.tl.open', v))
watch(timelineHeight, v => save('wb.tl.height', v))

// Timeline 拖高：resize handle 垂直拖曳
let dragStartY = 0
let dragStartH = 0
function onResizeMove(e: MouseEvent) {
  timelineHeight.value = Math.min(600, Math.max(80, dragStartH + (dragStartY - e.clientY)))
}
function onResizeEnd() {
  window.removeEventListener('mousemove', onResizeMove)
  window.removeEventListener('mouseup', onResizeEnd)
}
function onResizeStart(e: MouseEvent) {
  dragStartY = e.clientY
  dragStartH = timelineHeight.value
  window.addEventListener('mousemove', onResizeMove)
  window.addEventListener('mouseup', onResizeEnd)
}

// 快捷鍵：Cmd+1/2 切 provider tab、Cmd+K 聚焦輸入框（Esc 由 ApprovalDialog 處理）
function onGlobalKeydown(e: KeyboardEvent) {
  if (!e.metaKey) return
  if (e.key === '1') { e.preventDefault(); s.setActiveProvider('claude') }
  else if (e.key === '2') { e.preventDefault(); s.setActiveProvider('codex') }
  else if (e.key === 'k') {
    e.preventDefault()
    document.querySelector<HTMLTextAreaElement>('.composer textarea')?.focus()
  }
}
onUnmounted(() => window.removeEventListener('keydown', onGlobalKeydown))

onMounted(async () => {
  window.addEventListener('keydown', onGlobalKeydown)
  s.setBindings({
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (t) => SendMessage(s.provider, t),
  })
  EventsOn('workbench:event', (e: any) => s.apply(e))
  EventsOn('session:done', (d: any) => s.applyDone(d))
  try { s.restoreViews(await RestoreViews() as any) } catch { /* dev 無綁定時忽略 */ }
  try { cliInfo.value = await CLIInfo() } catch { /* dev 無綁定時忽略 */ }
})
</script>

<template>
  <div class="shell">
    <SettingsBar />
    <div class="meta" :title="JSON.stringify(cliInfo)">
      ws: {{ cliInfo.workspaceSource }} @ {{ cliInfo.workspace }} | tools: {{ cliInfo.toolsSource }} | node {{ cliInfo.node }}
      <span v-if="cliInfo.startupError" class="err">startup: {{ cliInfo.startupError }}</span>
    </div>
    <div class="body">
      <aside><FileTree @select="(p: string) => { selectedFile = p; tab = 'preview' }" /></aside>
      <main>
        <nav>
          <button :class="{ active: tab === 'chat' }" @click="tab = 'chat'">Chat</button>
          <button :class="{ active: tab === 'preview' }" @click="tab = 'preview'">Preview</button>
        </nav>
        <ChatPanel v-show="tab === 'chat'" />
        <PreviewPane v-show="tab === 'preview'" :path="selectedFile" />
      </main>
    </div>
    <div v-show="timelineOpen" class="tl-resize" title="拖曳調整高度" @mousedown.prevent="onResizeStart" />
    <div v-show="timelineOpen" class="tl" :style="{ height: timelineHeight + 'px' }"><Timeline /></div>
    <button class="tl-toggle" @click="timelineOpen = !timelineOpen">
      {{ timelineOpen ? '▾ Timeline' : '▸ Timeline' }}
    </button>
    <StatusBar />
    <ApprovalDialog />
  </div>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
body { background: var(--bg-app); color: var(--text); font-family: ui-sans-serif, system-ui, sans-serif; }
</style>

<style scoped>
.shell { display: flex; flex-direction: column; height: 100vh; }
.meta { font-size: 11px; color: var(--text-faint); padding: 0 10px 4px; text-align: left; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta .err { color: var(--err); margin-left: 8px; }
.body { flex: 1; display: flex; min-height: 0; }
aside { width: 220px; border-right: 1px solid var(--border); overflow-y: auto; }
main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
nav { display: flex; gap: 4px; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border); }
nav button { border: none; background: transparent; color: var(--text-muted); }
nav .active { background: var(--bg-bubble-user); color: #fff; }
main > :not(nav) { flex: 1; min-height: 0; }
.tl { border-top: 1px solid var(--border); overflow: hidden; }
.tl-resize { height: 4px; cursor: row-resize; background: transparent; }
.tl-resize:hover { background: var(--accent); }
.tl-toggle { align-self: flex-start; font-size: 11px; background: none; border: none; color: var(--text-faint); cursor: pointer; padding: 2px 10px; }
</style>
