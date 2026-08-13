// Runner executes an approved TestContract's Command inside a disposable
// worktree and turns the result into a durable, tamper-evident EvidenceRun
// (§3.7/§4). Run is the single frozen entry point; everything else in this
// file is private machinery in service of it:
//
//   - ContextLoader/RunSpec carry only identity + input artifacts. The
//     contract descriptor and oracle declaration are always loaded from the
//     committed plan (never accepted from the caller) so a caller cannot
//     smuggle a Command/OracleDigest that diverges from what gate2 actually
//     approved.
//   - The oracle-surface lineage check (plan.VerifyLineage) and the
//     recomputed OracleSurfaceDigest both run against repoRoot's committed
//     tree, before any worktree exists — a worktree checkout is not itself a
//     trust boundary here, the committed history is.
//   - Command execution never trusts exec.CommandContext's built-in
//     ctx-done kill (that only ever signals the direct child, not its
//     process group): timeout/output-limit enforcement replicates
//     internal/proc's TERM→grace→KILL pattern against the process group
//     explicitly, and unconditionally SIGKILLs the group after Wait returns
//     so a grandchild holding the stdout/stderr pipe write end can never
//     leave the tee readers blocked waiting for EOF.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/plan"
	"github.com/slam0504/sdlc-workbench/internal/spec"
)

const (
	// runnerVersion is the frozen RunnerVersion recorded on every EvidenceRun.
	runnerVersion = "m3a-1"
	// defaultTimeout is RunSpec.Timeout's default (§4).
	defaultTimeout = 10 * time.Minute
	// defaultOutputLimit is RunSpec.OutputLimit's default (§4-2): 4MB.
	defaultOutputLimit = 4 * 1024 * 1024
	// runnerTermGrace mirrors internal/proc.Config's TermGrace default: the
	// window between a process-group SIGTERM and the SIGKILL fallback.
	runnerTermGrace = 5 * time.Second
)

// envAllowlist is the only environment passed through to the executed
// Command (§4): everything else — including any provider token the app
// process itself holds — is dropped rather than inherited via
// append(os.Environ(), ...), which is deliberately NOT used here.
var envAllowlist = []string{"PATH", "HOME", "GOCACHE", "GOPATH", "TMPDIR"}

// testHookAfterStart, when non-nil, is invoked with the just-started
// command's process group id (same value as its PID, since Setpgid makes
// the child its own group leader). It exists solely so runner_test.go can
// assert zero process-group residue after a timeout kill — the same
// groupGone(pgid) convention internal/proc's tests use — without widening
// Run's frozen public signature to expose an internal pgid.
var testHookAfterStart func(pgid int)

// EvidenceRun is the frozen §3.7 evidence-run schema. Every field here is
// either loaded from committed input (never caller-supplied) or computed by
// Run itself; see the package doc for why.
type EvidenceRun struct {
	EvidenceID          string               `json:"evidence_id"`
	Kind                string               `json:"kind"`   // expected_red | negative_control
	Source              string               `json:"source"` // local_app
	BaseCommit          string               `json:"base_commit"`
	TestCommit          string               `json:"test_commit"`
	OracleSurfaceDigest string               `json:"oracle_surface_digest"`
	MutationDigest      string               `json:"mutation_digest,omitempty"`
	Command             plan.Command         `json:"command"`
	CWD                 string               `json:"cwd"` // 邏輯識別："worktree:<evidence_id>"
	StartedAt           string               `json:"started_at"`
	FinishedAt          string               `json:"finished_at"`
	ExitCode            int                  `json:"exit_code"`
	ExpectedFailure     plan.ExpectedFailure `json:"expected_failure"`
	ObservedFailure     string               `json:"observed_failure"`
	StdoutDigest        string               `json:"stdout_digest"`
	StderrDigest        string               `json:"stderr_digest"`
	RecordingRef        string               `json:"recording_ref"`
	RunnerVersion       string               `json:"runner_version"`
	Result              string               `json:"result"` // passed | failed | error
}

// ContextLoader reads the committed plan/risk-policy and oracle declaration
// as of a given commit. Run never trusts a caller-supplied Command or oracle
// digest — it always loads them itself via this interface, at PlanCommit,
// so an evidence run can never diverge from what gate2 approved.
type ContextLoader interface {
	// LoadAt mirrors Task 10's PlanLoader (gatepolicy.PlanLoader).
	LoadAt(commitOID, planID string) (plan.Plan, plan.RiskPolicy, error)
	// LoadOracleAt reads plan/oracle-surface.yaml as committed at commitOID.
	LoadOracleAt(commitOID string) (OracleDecl, error)
}

// RunSpec carries only identity and input artifacts for a single evidence
// run. See ContextLoader's doc comment for why the contract/oracle
// declaration are deliberately absent here.
type RunSpec struct {
	Kind          string // expected_red | negative_control
	PlanID        string
	TaskID        string
	PlanCommit    string
	TestCommit    string
	MutationPatch []byte        // negative_control 必填
	Timeout       time.Duration // 預設 10m
	OutputLimit   int           // 預設 4MB；超限＝result:error（§4-2）
}

// Run executes rs's task contract inside a disposable worktree checked out
// at rs.TestCommit and returns the resulting EvidenceRun. The frozen step
// order (§3.4/§4) is:
//
//  1. Load the approved TestContract (ld.LoadAt) and oracle declaration
//     (ld.LoadOracleAt), both at rs.PlanCommit.
//  2. Verify rs.TestCommit descends from rs.PlanCommit and that every path
//     touched in that range stays within the oracle surface
//     (plan.VerifyLineage with the oracle's Match, rename-safe both sides).
//  3. Recompute OracleSurfaceDigest against rs.TestCommit's committed tree.
//  4. Check out a worktree at rs.TestCommit (cleaned up via defer,
//     including on every error path from here on).
//  5. For negative_control, apply rs.MutationPatch and reject if any
//     touched path (rename-safe both sides) falls inside the oracle surface.
//  6. Run the approved Command with a filtered env, its own process group,
//     and a hard timeout; stdout/stderr are captured (bounded by
//     OutputLimit) and persisted to CAS regardless of outcome.
//  7. Classify the outcome and assemble the EvidenceRun.
func Run(ctx context.Context, repoRoot, casDir, registryPath string, ld ContextLoader, rs RunSpec, ulid func() string, now func() string) (run EvidenceRun, err error) {
	if rs.Kind != "expected_red" && rs.Kind != "negative_control" {
		return EvidenceRun{}, fmt.Errorf("evidence: run: unknown RunSpec.Kind %q", rs.Kind)
	}
	if rs.Kind == "expected_red" && len(rs.MutationPatch) > 0 {
		// A non-empty MutationPatch on an expected_red run would otherwise be
		// silently dropped (only negative_control ever applies it) — the
		// mutation intent must not vanish into a clean "passed" expected_red
		// result. Fail closed instead (review HIGH-1).
		return EvidenceRun{}, fmt.Errorf("evidence: run: expected_red must not carry a MutationPatch (got %d bytes)", len(rs.MutationPatch))
	}
	timeout := rs.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	outputLimit := rs.OutputLimit
	if outputLimit <= 0 {
		outputLimit = defaultOutputLimit
	}

	pl, _, err := ld.LoadAt(rs.PlanCommit, rs.PlanID)
	if err != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: load plan at %s: %w", rs.PlanCommit, err)
	}
	task, ok := findTask(pl, rs.TaskID)
	if !ok {
		return EvidenceRun{}, fmt.Errorf("evidence: run: task %q not found in plan %q at %s", rs.TaskID, rs.PlanID, rs.PlanCommit)
	}
	oracleDecl, err := ld.LoadOracleAt(rs.PlanCommit)
	if err != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: load oracle decl at %s: %w", rs.PlanCommit, err)
	}

	gitRunner := repoGitRunner{root: repoRoot}
	if err := plan.VerifyLineage(gitRunner, rs.PlanCommit, rs.TestCommit, oracleDecl.Match); err != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: %w", err)
	}

	gitRepo := spec.NewGitRepo(repoRoot, oracleDecl.Scope())
	oracleDigest, err := OracleDigestAt(gitRepo, oracleDecl, rs.TestCommit)
	if err != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: recompute oracle surface digest: %w", err)
	}

	evidenceID := ulid()
	w, err := NewWorktree(repoRoot, rs.TestCommit, registryPath, evidenceID)
	if err != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: new worktree: %w", err)
	}
	defer func() {
		if rmErr := w.Remove(repoRoot, registryPath); rmErr != nil && err == nil {
			err = fmt.Errorf("evidence: run: remove worktree: %w", rmErr)
			run = EvidenceRun{}
		}
	}()

	var mutationDigest string
	if rs.Kind == "negative_control" {
		if len(rs.MutationPatch) == 0 {
			return EvidenceRun{}, fmt.Errorf("evidence: run: negative_control requires a non-empty MutationPatch")
		}
		touched, aErr := w.ApplyPatch(rs.MutationPatch)
		if aErr != nil {
			return EvidenceRun{}, fmt.Errorf("evidence: run: apply mutation patch: %w", aErr)
		}
		for _, p := range touched {
			if oracleDecl.Match(p) {
				return EvidenceRun{}, fmt.Errorf("evidence: run: mutation patch touches oracle-surface path %q", p)
			}
		}
		digest, _, cErr := PutCAS(casDir, rs.MutationPatch)
		if cErr != nil {
			return EvidenceRun{}, fmt.Errorf("evidence: run: store mutation patch in CAS: %w", cErr)
		}
		mutationDigest = digest
	}

	startedAt := now()
	exitCode, stdoutBytes, stderrBytes, abortReason, rErr := runCommand(ctx, w.Dir, task.TestContract.Command, timeout, outputLimit)
	finishedAt := now()
	if rErr != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: %w", rErr)
	}

	stdoutDigest, _, cErr := PutCAS(casDir, stdoutBytes)
	if cErr != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: store stdout in CAS: %w", cErr)
	}
	stderrDigest, _, cErr := PutCAS(casDir, stderrBytes)
	if cErr != nil {
		return EvidenceRun{}, fmt.Errorf("evidence: run: store stderr in CAS: %w", cErr)
	}

	var result, observed string
	if abortReason != "" {
		result, observed = "error", abortReason
	} else {
		// A NUL separator sits between the two streams so a matcher
		// characteristic that never appears in stdout or stderr alone can
		// never "appear" by accident wherever stdout's tail happens to
		// complete stderr's head (or vice versa) once naively concatenated
		// (review defense).
		combined := append(append(append([]byte{}, stdoutBytes...), 0x00), stderrBytes...)
		switch rs.Kind {
		case "expected_red":
			result, observed = ClassifyExpectedRed(exitCode, combined, task.TestContract.ExpectedFailure)
		case "negative_control":
			result, observed = ClassifyNegativeControl(exitCode, combined, task.TestContract.ExpectedFailure)
		}
	}

	run = EvidenceRun{
		EvidenceID:          evidenceID,
		Kind:                rs.Kind,
		Source:              "local_app",
		BaseCommit:          rs.PlanCommit,
		TestCommit:          rs.TestCommit,
		OracleSurfaceDigest: oracleDigest,
		MutationDigest:      mutationDigest,
		Command:             task.TestContract.Command,
		CWD:                 "worktree:" + evidenceID,
		StartedAt:           startedAt,
		FinishedAt:          finishedAt,
		ExitCode:            exitCode,
		ExpectedFailure:     task.TestContract.ExpectedFailure,
		ObservedFailure:     observed,
		StdoutDigest:        stdoutDigest,
		StderrDigest:        stderrDigest,
		RecordingRef:        casDir,
		RunnerVersion:       runnerVersion,
		Result:              result,
	}
	return run, nil
}

// EvidenceRunDigest computes run's content-addressed digest: canonical JSON
// (struct field order, same as gate.RecordDigest) SHA-256. This is the
// digest an evidence_run binding and §3.9 content-addressed verification
// both check against.
func EvidenceRunDigest(run EvidenceRun) (string, error) {
	b, err := json.Marshal(run)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// findTask returns the task with the given id, if present.
func findTask(pl plan.Plan, taskID string) (plan.Task, bool) {
	for _, t := range pl.Tasks {
		if t.ID == taskID {
			return t, true
		}
	}
	return plan.Task{}, false
}

// repoGitRunner implements plan.GitRunner over repoRoot, reusing the
// package's runGit helper (worktree.go) instead of duplicating an exec.Cmd
// setup — the *exec.ExitError it wraps stays reachable via errors.As, which
// is what plan.VerifyLineage's exitCoder check relies on.
type repoGitRunner struct{ root string }

func (g repoGitRunner) Git(args ...string) ([]byte, error) {
	return runGit(g.root, nil, args...)
}

// runCommand runs command in dir under its own process group, with a
// filtered environment, and returns the outcome. It never returns a
// non-nil error for anything the command itself did (a bad exit code, a
// timeout, exceeding outputLimit) — those are reported via exitCode/
// abortReason instead, so a real Command that legitimately fails is still a
// classifiable EvidenceRun. err is non-nil only for a runner-level failure
// (e.g. the command could not even be started).
func runCommand(ctx context.Context, dir string, command plan.Command, timeout time.Duration, outputLimit int) (exitCode int, stdout, stderr []byte, abortReason string, err error) {
	cmd := exec.Command(command.Executable, command.Argv...)
	cmd.Dir = dir
	cmd.Env = filteredEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outR, outW, err := os.Pipe()
	if err != nil {
		return 0, nil, nil, "", fmt.Errorf("evidence: run command: stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return 0, nil, nil, "", fmt.Errorf("evidence: run command: stderr pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	if startErr := cmd.Start(); startErr != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return 0, nil, nil, "", fmt.Errorf("evidence: run command: start %v: %w", command, startErr)
	}
	outW.Close() // 父程序不留 write end，否則 EOF 永不到來
	errW.Close()

	pgid := cmd.Process.Pid
	if testHookAfterStart != nil {
		testHookAfterStart(pgid)
	}
	exitedCh := make(chan struct{})
	abort := &abortSignal{pgid: pgid, grace: runnerTermGrace, exited: exitedCh}

	stdoutCap := newLimitedCapture(outputLimit, func() { abort.trigger("output limit exceeded") })
	stderrCap := newLimitedCapture(outputLimit, func() { abort.trigger("output limit exceeded") })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(stdoutCap, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(stderrCap, errR) }()

	go func() {
		select {
		case <-time.After(timeout):
			abort.trigger(fmt.Sprintf("timeout exceeded after %s", timeout))
		case <-ctx.Done():
			abort.trigger("context canceled")
		case <-exitedCh:
		}
	}()

	waitErr := cmd.Wait()
	close(exitedCh)
	// 子程序（直接子行程）一退出即 group SIGKILL：清掉任何持有 pipe write end
	// 的孫程序，保證下面 wg.Wait() 收得到 EOF（同 internal/proc 慣例）。
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	wg.Wait()
	outR.Close()
	errR.Close()

	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	_ = waitErr // 已透過 code／abortReason 表達，不當 runner-level error 回傳

	return code, stdoutCap.Bytes(), stderrCap.Bytes(), abort.Reason(), nil
}

// filteredEnv builds the Command's environment from envAllowlist only —
// never append(os.Environ(), ...), which would leak everything the app
// process holds (including provider tokens) into an executed test command.
func filteredEnv() []string {
	var env []string
	for _, k := range envAllowlist {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// abortSignal is the shared trigger between the timeout watcher and both
// output capturers: whichever fires first records its reason and kills the
// process group exactly once (sync.Once), mirroring internal/proc's
// TERM→grace→KILL Terminate. reason is read only after cmd.Wait() has
// returned in the caller, but is still mutex-guarded because it can be
// written concurrently from a reader goroutine mid-run.
type abortSignal struct {
	pgid   int
	grace  time.Duration
	exited <-chan struct{}

	once   sync.Once
	mu     sync.Mutex
	reason string
}

func (a *abortSignal) trigger(reason string) {
	a.once.Do(func() {
		a.mu.Lock()
		a.reason = reason
		a.mu.Unlock()
		go func() {
			_ = syscall.Kill(-a.pgid, syscall.SIGTERM)
			select {
			case <-a.exited:
			case <-time.After(a.grace):
				_ = syscall.Kill(-a.pgid, syscall.SIGKILL)
			}
		}()
	})
}

func (a *abortSignal) Reason() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reason
}

// limitedCapture is an io.Writer that keeps at most limit bytes and, on the
// write that first crosses that boundary, calls onExceed exactly once. It
// always reports success (len(p), nil) even once over limit — the excess
// bytes are discarded, not buffered, and the copy loop is deliberately left
// running so the pipe keeps draining (see runCommand's doc comment for why
// a stalled drain risks the child blocking forever on write(2)).
type limitedCapture struct {
	mu       sync.Mutex
	buf      []byte
	limit    int
	exceeded bool
	onExceed func()
}

func newLimitedCapture(limit int, onExceed func()) *limitedCapture {
	return &limitedCapture{limit: limit, onExceed: onExceed}
}

func (c *limitedCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	justExceeded := false
	if !c.exceeded {
		remaining := c.limit - len(c.buf)
		switch {
		case remaining <= 0:
			c.exceeded = true
			justExceeded = true
		case len(p) > remaining:
			c.buf = append(c.buf, p[:remaining]...)
			c.exceeded = true
			justExceeded = true
		default:
			c.buf = append(c.buf, p...)
		}
	}
	c.mu.Unlock()
	if justExceeded && c.onExceed != nil {
		c.onExceed()
	}
	return len(p), nil
}

func (c *limitedCapture) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out
}
