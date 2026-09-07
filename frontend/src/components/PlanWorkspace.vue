<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ConfirmAnalysisBaseBump, ConfirmPlanCommit, PlanAssist, PlanList, PlanRead, PlanWrite,
  PreviewAnalysisBaseBump, PreviewPlanCommit, SubmitPlanForApproval,
} from '../../wailsjs/go/main/App'
import type { main, spec } from '../../wailsjs/go/models'
import { usePlan } from '../stores/plan'
// extractGherkin 只在 info tag 為 gherkin/feature 時才特殊處理，其餘（含無 tag／yaml
// tag）一律走通用 fence 擷取路徑——對 plan YAML 草稿一樣適用，別名匯入避免誤讀成
// domain 耦合（沿用既有、已測試涵蓋的 fence 擷取邏輯，不重複實作）。
import { extractGherkin as extractDraftContent } from '../lib/gherkin'
import { templateFor, inScope, PLAN_SCOPE_PATTERNS } from '../lib/planTemplates'
import { isWriteConflict } from '../lib/writeConflict'

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
const emit = defineEmits<{
  (e: 'escalate', payload: { sourceRef: string; blockScope: string }): void
  (e: 'busy', v: boolean): void
}>()

const plan = usePlan()

const selectedPath = ref('')
// effectivePath：selectedPath 是唯一權威來源——props.path 只在 mount／變動時
// seed 一次（見下方 onMounted／watch(props.path)），seed 完之後檔案清單的手動
// selectFile 點擊照常生效，不會被 prop 永久蓋掉（見 watch(props.path) 註解）。
const effectivePath = computed(() => selectedPath.value)

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

// A1a-1 非同步儲存契約：bufferDirty 改為內容比較（plan.currentContent／
// plan.savedContent），不再是手動旗標——applyDraft／confirmBump／saveFile 皆
// 不再手動賦值。busyReason 是寫入互斥旗標，''｜'save'｜'load'｜'bump' 四態，
// 同時驅動 [data-busy] 與 [data-editing-suspended]。
const bufferDirty = computed(() => plan.currentContent !== plan.savedContent)
const busyReason = ref<'' | 'save' | 'load' | 'bump'>('')
const saveError = ref('')
const saveConflict = ref(false)
watch(busyReason, v => emit('busy', v !== ''))

const planIdInput = ref('')

const submitBusy = ref(false)
const submitResult = ref('')

const commitToken = ref<spec.CommitToken | null>(null)
const commitDiff = ref('')
const commitMessage = ref('')
const commitBusy = ref(false)

// analysis_base bump 引導 UI（M3a.1 Task 6，spec §3.2）：bumpPreview 是
// PreviewAnalysisBaseBump 目前結果的快取，只在「檔案載入／儲存成功／視窗
// 聚焦」時機重新查（brief 凍結——不逐鍵擊呼叫），且只對主要 plan 文件查
// （isPrimaryPlanPath；risk-policy／oracle-surface／permissions 沒有
// analysis_base_commit 欄位，查了必錯，不是操作者需要看到的錯誤）。Preview
// 本身失敗（例如 analysis_base_commit 尚未填、plan 還在草稿階段）視為正常
// 過渡狀態，靜默清空 bumpPreview，不推進 plan.errors——那不是操作者這時要
// 處理的問題（模板本就把這欄位留空，見 planTemplates.ts planSkeleton）。
const bumpPreview = ref<main.BumpPreview | null>(null)
const bumpPanelOpen = ref(false)
const bumpConfirmError = ref('')
// bumpError／bumpStale（A1a-1）：confirmBump 四條結束路徑共用的新呈現點
// （[data-test=bump-error][data-stale]）——與既有 bumpConfirmError／
// [data-test=bump-confirm-error] 並存，既有測試依賴後者故不移除。
const bumpError = ref('')
const bumpStale = ref(false)

const editorHost = ref<HTMLElement | null>(null)
let cmView: { destroy(): void; dispatch(spec: unknown): void; state: { doc: { length: number } } } | null = null
let loadGen = 0

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
  const gen = ++loadGen
  busyReason.value = 'load'
  try {
    const pf = await PlanRead(effectivePath.value)
    if (gen !== loadGen) return // 過期世代：整筆丟棄，不動 buffer／saved／digest／編輯器／busy
    plan.setCurrentFile(effectivePath.value, pf.content, pf.digest)
    planIdInput.value = deriveDefaultPlanId(effectivePath.value)
    syncEditorDoc()
    busyReason.value = ''
    await checkBump()
  } catch (e) {
    if (gen !== loadGen) return
    loadError.value = String(e)
    busyReason.value = ''
  }
}

// checkBump：analysis_base bump 觸發點之一（brief 凍結：檔案載入／儲存成功／
// 視窗聚焦——見 loadFile／saveFile／onWindowFocus，非逐鍵擊）。只對主要 plan
// 文件查（見上方 bumpPreview 宣告註解）。keepConfirmError 供 confirmBump 失敗
// 後的重新預覽用：那次重查是為了刷新面板內容，不該連帶清掉剛顯示的錯誤訊息。
async function checkBump(opts: { keepConfirmError?: boolean } = {}) {
  if (!isPrimaryPlanPath(effectivePath.value)) {
    bumpPreview.value = null
  } else {
    try {
      bumpPreview.value = await PreviewAnalysisBaseBump(effectivePath.value, plan.currentContent)
    } catch {
      bumpPreview.value = null
    }
  }
  if (!opts.keepConfirmError) bumpConfirmError.value = ''
}

function resetBump() {
  bumpPreview.value = null
  bumpPanelOpen.value = false
  bumpConfirmError.value = ''
}

// confirmBump：ConfirmAnalysisBaseBump 通過後，editor buffer 直接被
// updatedBuffer 取代（標記未儲存——落地仍要走既有「儲存」／PlanWrite 樂觀
// 鎖，同 applyDraft 套用草稿的兩步慣例）。失敗（token 過期／buffer 或 HEAD
// 變動）原文顯示錯誤，並重新查一次 Preview 讓面板內容回到目前實際狀態
// （brief：「要求重新預覽」）。
async function confirmBump() {
  if (!bumpPreview.value || bumpPreview.value.no_bump_needed) return
  if (busyReason.value !== '') return
  const frozen = { path: effectivePath.value, buf: plan.currentContent }
  const token = bumpPreview.value.token
  busyReason.value = 'bump'
  bumpError.value = '' // A2：新操作開始清同類舊 transient error
  bumpStale.value = false
  try {
    const updated = await ConfirmAnalysisBaseBump(token, frozen.path, frozen.buf)
    if (effectivePath.value !== frozen.path || plan.currentPath !== frozen.path || plan.currentContent !== frozen.buf) {
      // 版本過期：等待期間 buffer 續打或文件識別被替換，回應不套用——維持既有
      // （文件識別同時比對 effectivePath 與 store 的 currentPath：currentContent
      // 屬於 currentPath 那一份文件，只比內容不比它所屬路徑會漏掉識別已換的情形）
      // 「要求重新預覽」流程刷新面板，原續打內容／目前檔案 buffer 不受影響。
      bumpError.value = t('bump.staleVersion')
      bumpStale.value = true
      await checkBump({ keepConfirmError: true })
      return
    }
    bumpConfirmError.value = ''
    bumpError.value = ''
    bumpStale.value = false
    plan.currentContent = updated
    syncEditorDoc()
    bumpPreview.value = null
    bumpPanelOpen.value = false
  } catch (e) {
    // 後端真正 reject（token 過期／HEAD 變動等）：原訊息保留，不被版本過期訊息換掉。
    bumpError.value = String(e)
    bumpStale.value = false
    bumpConfirmError.value = String(e)
    await checkBump({ keepConfirmError: true })
  } finally {
    busyReason.value = ''
  }
}

// onBumpRerunAssist：bump 面板「重新執行 PlannerAssist」建議按鈕，直接觸發
// 既有 PlanAssist 流程（runAssist）——不是另一條路徑，只是同一個動作在 bump
// 情境下的入口。
function onBumpRerunAssist() {
  void runAssist()
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
      state: EditorState.create({
        doc: plan.currentContent,
        extensions: [
          basicSetup,
          // editor → buffer：唯一新增的同步方向（同 SpecWorkspace）。syncEditorDoc()
          // （buffer → editor）維持不變。
          EditorView.updateListener.of(u => {
            if (u.docChanged) plan.currentContent = u.state.doc.toString()
          }),
        ],
      }),
      parent: editorHost.value,
    })
  } catch (e) {
    // jsdom 缺 rAF／ResizeObserver 等瀏覽器 API 時 CM6 可能初始化失敗——靜默吞錯，
    // 不影響 draft-apply／assist／save／commit 等純邏輯路徑（brief 測試走這條路徑）。
    loadError.value = loadError.value || String(e)
  }
}

// onWindowFocus：bump 觸發時機之三（brief 凍結：視窗聚焦）——操作者切回視窗
// 時可能其他人已推進 HEAD，重查一次讓提示條反映目前狀態。
function onWindowFocus() {
  void checkBump()
}

onMounted(async () => {
  if (props.path) selectedPath.value = props.path // 見下方 watch(props.path) 註解
  await loadFileList()
  await loadFile()
  await initEditor()
  window.addEventListener('focus', onWindowFocus)
})
onBeforeUnmount(() => {
  cmView?.destroy()
  window.removeEventListener('focus', onWindowFocus)
})
// M3a.1 Task 11（spec §3.5）：STALE 重核引導從 App.vue 一次性帶入 path 導航到
// 指定 plan 檔（例如 GateConsole／EscalationInbox 的「前往重新送核」）。path
// 只當「seed」——寫入 selectedPath 後，effectivePath 就以 selectedPath 為準，
// 之後操作者在檔案清單點別的檔（selectFile）仍照常生效，不會被 prop 永久鎖死
// 在導航進來的那個檔案（原本 `props.path ?? selectedPath.value` 若不 seed，
// 只要 path prop 還留著非空值，selectFile 點擊會被完全蓋掉——清單看起來能點，
// 實際檔案永遠不換）。
watch(() => props.path, (p) => {
  // 寫入互斥（save／bump 進行中）才擋切檔——'load' busy 不擋，理由同 SpecWorkspace。
  if (busyReason.value === 'save' || busyReason.value === 'bump') return
  if (p) selectedPath.value = p
  resetDraft() // 換檔：清掉舊檔殘留的草稿，避免套用草稿把 A 的草稿寫進 B（同 SpecWorkspace fix round 1）
  resetBump()
  void loadFile()
})

function resetDraft() {
  currentCorrelationId.value = null
}

function selectFile(p: string) {
  if (busyReason.value === 'save' || busyReason.value === 'bump') return
  selectedPath.value = p
  resetDraft()
  resetBump()
  void loadFile()
}

// 新增檔案 inline 列（M3a.1 Task 4，spec §3.1 SC4 缺口 1）：路徑輸入＋即時 scope
// 預驗（plan/**，UI 提示用——後端 PlanWrite 的 spec.PlanScope.Match 仍是權威
// 驗證）＋單一 plan 擋（見下方 isPrimaryPlanPath／hasPrimaryPlan：清單已存在
// 一份主要 plan 文件時，再輸入另一個主要 plan 路徑禁止送出——risk-policy／
// oracle-surface／permissions 不算主要 plan，不受限）＋送出呼叫
// PlanWrite(path, templateFor(path), '')（新檔：expectedDigest 留空）。成功後
// 才重載清單並選取新檔；失敗經 plan.pushError 原樣顯示（同 saveFile／
// submitForApproval 等既有 write 路徑慣例），清單不動。
const newFilePath = ref('')
const newFileBusy = ref(false)
const newFileInScope = computed(() => newFilePath.value !== '' && inScope(newFilePath.value, PLAN_SCOPE_PATTERNS))

// isPrimaryPlanPath：符合單一 plan 限制檢查的「主要 plan 文件」——plan/<id>.yaml，
// 排除 risk-policy.yaml／oracle-surface.yaml（brief Step 3：清單中已存在符合
// /^plan\/[^/]+\.yaml$/ 且非 risk-policy/oracle-surface 的檔案）。
function isPrimaryPlanPath(path: string): boolean {
  return /^plan\/[^/]+\.yaml$/.test(path) && path !== 'plan/risk-policy.yaml' && path !== 'plan/oracle-surface.yaml'
}
const hasPrimaryPlan = computed(() => plan.files.some(f => isPrimaryPlanPath(f.path)))
const singlePlanBlocked = computed(() => hasPrimaryPlan.value && isPrimaryPlanPath(newFilePath.value))

async function createNewFile() {
  if (!newFilePath.value || !newFileInScope.value || singlePlanBlocked.value) return
  const path = newFilePath.value
  const writer = props.write ?? PlanWrite
  newFileBusy.value = true
  plan.clearErrors('newFile') // A2：新操作開始清同類（newFile）舊 transient error
  try {
    await writer(path, templateFor(path), '')
    await loadFileList()
    selectFile(path)
    newFilePath.value = ''
    plan.clearErrors('newFile') // A2：操作成功清該操作既有錯誤
  } catch (e) {
    plan.pushError(String(e), 'newFile')
  } finally {
    newFileBusy.value = false
  }
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
  syncEditorDoc()
}

// saveFile：PlanWrite 樂觀鎖——buffer（plan.currentContent，CM6 目前內容）＋目前
// digest 於按下當下凍結成送出快照 P，成功後 plan.savedContent／plan.currentDigest
// 更新為 P 的值（不是回應到達當下可能已被續打改變的內容）；失敗三者皆不變、
// 不自動重載。
async function saveFile() {
  if (busyReason.value !== '') return
  const writer = props.write ?? PlanWrite
  const P = { path: effectivePath.value, content: plan.currentContent, digest: plan.currentDigest }
  busyReason.value = 'save'
  plan.clearErrors('save') // A2：新操作開始清同類（save）舊 transient error
  saveError.value = '' // 同上，新的呈現點也要清
  saveConflict.value = false
  try {
    const newDigest = await writer(P.path, P.content, P.digest)
    plan.savedContent = P.content
    plan.currentDigest = newDigest
    plan.clearErrors('save') // A2：操作成功清該操作既有錯誤
    saveError.value = ''
    saveConflict.value = false
    await checkBump() // 觸發時機之二（brief 凍結：儲存成功）
  } catch (e) {
    plan.pushError(String(e), 'save')
    saveError.value = String(e)
    saveConflict.value = isWriteConflict(e)
  } finally {
    busyReason.value = ''
  }
}

// submitForApproval（A2：error lifecycle 三原則）——HEAD 上這裡成功路徑從未清
// 過 plan.errors，Gate 2 dirty-tree 送核失敗後即使後續修正＋再送核成功，舊錯誤
// 仍留在畫面（A9 驗收 sec-12 記錄的真實缺陷）。原則 1：再次點送核時先清掉「上次
// 送核」的錯誤（kind='submit'，不動 save／previewCommit 等其他操作留下的錯誤）；
// 原則 2：這次送核成功後也明確清掉（雙重保險，語意對應驗收條件逐條核對）。
async function submitForApproval() {
  submitBusy.value = true
  plan.clearErrors('submit')
  try {
    submitResult.value = await SubmitPlanForApproval(planIdInput.value)
    plan.clearErrors('submit')
  } catch (e) {
    plan.pushError(String(e), 'submit')
  } finally {
    submitBusy.value = false
  }
}

async function previewCommit() {
  commitBusy.value = true
  plan.clearErrors('commit') // A2：新操作開始清同類（commit）舊 transient error
  try {
    const res = await PreviewPlanCommit()
    commitToken.value = res.token
    commitDiff.value = res.diff
    plan.clearErrors('commit') // A2：操作成功清該操作既有錯誤
  } catch (e) {
    plan.pushError(String(e), 'commit')
  } finally {
    commitBusy.value = false
  }
}

async function confirmCommit() {
  if (!commitToken.value) return
  commitBusy.value = true
  plan.clearErrors('commit') // A2：confirmCommit 與 previewCommit 共用同一個 commit 生命週期（連續兩步操作），同一 kind
  try {
    await ConfirmPlanCommit(commitToken.value, commitMessage.value)
    commitToken.value = null
    commitDiff.value = ''
    commitMessage.value = ''
    plan.clearErrors('commit') // A2：操作成功清該操作既有錯誤
  } catch (e) {
    plan.pushError(String(e), 'commit')
  } finally {
    commitBusy.value = false
  }
}
</script>

<template>
  <div class="plan-workspace" :data-busy="busyReason">
    <div class="new-file">
      <input v-model="newFilePath" data-test="new-file-path" :placeholder="t('newFile.path.placeholder')" />
      <button
        type="button" data-test="new-file-submit" :disabled="newFileBusy || !newFilePath || !newFileInScope || singlePlanBlocked"
        @click="createNewFile"
      >{{ t('newFile.action.create') }}</button>
      <span v-if="newFilePath && !newFileInScope" class="err" data-test="new-file-scope-hint">{{ t('newFile.scopeHint') }}</span>
      <span v-else-if="singlePlanBlocked" class="err" data-test="new-file-single-plan-hint">{{ t('newFile.singlePlanBlocked') }}</span>
    </div>

    <div class="files">
      <button v-for="f in plan.files" :key="f.path" :class="{ active: f.path === effectivePath }"
        @click="selectFile(f.path)">{{ f.name }}</button>
    </div>

    <div
      ref="editorHost" class="editor" data-test="editor-host"
      :data-editing-suspended="busyReason === 'load' ? 'true' : undefined"
    />
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
      <button data-test="save" :disabled="busyReason !== '' || !bufferDirty" @click="saveFile">{{ t('planWorkspace.action.save') }}</button>
    </div>
    <p v-if="saveError" class="err" data-test="save-error" :data-conflict="saveConflict ? 'true' : undefined">{{ saveError }}</p>

    <div v-if="bumpPreview || bumpConfirmError || bumpError" class="bump-area">
      <div v-if="bumpPreview && bumpPreview.no_bump_needed" class="bump-no-bump-needed" data-test="bump-no-bump-needed">
        {{ t('bump.noBumpNeeded') }}
      </div>
      <div v-else-if="bumpPreview" class="bump-banner" data-test="bump-banner">
        <span>{{ t('bump.banner.message') }}</span>
        <button type="button" data-test="bump-toggle" @click="bumpPanelOpen = !bumpPanelOpen">{{ t('bump.action.viewDiff') }}</button>
      </div>
      <div v-if="bumpPreview && !bumpPreview.no_bump_needed && bumpPanelOpen" class="bump-panel" data-test="bump-panel">
        <p class="bump-shas">
          <span :title="bumpPreview.old" data-test="bump-old">{{ bumpPreview.old.slice(0, 10) }}</span>
          →
          <span :title="bumpPreview.head" data-test="bump-head">{{ bumpPreview.head.slice(0, 10) }}</span>
        </p>
        <ul class="bump-commits" data-test="bump-commits">
          <li v-for="c in bumpPreview.commits" :key="c.oid">{{ c.oid.slice(0, 10) }} — {{ c.subject }}</li>
        </ul>
        <ul class="bump-touched-files" data-test="bump-touched-files">
          <li v-for="f in bumpPreview.touched_files" :key="f">{{ f }}</li>
        </ul>
        <p class="bump-warning" data-test="bump-warning">{{ t('bump.warning') }}</p>
        <button type="button" data-test="bump-rerun-assist" @click="onBumpRerunAssist">{{ t('bump.action.rerunAssist') }}</button>
        <button type="button" data-test="bump-confirm" :disabled="busyReason !== ''" @click="confirmBump">{{ t('bump.action.confirm') }}</button>
      </div>
      <!-- bumpConfirmError 獨立於面板外層——Confirm 失敗後觸發的重新預覽
           （checkBump keepConfirmError）若把狀態翻成 no_bump_needed 或本身也
           失敗（bumpPreview 變 null），錯誤訊息仍要留著，不能因為面板收合／
           消失就跟著靜默不見（Fail Loud）。 -->
      <p v-if="bumpConfirmError" class="err" data-test="bump-confirm-error">{{ bumpConfirmError }}</p>
      <!-- bumpError／bumpStale（A1a-1）：confirmBump 四條結束路徑共用的呈現點，
           與上面既有的 bumpConfirmError 並存（既有測試依賴後者，不移除）。 -->
      <p v-if="bumpError" class="err" data-test="bump-error" :data-stale="bumpStale ? 'true' : undefined">{{ bumpError }}</p>
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
.new-file { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.files { display: flex; gap: 4px; flex-wrap: wrap; }
.files button.active { background: var(--bg-bubble-user); color: #fff; }
.editor { height: 280px; min-height: 120px; border: 1px solid var(--border); border-radius: var(--radius-s); overflow: hidden; }
.editor :deep(.cm-editor) { height: 100%; }
.editor :deep(.cm-scroller) { overflow: auto; }
.assist-area { display: flex; flex-direction: column; gap: 4px; }
.draft-area { display: flex; flex-direction: column; gap: 4px; }
.draft-text { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); min-height: 60px; max-height: 240px; overflow-y: auto; }
.diff { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); max-height: 240px; overflow-y: auto; }
.bump-area { display: flex; flex-direction: column; gap: 4px; }
.bump-banner { display: flex; align-items: center; gap: 8px; background: var(--bg-inset); padding: 6px 8px; border-radius: var(--radius-s); }
.bump-no-bump-needed { color: var(--text-muted); font-size: var(--fs-s); }
.bump-panel { display: flex; flex-direction: column; gap: 6px; padding: 8px; border: 1px solid var(--border); border-radius: var(--radius-s); }
.bump-commits, .bump-touched-files { margin: 0; padding-left: 16px; max-height: 160px; overflow-y: auto; }
.bump-warning { font-weight: 600; }
.assist-busy { color: var(--text-muted); font-size: var(--fs-s); }
.err { color: var(--err); font-size: var(--fs-s); }
.ok { color: var(--text-muted); font-size: var(--fs-s); }
.errors { margin: 0; padding-left: 16px; }
</style>
