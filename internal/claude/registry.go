package claude

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// ErrRegistryClosed：registry 已經在收尾時關閉，之後的寫入一律被拒。
//
// 這是**明確的錯誤**而不是靜默丟棄：收尾之後遲到的 Bind 代表有一條 pump 還在
// 跑，呼叫端必須看得到自己寫不進去（app 側會留一筆稽核）。與 audit writer 的
// 處置刻意不同——那邊關掉之後丟棄是正確行為（lease 即將釋放，本來就不該再寫），
// 這邊則是「磁碟上那份綁定從此凍結」，呼叫端拿到的回傳值是唯一的分辨方式。
var ErrRegistryClosed = errors.New("claude: session registry 已關閉，不再接受寫入")

type Registry struct {
	mu     sync.Mutex
	path   string
	closed bool
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
	if r.closed {
		return ErrRegistryClosed
	}
	m, err := r.load()
	if err != nil {
		return err
	}
	m[sessionID] = registryEntry{CWD: cwd, WSID: wsid, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(r.path, b, 0o644)
}

// Close：收尾時關閉 registry——**與 Bind 共用同一把 mutex**，所以呼叫本身就會
// 等正在執行的那一筆 Bind 把檔案寫完，回來之後不可能再有新的寫入落盤。
//
// 冪等；不持有任何 fd（每次 Bind 自己開關檔案），所以沒有東西要關，這裡關的是
// **寫入權**而不是 handle。之後的 Bind 一律回 ErrRegistryClosed。
//
// Lookup 刻意不受影響：讀取不會改動磁碟事實，擋它只會讓收尾期間的診斷路徑跟著
// 失效，換不到任何單一 writer 的保證。
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
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
