<script setup lang="ts">
// spike quality: to be rebuilt in M1
import mermaid from 'mermaid'
import { ref, onMounted } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ReadDiagram } from '../../wailsjs/go/main/App'

const el = ref<HTMLElement | null>(null)
const error = ref('')
let n = 0

async function render(src: string) {
  try {
    const { svg } = await mermaid.render(`m0-${n++}`, src)
    if (el.value) el.value.innerHTML = svg
    error.value = ''
  } catch (e: any) {
    error.value = String(e)
  }
}

onMounted(async () => {
  mermaid.initialize({ startOnLoad: false, theme: 'dark' })
  EventsOn('diagram:changed', render)
  try {
    const src = await ReadDiagram()
    if (src) await render(src)
  } catch { /* 尚無圖檔 */ }
})
</script>

<template>
  <div class="pane">
    <p v-if="error" class="error">{{ error }}</p>
    <div ref="el" class="diagram" />
  </div>
</template>

<style scoped>
.pane { height: 100%; overflow: auto; padding: 8px; }
.error { color: #ff8a80; }
</style>
