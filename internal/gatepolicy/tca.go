// TCAPolicy implements gate.GatePolicy for the "test_contract_approval" gate
// (§3.4/§3.9, M3a task-21-brief.md): the second correctness core after
// Gate2Policy. It anchors a task's negative-control-verified TestContract
// evidence to (1) the specific gate2_approval that approved the task's plan,
// (2) the committed test_contract descriptor (Command/ExpectedFailure) at
// that approval's plan_commit, and (3) the two EvidenceRun records
// (expected_red/negative_control) proving the contract fails pre-mutation-
// revert and passes post-revert. Same architecture-freeze rationale as
// Gate2Policy: gatepolicy composes internal/gate (GatePolicy contract),
// internal/plan (Command/ExpectedFailure/Task) and internal/evidence
// (EvidenceRun/Mutation/OracleDecl) — internal/gate itself stays free of any
// domain-package import.
package gatepolicy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/slam0504/sdlc-workbench/internal/evidence"
	"github.com/slam0504/sdlc-workbench/internal/gate"
	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// EvidenceStore is TCA's query port onto the evidence journal/CAS. Both
// methods are content-addressed reads: implementations must re-verify the
// CAS artifacts a record references (stdout/stderr for Get, the patch for
// Mutation) against the record's own digest fields before returning, and
// report any journal-miss or CAS read/verify failure as an error — never a
// digest mismatch silently swallowed. This folds the §3.9 "CAS artifact
// reread" requirement into the two reads TCAPolicy already needs, rather
// than adding a third VerifyArtifacts method to the interface.
type EvidenceStore interface {
	Get(evidenceID string) (evidence.EvidenceRun, error)
	Mutation(mutationID string) (evidence.Mutation, error)
}

// GateReader is TCA's query port onto gate.Service for resolving the
// gate2_approval binding: the approved gate2 ApprovalRecord this test
// contract is anchored to, and its current projection state. gate.Service
// already implements this signature (see service.go's Lookup) — no adapter
// needed at the call site.
type GateReader interface {
	Lookup(approvalID string) (rec *gate.ApprovalRecord, state gate.State, err error)
}

// TCALoader extends Task 10's PlanLoader with LoadOracleAt (evidence.
// ContextLoader's shape): TCA needs both the committed plan (descriptor
// validator, §3.4) and the committed oracle-surface declaration (§3.9's
// continuous oracle recompute, resolved from the plan_commit the gate2
// approval anchors, then checked against the CURRENT worktree — see
// ReconcileBindings). appPlanLoader already implements both methods (it
// doubles as evidence.ContextLoader), so app.go needs no new adapter type.
// Kept as its own interface rather than adding LoadOracleAt to PlanLoader
// itself, so Gate2Policy's existing PlanLoader consumers (incl. its test
// fakes) are untouched.
type TCALoader interface {
	PlanLoader
	LoadOracleAt(commitOID string) (evidence.OracleDecl, error)
}

var (
	reApprovalRef = regexp.MustCompile(`^approval:[0-9A-Z]{26}$`)
	reBareOID     = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)
)

// tcaBindingReq mirrors gate2.go's bindingReq, plus an optional ref-format
// check (nil = ref not format-checked, same laxness gate2's bindingReq has
// for every kind whose ref carries no cross-checked meaning).
type tcaBindingReq struct {
	kind, role string
	digestRe   *regexp.Regexp
	refRe      *regexp.Regexp
}

var tcaBindingReqs = []tcaBindingReq{
	{kind: "gate2_approval", digestRe: reSHA256, refRe: reApprovalRef},
	{kind: "base_commit", digestRe: reGitOID},
	{kind: "oracle_surface", digestRe: reSHA256, refRe: reBareOID},
	{kind: "evidence_run", role: "expected_red", digestRe: reSHA256},
	{kind: "evidence_run", role: "negative_control", digestRe: reSHA256},
	{kind: "mutation", digestRe: reSHA256},
}

// TCAPolicy implements gate.GatePolicy for "test_contract_approval".
type TCAPolicy struct {
	ev     EvidenceStore
	gates  GateReader
	loader TCALoader
	git    plan.GitRunner

	// currentOracleDigest recomputes decl's manifest digest against the
	// CURRENT workspace content (§3.9's "持續重算" class, same current*
	// convention as Gate2Policy) — decl itself is loaded from the gate2
	// approval's plan_commit via loader.LoadOracleAt, never from the
	// worktree. Injected (rather than built from a bare root string inside
	// this package) so gatepolicy stays free of any direct filesystem/git-
	// worktree-scanning dependency — that machinery lives in internal/spec
	// and is wired by app.go, mirroring how Gate2Policy's four current*
	// funcs are constructed there.
	currentOracleDigest func(decl evidence.OracleDecl) (string, error)
}

var _ gate.GatePolicy = (*TCAPolicy)(nil)

// NewTCAPolicy returns the GatePolicy for test_contract_approval. ev/gates
// read evidence/gate2 state; loader reads committed plan/oracle content;
// g runs base_commit's existence check; currentOracleDigest recomputes an
// oracle-surface declaration's digest against the current workspace.
func NewTCAPolicy(ev EvidenceStore, gates GateReader, loader TCALoader, g plan.GitRunner,
	currentOracleDigest func(decl evidence.OracleDecl) (string, error)) gate.GatePolicy {
	return &TCAPolicy{ev: ev, gates: gates, loader: loader, git: g, currentOracleDigest: currentOracleDigest}
}

// ValidateRequest checks the TCA binding schema and the subject shape
// ("task:<plan_id>/<task_id>"). Unlike Gate2Policy, no lineage/plan read
// happens here — the descriptor and gate2 cross-checks are §3.4 decision-
// time business rules (BuildDecision), not request-shape validation.
func (p *TCAPolicy) ValidateRequest(req gate.GateRequest) error {
	if _, _, ok := taskRefFromSubject(req.Subject); !ok {
		return fmt.Errorf("gatepolicy: tca subject must have shape %q, got %q", "task:<plan_id>/<task_id>", req.Subject)
	}
	return validateTCABindings(req.Bindings)
}

// BuildDecision runs the §3.4 consistency validator for an approved
// decision (conditions a-h below); a rejected decision takes no input and
// needs no consistency check (mirrors Gate2Policy).
func (p *TCAPolicy) BuildDecision(req gate.GateRequest, decision string, input gate.DecisionInput) (*gate.Metadata, error) {
	if decision == "rejected" {
		if len(input.RiskSelections) > 0 {
			return nil, fmt.Errorf("gatepolicy: tca rejected decision must not include risk selections")
		}
		return nil, nil
	}
	if decision != "approved" {
		return nil, fmt.Errorf("gatepolicy: tca unknown decision %q", decision)
	}
	if len(input.RiskSelections) > 0 {
		return nil, fmt.Errorf("gatepolicy: tca does not accept risk selections")
	}

	planID, taskID, ok := taskRefFromSubject(req.Subject)
	if !ok {
		return nil, fmt.Errorf("gatepolicy: tca subject must have shape %q, got %q", "task:<plan_id>/<task_id>", req.Subject)
	}

	gate2B, _ := findBinding(req.Bindings, "gate2_approval", "")
	approvalID, aok := strings.CutPrefix(gate2B.Ref, "approval:")
	if !aok || approvalID == "" {
		return nil, fmt.Errorf("gatepolicy: tca gate2_approval ref %q malformed", gate2B.Ref)
	}
	gate2Rec, _, err := p.gates.Lookup(approvalID)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: tca lookup gate2_approval %s: %w", approvalID, err)
	}
	if gate2Rec == nil || gate2Rec.Decision != "approved" {
		return nil, fmt.Errorf("gatepolicy: tca gate2_approval %s is not an approved record", approvalID)
	}
	gate2Digest, err := gate.RecordDigest(*gate2Rec)
	if err != nil {
		return nil, err
	}
	if gate2Digest != gate2B.Digest {
		return nil, fmt.Errorf("gatepolicy: tca gate2_approval binding digest does not match approval record %s", approvalID)
	}

	// §3.0 anchor check: TCA's own base_commit binding must equal the
	// gate2_approval record's own base_commit binding (both are plan_commit).
	tcaBaseB, _ := findBinding(req.Bindings, "base_commit", "")
	gate2BaseDigest := bindingDigest(gate2Rec.Bindings, "base_commit")
	if tcaBaseB.Digest != gate2BaseDigest {
		return nil, fmt.Errorf("gatepolicy: tca base_commit binding does not match gate2_approval %s's base_commit", approvalID)
	}
	planCommit := gitOID(gate2BaseDigest)

	pl, _, err := p.loader.LoadAt(planCommit, planID)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: tca load plan %q at %s: %w", planID, planCommit, err)
	}
	task, ok := taskByID(pl, taskID)
	if !ok {
		return nil, fmt.Errorf("gatepolicy: tca task %q not found in plan %q at %s", taskID, planID, planCommit)
	}
	tc := task.TestContract
	// (g), matcher/test_ids half: fail closed even if the committed
	// descriptor itself is malformed — defense in depth alongside the
	// runner's own fail-closed check (task-21 ledger note).
	if len(tc.ExpectedFailure.TestIDs) == 0 || tc.ExpectedFailure.Matcher == "" {
		return nil, fmt.Errorf("gatepolicy: tca task %q committed test_contract has empty matcher/test_ids", taskID)
	}

	redB, _ := findBinding(req.Bindings, "evidence_run", "expected_red")
	negB, _ := findBinding(req.Bindings, "evidence_run", "negative_control")
	redRun, err := p.ev.Get(redB.Ref)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: tca load expected_red evidence %s: %w", redB.Ref, err)
	}
	negRun, err := p.ev.Get(negB.Ref)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: tca load negative_control evidence %s: %w", negB.Ref, err)
	}
	if redDigest, derr := evidence.EvidenceRunDigest(redRun); derr != nil {
		return nil, derr
	} else if redDigest != redB.Digest {
		return nil, fmt.Errorf("gatepolicy: tca expected_red evidence_run binding digest does not match record %s", redB.Ref)
	}
	if negDigest, derr := evidence.EvidenceRunDigest(negRun); derr != nil {
		return nil, derr
	} else if negDigest != negB.Digest {
		return nil, fmt.Errorf("gatepolicy: tca negative_control evidence_run binding digest does not match record %s", negB.Ref)
	}

	// (a) role <-> EvidenceRun.Kind.
	if redRun.Kind != "expected_red" {
		return nil, fmt.Errorf("gatepolicy: tca expected_red binding's evidence run kind is %q", redRun.Kind)
	}
	if negRun.Kind != "negative_control" {
		return nil, fmt.Errorf("gatepolicy: tca negative_control binding's evidence run kind is %q", negRun.Kind)
	}
	// (b) both passed.
	if redRun.Result != "passed" || negRun.Result != "passed" {
		return nil, fmt.Errorf("gatepolicy: tca both evidence runs must have result \"passed\" (expected_red=%q, negative_control=%q)", redRun.Result, negRun.Result)
	}
	// (c) three-field snapshot consistency.
	if redRun.BaseCommit != negRun.BaseCommit || redRun.TestCommit != negRun.TestCommit || redRun.OracleSurfaceDigest != negRun.OracleSurfaceDigest {
		return nil, fmt.Errorf("gatepolicy: tca expected_red/negative_control base_commit/test_commit/oracle_surface_digest mismatch")
	}
	// §3.0 ancestor-chain re-check (review HIGH finding): the two runs being
	// mutually consistent is not enough — they must also have actually run
	// against THIS gate2_approval's plan_commit. Without this, evidence
	// produced under an older gate2 (a stale code snapshot) would still
	// satisfy every other check (descriptor/oracle unchanged) and pass a TCA
	// anchored to a gate2 that was re-approved at a different plan_commit
	// after the evidence was gathered.
	if redRun.BaseCommit != planCommit {
		return nil, fmt.Errorf("gatepolicy: tca evidence base_commit %q does not match gate2_approval %s's plan_commit %q", redRun.BaseCommit, approvalID, planCommit)
	}
	// (d) oracle_surface.ref == test_commit.
	oracleB, _ := findBinding(req.Bindings, "oracle_surface", "")
	if oracleB.Ref != redRun.TestCommit {
		return nil, fmt.Errorf("gatepolicy: tca oracle_surface binding ref %q does not match test_commit %q", oracleB.Ref, redRun.TestCommit)
	}
	if oracleB.Digest != redRun.OracleSurfaceDigest {
		return nil, fmt.Errorf("gatepolicy: tca oracle_surface binding digest does not match evidence runs' oracle_surface_digest")
	}
	// (e)/(f) mutation digest alignment.
	mutB, _ := findBinding(req.Bindings, "mutation", "")
	mut, err := p.ev.Mutation(mutB.Ref)
	if err != nil {
		return nil, fmt.Errorf("gatepolicy: tca load mutation %s: %w", mutB.Ref, err)
	}
	if mut.Digest != mutB.Digest {
		return nil, fmt.Errorf("gatepolicy: tca mutation binding digest does not match registered mutation %s", mutB.Ref)
	}
	if negRun.MutationDigest != mut.Digest {
		return nil, fmt.Errorf("gatepolicy: tca negative_control evidence run's mutation_digest does not match the mutation binding")
	}
	if redRun.MutationDigest != "" {
		return nil, fmt.Errorf("gatepolicy: tca expected_red evidence run must not carry a mutation_digest")
	}
	// (h) mutation<->task binding (ledger's added eighth condition).
	if mut.TaskRef != planID+"/"+taskID {
		return nil, fmt.Errorf("gatepolicy: tca mutation task_ref %q does not match task %q/%q", mut.TaskRef, planID, taskID)
	}
	// (g) descriptor equality, exact — no partial/normalized comparison.
	if !commandEqual(tc.Command, redRun.Command) || !commandEqual(tc.Command, negRun.Command) {
		return nil, fmt.Errorf("gatepolicy: tca evidence command does not match the committed test_contract command")
	}
	if !expectedFailureEqual(tc.ExpectedFailure, redRun.ExpectedFailure) || !expectedFailureEqual(tc.ExpectedFailure, negRun.ExpectedFailure) {
		return nil, fmt.Errorf("gatepolicy: tca evidence expected_failure does not match the committed test_contract expected_failure")
	}

	return nil, nil
}

// SupersessionKey uses the default gate+"|"+subject formula (same as
// gate1/gate2) — subject already scopes to "task:<plan_id>/<task_id>", so
// approving a TCA request for one task never supersedes an unrelated gate's
// approval or a TCA approval for a different task.
func (p *TCAPolicy) SupersessionKey(gateName, subject string) string {
	return gateName + "|" + subject
}

// ReconcileBindings resolves every TCA binding against continuously-
// recomputed current state:
//   - gate2_approval: RecordDigest recompute + projection active check
//     (either mismatch is a StaleCause — the approval this contract anchors
//     to has drifted or is no longer the live one).
//   - evidence_run x2: EvidenceRunDigest recompute via ev.Get (which itself
//     re-verifies CAS artifacts, fail-closed on any read/verify error).
//   - mutation: content-addressed recompute via ev.Mutation (same fail-
//     closed CAS reread).
//   - oracle_surface: recompute against the CURRENT workspace, using the
//     OracleDecl loaded from the gate2 approval's plan_commit — §3.9's
//     "持續重算": an oracle-surface file edited after approval must go stale
//     even though test_commit's committed tree never changes (A5).
//   - base_commit: existence only (rev-parse), mirrors Gate2Policy — it must
//     NOT go stale just because HEAD moved past it.
//
// Any current-state read error (not a digest/state mismatch) is returned as-
// is: fail closed, never written as a permanent stale cause (§3.9).
func (p *TCAPolicy) ReconcileBindings(rec gate.ApprovalRecord) ([]gate.StaleCause, error) {
	var causes []gate.StaleCause

	if gate2B, ok := findBinding(rec.Bindings, "gate2_approval", ""); ok {
		approvalID, aok := strings.CutPrefix(gate2B.Ref, "approval:")
		if !aok || approvalID == "" {
			return nil, fmt.Errorf("gatepolicy: tca gate2_approval ref %q malformed", gate2B.Ref)
		}
		gate2Rec, state, err := p.gates.Lookup(approvalID)
		if err != nil {
			return nil, err // fail closed
		}
		if gate2Rec == nil {
			return nil, fmt.Errorf("gatepolicy: tca gate2_approval %s has no record", approvalID)
		}
		digest, derr := gate.RecordDigest(*gate2Rec)
		if derr != nil {
			return nil, derr
		}
		if digest != gate2B.Digest {
			causes = append(causes, gate.StaleCause{Cause: "gate2_approval changed", EvidenceRef: digest})
		}
		if state != gate.Active {
			causes = append(causes, gate.StaleCause{Cause: "gate2_approval not active", EvidenceRef: string(state)})
		}
	}

	for _, role := range []string{"expected_red", "negative_control"} {
		b, ok := findBinding(rec.Bindings, "evidence_run", role)
		if !ok {
			continue
		}
		run, err := p.ev.Get(b.Ref)
		if err != nil {
			return nil, err // fail closed — journal miss or CAS artifact tamper
		}
		digest, derr := evidence.EvidenceRunDigest(run)
		if derr != nil {
			return nil, derr
		}
		if digest != b.Digest {
			causes = append(causes, gate.StaleCause{Cause: "evidence_run (" + role + ") changed", EvidenceRef: digest})
		}
	}

	if mutB, ok := findBinding(rec.Bindings, "mutation", ""); ok {
		mut, err := p.ev.Mutation(mutB.Ref)
		if err != nil {
			return nil, err // fail closed
		}
		if mut.Digest != mutB.Digest {
			causes = append(causes, gate.StaleCause{Cause: "mutation changed", EvidenceRef: mut.Digest})
		}
	}

	if oracleB, ok := findBinding(rec.Bindings, "oracle_surface", ""); ok {
		baseDigest := bindingDigest(rec.Bindings, "base_commit")
		if baseDigest == "" {
			return nil, errors.New("gatepolicy: tca record missing base_commit binding")
		}
		decl, err := p.loader.LoadOracleAt(gitOID(baseDigest))
		if err != nil {
			return nil, err // fail closed
		}
		cur, err := p.currentOracleDigest(decl)
		if err != nil {
			return nil, err // fail closed (§3.9 A5: never a permanent stale cause)
		}
		if cur != oracleB.Digest {
			causes = append(causes, gate.StaleCause{Cause: "oracle_surface changed", EvidenceRef: cur})
		}
	}

	if baseDigest := bindingDigest(rec.Bindings, "base_commit"); baseDigest != "" {
		oid := gitOID(baseDigest)
		if _, err := p.git.Git("rev-parse", "--verify", "--quiet", oid+"^{commit}"); err != nil {
			var ec exitCoder
			if !errors.As(err, &ec) || ec.ExitCode() != 1 {
				return nil, err // fatal/unrecognized failure: fail closed, not a stale cause
			}
			causes = append(causes, gate.StaleCause{Cause: "base_commit missing", EvidenceRef: oid})
		}
	}

	return causes, nil
}

// taskRefFromSubject parses "task:<plan_id>/<task_id>" — both halves must be
// non-empty (a missing '/' or an empty plan/task id is malformed).
func taskRefFromSubject(subject string) (planID, taskID string, ok bool) {
	rest, ok := strings.CutPrefix(subject, "task:")
	if !ok {
		return "", "", false
	}
	planID, taskID, ok = strings.Cut(rest, "/")
	if !ok || planID == "" || taskID == "" {
		return "", "", false
	}
	return planID, taskID, true
}

// findBinding returns the (kind,role) binding, if present. Unlike gate2.go's
// bindingDigest (role always "" there), TCA needs role-scoped lookups for
// evidence_run, plus each binding's Ref (not just its digest).
func findBinding(bs []gate.Binding, kind, role string) (gate.Binding, bool) {
	for _, b := range bs {
		if b.Kind == kind && b.Role == role {
			return b, true
		}
	}
	return gate.Binding{}, false
}

func taskByID(pl plan.Plan, taskID string) (plan.Task, bool) {
	for _, t := range pl.Tasks {
		if t.ID == taskID {
			return t, true
		}
	}
	return plan.Task{}, false
}

func commandEqual(a, b plan.Command) bool {
	return a.Executable == b.Executable && slices.Equal(a.Argv, b.Argv)
}

func expectedFailureEqual(a, b plan.ExpectedFailure) bool {
	return a.Matcher == b.Matcher && slices.Equal(a.TestIDs, b.TestIDs)
}

func validateTCABindings(bs []gate.Binding) error {
	seen := map[[2]string]bool{}
	for _, b := range bs {
		key := [2]string{b.Kind, b.Role}
		if seen[key] {
			return fmt.Errorf("gatepolicy: duplicate binding (kind,role): (%q,%q)", b.Kind, b.Role)
		}
		seen[key] = true
	}
	for _, req := range tcaBindingReqs {
		b, found := findBinding(bs, req.kind, req.role)
		if !found {
			return fmt.Errorf("gatepolicy: missing required binding (kind,role): (%q,%q)", req.kind, req.role)
		}
		if !req.digestRe.MatchString(b.Digest) {
			return fmt.Errorf("gatepolicy: binding (%q,%q) digest %q does not match expected pattern", req.kind, req.role, b.Digest)
		}
		if req.refRe != nil && !req.refRe.MatchString(b.Ref) {
			return fmt.Errorf("gatepolicy: binding (%q,%q) ref %q does not match expected pattern", req.kind, req.role, b.Ref)
		}
	}
	return nil
}
