//go:build darwin

package wsregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestDarwinStrongSyncDoesNotFallBackToWeakFsync：owner rev2 第 3 點的**唯一
// 真正守門**——F_FULLFSYNC 不支援／失敗時，不得退回較弱的 fsync 之後仍回報成功。
//
// 為什麼需要注入：在正常的 APFS 上 F_FULLFSYNC 永遠成功，所以「失敗時會不會
// fallback」這條規則在真實環境裡量不到（失效形狀 (E)）。這裡把 syscall 那一層
// 換成回 ENOTSUP（「這個檔案系統不支援」的真實形狀），受測對象仍是
// platformStrongSync 自己的程式碼。
//
// 測試檔案刻意是一個**健康、可寫、f.Sync() 一定成功**的檔案：fallback 版本會
// 回 nil，因此「回 nil」就是 fallback 的直接證據，不是靠推論。
//
// mutation：platformStrongSync 的錯誤分支改成 `return f.Sync()` →
// 紅在「不得退回較弱的 fsync 再回報成功（got nil）」。
func TestDarwinStrongSyncDoesNotFallBackToWeakFsync(t *testing.T) {
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "healthy"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, werr := f.Write([]byte("x")); werr != nil {
		t.Fatal(werr)
	}
	// 前提：這個 fd 的 f.Sync() 確實會成功——否則下面「回 nil ＝ fallback」的
	// 推論就不成立。
	if serr := f.Sync(); serr != nil {
		t.Fatalf("前提不成立：健康檔案的 f.Sync() 應成功，got %v", serr)
	}

	prev := fullsyncFn
	fullsyncFn = func(*os.File) error { return unix.ENOTSUP }
	defer func() { fullsyncFn = prev }()

	serr := platformStrongSync(f)
	if serr == nil {
		t.Fatal("F_FULLFSYNC 不支援時不得退回較弱的 fsync 再回報成功（got nil）")
	}
	if !errors.Is(serr, unix.ENOTSUP) {
		t.Fatalf("錯誤要保留原始 errno（不支援 vs 一般 EIO 要分得開）：%v", serr)
	}
	if !strings.Contains(serr.Error(), "F_FULLFSYNC") {
		t.Fatalf("錯誤訊息要指出是哪一個 barrier 失敗：%v", serr)
	}
}

// TestDarwinFullsyncFailurePropagatesThroughFourStepContract：上面那條的整條
// 路徑版本——syscall 層不支援時，registry 寫入必須**失敗**，而不是靜默降級後
// 回報成功。
//
// mutation：platformStrongSync 改成 fallback → Put 回 nil，紅在
// 「barrier 不支援時整次寫入必須失敗」。
func TestDarwinFullsyncFailurePropagatesThroughFourStepContract(t *testing.T) {
	s, _ := openStore(t)
	prev := fullsyncFn
	fullsyncFn = func(*os.File) error { return unix.ENOTSUP }
	defer func() { fullsyncFn = prev }()

	err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	if err == nil {
		t.Fatal("barrier 不支援時整次寫入必須失敗（不得靜默降級後回報成功）")
	}
	if _, ok := s.Get("w1"); ok {
		t.Fatal("步驟 2 失敗＝commit 沒發生，記憶體必須回滾")
	}
}
