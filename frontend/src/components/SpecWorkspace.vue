<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ConfirmSpecCommit, PreviewSpecCommit, SpecAssist, SpecList, SpecRead, SpecWrite, SubmitForApproval,
} from '../../wailsjs/go/main/App'
import type { main, spec } from '../../wailsjs/go/models'
import { useSession } from '../stores/session'
import { useAssist } from '../stores/assist'
import { extractGherkin } from '../lib/gherkin'

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

const s = useSession()
const assist = useAssist()

const files = ref<main.FileNode[]>([])
const selectedPath = ref('')
const effectivePath = computed(() => props.path ?? selectedPath.value)

const fileContent = ref('')
const fileDigest = ref('')
const loadError = ref('')

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
  try {
    const sf = await SpecRead(effectivePath.value)
    fileContent.value = sf.content
    fileDigest.value = sf.digest
    syncEditorDoc()
  } catch (e) {
    loadError.value = String(e)
  }
}

function syncEditorDoc() {
  if (!cmView) return
  cmView.dispatch({ changes: { from: 0, to: cmView.state.doc.length, insert: fileContent.value } })
}

async function initEditor() {
  if (!editorHost.value) return
  try {
    const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
      import('codemirror'),
      import('@codemirror/state'),
    ])
    cmView = new EditorView({
      state: EditorState.create({ doc: fileContent.value, extensions: [basicSetup] }),
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
watch(() => props.path, () => {
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
  selectedPath.value = p
  resetDraft()
  if (!props.path) void loadFile()
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
async function acceptDraft() {
  acceptError.value = ''
  const writer = props.write ?? SpecWrite
  const content = extractGherkin(draftText.value)
  try {
    const newDigest = await writer(effectivePath.value, content, fileDigest.value)
    fileDigest.value = newDigest
    fileContent.value = content
    syncEditorDoc()
    currentCorrelationId.value = null
  } catch (e) {
    acceptError.value = String(e)
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
  <div class="spec-workspace">
    <div class="files">
      <button v-for="f in files" :key="f.path" :class="{ active: f.path === effectivePath }"
        @click="selectFile(f.path)">{{ f.name }}</button>
    </div>

    <div ref="editorHost" class="editor" data-test="editor-host" />
    <p v-if="loadError" class="err">{{ loadError }}</p>

    <div class="assist-buttons">
      <button data-test="assist-draft" :disabled="assistBusy" @click="draftGherkin">草擬 Gherkin</button>
      <button data-test="assist-ambiguity" :disabled="assistBusy" @click="detectAmbiguity">歧義偵測</button>
      <button data-test="assist-oracle" :disabled="assistBusy" @click="checkOracleCoverage">oracle 覆蓋檢查</button>
    </div>
    <p v-if="assistError" class="err">{{ assistError }}</p>

    <div class="draft-area">
      <p v-if="assistBusy" class="assist-busy" data-test="assist-busy">AI 產生中…</p>
      <pre class="draft-text" data-test="draft-text">{{ draftText }}</pre>
      <button data-test="accept-draft" :disabled="!draftText" @click="acceptDraft">Accept</button>
    </div>
    <p v-if="acceptError" class="err">{{ acceptError }}</p>

    <div class="approval">
      <button data-test="submit-for-approval" :disabled="submitBusy" @click="submitForApproval">Submit for Approval</button>
      <span v-if="submitResult" class="ok">approval_id: {{ submitResult }}</span>
    </div>
    <p v-if="submitError" class="err">{{ submitError }}</p>

    <div class="commit">
      <button data-test="preview-commit" :disabled="commitBusy" @click="previewCommit">Preview Commit</button>
      <pre v-if="commitDiff" class="diff" data-test="commit-diff">{{ commitDiff }}</pre>
      <template v-if="commitToken">
        <input v-model="commitMessage" data-test="commit-message" placeholder="commit message" />
        <button data-test="confirm-commit" :disabled="commitBusy" @click="confirmCommit">Confirm Commit</button>
      </template>
    </div>
    <p v-if="commitError" class="err">{{ commitError }}</p>
  </div>
</template>

<style scoped>
.spec-workspace { display: flex; flex-direction: column; gap: 8px; padding: 8px; text-align: left; height: 100%; overflow-y: auto; }
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
