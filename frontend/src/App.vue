<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { CLIInfo, GateDecide, GateList, RestoreViews, SpecList } from '../wailsjs/go/main/App'
import { makeBindings } from './lib/bindings'
import { useSession } from './stores/session'
import { useGate } from './stores/gate'
import { useAssist } from './stores/assist'
import { usePlan } from './stores/plan'
import { routeEnvelope } from './lib/gateRouting'
import { load, save } from './lib/persist'
import type { GateEntry } from './types'
import SettingsBar from './components/SettingsBar.vue'
import ChatPanel from './components/ChatPanel.vue'
import Timeline from './components/Timeline.vue'
import StatusBar from './components/StatusBar.vue'
import FileTree from './components/FileTree.vue'
import PreviewPane from './components/PreviewPane.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'
import GateConsole from './components/GateConsole.vue'
import SpecWorkspace from './components/SpecWorkspace.vue'
import PlanWorkspace from './components/PlanWorkspace.vue'
import DiagramPane from './components/DiagramPane.vue'
import DagPane from './components/DagPane.vue'

const { t } = useI18n()
const s = useSession()
const gate = useGate()
const assist = useAssist()
const plan = usePlan()
const gateDegraded = ref(false) // GateList().journal_degraded（任一筆為 true）→ 停用核可／駁回（spec §3.2）
const gateError = ref('')

// GateList 為權威重算（每次都 ReconcileGate1 後 project），refresh 後直接覆蓋 store
// projection——比等下一筆 workspace 事件更即時，也修正 decide 失敗後的畫面落後。
async function refreshGate() {
  try {
    const list = await GateList()
    gateDegraded.value = list.some(e => e.journal_degraded === true)
    const next: Record<string, GateEntry> = {}
    for (const e of list) {
      next[e.approval_id] = { approval_id: e.approval_id, state: e.state, gate: e.gate, bindings: e.bindings, base_commit: e.base_commit }
    }
    gate.entries = next
  } catch { /* dev 無綁定時忽略 */ }
}

async function decideGate(id: string, decision: string, reason: string) {
  gateError.value = ''
  try {
    await GateDecide(id, decision, reason, [])
  } catch (e) {
    gateError.value = String(e)
  }
  await refreshGate()
}
const tab = ref<'chat' | 'preview' | 'spec' | 'plan' | 'diagram' | 'dag'>('chat')
// Task 14：任務 DAG 表示圖層——select-task 先 no-op 佔位，導航到 GateConsole 對應項是 Task 15 的範圍
function onSelectTask(taskId: string) {
  console.log('select-task', taskId)
}
// Task 16：表示圖層——spec/context-map/*.mmd 的瀏覽／監看／重渲染 view（M2 非圖形編輯器）
const diagramFiles = ref<string[]>([])
const diagramPath = ref('')
async function refreshDiagramFiles() {
  try {
    const files = (await SpecList()) ?? []
    diagramFiles.value = files
      .map(f => f.path)
      .filter(p => p.startsWith('spec/context-map/') && p.endsWith('.mmd'))
    if (!diagramPath.value && diagramFiles.value.length) diagramPath.value = diagramFiles.value[0]
  } catch { /* dev 無綁定時忽略 */ }
}
watch(tab, t => { if (t === 'diagram') void refreshDiagramFiles() }) // 切到表示圖 tab 時重新掃 spec/context-map/*.mmd，避免新增檔案要重啟才看得到
const timelineOpen = ref(load('wb.tl.open', true)) // VS Code panel 慣例：可摺疊＋記憶
const timelineHeight = ref(load('wb.tl.height', 180)) // 拖高＋記憶（M1.5 T5）
const gateWidth = ref(load('wb.gate.width', 280)) // gate 面板拖寬＋記憶（同 timeline 拖高 pattern）
const selectedFile = ref('')
const cliInfo = ref<Record<string, string>>({})
watch(timelineOpen, v => save('wb.tl.open', v))
watch(timelineHeight, v => save('wb.tl.height', v))
watch(gateWidth, v => save('wb.gate.width', v))

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

// Gate 面板拖寬：resize handle 水平拖曳（往左拖＝變寬，鏡射 timeline 拖高 pattern）
let dragStartX = 0
let dragStartW = 0
function onGateResizeMove(e: MouseEvent) {
  gateWidth.value = Math.min(600, Math.max(200, dragStartW + (dragStartX - e.clientX)))
}
function onGateResizeEnd() {
  window.removeEventListener('mousemove', onGateResizeMove)
  window.removeEventListener('mouseup', onGateResizeEnd)
}
function onGateResizeStart(e: MouseEvent) {
  dragStartX = e.clientX
  dragStartW = gateWidth.value
  window.addEventListener('mousemove', onGateResizeMove)
  window.addEventListener('mouseup', onGateResizeEnd)
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
  s.setBindings(makeBindings())
  EventsOn('workbench:event', (e: any) => {
    const dst = routeEnvelope(e)
    if (dst === 'gate') gate.applyGateEvent(e)
    else if (dst === 'assist') assist.applyAssistEvent(e)
    else if (dst === 'plan') plan.applyAssistEvent(e)
    else s.apply(e)
  })
  EventsOn('session:done', (d: any) => s.applyDone(d))
  try { s.restoreViews(await RestoreViews() as any) } catch { /* dev 無綁定時忽略 */ }
  try { cliInfo.value = await CLIInfo() } catch { /* dev 無綁定時忽略 */ }
  await refreshGate() // 初始 hydrate：讓 restart 後既有的 pending/active/stale 項目立即可見
  await refreshDiagramFiles()
})
</script>

<template>
  <div class="shell">
    <SettingsBar />
    <div class="meta" :title="JSON.stringify(cliInfo)">
      ws: {{ cliInfo.workspaceSource }} @ {{ cliInfo.workspace }} | tools: {{ cliInfo.toolsSource }} | node {{ cliInfo.node }}
      <span v-if="cliInfo.startupError" class="err">{{ t('app.startupError', { error: cliInfo.startupError }) }}</span>
    </div>
    <div class="body">
      <aside><FileTree @select="(p: string) => { selectedFile = p; tab = 'preview' }" /></aside>
      <main>
        <nav>
          <button :class="{ active: tab === 'chat' }" @click="tab = 'chat'">{{ t('app.tab.chat') }}</button>
          <button :class="{ active: tab === 'preview' }" @click="tab = 'preview'">{{ t('app.tab.preview') }}</button>
          <button :class="{ active: tab === 'spec' }" @click="tab = 'spec'">{{ t('app.tab.spec') }}</button>
          <button :class="{ active: tab === 'plan' }" @click="tab = 'plan'">{{ t('app.tab.plan') }}</button>
          <button :class="{ active: tab === 'diagram' }" @click="tab = 'diagram'">{{ t('app.tab.diagram') }}</button>
          <button :class="{ active: tab === 'dag' }" @click="tab = 'dag'">{{ t('app.tab.dag') }}</button>
        </nav>
        <ChatPanel v-show="tab === 'chat'" />
        <PreviewPane v-show="tab === 'preview'" :path="selectedFile" />
        <SpecWorkspace v-if="tab === 'spec'" />
        <PlanWorkspace v-if="tab === 'plan'" />
        <div v-show="tab === 'diagram'" class="diagram-tab">
          <div class="diagram-files">
            <button v-for="f in diagramFiles" :key="f" :class="{ active: f === diagramPath }"
              @click="diagramPath = f">{{ f }}</button>
          </div>
          <DiagramPane :path="diagramPath" />
        </div>
        <DagPane v-if="tab === 'dag'" @select-task="onSelectTask" />
      </main>
      <div class="gate-resize" :title="t('app.resize.width')" @mousedown.prevent="onGateResizeStart" />
      <aside class="gate-panel" :style="{ width: gateWidth + 'px' }">
        <GateConsole :entries="gate.list" :decide="decideGate" :degraded="gateDegraded" />
        <p v-if="gateError" class="gate-err">{{ gateError }}</p>
      </aside>
    </div>
    <div v-show="timelineOpen" class="tl-resize" :title="t('app.resize.height')" @mousedown.prevent="onResizeStart" />
    <div v-show="timelineOpen" class="tl" :style="{ height: timelineHeight + 'px' }"><Timeline /></div>
    <button class="tl-toggle" @click="timelineOpen = !timelineOpen">
      {{ (timelineOpen ? '▾ ' : '▸ ') + t('app.timeline.label') }}
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
.gate-panel { border-left: 1px solid var(--border); overflow-y: auto; flex-shrink: 0; }
.gate-err { color: var(--err); font-size: 11px; padding: 0 8px 8px; }
.gate-resize { width: 5px; cursor: col-resize; background: transparent; flex-shrink: 0; }
.gate-resize:hover { background: var(--accent); }
main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
nav { display: flex; gap: 4px; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border); }
nav button { border: none; background: transparent; color: var(--text-muted); }
nav .active { background: var(--bg-bubble-user); color: #fff; }
main > :not(nav) { flex: 1; min-height: 0; }
.diagram-tab { display: flex; flex-direction: column; min-height: 0; }
.diagram-files { display: flex; gap: 4px; flex-wrap: wrap; padding: var(--space-1) var(--space-2); }
.diagram-files button.active { background: var(--bg-bubble-user); color: #fff; }
.diagram-tab > :not(.diagram-files) { flex: 1; min-height: 0; }
.tl { border-top: 1px solid var(--border); overflow: hidden; }
.tl-resize { height: 4px; cursor: row-resize; background: transparent; }
.tl-resize:hover { background: var(--accent); }
.tl-toggle { align-self: flex-start; font-size: 11px; background: none; border: none; color: var(--text-faint); cursor: pointer; padding: 2px 10px; }
</style>
