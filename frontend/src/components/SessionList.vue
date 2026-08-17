<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CreateSession, RemoveSession } from '../../wailsjs/go/main/App'
import { resolveState, sessionStateKeys } from '../i18n/stateKeys'
import { PROVIDERS, useSession } from '../stores/session'
import type { ProviderKey } from '../stores/session'

// SessionList（Task 27，spec §4）：左欄既有 session 清單——只畫真的存在的
// session（s.sessionList 已濾除 tombstone），不畫固定 8 張空卡；per-provider
// n/4 計數＋達上限停用建立；每卡：provider／task label／狀態／unread／busy／
// 待核可標記，操作：釘選至 pane、移除（二段式確認，說明稽核事件與錄流永久
// 保留）。
//
// MAX_SESSIONS_PER_PROVIDER：鏡射 Go 端 appcore.MaxSessionsPerProvider（凍結
// 常數，M3b §3.1.4，明確不進 config／不讀環境變數）。兩邊沒有共用型別可以跨
// 語言匯入，這裡各自持有同一個字面值；上限變動需要兩邊同步改。
const MAX_SESSIONS_PER_PROVIDER = 4

const { t } = useI18n()
const s = useSession()

// pin：把 session 釘進目前 focused pane（已釘選過→切 focus，不重新釘一次，
// 沿用 SettingsBar.selectSession 的既定行為）。
function pin(wsid: string) {
  const at = s.pins.indexOf(wsid)
  if (at === 0 || at === 1) s.setFocus(at)
  else s.pin(s.focused, wsid)
}

async function createSession(p: ProviderKey) {
  try {
    const w = await CreateSession(p, '')
    s.registerSession({ wsid: w, provider: p, taskLabel: '' })
    s.pin(s.focused, w)
  } catch (e) {
    // pushNotice（app-wide lane）而非 pushError：建立失敗根本還沒有 WSID，
    // 而 pushError 會落到 `focusedWsid`——把訊息掛到一個**不相干**的 session
    // 上，並且順手清掉那個 session 的 busy（它可能正在 streaming）。可見性不是
    // 問題（focused view 依定義看得到），錯的是歸屬與那個 busy 副作用。
    //
    // 已知未收斂：SettingsBar 的建立按鈕仍走 `call()` → pushError，同一個
    // 副作用還在（登記給後續，不在本票範圍）。
    s.pushNotice(t('sessionList.create.failed', { provider: p, error: String(e) }))
  }
}

// 移除：兩段式（先問、再送）。
//
// 帶入事項 1（M3b Task 26 review round-2 Important，登記給本 task）：
// RemoveSession 六步凍結順序把 tombstone_persist 放在 decrement_count 之前，
// 兩者間有一道尚未收斂的 TOCTOU 窗口——tombstone 可能已落盤但 Manager 釋放
// 名額失敗，回傳「已 tombstone 但釋放名額失敗」。徹底收斂需要新增 removing
// phase（動 phase 狀態機），超出本張票範圍（見 app.go RemoveSession doc）。
// 這裡能做、也必須做的：**據實呈現**——錯誤原文直接顯示給使用者（fail
// loud），不吞掉、不裝作成功。失敗時**不**呼叫 markRemoved：store 端的
// sessionList 因此繼續列出這筆，使用者不會誤以為移除已完成；下一次
// ListSessions() 重新整理（App.vue 下次 hydrate）才會依 registry 真實狀態
// 收斂。
//
// 失敗訊息走 pushNotice（app-wide lane），理由見 confirmRemove 內的註解。
const confirmTarget = ref<string | null>(null)
function askRemove(wsid: string) { confirmTarget.value = wsid }
function cancelRemove() { confirmTarget.value = null }
async function confirmRemove(wsid: string) {
  confirmTarget.value = null
  try {
    await RemoveSession(wsid)
    s.markRemoved(wsid)
  } catch (e) {
    // rev3 review Critical：這裡曾經是 pushError(msg, wsid, true)，寫進
    // `views[wsid].timeline`——而 timeline getter **只讀 focused view**。使用者
    // 可以對清單上**任何**一個 session 按移除，包括沒被釘選／不在 focus 的那些，
    // 所以失敗訊息會落進一個畫面上根本沒顯示的 view：畫面上只剩「卡片沒消失」，
    // 沒有任何說明。改走 app-wide 的 notices lane（訊息本身已帶 wsid）。
    //
    // 一併解掉 busy 的問題：這條路徑從沒把 busy 設成 true，而 pushError 會無條件
    // 清掉該 session 的 busy——對一個 streaming 中的 session 按移除（Removable()
    // 在最前面就擋下、完全沒碰後端狀態），busy-dot 會被關掉，讀起來像已經閒置。
    // pushNotice 從頭到尾不碰 busy，所以這件事變成結構上成立，不再靠傳對參數。
    s.pushNotice(t('sessionList.remove.failed', { wsid, error: String(e) }))
  }
}
</script>

<template>
  <div class="session-list">
    <div class="counts">
      <div v-for="p in PROVIDERS" :key="p" class="count-row">
        <span class="provider-name">{{ p }}</span>
        <span class="count" :data-test="'count-' + p">{{ s.countOf(p) }} / {{ MAX_SESSIONS_PER_PROVIDER }}</span>
        <button
          type="button" class="create" :data-test="'create-' + p"
          :disabled="s.countOf(p) >= MAX_SESSIONS_PER_PROVIDER"
          :title="t('settings.createSession.tooltip', { provider: p })"
          @click="createSession(p)"
        >{{ t('sessionList.action.create') }}</button>
      </div>
    </div>

    <p v-if="s.sessionList.length === 0" class="empty" data-test="empty">{{ t('sessionList.empty') }}</p>

    <div
      v-for="m in s.sessionList" :key="m.wsid" class="card" data-test="session-card"
      :class="{ unavailable: !m.available }"
    >
      <div class="head">
        <span class="provider">{{ m.provider }}</span>
        <span v-if="m.taskLabel" class="label">{{ m.taskLabel }}</span>
        <span :class="['state', m.state]">{{ resolveState(sessionStateKeys, m.state, t) }}</span>
      </div>
      <div class="badges">
        <span v-if="m.busy" class="busy-dot" :data-test="'busy-' + m.wsid" :title="t('sessionList.busyTooltip')" />
        <span v-if="s.unreadOf(m.wsid) > 0" class="unread" :data-test="'unread-' + m.wsid">{{ s.unreadOf(m.wsid) }}</span>
        <span
          v-if="s.awaitingOf(m.wsid)" class="await" :data-test="'awaiting-' + m.wsid"
          :title="t('settings.awaitingApproval.tooltip')"
        >⚠</span>
      </div>
      <p v-if="!m.available" class="err" :data-test="'unavailable-' + m.wsid">
        {{ t('store.sessionUnavailable', { wsid: m.wsid }) }}
      </p>
      <div class="actions">
        <button type="button" :data-test="'pin-' + m.wsid" @click="pin(m.wsid)">{{ t('sessionList.action.pin') }}</button>
        <button type="button" :data-test="'remove-' + m.wsid" @click="askRemove(m.wsid)">{{ t('sessionList.action.remove') }}</button>
      </div>
      <div v-if="confirmTarget === m.wsid" class="confirm" data-test="remove-confirm">
        <p>{{ t('sessionList.remove.confirmText') }}</p>
        <button type="button" data-test="remove-confirm-submit" @click="confirmRemove(m.wsid)">
          {{ t('sessionList.remove.confirmSubmit') }}
        </button>
        <button type="button" data-test="remove-cancel" @click="cancelRemove">{{ t('sessionList.remove.cancel') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.session-list { display: flex; flex-direction: column; gap: var(--space-2); padding: var(--space-2); overflow-y: auto; height: 100%; }
.counts { display: flex; flex-direction: column; gap: 4px; margin-bottom: var(--space-2); }
.count-row { display: flex; align-items: center; gap: var(--space-2); font-size: var(--fs-s); }
.provider-name { color: var(--text-muted); width: 48px; }
.count { color: var(--text-faint); }
.create { margin-left: auto; }
.empty { color: var(--text-faint); font-size: var(--fs-s); }
.card { border: 1px solid var(--border); border-radius: var(--radius-s); padding: var(--space-2); display: flex; flex-direction: column; gap: 4px; background: var(--bg-panel); }
.card.unavailable { border-color: var(--err); }
.head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.provider { font-weight: 600; }
.label { color: var(--text-muted); font-size: var(--fs-s); }
.state { margin-left: auto; font-size: var(--fs-s); color: var(--text-faint); }
.state.awaiting_approval { color: var(--warn); }
.state.failed { color: var(--err); }
.badges { display: flex; align-items: center; gap: 6px; }
.busy-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); display: inline-block; }
.unread { background: var(--accent); color: #10201e; border-radius: 10px; padding: 0 6px; font-size: var(--fs-s); }
.await { color: var(--warn); }
.err { color: var(--err); font-size: var(--fs-s); overflow-wrap: anywhere; }
.actions { display: flex; gap: 6px; }
.confirm { display: flex; flex-direction: column; gap: 4px; border-top: 1px solid var(--border); padding-top: 4px; }
.confirm p { margin: 0; font-size: var(--fs-s); color: var(--text-muted); }
</style>
