# UI i18n 對照表（zh-TW）

供日後前端語系化（vue-i18n 或直接 zh-TW 取代）當作 key / 譯文來源。
內容已納入 owner 2026-08-12 的審核修訂與漏掃補齊。

原則：**固定 UI 前綴／標籤／按鈕譯中文；provider 名、wire 值、事件 kind、原始錯誤內容、技術術語保留英文；資料契約欄位名保留原文（主畫面可中英並列，詳細/稽核畫面直接顯示原文以對照 JSONL）。**

---

## A. 翻譯（固定 UI 字串）

| 元件 | 原文／來源 | 繁中 |
|---|---|---|
| App.vue（分頁） | `Chat` | 對話 |
| App.vue（分頁） | `Preview` | 預覽 |
| App.vue（分頁） | `Spec` | 規格 |
| App.vue（`表示圖`） | — | （已中文） |
| App.vue（tl-toggle）| `▾ Timeline` / `▸ Timeline` | ▾ 執行時間軸 / ▸ 執行時間軸 |
| App.vue（錯誤前綴）| `startup:` | 啟動： |
| SettingsBar | `New` | 開新對話 |
| SettingsBar | `Terminate` | 強制終止 |
| SettingsBar | `End` | 結束 |
| SettingsBar | `Auth` | 登入狀態 |
| SettingsBar | `Login` | 登入 |
| SettingsBar | `Cancel` | 取消登入 |
| SettingsBar | `Logout` | 登出 |
| ChatPanel | `thinking` | 思考過程 |
| GateConsole | `Approve` | 核可 |
| GateConsole | `Reject` | 退回 |
| SpecWorkspace | `Accept` | 套用草稿 |
| SpecWorkspace | `Submit for Approval` | 送核 |
| SpecWorkspace | `Preview Commit` | 預覽 commit |
| SpecWorkspace | `Confirm Commit` | 建立 commit |
| SpecWorkspace（placeholder）| `commit message` | commit 訊息 |
| ApprovalDialog | `Allow` | 允許 |
| ApprovalDialog | `Deny` | 拒絕 |
| Timeline | `raw` | 原始資料 |

## B. 狀態徽章（GateConsole，來源 `e.state` 大寫）

| 原文 | 繁中 |
|---|---|
| `PENDING` | 待核可 |
| `ACTIVE` | 已生效 |
| `STALE` | 已失效 |
| `SUPERSEDED` | 已取代 |

## C. Placeholder / 提示 / tooltip 修訂

| 元件 | 原文 | 繁中 |
|---|---|---|
| ApprovalDialog（placeholder）| `理由（deny 建議填）` | 理由（拒絕時建議填寫） |
| GateConsole（placeholder）| `理由（reject 必填）` | 理由（退回時必填） |
| GateConsole（degraded 通知）| `journal degraded：核可／駁回暫停，僅供讀取（spec §3.2）` | 核可記錄異常：核可與退回功能已暫停，目前僅供查看 |
| SettingsBar（tooltip，quiesce）| `結束目前 session（quiesce 舊 provider）後開新對話` | 結束目前 session，等待舊 provider 收尾後開新對話 |

## D. 操作結果事件訊息（SettingsBar → UI 事件）

目前會產生 `new ok`、`auth ok` 等英文事件字串，應同步譯中文（實作時定位確切 emit 處，涵蓋 login/logout/terminate/end/new/auth 各結果）：

| 原文（示例）| 繁中 |
|---|---|
| `new ok` | 開新對話成功 |
| `auth ok` | 查詢登入狀態成功 |
| （login/logout/terminate/end 各結果同法映射）| …成功／…失敗 |

## E. Timeline 狀態映射（`summary()` / state_change）

| 來源 | 原文 | 繁中 |
|---|---|---|
| `result`（`e.is_error`）| `ERROR` / `ok` | 失敗 / 完成 |
| codex item `status` | `completed` / `inProgress` / `failed` | 已完成 / 執行中 / 失敗 |
| `state_change`（`狀態 → ${e.state}`）| 後端英文 session 狀態 | **重用 StatusBar 既有的中文狀態對照表**（不要各自硬編一份） |

## F. 保留英文（不譯 / 中英並列）

| 類別 | 字串 | 處置 |
|---|---|---|
| provider 名 | `claude` / `codex` | 保留 |
| approvalPolicy wire 值 | `on-request` / `never` / `untrusted` | **保留**（須與後端 wire 一致，`untrusted` 亦同 `on-request`/`never`） |
| 除錯 | `B1`（probe）、原始錯誤內容 | 保留（原始錯誤保留有助除錯） |
| 開發術語 | `session`、`tokens`、`Gherkin`、`oracle`、事件 `kind` | 保留（如需譯：session→工作階段、tokens→用量，但建議保留） |
| git | `commit` | **保留英文**，避免與「送核」混淆（見上 `Preview commit`/`建立 commit`/`commit 訊息`） |
| 資料契約欄位名 | `approval_id` / `base_commit` / `spec_manifest` | **保留原文**；主畫面中英並列、詳細/稽核畫面直接顯示原文 |

**中英並列（主畫面）建議**：
- `approval_id` → 核可編號（approval_id）
- `base_commit` → 基準 commit（base_commit）
- `spec_manifest` → 規格 manifest（spec_manifest）

詳細 bindings 與稽核畫面**直接顯示原始欄位名**，確保能與 `.workbench/gate.jsonl`／`events.jsonl` 對照。

---

## 待實作時確認
- SettingsBar 的操作結果事件（`new ok`/`auth ok` 等）確切 emit 來源與完整清單。
- StatusBar 既有中文狀態對照表的實際 key/值（Timeline 的 `state_change` 重用它）。
- SettingsBar tooltip `codex approvalPolicy` 是否也要中文化（未在本輪修訂列出）。
