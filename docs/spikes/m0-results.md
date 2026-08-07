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
| A0 app 內官方登入 | PENDING（選擇性） | — | 使用者已登入（Max）；完整登出→登入循環未演練。官方命令已確認：`claude auth login/logout/status`（fixture claude-auth-help.txt） |
| A1 串流 | **PASS**（2026-08-06） | `claude-basic.ndjson`（37KB）+ meta；text/thinking 逐 token 顯示 | 訂閱 login 模式；使用者環境（hooks/plugins）載入產生大量 system 事件 |
| A2 allow + contract probe | **PASS**（2026-08-06） | `probe/a2.txt` 存在；audit REQ Bash + DEC allow（updatedInput echo）；RawParams schema：`{name, arguments:{tool_name, input, tool_use_id}}` | fixture `claude-permission-request.sample.json` |
| A3 deny | **PASS**（2026-08-07） | `a3.txt` 不存在；audit DEC deny + 理由「A3 deny 測試」；turn 正常收尾 | |
| A4 逾時 fail closed | **PASS**（2026-08-07） | `WORKBENCH_APPROVAL_TIMEOUT=5s`：audit TIMEOUT + DEC deny（fail closed）；`a4.txt` 不存在 | 逾時後 UI 彈窗殘留 → 已修（approval:dismiss） |
| A5 broker 斷線 | **PASS**（2026-08-07） | `rm approval.sock` 後核可自動 deny（錄流含 4 處 fail closed 訊息）；`a5.txt` 不存在；不 hang | |
| A6 MCP 載入失敗 | **PASS**（2026-08-07） | init `mcp_servers:[{workbench, status:"failed"}]`；CLI exit 1、stderr 明確報 permission tool not found | 2.1.223 以 status:"failed" 呈現；`mcp_server_errors` 欄位未出現 |
| A7 decoder 韌性 | **PASS** | TestDecode + synthetic malformed 分類正確 | |
| A8 resume 與 cwd | **PASS**（2026-08-07） | 同 cwd resume：session_id 相同、正確引用 a5 前文；`/private/tmp` 啟動 resume → `resume refused` | |
| A9 replay | **PASS**（2026-08-07） | 8 份真實錄流 + fixtures 全過、零 malformed；`rate_limit_event` 升級 decoder 為 KindSystemOther | 錄流 sha256 見下 |
| A10 Terminate | **PASS**（2026-08-07） | sleep 60 執行中 Terminate → 5s 內收尾、**exit 143**（與文件值一致）、孫程序整組收掉 | |
| A11 訂閱模式主測 | **PASS（主路徑）** | A1–A10 全在訂閱 login 模式實測；使用者環境載入行為（hooks、plan-mode 預設、allow 規則）錄流在案 | bare + API-key 對照為選測，未執行（需成本授權） |
| A12 版本與 capabilities | **PASS** | capabilities：`interrupt_receipt_v1`、`interrupt_cancel_queued_v1`、`msg_lifecycle_v1`；model claude-opus-5；cli 2.1.223 | 自 claude-basic init 行 |
| B0 app 內官方登入 | PENDING（選擇性） | 帳號已登入（chatgpt plus）；`account/read` 實測正常 | 完整登出→登入循環未演練（同 A0） |
| B1 handshake probe | **PASS**（2026-08-07） | `codex-handshake.jsonl`＝完整雙向 initialize→result→initialized、無後續流量；meta `process_still_running: true`、無 exit_code | `RestartCodexServerRecorded` 經 UI 按鈕執行 |
| B2 登入狀態查詢 | **PASS**（2026-08-06/07） | `AuthStatus("codex")` 回報 chatgpt 帳號、planType | |
| B3 turn 串流 | **PASS**（2026-08-07） | `codex-turn.jsonl`：61 delta、item started/completed、turn/completed；mapping 全走 Task 8 定案表；UI 同一 Transcript 呈現 | 4 個子集外通知升級為 KindSystemOther |
| B4 核可往返 | **PASS**（2026-08-07） | Allow：requestApproval→`{"decision":"accept"}`→`serverRequest/resolved`→b4.txt 存在；Deny：`decline`→resolved→檔案不存在、turn 正常收尾 | 需 `thread/start` 帶 `approvalPolicy:"untrusted"`（預設 policy 自動放行、不觸發 requestApproval——實測發現）；request id 實測為 int；`availableDecisions` 未列 decline 但 server 接受 decline |
| B5 replay | **PASS**（2026-08-07） | 6 份 codex 錄流 + fixtures 全過（c2s 方法集、s2c 映射、direction envelope） | |
| B6 session 持續性 | **PASS**（2026-08-07） | `thread/resume {threadId}` 成功、回答精準引用前回合（b4-deny 被拒）脈絡 | UI resume 傳遞 bug（codex resume 被吞）發現並修復 |
| N1（同一 UI 雙 provider） | **PASS**（2026-08-06/07） | 同一 build 先後跑 claude 與 codex session（8/6 晚間同 app 實例）；Transcript／ApprovalDialog／session:done 全走 contract 事件、無 provider 特判；雙 fixtures glob 非空且全 Valid（replay 測試） | |
| R1（Recorder 失敗可見性） | **PASS**（2026-08-07） | `chmod 500 recordings` → session:done 顯示 `recorder: permission denied`（UI 可見）；恢復權限重跑正常 | |
| 封裝後隔離 smoke | PENDING | bundle 已產出 | 需 GUI 互動 |

## 協定觀察（至今）

- **Claude permission request 真實 schema（A2 實測）**：`arguments: {tool_name: string, input: object, tool_use_id: string}`——typed 化可據此定義；樣本 `testdata/fixtures/claude-permission-request.sample.json`。
- **`rate_limit_event`**：訂閱模式每 session 出現一筆（rate limit 與 overage 狀態），2.1.223 實測新事件型別，decoder 已納入 KindSystemOther。
- **MCP 失敗呈現（A6 實測）**：2.1.223 在 init 以 `mcp_servers[].status:"failed"` 呈現，`mcp_server_errors` 欄位未出現；permission tool 缺失時 CLI 直接 exit 1 + stderr 明確錯誤（fail loud 成立）。
- **SIGTERM exit code（A10 實測）**：143，與文件值一致。
- **使用者環境載入（A11 觀察）**：訂閱模式載入 hooks/plugins/skills（大量 system 事件、SessionEnd hook 失敗訊息入 stderr）、使用者 allow 規則生效（`sleep` 不彈窗）、plan-mode 預設需以 `--settings defaultMode` 覆寫（已實作）。
- **Codex approval policy（B4 實測）**：預設 `thread/start`（未帶 approvalPolicy）下 `touch` 直接自動執行、不發 requestApproval——M0 改為一律送 `approvalPolicy:"untrusted"`；`AskForApproval` enum＝`untrusted | on-request | never`（+granular）。approval request 的 `availableDecisions` 列 `[accept, acceptWithExecpolicyAmendment, cancel]`（無 decline），但回 `decline` server 正常接受並發 `serverRequest/resolved`。request id 實測為整數（string-ID 支援已具備、未在 live 流量出現）。
- **Codex 子集外通知（B3/B6 實測升級）**：`thread/status/changed`、`thread/tokenUsage/updated`、`account/rateLimits/updated`、`mcpServer/startupStatus/updated`、`thread/goal/cleared` 納入 KindSystemOther；皆在 pinned schema ServerNotification 列表內。
- **Codex 線錄流 sha256（repo `.workbench/recordings/`，不 commit）**：handshake `94114f53…`、turn `bcfc1c89…`、b4 `11ceb77c…`、b4d `305570ef…`、b6 `9826b78a…`、r1 `147bd89f…`
- **Claude 線錄流 sha256（原檔留 `~/.workbench/recordings/`，不 commit）**：
  - claude-basic `f5b7f19e…51b04`、claude-a2 `117e0ea5…41840`、claude-a3 `0521ac7c…50d216`、claude-a4 `6458e208…9be8a`、claude-a5 `c3b77597…b9a2b1`、claude-a6 `f69ab450…8e75a0`、claude-a8 `82c3de4c…0edbd8`、claude-a10 `0101f5aa…0012ca`
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

## Review 修正記錄（2026-08-06 第一輪 code review，3 P1 + 2 P2）

1. **P1 turn/interrupt 缺 turnId（已修）**：schema `TurnInterruptParams` 必填 `threadId`+`turnId`；app 現於 `turn/started` 通知追蹤 `turn.id`（`noteTurnStarted`）、`turn/completed` 清除；`TerminateSession(codex)` 由 `codexInterruptParams()` 組參數，無 active turn 即拒絕。迴歸測試：`TestCodexInterruptParamsLifecycle`／`TestParseTurnStarted`（app 層）。
2. **P1 B6 thread/resume 無 app 路徑（已修）**：`startCodex` 接受 resume（`thread/resume {threadId}`，response 同樣讀 `result.thread.id`）；UI resume 欄位雙 provider 皆顯示。live 行為仍以 B6 實測為準。
3. **P1 RequestID 只支援 int（已修）**：`Frame.ID` 改為保留原文的 `RequestID`（string | int64，`RequestId.json`）；pending map 改原文鍵；server request 回覆 echo 原始 ID。迴歸測試：`TestApprovalStringRequestIDRoundTrip`（含 wire 原文斷言）。實際 app-server 是否用 string ID 待 B4 live 驗證。
4. **P2 Claude 完成訊息只剩 placeholder（已修）**：decoder 對 assistant/user 訊息串接 `content[].text` 進 `Event.Text`；測試 `TestDecodeMessageKeepsText`。
5. **P2 codex login cancel 未實作（已修）**：新增 `CancelLogin` 綁定（`account/login/cancel {loginId}`）與 UI 按鈕；成功後清 loginId 並發 `auth:status`。

修正後 gate 重跑：`go vet`、`go test -race ./...`（7 packages，含新 app 層測試）、`wails build`、`bundle-clis.sh` 全過。

## 探索性 smoke 觀察（2026-08-06，封裝 app、Finder 啟動；非正式 gate 驗收）

- **Claude 單回合成功**：init → streaming → message → result（exit 0、cost $0.2375 顯示正常）；訂閱 login 模式下大量 `system_other` 事件（使用者環境 hooks／skills 載入——A11 的實測材料）；出現 1 筆 `unknown` 事件（正式 A1 錄流時抄 raw 進 allow-unknown 判斷）。
- **Workspace bug（已修）**：Finder 啟動時 cwd=`/`，`.workbench` 落根目錄不可寫 → approval socket bind 失敗。修為 env → 可寫 cwd → home 的 fallback 鏈，UI 顯示解析來源與 startup 錯誤。
- **node 前置需求修正**：pinned **claude 2.1.223 為 native Mach-O binary（不需 node）**；**codex 0.146.1 的進入點是 node script（需 node）**。GUI app 不繼承 shell PATH → app 啟動時解析 node（LookPath → /usr/local/bin → /opt/homebrew/bin）並注入所有子程序 PATH。VERSIONS.md 的「node 為系統前置需求」修正為僅 Codex 線需要。
- **UX**：M0 單回合設計（送完 prompt 即關 stdin）；session 結束後 UI 自動把 session/thread id 帶入 resume 欄，續問即 A8 的 resume 路徑。

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
