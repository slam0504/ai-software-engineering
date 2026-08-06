package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

func loadAllowUnknown(ndjsonPath string) map[string]bool {
	m := map[string]bool{}
	b, err := os.ReadFile(ndjsonPath[:len(ndjsonPath)-len(".ndjson")] + ".allow-unknown")
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

func topLevelType(line []byte) string {
	var h struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(line, &h)
	return h.Type
}

func TestContractReplay(t *testing.T) {
	// v1.4：(1) glob 一律 provider-scoped，Codex 錄流不得流入 claude.Decode；
	//        (2) committed fixture 為空即 FAIL——不允許 vacuous pass。
	fixtures, _ := filepath.Glob("../../testdata/fixtures/claude-*.ndjson")
	if len(fixtures) == 0 {
		t.Fatal("no committed claude fixture — replay would be vacuous; commit testdata/fixtures/claude-*.ndjson first")
	}
	recordings, _ := filepath.Glob("../../.workbench/recordings/claude-*.ndjson")
	for _, group := range [][]string{fixtures, recordings} {
		for _, f := range group {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			allowed := loadAllowUnknown(f)
			for i, line := range bytes.Split(data, []byte("\n")) {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				ev := Decode(line)
				if ev.Kind == contract.KindMalformed {
					t.Errorf("%s line %d malformed", f, i+1)
				}
				if ev.Kind == contract.KindUnknown && !allowed[topLevelType(line)] {
					t.Errorf("%s line %d unknown %q not allow-listed", f, i+1, topLevelType(line))
				}
			}
		}
	}
}
