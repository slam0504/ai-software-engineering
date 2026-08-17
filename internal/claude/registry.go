package claude

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Registry struct {
	mu   sync.Mutex
	path string
}

// registryEntry：一個 claude provider session id 綁到哪裡。
//
// WSID 是 M3b §3.3 加上的（D1）：只有 cwd 的話擋得住「resume 到別的工作目錄」，
// 擋不住「resume 到同一個 workspace 裡別人的 session」——那正是多 session 之後
// 才第一次可達的錯接形狀。空字串＝本 build 之前綁定的舊記錄（見 Lookup）。
type registryEntry struct {
	CWD       string `json:"cwd"`
	WSID      string `json:"wsid"`
	CreatedAt string `json:"created_at"`
}

func OpenRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) load() (map[string]registryEntry, error) {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return nil, err
	}
	m := map[string]registryEntry{}
	return m, json.Unmarshal(b, &m)
}

// Bind：把 provider session id 綁到 (cwd, wsid)。同一個 id 再次 Bind 會整筆
// 覆寫——claude 每次 init 都回報當前 session id，最新一次的綁定就是權威。
func (r *Registry) Bind(sessionID, cwd, wsid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return err
	}
	m[sessionID] = registryEntry{CWD: cwd, WSID: wsid, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(r.path, b, 0o644)
}

// Lookup：該 session id 目前綁到哪個 cwd／WSID。wsid 回空字串代表這筆是本
// build 之前寫下的舊記錄（那時沒有這個欄位），呼叫端據此決定放行或拒絕——
// 這個「不知道」與「知道且不符」是兩件事，不可混為一談。
func (r *Registry) Lookup(sessionID string) (cwd, wsid string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return "", "", false
	}
	e, found := m[sessionID]
	return e.CWD, e.WSID, found
}
