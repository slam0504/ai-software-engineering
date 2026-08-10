# sdlc-workbench

macOS 桌面版雙 provider AI coding agent cockpit：在同一個介面裡驅動 **Claude Code CLI** 與 **Codex CLI**，
提供多輪對話、工具核可（approval）、session 狀態即時可視化、稽核事件流與 wire 錄流。
以 Go + Wails v2 + Vue 3（TypeScript）實作。

## 目前狀態

| 里程碑 | 狀態 | 證據 |
|---|---|---|
| M0 spike | 完成（merged `05415b9`） | `docs/spikes/m0-results.md`（A/B/N/R 驗收矩陣） |
| M1 MVP | 完成（merged `bd40cc3`） | `docs/spikes/m1-results.md`（V0–V6 驗收矩陣＋live 證據） |
| M2 | 規劃中 | backlog：provider 切換時對話視窗跟著切、雙 session 並存 |

執行計畫快照（審核通過版本）在 `docs/architecture/`；`SHA256SUMS` 可驗證與外部審核版本一致。

## 功能總覽

- **雙 provider session**：Claude（`claude -p` stream-json 多輪子程序）與 Codex（長駐 `codex app-server`
  JSON-RPC，thread/turn 模型）。同一時間一個 active session；`New` 會先收尾舊 session 再開新。
- **多輪對話**：同 session 保留前後文；Claude 經 stdin 逐輪送 user message、Codex 經 `turn/start`。
  支援 resume（Claude session id 綁定 cwd、Codex `thread/resume`）。
- **工具核可**：兩個 provider 的工具權限請求走同一個 ApprovalDialog——
  Claude 經 MCP permission-prompt-tool（app 內建 `mcp-approval` 子命令 + unix socket broker）、
  Codex 經 app-server 的 `requestApproval` server request。逾時 fail-closed 自動 deny。
  Codex 另可選 `approvalPolicy`（untrusted／on-request／never）。
- **SC2 StatusBar**：單一狀態列同時回答「現在哪個任務、卡在哪、哪個 session、花了多少」——
  任務標籤、reducer 推導狀態（waiting／streaming／tool_running／awaiting_approval…）、
  session id、tokens（累計或 provider 最新值，語意以 `*`＋tooltip 明示）、cost。
- **Timeline**：全事件流（tool 卡片含工具名＋參數節錄＋best-effort 狀態；連續系統雜訊自動摺疊；
  每筆可展開 raw JSON）。
- **檔案樹＋預覽**：workspace 瀏覽（canonical path 邊界、symlink 逸出拒絕、1MB 上限）、
  Markdown 預覽（DOMPurify 消毒＋mermaid strict 渲染）、`.mmd` 檔案存檔即時重渲染。
- **稽核與錄流**：所有事件以 Envelope v1 落 `events.jsonl`（ULID 單調、user message 先於該輪
  provider 事件）；可選 per-session wire 錄流（含 meta：argv／cwd／exit code／stderr tail）。
- **官方登入**：app 不收密碼、不保管 token——Claude 開系統終端機跑 `claude auth login`、
  Codex 走 app-server 的 `account/login/start`（瀏覽器 OAuth）。

## 架構

參考 ports & adapters（hexagonal）慣例，核心邏輯與 wire／UI 隔離：

```
frontend/                Vue 3 + TS + Pinia（Wails webview）
  src/stores/session.ts    唯一事件入口 apply(envelope)：chat/timeline/totals/state 路由
  src/components/          ChatPanel（follow-tail）/ Timeline / StatusBar /
                           FileTree / PreviewPane / SettingsBar / ApprovalDialog
app.go                   薄綁定層：workspace/CLI 解析、Wails 事件出口、provider 接線
internal/
  contract/              Envelope v1（凍結契約）：ULID、Wrap、state reducer
  appcore/               可測核心：Manager（單一序列化 emit 入口、usage 語意、fail-loud 稽核）、
                         submission coordinator（ownership token、user-first 順序保證）、
                         session lifecycle 狀態機（idle/starting/active/ending + token）、
                         RecordingLease（收尾恰一次）、Pump/CloseSequence/EndSessionFlow
  ports/                 consumer-owned 介面（Turns、Exit）
  claude/                Claude CLI adapter：stream-json decode、多輪 session、registry
  codex/                 Codex app-server adapter：JSON-RPC conn、ThreadRunner
                         （early-completed latch）、handshake probe、錄流 tee
  proc/                  子程序 supervisor（process group、TERM→KILL、stderr tail）
  approval/              Claude 核可 broker（unix socket、逾時 fail-closed）
  recorder/              wire 錄流（ndjson/jsonl + meta）
cmd/probe-multiturn/     Claude 多輪行為 live probe（M1-T4 判定工具）
```

幾個關鍵設計約束（詳見 `docs/architecture/sdlc-workbench-m1-plan.md`）：

- **單一序列化事件入口**：所有 provider 事件經 `appcore.Manager.Emit`（同一 mutex 完成
  wrap→totals→sink→emit→state_change），輸出 event_id 嚴格遞增，稽核失敗 fail-loud 不無聲丟。
- **Submission coordinator**：送訊息採 Begin→provider 呼叫→Accept/Reject 三段交易；
  provider 事件在 Accept 前入 queue，保證 UI 與稽核都是 user message 先行。
- **Usage 語意雙軌**：Claude result usage 為 per-turn（累加成 `session_total`）、
  Codex tokenUsage 為 snapshot（`provider_latest` 覆寫）——UI 以 `*` 標示，不把最新值謊稱累計。
- **收尾 ownership**：錄流收尾（多來源併發）由 `RecordingLease` 保證恰一次；Claude 收尾走
  `CloseSequence`（關 stdin→quiesce→必要時 terminate→exit 證據），未知結局不偽裝 exit 0。

## 開發環境

- macOS（Wails v2 桌面 target）
- Go 1.26+
- Node.js（前端 build；**Codex CLI 本身是 node script**，執行期也需要）
- Wails CLI v2（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）

Pinned CLI 版本（M1 基線，勿隨意升版——wire 行為以此版本實測凍結）：

| CLI | 版本 |
|---|---|
| claude | 2.1.223 |
| codex | 0.146.1 |

CLI 由 `scripts/bundle-clis.sh` 裝進 `.app` 的 `Contents/Resources/tools/`；
開發模式 fallback 讀 repo 的 `tools/`，也可用 `WORKBENCH_TOOLS_DIR` 覆寫。

## 常用指令

```bash
# 開發（原生視窗 + http://localhost:34115 browser devserver）
wails dev

# 測試
go vet ./...
go test -race ./... -count=1
npm --prefix frontend run test        # vitest

# 建置
npm --prefix frontend run build       # vue-tsc typecheck + vite build
wails build                           # 產出 build/bin/sdlc-workbench.app
./scripts/bundle-clis.sh              # 把 pinned CLIs 裝進 .app
```

## 執行期環境變數

| 變數 | 用途 |
|---|---|
| `WORKBENCH_WORKSPACE` | 覆寫 workspace 根目錄（預設：可寫的 cwd → home） |
| `WORKBENCH_TOOLS_DIR` | 覆寫 CLI tools 目錄 |
| `WORKBENCH_APPROVAL_TIMEOUT` | 核可逾時（Go duration，如 `5s`；逾時自動 deny） |
| `WORKBENCH_MCP_COMMAND_OVERRIDE` | 測試用：覆寫 MCP approval server 指令 |

執行期狀態落在 workspace 的 `.workbench/`：`events.jsonl`（稽核事件流）、`audit.jsonl`
（app 層稽核）、`recordings/`（wire 錄流）、`sessions.json`（Claude resume registry）。

## 文件

- `docs/architecture/sdlc-workbench-m1-plan.md` — M1 執行計畫（v13 審核通過版，唯一執行依據）
- `docs/architecture/sdlc-workbench-m0-plan.md`、`sdlc-workbench-app-plan.md` — M0 計畫與整體產品計畫快照
- `docs/spikes/m0-results.md`、`docs/spikes/m1-results.md` — 驗收證據（含偏差記錄與殘餘風險）
