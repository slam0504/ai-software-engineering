package wsregistry

import "os"

// ---- durability barrier 的唯一出口（owner 2026-08-17 rev2 追加）----
//
// 四步契約裡有**兩個** barrier，兩個都必須用同一個強度：
//   - 步驟 2：暫存檔內容
//   - 步驟 4：rename 造成的父目錄項變更
//
// 為什麼不能只用 os.File.Sync()：在 macOS 上 fsync(2) 只保證資料交給磁碟，
// **不保證資料離開磁碟自己的揮發性快取**（Apple fsync(2) man page 明文；要更強
// 的持久性預期必須用 F_FULLFSYNC）。Task 2a 被納入 M3b 的理由正是「斷電後
// resume 身分不得回退」，只用 File.Sync() 在 macOS 上撐不起那個宣稱。
//
// 平台分歧只發生在 platformStrongSync／strongSyncName（build tag 分流，見
// strongsync_darwin.go／strongsync_other.go）。共用層刻意只有這一層薄包裝：
// 兩個 barrier 都呼叫 strongSync，新增平台不可能漏接其中一個。
//
// **不做 fallback**：F_FULLFSYNC 失敗（含「這個檔案系統不支援」的 ENOTSUP／
// ENOTTY）時退回較弱的 fsync 再回報成功，正是這個里程碑一路在修的形狀——用一個
// 看似安全的預設值掩蓋一個實際上未知的事實。這裡把它當成該步驟失敗往上丟，由
// 四步契約決定處置：步驟 2 失敗＝commit 沒發生（回滾）、步驟 4 失敗＝commit
// 結果不確定（latch ErrRegistryUncertain）。

// strongSyncFn：平台實作的唯一間接層。production 恆為 platformStrongSync；
// 只有 strongsync_test.go 的 ForceStrongSyncForTest 會換掉它（同 fsync_test.go
// 的 ForceStepHookForTest 慣例：注入宣告在 _test.go，production 檔只留接點）。
var strongSyncFn = platformStrongSync

// strongSync：兩個 barrier 共用的出口。呼叫序列由
// TestBothDurabilityBarriersGoThroughStrongSync 守——少呼叫一次、或其中一個
// barrier 偷偷改回 f.Sync()，記錄到的次數就對不上。
func strongSync(f *os.File) error { return strongSyncFn(f) }
