package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// ---- M3b Task 2a：registry uncertain latch 的 app 側拒絕範圍 ----
//
// latch 本身（rename 成功、directory sync 失敗 → 停手不猜）由
// internal/wsregistry/fsync_test.go 用真實 Store 守。這個檔案守的是另一半：
// **app 的哪些入口在 latch 期間必須拒絕、拒絕得夠早，哪些必須照樣放行。**
// 這裡用 stubRegistry 注入 latch，因為真實 Store 的注入鉤子是 package-private
// 的 test-only 方法，跨 package 取不到。

// TestRegistryUncertainRefusesLifecycleButAllowsReads：拒絕範圍的正題。
//
// 拒絕（會建立／銷毀 durable session 身分，或直接 mutate registry）：
// CreateSession／StartSession／NewSession／RemoveSession。
// 放行（唯讀）：ListSessions。
//
// **每個入口都各自斷言「拒絕得夠早」**，不只是「有回錯」——回錯但副作用已經
// 造成，跟沒擋一樣：
//   - CreateSession：名額不得被佔走。
//   - RemoveSession：一個 removeStep 都不得發出（deny_approvals／teardown／
//     cleanup_files 有不可逆的對外副作用）。
//   - StartSession：不得起 provider 子行程（hostFor 仍為 nil）。
//   - NewSession：不得 teardown（原 host 仍在）。
//
// mutation（各自只打紅一條，且紅在正題而非前提）：
//   - 拿掉 CreateSession 的 registryUncertain 早退 → 紅在「CreateSession 必須
//     拒絕」（stub 的 Put 不會失敗，所以會一路成功並佔名額）。
//   - 把 CreateSession 的早退移到 ReserveSession 之後 → 前一行仍綠，紅在
//     「不得佔走名額」。
//   - 拿掉 RemoveSession 的早退 → 紅在「RemoveSession 必須拒絕」。
//   - 把 RemoveSession 的早退移到 removeStep("cleanup_files") 之後 → 上一行仍
//     綠（stub 的 Remove 不會失敗，但 gate 仍會擋），紅在「不得跑到任何
//     removeStep」。
//   - 拿掉 StartSession 的早退 → 紅在「StartSession 必須拒絕」。
//   - 拿掉 NewSession 的早退 → 紅在「NewSession 必須拒絕」。
//   - 把 latch 檢查加進 ListSessions → 紅在「唯讀入口必須放行」。
func TestRegistryUncertainRefusesLifecycleButAllowsReads(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg

	// 前提：一個已啟動的 session（給 NewSession／RemoveSession 用），以及一個
	// 已建立但未啟動的 session（給 StartSession 用）。fake CLI 由 mustStartClaude
	// 寫好，所以 w2 的 StartSession 在沒有 gate 的情況下**會真的成功**——這條
	// 測試不靠「反正也起不來」兜底（失效形狀 A）。
	w1 := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w1)
	w2 := mustCreate(t, a, "claude")
	slotsBefore := a.manager.SlotCount("claude")

	reg.mu.Lock()
	reg.uncertain = true
	reg.mu.Unlock()

	// ---- CreateSession ----
	if _, err := a.CreateSession("claude", "after-latch"); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("latch 期間 CreateSession 必須拒絕，got %v", err)
	}
	if got := a.manager.SlotCount("claude"); got != slotsBefore {
		t.Fatalf("被拒絕的 CreateSession 不得佔走名額：%d → %d", slotsBefore, got)
	}

	// ---- StartSession ----
	if err := a.StartSession(string(w2), "hi", "", "", "task", ""); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("latch 期間 StartSession 必須拒絕，got %v", err)
	}
	if h := a.hostFor(w2); h != nil {
		t.Fatalf("被拒絕的 StartSession 不得起 provider 子行程：%+v", h)
	}

	// ---- NewSession ----
	if err := a.NewSession(string(w1)); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("latch 期間 NewSession 必須拒絕，got %v", err)
	}
	if a.hostFor(w1) == nil {
		t.Fatal("被拒絕的 NewSession 不得 teardown（原 host 必須還在）")
	}

	// ---- RemoveSession ----
	var steps []string
	a.hookRemoveStep = func(s string) { steps = append(steps, s) }
	if err := a.RemoveSession(string(w1)); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("latch 期間 RemoveSession 必須拒絕，got %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("被拒絕的 RemoveSession 不得跑到任何 removeStep（deny_approvals／cleanup_files 不可逆）：%v", steps)
	}
	if a.hostFor(w1) == nil {
		t.Fatal("被拒絕的 RemoveSession 不得 teardown（原 host 必須還在）")
	}
	a.hookRemoveStep = nil

	// ---- 唯讀入口放行 ----
	infos, err := a.ListSessions()
	if err != nil {
		t.Fatalf("唯讀入口必須放行（latch 的是 durability，不是記憶體內容）：%v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("唯讀入口必須照常回報既有 session：%+v", infos)
	}
}

// TestRegistryUncertainDoesNotBlockExistingConversation：拒絕範圍的另一半——
// 已經活著的對話不受影響。SendMessage 不寫 registry，續聊身分早已 commit；
// 擋掉它只會讓使用者在最需要看到說明的時候連手上的對話都用不了。
//
// mutation：把 registryUncertain 檢查加進 SendMessage → 紅在「既有對話不得被
// latch 擋住」。
func TestRegistryUncertainDoesNotBlockExistingConversation(t *testing.T) {
	a, ui := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	w := mustCreate(t, a, "claude")
	mustStartClaude(t, a, w)
	waitFor(t, "claude first result", func() bool { return len(ui.findEnvKind("result")) >= 1 })

	reg.mu.Lock()
	reg.uncertain = true
	reg.mu.Unlock()

	if err := a.SendMessage(string(w), "still works"); err != nil {
		t.Fatalf("既有對話不得被 latch 擋住：%v", err)
	}
}

// TestRegistryUncertainWriteIsFailLoudWithOwnSignal：latch 觸發時的訊號。
//
// per-WSID metadata writer（CommitResume／SetResume／ResetView）的失敗處置由
// noteRegistryWriteResult 統一。這條路徑**沒有同步呼叫端可以回錯**（late claude
// init 的 SetResume 是背景事件），所以 UI 訊號是使用者唯一的出口——它必須是前端
// **真的會消費並渲染**的形狀，不是「有呼叫 a.emit」就算數。
//
// rev2 走證據鏈時抓到的缺口：舊版直接 a.emit 一個 session-scope、**不帶
// workspace_session_id** 的 envelope。前端 routeEnvelope 把它判成 session lane，
// session store 的 apply() 對空 WSID 只做 `unrouted++` 然後丟棄，而 unrouted
// 至今沒有任何渲染端 → 這條 fail loud 對使用者是無聲的。改走 workspace lane
// （Manager.EmitWorkspace）之後才會進 notices → 合併進任何 focused pane 的
// timeline → Timeline.vue 渲染。前端那一半由
// frontend/src/registryUncertain.test.ts 斷言實際 DOM 內容。
//
// mutation：
//   - 拿掉 noteRegistryWriteResult 的 ErrRegistryUncertain 分支（落回 default）
//     → 紅在「必須留下 session_registry_uncertain 稽核」。
//   - 只寫 audit、不 emit → 紅在「必須發出使用者看得到的 stream_error」。
//   - 換回舊的 a.emit(session-scope、無 WSID) 形狀 → 紅在「必須走 workspace
//     lane」（scope 不是 workspace ＝ 前端會丟進 unrouted）。
//   - payload 只放 error、不放 component（或反之）→ 訊息仍渲染得出來，故**不**
//     在這裡斷言 component；前端的 summary() 契約由 Timeline.test.ts 守。
//   - 把訊息換成不含處置說明的字串 → 紅在「訊息必須說明該怎麼辦」。
func TestRegistryUncertainWriteIsFailLoudWithOwnSignal(t *testing.T) {
	a, ui := newTestApp(t)
	enableAudit(t, a)
	a.wsReg = &stubRegistry{mutateErr: wsregistry.ErrRegistryUncertain}

	a.commitSessionIdentity(appcore.WSID("w-uncertain"), contract.ProviderClaude, "sid", "label")

	if !auditHas(t, a.stateDir, "session_registry_uncertain") {
		t.Fatal("uncertain 必須留下 session_registry_uncertain 稽核（不得混進 restore_store_error）")
	}
	if auditHas(t, a.stateDir, "restore_store_error") {
		t.Fatal("uncertain 不得被記成一般的 restore store 錯誤")
	}

	// 兩個出口都撈：a.emit 的原始 UI 事件與 Manager 的 envelope 出口。這樣
	// 「有沒有發」與「形狀對不對」才是兩條各自獨立的斷言——只撈 Manager 那條的
	// 話，回到 a.emit 的 mutation 會紅在「必須發出」而不是紅在「必須走 workspace
	// lane」，讀起來像沒發訊號，掩蓋真正的缺陷。
	var got contract.Envelope
	envs := ui.findEnvKind(string(contract.KindStreamError))
	for _, ev := range ui.find("workbench:event") {
		if env, ok := ev.data.(contract.Envelope); ok {
			envs = append(envs, env)
		}
	}
	for _, env := range envs {
		if strings.Contains(string(env.Payload)+env.Error, "不確定") {
			got = env
		}
	}
	if got.EventID == "" {
		t.Fatal("uncertain 必須發出使用者看得到的 stream_error")
	}
	if got.Scope != "workspace" {
		t.Fatalf("必須走 workspace lane（session lane ＋空 WSID 會被前端 apply() 計進 unrouted 丟棄）：scope=%q wsid=%q",
			got.Scope, got.WorkspaceSessionID)
	}
	var payload map[string]string
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("payload 必須是前端讀得懂的 map：%v（%s）", err, got.Payload)
	}
	if !strings.Contains(payload["error"], "重啟") {
		t.Fatalf("訊息必須說明該怎麼辦（重啟 app）：%s", payload["error"])
	}
	if payload["wsid"] != "w-uncertain" {
		t.Fatalf("訊息要帶上是哪一個 session 的寫入失敗：%+v", payload)
	}
}

// TestShutdownSkipsRegistrySyncWhenUncertain：shutdown 總序第 12 步。
// 記憶體現況本身已經不知道是不是磁碟上那一份，再寫一次只會把未知固化成看起來
// 確定的樣子。跳過但留稽核（同 replay index 未驗證時跳過 checkpoint 的處置）。
//
// mutation：
//   - 拿掉 shutdown 的 registryUncertain 分支 → 紅在「不得再落盤」。
//   - 只跳過、不留稽核 → 紅在「跳過必須留稽核」。
func TestShutdownSkipsRegistrySyncWhenUncertain(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	reg := &stubRegistry{uncertain: true}
	a.wsReg = reg

	a.shutdown(context.Background())

	if n := reg.syncCount(); n != 0 {
		t.Fatalf("latch 期間 shutdown 不得再落盤 registry：Sync 被呼叫 %d 次", n)
	}
	if !auditHas(t, a.stateDir, "shutdown_registry_sync_skipped") {
		t.Fatal("被跳過的步驟不得無聲（Fail Loud）：需留 shutdown_registry_sync_skipped 稽核")
	}
}

// TestRegistryUncertainAuditCoversStubbableWrites：rev2 review I2 的守門。
//
// 情境是「latch 由**這一次**寫入設下」：早退 gate 讀的 `Uncertain()` 此刻仍是
// false（stub 不設 uncertain），所以呼叫進得去，寫入本身回哨兵。沒有這層稽核，
// post-mortem 只看得到之後一連串被拒絕的操作，答不出 latch 是何時、被哪一次
// 寫入設下的。
//
// **覆蓋範圍要說清楚（rev3 review Critical；C3 closeout 複查校正計數）**：
// `noteRegistryUncertainErr` 接了九個呼叫點，這條測試只守得住其中六個——凡是
// 經過 `a.wsReg`（sessionRegistry 介面，可換成 stub）**且**這裡實際驅動到的都
// 守得到：
//
//	create_put／create_rollback／reset_view／tombstone_persist／shutdown_sync／
//	legacy_flag_clear
//
// **另外三個 op 標籤目前零守門**，據實記在這裡：
//
//	resume_backfill／registry_load／set_layout
//
// resume_backfill／registry_load 直接吃具體型別 `*wsregistry.Store`
// （`BackfillResume` 不在介面上、`registry_load` 發生在 `a.wsReg` 接線之前），
// 而讓真實 Store 的步驟 4 失敗需要它 package-private 的注入鉤子，跨 package
// 取不到。把鉤子 export 進 production API 只為了測試，代價比這兩格的價值
// 高——登記為已知缺口，不要讀成「已覆蓋」。set_layout（`setPaneLayout`，
// app.go）雖然經 `a.wsReg`、理論上可用同一張表的手法驅動，但這裡尚未補上
// 對應列——一併登記為已知缺口，不是本次改動範圍。
//
// mutation（各自只打紅一列）：拿掉表中**任一**入口的 noteRegistryUncertainErr
// → 紅在該入口那一個 subtest。
func TestRegistryUncertainAuditCoversStubbableWrites(t *testing.T) {
	sentinel := wsregistry.ErrRegistryUncertain

	cases := []struct {
		name string
		op   string
		run  func(t *testing.T, a *App, reg *stubRegistry)
	}{
		{"CreateSession→Put", "create_put", func(t *testing.T, a *App, reg *stubRegistry) {
			reg.putErr = sentinel
			if _, err := a.CreateSession("claude", "x"); !errors.Is(err, sentinel) {
				t.Fatalf("前提：Put 回哨兵時 CreateSession 要回同一個錯誤，got %v", err)
			}
		}},
		{"CreateSession→DeleteUncommitted（回滾）", "create_rollback", func(t *testing.T, a *App, reg *stubRegistry) {
			// CommitCreate 失敗 → 走 DeleteUncommitted 回滾，而回滾本身撞上 latch。
			// deleteErr 要真的被走到：stub 的 DeleteUncommitted 對「entry 不存在」
			// 是冪等 no-op，所以必須讓 Put 先成功（不設 putErr）。
			a.hookForceCommitCreateError = errors.New("commit create boom")
			reg.mu.Lock()
			reg.deleteErr = sentinel
			reg.mu.Unlock()
			_, err := a.CreateSession("claude", "x")
			if !errors.Is(err, sentinel) {
				t.Fatalf("前提：回滾撞上哨兵時 CreateSession 要把它一起回報，got %v", err)
			}
		}},
		{"NewSession→ResetView", "reset_view", func(t *testing.T, a *App, reg *stubRegistry) {
			w := mustCreate(t, a, "claude")
			reg.mu.Lock()
			reg.mutateErr = sentinel
			reg.mu.Unlock()
			if err := a.NewSession(string(w)); !errors.Is(err, sentinel) {
				t.Fatalf("前提：ResetView 回哨兵時 NewSession 要回同一個錯誤，got %v", err)
			}
		}},
		{"RemoveSession→Remove", "tombstone_persist", func(t *testing.T, a *App, reg *stubRegistry) {
			w := mustCreate(t, a, "claude")
			reg.mu.Lock()
			reg.removeErr = sentinel
			reg.mu.Unlock()
			err := a.RemoveSession(string(w))
			if !errors.Is(err, sentinel) {
				t.Fatalf("前提：Remove 回哨兵時 RemoveSession 要回同一個錯誤，got %v", err)
			}
			// rev2 review M3：uncertain 時「可重試移除」是錯的指引——重試必然被
			// 開頭的 gate 擋掉，正確處置是重啟。
			if strings.Contains(err.Error(), "可重試移除") {
				t.Fatalf("uncertain 的移除失敗不得建議重試（重試必然再被拒）：%v", err)
			}
			if !strings.Contains(err.Error(), "重啟") {
				t.Fatalf("必須指出正確處置是重啟：%v", err)
			}
		}},
		{"shutdown→Sync", "shutdown_sync", func(t *testing.T, a *App, reg *stubRegistry) {
			reg.syncErr = sentinel
			a.shutdown(context.Background())
			if reg.syncCount() == 0 {
				t.Fatal("前提：latch 尚未設下時 shutdown 必須真的呼叫 Sync")
			}
		}},
		{"loadTurnsBefore→ClearLegacyTranscript", "legacy_flag_clear", func(t *testing.T, a *App, reg *stubRegistry) {
			// 三前提缺一不可（C3 brief）：entry 的 LegacyTranscript=true 且
			// ViewStartEventID 非空、events.jsonl 存在（mustCreate 之後即存在）、
			// window 為空（events.jsonl 未寫入任何無 WSID 事件）——齊備才會走到
			// 清旗標呼叫點。
			w := mustCreate(t, a, "claude")
			reg.mu.Lock()
			e := reg.entries[string(w)]
			e.LegacyTranscript = true
			e.ViewStartEventID = "0000000000"
			reg.entries[string(w)] = e
			reg.mutateErr = sentinel
			reg.mu.Unlock()
			if _, err := a.LoadTurnsBefore(string(w), "", 20); !errors.Is(err, sentinel) {
				t.Fatalf("前提：ClearLegacyTranscript 回哨兵時 loadTurnsBefore 要回同一個錯誤，got %v", err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			enableAudit(t, a)
			reg := &stubRegistry{}
			a.wsReg = reg
			tc.run(t, a, reg)
			if !auditHasOp(t, a.stateDir, "session_registry_uncertain", tc.op) {
				t.Fatalf("%s 首次設下 latch 時必須留下 session_registry_uncertain（op=%s）稽核", tc.name, tc.op)
			}
		})
	}
}

// auditHasOp：稽核裡有沒有某個 kind ＋ op 的組合。只看 kind 不夠——I2 要證明的
// 正是「不同入口各自留得下自己的那一筆」，共用一個 kind 斷言會讓任何一個入口
// 漏接都測不出來。
func auditHasOp(t *testing.T, stateDir, kind, op string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
			t.Fatalf("audit 壞行：%v（%s）", jerr, line)
		}
		if rec["kind"] != kind {
			continue
		}
		if d, ok := rec["data"].(map[string]any); ok && d["op"] == op {
			return true
		}
	}
	return false
}

// TestBackfillFailureStillReachesUserAfterEarlierWarning：rev2 review I1——
// 啟動期 backfill 落盤失敗（uncertain latch 是其中一種）的警告，**不得**被
// 較早那則「跳過 N 筆」吃掉。
//
// 原本的 noteStartupWarning 是 first-wins，而 loadSessionRegistry 的順序是
// 「先寫跳過警告 → 接線 wsReg → 呼叫 backfill」，所以 backfill 的警告必然排第二、
// 必然被丟棄：app 啟動成功、registry 已經停止寫入，而使用者一則相關訊息都沒有。
//
// 讓 backfill 一定失敗的方式：把暫存檔路徑用一個**目錄**佔住（O_CREATE 撞
// EISDIR）。這是 loadSessionRegistry 在這個 setup 下唯一還會發生的落盤
// （registry 檔已存在所以 Open 不寫、migrated 已為 true 所以不遷移），所以只
// 打中要測的那一步。**注意**：EISDIR 是步驟 1 的失敗，不是 dir-sync 的
// uncertain latch；這條守的是「後到的警告不得被吃掉」這件事本身，latch 只是
// 會走到同一行的其中一種原因。
//
// mutation：noteStartupWarning 改回 first-wins → 紅在「後到的警告不得被吃掉」。
func TestBackfillFailureStillReachesUserAfterEarlierWarning(t *testing.T) {
	dir := t.TempDir()
	a := newTestAppAt(t, dir)
	seedRegistryRaw(t, dir, map[string]map[string]any{
		"wX": {"wsid": "wX", "provider": "gemini", "created_at": "t0"}, // 觸發「跳過 1 筆」
		"w1": {"wsid": "w1", "provider": "claude", "created_at": "t1"},
	})
	if err := os.MkdirAll(filepath.Join(dir, "workspace-sessions.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := a.loadSessionRegistry(); err != nil {
		t.Fatalf("backfill 失敗不得阻擋啟動（它是續聊便利性，不是資料完整性）：%v", err)
	}
	if !auditHas(t, dir, "resume_backfill_failed") {
		t.Fatal("前提：backfill 必須真的失敗（否則這條測試量的是空集合）")
	}
	if !strings.Contains(a.startupErrText(), "跳過 1 筆") {
		t.Fatalf("前提：較早的警告要先寫進去：%q", a.startupErrText())
	}
	if !strings.Contains(a.startupErrText(), "續聊身分升級補寫失敗") {
		t.Fatalf("後到的警告不得被 first-wins 吃掉（Fail Loud）：%q", a.startupErrText())
	}
	// rev3 review I1：`.meta` 是單行 ellipsis，而第一則含完整絕對路徑（>100
	// 字元）；串在它後面等於在一般視窗寬度下被裁掉。真實路徑上也要驗排序。
	if strings.Index(a.startupErrText(), "續聊身分升級補寫失敗") > strings.Index(a.startupErrText(), "跳過 1 筆") {
		t.Fatalf("registry 這則必須排在「跳過 N 筆」前面，否則在單行版面上讀不到：%q", a.startupErrText())
	}
}

// TestErrRegistryUncertainKeepsUIMarker：跨語言字面值的漂移守門。
//
// 前端沒有別的辦法從一個 binding 錯誤**字串**判斷「這是 latch」——同步拒絕路徑
// （Create／Start／New／Remove）拿到的只有 error message，沒有結構化欄位。所以
// frontend/src/stores/session.ts 的 REGISTRY_UNCERTAIN_MARK 直接比對這個片語，
// 而 App.vue 據此對 latch 做 one-shot 強制展開 timeline。
//
// 沒有這條測試，改一個字就會讓前端那條路徑靜默失效（訊息照樣顯示，但 latch 的
// 強制展開不再觸發）——典型的「保證只寫在註解裡」。
//
// mutation：改動 errRegistryUncertain 的這段字 → 紅在下面那一行，訊息直接指出
// 要同步改哪一個前端檔案。
func TestErrRegistryUncertainKeepsUIMarker(t *testing.T) {
	// 必須與 frontend/src/stores/session.ts 的 REGISTRY_UNCERTAIN_MARK 逐字相同。
	const uiMarker = "session registry 上一次寫入的結果不確定"
	if !strings.Contains(errRegistryUncertain.Error(), uiMarker) {
		t.Fatalf("errRegistryUncertain 必須含前端用來辨識 latch 的片語 %q；"+
			"改動訊息時要同步改 frontend/src/stores/session.ts 的 REGISTRY_UNCERTAIN_MARK。\ngot: %s",
			uiMarker, errRegistryUncertain.Error())
	}
}

// TestStartupBlockerSortsBeforeBenignWarning：rev3 review I1——`.meta` 是單行
// ellipsis 版面，串在後面的句子在一般視窗寬度下會被裁掉。所以排序必須是**嚴重度
// 優先**，不是時序：registry 停止寫入／載入失敗這類要排在「跳過 N 筆」前面。
//
// mutation：noteStartupBlocker 改成單純 append → 紅在「blocker 必須排在良性
// 警告前面」。
func TestStartupBlockerSortsBeforeBenignWarning(t *testing.T) {
	a := &App{}
	a.noteStartupWarning("session registry: 跳過 1 筆無法還原的 entry（未刪除，仍在 /very/long/path/workspace-sessions.json）")
	a.noteStartupBlocker("session registry: 續聊身分升級補寫失敗")
	a.noteStartupWarning("replay index 開啟失敗")

	blocker := strings.Index(a.startupErrText(), "續聊身分升級補寫失敗")
	benign := strings.Index(a.startupErrText(), "跳過 1 筆")
	if blocker < 0 || benign < 0 {
		t.Fatalf("兩則都必須留著（累積，不得丟棄）：%q", a.startupErrText())
	}
	if blocker > benign {
		t.Fatalf("blocker 必須排在良性警告前面（.meta 是單行 ellipsis，後面的會被裁掉）：%q", a.startupErrText())
	}
	if !strings.Contains(a.startupErrText(), "replay index 開啟失敗") {
		t.Fatalf("後到的良性警告仍不得被丟棄：%q", a.startupErrText())
	}
}
