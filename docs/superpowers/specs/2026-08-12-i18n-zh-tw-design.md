# 前端 i18n（vue-i18n）設計

- 日期：2026-08-12
- 狀態：設計定稿（待 closure review 後進 writing-plans）
- 分支：`i18n-zh-tw`（自 main `c2aab34`，M2 Stage A 已 merge）
- 上游：`docs/i18n-zh-TW.md`（owner-reviewed 譯文快照，本設計的**審核來源**，非 runtime 權威）

---

## 1. 目標與範圍

把前端所有**固定、user-visible 字串**語系化（vue-i18n），預設 **zh-TW**，同時建立完整 **en** locale，讓介面可整套切換（本階段不做 UI 切換鈕）。

**範圍（owner 拍板，2026-08-12）**：**所有固定 user-visible 字串**（含目前硬編英文 **與** 目前硬編中文；涵蓋按鈕、tab、placeholder、title/tooltip、空狀態、前端錯誤前綴、store 產生的固定訊息）皆納入雙 locale，讓 `en` 是**完整英文介面**、`zh-TW` 是完整中文介面。**確切字串清單以 writing-plans 的完整 inventory 為準**（不預設數量）。

**不翻譯（維持原文）**：後端原始錯誤內容、provider 名（`claude`/`codex`）、wire 值（`on-request`/`never`/`untrusted`）、事件 `kind`、稽核／資料契約的**欄位值**與 bindings `kind`、技術術語（`Gherkin`/`oracle`/`session`/`tokens`/`commit`）。

**不做**：語系切換 UI（設定頁面日後另議）。

---

## 2. 架構（vue-i18n Composition API）

- 相依：`vue-i18n ^11.4.8`（搭 Vue `^3.5`）；實際版本由 `package-lock.json` 鎖定（沿 repo 慣例）。
- **Composition API 模式（凍結）**：
  ```ts
  createI18n({
    legacy: false,
    locale: 'zh-TW',
    fallbackLocale: 'en',
    messages: { 'zh-TW': zhTW, en },
  })
  ```
  `legacy: false` → 所有 `<script setup>` 統一用 `useI18n()`（不用 legacy `$t`）。
- **非元件呼叫規則（P1-1，凍結）**：不是每個產生固定 UI 字串的地方都是元件。
  - **元件（`<script setup>`）**：`useI18n()` 的 `t`。
  - **Pinia store／一般 TS 模組**（產生固定 UI 訊息，如 `session.ts` 目前直接 emit 的 `bindings not ready`）：用 **`i18n.global.t()`**（從 `i18n/index.ts` export `i18n` 實例）。
  - **後端／provider 回傳的動態錯誤內容**：原樣保留、不經 i18n（只有固定前綴翻譯，見 §5）。
- 佈局：
  - `frontend/src/i18n/index.ts`— `createI18n(...)` 與 export。
  - `frontend/src/i18n/locales/zh-TW.ts`、`frontend/src/i18n/locales/en.ts`— 訊息物件（**runtime 唯一權威**）。
  - `frontend/src/i18n/stateKeys.ts`— 三份 raw value → i18n key 固定映射（§4）。
  - `frontend/src/main.ts`— `app.use(i18n)`。
- **Source of truth**：`zh-TW.ts` 是 runtime 權威；`docs/i18n-zh-TW.md` 是審核來源／文字快照。兩者不並列為 source of truth（避免雙頭）。

---

## 3. Key 命名（語意分層，凍結）

依**語意**分層，**不**依元件位置、**不**拿英文原文當 key。範例：

```
app.tab.chat / app.tab.preview / app.tab.spec
app.timeline.label
settings.action.new / settings.action.terminate / settings.action.end
settings.action.login / settings.action.logout / settings.action.cancelLogin / settings.action.authStatus
settings.operationAction.new / ...authStatus / ...cancelLogin / ...   # 句中動作（與按鈕文字分開，見 §7）
settings.operation.success / settings.operation.failure   # 帶 {action}（+ {error}）interpolation
approval.action.allow / approval.action.deny
approval.reason.placeholder
gate.action.approve / gate.action.reject
gate.state.pending / gate.state.active / gate.state.stale / gate.state.superseded
gate.reason.placeholder / gate.degradedNotice
gate.label.approvalId / gate.label.baseCommit / gate.label.specManifest   # 中英並列標籤
spec.action.submit / spec.action.previewCommit / spec.action.confirmCommit / spec.action.acceptDraft
spec.assist.drafting                  # 「AI 產生中…」
spec.commitMessage.placeholder
chat.thinking / chat.input.placeholder
session.state.idle / session.state.waiting / ...
timeline.result.failed / timeline.result.completed
timeline.toolStatus.completed / timeline.toolStatus.inProgress / timeline.toolStatus.failed
timeline.raw
```

## 4. 動態狀態映射（三份獨立 raw→key，凍結）

**不**抽「翻譯後的 label 表」，而抽 **raw value → i18n key** 的固定映射，各狀態維度**三份獨立**：

```ts
// i18n/stateKeys.ts
export const sessionStateKeys = {
  idle: 'session.state.idle', waiting: 'session.state.waiting',
  streaming: 'session.state.streaming', tool_running: 'session.state.toolRunning',
  awaiting_approval: 'session.state.awaitingApproval', retrying: 'session.state.retrying',
  done: 'session.state.done', failed: 'session.state.failed',
} as const
export const gateStateKeys = {
  pending: 'gate.state.pending', active: 'gate.state.active',
  stale: 'gate.state.stale', superseded: 'gate.state.superseded',
} as const
export const codexToolStatusKeys = {
  completed: 'timeline.toolStatus.completed', inProgress: 'timeline.toolStatus.inProgress',
  failed: 'timeline.toolStatus.failed',
} as const
```

- StatusBar 與 Timeline 各自 `t(sessionStateKeys[state])`；Gate 徽章 `t(gateStateKeys[state])`；Timeline codex tool `t(codexToolStatusKeys[status])`。
- **unknown passthrough**：未知的 state/status **原樣顯示**（回退到 raw value），**不得**顯示缺漏的 key 字串（如 `session.state.foo`）。StatusBar 現有 `stateLabel[s.state] ?? s.state` 的 fallback 精神保留。

## 5. Interpolation（凍結）

用 `{action}`／`{error}`／`{status}` 組**完整句子**，**不在元件內自行串接標點**。例：
- `settings.operation.success` = `zh-TW: "{action}成功"` / `en: "{action} ok"`
- `settings.operation.failure` = `zh-TW: "{action}失敗：{error}"` / `en: "{action} failed: {error}"`
- 呼叫時 `{action}` 帶**句中動作**（§7 的 `settings.operationAction.*`，非按鈕文字），`{error}` 帶原始英文錯誤內容。
- 固定錯誤前綴翻譯、**後端原始錯誤內容維持英文**：如 `app.startupError` = `"啟動：{error}"`，`{error}` 帶原始英文內容。

## 6. 中英並列標籤（凍結）

主畫面的資料契約**標籤**進 locale（欄位**值**不翻）：
- `gate.label.approvalId` = `"核可編號（approval_id）"` / `"approval_id"`
- `gate.label.baseCommit` = `"基準 commit（base_commit）"` / `"base_commit"`
- `gate.label.specManifest` = `"規格 manifest（spec_manifest）"` / `"spec_manifest"`

詳細 bindings／稽核畫面**直接顯示原始欄位名與值**（不經 i18n），確保與 `.workbench/*.jsonl` 對照。

## 7. SettingsBar 操作結果事件（P1-2，凍結）

SettingsBar 在 **emit 當下翻譯**操作結果，成功走 `settings.operation.success`、**失敗走 `settings.operation.failure`**（§5）。

**完整操作清單（8 項）**：`new` / `terminate` / `end` / `auth` / `login` / `cancel-login` / `logout` / `b1-probe`。每項都有成功與失敗兩條路徑。

**按鈕文字與句中動作分開**（避免「登入狀態成功」不自然）：
- `settings.action.authStatus` = `"登入狀態"`（**按鈕**）
- `settings.operationAction.authStatus` = `"查詢登入狀態"`（**句中動作**，餵給 `{action}` → 「查詢登入狀態成功」）
- 其餘操作同理各有 `settings.action.*`（按鈕）與 `settings.operationAction.*`（句中）兩支 key。

實作時定位確切 emit 來源，確認 8 項 × {成功,失敗} 全覆蓋。

---

## 8. 驗證策略（凍結，含兩個實作要求）

**測試 harness（實作要求 1）**：現有元件測試直接 `mount()`，導入 `useI18n()` 後會缺 i18n plugin。建 **`mountWithI18n()`／test i18n factory**，每個測試取得**隔離的 i18n 實例**（避免跨測試污染 locale 狀態）。既有元件測試（GateConsole/SpecWorkspace/ApprovalDialog/ChatPanel/SettingsBar…）改用它、斷言改吃譯文後仍綠。

**locale key 一致性（實作要求 2）**：
- **遞迴**比較 `zh-TW` 與 `en` 的**所有 leaf path**：key 集合一致、且每個 path 的**型別一致**（object vs string 不可錯位）。
- **動態狀態全覆蓋**：測 `sessionStateKeys`／`gateStateKeys`／`codexToolStatusKeys` 的**所有已知 raw value** 都有對應 key 且能 `t()` 出非-key 字串；**加測 unknown passthrough**（未知值回退 raw、不顯示缺漏 key）。不能只驗幾個代表值。

**元件引用正確性（實作要求 3，P1-3）**：parity 只證兩 locale key 集合一致——若元件誤寫 `t('gate.action.aprove')`，兩份 locale 仍過 parity 但畫面漏 key。需**至少一種保證**：
- (a) **TypeScript 層**：以 message schema 型別限制 `t()` 接受的 key union（`vue-i18n` 的型別擴充／`DefineLocaleMessage`），誤 key 編譯即報；**或**
- (b) **測試掃描**：掃所有 literal `t('...')` 引用，斷言每個 literal key 都存在於 locale leaf paths。
（動態 key 由三份映射測試負責，不在此列。）

**`en` 完整性（P1-3）**：加**代表性 `locale: 'en'` render 測試**，確認原本硬編中文的畫面（如 GateConsole degraded 通知、SpecWorkspace「AI 產生中…」）能**完整轉為英文**——只驗預設 zh-TW 不足以支撐「完整英文介面」的宣稱。

**行為**：預設 locale = zh-TW；代表性元件 render 出中文；interpolation 組句正確（標點在 locale 內、非元件串接）。

**收尾 gate**：`cd frontend && npx vitest run` 全綠、`npm run build`（vue-tsc）乾淨、**`wails build` 成功**（本次改相依＋`main.ts` plugin 掛載＋production bundle，須驗桌面封裝整合，非只 vitest/vite build）。Go 端不受影響。

---

## 9. Backlog（本階段可接受限制）

- **無語系切換 UI**：設定頁面日後另議。
- **Timeline note 不隨 locale 重譯**：因無切換，既存 Timeline 條目的 note 於 locale 改變時不會重譯——本階段可接受，寫入 backlog（未來有切換時處理）。

## 10. 風險

1. **漏抽字串**：以 §8 的 key 一致性測試 ＋ 一次全元件掃描降低；但無法保證 100%，收尾人工掃一遍。
2. **既有測試改寫量**：多個元件測試需接 `mountWithI18n()`；bounded（元件數有限）。
3. **vue-i18n 版本**：`^11.4.8` 搭 Vue 3.5；以 package-lock 鎖定，build/vitest 驗證相容。
