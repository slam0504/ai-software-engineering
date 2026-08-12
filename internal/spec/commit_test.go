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
	r := NewGitRepo(dir, SpecScope)
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
	r := NewGitRepo(dir, SpecScope)
	tok, _, _ := r.PreviewSpecCommit()
	if err := r.ConfirmSpecCommit(tok, "m"); !errors.Is(err, ErrStagedChangesPresent) {
		t.Fatalf("out-of-scope staged change must fail closed: %v", err)
	}
}

func headTreePaths(t *testing.T, r *GitRepo) map[string]bool {
	t.Helper()
	entries, err := r.ReadScopedHeadTree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Path] = true
	}
	return out
}

// TestConfirmCommitsScopedContentChange is the plain success path: a scoped
// content change is actually committed and the new HEAD tree reflects it.
func TestConfirmCommitsScopedContentChange(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec", "features"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "features", "foo.feature"), []byte("v1"), 0o644)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "c1")

	os.WriteFile(filepath.Join(dir, "spec", "features", "foo.feature"), []byte("v2"), 0o644)
	r := NewGitRepo(dir, SpecScope)
	tok, _, err := r.PreviewSpecCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmSpecCommit(tok, "update foo"); err != nil {
		t.Fatalf("plain content-change confirm should succeed: %v", err)
	}
	head, err := r.HeadCommit()
	if err != nil {
		t.Fatal(err)
	}
	if head == tok.HeadOID {
		t.Fatal("HEAD must have moved after a successful commit")
	}
	blob, err := r.git("cat-file", "blob", "HEAD:spec/features/foo.feature")
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "v2" {
		t.Fatalf("committed content mismatch: got %q, want %q", blob, "v2")
	}
}

// TestConfirmCommitsScopedDeletion reproduces the reviewed bug: a managed
// scoped file is deleted (spec/glossary.md) while another scoped file is
// modified (spec/features/foo.feature). ConfirmSpecCommit must stage AND
// commit the deletion — not silently succeed while leaving the deletion
// uncommitted, which is what a stat-existence-filtered `git add --` did.
func TestConfirmCommitsScopedDeletion(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec", "features"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(dir, "spec", "features", "foo.feature"), []byte("a"), 0o644)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "c1")

	if err := os.Remove(filepath.Join(dir, "spec", "glossary.md")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "spec", "features", "foo.feature"), []byte("b"), 0o644)

	r := NewGitRepo(dir, SpecScope)
	tok, _, err := r.PreviewSpecCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmSpecCommit(tok, "m"); err != nil {
		t.Fatalf("confirm with scoped deletion should succeed: %v", err)
	}

	paths := headTreePaths(t, r)
	if paths["spec/glossary.md"] {
		t.Fatal("deleted scoped file must no longer be in the committed HEAD tree")
	}
	if !paths["spec/features/foo.feature"] {
		t.Fatal("modified scoped file must still be in the committed HEAD tree")
	}
	blob, err := r.git("cat-file", "blob", "HEAD:spec/features/foo.feature")
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "b" {
		t.Fatalf("committed content mismatch: got %q, want %q", blob, "b")
	}
}
