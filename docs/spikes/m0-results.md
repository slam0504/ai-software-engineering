# M0 Spike 結果（2026-08-07 定稿）

> 狀態：**驗收完成（已執行項全 PASS，A0／B0 經 owner 核可豁免）**——Claude 線 A1–A12、
> Codex 線 B1–B6、N1、R1、隔離封裝 smoke 全 PASS。**A0／B0（app 內完整登出→登入循環）為
> 計畫內驗收項、本輪未執行**：owner（使用者）於 2026-08-07 審核時明確接受此殘餘風險並核可
> 豁免，不影響本次 technical go/no-go；兩帳號現處登入態，auth 狀態讀取與官方 login 命令
> 已實測。A11 bare/API-key 對照為計畫明定選測（需成本授權），未執行。
> 建議：**方案 A 定案 GO；Codex 線 GO**（依據見「建議」節）。

## 版本基線

| 項 | 值 |
|---|---|
| claude CLI（exact pin） | 2.1.223 `sha256=350e657428a6d34f7cf71f6738c5ebb6a1952ccb12fc1747f64297e065b1846f` |
| codex CLI（exact pin） | 0.146.1 `sha256=134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477` |
| wails | v2.13.0 |
| go | go1.26.5 darwin/amd64 |
| node（系統前置需求） | v26.6.0 |
| Codex schema | `schemas/codex/`（`generate-json-schema` 產物 275 檔 + SHA256SUMS，與 0.146.1 綁定） |
| claude init capabilities | `interrupt_receipt_v1`、`interrupt_cancel_queued_v1`、`msg_lifecycle_v1`（A1 錄流 init 行；model claude-opus-5） |

## 驗收矩陣

| 項 | 結果 | 證據 | 備註 |
|---|---|---|---|
| 單元測試（contract／claude／codex／proc／approval／recorder／app 層） | PASS | `go test -race ./... -count=1` 全過（**7 packages**、70+ 測試，最終重跑 2026-08-07） | 含 supervisor 孫程序收割、大輸出反壓、真 ctx 取消、MCP E2E、RawParams 全鏈、session-scoped 錄流、Single／WithExclusive 併發、probe 四階段失敗注入；穩定性觀察見「測試穩定性」節 |
| 協定 contract replay（claude fixtures） | PASS | `TestContractReplay`（fixtures 非空 gate 生效） | fixtures 已含真實錄流去敏節錄（`claude-stream-shape.ndjson`、`claude-permission-request.sample.json`，committed）+ 8 份真實錄流本機 replay |
| 協定 contract replay（codex fixtures） | PASS | `TestReplay`（direction envelope、c2s 方法集、s2c 映射） | fixtures 已含真實錄流去敏節錄（`codex-turn-shape.jsonl`，committed）+ 6 份真實錄流本機 replay |
| 建置 gate（vet／test -race／frontend build／wails build） | PASS | 四項實際執行全過（最終重跑 2026-08-07，見「建置與封裝 gate 輸出」） | |
| CLI 進 bundle | DONE | `scripts/bundle-clis.sh` → Resources/tools 873M | 體積大：CLI node_modules 全量；M1 打包決策議題 |
| A0 app 內官方登入 | **未執行（owner 核可豁免，2026-08-07）** | — | 計畫內驗收項；owner 接受殘餘風險，不影響 technical go/no-go。使用者已登入（Max）；官方命令已確認：`claude auth login/logout/status`（fixture claude-auth-help.txt） |
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
| B0 app 內官方登入 | **未執行（owner 核可豁免，2026-08-07）** | 帳號已登入（chatgpt plus）；`account/read` 實測正常 | 計畫內驗收項；owner 接受殘餘風險，不影響 technical go/no-go（同 A0） |
| B1 handshake probe | **PASS**（2026-08-07） | `codex-handshake.jsonl`＝完整雙向 initialize→result→initialized、無後續流量；meta `process_still_running: true`、無 exit_code | `RestartCodexServerRecorded` 經 UI 按鈕執行 |
| B2 登入狀態查詢 | **PASS**（2026-08-06/07） | `AuthStatus("codex")` 回報 chatgpt 帳號、planType | |
| B3 turn 串流 | **PASS**（2026-08-07） | `codex-turn.jsonl`：61 delta、item started/completed、turn/completed；mapping 全走 Task 8 定案表；UI 同一 Transcript 呈現 | 4 個子集外通知升級為 KindSystemOther |
| B4 核可往返 | **PASS**（2026-08-07） | Allow：requestApproval→`{"decision":"accept"}`→`serverRequest/resolved`→b4.txt 存在；Deny：`decline`→resolved→檔案不存在、turn 正常收尾 | 需 `thread/start` 帶 `approvalPolicy:"untrusted"`（預設 policy 自動放行、不觸發 requestApproval——實測發現）；request id 實測為 int；`availableDecisions` 未列 decline 但 server 接受 decline |
| B5 replay | **PASS**（2026-08-07） | 6 份 codex 錄流 + fixtures 全過（c2s 方法集、s2c 映射、direction envelope） | |
| B6 session 持續性 | **PASS**（2026-08-07） | `thread/resume {threadId}` 成功、回答精準引用前回合（b4-deny 被拒）脈絡 | UI resume 傳遞 bug（codex resume 被吞）發現並修復 |
| N1（同一 UI 雙 provider） | **PASS**（2026-08-06/07） | 同一 build 先後跑 claude 與 codex session（8/6 晚間同 app 實例）；Transcript／ApprovalDialog／session:done 全走 contract 事件、無 provider 特判；雙 fixtures glob 非空且全 Valid（replay 測試） | |
| R1（Recorder 失敗可見性） | **PASS**（2026-08-07） | `chmod 500 recordings` → session:done 顯示 `recorder: permission denied`（UI 可見）；恢復權限重跑正常 | |
| 封裝後隔離 smoke | **PASS**（2026-08-07） | .app 複本於 `/tmp/m0smoke.*`、repo `tools/` 藏起、home cwd 啟動：CLIInfo 顯示 toolsDir=bundle 內路徑、雙 CLI 版本自 bundle 讀取、startupError 空；claude A2 級 probe（audit startup 記 tmp tools 路徑、Bash touch→allow→smoke.txt 存在）；codex binary 自 bundle 可執行（版本讀取）+ 操作者確認 turn 完成（未設 recordCase、無錄流留存） | 證據：截圖 + audit + probe 檔 |

## 協定觀察（至今）

- **Claude permission request 真實 schema（A2 實測）**：`arguments: {tool_name: string, input: object, tool_use_id: string}`——typed 化可據此定義；樣本 `testdata/fixtures/claude-permission-request.sample.json`。
- **`rate_limit_event`**：訂閱模式每 session 出現一筆（rate limit 與 overage 狀態），2.1.223 實測新事件型別，decoder 已納入 KindSystemOther。
- **MCP 失敗呈現（A6 實測）**：2.1.223 在 init 以 `mcp_servers[].status:"failed"` 呈現，`mcp_server_errors` 欄位未出現；permission tool 缺失時 CLI 直接 exit 1 + stderr 明確錯誤（fail loud 成立）。
- **SIGTERM exit code（A10 實測）**：143，與文件值一致。
- **使用者環境載入（A11 觀察）**：訂閱模式載入 hooks/plugins/skills（大量 system 事件、SessionEnd hook 失敗訊息入 stderr）、使用者 allow 規則生效（`sleep` 不彈窗）、plan-mode 預設需以 `--settings defaultMode` 覆寫（已實作）。
- **Codex approval policy（B4 實測）**：預設 `thread/start`（未帶 approvalPolicy）下 `touch` 直接自動執行、不發 requestApproval——M0 改為一律送 `approvalPolicy:"untrusted"`；`AskForApproval` enum＝`untrusted | on-request | never`（+granular）。approval request 的 `availableDecisions` 列 `[accept, acceptWithExecpolicyAmendment, cancel]`（無 decline），但回 `decline` server 正常接受並發 `serverRequest/resolved`。request id 實測為整數（string-ID 支援已具備、未在 live 流量出現）。
- **Codex 子集外通知（B3/B6 實測升級）**：`thread/status/changed`、`thread/tokenUsage/updated`、`account/rateLimits/updated`、`mcpServer/startupStatus/updated`、`thread/goal/cleared` 納入 KindSystemOther；皆在 pinned schema ServerNotification 列表內。
- **Codex 線錄流 SHA-256 完整清單（原檔：repo `.workbench/recordings/`，不 commit）**：

  ```
  94114f5368dc5bc4efd03e01d3cbebeaeac55b366372f0741d7972f9c0e8e7a9  codex-handshake.jsonl
  bcfc1c89d3c420c6f9b34d3280fb08fdecf9a9febfdb988f534aeded08001cbb  codex-turn.jsonl
  11ceb77cb48b8e135c416b1395bc417daef40fec50c87f5846554b7977bd31b4  codex-b4.jsonl
  305570efe314859d44168af6a4ce74c0a4f43bdd6bfcd8199b87f55f559a0126  codex-b4d.jsonl
  9826b78ab68915a511b2a5384bd20cdc60cff30b819212cf05a7c398c3cb3486  codex-b6.jsonl
  147bd89f573160684707b8801ab08f9e1d97fd3b0baebf2c14134c88d7ffee41  codex-r1.jsonl
  ```

- **Claude 線錄流 SHA-256 完整清單（原檔：`~/.workbench/recordings/`，不 commit）**：

  ```
  f5b7f19e4c367e4d8facd761c24c038ad71d12e6a422dcab1577e13eb6f51b04  claude-basic.ndjson
  117e0ea5a64d454d84035bcb8833f075c9aa818bc2325b1b3da63096f9a41840  claude-a2.ndjson
  0521ac7c5aef0a0aa2234a1cc36f9784ebefe37f3845938ae64a9b4c5950d216  claude-a3.ndjson
  6458e2083cb7af94258a33c24a7cdcb9a4abeebf5bb6c7e50719067858e9be8a  claude-a4.ndjson
  c3b77597d12fde5241300f305be533eac87ce29387efb9c57f01a34115b9a2b1  claude-a5.ndjson
  f69ab450a69fa07e3447f38644b2ffbd3adb7b4eb42911ccd635522c068e75a0  claude-a6.ndjson
  82c3de4c4b8a388a2e2093a1fca72bd820c598258f66cf7b11b3b212440edbd8  claude-a8.ndjson
  0101f5aa2020753272263bed337970844050e242755706ca4b844544700012ca  claude-a10.ndjson
  ```

- **稽核檔（定稿當日快照 digest；原檔不 commit、含 RawParams 原文）**：

  ```
  7b5d3cde04c0ad88c5ef50b0b7ac1873fce820cc1d16ffbce179e7906367dc4f  ~/.workbench/audit.jsonl（Claude 線 A2–A10 + 隔離 smoke）
  ad45f87432d414c3a78be99656dd4bc300077d0b292f127a48a7654f959042a6  <repo>/.workbench/audit.jsonl（Codex 線 B4 核可）
  ```

- **隔離封裝 smoke 證據落點**：CLIInfo tooltip 截圖由 owner 於審核對話中提供、未歸檔為 repo 檔案；可追溯證據＝`~/.workbench/audit.jsonl` 的 startup 行（`tools_dir=/tmp/m0smoke.I0yE/...`）與 `~/.workbench/probe/smoke.txt`（timestamp 2026-08-07 12:22）。
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

## 建置與封裝 gate 輸出（最終重跑 2026-08-07）

```
go vet ./...                    → OK
go test -race ./... -count=1    → 7 packages all ok（含 app 層測試）
npm run build（frontend）        → ✓ built（chunk size warning：mermaid，M1 處理）
wails build                     → Built build/bin/sdlc-workbench.app
scripts/bundle-clis.sh          → bundled: 873M
codesign --verify --deep --strict → PASS（審核者重跑確認）
```

## 測試穩定性觀察（審核要求記載）

- `TestTerminateKillsProcessGroup`（internal/claude）曾在**與 frontend build 並行執行**的一次
  `go test -race` 中以「kill escalation too slow」失敗（測試斷言 5 秒內收尾，系統高負載下
  escalation 逾時）；單獨重跑立即通過，其後獨立完整重跑 `go test -race ./... -count=1` 亦
  7 packages 全過。判定為**負載敏感的時間斷言**，非產品缺陷；不改描述為「每次全綠」。
  處置：M1／CI 導入時持續監控此測試，必要時放寬時間門檻或隔離為 serial 測試。

## 失敗歸因與轉向評估

全矩陣無驗收 FAIL。過程中發現的問題全數歸因於自寫 code 或環境整合（workspace cwd、node PATH、
UI resume 傳遞、彈窗殘留、plan-mode 覆寫、approvalPolicy 預設），依 Global Constraints 均屬
「修復、不構成轉向理由」，且已全部修復並補測試。**方案 C 轉向條件（Claude CLI bridge 本身
協定／能力缺口）未觸發**；Codex app-server 線無阻礙。

## 建議

- [x] **方案 A 定案（Go + Wails v2 + Vue 3 + CLI stream-json bridge）**——Claude 線 A1–A12
  全 PASS：streaming、權限 MCP 往返（allow/deny/逾時/斷線/載入失敗全路徑 fail closed）、
  resume/cwd 綁定、Terminate 整組收尾、replay 契約全部以真 CLI 驗證成立。
- [x] **Codex 線 GO**——app-server handshake、turn 串流、核可往返（含 serverRequest/resolved）、
  thread/resume、replay 全 PASS；訂閱（Sign in with ChatGPT）路徑實測可用。
- 回饋 app-plan 下一版的修訂點：
  1. M1 需把「多輪互動」做進問答面板（M0 單回合 + resume 的體感限制已實證）。
  2. Codex approvalPolicy 需成為 UI 可見設定（M0 硬編 untrusted；預設 policy 會自動放行）。
  3. CLI ownership 決策輸入：claude 2.1.223 為 native binary、codex 0.146.1 需系統 node
     （app 已做 PATH 解析注入）；bundle 873M 主要是 CLI node_modules，M1 打包需瘦身策略。
  4. 事件雜訊治理：訂閱模式載入使用者環境產生大量 system 事件，M1 時間軸需分層/摺疊。
  5. `permissionMode` 覆寫（defaultMode=default）與 ask 規則的組合已驗證，可作 M1 權限
     設定面的基礎。
