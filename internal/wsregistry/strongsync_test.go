package wsregistry

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// ---- M3b Task 2a rev2：Darwin strong-sync（F_FULLFSYNC）的守門 ----
//
// **這批測試量得到什麼、量不到什麼（失效形狀 (E)，先講清楚）**：
// 測試環境沒有斷電、也沒有辦法從 process 內觀測「資料是否真的離開硬碟的揮發性
// 快取」。所以這裡**沒有任何一條測試證明 F_FULLFSYNC 真的更持久**——那要真實的
// power-cut 實驗才驗得到，而且 Apple 本身也不承諾突然斷電下絕對不遺失。
//
// 可觀測、因此有守的三件事：
//  1. 兩個 barrier（暫存檔內容、rename 後的父目錄項）**都**走 strongSync，
//     且落在四步序列的正確位置（TestBothDurabilityBarriersGoThroughStrongSync）；
//  2. 平台分流真的分到對的實作（TestStrongSyncNameMatchesPlatform）；
//  3. barrier 失敗時**不得靜默退回較弱的 fsync 之後仍回報成功**，且兩個位置的
//     失敗處置不同（TestStrongSyncFailureIsNotSilentlyDowngraded）。
//
// 「strong-sync 測試綠」不得被讀成「斷電安全已保證」。

// ForceStrongSyncForTest：strongSyncFn 的測試專用替換（同 ForceStepHookForTest
// 慣例：注入宣告在 _test.go，production 檔只留接點）。回傳的 restore 必須以
// defer 呼叫——strongSyncFn 是 package 級變數，殘留會污染同 package 的其他測試。
func ForceStrongSyncForTest(fn func(*os.File) error) (restore func()) {
	prev := strongSyncFn
	strongSyncFn = fn
	return func() { strongSyncFn = prev }
}

// TestStrongSyncNameMatchesPlatform：build tag 分流真的分到對的實作。
//
// mutation：把 strongsync_darwin.go 的 build tag 改成 `//go:build ignore`
// （或兩個檔案的 tag 互換）→ 紅在「darwin 必須走 F_FULLFSYNC」。
func TestStrongSyncNameMatchesPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if strongSyncName != "fcntl(F_FULLFSYNC)" {
			t.Fatalf("darwin 必須走 F_FULLFSYNC，got %q", strongSyncName)
		}
		return
	}
	if strongSyncName != "fsync" {
		t.Fatalf("非 darwin 平台走 fsync，got %q", strongSyncName)
	}
}

// TestBothDurabilityBarriersGoThroughStrongSync：**呼叫序列守門**。
//
// 四步 hook 與 strongSync 記錄器合併成同一條時間軸，斷言逐格相符：
//
//	step:write → step:file-sync → strong-sync → step:rename →
//	step:directory-sync → strong-sync
//
// 這條測試同時守住 owner rev2 的第 1 與第 2 點：
//   - 暫存檔內容走 strong-sync（第 3 格）；
//   - rename **之後**的目錄項也走同一強度的 barrier（第 6 格）——只有前者
//     就等於留著同一個洞。
//
// mutation（各自只打紅這一條、且紅在正題那一行）：
//   - writeTempLocked 把 strongSync(f) 改回 f.Sync() → 第 3 格消失，紅在
//     「barrier 呼叫序列不符」。
//   - syncDir 把 strongSync(d) 改回 d.Sync() → 第 6 格消失，同一行紅。
//   - 把 syncDir 移到 rename 之前 → 序列變成 …directory-sync strong-sync
//     rename，同一行紅。
func TestBothDurabilityBarriersGoThroughStrongSync(t *testing.T) {
	s, _ := openStore(t)

	var seq []string
	s.ForceStepHookForTest(func(step fsyncStep) error {
		seq = append(seq, "step:"+string(step))
		return nil
	})
	restore := ForceStrongSyncForTest(func(f *os.File) error {
		seq = append(seq, "strong-sync")
		return platformStrongSync(f) // 仍走真實 barrier，不把受測路徑換成假的
	})
	defer restore()

	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"step:write",
		"step:file-sync", "strong-sync", // 步驟 2：暫存檔內容
		"step:rename",
		"step:directory-sync", "strong-sync", // 步驟 4：rename 後的目錄項
	}
	if strings.Join(seq, " ") != strings.Join(want, " ") {
		t.Fatalf("barrier 呼叫序列不符：\n got %v\nwant %v", seq, want)
	}
}

// TestStrongSyncFailureIsNotSilentlyDowngraded：owner rev2 的第 3 點——
// F_FULLFSYNC 不支援／失敗時，**不得**退回較弱的 fsync 之後仍回報成功。
//
// 用 ENOTSUP（「這個檔案系統不支援 F_FULLFSYNC」的真實形狀）注入，兩個位置分別
// 斷言，因為兩者的正確處置不同：
//   - 暫存檔那次失敗 → rename 還沒發生 → 回錯 ＋ 回滾記憶體 ＋ **不** latch；
//   - 目錄項那次失敗 → rename 已成功 → 回 ErrRegistryUncertain ＋ latch ＋
//     **不**回滾。
//
// mutation（各自只打紅一行）：
//   - platformStrongSync 在錯誤時改成 `return f.Sync()`（靜默 fallback）→
//     紅在「barrier 失敗必須回錯，不得退回較弱的 fsync 再報成功」。
//   - writeTempLocked 吞掉 barrier 錯誤（只記不回）→ 同上那一行紅。
//   - persistLocked 把 dir barrier 的錯誤當普通錯誤（不 latch）→ 紅在
//     「目錄項 barrier 失敗必須 latch uncertain」。
func TestStrongSyncFailureIsNotSilentlyDowngraded(t *testing.T) {
	notSup := errors.New("fcntl(F_FULLFSYNC) failed: operation not supported")

	t.Run("暫存檔 barrier 失敗＝commit 沒發生", func(t *testing.T) {
		s, path := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
		restore := ForceStrongSyncForTest(func(*os.File) error { return notSup })
		defer restore()

		err := s.SetResume("w1", "new")
		if err == nil {
			t.Fatal("barrier 失敗必須回錯，不得退回較弱的 fsync 再報成功")
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("錯誤要保留原因（不支援 vs 一般 EIO 要分得開）：%v", err)
		}
		if s.Uncertain() {
			t.Fatal("暫存檔 barrier 失敗時 rename 尚未執行，不得 latch uncertain")
		}
		if e, _ := s.Get("w1"); e.ResumeSessionID != "old" {
			t.Fatalf("記憶體必須退回舊值，got %q", e.ResumeSessionID)
		}
		if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "old" {
			t.Fatalf("磁碟必須維持舊值，got %q", e.ResumeSessionID)
		}
	})

	t.Run("目錄項 barrier 失敗＝commit 結果不確定", func(t *testing.T) {
		s, path := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", ResumeSessionID: "old", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
		// 只讓**第二次**（目錄項那次）失敗：暫存檔那次照走真實 barrier。
		var n int
		restore := ForceStrongSyncForTest(func(f *os.File) error {
			n++
			if n == 2 {
				return notSup
			}
			return platformStrongSync(f)
		})
		defer restore()

		err := s.SetResume("w1", "new")
		if !errors.Is(err, ErrRegistryUncertain) {
			t.Fatalf("目錄項 barrier 失敗必須 latch uncertain，got %v", err)
		}
		if !s.Uncertain() {
			t.Fatal("目錄項 barrier 失敗必須 latch uncertain")
		}
		if e, _ := s.Get("w1"); e.ResumeSessionID != "new" {
			t.Fatalf("記憶體不得退回舊值（rename 已成功），got %q", e.ResumeSessionID)
		}
		if e, _ := diskEntry(t, path, "w1"); e.ResumeSessionID != "new" {
			t.Fatalf("rename 已成功，磁碟應為新值，got %q", e.ResumeSessionID)
		}
	})
}

// TestStrongSyncBuildTagsCoverNonDarwin：**編譯守門**。
//
// 只在 darwin 上開發時，`//go:build !darwin` 那半邊永遠不會被編譯——加了新欄位、
// 改了 platformStrongSync 簽名、或忘了補非 Darwin 實作，本機 build／test 全綠
// 卻在別的平台整包編不起來（失效形狀 (E) 的另一種形狀：測試環境本身量不出
// 那個效果）。這裡用跨平台 build 把它變成可觀測。
//
// 用 `go vet` 而不是 `go build`（rev2 review M1）：`go build` **不含 `_test.go`**，
// 守不到「非 Darwin 平台上 test 檔還編不編得起來」那一維；`go vet` 會把測試檔
// 一起型別檢查，成本相同。
//
// **只測 linux，刻意不含 windows**（rev2 review M2）：這個 repo 在 Windows 上
// 本來就不 build（`internal/proc` 既有限制），而且 `syncDir` 用唯讀目錄 handle
// 再 Sync，在 Windows 上 `FlushFileBuffers` 會失敗——把 windows 列進守門會被
// 讀成「Windows 有覆蓋」，那是假的。這條守的範圍就是「`!darwin` 分支是一份
// 完整、編得起來的實作」，不是跨平台正確性。
//
// mutation：
//   - 刪掉 strongsync_other.go → 紅在「GOOS=linux 檢查失敗」（undefined:
//     platformStrongSync）。
//   - 把 strongsync_darwin.go 的 tag 改成 `//go:build !darwin`（兩邊都納入）
//     → 同一行紅（duplicate declaration）。
//   - 改掉其中一邊的 platformStrongSync 簽名 → 同一行紅。
func TestStrongSyncBuildTagsCoverNonDarwin(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("編譯守門需要 go toolchain（不靜默跳過）：%v", err)
	}
	cmd := exec.Command(goBin, "vet", ".")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("GOOS=linux 檢查失敗（!darwin 分支必須自成完整實作、含 _test.go）：%v\n%s", berr, out)
	}
}
