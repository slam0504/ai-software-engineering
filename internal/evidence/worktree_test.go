package evidence

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

// ---- test helpers: real git repo via t.TempDir(), no mocks ----

// initRepoTwoCommits creates a real git repo with two commits on file.txt,
// returning the repo root and both commit oids so tests can check out
// commit1 explicitly (proving NewWorktree honors commitOID, not just HEAD).
func initRepoTwoCommits(t *testing.T) (root, commit1, commit2 string) {
	t.Helper()
	root = t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")

	writeFileT(t, root, "file.txt", "version one\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "c1")
	commit1 = strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))

	writeFileT(t, root, "file.txt", "version two\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "c2")
	commit2 = strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))
	return root, commit1, commit2
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := runGit(dir, nil, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func mustRunGitT(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, err := runGit(dir, nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFileT(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// evID returns a unique-per-call evidence id derived from the test name, so
// concurrent/previous test runs never collide on the same real
// os.TempDir()-rooted worktree directory. The test name is folded into a
// short hash rather than used verbatim — NewWorktree now rejects evidence
// ids over 64 bytes (review defense), and t.Name() alone (subtests included)
// can easily exceed that.
func evID(t *testing.T, suffix string) string {
	t.Helper()
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	return fmt.Sprintf("t%x-%s-%d", h.Sum32(), suffix, time.Now().UnixNano())
}

// removeEvidenceDirLeftover is a final safety-net cleanup for the real
// system temp dir: idempotent even if the code under test already removed
// the directory, and even if the test failed before reaching its own
// assertions.
func removeEvidenceDirLeftover(t *testing.T, evidenceID string) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("cleanup: RemoveAll(%q): %v", dir, err)
	}
}

// ---- Step 1: NewWorktree sanitizes evidenceID (review defense) ----

// TestNewWorktreeRejectsInvalidEvidenceID guards against evidenceID being
// used unsanitized to derive a filesystem path (filepath.Join(os.TempDir(),
// "wb-evidence-"+evidenceID)): a path-traversal or otherwise malformed id
// must be rejected before any registry/filesystem mutation, while a
// legitimate ULID-shaped id must still work end-to-end.
func TestNewWorktreeRejectsInvalidEvidenceID(t *testing.T) {
	root, _, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")

	cases := []string{
		"../x",                  // path traversal
		"a/b",                   // embedded path separator
		"has space",             // whitespace
		strings.Repeat("a", 65), // over 64 chars
		"",                      // empty
	}
	for _, id := range cases {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			if _, err := NewWorktree(root, commit2, registryPath, id); err == nil {
				t.Fatalf("NewWorktree(%q): want error for invalid evidence id", id)
			}
		})
	}
	assertNoZombieWorktrees(t, root)

	// A legitimate ULID-shaped id (26 chars, Crockford base32) must still pass.
	validID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	t.Cleanup(func() { removeEvidenceDirLeftover(t, validID) })
	w, err := NewWorktree(root, commit2, registryPath, validID)
	if err != nil {
		t.Fatalf("NewWorktree(valid ULID): %v", err)
	}
	if err := w.Remove(root, registryPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// ---- Step 1: NewWorktree checks out the given commit, not HEAD ----

func TestNewWorktreeChecksOutGivenCommitNotHead(t *testing.T) {
	root, commit1, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, commit1, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(w.Dir, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "version one\n" {
		t.Errorf("worktree content = %q, want commit1 content %q (not HEAD/commit2 %q)", got, "version one\n", commit2)
	}

	// Registry must show intent then active, in order, for this evidence id.
	lines := readRegistryLines(t, registryPath)
	if len(lines) != 2 {
		t.Fatalf("registry lines = %d, want 2 (intent, active)", len(lines))
	}
	if lines[0].Type != "wt_intent" || lines[0].Dir != w.Dir || lines[0].EvidenceID != evidenceID {
		t.Errorf("line 0 = %+v, want wt_intent for %q at %q", lines[0], evidenceID, w.Dir)
	}
	if lines[1].Type != "wt_active" || lines[1].EvidenceID != evidenceID {
		t.Errorf("line 1 = %+v, want wt_active for %q", lines[1], evidenceID)
	}

	if err := w.Remove(root, registryPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	assertNoZombieWorktrees(t, root)
}

func readRegistryLines(t *testing.T, path string) []registryRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var out []registryRecord
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var rec registryRecord
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("unmarshal registry line %q: %v", ln, err)
		}
		out = append(out, rec)
	}
	return out
}

func assertNoZombieWorktrees(t *testing.T, root string) {
	t.Helper()
	out, err := runGit(root, nil, "worktree", "list")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	// Exactly one line: the main working tree itself.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Errorf("git worktree list has zombies: %s", out)
	}
}

// ---- Step 1: ApplyPatch --check fails closed, no half-apply ----

func TestApplyPatchUnapplicableFailsWithoutHalfApply(t *testing.T) {
	root, _, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, commit2, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer func() { _ = w.Remove(root, registryPath) }()

	before, err := os.ReadFile(filepath.Join(w.Dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}

	// A patch whose context line doesn't match anything in file.txt.
	badPatch := []byte(`diff --git a/file.txt b/file.txt
index 0000000..1111111 100644
--- a/file.txt
+++ b/file.txt
@@ -1,1 +1,1 @@
-this context line does not exist in file.txt
+replacement
`)
	if _, err := w.ApplyPatch(badPatch); err == nil {
		t.Fatal("ApplyPatch: want error for unapplicable patch, got nil")
	}

	after, err := os.ReadFile(filepath.Join(w.Dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("worktree mutated by a failed ApplyPatch: before=%q after=%q", before, after)
	}
}

func TestApplyPatchValidReturnsTouchedAndMutatesWorktree(t *testing.T) {
	root, _, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, commit2, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer func() { _ = w.Remove(root, registryPath) }()

	patch := []byte(`diff --git a/file.txt b/file.txt
index 0000000..1111111 100644
--- a/file.txt
+++ b/file.txt
@@ -1,1 +1,1 @@
-version two
+version three
`)
	touched, err := w.ApplyPatch(patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if len(touched) != 1 || touched[0] != "file.txt" {
		t.Errorf("touched = %v, want [file.txt]", touched)
	}

	got, err := os.ReadFile(filepath.Join(w.Dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "version three\n" {
		t.Errorf("worktree content after apply = %q, want %q", got, "version three\n")
	}
}

// TestApplyPatchRenameNumstatReturnsBothPaths guards the oracle-safety
// property called out in the brief: a rename must not let a caller checking
// only one side of the path evade an oracle-surface check.
func TestApplyPatchRenameNumstatReturnsBothPaths(t *testing.T) {
	root := t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, root, "src/foo.txt", "line1\nline2\nline3\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "init")
	head := strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))

	// Build a real rename+modify patch with git itself (git diff -M),
	// exactly as brief §4 assumes callers will produce it.
	runGitT(t, root, "mv", "src/foo.txt", "src/bar.txt")
	writeFileT(t, root, "src/bar.txt", "line1\nlineX\nline3\n")
	patch := mustRunGitT(t, root, "diff", "-M", "HEAD")
	// Reset the origin repo back to a clean HEAD so the worktree we create
	// from `head` below applies this patch fresh.
	runGitT(t, root, "checkout", "--", ".")
	runGitT(t, root, "clean", "-fd")

	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, head, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer func() { _ = w.Remove(root, registryPath) }()

	touched, err := w.ApplyPatch(patch)
	if err != nil {
		t.Fatalf("ApplyPatch(rename patch): %v", err)
	}
	want := map[string]bool{"src/foo.txt": false, "src/bar.txt": false}
	for _, p := range touched {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Errorf("touched = %v, missing rename side %q", touched, p)
		}
	}
}

// TestApplyPatchRenameNonASCIIPathUnquoted guards the HIGH finding from
// Task 18 review: git quotes "rename from "/"rename to " header paths
// (core.quotepath default) whenever a path contains a non-ASCII byte,
// emitting C-style octal escapes like "t\303\244b.txt". Without unquoting,
// touched would contain that literal escaped string instead of the real
// path, diverging from the raw UTF-8 bytes numstat -z returns for the same
// file.
func TestApplyPatchRenameNonASCIIPathUnquoted(t *testing.T) {
	root := t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	const oldName, newName = "src/täb.txt", "src/bäz.txt"
	writeFileT(t, root, oldName, "line1\nline2\nline3\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "init")
	head := strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))

	runGitT(t, root, "mv", oldName, newName)
	writeFileT(t, root, newName, "line1\nlineX\nline3\n")
	patch := mustRunGitT(t, root, "diff", "-M", "HEAD")
	if !strings.Contains(string(patch), `\303\244`) {
		t.Fatalf("test setup: expected patch to contain a C-style quoted non-ASCII escape, got:\n%s", patch)
	}
	runGitT(t, root, "checkout", "--", ".")
	runGitT(t, root, "clean", "-fd")

	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, head, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer func() { _ = w.Remove(root, registryPath) }()

	touched, err := w.ApplyPatch(patch)
	if err != nil {
		t.Fatalf("ApplyPatch(non-ASCII rename patch): %v", err)
	}
	want := map[string]bool{oldName: false, newName: false}
	for _, p := range touched {
		if _, ok := want[p]; ok {
			want[p] = true
		}
		if strings.Contains(p, `\`) || strings.Contains(p, `"`) {
			t.Errorf("touched entry %q still looks quoted/escaped, want raw UTF-8 path", p)
		}
	}
	for p, found := range want {
		if !found {
			t.Errorf("touched = %v, missing unquoted rename side %q", touched, p)
		}
	}
}

// TestApplyPatchAdversarialSelfQuotedRenameHeaderStillCanonical guards the
// evasion the reviewer confirmed: an attacker can hand-craft a patch whose
// "rename from " header wraps a plain ASCII path in quotes with no escapes
// needed (git apply accepts this — quoting is optional, not required, for
// an unescaped path). Without C-style unquoting, the literal quoted string
// `"src/oracle.txt"` would land in touched instead of the canonical
// `src/oracle.txt`, letting an oracle-surface rename evade detection.
func TestApplyPatchAdversarialSelfQuotedRenameHeaderStillCanonical(t *testing.T) {
	root := t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, root, "src/oracle.txt", "hello oracle\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "init")
	head := strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))

	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "wt")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, head, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	defer func() { _ = w.Remove(root, registryPath) }()

	// Hand-crafted: quotes with no escaped bytes inside, which git never
	// emits itself (it only quotes when an escape is actually needed) but
	// git apply accepts anyway.
	patch := []byte(`diff --git a/src/oracle.txt b/src/oracle2.txt
similarity index 100%
rename from "src/oracle.txt"
rename to "src/oracle2.txt"
`)
	touched, err := w.ApplyPatch(patch)
	if err != nil {
		t.Fatalf("ApplyPatch(self-quoted rename patch): %v", err)
	}
	found := false
	for _, p := range touched {
		if p == "src/oracle.txt" {
			found = true
		}
		if p == `"src/oracle.txt"` {
			t.Errorf("touched contains literal quoted string %q, unquoting was not applied", p)
		}
	}
	if !found {
		t.Errorf("touched = %v, missing canonical old path %q (oracle-surface bypass)", touched, "src/oracle.txt")
	}
}

func TestUnquoteCStyle(t *testing.T) {
	cases := []struct{ in, want string }{
		{`src/plain.txt`, `src/plain.txt`},     // not quoted: unchanged
		{`"src/oracle.txt"`, `src/oracle.txt`}, // quoted, no escapes needed: unwrapped
		{`"t\303\244b.txt"`, "t\xc3\xa4b.txt"}, // octal escapes: raw UTF-8 bytes
		{`"a\tb"`, "a\tb"},                     // standard C escape
		{`"a\\b\"c"`, `a\b"c`},                 // escaped backslash + quote
		{`"`, `"`},                             // malformed (single quote char): unchanged
	}
	for _, c := range cases {
		if got := unquoteCStyle(c.in); got != c.want {
			t.Errorf("unquoteCStyle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- Step 1: crash windows, driven through CleanupOrphans ----

// (a0) intent written, directory never created (crash before `worktree
// add` ran at all) — CleanupOrphans must be a no-op error-wise and mark the
// evidence id removed.
func TestCleanupOrphans_A0_IntentOnlyDirNeverCreated(t *testing.T) {
	root, _, _ := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "a0")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })
	dir := filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)

	reg := openTestRegistry(t, registryPath)
	mustAppendRecord(t, reg, registryRecord{Type: "wt_intent", EvidenceID: evidenceID, Dir: dir, At: nowRFC3339()})
	reg.Close()

	if err := CleanupOrphans(root, registryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("want dir %q absent, stat err = %v", dir, err)
	}
	lines := readRegistryLines(t, registryPath)
	if len(lines) != 2 || lines[1].Type != "wt_removed" || lines[1].EvidenceID != evidenceID {
		t.Fatalf("registry lines = %+v, want [intent, removed] for %q", lines, evidenceID)
	}
	assertNoZombieWorktrees(t, root)

	// (c) re-running must not error and must not duplicate the removed line.
	if err := CleanupOrphans(root, registryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans (idempotent re-run): %v", err)
	}
	if got := readRegistryLines(t, registryPath); len(got) != 2 {
		t.Errorf("registry lines after idempotent re-run = %d, want still 2 (no duplicate removed)", len(got))
	}
}

// (a) intent written, directory half-built (crash during `worktree add`:
// simulated by mkdir without ever running git worktree add) — CleanupOrphans
// must remove the directory and mark it removed.
func TestCleanupOrphans_A_IntentDirHalfBuilt(t *testing.T) {
	root, _, _ := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "a")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })
	dir := filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A half-built worktree add might have dropped partial content; a stray
	// file must not stop cleanup.
	writeFileT(t, dir, "partial.txt", "half-built\n")

	reg := openTestRegistry(t, registryPath)
	mustAppendRecord(t, reg, registryRecord{Type: "wt_intent", EvidenceID: evidenceID, Dir: dir, At: nowRFC3339()})
	reg.Close()

	if err := CleanupOrphans(root, registryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("want half-built dir %q removed, stat err = %v", dir, err)
	}
	lines := readRegistryLines(t, registryPath)
	if len(lines) != 2 || lines[1].Type != "wt_removed" {
		t.Fatalf("registry lines = %+v, want [intent, removed]", lines)
	}
	assertNoZombieWorktrees(t, root)
}

// (b) intent+active, no removed, evidence id not in liveIDs — an orphan left
// by a crash or a missed Remove call. CleanupOrphans must tear down the real
// registered worktree and mark it removed.
func TestCleanupOrphans_B_ActiveOrphanNotLive(t *testing.T) {
	root, _, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "b")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, commit2, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if _, err := os.Stat(w.Dir); err != nil {
		t.Fatalf("want worktree dir to exist before cleanup: %v", err)
	}

	// Not live, and Remove was never called: simulates a crash after the
	// task that owned this worktree finished but before it cleaned up.
	if err := CleanupOrphans(root, registryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}

	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Errorf("want orphan worktree dir %q removed, stat err = %v", w.Dir, err)
	}
	lines := readRegistryLines(t, registryPath)
	if len(lines) != 3 || lines[2].Type != "wt_removed" || lines[2].EvidenceID != evidenceID {
		t.Fatalf("registry lines = %+v, want [intent, active, removed]", lines)
	}
	assertNoZombieWorktrees(t, root)

	// A live active worktree must NOT be touched.
	evidenceID2 := evID(t, "b-live")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID2) })
	w2, err := NewWorktree(root, commit2, registryPath, evidenceID2)
	if err != nil {
		t.Fatalf("NewWorktree (live): %v", err)
	}
	defer func() { _ = w2.Remove(root, registryPath) }()

	if err := CleanupOrphans(root, registryPath, map[string]bool{evidenceID2: true}); err != nil {
		t.Fatalf("CleanupOrphans (with live id): %v", err)
	}
	if _, err := os.Stat(w2.Dir); err != nil {
		t.Errorf("want live worktree dir %q to survive cleanup, stat err = %v", w2.Dir, err)
	}
}

// (c) an already-removed evidence id must not be reprocessed (no error, no
// further git calls that could fail on an already-gone directory).
func TestCleanupOrphans_C_AlreadyRemovedSkipped(t *testing.T) {
	root, _, commit2 := initRepoTwoCommits(t)
	registryPath := filepath.Join(t.TempDir(), "registry.jsonl")
	evidenceID := evID(t, "c")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	w, err := NewWorktree(root, commit2, registryPath, evidenceID)
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := w.Remove(root, registryPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if err := CleanupOrphans(root, registryPath, map[string]bool{}); err != nil {
		t.Fatalf("CleanupOrphans on already-removed id: %v", err)
	}
	lines := readRegistryLines(t, registryPath)
	if len(lines) != 3 {
		t.Errorf("registry lines after CleanupOrphans on removed id = %d, want still 3 (no reprocessing)", len(lines))
	}
	assertNoZombieWorktrees(t, root)
}

// openTestRegistry opens the raw journal directly (bypassing
// NewWorktree/Remove) so crash-window tests can hand-stage exactly the
// registry lines a partial NewWorktree run would have left durable.
func openTestRegistry(t *testing.T, path string) *journal.Journal {
	t.Helper()
	reg, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	return reg
}

func mustAppendRecord(t *testing.T, reg *journal.Journal, rec registryRecord) {
	t.Helper()
	if err := appendRecord(reg, rec); err != nil {
		t.Fatalf("append registry record: %v", err)
	}
}
