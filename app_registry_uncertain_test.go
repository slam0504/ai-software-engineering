package main

import (
	"context"
	"encoding/json"
	"errors"
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
