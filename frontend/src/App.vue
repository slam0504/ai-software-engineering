<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { StartSession, SendMessage, CLIInfo } from '../wailsjs/go/main/App'
import { useSession } from './stores/session'
import SettingsBar from './components/SettingsBar.vue'
import ChatPanel from './components/ChatPanel.vue'
import Timeline from './components/Timeline.vue'
import StatusBar from './components/StatusBar.vue'
import FileTree from './components/FileTree.vue'
import PreviewPane from './components/PreviewPane.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'

const s = useSession()
const tab = ref<'chat' | 'preview'>('chat')
const timelineOpen = ref(true) // VS Code panel 慣例（normative：可摺疊；拖高/高度記憶 → M2）
const selectedFile = ref('')
const cliInfo = ref<Record<string, string>>({})

onMounted(async () => {
  s.setBindings({
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (t) => SendMessage(s.provider, t),
  })
  EventsOn('workbench:event', (e: any) => s.apply(e))
  EventsOn('session:done', (d: any) => s.applyDone(d))
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
    <div v-show="timelineOpen" class="tl"><Timeline /></div>
    <button class="tl-toggle" @click="timelineOpen = !timelineOpen">
      {{ timelineOpen ? '▾ Timeline' : '▸ Timeline' }}
    </button>
    <StatusBar />
    <ApprovalDialog />
  </div>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
body { background: #1b2636; color: #e6edf3; font-family: ui-sans-serif, system-ui, sans-serif; }
</style>

<style scoped>
.shell { display: flex; flex-direction: column; height: 100vh; }
.meta { font-size: 11px; color: #66788a; padding: 0 10px 4px; text-align: left; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta .err { color: #ff8a80; margin-left: 8px; }
.body { flex: 1; display: flex; min-height: 0; }
aside { width: 220px; border-right: 1px solid #3a4a5a; overflow-y: auto; }
main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
nav { display: flex; gap: 4px; padding: 4px 8px; }
nav .active { background: #2d5a88; color: #fff; }
main > :not(nav) { flex: 1; min-height: 0; }
.tl { height: 180px; border-top: 1px solid #3a4a5a; }
.tl-toggle { align-self: flex-start; font-size: 11px; background: none; border: none; color: #66788a; cursor: pointer; padding: 2px 10px; }
</style>
