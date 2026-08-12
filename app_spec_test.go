package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSpecWriteNewFileAtomic(t *testing.T) {
	a, _ := newTestApp(t) // workspaceDir = temp
	d, err := a.SpecWrite("spec/features/x.feature", "Feature: X\n", "")
	if err != nil {
		t.Fatal(err)
	}
	sf, err := a.SpecRead("spec/features/x.feature")
	if err != nil {
		t.Fatal(err)
	}
	if sf.Digest != d {
		t.Fatal("read digest must match write digest")
	}
}

func TestSpecWriteConflictOnStaleExpectedDigest(t *testing.T) {
	a, _ := newTestApp(t)
	a.SpecWrite("spec/glossary.md", "v1", "")
	if _, err := a.SpecWrite("spec/glossary.md", "v2", "sha256:wrong"); !errors.Is(err, ErrSpecWriteConflict) {
		t.Fatalf("stale expected_digest must conflict: %v", err)
	}
}

func TestSpecWriteRejectsOutOfScope(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.SpecWrite("app.go", "x", ""); err == nil {
		t.Fatal("out-of-scope write must reject")
	}
}

// TestSpecWriteRejectsSymlinkedParentEscape: spec/features pre-exists as a
// symlink pointing outside the workspace (git can carry symlinks, so a
// planted branch/template can arrange this). SpecWrite must detect the
// escape BEFORE creating anything — against the pre-fix code, MkdirAll
// follows the symlink and creates real directories at the external target
// before the containment check ever runs.
func TestSpecWriteRejectsSymlinkedParentEscape(t *testing.T) {
	a, _ := newTestApp(t)
	ext := t.TempDir()

	if err := os.MkdirAll(filepath.Join(a.workspaceDir, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, filepath.Join(a.workspaceDir, "spec", "features")); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SpecWrite("spec/features/newsub/x.feature", "Feature: X\n", ""); err == nil {
		t.Fatal("write through symlinked parent must reject (escapes workspace)")
	}

	if _, err := os.Stat(filepath.Join(ext, "newsub")); !os.IsNotExist(err) {
		t.Fatalf("no directory must be created outside workspace via the symlink; stat err = %v", err)
	}
}
