# sdlc-workbench M1 MVP 實作計畫

> 版本：v13（2026-08-10）
> 狀態：**草案——依第十二輪 plan gate（CHANGES_REQUIRED，1 P1）修訂；審核核可前 M1 coding NO-GO**
> 自足性聲明（第三輪 P1-1）：**本檔為唯一執行依據，所有 task 的測試、介面與實作內容完整內嵌，不引用歷史版本或對話紀錄。**
> 上游文件：`sdlc-workbench-app-plan.md` v1.11 §7 M1、`docs/spikes/m0-results.md`（M0 定稿）
> 執行 harness：Claude Code executor（superpowers:executing-plans 或 subagent-driven-development）；非 Claude 環境依 checkbox 步驟執行。
> 基線：repo `ai-software-engineering` main @ `05415b9`；M0 `internal/*` 為 production seed 沿用；`app.go`／`frontend/` 為重建對象。
> 審核快照：v1 `ecb78039…77d2c8`、v2 `f287cf4d…9f6e69`、v3 `36692861…801cd4`、v4 `082c4f89…e70922`、v5 `e7604669…e8b781`、v6.1 `e8c3f528…d3bd95`、v7 `1a54a90b…73eece`、v8.1 `5fdc7fba…ad27a3`、v9 `3f43dc84…be68e13`、v10 `c76338c9…d38d85`、v11 `be280444…96bb9`、v12 `44e62c91…2c8d36`。

**Goal**：把 M0 spike 升級為可日常使用的 MVP——多輪問答面板、執行時間軸、檔案樹與 Markdown/Mermaid 預覽、事件 schema v1 與稽核 JSONL、minimal task identity；達成 SC2（任一 session 隨時可回答：在哪個任務、剛做了什麼工具呼叫、卡在哪、花了多少 token/成本）。

**Architecture**：三層不變（Vue 3 webview ↔ Wails 綁定 ↔ Go host）。M1 新增：`internal/contract` 的 **Envelope v1**（event_id/ts/role/task_id/usage/state）與 **state reducer**；`internal/appcore.Manager` 為唯一序列化事件入口，內建 **submission coordinator**（第三輪 P1-2：submit-pending 期間 buffer provider 事件，接受後 user envelope 先行再依序 flush）與 **RecordingLease**（第三輪 P1-3：sync.Once 收尾 ownership，多來源併發觸發各恰一次）。claude 長駐多輪由 Task 4 probe 裁決（FAIL → Task 4b `ResumeTurns`）；codex `ThreadRunner`（early-completed latch）。前端 Pinia store + 元件化。

**Tech Stack**：M0 既有（Go 1.22+、Wails v2、Vue 3 + TS + Vite、mermaid、fsnotify、MCP go-sdk）+ Pinia、vitest、marked + DOMPurify。CLI pin 不變（claude 2.1.223、codex 0.146.1）。

## Global Constraints

- **事件契約 additive-only**：M0 kinds 全保留；M1 新增 `usage`、`state_change`、`approval_decision` kind 與 `role`／`usage`／`state`／`task_id` 欄位。
- **Role 語意凍結**：`Event.Role` 由 adapter 明確標注——`user`（僅 host canonical user message）、`assistant`（模型輸出）、`tool`（provider echo：claude `type:"user"`、codex `item.type:"userMessage"`）、`system`（其餘）。Chat 只渲染 `user`／`assistant`；`tool` 只進 timeline。
- **Submit 順序凍結（第四輪 P1-1/P1-2 強化）**：submit 全程由 `Manager` coordinator 管制且具**唯一 ownership**——`BeginSubmit()` 回傳 `SubmissionID`，已有 owner 時回 `ErrSubmitActive`；`AcceptSubmit`／`RejectSubmit` 必須攜帶匹配 ID，`NewSession` 遞增 generation 使舊 ID 全部失效（stale 呼叫回 error、no-op）。pending 期間**所有 turn-scoped 事件——含 approval request/decision 及其 reducer side effect——一律進同一 queue**，接受後依 `user → state_change(waiting) → 佇列事件（含各自 state 轉移）` 順序 flush；拒絕不發 user。任何 goroutine 交錯（early-completed、early-approval、並發 submit）都不可反轉順序或跨 session 汙染。
- **Usage 語意凍結（第四輪 P2-1 補 UI 契約）**：Manager 輸出 session 累計 snapshot、前端只覆寫；codex `tokenUsage.total` 覆寫；claude 語意由 Task 4 VERDICT 決定（per-turn → 累加；cumulative／inconclusive → 覆寫）。Envelope 增 `usage_semantics` 欄位（`session_total`＝本 session 累加值｜`provider_latest`＝provider 最新回報值），store 記錄、StatusBar 於 `provider_latest` 時在 tokens 旁標 `*`＋tooltip「provider 最新回報值」；codex cost 顯示 `—`。
- **Codex 錄流 lifecycle 凍結（第三輪 P1-3 強化）**：recordCase 為 session-scoped——session 啟動 attach、跨輪保持；收尾（EndSession／new session／shutdown／fatal）一律經 **`RecordingLease.Finalize(ex ports.Exit)`**：Stop 恰一次、CloseWith 恰一次、首個錯誤保留、後續呼叫冪等（barrier + `-race` 測試證明）。**四來源的 Exit 語意（第七輪 P2）**：claude＝`CloseSequence` 回傳的 ex（正常/Terminate 皆 `Exited:true`；pump 卡死 `Exited:false` → meta ExitCode nil）；codex＝一律 `Exit{Exited:false}`（長駐 server 續跑，meta 用 ProcessStillRunning＋StderrSnapshot，不填 ExitCode）。turn/completed 只解 busy、不動 recorder。
- **事件序列化與稽核 fail-loud（第四輪 P1-4 強化）**：Manager 單一 mutex 入口（wrap→totals→sink→emit→state_change 同鎖）；sink 錯誤 latch 後**先 emit 原 envelope、再 emit 較大 ID 的 `stream_error` 合成事件**（不回寫 sink），event_id 輸出序在 fail-loud 路徑同樣嚴格遞增；`Manager.Close()` 走同一 mutex、設 closed 旗標——close 後 `Emit` 不寫 sink、改發「event after close dropped」`stream_error` 至 UI（fail loud，不無聲丟棄）。
- **Session 汰換契約（第六輪 P1-2/P1-3 強化）**：lifecycle 為單一 mutex 下的 phase 狀態機（idle→starting→active→ending→idle）＋ **session generation token**——`BeginNewSessionSubmit(taskID)` 是 StartSession 的原子交易（併發 StartSession 恰一個取得 ownership，輸家在建立 process／recorder／pump 前失敗）；`BeginEndSession()`／`FinishEndSession(token)` 是 EndSession 的兩段式收尾（starting 期間 End 回 `ErrStartInProgress` 不得無聲成功；stale token 的 Finish 為 no-op error，舊 End 不可能清掉新 session）。`EndSession` 對 `ErrNoSession` 冪等回 nil；claude 收尾走 `CloseSequence`（`Close → WaitQuiesce(5s，逾時 Terminate + killTimeout 第二上限) → Wait() → Finalize(ex)`——quiesce timeout 保留於回傳錯誤、卡死於界限內回錯並盡力 finalize）；UI「New」＝`await EndSession()` 成功後才 reset，真實錯誤原樣顯示、不 reset。quiesce 完成先於換代。app shutdown：EndSession 流程 → `Manager.Close()`（pending queue 由 abort+flush 兜底）。
- **多輪 fallback 規則**：Task 4 嚴格序 probe（`run() error` 結構化清理、自然收尾驗證、錄流寫入錯誤全檢）裁決；FAIL → Task 4b `ResumeTurns`（前輪事件流 EOF + `Wait()` 完成後才允許下一輪）。
- **Codex 回合串行**：`turn/start` response 立即回 `turn{id,status:"inProgress"}`（M0 錄流實據）；active turn id 匹配解鎖；completed 先到以 early-completed latch 對消；response 缺 turn id 一律 error。
- **FileTree 安全**：EvalSymlinks canonical 邊界（symlink 檔／目錄逃逸拒絕）；marked+DOMPurify（sanitizer 單元測試）；Stat 前置 1MB + LimitReader。
- **品質分級**：`internal/*` production seed（有測試）；frontend store/reducer 有 vitest；`// spike quality` 標註全移除。
- **架構慣例（go-ddd-adapters 對齊，2026-08-10 使用者補充；詳見「架構參考」節）**：provider 抽象定義於 `internal/ports`（appcore 不 import provider adapter）；新 package 採 pinned-semantics godoc（error precedence／lifecycle／併發契約凍結於 doc）；可判別條件一律 `Err*` sentinel（`errors.Is` 可判、訊息帶 package prefix）。
- **錄流衛生同 M0**：`.workbench/` 絕不 commit；fixtures 去敏；禁 `git add -A`。
- **測試穩定性**：gate 跑 `-count=1`；`TestTerminateKillsProcessGroup` 負載敏感失敗依 m0-results 處置，不靜默重跑。
- 最終 gate 實際執行：`go vet ./...`、`go test -race ./... -count=1`、`npm --prefix frontend run test`、`npm --prefix frontend run build`、`wails build`、`scripts/bundle-clis.sh`、V0–V6；**舊型別 grep gate：`grep -rn 'ClaudeTurns' internal/ cmd/ app.go` 必須為 0**；**clean working tree gate：全部 commit 後 `git status --short` 必須為空**（第七輪 P1-4——防 internal/ports 等新檔遺留 working tree）。`internal/claude/multiturn*.go` 僅 Task 4b 觸發時存在並執行。

## 架構參考：go-ddd-adapters 對齊（2026-08-10 使用者補充）

參考專案：`~/playground/project/go-ddd-adapters`（+ 同 workspace 的 `go-ddd-core`）——實作已久的
ports & adapters 架構。對照現況：workbench 已隱性同構（`internal/contract`≈技術中立契約、
`internal/claude`／`internal/codex`≈per-technology adapter、`internal/appcore`≈application 層、
`internal/proc`／`recorder`／`approval`≈infrastructure），M1 不做整層重構，採用以下慣例：

**M1 採用（低成本、實質收益）**：

1. **Ports 依賴方向**：新增 `internal/ports/turns.go`——多輪 provider 抽象自 `internal/claude`
   移出為 `ports.Turns`（原 `ClaudeTurns` 更名；`*claude.Session`／`*claude.ResumeTurns` 滿足，
   附編譯期斷言）。`appcore` 只 import `ports`＋`contract`，不 import provider adapter——
   對齊 go-ddd「consumer 依賴契約、不依賴實作」的方向。Task 3／4b／6 的介面引用同步改為
   `ports.Turns`，內容不變。
2. **Pinned-semantics godoc（go-ddd-core ports 風格）**：M1 新 package（`ports`、`appcore`）與新
   介面的 package／type doc 必須凍結語意——error precedence、lifecycle、併發契約、冪等性，
   如 `ports/cache` 對 TTL 與 error 先後順序的寫法。計畫中散落的行為規則（coordinator 順序、
   lease 冪等、closed 行為）落 code 時收斂進 godoc，成為單一權威。
3. **Sentinel error 慣例（明文化，現況已符合）**：可判別條件用 `Err*` sentinel（訊息帶
   `package:` prefix；`errors.Is` 可判），如 `ErrTurnActive`／`ErrSubmitActive`／`ErrMiss` 同風格；
   不引入字串比對。
4. **Contract-suite 思路（已具現）**：M0 的 replay／fixtures gate 即 go-ddd `*test.RunContract`
   模式在 wire 層的對應；M1 維持「committed fixtures 非空即 gate」紀律。

**M2+ 再評估（本輪不做，避免 scope 膨脹）**：

- `errorsx` 式 coded error taxonomy（CodeUnavailable／CodeInvalidArgument、never CodeUnknown）——
  workbench 目前是單一 app、無跨服務邊界，等 forge adapter（M4）或對外介面出現再引入。
- provider 抽象的共用 `turnstest.RunContract` suite——claude／codex 的 turn lifecycle 尚不對稱
  （codex 無 per-process 收尾），等 M2 兩側穩定後評估統一。
- core／adapters 式 repo 拆分與 release gate——單 app 無此需求。

## UI 設計參考：BAT 與 VS Code（2026-08-10 使用者補充）

前端（Task 7–11）的互動與資訊架構參考兩個成熟實作，減少採坑；**參考的是設計決策與
互動細節，不逐行移植 code**（BAT 為 React+TS、MIT 授權，本專案 Vue 3；app-plan §8.6
「不 fork」立場不變）。BAT source 位於 `~/playground/external/better-agent-terminal`，
**參考基準 pin 於 commit `72dc4ba`**（第八輪 P2）——執行期間若 checkout 漂移，重新確認
對應行為並記入 m1-results。

**Normative（已同步進本計畫的 code／測試／驗收，具強制力）**：

| 行為 | 來源慣例 | 落點 |
|---|---|---|
| Chat 上捲停止自動跟隨、回底或送出恢復 | BAT `ChatMarkdown.tsx` | Task 8 ChatPanel `follow` 邏輯＋`lib/scroll.ts` 測試＋V3 |
| Timeline tool 卡片＝工具名＋參數節錄（雙 provider 強制；codex per-type 摘要：command／`server.tool(args)`／query／paths，wire 欄位缺失時型別名 fallback 不記 FAIL）＋狀態（**best-effort**：codex `item.status` wire 有提供才顯示；claude 無狀態欄位、如實省略） | BAT `AgentToolRow.tsx` | Task 2 adapter per-type 摘要＋Task 9 `summary()`＋V3 |
| Timeline 可摺疊（toggle） | VS Code panel | Task 11 App.vue `timelineOpen`＋V3 |

**Non-normative（視覺與互動靈感；不驗收、缺項不算 FAIL）**：

| M1 元件 | 參考（pin 72dc4ba 的精確位置） | 借的設計決策 |
|---|---|---|
| ChatPanel 渲染 | `renderer/src/components/ChatMarkdown.tsx` | streaming 游標、thinking 摺疊預設、Markdown 程式碼區塊處理 |
| Timeline 展開互動 | `AgentToolRow.tsx`、`AgentActivityTree.tsx` | 展開看原始輸出的互動 |
| 摺疊條外觀 | `CollapsedBar.tsx`（通用 panel 摺疊條——**僅參考外觀與點擊手勢**；連續雜訊分組是本計畫自有設計，store group 邏輯已有測試） | 摺疊條視覺 |
| StatusBar 資訊密度 | **`ClaudeAgentPanel.tsx` 內的 statusline 實作**（設定定義在 `renderer/src/types/index.ts`；app-plan §4 點名為基準） | 四問一列的欄位密度與縮寫格式 |
| ApprovalDialog（沿用 M0） | **`ClaudeAgentPanel.tsx` 內的 permission UI**（`AskUserQuestion.*` 為問答卡、非工具核可——僅參考問答呈現） | 權限請求的參數呈現層級（指令醒目、參數摺疊） |
| FileTree | `FileTree.tsx` | 懶載入展開、單擊預覽節奏 |
| 佈局／視覺 | VS Code（sidebar/editor/panel/status bar 四區、單擊預覽、dark 層次） | 三欄佈局原型、檔案樹互動、視覺層次 |

**移至 M2 backlog（本輪明確不做）**：Timeline 拖高與高度記憶、`Cmd+K` 等快捷鍵、
檔案樹斜體 preview-tab 語意。

執行規則：normative 三項以本計畫內嵌 code 與驗收為準（已無衝突）；non-normative 參考
於實作時查閱 pin 版位置，取捨自由、缺項不記 FAIL。

## 檔案結構（決策鎖定）

```
internal/contract/
├── event.go（修改：Role/Usage 欄位、三個新 kind）
├── envelope.go / envelope_test.go     # Task 1
├── state.go / state_test.go           # Task 1
internal/ports/
├── turns.go                           # Task 3：ports.Turns（多輪 provider 抽象；go-ddd 對齊）
internal/claude/
├── decode.go（修改）/ decode_test.go   # Task 2：usage + Role
├── session.go（修改）/ session_test.go # Task 3：多輪 Send/Close（實作 ports.Turns）
├── multiturn.go / multiturn_test.go   # Task 4b（條件）
internal/codex/
├── mapevent.go（修改）/ mapevent_test.go # Task 2
├── turns.go / turns_test.go           # Task 5
internal/appcore/
├── sink.go                            # Task 6：AuditSink + JSONLSink
├── recording.go / recording_test.go   # Task 6：RecordingLease（P1-3）
├── manager.go / manager_test.go       # Task 6：Manager + coordinator（P1-2）
cmd/probe-multiturn/main.go            # Task 4
app.go（重寫）/ app_test.go             # Task 6／10
frontend/src/
├── types.ts / stores/session.ts / stores/session.test.ts / vitest.config.ts  # Task 7
├── lib/markdown.ts / lib/markdown.test.ts                                     # Task 10
├── components/ChatPanel.vue           # Task 8
├── components/Timeline.vue            # Task 9
├── components/StatusBar.vue           # Task 9
├── components/FileTree.vue            # Task 10
├── components/PreviewPane.vue         # Task 10（取代 MermaidPane.vue）
├── components/SettingsBar.vue         # Task 11
├── components/ApprovalDialog.vue（M0 版沿用，不改）
├── App.vue（重寫）                     # Task 11
docs/spikes/m1-results.md               # Task 12
```

---

### Task 1：Envelope v1 與 state reducer（`internal/contract`）

**Files:**
- Create: `internal/contract/envelope.go`、`internal/contract/state.go`
- Modify: `internal/contract/event.go`
- Test: `internal/contract/envelope_test.go`、`internal/contract/state_test.go`

**Interfaces（Produces；後續全部 task 依此）:**

```go
// event.go 追加（additive）
const (
	KindUsage            Kind = "usage"
	KindStateChange      Kind = "state_change"
	KindApprovalDecision Kind = "approval_decision"
)
// Event 增欄位：
//   Role  string  // ""|user|assistant|tool|system——adapter 明確標注
//   Usage *Usage
```

```go
// envelope.go
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedInput  int64 `json:"cached_input_tokens,omitempty"`
}

type Envelope struct {
	EventID   string          `json:"event_id"` // ULID，同毫秒單調
	TS        string          `json:"ts"`       // RFC3339Nano UTC
	Provider  string          `json:"provider"`
	SessionID string          `json:"session_id,omitempty"`
	Role      string          `json:"role,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	Kind      string          `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	CostUSD   float64         `json:"cost_usd,omitempty"`
	Usage     *Usage          `json:"usage,omitempty"`           // Manager 輸出時一律為累計 snapshot
	UsageSemantics string     `json:"usage_semantics,omitempty"` // session_total | provider_latest（P2-1）
	State     string          `json:"state,omitempty"`
	Error     string          `json:"error,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

func NewULID(t time.Time) string
// Wrap role 規則：ev.Role != "" → 原樣；否則 Kind==delta → "assistant"、其餘 → "system"。
// ev.Raw 非 json.Valid → Raw = json.Marshal(string(ev.Raw))（Envelope 必可 marshal）。
func Wrap(ev Event, taskID string) Envelope
```

```go
// state.go
type SessionState string

const (
	StateIdle             SessionState = "idle"
	StateWaiting          SessionState = "waiting" // user message 已送、回覆未開始
	StateStreaming        SessionState = "streaming"
	StateToolRunning      SessionState = "tool_running"
	StateAwaitingApproval SessionState = "awaiting_approval"
	StateRetrying         SessionState = "retrying"
	StateDone             SessionState = "done"
	StateFailed           SessionState = "failed"
)

type Reducer struct{ cur SessionState } // 非併發安全：僅供 Manager 於其 mutex 內使用

func NewReducer() *Reducer
func (r *Reducer) Apply(ev Event) (SessionState, bool)
func (r *Reducer) ResolveApproval() (SessionState, bool) // allow/deny/timeout 同構：awaiting → tool_running
func (r *Reducer) Reset()
func (r *Reducer) Current() SessionState
```

- [ ] **Step 1: 寫失敗測試**

```go
// envelope_test.go
package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestULIDMonotonicAndSortable(t *testing.T) {
	t0 := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	a, b := NewULID(t0), NewULID(t0) // 同毫秒仍遞增
	c := NewULID(t0.Add(time.Second))
	if !(a < b && b < c) {
		t.Fatalf("ulid order broken: %s %s %s", a, b, c)
	}
	if len(a) != 26 || strings.ContainsAny(a, "ILOU") {
		t.Fatalf("bad ulid %s", a)
	}
}

func TestWrapFillsEnvelope(t *testing.T) {
	env := Wrap(Event{Provider: ProviderClaude, Kind: KindDelta, SessionID: "s1",
		Raw: []byte(`{"x":1}`), Text: "hi"}, "task-42")
	if env.EventID == "" || env.TS == "" {
		t.Fatal("event_id / ts must be filled")
	}
	if env.Provider != "claude" || env.Kind != string(KindDelta) ||
		env.SessionID != "s1" || env.TaskID != "task-42" || env.Text != "hi" {
		t.Fatalf("fields not mapped: %+v", env)
	}
	b, err := json.Marshal(env)
	if err != nil || !strings.Contains(string(b), `"task_id":"task-42"`) {
		t.Fatalf("marshal: %v %s", err, b)
	}
}

func TestWrapRolePrecedence(t *testing.T) {
	if env := Wrap(Event{Provider: ProviderCodex, Kind: KindMessage, Role: "tool", Raw: []byte(`{}`)}, ""); env.Role != "tool" {
		t.Fatalf("explicit role must win: %q", env.Role)
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "assistant", Raw: []byte(`{}`)}, ""); env.Role != "assistant" {
		t.Fatal("assistant role must pass through")
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte(`{}`)}, ""); env.Role != "assistant" {
		t.Fatal("delta fallback must be assistant")
	}
	if env := Wrap(Event{Provider: ProviderClaude, Kind: KindMessage, Raw: []byte(`{}`)}, ""); env.Role != "system" {
		t.Fatal("unlabelled message must fallback to system, not assistant")
	}
}

func TestWrapMalformedRawStillMarshals(t *testing.T) {
	ev := Event{Provider: ProviderClaude, Kind: KindMalformed,
		Raw: []byte(`{"type":"resul`), Err: errors.New("unexpected EOF")}
	env := Wrap(ev, "")
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("envelope with invalid raw must marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	var s string
	if err := json.Unmarshal(back.Raw, &s); err != nil || !strings.Contains(s, "resul") {
		t.Fatalf("raw fallback = %s (%v)", back.Raw, err)
	}
	if back.Error == "" {
		t.Fatal("error string must survive")
	}
}

func TestWrapValidRawKeptVerbatim(t *testing.T) {
	env := Wrap(Event{Provider: ProviderCodex, Kind: KindMessage, Raw: []byte(`{"a":1}`)}, "")
	if string(env.Raw) != `{"a":1}` {
		t.Fatalf("valid raw must be verbatim: %s", env.Raw)
	}
}
```

```go
// state_test.go
package contract

import "testing"

func applyKind(t *testing.T, r *Reducer, kind Kind, isErr bool, role string, want SessionState) {
	t.Helper()
	got, _ := r.Apply(Event{Provider: ProviderClaude, Kind: kind, IsError: isErr, Role: role, Raw: []byte("{}")})
	if got != want {
		t.Fatalf("%s(role=%q,isErr=%v) -> %s, want %s", kind, role, isErr, got, want)
	}
}

func TestReducerHappyPath(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindInit, false, "", StateIdle)
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindToolUse, false, "", StateToolRunning)
	applyKind(t, r, KindApproval, false, "", StateAwaitingApproval)
	if st, changed := r.ResolveApproval(); st != StateToolRunning || !changed {
		t.Fatalf("allow resolve -> %s", st)
	}
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindResult, false, "", StateDone)
}

func TestReducerFailedResult(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	applyKind(t, r, KindResult, true, "", StateFailed)
}

func TestReducerMalformedIsNonTerminal(t *testing.T) { // M0 定義：malformed 不中斷
	r := NewReducer()
	applyKind(t, r, KindDelta, false, "", StateStreaming)
	if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMalformed, Raw: []byte("x")}); changed {
		t.Fatal("malformed must not change state")
	}
	applyKind(t, r, KindStreamError, false, "", StateFailed)
}

func TestReducerUserMessageEntersWaiting(t *testing.T) { // 第二輪送出不得停在 done
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindResult, Raw: []byte("{}")})
	got, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "user", Raw: []byte("{}")})
	if got != StateWaiting || !changed {
		t.Fatalf("user message -> %s", got)
	}
	if st, _ := r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")}); st != StateStreaming {
		t.Fatal("waiting -> streaming on first delta")
	}
}

func TestReducerToolEchoIsNeutral(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: KindMessage, Role: "tool", Raw: []byte("{}")}); changed {
		t.Fatal("tool echo must be neutral")
	}
}

func TestReducerDenyAndTimeoutResolve(t *testing.T) {
	for range 2 {
		r := NewReducer()
		r.Apply(Event{Provider: ProviderClaude, Kind: KindApproval, Raw: []byte("{}")})
		if st, _ := r.ResolveApproval(); st != StateToolRunning {
			t.Fatalf("resolve -> %s", st)
		}
	}
}

func TestReducerResolveOutsideApprovalIsNoop(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	if _, changed := r.ResolveApproval(); changed {
		t.Fatal("resolve without pending approval must be noop")
	}
}

func TestReducerResetForNewSession(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindStreamError, Raw: []byte("{}")})
	r.Reset()
	if r.Current() != StateIdle {
		t.Fatalf("after reset = %s", r.Current())
	}
}

func TestReducerRetryRecovers(t *testing.T) {
	r := NewReducer()
	applyKind(t, r, KindRetry, false, "", StateRetrying)
	applyKind(t, r, KindDelta, false, "", StateStreaming)
}

func TestReducerNeutralKinds(t *testing.T) {
	r := NewReducer()
	r.Apply(Event{Provider: ProviderClaude, Kind: KindDelta, Raw: []byte("{}")})
	for _, k := range []Kind{KindSystemOther, KindUnknown, KindUsage, KindApprovalDecision} {
		if _, changed := r.Apply(Event{Provider: ProviderClaude, Kind: k, Raw: []byte("{}")}); changed {
			t.Fatalf("%s must be neutral", k)
		}
	}
}
```

- [ ] **Step 2: 確認失敗** — `go test ./internal/contract/ -run 'TestULID|TestWrap|TestReducer' -v` → FAIL（未定義）。

- [ ] **Step 3: 實作**

```go
// envelope.go
package contract

import (
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ulidMu   sync.Mutex
	ulidLast int64
	ulidSeq  uint32
)

// NewULID：48-bit ms timestamp → 10 字 + 10 byte 隨機（前 2 byte 帶同毫秒序號保單調）。
func NewULID(t time.Time) string {
	ms := t.UnixMilli()
	ulidMu.Lock()
	if ms == ulidLast {
		ulidSeq++
	} else {
		ulidLast, ulidSeq = ms, 0
	}
	seq := ulidSeq
	ulidMu.Unlock()

	var b [26]byte
	v := uint64(ms)
	for i := 9; i >= 0; i-- {
		b[i] = crockford[v&31]
		v >>= 5
	}
	var rnd [10]byte
	_, _ = rand.Read(rnd[:])
	rnd[0], rnd[1] = byte(seq>>8), byte(seq)
	var acc uint64
	bits := 0
	j := 10
	for _, r := range rnd {
		acc = acc<<8 | uint64(r)
		bits += 8
		for bits >= 5 && j < 26 {
			bits -= 5
			b[j] = crockford[(acc>>uint(bits))&31]
			j++
		}
	}
	for ; j < 26; j++ {
		b[j] = crockford[0]
	}
	return string(b[:])
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedInput  int64 `json:"cached_input_tokens,omitempty"`
}

type Envelope struct {
	EventID   string          `json:"event_id"`
	TS        string          `json:"ts"`
	Provider  string          `json:"provider"`
	SessionID string          `json:"session_id,omitempty"`
	Role      string          `json:"role,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	Kind      string          `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	CostUSD   float64         `json:"cost_usd,omitempty"`
	Usage     *Usage          `json:"usage,omitempty"`
	UsageSemantics string     `json:"usage_semantics,omitempty"`
	State     string          `json:"state,omitempty"`
	Error     string          `json:"error,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

func Wrap(ev Event, taskID string) Envelope {
	now := time.Now()
	role := ev.Role
	if role == "" {
		if ev.Kind == KindDelta {
			role = "assistant"
		} else {
			role = "system"
		}
	}
	env := Envelope{
		EventID: NewULID(now), TS: now.UTC().Format(time.RFC3339Nano),
		Provider: string(ev.Provider), SessionID: ev.SessionID, Role: role,
		TaskID: taskID, Kind: string(ev.Kind), Text: ev.Text, Thinking: ev.Thinking,
		IsError: ev.IsError, CostUSD: ev.CostUSD, Usage: ev.Usage,
	}
	if len(ev.Raw) > 0 {
		if json.Valid(ev.Raw) {
			env.Raw = json.RawMessage(ev.Raw)
		} else {
			s, _ := json.Marshal(string(ev.Raw))
			env.Raw = json.RawMessage(s)
		}
	}
	if ev.Err != nil {
		env.Error = ev.Err.Error()
	}
	return env
}
```

```go
// state.go
package contract

type SessionState string

const (
	StateIdle             SessionState = "idle"
	StateWaiting          SessionState = "waiting"
	StateStreaming        SessionState = "streaming"
	StateToolRunning      SessionState = "tool_running"
	StateAwaitingApproval SessionState = "awaiting_approval"
	StateRetrying         SessionState = "retrying"
	StateDone             SessionState = "done"
	StateFailed           SessionState = "failed"
)

type Reducer struct{ cur SessionState }

func NewReducer() *Reducer               { return &Reducer{cur: StateIdle} }
func (r *Reducer) Current() SessionState { return r.cur }
func (r *Reducer) Reset()                { r.cur = StateIdle }

func (r *Reducer) Apply(ev Event) (SessionState, bool) {
	next := r.cur
	switch ev.Kind {
	case KindInit:
		next = StateIdle
	case KindMessage:
		switch ev.Role {
		case "user":
			next = StateWaiting
		case "assistant":
			next = StateStreaming
		default: // tool echo / system：中性
		}
	case KindDelta:
		next = StateStreaming
	case KindToolUse:
		next = StateToolRunning
	case KindApproval:
		next = StateAwaitingApproval
	case KindRetry:
		next = StateRetrying
	case KindResult:
		if ev.IsError {
			next = StateFailed
		} else {
			next = StateDone
		}
	case KindStreamError:
		next = StateFailed
	default: // malformed / system_other / unknown / usage / approval_decision：中性
	}
	changed := next != r.cur
	r.cur = next
	return r.cur, changed
}

func (r *Reducer) ResolveApproval() (SessionState, bool) {
	if r.cur != StateAwaitingApproval {
		return r.cur, false
	}
	r.cur = StateToolRunning
	return r.cur, true
}
```

`event.go` 追加（additive）：三個新 kind 常數與 `Role string`、`Usage *Usage` 欄位。

- [ ] **Step 4: 確認通過** — `go test ./internal/contract/ -race -v` → 全 PASS（含 M0 回歸）。
- [ ] **Step 5: Commit** — `git add internal/contract && git commit -m "feat(contract): envelope v1 with explicit roles, waiting state, raw fallback"`

---

### Task 2：Usage 解析與 Role 標注（adapter 層）

**Files:**
- Modify: `internal/claude/decode.go`、`internal/codex/mapevent.go`
- Test: `internal/claude/decode_test.go`、`internal/codex/mapevent_test.go`

**Interfaces:**
- Produces（wire 原語意；session 級收斂在 Task 6）:
  - claude `KindResult`.Usage＝該輪 wire 值（`usage.{input_tokens,output_tokens,cache_read_input_tokens}`，M0 錄流實據）；claude `type:"assistant"` → `Role:"assistant"`、`type:"user"` → `Role:"tool"`。
  - codex `KindUsage`.Usage＝thread 累計 snapshot（`tokenUsage.total.{inputTokens,outputTokens,cachedInputTokens}`，M0 錄流實據）；`agentMessage`／`reasoning` → `Role:"assistant"`、`userMessage` → `Role:"tool"`。
  - **tool_use 摘要 Text（Timeline tool 卡片 normative 的資料來源）**：
    **節錄規則（第十輪 P2-1 凍結）：內容 ≤80 rune 原樣；超過取前 80 rune 加刪節號
    「…」——節錄總長上限 81 rune（不含工具名與括號）**。claude assistant 訊息若
    **只含 tool_use blocks（無 text）** → `Kind=KindToolUse`、`Text = "<name>(<節錄>)"`
    （首個 block；多 block 加 ` +N`）；含 text 者維持 KindMessage（M0 行為不變）。
    codex tool 類 item → per-type 摘要（`codexToolSummary`）：commandExecution＝
    `command` 節錄、mcpToolCall＝`server.tool(arguments 節錄)`、webSearch＝
    `webSearch(query 節錄)`、fileChange＝`fileChange(首路徑 +N)`；wire 欄位缺失時
    型別名 fallback（如實顯示、不記 FAIL）。

- [ ] **Step 1: 寫失敗測試**

```go
// decode_test.go 追加
func TestDecodeResultUsage(t *testing.T) {
	line := `{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0.1,"usage":{"input_tokens":4,"output_tokens":714,"cache_read_input_tokens":84414}}`
	ev := Decode([]byte(line))
	if ev.Usage == nil || ev.Usage.InputTokens != 4 || ev.Usage.OutputTokens != 714 || ev.Usage.CachedInput != 84414 {
		t.Fatalf("usage = %+v", ev.Usage)
	}
}

func TestDecodeAssistantToolUseSummary(t *testing.T) { // 第八輪 P1-2；第九輪補精確案例
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"touch x"}}]}}`
	ev := Decode([]byte(line))
	if ev.Kind != contract.KindToolUse {
		t.Fatalf("tool-only assistant must map KindToolUse, got %s", ev.Kind)
	}
	if !strings.Contains(ev.Text, "Bash") || !strings.Contains(ev.Text, "touch x") {
		t.Fatalf("text must carry name+input excerpt: %q", ev.Text)
	}
	mixed := Decode([]byte(`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","input":{}}]}}`))
	if mixed.Kind != contract.KindMessage || mixed.Text != "hi" { // 含 text 維持 M0 行為
		t.Fatalf("mixed content must stay message: %s %q", mixed.Kind, mixed.Text)
	}
}

func TestDecodeToolSummaryTruncationAndMulti(t *testing.T) { // 第九輪 P1-2：80-rune 截斷、+N
	long := strings.Repeat("字", 100) // 100 rune 的 input 內容
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","name":"Write","input":{"path":"` + long + `"}},` +
		`{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	ev := Decode([]byte(line))
	if ev.Kind != contract.KindToolUse {
		t.Fatalf("kind = %s", ev.Kind)
	}
	if !strings.Contains(ev.Text, "Write(") || !strings.Contains(ev.Text, "…") {
		t.Fatalf("long input must be truncated with ellipsis: %q", ev.Text)
	}
	if !strings.HasSuffix(ev.Text, " +1") { // 兩個 tool block → +1
		t.Fatalf("multi-tool must append +N: %q", ev.Text)
	}
	// 節錄總長上限 81 rune（80 內容 + 刪節號；不含名稱與括號）——第十輪 P2-1 凍結
	inner := ev.Text[strings.Index(ev.Text, "(")+1 : strings.LastIndex(ev.Text, ")")]
	if n := len([]rune(inner)); n > 81 {
		t.Fatalf("excerpt too long: %d runes (cap 81)", n)
	}
}

func TestDecodeRoles(t *testing.T) {
	a := Decode([]byte(`{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))
	if a.Role != "assistant" {
		t.Fatalf("assistant role = %q", a.Role)
	}
	u := Decode([]byte(`{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`))
	if u.Role != "tool" {
		t.Fatalf("user-echo role = %q", u.Role)
	}
}
```

```go
// mapevent_test.go：cases struct 增 role 欄位並逐案斷言 ev.Role；表格更新為（節錄關鍵列，其餘列 role 填 ""）：
// cases struct 增 role 欄位並逐案斷言 ev.Role；既有表格逐列更新（text／role 欄）：
//   cmd_started：text "ls"（command）、cmd_completed：text "echo hi"
//   file_change／mcp_tool／web_search：text 為型別名 fallback（"fileChange"/"mcpToolCall"/"webSearch"）
//   agent_msg_completed：text "hello"、role "assistant"；user_msg：role "tool"
//   reasoning：thinking "thinking hard"、role "assistant"
// token_usage 案例 want 改 contract.KindUsage。另追加明確測試：

```go
func TestMapEventToolText(t *testing.T) { // 第十輪 P1-2：per-type 摘要（雙 provider 強制）
	cmd := MapEvent(MethodItemCompleted, json.RawMessage(`{"threadId":"t1","item":{"id":"i1","type":"commandExecution","command":"echo hi","status":"completed"}}`))
	if cmd.Kind != contract.KindToolUse || cmd.Text != "echo hi" {
		t.Fatalf("command text: %s %q", cmd.Kind, cmd.Text)
	}
	mcp := MapEvent(MethodItemStarted, json.RawMessage(`{"threadId":"t1","item":{"id":"i2","type":"mcpToolCall","server":"github","tool":"create_issue","arguments":{"title":"x"}}}`))
	if mcp.Text != `github.create_issue({"title":"x"})` { // 真正工具名＋參數
		t.Fatalf("mcp summary: %q", mcp.Text)
	}
	ws := MapEvent(MethodItemStarted, json.RawMessage(`{"threadId":"t1","item":{"id":"i3","type":"webSearch","query":"golang mutex"}}`))
	if ws.Text != "webSearch(golang mutex)" {
		t.Fatalf("webSearch summary: %q", ws.Text)
	}
	fc := MapEvent(MethodItemCompleted, json.RawMessage(`{"threadId":"t1","item":{"id":"i4","type":"fileChange","changes":[{"path":"a.go"},{"path":"b.go"}]}}`))
	if fc.Text != "fileChange(a.go +1)" { // 路徑＋多檔 +N
		t.Fatalf("fileChange summary: %q", fc.Text)
	}
	// 第十一輪 P2-1：MCP 必要欄位各自缺失 → 一律型別名 fallback（無半成品摘要）
	for name, params := range map[string]string{
		"all_missing":  `{"threadId":"t1","item":{"id":"i5","type":"mcpToolCall"}}`,
		"no_server":    `{"threadId":"t1","item":{"id":"i6","type":"mcpToolCall","tool":"create_issue","arguments":{"a":1}}}`,
		"no_tool":      `{"threadId":"t1","item":{"id":"i7","type":"mcpToolCall","server":"github","arguments":{"a":1}}}`,
		"no_arguments": `{"threadId":"t1","item":{"id":"i8","type":"mcpToolCall","server":"github","tool":"create_issue"}}`,
	} {
		if ev := MapEvent(MethodItemStarted, json.RawMessage(params)); ev.Text != "mcpToolCall" {
			t.Fatalf("%s must fallback to type name, got %q", name, ev.Text)
		}
	}
}
```

另追加 usage 斷言：
if ev := MapEvent(MethodThreadTokenUsageUpdated, json.RawMessage(`{"threadId":"t1","tokenUsage":{"total":{"inputTokens":10,"cachedInputTokens":4,"outputTokens":2}}}`)); ev.Usage == nil || ev.Usage.InputTokens != 10 || ev.Usage.OutputTokens != 2 || ev.Usage.CachedInput != 4 {
	t.Fatalf("codex usage = %+v", ev.Usage)
}
```

- [ ] **Step 2: 確認失敗** → FAIL。
- [ ] **Step 3: 實作**

`decode.go`（result 與 assistant/user case）：

```go
	case "assistant", "user":
		if head.Type == "assistant" {
			ev.Role = "assistant"
		} else {
			ev.Role = "tool" // provider echo（tool_result 載體），不進 Chat
		}
		ev.Kind = contract.KindMessage
		var body struct {
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &body) == nil {
			var sb strings.Builder
			var tools []string
			for _, c := range body.Message.Content {
				switch c.Type {
				case "text":
					sb.WriteString(c.Text)
				case "tool_use":
					tools = append(tools, toolSummary(c.Name, c.Input))
				}
			}
			ev.Text = sb.String()
			// 第八輪 P1-2：tool-only assistant → KindToolUse + 摘要（含 text 者維持 M0 行為）
			if head.Type == "assistant" && ev.Text == "" && len(tools) > 0 {
				ev.Kind = contract.KindToolUse
				ev.Text = tools[0]
				if len(tools) > 1 {
					ev.Text += fmt.Sprintf(" +%d", len(tools)-1)
				}
			}
		}
	case "result":
		ev.Kind = contract.KindResult
		var body struct {
			IsError      bool    `json:"is_error"`
			TotalCostUSD float64 `json:"total_cost_usd"`
			Usage        *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheRead    int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &body) == nil {
			ev.IsError, ev.CostUSD = body.IsError, body.TotalCostUSD
			if body.Usage != nil {
				ev.Usage = &contract.Usage{InputTokens: body.Usage.InputTokens,
					OutputTokens: body.Usage.OutputTokens, CachedInput: body.Usage.CacheRead}
			}
		}
```

`decode.go` 另加 helper（import 增 `fmt`）：

```go
// toolSummary：工具名 + input JSON 節錄（規則同 excerpt80：內容 ≤80 rune，
// 超過取前 80 rune 加「…」，節錄總長上限 81 rune）。
func toolSummary(name string, input json.RawMessage) string {
	excerpt := string(input)
	if r := []rune(excerpt); len(r) > 80 {
		excerpt = string(r[:80]) + "…"
	}
	return name + "(" + excerpt + ")"
}
```

`mapevent.go`：item 分流各 case 填 Role（agentMessage/reasoning→assistant、userMessage→tool）；tool 類 item 以 **per-type 摘要**填 Text（第十輪 P1-2，欄位名經 pinned schema 覆核：`command`／`server`+`tool`+`arguments`／`query`／`changes[].path`）——item struct 增欄位：

```go
// codexItem：item tagged-union 的 M1 支援子集欄位（pinned schema 覆核；
// MapEvent 的 params 解析改用 `var p struct{ Item codexItem \`json:"item"\` }`）。
type codexItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Summary   string          `json:"summary"`
	Command   string          `json:"command"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Query     string          `json:"query"`
	Changes   []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

// MapEvent 的 item 事件分流內：
		case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
			ev.Kind = contract.KindToolUse
			ev.Text = codexToolSummary(p.Item)
```

```go
// excerpt80：節錄規則（與 claude toolSummary 同一定義，第十輪 P2-1 凍結）：
// 內容 ≤80 rune 原樣；超過取前 80 rune 加刪節號「…」——總長上限 81 rune。
func excerpt80(v string) string {
	if r := []rune(v); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return v
}

// codexToolSummary：per-type 工具摘要（工具名＋參數節錄）；wire 欄位缺失時
// 以型別名 fallback（如實顯示、V3 不記 FAIL）。
func codexToolSummary(it codexItem) string {
	switch it.Type {
	case "commandExecution":
		if it.Command != "" {
			return excerpt80(it.Command)
		}
	case "mcpToolCall":
		// 第十一輪 P2-1：必要欄位（server、tool、arguments）全部存在才產生摘要，
		// 任一缺失 → 型別名 fallback（不產生 "server.()"、"tool()" 等半成品）
		if it.Server != "" && it.Tool != "" && len(it.Arguments) > 0 {
			return it.Server + "." + it.Tool + "(" + excerpt80(string(it.Arguments)) + ")"
		}
	case "webSearch":
		if it.Query != "" {
			return "webSearch(" + excerpt80(it.Query) + ")"
		}
	case "fileChange":
		if len(it.Changes) > 0 {
			paths := it.Changes[0].Path
			if len(it.Changes) > 1 {
				paths += fmt.Sprintf(" +%d", len(it.Changes)-1)
			}
			return "fileChange(" + excerpt80(paths) + ")"
		}
	}
	return it.Type
}
```

（`mapevent.go` import 只增 `fmt`（MCP 摘要為直接串接、無 strings 呼叫——第十二輪 P1）；`codexItem` 定義如上、`p.Item` 型別即為它。）

`MethodThreadTokenUsageUpdated` 獨立 case：

```go
	case MethodThreadTokenUsageUpdated:
		ev.Kind = contract.KindUsage
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens  int64 `json:"inputTokens"`
					OutputTokens int64 `json:"outputTokens"`
					CachedInput  int64 `json:"cachedInputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &p) == nil {
			ev.Usage = &contract.Usage{InputTokens: p.TokenUsage.Total.InputTokens,
				OutputTokens: p.TokenUsage.Total.OutputTokens, CachedInput: p.TokenUsage.Total.CachedInput}
		}
```

- [ ] **Step 4: 確認通過** — `go test ./internal/claude/ ./internal/codex/ -v`（含 A9/B5 replay 回歸：KindUsage Valid）→ PASS。
- [ ] **Step 5: Commit** — `git add internal/claude internal/codex && git commit -m "feat(adapters): explicit event roles and usage parsing"`

---

### Task 3：Claude 多輪 session（`Send`／`Close`；實作 `ports.Turns`）

**Files:**
- Create: `internal/ports/turns.go`（第七輪 P1-4：本 task 產出並納入 commit）
- Modify: `internal/claude/session.go`、`testdata/fake-claude.sh`
- Test: `internal/claude/session_test.go`

**Interfaces:**
- Produces:

```go
// internal/ports/turns.go（package ports；只 import contract——不依賴 proc 等 infrastructure，
// 第六輪 P2-1）。package doc 凍結語意（pinned-semantics godoc 慣例）：
//
// Package ports 定義 provider 中立的應用層契約。
//
// # Exit 語意（凍結）
//
// Exit 是 ports 自有的中立收尾值（不引用 infrastructure 型別）。
// Exited=false 表示「未取得 exit」（例如 pump 卡死、supervisor 未回收，或
// codex 長駐 server 尚在執行）——此時 Code 無意義，稽核端（recorder meta 的
// ExitCode）必須維持 nil，不得把未知偽裝成 exit 0（第七輪 P1-3）。
type Exit struct {
	Exited     bool   // true = Code 有效（process 已回收）
	Code       int
	StderrTail string
}

// # Turns 語意（凍結）
//
// Send 只在前一輪 result 事件已出現後合法；生命週期實作各自定義（同 process 多輪
// 或 resume-per-turn），對呼叫端不可見。Close 冪等、代表「不再有輸入」，之後
// Send 必回錯誤；Terminate 強殺（整組）。Wait 回傳快取的 Exit，任意時點可呼叫；
// Events channel 於 provider 收尾後關閉。argv／錄流等診斷資訊不屬 turn 行為，
// 拆為選配的 Diagnostics capability。
type Turns interface {
	Events() <-chan contract.Event
	Send(prompt string) error
	Close() error     // 結束輸入（自然收尾）
	Terminate() error // 強殺
	Wait() Exit
}

// Diagnostics 為選配診斷能力（稽核／recorder meta 用）；需要時以型別斷言取得。
type Diagnostics interface {
	Argv() []string
}
```

（`internal/claude` 的 `Session.Wait()` 改回傳 `ports.Exit`——由 `proc.Exit` 映射
`{Exited: true, Code, StderrTail}`（supervisor 已回收才返回，故恆為 Exited)，M0 既有
測試僅使用 Code/StderrTail 欄位、不受影響；`Argv()` 留在具體型別上滿足
`ports.Diagnostics`。編譯期斷言見 Task 3／4b 測試。）

- `Config.MultiTurn bool`——true 時 `Start` 不關 stdin；false 保 M0 行為（送完即關）。
- `Session.Send(prompt)`——寫入一則 stream-json user message；stdin 已關或寫入失敗（→ Terminate 整組）回 error。
- `Session.Close()`——冪等關 stdin；CLI 隨後自然收尾。
- Turn 邊界＝每輪一個 `KindResult` 事件。

- [ ] **Step 1: `fake-claude.sh` 增 FAKE_MULTI 分支**（插在既有單輪邏輯之前）：

```bash
if [ -n "${FAKE_MULTI:-}" ]; then
  echo '{"type":"system","subtype":"init","session_id":"fake-1","model":"m","mcp_servers":[]}'
  n=0
  while read -r _line; do
    n=$((n+1))
    printf '{"type":"stream_event","session_id":"fake-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"t%d"}}}\n' "$n"
    printf '{"type":"result","subtype":"success","session_id":"fake-1","result":"t%d","total_cost_usd":0,"is_error":false}\n' "$n"
  done
  exit 0
fi
```

- [ ] **Step 2: 寫失敗測試**

```go
var _ ports.Turns = (*Session)(nil)       // 編譯期介面契約（import "github.com/slam0504/sdlc-workbench/internal/ports"）
var _ ports.Diagnostics = (*Session)(nil) // Argv 診斷能力

func TestMultiTurnSendAndTurnBoundaries(t *testing.T) {
	cfg := fakeCfg(t, "FAKE_MULTI=1")
	cfg.MultiTurn = true
	cfg.Prompt = "first"
	s, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var results int
	events := s.Events()
	waitResult := func() {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatal("stream closed before result")
				}
				if ev.Kind == contract.KindResult {
					results++
					return
				}
			case <-deadline:
				t.Fatal("no result within 5s")
			}
		}
	}
	waitResult() // 第 1 輪（Start 的 prompt）
	if err := s.Send("second"); err != nil {
		t.Fatal(err)
	}
	waitResult()
	if err := s.Send("third"); err != nil {
		t.Fatal(err)
	}
	waitResult()
	if results != 3 {
		t.Fatalf("results = %d", results)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range events { // drain 至 EOF
	}
	if ex := s.Wait(); ex.Code != 0 {
		t.Fatalf("exit = %d", ex.Code)
	}
}

func TestSendAfterCloseErrors(t *testing.T) {
	cfg := fakeCfg(t, "FAKE_MULTI=1")
	cfg.MultiTurn = true
	s, _ := Start(context.Background(), cfg)
	_ = s.Close()
	if err := s.Send("x"); err == nil {
		t.Fatal("Send after Close must error")
	}
	drain(s)
	s.Wait()
}

func TestSingleTurnBehaviorUnchanged(t *testing.T) { // M0 回歸
	s, err := Start(context.Background(), fakeCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	ks := drain(s)
	if len(ks) != 3 {
		t.Fatalf("single-turn kinds = %v", ks)
	}
	if err := s.Send("x"); err == nil {
		t.Fatal("single-turn Send must error (stdin closed)")
	}
}
```

- [ ] **Step 3: 確認失敗** → FAIL。

- [ ] **Step 4: 實作**（`session.go`）

```go
// Config 增：MultiTurn bool
// Session 增：stdinMu sync.Mutex；stdin io.WriteCloser（nil = 已關）
// Start：prompt 送出後——
//   if cfg.MultiTurn { s.stdin = p.Stdin } else { _ = p.Stdin.Close() }

func (s *Session) Send(prompt string) error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if s.stdin == nil {
		return errors.New("claude: session stdin closed")
	}
	msg, _ := json.Marshal(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": prompt}}}})
	if _, err := fmt.Fprintf(s.stdin, "%s\n", msg); err != nil {
		_ = s.p.Terminate() // 寫入失敗＝process 不可信，整組收掉
		return err
	}
	return nil
}

func (s *Session) Close() error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if s.stdin == nil {
		return nil
	}
	err := s.stdin.Close()
	s.stdin = nil
	return err
}
```

- [ ] **Step 5: 確認通過** — `go test ./internal/claude/ -race -v` → PASS（含 M0 全部回歸）。
- [ ] **Step 6: Commit** — `git add internal/ports internal/claude testdata/fake-claude.sh && git commit -m "feat(claude): multi-turn session implementing ports.Turns"`

---

### Task 4：多輪 live probe（結構化清理＋usage VERDICT）

**Files:**
- Create: `cmd/probe-multiturn/main.go`
- 產出：`.workbench/recordings/claude-multiturn.ndjson`（不 commit）；去敏 `testdata/fixtures/claude-multiturn-shape.ndjson`（commit）

**Interfaces:**
- Produces: (1) 多輪支援判定（exit code 0=PASS）；(2) **usage VERDICT**（`per-turn`｜`cumulative`｜`inconclusive`）——判定規則（第三輪 P2-2）：turn1 刻意長回答、turn2 刻意極短回答，比較 output tokens：
  - `o2 < o1/2` → **per-turn**（Task 6 claude 累加制）
  - `o2 ≥ o1` → **cumulative**（Task 6 覆寫制）
  - 其餘 → **inconclusive** → fallback 覆寫制、UI 標「provider 最新回報值」。

- [ ] **Step 1: 實作 probe**（第三輪 P2-1：Close 錯誤、錄流寫入錯誤、Close 後持續 drain）

```go
// cmd/probe-multiturn/main.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
)

type turnInfo struct {
	sessionID string
	text      strings.Builder
	usage     *contract.Usage
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "PROBE FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PROBE PASS: in-process multi-turn confirmed")
}

func run() (retErr error) {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	recDir := filepath.Join(root, ".workbench", "recordings")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		return fmt.Errorf("mkdir recordings: %w", err)
	}
	out, err := os.Create(filepath.Join(recDir, "claude-multiturn.ndjson"))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("recording close: %w", cerr)
		}
	}()

	s, err := claude.Start(context.Background(), claude.Config{
		Binary: filepath.Join(root, "tools/claude-cli/node_modules/.bin/claude"),
		CWD:    root, MultiTurn: true,
		Prompt: "記住暗號：鳳梨酥。然後用大約兩百字介紹 TCP 三次握手", // turn1 刻意長回答（VERDICT 用）
	})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer func() { // 正常路徑等自然收尾（Close 錯誤不吞；Close 後持續 drain 讓 scanner 不阻塞）
		if cerr := s.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("stdin close: %w", cerr)
		}
		go func() {
			for range s.Events() {
			}
		}()
		exit := make(chan ports.Exit, 1)
		go func() { exit <- s.Wait() }()
		select {
		case ex := <-exit:
			if retErr == nil && ex.Code != 0 {
				retErr = fmt.Errorf("CLI exit %d after close (stderr: %s)", ex.Code, ex.StderrTail)
			}
		case <-time.After(30 * time.Second):
			_ = s.Terminate()
			<-exit
			if retErr == nil {
				retErr = errors.New("CLI did not exit naturally within 30s after stdin close")
			}
		}
	}()

	waitResult := func(step string, d time.Duration) (turnInfo, error) {
		var tu turnInfo
		timer := time.After(d)
		for {
			select {
			case ev, ok := <-s.Events():
				if !ok {
					return tu, fmt.Errorf("%s: stream closed before result (no in-process multi-turn)", step)
				}
				if _, werr := fmt.Fprintf(out, "%s\n", ev.Raw); werr != nil {
					return tu, fmt.Errorf("%s: recording write: %w", step, werr)
				}
				if ev.SessionID != "" {
					tu.sessionID = ev.SessionID
				}
				if ev.Kind == contract.KindDelta || (ev.Kind == contract.KindMessage && ev.Role == "assistant") {
					tu.text.WriteString(ev.Text)
				}
				if ev.Kind == contract.KindResult {
					tu.usage = ev.Usage
					return tu, nil
				}
			case <-timer:
				return tu, fmt.Errorf("%s: timeout", step)
			}
		}
	}

	t1, err := waitResult("turn1", 180*time.Second)
	if err != nil {
		return err
	}
	// 嚴格序：第一個 result 已讀到、stdin 仍開啟，才送第二則
	if err := s.Send("暗號是什麼？只回覆暗號本身"); err != nil { // turn2 刻意極短回答
		return fmt.Errorf("send2: %w", err)
	}
	t2, err := waitResult("turn2", 120*time.Second)
	if err != nil {
		return err
	}

	if t1.sessionID == "" || t1.sessionID != t2.sessionID {
		return fmt.Errorf("session id mismatch: %q vs %q", t1.sessionID, t2.sessionID)
	}
	if !strings.Contains(t2.text.String(), "鳳梨酥") {
		return fmt.Errorf("context lost: %q", t2.text.String())
	}

	// usage VERDICT（可判定規則 + inconclusive fallback）
	verdict := "inconclusive"
	if t1.usage != nil && t2.usage != nil && t1.usage.OutputTokens > 0 {
		o1, o2 := t1.usage.OutputTokens, t2.usage.OutputTokens
		switch {
		case o2 < o1/2:
			verdict = "per-turn"
		case o2 >= o1:
			verdict = "cumulative"
		}
	}
	u1, _ := json.Marshal(t1.usage)
	u2, _ := json.Marshal(t2.usage)
	fmt.Printf("USAGE turn1=%s turn2=%s VERDICT=%s\n", u1, u2, verdict)
	return nil
}
```

- [ ] **Step 2: 執行判定** — `go run ./cmd/probe-multiturn`（真 CLI、訂閱模式）。PASS → Task 4b 跳過；FAIL（stderr 歸因）→ 執行 Task 4b。VERDICT 決定 Task 6 的 `claudeUsageCumulative` 常數（per-turn → false；cumulative／inconclusive → true）；連同錄流 digest 記入 m1-results 草稿。
- [ ] **Step 3: 去敏節錄** — 自錄流取 init／兩組（delta→result）樣本，文字換占位、環境欄位清空，寫 `testdata/fixtures/claude-multiturn-shape.ndjson`（`TestContractReplay` 自動涵蓋）。
- [ ] **Step 4: Commit** — `git add cmd/probe-multiturn testdata/fixtures/claude-multiturn-shape.ndjson && git commit -m "test(m1): structured multi-turn probe with usage verdict"`

---

### Task 4b（條件：僅 Task 4 FAIL 時執行）：`ResumeTurns` fallback（EOF+Wait gating）

**Files:**
- Create: `internal/claude/multiturn.go`；Test: `internal/claude/multiturn_test.go`（僅本 task 觸發時建立）

**Interfaces:**

```go
type ResumeConfig struct {
	Base        Config                 // Binary/CWD/MCP 等共通設定（MultiTurn=false）
	OnSessionID func(sessionID string) // 每輪 init 時回報（appcore 綁 registry 與 UI）
}

type ResumeTurns struct { /* mu、cfg、curSID、cur *Session、events chan contract.Event、idle bool、closed bool、lastArgv []string */ }

func StartResumeTurns(ctx context.Context, cfg ResumeConfig, firstPrompt string) (*ResumeTurns, error)
// Send：前輪必須「事件流 EOF + Wait() 完成」才 idle——result 已到但 process 未退出
// 時仍 busy（session state 未落盤，--resume 過早會讀不到）。Send 阻塞至 idle（上限 15s）
// 或回 error；idle 後以 Base+Resume(curSID)+Prompt 起新 process 並接管其事件。
func (r *ResumeTurns) Send(prompt string) error
func (r *ResumeTurns) LastArgv() []string // 測試用：驗第二輪 argv 帶 --resume <前輪 sid>
var _ ports.Turns = (*ResumeTurns)(nil)
var _ ports.Diagnostics = (*ResumeTurns)(nil)
```

- [ ] **Step 1: 寫失敗測試**

```go
func TestResumeTurnsGatesOnEOFAndCarriesResume(t *testing.T) {
	var sids []string
	p, _ := filepath.Abs("../../testdata/fake-claude.sh")
	rt, err := StartResumeTurns(context.Background(), ResumeConfig{
		Base:        Config{Binary: p, CWD: t.TempDir(), TermGrace: 200 * time.Millisecond},
		OnSessionID: func(sid string) { sids = append(sids, sid) },
	}, "first")
	if err != nil {
		t.Fatal(err)
	}
	waitResult := func() {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-rt.Events():
				if !ok {
					t.Fatal("closed early")
				}
				if ev.Kind == contract.KindResult {
					return
				}
			case <-deadline:
				t.Fatal("no result")
			}
		}
	}
	waitResult()
	if err := rt.Send("second"); err != nil { // 內部等前輪 EOF+Wait 後才 spawn
		t.Fatal(err)
	}
	waitResult()
	argv := strings.Join(rt.LastArgv(), " ")
	if !strings.Contains(argv, "--resume fake-1") {
		t.Fatalf("second argv missing resume: %s", argv)
	}
	if len(sids) != 2 {
		t.Fatalf("session callbacks = %d", len(sids))
	}
	_ = rt.Close()
	for range rt.Events() {
	}
}

func TestResumeTurnsSendWhileBusyErrors(t *testing.T) {
	p, _ := filepath.Abs("../../testdata/fake-claude.sh")
	rt, _ := StartResumeTurns(context.Background(), ResumeConfig{
		Base: Config{Binary: p, CWD: t.TempDir(), Env: []string{"FAKE_HANG=1"}, TermGrace: 200 * time.Millisecond},
	}, "first")
	time.Sleep(100 * time.Millisecond)
	if err := rt.Send("second"); err == nil {
		t.Fatal("Send during active turn must error")
	}
	_ = rt.Terminate()
	for range rt.Events() {
	}
}
```

- [ ] **Step 2: 確認失敗** → FAIL。
- [ ] **Step 3: 實作**：pump goroutine 逐輪 forward `cur.Events()` 進共用 channel、`ParseInit` 時呼叫 `OnSessionID` 並記 `curSID`、**EOF 後呼叫 `cur.Wait()`、之後才設 `idle=true`**；`Send` 以 condition variable／polling 等 idle（15s 上限，逾時 error）→ `Start(Base + Resume: curSID, Prompt: prompt)`（記 `lastArgv = session.Argv()`）；`Close` 標記 closed、目前輪收尾後 close channel；`Terminate`／`Wait`／`Argv` 委派目前輪。約 130 行。
- [ ] **Step 4: 確認通過** — `go test ./internal/claude/ -race -v` → PASS。
- [ ] **Step 5: Commit** — `git add internal/claude && git commit -m "feat(claude): resume-per-turn fallback with EOF+Wait gating"`

---

### Task 5：Codex `ThreadRunner`（early-completed latch、barrier、session-scoped 錄流雙輪）

**Files:**
- Create: `internal/codex/turns.go`；Test: `internal/codex/turns_test.go`

**Interfaces:**

```go
var ErrTurnActive = errors.New("codex: turn already active")

type ThreadRunner struct { /* conn、mu、threadID、activeTurnID string、pending bool、earlyEnded map[string]bool */ }

func NewThreadRunner(conn *Conn) *ThreadRunner
func (t *ThreadRunner) EnsureThread(ctx context.Context, resume, approvalPolicy string) (string, error) // 冪等
// StartTurn：同鎖佔位（pending）→ Call（response 立即回 inProgress）→ 解析 turn id。
//   response 錯誤或 id 空 → 清占位、回 error；earlyEnded 含該 id → 對消、alreadyEnded=true。
func (t *ThreadRunner) StartTurn(ctx context.Context, prompt string) (turnID string, alreadyEnded bool, err error)
// NoteTurnEnded：completed/failed/interrupt 收尾通知呼叫。匹配 active → 解鎖回 true；
// pending 中 → latch 進 earlyEnded 回 false（收尾由 StartTurn 對消側執行）；其他 → false。
func (t *ThreadRunner) NoteTurnEnded(turnID string) bool
func (t *ThreadRunner) ThreadID() string
func (t *ThreadRunner) ActiveTurnID() string
```

- [ ] **Step 1: 寫失敗測試**

```go
func TestThreadRunnerWireLifecycle(t *testing.T) { // response 立即回 inProgress、completed 解鎖
	p := newFakePair(t)
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	ctx := context.Background()
	if _, err := r.EnsureThread(ctx, "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	if id2, _ := r.EnsureThread(ctx, "", "untrusted"); id2 != "t1" { // 冪等
		t.Fatal("EnsureThread must be idempotent")
	}
	turnID, ended, err := r.StartTurn(ctx, "one")
	if err != nil || turnID != "turn-A" || ended || r.ActiveTurnID() != "turn-A" {
		t.Fatalf("start: %s %v %v", turnID, ended, err)
	}
	if _, _, err := r.StartTurn(ctx, "two"); err != ErrTurnActive {
		t.Fatalf("busy must reject, got %v", err)
	}
	if r.NoteTurnEnded("turn-OTHER") {
		t.Fatal("mismatched turn id must be ignored")
	}
	if !r.NoteTurnEnded("turn-A") {
		t.Fatal("matching turn id must unlock")
	}
	if _, _, err := r.StartTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}
}

func TestThreadRunnerEarlyCompletedLatch(t *testing.T) { // completed 先到不得永久 busy
	p := newFakePair(t)
	r := NewThreadRunner(p.conn)
	ended := make(chan string, 2)
	p.conn.OnNotification(func(m string, params json.RawMessage) {
		if m == MethodTurnCompleted { // notification handler 真正呼叫 NoteTurnEnded
			var tp struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(params, &tp)
			if r.NoteTurnEnded(tp.Turn.ID) {
				ended <- tp.Turn.ID
			}
		}
	})
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart: // 惡意順序：completed 先送、response 後送
			p.fake.send(map[string]any{"method": MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": "turn-A", "status": "completed"}}})
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	if _, err := r.EnsureThread(context.Background(), "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	turnID, alreadyEnded, err := r.StartTurn(context.Background(), "x")
	if err != nil || turnID != "turn-A" || !alreadyEnded {
		t.Fatalf("latch must reconcile: %s %v %v", turnID, alreadyEnded, err)
	}
	if r.ActiveTurnID() != "" {
		t.Fatal("runner must not stay busy")
	}
	if _, _, err := r.StartTurn(context.Background(), "y"); err != nil {
		t.Fatal(err)
	}
}

func TestThreadRunnerEmptyTurnIDIsError(t *testing.T) {
	p := newFakePair(t)
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{}}) // 缺 turn
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	_, _ = r.EnsureThread(context.Background(), "", "untrusted")
	if _, _, err := r.StartTurn(context.Background(), "x"); err == nil {
		t.Fatal("missing turn id must be an error")
	}
	if _, _, err := r.StartTurn(context.Background(), "x"); err == nil { // 占位已清、可重試
		t.Fatal("pending must be cleared after error")
	}
}

func TestThreadRunnerBarrierOnlyOneWireRequest(t *testing.T) { // 並行 ownership 證明
	p := newFakePair(t)
	var turnStarts atomic.Int32
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			turnStarts.Add(1)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	if _, err := r.EnsureThread(context.Background(), "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-begin
			_, _, err := r.StartTurn(context.Background(), "x")
			errs <- err
		}()
	}
	close(begin)
	e1, e2 := <-errs, <-errs
	if !((e1 == nil && e2 == ErrTurnActive) || (e2 == nil && e1 == ErrTurnActive)) {
		t.Fatalf("exactly one must win: %v / %v", e1, e2)
	}
	waitFor(t, func() bool { return turnStarts.Load() == 1 }, "exactly one wire turn/start")
}

func TestThreadRunnerResume(t *testing.T) {
	p := newFakePair(t)
	var resumed struct {
		ThreadID       string `json:"threadId"`
		ApprovalPolicy string `json:"approvalPolicy"`
	}
	p.fake.onReq = func(fr Frame) {
		if fr.Method == MethodThreadResume {
			_ = json.Unmarshal(fr.Params, &resumed)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t9"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	id, err := r.EnsureThread(context.Background(), "t9", "on-request")
	if err != nil || id != "t9" || resumed.ThreadID != "t9" || resumed.ApprovalPolicy != "on-request" {
		t.Fatalf("resume wiring: %v %s %+v", err, id, resumed)
	}
}

func TestRecordingSpansMultipleTurns(t *testing.T) { // session-scoped 錄流：跨輪 attach、Stop 一次
	p := newFakePair(t)
	var mu sync.Mutex
	var lines [][]byte
	if err := p.conn.BeginRecording(func(b []byte) error {
		mu.Lock()
		lines = append(lines, append([]byte(nil), b...))
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	turnN := atomic.Int32{}
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			n := turnN.Add(1)
			id := fmt.Sprintf("turn-%d", n)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": id, "status": "inProgress"}}})
			p.fake.send(map[string]any{"method": MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": id, "status": "completed"}}})
		}
	}
	done := make(chan string, 2)
	r := NewThreadRunner(p.conn)
	p.conn.OnNotification(func(m string, params json.RawMessage) {
		if m == MethodTurnCompleted {
			var tp struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(params, &tp)
			if r.NoteTurnEnded(tp.Turn.ID) {
				done <- tp.Turn.ID
			}
		}
	})
	doHandshake(t, p.conn)
	_, _ = r.EnsureThread(context.Background(), "", "untrusted")
	runTurn := func(prompt string) {
		_, ended, err := r.StartTurn(context.Background(), prompt)
		if err != nil {
			t.Fatal(err)
		}
		if !ended {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("turn not completed")
			}
		}
	}
	runTurn("one")
	runTurn("two")
	if err := p.conn.StopRecording(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var starts, completes int
	for _, b := range lines {
		if bytes.Contains(b, []byte(`"turn/start"`)) {
			starts++
		}
		if bytes.Contains(b, []byte(`"turn/completed"`)) {
			completes++
		}
	}
	if starts != 2 || completes != 2 {
		t.Fatalf("recording must span both turns: starts=%d completes=%d", starts, completes)
	}
}
```

- [ ] **Step 2: 確認失敗** → FAIL。

- [ ] **Step 3: 實作**（`turns.go`）

```go
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

var ErrTurnActive = errors.New("codex: turn already active")

type ThreadRunner struct {
	conn         *Conn
	mu           sync.Mutex
	threadID     string
	activeTurnID string
	pending      bool
	earlyEnded   map[string]bool
}

func NewThreadRunner(conn *Conn) *ThreadRunner {
	return &ThreadRunner{conn: conn, earlyEnded: map[string]bool{}}
}

func (t *ThreadRunner) ThreadID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.threadID
}

func (t *ThreadRunner) ActiveTurnID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeTurnID
}

func (t *ThreadRunner) EnsureThread(ctx context.Context, resume, approvalPolicy string) (string, error) {
	t.mu.Lock()
	if t.threadID != "" {
		id := t.threadID
		t.mu.Unlock()
		return id, nil
	}
	t.mu.Unlock()
	method := MethodThreadStart
	params := map[string]any{"approvalPolicy": approvalPolicy}
	if resume != "" {
		method = MethodThreadResume
		params["threadId"] = resume
	}
	res, err := t.conn.Call(ctx, method, params)
	if err != nil {
		return "", err
	}
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &tr); err != nil || tr.Thread.ID == "" {
		return "", errors.New("codex: thread id missing in response")
	}
	t.mu.Lock()
	t.threadID = tr.Thread.ID
	t.mu.Unlock()
	return tr.Thread.ID, nil
}

func (t *ThreadRunner) StartTurn(ctx context.Context, prompt string) (string, bool, error) {
	t.mu.Lock()
	if t.pending || t.activeTurnID != "" {
		t.mu.Unlock()
		return "", false, ErrTurnActive
	}
	t.pending = true
	threadID := t.threadID
	t.mu.Unlock()

	res, err := t.conn.Call(ctx, MethodTurnStart, map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	})
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = false
	if err != nil {
		return "", false, err
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if uerr := json.Unmarshal(res, &tr); uerr != nil || tr.Turn.ID == "" {
		return "", false, errors.New("codex: turn id missing in turn/start response")
	}
	if t.earlyEnded[tr.Turn.ID] { // completed 先到：對消，不設 active
		delete(t.earlyEnded, tr.Turn.ID)
		return tr.Turn.ID, true, nil
	}
	t.activeTurnID = tr.Turn.ID
	return tr.Turn.ID, false, nil
}

func (t *ThreadRunner) NoteTurnEnded(turnID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending && t.activeTurnID == "" { // response 尚未消化：latch
		t.earlyEnded[turnID] = true
		return false
	}
	if t.activeTurnID == "" || turnID != t.activeTurnID {
		return false
	}
	t.activeTurnID = ""
	return true
}
```

- [ ] **Step 4: 確認通過** — `go test ./internal/codex/ -race -v` → PASS（含 M0 回歸）。
- [ ] **Step 5: Commit** — `git add internal/codex && git commit -m "feat(codex): ThreadRunner with early-completed latch; session-scoped recording proven across turns"`

---

### Task 6：`appcore`（Manager + submission coordinator + RecordingLease）與 app.go 重寫

**Files:**
- Create: `internal/appcore/sink.go`、`internal/appcore/recording.go`、`internal/appcore/manager.go`
- Test: `internal/appcore/recording_test.go`、`internal/appcore/manager_test.go`
- Modify: `app.go`（重寫）、`app_test.go`（M0 turn 追蹤測試遷入 appcore）

**Interfaces:**

```go
// sink.go
type AuditSink interface {
	Write(env contract.Envelope) error
	Close() error
}
// JSONLSink：O_APPEND 檔案實作；Write = marshal + 換行；錯誤原樣回傳。
func NewJSONLSink(path string) (*JSONLSink, error)
```

```go
// recording.go —— 第三輪 P1-3：session 錄流收尾 ownership
type RecorderHandle interface { // *recorder.Recorder 滿足
	Line(b []byte) error
	CloseWith(m recorder.Meta) error
}

// RecordingLease：多個收尾來源（EndSession／new session／shutdown／fatal）併發觸發時，
// stop 恰一次、CloseWith 恰一次、首個錯誤保留、後續呼叫冪等回傳同一結果。
// Finalize 攜帶 ports.Exit（第六輪 P2-2）：首次呼叫的 exit 進 metaFn，
// recorder meta 的 ExitCode 因此有明確資料路徑（CloseSequence → Finalize(ex)）。
type RecordingLease struct { /* once sync.Once、stop func() error、rec RecorderHandle、
                               metaFn func(ports.Exit) recorder.Meta、err error、done atomic.Bool */ }

func NewRecordingLease(rec RecorderHandle, stop func() error,
	metaFn func(ex ports.Exit) recorder.Meta) *RecordingLease
func (l *RecordingLease) Finalize(ex ports.Exit) error // 冪等；首次呼叫的 ex 生效
func (l *RecordingLease) Finalized() bool
```

```go
// manager.go
type Config struct {
	Sink AuditSink                    // 必填
	Emit func(env contract.Envelope) // 必填：UI 出口
	ClaudeUsageCumulative bool       // Task 4 VERDICT：per-turn=false；cumulative/inconclusive=true
}

type Manager struct { /* mu、cfg、reducer、taskID、totalCost、totalUsage、auditErr、
                        submitPending bool、pendingBuf []contract.Event */ }

var ErrSubmitActive = errors.New("appcore: submission already in progress")
var ErrSessionActive = errors.New("appcore: session already active; end it first")
var ErrStaleSubmission = errors.New("appcore: stale submission id")
var ErrNoSession = errors.New("appcore: no active session")
var ErrStartInProgress = errors.New("appcore: session start in progress")
var ErrEndInProgress = errors.New("appcore: session end in progress")
var ErrStaleSession = errors.New("appcore: stale session token")
var ErrClosed = errors.New("appcore: manager closed")

// SubmissionID：coordinator 的唯一 ownership token。
type SubmissionID struct{ gen, seq uint64 }

// SessionToken：session lifecycle 的 token（generation + end 序號）——
// EndSession 兩段式收尾的憑證；每次 BeginEndSession 發新 token，
// Cancel／Finish 只認「目前 outstanding」的那一枚，舊 token（含 Cancel 後
// 重新 Begin 前的）一律 ErrStaleSession no-op。
type SessionToken struct{ gen, seq uint64 }

// Session lifecycle phase（單一 mutex 下的狀態機）：
//   idle --BeginNewSessionSubmit--> starting --AcceptSubmit--> active
//   starting --RejectSubmit--> idle
//   active --BeginEndSession--> ending --FinishEndSession(token)--> idle
// 非法轉移一律 sentinel error：
//   BeginNewSessionSubmit：starting → ErrSubmitActive；active/ending → ErrSessionActive
//   BeginEndSession：idle → ErrNoSession；starting → ErrStartInProgress（End 不得
//     在 Start 已建 process、尚未 Accept 時無聲成功——第六輪 P1-2）；ending → ErrEndInProgress
//   FinishEndSession：token.gen ≠ 現任 session gen 或 phase ≠ ending → ErrStaleSession
//     （舊 session 的延遲 End 不可能把新 session 清成 inactive）

func New(cfg Config) *Manager
func (m *Manager) BeginNewSessionSubmit(taskID string) (SubmissionID, error)
func (m *Manager) BeginEndSession() (SessionToken, error)
// CancelEndSession：進入 ending 後、teardown 尚未發生前的復原（第七輪 P1-2：
// 例如 codex busy 檢查在 ending 內才失敗）——token 匹配 → 回 active；否則 ErrStaleSession。
func (m *Manager) CancelEndSession(t SessionToken) error
// FinishEndSession：teardown 已執行（成功或失敗）後收尾——teardown 回錯時仍必須呼叫
// （session 已終結），呼叫端以 errors.Join 保留 teardown 與 finish 兩者的錯誤。
func (m *Manager) FinishEndSession(t SessionToken) error
func (m *Manager) SessionActive() bool // phase == active
func (m *Manager) Emit(ev contract.Event) // closed 最先檢查（P1-4）→ pending 入 queue → emitLocked
// —— submission coordinator ——
func (m *Manager) BeginSubmit() (SubmissionID, error) // 既有 session 內的後續輪；已有 owner → ErrSubmitActive
func (m *Manager) AcceptSubmit(id SubmissionID, provider contract.Provider, sessionID, text string) error // closed → ErrClosed；ID 不匹配/stale → ErrStaleSubmission（no-op）
func (m *Manager) RejectSubmit(id SubmissionID) error
// —— approval 入口（同一 coordinator queue：pending 時連同 reducer side effect 一起入列）——
func (m *Manager) EmitApprovalRequest(provider contract.Provider, sessionID, toolName string, raw []byte)
func (m *Manager) EmitApprovalDecision(provider contract.Provider, sessionID, decision, reason string) // allow|deny|timeout；均 ResolveApproval
func (m *Manager) Totals() (costUSD float64, u contract.Usage)
func (m *Manager) State() contract.SessionState
func (m *Manager) AuditErr() error
// Close：同一 mutex、設 closed；之後 Emit 不寫 sink、發「event after close dropped」
// stream_error 至 UI（第四輪 P1-4）。
func (m *Manager) Close() error
```

```go
// pump.go —— provider pump 的 quiesce 契約（可測核心）
// Pump：把事件 channel 逐一送進 emit；channel 關閉時 close 回傳的 done。
func Pump(events <-chan contract.Event, emit func(contract.Event)) <-chan struct{}
// WaitQuiesce：等 pump 結束；逾時回 error（呼叫端據此升級為 Terminate）。
func WaitQuiesce(done <-chan struct{}, timeout time.Duration) error
// CloseSequence —— claude session 收尾的固定順序編排（第六輪 P1-3/P2-2 修訂；
// 全部使用 ports.Exit，appcore 不 import proc）：
//   close() → WaitQuiesce(done, quiesceTimeout)
//     逾時 → terminate()，再以 killTimeout 等第二次；仍未關 →
//     不呼叫 wait()（可能同樣阻塞），以 Exit{Exited:false} 盡力 finalize——
//     稽核端（meta ExitCode）維持 nil、不得偽裝 exit 0（第七輪 P1-3）；
//     回含兩次 timeout 的 error（quiesce timeout 一律保留，不被 terminate 吞掉）。
//   done 已關 → exit = wait()（cached Exit）→ finalize(exit)（recorder meta 的
//   ExitCode 有明確資料路徑）。
// 回傳 exit 與 join 後的錯誤；順序由測試以 call log 固定。
func CloseSequence(close func() error, done <-chan struct{},
	quiesceTimeout, killTimeout time.Duration,
	terminate func() error, wait func() ports.Exit,
	finalize func(ports.Exit) error) (ports.Exit, error)
```

**Usage 收斂規則（emitLocked 內）**：`KindUsage` → totals 覆寫；`KindResult` → cost 累加，usage 依 `ClaudeUsageCumulative` 覆寫或逐欄累加；兩者輸出前 `env.Usage = &totalsSnapshot`。

- [ ] **Step 1: 寫失敗測試（recording_test.go）**

```go
package appcore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type countingRec struct {
	closes atomic.Int32
	fail   error
}

func (c *countingRec) Line(b []byte) error { return nil }
func (c *countingRec) CloseWith(m recorder.Meta) error {
	c.closes.Add(1)
	return c.fail
}

func TestLeaseFinalizeExactlyOnceUnderBarrier(t *testing.T) {
	rec := &countingRec{}
	var stops atomic.Int32
	var gotExit ports.Exit
	l := NewRecordingLease(rec, func() error { stops.Add(1); return nil },
		func(ex ports.Exit) recorder.Meta {
			gotExit = ex
			m := recorder.Meta{Provider: "claude"}
			if ex.Exited { // 未知 exit → ExitCode 維持 nil（第七輪 P1-3）
				code := ex.Code
				m.ExitCode = &code
			}
			return m
		})
	begin := make(chan struct{})
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range 8 { // EndSession/new session/shutdown/fatal 併發觸發（帶不同 exit）
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			errs[i] = l.Finalize(ports.Exit{Exited: true, Code: 143})
		}(i)
	}
	close(begin)
	wg.Wait()
	if stops.Load() != 1 || rec.closes.Load() != 1 {
		t.Fatalf("stop=%d close=%d, want exactly once", stops.Load(), rec.closes.Load())
	}
	for _, e := range errs { // 全部呼叫者拿到同一（nil）結果
		if e != nil {
			t.Fatalf("idempotent result mismatch: %v", e)
		}
	}
	if !gotExit.Exited || gotExit.Code != 143 { // meta 的 ExitCode 來自 Finalize 的 Exit
		t.Fatalf("meta exit = %+v, want Exited 143", gotExit)
	}
	if !l.Finalized() {
		t.Fatal("finalized flag")
	}
}

func TestLeaseUnknownExitLeavesMetaNil(t *testing.T) { // 第七輪 P1-3：stuck 路徑稽核證據
	rec := &countingRec{}
	var gotMeta recorder.Meta
	l := NewRecordingLease(rec, func() error { return nil },
		func(ex ports.Exit) recorder.Meta {
			m := recorder.Meta{Provider: "claude", StderrTail: ex.StderrTail}
			if ex.Exited {
				code := ex.Code
				m.ExitCode = &code
			}
			gotMeta = m
			return m
		})
	if err := l.Finalize(ports.Exit{Exited: false}); err != nil {
		t.Fatal(err)
	}
	if gotMeta.ExitCode != nil { // 未知不得寫成 exit 0
		t.Fatalf("unknown exit must leave meta ExitCode nil, got %d", *gotMeta.ExitCode)
	}
}

func TestLeaseFirstErrorRetained(t *testing.T) {
	boom := errors.New("close failed")
	rec := &countingRec{fail: boom}
	l := NewRecordingLease(rec, func() error { return nil },
		func(ports.Exit) recorder.Meta { return recorder.Meta{} })
	if err := l.Finalize(ports.Exit{}); !errors.Is(err, boom) {
		t.Fatalf("first error: %v", err)
	}
	if err := l.Finalize(ports.Exit{Exited: true, Code: 9}); !errors.Is(err, boom) { // 後續冪等回同一錯誤、ex 忽略
		t.Fatalf("repeat must return same error: %v", err)
	}
	if rec.closes.Load() != 1 {
		t.Fatal("CloseWith must not repeat")
	}
}

func TestLeaseStopErrorJoined(t *testing.T) {
	stopErr := errors.New("stop failed")
	rec := &countingRec{}
	l := NewRecordingLease(rec, func() error { return stopErr },
		func(ports.Exit) recorder.Meta { return recorder.Meta{} })
	if err := l.Finalize(ports.Exit{}); !errors.Is(err, stopErr) {
		t.Fatalf("stop error must surface: %v", err)
	}
	if rec.closes.Load() != 1 { // stop 失敗仍要 CloseWith（meta 盡力寫，同 M0 recorder 慣例）
		t.Fatal("CloseWith must still run after stop error")
	}
}
```

- [ ] **Step 2: 寫失敗測試（manager_test.go）**

```go
package appcore

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
)

type memSink struct {
	mu   sync.Mutex
	rows []contract.Envelope
	fail error
}

func (s *memSink) Write(e contract.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.rows = append(s.rows, e)
	return nil
}
func (s *memSink) Close() error { return nil }

func newTestManager(sink *memSink) (*Manager, *[]contract.Envelope, *sync.Mutex) {
	var got []contract.Envelope
	var mu sync.Mutex
	m := New(Config{Sink: sink, Emit: func(e contract.Envelope) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}})
	return m, &got, &mu
}

// startActive：以 StartSession 交易把 manager 帶到 phaseActive，並完成 boot turn
// （Emit result → reducer 進 done）——這才是多輪的真實前置狀態：下一輪在上一輪
// result 解鎖後送出，user message 必須觸發 done → waiting（第十輪 P1-1）。
func startActive(t *testing.T, m *Manager) {
	t.Helper()
	id, err := m.BeginNewSessionSubmit("task-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s0", "boot"); err != nil {
		t.Fatal(err)
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
}

func TestUsageSemantics(t *testing.T) {
	m, got, _ := newTestManager(&memSink{})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 1}})
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindUsage, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 3}})
	if _, u := m.Totals(); u.InputTokens != 15 || u.OutputTokens != 3 { // snapshot 覆寫：非 25
		t.Fatalf("codex snapshot must overwrite: %+v", u)
	}
	m.NewSession("t2")
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.1, Usage: &contract.Usage{InputTokens: 5, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 0.2, Usage: &contract.Usage{InputTokens: 7, OutputTokens: 4}})
	cost, u := m.Totals()
	if u.InputTokens != 12 || u.OutputTokens != 6 || cost < 0.299 || cost > 0.301 { // per-turn 累加（VERDICT=per-turn 組態）
		t.Fatalf("claude must accumulate: %+v cost=%v", u, cost)
	}
	last := (*got)[len(*got)-2] // 最後 result envelope（其後是 state_change）
	if last.Usage == nil || last.Usage.InputTokens != 12 {
		t.Fatalf("emitted usage must be cumulative snapshot: %+v", last.Usage)
	}
	if last.UsageSemantics != "session_total" { // P2-1：per-turn 累加 → session_total
		t.Fatalf("semantics = %q", last.UsageSemantics)
	}
}

func TestUsageSemanticsCumulativeOverwrite(t *testing.T) { // P2-1：ClaudeUsageCumulative=true 分支
	var got []contract.Envelope
	var mu sync.Mutex
	m := New(Config{Sink: &memSink{}, ClaudeUsageCumulative: true,
		Emit: func(e contract.Envelope) { mu.Lock(); got = append(got, e); mu.Unlock() }})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 10, OutputTokens: 2}})
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 15, OutputTokens: 5}})
	if _, u := m.Totals(); u.InputTokens != 15 || u.OutputTokens != 5 { // 覆寫：非 25
		t.Fatalf("cumulative mode must overwrite: %+v", u)
	}
	mu.Lock()
	defer mu.Unlock()
	last := got[len(got)-2]
	if last.UsageSemantics != "provider_latest" { // 覆寫制 → provider_latest
		t.Fatalf("semantics = %q", last.UsageSemantics)
	}
}

func TestNewSessionResetsAtomically(t *testing.T) {
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}"),
		CostUSD: 1, Usage: &contract.Usage{InputTokens: 9}})
	_, _ = m.BeginSubmit() // 殘留的 pending 也要被 reset 清掉
	m.NewSession("task-B")
	if cost, u := m.Totals(); cost != 0 || u.InputTokens != 0 {
		t.Fatal("totals must reset")
	}
	if m.State() != contract.StateIdle {
		t.Fatal("reducer must reset")
	}
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	if m.State() != contract.StateStreaming { // reset 後事件直通（不被舊 pending 卡住）
		t.Fatal("events must flow after reset")
	}
}

func TestSubmitCoordinatorOrdering(t *testing.T) { // provider 事件先到也不得排在 user 前
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got) // boot 事件基準線，斷言只看其後
	mu.Unlock()
	id, err := m.BeginSubmit()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // provider goroutine 在 acceptance 前送出 delta + completed
		defer wg.Done()
		m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindDelta, Raw: []byte("{}"), Text: "early"})
		m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindResult, Raw: []byte("{}")})
	}()
	wg.Wait() // 事件已抵達（進 queue）
	mu.Lock()
	if len(*got) != base {
		mu.Unlock()
		t.Fatal("queued events must not emit before acceptance")
	}
	mu.Unlock()
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	kinds := []string{}
	for _, e := range (*got)[base:] {
		kinds = append(kinds, e.Kind+"/"+e.Role)
	}
	// 完整凍結序列（boot turn 已 done）：user → waiting → queued provider events
	if !strings.HasPrefix(strings.Join(kinds, ","),
		"message/user,state_change/system,delta/assistant") {
		t.Fatalf("order broken: %v", kinds)
	}
	if (*got)[base+1].State != string(contract.StateWaiting) {
		t.Fatalf("first state_change must be waiting: %+v", (*got)[base+1])
	}
}

func TestStartSessionOwnershipBarrier(t *testing.T) { // 第五輪 P1-2：production path 的原子交易
	m, _, _ := newTestManager(&memSink{})
	var providerStarts atomic.Int32 // injected provider-start：只有取得 reservation 才會呼叫
	begin := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-begin
			id, err := m.BeginNewSessionSubmit(fmt.Sprintf("task-%d", i))
			if err != nil {
				results <- err
				return
			}
			providerStarts.Add(1) // 模擬 process/recorder/pump 建立
			results <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
		}(i)
	}
	close(begin)
	e1, e2 := <-results, <-results
	winners := 0
	for _, e := range []error{e1, e2} {
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrSubmitActive) && !errors.Is(e, ErrSessionActive) {
			t.Fatalf("loser must fail with ownership error, got %v", e)
		}
	}
	if winners != 1 || providerStarts.Load() != 1 { // 恰一個成功、恰一次 provider start
		t.Fatalf("winners=%d starts=%d", winners, providerStarts.Load())
	}
	if !m.SessionActive() {
		t.Fatal("winner's session must be active")
	}
	if _, err := m.BeginNewSessionSubmit("task-next"); !errors.Is(err, ErrSessionActive) { // active 期間拒絕
		t.Fatalf("active session must reject new StartSession: %v", err)
	}
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginNewSessionSubmit("task-next"); err != nil { // End 後可再開
		t.Fatal(err)
	}
}

func TestEndDuringStartingIsRejected(t *testing.T) { // 第七輪 P1-2：deterministic 命中 starting
	m, _, _ := newTestManager(&memSink{})
	providerStarted := make(chan struct{}) // Start 已取得 reservation 且 provider-start 已發生
	release := make(chan struct{})         // 放行 Accept
	startDone := make(chan error, 1)
	go func() {
		id, err := m.BeginNewSessionSubmit("task-A")
		if err != nil {
			startDone <- err
			return
		}
		close(providerStarted) // 模擬 process/recorder/pump 已建立
		<-release
		startDone <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	}()
	<-providerStarted // 確定命中 starting phase（不靠排程運氣）
	if _, err := m.BeginEndSession(); !errors.Is(err, ErrStartInProgress) {
		t.Fatalf("End during starting must be ErrStartInProgress, got %v", err)
	}
	close(release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if !m.SessionActive() {
		t.Fatal("session must be active after Accept")
	}
	tok, err := m.BeginEndSession() // Accept 之後 End 正常
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
}

func TestSendThenEndBarrier(t *testing.T) { // 第九輪 P1-1：pending submit 期間 End 必拒
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	id, err := m.BeginSubmit()
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{}) // provider Send 已發出（尚未 Accept）
	release := make(chan struct{})
	acceptDone := make(chan error, 1)
	go func() {
		close(sent) // 模擬 provider call 已送出
		<-release
		acceptDone <- m.AcceptSubmit(id, contract.ProviderClaude, "s1", "round")
	}()
	<-sent
	if _, err := m.BeginEndSession(); !errors.Is(err, ErrSubmitActive) { // teardown 不得與 pending submit 重疊
		t.Fatalf("End during pending submit must be ErrSubmitActive: %v", err)
	}
	close(release)
	if err := <-acceptDone; err != nil { // Accept 不受影響、無晚到寫入
		t.Fatal(err)
	}
	if err := EndSessionFlow(m, nil, func() error { return nil }); err != nil { // 之後可正常 End
		t.Fatal(err)
	}
	if m.SessionActive() {
		t.Fatal("session must end cleanly after accept")
	}
}

func TestEndThenSendRejected(t *testing.T) { // 第九輪 P1-1：ending 期間 Send 必拒
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); !errors.Is(err, ErrEndInProgress) { // teardown 期間不得起新 provider request
		t.Fatalf("Send during ending must be ErrEndInProgress: %v", err)
	}
	if err := m.FinishEndSession(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); !errors.Is(err, ErrNoSession) { // 結束後 Send 需先開新 session
		t.Fatalf("Send after end must be ErrNoSession: %v", err)
	}
}

func TestEndSessionFlowBusyThenRetry(t *testing.T) { // 第八輪 P1-1：busy 不殘留 ending
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hi")
	busy := true
	teardowns := 0
	err := EndSessionFlow(m, func() bool { return busy }, func() error { teardowns++; return nil })
	if !errors.Is(err, ErrProviderBusy) {
		t.Fatalf("busy must surface: %v", err)
	}
	if teardowns != 0 {
		t.Fatal("teardown must not run when busy")
	}
	if !m.SessionActive() { // phase 恢復 active，不殘留 ending
		t.Fatal("busy end must restore active")
	}
	busy = false
	if err := EndSessionFlow(m, func() bool { return busy }, func() error { teardowns++; return nil }); err != nil {
		t.Fatalf("retry end must succeed: %v", err)
	}
	if teardowns != 1 || m.SessionActive() {
		t.Fatalf("teardown=%d active=%v", teardowns, m.SessionActive())
	}
}

func TestEndSessionFlowTeardownErrorStillFinishes(t *testing.T) { // 第八輪 P1-1
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	boom := errors.New("teardown failed")
	err := EndSessionFlow(m, nil, func() error { return boom })
	if !errors.Is(err, boom) { // teardown 錯誤保留
		t.Fatalf("teardown error must surface: %v", err)
	}
	if m.SessionActive() { // 不殘留 ending / active——已 Finish
		t.Fatal("teardown error must still finish end")
	}
	if _, err := m.BeginNewSessionSubmit("task-B"); err != nil { // 可再開新 session
		t.Fatalf("new session after failed teardown: %v", err)
	}
}

func TestEndSessionFlowIdempotentNoSession(t *testing.T) {
	m, _, _ := newTestManager(&memSink{})
	if err := EndSessionFlow(m, nil, func() error {
		t.Fatal("teardown must not run without session")
		return nil
	}); err != nil {
		t.Fatalf("no-session end must be nil: %v", err)
	}
}

func TestCancelEndSessionRestoresActive(t *testing.T) { // 第七輪 P1-2：ending 復原
	m, _, _ := newTestManager(&memSink{})
	id, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hi")
	tok, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	// 模擬：進 ending 後 teardown 前置檢查失敗（codex busy）→ Cancel 復原
	if err := m.CancelEndSession(tok); err != nil {
		t.Fatal(err)
	}
	if !m.SessionActive() {
		t.Fatal("cancel must restore active phase")
	}
	if err := m.FinishEndSession(tok); !errors.Is(err, ErrStaleSession) { // 已 Cancel 的 token 失效
		t.Fatalf("finish after cancel must be stale: %v", err)
	}
	tok2, err := m.BeginEndSession() // 可再次正常 End
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tok2); err != nil {
		t.Fatal(err)
	}
	if m.SessionActive() {
		t.Fatal("session must be inactive after finish")
	}
}

func TestStaleEndAfterNewSessionIsNoop(t *testing.T) { // 第六輪 P1-2：舊 End 不得清掉新 session
	m, _, _ := newTestManager(&memSink{})
	idA, _ := m.BeginNewSessionSubmit("task-A")
	_ = m.AcceptSubmit(idA, contract.ProviderClaude, "sA", "hi")
	tokA, err := m.BeginEndSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FinishEndSession(tokA); err != nil { // A 正常收尾
		t.Fatal(err)
	}
	idB, err := m.BeginNewSessionSubmit("task-B") // 新 session B
	if err != nil {
		t.Fatal(err)
	}
	_ = m.AcceptSubmit(idB, contract.ProviderClaude, "sB", "hi")
	if err := m.FinishEndSession(tokA); !errors.Is(err, ErrStaleSession) { // 延遲重放的舊 End
		t.Fatalf("stale end must error: %v", err)
	}
	if !m.SessionActive() { // B 不受影響
		t.Fatal("stale end must not deactivate new session")
	}
}

func TestEmitAfterCloseNoStateMutation(t *testing.T) { // closed 最先、內部狀態不變
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, _ := m.BeginSubmit() // pending 中 Close
	_ = m.Close()
	mu.Lock()
	uiAfterClose := len(*got) // abort 通知已在此之前發出
	mu.Unlock()
	sink.mu.Lock()
	rowsAfterClose := len(sink.rows)
	sink.mu.Unlock()
	stateBefore := m.State()
	_, usageBefore := m.Totals()
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}"),
		Usage: &contract.Usage{InputTokens: 99}})
	m.EmitApprovalRequest(contract.ProviderClaude, "s1", "Bash", []byte(`{}`))
	m.EmitApprovalDecision(contract.ProviderClaude, "s1", "allow", "")
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("accept after close must ErrClosed: %v", err)
	}
	if m.State() != stateBefore { // reducer 未動
		t.Fatal("state mutated after close")
	}
	if _, u := m.Totals(); u != usageBefore { // totals 未動
		t.Fatal("totals mutated after close")
	}
	sink.mu.Lock()
	if len(sink.rows) != rowsAfterClose { // close 後 sink 不得再寫
		sink.mu.Unlock()
		t.Fatal("sink written after close")
	}
	sink.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	aborted := 0
	for _, e := range (*got)[:uiAfterClose] {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closing during pending submission") {
			aborted++
		}
	}
	if aborted != 1 { // Close 對 pending submission 的 abort 通知
		t.Fatalf("abort notice count = %d, want 1", aborted)
	}
	dropped := 0
	for _, e := range (*got)[uiAfterClose:] {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closed: event dropped") {
			dropped++
		} else if e.Role == "user" {
			t.Fatal("no user envelope after close")
		}
	}
	if dropped != 3 { // 每個被丟棄的入口各一個 fail-loud（queue 不得增加）
		t.Fatalf("dropped stream_error count = %d, want 3", dropped)
	}
}

func TestCloseDuringPendingFlushesLoudly(t *testing.T) { // close 前入列事件不遺失
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, _ := m.BeginSubmit()
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindDelta,
		Raw: []byte("{}"), Text: "queued-before-close"})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	var inSink bool
	for _, r := range sink.rows {
		if r.Kind == "delta" && r.Text == "queued-before-close" {
			inSink = true
		}
	}
	sink.mu.Unlock()
	if !inSink { // flush 發生在 sink 關閉前：audit 不失事件
		t.Fatal("queued event must reach sink before close")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawDelta, sawNotice bool
	for _, e := range *got {
		if e.Kind == "delta" && e.Text == "queued-before-close" {
			sawDelta = true
		}
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "closing during pending submission") {
			sawNotice = true
		}
	}
	if !sawDelta || !sawNotice {
		t.Fatalf("flush+notice must both surface: delta=%v notice=%v", sawDelta, sawNotice)
	}
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("late accept must ErrClosed: %v", err)
	}
}

func TestApprovalDecisionDuringSubmitQueued(t *testing.T) { // resolveApprove 分支
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	stateBefore := m.State()
	id, _ := m.BeginSubmit()
	m.EmitApprovalRequest(contract.ProviderCodex, "t1", "commandExecution", []byte(`{}`))
	m.EmitApprovalDecision(contract.ProviderCodex, "t1", "allow", "")
	if m.State() != stateBefore { // flush 前 reducer 不動
		t.Fatalf("reducer moved before flush: %s", m.State())
	}
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var seq []string
	for _, e := range (*got)[base:] {
		if e.Kind == "state_change" {
			seq = append(seq, "state:"+e.State)
		} else {
			seq = append(seq, e.Kind)
		}
	}
	want := []string{"message", "state:waiting", "approval", "state:awaiting_approval",
		"approval_decision", "state:tool_running"} // 完整凍結序列（boot turn 已 done）
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
}

func TestCloseSequenceOrderTimeoutAndStuck(t *testing.T) { // 第六輪 P1-3/P2-2
	var calls []string
	var mu sync.Mutex
	log := func(s string) func() error {
		return func() error { mu.Lock(); calls = append(calls, s); mu.Unlock(); return nil }
	}
	fin := func(gotExit *ports.Exit) func(ports.Exit) error {
		return func(ex ports.Exit) error {
			mu.Lock()
			calls = append(calls, "finalize")
			mu.Unlock()
			*gotExit = ex
			return nil
		}
	}

	// 正常路徑：done 已關 → close,wait,finalize；finalize 收到 wait 的 Exit
	done := make(chan struct{})
	close(done)
	var finExit ports.Exit
	ex, err := CloseSequence(log("close"), done, time.Second, time.Second, log("terminate"),
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true, Code: 0} },
		fin(&finExit))
	if err != nil || !ex.Exited || ex.Code != 0 || !finExit.Exited || finExit.Code != 0 {
		t.Fatalf("normal: %v %+v %+v", err, ex, finExit)
	}
	if strings.Join(calls, ",") != "close,wait,finalize" {
		t.Fatalf("order = %v", calls)
	}

	// 逾時升級路徑：quiesce 逾時 → terminate → done 關 → wait(143) → finalize(143)；
	// 原始 quiesce timeout 必須保留在回傳錯誤內（P1-3）
	calls = nil
	hung := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); close(hung) }()
	ex, err = CloseSequence(log("close"), hung, 30*time.Millisecond, 5*time.Second,
		func() error { mu.Lock(); calls = append(calls, "terminate"); mu.Unlock(); return nil },
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true, Code: 143} },
		fin(&finExit))
	if err == nil || !strings.Contains(err.Error(), "quiesce timeout") { // timeout 不被吞
		t.Fatalf("quiesce timeout must surface: %v", err)
	}
	if !ex.Exited || ex.Code != 143 || !finExit.Exited || finExit.Code != 143 { // meta ExitCode 資料路徑
		t.Fatalf("exit evidence = %+v / %+v", ex, finExit)
	}
	if strings.Join(calls, ",") != "close,terminate,wait,finalize" {
		t.Fatalf("timeout order = %v", calls)
	}

	// 卡死路徑：done 永不關 → 界限內回錯、不呼叫 wait、仍盡力 finalize（零值 Exit）
	calls = nil
	stuck := make(chan struct{}) // 永不 close
	start := time.Now()
	_, err = CloseSequence(log("close"), stuck, 20*time.Millisecond, 30*time.Millisecond,
		log("terminate"),
		func() ports.Exit { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return ports.Exit{Exited: true} },
		fin(&finExit))
	if err == nil || !strings.Contains(err.Error(), "did not quiesce") {
		t.Fatalf("stuck path must error: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stuck path must return within bounds")
	}
	joined := strings.Join(calls, ",")
	if strings.Contains(joined, "wait") || !strings.Contains(joined, "finalize") {
		t.Fatalf("stuck path must skip wait but still finalize: %v", calls)
	}
	if finExit.Exited { // 第七輪 P1-3：未知不得偽裝 exit
		t.Fatalf("stuck path finalize must receive Exited=false: %+v", finExit)
	}
}

func TestBeginSubmitExclusiveBarrier(t *testing.T) { // 唯一 ownership
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	begin := make(chan struct{})
	results := make(chan error, 2)
	ids := make(chan SubmissionID, 2)
	for range 2 {
		go func() {
			<-begin
			id, err := m.BeginSubmit()
			results <- err
			if err == nil {
				ids <- id
			}
		}()
	}
	close(begin)
	e1, e2 := <-results, <-results
	if !((e1 == nil && errors.Is(e2, ErrSubmitActive)) || (e2 == nil && errors.Is(e1, ErrSubmitActive))) {
		t.Fatalf("exactly one Begin must win: %v / %v", e1, e2)
	}
	winner := <-ids
	if err := m.RejectSubmit(winner); err != nil { // 贏家可正常結束
		t.Fatal(err)
	}
	if _, err := m.BeginSubmit(); err != nil { // 結束後可再 Begin
		t.Fatal(err)
	}
}

func TestStaleAcceptAfterNewSessionIsNoop(t *testing.T) { // generation 失效
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	id, _ := m.BeginSubmit()
	m.NewSession("new-task") // 舊 ID 失效
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "stale hello"); !errors.Is(err, ErrStaleSubmission) {
		t.Fatalf("stale accept must error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *got {
		if e.Text == "stale hello" {
			t.Fatal("stale accept must not emit its user envelope")
		}
		if e.TaskID == "new-task" && e.Kind == "message" {
			t.Fatal("no message may carry new task from stale submission")
		}
	}
}

func TestApprovalDuringSubmitQueued(t *testing.T) { // approval 不繞過 coordinator
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit()
	m.EmitApprovalRequest(contract.ProviderCodex, "t1", "commandExecution", []byte(`{"k":"v"}`)) // early approval
	mu.Lock()
	if len(*got) != base {
		mu.Unlock()
		t.Fatal("approval during pending must be queued, not emitted")
	}
	mu.Unlock()
	if m.State() == contract.StateAwaitingApproval { // side effect 也必須延後
		t.Fatal("reducer must not transition before flush")
	}
	if err := m.AcceptSubmit(id, contract.ProviderCodex, "t1", "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	kinds := []string{}
	for _, e := range (*got)[base:] {
		kinds = append(kinds, e.Kind+"/"+e.State)
	}
	// 完整凍結序列：user → waiting → approval → awaiting_approval
	want := []string{"message/", "state_change/waiting", "approval/", "state_change/awaiting_approval"}
	for i, w := range want {
		parts := strings.SplitN(w, "/", 2)
		if kinds[i] != parts[0]+"/"+parts[1] {
			t.Fatalf("order[%d] = %s, want %s (all: %v)", i, kinds[i], w, kinds)
		}
	}
	if m.State() != contract.StateAwaitingApproval {
		t.Fatalf("final state = %s", m.State())
	}
}

func TestRejectSubmitEmitsNoUser(t *testing.T) {
	m, got, mu := newTestManager(&memSink{})
	startActive(t, m)
	mu.Lock()
	base := len(*got)
	mu.Unlock()
	id, _ := m.BeginSubmit()
	m.Emit(contract.Event{Provider: contract.ProviderCodex, Kind: contract.KindSystemOther, Raw: []byte("{}")})
	if err := m.RejectSubmit(id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range (*got)[base:] {
		if e.Role == "user" {
			t.Fatal("rejected submit must not emit user envelope")
		}
	}
	if len(*got) != base+1 { // queue 照常 flush
		t.Fatalf("queued events must flush on reject: %d", len(*got)-base)
	}
}

func TestUserMessageEntersWaiting(t *testing.T) {
	m, _, _ := newTestManager(&memSink{})
	startActive(t, m)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")})
	id, _ := m.BeginSubmit()
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "round 2"); err != nil {
		t.Fatal(err)
	}
	if m.State() != contract.StateWaiting {
		t.Fatalf("after submit state = %s, want waiting", m.State())
	}
}

func TestCloseEmitBarrier(t *testing.T) { // 第四輪 P1-4：Close 與 Emit 交錯
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			for range 20 {
				m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-begin
		_ = m.Close()
	}()
	close(begin)
	wg.Wait() // -race 驗證無競態；close 後的 Emit 走 fail-loud
	sink.mu.Lock()
	sinkRows := len(sink.rows)
	sink.mu.Unlock()
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	sink.mu.Lock()
	if len(sink.rows) != sinkRows {
		sink.mu.Unlock()
		t.Fatal("Emit after Close must not write sink")
	}
	sink.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	last := (*got)[len(*got)-1]
	if last.Kind != string(contract.KindStreamError) || !strings.Contains(last.Error, "closed") {
		t.Fatalf("post-close emit must surface stream_error: %+v", last)
	}
}

func TestApprovalFlowThroughEnvelopes(t *testing.T) {
	m, got, mu := newTestManager(&memSink{})
	m.EmitApprovalRequest(contract.ProviderClaude, "s1", "Bash", []byte(`{"k":"v"}`))
	if m.State() != contract.StateAwaitingApproval {
		t.Fatalf("state = %s", m.State())
	}
	m.EmitApprovalDecision(contract.ProviderClaude, "s1", "timeout", "")
	if m.State() != contract.StateToolRunning {
		t.Fatalf("timeout must leave awaiting: %s", m.State())
	}
	mu.Lock()
	defer mu.Unlock()
	var kinds []string
	for _, e := range *got {
		kinds = append(kinds, e.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "approval") || !strings.Contains(joined, "approval_decision") {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestAuditFailureIsLoud(t *testing.T) {
	sink := &memSink{fail: errors.New("disk full")}
	m, got, mu := newTestManager(sink)
	m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
	if m.AuditErr() == nil {
		t.Fatal("audit error must latch")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawStreamErr bool
	for _, e := range *got {
		if e.Kind == string(contract.KindStreamError) && strings.Contains(e.Error, "disk full") {
			sawStreamErr = true
		}
	}
	if !sawStreamErr {
		t.Fatal("sink failure must surface to UI as stream_error envelope")
	}
	for i := 1; i < len(*got); i++ { // 第四輪 P1-4：fail-loud 路徑 ID 仍嚴格遞增
		if (*got)[i].EventID <= (*got)[i-1].EventID {
			t.Fatalf("event_id order broken in fail-loud path at %d: %s <= %s",
				i, (*got)[i].EventID, (*got)[i-1].EventID)
		}
	}
}

func TestPumpQuiesceBeforeNewSession(t *testing.T) { // 第四輪 P1-3：晚到事件不進新 task
	m, got, mu := newTestManager(&memSink{})
	m.NewSession("old-task")
	ch := make(chan contract.Event, 8)
	done := Pump(ch, m.Emit)
	ch <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")}
	ch <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindResult, Raw: []byte("{}")}
	close(ch) // provider 收尾（EndSession 的 Close → EOF）
	if err := WaitQuiesce(done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	m.NewSession("new-task") // quiesce 完成後才換代
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *got {
		if e.TaskID == "new-task" {
			t.Fatalf("old pump event leaked into new task: %+v", e)
		}
		if e.TaskID != "old-task" && e.TaskID != "" {
			t.Fatalf("unexpected task id: %+v", e)
		}
	}
}

func TestWaitQuiesceTimeout(t *testing.T) {
	ch := make(chan contract.Event)
	done := Pump(ch, func(contract.Event) {})
	if err := WaitQuiesce(done, 50*time.Millisecond); err == nil { // channel 未關 → 逾時（呼叫端升級 Terminate）
		t.Fatal("quiesce must time out when pump still running")
	}
	close(ch)
	if err := WaitQuiesce(done, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentEmitOrderAndAdjacency(t *testing.T) {
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			for range 25 {
				m.Emit(contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindDelta, Raw: []byte("{}")})
			}
		}()
	}
	close(begin)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(*got) < 200 {
		t.Fatalf("events lost: %d", len(*got))
	}
	for i := 1; i < len(*got); i++ {
		if (*got)[i].EventID <= (*got)[i-1].EventID {
			t.Fatalf("event_id order broken at %d", i)
		}
	}
	if (*got)[0].Kind != "delta" || (*got)[1].Kind != "state_change" {
		t.Fatalf("state_change must be adjacent: %s,%s", (*got)[0].Kind, (*got)[1].Kind)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != len(*got) {
		t.Fatalf("audit rows %d != emitted %d", len(sink.rows), len(*got))
	}
}

func TestEmitUserMessage(t *testing.T) { // AcceptSubmit 產生的 user envelope 同源進 UI 與稽核
	sink := &memSink{}
	m, got, mu := newTestManager(sink)
	startActive(t, m)
	id, err := m.BeginSubmit()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptSubmit(id, contract.ProviderClaude, "s1", "hello there"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	e := (*got)[len(*got)-2] // 末二：user message（其後跟 done→waiting 的 state_change）
	if e.Role != "user" || e.Kind != "message" || e.Text != "hello there" || e.SessionID != "s1" {
		t.Fatalf("user envelope = %+v", e)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != len(*got) {
		t.Fatal("user message must reach audit too")
	}
}
```

- [ ] **Step 3: 確認失敗** — `go test ./internal/appcore/ -race -v` → FAIL。

- [ ] **Step 4: 實作**

`recording.go`：

```go
package appcore

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
)

type RecorderHandle interface {
	Line(b []byte) error
	CloseWith(m recorder.Meta) error
}

type RecordingLease struct {
	once   sync.Once
	stop   func() error
	rec    RecorderHandle
	metaFn func(ports.Exit) recorder.Meta
	err    error
	done   atomic.Bool
}

func NewRecordingLease(rec RecorderHandle, stop func() error,
	metaFn func(ports.Exit) recorder.Meta) *RecordingLease {
	return &RecordingLease{rec: rec, stop: stop, metaFn: metaFn}
}

// Finalize 冪等；首次呼叫的 ex 進 metaFn（後續呼叫的 ex 忽略、回傳首次結果）。
func (l *RecordingLease) Finalize(ex ports.Exit) error {
	l.once.Do(func() {
		stopErr := l.stop()
		closeErr := l.rec.CloseWith(l.metaFn(ex)) // stop 失敗仍 CloseWith（meta 盡力寫）
		l.err = errors.Join(stopErr, closeErr)
		l.done.Store(true)
	})
	return l.err
}

func (l *RecordingLease) Finalized() bool { return l.done.Load() }
```

`pump.go`：

```go
package appcore

import (
	"errors"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
)

// Pump 把事件 channel 逐一送進 emit；channel 關閉時關閉回傳的 done。
func Pump(events <-chan contract.Event, emit func(contract.Event)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			emit(ev)
		}
	}()
	return done
}

// WaitQuiesce 等 pump 結束；逾時回 error（呼叫端據此升級 Terminate）。
func WaitQuiesce(done <-chan struct{}, timeout time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("appcore: pump quiesce timeout")
	}
}

// CloseSequence：claude session 收尾固定順序（第六輪 P1-3/P2-2）。
func CloseSequence(closeFn func() error, done <-chan struct{},
	quiesceTimeout, killTimeout time.Duration,
	terminate func() error, wait func() ports.Exit,
	finalize func(ports.Exit) error) (ports.Exit, error) {
	closeErr := closeFn()
	qErr := WaitQuiesce(done, quiesceTimeout) // 原始 timeout 一律保留（P1-3）
	var termErr, killErr error
	if qErr != nil {
		termErr = terminate()
		if killErr = WaitQuiesce(done, killTimeout); killErr != nil {
			// pump 卡死：wait() 可能同樣阻塞——以 Exit{Exited:false} 盡力 finalize
			// （meta ExitCode 維持 nil），界限內回錯
			unknown := ports.Exit{Exited: false}
			finErr := finalize(unknown)
			return unknown, errors.Join(closeErr, qErr, termErr,
				errors.New("appcore: pump did not quiesce after terminate"), finErr)
		}
	}
	ex := wait() // done 已關：cached Exit（finalize 的 exit 證據）
	finErr := finalize(ex)
	return ex, errors.Join(closeErr, qErr, termErr, finErr)
}

var ErrProviderBusy = errors.New("appcore: provider busy; cannot end session now")

// EndSessionFlow：EndSession 的單一編排（第八輪 P1-1）。
// busyCheck 為 teardown 前置檢查（nil = 無）；true → Cancel + ErrProviderBusy（phase 復原）。
// teardown 一旦開始，無論成敗都 FinishEndSession；回傳 errors.Join(teardownErr, finishErr)。
func EndSessionFlow(m *Manager, busyCheck func() bool, teardown func() error) error {
	tok, err := m.BeginEndSession()
	if errors.Is(err, ErrNoSession) {
		return nil // 冪等
	}
	if err != nil {
		return err
	}
	if busyCheck != nil && busyCheck() {
		cerr := m.CancelEndSession(tok) // 第九輪 P1-1：Cancel 錯誤保留、不吞
		return errors.Join(ErrProviderBusy, cerr)
	}
	tearErr := teardown()
	finErr := m.FinishEndSession(tok)
	return errors.Join(tearErr, finErr)
}
```

`manager.go`（核心邏輯全文）：

```go
package appcore

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

var (
	ErrSubmitActive    = errors.New("appcore: submission already in progress")
	ErrSessionActive   = errors.New("appcore: session already active; end it first")
	ErrStaleSubmission = errors.New("appcore: stale submission id")
	ErrNoSession       = errors.New("appcore: no active session")
	ErrStartInProgress = errors.New("appcore: session start in progress")
	ErrEndInProgress   = errors.New("appcore: session end in progress")
	ErrStaleSession    = errors.New("appcore: stale session token")
	ErrClosed          = errors.New("appcore: manager closed")
)

type SubmissionID struct{ gen, seq uint64 }

// SessionToken：session lifecycle token（generation + end 序號）；
// Cancel／Finish 只認目前 outstanding 的一枚。
type SessionToken struct{ gen, seq uint64 }

type Config struct {
	Sink                  AuditSink
	Emit                  func(env contract.Envelope)
	ClaudeUsageCumulative bool
}

type pendingEntry struct {
	ev             contract.Event
	resolveApprove bool // EmitApprovalDecision 的 reducer side effect 隨事件入列（P1-2）
}

type Manager struct {
	mu         sync.Mutex
	cfg        Config
	reducer    *contract.Reducer
	taskID     string
	totalCost  float64
	totalUsage contract.Usage
	auditErr   error
	closed     bool

	gen        uint64 // 換代遞增：舊 SubmissionID／SessionToken 全部失效
	seq        uint64
	submitting *SubmissionID // nil = 無 owner
	fromNewSession bool      // reservation 來自 BeginNewSessionSubmit
	phase      sessionPhase  // idle/starting/active/ending 狀態機
	sessionGen uint64        // 現任 session 的 generation
	endSeq     uint64        // BeginEndSession 每次遞增
	endTok     *SessionToken // 目前 outstanding 的 end token（nil = 無）
	pendingBuf []pendingEntry
}

type sessionPhase int

const (
	phaseIdle sessionPhase = iota
	phaseStarting
	phaseActive
	phaseEnding
)

func New(cfg Config) *Manager {
	return &Manager{cfg: cfg, reducer: contract.NewReducer()}
}

// newSessionLocked：flush 殘留 queue（掛舊 task）→ 換代 → 重設。
func (m *Manager) newSessionLocked(taskID string) {
	m.flushLocked()
	m.gen++ // 舊 SubmissionID／SessionToken 失效
	m.submitting, m.fromNewSession = nil, false
	m.phase, m.endTok = phaseIdle, nil
	m.reducer.Reset()
	m.taskID = taskID
	m.totalCost, m.totalUsage = 0, contract.Usage{}
}

func (m *Manager) NewSession(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newSessionLocked(taskID)
}

// BeginNewSessionSubmit：StartSession 的單一 ownership 交易。
func (m *Manager) BeginNewSessionSubmit(taskID string) (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	switch m.phase {
	case phaseActive, phaseEnding:
		return SubmissionID{}, ErrSessionActive
	case phaseStarting:
		return SubmissionID{}, ErrSubmitActive
	}
	if m.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	m.newSessionLocked(taskID)
	m.seq++
	id := SubmissionID{gen: m.gen, seq: m.seq}
	m.submitting, m.fromNewSession = &id, true
	m.phase = phaseStarting
	return id, nil
}

// BeginEndSession：進入 ending phase 並取得 token（第六輪 P1-2）。
func (m *Manager) BeginEndSession() (SessionToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SessionToken{}, ErrClosed
	}
	switch m.phase {
	case phaseIdle:
		return SessionToken{}, ErrNoSession
	case phaseStarting:
		return SessionToken{}, ErrStartInProgress // Start 未 Accept 前 End 不得無聲成功
	case phaseEnding:
		return SessionToken{}, ErrEndInProgress
	}
	if m.submitting != nil { // 第九輪 P1-1：pending submit 期間拒絕 End——
		return SessionToken{}, ErrSubmitActive // 舊輪事件不得在 session 結束後才落地
	}
	m.phase = phaseEnding
	m.endSeq++
	tok := SessionToken{gen: m.sessionGen, seq: m.endSeq}
	m.endTok = &tok
	return tok, nil
}

// CancelEndSession：ending → active 復原（teardown 前）；stale token no-op error。
func (m *Manager) CancelEndSession(t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phaseEnding || m.endTok == nil || *m.endTok != t {
		return ErrStaleSession
	}
	m.phase = phaseActive
	m.endTok = nil
	return nil
}

// FinishEndSession：收尾完成；stale token 一律 no-op error。
func (m *Manager) FinishEndSession(t SessionToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != phaseEnding || m.endTok == nil || *m.endTok != t {
		return ErrStaleSession
	}
	m.phase = phaseIdle
	m.endTok = nil
	return nil
}

func (m *Manager) SessionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.phase == phaseActive
}

func (m *Manager) Emit(ev contract.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // 第五輪 P1-4：closed 最先——不 queue、不 Apply、不動 totals
		m.emitClosedDroppedLocked(string(ev.Kind), string(ev.Provider))
		return
	}
	if m.submitting != nil { // coordinator queue
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(ev)
}

// BeginSubmit：既有 session 的後續輪。第九輪 P1-1：僅 phaseActive 允許——
// idle → ErrNoSession、starting → ErrStartInProgress、ending → ErrEndInProgress
// （teardown 期間不得啟動新 provider request）。
func (m *Manager) BeginSubmit() (SubmissionID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return SubmissionID{}, ErrClosed
	}
	switch m.phase {
	case phaseIdle:
		return SubmissionID{}, ErrNoSession
	case phaseStarting:
		return SubmissionID{}, ErrStartInProgress
	case phaseEnding:
		return SubmissionID{}, ErrEndInProgress
	}
	if m.submitting != nil {
		return SubmissionID{}, ErrSubmitActive
	}
	m.seq++
	id := SubmissionID{gen: m.gen, seq: m.seq}
	m.submitting = &id
	return id, nil
}

func (m *Manager) checkOwnerLocked(id SubmissionID) error {
	if m.closed {
		return ErrClosed
	}
	if m.submitting == nil || *m.submitting != id || id.gen != m.gen {
		return ErrStaleSubmission
	}
	return nil
}

func (m *Manager) AcceptSubmit(id SubmissionID, provider contract.Provider, sessionID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkOwnerLocked(id); err != nil {
		return err
	}
	if m.fromNewSession { // StartSession 路徑：provider 接受即 session 存活
		m.phase = phaseActive
		m.sessionGen = id.gen // 現任 session 的 token generation
	}
	m.submitting, m.fromNewSession = nil, false
	m.emitLocked(contract.Event{Provider: provider, Kind: contract.KindMessage,
		Role: "user", SessionID: sessionID, Text: text,
		Raw: []byte(`{"source":"workbench_user_input"}`)})
	m.flushLocked()
	return nil
}

func (m *Manager) RejectSubmit(id SubmissionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkOwnerLocked(id); err != nil {
		return err
	}
	if m.fromNewSession { // StartSession 失敗：回 idle
		m.phase = phaseIdle
	}
	m.submitting, m.fromNewSession = nil, false
	m.flushLocked()
	return nil
}

// emitClosedDroppedLocked：close 後的唯一輸出——單一 UI stream_error（不寫 sink）。
func (m *Manager) emitClosedDroppedLocked(kind, provider string) {
	m.cfg.Emit(contract.Envelope{
		EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
		Provider: provider, Kind: string(contract.KindStreamError),
		Error: "manager closed: event dropped (kind=" + kind + ")",
	})
}

func (m *Manager) flushLocked() {
	buf := m.pendingBuf
	m.pendingBuf = nil
	for _, e := range buf {
		m.emitLocked(e.ev)
		if e.resolveApprove {
			if st, changed := m.reducer.ResolveApproval(); changed {
				m.emitStateLocked(e.ev.Provider, e.ev.SessionID, st)
			}
		}
	}
}

func (m *Manager) EmitApprovalRequest(provider contract.Provider, sessionID, toolName string, raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // 第五輪 P1-4：closed 最先
		m.emitClosedDroppedLocked(string(contract.KindApproval), string(provider))
		return
	}
	ev := contract.Event{Provider: provider, Kind: contract.KindApproval,
		SessionID: sessionID, Text: toolName, Raw: raw}
	if m.submitting != nil { // approval 同樣入 queue（side effect 由 emitLocked 的 Apply 產生）
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev})
		return
	}
	m.emitLocked(ev)
}

func (m *Manager) EmitApprovalDecision(provider contract.Provider, sessionID, decision, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed { // 第五輪 P1-4：closed 最先
		m.emitClosedDroppedLocked(string(contract.KindApprovalDecision), string(provider))
		return
	}
	ev := contract.Event{Provider: provider, Kind: contract.KindApprovalDecision,
		SessionID: sessionID, Text: decision, Thinking: reason,
		Raw: []byte(`{"decision":"` + decision + `"}`)}
	if m.submitting != nil {
		m.pendingBuf = append(m.pendingBuf, pendingEntry{ev: ev, resolveApprove: true})
		return
	}
	m.emitLocked(ev)
	if st, changed := m.reducer.ResolveApproval(); changed {
		m.emitStateLocked(provider, sessionID, st)
	}
}

func (m *Manager) emitLocked(ev contract.Event) {
	semantics := ""
	switch {
	case ev.Kind == contract.KindUsage && ev.Usage != nil: // codex snapshot：覆寫
		m.totalUsage = *ev.Usage
		semantics = "provider_latest"
	case ev.Kind == contract.KindResult:
		m.totalCost += ev.CostUSD
		if ev.Usage != nil {
			if m.cfg.ClaudeUsageCumulative {
				m.totalUsage = *ev.Usage
				semantics = "provider_latest"
			} else {
				m.totalUsage.InputTokens += ev.Usage.InputTokens
				m.totalUsage.OutputTokens += ev.Usage.OutputTokens
				m.totalUsage.CachedInput += ev.Usage.CachedInput
				semantics = "session_total"
			}
		}
	}
	env := contract.Wrap(ev, m.taskID)
	if ev.Kind == contract.KindUsage || ev.Kind == contract.KindResult {
		snap := m.totalUsage
		env.Usage = &snap // 輸出一律累計 snapshot
		env.UsageSemantics = semantics
	}
	m.writeAndEmitLocked(env)
	if st, changed := m.reducer.Apply(ev); changed {
		m.emitStateLocked(ev.Provider, ev.SessionID, st)
	}
}

func (m *Manager) emitStateLocked(provider contract.Provider, sessionID string, st contract.SessionState) {
	env := contract.Wrap(contract.Event{Provider: provider, Kind: contract.KindStateChange,
		SessionID: sessionID, Raw: []byte(`{"state":"` + string(st) + `"}`)}, m.taskID)
	env.State = string(st)
	m.writeAndEmitLocked(env)
}

func (m *Manager) writeAndEmitLocked(env contract.Envelope) {
	// closed 已在所有公開入口最先攔截（Emit／approval／Accept／Reject），
	// 此處不再有 closed 分支——emitLocked 不可能於 closed 後執行。
	sinkErr := m.cfg.Sink.Write(env)
	m.cfg.Emit(env) // P1-4：原 envelope 先出（ID 較小），合成事件後出——輸出序嚴格遞增
	if sinkErr != nil {
		if m.auditErr == nil {
			m.auditErr = sinkErr
		}
		m.cfg.Emit(contract.Envelope{ // 只走 UI，不回寫 sink（防遞迴）
			EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Provider: env.Provider, Kind: string(contract.KindStreamError),
			Error: "audit sink: " + sinkErr.Error(),
		})
	}
}

func (m *Manager) Totals() (float64, contract.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalCost, m.totalUsage
}

func (m *Manager) State() contract.SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reducer.Current()
}

func (m *Manager) AuditErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auditErr
}

// Close：同一 mutex、closed 旗標。pending submission 存在時採**顯式 abort+flush**
// （第六輪 P1-4 契約選定）：sink 關閉前把 queue 內事件全部落 audit（無 user envelope），
// 並發 fail-loud 通知——close 前已入列的事件不會無聲遺失。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if m.submitting != nil || len(m.pendingBuf) > 0 {
		n := len(m.pendingBuf)
		m.submitting, m.fromNewSession = nil, false
		if m.phase == phaseStarting {
			m.phase = phaseIdle
		}
		m.flushLocked() // sink 尚未關：queue 事件全數落 audit + UI
		m.cfg.Emit(contract.Envelope{
			EventID: contract.NewULID(time.Now()), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Kind:    string(contract.KindStreamError),
			Error:   fmt.Sprintf("manager closing during pending submission: %d queued events flushed without user acceptance", n),
		})
	}
	m.closed = true
	return m.cfg.Sink.Close()
}
```

（M0 的 `parseTurnStarted`／`noteTurnStarted`／`codexInterruptParams` 自 app.go 遷入 appcore，原測試一併遷移。）

- [ ] **Step 5: app.go 重寫接線規則**（保留 M0 全部行為：workspace/tools/node 解析、broker+MCP、登入流、B1 probe、approval dismiss；變更點如下）：

1. **事件出口**：`Manager` 以 `Sink=NewJSONLSink(stateDir/events.jsonl)`、`Emit=EventsEmit("workbench:event", env)` 建構；所有 provider 事件（pumpClaude 迴圈、codex OnNotification/OnUnknown）一律改走 `Manager.Emit`。
2. **綁定簽名**：`StartSession(provider, prompt, resume, recordCase, taskLabel, approvalPolicy string) error`、`SendMessage(prompt string) error`、`EndSession() error`、`TerminateSession`、`ResolveApproval`、`AuthStatus`／`StartLogin`／`CancelLogin`／`Logout`、`CLIInfo`、`ReadDiagram`、`ListWorkspace`、`ReadWorkspaceFile`。
3. **Submit 流程（單一 ownership 交易，第五輪 P1-2）**：`StartSession`＝`id, err := Manager.BeginNewSessionSubmit(taskLabel)`——active 檢查、換代與 reservation 在同一 mutex 交易內完成，`ErrSessionActive`／`ErrSubmitActive` 原樣回 UI，**輸家在建立任何 process／recorder／pump 之前就失敗** → provider 同步啟動（claude `Start`＋首則 prompt；codex `EnsureThread`＋`StartTurn` bounded synchronous，ctx 30s）→ 成功 `AcceptSubmit(id, …)`（同時標 sessionActive）／失敗 `RejectSubmit(id)`＋回錯誤。`SendMessage`＝`BeginSubmit()`（僅 phaseActive 允許——idle/starting/ending 各回 `ErrNoSession`/`ErrStartInProgress`/`ErrEndInProgress` 原樣至 UI；不換代；claude `turns.Send`；codex `StartTurn`）。`StartTurn` 回 `alreadyEnded=true` → Accept 後直接執行 turn 收尾。
4. **codex 錄流**：`StartSession(codex, recordCase≠"")` → `recorder.New`＋`conn.BeginRecording(rec.Line)`＋建 `RecordingLease(rec, conn.StopRecording, metaFn)`（metaFn＝live snapshot：ProcessStillRunning、StderrSnapshot、Argv）；**收尾一律 `lease.Finalize(ex)`**——`EndSession`（busy 檢查統一由 `EndSessionFlow` 的 busyCheck 執行：busy → Cancel → `ErrProviderBusy`，見規則 6）、開新 session 前、shutdown、conn `Done()`（fatal）四個來源都呼叫，冪等由 lease 保證；codex 各來源一律傳 `ports.Exit{Exited:false}`（meta＝ProcessStillRunning＋StderrSnapshot、ExitCode nil），claude 傳 `CloseSequence` 回傳的 ex。錯誤進 session 結束 envelope 的 recorderError。
5. **核可**：broker pending → `EmitApprovalRequest`＋`approval:request` UI 事件；Resolve／timeout → `EmitApprovalDecision("allow"/"deny"/"timeout")`；`approval:dismiss` 保留。
6. **收尾與汰換（第八輪 P1-1 收斂為單一流程）**：claude pump 一律以 `appcore.Pump(turns.Events(), manager.Emit)` 建立並保留 done channel。`EndSession` 綁定＝呼叫 **`appcore.EndSessionFlow(manager, busyCheck, teardown)`**（可測編排，Task 6 實作與測試）：

   ```
   EndSessionFlow:
     tok, err := m.BeginEndSession()
     ErrNoSession → return nil（冪等）；其他 err → return err
     busyCheck() == true（codex：runner.ActiveTurnID() != ""）
         → m.CancelEndSession(tok)；return ErrProviderBusy（phase 恢復 active，可重試）
     teardownErr := teardown()   // 一旦開始，無論成敗都要 Finish
     finishErr  := m.FinishEndSession(tok)
     return errors.Join(teardownErr, finishErr)
   ```

   teardown 內容：claude＝`CloseSequence(turns.Close, done, 5s, 10s, turns.Terminate, turns.Wait, lease.Finalize)`（`Wait()` 在 `Finalize(ex)` 前、quiesce timeout 保留、卡死於 killTimeout 內回錯並以 `Exit{Exited:false}` 盡力 finalize）；codex＝`lease.Finalize(ports.Exit{Exited:false})`。claude 無 busy 前置檢查（busyCheck=nil）。session 結束 envelope 以 `Exit` 為證據。UI「New」＝`await EndSession()` 成功後才 `reset()`，`ErrProviderBusy` 等真實錯誤原樣顯示、不 reset。codex turn/completed＝`NoteTurnEnded` true（或 alreadyEnded）→ 解 busy＋turn 級 envelope，不動 recorder。app shutdown＝對 active session 走同一 `EndSessionFlow` → `Manager.Close()`（pending queue 由 abort+flush 兜底）。

- [ ] **Step 6: 確認通過** — `go vet ./... && go test -race ./... -count=1` → 全 PASS。
- [ ] **Step 7: Commit** — `git add internal/appcore app.go app_test.go && git commit -m "feat(appcore): submission coordinator, recording lease, serialized manager"`

---

### Task 7：前端基盤（Pinia store、vitest）

**Files:**
- Create: `frontend/src/types.ts`、`frontend/src/stores/session.ts`、`frontend/src/stores/session.test.ts`、`frontend/vitest.config.ts`
- Modify: `frontend/package.json`（deps：pinia；devDeps：vitest、@vue/test-utils、jsdom；scripts.test="vitest run"）

**Interfaces:**

```ts
// types.ts
export interface Usage { input_tokens: number; output_tokens: number; cached_input_tokens?: number }
export interface Envelope {
  event_id: string; ts: string; provider: string; session_id?: string; role?: string
  task_id?: string; kind: string; text?: string; thinking?: string; is_error?: boolean
  cost_usd?: number; usage?: Usage; usage_semantics?: string; state?: string
  error?: string; raw?: unknown
}
export interface ChatItem { role: 'user' | 'assistant'; text: string; thinking: string; streaming: boolean }
export interface TimelineItem { env: Envelope; group?: number }
export interface Bindings {
  StartSession(provider: string, prompt: string, resume: string, recordCase: string,
    taskLabel: string, approvalPolicy: string): Promise<void>
  SendMessage(prompt: string): Promise<void>
}
```

`useSession()` 規約：
- state：`provider`、`taskLabel`、`approvalPolicy`（預設 `untrusted`）、`recordCase`、`resume`（per-provider 記憶）、`sessionId`、`taskId`、`state`、`chat: ChatItem[]`、`timeline: TimelineItem[]`、`totals {cost,input,output}`、`busy`、`active`（session 已啟動）。
- `apply(env)` 唯一入口：`role==='user' && kind==='message'` → user 氣泡；`kind==='delta'` → 累積 assistant streaming 項；`kind==='message' && role==='assistant'` → 落定；`role==='tool'` → 只進 timeline；`usage` 欄位出現（任何 kind）→ totals **覆寫**；`result` → `totals.cost += cost_usd`、`busy=false`；`state_change` → state；`init` → sessionId/taskId；delta 以外全部進 timeline，連續 `system_other`/`unknown` 併 group。
- `submit(text)`：busy → no-op；`busy=true`；`active` 為 false → `bindings.StartSession(...)` 成功後 `active=true`，否則 `bindings.SendMessage(text)`；reject → `pushError` + `busy=false`。**不本地新增 user 氣泡**（等 host envelope）。
- `costDisplay` getter：`totals.cost > 0 ? '$'+toFixed(4) : '—'`。
- `usageSemantics` state：由帶 `usage_semantics` 的 envelope 覆寫（`session_total`｜`provider_latest`）；StatusBar 於 `provider_latest` 時 tokens 旁標 `*`＋tooltip「provider 最新回報值」（第四輪 P2-1）。
- `pushError(msg)`：timeline 入 error 項、`busy=false`。`setBindings(b)`（測試注入 mock）。`reset()`。

- [ ] **Step 1: 安裝依賴** — `npm --prefix frontend i pinia && npm --prefix frontend i -D vitest @vue/test-utils jsdom`；`vitest.config.ts`（jsdom 環境）。
- [ ] **Step 2: 寫失敗測試**

```ts
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSession } from './session'
import type { Envelope } from '../types'

const env = (over: Partial<Envelope>): Envelope => ({
  event_id: String(Math.random()), ts: 't', provider: 'claude', kind: 'delta', ...over,
})

describe('session store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('adds user bubble only from host envelope', () => {
    const s = useSession()
    expect(s.chat.length).toBe(0)
    s.apply(env({ kind: 'message', role: 'user', text: 'hi' }))
    expect(s.chat[0]).toMatchObject({ role: 'user', text: 'hi' })
  })

  it('routes tool-role echo to timeline, not chat', () => {
    const s = useSession()
    s.apply(env({ kind: 'message', role: 'tool', text: 'tool result echo' }))
    expect(s.chat.length).toBe(0)
    expect(s.timeline.at(-1)!.env.text).toBe('tool result echo')
  })

  it('accumulates deltas then finalizes on assistant message', () => {
    const s = useSession()
    s.apply(env({ kind: 'delta', text: 'a' }))
    s.apply(env({ kind: 'delta', text: 'b', thinking: 'th' }))
    expect(s.chat.at(-1)).toMatchObject({ role: 'assistant', text: 'ab', streaming: true })
    s.apply(env({ kind: 'message', role: 'assistant', text: 'ab!' }))
    expect(s.chat.at(-1)).toMatchObject({ text: 'ab!', streaming: false })
  })

  it('overwrites usage snapshot instead of adding', () => {
    const s = useSession()
    s.apply(env({ kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 } }))
    s.apply(env({ kind: 'usage', usage: { input_tokens: 15, output_tokens: 3 } }))
    expect(s.totals.input).toBe(15)
    s.apply(env({ kind: 'result', cost_usd: 0.25, usage: { input_tokens: 20, output_tokens: 5 } }))
    expect(s.totals.input).toBe(20)
    expect(s.totals.cost).toBeCloseTo(0.25)
  })

  it('shows — when provider reports no cost', () => {
    const s = useSession()
    expect(s.costDisplay).toBe('—')
    s.apply(env({ kind: 'result', cost_usd: 0.5 }))
    expect(s.costDisplay).toBe('$0.5000')
  })

  it('tracks usage semantics for provider_latest marker', () => { // 第四輪 P2-1
    const s = useSession()
    s.apply(env({ kind: 'usage', usage: { input_tokens: 10, output_tokens: 1 }, usage_semantics: 'provider_latest' }))
    expect(s.usageSemantics).toBe('provider_latest')
    s.apply(env({ kind: 'result', usage: { input_tokens: 5, output_tokens: 2 }, usage_semantics: 'session_total' }))
    expect(s.usageSemantics).toBe('session_total')
  })

  it('tracks identity and state; result unlocks busy', () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) })
    void s.submit('hello')
    expect(s.busy).toBe(true)
    s.apply(env({ kind: 'init', session_id: 's1', task_id: 'task-1' }))
    s.apply(env({ kind: 'state_change', state: 'waiting' }))
    s.apply(env({ kind: 'result' }))
    expect(s.busy).toBe(false)
    expect(s.sessionId).toBe('s1')
    expect(s.taskId).toBe('task-1')
    expect(s.state).toBe('waiting') // state 只由 state_change 驅動（result 的 done 事件另行到達）
  })

  it('submit routes first to StartSession then to SendMessage', async () => {
    const s = useSession()
    const start = vi.fn(async () => {})
    const send = vi.fn(async () => {})
    s.setBindings({ StartSession: start, SendMessage: send })
    await s.submit('one')
    expect(start).toHaveBeenCalledOnce()
    s.apply(env({ kind: 'result' })) // busy 解鎖
    await s.submit('two')
    expect(send).toHaveBeenCalledWith('two')
  })

  it('submit failure pushes error and unlocks without user bubble', async () => {
    const s = useSession()
    s.setBindings({ StartSession: vi.fn(async () => { throw new Error('busy') }), SendMessage: vi.fn(async () => {}) })
    await s.submit('x')
    expect(s.busy).toBe(false)
    expect(s.chat.length).toBe(0)
    expect(s.timeline.at(-1)!.env.error).toContain('busy')
  })

  it('groups consecutive system noise in timeline', () => {
    const s = useSession()
    s.apply(env({ kind: 'system_other' }))
    s.apply(env({ kind: 'system_other' }))
    s.apply(env({ kind: 'unknown' }))
    s.apply(env({ kind: 'tool_use', text: 'Bash' }))
    const groups = new Set(s.timeline.map(i => i.group).filter(g => g !== undefined))
    expect(groups.size).toBe(1)
    expect(s.timeline.at(-1)!.group).toBeUndefined()
  })
})
```

- [ ] **Step 3: 確認失敗** → FAIL。
- [ ] **Step 4: 實作 `stores/session.ts`**（約 150 行；`apply` 純函式風格、wails 依賴全部經 `setBindings` 注入；wails listener 於 App.vue onMounted 註冊 `EventsOn('workbench:event', s.apply)` 與真實綁定 `setBindings`）。
- [ ] **Step 5: 確認通過** — `npm --prefix frontend run test` → PASS。
- [ ] **Step 6: Commit** — `git add frontend/src frontend/vitest.config.ts frontend/package.json frontend/package-lock.json && git commit -m "feat(ui): pinia session store with role routing and snapshot usage"`

---

### Task 8：ChatPanel

**Files:** Create: `frontend/src/components/ChatPanel.vue`

- [ ] **Step 1: 實作元件**

```vue
<script setup lang="ts">
import { ref, nextTick, watch } from 'vue'
import { useSession } from '../stores/session'
import { isAtBottom } from '../lib/scroll'

const s = useSession()
const draft = ref('')
const listEl = ref<HTMLElement | null>(null)
const follow = ref(true) // BAT 慣例（normative）：使用者上捲後停止自動跟隨

function onScroll() {
  const el = listEl.value
  if (el) follow.value = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight)
}

async function send() {
  const text = draft.value.trim()
  if (!text || s.busy) return
  draft.value = ''
  follow.value = true // 送出視為回到追蹤
  await s.submit(text)
}

watch(() => s.chat.length + (s.chat.at(-1)?.text.length ?? 0), () =>
  nextTick(() => {
    if (follow.value) listEl.value?.scrollTo({ top: listEl.value.scrollHeight })
  }))
</script>

<template>
  <div class="chat">
    <div ref="listEl" class="msgs" @scroll.passive="onScroll">
      <div v-for="(m, i) in s.chat" :key="i" :class="['bubble', m.role]">
        <details v-if="m.thinking" class="thinking"><summary>thinking</summary>
          <pre>{{ m.thinking }}</pre>
        </details>
        <div class="text">{{ m.text }}<span v-if="m.streaming" class="cursor">▌</span></div>
      </div>
    </div>
    <div class="composer">
      <textarea v-model="draft" rows="2" :disabled="s.busy"
        placeholder="輸入訊息，Enter 送出（Shift+Enter 換行）"
        @keydown.enter.exact.prevent="send" />
      <button :disabled="s.busy || !draft.trim()" @click="send">送出</button>
    </div>
  </div>
</template>

<style scoped>
.chat { display: flex; flex-direction: column; height: 100%; }
.msgs { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.bubble { max-width: 76%; padding: 8px 12px; border-radius: 10px; text-align: left; white-space: pre-wrap; }
.bubble.user { align-self: flex-end; background: #2d5a88; }
.bubble.assistant { align-self: flex-start; background: #263444; }
.thinking pre { font-size: 12px; color: #8aa0b4; white-space: pre-wrap; }
.cursor { animation: blink 1s step-start infinite; }
@keyframes blink { 50% { opacity: 0; } }
.composer { display: flex; gap: 8px; padding: 8px; border-top: 1px solid #3a4a5a; }
.composer textarea { flex: 1; resize: none; padding: 8px; }
</style>
```

- [ ] **Step 2: `frontend/src/lib/scroll.ts` 與測試（follow-tail 為 normative，第八輪 P1-2）**

```ts
// lib/scroll.ts
export function isAtBottom(scrollTop: number, scrollHeight: number,
  clientHeight: number, slack = 24): boolean {
  return scrollTop + clientHeight >= scrollHeight - slack
}
```

```ts
// lib/scroll.test.ts
import { describe, expect, it } from 'vitest'
import { isAtBottom } from './scroll'

describe('isAtBottom', () => {
  it('true at exact bottom', () => expect(isAtBottom(760, 1000, 240)).toBe(true))
  it('true within slack', () => expect(isAtBottom(740, 1000, 240)).toBe(true))
  it('false when scrolled up', () => expect(isAtBottom(300, 1000, 240)).toBe(false))
})
```

- [ ] **Step 3: 驗證** — `npm --prefix frontend run test && npm --prefix frontend run build` → PASS；`wails dev` 手動：長回覆中上捲 → 不再自動跳底、拉回底部或送出新訊息 → 恢復跟隨。
- [ ] **Step 4: Commit** — `git add frontend/src && git commit -m "feat(ui): multi-turn chat panel with follow-tail scrolling"`

---

### Task 9：Timeline 與 StatusBar

**Files:** Create: `frontend/src/components/Timeline.vue`、`frontend/src/components/StatusBar.vue`

- [ ] **Step 1: StatusBar**

```vue
<script setup lang="ts">
import { useSession } from '../stores/session'
const s = useSession()
const stateLabel: Record<string, string> = {
  idle: '待命', waiting: '等待回覆', streaming: '回覆中', tool_running: '工具執行中',
  awaiting_approval: '等待核可', retrying: '重試中', done: '完成', failed: '失敗',
}
</script>

<template>
  <div class="status">
    <span class="task">任務：{{ s.taskId || '—' }}</span>
    <span :class="['state', s.state]">{{ stateLabel[s.state] ?? s.state }}</span>
    <span class="sid">session：{{ s.sessionId || '—' }}</span>
    <span class="usage" :title="s.usageSemantics === 'provider_latest' ? 'provider 最新回報值' : '本 session 累計'">
      tokens {{ s.totals.input }}/{{ s.totals.output }}{{ s.usageSemantics === 'provider_latest' ? '*' : '' }}
    </span>
    <span class="cost">{{ s.costDisplay }}</span>
  </div>
</template>

<style scoped>
.status { display: flex; gap: 16px; padding: 4px 12px; font-size: 12px; border-top: 1px solid #3a4a5a; color: #9db2c5; }
.state.awaiting_approval { color: #ffd54f; }
.state.failed { color: #ff8a80; }
.state.waiting, .state.streaming, .state.tool_running { color: #80cbc4; }
</style>
```

- [ ] **Step 2: Timeline**

```vue
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useSession } from '../stores/session'
import type { TimelineItem } from '../types'

const s = useSession()
const openGroups = ref(new Set<number>())
const openRaw = ref(new Set<string>())

const rows = computed(() => {
  const out: Array<{ head: TimelineItem; count: number; group?: number }> = []
  for (const item of s.timeline) {
    const last = out.at(-1)
    if (item.group !== undefined && last && last.group === item.group) { last.count++; continue }
    out.push({ head: item, count: 1, group: item.group })
  }
  return out
})
function groupItems(g: number) { return s.timeline.filter(i => i.group === g) }
function summary(i: TimelineItem) {
  const e = i.env
  if (e.kind === 'tool_use') { // BAT AgentToolRow（normative）：工具名＋參數節錄＋狀態
    const status = (e.raw as any)?.params?.item?.status // codex item 狀態，best-effort
    const label = e.text || '工具呼叫' // adapter 已填「名稱(參數節錄)」（Task 2）
    return status ? `${label}（${status}）` : label
  }
  if (e.kind === 'result') return `${e.is_error ? 'ERROR' : 'ok'}`
  if (e.kind === 'approval') return `核可請求：${e.text}`
  if (e.kind === 'approval_decision') return `核可決定：${e.text}`
  if (e.kind === 'state_change') return `狀態 → ${e.state}`
  if (e.kind === 'retry') return 'provider 重試'
  if (e.kind === 'message' && e.role === 'tool') return '工具結果'
  if (e.error) return e.error
  return e.kind
}
function toggle(set: Set<number> | Set<string>, key: never) {
  ;(set.has(key) ? set.delete(key) : set.add(key))
}
</script>

<template>
  <div class="timeline">
    <template v-for="(r, idx) in rows" :key="idx">
      <div v-if="r.group !== undefined" class="row noise">
        <button @click="toggle(openGroups, r.group! as never)">
          {{ openGroups.has(r.group!) ? '▾' : '▸' }} 系統事件 ×{{ r.count }}
        </button>
        <div v-if="openGroups.has(r.group!)" class="noise-items">
          <div v-for="g in groupItems(r.group!)" :key="g.env.event_id" class="row sub">
            <span class="kind">{{ g.env.kind }}</span>
            <button class="rawbtn" @click="toggle(openRaw, g.env.event_id as never)">raw</button>
            <pre v-if="openRaw.has(g.env.event_id)">{{ JSON.stringify(g.env.raw, null, 1) }}</pre>
          </div>
        </div>
      </div>
      <div v-else :class="['row', r.head.env.kind]">
        <span class="kind">{{ r.head.env.kind }}</span>
        <span class="sum">{{ summary(r.head) }}</span>
        <button class="rawbtn" @click="toggle(openRaw, r.head.env.event_id as never)">raw</button>
        <pre v-if="openRaw.has(r.head.env.event_id)">{{ JSON.stringify(r.head.env.raw, null, 1) }}</pre>
      </div>
    </template>
  </div>
</template>

<style scoped>
.timeline { height: 100%; overflow-y: auto; padding: 6px 10px; font-size: 12px; text-align: left; }
.row { margin: 2px 0; }
.kind { color: #7aa2c4; margin-right: 8px; }
.row.tool_use .sum { color: #80cbc4; }
.row.approval .sum, .row.approval_decision .sum { color: #ffd54f; }
.row.result .sum { font-weight: 600; }
.row.retry .sum, .row.stream_error .sum { color: #ff8a80; }
.noise button { color: #66788a; background: none; border: none; cursor: pointer; }
.rawbtn { font-size: 10px; margin-left: 6px; }
pre { background: #101820; padding: 6px; border-radius: 4px; white-space: pre-wrap; word-break: break-all; }
.sub { margin-left: 18px; }
</style>
```

- [ ] **Step 3: 驗證** — vitest + frontend build；`wails dev` 手動：codex turn 中 StatusBar 四欄位變化、cost 顯示 `—`、雜訊摺疊。
- [ ] **Step 4: Commit** — `git add frontend/src && git commit -m "feat(ui): timeline with noise folding and SC2 status bar"`

---

### Task 10：FileTree 與 PreviewPane（canonical 邊界＋sanitize）

**Files:**
- Create: `frontend/src/lib/markdown.ts`、`frontend/src/lib/markdown.test.ts`、`frontend/src/components/FileTree.vue`、`frontend/src/components/PreviewPane.vue`
- Modify: `app.go`；Delete: `frontend/src/components/MermaidPane.vue`
- 依賴：`npm --prefix frontend i marked dompurify`

**Interfaces（Go 綁定）:**

```go
type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // workspace 相對路徑
	IsDir bool   `json:"isDir"`
}
func (a *App) ListWorkspace(rel string) ([]FileNode, error)
func (a *App) ReadWorkspaceFile(rel string) (string, error)
```

**安全規則**：
1. workspace root canonical（`claude.NormalizeCWD` 已含 EvalSymlinks）。
2. `abs = filepath.Join(root, filepath.Clean("/"+rel))` → `resolved, err := filepath.EvalSymlinks(abs)` → `resolved == root || strings.HasPrefix(resolved, root+sep)` 不成立即 error（symlink 指外被擋）。
3. `ReadWorkspaceFile`：`os.Stat(resolved)` 非 regular file 或 `>1MB` → error（不讀內容）；讀取用 `io.LimitReader(f, 1<<20+1)` 雙保險。
4. List 排除：`.git`、`.workbench`、`node_modules`、`build` 與 `.` 開頭項。
5. 前端渲染一律經 `renderMarkdown`；mermaid `securityLevel:'strict'`。

- [ ] **Step 1: 寫失敗測試（`app_test.go` 追加）**

```go
func TestWorkspaceReadSecurity(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.md"), []byte("# hi"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
	os.Symlink(outside, filepath.Join(root, "linkdir"))
	big := make([]byte, 1<<20+1)
	os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644)

	a := NewApp()
	a.workspaceDir, _ = claude.NormalizeCWD(root)

	if got, err := a.ReadWorkspaceFile("ok.md"); err != nil || got != "# hi" {
		t.Fatalf("normal read: %v %q", err, got)
	}
	if _, err := a.ReadWorkspaceFile("../etc/passwd"); err == nil {
		t.Fatal("dot-dot escape must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("link.txt"); err == nil {
		t.Fatal("symlink file to outside must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("linkdir/secret.txt"); err == nil {
		t.Fatal("symlink dir traversal must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("big.bin"); err == nil {
		t.Fatal(">1MB must be rejected before full read")
	}
	if _, err := a.ListWorkspace(".."); err == nil {
		t.Fatal("list escape must be rejected")
	}
}
```

- [ ] **Step 2: sanitizer 測試（`lib/markdown.test.ts`）**

```ts
import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown'

describe('renderMarkdown sanitization', () => {
  it('strips script tags', () => {
    expect(renderMarkdown('hello <script>alert(1)</script>')).not.toContain('<script')
  })
  it('strips event handlers', () => {
    expect(renderMarkdown('<img src=x onerror="alert(1)">')).not.toContain('onerror')
  })
  it('strips javascript: URLs', () => {
    expect(renderMarkdown('[x](javascript:alert(1))')).not.toContain('javascript:')
  })
  it('keeps normal markdown', () => {
    const html = renderMarkdown('# Title\n\n**bold**')
    expect(html).toContain('<h1')
    expect(html).toContain('<strong>')
  })
})
```

- [ ] **Step 3: 確認失敗**（Go + vitest）→ FAIL。
- [ ] **Step 4: 實作**

`lib/markdown.ts`：

```ts
import { marked } from 'marked'
import DOMPurify from 'dompurify'

export function renderMarkdown(md: string): string {
  return DOMPurify.sanitize(marked.parse(md, { async: false }) as string)
}
```

Go 綁定（依安全規則 1–4，約 60 行）；`FileTree.vue`（懶載入樹：根層 `ListWorkspace("")`，點目錄載子層、點檔案 `emit('select', path)`，約 60 行）；`PreviewPane.vue`（接 `select`：`.md` → `renderMarkdown` + 抽出 ```mermaid``` 區塊逐一 `mermaid.render`；`.mmd` → 全檔 mermaid；其他 → `<pre>`；`diagram:changed` 事件對開啟中檔案重渲染；約 90 行）。

- [ ] **Step 5: 驗證** — `go test ./... -run TestWorkspaceReadSecurity` + vitest + build；`wails dev` 開 `docs/sample.mmd` 編輯 1 秒內重渲染、開含 mermaid 區塊的 `.md`。
- [ ] **Step 6: Commit** — `git add app.go app_test.go frontend/src frontend/package.json frontend/package-lock.json && git commit -m "feat(ui): file tree and sanitized markdown preview with canonical path boundary"`

---

### Task 11：SettingsBar 與 App.vue 重組（三欄佈局）

**Files:**
- Create: `frontend/src/components/SettingsBar.vue`
- Modify: `frontend/src/App.vue`（重寫）；`ApprovalDialog.vue` 沿 M0 版不改。

- [ ] **Step 1: SettingsBar**

```vue
<script setup lang="ts">
import { useSession } from '../stores/session'
import {
  TerminateSession, EndSession, AuthStatus, StartLogin, CancelLogin, Logout,
  RestartCodexServerRecorded,
} from '../../wailsjs/go/main/App'

const s = useSession()

async function call(fn: () => Promise<unknown>, label: string) {
  try {
    const r = await fn()
    s.note(`${label} ok${typeof r === 'string' && r ? '：' + r.slice(0, 400) : ''}`)
  } catch (e: any) {
    s.pushError(`${label}: ${e}`)
  }
}
</script>

<template>
  <div class="settings">
    <select v-model="s.provider">
      <option value="claude">claude</option>
      <option value="codex">codex</option>
    </select>
    <input v-model="s.taskLabel" class="w-160" placeholder="任務標籤（task id）" />
    <select v-if="s.provider === 'codex'" v-model="s.approvalPolicy" title="codex approvalPolicy">
      <option value="untrusted">untrusted（每次核可）</option>
      <option value="on-request">on-request</option>
      <option value="never" class="danger">never（不核可，風險自負）</option>
    </select>
    <input v-model="s.recordCase" class="w-160" :placeholder="s.provider + '-case（錄流，可空）'" />
    <input v-model="s.resumeInput" class="w-200" placeholder="resume id（可空）" />
    <button title="結束目前 session（quiesce 舊 provider）後開新對話"
      @click="call(async () => { await EndSession(); s.reset() }, 'new')">New</button>
    <!-- EndSession 冪等（無 active session 回 nil）；真實錯誤由 call() 顯示且不 reset -->

    <button @click="call(TerminateSession, 'terminate')">Terminate</button>
    <button @click="call(EndSession, 'end')">End</button>
    <span class="spacer" />
    <button @click="call(() => AuthStatus(s.provider), 'auth')">Auth</button>
    <button @click="call(() => StartLogin(s.provider), 'login')">Login</button>
    <button v-if="s.provider === 'codex'" @click="call(() => CancelLogin(s.provider), 'cancel-login')">Cancel</button>
    <button @click="call(() => Logout(s.provider), 'logout')">Logout</button>
    <button v-if="s.provider === 'codex'" @click="call(() => RestartCodexServerRecorded(s.recordCase || 'codex-handshake'), 'b1-probe')">B1</button>
  </div>
</template>

<style scoped>
.settings { display: flex; gap: 6px; padding: 6px 8px; align-items: center; flex-wrap: wrap; }
.w-160 { width: 160px; }
.w-200 { width: 200px; }
.spacer { flex: 1; }
.danger { color: #ff8a80; }
</style>
```

（store 增 `note(msg)` action：入 timeline 資訊項；`resumeInput` 為 per-provider `resume` 的 computed 包裝——切 provider 顯示各自記憶值。）

- [ ] **Step 2: App.vue（三欄佈局）**

```vue
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { StartSession, SendMessage, CLIInfo } from '../wailsjs/go/main/App'
import { useSession } from './stores/session'
import SettingsBar from './components/SettingsBar.vue'
import ChatPanel from './components/ChatPanel.vue'
import Timeline from './components/Timeline.vue'
import StatusBar from './components/StatusBar.vue'
import FileTree from './components/FileTree.vue'
import PreviewPane from './components/PreviewPane.vue'
import ApprovalDialog from './components/ApprovalDialog.vue'

const s = useSession()
const tab = ref<'chat' | 'preview'>('chat')
const timelineOpen = ref(true) // VS Code panel 慣例（normative：可摺疊；拖高/高度記憶 → M2）
const selectedFile = ref('')
const cliInfo = ref<Record<string, string>>({})

onMounted(async () => {
  s.setBindings({
    StartSession: (p, prompt, resume, rc, task, policy) => StartSession(p, prompt, resume, rc, task, policy),
    SendMessage: (t) => SendMessage(t),
  })
  EventsOn('workbench:event', (e: any) => s.apply(e))
  EventsOn('session:done', (d: any) => s.applyDone(d))
  try { cliInfo.value = await CLIInfo() } catch { /* dev 無綁定時忽略 */ }
})
</script>

<template>
  <div class="shell">
    <SettingsBar />
    <div class="meta" :title="JSON.stringify(cliInfo)">
      ws: {{ cliInfo.workspaceSource }} @ {{ cliInfo.workspace }} | tools: {{ cliInfo.toolsSource }} | node {{ cliInfo.node }}
      <span v-if="cliInfo.startupError" class="err">startup: {{ cliInfo.startupError }}</span>
    </div>
    <div class="body">
      <aside><FileTree @select="(p: string) => { selectedFile = p; tab = 'preview' }" /></aside>
      <main>
        <nav>
          <button :class="{ active: tab === 'chat' }" @click="tab = 'chat'">Chat</button>
          <button :class="{ active: tab === 'preview' }" @click="tab = 'preview'">Preview</button>
        </nav>
        <ChatPanel v-show="tab === 'chat'" />
        <PreviewPane v-show="tab === 'preview'" :path="selectedFile" />
      </main>
    </div>
    <div v-show="timelineOpen" class="tl"><Timeline /></div>
    <button class="tl-toggle" @click="timelineOpen = !timelineOpen">
      {{ timelineOpen ? '▾ Timeline' : '▸ Timeline' }}
    </button>
    <StatusBar />
    <ApprovalDialog />
  </div>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
body { background: #1b2636; color: #e6edf3; font-family: ui-sans-serif, system-ui, sans-serif; }
</style>

<style scoped>
.shell { display: flex; flex-direction: column; height: 100vh; }
.meta { font-size: 11px; color: #66788a; padding: 0 10px 4px; text-align: left; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta .err { color: #ff8a80; margin-left: 8px; }
.body { flex: 1; display: flex; min-height: 0; }
aside { width: 220px; border-right: 1px solid #3a4a5a; overflow-y: auto; }
main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
nav { display: flex; gap: 4px; padding: 4px 8px; }
nav .active { background: #2d5a88; color: #fff; }
main > :not(nav) { flex: 1; min-height: 0; }
.tl { height: 180px; border-top: 1px solid #3a4a5a; }
.tl-toggle { align-self: flex-start; font-size: 11px; background: none; border: none; color: #66788a; cursor: pointer; padding: 2px 10px; }
</style>
```

（store 增 `applyDone(d)`：session:done 資訊入 timeline（exit/stderr/recorderError）；`submit` 內部組 `StartSession(provider, text, resume, recordCase, taskLabel, approvalPolicy)`。）

- [ ] **Step 3: 驗證** — vitest（note/applyDone/resumeInput 三個新 action 各一條測試：入 timeline、per-provider resume 記憶）+ frontend build + `wails dev` 雙 provider 各 2 輪。
- [ ] **Step 4: Commit** — `git add frontend/src && git commit -m "feat(ui): settings bar and three-pane M1 layout"`

---

### Task 12：M1 驗收（V0–V6）、最終 gate、m1-results

**Files:** Create: `docs/spikes/m1-results.md`

**V0 M0 迴歸 gate**（app.go 重寫必須證明 M0 行為未破）：

- [ ] V0.1 claude allow：touch probe → 彈窗 → Allow → 檔案存在、audit request/decision。
- [ ] V0.2 claude deny：Deny + 理由 → 檔案不存在、turn 正常收尾。
- [ ] V0.3 claude 逾時：`WORKBENCH_APPROVAL_TIMEOUT=5s` 不操作 → 自動 deny + 彈窗自動消失。
- [ ] V0.4 claude resume：跨 app 重啟 resume 引用前文；異 cwd resume refused。
- [ ] V0.5 claude Terminate：長任務中止 → 5s 內收尾、exit 143。
- [ ] V0.6 Recorder 失敗可見：chmod 500 recordings → session 帶 recorderError；恢復後正常。
- [ ] V0.7 codex approval allow/deny（untrusted）＋ resume（thread/resume 引用前文）。
- [ ] V0.8 codex auth：`AuthStatus` 讀取正常。**Logout/login 循環僅在 owner 事前明示同意時執行，且執行後當場恢復登入並驗證**；未同意記「未執行（沿 M0 waiver）」。
- [ ] V0.9 replay 自動迴歸：`go test ./internal/claude/ -run TestContractReplay -count=1 && go test ./internal/codex/ -run TestReplay -count=1`。

**V1–V6：**

- [ ] **V1 多輪**：claude 3 輪（依 Task 4 判定路徑）＋ codex 3 輪（同 thread、server 不重啟）；第 2、3 輪引用前文；busy 期間輸入停用；**codex 三輪錄流為單一 `codex-m1-chat.jsonl`（三次 turn/start＋三次 turn/completed、meta 恰一份）**——session-scoped lease 的 live 證據。
- [ ] **V2 SC2 四問**：StatusBar 同時可見任務標籤／狀態（含「等待核可」時刻）／session id／tokens；cost 僅要求 claude 非零、codex 顯示 `—`。**usage 標示依 Task 4 VERDICT 驗收**：per-turn → claude tokens 無 `*`（session_total）；cumulative／inconclusive → `*` 與 tooltip「provider 最新回報值」必須顯示（不得把最新回報值稱為累計值）；codex 恆為 `*`。截圖存證。
- [ ] **V3 BAT 問答等價＋normative UI 行為（第八輪 P1-2）**：streaming 逐 token、thinking 摺疊、result cost 對照 M0 無倒退；**上捲停止跟隨**（長回覆中上捲不跳底、回底/送出恢復）；**tool 卡片含工具名＋參數節錄**（claude `Bash(touch …)`；codex per-type：`echo hi`／`server.tool(args)` 等實測樣張）；**codex 卡片另驗狀態顯示**（`（completed）` 等；claude 無狀態欄位、不要求）；**Timeline 摺疊 toggle** 收合後 Chat 區域擴大。
- [ ] **V4 檔案樹＋預覽**：瀏覽 repo、開 `.md`（含 mermaid 區塊）與 `.mmd`、編輯 `.mmd` 存檔 1 秒內重渲染；**symlink 拒絕一例實測**（建 symlink 指 /etc → UI 顯示錯誤）。
- [ ] **V5 approvalPolicy**：untrusted → touch 彈核可；never → 同 prompt 不彈（行為與風險如實記錄）。
- [ ] **V6 稽核 JSONL**：`events.jsonl` 涵蓋 V0–V5 全部 envelope（含 role=user 訊息、approval request/decision、state_change）；`jq` 驗 event_id 單調、抽 3 筆對照 UI；**user message envelope 先於該輪首個 provider event**（coordinator 的 live 證據）；AuditErr 為 nil（UI 無 stream_error）。
- [ ] **最終 gate**：`go vet ./...`、`go test -race ./... -count=1`、`npm --prefix frontend run test`、`npm --prefix frontend run build`、`wails build`、`scripts/bundle-clis.sh`＋封裝 smoke（雙 provider 各 1 輪）。
- [ ] **m1-results.md**：版本基線（CLI pin 不變聲明）、Task 4 多輪判定＋usage VERDICT、V0–V6 證據表（含錄流 digest）、偏差記錄、殘餘風險（含測試穩定性追蹤）、SC2 達成聲明。
- [ ] **Commit** — `git add docs/spikes/m1-results.md testdata/fixtures && git commit -m "docs(m1): acceptance results"`；通知使用者審閱。

---

## 驗證策略總表

| 層 | 手段 |
|---|---|
| 單元（Go） | `-race -count=1`：contract（ULID、Wrap role precedence、raw fallback、reducer 全轉移：waiting／failed-result／malformed-neutral／tool-echo／resolve×3／reset／retry／neutral）、claude（多輪×3、Role、usage；4b 觸發時 ResumeTurns EOF gating＋resume argv）、codex（wire lifecycle、**early-completed latch**、**空 turn id**、**barrier 單 wire request**、resume、**session-scoped 錄流雙輪**、Role、usage）、appcore（**RecordingLease barrier 恰一次／首錯保留／stop-err join**、**coordinator：ownership barrier／stale-accept／approval 入列順序／reject**、**Pump quiesce／timeout**、**Close×Emit barrier＋fail-loud ID 遞增**、usage 語意雙分支＋semantics、reset、approval 流、audit fail-loud、併發順序、user message）、main（workspace 安全 6 例） |
| 單元（TS） | vitest：user-envelope 氣泡、tool-echo 路由、delta 累積、usage 覆寫、costDisplay、busy、submit 分流／失敗、雜訊分組、sanitizer 4 例、note／applyDone／resumeInput |
| Live probe | Task 4（嚴格序、結構化清理、自然收尾、usage VERDICT） |
| 協定 | M0 replay 全保留 + `claude-multiturn-shape` |
| 整合 | V0（9 項）+ V1–V6（V1 codex 單檔三輪錄流、V6 user-first 順序）+ 封裝 smoke |

## 風險與已知邊界

1. 多輪 stream-json 未 live 驗證：Task 4 前置、雙路徑可執行。
2. claude 多輪 usage 形狀未驗：Task 4 VERDICT 判定（inconclusive → 覆寫 fallback）。
3. 多輪 broker 生命週期：V0.1–V0.3 迴歸覆蓋。
4. turn/steer、bundle 瘦身不在 M1；`never` policy 警示；codex resumed usage 如實顯示；事件量 M3 視窗化。
5. **coordinator 覆蓋邊界**：ownership token 使錯誤接線（漏 Accept、重複 submit、stale Accept）都變成顯式 error 而非靜默亂序；`NewSession` flush＋generation 失效兜底；殘餘的「app.go 忘記呼叫」情境由 V1／V6 live 驗收（user-first 順序、approval 順序）把關。
6. 粗估工時：Go 層 2.5 週、前端 1.5 週、驗收 3 天；4b 觸發 +2 天。未依 throughput 校準。

## 修訂記錄

### v13（2026-08-10）— 依第十二輪 plan gate（CHANGES_REQUIRED，1 P1）修訂

1. **未使用 import 修正（P1）**：mapevent.go 的 import 指示由「增 `fmt`、`strings`」改為只增 `fmt`——MCP 摘要為直接串接、實作無任何 `strings.*` 呼叫，原指示會造成 `"strings" imported and not used` 編譯失敗。

### v12（2026-08-10）— 依第十一輪 plan gate（CHANGES_REQUIRED，1 P1 + 1 P2）修訂

1. **codexItem 型別實際內嵌（P1-1）**：補完整 `type codexItem struct` 定義（Type/Text/Summary + Command/Server/Tool/Arguments/Query/Changes，欄位註解版刪除）；`MapEvent` 的 params 解析明確改用 `struct{ Item codexItem }`——`codexToolSummary(it codexItem)` 的引用自此有定義，符合完整內嵌聲明。
2. **MCP fallback 條件收斂（P2-1）**：`mcpToolCall` 摘要改為必要欄位全備（`Server != "" && Tool != "" && len(Arguments) > 0`）才產生，任一缺失 → 型別名 fallback（消除 `server.()`／`tool()`／`server.tool()` 半成品）；`TestMapEventToolText` 增 table test 四案例（全缺／缺 server／缺 tool／缺 arguments 各自 fallback）。

### v11（2026-08-10）— 依第十輪 plan gate（CHANGES_REQUIRED，2 P1 + 1 P2）修訂

1. **coordinator 序列斷言恢復（P1-1）**：`startActive` 補完成 boot turn（Emit result → reducer 進 done——多輪的真實前置：下一輪於前輪 result 解鎖後送出）；三個整合測試恢復完整凍結序列斷言——coordinator ordering `user → state_change(waiting) → delta…`（含 `(*got)[base+1].State == waiting`）、approval request `user → waiting → approval → awaiting_approval`、approval decision 六步全序；`TestEmitUserMessage` 對應改取末二 envelope。
2. **Codex per-type 工具摘要（P1-2，維持雙 provider 強制）**：`codexToolSummary`——commandExecution＝command 節錄、mcpToolCall＝`server.tool(arguments 節錄)`、webSearch＝`webSearch(query)`、fileChange＝`fileChange(首路徑 +N)`（欄位名經 pinned schema 覆核：`server`/`tool`/`arguments`/`query`/`changes[].path`）；wire 欄位缺失時型別名 fallback（如實顯示、不記 FAIL）。`TestMapEventToolText` 擴為五案例（command／mcp 真名／webSearch／fileChange +N／fallback）；normative 表與 V3 同步。
3. **80-rune 定義凍結（P2-1）**：節錄規則統一為「內容 ≤80 rune；超過取前 80 rune 加『…』，節錄總長上限 81 rune（不含工具名與括號）」——契約、claude `toolSummary`、codex `excerpt80` 與測試上限（81）三者一致。

### v10（2026-08-10）— 依第九輪 plan gate（CHANGES_REQUIRED，2 P1 + 1 P2）修訂

1. **Submit×End 共用 lifecycle ownership（P1-1）**：`BeginSubmit` 僅允許 phaseActive（idle/starting/ending 各回 `ErrNoSession`/`ErrStartInProgress`/`ErrEndInProgress`）；`BeginEndSession` 同鎖拒絕 `submitting != nil`（`ErrSubmitActive`——teardown 不得與 pending submit 重疊）；`EndSessionFlow` busy 分支的 Cancel 錯誤以 `errors.Join` 保留；`newSessionLocked` 一併重設 phase/endTok。新測試（channel barrier、-race）：`TestSendThenEndBarrier`（provider 已送出、未 Accept → End 必 `ErrSubmitActive`、Accept 無晚到寫入、其後可正常 End）、`TestEndThenSendRejected`（ending 期間 Send 必 `ErrEndInProgress`、Finish 後 `ErrNoSession`）。既有 submit 類測試改以 `startActive` helper 前置並以 baseline 切片斷言（10 個測試同步更新，順序斷言依 boot 後 waiting 狀態修正）。
2. **Task 2 adapter 實作補齊（P1-2）**：decode.go assistant/user case 完整內嵌（content 解析、tool-only 判定、`toolSummary` helper：80-rune 截斷＋`…`、多 block ` +N`）；mapevent.go tool case 完整內嵌（item 增 `Command` 欄位、command 優先、型別名 fallback）。新測試：`TestDecodeToolSummaryTruncationAndMulti`（截斷、+1、節錄長度上限）、`TestMapEventToolText`（command 與 mcpToolCall fallback）；既有 mapevent 表列 text/role 期望值逐列明定。
3. **Tool 狀態契約定為 best-effort（P2-1）**：normative 表述修正——工具名＋參數節錄雙 provider 強制；狀態為 best-effort（codex `item.status` wire 提供才顯示、claude 無狀態欄位如實省略）；V3 補「codex 卡片驗狀態顯示、claude 不要求」。

### v9（2026-08-10）— 依第八輪 plan gate（CHANGES_REQUIRED，2 P1 + 1 P2）修訂

1. **EndSession 單一流程（P1-1）**：新增 `appcore.EndSessionFlow(m, busyCheck, teardown)`——冪等（ErrNoSession→nil）、busy → `CancelEndSession` + `ErrProviderBusy`（phase 復原 active）、teardown 一旦開始無論成敗都 `FinishEndSession`、回 `errors.Join(teardownErr, finishErr)`；接線規則 4／6 收斂引用同一流程（矛盾消除）。新測試：`TestEndSessionFlowBusyThenRetry`（busy 不殘留 ending、可重試成功）、`TestEndSessionFlowTeardownErrorStillFinishes`（teardown 錯誤保留且可再開新 session）、`TestEndSessionFlowIdempotentNoSession`。
2. **UI 參考規則二分（P1-2）**：三項升為 **normative** 並同步實作與驗收——(a) Chat 上捲停止跟隨：ChatPanel `follow` 狀態＋`@scroll` 監聽＋`lib/scroll.ts` `isAtBottom` 純函式（vitest 3 例）；(b) tool 卡片＝工具名＋參數節錄＋狀態：Task 2 adapter 填 `Text`（claude tool-only assistant → KindToolUse＋摘要、codex `item.command`，各附測試）＋Timeline `summary()` 讀 status；(c) Timeline 摺疊 toggle：App.vue `timelineOpen`。V3 驗收補三項實測。其餘參考明標 **non-normative**（不驗收、缺項不 FAIL）；拖高／高度記憶／快捷鍵明列 M2 backlog。
3. **BAT 參考 pin 與映射修正（P2）**：pin commit `72dc4ba`（漂移時重新確認並記入 m1-results）；StatusBar 參考修正為 `ClaudeAgentPanel.tsx` statusline（設定於 `renderer/src/types/index.ts`）；permission UI 參考修正為 `ClaudeAgentPanel.tsx`（`AskUserQuestion.*` 如實標為問答卡）；`CollapsedBar.tsx` 限定為外觀與點擊手勢參考。

### v8.1（2026-08-10）— 使用者補充：UI 設計參考 BAT 與 VS Code

非 gate 輪次。新增「UI 設計參考」節：Task 8–11 各元件對應 BAT（`~/playground/external/
better-agent-terminal`，MIT）的具體參考檔案與借用的設計決策（streaming 渲染、tool 卡片、
雜訊摺疊、statusline 密度、核可呈現、檔案樹互動）；佈局與操作慣例參考 VS Code
（sidebar/editor/panel/status bar、檔案樹單擊預覽、panel 摺疊）。參考互動與資訊架構、
不逐行移植 code；衝突時以本計畫契約為準並記入 m1-results。任務內容與驗收不變。

### v8（2026-08-10）— 依第七輪 plan gate（CHANGES_REQUIRED，4 P1 + 1 P2）修訂

1. **manager.go 宣告補齊（P1-1）**：四個 lifecycle sentinel（ErrNoSession／ErrStartInProgress／ErrEndInProgress／ErrStaleSession）與 `SessionToken` 實際納入 manager.go 完整實作區（介面區與實作區一致）。
2. **Deterministic starting barrier＋ending 復原（P1-2）**：`TestStartEndBarrier` 改寫為 `TestEndDuringStartingIsRejected`——providerStarted／release 兩段 channel 固定命中 starting phase（不靠排程），End 必得 `ErrStartInProgress`；新增 `CancelEndSession(token)`（ending→active 復原，app 接線：codex busy 於 `BeginEndSession` 後檢查、busy 即 Cancel＋回 error）；teardown 回錯仍 `FinishEndSession`、`errors.Join` 保留兩者；`SessionToken` 增 end 序號——Cancel／Finish 只認目前 outstanding 的一枚，Cancel 後舊 token 對後續 End 一律 stale（`TestCancelEndSessionRestoresActive`）。
3. **未知 Exit 不偽裝 exit 0（P1-3）**：`ports.Exit` 增 `Exited bool`；`claude.Session.Wait` 映射 `Exited:true`；CloseSequence 卡死路徑 finalize `Exit{Exited:false}`；metaFn 規約：`Exited=false` → `Meta.ExitCode` 維持 nil。測試：CloseSequence 三路徑補 Exited 斷言、新增 `TestLeaseUnknownExitLeavesMetaNil`。
4. **internal/ports 納入 commit（P1-4）**：Task 3 Files 增 `internal/ports/turns.go`、commit 增 `git add internal/ports`；最終 gate 增 clean working tree 檢查（`git status --short` 為空）。
5. **Finalize 簽名殘留同步（P2）**：Global Constraints 與 app 接線規則改為 `Finalize(ex ports.Exit)` 並明定四來源 Exit 語意——claude＝CloseSequence 回傳 ex（卡死 `Exited:false`）、codex 各來源一律 `Exit{Exited:false}`（meta 用 ProcessStillRunning，不填 ExitCode）。

### v7（2026-08-10）— 依第六輪 plan gate（CHANGES_REQUIRED，4 P1 + 2 P2）修訂

1. **舊型別全面替換（P1-1）**：執行區所有 `ClaudeTurns` 逐處改為 `ports.Turns`（Task 3 標題與斷言、Task 4b 斷言、檔案結構、commit 訊息），刪除「後文一律讀作」註記；最終 gate 增 grep gate（`grep -rn 'ClaudeTurns' internal/ cmd/ app.go` == 0）。
2. **Session lifecycle 狀態機＋token（P1-2）**：`sessionActive bool` 換成 phase 狀態機（idle／starting／active／ending）＋ `SessionToken`（generation）；`BeginEndSession`（starting → `ErrStartInProgress`，End 不得於 Start 未 Accept 時無聲成功）／`FinishEndSession(token)`（stale → `ErrStaleSession` no-op）取代 `MarkSessionEnded`。新測試：`TestStartEndBarrier`（Start×End 交錯的允許結果集）、`TestStaleEndAfterNewSessionIsNoop`（舊 End 不清新 session）。
3. **CloseSequence 逾時語意（P1-3）**：quiesce timeout 一律保留於回傳錯誤（不被 terminate 吞）；Terminate 後以 `killTimeout` 設第二上限，pump 卡死時不呼叫 wait、以零值 Exit 盡力 finalize、界限內回錯。測試改寫為三路徑（正常／逾時升級／卡死），逾時路徑斷言 err 含 quiesce timeout。
4. **Close 的 pending queue 契約（P1-4）**：選定顯式 abort+flush——Close 於 sink 關閉前 flush queue（事件全落 audit＋UI、無 user envelope）＋ fail-loud abort 通知。新測試 `TestCloseDuringPendingFlushesLoudly`（Begin→Emit→Close：事件到 sink、通知可見、晚到 Accept ErrClosed）；`TestEmitAfterCloseNoStateMutation` 斷言更新（3 dropped＋1 abort 通知）。
5. **ports 去 infrastructure 洩漏（P2-1）**：`ports.Exit` 中立 value（Code／StderrTail）、`Turns.Wait()` 回傳 `ports.Exit`、`Argv` 自 port 移除改為選配 `ports.Diagnostics`；`claude.Session.Wait` 改回傳 `ports.Exit`（欄位相容、M0 測試不受影響）；appcore／probe 全面改用 ports.Exit、不再 import proc。
6. **Exit 結構化進 finalizer（P2-2）**：`CloseSequence` 的 `finalize func(ports.Exit) error`；`RecordingLease.Finalize(ex)`＋`metaFn(ex)`——meta 的 ExitCode 有明確資料路徑；lease barrier 測試斷言 meta ExitCode == Wait 回傳值（143）。

### v6.1（2026-08-10）— 使用者補充：架構參考 go-ddd-adapters

非 gate 輪次、使用者輸入：程式架構參考 `~/playground/project/go-ddd-adapters`（+ `go-ddd-core`）。
新增「架構參考」節——M1 採用四項慣例（`internal/ports` 依賴方向：`ClaudeTurns` 移出為
`ports.Turns`、pinned-semantics godoc、sentinel error 明文化、contract-suite 紀律確認），
M2+ 再評估三項（errorsx coded taxonomy、共用 turnstest suite、repo 拆分）。Task 3 介面
定義位置調整，行為與測試內容零變更。

### v6（2026-08-10）— 依第五輪 plan gate（CHANGES_REQUIRED，4 P1 + 2 P2）修訂

1. **編譯殘留（P1-1）**：`TestEmitUserMessage` 改用 `BeginSubmit` 回傳的 id 呼叫 `AcceptSubmit(id, …)`；TS `Envelope` 增 `usage_semantics?: string`（store 測試不再觸發 excess-property error）；manager_test import 補齊（fmt/atomic/time/proc）。
2. **StartSession 原子交易（P1-2）**：新增 `BeginNewSessionSubmit(taskID)`——closed／sessionActive／start-in-progress 檢查、flush＋換代、reservation 建立在同一 mutex 交易內；`AcceptSubmit` 於 StartSession 路徑標 `sessionActive`、`MarkSessionEnded` 由 EndSession 清除。新測試 `TestStartSessionOwnershipBarrier`：injected provider-start barrier——恰一個 StartSession 成功、恰一次 provider start、輸家在建立 process／recorder／pump 前失敗、active 期間再開被拒、End 後可再開。
3. **EndSession closure（P1-3）**：`EndSession` 冪等（無 active session 回 nil）；UI New 移除吞錯 catch——`await EndSession(); s.reset()`，真實錯誤由 `call()` 顯示且不 reset。新增 `appcore.CloseSequence`（Close → WaitQuiesce（逾時升級 Terminate）→ **Wait()（cached Exit，Finalize 前取得）** → Finalize），正常與升級路徑共用；新測試 `TestCloseSequenceOrderAndTimeout` 以 call log 固定 `close,wait,finalize` 與 `close,terminate,wait,finalize` 兩序。
4. **Closed 最先檢查（P1-4）**：`Emit`／`EmitApprovalRequest`／`EmitApprovalDecision` 入口最前面攔截 closed——不 queue、不 Apply reducer、不動 totals、不寫 sink，只發單一 UI `stream_error`；`AcceptSubmit`／`RejectSubmit` closed → `ErrClosed`；`writeAndEmitLocked` 的死碼 closed 分支移除。新測試 `TestEmitAfterCloseNoStateMutation`（Begin → Close → Emit/Approval×2 → 斷言 queue/state/totals/sink 全不變、3 個 fail-loud stream_error、stale Accept ErrClosed）。
5. **P2 補強**：`TestApprovalDecisionDuringSubmitQueued`——early request＋decision 入列，flush 前 reducer 不動、flush 後固定 `user → waiting → approval → awaiting_approval → approval_decision → tool_running`；V2 驗收補 usage 標示規則（per-turn 無 `*`；cumulative／inconclusive 必須顯示 `*`＋tooltip；codex 恆 `*`）。

### v5（2026-08-07）— 依第四輪 plan gate（CHANGES_REQUIRED，4 P1 + 1 P2）修訂

1. **Coordinator 唯一 ownership（P1-1）**：`BeginSubmit()` 改回傳 `SubmissionID`（已有 owner → `ErrSubmitActive`）；`AcceptSubmit`／`RejectSubmit` 驗證 ID；`NewSession` 遞增 generation 使舊 ID 失效（stale → `ErrStaleSubmission` no-op）並先 flush 殘留 queue（掛舊 task）。新測試：`TestBeginSubmitExclusiveBarrier`（雙 Begin barrier、恰一個贏家）、`TestStaleAcceptAfterNewSessionIsNoop`（不汙染新 session）。
2. **Approval 進 coordinator queue（P1-2）**：`EmitApprovalRequest`／`EmitApprovalDecision` 於 pending 期間入列（decision 連同 `ResolveApproval` side effect 以 `pendingEntry.resolveApprove` 延後執行）。新測試：`TestApprovalDuringSubmitQueued`——Begin → early approval → Accept 固定得到 `user → waiting → approval → awaiting_approval`。
3. **New／session 汰換契約（P1-3）**：選定「backend 主導」——`StartSession` 遇 active session 拒絕；UI New＝先 `EndSession()`（quiesce）再 reset；`EndSession(claude)`＝Close → `WaitQuiesce(done,5s)`（逾時升級 Terminate）→ lease Finalize；pump 抽為 `appcore.Pump`／`WaitQuiesce`。新測試：`TestPumpQuiesceBeforeNewSession`（晚到事件不進新 task）、`TestWaitQuiesceTimeout`。
4. **Manager 序列化邊界（P1-4）**：sink 錯誤改「原 envelope 先 emit、合成 `stream_error` 後 emit」——fail-loud 路徑 event_id 仍嚴格遞增（`TestAuditFailureIsLoud` 補順序斷言）；`Close()` 進同一 mutex＋closed 旗標，close 後 `Emit` 不寫 sink、發「manager closed: event dropped」stream_error（`TestCloseEmitBarrier`，-race）；shutdown 順序明定 quiesce → Finalize → Close。
5. **Usage semantics UI 契約（P2-1）**：Envelope 增 `usage_semantics`（`session_total`｜`provider_latest`），Manager 依收斂分支填值；store `usageSemantics` state＋StatusBar `*` 標記與 tooltip；新測試：`TestUsageSemanticsCumulativeOverwrite`（ClaudeUsageCumulative=true 的 10→15=15＋semantics 斷言）、store semantics 測試。

### v4（2026-08-07）— 依第三輪 plan gate（CHANGES_REQUIRED，3 P1 + 2 P2）修訂

1. **文件自足化（P1-1）**：所有「同 v2」引用全文展開——Task 3（多輪 session 測試＋實作）、Task 6（Manager 全部測試＋manager.go／recording.go 完整實作＋app.go 六條接線規則）、Task 7（store 全規約＋十條測試）、Task 8–9（元件完整碼）、Task 10（安全測試＋renderMarkdown）、Task 11（SettingsBar／App.vue 完整碼）、Task 12（V0–V6 全文）。本檔為唯一執行依據。
2. **Submission coordinator（P1-2）**：Manager 增 `BeginSubmit`／`AcceptSubmit`／`RejectSubmit`——pending 期間 provider 事件入 buffer，接受後 user envelope 先行再依序 flush（`user → state_change(waiting) → provider events` 順序保證）；拒絕不發 user。新測試：`TestSubmitCoordinatorOrdering`（provider goroutine 先送 delta+completed 的 barrier 情境）、`TestRejectSubmitEmitsNoUser`、`NewSession` 清 pending。原殘餘風險 #5（僅靠 checklist）撤銷，改為 coordinator + `NewSession` 兜底 + V6 user-first live 驗證。
3. **RecordingLease（P1-3）**：session 錄流收尾 ownership 抽為 `appcore.RecordingLease`（sync.Once）；EndSession／new session／shutdown／fatal 四來源一律 `Finalize()`。新測試：8-goroutine barrier 恰一次（stop=1、close=1）、首錯保留且後續冪等回同一錯誤、stop 失敗仍 CloseWith；資料完整性由 Task 5 `TestRecordingSpansMultipleTurns` 與 V1 單檔三輪錄流覆蓋。
4. **Probe 清理補強（P2-1）**：`s.Close()` 錯誤不吞、錄流 `Fprintf` 逐筆檢查、Close 後啟 drain goroutine 防 scanner 阻塞自然退出。
5. **Usage VERDICT 規則（P2-2）**：turn1 長回答／turn2 極短回答；`o2 < o1/2` → per-turn、`o2 ≥ o1` → cumulative、其餘 inconclusive → 覆寫 fallback＋UI 標「provider 最新回報值」；Manager 以 `ClaudeUsageCumulative` 組態實作。

### v3（2026-08-07）— 第二輪 gate 修訂

ThreadRunner early-completed latch＋空 turn id error；codex 錄流 session-scoped 凍結；`Event.Role`／tool echo 路由／`StateWaiting`／submit 順序；probe `run() error`＋fallback EOF+Wait gating；sanitizer 測試、`go vet ./...` 明寫、V0.8 owner 邊界。快照 `36692861…801cd4`。

### v2（2026-08-07）— 第一輪 gate 修訂

Usage snapshot 語意；reducer 吃 Event＋Reset；Manager 序列化＋AuditSink fail-loud；probe Go driver＋Task 4b；Envelope role＋raw fallback；FileTree EvalSymlinks＋DOMPurify；ThreadRunner 對齊真實 wire；V0 迴歸 gate。快照 `f287cf4d…9f6e69`。

### v1（2026-08-07）

初稿：12 tasks。快照 `ecb78039…77d2c8`。
