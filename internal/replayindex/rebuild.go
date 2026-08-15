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
// append）——runtime 重建（有並行 append、需要 bulk＋鎖外 catch-up＋鎖內收
// 斂的完整序列）是 Task 19 範圍，本檔不處理。
package replayindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// maxLineWindow：checkpointTrustedLocked／lineEventIDEndingAt 驗證 checkpoint
// 時，往回讀取的最大 window（bytes）。checkpoint 只會落在 turn boundary 事
// 件（canonical user message 或 terminal state_change）的行尾，這兩種事件
// 本身都不攜帶大量內容（delta 的大塊文字不會觸發 boundary），1MB 對單行給
// 了充裕餘裕。這個常數是 replayindex 存在的理由本身：驗證 checkpoint 不能
// 隨 events.jsonl 成長而變慢，見 lineEventIDEndingAt。
const maxLineWindow = 1 << 20

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
// turn 的 firstEventID。全程持 idx.mu；呼叫時尚無並行 append（§3.2.4）。
//
// 特意重新從磁碟讀入 checkpoint.json（見 reloadCheckpointFromDiskLocked），
// 不信任 Open() 當時快取在記憶體裡的值——VerifyOrRebuild 是獨立於 Open 的驗
// 證步驟，其職責就是核對「磁碟現在記的狀態」是否仍與 audit 一致。
func (idx *Index) VerifyOrRebuild(auditPath string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := idx.reloadCheckpointFromDiskLocked(); err != nil {
		return err
	}

	var auditSize int64
	if info, err := os.Stat(auditPath); err == nil {
		auditSize = info.Size()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("replayindex: stat audit %s: %w", auditPath, err)
	}

	trusted, err := idx.checkpointTrustedLocked(auditPath, auditSize)
	if err != nil {
		return err
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
			return err
		}
		from = 0
	} else if err := idx.hydrateOpenTurnFirstEventIDsLocked(auditPath); err != nil {
		// 已存在的 checkpoint 若記錄某 WSID 有 open turn，loadCheckpoint 只
		// 帶回 offset（§3.5.5），firstEventID 尚未知——從 audit 補讀，否則
		// 之後這個 turn 收尾時 appendTurnRecord 會寫出空字串的 FirstEventID。
		return err
	}

	return idx.rescanFromLocked(auditPath, from)
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
func (idx *Index) checkpointTrustedLocked(auditPath string, auditSize int64) (bool, error) {
	if idx.checkpointOffset == 0 {
		// offset 0 卻宣稱有 last_event_id 是自相矛盾的資料，不可信。
		return idx.checkpointLastEventID == "", nil
	}
	if idx.checkpointOffset < 0 || idx.checkpointOffset > auditSize {
		return false, nil // 超前／offset 超界
	}
	id, found, err := lineEventIDEndingAt(auditPath, idx.checkpointOffset)
	if err != nil {
		return false, err
	}
	return found && id == idx.checkpointLastEventID, nil // event ID 不符 → false
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
	f, err := os.Open(auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 沒有 audit 檔可掃（例如全新 workspace）
		}
		return fmt.Errorf("replayindex: open audit %s: %w", auditPath, err)
	}
	defer f.Close()

	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return fmt.Errorf("replayindex: seek audit %s to %d: %w", auditPath, from, err)
		}
	}

	r := bufio.NewReader(f)
	cur := from
	for {
		line, rerr := r.ReadBytes('\n')
		if n := int64(len(line)); n > 0 {
			trimmed := bytes.TrimRight(line, "\n")
			if len(bytes.TrimSpace(trimmed)) > 0 {
				var env contract.Envelope
				if jerr := json.Unmarshal(trimmed, &env); jerr != nil {
					return fmt.Errorf("replayindex: malformed audit line at offset %d: %w", cur, jerr)
				}
				receipt := appcore.AppendReceipt{StartOffset: cur, EndOffset: cur + n, EventID: env.EventID}
				boundary, aerr := idx.applyTurnState(env, receipt)
				if aerr != nil {
					return aerr
				}
				idx.checkpointOffset = receipt.EndOffset
				idx.checkpointLastEventID = receipt.EventID
				if boundary {
					if werr := idx.writeCheckpointFile(); werr != nil {
						return werr // 啟動期無並行、無 degraded latch 概念：直接 fail loud
					}
				}
			}
			cur += n
		}
		if rerr != nil {
			break // EOF 或讀取錯誤都代表已到掃描終點
		}
	}
	return idx.writeCheckpointFile()
}

// lineEventIDEndingAt：找出 auditPath 中「行的結尾恰好落在 target offset」
// 的那一行、回傳其 event id。**O(1) 相對檔案總大小**——只讀 target 之前最多
// maxLineWindow bytes（見該常數說明），不從頭掃整份 audit：重啟驗證 checkpoint
// 若退化成又一次全掃 events.jsonl，就違背了 replayindex 存在的理由（讓重啟
// 不必全掃）。找不到（target 不在任何行邊界上，或該行無法解析）回傳
// found=false、非錯誤——由呼叫端決定這代表 checkpoint 不可信，不是硬性 I/O
// 失敗。
func lineEventIDEndingAt(auditPath string, target int64) (id string, found bool, err error) {
	if target <= 0 {
		return "", false, nil
	}
	f, err := openAuditFile(auditPath)
	if err != nil {
		return "", false, fmt.Errorf("replayindex: open audit %s: %w", auditPath, err)
	}
	defer f.Close()

	windowStart := target - maxLineWindow
	if windowStart < 0 {
		windowStart = 0
	}
	buf := make([]byte, target-windowStart)
	if _, err := f.ReadAt(buf, windowStart); err != nil {
		return "", false, nil // window 讀不滿（例如 target 超過檔案長度）：視為對不上
	}
	if buf[len(buf)-1] != '\n' {
		return "", false, nil // target 沒有落在行邊界上
	}
	body := buf[:len(buf)-1] // 去掉行尾換行
	// 找 body 內最後一個換行——其後到 body 結尾就是 target 對應的那一整行。
	// windowStart 可能切在更早一行的中間，但那不影響這裡：我們只在乎「最後
	// 一個」換行之後的內容，不需要 window 起點恰好對齊行邊界。
	lineStart := bytes.LastIndexByte(body, '\n')
	line := body[lineStart+1:]
	var env contract.Envelope
	if jerr := json.Unmarshal(line, &env); jerr != nil {
		return "", false, nil // 該行本身無法解析（可能是超長行被 window 切斷）：視為對不上
	}
	return env.EventID, true, nil
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
