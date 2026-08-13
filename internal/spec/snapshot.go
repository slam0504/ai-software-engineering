package spec

import "errors"

var ErrConcurrentModification = errors.New("spec: concurrent modification during scan")

type Repo interface {
	HeadCommit() (string, error)
	ScopedClean() (bool, error)
	ReadScopedWorktree() ([]FileEntry, error)
	ReadScopedHeadTree(head string) ([]FileEntry, error)
}

// buildRetries is the max total ReadScopedWorktree calls BuildCurrentManifest
// will make (sliding-window compare against the previous read), not a count
// of double-read "attempts" — see BuildCurrentManifest's doc comment.
const buildRetries = 3

// BuildCurrentManifest is the SpecScope-bound wrapper (kept for existing
// callers) around BuildCurrentManifestScoped.
func BuildCurrentManifest(r Repo) (string, error) {
	return BuildCurrentManifestScoped(r, SpecScope)
}

// BuildCurrentManifestScoped builds the canonical manifest, digested under
// sc, from the worktree and re-reads it to confirm stability. It keeps
// re-reading (sliding-window comparison against the previous read) up to
// buildRetries total reads; if no two consecutive reads agree, it gives up
// rather than return a mixed-snapshot digest.
//
// sc is only used for ManifestDigest's canonical content (scope
// version/patterns) — entry enumeration is entirely r's responsibility
// (r.ReadScopedWorktree), so callers must pass a Repo whose scope matches sc
// (e.g. a GitRepo constructed with the same Scope) or the digest will not
// reflect what r actually enumerated.
func BuildCurrentManifestScoped(r Repo, sc Scope) (string, error) {
	prev, err := r.ReadScopedWorktree()
	if err != nil {
		return "", err
	}
	prevDigest, err := sc.ManifestDigest(prev)
	if err != nil {
		return "", err
	}
	for i := 1; i < buildRetries; i++ {
		cur, err := r.ReadScopedWorktree()
		if err != nil {
			return "", err
		}
		curDigest, err := sc.ManifestDigest(cur)
		if err != nil {
			return "", err
		}
		if curDigest == prevDigest {
			return curDigest, nil
		}
		prevDigest = curDigest
	}
	return "", ErrConcurrentModification
}

// BuildCommittedSnapshot is the SpecScope-bound wrapper (kept for existing
// callers) around BuildCommittedSnapshotScoped.
func BuildCommittedSnapshot(r Repo) (string, string, error) {
	return BuildCommittedSnapshotScoped(r, SpecScope)
}

// BuildCommittedSnapshotScoped is BuildCommittedSnapshot digested under sc
// instead of the fixed SpecScope — see BuildCurrentManifestScoped's doc
// comment for the same r/sc-must-match caveat.
func BuildCommittedSnapshotScoped(r Repo, sc Scope) (string, string, error) {
	head1, err := r.HeadCommit()
	if err != nil {
		return "", "", err
	}
	clean, err := r.ScopedClean()
	if err != nil {
		return "", "", err
	}
	if !clean {
		return "", "", errors.New("spec: scoped tree dirty — commit before 送核")
	}
	entries, err := r.ReadScopedHeadTree(head1)
	if err != nil {
		return "", "", err
	}
	digest, err := sc.ManifestDigest(entries)
	if err != nil {
		return "", "", err
	}
	head2, err := r.HeadCommit()
	if err != nil {
		return "", "", err
	}
	if head1 != head2 {
		return "", "", errors.New("spec: HEAD moved during snapshot — retry")
	}
	return digest, "git:sha1:" + head1, nil
}
