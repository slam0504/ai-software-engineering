package recorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Meta struct {
	Provider            string   `json:"provider"`
	CLIVersion          string   `json:"cli_version"`
	Argv                []string `json:"argv"`
	CWD                 string   `json:"cwd"`
	RecordedAt          string   `json:"recorded_at"`
	ExitCode            *int     `json:"exit_code,omitempty"`             // v1.7：*int——執行中省略；已退出必填（0 也保留）
	ProcessStillRunning bool     `json:"process_still_running,omitempty"` // v1.6：長駐 server 回合證據
	StderrTail          string   `json:"stderr_tail,omitempty"`
	RecorderError       string   `json:"recorder_error,omitempty"`
}

type Recorder struct {
	mu   sync.Mutex // v1.6：c2s／s2c 並行 tee 安全
	f    *os.File
	dir  string
	name string
	err  error
}

var caseNameRe = regexp.MustCompile(`^(claude|codex)-[A-Za-z0-9._-]+$`) // 合法 basename，無路徑分隔符

func New(dir, caseName, ext string) (*Recorder, error) {
	if ext != ".ndjson" && ext != ".jsonl" { // v1.5：provider-scoped 副檔名白名單
		return nil, fmt.Errorf("recorder: unsupported ext %q", ext)
	}
	if !caseNameRe.MatchString(caseName) { // v1.6：防路徑逃逸與 glob 掃不到的檔名
		return nil, fmt.Errorf("recorder: invalid case name %q", caseName)
	}
	if (ext == ".ndjson") != strings.HasPrefix(caseName, "claude-") { // v1.6：prefix ↔ ext 一致
		return nil, fmt.Errorf("recorder: case %q does not match ext %q", caseName, ext)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, caseName+ext))
	if err != nil {
		return nil, err
	}
	return &Recorder{f: f, dir: dir, name: caseName}, nil
}

func (r *Recorder) Line(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.f.Write(append(append([]byte(nil), b...), '\n'))
	if err != nil && r.err == nil {
		r.err = err
	}
	return err
}

func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) CloseWith(m Meta) error { // v1.4：close / meta / latched 錯誤全部回傳，meta 仍盡力寫
	r.mu.Lock()
	defer r.mu.Unlock()
	closeErr := r.f.Close()
	if r.err == nil && closeErr != nil {
		r.err = closeErr
	}
	if r.err != nil {
		m.RecorderError = r.err.Error()
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	metaErr := os.WriteFile(filepath.Join(r.dir, r.name+".meta.json"), b, 0o644)
	return errors.Join(r.err, metaErr)
}
