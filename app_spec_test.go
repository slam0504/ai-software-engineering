package main

import (
	"errors"
	"testing"
)

func TestSpecWriteNewFileAtomic(t *testing.T) {
	a, _ := newTestApp(t) // workspaceDir = temp
	d, err := a.SpecWrite("spec/features/x.feature", "Feature: X\n", "")
	if err != nil {
		t.Fatal(err)
	}
	_, got, _ := a.SpecRead("spec/features/x.feature")
	if got != d {
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
