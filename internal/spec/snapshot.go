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

// BuildCurrentManifest builds the canonical manifest from the worktree and
// re-reads it to confirm stability. It keeps re-reading (sliding-window
// comparison against the previous read) up to buildRetries total reads; if
// no two consecutive reads agree, it gives up rather than return a
// mixed-snapshot digest.
func BuildCurrentManifest(r Repo) (string, error) {
	prev, err := r.ReadScopedWorktree()
	if err != nil {
		return "", err
	}
	prevDigest, err := ManifestDigest(prev)
	if err != nil {
		return "", err
	}
	for i := 1; i < buildRetries; i++ {
		cur, err := r.ReadScopedWorktree()
		if err != nil {
			return "", err
		}
		curDigest, err := ManifestDigest(cur)
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

func BuildCommittedSnapshot(r Repo) (string, string, error) {
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
	digest, err := ManifestDigest(entries)
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
