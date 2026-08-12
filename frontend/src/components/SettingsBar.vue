<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { PROVIDERS, useSession } from '../stores/session'
import {
  TerminateSession, EndSession, NewSession, AuthStatus, StartLogin, CancelLogin, Logout,
  RestartCodexServerRecorded,
} from '../../wailsjs/go/main/App'

const { t } = useI18n()
const s = useSession()

// per-view 輸入欄位的 v-model 包裝（store getter 唯讀，寫入走 action）
const resumeInput = computed({ get: () => s.resumeInput, set: (v: string) => s.setResumeInput(v) })
const taskLabel = computed({ get: () => s.taskLabel, set: (v: string) => s.setTaskLabel(v) })
const recordCase = computed({ get: () => s.recordCase, set: (v: string) => s.setRecordCase(v) })

// opKey ∈ settings.operationAction 的 key（new/terminate/end/authStatus/login/cancelLogin/logout/b1Probe）
async function call(fn: () => Promise<unknown>, opKey: string) {
  const action = t('settings.operationAction.' + opKey)
  try {
    await fn()
    s.note(t('settings.operation.success', { action }))
  } catch (e: any) {
    s.note(t('settings.operation.failure', { action, error: String(e) }))
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
        <span v-if="s.awaitingOf(p)" class="await" :title="t('settings.awaitingApproval.tooltip')">⚠</span>
      </button>
    </nav>
    <input v-model="taskLabel" class="w-160" :placeholder="t('settings.taskId.placeholder')" />
    <select v-if="s.provider === 'codex'" v-model="s.approvalPolicy" :title="t('settings.approvalPolicy.tooltip')">
      <option value="untrusted">{{ t('settings.approvalPolicy.untrusted') }}</option>
      <option value="on-request">{{ t('settings.approvalPolicy.onRequest') }}</option>
      <option value="never" class="danger">{{ t('settings.approvalPolicy.never') }}</option>
    </select>
    <input v-model="recordCase" class="w-160" :placeholder="t('settings.recordCase.placeholder', { provider: s.provider })" />
    <input v-model="resumeInput" class="w-200" :placeholder="t('settings.resumeId.placeholder')" />
    <button :title="t('settings.newSession.tooltip')"
      @click="call(async () => { await NewSession(s.provider); s.reset() }, 'new')">{{ t('settings.action.new') }}</button>
    <!-- NewSession 原子流程（收尾＋恢復視窗重設）；失敗由 call() 顯示且不 reset -->

    <button @click="call(() => TerminateSession(s.provider), 'terminate')">{{ t('settings.action.terminate') }}</button>
    <button @click="call(() => EndSession(s.provider), 'end')">{{ t('settings.action.end') }}</button>
    <span class="spacer" />
    <button @click="call(() => AuthStatus(s.provider), 'authStatus')">{{ t('settings.action.authStatus') }}</button>
    <button @click="call(() => StartLogin(s.provider), 'login')">{{ t('settings.action.login') }}</button>
    <button v-if="s.provider === 'codex'" @click="call(() => CancelLogin(s.provider), 'cancelLogin')">{{ t('settings.action.cancelLogin') }}</button>
    <button @click="call(() => Logout(s.provider), 'logout')">{{ t('settings.action.logout') }}</button>
    <button v-if="s.provider === 'codex'" @click="call(() => RestartCodexServerRecorded(s.recordCase || 'codex-handshake'), 'b1Probe')">{{ t('settings.action.b1Probe') }}</button>
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
