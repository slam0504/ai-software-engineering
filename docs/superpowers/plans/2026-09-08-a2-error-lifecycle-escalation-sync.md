# A2 Error lifecycle 三原則＋escalation 同步 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev4（2026-09-09，**A2 aggregate 關票回填**：A2-1／A2-2 皆經 owner 驗收通過；第七節記錄裁定綁定、證據封存、保留限制、rulings 與實際工時；前版：rev3、rev2、rev1）。
> 狀態：**已完成／A2 aggregate 關票（owner 2026-09-09 裁定）**。實作留在工作樹**未提交、未推送、未追加 PR #5、未合併**；證據已存持久位置並驗證 manifest，**備份未確認**。
> 票源：Pre-M4 Readiness Backlog **A2**（票面 0.4 pt，診斷已過期；依 owner 2026-09-08 裁定拆為 **A2-1 前端同步與 error lifecycle**／**A2-2 後端三類 blocker 回歸測試**，A2 為 aggregate，兩票完成才關）。
> 基準：`main`＝`f9e2524`；本 plan 草稿位於分支 `docs/a1a-closure-readme`（untracked），實作時另開分支。
> 授權邊界（owner 2026-09-08）：只呈現既有 blocker 狀態，**不改建立／解除規則、不新增 escalation 專屬事件 lane、不新增 production 注入介面、不為通知而禁止切分頁**；檔案監看沿用既有 `spec:changed`／`plan:changed`；GateConsole 重試候選另案。

**Goal:** 工作區操作（含操作等待中切走分頁）與檔案監看 reconcile 之後，收件匣清單與 badge 反映後端已建立／解除的 blocker；補齊 `missing-binding:`／`negative-control-missed:`／`journal-degraded:` 三枚 key 的建立與解除回歸測試；補 SpecWorkspace 的 error lifecycle 專門回歸。

**Architecture:** 重載責任**由 App 持有**：`App.vue` 以純函式 `withEscalationReload(fn, reload)` 包裝所有可能建立／解除 blocker 的綁定（`SubmitForApproval`、`SubmitPlanForApproval`、`PlanAssist`、`RunEvidence`、`SubmitTestContract`；`GateDecide` 於 `decideGate` 內直接補重載；`SpecAssist` 無建立／解除 blocker 路徑，不包裝），成功與失敗都在結果回到呼叫端前先重載，原回傳值與錯誤原樣透傳；子元件以既有 `write?` optional-prop 慣例接收包裝後的函式，不再自己 emit。元件被 `v-if` 卸載後操作才完成時，重載仍由 App 執行。連續觸發以 store 序號丟棄舊回應。後端不動 production，只加 App 層整合測試，注入沿用 `Journal.Close()` 使後續 `Append()` 失敗。

**Tech Stack:** Go（`app.go` 整合測試；既有 helper `newTestAppGit`／`newTestAppEvidence`／`setupApprovedEvidencePlan`／`openItemByKey`／`newTestAppAt`）、Vue 3＋Pinia＋vitest＋@vue/test-utils。

**Spec:** M3a `docs/superpowers/specs/2026-08-12-m3a-plan-test-contract-design.md` §3.8／§3.10；M3a.1 `docs/superpowers/specs/2026-08-13-m3a1-closure-design.md` §3.4 Erratum。

## Global Constraints

- §3.8：硬性條件以 validator 為權威；本票只改呈現，不動 `escCreateSystemLocked`／`escResolveByKeyLocked` 及其呼叫點。
- §3.8「收件匣不可用時不得裝空」：`unavailable` 非空時 badge 顯示 warn（`lib/escalationBadge.ts`），優先序不改。
- `App.vue:70-72` 的 brief 決定「不加專屬事件 lane」維持；watcher 同步掛既有 `spec:changed`（`app.go:2633`）與 `plan:changed`（`app.go:2781`）。
- 前端 store 不 import wailsjs；`EscalationList` 只經 `wailsBindings`（`lib/bindings.ts`）注入；`App.vue:73` 仍是唯一呼叫點。
- Go 測試不新增 production 介面、不放寬 guard、不加 retry；mutation 還原用位元組備份。
- 中文用台灣慣用語；測試名稱沿「中文描述＋票號」慣例。

---

## 一、已確認事實（2026-09-08 唯讀核對；rev2 新增以 ★ 標記）

| 事實 | 證據 |
|---|---|
| §3.8 九項來源各有 key、建立／解除路徑齊全 | `app.go:5061/5073`、`6120/6140`、`6049/5880`、`5362/6092/6084`、`6097/6088`、`6065/6069`、`6173/6571`、`6178/6660` |
| `reconcileLocked` 只由 `gateDecide`（`:5859`）與 `reconcileGate1NotifyOnly`（`:2662`）呼叫；後者返回後才 emit `spec:changed`／`plan:changed` | `app.go:2631-2639`、`:2779-2781` |
| ★ `reconcileLocked` 第一步 `svc.List()` 會執行 `Reconcile()`，有 Active 紀錄需轉 stale 時**先 append transition**；journal 已關閉時此處即失敗返回，走不到 `escJournalDegradedLocked` | `app.go:6018-6024`、`internal/gate/service.go:210-215`、`:274-304` |
| ★ `gateDecide` 在 `reconcileLocked` 之前只做 `ensureGate` 與 git identity，不 append | `app.go:5840-5860` |
| `PlanAssist` 同步：`runner.Run` 完成、runtime key 建立／解除後才回傳 | `app.go:6641-6666` |
| `RunEvidence` 回傳時 finalize 已完成 | `app_evidence_test.go:404-409` |
| ★ `SubmitTestContract` 亦經 `submitGateRequest`（可建立／解除 `missing-binding:test_contract_approval:task:<p>/<t>`），App.vue 目前直接傳原綁定 | `app.go:5618`、`App.vue:395` |
| ★ gate1／gate2 送核在進 `submitGateRequest` 前已預檢綁定內容；**TCA 則有正式 UI 可達路徑**：兩筆 passed evidence 後，test commit 輸入欄仍可改，送核按鈕只檢查 `submitBusy` 與 `bothPassed`（`TcaWorkspace.vue:328`），送核重讀目前輸入並由 `tcaBindings` 原樣放入 `oracle_surface.Ref`（`app.go:5538`），`validateTCABindings` 對 ref 格式不符回錯（`tca.go:453`）→ 建立 `missing-binding:test_contract_approval:task:<p>/<t>`。owner 2026-09-08 靜態複核提出，**本輪未以 GUI 重現**；本票不改 TCA 輸入或送核規則 | `app.go:3941`、`:5079-5101`、`:5538`、`:5618`；`TcaWorkspace.vue:219-233`、`:328`；`internal/gatepolicy/tca.go:121-126`、`:447-453` |
| ★ `gateDecide` 的 blocking escalation 檢查在 `svc.PrepareDecision` **之後**（`app.go:5884-5889`），拒絕訊息為 `blocked by N escalation item(s): <condition_key>（summary）`（`summarizeEscalations`，`:5955-5966`） | `app.go:5862-5889`、`:5955-5966` |
| ★ `specAssist` 無任何 `esc*Locked` 呼叫（preflight 與 runtime enforcement 只在 `planAssist`） | `app.go` 以 awk 掃 `specAssist` 函式體零命中 |
| ★ runner 於 negative_control 會拒絕 mutation 觸及 oracle surface；fixture 的 `oracle-surface.yaml` 只涵蓋 `run_test.sh` | `internal/evidence/runner.go:129-130`；`app_evidence_test.go:99` |
| negative_control 分類：exit 0 → `failed`；exit≠0 且含 matcher → `passed` | `internal/evidence/matcher.go:43-49` |
| `journal.Append` 於 write／sync 失敗設 `degraded=true`；`Close()` 後 `Append` 必失敗 | `internal/journal/journal.go:125-138`、`:160` |
| `ResolveByKey` 無未 resolved 項時 no-op | `internal/escalation/service.go:141-160` |
| ★ Plan／Spec 的 `assistBusy`／`submitBusy` 不進 `busyReason`，等待期間可切分頁；三個工作區皆 `v-if` 掛載，切走即卸載；已卸載元件的 emit 不會送達 | `PlanWorkspace.vue:74`（只 watch `busyReason`）、`:443-452`；`App.vue:389-397` |
| 前端 `refreshEscalation` 只在啟動（`App.vue:355`）與收件匣自身操作後（`:428`）呼叫；點收件匣分頁只改 `sidePanel`、元件 `v-show`，不重載 | `App.vue:327-340`、`:411`、`:425` |
| ★ App 預設 `chat` 分頁；測試切分頁用 `findAll('nav button').find(b => b.text() === '計畫')`（既有慣例） | `App.test.ts:95`、`:177` |
| ★ SpecWorkspace／PlanWorkspace 已有 optional 函式 prop 慣例：`write?: (path, content, expectedDigest) => Promise<string>`，`props.write ?? SpecWrite` | `SpecWorkspace.vue:25-29`、`:297`；`PlanWorkspace.vue:30-34` |
| Plan 送核 error lifecycle 原則 1／2 已落地並有回歸 | `7789f40`；`PlanWorkspace.vue:443-450`；`plan.test.ts:56` |
| SpecWorkspace 逐操作獨立錯誤 ref、操作開始即清 | `SpecWorkspace.vue:296/322/342/354/369` |
| 三枚 key 於 `*_test.go` grep 零命中 | 2026-09-08 grep |

---

## 二、A2-1 前端同步與 error lifecycle

### 檔案結構

- Create: `frontend/src/lib/escalationReload.ts`（純函式 `withEscalationReload`，同 `escalationBadge.ts` 的「抽純函式以便單元測試」慣例）
- Modify: `frontend/src/stores/escalation.ts`（序號守衛）
- Modify: `frontend/src/App.vue`（建立五個包裝函式；`decideGate` 補重載；Spec／Plan／Tca 以 prop 注入；`spec:changed`／`plan:changed` 監聽）
- Modify: `frontend/src/components/SpecWorkspace.vue`（新增 `submit?` prop；`SpecAssist` 保留原呼叫）
- Modify: `frontend/src/components/PlanWorkspace.vue`（新增 `submit?`／`assist?` prop）
- Test: `lib/escalationReload.test.ts`（新）、`stores/escalation.test.ts`、`App.test.ts`、`components/SpecWorkspace.test.ts`、`components/PlanWorkspace.test.ts`

### Task 1：escalation store 序號守衛

**Files:** Modify `frontend/src/stores/escalation.ts`；Test `frontend/src/stores/escalation.test.ts`

**Interfaces:** `load(list)` 簽章不變；新增 state `loadSeq: number`。

- [ ] **Step 1: 寫失敗測試（三條）**

```ts
  it('連續 load()：先發後到的舊成功回應不得覆蓋新狀態（A2-1）', async () => {
    const s = useEscalation()
    let resolveOld!: (v: escalation.Entry[]) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>(r => { resolveOld = r }))
    await s.load(async () => [entry('E-new', 'open')])
    resolveOld([entry('E-old', 'open'), entry('E-old2', 'open')])
    await p1
    expect(s.entries.map(e => e.Item.escalation_id)).toEqual(['E-new'])
    expect(s.unresolvedCount).toBe(1)
  })

  it('連續 load()：舊呼叫的失敗不得把新成功狀態改成 unavailable（A2-1）', async () => {
    const s = useEscalation()
    let rejectOld!: (e: unknown) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>((_, rej) => { rejectOld = rej }))
    await s.load(async () => [entry('E-new', 'open')])
    rejectOld(new Error('stale failure'))
    await p1
    expect(s.unavailable).toBe('')
    expect(s.entries).toHaveLength(1)
  })

  it('連續 load()：較新請求失敗後，較舊成功回應不得清除 unavailable（A2-1）', async () => {
    const s = useEscalation()
    let resolveOld!: (v: escalation.Entry[]) => void
    const p1 = s.load(() => new Promise<escalation.Entry[]>(r => { resolveOld = r }))
    await s.load(async () => { throw new Error('journal degraded') })
    expect(s.unavailable).toBe('Error: journal degraded')
    resolveOld([entry('E-old', 'open')])
    await p1
    expect(s.unavailable).toBe('Error: journal degraded')
    expect(s.entries).toHaveLength(0)
  })
```

- [ ] **Step 2: 跑測試確認失敗**

Run: `cd frontend && npx vitest run src/stores/escalation.test.ts`
Expected: 三條 FAIL（第一條 entries 為兩筆 old；第二條 unavailable 為 `'Error: stale failure'`；第三條 unavailable 被清空）。

- [ ] **Step 3: 最小實作**

```ts
interface State {
  entries: escalation.Entry[]
  unavailable: string
  loadSeq: number // A2-1：每次 load 遞增；回應到達時序號已變＝舊回應，整個結果丟棄
}
  state: (): State => ({ entries: [], unavailable: '', loadSeq: 0 }),
    async load(list: () => Promise<escalation.Entry[]>) {
      const seq = ++this.loadSeq
      try {
        const entries = await list()
        if (seq !== this.loadSeq) return
        this.entries = entries
        this.unavailable = ''
      } catch (e) {
        if (seq !== this.loadSeq) return
        this.unavailable = String(e)
      }
    },
```

- [ ] **Step 4: 跑測試確認通過** — 同 Step 2，全綠（含既有三條）。
- [ ] ~~**Step 5: Commit**~~ — **跳過**（Ruling 1：owner 2026-09-08 明定此次範圍不含提交；未執行，不勾）

### Task 2：`withEscalationReload` 純函式

**Files:** Create `frontend/src/lib/escalationReload.ts`；Test `frontend/src/lib/escalationReload.test.ts`

**Interfaces:**
- Produces:

```ts
export function withEscalationReload<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  reload: () => Promise<void>,
): (...args: A) => Promise<R>
```

語意：呼叫 `fn`；不論 resolve 或 reject，**先 `await reload()` 完成，再**把原值回傳／原錯誤重拋。**契約前提：`reload` 必須自行處理查詢失敗並落 `unavailable`、不向外拋出**（現行 `escalation.load` 已是如此，`stores/escalation.ts` 的 try/catch；`refreshEscalation` 只是它的薄包裝）。`try/finally` 內的 `await reload()` 若 reject 會取代原結果，因此 wrapper **不另加吞錯**，透傳原結果的保證建立在此前提上；Task 4 的 App 案例「重載失敗 → badge warn」同時證明 `refreshEscalation` 在 `EscalationList` 失敗時不拋。

- [ ] **Step 1: 寫失敗測試**

```ts
import { describe, expect, it, vi } from 'vitest'
import { withEscalationReload } from './escalationReload'

describe('withEscalationReload（A2-1：重載由 App 持有，與呼叫元件是否仍掛載無關）', () => {
  it('成功：先 reload 再回傳原值', async () => {
    const order: string[] = []
    const reload = vi.fn(async () => { order.push('reload') })
    const fn = vi.fn(async (a: string) => { order.push('fn'); return 'id-' + a })
    const wrapped = withEscalationReload(fn, reload)
    await expect(wrapped('x')).resolves.toBe('id-x')
    expect(fn).toHaveBeenCalledWith('x')
    expect(order).toEqual(['fn', 'reload'])
  })
  it('失敗：先 reload 再重拋原錯誤（失敗也可能已建立 blocker）', async () => {
    const reload = vi.fn(async () => {})
    const err = new Error('缺必要 binding')
    const wrapped = withEscalationReload(async () => { throw err }, reload)
    await expect(wrapped()).rejects.toBe(err)
    expect(reload).toHaveBeenCalledTimes(1)
  })
  it('等待中的呼叫在 fn 完成後才 reload（呼叫端可能已卸載，reload 仍執行）', async () => {
    const reload = vi.fn(async () => {})
    let resolveFn!: (v: string) => void
    const wrapped = withEscalationReload(() => new Promise<string>(r => { resolveFn = r }), reload)
    const p = wrapped()
    expect(reload).not.toHaveBeenCalled()
    resolveFn('done')
    await expect(p).resolves.toBe('done')
    expect(reload).toHaveBeenCalledTimes(1)
  })
  it('reload 完成前 wrapped Promise 尚未結束（等待順序：fn → reload → 回傳）', async () => {
    let finishReload!: () => void
    const reload = vi.fn(() => new Promise<void>(r => { finishReload = r }))
    const wrapped = withEscalationReload(async () => 'v', reload)
    let settled = false
    const p = wrapped().then(v => { settled = true; return v })
    await Promise.resolve(); await Promise.resolve() // 讓 fn 與 reload 開始
    expect(reload).toHaveBeenCalledTimes(1)
    expect(settled).toBe(false)
    finishReload()
    await expect(p).resolves.toBe('v')
    expect(settled).toBe(true)
  })
})
```

- [ ] **Step 2: 跑測試確認失敗** — `npx vitest run src/lib/escalationReload.test.ts`，四條 FAIL（模組不存在）。
- [ ] **Step 3: 最小實作**

```ts
// withEscalationReload：把「可能建立／解除 blocker 的綁定」包成「完成後先重載
// 收件匣再回傳」。契約前提：reload 自行處理查詢失敗（落 unavailable）、不拋；
// finally 內的 await 若 reject 會取代原結果，這裡刻意不吞錯以免掩蓋契約違反。重載責任在 App（呼叫端元件可能在等待期間已被 v-if 卸載，
// 已卸載元件的 emit 不會送達，所以不能靠元件自己通知）。成功與失敗都重載：
// 失敗也可能已建立阻擋項（missing-binding 建立後仍回傳錯誤，app.go:6120）。
export function withEscalationReload<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  reload: () => Promise<void>,
): (...args: A) => Promise<R> {
  return async (...args: A) => {
    try {
      return await fn(...args)
    } finally {
      await reload()
    }
  }
}
```

- [ ] **Step 4: 跑測試確認通過**；~~**Step 5: Commit**~~ — **跳過**（Ruling 1，未執行）

### Task 3：SpecWorkspace／PlanWorkspace 改由 prop 注入送核與 assist

**Files:**
- Modify `frontend/src/components/SpecWorkspace.vue:25-29`（props）、`:345`（`SubmitForApproval` 呼叫）；`:255` 的 `SpecAssist` 保留原呼叫（D7：無 blocker 路徑）
- Modify `frontend/src/components/PlanWorkspace.vue:30-34`（props）、`:369`（`PlanAssist` 呼叫）、`:447`（`SubmitPlanForApproval` 呼叫）
- Test `SpecWorkspace.test.ts`、`PlanWorkspace.test.ts`

**Interfaces（Produces，Task 4 依此注入）：**

```ts
// SpecWorkspace
submit?: () => Promise<string>
// PlanWorkspace
submit?: (planId: string) => Promise<string>
assist?: (provider: string, prompt: string) => Promise<string>
```

未注入時回退直接 import（同 `props.write ?? SpecWrite`），既有元件測試不需改。

- [ ] **Step 1: 寫失敗測試**

SpecWorkspace.test.ts：

```ts
  it('注入 submit prop 時送核走 prop，不走 wailsjs 直呼（A2-1：重載責任在 App）', async () => {
    const submit = vi.fn(async () => 'approval-9')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', submit } })
    await flushPromises()
    await w.find('[data-test=submit-for-approval]').trigger('click')
    await flushPromises()
    expect(submit).toHaveBeenCalledTimes(1)
    expect(mocks.SubmitForApproval).not.toHaveBeenCalled()
  })

  // D3：Spec 專門回歸——失敗後重試成功，同類錯誤清除、其他操作的錯誤保留。
  // 「其他操作」用 preview-commit（SpecWorkspace.vue:439，只在 commitBusy 時 disabled，
  // 不需先弄 dirty）；save 按鈕在非 dirty 時 disabled（:414），不適合當第二操作。
  it('送核失敗→預覽 commit 失敗→再送核成功：送核錯誤清空、commit 錯誤保留（A2 原則 1／2，Spec 同型）', async () => {
    const submit = vi.fn()
      .mockRejectedValueOnce(new Error('spec: dirty tree'))
      .mockResolvedValueOnce('approval-10')
    mocks.PreviewSpecCommit.mockRejectedValueOnce(new Error('commit: nothing to commit'))
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature', submit } })
    await flushPromises()
    await w.find('[data-test=submit-for-approval]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('spec: dirty tree')
    await w.find('[data-test=preview-commit]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('commit: nothing to commit')
    await w.find('[data-test=submit-for-approval]').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain('spec: dirty tree')
    expect(w.text()).toContain('commit: nothing to commit')
  })
```

PlanWorkspace.test.ts：

```ts
  it('注入 submit／assist prop 時走 prop，不走 wailsjs 直呼（A2-1）', async () => {
    const submit = vi.fn(async (id: string) => 'approval-' + id)
    const assist = vi.fn(async () => 'corr-1')
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/my-plan.yaml', submit, assist } })
    await flushPromises()
    await w.find('[data-test=submit-gate2]').trigger('click'); await flushPromises()
    await w.find('[data-test=generate-draft]').trigger('click'); await flushPromises()
    expect(submit).toHaveBeenCalledTimes(1)
    expect(assist).toHaveBeenCalledTimes(1)
    expect(mocks.SubmitPlanForApproval).not.toHaveBeenCalled()
    expect(mocks.PlanAssist).not.toHaveBeenCalled()
  })
```

- [ ] **Step 2: 跑測試確認失敗** — prop 尚不存在，`submit` 未被呼叫、`mocks.SubmitForApproval` 被呼叫。
- [ ] **Step 3: 最小實作**

SpecWorkspace.vue：

```ts
const props = defineProps<{
  path?: string
  draft?: string
  write?: (path: string, content: string, expectedDigest: string) => Promise<string>
  // A2-1：由 App 注入「完成後重載收件匣」的包裝版本；未注入時回退直呼（同 write）。
  // SpecAssist 不注入：specAssist 沒有建立／解除 blocker 的路徑（D7）。
  submit?: () => Promise<string>
}>()
// submitForApproval 內：submitResult.value = await (props.submit ?? SubmitForApproval)()
```

PlanWorkspace.vue 同型：`(props.assist ?? PlanAssist)(provider.value, promptInput.value)`、`(props.submit ?? SubmitPlanForApproval)(planIdInput.value)`。

- [ ] **Step 4: 跑測試確認通過**（兩檔全綠、既有案例不變）；~~**Step 5: Commit**~~ — **跳過**（Ruling 1，未執行）

### Task 4：App.vue 持有重載——六個包裝、prop 注入、watcher 事件

**Files:** Modify `frontend/src/App.vue:61-69`（decideGate）、`:73-75`（refreshEscalation 附近新增包裝）、`:327-340`（EventsOn）、`:389-397`（三個工作區 props）；Test `frontend/src/App.test.ts`

**Interfaces（Consumes）：** Task 2 `withEscalationReload`；Task 3 的 Spec `submit?`、Plan `submit?`／`assist?` props；`wailsBindings.RunEvidence`／`SubmitTestContract`；`TcaWorkspace` props `runEvidence`／`submitTestContract`（`TcaWorkspace.vue:25`、對應行）。

- [ ] **Step 1: 寫失敗測試（App.test.ts；沿既有 `shallowMount`＋`findAll('nav button')` 慣例）**

```ts
import TcaWorkspace from './components/TcaWorkspace.vue'
import { escalation } from '../wailsjs/go/models'
const openEntry = (id: string, state = 'open') => escalation.Entry.createFrom({
  Item: { _type: 'escalation_item', escalation_id: id, condition_key: 'missing-binding:gate1:workspace',
    occurrence: 1, source: 'system', source_ref: 'workspace', block_scope: 'workspace', hard: true,
    summary: 's', created_at: '2026-01-01T00:00:00Z' }, State: state,
})
const badgeText = (w: VueWrapper) => w.find('[data-test=escalation-badge]').exists() ? w.find('[data-test=escalation-badge]').text() : ''

describe('A2-1：重載由 App 持有——工作區操作（含等待中切走）與 watcher 事件後，清單與 badge 同步', () => {
  async function mountAt(tab: '規格' | '計畫' | '測試契約核可') { // 文字依 i18n zh-TW（app.tab.*）
    const pinia = createPinia(); setActivePinia(pinia)
    const w = shallowMount(App, { global: { plugins: [pinia, makeI18n()] } })
    await flushPromises()
    await w.findAll('nav button').find(b => b.text() === tab)!.trigger('click')
    await flushPromises()
    return w
  }

  it('Plan 送核等待中切到對話分頁（PlanWorkspace 卸載）→ 失敗完成 → 清單出現 blocker、badge=1', async () => {
    const w = await mountAt('計畫')
    let rejectSubmit!: (e: unknown) => void
    wailsAppMocks.SubmitPlanForApproval.mockImplementationOnce(() => new Promise((_, rej) => { rejectSubmit = rej }))
    const submit = w.findComponent(PlanWorkspace).props('submit') as (id: string) => Promise<string>
    const p = submit('P1').catch(e => e)
    await w.findAll('nav button').find(b => b.text() === '對話')!.trigger('click')
    await flushPromises()
    expect(w.findComponent(PlanWorkspace).exists()).toBe(false)
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('E1')])
    rejectSubmit(new Error('缺必要 binding'))
    await p; await flushPromises()
    expect(useEscalation().entries.map(e => e.Item.escalation_id)).toEqual(['E1'])
    expect(badgeText(w)).toBe('1')
  })

  it('Spec 送核等待中切走 → 成功完成 → 既有 blocker 轉 resolved、未解除項目清空、badge 消失', async () => {
    const w = await mountAt('規格')
    useEscalation().entries = [openEntry('E1')]
    await flushPromises()
    expect(badgeText(w)).toBe('1')
    let resolveSubmit!: (v: string) => void
    wailsAppMocks.SubmitForApproval.mockImplementationOnce(() => new Promise(r => { resolveSubmit = r }))
    const submit = w.findComponent(SpecWorkspace).props('submit') as () => Promise<string>
    const p = submit()
    await w.findAll('nav button').find(b => b.text() === '對話')!.trigger('click'); await flushPromises()
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false)
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('E1', 'resolved')])
    resolveSubmit('approval-1')
    await expect(p).resolves.toBe('approval-1'); await flushPromises()
    expect(useEscalation().entries).toHaveLength(1) // resolved entry 仍在清單
    expect(useEscalation().entries[0].State).toBe('resolved')
    expect(useEscalation().unresolvedCount).toBe(0)
    expect(badgeText(w)).toBe('')
  })

  it('Plan assist 完成（失敗／成功）後都重載，清單反映 planner 項的建立與解除', async () => {
    const w = await mountAt('計畫')
    const assist = w.findComponent(PlanWorkspace).props('assist') as (p: string, q: string) => Promise<string>
    wailsAppMocks.PlanAssist.mockRejectedValueOnce(new Error('planner-enforcement-preflight'))
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('PF1')])
    await assist('claude', 'x').catch(() => {}); await flushPromises()
    expect(useEscalation().entries.map(e => e.Item.escalation_id)).toEqual(['PF1'])
    expect(badgeText(w)).toBe('1')
    wailsAppMocks.PlanAssist.mockResolvedValueOnce('corr-1')
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('PF1', 'resolved')])
    await assist('claude', 'x'); await flushPromises()
    expect(useEscalation().unresolvedCount).toBe(0)
    expect(badgeText(w)).toBe('')
    expect(w.findComponent(SpecWorkspace).exists()).toBe(false) // Spec 未掛載；SpecAssist 不包裝（D7）
  })

  it('TCA 的 run-evidence 與 submit-test-contract 包裝：成功回傳原值、失敗重拋原錯誤，兩者都重載', async () => {
    const w = await mountAt('測試契約核可')
    const base = wailsAppMocks.EscalationList.mock.calls.length
    const tca = w.findComponent(TcaWorkspace)
    wailsAppMocks.RunEvidence.mockResolvedValueOnce('EV1')
    await expect(tca.props('runEvidence')('A1', 'P1', 'T1', 'c0ffee', 'expected_red', '')).resolves.toBe('EV1')
    wailsAppMocks.SubmitTestContract.mockRejectedValueOnce(new Error('缺必要 binding'))
    await expect(tca.props('submitTestContract')('P1', 'T1', 'c0ffee', 'EV1', 'EV2', 'M1')).rejects.toThrow('缺必要 binding')
    expect(wailsAppMocks.EscalationList).toHaveBeenCalledTimes(base + 2)
  })

  it('decideGate 成功／失敗後都重載；失敗時 gateError 仍顯示', async () => {
    const w = await mountAt('計畫')
    const base = wailsAppMocks.EscalationList.mock.calls.length
    const decide = w.findComponent(GateConsole).props('decide')
    wailsAppMocks.GateDecide.mockResolvedValueOnce(undefined)
    await decide('A1', 'approved', '', []); await flushPromises()
    wailsAppMocks.GateDecide.mockRejectedValueOnce(new Error('blocked by escalation'))
    await decide('A1', 'approved', '', []); await flushPromises()
    expect(wailsAppMocks.EscalationList).toHaveBeenCalledTimes(base + 2)
    expect(w.find('.gate-err').text()).toContain('blocked by escalation') // App.vue:423，sidePanel 預設 gate
  })

  it('spec:changed／plan:changed（reconcile 已返回後送出）觸發重載，清單反映 stale 項', async () => {
    const handlers: Record<string, (p?: unknown) => void> = {}
    runtimeMocks.EventsOn.mockImplementation((name: string, h: (p?: unknown) => void) => { handlers[name] = h; return () => {} })
    const w = await mountAt('規格')
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('S1')])
    handlers['spec:changed']?.('spec/a.feature'); await flushPromises()
    expect(useEscalation().entries.map(e => e.Item.escalation_id)).toEqual(['S1'])
    wailsAppMocks.EscalationList.mockResolvedValueOnce([])
    handlers['plan:changed']?.('plan/P1.yaml'); await flushPromises()
    expect(useEscalation().entries).toHaveLength(0)
    expect(badgeText(w)).toBe('')
  })

  it('重載失敗 → badge warn；下一次成功 → 恢復計數（清單與 badge 同源）', async () => {
    const w = await mountAt('規格')
    wailsAppMocks.SubmitForApproval.mockResolvedValue('approval-1')
    wailsAppMocks.EscalationList.mockRejectedValueOnce(new Error('journal degraded'))
    await (w.findComponent(SpecWorkspace).props('submit') as () => Promise<string>)(); await flushPromises()
    expect(w.find('[data-test=escalation-badge-warn]').exists()).toBe(true)
    wailsAppMocks.EscalationList.mockResolvedValueOnce([openEntry('E1')])
    await (w.findComponent(SpecWorkspace).props('submit') as () => Promise<string>)(); await flushPromises()
    expect(w.find('[data-test=escalation-badge-warn]').exists()).toBe(false)
    expect(badgeText(w)).toBe('1')
  })
})
```

前置改動：`wailsAppMocks` 需補 `SubmitForApproval`、`SubmitPlanForApproval`、`PlanAssist`（目前缺，未 mock 時 `props('submit')` 呼叫會撞 `window.go`）；`vi.mock('../wailsjs/runtime/runtime')` 改為 hoisted 的 `runtimeMocks = { EventsOn: vi.fn() }` 以便 `mockImplementation`。

- [ ] **Step 2: 跑測試確認失敗** — `npx vitest run src/App.test.ts`；七條 FAIL（`props('submit')` 為 undefined、EscalationList 次數不增、badge 未變）。
- [ ] **Step 3: 最小實作（App.vue）**

```ts
import { withEscalationReload } from './lib/escalationReload'
import { SubmitForApproval, PlanAssist, SubmitPlanForApproval } from '../wailsjs/go/main/App'

async function refreshEscalation() { await escalation.load(wailsBindings.EscalationList) }

// A2-1：可能建立／解除 blocker 的操作一律由 App 包裝——完成（成功或失敗）後先
// 重載收件匣再回傳，原值與錯誤原樣透傳；呼叫端元件被 v-if 卸載也不影響重載。
const submitSpecAndReload = withEscalationReload(SubmitForApproval, refreshEscalation)
const submitPlanAndReload = withEscalationReload(SubmitPlanForApproval, refreshEscalation)
const planAssistAndReload = withEscalationReload(PlanAssist, refreshEscalation)
const runEvidenceAndReload = withEscalationReload(wailsBindings.RunEvidence, refreshEscalation)
const submitTestContractAndReload = withEscalationReload(wailsBindings.SubmitTestContract, refreshEscalation)

async function decideGate(id: string, decision: string, reason: string, riskSelections: RiskSelection[]) {
  gateError.value = ''
  try {
    await GateDecide(id, decision, reason, riskSelections)
  } catch (e) {
    gateError.value = String(e)
  }
  await refreshGate()
  await refreshEscalation() // gateDecide 內 reconcile 可能補建／解除 blocker；失敗也可能已建立
}

// onMounted：既有三個 EventsOn 之後
// A2-1：後端在 reconcileGate1NotifyOnly() 已返回後才 emit（app.go:2633／2781）；
// reconcile 可能失敗，這裡只是「該重讀一次」的訊號，權威仍是 EscalationList。
wailsDisposers.push(EventsOn('spec:changed', () => { void refreshEscalation() }))
wailsDisposers.push(EventsOn('plan:changed', () => { void refreshEscalation() }))
```

template：

```vue
<SpecWorkspace v-if="tab === 'spec'" :submit="submitSpecAndReload" @busy="…" @dirty="…" />
<PlanWorkspace v-if="tab === 'plan'" :path="planFocusPath" :submit="submitPlanAndReload" :assist="planAssistAndReload" @escalate="onEscalate" @busy="…" @dirty="…" />
<TcaWorkspace … :run-evidence="runEvidenceAndReload" :submit-test-contract="submitTestContractAndReload" … />
```

`SubmitForApproval`／`SubmitPlanForApproval`／`PlanAssist` 目前不在 `lib/bindings.ts`；沿 `GateDecide` 的既有做法在 App.vue 直接 import，不擴 `makeBindings`（D7 已接受）。

- [ ] **Step 4: 跑測試確認通過** — `npx vitest run src/App.test.ts && npx vue-tsc --noEmit`。
- [ ] ~~**Step 5: Commit**~~ — **跳過**（Ruling 1，未執行）

### A2-1 驗證策略

| 層 | 指令／方式 | 判準 |
|---|---|---|
| 單元 | `cd frontend && npx vitest run` | 全綠；新增：store 3、escalationReload 4、Spec 2、Plan 1、App 7 |
| 型別／建置 | `npx vue-tsc --noEmit`、`npm run build` | exit 0 |
| mutation（各一格，紅在正題後回綠） | (1) `withEscalationReload` 的 `finally` 改 `try` 內只在成功後 reload → escalationReload 案例 2 紅；(2) 移除 store 成功分支的 `seq !== this.loadSeq` → store 案例 1 與 3 紅；(3) 移除 catch 分支的序號判斷 → store 案例 2 紅；(4) App.vue 的 Plan `:submit` 改傳原 `SubmitPlanForApproval` → App 案例 1 紅；(5) 移除 `EventsOn('plan:changed')` → App 案例 6 紅；(6) `decideGate` 移除 `refreshEscalation` → App 案例 5 紅 |
| Wails GUI（agent 執行，附截圖；`EscalationList` 輸出只作後端對照，不替代截圖） | 見下方「GUI fixture」 |

**GUI fixture（D6 裁定：本票 GUI 選用 `risk-unclassifiable` 與 `stale`；`missing-binding` 未做 GUI 驗證）**。`missing-binding` 有正式 UI 可達路徑（事實表 ★：TCA 兩筆 passed 後改 test commit 輸入為非 SHA 再送核），本輪未以 GUI 重現、本票不改 TCA 規則；其 GUI 驗證列為未涵蓋，只留 A2-2 的 Go 證據。

**隔離 fixture**：每段使用獨立的暫時工作區（`mktemp -d` 後 `git init`＋`git config user.name/email`），以 `WORKBENCH_WORKSPACE=<該目錄>` 啟動（README「環境變數」段；執行期狀態落在該工作區的 `.workbench/`），不用日常工作區。各段初始條件明列如下；**除第 2 段延續第 1 段外，其餘使用獨立工作區**。

1. **建立後出現（`risk-unclassifiable:<plan>`）**。初始：新工作區，`spec/glossary.md` 已 commit，Gate 1 已核可；`plan/P1.yaml`＋`risk-policy.yaml`（`default_tier: medium`）＋`permissions/T1.yaml` 已 commit，badge 為 0。操作：計畫工作區開 `plan/P1.yaml`，把 `minimum_risk_tier` 改 `low`（同 `app_escalation_test.go:414-416` fixture）→ 儲存 → commit → 送核。預期：送核紅字出現，**不切分頁**時 badge 由 0 變 1，收件匣有 `risk-unclassifiable:P1` open 項。截圖：紅字＋badge＋收件匣。
2. **解除後更新**。初始：**延續第 1 段結束狀態**（唯一允許延續的段，因解除必須以建立為前提）。操作：改回 `medium` → 儲存 → commit → 再送核。預期：紅字清空、badge 變 0、收件匣該項 resolved。截圖。
3. **watcher 同步（`stale:gate1:workspace`）**。初始：**另建**新工作區，Gate 1 已核可，badge 為 0，app 停在對話分頁。操作：在 app 外修改 `spec/glossary.md` 並 commit，不碰 app。預期：badge 出現 1、收件匣有 `stale:gate1:workspace` 項。截圖。
4. **等待中切走**。初始：**另建**新工作區並重新做到第 1 段「改 `low`、儲存、commit」但**尚未送核**，badge 為 0。操作：按送核後立刻切到對話分頁，再切回計畫分頁。預期：badge 為 1（由 App 包裝重載，非元件）。若本機送核回應太快無法在等待窗口內切走，以 App 案例 1 的自動化證據承接並在報告明列「GUI 第 4 段未涵蓋」。

### A2-1 未涵蓋項目

- `RunEvidence` 進行中 app shutdown：包裝仍會 reload，但 app 已關閉，失敗只落 `unavailable`；不測。
- `spec:changed`／`plan:changed` 短時間連發：兩次 `EscalationList`，由序號守衛保證終態；不做去抖。
- 手動 create／ack／resolve 已由 `EscalationInbox.test.ts:36-93` 涵蓋。
- `missing-binding` 的 GUI 驗證：有可達路徑但本票未做（D6），只有 A2-2 的 Go 證據。
- `bump-rerun-assist`（`PlanWorkspace.vue:560`）走同一個 `runAssist`，已由 `assist` prop 涵蓋，不另測。
- `SpecAssist`：無 blocker 路徑，不包裝、不測（D7）。
- GateConsole 重試候選：另案。

---

## 三、A2-2 後端三類 blocker 回歸測試

全部新增於 `app_escalation_test.go`；不改 production。共用 helper（本檔新增）：

```go
// entryByID：以 escalation_id 取回同一筆紀錄（含已 resolved），供「同一筆轉 resolved」斷言。
func entryByID(t *testing.T, a *App, id string) *escalation.Entry {
	t.Helper()
	entries, err := a.EscalationList()
	if err != nil {
		t.Fatalf("EscalationList: %v", err)
	}
	for i := range entries {
		if entries[i].Item.EscalationID == id {
			return &entries[i]
		}
	}
	return nil
}

// gateStateOf：以 approval_id 取回 GateList 投影的 state，供「pending 未被核可」斷言。
func gateStateOf(t *testing.T, a *App, approvalID string) string {
	t.Helper()
	list, err := a.GateList()
	if err != nil {
		t.Fatalf("GateList: %v", err)
	}
	for _, e := range list {
		if e.ApprovalID == approvalID {
			return e.State
		}
	}
	t.Fatalf("GateList 找不到 %s", approvalID)
	return ""
}

// assertBlockedBy：拒核錯誤必須來自 blocking escalation 檢查（app.go:5884-5889，
// 在 PrepareDecision 之後），且訊息含該 condition key——journal 已關閉時任何
// 後續 append 也會失敗，單憑「回錯」分辨不出拒絕來源。
func assertBlockedBy(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil {
		t.Fatalf("核可必須被拒（blocker %s）", key)
	}
	if !strings.Contains(err.Error(), "blocked by") || !strings.Contains(err.Error(), key) {
		t.Fatalf("拒核來源必須是 blocking escalation 且含 %q，got: %v", key, err)
	}
}
```

`GateEntryDTO` 的欄位名以 `app.go:5700-5716` 為準（json `approval_id`／`state`）。

### Task 5：`missing-binding:` 建立＋回傳錯誤＋擋下有效 pending＋同一筆解除

**Files:** Test `app_escalation_test.go`；參考 `app.go:6113-6145`、`app_gate_test.go:184-194`（`probePolicy` 施工約束）

**Interfaces（本檔新增）：**

```go
// failingPolicy：ValidateRequest 依 fail 旗標回錯——注入 a.gateReg，不是 production hook。
type failingPolicy struct {
	gate.GatePolicy
	fail *bool
}

func (p failingPolicy) ValidateRequest(req gate.GateRequest) error {
	if *p.fail {
		return errors.New("test: 缺必要 binding")
	}
	return p.GatePolicy.ValidateRequest(req)
}
```

- [ ] **Step 1: 寫測試**

```go
// ---- §3.8 (2) missing-binding：建立並回傳錯誤；hard workspace 項擋下有效 pending；Submit 成功後同一筆轉 resolved ----

func TestMissingBindingEscalationLifecycle(t *testing.T) {
	a := newTestAppGit(t)
	if _, err := a.SpecWrite("spec/glossary.md", "term v1", ""); err != nil { // 前置失敗不得誤歸因為 escalation 行為
		t.Fatalf("SpecWrite: %v", err)
	}
	commitAll(t, a)
	if _, err := a.ensureGate(); err != nil {
		t.Fatal(err)
	}
	fail := false
	a.gateReg["gate1"] = failingPolicy{GatePolicy: a.gateReg["gate1"], fail: &fail}

	// 先留一筆有效 pending（policy 正常）
	pendingID, err := a.SubmitForApproval()
	if err != nil {
		t.Fatal(err)
	}

	// 建立：ValidateRequest 失敗 → 建立 hard workspace 項，且仍回傳錯誤（兩者並存，§3.8）
	fail = true
	if _, err := a.SubmitForApproval(); err == nil {
		t.Fatal("ValidateRequest 失敗時送核必須回傳錯誤")
	}
	e := openItemByKey(t, a, "missing-binding:gate1:workspace") // gate1 subject 固定 "workspace"（app.go:3941）
	if e == nil {
		t.Fatal("ValidateRequest 失敗必須建立 missing-binding 項")
	}
	if !e.Item.Hard || e.Item.BlockScope != "workspace" {
		t.Fatalf("missing-binding 必須 hard 且 scope=workspace，got hard=%v scope=%q", e.Item.Hard, e.Item.BlockScope)
	}
	itemID := e.Item.EscalationID

	// 阻擋：既有的有效 pending 被該 blocker 擋下（§3.10 順序 3；檢查位於 gateDecide、PrepareDecision 之後）
	assertBlockedBy(t, a.GateDecide(pendingID, "approved", "x", nil), "missing-binding:gate1:workspace")
	if st := gateStateOf(t, a, pendingID); st != "pending" {
		t.Fatalf("被擋下的 pending 必須仍為 pending，got %q", st)
	}

	// 解除：修正後 Submit 成功 → 同一筆轉 resolved（不是查不到 open 就算）
	fail = false
	pendingID2, err := a.SubmitForApproval()
	if err != nil {
		t.Fatalf("修正後送核必須成功: %v", err)
	}
	got := entryByID(t, a, itemID)
	if got == nil || got.State != "resolved" {
		t.Fatalf("Submit 成功後同一筆 %s 必須為 resolved，got %+v", itemID, got)
	}
	if openItemByKey(t, a, "missing-binding:gate1:workspace") != nil {
		t.Fatal("解除後不得仍有同 key 的未 resolved 項")
	}
	if err := a.GateDecide(pendingID2, "approved", "ok", nil); err != nil {
		t.Fatalf("blocker 解除後核可必須通過: %v", err)
	}
}
```

- [ ] **Step 2: 跑測試** — `go test ./ -run TestMissingBindingEscalationLifecycle -count=1 -race`，預期綠（production 已實作）。
- [ ] **Step 3: mutation 兩格** — 註解 `app.go:6120` create → 建立段紅；註解 `:6140` resolve → 「同一筆 resolved」段紅。還原。
- [ ] ~~**Step 4: Commit**~~ — **跳過**（Ruling 1，未執行）

### Task 6：`negative-control-missed:` 建立＋同 key passed 解除（patch 只改範圍外受測檔）

**Files:** Test `app_escalation_test.go`；參考 `app_evidence_test.go:382-418`、`app.go:6076-6102`、`matcher.go:43-49`、`runner.go:129-130`

fixture：`run_test.sh`（oracle surface 內，**不變異**）讀取範圍外的受測檔 `impl.txt`；兩份 patch 只改 `impl.txt`：

```sh
#!/bin/sh
if grep -q correct impl.txt; then echo ok; exit 0; fi
echo 'FAIL: TestX'; exit 1
```

- patch A：`impl.txt` 由 `correct v1` 改 `correct v2`（仍含 `correct`）→ 測試仍綠 → exit 0 → `failed`（mutation 未被抓到）→ 建立。
- patch B：`impl.txt` 改 `broken` → `FAIL: TestX` exit 1 → `passed` → 解除。

- [ ] **Step 1: 寫測試**

```go
// ---- §3.8 (7) negative-control-missed：failed 建立（soft、tca scope）；同 key 新 run passed 解除 ----

const negativeControlScript = "#!/bin/sh\nif grep -q correct impl.txt; then echo ok; exit 0; fi\necho 'FAIL: TestX'; exit 1\n"

func mutationPatchOf(t *testing.T, a *App, mutate func()) string {
	t.Helper()
	mutate()
	patch, err := exec.Command("git", "-C", a.workspaceDir, "diff", "HEAD").Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	runGit(t, a, "checkout", "--", ".")
	return string(patch)
}

func TestNegativeControlMissedEscalationLifecycle(t *testing.T) {
	a, _ := newTestAppEvidence(t)
	writeFile(t, filepath.Join(a.workspaceDir, "run_test.sh"), negativeControlScript)
	writeFile(t, filepath.Join(a.workspaceDir, "impl.txt"), "correct v1\n")
	planCommit := setupApprovedEvidencePlan(t, a, "P1") // oracle surface 只含 run_test.sh；impl.txt 在範圍外
	approvalID := activeApprovalIDFor(t, a, "P1")

	pA := mutationPatchOf(t, a, func() { writeFile(t, filepath.Join(a.workspaceDir, "impl.txt"), "correct v2\n") })
	mA, err := a.RegisterMutation("P1/T1", pA)
	if err != nil {
		t.Fatal(err)
	}
	evA, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "negative_control", mA)
	if err != nil {
		t.Fatalf("RunEvidence A: %v", err)
	}
	runA, err := a.EvidenceGet(evA)
	if err != nil {
		t.Fatalf("EvidenceGet A: %v", err)
	}
	if runA.Result != "failed" {
		t.Fatalf("前提不成立：patch A 應得 result=failed（tests did not fail），got %q", runA.Result)
	}
	e := openItemByKey(t, a, "negative-control-missed:P1/T1")
	if e == nil {
		t.Fatal("negative_control result=failed 必須建立 negative-control-missed 項")
	}
	if e.Item.Hard || e.Item.BlockScope != "tca:P1/T1" {
		t.Fatalf("必須 hard=false 且 scope=tca:P1/T1，got hard=%v scope=%q", e.Item.Hard, e.Item.BlockScope)
	}
	itemID := e.Item.EscalationID

	pB := mutationPatchOf(t, a, func() { writeFile(t, filepath.Join(a.workspaceDir, "impl.txt"), "broken\n") })
	mB, err := a.RegisterMutation("P1/T1", pB)
	if err != nil {
		t.Fatal(err)
	}
	evB, err := a.RunEvidence(approvalID, "P1", "T1", planCommit, "negative_control", mB)
	if err != nil {
		t.Fatalf("RunEvidence B: %v", err)
	}
	runB, err := a.EvidenceGet(evB)
	if err != nil {
		t.Fatalf("EvidenceGet B: %v", err)
	}
	if runB.Result != "passed" {
		t.Fatalf("前提不成立：patch B 應得 result=passed，got %q", runB.Result)
	}
	if got := entryByID(t, a, itemID); got == nil || got.State != "resolved" {
		t.Fatalf("同 key 新 run passed 後同一筆 %s 必須為 resolved，got %+v", itemID, got)
	}
	assertNoZombieWorktrees(t, a.workspaceDir)
}
```

- [ ] **Step 2: 跑測試** — `go test ./ -run TestNegativeControlMissedEscalationLifecycle -count=1 -race`，預期綠；兩個「前提不成立」若紅，先修 fixture（腳本、`impl.txt`、matcher）再談 escalation。
- [ ] **Step 3: mutation 兩格** — `app.go:6097` 的 `kind == "negative_control"` 改 `"never"` → 建立段紅；註解 `:6088` → 解除段紅。還原。
- [ ] ~~**Step 4: Commit**~~ — **跳過**（Ruling 1，未執行）

### Task 7：`journal-degraded:gate` 建立（拒核）＋重開後同一筆 resolved

**Files:** Test `app_escalation_test.go`；參考 `app.go:6018-6032`、`:6060-6070`、`journal.go:125-138`、`app_gate_test.go:355`、`app_restore_dormant_test.go:23`

fixture 依 owner 修正：**只有有效 pending、沒有 Active 紀錄**（`Reconcile()` 對非 Active 不 append，`service.go:281-283`），journal 關閉後 `svc.List()` 不會先在 stale transition 失敗。

- [ ] **Step 1: 寫測試**

```go
// ---- §3.8 (8) journal-degraded：degraded 時補建 workspace hard 項並拒核；journal 重開後同一筆轉 resolved ----

func TestJournalDegradedEscalationLifecycle(t *testing.T) {
	a := newTestAppGit(t)
	if _, err := a.SpecWrite("spec/glossary.md", "term v1", ""); err != nil {
		t.Fatalf("SpecWrite: %v", err)
	}
	commitAll(t, a)
	if _, err := a.ensureGate(); err != nil {
		t.Fatal(err)
	}
	pendingID, err := a.SubmitForApproval() // 唯一紀錄：pending，無 Active → Reconcile 不寫入
	if err != nil {
		t.Fatal(err)
	}

	// 注入：關閉 gate journal 檔案 → 下一次 append 失敗並設 degraded（journal.go:133）
	if err := a.gateJournal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SubmitForApproval(); err == nil {
		t.Fatal("journal 已關閉，Submit 的 append 必須失敗（用以觸發 degraded）")
	}
	if !a.gateJournal.Degraded() {
		t.Fatal("前提不成立：append 失敗後 gate journal 應為 degraded")
	}

	// 建立：reconcile → svc.List()（無 Active，不 append）→ escJournalDegradedLocked 建立
	a.reconcileGate1NotifyOnly()
	e := openItemByKey(t, a, "journal-degraded:gate")
	if e == nil {
		t.Fatal("gate journal degraded 時 reconcile 必須建立 journal-degraded:gate 項")
	}
	if !e.Item.Hard || e.Item.BlockScope != "workspace" {
		t.Fatalf("必須 hard 且 scope=workspace，got hard=%v scope=%q", e.Item.Hard, e.Item.BlockScope)
	}
	itemID := e.Item.EscalationID

	// 拒核：degraded 期間有效 pending 不得核可——且必須是 blocker 擋的（journal 已關閉，
	// 後面的 append 本來就會失敗，只斷言回錯分辨不出來源）
	assertBlockedBy(t, a.GateDecide(pendingID, "approved", "x", nil), "journal-degraded:gate")
	if st := gateStateOf(t, a, pendingID); st != "pending" {
		t.Fatalf("被擋下的 pending 必須仍為 pending，got %q", st)
	}

	// 解除：同 stateDir 重啟 → journal 重開且健康 → 先確認讀回同一筆未解除項，再 reconcile → 同一筆 resolved
	a2 := newTestAppAt(t, a.stateDir)
	a2.workspaceDir = a.workspaceDir
	if _, err := a2.ensureGate(); err != nil {
		t.Fatal(err)
	}
	if a2.gateJournal.Degraded() {
		t.Fatal("前提不成立：重開後 gate journal 不應 degraded")
	}
	before := entryByID(t, a2, itemID)
	if before == nil || before.State == "resolved" {
		t.Fatalf("重啟後必須讀回同一筆未解除項 %s，got %+v", itemID, before)
	}
	a2.reconcileGate1NotifyOnly()
	after := entryByID(t, a2, itemID)
	if after == nil || after.State != "resolved" {
		t.Fatalf("journal 重開後 reconcile 必須把同一筆 %s 轉 resolved（app.go:6069），got %+v", itemID, after)
	}
}
```

**待實測假設**：`newTestAppAt` 加 `workspaceDir` 後，`ensureGate` 能於同 stateDir 重開 gate／escalation journal（`app_restore_dormant_test.go:23-40` 顯示它建 eventSink 與 replay index，gate 由 `ensureGate` 延遲建立）。不成立時改為在測試內以 `newTestAppGit` 的建構步驟指定同一 stateDir，不加 production 介面（D4 已接受）。

- [ ] **Step 2: 跑測試** — `go test ./ -run TestJournalDegradedEscalationLifecycle -count=1 -race`。
- [ ] **Step 3: mutation 兩格** — `app.go:6064` `if degraded` 改 `if false` → 建立段紅；`:6069` 改 `return nil` → 解除段紅。還原。
- [ ] ~~**Step 4: Commit**~~ — **跳過**（Ruling 1，未執行）

### A2-2 驗證策略

| 層 | 指令 | 判準 |
|---|---|---|
| 目標測試 | `go test ./ -run 'MissingBinding|NegativeControlMissed|JournalDegraded' -count=1 -race` | 三條綠；四個「前提不成立」與所有前置（SpecWrite／EvidenceGet／GateList）錯誤斷言皆未觸發 |
| 全套 | `go test ./... -count=1 -race`；`gofmt -l .` | rc 0；gofmt 無輸出 |
| mutation | 六格（每 key 建立／解除各一） | 紅在正題、還原回綠；位元組備份還原 |
| 殘留 | `assertNoZombieWorktrees` | 無殘留 |

### A2-2 未涵蓋項目

- `journal-degraded:evidence`：同函式同分支，只差 `which`；不重複測。
- `missing-binding` 的 gate2／TCA subject 變體：三條路徑共用 `submitGateRequest`，只測 gate1；scope 字串無專門斷言。
- `evidence-error` 的 runner 啟動失敗路徑（`app.go:5362`）：非本票 key。
- Task 7 拒核斷言的前提：`reconcileLocked` 內 `escJournalDegradedLocked` 對已存在項為去重 no-op（escalation journal 仍健康），`svc.PrepareDecision` 不 append，之後 `gateDecide` 的 blocker 檢查（`app.go:5884-5889`）以含 key 的訊息拒核。若實測發現 `PrepareDecision` 在 degraded gate journal 下先回錯，`assertBlockedBy` 會紅，屆時回報而非放寬斷言。

---

## 四、估點（0.1 pt＝1 hr；**owner 2026-09-08 接受 1.07 pt 為規劃估點**；實際工時見第七節）

| 票 | 項目 | hr |
|---|---|---|
| A2-1 | Task 1 store 序號守衛＋3 測試 | 0.6 |
| A2-1 | Task 2 `withEscalationReload`＋4 測試 | 0.5 |
| A2-1 | Task 3 兩元件 prop 注入＋3 測試（含 D3 Spec 專門回歸） | 1.2 |
| A2-1 | Task 4 App.vue 五個包裝、prop 接線、watcher、mock 前置改動＋7 測試 | 2.2 |
| A2-1 | mutation 六格＋vue-tsc／build＋GUI 四段（含截圖） | 1.5 |
| **A2-1 小計** | | **6.0（0.6 pt）** |
| A2-2 | Task 5 missing-binding（failingPolicy、entryByID、阻擋與同一筆斷言） | 1.0 |
| A2-2 | Task 6 negative-control（範圍外受測檔 fixture、兩份 patch、前提斷言） | 1.3 |
| A2-2 | Task 7 journal-degraded（pending-only fixture、重啟、同一筆斷言；含 fixture 假設不成立的替代） | 1.7 |
| A2-2 | mutation 六格＋全套 race＋gofmt | 0.7 |
| **A2-2 小計** | | **4.7（0.47 pt）** |
| **合計** | | **10.7 hr（1.07 pt，owner 2026-09-08 接受為規劃估點）**；10.0–11.5 hr 為不確定區間，非已驗證工時 |

較 rev1（8.2 hr）增加的來源：卸載情境改為 App 持有包裝（多一個 lib＋兩元件 prop）、`SubmitTestContract` 納入、App 測試由計次改為斷言清單與 badge、GUI 增加截圖與四段、Go 三條增加「擋下 pending」與「同一筆 resolved」斷言。A2-1 為 0.6 pt，未跨 2.0 pt 拆票門檻；兩票性質不同已分開。

## 五、Design gate 裁定紀錄（rev3 待確認三項已於 rev3 design gate 接受）

- **D1–D4（2026-09-08 接受）**：App 持有包裝、optional prop、Spec 專門回歸、修正後 fixture 方向。
- **D5（接受）**：1.07 pt 為規劃估點；10–11.5 hr 為不確定區間。
- **D6（修正後接受）**：GUI 選用 `risk-unclassifiable`／`stale`；`missing-binding` 有 UI 可達路徑（TCA test commit 輸入）但本票未做 GUI 驗證，不改 TCA 規則。
- **D7（接受＋修正）**：App 直接 import，不擴 `makeBindings`；`SpecAssist` 無 blocker 路徑，已移除包裝。
- **rev3 待確認**：(a) Task 2 的 reload 契約改為「reload 不拋、wrapper 不吞錯」＋新增等待順序測試；(b) Task 5／7 改斷言 `blocked by`＋condition key＋pending 仍為 pending；(c) GUI 四段各自隔離 fixture 與初始條件。
- **後續裁定（owner 2026-09-08 rev3 design gate 通過）**：上列 (a)(b)(c) 三項落實方式均接受——(a) reload 契約由 reload 處理查詢失敗、延遲 Promise 測試檢查等待順序；(b) `blocked by`＋condition key＋pending 狀態可區分 blocker 拒核與 journal 寫入失敗；(c) 第 1／2 段共用建立與解除情境、第 3／4 段各用獨立工作區。隨後 A2-1（2026-09-09）與 A2-2（2026-09-09）驗收通過，A2 aggregate 關票，見第七節。

## 七、關票回填（owner 2026-09-09 裁定；rev4）

### 7.1 驗收與綁定
- **A2-1**：2026-09-09 驗收通過（自動化 review PASS 0 findings＋補正＋agent 執行的 Wails GUI 四段，含第 4 段涵蓋）。
- **A2-2**：2026-09-09 驗收通過（自動化 review PASS 0 findings；全套 `go test ./... -count=1 -race` rc 0、23 套件 ok；G1–G7）。
- **A2 aggregate 關票**。裁定綁定＝**base `6a32b2318d9b2453fbcba861261f1db967bcd394`＋兩份受測快照**（工作樹未提交，故不能只寫 base SHA）：
  - A2-1 快照：13 檔（11 tracked 修改＋2 新檔），tracked diff sha256 `36bc9fb779290a66dfe6b8de836b565e1e78f5b9e64fd30f154aa6ee66118af1`；各檔 hash 見封存 `snapshot/manifest.txt`。GUI 受測執行檔 sha256 `28ac45eff17461b7101cfa1e77cbe9f1189064763e6f8f3953f0a1bd0a9d6fe3`（`npm run build` → `wails build -s`，無 tracked 再生差異）。
  - A2-2 快照：`app_escalation_test.go` sha256 `ef97d234a818809773e378e5aea84af3b8cded7c4e50b29e4b5ba8691d6d0acb`（+253 行），tracked diff sha256 `209625b3ec56595ced47fd2dbb824e9574d3b1815fa269ff2fe4b2a2151a5ee6`；`app.go` 與 `internal/{escalation,gate,journal,evidence}` 五檔與 HEAD 位元組相同。
- **未提交、未推送、未追加 PR #5、未合併**；分支 `docs/a1a-closure-readme` 工作樹另含本 plan（untracked）。

### 7.2 證據封存（`~/.local/share/sdlc-evidence/`，MANIFEST.sha256 皆驗證 0 筆不符；**備份未確認**）
| 批次 | 檔數 | MANIFEST sha256（前 16） | 內容 |
|---|---|---|---|
| `a2-1/6a32b23…/review-20260908T170306Z` | 12 | `1419e3169d71c09e` | 快照、criteria、review 報告 v1、四份 task 報告、ledger |
| `a2-1/6a32b23…/review-supplement-20260909T005545Z` | 12 | `a6ef480ca1c445e5` | reviewer 原始 jsonl、三個 mutation 前備份、vue-tsc.log／rc（真實 rc 0）、自構卸載反證測試檔與完整輸出、副本 hash 前後、報告 v2 |
| `a2-1/6a32b23…/gui-20260909T014752Z` | 105 | `ddd6ce0309d4dd35` | 75 張截圖（round 1＋2）、兩輪報告、brief、controller 附註、五個工作區 git log、jsonl 對照、建置 log、GUI agent 原始 jsonl、`seg4b-timing.md` |
| `a2-2/6a32b23…/review-20260909T032351Z` | 71 | `75d99fe184592d30` | 快照、criteria、G1–G7 變異 diff／變異檔／備份／log／rc、全套 log（rc 0）、review 報告、reviewer 與三位 implementer 原始 jsonl |

### 7.3 保留限制
1. GUI 由 **agent 透過 osascript 操作，非 owner 親自操作**；GUI 證據與涵蓋限制已經驗收裁定接受（owner 2026-09-09）；第 1 段 `minimum_risk_tier` 編輯以 shell 完成，送核／核可／切分頁／觀察皆 GUI。round 1 第 2 段因 fixture 缺 `.gitignore` 不採信；採信的 ws1b／ws3／ws4b 未包含 `.workbench/`。
2. `missing-binding` 有正式 UI 可達路徑（TCA test commit 輸入）但**未做 GUI 驗證**，只有 A2-2 Go 證據（D6）。
3. A2-1 首輪 M1–M6 的**變異後檔案內容未保留**（僅 sha256；補正時揭露，未重做）；A2-2 G1–G7 變異檔完整保留。首輪 `| tail` 輸出標為可能截短。
4. **備份未確認**（持久證據區只確認位置已建立並驗證 manifest）。
5. 附帶觀察（owner 裁定）：`wails build -s` 沿用舊 dist 為既定行為，不立票，保留建置順序；`.workbench/` 排除提示列為文件／新工作區初始化候選，另案。

### 7.4 Rulings（controller 執行期裁定，見 ledger）
- Ruling 1：跳過各 Task 的 commit 步驟（owner 明定不含提交）；review package 以工作樹 diff 快照產生。
- Ruling 2：review 檢查點放票級（A2-1、A2-2 各一次），票內 task 連續實作。
- Ruling 3：implementer 不得 git 寫入、不派 subagent；mutation 位元組備份還原。
- Ruling 4：Task 4 連鎖紅的三個既有 mock 檔各補三個 `vi.fn()`（App.test.ts 既有註解的義務），owner 已核對。
- Ruling 5：GUI round 1 第 2 段不採信，修正 fixture 重跑第 1／2／4 段。
- 另：Task 5 implementer 以 `go build ./` 留下 repo 根目錄執行檔，controller 已刪除。

### 7.5 估點對實際
- 核定估點 **1.07 pt（10.7 hr）**：A2-1 6.0 hr／A2-2 4.7 hr。
- **實際工時未完整統計**：無逐 task 工時紀錄；子代理執行時長（implementer 約 1.5–21 分鐘／task、reviewer 約 8 分鐘、GUI 兩輪約 33 分鐘）為壁鐘時間、含等待，**不得換算為工時**。

## 六、修訂記錄

- rev4（2026-09-09）：關票回填（補正：第五節補後續接受裁定、§7.3 第 1 項改為操作歸屬與裁定分述）——新增第七節（驗收與綁定、證據封存、保留限制、rulings、估點對實際）；狀態改為已完成／關票；各 Task 的 commit 步驟標為跳過（Ruling 1）不勾；估點標題改為已核定。實作內容與測試結論不變。
- rev3（2026-09-08）：依 owner 靜態複核——D6 改寫（TCA test commit 輸入為 UI 可達路徑，本票未做 GUI 驗證，不寫「不可能」）；Task 2 明訂 reload 不拋、wrapper 不吞錯，新增「reload 完成前 wrapped 未結束」測試；Task 5／7 拒核斷言改為含 `blocked by` 與 condition key、並斷言 pending 仍為 pending，新增 `gateStateOf`／`assertBlockedBy`；「PrepareDecision 的 blocker 檢查」更正為 `gateDecide` 內、PrepareDecision 之後（`app.go:5884-5889`）；store mutation 對應改為 catch 分支→案例 2；Spec 解除案例改名「未解除項目清空」並斷言 resolved entry 仍在；移除 `PreviewSpecCommit` 待查註記；GUI 四段明訂隔離 fixture 與初始條件、第 4 段重新建立情境；Go fixture 檢查 `SpecWrite`／`EvidenceGet` 錯誤；D7 移除 `SpecAssist` 包裝。估點維持 1.07 pt。
- rev2（2026-09-08）：依 owner design gate 五項修正——(1) 子元件 finally emit 改為 App 持有的 `withEscalationReload` 包裝＋optional prop 注入，補「等待中切走→完成→收件匣更新」測試；(2) 納入 `SubmitTestContract` 包裝；(3) Task 6 改以範圍外受測檔 `impl.txt` 控制結果，腳本與 oracle 宣告不變；(4) Task 7 改 pending-only fixture，重啟後先讀回同一筆再驗 resolved；(5) App 測試先切分頁、Spec／Plan 皆實際涵蓋、斷言清單與 badge。另補：store 第三案（較新失敗後舊成功不得清 unavailable）；missing-binding 補擋下 pending 與同一筆 resolved；GUI fixture 改用可觸發 key 並附截圖要求；watcher 註解改「reconcile 已返回」；D3 Spec 專門回歸；重估 1.07 pt。
- rev1（2026-09-08）：初版草稿。
