package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

func TestGitRepoCommittedSnapshotIgnoresWorktreeEdit(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec"), 0o755)
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("v1"), 0o644)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "c1")
	r := NewGitRepo(dir)
	d1, _, err := BuildCommittedSnapshot(r)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "spec", "glossary.md"), []byte("v2-uncommitted"), 0o644)
	// 委託快照應仍等於 v1（讀 HEAD tree，非 worktree）— 但 dirty 應拒送
	if _, _, err := BuildCommittedSnapshot(r); err == nil {
		t.Fatal("dirty scoped tree must reject")
	}
	entries, _ := r.ReadScopedHeadTree("HEAD")
	d2, _ := ManifestDigest(entries)
	if d1 != d2 {
		t.Fatal("HEAD-tree manifest must be stable regardless of worktree edit")
	}
}

func TestGitRepoRejectsSymlinkInScope(t *testing.T) {
	dir := initRepo(t)
	os.MkdirAll(filepath.Join(dir, "spec", "features"), 0o755)
	os.Symlink("/etc/passwd", filepath.Join(dir, "spec", "features", "evil.feature"))
	r := NewGitRepo(dir)
	if _, err := r.ReadScopedWorktree(); err == nil {
		t.Fatal("symlink in scope must be rejected")
	}
}

func TestGitRepoRejectsSubmoduleInScope(t *testing.T) {
	dir := initRepo(t)
	vendor := filepath.Join(dir, "spec", "features", "vendor")
	os.MkdirAll(vendor, 0o755)
	os.WriteFile(filepath.Join(vendor, ".git"), []byte("gitdir: ../../../.git/modules/vendor\n"), 0o644)
	os.WriteFile(filepath.Join(vendor, "x.feature"), []byte("Feature: vendored"), 0o644)
	r := NewGitRepo(dir)
	if _, err := r.ReadScopedWorktree(); err == nil {
		t.Fatal("submodule/nested repo in scope must be rejected")
	}
}
