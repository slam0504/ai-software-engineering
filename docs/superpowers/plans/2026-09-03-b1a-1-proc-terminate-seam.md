# B1a-1 Proc.Terminate() Timer／Signal-Event Seam Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev4（2026-09-03，owner 第二輪 CHANGES_REQUIRED 的兩項文件契約修正；程式模板與 mutation 語意未動，第二輪落地證據續用）
> 狀態：**design gate 待裁定**——rev2 模板已於隔離 worktree 完整重跑（build／vet／gofmt／`-race` 兩包／mutation 5/5 × 兩包／process 洩漏檢查），證據見「Design-gate 證據 · 第二輪」。主工作區未落地任何 `.go` 變更
> 票源：Pre-M4 Readiness Backlog **B1a-1**（`docs/architecture/pre-m4-readiness-backlog.md`，wall-clock 測試 #1 `TestAppServerTerminateKillsGroup` 根因修復；Appendix C bottom-up 估點 **1.13 pt**，範圍 0.90–1.35）
> 基準 commit：`a667ab595873cfd471aa450190c81cd05cb98a16`（本 plan 撰寫時的 HEAD＝origin/main）

**Goal:** 在 `internal/proc.Proc` 加入未匯出的 timer seam（`afterFunc`）與訊號事件 seam（`signalObserverFunc`），讓 `Terminate()` 的 grace-timeout 升級分支可被白箱測試確定性觸發與觀察；同步修正 `internal/codex` 那條長年誤報「已驗過 escalation」的測試，並在 `internal/proc` 新增真正驗到 escalation 順序的白箱測試。

**Architecture:** 沿既有 `internal/appcore.After`／`RealAfter`（`internal/appcore/pump.go`）與 `app.go` 的 `afterFn`／`a.after()` 慣例，在 `internal/proc` 內新增同形狀但**未匯出**的 timer seam；訊號事件另開一個獨立的未匯出 observer seam（`signalObserverFunc`），只在對應的 `SignalGroup` 呼叫**成功後**才發，讓白箱測試能區分 Terminate() 升級 KILL 與 supervisor 收尾管線的清孫程序 KILL。兩個 seam 皆為同套件 white-box 注入，不改變 `Proc` 的匯出面。

**Tech Stack:** Go（stdlib only，無新依賴）；驗證慣例 `go build`／`go vet`／`go test -race -count=1`，沿用本 repo 既有慣例。

**參考文件：**
- `docs/architecture/pre-m4-readiness-backlog.md`（B1a-1 範圍、owner 決策、Appendix C 估點、#1 escalation-branch-never-exercised 的 rev8 事實裁定）
- `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7（mutation 鑑別表 N/N 執行規則、四步完成定義、panic／跨範圍連帶不算有效紅燈）、§6.8（同檔 production comment 禁止內嵌行號，改用符號名；plan 文字例外允許「符號名＋（現 line N）」）
- `internal/appcore/pump.go`（`After`／`RealAfter` 型別慣例）、`app.go`（`afterFn`／`a.after()` App 層類比）
- `docs/superpowers/plans/2026-08-28-b6-m4-application-seams.md`（本 plan 的格式模板來源：header／Global Constraints／per-Task checkbox Step／mutation 鑑別表格式）

## 已實測的關鍵事實（backlog rev8，本票不重新論證）

`internal/codex/session_test.go` 現行（baseline `a667ab5`，`TestAppServerTerminateKillsGroup`，現 session_test.go:84）從未真正驗過 `Proc.Terminate()` 的 grace-timeout 升級分支：

- 測試用的 `FAKE_ORPHAN=1` fake app-server（`testdata/fake-codex-appserver.sh`）只讓**孫程序**（`bash -c 'trap "" TERM; sleep 30' &`）trap TERM；leader 腳本本身沒有 trap。
- `Terminate()`（現 proc.go:243）送出 group TERM 後，leader 立刻死亡，`exitedCh` 立刻 close；`Terminate()` 內的 `select { case <-p.exitedCh: ...; case <-time.After(p.grace): ... }` 永遠走前者，`time.After(p.grace)` 那個 escalation 分支從未觸發。
- 測試原本斷言的 `time.Since(start) > 5*time.Second` 失敗訊息（"kill escalation too slow"）具有誤導性——它實際驗到的是 **supervisor 收尾管線**（`Start()` 內 supervisor goroutine，`cmd.Wait()` 返回後對整組補送一次 SIGKILL 清孫程序，現 proc.go:146）能在合理時間內把整組收乾淨，跟 escalation 完全無關。

本票的兩個子任務因此必須綁在同一張票：(1) 在 `internal/proc` 新增能**確定性**觸發 escalation 分支的白箱測試（不依賴真實 grace timer），(2) 重寫 `internal/codex` 那條測試，讓它誠實地只宣稱驗到 supervisor 收尾路徑、並且**證明**自己沒有誤觸 escalation。兩者共用同一個 `Proc.Terminate()` seam 契約，拆開會讓其中一票的 seam 設計失去驗證對象。

## 七個呼叫點——本票零變更聲明

`proc.Start`／`proc.Output` 的匯出簽章與所有既有呼叫點在本票**零變更**（已 grep 逐一確認，基準 `a667ab5`）：

| 呼叫點 | 函式 |
|---|---|
| `app.go:4028` | `proc.Output` |
| `app.go:6425` | `proc.Output` |
| `internal/spec/gitrepo.go:48` | `proc.Output` |
| `internal/assist/preflight.go:157` | `proc.Output` |
| `internal/assist/oneshot.go:129` | `proc.Start` |
| `internal/claude/session.go:73` | `proc.Start` |
| `internal/codex/session.go:27` | `proc.Start` |

新增的兩個 seam 欄位（`after`／`onSignal`）為 `Proc` struct 的未匯出欄位，`Config`／`Start`／`Output`／`Terminate`／`Wait` 等既有匯出簽章全部不動。任何觸及以上 7 個呼叫點、`Config` 匯出面或既有死因仲裁（`termSent`／`killSent`／`fatalSig`）語意的變更都超出本票範圍，屬 class-3、須停手重新界定範圍。

## Global Constraints

- Module：`github.com/slam0504/sdlc-workbench`。
- 驗證指令（每 task 結尾）：`go build ./internal/proc/... ./internal/codex/...`、`go vet ./internal/proc/... ./internal/codex/...`、`go test -race ./internal/proc/... ./internal/codex/... -count=1`（受影響 package focused 跑；commit 前跑 `internal/proc`＋`internal/codex` 兩包全量）。**本票只做 focused 與 `internal/proc`／`internal/codex` 兩包回歸；B1a-4 的五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`，見 backlog `pre-m4-readiness-backlog.md` 的 B1a-4 列）本票不做、也不得宣稱完成**。注意該矩陣**包含 `internal/codex` 與 `internal/proc`**——B1a-4 會在整合 HEAD 對這兩包再跑一次，**不以 B1a-1 的 focused 結果取代**（見下方 Design-gate 證據段落的範圍澄清）。
- 無新外部依賴；一律 stdlib。
- **`go build ./...`（全包）只在主 repo 跑，不在隔離 worktree 跑**：root package 的 `main.go` 有 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore:18` 忽略、零追蹤檔——`git worktree add` 不會帶過去，因此新 worktree 內 `go build ./...` 必然失敗於 `pattern all:frontend/dist: no matching files found`。這是 worktree 隔離的產物，不是回歸。已於主 repo 遮蔽 `frontend/dist` 重現確認：失敗只波及 root package，`go build ./internal/...` 仍 exit 0。worktree 內用套件範圍的 build，全包 build 留到 implementation 於主 repo 執行。
- **同檔 production comment 禁止內嵌行號**（governance §6.8）：新增的 `proc.go` 註解一律用符號名指稱同檔宣告，不寫行號；本 plan 文件內的行號引用一律標「符號名＋（現 line N，基準 `a667ab5`）」。
- **不新增／不修改任何匯出 `Config` 或其他匯出簽章**；seam 僅限未匯出欄位、同套件 white-box 注入。
- **Observer 呼叫規約**：`onSignal` 事件一律在解鎖後才呼叫、不得在持有 `p.mu` 時呼叫；事件本身**不參與**任何 production 判定（純觀察）；事件只在對應的 `SignalGroup` 呼叫**成功**後才發（≥3 種：TERM 送出、Terminate 升級 KILL、supervisor 收尾 KILL）。
- **Seam 讀寫必須是 race-safe 的**：欄位讀取需在 `p.mu` 下完成，或在 goroutine 啟動前固定成局部變數；一律以 `go test -race` 驗證。
- **Nil-safe 是硬性要求**：既有 `proc_test.go` 有多處以 `&Proc{...}` 部分欄位建構（例：`TestTerminateDoesNotRecordUnsentSignal` 現用 `&Proc{pgid:, grace:, exitedCh:}`、`TestCanceledByContextArbitratesByActualExit` 用不含 `exitedCh` 的部分建構）。新 seam 欄位為 nil 時必須退回真實行為（真實 timer、no-op observer），不得呼叫 nil function（panic）或送值進 nil channel（永久 hang）。
- **保留既有死因仲裁與 `SignalGroup` 錯誤語意**：`termSent`／`killSent`／`fatalSig` 的既有記錄規則（成功才記錄、鎖內同臨界區）不得變動；受影響既有測試見 Task 3 回歸清單。
- **測試失敗也必須清乾淨 process group**：新增測試如果斷言排在 `p.Wait()`／process 死亡之前（為了驗中間事件順序），必須用 `t.Cleanup` 等機制保證任何失敗路徑都不留下殘存的子程序（本票落地驗證階段實測到一次真實違反，見下方落地驗證段落）。
- **#1 的 mutation 鑑別力非唯一**：supervisor 收尾管線是 7 個呼叫點共用的路徑；`internal/codex` 的 `Server.Handshake` 也被同檔另外兩條測試共用。Task 3 的鑑別表**每一項 mutation 都必須實跑 `internal/proc` 與 `internal/codex` 兩包**，逐項記錄兩包各自的紅／綠清單；不得只跑植入點所在的那一包、不得只靠 grep 推測、也不延後到 B1a-4（還原後無法補證）。這是 mutation collateral 驗證，與 B1a-4 的五套件整合負載矩陣是兩回事。

---

## Task 1: `internal/proc` — timer／signal-event seam ＋ (b) 白箱測試

- [ ] **Step 1: 寫紅——三條新白箱測試（先失敗，因為 seam 尚不存在）**

新增到 `internal/proc/proc_test.go` 尾端：

```go
// ---- backlog B1a-1：Terminate() 未匯出 timer／signal-event seam 的白箱測試 ----

// killAndReap：white-box 測試的收尾 helper。在 p.mu 下確認「退出尚未被記錄」才
// 對 group 送 KILL——程序退出後 pgid 可能已被 OS 回收並重用，無條件送 KILL 有打
// 到其他 process group 的風險（與 cancelRequested 的既有理由同源）；解鎖後等
// supervisor 收尾完成，確保任何失敗路徑都不留下子程序，也不留下卡在注入 timer
// 上的 escalation goroutine。僅適用於經 Start／bashProc 啟動、有 supervisor 的
// Proc；手工部分建構的 Proc 沒有 doneCh 可等，也沒有真實程序要清。
func killAndReap(t *testing.T, p *Proc) {
	t.Helper()
	p.mu.Lock()
	if !p.exited {
		_ = p.SignalGroup(syscall.SIGKILL)
	}
	p.mu.Unlock()
	p.Wait() // 等 supervisor 收尾（stderr EOF ＋ Exit 快取）
}

func TestTerminateEscalatesViaInjectedTimerInOrder(t *testing.T) {
	p := bashProc(t, context.Background(), `trap '' TERM HUP; echo ready; sleep 3600`, time.Hour)
	t.Cleanup(func() { killAndReap(t, p) }) // 失敗路徑一律清組並等收尾
	buf := make([]byte, 8)
	if _, err := p.Stdout.Read(buf); err != nil { // 等 trap 生效
		t.Fatal(err)
	}
	go func() { _, _ = io.ReadAll(p.Stdout) }()

	events := make(chan signalEvent, 8)
	timerCh := make(chan time.Time, 1)
	durCh := make(chan time.Duration, 1) // channel 而非共用變數：避免 -race
	p.mu.Lock()
	p.onSignal = func(ev signalEvent) { events <- ev }
	p.after = func(d time.Duration) <-chan time.Time { durCh <- d; return timerCh }
	p.mu.Unlock()

	if err := p.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	select { // TERM 事件在 Terminate() 返回前同步發出；此刻 leader 已 trap TERM
	case ev := <-events: // 仍活著，不可能有其他事件搶先入列
		if ev != sigEventTermSent {
			t.Fatalf("第一個事件 = %v, want sigEventTermSent", ev)
		}
	default:
		t.Fatal("Terminate() 返回時必須已經送出 sigEventTermSent")
	}
	select {
	case d := <-durCh:
		if d != p.grace {
			t.Fatalf("注入的 timer 收到的 duration = %v, want p.grace = %v", d, p.grace)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("卡死保險：escalation goroutine 未呼叫注入的 after()")
	}
	select {
	case <-p.exitedCh:
		t.Fatal("leader 不該在 escalation timer 觸發前就退出（腳本已 trap TERM/HUP）")
	default:
	}
	timerCh <- time.Now() // 測試觸發：模擬 grace 逾時

	// escalation KILL 事件與 supervisor 收尾 KILL 事件之間沒有 happens-before：
	// escalation 是在 p.mu 解鎖之後才通知觀察者，這段空窗足以讓 leader 死亡、
	// supervisor 送出自己的 cleanup 事件並搶先入列。因此只要求 escalation 事件
	// 「會出現」，容許 cleanup 事件夾在它前面，不斷言它必為佇列第二筆。
	deadline := time.After(10 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-events:
			switch ev {
			case sigEventEscalationKill:
				got = true
			case sigEventSupervisorCleanupKill: // 容許：與 escalation 事件無順序保證
			default:
				t.Fatalf("非預期事件 %v", ev)
			}
		case <-deadline:
			t.Fatal("卡死保險：未收到 sigEventEscalationKill 事件")
		}
	}

	ex := p.Wait()
	if ex.Code == 0 {
		t.Fatal("escalation KILL 收場不得是 exit 0")
	}
	if !groupGone(p.PGID()) {
		t.Fatal("escalation KILL 之後 process group 必須完全消失")
	}
}

// 本測試手工部分建構 Proc、不啟動任何長駐程序（probe 已 Wait 收屍），因此不註冊
// killAndReap——它沒有 supervisor，也沒有 doneCh 可等。
func TestTerminateDoesNotEmitTermSentEventWhenSignalGroupFails(t *testing.T) {
	probe := exec.Command("/usr/bin/true")
	if err := probe.Start(); err != nil {
		t.Fatal(err)
	}
	if err := probe.Wait(); err != nil {
		t.Fatal(err)
	}
	events := make(chan signalEvent, 4)
	p := &Proc{pgid: probe.Process.Pid, grace: time.Second, exitedCh: make(chan struct{})}
	p.mu.Lock()
	p.onSignal = func(ev signalEvent) { events <- ev }
	p.mu.Unlock()
	if err := p.Terminate(); err == nil {
		t.Fatal("對已消失的 process group 送 TERM 必須回報錯誤")
	}
	select {
	case ev := <-events:
		t.Fatalf("SignalGroup 失敗時不得發出任何事件，收到 %v", ev)
	default:
	}
}

func TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned(t *testing.T) {
	cases := []struct {
		name      string
		script    string
		wantEvent bool
	}{
		{name: "orphan_present_event_fires",
			script:    `bash -c 'trap "" TERM; sleep 30' & echo ready; read -r _; echo out; echo err >&2; exit 5`,
			wantEvent: true},
		{name: "no_orphan_no_event",
			script:    `echo ready; read -r _; exit 5`,
			wantEvent: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := bashProc(t, context.Background(), c.script, time.Second)
			t.Cleanup(func() { killAndReap(t, p) }) // 前段斷言失敗也要清乾淨
			buf := make([]byte, 8)
			if _, err := p.Stdout.Read(buf); err != nil {
				t.Fatal(err)
			}
			events := make(chan signalEvent, 8)
			p.mu.Lock()
			p.onSignal = func(ev signalEvent) { events <- ev }
			p.mu.Unlock()
			out, rd := drainStdout(p)
			if _, err := p.Stdin.Write([]byte("\n")); err != nil {
				t.Fatal(err)
			}
			ex := p.Wait()
			if ex.Code != 5 {
				t.Fatalf("code = %d, want 5", ex.Code)
			}
			rd.Wait()
			if c.wantEvent && !strings.Contains(out.String(), "out") {
				t.Fatalf("stdout = %q", out.String())
			}
			select {
			case ev := <-events:
				if !c.wantEvent {
					t.Fatalf("no_orphan 案例不該有 cleanup KILL 事件，收到 %v", ev)
				}
				if ev != sigEventSupervisorCleanupKill {
					t.Fatalf("事件 = %v, want sigEventSupervisorCleanupKill", ev)
				}
			default:
				if c.wantEvent {
					t.Fatal("orphan_present 案例必須發出 sigEventSupervisorCleanupKill")
				}
			}
			if !groupGone(p.PGID()) {
				t.Fatal("process group 必須完全消失（含孫程序，若有）")
			}
		})
	}
}
```

跑 `go test ./internal/proc/... -run 'TestTerminateEscalatesViaInjectedTimerInOrder|TestTerminateDoesNotEmitTermSentEventWhenSignalGroupFails|TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned'`，預期編譯失敗（`signalEvent`／`p.onSignal`／`p.after` 尚不存在）。

- [ ] **Step 2: 加 seam 型別與欄位（`Proc` struct 尾端，緊接既有 `fatalSig` 欄位之後）**

```go
	// after／onSignal：backlog B1a-1 的未匯出 timer／signal-event seam，僅同套件
	// white-box 測試可注入（proc_test.go）。讀寫一律經 seamAfter／seamOnSignal 在
	// p.mu 下完成；回傳值本身在解鎖後才被呼叫／使用，避免 observer 持鎖呼叫、也
	// 避免與測試注入的欄位寫入產生 -race。nil 時分別退回 real timer（time.After）
	// 與 no-op。
	after    afterFunc
	onSignal signalObserverFunc
}

// afterFunc 是 Terminate() 的計時器 seam；型別對齊 internal/appcore/pump.go 的
// After／RealAfter 慣例。nil 時退回 time.After（見 seamAfter）。
type afterFunc func(time.Duration) <-chan time.Time

// signalEvent 區分 Proc 內部三個「實際送出過訊號」的時刻——只在對應的
// SignalGroup 呼叫成功後才發出。
type signalEvent int

const (
	sigEventTermSent              signalEvent = iota // Terminate() 的 group SIGTERM 送出成功
	sigEventEscalationKill                            // Terminate() grace 逾時升級的 group SIGKILL 送出成功
	sigEventSupervisorCleanupKill                     // supervisor 收尾管線的清孫程序 group SIGKILL 送出成功
)

// signalObserverFunc 是訊號事件的 seam；nil 時退回 no-op（見 seamOnSignal）。
// 呼叫規約：不得在持有 p.mu 時呼叫，也不參與任何 production 判定——純觀察用途。
type signalObserverFunc func(signalEvent)

// seamAfter／seamOnSignal：nil-safe 存取子。欄位讀取在 p.mu 下完成，但回傳值
// 本身在解鎖後才被呼叫／使用。
func (p *Proc) seamAfter() afterFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.after != nil {
		return p.after
	}
	return time.After
}

func (p *Proc) seamOnSignal() signalObserverFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onSignal != nil {
		return p.onSignal
	}
	return func(signalEvent) {}
}
```

- [ ] **Step 3: supervisor 收尾管線改為捕捉錯誤並在成功時發事件**（`Start()` 內 supervisor goroutine，現 `_ = p.SignalGroup(syscall.SIGKILL)` 那一行，symbol：supervisor goroutine 內對 `cmd.Wait()` 返回後的清理段落）

```go
		cleanupErr := p.SignalGroup(syscall.SIGKILL) // 子程序已退出 → 立即清整組殘存孫程序
		if cleanupErr == nil {
			p.seamOnSignal()(sigEventSupervisorCleanupKill) // 只在 SignalGroup 成功後、鎖外才發
		}
		wg.Wait() // stderr 讀到 EOF（group kill 保證）
```

- [ ] **Step 4: `Terminate()` 改為使用 seam 並在成功時發事件**（現 `func (p *Proc) Terminate() error`）

```go
	// 事件只在 SignalGroup 成功後、解鎖後才發。
	p.seamOnSignal()(sigEventTermSent)
	after := p.seamAfter() // 在 goroutine 啟動前固定，避免與後續欄位寫入產生 -race
	go func() {
		select {
		case <-p.exitedCh:
		case <-after(p.grace):
			p.mu.Lock()
			killed := false
			if !p.exited {
				if p.SignalGroup(syscall.SIGKILL) == nil {
					p.killSent = true
					killed = true
				}
			}
			p.mu.Unlock()
			if killed {
				p.seamOnSignal()(sigEventEscalationKill)
			}
		}
	}()
	return nil
```

- [ ] **Step 5: 跑 Step 1 三條新測試，預期 PASS；`go test -race ./internal/proc/... -count=1` 全包（15 條既有＋3 條新增＝18 條頂層；新增的第三條含 2 個子測試）**
- [ ] **Step 6: `gofmt -l`／`go vet ./internal/proc/...` 乾淨**
- [ ] **Step 7: Commit**

```bash
git add internal/proc/proc.go internal/proc/proc_test.go
git commit -m "feat(proc): B1a-1 timer／signal-event seam＋escalation 白箱測試（backlog #1 rev8）"
```

---

## Task 2: `internal/codex` — 重寫 `TestAppServerTerminateKillsGroup`

- [ ] **Step 1: 寫紅——先確認舊斷言真的失效（不需要，舊測試本身已 PASS 但驗錯東西；直接重寫）**
- [ ] **Step 2: 重寫測試本體**（`internal/codex/session_test.go`，現 `func TestAppServerTerminateKillsGroup`，現 line 84；imports 段新增 `"errors"`、`"os/exec"`）

```go
// TestAppServerTerminateKillsGroup 驗「leader 收到 TERM 後退出、supervisor
// 在 cmd.Wait 返回後清除仍存活的 process group」——不是 escalation。
//
// #1 preflight 事實修正（backlog B1 rev8）：這條測試從未真的驗過 Terminate()
// 的 grace-timeout 升級分支。FAKE_ORPHAN 只讓孫程序 trap TERM，leader 本身不
// trap——group TERM 一到 leader 就死，Terminate() 內的 escalation select 永遠
// 走 <-p.exitedCh。原本的 kill escalation too slow 斷言因此從未驗到它宣稱要驗
// 的東西；已移除，不再宣稱驗到 escalation。deterministic escalation 契約改由
// internal/proc 的白箱測試承擔。
//
// 本測試如何把孫程序的死亡確定性歸因給 supervisor 收尾管線：把 TermGrace 拉到
// 遠大於整條測試生命週期（1 小時），escalation 分支的 grace timer 在測試結束前
// 不可能到期，因此對 group 送出 KILL 的只可能是 supervisor 在 cmd.Wait 返回後
// 的清孫程序路徑。這個歸因不需要存取 internal/proc 的未匯出 seam。
// 另外斷言 leader 死因為 SIGTERM，把「leader 自身也不是被 KILL 收掉的」一併釘死。
func TestAppServerTerminateKillsGroup(t *testing.T) {
	cfg := fakeSrvCfg(t, "FAKE_ORPHAN=1")
	cfg.TermGrace = time.Hour // 遠大於測試生命週期：escalation 分支在本測試中不可能到期
	srv, err := StartAppServer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Terminate(); srv.Wait() }) // 失敗路徑也要收乾淨；
	// Terminate() 內建「退出已記錄就不送訊號」的守衛，故對已死的 pgid 呼叫是安全的。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Handshake(ctx, ClientInfo{Name: "t", Version: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	// 真實時間 timeout 只作卡死保險（不作效能驗收，backlog #1 裁定移除 5s 效能
	// 斷言）：go test 的全域 -timeout 已是最終防線。
	ex := srv.Wait()
	if ex.Code == 0 {
		t.Fatal("terminated server must not exit 0")
	}

	// leader 死因必須是 TERM 本身。
	var ee *exec.ExitError
	if !errors.As(ex.Err, &ee) {
		t.Fatalf("leader 必須死於訊號（*exec.ExitError），實得 %v", ex.Err)
	}
	ws, isWS := ee.Sys().(syscall.WaitStatus)
	if !isWS || !ws.Signaled() {
		t.Fatalf("leader 必須死於訊號終止，實得 isWS=%v", isWS)
	}
	if ws.Signal() != syscall.SIGTERM {
		t.Fatalf("leader 死因必須是 SIGTERM（未進入 escalation 分支），實得 %v", ws.Signal())
	}

	// 孫程序（trap TERM、sleep 30）也必須消失——在 TermGrace=1h 的前提下，只可能
	// 是 supervisor 收尾管線清掉的。
	if !srvGroupDead(srv.PGID()) {
		t.Fatal("process group must be fully dead")
	}
}
```

- [ ] **Step 3: `go test -race ./internal/codex/... -count=1` 全包（47 條頂層，含改寫後這條；條數不變，含 `TestAppServerMidStreamDeath` 等相鄰測試零回歸）**
- [ ] **Step 4: `gofmt -l`／`go vet ./internal/codex/...` 乾淨**
- [ ] **Step 5: Commit**

```bash
git add internal/codex/session_test.go
git commit -m "test(codex): B1a-1 修正 TestAppServerTerminateKillsGroup 誤報 escalation 的斷言（backlog #1 rev8）"
```

---

## Task 3: Mutation 鑑別表 ＋ 回歸 ＋ design gate 收尾

- [ ] **Step 1: mutation 鑑別表 5 項全部執行（5/5，不抽驗）；design gate 階段即實跑，不延後到 B1a-4**

下方「Mutation 鑑別表」的 5 項全部執行。每一項都必須完成以下四步並留下證據，缺任一步即該項未通過：

1. **套用**：production 檔案 hash（`shasum -a 256`）在套用前後不同，確認變異真的落到檔案上。
2. **轉紅**：表中指定的測試確實 FAIL，且**紅在正題**——失敗訊息是該測試自己的斷言，不是撞到 `go test -timeout` 或 panic。
3. **還原**：還原後檔案 hash 與變異前**byte-identical**。
4. **轉綠**：還原後，指定測試（focused）＋`internal/proc` 與 `internal/codex` **兩包**全包回綠。

**關於 `_ = <var>` 中和子**：第 2、4、5 項移除的程式碼會讓原本被它使用的區域變數變成 write-only，Go 編譯器直接報 `declared and not used`——編譯失敗不是有效紅燈（§6.7）。因此表中明列必須補上的 `_ = <var>`：它只中和編譯器的未使用檢查，不改變該變異的 runtime 語意（事件仍然不發／注入的 seam 仍然被忽略）。這是變異定義的一部分，不是執行者的臨場發揮，套用時必須逐字照做。

另外，**轉紅那一步也要同時跑兩包**並記錄 `internal/codex` 的實際結果（預期全綠——本表 5 項植入點都在 `internal/proc/proc.go`，且 `internal/codex` 不注入任何 observer——但這個預期必須由實跑證實，不得推定）。

**Mutation 鑑別表（`internal/proc/proc.go`，逐條一對一）**：

| # | Mutation（植入處） | 預期紅燈斷言 | 對應測試 |
|---|---|---|---|
| 1 | `Terminate()` 移除 `p.seamOnSignal()(sigEventTermSent)` 那一行 | `Terminate()` 返回時必須已經送出 `sigEventTermSent` | `TestTerminateEscalatesViaInjectedTimerInOrder` |
| 2 | `Terminate()` escalation 分支移除 `if killed { p.seamOnSignal()(sigEventEscalationKill) }` 整段，並在 `killed` 宣告後補 `_ = killed` | 「卡死保險：未收到 sigEventEscalationKill 事件」——現行測試的等待迴圈會吸收掉 supervisor cleanup 事件，因此唯一的紅燈路徑是本測試自身的 10s `t.Fatal`（第二輪實測 10.01s），不是 `go test -timeout`、也不會出現任何「第二個事件」形式的訊息 | `TestTerminateEscalatesViaInjectedTimerInOrder` |
| 3 | `Terminate()` 把 `p.seamOnSignal()(sigEventTermSent)` 移到 `if err != nil { return err }` **之前**（SignalGroup 失敗仍誤發事件） | 「SignalGroup 失敗時不得發出任何事件，收到 …」 | `TestTerminateDoesNotEmitTermSentEventWhenSignalGroupFails` |
| 4 | supervisor 收尾段移除 `if cleanupErr == nil` guard、改成無條件發 `sigEventSupervisorCleanupKill`，並補 `_ = cleanupErr` | no_orphan 案例不該有 cleanup KILL 事件 | `TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned/no_orphan_no_event` |
| 5 | `Terminate()` escalation goroutine 把 `<-after(p.grace)` 改回硬編 `<-time.After(p.grace)`（忽略注入的 seam），並補 `_ = after` | 卡死保險：escalation goroutine 未呼叫注入的 `after()` | `TestTerminateEscalatesViaInjectedTimerInOrder` |

**明確排除、不列入本表的第 6 項**：讓 escalation 事件在 `SignalGroup(SIGKILL)` 失敗時仍然發出（即移除 `killed` 的成敗判斷本身，而非移除整段發事件邏輯）。這需要在 escalation 觸發的瞬間讓 `SignalGroup` 對一個仍存在的 group 回錯——在 macOS 上只能靠 OS 排程競態（例如另一執行緒同時把 group kill 掉）構造，無法確定性重現。此為 backlog rev8 已承認的「#1 鑑別力非唯一」的具體體現之一，保留為已知缺口、不假造一個會 flaky 或需要 sleep-based 賭時序的測試來湊數。

- [ ] **Step 2: 既有回歸測試清單——零回歸（`internal/proc` 全部 15 條既有＋`internal/codex` 全部 47 條頂層既有）**，其中與死因仲裁／`SignalGroup` 語意直接相關、須特別確認：`TestCanceledByContextArbitratesByActualExit`、`TestTerminateDoesNotRecordUnsentSignal`、`TestNaturalSignalDeathDuringCancelIsNotCancellation`、`TestTerminateEscalatesToGroupKill`、`TestAppServerMidStreamDeath`。
- [ ] **Step 3: 記錄每項 mutation 在 `internal/proc` 與 `internal/codex` 兩包的實際紅／綠清單，寫入落地驗證段落**（不得只靠 grep 推測；兩包都要有逐項結果）。
- [ ] **Step 4: implementation 完成 gate**——確認 Task 1／Task 2 兩個 commit 已落在主 repo；`go build ./...`、`go vet`、`go test -race ./internal/proc/... ./internal/codex/... -count=1` 全綠；mutation 鑑別表 5/5 四步齊備且兩包連帶清單已記錄；`gofmt -l` 與 `git diff --check` 乾淨。**五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）明確不在本票範圍，屬 B1a-4，不得宣稱完成。**注意該矩陣**包含 `internal/codex` 與 `internal/proc`**——B1a-4 會在整合 HEAD 對這兩包再跑一次，**不以 B1a-1 的 focused 結果取代**。

---

## Design-gate 證據

### 第一輪（2026-09-03，隔離 worktree `~/scratch-worktrees/b1a1-proc-seam`，基準 `a667ab5`）——**已作廢，不得作為關票依據**

第一輪曾在隔離 worktree 完整套用模板並跑出 build／vet／`-race`／mutation 5/5。owner 於 CHANGES_REQUIRED 裁定後，測試模板與 mutation 表的位元組均已改變（P1-1～P1-5），**該輪證據整套作廢**，必須以現行模板重跑，不得沿用。worktree 已於第一輪結束時移除。

第一輪仍然成立、已直接反映進現行模板的**設計發現**（這些是實測得到的失效形狀，不是可推測的結論）：

1. **資料競賽**（`-race` 抓到）：初稿用共用變數記錄注入 `after()` 收到的 duration，寫入在 escalation goroutine、讀取在測試 goroutine。改用 buffered channel（`durCh`），靠 send/receive 的 happens-before 同步。
2. **Barrier 缺失**：初版腳本沒有「trap 已生效」的 barrier，`Terminate()` 送 TERM 偶爾早於 bash 執行到 `trap` 那一行，leader 走預設處置直接死亡，escalation 分支意外沒被觸發。改成腳本先 `trap` 再 `echo ready`、測試讀到 `ready` 才呼叫 `Terminate()`，沿用 `TestTerminateEscalatesToGroupKill`／`TestCtxCancelKillsWholeGroup` 的既有 barrier 慣例。
3. **失敗路徑洩漏 process group**：斷言刻意排在 `p.Wait()` 之前（為驗事件順序），任何中間斷言 `t.Fatal` 都會讓 escalation goroutine 卡在永遠收不到訊號的 channel 上，leader（已 `trap TERM/HUP`）永久存活。`ps aux` 實測確認洩漏。現行模板改用 `killAndReap` helper（見 P1-4）。
4. **escalation／cleanup 事件無順序保證**：實測到 escalation KILL 之後偶爾會再收到一次 `sigEventSupervisorCleanupKill`。第一輪只是刪掉過嚴的斷言；owner 於 P1-2 指出真正的根因是兩者之間沒有 happens-before（escalation 在解鎖後才通知觀察者，這個空窗足以讓 supervisor 搶先入列），現行模板改為**容許 cleanup 事件夾在前面的等待迴圈**，而不是迴避斷言。

### 第二輪（rev2 模板，隔離 worktree `~/scratch-worktrees/b1a1-r2`，基準 `a667ab5`）——已完成

模板逐字套用，**零設計偏離**；唯一的機械調整是 seam const 區塊的 gofmt 對齊（貼上時多一個空格，`gofmt -w` 修正）。修正後的 golden hash：`b40c9e6c604ebb03ef7a66137f113f4c795e9012179a6400558bcaa9bbeb5c5f`，五輪 mutation 的還原全部以它為基準做 byte-identical 比對。

**基礎驗證**

| 指令 | 結果 |
|---|---|
| `go build ./internal/proc/... ./internal/codex/...` | exit 0 |
| `go vet ./internal/proc/... ./internal/codex/...` | 乾淨 |
| `gofmt -l internal/proc internal/codex` | 零輸出 |
| `go test -race ./internal/proc/... -count=1 -v` | **18 條頂層 PASS／含子測試 20 條**，約 21.7s |
| `go test -race ./internal/codex/... -count=1 -v` | **47 條頂層 PASS／含子測試 73 條**，約 2.9s |

兩包條數與 rev2 預測完全相符（proc 15 既有＋3 新增，其中一條含 2 個子測試；codex 改寫不增減條數）。

**Mutation 鑑別表 5/5，每項四步齊備、每步都實跑兩包**

| # | 套用（hash 改變） | 轉紅（逐字抄錄的失敗訊息） | `internal/codex` 連帶 | 還原 byte-identical | 轉綠（兩包） |
|---|---|---|---|---|---|
| 1 | 確認 | `proc_test.go:510: Terminate() 返回時必須已經送出 sigEventTermSent`（測試自身 `t.Fatal`，0.01s） | `ok` 2.648s | 確認 | 兩包 `ok` |
| 2 | 確認 | `proc_test.go:543: 卡死保險：未收到 sigEventEscalationKill 事件`（測試自身 10s deadline `t.Fatal`，實跑 10.01s，非 `go test -timeout`、無 panic） | `ok` 2.420s | 確認 | 兩包 `ok` |
| 3 | 確認 | `proc_test.go:576: SignalGroup 失敗時不得發出任何事件，收到 0`（測試自身 `t.Fatalf`） | `ok` 2.360s | 確認 | 兩包 `ok` |
| 4 | 確認 | `proc_test.go:621: no_orphan 案例不該有 cleanup KILL 事件，收到 2`（測試自身 `t.Fatalf`；`no_orphan_no_event` FAIL 而姊妹子測試 `orphan_present_event_fires` 仍 PASS——雙向鑑別成立） | `ok` 3.351s | 確認 | 兩包 `ok` |
| 5 | 確認 | `proc_test.go:518: 卡死保險：escalation goroutine 未呼叫注入的 after()`（測試自身 10s deadline `t.Fatal`，實跑 10.01s） | `ok` 2.569s | 確認 | 兩包 `ok` |

五項紅燈全部是測試自己的 `t.Fatal`／`t.Fatalf`，無一撞到 `go test -timeout`、panic 或編譯失敗。`internal/codex` 五項全綠——符合預期（植入點都在 `internal/proc/proc.go`，且 codex 不注入任何 observer），且此預期已由實跑證實，非推定。

**Process 洩漏檢查**：mutation #1／#2／#5 會讓 `TestTerminateEscalatesViaInjectedTimerInOrder` 走失敗路徑；每次轉紅後立即執行 `ps aux | grep -c "[s]leep 3600"`，三次皆為 **0**。`killAndReap` 的 `t.Cleanup` 在 escalation 失敗路徑上確實生效。

**收尾確認**：worktree 已 `--force` 移除、`~/scratch-worktrees/` 已空、`git worktree list` 只剩主 repo；主 repo `git rev-parse HEAD` 仍為 `a667ab5`、`git status --porcelain` 只有本 plan 檔一行未追蹤。

**明確不在本票範圍、標記未驗證**：五套件整合負載矩陣（root／`internal/codex`／`internal/assist`／`internal/claude`／`internal/proc`）屬 B1a-4。注意該矩陣**包含 `internal/codex` 與 `internal/proc`**——B1a-4 會在整合 HEAD 對這兩包再跑一次，**不以 B1a-1 的 focused 結果取代**。`go build ./...`（全包）在隔離 worktree 不可執行（見 Global Constraints 的 `frontend/dist` 說明），留待 implementation 於主 repo 驗證。

### 已知缺口（誠實標註）

- **第 6 項 mutation 未列表**：讓 escalation 事件在 `SignalGroup(SIGKILL)` 失敗時仍發出。要構造此情境需要 group 在注入 timer 觸發的瞬間已消失，但 group 消失必然經由 supervisor 設 `p.exited = true`，而該分支的 `if !p.exited` 守衛會先擋掉整段——兩個條件無法解耦，非省略。三條訊號路徑中，TERM 路徑（表第 3 列）與 supervisor 收尾路徑（表第 4 列）的「失敗側不得發事件」皆有測試守著，僅 escalation 路徑的失敗側無覆蓋。
- **`internal/codex` 層不做 mutation 鑑別**：該套件沒有存取 `internal/proc` 未匯出 seam 的手段，直接鑑別不成立於這一層。正向鑑別力由 Task 1 三條白箱測試提供；`internal/codex` 在 mutation 期間的角色是**連帶驗證**（確認不被誤傷），不是鑑別來源。

---

## Erratum

### 第一輪 design gate 審查（2026-09-03，審查者）

審查者以基準 `a667ab5` 獨立重跑 baseline，修正 plan 初稿三處：

1. **測試條數全數錯報**（已改為實測值）。實測 `go test -race -count=1 -v`：`internal/proc` 15 條頂層、無子測試；`internal/codex` 47 條頂層、含子測試 73 條。初稿寫的「16 條／27 條既有」與落地驗證表頭的「19/19」「28/28」皆不可重現。**跑批本身是真的**——初稿記錄的 13.089s／1.940s 與審查者重跑的 13.300s／2.012s 吻合，兩包 baseline 全綠；錯的是貼在真實跑批上的條數標籤。Task 3 Step 2 以「全部 N 條既有零回歸」為完成判準，N 錯會讓該判準無法驗收，故列為 gate 前必修。
2. **Mutation 表第 2 列有一句不可讀的破碎文字**（已重寫；該列的紅燈條件後續又依 P1-2 再次改寫）。
3. **Task 2 註解的證據強度與實際證據不符**（已改寫）。原文「孫程序的死亡因此只能歸因於 supervisor 收尾管線」是排他結論，但 `Exit.Err` 顯示 leader 死於 SIGTERM，只證明 leader 不是被 escalation KILL 殺的。

審查者另行獨立核實、未發現問題：`bashProc`／`groupGone`／`drainStdout` 三個 helper 存在且簽章相符；`proc_test.go` 既有 imports 已涵蓋新測試所需；`session_test.go` 確實需補 `errors`／`os/exec`；行號錨點 `proc.go:243`（`Terminate`）、`proc.go:146`（supervisor 收尾 KILL）、`session_test.go:84` 在基準上全部命中；supervisor 收尾 KILL 位於 `p.mu.Unlock()` 之後（proc.go:145 解鎖、146 送訊號），故在該處呼叫會自行取鎖的 `seamOnSignal()` 不會死鎖；`SignalGroup` 本身不取鎖。

### 第二輪 owner 裁定 CHANGES_REQUIRED（2026-09-03）

owner 判定 implementation NO-GO，核心 seam 設計不必重設，修正集中在測試與驗證契約。六項已全部套用：

| 項 | 問題 | 本 plan 的修正 |
|---|---|---|
| P1-1 | codex 測試沒有履行 (a) 契約——仍可能由 escalation KILL 清掉孫程序後通過 | 改為 `cfg := fakeSrvCfg(...)`＋`cfg.TermGrace = time.Hour`，讓 escalation 分支在測試生命週期內不可能到期，清理只能歸因於 supervisor 收尾管線。已獨立核實 `codex.Config.TermGrace` 於 `session.go:28` 直通 `proc.Config.TermGrace` → `p.grace`（proc.go:118），此手法成立且不需存取未匯出 seam |
| P1-2 | escalation 與 cleanup 事件沒有 happens-before，「第二個事件必為 escalation」會偶發失敗 | 等待 `sigEventEscalationKill` 改為容許略過 cleanup 事件的迴圈；mutation #2 的紅燈改為測試自身的 10s 卡死保險。**未為測試新增任何 production 排序機制** |
| P1-3 | mutation #3 打的是既有 `termSent` 記錄，不是本票新增的事件碼，違反 §6.7 變異目標規則 | #3 改為把 `p.seamOnSignal()(sigEventTermSent)` 移到 `if err != nil { return err }` 之前，預期由 `TestTerminateDoesNotEmitTermSentEventWhenSignalGroupFails` 轉紅；`TestTerminateDoesNotRecordUnsentSignal` 只留在回歸清單，不作本票 mutation |
| P1-4 | 第一條測試的 cleanup 是裸 `SignalGroup`（pgid 重用風險），第三條測試完全沒有 cleanup | 新增 `killAndReap` helper：在 `p.mu` 下確認 `!p.exited` 才送 KILL，解鎖後 `p.Wait()` 等收尾。三條新測試中兩條啟動真實程序者立即註冊；第二條手工建構、無 supervisor，已加註說明為何不註冊。codex 重寫測試加上等價的 `t.Cleanup(func(){ _ = srv.Terminate(); srv.Wait() })`（`Terminate()` 自帶「退出已記錄就不送訊號」守衛） |
| P1-5 | 連帶清單只跑了 `internal/proc` | Global Constraint 與 Task 3 Step 1／Step 3 全部改為**每項 mutation 實跑兩包**、逐項記錄兩包紅／綠 |
| P2 | 執行 checklist 混入已完成的 design-gate 動作，Task 1／2 的 commit 與「不進 implementation」互相矛盾 | 落地驗證結果與 worktree 清理移入「Design-gate 證據」段落（敘述體，非待辦）；Task 3 Step 4 改為 **implementation 完成 gate**；原「收尾」checkbox 段落移除 |

**第一輪的 mutation 5/5 證據已整套作廢**——測試模板與 mutation 表位元組均已改變，須以現行模板重跑後才可送下一輪 design gate。（重跑已完成，見「Design-gate 證據 · 第二輪」。）

### 第四輪 owner 裁定 CHANGES_REQUIRED（2026-09-03，文件契約修正，不涉程式模板）

owner 確認第一輪六項要求皆已關閉、且明示**不需第三輪落地驗證或 mutation 重跑**（`_ = killed`／`cleanupErr`／`after` 是把第二輪實際執行過的變異內容補回規格，不是證據完成後又改 runtime 語意）。本輪只修兩處文件不一致：

| 項 | 問題 | 修正 |
|---|---|---|
| P1 | B1a-4 負載矩陣寫錯：三處（Global Constraints／Task 3 Step 4／Design-gate 證據）稱「五套件」卻只列四個，且誤含 `internal/spec`、漏掉 `internal/codex` 與 `internal/proc` | 三處統一為 backlog B1a-4 列的正式矩陣 `root／internal/codex／internal/assist／internal/claude／internal/proc`，並補上邊界：該矩陣**包含** codex 與 proc，B1a-4 會在整合 HEAD 對這兩包再跑一次，不以 B1a-1 的 focused 結果取代。原寫法的實際風險是後續漏跑 codex／proc 的整合 HEAD、反而多跑不在矩陣內的 `internal/spec`，讓 B1a-4 驗收範圍失真 |
| P2 | mutation #2 的預期紅燈仍描述舊版事件順序（「第二個事件應為 escalation」「第二個事件 = 2」），而現行測試的等待迴圈會吸收 cleanup 事件、根本不會產生該訊息 | 只保留唯一真實紅燈「卡死保險：未收到 sigEventEscalationKill 事件」，刪除全部「第二個事件」分支描述，與第二輪實測（10.01s）對齊。此列是 P1-2 修正的連帶遺漏——當時的取代未命中目標字串，而批次尾端的整體斷言掩蓋了個別未命中；本輪改為逐項斷言後修正 |

**第二輪落地證據續用**：程式模板與 mutation 的 runtime 語意本輪皆未變動。

### 第三輪 重跑期間發現、已修進 plan（2026-09-03）

1. **`go build ./...` 在隔離 worktree 必然失敗，但這不是回歸**。root package 的 `main.go:22` 有 `//go:embed all:frontend/dist`，而 `frontend/dist` 被 `.gitignore:18` 忽略、零追蹤檔，`git worktree add` 不會帶過去。審查者於主 repo 獨立核實：`go build ./...` **exit 0**（dist 在本機存在）；暫時遮蔽 `frontend/dist` 後重現出一模一樣的 `pattern all:frontend/dist: no matching files found`，且確認波及範圍只有 root package——`go build ./internal/...` 仍 exit 0。執行者原本把它記成「與本票無關的既有 repo 狀態」，措辭不準確：repo 本身建得起來，建不起來的是**新開的隔離 worktree**。已改寫 Global Constraints，worktree 內用套件範圍 build，全包 build 留給主 repo 的 implementation 階段。這條同樣適用於未來任何在隔離 worktree 做範本落地驗證的票。
2. **Mutation #2／#4／#5 依字面套用會編譯失敗**。三者移除的程式碼各自讓一個區域變數（`killed`／`cleanupErr`／`after`）變成 write-only，Go 直接報 `declared and not used`——編譯失敗不是有效紅燈。執行者以最小的 `_ = <var>` 中和後，三項都紅在表中預測的斷言上；此處理正確且未更動變異語意，但**原表的寫法沒有預期到這個副作用**。已把 `_ = <var>` 明列進 mutation 表第 2、4、5 列，並在四步定義下方說明它只中和編譯器的未使用檢查、不改變 runtime 語意，套用時必須逐字照做——避免下一位執行者撞到同一個編譯錯誤後各自發揮。
