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

type registryEntry struct {
	CWD       string `json:"cwd"`
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

func (r *Registry) Bind(sessionID, cwd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return err
	}
	m[sessionID] = registryEntry{CWD: cwd, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(r.path, b, 0o644)
}

func (r *Registry) CWD(sessionID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.load()
	if err != nil {
		return "", false
	}
	e, ok := m[sessionID]
	return e.CWD, ok
}
