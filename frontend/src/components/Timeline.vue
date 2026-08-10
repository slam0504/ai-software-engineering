<script setup lang="ts">
import { computed, ref } from 'vue'
import { useSession } from '../stores/session'
import type { TimelineItem } from '../types'

const s = useSession()
const openGroups = ref(new Set<number>())
const openRaw = ref(new Set<string>())

const rows = computed(() => {
  const out: Array<{ head: TimelineItem; count: number; group?: number }> = []
  for (const item of s.timeline) {
    const last = out.at(-1)
    if (item.group !== undefined && last && last.group === item.group) { last.count++; continue }
    out.push({ head: item, count: 1, group: item.group })
  }
  return out
})
function groupItems(g: number) { return s.timeline.filter(i => i.group === g) }
function summary(i: TimelineItem) {
  const e = i.env
  if (e.kind === 'tool_use') { // BAT AgentToolRow（normative）：工具名＋參數節錄＋狀態
    const status = (e.raw as any)?.params?.item?.status // codex item 狀態，best-effort
    const label = e.text || '工具呼叫' // adapter 已填「名稱(參數節錄)」（Task 2）
    return status ? `${label}（${status}）` : label
  }
  if (e.kind === 'result') return `${e.is_error ? 'ERROR' : 'ok'}`
  if (e.kind === 'approval') return `核可請求：${e.text}`
  if (e.kind === 'approval_decision') return `核可決定：${e.text}`
  if (e.kind === 'state_change') return `狀態 → ${e.state}`
  if (e.kind === 'retry') return 'provider 重試'
  if (e.kind === 'message' && e.role === 'tool') return '工具結果'
  if (e.error) return e.error
  return e.kind
}
function toggle(set: Set<number> | Set<string>, key: never) {
  ;(set.has(key) ? set.delete(key) : set.add(key))
}
</script>

<template>
  <div class="timeline">
    <template v-for="(r, idx) in rows" :key="idx">
      <div v-if="r.group !== undefined" class="row noise">
        <button @click="toggle(openGroups, r.group! as never)">
          {{ openGroups.has(r.group!) ? '▾' : '▸' }} 系統事件 ×{{ r.count }}
        </button>
        <div v-if="openGroups.has(r.group!)" class="noise-items">
          <div v-for="g in groupItems(r.group!)" :key="g.env.event_id" class="row sub">
            <span class="kind">{{ g.env.kind }}</span>
            <button class="rawbtn" @click="toggle(openRaw, g.env.event_id as never)">raw</button>
            <pre v-if="openRaw.has(g.env.event_id)">{{ JSON.stringify(g.env.raw, null, 1) }}</pre>
          </div>
        </div>
      </div>
      <div v-else :class="['row', r.head.env.kind]">
        <span class="kind">{{ r.head.env.kind }}</span>
        <span class="sum">{{ summary(r.head) }}</span>
        <button class="rawbtn" @click="toggle(openRaw, r.head.env.event_id as never)">raw</button>
        <pre v-if="openRaw.has(r.head.env.event_id)">{{ JSON.stringify(r.head.env.raw, null, 1) }}</pre>
      </div>
    </template>
  </div>
</template>

<style scoped>
.timeline { height: 100%; overflow-y: auto; padding: 6px 10px; font-size: 12px; text-align: left; }
.row { margin: 2px 0; }
.kind { color: var(--accent); margin-right: 8px; }
.row.tool_use .sum { color: var(--ok); }
.row.approval .sum, .row.approval_decision .sum { color: var(--warn); }
.row.result .sum { font-weight: 600; }
.row.retry .sum, .row.stream_error .sum { color: var(--err); }
.noise button { color: var(--text-faint); background: none; border: none; cursor: pointer; }
.rawbtn { font-size: 10px; margin-left: 6px; }
pre { background: var(--bg-inset); padding: 6px; border-radius: 4px; white-space: pre-wrap; word-break: break-all; }
.sub { margin-left: 18px; }
</style>
