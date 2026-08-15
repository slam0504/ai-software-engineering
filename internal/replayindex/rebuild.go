// Task 17：crash consistency 三態修復（啟動期，§3.5.3／§3.5.5）。正常路徑是
// 「先 audit write、再更新 index」，audit sink 不為快取強迫逐事件 fsync，故
// crash 後 checkpoint 相對 audit 可能：
//
//  1. 落後——checkpoint 是 audit 中某個真實行邊界，只是還沒追上最新進度：
//     從該 offset 續掃 audit suffix 補齊缺的 turn record。
//  2. 超前／offset 超界／event ID 不符——checkpoint 指到 audit 裡不存在、或
//     對不上的位置：整份視為不可信快取，既有 *.turns.jsonl **quarantine**
//     （改名保留、非刪除，§3.5.6 凍結用字）後從頭全量重建（尾端 truncate
//     vs 中段細分、復原通知是 Task 18 範圍，本檔先以「不可信就整份重建＋
//     quarantine 舊檔」這個安全但較保守的處置頂住，不做進一步分級）。
//  3. checkpoint 越過未完成 turn——checkpointOffset 是單一全域欄位、不是
//     per-WSID，故某個 WSID 的 open turn 起點可能早於目前全域 checkpoint。
//     這種狀況下 checkpoint.json 的 open_turn_start_offsets 仍會正確保存該
//     WSID 的起點（§3.5.5），但 firstEventID 只在記憶體才有、不隨 checkpoint
//     落盤——重啟後必須從 audit 該 offset 讀回 firstEventID，否則該 turn 之
//     後收尾時 appendTurnRecord 會寫出空字串的 FirstEventID。
//
// 補掃／重建全程**不推算 offset**（§3.5.2 凍結：index 一律以 AppendReceipt
// 為準）——這裡沒有 receipt 可用，但掃描 audit 檔時逐行用 bufio.Reader 讀
// 取、自行累計已讀取的 byte 數作為每筆事件的 StartOffset／EndOffset，這是
// **實際讀到的位置**，不是推算，因此不違反凍結取捨。
//
// 啟動期無並行（§3.2.4 序列：VerifyOrRebuild 在開 UI 前完成，尚無並行
// append）。app 運行中由 degraded latch 觸發的重建條件完全不同——provider turn
// 仍持續 append audit，必須走 §3.5.7 凍結的五步序列，見本檔後半段的
// RuntimeRebuild（Task 19）。兩條路徑共用 scanAuditRangeLocked 的掃描迴圈，差
// 別在「掃到的進度寫進哪裡」（啟動期直接寫 checkpoint，runtime 期只前移
// rebuildCursor）以及「一次掃多少」（啟動期一次掃到底，runtime 期分段釋放
// idx.mu，見 scanSegmentBytes）。
//
// 已知限制（非本 task 要解的問題，記錄供 Task 18／19 或未來維運參考）：連續
// 多次「crash → 判定不可信 → resetTurnFilesLocked quarantine」循環，quarantine
// 檔會無限累積在 idx.dir 底下，目前沒有清理／保留期限策略。
package replayindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// maxLineWindow：checkpointTrustedLocked／lineEventIDEndingAt 驗證 checkpoint
// 時，往回讀取的最大 window（bytes）。對齊 repo 讀 audit／JSONL 的既有慣例
// ——internal/codex/rpc.go、internal/assist/oneshot.go、
// internal/wirelog/frameindex.go、internal/claude/session.go、restore.go
// 六處讀 audit 相關內容一律用 16MB scanner buffer，這裡跟隨同一個先例（而
// 非同 package 內 index.go:520 那個 10MB——那是掃小得多的 turn record，不
// 是 audit 本體）。
//
// 16MB 仍不是硬保證：canonical user message 的 Text 長度不受限
// （contract.Envelope、JSONLSink.Write 都沒有行長度上限），使用者貼一段超
// 大 log／diff 進單則訊息是正常操作。單行仍可能超出這個 window——這種情況
// 不會誤判、也不會 panic（見 lineEventIDEndingAt 的 windowTruncated 分支），
// 但會安全地退化成全量重建；VerifyOrRebuild 對此一律透過 Config.Notify 發
// 出可觀測訊號，不能讓這個退化靜默發生。
const maxLineWindow = 16 << 20

// auditFile：lineEventIDEndingAt 讀取 audit 檔所需的最小介面。生產環境一律
// 是 *os.File；測試可透過覆寫 openAuditFile 換成計數 wrapper，驗證「checkpoint
// 驗證只讀 window、不需隨檔案大小全掃」（見 rebuild_test.go 的
// TestCheckpointVerificationDoesNotScanWholeFile）。
type auditFile interface {
	io.ReaderAt
	io.Closer
}

var openAuditFile = func(path string) (auditFile, error) {
	return os.Open(path)
}

// VerifyOrRebuild：啟動期呼叫一次，驗證 checkpoint 相對 auditPath（events.jsonl
// 權威檔）是否可信，落後則補掃、超前或對不上則整份重建，並補回任何未完成
// turn 的 firstEventID。呼叫時尚無並行 append（§3.2.4）。
//
// 實際邏輯在 verifyOrRebuildLocked（全程持 idx.mu）；這裡只多做一件事：把
// 「checkpoint 驗證因單行超出 maxLineWindow 而降級為全量重建」這個訊號帶出
// 鎖外才呼叫 Config.Notify——同 Observe 的「先處理、後解鎖、再通知」慣例
// （見 Observe 的說明），避免在持鎖時呼叫可能回呼 Index 方法的 callback 造
// 成死鎖。這個通知與 §3.5.4 的 degraded latch 無關（VerifyOrRebuild 完全不
// 觸碰 idx.degraded），純粹是「這次重啟多做了一次原本不該發生的全量重建」
// 這件事本身需要可觀測，不能靠人猜——16MB window 仍不是硬保證（見
// maxLineWindow 說明），一旦真的發生，每次重啟都會靜默觸發全量重建，正是
// 這個套件存在理由要避免的事。
func (idx *Index) VerifyOrRebuild(auditPath string) error {
	windowTruncated, corruptionRebuilt, err := func() (bool, bool, error) {
		idx.mu.Lock()
		defer idx.mu.Unlock()
		return idx.verifyOrRebuildLocked(auditPath)
	}()

	if err == nil && windowTruncated {
		if notify := idx.cfg.Notify; notify != nil {
			notify(fmt.Sprintf(
				"replayindex: checkpoint 驗證因單行超出 window（%d bytes）無法判讀，已降級為全量重建",
				maxLineWindow))
		}
	}
	if err == nil && corruptionRebuilt {
		// 復原通知只在中段損壞才發（§3.5.6）；尾端損壞在
		// repairTurnFileCorruptionLocked／readTurnFileLocked 內已經就地
		// truncate 續用，不會走到這裡。同 windowTruncated 的慣例：鎖外才呼
		// 叫 Notify，避免持鎖時觸發可能回呼 Index 方法的 callback。
		if notify := idx.cfg.Notify; notify != nil {
			notify("replayindex: turn index 中段損壞，已 quarantine 並全量重建（§3.5.6）")
		}
	}
	return err
}

// verifyOrRebuildLocked：呼叫端須持有 idx.mu。VerifyOrRebuild 的實際流程；
// 特意重新從磁碟讀入 checkpoint.json（見 reloadCheckpointFromDiskLocked），
// 不信任 Open() 當時快取在記憶體裡的值——這是獨立於 Open 的驗證步驟，職責
// 就是核對「磁碟現在記的狀態」是否仍與 audit 一致。
//
// corruptionRebuilt（Task 18，§3.5.6）：checkpoint 本身可信時，既有
// *.turns.jsonl 檔案內容仍可能中段損壞（checkpoint 只記全域 offset／event
// id，不驗證 turn file 內容）——repairTurnFileCorruptionLocked 負責偵測並在
// 偵測到中段損壞時觸發與「checkpoint 不可信」相同的 quarantine＋全量重建路
// 徑。尾端損壞則完全不影響這裡的流程：readTurnFileLocked 內部就地 truncate
// 續用，corruptionRebuilt 維持 false。
func (idx *Index) verifyOrRebuildLocked(auditPath string) (windowTruncated, corruptionRebuilt bool, err error) {
	if err := idx.reloadCheckpointFromDiskLocked(); err != nil {
		return false, false, err
	}

	var auditSize int64
	if info, err := os.Stat(auditPath); err == nil {
		auditSize = info.Size()
	} else if !os.IsNotExist(err) {
		return false, false, fmt.Errorf("replayindex: stat audit %s: %w", auditPath, err)
	}

	trusted, truncated, err := idx.checkpointTrustedLocked(auditPath, auditSize)
	if err != nil {
		return truncated, false, err
	}

	from := idx.checkpointOffset
	if !trusted {
		// 超前／offset 超界／event ID 不符：不可信快取一律視為修復對象
		// （§3.5.3）。無法判斷既有 turn record 中哪些仍可信，寧可整份丟棄、
		// 從頭全量重掃，避免與後續補掃疊加出重複 record。
		idx.checkpointOffset = 0
		idx.checkpointLastEventID = ""
		idx.turns = map[string]*turnState{}
		if err := idx.resetTurnFilesLocked(); err != nil {
			return truncated, false, err
		}
		from = 0
	} else {
		rebuilt, err := idx.repairTurnFileCorruptionLocked()
		if err != nil {
			return truncated, false, err
		}
		if rebuilt {
			corruptionRebuilt = true
			from = 0
		} else if err := idx.hydrateOpenTurnFirstEventIDsLocked(auditPath); err != nil {
			// 已存在的 checkpoint 若記錄某 WSID 有 open turn，loadCheckpoint
			// 只帶回 offset（§3.5.5），firstEventID 尚未知——從 audit 補
			// 讀，否則之後這個 turn 收尾時 appendTurnRecord 會寫出空字串的
			// FirstEventID。
			return truncated, false, err
		}
	}

	return truncated, corruptionRebuilt, idx.rescanFromLocked(auditPath, from)
}

// reloadCheckpointFromDiskLocked：呼叫端須持有 idx.mu。無條件捨棄目前記憶體
// 狀態、從 checkpoint.json 重新讀入——與建構期的 loadCheckpoint 邏輯相同，
// 但 loadCheckpoint 假設呼叫時記憶體本就是空的（建構函式初始化路徑），這裡
// 則是重啟驗證步驟，必須先清空才能保證讀到的是磁碟現況，不是兩者疊加。
func (idx *Index) reloadCheckpointFromDiskLocked() error {
	idx.checkpointOffset = 0
	idx.checkpointLastEventID = ""
	idx.turns = map[string]*turnState{}

	path := idx.checkpointPath()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		var cf checkpointFile
		if jerr := json.Unmarshal(b, &cf); jerr != nil {
			return fmt.Errorf("replayindex: malformed checkpoint %s: %w", path, jerr)
		}
		idx.checkpointOffset = cf.Offset
		idx.checkpointLastEventID = cf.LastEventID
		for wsid, off := range cf.OpenTurns {
			idx.turns[wsid] = &turnState{open: true, startOffset: off}
		}
		return nil
	case os.IsNotExist(err):
		return nil
	default:
		return fmt.Errorf("replayindex: read checkpoint %s: %w", path, err)
	}
}

// checkpointTrustedLocked：呼叫端須持有 idx.mu。判斷目前記憶體中的
// checkpointOffset／checkpointLastEventID 是否確實對應 auditPath 裡一個真實
// 的行邊界與 event id。offset 0（從未索引過）視為天然可信的基線。
// windowTruncated 原樣轉傳 lineEventIDEndingAt 的訊號（見該函式說明）。
func (idx *Index) checkpointTrustedLocked(auditPath string, auditSize int64) (trusted, windowTruncated bool, err error) {
	if idx.checkpointOffset == 0 {
		// offset 0 卻宣稱有 last_event_id 是自相矛盾的資料，不可信。
		return idx.checkpointLastEventID == "", false, nil
	}
	if idx.checkpointOffset < 0 || idx.checkpointOffset > auditSize {
		return false, false, nil // 超前／offset 超界
	}
	id, found, truncated, err := lineEventIDEndingAt(auditPath, idx.checkpointOffset)
	if err != nil {
		return false, truncated, err
	}
	return found && id == idx.checkpointLastEventID, truncated, nil // event ID 不符 → false
}

// resetTurnFilesLocked：呼叫端須持有 idx.mu。把 idx.dir 下所有既有
// *.turns.jsonl **quarantine**（改名搬移，非刪除）——§3.5.6 凍結用字是
// 「quarantine」：索引本身可從 audit 完全重建，遺失不算資料遺失，但保留損
// 壞／不可信的舊檔是明文要求的診斷與復原通知資產，Task 18 落實完整分級處
// 置時可以直接沿用這個搬移動作，不必先拆掉刪除邏輯。只在 checkpoint 被判
// 定不可信、即將整份重掃時呼叫，避免重掃寫出的 turn record 疊加在舊內容之
// 上造成重複。
//
// quarantine 檔名帶奈秒時間戳只為了同一秒內對同一 wsid 觸發多次時仍保持唯
// 一，不是供程式解析用的正式格式；解析／復原通知是 Task 18 範圍。
func (idx *Index) resetTurnFilesLocked() error {
	matches, err := filepath.Glob(filepath.Join(idx.dir, "*.turns.jsonl"))
	if err != nil {
		return fmt.Errorf("replayindex: glob turn files: %w", err)
	}
	for _, p := range matches {
		quarantined := fmt.Sprintf("%s.quarantine-%d", p, time.Now().UnixNano())
		if err := os.Rename(p, quarantined); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replayindex: quarantine stale turn file %s: %w", p, err)
		}
	}
	return nil
}

// hydrateOpenTurnFirstEventIDsLocked：呼叫端須持有 idx.mu。為每個從
// checkpoint 載入、firstEventID 仍空白的 open turn，從 audit 該 startOffset
// 讀回真正的 event id（§3.5.5）。
func (idx *Index) hydrateOpenTurnFirstEventIDsLocked(auditPath string) error {
	for wsid, st := range idx.turns {
		if !st.open || st.firstEventID != "" {
			continue
		}
		id, err := eventIDAtOffset(auditPath, st.startOffset)
		if err != nil {
			return fmt.Errorf("replayindex: recover open turn start for wsid %s: %w", wsid, err)
		}
		st.firstEventID = id
	}
	return nil
}

// rescanFromLocked：呼叫端須持有 idx.mu。從 auditPath 的 from offset 開始逐行
// 讀到檔尾，每筆事件以「掃描時實際讀到的行起訖位置」組出 AppendReceipt（不
// 推算），餵給既有的 applyTurnState／appendTurnRecord／writeCheckpointFile
// （與 Observe 相同的底層邏輯，唯一差異是這裡已持鎖、不重入 idx.mu），藉此
// 跨所有出現在這段 audit suffix 裡的 WSID 一併補齊 turn 狀態與 checkpoint。
//
// 結尾無條件補一次 writeCheckpointFile：掃描終點若落在非 boundary 事件（例
// 如 suffix 只有幾筆 delta、turn 仍未收尾），迴圈內不會觸發落盤，若不在此
// 補一次，磁碟 checkpoint 會停在掃描前的舊值，下次重啟會重掃同一段 suffix、
// 但這段已經在本次 appendTurnRecord 寫過的 turn record 會被重複補寫。
func (idx *Index) rescanFromLocked(auditPath string, from int64) error {
	// 重掃可能與「上一個 process 的重建已經寫進 turn file 的 record」重疊
	// （見 beginRebuildDedupLocked），去重整段掃描期間都要開著。
	idx.beginRebuildDedupLocked()
	defer idx.endRebuildDedupLocked()

	_, found, err := idx.scanAuditRangeLocked(auditPath, from, unlimitedScanBudget, func(receipt appcore.AppendReceipt, boundary bool) error {
		idx.checkpointOffset = receipt.EndOffset
		idx.checkpointLastEventID = receipt.EventID
		if boundary {
			return idx.writeCheckpointFile() // 啟動期無並行、無 degraded latch 概念：失敗直接 fail loud
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return nil // 沒有 audit 檔可掃（例如全新 workspace）
	}
	return idx.writeCheckpointFile()
}

// scanAuditRangeLocked：呼叫端須持有 idx.mu。從 auditPath 的 from offset 逐行
// 讀到檔尾，每筆事件以「掃描時實際讀到的行起訖位置」組出 AppendReceipt（不推
// 算，§3.5.2），先餵給 applyTurnState，再把 receipt 與 boundary 交給 commit
// 決定要把進度記到哪裡——啟動期（rescanFromLocked）直接寫 checkpoint，runtime
// 重建（scanIntoRebuildCursorLocked）只前移 rebuildCursor。
//
// budget 是本次最多處理的 byte 數，**在處理每筆事件之前檢查**：所以 budget 為
// 0 時一筆都不會處理（finalCatchUpAndAttach 用它把補掃終點夾在既定的 end），而
// budget 為正時至少會處理一筆事件（即使該筆單獨就超過 budget），保證分段掃描
// 的進度必然前進、不會空轉。啟動期傳 unlimitedScanBudget 一次掃到底。
//
// 回傳 cur 是實際掃到的終點 offset；found=false 代表 audit 檔不存在（呼叫端自
// 行決定這是不是問題）。commit 或 applyTurnState 回錯時立即中止並回傳當下的
// cur——即「最後一筆成功提交的事件結尾」，讓 runtime 重建的重試能從那裡續掃、
// 不重複索引已經寫進 turn file 的 record。
func (idx *Index) scanAuditRangeLocked(auditPath string, from, budget int64, commit func(receipt appcore.AppendReceipt, boundary bool) error) (cur int64, found bool, err error) {
	f, oerr := os.Open(auditPath)
	if oerr != nil {
		if os.IsNotExist(oerr) {
			return from, false, nil
		}
		return from, false, fmt.Errorf("replayindex: open audit %s: %w", auditPath, oerr)
	}
	defer f.Close()

	if from > 0 {
		if _, serr := f.Seek(from, io.SeekStart); serr != nil {
			return from, true, fmt.Errorf("replayindex: seek audit %s to %d: %w", auditPath, from, serr)
		}
	}

	r := bufio.NewReader(f)
	cur = from
	for {
		if cur-from >= budget {
			break // 本段預算用完：進度已記在 cur，呼叫端決定要不要續掃
		}
		line, rerr := r.ReadBytes('\n')
		if n := int64(len(line)); n > 0 {
			trimmed := bytes.TrimRight(line, "\n")
			if len(bytes.TrimSpace(trimmed)) > 0 {
				var env contract.Envelope
				if jerr := json.Unmarshal(trimmed, &env); jerr != nil {
					return cur, true, fmt.Errorf("replayindex: malformed audit line at offset %d: %w", cur, jerr)
				}
				receipt := appcore.AppendReceipt{StartOffset: cur, EndOffset: cur + n, EventID: env.EventID}
				boundary, aerr := idx.applyTurnState(env, receipt)
				if aerr != nil {
					return cur, true, aerr
				}
				if cerr := commit(receipt, boundary); cerr != nil {
					return cur, true, cerr
				}
			}
			cur += n
		}
		if rerr != nil {
			break // EOF 或讀取錯誤都代表已到掃描終點
		}
	}
	return cur, true, nil
}

// lineEventIDEndingAt：找出 auditPath 中「行的結尾恰好落在 target offset」
// 的那一行、回傳其 event id。**O(1) 相對檔案總大小**——只讀 target 之前最多
// maxLineWindow bytes（見該常數說明），不從頭掃整份 audit：重啟驗證 checkpoint
// 若退化成又一次全掃 events.jsonl，就違背了 replayindex 存在的理由（讓重啟
// 不必全掃）。
//
// 找不到目標行（target 不在任何行邊界上，或該行無法解析）回傳 found=false、
// 非錯誤——由呼叫端決定這代表 checkpoint 不可信，不是硬性 I/O 失敗。
//
// windowTruncated=true 是其中一種更明確的子情況：window 內完全沒有找到
// target 那一行**之前**的換行符，代表 target 那一行本身可能超出
// maxLineWindow、我們讀到的只是被截斷的中後段片段，不是這一行的完整內
// 容——這種「讀不完整」與「讀到完整但對不上」不同，呼叫端（VerifyOrRebuild）
// 需要為此發出可觀測訊號，不能讓它跟一般的「事件 ID 不符」混在一起靜默處
// 理。windowStart==0 時（window 已涵蓋到檔案開頭）沒有這個歧義，不算截斷。
func lineEventIDEndingAt(auditPath string, target int64) (id string, found, windowTruncated bool, err error) {
	if target <= 0 {
		return "", false, false, nil
	}
	f, err := openAuditFile(auditPath)
	if err != nil {
		return "", false, false, fmt.Errorf("replayindex: open audit %s: %w", auditPath, err)
	}
	defer f.Close()

	windowStart := target - maxLineWindow
	if windowStart < 0 {
		windowStart = 0
	}
	buf := make([]byte, target-windowStart)
	if _, err := f.ReadAt(buf, windowStart); err != nil {
		return "", false, false, nil // window 讀不滿（例如 target 超過檔案長度）：視為對不上
	}
	if buf[len(buf)-1] != '\n' {
		return "", false, false, nil // target 沒有落在行邊界上
	}
	body := buf[:len(buf)-1] // 去掉行尾換行
	// 找 body 內最後一個換行——其後到 body 結尾就是 target 對應的那一整行。
	// windowStart 可能切在更早一行的中間，但那不影響這裡：我們只在乎「最後
	// 一個」換行之後的內容，不需要 window 起點恰好對齊行邊界。
	lineStart := bytes.LastIndexByte(body, '\n')
	if lineStart == -1 && windowStart > 0 {
		return "", false, true, nil // window 被單一一行佔滿，讀到的只是被截斷的片段
	}
	line := body[lineStart+1:]
	var env contract.Envelope
	if jerr := json.Unmarshal(line, &env); jerr != nil {
		return "", false, false, nil // 該行本身無法解析：視為對不上
	}
	return env.EventID, true, false, nil
}

// ---- Task 19：runtime 重建交接（§3.5.7 凍結的五步序列）----

// 收斂上限與嘗試界限（§3.5.7 凍結常數，**不得改為設定值**）。
//
//   - MaxCatchUpBytes／MaxCatchUpRecords：取得 emit mutex 之前，「audit 檔尾
//     減去 rebuildCursor」這段殘量必須同時低於這兩個上限。byte 與 record 雙
//     上限缺一不可：單一事件的大小沒有上限（使用者可以貼一段巨大 log 進單則
//     訊息），只看 record 數會讓鎖內補掃量無界；反過來只看 byte 數則會讓大量
//     小事件（delta 串流常態）擠進同一個 byte 預算，鎖內要處理的 record 數同
//     樣無界。
//   - MaxCatchUpAttempts：鎖外 catch-up 的嘗試界限。沒有它，持續高頻 append
//     的工作負載會讓「殘量達標」永遠不成立，迴圈變成 busy-loop；有了它，兩種
//     不收斂情境都會在界限內中止本輪、保留 degraded latch 交給呼叫端 backoff
//     重試。
const (
	MaxCatchUpBytes    = 1 << 20
	MaxCatchUpRecords  = 512
	MaxCatchUpAttempts = 8
)

// auditEndFunc：回報「目前 audit 檔尾 offset」的來源。RuntimeRebuild 用它計算
// 殘量，而不是自己 stat——呼叫端（Task 20 的 App）才知道 audit sink 的權威檔
// 尾在哪裡（例如 sink 內部有 buffer 尚未 flush 時，檔案大小並不等於已受理的
// append 進度）。
//
// **呼叫端契約（違反會直接死鎖或 data race）**：
//
//   - RuntimeRebuild 的第 4 步會在**已持有 emitMu 的情況下**呼叫 auditEnd，所
//     以 auditEnd **不得再取得 emitMu**——sync.Mutex 不可重入，`m.mu.Lock();
//     return sink.offset` 這種最自然的寫法會當場自我死鎖。
//   - 同時 auditEnd 也會在**完全不持任何鎖**的情況下被呼叫（第 2 步），所以它
//     必須自行保證讀取 sink offset 的併發安全。appcore.JSONLSink 的 offset 沒
//     有自己的鎖（見 internal/appcore/sink.go：正確性完全依賴呼叫端序列
//     化），直接讀會被 -race 抓到。
//   - 可行的作法例如：sink offset 改用 atomic 型別讀寫，或呼叫端另外維護一個
//     只給 auditEnd 用的 atomic 鏡像值，總之**不要重用 emitMu**。
//
// 它同時是 barrier 測試唯一能模擬「等待 emit mutex 期間其他 goroutine 仍在
// append」的注入點：production 中 append 必須持有同一把 emit mutex，所以測試
// 不可能（也不該）在鎖內真的 append 一筆事件到 audit——鎖內 append 是不可能發
// 生的情境，照著做只會測到一個假的窗口。真正存在的窗口是「殘量檢查通過、
// emitMu 尚未到手」這段時間，注入一個會回報更大檔尾的 auditEndFunc 才能忠實
// 重現它。
type auditEndFunc func() (int64, error)

// ErrRebuildNotConverged：本輪 runtime 重建在 MaxCatchUpAttempts 內未能把殘量
// 壓到上限以下。degraded latch **保持不變**（index 仍是 degraded、Observe 仍
// 是空操作），rebuildCursor 也保留在已索引的位置——呼叫端應 backoff 後再呼叫
// RuntimeRebuild，屆時會從 rebuildCursor 續掃、不重跑 bulk。
var ErrRebuildNotConverged = errors.New("replayindex: catch-up 未收斂，保留 degraded latch")

// ErrNotDegraded：對一個不是 degraded 的 index 呼叫 RuntimeRebuild。這不是可
// 以靜默容忍的狀況，見 RuntimeRebuild 的「前提」段落。
var ErrNotDegraded = errors.New("replayindex: RuntimeRebuild 只服務 degraded latch，index 目前不是 degraded")

// ErrRebuildInProgress：已經有一輪 RuntimeRebuild 在跑。single-flight 是強制
// 的，見 beginRebuildRun。
var ErrRebuildInProgress = errors.New("replayindex: 已有一輪 RuntimeRebuild 進行中")

// RuntimeRebuild：app **運行中**（degraded latch 觸發）重建 turn 索引並把
// writer 原子接回，實作 §3.5.7 凍結的五步序列。與啟動期的 VerifyOrRebuild 的
// 關鍵差異是：這裡 provider turn 仍持續 append audit，所以**不能邊掃邊解除
// latch**——掃描高水位之後、writer 接回之前抵達的事件會留下永久缺口。五步序列
// 就是為了消掉那個窗口：
//
//  1. bulk 重建至初始高水位（**不持 emitMu**，append 照常進行）；
//  2. 鎖外反覆 catch-up 至最新 audit 檔尾，直到殘量低於收斂上限。迭代本身有
//     固定嘗試界限（MaxCatchUpAttempts），界限內仍未達標即中止本輪；
//  3. 殘量達標**才**取得 emit mutex（emitMu 是 audit append 與 Observe 的序列
//     化點，取得它等於凍結 audit 進度）；
//  4. 鎖內重讀殘量：**若又超限，立即 unlock 回到第 2 步重試**，不得在鎖內硬
//     掃——鎖內處理量必須有界，否則整條 provider 事件管線會被重建卡住；
//  5. 殘量符合上限 → 鎖內完成最後補掃 → 前移 checkpoint → 接回 writer → 解除
//     latch → unlock。第 5 步全程持有 emitMu，期間不可能有新的 append，因此
//     「掃到檔尾」與「解除 latch」之間沒有缺口，這就是接回的原子性來源。
//
// 兩種不收斂分支一律以「保留 degraded latch ＋ 中止本輪 ＋ 交給呼叫端 backoff
// 重試」收束，回傳 ErrRebuildNotConverged：(a) 鎖外 catch-up 在嘗試界限內始終
// 未達標，從未進入取鎖階段；(b) 達標取鎖後殘量又超限、反覆 unlock 重試至嘗試
// 界限。兩者都不解除 latch、不前移 checkpoint，index 維持 degraded 但**索引進
// 度不歸零**（rebuildCursor 保留）。
//
// I/O 錯誤（開檔、掃描、checkpoint 落盤失敗）不屬於這兩種分支，直接回傳原始
// 錯誤，latch 同樣保留。
//
// **前提：single-flight**——同一時間只能有一輪 RuntimeRebuild，重入直接回傳
// ErrRebuildInProgress，見 beginRebuildRun。
//
// **前提：index 必須已經處於 degraded latch**，否則回傳 ErrNotDegraded。這不
// 是防禦性檢查而是本函式正確性的必要條件：整套「同一段 audit 只會被掃一次、
// 所以不需要去重」的論證，隱含前提是重建期間 Observe 停著（degraded 時它是空
// 操作）。若 index 是活的，Observe 會與 catch-up 交錯取得 idx.mu、各自前移
// checkpoint 並寫 turn record，下一輪 catch-up 又從落後的 cursor 把同一段掃一
// 次——重複 record ＋ turn 狀態機互踩。這裡不靜默補 latch：那會發出呼叫端沒預
// 期的 degraded 通知，掩蓋掉「呼叫端用錯了」這件事。
//
// 因此本函式**只服務 degraded latch**。§3.5.6 的中段損壞 quarantine 並不會
// latch degraded，需要的也是不同的動作（reset turn files ＋從 offset 0 全量重
// 建），本函式的 bulkRebuild 永遠從 max(cursor, checkpoint) 起掃，給不了那個
// 語意——那條路徑目前沒有 runtime 入口，是 Task 20 的範圍。
//
// 呼叫端契約：emitMu 必須是「audit append 與 Observe 共用」的那一把序列化
// 鎖，且呼叫 RuntimeRebuild 時**不得已經持有它**（本函式自己取得）；auditEnd
// 另有契約，見 auditEndFunc 的說明。
func (idx *Index) RuntimeRebuild(auditPath string, emitMu sync.Locker, auditEnd auditEndFunc) error {
	if err := idx.beginRebuildRun(); err != nil {
		return err
	}
	defer idx.endRebuildRun()

	if err := idx.bulkRebuild(auditPath); err != nil { // 1. 掃至初始高水位（不持 emitMu）
		return err
	}

	for attempt := 0; attempt < MaxCatchUpAttempts; attempt++ {
		idx.countCatchUpAttempt()

		if err := idx.catchUpUnlocked(auditPath); err != nil { // 2. 鎖外反覆 catch-up
			return err
		}
		if idx.hookAfterUnlockedCatchUp != nil {
			idx.hookAfterUnlockedCatchUp()
		}

		ok, _, err := idx.residualWithinLimit(auditPath, auditEnd)
		if err != nil {
			return err
		}
		if !ok {
			continue // 殘量仍超限：不取鎖，再掃一輪
		}

		if idx.hookAfterResidualOKBeforeLock != nil {
			idx.hookAfterResidualOKBeforeLock()
		}

		emitMu.Lock() // 3. 殘量達標才取鎖
		idx.holdingEmitLock.Store(true)

		ok, end, err := idx.residualWithinLimit(auditPath, auditEnd) // 4. 鎖內重讀
		if err != nil || !ok {
			idx.holdingEmitLock.Store(false)
			emitMu.Unlock() // 超限即解鎖重試，不在鎖內硬掃
			if err != nil {
				return err
			}
			idx.countUnlockRetry()
			continue
		}

		err = idx.finalCatchUpAndAttach(auditPath, end) // 5. 補掃＋checkpoint＋接回＋解 latch
		idx.holdingEmitLock.Store(false)
		emitMu.Unlock()
		return err
	}

	return ErrRebuildNotConverged // (a) 或 (b)：保留 degraded latch，交給呼叫端 backoff
}

// bulkRebuild：五步序列第 1 步。把 rebuildCursor 定位到起掃點，然後掃到目前的
// audit 檔尾（初始高水位）。不持 emitMu。
//
// 起掃點取「rebuildCursor 與 checkpointOffset 的較大者」：
//   - 首次重建時 rebuildCursor 是 0，起掃點就是記憶體 checkpointOffset。用記憶
//     體值而非磁碟 checkpoint.json 是刻意的——checkpoint 只在 turn boundary 落
//     盤，記憶體值逐事件更新，是比磁碟新且與 idx.turns 一致的那一份；且
//     degraded 期間 Observe 是空操作、失敗那次的前移已被回滾，所以記憶體狀態
//     就是「最後一次成功索引到哪裡」的忠實快照，從那裡續掃不會重複也不會遺
//     漏。
//   - 上一輪 RuntimeRebuild 未收斂而中止時 rebuildCursor > checkpointOffset
//     （checkpoint 依 §3.5.4 沒有前移），此時必須保留 rebuildCursor：backoff
//     重試要從已索引位置續掃、**不重跑 bulk**，否則已寫進 turn file 的 record
//     會被重複補寫一次。
//
// 與啟動期不同，這裡不做 checkpointTrustedLocked 的可信度判定：runtime 期間
// audit 只會 append、不會被換掉或裁切，而記憶體 checkpointOffset 直接來自
// AppendReceipt，沒有「超前／落在非行邊界／event id 對不上」的可能。可信度是
// 重啟才需要回答的問題（Task 17）。
func (idx *Index) bulkRebuild(auditPath string) error {
	idx.mu.Lock()
	if idx.rebuildCursor < idx.checkpointOffset {
		idx.rebuildCursor = idx.checkpointOffset
		idx.rebuildLastEventID = idx.checkpointLastEventID
	}
	idx.mu.Unlock()
	return idx.catchUpUnlocked(auditPath)
}

// scanSegmentBytes：鎖外掃描（bulk 與 catch-up）每次持有 idx.mu 的最大掃描
// 量。掃完一段就釋放 idx.mu、再重新取得續掃下一段。
//
// 為什麼必須分段：**不能只看「degraded 期間 Observe 取鎖後只做一件事」就以為
// 它不會被阻塞**——Observe 等鎖的時間就是整段掃描的時間。而接線後 append 路徑
// 是 emitMu → Observe → idx.mu，所以一個 append 執行緒會**握著 emitMu 卡在
// idx.mu 上**，被停住的不是 UI 而是整條 provider 事件管線。那正是第 4 步「不
// 得在鎖內硬掃」要禁止的形狀，只是換成另一把鎖，不能因為 §3.5.7 的字面只講
// emitMu 就放過。
//
// 為什麼是 256KB：取「單段掃描的停頓小到感覺不出來」與「重取鎖／重開檔的固定
// 成本不要放大」之間。events.jsonl 單筆事件常態在數百 bytes 到數 KB，256KB 約
// 是數十到數百筆，單段解析時間在毫秒級；同時它是 MaxCatchUpBytes（1MB）的
// 1/4，代表即使殘量剛好卡在收斂上限，鎖外 catch-up 也至少會讓出 idx.mu 四
// 次。真正需要調整的訊號是實測到 append 路徑因重建而出現可觀測的延遲尖峰，屆
// 時往下調即可——分段本身不影響正確性（rebuildCursor 是續掃形狀，段與段之間的
// 狀態是完整的）。
const scanSegmentBytes = 256 << 10

// unlimitedScanBudget：啟動期掃描不需要分段（§3.2.4：尚無並行 append，也還沒
// 有任何讀取端），一次掃到底。
const unlimitedScanBudget = int64(math.MaxInt64)

// catchUpUnlocked：五步序列第 1／2 步的掃描本體。從 rebuildCursor 續掃到目前檔
// 尾，**不持 emitMu**（audit 照常 append），idx.mu 則是**每段取一次**：掃滿
// scanSegmentBytes 就釋放，讓 Observe／RecentTurns 等待鎖的一方有機會插進來，
// 再重新取得續掃。
//
// 迴圈終止：scanAuditRangeLocked 的 budget 是在處理每筆事件**之前**檢查的，所
// 以每段至少會處理一筆事件（即使該筆單獨就超過 budget），進度必然前進；掃到檔
// 尾時該段回傳 scanned==0，迴圈結束。
func (idx *Index) catchUpUnlocked(auditPath string) error {
	for {
		idx.mu.Lock()
		scanned, err := idx.scanIntoRebuildCursorLocked(auditPath, scanSegmentBytes)
		idx.mu.Unlock()

		idx.countLockSegment()
		if idx.hookBetweenScanSegments != nil {
			idx.hookBetweenScanSegments()
		}
		if err != nil {
			return err
		}
		if scanned == 0 {
			return nil // 已到檔尾
		}
	}
}

// scanIntoRebuildCursorLocked：呼叫端須持有 idx.mu。從 rebuildCursor 起掃，最
// 多處理 budget bytes，把進度記進 rebuildCursor／rebuildLastEventID 而**不碰
// checkpoint、不寫 checkpoint.json**——degraded 期間 checkpoint 依 §3.5.4 不得
// 前移，只有成功接回（finalCatchUpAndAttach）才一次寫入。回傳本次實際掃過的
// byte 數。
//
// 進度逐事件更新（而非掃完才一次更新）：中途出錯時 rebuildCursor 停在最後一
// 筆成功處理的事件結尾，重試從那裡續掃，已寫進 turn file 的 record 不會被重複
// 補寫。
func (idx *Index) scanIntoRebuildCursorLocked(auditPath string, budget int64) (int64, error) {
	from := idx.rebuildCursor
	_, _, err := idx.scanAuditRangeLocked(auditPath, from, budget, func(receipt appcore.AppendReceipt, _ bool) error {
		idx.rebuildCursor = receipt.EndOffset
		idx.rebuildLastEventID = receipt.EventID
		return nil
	})
	return idx.rebuildCursor - from, err
}

// residualWithinLimit：殘量（audit 檔尾減去 rebuildCursor）是否同時低於
// MaxCatchUpBytes 與 MaxCatchUpRecords。
//
// 呼叫時**不持 idx.mu**：auditEnd 是外部注入的 callback，持鎖呼叫它等於把未知
// 的程式碼放進 idx.mu 的臨界區。byte 上限先判、超限就直接回 false，所以
// record 計數最多只會讀 MaxCatchUpBytes 這麼多 bytes，且只數換行、不解析
// JSON——判斷「值不值得取鎖」的成本必須遠小於真正的補掃。
//
// 檔尾回報值小於等於 cursor（append-only 檔理論上不會，但注入的 auditEnd 可能
// 回報保守值）視為殘量 0、達標。
//
// 一併回傳這次讀到的 end：第 4 步通過檢查後，第 5 步的鎖內補掃要以**同一個
// end** 為終點（見 finalCatchUpAndAttach），不能各自重新取一次。
func (idx *Index) residualWithinLimit(auditPath string, auditEnd auditEndFunc) (bool, int64, error) {
	end, err := auditEnd()
	if err != nil {
		return false, 0, fmt.Errorf("replayindex: audit end: %w", err)
	}

	idx.mu.Lock()
	cursor := idx.rebuildCursor
	idx.mu.Unlock()

	if end <= cursor {
		return true, end, nil
	}
	if end-cursor > MaxCatchUpBytes {
		return false, end, nil
	}
	records, err := countRecordsBetween(auditPath, cursor, end)
	if err != nil {
		return false, end, err
	}
	return records <= MaxCatchUpRecords, end, nil
}

// countRecordsBetween：[from, to) 這段 audit 內的完整 record 筆數（數換行，不
// 解析 JSON）。呼叫端已先確認這段不超過 MaxCatchUpBytes。實際讀不滿（檔案比
// 回報的檔尾短）時只數讀到的部分，不視為錯誤：殘量會在下一輪重新評估。
func countRecordsBetween(auditPath string, from, to int64) (int, error) {
	f, err := os.Open(auditPath)
	if err != nil {
		return 0, fmt.Errorf("replayindex: open audit %s: %w", auditPath, err)
	}
	defer f.Close()

	buf := make([]byte, to-from)
	n, err := f.ReadAt(buf, from)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("replayindex: read audit %s residual at %d: %w", auditPath, from, err)
	}
	return bytes.Count(buf[:n], []byte("\n")), nil
}

// finalCatchUpAndAttach：五步序列第 5 步，呼叫端須持有 emitMu（因此本函式執行
// 期間 audit 不可能再有新 append，「掃到檔尾」即「掃到全部」）。
//
// 順序不可調換：補掃 → 前移 checkpoint → 落盤 → 解除 latch。latch 必須最後
// 解——它一解除，Observe 就會恢復處理新事件並從 checkpointOffset 之後接續，所
// 以 checkpoint 必須先反映補掃結果。checkpoint 落盤失敗則把記憶體值回滾並保留
// latch：寧可維持 degraded 等下一輪重試，也不能讓「記憶體宣稱已接回、磁碟仍停
// 在舊位置」這個狀態存活到下次重啟。
//
// 「接回 writer」在本套件裡就是解除 degraded latch——Index 沒有獨立的 writer
// handle，Observe 是唯一寫入路徑，latch 解除的那一刻它就恢復工作，而此刻
// emitMu 仍在手上，保證沒有 in-flight 的 append 被漏掉。
//
// end 是第 4 步剛通過收斂檢查的那個檔尾，補掃終點必須**夾在它**、而不是掃到檔
// 案真實 EOF：兩者一般相同（持有 emitMu 期間不會有新 append），但 auditEnd 若
// 低報（sink 有未 flush 的 buffer、或呼叫端實作有誤），掃到 EOF 會讓鎖內處理量
// 靜默超過凍結上限——maxLockedScanBytes 只是事後記錄，不會攔住任何東西。夾在
// end 之後，超出的部分留給下一輪：latch 沒解除，下一次 RuntimeRebuild 會從
// rebuildCursor 續掃。
//
// 夾在 end 之後多一道檢查：補掃完若檔案實際上還有 rebuildCursor 之後的資料，
// 代表 auditEnd 低報了檔尾（違反 auditEndFunc 的契約）。此刻持有 emitMu、不可
// 能有 in-flight append，所以「檔案大小 > cursor」是明確的契約違反訊號。這種
// 情況必須 fail loud、保留 latch：若照樣接回，latch 解除後 Observe 會從
// checkpoint（= 低報的 end）繼續，中間那段永遠不會有人索引——把「鎖內處理量靜
// 默超標」換成「索引靜默留缺口」，兩個都不能接受。
//
// 這裡**不分段釋放 idx.mu**（與鎖外 catch-up 相反）：補掃量已被收斂上限與 end
// 夾住、有界，而且第 5 步的原子性正是建立在「補掃到接回之間沒有任何人插進來」
// 之上。
func (idx *Index) finalCatchUpAndAttach(auditPath string, end int64) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	budget := end - idx.rebuildCursor
	if budget < 0 {
		budget = 0
	}
	scanned, err := idx.scanIntoRebuildCursorLocked(auditPath, budget)
	if scanned > idx.maxLockedScanBytes {
		idx.maxLockedScanBytes = scanned
	}
	if err != nil {
		return err
	}
	info, serr := os.Stat(auditPath)
	if serr != nil {
		// stat 失敗不能當成「檢查通過」——那會讓這道護欄在最需要它的時候靜默
		// 關掉，正好是它要防的失效模式。此刻持有 emitMu，audit 檔 stat 不到本
		// 身就是異常訊號，回錯並保留 latch。
		return fmt.Errorf("replayindex: stat audit %s（接回前的檔尾核對）: %w", auditPath, serr)
	}
	if info.Size() > idx.rebuildCursor {
		return fmt.Errorf(
			"replayindex: auditEnd 低報檔尾（回報 %d，實際到 %d 仍有未索引資料），保留 degraded latch",
			end, info.Size())
	}

	prevOffset, prevEventID := idx.checkpointOffset, idx.checkpointLastEventID
	idx.checkpointOffset = idx.rebuildCursor
	idx.checkpointLastEventID = idx.rebuildLastEventID
	if werr := idx.writeCheckpointFile(); werr != nil {
		idx.checkpointOffset, idx.checkpointLastEventID = prevOffset, prevEventID
		return werr
	}

	idx.clearDegradedLocked()
	return nil
}

// beginRebuildRun：一輪 RuntimeRebuild 的入口守衛。**兩個前提在同一次 idx.mu
// 臨界區內一起檢查並 CAS**——分成兩次取鎖等於自己造一個 check-then-act 競態。
//
// single-flight 是強制的、不是文件約定（同 ErrNotDegraded 的標準：把呼叫端前提
// 變成不變量）。兩輪並行會壞在兩處：
//
//   - A 跑完第 5 步、解除 latch、放掉 emitMu 之後，B 才取得 emitMu，卻帶著**過
//     期的 end** 進 finalCatchUpAndAttach，把 checkpointOffset 覆寫成同樣過期的
//     rebuildCursor——latch 已解除、Observe 早就推過 checkpoint（但不會推
//     rebuildCursor），結果是 **checkpoint 倒退並落盤**。
//   - A 的 endRebuildRun 會在 B 還在跑時把 rebuildSeen 設成 nil，兩者的
//     begin/end 互踩，B 的去重靜默失效。
//
// 這個缺口是本 task 的分段釋放（見 scanSegmentBytes）放大的：分段之前 bulk 全程
// 持 idx.mu，並行者大部分時間被擋在鎖上；分段之後窗口顯著拉長。Task 20 要接的
// 正是最容易疊起來的形狀（ErrRebuildNotConverged → backoff 重試排程）。
//
// 一併在同一個臨界區做本輪初始化：統計歸零（per-call 語意，「本輪用掉多少嘗
// 試」，不是行程累計）與開啟去重。rebuildCursor **不在**歸零範圍內：它是跨輪保
// 留的索引進度（見 bulkRebuild）。去重整輪開著（含分段掃描與第 5 步的鎖內補
// 掃），但跨輪不保留鍵集——backoff 重試是新的一輪，重新從磁碟載入才反映得出上
// 一輪實際寫成功了哪些。
func (idx *Index) beginRebuildRun() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if !idx.degraded {
		return ErrNotDegraded
	}
	if idx.rebuilding {
		return ErrRebuildInProgress
	}
	idx.rebuilding = true

	idx.catchUpAttempts = 0
	idx.unlockRetries = 0
	idx.maxLockedScanBytes = 0
	idx.lockSegments = 0
	idx.beginRebuildDedupLocked()
	return nil
}

// endRebuildRun：釋放 single-flight 旗標並關閉去重。
func (idx *Index) endRebuildRun() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.rebuilding = false
	idx.endRebuildDedupLocked()
}

// countCatchUpAttempt／countUnlockRetry／countLockSegment：本輪統計的維護，歸零
// 在 beginRebuildRun。

func (idx *Index) countCatchUpAttempt() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.catchUpAttempts++
}

func (idx *Index) countUnlockRetry() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.unlockRetries++
}

func (idx *Index) countLockSegment() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.lockSegments++
}

// eventIDAtOffset：讀取 auditPath 在 offset 處起始的那一行、回傳其 event id
// （用於補回 open turn 的 firstEventID，該 offset 來自 checkpoint 的
// open_turn_start_offset，保證是某行的起點）。
func eventIDAtOffset(auditPath string, offset int64) (string, error) {
	f, err := os.Open(auditPath)
	if err != nil {
		return "", fmt.Errorf("replayindex: open audit %s: %w", auditPath, err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("replayindex: seek audit %s to %d: %w", auditPath, offset, err)
	}
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", fmt.Errorf("replayindex: read audit %s at %d: %w", auditPath, offset, err)
	}
	var env contract.Envelope
	if jerr := json.Unmarshal(bytes.TrimRight(line, "\n"), &env); jerr != nil {
		return "", fmt.Errorf("replayindex: malformed audit line at offset %d: %w", offset, jerr)
	}
	return env.EventID, nil
}
