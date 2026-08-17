package wsregistry

import (
	"os"
	"reflect"
	"sync"
	"testing"
)

// ---- SetLayout 的冪等守衛（owner 2026-08-17 裁決）----
//
// owner 指定守門至少要證明三件事，本檔逐條對應一個測試：
//  1. 相同 layout：零落盤步驟、檔案 mtime 不變 → TestSetLayoutSkipsPersistWhenUnchanged
//  2. layout 有變：恰好執行一次四步落盤 → TestSetLayoutPersistsExactlyOnceWhenChanged
//  3. 比對與更新在同一把 registry 鎖內 → TestSetLayoutComparesUnderTheSameLock
//     （**確定性**：經 compareHook 在比對當下量持鎖狀態與呼叫次數）＋
//     TestSetLayoutPersistsOnceUnderConcurrentIdenticalWrites（併發下的落盤次數）
//
// 第 1 條的判準刻意**量磁碟事實**而不是「有沒有呼叫某個函式」：除了步驟計數，
// 還比對 os.SameFile（dev+ino）與 ModTime。四步落盤是 temp file ＋ atomic
// rename，**必然**換掉目標路徑的 inode，所以 SameFile 為真就是「這次真的沒碰
// 檔案」的直接證據，不依賴時間戳解析度。

// layoutFileID：目標檔的身分與修改時間快照。
func layoutFileID(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// TestSetLayoutSkipsPersistWhenUnchanged：判準 1。相同排列不得產生任何落盤
// 步驟，磁碟上的檔案必須原封不動（同一個 inode、同一個 mtime）。
//
// 這也是 owner「釘選判準 3」的**零落盤那一半**：使用者重複釘選一個已經在該格、
// 且該格已 focused 的 session 時，前端送出的 layout 逐字相同（前端側的守門見
// paneLayout.test.ts 的 `判準 3：重複釘選同一格送出逐字相同的 layout；切格則
// 不同`），由這裡的守衛攔下。**注意對照組在 PersistsExactlyOnceWhenChanged**：
// 「pins 相同但 focus 不同」是**該落盤**的，不得被守衛吃掉。
//
// 跨重啟（形狀 F）：被跳過的那一次寫入不得讓磁碟內容退化——重新 Open 一個
// Store（真的重讀磁碟）必須仍拿到正確的排列。「不寫」的正確性只有在重啟後
// 才看得出來：一個把 layout 寫壞又跳過修正的實作，在同一個 process 內用記憶體
// 讀回來永遠是對的。
//
// **斷言不得用 layoutEqual 當比對器**（rev2 review：自我指涉的 oracle）——
// layoutEqual 正是受測對象，「layoutEqual 恆 true」那一刀會讓斷言恆真，整條
// 測試連紅都不會紅（實測 PASS）。這裡一律用 reflect.DeepEqual。
//
// mutation（各自只打紅一列）：
//   - 拿掉 SetLayout 的 layoutEqual 早退 → 紅在「不得產生任何落盤步驟」。
//   - layoutEqual 改成恆 true（永不寫入）→ 紅在跨重啟那段（磁碟上是空排列）。
func TestSetLayoutSkipsPersistWhenUnchanged(t *testing.T) {
	s, path := openStore(t)
	want := Layout{Pins: []string{"w1", "w2"}, Focused: "w2"}
	if err := s.SetLayout(want); err != nil {
		t.Fatalf("前提：第一次寫入要成功，got %v", err)
	}
	before := layoutFileID(t, path)

	seen := recordSteps(s)
	// 逐字相同、但是**另一個 slice 實例**——守衛比的必須是內容，不是指標。
	if err := s.SetLayout(Layout{Pins: []string{"w1", "w2"}, Focused: "w2"}); err != nil {
		t.Fatalf("相同排列必須成功回傳（no-op 不是錯誤），got %v", err)
	}
	if steps := seen(); len(steps) != 0 {
		t.Fatalf("相同排列不得產生任何落盤步驟，got %v", steps)
	}

	after := layoutFileID(t, path)
	if !os.SameFile(before, after) {
		t.Fatal("相同排列不得重寫檔案：目標路徑換了 inode（代表發生過 atomic rename）")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("相同排列不得重寫檔案：mtime 變了（%v → %v）", before.ModTime(), after.ModTime())
	}

	// 跨重啟：新的 Store 實例、重讀磁碟。
	s.ForceStepHookForTest(nil)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Layout(); !reflect.DeepEqual(got, want) {
		t.Fatalf("被跳過的寫入不得影響磁碟內容：重啟後 got %+v want %+v", got, want)
	}
}

// TestSetLayoutPersistsExactlyOnceWhenChanged：判準 2。**任何**一項不同都算變更，
// 且只跑一次完整四步。
//
// 三種變更形狀分開驗，是為了讓「只比 Pins」或「只比 Focused」的偷懶守衛各自有一
// 條會紅的測試——只驗「兩者都變」的話，兩種偷懶實作都照樣全綠。
//
// mutation（各自只打紅一列）：
//   - layoutEqual 只比 Pins（忽略 Focused）→ 紅在 `只有 focused 變`。
//   - layoutEqual 只比 Focused（忽略 Pins）→ 紅在 `只有 pins 變`。
//   - 守衛改成恆 true（永不寫入）→ 三個 subtest 全紅（步驟數 0）。
func TestSetLayoutPersistsExactlyOnceWhenChanged(t *testing.T) {
	base := Layout{Pins: []string{"w1", "w2"}, Focused: "w2"}
	cases := []struct {
		name string
		next Layout
	}{
		{"只有 pins 變", Layout{Pins: []string{"w1", "w3"}, Focused: "w2"}},
		{"只有 focused 變", Layout{Pins: []string{"w1", "w2"}, Focused: "w1"}},
		{"兩者都變", Layout{Pins: []string{"w9", ""}, Focused: "w9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openStore(t)
			if err := s.SetLayout(base); err != nil {
				t.Fatal(err)
			}
			before := layoutFileID(t, path)

			seen := recordSteps(s)
			if err := s.SetLayout(tc.next); err != nil {
				t.Fatal(err)
			}

			want := []fsyncStep{stepWrite, stepFileSync, stepRename, stepDirSync}
			got := seen()
			if len(got) != len(want) {
				t.Fatalf("排列有變必須恰好跑一次四步落盤，got %v", got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("四步順序不符：got %v want %v", got, want)
				}
			}
			if os.SameFile(before, layoutFileID(t, path)) {
				t.Fatal("排列有變必須真的 atomic rename 換掉目標檔（inode 應改變）")
			}
		})
	}
}

// TestSetLayoutPersistsOnceUnderConcurrentIdenticalWrites：判準 3 的併發面——
// N 個 goroutine 同時要求改成**同一個**新值，只能落盤一次。不用 sleep 或時間
// 假設（barrier 測試不得依賴 time.Sleep），一起 close(start) 放行即可。
//
// **這條守得到什麼、守不到什麼（據實寫，不要讀成「原子性已完全覆蓋」）**：
//
//   - **守得到（確定性）**：守衛整個消失 → 步驟數變成 4×N（實測 32），必紅。
//     這是併發下的迴歸防線。
//   - **守得到（確定性）**：最終記憶體與磁碟排列一致且等於目標值。
//   - **這條本身只守得到機率性**：把比對搬到鎖外、或改成兩段式取鎖，本測試都
//     不保證會紅（純黑箱沒有確定性判別點——SetLayout 的記憶體更新早於落盤）。
//     **那兩刀由 TestSetLayoutComparesUnderTheSameLock 確定性接手**，本條只是
//     併發下的第二道。
//
// mutation：拿掉守衛 → 確定性紅在「恰好一次」（4 → 32 步）。
func TestSetLayoutPersistsOnceUnderConcurrentIdenticalWrites(t *testing.T) {
	s, path := openStore(t)
	if err := s.SetLayout(Layout{Pins: []string{"w1", "w2"}, Focused: "w1"}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var steps []fsyncStep
	s.ForceStepHookForTest(func(st fsyncStep) error {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, st)
		return nil
	})

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// 每個 goroutine 自己造 slice：守衛比的是內容，而 SetLayout 深拷貝
			// 的語意也要在併發下成立。
			errs[i] = s.SetLayout(Layout{Pins: []string{"w3", "w4"}, Focused: "w4"})
		}(i)
	}
	close(start) // 一起放行，不用 sleep 對齊
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	mu.Lock()
	got := append([]fsyncStep(nil), steps...)
	mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("%d 個併發的「改成同一個新值」必須只落盤一次（4 步），got %d 步：%v", n, len(got), got)
	}

	want := Layout{Pins: []string{"w3", "w4"}, Focused: "w4"}
	if cur := s.Layout(); !reflect.DeepEqual(cur, want) {
		t.Fatalf("記憶體排列 got %+v want %+v", cur, want)
	}
	s.ForceStepHookForTest(nil)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur := reopened.Layout(); !reflect.DeepEqual(cur, want) {
		t.Fatalf("重啟後排列 got %+v want %+v", cur, want)
	}
}

// TestSetLayoutComparesUnderTheSameLock：判準 3 的**確定性**守門。
//
// rev2 review 推翻了「判準 3 沒有確定性守法」這個結論，並給了可運作的原型：
// 判準 3 是**結構性**要求（「在同一把鎖內」），用 repo 自己的白箱注入慣例守它
// 才是 owner 判準的字面意思。做法——在比對點掛一個觀測 hook，於 hook 內：
//
//  1. `s.mu.TryLock()` 探測**比對當下**有沒有人持鎖。sync.Mutex 不可重入，
//     所以「自己正持著」時必回 false；回 true 就代表比對跑在鎖外。
//     （TryLock 成功時必須立刻 Unlock，否則後面真正的 Lock 會死鎖。）
//  2. 記錄呼叫次數：比對必須是 SetLayout 內**唯一**的決策點，每次呼叫剛好一次。
//     繞過 hook 另做一份比對的實作會在這裡露餡（次數變 0）。
//
// 單執行緒、無 time.Sleep、不依賴 -race、不依賴排程。
//
// 相同輸入（走早退）與不同輸入（走落盤）兩條路都要驗——只驗一條的話，把守衛
// 搬到鎖外但保留鎖內第二次比對之類的實作會漏掉。
//
// mutation（實測）：
//   - 比對（連同 hook）搬到 s.mu.Lock() 之前 → 紅在「必須與更新在同一把鎖內」。
//   - 改成兩段式（先 s.Layout() 比對、再取鎖寫入），hook 隨比對走 → 紅在同一行。
//   - 兩段式且繞過 hook → 紅在「比對必須是唯一的決策點」（次數 0）。
func TestSetLayoutComparesUnderTheSameLock(t *testing.T) {
	s, _ := openStore(t)
	if err := s.SetLayout(Layout{Pins: []string{"w1", ""}, Focused: "w1"}); err != nil {
		t.Fatalf("前提：第一次寫入要成功，got %v", err)
	}

	calls := 0
	heldAtCompare := []bool{}
	s.ForceCompareHookForTest(func() {
		calls++
		if s.mu.TryLock() { // 拿得到＝比對當下沒有人持鎖
			s.mu.Unlock()
			heldAtCompare = append(heldAtCompare, false)
			return
		}
		heldAtCompare = append(heldAtCompare, true)
	})

	// 兩條路都要走到：相同（早退）與不同（落盤）
	if err := s.SetLayout(Layout{Pins: []string{"w1", ""}, Focused: "w1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLayout(Layout{Pins: []string{"w2", ""}, Focused: "w2"}); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("比對必須是 SetLayout 內唯一的決策點（每次呼叫剛好一次），got %d", calls)
	}
	for i, held := range heldAtCompare {
		if !held {
			t.Fatalf("判準 3：冪等比對必須與更新在同一把 registry 鎖內，實際在鎖外（第 %d 次呼叫）", i+1)
		}
	}
}

// ---- 測試專用注入器（同 fsync_test.go 的 ForceStepHookForTest 慣例：欄位在
// production 檔宣告、注入器只存在於 _test.go）----

// ForceCompareHookForTest：裝上 SetLayout 冪等比對點的觀測鉤子。傳 nil 清除。
func (s *Store) ForceCompareHookForTest(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compareHook = hook
}
