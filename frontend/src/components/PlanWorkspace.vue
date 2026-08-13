<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ConfirmPlanCommit, PlanAssist, PlanList, PlanRead, PlanWrite, PreviewPlanCommit, SubmitPlanForApproval,
} from '../../wailsjs/go/main/App'
import type { spec } from '../../wailsjs/go/models'
import { usePlan } from '../stores/plan'
// extractGherkin 只在 info tag 為 gherkin/feature 時才特殊處理，其餘（含無 tag／yaml
// tag）一律走通用 fence 擷取路徑——對 plan YAML 草稿一樣適用，別名匯入避免誤讀成
// domain 耦合（沿用既有、已測試涵蓋的 fence 擷取邏輯，不重複實作）。
import { extractGherkin as extractDraftContent } from '../lib/gherkin'

const { t } = useI18n()

// PlanWorkspace（Task 13，spec §7 Stage B）：鏡射 SpecWorkspace.vue 的資料流與
// props 慣例（`path`/`draft`/`write` 皆可注入，測試走這條路徑，避開 CM6 DOM／
// 真實 Wails binding）。與 SpecWorkspace 的關鍵差異：
// - AI 草稿區是自由輸入 prompt＋provider 選擇（PlanAssist(provider, prompt)），
//   不是固定的三個按鈕。
// - 「套用草稿」只把草稿內容寫進編輯器 buffer（store.currentContent），不直接
//   落地；落地要另外按「儲存」（PlanWrite，樂觀鎖）——SpecWorkspace 的
//   accept＝寫檔在這裡拆成兩步。
// - 檔案清單／目前檔（rel/content/digest）／草稿／驗證與送核錯誤都放在
//   usePlan store（沿 assist.ts 的 corr_id 累積慣例），不是元件內 local state
//   ——供後續 task（DagPane／GateConsole gate2 卡片）共用同一份目前計畫內容。
const props = defineProps<{
  path?: string
  draft?: string
  write?: (path: string, content: string, expectedDigest: string) => Promise<string>
}>()
const emit = defineEmits<{ (e: 'escalate', payload: { sourceRef: string; blockScope: string }): void }>()

const plan = usePlan()

const selectedPath = ref('')
const effectivePath = computed(() => props.path ?? selectedPath.value)

// escalate：review fix（spec §3.8 回填）——sourceRef 帶目前 plan 檔的 rel path
// （effectivePath，永遠可得；planIdInput 只在 plan/<id>.yaml 才推得出來，見
// deriveDefaultPlanId），blockScope 留空——建立與這份 plan 檔相關的升級項目
// 不預設它一定阻擋某個 gate scope，由操作者自行選。
function onEscalate() {
  emit('escalate', { sourceRef: effectivePath.value, blockScope: '' })
}

const loadError = ref('')

const currentCorrelationId = ref<string | null>(null)
const draftText = computed(() => props.draft ?? plan.draftOf(currentCorrelationId.value ?? '').text)
const assistBusy = ref(false)
const assistError = ref('')
const provider = ref<'claude' | 'codex'>('claude')
const promptInput = ref('')

const bufferDirty = ref(false)
const saveBusy = ref(false)

const planIdInput = ref('')

const submitBusy = ref(false)
const submitResult = ref('')

const commitToken = ref<spec.CommitToken | null>(null)
const commitDiff = ref('')
const commitMessage = ref('')
const commitBusy = ref(false)

const editorHost = ref<HTMLElement | null>(null)
let cmView: { destroy(): void; dispatch(spec: unknown): void; state: { doc: { length: number } } } | null = null

async function loadFileList() {
  try {
    const files = (await PlanList()) ?? []
    plan.setFiles(files.map(f => ({ name: f.name, path: f.path })))
  } catch (e) {
    loadError.value = String(e)
  }
}

// deriveDefaultPlanId：plan/<planID>.yaml 之外的檔（例如 risk-policy.yaml 或非
// plan/ 頂層檔）不推導——送核輸入框留給操作者自行填。
function deriveDefaultPlanId(path: string): string {
  const m = /^plan\/([^/]+)\.yaml$/.exec(path)
  if (!m || m[1] === 'risk-policy') return ''
  return m[1]
}

async function loadFile() {
  loadError.value = ''
  if (!effectivePath.value) return
  try {
    const pf = await PlanRead(effectivePath.value)
    plan.setCurrentFile(effectivePath.value, pf.content, pf.digest)
    bufferDirty.value = false
    planIdInput.value = deriveDefaultPlanId(effectivePath.value)
    syncEditorDoc()
  } catch (e) {
    loadError.value = String(e)
  }
}

function syncEditorDoc() {
  if (!cmView) return
  cmView.dispatch({ changes: { from: 0, to: cmView.state.doc.length, insert: plan.currentContent } })
}

async function initEditor() {
  if (!editorHost.value) return
  try {
    const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
      import('codemirror'),
      import('@codemirror/state'),
    ])
    cmView = new EditorView({
      state: EditorState.create({ doc: plan.currentContent, extensions: [basicSetup] }),
      parent: editorHost.value,
    })
  } catch (e) {
    // jsdom 缺 rAF／ResizeObserver 等瀏覽器 API 時 CM6 可能初始化失敗——靜默吞錯，
    // 不影響 draft-apply／assist／save／commit 等純邏輯路徑（brief 測試走這條路徑）。
    loadError.value = loadError.value || String(e)
  }
}

onMounted(async () => {
  await loadFileList()
  await loadFile()
  await initEditor()
})
onBeforeUnmount(() => cmView?.destroy())
watch(() => props.path, () => {
  resetDraft() // 換檔：清掉舊檔殘留的草稿，避免套用草稿把 A 的草稿寫進 B（同 SpecWorkspace fix round 1）
  void loadFile()
})

function resetDraft() {
  currentCorrelationId.value = null
}

function selectFile(p: string) {
  selectedPath.value = p
  resetDraft()
  if (!props.path) void loadFile()
}

// runAssist：呼叫 PlanAssist(provider, prompt)，輸出經 EventsOn('workbench:event')
// → App.vue routeEnvelope（purpose=plan_draft）→ plan store 累積。PlanAssist 直接
// 回傳這次呼叫的 correlation_id（同 SpecAssist），換檔競態處理同 SpecWorkspace：
// await 期間操作者可能已切到另一個檔案，只在 effectivePath 沒變時才採用回傳的 id。
async function runAssist() {
  assistError.value = ''
  assistBusy.value = true
  const startedForPath = effectivePath.value
  try {
    const id = await PlanAssist(provider.value, promptInput.value)
    if (id && effectivePath.value === startedForPath) {
      currentCorrelationId.value = id
    }
  } catch (e) {
    assistError.value = String(e)
  } finally {
    assistBusy.value = false
  }
}

// applyDraft：只把草稿寫進編輯器 buffer（store.currentContent），不落地——
// 落地是「儲存」的職責（見下方 saveFile）。同 SpecWorkspace acceptDraft，只取
// fenced code block 內容，不把整段 prose 一起帶進 buffer。
function applyDraft() {
  const content = extractDraftContent(draftText.value)
  plan.currentContent = content
  bufferDirty.value = true
  syncEditorDoc()
}

// saveFile：PlanWrite 樂觀鎖——buffer 內容＋目前 digest 送出，成功後用新 digest
// 覆蓋，失敗（含 ErrPlanWriteConflict）原樣推進 plan.errors，不吞。
async function saveFile() {
  const writer = props.write ?? PlanWrite
  saveBusy.value = true
  try {
    const newDigest = await writer(effectivePath.value, plan.currentContent, plan.currentDigest)
    plan.currentDigest = newDigest
    bufferDirty.value = false
  } catch (e) {
    plan.pushError(String(e))
  } finally {
    saveBusy.value = false
  }
}

async function submitForApproval() {
  submitBusy.value = true
  try {
    submitResult.value = await SubmitPlanForApproval(planIdInput.value)
  } catch (e) {
    plan.pushError(String(e))
  } finally {
    submitBusy.value = false
  }
}

async function previewCommit() {
  commitBusy.value = true
  try {
    const res = await PreviewPlanCommit()
    commitToken.value = res.token
    commitDiff.value = res.diff
  } catch (e) {
    plan.pushError(String(e))
  } finally {
    commitBusy.value = false
  }
}

async function confirmCommit() {
  if (!commitToken.value) return
  commitBusy.value = true
  try {
    await ConfirmPlanCommit(commitToken.value, commitMessage.value)
    commitToken.value = null
    commitDiff.value = ''
    commitMessage.value = ''
  } catch (e) {
    plan.pushError(String(e))
  } finally {
    commitBusy.value = false
  }
}
</script>

<template>
  <div class="plan-workspace">
    <div class="files">
      <button v-for="f in plan.files" :key="f.path" :class="{ active: f.path === effectivePath }"
        @click="selectFile(f.path)">{{ f.name }}</button>
    </div>

    <div ref="editorHost" class="editor" data-test="editor-host" />
    <p v-if="loadError" class="err">{{ loadError }}</p>

    <div class="assist-area">
      <select v-model="provider" data-test="provider-select" :aria-label="t('planWorkspace.provider.label')">
        <option value="claude">claude</option>
        <option value="codex">codex</option>
      </select>
      <textarea v-model="promptInput" data-test="prompt-input" :placeholder="t('planWorkspace.prompt.placeholder')" />
      <button data-test="generate-draft" :disabled="assistBusy" @click="runAssist">{{ t('planWorkspace.action.generateDraft') }}</button>
    </div>
    <p v-if="assistError" class="err">{{ assistError }}</p>

    <div class="draft-area">
      <p v-if="assistBusy" class="assist-busy" data-test="assist-busy">{{ t('planWorkspace.assist.drafting') }}</p>
      <pre class="draft-text" data-test="draft-text">{{ draftText }}</pre>
      <button data-test="apply-draft" :disabled="!draftText" @click="applyDraft">{{ t('planWorkspace.action.applyDraft') }}</button>
    </div>

    <div class="save-area">
      <button data-test="save" :disabled="saveBusy || !bufferDirty" @click="saveFile">{{ t('planWorkspace.action.save') }}</button>
    </div>

    <div class="approval">
      <input v-model="planIdInput" data-test="plan-id" :placeholder="t('planWorkspace.planId.placeholder')" />
      <button data-test="submit-gate2" :disabled="submitBusy || !planIdInput" @click="submitForApproval">{{ t('planWorkspace.action.submit') }}</button>
      <span v-if="submitResult" class="ok">{{ t('planWorkspace.submittedApprovalId', { id: submitResult }) }}</span>
      <button v-if="effectivePath" type="button" data-test="escalate" @click="onEscalate">{{ t('escalation.create.buttonFrom') }}</button>
    </div>

    <div class="commit">
      <button data-test="preview-commit" :disabled="commitBusy" @click="previewCommit">{{ t('planWorkspace.action.previewCommit') }}</button>
      <pre v-if="commitDiff" class="diff" data-test="commit-diff">{{ commitDiff }}</pre>
      <template v-if="commitToken">
        <input v-model="commitMessage" data-test="commit-message" :placeholder="t('planWorkspace.commitMessage.placeholder')" />
        <button data-test="confirm-commit" :disabled="commitBusy" @click="confirmCommit">{{ t('planWorkspace.action.confirmCommit') }}</button>
      </template>
    </div>

    <ul v-if="plan.errors.length" class="errors" data-test="plan-errors">
      <li v-for="(e, i) in plan.errors" :key="i" class="err">{{ e }}</li>
    </ul>
  </div>
</template>

<style scoped>
.plan-workspace { display: flex; flex-direction: column; gap: 8px; padding: 8px; text-align: left; height: 100%; overflow-y: auto; }
.files { display: flex; gap: 4px; flex-wrap: wrap; }
.files button.active { background: var(--bg-bubble-user); color: #fff; }
.editor { height: 280px; min-height: 120px; border: 1px solid var(--border); border-radius: var(--radius-s); overflow: hidden; }
.editor :deep(.cm-editor) { height: 100%; }
.editor :deep(.cm-scroller) { overflow: auto; }
.assist-area { display: flex; flex-direction: column; gap: 4px; }
.draft-area { display: flex; flex-direction: column; gap: 4px; }
.draft-text { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); min-height: 60px; max-height: 240px; overflow-y: auto; }
.diff { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); max-height: 240px; overflow-y: auto; }
.assist-busy { color: var(--text-muted); font-size: var(--fs-s); }
.err { color: var(--err); font-size: var(--fs-s); }
.ok { color: var(--text-muted); font-size: var(--fs-s); }
.errors { margin: 0; padding-left: 16px; }
</style>
