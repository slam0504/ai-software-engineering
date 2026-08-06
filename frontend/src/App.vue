<script setup lang="ts">
// spike quality: to be rebuilt in M1
import { ref, onMounted } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import {
  StartSession, TerminateSession, AuthStatus, StartLogin, Logout,
  RestartCodexServerRecorded, CLIInfo,
} from '../wailsjs/go/main/App'
import Transcript from './components/Transcript.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'
import MermaidPane from './components/MermaidPane.vue'

const provider = ref<'claude' | 'codex'>('claude')
const prompt = ref('')
const recordCase = ref('')
const resume = ref('')
const sessionId = ref('')
const tab = ref<'transcript' | 'mermaid'>('transcript')
const authInfo = ref('')
const statusMsg = ref('')
const cliInfo = ref<Record<string, string>>({})

EventsOn('bridge:event', (ev: any) => {
  if (ev.kind === 'init' && ev.sessionId) sessionId.value = ev.sessionId
})
EventsOn('auth:status', (s: any) => {
  statusMsg.value = `auth[${s.provider}]: ${s.event ?? ''} ${s.authUrl ?? ''}`
})

async function start() {
  statusMsg.value = ''
  try {
    await StartSession(provider.value, prompt.value,
      provider.value === 'claude' ? resume.value : '', recordCase.value)
    statusMsg.value = 'session started'
  } catch (e: any) {
    statusMsg.value = `start failed: ${e}`
  }
}

async function terminate() {
  try {
    await TerminateSession()
    statusMsg.value = 'terminate sent'
  } catch (e: any) {
    statusMsg.value = `terminate failed: ${e}`
  }
}

async function refreshAuth() {
  try {
    authInfo.value = await AuthStatus(provider.value)
  } catch (e: any) {
    authInfo.value = `auth status failed: ${e}`
  }
}

async function login() {
  try {
    await StartLogin(provider.value)
  } catch (e: any) {
    statusMsg.value = `login failed: ${e}`
  }
}

async function logout() {
  try {
    await Logout(provider.value)
    await refreshAuth()
  } catch (e: any) {
    statusMsg.value = `logout failed: ${e}`
  }
}

async function probeHandshake() {
  try {
    await RestartCodexServerRecorded(recordCase.value || 'codex-handshake')
    statusMsg.value = 'codex handshake probe ok'
  } catch (e: any) {
    statusMsg.value = `probe failed: ${e}`
  }
}

onMounted(async () => {
  try { cliInfo.value = await CLIInfo() } catch { /* dev 模式無綁定時忽略 */ }
})
</script>

<template>
  <div class="shell">
    <header>
      <select v-model="provider">
        <option value="claude">claude</option>
        <option value="codex">codex</option>
      </select>
      <input v-model="prompt" class="prompt" placeholder="prompt" @keyup.enter="start" />
      <input v-model="recordCase" class="rec" :placeholder="provider + '-case（錄流，可空）'" />
      <input v-if="provider === 'claude'" v-model="resume" class="rec" placeholder="resume session id（可空）" />
      <button @click="start">Start</button>
      <button @click="terminate">Terminate</button>
      <button v-if="provider === 'codex'" @click="probeHandshake">B1 Probe</button>
    </header>
    <header class="second">
      <span class="sid">session: {{ sessionId || '—' }}</span>
      <button @click="refreshAuth">Auth Status</button>
      <button @click="login">Login</button>
      <button @click="logout">Logout</button>
      <span class="cli" :title="JSON.stringify(cliInfo)">
        tools: {{ cliInfo.toolsSource }} @ {{ cliInfo.toolsDir }} | node {{ cliInfo.node }}
      </span>
    </header>
    <div v-if="authInfo" class="auth"><pre>{{ authInfo }}</pre></div>
    <div v-if="statusMsg" class="status">{{ statusMsg }}</div>
    <nav>
      <button :class="{ active: tab === 'transcript' }" @click="tab = 'transcript'">Transcript</button>
      <button :class="{ active: tab === 'mermaid' }" @click="tab = 'mermaid'">Mermaid</button>
    </nav>
    <main>
      <Transcript v-show="tab === 'transcript'" />
      <MermaidPane v-show="tab === 'mermaid'" />
    </main>
    <ApprovalDialog />
  </div>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
body { background: #1b2636; color: #e6edf3; font-family: ui-sans-serif, system-ui, sans-serif; }
</style>

<style scoped>
.shell { display: flex; flex-direction: column; height: 100vh; }
header { display: flex; gap: 6px; padding: 8px; align-items: center; }
header.second { padding-top: 0; font-size: 12px; }
.prompt { flex: 1; padding: 6px; }
.rec { width: 200px; padding: 6px; }
.sid { color: #7aa2c4; margin-right: 8px; }
.cli { color: #66788a; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.auth pre { text-align: left; font-size: 12px; background: #101820; margin: 0 8px; padding: 6px; max-height: 140px; overflow: auto; }
.status { color: #ffd54f; font-size: 13px; padding: 2px 8px; text-align: left; }
nav { display: flex; gap: 4px; padding: 4px 8px; }
nav .active { background: #2d5a88; color: #fff; }
main { flex: 1; min-height: 0; }
</style>
