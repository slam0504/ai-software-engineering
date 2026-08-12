package spec

import (
	"errors"
	"testing"
)

type fakeRepo struct {
	head     string
	heads    []string // 若非空，HeadCommit 逐次回傳不同值（模擬 HEAD 在快照期間移動）；否則固定回 head
	headCall int
	clean    bool
	worktree [][]FileEntry // 逐次呼叫回不同集合（模擬掃描期間變動）
	headTree []FileEntry
	wtCall   int
}

func (f *fakeRepo) HeadCommit() (string, error) {
	if len(f.heads) > 0 {
		h := f.heads[min(f.headCall, len(f.heads)-1)]
		f.headCall++
		return h, nil
	}
	return f.head, nil
}
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

// 驗證 committed snapshot 讀的是 HEAD tree，不是 worktree（TOCTOU 安全性關鍵）；
// 同時驗證 happy path 回傳值：(ManifestDigest(headTree), "git:sha1:"+head, nil)。
func TestBuildCommittedSnapshotReadsHeadTreeNotWorktree(t *testing.T) {
	headTree := []FileEntry{{Path: "spec/glossary.md", SHA256: "head-content"}}
	worktree := []FileEntry{{Path: "spec/glossary.md", SHA256: "worktree-content"}}
	r := &fakeRepo{head: "c1", clean: true, headTree: headTree, worktree: [][]FileEntry{worktree}}

	digest, base, err := BuildCommittedSnapshot(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, err := ManifestDigest(headTree)
	if err != nil {
		t.Fatalf("ManifestDigest(headTree): %v", err)
	}
	if digest != want {
		t.Fatalf("want digest from headTree %q, got %q (worktree must not be used)", want, digest)
	}
	if base != "git:sha1:c1" {
		t.Fatalf("want base git:sha1:c1, got %q", base)
	}
}

func TestBuildCommittedSnapshotRejectsHeadMoved(t *testing.T) {
	r := &fakeRepo{
		heads:    []string{"c1", "c2"}, // HEAD₁="c1"、HEAD₂="c2" → 不一致
		clean:    true,
		headTree: []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}},
	}
	if _, _, err := BuildCommittedSnapshot(r); err == nil {
		t.Fatal("HEAD moved during snapshot must reject")
	}
}
