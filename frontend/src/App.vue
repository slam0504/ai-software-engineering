<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { CLIInfo, GateDecide, GateDecisionContext, GateList, ListSessions, SpecList } from '../wailsjs/go/main/App'
import { makeBindings } from './lib/bindings'
import { useSession } from './stores/session'
import { useGate } from './stores/gate'
import { useAssist } from './stores/assist'
import { usePlan } from './stores/plan'
import { useEvidence } from './stores/evidence'
import { useEscalation } from './stores/escalation'
import { routeEnvelope } from './lib/gateRouting'
import { load, save } from './lib/persist'
import { escalationBadge } from './lib/escalationBadge'
import { resolveResubmitTarget } from './lib/staleNav'
import type { GateEntry, RiskSelection } from './types'
import SettingsBar from './components/SettingsBar.vue'
import DualPane from './components/DualPane.vue'
import SessionList from './components/SessionList.vue'
import Timeline from './components/Timeline.vue'
import StatusBar from './components/StatusBar.vue'
import FileTree from './components/FileTree.vue'
import PreviewPane from './components/PreviewPane.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'
import GateConsole from './components/GateConsole.vue'
import EscalationInbox from './components/EscalationInbox.vue'
import SpecWorkspace from './components/SpecWorkspace.vue'
import PlanWorkspace from './components/PlanWorkspace.vue'
import TcaWorkspace from './components/TcaWorkspace.vue'
import EvidenceDetail from './components/EvidenceDetail.vue'
import DiagramPane from './components/DiagramPane.vue'
import DagPane from './components/DagPane.vue'

const { t } = useI18n()
const s = useSession()
const gate = useGate()
const assist = useAssist()
const plan = usePlan()
const evidence = useEvidence()
const escalation = useEscalation()
const wailsBindings = makeBindings() // Task 22：TCA workspace 六個綁定＋Task 25：escalation 收件匣四個綁定的唯一 production 來源（見 bindings.test.ts）
const selectedEvidenceId = ref('')
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
      next[e.approval_id] = { approval_id: e.approval_id, state: e.state, gate: e.gate, subject: e.subject, bindings: e.bindings, base_commit: e.base_commit }
    }
    gate.entries = next
  } catch { /* dev 無綁定時忽略 */ }
}

async function decideGate(id: string, decision: string, reason: string, riskSelections: RiskSelection[]) {
  gateError.value = ''
  try {
    await GateDecide(id, decision, reason, riskSelections)
  } catch (e) {
    gateError.value = String(e)
  }
  await refreshGate()
}
// Task 25：escalation 收件匣——沒有專屬事件 lane（brief 明講不加），沿用
// refreshGate 的重載慣例，operations after ack/resolve/create 由 EscalationInbox
// 自己呼叫 reload（見 wiring below），此處只是唯一的 EscalationList 呼叫點。
async function refreshEscalation() {
  await escalation.load(wailsBindings.EscalationList)
}
const sidePanel = ref<'gate' | 'escalation'>('gate') // 右側欄 Gate／Escalation 並列 tab（§6）
// escalatePrefill：review fix（spec §3.8 回填）——PlanWorkspace／GateConsole／
// EvidenceDetail 的「建立升級項目」按鈕 emit 的 payload，轉發給
// EscalationInbox 的建立表單，並切到 escalation side panel 讓使用者立刻
// 看到已預填的表單（不做 inline dialog，重用既有表單）。
const escalatePrefill = ref<{ sourceRef: string; blockScope: string } | null>(null)
function onEscalate(payload: { sourceRef: string; blockScope: string }) {
  escalatePrefill.value = payload
  sidePanel.value = 'escalation'
}
// escalationBadgeState：委給 lib/escalationBadge.ts 的純函式（見該檔說明），
// 這裡只是把 store 的兩個欄位接進去——邏輯本身直接單元測試，不靠 App.vue
// mount。
const escalationBadgeState = computed(() => escalationBadge(escalation.unavailable, escalation.unresolvedCount))
const tab = ref<'chat' | 'preview' | 'spec' | 'plan' | 'diagram' | 'dag' | 'tca'>('chat')
// Task 15：DagPane 的 select-task → 找出目前 pending 的 gate2 卡片中，
// GateDecisionContext 實際含這個 task_id 的那一筆，於 GateConsole 高亮（gate 面板
// 本身是常駐側欄，不是分頁，故「導航」在此語意上就是高亮＋不動 tab）。查不到對應
// 卡片就保留現狀，不拋錯（M3a 單一 active plan 假設下最多命中一筆）。
const highlightedApprovalId = ref('')
async function onSelectTask(taskId: string) {
  for (const e of gate.list) {
    if (e.gate !== 'gate2' || e.state !== 'pending') continue
    try {
      const ctx = await GateDecisionContext(e.approval_id)
      if (ctx.tasks.some(t => t.task_id === taskId)) {
        highlightedApprovalId.value = e.approval_id
        return
      }
    } catch { /* 該卡片載入失敗就跳過，繼續找下一筆，不炸 */ }
  }
}

// onGoResubmit（M3a.1 Task 11，spec §3.5）：STALE 重核引導——GateConsole 的
// stale 卡片與 EscalationInbox 的系統 stale 項按下「前往重新送核」時觸發，
// payload 已是結構化 (gate, subject)（GateConsole 直接給、EscalationInbox 已
// 用 parseStaleTarget 切過 condition_key）。這裡只做二次解析（subject 形狀是
// 否符合該 gate 的期待格式）＋純 view 導航（切 tab／選 plan 檔／聚焦 tca task
// 列），完全不呼叫任何寫入 binding——不核可、不建立、不解除，只是把操作者帶到
// 「該去哪裡重新送核」，實際動作仍要操作者在對應工作區自行完成。解析失敗（理論
// 上不該發生——GateConsole／EscalationInbox 各自已經擋過一層，這裡是第二層
// fail-safe）一律顯示錯誤、不導航，不猜一個目標出來。
const planFocusPath = ref<string | undefined>(undefined)
const tcaFocusTaskId = ref('')
const goResubmitError = ref('')
function onGoResubmit(payload: { gate: string; subject: string }) {
  goResubmitError.value = ''
  const target = resolveResubmitTarget(payload.gate, payload.subject)
  if (!target) {
    goResubmitError.value = t('app.goResubmit.integrityError')
    return
  }
  if (target.kind === 'gate1') {
    tab.value = 'spec'
  } else if (target.kind === 'gate2') {
    tab.value = 'plan'
    planFocusPath.value = `plan/${target.planId}.yaml`
  } else {
    tab.value = 'tca'
    tcaFocusTaskId.value = target.taskId
  }
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

// 快捷鍵：Cmd+1/2 切 pane 焦點、Cmd+K 聚焦輸入框（Esc 由 ApprovalDialog 處理）
// ——Task 26 之後 pane 綁 WSID，不再是 provider tab。
function onGlobalKeydown(e: KeyboardEvent) {
  if (!e.metaKey) return
  if (e.key === '1') { e.preventDefault(); s.setFocus(0) }
  else if (e.key === '2') { e.preventDefault(); s.setFocus(1) }
  else if (e.key === 'k') {
    e.preventDefault()
    document.querySelector<HTMLTextAreaElement>('.composer textarea')?.focus()
  }
}
onUnmounted(() => window.removeEventListener('keydown', onGlobalKeydown))

onMounted(async () => {
  window.addEventListener('keydown', onGlobalKeydown)
  s.setBindings(wailsBindings)
  EventsOn('workbench:event', (e: any) => {
    const dst = routeEnvelope(e)
    if (dst === 'gate') gate.applyGateEvent(e)
    else if (dst === 'assist') assist.applyAssistEvent(e)
    else if (dst === 'plan') plan.applyAssistEvent(e)
    else if (dst === 'evidence') evidence.applyEvidenceEvent(e)
    // workspace scope 但非 gate／evidence 的事件（replay index degraded、codex
    // broadcast、dispatch fail loud）——此前全被丟進 gate store 靜默丟棄。
    else if (dst === 'notice') s.applyNotice(e)
    else s.apply(e)
  })
  EventsOn('session:done', (d: any) => s.applyDone(d))
  // session 清單以 registry 為權威（ListSessions）；transcript 的視窗化載入是
  // Task 29 的 lazy load，這裡只 hydrate metadata。
  try { s.hydrateSessions(await ListSessions()) } catch { /* dev 無綁定時忽略 */ }
  try { cliInfo.value = await CLIInfo() } catch { /* dev 無綁定時忽略 */ }
  await refreshGate() // 初始 hydrate：讓 restart 後既有的 pending/active/stale 項目立即可見
  await refreshEscalation()
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
      <aside class="side">
        <div class="side-sessions"><SessionList /></div>
        <div class="side-files"><FileTree @select="(p: string) => { selectedFile = p; tab = 'preview' }" /></div>
      </aside>
      <main>
        <nav>
          <button :class="{ active: tab === 'chat' }" @click="tab = 'chat'">{{ t('app.tab.chat') }}</button>
          <button :class="{ active: tab === 'preview' }" @click="tab = 'preview'">{{ t('app.tab.preview') }}</button>
          <button :class="{ active: tab === 'spec' }" @click="tab = 'spec'">{{ t('app.tab.spec') }}</button>
          <button :class="{ active: tab === 'plan' }" @click="tab = 'plan'">{{ t('app.tab.plan') }}</button>
          <button :class="{ active: tab === 'diagram' }" @click="tab = 'diagram'">{{ t('app.tab.diagram') }}</button>
          <button :class="{ active: tab === 'dag' }" @click="tab = 'dag'">{{ t('app.tab.dag') }}</button>
          <button :class="{ active: tab === 'tca' }" @click="tab = 'tca'">{{ t('app.tab.tca') }}</button>
        </nav>
        <DualPane v-show="tab === 'chat'" />
        <PreviewPane v-show="tab === 'preview'" :path="selectedFile" />
        <SpecWorkspace v-if="tab === 'spec'" />
        <PlanWorkspace v-if="tab === 'plan'" :path="planFocusPath" @escalate="onEscalate" />
        <TcaWorkspace
          v-if="tab === 'tca'" :entries="gate.list" :load-decision-context="GateDecisionContext"
          :list-candidates="wailsBindings.EvidenceCommitCandidates" :validate-test-commit="wailsBindings.ValidateTestCommit"
          :register-mutation="wailsBindings.RegisterMutation" :run-evidence="wailsBindings.RunEvidence"
          :get-evidence="wailsBindings.EvidenceGet" :submit-test-contract="wailsBindings.SubmitTestContract"
          :focus-task-id="tcaFocusTaskId"
        />
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
        <nav class="side-nav">
          <button :class="{ active: sidePanel === 'gate' }" @click="sidePanel = 'gate'">{{ t('app.sideTab.gate') }}</button>
          <button :class="{ active: sidePanel === 'escalation' }" data-test="side-tab-escalation" @click="sidePanel = 'escalation'">
            {{ t('app.sideTab.escalation') }}
            <span v-if="escalationBadgeState?.kind === 'warn'" class="badge-count badge-warn" data-test="escalation-badge-warn">!</span>
            <span v-else-if="escalationBadgeState?.kind === 'count'" class="badge-count" data-test="escalation-badge">{{ escalationBadgeState.n }}</span>
          </button>
        </nav>
        <GateConsole
          v-show="sidePanel === 'gate'"
          :entries="gate.list" :decide="decideGate" :load-decision-context="GateDecisionContext"
          :get-evidence="wailsBindings.EvidenceGet" :degraded="gateDegraded" :highlight-id="highlightedApprovalId"
          @open-evidence="selectedEvidenceId = $event" @escalate="onEscalate" @go-resubmit="onGoResubmit"
        />
        <p v-if="gateError && sidePanel === 'gate'" class="gate-err">{{ gateError }}</p>
        <EscalationInbox
          v-show="sidePanel === 'escalation'"
          :entries="escalation.entries" :unavailable="escalation.unavailable" :prefill="escalatePrefill"
          :ack="wailsBindings.EscalationAck" :resolve="wailsBindings.EscalationResolve"
          :create="wailsBindings.EscalationCreate" :reload="refreshEscalation" @go-resubmit="onGoResubmit"
        />
        <p v-if="goResubmitError" class="gate-err" data-test="go-resubmit-error">{{ goResubmitError }}</p>
      </aside>
    </div>
    <div v-show="timelineOpen" class="tl-resize" :title="t('app.resize.height')" @mousedown.prevent="onResizeStart" />
    <div v-show="timelineOpen" class="tl" :style="{ height: timelineHeight + 'px' }"><Timeline /></div>
    <button class="tl-toggle" @click="timelineOpen = !timelineOpen">
      {{ (timelineOpen ? '▾ ' : '▸ ') + t('app.timeline.label') }}
    </button>
    <StatusBar />
    <ApprovalDialog />
    <EvidenceDetail
      :evidence-id="selectedEvidenceId" :get="wailsBindings.EvidenceGet"
      @close="selectedEvidenceId = ''" @escalate="onEscalate"
    />
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
.side { display: flex; flex-direction: column; }
.side-sessions { flex: 0 1 45%; overflow-y: auto; border-bottom: 1px solid var(--border); }
.side-files { flex: 1; overflow-y: auto; min-height: 0; }
.gate-panel { border-left: 1px solid var(--border); overflow-y: auto; flex-shrink: 0; display: flex; flex-direction: column; }
.gate-panel .side-nav { display: flex; gap: 4px; padding: var(--space-1) var(--space-2); border-bottom: 1px solid var(--border); flex-shrink: 0; }
.gate-panel .side-nav button { border: none; background: transparent; color: var(--text-muted); display: flex; align-items: center; gap: 4px; }
.gate-panel .side-nav .active { background: var(--bg-bubble-user); color: #fff; }
.badge-count { background: var(--err); color: #2a0d0b; border-radius: 999px; padding: 0 5px; font-size: 10px; font-weight: 700; }
.badge-warn { padding: 0 6px; } /* 警示徽章沿用 badge-count 底色，僅加寬容納「!」 */
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
