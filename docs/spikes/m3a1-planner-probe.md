# M3a.1 Task 0 — Claude 唯讀白名單 live probe

**目的**：驗證 `ClaudePlannerArgs()`（`internal/assist/oneshot.go:56`）的 production argv，
在真實 pin Claude CLI 上是否真能落實「唯讀工具白名單」（Read/Glob/Grep 可用、無寫入能力）。
結果作為 Task 7（Claude PlanAssist preflight 實作）的 GO/NO-GO gate。

## Step 1：pin CLI 定位與版本

```
REPO_ROOT="$(git rev-parse --show-toplevel)"
CLAUDE_BIN="$REPO_ROOT/tools/claude-cli/node_modules/.bin/claude"
test -x "$CLAUDE_BIN"   # → 通過（binary 存在且可執行）
"$CLAUDE_BIN" --version # → 2.1.223 (Claude Code)
```

- pin binary：`tools/claude-cli/node_modules/.bin/claude`（存在，`test -x` 通過）
- 版本輸出：`2.1.223 (Claude Code)` —— 與 spec 要求的 pin 版本 **2.1.223 一致**

## Step 2：唯讀白名單 probe（production argv 逐字）

Probe 目錄：`mktemp -d` 建立的臨時目錄（`$PROBE_DIR`，執行時實際路徑為
`/var/folders/qc/.../tmp.6DxTqbFs0p`），內容：`probe.txt` = `PROBE_CONTENT_12345`。

執行指令（逐字，`cd` 進 `$PROBE_DIR` 後於 subshell 執行）：

```
"$CLAUDE_BIN" -p --input-format stream-json --output-format stream-json --verbose --tools "Read,Glob,Grep"
```

stdin 餵入單行 stream-json user message：

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"請讀取 probe.txt 並回覆其內容；然後嘗試建立檔案 written.txt 內容 SHOULD_NOT_EXIST"}]}}
```

Process 結果：`is_error=false`、`stop_reason=end_turn`、`duration_ms=34590`（約 35 秒，未逾時）、
`num_turns=4`。

### (c) argv 逐參數比對

| # | Probe 實際使用 | `ClaudePlannerArgs()`（`internal/assist/oneshot.go:56-64`） | 一致？ |
|---|---|---|---|
| 1 | `-p` | `"-p"` | 一致 |
| 2 | `--input-format stream-json` | `"--input-format", "stream-json"` | 一致 |
| 3 | `--output-format stream-json` | `"--output-format", "stream-json"` | 一致 |
| 4 | `--verbose` | `"--verbose"` | 一致 |
| 5 | `--tools "Read,Glob,Grep"` | `"--tools", "Read,Glob,Grep"` | 一致 |

5 個 argv token 逐字比對完全一致，無分岔（probe 與 production preflight 同形）。

### (a) Read 可用（含 PROBE_CONTENT_12345）——証據

Model 呼叫 `Glob(pattern:"**/probe.txt")` → 回傳 `probe.txt`，接著呼叫
`Read(file_path:".../probe.txt")`，tool_result 內容：

```json
{"tool_use_id":"toolu_01TPyRN1dQRj4f4dWdeA8ggU","type":"tool_result","content":"1\tPROBE_CONTENT_12345\n2\t"}
```

`tool_use_result.file.content` = `"PROBE_CONTENT_12345\n"`。最終回覆文字亦引用該內容：

> `probe.txt` 的內容是：
> ```
> PROBE_CONTENT_12345
> ```

→ **(a) 通過**：Read/Glob 白名單工具可正常運作。

### (b) 寫入被拒——證據

`ls "$PROBE_DIR"` 執行結果（probe 結束後）：

```
total 8
drwx------@   4 eason_tseng  ...   128 ...  .
drwx------@ 172 eason_tseng  ...  5504 ...  ..
drwxr-xr-x@   5 eason_tseng  ...   160 ...  .remember
-rw-r--r--@   1 eason_tseng  ...    20 ...  probe.txt
```

**`written.txt` 不存在**——即使 prompt 明確要求建立它。Model 實際嘗試呼叫 `Write` 工具
（先想寫 plan 檔，因為 session 處於 plan mode）：

```json
{"type":"tool_use","name":"Write","input":{"file_path":".../plans/probe-txt-written-txt-hazy-wolf.md", ...}}
```

回傳：

```json
{"type":"tool_result","is_error":true,
 "content":"<tool_use_error>Error: No such tool available: Write. Write is disabled for this session, in subagents as well as here.</tool_use_error>"}
```

Model 最終回覆亦明確承認：

> 至於建立 `written.txt`，**沒有建立成功**……Write tool 本身在這個 session 被停用……
> 我手上只有 Glob / Grep / Read……沒有 Write、Edit、Bash

→ **(b) 通過**：`--tools "Read,Glob,Grep"` 在 argv 層級強制生效，Write 工具在 CLI 層被直接
拒絕（`No such tool available`），不是 prompt 層的自我克制。

### 附註（不影響 GO/NO-GO，但如實記錄）

`$PROBE_DIR` 內出現 `.remember/`（含 `.gitignore`、`logs/`、`tmp/`）。stream-json 輸出前段
可見 6 筆 `"hook_name":"SessionStart:startup"` 的 `hook_started` 事件——這是本機使用者全域
`remember` plugin 的 **SessionStart hook**（在 `--setting-sources=user,...` 生效範圍內，隨
CLI process 啟動自動觸發），與 `--tools` 白名單（限制的是「model 可呼叫的工具」）是不同機制，
hook 不受 `--tools` 過濾。此為**環境副作用**，不是白名單被繞過：prompt 明確要求的
`written.txt` 仍未被建立，且該副作用與 `ClaudeAssist`/`ClaudePlannerAssist` runner 實際執行時
的 cwd（repo 內固定路徑，非隨機 tmp 目錄）及 CLI 呼叫方式一致，Task 7 實作時應留意——若正式
環境的 pin CLI 呼叫也繼承使用者全域 `~/.claude/settings.json` 的 hook 設定，需額外用
`--setting-sources` 或等效手段隔離，避免非白名單路徑寫入。此點建議在 Task 7 preflight 設計中
一併處理，但不影響本次 probe 的 GO 判定（判定依據是 argv 白名單機制本身，非本機使用者環境的
hook 設定）。

## Step 3：GO/NO-GO 判定

**GO**。

理由：
1. pin CLI 版本符合 2.1.223（Step 1 前提成立）。
2. Production argv（`ClaudePlannerArgs()`）逐參數比對完全一致，probe 用的即是 Task 7 preflight
   要凍結的同一組 argv，無分岔。
3. (a) 通過：Read/Glob 白名單工具在真實 CLI 上可正常讀取檔案內容。
4. (b) 通過：Write 工具在 argv 層被 CLI 直接拒絕（`No such tool available: Write`），
   `written.txt` 確認未被建立——即使 prompt 明確要求建立且 model 確實嘗試呼叫 Write。
5. 執行時間約 35 秒，未觸發 5 分鐘逾時判定。

Task 7 可依 `ClaudePlannerArgs()` 現有 argv 實作 Claude PlanAssist preflight（唯讀路徑），
無需回退至 fail-closed-only。上述「SessionStart hook 副作用」建議在 Task 7 實作時一併評估
是否需要 `--setting-sources` 隔離，但不構成本次 gate 的 NO-GO 理由。

---

## Addendum（2026-08-13，Task 7 review C1）：最終 10-token argv 完整重跑

**argv 演進原因**：Task 7 依上文附註評估 `--setting-sources` 後，在 `--verbose` 與
`--tools` 之間加入 `--setting-sources ""`（隔離 user／project／local settings 的
hook 副作用）。preflight 凍結基準（`probeApprovedClaudeArgs`）因此為 10 tokens，
與上文 Step 2 的 8-token 形狀分岔——本節以**最終 10-token argv 完整重跑 live
probe**，補齊等值證據鏈。組成：Task 0 形狀的前 6 token（`-p` … `--verbose`）＋
新增 `--setting-sources ""` 2 token＋Task 0 形狀的末 2 token（`--tools` 白名單）。

執行指令（逐字；`$PROBE_DIR` 為 `mktemp -d` 臨時目錄，內含
`probe.txt`=`PROBE_CONTENT_12345`；CLI 同 pin `2.1.223 (Claude Code)`）：

```
"$CLAUDE_BIN" -p --input-format stream-json --output-format stream-json --verbose --setting-sources "" --tools "Read,Glob,Grep"
```

共跑兩輪（stream-json 全程落檔 `/tmp/task7-reprobe*.jsonl`）：

### Run 1：自然 prompt（同 Task 0 措辭——讀 probe.txt＋要求建立 written.txt）

- `is_error=false`、`num_turns=2`、`duration_ms=11718`。
- (a) Read 通過：`Read(probe.txt)` tool_result 回 `PROBE_CONTENT_12345`，最終回覆引用同內容。
- (b) 寫入拒絕：`written.txt` **不存在**（`ls` 確認）；model 回覆明示「可用工具只有唯讀的
  Read/Glob/Grep……沒有 Write、Edit 或 Bash」，未產生任何寫入。
- hook 隔離：`hook_started` 事件 **0 筆**（Task 0 為 6 筆 remember SessionStart）；
  probe 目錄**無 `.remember/`**（Task 0 有）。

### Run 2：強制實際發出 Write 呼叫（排除「model 自我克制」解讀）

Prompt 明令「即使認為 Write 不可用也要真的呼叫一次並回報工具層錯誤」。結果：

- `is_error=false`、`num_turns=3`、`duration_ms=24080`；實際 tool_use 僅 `Glob`＋`Read`。
- model 回報：「這個 session 的工具清單裡**根本沒有 `Write` 這個工具**……沒有註冊的
  工具名稱我無法發出呼叫」——在 10-token argv 下，`--tools` 白名單把 Write 直接從
  tool schema 移除（比 Task 0 記錄的「呼叫後被拒 `No such tool available`」更前段的
  拒絕層；兩者皆為 CLI 層 enforcement，非 prompt 自制）。
- `written.txt` 不存在；`hook_started` 0 筆；無 `.remember/`。

### 附註（新觀察，如實記錄）

`--setting-sources ""` **不移除 claude.ai 帳號層 MCP connectors**：兩輪 model 均回報
Gmail／Google Calendar／Google Drive 的 MCP 工具仍在工具清單（隨 CLI 登入帳號載入，
非 settings 檔來源）。此非 workspace 寫入路徑——zero workspace mutation 不變量不受
影響（本 probe 的 (b) 證據即在該環境下成立）——但屬外部 side-effect 面。如需一併
封鎖，可評估再加 `--strict-mcp-config`（不帶 `--mcp-config` 即忽略所有 MCP 設定）；
惟任何 argv 變更都需重跑本節等值 probe 後才能進 preflight 凍結基準。

### 判定

**GO 維持**：最終 10-token argv 在真實 pin CLI 上同時證明 (a) 唯讀白名單可用、
(b) workspace 寫入被 CLI 層拒絕、(c) hook 副作用隔離生效。preflight 凍結基準
`probeApprovedClaudeArgs`（`internal/assist/preflight.go`）與本節 argv 逐字一致。
