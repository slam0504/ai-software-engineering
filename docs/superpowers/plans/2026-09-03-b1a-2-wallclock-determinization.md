# B1a-2 非 process 類牆鐘測試確定性化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev5（2026-09-03，owner design gate APPROVED；證據分層改記 golden hash 已由 owner 機械重建命中、Gate B 的 `<plan-commit>` 改為現場取得。模板、negative control 與執行範圍未動）
> 狀態：**design gate APPROVED（2026-09-03）**——Gate A 已完成（build／vet／gofmt／三包 `-race`／negative control 3/3／positive control 3b／PGID 殘留檢查），三個 golden SHA-256 已由 owner 從基準機械重建精確命中。**可進入 Gate B implementation**
> 票源：Pre-M4 Readiness Backlog **B1a-2**（`docs/architecture/pre-m4-readiness-backlog.md`，wall-clock 測試 #2／#3／#4；Appendix C bottom-up 估點 **1.01 pt**，範圍 0.81–1.21）
> 基準 commit：`a5a3cabafa6476ebf9400fd7d2819063b999ca30`（本 plan 撰寫時的 HEAD＝origin/main）

**Goal:** 讓三條牆鐘相依測試不再把「時間」當成隱性成功／失敗判準，同時**不改動任何 production 程式碼**：#2 移除不必要的 fixture 資料轉換並保留 15 秒 context；#3 把僅供卡死診斷的局部 deadline 由 5 秒放寬到 15 秒，對齊同 repo 既有先例；#4 把 root package 的 `afterFn` 接上既有 `newFakeAfter()`，讓 5 秒 quiesce 逾時不再由真實時鐘決定。

**Architecture:** 三條各有各的契約，**不共用修法**（owner 2026-09-03 裁示）。#2 的成功判準本來就是「必須 surface `stream_error`」，時間只是保險絲；#3 的成功判準是收到 `KindResult` 事件，時間同樣只是卡死診斷；#4 是唯一一條時間真的參與 production 決策（`appcore.CloseSequence` 的 quiesce 逾時會升級成 `terminate()`）的，因此改用既有可注入時鐘 seam，而不是調大常數。

**Tech Stack:** Go（stdlib only，無新依賴）；驗證慣例 `go build`／`go vet`／`go test -race -count=1`，沿用本 repo 既有慣例。

**參考文件：**
- `docs/architecture/pre-m4-readiness-backlog.md`（B1a-2 範圍、Appendix C 估點、B1a-4 承擔的整合負載矩陣邊界）
- `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7（mutation 鑑別表 N/N 執行規則、四步完成定義、panic／編譯失敗／`go test -timeout` 不算有效紅燈）、§6.8（同檔 production comment 禁止內嵌行號）
- `docs/superpowers/plans/2026-09-03-b1a-1-proc-terminate-seam.md`（同系列前一票；本 plan 沿用其 header／Global Constraints／mutation 鑑別表格式）
- `docs/spikes/m3b-results.md` §7（牆鐘相依測試權威名單）

---

## 三條各自的契約（preflight 已實測，本票不重新論證）

三條的失效機制不同，owner 已裁示不預設共用修法。以下為兩輪 read-only preflight（隔離 worktree，基準 `a5a3cab`，主工作區零 `.go` 異動）取得的事實：

### #2 `TestClaudeAssistFailsLoudOnOversizedLine`（`internal/assist/oneshot_test.go:60`）

原先懷疑的「假通過」（ctx 逾時 → pipe read 錯誤 → `KindStreamError` → `sawStreamErr` 誤真）**不成立**，有兩個互相獨立的結構性理由：

1. `internal/proc` 的 stdout 是父程序**自建的 `os.Pipe()`**（非 `cmd.StdoutPipe()`），`Terminate()` 只送 group 訊號、從不主動關讀端。子孫程序死亡後所有 write end 關閉，讀端得到的是**乾淨 EOF**，不是 read error——`sc.Err()` 為 nil。`internal/proc/proc.go` 既有註解（符號 `Start` 內建立 pipe 處）已寫明這個設計意圖。
2. **更決定性**：`oneshot.go` 的 `Run` 在 `case <-ctx.Done():` 分支只把 `events` **排乾丟棄**（`for range events {}`），不再呼叫 `sink`。即使 scanner goroutine 在取消後真的發出 `KindStreamError`，它也永遠到不了設定 `sawStreamErr` 的那個 callback。

三種時序（正常路徑／小行→sleep→ctx 1s 命中 sleep 中段／20 個 850KB chunk 拼成單一大行、ctx 900ms 落在約 7.65MB 處）各跑三次，後兩種全部 `sawStreamErr=false` 並如實變紅。**唯一剩下的風險方向是「環境慢 → context 先到 → 誤紅」**，與原假設相反。

fixture 生成成本實測 1.5–1.7 秒，context 預算 15 秒，**餘裕約 9 倍**。移除 `tr` 後單條測試實測由約 2.38 秒降至約 1.38 秒。

### #3 `TestMultiTurnSendAndTurnBoundaries`（`internal/claude/session_test.go:31`）

`waitResult` 的 `deadline := time.After(5 * time.Second)` 是**卡死診斷**用的保險絲，不是成功判準——成功判準是收到 `contract.KindResult` 事件。240 筆耗時量測：

| 情境 | 樣本 | min／median | max |
|---|---|---|---|
| 單獨 `-count=20` | 60 | 0ms／0ms | **383ms** |
| `./internal/...` 並行負載下 ×3 輪 | 180 | 0ms／0ms | **29ms** |

無一次逾時、無一次 FAIL。**本輪 240 筆並未證明 5 秒不足**。

### #4 `TestInFlightTurnDoesNotBlockNewSession`（`app_invariants_test.go:308`）

`a.NewSession(w)` 會走 `claudeTeardown` → `appcore.CloseSequence(host.sess.Close, host.pumpDone, 5*time.Second, 10*time.Second, …)`（`app.go:7574`）。5 秒 quiesce 逾時是**唯一一條會真的參與 production 決策**的牆鐘：逾時就升級成 `terminate()`。

根因已由兩段診斷 mutation 證實：未接線 ＋ fake CLI 注入 `sleep 6` → 在 **5.01 秒**由 `appcore: pump quiesce timeout` 轉紅；接線 ＋ 同樣延遲 → PASS。未注入延遲時的真實成本為 **0.33–0.35 秒**（先前回報的 12.57 秒是診斷 mutation 尚未移除時量到的，不是真實成本）。

---

## Production 零變更聲明

本票**不新增、不修改任何 production 程式碼**。三條全部只動測試檔與（僅在 mutation 期間、事後還原）測試 fixture：

| 檔案 | 性質 | 本票動作 |
|---|---|---|
| `internal/assist/oneshot_test.go` | 測試 | 修改（Task 1） |
| `internal/claude/session_test.go` | 測試 | 修改（Task 2） |
| `app_invariants_test.go` | 測試（root package） | 修改（Task 3） |
| `testdata/fake-claude.sh` | 測試 fixture | **僅 negative control 3a 與 positive control 3b 期間暫時修改，事後 byte-identical 還原**（Task 4） |

`internal/assist/oneshot.go`、`internal/claude/session.go`、`internal/appcore/pump.go`、`app.go` 一律零變更。**每個 implementation commit 前必須以 `git diff --name-only HEAD` 核對工作區**，出現任何非測試檔即停工回報，不得自行新增 production seam（owner 已把此列為硬性停止點）。這是 per-commit 的工作區檢查，與 Gate B 的兩條 range diff 是不同的兩層，兩層都要做。

---

## Global Constraints

- Module：`github.com/slam0504/sdlc-workbench`。
- **驗證分層（rev3 校正）**：**受影響 package 為三個：`internal/assist`、`internal/claude`、root（`.`）**。
  - **Task 1／Task 2 是 package-scoped**：只跑 `go build`／`go vet`／`gofmt -l`／`go test -race -count=1` 於該 task 動到的那一包，**不在 task 內跑 `go build ./...`／`go vet ./...`**。
  - **Task 3 動到 root package**：跑 root 的 focused 與全量 `-race`（423 條，成本高，只跑這一次）。
  - **完整 `go build ./...`／`go vet ./...`／`gofmt -l .` 由 Gate A 統一執行一次**，不由各 task 重複。
  - **所有 Gate A 驗證（含 Task 1–3 的 package-scoped 指令）一律在隔離 worktree 內執行**，主工作區全程零異動。
- **Task 1–3 各自結尾的「然後 commit」屬於 Gate B**（主 repo 的 implementation 階段）。**Gate A 期間只在隔離 worktree 內套用模板與跑驗證，不 commit**——worktree 的目的是取得 golden hash 與 negative control 證據，不是產生 commit。
- **本票只做 focused 與上述三包回歸；B1a-4 的五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）本票不做、也不得宣稱完成。** 注意該矩陣**包含 root／`internal/assist`／`internal/claude`**——B1a-4 會在整合 HEAD 對這三包再跑一次，**不以 B1a-2 的 focused 結果取代**。
- 無新外部依賴；一律 stdlib。
- **落地驗證環境：隔離 worktree ＋ 從主 repo 複製 `frontend/dist`**（owner 2026-09-03 定案，**不採主工作區 `git stash`、也不用單一佔位檔**）。理由：root package 的 `main.go` 有 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore:18` 忽略、零追蹤檔——`git worktree add` 不會帶過去，因此新 worktree 內 `go build ./...` 必然失敗於 `pattern all:frontend/dist: no matching files found`（B1a-1 已於主 repo 遮蔽 `frontend/dist` 重現確認：失敗只波及 root package）。**這對本票影響比 B1a-1 大**——#4 在 root package，worktree 內連 `go test .` 都無法編譯。主 repo 現有可用的 ignored build artifacts，落地驗證第一步即 `cp -R <主 repo>/frontend/dist <worktree>/frontend/`，之後 root build／vet／focused test／全套測試都在**同一個 worktree** 內完成，主工作區全程零異動。複製來源與複製後的 `go build ./...` exit code 須記入證據段落。
- **production 零變更是硬性邊界**（見上方聲明）。無法只靠測試側 fixture 接線就達成的，停下回報，不得自行新增 production seam，也不得默默改動估點前提。
- **時間常數不得成為成功判準**：三條修改後，時間仍只扮演「卡死時終於失敗」的角色。任何把「跑得夠快」寫成通過條件的斷言一律不加。
- **`go test -timeout` 撞牆不是有效紅燈**：Task 4 的每一項 negative control 都必須紅在測試自身的 `t.Fatal`／`t.Fatalf`，逐字抄錄失敗訊息為證（本票依 owner 核准的 B1a-2 專屬例外執行，措辭見 Task 4 開頭）。
- **fixture mutation 必須還原成 byte-identical**：`testdata/fake-claude.sh` 是被追蹤檔案，mutation 前後以 `shasum -a 256` 比對。

---

## Task 1: `internal/assist` — #2 移除 `tr`，保留 15 秒 context

- [ ] **Step 1: 修改 fixture 腳本一行**

`internal/assist/oneshot_test.go` 的 `TestClaudeAssistFailsLoudOnOversizedLine` 內：

```go
	// 讀掉 prompt 行後吐一條 >16MB 的行（超過 scanner 上限）。
	//
	// 刻意直接吐 raw NUL byte、不再 `| tr '\0' 'a'`：Scanner 的 token 上限只看
	// **byte 長度**，與 byte 內容無關，17MB 的 NUL 單行撞的是同一個 ErrTooLong，
	// 契約不變。省掉的 tr 是一個額外程序與一次全量資料轉換——本條實測成本因此
	// 由約 2.38s 降到約 1.38s。
	script := "#!/bin/sh\nread -r _line\nhead -c 17000000 /dev/zero\necho\n"
```

其餘一律不動：**15 秒 context 保留原值**（`context.WithTimeout(context.Background(), 15*time.Second)`），斷言不動，`sawStreamErr` 判定不動。

- [ ] **Step 2: 補上餘裕與風險方向的誠實註記**

在既有的函式上方註解之後、`script` 之前補一段（不覆蓋既有註解）：

```go
	// 15s context 是卡死保險，不是成功判準（成功判準是 sawStreamErr）。preflight
	// 實測 fixture 生成 1.5–1.7s，餘裕約 9 倍。本輪未重現過任何誤紅；已知的唯一
	// 風險方向是「環境慢 → context 先到 → 誤紅」，不存在假通過路徑：ctx 取消後
	// oneshot.Run 只排乾 events、不再呼叫 sink，取消後產生的事件改不動 sawStreamErr。
```

**措辭紀律**：註解與後續回報一律只寫「已量得約 9 倍餘裕、並降低已知 fixture 成本」，**不得宣稱本輪重現過誤紅**（owner 明示）。

- [ ] **Step 3: 驗證**

`go build ./internal/assist/...`、`go vet ./internal/assist/...`、`gofmt -l internal/assist`、`go test -race ./internal/assist/... -count=1 -v`。基準：`internal/assist` 現有 **20 條頂層測試**，本票不增減條數，預期 20 條全 PASS。同時記錄 `TestClaudeAssistFailsLoudOnOversizedLine` 的實際耗時（預期約 1.4s，對照修改前約 2.4s）。

- [ ] **Step 4: `git diff --name-only` 確認只有 `internal/assist/oneshot_test.go`**，然後 commit。

---

## Task 2: `internal/claude` — #3 局部 deadline 5s → 15s

- [ ] **Step 1: 註冊 failure-safe cleanup（先做，`waitResult` 的失敗路徑要靠它）**

現行 `TestMultiTurnSendAndTurnBoundaries` 的程序回收**全部集中在函式尾端**（`s.Close()` → `for range events` → `s.Wait()`），`Start` 成功後沒有任何 `defer`／`t.Cleanup`。`waitResult` 的 `t.Fatal` 一旦觸發（mutation 3a 就是刻意製造這條路徑），尾端那三行完全不會執行，刻意製造的失敗會留下 fake CLI 程序干擾同套件後續測試與連帶清單判讀。

在 `Start` 的錯誤檢查之後、`var results int` 之前插入：

```go
	// failure-safe 回收：waitResult 的 deadline t.Fatal 會跳過函式尾端的
	// Close／drain／Wait，殘存的 fake CLI 會污染同套件後續測試（mutation 3a 就是
	// 刻意走這條路）。Terminate 在退出已記錄時是 no-op（proc.Terminate 的
	// `if p.exited` 守衛，避免對已回收、可能被重用的 pgid 再送訊號），Wait 回傳
	// supervisor 快取值、可重複呼叫——因此正常路徑既有的 graceful close 不受影響。
	t.Cleanup(func() {
		_ = s.Terminate()
		s.Wait()
	})
```

正常路徑的 `s.Close()` → `for range events` → `s.Wait()` 全部保留，不改。

- [ ] **Step 2: 改常數與訊息**

`internal/claude/session_test.go` 的 `TestMultiTurnSendAndTurnBoundaries` 內 `waitResult`：

```go
	waitResult := func() {
		// 15s 只是卡死診斷的保險絲，成功判準是收到 KindResult 事件，不是「跑得夠快」。
		// 沿用 app_test.go 的 waitFor 先例（同一種 fake CLI spawn 壓力下，5s 在
		// -race 全套並行時實測會偶發逾時）。維持局部 deadline、不退回只靠
		// `go test -timeout`：局部失敗能指出「卡在第幾輪的哪個等待」，package
		// timeout 只會丟出整包 goroutine dump。
		deadline := time.After(15 * time.Second)
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
				t.Fatal("no result within 15s")
			}
		}
	}
```

失敗訊息必須同步改成 `no result within 15s`（owner 明示）。其餘不動。

- [ ] **Step 3: 在測試上方補「證據與缺口」註記**

```go
// 牆鐘相依處置（backlog B1a-2）：waitResult 的局部 deadline 由 5s 放寬到 15s。
// preflight 240 筆量測（單獨 -count=20 共 60 筆：max 383ms；./internal/... 並行
// 負載下 ×3 輪共 180 筆：max 29ms）**沒有**證明 5s 不足，也沒有觀察到任何逾時。
// 383ms 是本輪唯一觀察到的冷啟動樣本，**CI runner 仍未驗證**——這個缺口留給
// B1a-4 的整合負載矩陣，本票不宣稱已消除。
```

**措辭紀律**：不得寫成「已證實 5 秒不足」，也不得寫成「已消除 CI 風險」。

- [ ] **Step 4: 驗證**

`go build ./internal/claude/...`、`go vet ./internal/claude/...`、`gofmt -l internal/claude`、`go test -race ./internal/claude/... -count=1 -v`。基準：`internal/claude` 現有 **22 條頂層測試**，本票不增減條數，預期 22 條全 PASS。

- [ ] **Step 5: `git diff --name-only` 確認只有 `internal/claude/session_test.go`**，然後 commit。

---

## Task 3: root package — #4 接上 `afterFn`／`newFakeAfter()`

- [ ] **Step 1: 兩行 test-only 接線**

`app_invariants_test.go` 的 `TestInFlightTurnDoesNotBlockNewSession`，在 `a, _ := newTestApp(t)` 之後、`a.wsReg = &stubRegistry{}` 之前插入：

```go
	// 受控時鐘：NewSession 會走 claudeTeardown → appcore.CloseSequence 的 5s
	// quiesce 逾時，那是本測試裡唯一會參與 production 決策的牆鐘。改注入
	// fakeAfter 之後，逾時分支不再由真實時鐘決定——本測試驗的是「卡住的 turn
	// 不擋新對話」，不是「pump 能在 5 秒內收乾淨」。
	// 先例：app_shutdown_multi_test.go:353-354、app_lease_boundary_test.go:168-169。
	timers := newFakeAfter()
	a.afterFn = timers.After
```

`newFakeAfter()` 定義於 `app_shutdown_multi_test.go:150`（同 package，可直接用）。**不新增任何 production seam**——`afterFn` 是既有欄位（`app.go:503`），`a.after()`（`app.go:6392`）是既有存取器。

- [ ] **Step 2: 驗證邊界（必寫進註解，不得省略）**

接在 Step 1 註解之後：

```go
	// 驗證邊界：fakeAfter 的 timer 除非測試自己 fireAll 否則永不觸發，因此真正的
	// pump 卡死不再有 5 秒的局部快速失敗，會一路落到 go test -timeout 才收場。
	// 這是本票接受的取捨（owner 2026-09-03 裁定），**不宣稱卡死診斷延遲已消除**。
```

- [ ] **Step 3: 接線鑑別斷言（owner 2026-09-03 裁定採用；不是選配）**

在 `a.NewSession(string(w))` 的錯誤檢查之後插入：

```go
	// 接線鑑別：CloseSequence 一定會呼叫 WaitQuiesce(done, 5s, after)，而 Go 的
	// select 在挑分支前會先求值每個 case 的 channel 運算元——因此 after(5s) 必被
	// 呼叫一次，與 done 是否已關無關。這一行是「seam 真的被接上」的確定性證據：
	// 拿掉上面的 a.afterFn = timers.After，這裡立刻紅，不需要慢 fixture。
	if n := timers.totalCreated(); n == 0 {
		t.Fatal("afterFn seam 未被接上：CloseSequence 應至少向注入時鐘要過一次 timer")
	}
```

`totalCreated()` 定義於 `app_shutdown_multi_test.go:207`，`After()`（`:156`）每次呼叫都在鎖內遞增 `created`，讀寫皆有鎖保護。

owner 裁定要點：**這不算擴大 production scope**——它是那兩行接線的驗收斷言，`WaitQuiesce` 進入 `select` 時一定先求值 `after(timeout)`，`totalCreated()` 已有鎖保護。**保留 `n == 0` 的判斷形式即可**（不改成其他門檻）。採用本斷言後，mutation 4a **不需要** `_ = timers` 中和子——`timers` 仍被這一行使用，不會觸發 `declared and not used`。

- [ ] **Step 4: 驗證**

**在隔離 worktree 內**（已複製 `frontend/dist`，見 Global Constraints）：`go test -race . -run 'TestInFlightTurnDoesNotBlockNewSession' -count=1 -v`，再跑 root package 全量 `go test -race . -count=1`。基準：root package 現有 **423 條頂層測試**（39 個 `*_test.go`），本票不增減條數。同時記錄本條實際耗時（預期約 0.33–0.35s）。

完整 `go build ./...`／`go vet ./...`／`gofmt -l .` **不在本 task 跑**，由 Gate A 統一執行一次。

- [ ] **Step 5: `git diff --name-only` 確認只有 `app_invariants_test.go`**，然後 commit。

---

## Task 4: Negative-control 鑑別表 ＋ 回歸 ＋ 兩道 gate

### §6.7 的 B1a-2 專屬例外（owner 2026-09-03 核准，必讀）

治理文件 `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7 的原文要求：變異必須植入**本票修改且承載契約的 production code**。**本票無法符合這條原文**——B1a-2 的既定邊界就是 production 零變更，沒有任何 production code 可植入。

owner 因此核准一個**僅限 B1a-2** 的例外：以**測試側 negative control** 代替 production mutation，但 **N/N 全跑、hash 前後比對、byte-identical 還原、完整回歸強度**四項要求一併保留，不打折。

**措辭紀律**：本票的驗收證據**不得**寫成「符合 §6.7 的 production-target 規則」，只能寫成「依 owner 核准的 B1a-2 專屬例外，以測試側 negative control 執行，強度要求未降低」。後續 rev、commit message、backlog 狀態更新一律照此措辭。

### 分母：3/3（2a、3a、4a）

**三項全部必須由指定斷言轉為失敗**，缺一即本票未通過。**3b 不在分母內**——它是 3a 的 **positive control**（見下），不是變異。

每一項變異都必須完成以下四步並留下證據，缺任一步即該項未通過：

1. **套用**：被改檔案的 `shasum -a 256` 在套用前後不同，確認變異真的落到檔案上。
2. **轉紅**：表中指定的測試確實 FAIL，且**紅在正題**——失敗訊息是該測試自己的 `t.Fatal`／`t.Fatalf`，不是撞到 `go test -timeout`、panic 或編譯失敗。
3. **還原**：還原後檔案 hash 與變異前 **byte-identical**。
4. **轉綠**：還原後，指定測試（focused）＋該變異的**連帶範圍**全數回綠。

**連帶範圍**：每項在下表指明。fixture 變異（3a）的連帶已由 grep 界定——`testdata/fake-claude.sh` 全 repo 只被 `internal/claude/session_test.go` 引用，`FAKE_MULTI` 只被 `TestMultiTurnSendAndTurnBoundaries`（:32）與 `TestSendAfterCloseErrors`（:81）使用——但**實際連帶清單仍須實跑取得**，不得只靠 grep 推定。

- [ ] **Step 1: negative control 3/3 全部執行（不抽驗）；design gate 階段即實跑，不延後到 B1a-4**

| # | 變異（植入處） | 必須轉紅的斷言（測試自身的 `t.Fatal`） | 對應測試 | 連帶範圍（須實跑記錄） |
|---|---|---|---|---|
| 2a | `internal/assist/oneshot_test.go`：把 `head -c 17000000` 改為 `head -c 1000000`（低於 16MiB＝16,777,216，不再觸發 `bufio.Scanner` 的 `ErrTooLong`） | `oversized line must surface a stream_error (fail loud); run err=%v` | `TestClaudeAssistFailsLoudOnOversizedLine` | `internal/assist` |
| 3a | `testdata/fake-claude.sh`：在 `FAKE_MULTI` 迴圈內、`printf` 出 `result` 那行**之前**插入 `sleep 6`；**同時**把 `session_test.go` 的 deadline 改回 `5 * time.Second`／訊息改回 `no result within 5s` | `no result within 5s`（測試自身，實測應約 5.0s） | `TestMultiTurnSendAndTurnBoundaries` | `internal/claude` |
| 4a | `app_invariants_test.go`：移除 `a.afterFn = timers.After` 那一行（**保留** `timers := newFakeAfter()`；因 Task 3 Step 3 的斷言仍使用 `timers`，**不需要** `_ = timers` 中和子） | `afterFn seam 未被接上：CloseSequence 應至少向注入時鐘要過一次 timer` | `TestInFlightTurnDoesNotBlockNewSession` | root package |

**2a 證明的是什麼（措辭更正）**：2a 證明**新版 fixture（已移除 `tr`）仍然正確跨越 Scanner 的 token 上限**——門檻降到 16MiB 以下就不再觸發 `ErrTooLong`，測試如實轉紅。它**不是**在證明「移除 `tr` 提高了鑑別力」；移除 `tr` 是成本優化，鑑別力不變（token byte length 相同）。降到 1MB 的 NUL 單行會被 Scanner 正常讀完、再由 `claude.Decode` 判為 `KindMalformed`（`internal/claude/decode.go:36-39`），**不會**意外產生 `KindStreamError`，因此不存在假綠路徑。

**明確排除、不列入本表**：把 `internal/assist/oneshot_test.go` 的 `| tr '\0' 'a'` 加回去。owner 明示這**不是**必須打紅的語意變異——`tr` 的有無不改變 Scanner 契約，加回去只會讓測試變慢、不會變紅，列進來是假鑑別。

- [ ] **Step 2: 3a 的 positive control（3b）——必跑，但不計入分母**

| 項目 | 內容 |
|---|---|
| 設定 | 套用 3a 的 **fixture 延遲部分**（`sleep 6`），但 `session_test.go` **保持本票的 15s 版本**（不改回 5s） |
| 預期 | **PASS**（每輪等待約 6s < 15s；三輪總耗時約 18–19s） |
| 證明什麼 | 3a 的紅燈確實來自「5 秒常數不夠」，而不是 fixture 延遲本身把測試弄壞。**單獨跑 3a 證不出常數變更有意義**，必須成對 |
| 記錄 | 實際耗時、PASS 結果，與 3a 的失敗訊息並列 |

`go test -timeout` 撞牆在 3a／4a／2a 任何一項都**不算**有效紅燈；3b 若因 package timeout 而失敗，代表環境異常，須重跑而非記為證據。

- [ ] **Step 3: 3a 的程序殘留檢查——以 PGID 精確比對（失敗路徑專屬）**

3a 是本票唯一刻意走測試失敗路徑的項目，必須另外證明沒有殘留 fake CLI 程序。Task 2 Step 1 的 `t.Cleanup` 就是為此存在。

**為什麼不用字串計數**：先落檔再 grep 只解決了「grep 程序尚未啟動」這一半。外層 `zsh -lc` 之類的 wrapper 命令列仍可能帶著後續要比對的字串而被快照收進去；而且全機器搜尋 `sleep 6` 會比中與本測試無關的其他工作。**全機器的 `fake-claude.sh`／`sleep 6` 字串計數一律不作為主要證據**（B1a-1 已實際踩過自我比中誤報 1 的坑）。

**指定方式——以本次測試的 process group 精確比對**：

1. 套用 3a 時，於 `TestMultiTurnSendAndTurnBoundaries` 內 `Start` 成功後**暫時**加一行記錄 PGID（`Session.PGID()` 定義於 `internal/claude/session.go:122`）：

   ```go
   t.Logf("B1A2-PGID=%d", s.PGID())
   ```

   跑 3a 時用 `-v` 取得該值（`t.Logf` 在 FAIL 的測試一定會輸出）。

2. **另一次獨立的工具呼叫**產生含 PGID 欄的程序快照（與步驟 3 分開，不寫在同一條命令列）：

   ```sh
   ps -eo pgid=,pid=,ppid=,args= > /tmp/b1a2-ps-after-3a.txt
   ```

3. 以**第一欄 PGID 精確比對**該數值，不做任何字串比對：

   ```sh
   awk -v g=<步驟 1 取得的 PGID> '$1 == g' /tmp/b1a2-ps-after-3a.txt
   ```

   **預期輸出為空**——該 process group 已無任何存活程序。比對用的 `awk` 自身屬於當前 shell 的 process group，與待測 PGID 不同，結構上不可能自我比中。

4. 移除步驟 1 的臨時 `t.Logf`，並確認 `internal/claude/session_test.go` 的 `shasum -a 256` **回到 negative control 套用前的值**（即 Gate A 記錄的 golden hash）——臨時記錄與 3a 的常數變異兩者都必須完全還原。

快照檔、`awk` 的空輸出、還原後的 hash 三者一併留為證據。

- [ ] **Step 4: 既有回歸清單——零回歸**

| package | 頂層測試條數（基準 `a5a3cab`） | 本票預期 |
|---|---|---|
| `internal/assist` | 20 | 20 PASS，條數不變 |
| `internal/claude` | 22 | 22 PASS，條數不變 |
| root（`.`） | 423 | **422 PASS ＋ 1 SKIP**（總數 423，條數不變）。SKIP 為既有 env-gated `TestRegenerateCorpusVerdicts`（`domainspec_oracle_freshness_test.go:896-898`，`UPDATE_CORPUS != "1"` 即 skip，註解明寫 CI 不跑），與本票無關 |

條數以 `go test -list '^Test'` 實際編譯列出為準（owner 已於 rev1 審查時以此法核對三包數字相符）。其中須特別確認未被波及：`internal/claude/TestSendAfterCloseErrors`（同樣使用 `FAKE_MULTI`，是 fixture 變異的主要連帶候選）、`internal/claude/TestSingleTurnBehaviorUnchanged`、root 的 `TestSecondInFlightTurnRejected`（與 #4 同檔、共用 `writeSilentClaude`，`app_invariants_test.go:263`）。

- [ ] **Step 5: 記錄每項變異的實際連帶清單**（不得只靠 grep 推測），寫入下方「Design-gate 證據」段落。

---

### Gate A：design-gate 落地驗證完成條件（在隔離 worktree 執行）

全部在 `~/scratch-worktrees/b1a2-<n>` 內完成，主工作區零異動：

- [ ] worktree 建立後第一步：`cp -R <主 repo>/frontend/dist <worktree>/frontend/`，並記錄 `go build ./...` exit code。
- [ ] 三個測試檔按 Task 1／2／3 的模板逐字套用，`gofmt -l` 乾淨。
- [ ] **統一執行一次**（不由各 task 重複）：`go build ./...`、`go vet ./...`、`gofmt -l .` 乾淨；三包 `go test -race -count=1 -v` 全綠，條數與上表相符。
- [ ] negative control **3/3** 四步齊備，逐字抄錄失敗訊息與耗時。
- [ ] positive control **3b** 已跑且 PASS，耗時已記錄。
- [ ] 3a 的程序殘留檢查：`awk` 的 PGID 精確比對輸出為空，快照檔留存；臨時 `t.Logf` 已移除且 `session_test.go` hash 回到 golden 值。
- [ ] `testdata/fake-claude.sh` 還原後 `shasum -a 256` 與基準 byte-identical。
- [ ] **記錄三個最終測試檔的 golden SHA-256**（`internal/assist/oneshot_test.go`、`internal/claude/session_test.go`、`app_invariants_test.go`），寫入證據段落。
- [ ] worktree 移除、`git worktree list` 只剩主 repo。

### 證據轉移契約（Gate A → Gate B）

Gate A 在隔離 worktree 執行，正式 implementation 稍後才落到 main。**沒有 byte-level 綁定就不能證明兩邊是同一套測試與變異定義**，因此 Gate A 的證據只有在以下**四項全部成立**時才可轉移到 main 的 implementation：

1. **三個測試檔 hash 相符**：main 上 implementation commit 後的三個檔案 SHA-256，與 Gate A 記錄的 golden hash 逐一相同。
2. **變異內容與預期失敗訊息不變**：本 plan 的 negative control 表格（2a／3a／4a 的植入內容與必須轉紅的斷言字串）自 Gate A 後未被修改。
3. **驗收斷言模板不變**：Task 1／2／3 的程式模板（含 `t.Cleanup` 與 `totalCreated()` 斷言）自 Gate A 後未被修改。
4. **執行套件與測試範圍沒有擴張**：仍為 `internal/assist`、`internal/claude`、root 三包，未增減。

**任一項不符即整套重跑，不做部分轉移。** 此契約與 B1a-1 採用的 golden-hash 轉移規則同源，但本票的比對對象是**三個測試檔**（B1a-1 是單一 production 檔）。

### Gate B：implementation 完成 gate（在主 repo 執行）

- [ ] **三個 implementation commits** 已落在主 repo（plan commit 不計入這三個）。
- [ ] 三個測試檔 hash 與 Gate A golden hash 逐一相符（證據轉移契約第 1 項）；契約第 2–4 項逐項確認並記錄。
- [ ] `go build ./...`、`go vet ./...`、`go test -race ./internal/assist/... ./internal/claude/... . -count=1` 全綠。
- [ ] `gofmt -l` 與 `git diff --check` 乾淨。
- [ ] **範圍檢查拆成兩個可重現的 range diff**（plan 會先 commit、implementation commits 在後，因此單一「對 `a5a3cab` 只含三個測試檔」必然失敗）：
  - `git diff --name-only <plan-commit>..HEAD` → **只含三個測試檔**。
  - `git diff --name-only a5a3cab..HEAD` → **只含本 plan 文件 ＋ 三個測試檔**。
  - 兩條指令的完整輸出都要留為證據。
  - **`<plan-commit>` 於 Gate B 執行時現場取得，不回寫進已提交的 plan**——plan 無法在同一個 commit 內記錄自己的最終 SHA，事後回填又會讓 `a5a3cab..HEAD` 的 range diff 多出一次 plan 異動。取得方式：

    ```sh
    git log --reverse --format=%H a5a3cab..HEAD -- \
      docs/superpowers/plans/2026-09-03-b1a-2-wallclock-determinization.md |
      head -1
    ```

    （首次加入本 plan 的 commit。）取得的 SHA 只留在 Gate B 證據裡。
- [ ] **五套件整合負載矩陣明確不在本票範圍，屬 B1a-4，不得宣稱完成。**

---

## Design-gate 證據

### Gate A 執行結果（2026-09-03，隔離 worktree `~/scratch-worktrees/b1a2-gateA`，基準 `a5a3cab`）——已完成

rev3 模板逐字套用，**零設計偏離**；`gofmt -l .` 空輸出，未觸發任何機械格式調整。

**環境**：`cp -R <主 repo>/frontend/dist <worktree>/frontend/` 成功，複製後 `go build ./...` **exit 0**（若無此步，root package 會因 `//go:embed all:frontend/dist` 無法編譯）。

**基礎驗證（Gate A 統一執行一次）**

| 指令 | 結果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `gofmt -l .` | 空輸出 |
| `go test -race ./internal/assist/... -count=1 -v` | **20 條頂層 PASS**，5.306s |
| `go test -race ./internal/claude/... -count=1 -v` | **22 條頂層 PASS**，2.822s |
| `go test -race . -count=1 -v` | **422 PASS ＋ 1 SKIP＝423**，0 FAIL，196.632s |

三包條數與基準相符。三條受測測試的實測耗時：`TestClaudeAssistFailsLoudOnOversizedLine` **0.61s**、`TestMultiTurnSendAndTurnBoundaries` 0.37s、`TestInFlightTurnDoesNotBlockNewSession` **0.32s**。

**Golden SHA-256（Task 1／2／3 套用後、任何 negative control 之前）**

```
internal/assist/oneshot_test.go   5c00cb0d1d0f7651bc5f2c74eee6be722e965524187348f4672792f87e157796
internal/claude/session_test.go   dd3f40eab041695c7fe3a8d4332e2ba30d4363383317b11bec6026907e048d12
app_invariants_test.go            5f31cfdefba9dbf9620bc7c2f60d6ca0051a03424d53cfa75233810ffeaa514b
```

**Negative control 3/3，每項四步齊備**

| # | 套用（hash 改變） | 轉紅（逐字抄錄） | 還原 byte-identical | 轉綠（連帶範圍，實跑） |
|---|---|---|---|---|
| 2a | `c7409bf5625e9dce5d734f9387a271b1e6173b4ab0829cda8ee04bf5de07ad63` | `oneshot_test.go:87: oversized line must surface a stream_error (fail loud); run err=<nil>`；`--- FAIL: TestClaudeAssistFailsLoudOnOversizedLine (0.47s)`，exit 1 | 回 `5c00cb0d…` | `internal/assist` **20 PASS / 0 FAIL** |
| 3a | `fake-claude.sh` → `ea4b338f…`；`session_test.go` → `de7958f6…` | `session_test.go:73: no result within 5s`；`--- FAIL: TestMultiTurnSendAndTurnBoundaries (5.01s)`，exit 1 | `fake-claude.sh` 回 `be7a1026…`；`session_test.go` 回 `dd3f40ea…` | `internal/claude` **22 PASS / 0 FAIL**（含 `TestSendAfterCloseErrors` PASS 0.01s） |
| 4a | `4a70ba26a10f0f872a5cae598f7711c42e181c04cdfb4a9d51ea7fb5919140ea`（build 仍成功——`timers` 仍被 `totalCreated()` 使用，無 `declared and not used`，證實 rev2 移除 `_ = timers` 的判斷正確） | `app_invariants_test.go:337: afterFn seam 未被接上：CloseSequence 應至少向注入時鐘要過一次 timer`；`--- FAIL: TestInFlightTurnDoesNotBlockNewSession (0.31s)`，exit 1 | 回 `5f31cfde…` | root **422 PASS ＋ 1 SKIP，0 FAIL**（含 `TestSecondInFlightTurnRejected` PASS 0.31s） |

三項紅燈全部是測試自身的 `t.Fatal`／`t.Fatalf`，無一撞到 `go test -timeout`、panic 或編譯失敗。

**Positive control 3b**：`fake-claude.sh` 保留 `sleep 6`、`session_test.go` 維持 15s golden → **PASS，18.05s**（預期 18–19s，相符）。3a／3b 成對成立：同一個 6 秒延遲下，5 秒版本紅、15 秒版本綠，證明常數變更確實改變鑑別力。

**3a 的 PGID 程序殘留檢查**：暫時 `t.Logf` 取得 `session_test.go:44: B1A2-PGID=47425`（同輪 FAIL at 5.01s，訊息不變、行號因多一行 log 位移 +1）→ 獨立呼叫產生 `ps -eo pgid=,pid=,ppid=,args= > /tmp/b1a2-ps-after-3a.txt`（784 行）→ 獨立呼叫 `awk -v g=47425 '$1 == g' /tmp/b1a2-ps-after-3a.txt` → **空輸出**。移除臨時 `t.Logf` 後 `session_test.go` hash 回到 golden `dd3f40ea…`。Task 2 Step 1 的 `t.Cleanup` 在失敗路徑上確實生效。

**fixture 最終還原**：`testdata/fake-claude.sh` = `be7a1026b8656d7a8e1debfb329827c11fdc11e1525c0baea1d447c69267f70e`，與基準 byte-identical。

**收尾**：worktree 已 `--force` 移除、`git worktree list` 只剩主 repo、`~/scratch-worktrees/` 已空；主 repo `git rev-parse HEAD` 仍為 `a5a3cab`、`git status --porcelain` 只有本 plan 一行未追蹤。

### Gate A 的證據強度分層（誠實標註）

依 completion-gate 規則，執行者自述不得單獨作為關票依據。本輪的分層如下：

**(a) 由 reviewer 獨立機械複驗**：

- **三個 golden SHA-256 已由 owner 從基準 `a5a3cab` 建立全新 detached worktree、逐字套用 Task 1–3 模板並執行 `gofmt` 後重建，三個 hash 全部精確命中**（2026-09-03，臨時 review worktree 已移除）。因此 `dd3f40ea…` 對應的就是正確的 Task 2 模板內容，**不含 `t.Logf`、也不含執行過程中曾短暫誤植的 `if false { t.Fatal(err) }`**。這項已由基準機械重建確認，**不再依賴執行者對時間順序的回憶**。
- 主 repo HEAD＝`a5a3cab`、工作區只有 plan 一行、`git worktree list` 只剩主 repo、`~/scratch-worktrees/` 已空；`go test -list '^Test' .` ＝ **423**；`testdata/fake-claude.sh` 實測 hash 與上表還原值**逐字相同**；SKIP 來源已讀原始碼確認為既有 env gate。

**(b) 僅由執行者回報、reviewer 未獨立重跑**：三包 `-race` 的條數與耗時、negative control 三項的失敗訊息與 hash、positive control 3b 的耗時、PGID 檢查的實際輸出。Gate A worktree 已移除，這些無法在事後重跑複驗。**Gate B 仍須依轉移契約重跑三包綠燈驗證並逐項核對四項轉移條件**，不以 Gate A 的結果取代。

### 與 plan 預期的偏差（均非失敗，記錄備查）

- **root 回歸為 422 PASS ＋ 1 SKIP**，非 rev3 所寫的「423 PASS」。SKIP 是既有 env-gated `TestRegenerateCorpusVerdicts`，與本票無關；rev4 已修正回歸表措辭。
- **#2 實測 0.61s**，快於 rev3 預期的「約 1.4s」。方向有利（餘裕更大），不影響任何斷言；plan 的 15 秒 context 與餘裕敘述不因此改寫，避免用單次量測取代 preflight 的多次量測結論。

### 已知缺口（誠實標註）

- **§6.7 原文未被滿足**：本票以 owner 核准的 B1a-2 專屬例外執行測試側 negative control，**不是** production-target mutation。強度要求（N/N、hash、還原、完整回歸）未降低，但適用規則與 §6.7 原文不同，不得混稱。
- **CI runner 冷啟動未驗證**：#3 的 383ms 是本輪**唯一**觀察到的冷啟動樣本，且來自本機。CI runner 的冷啟動分布仍未取得。這個缺口留給 **B1a-4** 的整合負載矩陣，本票不宣稱消除。
- **#2 未重現過誤紅**：本輪只量得約 9 倍餘裕並降低 fixture 成本；「15 秒足夠」是由餘裕推得，不是由重現誤紅再修好證得。
- **#4 失去 5 秒局部快速失敗**：注入 fakeAfter 後真正的 pump 卡死會落到 `go test -timeout`。這是 owner 已接受的取捨，不是被消除的風險。
- **三包的整合負載表現未驗證**：本票只跑 focused 與三包各自的 `-race`，未跑五套件同時的整合矩陣（屬 B1a-4，且該矩陣**包含**本票的三包，B1a-4 須於整合 HEAD 重跑，不得以本票結果取代）。

---

## 尚未完成（Gate B）

Gate A 已完成，**Gate B 尚未執行**。implementation 仍為 NO-GO，須待 owner 對本 rev 做 design gate 最終裁定後才可進入。

---

## Erratum

### owner design gate APPROVED（2026-09-03，rev4 → rev5）

owner 裁定 **Design Gate APPROVED**，不需重跑 Gate A，完成兩處文件修正並提交 plan 後即可進入 Gate B implementation。兩處修正**都不改模板、negative control 或執行範圍**：

- **golden hash 改記為已機械複驗**：owner 從基準 `a5a3cab` 建立全新 detached worktree、逐字套用 Task 1–3 模板並執行 `gofmt`，三個 hash 全部精確命中，因此 `if false { t.Fatal(err) }` 疑點已機械排除。證據分層 (a) 改寫，不再寫成「無法事後獨立複驗」；(c) 併入 (b) 的 Gate B 重跑要求。
- **Gate B 的 `<plan-commit>` 改為現場取得**：plan 無法在同一 commit 內記錄自己的最終 SHA，事後回填又會讓 `a5a3cab..HEAD` 的 range diff 多出一次 plan 異動。改以 `git log --reverse --format=%H a5a3cab..HEAD -- <plan 路徑> | head -1` 取首次加入 plan 的 commit，SHA 只留在 Gate B 證據，不回寫 plan。
- **證據邊界維持**：owner 本輪獨立驗證的是三個 golden hash、Git／worktree 狀態與文件契約；Gate A 的 `-race`、negative control、3b 與 PGID 實際輸出仍屬執行者結果。**Gate B 須依契約重跑三包綠燈驗證並核對四項轉移條件。**

### Gate A 執行完成（2026-09-03，rev3 → rev4）

- owner 於裁定 Gate A 可啟動時另指兩處非阻斷文字修正，已於本 rev 落實：Design-gate 證據段的「rev2 提交時為空」改成 rev3；Global Constraints 的「Task 4 的每一項 mutation」改成「每一項 negative control」，並指向 Task 4 開頭的 B1a-2 專屬例外措辭。
- Gate A 十項 checklist 全部執行完畢，證據回填「Design-gate 證據」段，並新增「證據強度分層」與「與 plan 預期的偏差」兩小節。
- **回歸表 root 列措辭修正**：實測為 **422 PASS ＋ 1 既有 env-gated SKIP**（總數仍 423），rev3 寫成「423 PASS」不精確。SKIP 為 `TestRegenerateCorpusVerdicts`（`domainspec_oracle_freshness_test.go:896-898`），與本票無關。
- **模板、negative control 定義、估點、票面範圍一律未動**；估點維持 1.01 pt。

### 第二輪 owner 裁定 CHANGES_REQUIRED（2026-09-03，rev2 → rev3）

owner 確認 rev2 的四項修正皆已正確落實，另提兩項 P1、一項 P2。**估點與票面範圍未動。**

- **P1 — Gate B 的 diff 條件必然失敗**：plan 會先 commit、implementation commits 在後，因此「對基準 `a5a3cab` 只含三個測試檔」與實際 commit 順序矛盾、永遠不可能成立。rev3 拆成兩條可重現的 range diff：`<plan-commit>..HEAD` 只含三個測試檔、`a5a3cab..HEAD` 只含 plan 文件＋三個測試檔；「三個 commit」一律改稱「三個 implementation commits」。並釐清這兩條 range 檢查與 per-commit 的工作區 `git diff --name-only HEAD` 是不同兩層，兩層都要做。
- **P1 — 程序殘留檢查仍可能誤判**：rev2 的「先落檔再 grep」只解決了 grep 程序尚未啟動這一半，沒有排除外層 `zsh -lc` wrapper 的命令列帶著待比對字串被快照收進去，也沒有把 `sleep 6` 限定到本次測試的 process group（全機器搜尋會比中無關工作）。rev3 改為 PGID 精確比對：3a 期間暫時 `t.Logf` 記錄 `s.PGID()`（`internal/claude/session.go:122`）→ 另一次獨立工具呼叫產生含 PGID 欄的 `ps` 快照 → `awk '$1 == g'` 以第一欄精確比對、預期空輸出 → 移除臨時 `t.Logf` 並確認 `session_test.go` hash 回到 golden 值。全機器字串計數降級為非主要證據。
- **P2 — 驗證環境殘留 rev1 舊字樣**：Task 3 Step 4 仍寫「`go build ./...`（主 repo）」，與已定案的「Gate A 全部在隔離 worktree」衝突。rev3 改為隔離 worktree，並校正 Global Constraints 的驗證分層——Task 1／2 是 package-scoped，Task 3 動 root package，完整 `go build ./...`／`go vet ./...`／`gofmt -l .` 由 Gate A 統一執行一次，不由各 task 重複。

### 第一輪 owner 裁定 CHANGES_REQUIRED（2026-09-03，rev1 → rev2）

- **#4 裁定選 A**：採用 `totalCreated()` 斷言，保留 `n == 0` 判斷形式；不算擴大 production scope。rev1 的「送 design gate 的未決項」章節連同三選項表已整章移除，Task 3 Step 3 改為既定步驟。mutation 4a 同步移除 `_ = timers` 中和子的敘述。
- **P1 — mutation 表違反規範且自相矛盾**：rev1 一邊要求「每項必須由自身斷言 FAIL」，一邊列了預期 PASS 的 3b，且把 §6.7 的 production-target 規則自行改寫成測試側規則。rev2 改為：明載 owner 核准的 B1a-2 專屬例外、分母改 3/3（2a／3a／4a）、3b 移出分母成為 3a 的必跑 positive control。另更正 2a 的敘述——它證明的是新版 fixture 仍正確跨越 Scanner 上限，不是證明移除 `tr` 提高鑑別力。
- **P1 — design-gate 證據缺少轉移契約**：rev1 的 Task 4 Step 4 從落地驗證直接跳到「三個 commit 已落在 main」，兩階段無銜接條件。rev2 拆成 **Gate A（隔離 worktree 落地驗證）** 與 **Gate B（主 repo implementation 完成）**，中間插入四條件的證據轉移契約，並要求記錄三個測試檔的 golden SHA-256；任一條件不符即整套重跑，不做部分轉移。
- **P1 — 3a 失敗路徑未回收 subprocess**：rev1 未察覺 `TestMultiTurnSendAndTurnBoundaries` 的回收全部集中在函式尾端（現 `session_test.go:70` 一帶），`waitResult` 的 `t.Fatal` 會整段跳過。rev2 在 Task 2 新增 Step 1，於 `Start` 成功後立即註冊 `t.Cleanup(Terminate → Wait)`；並新增 Task 4 Step 3 的殘留檢查，明令採用「先落檔快照、再比對檔案」的方式，禁用會自我比中的 `pgrep -f` 單行寫法。
- **P2 — 落地驗證方式定案**：rev1 保留「主 repo `git stash`」與「`frontend/dist` 佔位」二選一。rev2 依 owner 裁定改為單一方式：**隔離 worktree ＋ 從主 repo 複製既有 `frontend/dist`**，Global Constraints 與 Gate A checklist 均已同步，不再保留選項。
- **未變更**：估點維持 **1.01 pt**（0.81–1.21）；三條的範圍、production 零變更邊界、#2／#3 的措辭紀律均未動。
