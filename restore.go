package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// restore.json（M1.5 plan D6 的 provider-keyed 恢復索引）自 M3b per-WSID durable
// metadata writer 起**降級為 legacy 來源**（owner 2026-08-17 D6）：續聊身分／
// task label／view boundary 三項的權威改為 workspace-sessions.json 的 per-WSID
// Entry。這裡只剩兩個消費者——§3.2.5 的一次性 legacy 遷移，以及
// backfillResumeFromLegacy 的升級補寫（搬完呼叫 ClearResume 把舊值清掉）。
// 檔案刻意保留不刪：它是 M3a 使用者的最後一份備份。
//
// **「只讀不寫」要說準**：production 不再寫入那三個 metadata 欄位，但
// openRestoreStore 在**首次使用**與**壞檔重建**時仍會落盤（既有行為，見下方
// os.IsNotExist／malformed 兩條分支），backfill 完成後也會寫一次 ClearResume。
//
// restoreEntry：單一 provider 的恢復索引。
type restoreEntry struct {
	ViewStartEventID string `json:"view_start_event_id"` // 僅 NewSession 重設；> 此 ID 的事件屬本 view
	ResumeSessionID  string `json:"resume_session_id"`   // staged candidate 於 Accept 成功後 commit
	TaskID           string `json:"task_id"`
}

// restoreStore：restore.json 的唯一 ownership（單一 mutex；temp file + atomic
// rename、0600；malformed 容錯——壞檔重建、fail loud 由呼叫端記錄）。
type restoreStore struct {
	mu      sync.Mutex
	path    string
	entries map[string]restoreEntry
}

func openRestoreStore(path, highWatermark string) (*restoreStore, error) {
	rs := &restoreStore{path: path, entries: map[string]restoreEntry{}}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(b, &rs.entries); jerr != nil {
			// malformed：重建（不讓全部恢復失敗），以 high-watermark 初始化
			rs.entries = freshEntries(highWatermark)
			if werr := rs.persistLocked(); werr != nil {
				return rs, fmt.Errorf("restore.json malformed (%v) and rebuild failed: %w", jerr, werr)
			}
			return rs, fmt.Errorf("restore.json malformed, rebuilt at high-watermark: %w", jerr)
		}
	case os.IsNotExist(err):
		// 首次使用／升級：以當下 audit high-watermark 初始化——不把既有
		// events.jsonl 全部歷史當成 view 重放
		rs.entries = freshEntries(highWatermark)
		if werr := rs.persistLocked(); werr != nil {
			return rs, werr
		}
	default:
		return rs, err
	}
	for _, p := range []string{"claude", "codex"} { // 缺 provider 條目補齊
		if _, ok := rs.entries[p]; !ok {
			rs.entries[p] = restoreEntry{ViewStartEventID: highWatermark}
		}
	}
	return rs, nil
}

func freshEntries(highWatermark string) map[string]restoreEntry {
	return map[string]restoreEntry{
		"claude": {ViewStartEventID: highWatermark},
		"codex":  {ViewStartEventID: highWatermark},
	}
}

// persistLocked：temp file + atomic rename（0600）。呼叫端持鎖。
func (rs *restoreStore) persistLocked() error {
	b, err := json.MarshalIndent(rs.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := rs.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, rs.path)
}

func (rs *restoreStore) Get(provider string) restoreEntry {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.entries[provider]
}

// CommitResume：Accept 成功後 commit（staged candidate → durable）。
// persist 失敗回滾記憶體 entry（第三輪 P1-4：否則另一 provider 的後續成功寫入
// 會把這筆失敗變更一起持久化）。
func (rs *restoreStore) CommitResume(provider, sessionID, taskID string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	old := rs.entries[provider]
	e := old
	if sessionID != "" {
		e.ResumeSessionID = sessionID
	}
	e.TaskID = taskID
	rs.entries[provider] = e
	if err := rs.persistLocked(); err != nil {
		rs.entries[provider] = old // 回滾
		return err
	}
	return nil
}

// ClearResume：清掉該 provider 的續聊身分（resume id ＋ taskID），**保留
// ViewStartEventID**——與 ResetView 的差別就在這裡：view 視窗是 provider 層的
// 重放起點，前移它會連帶影響同 provider 其他 session 的歷史判定
// （restoreSessions 以 replayViewWindow 判 dormant），移除一個 session 不該動它。
// 失敗時 entry 不變。
func (rs *restoreStore) ClearResume(provider string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	old := rs.entries[provider]
	e := old
	e.ResumeSessionID, e.TaskID = "", ""
	rs.entries[provider] = e
	if err := rs.persistLocked(); err != nil {
		rs.entries[provider] = old
		return err
	}
	return nil
}

// auditHighWatermark：events.jsonl 最後一筆 valid envelope 的 event_id（無檔＝""）。
func auditHighWatermark(eventsPath string) string {
	f, err := os.Open(eventsPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e struct {
			EventID string `json:"event_id"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.EventID != "" {
			last = e.EventID
		} // malformed 行跳過
	}
	return last
}

// RestoredView：RestoreViews binding 的 per-provider 回傳。
type RestoredView struct {
	Envelopes       []contract.Envelope `json:"envelopes"`
	ResumeSessionID string              `json:"resumeSessionId"`
	TaskID          string              `json:"taskId"`
}

// replayViewWindow：讀 events.jsonl，回傳 provider 相符且 event_id > viewStart 的
// 全部 audited envelopes（含空 session_id 與無 ID 雜訊；跨多個 session）。
// 唯讀：不 spawn、不回寫 audit；malformed 行跳過不中斷。
func replayViewWindow(eventsPath, provider, viewStart string) []contract.Envelope {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []contract.Envelope
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e contract.Envelope
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // malformed 行跳過
		}
		if e.Provider != provider || e.EventID == "" {
			continue
		}
		// Only genuine provider-session events belong in a provider view window.
		// Provider session events go through contract.Wrap, which leaves Scope
		// empty; only EmitWorkspace sets scope="workspace" and EmitAssist sets
		// scope="session"+purpose="spec_assist". So exclude workspace/gate
		// envelopes (defensive — they also carry no provider) and, critically,
		// isolated SpecAssist events: those share the provider but must never be
		// replayed through session.apply, or their delta/message would leak into
		// the provider Chat and inflate totals (frozen §5.1).
		if e.Scope == "workspace" || e.Purpose == "spec_assist" {
			continue
		}
		if viewStart != "" && e.EventID <= viewStart {
			continue
		}
		out = append(out, e)
	}
	return out
}

// scanLegacyWindow 讀取 events.jsonl，篩選無 WorkspaceSessionID 的 provider 事件。
// 與 replayViewWindow 差異：(1) 回傳 error；(2) 檔案不存在 → (nil,nil)；
// (3) 只保留 WorkspaceSessionID == ""；(4) Scanner.Err() 檢查。
func scanLegacyWindow(eventsPath, provider, viewStart string) ([]contract.Envelope, error) {
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open events file: %w", err)
	}
	defer f.Close()
	var out []contract.Envelope
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e contract.Envelope
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // malformed 行跳過
		}
		if e.Provider != provider || e.EventID == "" {
			continue
		}
		// Only genuine provider-session events belong in a provider view window.
		// Provider session events go through contract.Wrap, which leaves Scope
		// empty; only EmitWorkspace sets scope="workspace" and EmitAssist sets
		// scope="session"+purpose="spec_assist". So exclude workspace/gate
		// envelopes (defensive — they also carry no provider) and, critically,
		// isolated SpecAssist events: those share the provider but must never be
		// replayed through session.apply, or their delta/message would leak into
		// the provider Chat and inflate totals (frozen §5.1).
		if e.Scope == "workspace" || e.Purpose == "spec_assist" {
			continue
		}
		if viewStart != "" && e.EventID <= viewStart {
			continue
		}
		// Legacy window 只保留無 WorkspaceSessionID 的事件（post-WSID 事件已在
		// 新 provider session 內）。
		if e.WorkspaceSessionID != "" {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan events file: %w", err)
	}
	return out, nil
}

func (a *App) eventsPath() string { return filepath.Join(a.stateDir, "events.jsonl") }
