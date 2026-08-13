package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/spec"
)

// testGitRepo is a minimal GitRunner backed by a real git repo under
// t.TempDir(), mirroring internal/spec/gitrepo_test.go's helper
// conventions (initRepo/run) rather than reusing spec's unexported git()
// helper — plan must not depend on spec's git plumbing.
type testGitRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testGitRepo {
	t.Helper()
	dir := t.TempDir()
	g := &testGitRepo{t: t, dir: dir}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		g.run(args...)
	}
	// C0: baseline commit with code files, so both the plan-scope test and
	// the rename-safety test can build on it.
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	// Padded so git's rename detection recognizes an unmodified move as a
	// rename (R100) rather than a delete+add pair.
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte(strings.Repeat("package a\n// filler line to pad size\n", 50)), 0o644)
	g.run("add", "-A")
	g.run("commit", "-m", "C0: code baseline")
	return g
}

func (g *testGitRepo) run(args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %s", args, out)
	}
}

// Git implements GitRunner. Unlike spec.GitRepo's git() helper, it returns
// the raw *exec.ExitError unwrapped so VerifyLineage can inspect the exit
// code (needed to tell "not an ancestor" from a genuine command failure).
func (g *testGitRepo) Git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	return cmd.Output()
}

func (g *testGitRepo) oid(rev string) string {
	g.t.Helper()
	out, err := g.Git("rev-parse", rev)
	if err != nil {
		g.t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func (g *testGitRepo) commitFile(path, content string) {
	g.t.Helper()
	full := filepath.Join(g.dir, filepath.FromSlash(path))
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		g.t.Fatalf("write %s: %v", path, err)
	}
	g.run("add", "-A")
	g.run("commit", "-m", "commit "+path)
}

func (g *testGitRepo) gitMv(oldPath, newPath string) {
	g.t.Helper()
	dst := filepath.Join(g.dir, filepath.FromSlash(newPath))
	os.MkdirAll(filepath.Dir(dst), 0o755)
	g.run("mv", oldPath, newPath)
	g.run("commit", "-m", "rename "+oldPath+" -> "+newPath)
}

func TestVerifyLineage(t *testing.T) {
	g := newTestRepo(t)                // commit C0（code：main.go）
	g.commitFile("plan/P1.yaml", "v1") // C1：只動 plan/**
	if err := VerifyLineage(g, g.oid("HEAD~1"), g.oid("HEAD"), spec.PlanScope.Match); err != nil {
		t.Fatalf("plan-only range must pass: %v", err)
	}
	g.commitFile("main.go", "x") // C2：混入 code 變更
	if err := VerifyLineage(g, g.oid("HEAD~2"), g.oid("HEAD"), spec.PlanScope.Match); err == nil {
		t.Fatal("range with non-plan change must fail") // §3.0
	}
	if err := VerifyLineage(g, g.oid("HEAD"), g.oid("HEAD~2"), spec.PlanScope.Match); err == nil {
		t.Fatal("non-ancestor must fail")
	}
}

func TestVerifyLineageRenameSafety(t *testing.T) {
	g := newTestRepo(t)                // C0：code 檔 src/a.go
	g.gitMv("src/a.go", "plan/a.yaml") // code→plan rename：old path 在 allow 外
	if err := VerifyLineage(g, g.oid("HEAD~1"), g.oid("HEAD"), spec.PlanScope.Match); err == nil {
		t.Fatal("code→plan rename must fail (old path outside allow)")
	}
	// oracle 用法的對偶測試（allow=OracleDecl.Match）在 Task 19 的 runner 測試補：
	// oracle→非 oracle rename 必須拒絕（new path 在 allow 外）。
}
