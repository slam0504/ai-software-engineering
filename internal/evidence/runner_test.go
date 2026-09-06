package evidence

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
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

// ---- B2c-6：process-group oracle 有界輪詢（B2c-4 裁定記錄 D4：只有 ESRCH
// 算群組消失；EPERM／其他 errno 記錄但繼續輪詢，不偽裝成 gone） ----

// pollGroupGone 每輪先 probe：ESRCH 立即回報 gone；nil／EPERM／其他 errno 記錄
// 進 seq 並計數 EPERM，再判斷 deadline；還有時間才 sleep(min(backoff, remaining))，
// backoff 序列 1/2/4/8/16/32/64 ms 之後固定 64 ms。
//
//nolint:staticcheck // ST1008: 回傳順序（gone, epermCount, last, seq）是 B2c-6 design gate 核准的固定形狀，四套件須逐字一致
func pollGroupGone(probe func() error, deadline time.Time, now func() time.Time, sleep func(time.Duration)) (gone bool, epermCount int, last error, seq []string) {
	const maxBackoff = 64 * time.Millisecond
	backoff := time.Millisecond
	for {
		err := probe()
		last = err
		seq = append(seq, errnoToken(err))
		if errors.Is(err, syscall.ESRCH) {
			return true, epermCount, last, seq
		}
		if errors.Is(err, syscall.EPERM) {
			epermCount++
		}
		if !now().Before(deadline) {
			return false, epermCount, last, seq
		}
		d := backoff
		if remaining := deadline.Sub(now()); d > remaining {
			d = remaining
		}
		sleep(d)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// errnoToken 把 probe 的錯誤正規化成人類可讀 token，供失敗訊息與 table test
// 斷言使用。
func errnoToken(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, syscall.ESRCH):
		return "ESRCH"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.EINVAL):
		return "EINVAL"
	default:
		return err.Error()
	}
}

// groupOracleSnapshot 直接執行 ps（不經 shell），依第二欄 PGID 在 Go 內篩選——
// `ps -g` 在 Linux procps 對純數字選的是 session、非 process group，不可用。
func groupOracleSnapshot(pgid int) string {
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=,stat=,command=").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<ps failed: %v>\n%s", err, out)
	}
	want := strconv.Itoa(pgid)
	var rows []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != want {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("<no rows for pgid %d>", pgid)
	}
	return strings.Join(rows, "\n")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPollGroupGoneTableCases 五案（design gate rev2 D1）：fake clock 只由 fake
// sleep 推進，不真的睡、不忙迴圈。
func TestPollGroupGoneTableCases(t *testing.T) {
	t.Run("eperm_eperm_esrch_gone", func(t *testing.T) {
		errs := []error{syscall.EPERM, syscall.EPERM, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if epermCount != 2 {
			t.Fatalf("epermCount = %d, want 2", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"EPERM", "EPERM", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
		if want := []time.Duration{time.Millisecond, 2 * time.Millisecond}; !equalDurations(sleeps, want) {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	})

	t.Run("nil_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return nil
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		var sleeps []time.Duration
		sleepFn := func(d time.Duration) {
			sleeps = append(sleeps, d)
			clock = clock.Add(d)
		}
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if last != nil {
			t.Fatalf("last = %v, want nil", last)
		}
		wantSleeps := []time.Duration{}
		for _, ms := range []int{1, 2, 4, 8, 16, 32} {
			wantSleeps = append(wantSleeps, time.Duration(ms)*time.Millisecond)
		}
		for range 30 {
			wantSleeps = append(wantSleeps, 64*time.Millisecond)
		}
		wantSleeps = append(wantSleeps, 17*time.Millisecond) // 2000 - (63 + 64*30) = 17 的截短值
		if !equalDurations(sleeps, wantSleeps) {
			t.Fatalf("sleeps = %v (len=%d), want %v (len=%d)", sleeps, len(sleeps), wantSleeps, len(wantSleeps))
		}
		if probeCount != len(tokens) {
			t.Fatalf("probeCount = %d, len(seq) = %d, want equal", probeCount, len(tokens))
		}
		if len(tokens) != 38 {
			t.Fatalf("len(seq) = %d, want 38 (37 sleeps + 1 final probe after deadline)", len(tokens))
		}
		for _, tok := range tokens {
			if tok != "nil" {
				t.Fatalf("seq contains non-nil token %q: %v", tok, tokens)
			}
		}
	})

	t.Run("eperm_expires_at_deadline_not_gone", func(t *testing.T) {
		probeCount := 0
		probe := func() error {
			probeCount++
			return syscall.EPERM
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if gone {
			t.Fatal("want gone=false")
		}
		if !errors.Is(last, syscall.EPERM) {
			t.Fatalf("last = %v, want EPERM", last)
		}
		if epermCount != probeCount {
			t.Fatalf("epermCount = %d, probeCount = %d, want equal", epermCount, probeCount)
		}
		if len(tokens) != probeCount {
			t.Fatalf("len(seq) = %d, probeCount = %d, want equal", len(tokens), probeCount)
		}
	})

	t.Run("immediate_esrch_zero_sleeps", func(t *testing.T) {
		probe := func() error { return syscall.ESRCH }
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepCalls := 0
		sleepFn := func(time.Duration) { sleepCalls++ }
		deadline := clock.Add(2 * time.Second)

		gone, epermCount, last, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if sleepCalls != 0 {
			t.Fatalf("sleepCalls = %d, want 0", sleepCalls)
		}
		if epermCount != 0 {
			t.Fatalf("epermCount = %d, want 0", epermCount)
		}
		if !errors.Is(last, syscall.ESRCH) {
			t.Fatalf("last = %v, want ESRCH", last)
		}
		if want := []string{"ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})

	t.Run("mixed_errno_sequence_normalizes_tokens", func(t *testing.T) {
		errs := []error{nil, syscall.EPERM, syscall.EINVAL, syscall.ESRCH}
		i := 0
		probe := func() error {
			e := errs[i]
			i++
			return e
		}
		clock := time.Unix(0, 0)
		now := func() time.Time { return clock }
		sleepFn := func(d time.Duration) { clock = clock.Add(d) }
		deadline := clock.Add(2 * time.Second)

		gone, _, _, tokens := pollGroupGone(probe, deadline, now, sleepFn)

		if !gone {
			t.Fatal("want gone=true")
		}
		if want := []string{"nil", "EPERM", "EINVAL", "ESRCH"}; !equalStrings(tokens, want) {
			t.Fatalf("seq = %v, want %v", tokens, want)
		}
	})
}

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

// TestRun_OutputMergeSeparatorBlocksCrossStreamMatch guards the merge of
// stdout/stderr Run feeds to the matcher: naively concatenating the two
// streams lets a matcher characteristic that never appeared in either stream
// alone "appear" by accident wherever stdout's tail happens to complete
// stderr's head (or vice versa). The command below writes "...SPL" to stdout
// (no trailing newline) and "IT...\n" to stderr — concatenated without a
// separator that reads as "SPLIT", but the two streams captured/classified
// independently never contain "SPLIT" on their own. A separator byte between
// them must keep this from being classified "passed".
func TestRun_OutputMergeSeparatorBlocksCrossStreamMatch(t *testing.T) {
	root := newRunnerFixtureRepo(t)
	planCommit := headOID(t, root)
	testCommit := commitRunTestSh(t, root, "#!/bin/sh\nprintf 'TestX SPL'\nprintf 'IT\\n' >&2\nexit 1\n")

	casDir, registryPath := runFixtureDirs(t)
	evidenceID := evID(t, "sep")
	t.Cleanup(func() { removeEvidenceDirLeftover(t, evidenceID) })

	pl := plan.Plan{
		PlanID: "P1",
		Tasks: []plan.Task{{
			ID: "T1",
			TestContract: plan.TestContract{
				Command:         plan.Command{Executable: "sh", Argv: []string{"run_test.sh"}},
				ExpectedFailure: plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "SPLIT"},
			},
		}},
	}
	ld := fakeLoader{pl: pl, oracle: testOracle()}
	rs := RunSpec{Kind: "expected_red", PlanID: "P1", TaskID: "T1", PlanCommit: planCommit, TestCommit: testCommit}

	run, err := Run(context.Background(), root, casDir, registryPath, ld, rs, func() string { return evidenceID }, testNow)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Result != "error" {
		t.Fatalf("Result = %q (observed=%q), want error — a separator byte between stdout and stderr must block a match formed only by concatenating stdout's tail (\"...SPL\") with stderr's head (\"IT...\")", run.Result, run.ObservedFailure)
	}
	assertNoZombieWorktrees(t, root)
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
	probe := func() error { return syscall.Kill(-pgid, 0) }
	gone, epermCount, last, seq := pollGroupGone(probe, deadline, time.Now, time.Sleep)
	if !gone {
		t.Errorf("process group %d still alive after Run returned — zero-residue guarantee violated: errno sequence=%v (EPERM=%d, last=%v); ps rows for pgid:\n%s",
			pgid, seq, epermCount, last, groupOracleSnapshot(pgid))
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

	// Non-string field: ExitCode is an int, not a string — a digest that
	// only canonicalizes/hashes string fields (or drops non-string ones)
	// would miss this tamper.
	tampered3 := run
	tampered3.ExitCode = 2
	d5, _ := EvidenceRunDigest(tampered3)
	if d5 == d1 {
		t.Fatal("ExitCode tamper must change digest")
	}
}
