package wsregistry

import (
	"os"
	"sync"
	"testing"
)

// ---- SetLayout 的冪等守衛（owner 2026-08-17 裁決）----
//
// owner 指定守門至少要證明三件事，本檔逐條對應一個測試：
//  1. 相同 layout：零落盤步驟、檔案 mtime 不變 → TestSetLayoutSkipsPersistWhenUnchanged
//  2. layout 有變：恰好執行一次四步落盤 → TestSetLayoutPersistsExactlyOnceWhenChanged
//  3. 比對與更新在同一把 registry 鎖內 → TestSetLayoutPersistsOnceUnderConcurrentIdenticalWrites
//     （**覆蓋範圍有限，見該測試的 doc**：確定性守得到「守衛整個消失」，鎖外比對
//     那個變體只由 `-race` 機率性攔到，兩段式取鎖的變體守不到）
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
// 跨重啟（形狀 F）：被跳過的那一次寫入不得讓磁碟內容退化——重新 Open 一個
// Store（真的重讀磁碟）必須仍拿到正確的排列。「不寫」的正確性只有在重啟後
// 才看得出來：一個把 layout 寫壞又跳過修正的實作，在同一個 process 內用記憶體
// 讀回來永遠是對的。
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
	if got := reopened.Layout(); !layoutEqual(got, want) {
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
//   - **只守得到機率性**：把比對搬到 `s.mu.Lock()` 之前（鎖外讀 `s.file.Layout`）
//     是 data race，靠 repo gate 的 `-race` 攔——但**是否觸發取決於排程**，實測
//     5 次有 3 次報 race。原因是 SetLayout 的**記憶體更新早於落盤**，等到有人能
//     觀察到差異時狀態早已是新值，因此沒有確定性的行為判別點。
//   - **守不到**：把比對改成先呼叫 `s.Layout()`（自己會取鎖）再另外取鎖寫入的
//     兩段式實作——沒有 data race，也沒有可觀測的狀態差異，最壞只是多寫一次
//     相同內容（是最佳化失效，不是狀態錯誤）。登記為已知未覆蓋。
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
	if cur := s.Layout(); !layoutEqual(cur, want) {
		t.Fatalf("記憶體排列 got %+v want %+v", cur, want)
	}
	s.ForceStepHookForTest(nil)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if cur := reopened.Layout(); !layoutEqual(cur, want) {
		t.Fatalf("重啟後排列 got %+v want %+v", cur, want)
	}
}
