<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { escalation } from '../../wailsjs/go/models'

const { t } = useI18n()

// EscalationInbox（Task 25，spec §3.8／§6）：升級收件匣——open／acknowledged
// 分區清單＋resolved 摺疊區、手動建立表單、ack／resolve 動作。同 GateConsole／
// TcaWorkspace 的既定慣例：entries／unavailable 走 props 注入（權威來源是
// App.vue 的 escalation store，見 stores/escalation.ts），ack／resolve／
// create／reload 也走 props 注入的 wails 函式——測試以 props 驅動，不依賴真實
// Wails binding，也不需要在測試裡起 Pinia。unavailable 非空時（EscalationList
// 失敗，即 Project 拒絕或 journal 壞掉）整個收件匣只顯示錯誤原文＋重試按鈕，
// 絕不裝空（§3.8：讀不到不能當成沒有 blocker）。
const props = defineProps<{
  entries: escalation.Entry[]
  unavailable: string
  ack: (id: string) => Promise<void>
  resolve: (id: string, resolution: string, reason: string) => Promise<void>
  create: (sourceRef: string, blockScope: string, summary: string) => Promise<string>
  reload: () => Promise<void>
}>()

const openEntries = computed(() => props.entries.filter(e => e.State === 'open'))
const ackEntries = computed(() => props.entries.filter(e => e.State === 'acknowledged'))
const resolvedEntries = computed(() => props.entries.filter(e => e.State === 'resolved'))
const resolvedExpanded = ref(false)

const actionError = ref('')
async function onAck(id: string) {
  actionError.value = ''
  try {
    await props.ack(id)
  } catch (e) {
    actionError.value = String(e)
  }
  await props.reload()
}

// resolve 表單：per escalation_id 獨立輸入，未選 resolution 或未填 reason 前
// 送出按鈕直接 disabled（不是 GateConsole reject 那種「點了才提示」，brief
// 明講要 disabled）。
const resolutionInput = ref<Record<string, string>>({})
const reasonInput = ref<Record<string, string>>({})
function resolveDisabled(id: string): boolean {
  return !(resolutionInput.value[id] && (reasonInput.value[id] ?? '').trim())
}
async function onResolve(id: string) {
  if (resolveDisabled(id)) return
  actionError.value = ''
  try {
    await props.resolve(id, resolutionInput.value[id], reasonInput.value[id])
    delete resolutionInput.value[id]
    delete reasonInput.value[id]
  } catch (e) {
    actionError.value = String(e)
  }
  await props.reload()
}

// 手動建立表單：sourceRef／summary 必填，未填時送出 disabled。blockScope 選填
// 下拉——workspace／gate2:<id>／tca:<plan>/<task>／自由輸入四選一（§3.8
// BlockScope 語意：workspace｜gate2:<id>｜tca:<plan>/<task>｜""＝非阻擋）。
const newSourceRef = ref('')
const newScopeKind = ref<'' | 'workspace' | 'gate2' | 'tca' | 'custom'>('')
const newScopeId = ref('')
const newSummary = ref('')
const createError = ref('')

function computedBlockScope(): string {
  switch (newScopeKind.value) {
    case 'workspace': return 'workspace'
    case 'gate2': return newScopeId.value ? `gate2:${newScopeId.value}` : ''
    case 'tca': return newScopeId.value ? `tca:${newScopeId.value}` : ''
    case 'custom': return newScopeId.value
    default: return ''
  }
}
function createDisabled(): boolean {
  return !newSourceRef.value.trim() || !newSummary.value.trim()
}
async function onCreate() {
  if (createDisabled()) return
  createError.value = ''
  try {
    await props.create(newSourceRef.value, computedBlockScope(), newSummary.value)
    newSourceRef.value = ''
    newScopeKind.value = ''
    newScopeId.value = ''
    newSummary.value = ''
  } catch (e) {
    createError.value = String(e)
  }
  await props.reload()
}
</script>

<template>
  <div class="escalation-inbox">
    <p v-if="unavailable" class="err" data-test="escalation-unavailable">{{ unavailable }}</p>
    <button v-if="unavailable" type="button" data-test="escalation-retry" @click="reload">
      {{ t('escalation.action.retry') }}
    </button>

    <template v-else>
      <div class="create-form" data-test="create-form">
        <h4>{{ t('escalation.create.title') }}</h4>
        <input v-model="newSourceRef" data-test="create-source-ref" :placeholder="t('escalation.create.sourceRefPlaceholder')" />
        <select v-model="newScopeKind" data-test="create-scope-select">
          <option value="">{{ t('escalation.create.scope.none') }}</option>
          <option value="workspace">{{ t('escalation.create.scope.workspace') }}</option>
          <option value="gate2">{{ t('escalation.create.scope.gate2') }}</option>
          <option value="tca">{{ t('escalation.create.scope.tca') }}</option>
          <option value="custom">{{ t('escalation.create.scope.custom') }}</option>
        </select>
        <input
          v-if="newScopeKind === 'gate2' || newScopeKind === 'tca'"
          v-model="newScopeId" data-test="create-scope-id" :placeholder="t('escalation.create.scopeIdPlaceholder')"
        />
        <input
          v-if="newScopeKind === 'custom'"
          v-model="newScopeId" data-test="create-scope-id" :placeholder="t('escalation.create.scopeCustomPlaceholder')"
        />
        <textarea v-model="newSummary" data-test="create-summary" :placeholder="t('escalation.create.summaryPlaceholder')" />
        <button type="button" data-test="create-submit" :disabled="createDisabled()" @click="onCreate">
          {{ t('escalation.create.submit') }}
        </button>
        <p v-if="createError" class="err" data-test="create-error">{{ createError }}</p>
      </div>

      <p v-if="actionError" class="err" data-test="action-error">{{ actionError }}</p>

      <section data-test="section-open">
        <h4>{{ t('escalation.section.open') }}</h4>
        <p v-if="openEntries.length === 0" class="empty" data-test="empty-open">{{ t('escalation.empty.open') }}</p>
        <div v-for="e in openEntries" :key="e.Item.escalation_id" class="entry" :data-test="'entry-' + e.Item.escalation_id">
          <div class="head">
            <span class="summary">{{ e.Item.summary }}</span>
            <span :class="['badge', 'badge-source-' + e.Item.source]" :data-test="'source-badge-' + e.Item.escalation_id">
              {{ e.Item.source === 'system' ? t('escalation.badge.source.system') : t('escalation.badge.source.manual') }}
            </span>
            <span v-if="e.Item.hard" class="badge badge-hard" :data-test="'hard-badge-' + e.Item.escalation_id">
              {{ t('escalation.badge.hard') }}
            </span>
          </div>
          <p class="meta">
            <span v-if="e.Item.block_scope">{{ t('escalation.label.blockScope') }}: {{ e.Item.block_scope }}</span>
            <span v-else>{{ t('escalation.label.noScope') }}</span>
            <span v-if="e.Item.source_ref">{{ t('escalation.label.sourceRef') }}: {{ e.Item.source_ref }}</span>
            <span v-if="e.Item.occurrence > 1" :data-test="'occurrence-' + e.Item.escalation_id">
              {{ t('escalation.badge.occurrence', { n: e.Item.occurrence }) }}
            </span>
          </p>

          <div class="actions">
            <button type="button" :data-test="'ack-' + e.Item.escalation_id" @click="onAck(e.Item.escalation_id)">
              {{ t('escalation.action.ack') }}
            </button>
          </div>

          <div v-if="e.Item.hard" class="hard-notice" :data-test="'hard-notice-' + e.Item.escalation_id">
            {{ t('escalation.hardNotice') }}
          </div>
          <div v-else class="resolve-form" :data-test="'resolve-form-' + e.Item.escalation_id">
            <select v-model="resolutionInput[e.Item.escalation_id]" :data-test="'resolve-resolution-' + e.Item.escalation_id">
              <option value="">{{ t('escalation.resolve.resolutionPlaceholder') }}</option>
              <option value="fixed">{{ t('escalation.resolve.resolution.fixed') }}</option>
              <option value="accepted_risk">{{ t('escalation.resolve.resolution.accepted_risk') }}</option>
              <option value="other">{{ t('escalation.resolve.resolution.other') }}</option>
            </select>
            <textarea
              v-model="reasonInput[e.Item.escalation_id]" :data-test="'resolve-reason-' + e.Item.escalation_id"
              :placeholder="t('escalation.resolve.reasonPlaceholder')"
            />
            <button
              type="button" :data-test="'resolve-submit-' + e.Item.escalation_id"
              :disabled="resolveDisabled(e.Item.escalation_id)" @click="onResolve(e.Item.escalation_id)"
            >{{ t('escalation.resolve.submit') }}</button>
          </div>
        </div>
      </section>

      <section data-test="section-acknowledged">
        <h4>{{ t('escalation.section.acknowledged') }}</h4>
        <p v-if="ackEntries.length === 0" class="empty" data-test="empty-acknowledged">{{ t('escalation.empty.acknowledged') }}</p>
        <div v-for="e in ackEntries" :key="e.Item.escalation_id" class="entry" :data-test="'entry-' + e.Item.escalation_id">
          <div class="head">
            <span class="summary">{{ e.Item.summary }}</span>
            <span :class="['badge', 'badge-source-' + e.Item.source]" :data-test="'source-badge-' + e.Item.escalation_id">
              {{ e.Item.source === 'system' ? t('escalation.badge.source.system') : t('escalation.badge.source.manual') }}
            </span>
            <span v-if="e.Item.hard" class="badge badge-hard" :data-test="'hard-badge-' + e.Item.escalation_id">
              {{ t('escalation.badge.hard') }}
            </span>
          </div>
          <p class="meta">
            <span v-if="e.Item.block_scope">{{ t('escalation.label.blockScope') }}: {{ e.Item.block_scope }}</span>
            <span v-else>{{ t('escalation.label.noScope') }}</span>
            <span v-if="e.Item.source_ref">{{ t('escalation.label.sourceRef') }}: {{ e.Item.source_ref }}</span>
            <span v-if="e.Item.occurrence > 1" :data-test="'occurrence-' + e.Item.escalation_id">
              {{ t('escalation.badge.occurrence', { n: e.Item.occurrence }) }}
            </span>
          </p>

          <div v-if="e.Item.hard" class="hard-notice" :data-test="'hard-notice-' + e.Item.escalation_id">
            {{ t('escalation.hardNotice') }}
          </div>
          <div v-else class="resolve-form" :data-test="'resolve-form-' + e.Item.escalation_id">
            <select v-model="resolutionInput[e.Item.escalation_id]" :data-test="'resolve-resolution-' + e.Item.escalation_id">
              <option value="">{{ t('escalation.resolve.resolutionPlaceholder') }}</option>
              <option value="fixed">{{ t('escalation.resolve.resolution.fixed') }}</option>
              <option value="accepted_risk">{{ t('escalation.resolve.resolution.accepted_risk') }}</option>
              <option value="other">{{ t('escalation.resolve.resolution.other') }}</option>
            </select>
            <textarea
              v-model="reasonInput[e.Item.escalation_id]" :data-test="'resolve-reason-' + e.Item.escalation_id"
              :placeholder="t('escalation.resolve.reasonPlaceholder')"
            />
            <button
              type="button" :data-test="'resolve-submit-' + e.Item.escalation_id"
              :disabled="resolveDisabled(e.Item.escalation_id)" @click="onResolve(e.Item.escalation_id)"
            >{{ t('escalation.resolve.submit') }}</button>
          </div>
        </div>
      </section>

      <section data-test="section-resolved">
        <button type="button" data-test="toggle-resolved" @click="resolvedExpanded = !resolvedExpanded">
          {{ t('escalation.section.resolved', { n: resolvedEntries.length }) }}
        </button>
        <template v-if="resolvedExpanded">
          <p v-if="resolvedEntries.length === 0" class="empty" data-test="empty-resolved">{{ t('escalation.empty.resolved') }}</p>
          <div v-for="e in resolvedEntries" :key="e.Item.escalation_id" class="entry entry-resolved" :data-test="'entry-' + e.Item.escalation_id">
            <div class="head">
              <span class="summary">{{ e.Item.summary }}</span>
              <span :class="['badge', 'badge-source-' + e.Item.source]" :data-test="'source-badge-' + e.Item.escalation_id">
                {{ e.Item.source === 'system' ? t('escalation.badge.source.system') : t('escalation.badge.source.manual') }}
              </span>
            </div>
            <p class="meta">
              <span v-if="e.Item.block_scope">{{ t('escalation.label.blockScope') }}: {{ e.Item.block_scope }}</span>
              <span v-if="e.Item.source_ref">{{ t('escalation.label.sourceRef') }}: {{ e.Item.source_ref }}</span>
            </p>
          </div>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.escalation-inbox { text-align: left; padding: 8px; overflow-y: auto; height: 100%; }
.err { color: var(--err); font-size: var(--fs-s); }
.empty { color: var(--text-faint); font-size: var(--fs-s); }
.create-form { display: flex; flex-direction: column; gap: 6px; border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; margin-bottom: 10px; }
.create-form h4 { margin: 0 0 2px; font-size: var(--fs-s); }
.create-form textarea { min-height: 50px; padding: 4px 6px; }
section { margin-bottom: 12px; }
section h4 { margin: 0 0 4px; font-size: var(--fs-s); color: var(--text-muted); }
.entry { border: 1px solid var(--border); border-radius: var(--radius-s); padding: 8px; margin-bottom: 8px; display: flex; flex-direction: column; gap: 4px; }
.entry-resolved { opacity: 0.75; }
.head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.summary { font-weight: 600; }
.badge { font-size: var(--fs-s); padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; }
.badge-source-system { background: var(--text-faint, #6b7280); color: #f0f0f0; }
.badge-source-manual { background: var(--accent); color: #10201e; }
.badge-hard { background: var(--err); color: #2a0d0b; }
.meta { display: flex; gap: 10px; flex-wrap: wrap; color: var(--text-faint); font-size: var(--fs-s); margin: 0; overflow-wrap: anywhere; word-break: break-all; }
.actions { display: flex; gap: 6px; }
.hard-notice { color: var(--err); font-size: var(--fs-s); }
.resolve-form { display: flex; flex-direction: column; gap: 4px; }
.resolve-form textarea { min-height: 40px; padding: 4px 6px; }
</style>
