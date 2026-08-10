# SDLC Workbench

<div align="center">

![Milestone](https://img.shields.io/badge/milestone-M1%20MVP-blue.svg)
![Platform](https://img.shields.io/badge/platform-macOS-lightgrey.svg)
![Wails](https://img.shields.io/badge/wails-2.x-DF0000.svg)
![Go](https://img.shields.io/badge/go-1.26+-00ADD8.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)

**A dual-provider AI coding agent cockpit — drive Claude Code and Codex from one desktop app**

在同一個介面裡驅動 **Claude Code CLI** 與 **Codex CLI**：多輪對話、統一的工具核可（approval）流程、
session 狀態即時可視化（SC2 StatusBar）、完整稽核事件流與 wire 錄流。
以 Go + Wails v2 + Vue 3（TypeScript）實作，pinned CLI 版本、契約凍結、逐里程碑驗收。

</div>

---

## Screenshots

<div align="center">

**Claude session** — 多輪對話、事件 Timeline、SC2 StatusBar（累計 tokens 與 cost）
<img src="docs/spikes/evidence/v2-claude-statusbar.png" alt="Claude session" width="800">

**Codex session** — 同一介面、provider 最新 usage（`*` 標示）、長駐 app-server
<img src="docs/spikes/evidence/v2-codex-statusbar.png" alt="Codex session" width="800">

</div>

---

## Features

### 雙 Provider Session
- **Claude Code**（pinned `2.1.223`）— `claude -p` stream-json 多輪子程序：stdin 保持開啟逐輪送訊息、
  自然收尾 exit 0；session id 綁定 workspace，跨 app 重啟可 resume（異 cwd 拒絕）
- **Codex**（pinned `0.146.1`）— 長駐 `codex app-server` JSON-RPC：thread/turn 模型、
  `thread/resume` 引用前文、單一 server 跨 session 重用
- 單一 active session；**New** 會先 quiesce 舊 session（等事件收乾、錄流收尾）再開新

### 工具核可（Approval）
- 兩個 provider 共用同一個 ApprovalDialog，決定與理由全程稽核
- **Claude** — 經 MCP permission-prompt-tool（app 內建 `mcp-approval` 子命令 + unix socket broker）
- **Codex** — 經 app-server `requestApproval`；`approvalPolicy` 可選 `untrusted`（每次核可）/
  `on-request` / `never`（不核可，風險自負）
- 逾時 **fail-closed** 自動 deny，過期彈窗自動收掉（多視窗亦同步 dismiss）

### SC2 StatusBar
單一狀態列同時回答四個問題——「現在哪個任務、卡在哪、哪個 session、花了多少」：

| 欄位 | 內容 |
|---|---|
| 任務 | 使用者標記的 task label |
| 狀態 | reducer 推導：waiting / streaming / tool_running / **awaiting_approval**（高亮）/ done / failed |
| Session | Claude session id 或 Codex thread id |
| Tokens | 累計（`session_total`）或 provider 最新值（`provider_latest`，以 `*`＋tooltip 明示，不謊稱累計） |
| Cost | Claude 累加 USD；Codex 無回報時顯示 `—` |

### Chat 與 Timeline
- **Streaming 逐 token** 渲染＋游標；thinking 內容摺疊於預設收合的 `<details>`
- **Follow-tail** — 上捲即停止自動跟隨，回到底部或送出訊息恢復（BAT 慣例）
- **Tool 卡片** — 工具名＋參數節錄（80-rune 截斷）＋best-effort 狀態（codex `inProgress → completed`）
- **雜訊摺疊** — 連續系統事件自動收合為一列，可展開；每筆事件可看 raw JSON
- Timeline 面板可整個收合，Chat 區域隨之擴大

### 檔案樹與預覽
- Workspace 懶載入樹狀瀏覽；**canonical path 邊界**——symlink 逸出 workspace 一律拒絕、單檔 1MB 上限
- Markdown 預覽：DOMPurify 消毒＋mermaid `strict` 渲染（```mermaid``` 區塊→SVG）
- `.mmd` 檔案存檔 1 秒內自動重渲染（fsnotify 監看）

### 稽核與錄流
- 所有事件以 **Envelope v1** 落 `events.jsonl`：ULID 嚴格單調、user message 保證先於該輪
  provider 事件（submission coordinator）、稽核寫入失敗 fail-loud 直達 UI
- 可選 per-session **wire 錄流**（Claude ndjson / Codex jsonl）＋ meta
 （argv、cwd、exit code、stderr tail）；收尾由 RecordingLease 保證恰一次

### 官方登入
- App 不收密碼、不保管 token
- Claude：開系統終端機跑 `claude auth login`＋背景輪詢登入狀態
- Codex：app-server `account/login/start` 開瀏覽器 OAuth，可取消

---

## Quick Start

### Build from Source

需求：macOS、Go 1.26+、Node.js、[Wails CLI v2](https://wails.io/docs/gettingstarted/installation)

```bash
git clone https://github.com/slam0504/ai-software-engineering.git
cd ai-software-engineering

# 開發模式（原生視窗 + http://localhost:34115 browser devserver）
wails dev

# 產出 .app
wails build                  # → build/bin/sdlc-workbench.app
./scripts/bundle-clis.sh     # 把 pinned CLIs 裝進 .app 的 Resources/tools/
```

> **CLI 版本 pin**：claude `2.1.223`、codex `0.146.1`。wire 行為以此版本實測凍結，
> 勿隨意升版；升版需重跑 live probe 與驗收矩陣。Codex CLI 是 node script，
> 執行期需要 node（GUI 啟動時會自動探測 `/usr/local/bin`、`/opt/homebrew/bin`）。

### 測試

```bash
go vet ./...
go test -race ./... -count=1     # 全 package，含 production-path barrier 測試
npm --prefix frontend run test   # vitest（store / scroll / sanitizer / ChatPanel）
npm --prefix frontend run build  # vue-tsc typecheck + vite build
```

---

## Architecture

參考 ports & adapters（hexagonal）慣例，核心邏輯與 wire／UI 隔離：

```
frontend/                Vue 3 + TS + Pinia（Wails webview）
  src/stores/session.ts    唯一事件入口 apply(envelope)：chat/timeline/totals/state 路由
  src/components/          ChatPanel / Timeline / StatusBar / FileTree /
                           PreviewPane / SettingsBar / ApprovalDialog
app.go                   薄綁定層：workspace/CLI 解析、Wails 事件出口、provider 接線
internal/
  contract/              Envelope v1（凍結契約）：ULID、Wrap、state reducer
  appcore/               可測核心：Manager（單一序列化 emit 入口）、submission coordinator、
                         session lifecycle 狀態機、RecordingLease、Pump/CloseSequence
  ports/                 consumer-owned 介面（Turns、Exit）
  claude/                Claude CLI adapter：stream-json decode、多輪 session、resume registry
  codex/                 Codex app-server adapter：JSON-RPC conn、ThreadRunner、錄流 tee
  proc/                  子程序 supervisor（process group、TERM→KILL、stderr tail）
  approval/              Claude 核可 broker（unix socket、逾時 fail-closed）
  recorder/              wire 錄流（ndjson/jsonl + meta）
```

關鍵設計約束（詳見 [`docs/architecture/sdlc-workbench-m1-plan.md`](docs/architecture/sdlc-workbench-m1-plan.md)）：

- **單一序列化事件入口** — 所有 provider 事件經 `appcore.Manager.Emit`（同一 mutex 完成
  wrap→totals→sink→emit→state_change），輸出 event_id 嚴格遞增，稽核失敗 fail-loud
- **Submission coordinator** — 送訊息採 Begin→provider 呼叫→Accept/Reject 三段 ownership 交易；
  provider 事件在 Accept 前入 queue，保證 UI 與稽核都是 user message 先行
- **Usage 語意雙軌** — Claude per-turn 累加（`session_total`）、Codex snapshot 覆寫
 （`provider_latest`）；UI 以 `*` 區分，不把最新值謊稱累計
- **收尾 ownership** — 錄流收尾多來源併發時由 `RecordingLease` 保證恰一次；
  Claude 收尾走 `CloseSequence`（關 stdin→quiesce→必要時 terminate→exit 證據），
  未知結局不偽裝 exit 0

### Tech Stack

| 層 | 技術 |
|---|---|
| Host | Go 1.26、Wails v2 |
| Frontend | Vue 3、TypeScript、Pinia、Vite |
| 渲染 | marked + DOMPurify（Markdown）、mermaid（圖表，strict） |
| 測試 | `go test -race`、vitest + @vue/test-utils、wire replay fixtures |
| Agent CLIs | claude 2.1.223（native binary）、codex 0.146.1（node script） |

---

## Configuration

### 環境變數

| 變數 | 用途 |
|---|---|
| `WORKBENCH_WORKSPACE` | 覆寫 workspace 根目錄（預設：可寫的 cwd → home） |
| `WORKBENCH_TOOLS_DIR` | 覆寫 CLI tools 目錄（預設：bundle Resources/tools → repo tools/） |
| `WORKBENCH_APPROVAL_TIMEOUT` | 核可逾時（Go duration，如 `5s`；逾時自動 deny） |
| `WORKBENCH_MCP_COMMAND_OVERRIDE` | 測試用：覆寫 MCP approval server 指令 |

### 執行期狀態（workspace 的 `.workbench/`）

| 檔案 | 內容 |
|---|---|
| `events.jsonl` | Envelope v1 稽核事件流（UI 所見即所錄） |
| `audit.jsonl` | App 層稽核（啟動資訊、核可決定、登入事件） |
| `recordings/` | wire 錄流與 meta |
| `sessions.json` | Claude resume registry（session id ↔ cwd 綁定） |

---

## Roadmap

| 里程碑 | 狀態 | 內容 |
|---|---|---|
| **M0** spike | ✅ merged | 雙 CLI wire 打通、核可 E2E、錄流／replay、驗收矩陣 A/B/N/R（[結果](docs/spikes/m0-results.md)） |
| **M1** MVP | ✅ merged | Envelope v1 契約、序列化 Manager＋coordinator、多輪雙 provider、三欄 UI、驗收矩陣 V0–V6（[結果](docs/spikes/m1-results.md)） |
| **M2** | 規劃中 | Provider 切換時對話視窗跟著切、雙 session 並存；`turn/steer`；bundle 瘦身 |

每個里程碑的執行計畫經外部審核後凍結於 [`docs/architecture/`](docs/architecture/)（`SHA256SUMS` 可驗證），
實作偏差與殘餘風險記錄於對應的 results 文件。

---

## License

[MIT](LICENSE) © 2026 slam0504

## Acknowledgments

- UI 互動慣例參考 [Better Agent Terminal](https://github.com/tony1223/better-agent-terminal)（MIT）
  與 VS Code 的佈局慣例（sidebar / editor / panel / status bar）
- [Wails](https://wails.io/) — Go 桌面應用框架
