<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { evidence } from '../../wailsjs/go/models'
import { resolveState, evidenceResultKeys } from '../i18n/stateKeys'

const { t } = useI18n()

// EvidenceDetail（Task 22）：EvidenceGet 的完整 record 顯示——GateConsole 的
// tca 卡片、TcaWorkspace 都可能觸發開啟（emit evidence_id），本身是自包含的
// overlay（鏡射 ApprovalDialog 的 v-if="current" 慣例），由 evidenceId prop
// 是否非空決定顯隱。get 走 props 注入（同 PlanWorkspace 的 write 慣例），測試
// 以 props 驅動，不依賴真實 Wails binding。
const props = defineProps<{
  evidenceId: string
  get: (evidenceId: string) => Promise<evidence.EvidenceRun>
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const record = ref<evidence.EvidenceRun | null>(null)
const error = ref('')
const busy = ref(false)

async function load() {
  record.value = null
  error.value = ''
  if (!props.evidenceId) return
  busy.value = true
  try {
    record.value = await props.get(props.evidenceId)
  } catch (e) {
    error.value = String(e)
  } finally {
    busy.value = false
  }
}
watch(() => props.evidenceId, load, { immediate: true })

// shortDigest：同 GateConsole 的既定短格式（前 12 字元＋…，全文交給 title tooltip）。
function shortDigest(d: string | undefined): string {
  if (!d) return ''
  return d.length > 12 ? d.slice(0, 12) + '…' : d
}
</script>

<template>
  <div v-if="evidenceId" class="overlay" data-test="evidence-detail">
    <div class="dialog">
      <div class="head">
        <h3>{{ t('evidence.title', { id: evidenceId }) }}</h3>
        <button type="button" data-test="close" @click="emit('close')">{{ t('evidence.action.close') }}</button>
      </div>
      <p v-if="busy" class="busy" data-test="evidence-loading">{{ t('evidence.loading') }}</p>
      <p v-else-if="error" class="err" data-test="evidence-error">{{ error }}</p>
      <dl v-else-if="record" class="fields" data-test="evidence-fields">
        <dt>{{ t('evidence.label.evidenceId') }}</dt><dd>{{ record.evidence_id }}</dd>
        <dt>{{ t('evidence.label.kind') }}</dt><dd>{{ record.kind }}</dd>
        <dt>{{ t('evidence.label.result') }}</dt>
        <dd :class="['badge', 'badge-' + record.result]" data-test="evidence-result">
          {{ resolveState(evidenceResultKeys, record.result, t) }}
        </dd>
        <dt>{{ t('evidence.label.baseCommit') }}</dt><dd :title="record.base_commit">{{ record.base_commit }}</dd>
        <dt>{{ t('evidence.label.testCommit') }}</dt><dd :title="record.test_commit">{{ record.test_commit }}</dd>
        <dt>{{ t('evidence.label.oracleSurfaceDigest') }}</dt>
        <dd :title="record.oracle_surface_digest">{{ shortDigest(record.oracle_surface_digest) }}</dd>
        <template v-if="record.mutation_digest">
          <dt>{{ t('evidence.label.mutationDigest') }}</dt>
          <dd :title="record.mutation_digest">{{ shortDigest(record.mutation_digest) }}</dd>
        </template>
        <dt>{{ t('evidence.label.command') }}</dt>
        <dd>{{ record.command.executable }} {{ (record.command.argv ?? []).join(' ') }}</dd>
        <dt>{{ t('evidence.label.cwd') }}</dt><dd>{{ record.cwd }}</dd>
        <dt>{{ t('evidence.label.startedAt') }}</dt><dd>{{ record.started_at }}</dd>
        <dt>{{ t('evidence.label.finishedAt') }}</dt><dd>{{ record.finished_at }}</dd>
        <dt>{{ t('evidence.label.exitCode') }}</dt><dd>{{ record.exit_code }}</dd>
        <dt>{{ t('evidence.label.expectedFailure') }}</dt>
        <dd>{{ record.expected_failure.matcher }}: {{ (record.expected_failure.test_ids ?? []).join(', ') }}</dd>
        <dt>{{ t('evidence.label.observedFailure') }}</dt><dd>{{ record.observed_failure }}</dd>
        <dt>{{ t('evidence.label.stdoutDigest') }}</dt><dd :title="record.stdout_digest">{{ shortDigest(record.stdout_digest) }}</dd>
        <dt>{{ t('evidence.label.stderrDigest') }}</dt><dd :title="record.stderr_digest">{{ shortDigest(record.stderr_digest) }}</dd>
        <dt>{{ t('evidence.label.recordingRef') }}</dt><dd>{{ record.recording_ref }}</dd>
        <dt>{{ t('evidence.label.runnerVersion') }}</dt><dd>{{ record.runner_version }}</dd>
      </dl>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.55);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.dialog {
  background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius-m);
  padding: 16px; max-width: 640px; width: 90%; max-height: 80vh; overflow-y: auto; text-align: left;
}
.head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.fields { display: grid; grid-template-columns: max-content 1fr; gap: 4px 10px; font-size: var(--fs-s); margin-top: 8px; }
.fields dt { color: var(--text-faint); }
.fields dd { margin: 0; overflow-wrap: anywhere; word-break: break-all; }
.badge { display: inline-block; padding: 1px 6px; border-radius: var(--radius-s); font-weight: 600; width: fit-content; }
.badge-passed { background: var(--ok); color: #10201e; }
.badge-failed, .badge-error { background: var(--err); color: #2a0d0b; }
.busy { color: var(--text-muted); font-size: var(--fs-s); }
.err { color: var(--err); font-size: var(--fs-s); }
</style>
