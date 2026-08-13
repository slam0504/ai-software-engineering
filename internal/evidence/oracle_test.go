package evidence

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/spec"
)

func TestParseOracleDeclAcceptsLegalPatterns(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - "tests/**"
  - "internal/evidence/testdata/**"
  - "scripts/run_oracle.sh"
`)
	d, err := ParseOracleDecl(b)
	if err != nil {
		t.Fatalf("want legal decl to parse, got error: %v", err)
	}
	if d.Version != 1 || len(d.Patterns) != 3 {
		t.Fatalf("unexpected decl: %+v", d)
	}
}

func TestParseOracleDeclRejectsMidSegmentWildcard(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - "internal/*/testdata/**"
`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want mid-segment wildcard pattern rejected")
	}
}

func TestParseOracleDeclRejectsSingleSegmentGlob(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - "*.go"
`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want single-segment glob pattern rejected")
	}
}

func TestParseOracleDeclRejectsEmptyPattern(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - ""
`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want empty pattern rejected")
	}
}

func TestParseOracleDeclRejectsAbsolutePath(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - "/etc/passwd"
`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want absolute-path pattern rejected")
	}
}

// TestParseOracleDeclRejectsPathTraversal guards the Scope().Roots
// downstream usage (Task 19 runner, GitRepo.ScopedClean pathspec): a ".."
// segment must be rejected at Parse time, not surface later as an
// unexpected git pathspec error at some other call site.
func TestParseOracleDeclRejectsPathTraversal(t *testing.T) {
	for _, pattern := range []string{"../x/**", "a/../b/**", "../../etc/passwd"} {
		b := []byte("version: 1\npatterns:\n  - \"" + pattern + "\"\n")
		if _, err := ParseOracleDecl(b); err == nil {
			t.Errorf("want path-traversal pattern %q rejected", pattern)
		}
	}
}

func TestParseOracleDeclRejectsUnknownField(t *testing.T) {
	b := []byte(`
version: 1
patterns:
  - "tests/**"
unexpected_field: true
`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want unknown field rejected")
	}
}

func TestParseOracleDeclRejectsNoPatterns(t *testing.T) {
	b := []byte(`version: 1`)
	if _, err := ParseOracleDecl(b); err == nil {
		t.Fatal("want decl with no patterns rejected")
	}
}

func TestOracleDeclMatch(t *testing.T) {
	d := OracleDecl{Version: 1, Patterns: []string{"tests/**", "scripts/run_oracle.sh"}}
	cases := map[string]bool{
		"tests/foo_test.go":     true,
		"tests":                 true,
		"scripts/run_oracle.sh": true,
		"scripts/other.sh":      false,
		"internal/evidence.go":  false,
	}
	for rel, want := range cases {
		if got := d.Match(rel); got != want {
			t.Errorf("Match(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestOracleDeclScope(t *testing.T) {
	d := OracleDecl{Version: 3, Patterns: []string{"tests/**", "scripts/run_oracle.sh"}}
	sc := d.Scope()
	if sc.Version != 3 {
		t.Errorf("Scope().Version = %d, want 3", sc.Version)
	}
	wantRoots := []string{"tests", "scripts/run_oracle.sh"}
	if len(sc.Roots) != len(wantRoots) {
		t.Fatalf("Scope().Roots = %v, want %v", sc.Roots, wantRoots)
	}
	for i, r := range wantRoots {
		if sc.Roots[i] != r {
			t.Errorf("Scope().Roots[%d] = %q, want %q", i, sc.Roots[i], r)
		}
	}
	if !sc.Match("tests/foo_test.go") || sc.Match("other/file.go") {
		t.Error("Scope().Match must delegate to d.Match")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

// TestOracleDigestAtDiffersAcrossCommits guards the core §3.6 property: the
// same oracle-surface declaration digested at two different commits, where
// the in-scope test file content differs, must produce two different
// digests — otherwise a silent oracle-file edit between plan commit and
// evidence commit would go undetected.
func TestOracleDigestAtDiffersAcrossCommits(t *testing.T) {
	dir := initRepo(t)
	d := OracleDecl{Version: 1, Patterns: []string{"tests/**"}}

	os.MkdirAll(filepath.Join(dir, "tests"), 0o755)
	os.WriteFile(filepath.Join(dir, "tests", "oracle_test.go"), []byte("v1"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "c1")
	c1, err := currentHead(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(dir, "tests", "oracle_test.go"), []byte("v2-different"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "c2")
	c2, err := currentHead(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	r := spec.NewGitRepo(dir, d.Scope())
	digest1, err := OracleDigestAt(r, d, c1)
	if err != nil {
		t.Fatal(err)
	}
	digest2, err := OracleDigestAt(r, d, c2)
	if err != nil {
		t.Fatal(err)
	}
	if digest1 == digest2 {
		t.Fatalf("digest must differ across commits with different oracle content, both got %s", digest1)
	}

	// Re-digesting the same commit must be stable.
	digest1Again, err := OracleDigestAt(r, d, c1)
	if err != nil {
		t.Fatal(err)
	}
	if digest1 != digest1Again {
		t.Fatalf("digest must be stable for the same commit: %s vs %s", digest1, digest1Again)
	}
}

func currentHead(t *testing.T, dir string) (string, error) {
	t.Helper()
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
