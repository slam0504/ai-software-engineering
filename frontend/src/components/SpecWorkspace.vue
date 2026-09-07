<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ConfirmSpecCommit, PreviewSpecCommit, SpecAssist, SpecList, SpecRead, SpecWrite, SubmitForApproval,
} from '../../wailsjs/go/main/App'
import type { main, spec } from '../../wailsjs/go/models'
import { useSession } from '../stores/session'
import { useAssist } from '../stores/assist'
import { extractGherkin } from '../lib/gherkin'
import { templateFor, inScope, SPEC_SCOPE_PATTERNS } from '../lib/planTemplates'
import { isWriteConflict } from '../lib/writeConflict'

const { t } = useI18n()

// SpecWorkspace（Task 15，spec §5.1）：CodeMirror 6 編輯器＋三個 AI 輔助按鈕＋
// 草稿區（accept 後才 SpecWrite）＋送核＋SpecCommit 兩階段 UI。
//
// `path`/`draft`/`write` 皆為可注入 props（測試走這條路徑，避開 CM6 DOM／真實
// Wails binding）：未提供時分別 fallback 到內部檔案選取狀態、assist store 累積
// 草稿、真正的 SpecWrite。CM6 初始化與所有 SpecRead/SpecList 呼叫都在 onMounted
// 動態 import＋try/catch 內完成——jsdom 下沒有 window.go／無 rAF 等瀏覽器 API
// 一律靜默吞錯（同 App.vue／PreviewPane.vue「dev 無綁定時忽略」慣例），不讓測試
// 掛在未處理的例外上。
const props = defineProps<{
  path?: string
  draft?: string
  write?: (path: string, content: string, expectedDigest: string) => Promise<string>
}>()
const emit = defineEmits<{ (e: 'busy', v: boolean): void; (e: 'dirty', v: boolean): void }>()

const s = useSession()
const assist = useAssist()

const files = ref<main.FileNode[]>([])
// selectedPath 是唯一權威來源（A1a-1 缺口 3 修正）：props.path 只在宣告當下 seed
// 一次，之後 effectivePath 永遠以 selectedPath 為準——watch(props.path) 在寫入
// 互斥期間連 selectedPath 都不更新，避免「A 儲存中 prop 改為 B」造成 saveFile
// 送出下一次時用 B 的路徑配 A 的內容與 digest（見該 watch 註解）。
const selectedPath = ref(props.path ?? '')
const effectivePath = computed(() => selectedPath.value)

const fileContent = ref('')
const fileDigest = ref('')
const loadError = ref('')

// A1a-1 非同步儲存契約：savedContent＝最近一次成功寫入所送出的內容，
// dirty＝buffer（fileContent，CM6 編輯器目前內容）與 saved 是否不同（純內容
// 比較，不靠手動旗標）。busyReason 是寫入／載入互斥旗標，''｜'save'｜'load'｜
// 'accept' 四態，同時驅動 [data-busy] 呈現與 [data-editing-suspended]。
const savedContent = ref('')
const dirty = computed(() => fileContent.value !== savedContent.value)
const busyReason = ref<'' | 'save' | 'load' | 'accept'>('')
const saveError = ref('')
const saveConflict = ref(false)
watch(busyReason, v => emit('busy', v !== ''))
// A1a-2：dirty 對外回報，供 App.vue 的跨分頁導覽守衛判斷。immediate 讓掛載當下
// 的狀態也送出，避免 App 端停留在預設值。
watch(dirty, v => emit('dirty', v), { immediate: true })
// pendingPath：工作區「內部清單切檔」的守衛目標。A1a-2 只攔這條路徑；由 App
// 變更 props.path 觸發的載入不再攔（App 已確認過，同一次導覽不得出現兩次確認）。
const pendingPath = ref<string | null>(null)

// loadGen：每次 loadFile() 呼叫遞增的載入世代——<script setup> 頂層程式碼每個
// 元件實例各跑一次，這裡是「模組內」但屬於該實例，不會跨元件實例互相污染。
// await 期間若世代已被更新的載入蓋過，回應到達時整筆丟棄（不寫 fileContent／
// savedContent／fileDigest、不動編輯器、不清 busy——由「贏得世代」的那次載入
// 自行清 busy）。

const currentCorrelationId = ref<string | null>(null)
const draftText = computed(() => props.draft ?? assist.draftOf(currentCorrelationId.value ?? '').text)
const assistBusy = ref(false)
const assistError = ref('')

const acceptError = ref('')

const submitBusy = ref(false)
const submitResult = ref('')
const submitError = ref('')

const commitToken = ref<spec.CommitToken | null>(null)
const commitDiff = ref('')
const commitMessage = ref('')
const commitBusy = ref(false)
const commitError = ref('')

const editorHost = ref<HTMLElement | null>(null)
let cmView: { destroy(): void; dispatch(spec: unknown): void; state: { doc: { length: number } } } | null = null
let loadGen = 0
// editableComp／EditorViewRef：CM6 可編輯狀態的 Compartment（缺口 1 修正）——
// `EditorView.editable` 只擋使用者輸入，程式化 dispatch 仍會改文件，所以「載入
// 期間 buffer 不被污染」另外靠 updateListener 的 busyReason 檢查保證（見
// initEditor）。setEditable() 在 cmView／editableComp 尚未就緒（第一次載入完成
// 前）時是 no-op，由 initEditor 建構時直接以目前 busyReason 決定初始可編輯狀態。
let editableComp: { reconfigure(effect: unknown): unknown } | null = null
let EditorViewRef: { editable: { of(v: boolean): unknown } } | null = null

function setEditable(v: boolean) {
  if (!cmView || !editableComp || !EditorViewRef) return
  cmView.dispatch({ effects: editableComp.reconfigure(EditorViewRef.editable.of(v)) })
}

async function loadFileList() {
  try {
    files.value = (await SpecList()) ?? []
  } catch (e) {
    loadError.value = String(e)
  }
}

async function loadFile() {
  loadError.value = ''
  if (!effectivePath.value) return
  const gen = ++loadGen
  busyReason.value = 'load'
  setEditable(false)
  try {
    const sf = await SpecRead(effectivePath.value)
    if (gen !== loadGen) return // 過期世代：整筆丟棄，不動 buffer／saved／digest／編輯器／busy
    fileContent.value = sf.content
    savedContent.value = sf.content
    fileDigest.value = sf.digest
    syncEditorDoc()
    busyReason.value = ''
    setEditable(true) // 只有贏得世代的那次才解除暫停
  } catch (e) {
    if (gen !== loadGen) return
    loadError.value = String(e)
    busyReason.value = ''
    setEditable(true)
  }
}

function syncEditorDoc() {
  if (!cmView) return
  cmView.dispatch({ changes: { from: 0, to: cmView.state.doc.length, insert: fileContent.value } })
}

async function initEditor() {
  if (!editorHost.value) return
  try {
    const [{ EditorView, basicSetup }, { EditorState, Compartment }] = await Promise.all([
      import('codemirror'),
      import('@codemirror/state'),
    ])
    EditorViewRef = EditorView
    const compartment = new Compartment()
    editableComp = compartment
    cmView = new EditorView({
      state: EditorState.create({
        doc: fileContent.value,
        extensions: [
          basicSetup,
          // 可編輯狀態（缺口 1 修正）：包在 Compartment 內才能之後用 setEditable()
          // 動態切換，不必整個重建 EditorState。初始值以目前 busyReason 決定——
          // initEditor 排在 onMounted 的 loadFile 之後才跑，正常情況下第一次載入
          // 已完成、busyReason 已清空，但仍以實際狀態為準，不寫死 true。
          compartment.of(EditorView.editable.of(busyReason.value !== 'load')),
          // editor → buffer：唯一新增的同步方向。syncEditorDoc()（buffer → editor）
          // 維持不變，兩者不會互相形成迴圈——docChanged 只在使用者輸入／外部
          // dispatch 造成文件實際改變時觸發。load 期間不回寫：EditorView.editable
          // 只擋使用者輸入，程式化 dispatch（例如過期世代到達前的中繼狀態）仍會
          // 改動文件，這裡再擋一層才能讓「載入期間 buffer 不被污染」可被測試證明。
          EditorView.updateListener.of(u => {
            if (u.docChanged && busyReason.value !== 'load') fileContent.value = u.state.doc.toString()
          }),
        ],
      }),
      parent: editorHost.value,
    })
  } catch (e) {
    // jsdom 缺 rAF／ResizeObserver 等瀏覽器 API 時 CM6 可能初始化失敗——靜默吞錯，
    // 不影響 draft-accept／assist／commit 等純邏輯路徑（brief 測試走這條路徑）。
    loadError.value = loadError.value || String(e)
  }
}

onMounted(async () => {
  await loadFileList()
  await loadFile()
  await initEditor()
})
onBeforeUnmount(() => cmView?.destroy())
watch(() => props.path, p => {
  // 寫入互斥（save／accept 進行中）才擋切檔——'load' busy 不擋：新的載入世代
  // 本來就該蓋過舊的（見 loadFile 的 gen 丟棄邏輯），擋掉會讓過期載入卡死畫面。
  // 忙碌時連 selectedPath 都不更新（缺口 3 修正）：effectivePath 是唯一權威
  // 來源，若在此就先把 selectedPath 換成新路徑，寫入完成後下一次儲存會用新
  // 路徑配舊內容與舊 digest，寫錯檔——見上方 selectedPath 宣告註解。
  if (busyReason.value === 'save' || busyReason.value === 'accept') return
  if (p !== undefined) selectedPath.value = p
  resetDraft() // 換檔：清掉舊檔殘留的草稿，避免 accept 把 A 的草稿寫進 B（見 fix round 1）
  void loadFile()
})

// resetDraft：換選檔時呼叫——草稿是逐檔的，不能帶著另一個檔案的 correlation_id
// 跨檔殘留，否則 acceptDraft() 會用「目前選中檔案」的合法 digest 把「另一個檔案」
// 的草稿寫進來，SpecWrite 的樂觀鎖擋不住（digest 本身確實對得上目前檔案）。
function resetDraft() {
  currentCorrelationId.value = null
}

function selectFile(p: string) {
  if (busyReason.value === 'save' || busyReason.value === 'accept') return // A1a-1 優先於 A1a-2 守衛
  if (dirty.value) { pendingPath.value = p; return } // 未儲存 → 先確認，不得靜默覆蓋
  selectedPath.value = p
  resetDraft()
  void loadFile() // selectedPath 是唯一權威來源，選檔一律觸發載入（同 PlanWorkspace）
}

// 新增檔案 inline 列（M3a.1 Task 4，spec §3.1 SC4 缺口 1）：路徑輸入＋即時 scope
// 預驗（四 pattern，UI 提示用——後端 SpecWrite 的 spec.InScope 仍是權威驗證）＋
// 送出呼叫 SpecWrite(path, templateFor(path), '')（新檔：expectedDigest 留空，
// 見 app.go SpecWrite 對「檔案不存在時 expectedDigest 必須為空」的樂觀鎖語意）。
// 成功後才重載清單並選取新檔；失敗顯示錯誤原文，清單不動（catch 內不觸碰
// files／selectedPath）。
const newFilePath = ref('')
const newFileBusy = ref(false)
const newFileError = ref('')
const newFileInScope = computed(() => newFilePath.value !== '' && inScope(newFilePath.value, SPEC_SCOPE_PATTERNS))

async function createNewFile() {
  newFileError.value = ''
  if (!newFilePath.value || !newFileInScope.value) return
  const path = newFilePath.value
  const writer = props.write ?? SpecWrite
  newFileBusy.value = true
  try {
    await writer(path, templateFor(path), '')
    await loadFileList()
    selectFile(path)
    newFilePath.value = ''
  } catch (e) {
    newFileError.value = String(e)
  } finally {
    newFileBusy.value = false
  }
}

// 三個 AI 輔助按鈕：呼叫 SpecAssist(provider,'spec_assist',prompt)，輸出經
// EventsOn('workbench:event') → App.vue routeEnvelope → assist store 累積。
// SpecAssist 現在直接回傳這次呼叫的 correlation_id（app.go 的 gen.correlationID）
// ——不再靠「await 前後 diff assist.drafts 的 key」推測，那個做法不可靠：Wails
// 不保證事件已送達／processed 才 resolve method 的 Promise，草稿可能永遠綁不上。
//
// 換檔競態（fix round 2，沿用）：await 期間操作者可能已切到另一個檔案——
// resetDraft() 會在切檔當下清掉 currentCorrelationId，這裡仍先記下
// startedForPath，await 後只在 effectivePath 沒變時才採用回傳的 id；變了就視
// 為操作者已經放棄這次結果，草稿留空。
async function runAssist(prompt: string) {
  assistError.value = ''
  assistBusy.value = true
  const startedForPath = effectivePath.value
  try {
    const id = await SpecAssist(s.provider, 'spec_assist', prompt)
    if (id && effectivePath.value === startedForPath) {
      currentCorrelationId.value = id
    }
  } catch (e) {
    assistError.value = String(e)
  } finally {
    assistBusy.value = false
  }
}

function draftGherkin() {
  void runAssist(`草擬 ${effectivePath.value || '(未選檔)'} 的 Gherkin 內容：\n${fileContent.value}`)
}
function detectAmbiguity() {
  void runAssist(`偵測以下 spec 內容的歧義：\n${fileContent.value}`)
}
function checkOracleCoverage() {
  void runAssist(`檢查以下 spec 內容的 oracle 覆蓋：\n${fileContent.value}`)
}

// Accept：草稿寫入檔案的唯一入口（spec §5.1 不變量——AI 輸出不直接寫檔）。只取
// draft 裡 ```gherkin/```feature（或退而求其次的通用 ``` code fence）的內容，
// 不把 assistant 的整段 prose（例如「我沒辦法直接讀寫檔案…」）一起寫進 .feature。
function unsavedKeep() { pendingPath.value = null } // 保留：停留原檔，新檔不載入

function unsavedDiscard() {
  const p = pendingPath.value
  pendingPath.value = null
  if (!p) return
  selectedPath.value = p
  resetDraft()
  void loadFile()
}

async function acceptDraft() {
  if (busyReason.value !== '') return
  acceptError.value = ''
  const writer = props.write ?? SpecWrite
  fileContent.value = extractGherkin(draftText.value)
  syncEditorDoc()
  const P = { path: effectivePath.value, content: fileContent.value, digest: fileDigest.value }
  busyReason.value = 'accept'
  try {
    const newDigest = await writer(P.path, P.content, P.digest)
    savedContent.value = P.content // 送出時內容，不是回應到達當下的 fileContent（等待期間可能已續打）
    fileDigest.value = newDigest
    currentCorrelationId.value = null
  } catch (e) {
    // 失敗：savedContent／fileDigest 不變；fileContent 保留接受後（送出前）內容，
    // 即使使用者等待期間又續打，續打結果也不還原——buffer 是使用者目前看到的
    // 內容，不因失敗而回捲。
    acceptError.value = String(e)
  } finally {
    busyReason.value = ''
  }
}

// saveFile：手動儲存——buffer（fileContent，CM6 目前內容）＋目前 digest 於按下當下
// 凍結成送出快照 P，成功後 savedContent／fileDigest 更新為 P 的值（不是回應到達
// 當下可能已被續打改變的 fileContent／fileDigest）；失敗三者皆不變、不自動重載。
async function saveFile() {
  if (busyReason.value !== '') return
  saveError.value = '' // A2：新操作開始清同類舊 transient error（與 acceptDraft 一致）
  saveConflict.value = false
  const writer = props.write ?? SpecWrite
  const P = { path: effectivePath.value, content: fileContent.value, digest: fileDigest.value }
  busyReason.value = 'save'
  try {
    const newDigest = await writer(P.path, P.content, P.digest)
    savedContent.value = P.content
    fileDigest.value = newDigest
    saveError.value = ''
    saveConflict.value = false
  } catch (e) {
    saveError.value = String(e)
    saveConflict.value = isWriteConflict(e)
  } finally {
    busyReason.value = ''
  }
}

async function submitForApproval() {
  submitError.value = ''
  submitBusy.value = true
  try {
    submitResult.value = await SubmitForApproval()
  } catch (e) {
    submitError.value = String(e)
  } finally {
    submitBusy.value = false
  }
}

async function previewCommit() {
  commitError.value = ''
  commitBusy.value = true
  try {
    const res = await PreviewSpecCommit()
    commitToken.value = res.token
    commitDiff.value = res.diff
  } catch (e) {
    commitError.value = String(e)
  } finally {
    commitBusy.value = false
  }
}

async function confirmCommit() {
  if (!commitToken.value) return
  commitError.value = ''
  commitBusy.value = true
  try {
    await ConfirmSpecCommit(commitToken.value, commitMessage.value)
    commitToken.value = null
    commitDiff.value = ''
    commitMessage.value = ''
  } catch (e) {
    commitError.value = String(e)
  } finally {
    commitBusy.value = false
  }
}
</script>

<template>
  <div class="spec-workspace" :data-busy="busyReason">
    <div class="new-file">
      <input v-model="newFilePath" data-test="new-file-path" :placeholder="t('newFile.path.placeholder')" />
      <button
        type="button" data-test="new-file-submit" :disabled="newFileBusy || !newFilePath || !newFileInScope"
        @click="createNewFile"
      >{{ t('newFile.action.create') }}</button>
      <span v-if="newFilePath && !newFileInScope" class="err" data-test="new-file-scope-hint">{{ t('newFile.scopeHint') }}</span>
    </div>
    <p v-if="newFileError" class="err" data-test="new-file-error">{{ newFileError }}</p>

    <div class="files">
      <button v-for="f in files" :key="f.path" :class="{ active: f.path === effectivePath }"
        @click="selectFile(f.path)">{{ f.name }}</button>
    </div>

    <div
      ref="editorHost" class="editor" data-test="editor-host"
      :data-editing-suspended="busyReason === 'load' ? 'true' : undefined"
    />
    <p v-if="loadError" class="err">{{ loadError }}</p>

    <div v-if="pendingPath" class="unsaved-guard" data-test="unsaved-guard">
      <p>{{ t('unsaved.message') }}</p>
      <button data-test="unsaved-keep" @click="unsavedKeep">{{ t('unsaved.action.keep') }}</button>
      <button data-test="unsaved-discard" @click="unsavedDiscard">{{ t('unsaved.action.discard') }}</button>
    </div>

    <div class="save-area">
      <button data-test="save" :disabled="busyReason !== '' || !dirty" @click="saveFile">{{ t('spec.action.save') }}</button>
    </div>
    <p v-if="saveError" class="err" data-test="save-error" :data-conflict="saveConflict ? 'true' : undefined">{{ saveError }}</p>

    <div class="assist-buttons">
      <button data-test="assist-draft" :disabled="assistBusy" @click="draftGherkin">{{ t('spec.action.draftGherkin') }}</button>
      <button data-test="assist-ambiguity" :disabled="assistBusy" @click="detectAmbiguity">{{ t('spec.action.detectAmbiguity') }}</button>
      <button data-test="assist-oracle" :disabled="assistBusy" @click="checkOracleCoverage">{{ t('spec.action.checkOracle') }}</button>
    </div>
    <p v-if="assistError" class="err">{{ assistError }}</p>

    <div class="draft-area">
      <p v-if="assistBusy" class="assist-busy" data-test="assist-busy">{{ t('spec.assist.drafting') }}</p>
      <pre class="draft-text" data-test="draft-text">{{ draftText }}</pre>
      <button data-test="accept-draft" :disabled="!draftText || busyReason !== ''" @click="acceptDraft">{{ t('spec.action.acceptDraft') }}</button>
    </div>
    <p v-if="acceptError" class="err">{{ acceptError }}</p>

    <div class="approval">
      <button data-test="submit-for-approval" :disabled="submitBusy" @click="submitForApproval">{{ t('spec.action.submit') }}</button>
      <span v-if="submitResult" class="ok">{{ t('spec.submittedApprovalId', { id: submitResult }) }}</span>
    </div>
    <p v-if="submitError" class="err">{{ submitError }}</p>

    <div class="commit">
      <button data-test="preview-commit" :disabled="commitBusy" @click="previewCommit">{{ t('spec.action.previewCommit') }}</button>
      <pre v-if="commitDiff" class="diff" data-test="commit-diff">{{ commitDiff }}</pre>
      <template v-if="commitToken">
        <input v-model="commitMessage" data-test="commit-message" :placeholder="t('spec.commitMessage.placeholder')" />
        <button data-test="confirm-commit" :disabled="commitBusy" @click="confirmCommit">{{ t('spec.action.confirmCommit') }}</button>
      </template>
    </div>
    <p v-if="commitError" class="err">{{ commitError }}</p>
  </div>
</template>

<style scoped>
.spec-workspace { display: flex; flex-direction: column; gap: 8px; padding: 8px; text-align: left; height: 100%; overflow-y: auto; }
.new-file { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.files { display: flex; gap: 4px; flex-wrap: wrap; }
.files button.active { background: var(--bg-bubble-user); color: #fff; }
.editor { height: 280px; min-height: 120px; border: 1px solid var(--border); border-radius: var(--radius-s); overflow: hidden; }
.editor :deep(.cm-editor) { height: 100%; }
.editor :deep(.cm-scroller) { overflow: auto; }
.assist-buttons { display: flex; gap: 6px; }
.draft-area { display: flex; flex-direction: column; gap: 4px; }
.draft-text { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); min-height: 60px; max-height: 240px; overflow-y: auto; }
.diff { white-space: pre-wrap; background: var(--bg-inset); padding: 8px; border-radius: var(--radius-s); max-height: 240px; overflow-y: auto; }
.assist-busy { color: var(--text-muted); font-size: var(--fs-s); }
.err { color: var(--err); font-size: var(--fs-s); }
.ok { color: var(--text-muted); font-size: var(--fs-s); }
</style>
