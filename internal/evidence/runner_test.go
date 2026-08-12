package evidence

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// ---- test helpers: real git repo + fake ContextLoader, no mocks for git ----

// newRunnerFixtureRepo creates a real git repo with a single baseline commit
// holding run_test.sh (the task's approved Command target) — the only file
// under the oracle surface used by most runner tests below.
func newRunnerFixtureRepo(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")
	writeFileT(t, root, "run_test.sh", "#!/bin/sh\nexit 0\n")
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "baseline")
	return root
}

// commitRunTestSh commits new run_test.sh content on top of root's current
// HEAD and returns the new commit oid — used to build each scenario's
// TestCommit on top of a shared PlanCommit baseline, keeping the diff
// between them confined to the oracle-declared run_test.sh (required for
// plan.VerifyLineage to accept the range).
func commitRunTestSh(t *testing.T, root, content string) string {
	t.Helper()
	writeFileT(t, root, "run_test.sh", content)
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "test commit")
	return headOID(t, root)
}

func headOID(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(string(mustRunGitT(t, root, "rev-parse", "HEAD")))
}

// buildRenamePatch stages a real `git mv` + `git diff -M` to produce a
// genuine rename patch (mirrors worktree_test.go's rename-patch tests),
// then resets root's worktree back to clean HEAD so the patch can be
// applied fresh inside the evidence worktree the test under it creates.
func buildRenamePatch(t *testing.T, root, oldPath, newPath string) []byte {
	t.Helper()
	runGitT(t, root, "mv", oldPath, newPath)
	patch := mustRunGitT(t, root, "diff", "-M", "HEAD")
	runGitT(t, root, "checkout", "--", ".")
	runGitT(t, root, "clean", "-fd")
	return patch
}

func testPlan() plan.Plan {
	return plan.Plan{
		PlanID: "P1",
		Tasks: []plan.Task{{
			ID: "T1",
			TestContract: plan.TestContract{
				Command:         plan.Command{Executable: "sh", Argv: []string{"run_test.sh"}},
				ExpectedFailure: plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL"},
			},
		}},
	}
}

func testOracle(patterns ...string) OracleDecl {
	if len(patterns) == 0 {
		patterns = []string{"run_test.sh"}
	}
	return OracleDecl{Version: 1, Patterns: patterns}
}

// fakeLoader implements ContextLoader by returning fixed, in-memory values
// regardless of commitOID — Run only ever calls LoadAt/LoadOracleAt with
// rs.PlanCommit, and this package's own tests are only responsible for
// exercising Run's contract, not a real git-backed ContextLoader (that is
// Task 20/21's app-wiring concern, analogous to app.go's appPlanLoader).
type fakeLoader struct {
	pl     plan.Plan
	pol    plan.RiskPolicy
	oracle OracleDecl
}

func (f fakeLoader) LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error) {
	return f.pl, f.pol, nil
}

func (f fakeLoader) LoadOracleAt(commitOID string) (OracleDecl, error) {
	return f.oracle, nil
}

func testNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// groupGone mirrors internal/proc/proc_test.go's convention exactly:
// signal 0 to a process group returns ESRCH once every process in it is
// dead.
func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) != nil }

func runFixtureDirs(t *testing.T) (casDir, registryPath string) {
	t.Helper()
	casDir = filepath.Join(t.TempDir(), "cas")
	registryPath = filepath.Join(t.TempDir(), "registry.jsonl")
	return casDir, registryPath
}

// ---- Step 2: happy path — expected_red reproduces the declared red state ----

func TestRun_ExpectedRed_RedStatePasses(t *testing.T) {
	root := newRunnerFixtureRepo(t)
	planCommit := headOID(t, root)
	testCommit := commitRunTestSh(t, root, "#!/bin/sh\necho 'FAIL: TestX'\nexit 1\n")

	casDir, registryPath := runFixtureDirs(t)
	evidenceID := evID(t, "run")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	ld := fakeLoader{pl: testPlan(), oracle: testOracle()}
	rs := RunSpec{Kind: "expected_red", PlanID: "P1", TaskID: "T1", PlanCommit: planCommit, TestCommit: testCommit}

	run, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Result != "passed" {
		t.Fatalf("Result = %q, want passed (observed=%q)", run.Result, run.ObservedFailure)
	}
	if run.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", run.ExitCode)
	}
	if run.EvidenceID != evidenceID {
		t.Errorf("EvidenceID = %q, want %q", run.EvidenceID, evidenceID)
	}
	if run.CWD != "worktree:"+evidenceID {
		t.Errorf("CWD = %q, want %q", run.CWD, "worktree:"+evidenceID)
	}
	if run.RunnerVersion != "m3a-1" {
		t.Errorf("RunnerVersion = %q, want m3a-1", run.RunnerVersion)
	}
	if run.RecordingRef != casDir {
		t.Errorf("RecordingRef = %q, want %q", run.RecordingRef, casDir)
	}
	if run.BaseCommit != planCommit || run.TestCommit != testCommit {
		t.Errorf("BaseCommit/TestCommit = %q/%q, want %q/%q", run.BaseCommit, run.TestCommit, planCommit, testCommit)
	}
	if run.StdoutDigest == "" {
		t.Error("StdoutDigest must be populated")
	}
	if run.OracleSurfaceDigest == "" {
		t.Error("OracleSurfaceDigest must be populated")
	}
	assertNoZombieWorktrees(t, root)
}

// TestRun_ExpectedRed_RejectsMutationPatch guards review finding HIGH-1: a
// non-empty MutationPatch on an expected_red RunSpec must never be silently
// dropped (only negative_control ever applies a patch) — that would let a
// caller's mutation intent vanish into a clean "passed" expected_red
// result. Run must reject this before touching git/worktree state at all.
func TestRun_ExpectedRed_RejectsMutationPatch(t *testing.T) {
	casDir, registryPath := runFixtureDirs(t)
	rs := RunSpec{
		Kind: "expected_red", PlanID: "P1", TaskID: "T1",
		PlanCommit: "irrelevant", TestCommit: "irrelevant",
		MutationPatch: []byte("diff --git a/x b/x\n"),
	}
	_, err := Run(context.Background(), t.TempDir(), casDir, registryPath, nil, rs, func() string { return "ev" }, testNow)
	if err == nil {
		t.Fatal("Run: want error, expected_red must not silently accept a MutationPatch")
	}
}

// ---- Step 2: timeout — worktree and process group must both be fully cleaned ----

func TestRun_Timeout_KillsProcessGroupAndCleansWorktree(t *testing.T) {
	root := newRunnerFixtureRepo(t)
	planCommit := headOID(t, root)
	testCommit := commitRunTestSh(t, root, "#!/bin/sh\nsleep 60\n")

	var pgid int
	testHookAfterStart = func(p int) { pgid = p }
	t.Cleanup(func() { testHookAfterStart = nil })

	casDir, registryPath := runFixtureDirs(t)
	evidenceID := evID(t, "timeout")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	ld := fakeLoader{pl: testPlan(), oracle: testOracle()}
	rs := RunSpec{Kind: "expected_red", PlanID: "P1", TaskID: "T1", PlanCommit: planCommit, TestCommit: testCommit, Timeout: time.Second}

	start := time.Now()
	run, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want killed promptly after the 1s timeout (sleep 60 must not be allowed to run to completion)", elapsed)
	}
	if run.Result != "error" {
		t.Fatalf("Result = %q, want error", run.Result)
	}
	if !strings.Contains(run.ObservedFailure, "timeout") {
		t.Errorf("ObservedFailure = %q, want it to mention timeout", run.ObservedFailure)
	}

	if pgid == 0 {
		t.Fatal("test hook never observed a process group id")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !groupGone(pgid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !groupGone(pgid) {
		t.Errorf("process group %d still alive after Run returned — zero-residue guarantee violated", pgid)
	}
	assertNoZombieWorktrees(t, root)
}

// ---- Step 2: output limit — a runaway command must be classified error, not left running ----

func TestRun_OutputLimitExceeded_ResultError(t *testing.T) {
	root := newRunnerFixtureRepo(t)
	planCommit := headOID(t, root)
	testCommit := commitRunTestSh(t, root, "#!/bin/sh\ndd if=/dev/zero bs=1024 count=100 2>/dev/null\nexit 1\n")

	casDir, registryPath := runFixtureDirs(t)
	evidenceID := evID(t, "outlimit")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	ld := fakeLoader{pl: testPlan(), oracle: testOracle()}
	rs := RunSpec{Kind: "expected_red", PlanID: "P1", TaskID: "T1", PlanCommit: planCommit, TestCommit: testCommit, OutputLimit: 16}

	run, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Result != "error" {
		t.Fatalf("Result = %q, want error", run.Result)
	}
	if run.ObservedFailure != "output limit exceeded" {
		t.Errorf("ObservedFailure = %q, want %q", run.ObservedFailure, "output limit exceeded")
	}
	assertNoZombieWorktrees(t, root)
}

// ---- Step 2: oracle→non-oracle rename in the plan_commit..test_commit range must be rejected (Task 9 對偶) ----

func TestRun_RejectsOracleToNonOracleRenameInLineage(t *testing.T) {
	root := newRunnerFixtureRepo(t)
	planCommit := headOID(t, root)

	runGitT(t, root, "mv", "run_test.sh", "not_oracle.sh")
	runGitT(t, root, "commit", "-q", "-m", "rename run_test.sh out of oracle scope")
	testCommit := headOID(t, root)

	casDir, registryPath := runFixtureDirs(t)
	ld := fakeLoader{pl: testPlan(), oracle: testOracle()} // oracle = {"run_test.sh"} only
	rs := RunSpec{Kind: "expected_red", PlanID: "P1", TaskID: "T1", PlanCommit: planCommit, TestCommit: testCommit}

	_, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evID(t, "rej") }, testNow)
	if err == nil {
		t.Fatal("Run: want error, rename of the oracle file out of oracle scope must be rejected before any worktree is created")
	}
	assertNoZombieWorktrees(t, root)
}

// ---- Step 2: negative_control mutation patch rename, both directions, both rejected ----

// newMutationRenameFixture builds a baseline with an oracle file
// (run_test.sh) and a non-oracle file (other.txt) both present, and an
// oracle declaration with two exact patterns so a patch can rename into the
// unoccupied oracle path "oracle2.txt" without colliding with an existing
// file.
func newMutationRenameFixture(t *testing.T) (root, commit string, oracle OracleDecl) {
	t.Helper()
	root = t.TempDir()
	runGitT(t, root, "init", "-q")
	runGitT(t, root, "config", "user.email", "t@t.com")
	runGitT(t, root, "config", "user.name", "t")
	writeFileT(t, root, "run_test.sh", "#!/bin/sh\nexit 0\n")
	writeFileT(t, root, "other.txt", strings.Repeat("filler line\n", 20)) // padded for rename detection
	runGitT(t, root, "add", "-A")
	runGitT(t, root, "commit", "-q", "-m", "baseline")
	return root, headOID(t, root), testOracle("run_test.sh", "oracle2.txt")
}

func TestRun_NegativeControl_RejectsMutationRenameOracleToNonOracle(t *testing.T) {
	root, commit, oracle := newMutationRenameFixture(t)
	patch := buildRenamePatch(t, root, "run_test.sh", "other2.sh") // oracle -> non-oracle

	casDir, registryPath := runFixtureDirs(t)
	ld := fakeLoader{pl: testPlan(), oracle: oracle}
	rs := RunSpec{Kind: "negative_control", PlanID: "P1", TaskID: "T1", PlanCommit: commit, TestCommit: commit, MutationPatch: patch}

	evidenceID := evID(t, "mutrename-a")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })
	_, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err == nil {
		t.Fatal("Run: want error, mutation renaming the oracle file out of oracle scope must be rejected")
	}
	assertNoZombieWorktrees(t, root)
}

func TestRun_NegativeControl_RejectsMutationRenameNonOracleToOracle(t *testing.T) {
	root, commit, oracle := newMutationRenameFixture(t)
	patch := buildRenamePatch(t, root, "other.txt", "oracle2.txt") // non-oracle -> oracle

	casDir, registryPath := runFixtureDirs(t)
	ld := fakeLoader{pl: testPlan(), oracle: oracle}
	rs := RunSpec{Kind: "negative_control", PlanID: "P1", TaskID: "T1", PlanCommit: commit, TestCommit: commit, MutationPatch: patch}

	evidenceID := evID(t, "mutrename-b")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })
	_, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err == nil {
		t.Fatal("Run: want error, mutation renaming a non-oracle file into oracle scope must be rejected")
	}
	assertNoZombieWorktrees(t, root)
}

// ---- Step 2: EvidenceRunDigest — deterministic and tamper-evident ----

func TestEvidenceRunDigest_DeterministicAndTamperEvident(t *testing.T) {
	run := EvidenceRun{
		EvidenceID:          "ev1",
		Kind:                "expected_red",
		Source:              "local_app",
		BaseCommit:          strings.Repeat("a", 40),
		TestCommit:          strings.Repeat("b", 40),
		OracleSurfaceDigest: "sha256:" + strings.Repeat("c", 64),
		Command:             plan.Command{Executable: "sh", Argv: []string{"run_test.sh"}},
		CWD:                 "worktree:ev1",
		StartedAt:           "2026-08-12T00:00:00Z",
		FinishedAt:          "2026-08-12T00:00:01Z",
		ExitCode:            1,
		ExpectedFailure:     plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL"},
		ObservedFailure:     "FAIL: TestX",
		StdoutDigest:        "sha256:" + strings.Repeat("d", 64),
		StderrDigest:        "sha256:" + strings.Repeat("e", 64),
		RecordingRef:        "/tmp/cas",
		RunnerVersion:       "m3a-1",
		Result:              "passed",
	}

	d1, err := EvidenceRunDigest(run)
	if err != nil || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("EvidenceRunDigest: %v %q", err, d1)
	}
	d2, _ := EvidenceRunDigest(run)
	if d1 != d2 {
		t.Fatal("digest must be deterministic")
	}

	tampered := run
	tampered.Result = "failed"
	d3, _ := EvidenceRunDigest(tampered)
	if d3 == d1 {
		t.Fatal("Result tamper must change digest")
	}

	tampered2 := run
	tampered2.ObservedFailure = "something else"
	d4, _ := EvidenceRunDigest(tampered2)
	if d4 == d1 {
		t.Fatal("ObservedFailure tamper must change digest")
	}
}
