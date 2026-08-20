package spec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// GitRepo is the git-backed implementation of Repo. HEAD-tree reads go
// through the git object database (git cat-file), never the worktree — this
// is the TOCTOU-safety boundary: a dirty worktree must never leak into a
// committed snapshot's digest.
type GitRepo struct {
	root string
	sc   Scope
	// ctx：git 子行程的取消來源（nil ＝ 不可取消，僅供不在收尾路徑上的呼叫端）。
	//
	// 需要它的理由：這些 git 呼叫發生在 Wails binding 的交易內，shutdown 必須能
	// 讓它們收斂——一個忽略 TERM 的 git 會讓 inflight.Wait 無限等下去
	// （reviewer 2026-08-20）。
	ctx context.Context
}

func NewGitRepo(root string, sc Scope) *GitRepo {
	return &GitRepo{root: root, sc: sc}
}

// NewGitRepoCtx：帶取消來源的版本（app 的 binding 路徑一律用這個）。
func NewGitRepoCtx(ctx context.Context, root string, sc Scope) *GitRepo {
	return &GitRepo{root: root, sc: sc, ctx: ctx}
}

// git runs `git -C root <args>` and returns raw stdout bytes.
func (g *GitRepo) git(args ...string) ([]byte, error) {
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %v: %s", args, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return out, nil
}

func (g *GitRepo) HeadCommit() (string, error) {
	out, err := g.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ScopedClean reports whether the managed scope's paths (per r.sc.Match)
// have no staged, unstaged, or untracked changes. --untracked-files=all
// expands untracked directories into individual file paths so Match
// filtering works on file granularity.
func (g *GitRepo) ScopedClean() (bool, error) {
	out, err := g.git(append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, g.sc.Roots...)...)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if g.sc.Match(statusPath(line)) {
			return false, nil
		}
	}
	return true, nil
}

// statusPath extracts the path from a `git status --porcelain` line
// ("XY path" or "XY old -> new" for renames).
func statusPath(line string) string {
	if len(line) < 4 {
		return ""
	}
	rest := line[3:]
	if idx := strings.Index(rest, " -> "); idx >= 0 {
		rest = rest[idx+4:]
	}
	return strings.Trim(rest, "\"")
}

// ReadScopedHeadTree enumerates the HEAD tree and reads each in-scope blob
// straight from the object database via `git cat-file blob <head>:<path>`.
// It never touches the worktree, so it is stable regardless of uncommitted
// worktree edits.
func (g *GitRepo) ReadScopedHeadTree(head string) ([]FileEntry, error) {
	out, err := g.git("ls-tree", "-r", "--name-only", head)
	if err != nil {
		return nil, err
	}
	var entries []FileEntry
	for _, path := range strings.Split(string(out), "\n") {
		if path == "" || !g.sc.Match(path) {
			continue
		}
		blob, err := g.git("cat-file", "blob", head+":"+path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, FileEntry{Path: path, SHA256: HashBytes(blob)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// ReadScopedWorktree enumerates exactly the files git considers in scope —
// tracked (--cached) plus untracked-but-not-ignored (--others
// --exclude-standard) — via `git ls-files`, so its view of the managed scope
// matches ScopedClean and ReadScopedHeadTree. This is the fix for the false
// permanent-STALE bug: a raw filesystem walk also enumerated git-ignored
// files (e.g. macOS `.DS_Store`), which the HEAD-tree manifest never contains,
// so the digests diverged with zero real spec change. Deferring enumeration to
// git makes .gitignore / global ignore / .git/info/exclude apply uniformly.
//
// It still rejects symlinks and any non-regular file (fail loud, never
// silently skip) and refuses to read files under a nested-repo boundary so a
// submodule/embedded repo under scope cannot be read as plain files.
func (g *GitRepo) ReadScopedWorktree() ([]FileEntry, error) {
	out, err := g.git(append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--"}, g.sc.Roots...)...)
	if err != nil {
		return nil, err
	}
	var entries []FileEntry
	checkedDirs := map[string]bool{} // memo: dir already verified free of nested .git
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || !g.sc.Match(rel) {
			continue
		}
		if err := g.rejectNestedRepo(rel, checkedDirs); err != nil {
			return nil, err
		}
		abs := filepath.Join(g.root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue // tracked (--cached) but deleted from the worktree — not present content
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("spec: symlink not allowed in scoped tree: %s", rel)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("spec: non-regular file not allowed in scoped tree: %s", rel)
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		entries = append(entries, FileEntry{Path: rel, SHA256: HashBytes(raw)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// rejectNestedRepo fails loud if any in-scope ancestor directory of rel holds a
// `.git` marker. A submodule working tree has a `.git` FILE (gitdir pointer);
// an embedded repo has a `.git` DIR. When that pointer is not itself a live
// repo, `git ls-files` still lists the files beneath it, so this explicit guard
// preserves Task 6's boundary — a nested repo under scope must never be read as
// plain spec files. Checked dirs are memoised so shared ancestors stat once.
func (g *GitRepo) rejectNestedRepo(rel string, checked map[string]bool) error {
	for dir := path.Dir(rel); dir != "." && dir != "/" && g.sc.Match(dir); dir = path.Dir(dir) {
		if checked[dir] {
			continue
		}
		checked[dir] = true
		marker := filepath.Join(g.root, filepath.FromSlash(dir), ".git")
		if _, err := os.Lstat(marker); err == nil {
			return fmt.Errorf("spec: submodule/nested repo not allowed in scope: %s", dir)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
