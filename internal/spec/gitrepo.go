package spec

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
}

func NewGitRepo(root string) *GitRepo {
	return &GitRepo{root: root}
}

// git runs `git -C root <args>` and returns raw stdout bytes.
func (g *GitRepo) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", g.root}, args...)...)
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

// ScopedClean reports whether the managed spec/ paths (per InScope) have no
// staged, unstaged, or untracked changes. --untracked-files=all expands
// untracked directories into individual file paths so InScope filtering
// works on file granularity.
func (g *GitRepo) ScopedClean() (bool, error) {
	out, err := g.git("status", "--porcelain", "--untracked-files=all", "--", "spec/")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if InScope(statusPath(line)) {
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
		if path == "" || !InScope(path) {
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

// ReadScopedWorktree walks the managed spec/ patterns on disk and hashes raw
// bytes. It rejects symlinks and any non-regular file (fail loud, never
// silently skip) and refuses to descend into nested .git directories so a
// submodule checkout under scope cannot be read as plain files.
func (g *GitRepo) ReadScopedWorktree() ([]FileEntry, error) {
	root := filepath.Join(g.root, "spec")
	var entries []FileEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil // no spec/ dir yet — nothing in scope
			}
			return err
		}
		rel, err := filepath.Rel(g.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !InScope(rel) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("spec: symlink not allowed in scoped tree: %s", rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("spec: non-regular file not allowed in scoped tree: %s", rel)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, FileEntry{Path: rel, SHA256: HashBytes(raw)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
