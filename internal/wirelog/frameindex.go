package wirelog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FrameKey 索引一筆 wire frame：wire_log_id＋direction＋requestID。§3.4.3 凍結
// direction 為必要欄位——client request 與 server request 各自獨立配發 ID，同一
// 數值在兩個方向可能各自代表完全不同的 RPC 交換，缺 direction 會讓兩者的 frame
// 誤併成同一個 bucket。
type FrameKey struct {
	WireLogID string
	Direction Direction
	RequestID string
}

// FrameIndex 是可重建的 frame 索引：FrameKey → 該 key 底下的 frame 編號（依寫
// 入順序），以及每個 frame 編號目前的 WSID 歸屬（Attribute 標記，未標記則不在
// map 中）。
type FrameIndex struct {
	mu    sync.Mutex
	byKey map[FrameKey][]int
	wsid  map[int]string
}

func newFrameIndex() *FrameIndex {
	return &FrameIndex{byKey: map[FrameKey][]int{}, wsid: map[int]string{}}
}

func (fi *FrameIndex) add(key FrameKey, frame int) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.byKey[key] = append(fi.byKey[key], frame)
}

func (fi *FrameIndex) attribute(key FrameKey, wsid string) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	for _, frame := range fi.byKey[key] {
		fi.wsid[frame] = wsid
	}
}

func (fi *FrameIndex) setWSID(frame int, wsid string) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.wsid[frame] = wsid
}

// Lookup 回傳某個 FrameKey 底下的 frame 編號（依寫入順序），找不到回 nil。
func (fi *FrameIndex) Lookup(key FrameKey) []int {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	v := fi.byKey[key]
	if v == nil {
		return nil
	}
	out := make([]int, len(v))
	copy(out, v)
	return out
}

// FrameIndexSnapshot 是 FrameIndex 某一刻的完整內容快照，供 rebuild 相等性比較
// （TestFrameIndexIsRebuildable）。兩個 map 欄位的相等比較（reflect.DeepEqual）
// 不受 map 迭代順序影響；決定性來自每個 key 底下 []int 的建構順序恆為寫入順序
// （Line／RebuildFrameIndex 皆依序附加，從不透過 map 迭代組裝）。
type FrameIndexSnapshot struct {
	ByKey map[FrameKey][]int
	WSID  map[int]string
}

// Snapshot 回傳目前 FrameIndex 內容的深拷貝快照。
func (fi *FrameIndex) Snapshot() FrameIndexSnapshot {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	byKey := make(map[FrameKey][]int, len(fi.byKey))
	for k, v := range fi.byKey {
		cp := make([]int, len(v))
		copy(cp, v)
		byKey[k] = cp
	}
	wsid := make(map[int]string, len(fi.wsid))
	for k, v := range fi.wsid {
		wsid[k] = v
	}
	return FrameIndexSnapshot{ByKey: byKey, WSID: wsid}
}

// RebuildFrameIndex 從一份 wire log JSONL 檔重建 FrameIndex（§3.4.5：frame
// index 必須可由 wire log 完整重建，index 檔損壞不致命）。wire_log_id 取自檔名
// （去除副檔名），與 NewGeneration 的 id 對應。
//
// 已知限制：只讀 wire log 本身，不會還原 Generation.Attribute 事後標記的 WSID
// （該標記不落盤，見 Attribute 的 doc）。
func RebuildFrameIndex(path string) (*FrameIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	idx := newFrameIndex()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var row wireRow
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("wirelog: rebuild %s: malformed row at line %d: %w", path, lineNo, err)
		}
		key := FrameKey{WireLogID: id, Direction: row.Dir, RequestID: extractRequestID(row.Raw)}
		idx.add(key, row.Frame)
		if row.WSID != "" {
			idx.setWSID(row.Frame, row.WSID)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return idx, nil
}
