import { defineStore } from 'pinia'
import { t } from '../i18n'
import type { Bindings, ChatItem, Envelope, PaneLayoutInfo, SessionInfo, TimelineItem } from '../types'

const NOISE_KINDS = new Set(['system_other', 'unknown'])
export const PROVIDERS = ['claude', 'codex'] as const
export type ProviderKey = (typeof PROVIDERS)[number]
// TURN_WINDOW_SIZE：§3.8 凍結的分頁大小，鏡射 Go 端 rebuild_orchestrator.go
// 的 turnPageSize（同一常數，兩邊各自持有——沒有共用型別可以跨語言匯入，
// 見 SessionList.vue 的 MAX_SESSIONS_PER_PROVIDER 同一慣例）。
const TURN_WINDOW_SIZE = 20

// SessionMeta：per-WSID 的 session 狀態（§3.8「非釘選只保留 metadata」的那一半）。
//
// 契約凍結（plan Task 26）：busy／unread／awaitingApproval／state 一律讀寫這裡，
// SessionView 只承載 transcript。解除釘選會釋放 view，凡是「解除釘選後仍須存在」
// 的欄位（身分、輸入欄位、resume）因此都掛在 meta 上而非 view 上。
export interface SessionMeta {
  wsid: string
  provider: ProviderKey
  taskLabel: string
  state: string
  unread: number
  busy: boolean
  awaitingApproval: boolean
  removed: boolean
  // 以下非 plan 明列，但屬「釋放 transcript 後仍須保留」的 per-session 狀態：
  sessionId: string // provider 回報的 session/thread id
  taskId: string
  active: boolean // 已 StartSession 且尚未收尾 → 下一輪走 SendMessage
  recordCase: string
  resume: string
  // available：registry 有 entry 但 Manager 沒有 committed slot 時為 false
  // （見 Go 端 SessionInfo doc）。false 時所有 lifecycle 呼叫都會
  // ErrSessionNotFound，store 因此在 submit 前就擋下並 fail loud。
  available: boolean
}

// SessionView：單一 WSID 的 transcript（只有釘選／transient 顯示中的才有）。
export interface SessionView {
  chat: ChatItem[]
  timeline: TimelineItem[]
  totals: { cost: number; input: number; output: number }
  usageSemantics: string
  noiseGroup: number
  nextGroup: number
}

function newView(): SessionView {
  return {
    chat: [], timeline: [],
    totals: { cost: 0, input: 0, output: 0 },
    usageSemantics: '', noiseGroup: -1, nextGroup: 0,
  }
}

function newMeta(wsid: string, provider: ProviderKey): SessionMeta {
  return {
    wsid, provider, taskLabel: '', state: 'idle', unread: 0, busy: false,
    awaitingApproval: false, removed: false,
    sessionId: '', taskId: '', active: false, recordCase: '', resume: '',
    available: true,
  }
}

// applyToView：把單一 envelope 疊進某 SessionView——transcript（chat／
// timeline／totals／usageSemantics／noise 分組）唯一的寫入邏輯。apply()
// （即時串流）與 §3.8 視窗化載入（pin()／loadOlder()，Task 29）共用同一份
// reducer：使用者當下看到的畫面跟重新載入歷史後看到的畫面若各走一套邏輯，
// 兩邊遲早會漂移不一致。**只碰 view，不碰 SessionMeta**（busy／unread／
// state／identity 是 apply() 自己在 switch 那段的職責；歷史回放不重放這些
// 副作用——重放時 unread 再 ++ 或 busy 被短暫撥動都是錯的，見 pin() 文件）。
function applyToView(v: SessionView, env: Envelope) {
  if (env.usage) {
    v.totals.input = env.usage.input_tokens
    v.totals.output = env.usage.output_tokens
  }
  if (env.usage_semantics) v.usageSemantics = env.usage_semantics

  if (env.kind === 'result') {
    v.totals.cost += env.cost_usd ?? 0
    const tail = v.chat.at(-1)
    if (tail && tail.role === 'assistant' && tail.streaming) {
      if (tail.text === '' && tail.thinking === '') v.chat.pop()
      else tail.streaming = false
    }
  }

  if (env.kind === 'delta') {
    const last = v.chat.at(-1)
    if (last && last.role === 'assistant' && last.streaming) {
      last.text += env.text ?? ''
      last.thinking += env.thinking ?? ''
    } else {
      v.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: true })
    }
    return // delta 不進 timeline
  }
  if (env.kind === 'message' && env.role === 'user') {
    v.chat.push({ role: 'user', text: env.text ?? '', thinking: '', streaming: false })
  } else if (env.kind === 'message' && env.role === 'assistant') {
    const last = v.chat.at(-1)
    if (last && last.role === 'assistant' && last.streaming) {
      last.text = env.text ?? last.text
      if (env.thinking) last.thinking = env.thinking
      last.streaming = false
    } else {
      v.chat.push({ role: 'assistant', text: env.text ?? '', thinking: env.thinking ?? '', streaming: false })
    }
  }
  // role==='tool' 的 message 只進 timeline

  if (NOISE_KINDS.has(env.kind)) {
    if (v.noiseGroup === -1) v.noiseGroup = v.nextGroup++
    v.timeline.push({ env, group: v.noiseGroup })
  } else {
    v.noiseGroup = -1
    v.timeline.push({ env })
  }
}

interface State {
  sessions: Record<string, SessionMeta>
  views: Record<string, SessionView>
  // persistentPins：使用者真正釘選的兩個 pane；pins 才是目前顯示的內容。
  // approval 的 transient presentation 只改 pins，persistentPins 永不被改寫
  // （§3.6.4）。
  persistentPins: [string | null, string | null]
  pins: [string | null, string | null]
  // focused：**畫面上正在作用的那個 pane**（composer、Enter、SettingsBar 的
  // End/Terminate/New 都跟著它，§3.7）。approval 路由會暫時改它。
  focused: 0 | 1
  // durableFocusPane：**使用者明確選定**的 pane——唯一會被寫進 registry
  // `layout.focused` 的來源，也是 approval 處理完之後 focused 要還原回去的目標。
  //
  // 為什麼要跟 focused 分開（owner 2026-08-17 裁決）：approval routing 是「系統
  // 為了處理待核可事件而做的暫時焦點切換」，不等於使用者選定啟動後要恢復的
  // pane。改版前 routeApproval 直接改 `focused` 而**當下不持久化**，於是重啟結果
  // 取決於「之後有沒有碰巧發生其他 layout 寫入」——那是三種語意裡最不穩定的一種
  // （既不是「不記住」也不是「記住」，而是「看運氣」）。
  durableFocusPane: 0 | 1
  transientPane: 0 | 1 | null
  scrollAnchors: Record<string, string>
  // notices：workspace scope 的事件（replay index degraded、codex broadcast、
  // 不屬於任何 session 的 UI 錯誤）。不屬於任何 lane，但必須到得了使用者——
  // 這是修掉「degraded 通知被 gate store 靜默丟棄」那條路徑的落點。
  notices: TimelineItem[]
  // unrouted：conversation lane 但沒帶 workspace_session_id 的事件數。丟棄是
  // 刻意的（沒有 lane 可放）。**目前沒有任何渲染端**——這個計數只在測試裡看得
  // 見，接進 UI 是 Task 28 的事（DualPane／狀態列）。在那之前，丟棄的事件對使
  // 用者仍是無聲的，稽核（events.jsonl）才是唯一完整的證據來源。
  unrouted: number
  // errorSeq：**單調遞增**的錯誤計數（Task 2a rev3 review）。App.vue 的
  // timeline 未讀 badge 以它為來源。刻意不從 `timeline` getter 投影推導——那個
  // getter 只讀 focused view，非 focus pane 的錯誤數不進去（漏報），而切換
  // pane 會讓數字上下跳動、把已讀的重新算成未讀（誤報）。
  //
  // 只在「事件真的被放進某個看得到的地方」時遞增；§3.8 的歷史回放（pin／
  // loadOlder 直接走 applyToView）不算——那些錯誤第一次抵達時已經數過了。
  errorSeq: number
  // latchSeq：**registry uncertain latch 專屬**的單調計數。App.vue 據此做
  // one-shot 強制展開 timeline。與 errorSeq 分開是因為成本不對稱：latch 是
  // 「必須重啟、之後每個 lifecycle 操作都會被拒」的狀態，使用者晚看到一分鐘
  // 就是多按 N 次都失敗，跟一般 stream_error 不同等級。
  latchSeq: number
  approvalPolicy: string
  bindings: Bindings | null
}

// REGISTRY_UNCERTAIN_MARK：app.go `errRegistryUncertain` 訊息裡的固定片語。
// 前端沒有別的辦法從一個 binding 錯誤字串判斷「這是 latch」——同步拒絕路徑
// （Create／Start／New／Remove）拿到的只有字串，沒有結構化欄位。
//
// 跨語言字面值共用，沿用 MAX_SESSIONS_PER_PROVIDER／TURN_WINDOW_SIZE 的既有
// 慣例；Go 端 `TestErrRegistryUncertainKeepsUIMarker` 守住它不漂移（改了 Go
// 訊息卻沒改這裡，那條測試會紅）。
const REGISTRY_UNCERTAIN_MARK = 'session registry 上一次寫入的結果不確定'

function providerOf(p: string | undefined): ProviderKey {
  return p === 'codex' ? 'codex' : 'claude'
}

function uiEnv(kind: string, over: Partial<Envelope>): Envelope {
  return {
    event_id: 'ui-' + kind + '-' + Math.random().toString(36).slice(2, 10),
    ts: new Date().toISOString(), provider: '', kind, ...over,
  }
}

export const useSession = defineStore('session', {
  state: (): State => ({
    sessions: {},
    views: {},
    persistentPins: [null, null],
    pins: [null, null],
    focused: 0,
    durableFocusPane: 0,
    transientPane: null,
    scrollAnchors: {},
    notices: [],
    unrouted: 0,
    errorSeq: 0,
    latchSeq: 0,
    approvalPolicy: 'untrusted',
    bindings: null,
  }),

  getters: {
    focusedWsid: (s): string => s.pins[s.focused] ?? '',
    focusedSession(): SessionMeta | null { return this.sessions[this.focusedWsid] ?? null },
    view(): SessionView | null { return this.views[this.focusedWsid] ?? null },
    // sessionList：既有（未移除）session，順序以 WSID 遞增——ULID 單調，等同建立
    // 順序，且不隨物件 key 插入順序漂移。
    sessionList: (s): SessionMeta[] =>
      Object.values(s.sessions).filter(m => !m.removed).sort((a, b) => (a.wsid < b.wsid ? -1 : 1)),

    // 以下是 focused pane 的轉發 getter（元件沿 M1 讀法）。
    provider(): ProviderKey { return this.focusedSession?.provider ?? 'claude' },
    chat(): ChatItem[] { return this.view?.chat ?? [] },
    // timeline：focused pane 的事件 ＋ workspace 通知。合併而非另開面板，是為了
    // 讓 workspace 級通知（degraded 等）不需要新元件就到得了使用者；代價是
    // notices 一律排在該 pane 事件之後，不與 event_id 交錯排序。
    timeline(): TimelineItem[] {
      const own = this.view?.timeline ?? []
      return this.notices.length ? [...own, ...this.notices] : own
    },
    totals(): { cost: number; input: number; output: number } {
      return this.view?.totals ?? { cost: 0, input: 0, output: 0 }
    },
    usageSemantics(): string { return this.view?.usageSemantics ?? '' },
    state(): string { return this.focusedSession?.state ?? 'idle' },
    sessionId(): string { return this.focusedSession?.sessionId ?? '' },
    taskId(): string { return this.focusedSession?.taskId ?? '' },
    busy(): boolean { return this.focusedSession?.busy ?? false },
    active(): boolean { return this.focusedSession?.active ?? false },
    taskLabel(): string { return this.focusedSession?.taskLabel ?? '' },
    recordCase(): string { return this.focusedSession?.recordCase ?? '' },
    resumeInput(): string { return this.focusedSession?.resume ?? '' },
    costDisplay(): string {
      const c = this.totals.cost
      return c > 0 ? '$' + c.toFixed(4) : '—'
    },
    unreadOf: (s) => (wsid: string): number => s.sessions[wsid]?.unread ?? 0,
    awaitingOf: (s) => (wsid: string): boolean => s.sessions[wsid]?.awaitingApproval ?? false,
    countOf(): (p: ProviderKey) => number {
      return (p: ProviderKey) => this.sessionList.filter(m => m.provider === p).length
    },
  },

  actions: {
    setBindings(b: Bindings) {
      this.bindings = b
    },

    // metaFor：取得（必要時建立）某 WSID 的 metadata。事件先於 registry 抵達時
    // 自動登記——把它丟掉會讓稽核裡有事件、UI 卻沒有這個 session。
    metaFor(wsid: string, provider: ProviderKey): SessionMeta {
      let m = this.sessions[wsid]
      if (!m) {
        m = newMeta(wsid, provider)
        this.sessions[wsid] = m
      }
      return m
    },

    // registerSession：登記／更新 durable metadata。**不碰 runtime 狀態**
    // （busy／unread／awaitingApproval／state 是 §3.2.1 的 in-memory 推導值，
    // registry hydrate 覆寫它們會把正在跑的 session 顯示成閒置）。
    registerSession(e: { wsid: string; provider: ProviderKey; taskLabel: string; resume?: string; available?: boolean }) {
      const m = this.metaFor(e.wsid, e.provider)
      m.provider = e.provider
      m.taskLabel = e.taskLabel
      if (e.resume !== undefined && e.resume !== '') m.resume = e.resume
      if (e.available !== undefined) m.available = e.available
    },

    // hydrateSessions：ListSessions() 的結果 → store。registry 是「有哪些
    // session」的權威，available 則反映 Manager 是否真的能定址它。
    hydrateSessions(list: SessionInfo[]) {
      for (const e of list) {
        this.registerSession({
          wsid: e.wsid, provider: providerOf(e.provider), taskLabel: e.task_label,
          resume: e.resume_session_id, available: e.available,
        })
        // 無條件覆寫：ListSessions 的 state 與 conversation lane 的 state_change
        // 是**同一個 reducer** 的輸出（值域相同），不存在「用 slot phase 蓋掉
        // runtime 狀態」的問題。available ⇔ state 非空則是 Go 端同一個賦值保證
        // 的（守門：TestListSessionsReconcilesRegistryWithSlots），因此這裡不再
        // 補 available 的三元判斷——那個分支在真實輸入下不可達，留著只會是一段
        // 沒有測試守得住的死碼。
        this.sessions[e.wsid].state = e.state
      }
    },

    // setFocus：**使用者明確點選 pane**（點 pane、Cmd+1/2、SessionList 點到已釘選
    // 的卡片）。
    //
    // 更新 durable focus 的入口有**兩個**：這裡與 `pin()`（owner 2026-08-17 追加
    // 裁決——「把 X 釘進第 N 格」同時明確選了 session 與 pane）。approval 的自動
    // 切換走 `routeApproval`，**不**更新 durable。
    setFocus(i: 0 | 1) {
      this.focused = i
      this.durableFocusPane = i
      const w = this.pins[i]
      if (w && this.sessions[w]) this.sessions[w].unread = 0
      void this.persistLayout()
    },

    // persistLayout：把「使用者真正選定的排列」寫進 registry（§3.2.1 白名單的
    // pane pins／focused pane）。**唯一的寫入點**——pin／unpin／setFocus／
    // markRemoved 都經過這裡。
    //
    // 送出的是 persistentPins **不是** pins：approval 的 transient secondary
    // presentation 會改 pins（§3.6.4），把它寫進 registry 等於讓一次核可對話
    // 永久改掉使用者的釘選——§3.6.4 明文「persistent pin 永不被 transient view
    // 改寫」，這裡是那條規則在 durable 層的落點。
    //
    // **失敗不回滾**（本票裁決，見 Go 端 SetPaneLayout doc）：pins 是 UI 偏好、
    // 不是 resume correctness。寫入被 registry uncertain latch 拒絕時，正確的
    // 降級是「這次的排列重啟後會遺失」，不是「不准釘選」。所以只把錯誤送進
    // notices lane（app-wide，不是 pane-scoped——latch 與 registry 不可用都不
    // 屬於任何單一 session，pushError 會把訊息掛到不相干的 pane 上並清掉它的
    // busy），畫面上的釘選照常生效。錯誤字串帶著 latch 片語，因此 bumpError
    // 會照常撥 latchSeq、強制展開 timeline。
    //
    // 不做 debounce：觸發點是離散的使用者手勢，速率上限就是人手點擊；每次寫入
    // 都在 binding 的非同步路徑上、不擋畫面。debounce 只會多欠一筆 shutdown 前
    // 要 flush 的義務（§3.6.5 的總序是凍結的），換不到有意義的東西。
    async persistLayout() {
      const set = this.bindings?.SetPaneLayout
      if (!set) return // dev 未 setBindings／測試字面量未提供：靜默跳過持久化
      const pins = [this.persistentPins[0] ?? '', this.persistentPins[1] ?? '']
      // 送 durableFocusPane 而非 focused：approval 的暫時焦點切換不得洩漏進
      // durable layout（§3.6.4 的 persistent／transient 分界，在 focus 這一維
      // 的落點）。索引的是 persistentPins，所以 transient pin 也不會被寫出去。
      try {
        await set(pins, pins[this.durableFocusPane])
      } catch (e) {
        this.pushNotice(t('store.paneLayoutPersistFailed', { error: String((e as Error)?.message ?? e) }))
      }
    },

    // restoreLayout：啟動時以 registry 的排列重建兩個釘選 pane（§3.8）。
    // App.vue 在 hydrateSessions() **之後**呼叫——pin() 需要該 WSID 的 meta 才
    // 能歸零 unread，而 meta 來自 ListSessions()。
    //
    // 跳過 sessions 裡沒有的 WSID：Go 端 PaneLayout() 已經濾掉 tombstone 與不存
    // 在的 entry，這裡是第二道——把一個沒有 metadata 的 WSID 釘上去會生出一個
    // 綁不到任何 session 的 pane（provider／狀態全部讀不到），比少還原一格糟。
    //
    // pin(..., false)：還原不重寫。逐格 pin 會先寫一次只有 pane 0 的排列、再寫
    // 一次兩格都有的，中途關掉 app 就把使用者的第二個釘選弄丟了；而且啟動本來
    // 就沒有任何新資訊要寫回磁碟。
    async restoreLayout(l: PaneLayoutInfo | null | undefined) {
      const pins = l?.pins ?? []
      for (const idx of [0, 1] as const) {
        const w = pins[idx] ?? ''
        if (!w || !this.sessions[w]) continue
        await this.pin(idx, w, false)
      }
      // 直接設而不呼叫 setFocus()：setFocus 會觸發 persistLayout（還原不回寫）。
      // 兩個都要設：durableFocusPane 是「使用者選定的那一格」，focused 是畫面
      // 當下的作用格，啟動時兩者相同。
      const at = this.pins.indexOf(l?.focused ?? '')
      if (at === 0 || at === 1) {
        this.focused = at
        this.durableFocusPane = at
      }
    },

    // pin：把 session 釘進 pane（persistent）。已有 view（例如同一 wsid 仍釘在
    // 另一 pane）就沿用既有 transcript，不重新載入；否則建立容器並觸發 §3.8
    // 尾端視窗 lazy load（Task 29——修掉 Task 26→28 刻意接受的暫時降級：重啟
    // 後重新釘選一個既有 session，畫面只有 metadata／unread／狀態是對的，歷史
    // 對話看不到，因為 pin() 過去只建空殼、從不回填）。沒有
    // bindings.LoadTurnsBefore（dev 尚未 setBindings、或測試 mock 未提供）時
    // 退回舊行為：空殼 view、不拋錯。
    // persist=false 只有一個呼叫端：restoreLayout()（見那裡的說明）。
    async pin(idx: 0 | 1, wsid: string, persist = true) {
      const prev = this.pins[idx]
      if (prev && prev !== wsid) this.releaseView(prev, idx)
      this.persistentPins[idx] = wsid
      this.pins[idx] = wsid
      if (this.transientPane === idx) this.transientPane = null
      // **釘選＝同時明確選了 session 與 pane**（owner 2026-08-17 裁決）。所以
      // 焦點（畫面與 durable 兩者）跟著移到被釘的那一格——這也消除了同一顆釘選鈕
      // 因為「這個 session 是否已釘選」而產生的兩種語意（已釘選走 setFocus 會移
      // 焦點、未釘選卻不會）。
      //
      // 只在 persist（＝使用者動作）時做。`persist === false` 的唯一呼叫端是
      // restoreLayout，那裡的焦點權威是 registry 的 `layout.focused`，不是「迴圈
      // 最後釘的那一格」——不擋的話，`layout.focused` 解析不出來時 durable 會落在
      // 一個任意的 pane 上。
      //
      // Pins 與 Focused **在同一次 persistLayout 送出**（owner 指定）：兩者都已
      // 更新完才呼叫，所以只會有一次 SetLayout、一次落盤，不是兩次 45ms。
      // 在 lazy load 之前就送出：持久化的是使用者的選擇，沒有理由等 transcript
      // 載完（那一段可能因為 index 重建而慢，甚至失敗）。
      if (persist) {
        this.focused = idx
        this.durableFocusPane = idx
        void this.persistLayout()
      }
      const isNew = !this.views[wsid]
      if (isNew) this.views[wsid] = newView()
      const m = this.sessions[wsid]
      if (m) m.unread = 0
      if (!isNew) return
      const load = this.bindings?.LoadTurnsBefore
      if (!load) return
      const envs = await load(wsid, '', TURN_WINDOW_SIZE)
      // await 期間可能已被切走／釋放，重新取一次——這是存在性檢查，不是身分
      // 檢查（不比對「這個 view 是不是我剛剛建立的那個實例」）。它正確防住的
      // 是「pin(A) 載入中，同一 pane 被切去 pin(B)」（releaseView(A) 會
      // delete views[A]，見下方 pin(A)→pin(B) 的測試）與「A 在等待期間被
      // RemoveSession／markRemoved 刪除」。它**沒有**防住「同一個 wsid 的
      // view 被刪除後又重建」（unpin → 再 pin 回同一個 wsid，此時 v 存在但是
      // 新實例，這批舊 envelope 會不去重地疊上去，因為只有 loadOlder() 做
      // event_id 去重、這裡的尾端載入沒有）。這條分支目前**不可達**：
      // unpin() 在整個 frontend/src 零 UI 呼叫端（SessionList／SettingsBar／
      // PaneView 都沒有「解除釘選」控制項）。只要日後任何票加一顆解除釘選
      // 按鈕，這個分支就會變可達，屆時需要在這裡也比對「view 是不是同一個
      // 尾端載入啟動時建立的那個實例」（例如帶一個 generation 計數）。
      const v = this.views[wsid]
      if (!v || !envs) return
      for (const e of envs) applyToView(v, e)
    },

    unpin(idx: 0 | 1) {
      const w = this.pins[idx]
      this.persistentPins[idx] = null
      this.pins[idx] = null
      if (this.transientPane === idx) this.transientPane = null
      if (w) this.releaseView(w, idx)
      void this.persistLayout()
    },

    // releaseView：釋放某 WSID 的 transcript——除非它同時釘在另一個 pane 上。
    // metadata、已讀狀態與捲動錨點一律保留（§3.8）。
    releaseView(wsid: string, exceptPane: 0 | 1) {
      const other = exceptPane === 0 ? 1 : 0
      if (this.pins[other] === wsid) return
      delete this.views[wsid]
    },

    setScrollAnchor(wsid: string, eventID: string) {
      this.scrollAnchors[wsid] = eventID
    },

    // loadOlder：使用者捲到頂——§3.8 向上分頁，游標是目前 timeline 最舊事件的
    // event_id（帶 cursor＝該 turn 之前的一頁，語意見 TurnWindowBindings 文件）。
    // 以 event_id 去重：TurnsBefore 契約上是開區間、不該與已載入的重疊，但這裡
    // 仍防禦性去重而不假設呼叫端一定遵守（這個 session 稍早的測試教訓：只寫在
    // 註解裡的保證＝沒有守門）。舊 turn 的 usage／cost 快照**不**回寫
    // totals／usageSemantics——那是「該 turn 當下」的值，覆寫畫面現在顯示的
    // 最新累計值只會讓 cost／token 顯示倒退，因此另建 scratch view 疊算，只取
    // 它的 chat／timeline 往前插入。
    async loadOlder(wsid: string) {
      const load = this.bindings?.LoadTurnsBefore
      const v = this.views[wsid]
      if (!load || !v) return
      const cursor = v.timeline[0]?.env.event_id ?? ''
      if (!cursor) return // 尚未有任何已載入事件：沒有游標可分頁
      const envs = await load(wsid, cursor, TURN_WINDOW_SIZE)
      if (!envs?.length) return
      const known = new Set(v.timeline.map(i => i.env.event_id))
      const fresh = envs.filter(e => !known.has(e.event_id))
      if (!fresh.length) return
      const scratch = newView()
      for (const e of fresh) applyToView(scratch, e)
      v.timeline.unshift(...scratch.timeline)
      v.chat.unshift(...scratch.chat)
    },

    // routeApproval（§3.6.4）：把核可請求的來源帶到使用者眼前。
    //  - 來源已釘選 → 自動切 focus 到那個 pane；
    //  - 來源未釘選 → 次要 pane（非 focused 那個）transient 顯示它，
    //    persistentPins 不動，focus 也不動（composer 留在使用者原本的 session）。
    routeApproval(wsid: string) {
      const at = this.pins.indexOf(wsid)
      if (at === 0 || at === 1) {
        // **transient UI focus**：只動畫面作用格，不動 durableFocusPane
        // （owner 2026-08-17 裁決——系統為了處理待核可事件而切畫面，不算使用者
        // 選定）。resolveApprovalPresentation 會把它還原回去。
        this.focused = at
        const m = this.sessions[wsid]
        if (m) m.unread = 0
        return
      }
      const target: 0 | 1 = this.focused === 0 ? 1 : 0
      const displaced = this.pins[target]
      // 第二筆 transient 接手時不重新備份——persistentPins 已經是「使用者真正
      // 釘選的那個」，用 displaced（上一筆 transient）覆寫會永久失去原釘選。
      if (this.transientPane === target && displaced && displaced !== wsid) {
        this.releaseView(displaced, target)
      }
      this.transientPane = target
      this.pins[target] = wsid
      if (!this.views[wsid]) this.views[wsid] = newView()
    },

    // resolveApprovalPresentation：§3.6.4 凍結的六種觸發（allow／deny／timeout／
    // dismiss／remove／shutdown）之後恢復原釘選。**六種的行為完全相同**，因此不
    // 收 trigger 參數——收一個永遠沒人分支的參數只會讓人以為它有分歧。呼叫端
    // （ApprovalDialog）負責在六種情境都呼叫到。無 transient 顯示中時為 no-op。
    resolveApprovalPresentation() {
      // transient **focus** 的還原必須在 transientPane 的早退之前：來源已釘選時
      // routeApproval 只切了焦點、沒有動 pins（transientPane 仍是 null），早退在
      // 前面的話那次焦點切換就永遠不會被還原。
      this.focused = this.durableFocusPane
      const pane = this.transientPane
      if (pane === null) return
      const shown = this.pins[pane]
      this.transientPane = null
      this.pins[pane] = this.persistentPins[pane]
      if (shown) this.releaseView(shown, pane)
    },

    // apply：host conversation envelope 的唯一入口，依 workspace_session_id 路由。
    // metadata（busy／unread／state／identity）一律更新；transcript 的實際寫入
    // 委給 applyToView（只在該 WSID 有 view——釘選或 transient 顯示中——時才
    // 呼叫，見底下 `if (!v) return`）。
    //
    // 注意：§3.8 視窗化載入（pin()／loadOlder()，Task 29）的歷史回放**不**經
    // 這個方法——它直接呼叫 applyToView，跳過整段 metadata switch。這是刻意
    // 的：歷史事件重放時再讓 unread++／busy 撥動一次都是錯的（那些副作用在
    // 事件第一次抵達時已經發生過，registry hydrate／目前的 runtime 狀態才是
    // 權威）。
    apply(env: Envelope) {
      const wsid = env.workspace_session_id ?? ''
      if (!wsid) { // 沒有 lane 可放：丟棄但不靜默（見 State.unrouted）
        this.unrouted++
        return
      }
      const m = this.metaFor(wsid, providerOf(env.provider))
      const v = this.views[wsid]

      switch (env.kind) {
        case 'init':
          if (env.session_id) {
            m.sessionId = env.session_id
            m.resume = env.session_id // End 後續聊自動接續（與 backend fallback 一致）
          }
          if (env.task_id) m.taskId = env.task_id
          break
        case 'state_change':
          if (env.state) {
            m.state = env.state
            m.awaitingApproval = env.state === 'awaiting_approval'
          }
          break
        case 'result':
          m.busy = false
          // unread：只有「看不到」的 session 才累積——兩個 pane 都是可見的。
          if (!this.pins.includes(wsid)) m.unread++
          break
      }

      if (env.kind === 'message' && env.role === 'user') {
        m.busy = true // canonical user message ＝ turn 起點（§3.5.9）
      }

      if (!v) return // 非釘選：metadata 已更新，不保留 transcript
      applyToView(v, env)
      if (env.kind === 'stream_error') this.bumpError(String(env.error ?? ''))
    },

    // applyNotice：workspace scope 事件（不屬於任何 session）。
    applyNotice(env: Envelope) {
      this.notices.push({ env })
      if (env.kind === 'stream_error') {
        this.bumpError(String(env.error ?? (env.payload as any)?.error ?? ''))
      }
    },

    // bumpError：錯誤計數的唯一入口（errorSeq 恆增；含 latch 片語時 latchSeq
    // 另計）。三個呼叫點：apply()（session lane）、applyNotice()（workspace
    // lane）、pushError()（UI 自己產生的錯誤，含所有 binding 失敗）。
    //
    // pushError 那條特別要緊：StartSession／NewSession／SettingsBar 的每一個
    // 操作失敗都走它，所以它同時餵 badge **與 latchSeq**——漏掉它，「收合狀態下
    // 送訊息被 latch 拒絕」就不再強制展開。三個呼叫點各自有守門測試。
    bumpError(text: string) {
      this.errorSeq++
      if (text.includes(REGISTRY_UNCERTAIN_MARK)) this.latchSeq++
    },

    // submit：作用於 focused pane 的 session。第一輪 StartSession（帶 resume／
    // recordCase／taskLabel），之後 SendMessage；兩者都以 WSID 定址。
    async submit(text: string) {
      const wsid = this.focusedWsid
      const m = this.sessions[wsid]
      if (!wsid || !m) {
        this.pushNotice(t('store.noFocusedSession'))
        return
      }
      if (!m.available) { // registry 有、Manager 無 slot：呼叫必然 ErrSessionNotFound
        this.pushNotice(t('store.sessionUnavailable', { wsid }))
        return
      }
      if (m.busy) return
      if (!this.bindings) {
        this.pushNotice(t('store.bindingsNotReady'))
        return
      }
      m.busy = true
      try {
        if (!m.active) {
          await this.bindings.StartSession(wsid, text, m.resume, m.recordCase, m.taskLabel, this.approvalPolicy)
          m.active = true
        } else {
          await this.bindings.SendMessage(wsid, text)
        }
        // 不本地新增 user 氣泡：等 host 的 canonical user envelope
      } catch (e) {
        this.pushError(String((e as Error)?.message ?? e), wsid)
      }
    },

    // note／pushError：帶 WSID 就進該 session 的 timeline，否則進 workspace
    // notices——兩者都看得見（timeline getter 會合併 notices）。
    note(msg: string, wsid?: string) {
      const w = wsid ?? this.focusedWsid
      const v = this.views[w]
      if (!v) {
        this.applyNotice(uiEnv('note', { text: msg }))
        return
      }
      v.timeline.push({ env: uiEnv('note', { provider: this.sessions[w]?.provider ?? '', text: msg }) })
      v.noiseGroup = -1
    },

    // pushError：**pane-scoped** 的錯誤出口——寫進該 WSID 自己的 transcript，
    // 並清掉它的 busy。用在「錯誤真的屬於某一個 session 的當前 turn」時
    // （submit 送不出去、SettingsBar 對 focused session 的 lifecycle 操作失敗）。
    // 作用範圍不是單一 session 的錯誤請用 pushNotice（見下）。
    //
    // 清 busy 是這個出口的本職：呼叫端在送出前把 busy 設成 true，失敗要復原。
    // 曾經有一個 keepBusy 參數給「從沒設過 busy 的呼叫端」用（SessionList 的
    // 移除失敗）——那條路徑改走 pushNotice 之後零呼叫端，參數已移除；
    // 「不該動 busy 的錯誤不要走 pushError」現在是結構上的分工，不是一個要記得
    // 傳對的旗標。
    pushError(msg: string, wsid?: string) {
      const w = wsid ?? this.focusedWsid
      const m = this.sessions[w]
      if (m) m.busy = false
      const v = this.views[w]
      if (!v) {
        this.applyNotice(uiEnv('stream_error', { error: msg })) // 計數由 applyNotice 負責
        return
      }
      v.timeline.push({ env: uiEnv('stream_error', { provider: m?.provider ?? '', error: msg }) })
      v.noiseGroup = -1
      this.bumpError(msg)
    },

    // pushNotice：**app-wide** 的錯誤出口——不綁任何 pane。
    //
    // 什麼時候用它而不是 pushError（rev3 review Critical）：錯誤的作用範圍不是
    // 「使用者正在看的那個 session」時。pushError 會寫進 `views[wsid].timeline`，
    // 而 `timeline` getter **只讀 focused view**——對一個沒被 focus 的 session
    // 按移除，錯誤就寫進一個畫面上根本沒顯示的 view，使用者只看到卡片沒消失。
    pushNotice(msg: string) {
      this.applyNotice(uiEnv('stream_error', { error: msg }))
    },

    // applyDone：session:done 依 payload 的 wsid 路由（provider 已不足以定位）。
    applyDone(d: Record<string, unknown>) {
      const wsid = String(d?.wsid ?? '')
      if (!wsid) {
        this.unrouted++
        return
      }
      const m = this.metaFor(wsid, providerOf(String(d?.provider ?? '')))
      m.busy = false
      m.active = false
      const v = this.views[wsid]
      if (!v) return
      v.timeline.push({ env: uiEnv('session_done', { provider: m.provider, text: JSON.stringify(d) }) })
      v.noiseGroup = -1
    },

    // reset：NewSession 成功後清掉 focused pane 的 transcript 與 session 身分；
    // 輸入欄位（taskLabel／recordCase）保留沿用，其餘 session 完全不動。
    reset() {
      const wsid = this.focusedWsid
      const m = this.sessions[wsid]
      if (!m) return
      m.sessionId = ''
      m.taskId = ''
      m.resume = ''
      m.state = 'idle'
      m.busy = false
      m.active = false
      m.awaitingApproval = false
      m.unread = 0
      if (this.views[wsid]) this.views[wsid] = newView()
    },

    // markRemoved：RemoveSession 成功後的 UI 狀態——tombstone 標記留著（不從
    // sessions 刪掉），pane 與 transcript 釋放。清單以 removed 過濾。
    markRemoved(wsid: string) {
      const m = this.sessions[wsid]
      if (m) {
        m.removed = true
        m.busy = false
        m.active = false
        m.awaitingApproval = false
      }
      let unpinned = false
      for (const idx of [0, 1] as const) {
        if (this.persistentPins[idx] === wsid) {
          this.persistentPins[idx] = null
          unpinned = true
        }
        if (this.pins[idx] === wsid) {
          this.pins[idx] = null
          if (this.transientPane === idx) this.transientPane = null
        }
      }
      delete this.views[wsid]
      // 只有真的動到 persistent 釘選才寫：移除一個沒被釘選的 session 不改變排列。
      // 不寫的話 registry 會留著一個指向 tombstone 的 pin——Go 端 PaneLayout()
      // 讀取時會濾掉它，但那是第二道防線，不是不寫的理由。
      if (unpinned) void this.persistLayout()
    },

    setTaskLabel(v: string) { const m = this.focusedSession; if (m) m.taskLabel = v },
    setRecordCase(v: string) { const m = this.focusedSession; if (m) m.recordCase = v },
    setResumeInput(v: string) { const m = this.focusedSession; if (m) m.resume = v },
    setApprovalPolicy(v: string) { this.approvalPolicy = v },
  },
})
