package spec

import (
	"errors"
	"testing"
)

type fakeRepo struct {
	head     string
	clean    bool
	worktree [][]FileEntry // 逐次呼叫回不同集合（模擬掃描期間變動）
	headTree []FileEntry
	wtCall   int
}

func (f *fakeRepo) HeadCommit() (string, error)                    { return f.head, nil }
func (f *fakeRepo) ScopedClean() (bool, error)                     { return f.clean, nil }
func (f *fakeRepo) ReadScopedHeadTree(string) ([]FileEntry, error) { return f.headTree, nil }
func (f *fakeRepo) ReadScopedWorktree() ([]FileEntry, error) {
	e := f.worktree[min(f.wtCall, len(f.worktree)-1)]
	f.wtCall++
	return e, nil
}

func TestBuildCurrentManifestStableTwice(t *testing.T) {
	same := []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}
	r := &fakeRepo{worktree: [][]FileEntry{same, same}}
	if _, err := BuildCurrentManifest(r); err != nil {
		t.Fatalf("stable double-build should pass: %v", err)
	}
}

func TestBuildCurrentManifestConcurrentModification(t *testing.T) {
	r := &fakeRepo{worktree: [][]FileEntry{
		{{Path: "spec/glossary.md", SHA256: "aa"}},
		{{Path: "spec/glossary.md", SHA256: "bb"}}, // 內容替換（mtime/size 可能不變）
		{{Path: "spec/glossary.md", SHA256: "cc"}},
	}}
	if _, err := BuildCurrentManifest(r); !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("want ErrConcurrentModification, got %v", err)
	}
}

func TestBuildCommittedSnapshotRejectsDirty(t *testing.T) {
	r := &fakeRepo{head: "c1", clean: false, headTree: []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}}
	if _, _, err := BuildCommittedSnapshot(r); err == nil {
		t.Fatal("dirty scoped tree must reject 送核")
	}
}
