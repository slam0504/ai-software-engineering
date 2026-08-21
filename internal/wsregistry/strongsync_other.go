//go:build !darwin

package wsregistry

import "os"

// strongSyncName：非 Darwin 平台就是 fsync(2)。
//
// 刻意**不**在這裡替 Linux 之類的平台再加一層「更強」的東西：Linux 的 fsync
// 本來就要求把資料寫到底層裝置（write barrier 由檔案系統／block layer 負責），
// 沒有 F_FULLFSYNC 這種對應物。要在其他平台補等價保證需要各自的實測證據，
// 沒有證據就加等於再造一個「只寫在註解裡的保證」。
const strongSyncName = "fsync"

func platformStrongSync(f *os.File) error { return f.Sync() }
