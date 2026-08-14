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

	// truncatedTail 是 RebuildFrameIndex 在檔尾遇到不完整行時丟棄的位元組數；
	// 0 表示乾淨重建（含正常呼叫 newFrameIndex 建構的存活 index）。
	truncatedTail int64
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

func (fi *FrameIndex) noteTruncatedTail(n int64) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.truncatedTail = n
}

// TruncatedTailBytes 回報 RebuildFrameIndex 在檔尾丟棄了多少位元組（不完整的最
// 後一行）；0 表示沒有截斷（含所有非 rebuild 產生的 FrameIndex）。
func (fi *FrameIndex) TruncatedTailBytes() int64 {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return fi.truncatedTail
}

// ingestRow 把單行 wire log JSON 解析進索引；空白行視為合法的 no-op（回 nil）。
// 呼叫端負責決定 unmarshal 失敗時的處置（檔尾截斷容忍 vs 中段損壞 fail loud，
// 見 RebuildFrameIndex）。
func (fi *FrameIndex) ingestRow(wireLogID string, line []byte) error {
	if len(strings.TrimSpace(string(line))) == 0 {
		return nil
	}
	var row wireRow
	if err := json.Unmarshal(line, &row); err != nil {
		return err
	}
	key := FrameKey{WireLogID: wireLogID, Direction: row.Dir, RequestID: extractRequestID(row.Raw)}
	fi.add(key, row.Frame)
	if row.WSID != "" {
		fi.setWSID(row.Frame, row.WSID)
	}
	return nil
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
	ByKey              map[FrameKey][]int
	WSID               map[int]string
	TruncatedTailBytes int64
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
	return FrameIndexSnapshot{ByKey: byKey, WSID: wsid, TruncatedTailBytes: fi.truncatedTail}
}

// RebuildFrameIndex 從一份 wire log JSONL 檔重建 FrameIndex（§3.4.5：frame
// index 必須可由 wire log 完整重建，index 檔損壞不致命）。wire_log_id 取自檔名
// （去除副檔名），與 NewGeneration 的 id 對應。
//
// 損壞處置分級（比照 §3.5.6 replay index 的分級政策，wire log 沒理由用不同標
// 準）：
//   - 檔尾（僅最後一行）不完整 → 容忍：丟棄該行、回傳有效前綴建出的 index。
//     append-only 錄流最典型的損壞就是「app-server 意外死亡時最後一行沒寫完」，
//     這正是本函式存在的動機（reaper／受控 restart／啟動修復），不能因為最常見
//     的崩潰情境就整份放棄。丟棄的位元組數可由 FrameIndex.TruncatedTailBytes
//     查得。
//   - 中段損壞（壞行之後還有有效行）→ fail loud，回傳 error、不回傳部分結果。
//     中段損壞不是 crash 的典型後果，性質不同（例如檔案被截斷後又被覆寫、或磁碟
//     損毀影響中段區塊），沉默跳過該行會讓後續 frame 編號看起來連續、掩蓋了證據
//     缺口。
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

	// 用一行 lookahead 判斷「目前手上這行是不是最後一行」：只有掃到下一行成功
	// 之後，才確定前一行不是檔尾——若前一行 unmarshal 失敗，那一刻已經知道它是
	// 中段損壞（後面還有東西），必須 fail loud。迴圈結束後手上剩的最後一筆才可
	// 能是檔尾截斷，容忍處理。
	var pending []byte
	havePending := false
	lineNo := 0
	pendingLineNo := 0
	for sc.Scan() {
		lineNo++
		if havePending {
			if err := idx.ingestRow(id, pending); err != nil {
				return nil, fmt.Errorf("wirelog: rebuild %s: malformed row at line %d（中段損壞，非檔尾）: %w", path, pendingLineNo, err)
			}
		}
		pending = append([]byte(nil), sc.Bytes()...)
		pendingLineNo = lineNo
		havePending = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if havePending {
		if err := idx.ingestRow(id, pending); err != nil {
			// 檔尾（最後一行）不完整：容忍，丟棄該行、回傳有效前綴。
			idx.noteTruncatedTail(int64(len(strings.TrimSpace(string(pending)))))
		}
	}
	return idx, nil
}
