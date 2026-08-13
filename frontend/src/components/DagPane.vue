<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import mermaid from 'mermaid'
import { usePlan } from '../stores/plan'
import { parsePlanDoc, planToMermaid, buildNodeIdMap, type PlanDoc } from '../lib/planDag'

// DagPane（Task 14，spec §6）：plan store 目前檔內容（PlanWorkspace 已載入的
// buffer）→ parsePlanDoc → planToMermaid → mermaid render，重用 DiagramPane.vue
// 的錯誤呈現／render 流程。mermaid.initialize({strict,...}) 是模組載入時就跑
// 一次的全域設定（見 DiagramPane.vue），這裡不重覆呼叫以免互相覆蓋。
//
// 與 DiagramPane 的差異：來源是 plan store 的 reactive currentContent，不是
// 檔案 read()——watch 該欄位（immediate）就同時涵蓋首次渲染與「plan 檔變更→
// 自動重渲染」，不需要另外訂閱 workspace 事件。
//
// 節點點選：mermaid 11 對 flowchart 節點的 DOM id 慣例是
// `flowchart-<nodeId>-<index>`；渲染後用這個規則換回原始 task id 再 emit
// select-task，交給 App.vue 導向 GateConsole（Task 15）。nodeId → task id 的
// 反查表用 planDag 的 buildNodeIdMap（與 planToMermaid 內部同一份邏輯）反轉
// 而來——task id sanitize 後可能碰撞（如 "a b" 與 "a_b" 都變 "a_b"），
// buildNodeIdMap 已經用遞增後綴保證節點 id 彼此不同，這裡才能放心反轉成
// 一對一的 Map，不會把兩個 task 的點選都導到同一個 taskId。
const { t } = useI18n()
const plan = usePlan()
const emit = defineEmits<{ (e: 'select-task', taskId: string): void }>()

const svg = ref('')
const error = ref('')
const container = ref<HTMLElement | null>(null)
let renderSeq = 0

function bindClicks(doc: PlanDoc) {
  if (!container.value) return
  const idByNode = new Map(Array.from(buildNodeIdMap(doc), ([taskId, nodeId]) => [nodeId, taskId]))
  container.value.querySelectorAll<SVGGElement>('.node').forEach(node => {
    const m = /^flowchart-(.+)-\d+$/.exec(node.id)
    const taskId = m ? idByNode.get(m[1]) : undefined
    if (!taskId) return
    node.style.cursor = 'pointer'
    node.addEventListener('click', () => emit('select-task', taskId))
  })
}

async function render() {
  error.value = ''
  const content = plan.currentContent
  if (!content) {
    svg.value = ''
    return
  }
  const doc = parsePlanDoc(content)
  if (!doc) {
    error.value = t('dag.parseError')
    svg.value = ''
    return
  }
  const id = `dag-${++renderSeq}`
  try {
    const { svg: rendered } = await mermaid.render(id, planToMermaid(doc)) // securityLevel:'strict' 已消毒
    svg.value = rendered
  } catch (e) {
    error.value = `mermaid: ${String(e)}` // 錯誤走 textContent（{{ }}），不進 HTML sink
    svg.value = ''
    return
  }
  await nextTick()
  bindClicks(doc)
}

watch(() => plan.currentContent, render, { immediate: true })
</script>

<template>
  <div class="dag-pane">
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="!error && !svg" class="hint">{{ t('dag.empty') }}</div>
    <div v-if="svg" ref="container" class="rendered" v-html="svg" />
  </div>
</template>

<style scoped>
.dag-pane { height: 100%; overflow: auto; padding: 12px 16px; text-align: left; }
.err { color: var(--err); }
.hint { color: var(--text-faint); }
.rendered :deep(svg) { max-width: 100%; }
</style>
