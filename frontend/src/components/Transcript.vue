<script setup lang="ts">
// spike quality: to be rebuilt in M1
import { ref, nextTick } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'

interface UIEvent {
  provider: string
  kind: string
  sessionId?: string
  text?: string
  thinking?: string
  isError?: boolean
  costUsd?: number
  raw?: string
  error?: string
}

interface Row {
  kind: string
  provider: string
  text: string
  raw: string
  open: boolean
}

const rows = ref<Row[]>([])
const streamText = ref('')
const streamThinking = ref('')
const done = ref<any | null>(null)
const listEl = ref<HTMLElement | null>(null)

function push(kind: string, provider: string, text: string, raw = '') {
  rows.value.push({ kind, provider, text, raw, open: false })
  nextTick(() => { listEl.value?.scrollTo({ top: listEl.value.scrollHeight }) })
}

EventsOn('bridge:event', (ev: UIEvent) => {
  switch (ev.kind) {
    case 'delta':
      if (ev.text) streamText.value += ev.text
      if (ev.thinking) streamThinking.value += ev.thinking
      break
    case 'init':
      push('init', ev.provider, `session ${ev.sessionId ?? '?'}`, ev.raw ?? '')
      break
    case 'tool_use':
      push('tool_use', ev.provider, ev.text || '(tool activity)', ev.raw ?? '')
      break
    case 'message':
      if (streamText.value || streamThinking.value) {
        streamText.value = ''
        streamThinking.value = ''
      }
      push('message', ev.provider, ev.text || '(structured message)', ev.raw ?? '')
      break
    case 'result':
      push('result', ev.provider,
        `${ev.isError ? 'ERROR' : 'ok'} cost=$${(ev.costUsd ?? 0).toFixed(4)}`, ev.raw ?? '')
      streamText.value = ''
      streamThinking.value = ''
      break
    case 'retry':
      push('retry', ev.provider, 'provider 重試中', ev.raw ?? '')
      break
    case 'approval':
      push('approval', ev.provider, '核可請求', ev.raw ?? '')
      break
    case 'stream_error':
    case 'malformed':
    case 'recorder_error':
      push(ev.kind, ev.provider ?? '?', ev.error || '(stream problem)', ev.raw ?? '')
      break
    case 'unknown':
    case 'system_other':
      push(ev.kind, ev.provider, ev.kind, ev.raw ?? '')
      break
  }
})

EventsOn('session:done', (d: any) => { done.value = d })
</script>

<template>
  <div class="transcript">
    <div ref="listEl" class="list">
      <div v-for="(r, i) in rows" :key="i" :class="['row', r.kind]">
        <span class="tag">[{{ r.provider }}·{{ r.kind }}]</span>
        <span class="text">{{ r.text }}</span>
        <button v-if="r.raw" class="rawbtn" @click="r.open = !r.open">{{ r.open ? '隱藏' : 'raw' }}</button>
        <pre v-if="r.open" class="raw">{{ r.raw }}</pre>
      </div>
      <details v-if="streamThinking" class="thinking" open>
        <summary>thinking</summary>
        <pre>{{ streamThinking }}</pre>
      </details>
      <pre v-if="streamText" class="stream">{{ streamText }}</pre>
    </div>
    <div v-if="done" class="done">
      session done：
      <span v-if="done.exitCode !== undefined">exit={{ done.exitCode }}</span>
      <span v-if="done.turnStatus">turn={{ done.turnStatus }}</span>
      <span v-if="done.processStillRunning">（server 續跑）</span>
      <span v-if="done.error" class="err">error: {{ done.error }}</span>
      <span v-if="done.recorderError" class="err">recorder: {{ done.recorderError }}</span>
      <details v-if="done.stderrTail"><summary>stderr</summary><pre>{{ done.stderrTail }}</pre></details>
    </div>
  </div>
</template>

<style scoped>
.transcript { display: flex; flex-direction: column; height: 100%; text-align: left; }
.list { flex: 1; overflow-y: auto; padding: 8px; }
.row { margin: 2px 0; font-size: 13px; }
.tag { color: #7aa2c4; margin-right: 6px; }
.row.result .text { font-weight: 600; }
.row.stream_error .text, .row.malformed .text, .row.recorder_error .text { color: #ff8a80; }
.rawbtn { margin-left: 8px; font-size: 11px; }
.raw, .stream, .thinking pre {
  background: #101820; padding: 6px; border-radius: 4px;
  white-space: pre-wrap; word-break: break-all; font-size: 12px;
}
.done { border-top: 1px solid #3a4a5a; padding: 6px 8px; font-size: 13px; }
.done .err { color: #ff8a80; margin-left: 8px; }
</style>
