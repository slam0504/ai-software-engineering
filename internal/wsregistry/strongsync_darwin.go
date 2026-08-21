//go:build darwin

package wsregistry

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// strongSyncName：這個平台上實際發出的 syscall。同時是錯誤訊息的一部分與
// 呼叫序列守門測試的斷言對象（見 strongsync_test.go）。
const strongSyncName = "fcntl(F_FULLFSYNC)"

// fullsyncFn：F_FULLFSYNC 這個 syscall 的唯一發出點。
//
// Darwin 上刻意有**兩層**注入點，因為它們守的是不同的東西：
//   - strongSyncFn（strongsync.go，跨平台）：barrier 的**呼叫序列**與失敗時
//     四步契約的處置；
//   - fullsyncFn（這裡，Darwin 專屬）：platformStrongSync **自己內部**的
//     「不得靜默 fallback」規則。少了這一層，「F_FULLFSYNC 失敗時退回 f.Sync()
//     再回報成功」這個 mutation 沒有任何測試會紅——在沒有壞掉的檔案系統上，
//     F_FULLFSYNC 永遠成功，這條規則就只是註解裡的保證（失效形狀 (C)）。
//
// production 恆為 fcntlFullsync。
var fullsyncFn = fcntlFullsync

func fcntlFullsync(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}

// platformStrongSync：macOS 的強持久性 barrier。
//
// F_FULLFSYNC 對**檔案 fd 與目錄 fd 都適用**（2026-08-17 在 APFS 上實測：兩者
// 皆回 nil；四步契約的步驟 4 因此也走這裡，而不是退回 d.Sync()）。
//
// 失敗一律往上丟、**不** fallback 到 f.Sync()：理由見 strongsync.go 檔頭。
// 錯誤訊息帶上 syscall 名稱與路徑，讓「這個檔案系統不支援 F_FULLFSYNC」在
// post-mortem 裡跟一般 EIO 分得開。
func platformStrongSync(f *os.File) error {
	if err := fullsyncFn(f); err != nil {
		return fmt.Errorf("wsregistry: %s(%s) 失敗：%w"+
			"（macOS 的 fsync 不保證資料離開硬碟的揮發性快取，因此刻意不退回較弱的 fsync 再回報成功）",
			strongSyncName, f.Name(), err)
	}
	return nil
}
