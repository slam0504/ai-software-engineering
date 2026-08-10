<script setup lang="ts">
import { computed } from 'vue'
import { PROVIDERS, useSession } from '../stores/session'
import {
  TerminateSession, EndSession, NewSession, AuthStatus, StartLogin, CancelLogin, Logout,
  RestartCodexServerRecorded,
} from '../../wailsjs/go/main/App'

const s = useSession()

// per-view 輸入欄位的 v-model 包裝（store getter 唯讀，寫入走 action）
const resumeInput = computed({ get: () => s.resumeInput, set: (v: string) => s.setResumeInput(v) })
const taskLabel = computed({ get: () => s.taskLabel, set: (v: string) => s.setTaskLabel(v) })
const recordCase = computed({ get: () => s.recordCase, set: (v: string) => s.setRecordCase(v) })


async function call(fn: () => Promise<unknown>, label: string) {
  try {
    const r = await fn()
    s.note(`${label} ok${typeof r === 'string' && r ? '：' + r.slice(0, 400) : ''}`)
  } catch (e: any) {
    s.pushError(`${label}: ${e}`)
  }
}
</script>

<template>
  <div class="settings">
    <nav class="tabs" role="tablist">
      <button v-for="p in PROVIDERS" :key="p" role="tab"
        :class="['tab', { active: s.activeProvider === p }]"
        :aria-selected="s.activeProvider === p"
        @click="s.setActiveProvider(p)">
        {{ p }}
        <span v-if="s.unreadOf(p) > 0" class="badge">{{ s.unreadOf(p) }}</span>
        <span v-if="s.awaitingOf(p)" class="await" title="等待核可">⚠</span>
      </button>
    </nav>
    <input v-model="taskLabel" class="w-160" placeholder="任務標籤（task id）" />
    <select v-if="s.provider === 'codex'" v-model="s.approvalPolicy" title="codex approvalPolicy">
      <option value="untrusted">untrusted（每次核可）</option>
      <option value="on-request">on-request</option>
      <option value="never" class="danger">never（不核可，風險自負）</option>
    </select>
    <input v-model="recordCase" class="w-160" :placeholder="s.provider + '-case（錄流，可空）'" />
    <input v-model="resumeInput" class="w-200" placeholder="resume id（可空）" />
    <button title="結束目前 session（quiesce 舊 provider）後開新對話"
      @click="call(async () => { await NewSession(s.provider); s.reset() }, 'new')">New</button>
    <!-- NewSession 原子流程（收尾＋恢復視窗重設）；失敗由 call() 顯示且不 reset -->

    <button @click="call(() => TerminateSession(s.provider), 'terminate')">Terminate</button>
    <button @click="call(() => EndSession(s.provider), 'end')">End</button>
    <span class="spacer" />
    <button @click="call(() => AuthStatus(s.provider), 'auth')">Auth</button>
    <button @click="call(() => StartLogin(s.provider), 'login')">Login</button>
    <button v-if="s.provider === 'codex'" @click="call(() => CancelLogin(s.provider), 'cancel-login')">Cancel</button>
    <button @click="call(() => Logout(s.provider), 'logout')">Logout</button>
    <button v-if="s.provider === 'codex'" @click="call(() => RestartCodexServerRecorded(s.recordCase || 'codex-handshake'), 'b1-probe')">B1</button>
  </div>
</template>

<style scoped>
.settings { display: flex; gap: 6px; padding: 6px 8px; align-items: center; flex-wrap: wrap; }
.tabs { display: flex; gap: 2px; border: 1px solid var(--border); border-radius: var(--radius-s); overflow: hidden; }
.tab { background: var(--bg-inset); color: var(--text-muted); border: none; padding: var(--space-1) var(--space-3); font-size: var(--fs-m); cursor: pointer; display: flex; align-items: center; gap: var(--space-1); }
.tab.active { background: var(--bg-bubble-user); color: var(--text); }
.tab .await { color: var(--warn); }
.w-160 { width: 160px; }
.w-200 { width: 200px; }
.spacer { flex: 1; }
.danger { color: var(--err); }
</style>
