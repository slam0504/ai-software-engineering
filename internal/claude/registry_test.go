package claude

import (
	"path/filepath"
	"testing"
)

func TestRegistryBindLookup(t *testing.T) {
	r, err := OpenRegistry(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("s1", "/a/b"); err != nil {
		t.Fatal(err)
	}
	if cwd, ok := r.CWD("s1"); !ok || cwd != "/a/b" {
		t.Fatalf("lookup = %q %v", cwd, ok)
	}
	if _, ok := r.CWD("nope"); ok {
		t.Fatal("unknown id must miss")
	}
}
