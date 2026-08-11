<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import mermaid from 'mermaid'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { SpecRead } from '../../wailsjs/go/main/App'

// DiagramPane（Task 16，spec §5.2）：spec/context-map/*.mmd 的瀏覽／監看／重渲染
// view，重用 PreviewPane 的 mermaid strict 設定與 render 流程。M2 範圍內僅止於
// 檢視，不是圖形編輯器——沒有任何寫入／編輯操作。
//
// `read` 為可注入 prop（測試走這條路徑，避開真實 Wails binding）：未提供時
// fallback 到真正的 SpecRead。SpecRead 回傳 SpecFile{content,digest}（Task 15
// 起的簽章），這裡只取 .content 餵給 mermaid。
const props = defineProps<{
  path: string
  read?: (rel: string) => Promise<{ content: string; digest: string }>
}>()

const svg = ref('')
const error = ref('')

mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'dark' })

let renderSeq = 0

async function load() {
  error.value = ''
  if (!props.path) {
    svg.value = ''
    return
  }
  const reader = props.read ?? SpecRead
  let content = ''
  try {
    const sf = await reader(props.path)
    content = sf.content
  } catch (e) {
    error.value = String(e)
    svg.value = ''
    return
  }
  const id = `diagram-${++renderSeq}`
  try {
    const { svg: rendered } = await mermaid.render(id, content) // securityLevel:'strict' 已消毒
    svg.value = rendered
  } catch (e) {
    error.value = `mermaid: ${String(e)}` // 錯誤走 textContent（{{ }}），不進 HTML sink
    svg.value = ''
  }
}

watch(() => props.path, load)
onMounted(() => {
  void load()
  try {
    // 開啟中的 context-map 檔變更 → 重渲染（同 PreviewPane 的 diagram:changed 慣例）
    EventsOn('diagram:changed', () => { void load() })
  } catch { /* dev/test 無 runtime 綁定時忽略 */ }
})
</script>

<template>
  <div class="diagram-pane">
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="!error && !svg" class="hint">尚無可顯示的圖表</div>
    <div v-if="svg" class="rendered" v-html="svg" />
  </div>
</template>

<style scoped>
.diagram-pane { height: 100%; overflow: auto; padding: 12px 16px; text-align: left; }
.err { color: var(--err); }
.hint { color: var(--text-faint); }
.rendered :deep(svg) { max-width: 100%; }
</style>
