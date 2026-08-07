package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func loadAllowUnknownJSONL(path string) map[string]bool {
	m := map[string]bool{}
	b, err := os.ReadFile(path[:len(path)-len(".jsonl")] + ".allow-unknown")
	if err != nil {
		return m
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if s := string(bytes.TrimSpace(line)); s != "" {
			m[s] = true
		}
	}
	return m
}

func TestReplay(t *testing.T) {
	// v1.4：committed fixtures 為空即 FAIL（非 skip）；glob provider-scoped。
	fixtures, _ := filepath.Glob("../../testdata/fixtures/codex-*.jsonl")
	if len(fixtures) == 0 {
		t.Fatal("no committed codex fixture — replay would be vacuous; commit testdata/fixtures/codex-*.jsonl first")
	}
	recordings, _ := filepath.Glob("../../.workbench/recordings/codex-*.jsonl")
	for _, group := range [][]string{fixtures, recordings} {
		for _, f := range group {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			allowed := loadAllowUnknownJSONL(f)
			for i, line := range bytes.Split(data, []byte("\n")) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				var env struct {
					Dir   string          `json:"dir"`
					Frame json.RawMessage `json:"frame"`
				}
				if err := json.Unmarshal(line, &env); err != nil || len(env.Frame) == 0 {
					t.Errorf("%s line %d: bad direction envelope: %s", f, i+1, line)
					continue
				}
				var fr Frame
				if err := json.Unmarshal(env.Frame, &fr); err != nil {
					t.Errorf("%s line %d: malformed frame: %s", f, i+1, env.Frame)
					continue
				}
				switch env.Dir {
				case "c2s":
					if fr.Method == "" && fr.ID != nil {
						continue // client 對 server request 的回覆
					}
					if !ClientMethods[fr.Method] {
						t.Errorf("%s line %d: c2s method %q not in methods.go constant set", f, i+1, fr.Method)
					}
				case "s2c":
					switch {
					case fr.Method != "" && fr.ID == nil: // notification → 事件映射
						ev := MapEvent(fr.Method, fr.Params)
						if !ev.Valid() {
							t.Errorf("%s line %d: mapped event invalid: %s", f, i+1, env.Frame)
						}
						if ev.Kind == contract.KindUnknown && !allowed[fr.Method] {
							t.Errorf("%s line %d: unknown method %q not allow-listed", f, i+1, fr.Method)
						}
					case fr.ID != nil && (len(fr.Result) > 0 || len(fr.Error) > 0): // response 形狀
					case fr.ID != nil && fr.Method != "": // server→client request
					default:
						t.Errorf("%s line %d: unclassifiable s2c frame: %s", f, i+1, env.Frame)
					}
				default:
					t.Errorf("%s line %d: bad dir %q", f, i+1, env.Dir)
				}
			}
		}
	}
}
