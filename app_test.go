package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/claude"
)

func TestWorkspaceReadSecurity(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.md"), []byte("# hi"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
	os.Symlink(outside, filepath.Join(root, "linkdir"))
	big := make([]byte, 1<<20+1)
	os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644)

	a := NewApp()
	a.workspaceDir, _ = claude.NormalizeCWD(root)

	if got, err := a.ReadWorkspaceFile("ok.md"); err != nil || got != "# hi" {
		t.Fatalf("normal read: %v %q", err, got)
	}
	if _, err := a.ReadWorkspaceFile("../etc/passwd"); err == nil {
		t.Fatal("dot-dot escape must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("link.txt"); err == nil {
		t.Fatal("symlink file to outside must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("linkdir/secret.txt"); err == nil {
		t.Fatal("symlink dir traversal must be rejected")
	}
	if _, err := a.ReadWorkspaceFile("big.bin"); err == nil {
		t.Fatal(">1MB must be rejected before full read")
	}
	if _, err := a.ListWorkspace(".."); err == nil {
		t.Fatal("list escape must be rejected")
	}
}
