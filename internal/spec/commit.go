package spec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrCommitStale is returned by ConfirmSpecCommit when the current HEAD
	// OID or the current scoped tree digest no longer matches the
	// CommitToken returned by PreviewSpecCommit — content or HEAD changed
	// after preview, so the previewed diff no longer describes what would be
	// committed.
	ErrCommitStale = errors.New("spec: commit token stale — content or HEAD changed since preview")

	// ErrStagedChangesPresent is returned by ConfirmSpecCommit when the
	// index contains staged changes outside the managed spec scope. We fail
	// closed rather than risk disturbing or silently committing unrelated
	// staged work.
	ErrStagedChangesPresent = errors.New("spec: staged changes outside managed scope present")
)

// managedScopeRoots are the top-level managed paths passed to `git add -A`
// as a pathspec. See activeScopePathspecs for why the full list can't
// always be passed as-is.
var managedScopeRoots = []string{"spec/features", "spec/nfr", "spec/glossary.md", "spec/context-map"}

// CommitToken binds a previewed SpecCommit to the exact repo state it was
// previewed against: the HEAD commit OID and a canonical digest over the
// in-scope worktree content (see ReadScopedWorktree + ManifestDigest). The
// digest is content-only — each entry is path + sha256 of raw bytes, with no
// file mode — so it binds content (path + content hash) over the managed scope,
// NOT file mode. Any scoped add/delete/rename/content change, or any HEAD move,
// changes at least one field, which ConfirmSpecCommit uses to detect staleness.
type CommitToken struct {
	HeadOID    string
	TreeDigest string
}

// headOIDOrUnborn returns the current HEAD commit OID, or "" if HEAD does
// not resolve (unborn HEAD on a repo with no commits yet) — that is a valid
// state, not an error, so PreviewSpecCommit/ConfirmSpecCommit can still run
// before the first commit. `git rev-parse HEAD`'s failure message is
// locale-dependent, so any failure here is treated as unborn rather than
// pattern-matched on stderr text; a genuinely broken repo will still fail
// loudly on the git calls that follow (ReadScopedWorktree, git add/commit).
func (r *GitRepo) headOIDOrUnborn() string {
	oid, err := r.HeadCommit()
	if err != nil {
		return ""
	}
	return oid
}

// scopedTreeDigest computes the canonical digest over the current in-scope
// worktree content — this is what CommitToken.TreeDigest binds to.
func (r *GitRepo) scopedTreeDigest() (string, error) {
	entries, err := r.ReadScopedWorktree()
	if err != nil {
		return "", err
	}
	return ManifestDigest(entries)
}

// activeScopePathspecs returns the managedScopeRoots entries that are safe
// to pass to `git add -A`/`git diff` as a pathspec: those that currently
// exist on disk, OR (when headOID is non-empty) were tracked under HEAD. A
// root that was deleted from disk but is still tracked in HEAD MUST stay in
// the list — otherwise `git add -A -- <roots>` never even sees the deletion
// (the path is gone, so a stat-existence filter silently drops it) and the
// deletion is never staged/committed. Passing a pathspec that matches
// nothing at all (never existed, never tracked) makes `git add`/`git diff`
// fail with "pathspec did not match any files", so roots that are neither
// on disk nor in HEAD are omitted instead.
func (r *GitRepo) activeScopePathspecs(headOID string) ([]string, error) {
	var out []string
	for _, p := range r.sc.Roots {
		if _, err := os.Stat(filepath.Join(r.root, p)); err == nil {
			out = append(out, p)
			continue
		}
		if headOID == "" {
			continue // unborn HEAD: nothing can be tracked yet
		}
		tracked, err := r.git("ls-tree", "-r", "--name-only", "HEAD", "--", p)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(tracked)) != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// scopedDiffForDisplay renders a human-readable diff of in-scope changes:
// `git diff HEAD` over the active managed paths (this shows deletions, not
// just modifications, since it compares the worktree directly against the
// HEAD tree) plus a listing of untracked in-scope files. This is for UI
// display only — it is never used to decide what gets committed.
func (r *GitRepo) scopedDiffForDisplay(headOID string) (string, error) {
	var b strings.Builder
	if headOID != "" {
		paths, err := r.activeScopePathspecs(headOID)
		if err != nil {
			return "", err
		}
		if len(paths) > 0 {
			out, err := r.git(append([]string{"diff", "HEAD", "--"}, paths...)...)
			if err != nil {
				return "", err
			}
			b.Write(out)
		}
	}
	statusOut, err := r.git(append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, r.sc.Roots...)...)
	if err != nil {
		// No scoped path tracked/existing yet — nothing more to show.
		return b.String(), nil
	}
	for _, line := range strings.Split(string(statusOut), "\n") {
		if !strings.HasPrefix(line, "??") {
			continue
		}
		path := statusPath(line)
		if !r.sc.Match(path) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(r.root, path))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "+++ new file: %s\n%s\n", path, content)
	}
	return b.String(), nil
}

// checkNoOutOfScopeStaged fails closed with ErrStagedChangesPresent if the
// index has any staged change outside the managed spec scope. Guaranteeing
// perfect index isolation is hard, so this is the required fallback: refuse
// to touch the index/commit at all rather than risk disturbing or including
// unrelated staged work.
func (r *GitRepo) checkNoOutOfScopeStaged() error {
	out, err := r.git("diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(string(out), "\n") {
		if path == "" {
			continue
		}
		if !r.sc.Match(path) {
			return ErrStagedChangesPresent
		}
	}
	return nil
}

// PreviewSpecCommit returns a CommitToken binding {current HEAD OID, current
// scoped TreeDigest} together with a human-readable diff for display. The
// token must be passed unchanged to ConfirmSpecCommit, which re-verifies
// both fields before committing.
func (r *GitRepo) PreviewSpecCommit() (CommitToken, string, error) {
	headOID := r.headOIDOrUnborn()
	digest, err := r.scopedTreeDigest()
	if err != nil {
		return CommitToken{}, "", err
	}
	diff, err := r.scopedDiffForDisplay(headOID)
	if err != nil {
		return CommitToken{}, "", err
	}
	return CommitToken{HeadOID: headOID, TreeDigest: digest}, diff, nil
}

// ConfirmSpecCommit re-reads the current HEAD OID and scoped TreeDigest; if
// either differs from tok, the repo moved since preview and it returns
// ErrCommitStale rather than commit stale content. It then fails closed with
// ErrStagedChangesPresent if any staged change lies outside the managed
// scope, and otherwise stages the managed scope (pathspec-restricted
// `git add -A`, never a global `git add -A`) and commits it. `-A` is
// required, not `git add --`, so a managed path that was deleted on disk
// still has its deletion staged — see activeScopePathspecs.
func (r *GitRepo) ConfirmSpecCommit(tok CommitToken, message string) error {
	headOID := r.headOIDOrUnborn()
	if headOID != tok.HeadOID {
		return ErrCommitStale
	}
	digest, err := r.scopedTreeDigest()
	if err != nil {
		return err
	}
	if digest != tok.TreeDigest {
		return ErrCommitStale
	}
	if err := r.checkNoOutOfScopeStaged(); err != nil {
		return err
	}
	paths, err := r.activeScopePathspecs(headOID)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("spec: nothing in managed scope to commit")
	}
	if _, err := r.git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	if _, err := r.git("commit", "-m", message); err != nil {
		return err
	}
	return nil
}
