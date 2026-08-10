<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ListWorkspace } from '../../wailsjs/go/main/App'

interface FileNode { name: string; path: string; isDir: boolean }

// 懶載入樹：每層是一個 FileTree（檔名自遞迴）；子層在展開時才 mount → 才 List。
const props = defineProps<{ rel?: string }>()
const emit = defineEmits<{ (e: 'select', path: string): void }>()

const nodes = ref<FileNode[]>([])
const open = ref(new Set<string>())
const error = ref('')

onMounted(async () => {
  try {
    nodes.value = (await ListWorkspace(props.rel ?? '')) ?? []
  } catch (e) {
    error.value = String(e)
  }
})

function toggle(p: string) {
  if (open.value.has(p)) open.value.delete(p)
  else open.value.add(p)
}
</script>

<template>
  <ul class="tree">
    <li v-if="error" class="err">{{ error }}</li>
    <li v-for="n in nodes" :key="n.path">
      <button v-if="n.isDir" class="dir" @click="toggle(n.path)">
        {{ open.has(n.path) ? '▾' : '▸' }} {{ n.name }}
      </button>
      <button v-else class="file" @click="emit('select', n.path)">{{ n.name }}</button>
      <FileTree v-if="n.isDir && open.has(n.path)" :rel="n.path"
        @select="(p: string) => emit('select', p)" />
    </li>
  </ul>
</template>

<style scoped>
.tree { list-style: none; margin: 0; padding-left: 12px; font-size: 13px; text-align: left; }
button { background: none; border: none; color: var(--text); cursor: pointer; padding: 2px 4px; font-size: 13px; }
button.dir { color: var(--text-muted); }
button:hover { color: #fff; }
.err { color: var(--err); font-size: 12px; padding: 4px; list-style: none; }
</style>
