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
import { createExternalChangeGuard, shouldApplyBackground, type SyncState } from '../lib/externalChangeGuard'
import ExternalChangeChoice from './ExternalChangeChoice.vue'
import ExternalChangeCompare from './ExternalChangeCompare.vue'

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
  // A2-1：由 App 注入「完成後重載收件匣」的包裝版本；未注入時回退直呼（同 write）。
  // SpecAssist 不注入：specAssist 沒有建立／解除 blocker 的路徑（D7）。
  submit?: () => Promise<string>
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

// A1b-1：外部檔案變更防護——所有權／背景世代／B1–B4 核對語意全部由
// externalChangeGuard 提供（見該檔頂端設計理由），這裡只負責接線與 UI 呈現。
const guard = createExternalChangeGuard()
// 同步三值僅供內部判定，不外顯為常駐 UI 狀態或 badge（設計稿條款 14）；不必是
// reactive ref，模板不依賴它。
let syncState: SyncState = 'unknown'
// 背景檢查（視窗取得焦點）產生的使用者可見訊息：已同步時清空；偵測到變更時
// 依當下是否有未儲存內容分流 notice（已自動重載）／detected（保留本地未覆寫）；
// 讀取失敗／檔案已刪除則顯示 readFailed／deleted。三個檢查點（回到工作區／視窗
// 取得焦點／寫入前）一律無條件重讀並比對 digest，不靠任何事件訂閱標記閘控
// （沒有消費端的旗標不留——修正輪 owner 裁定）。
const externalChangeNotice = ref('')
// externalChangeClass：F1 修正（J4）——external-change 過去固定 class="notice"，
// 導致讀取失敗／已刪除也顯示為淡色。依訊息類別分流：notice／detected／
// reloadKeptNewInput 是資訊（notice），readFailed／deleted 是錯誤（err）。
const externalChangeClass = ref<'notice' | 'err'>('notice')
// externalAbortMessage：寫入前檢查（saveFile／acceptDraft）中止本次寫入的
// 原因——writeAborted（偵測到外部變更）或 readFailed／deleted（預檢讀取失敗）。
// 與 externalChangeNotice（背景檢查結果）分開呈現，兩者互不覆寫；save-error／
// accept-error 只保留給 writer 真的被呼叫後才失敗的情況（含後端 digest 衝突）。
const externalAbortMessage = ref('')

// A1b-2（J1）：三選一是否顯示——背景 detected 與前景 writeAborted 兩處都會打開；
// 讀取失敗／已刪除不打開。新操作開始（saveFile／acceptDraft／loadFile）時關閉，
// 對應訊息一併清除的同一時機。
const showChoice = ref(false)
// reloadInProgress：重新載入進行中——只用來讓三選一按鈕 disabled，避免重複點擊
// （§2.4.4 明確 reload 要求）。★ 刻意不進 busyReason／setEditable(false)：
// 一旦設 busy='load' 編輯器會變不可編輯，使用者就不可能在點擊重新載入後續打，
// §2.4「點擊後新增的輸入」的捨棄範圍判斷會變成不可能發生的情境。
const reloadInProgress = ref(false)

// A1b-2（J3）：比較（compare）唯讀並列兩欄狀態。左欄在開啟當下凍結、右欄在
// 開啟時重新讀取磁碟後凍結；compareGen 是比較世代——只用來讓「關閉後又重新
// 開啟」時舊的讀取回應不會填進新的比較（不影響 guard 的背景世代）。
const showCompare = ref(false)
const compareLeft = ref('')
const compareLeftCapturedAt = ref<Date>(new Date())
const compareRight = ref<string | null>(null)
const compareRightCapturedAt = ref<Date | null>(null)
const compareRightError = ref('')
const compareRightLoading = ref(false)
let compareGen = 0

// isDeletedError：以錯誤文字含 "no such file" 判定為「檔案已刪除」——對齊
// os.ReadFile 對不存在檔案的錯誤文字（"open ...: no such file or directory"），
// 也對齊本檔測試 makeFileStore 的錯誤字串慣例（`no such file ${path}`）。
function isDeletedError(e: unknown): boolean {
  return String(e).toLowerCase().includes('no such file')
}

// checkExternalChangeInBackground：背景檢查點（視窗聚焦）共用實作——「回到
// 工作區」已由既有 onMounted → loadFile() 滿足，不再另外處理（裁定 13）。
async function checkExternalChangeInBackground() {
  // canStartBackgroundRead() 只認得 guard 前景所有權（saveFile／acceptDraft 的
  // acquire）；loadFile 的 busy='load' 並未呼叫 guard.acquire()，所以另外檢查
  // busyReason——避免背景讀取與進行中的載入互踩（讀到載入中途的 fileDigest／
  // path 當基準）。
  if (!guard.canStartBackgroundRead() || busyReason.value !== '') return
  const path = effectivePath.value
  if (!path) return
  const baseline = fileDigest.value
  const stamp = guard.snapshot(path, baseline, true) // 發出背景讀取：遞增背景世代
  let disk: { content: string; digest: string } | null = null
  let readErr: unknown = null
  try {
    disk = await SpecRead(path) // 無條件重讀，不看 spec:changed 標記
  } catch (e) {
    readErr = e
  }
  // B1–B4 四項核對：任一不符（含前景取得所有權時已使世代失效、期間切檔、
  // 期間卸載、期間寫入成功使基準更新）即完全 no-op，不改任何狀態。
  // 比對用快照必須讀「目前」的 effectivePath——用發出時捕捉的 path 區域變數會讓
  // B2 變成恆真式，期間切到「內容剛好相同」的另一個檔時 B4 也擋不住（兩檔 digest
  // 相同），舊回應就會被套到新檔上。
  //
  // B3（guard.isActive()）必須另外查：dispose() 不改變 instanceId 與世代，四欄
  // 比對在卸載後仍可能全等，光靠 shouldApplyBackground 擋不住卸載後才返回的回應
  // （修正輪修正 1）——成功／失敗兩條路徑共用這一個檢查點，都涵蓋到。
  if (!guard.isActive() || !shouldApplyBackground(stamp, guard.snapshot(effectivePath.value, fileDigest.value))) return

  if (readErr !== null) {
    syncState = 'unknown'
    externalChangeNotice.value = isDeletedError(readErr)
      ? t('externalChange.deleted')
      : t('externalChange.readFailed', { error: String(readErr) })
    externalChangeClass.value = 'err' // F1（J4）：讀取失敗／已刪除是錯誤類
    showChoice.value = false // 讀取失敗／已刪除不顯示三選一（J1）
    return
  }
  if (disk!.digest === fileDigest.value) {
    syncState = 'insync'
    externalChangeNotice.value = '' // 已同步：靜默清除標記
    showChoice.value = false // 已同步，不再有可供選擇的外部變更
    return
  }
  syncState = 'diverged'
  if (!dirty.value) {
    // 當下無未儲存內容 → 自動重載為磁碟版本＋非阻斷告知
    fileContent.value = disk!.content
    savedContent.value = disk!.content
    fileDigest.value = disk!.digest
    syncEditorDoc()
    syncState = 'insync' // 已依磁碟內容重載，基準重新與磁碟相符
    externalChangeNotice.value = t('externalChange.notice')
    externalChangeClass.value = 'notice' // F1（J4）：資訊類
    showChoice.value = false // 已自動重載，沒有需要選擇的東西
  } else {
    // 當下有未儲存內容 → 不覆寫，僅提示＋【A1b-2】三選一（J1 背景分流）
    externalChangeNotice.value = t('externalChange.detected')
    externalChangeClass.value = 'notice' // F1（J4）：資訊類
    showChoice.value = true
  }
}

function onWindowFocus() {
  void checkExternalChangeInBackground()
}

async function loadFileList() {
  try {
    files.value = (await SpecList()) ?? []
  } catch (e) {
    loadError.value = String(e)
  }
}

async function loadFile() {
  // A1b-1 修正輪修正 4：loadFile 的 busy='load' 並未呼叫 guard.acquire()，此前
  // 已在途的背景讀取無法靠所有權失效——必須在操作開始當下（這裡）就使其失效，
  // 不能等回應到達時看 busy：loadFile 可能已結束、busy 已清，回應才到，那時光
  // 看 busy 已經擋不住。
  guard.invalidateBackground()
  loadError.value = ''
  // 換檔／重載：上一個檔的外部變更訊息與中止訊息都不得留在畫面上被誤讀成新檔
  // 的狀態。背景自動重載不走這裡（直接改 fileContent／savedContent／fileDigest），
  // 所以它設的 notice 不會被這行清掉。
  externalChangeNotice.value = ''
  externalChangeClass.value = 'notice'
  externalAbortMessage.value = ''
  // A1b-2（重設時機）：三選一、比較及其暫存狀態隨切檔／重載一併重設——上一個
  // 檔的選擇畫面不得殘留在新檔上。compareGen 遞增讓任何在途的舊比較右欄讀取
  // 之後被判定為過期，即使已被 showCompare=false 擋住也不留隱患。
  showChoice.value = false
  showCompare.value = false
  compareGen += 1
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
  window.addEventListener('focus', onWindowFocus) // A1b-1：視窗取得焦點＝背景檢查點之一（比照 PlanWorkspace.vue 的 focus listener 形狀）
})
onBeforeUnmount(() => {
  cmView?.destroy()
  window.removeEventListener('focus', onWindowFocus)
  guard.dispose()
})
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
  // 確認框開啟後才開始的寫入也要擋（確認框不阻止按儲存／接受草稿／確認 bump）：
  // 執行前重新檢查，且在確定切檔前不改 selectedPath、不動 buffer；pendingPath
  // 保留，寫入結束後可再按一次捨棄。
  if (busyReason.value !== '') return
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
  externalAbortMessage.value = '' // 新操作開始，清掉上次中止提示（修正輪修正 2）
  showChoice.value = false // A1b-2（重設時機）：新的寫入操作開始，對應的三選一也關閉
  showCompare.value = false
  const writer = props.write ?? SpecWrite
  fileContent.value = extractGherkin(draftText.value)
  syncEditorDoc()
  const P = { path: effectivePath.value, content: fileContent.value, digest: fileDigest.value }
  // A1b-1：取得前景所有權——涵蓋「預檢 → 寫入 → 結果套用」整段；副作用是使此前
  // 在途的背景讀取立即失效（擋 C3）。等待期間使用者仍可續打，不使本次寫入失效。
  const token = guard.acquire()
  busyReason.value = 'accept'
  try {
    let disk: { content: string; digest: string } | null = null
    let readErr: unknown = null
    try {
      disk = await SpecRead(P.path) // 寫入前預檢：無條件重讀，不看 spec:changed 標記
    } catch (e) {
      readErr = e
    }
    // 已失去所有權（理論防線：busyReason 互斥已擋住同類競爭，僅防禦卸載期間的
    // 遲到回應）→ 完全 no-op，不改任何狀態。
    if (!guard.isOwner(token)) return

    if (readErr !== null) {
      // 讀取失敗／檔案已刪除：中止本次寫入，保留三者，顯示原始訊息——writer
      // 尚未被呼叫，不算實際寫入失敗，改用獨立的中止呈現點（修正輪修正 2）。
      externalAbortMessage.value = isDeletedError(readErr) ? t('externalChange.deleted') : t('externalChange.readFailed', { error: String(readErr) })
      return
    }
    if (disk!.digest !== P.digest) {
      // 磁碟已被外部改動——一律中止，不看當下 dirty，不呼叫 writer，保留三者
      externalAbortMessage.value = t('externalChange.writeAborted')
      showChoice.value = true // 【A1b-2】前景中止在中止提示上加三選一（J1）
      return
    }

    const newDigest = await writer(P.path, P.content, P.digest)
    if (!guard.isOwner(token)) return
    savedContent.value = P.content // 送出時內容，不是回應到達當下的 fileContent（等待期間可能已續打）
    fileDigest.value = newDigest
    currentCorrelationId.value = null
  } catch (e) {
    if (!guard.isOwner(token)) return
    // 失敗：savedContent／fileDigest 不變；fileContent 保留接受後（送出前）內容，
    // 即使使用者等待期間又續打，續打結果也不還原——buffer 是使用者目前看到的
    // 內容，不因失敗而回捲。
    acceptError.value = String(e)
  } finally {
    guard.release(token)
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
  externalAbortMessage.value = '' // 新操作開始，清掉上次中止提示（修正輪修正 2）
  showChoice.value = false // A1b-2（重設時機）：新的寫入操作開始，對應的三選一也關閉
  showCompare.value = false
  const writer = props.write ?? SpecWrite
  const P = { path: effectivePath.value, content: fileContent.value, digest: fileDigest.value }
  // A1b-1：取得前景所有權（同 acceptDraft，理由見該處註解）。
  const token = guard.acquire()
  busyReason.value = 'save'
  try {
    let disk: { content: string; digest: string } | null = null
    let readErr: unknown = null
    try {
      disk = await SpecRead(P.path) // 寫入前預檢：無條件重讀，不看 spec:changed 標記
    } catch (e) {
      readErr = e
    }
    if (!guard.isOwner(token)) return // 已失去所有權：完全 no-op（理由同 acceptDraft）

    if (readErr !== null) {
      // writer 尚未被呼叫，不算實際寫入失敗，改用獨立的中止呈現點（修正輪修正 2）。
      externalAbortMessage.value = isDeletedError(readErr) ? t('externalChange.deleted') : t('externalChange.readFailed', { error: String(readErr) })
      return
    }
    if (disk!.digest !== P.digest) {
      // 磁碟已被外部改動——一律中止，不看當下 dirty，不呼叫 writer，保留三者
      externalAbortMessage.value = t('externalChange.writeAborted')
      showChoice.value = true // 【A1b-2】前景中止在中止提示上加三選一（J1）
      return
    }

    const newDigest = await writer(P.path, P.content, P.digest)
    if (!guard.isOwner(token)) return
    savedContent.value = P.content
    fileDigest.value = newDigest
    saveError.value = ''
    saveConflict.value = false
  } catch (e) {
    if (!guard.isOwner(token)) return
    saveError.value = String(e)
    saveConflict.value = isWriteConflict(e)
  } finally {
    guard.release(token)
    busyReason.value = ''
  }
}

// A1b-2（§2.6「保留本地」）：保留編輯內容，fileDigest 不變、不呼叫 writer；只
// 關閉提示（清掉 detected／writeAborted 訊息、隱藏三選一與比較），操作結束。
// 狀態維持「已分歧」——不特別改動任何基準，之後再按儲存時，寫入前檢查會用
// 同一個 fileDigest 重新核對，磁碟仍不符就會再次中止（不暗中續寫）。
function onChoiceKeep() {
  externalChangeNotice.value = ''
  externalAbortMessage.value = ''
  showChoice.value = false
  showCompare.value = false
}

// A1b-2（J2、§2.4 明確 reload 的額外要求）：重新載入。點擊當下凍結「使用者已
// 同意捨棄的 buffer 快照」與目前路徑，取得前景所有權（涵蓋此前在途背景讀取
// 失效，享有 C1／C3 同等保護），無條件重新讀取磁碟（不套用任何先前讀到的
// 快照）。回應到達時若已失去所有權／guard 已卸載／路徑已變，三者完全不改動；
// 若 buffer 已不等於點擊當下凍結的快照（點擊後又打字），不得捨棄新輸入，改為
// 顯示 reloadKeptNewInput 並讓三選一維持開啟，供使用者重新選擇。
// ★ 刻意不設 busyReason='load'：編輯器必須維持可編輯，見 reloadInProgress 宣告
// 處的說明。
async function onChoiceReload() {
  const path = effectivePath.value
  if (!path) return
  const frozenSnapshot = fileContent.value // 已同意捨棄的 buffer 快照
  const token = guard.acquire()
  reloadInProgress.value = true
  try {
    let disk: { content: string; digest: string } | null = null
    let readErr: unknown = null
    try {
      disk = await SpecRead(path) // 重新讀取磁碟——不得套用提示當時讀到的任何快照
    } catch (e) {
      readErr = e
    }
    if (!guard.isOwner(token) || !guard.isActive() || effectivePath.value !== path) return // 完全不改狀態

    if (readErr !== null) {
      externalChangeNotice.value = isDeletedError(readErr)
        ? t('externalChange.deleted')
        : t('externalChange.readFailed', { error: String(readErr) })
      externalChangeClass.value = 'err' // F1（J4）：錯誤類
      return // 三者不變；三選一維持開啟
    }

    if (fileContent.value !== frozenSnapshot) {
      // 點擊後、回應前使用者又打字——不得套用磁碟內容，也不得捨棄新增的輸入，
      // 須重新提示讓使用者再選一次。
      externalChangeNotice.value = t('externalChange.reloadKeptNewInput')
      externalChangeClass.value = 'notice' // F1（J4）：資訊類
      return // 三選一維持開啟
    }

    fileContent.value = disk!.content
    savedContent.value = disk!.content
    fileDigest.value = disk!.digest
    syncEditorDoc()
    externalChangeNotice.value = ''
    externalAbortMessage.value = ''
    showChoice.value = false
    showCompare.value = false
  } finally {
    guard.release(token)
    reloadInProgress.value = false
  }
}

// A1b-2（J3、§2.9）：比較。開啟當下凍結左欄（目前 buffer），並在開啟時重新
// 讀取磁碟填右欄——不 acquire()：比較只讀、不改任何基準。compareGen 讓「關閉
// 後又重新開啟」時舊的讀取回應不會填進新的比較；guard.isActive()／路徑未變／
// showCompare 仍為開／世代相符四項皆須通過才套用，任一不符即完全丟棄（不沿用
// 任何舊內容）。
async function onChoiceCompare() {
  const path = effectivePath.value
  const myGen = ++compareGen
  compareLeft.value = fileContent.value
  compareLeftCapturedAt.value = new Date()
  compareRight.value = null
  compareRightCapturedAt.value = null
  compareRightError.value = ''
  compareRightLoading.value = true
  showCompare.value = true
  if (!path) {
    compareRightLoading.value = false
    return
  }
  const stillValid = () => guard.isActive() && effectivePath.value === path && showCompare.value && myGen === compareGen
  try {
    const disk = await SpecRead(path)
    if (!stillValid()) return
    compareRight.value = disk.content
    compareRightCapturedAt.value = new Date()
    compareRightError.value = ''
  } catch (e) {
    if (!stillValid()) return
    compareRightError.value = String(e) // 讀取失敗：保留本地內容並顯示失敗，不沿用任何舊內容
    compareRight.value = null
    compareRightCapturedAt.value = null
  } finally {
    if (stillValid()) compareRightLoading.value = false
  }
}

// 關閉比較：回到三選一；唯讀操作，不改 fileContent／savedContent／fileDigest。
function closeCompareView() {
  showCompare.value = false
}

async function submitForApproval() {
  submitError.value = ''
  submitBusy.value = true
  try {
    submitResult.value = await (props.submit ?? SubmitForApproval)()
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
        :data-test="'file-tree-' + f.path" @click="selectFile(f.path)">{{ f.name }}</button>
    </div>

    <div
      ref="editorHost" class="editor" data-test="editor-host"
      :data-editing-suspended="busyReason === 'load' ? 'true' : undefined"
    />
    <p v-if="loadError" class="err">{{ loadError }}</p>
    <p v-if="externalChangeNotice" :class="externalChangeClass" data-test="external-change">{{ externalChangeNotice }}</p>
    <!-- A1b-1：外部檔案變更——external-change 是背景檢查（視窗聚焦）結果；
         external-abort 是寫入前檢查（saveFile／acceptDraft）中止本次寫入的原因
         （含預檢讀取失敗／檔案已刪除），與 save-error／accept-error（writer 真的
         被呼叫後才失敗）互不覆寫（修正輪修正 2，比照 PlanWorkspace 的
         externalAbortMessage）。 -->
    <p v-if="externalAbortMessage" class="err" data-test="external-abort">{{ externalAbortMessage }}</p>
    <!-- A1b-2：三選一疊在上面兩則訊息旁（J1）；比較開啟時暫代三選一，關閉後
         回到三選一。兩者共用元件不含任何讀寫邏輯，接線全在本檔。 -->
    <ExternalChangeChoice
      v-if="showChoice && !showCompare" :disabled="reloadInProgress"
      @reload="onChoiceReload" @compare="onChoiceCompare" @keep="onChoiceKeep"
    />
    <ExternalChangeCompare
      v-if="showCompare"
      :left="compareLeft" :left-captured-at="compareLeftCapturedAt"
      :right="compareRight" :right-captured-at="compareRightCapturedAt"
      :right-error="compareRightError" :right-loading="compareRightLoading"
      @close="closeCompareView"
    />

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
    <p v-if="acceptError" class="err" data-test="accept-error">{{ acceptError }}</p>

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
.notice { color: var(--text-muted); font-size: var(--fs-s); }
</style>
