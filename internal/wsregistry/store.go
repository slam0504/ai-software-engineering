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
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// 兩個可辨識的哨兵錯誤：per-WSID 的 durable metadata writer（resume identity／
// task label／view boundary）在這兩種情況下必須被呼叫端當成**良性跳過**、而不是
// 使用者可見錯誤——
//   - ErrEntryNotFound：該 WSID 沒有 registry entry（registry 未接線的測試路徑、
//     或 legacy／半還原狀態）。
//   - ErrTombstoned：使用者已明確移除該 session。
//
// 這兩個判定發生在 Store 自己的 mutex 內、與 Remove 的 tombstone 寫入有全序，
// 所以取代掉的 app 側 check-then-act（舊 resumeWriteAllowed）殘餘 TOCTOU 在此
// 真正關閉：不可能出現「檢查時沒 tombstone、寫入時已 tombstone」的交錯。
var (
	ErrEntryNotFound = errors.New("wsregistry: entry not found")
	ErrTombstoned    = errors.New("wsregistry: entry is tombstoned")
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
//
// ResumeBackfilled 是 schema v2 的 additive 欄位（不升版本）：舊檔沒有它就是
// false，正是「還沒 backfill 過」的正確語意；舊 build 讀新檔會忽略它，最壞情況
// 是再跑一次 backfill，而 BackfillResume 只填空值，重跑無副作用。
type fileFormat struct {
	SchemaVersion    int              `json:"schema_version"`
	Entries          map[string]Entry `json:"entries"`
	Layout           Layout           `json:"layout"`
	Migrated         bool             `json:"migrated"`
	ResumeBackfilled bool             `json:"resume_backfilled"`
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

// mutate：per-WSID 欄位級的**單一交易**更新——找不到／已 tombstone 一律回哨兵
// 錯誤，persist 失敗回滾記憶體。
//
// 刻意不讓呼叫端用 Get＋Put 兩段拼出來：那正是 restore.go 的 CommitSessionID
// 當初為了消除「late init 以舊值覆寫新 commit」的競態而做掉的形狀，而且兩段式
// 讀不到 tombstone 的全序（見 ErrTombstoned 的說明）。
func (s *Store) mutate(wsid string, fn func(*Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.file.Entries[wsid]
	if !ok {
		return fmt.Errorf("%w: %q", ErrEntryNotFound, wsid)
	}
	if old.RemovedAt != "" {
		return fmt.Errorf("%w: %q", ErrTombstoned, wsid)
	}
	e := old
	fn(&e)
	s.file.Entries[wsid] = e
	if err := s.persistLocked(); err != nil {
		s.file.Entries[wsid] = old
		return err
	}
	return nil
}

// CommitResume：StartSession 的 Accept 成功後，把該 WSID 這一次的續聊身分
// （provider 回報的 session／thread id）與 task label 一次寫入。
//
// 兩個空值語意刻意不同：
//   - resumeSessionID 為空＝「這次還不知道 id」（claude 的 init 可能晚於 Accept），
//     保留既有值不動，由稍後的 SetResume 補上。
//   - taskLabel 為空＝「這次沒帶標籤」，同樣保留既有值——TaskLabel 是 session
//     清單 UI 顯示的名字，讓一次沒帶標籤的送出把它清空是使用者可見的退步。
//     D3 裁決沒有 rename 入口，因此「清空 label」不是需要支援的操作。
func (s *Store) CommitResume(wsid, resumeSessionID, taskLabel string) error {
	return s.mutate(wsid, func(e *Entry) {
		if resumeSessionID != "" {
			e.ResumeSessionID = resumeSessionID
		}
		if taskLabel != "" {
			e.TaskLabel = taskLabel
		}
	})
}

// SetResume：claude init 抵達時的單一交易——只更新續聊身分，保留 TaskLabel
// 與 ViewStartEventID（對應 restore.go 舊有的 CommitSessionID 語意）。
func (s *Store) SetResume(wsid, resumeSessionID string) error {
	return s.mutate(wsid, func(e *Entry) { e.ResumeSessionID = resumeSessionID })
}

// ResetView：NewSession（「開新對話」）收尾成功後的單一交易——view boundary 前移
// 到當下的 audit high-watermark，同時清空續聊身分。
//
// 兩者必須同一筆：boundary 前移代表「舊 turn 不再屬於這個 view」，若 resume 還
// 留著，下一次 StartSession 會接回那段已經看不到的對話。TaskLabel **不清**——
// 它是 session 的名字，不是這一段對話的身分。
func (s *Store) ResetView(wsid, viewStartEventID string) error {
	return s.mutate(wsid, func(e *Entry) {
		e.ViewStartEventID, e.ResumeSessionID = viewStartEventID, ""
	})
}

// ResumeBackfilled：是否已完成「provider-keyed restore.json → per-WSID entry」的
// 一次性續聊身分補寫（見 BackfillResume）。
func (s *Store) ResumeBackfilled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.ResumeBackfilled
}

// BackfillResume：升級補寫（D2）——把既有 build 留在 provider-keyed restore.json
// 的續聊身分，一次性搬進對應的 per-WSID entry，並設下不可重跑的 marker。
//
// **marker 與「只填空值」兩層缺一不可**：只靠「只填空值」的話，使用者按過
// NewSession（resume 已被清空）之後重啟，會被 restore.json 那個 stale 值再次
// 回填、靜默復活舊對話。marker 保證這件事一輩子只發生一次。
//
// 沒有可填的 entry 時**仍然**設 marker：那代表「已經檢查過、沒有東西要搬」，
// 不設的話每次啟動都會重新對 stale 的 restore.json 做一次判斷。
//
// 跳過 tombstone 與不存在的 wsid（呼叫端的候選集合可能是上一輪的快照），跳過
// 已有值的 entry（那是比 legacy 更新的資料）。整批一次 persist，失敗回滾記憶體
// ——marker 與 entries 必須同生共死，否則會出現「marker 已設、entries 未填」的
// 半完成狀態，而 marker 是單向的，那些 session 從此永遠補不回來。
func (s *Store) BackfillResume(fill map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldEntries := make(map[string]Entry, len(fill))
	for wsid, id := range fill {
		e, ok := s.file.Entries[wsid]
		if !ok || e.RemovedAt != "" || e.ResumeSessionID != "" || id == "" {
			continue
		}
		oldEntries[wsid] = e
		e.ResumeSessionID = id
		s.file.Entries[wsid] = e
	}
	oldMarker := s.file.ResumeBackfilled
	s.file.ResumeBackfilled = true
	if err := s.persistLocked(); err != nil {
		for wsid, e := range oldEntries {
			s.file.Entries[wsid] = e
		}
		s.file.ResumeBackfilled = oldMarker
		return err
	}
	return nil
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
