<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { PROVIDERS, useSession } from '../stores/session'
import type { ProviderKey } from '../stores/session'
import {
  TerminateSession, EndSession, NewSession, CreateSession, AuthStatus, StartLogin, CancelLogin,
  Logout, RestartCodexServerRecorded,
} from '../../wailsjs/go/main/App'

const { t } = useI18n()
const s = useSession()

// Task 26：session 分頁取代原本的 provider 分頁——lifecycle binding 已改為 WSID
// 定址，「目前操作哪一個 session」因此必須是明確的 WSID 而非 provider。
// 正式的 SessionList（§4）與雙 pane 是 Task 27／28，這裡維持既有 SettingsBar
// 形狀做最小接線：點分頁＝把該 session 釘進 focused pane。
const wsid = computed(() => s.focusedWsid)

function selectSession(w: string) {
  const at = s.pins.indexOf(w)
  if (at === 0 || at === 1) s.setFocus(at)
  else s.pin(s.focused, w)
}

async function createSession(p: ProviderKey) {
  await call(async () => {
    const w = await CreateSession(p, s.taskLabel)
    s.registerSession({ wsid: w, provider: p, taskLabel: s.taskLabel })
    s.pin(s.focused, w)
    return w
  }, 'create')
}

// per-view 輸入欄位的 v-model 包裝（store getter 唯讀，寫入走 action）
const resumeInput = computed({ get: () => s.resumeInput, set: (v: string) => s.setResumeInput(v) })
const taskLabel = computed({ get: () => s.taskLabel, set: (v: string) => s.setTaskLabel(v) })
const recordCase = computed({ get: () => s.recordCase, set: (v: string) => s.setRecordCase(v) })

// opKey ∈ settings.operationAction 的 key（new/terminate/end/authStatus/login/cancelLogin/logout/b1Probe）
async function call(fn: () => Promise<unknown>, opKey: string) {
  const action = t(`settings.operationAction.${opKey}`)
  try {
    const r = await fn()
    if (typeof r === 'string' && r) {
      s.note(t('settings.operation.successDetail', { action, detail: r.slice(0, 400) }))
    } else {
      s.note(t('settings.operation.success', { action }))
    }
  } catch (e: any) {
    s.pushError(t('settings.operation.failure', { action, error: String(e) }))
  }
}
</script>

<template>
  <div class="settings">
    <nav class="tabs" role="tablist">
      <button v-for="m in s.sessionList" :key="m.wsid" role="tab"
        :class="['tab', { active: m.wsid === wsid, unavailable: !m.available }]"
        :aria-selected="m.wsid === wsid" :data-test="'session-tab-' + m.wsid"
        :title="m.available ? m.wsid : t('store.sessionUnavailable', { wsid: m.wsid })"
        @click="selectSession(m.wsid)">
        {{ m.provider }}{{ m.taskLabel ? ' · ' + m.taskLabel : '' }}
        <span v-if="s.unreadOf(m.wsid) > 0" class="badge">{{ s.unreadOf(m.wsid) }}</span>
        <span v-if="s.awaitingOf(m.wsid)" class="await" :title="t('settings.awaitingApproval.tooltip')">⚠</span>
      </button>
      <button v-for="p in PROVIDERS" :key="'new-' + p" class="tab create"
        :data-test="'create-' + p" :title="t('settings.createSession.tooltip', { provider: p })"
        @click="createSession(p)">+{{ p }}</button>
    </nav>
    <input v-model="taskLabel" class="w-160" :placeholder="t('settings.taskId.placeholder')" />
    <select v-if="s.provider === 'codex'" v-model="s.approvalPolicy" :title="t('settings.approvalPolicy.tooltip')">
      <option value="untrusted">{{ t('settings.approvalPolicy.untrusted') }}</option>
      <option value="on-request">{{ t('settings.approvalPolicy.onRequest') }}</option>
      <option value="never" class="danger">{{ t('settings.approvalPolicy.never') }}</option>
    </select>
    <!-- recordCase：§3.4.4 之後 codex 的錄流是 connection-wide（一個 Conn 一份
         wire log），這個欄位對 codex 只剩 audit label、不再控制任何錄製行為，
         因此在 codex session 上停用並說明，避免使用者以為填了就會錄。 -->
    <input v-model="recordCase" class="w-160" :disabled="s.provider === 'codex'"
      :title="s.provider === 'codex' ? t('settings.recordCase.codexLabelOnly') : ''"
      :placeholder="s.provider === 'codex' ? t('settings.recordCase.codexLabelOnly')
        : t('settings.recordCase.placeholder', { provider: s.provider })" />
    <input v-model="resumeInput" class="w-200" :placeholder="t('settings.resumeId.placeholder')" />
    <button :title="t('settings.newSession.tooltip')"
      :disabled="!wsid"
      @click="call(async () => { await NewSession(wsid); s.reset() }, 'new')">{{ t('settings.action.new') }}</button>
    <!-- NewSession 原子流程（收尾＋恢復視窗重設）；失敗由 call() 顯示且不 reset -->

    <button :disabled="!wsid" @click="call(() => TerminateSession(wsid), 'terminate')">{{ t('settings.action.terminate') }}</button>
    <button :disabled="!wsid" data-test="end-session" @click="call(() => EndSession(wsid), 'end')">{{ t('settings.action.end') }}</button>
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
.tab.unavailable { color: var(--err); text-decoration: line-through; }
.tab.create { color: var(--text-faint); }
.tab .await { color: var(--warn); }
.w-160 { width: 160px; }
.w-200 { width: 200px; }
.spacer { flex: 1; }
.danger { color: var(--err); }
</style>
