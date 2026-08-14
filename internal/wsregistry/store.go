// Package wsregistry：workspace-sessions.json 的 durable metadata store（M3b
// spec §3.2.1／§3.6.1）。重啟後還原 session 清單的唯一權威。
//
// 只持久化 durable metadata（WSID／provider／resume 交握資訊／建立時間／
// tombstone）。starting／active／ending／busy／approval pending 等 runtime
// state 一律不持久化——app crash 後這些狀態已失去真實 owner，不可原樣恢復。
// 白名單靠型別強制：Entry 根本不宣告這些欄位。
package wsregistry

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// schemaVersion：固定值，不讀設定。
const schemaVersion = 2

// Entry：單一 workspace session 的 durable metadata（白名單即型別本身）。
type Entry struct {
	WSID             string `json:"wsid"`
	Provider         string `json:"provider"`
	ResumeSessionID  string `json:"resume_session_id"`
	TaskLabel        string `json:"task_label"`
	ViewStartEventID string `json:"view_start_event_id"`
	CreatedAt        string `json:"created_at"`
	RemovedAt        string `json:"removed_at"`
	RemoveReason     string `json:"remove_reason"`
}

// Layout：pinned／focused workspace 排列（durable，非 runtime state）。
type Layout struct {
	Pins    []string `json:"pins"`
	Focused string   `json:"focused"`
}

// fileFormat：workspace-sessions.json 的完整內容。
type fileFormat struct {
	SchemaVersion int              `json:"schema_version"`
	Entries       map[string]Entry `json:"entries"`
	Layout        Layout           `json:"layout"`
	Migrated      bool             `json:"migrated"`
}

// Store：workspace-sessions.json 的唯一 ownership（單一 mutex；temp file +
// atomic rename、0600；persist 失敗回滾記憶體，同 restore.go 慣例）。
type Store struct {
	mu   sync.Mutex
	path string
	file fileFormat
}

// Open：讀取既有檔案，或於首次使用時以空白狀態初始化並落盤。
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		// malformed 一律直接回錯，不像 restore.go:34-41 那樣「重建並回錯」：
		// restore.go 重建只丟失可再生的 view 視窗（下次重放 audit 即可復
		// 原），而這裡的 entries 是使用者所有 session 的權威記錄，重建等於
		// 把它們全部抹掉、不可再生，所以刻意不沿用那個自動修復策略。
		if jerr := json.Unmarshal(b, &s.file); jerr != nil {
			return nil, fmt.Errorf("wsregistry: malformed %s: %w", path, jerr)
		}
		// 0 視為舊檔／缺欄位，維持現行接受行為；非 0 且不等於目前
		// schemaVersion 一律拒絕。schema_version 是凍結常數，另一半語意
		// 就是「非此版本」不可靜默接受——否則下一次任何寫入會把 version
		// 蓋回目前值並落盤，造成未預期版本間的靜默資料遺失。
		if s.file.SchemaVersion != 0 && s.file.SchemaVersion != schemaVersion {
			return nil, fmt.Errorf("wsregistry: %s schema_version=%d, want %d（不支援的版本，拒絕載入）", path, s.file.SchemaVersion, schemaVersion)
		}
		if s.file.Entries == nil {
			s.file.Entries = map[string]Entry{}
		}
	case os.IsNotExist(err):
		s.file = fileFormat{SchemaVersion: schemaVersion, Entries: map[string]Entry{}}
		if werr := s.persistLocked(); werr != nil {
			return nil, werr
		}
	default:
		return nil, err
	}
	return s, nil
}

// persistLocked：temp file + atomic rename（0600）。呼叫端持鎖。
func (s *Store) persistLocked() error {
	s.file.SchemaVersion = schemaVersion
	b, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Put：新增或覆寫一筆 entry。若該 WSID 已被 Remove tombstone，拒絕覆寫——
// 否則呼叫端傳入未帶 RemovedAt 的 Entry（例如 replay index 從 audit 重建時
// 對已知 WSID 呼叫 Put）就會靜默清掉 tombstone，讓已移除的 session 復活，
// 正是 spec §3.6.1 tombstone 機制要防的情況。
// persist 失敗回滾記憶體（第三輪 P1-4 慣例）。
func (s *Store) Put(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.file.Entries[e.WSID]
	if existed && old.RemovedAt != "" {
		return fmt.Errorf("wsregistry: wsid %q is tombstoned, refusing to revive via Put", e.WSID)
	}
	s.file.Entries[e.WSID] = e
	if err := s.persistLocked(); err != nil {
		if existed {
			s.file.Entries[e.WSID] = old
		} else {
			delete(s.file.Entries, e.WSID)
		}
		return err
	}
	return nil
}

// Remove：使用者明確移除——留 tombstone（RemovedAt／RemoveReason），因為
// replay index 重建時看到 audit 裡的 WSID 才不會把已移除的 session 復活。
func (s *Store) Remove(wsid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.file.Entries[wsid]
	if !ok {
		return fmt.Errorf("wsregistry: wsid %q not found", wsid)
	}
	e := old
	e.RemovedAt = time.Now().UTC().Format(time.RFC3339)
	e.RemoveReason = reason
	s.file.Entries[wsid] = e
	if err := s.persistLocked(); err != nil {
		s.file.Entries[wsid] = old
		return err
	}
	return nil
}

// DeleteUncommitted：建立交易失敗的回滾——整筆刪除、不留 tombstone，因為
// 失敗的建立不該在 registry 永久留痕。冪等：wsid 不存在時視為已達目標
// 狀態（Put persist 失敗時已自行回滾記憶體，呼叫端的 abort 路徑常態性會
// 遇到「entry 根本不在 registry」，no-op 避免退化成 check-then-act 競態）。
// 若該 wsid 已是 tombstone（使用者已明確 Remove），拒絕刪除——這種情況下
// 呼叫端傳入的 wsid 顯然不是「本次建立失敗」的那一筆，整筆刪除會抹掉
// audit 需要的 tombstone 痕跡。
func (s *Store) DeleteUncommitted(wsid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.file.Entries[wsid]
	if !existed {
		return nil
	}
	if old.RemovedAt != "" {
		return fmt.Errorf("wsregistry: wsid %q is tombstoned, refusing to delete", wsid)
	}
	delete(s.file.Entries, wsid)
	if err := s.persistLocked(); err != nil {
		s.file.Entries[wsid] = old
		return err
	}
	return nil
}

// Get：回傳指定 wsid 的 entry（含 tombstone；DeleteUncommitted 後的 wsid 不存在）。
func (s *Store) Get(wsid string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.file.Entries[wsid]
	return e, ok
}

// entryCount：registry 目前持久化的 entry 總數，含 tombstone（未 export，
// package 內部用；目前只給 migrate.go 的第二層防禦判斷「registry 是否應
// 視為全新」——tombstone 也算數，因為丟掉它就等於把 §3.6.1 tombstone 要防
// 的「已移除 session 復活」的洞打開）。
func (s *Store) entryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.file.Entries)
}

// Live：排除 tombstone（RemovedAt 非空）與已刪除的 entries。排序：CreatedAt
// 遞增，同值以 WSID 為 tie-break——map 迭代順序不穩定，不排序的話每次呼叫
// （含每次重啟）順序都可能不同，會讓 session 清單 UI 跳動、也讓依賴順序
// 的呼叫端測試不穩定。
func (s *Store) Live() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.file.Entries))
	for _, e := range s.file.Entries {
		if e.RemovedAt == "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].WSID < out[j].WSID
	})
	return out
}

// SetLayout：更新 pinned／focused 排列。Pins 進來時深拷貝——若直接存呼叫端
// 傳入的 slice，呼叫端事後原地修改（例如 append 且 cap 有餘裕）會在鎖外
// 污染 store 內部狀態；且 persist 失敗要回滾的 old.Pins 也會指向同一塊已
// 被污染的 backing array，回滾等於回滾到錯的資料，直接否定「記憶體與磁碟
// 不分裂」的不變量。persist 失敗回滾記憶體。
func (s *Store) SetLayout(l Layout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.file.Layout
	s.file.Layout = Layout{Pins: append([]string(nil), l.Pins...), Focused: l.Focused}
	if err := s.persistLocked(); err != nil {
		s.file.Layout = old
		return err
	}
	return nil
}

// Layout：回傳目前的 pinned／focused 排列。Pins 為深拷貝，呼叫端修改回傳值
// 不影響 store 內部狀態（理由同 SetLayout）。
func (s *Store) Layout() Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Layout{Pins: append([]string(nil), s.file.Layout.Pins...), Focused: s.file.Layout.Focused}
}

// Migrated：是否已完成舊格式的一次性遷移（Task 5 消費）。
func (s *Store) Migrated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Migrated
}

// MarkMigrated：把遷移後的 entries 與 migrated marker 一次原子寫入——
// Task 5 的冪等性靠這個原子性：中途 app crash 不會出現「entries 已寫、
// marker 未寫」的半完成狀態，重啟後會整批重跑遷移而不是誤判已完成
//（僅涵蓋 app crash；未做 fsync，斷電情境不在保證範圍內，同 restore.go
// 既有慣例缺口）。
//
// entries 是整批取代既有 s.file.Entries，不是合併——呼叫端必須傳入遷移
// 後完整的目標集合。允許空 slice（legacy 遷移來源本身就沒有 entries 時，
// 例如兩個 provider 的 restore entry 都空，屬合法情況，不算清空）。拒絕
// 空 WSID 與重複 WSID：兩者都代表呼叫端建構 entries 時已經有 bug，讓它
// 靜默通過只會在 registry 裡留下無法定址或互相覆蓋的資料。
func (s *Store) MarkMigrated(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	newEntries := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if e.WSID == "" {
			return fmt.Errorf("wsregistry: MarkMigrated: entry with empty WSID")
		}
		if _, dup := newEntries[e.WSID]; dup {
			return fmt.Errorf("wsregistry: MarkMigrated: duplicate wsid %q", e.WSID)
		}
		newEntries[e.WSID] = e
	}
	oldEntries := s.file.Entries
	oldMigrated := s.file.Migrated
	s.file.Entries = newEntries
	s.file.Migrated = true
	if err := s.persistLocked(); err != nil {
		s.file.Entries = oldEntries
		s.file.Migrated = oldMigrated
		return err
	}
	return nil
}

// Sync：把目前記憶體狀態重新落盤（例如 shutdown 總序中的最終 flush）。
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked()
}
