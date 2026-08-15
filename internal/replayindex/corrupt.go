// Task 18：index 檔本身（<wsid>.turns.jsonl）的損壞處置分級（§3.5.6，凍
// 結）。Task 17 的 VerifyOrRebuild 已經處理 checkpoint 相對 audit 落後／不可
// 信的三態修復，但完全沒檢查既有 *.turns.jsonl 檔案內容本身是否可解析——若
// 某個 wsid 的 turn file 尾端或中段有半筆寫入（例如 crash 發生在
// appendTurnRecord 的 os.OpenFile／Fprintf 之間），checkpoint 可能仍然可
// 信（它只記全域 offset／event id，不驗證 turn file 內容），但 RecentTurns
// 讀到那個壞行時仍會出事。本檔補上這一層。
//
// 判定方式（凍結）：第一個無法解析的行之後，若還有其他能解析的行 ⇒ 中段；
// 否則 ⇒ 尾端。這個判定邏輯與實際修復動作（尾端 truncate／中段標記）都寫在
// index.go 的 readTurnFileLocked——本檔只負責「VerifyOrRebuild 啟動期逐檔案
// 呼叫它、辨識中段損壞的 sentinel error、觸發 quarantine＋全量重建」，不重
// 複那段解析邏輯。
package replayindex

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// midCorruptionError：readTurnFileLocked 判定某個 wsid 的 turn file 是中段損
// 壞時回傳的 sentinel——它自己沒有 audit path，無法安全重建，只能把「這是中
// 段損壞、不是一般 I/O 錯誤」這件事帶出去，讓握有 audit path 的呼叫端（本檔
// 的 repairTurnFileCorruptionLocked）決定如何處置。一般呼叫端（RecentTurns／
// TurnsBefore）不特判這個型別，走一般 error 路徑 fail loud——維持 Task 15 對
// 「無法安全處理的損壞」一律回錯的既有行為，不因為多了分級就對外放寬。
type midCorruptionError struct {
	wsid string
	err  error
}

func (e *midCorruptionError) Error() string {
	return fmt.Sprintf("replayindex: mid-file corruption in %s turn file: %v", e.wsid, e.err)
}

func (e *midCorruptionError) Unwrap() error { return e.err }

// repairTurnFileCorruptionLocked：呼叫端須持有 idx.mu，只在 checkpoint 本身
// 已判定可信時呼叫（verifyOrRebuildLocked 的 !trusted 分支已經無條件
// quarantine 全部既有 turn file、從頭重建，不需要也不該重複判斷）。
//
// 逐一對 idx.dir 下每個既有 *.turns.jsonl 呼叫 readTurnFileLocked：
//   - 沒有損壞：no-op（readTurnFileLocked 回傳完整內容，這裡不使用，只借它
//     的解析與副作用）。
//   - 尾端損壞：readTurnFileLocked 內部已經 truncate 磁碟檔案並正常回傳，這
//     裡同樣是 no-op——不需要另外通知或標記。
//   - 中段損壞：readTurnFileLocked 回傳 *midCorruptionError。此時視同
//     checkpoint 不可信：checkpoint 歸零、既有 *.turns.jsonl 全部 quarantine
//     （沿用 rebuild.go 的 resetTurnFilesLocked，該函式的文件本就預告 Task 18
//     會直接沿用這個搬移動作）、回傳 rebuilt=true 讓呼叫端從 offset 0 全量
//     重掃 audit。events.jsonl 是唯一權威，全量重掃不會遺失或猜測任何事
//     件——即使只有一個 wsid 的 turn file 中段損壞，也是重建全部 wsid（不逐
//     wsid 精細重建），理由：resetTurnFilesLocked／rescanFromLocked(0) 是
//     Task 17 已測試過的既有路徑，直接複用比新增一條「只重建單一 wsid」的
//     路徑更簡單、風險更低，且全量重掃的成本相對 turns.jsonl（每筆約 100
//     bytes）並不高（見 index.go RecentTurns 的說明）。一次呼叫最多觸發一次
//     全量重建：只要偵測到任何一個 wsid 中段損壞就立刻處理並回傳，不逐檔案
//     累積多次 quarantine。
func (idx *Index) repairTurnFileCorruptionLocked() (rebuilt bool, err error) {
	matches, err := filepath.Glob(filepath.Join(idx.dir, "*.turns.jsonl"))
	if err != nil {
		return false, fmt.Errorf("replayindex: glob turn files: %w", err)
	}

	for _, p := range matches {
		wsid := strings.TrimSuffix(filepath.Base(p), ".turns.jsonl")
		if _, err := idx.readTurnFileLocked(wsid); err != nil {
			var mc *midCorruptionError
			if !errors.As(err, &mc) {
				return false, err
			}
			idx.checkpointOffset = 0
			idx.checkpointLastEventID = ""
			idx.turns = map[string]*turnState{}
			if rerr := idx.resetTurnFilesLocked(); rerr != nil {
				return false, rerr
			}
			return true, nil
		}
	}
	return false, nil
}
