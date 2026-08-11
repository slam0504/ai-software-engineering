package spec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmRejectsWhenContentChangedAfterPreview(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("draft"), 0o644)
	r := NewGitRepo(dir)
	tok, _, err := r.PreviewSpecCommit()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("changed-after-preview"), 0o644)
	if err := r.ConfirmSpecCommit(tok, "commit spec"); !errors.Is(err, ErrCommitStale) {
		t.Fatalf("content change after preview must reject: %v", err)
	}
}

func TestConfirmFailsClosedWithOutOfScopeStaged(t *testing.T) {
	dir := initRepo(t)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package x"), 0o644)
	run(t, dir, "add", "app.go") // scope 外 staged
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("draft"), 0o644)
	r := NewGitRepo(dir)
	tok, _, _ := r.PreviewSpecCommit()
	if err := r.ConfirmSpecCommit(tok, "m"); !errors.Is(err, ErrStagedChangesPresent) {
		t.Fatalf("out-of-scope staged change must fail closed: %v", err)
	}
}
