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
		if jerr := json.Unmarshal(b, &s.file); jerr != nil {
			return nil, fmt.Errorf("wsregistry: malformed %s: %w", path, jerr)
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

// Put：新增或覆寫一筆 entry。persist 失敗回滾記憶體（第三輪 P1-4 慣例）。
func (s *Store) Put(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.file.Entries[e.WSID]
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
// 失敗的建立不該在 registry 永久留痕。冪等：wsid 不存在時視為已達目標狀態。
func (s *Store) DeleteUncommitted(wsid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.file.Entries[wsid]
	if !existed {
		return nil
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

// Live：排除 tombstone（RemovedAt 非空）與已刪除的 entries。
func (s *Store) Live() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.file.Entries))
	for _, e := range s.file.Entries {
		if e.RemovedAt == "" {
			out = append(out, e)
		}
	}
	return out
}

// SetLayout：更新 pinned／focused 排列。persist 失敗回滾記憶體。
func (s *Store) SetLayout(l Layout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.file.Layout
	s.file.Layout = l
	if err := s.persistLocked(); err != nil {
		s.file.Layout = old
		return err
	}
	return nil
}

// Layout：回傳目前的 pinned／focused 排列。
func (s *Store) Layout() Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Layout
}

// Migrated：是否已完成舊格式的一次性遷移（Task 5 消費）。
func (s *Store) Migrated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Migrated
}

// MarkMigrated：把遷移後的 entries 與 migrated marker 一次原子寫入——
// Task 5 的冪等性靠這個原子性：中途 crash 不會出現「entries 已寫、marker
// 未寫」的半完成狀態，重啟後會整批重跑遷移而不是誤判已完成。
func (s *Store) MarkMigrated(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldEntries := s.file.Entries
	oldMigrated := s.file.Migrated
	newEntries := make(map[string]Entry, len(entries))
	for _, e := range entries {
		newEntries[e.WSID] = e
	}
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
