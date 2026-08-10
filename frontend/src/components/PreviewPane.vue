<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import mermaid from 'mermaid'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ReadWorkspaceFile } from '../../wailsjs/go/main/App'
import { renderMarkdown } from '../lib/markdown'

const props = defineProps<{ path: string }>()

const html = ref('')
const plain = ref('')
const mode = ref<'md' | 'mmd' | 'text' | 'empty'>('empty')
const error = ref('')
const paneEl = ref<HTMLElement | null>(null)

mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'dark' })

let renderSeq = 0

async function renderMermaidBlocks() {
  await nextTick()
  const blocks = Array.from(paneEl.value?.querySelectorAll<HTMLElement>('.mermaid-src') ?? [])
  for (const el of blocks) {
    const src = el.textContent ?? ''
    const id = `mmd-${++renderSeq}`
    try {
      const { svg } = await mermaid.render(id, src) // securityLevel:'strict' 已消毒
      el.outerHTML = svg
    } catch (e) {
      const pre = document.createElement('pre') // 錯誤文字走 textContent，不進 HTML sink
      pre.className = 'mmd-err'
      pre.textContent = `mermaid: ${String(e)}`
      el.replaceWith(pre)
    }
  }
}

// .md：renderMarkdown 後把 ```mermaid``` 區塊逐一 mermaid.render。
// marked 產出 <pre><code class="language-mermaid">，先換成佔位元素再渲染。
function mdToHtml(src: string): string {
  const rendered = renderMarkdown(src)
  const tpl = document.createElement('template')
  tpl.innerHTML = rendered
  for (const code of Array.from(tpl.content.querySelectorAll('pre > code.language-mermaid'))) {
    const holder = document.createElement('div')
    holder.className = 'mermaid-src'
    holder.textContent = code.textContent ?? ''
    code.parentElement!.replaceWith(holder)
  }
  return tpl.innerHTML
}

async function load() {
  error.value = ''
  if (!props.path) {
    mode.value = 'empty'
    return
  }
  let content = ''
  try {
    content = await ReadWorkspaceFile(props.path)
  } catch (e) {
    error.value = String(e)
    mode.value = 'empty'
    return
  }
  if (props.path.endsWith('.md')) {
    mode.value = 'md'
    html.value = mdToHtml(content)
    await renderMermaidBlocks()
  } else if (props.path.endsWith('.mmd')) {
    mode.value = 'mmd'
    const id = `mmd-${++renderSeq}`
    try {
      const { svg } = await mermaid.render(id, content)
      html.value = svg
    } catch (e) {
      error.value = `mermaid: ${String(e)}`
      html.value = ''
    }
  } else {
    mode.value = 'text'
    plain.value = content
  }
}

watch(() => props.path, load)
onMounted(() => {
  void load()
  EventsOn('diagram:changed', () => { // 開啟中的檔案變更 → 重渲染
    if (props.path.endsWith('.mmd') || props.path.endsWith('.md')) void load()
  })
})
</script>

<template>
  <div ref="paneEl" class="preview">
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="mode === 'empty' && !error" class="hint">在左側選擇檔案以預覽</div>
    <pre v-if="mode === 'text'" class="plain">{{ plain }}</pre>
    <div v-if="mode === 'md' || mode === 'mmd'" class="rendered" v-html="html" />
  </div>
</template>

<style scoped>
.preview { height: 100%; overflow-y: auto; padding: 12px 16px; text-align: left; }
.err { color: #ff8a80; }
.hint { color: #66788a; }
.plain { white-space: pre-wrap; word-break: break-all; font-size: 13px; }
.rendered :deep(pre) { background: #101820; padding: 8px; border-radius: 6px; overflow-x: auto; }
.rendered :deep(a) { color: #7aa2c4; }
.rendered :deep(.mmd-err) { color: #ff8a80; }
</style>
