# M0 Spike 結果（2026-08-06，進行中）

> 狀態：**Task 1–10 完成、Task 12 自動化 gate 全過；Task 11 驗收矩陣（A0–A12／B0–B6／N1／R1）與封裝後隔離 smoke 待使用者本人操作**（真帳號登入與 UI 互動無法由 agent 代行）。本報告依實際執行證據撰寫，未執行項一律標 PENDING，不使用「應該可以」措辭。

## 版本基線

| 項 | 值 |
|---|---|
| claude CLI（exact pin） | 2.1.223 `sha256=350e657428a6d34f7cf71f6738c5ebb6a1952ccb12fc1747f64297e065b1846f` |
| codex CLI（exact pin） | 0.146.1 `sha256=134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477` |
| wails | v2.13.0 |
| go | go1.26.5 darwin/amd64 |
| node（系統前置需求） | v26.6.0 |
| Codex schema | `schemas/codex/`（`generate-json-schema` 產物 275 檔 + SHA256SUMS，與 0.146.1 綁定） |
| claude init capabilities | PENDING（A1 錄流後抄入） |

## 驗收矩陣

| 項 | 結果 | 證據 | 備註 |
|---|---|---|---|
| 單元測試（contract／claude／codex／proc／approval／recorder） | PASS | `go test -race ./...` 全綠（6 package、60+ 測試） | 含 supervisor 孫程序收割、大輸出反壓、真 ctx 取消、MCP E2E、RawParams 全鏈、session-scoped 錄流、Single／WithExclusive 併發、probe 四階段失敗注入 |
| 協定 contract replay（claude fixtures） | PASS | `TestContractReplay`（fixtures 非空 gate 生效） | 目前僅 sample fixture；A1 真實錄流去敏後增補 |
| 協定 contract replay（codex fixtures） | PASS | `TestReplay`（direction envelope、c2s 方法集、s2c 映射） | 同上，B3 錄流後增補 |
| 建置 gate（vet／test -race／frontend build／wails build） | PASS | 四項實際執行全過（2026-08-06） | |
| CLI 進 bundle | DONE | `scripts/bundle-clis.sh` → Resources/tools 873M | 體積大：CLI node_modules 全量；M1 打包決策議題 |
| A0–A12（Claude 線） | PENDING | — | 需使用者：app 內登入、真 CLI turn、UI 核可操作 |
| B0–B6（Codex 線） | PENDING | — | 需使用者：Sign in with ChatGPT、真 app-server 回合 |
| N1（同一 UI 雙 provider） | PENDING（程式面已就位） | UI 無 provider 特判、事件全走 contract.Event；fixtures 掃描在 replay 測試內 | live 驗證待 A1+B3 |
| R1（Recorder 失敗可見性） | PENDING | 機制已有單元測試（錯誤 latch／CloseWith 傳播／session:done recorderError） | chmod 500 實測待 UI smoke |
| 封裝後隔離 smoke | PENDING | bundle 已產出 | 需 GUI 互動 |

## 協定觀察（至今）

- **Claude permission request 真實 schema**：PENDING（A2 錄流），typed 化建議留待 audit 的 RawParams。
- **Codex schema 覆核**：pinned 0.146.1 的 item union 為 **18 型**（官方文件頁面載 14 型；新增 `hookPrompt`／`subAgentActivity`／`sleep`／`imageGeneration`，`collabToolCall` 實名 `collabAgentToolCall`）——證實計畫「子集原則」；未支援型別落 KindUnknown。方法集與計畫定名全部吻合（`thread/start`、`turn/start` input=item 陣列、`result.thread.id`、`item/commandExecution/requestApproval`、`account/login/start` 等）。
- **Claude 官方 auth 命令（pinned 2.1.223 實測）**：`claude auth login`（互動式，`--claudeai` 為預設）／`auth logout`／`auth status`（JSON 輸出，含 loggedIn 欄位）——fixture `testdata/fixtures/claude-auth-help.txt`。app 的 fallback 輪詢即以 `auth status` JSON 實作。

## 實作決策與偏差記錄

1. **Repo 位置**：使用者指示改放 `ai-software-engineering` repo（原計畫 `~/playground/sdlc-workbench`）；模組名維持 `github.com/slam0504/sdlc-workbench`。branch：`m0-spike`。
2. **MCP server 實作採 `modelcontextprotocol/go-sdk` v1.7.0**（計畫首選路徑）：`IOTransport{Reader,Writer}` 支援自訂 stdio、`CallToolParamsRaw.Arguments` 保留原文；Task 7 全部 E2E 測試（allow／deny-via-broker／broker-down／RawParams 全鏈）以 raw JSON-RPC frame 直測通過，未落手寫 fallback。
3. **計畫內嵌 code 的 bug 修正（Wails/自寫 bug 不構成轉向）**：
   - `bufio.Scanner.Buffer` 的 max token = max(maxLine, cap(buf))，計畫代碼初始 cap 64KB 蓋掉小的 `MaxLineBytes` → 修為 `initCap = min(64KB, maxLine)`（`internal/claude/session.go`），`TestScannerErrorSurfaced` 因此轉綠。
   - wails vue-ts 模板 `App.vue` 的 `</script>` 與 import 同行 → vue-tsc 2.2.12 解析失敗，拆行修復。
4. **probe start 失敗的 meta 錯誤欄位**：`recorder.Meta` 無一般 error 欄位，start 失敗訊息記入 `StderrTail`（`"probe start failed: …"`）；`RecorderError` 保留給 recorder 自身錯誤。
5. **Codex 未知通知的分流**：M0 已認得通知集合定義於 `methods.go` `ServerNotifications`；集合外通知走 `OnUnknown`（raw 保留 → UI unknown 卡片），集合內未特判者映 KindSystemOther。

## 訂閱與合規

- 兩 provider 均僅喚起官方 login flow（Codex：app-server `account/login/start {"type":"chatgpt"}`；Claude：系統終端機執行 `claude auth login` + 狀態輪詢）；**app 程式碼不接收密碼、不讀寫 token**（credential 由官方 CLI 自有機制保管）。
- Claude 訂閱路徑：**目標自用型態的技術驗證進行中**；Anthropic 規範對個人 wrapper 的適用性未獲官方確認，列為已知風險（不以「合規 ✔」表述）；發布 gating 為條件款（無發布計畫）。

## 建置與封裝 gate 輸出（2026-08-06）

```
go vet ./...          → OK
go test -race ./...   → 6 packages all ok
npm run build         → ✓ built in 8.99s（chunk size warning：mermaid，M1 處理）
wails build           → Built build/bin/sdlc-workbench.app
scripts/bundle-clis.sh → bundled: 873M
```

## 失敗歸因與轉向評估

至今無驗收 FAIL。方案 C 轉向條件（Claude CLI bridge 本身缺口）未觸發；Codex 線無阻礙。

## 待辦（需使用者）

1. `wails dev`（或開啟 build 出的 app）→ 依 Task 11 逐項執行 A0–A12／B0–B6／N1／R1，錄流落 `.workbench/recordings/`。
2. 去敏錄流節錄補進 `testdata/fixtures/`（`claude-stream-shape.ndjson`、`claude-permission-request.sample.json`、`codex-turn-shape.jsonl`）。
3. Task 12 Step 3 隔離 smoke（.app 複製至暫存目錄、`tools/` 藏起、驗證解析路徑在 bundle 內）。
4. 依模板補完本報告 → 方案 A 定案與 Codex go／no-go 勾選。
