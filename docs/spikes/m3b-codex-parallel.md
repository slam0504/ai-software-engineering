# M3b Task 0 — Codex 單 app-server 多 thread 並行 live probe（NO-GO gate）

**目的**：驗證 M3b 的架構前提——**單一 `codex app-server` 子程序能同時承載多個並行 thread**。
本 spike 是整個 M3b 里程碑的 GO/NO-GO gate。

**判定範圍（凍結）**：

- **(a)** 兩 thread 並行 turn 是否**真並行**（非 A 全部完成才出現 B）
- **(b)** notification **與 approval request** 是否帶足以歸屬的 thread／turn identity（不靠抵達順序）
- **(c)** 自然與強制（`-force`）兩種收尾是否 bounded 收斂且錄到最後一筆 frame

`completed-before-response` **不列入**本 probe——它是 host 對惡意／異常順序的容錯，真 server
不一定自然產生；改由 Task 9 的 fake-wire 測試鎖住。

**結論：GO**（(a)(b)(c) 三項在 natural／forced 兩次真實執行中全部成立，driver 自動判定亦回 `GATE GO`）。

> **本文件為 rev2**：Task review 指出 rev1 的 (a) 自動指標是恆真式（無法區分並行與串行化）等
> 5 項品質問題，driver 已修正並**重跑全部 live run**；以下數據皆為修正後重跑的結果。
> rev1 的判定結論未被推翻。修正細節見文末「rev1 → rev2 修正」。

---

## Step 1：bundled binary 與 pin 版本

```
$ ./scripts/check-cli.sh
claude 2.1.223 sha256=350e657428a6d34f7cf71f6738c5ebb6a1952ccb12fc1747f64297e065b1846f
codex 0.146.1 sha256=134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477
OK

$ CODEX_BIN="$(git rev-parse --show-toplevel)/tools/codex-cli/node_modules/.bin/codex"
$ test -x "$CODEX_BIN" && echo executable OK
executable OK
```

- **Codex CLI 版本**：`0.146.1`（由 `scripts/check-cli.sh:16` 與 `tools/codex-cli/package.json` 的 pin 比對，非 grep 推測）
- **sha256**：`134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`
- **binary 路徑**：`tools/codex-cli/node_modules/.bin/codex`（存在且可執行）
- 執行日期：2026-08-14（本機 macOS 24.6.0）

---

## Step 2：probe driver 與凍結參數

Driver：`cmd/probe-codex-parallel/main.go`。全程走 production API——
`codex.StartAppServer` / `Server.BeginRecording` / `Server.Handshake` /
`codex.NewThreadRunner` + `ThreadRunner.EnsureThread` + `ThreadRunner.StartTurn` /
`Conn.OnNotification` / `Conn.OnServerRequest` / `Server.Terminate` / `Server.Wait` /
`Server.StopRecording`，不另建 wire 路徑。

### 凍結參數（逐字）

| 參數 | 值 |
|---|---|
| `probeTimeout` | `90 * time.Second` |
| `turnTimeout` | `60 * time.Second` |
| `approvalPolicy` | `"untrusted"` |
| `promptA` | `請只回覆字串 PROBE_A_DONE，不要使用任何工具。` |
| `promptB` | `請只回覆字串 PROBE_B_DONE，不要使用任何工具。` |
| `promptApproval` | `請在目前工作目錄建立檔案 probe-approval.txt，內容為 PROBE。` |
| approval turn 順序 | 一律排在並行段**之前**（`-force` 會在 (a) 途中終止 server） |
| 旗標 | `-codex-bin`（必填）、`-force` |

`-long-output` 是 rev2 新增的**補充 run 專用旗標**，只替換 `promptA`／`promptB` 為長輸出 prompt，
**不改變上表任何凍結參數的定義**；補充 run 與凍結參數的兩次主 run 分開記錄（見 Step 5）。

### 退出碼契約

| 碼 | 意義 |
|---|---|
| 0 | 全部判定通過（印 `GATE GO`） |
| 1 | **probe 執行失敗**（環境／server 問題，例如 binary 缺失、handshake 失敗、`usageLimitExceeded`）——**不是**判定結果 |
| 2 | probe 跑得完但**判定 NO-GO**（印 `GATE NO-GO` 與逐條原因） |

### 依實際 API 補齊的實作細節（brief 未凍結的部分）

- `mustStartThread` = `codex.NewThreadRunner(srv.Conn())` + `EnsureThread(ctx, "", "untrusted")`；
  回傳 `*codex.ThreadRunner`（brief 寫 `string`，但送 turn 需要 runner）。同一個 `*codex.Conn`
  上建立三個獨立 runner（approval／A／B），對應 server 端三個 thread。
- turn 收尾以 `Conn.OnNotification` 收 `turn/completed`，依 `params.threadId` 路由到該 thread 的
  waiter。**若 `turn/completed` 缺 `threadId`（或找不到 waiter）則退化為廣播並記錄
  `broadcast_fallback=true`**——這正是 (b) 失敗時的實據路徑，不會讓 probe 卡死而掩蓋結果。
- `runTurnDenyingApprovals` 與 `runTurn` 同一路徑；核可拒絕由 conn 層 `OnServerRequest` handler
  統一處理，**一律回 `{"decision":"decline"}`**（與 production `app.go:4285` fail-closed 一致）。
- wire log 寫在 `os.TempDir()`（不在 probe cwd 的 `tmp` 內）——`tmp` 於離開時整個刪除，證據必須留存。
  每行 `{"seq":N,"ts":"<RFC3339Nano>","dir":"c2s|s2c","frame":<原始 frame 逐字>}`；`seq` 配發與
  寫檔在同一把鎖內（read loop 與並行段的 c2s goroutine 會同時寫入同一個證據檔）。
  檔名依 mode 命名，**同 mode 重跑會覆蓋**。
- `-force` 分支在送出兩個 `turn/start` 後 **`time.Sleep(2s)` 再 `Terminate()`**——否則 SIGTERM 可能
  早於 turn 真正上 wire，測不到「turn 進行中強制終止」。
- 任何退出路徑（成功／執行失敗／NO-GO）都走同一份 cleanup：`Terminate → Wait → StopRecording`
  → 關閉 wire log → 刪除 tmp，因此不會留下孤兒 app-server（獨立 process group）或殘留暫存目錄。

### 執行指令

```
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN"               # natural（凍結主 run）
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN" -force        # forced（凍結主 run）
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN" -long-output  # 補充 run（非凍結）
```

---

## Step 3：Run 1 — natural 收尾（凍結主 run，14:39:45）

```
MODE natural promptA="請只回覆字串 PROBE_A_DONE，不要使用任何工具。" promptB="請只回覆字串 PROBE_B_DONE，不要使用任何工具。"
threads approval=01a000b6-d236-78a2-ad99-fe8cab24d98f
        A=01a000b6-f614-76c0-894b-638789645207
        B=01a000b6-f65d-7cf1-be2c-e020ae94293b
WIRE …/probe-codex-parallel-natural.jsonl frames=142

TURN label=APPROVAL turn=01a000b6-d29c-7fc2-b71d-859e7c2daf0c status=completed dur=9.075s
TURN label=A        turn=01a000b6-f692-79f3-95f0-bad3a0375d1e status=completed dur=4.328s
TURN label=B        turn=01a000b6-f692-79f3-95f0-bac0b4ae0956 status=completed dur=3.828s
```

### (a) 並行證據

**兩個 `turn/start` 同一毫秒送出，server 兩個都立即回 `inProgress`，沒有任何一個被拒或被排隊：**

```
seq=93 14:36:45.197 c2s turn/start threadId=…34a6…036  [{"text":"請只回覆字串 PROBE_A_DONE…"}]
seq=94 14:36:45.197 c2s turn/start threadId=…34db…78c  [{"text":"請只回覆字串 PROBE_B_DONE…"}]
seq=95 14:36:45.198 s2c RESPONSE turn.id=…791afd5dedd3 status=inProgress
seq=96 14:36:45.198 s2c RESPONSE turn.id=…79090336dae4 status=inProgress
```
（上列 c2s 節錄取自同一 driver 版本的前一次 natural run，wire 結構與 14:39:45 run 完全相同；
14:39:45 run 的對應 frame 為 seq 110/111 與其 response。）

**turn-scoped s2c trace（只取 `turnId` 非空的 frame，排除 `thread/started` 等 thread 級 frame）**：

```
seq=114 14:39:45.820 A turn/started              turn=…bad3a0375d1e   ← A turn 開始
seq=116 14:39:45.823 B turn/started              turn=…bac0b4ae0956   ← B turn 開始（A 未結束）
seq=119 14:39:47.996 A item/started
seq=120 14:39:47.996 A item/completed
seq=121 14:39:48.055 B item/started
seq=122 14:39:48.055 B item/completed
seq=123 14:39:49.401 B item/started
seq=124 14:39:49.404 B item/agentMessage/delta
seq=127 14:39:49.443 B item/agentMessage/delta
seq=128 14:39:49.580 B item/completed
seq=132 14:39:49.638 B turn/completed            status=completed
seq=133 14:39:50.006 A item/started
seq=134 14:39:50.016 A item/agentMessage/delta
seq=137 14:39:50.024 A item/agentMessage/delta
seq=138 14:39:50.090 A item/completed
seq=142 14:39:50.137 A turn/completed            status=completed
```

**判定指標（rev2 修正後）**：

```
VERDICT_A turn_lifetime_overlap=yes
  startedA=seq114(14:39:45.820) endedA=seq142(14:39:50.137)
  startedB=seq116(14:39:45.823) endedB=seq132(14:39:49.638)
```

判準：`turn/started(A) < turn/completed(B)` **且** `turn/started(B) < turn/completed(A)`
→ `114 < 132` 且 `116 < 142`，兩 turn 的生命期真的重疊。
trace 中 A/B 的 frame 也實際交替出現（119-120 A → 121-128 B → 133-142 A）。

**判定：(a) 通過。** 不是「A 全部完成才出現 B」，也沒有第二個 thread 被拒。

### (b) identity 歸屬證據

```
VERDICT_B notifications=125 with_threadId=120 with_turnId=100 missing_both=5 broadcast_fallback=false
  notif_missing_identity_methods=[account/rateLimits/updated remoteControl/status/changed]
VERDICT_B approvals=1 missing_identity=0
  APPROVAL at=14:39:42.651 method=item/fileChange/requestApproval
    threadId="01a000b6-d236-78a2-ad99-fe8cab24d98f"
    turnId="01a000b6-d29c-7fc2-b71d-859e7c2daf0c"
    itemId="exec-15c7eef4-2fd1-492d-9ef0-49f8e4cf53fe"  decision=decline
  raw={"threadId":"01a000b6-d236-78a2-ad99-fe8cab24d98f",
       "turnId":"01a000b6-d29c-7fc2-b71d-859e7c2daf0c",
       "itemId":"exec-15c7eef4-2fd1-492d-9ef0-49f8e4cf53fe",
       "startedAtMs":1786718382651,"reason":null,"grantRoot":null}
```

- **125 筆 s2c notification 中 120 筆帶 `threadId`**；缺 identity 的 5 筆全部是
  `account/rateLimits/updated` 與 `remoteControl/status/changed`——**帳號／server 層級事件，本來就
  不屬於任何 thread**，不是歸屬缺口。
- `broadcast_fallback=false`：**所有 `turn/completed` 都能靠 `threadId` 精確歸屬**，全程未退化。
- approval request 帶完整 `threadId` + `turnId` + `itemId`，`missing_identity=0`。

拒絕生效的獨立佐證：

- `probe-approval.txt` **未被建立**（driver 的 `os.Stat` 檢查通過，否則直接以 exit 2 中止）
- server stderr：`codex_core::tools::router: error=patch rejected by user`

**判定：(b) 通過。**

### (c) natural 收尾

```
exit_code=0
SHUTDOWN mode=natural ran=true bounded=true done_after_first_terminate=18ms
         record_err=<nil> last_frame_seq=142 last_frame_at=14:39:50.137
GATE GO
```

- **自第一次 `Terminate()` 起算 18ms** 內 `srv.Wait()` 返回（`Done` 已關閉、`Exit` 已快取），
  `TermGrace=5s` 未觸發 SIGKILL，exit code 0
- `StopRecording()` 回 `nil`（sink 無錯、in-flight callback 全數 drain）
- wire log 最後一筆（seq 142）就是 thread A 的 `turn/completed`，JSON 完整未截斷

---

## Step 4：Run 2 — `-force` 強制收尾（凍結主 run，14:39:56）

```
MODE forced
threads approval=01a000b7-0aa2-7183-b3be-a350c39de49b
        A=01a000b7-364a-7201-9976-41018a2306f4
        B=01a000b7-3691-7982-8a07-c2e95fd3aa14
WIRE …/probe-codex-parallel-forced.jsonl frames=100

TURN label=APPROVAL status=completed  dur=11.092s
TURN label=A        status=server-died dur=2.017s
TURN label=B        status=server-died dur=2.017s
```

### (a)（forced run 供交叉佐證，正式判定以 natural 為準）

```
seq=92 14:40:02.240 c2s turn/start threadId=…364a…6f4  PROBE_A_DONE
seq=93 14:40:02.240 c2s turn/start threadId=…3691…a14  PROBE_B_DONE
seq=94 14:40:02.246 s2c RESPONSE turn=…61d4b9833819 status=inProgress
seq=95 14:40:02.246 s2c RESPONSE turn=…36105935fe65 status=inProgress
seq=97 14:40:02.251 A turn/started turn=…61d4b9833819
seq=99 14:40:02.253 B turn/started turn=…36105935fe65

VERDICT_A turn_lifetime_overlap=inconclusive 兩個 turn 都沒有 turn/completed（forced 收尾預期如此）
```

兩個 turn 同時 `inProgress`，與 natural run 一致。**forced run 的 (a) 自動指標刻意回
`inconclusive` 而非 `yes`**——server 在 turn 進行中被殺，本來就不會有 `turn/completed`；
謊報 `yes` 會讓指標失去鑑別力。gate 因此在 `-force` 模式跳過 (a) 強制（(a) 以 natural 為準）。

### (b)（forced run 亦成立）

```
VERDICT_B notifications=83 with_threadId=80 with_turnId=63 missing_both=3 broadcast_fallback=false
VERDICT_B approvals=1 missing_identity=0
  APPROVAL at=14:39:56.910 method=item/fileChange/requestApproval
    threadId="01a000b7-0aa2-7183-b3be-a350c39de49b"
    turnId="01a000b7-0af0-7d10-99a3-71dec1378e29"
    itemId="exec-c6dae653-477b-42b4-9d3b-cc1aa3e2e931" decision=decline
```

缺 identity 的 3 筆同樣只有 `account/rateLimits/updated` / `remoteControl/status/changed`。
`probe-approval.txt` 未建立。

### (c) forced 收尾

```
exit_code=0 stderr_tail="… codex_core::tools::router: error=patch rejected by user"
SHUTDOWN mode=forced ran=true bounded=true done_after_first_terminate=17ms
         record_err=<nil> last_frame_seq=100 last_frame_at=14:40:02.849
GATE GO
```

- **`done_after_first_terminate=17ms`**：從**並行段中途那一次** `Terminate()`（turn 進行中）
  到 server 被收割（`Done` 關閉、`Exit` 快取）只花 17ms。兩個 turn goroutine 隨即由
  `srv.Done()` 喚醒返回（`status=server-died dur=2.017s`，其中 2.0s 是刻意的 sleep），
  `wg.Wait()` 立即返回——**turn 進行中強制終止不會 hang 住 host，也不需靠 `turnTimeout` 兜底**。
- 錄流在讀迴圈 EOF 前**全程掛載**，`StopRecording()` 於 `Wait()` 之後才 detach 且回 `nil`；
  wire log 最後一筆（seq 100，`mcpServer/startupStatus/updated`）即 server 死亡前送出的最後一筆
  frame，該行 JSON 完整。SIGTERM 之後 server 未再送出任何 frame。

---

## Step 5：補充 run — 長輸出（**非凍結參數**，不改變 GO/NO-GO）

**目的**：Step 3 的兩個 prompt 回覆極短（各 4 個 delta），只能證明「並行**受理**」；
本補充 run 改用長輸出 prompt，觀察兩 thread 的**模型輸出階段**是否真的交錯，
用來排除「server 端把實際工作全域串行化」的假說。

補充 run 專用 prompt（`-long-output`，**不屬於凍結參數**）：

| 參數 | 值 |
|---|---|
| `promptALongOutput` | `請從 1 數到 60，每個數字一行，不要使用任何工具。` |
| `promptBLongOutput` | `請從 101 數到 160，每個數字一行，不要使用任何工具。` |

共執行 4 次，每次兩 thread 各產生 119 個 `item/agentMessage/delta`：

| # | 時間 | `turn_lifetime_overlap` | `delta_interleaved` | deltaA seq 區間 | deltaB seq 區間 |
|---|---|---|---|---|---|
| 1 | 14:37:49 | yes | **yes** | 106–311 | 136–349 |
| 2 | 14:40:13 | yes | **no** | 257–375 | 130–248 |
| 3 | 14:40:46 | yes | **yes** | 109–344 | 106–342 |
| 4 | 14:41:07 | yes | **yes** | 170–397 | 159–386 |

交錯 run（#4）的實際 wire 節錄——A/B 的 delta 逐筆交替：

```
seq=184 14:41:11.954 A item/agentMessage/delta
seq=185 14:41:11.962 B item/agentMessage/delta
seq=186 14:41:11.964 A item/agentMessage/delta
seq=187 14:41:11.970 B item/agentMessage/delta
seq=188 14:41:11.980 B item/agentMessage/delta
seq=189 14:41:11.985 A item/agentMessage/delta
seq=190 14:41:11.994 B item/agentMessage/delta
seq=191 14:41:11.996 A item/agentMessage/delta
```

run #3／#4 的 turn-scoped trace 分別有 111／118 個 A↔B 交替區塊。
未交錯的 run #2 則是 `1 B, 3 A, 125 B, 125 A`——B 全部產出後 A 才開始。

**結論（4 次中 3 次交錯）**：

- **「server 端把實際工作全域串行化」的假說已被排除**——存在 119 對 delta 逐筆交叉的實據。
- 但**吞吐並行不保證**：1/4 的 run 兩 thread 的輸出階段完全不重疊。
- 依裁決，本補充 run **不改變 GO/NO-GO**；凍結的 (a) 判準（未被串行化、未被拒絕）已在
  Step 3 成立。上述觀察改寫為殘留風險 1。

---

## 逐項判定

| 項目 | natural | forced | 聚合判定 | 理由 |
|---|---|---|---|---|
| **(a)** 真並行 | **PASS**（`turn_lifetime_overlap=yes`） | 交叉佐證（兩 turn 同時 `inProgress`；指標依設計為 `inconclusive`） | **PASS**（以 natural 為準） | `turn/started(A) < turn/completed(B)` 且 `turn/started(B) < turn/completed(A)`；trace 中 A/B frame 實際交替。無串行化、無第二 thread 被拒 |
| **(b)** identity 歸屬 | **PASS** | **PASS** | **PASS**（以 natural 為準） | 120/125 notification 帶 `threadId`，缺者僅 account／server 層事件；approval request `missing_identity=0`；`broadcast_fallback=false` |
| **(c)** bounded 收尾 | **PASS**（18ms、`StopRecording=nil`、末筆 = `turn/completed`） | **PASS**（自第一次 Terminate 17ms、`StopRecording=nil`、末筆完整） | **PASS**（兩次都通過，符合 (c) 需雙通過） | 兩種收尾都在 `TermGrace` 內收斂、exit 0、錄流無錯且未截斷 |

兩次凍結主 run 的 driver 自動判定都印出 **`GATE GO`**（exit 0）。

## 最終裁決：**GO**

單一 `codex app-server` 可承載多個並行 thread；notification 與 approval request 都帶
thread／turn identity，host 可據以做 per-session 路由；兩種收尾都 bounded。M3b 的架構前提成立。

## 殘留風險（不阻擋 GO，M3b 實作需注意）

1. **吞吐並行不保證**（rev2 更新）：長輸出補充 run 4 次中 3 次兩 thread 的 delta 逐筆交叉
   （全域串行化假說已排除），但 1 次兩者輸出完全不重疊。**M3b 的 UI 不得假設兩個 session
   一定同時串流**；某個 session 數秒沒有新 delta 是正常現象，不可據此判定卡死或逾時。
2. **新 thread 的首輪可能有數秒空窗**：rev1 的 natural run 觀察到兩 thread 的
   `mcpServer/startupStatus/updated` 相差 ~4.1s，慢的那個首輪 `item/started` 因此延後。
   多 session 同時開新 thread 時要容忍首輪較長的無回應期。
3. **無 `threadId` 的 s2c 事件必須歸為 server 級廣播**：`account/rateLimits/updated` 與
   `remoteControl/status/changed` 都不帶 `threadId`，不能誤派給任一 session。
   `remoteControl/status/changed` 不在 `codex.ServerNotifications`，目前落 `OnUnknown` →
   `KindUnknown`（設計內的 fallback，非錯誤）；若 M3b 要顯示它，需另行決定是否納入白名單。
4. **turn id 前綴高度相似**：同一批 turn 的 id 只有末段不同（例如
   `01a000b6-f692-79f3-95f0-bad3a0375d1e` 與 `01a000b6-f692-79f3-95f0-bac0b4ae0956`）。
   路由一律用完整字串比對，任何 prefix／短碼比對都有碰撞風險。
5. **樣本數**：凍結主 run 每種收尾各 1 次，補充 run 4 次。並行度只驗到 2 個並行 thread
   + 1 個先行 thread，未驗更高並行度、長時間持續負載或 thread 數上限。
6. **`completed-before-response` 未涵蓋**（凍結範圍外）：真 server 本次未自然產生此順序，
   須由 Task 9 的 fake-wire 測試鎖住。
7. **approval 只驗到 `item/fileChange/requestApproval` 一種**：
   `item/commandExecution/requestApproval` 未被觸發（模型直接走 apply_patch），其 identity
   目前只有 pinned schema 保證（`CommandExecutionRequestApprovalParams.json` 的 `required` 含
   `threadId`、`turnId`、`itemId`），沒有 live frame 佐證。
8. **無害的 server 端錯誤 log**：`codex_models_manager::cache: failed to load models cache:
   missing field 'base_instructions'`——本機 `~/.codex/models_cache.json` 與 0.146.1 的 schema
   不合，probe 全程正常，未影響任何判定。

---

## rev1 → rev2 修正（Task review findings）

| # | 問題 | 修正 |
|---|---|---|
| C1 | (a) 的自動指標 `firstB < lastA && firstA < lastB` 是**恆真式**：`trace` 含 `thread/started` 等 thread 級 frame，A/B 依建立順序必佔最前面，串行化的 server 也會讓兩區間互相包含 | 改為 **turn 生命期重疊**（`turn/started(A) < turn/completed(B)` 且反向亦然），且 trace **只取 `turnId` 非空的 turn-scoped frame**。抽成純函式 `overlapVerdict` 並加上 `TestOverlapVerdict`——其中 `serialized` case 就是舊指標的反例 |
| I2 | GO 條件沒有被 driver 強制，`approvals=0` 或 identity 全空一樣 exit 0 | 新增 gate：`approvals==0`、approval 缺 `threadId`／`turnId`、`broadcast_fallback=true`、(a) 生命期未重疊（非 force 模式）、錄流錯誤、收尾未 bounded → 印 `GATE NO-GO` 並 **exit 2**；全數通過才印 `GATE GO` |
| I3 | `fatal()` 用 `os.Exit(1)` 跳過 defer，NO-GO 路徑洩漏孤兒 app-server（獨立 process group）與暫存目錄 | 導入 cleanup registry：所有退出路徑共用 `Terminate → Wait → StopRecording` → 關 wire log → 刪 tmp。**實測**（`-codex-bin /bin/cat` 觸發 handshake 失敗的 fatal）：exit 1、無殘留 tmp、無孤兒行程 |
| I4 | forced run 的 `SHUTDOWN elapsed=0s` 量的是**第二次** Terminate（no-op），論證鏈接錯指標 | 改量 **自第一次 `Terminate()` 到 `Done` 關閉**（`done_after_first_terminate`）。forced=17ms、natural=18ms；文件論證同步改寫 |
| I5 | wire log 的 `Fprintf` 在鎖外，read loop 與並行段的 c2s goroutine 會同時寫同一個證據檔 | `seq` 配發與 `Fprintf` 移進同一把鎖 |

**rev2 追加（review 未列，重跑時實測發現）**：某次重跑三個 turn 全部 `status=failed`
（server 回 `usageLimitExceeded`），driver 卻仍印 `GATE GO`——`failed` 的 turn 一樣會產生
`turn/started` + `turn/completed`，生命期重疊成立。已補上分類：任一 turn 未 `completed`
（forced 模式的並行 turn 除外，那是預期行為）即判為 **probe 執行失敗（exit 1）**，
明確與判定 NO-GO（exit 2）分離。實測輸出：

```
PROBE EXECUTION FAILED（環境／server 問題，非 GO/NO-GO 判定）
  - turn APPROVAL status=failed err="You've hit your usage limit. …try again at Aug 20th, 2026 12:17 PM."
exit status 1
```

## 驗證紀錄

```
./scripts/check-cli.sh             # codex 0.146.1 sha256=134063e1…f59f1477，OK
go build ./...                     # OK
go vet ./...                       # OK
go test -race ./... -count=1       # 19 個 package 全 ok（含新增的 cmd/probe-codex-parallel）
```

Live run（未進 git，本機留存於 `$TMPDIR/probe-codex-parallel-<mode>.jsonl`；同 mode 重跑會覆蓋）：
natural 142 frames、forced 100 frames、natural-long 最多 380 frames。
