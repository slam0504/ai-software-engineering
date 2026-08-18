package wirelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FrameAttribution 是一份 generation 的 **compact frame 歸屬索引**（sidecar
// `<wire_log_id>.frames.json`）：wsid → 該代裡歸屬它的 frame 編號。
//
// 為什麼要有它（owner 2026-08-18 裁決的契約第 4 條）：歸屬本身寫在 wire log 每一行的
// wsid 欄位，所以「可重建」不需要這份檔案——但重建要把整份錄流重讀一遍，而讀者
// （App 的收尾證據出口）只需要「這個 WSID 有哪些 frame」。sidecar 讓已收尾的
// generation 用 O(歸屬筆數) 取代 O(整份錄流)。
//
// **它不是權威**：wire log 才是。sidecar 遺失／損壞一律 fallback 重讀錄流再補建
// （見 LoadOrBuildAttribution），所以它可以被安全刪除，也不需要 crash-safe 的
// 寫入協定。
type FrameAttribution struct {
	WireLogID string           `json:"wire_log_id"`
	ByWSID    map[string][]int `json:"by_wsid"`
	// TruncatedTailBytes 只在「由重建產生」時非零，代表檔尾有一段不完整的行被丟棄
	// （見 RebuildFrameIndex）。由 Finalize 直接寫出的那一份恆為 0。
	TruncatedTailBytes int64 `json:"truncated_tail_bytes,omitempty"`
}

// AttributionPath 是某個 generation 的 sidecar 落點。
func AttributionPath(dir, wireLogID string) string {
	return filepath.Join(dir, wireLogID+".frames.json")
}

// AttributionByWSID 回傳 wsid → frame 編號（遞增）的完整歸屬對照；沒有歸屬回空 map。
// 未歸屬的 frame 不出現在任何一格（空字串不是 WSID，見 FramesOf）。
func (fi *FrameIndex) AttributionByWSID() map[string][]int {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	out := map[string][]int{}
	for frame, w := range fi.wsid {
		if w == "" {
			continue
		}
		out[w] = append(out[w], frame)
	}
	for _, v := range out {
		sort.Ints(v) // map 迭代順序不定，落盤內容必須是決定性的
	}
	return out
}

// WriteAttribution 把 idx 的歸屬寫成 sidecar。呼叫端：Generation.Finalize（新
// generation 收尾時直接產生，不必事後重讀）與 LoadOrBuildAttribution 的補建路徑。
func WriteAttribution(dir, wireLogID string, idx *FrameIndex) error {
	fa := FrameAttribution{WireLogID: wireLogID, ByWSID: idx.AttributionByWSID(),
		TruncatedTailBytes: idx.TruncatedTailBytes()}
	b, err := json.MarshalIndent(fa, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AttributionPath(dir, wireLogID), b, 0o644)
}

// LoadOrBuildAttribution 取得某個 generation 的歸屬索引：
//
//  1. sidecar 存在且讀得開 → 直接用（fromSidecar=true）；
//  2. 不存在（或解不開）→ 從 wire log 重建（RebuildFrameIndex）並**補建 sidecar**，
//     下次就走第 1 條。
//
// 錄流本體讀不到／中段損壞時回 error——**不得回一份空的歸屬**：空歸屬與「這一代
// 真的沒有我的 frame」在證據上長得一模一樣，呼叫端必須分得出來（§3.4.5 的 Fail Loud
// 延伸到證據出口）。
//
// sidecar 解不開刻意**不 fail loud**而是重建：它是可再生的衍生檔，不是證據；而
// 「index 檔損壞不致命」正是 §3.4.5 對 frame index 的要求。補建失敗同樣不致命
// （回傳的歸屬照用），但會併進 err 讓呼叫端記錄——下次還是會重讀，不是靜默降級。
func LoadOrBuildAttribution(dir, wireLogID string) (byWSID map[string][]int, fromSidecar bool, err error) {
	b, rerr := os.ReadFile(AttributionPath(dir, wireLogID))
	if rerr == nil {
		var fa FrameAttribution
		if json.Unmarshal(b, &fa) == nil && fa.ByWSID != nil {
			return fa.ByWSID, true, nil
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		// 讀得到檔案卻讀不出內容（權限／IO）：照樣往下重建，但不吞掉原因。
		rerr = fmt.Errorf("wirelog: read attribution sidecar %s: %w", wireLogID, rerr)
	} else {
		rerr = nil
	}

	idx, berr := RebuildFrameIndex(filepath.Join(dir, wireLogID+".jsonl"))
	if berr != nil {
		return nil, false, errors.Join(rerr, berr)
	}
	return idx.AttributionByWSID(), false, errors.Join(rerr, WriteAttribution(dir, wireLogID, idx))
}
