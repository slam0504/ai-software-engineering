package main

import (
	"errors"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/wsregistry"
)

// ---- M3b owner review 修正 3：pane pins 持久化（spec §3.2.1 白名單、§3.8 啟動
// 只重建兩個釘選 pane）----
//
// **這批測試的核心風險是失效形狀 (F)：跨重啟那一維沒守。** pins 的全部價值都在
// 重啟後——同一個 App 實例先 Set 再 Get 只證明記憶體會記住自己剛寫的東西，那條
// 路徑就算完全沒碰磁碟也會綠。所以主要保證一律經 restartApp（新的 App、新的
// wsregistry.Store、從磁碟重讀）驗收。

// TestPaneLayoutSurvivesRestart：整張票的主要保證——使用者釘選的兩個 pane 與焦點
// 在重啟後仍在，這是 §3.8 啟動重建的唯一輸入。
//
// mutation：SetPaneLayout 改成不呼叫 wsReg.SetLayout（只回 nil）→ 紅在本條的
// 「重啟後必須還原兩個釘選 pane」。這一刀**同時打紅四條**（本條、
// RestoresEmptySecondPane、DropsRemovedPin、UncertainLatch 的排列比對）——寫入
// 路徑是這一組的共同前提，據實記在這裡，不宣稱它只打紅一條。各條的「只打紅
// 自己」mutation 分別列在各自的 doc 上。
func TestPaneLayoutSurvivesRestart(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w1 := mustCreate(t, a, "claude")
	w2 := mustCreate(t, a, "codex")

	if err := a.SetPaneLayout([]string{string(w1), string(w2)}, string(w2)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	b, _ := restartApp(t, a)
	got, err := b.PaneLayout()
	if err != nil {
		t.Fatalf("重啟後 PaneLayout: %v", err)
	}
	if len(got.Pins) != 2 || got.Pins[0] != string(w1) || got.Pins[1] != string(w2) {
		t.Fatalf("重啟後必須還原兩個釘選 pane，got %+v（want [%s %s]）", got.Pins, w1, w2)
	}
	if got.Focused != string(w2) {
		t.Fatalf("重啟後必須還原 focused pane，got %q want %q", got.Focused, w2)
	}
}

// TestPaneLayoutRestoresEmptySecondPane：只釘一格的排列同樣要跨重啟——空字串是
// 「這格沒有釘選」的正式表示，不是「沒設定」。
//
// 為什麼獨立一條：只驗兩格都有的情況，會漏掉「pins 長度被實作截短／空格被丟掉」
// 這一類錯誤（第二格是空的時候，把它整個省略掉的實作在上一條測試裡看不出來，
// 卻會讓還原時的 pane index 錯位）。
//
// mutation：PaneLayout 改成只回傳非空的 WSID（跳過空格）→ 紅在「空的第一格
// 必須原位保留」（pins[1] 變成 w1、長度變 1）。
func TestPaneLayoutRestoresEmptySecondPane(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w1 := mustCreate(t, a, "claude")

	if err := a.SetPaneLayout([]string{"", string(w1)}, string(w1)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	b, _ := restartApp(t, a)
	got, err := b.PaneLayout()
	if err != nil {
		t.Fatalf("重啟後 PaneLayout: %v", err)
	}
	if len(got.Pins) != 2 || got.Pins[0] != "" || got.Pins[1] != string(w1) {
		t.Fatalf("空的第一格必須原位保留，got %+v", got.Pins)
	}
	if got.Focused != string(w1) {
		t.Fatalf("focused 應為 %q，got %q", w1, got.Focused)
	}
}

// TestPaneLayoutDropsRemovedPin：被移除（tombstone）的 session 不得在重啟後被
// 釘回來——§3.6.1 的 removed「不顯示、不自動恢復」延伸到 pane 排列這一層。
//
// 這條不是假想情況：移除成功但 SetPaneLayout 隨後失敗（latch／磁碟滿），
// registry 就會留著一個指向 tombstone 的 pin。
//
// mutation（各自只打紅一列）：
//   - 拿掉 PaneLayout 的 RemovedAt 檢查 → 紅在「tombstone 不得被釘回來」。
//   - 拿掉 focused 的降級（focused 不在 pins 之中就清空）→ 紅在 focused 那一行。
func TestPaneLayoutDropsRemovedPin(t *testing.T) {
	a, _ := newTestApp(t)
	bootRegistry(t, a)
	w1 := mustCreate(t, a, "claude")
	w2 := mustCreate(t, a, "codex")
	if err := a.SetPaneLayout([]string{string(w1), string(w2)}, string(w2)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}
	// registry 直接下 tombstone（不走 RemoveSession 的 teardown 全序）：這條要驗
	// 的是「PaneLayout 讀到 tombstone 怎麼處置」，不是移除流程本身。
	if err := a.wsReg.Remove(string(w2), "test"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	b, _ := restartApp(t, a)
	got, err := b.PaneLayout()
	if err != nil {
		t.Fatalf("重啟後 PaneLayout: %v", err)
	}
	if got.Pins[1] != "" {
		t.Fatalf("已 tombstone 的 session 不得被釘回來，got %+v", got.Pins)
	}
	if got.Pins[0] != string(w1) {
		t.Fatalf("另一格不受影響，got %+v", got.Pins)
	}
	if got.Focused != "" {
		t.Fatalf("focused 指向已被濾掉的那一格時必須降級成空，got %q", got.Focused)
	}
}

// TestSetPaneLayoutRefusedByUncertainLatchButReadStillWorks：latch 期間的處置。
//
// 兩件事要同時成立：
//  1. 寫入 **fail loud**（回 errRegistryUncertain，訊息帶前端據以撥 latchSeq 的
//     片語）——不得靜默吞掉，否則使用者以為排列存好了。
//  2. 讀取 **照常放行**——latch 的是 durability 不是記憶體內容；擋掉讀取只會讓
//     latch 之後的重啟連上一次成功寫入的排列都拿不到。
//
// 「不擋路」那一半在前端（persistLayout 失敗只發 notice、不回滾釘選），由
// paneLayout.test.ts 守。
//
// mutation（各自只打紅一列）：
//   - 拿掉 SetPaneLayout 的 registryUncertain 早退 → 紅在「latch 期間必須拒絕」
//     （stub 的 SetLayout 不會失敗，會一路成功）。
//   - 把 latch 檢查加進 PaneLayout → 紅在「唯讀入口必須放行」。
func TestSetPaneLayoutRefusedByUncertainLatchButReadStillWorks(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	w1 := mustCreate(t, a, "claude")
	if err := a.SetPaneLayout([]string{string(w1), ""}, string(w1)); err != nil {
		t.Fatalf("前提：latch 之前的寫入要成功，got %v", err)
	}

	reg.mu.Lock()
	reg.uncertain = true
	reg.mu.Unlock()

	err := a.SetPaneLayout([]string{"", string(w1)}, string(w1))
	if !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("latch 期間 SetPaneLayout 必須拒絕，got %v", err)
	}
	if !errors.Is(err, wsregistry.ErrRegistryUncertain) {
		t.Fatalf("錯誤必須保留 wsregistry 哨兵（前端據訊息片語撥 latchSeq），got %v", err)
	}

	got, rerr := a.PaneLayout()
	if rerr != nil {
		t.Fatalf("唯讀入口必須放行：%v", rerr)
	}
	if got.Pins[0] != string(w1) || got.Pins[1] != "" {
		t.Fatalf("被拒絕的寫入不得改變已持久化的排列，got %+v", got.Pins)
	}
}

// TestSetPaneLayoutReportsPlainWriteFailure：**非 latch** 的一般寫入失敗（磁碟
// 滿、權限、rename 失敗）必須原樣回報，不得被吞掉、也不得被誤報成 latch。
//
// 為什麼要跟 latch 那條分開（review #6）：兩者的處置不同——latch 是「早退、之後
// 每次都會失敗、要重啟」，一般失敗是「這一次沒寫成、下一次可能成功」。只測 latch
// 分支的話，`SetPaneLayout` 把 `wsReg.SetLayout` 的錯誤吞掉改回 nil 也照樣全綠，
// 而前端的「fail loud 但不擋路」整條就沒有東西可接。
//
// mutation（各自只打紅一列）：
//   - `SetPaneLayout` 改成 `_ = a.wsReg.SetLayout(...); return nil` → 紅在「必須
//     原樣回報」。
//   - 把一般失敗也包成 errRegistryUncertain → 紅在「不得誤報成 latch」。
func TestSetPaneLayoutReportsPlainWriteFailure(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	boom := errors.New("write layout: no space left on device")
	reg.mu.Lock()
	reg.layoutErr = boom
	reg.mu.Unlock()

	err := a.SetPaneLayout([]string{"w1", ""}, "w1")
	if !errors.Is(err, boom) {
		t.Fatalf("一般寫入失敗必須原樣回報（前端據此發 notice），got %v", err)
	}
	if errors.Is(err, errRegistryUncertain) {
		t.Fatalf("一般寫入失敗不得誤報成 latch（處置不同：latch 要重啟），got %v", err)
	}
}

// TestSetPaneLayoutRejectsMalformedShape：兩條結構前提。
//
// 為什麼要擋而不是照收：pins 超過 2 格代表呼叫端已經不是 §3.7 的雙 pane 模型；
// focused 不在 pins 之中則會在下一次啟動還原出一個指向不存在 pane 的焦點——兩者
// 都是寫進去才會在重啟時發作的錯誤，當場擋下才有機會被看見。
//
// mutation（各自只打紅一列）：拿掉任一 guard → 紅在對應的 subtest，且
// 「不得寫進 registry」那一行同時證明它真的是早退、不是回錯之後還寫了。
func TestSetPaneLayoutRejectsMalformedShape(t *testing.T) {
	cases := []struct {
		name    string
		pins    []string
		focused string
	}{
		{"超過兩個 pane", []string{"w1", "w2", "w3"}, "w1"},
		{"focused 不在 pins 之中", []string{"w1", ""}, "w2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			reg := &stubRegistry{}
			a.wsReg = reg
			if err := a.SetPaneLayout(tc.pins, tc.focused); err == nil {
				t.Fatal("結構不合法的排列必須 fail loud")
			}
			if got := reg.Layout(); len(got.Pins) != 0 || got.Focused != "" {
				t.Fatalf("被拒絕的排列不得寫進 registry，got %+v", got)
			}
		})
	}
}

// TestSetPaneLayoutRefusedAfterShutdownBarrier：shutdown 之後的寫入必須被柵欄
// 擋下。§3.6.5 把 `session registry Sync` 凍結為總序的最後一步，晚於它抵達的
// 寫入不是遺失就是把已 flush 的內容再弄髒。
//
// mutation：拿掉 SetPaneLayout 的 beginAppTxn → 紅在這裡（stub 的 SetLayout 會
// 照常成功）。
func TestSetPaneLayoutRefusedAfterShutdownBarrier(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg

	a.shutMu.Lock()
	a.phase = phaseShuttingDown
	a.shutMu.Unlock()

	if err := a.SetPaneLayout([]string{"w1", ""}, "w1"); err == nil {
		t.Fatal("shutdown 之後的 SetPaneLayout 必須被柵欄拒絕")
	}
	if got := reg.Layout(); len(got.Pins) != 0 {
		t.Fatalf("被柵欄拒絕的寫入不得落到 registry，got %+v", got)
	}
}

// TestPaneLayoutWithoutRegistryFailsLoud：registry 未接線（malformed／載入失敗）
// 時兩個入口都回 errNoSessionRegistry，與 ListSessions／CreateSession 同一個錯誤
// ——UI 顯示的是「registry 不可用」而不是「沒有釘選」。
//
// mutation：把 PaneLayout 的 nil 檢查拿掉 → panic（nil 介面呼叫），紅在這裡。
func TestPaneLayoutWithoutRegistryFailsLoud(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = nil
	if _, err := a.PaneLayout(); !errors.Is(err, errNoSessionRegistry) {
		t.Fatalf("PaneLayout 應回 errNoSessionRegistry，got %v", err)
	}
	if err := a.SetPaneLayout([]string{"w1", ""}, "w1"); !errors.Is(err, errNoSessionRegistry) {
		t.Fatalf("SetPaneLayout 應回 errNoSessionRegistry，got %v", err)
	}
}
