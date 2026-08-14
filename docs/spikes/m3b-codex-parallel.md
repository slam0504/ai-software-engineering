# M3b Task 0 — Codex 單 app-server 多 thread 並行 live probe（NO-GO gate）

**目的**：驗證 M3b 的架構前提——**單一 `codex app-server` 子程序能同時承載多個並行 thread**。
本 spike 是整個 M3b 里程碑的 GO/NO-GO gate。

**判定範圍（凍結）**：

- **(a)** 兩 thread 並行 turn 是否**真並行**（wire frame 時間上交錯，非 A 全部完成才出現 B）
- **(b)** notification **與 approval request** 是否帶足以歸屬的 thread／turn identity（不靠抵達順序）
- **(c)** 自然與強制（`-force`）兩種收尾是否 bounded 收斂且錄到最後一筆 frame

`completed-before-response` **不列入**本 probe——它是 host 對惡意／異常順序的容錯，真 server
不一定自然產生；改由 Task 9 的 fake-wire 測試鎖住。

**結論：GO**（(a)(b)(c) 三項在 natural／forced 兩次真實執行中全部成立）。

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

- **Codex CLI 版本**：`0.146.1`（與 `tools/codex-cli/package.json` 的 pin 一致，由 `scripts/check-cli.sh:16` 比對，非 grep 推測）
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
  每行格式：`{"seq":N,"ts":"<RFC3339Nano>","dir":"c2s|s2c","frame":<原始 frame 逐字>}`。
  `ts`／`seq` 是 (a) 判定所需的時間軸，frame 本體未經改寫。
- `-force` 分支在送出兩個 `turn/start` 後 **`time.Sleep(2s)` 再 `Terminate()`**——否則 SIGTERM 可能
  早於 turn 真正上 wire，測不到「turn 進行中強制終止」。

### 執行指令

```
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN"        2>&1 | tee /tmp/probe-natural.log
go run ./cmd/probe-codex-parallel -codex-bin "$CODEX_BIN" -force 2>&1 | tee /tmp/probe-forced.log
```

兩次都 exit 0。wire log：
`$TMPDIR/probe-codex-parallel-natural.jsonl`（124 frames）、
`$TMPDIR/probe-codex-parallel-forced.jsonl`（95 frames）。

---

## Step 3：Run 1 — natural 收尾

Thread 三者皆由同一個 app-server 建立：

```
threads approval=01a000a2-e084-7531-8ca5-4ac8d71ab764
        A=01a000a3-1ff3-7f40-8848-e3b7a1148881
        B=01a000a3-2031-7ec3-90e8-531787ad6af4
```

```
TURN label=APPROVAL thread=01a000a2-e084…764 turn=01a000a2-e242…437 status=completed dur=15.794s
TURN label=A        thread=01a000a3-1ff3…881 turn=01a000a3-2069-7ca3-822a-7cb85de3e1fe status=completed dur=9.134s
TURN label=B        thread=01a000a3-2031…4f4 turn=01a000a3-2069-7ca3-822a-7ca58de01e80 status=completed dur=4.333s
```

### (a) 並行證據

**兩個 `turn/start` 在同一毫秒送出，server 兩個都立即回 `status:"inProgress"`，沒有任何一個被拒或被排隊：**

```
seq=91 14:18:05.801 c2s turn/start {"input":[{"text":"請只回覆字串 PROBE_B_DONE…"}],"threadId":"01a000a3-2031…4f4"}
seq=92 14:18:05.801 c2s turn/start {"input":[{"text":"請只回覆字串 PROBE_A_DONE…"}],"threadId":"01a000a3-1ff3…881"}
seq=93 14:18:05.802 s2c RESPONSE   {"turn":{"id":"01a000a3-2069…3e1fe","status":"inProgress",…}}
seq=94 14:18:05.802 s2c RESPONSE   {"turn":{"id":"01a000a3-2069…01e80","status":"inProgress",…}}
```

s2c frame 交錯軌跡（只列 thread A/B 的 frame；`VERDICT_A interleaved=true framesA=17 framesB=17`）：

```
seq=85  14:18:05.736 A thread/started
seq=90  14:18:05.801 B thread/started
seq=96  14:18:05.816 A turn/started       turn=…7cb85de3e1fe   ← A turn 開始
seq=98  14:18:05.833 B turn/started       turn=…7ca58de01e80   ← B turn 開始（A 尚未結束）
seq=100 14:18:08.596 B item/started
seq=103 14:18:09.938 B item/agentMessage/delta
seq=107 14:18:10.066 B item/completed
seq=111 14:18:10.133 B turn/completed     turn=…7ca58de01e80 status=completed durationMs=4306
seq=113 14:18:13.281 A item/started
seq=116 14:18:14.710 A item/agentMessage/delta
seq=120 14:18:14.887 A item/completed
seq=124 14:18:14.935 A turn/completed     turn=…7cb85de3e1fe status=completed durationMs=9128
```

**判定：(a) 通過。** A 的 turn 生命期 05.816 → 14.935 完整包住 B 的 05.833 → 10.133；
B 的整輪（含 item/delta/completed）全部發生在 A 的 turn 仍 `inProgress` 期間。
不是「A 全部完成才出現 B」，也沒有第二個 thread 被拒。

**如實補記（不影響判定，列為殘留風險）**：本次 run 中兩 turn 的**模型輸出階段**其實沒有交錯——
B 的 delta 全在 A 的 delta 之前。可觀察到的原因是 per-thread 的 `mcpServer/startupStatus/updated`
似乎被序列化：B 於 `14:18:06.447` 就緒、A 遲至 `14:18:10.520` 才就緒（差 ~4.1s），A 的首個
`item/started` 也因此落在 13.281。這是**啟動階段的資源競用**，不是 protocol 層把 turn 串行化
（wire 上兩個 turn 同時 `inProgress` 已證明）。M3b 需注意：同一 app-server 上新開 thread 時，
MCP server 啟動可能拉長首輪延遲。

### (b) identity 歸屬證據

```
VERDICT_B notifications=107 with_threadId=102 with_turnId=82 missing_both=5 broadcast_fallback=false
  notif_missing_identity_methods=[account/rateLimits/updated remoteControl/status/changed]
```

- **107 筆 s2c notification 中 102 筆帶 `threadId`**；缺 identity 的 5 筆全部是
  `account/rateLimits/updated` 與 `remoteControl/status/changed`——**帳號／server 層級事件，本來就
  不屬於任何 thread**，不是歸屬缺口。
- `broadcast_fallback=false`：**所有 `turn/completed` 都能靠 `threadId` 精確歸屬**，probe 全程未曾
  退化到廣播。
- **approval request 實際收到 1 筆，帶完整 `threadId` + `turnId` + `itemId`**：

```
seq=46 14:18:02.422 s2c item/fileChange/requestApproval
  {"threadId":"01a000a2-e084-7531-8ca5-4ac8d71ab764",
   "turnId":"01a000a2-e242-7600-841e-19c87f560437",
   "itemId":"exec-efc72aa6-1d7b-4ae4-a5af-9e8c5bb3d4fa",
   "startedAtMs":1786717082416,"reason":null,"grantRoot":null}
seq=47 14:18:02.422 c2s {"id":0,"result":{"decision":"decline"}}
```

拒絕生效的獨立佐證：

- `probe-approval.txt` **未被建立**（driver 的 `os.Stat` 檢查通過，否則直接 `NO-GO` 退出）
- server stderr：`codex_core::tools::router: error=patch rejected by user`

**判定：(b) 通過。** notification 與 approval request 都不需靠抵達順序即可歸屬到 thread／turn。

### (c) natural 收尾

```
exit_code=0
SHUTDOWN mode=natural bounded=true elapsed=25ms record_err=<nil>
         last_frame_seq=124 last_frame_at=14:18:14.935
```

- `Terminate → Wait` 在 **25ms** 內返回（`TermGrace=5s` 未觸發 SIGKILL），exit code 0
- `StopRecording()` 回 `nil`（sink 無錯、in-flight callback 全數 drain）
- wire log 最後一筆（seq 124）就是 thread A 的 `turn/completed`，內含
  `"text":"PROBE_A_DONE"`、`"status":"completed"`，JSON 完整未截斷

---

## Step 4：Run 2 — `-force` 強制收尾

```
threads approval=01a000a3-b1e4-7362-ab4b-434c21cbd0a9
        A=01a000a3-d648-7b73-b801-e9ff2f9f84ac
        B=01a000a3-d6bb-7b12-a96b-c59b20f3b643
```

```
TURN label=APPROVAL status=completed  dur=9.206s
TURN label=A        status=server-died dur=2.032s
TURN label=B        status=server-died dur=2.032s
```

### (b)（forced run 亦成立）

```
VERDICT_B notifications=78 with_threadId=75 with_turnId=57 missing_both=3 broadcast_fallback=false
seq=42 14:18:48.652 s2c item/fileChange/requestApproval
  {"threadId":"01a000a3-b1e4-7362-ab4b-434c21cbd0a9",
   "turnId":"01a000a3-b24d-7ac0-8650-7882a597acd6",
   "itemId":"exec-d69671d9-fe37-43c2-8844-d74c0d120ba3","startedAtMs":1786717128652,…}
  → decision=decline，probe-approval.txt 未建立
```

缺 identity 的 3 筆同樣只有 `account/rateLimits/updated` / `remoteControl/status/changed`。

### (a)（forced run 供交叉佐證，正式判定以 natural 為準）

```
seq=86 14:18:52.578 c2s turn/start  threadId=…d6bb…643（B）
seq=87 14:18:52.578 c2s turn/start  threadId=…d648…4ac（A）
seq=91 14:18:52.627 s2c turn/started threadId=…d6bb…643 turn=…0c57f3d1bf56 status=inProgress
seq=93 14:18:52.637 s2c turn/started threadId=…d648…4ac turn=…0c6c21ce059d status=inProgress
```

兩個 turn 同時 `inProgress`，與 natural run 一致（`interleaved=true framesA=6 framesB=6`）。

### (c) forced 收尾

```
exit_code=0 stderr_tail="… codex_core::tools::router: error=patch rejected by user"
SHUTDOWN mode=forced bounded=true elapsed=0s record_err=<nil>
         last_frame_seq=95 last_frame_at=14:18:53.155
```

- 兩個 turn goroutine 在 `Terminate()` 後 **約 30ms** 內經 `srv.Done()` 觀察到 server 死亡並返回
  （`dur=2.032s` 減去 `2s` sleep；精度 ~ms），`wg.Wait()` 隨即返回——turn 進行中強制終止**不會
  hang 住 host**，且不需靠 `turnTimeout` 兜底。
- 收尾階段的第二次 `Terminate → Wait` 為 0s（程序已被前一次 Terminate 收割，`Exit` 已快取），
  exit code 0。
- 錄流在讀迴圈 EOF 前**全程掛載**，`StopRecording()` 於 `Wait()` 之後才 detach 且回 `nil`；
  wire log 最後一筆（seq 95, `mcpServer/startupStatus/updated`）即 server 死亡前送出的最後一筆
  frame，該行 JSON 完整。SIGTERM 之後 server 未再送出任何 frame。

---

## 逐項判定

| 項目 | natural | forced | 聚合判定 | 理由 |
|---|---|---|---|---|
| **(a)** 真並行 | **PASS** | PASS（交叉佐證） | **PASS**（以 natural 為準） | 兩個 `turn/start` 同一毫秒送出、兩者皆回 `inProgress`；A 的 turn 生命期完整包住 B 的整輪。無串行化、無第二 thread 被拒 |
| **(b)** identity 歸屬 | **PASS** | PASS | **PASS**（以 natural 為準） | 102/107 notification 帶 `threadId`，缺者僅 account／server 層事件；approval request 帶 `threadId`+`turnId`+`itemId`；`broadcast_fallback=false` |
| **(c)** bounded 收尾 | **PASS**（25ms、`StopRecording=nil`、末筆 = `turn/completed`） | **PASS**（Done ~30ms、`StopRecording=nil`、末筆完整） | **PASS**（兩次都通過，符合 (c) 需雙通過） | 兩種收尾都在 `TermGrace` 內收斂、exit 0、錄流無錯且未截斷 |

## 最終裁決：**GO**

單一 `codex app-server` 可承載多個並行 thread；notification 與 approval request 都帶
thread／turn identity，host 可據以做 per-session 路由；兩種收尾都 bounded。M3b 的架構前提成立。

## 殘留風險（不阻擋 GO，M3b 實作需注意）

1. **新 thread 的 MCP 啟動可能序列化**：natural run 中兩個 thread 的
   `mcpServer/startupStatus/updated` 相差 ~4.1s，慢的那個 thread 首輪延遲被拉長。多 session
   同時開新 thread 時，UI 需容忍首輪較長的「無回應」空窗，不可據此判定 session 卡死。
2. **`remoteControl/status/changed` 不在 `codex.ServerNotifications`**：目前落 `OnUnknown` →
   `KindUnknown`（設計內的 fallback，非錯誤），但它**不帶 `threadId`**，M3b 的 per-session 路由
   必須把「無 threadId 的 s2c 事件」歸為 server 級廣播，不能誤派給任一 session。
   `account/rateLimits/updated` 同理。
3. **turn id 前綴高度相似**：本次 run 中 A/B 的 turn id 為 `01a000a3-2069-7ca3-822a-7cb85de3e1fe`
   與 `01a000a3-2069-7ca3-822a-7ca58de01e80`（僅末段不同）。路由一律用完整字串比對，任何
   prefix/短碼比對都有碰撞風險。
4. **樣本數**：每種收尾各執行 1 次（brief 凍結的執行次數）。並行度只驗到 2 個並行 thread + 1 個
   先行 thread，未驗更高並行度或長時間持續負載。
5. **`completed-before-response` 未涵蓋**（凍結範圍外）：真 server 本次未自然產生此順序，
   由 Task 9 的 fake-wire 測試鎖住。
6. **無害的 server 端錯誤 log**：`codex_models_manager::cache: failed to load models cache:
   missing field 'base_instructions'`——本機 `~/.codex/models_cache.json` 與 0.146.1 的 schema
   不合，probe 全程仍正常，未影響任何判定。

## 驗證紀錄

```
go build ./...                    # OK
go vet ./...                      # OK
go test -race ./... -count=1       # 全部 ok（18 個 package，無 FAIL）
```
