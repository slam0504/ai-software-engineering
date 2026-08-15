import { defineStore } from 'pinia'
import { t } from '../i18n'
import type { Bindings, ChatItem, Envelope, SessionInfo, TimelineItem } from '../types'

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
  focused: 0 | 1
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
  approvalPolicy: string
  bindings: Bindings | null
}

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
    transientPane: null,
    scrollAnchors: {},
    notices: [],
    unrouted: 0,
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

    setFocus(i: 0 | 1) {
      this.focused = i
      const w = this.pins[i]
      if (w && this.sessions[w]) this.sessions[w].unread = 0
    },

    // pin：把 session 釘進 pane（persistent）。已有 view（例如同一 wsid 仍釘在
    // 另一 pane）就沿用既有 transcript，不重新載入；否則建立容器並觸發 §3.8
    // 尾端視窗 lazy load（Task 29——修掉 Task 26→28 刻意接受的暫時降級：重啟
    // 後重新釘選一個既有 session，畫面只有 metadata／unread／狀態是對的，歷史
    // 對話看不到，因為 pin() 過去只建空殼、從不回填）。沒有
    // bindings.LoadTurnsBefore（dev 尚未 setBindings、或測試 mock 未提供）時
    // 退回舊行為：空殼 view、不拋錯。
    async pin(idx: 0 | 1, wsid: string) {
      const prev = this.pins[idx]
      if (prev && prev !== wsid) this.releaseView(prev, idx)
      this.persistentPins[idx] = wsid
      this.pins[idx] = wsid
      if (this.transientPane === idx) this.transientPane = null
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
    },

    // applyNotice：workspace scope 事件（不屬於任何 session）。
    applyNotice(env: Envelope) {
      this.notices.push({ env })
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

    // keepBusy：預設 false（清 busy）——原本唯一呼叫端 submit() 在送出前把
    // busy 設成 true，失敗要復原。Task 27 review 抓到第二個呼叫端
    // （SessionList 的移除失敗）從未動過 busy，卻無條件被這行清成 false：
    // 使用者對一個 streaming 中的 session 按移除，Removable() 在最前面就擋
    // 下、完全沒碰後端狀態，畫面卻把 busy-dot 關掉，讀起來像「已經閒置」。
    // keepBusy=true 讓沒有設過 busy 的呼叫端不去動它，不改動預設呼叫端
    // （submit）的既有行為。
    pushError(msg: string, wsid?: string, keepBusy = false) {
      const w = wsid ?? this.focusedWsid
      const m = this.sessions[w]
      if (m && !keepBusy) m.busy = false
      const v = this.views[w]
      if (!v) {
        this.applyNotice(uiEnv('stream_error', { error: msg }))
        return
      }
      v.timeline.push({ env: uiEnv('stream_error', { provider: m?.provider ?? '', error: msg }) })
      v.noiseGroup = -1
    },

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
      for (const idx of [0, 1] as const) {
        if (this.persistentPins[idx] === wsid) this.persistentPins[idx] = null
        if (this.pins[idx] === wsid) {
          this.pins[idx] = null
          if (this.transientPane === idx) this.transientPane = null
        }
      }
      delete this.views[wsid]
    },

    setTaskLabel(v: string) { const m = this.focusedSession; if (m) m.taskLabel = v },
    setRecordCase(v: string) { const m = this.focusedSession; if (m) m.recordCase = v },
    setResumeInput(v: string) { const m = this.focusedSession; if (m) m.resume = v },
    setApprovalPolicy(v: string) { this.approvalPolicy = v },
  },
})
