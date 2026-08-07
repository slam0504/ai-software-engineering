# sdlc-workbench M0 技術 spike 實作計畫

> 版本：v1.10（2026-08-06）
> 狀態：**已核可（第十輪 plan gate APPROVED，無 findings）**——核可綁定 v1.9 快照 `6b3c4331ae462eda55ac5a92bafa23c869b1be4659a700c0e8e8aa3095c03dc6`；本版僅狀態標記，內容未變。**M0 coding NO-GO 解除，可自 Task 1 開始**；Go／-race／真 CLI 驗收仍依各 Task gate 實際執行。
> 上游文件：`sdlc-workbench-app-plan.md` v1.11（§5.5 provider 與帳號原則、§7 M0 定義驗收範圍）
> 執行 harness 說明：本計畫可由 Claude Code executor（可用 superpowers:executing-plans 或 subagent-driven-development skill）或其他 agent／人工執行；**非 Claude 環境忽略 skill 指名，直接依各 task 的 checkbox 步驟與驗證執行**。
> 自足性聲明（v1.2）：本文件不引用任何歷史版本內容；每個 task 的程式、測試與驗證步驟完整在本文件內。

**Goal**：驗證方案 A（Go + Wails v2 + Vue 3）之上的**雙 provider**可行性——**Claude 線**（Claude Code CLI headless stream-json bridge）與 **Codex 線**（`codex app-server` JSON-RPC）——共用 provider-neutral event contract 與同一套 UI；訂閱帳號一律走官方 login flow。產出驗收矩陣證據與 spike 報告，供方案 A 定案與各 provider go / no-go 決策。

**Architecture**：Wails v2 桌面 app（Go host + Vue 3 webview）。`internal/contract` 定義 provider-neutral 事件 envelope；`internal/claude` 把 CLI stream-json 轉為 contract 事件，權限經 `mcp-approval` 子命令（stdio MCP server）→ unix socket → broker → UI；`internal/codex` 以 JSON-RPC 2.0 驅動 `codex app-server`（長駐子程序），事件通知與核可請求映射到同一 contract 與同一 ApprovalDialog。兩個 CLI 都是 repo 內管理的 pinned binary。

**Tech Stack**：Go ≥ 1.22、Wails v2（不用 v3 beta）、Vue 3 + TypeScript + Vite（`vue-ts` template）、mermaid（npm）、`github.com/modelcontextprotocol/go-sdk`（MCP stdio server；整合遇阻改手寫最小 JSON-RPC 2.0，決策記入 m0-results——**兩種做法都必須通過 Task 7 的 E2E 測試**）、`github.com/fsnotify/fsnotify`、`@anthropic-ai/claude-code`（exact pin，≥ 2.1.219）、`@openai/codex`（exact pin）。

## Global Constraints

- **雙 CLI 都必須真正 pin**：`tools/claude-cli/` 與 `tools/codex-cli/` 各以 package.json 鎖 exact 版本，binary 路徑 `tools/<name>/node_modules/.bin/{claude,codex}`；**不得使用 PATH 上的任何 CLI**。`scripts/check-cli.sh` 驗證「實際版本 == pinned」且 claude ≥ **2.1.219**（`mcp_server_errors` 需 2.1.219+；permission MCP 啟動等待修正在 2.1.206），不符即 fail；記錄兩個 binary 的 sha256。背景：第二輪審閱環境 PATH 解析到 2.1.205、本機為 2.1.220，分歧即「記錄 PATH 版本不構成 pin」的實證。
- **訂閱與 credential 原則（v1.2 新增，對應使用者需求）**：兩個 provider 都用**官方瀏覽器 OAuth／device flow**登入（`claude` 官方 login、Codex「Sign in with ChatGPT」）；**app 不收密碼、不讀取、不搬移、不自行保管任何 token**——credential 由各官方 CLI 自有機制保管；app 只喚起 login 流程與查詢登入狀態。
- **Claude 訂閱合規（v1.4 措辭修正）**：app 定位為**個人自用**（2026-08-06 使用者確認）——本人、本機、自有帳號、經官方 CLI；此為 **M0 的 scope 決策與風險承擔，不是合規確認**：Anthropic 規範（[legal-and-compliance](https://code.claude.com/docs/en/legal-and-compliance)）未明確核可個人 wrapper，也未說僅發布才觸發限制，適用性未獲官方確認、列為已知風險。發布相關條款為條件款（僅在未來改變定位時觸發：需書面核准，未核准的對外版僅 API-key）。
- **Codex 協定紀律（v1.4 改為 schema-first）**：wire 為 JSON-RPC 2.0 語意但**省略 `jsonrpc` 欄位**、stdio 採 JSONL；handshake 嚴格 `initialize`（帶 clientInfo）→ `initialized` 通知，先送其他請求會收「Not initialized」。pin 後**先執行 `codex app-server generate-json-schema` 產出該版本專屬 schema 並 commit + digest**（[官方文件](https://learn.chatgpt.com/docs/app-server)）；方法名與 payload 以 schema 產物為準，測試以 schema 對齊的 real-wire fixtures 撰寫——**不延後到人工錄流**。**子集原則（v1.6，第六輪 P1）**：官方 item union 與核可回覆集合大於 M0 需求且會持續增長；計畫內的列舉一律是「M0 支援子集」，不宣稱完整 enum——完整集合以 schema 產物為準，未支援型別依 mapping 落 KindUnknown／KindSystemOther（raw 保留）。
- **Process tree 終止（v1.5 supervisor 收尾；v1.6 明定 stdout 汲取契約）**：兩個 runner 都以獨立 process group（`Setpgid`）啟動；`internal/proc` 的**背景 wait supervisor** 是唯一收尾路徑——子程序一退出即 group SIGKILL（清掉持有 stdout/stderr pipe 的殘存孫程序，reader 的 EOF 因此保證到來）、收完 stderr、快取 `Exit`。**汲取契約（v1.6，第六輪 P0 裁決：收斂契約，不做 supervisor spool）**：呼叫端必須在 `Start` 後**並行持續汲取 `Proc.Stdout`**（否則子程序輸出超過 pipe buffer 會卡在 write、永不退出，supervisor 無從介入）——兩個 runner 的 `Start` 內建 reader goroutine，契約由建構滿足；`Wait()` 與**汲取完成**無順序依賴、任意時點可呼叫（supervisor 保證的是「退出後 EOF 必到、Exit 快取」，不是免汲取）。stdout/stderr 用自建 `os.Pipe`（不用 `cmd.StdoutPipe`——`cmd.Wait` 會提前關 pipe 截走孫程序輸出）；ctx 取消與 stdin 寫入失敗一律**覆寫為 group 終止**。`Terminate()` ＝ group SIGTERM → grace → group SIGKILL。必測：「衍生且忽略 SIGTERM 的孫程序」的正常結束與 Terminate 兩情境、**大輸出（> pipe buffer）不死鎖**、以及**真正的 ctx 取消**（用不會自行退出的 script、等 ready 訊號後 cancel——第六輪 P0：舊測試把 `sleep 30` 接在 `exit 5` 之後，實測到的是正常退出 cleanup）。
- **Contract replay 非空保證（v1.4 新增）**：每條 replay test 的 committed fixture glob 為空即 **FAIL**（不是 skip、不是 0 檔通過）；glob 一律 **provider-scoped**（`claude-*.ndjson`／`codex-*.jsonl`），杜絕跨 provider 資料互染。Recorder 檔名同樣 provider-scoped：claude 錄 `.ndjson`、codex 錄 `.jsonl`（`recorder.New` 帶副檔名參數，v1.5）；**`New` 驗證 caseName 為合法 basename 且 provider prefix ↔ 副檔名一致**（v1.6：否則可經路徑分隔符寫出 recordings 目錄，或產生 replay glob 掃不到的檔案）。Codex 錄流採 **direction envelope**（`{"dir":"c2s"|"s2c","frame":{…}}`），replay 時 s2c 走事件映射、c2s 驗方法集；**長駐 Conn 的錄流為 session-scoped**（`BeginRecording`／`StopRecording` 原子 detach，v1.6——Task 8）。
- **Recorder 錯誤全路徑可見（v1.4 強化；v1.6 擴及 StopRecording）**：`New`／`Line`／`CloseWith`（含底層 close 與 meta 寫入）／`Conn.StopRecording` 任一失敗都必須進入可見的 session 失敗路徑（session:done 帶 recorderError、該驗收 case 記 FAIL），不得只留正常結束假象。**證據契約調整（v1.6；v1.7 型別落實）**：長駐 codex server 不隨回合退出，回合 meta 記 `process_still_running: true` 與 live stderr snapshot（`Server.StderrSnapshot()`）；`Meta.ExitCode` 為 **`*int` + omitempty**——執行中省略欄位、已退出時填入且**退出碼 0 必須保留**（非指標 int 會把「尚未退出」誤表示成 `exit_code:0`，第七輪 P0）。
- **App 內官方登入（v1.5 方法定名）**：app 提供 `StartLogin`／`Logout` 綁定——Codex 走 app-server account 方法，**官方文件已確認**（openai/codex app-server README，2026-08-06 查證）：`account/read`（查狀態）、`account/login/start {"type":"chatgpt"}`（result 含 `loginId`／`authUrl`）、`account/login/completed`＋`account/updated` 通知、`account/login/cancel`、`account/logout`；schema 產物（Task 1 Step 4b）仍為 pin 版本的最終依據。Claude 以 pinned CLI 的官方 auth 命令喚起；系統終端機 fallback 必須輪詢登入狀態回報完成（Task 9 Step 1c）。app 只處理 auth URL、狀態與完成事件，不接收密碼或 token。
- **錄流資料衛生**：原始 NDJSON／JSON-RPC 流量／稽核（可能含 prompt、thinking、程式碼、路徑、tool input）一律寫入 `.workbench/`（gitignored），**絕不 commit**。repo 只納入：去敏 fixtures（`testdata/fixtures/`）、metadata、digest、必要截圖。預期失敗也保存 stderr、exit code 與**完整 argv**。Task 1 之後 commit 一律列明確路徑，**禁止 `git add -A`**。
- 權限逾時預設 120s，env `WORKBENCH_APPROVAL_TIMEOUT`（Go duration）可覆寫；逾時、socket 斷線、MCP 失敗一律 deny（fail closed）。Codex 線的核可請求同樣逾時即 deny。
- **permission response 契約（Claude 線）**：allow 回覆**必須**含 `updatedInput`（未修改時 echo 原始 input），deny 含 `message`；回覆物件只含 CLI schema 欄位。精確 schema 以 A2 錄流為最終依據（contract probe 先於 typed schema）。**Codex 線同原則**：核可請求為 `item/commandExecution/requestApproval`／`item/fileChange/requestApproval`（回覆 `accept`／`decline`／`cancel`／`acceptWithExecpolicyAmendment`，[官方文件](https://learn.chatgpt.com/docs/app-server) 2026-08-06 查證）；精確欄位以 pinned 版本 schema 與 B 線錄流為準。
- **API-key 實測（A11 選測）成本授權**：執行前必須取得使用者明確成本授權，且使用隔離 credential profile（專用 key、只在該次 run 注入、不落 shell profile）。
- **轉方案 C 的唯一條件**：驗收失敗且歸因於 Claude CLI bridge 本身（協定／能力缺口，需官方文件或 issue 佐證）；Wails／Vue／自寫 code 的 bug 一律修復、不構成轉向理由。Codex 線失敗不觸發方案 C（app-server 本就是 JSON-RPC，無 sidecar 替代問題），其 go / no-go 獨立評估。
- 品質分級：`internal/contract`、`internal/claude`、`internal/codex`（含 `Single`／`RunHandshakeProbe`）、`internal/proc`、`internal/approval`、`internal/recorder` 是 production seed，必須有測試；`frontend/` 與 `app.go` 為 spike 品質（M1 重整），檔頭標註 `// spike quality: to be rebuilt in M1`。
- 最終 gate 必須**實際執行**：`go vet ./...`、`go test -race ./...`、frontend build、`wails build`、**封裝後 app 的隔離雙 provider smoke**（v1.4：managed CLI 複製進 bundle、.app 複製到隔離暫存目錄、非 repo cwd 啟動、驗證 source tree 不可見——Task 12 步驟，不是列表）。CLI 為 node script，**node 為系統前置需求**（版本記入 VERSIONS.md、app 啟動時檢查）；runtime 內嵌與否是 M1 打包決策，M0 如實記錄此邊界。

## 檔案結構（決策鎖定）

```
sdlc-workbench/
├── main.go                        # dispatch：GUI / `mcp-approval` 子命令
├── app.go                         # Wails 綁定（spike 品質；Recorder 接線、TerminateSession、provider 切換）
├── wails.json / go.mod / .gitignore
├── tools/claude-cli/package.json  # exact pin（node_modules gitignored）
├── tools/codex-cli/package.json   # exact pin（node_modules gitignored）
├── scripts/check-cli.sh           # 雙 CLI 版本 gate
├── scripts/record-claude.sh       # Claude 錄流（→ .workbench/recordings/）
├── scripts/record-codex.sh        # Codex JSON-RPC 錄流（→ .workbench/recordings/）
├── internal/contract/event.go     # provider-neutral envelope（+ _test.go）
├── internal/claude/
│   ├── decode.go / decode_test.go     # stream-json → contract.Event
│   ├── session.go / session_test.go   # CLI 生命週期、stderr、grace kill、canonical cwd
│   └── registry.go / registry_test.go # session_id ↔ cwd 綁定、resume mismatch 拒絕
├── internal/proc/proc.go              # 共用：process group + 背景 wait supervisor（退出即清整組、Exit 快取；+ _test.go）
├── internal/codex/
│   ├── rpc.go / rpc_test.go           # 無 jsonrpc 欄位的 framing、handshake 狀態機、server→client request、notification
│   ├── mapevent.go                    # schema 對齊的事件映射表（method → contract.Kind）
│   ├── session.go / session_test.go   # app-server 子程序生命週期（含 process-tree、失敗路徑測試）
│   ├── single.go / single_test.go     # 單一長駐 instance ownership（Ensure 併發序列化 v1.8；WithExclusive 原子 replacement v1.9）
│   └── probe.go / probe_test.go       # B1 handshake probe 編排（四階段失敗注入 v1.8；probe×Ensure 互斥 v1.9）
├── schemas/codex/                     # `codex app-server generate-json-schema` 產物（committed + SHA256SUMS）
├── internal/approval/
│   ├── broker.go / broker_test.go     # socket listener、逾時 deny、updatedInput echo、稽核
│   └── mcpserver.go / mcpserver_test.go # stdio MCP server + E2E（allow / deny-via-broker / broker-down）
├── internal/recorder/recorder.go      # 原始行錄流 + meta（完整 argv/exit/stderr；錯誤不吞）
├── frontend/src/components/{Transcript,ApprovalDialog,MermaidPane}.vue
├── testdata/
│   ├── fake-claude.sh                 # 假 Claude CLI（單元測試；含 orphan / badline 情境）
│   ├── fake-codex-appserver.sh        # 假 app-server（**只**測 runner 生命週期；協定邏輯用 Go in-test fake）
│   ├── fixtures/                      # 去敏 fixtures（committed；contract test 資料源）
│   └── synthetic/malformed.ndjson     # 合成 malformed / unknown（不入 contract glob）
├── .workbench/                        # gitignored：recordings/、audit.jsonl、probe/、state
└── docs/{architecture,VERSIONS.md,spikes/m0-results.md,sample.mmd}
```

---

### Task 1：Repo scaffold、雙 CLI exact pin、文件快照

**Files:**
- Create: repo skeleton（`wails init`）、`tools/{claude-cli,codex-cli}/package.json`、`scripts/check-cli.sh`、`docs/architecture/`、`docs/VERSIONS.md`、`.gitignore`

**Interfaces:**
- Produces: 可 build 的 Wails v2 + Vue 3 專案；兩個 managed binary 路徑；`scripts/check-cli.sh` 是所有整合步驟的前置 gate。

- [ ] **Step 1: 建 repo 與 scaffold**

```bash
mkdir -p /Users/eason_tseng/playground/sdlc-workbench && cd $_
git init
wails init -n sdlc-workbench -t vue-ts -d .   # go.mod module 調成 github.com/slam0504/sdlc-workbench
```

- [ ] **Step 2: `.gitignore`**

```
.workbench/
tools/claude-cli/node_modules/
tools/codex-cli/node_modules/
build/bin/
frontend/dist/
node_modules/
```

- [ ] **Step 3: pin 兩個 CLI（exact 版本，取執行當日 stable）**

```bash
for p in "claude-cli:@anthropic-ai/claude-code" "codex-cli:@openai/codex"; do
  dir="tools/${p%%:*}"; pkg="${p#*:}"
  mkdir -p "$dir"
  v=$(npm view "$pkg" version)
  printf '{ "private": true, "dependencies": { "%s": "%s" } }\n' "$pkg" "$v" > "$dir/package.json"
  npm install --prefix "$dir"
done
```

- [ ] **Step 4: 版本 gate `scripts/check-cli.sh`（`chmod +x`）**

```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIN_CLAUDE="2.1.219"
fail() { echo "FAIL: $1"; exit 1; }
check() { # $1=dir $2=pkg $3=bin
  local pinned actual bin="$ROOT/tools/$1/node_modules/.bin/$3"
  pinned="$(node -p "require('$ROOT/tools/$1/package.json').dependencies['$2']")"
  actual="$("$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
  [ "$actual" = "$pinned" ] || fail "$3 binary $actual != pinned $pinned"
  echo "$3 $actual sha256=$(shasum -a 256 "$bin" | awk '{print $1}')"
}
check claude-cli @anthropic-ai/claude-code claude
printf '%s\n%s\n' "$MIN_CLAUDE" "$(check claude-cli @anthropic-ai/claude-code claude | awk '{print $2}')" | sort -V -C \
  || fail "claude < min $MIN_CLAUDE"
check codex-cli @openai/codex codex
echo "OK"
```

Run: `scripts/check-cli.sh` → Expected: 兩行版本 + sha256 + `OK`；輸出抄進 `docs/VERSIONS.md`。

- [ ] **Step 4b: 產生並凍結 Codex protocol schema（v1.4 新增）**

```bash
mkdir -p schemas/codex
tools/codex-cli/node_modules/.bin/codex app-server generate-json-schema --out schemas/codex
( cd schemas/codex && shasum -a 256 * > SHA256SUMS )
```

Expected: schema 檔產出且與 pinned 版本綁定；從 schema 確認方法集（initialize／initialized、thread/start、turn/start、item/agentMessage/delta、turn/completed、approval 請求與 account 登入方法的實際名稱），把名稱填進 `internal/codex` 的 method 常數表（Task 8）。schema 目錄 commit。

- [ ] **Step 5: 文件快照 + 版本基線**

```bash
mkdir -p docs/architecture docs/spikes
cp ~/playground/reports/sdlc-workbench-app-plan.md ~/playground/reports/sdlc-bdd-ddd-tdd-reference.md \
   ~/playground/reports/sdlc-ai-agent-automation-plan.md ~/playground/reports/sdlc-workbench-m0-plan.md docs/architecture/
( cd docs/architecture && shasum -a 256 *.md > SHA256SUMS )
{ echo "# M0 版本基線（$(date +%F)）"; scripts/check-cli.sh
  echo "- wails: $(wails version)"; echo "- go: $(go version)"; echo "- node: $(node --version)"
} > docs/VERSIONS.md
```

（只快照執行當下**已核可**版本；有更新版以更新版為準並重算 SHA。）

- [ ] **Step 6: 驗證 build** — Run: `wails doctor && wails build && open build/bin/sdlc-workbench.app` → Expected: doctor 無 blocker、app 開出視窗。
- [ ] **Step 7: Commit** — `git add -A && git commit -m "chore: scaffold, pin claude+codex CLIs, snapshot approved docs"`（唯一允許 `-A` 的一次：此時尚無錄流）。

---

### Task 2：Provider-neutral event contract

**Files:**
- Create: `internal/contract/event.go`；Test: `internal/contract/event_test.go`

**Interfaces:**
- Produces（後續所有 task 只依此，不碰 provider wire format）:

```go
package contract

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type Kind string

const (
	KindInit        Kind = "init"         // session 建立（含 provider session id）
	KindMessage     Kind = "message"      // 完整訊息（assistant / user）
	KindDelta       Kind = "delta"        // 串流片段（text / thinking）
	KindToolUse     Kind = "tool_use"     // 工具呼叫與結果摘要
	KindApproval    Kind = "approval"     // 核可請求（配 ApprovalRequest）
	KindRetry       Kind = "retry"        // provider 重試中
	KindResult      Kind = "result"       // turn 結束（成功或失敗）
	KindSystemOther Kind = "system_other" // 已認得但 M0 不處理的系統事件
	KindUnknown     Kind = "unknown"      // 不認得：Raw 保留、不中斷
	KindMalformed   Kind = "malformed"    // 解析失敗：Raw 保留、Err 必填
	KindStreamError Kind = "stream_error" // 傳輸層錯誤（scanner / rpc）
)

type Event struct {
	Provider  Provider
	Kind      Kind
	SessionID string
	Raw       []byte // provider wire 原文，一律保留
	Text      string // delta / message 的文字
	Thinking  string
	IsError   bool    // result 用
	CostUSD   float64 // result 用（provider 有提供才填）
	Err       error
}

type ApprovalRequest struct {
	ID        string
	Provider  Provider
	ToolName  string // best-effort；原文在 RawParams
	Input     []byte
	RawParams []byte // provider 端請求原文（contract probe）
}

type ApprovalDecision struct {
	ID           string
	Behavior     string // allow | deny
	Message      string
	UpdatedInput []byte
}
```

- [ ] **Step 1: 寫失敗測試**

```go
package contract

import "testing"

func TestEventZeroValueIsInvalid(t *testing.T) {
	var e Event
	if e.Valid() {
		t.Fatal("zero event must be invalid")
	}
}

func TestEventValidRequiresProviderAndKind(t *testing.T) {
	e := Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")}
	if !e.Valid() {
		t.Fatal("provider+kind+raw must be valid")
	}
	if (Event{Provider: "x", Kind: KindDelta, Raw: []byte("{}")}).Valid() {
		t.Fatal("unknown provider must be invalid")
	}
	if (Event{Provider: ProviderCodex, Kind: KindMalformed, Raw: []byte("{}")}).Valid() {
		t.Fatal("malformed without Err must be invalid")
	}
}
```

- [ ] **Step 2: 確認失敗** — `go test ./internal/contract/ -v` → FAIL（`Valid` 未定義）。

- [ ] **Step 3: 實作 `Valid()`**

```go
func (e Event) Valid() bool {
	switch e.Provider {
	case ProviderClaude, ProviderCodex:
	default:
		return false
	}
	if e.Kind == "" || len(e.Raw) == 0 {
		return false
	}
	if e.Kind == KindMalformed && e.Err == nil {
		return false
	}
	return true
}
```

- [ ] **Step 4: 確認通過** — `go test ./internal/contract/ -v` → PASS。
- [ ] **Step 5: Commit** — `git add internal/contract && git commit -m "feat(contract): provider-neutral event envelope"`

---

### Task 3：Claude wire decoder（完整內嵌；unknown / malformed 不中斷）

**Files:**
- Create: `internal/claude/decode.go`；Test: `internal/claude/decode_test.go`

**Interfaces:**
- Consumes: `contract.Event` / `Kind`。
- Produces: `claude.Decode(line []byte) contract.Event`（Provider 固定 `ProviderClaude`）；`claude.InitInfo`（`ParseInit(ev) *InitInfo`）供 app 讀 `session_id` / `mcp_servers` / `mcp_server_errors` / `capabilities`。

- [ ] **Step 1: 寫失敗測試**

```go
package claude

import (
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		name string
		line string
		want contract.Kind
	}{
		{"init", `{"type":"system","subtype":"init","session_id":"abc-123","model":"m","capabilities":["interrupt_receipt_v1"],"mcp_servers":[{"name":"workbench","status":"connected"}]}`, contract.KindInit},
		{"init_mcp_error", `{"type":"system","subtype":"init","session_id":"abc-123","mcp_servers":[],"mcp_server_errors":[{"name":"workbench","type":"invalid_config","message":"bad"}]}`, contract.KindInit},
		{"text_delta", `{"type":"stream_event","session_id":"abc-123","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}`, contract.KindDelta},
		{"thinking_delta", `{"type":"stream_event","session_id":"abc-123","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}}`, contract.KindDelta},
		{"assistant", `{"type":"assistant","session_id":"abc-123","message":{"role":"assistant","content":[]}}`, contract.KindMessage},
		{"user", `{"type":"user","session_id":"abc-123","message":{"role":"user","content":[]}}`, contract.KindMessage},
		{"result_ok", `{"type":"result","subtype":"success","session_id":"abc-123","result":"done","total_cost_usd":0.012,"is_error":false}`, contract.KindResult},
		{"result_err", `{"type":"result","subtype":"error_during_execution","session_id":"abc-123","is_error":true}`, contract.KindResult},
		{"api_retry", `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":2000,"error_status":529,"error":"overloaded","uuid":"u1","session_id":"abc-123"}`, contract.KindRetry},
		{"system_other", `{"type":"system","subtype":"plugin_install","status":"started"}`, contract.KindSystemOther},
		{"unknown", `{"type":"banana","x":1}`, contract.KindUnknown},
		{"malformed", `{"type":"resul`, contract.KindMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Decode([]byte(c.line))
			if ev.Kind != c.want {
				t.Fatalf("kind = %s, want %s", ev.Kind, c.want)
			}
			if ev.Provider != contract.ProviderClaude || string(ev.Raw) != c.line {
				t.Fatal("provider / raw not preserved")
			}
			if !ev.Valid() {
				t.Fatal("decoded event must satisfy contract.Valid")
			}
		})
	}
	if ev := Decode([]byte(cases[2].line)); ev.Text != "Hel" {
		t.Fatalf("text = %q", ev.Text)
	}
	if ev := Decode([]byte(cases[3].line)); ev.Thinking != "hm" {
		t.Fatalf("thinking = %q", ev.Thinking)
	}
	if ev := Decode([]byte(cases[7].line)); !ev.IsError {
		t.Fatal("is_error not mapped")
	}
	init := ParseInit(Decode([]byte(cases[1].line)))
	if init == nil || len(init.MCPServerErrors) != 1 || init.MCPServerErrors[0].Type != "invalid_config" {
		t.Fatalf("init parse: %+v", init)
	}
}
```

- [ ] **Step 2: 確認失敗** — `go test ./internal/claude/ -run TestDecode -v` → FAIL。

- [ ] **Step 3: 完整實作**

```go
package claude

import (
	"encoding/json"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type MCPServerError struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
}
type InitInfo struct {
	SessionID       string            `json:"session_id"`
	Model           string            `json:"model"`
	Capabilities    []string          `json:"capabilities"`
	MCPServers      []MCPServerStatus `json:"mcp_servers"`
	MCPServerErrors []MCPServerError  `json:"mcp_server_errors"`
}

func Decode(line []byte) contract.Event {
	raw := append([]byte(nil), line...)
	ev := contract.Event{Provider: contract.ProviderClaude, Raw: raw}
	var head struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		ev.Kind, ev.Err = contract.KindMalformed, err
		return ev
	}
	ev.SessionID = head.SessionID
	switch head.Type {
	case "system":
		switch head.Subtype {
		case "init":
			ev.Kind = contract.KindInit
		case "api_retry":
			ev.Kind = contract.KindRetry
		default:
			ev.Kind = contract.KindSystemOther
		}
	case "assistant", "user":
		ev.Kind = contract.KindMessage
	case "stream_event":
		ev.Kind = contract.KindDelta
		var body struct {
			Event struct {
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			} `json:"event"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.Text, ev.Thinking = body.Event.Delta.Text, body.Event.Delta.Thinking
		}
	case "result":
		ev.Kind = contract.KindResult
		var body struct {
			IsError      bool    `json:"is_error"`
			TotalCostUSD float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.IsError, ev.CostUSD = body.IsError, body.TotalCostUSD
		}
	default:
		ev.Kind = contract.KindUnknown
	}
	return ev
}

// ParseInit 只對 KindInit 事件回傳完整 init 資訊，其餘回 nil。
func ParseInit(ev contract.Event) *InitInfo {
	if ev.Kind != contract.KindInit {
		return nil
	}
	info := &InitInfo{}
	if json.Unmarshal(ev.Raw, info) != nil {
		return nil
	}
	return info
}
```

- [ ] **Step 4: 確認通過** — `go test ./internal/claude/ -run TestDecode -v` → PASS。
- [ ] **Step 5: Commit** — `git add internal/claude && git commit -m "feat(claude): wire decoder to neutral contract"`

---

### Task 4：共用 process supervisor（`internal/proc`）+ Claude session runner（stderr、canonical cwd、失敗路徑）與 registry

**Files:**
- Create: `internal/proc/proc.go`、`internal/claude/session.go`、`internal/claude/registry.go`、`testdata/fake-claude.sh`
- Test: `internal/proc/proc_test.go`、`internal/claude/session_test.go`、`internal/claude/registry_test.go`

**Interfaces:**
- Consumes: `Decode`。
- Produces:
  - `proc.Start(ctx, proc.Config{Binary string; Args []string; Dir string; Env []string; TermGrace time.Duration}) (*Proc, error)`——獨立 process group + 背景 wait supervisor（v1.5 重設計，Global Constraints）；`Proc.Stdin io.WriteCloser`／`Proc.Stdout io.ReadCloser`（自建 pipe；**契約：呼叫端須並行持續汲取，否則大輸出反壓會讓子程序卡在 write**——v1.6；EOF 由 supervisor 的 group SIGKILL 保證）、`Proc.Terminate() error`（group SIGTERM → grace（預設 5s）→ group SIGKILL）、`Proc.Wait() Exit`（**supervisor 快取結果，任意時點、任意次數可呼叫，與汲取「完成」無順序依賴**）、`Proc.SignalGroup(sig)`、`Proc.PGID() int`、`Proc.StderrSnapshot() string`（**v1.6：長駐程序仍在跑時的 live stderr tail，證據 meta 用**）、`Proc.Done() <-chan struct{}`（**v1.7：supervisor 收尾完成（Exit 已快取）後關閉；`select`-default 即為非阻塞存活判定，`ensureAppServer` 的「已死」依據**）；`proc.Exit{Code int, StderrTail string, Err error}`（stderr 保留最後 64KB）。Codex runner（Task 8）共用本套語意。
  - `claude.NormalizeCWD(p string) (string, error)`（Abs + EvalSymlinks）。
  - `claude.Start(ctx, Config) (*Session, error)`；`Config{Binary, CWD, Prompt, Resume, MCPConfigPath, PermissionPromptTool, SettingsJSON string; Env []string; TermGrace time.Duration; MaxLineBytes int}`。
  - `Session.Events() <-chan contract.Event`（scanner 錯誤 → 合成 `KindStreamError` 後 `Terminate` 整組並關閉）、`Session.Argv() []string`（**完整** argv，Recorder meta 用）、`Session.Terminate() error`、`Session.Wait() proc.Exit`、`Session.PGID() int`。
  - `claude.OpenRegistry(path string) (*Registry, error)`；`Bind(sessionID, cwd string) error`、`CWD(sessionID string) (string, bool)`。

- [ ] **Step 1: `testdata/fake-claude.sh`（`chmod +x`）**

```bash
#!/usr/bin/env bash
# FAKE_EXIT=退出碼 FAKE_HANG=收尾前掛住 FAKE_STDERR=吐 stderr FAKE_DIE=init 後立刻死（無 result）
# FAKE_ORPHAN=衍生忽略 SIGTERM 的孫程序（繼承 pipe） FAKE_BADLINE=吐超過 buffer 的行（scanner error）
read -r _prompt || true
[ -n "${FAKE_STDERR:-}" ] && echo "boom-stderr" >&2
[ -n "${FAKE_ORPHAN:-}" ] && bash -c 'trap "" TERM; sleep 30' &
echo '{"type":"system","subtype":"init","session_id":"fake-1","model":"m","mcp_servers":[]}'
[ -n "${FAKE_DIE:-}" ] && exit 7
[ -n "${FAKE_BADLINE:-}" ] && printf '{"type":"x","pad":"%s"}\n' "$(head -c 4096 </dev/zero | tr '\0' a)"
echo '{"type":"stream_event","session_id":"fake-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}'
if [ -n "${FAKE_HANG:-}" ]; then sleep 30; fi
echo '{"type":"result","subtype":"success","session_id":"fake-1","result":"hi","total_cost_usd":0,"is_error":false}'
exit "${FAKE_EXIT:-0}"
```

- [ ] **Step 2: `internal/proc` 失敗測試（v1.5 新增：supervisor 語意先於 claude session 驗證）**

```go
package proc

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func bashProc(t *testing.T, ctx context.Context, script string, grace time.Duration) *Proc {
	t.Helper()
	p, err := Start(ctx, Config{Binary: "/bin/bash", Args: []string{"-c", script}, Dir: t.TempDir(), TermGrace: grace})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// drainStdout 依 v1.6 汲取契約啟動並行 reader；回傳讀到的內容（reader 結束後才可讀取 buffer）。
func drainStdout(p *Proc) (*bytes.Buffer, *sync.WaitGroup) {
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(&buf, p.Stdout) }()
	return &buf, &wg
}

func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) != nil } // ESRCH = 整組已消失

// 孫程序忽略 SIGTERM 且繼承 stdout/stderr pipe——第五輪 P0 的核心情境。
const orphanScript = `bash -c 'trap "" TERM; sleep 30' & echo out; echo err >&2; exit 5`

func TestNormalExitReapsOrphanAndCachesExit(t *testing.T) {
	p := bashProc(t, context.Background(), orphanScript, time.Second)
	out, rd := drainStdout(p) // v1.6 契約：reader 並行汲取；Wait 不等汲取完成即可呼叫
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code != 5 {
			t.Fatalf("code = %d, want 5", ex.Code)
		}
		if !strings.Contains(ex.StderrTail, "err") {
			t.Fatalf("stderr tail = %q", ex.StderrTail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait hung: supervisor must reap group while reader is still draining")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("orphan must be killed when parent exits")
	}
	select { // v1.7：Wait 返回後 Done 必已關閉（非阻塞存活判定的依據）
	case <-p.Done():
	default:
		t.Fatal("Done must be closed after Wait returns")
	}
	rd.Wait() // group kill 後 EOF 必然到來
	if !strings.Contains(out.String(), "out") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestLargeOutputDoesNotDeadlock(t *testing.T) { // v1.6：> pipe buffer 的輸出在並行汲取下不死鎖
	p := bashProc(t, context.Background(), `head -c 2000000 /dev/zero | tr '\0' a; exit 0`, time.Second)
	out, rd := drainStdout(p)
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code != 0 {
			t.Fatalf("code = %d", ex.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("large output deadlocked")
	}
	rd.Wait()
	if out.Len() < 2000000 {
		t.Fatalf("stdout truncated: %d bytes", out.Len())
	}
}

func TestTerminateEscalatesToGroupKill(t *testing.T) {
	p := bashProc(t, context.Background(), `trap "" TERM; echo up; sleep 30`, 200*time.Millisecond)
	buf := make([]byte, 8)
	_, _ = p.Stdout.Read(buf) // 等 trap 生效
	_, rd := drainStdout(p)
	start := time.Now()
	_ = p.Terminate()
	ex := p.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated proc must not exit 0")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill escalation too slow")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("group must be fully dead")
	}
	rd.Wait()
}

func TestCtxCancelKillsWholeGroup(t *testing.T) { // v1.6：獨立 script、不會自行退出——真正測 ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	p := bashProc(t, ctx, `bash -c 'trap "" TERM; sleep 30' & echo ready; sleep 30`, 200*time.Millisecond)
	buf := make([]byte, 8)
	_, _ = p.Stdout.Read(buf) // 等 ready：orphan 已衍生、parent 未退出
	_, rd := drainStdout(p)
	cancel()
	done := make(chan Exit, 1)
	go func() { done <- p.Wait() }()
	select {
	case ex := <-done:
		if ex.Code == 0 {
			t.Fatal("cancelled proc must not exit 0")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel must terminate whole group")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("group must be dead after ctx cancel")
	}
	rd.Wait()
}
```

Run: `go test ./internal/proc/ -v` → FAIL（`Start` 未定義）。

- [ ] **Step 3: `internal/proc` 完整實作**

```go
package proc

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Binary    string
	Args      []string
	Dir       string
	Env       []string      // 附加於 os.Environ()
	TermGrace time.Duration // 預設 5s
}

type Exit struct {
	Code       int
	StderrTail string
	Err        error
}

// Proc 以獨立 process group 啟動子程序，背景 supervisor 是唯一收尾路徑：
// 子程序一退出即 group SIGKILL（清掉持有 pipe 的孫程序 → reader 的 EOF 保證到來）
// → 收完 stderr → 快取 Exit。Wait() 只回傳快取結果，與汲取「完成」無順序依賴。
// 契約（v1.6）：呼叫端必須在 Start 後並行持續汲取 Stdout——supervisor 不做 stdout
// spool，若無人讀，子程序輸出超過 pipe buffer 會卡在 write、永不退出。
type Proc struct {
	cmd      *exec.Cmd
	pgid     int
	grace    time.Duration
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	mu       sync.Mutex
	stderr   []byte // 最後 64KB
	exit     Exit
	exitedCh chan struct{} // 子程序本體已退出
	doneCh   chan struct{} // Exit 已快取（stderr 收完）
}

const stderrCap = 64 * 1024

func (p *Proc) appendStderr(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stderr = append(p.stderr, b...)
	if n := len(p.stderr); n > stderrCap {
		p.stderr = p.stderr[n-stderrCap:]
	}
}

func (p *Proc) stderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.stderr)
}

func Start(ctx context.Context, cfg Config) (*Proc, error) {
	cmd := exec.Command(cfg.Binary, cfg.Args...) // 不用 CommandContext：ctx 取消必須殺整組（見下）
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// 自建 os.Pipe，不用 cmd.StdoutPipe：cmd.Wait 會在程序退出時關閉 StdoutPipe，
	// 孫程序尚未寫完的輸出會被截走；自建 pipe + group SIGKILL 保證
	// 「所有 write end 關閉 → reader 讀到 EOF」的順序成立。
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil { // binary 不存在等啟動失敗在這裡浮現
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, err
	}
	outW.Close() // 父程序不留 write end，否則 EOF 永不到來
	errW.Close()

	grace := cfg.TermGrace
	if grace == 0 {
		grace = 5 * time.Second
	}
	p := &Proc{cmd: cmd, pgid: cmd.Process.Pid, grace: grace, Stdin: stdin, Stdout: outR,
		exitedCh: make(chan struct{}), doneCh: make(chan struct{})}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stderr reader
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := errR.Read(buf)
			if n > 0 {
				p.appendStderr(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	go func() { // supervisor：唯一呼叫 cmd.Wait 的地方
		werr := cmd.Wait()
		close(p.exitedCh)
		_ = p.SignalGroup(syscall.SIGKILL) // 子程序已退出 → 立即清整組殘存孫程序
		wg.Wait()                          // stderr 讀到 EOF（group kill 保證）
		errR.Close()
		p.exit = Exit{Code: cmd.ProcessState.ExitCode(), StderrTail: p.stderrTail(), Err: werr}
		close(p.doneCh)
	}()
	go func() { // 覆寫 ctx 取消語意：走 Terminate（整組），不是單程序 kill
		select {
		case <-ctx.Done():
			_ = p.Terminate()
		case <-p.exitedCh:
		}
	}()
	return p, nil
}

func (p *Proc) SignalGroup(sig syscall.Signal) error { return syscall.Kill(-p.pgid, sig) }

func (p *Proc) PGID() int { return p.pgid }

// StderrSnapshot 回傳目前的 stderr tail（v1.6：長駐程序仍在跑時取證用，不等待退出）。
func (p *Proc) StderrSnapshot() string { return p.stderrTail() }

// Done 在 supervisor 收尾完成（Exit 已快取）後關閉；select-default 即為非阻塞存活判定（v1.7）。
func (p *Proc) Done() <-chan struct{} { return p.doneCh }

func (p *Proc) Terminate() error { // group SIGTERM → grace 內未退出 → group SIGKILL
	err := p.SignalGroup(syscall.SIGTERM)
	go func() {
		select {
		case <-p.exitedCh:
		case <-time.After(p.grace):
			_ = p.SignalGroup(syscall.SIGKILL)
		}
	}()
	return err
}

// Wait 回傳 supervisor 快取的 Exit；任意時點、任意次數可呼叫。
func (p *Proc) Wait() Exit {
	<-p.doneCh
	return p.exit
}
```

Run: `go test ./internal/proc/ -v` → PASS（3 測）。

- [ ] **Step 4: 寫 claude session／registry 失敗測試**

```go
package claude

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func fakeCfg(t *testing.T, env ...string) Config {
	p, _ := filepath.Abs("../../testdata/fake-claude.sh")
	return Config{Binary: p, CWD: t.TempDir(), Prompt: "x", Env: env, TermGrace: 200 * time.Millisecond}
}

func drain(s *Session) []contract.Kind {
	var ks []contract.Kind
	for ev := range s.Events() {
		ks = append(ks, ev.Kind)
	}
	return ks
}

func TestHappyPathAndArgv(t *testing.T) {
	s, err := Start(context.Background(), fakeCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	ks := drain(s)
	if len(ks) != 3 || ks[0] != contract.KindInit || ks[2] != contract.KindResult {
		t.Fatalf("kinds = %v", ks)
	}
	argv := strings.Join(s.Argv(), " ")
	for _, must := range []string{"--output-format stream-json", "--verbose", "--include-partial-messages"} {
		if !strings.Contains(argv, must) {
			t.Fatalf("argv missing %q: %s", must, argv)
		}
	}
	if ex := s.Wait(); ex.Code != 0 {
		t.Fatalf("exit = %d", ex.Code)
	}
}

func TestStartBinaryNotFound(t *testing.T) {
	if _, err := Start(context.Background(), Config{Binary: "/nonexistent/claude", CWD: t.TempDir(), Prompt: "x"}); err == nil {
		t.Fatal("must error on missing binary")
	}
}

func TestProcessDiesMidStream(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_DIE=1"))
	ks := drain(s)
	for _, k := range ks {
		if k == contract.KindResult {
			t.Fatal("must not reach result")
		}
	}
	if ex := s.Wait(); ex.Code != 7 {
		t.Fatalf("exit = %d, want 7", ex.Code)
	}
}

func TestStderrCaptured(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_STDERR=1"))
	drain(s)
	if ex := s.Wait(); !strings.Contains(ex.StderrTail, "boom-stderr") {
		t.Fatalf("stderr tail = %q", ex.StderrTail)
	}
}

func TestExitCodePropagates(t *testing.T) {
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_EXIT=3"))
	drain(s)
	if ex := s.Wait(); ex.Code != 3 {
		t.Fatalf("exit = %d, want 3", ex.Code)
	}
}

func groupDead(pgid int) bool { return syscall.Kill(-pgid, 0) != nil } // ESRCH = 整組已消失

func TestTerminateKillsProcessGroup(t *testing.T) { // 孫程序忽略 SIGTERM 也必須被整組收掉
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_HANG=1", "FAKE_ORPHAN=1"))
	<-s.Events()
	start := time.Now()
	_ = s.Terminate()
	drain(s) // 孫程序持有 pipe 也不得讓 drain 卡住（supervisor 的 group SIGKILL 保證 EOF）
	ex := s.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated session must not exit 0")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill escalation too slow")
	}
	if !groupDead(s.PGID()) {
		t.Fatal("process group must be fully dead (orphan survived)")
	}
}

func TestOrphanDoesNotHangNormalExit(t *testing.T) { // 第五輪 P0 情境：正常結束 + 孫程序持有 stdout
	s, _ := Start(context.Background(), fakeCfg(t, "FAKE_ORPHAN=1"))
	doneCh := make(chan struct{})
	go func() { drain(s); s.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("drain/Wait hung on orphan-held pipes")
	}
	if !groupDead(s.PGID()) {
		t.Fatal("orphan must be reaped by supervisor on parent exit")
	}
}

func TestScannerErrorSurfaced(t *testing.T) { // v1.4：超長行 → KindStreamError，不是無聲截斷
	cfg := fakeCfg(t, "FAKE_BADLINE=1")
	cfg.MaxLineBytes = 1024
	s, _ := Start(context.Background(), cfg)
	var sawStreamErr bool
	for ev := range s.Events() {
		if ev.Kind == contract.KindStreamError {
			sawStreamErr = true
		}
	}
	s.Wait()
	if !sawStreamErr {
		t.Fatal("oversized line must surface KindStreamError")
	}
}

func TestRegistryBindLookup(t *testing.T) {
	r, err := OpenRegistry(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("s1", "/a/b"); err != nil {
		t.Fatal(err)
	}
	if cwd, ok := r.CWD("s1"); !ok || cwd != "/a/b" {
		t.Fatalf("lookup = %q %v", cwd, ok)
	}
	if _, ok := r.CWD("nope"); ok {
		t.Fatal("unknown id must miss")
	}
}
```

- [ ] **Step 5: 確認失敗** — `go test ./internal/claude/ -v` → decoder PASS、session/registry FAIL。

- [ ] **Step 6: 完整實作 `session.go`（v1.5：薄封裝 `internal/proc`，收尾全交 supervisor）**

```go
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/proc"
)

type Config struct {
	Binary               string // 必填：managed CLI 路徑
	CWD                  string // canonical cwd：resume 必須與首次相同
	Prompt               string
	Resume               string
	MCPConfigPath        string
	PermissionPromptTool string
	SettingsJSON         string // --settings inline JSON（probe ask rule 用）
	Env                  []string
	TermGrace            time.Duration // 預設 5s
	MaxLineBytes         int           // scanner 行上限，預設 16MB；超過 → KindStreamError
}

func (c Config) args() []string {
	a := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages"}
	if c.SettingsJSON != "" {
		a = append(a, "--settings", c.SettingsJSON)
	}
	if c.PermissionPromptTool != "" {
		a = append(a, "--permission-prompt-tool", c.PermissionPromptTool,
			"--mcp-config", c.MCPConfigPath, "--strict-mcp-config")
	}
	if c.Resume != "" {
		a = append(a, "--resume", c.Resume)
	}
	return a
}

func NormalizeCWD(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

type Session struct {
	p      *proc.Proc
	argv   []string
	events chan contract.Event
}

func Start(ctx context.Context, cfg Config) (*Session, error) {
	cwd, err := NormalizeCWD(cfg.CWD)
	if err != nil {
		return nil, err
	}
	p, err := proc.Start(ctx, proc.Config{Binary: cfg.Binary, Args: cfg.args(),
		Dir: cwd, Env: cfg.Env, TermGrace: cfg.TermGrace})
	if err != nil { // binary 不存在等啟動失敗在這裡浮現
		return nil, err
	}
	msg, _ := json.Marshal(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": cfg.Prompt}}}})
	if _, err := fmt.Fprintf(p.Stdin, "%s\n", msg); err != nil {
		_ = p.SignalGroup(syscall.SIGKILL) // v1.5：stdin 失敗也殺整組，不是只殺直接子程序
		p.Wait()
		return nil, err
	}
	_ = p.Stdin.Close() // M0 單回合：送完 prompt 即關

	maxLine := cfg.MaxLineBytes
	if maxLine == 0 {
		maxLine = 16 * 1024 * 1024
	}
	s := &Session{p: p, argv: append([]string{cfg.Binary}, cfg.args()...),
		events: make(chan contract.Event, 64)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(p.Stdout)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		for sc.Scan() {
			s.events <- Decode(sc.Bytes())
		}
		if err := sc.Err(); err != nil { // 傳輸層錯誤是驗收證據，不可吞
			s.events <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindStreamError,
				Raw: []byte(err.Error()), Err: err}
			_ = p.Terminate() // stream 已不可信，收掉整組
		}
	}()
	return s, nil
}

func (s *Session) Events() <-chan contract.Event { return s.events }
func (s *Session) Argv() []string                { return append([]string(nil), s.argv...) }
func (s *Session) Terminate() error              { return s.p.Terminate() }
func (s *Session) Wait() proc.Exit               { return s.p.Wait() } // supervisor 快取，任意時點可呼叫
func (s *Session) PGID() int                     { return s.p.PGID() }
```

`registry.go`（完整）：

```go
package claude

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Registry struct {
	mu   sync.Mutex
	path string
}

type registryEntry struct {
	CWD       string `json:"cwd"`
	CreatedAt string `json:"created_at"`
}

func OpenRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) load() (map[string]registryEntry, error) {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return nil, err
	}
	m := map[string]registryEntry{}
	return m, json.Unmarshal(b, &m)
}

func (r *Registry) Bind(sessionID, cwd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return err
	}
	m[sessionID] = registryEntry{CWD: cwd, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(r.path, b, 0o644)
}

func (r *Registry) CWD(sessionID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return "", false
	}
	e, ok := m[sessionID]
	return e.CWD, ok
}
```

- [ ] **Step 7: 確認通過** — `go test ./internal/proc/ ./internal/claude/ -v` → PASS。
- [ ] **Step 8: Commit** — `git add internal/proc internal/claude testdata/fake-claude.sh && git commit -m "feat(proc,claude): supervisor-reaped process group runner, session, registry"`

---

### Task 5：Recorder（錯誤不吞、完整 argv）與 contract test

**Files:**
- Create: `internal/recorder/recorder.go`、`scripts/record-claude.sh`、`testdata/fixtures/.gitkeep`、`testdata/synthetic/malformed.ndjson`
- Test: `internal/recorder/recorder_test.go`、`internal/claude/contract_replay_test.go`

**Interfaces:**
- Produces: `recorder.New(dir, caseName, ext string) (*Recorder, error)`（v1.5：ext ∈ `".ndjson"`｜`".jsonl"`；**v1.6：caseName 驗證**——必須符合 `^(claude|codex)-[A-Za-z0-9._-]+$`（合法 basename、無路徑分隔符）且 prefix ↔ ext 一致（`claude-` ↔ `.ndjson`、`codex-` ↔ `.jsonl`），否則回 error——防止寫出 recordings 目錄或產生 replay glob 掃不到的檔案）；`(*Recorder).Line(b []byte) error`（**回傳寫入錯誤**且 latch 到 `Err()`；**v1.6：mutex 保護，c2s／s2c 並行 tee 安全**）；`(*Recorder).Err() error`；`(*Recorder).CloseWith(meta Meta) error`；`Meta{Provider, CLIVersion string, Argv []string, CWD, RecordedAt string, ExitCode *int, ProcessStillRunning bool, StderrTail, RecorderError string}`（**v1.6：長駐 server 的回合 meta 記 `process_still_running: true` + live stderr snapshot；v1.7：ExitCode 為 `*int` + omitempty——執行中省略、已退出時填入且退出碼 0 保留**）。
- 錄流落點 `.workbench/recordings/`；**Claude 線 contract replay 資料源 = `testdata/fixtures/claude-*.ndjson`（committed、去敏）∪ `.workbench/recordings/claude-*.ndjson`（本機有才跑）**（v1.5：glob 一律 provider-scoped；Codex 線 replay 在 Task 8，讀 `codex-*.jsonl`）；`testdata/synthetic/` 不在 glob 內（malformed 案例與 replay 驗收互不衝突）。

- [ ] **Step 1: 寫失敗測試**

```go
package recorder

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWritesNDJSONAndFullMeta(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "claude-case1", ".ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Line([]byte(`{"type":"result"}`)); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := r.CloseWith(Meta{Provider: "claude", CLIVersion: "2.x",
		Argv: []string{"claude", "-p", "--verbose"}, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "claude-case1.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{`"argv"`, `"--verbose"`, `"exit_code": 0`, `"provider"`} {
		if !strings.Contains(string(b), must) {
			t.Fatalf("meta lacks %s: %s", must, b)
		}
	}
}

func TestExitCodeOnlyWhenExited(t *testing.T) { // v1.7：執行中無 exit_code；退出碼 0 必須保留
	dir := t.TempDir()
	r, _ := New(dir, "codex-running", ".jsonl")
	if err := r.CloseWith(Meta{Provider: "codex", ProcessStillRunning: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "codex-running.meta.json"))
	if strings.Contains(string(b), `"exit_code"`) {
		t.Fatalf("running meta must omit exit_code entirely: %s", b)
	}
	if !strings.Contains(string(b), `"process_still_running": true`) {
		t.Fatalf("running meta must mark process_still_running: %s", b)
	}
}

func TestNewValidatesCaseNameAndExt(t *testing.T) { // v1.6：白名單 + basename + prefix↔ext 一致
	dir := t.TempDir()
	for _, bad := range []struct{ name, ext string }{
		{"claude-x", ".txt"},        // 副檔名白名單
		{"case1", ".ndjson"},        // 無 provider prefix
		{"../claude-evil", ".ndjson"}, // 路徑分隔符
		{"sub/claude-x", ".ndjson"},
		{"codex-x", ".ndjson"},      // prefix ↔ ext 不一致
		{"claude-x", ".jsonl"},
	} {
		if _, err := New(dir, bad.name, bad.ext); err == nil {
			t.Fatalf("must reject %q %s", bad.name, bad.ext)
		}
	}
	if _, err := New(dir, "codex-ok", ".jsonl"); err != nil {
		t.Fatal(err)
	}
}

func TestLineConcurrentSafe(t *testing.T) { // v1.6：c2s／s2c 並行 tee；go test -race 驗證
	r, err := New(t.TempDir(), "codex-race", ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.Line([]byte(`{"dir":"s2c","frame":{}}`))
			}
		}()
	}
	wg.Wait()
	if err := r.CloseWith(Meta{Provider: "codex", ProcessStillRunning: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLinePropagatesWriteError(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "claude-case2", ".ndjson")
	if err != nil {
		t.Fatal(err)
	}
	r.f.Close() // 人為造成底層檔案不可寫
	if err := r.Line([]byte("x")); err == nil {
		t.Fatal("Line must propagate write error")
	}
	if r.Err() == nil {
		t.Fatal("Err must latch")
	}
	// v1.4：有 latched 錯誤時 CloseWith 必須「meta 照寫 + 回傳非 nil」，錯誤不得只留在 meta
	if err := r.CloseWith(Meta{}); err == nil {
		t.Fatal("CloseWith must return the latched error")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "claude-case2.meta.json"))
	if !strings.Contains(string(b), `"recorder_error"`) {
		t.Fatalf("meta must still carry recorder_error: %s", b)
	}
}

func TestCloseWithPropagatesCloseError(t *testing.T) { // v1.4：底層 close 失敗不可吞
	dir := t.TempDir()
	r, _ := New(dir, "claude-case3", ".ndjson")
	r.f.Close() // 先關掉 → CloseWith 的 close 會失敗
	if err := r.CloseWith(Meta{}); err == nil {
		t.Fatal("CloseWith must propagate underlying close error")
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-case3.meta.json")); err != nil {
		t.Fatal("meta must be written even when close fails")
	}
}
```

- [ ] **Step 2: 確認失敗** — `go test ./internal/recorder/ -v` → FAIL。

- [ ] **Step 3: 完整實作**

```go
package recorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Meta struct {
	Provider            string   `json:"provider"`
	CLIVersion          string   `json:"cli_version"`
	Argv                []string `json:"argv"`
	CWD                 string   `json:"cwd"`
	RecordedAt          string   `json:"recorded_at"`
	ExitCode            *int     `json:"exit_code,omitempty"` // v1.7：*int——執行中省略；已退出必填（0 也保留）
	ProcessStillRunning bool     `json:"process_still_running,omitempty"` // v1.6：長駐 server 回合證據
	StderrTail          string   `json:"stderr_tail,omitempty"`
	RecorderError       string   `json:"recorder_error,omitempty"`
}

type Recorder struct {
	mu   sync.Mutex // v1.6：c2s／s2c 並行 tee 安全
	f    *os.File
	dir  string
	name string
	err  error
}

var caseNameRe = regexp.MustCompile(`^(claude|codex)-[A-Za-z0-9._-]+$`) // 合法 basename，無路徑分隔符

func New(dir, caseName, ext string) (*Recorder, error) {
	if ext != ".ndjson" && ext != ".jsonl" { // v1.5：provider-scoped 副檔名白名單
		return nil, fmt.Errorf("recorder: unsupported ext %q", ext)
	}
	if !caseNameRe.MatchString(caseName) { // v1.6：防路徑逃逸與 glob 掃不到的檔名
		return nil, fmt.Errorf("recorder: invalid case name %q", caseName)
	}
	if (ext == ".ndjson") != strings.HasPrefix(caseName, "claude-") { // v1.6：prefix ↔ ext 一致
		return nil, fmt.Errorf("recorder: case %q does not match ext %q", caseName, ext)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, caseName+ext))
	if err != nil {
		return nil, err
	}
	return &Recorder{f: f, dir: dir, name: caseName}, nil
}

func (r *Recorder) Line(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.f.Write(append(append([]byte(nil), b...), '\n'))
	if err != nil && r.err == nil {
		r.err = err
	}
	return err
}

func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) CloseWith(m Meta) error { // v1.4：close / meta / latched 錯誤全部回傳，meta 仍盡力寫
	r.mu.Lock()
	defer r.mu.Unlock()
	closeErr := r.f.Close()
	if r.err == nil && closeErr != nil {
		r.err = closeErr
	}
	if r.err != nil {
		m.RecorderError = r.err.Error()
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	metaErr := os.WriteFile(filepath.Join(r.dir, r.name+".meta.json"), b, 0o644)
	return errors.Join(r.err, metaErr)
}
```

（測試需要存取 `r.f`，`recorder_test.go` 與實作同 package。）

`internal/claude/contract_replay_test.go`（fixtures + 本機錄流雙來源）：

```go
package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func loadAllowUnknown(ndjsonPath string) map[string]bool {
	m := map[string]bool{}
	b, err := os.ReadFile(ndjsonPath[:len(ndjsonPath)-len(".ndjson")] + ".allow-unknown")
	if err != nil {
		return m
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if s := string(bytes.TrimSpace(line)); s != "" {
			m[s] = true
		}
	}
	return m
}

func topLevelType(line []byte) string {
	var h struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(line, &h)
	return h.Type
}

func TestContractReplay(t *testing.T) {
	// v1.4：(1) glob 一律 provider-scoped，Codex 錄流不得流入 claude.Decode；
	//        (2) committed fixture 為空即 FAIL——不允許 vacuous pass。
	fixtures, _ := filepath.Glob("../../testdata/fixtures/claude-*.ndjson")
	if len(fixtures) == 0 {
		t.Fatal("no committed claude fixture — replay would be vacuous; commit testdata/fixtures/claude-*.ndjson first")
	}
	recordings, _ := filepath.Glob("../../.workbench/recordings/claude-*.ndjson")
	for _, group := range [][]string{fixtures, recordings} {
		for _, f := range group {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			allowed := loadAllowUnknown(f)
			for i, line := range bytes.Split(data, []byte("\n")) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				ev := Decode(line)
				if ev.Kind == contract.KindMalformed {
					t.Errorf("%s line %d malformed", f, i+1)
				}
				if ev.Kind == contract.KindUnknown && !allowed[topLevelType(line)] {
					t.Errorf("%s line %d unknown %q not allow-listed", f, i+1, topLevelType(line))
				}
			}
		}
	}
}
```

**Step 3b（v1.4 新增）：commit 最小 claude fixture，讓 replay 從第一天就非空**——建 `testdata/fixtures/claude-basic.sample.ndjson`，內容取 Task 3 decode 測試的 init／text_delta／assistant／result 四行真實形狀樣本（占位文字、無敏感內容）；Task 11 的真實錄流去敏後**增補**、不取代此檔。

`testdata/synthetic/malformed.ndjson`（兩行，decoder 韌性引用）：

```
{"type":"future_event","v":2}
{"type":"resul
```

`scripts/record-claude.sh`（`chmod +x`；argv 完整寫入 meta）：

```bash
#!/usr/bin/env bash
# 用法: scripts/record-claude.sh <case> "<prompt>" [extra flags...]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
"$ROOT/scripts/check-cli.sh" >/dev/null
BIN="$ROOT/tools/claude-cli/node_modules/.bin/claude"
case="$1"; prompt="$2"; shift 2
args=(-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages "$@")
out="$ROOT/.workbench/recordings"; mkdir -p "$out"
set +e
printf '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}\n' "$prompt" | \
  "$BIN" "${args[@]}" >"$out/$case.ndjson" 2>"$out/$case.stderr.log"
code=$?; set -e
jq -n --arg v "$("$BIN" --version)" --arg cwd "$PWD" --arg at "$(date -u +%FT%TZ)" --argjson code "$code" \
  --args '{provider:"claude",cli_version:$v,cwd:$cwd,recorded_at:$at,exit_code:$code,argv:$ARGS.positional}' \
  -- "$BIN" "${args[@]}" > "$out/$case.meta.json"
echo "recorded: $out/$case.ndjson (exit $code)"
```

- [ ] **Step 4: 確認通過** — `go test ./internal/recorder/ ./internal/claude/ -v` → PASS。前提＝Step 3b 的 committed claude fixture 已存在（fixture glob 為空即 FAIL，無 vacuous pass）；`.workbench/recordings/` 群組本機沒有時只跑 fixtures 群組。
- [ ] **Step 5: Commit** — `git add internal/recorder internal/claude scripts/record-claude.sh testdata && git commit -m "feat(recorder): error-latching recorder with full argv meta; dual-source replay"`

---

### Task 6：Approval broker（updatedInput echo、逾時 deny、稽核含 RawParams）

**Files:**
- Create: `internal/approval/broker.go`；Test: `internal/approval/broker_test.go`

**Interfaces:**
- Produces: `approval.NewBroker(socketPath string, timeout time.Duration, audit io.Writer) (*Broker, error)`；`Broker.Pending() <-chan Request`；`Broker.Resolve(id string, d Decision) error`；`Broker.Close() error`。
  - `Request{ID, ToolName string, Input, RawParams json.RawMessage}`；`Decision{ID, Behavior, Message string, UpdatedInput json.RawMessage}`。
  - 線協定＝每行一個 JSON（子程序送 Request、broker 回 Decision）。**allow 且 UpdatedInput 為空時自動 echo `Request.Input`**。request／decision 各寫一行稽核 JSONL（request 含 RawParams 原文）。

- [ ] **Step 1: 寫失敗測試（完整五測）**

```go
package approval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func newTestBroker(t *testing.T, timeout time.Duration) (*Broker, string, *bytes.Buffer) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	audit := &bytes.Buffer{}
	br, err := NewBroker(sock, timeout, audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { br.Close() })
	return br, sock, audit
}

func dialAndAsk(t *testing.T, sock string, req Request) Decision {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b, _ := json.Marshal(req)
	conn.Write(append(b, '\n'))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second)) // 測試永不無限阻塞
	var d Decision
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAllowRoundTrip(t *testing.T) {
	br, sock, audit := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"})
	}()
	d := dialAndAsk(t, sock, Request{ID: "r1", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)})
	if d.Behavior != "allow" {
		t.Fatalf("behavior = %s", d.Behavior)
	}
	if !bytes.Contains(audit.Bytes(), []byte(`"r1"`)) {
		t.Fatal("audit missing request id")
	}
}

func TestAllowEchoesUpdatedInput(t *testing.T) {
	br, sock, _ := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"}) // 未帶 UpdatedInput
	}()
	d := dialAndAsk(t, sock, Request{ID: "r2", ToolName: "Bash", Input: json.RawMessage(`{"command":"touch x"}`)})
	if string(d.UpdatedInput) != `{"command":"touch x"}` {
		t.Fatalf("updatedInput not echoed: %s", d.UpdatedInput)
	}
}

func TestDenyCarriesMessage(t *testing.T) {
	br, sock, _ := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "operator denied"})
	}()
	d := dialAndAsk(t, sock, Request{ID: "r3", ToolName: "Bash"})
	if d.Behavior != "deny" || d.Message != "operator denied" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestTimeoutDeniesFailClosed(t *testing.T) {
	_, sock, audit := newTestBroker(t, 50*time.Millisecond)
	d := dialAndAsk(t, sock, Request{ID: "r4", ToolName: "Bash"}) // 無人 Resolve
	if d.Behavior != "deny" {
		t.Fatalf("timeout must deny, got %s", d.Behavior)
	}
	if !bytes.Contains(audit.Bytes(), []byte("timeout")) {
		t.Fatal("audit missing timeout cause")
	}
}

func TestAuditKeepsRawParams(t *testing.T) {
	br, sock, audit := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "n"})
	}()
	dialAndAsk(t, sock, Request{ID: "r5", ToolName: "Bash",
		RawParams: json.RawMessage(`{"name":"approval_prompt","arguments":{"k":"v"}}`)})
	if !bytes.Contains(audit.Bytes(), []byte(`"arguments"`)) {
		t.Fatal("audit must keep raw params verbatim (contract probe)")
	}
}
```

- [ ] **Step 2: 確認失敗** — `go test ./internal/approval/ -v` → FAIL。

- [ ] **Step 3: 完整實作**

```go
package approval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Request struct {
	ID        string          `json:"id"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input,omitempty"`
	RawParams json.RawMessage `json:"raw_params,omitempty"`
}

type Decision struct {
	ID           string          `json:"id"`
	Behavior     string          `json:"behavior"` // allow | deny
	Message      string          `json:"message,omitempty"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

type Broker struct {
	ln      net.Listener
	timeout time.Duration
	audit   io.Writer
	pending chan Request
	mu      sync.Mutex
	waiters map[string]chan Decision
}

func NewBroker(socketPath string, timeout time.Duration, audit io.Writer) (*Broker, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	b := &Broker{ln: ln, timeout: timeout, audit: audit,
		pending: make(chan Request, 16), waiters: map[string]chan Decision{}}
	go b.acceptLoop()
	return b, nil
}

func (b *Broker) Pending() <-chan Request { return b.pending }

func (b *Broker) Resolve(id string, d Decision) error {
	b.mu.Lock()
	ch, ok := b.waiters[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending request %s", id)
	}
	d.ID = id
	ch <- d
	return nil
}

func (b *Broker) Close() error { return b.ln.Close() }

func (b *Broker) log(kind string, v any) {
	rec, _ := json.Marshal(map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind, "data": v})
	fmt.Fprintf(b.audit, "%s\n", rec)
}

func (b *Broker) acceptLoop() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.serve(conn)
	}
}

func (b *Broker) serve(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			b.log("malformed_request", string(sc.Bytes()))
			continue
		}
		b.log("request", req) // 含 RawParams 原文（contract probe）
		ch := make(chan Decision, 1)
		b.mu.Lock()
		b.waiters[req.ID] = ch
		b.mu.Unlock()
		b.pending <- req

		var d Decision
		select {
		case d = <-ch:
		case <-time.After(b.timeout):
			d = Decision{ID: req.ID, Behavior: "deny", Message: "approval timeout (fail closed)"}
			b.log("timeout", req.ID)
		}
		if d.Behavior == "allow" && len(d.UpdatedInput) == 0 {
			d.UpdatedInput = req.Input // 官方 allow 回覆必須含 updatedInput
		}
		b.mu.Lock()
		delete(b.waiters, req.ID)
		b.mu.Unlock()
		b.log("decision", d)
		out, _ := json.Marshal(d)
		conn.Write(append(out, '\n'))
	}
}
```

- [ ] **Step 4: 確認通過** — `go test ./internal/approval/ -v` → PASS（6 測）。
- [ ] **Step 5: Commit** — `git add internal/approval && git commit -m "feat(approval): broker with updatedInput echo, timeout fail-closed, raw-params audit"`

---

### Task 7：MCP approval server 子命令 + stdio E2E（allow / deny-via-broker / broker-down）

**Files:**
- Create: `internal/approval/mcpserver.go`；Modify: `main.go`；Test: `internal/approval/mcpserver_test.go`

**Interfaces:**
- Consumes: Task 6 socket 線協定。
- Produces: `approval.RunMCPServer(socketPath string, stdin io.Reader, stdout io.Writer) error`——stdio MCP server，tool `approval_prompt`（CLI 端全名 `mcp__workbench__approval_prompt`）、inputSchema `{"type":"object","additionalProperties":true}`；handler 把完整 params 原文塞 `Request.RawParams`、best-effort 抽 `tool_name`／`input`，經 socket 取得 Decision，回傳 MCP text content＝只含 CLI schema 欄位的 JSON。socket 不可用／逾時（broker timeout + 5s）→ deny。

- [ ] **Step 1: 寫失敗的 E2E 測試（reader goroutine + select timeout，任何分支都不會永久阻塞）**

```go
package approval

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type rpcFrame struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func lineChan(r io.Reader) <-chan []byte {
	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			ch <- append([]byte(nil), sc.Bytes()...)
		}
	}()
	return ch
}

// readResult 以 channel + timeout 讀指定 id 的 result；逾時或串流關閉都會 fail 而非阻塞。
func readResult(t *testing.T, lines <-chan []byte, wantID float64) json.RawMessage {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed before result")
			}
			var f rpcFrame
			if json.Unmarshal(line, &f) != nil || f.ID == nil {
				continue
			}
			if id, ok := f.ID.(float64); ok && id == wantID {
				if f.Error != nil {
					t.Fatalf("rpc error: %s", f.Error)
				}
				return f.Result
			}
		case <-deadline:
			t.Fatal("timeout waiting rpc result")
		}
	}
}

func runServer(t *testing.T, sock string) (io.Writer, <-chan []byte) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = RunMCPServer(sock, inR, outW) }()
	t.Cleanup(func() { inW.Close() })
	return inW, lineChan(outR)
}

func handshake(t *testing.T, in io.Writer, out <-chan []byte) {
	t.Helper()
	io.WriteString(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`+"\n")
	readResult(t, out, 1)
	io.WriteString(in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
}

func callApproval(t *testing.T, in io.Writer, out <-chan []byte, id float64) (behavior, message string, updated json.RawMessage) {
	t.Helper()
	io.WriteString(in, `{"jsonrpc":"2.0","id":`+strconvID(id)+`,"method":"tools/call","params":{"name":"approval_prompt","arguments":{"tool_name":"Bash","input":{"command":"touch x"}}}}`+"\n")
	res := readResult(t, out, id)
	var call struct {
		Content []struct{ Text string `json:"text"` } `json:"content"`
	}
	if err := json.Unmarshal(res, &call); err != nil || len(call.Content) == 0 {
		t.Fatalf("bad tools/call result: %s", res)
	}
	var reply struct {
		Behavior     string          `json:"behavior"`
		Message      string          `json:"message"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
		ID           string          `json:"id"`
	}
	if err := json.Unmarshal([]byte(call.Content[0].Text), &reply); err != nil {
		t.Fatalf("reply not json: %s", call.Content[0].Text)
	}
	if reply.ID != "" {
		t.Fatal("reply must not leak internal id")
	}
	return reply.Behavior, reply.Message, reply.UpdatedInput
}

func TestE2EAllow(t *testing.T) {
	br, sock, _ := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	io.WriteString(in, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	if res := readResult(t, out, 2); !strings.Contains(string(res), `"approval_prompt"`) {
		t.Fatalf("tools/list missing approval_prompt: %s", res)
	}
	behavior, _, updated := callApproval(t, in, out, 3)
	if behavior != "allow" || len(updated) == 0 {
		t.Fatalf("allow must carry updatedInput; got %s / %s", behavior, updated)
	}
}

func TestE2EDenyViaBroker(t *testing.T) { // 完整 broker 鏈的 deny（本輪修正）
	br, sock, _ := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "operator denied"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	behavior, message, updated := callApproval(t, in, out, 2)
	if behavior != "deny" || !strings.Contains(message, "operator denied") || len(updated) != 0 {
		t.Fatalf("deny via broker wrong: %s / %s / %s", behavior, message, updated)
	}
}

func TestE2EDenyOnBrokerDown(t *testing.T) {
	in, out := runServer(t, "/nonexistent.sock")
	handshake(t, in, out)
	behavior, message, _ := callApproval(t, in, out, 2)
	if behavior != "deny" || !strings.Contains(message, "fail closed") {
		t.Fatalf("broker down must deny fail-closed: %s / %s", behavior, message)
	}
}

func TestE2ERawParamsFullChain(t *testing.T) { // v1.4：initialize → tools/call（含未知巢狀 sentinel）→ socket → broker audit 的結構等價
	br, sock, audit := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "n"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	sent := `{"tool_name":"Bash","input":{"command":"touch x"},"x_sentinel":{"deep":[1,"two"],"note":"probe"}}`
	io.WriteString(in, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"approval_prompt","arguments":`+sent+`}}`+"\n")
	readResult(t, out, 2)
	var got any
	for _, line := range bytes.Split(audit.Bytes(), []byte("\n")) {
		var rec struct {
			Kind string `json:"kind"`
			Data struct {
				RawParams json.RawMessage `json:"raw_params"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Kind == "request" && len(rec.Data.RawParams) > 0 {
			var params struct {
				Arguments any `json:"arguments"`
			}
			if json.Unmarshal(rec.Data.RawParams, &params) == nil {
				got = params.Arguments
			}
		}
	}
	var want any
	_ = json.Unmarshal([]byte(sent), &want)
	if !reflect.DeepEqual(got, want) { // go-sdk 路徑必須保留未知巢狀欄位
		t.Fatalf("raw_params not preserved through full chain:\n got: %v\nwant: %v", got, want)
	}
}
```

（`strconvID` = `strconv.FormatFloat(id, 'f', -1, 64)` 包裝，同檔 helper。）

- [ ] **Step 2: 確認失敗（red）** — `go test ./internal/approval/ -run TestE2E -v` → FAIL（`RunMCPServer` 未定義）。

- [ ] **Step 3: 實作**：首選 `github.com/modelcontextprotocol/go-sdk`（stdio transport 接傳入 stdin/stdout、註冊 `approval_prompt`）；SDK 無法以自訂 reader/writer 驅動或行為不符測試 → 改手寫最小 JSON-RPC 2.0（initialize / notifications/initialized / tools/list / tools/call 四個 method），**以 Step 1 測試為契約**，選擇記入 m0-results。核心 handler 與 socket 轉送：

```go
func handleApproval(sock string, timeout time.Duration, rawParams json.RawMessage) string {
	var p struct {
		Arguments struct {
			ToolName string          `json:"tool_name"`
			Input    json.RawMessage `json:"input"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(rawParams, &p) // best-effort；真實欄位名以 A2 錄流為準
	d := forwardToSocket(sock, Request{ID: newULID(), ToolName: p.Arguments.ToolName,
		Input: p.Arguments.Input, RawParams: rawParams}, timeout)
	reply := struct {
		Behavior     string          `json:"behavior"`
		Message      string          `json:"message,omitempty"`
		UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	}{d.Behavior, d.Message, d.UpdatedInput}
	out, _ := json.Marshal(reply)
	return string(out)
}

func forwardToSocket(sock string, req Request, timeout time.Duration) Decision {
	deny := func(msg string) Decision { return Decision{Behavior: "deny", Message: msg + " (fail closed)"} }
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return deny("approval broker unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return deny("approval broker write failed")
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return deny("approval broker read failed")
	}
	var d Decision
	if json.Unmarshal(line, &d) != nil {
		return deny("approval broker bad reply")
	}
	return d
}
```

`main.go`（完整 dispatch）：

```go
func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-approval" {
		fs := flag.NewFlagSet("mcp-approval", flag.ExitOnError)
		sock := fs.String("socket", "", "broker unix socket path")
		_ = fs.Parse(os.Args[2:])
		if err := approval.RunMCPServer(*sock, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	runWailsApp() // wails init 產生的既有啟動流程包成此函式
}
```

- [ ] **Step 4: 確認通過（green）** — `go test ./internal/approval/ -v` → PASS（9 測）。
- [ ] **Step 5: Commit** — `git add internal/approval main.go && git commit -m "feat(approval): mcp-approval subcommand; e2e allow/deny-via-broker/broker-down"`

---

### Task 8：Codex app-server client（schema-first：framing、handshake、typed 最小方法集、replay、runner 生命週期）

依據（[官方文件](https://learn.chatgpt.com/docs/app-server) + openai/codex app-server README，2026-08-06 查證）：wire 為 JSON-RPC 2.0 語意但**省略 `jsonrpc` 欄位**、stdio 傳輸為 JSONL；handshake 嚴格 `initialize`（clientInfo）→ `initialized` 通知——先送其他請求收「Not initialized」、重複 initialize 收「Already initialized」。**關鍵 wire 形狀（v1.5 對齊官方，第五輪 P0）**：`turn/start` 的 `input` 是 **item 陣列**（如 `[{"type":"text","text":"…"}]`），不是字串；`thread/start` 的 thread ID 在 **`result.thread.id`**，不是 `result.threadId`；`item/started`／`item/completed` 的 params 含 **tagged-union `item` 物件**，映射必須依 `params.item.type` 分流——官方 union 現載 14 型（`userMessage`／`agentMessage`／`plan`／`reasoning`／`commandExecution`／`fileChange`／`mcpToolCall`／`dynamicToolCall`／`collabToolCall`／`webSearch`／`imageView`／`enteredReviewMode`／`exitedReviewMode`／`contextCompaction`，2026-08-06 再查證），**且會持續增長：本計畫列舉一律是 M0 支援子集，完整集合以 schema 產物為準**（v1.6，第六輪 P1）；`turn/completed` 的 params 為 `{turn:{id,status,error}}`。其他方法：`thread/resume`／`thread/fork`、`turn/steer`／`turn/interrupt`、`item/agentMessage/delta`、`turn/diff/updated`。核可為 server→client request：`item/commandExecution/requestApproval`（回覆 `accept`／`acceptForSession`／`decline`／`cancel`／`acceptWithExecpolicyAmendment`）與 `item/fileChange/requestApproval`（回覆 `accept`／`acceptForSession`／`decline`／`cancel`），皆含 threadId／turnId／itemId；**回覆後有 `serverRequest/resolved` 確認通知**（v1.6：官方頁面明載——第五輪誤依 fetch 摘要刪去 `acceptForSession` 並把 resolved 降為 conditional，本輪回復；M0 僅使用 `accept`／`decline` 子集）。account 方法（README 已確認，v1.5 定名）：`account/read`、`account/login/start {"type":"chatgpt"}`（result 含 `loginId`／`authUrl`）、`account/login/completed`＋`account/updated` 通知、`account/login/cancel`、`account/logout`。**方法名集中於 `internal/codex/methods.go` 常數表（以 Task 1 Step 4b schema 產物覆核後填入），測試與實作引用同一常數表**——mapping 在本 task 定案，B3 只做 live 驗證，無循環。

**Files:**
- Create: `internal/codex/{methods.go,rpc.go,mapevent.go,session.go,single.go,probe.go}`、`testdata/fake-codex-appserver.sh`、`testdata/fixtures/codex-handshake.sample.jsonl`
- Test: `internal/codex/{rpc_test.go,mapevent_test.go,session_test.go,replay_test.go,single_test.go,probe_test.go}`
- 引用：`internal/proc`（Task 4 已建立，supervisor 語意直接共用）、`internal/recorder`（`.jsonl` 錄流）

**Interfaces:**
- Produces:
  - `codex.Frame{ID *int64, Method string, Params, Result, Error json.RawMessage}`——marshal 後**不含** `jsonrpc` 欄位。
  - `codex.NewConn(stdin io.Writer, stdout io.Reader) *Conn`；`Conn.Handshake(ctx, clientInfo) error`（initialize → initialized；未完成前 `Call` 其他方法回錯誤）；`Call(ctx, method, params) (json.RawMessage, error)`（ctx select，不阻塞）；`OnServerRequest(func(method string, params json.RawMessage) (any, error))`；`OnNotification(func(method string, params json.RawMessage))`；`OnUnknown(func(raw []byte))`；**session-scoped 錄流（v1.6，取代 v1.5 的 SetRecorder／RecorderErr）**：`Conn.BeginRecording(sink func([]byte) error) error`（已在錄流中回 error）＋ `Conn.StopRecording() error`——c2s／s2c 各以 direction envelope `{"dir":"c2s"|"s2c","frame":{…}}` 包裝後逐行送進 sink（實務上接 `recorder.Line`，錄至 `.workbench/recordings/codex-<case>.jsonl`）；`StopRecording` **原子 detach**：摘除 sink、等待 in-flight callback 完成（sink mutex + in-flight 計數）、回傳**本次錄流** latch 的首個錯誤並重設狀態——Stop 之後的 trailing notification 與下一回合流量**不得**再進已停止的 sink（第六輪 P0：舊 SetRecorder 無 detach，CloseWith 後長駐 Conn 仍會寫入已關閉檔案）。
  - `codex.MapEvent(method string, params json.RawMessage) contract.Event`（**依 method + `params.item.type` 二級分流**；mapping 表見 Step 3）。
  - `codex.StartAppServer(ctx, Config{Binary, CWD string, Env []string, TermGrace time.Duration}) (*Server, error)`；`Server.Conn()`、`Server.Argv()`、`Server.Terminate()`、`Server.Wait() proc.Exit`、`Server.StderrSnapshot() string`（**v1.6：長駐 server 仍在跑時的回合證據**，轉呼叫 `proc.StderrSnapshot`）、`Server.Done() <-chan struct{}`（**v1.7：轉呼叫 `proc.Done`——`ensureAppServer` 的非阻塞死亡判定，不用 OS process probing**）——直接以 `internal/proc` 啟動（Task 4 supervisor 語意：group 啟動、退出即清整組、`Wait` 快取；stdout reader＝`Conn` 讀迴圈，滿足 v1.6 汲取契約）。

- [ ] **Step 1: 手寫 real-wire fixture `testdata/fixtures/codex-handshake.sample.jsonl`**（依 schema；無 `jsonrpc` 欄位；direction envelope）：

```
{"dir":"c2s","frame":{"id":1,"method":"initialize","params":{"clientInfo":{"name":"sdlc-workbench","version":"0.0.1"}}}}
{"dir":"s2c","frame":{"id":1,"result":{}}}
{"dir":"c2s","frame":{"method":"initialized"}}
{"dir":"c2s","frame":{"id":2,"method":"thread/start","params":{}}}
{"dir":"s2c","frame":{"id":2,"result":{"thread":{"id":"t1"}}}}
{"dir":"c2s","frame":{"id":3,"method":"turn/start","params":{"threadId":"t1","input":[{"type":"text","text":"hello"}]}}}
{"dir":"s2c","frame":{"method":"item/started","params":{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"echo hi","status":"inProgress"}}}}
{"dir":"s2c","frame":{"method":"item/completed","params":{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"echo hi","status":"completed"}}}}
{"dir":"s2c","frame":{"method":"item/agentMessage/delta","params":{"threadId":"t1","itemId":"i2","delta":"hi"}}}
{"dir":"s2c","frame":{"method":"item/completed","params":{"threadId":"t1","item":{"id":"i2","type":"agentMessage","text":"hi"}}}}
{"dir":"s2c","frame":{"id":3,"result":{}}}
{"dir":"s2c","frame":{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn1","status":"completed"}}}}
```

（形狀已依官方文件對齊：`input` item 陣列、`result.thread.id`、item tagged union、`turn` 物件——v1.5；payload 細節仍以 schema 為最終依據，執行時若與 schema 不符，**改 fixture 對齊 schema**並記入 m0-results。）

- [ ] **Step 2: 寫失敗測試**。`fakeServer` 為 **Go in-test fake**（in-process pipe、依 `methods.go` 常數回應），並**強制協定紀律**：(a) 任一收到的 frame 含 `jsonrpc` 欄位即回 error；(b) initialize 前任何 request 回「Not initialized」；(c) 未收到 `initialized` 通知前拒絕 `thread/start`。核心測試與斷言：

```go
func TestHandshakeOrderEnforced(t *testing.T)   // 未 Handshake 就 Call → client 狀態機回錯誤；fake 端同樣拒絕（雙保險皆驗）
func TestWireOmitsJSONRPCField(t *testing.T)    // 攔截 client 送出的每一個 raw frame，斷言不含 "jsonrpc" key
func TestTurnStreamMapsToContract(t *testing.T) // Handshake → thread/start（result.thread.id → KindInit）→ turn/start（input=item 陣列）；
                                                // 收 commandExecution item/started+completed（KindToolUse）、agentMessage delta（KindDelta Text=="hi"）、
                                                // item/completed agentMessage（KindMessage）、turn/completed（KindResult !IsError），全部 contract.Valid
func TestMapEventBranchesOnItemType(t *testing.T) // v1.5：item/completed 依 params.item.type 分流——commandExecution→KindToolUse、
                                                  // agentMessage→KindMessage(Text)、reasoning→KindMessage(Thinking=summary)、
                                                  // plan→KindSystemOther、未知 item.type→KindUnknown；全部 contract.Valid
func TestApprovalAllowDenyTimeout(t *testing.T) // fake 發 item/commandExecution/requestApproval（含 threadId/turnId/itemId）三情境：
                                                // handler 回 allow → fake 收到 accept；回 deny → 收到 decline；handler 逾時 → 自動 decline（fail closed）
func TestServerErrorSurfaced(t *testing.T)      // fake 回 error frame（code -32001）→ Call 回傳含 code 的 error
func TestUnknownMethodKeptRaw(t *testing.T)     // 未知通知 → OnUnknown 原文；MapEvent → KindUnknown 且 contract.Valid
func TestRecordingSessionScoped(t *testing.T)   // v1.6：Begin→frame→Stop→trailing notification（不得進已停止 sink）→
                                                // 第二次 Begin（新 sink 只收之後的 frame）；sink 回 error → Stop 回傳該錯誤並重設，
                                                // 第二次錄流的 Stop 回 nil（latch 不跨 session 殘留）
func TestRecordingConcurrentSafe(t *testing.T)  // v1.6：c2s Call 與 s2c notification 並行下錄流、Stop 等待 in-flight callback
                                                // 完成後才返回（go test -race 驗證；Stop 後計數歸零）
```

- [ ] **Step 3: 確認失敗（red）** — `go test ./internal/codex/ -v` → FAIL。**Mapping 表在此定案**（`mapevent.go`；方法名引用 methods.go 常數）：

| wire 事件（v1.5：item 事件依 `params.item.type` 二級分流；**v1.6：本表為 M0 支援子集，非完整官方 enum**——未列型別落 KindUnknown，完整集合以 schema 為準） | contract.Kind |
|---|---|
| `thread/start` result（`result.thread.id`；response 由呼叫端映射） | KindInit（SessionID = thread.id） |
| `item/agentMessage/delta` | KindDelta（Text） |
| `item/started`／`item/completed`，`item.type` ∈ {`commandExecution`, `fileChange`, `mcpToolCall`, `webSearch`} | KindToolUse（raw 保留） |
| `item/completed`，`item.type` = `agentMessage` | KindMessage（Text = item.text） |
| `item/completed`，`item.type` = `userMessage` | KindMessage |
| `item/completed`，`item.type` = `reasoning` | KindMessage（Thinking = summary） |
| `item/started`（非 tool 類）、`item.type` ∈ {`plan`, `contextCompaction`} | KindSystemOther（raw 保留） |
| `item/*`，未知 `item.type` | KindUnknown（raw 保留） |
| `turn/completed`（`turn.status` = failed → IsError） | KindResult |
| `turn/diff/updated` | KindSystemOther |
| 未知 method | KindUnknown（raw 保留） |

- [ ] **Step 4: 實作**：`methods.go`（以 schemas/codex 覆核後填常數：thread／turn／item 方法、`item/commandExecution/requestApproval`／`item/fileChange/requestApproval`、`serverRequest/resolved`、`account/read`／`account/login/start`／`account/login/cancel`／`account/logout` 與通知名——M0 使用子集，常數表如實標注）、`rpc.go`（writer mutex + pending map；讀迴圈分流 response／server request／notification／unknown；Handshake 狀態機；**BeginRecording／StopRecording**：sink mutex + in-flight 計數、Stop 原子 detach 並重設 latch）、`mapevent.go`（上表，item.type 分流）、`session.go`（以 `internal/proc` 啟動 `codex app-server`，supervisor 語意同 Task 4）。

- [ ] **Step 5: Runner 生命週期測試（v1.4 新增；v1.7 補 Done）**：`fake-codex-appserver.sh` 只服務此步（正確 wire、無 jsonrpc 欄位；支援 FAKE_DIE／FAKE_STDERR／FAKE_EXIT／FAKE_ORPHAN）。測試：missing binary（Start 回 error）、mid-stream death（initialize 回應後 exit 7 → conn 錯誤浮現、Wait 取得 code 7、**`Server.Done()` 於死亡後關閉、存活時 select-default 不觸發**——v1.7）、stderr tail、exit code 傳遞、Terminate 整組收掉忽略 SIGTERM 的孫程序（groupDead 斷言，同 Task 4）。

- [ ] **Step 5b: Single ownership 與 HandshakeProbe（v1.8 新增：把 app 層兩個保證抽成可測核心）**

`single.go`——單一長駐 instance 的序列化 ownership（`ensureAppServer` 的可測核心）：

```go
package codex

import "sync"

// Alive 是 Single 管理對象的最小介面（*Server 滿足；測試用 stub）。
type Alive interface{ Done() <-chan struct{} }

// Single 序列化「單一長駐 instance」的取得與重建（v1.8）。
type Single[T Alive] struct {
	mu  sync.Mutex
	cur T
	ok  bool
}

// Ensure：既有 instance 存在且未死（Done 未關閉）→ 直接回傳；否則呼叫 start 重建。
// start 失敗 → 不保留任何 instance；start 內部必須自行清理其失敗的中間產物。
func (s *Single[T]) Ensure(start func() (T, error)) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ok {
		select {
		case <-s.cur.Done(): // 已死：重建
		default:
			return s.cur, nil
		}
	}
	t, err := start()
	if err != nil {
		var zero T
		s.cur, s.ok = zero, false
		return zero, err
	}
	s.cur, s.ok = t, true
	return t, nil
}

// Take 取出並清空 ownership（僅供 app 關閉：取出後立即 Terminate+Wait，無後續回填）；
// 無 instance 時 ok=false。
func (s *Single[T]) Take() (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.cur, s.ok
	var zero T
	s.cur, s.ok = zero, false
	return t, ok
}

// WithExclusive 在同一把 mutex 下執行整段 replacement（v1.9，第九輪 P0）：
// fn 收到目前 instance（可能為空），負責 dispose 舊 instance 與建立新 instance；
// 回傳 (新 instance, keep, err)——keep=true 則回填（err 非 nil 亦保留，供「成功但
// stop/close 有錯」情境）、keep=false 則 ownership 留空。fn 執行期間 Ensure／Take
// 一律阻塞，因此不存在「probe 空窗內另建 server」的競態；fn 內不得呼叫 Single
// 的其他方法（同鎖，會死鎖）。
func (s *Single[T]) WithExclusive(fn func(cur T, ok bool) (T, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, keep, err := fn(s.cur, s.ok)
	if keep {
		s.cur, s.ok = t, true
	} else {
		var zero T
		s.cur, s.ok = zero, false
	}
	return err
}
```

`single_test.go` 核心測試（**第八輪 P1：併發保證必須是實際測試，循序呼叫不足**）：

```go
func TestSingleEnsureConcurrentRestartsOnce(t *testing.T)
// 先以 Ensure 放入一個 Done 已關閉的 stub（已死）；兩個 goroutine 以同一 barrier（close(begin)）
// 同時進入 Ensure，start 以 atomic 計數並回傳同一個新 stub。
// 斷言：start 恰被呼叫 1 次；兩個 goroutine 取得同一個新 instance；-race 下通過。
func TestSingleEnsureReusesAlive(t *testing.T)     // 存活 instance：Ensure 不呼叫 start
func TestSingleEnsureStartFailureLeavesEmpty(t *testing.T) // start 回 error → ownership 空（Take ok=false）
func TestWithExclusiveBlocksEnsure(t *testing.T)   // v1.9：fn 進入後停在 gate；並行 Ensure 斷言「未返回」；
                                                   // 放行 fn 回傳 (B, keep=true) → Ensure 返回 B 且其 start 未被呼叫；ownership 唯一
func TestWithExclusiveReplaceDisposesOld(t *testing.T) // 先有存活 A；fn 收到 A、記錄 Terminate+Wait 後回傳 B；
                                                       // 斷言 A 有 Terminate/Wait、最終 ownership == B、無無主 server
func TestWithExclusiveFailureLeavesEmpty(t *testing.T) // fn 回 keep=false + err → ownership 空、err 透傳
func TestWithExclusiveKeepWithError(t *testing.T)      // keep=true + err（stop/close 失敗但 server 留用）→ ownership == 新 instance、err 透傳
```

`probe.go`——B1 受控重啟 probe 的編排（`RestartCodexServerRecorded` 的可測核心）。為使四階段失敗可注入，編排面向最小介面（`*Server` 以薄委派滿足：`BeginRecording`／`StopRecording` 轉 `Conn`）：

```go
type probeTarget interface {
	Alive
	BeginRecording(sink func([]byte) error) error
	StopRecording() error
	Handshake(ctx context.Context, ci ClientInfo) error
	Terminate() error
	Wait() proc.Exit
	StderrSnapshot() string
	Argv() []string
}

func RunHandshakeProbe[T probeTarget](ctx context.Context, single *Single[T],
	newRec func() (*recorder.Recorder, error), start func() (T, error), ci ClientInfo) error
```

行為（**v1.8 P0：Start 成功後、Handshake 成功前的任何失敗——含 BeginRecording 失敗——一律 Terminate → Wait → ownership 清空，以 Exit 填 meta；v1.9 P0：整段 replacement 是 `single.WithExclusive` 的單一互斥交易**——fn 內依序執行下列步驟，期間任何 `Ensure`（session／login）一律阻塞，不存在「空窗內另建 server、Put 覆寫洩漏」的競態；成功以 keep=true 回填、失敗 keep=false 留空，**不經公開的 Take／Put 組合**）：

1. fn 收到舊 instance（ok=true）→ `Terminate()` + `Wait()`（被替換的每個 server 都有 dispose）。
2. `newRec()` 失敗 → keep=false、回傳 error（尚無新 server）。
3. `start()` 失敗 → `CloseWith`（meta 記錯誤、無 exit_code）→ keep=false、join 回傳。
4. `BeginRecording` 失敗 → **`Terminate()` → `Wait()`（以 `Exit` 填 meta 的 `ExitCode`／`StderrTail`）→ `CloseWith`（meta 記錯誤）**→ keep=false、join 回傳（**不得留下已啟動未 handshake 的 server**）。
5. `Handshake` 失敗 → `StopRecording()` → 同步驟 4 的 dispose → keep=false、join 回傳。
6. 成功 → `StopRecording()` → `CloseWith(Meta{ProcessStillRunning: true, StderrTail: StderrSnapshot(), Argv, …})` → **keep=true 回填新 server**；stop／close 錯誤 join 進回傳值（server 仍留用，對應 `TestWithExclusiveKeepWithError`）。

`probe_test.go`（stub target 記錄 Terminate／Wait／Stop 呼叫；**四階段失敗注入固定資源歸屬**）：

```go
func TestProbeNewRecFails(t *testing.T)       // 無 server 啟動；ownership 空
func TestProbeStartFails(t *testing.T)        // meta 關檔記錯誤；ownership 空；無 Terminate 呼叫（沒有東西可殺）
func TestProbeBeginRecordingFails(t *testing.T) // v1.8 P0：Terminate+Wait 恰各 1 次、meta 含 Exit 與錯誤、ownership 空
func TestProbeHandshakeFails(t *testing.T)    // Stop → Terminate+Wait → meta 含 Exit；ownership 空
func TestProbeSuccess(t *testing.T)           // Stop+CloseWith（process_still_running、無 exit_code）；ownership = 新 server；無 Terminate
func TestProbeExcludesEnsure(t *testing.T)    // v1.9 P0：probe 的 Handshake 停在 gate；並行 Ensure 阻塞（斷言未返回）；
                                              // 放行後 probe 成功 → ownership == probe 的新 server、Ensure 返回同一 instance
                                              // 且其 start 計數 == 0；全程無無主 server（每個被替換／丟棄的 stub 都有 Terminate+Wait）；-race 通過
```

Run: `go test ./internal/codex/ -run 'TestSingle|TestProbe|TestWithExclusive' -race -v` → PASS。

- [ ] **Step 6: Replay 測試（實際執行）**：`replay_test.go` 讀 `testdata/fixtures/codex-*.jsonl` ∪ `.workbench/recordings/codex-*.jsonl`；**committed fixtures 為空即 FAIL**（非 skip）；每行解 direction envelope——`s2c` 有 method 者過 `MapEvent`（unknown 須列 `<case>.allow-unknown`）、有 id + result/error 者驗形狀；`c2s` 的 method 必須在 methods.go 常數集內。

- [ ] **Step 7: 確認通過（green）** — `go test ./internal/codex/ ./internal/claude/ ./internal/proc/ -v` → PASS（proc／claude 為回歸確認）。
- [ ] **Step 8: Commit** — `git add internal/codex testdata/fake-codex-appserver.sh testdata/fixtures/codex-handshake.sample.jsonl && git commit -m "feat(codex): schema-first app-server client with handshake state machine, item-type mapping, replay"`

---
### Task 9：Wails 綁定與最小 UI（雙 provider、Recorder 接線、Terminate、probe ask rule）

**Files:**
- Create/Modify: `app.go`、`frontend/src/App.vue`、`frontend/src/components/{Transcript,ApprovalDialog}.vue`

**Interfaces:**
- Produces: Wails 綁定 `StartSession(provider, prompt, resume, recordCase string) error`、`TerminateSession() error`（claude ＝ `Session.Terminate()` 整組；**codex ＝ `turn/interrupt`——長駐 server 不因單一 session 終止而關閉，v1.5**）、`ResolveApproval(id string, allow bool, reason string) error`、`AuthStatus(provider string) (string, error)`、**`StartLogin(provider string) error`、`Logout(provider string) error`（app 內喚起官方登入）**、**`RestartCodexServerRecorded(recordCase string) error`（v1.6 B1 probe；v1.7 完整生命週期；v1.8 改為薄封裝 `codex.RunHandshakeProbe`——Task 8 Step 5b 的可測核心）**——app 層只組裝參數：`recorder.New(dir, recordCase, ".jsonl")` 的工廠、`StartAppServer` 的 start 函式、`a.codexSingle`（`codex.Single[*codex.Server]`）；生命週期（Begin → Handshake → Stop → CloseWith）、**Start 成功後 Handshake 成功前任何失敗（含 BeginRecording 失敗）一律 Terminate → Wait → ownership 清空並以 Exit 填 meta**（第八輪 P0）、成功後留用為長駐實例、**整段 replacement 在 `Single.WithExclusive` 單一互斥交易內（probe 進行中 `ensureAppServer` 阻塞，不產生無主 server——第九輪 P0）**——全部由 `RunHandshakeProbe` 實作並在 Task 8 以四階段失敗注入＋probe×Ensure barrier 測試固定；錯誤 `errors.Join` 後由綁定回傳並呈現於 UI；Wails events `bridge:event`（contract.Event 摘要 + provider）、`approval:request`（含 provider）、`auth:status`（登入進度／完成）、`session:done`（exitCode、stderrTail、costUsd、**recorderError?**；codex 回合為 `processStillRunning` + stderr snapshot——v1.6）。

- [ ] **Step 1: `app.go`（節錄；檔頭 `// spike quality: to be rebuilt in M1`）** — Claude 路徑：

```go
func (a *App) startClaude(prompt, resume, recordCase string) error {
	cwd, err := claude.NormalizeCWD(a.workspaceDir)
	if err != nil {
		return err
	}
	if resume != "" { // resume mismatch 拒絕
		if bound, ok := a.registry.CWD(resume); !ok || bound != cwd {
			return fmt.Errorf("resume refused: session %s bound to %q, current %q", resume, bound, cwd)
		}
	}
	sock := filepath.Join(a.stateDir, "approval.sock")
	_ = os.Remove(sock)
	br, err := approval.NewBroker(sock, approvalTimeout(), a.auditWriter())
	if err != nil {
		return err
	}
	a.broker = br
	go a.pumpApprovals(br, "claude")

	self, _ := os.Executable()
	if o := os.Getenv("WORKBENCH_MCP_COMMAND_OVERRIDE"); o != "" { // A6 注入點
		self = o
	}
	mcpCfg := filepath.Join(a.stateDir, "mcp.json")
	cfg := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":%q,"args":["mcp-approval","--socket",%q]}}}`, self, sock)
	if err := os.WriteFile(mcpCfg, []byte(cfg), 0o644); err != nil {
		return err
	}
	sess, err := claude.Start(a.ctx, claude.Config{
		Binary: a.claudeCLIPath(), CWD: cwd, Prompt: prompt, Resume: resume,
		MCPConfigPath: mcpCfg, PermissionPromptTool: "mcp__workbench__approval_prompt",
		SettingsJSON: `{"permissions":{"ask":["Bash(touch *)"]}}`, // probe 必問：ask 優先於 allow、所有 mode 有效
	})
	if err != nil {
		return err
	}
	a.claudeSess = sess
	go a.pumpClaude(sess, cwd, recordCase)
	return nil
}

func (a *App) pumpClaude(sess *claude.Session, cwd, recordCase string) {
	var rec *recorder.Recorder
	if recordCase != "" {
		var recErr error
		rec, recErr = recorder.New(filepath.Join(a.stateDir, "recordings"), recordCase, ".ndjson")
		if recErr != nil { // v1.4：Recorder 初始化失敗 = 可見的 session 失敗，不無聲降級
			_ = sess.Terminate()
			sess.Wait()
			runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "claude", "recorderError": recErr.Error()})
			return
		}
	}
	for ev := range sess.Events() {
		if rec != nil {
			if err := rec.Line(ev.Raw); err != nil {
				runtime.EventsEmit(a.ctx, "bridge:event", map[string]any{"kind": "recorder_error", "error": err.Error()})
			}
		}
		if info := claude.ParseInit(ev); info != nil {
			_ = a.registry.Bind(info.SessionID, cwd)
		}
		runtime.EventsEmit(a.ctx, "bridge:event", toUIEvent(ev))
	}
	ex := sess.Wait()
	var recErrText string
	if rec != nil { // v1.4：CloseWith（含底層 close 與 meta 寫入）錯誤進 session:done
		if err := rec.CloseWith(recorder.Meta{Provider: "claude", CLIVersion: a.cliVersion("claude"),
			Argv: sess.Argv(), CWD: cwd, RecordedAt: time.Now().UTC().Format(time.RFC3339),
			ExitCode: &ex.Code, StderrTail: ex.StderrTail}); err != nil { // v1.7：*int，claude 回合必已退出
			recErrText = err.Error()
		}
	}
	a.claudeSess = nil
	runtime.EventsEmit(a.ctx, "session:done", map[string]any{"provider": "claude",
		"exitCode": ex.Code, "stderrTail": ex.StderrTail, "recorderError": recErrText})
}
```

- [ ] **Step 1b: Codex 路徑（`startCodex`，v1.5 改以長駐 server 為前提；步驟數以本節為準）**：

1. **Server ownership（v1.5 定案；v1.7 死亡判定；v1.8 改為可測核心）**：app 持有 `a.codexSingle`（`codex.Single[*codex.Server]`，Task 8 Step 5b）；`ensureAppServer()` ＝ `a.codexSingle.Ensure(start)`，其中 start ＝ `codex.StartAppServer`（managed binary、`internal/proc` supervisor 語意）+ `Conn.Handshake(ctx, clientInfo)`——**start 內 Handshake 失敗必須自行 `Terminate()` + `Wait()` 後回傳 error**（Ensure 契約：start 失敗不保留 instance），session 情境即 session:done 帶錯誤。「未啟動或已死」判定與**併發只重啟一次**由 `Single` 保證並已有實際測試（`TestSingleEnsureConcurrentRestartsOnce`：兩 goroutine 同 barrier 進入、start 計數 == 1、取得同一 instance；第八輪 P1）。**登入（Step 1c）與所有 codex session 重用同一長駐 server**；B1 受控重啟（`RunHandshakeProbe`）進行中，`Ensure` 因同一把 mutex 阻塞至 probe 完成（v1.9）；app 關閉（Wails shutdown hook）→ `Take()`（取出即清空 ownership，無後續回填）→ `Server.Terminate()`（group SIGTERM → grace → SIGKILL）+ `Server.Wait()`。
2. recordCase 非空時 `recorder.New(dir, recordCase, ".jsonl")` 建 Recorder、`Conn.BeginRecording(rec.Line)` 開 direction-envelope tee（`New`／`BeginRecording` 失敗＝可見 session 失敗，同 Claude 路徑）。**注意（v1.6）：一般 session 掛在既有長駐 Conn 上，錄流自 Begin 起算、不含 handshake——完整 handshake 錄流由 B1 的 `RestartCodexServerRecorded` 受控重啟取得（recorder 先於 `Handshake` 安裝）。**
3. `OnNotification` → `MapEvent` → `bridge:event`；`OnServerRequest` 收 `item/commandExecution/requestApproval`／`item/fileChange/requestApproval` → 轉 `contract.ApprovalRequest{Provider: codex, RawParams: params}` 發 `approval:request` → 等 UI `ResolveApproval`：allow → 回 `accept`、deny → 回 `decline`（映射集中一處）；逾時（`WORKBENCH_APPROVAL_TIMEOUT`）→ 自動 `decline`（fail closed）。
4. `Call(thread/start)` → 讀 `result.thread.id`（映 KindInit）。
5. `Call(turn/start, {threadId, input: [{"type":"text","text": prompt}]})`（v1.5：input 為 item 陣列）。
6. `turn/completed`（`turn.status=failed` → IsError）→ session:done；`TerminateSession` → `Call(turn/interrupt)`（**不關長駐 server**；server 本體只在 app 關閉時 Terminate + Wait）。
7. Recorder 收尾（v1.6 順序固定）：先 `stopErr := Conn.StopRecording()`（原子 detach——之後的 trailing notification 不會再寫入），再 `rec.CloseWith(Meta{Provider: "codex", Argv: Server.Argv(), ProcessStillRunning: true, StderrTail: Server.StderrSnapshot(), …})`——長駐 server 不隨回合退出，meta 記 `process_still_running: true` + live stderr snapshot，ExitCode 僅在 server 已退出時填（證據契約調整）；`stopErr` 與 `CloseWith` error **join 後**進 session:done 的 recorderError。

- [ ] **Step 1c: App 內官方登入（v1.5 方法定名 + fallback 狀態回報）**：

- `StartLogin("codex")`：`ensureAppServer()`（Step 1b 的長駐 server，登入後**不重啟**、後續 session 直接重用）→ `Call(account/read)` 判斷現狀 → `Call(account/login/start, {"type":"chatgpt"})` → result 的 `authUrl` 以 `runtime.BrowserOpenURL` 開瀏覽器（`loginId` 留存供取消）→ 等 `account/login/completed`（success／error）與 `account/updated`（authMode）通知轉 `auth:status`；UI 取消 → `Call(account/login/cancel, {loginId})`。
- `AuthStatus("codex")` ＝ `Call(account/read)`；`Logout("codex")` ＝ `Call(account/logout)`（成功會觸發 `account/updated`）。
- `StartLogin("claude")`：先以 pinned CLI `--help`／`auth --help` 探得官方 login 命令（輸出存 `testdata/fixtures/claude-auth-help.txt`），以該命令 spawn，stdout 的 auth URL 與狀態轉 `auth:status`；若 pinned 版本僅支援互動式 login，fallback＝以系統終端機開啟官方命令（仍是官方流程）。**fallback 完成狀態回報（v1.5 補）**：終端機開啟後 app 以 pinned CLI 的狀態查詢命令（`--help` 探得，如 `claude auth status`；無此命令則以一次最小 `-p` probe 的成敗判定）每 5s 輪詢，成功 → `auth:status` 已登入；5 分鐘逾時 → `auth:status` 標 pending／failed；實測行為記入 m0-results。
- `Logout("claude")` 走官方 logout 命令。**app 全程只處理 auth URL、狀態與完成事件，不接收密碼、不讀寫 token。**

- [ ] **Step 2: 前端**（完整內嵌）——`ApprovalDialog.vue`：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ResolveApproval } from '../../wailsjs/go/main/App'
const req = ref<{ id: string; provider: string; toolName: string; inputJson: string } | null>(null)
const reason = ref('')
EventsOn('approval:request', (r) => { req.value = r })
async function decide(allow: boolean) {
  if (!req.value) return
  await ResolveApproval(req.value.id, allow, reason.value)
  req.value = null; reason.value = ''
}
</script>
<template>
  <div v-if="req" class="overlay">
    <h3>[{{ req.provider }}] 工具權限請求：{{ req.toolName }}</h3>
    <pre>{{ req.inputJson }}</pre>
    <input v-model="reason" placeholder="理由（deny 建議填）" />
    <button @click="decide(true)">Allow</button>
    <button @click="decide(false)">Deny</button>
  </div>
</template>
```

`Transcript.vue`：訂閱 `bridge:event`，依 kind 呈現（delta 逐 token 追加、thinking 摺疊、tool_use 卡片、result 顯示 exit / cost、unknown 顯示 raw 摺疊）。`App.vue`：provider 下拉（claude / codex）、prompt 輸入、recordCase 欄、session_id 顯示、resume 欄（僅 claude）、Terminate 按鈕、AuthStatus 顯示、exit code / stderrTail。

- [ ] **Step 3: 手動 smoke** — `scripts/check-cli.sh && wails dev`：claude 選項下 prompt「執行指令 `touch .workbench/probe/smoke.txt`，完成後回覆 done」→ Expected: 逐 token 出字、**必**彈 approval、Allow 後檔案存在、audit 有 request（含 RawParams）與 decision。
- [ ] **Step 4: Commit** — `git add app.go frontend/src && git commit -m "feat(app): dual-provider wiring with recorder, terminate, probe ask-rule (spike quality)"`

---

### Task 10：Mermaid pane 與檔案監看（完整內嵌）

**Files:**
- Create: `frontend/src/components/MermaidPane.vue`、`docs/sample.mmd`；Modify: `app.go`（watcher + `ReadDiagram`）

- [ ] **Step 1: `docs/sample.mmd`**

```
flowchart LR
    A[規格] --> B[Gate 1] --> C[實作]
```

- [ ] **Step 2: `app.go` watcher**

```go
func (a *App) watchDiagram(path string) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	_ = w.Add(filepath.Dir(path))
	go func() {
		for ev := range w.Events {
			if ev.Name == path && ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if b, err := os.ReadFile(path); err == nil {
					runtime.EventsEmit(a.ctx, "diagram:changed", string(b))
				}
			}
		}
	}()
}
```

- [ ] **Step 3: `MermaidPane.vue`**

```vue
<script setup lang="ts">
import mermaid from 'mermaid'
import { ref, onMounted } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
const el = ref<HTMLElement | null>(null)
let n = 0
async function render(src: string) {
  const { svg } = await mermaid.render(`m0-${n++}`, src)
  if (el.value) el.value.innerHTML = svg
}
onMounted(() => {
  mermaid.initialize({ startOnLoad: false })
  EventsOn('diagram:changed', render)
})
</script>
<template><div ref="el" /></template>
```

- [ ] **Step 4: 驗證** — `wails dev` 開 Mermaid 分頁，編輯 `docs/sample.mmd` 加一個節點存檔 → Expected: 1 秒內重渲染。
- [ ] **Step 5: Commit** — `git add app.go frontend/src/components/MermaidPane.vue docs/sample.mmd && git commit -m "feat(ui): mermaid pane with fsnotify live re-render"`

---

### Task 11：驗收矩陣執行（Claude A0–A12、Codex B0–B6、契約 N1、韌性 R1）

**Files:**
- 產出：`.workbench/recordings/*`（不 commit）；`testdata/fixtures/*`（去敏後 commit，Claude 檔名 `claude-*`、Codex 檔名 `codex-*`）；m0-results 結果表。

前置：每 case 前跑 `scripts/check-cli.sh`；網路可用、兩個訂閱帳號存在。**登入本身是驗收項（A0／B0），不是人工前置**；app 全程不觸碰 credential。

**Claude 線（A0–A12）**：

- [ ] **A0 app 內官方登入（v1.4 新增）**：登出狀態下按 UI 的 Login → `StartLogin("claude")` 喚起官方命令、瀏覽器開啟、完成後 `auth:status` 轉已登入；全程 app 未接收密碼／token（檢查 `.workbench/` 與 app 狀態無 credential 檔）；若 pinned CLI 僅互動式 login，記錄 fallback 行為。

- [ ] **A1 串流**：prompt「用三段解釋 recursion」（recordCase=`claude-basic`）→ text／thinking 逐 token；錄流含 stream_event 序列與末行 result。去敏節錄存 `testdata/fixtures/claude-stream-shape.ndjson`。
- [ ] **A2 allow + contract probe**：「執行指令 `touch .workbench/probe/a2.txt`，完成後回覆 done」→ Allow → 檔案**存在**；audit 的 RawParams 去敏後存 `testdata/fixtures/claude-permission-request.sample.json`（typed schema 依據）；decision 行含 updatedInput。
- [ ] **A3 deny**：同上改 `a3.txt` → Deny + reason → 檔案**不存在**、turn 正常收尾。
- [ ] **A4 逾時 fail closed**：`WORKBENCH_APPROVAL_TIMEOUT=5s`、不操作 → 5s 自動 deny、audit 含 timeout、probe 檔不存在。
- [ ] **A5 broker 斷線**：turn 中 `rm .workbench/approval.sock` → 後續請求 deny（broker unavailable）、UI 呈現錯誤、不 hang。
- [ ] **A6 MCP 載入失敗**：`WORKBENCH_MCP_COMMAND_OVERRIDE=/nonexistent/bin` → 30s 內可見失敗；init 的 `mcp_server_errors` 或啟動錯誤呈現於 UI；meta 保存 stderr／exit／argv；不會靜默失效。
- [ ] **A7 decoder 韌性**：`go test ./internal/claude/ -run TestDecode -v` PASS + `testdata/synthetic/malformed.ndjson` 兩案例分類正確（synthetic 不在 replay glob，與 A9 無衝突）。
- [ ] **A8 resume 與 cwd**：A1 後同 cwd resume 問「剛才第二段說了什麼」→ 引用前文、同 session_id；registry 拒絕測試：改 workspace 指向他處 resume → `resume refused`；另用 `scripts/record-claude.sh` 從不同 cwd `--resume` 記錄 CLI 實際行為。
- [ ] **A9 replay**：`go test ./internal/claude/ -run TestContractReplay -v` → fixtures + 本機錄流全過。
- [ ] **A10 Terminate**：長任務中按 Terminate → 5s 內收尾（SIGTERM→必要時 SIGKILL）、exit code 記錄（文件值 143，不同則記實測）、UI 顯示 aborted + stderrTail。
- [ ] **A11 訂閱模式為主 + bare 對照（選測，需成本授權）**：主路徑＝訂閱 login 模式實測（A1 已覆蓋，明記 init 顯示的環境載入）；選測＝`--bare` 無 key（認證失敗以 result 呈現，meta 保存 argv／exit）與 `--bare` + 專用 key（cost 有值）——**執行前逐次取得成本授權**。
- [ ] **A12 版本與 capabilities**：VERSIONS.md + `claude-basic` init 行 capabilities 抄入 m0-results。

**Codex 線（B0–B6）**：

- [ ] **B0 app 內官方登入**：登出狀態下按 Login → `StartLogin("codex")` 走 `account/login/start {"type":"chatgpt"}`（v1.5 定名；官方文件確認）→ 瀏覽器 Sign in with ChatGPT → `account/login/completed`＋`account/updated` 通知轉 `auth:status`；驗證 app 未觸碰 credential（`.workbench/` 與 app 狀態無 token 檔，credential 僅在 codex 官方位置）。
- [ ] **B1 啟動與 handshake**：以 `RestartCodexServerRecorded("codex-handshake")` 受控重啟（v1.6：recorder 先於 `Handshake` 安裝——一般 session 掛長駐 Conn，錄不到 handshake；**v1.7：生命週期固定為 Begin → Handshake → Stop → CloseWith**，成功即關檔）→ 錄流含**完整雙向** `initialize` → `initialized` frame **且不含後續 B2／B3 流量**；meta 記 `process_still_running: true`、無 `exit_code`；違序請求收「Not initialized」的實測記錄一次；重啟後長駐 server 即為本次啟動的 instance，後續 B 案沿用（隨後 B3 的 `BeginRecording("codex-turn")` 成功本身即驗證 B1 已 detach）。
- [ ] **B2 登入狀態查詢**：`AuthStatus("codex")`（= `account/read`）在登入前後回報正確狀態。
- [ ] **B3 turn 串流 live 驗證**：`thread/start` → `turn/start` → `item/agentMessage/delta`／`item/completed`／`turn/completed` 經 Task 8 已定案的 mapping 顯示於同一 Transcript（recordCase=`codex-turn`）；實測與 mapping 表不符處＝修 mapping + fixture 並記入 m0-results（**live 驗證，非設計**）。去敏節錄存 `testdata/fixtures/codex-turn-shape.jsonl`。
- [ ] **B4 核可往返**：觸發 `item/commandExecution/requestApproval`（要求執行 `touch .workbench/probe/b4.txt`）→ 同一 ApprovalDialog（provider=codex）→ Allow（回 accept）檔案存在；Deny（回 decline）檔案不存在且 turn 正常收尾（**官方明載回覆後有 `serverRequest/resolved` 確認通知——錄流存證，欄位以 schema 為準**；v1.6 回復）；逾時自動 decline 驗一次。M0 僅回 accept／decline 子集（`acceptForSession`／`cancel`／execpolicy amendment 不在 M0 範圍，如實記入 m0-results）。
- [ ] **B5 replay**：`go test ./internal/codex/ -run TestReplay -v` → fixtures + 本機 codex 錄流全過（c2s 方法在常數集、s2c unknown 列管、無 malformed）。
- [ ] **B6 session 持續性**：依 pinned 版本能力實測 `thread/resume`（app-server 會話管理），記錄實際行為（能力缺就記缺，不視為 FAIL）。

**契約線（N1）**：

- [ ] **N1 同一 UI 承載雙 provider**：同一 build 內先後跑 A1 與 B3，Transcript／ApprovalDialog／session:done 全走 `contract.Event`／`ApprovalRequest`，UI 無 provider 特判分支（provider 欄位僅作顯示）；fixtures 兩組都通過 `contract.Valid()` 掃描——`testdata/fixtures/claude-*.ndjson` 經 `claude.Decode`、`testdata/fixtures/codex-*.jsonl` 的 s2c frame 經 `codex.MapEvent`（v1.5：兩個 glob 分列，各自為空即 FAIL），全 Valid。

**韌性線（R1，v1.4 新增）**：

- [ ] **R1 Recorder 失敗可見性**：把 `.workbench/recordings/` 換成不可寫（`chmod 500`）後跑一次 recordCase session → Expected: session:done 帶 `recorderError`、UI 呈現、該 case 在矩陣記 FAIL 機制成立；恢復權限後重跑正常。

- [ ] **Commit（只納去敏證據）** — `git add testdata/fixtures docs/spikes && git commit -m "test(m0): dual-provider acceptance evidence (sanitized)"`；recordings 的 sha256 清單寫入 m0-results，原檔留 `.workbench/`。

---

### Task 12：最終驗證 gate（含封裝 app smoke）+ Spike 報告與 go / no-go

**Files:**
- Create: `docs/spikes/m0-results.md`

- [ ] **Step 1: 建置 gate（實際執行）**

```bash
go vet ./...
go test -race ./...
npm --prefix frontend run build
wails build
```

Expected: 四項全過；輸出摘要記入 m0-results。

- [ ] **Step 2: CLI 進 bundle（v1.4 新增）** — `scripts/bundle-clis.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RES="$ROOT/build/bin/sdlc-workbench.app/Contents/Resources/tools"
mkdir -p "$RES"
cp -R "$ROOT/tools/claude-cli" "$ROOT/tools/codex-cli" "$RES/"
echo "bundled: $(du -sh "$RES" | awk '{print $1}')"
```

app 的 CLI 路徑解析順序（`toolsDir()`）：env `WORKBENCH_TOOLS_DIR` → `filepath.Dir(os.Executable())/../Resources/tools`（封裝）→ repo `tools/`（dev fallback，**解析結果一律寫入 audit 與 UI**）。node 為系統前置需求（啟動時檢查並顯示版本）。

- [ ] **Step 3: 隔離 smoke（v1.4 重寫——證明可攜性，不吃 source tree）**

```bash
TMP=$(mktemp -d)
cp -R build/bin/sdlc-workbench.app "$TMP/"
mv tools tools.hidden            # 藏起 source tree 的 CLI，杜絕無聲 fallback
trap 'mv tools.hidden tools' EXIT
( cd "$HOME" && "$TMP/sdlc-workbench.app/Contents/MacOS/sdlc-workbench" & )
```

在隔離啟動的封裝版內：確認 UI 顯示的 CLI 解析路徑位於 `$TMP/…/Resources/tools`（**不是** repo）；claude 跑一次 A2 等級 probe（allow → 檔案存在）、codex 跑一次 B3 等級 turn。Expected: 兩 provider 各完成一個 turn、audit 有記錄、解析路徑證據 + 截圖存證；結束後還原 `tools/`。這證明 bundle 內路徑解析（managed CLI、`os.Executable` 的 mcp-approval 自指）在無 source tree 情況下成立。

- [ ] **Step 4: 依模板撰寫報告**

```markdown
# M0 Spike 結果（YYYY-MM-DD）
## 版本基線
claude / codex exact 版本 + sha256、wails/go/node、claude init capabilities
## 驗收矩陣
| 項 | 結果 | 證據（fixture / digest / meta / 截圖） | 歸因備註 |
|---|---|---|---|
| A0–A12, B0–B6, N1, R1, 隔離封裝 smoke | PASS/FAIL | … | … |
## 協定觀察
- Claude permission request 真實 schema 與 typed 化建議
- Codex app-server：實測方法清單、mapping 表（方法 → contract.Kind）、unknown 列管
- resume / session 持續性、SIGTERM 實測 exit code
## 訂閱與合規
- 兩 provider 官方 login 流程實測；app 未觸碰 credential 的驗證方式與結果
- Claude 訂閱路徑：**目標自用型態已完成技術驗證**；Anthropic 規範對個人 wrapper 的**適用性未獲官方確認，列為已知風險**（不以「合規 ✔」表述）；發布 gating 為條件款（無發布計畫）
## 建置與封裝 gate 輸出
## 失敗歸因與轉向評估
逐 FAIL 歸因；僅 Claude bridge 本身缺口可觸發方案 C；Codex 線獨立評估
## 建議
- [ ] 方案 A 定案 / [ ] 轉方案 C（Claude 線理由）
- [ ] Codex 線 go / no-go
- 回饋 app-plan 下一版的修訂點清單
```

- [ ] **Step 5: 自我檢查**：矩陣每項有可指認證據；FAIL 有歸因；無「應該可以」措辭；`git status` 確認 recordings 未進 git、`tools/` 已還原。
- [ ] **Step 6: Commit + 通知使用者審閱** — `git add docs/spikes scripts/bundle-clis.sh && git commit -m "docs(m0): spike results and per-provider go/no-go"`。

---

## 驗證策略總表

| 層 | 手段 |
|---|---|
| 單元 | `go test -race ./...`（contract、claude、codex（含 handshake 狀態機／wire 純度／item-type 分流／approval 三情境／**session-scoped 錄流：detach、trailing notification、並行 race**）、proc（**supervisor：正常結束與 Terminate 都清整組＋忽略 SIGTERM 的孫程序；大輸出反壓；真 ctx 取消；Wait 任意時點**）、approval（MCP E2E：allow / deny-via-broker / broker-down / **RawParams 全鏈**）、recorder（錯誤傳播、副檔名白名單、**caseName 驗證、並行 Line、ExitCode 雙態（執行中省略／退出 0 保留）**）；proc／codex 另含 **Done() 非阻塞死亡判定、Single 併發 Ensure（barrier 同時進入、start 計數 == 1）、**WithExclusive 原子 replacement（probe×Ensure 互斥、被替換 server 均有 dispose）**、HandshakeProbe 四階段失敗注入**） |
| 協定 contract | **非空** fixtures + 本機錄流 replay，glob provider-scoped（Claude `claude-*` / Codex `codex-*` direction envelope；CLI 升版必跑） |
| 整合（真 CLI） | Task 11 A0–A12 / B0–B6 / N1 / R1，證據＝去敏 fixture + digest + meta（完整 argv/exit/stderr）+ 截圖 |
| 建置與封裝 | Task 12：vet / test -race / frontend build / wails build / **CLI 進 bundle + 隔離 smoke**（source tree 藏起後仍可跑雙 provider） |

## 修訂記錄

### v1.10（2026-08-06）— 第十輪 plan gate APPROVED（狀態標記）

無新 findings；核可綁定 v1.9 快照 `6b3c4331…3dc6`（app v1.10 `4192f95d…841e`）。本版僅更新 header 狀態，任務內容零變更。M0 coding 解除 NO-GO。

### v1.9（2026-08-06）— 依第九輪 plan gate（CHANGES_REQUIRED）修訂

核對快照：M0 v1.8 `e5274f25d8191cec61a3ea7c52a61c5ce41c4746f5351182b49cad0af73fa2d4`、app v1.9 `ca61324e1f81c9c8172ef9b1c92c12cf3ec17683501f29a28da602e70251112d`。

1. **Replacement 原子化（P0）**：v1.8 的 probe 以 `Take()` → 長流程 → `Put()` 組合，兩次持鎖間有空窗——並行 `Ensure` 可建立 server A，probe 成功 `Put(B)` 會覆寫且不終止 A（無主 server 洩漏、單一長駐保證失效）。修法：`Single` 新增 **`WithExclusive(fn)`——整段 replacement（dispose 舊 → probe 六步 → 成功 keep=true 回填／失敗 keep=false 留空）在同一把 mutex 內完成**，fn 期間 `Ensure`／`Take` 一律阻塞；`Put` 自公開 API 移除（不再允許外部以 Take/Put 組合），`Take` 限定 app 關閉（取出即清空、無回填）。新增測試：`TestWithExclusiveBlocksEnsure`／`ReplaceDisposesOld`／`FailureLeavesEmpty`／`KeepWithError`（Single 層）與 `TestProbeExcludesEnsure`（probe×Ensure barrier：ownership 唯一、Ensure 取得同一 instance 且 start 計數 0、被替換的每個 server 均有 Terminate+Wait、-race）（Task 8 Step 5b、Task 9）。

### v1.8（2026-08-06）— 依第八輪 plan gate（CHANGES_REQUIRED）修訂

核對快照：M0 v1.7 `d7a26492732ff0c362e0cf9c52cc0d6479cbbcb2bcf112eda9225cda74a2571e`、app v1.8 `79506a9761a0e7377fcd750767d6bbc092e8e4c6bbbdeab45776dbf0780dd922`。

1. **BeginRecording 失敗的 server 處置（P0）**：v1.7 清理路徑只在 Handshake 失敗時 Terminate + Wait，BeginRecording 失敗會留下已啟動未 handshake 的 server（Done 未關閉 → ensureAppServer 誤判可重用）。裁決：**Start 成功後、Handshake 成功前的任何失敗一律 Terminate → Wait → ownership 清空、以 Exit 填 meta**。probe 編排抽為 `codex.RunHandshakeProbe`（面向 `probeTarget` 最小介面），以 New／Start／BeginRecording／Handshake **四階段失敗注入測試**固定資源歸屬（`TestProbeBeginRecordingFails` 斷言 Terminate/Wait 各恰 1 次、ownership 空）；`RestartCodexServerRecorded` 降為薄封裝（Task 8 Step 5b、Task 9）。
2. **併發單次重啟改為實際測試（P1）**：ownership 抽為 `codex.Single[T Alive]`（mutex + Done 判定 + Ensure／Take／Put），`ensureAppServer` ＝ `Single.Ensure(start)`；新增 `TestSingleEnsureConcurrentRestartsOnce`——兩 goroutine 以 barrier 同時進入 Ensure、start 原子計數 == 1、兩者取得同一新 instance（-race）；另有 alive 重用與 start 失敗 ownership 清空測試。start 內 Handshake 失敗須自行 Terminate + Wait 後回傳 error（Task 8 Step 5b、Task 9）。
3. 附帶：品質分級補列 `internal/proc` 與 `Single`／`RunHandshakeProbe` 為 production seed（v1.5 抽出 proc 時漏列）。

### v1.7（2026-08-06）— 依第七輪 plan gate（CHANGES_REQUIRED）修訂

核對快照：M0 v1.6 `82e4939f02759d8e2c3e6a63a72f0381a01730f82dc2b436aeae141afc595002`、app v1.7 `4652b6e4101b3fca1042b525c3ab4c8161dd170dd2d1a92fa5b22c13bd56f59c`。

1. **B1 probe 完整生命週期（P0）**：`RestartCodexServerRecorded` 補齊 Begin → Handshake → **Stop → CloseWith** 與單一清理路徑——成功即關檔（不續錄 B2／B3、後續 BeginRecording 不受影響）、server 保留為長駐實例、meta 記 `process_still_running`；New／Start／BeginRecording／Handshake 任一步失敗走同一清理（已 Begin 則 Stop、已 New 則 CloseWith 記錯誤；Handshake 失敗即 Terminate + Wait、以 Exit 填 ExitCode、不留用），全部錯誤 `errors.Join` 回傳（Task 9 Interfaces、Task 11 B1）。
2. **ExitCode 型別落實（P0）**：`Meta.ExitCode` 由 `int` 改 **`*int` + omitempty**——非指標 int 會讓 `process_still_running: true` 的 meta 仍輸出 `exit_code:0`，把「尚未退出」誤表示成正常退出。新增 `TestExitCodeOnlyWhenExited`（執行中：無 `exit_code` 欄位；已退出：`exit_code: 0` 必須保留）；pumpClaude／既有測試改傳指標（Task 5、9、Global Constraints）。
3. **非阻塞死亡判定（P1）**：`proc.Done() <-chan struct{}`（supervisor 收尾完成後關閉）與 `Server.Done()` 公開；`ensureAppServer` 的「已死」判定明定為 Done 已關閉（select-default）、以 mutex 序列化、death 後併發呼叫只重啟一次；Task 8 Step 5 補 Done-on-death 測試、Task 4 proc 測試補 Wait 後 Done 已關閉斷言（Task 4、8、9）。

### v1.6（2026-08-06）— 依第六輪 plan gate（CHANGES_REQUIRED）修訂

核對快照：M0 v1.5 `78924dead69c36ec08060b96e53fe18151ccf9a25ff0a8ea24e987ee538644e3`、app v1.6 `49e78225a0fdbbf1792ed9381d11c62f0de2cb9c58016a78920208705401042d`。

1. **stdout 汲取契約收斂（P0）**：v1.5 supervisor 沒有 stdout drainer，「Wait 不依賴汲取」在輸出超過 pipe buffer 時不成立（子程序卡在 write、cmd.Wait 不返回）。裁決＝**收斂契約而非 supervisor spool**（兩個 runner 的 Start 內建 reader，契約由建構滿足）：呼叫端必須並行持續汲取 `Proc.Stdout`；`Wait()` 僅與汲取「完成」無順序依賴。測試改為 reader 已啟動後即刻 Wait，並新增 `TestLargeOutputDoesNotDeadlock`（> pipe buffer）。同輪修正 ctx 取消測試：v1.5 把 `sleep 30` 接在含 `exit 5` 的 script 後不可達，實測到的是正常退出 cleanup——改用不自行退出的獨立 script、等 ready 訊號後 cancel（Task 4、Global Constraints）。
2. **B1 handshake 錄流路徑修正（P0）**：Step 1b 先 `ensureAppServer()`（含 Handshake）才掛 recorder，B1 要求的完整雙向 handshake 錄流依該步驟不可能取得。新增 `RestartCodexServerRecorded(recordCase)` 受控重啟 probe：Terminate + Wait 舊 server → StartAppServer → **Handshake 前** BeginRecording；B1 改走此 probe，一般 session 錄流明示不含 handshake。長駐 server 的回合證據契約同步調整：meta 增 `process_still_running`、`Server.StderrSnapshot()`（`proc.StderrSnapshot` 新增）提供 live stderr，ExitCode 僅在已退出時填（Task 4、8、9、11 B1）。
3. **Codex 錄流 session-scoped 化（P0）**：`SetRecorder`／`RecorderErr` 改為 `BeginRecording`／`StopRecording`——Stop 原子 detach、等待 in-flight callback、回傳該次錄流錯誤並重設 latch；回合收尾順序固定為 Stop → CloseWith，杜絕 trailing notification 寫入已關閉檔案；新增 `TestRecordingSessionScoped`（兩次連續錄流＋中間 trailing notification）與 `TestRecordingConcurrentSafe`（-race）。`Recorder` 增 mutex（c2s／s2c 並行 tee）；`recorder.New` 增 caseName 驗證（合法 basename + provider prefix ↔ ext 一致），原測試 caseName 全數改為合法 prefix（Task 5、8、9）。
4. **官方契約列舉改子集措辭（P1）**：重新查證官方頁面——`acceptForSession` 存在於兩種核可回覆、`serverRequest/resolved` 明載為核可後確認通知、item union 現載 14 型（增 `dynamicToolCall`／`collabToolCall`／`imageView`／`enteredReviewMode`／`exitedReviewMode`）。**第五輪誤依 fetch 摘要刪去 `acceptForSession` 並把 resolved 降為 conditional，本輪回復並更正**；Task 8 依據段與 mapping 表改標「M0 支援子集」，不宣稱完整 enum，完整集合以 schema 產物為準；B4 補 resolved 錄流存證與子集註記（Global Constraints、Task 8、11 B4）。

### v1.5（2026-08-06）— 依第五輪 plan gate（CHANGES_REQUIRED）修訂

核對快照：M0 v1.4 `5f4acba34322763573bcb6a2cf62d9696fcf463425fa6be0856f5a40933e45b6`、app v1.5 `fabc3cc03e4c18d43045f9b9b8410d700e60e1650b7e546c9fa5f73986bc632b`。

1. **Process supervisor 重設計（P0）**：v1.4 的「drain 完才 Wait、group SIGKILL 在 Wait 內」在孫程序持有 stdout 時形成循環等待，連自己的 `TestOrphanDoesNotHangNormalExit` 都過不了；且 `exec.CommandContext` 與 stdin 寫入失敗只 kill 直接子程序。改為：`internal/proc` 移至 Task 4，**背景 wait supervisor** 是唯一收尾路徑（子程序退出 → 立即 group SIGKILL → EOF 保證 → 收 stderr → 快取 Exit）；`Wait()` 任意時點可呼叫、與 stdout 汲取無順序依賴；ctx 取消與 stdin 失敗覆寫為 group 終止；stdout/stderr 改自建 `os.Pipe`；新增 proc 直測三例（Task 4；Task 8 直接引用）。
2. **Codex wire 對齊官方（P0）**：fixture 與 mapping 改為官方形狀（[官方文件](https://learn.chatgpt.com/docs/app-server) 2026-08-06 查證）——`turn/start.input` 為 **item 陣列**、thread ID 在 **`result.thread.id`**、`item/started`／`item/completed` 含 tagged-union `item`；`MapEvent` 依 `params.item.type` 二級分流（commandExecution／fileChange／mcpToolCall／webSearch → KindToolUse、agentMessage／userMessage／reasoning → KindMessage、plan／contextCompaction → KindSystemOther、未知 → KindUnknown），不再一律 KindToolUse；核可方法定名 `item/commandExecution/requestApproval`／`item/fileChange/requestApproval`（回覆 accept／decline／cancel／acceptWithExecpolicyAmendment）；新增 `TestMapEventBranchesOnItemType`（Task 8、11 B4）。
3. **Replay／Recorder provider 隔離閉合（P0）**：清除 Task 5 殘留的未分 provider 舊文（介面資料源改 `claude-*.ndjson`、Step 4 刪「0 檔掃描」）；N1 掃描補 `codex-*.jsonl`（兩 glob 分列、各自為空即 FAIL）；`recorder.New` 增副檔名參數（claude `.ndjson`／codex `.jsonl`，白名單驗證）；`Conn.SetRecorder` 改 `func([]byte) error` + `RecorderErr()` latch，Codex 的 Line 寫入錯誤與 CloseWith 錯誤 join 進 session:done recorderError；另修 recorder import 漏 `errors`／`fmt`（Task 5、8、9、11）。
4. **登入與 Codex server ownership 補全（P1）**：account 方法定名（openai/codex app-server README 查證）——`account/read`、`account/login/start {"type":"chatgpt"}`（result 含 loginId／authUrl）、`account/login/completed`＋`account/updated` 通知、`account/login/cancel`、`account/logout`；Task 9 Step 1b 重寫：**單一長駐 app-server**、`ensureAppServer()`、登入與 session 重用同一 server、app 關閉 Terminate + Wait、`TerminateSession(codex)` = `turn/interrupt`；Claude 系統終端機 fallback 補輪詢回報完成狀態（5s 輪詢／5 分鐘逾時）（Global Constraints、Task 9、11 B0／B2）。
5. **引用修正**：Global Constraints 的 developers.openai.com 舊址改 learn.chatgpt.com。app plan 正文舊狀態（第五輪 P1 第 5 項）由 app plan v1.6 處理。

1. **Codex 線改 schema-first、拆解循環**：pin 後即 `codex app-server generate-json-schema` 產 schema + digest（Task 1 Step 4b）；方法名集中 `methods.go` 常數表；mapping 表在 Task 8 定案、B3 降為 live 驗證；fixture 依官方 wire（**無 `jsonrpc` 欄位**）手寫；Go in-test fake 強制 initialize → initialized 順序與 wire 純度；Task 9 Codex 路徑具體化為六步 + approval accept／decline 映射（Task 8、9、11）。
2. **Process tree 終止**：兩 runner 以 `Setpgid` 獨立 group、信號作用整組（SIGTERM → grace → SIGKILL）；`Wait` 收尾時 group SIGKILL 清殘存孫程序並同步等 stderr reader；新增忽略 SIGTERM 孫程序、正常結束殘留、scanner 超長行（`MaxLineBytes` + `KindStreamError`）測試；共用邏輯抽 `internal/proc`，Codex runner 補 missing-binary／mid-stream death／stderr／exit／terminate 測試（Task 4、8）。
3. **Replay 非空 + provider 隔離**：committed fixture 為空即 FAIL；glob 改 `claude-*`／`codex-*`（Codex 錄流不再流入 claude.Decode）；Task 5 先 commit 最小 claude fixture；Codex 錄流採 `{"dir","frame"}` direction envelope 並有實際 replay 測試（Task 5、8）。
4. **封裝可攜性**：`bundle-clis.sh` 把兩套 managed CLI 複製進 `.app/Contents/Resources/tools`；解析順序 env → bundle → dev fallback（解析結果寫入 audit 與 UI）；smoke 改為 .app 複製到暫存目錄、非 repo cwd 啟動、`tools/` 藏起後驗證解析路徑在 bundle 內（Task 12）。
5. **App 內官方登入**：新增 `StartLogin`／`Logout` 綁定與 `auth:status` 事件——Codex 走 account 登入方法（schema 確認，預期 `account/login/start`）、Claude 以 pinned CLI 官方 auth 命令喚起（互動式則系統終端機 fallback）；A0／B0 成為驗收項、登入不再是人工前置（Task 9、11）。
6. **Recorder 錯誤全路徑**：`CloseWith` 回傳 close／meta／latched 錯誤（meta 仍盡力寫）；app 對 `New`／`CloseWith` 失敗發可見的 session 失敗（session:done 帶 recorderError）；新增 R1 驗收（Task 5、9、11）。
7. **RawParams 全鏈 E2E**：initialize → tools/call（含未知巢狀 sentinel）→ socket → broker audit 的結構等價斷言（`TestE2ERawParamsFullChain`，Task 7）。
8. **合規措辭修正**：自用是 scope 決策與風險承擔、非合規確認；規範未明確核可個人 wrapper、適用性未獲確認列已知風險；報告模板不用「合規 ✔」（Global Constraints、Task 12）。

### v1.3（2026-08-06）— 使用者定位確認

app 確認為**個人自用工具**：Global Constraints 的 Claude 合規段改以自用為基準情境（M0 驗證的就是目標使用型態）、發布 gating 轉為條件款；Task 12 報告模板同步改寫。驗收矩陣與任務內容不變。

### v1.2（2026-08-06）— 依第三輪 plan gate（CHANGES_REQUIRED）與新需求修訂

1. **納入新需求（訂閱帳號、Claude + Codex）**：M0 拆成 Claude 線與 Codex 線兩條驗證線；新增 `internal/contract` provider-neutral event contract（Task 2）與 N1 驗收；帳號一律官方瀏覽器 OAuth／device flow，app 不收密碼、不保管 token（Global Constraints、Task 9、11）。
2. **合規不對稱如實編碼**：Codex app-server 為官方第三方整合介面（含 Sign in with ChatGPT）；Claude 訂閱路徑標注「取得 Anthropic 書面核准後才可發布」、M0 僅限本人本機自用驗證、BAT 不構成合規先例（Global Constraints、Task 12 報告模板）。
3. **文件自足化**：v1.1 七處「同 v1」全部以完整內容內嵌（decoder 型別與實作、session 旗標組裝、Recorder、broker 全部測試與實作、main.go dispatch、ApprovalDialog.vue、整個 Mermaid task）；新增自足性聲明（header）。
4. **驗證缺口補齊**：MCP deny 新增走完整 broker 鏈的 E2E（`TestE2EDenyViaBroker`）；`readResult` 改 reader goroutine + select timeout，任何分支不永久阻塞；runner 新增 binary-not-found 與 die-mid-stream 失敗路徑測試；Recorder `Line` 回傳並 latch 錯誤、meta 記**完整 argv**（session.Argv()）；record script argv 以同一陣列寫入 meta；新增**封裝後 app 的雙 provider session smoke**（Task 12 Step 2）。
5. Codex 線方法名不寫死：JSON-RPC framing 以 fake server 單元測試（correlation、server→client request、notification、timeout、unknown），實際方法與 mapping 表由 B 線錄流定案（contract-probe 原則沿用）。

### v1.1（2026-08-05）— 依第二輪 plan gate 修訂

CLI exact pin（≥ 2.1.219）＋分歧實證；permission contract probe（RawParams、updatedInput echo）；MCP stdio E2E red/green；Recorder 接線、A6 注入點、TerminateSession、synthetic 移出 contract glob、probe 改 `touch` + ask rule；runner stderr／KindStreamError／grace kill／registry；錄流衛生（.workbench、禁 `git add -A`）；最終 gate 實際執行；executor 標注。

### v1（2026-08-05）

初稿：10 task 與 A1–A12 驗收矩陣。

## Self-review 記錄（撰寫時自查）

- 覆蓋檢查：第九輪 1 項 P0 ↔ 修訂記錄 v1.9（WithExclusive 原子 replacement + probe×Ensure barrier 測試）。
- 資源歸屬檢查（v1.8 補；v1.9 原子化）：probe 六步全程持鎖，無 Take→Put 空窗；每個失敗點的 server 歸屬明確——Start 前失敗無 server；Start 後 Handshake 前失敗（含 Begin）一律 dispose + keep=false；成功 keep=true 回填。`Single.Ensure` 的 start 契約（失敗自清）與 probe 步驟 4／5 一致；`Take` 僅存在於 app 關閉路徑。
- 死鎖檢查（v1.9 補）：WithExclusive 的 fn 明文禁止呼叫 Single 其他方法；probe fn 內只操作 server／recorder，不觸碰 Single。
- 錄流生命週期（v1.7 補）：B1 probe＝Begin → Handshake → Stop → CloseWith（成功即關檔、失敗同一清理路徑）；一般 session＝Begin → 流量 → Stop → CloseWith（turn/completed 或 Terminate 後）；兩者互不干擾（B3 的 Begin 成功即證明 B1 已 detach）。
- 死鎖檢查：汲取契約明示「reader 並行、Wait 不等汲取完成」；`TestNormalExitReapsOrphanAndCachesExit` 為 reader 已啟動即刻 Wait，`TestLargeOutputDoesNotDeadlock` 覆蓋反壓；ctx 取消測試等 ready 訊號、script 不自行退出。
- 錄流生命週期檢查：Begin → 流量 → Stop（detach + 等 in-flight + 回傳錯誤 + 重設）→ CloseWith 順序固定；B1 handshake 錄流走受控重啟 probe，一般 session 明示不含 handshake；caseName 驗證擋路徑逃逸與 glob 掃不到的檔名。
- 循環檢查：mapping 表（含 item.type 分流）定案於 Task 8 Step 3，B3 僅 live 驗證——無「先顯示才定義」循環；Codex 相關測試全部在 Task 9 之前。
- 型別一致性：`contract.*`、`claude.Config/Session/Registry`、`proc.Config/Proc/Exit/StderrSnapshot/Done`（claude／codex 共用 `Wait() proc.Exit`）、`codex.Frame/Conn（BeginRecording/StopRecording）/Server（Done/StderrSnapshot）/MapEvent`、`approval.Request/Decision`、`recorder.New(dir, case, ext)/Meta（ExitCode *int、ProcessStillRunning）` 在各 task 間簽名一致；`StartSession(provider, prompt, resume, recordCase)` 與 A／B 線 recordCase 用法一致（`claude-*`／`codex-*` 命名同時被 recorder 驗證與 replay glob 依賴）；A0–A12／B0–B6／N1／R1 與 Task 12 報告模板編號一致。
- 子集原則：Task 8 依據段、mapping 表、B4 均標注 M0 支援子集；完整 item union／核可回覆／方法集以 schema 產物為準，未支援型別落 KindUnknown（raw 保留）。
- 已知留白（刻意，非 placeholder）：方法名已依官方文件定名，仍以 pinned 版本 schema 產物覆核（Task 1 Step 4b；Task 7／8 測試為契約）；MCP go-sdk 掛接可換手寫 JSON-RPC；`newULID`、`toUIEvent`、`approvalTimeout`、`strconvID`、`runWailsApp`、`toolsDir`、`ensureAppServer` 為 <15 行機械 helper，由執行者依測試補齊。
